package live

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/AGlElectronics/wiki/backend/internal/domain"
	"github.com/AGlElectronics/wiki/backend/internal/httperror"
	"github.com/AGlElectronics/wiki/backend/internal/wiki"
)

const (
	pingEvery    = 30 * time.Second
	writeTimeout = 10 * time.Second
	// A browser sends a fresh ticket every minute; without one for this
	// long its groups may be stale, so it must reconnect.
	refreshGrace = 3 * time.Minute
	outQueue     = 512
	// Token bucket per connection: typing sends an update and a cursor
	// move per keystroke.
	rateLimit = 200.0
	rateBurst = 400.0
)

// Close codes the browser acts on: reconnect for 4401, give up for 4404,
// reload the document fresh for 4409 (a revision was restored, so the
// browser's copy belongs to a replaced timeline and must be dropped).
const (
	closeExpired  websocket.StatusCode = 4401
	closeNoAccess websocket.StatusCode = 4404
	closeRestored websocket.StatusCode = 4409
)

type outMsg struct {
	typ  websocket.MessageType
	data []byte
}

// conn is one browser on a page.
type conn struct {
	m    *Manager
	h    *hub
	ws   *websocket.Conn
	out  chan outMsg
	done chan struct{}
	once sync.Once

	// Guarded by h.mu.
	level     domain.Level
	awareness []byte
	clients   map[uint64]uint64

	mu          sync.Mutex
	caller      wiki.Caller
	refreshedAt time.Time

	tokens   float64
	lastTick time.Time
	warned   bool
}

// send queues a frame. A browser that cannot keep up is disconnected
// rather than slowing the page down for everyone.
func (c *conn) send(typ websocket.MessageType, data []byte) {
	select {
	case <-c.done:
	case c.out <- outMsg{typ: typ, data: data}:
	default:
		c.close(websocket.StatusPolicyViolation, "connection too slow")
	}
}

func (c *conn) control(v map[string]any) {
	b, _ := json.Marshal(v)
	c.send(websocket.MessageText, b)
}

func (c *conn) close(code websocket.StatusCode, reason string) {
	c.once.Do(func() {
		close(c.done)
		go func() { _ = c.ws.Close(code, reason) }()
	})
}

func (c *conn) writer() {
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	for {
		select {
		case <-c.done:
			return
		case msg := <-c.out:
			ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			err := c.ws.Write(ctx, msg.typ, msg.data)
			cancel()
			if err != nil {
				c.close(websocket.StatusGoingAway, "")
				return
			}
		case <-ping.C:
			ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			err := c.ws.Ping(ctx)
			cancel()
			if err != nil {
				c.close(websocket.StatusGoingAway, "")
				return
			}
		}
	}
}

func (c *conn) allow() bool {
	now := time.Now()
	if c.lastTick.IsZero() {
		c.tokens = rateBurst
	} else {
		c.tokens = min(rateBurst, c.tokens+now.Sub(c.lastTick).Seconds()*rateLimit)
	}
	c.lastTick = now
	if c.tokens < 1 {
		return false
	}
	c.tokens--
	return true
}

// readLoop runs until the socket closes. Its context is never cancelled:
// that would make the library close with its own code instead of ours.
func (c *conn) readLoop() {
	ctx := context.Background()
	c.ws.SetReadLimit(maxSnapshotBytes + 1<<16)
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		if !c.allow() {
			c.close(websocket.StatusPolicyViolation, "too many messages")
			return
		}
		if typ == websocket.MessageText {
			c.handleControl(data)
			continue
		}
		if len(data) == 0 {
			continue
		}
		switch data[0] {
		case frameUpdate:
			if len(data)-1 > maxUpdateBytes {
				c.close(websocket.StatusMessageTooBig, "change too large")
				return
			}
			err := c.h.update(c, data[1:])
			if errors.Is(err, errReadOnly) {
				if !c.warned {
					c.warned = true
					c.control(map[string]any{"type": "error", "message": "You can only view this page; your changes are not saved."})
				}
			} else if err != nil {
				c.m.log.Error().Err(err).Str("page", c.h.id.String()).Msg("could not buffer an edit")
				c.close(websocket.StatusInternalError, "could not save the change")
				return
			}
		case frameAwareness:
			c.h.awareness(c, data)
		case frameSnapshot:
			sctx, scancel := context.WithTimeout(ctx, 30*time.Second)
			err := c.h.saveSnapshot(sctx, c, data[1:])
			scancel()
			if err != nil {
				c.m.log.Error().Err(err).Str("page", c.h.id.String()).Msg("could not save a page snapshot")
			}
		}
	}
}

func (c *conn) handleControl(data []byte) {
	var msg struct {
		Type   string `json:"type"`
		Ticket string `json:"ticket"`
	}
	if json.Unmarshal(data, &msg) != nil || msg.Type != "refresh" {
		return
	}
	caller, ok := c.m.tickets.redeem(msg.Ticket, c.h.id)
	c.mu.Lock()
	if !ok || caller.UserID != c.caller.UserID {
		c.mu.Unlock()
		return
	}
	c.caller, c.refreshedAt = caller, time.Now()
	c.mu.Unlock()
	go c.recheck()
}

// watch re-checks the person's access while the page is open, so removing
// a grant or a group closes the editor within a minute.
func (c *conn) watch() {
	t := time.NewTicker(c.m.recheckEvery)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			c.recheck()
		}
	}
}

func (c *conn) recheck() {
	c.mu.Lock()
	caller, refreshed := c.caller, c.refreshedAt
	c.mu.Unlock()
	if time.Since(refreshed) > refreshGrace {
		c.close(closeExpired, "session expired")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := c.m.svc.GetPage(ctx, caller, c.h.id)
	var he *httperror.Error
	switch {
	case errors.As(err, &he) && he.Code == httperror.CodeNotFound:
		c.close(closeNoAccess, "page not available")
	case err != nil:
		c.m.log.Warn().Err(err).Msg("live access re-check failed")
	default:
		c.h.setLevel(c, p.Access)
	}
}
