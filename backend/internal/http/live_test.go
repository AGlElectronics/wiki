package http_test

// Live editing over real websockets, against the same disposable database
// as wiki_test.go (TEST_DATABASE_URL). The server never decodes Yjs, so
// plain bytes stand in for updates and snapshots.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	httpx "github.com/AGlElectronics/wiki/backend/internal/http"
	"github.com/AGlElectronics/wiki/backend/internal/live"
	"github.com/AGlElectronics/wiki/backend/internal/wiki"
)

// memObjects is an in-memory bucket.
type memObjects struct {
	mu   sync.Mutex
	data map[string][]byte
	// failRemove makes RemovePrefix fail, like an unreachable bucket.
	failRemove bool
}

func (o *memObjects) Put(_ context.Context, key string, data []byte, _ string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.data[key] = append([]byte(nil), data...)
	return nil
}

func (o *memObjects) Get(_ context.Context, key string) ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	d, ok := o.data[key]
	if !ok {
		return nil, fmt.Errorf("no object %s", key)
	}
	return d, nil
}

func (o *memObjects) Remove(_ context.Context, key string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.data, key)
	return nil
}

func (o *memObjects) RemovePrefix(_ context.Context, prefix string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.failRemove {
		return fmt.Errorf("bucket unreachable")
	}
	for k := range o.data {
		if strings.HasPrefix(k, prefix) {
			delete(o.data, k)
		}
	}
	return nil
}

// PutObject and GetObject adapt the byte API to wiki.FileObjects (uploads).
func (o *memObjects) PutObject(ctx context.Context, key string, r io.Reader, _ int64, contentType string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return o.Put(ctx, key, data, contentType)
}

func (o *memObjects) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	data, err := o.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (o *memObjects) keys(prefix string) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []string
	for k := range o.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

// liveServer is one backend process: a manager and its HTTP server.
type liveServer struct {
	mgr *live.Manager
	svc *wiki.Service
	h   nethttp.Handler
	srv *httptest.Server
}

func startLive(t *testing.T, deps httpx.Deps, objects *memObjects, dir string) *liveServer {
	t.Helper()
	store, err := live.NewStore(deps.Pool, objects, dir, zerolog.Nop())
	if err != nil {
		t.Fatalf("live store: %v", err)
	}
	svc := wiki.NewService(deps.Pool)
	mgr := live.NewManager(store, svc, nil, zerolog.Nop())
	mgr.SetRecheckInterval(100 * time.Millisecond)
	if err := mgr.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	deps.Wiki, deps.Live, deps.Files = svc, mgr, wiki.NewFiles(svc, objects)
	h := httpx.NewRouter(deps, "test")
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &liveServer{mgr: mgr, svc: svc, h: h, srv: srv}
}

func (s *liveServer) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.mgr.Shutdown(ctx)
	s.srv.Close()
}

type liveClient struct {
	t  *testing.T
	ws *websocket.Conn
}

func ticket(t *testing.T, s *liveServer, id identity, page string) string {
	t.Helper()
	code, body := call(t, s.h, id, "POST", "/api/v1/pages/"+page+"/live-ticket", nil)
	mustStatus(t, code, 200, "live ticket for "+id.user)
	return str(body, "ticket")
}

func liveURL(s *liveServer, page, tk string) string {
	return "ws" + strings.TrimPrefix(s.srv.URL, "http") + "/api/v1/pages/" + page + "/live?ticket=" + url.QueryEscape(tk)
}

