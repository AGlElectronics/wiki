// Package app wires configuration, the database pool, auth and HTTP into a
// runnable server. cmd/server/main.go just calls Run.
//
// This package does NOT run Goose migrations on start. Migrations are a
// one-shot controlled operation handled by cmd/migrate (Compose service /
// K8s Job), same as every other Con Services module.
package app

import (
	"context"
	"errors"
	"fmt"
	nethttp "net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/AGlElectronics/wiki/backend/internal/auth"
	"github.com/AGlElectronics/wiki/backend/internal/brand"
	"github.com/AGlElectronics/wiki/backend/internal/config"
	"github.com/AGlElectronics/wiki/backend/internal/db"
	"github.com/AGlElectronics/wiki/backend/internal/denlink"
	"github.com/AGlElectronics/wiki/backend/internal/files"
	httpx "github.com/AGlElectronics/wiki/backend/internal/http"
	"github.com/AGlElectronics/wiki/backend/internal/live"
	"github.com/AGlElectronics/wiki/backend/internal/tlscert"
	"github.com/AGlElectronics/wiki/backend/internal/wiki"
)

// Version is set via -ldflags at build time; left as "dev" for local runs.
var Version = "dev"

// Run boots the server and blocks until a shutdown signal is received or
// ListenAndServe fails.
func Run(ctx context.Context, logger zerolog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger.Info().
		Str("env", cfg.AppEnv).
		Str("addr", cfg.HTTPAddr).
		Str("auth_mode", string(cfg.AuthMode)).
		Str("version", Version).
		Msg("starting wiki backend")

	// DB pool. Migrations are NOT applied here; cmd/migrate ran first.
	dbCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pool, err := db.Open(dbCtx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer pool.Close()
	logger.Info().Msg("database pool ready")

	// OIDC verifier. Only required when AUTH_MODE=oidc; dev mode skips.
	var oidcVerifier *auth.OIDCVerifier
	if cfg.AuthMode == config.AuthModeOIDC {
		oidcCtx, cancelOIDC := context.WithTimeout(ctx, 30*time.Second)
		oidcVerifier, err = auth.NewOIDCVerifier(oidcCtx, cfg)
		cancelOIDC()
		if err != nil {
			return fmt.Errorf("init oidc verifier: %w", err)
		}
		logger.Info().Str("issuer", cfg.OIDCIssuerURL).Msg("oidc verifier ready")
	}

	// Access config catalog. A bogus ACCESS_CONFIG_PATH fails loudly; no
	// file at all falls back to the built-in role catalog (dev only).
	var access *auth.AccessConfig
	if cfg.AccessConfigPath != "" {
		access, err = auth.LoadAccessConfig(cfg.AccessConfigPath)
		if err != nil {
			return fmt.Errorf("load access config: %w", err)
		}
		logger.Info().
			Str("path", cfg.AccessConfigPath).
			Strs("roles", access.Roles()).
			Msg("access config loaded")
	} else {
		access = auth.DefaultAccessConfig()
		logger.Info().Msg("no access config file configured; using built-in role catalog")
	}

	brandProvider := brand.New(cfg)
	den, err := denlink.Open(pool, cfg.DenConfig(Version), logger)
	if err != nil {
		return fmt.Errorf("den link: %w", err)
	}
	if err := den.Restore(ctx); err != nil {
		logger.Warn().Err(err).Msg("den link: load failed")
	}
	go den.Run(ctx)

	// Object storage holds page content. A wrong bucket or credential fails
	// the start, like a wrong database URL; no S3_ENDPOINT turns live
	// editing off (page tree and sharing still work).
	svc := wiki.NewService(pool)
	svc.SetRevisionRetentionDays(cfg.RevisionRetentionDays)
	svc.SetGroupMatch(cfg.GroupMatch)
	var liveMgr *live.Manager
	var filesSvc *wiki.Files
	filesCtx, cancelFiles := context.WithTimeout(ctx, 15*time.Second)
	fileClient, err := files.New(filesCtx, cfg)
	cancelFiles()
	switch {
	case err != nil:
		return fmt.Errorf("object storage: %w", err)
	case fileClient != nil:
		logger.Info().Str("bucket", fileClient.Bucket()).Msg("object storage ready")
		store, err := live.NewStore(pool, fileClient, cfg.BufferDir, logger)
		if err != nil {
			return err
		}
		liveMgr = live.NewManager(store, svc, cfg.CORSAllowedOrigins, logger)
		filesSvc = wiki.NewFiles(svc, fileClient)
		recoverCtx, cancelRecover := context.WithTimeout(ctx, time.Minute)
		err = liveMgr.Recover(recoverCtx)
		cancelRecover()
		if err != nil {
			// The files stay on disk and pages still load them.
			logger.Error().Err(err).Msg("could not ship every buffered edit from the previous run")
		}
		go liveMgr.Run(ctx)
	default:
		logger.Warn().Msg("S3_ENDPOINT not set; live editing and uploads are off")
	}

	// Hourly maintenance (and once at start-up). Uses svc so the live
	// manager's delete hook also removes the stored content.
	go func() {
		maintain(ctx, svc, liveMgr, cfg, logger)
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				maintain(ctx, svc, liveMgr, cfg, logger)
			}
		}
	}()

	router := httpx.NewRouter(httpx.Deps{
		Config:       cfg,
		Pool:         pool,
		Logger:       logger,
		Brand:        brandProvider,
		Den:          den,
		OIDCVerifier: oidcVerifier,
		Access:       access,
		Wiki:         svc,
		Live:         liveMgr,
		Files:        filesSvc,
	}, Version)

	server := &nethttp.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if _, err := tlscert.Configure(ctx, server, logger); err != nil {
		return fmt.Errorf("tls: %w", err)
	}

	return runServer(ctx, server, pool, liveMgr, cfg.ShutdownTimeout, logger)
}

