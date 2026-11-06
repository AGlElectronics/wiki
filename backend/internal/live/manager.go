package live

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/AGlElectronics/wiki/backend/internal/domain"
	"github.com/AGlElectronics/wiki/backend/internal/httperror"
	"github.com/AGlElectronics/wiki/backend/internal/wiki"
)

var errClosing = httperror.Unavailable("the server is restarting")

// closeWait bounds how long Shutdown waits for browsers to answer the
// close before shipping.
const closeWait = 2 * time.Second

// Manager owns every open page. One backend replica runs one Manager.
type Manager struct {
	store        *Store
	svc          *wiki.Service
	log          zerolog.Logger
	tickets      *tickets
	origins      []string
	recheckEvery time.Duration
	compactIdle  time.Duration

	mu      sync.Mutex
	hubs    map[uuid.UUID]*hub
	closing bool
	// blocked holds pages mid-restore; connections wait and retry.
	blocked map[uuid.UUID]bool
	// serving counts open connections, so Shutdown can wait for them.
	serving sync.WaitGroup
}

// NewManager wires the live editing service. allowedOrigins are the
// browser origins allowed to open websockets besides the API's own host.
func NewManager(store *Store, svc *wiki.Service, allowedOrigins []string, log zerolog.Logger) *Manager {
	var origins []string
	for _, o := range allowedOrigins {
		if u, err := url.Parse(o); err == nil && u.Host != "" {
			origins = append(origins, u.Host)
		}
	}
	m := &Manager{
		store: store, svc: svc, log: log, tickets: newTickets(), origins: origins,
		recheckEvery: time.Minute, compactIdle: compactIdle, hubs: map[uuid.UUID]*hub{},
		blocked: map[uuid.UUID]bool{},
	}
	svc.OnPagesDeleted(m.forget)
	return m
}

// SetRecheckInterval changes how often open connections re-check access
// (tests only).
func (m *Manager) SetRecheckInterval(d time.Duration) { m.recheckEvery = d }

// SetCompactIdle changes how long a page must be quiet before compaction
// (tests only).
func (m *Manager) SetCompactIdle(d time.Duration) { m.compactIdle = d }

// Recover ships edits a previous run left in the buffer directory. Call it
// before serving.
func (m *Manager) Recover(ctx context.Context) error { return m.store.recover(ctx) }

// Run ships buffered edits and requests compactions until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(shipEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Tick(ctx)
		}
	}
}

// Tick runs one ship-and-compact round (Run calls it every few seconds).
func (m *Manager) Tick(ctx context.Context) {
	m.tickets.sweep()
	for _, h := range m.openHubs() {
		// A hub idle before shipping has nothing new after it.
		m.mu.Lock()
		idle := h.refs == 0
		m.mu.Unlock()
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := h.ship(sctx)
		cancel()
		if err != nil {
			m.log.Warn().Err(err).Str("page", h.id.String()).Msg("could not ship buffered edits; retrying")
			continue
		}
		h.maybeCompact(m.compactIdle)
		if !idle {
			continue
		}
		m.mu.Lock()
		if h.refs == 0 && m.hubs[h.id] == h {
			delete(m.hubs, h.id)
			h.closeLog()
		}
		m.mu.Unlock()
	}
}

func (m *Manager) openHubs() []*hub {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*hub, 0, len(m.hubs))
	for _, h := range m.hubs {
		select {
		case <-h.ready:
			if h.loadErr == nil {
				out = append(out, h)
			}
		default:
		}
	}
	return out
}

// Shutdown closes every connection and ships everything buffered, so a
// deploy loses no edits.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	m.closing = true
	m.mu.Unlock()
	hubs := m.openHubs()
	for _, h := range hubs {
		h.closeAll(websocket.StatusServiceRestart, "server restarting")
	}
	// Browsers answer the close right away and then reconnect elsewhere.
	// Closing connections log nothing more, so a client that does not
	// answer only delays shipping by closeWait.
	done := make(chan struct{})
	go func() {
		m.serving.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(closeWait):
	case <-ctx.Done():
	}
	for _, h := range hubs {
		if err := h.ship(ctx); err != nil {
			m.log.Error().Err(err).Str("page", h.id.String()).Msg("could not ship buffered edits on shutdown; they stay on disk")
		}
		h.closeLog()
	}
}

func (m *Manager) acquire(ctx context.Context, id uuid.UUID) (*hub, error) {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return nil, errClosing
	}
	if m.blocked[id] {
		m.mu.Unlock()
		return nil, httperror.Unavailable("the page is being restored")
	}
	h, existed := m.hubs[id]
	if !existed {
		h = &hub{id: id, store: m.store, ready: make(chan struct{}), conns: map[*conn]struct{}{}}
		m.hubs[id] = h
	}
	h.refs++
	m.serving.Add(1)
	m.mu.Unlock()

	if !existed {
		lctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		h.loadErr = h.load(lctx)
		cancel()
		close(h.ready)
	} else {
		select {
		case <-h.ready:
		case <-ctx.Done():
			m.release(h)
			return nil, ctx.Err()
		}
	}
	if h.loadErr != nil {
		m.mu.Lock()
		h.refs--
		if m.hubs[id] == h {
			delete(m.hubs, id)
		}
		m.mu.Unlock()
		m.serving.Done()
		m.log.Error().Err(h.loadErr).Str("page", id.String()).Msg("could not load page content")
		return nil, httperror.Unavailable("page content could not be loaded")
	}
	return h, nil
}

func (m *Manager) release(h *hub) {
	m.mu.Lock()
	h.refs--
	m.mu.Unlock()
	m.serving.Done()
}

