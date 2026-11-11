package wiki

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AGlElectronics/wiki/backend/internal/domain"
	"github.com/AGlElectronics/wiki/backend/internal/httperror"
)

// PageNode is one page in a sidebar tree.
type PageNode struct {
	ID         uuid.UUID    `json:"id"`
	ParentID   *uuid.UUID   `json:"parent_id"`
	Position   float64      `json:"position"`
	Title      string       `json:"title"`
	Icon       string       `json:"icon"`
	Kind       string       `json:"kind"`
	Restricted bool         `json:"restricted"`
	IsTemplate bool         `json:"is_template"`
	Access     domain.Level `json:"access"`
}

// Crumb is one ancestor in a page's breadcrumbs.
type Crumb struct {
	ID    uuid.UUID `json:"id"`
	Title string    `json:"title"`
	Icon  string    `json:"icon"`
}

// Page is one page with what the caller may do with it.
type Page struct {
	PageNode
	SpaceID   uuid.UUID `json:"space_id"`
	SpaceName string    `json:"space_name"`
	SpaceKind string    `json:"space_kind"`
	UpdatedAt time.Time `json:"updated_at"`
	// Breadcrumbs are the ancestors the caller can see, root first.
	Breadcrumbs []Crumb `json:"breadcrumbs"`
	// Grants are the page's own grants (not inherited ones).
	Grants   []Grant `json:"grants"`
	CanTrash bool    `json:"can_trash"`
}

// TrashedPage is a page in a space's trash.
type TrashedPage struct {
	ID        uuid.UUID `json:"id"`
	Title     string    `json:"title"`
	Icon      string    `json:"icon"`
	DeletedAt time.Time `json:"deleted_at"`
}

// SharedPage is a page shared with the caller in a space they cannot
// otherwise see.
type SharedPage struct {
	PageNode
	SpaceID   uuid.UUID `json:"space_id"`
	SpaceName string    `json:"space_name"`
}

func (t *tree) node(p *pageRow, c Caller) PageNode {
	return PageNode{
		ID: p.ID, ParentID: p.ParentID, Position: p.Position, Title: p.Title, Icon: p.Icon,
		Kind: p.Kind, Restricted: p.Restricted, IsTemplate: p.IsTemplate, Access: t.level(p.ID, c),
	}
}

// visibleParent is the nearest ancestor the caller can see, so a page
// under a hidden restricted page still hangs off something visible (or
// the root).
func (t *tree) visibleParent(p *pageRow, c Caller) *uuid.UUID {
	for parent := p.ParentID; parent != nil; {
		pp := t.pages[*parent]
		if pp == nil {
			return nil
		}
		if t.level(pp.ID, c) >= domain.LevelView {
			return parent
		}
		parent = pp.ParentID
	}
	return nil
}

// Tree returns every live page of a space the caller can view. A space the
// caller cannot see at all, and that has no page shared with them, is 404.
func (s *Service) Tree(ctx context.Context, c Caller, spaceID uuid.UUID) ([]PageNode, error) {
	t, err := loadTree(ctx, s.pool, spaceID, false)
	if err != nil {
		return nil, err
	}
	out := []PageNode{}
	for _, p := range t.order {
		if t.inTrash(p.ID) {
			continue
		}
		n := t.node(p, c)
		if n.Access < domain.LevelView {
			continue
		}
		n.ParentID = t.visibleParent(p, c)
		out = append(out, n)
	}
	if len(out) == 0 && t.space.level(c) < domain.LevelView {
		return nil, httperror.NotFound("space")
	}
	return out, nil
}

func (t *tree) page(p *pageRow, l domain.Level, c Caller) Page {
	n := t.node(p, c)
	n.Access = l
	out := Page{
		PageNode: n, SpaceID: t.space.ID, SpaceName: t.space.Name, SpaceKind: t.space.Kind,
		UpdatedAt: p.UpdatedAt, Breadcrumbs: []Crumb{}, Grants: p.grants, CanTrash: canTrash(p, l, c),
	}
	if out.Grants == nil {
		out.Grants = []Grant{}
	}
	for parent := t.visibleParent(p, c); parent != nil; {
		pp := t.pages[*parent]
		out.Breadcrumbs = append([]Crumb{{ID: pp.ID, Title: pp.Title, Icon: pp.Icon}}, out.Breadcrumbs...)
		parent = t.visibleParent(pp, c)
	}
	return out
}

