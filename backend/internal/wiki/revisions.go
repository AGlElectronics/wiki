package wiki

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Revision is one entry in a page's history. The document bytes live in
// S3; GET /api/v1/revisions/{id} streams them for the preview.
type Revision struct {
	ID        uuid.UUID `json:"id"`
	PageID    uuid.UUID `json:"page_id"`
	SizeBytes int64     `json:"size_bytes"`
	// Reason is auto (periodic), restore (the state replaced by a restore)
	// or manual.
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

// ListRevisions returns a page's history, newest first (view access).
func (s *Service) ListRevisions(ctx context.Context, c Caller, pageID uuid.UUID) ([]Revision, error) {
	if _, err := s.GetPage(ctx, c, pageID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, page_id, size_bytes, reason, created_at
		  FROM page_revisions WHERE page_id = $1
		 ORDER BY created_at DESC, id`, pageID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Revision, error) {
		var rev Revision
		err := r.Scan(&rev.ID, &rev.PageID, &rev.SizeBytes, &rev.Reason, &rev.CreatedAt)
		return rev, err
	})
	if out == nil {
		out = []Revision{}
	}
	return out, err
}
