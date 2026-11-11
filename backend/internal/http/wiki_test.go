package http_test

// Access tests against a real PostgreSQL.
//
// They run only when TEST_DATABASE_URL is set and MIGRATE THE DATABASE FROM
// SCRATCH (drop + recreate the public schema), so point it at a disposable
// database, never a real one:
//
//	TEST_DATABASE_URL='postgres://wiki:wiki@localhost:5439/wiki_test?sslmode=disable' go test ./...
//
// The suite runs through the real router in dev auth mode. Dev auth feeds
// X-Dev-Groups through the same access-config binding path as OIDC token
// groups, so these tests exercise the production authorization code.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/rs/zerolog"

	"github.com/AGlElectronics/wiki/backend/internal/auth"
	"github.com/AGlElectronics/wiki/backend/internal/config"
	"github.com/AGlElectronics/wiki/backend/internal/db"
	httpx "github.com/AGlElectronics/wiki/backend/internal/http"
	"github.com/AGlElectronics/wiki/backend/migrations"
)

const accessConfigTOML = `
organization = "demo-org"

[roles.organization_admin]
description = "Admin"
permissions = ["wiki.manage", "space.create", "settings.manage"]

[roles.space_creator]
description = "Creator"
permissions = ["space.create"]

[[bindings]]
group = "wiki-admin"
role = "organization_admin"

[[bindings]]
group = "wiki-editor"
role = "space_creator"
`

// identity is a dev-mode caller: a name plus simulated IdP groups.
type identity struct {
	user   string
	groups string
}

var (
	admin    = identity{user: "dev-admin", groups: "wiki-admin"}
	editor   = identity{user: "eddie", groups: "wiki-editor"}
	security = identity{user: "sec-sam", groups: "dept-security"}
	other    = identity{user: "olga", groups: "dept-bar"}
)

func setupRouter(t *testing.T) nethttp.Handler {
	t.Helper()
	deps := setupDeps(t)
	return httpx.NewRouter(deps, "test")
}

// setupDeps migrates the test database from scratch and returns router
// dependencies in dev auth mode.
func setupDeps(t *testing.T) httpx.Deps {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)

	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if _, err := sqlDB.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	goose.SetBaseFS(migrations.FS)
	goose.SetTableName("goose_db_version")
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose dialect: %v", err)
	}
	if err := goose.UpContext(ctx, sqlDB, "."); err != nil {
		t.Fatalf("goose up: %v", err)
	}

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)

	accessPath := filepath.Join(t.TempDir(), "access-config.toml")
	if err := os.WriteFile(accessPath, []byte(accessConfigTOML), 0o600); err != nil {
		t.Fatalf("write access config: %v", err)
	}
	access, err := auth.LoadAccessConfig(accessPath)
	if err != nil {
		t.Fatalf("load access config: %v", err)
	}

	cfg := &config.Config{
		AppEnv:         "development",
		HTTPAddr:       ":0",
		DatabaseURL:    dsn,
		AuthMode:       config.AuthModeDev,
		DevDefaultUser: "dev-admin",
	}
	return httpx.Deps{
		Config: cfg,
		Pool:   pool,
		Logger: zerolog.Nop(),
		Access: access,
	}
}

// call performs one request as the given identity and decodes the JSON body.
func call(t *testing.T, h nethttp.Handler, id identity, method, path string, body any) (int, map[string]any) {
	t.Helper()
	rd := bytes.NewReader(nil)
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Dev-User", id.user)
	req.Header.Set("X-Dev-Groups", id.groups)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	out := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: invalid JSON response (%d): %s", method, path, rec.Code, rec.Body.String())
		}
	}
	return rec.Code, out
}

func mustStatus(t *testing.T, got int, want int, what string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got status %d, want %d", what, got, want)
	}
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func list(m map[string]any, key string) []map[string]any {
	raw, _ := m[key].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if mv, ok := v.(map[string]any); ok {
			out = append(out, mv)
		}
	}
	return out
}

// pageIDs returns the ids of a tree response.
func pageIDs(m map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, p := range list(m, "pages") {
		out[str(p, "id")] = true
	}
	return out
}

