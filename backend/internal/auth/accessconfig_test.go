package auth

import (
	"strings"
	"testing"

	"github.com/AGlElectronics/wiki/backend/internal/domain"
)

const sampleAccess = `
organization = "demo-org"

[roles.organization_admin]
description = "Admin"
permissions = ["wiki.manage", "space.create", "settings.manage"]

[roles.space_creator]
permissions = ["space.create"]

[[bindings]]
group = "wiki-admin"
role = "organization_admin"

[[bindings]]
group = "/Wiki-Editor"
role = "space_creator"
organization = "other-org"
`

func TestParseAccessConfig(t *testing.T) {
	cfg, err := parseAccessConfig([]byte(sampleAccess))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg.HasRole("organization_admin") || cfg.RoleDescription("organization_admin") != "Admin" {
		t.Fatalf("role catalog: %v", cfg.Roles())
	}
	perms := cfg.PermissionsForRole("space_creator")
	if _, ok := perms[domain.PermSpaceCreate]; !ok || len(perms) != 1 {
		t.Fatalf("a role in the file replaces its built-in permissions, got %v", perms)
	}
	if cfg.PermissionsForRole("no-such-role") != nil {
		t.Fatal("an unknown role has no permissions")
	}
	b := cfg.Bindings()
	if len(b) != 2 || b[0].Organization != "demo-org" || b[1].Organization != "other-org" {
		t.Fatalf("bindings = %+v (top-level organization is the default)", b)
	}
}

func TestParseAccessConfigErrors(t *testing.T) {
	cases := map[string]string{
		"unknown permission": `
[roles.x]
permissions = ["wiki.fly"]`,
		"unknown role": `
organization = "demo-org"
[[bindings]]
group = "g"
role = "nope"`,
		"organization is required": `
[[bindings]]
group = "wiki-admin"
role = "organization_admin"`,
		"group is required": `
organization = "demo-org"
[[bindings]]
role = "organization_admin"`,
		"role is required": `
organization = "demo-org"
[[bindings]]
group = "g"`,
		"parse toml": `not = [valid`,
	}
	for want, file := range cases {
		_, err := parseAccessConfig([]byte(file))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}

func TestBindingsForNormalisesGroups(t *testing.T) {
	cfg, err := parseAccessConfig([]byte(sampleAccess))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		groups []string
		roles  []string
	}{
		{[]string{"wiki-admin"}, []string{"organization_admin"}},
		{[]string{"/WIKI-ADMIN"}, []string{"organization_admin"}},
		{[]string{" wiki-editor "}, []string{"space_creator"}},
		{[]string{"wiki-admin", "/wiki-editor"}, []string{"organization_admin", "space_creator"}},
		{[]string{"someone-else"}, nil},
		{nil, nil},
	}
	for _, tc := range cases {
		var got []string
		for _, b := range cfg.BindingsFor(tc.groups) {
			got = append(got, b.Role)
		}
		if strings.Join(got, ",") != strings.Join(tc.roles, ",") {
			t.Errorf("BindingsFor(%v) = %v, want %v", tc.groups, got, tc.roles)
		}
	}
}

func TestDefaultAccessConfig(t *testing.T) {
	cfg := DefaultAccessConfig()
	if len(cfg.Roles()) == 0 || len(cfg.Bindings()) != 0 {
		t.Fatalf("default catalog: roles %v, bindings %v", cfg.Roles(), cfg.Bindings())
	}
	var nilCfg *AccessConfig
	if nilCfg.HasRole("x") || nilCfg.BindingsFor([]string{"x"}) != nil || nilCfg.Roles() != nil {
		t.Fatal("a nil config grants nothing")
	}
}
