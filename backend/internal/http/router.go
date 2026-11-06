// Package http wires the chi router and ties handlers to their
// dependencies. The structure deliberately mirrors the other Con Services
// modules: one Deps struct consumed by one NewRouter, with per-resource
// handler types owning their own routing closure.
//
// API shape:
//
//	GET /livez             — process liveness (unauthenticated)
//	GET /readyz            — service readiness (unauthenticated)
//	GET /api/health        — richer status for humans
//	GET /api/role-catalog  — public role catalog for HR's access picker
//	GET /api/v1/brand      — active brand package (unauthenticated)
//	GET /api/v1/me         — authenticated principal + permissions
//	GET|POST /api/v1/spaces — visible spaces (+ pages shared from elsewhere)
//	GET|PATCH|DELETE /api/v1/spaces/{space}, PUT /api/v1/spaces/{space}/grants
//	GET|POST /api/v1/spaces/{space}/pages — page tree / new page
//	GET /api/v1/spaces/{space}/trash
//	GET|PATCH /api/v1/pages/{page}, POST /api/v1/pages/{page}/move
//	POST /api/v1/pages/{page}/trash, POST /api/v1/pages/{page}/restore
//	DELETE /api/v1/pages/{page} — delete a trashed page forever
//	PUT /api/v1/pages/{page}/grants, PUT /api/v1/pages/{page}/restricted
//	POST /api/v1/pages/{page}/live-ticket, GET /api/v1/pages/{page}/live?ticket= (websocket)
//	POST /api/v1/pages/{page}/files — multipart upload (edit access)
//	GET /api/v1/files/{file} — download through the backend (view access)
//	GET /api/v1/pages/{page}/revisions — history (view access)
//	GET /api/v1/revisions/{revision} — one revision's document (view access)
//	POST /api/v1/revisions/{revision}/restore — make it current (edit access)
//	GET /api/v1/search?q= — titles and body text, filtered by access
//	GET /api/v1/pages/{page}/backlinks — pages linking here (view access)
//	GET|PUT|DELETE /api/v1/me/favorites[/{page}] — the caller's favourites
//	GET|POST /api/v1/pages/{page}/threads — discussions (comment access to write)
//	POST /api/v1/threads/{thread}/comments|resolve|unresolve, DELETE /api/v1/threads/{thread}
//	PATCH|DELETE /api/v1/comments/{comment}, POST|DELETE /api/v1/comments/{comment}/reactions
//	POST /api/v1/pages/{page}/duplicate — copy a page + subtree (templates)
//	GET /api/v1/pages/{page}/database|rows, PUT /api/v1/pages/{page}/values — databases (ADR 0002)
//	POST /api/v1/pages/{page}/properties|views, PATCH|DELETE /api/v1/properties/{id}, /api/v1/views/{id}
//	GET|POST /api/v1/pages/{page}/share-links, DELETE /api/v1/share-links/{link} — full access
//	GET /api/public/v1/shares/{token}[/pages/{page}/doc|/files/{file}] — no account,
//	rate limited; 404 unless wiki_public_links (Den) or PUBLIC_LINKS_ENABLED is on
//	POST /api/internal/offboarded — HR's hook (WIKI_SERVICE_TOKEN; 404 without one)
//	GET /api/v1/users?q=, GET /api/v1/groups — share dialog pickers
//	/api/v1/den…           — Den pairing admin (settings.manage)
//
// Everything under /api/v1 (except /brand) requires authentication; the
// auth middleware is applied inside a chi.Group so the shape is explicit.
package http

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"

	"github.com/AGlElectronics/wiki/backend/internal/auth"
	"github.com/AGlElectronics/wiki/backend/internal/brand"
	"github.com/AGlElectronics/wiki/backend/internal/config"
	"github.com/AGlElectronics/wiki/backend/internal/denlink"
	"github.com/AGlElectronics/wiki/backend/internal/domain"
	"github.com/AGlElectronics/wiki/backend/internal/live"
	"github.com/AGlElectronics/wiki/backend/internal/wiki"
)

// Deps groups the runtime dependencies the HTTP layer needs.
type Deps struct {
	Config       *config.Config
	Pool         *pgxpool.Pool
	Logger       zerolog.Logger
	Brand        brand.Provider
	OIDCVerifier *auth.OIDCVerifier
	Access       *auth.AccessConfig
	// Den is the pairing with Den; nil means a fresh standalone client.
	Den *denlink.Client
	// Wiki is the page service; nil builds one from Pool.
	Wiki *wiki.Service
	// Live is live editing; nil (no object storage) turns it off.
	Live *live.Manager
	// Files is uploads; nil (no object storage) turns them off.
	Files *wiki.Files
}