func userID(t *testing.T, h nethttp.Handler, id identity) string {
	t.Helper()
	code, me := call(t, h, id, "GET", "/api/v1/me", nil)
	mustStatus(t, code, 200, "me for "+id.user)
	return str(me, "id")
}

// TestSpacesAndPageAccess walks the access model: private spaces, team
// space grants (person, group, everyone), restriction, sharing a single
// page, trash and the 404-not-403 rule.
func TestSpacesAndPageAccess(t *testing.T) {
	h := setupRouter(t)
	for _, id := range []identity{admin, editor, security, other} {
		userID(t, h, id)
	}
	otherID := userID(t, h, other)

	// Everyone gets a private space on first visit, visible only to them.
	code, body := call(t, h, other, "GET", "/api/v1/spaces", nil)
	mustStatus(t, code, 200, "olga lists spaces")
	spaces := list(body, "spaces")
	if len(spaces) != 1 || str(spaces[0], "kind") != "private" {
		t.Fatalf("olga should see exactly her private space, got %v", spaces)
	}
	olgaPrivate := str(spaces[0], "id")
	if body["can_create"] != false {
		t.Fatalf("olga holds no space.create, can_create = %v", body["can_create"])
	}
	code, _ = call(t, h, admin, "GET", "/api/v1/spaces/"+olgaPrivate, nil)
	mustStatus(t, code, 404, "admin cannot see olga's private space")

	// Only space.create holders create team spaces.
	code, _ = call(t, h, other, "POST", "/api/v1/spaces", map[string]any{"name": "Nope"})
	mustStatus(t, code, 403, "olga creates a team space")
	code, sp := call(t, h, editor, "POST", "/api/v1/spaces", map[string]any{"name": "Security", "icon": "🛡️"})
	mustStatus(t, code, 201, "editor creates a team space")
	spaceID := str(sp, "id")
	if str(sp, "access") != "full" {
		t.Fatalf("creator should have full access, got %q", str(sp, "access"))
	}

	// Closed by default: others get 404, admins see every team space.
	code, _ = call(t, h, security, "GET", "/api/v1/spaces/"+spaceID+"/pages", nil)
	mustStatus(t, code, 404, "security reads a closed space")
	code, _ = call(t, h, admin, "GET", "/api/v1/spaces/"+spaceID+"/pages", nil)
	mustStatus(t, code, 200, "admin reads a closed space")

	// A group grant opens it to the group, at that level.
	code, _ = call(t, h, editor, "PUT", "/api/v1/spaces/"+spaceID+"/grants",
		map[string]any{"principal_kind": "group", "principal": "dept-security", "level": "view"})
	mustStatus(t, code, 200, "grant dept-security view")
	code, _ = call(t, h, security, "GET", "/api/v1/spaces/"+spaceID+"/pages", nil)
	mustStatus(t, code, 200, "security reads the space")
	code, _ = call(t, h, security, "POST", "/api/v1/spaces/"+spaceID+"/pages", map[string]any{"title": "Mine"})
	mustStatus(t, code, 403, "view-only security creates a page")
	code, _ = call(t, h, other, "GET", "/api/v1/spaces/"+spaceID+"/pages", nil)
	mustStatus(t, code, 404, "olga (other group) still cannot see it")

	// Build a small tree: Handbook > Radio > Secret.
	code, hb := call(t, h, editor, "POST", "/api/v1/spaces/"+spaceID+"/pages", map[string]any{"title": "Handbook"})
	mustStatus(t, code, 201, "create Handbook")
	handbook := str(hb, "id")
	code, rp := call(t, h, editor, "POST", "/api/v1/spaces/"+spaceID+"/pages", map[string]any{"title": "Radio", "parent_id": handbook})
	mustStatus(t, code, 201, "create Radio")
	radio := str(rp, "id")
	code, sc := call(t, h, editor, "POST", "/api/v1/spaces/"+spaceID+"/pages", map[string]any{"title": "Secret", "parent_id": radio})
	mustStatus(t, code, 201, "create Secret")
	secret := str(sc, "id")
	if crumbs := list(sc, "breadcrumbs"); len(crumbs) != 2 || str(crumbs[0], "title") != "Handbook" {
		t.Fatalf("Secret breadcrumbs = %v", crumbs)
	}

	code, page := call(t, h, security, "GET", "/api/v1/pages/"+secret, nil)
	mustStatus(t, code, 200, "security reads Secret through inheritance")
	if str(page, "access") != "view" {
		t.Fatalf("security access to Secret = %q, want view", str(page, "access"))
	}
	code, _ = call(t, h, security, "PATCH", "/api/v1/pages/"+secret, map[string]any{"title": "Hacked"})
	mustStatus(t, code, 403, "view-only security renames Secret")

	// Restricting Radio hides Radio and Secret from the group. Restriction
	// also cuts off the editor's space grant, so they get a full grant on
	// Radio and keep access.
	code, rp = call(t, h, editor, "PUT", "/api/v1/pages/"+radio+"/restricted", map[string]any{"restricted": true})
	mustStatus(t, code, 200, "editor restricts Radio")
	if str(rp, "access") != "full" {
		t.Fatalf("restricting person keeps full access, got %q", str(rp, "access"))
	}
	code, _ = call(t, h, security, "GET", "/api/v1/pages/"+secret, nil)
	mustStatus(t, code, 404, "security reads Secret under a restricted page")
	code, tr := call(t, h, security, "GET", "/api/v1/spaces/"+spaceID+"/pages", nil)
	mustStatus(t, code, 200, "security tree")
	if ids := pageIDs(tr); ids[radio] || ids[secret] || !ids[handbook] {
		t.Fatalf("security tree should hold Handbook only, got %v", ids)
	}
	code, _ = call(t, h, admin, "GET", "/api/v1/pages/"+secret, nil)
	mustStatus(t, code, 200, "admin reads restricted pages")

	// Sharing Secret with olga: she sees just that page, under "shared".
	code, _ = call(t, h, editor, "PUT", "/api/v1/pages/"+secret+"/grants",
		map[string]any{"principal_kind": "user", "principal": otherID, "level": "edit"})
	mustStatus(t, code, 200, "share Secret with olga")
	code, page = call(t, h, other, "GET", "/api/v1/pages/"+secret, nil)
	mustStatus(t, code, 200, "olga reads the shared page")
	if crumbs := list(page, "breadcrumbs"); len(crumbs) != 0 {
		t.Fatalf("olga must not see hidden ancestors in breadcrumbs, got %v", crumbs)
	}
	code, _ = call(t, h, other, "PATCH", "/api/v1/pages/"+secret, map[string]any{"title": "Secret (edited)"})
	mustStatus(t, code, 200, "olga edits the shared page")
	code, _ = call(t, h, other, "GET", "/api/v1/pages/"+radio, nil)
	mustStatus(t, code, 404, "olga reads the restricted parent")
	code, body = call(t, h, other, "GET", "/api/v1/spaces", nil)
	mustStatus(t, code, 200, "olga lists spaces again")
	if shared := list(body, "shared"); len(shared) != 1 || str(shared[0], "id") != secret {
		t.Fatalf("olga's shared list = %v", shared)
	}
	code, _ = call(t, h, other, "PUT", "/api/v1/pages/"+secret+"/grants",
		map[string]any{"principal_kind": "group", "principal": "dept-bar", "level": "view"})
	mustStatus(t, code, 403, "edit access cannot share")

	// Moving: never under itself, and only within the space.
	code, _ = call(t, h, editor, "POST", "/api/v1/pages/"+handbook+"/move", map[string]any{"parent_id": secret})
	mustStatus(t, code, 422, "move Handbook under its own descendant")
	code, _ = call(t, h, editor, "POST", "/api/v1/pages/"+secret+"/move", map[string]any{"parent_id": nil})
	mustStatus(t, code, 200, "move Secret to the root")

	// Everyone level opens the space to every signed-in person.
	code, _ = call(t, h, editor, "PATCH", "/api/v1/spaces/"+spaceID, map[string]any{"everyone_level": "view"})
	mustStatus(t, code, 200, "open the space to everyone")
	code, _ = call(t, h, other, "GET", "/api/v1/pages/"+handbook, nil)
	mustStatus(t, code, 200, "olga reads Handbook once the space is open")

	// Trash: view access cannot trash; the trashed subtree disappears and
	// comes back on restore.
	code, _ = call(t, h, security, "POST", "/api/v1/pages/"+handbook+"/trash", nil)
	mustStatus(t, code, 403, "view-only security trashes Handbook")
	code, _ = call(t, h, editor, "POST", "/api/v1/pages/"+handbook+"/trash", nil)
	mustStatus(t, code, 204, "editor trashes Handbook")
	code, _ = call(t, h, editor, "GET", "/api/v1/pages/"+radio, nil)
	mustStatus(t, code, 404, "a child of a trashed page is gone")
	code, tr = call(t, h, editor, "GET", "/api/v1/spaces/"+spaceID+"/trash", nil)
	mustStatus(t, code, 200, "editor lists the trash")
	if ids := pageIDs(tr); len(ids) != 1 || !ids[handbook] {
		t.Fatalf("trash should hold Handbook, got %v", ids)
	}
	code, _ = call(t, h, editor, "POST", "/api/v1/pages/"+handbook+"/restore", nil)
	mustStatus(t, code, 200, "restore Handbook")
	code, _ = call(t, h, editor, "GET", "/api/v1/pages/"+radio, nil)
	mustStatus(t, code, 200, "Radio is back with its parent")

	// Delete forever only from the trash.
	code, _ = call(t, h, editor, "DELETE", "/api/v1/pages/"+handbook, nil)
	mustStatus(t, code, 404, "delete forever a live page")
	code, _ = call(t, h, editor, "POST", "/api/v1/pages/"+handbook+"/trash", nil)
	mustStatus(t, code, 204, "trash Handbook again")
	code, _ = call(t, h, editor, "DELETE", "/api/v1/pages/"+handbook, nil)
	mustStatus(t, code, 204, "delete Handbook forever")
	code, _ = call(t, h, admin, "GET", "/api/v1/pages/"+radio, nil)
	mustStatus(t, code, 404, "its children are deleted too")

	// Leaving the group revokes access on the next request.
	code, _ = call(t, h, editor, "PATCH", "/api/v1/spaces/"+spaceID, map[string]any{"everyone_level": ""})
	mustStatus(t, code, 200, "close the space again")
	code, _ = call(t, h, identity{user: security.user, groups: ""}, "GET", "/api/v1/spaces/"+spaceID+"/pages", nil)
	mustStatus(t, code, 404, "security without the group")
}

