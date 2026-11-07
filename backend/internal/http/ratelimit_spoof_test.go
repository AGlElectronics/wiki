package http

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AGlElectronics/wiki/backend/internal/clientip"
)

// A direct client rotating X-Forwarded-For values must still share one bucket.
func TestRateLimitIgnoresForgedForwardedFor(t *testing.T) {
	trusted, err := clientip.ParseCIDRs(clientip.DefaultTrustedProxies)
	if err != nil {
		t.Fatal(err)
	}
	h := clientip.Middleware(trusted)(newRateLimiter(60, 3).middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))

	var limited bool
	for i := range 10 {
		req := httptest.NewRequest(http.MethodGet, "/api/public/share/x", nil)
		req.RemoteAddr = "203.0.113.7:5555"
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i))
		req.Header.Set("X-Real-IP", fmt.Sprintf("198.51.100.%d", i))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("forged X-Forwarded-For values bypassed the rate limit")
	}
}
