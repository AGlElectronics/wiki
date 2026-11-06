package live

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/AGlElectronics/wiki/backend/internal/wiki"
)

// ticketTTL is how long a ticket can wait before the websocket opens.
const ticketTTL = 30 * time.Second

// Browsers cannot send an Authorization header on a websocket, so the page
// first gets a one-time ticket over the normal API and puts it in the URL.
type tickets struct {
	mu sync.Mutex
	m  map[string]ticket
}

type ticket struct {
	caller  wiki.Caller
	page    uuid.UUID
	expires time.Time
}

func newTickets() *tickets { return &tickets{m: map[string]ticket{}} }

func (t *tickets) issue(c wiki.Caller, page uuid.UUID) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	t.mu.Lock()
	t.m[tok] = ticket{caller: c, page: page, expires: time.Now().Add(ticketTTL)}
	t.mu.Unlock()
	return tok
}

// redeem uses up a ticket for page.
func (t *tickets) redeem(tok string, page uuid.UUID) (wiki.Caller, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tk, ok := t.m[tok]
	delete(t.m, tok)
	if !ok || tk.page != page || time.Now().After(tk.expires) {
		return wiki.Caller{}, false
	}
	return tk.caller, true
}

func (t *tickets) sweep() {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, tk := range t.m {
		if now.After(tk.expires) {
			delete(t.m, k)
		}
	}
}