// TestGroupGrantNormalisation: a group grant matches the token's groups
// whatever the case or a leading "/", keeps the typed name for display,
// and never lets a nested path stand in for a plain name.
func TestGroupGrantNormalisation(t *testing.T) {
	h := setupRouter(t)
	keycloakStyle := identity{user: "kim", groups: "/Dept-Security"}
	nested := identity{user: "nina", groups: "/org/dept-security"}
	for _, id := range []identity{editor, keycloakStyle, nested, other} {
		userID(t, h, id)
	}

	code, sp := call(t, h, editor, "POST", "/api/v1/spaces", map[string]any{"name": "Ops"})
	mustStatus(t, code, 201, "create space")
	spaceID := str(sp, "id")
	code, sp = call(t, h, editor, "PUT", "/api/v1/spaces/"+spaceID+"/grants",
		map[string]any{"principal_kind": "group", "principal": " DEPT-security ", "level": "view"})
	mustStatus(t, code, 200, "grant DEPT-security")
	var grant map[string]any
	for _, g := range list(sp, "grants") {
		if str(g, "principal_kind") == "group" {
			grant = g
		}
	}
	if str(grant, "principal") != "dept-security" || str(grant, "display_name") != "DEPT-security" {
		t.Fatalf("group grant = %v, want principal dept-security shown as DEPT-security", grant)
	}

	code, _ = call(t, h, keycloakStyle, "GET", "/api/v1/spaces/"+spaceID+"/pages", nil)
	mustStatus(t, code, 200, "/Dept-Security matches the grant")
	code, _ = call(t, h, nested, "GET", "/api/v1/spaces/"+spaceID+"/pages", nil)
	mustStatus(t, code, 404, "/org/dept-security is another group")

	// The same group typed differently updates the one grant.
	code, sp = call(t, h, editor, "PUT", "/api/v1/spaces/"+spaceID+"/grants",
		map[string]any{"principal_kind": "group", "principal": "/dept-security", "level": "edit"})
	mustStatus(t, code, 200, "regrant /dept-security")
	groups := 0
	for _, g := range list(sp, "grants") {
		if str(g, "principal_kind") == "group" {
			groups++
			if str(g, "level") != "edit" {
				t.Fatalf("grant level = %q, want edit", str(g, "level"))
			}
		}
	}
	if groups != 1 {
		t.Fatalf("want one group grant after retyping the name, got %d", groups)
	}

	// Page grants and the "shared with me" list use the same rule.
	code, pg := call(t, h, editor, "POST", "/api/v1/spaces/"+spaceID+"/pages", map[string]any{"title": "Runbook"})
	mustStatus(t, code, 201, "create page")
	page := str(pg, "id")
	code, _ = call(t, h, editor, "PUT", "/api/v1/pages/"+page+"/grants",
		map[string]any{"principal_kind": "group", "principal": "Dept-Bar", "level": "view"})
	mustStatus(t, code, 200, "share the page with Dept-Bar")
	code, body := call(t, h, other, "GET", "/api/v1/spaces", nil)
	mustStatus(t, code, 200, "olga lists spaces")
	if shared := list(body, "shared"); len(shared) != 1 || str(shared[0], "id") != page {
		t.Fatalf("olga (dept-bar) should see Runbook as shared, got %v", shared)
	}

	// Removing works with any spelling.
	code, _ = call(t, h, editor, "PUT", "/api/v1/spaces/"+spaceID+"/grants",
		map[string]any{"principal_kind": "group", "principal": "Dept-Security", "level": ""})
	mustStatus(t, code, 200, "remove the group grant")
	code, _ = call(t, h, keycloakStyle, "GET", "/api/v1/spaces/"+spaceID+"/pages", nil)
	mustStatus(t, code, 404, "access gone after removal")

	code, _ = call(t, h, editor, "PUT", "/api/v1/spaces/"+spaceID+"/grants",
		map[string]any{"principal_kind": "group", "principal": " / ", "level": "view"})
	mustStatus(t, code, 422, "a group name that normalises to nothing")
}

