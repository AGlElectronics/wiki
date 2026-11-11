package http_test

// Offboarding: HR's internal hook orphans private spaces, admins gain
// access until the sweep deletes them.

import (
	"bytes"
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	httpx "github.com/AGlElectronics/wiki/backend/internal/http"
	"github.com/AGlElectronics/wiki/backend/internal/wiki"
)

func postInternal(t *testing.T, h nethttp.Handler, token string, body any) int {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/internal/offboarded", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// TestOffboarding walks the hook, admin visibility and the sweep.
func TestOffboarding(t *testing.T) {
	deps := setupDeps(t)

	// Without a configured token the internal API answers 404.
	bare := httpx.NewRouter(deps, "test")
	mustStatus(t, postInternal(t, bare, "any", map[string]any{"subject": "olga"}), 404, "internal API without a token")

	deps.Config.ServiceToken = "test-service-token"
	h := httpx.NewRouter(deps, "test")
	userID(t, h, admin)
	userID(t, h, other) // creates olga's private space

	// Olga writes something private.
	code, body := call(t, h, other, "GET", "/api/v1/spaces", nil)
	mustStatus(t, code, 200, "olga lists spaces")
	private := str(list(body, "spaces")[0], "id")
	code, p := call(t, h, other, "POST", "/api/v1/spaces/"+private+"/pages", map[string]any{"title": "Olga's notes"})
	mustStatus(t, code, 201, "olga writes a page")
	notes := str(p, "id")
	code, _ = call(t, h, admin, "GET", "/api/v1/pages/"+notes, nil)
	mustStatus(t, code, 404, "admins cannot read a live private space")

	// The hook needs the right token and a subject.
	mustStatus(t, postInternal(t, h, "wrong", map[string]any{"subject": "olga"}), 401, "wrong token")
	mustStatus(t, postInternal(t, h, "test-service-token", map[string]any{}), 422, "missing subject")

	// The person is matched on issuer and subject: the same subject under
	// another issuer is somebody else. Dev-auth users have issuer "dev" and
	// subject = their user name.
	mustStatus(t, postInternal(t, h, "test-service-token",
		map[string]any{"issuer": "https://auth.example.org/realms/other", "subject": "olga"}), 204, "other issuer")
	code, _ = call(t, h, admin, "GET", "/api/v1/pages/"+notes, nil)
	mustStatus(t, code, 404, "another issuer's olga does not orphan this one")
	mustStatus(t, postInternal(t, h, "test-service-token",
		map[string]any{"issuer": "dev/", "subject": "olga", "username": "olga"}), 204, "offboard olga (trailing slash ignored)")
	mustStatus(t, postInternal(t, h, "test-service-token", map[string]any{"issuer": "dev", "subject": "olga"}), 204, "idempotent")
	mustStatus(t, postInternal(t, h, "test-service-token", map[string]any{"issuer": "dev", "subject": "never-seen"}), 204, "unknown subject")

	// Without an issuer (an older HR) the subject alone still works.
	userID(t, h, security)
	code, body = call(t, h, security, "GET", "/api/v1/spaces", nil)
	mustStatus(t, code, 200, "sam lists spaces")
	samPrivate := str(list(body, "spaces")[0], "id")
	mustStatus(t, postInternal(t, h, "test-service-token", map[string]any{"subject": "sec-sam"}), 204, "offboard sam by subject only")
	code, _ = call(t, h, admin, "GET", "/api/v1/spaces/"+samPrivate, nil)
	mustStatus(t, code, 200, "sam's space is orphaned too")

	// Admins now see the orphaned space and its pages; others still not.
	code, body = call(t, h, admin, "GET", "/api/v1/spaces", nil)
	mustStatus(t, code, 200, "admin lists spaces")
	var orphaned map[string]any
	for _, sp := range list(body, "spaces") {
		if str(sp, "id") == private {
			orphaned = sp
		}
	}
	if orphaned == nil || str(orphaned, "orphaned_at") == "" {
		t.Fatalf("admin should see the orphaned space with its stamp, got %v", list(body, "spaces"))
	}
	code, _ = call(t, h, admin, "GET", "/api/v1/pages/"+notes, nil)
	mustStatus(t, code, 200, "admin reads the orphaned space")
	code, _ = call(t, h, editor, "GET", "/api/v1/pages/"+notes, nil)
	mustStatus(t, code, 404, "non-admins still cannot")

	// The sweep respects the grace period, then deletes everything.
	svc := wiki.NewService(deps.Pool)
	n, err := svc.SweepOrphaned(context.Background(), 24*time.Hour)
	if err != nil || n != 0 {
		t.Fatalf("sweep inside the grace period = %d, %v", n, err)
	}
	if _, err := deps.Pool.Exec(context.Background(),
		`UPDATE spaces SET orphaned_at = now() - interval '40 days' WHERE id = $1`, private); err != nil {
		t.Fatalf("age the space: %v", err)
	}
	n, err = svc.SweepOrphaned(context.Background(), 30*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("sweep after the grace period = %d, %v", n, err)
	}
	code, _ = call(t, h, admin, "GET", "/api/v1/spaces/"+private, nil)
	mustStatus(t, code, 404, "the orphaned space is gone")
	code, _ = call(t, h, admin, "GET", "/api/v1/spaces/"+samPrivate, nil)
	mustStatus(t, code, 200, "sam's space is still inside its grace period")
}

// TestSweepQueuesContent: the sweep deletes the rows and queues the pages'
// stored content in one transaction; content that cannot be deleted stays
// queued until a retry succeeds, so nothing is left in S3 without an owner.
func TestSweepQueuesContent(t *testing.T) {
	deps := setupDeps(t)
	deps.Config.ServiceToken = "test-service-token"
	objects := &memObjects{data: map[string][]byte{}}
	s := startLive(t, deps, objects, t.TempDir())
	defer s.stop()
	h := s.h
	ctx := context.Background()

	userID(t, h, other)
	code, body := call(t, h, other, "GET", "/api/v1/spaces", nil)
	mustStatus(t, code, 200, "olga lists spaces")
	private := str(list(body, "spaces")[0], "id")
	code, p := call(t, h, other, "POST", "/api/v1/spaces/"+private+"/pages", map[string]any{"title": "Notes"})
	mustStatus(t, code, 201, "olga writes a page")
	page := str(p, "id")
	code, _ = uploadFile(t, h, other, page, "a.pdf", "application/pdf", []byte("%PDF-1.7"))
	mustStatus(t, code, 201, "olga uploads a file")

	mustStatus(t, postInternal(t, h, "test-service-token", map[string]any{"issuer": "dev", "subject": "olga"}), 204, "offboard olga")
	if _, err := deps.Pool.Exec(ctx, `UPDATE spaces SET orphaned_at = now() - interval '40 days' WHERE id = $1`, private); err != nil {
		t.Fatalf("age the space: %v", err)
	}

	queued := func() (int, int) {
		var n, attempts int
		if err := deps.Pool.QueryRow(ctx, `SELECT count(*), COALESCE(max(attempts), 0) FROM content_deletions WHERE page_id = $1`, page).Scan(&n, &attempts); err != nil {
			t.Fatalf("queue: %v", err)
		}
		return n, attempts
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// The bucket is down: rows go, the page stays queued with the error.
	objects.mu.Lock()
	objects.failRemove = true
	objects.mu.Unlock()
	if n, err := s.svc.SweepOrphaned(ctx, 30*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	waitFor("the failed attempt to be recorded", func() bool { n, a := queued(); return n == 1 && a >= 1 })
	if keys := objects.keys("wiki/files/" + page + "/"); len(keys) != 1 {
		t.Fatalf("the upload should still be in the bucket, got %v", keys)
	}

	// The bucket is back: a retry deletes the content and clears the queue.
	objects.mu.Lock()
	objects.failRemove = false
	objects.mu.Unlock()
	if n, err := s.svc.RetryContentDeletions(ctx, 0); err != nil || n != 1 {
		t.Fatalf("retry = %d, %v", n, err)
	}
	waitFor("the queue to clear", func() bool { n, _ := queued(); return n == 0 })
	if keys := objects.keys("wiki/files/" + page + "/"); len(keys) != 0 {
		t.Fatalf("the upload should be gone, got %v", keys)
	}
}
