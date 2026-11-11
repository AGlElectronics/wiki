package wiki

import (
	"testing"

	"github.com/google/uuid"

	"github.com/AGlElectronics/wiki/backend/internal/domain"
	"github.com/AGlElectronics/wiki/backend/internal/groupmatch"
)

// Group grants are stored with groupmatch.NewGrant and matched with the
// caller's groupmatch.Config, so OIDC_GROUP_MATCH and OIDC_GROUP_PREFIX
// apply to page and space grants like to role bindings.
func TestGroupGrantMatching(t *testing.T) {
	grant := func(name string) Grant {
		return Grant{PrincipalKind: domain.PrincipalGroup, Principal: groupmatch.NewGrant(name).Stored, Level: domain.LevelView}
	}
	full := groupmatch.Config{}
	last := groupmatch.Config{Mode: groupmatch.ModeLast}
	prefixed := groupmatch.Config{Prefix: "/con2026"}
	cases := []struct {
		name   string
		gm     groupmatch.Config
		groups []string
		grant  Grant
		want   bool
	}{
		{"same name", full, []string{"dept-security"}, grant("Dept-Security"), true},
		{"other case", full, []string{"DEPT-SECURITY"}, grant("dept-security"), true},
		{"keycloak full path", full, []string{"/dept-security"}, grant("dept-security"), true},
		{"other group", full, []string{"dept-bar"}, grant("dept-security"), false},
		{"nested path matches itself", full, []string{"/org/dept-x"}, grant("/org/dept-x"), true},
		{"plain name never matches a nested path", full, []string{"dept-x"}, grant("/org/dept-x"), false},
		{"nested path never matches a plain name", full, []string{"/org/dept-security"}, grant("dept-security"), false},
		{"last segment mode", last, []string{"/org/dept-security"}, grant("dept-security"), true},
		{"prefix removed", prefixed, []string{"/con2026/dept-security"}, grant("dept-security"), true},
		{"other year's prefix", prefixed, []string{"/con2025/dept-security"}, grant("dept-security"), false},
	}
	for _, tc := range cases {
		c := Caller{UserID: uuid.New(), Groups: tc.groups, GroupMatch: tc.gm}
		if got := tc.grant.matches(c); got != tc.want {
			t.Errorf("%s: matches(%v) = %v, want %v", tc.name, tc.groups, got, tc.want)
		}
	}
}
