// Package config loads and validates the backend runtime configuration from
// environment variables (12-factor). Validation runs once at startup and
// fails loudly: a misconfigured deployment is better than a silently
// degraded one.
//
// Object storage (S3_*) holds page content and uploads. An empty
// S3_ENDPOINT turns it off: the page tree, sharing, search and comments
// keep working, but live editing, uploads, history and duplication answer
// 503 until a bucket is configured.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/AGlElectronics/wiki/backend/internal/authbase"
	"github.com/AGlElectronics/wiki/backend/internal/clientip"
	"github.com/AGlElectronics/wiki/backend/internal/denlink"
	"github.com/AGlElectronics/wiki/backend/internal/groupmatch"
)

// AuthMode selects how incoming requests are authenticated (authbase).
//
//   - AuthModeDev reads an X-Dev-User header (identity) and an X-Dev-Groups
//     header (simulated IdP groups) and provisions a local user tied to the
//     "dev" issuer. Only allowed with APP_ENV=development.
//   - AuthModeOIDC verifies a Bearer access token via the configured issuer.
type AuthMode = authbase.AuthMode

const (
	AuthModeOIDC = authbase.AuthOIDC
	AuthModeDev  = authbase.AuthDev
)

// DefaultAudience is OIDC_AUDIENCE when unset: the access token audience
// Keycloak adds for Wiki's API (token contract).
const DefaultAudience = "wiki-api"

// Config is the validated process configuration.
type Config struct {
	// Core. AppEnv defaults to production; only "development" relaxes rules.
	AppEnv     string
	AppBaseURL string
	HTTPAddr   string

	// Database
	DatabaseURL string

	// Auth
	AuthMode         AuthMode
	OIDCIssuerURL    string
	OIDCClientID     string
	OIDCAudience     string
	AccessConfigPath string
	DevDefaultUser   string
	DevDefaultGroups []string
	// GroupMatch is OIDC_GROUPS_CLAIM, OIDC_GROUP_MATCH, OIDC_GROUP_PREFIX.
	GroupMatch groupmatch.Config
	// HRServiceToken lets HR read GET /api/role-catalog without a sign-in
	// (HR_SERVICE_TOKEN). Empty: only signed-in staff may read it.
	HRServiceToken string

	// Brand provider
	BrandLocalPath string // optional brand.json; Den's brand overlays it once paired
	// Den is the pairing configuration (DEN_MODE, DEN_INTERNAL_URL,
	// DEN_ALLOWED_HOSTS, DEN_LINK_ENCRYPTION_KEY[_PREVIOUS], APP_ENV).
	Den denlink.Config

	// HTTP
	CORSAllowedOrigins []string
	// TrustedProxyCIDRs lists the proxies whose X-Forwarded-For entries are
	// believed when working out the client IP (TRUSTED_PROXY_CIDRS).
	TrustedProxyCIDRs []netip.Prefix
	ShutdownTimeout   time.Duration

	// Object storage (S3-compatible; SeaweedFS in dev). Empty S3Endpoint
	// disables it.
	S3Endpoint        string
	S3Bucket          string
	S3AccessKeyID     string
	S3SecretAccessKey string
	S3Region          string
	S3UsePathStyle    bool

	// BufferDir holds the last few seconds of live edits before they are
	// shipped to S3. One backend replica owns it (docs/plan.md).
	BufferDir string

	// PublicLinksEnabled switches public share links on when the module is
	// standalone. Once paired, Den's wiki_public_links setting wins.
	PublicLinksEnabled bool

	// ServiceToken guards /api/internal/* (HR's offboarding hook). Empty
	// turns the internal API off (it answers 404), like Learn.
	ServiceToken string
	// OffboardGraceDays is how long an offboarded person's private space
	// stays (visible to wiki admins) before the sweep deletes it.
	OffboardGraceDays int

	// RevisionRetentionDays deletes page revisions older than this many
	// days. 0 keeps every revision. Once paired, Den's integration bundle
	// overrides it (retention.revision_days).
	RevisionRetentionDays int

	// Group picker directories (package directory). HR is used while the
	// module is paired with Den; the identity provider otherwise. Empty
	// URLs turn a source off.
	HRDirectoryURL           string
	HRDirectoryToken         string
	IdPDirectoryURL          string
	IdPDirectoryRealm        string
	IdPDirectoryClientID     string
	IdPDirectoryClientSecret string
}

// S3Enabled reports whether object storage is configured.
func (c *Config) S3Enabled() bool { return strings.TrimSpace(c.S3Endpoint) != "" }

// Auth is the authbase view of AUTH_MODE and APP_ENV.
func (c *Config) Auth() authbase.Config {
	return authbase.Config{AppEnv: c.AppEnv, AuthMode: c.AuthMode}
}

// Audience is the audience access tokens must carry.
func (c *Config) Audience() string {
	if c.OIDCAudience != "" {
		return c.OIDCAudience
	}
	return DefaultAudience
}

// DenConfig is Den with the slug and version filled in, and APP_ENV taken
// from the process when Den does not set its own.
func (c *Config) DenConfig(version string) denlink.Config {
	d := c.Den
	d.Slug, d.Version = "wiki", version
	if d.AppEnv == "" {
		d.AppEnv = c.AppEnv
	}
	return d
}

