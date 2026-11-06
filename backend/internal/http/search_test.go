package http_test

// Search, backlinks and favourites, with access filtering throughout.

import (
	"testing"
)

// TestSearchBacklinksFavorites builds two spaces (one closed), fills one
// page's body text through a compaction, links pages, and checks that
// search, backlinks and favourites all filter by the caller's access.
func TestSearchBacklinksFavorites(t *testing.T) {
	deps := setupDeps(t)
	objects := &memObjects{data: map[string][]byte{}}
	s := startLive(t, deps, objects, t.TempDir())
	defer s.stop()
	s.mgr.SetCompactIdle(0)
	for _, id := range []identity{editor, security, other} {
		userID(t, s.h, id)
	}

	// An open-to-security space and a closed one, same titles.
	code, sp := call(t, s.h, editor, "POST", "/api/v1/spaces", map[string]any{"name": "Open"})
	mustStatus(t, code, 201, "create open space")
	openSpace := str(sp, "id")
	code, _ = call(t, s.h, editor, "PUT", "/api/v1/spaces/"+openSpace+"/grants",
		map[string]any{"principal_kind": "group", "principal": "dept-security", "level": "view"})
	mustStatus(t, code, 200, "grant security view")
	code, sp = call(t, s.h, editor, "POST", "/api/v1/spaces", map[string]any{"name": "Closed"})
	mustStatus(t, code, 201, "create closed space")
	closedSpace := str(sp, "id")

	code, p := call(t, s.h, editor, "POST", "/api/v1/spaces/"+openSpace+"/pages", map[string]any{"title": "Radio protocol"})
	mustStatus(t, code, 201, "create open page")
	radio := str(p, "id")
	code, p = call(t, s.h, editor, "POST", "/api/v1/spaces/"+closedSpace+"/pages", map[string]any{"title": "Radio secrets"})
	mustStatus(t, code, 201, "create closed page")
	secretRadio := str(p, "id")
	code, p = call(t, s.h, editor, "POST", "/api/v1/spaces/"+openSpace+"/pages", map[string]any{"title": "Handbook"})
	mustStatus(t, code, 201, "create handbook")
	handbook := str(p, "id")

	// Search by title: security sees only the open space's page.
	code, body := call(t, s.h, security, "GET", "/api/v1/search?q=radio", nil)
	mustStatus(t, code, 200, "security searches")
	hits := list(body, "hits")
	if len(hits) != 1 || str(hits[0], "id") != radio {
		t.Fatalf("security should find only the open Radio page, got %v", hits)
	}
	code, body = call(t, s.h, editor, "GET", "/api/v1/search?q=radio", nil)
	mustStatus(t, code, 200, "editor searches")
	if hits = list(body, "hits"); len(hits) != 2 {
		t.Fatalf("editor should find both radio pages, got %v", hits)
	}
	_ = secretRadio

	// Body text search: fill Handbook's body through a compaction, with a
	// link to Radio.
	c1, _, _ := join(t, s, editor, handbook)
	c1.send(update("u1"))
	s.mgr.Tick(t.Context())
	msg := c1.control()
	if msg["type"] != "compact" {
		t.Fatalf("expected compact, got %v", msg)
	}
	c1.send(snapshot(int64(msg["upto"].(float64)), "the bananas chapter", []string{radio}, "state"))
	eventually(t, "body text indexed", func() bool {
		code, body := call(t, s.h, security, "GET", "/api/v1/search?q=bananas", nil)
		return code == 200 && len(list(body, "hits")) == 1
	})
	code, body = call(t, s.h, security, "GET", "/api/v1/search?q=bananas", nil)
	mustStatus(t, code, 200, "search by body text")
	hits = list(body, "hits")
	if str(hits[0], "id") != handbook || str(hits[0], "snippet") == "" {
		t.Fatalf("body search hit = %v", hits)
	}

	// Backlinks: Radio is linked from Handbook; access filtered.
	code, body = call(t, s.h, security, "GET", "/api/v1/pages/"+radio+"/backlinks", nil)
	mustStatus(t, code, 200, "security lists backlinks")
	if hits = list(body, "hits"); len(hits) != 1 || str(hits[0], "id") != handbook {
		t.Fatalf("backlinks = %v", hits)
	}
	code, _ = call(t, s.h, other, "GET", "/api/v1/pages/"+radio+"/backlinks", nil)
	mustStatus(t, code, 404, "olga lists backlinks of an invisible page")

	// Favourites: starring needs view access; the list hides what the
	// caller can no longer see.
	code, _ = call(t, s.h, other, "PUT", "/api/v1/me/favorites/"+radio, nil)
	mustStatus(t, code, 404, "olga stars an invisible page")
	code, _ = call(t, s.h, security, "PUT", "/api/v1/me/favorites/"+radio, nil)
	mustStatus(t, code, 204, "security stars Radio")
	code, body = call(t, s.h, security, "GET", "/api/v1/me/favorites", nil)
	mustStatus(t, code, 200, "security lists favourites")
	favs := list(body, "favorites")
	if len(favs) != 1 || str(favs[0], "page_id") != radio {
		t.Fatalf("favourites = %v", favs)
	}
	// The group grant goes; the favourite row stays but the list hides it.
	code, _ = call(t, s.h, editor, "PUT", "/api/v1/spaces/"+openSpace+"/grants",
		map[string]any{"principal_kind": "group", "principal": "dept-security", "level": ""})
	mustStatus(t, code, 200, "revoke the group grant")
	code, body = call(t, s.h, security, "GET", "/api/v1/me/favorites", nil)
	mustStatus(t, code, 200, "favourites after revoke")
	if favs = list(body, "favorites"); len(favs) != 0 {
		t.Fatalf("favourites should be hidden after revoke, got %v", favs)
	}
	code, body = call(t, s.h, security, "GET", "/api/v1/search?q=radio", nil)
	mustStatus(t, code, 200, "search after revoke")
	if hits = list(body, "hits"); len(hits) != 0 {
		t.Fatalf("search should be empty after revoke, got %v", hits)
	}
	// Unstar works even without access.
	code, _ = call(t, s.h, security, "DELETE", "/api/v1/me/favorites/"+radio, nil)
	mustStatus(t, code, 204, "security unstars")
}
