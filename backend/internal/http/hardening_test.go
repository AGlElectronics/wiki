package http_test

// Shared foundations in Wiki: authbase security headers, CORS and body
// limit, the authenticated role catalog, and denlink's revoked state.

import (
	"bytes"
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/AGlElectronics/wiki/backend/internal/config"
	"github.com/AGlElectronics/wiki/backend/internal/denlink"
	httpx "github.com/AGlElectronics/wiki/backend/internal/http"
)

func TestSecurityHeadersAndCORS(t *testing.T) {
	deps := setupDeps(t)
	deps.Config.CORSAllowedOrigins = []string{"https://wiki.example.org"}
	h := httpx.NewRouter(deps, "test")

	rec := rawGet(t, h, editor, "/api/v1/me")
	mustStatus(t, rec.Code, 200, "me")
	for k, v := range map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	} {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}

	preflight := func(h nethttp.Handler) string {
		req := httptest.NewRequest("OPTIONS", "/api/v1/spaces", nil)
		req.Header.Set("Origin", "https://wiki.example.org")
		req.Header.Set("Access-Control-Request-Method", "POST")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Header().Get("Access-Control-Allow-Headers")
	}
	if got := preflight(h); !strings.Contains(got, "X-Dev-User") {
		t.Errorf("dev mode in development should allow X-Dev-User, got %q", got)
	}
	oidc := deps
	cfg := *deps.Config
	cfg.AuthMode, cfg.AppEnv = config.AuthModeOIDC, "production"
	oidc.Config = &cfg
	if got := preflight(httpx.NewRouter(oidc, "test")); strings.Contains(got, "X-Dev") || !strings.Contains(got, "Authorization") {
		t.Errorf("OIDC mode must not allow dev headers, got %q", got)
	}
}

func TestJSONBodyLimit(t *testing.T) {
	h := setupRouter(t)
	big := `{"name":"` + strings.Repeat("x", 2<<20) + `"}`
	req := httptest.NewRequest("POST", "/api/v1/spaces", strings.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dev-User", editor.user)
	req.Header.Set("X-Dev-Groups", editor.groups)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != nethttp.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "request_too_large") {
		t.Fatalf("oversized body: %d %s", rec.Code, rec.Body.String())
	}
	code, _ := call(t, h, editor, "POST", "/api/v1/spaces", map[string]any{"name": "Ok", "bogus": 1})
	mustStatus(t, code, 400, "unknown field")
}

func TestRoleCatalogContract(t *testing.T) {
	deps := setupDeps(t)
	deps.Config.HRServiceToken = "hr-service-test-token"

	// Signed in (dev mode): allowed, with the 1.0 fields.
	h := httpx.NewRouter(deps, "test")
	rec := rawGet(t, h, other, "/api/role-catalog")
	mustStatus(t, rec.Code, 200, "signed-in person reads the catalog")
	var body struct {
		Module   string `json:"module"`
		Revision string `json:"revision"`
		Roles    []struct {
			Key, Group, GroupDisplay, Scope string
			Privileged, Dynamic, Assignable bool
		} `json:"roles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Module != "wiki" || body.Revision == "" || rec.Header().Get("ETag") != `"`+body.Revision+`"` {
		t.Fatalf("catalog head: %+v, ETag %q", body, rec.Header().Get("ETag"))
	}
	privileged := map[string]bool{}
	for _, r := range body.Roles {
		privileged[r.Key] = r.Privileged
		if r.Scope != "organization" || r.Dynamic || !r.Assignable || r.Group == "" {
			t.Errorf("role %+v", r)
		}
	}
	if !privileged["organization_admin"] || privileged["space_creator"] {
		t.Fatalf("privileged flags = %v", privileged)
	}
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	first := raw["roles"].([]any)[0].(map[string]any)
	for _, k := range []string{"group_display", "privileged", "dynamic", "default"} {
		if _, ok := first[k]; !ok {
			t.Errorf("role is missing %q: %v", k, first)
		}
	}

	req := httptest.NewRequest("GET", "/api/role-catalog", nil)
	req.Header.Set("X-Dev-User", other.user)
	req.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	mustStatus(t, rec2.Code, 304, "unchanged catalog")

	// OIDC mode: HR's token is enough; nothing else is (no verifier here, so
	// a sign-in cannot succeed either).
	cfg := *deps.Config
	cfg.AuthMode = config.AuthModeOIDC
	oidc := deps
	oidc.Config = &cfg
	ho := httpx.NewRouter(oidc, "test")
	get := func(auth string) int {
		req := httptest.NewRequest("GET", "/api/role-catalog", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		ho.ServeHTTP(rec, req)
		return rec.Code
	}
	mustStatus(t, get("Bearer hr-service-test-token"), 200, "HR's service token")
	if code := get(""); code == 200 {
		t.Fatal("an anonymous request read the catalog")
	}
	if code := get("Bearer wrong"); code == 200 {
		t.Fatal("a wrong token read the catalog")
	}
}

func TestDenRevokedState(t *testing.T) {
	deps := setupDeps(t)
	ctx := context.Background()
	if _, err := deps.Pool.Exec(ctx, `
		INSERT INTO den_link (den_base_url, credential, bundle, last_error, revoked_at)
		VALUES ('https://den.test', 'cred', '{"brand":{},"settings":{"data":{}}}', 'Den rejected this module''s credential', now())`); err != nil {
		t.Fatalf("seed a revoked link: %v", err)
	}
	den := denlink.New(deps.Pool, deps.Config.DenConfig("test"), zerolog.Nop())
	if err := den.Restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	deps.Den = den
	h := httpx.NewRouter(deps, "test")

	rec := rawGet(t, h, admin, "/api/den/state")
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"revoked":true`)) {
		t.Fatalf("state = %s", rec.Body.String())
	}
	code, status := call(t, h, admin, "GET", "/api/v1/den/", nil)
	mustStatus(t, code, 200, "den status")
	if status["revoked"] != true || str(status, "revoked_at") == "" {
		t.Fatalf("status = %v", status)
	}
}