// timeoutExceptLive bounds request time, except for live editing
// websockets, which stay open while a page is.
func timeoutExceptLive(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		limited := chimiddleware.Timeout(d)(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/live") {
				next.ServeHTTP(w, r)
				return
			}
			limited.ServeHTTP(w, r)
		})
	}
}

// NewRouter builds the chi router with middleware, CORS, and routes.
// Version is passed in from cmd/server at build time.
func NewRouter(deps Deps, version string) http.Handler {
	r := chi.NewRouter()

	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Recoverer)
	r.Use(timeoutExceptLive(60 * time.Second))

	r.Use(hlog.NewHandler(deps.Logger))
	r.Use(hlog.AccessHandler(func(req *http.Request, status, size int, duration time.Duration) {
		hlog.FromRequest(req).Info().
			Str("method", req.Method).
			Str("path", req.URL.Path).
			Int("status", status).
			Int("size", size).
			Dur("duration", duration).
			Msg("http")
	}))
	r.Use(hlog.RequestIDHandler("request_id", "X-Request-ID"))

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   deps.Config.CORSAllowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Dev-User", "X-Dev-Groups", "X-Request-ID"},
		ExposedHeaders:   []string{"X-Request-ID"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	health := newHealthHandler(deps.Pool, version)
	resolver := auth.NewResolver(deps.Pool, deps.Access)
	authMW := auth.Middleware(deps.Config, resolver, deps.OIDCVerifier)

	den := deps.Den
	if den == nil {
		den = denlink.New(deps.Pool, denlink.Config{Slug: "wiki", Version: version}, deps.Logger)
	}
	brandH := &brandHandler{provider: deps.Brand, den: den}
	svc := deps.Wiki
	if svc == nil {
		svc = wiki.NewService(deps.Pool)
	}
	wh := &wikiHandler{svc: svc, live: deps.Live, files: deps.Files}

	// Orchestrator probes — unauthenticated, served from the root so they
	// don't collide with any /api/* middleware.
	r.Get("/livez", health.livez)
	r.Get("/readyz", health.readyz)

	r.Route("/api", func(api chi.Router) {
		api.Get("/health", health.get)
		api.Get("/role-catalog", roleCatalogHandler(deps.Access))

		// Server-to-server (HR's offboarding hook); 404 without a token.
		internal := &internalHandler{svc: svc, token: deps.Config.ServiceToken}
		api.Post("/internal/offboarded", internal.offboarded)

		// Public share links: read-only access for anyone with a link
		// token, rate limited, never indexed. 404 unless switched on
		// (Den's wiki_public_links setting, or PUBLIC_LINKS_ENABLED).
		share := &shareHandler{svc: svc, live: deps.Live, cfg: deps.Config, den: den}
		shareLimiter := newRateLimiter(120, 30)
		api.Route("/public/v1/shares", func(pub chi.Router) {
			pub.Use(shareLimiter.middleware)
			pub.Get("/{token}", share.get)
			pub.Get("/{token}/pages/{page}/doc", share.doc)
			pub.Get("/{token}/files/{file}", share.file)
		})

		api.Route("/v1", func(v1 chi.Router) {
			// Brand is public so the login screen can render before the
			// user authenticates.
			v1.Get("/brand", brandH.get)
			v1.Get("/brand/{role}", den.AssetHandler)
			// Authenticated by the one-time ticket in the URL.
			v1.Get("/pages/{page}/live", wh.serveLive)

			v1.Group(func(secure chi.Router) {
				secure.Use(authMW)
				secure.Get("/me", meHandler)
				prefs := den.PreferencesHandler(func(r *http.Request) (string, string, bool) {
					u, ok := auth.FromContext(r.Context())
					if !ok || u.Issuer == "" || u.Subject == "" {
						return "", "", false
					}
					return u.Issuer, u.Subject, true
				})
				secure.Get("/me/preferences", prefs)
				secure.Put("/me/preferences", prefs)
				secure.Route("/den", func(dr chi.Router) {
					dr.Use(requirePermission(domain.PermSettingsManage))
					dr.Get("/", den.StatusHandler)
					dr.Post("/pair", den.PairHandler)
					dr.Post("/sync", den.SyncHandler)
					dr.Delete("/pair", den.UnpairHandler)
				})

				secure.Get("/users", wh.with(wh.people))
				secure.Get("/groups", wh.with(wh.groups))

				secure.Get("/spaces", wh.with(wh.listSpaces))
				secure.Post("/spaces", wh.with(wh.createSpace))
				secure.Get("/spaces/{space}", wh.with(wh.getSpace))
				secure.Patch("/spaces/{space}", wh.with(wh.updateSpace))
				secure.Delete("/spaces/{space}", wh.with(wh.deleteSpace))
				secure.Put("/spaces/{space}/grants", wh.with(wh.setSpaceGrant))
				secure.Get("/spaces/{space}/pages", wh.with(wh.tree))
				secure.Post("/spaces/{space}/pages", wh.with(wh.createPage))
				secure.Get("/spaces/{space}/trash", wh.with(wh.trash))

				secure.Get("/pages/{page}", wh.with(wh.getPage))
				secure.Patch("/pages/{page}", wh.with(wh.updatePage))
				secure.Delete("/pages/{page}", wh.with(wh.deletePage))
				secure.Post("/pages/{page}/move", wh.with(wh.movePage))
				secure.Post("/pages/{page}/trash", wh.with(wh.trashPage))
				secure.Post("/pages/{page}/restore", wh.with(wh.restorePage))
				secure.Put("/pages/{page}/grants", wh.with(wh.setPageGrant))
				secure.Put("/pages/{page}/restricted", wh.with(wh.setRestricted))
				secure.Post("/pages/{page}/live-ticket", wh.with(wh.liveTicket))

				secure.Post("/pages/{page}/files", wh.with(wh.uploadFile))
				secure.Get("/files/{file}", wh.downloadFile)

				secure.Get("/pages/{page}/revisions", wh.with(wh.listRevisions))
				secure.Get("/revisions/{revision}", wh.revisionBytes)
				secure.Post("/revisions/{revision}/restore", wh.with(wh.restoreRevision))

				secure.Post("/pages/{page}/duplicate", wh.with(wh.duplicatePage))

				secure.Get("/pages/{page}/database", wh.with(wh.getDatabase))
				secure.Get("/pages/{page}/rows", wh.with(wh.listRows))
				secure.Put("/pages/{page}/values", wh.with(wh.setValues))
				secure.Post("/pages/{page}/properties", wh.with(wh.createProperty))
				secure.Patch("/properties/{property}", wh.with(wh.updateProperty))
				secure.Delete("/properties/{property}", wh.with(wh.deleteProperty))
				secure.Post("/pages/{page}/views", wh.with(wh.createView))
				secure.Patch("/views/{view}", wh.with(wh.updateView))
				secure.Delete("/views/{view}", wh.with(wh.deleteView))

				secure.Get("/pages/{page}/share-links", wh.with(wh.listShareLinks))
				secure.Post("/pages/{page}/share-links", wh.with(wh.createShareLink))
				secure.Delete("/share-links/{link}", wh.with(wh.revokeShareLink))

				secure.Get("/pages/{page}/threads", wh.with(wh.listThreads))
				secure.Post("/pages/{page}/threads", wh.with(wh.createThread))
				secure.Post("/threads/{thread}/comments", wh.with(wh.addComment))
				secure.Delete("/threads/{thread}", wh.with(wh.deleteThread))
				secure.Post("/threads/{thread}/resolve", wh.with(wh.resolveThread(true)))
				secure.Post("/threads/{thread}/unresolve", wh.with(wh.resolveThread(false)))
				secure.Patch("/comments/{comment}", wh.with(wh.updateComment))
				secure.Delete("/comments/{comment}", wh.with(wh.deleteComment))
				secure.Post("/comments/{comment}/reactions", wh.with(wh.setReaction(true)))
				secure.Delete("/comments/{comment}/reactions", wh.with(wh.setReaction(false)))

				secure.Get("/search", wh.with(wh.search))
				secure.Get("/pages/{page}/backlinks", wh.with(wh.backlinks))
				secure.Get("/me/favorites", wh.with(wh.listFavorites))
				secure.Put("/me/favorites/{page}", wh.with(wh.addFavorite))
				secure.Delete("/me/favorites/{page}", wh.with(wh.removeFavorite))
			})
		})
	})

	return r
}