// maintain runs the hourly jobs: the offboarding sweep (private spaces of
// offboarded people, after WIKI_OFFBOARD_GRACE_DAYS), retrying stored
// content that could not be deleted yet, and deleting revisions past the
// retention (WIKI_REVISION_RETENTION_DAYS or Den's module settings).
func maintain(ctx context.Context, svc *wiki.Service, liveMgr *live.Manager, cfg *config.Config, logger zerolog.Logger) {
	sctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	grace := time.Duration(cfg.OffboardGraceDays) * 24 * time.Hour
	if n, err := svc.SweepOrphaned(sctx, grace); err != nil {
		logger.Warn().Err(err).Msg("offboarding sweep failed; retrying next hour")
	} else if n > 0 {
		logger.Info().Int("spaces", n).Msg("offboarding sweep removed orphaned private spaces")
	}
	if n, err := svc.RetryContentDeletions(sctx, 10*time.Minute); err != nil {
		logger.Warn().Err(err).Msg("could not list page content waiting for deletion")
	} else if n > 0 {
		logger.Info().Int("pages", n).Msg("retrying deletion of stored page content")
	}
	if liveMgr == nil {
		return
	}
	retention, err := svc.RevisionRetention(sctx)
	if err != nil {
		logger.Warn().Err(err).Msg("could not read Den's revision retention; using WIKI_REVISION_RETENTION_DAYS")
	}
	if n, err := liveMgr.PruneRevisions(sctx, retention.Days); err != nil {
		logger.Warn().Err(err).Msg("revision retention failed; retrying next hour")
	} else if n > 0 {
		logger.Info().Int("revisions", n).Int("days", retention.Days).Str("source", retention.Source).
			Msg("deleted revisions past the retention")
	}
}

func runServer(ctx context.Context, server *nethttp.Server, pool *pgxpool.Pool, liveMgr *live.Manager, shutdownTimeout time.Duration, logger zerolog.Logger) error {
	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info().Str("addr", server.Addr).Msg("http listening")
		if err := tlscert.ListenAndServe(server); err != nil && !errors.Is(err, nethttp.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-signalCtx.Done():
		logger.Info().Msg("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("http shutdown error")
	}
	// Websockets are hijacked, so server.Shutdown does not wait for them.
	if liveMgr != nil {
		liveMgr.Shutdown(shutdownCtx)
		logger.Info().Msg("live edits flushed")
	}
	pool.Close()
	logger.Info().Msg("shutdown complete")
	return nil
}
