package http_test

// Notion-style databases: schema CRUD, rows as child pages, values,
// server-side filter and sort, and access rules (ADR 0002).

import (
	"testing"
)

// TestDatabases drives a small task-tracker database end to end.
func TestDatabases(t *testing.T) {
	h := setupRouter(t)
	for _, id := range []identity{editor, security} {
		userID(t, h, id)
	}

	code, sp := call(t, h, editor, "POST", "/api/v1/spaces", map[string]any{"name": "Projects"})
	mustStatus(t, code, 201, "create space")
	spaceID := str(sp, "id")
	code, _ = call(t, h, editor, "PUT", "/api/v1/spaces/"+spaceID+"/grants",
		map[string]any{"principal_kind": "group", "principal": "dept-security", "level": "view"})
	mustStatus(t, code, 200, "grant security view")

	// A database page gets a default table view.
	code, db := call(t, h, editor, "POST", "/api/v1/spaces/"+spaceID+"/pages",
		map[string]any{"title": "Tasks", "kind": "database"})
	mustStatus(t, code, 201, "create database")
	dbID := str(db, "id")
	if str(db, "kind") != "database" {
		t.Fatalf("kind = %v", db["kind"])
	}
	code, schema := call(t, h, editor, "GET", "/api/v1/pages/"+dbID+"/database", nil)
	mustStatus(t, code, 200, "get database")
	views := list(schema, "views")
	if len(views) != 1 || str(views[0], "kind") != "table" {
		t.Fatalf("default views = %v", views)
	}

	// Columns: a select and a number. View access cannot add columns.
	code, _ = call(t, h, security, "POST", "/api/v1/pages/"+dbID+"/properties",
		map[string]any{"name": "Status", "type": "select"})
	mustStatus(t, code, 403, "view access adds a column")
	code, statusProp := call(t, h, editor, "POST", "/api/v1/pages/"+dbID+"/properties",
		map[string]any{"name": "Status", "type": "select", "options": map[string]any{"choices": []string{"Todo", "Done"}}})
	mustStatus(t, code, 201, "add Status column")
	statusID := str(statusProp, "id")
	code, effortProp := call(t, h, editor, "POST", "/api/v1/pages/"+dbID+"/properties",
		map[string]any{"name": "Effort", "type": "number"})
	mustStatus(t, code, 201, "add Effort column")
	effortID := str(effortProp, "id")

	// Rows are plain child pages.
	mkRow := func(title string, status string, effort float64) string {
		t.Helper()
		code, p := call(t, h, editor, "POST", "/api/v1/spaces/"+spaceID+"/pages",
			map[string]any{"title": title, "parent_id": dbID})
		mustStatus(t, code, 201, "create row "+title)
		id := str(p, "id")
		code, _ = call(t, h, editor, "PUT", "/api/v1/pages/"+id+"/values",
			map[string]any{"values": map[string]any{statusID: status, effortID: effort}})
		mustStatus(t, code, 200, "set values on "+title)
		return id
	}
	a := mkRow("Fix radios", "Todo", 3)
	mkRow("Order badges", "Done", 1)
	cRow := mkRow("Walkthrough", "Todo", 2)

	// Everyone with view reads rows and values.
	code, body := call(t, h, security, "GET", "/api/v1/pages/"+dbID+"/rows", nil)
	mustStatus(t, code, 200, "security lists rows")
	rows := list(body, "rows")
	if len(rows) != 3 {
		t.Fatalf("rows = %v", rows)
	}
	// But cannot write values.
	code, _ = call(t, h, security, "PUT", "/api/v1/pages/"+a+"/values",
		map[string]any{"values": map[string]any{statusID: "Done"}})
	mustStatus(t, code, 403, "view access writes values")

	// Type validation.
	code, _ = call(t, h, editor, "PUT", "/api/v1/pages/"+a+"/values",
		map[string]any{"values": map[string]any{effortID: "a lot"}})
	mustStatus(t, code, 422, "a string in a number column")

	// A filtered, sorted view applies server-side.
	code, view := call(t, h, editor, "POST", "/api/v1/pages/"+dbID+"/views", map[string]any{
		"name": "Open by effort", "kind": "table",
		"config": map[string]any{
			"filter": []map[string]any{{"property": statusID, "equals": "Todo"}},
			"sort":   []map[string]any{{"property": effortID, "desc": true}},
		},
	})
	mustStatus(t, code, 201, "create a filtered view")
	viewID := str(view, "id")
	code, body = call(t, h, editor, "GET", "/api/v1/pages/"+dbID+"/rows?view="+viewID, nil)
	mustStatus(t, code, 200, "rows through the view")
	rows = list(body, "rows")
	if len(rows) != 2 || str(rows[0], "id") != a || str(rows[1], "id") != cRow {
		titles := make([]string, len(rows))
		for i, r := range rows {
			titles[i] = str(r, "title")
		}
		t.Fatalf("view rows = %v", titles)
	}

	// Deleting a column removes its values.
	code, _ = call(t, h, editor, "DELETE", "/api/v1/properties/"+effortID, nil)
	mustStatus(t, code, 204, "delete Effort")
	code, body = call(t, h, editor, "GET", "/api/v1/pages/"+dbID+"/rows", nil)
	mustStatus(t, code, 200, "rows after column delete")
	for _, r := range list(body, "rows") {
		values := r["values"].(map[string]any)
		if _, stillThere := values[effortID]; stillThere {
			t.Fatalf("effort value survived the column delete: %v", values)
		}
	}

	// A plain page refuses database calls; values on a non-row refuse too.
	code, p := call(t, h, editor, "POST", "/api/v1/spaces/"+spaceID+"/pages", map[string]any{"title": "Notes"})
	mustStatus(t, code, 201, "create a plain page")
	code, _ = call(t, h, editor, "GET", "/api/v1/pages/"+str(p, "id")+"/database", nil)
	mustStatus(t, code, 422, "database call on a plain page")
	code, _ = call(t, h, editor, "PUT", "/api/v1/pages/"+str(p, "id")+"/values",
		map[string]any{"values": map[string]any{statusID: "Todo"}})
	mustStatus(t, code, 422, "values on a non-row")

	// No access means 404, never 403.
	code, _ = call(t, h, identity{user: "stranger", groups: ""}, "GET", "/api/v1/pages/"+dbID+"/rows", nil)
	mustStatus(t, code, 404, "a stranger lists rows")
}
