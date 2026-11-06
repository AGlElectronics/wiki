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
	// Dev-auth users have subject = their user name.
	mustStatus(t, postInternal(t, h, "test-service-token", map[string]any{"subject": "olga", "username": "olga"}), 204, "offboard olga")
	mustStatus(t, postInternal(t, h, "test-service-token", map[string]any{"subject": "olga"}), 204, "idempotent")
	mustStatus(t, postInternal(t, h, "test-service-token", map[string]any{"subject": "never-seen"}), 204, "unknown subject")

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
}