// Load reads environment variables (plus an optional .env file) and returns
// a validated Config. Required fields that are missing return an aggregated
// error so operators see every problem at once instead of fixing one at a
// time.
func Load() (*Config, error) {
	_ = godotenv.Load()

	var errs []string
	auth, err := authbase.FromEnv(os.Getenv)
	if err != nil {
		errs = append(errs, err.Error())
	}
	groups, err := groupmatch.FromEnv(os.Getenv)
	if err != nil {
		errs = append(errs, err.Error())
	}
	den, err := denlink.FromEnv("wiki", "", os.Getenv)
	if err != nil {
		errs = append(errs, err.Error())
	}

	cfg := &Config{
		AppEnv: auth.AppEnv,
		// Container-internal defaults: every module in the ecosystem binds
		// :8080 inside its container. Host-side port mapping (host 8140 ->
		// container 8080 for Wiki) is handled by docker-compose / K8s.
		AppBaseURL: getEnv("APP_BASE_URL", "http://localhost:8080"),
		HTTPAddr:   getEnv("HTTP_ADDR", ":8080"),

		DatabaseURL: getEnv("DATABASE_URL", ""),

		AuthMode:         auth.AuthMode,
		OIDCIssuerURL:    getEnv("OIDC_ISSUER_URL", ""),
		OIDCClientID:     getEnv("OIDC_CLIENT_ID", ""),
		OIDCAudience:     getEnv("OIDC_AUDIENCE", DefaultAudience),
		AccessConfigPath: getEnv("ACCESS_CONFIG_PATH", ""),
		DevDefaultUser:   getEnv("DEV_DEFAULT_USER", "dev-admin"),
		DevDefaultGroups: splitCSV(getEnv("DEV_DEFAULT_GROUPS", "wiki-admin")),
		GroupMatch:       groups,
		HRServiceToken:   getEnv("HR_SERVICE_TOKEN", ""),

		BrandLocalPath: getEnv("BRAND_LOCAL_PATH", ""),
		Den:            den,

		CORSAllowedOrigins: splitCSV(getEnv("CORS_ALLOWED_ORIGINS", "http://localhost:3007")),
		ShutdownTimeout:    getEnvDuration("SHUTDOWN_TIMEOUT", 15*time.Second),

		S3Endpoint:        getEnv("S3_ENDPOINT", ""),
		S3Bucket:          getEnv("S3_BUCKET", "wiki-data-dev"),
		S3AccessKeyID:     getEnv("S3_ACCESS_KEY_ID", ""),
		S3SecretAccessKey: getEnv("S3_SECRET_ACCESS_KEY", ""),
		S3Region:          getEnv("S3_REGION", "us-east-1"),
		S3UsePathStyle:    getEnvBool("S3_USE_PATH_STYLE", true),

		BufferDir: getEnv("WIKI_BUFFER_DIR", "./data/buffer"),

		PublicLinksEnabled: getEnvBool("PUBLIC_LINKS_ENABLED", false),

		ServiceToken:      getEnv("WIKI_SERVICE_TOKEN", ""),
		OffboardGraceDays: getEnvInt("WIKI_OFFBOARD_GRACE_DAYS", 30),

		RevisionRetentionDays: getEnvInt("WIKI_REVISION_RETENTION_DAYS", 0),

		HRDirectoryURL:           getEnv("HR_DIRECTORY_URL", ""),
		HRDirectoryToken:         getEnv("HR_DIRECTORY_TOKEN", ""),
		IdPDirectoryURL:          getEnv("IDP_DIRECTORY_URL", ""),
		IdPDirectoryRealm:        getEnv("IDP_DIRECTORY_REALM", "conservices"),
		IdPDirectoryClientID:     getEnv("IDP_DIRECTORY_CLIENT_ID", ""),
		IdPDirectoryClientSecret: getEnv("IDP_DIRECTORY_CLIENT_SECRET", ""),
	}

	trusted, err := clientip.ParseCIDRs(getEnv("TRUSTED_PROXY_CIDRS", clientip.DefaultTrustedProxies))
	if err != nil {
		return nil, fmt.Errorf("invalid config: TRUSTED_PROXY_CIDRS: %w", err)
	}
	cfg.TrustedProxyCIDRs = trusted

	if err := cfg.validate(errs); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate(errs []string) error {
	if c.DatabaseURL == "" {
		errs = append(errs, "DATABASE_URL is required")
	}
	if c.RevisionRetentionDays < 0 {
		errs = append(errs, "WIKI_REVISION_RETENTION_DAYS must be 0 (keep every revision) or a number of days")
	}
	if c.HRDirectoryURL != "" && c.HRDirectoryToken == "" {
		errs = append(errs, "HR_DIRECTORY_TOKEN is required when HR_DIRECTORY_URL is set")
	}
	if c.IdPDirectoryURL != "" && (c.IdPDirectoryClientID == "" || c.IdPDirectoryClientSecret == "") {
		errs = append(errs, "IDP_DIRECTORY_CLIENT_ID and IDP_DIRECTORY_CLIENT_SECRET are required when IDP_DIRECTORY_URL is set")
	}

	switch c.AuthMode {
	case AuthModeOIDC:
		if c.OIDCIssuerURL == "" {
			errs = append(errs, "OIDC_ISSUER_URL is required when AUTH_MODE=oidc")
		}
		if c.OIDCClientID == "" {
			errs = append(errs, "OIDC_CLIENT_ID is required when AUTH_MODE=oidc")
		}
	case AuthModeDev:
		// dev mode: no extra required fields; dev users auto-provision.
		// authbase.FromEnv already refused it outside development.
	}

	if len(errs) > 0 {
		return errors.New("invalid config: " + strings.Join(errs, "; "))
	}
	return nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return parsed
}

func getEnvInt(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return parsed
}

func getEnvBool(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return parsed
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
