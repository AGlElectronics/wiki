package wiki

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AGlElectronics/wiki/backend/internal/domain"
	"github.com/AGlElectronics/wiki/backend/internal/httperror"
)

// Service is safe for concurrent use.
type Service struct {
	pool *pgxpool.Pool
	// onDeleted runs after pages are deleted forever, to drop their content.
	onDeleted func([]uuid.UUID)
}

// NewService constructs the wiki service.
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// OnPagesDeleted registers fn to run after pages are deleted forever
// (directly or with their space). Set it before serving requests.
func (s *Service) OnPagesDeleted(fn func([]uuid.UUID)) { s.onDeleted = fn }

func (s *Service) deleted(ids []uuid.UUID) {
	if s.onDeleted != nil && len(ids) > 0 {
		s.onDeleted(ids)
	}
}

// Space is a space as the caller sees it.
type Space struct {
	ID            uuid.UUID    `json:"id"`
	Kind          string       `json:"kind"`
	Name          string       `json:"name"`
	Icon          string       `json:"icon"`
	Description   string       `json:"description"`
	EveryoneLevel *string      `json:"everyone_level"`
	CreatedAt     time.Time    `json:"created_at"`
	Access        domain.Level `json:"access"`
	// OrphanedAt is set when the owner was offboarded; admins see the
	// space until the sweep deletes it.
	OrphanedAt *time.Time `json:"orphaned_at"`
	// Grants are listed for anyone who can see the space.
	Grants []Grant `json:"grants"`
}

func spaceView(s *spaceRow, c Caller) Space {
	return Space{
		ID: s.ID, Kind: s.Kind, Name: s.Name, Icon: s.Icon, Description: s.Description,
		EveryoneLevel: s.EveryoneLevel, CreatedAt: s.CreatedAt, Access: s.level(c),
		OrphanedAt: s.OrphanedAt, Grants: s.grants,
	}
}

// DefaultOrganization returns the first organization (single-org slice).
func (s *Service) DefaultOrganization(ctx context.Context) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT id FROM organizations ORDER BY slug LIMIT 1`).Scan(&id)
	return id, err
}

// ensurePrivateSpace creates the caller's private space on first use.
func (s *Service) ensurePrivateSpace(ctx context.Context, c Caller) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO spaces (organization_id, kind, owner_id, name, icon, created_by)
		SELECT id, 'private', $1, 'Private', '🔒', $1 FROM organizations ORDER BY slug LIMIT 1
		ON CONFLICT (owner_id) WHERE kind = 'private' DO NOTHING`, c.UserID)
	return err
}

