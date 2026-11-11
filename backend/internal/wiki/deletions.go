package wiki

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Content of pages deleted forever lives in S3. Whoever deletes page rows
// queues the pages in content_deletions in the same transaction; the live
// manager removes the queue row once the objects are gone (ContentDeleted)
// and RetryContentDeletions hands it what is still queued.

// queueDeletion records pages whose stored content must go. Without object
// storage (no delete hook) there is nothing to delete.
func (s *Service) queueDeletion(ctx context.Context, q querier, pages []uuid.UUID) error {
	if s.onDeleted == nil || len(pages) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `
		INSERT INTO content_deletions (page_id) SELECT unnest($1::uuid[])
		ON CONFLICT (page_id) DO NOTHING`, pages)
	return err
}

// ContentDeleted marks a page's stored content as gone.
func (s *Service) ContentDeleted(ctx context.Context, page uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM content_deletions WHERE page_id = $1`, page)
	return err
}

// ContentDeletionFailed records a failed attempt; the page stays queued.
func (s *Service) ContentDeletionFailed(ctx context.Context, page uuid.UUID, cause error) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE content_deletions SET attempts = attempts + 1, last_error = left($2, 500) WHERE page_id = $1`,
		page, cause.Error())
	return err
}

// RetryContentDeletions hands pages queued for longer than minAge back to
// the delete hook and returns how many. minAge leaves the first attempt
// (right after the delete) time to finish.
func (s *Service) RetryContentDeletions(ctx context.Context, minAge time.Duration) (int, error) {
	if s.onDeleted == nil {
		return 0, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT page_id FROM content_deletions WHERE queued_at < $1 ORDER BY queued_at LIMIT 500`,
		time.Now().Add(-minAge))
	if err != nil {
		return 0, err
	}
	pages, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	s.deleted(pages)
	return len(pages), nil
}