// forget closes and deletes the content of pages deleted forever.
func (m *Manager) forget(ids []uuid.UUID) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for _, id := range ids {
			m.mu.Lock()
			h := m.hubs[id]
			delete(m.hubs, id)
			m.mu.Unlock()
			if h != nil {
				h.closeAll(closeNoAccess, "page deleted")
				h.closeLog()
			}
			if err := m.store.forget(ctx, id); err != nil {
				m.log.Warn().Err(err).Str("page", id.String()).Msg("could not delete page content")
			}
		}
	}()
}

// CopyContent duplicates stored documents for a page duplication (old id →
// new id). Open hubs of the sources are shipped first so the copy includes
// everything on disk.
func (m *Manager) CopyContent(ctx context.Context, pages map[uuid.UUID]uuid.UUID) error {
	var errs []error
	for from, to := range pages {
		m.mu.Lock()
		h := m.hubs[from]
		m.mu.Unlock()
		if h != nil {
			if err := h.ship(ctx); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if err := m.store.copyContent(ctx, from, to); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// OpenObject streams one raw object (a shared page's upload). The caller
// has already checked access.
func (m *Manager) OpenObject(ctx context.Context, key string) (io.ReadCloser, error) {
	type opener interface {
		GetObject(ctx context.Context, key string) (io.ReadCloser, error)
	}
	o, ok := m.store.objects.(opener)
	if !ok {
		return nil, httperror.Unavailable("object storage cannot stream")
	}
	return o.GetObject(ctx, key)
}

// LoadDocument returns a page's stored document as the update list a
// reader merges (snapshot first). Used by the public share view; the
// caller has already checked access.
func (m *Manager) LoadDocument(ctx context.Context, page uuid.UUID) ([][]byte, error) {
	l, err := m.store.load(ctx, page)
	if err != nil {
		return nil, err
	}
	out := make([][]byte, 0, len(l.records)+1)
	if l.snapshot != nil {
		out = append(out, l.snapshot)
	}
	for _, r := range l.records {
		out = append(out, r.data)
	}
	return out, nil
}

// OpenRevision returns one revision's document bytes for the history
// preview (view access on the page; an invisible page reads as a missing
// revision).
func (m *Manager) OpenRevision(ctx context.Context, c wiki.Caller, rev uuid.UUID) ([]byte, error) {
	page, key, err := m.store.revisionMeta(ctx, rev)
	if err != nil {
		return nil, err
	}
	if _, err := m.svc.GetPage(ctx, c, page); err != nil {
		return nil, hideAsRevision(err)
	}
	return m.store.objects.Get(ctx, key)
}

// Restore makes a revision the page's current document (edit access). Open
// connections close with closeRestored so every browser reloads the page
// with a fresh document; their copy belongs to the replaced timeline.
func (m *Manager) Restore(ctx context.Context, c wiki.Caller, rev uuid.UUID) (uuid.UUID, error) {
	page, _, err := m.store.revisionMeta(ctx, rev)
	if err != nil {
		return uuid.Nil, err
	}
	p, err := m.svc.GetPage(ctx, c, page)
	if err != nil {
		return uuid.Nil, hideAsRevision(err)
	}
	if p.Access < domain.LevelEdit {
		return uuid.Nil, httperror.New(httperror.CodeForbidden, "needs edit access")
	}

	// Block new connections, then close the page's hub so nothing holds
	// the buffer files and no update races the swap.
	m.mu.Lock()
	if m.blocked[page] {
		m.mu.Unlock()
		return uuid.Nil, httperror.Conflict("another restore is already running")
	}
	m.blocked[page] = true
	h := m.hubs[page]
	delete(m.hubs, page)
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.blocked, page)
		m.mu.Unlock()
	}()
	if h != nil {
		h.closeAll(closeRestored, "page restored")
		h.closeLog()
	}
	return page, m.store.restore(ctx, page, rev)
}

// hideAsRevision turns a page 404 into a revision 404, so the response
// never confirms an invisible page exists.
func hideAsRevision(err error) error {
	var he *httperror.Error
	if errors.As(err, &he) && he.Code == httperror.CodeNotFound {
		return httperror.NotFound("revision")
	}
	return err
}

// Ticket issues a one-time websocket ticket for a page the caller can see.
func (m *Manager) Ticket(ctx context.Context, c wiki.Caller, page uuid.UUID) (string, domain.Level, error) {
	p, err := m.svc.GetPage(ctx, c, page)
	if err != nil {
		return "", domain.LevelNone, err
	}
	return m.tickets.issue(c, page), p.Access, nil
}

// Serve upgrades a ticketed request to the page's live connection and
// blocks until it closes.
func (m *Manager) Serve(w http.ResponseWriter, r *http.Request, page uuid.UUID) {
	caller, ok := m.tickets.redeem(r.URL.Query().Get("ticket"), page)
	if !ok {
		httperror.Render(w, r, httperror.ErrUnauthorized)
		return
	}
	p, err := m.svc.GetPage(r.Context(), caller, page)
	if err != nil {
		httperror.Render(w, r, err)
		return
	}
	h, err := m.acquire(r.Context(), page)
	if err != nil {
		httperror.Render(w, r, err)
		return
	}
	defer m.release(h)
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: m.origins})
	if err != nil {
		return
	}
	c := &conn{
		m: m, h: h, ws: ws,
		out: make(chan outMsg, outQueue), done: make(chan struct{}),
		level: p.Access, clients: map[uint64]uint64{},
		caller: caller, refreshedAt: time.Now(),
	}
	go c.writer()
	h.join(c)
	m.mu.Lock()
	closing := m.closing
	m.mu.Unlock()
	if closing {
		c.close(websocket.StatusServiceRestart, "server restarting")
	}
	go c.watch()
	c.readLoop()
	h.leave(c)
	c.close(websocket.StatusNormalClosure, "")
}