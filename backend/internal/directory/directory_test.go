package directory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHRGroups(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != HRPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer good-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"groups": []map[string]string{
			{"name": "dept-security", "display_name": "Security", "kind": "department"},
			{"name": "wiki-editor", "display_name": "Wiki editors", "kind": "access"},
			{"name": "odd", "kind": "something-else"},
		}})
	}))
	defer srv.Close()

	groups, err := NewHR(srv.URL+"/", "good-token").Groups(context.Background())
	if err != nil {
		t.Fatalf("groups: %v", err)
	}
	if len(groups) != 3 || groups[0] != (Group{Name: "dept-security", DisplayName: "Security", Kind: KindDepartment}) {
		t.Fatalf("groups = %+v", groups)
	}
	if groups[2].Kind != KindAccess {
		t.Fatalf("an unknown kind is reported as access, got %q", groups[2].Kind)
	}

	if _, err := NewHR(srv.URL, "wrong").Groups(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("wrong token: err = %v, want a 401 error", err)
	}
}

func TestKeycloakGroups(t *testing.T) {
	var tokenCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/conservices/protocol/openid-connect/token":
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != "wiki-directory" || r.Form.Get("client_secret") != "s3cret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			tokenCalls.Add(1)
			_, _ = w.Write([]byte(`{"access_token":"kc-token"}`))
		case r.Header.Get("Authorization") != "Bearer kc-token":
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/admin/realms/conservices/groups":
			_, _ = w.Write([]byte(`[
				{"id":"1","name":"dept-security","path":"/dept-security","subGroupCount":0},
				{"id":"2","name":"org","path":"/org","subGroupCount":1},
				{"id":"3","name":"legacy","path":"/legacy","subGroups":[{"id":"4","name":"old","path":"/legacy/old"}]}
			]`))
		case r.URL.Path == "/admin/realms/conservices/groups/2/children":
			_, _ = w.Write([]byte(`[{"id":"5","name":"dept-x","path":"/org/dept-x","subGroupCount":0}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	groups, err := NewKeycloak(srv.URL, "conservices", "wiki-directory", "s3cret").Groups(context.Background())
	if err != nil {
		t.Fatalf("groups: %v", err)
	}
	got := map[string]string{}
	for _, g := range groups {
		if g.Kind != KindIdP {
			t.Fatalf("kind = %q", g.Kind)
		}
		got[g.Name] = g.DisplayName
	}
	want := map[string]string{"dept-security": "dept-security", "org": "org", "/org/dept-x": "dept-x", "legacy": "legacy", "/legacy/old": "old"}
	if len(got) != len(want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("group %q = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}

	if _, err := NewKeycloak(srv.URL, "conservices", "wiki-directory", "wrong").Groups(context.Background()); err == nil {
		t.Fatal("a wrong client secret must fail")
	}
}

type countingSource struct {
	calls  int
	groups []Group
	err    error
}

func (c *countingSource) Groups(context.Context) ([]Group, error) {
	c.calls++
	return c.groups, c.err
}

func TestCached(t *testing.T) {
	src := &countingSource{groups: []Group{{Name: "b"}, {Name: "A", DisplayName: "a"}, {Name: "/b"}}}
	c := NewCached(src, time.Hour)
	for range 3 {
		groups, err := c.Groups(context.Background())
		if err != nil {
			t.Fatalf("groups: %v", err)
		}
		if len(groups) != 2 || groups[0].Name != "A" || groups[1].Name != "b" || groups[1].DisplayName != "b" {
			t.Fatalf("groups = %+v (sorted, duplicates dropped, display name filled in)", groups)
		}
	}
	if src.calls != 1 {
		t.Fatalf("source called %d times, want 1", src.calls)
	}

	failing := &countingSource{err: errors.New("down")}
	fc := NewCached(failing, time.Hour)
	for range 2 {
		if _, err := fc.Groups(context.Background()); err == nil {
			t.Fatal("want the error")
		}
	}
	if failing.calls != 2 {
		t.Fatalf("errors must not be cached; source called %d times", failing.calls)
	}
}

func TestFromToken(t *testing.T) {
	groups := FromToken([]string{"wiki-editor", " ", "dept-security", "/Dept-Security"})
	if len(groups) != 2 || groups[0].Name != "dept-security" || groups[1].Name != "wiki-editor" || groups[0].Kind != KindToken {
		t.Fatalf("groups = %+v", groups)
	}
}
