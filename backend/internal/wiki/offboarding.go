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

// OrphanPrivateSpaces stamps the private spaces of the user with this IdP
// issuer and subject (idempotent; an unknown person is fine — they may
// never have opened the wiki). Issuers compare without a trailing "/".
// An empty issuer matches the subject under any issuer; the handler logs
// that, because a subject is only unique within one issuer.
func (s *Service) OrphanPrivateSpaces(ctx context.Context, issuer, subject string) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE spaces SET orphaned_at = now(), updated_at = now()
		 WHERE kind = 'private' AND orphaned_at IS NULL
		   AND owner_id IN (SELECT id FROM users
		                     WHERE subject = $2 AND ($1 = '' OR rtrim(issuer, '/') = rtrim($1, '/')))`,
		issuer, subject)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// SweepOrphaned deletes orphaned private spaces older than the grace
// period, with every page in them, in one transaction that also queues
// the pages' stored content for deletion (content_deletions). It returns
// how many spaces went.
func (s *Service) SweepOrphaned(ctx context.Context, grace time.Duration) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	rows, err := tx.Query(ctx, `
		SELECT id FROM spaces WHERE kind = 'private' AND orphaned_at IS NOT NULL AND orphaned_at < $1
		 FOR UPDATE`, time.Now().Add(-grace))
	if err != nil {
		return 0, err
	}
	spaces, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil || len(spaces) == 0 {
		return 0, err
	}
	rows, err = tx.Query(ctx, `SELECT id FROM pages WHERE space_id = ANY($1)`, spaces)
	if err != nil {
		return 0, err
	}
	pages, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	if err := s.queueDeletion(ctx, tx, pages); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM spaces WHERE id = ANY($1)`, spaces); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	s.deleted(pages)
	return len(spaces), nil
}
