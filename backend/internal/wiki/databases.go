package wiki

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AGlElectronics/wiki/backend/internal/domain"
	"github.com/AGlElectronics/wiki/backend/internal/httperror"
)

// Notion-style databases (ADR 0002): a database is a page with kind
// 'database'; rows are its direct child pages.

// PropertyTypes are the supported column types.
var PropertyTypes = []string{"text", "number", "select", "multi_select", "date", "person", "checkbox", "url"}

// Property is one column of a database.
type Property struct {
	ID         uuid.UUID       `json:"id"`
	DatabaseID uuid.UUID       `json:"database_id"`
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	Options    json.RawMessage `json:"options"`
	Position   float64         `json:"position"`
}

// View is one saved way of looking at a database.
type View struct {
	ID         uuid.UUID       `json:"id"`
	DatabaseID uuid.UUID       `json:"database_id"`
	Name       string          `json:"name"`
	Kind       string          `json:"kind"`
	Config     json.RawMessage `json:"config"`
	Position   float64         `json:"position"`
}

// viewConfig is the validated shape of View.Config.
type viewConfig struct {
	Filter []struct {
		Property uuid.UUID       `json:"property"`
		Equals   json.RawMessage `json:"equals"`
	} `json:"filter"`
	Sort []struct {
		Property uuid.UUID `json:"property"`
		Desc     bool      `json:"desc"`
	} `json:"sort"`
	GroupBy      *uuid.UUID `json:"group_by"`
	DateProperty *uuid.UUID `json:"date_property"`
}

// remapViewConfig rewrites property ids after a database duplication.
func remapViewConfig(raw []byte, propMap map[uuid.UUID]uuid.UUID) ([]byte, error) {
	if len(raw) == 0 || string(raw) == "{}" || string(raw) == "null" {
		return raw, nil
	}
	var cfg viewConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return raw, nil //nolint:nilerr // an odd config copies verbatim
	}
	for i := range cfg.Filter {
		if np, ok := propMap[cfg.Filter[i].Property]; ok {
			cfg.Filter[i].Property = np
		}
	}
	for i := range cfg.Sort {
		if np, ok := propMap[cfg.Sort[i].Property]; ok {
			cfg.Sort[i].Property = np
		}
	}
	if cfg.GroupBy != nil {
		if np, ok := propMap[*cfg.GroupBy]; ok {
			cfg.GroupBy = &np
		}
	}
	if cfg.DateProperty != nil {
		if np, ok := propMap[*cfg.DateProperty]; ok {
			cfg.DateProperty = &np
		}
	}
	return json.Marshal(cfg)
}

// databaseLevel checks the page exists, is a database and is visible; it
// returns the caller's level on it.
func (s *Service) databaseLevel(ctx context.Context, c Caller, dbID uuid.UUID) (domain.Level, error) {
	t, err := s.pageTree(ctx, s.pool, dbID, false)
	if err != nil {
		return domain.LevelNone, err
	}
	p, l, err := t.visiblePage(dbID, c)
	if err != nil {
		return domain.LevelNone, err
	}
	if p.Kind != "database" {
		return domain.LevelNone, httperror.Validation("not a database page", nil)
	}
	return l, nil
}

// Database is a database page's schema: its columns and views.
type Database struct {
	Properties []Property `json:"properties"`
	Views      []View     `json:"views"`
}

// GetDatabase returns the columns and views (view access).
func (s *Service) GetDatabase(ctx context.Context, c Caller, dbID uuid.UUID) (Database, error) {
	if _, err := s.databaseLevel(ctx, c, dbID); err != nil {
		return Database{}, err
	}
	out := Database{Properties: []Property{}, Views: []View{}}
	rows, err := s.pool.Query(ctx, `
		SELECT id, database_id, name, type, options, position
		  FROM database_properties WHERE database_id = $1 ORDER BY position, created_at`, dbID)
	if err != nil {
		return out, err
	}
	out.Properties, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Property, error) {
		var p Property
		err := r.Scan(&p.ID, &p.DatabaseID, &p.Name, &p.Type, &p.Options, &p.Position)
		return p, err
	})
	if err != nil {
		return out, err
	}
	rows, err = s.pool.Query(ctx, `
		SELECT id, database_id, name, kind, config, position
		  FROM database_views WHERE database_id = $1 ORDER BY position, created_at`, dbID)
	if err != nil {
		return out, err
	}
	out.Views, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (View, error) {
		var v View
		err := r.Scan(&v.ID, &v.DatabaseID, &v.Name, &v.Kind, &v.Config, &v.Position)
		return v, err
	})
	return out, err
}

