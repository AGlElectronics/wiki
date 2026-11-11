package config

import (
	"strings"
	"testing"
)

func TestLoadValidatesDirectoriesAndRetention(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://x", "AUTH_MODE": "dev"}
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
