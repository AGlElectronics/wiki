package http_test

// Revision retention against the disposable database (TEST_DATABASE_URL)
// and an in-memory bucket.

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestRevisionRetention: revisions older than the retention go (object
// first, then row); Den's retention.revision_days overrides the local
// setting; 0 keeps everything.
func TestRevisionRetention(t *testing.T) {
	deps := setupDeps(t)
	deps.Config.RevisionRetentionDays = 30
	objects := &memObjects{data: map[string][]byte{}}
	s := startLive(t, deps, objects, t.TempDir())
	defer s.stop()
	s.svc.SetRevisionRetentionDays(30)
	h := s.h
	ctx := context.Background()

	code, sp := call(t, h, editor, "POST", "/api/v1/spaces", map[string]any{"name": "Docs"})
	mustStatus(t, code, 201, "create space")
	code, pg := call(t, h, editor, "POST", "/api/v1/spaces/"+str(sp, "id")+"/pages", map[string]any{"title": "Page"})
	mustStatus(t, code, 201, "create page")
	page := str(pg, "id")

	addRevision := func(ageDays int) (uuid.UUID, string) {
		t.Helper()
		id := uuid.New()
		key := "wiki/pages/" + page + "/revisions/" + id.String() + ".ydoc"
		if err := objects.Put(ctx, key, []byte("doc"), ""); err != nil {
			t.Fatal(err)
		}
		if _, err := deps.Pool.Exec(ctx, `
			INSERT INTO page_revisions (id, page_id, storage_key, size_bytes, upto_seq, created_at)
			VALUES ($1, $2, $3, 3, 1, now() - make_interval(days => $4))`, id, page, key, ageDays); err != nil {
			t.Fatalf("insert revision: %v", err)
		}
		return id, key
	}
	count := func() int {
		var n int
		if err := deps.Pool.QueryRow(ctx, `SELECT count(*) FROM page_revisions WHERE page_id = $1`, page).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	_, oldKey := addRevision(40)
	_, recentKey := addRevision(5)
	_, midKey := addRevision(10)

	code, body := call(t, h, editor, "GET", "/api/v1/pages/"+page+"/revisions", nil)
	mustStatus(t, code, 200, "list revisions")
	if body["retention_days"] != float64(30) || str(body, "retention_source") != "config" {
		t.Fatalf("retention in the list = %v / %v, want 30 from config", body["retention_days"], body["retention_source"])
	}

	// Local retention: 30 days.
	if n, err := s.mgr.PruneRevisions(ctx, 30); err != nil || n != 1 {
		t.Fatalf("prune 30 days = %d, %v; want 1", n, err)
	}
	if count() != 2 || len(objects.keys(oldKey)) != 0 || len(objects.keys(recentKey)) != 1 {
		t.Fatalf("after pruning: %d rows, objects %v", count(), objects.keys("wiki/pages/"+page+"/revisions/"))
	}

	// 0 keeps everything.
	if n, err := s.mgr.PruneRevisions(ctx, 0); err != nil || n != 0 {
		t.Fatalf("prune with 0 = %d, %v; want nothing", n, err)
	}

	// Den's module settings win once paired.
	if _, err := deps.Pool.Exec(ctx, `
		INSERT INTO den_link (den_base_url, credential, bundle)
		VALUES ('https://den.test', 'cred', '{"brand":{},"settings":{"data":{}},"retention":{"revision_days":7}}')`); err != nil {
		t.Fatalf("pair: %v", err)
	}
	retention, err := s.svc.RevisionRetention(ctx)
	if err != nil || retention.Days != 7 || retention.Source != "den" {
		t.Fatalf("retention = %+v, %v; want 7 from den", retention, err)
	}
	code, body = call(t, h, editor, "GET", "/api/v1/pages/"+page+"/revisions", nil)
	mustStatus(t, code, 200, "list revisions (paired)")
	if body["retention_days"] != float64(7) || str(body, "retention_source") != "den" {
		t.Fatalf("retention in the list = %v / %v, want 7 from den", body["retention_days"], body["retention_source"])
	}
	if n, err := s.mgr.PruneRevisions(ctx, retention.Days); err != nil || n != 1 || len(objects.keys(midKey)) != 0 {
		t.Fatalf("prune 7 days = %d, %v; want the 10-day-old revision gone", n, err)
	}

	// A value Den sends that is not a whole number of days is ignored.
	for _, bad := range []string{`"30"`, `-1`, `1.5`, `null`} {
		if _, err := deps.Pool.Exec(ctx, `UPDATE den_link SET bundle = jsonb_set(bundle, '{retention,revision_days}', $1::jsonb)`, bad); err != nil {
			t.Fatal(err)
		}
		if r, err := s.svc.RevisionRetention(ctx); err != nil || r.Source != "config" || r.Days != 30 {
			t.Fatalf("Den value %s: retention = %+v, %v; want the local 30", bad, r, err)
		}
	}

	// A revision whose object cannot be removed keeps its row (retried on
	// the next run) and nothing else is touched.
	if count() != 1 {
		t.Fatalf("want the recent revision left, got %d", count())
	}
	objects.mu.Lock()
	objects.failRemove = true
	objects.mu.Unlock()
	if n, err := s.mgr.PruneRevisions(ctx, 1); err != nil || n != 0 {
		t.Fatalf("prune with the bucket down = %d, %v; want 0 and no error", n, err)
	}
	if count() != 1 || len(objects.keys(recentKey)) != 1 {
		t.Fatalf("a revision whose object stayed must keep its row: %d rows", count())
	}
	objects.mu.Lock()
	objects.failRemove = false
	objects.mu.Unlock()
	if n, err := s.mgr.PruneRevisions(ctx, 1); err != nil || n != 1 || count() != 0 {
		t.Fatalf("retry = %d, %v, %d rows left; want it gone", n, err, count())
	}
}
