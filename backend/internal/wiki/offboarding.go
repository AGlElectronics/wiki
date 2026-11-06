package wiki

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Offboarding (decision WIKI-13): the private space of a deleted account
// is handed to the wiki admins (stamped orphaned_at, visible to them with
// a banner), then removed by the sweep after the grace period. HR calls
// OrphanPrivateSpaces through /api/internal/offboarded.

// OrphanPrivateSpaces stamps the private spaces of every user with this
// IdP subject (idempotent; an unknown subject is fine — the person may
// never have opened the wiki).
func (s *Service) OrphanPrivateSpaces(ctx context.Context, subject string) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE spaces SET orphaned_at = now(), updated_at = now()
		 WHERE kind = 'private' AND orphaned_at IS NULL
		   AND owner_id IN (SELECT id FROM users WHERE subject = $1)`, subject)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// SweepOrphaned deletes orphaned private spaces older than the grace
// period, with every page in them (the live manager removes the content).
// It returns how many spaces went.
func (s *Service) SweepOrphaned(ctx context.Context, grace time.Duration) (int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM spaces WHERE kind = 'private' AND orphaned_at IS NOT NULL AND orphaned_at < $1`,
		time.Now().Add(-grace))
	if err != nil {
		return 0, err
	}
	spaces, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	for _, spaceID := range spaces {
		prows, err := s.pool.Query(ctx, `SELECT id FROM pages WHERE space_id = $1`, spaceID)
		if err != nil {
			return 0, err
		}
		pages, err := pgx.CollectRows(prows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return 0, err
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM spaces WHERE id = $1`, spaceID); err != nil {
			return 0, err
		}
		s.deleted(pages)
	}
	return len(spaces), nil
}
