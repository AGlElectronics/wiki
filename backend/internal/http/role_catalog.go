package http

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/AGlElectronics/wiki/backend/internal/auth"
	"github.com/AGlElectronics/wiki/backend/internal/domain"
	"github.com/AGlElectronics/wiki/backend/internal/groupmatch"
)

// roleCatalogEntry is one role in GET /api/role-catalog
// (docs/concepts/role-catalog.md).
type roleCatalogEntry struct {
	Key          string   `json:"key"`
	Label        string   `json:"label"`
	Description  string   `json:"description"`
	Group        string   `json:"group"`
	GroupDisplay string   `json:"group_display"`
	Assignable   bool     `json:"assignable"`
	Default      bool     `json:"default"`
	Scope        string   `json:"scope"`
	Privileged   bool     `json:"privileged"`
	Dynamic      bool     `json:"dynamic"`
	Permissions  []string `json:"permissions"`
}

var wikiRoleDescriptions = map[string]string{
	domain.RoleOrganizationAdmin: "Full access to every team space and page, and the module settings.",
	domain.RoleSpaceCreator:      "Creates new team spaces.",
}

// privilegedPermissions make a role admin-type: they reach other people's
// pages or the module's settings.
var privilegedPermissions = map[string]bool{
	domain.PermWikiManage:     true,
	domain.PermSettingsManage: true,
}

// roleCatalogAuth admits HR's backend with HR_SERVICE_TOKEN (constant-time
// compare) and otherwise requires a normal sign-in through authMW. Without
// a configured token only signed-in staff can read the catalog.
func roleCatalogAuth(hrToken string, authMW func(http.Handler) http.Handler, next http.Handler) http.HandlerFunc {
	signedIn := authMW(next)
	return func(w http.ResponseWriter, r *http.Request) {
		if hrToken != "" {
			got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if subtle.ConstantTimeCompare([]byte(got), []byte(hrToken)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}
		signedIn.ServeHTTP(w, r)
	}
}

// roleCatalogHandler serves GET /api/role-catalog. Roles bound to an IdP
// group in the access config are assignable from HR; the group is the
// stored (normalised) form, group_display the one in the file.
func roleCatalogHandler(access *auth.AccessConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roles := make([]roleCatalogEntry, 0)
		bindings := access.Bindings()
		for _, role := range access.Roles() {
			set := access.PermissionsForRole(role)
			perms := make([]string, 0, len(set))
			privileged := false
			for p := range set {
				perms = append(perms, p)
				privileged = privileged || privilegedPermissions[p]
			}
			sort.Strings(perms)
			desc := access.RoleDescription(role)
			if desc == "" {
				desc = wikiRoleDescriptions[role]
			}
			base := roleCatalogEntry{
				Key:         role,
				Label:       humanizeRole(role),
				Description: desc,
				Scope:       "organization",
				Privileged:  privileged,
				Permissions: perms,
			}
			for _, b := range bindings {
				if b.Role != role {
					continue
				}
				g := groupmatch.NewGrant(b.Group)
				entry := base
				entry.Group, entry.GroupDisplay = g.Stored, g.Display
				entry.Assignable = true
				roles = append(roles, entry)
			}
		}
		body := map[string]any{
			"module":       "wiki",
			"display_name": "Wiki",
			"description":  "Pages, team spaces and shared knowledge.",
			"roles":        roles,
		}
		raw, _ := json.Marshal(roles)
		sum := sha256.Sum256(raw)
		revision := hex.EncodeToString(sum[:8])
		body["revision"] = revision
		etag := `"` + revision + `"`
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeJSON(w, http.StatusOK, body)
	}
}

func humanizeRole(role string) string {
	parts := strings.Split(strings.ReplaceAll(role, "_", " "), " ")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}