// spaceOf returns the space of a page, or a 404.
func (s *Service) spaceOf(ctx context.Context, q querier, pageID uuid.UUID) (uuid.UUID, error) {
	var spaceID uuid.UUID
	err := q.QueryRow(ctx, `SELECT space_id FROM pages WHERE id = $1`, pageID).Scan(&spaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, httperror.NotFound("page")
	}
	return spaceID, err
}

// pageTree loads the tree around a page.
func (s *Service) pageTree(ctx context.Context, q querier, pageID uuid.UUID, lock bool) (*tree, error) {
	spaceID, err := s.spaceOf(ctx, q, pageID)
	if err != nil {
		return nil, err
	}
	return loadTree(ctx, q, spaceID, lock)
}

// GetPage returns a live page the caller can view.
func (s *Service) GetPage(ctx context.Context, c Caller, id uuid.UUID) (Page, error) {
	t, err := s.pageTree(ctx, s.pool, id, false)
	if err != nil {
		return Page{}, err
	}
	p, l, err := t.visiblePage(id, c)
	if err != nil {
		return Page{}, err
	}
	return t.page(p, l, c), nil
}

// PageInput creates a page or renames one. IsTemplate marks it as a
// template for "new page from template". Kind ("page" or "database") only
// applies on creation; a page never changes kind.
type PageInput struct {
	ParentID   *uuid.UUID `json:"parent_id"`
	Title      *string    `json:"title"`
	Icon       *string    `json:"icon"`
	IsTemplate *bool      `json:"is_template"`
	Kind       *string    `json:"kind"`
}

func validateTitle(title, icon *string) error {
	issues := map[string]string{}
	if title != nil && len(*title) > 300 {
		issues["title"] = "at most 300 characters"
	}
	if icon != nil && len(*icon) > 32 {
		issues["icon"] = "one emoji or a short text"
	}
	if len(issues) > 0 {
		return httperror.Validation("invalid page", issues)
	}
	return nil
}

// withTree runs fn on a locked tree inside a transaction.
func (s *Service) withTree(ctx context.Context, spaceID *uuid.UUID, pageID uuid.UUID, fn func(pgx.Tx, *tree) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var t *tree
	if spaceID != nil {
		t, err = loadTree(ctx, tx, *spaceID, true)
	} else {
		t, err = s.pageTree(ctx, tx, pageID, true)
	}
	if err != nil {
		return err
	}
	if err := fn(tx, t); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// destLevel is the caller's level on a move or create destination: the
// parent page, or the space root.
func (t *tree) destLevel(parentID *uuid.UUID, c Caller) (domain.Level, error) {
	if parentID == nil {
		l := t.space.level(c)
		if l < domain.LevelView {
			return l, httperror.NotFound("space")
		}
		return l, nil
	}
	_, l, err := t.visiblePage(*parentID, c)
	if err != nil {
		return l, httperror.NotFound("parent page")
	}
	return l, nil
}

func nextPosition(ctx context.Context, q querier, spaceID uuid.UUID, parentID *uuid.UUID) (float64, error) {
	var pos float64
	err := q.QueryRow(ctx, `
		SELECT COALESCE(max(position), 0) + 1 FROM pages
		 WHERE space_id = $1 AND parent_id IS NOT DISTINCT FROM $2`, spaceID, parentID).Scan(&pos)
	return pos, err
}

// CreatePage adds a page at the end of its parent (edit access there). A
// kind of "database" creates a Notion-style database with a default table
// view (ADR 0002).
func (s *Service) CreatePage(ctx context.Context, c Caller, spaceID uuid.UUID, in PageInput) (Page, error) {
	if err := validateTitle(in.Title, in.Icon); err != nil {
		return Page{}, err
	}
	kind := "page"
	if in.Kind != nil && *in.Kind != "" {
		kind = *in.Kind
	}
	if kind != "page" && kind != "database" {
		return Page{}, httperror.Validation("invalid page", map[string]string{"kind": "page or database"})
	}
	title, icon := "", ""
	if in.Title != nil {
		title = strings.TrimSpace(*in.Title)
	}
	if in.Icon != nil {
		icon = *in.Icon
	}
	var id uuid.UUID
	err := s.withTree(ctx, &spaceID, uuid.Nil, func(tx pgx.Tx, t *tree) error {
		l, err := t.destLevel(in.ParentID, c)
		if err != nil {
			return err
		}
		if err := need(l, domain.LevelEdit); err != nil {
			return err
		}
		pos, err := nextPosition(ctx, tx, spaceID, in.ParentID)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO pages (space_id, parent_id, position, title, icon, kind, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7) RETURNING id`,
			spaceID, in.ParentID, pos, title, icon, kind, c.UserID).Scan(&id); err != nil {
			return err
		}
		if kind == "database" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO database_views (database_id, name, kind, position) VALUES ($1, 'Table', 'table', 1)`, id); err != nil {
				return err
			}
		}
		// The search row exists from the start; the title keeps it findable
		// before the first compaction writes the body text.
		_, err = tx.Exec(ctx, `
			INSERT INTO page_search (page_id, title) VALUES ($1, $2)
			ON CONFLICT (page_id) DO UPDATE SET title = EXCLUDED.title, updated_at = now()`, id, title)
		return err
	})
	if err != nil {
		return Page{}, err
	}
	return s.GetPage(ctx, c, id)
}