// join connects and returns the stored updates and the synced level.
func join(t *testing.T, s *liveServer, id identity, page string) (*liveClient, [][]byte, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, liveURL(s, page, ticket(t, s, id, page)), nil)
	if err != nil {
		t.Fatalf("%s joins: %v", id.user, err)
	}
	ws.SetReadLimit(64 << 20)
	c := &liveClient{t: t, ws: ws}
	t.Cleanup(func() { _ = ws.CloseNow() })

	typ, data := c.next()
	if typ != websocket.MessageBinary || len(data) == 0 || data[0] != 2 {
		t.Fatalf("%s: first frame should be a batch, got %v %q", id.user, typ, data)
	}
	var updates [][]byte
	for b := data[1:]; len(b) > 0; {
		n := int(binary.BigEndian.Uint32(b))
		updates = append(updates, b[4:4+n])
		b = b[4+n:]
	}
	synced := c.control()
	if synced["type"] != "synced" {
		t.Fatalf("%s: expected synced, got %v", id.user, synced)
	}
	return c, updates, str(synced, "level")
}

func (c *liveClient) next() (websocket.MessageType, []byte) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, data, err := c.ws.Read(ctx)
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	return typ, data
}

func (c *liveClient) control() map[string]any {
	c.t.Helper()
	typ, data := c.next()
	if typ != websocket.MessageText {
		c.t.Fatalf("expected a control message, got binary %q", data)
	}
	out := map[string]any{}
	if err := json.Unmarshal(data, &out); err != nil {
		c.t.Fatalf("control json: %v", err)
	}
	return out
}

func (c *liveClient) send(frame []byte) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageBinary, frame); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *liveClient) expectUpdate(want string) {
	c.t.Helper()
	typ, data := c.next()
	if typ != websocket.MessageBinary || len(data) == 0 || data[0] != 1 || string(data[1:]) != want {
		c.t.Fatalf("expected update %q, got %v %q", want, typ, data)
	}
}

// expectClose reads until the server closes and checks the code.
func (c *liveClient) expectClose(want websocket.StatusCode) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, _, err := c.ws.Read(ctx)
		if err == nil {
			continue
		}
		if got := websocket.CloseStatus(err); got != want {
			c.t.Fatalf("expected close %d, got %d (%v)", want, got, err)
		}
		return
	}
}

func update(s string) []byte { return append([]byte{1}, s...) }

func awareness(client, clock uint64, state string) []byte {
	out := []byte{3}
	out = binary.AppendUvarint(out, 1)
	out = binary.AppendUvarint(out, client)
	out = binary.AppendUvarint(out, clock)
	out = binary.AppendUvarint(out, uint64(len(state)))
	return append(out, state...)
}

func snapshot(upto int64, text string, links []string, state string) []byte {
	hdr, _ := json.Marshal(map[string]any{"upto": upto, "text": text, "links": links})
	out := []byte{5}
	out = binary.BigEndian.AppendUint32(out, uint32(len(hdr)))
	out = append(out, hdr...)
	return append(out, state...)
}

