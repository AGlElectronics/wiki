package wiki

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AGlElectronics/wiki/backend/internal/domain"
	"github.com/AGlElectronics/wiki/backend/internal/httperror"
)

// ShareLink is one public link's metadata. The token is returned once, on
// creation, and never stored (only its SHA-256).
type ShareLink struct {
	ID              uuid.UUID  `json:"id"`
	PageID          uuid.UUID  `json:"page_id"`
	IncludeChildren bool       `json:"include_children"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       *time.Time `json:"expires_at"`
	RevokedAt       *time.Time `json:"revoked_at"`
	LastUsedAt      *time.Time `json:"last_used_at"`
}

// ShareLinkInput creates a link.
type ShareLinkInput struct {
	IncludeChildren bool `json:"include_children"`
	// ExpiresInDays 0 (or missing) never expires.
	ExpiresInDays int `json:"expires_in_days"`
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateShareLink makes a public link for a page (full access) and returns
// the token exactly once.
func (s *Service) CreateShareLink(ctx context.Context, c Caller, pageID uuid.UUID, in ShareLinkInput) (ShareLink, string, error) {
	l, err := s.pageLevel(ctx, c, pageID)
	if err != nil {
		return ShareLink{}, "", err
	}
	if err := need(l, domain.LevelFull); err != nil {
		return ShareLink{}, "", err
	}
	if in.ExpiresInDays < 0 || in.ExpiresInDays > 3650 {
		return ShareLink{}, "", httperror.Validation("invalid link", map[string]string{"expires_in_days": "0 (never) to 3650"})
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return ShareLink{}, "", err
	}
	token := "wsl_" + base64.RawURLEncoding.EncodeToString(raw)
	var expires *time.Time
	if in.ExpiresInDays > 0 {
		t := time.Now().AddDate(0, 0, in.ExpiresInDays)
		expires = &t
	}
	out := ShareLink{PageID: pageID, IncludeChildren: in.IncludeChildren, ExpiresAt: expires}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO page_share_links (page_id, token_hash, include_children, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at`,
		pageID, hashToken(token), in.IncludeChildren, c.UserID, expires).Scan(&out.ID, &out.CreatedAt)
	if err != nil {
		return ShareLink{}, "", err
	}
	return out, token, nil
}

// ListShareLinks lists a page's links, newest first (full access).
func (s *Service) ListShareLinks(ctx context.Context, c Caller, pageID uuid.UUID) ([]ShareLink, error) {
	l, err := s.pageLevel(ctx, c, pageID)
	if err != nil {
		return nil, err
	}
	if err := need(l, domain.LevelFull); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, page_id, include_children, created_at, expires_at, revoked_at, last_used_at
		  FROM page_share_links WHERE page_id = $1 ORDER BY created_at DESC, id`, pageID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ShareLink, error) {
		var sl ShareLink
		err := r.Scan(&sl.ID, &sl.PageID, &sl.IncludeChildren, &sl.CreatedAt, &sl.ExpiresAt, &sl.RevokedAt, &sl.LastUsedAt)
		return sl, err
	})
	if out == nil {
		out = []ShareLink{}
	}
	return out, err
}

// RevokeShareLink turns a link off for good (full access on its page).
func (s *Service) RevokeShareLink(ctx context.Context, c Caller, linkID uuid.UUID) error {
	var pageID uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT page_id FROM page_share_links WHERE id = $1`, linkID).Scan(&pageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return httperror.NotFound("share link")
	}
	if err != nil {
		return err
	}
	l, err := s.pageLevel(ctx, c, pageID)
	if err != nil {
		return hide(err, "share link")
	}
	if err := need(l, domain.LevelFull); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE page_share_links SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, linkID)
	return err
}

// SharePage is one page as an anonymous visitor sees it.
type SharePage struct {
	ID       uuid.UUID  `json:"id"`
	ParentID *uuid.UUID `json:"parent_id"`
	Title    string     `json:"title"`
	Icon     string     `json:"icon"`
}

// ShareView is what GET /api/public/v1/shares/{token} returns: the root
// page and, when the link includes children, the whole visible subtree.
type ShareView struct {
	Page  SharePage   `json:"page"`
	Pages []SharePage `json:"pages"`
}

// ShareScope is a resolved link: which pages the token may read.
type ShareScope struct {
	LinkID uuid.UUID
	RootID uuid.UUID
	// pages are every readable page id in the scope.
	pages map[uuid.UUID]bool
	view  ShareView
}

// Allows reports whether the scope covers a page.
func (sc *ShareScope) Allows(pageID uuid.UUID) bool { return sc.pages[pageID] }

// View is the share's public listing.
func (sc *ShareScope) View() ShareView { return sc.view }

// ResolveShare turns a presented token into a scope. Unknown, revoked and
// expired tokens, and trashed pages, are all the same 404.
func (s *Service) ResolveShare(ctx context.Context, token string) (*ShareScope, error) {
	var (
		linkID          uuid.UUID
		pageID          uuid.UUID
		includeChildren bool
		expiresAt       *time.Time
		revokedAt       *time.Time
	)
	err := s.pool.QueryRow(ctx, `
		SELECT id, page_id, include_children, expires_at, revoked_at
		  FROM page_share_links WHERE token_hash = $1`, hashToken(token)).
		Scan(&linkID, &pageID, &includeChildren, &expiresAt, &revokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httperror.NotFound("share")
	}
	if err != nil {
		return nil, err
	}
	if revokedAt != nil || (expiresAt != nil && time.Now().After(*expiresAt)) {
		return nil, httperror.NotFound("share")
	}
	t, err := s.pageTree(ctx, s.pool, pageID, false)
	if err != nil {
		return nil, hide(err, "share")
	}
	root, ok := t.pages[pageID]
	if !ok || t.inTrash(pageID) {
		return nil, httperror.NotFound("share")
	}
	sc := &ShareScope{LinkID: linkID, RootID: pageID, pages: map[uuid.UUID]bool{pageID: true}}
	sc.view.Page = SharePage{ID: root.ID, Title: root.Title, Icon: root.Icon}
	sc.view.Pages = []SharePage{}
	if includeChildren {
		for _, p := range t.order {
			if p.ID == pageID || !t.isAncestor(pageID, p.ID) || t.inTrash(p.ID) {
				continue
			}
			sc.pages[p.ID] = true
			sc.view.Pages = append(sc.view.Pages, SharePage{ID: p.ID, ParentID: p.ParentID, Title: p.Title, Icon: p.Icon})
		}
	}
	_, _ = s.pool.Exec(ctx, `UPDATE page_share_links SET last_used_at = now() WHERE id = $1`, linkID)
	return sc, nil
}

// ShareFile returns a file's metadata when its page is inside the scope.
func (s *Service) ShareFile(ctx context.Context, sc *ShareScope, fileID uuid.UUID) (File, string, error) {
	var out File
	var key string
	err := s.pool.QueryRow(ctx, `
		SELECT id, page_id, storage_key, filename, content_type, size_bytes, created_at
		  FROM files WHERE id = $1`, fileID).
		Scan(&out.ID, &out.PageID, &key, &out.Filename, &out.ContentType, &out.SizeBytes, &out.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, "", httperror.NotFound("file")
	}
	if err != nil {
		return File{}, "", err
	}
	if !sc.Allows(out.PageID) {
		return File{}, "", httperror.NotFound("file")
	}
	return out, key, nil
}