// UpdatePage renames a page or changes its icon (edit access).
func (s *Service) UpdatePage(ctx context.Context, c Caller, id uuid.UUID, in PageInput) (Page, error) {
	if in.ParentID != nil {
		return Page{}, httperror.Validation("use the move endpoint to change the parent", nil)
	}
	if in.Kind != nil {
		return Page{}, httperror.Validation("a page never changes kind", nil)
	}
	if err := validateTitle(in.Title, in.Icon); err != nil {
		return Page{}, err
	}
	t, err := s.pageTree(ctx, s.pool, id, false)
	if err != nil {
		return Page{}, err
	}
	_, l, err := t.visiblePage(id, c)
	if err != nil {
		return Page{}, err
	}
	if err := need(l, domain.LevelEdit); err != nil {
		return Page{}, err
	}
	var title *string
	if in.Title != nil {
		tt := strings.TrimSpace(*in.Title)
		title = &tt
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE pages SET title = COALESCE($2, title), icon = COALESCE($3, icon),
		       is_template = COALESCE($5, is_template), updated_by = $4, updated_at = now()
		 WHERE id = $1`, id, title, in.Icon, c.UserID, in.IsTemplate); err != nil {
		return Page{}, err
	}
	if title != nil {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO page_search (page_id, title) VALUES ($1, $2)
			ON CONFLICT (page_id) DO UPDATE SET title = EXCLUDED.title, updated_at = now()`, id, *title); err != nil {
			return Page{}, err
		}
	}
	return s.GetPage(ctx, c, id)
}

// MoveInput puts a page under ParentID (nil = space root) at Position
// (nil = at the end).
type MoveInput struct {
	ParentID *uuid.UUID `json:"parent_id"`
	Position *float64   `json:"position"`
}

