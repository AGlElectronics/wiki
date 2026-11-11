package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AGlElectronics/wiki/backend/internal/config"
)

// fakeIssuer is an OpenID provider with discovery and a JWKS that signs
// test tokens with its own RSA key.
type fakeIssuer struct {
	srv *httptest.Server
	key *rsa.PrivateKey
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIssuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.srv.URL,
			"jwks_uri":                              f.srv.URL + "/jwks",
			"authorization_endpoint":                f.srv.URL + "/auth",
			"token_endpoint":                        f.srv.URL + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// token signs claims (iss defaults to the issuer, exp to an hour ahead).
func (f *fakeIssuer) token(t *testing.T, claims map[string]any) string {
	t.Helper()
	if _, ok := claims["iss"]; !ok {
		claims["iss"] = f.srv.URL
	}
	if _, ok := claims["exp"]; !ok {
		claims["exp"] = time.Now().Add(time.Hour).Unix()
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "test"})
	payload, _ := json.Marshal(claims)
	signing := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + b64(sig)
}

func TestOIDCVerify(t *testing.T) {
	f := newFakeIssuer(t)
	ctx := context.Background()
	v, err := NewOIDCVerifier(ctx, &config.Config{OIDCIssuerURL: f.srv.URL, OIDCClientID: "wiki-spa"})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}

	claims, err := v.Verify(ctx, f.token(t, map[string]any{
		"sub": "kc-123", "aud": "wiki-spa", "email": "sam@example.org", "nickname": "Sam",
		"groups": []string{"/dept-security", "wiki-editor"}, "roles": []string{"wiki-editor", "offline_access"},
	}))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Subject != "kc-123" || claims.Issuer != f.srv.URL || claims.Email != "sam@example.org" {
		t.Fatalf("claims = %+v", claims)
	}
	// Current behaviour (until the suite-wide OIDC_GROUPS_CLAIM): roles are
	// merged into groups, without duplicates.
	if strings.Join(claims.Groups, ",") != "/dept-security,wiki-editor,offline_access" {
		t.Fatalf("groups = %v", claims.Groups)
	}
	if got := displayNameFromClaims(claims); got != "Sam" {
		t.Fatalf("display name = %q", got)
	}

	bad := map[string]map[string]any{
		"wrong audience":  {"sub": "kc-1", "aud": "other-spa"},
		"expired":         {"sub": "kc-1", "aud": "wiki-spa", "exp": time.Now().Add(-time.Hour).Unix()},
		"another issuer":  {"sub": "kc-1", "aud": "wiki-spa", "iss": "https://evil.example.org"},
		"missing subject": {"aud": "wiki-spa"},
	}
	for name, c := range bad {
		if _, err := v.Verify(ctx, f.token(t, c)); err == nil {
			t.Errorf("%s: token accepted", name)
		}
	}
	tampered := f.token(t, map[string]any{"sub": "kc-1", "aud": "wiki-spa"})
	parts := strings.Split(tampered, ".")
	parts[1] = b64([]byte(`{"sub":"admin","aud":"wiki-spa","iss":"` + f.srv.URL + `","exp":9999999999}`))
	if _, err := v.Verify(ctx, strings.Join(parts, ".")); err == nil {
		t.Error("a token with a changed payload was accepted")
	}
	if _, err := v.Verify(ctx, "  "); err == nil {
		t.Error("an empty bearer was accepted")
	}

	// OIDC_AUDIENCE overrides the client id as the expected audience.
	va, err := NewOIDCVerifier(ctx, &config.Config{OIDCIssuerURL: f.srv.URL, OIDCClientID: "wiki-spa", OIDCAudience: "wiki-api"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := va.Verify(ctx, f.token(t, map[string]any{"sub": "kc-1", "aud": "wiki-api"})); err != nil {
		t.Errorf("audience wiki-api: %v", err)
	}
	if _, err := NewOIDCVerifier(ctx, &config.Config{}); err == nil {
		t.Error("a verifier without issuer and client id must fail")
	}
}

func TestDisplayNameFallbacks(t *testing.T) {
	cases := []struct {
		c    OIDCClaims
		want string
	}{
		{OIDCClaims{Subject: "s", Name: "Full Name", PreferredUsername: "user"}, "Full Name"},
		{OIDCClaims{Subject: "s", PreferredUsername: "user", Email: "a@b"}, "user"},
		{OIDCClaims{Subject: "s", Email: "a@b"}, "a@b"},
		{OIDCClaims{Subject: "s", Name: "  "}, "s"},
	}
	for _, tc := range cases {
		if got := displayNameFromClaims(&tc.c); got != tc.want {
			t.Errorf("displayNameFromClaims(%+v) = %q, want %q", tc.c, got, tc.want)
		}
	}
	if displayFromDevHeader("dev-admin") != "Dev Admin" || displayFromDevHeader("") != "Dev User" {
		t.Error("dev display names")
	}
}

func TestBearerAndGroupsHeaders(t *testing.T) {
	cases := map[string]string{"Bearer abc": "abc", "bearer  abc ": "abc", "Basic abc": "", "Bearer": "", "": ""}
	for in, want := range cases {
		if got := bearerFromHeader(in); got != want {
			t.Errorf("bearerFromHeader(%q) = %q, want %q", in, got, want)
		}
	}
	if got := splitGroups(" a, ,b ,"); strings.Join(got, "|") != "a|b" {
		t.Errorf("splitGroups = %v", got)
	}
}
