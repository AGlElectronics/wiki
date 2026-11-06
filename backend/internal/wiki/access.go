// Package wiki is the wiki service layer: private and team spaces, the page
// tree with trash, and access through space and page grants. Every method
// takes the Caller and enforces access itself, so handlers cannot forget a
// check; failures are typed httperror values.
//
// Access rules (docs/plan.md, "Access model"):
//
//   - A private space belongs to its owner, who has full access to every
//     page in it. Nobody else sees it, admins included, unless a page in it
//     is shared with them.
//   - A team space grants levels to people and IdP groups, plus optionally
//     a level for everyone signed in. wiki.manage holders have full access
//     to every team space and page, restricted pages included.
//   - A page inherits from its parent (top-level pages from the space) and
//     adds its own grants. A restricted page stops inheritance: only its
//     own grants (and those of its non-restricted descendants) apply.
//   - No view access, or a page in the trash, reads as 404 so the response
//     never confirms a page exists.
package wiki

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/AGlElectronics/wiki/backend/internal/domain"
	"github.com/AGlElectronics/wiki/backend/internal/httperror"
)

// Caller is the person a request acts for.
type Caller struct {
	UserID uuid.UUID
	// Groups are the IdP groups of the current token.
	Groups []string
	// Admin holds wiki.manage.
	Admin bool
	// CreateSpaces holds space.create.
	CreateSpaces bool
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Grant gives a person or an IdP group a level on a space or page.
type Grant struct {
	PrincipalKind string       `json:"principal_kind"`
	Principal     string       `json:"principal"`
	Level         domain.Level `json:"level"`
	// DisplayName is the person's name, or the group name.
	DisplayName string `json:"display_name"`
}

func (g Grant) matches(c Caller) bool {
	switch g.PrincipalKind {
	case domain.PrincipalUser:
		return g.Principal == c.UserID.String()
	case domain.PrincipalGroup:
		return slices.Contains(c.Groups, g.Principal)
	}
	return false
}

func grantsLevel(grants []Grant, c Caller) domain.Level {
	l := domain.LevelNone
	for _, g := range grants {
		if g.matches(c) {
			l = max(l, g.Level)
		}
	}
	return l
}

type spaceRow struct {
	ID            uuid.UUID
	OrgID         uuid.UUID
	Kind          string
	OwnerID       *uuid.UUID
	Name          string
	Icon          string
	Description   string
	EveryoneLevel *string
	CreatedAt     time.Time
	OrphanedAt    *time.Time
	grants        []Grant
}

// override is the access that ignores page restriction: the owner of a
// private space, or an admin in a team space. An orphaned private space
// (its owner was offboarded) opens to admins so they can export it before
// the sweep deletes it.
func (s *spaceRow) override(c Caller) domain.Level {
	if s.Kind == domain.SpacePrivate {
		if s.OwnerID != nil && *s.OwnerID == c.UserID {
			return domain.LevelFull
		}
		if s.OrphanedAt != nil && c.Admin {
			return domain.LevelFull
		}
		return domain.LevelNone
	}
	if c.Admin {
		return domain.LevelFull
	}
	return domain.LevelNone
}

// level is the caller's access to the space itself (and so to top-level
// pages before their own grants).
func (s *spaceRow) level(c Caller) domain.Level {
	l := s.override(c)
	if s.Kind == domain.SpacePrivate {
		return l
	}
	if s.EveryoneLevel != nil {
		if e, ok := domain.ParseLevel(*s.EveryoneLevel); ok {
			l = max(l, e)
		}
	}
	return max(l, grantsLevel(s.grants, c))
}

type pageRow struct {
	ID         uuid.UUID
	SpaceID    uuid.UUID
	ParentID   *uuid.UUID
	Position   float64
	Title      string
	Icon       string
	Kind       string
	Restricted bool
	IsTemplate bool
	CreatedBy  *uuid.UUID
	UpdatedAt  time.Time
	DeletedAt  *time.Time
	grants     []Grant
}

// tree is one space with every page in it (trash included) and all their
// grants, loaded in four queries. Access is computed in memory; spaces are
// small enough that this beats a recursive query per page.
type tree struct {
	space spaceRow
	pages map[uuid.UUID]*pageRow
	order []*pageRow // by parent, then position
}

func loadSpace(ctx context.Context, q querier, spaceID uuid.UUID, lock bool) (*spaceRow, error) {
	sql := `SELECT id, organization_id, kind, owner_id, name, icon, description, everyone_level, created_at, orphaned_at
	          FROM spaces WHERE id = $1`
	if lock {
		sql += ` FOR UPDATE`
	}
	var s spaceRow
	err := q.QueryRow(ctx, sql, spaceID).Scan(&s.ID, &s.OrgID, &s.Kind, &s.OwnerID, &s.Name, &s.Icon, &s.Description, &s.EveryoneLevel, &s.CreatedAt, &s.OrphanedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httperror.NotFound("space")
	}
	if err != nil {
		return nil, err
	}
	s.grants, err = loadGrants(ctx, q, `
		SELECT g.space_id, g.principal_kind, g.principal, g.level, COALESCE(u.display_name, g.principal)
		  FROM space_grants g
		  LEFT JOIN users u ON g.principal_kind = 'user' AND u.id::text = g.principal
		 WHERE g.space_id = $1
		 ORDER BY g.principal_kind DESC, 5`, spaceID, nil)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// loadTree loads a space and its pages. lock takes a row lock on the space
// so concurrent tree changes (moves, trash) in one space serialize.
func loadTree(ctx context.Context, q querier, spaceID uuid.UUID, lock bool) (*tree, error) {
	space, err := loadSpace(ctx, q, spaceID, lock)
	if err != nil {
		return nil, err
	}
	t := &tree{space: *space, pages: map[uuid.UUID]*pageRow{}}
	rows, err := q.Query(ctx, `
		SELECT id, space_id, parent_id, position, title, icon, kind, restricted, is_template, created_by, updated_at, deleted_at
		  FROM pages WHERE space_id = $1
		 ORDER BY parent_id NULLS FIRST, position, created_at`, spaceID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		p := &pageRow{}
		if err := rows.Scan(&p.ID, &p.SpaceID, &p.ParentID, &p.Position, &p.Title, &p.Icon, &p.Kind, &p.Restricted, &p.IsTemplate, &p.CreatedBy, &p.UpdatedAt, &p.DeletedAt); err != nil {
			rows.Close()
			return nil, err
		}
		t.pages[p.ID] = p
		t.order = append(t.order, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_, err = loadGrants(ctx, q, `
		SELECT g.page_id, g.principal_kind, g.principal, g.level, COALESCE(u.display_name, g.principal)
		  FROM page_grants g
		  JOIN pages p ON p.id = g.page_id
		  LEFT JOIN users u ON g.principal_kind = 'user' AND u.id::text = g.principal
		 WHERE p.space_id = $1
		 ORDER BY g.principal_kind DESC, 5`, spaceID, func(owner uuid.UUID, g Grant) {
		if p, ok := t.pages[owner]; ok {
			p.grants = append(p.grants, g)
		}
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// loadGrants runs a grant query whose columns are (owner id, kind,
// principal, level, display name). With add nil it returns the grants;
// otherwise it hands each one to add.
func loadGrants(ctx context.Context, q querier, sql string, arg any, add func(uuid.UUID, Grant)) ([]Grant, error) {
	rows, err := q.Query(ctx, sql, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Grant{}
	for rows.Next() {
		var owner uuid.UUID
		var g Grant
		var level string
		if err := rows.Scan(&owner, &g.PrincipalKind, &g.Principal, &level, &g.DisplayName); err != nil {
			return nil, err
		}
		g.Level, _ = domain.ParseLevel(level)
		if add != nil {
			add(owner, g)
		} else {
			out = append(out, g)
		}
	}
	return out, rows.Err()
}

// level is the caller's access to a page, ignoring the trash.
func (t *tree) level(id uuid.UUID, c Caller) domain.Level {
	l := domain.LevelNone
	for p := t.pages[id]; p != nil; {
		l = max(l, grantsLevel(p.grants, c))
		if p.Restricted {
			return max(l, t.space.override(c))
		}
		if p.ParentID == nil {
			break
		}
		p = t.pages[*p.ParentID]
	}
	return max(l, t.space.level(c))
}

// inTrash reports whether the page or one of its ancestors is in the trash.
func (t *tree) inTrash(id uuid.UUID) bool {
	for p := t.pages[id]; p != nil; {
		if p.DeletedAt != nil {
			return true
		}
		if p.ParentID == nil {
			return false
		}
		p = t.pages[*p.ParentID]
	}
	return false
}

// isAncestor reports whether anc is id itself or one of its ancestors.
func (t *tree) isAncestor(anc, id uuid.UUID) bool {
	for p := t.pages[id]; p != nil; {
		if p.ID == anc {
			return true
		}
		if p.ParentID == nil {
			return false
		}
		p = t.pages[*p.ParentID]
	}
	return false
}

// visiblePage returns a live page the caller may at least view, or a 404.
func (t *tree) visiblePage(id uuid.UUID, c Caller) (*pageRow, domain.Level, error) {
	p, ok := t.pages[id]
	if !ok || t.inTrash(id) {
		return nil, domain.LevelNone, httperror.NotFound("page")
	}
	l := t.level(id, c)
	if l < domain.LevelView {
		return nil, domain.LevelNone, httperror.NotFound("page")
	}
	return p, l, nil
}

// need returns a 403 when have < want. Callers have already checked the
// person may see the thing (otherwise it is a 404).
func need(have, want domain.Level) error {
	if have < want {
		return httperror.New(httperror.CodeForbidden, "needs "+want.String()+" access")
	}
	return nil
}

// canTrash: full access, or edit access on a page one created.
func canTrash(p *pageRow, l domain.Level, c Caller) bool {
	return l >= domain.LevelFull || (l >= domain.LevelEdit && p.CreatedBy != nil && *p.CreatedBy == c.UserID)
}
