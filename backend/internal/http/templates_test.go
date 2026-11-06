package http_test

// Templates and duplication: the flag, the server-side copy of the
// subtree and its stored content, and the access rules.

import (
	"testing"
)

// TestDuplicateAndTemplates copies a small subtree with content and checks
// flags, titles and stored bytes.
func TestDuplicateAndTemplates(t *testing.T) {
	deps := setupDeps(t)
	objects := &memObjects{data: map[string][]byte{}}
	s := startLive(t, deps, objects, t.TempDir())
	defer s.stop()
	h := s.h
	for _, id := range []identity{editor, security} {
		userID(t, h, id)
	}

	code, sp := call(t, h, editor, "POST", "/api/v1/spaces", map[string]any{"name": "Docs"})
	mustStatus(t, code, 201, "create space")
	spaceID := str(sp, "id")
	code, _ = call(t, h, editor, "PUT", "/api/v1/spaces/"+spaceID+"/grants",
		map[string]any{"principal_kind": "group", "principal": "dept-security", "level": "view"})
	mustStatus(t, code, 200, "grant security view")

	// Meeting notes template with one sub-page and some content.
	code, p := call(t, h, editor, "POST", "/api/v1/spaces/"+spaceID+"/pages", map[string]any{"title": "Meeting notes"})
	mustStatus(t, code, 201, "create template root")
	tmpl := str(p, "id")
	code, p = call(t, h, editor, "POST", "/api/v1/spaces/"+spaceID+"/pages",
		map[string]any{"title": "Agenda", "parent_id": tmpl})
	mustStatus(t, code, 201, "create template child")
	child := str(p, "id")
	code, p = call(t, h, editor, "PATCH", "/api/v1/pages/"+tmpl, map[string]any{"is_template": true})
	mustStatus(t, code, 200, "mark as template")
	if p["is_template"] != true {
		t.Fatalf("template flag missing: %v", p)
	}

	c1, _, _ := join(t, s, editor, child)
	c1.send(update("agenda-content"))

	// View access cannot create the copy (edit at the destination).
	code, _ = call(t, h, security, "POST", "/api/v1/pages/"+tmpl+"/duplicate", map[string]any{})
	mustStatus(t, code, 403, "view access duplicates")

	// New from template: title override, flag dropped, subtree copied.
	code, out := call(t, h, editor, "POST", "/api/v1/pages/"+tmpl+"/duplicate",
		map[string]any{"title": "Weekly sync 46"})
	mustStatus(t, code, 201, "duplicate the template")
	copyRoot := out["page"].(map[string]any)
	if str(copyRoot, "title") != "Weekly sync 46" || copyRoot["is_template"] == true {
		t.Fatalf("copy root = %v", copyRoot)
	}
	newRootID := str(copyRoot, "id")

	code, tr := call(t, h, editor, "GET", "/api/v1/spaces/"+spaceID+"/pages", nil)
	mustStatus(t, code, 200, "tree after duplicate")
	var newChild string
	for _, n := range list(tr, "pages") {
		if str(n, "title") == "Agenda" && str(n, "parent_id") == newRootID {
			newChild = str(n, "id")
		}
	}
	if newChild == "" {
		t.Fatalf("the copy should hold an Agenda child, got %v", list(tr, "pages"))
	}

	// The copied child serves the same content as the source.
	c2, updates, _ := join(t, s, editor, newChild)
	if got := strs(updates); len(got) != 1 || got[0] != "agenda-content" {
		t.Fatalf("copied content = %v", got)
	}
	_ = c2

	// A plain duplicate (no title) gets "(copy)".
	code, out = call(t, h, editor, "POST", "/api/v1/pages/"+child+"/duplicate", map[string]any{})
	mustStatus(t, code, 201, "duplicate a plain page")
	if got := str(out["page"].(map[string]any), "title"); got != "Agenda (copy)" {
		t.Fatalf("plain copy title = %q", got)
	}
}
