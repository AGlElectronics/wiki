package wiki

import (
	"testing"

	"github.com/google/uuid"

	"github.com/AGlElectronics/wiki/backend/internal/domain"
)

func TestGroupKey(t *testing.T) {
	cases := map[string]string{
		"dept-security":     "dept-security",
		"Dept-Security":     "dept-security",
		"/dept-security":    "dept-security",
		"  /Dept-Security ": "dept-security",
		"//dept-security":   "/dept-security",
		"/org/Dept-X":       "org/dept-x",
		"":                  "",
		" / ":               "",
	}
	for in, want := range cases {
		if got := groupKey(in); got != want {
			t.Errorf("groupKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGroupGrantMatching(t *testing.T) {
	grant := Grant{PrincipalKind: domain.PrincipalGroup, Principal: groupKey("Dept-Security"), Level: domain.LevelView}
	nested := Grant{PrincipalKind: domain.PrincipalGroup, Principal: groupKey("/org/dept-x"), Level: domain.LevelEdit}
	cases := []struct {
		name   string
		groups []string
		grant  Grant
		want   bool
	}{
		{"same name", []string{"dept-security"}, grant, true},
		{"other case", []string{"DEPT-SECURITY"}, grant, true},
		{"keycloak full path", []string{"/dept-security"}, grant, true},
		{"other group", []string{"dept-bar"}, grant, false},
		{"nested path matches itself", []string{"/org/dept-x"}, nested, true},
		{"plain name never matches a nested path", []string{"dept-x"}, nested, false},
		{"nested path never matches a plain name", []string{"/org/dept-security"}, grant, false},
	}
	for _, tc := range cases {
		c := Caller{UserID: uuid.New(), Groups: tc.groups}
		if got := tc.grant.matches(c); got != tc.want {
			t.Errorf("%s: matches(%v) = %v, want %v", tc.name, tc.groups, got, tc.want)
		}
	}
}