// PropertyInput creates or updates a column.
type PropertyInput struct {
	Name     *string         `json:"name"`
	Type     *string         `json:"type"`
	Options  json.RawMessage `json:"options"`
	Position *float64        `json:"position"`
}

func (in *PropertyInput) validate(create bool) error {
	issues := map[string]string{}
	if create || in.Name != nil {
		if in.Name == nil || strings.TrimSpace(*in.Name) == "" {
			issues["name"] = "required"
		} else if len(*in.Name) > 120 {
			issues["name"] = "at most 120 characters"
		}
	}
	if create {
		if in.Type == nil || !slices.Contains(PropertyTypes, *in.Type) {
			issues["type"] = "one of " + strings.Join(PropertyTypes, ", ")
		}
	} else if in.Type != nil {
		issues["type"] = "a property never changes type"
	}
	if len(in.Options) > 16<<10 {
		issues["options"] = "too large"
	}
	if len(issues) > 0 {
		return httperror.Validation("invalid property", issues)
	}
	return nil
}

// CreateProperty adds a column (edit access on the database).
func (s *Service) CreateProperty(ctx context.Context, c Caller, dbID uuid.UUID, in PropertyInput) (Property, error) {
	l, err := s.databaseLevel(ctx, c, dbID)
	if err != nil {
		return Property{}, err
	}
	if err := need(l, domain.LevelEdit); err != nil {
		return Property{}, err
	}
	if err := in.validate(true); err != nil {
		return Property{}, err
	}
	options := in.Options
	if len(options) == 0 {
		options = json.RawMessage("null")
	}
	out := Property{DatabaseID: dbID, Name: strings.TrimSpace(*in.Name), Type: *in.Type, Options: options}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO database_properties (database_id, name, type, options, position)
		SELECT $1, $2, $3, $4, COALESCE(max(position), 0) + 1 FROM database_properties WHERE database_id = $1
		RETURNING id, position`, dbID, out.Name, out.Type, options).Scan(&out.ID, &out.Position)
	return out, err
}

// propertyDB finds the database a property belongs to.
func (s *Service) propertyDB(ctx context.Context, propID uuid.UUID) (uuid.UUID, error) {
	var dbID uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT database_id FROM database_properties WHERE id = $1`, propID).Scan(&dbID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, httperror.NotFound("property")
	}
	return dbID, err
}

// UpdateProperty renames a column or changes its options or position.
func (s *Service) UpdateProperty(ctx context.Context, c Caller, propID uuid.UUID, in PropertyInput) (Property, error) {
	dbID, err := s.propertyDB(ctx, propID)
	if err != nil {
		return Property{}, err
	}
	l, err := s.databaseLevel(ctx, c, dbID)
	if err != nil {
		return Property{}, hide(err, "property")
	}
	if err := need(l, domain.LevelEdit); err != nil {
		return Property{}, err
	}
	if err := in.validate(false); err != nil {
		return Property{}, err
	}
	var name *string
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		name = &n
	}
	var options *json.RawMessage
	if len(in.Options) > 0 {
		options = &in.Options
	}
	var out Property
	err = s.pool.QueryRow(ctx, `
		UPDATE database_properties
		   SET name = COALESCE($2, name), options = COALESCE($3, options), position = COALESCE($4, position)
		 WHERE id = $1
		RETURNING id, database_id, name, type, options, position`,
		propID, name, options, in.Position).
		Scan(&out.ID, &out.DatabaseID, &out.Name, &out.Type, &out.Options, &out.Position)
	return out, err
}

