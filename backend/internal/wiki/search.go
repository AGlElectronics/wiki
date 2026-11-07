package wiki

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// searchLimit caps what one query returns after access filtering.
const searchLimit = 20

// searchCandidates caps how many database hits are access-checked.
const searchCandidates = 100

// SearchHit is one result of search or backlinks.
type SearchHit struct {
	ID        uuid.UUID `json:"id"`
	SpaceID   uuid.UUID `json:"space_id"`
	SpaceName string    `json:"space_name"`
	Title     string    `json:"title"`
	Icon      string    `json:"icon"`
	// Snippet is a highlighted fragment of the body text ("" for title-only
	// matches and backlinks).
	Snippet string `json:"snippet"`
}

// visibleHits keeps the hits the caller can view, in order, loading each
// space's tree once. Trashed and hidden pages drop out silently.
func (s *Service) visibleHits(ctx context.Context, c Caller, hits []SearchHit, limit int) ([]SearchHit, error) {
	trees := map[uuid.UUID]*tree{}
	out := []SearchHit{}
	for _, hit := range hits {
		if len(out) == limit {
			break
		}
		t, ok := trees[hit.SpaceID]
		if !ok {
			var err error
			t, err = loadTree(ctx, s.pool, hit.SpaceID, false)
			if err != nil {
				return nil, err
			}
			trees[hit.SpaceID] = t
		}
		if _, _, err := t.visiblePage(hit.ID, c); err != nil {
			continue
		}
		hit.SpaceName = t.space.Name
		out = append(out, hit)
	}
	return out, nil
}

// Search finds pages by title or body text, filtered by the caller's
// access. The query is plain words; titles also match as a substring so
// typing part of a word already finds the page.
func (s *Service) Search(ctx context.Context, c Caller, query string) ([]SearchHit, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 200 {
		return []SearchHit{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		WITH q AS (SELECT plainto_tsquery('simple', $1) AS tsq)
		SELECT p.id, p.space_id, p.title, p.icon,
		       CASE WHEN s.tsv @@ q.tsq AND s.body_text <> ''
		            THEN ts_headline('simple', s.body_text, q.tsq, 'MaxWords=14, MinWords=6, MaxFragments=1')
		            ELSE '' END
		  FROM pages p
		  JOIN page_search s ON s.page_id = p.id
		 CROSS JOIN q
		 WHERE p.deleted_at IS NULL
		   AND (p.title ILIKE '%' || $1 || '%' OR s.tsv @@ q.tsq)
		 ORDER BY (p.title ILIKE '%' || $1 || '%') DESC,
		          ts_rank(s.tsv, q.tsq) DESC, p.updated_at DESC
		 LIMIT $2`, query, searchCandidates)
	if err != nil {
		return nil, err
	}
	hits, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (SearchHit, error) {
		var h SearchHit
		err := r.Scan(&h.ID, &h.SpaceID, &h.Title, &h.Icon, &h.Snippet)
		return h, err
	})
	if err != nil {
		return nil, err
	}
	return s.visibleHits(ctx, c, hits, searchLimit)
}

// Backlinks lists the pages that link to this one, filtered by access.
func (s *Service) Backlinks(ctx context.Context, c Caller, pageID uuid.UUID) ([]SearchHit, error) {
	if _, err := s.GetPage(ctx, c, pageID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.space_id, p.title, p.icon
		  FROM page_links l JOIN pages p ON p.id = l.from_page
		 WHERE l.to_page = $1 AND p.deleted_at IS NULL
		 ORDER BY p.updated_at DESC
		 LIMIT $2`, pageID, searchCandidates)
	if err != nil {
		return nil, err
	}
	hits, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (SearchHit, error) {
		var h SearchHit
		err := r.Scan(&h.ID, &h.SpaceID, &h.Title, &h.Icon)
		return h, err
	})
	if err != nil {
		return nil, err
	}
	return s.visibleHits(ctx, c, hits, searchLimit)
}

// Favorite is one entry in the caller's favourites, sidebar-ready.
type Favorite struct {
	PageID  uuid.UUID `json:"page_id"`
	SpaceID uuid.UUID `json:"space_id"`
	Title   string    `json:"title"`
	Icon    string    `json:"icon"`
}

// ListFavorites returns the caller's favourites they can still see.
func (s *Service) ListFavorites(ctx context.Context, c Caller) ([]Favorite, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.space_id, p.title, p.icon
		  FROM favorites f JOIN pages p ON p.id = f.page_id
		 WHERE f.user_id = $1 AND p.deleted_at IS NULL
		 ORDER BY f.position, f.created_at`, c.UserID)
	if err != nil {
		return nil, err
	}
	hits, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (SearchHit, error) {
		var h SearchHit
		err := r.Scan(&h.ID, &h.SpaceID, &h.Title, &h.Icon)
		return h, err
	})
	if err != nil {
		return nil, err
	}
	visible, err := s.visibleHits(ctx, c, hits, len(hits))
	if err != nil {
		return nil, err
	}
	out := make([]Favorite, 0, len(visible))
	for _, h := range visible {
		out = append(out, Favorite{PageID: h.ID, SpaceID: h.SpaceID, Title: h.Title, Icon: h.Icon})
	}
	return out, nil
}

// AddFavorite stars a page the caller can view.
func (s *Service) AddFavorite(ctx context.Context, c Caller, pageID uuid.UUID) error {
	if _, err := s.GetPage(ctx, c, pageID); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO favorites (user_id, page_id, position)
		SELECT $1, $2, COALESCE(max(position), 0) + 1 FROM favorites WHERE user_id = $1
		ON CONFLICT (user_id, page_id) DO NOTHING`, c.UserID, pageID)
	return err
}

// RemoveFavorite unstars a page. Removing a page that is not starred (or
// no longer visible) is not an error.
func (s *Service) RemoveFavorite(ctx context.Context, c Caller, pageID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM favorites WHERE user_id = $1 AND page_id = $2`, c.UserID, pageID)
	return err
}