// ListSpaces returns the caller's private space first, then every team
// space they can see, by position. Admins also get orphaned private
// spaces (their owner was offboarded) until the sweep removes them.
func (s *Service) ListSpaces(ctx context.Context, c Caller) ([]Space, error) {
	if err := s.ensurePrivateSpace(ctx, c); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM spaces
		 WHERE kind = 'team' OR owner_id = $1
		    OR (kind = 'private' AND orphaned_at IS NOT NULL AND $2)
		 ORDER BY kind = 'team', orphaned_at IS NOT NULL, position, name`, c.UserID, c.Admin)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	out := []Space{}
	for _, id := range ids {
		sp, err := loadSpace(ctx, s.pool, id, false)
		if err != nil {
			return nil, err
		}
		if v := spaceView(sp, c); v.Access >= domain.LevelView {
			out = append(out, v)
		}
	}
	return out, nil
}

// GetSpace returns one space the caller can see, or a 404.
func (s *Service) GetSpace(ctx context.Context, c Caller, id uuid.UUID) (Space, error) {
	sp, err := loadSpace(ctx, s.pool, id, false)
	if err != nil {
		return Space{}, err
	}
	v := spaceView(sp, c)
	if v.Access < domain.LevelView {
		return Space{}, httperror.NotFound("space")
	}
	return v, nil
}

// SpaceInput creates or updates a team space. EveryoneLevel "" closes the
// space to people without a grant.
type SpaceInput struct {
	Name          *string `json:"name"`
	Icon          *string `json:"icon"`
	Description   *string `json:"description"`
	EveryoneLevel *string `json:"everyone_level"`
}

func (in *SpaceInput) validate(create bool) error {
	issues := map[string]string{}
	if create || in.Name != nil {
		if in.Name == nil || strings.TrimSpace(*in.Name) == "" {
			issues["name"] = "required"
		} else if len(*in.Name) > 120 {
			issues["name"] = "at most 120 characters"
		}
	}
	if in.Icon != nil && len(*in.Icon) > 32 {
		issues["icon"] = "one emoji or a short text"
	}
	if in.EveryoneLevel != nil && *in.EveryoneLevel != "" {
		if _, ok := domain.ParseLevel(*in.EveryoneLevel); !ok {
			issues["everyone_level"] = "view, comment, edit, full or empty"
		}
	}
	if len(issues) > 0 {
		return httperror.Validation("invalid space", issues)
	}
	return nil
}

func nullIfEmpty(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}

// CreateSpace makes a team space; the creator gets full access.
func (s *Service) CreateSpace(ctx context.Context, c Caller, in SpaceInput) (Space, error) {
	if !c.Admin && !c.CreateSpaces {
		return Space{}, httperror.New(httperror.CodeForbidden, "missing permission: "+domain.PermSpaceCreate)
	}
	if err := in.validate(true); err != nil {
		return Space{}, err
	}
	icon, desc := "", ""
	if in.Icon != nil {
		icon = *in.Icon
	}
	if in.Description != nil {
		desc = *in.Description
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Space{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO spaces (organization_id, kind, name, icon, description, everyone_level, created_by, position)
		SELECT o.id, 'team', $1, $2, $3, $4, $5,
		       (SELECT COALESCE(max(position), 0) + 1 FROM spaces WHERE kind = 'team')
		  FROM organizations o ORDER BY o.slug LIMIT 1
		RETURNING id`,
		strings.TrimSpace(*in.Name), icon, desc, nullIfEmpty(in.EveryoneLevel), c.UserID).Scan(&id)
	if err != nil {
		return Space{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO space_grants (space_id, principal_kind, principal, level) VALUES ($1, 'user', $2, 'full')`,
		id, c.UserID.String()); err != nil {
		return Space{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Space{}, err
	}
	return s.GetSpace(ctx, c, id)
}

// spaceFor loads a space and checks the caller's level on it: 404 below
// view, 403 below want.
func (s *Service) spaceFor(ctx context.Context, q querier, c Caller, id uuid.UUID, want domain.Level) (*spaceRow, error) {
	sp, err := loadSpace(ctx, q, id, false)
	if err != nil {
		return nil, err
	}
	l := sp.level(c)
	if l < domain.LevelView {
		return nil, httperror.NotFound("space")
	}
	if err := need(l, want); err != nil {
		return nil, err
	}
	return sp, nil
}

// UpdateSpace applies the non-nil fields (full access).
func (s *Service) UpdateSpace(ctx context.Context, c Caller, id uuid.UUID, in SpaceInput) (Space, error) {
	sp, err := s.spaceFor(ctx, s.pool, c, id, domain.LevelFull)
	if err != nil {
		return Space{}, err
	}
	if err := in.validate(false); err != nil {
		return Space{}, err
	}
	if sp.Kind == domain.SpacePrivate && (in.Name != nil || in.EveryoneLevel != nil) {
		return Space{}, httperror.Validation("a private space keeps its name and stays private", nil)
	}
	var name *string
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		name = &n
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE spaces SET name = COALESCE($2, name), icon = COALESCE($3, icon), description = COALESCE($4, description),
		       everyone_level = CASE WHEN $5::boolean THEN $6 ELSE everyone_level END, updated_at = now()
		 WHERE id = $1`,
		id, name, in.Icon, in.Description, in.EveryoneLevel != nil, nullIfEmpty(in.EveryoneLevel))
	if err != nil {
		return Space{}, err
	}
	return s.GetSpace(ctx, c, id)
}

// DeleteSpace removes a team space with every page in it (full access).
// A private space can only be deleted once its owner was offboarded (the
// admin exported it and does not want to wait for the sweep).
func (s *Service) DeleteSpace(ctx context.Context, c Caller, id uuid.UUID) error {
	sp, err := s.spaceFor(ctx, s.pool, c, id, domain.LevelFull)
	if err != nil {
		return err
	}
	if sp.Kind == domain.SpacePrivate && sp.OrphanedAt == nil {
		return httperror.Conflict("a private space cannot be deleted")
	}
	rows, err := s.pool.Query(ctx, `SELECT id FROM pages WHERE space_id = $1`, id)
	if err != nil {
		return err
	}
	pages, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	if _, err = s.pool.Exec(ctx, `DELETE FROM spaces WHERE id = $1`, id); err != nil {
		return err
	}
	s.deleted(pages)
	return nil
}

// GrantInput adds or changes one grant. Level "" removes it.
type GrantInput struct {
	PrincipalKind string `json:"principal_kind"`
	Principal     string `json:"principal"`
	Level         string `json:"level"`
	// display is the group name as typed; Principal becomes its groupKey.
	display string
}

