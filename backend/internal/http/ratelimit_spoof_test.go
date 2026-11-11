package http

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AGlElectronics/wiki/backend/internal/authbase"
)

func defaultProxies(t *testing.T) authbase.TrustedProxies {
	t.Helper()
	trusted, err := authbase.ParseTrustedProxies(authbase.DefaultTrustedProxyCIDRs)
	if err != nil {
		t.Fatal(err)
	}
	return trusted
}

// A direct client rotating X-Forwarded-For values must still share one bucket.
func TestRateLimitIgnoresForgedForwardedFor(t *testing.T) {
	limiter := authbase.NewRateLimiter(authbase.RateLimitOptions{Rate: 1, Burst: 3})
	h := defaultProxies(t).Middleware(limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))

	var limited bool
	for i := range 10 {
		req := httptest.NewRequest(http.MethodGet, "/api/public/share/x", nil)
		req.RemoteAddr = "203.0.113.7:5555"
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i))
		req.Header.Set("X-Real-IP", fmt.Sprintf("198.51.100.%d", i))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			if body := rec.Body.String(); !strings.Contains(body, `"rate_limited"`) {
				t.Fatalf("429 body = %s, want code rate_limited", body)
			}
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("forged X-Forwarded-For values bypassed the rate limit")
	}
}

// TestTrustedProxyClientAddress pins the TRUSTED_PROXY_CIDRS rules Wiki
// had before it used authbase: headers only from a trusted peer,
// X-Forwarded-For read right to left, X-Real-IP never.
func TestTrustedProxyClientAddress(t *testing.T) {
	trusted := defaultProxies(t)
	cases := []struct {
		name    string
		remote  string
		headers map[string][]string
		want    string
	}{
		{"untrusted peer forges XFF", "203.0.113.7:5555", map[string][]string{"X-Forwarded-For": {"1.2.3.4"}}, "203.0.113.7"},
		{"untrusted peer forges private XFF", "203.0.113.7:5555", map[string][]string{"X-Forwarded-For": {"10.0.0.1"}}, "203.0.113.7"},
		{"untrusted IPv6 peer forges XFF", "[2001:db8::1]:443", map[string][]string{"X-Forwarded-For": {"1.2.3.4"}}, "2001:db8::1"},
		{"trusted peer, no header", "10.0.0.5:1234", nil, "10.0.0.5"},
		{"single proxy", "10.0.0.5:1234", map[string][]string{"X-Forwarded-For": {"198.51.100.9"}}, "198.51.100.9"},
		{"Gateway then nginx", "10.0.1.20:40000", map[string][]string{"X-Forwarded-For": {"198.51.100.9, 10.0.2.3"}}, "198.51.100.9"},
		{"client prepends fake entries", "127.0.0.1:1", map[string][]string{"X-Forwarded-For": {"8.8.8.8,9.9.9.9 , 198.51.100.9,172.18.0.4"}}, "198.51.100.9"},
		{"client fakes private hop", "10.0.1.20:40000", map[string][]string{"X-Forwarded-For": {"10.9.9.9, 198.51.100.9, 10.0.2.3"}}, "198.51.100.9"},
		{"multiple XFF headers merged", "10.0.1.20:40000", map[string][]string{"X-Forwarded-For": {"1.2.3.4", "198.51.100.9", "10.0.2.3"}}, "198.51.100.9"},
		{"all hops trusted uses leftmost", "10.0.0.5:1234", map[string][]string{"X-Forwarded-For": {"192.168.1.10, 10.0.2.3"}}, "192.168.1.10"},
		{"IPv6 proxy chain", "[fd00::5]:8080", map[string][]string{"X-Forwarded-For": {"2001:db8::42, fd00::1"}}, "2001:db8::42"},
		{"IPv4-mapped peer is trusted", "[::ffff:10.0.0.5]:8080", map[string][]string{"X-Forwarded-For": {"198.51.100.9"}}, "198.51.100.9"},
		{"mapped trusted alias skipped", "10.0.0.5:1", map[string][]string{"X-Forwarded-For": {"198.51.100.9, ::ffff:10.0.0.6"}}, "198.51.100.9"},
		{"entry with port", "10.0.0.5:1", map[string][]string{"X-Forwarded-For": {"198.51.100.9:4711"}}, "198.51.100.9"},
		{"bracketed IPv6 entry with port", "10.0.0.5:1", map[string][]string{"X-Forwarded-For": {"[2001:db8::42]:443"}}, "2001:db8::42"},
		{"garbage only", "10.0.0.5:1", map[string][]string{"X-Forwarded-For": {"not-an-ip"}}, "10.0.0.5"},
		{"garbage left of real client", "10.0.0.5:1", map[string][]string{"X-Forwarded-For": {"garbage, 198.51.100.9"}}, "198.51.100.9"},
		{"garbage behind trusted hop", "10.0.0.5:1", map[string][]string{"X-Forwarded-For": {"198.51.100.9, garbage, 10.0.2.3"}}, "10.0.2.3"},
		{"CIDR in header", "10.0.0.5:1", map[string][]string{"X-Forwarded-For": {"198.51.100.0/24"}}, "10.0.0.5"},
		{"proxy with X-Real-IP only is not believed", "10.0.0.5:1234", map[string][]string{"X-Real-IP": {"1.2.3.4"}, "True-Client-IP": {"1.2.3.4"}}, "10.0.0.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			h := trusted.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, _, _ = net.SplitHostPort(r.RemoteAddr)
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remote
			for k, vs := range tc.headers {
				for _, v := range vs {
					req.Header.Add(k, v)
				}
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
			if got != tc.want {
				t.Fatalf("client = %q, want %q", got, tc.want)
			}
		})
	}

	none, err := authbase.ParseTrustedProxies("none")
	if err != nil || len(none) != 0 {
		t.Fatalf("none = %v, %v", none, err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:1"
	req.Header.Set("X-Forwarded-For", "198.51.100.9")
	if a, _ := none.ClientAddr(req); a.String() != "10.0.0.5" {
		t.Fatalf("TRUSTED_PROXY_CIDRS=none: client = %s, want the peer", a)
	}
}
