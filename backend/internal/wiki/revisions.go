package wiki

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AGlElectronics/wiki/backend/internal/denlink"
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

// Retention is how long revisions are kept.
type Retention struct {
	// Days: revisions older than this are deleted; 0 keeps every revision.
	Days int `json:"retention_days"`
	// Source is "den" (Den's module settings) or "config"
	// (WIKI_REVISION_RETENTION_DAYS).
	Source string `json:"retention_source"`
}

// SetRevisionRetentionDays sets the local retention (0 keeps everything).
// Set it before serving requests.
func (s *Service) SetRevisionRetentionDays(days int) { s.retentionDays = days }

// SetModuleSettings sets where Den's module settings come from, normally
// the Den link's ModuleSettings method. Set it before serving requests.
func (s *Service) SetModuleSettings(settings func() *denlink.ModuleSettings) {
	s.moduleSettings = settings
}

// RevisionRetention is Den's module setting wiki.revision_retention_days
// (docs/concepts/module-settings.md), when Den sends a whole number of days
// (0 or more); otherwise the local setting.
func (s *Service) RevisionRetention() Retention {
	if s.moduleSettings != nil {
		if days, ok := s.moduleSettings().Days("wiki.revision_retention_days"); ok {
			return Retention{Days: days, Source: "den"}
		}
	}
	return Retention{Days: s.retentionDays, Source: "config"}
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
