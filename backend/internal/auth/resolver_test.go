package auth

// Resolver and middleware against a disposable database
// (TEST_DATABASE_URL); the schema is dropped and recreated.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/AGlElectronics/wiki/backend/internal/config"
	"github.com/AGlElectronics/wiki/backend/internal/domain"
	"github.com/AGlElectronics/wiki/backend/migrations"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	goose.SetBaseFS(migrations.FS)
	goose.SetTableName("goose_db_version")
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestResolver(t *testing.T) {
	pool := testPool(t)
	access, err := parseAccessConfig([]byte(sampleAccess + `
[[bindings]]
group = "ghost-admins"
role = "organization_admin"
organization = "no-such-org"
`))
	if err != nil {
		t.Fatal(err)
	}
	r := NewResolver(pool, access)
	ctx := context.Background()
	const demoOrg = "00000000-0000-0000-0000-000000000010"

	// Groups map to org-wide roles through the bindings, Keycloak's
	// full-path form included; a binding to an unknown org grants nothing.
	u, err := r.LoadOrProvisionOIDCUser(ctx, &OIDCClaims{
		Issuer: "https://auth.example.org/realms/c", Subject: "kc-1", Email: "sam@example.org", Name: "Sam",
		Groups: []string{"/Wiki-Admin", "ghost-admins"},
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if len(u.Memberships) != 1 || u.Memberships[0].OrganizationID.String() != demoOrg || u.Memberships[0].Role != "organization_admin" {
		t.Fatalf("memberships = %+v", u.Memberships)
	}
	if !u.HasInOrg(domain.PermWikiManage, u.Memberships[0].OrganizationID) || !u.HasAny(domain.PermSettingsManage) {
		t.Fatalf("permissions = %v", u.EffectivePermissions())
	}

	// The same (issuer, subject) is the same person; name and groups are
	// refreshed, and leaving the group drops the role at once.
	again, err := r.LoadOrProvisionOIDCUser(ctx, &OIDCClaims{
		Issuer: "https://auth.example.org/realms/c", Subject: "kc-1", Name: "Samantha", Groups: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != u.ID || again.DisplayName != "Samantha" || len(again.Memberships) != 0 || again.HasAny(domain.PermWikiManage) {
		t.Fatalf("second login = %+v", again)
	}
	var email string
	if err := pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, u.ID).Scan(&email); err != nil || email != "sam@example.org" {
		t.Fatalf("a token without email keeps the stored one: %q, %v", email, err)
	}

	// Same email, another subject or issuer: a new person, never linked.
	for _, c := range []*OIDCClaims{
		{Issuer: "https://auth.example.org/realms/c", Subject: "kc-2", Email: "sam@example.org"},
		{Issuer: "https://other.example.org", Subject: "kc-1", Email: "sam@example.org"},
	} {
		other, err := r.LoadOrProvisionOIDCUser(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if other.ID == u.ID {
			t.Fatalf("%s/%s was linked to the existing person by email", c.Issuer, c.Subject)
		}
	}

	if _, err := r.LoadOrProvisionOIDCUser(ctx, &OIDCClaims{Subject: "kc-3"}); err == nil {
		t.Fatal("claims without an issuer must be refused")
	}

	dev, err := r.LoadOrProvisionDevUser(ctx, "  ", []string{"wiki-admin"})
	if err != nil {
		t.Fatal(err)
	}
	if dev.Issuer != "dev" || dev.Subject != "dev-admin" || dev.Email != "dev-admin@wiki.local" || len(dev.Memberships) != 1 {
		t.Fatalf("dev user = %+v", dev)
	}
}

func TestMiddlewareOIDC(t *testing.T) {
	pool := testPool(t)
	f := newFakeIssuer(t)
	ctx := context.Background()
	cfg := &config.Config{AuthMode: config.AuthModeOIDC, OIDCIssuerURL: f.srv.URL, OIDCClientID: "wiki-spa"}
	v, err := NewOIDCVerifier(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	access, err := parseAccessConfig([]byte(sampleAccess))
	if err != nil {
		t.Fatal(err)
	}
	h := Middleware(cfg, NewResolver(pool, access), v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := MustFromContext(r.Context())
		if err != nil {
			t.Error("handler reached without a user")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"subject": u.Subject, "admin": u.HasAny(domain.PermWikiManage)})
	}))
	do := func(auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		// Dev headers are ignored in OIDC mode.
		req.Header.Set("X-Dev-User", "dev-admin")
		req.Header.Set("X-Dev-Groups", "wiki-admin")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := do(""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no bearer: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("Bearer not-a-jwt"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("garbage bearer: %d", rec.Code)
	}
	if rec := do("Bearer " + f.token(t, map[string]any{"sub": "kc-1", "aud": "someone-else"})); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong audience: %d", rec.Code)
	}
	rec := do("Bearer " + f.token(t, map[string]any{"sub": "kc-1", "aud": "wiki-spa", "groups": []string{"wiki-admin"}}))
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["subject"] != "kc-1" || out["admin"] != true {
		t.Fatalf("principal = %v", out)
	}

	// OIDC mode without a verifier refuses everything.
	noVerifier := Middleware(cfg, nil, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("handler reached without a verifier")
	}))
	rec = httptest.NewRecorder()
	noVerifier.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code < 500 {
		t.Fatalf("no verifier: %d", rec.Code)
	}
}

func TestRequireHelpers(t *testing.T) {
	ctx := context.Background()
	if err := RequireAny(ctx, domain.PermWikiManage); err == nil {
		t.Fatal("no user must be refused")
	}
	u := &User{Permissions: map[string]struct{}{domain.PermSpaceCreate: {}}}
	ctx = WithUser(ctx, u)
	if err := RequireAny(ctx, domain.PermSpaceCreate); err != nil {
		t.Fatalf("held permission: %v", err)
	}
	if err := RequireAny(ctx, domain.PermWikiManage); err == nil {
		t.Fatal("missing permission must be refused")
	}
}
