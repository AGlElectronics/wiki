package http_test

// Uploads and downloads against the same disposable database
// (TEST_DATABASE_URL) and an in-memory bucket.

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	nethttp "net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"
	"time"
)

// uploadFile posts one multipart file as the given identity.
func uploadFile(t *testing.T, h nethttp.Handler, id identity, page, filename, contentType string, data []byte) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	hdr.Set("Content-Type", contentType)
	part, err := w.CreatePart(hdr)
	if err != nil {
		t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/v1/pages/"+page+"/files", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-Dev-User", id.user)
	req.Header.Set("X-Dev-Groups", id.groups)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

// rawGet performs one GET and returns the raw response.
func rawGet(t *testing.T, h nethttp.Handler, id identity, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("X-Dev-User", id.user)
	req.Header.Set("X-Dev-Groups", id.groups)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestFiles covers upload access, downloads riding on page access, and the
// S3 cleanup when a page is deleted forever.
func TestFiles(t *testing.T) {
	deps := setupDeps(t)
	objects := &memObjects{data: map[string][]byte{}}
	s := startLive(t, deps, objects, t.TempDir())
	defer s.stop()
	h := s.h
	for _, id := range []identity{admin, editor, security, other} {
		userID(t, h, id)
	}

	// A space readable by dept-security, with one page.
	code, sp := call(t, h, editor, "POST", "/api/v1/spaces", map[string]any{"name": "Docs"})
	mustStatus(t, code, 201, "create space")
	spaceID := str(sp, "id")
	code, _ = call(t, h, editor, "PUT", "/api/v1/spaces/"+spaceID+"/grants",
		map[string]any{"principal_kind": "group", "principal": "dept-security", "level": "view"})
	mustStatus(t, code, 200, "grant dept-security view")
	code, pg := call(t, h, editor, "POST", "/api/v1/spaces/"+spaceID+"/pages", map[string]any{"title": "Page"})
	mustStatus(t, code, 201, "create page")
	pageID := str(pg, "id")

	payload := []byte("\x89PNG fake image bytes")

	// View access cannot upload; edit access can.
	code, _ = uploadFile(t, h, security, pageID, "pic.png", "image/png", payload)
	mustStatus(t, code, 403, "view-only upload")
	code, f := uploadFile(t, h, editor, pageID, "pic.png", "image/png", payload)
	mustStatus(t, code, 201, "editor uploads")
	fileURL := str(f, "url")
	if fileURL == "" {
		t.Fatalf("upload response has no url: %v", f)
	}

	// Download rides on page access: view works, no access is a 404.
	rec := rawGet(t, h, security, fileURL)
	mustStatus(t, rec.Code, 200, "security downloads")
	if !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Fatalf("downloaded bytes differ")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `inline; filename="pic.png"` {
		t.Fatalf("content disposition = %q", cd)
	}
	rec = rawGet(t, h, other, fileURL)
	mustStatus(t, rec.Code, 404, "no access downloads")

	// An attachment (non-image) downloads as an attachment.
	code, f2 := uploadFile(t, h, editor, pageID, "notes.pdf", "application/pdf", []byte("pdf"))
	mustStatus(t, code, 201, "editor uploads a pdf")
	rec = rawGet(t, h, editor, str(f2, "url"))
	mustStatus(t, rec.Code, 200, "pdf downloads")
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="notes.pdf"` {
		t.Fatalf("pdf content disposition = %q", cd)
	}

	if keys := objects.keys("wiki/files/" + pageID + "/"); len(keys) != 2 {
		t.Fatalf("bucket should hold 2 uploads, got %v", keys)
	}

	// Delete forever removes the metadata (cascade) and the S3 objects.
	code, _ = call(t, h, editor, "POST", "/api/v1/pages/"+pageID+"/trash", nil)
	mustStatus(t, code, 204, "trash the page")
	code, _ = call(t, h, editor, "DELETE", "/api/v1/pages/"+pageID, nil)
	mustStatus(t, code, 204, "delete the page forever")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if keys := objects.keys("wiki/files/" + pageID + "/"); len(keys) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("uploads still in the bucket after delete forever: %v", objects.keys("wiki/files/"+pageID+"/"))
		}
		time.Sleep(50 * time.Millisecond)
	}
	rec = rawGet(t, h, editor, fileURL)
	mustStatus(t, rec.Code, 404, "download after delete forever")
}
