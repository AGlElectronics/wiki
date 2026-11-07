package http

import (
	"net/http"
	"sync"
	"time"

	"github.com/AGlElectronics/wiki/backend/internal/clientip"
	"github.com/AGlElectronics/wiki/backend/internal/httperror"
)

// rateLimiter is a small per-IP token bucket for the public share routes.
// In-memory, like the live tickets: the wiki runs as one replica.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens per second
	burst   float64
	swept   time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(perMinute, burst int) *rateLimiter {
	return &rateLimiter{
		buckets: map[string]*bucket{},
		rate:    float64(perMinute) / 60,
		burst:   float64(burst),
		swept:   time.Now(),
	}
}

func (l *rateLimiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.swept) > 10*time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.last) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.swept = now
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// middleware limits by client IP as resolved by clientip.Middleware, which
// only believes X-Forwarded-For from trusted proxies.
func (l *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(clientip.FromRequest(r)) {
			w.Header().Set("Retry-After", "60")
			httperror.Render(w, r, httperror.New(httperror.CodeTooManyRequests, "too many requests; try again shortly"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