// validate checks the input and, for people, that they exist. A group name
// is normalised (groupKey); the typed form is kept for display.
func (in *GrantInput) validate(ctx context.Context, q querier) (remove bool, err error) {
	in.Principal = strings.TrimSpace(in.Principal)
	switch in.PrincipalKind {
	case domain.PrincipalUser:
		id, perr := uuid.Parse(in.Principal)
		if perr != nil {
			return false, httperror.Validation("invalid grant", map[string]string{"principal": "a person id"})
		}
		var exists bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, id).Scan(&exists); err != nil {
			return false, err
		}
		if !exists {
			return false, httperror.Validation("invalid grant", map[string]string{"principal": "unknown person"})
		}
		in.Principal = id.String()
	case domain.PrincipalGroup:
		if groupKey(in.Principal) == "" || len(in.Principal) > 200 {
			return false, httperror.Validation("invalid grant", map[string]string{"principal": "a group name"})
		}
		in.display = in.Principal
		in.Principal = groupKey(in.Principal)
	default:
		return false, httperror.Validation("invalid grant", map[string]string{"principal_kind": "user or group"})
	}
	if in.Level == "" {
		return true, nil
	}
	if _, ok := domain.ParseLevel(in.Level); !ok {
		return false, httperror.Validation("invalid grant", map[string]string{"level": "view, comment, edit or full"})
	}
	return false, nil
}

// SetSpaceGrant adds, changes or (level "") removes a grant on a team
// space (full access).
func (s *Service) SetSpaceGrant(ctx context.Context, c Caller, id uuid.UUID, in GrantInput) (Space, error) {
	sp, err := s.spaceFor(ctx, s.pool, c, id, domain.LevelFull)
	if err != nil {
		return Space{}, err
	}
	if sp.Kind == domain.SpacePrivate {
		return Space{}, httperror.Validation("share single pages from a private space, not the whole space", nil)
	}
	remove, err := in.validate(ctx, s.pool)
	if err != nil {
		return Space{}, err
	}
	if remove {
		_, err = s.pool.Exec(ctx, `DELETE FROM space_grants WHERE space_id = $1 AND principal_kind = $2 AND principal = $3`,
			id, in.PrincipalKind, in.Principal)
	} else {
		_, err = s.pool.Exec(ctx, `
			INSERT INTO space_grants (space_id, principal_kind, principal, level, principal_display) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (space_id, principal_kind, principal)
			DO UPDATE SET level = EXCLUDED.level, principal_display = EXCLUDED.principal_display`,
			id, in.PrincipalKind, in.Principal, in.Level, in.display)
	}
	if err != nil {
		return Space{}, err
	}
	sp, err = loadSpace(ctx, s.pool, id, false)
	if err != nil {
		return Space{}, err
	}
	// Removing your own access is allowed; the response then says so.
	return spaceView(sp, c), nil
}

// Person is a users row projected for the share dialog. Email is only
// filled in for wiki admins.
type Person struct {
	ID          uuid.UUID `json:"id"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email,omitempty"`
}

func collectPeople(rows pgx.Rows, withEmail bool) ([]Person, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Person, error) {
		var p Person
		err := r.Scan(&p.ID, &p.DisplayName, &p.Email)
		if !withEmail {
			p.Email = ""
		}
		return p, err
	})
}

// ListPeople returns people who have signed in to the wiki, filtered by
// name. Wiki admins also see and search by email addresses; everyone else
// gets names only, so the picker cannot be used to harvest addresses.
func (s *Service) ListPeople(ctx context.Context, c Caller, query string) ([]Person, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, display_name, COALESCE(email, '') FROM users
		 WHERE issuer <> '' AND ($1 = '' OR display_name ILIKE '%' || $1 || '%' OR ($2 AND email ILIKE '%' || $1 || '%'))
		 ORDER BY display_name LIMIT 50`, strings.TrimSpace(query), c.Admin)
	if err != nil {
		return nil, err
	}
	return collectPeople(rows, c.Admin)
}

// PeopleByIDs returns the given users, for showing comment authors. Email
// addresses as in ListPeople.
func (s *Service) PeopleByIDs(ctx context.Context, c Caller, ids []uuid.UUID) ([]Person, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, display_name, COALESCE(email, '') FROM users WHERE id = ANY($1) LIMIT 100`, ids)
	if err != nil {
		return nil, err
	}
	return collectPeople(rows, c.Admin)
}