// DeleteProperty removes a column and every value in it.
func (s *Service) DeleteProperty(ctx context.Context, c Caller, propID uuid.UUID) error {
	dbID, err := s.propertyDB(ctx, propID)
	if err != nil {
		return err
	}
	l, err := s.databaseLevel(ctx, c, dbID)
	if err != nil {
		return hide(err, "property")
	}
	if err := need(l, domain.LevelEdit); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM database_properties WHERE id = $1`, propID)
	return err
}

// ViewInput creates or updates a view.
type ViewInput struct {
	Name     *string         `json:"name"`
	Kind     *string         `json:"kind"`
	Config   json.RawMessage `json:"config"`
	Position *float64        `json:"position"`
}

var viewKinds = []string{"table", "board", "calendar"}

func (in *ViewInput) validate(create bool) error {
	issues := map[string]string{}
	if create || in.Name != nil {
		if in.Name == nil || strings.TrimSpace(*in.Name) == "" {
			issues["name"] = "required"
		} else if len(*in.Name) > 120 {
			issues["name"] = "at most 120 characters"
		}
	}
	if create && (in.Kind == nil || !slices.Contains(viewKinds, *in.Kind)) {
		issues["kind"] = "table, board or calendar"
	}
	if !create && in.Kind != nil {
		issues["kind"] = "a view never changes kind"
	}
	if len(in.Config) > 0 {
		var cfg viewConfig
		if err := json.Unmarshal(in.Config, &cfg); err != nil {
			issues["config"] = "invalid"
		}
	}
	if len(issues) > 0 {
		return httperror.Validation("invalid view", issues)
	}
	return nil
}

// CreateView adds a view (edit access on the database).
func (s *Service) CreateView(ctx context.Context, c Caller, dbID uuid.UUID, in ViewInput) (View, error) {
	l, err := s.databaseLevel(ctx, c, dbID)
	if err != nil {
		return View{}, err
	}
	if err := need(l, domain.LevelEdit); err != nil {
		return View{}, err
	}
	if err := in.validate(true); err != nil {
		return View{}, err
	}
	config := in.Config
	if len(config) == 0 {
		config = json.RawMessage("{}")
	}
	out := View{DatabaseID: dbID, Name: strings.TrimSpace(*in.Name), Kind: *in.Kind, Config: config}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO database_views (database_id, name, kind, config, position)
		SELECT $1, $2, $3, $4, COALESCE(max(position), 0) + 1 FROM database_views WHERE database_id = $1
		RETURNING id, position`, dbID, out.Name, out.Kind, config).Scan(&out.ID, &out.Position)
	return out, err
}

func (s *Service) viewDB(ctx context.Context, viewID uuid.UUID) (uuid.UUID, error) {
	var dbID uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT database_id FROM database_views WHERE id = $1`, viewID).Scan(&dbID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, httperror.NotFound("view")
	}
	return dbID, err
}

// UpdateView renames a view or changes its config or position.
func (s *Service) UpdateView(ctx context.Context, c Caller, viewID uuid.UUID, in ViewInput) (View, error) {
	dbID, err := s.viewDB(ctx, viewID)
	if err != nil {
		return View{}, err
	}
	l, err := s.databaseLevel(ctx, c, dbID)
	if err != nil {
		return View{}, hide(err, "view")
	}
	if err := need(l, domain.LevelEdit); err != nil {
		return View{}, err
	}
	if err := in.validate(false); err != nil {
		return View{}, err
	}
	var name *string
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		name = &n
	}
	var config *json.RawMessage
	if len(in.Config) > 0 {
		config = &in.Config
	}
	var out View
	err = s.pool.QueryRow(ctx, `
		UPDATE database_views
		   SET name = COALESCE($2, name), config = COALESCE($3, config), position = COALESCE($4, position)
		 WHERE id = $1
		RETURNING id, database_id, name, kind, config, position`,
		viewID, name, config, in.Position).
		Scan(&out.ID, &out.DatabaseID, &out.Name, &out.Kind, &out.Config, &out.Position)
	return out, err
}

// DeleteView removes a view.
func (s *Service) DeleteView(ctx context.Context, c Caller, viewID uuid.UUID) error {
	dbID, err := s.viewDB(ctx, viewID)
	if err != nil {
		return err
	}
	l, err := s.databaseLevel(ctx, c, dbID)
	if err != nil {
		return hide(err, "view")
	}
	if err := need(l, domain.LevelEdit); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM database_views WHERE id = $1`, viewID)
	return err
}

