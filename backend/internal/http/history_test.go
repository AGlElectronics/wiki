package http_test

// Revisions: list, preview, restore. Same disposable database and
// in-memory bucket as the live tests; plain bytes stand in for Yjs state.

import (
	"context"
	"testing"
)

// compactTo drives one compaction round: tick, answer the compact request
// with the given state, and wait until the snapshot is stored.
func compactTo(t *testing.T, s *liveServer, c *liveClient, objects *memObjects, page, state string) {
	t.Helper()
	s.mgr.Tick(context.Background())
	msg := c.control()
	if msg["type"] != "compact" {
		t.Fatalf("expected a compact request, got %v", msg)
	}
	upto := int64(msg["upto"].(float64))
	c.send(snapshot(upto, "searchable text", nil, state))
	eventually(t, "snapshot stored", func() bool {
		data, err := objects.Get(context.Background(), "wiki/pages/"+page+"/current.ydoc")
		return err == nil && string(data) == state
	})
}

// TestHistory walks a page through two snapshots, previews the revision and
// restores it, and checks access rules on the way.
func TestHistory(t *testing.T) {
	deps := setupDeps(t)
	objects := &memObjects{data: map[string][]byte{}}
	s := startLive(t, deps, objects, t.TempDir())
	defer s.stop()
	s.mgr.SetCompactIdle(0)
	for _, id := range []identity{editor, security, other} {
		userID(t, s.h, id)
	}

	code, sp := call(t, s.h, editor, "POST", "/api/v1/spaces", map[string]any{"name": "Ops"})
	mustStatus(t, code, 201, "create space")
	spaceID := str(sp, "id")
	code, _ = call(t, s.h, editor, "PUT", "/api/v1/spaces/"+spaceID+"/grants",
		map[string]any{"principal_kind": "group", "principal": "dept-security", "level": "view"})
	mustStatus(t, code, 200, "grant security view")
	code, p := call(t, s.h, editor, "POST", "/api/v1/spaces/"+spaceID+"/pages", map[string]any{"title": "Runbook"})
	mustStatus(t, code, 201, "create page")
	page := str(p, "id")

	// Two compactions: the second keeps the first state as a revision.
	c1, _, _ := join(t, s, editor, page)
	c1.send(update("alpha"))
	compactTo(t, s, c1, objects, page, "state-v1")
	c1.send(update("beta"))
	compactTo(t, s, c1, objects, page, "state-v2")

	code, body := call(t, s.h, editor, "GET", "/api/v1/pages/"+page+"/revisions", nil)
	mustStatus(t, code, 200, "editor lists revisions")
	revs := list(body, "revisions")
	if len(revs) != 1 || str(revs[0], "reason") != "auto" {
		t.Fatalf("expected one auto revision, got %v", revs)
	}
	revID := str(revs[0], "id")

	// Preview rides on page access; no access reads as a missing revision.
	rec := rawGet(t, s.h, security, "/api/v1/revisions/"+revID)
	mustStatus(t, rec.Code, 200, "security previews a revision")
	if rec.Body.String() != "state-v1" {
		t.Fatalf("revision bytes = %q, want state-v1", rec.Body.String())
	}
	rec = rawGet(t, s.h, other, "/api/v1/revisions/"+revID)
	mustStatus(t, rec.Code, 404, "olga previews a revision")
	code, _ = call(t, s.h, other, "GET", "/api/v1/pages/"+page+"/revisions", nil)
	mustStatus(t, code, 404, "olga lists revisions")

	// Restore needs edit access; view-only gets 403.
	code, _ = call(t, s.h, security, "POST", "/api/v1/revisions/"+revID+"/restore", nil)
	mustStatus(t, code, 403, "view-only restore")
	code, _ = call(t, s.h, editor, "POST", "/api/v1/revisions/"+revID+"/restore", nil)
	mustStatus(t, code, 200, "editor restores")

	// Open connections are told to reload the document fresh.
	c1.expectClose(4409)

	// The replaced state was kept as a 'restore' revision.
	code, body = call(t, s.h, editor, "GET", "/api/v1/pages/"+page+"/revisions", nil)
	mustStatus(t, code, 200, "revisions after restore")
	revs = list(body, "revisions")
	if len(revs) != 2 {
		t.Fatalf("expected two revisions after restore, got %v", revs)
	}
	var restoreRev string
	for _, r := range revs {
		if str(r, "reason") == "restore" {
			restoreRev = str(r, "id")
		}
	}
	if restoreRev == "" {
		t.Fatalf("no 'restore' revision: %v", revs)
	}
	rec = rawGet(t, s.h, editor, "/api/v1/revisions/"+restoreRev)
	mustStatus(t, rec.Code, 200, "preview the kept state")
	if rec.Body.String() != "state-v2" {
		t.Fatalf("kept state = %q, want state-v2", rec.Body.String())
	}

	// A fresh join gets the restored document and nothing else.
	c2, updates, _ := join(t, s, editor, page)
	if got := strs(updates); len(got) != 1 || got[0] != "state-v1" {
		t.Fatalf("after restore the stored document = %v, want [state-v1]", got)
	}
	_ = c2

	// The replaced timeline's segments are gone from the bucket.
	if keys := objects.keys("wiki/pages/" + page + "/updates/"); len(keys) != 0 {
		t.Fatalf("segments survived the restore: %v", keys)
	}
}
