// Package directory lists the groups the share dialog offers. Grants can
// always name any group (free-text entry); a directory only makes picking
// easier.
//
// Sources, chosen per request by the HTTP layer:
//
//   - HR, when Wiki is paired with Den and HR_DIRECTORY_URL is set: HR's
//     departments and access groups. HR must expose
//     GET /api/internal/directory/groups (contract in HR, docs on the Wiki
//     configuration page).
//   - The identity provider (Keycloak's admin API), when IDP_DIRECTORY_URL
//     is set: every realm group.
//   - Otherwise the caller's own groups from their token.
package directory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// Group kinds.
const (
	KindDepartment = "department"
	KindAccess     = "access"
	KindIdP        = "idp"
	KindToken      = "token"
)

// Group is one entry of the group picker.
type Group struct {
	// Name is what a grant stores, e.g. "dept-security" or "/org/dept-x".
	Name string `json:"name"`
	// DisplayName is a human label, e.g. the department's name.
	DisplayName string `json:"display_name"`
	// Kind is department, access (an HR access group), idp or token.
	Kind string `json:"kind"`
}

// Source lists groups.
type Source interface {
	Groups(ctx context.Context) ([]Group, error)
}

// Cached wraps a source and keeps its answer for ttl, so opening the share
// dialog does not call HR or Keycloak every time. Errors are not cached.
type Cached struct {
	src Source
	ttl time.Duration

	mu      sync.Mutex
	groups  []Group
	fetched time.Time
}

// NewCached wraps src.
func NewCached(src Source, ttl time.Duration) *Cached { return &Cached{src: src, ttl: ttl} }

// Groups returns the cached list or fetches a fresh one.
func (c *Cached) Groups(ctx context.Context) ([]Group, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.groups != nil && time.Since(c.fetched) < c.ttl {
		return c.groups, nil
	}
	groups, err := c.src.Groups(ctx)
	if err != nil {
		return nil, err
	}
	c.groups, c.fetched = sorted(groups), time.Now()
	return c.groups, nil
}

// FromToken turns the caller's token groups into picker entries.
func FromToken(groups []string) []Group {
	out := make([]Group, 0, len(groups))
	for _, g := range groups {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, Group{Name: g, DisplayName: g, Kind: KindToken})
		}
	}
	return sorted(out)
}

// sorted drops empty names and duplicates (first one wins) and sorts by
// display name.
func sorted(groups []Group) []Group {
	seen := map[string]bool{}
	out := make([]Group, 0, len(groups))
	for _, g := range groups {
		g.Name = strings.TrimSpace(g.Name)
		key := strings.ToLower(strings.TrimPrefix(g.Name, "/"))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if strings.TrimSpace(g.DisplayName) == "" {
			g.DisplayName = g.Name
		}
		out = append(out, g)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].DisplayName) < strings.ToLower(out[j].DisplayName)
	})
	return out
}
