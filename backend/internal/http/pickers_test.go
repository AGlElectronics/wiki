package http_test

// Share dialog pickers against the disposable database (TEST_DATABASE_URL).

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"

	"github.com/AGlElectronics/wiki/backend/internal/denlink"
	"github.com/AGlElectronics/wiki/backend/internal/directory"
	httpx "github.com/AGlElectronics/wiki/backend/internal/http"
)

type fakeDirectory struct {
	groups []directory.Group
	err    error
}

func (f fakeDirectory) Groups(context.Context) ([]directory.Group, error) { return f.groups, f.err }

// TestPeoplePickerEmails: only wiki admins see (and search by) email
// addresses; everyone else gets names.
func TestPeoplePickerEmails(t *testing.T) {
	h := setupRouter(t)
	eddieID := userID(t, h, editor)
	userID(t, h, admin)

	code, body := call(t, h, other, "GET", "/api/v1/users?q=eddie", nil)
	mustStatus(t, code, 200, "olga searches people")
	people := list(body, "people")
	if len(people) != 1 || str(people[0], "display_name") != "Eddie" {
		t.Fatalf("olga's search = %v", people)
	}
	if _, has := people[0]["email"]; has {
		t.Fatalf("a non-admin must not see email addresses: %v", people[0])
	}
	code, body = call(t, h, other, "GET", "/api/v1/users?q=wiki.local", nil)
	mustStatus(t, code, 200, "olga searches by email domain")
	if people := list(body, "people"); len(people) != 0 {
		t.Fatalf("a non-admin must not find people by email: %v", people)
	}
	code, body = call(t, h, other, "GET", "/api/v1/users?ids="+eddieID, nil)
	mustStatus(t, code, 200, "olga looks up a comment author")
	if p := list(body, "people"); len(p) != 1 || p[0]["email"] != nil {
		t.Fatalf("lookup by id leaks the email: %v", p)
	}

	code, body = call(t, h, admin, "GET", "/api/v1/users?q=eddie@wiki", nil)
	mustStatus(t, code, 200, "admin searches by email")
	if people := list(body, "people"); len(people) != 1 || str(people[0], "email") != "eddie@wiki.local" {
		t.Fatalf("admin's search = %v", people)
	}
}

// TestGroupPickerSources: HR while paired, the IdP directory otherwise,
// else the caller's own groups; a failing directory falls back.
func TestGroupPickerSources(t *testing.T) {
	deps := setupDeps(t)
	hr := fakeDirectory{groups: []directory.Group{{Name: "dept-security", DisplayName: "Security", Kind: directory.KindDepartment}}}
	idp := fakeDirectory{groups: []directory.Group{{Name: "/org/dept-x", DisplayName: "dept-x", Kind: directory.KindIdP}}}
	caller := identity{user: "kim", groups: "wiki-editor,dept-bar"}

	groupsAs := func(d httpx.Deps) (string, []map[string]any, map[string]any) {
		t.Helper()
		h := httpx.NewRouter(d, "test")
		code, body := call(t, h, caller, "GET", "/api/v1/groups", nil)
		mustStatus(t, code, 200, "list groups")
		return str(body, "source"), list(body, "groups"), body
	}

	src, groups, _ := groupsAs(deps)
	if src != "token" || len(groups) != 2 || str(groups[0], "name") != "dept-bar" {
		t.Fatalf("no directory: source %q, groups %v; want the caller's own groups", src, groups)
	}

	withDirs := deps
	withDirs.HRGroups, withDirs.IdPGroups = hr, idp
	if src, groups, _ = groupsAs(withDirs); src != "idp" || str(groups[0], "name") != "/org/dept-x" {
		t.Fatalf("standalone: source %q, groups %v; want the IdP directory", src, groups)
	}

	// Paired with Den: HR wins.
	ctx := context.Background()
	if _, err := deps.Pool.Exec(ctx, `INSERT INTO den_link (den_base_url, credential, bundle) VALUES ('https://den.test', 'cred', '{"brand":{},"settings":{"data":{}}}')`); err != nil {
		t.Fatalf("pair: %v", err)
	}
	den := denlink.New(deps.Pool, denlink.Config{Slug: "wiki", Mode: denlink.ModeStandalone}, zerolog.Nop())
	if err := den.Restore(ctx); err != nil || !den.Paired() {
		t.Fatalf("restore pairing: paired %v, err %v", den.Paired(), err)
	}
	paired := withDirs
	paired.Den = den
	src, groups, _ = groupsAs(paired)
	if src != "hr" || len(groups) != 1 || str(groups[0], "display_name") != "Security" || str(groups[0], "kind") != "department" {
		t.Fatalf("paired: source %q, groups %v; want HR", src, groups)
	}

	broken := paired
	broken.HRGroups = fakeDirectory{err: errors.New("hr down")}
	src, groups, body := groupsAs(broken)
	if src != "token" || len(groups) != 2 || str(body, "warning") == "" {
		t.Fatalf("HR down: source %q, groups %v, body %v; want the caller's groups with a warning", src, groups, body)
	}
}
