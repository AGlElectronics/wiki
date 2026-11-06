// Package domain holds the wiki vocabulary — role names, permission
// identifiers and access levels — used by both the service layer and the
// HTTP layer. Keeping these as typed constants prevents stringly typed
// drift between tables, APIs, and the access config file.
package domain

// Role names recognized by default. The access config file (TOML) may add
// more roles or redefine the permission set attached to any of these.
const (
	// RoleOrganizationAdmin manages every team space and page, and the
	// module settings. People's private spaces stay private.
	RoleOrganizationAdmin = "organization_admin"
	// RoleSpaceCreator may create team spaces (and gets full access to the
	// ones they create).
	RoleSpaceCreator = "space_creator"
)

// Permission identifiers. Reading and writing pages needs no permission:
// page access comes from space and page grants, not from roles.
const (
	// PermWikiManage: full access to every team space and page, including
	// restricted pages, and the trash of every team space.
	PermWikiManage = "wiki.manage"
	// PermSpaceCreate: create team spaces.
	PermSpaceCreate = "space.create"
	// PermSettingsManage: module settings, including the Den pairing.
	PermSettingsManage = "settings.manage"
)

// AllPermissions returns every permission the access-config validator will
// accept. The access config file may only reference identifiers listed here;
// unknown names fail configuration load with a clear error.
func AllPermissions() []string {
	return []string{
		PermWikiManage,
		PermSpaceCreate,
		PermSettingsManage,
	}
}

// DefaultRolePermissions is the built-in role → permission catalog applied
// when the access config file omits a role (or isn't configured at all).
func DefaultRolePermissions() map[string][]string {
	return map[string][]string{
		RoleOrganizationAdmin: AllPermissions(),
		RoleSpaceCreator:      {PermSpaceCreate},
	}
}

// Space kinds.
const (
	// SpacePrivate belongs to one person; only they (and people they share
	// single pages with) see it.
	SpacePrivate = "private"
	// SpaceTeam is shared through space grants.
	SpaceTeam = "team"
)

// Grant principal kinds.
const (
	PrincipalUser  = "user"
	PrincipalGroup = "group"
)

// Level is an access level on a space or page. Higher includes lower.
type Level int

const (
	LevelNone Level = iota
	LevelView
	// LevelComment reads like LevelView and may also start and join
	// comment threads, resolve them and react; never edit the page.
	LevelComment
	LevelEdit
	// LevelFull may also share, restrict, move to trash and delete.
	LevelFull
)

var levelNames = []string{"none", "view", "comment", "edit", "full"}

// String returns the API name ("none", "view", …).
func (l Level) String() string {
	if l < LevelNone || l > LevelFull {
		return "none"
	}
	return levelNames[l]
}

// ParseLevel parses a grantable level name. "none" is not grantable.
func ParseLevel(s string) (Level, bool) {
	for i, n := range levelNames {
		if n == s && i > 0 {
			return Level(i), true
		}
	}
	return LevelNone, false
}

// MarshalText makes levels render as their names in JSON.
func (l Level) MarshalText() ([]byte, error) { return []byte(l.String()), nil }
