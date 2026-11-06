package wiki

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AGlElectronics/wiki/backend/internal/domain"
	"github.com/AGlElectronics/wiki/backend/internal/httperror"
)

// DuplicateInput places the copy. ParentID nil puts it at the space root;
// Title overrides the root copy's title (default "<source> (copy)").
type DuplicateInput struct {
	ParentID *uuid.UUID `json:"parent_id"`
	Title    *string    `json:"title"`
}

// DuplicatePage copies a page and its live subtree inside its space: new
// page rows (never templates, no grants or restriction carried over) and a
// mapping old → new so the caller can copy the stored documents byte for
// byte. Needs view access on the source and edit access at the
// destination.
//
// Known limitation: uploads are not copied; blocks in the copy keep
// pointing at the source pages' files, which disappear if those pages are
// ever deleted forever.
func (s *Service) DuplicatePage(ctx context.Context, c Caller, srcID uuid.UUID, in DuplicateInput) (Page, map[uuid.UUID]uuid.UUID, error) {
	if in.Title != nil && len(*in.Title) > 300 {
		return Page{}, nil, httperror.Validation("invalid page", map[string]string{"title": "at most 300 characters"})
	}
	mapping := map[uuid.UUID]uuid.UUID{}
	var rootNew uuid.UUID
	err := s.withTree(ctx, nil, srcID, func(tx pgx.Tx, t *tree) error {
		src, _, err := t.visiblePage(srcID, c)
		if err != nil {
			return err
		}
		dl, err := t.destLevel(in.ParentID, c)
		if err != nil {
			return err
		}
		if err := need(dl, domain.LevelEdit); err != nil {
			return err
		}
		if in.ParentID != nil && t.isAncestor(srcID, *in.ParentID) {
			return httperror.Validation("a page cannot be copied under itself", nil)
		}

		// Breadth-first over the live, visible subtree so parents are
		// always inserted before their children.
		type job struct {
			old       *pageRow
			newParent *uuid.UUID
		}
		pos, err := nextPosition(ctx, tx, t.space.ID, in.ParentID)
		if err != nil {
			return err
		}
		queue := []job{{old: src, newParent: in.ParentID}}
		for len(queue) > 0 {
			j := queue[0]
			queue = queue[1:]
			title := j.old.Title
			position := j.old.Position
			if j.old.ID == srcID {
				if in.Title != nil {
					title = strings.TrimSpace(*in.Title)
				} else if !j.old.IsTemplate {
					title = strings.TrimSpace(title + " (copy)")
				}
				position = pos
			}
			var newID uuid.UUID
			if err := tx.QueryRow(ctx, `
				INSERT INTO pages (space_id, parent_id, position, title, icon, kind, created_by, updated_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $7) RETURNING id`,
				t.space.ID, j.newParent, position, title, j.old.Icon, j.old.Kind, c.UserID).Scan(&newID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO page_search (page_id, title, body_text)
				SELECT $1, $2, COALESCE((SELECT body_text FROM page_search WHERE page_id = $3), '')
				ON CONFLICT (page_id) DO NOTHING`, newID, title, j.old.ID); err != nil {
				return err
			}
			mapping[j.old.ID] = newID
			if j.old.ID == srcID {
				rootNew = newID
			}
			for _, p := range t.order {
				if p.ParentID != nil && *p.ParentID == j.old.ID && p.DeletedAt == nil {
					parent := newID
					queue = append(queue, job{old: p, newParent: &parent})
				}
			}
		}
		return nil
	})
	if err != nil {
		return Page{}, nil, err
	}
	if err := s.copyDatabaseData(ctx, mapping); err != nil {
		return Page{}, nil, err
	}
	page, err := s.GetPage(ctx, c, rootNew)
	return page, mapping, err
}

// copyDatabaseData copies properties, views and row values for duplicated
// databases. Property ids are remapped when their database was copied;
// values of rows copied inside the same original database keep their
// property ids (the copy of a single row stays in the same database).
func (s *Service) copyDatabaseData(ctx context.Context, mapping map[uuid.UUID]uuid.UUID) error {
	propMap := map[uuid.UUID]uuid.UUID{}
	for oldDB, newDB := range mapping {
		rows, err := s.pool.Query(ctx, `
			SELECT id, name, type, options, position FROM database_properties WHERE database_id = $1`, oldDB)
		if err != nil {
			return err
		}
		type prop struct {
			id       uuid.UUID
			name     string
			typ      string
			options  []byte
			position float64
		}
		props, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (prop, error) {
			var p prop
			err := r.Scan(&p.id, &p.name, &p.typ, &p.options, &p.position)
			return p, err
		})
		if err != nil {
			return err
		}
		for _, p := range props {
			var newID uuid.UUID
			if err := s.pool.QueryRow(ctx, `
				INSERT INTO database_properties (database_id, name, type, options, position)
				VALUES ($1, $2, $3, $4, $5) RETURNING id`,
				newDB, p.name, p.typ, p.options, p.position).Scan(&newID); err != nil {
				return err
			}
			propMap[p.id] = newID
		}
		if len(props) == 0 {
			continue
		}
	}
	// Views: remap property ids inside the config.
	for oldDB, newDB := range mapping {
		rows, err := s.pool.Query(ctx, `
			SELECT name, kind, config, position FROM database_views WHERE database_id = $1`, oldDB)
		if err != nil {
			return err
		}
		type view struct {
			name, kind string
			config     []byte
			position   float64
		}
		views, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (view, error) {
			var v view
			err := r.Scan(&v.name, &v.kind, &v.config, &v.position)
			return v, err
		})
		if err != nil {
			return err
		}
		for _, v := range views {
			cfg, err := remapViewConfig(v.config, propMap)
			if err != nil {
				return err
			}
			if _, err := s.pool.Exec(ctx, `
				INSERT INTO database_views (database_id, name, kind, config, position) VALUES ($1, $2, $3, $4, $5)`,
				newDB, v.name, v.kind, cfg, v.position); err != nil {
				return err
			}
		}
	}
	// Values of copied rows. A property copied with its database remaps;
	// a row copied alone (parent database not in the mapping) keeps the id.
	for oldPage, newPage := range mapping {
		rows, err := s.pool.Query(ctx, `
			SELECT property_id, value FROM page_property_values WHERE page_id = $1`, oldPage)
		if err != nil {
			return err
		}
		type pv struct {
			prop  uuid.UUID
			value []byte
		}
		values, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pv, error) {
			var v pv
			err := r.Scan(&v.prop, &v.value)
			return v, err
		})
		if err != nil {
			return err
		}
		for _, v := range values {
			prop := v.prop
			if np, ok := propMap[prop]; ok {
				prop = np
			}
			if _, err := s.pool.Exec(ctx, `
				INSERT INTO page_property_values (page_id, property_id, value) VALUES ($1, $2, $3)
				ON CONFLICT (page_id, property_id) DO NOTHING`, newPage, prop, v.value); err != nil {
				return err
			}
		}
	}
	return nil
}