// TestPrivateSpace covers the owner's pages and sharing one of them.
func TestPrivateSpace(t *testing.T) {
	h := setupRouter(t)
	eddieID := userID(t, h, editor)
	userID(t, h, other)

	code, body := call(t, h, other, "GET", "/api/v1/spaces", nil)
	mustStatus(t, code, 200, "olga lists spaces")
	private := str(list(body, "spaces")[0], "id")

	code, p := call(t, h, other, "POST", "/api/v1/spaces/"+private+"/pages", map[string]any{"title": "Notes"})
	mustStatus(t, code, 201, "olga writes in her private space")
	notes := str(p, "id")
	code, _ = call(t, h, editor, "GET", "/api/v1/pages/"+notes, nil)
	mustStatus(t, code, 404, "eddie reads olga's private page")
	code, _ = call(t, h, admin, "GET", "/api/v1/pages/"+notes, nil)
	mustStatus(t, code, 404, "admins do not read private pages")
	code, _ = call(t, h, other, "PUT", "/api/v1/spaces/"+private+"/grants",
		map[string]any{"principal_kind": "user", "principal": eddieID, "level": "view"})
	mustStatus(t, code, 422, "a private space cannot be shared whole")
	code, _ = call(t, h, other, "PUT", "/api/v1/pages/"+notes+"/grants",
		map[string]any{"principal_kind": "user", "principal": eddieID, "level": "view"})
	mustStatus(t, code, 200, "olga shares one page")
	code, _ = call(t, h, editor, "GET", "/api/v1/pages/"+notes, nil)
	mustStatus(t, code, 200, "eddie reads the shared private page")
	code, _ = call(t, h, other, "DELETE", "/api/v1/spaces/"+private, nil)
	mustStatus(t, code, 409, "a private space cannot be deleted")
}