// Row is one child page with its property values, keyed by property id.
type Row struct {
	PageNode
	Values map[string]json.RawMessage `json:"values"`
}

// Rows lists the database's visible rows. With a view, its filter and sort
// apply server-side; otherwise rows come in tree order.
func (s *Service) Rows(ctx context.Context, c Caller, dbID uuid.UUID, viewID *uuid.UUID) ([]Row, error) {
	t, err := s.pageTree(ctx, s.pool, dbID, false)
	if err != nil {
		return nil, err
	}
	p, _, err := t.visiblePage(dbID, c)
	if err != nil {
		return nil, err
	}
	if p.Kind != "database" {
		return nil, httperror.Validation("not a database page", nil)
	}
	rows := []Row{}
	ids := []uuid.UUID{}
	for _, child := range t.order {
		if child.ParentID == nil || *child.ParentID != dbID || t.inTrash(child.ID) {
			continue
		}
		n := t.node(child, c)
		if n.Access < domain.LevelView {
			continue
		}
		rows = append(rows, Row{PageNode: n, Values: map[string]json.RawMessage{}})
		ids = append(ids, child.ID)
	}
	if len(ids) > 0 {
		vrows, err := s.pool.Query(ctx, `
			SELECT page_id, property_id, value FROM page_property_values WHERE page_id = ANY($1)`, ids)
		if err != nil {
			return nil, err
		}
		byPage := map[uuid.UUID]*Row{}
		for i := range rows {
			byPage[rows[i].ID] = &rows[i]
		}
		for vrows.Next() {
			var pageID, propID uuid.UUID
			var value json.RawMessage
			if err := vrows.Scan(&pageID, &propID, &value); err != nil {
				vrows.Close()
				return nil, err
			}
			if r := byPage[pageID]; r != nil {
				r.Values[propID.String()] = value
			}
		}
		vrows.Close()
		if err := vrows.Err(); err != nil {
			return nil, err
		}
	}
	if viewID == nil {
		return rows, nil
	}
	var raw []byte
	err = s.pool.QueryRow(ctx, `SELECT config FROM database_views WHERE id = $1 AND database_id = $2`, viewID, dbID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httperror.NotFound("view")
	}
	if err != nil {
		return nil, err
	}
	var cfg viewConfig
	_ = json.Unmarshal(raw, &cfg)
	rows = applyFilter(rows, cfg)
	applySort(rows, cfg)
	return rows, nil
}

// applyFilter keeps rows matching every condition (equality; contains for
// multi_select arrays).
func applyFilter(rows []Row, cfg viewConfig) []Row {
	if len(cfg.Filter) == 0 {
		return rows
	}
	out := rows[:0]
	for _, r := range rows {
		keep := true
		for _, f := range cfg.Filter {
			if len(f.Equals) == 0 {
				continue
			}
			v := r.Values[f.Property.String()]
			if jsonArrayContains(v, f.Equals) {
				continue
			}
			if !jsonEqual(v, f.Equals) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, r)
		}
	}
	return out
}

func jsonEqual(a, b json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b))
}

// jsonArrayContains reports whether a is an array holding b.
func jsonArrayContains(a, b json.RawMessage) bool {
	var arr []json.RawMessage
	if json.Unmarshal(a, &arr) != nil {
		return false
	}
	for _, el := range arr {
		if jsonEqual(el, b) {
			return true
		}
	}
	return false
}

// applySort orders rows by the view's sort keys; missing values go last.
func applySort(rows []Row, cfg viewConfig) {
	if len(cfg.Sort) == 0 {
		return
	}
	slices.SortStableFunc(rows, func(a, b Row) int {
		for _, srt := range cfg.Sort {
			av, aok := a.Values[srt.Property.String()]
			bv, bok := b.Values[srt.Property.String()]
			var cmp int
			switch {
			case !aok && !bok:
				cmp = 0
			case !aok:
				cmp = 1 // missing sorts last either way
			case !bok:
				cmp = -1
			default:
				cmp = compareJSON(av, bv)
				if srt.Desc {
					cmp = -cmp
				}
			}
			if cmp != 0 {
				return cmp
			}
		}
		return 0
	})
}