// MovePage moves a page within its space: edit access on the page and on
// the destination, and never under itself.
func (s *Service) MovePage(ctx context.Context, c Caller, id uuid.UUID, in MoveInput) (Page, error) {
	err := s.withTree(ctx, nil, id, func(tx pgx.Tx, t *tree) error {
		_, l, err := t.visiblePage(id, c)
		if err != nil {
			return err
		}
		if err := need(l, domain.LevelEdit); err != nil {
			return err
		}
		if in.ParentID != nil {
			if _, ok := t.pages[*in.ParentID]; !ok {
				return httperror.Validation("the new parent must be a page in the same space", nil)
			}
			if t.isAncestor(id, *in.ParentID) {
				return httperror.Validation("a page cannot move under itself", nil)
			}
		}
		dl, err := t.destLevel(in.ParentID, c)
		if err != nil {
			return err
		}
		if err := need(dl, domain.LevelEdit); err != nil {
			return err
		}
		pos := 0.0
		if in.Position != nil {
			pos = *in.Position
		} else if pos, err = nextPosition(ctx, tx, t.space.ID, in.ParentID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE pages SET parent_id = $2, position = $3, updated_at = now() WHERE id = $1`,
			id, in.ParentID, pos)
		return err
	})
	if err != nil {
		return Page{}, err
	}
	return s.GetPage(ctx, c, id)
}

// TrashPage moves a page and everything under it to the space's trash
// (full access, or edit on a page one created).
func (s *Service) TrashPage(ctx context.Context, c Caller, id uuid.UUID) error {
	return s.withTree(ctx, nil, id, func(tx pgx.Tx, t *tree) error {
		p, l, err := t.visiblePage(id, c)
		if err != nil {
			return err
		}
		if !canTrash(p, l, c) {
			return httperror.New(httperror.CodeForbidden, "needs full access, or edit access on a page you created")
		}
		_, err = tx.Exec(ctx, `UPDATE pages SET deleted_at = now(), deleted_by = $2 WHERE id = $1`, id, c.UserID)
		return err
	})
}

// Trash lists the trashed pages of a space the caller could trash.
func (s *Service) Trash(ctx context.Context, c Caller, spaceID uuid.UUID) ([]TrashedPage, error) {
	t, err := loadTree(ctx, s.pool, spaceID, false)
	if err != nil {
		return nil, err
	}
	out := []TrashedPage{}
	for _, p := range t.order {
		if p.DeletedAt == nil || !canTrash(p, t.level(p.ID, c), c) {
			continue
		}
		out = append(out, TrashedPage{ID: p.ID, Title: p.Title, Icon: p.Icon, DeletedAt: *p.DeletedAt})
	}
	if len(out) == 0 && t.space.level(c) < domain.LevelView {
		return nil, httperror.NotFound("space")
	}
	return out, nil
}

// trashedPage returns a page that was itself moved to the trash and that
// the caller may restore or delete.
func (t *tree) trashedPage(id uuid.UUID, c Caller) (*pageRow, error) {
	p, ok := t.pages[id]
	if !ok || p.DeletedAt == nil {
		return nil, httperror.NotFound("trashed page")
	}
	l := t.level(id, c)
	if l < domain.LevelView {
		return nil, httperror.NotFound("trashed page")
	}
	if !canTrash(p, l, c) {
		return nil, httperror.New(httperror.CodeForbidden, "needs full access, or edit access on a page you created")
	}
	return p, nil
}

// RestorePage takes a page out of the trash. If its parent is itself in
// the trash, it comes back at the space root.
func (s *Service) RestorePage(ctx context.Context, c Caller, id uuid.UUID) (Page, error) {
	err := s.withTree(ctx, nil, id, func(tx pgx.Tx, t *tree) error {
		p, err := t.trashedPage(id, c)
		if err != nil {
			return err
		}
		parent := p.ParentID
		if parent != nil && t.inTrash(*parent) {
			parent = nil
		}
		_, err = tx.Exec(ctx, `
			UPDATE pages SET deleted_at = NULL, deleted_by = NULL, parent_id = $2,
			       position = CASE WHEN $2::uuid IS DISTINCT FROM parent_id
			                       THEN (SELECT COALESCE(max(position), 0) + 1 FROM pages WHERE space_id = $3 AND parent_id IS NULL)
			                       ELSE position END
			 WHERE id = $1`, id, parent, t.space.ID)
		return err
	})
	if err != nil {
		return Page{}, err
	}
	return s.GetPage(ctx, c, id)
}

// DeletePage permanently deletes a trashed page and everything under it.
func (s *Service) DeletePage(ctx context.Context, c Caller, id uuid.UUID) error {
	var gone []uuid.UUID
	err := s.withTree(ctx, nil, id, func(tx pgx.Tx, t *tree) error {
		if _, err := t.trashedPage(id, c); err != nil {
			return err
		}
		if err := need(t.level(id, c), domain.LevelFull); err != nil {
			return err
		}
		for _, p := range t.order {
			if t.isAncestor(id, p.ID) {
				gone = append(gone, p.ID)
			}
		}
		if err := s.queueDeletion(ctx, tx, gone); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM pages WHERE id = $1`, id)
		return err
	})
	if err != nil {
		return err
	}
	s.deleted(gone)
	return nil
}

