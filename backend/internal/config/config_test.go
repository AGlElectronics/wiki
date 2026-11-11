package config

import (
	"strings"
	"testing"
)

func TestLoadValidatesDirectoriesAndRetention(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://x", "AUTH_MODE": "dev", "APP_ENV": "development"}
	cases := []struct {
		name string
		env  map[string]string
		want string // "" = valid
	}{
		{"defaults", nil, ""},
		{"hr without token", map[string]string{"HR_DIRECTORY_URL": "http://hr"}, "HR_DIRECTORY_TOKEN is required"},
		{"hr with token", map[string]string{"HR_DIRECTORY_URL": "http://hr", "HR_DIRECTORY_TOKEN": "t"}, ""},
		{"idp without secret", map[string]string{"IDP_DIRECTORY_URL": "http://kc", "IDP_DIRECTORY_CLIENT_ID": "wiki-directory"}, "IDP_DIRECTORY_CLIENT_SECRET are required"},
		{"idp complete", map[string]string{"IDP_DIRECTORY_URL": "http://kc", "IDP_DIRECTORY_CLIENT_ID": "c", "IDP_DIRECTORY_CLIENT_SECRET": "s"}, ""},
		{"negative retention", map[string]string{"WIKI_REVISION_RETENTION_DAYS": "-5"}, "WIKI_REVISION_RETENTION_DAYS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range base {
				t.Setenv(k, v)
			}
			for _, k := range []string{"HR_DIRECTORY_URL", "HR_DIRECTORY_TOKEN", "IDP_DIRECTORY_URL", "IDP_DIRECTORY_CLIENT_ID", "IDP_DIRECTORY_CLIENT_SECRET", "WIKI_REVISION_RETENTION_DAYS"} {
				t.Setenv(k, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			cfg, err := Load()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("want valid, got %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("want an error containing %q, got %v", tc.want, err)
			}
			if tc.name == "defaults" && (cfg.RevisionRetentionDays != 0 || cfg.IdPDirectoryRealm != "conservices") {
				t.Fatalf("defaults: retention %d, realm %q", cfg.RevisionRetentionDays, cfg.IdPDirectoryRealm)
			}
		})
	}
}

// TestSafeDefaults: without AUTH_MODE and APP_ENV the backend runs OIDC in
// production; dev sign-in and a missing Den key are refused outside
// development (authbase, denlink).
func TestSafeDefaults(t *testing.T) {
	clear := func(t *testing.T) {
		for _, k := range []string{"AUTH_MODE", "APP_ENV", "OIDC_ISSUER_URL", "OIDC_CLIENT_ID", "OIDC_AUDIENCE",
			"DEN_LINK_ENCRYPTION_KEY", "DEN_MODE", "OIDC_GROUPS_CLAIM", "OIDC_GROUP_MATCH", "OIDC_GROUP_PREFIX", "HR_SERVICE_TOKEN"} {
			t.Setenv(k, "")
		}
		t.Setenv("DATABASE_URL", "postgres://x")
	}
	key := "ZGV2LW9ubHktd2lraS1kZW4tbGluay1rZXktMDAwMDA=" // 32 bytes, dev only

	t.Run("production oidc", func(t *testing.T) {
		clear(t)
		t.Setenv("OIDC_ISSUER_URL", "https://auth.example.org/realms/c")
		t.Setenv("OIDC_CLIENT_ID", "wiki-spa")
		t.Setenv("DEN_LINK_ENCRYPTION_KEY", key)
		t.Setenv("OIDC_GROUP_MATCH", "last")
		t.Setenv("OIDC_GROUP_PREFIX", "/con2026")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.AppEnv != "production" || cfg.AuthMode != AuthModeOIDC || cfg.Audience() != "wiki-api" {
			t.Fatalf("defaults: env %q, mode %q, audience %q", cfg.AppEnv, cfg.AuthMode, cfg.Audience())
		}
		if cfg.GroupMatch.ClaimName() != "groups" || cfg.GroupMatch.Mode != "last" || cfg.GroupMatch.Prefix != "/con2026" {
			t.Fatalf("group match = %+v", cfg.GroupMatch)
		}
		if d := cfg.DenConfig("1.0.0"); d.Slug != "wiki" || d.Version != "1.0.0" || d.EncryptionKey != key {
			t.Fatalf("den config = %+v", d)
		}
	})
	t.Run("dev refused in production", func(t *testing.T) {
		clear(t)
		t.Setenv("AUTH_MODE", "dev")
		t.Setenv("DEN_LINK_ENCRYPTION_KEY", key)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AUTH_MODE=dev") {
			t.Fatalf("want the AUTH_MODE=dev refusal, got %v", err)
		}
	})
	t.Run("den key required in production", func(t *testing.T) {
		clear(t)
		t.Setenv("OIDC_ISSUER_URL", "https://auth.example.org/realms/c")
		t.Setenv("OIDC_CLIENT_ID", "wiki-spa")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DEN_LINK_ENCRYPTION_KEY") {
			t.Fatalf("want the DEN_LINK_ENCRYPTION_KEY refusal, got %v", err)
		}
	})
	t.Run("trusted proxies", func(t *testing.T) {
		for _, tc := range []struct {
			value, want string
			n           int
		}{
			{"", "", 6}, // the private and loopback ranges
			{"none", "", 0},
			{"203.0.113.0/24, 2001:db8::/32", "", 2},
			{"203.0.113.9", "", 1},
			{"10.0.0.0/8,example.com", "TRUSTED_PROXY_CIDRS", 0},
		} {
			clear(t)
			t.Setenv("APP_ENV", "development")
			t.Setenv("AUTH_MODE", "dev")
			t.Setenv("TRUSTED_PROXY_CIDRS", tc.value)
			cfg, err := Load()
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("%q: want an error containing %q, got %v", tc.value, tc.want, err)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%q: %v", tc.value, err)
			}
			if len(cfg.TrustedProxies) != tc.n || len(cfg.Auth().TrustedProxies) != tc.n {
				t.Fatalf("%q: trusted proxies %v, want %d", tc.value, cfg.TrustedProxies, tc.n)
			}
		}
	})
	t.Run("bad group match", func(t *testing.T) {
		clear(t)
		t.Setenv("APP_ENV", "development")
		t.Setenv("AUTH_MODE", "dev")
		t.Setenv("OIDC_GROUP_MATCH", "fuzzy")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OIDC_GROUP_MATCH") {
			t.Fatalf("want the OIDC_GROUP_MATCH error, got %v", err)
		}
	})
}