func strs(b [][]byte) []string {
	out := make([]string, len(b))
	for i, v := range b {
		out[i] = string(v)
	}
	return out
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestLiveEditing relays edits between browsers, keeps them across a
// restart, compacts them into a snapshot and follows access changes.
func TestLiveEditing(t *testing.T) {
	deps := setupDeps(t)
	objects := &memObjects{data: map[string][]byte{}}
	dir := t.TempDir()
	s := startLive(t, deps, objects, dir)
	ctx := context.Background()

	for _, id := range []identity{editor, security, other} {
		userID(t, s.h, id)
	}
	olgaID := userID(t, s.h, other)
	code, sp := call(t, s.h, editor, "POST", "/api/v1/spaces", map[string]any{"name": "Ops"})
	mustStatus(t, code, 201, "create space")
	spaceID := str(sp, "id")
	code, _ = call(t, s.h, editor, "PUT", "/api/v1/spaces/"+spaceID+"/grants",
		map[string]any{"principal_kind": "group", "principal": "dept-security", "level": "view"})
	mustStatus(t, code, 200, "grant security view")
	code, p := call(t, s.h, editor, "POST", "/api/v1/spaces/"+spaceID+"/pages", map[string]any{"title": "Runbook"})
	mustStatus(t, code, 201, "create page")
	page := str(p, "id")
	code, p = call(t, s.h, editor, "POST", "/api/v1/spaces/"+spaceID+"/pages", map[string]any{"title": "Linked"})
	mustStatus(t, code, 201, "create linked page")
	linked := str(p, "id")

	// Tickets: only for visible pages, and good for one connection.
	code, _ = call(t, s.h, other, "POST", "/api/v1/pages/"+page+"/live-ticket", nil)
	mustStatus(t, code, 404, "olga asks for a ticket to a page she cannot see")
	tk := ticket(t, s, editor, page)
	dctx, dcancel := context.WithTimeout(ctx, 5*time.Second)
	ws, _, err := websocket.Dial(dctx, liveURL(s, page, tk), nil)
	dcancel()
	if err != nil {
		t.Fatalf("dial with a fresh ticket: %v", err)
	}
	_ = ws.CloseNow()
	dctx, dcancel = context.WithTimeout(ctx, 5*time.Second)
	_, resp, err := websocket.Dial(dctx, liveURL(s, page, tk), nil)
	dcancel()
	if err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("a used ticket must be refused with 401, got %v %v", resp, err)
	}

	// Edits relay between editors; a viewer gets them but cannot send.
	a, stored, level := join(t, s, editor, page)
	if len(stored) != 0 || level != "full" {
		t.Fatalf("new page: stored=%q level=%s", strs(stored), level)
	}
	b, _, level := join(t, s, security, page)
	if level != "view" {
		t.Fatalf("security level = %s, want view", level)
	}
	a.send(update("u1"))
	b.expectUpdate("u1")
	b.send(update("evil"))
	if msg := b.control(); msg["type"] != "error" {
		t.Fatalf("viewer edit should be refused, got %v", msg)
	}
	a.send(update("u2"))
	b.expectUpdate("u2")

	// Presence: awareness is relayed, and cleared when the viewer leaves.
	b.send(awareness(77, 3, `{"user":{"name":"Sam"}}`))
	typ, data := a.next()
	if typ != websocket.MessageBinary || !bytes.Equal(data, awareness(77, 3, `{"user":{"name":"Sam"}}`)) {
		t.Fatalf("awareness not relayed: %q", data)
	}
	_ = b.ws.Close(websocket.StatusNormalClosure, "")
	typ, data = a.next()
	if typ != websocket.MessageBinary || !bytes.Equal(data, awareness(77, 4, "null")) {
		t.Fatalf("leaving should clear presence, got %q", data)
	}

	// Edits ship to S3 as segments.
	s.mgr.Tick(ctx)
	prefix := "wiki/pages/" + page + "/"
	if segs := objects.keys(prefix + "updates/"); len(segs) != 1 {
		t.Fatalf("expected one shipped segment, got %v", segs)
	}

	// Edits buffered at shutdown ship then, and a restart loses nothing.
	a.send(update("u3"))
	c, stored, _ := join(t, s, editor, page)
	if len(stored) == 2 {
		c.expectUpdate("u3")
	}
	// Shutdown waits for browsers to answer the close, as they do while
	// reading.
	stopped := make(chan struct{})
	go func() {
		s.stop()
		close(stopped)
	}()
	a.expectClose(websocket.StatusServiceRestart)
	c.expectClose(websocket.StatusServiceRestart)
	<-stopped
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Fatalf("shutdown should ship the buffer, left %v", left)
	}
	s = startLive(t, deps, objects, dir)
	a, stored, _ = join(t, s, editor, page)
	if got := strs(stored); strings.Join(got, ",") != "u1,u2,u3" {
		t.Fatalf("after restart stored = %v", got)
	}

	// A crash leaves the buffer on disk; the next start ships it.
	if err := os.WriteFile(filepath.Join(dir, page+".log"), recordBytes(4, "u4"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = a.ws.CloseNow()
	crashed := s
	s = startLive(t, deps, objects, dir)
	crashed.srv.Close()
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Fatalf("recover should ship leftover buffers, left %v", left)
	}
	a, stored, _ = join(t, s, editor, page)
	if got := strs(stored); strings.Join(got, ",") != "u1,u2,u3,u4" {
		t.Fatalf("after recovery stored = %v", got)
	}

	// Compaction: an editor is asked for a snapshot once the page is quiet.
	s.mgr.SetCompactIdle(0)
	s.mgr.Tick(ctx)
	msg := a.control()
	if msg["type"] != "compact" || msg["upto"] != float64(4) {
		t.Fatalf("expected compact upto 4, got %v", msg)
	}
	a.send(snapshot(4, "hello world", []string{linked, "not-a-uuid"}, "STATE"))
	eventually(t, "snapshot saved", func() bool {
		var upto int64
		_ = deps.Pool.QueryRow(ctx, `SELECT upto_seq FROM page_snapshots WHERE page_id = $1`, page).Scan(&upto)
		return upto == 4
	})
	if segs := objects.keys(prefix + "updates/"); len(segs) != 0 {
		t.Fatalf("compaction should delete covered segments, left %v", segs)
	}
	var body string
	var links int
	if err := deps.Pool.QueryRow(ctx, `SELECT body_text FROM page_search WHERE page_id = $1`, page).Scan(&body); err != nil || body != "hello world" {
		t.Fatalf("search text = %q (%v)", body, err)
	}
	if err := deps.Pool.QueryRow(ctx, `SELECT count(*) FROM page_links WHERE from_page = $1 AND to_page = $2`, page, linked).Scan(&links); err != nil || links != 1 {
		t.Fatalf("links = %d (%v)", links, err)
	}
	a.send(update("u5"))
	c, stored, _ = join(t, s, editor, page)
	if got := strs(stored); strings.Join(got, ",") != "STATE,u5" {
		t.Fatalf("after compaction stored = %v", got)
	}
	_ = c.ws.CloseNow()

	// Access follows grants while connected.
	code, _ = call(t, s.h, editor, "PUT", "/api/v1/pages/"+page+"/grants",
		map[string]any{"principal_kind": "user", "principal": olgaID, "level": "view"})
	mustStatus(t, code, 200, "share with olga (view)")
	o, _, level := join(t, s, other, page)
	if level != "view" {
		t.Fatalf("olga level = %s", level)
	}
	code, _ = call(t, s.h, editor, "PUT", "/api/v1/pages/"+page+"/grants",
		map[string]any{"principal_kind": "user", "principal": olgaID, "level": "edit"})
	mustStatus(t, code, 200, "olga can edit")
	if msg := o.control(); msg["type"] != "access" || msg["level"] != "edit" {
		t.Fatalf("expected access edit, got %v", msg)
	}
	o.send(update("from-olga"))
	a.expectUpdate("from-olga")
	code, _ = call(t, s.h, editor, "PUT", "/api/v1/pages/"+page+"/grants",
		map[string]any{"principal_kind": "user", "principal": olgaID, "level": ""})
	mustStatus(t, code, 200, "unshare")
	o.expectClose(4404)

	// Deleting forever closes the page and removes its content.
	code, _ = call(t, s.h, editor, "POST", "/api/v1/pages/"+page+"/trash", nil)
	mustStatus(t, code, 204, "trash")
	a.expectClose(4404)
	code, _ = call(t, s.h, editor, "DELETE", "/api/v1/pages/"+page, nil)
	mustStatus(t, code, 204, "delete forever")
	eventually(t, "content removed", func() bool { return len(objects.keys(prefix)) == 0 })

	s.stop()
}

func recordBytes(seq uint64, data string) []byte {
	out := binary.BigEndian.AppendUint64(nil, seq)
	out = binary.BigEndian.AppendUint32(out, uint32(len(data)))
	return append(out, data...)
}