// SetPageGrant adds, changes or (level "") removes a grant on a page (full
// access).
func (s *Service) SetPageGrant(ctx context.Context, c Caller, id uuid.UUID, in GrantInput) (Page, error) {
	err := s.withTree(ctx, nil, id, func(tx pgx.Tx, t *tree) error {
		_, l, err := t.visiblePage(id, c)
		if err != nil {
			return err
		}
		if err := need(l, domain.LevelFull); err != nil {
			return err
		}
		remove, err := in.validate(ctx, tx)
		if err != nil {
			return err
		}
		if remove {
			_, err = tx.Exec(ctx, `DELETE FROM page_grants WHERE page_id = $1 AND principal_kind = $2 AND principal = $3`,
				id, in.PrincipalKind, in.Principal)
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO page_grants (page_id, principal_kind, principal, level, principal_display) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (page_id, principal_kind, principal)
			DO UPDATE SET level = EXCLUDED.level, principal_display = EXCLUDED.principal_display`,
			id, in.PrincipalKind, in.Principal, in.Level, in.display)
		return err
	})
	if err != nil {
		return Page{}, err
	}
	return s.pageOrGone(ctx, c, id)
}

// SetRestricted turns inheritance off or on (full access). Restricting
// keeps the caller in: if they would lose full access, they get a full
// grant on the page first.
func (s *Service) SetRestricted(ctx context.Context, c Caller, id uuid.UUID, restricted bool) (Page, error) {
	err := s.withTree(ctx, nil, id, func(tx pgx.Tx, t *tree) error {
		p, l, err := t.visiblePage(id, c)
		if err != nil {
			return err
		}
		if err := need(l, domain.LevelFull); err != nil {
			return err
		}
		if restricted && !p.Restricted {
			p.Restricted = true
			if t.level(id, c) < domain.LevelFull {
				if _, err := tx.Exec(ctx, `
					INSERT INTO page_grants (page_id, principal_kind, principal, level) VALUES ($1, 'user', $2, 'full')
					ON CONFLICT (page_id, principal_kind, principal) DO UPDATE SET level = 'full'`,
					id, c.UserID.String()); err != nil {
					return err
				}
			}
		}
		_, err = tx.Exec(ctx, `UPDATE pages SET restricted = $2, updated_at = now() WHERE id = $1`, id, restricted)
		return err
	})
	if err != nil {
		return Page{}, err
	}
	return s.GetPage(ctx, c, id)
}

// pageOrGone returns the page, or an empty page with access "none" when
// the caller just removed their own access.
func (s *Service) pageOrGone(ctx context.Context, c Caller, id uuid.UUID) (Page, error) {
	p, err := s.GetPage(ctx, c, id)
	var he *httperror.Error
	if errors.As(err, &he) && he.Code == httperror.CodeNotFound {
		return Page{PageNode: PageNode{ID: id}, Breadcrumbs: []Crumb{}, Grants: []Grant{}}, nil
	}
	return p, err
}

// Shared lists pages shared with the caller (by person or group) in
// spaces they cannot otherwise see, topmost first.
func (s *Service) Shared(ctx context.Context, c Caller) ([]SharedPage, error) {
	groups := groupKeys(c.Groups)
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT p.space_id FROM page_grants g JOIN pages p ON p.id = g.page_id
		 WHERE (g.principal_kind = 'user' AND g.principal = $1)
		    OR (g.principal_kind = 'group' AND g.principal = ANY($2))`, c.UserID.String(), groups)
	if err != nil {
		return nil, err
	}
	spaceIDs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	out := []SharedPage{}
	for _, spaceID := range spaceIDs {
		t, err := loadTree(ctx, s.pool, spaceID, false)
		if err != nil {
			return nil, err
		}
		if t.space.level(c) >= domain.LevelView {
			continue
		}
		for _, p := range t.order {
			if t.inTrash(p.ID) || t.level(p.ID, c) < domain.LevelView || t.visibleParent(p, c) != nil {
				continue
			}
			n := t.node(p, c)
			n.ParentID = nil
			out = append(out, SharedPage{PageNode: n, SpaceID: t.space.ID, SpaceName: t.space.Name})
		}
	}
	return out, nil
}