// compareJSON orders two scalar JSON values: numbers numerically, bools
// false<true, everything else as strings.
func compareJSON(a, b json.RawMessage) int {
	var af, bf float64
	if json.Unmarshal(a, &af) == nil && json.Unmarshal(b, &bf) == nil {
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		default:
			return 0
		}
	}
	var ab, bb bool
	if json.Unmarshal(a, &ab) == nil && json.Unmarshal(b, &bb) == nil {
		switch {
		case !ab && bb:
			return -1
		case ab && !bb:
			return 1
		default:
			return 0
		}
	}
	var as, bs string
	if json.Unmarshal(a, &as) != nil {
		as = string(a)
	}
	if json.Unmarshal(b, &bs) != nil {
		bs = string(b)
	}
	return strings.Compare(strings.ToLower(as), strings.ToLower(bs))
}

// validateValue checks a value against its property's type. null removes.
func validateValue(p Property, value json.RawMessage) error {
	bad := func(want string) error {
		return httperror.Validation("invalid value", map[string]string{p.Name: want})
	}
	if len(value) > 16<<10 {
		return bad("too large")
	}
	switch p.Type {
	case "text", "url", "date", "select", "person":
		var s string
		if json.Unmarshal(value, &s) != nil {
			return bad("a string")
		}
		if p.Type == "person" && s != "" {
			if _, err := uuid.Parse(s); err != nil {
				return bad("a person id")
			}
		}
	case "number":
		var f float64
		if json.Unmarshal(value, &f) != nil {
			return bad("a number")
		}
	case "checkbox":
		var b bool
		if json.Unmarshal(value, &b) != nil {
			return bad("true or false")
		}
	case "multi_select":
		var arr []string
		if json.Unmarshal(value, &arr) != nil {
			return bad("a list of strings")
		}
	}
	return nil
}

// SetValues writes a row's property values (edit access on the row). Every
// property must belong to the row's parent database; null removes a value.
func (s *Service) SetValues(ctx context.Context, c Caller, pageID uuid.UUID, values map[uuid.UUID]json.RawMessage) (Row, error) {
	t, err := s.pageTree(ctx, s.pool, pageID, false)
	if err != nil {
		return Row{}, err
	}
	p, l, err := t.visiblePage(pageID, c)
	if err != nil {
		return Row{}, err
	}
	if err := need(l, domain.LevelEdit); err != nil {
		return Row{}, err
	}
	if p.ParentID == nil {
		return Row{}, httperror.Validation("not a database row", nil)
	}
	parent := t.pages[*p.ParentID]
	if parent == nil || parent.Kind != "database" {
		return Row{}, httperror.Validation("not a database row", nil)
	}
	for propID, value := range values {
		var prop Property
		err := s.pool.QueryRow(ctx, `
			SELECT id, database_id, name, type, options, position FROM database_properties WHERE id = $1`, propID).
			Scan(&prop.ID, &prop.DatabaseID, &prop.Name, &prop.Type, &prop.Options, &prop.Position)
		if errors.Is(err, pgx.ErrNoRows) {
			return Row{}, httperror.NotFound("property")
		}
		if err != nil {
			return Row{}, err
		}
		if prop.DatabaseID != *p.ParentID {
			return Row{}, httperror.Validation("the property belongs to another database", nil)
		}
		if len(value) == 0 || string(value) == "null" {
			if _, err := s.pool.Exec(ctx, `
				DELETE FROM page_property_values WHERE page_id = $1 AND property_id = $2`, pageID, propID); err != nil {
				return Row{}, err
			}
			continue
		}
		if err := validateValue(prop, value); err != nil {
			return Row{}, err
		}
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO page_property_values (page_id, property_id, value) VALUES ($1, $2, $3)
			ON CONFLICT (page_id, property_id) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
			pageID, propID, value); err != nil {
			return Row{}, err
		}
	}
	// Reload the row.
	rows, err := s.Rows(ctx, c, *p.ParentID, nil)
	if err != nil {
		return Row{}, err
	}
	for _, r := range rows {
		if r.ID == pageID {
			return r, nil
		}
	}
	return Row{}, httperror.NotFound("row")
}
