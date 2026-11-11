package http_test

// Public share links: anonymous read access behind unguessable tokens.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"testing"
)

// anonGet performs a request with no auth headers at all.
func anonGet(t *testing.T, h nethttp.Handler, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 && rec.Header().Get("Content-Type") == "application/json; charset=utf-8" {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

// TestShareLinks covers creation rules, the anonymous view, scope limits,
// the feature switch and revocation.
func TestShareLinks(t *testing.T) {
	deps := setupDeps(t)
	deps.Config.PublicLinksEnabled = true
	objects := &memObjects{data: map[string][]byte{}}
	s := startLive(t, deps, objects, t.TempDir())
	defer s.stop()
	h := s.h
	for _, id := range []identity{editor, security} {
		userID(t, h, id)
	}

	// Handbook (with child Radio) and a separate Secret page.
	code, sp := call(t, h, editor, "POST", "/api/v1/spaces", map[string]any{"name": "Docs"})
	mustStatus(t, code, 201, "create space")
	spaceID := str(sp, "id")
	code, _ = call(t, h, editor, "PUT", "/api/v1/spaces/"+spaceID+"/grants",
		map[string]any{"principal_kind": "group", "principal": "dept-security", "level": "view"})
	mustStatus(t, code, 200, "grant security view")
	code, p := call(t, h, editor, "POST", "/api/v1/spaces/"+spaceID+"/pages", map[string]any{"title": "Handbook"})
	mustStatus(t, code, 201, "create Handbook")
	handbook := str(p, "id")
	code, p = call(t, h, editor, "POST", "/api/v1/spaces/"+spaceID+"/pages", map[string]any{"title": "Radio", "parent_id": handbook})
	mustStatus(t, code, 201, "create Radio")
	radio := str(p, "id")
	code, p = call(t, h, editor, "POST", "/api/v1/spaces/"+spaceID+"/pages", map[string]any{"title": "Secret"})
	mustStatus(t, code, 201, "create Secret")
	secret := str(p, "id")

	// Content on Radio (straight into the buffer; no compaction needed)
	// and a file on Handbook, plus one on Secret.
	c1, _, _ := join(t, s, editor, radio)
	c1.send(update("radio-content"))
	code, f := uploadFile(t, h, editor, handbook, "map.png", "image/png", []byte("png-bytes"))
	mustStatus(t, code, 201, "upload to Handbook")
	handbookFile := str(f, "id")
	code, f = uploadFile(t, h, editor, secret, "secret.png", "image/png", []byte("secret-bytes"))
	mustStatus(t, code, 201, "upload to Secret")
	secretFile := str(f, "id")

	// Only full access creates links.
	code, _ = call(t, h, security, "POST", "/api/v1/pages/"+handbook+"/share-links",
		map[string]any{"include_children": true})
	mustStatus(t, code, 403, "view access creates a link")
	code, created := call(t, h, editor, "POST", "/api/v1/pages/"+handbook+"/share-links",
		map[string]any{"include_children": true})
	mustStatus(t, code, 201, "editor creates a link")
	token := str(created, "token")
	if token == "" {
		t.Fatalf("no token returned: %v", created)
	}
	linkID := str(created["link"].(map[string]any), "id")

	// Anonymous view: the root and its subtree, nothing else.
	rec, view := anonGet(t, h, "/api/public/v1/shares/"+token)
	mustStatus(t, rec.Code, 200, "anonymous share view")
	if rec.Header().Get("X-Robots-Tag") == "" {
		t.Fatalf("share responses must send X-Robots-Tag")
	}
	if str(view["page"].(map[string]any), "id") != handbook {
		t.Fatalf("share root = %v", view["page"])
	}
	if pages := list(view, "pages"); len(pages) != 1 || str(pages[0], "id") != radio {
		t.Fatalf("share subtree = %v", pages)
	}

	// Documents: in-scope pages serve their updates; others are 404.
	rec, doc := anonGet(t, h, "/api/public/v1/shares/"+token+"/pages/"+radio+"/doc")
	mustStatus(t, rec.Code, 200, "shared doc")
	raw, _ := doc["updates"].([]any)
	if len(raw) != 1 {
		t.Fatalf("expected one update, got %v", doc)
	}
	if b, _ := base64.StdEncoding.DecodeString(raw[0].(string)); string(b) != "radio-content" {
		t.Fatalf("shared doc bytes = %q", b)
	}
	rec, _ = anonGet(t, h, "/api/public/v1/shares/"+token+"/pages/"+secret+"/doc")
	mustStatus(t, rec.Code, 404, "out-of-scope doc")

	// Files: in scope streams, out of scope is 404.
	rec, _ = anonGet(t, h, "/api/public/v1/shares/"+token+"/files/"+handbookFile)
	mustStatus(t, rec.Code, 200, "shared file")
	if rec.Body.String() != "png-bytes" {
		t.Fatalf("shared file bytes = %q", rec.Body.String())
	}
	// "png-bytes" is not a PNG, so it was stored as plain bytes and
	// downloads as a sandboxed attachment.
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="map.png"` {
		t.Fatalf("shared file content disposition = %q", cd)
	}
	if rec.Header().Get("Content-Security-Policy") != "sandbox" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("shared file misses the sandbox CSP or nosniff: %v", rec.Header())
	}
	rec, _ = anonGet(t, h, "/api/public/v1/shares/"+token+"/files/"+secretFile)
	mustStatus(t, rec.Code, 404, "out-of-scope file")

	// Garbage tokens and the feature switch.
	rec, _ = anonGet(t, h, "/api/public/v1/shares/wsl_notarealtokenatall123456")
	mustStatus(t, rec.Code, 404, "unknown token")
	deps.Config.PublicLinksEnabled = false
	rec, _ = anonGet(t, h, "/api/public/v1/shares/"+token)
	mustStatus(t, rec.Code, 404, "feature off")
	deps.Config.PublicLinksEnabled = true

	// Expiry and revocation read as 404.
	if _, err := deps.Pool.Exec(context.Background(),
		`UPDATE page_share_links SET expires_at = now() - interval '1 hour' WHERE id = $1`, linkID); err != nil {
		t.Fatalf("expire link: %v", err)
	}
	rec, _ = anonGet(t, h, "/api/public/v1/shares/"+token)
	mustStatus(t, rec.Code, 404, "expired link")
	if _, err := deps.Pool.Exec(context.Background(),
		`UPDATE page_share_links SET expires_at = NULL WHERE id = $1`, linkID); err != nil {
		t.Fatalf("unexpire link: %v", err)
	}
	rec, _ = anonGet(t, h, "/api/public/v1/shares/"+token)
	mustStatus(t, rec.Code, 200, "live again")
	code, _ = call(t, h, editor, "DELETE", "/api/v1/share-links/"+linkID, nil)
	mustStatus(t, code, 204, "revoke the link")
	rec, _ = anonGet(t, h, "/api/public/v1/shares/"+token)
	mustStatus(t, rec.Code, 404, "revoked link")

	// The listing shows the link without its token.
	code, body := call(t, h, editor, "GET", "/api/v1/pages/"+handbook+"/share-links", nil)
	mustStatus(t, code, 200, "list links")
	links := list(body, "links")
	if len(links) != 1 || str(links[0], "revoked_at") == "" {
		t.Fatalf("links = %v", links)
	}
	if _, hasToken := links[0]["token"]; hasToken {
		t.Fatalf("the token must never be listed")
	}
}
