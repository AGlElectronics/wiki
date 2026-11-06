package live

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/AGlElectronics/wiki/backend/internal/domain"
)

const (
	// shipEvery is how often buffered edits go to S3.
	shipEvery = 5 * time.Second
	// Compaction is requested when a page has been quiet this long, or has
	// this many segments or buffered updates.
	compactIdle     = 30 * time.Second
	compactSegments = 50
	compactRecords  = 2000
	// compactTimeout frees a compaction request nobody answered.
	compactTimeout = time.Minute
	// maxBufferBytes pauses editing when S3 has been unreachable for long.
	maxBufferBytes = 64 << 20
)

// hub is one page with people on it. Every change to its state happens
// under mu, so all browsers see updates in the order they were logged.
type hub struct {
	id    uuid.UUID
	store *Store

	ready   chan struct{}
	loadErr error
	refs    int // guarded by Manager.mu

	// shipping serializes rotate + upload for this page.
	shipping sync.Mutex

	mu         sync.Mutex
	conns      map[*conn]struct{}
	snapshot   []byte
	snapUpto   int64
	records    []record
	nextSeq    int64
	segments   int
	pending    int64 // bytes on disk not yet shipped
	log        *os.File
	lastUpdate time.Time
	compactor  *conn
	askedAt    time.Time
}

func (h *hub) load(ctx context.Context) error {
	l, err := h.store.load(ctx, h.id)
	if err != nil {
		return err
	}
	h.snapshot, h.snapUpto, h.records, h.segments = l.snapshot, l.snapUpto, l.records, l.segments
	h.nextSeq = l.lastSeq() + 1
	return nil
}

// join registers a browser and queues the stored document for it.
func (h *hub) join(c *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	updates := make([][]byte, 0, len(h.records)+1)
	if h.snapshot != nil {
		updates = append(updates, h.snapshot)
	}
	for _, r := range h.records {
		updates = append(updates, r.data)
	}
	c.send(websocket.MessageBinary, batchFrame(updates))
	c.control(map[string]any{"type": "synced", "level": c.level, "seq": h.nextSeq - 1})
	for other := range h.conns {
		if other.awareness != nil {
			c.send(websocket.MessageBinary, other.awareness)
		}
	}
	h.conns[c] = struct{}{}
}

func (h *hub) leave(c *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.conns, c)
	if h.compactor == c {
		h.compactor = nil
	}
	if len(c.clients) > 0 {
		h.broadcast(c, awarenessRemoval(c.clients))
	}
}

// broadcast sends a frame to everyone but from. Caller holds mu.
func (h *hub) broadcast(from *conn, frame []byte) {
	for other := range h.conns {
		if other != from {
			other.send(websocket.MessageBinary, frame)
		}
	}
}

var errReadOnly = errors.New("read-only")

// update logs a browser's change and forwards it.
func (h *hub) update(c *conn, data []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.level < domain.LevelEdit {
		return errReadOnly
	}
	select {
	case <-c.done:
		// Closing (often for a restart); the browser resends it on reconnect.
		return nil
	default:
	}
	if h.pending > maxBufferBytes {
		c.close(websocket.StatusTryAgainLater, "saving is behind; reconnecting shortly")
		return nil
	}
	if h.log == nil {
		f, err := os.OpenFile(h.store.logPath(h.id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		h.log = f
	}
	r := record{seq: h.nextSeq, data: data}
	buf := appendRecord(nil, r)
	if _, err := h.log.Write(buf); err != nil {
		return err
	}
	h.nextSeq++
	h.records = append(h.records, r)
	h.pending += int64(len(buf))
	h.lastUpdate = time.Now()
	h.broadcast(c, append([]byte{frameUpdate}, data...))
	return nil
}

func (h *hub) awareness(c *conn, frame []byte) {
	clients, err := awarenessClients(frame[1:])
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, clock := range clients {
		c.clients[id] = clock
	}
	c.awareness = frame
	h.broadcast(c, frame)
}

// saveSnapshot stores a compacted document sent by the browser asked for it.
func (h *hub) saveSnapshot(ctx context.Context, c *conn, body []byte) error {
	rawHeader, state, err := splitSnapshot(body)
	if err != nil {
		return err
	}
	var hdr snapshotHeader
	if err := json.Unmarshal(rawHeader, &hdr); err != nil {
		return errBadFrame
	}
	h.mu.Lock()
	ok := h.compactor == c && c.level >= domain.LevelEdit && hdr.Upto > h.snapUpto && hdr.Upto < h.nextSeq
	prev, prevUpto := h.snapshot, h.snapUpto
	h.mu.Unlock()
	if !ok {
		return nil
	}

	remaining, err := h.store.saveSnapshot(ctx, h.id, prev, prevUpto, hdr.Upto, state, hdr)

	h.mu.Lock()
	defer h.mu.Unlock()
	h.compactor = nil
	if err != nil {
		return err
	}
	h.snapshot, h.snapUpto, h.segments = state, hdr.Upto, remaining
	i := 0
	for i < len(h.records) && h.records[i].seq <= hdr.Upto {
		i++
	}
	h.records = append([]record(nil), h.records[i:]...)
	return nil
}

// ship rotates the buffer file and uploads everything waiting.
func (h *hub) ship(ctx context.Context) error {
	h.shipping.Lock()
	defer h.shipping.Unlock()
	h.mu.Lock()
	if h.log != nil {
		_ = h.log.Close()
		h.log = nil
	}
	err := h.store.rotate(h.id)
	h.mu.Unlock()
	if err != nil {
		return err
	}

	n, shipped, err := h.store.ship(ctx, h.id)
	h.mu.Lock()
	h.segments += n
	h.pending -= shipped
	h.mu.Unlock()
	return err
}

// maybeCompact asks one editor on the page for a snapshot when the log
// since the last one has grown or the page went quiet.
func (h *hub) maybeCompact(idle time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.records) == 0 {
		return
	}
	if h.compactor != nil && time.Since(h.askedAt) < compactTimeout {
		return
	}
	due := time.Since(h.lastUpdate) >= idle || h.segments >= compactSegments || len(h.records) >= compactRecords
	if !due {
		return
	}
	for c := range h.conns {
		if c.level >= domain.LevelEdit {
			h.compactor, h.askedAt = c, time.Now()
			c.control(map[string]any{"type": "compact", "upto": h.nextSeq - 1})
			return
		}
	}
}

// setLevel changes a connection's access after a re-check.
func (h *hub) setLevel(c *conn, l domain.Level) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.level == l {
		return
	}
	c.level = l
	if h.compactor == c && l < domain.LevelEdit {
		h.compactor = nil
	}
	c.control(map[string]any{"type": "access", "level": l})
}

func (h *hub) closeAll(code websocket.StatusCode, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		c.close(code, reason)
	}
}

func (h *hub) closeLog() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.log != nil {
		_ = h.log.Close()
		h.log = nil
	}
}
