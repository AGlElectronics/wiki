# Changelog

All notable changes to Wiki are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

Needs Den: 1.0.0 or later (Den MAJOR 1).

First release, `1.0.0`. Released versions are tagged `vMAJOR.MINOR.PATCH`;
the images `ghcr.io/aglelectronics/wiki-backend`, `wiki-frontend` and
`wiki-migrate` are published as `MAJOR.MINOR.PATCH-BUILD`, where `BUILD`
counts rebuilds of the same version. Rebuilds get no entry here.

### Added

- Pages nested in spaces (a team space per department or project), edited
  live by several people at once, with comment threads.
- Search over titles and text, mentions and backlinks, favourites and page
  templates.
- Two kinds of discussion: comments on a piece of text (inline, need edit
  access, because the anchor is stored in the page) and page-level
  discussions under the page, which anyone with comment access can start.
  Comment access also replies to, resolves and reacts in both kinds.
  `POST /api/v1/pages/{page}/threads` takes `"anchored": false` for a
  page-level discussion; threads report `anchored`. Migration
  `20261116000001` marks existing threads as inline.
- Access follows the organisation's groups; sensitive pages can be
  restricted, and a single page can be published read-only with a link
  that expires (when Den's `wiki_public_links` allows it).
- Page history with restore, a trash, and an offboarding hook that HR
  calls to hand a departing volunteer's private notes to the admins.
  The hook (`POST /api/internal/offboarded`) takes `issuer` and `subject`
  and matches the person on both, so the same subject from another sign-in
  server is never affected. A request without `issuer` still matches on
  the subject alone and is logged as a warning.
- Revision retention: `WIKI_REVISION_RETENTION_DAYS` (default `0`, keep
  every revision) deletes page revisions older than that many days, every
  hour, from S3 and the database. Once paired, Den can set it for Wiki in
  its module settings (`retention.revision_days` in the integration
  bundle), which wins over the local value. The history panel says how long
  revisions are kept, and `GET /api/v1/pages/{page}/revisions` returns
  `retention_days` and `retention_source`.
- Deleting a page forever, deleting a space and the offboarding sweep
  never leave page content behind in object storage. The sweep runs in one
  database transaction, and every page whose content still has to be
  removed from S3 is queued in the same transaction; content that could not
  be deleted (for example while S3 was unreachable) is retried every hour
  and at start-up. Migration `20261115000001` adds the queue.
- Databases (tables, boards, calendars) for small structured lists.
- Sharing with a group ignores upper and lower case and a leading `/`, so a
  grant to `Dept-Security` matches Keycloak's `/dept-security`. A nested
  group keeps its full path: a grant to `/org/dept-x` never matches a plain
  `dept-x`. The share dialog still shows the name as it was typed.
  Migration `20261114000001` rewrites existing group grants the same way;
  two spellings of one group on the same space or page become one grant
  with the higher level.
- `DEN_MODE=required` keeps the module locked until it is paired with Den:
  the API answers `503 den_pairing_required` (except health, brand, sign-in
  and the Den connection routes) and the public `GET /api/den/state`
  (also at `/api/v1/den/state`) reports `{"mode","paired"}`. The default
  `standalone` runs without Den; any other value stops the backend at
  startup.
- The web app shows a "Connect this module to Den" page (admins) or
  "Waiting for Den" (everyone else) while the backend runs with
  `DEN_MODE=required` and is not paired yet.
- The share dialog's group picker lists real groups: HR's departments and
  access groups while Wiki is paired with Den (`HR_DIRECTORY_URL`,
  `HR_DIRECTORY_TOKEN`; HR must provide `GET /api/internal/directory/groups`),
  otherwise the groups in the sign-in server (`IDP_DIRECTORY_URL`,
  `IDP_DIRECTORY_REALM`, `IDP_DIRECTORY_CLIENT_ID`,
  `IDP_DIRECTORY_CLIENT_SECRET`), otherwise the person's own groups. Any
  group name can still be typed in. `GET /api/v1/groups` now returns
  `{"groups":[{"name","display_name","kind"}],"source"}`. See
  [Configuration](https://aglelectronics.github.io/con-services/modules/wiki/configuration/#group-picker).
- Uses the shared Con Services packages (copies of `con-services`
  `shared/go`, see
  [Shared code](https://aglelectronics.github.io/con-services/concepts/shared-code/)):
  `groupmatch`, `denlink` and `authbase`.
- Group matching follows the suite-wide rule: groups are read only from
  `OIDC_GROUPS_CLAIM` (default `groups`; a dotted name such as
  `realm_access.roles` is followed into nested claims), the `roles` claim is
  never merged in, `OIDC_GROUP_MATCH` (`full` by default, or `last`) and
  `OIDC_GROUP_PREFIX` (for example `/con2026`) apply to role bindings and to
  space and page grants alike.
- The Den connection shows when Den rejected Wiki's credential ("pair again
  from Den"); `GET /api/den/state` and `GET /api/v1/den/` report `revoked`
  (and `revoked_at`). Migration `20261117000001` adds `den_link.revoked_at`.
  `DEN_ALLOWED_HOSTS` limits which hosts a pasted pairing URL may point at
  when `DEN_INTERNAL_URL` is empty.
- `GET /api/role-catalog` follows the 1.0 contract: it needs HR's
  `HR_SERVICE_TOKEN` or a staff sign-in, carries `revision` (also the
  `ETag`; `If-None-Match` answers `304`), and each role has `group` (stored,
  normalised), `group_display`, `privileged` (`organization_admin` is),
  `dynamic`, `default` and `scope: "organization"`.
- `TRUSTED_PROXY_CIDRS` (optional): comma-separated CIDRs of the proxies in
  front of the backend whose `X-Forwarded-For` entries are believed. The
  default, `10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,127.0.0.0/8,::1/128,fc00::/7`,
  covers the Gateway, nginx and Caddy; change it only for a proxy outside
  the private ranges, or set `none` when no proxy is in front. See
  [Client IP addresses](https://aglelectronics.github.io/con-services/operations/production/#client-ip-addresses).
- The backend can serve HTTPS. Set `TLS_CERT_FILE` and `TLS_KEY_FILE` to a
  PEM certificate and private key; the backend re-reads both every minute,
  so a certificate renewed by cert-manager is used without a restart. With
  neither set it serves plain HTTP, which is what Docker Compose
  development uses. See
  [Internal TLS](https://aglelectronics.github.io/con-services/operations/internal-tls/).
- The web app reads its installation settings (sign-in mode, issuer, client
  id, scopes, API address and product name) from `/config.json` when it
  starts, so the published `wiki-frontend` image works for every
  installation. nginx serves the file from `/etc/conservices/config.json`
  with `Cache-Control: no-store`, so a change shows on the next page load.
  Without a mounted file the web app shows "Not configured yet"; a broken
  file gives a clear error page. See
  [Web app settings](https://aglelectronics.github.io/con-services/modules/wiki/configuration/#web-app-settings-configjson).
- Kubernetes: Helm chart `deploy/charts/wiki` `1.0.0` in `con-services`,
  released with the tag `wiki/v1.0.0`. Internal TLS is on by default:
  cert-manager issues `wiki-backend-tls` and `wiki-frontend-tls`, the
  backend and the web app serve HTTPS inside the cluster (web app Service
  on port 443, backend on 8080, both named `https`), and every in-cluster
  address uses `https://`. The cluster needs cert-manager, trust-manager
  and the platform's `conservices-internal-ca` issuer; without them set
  `internalTLS.enabled: false`. See
  [Set up Wiki](https://aglelectronics.github.io/con-services/modules/wiki/setup/).
  The chart's `config:` includes `WIKI_REVISION_RETENTION_DAYS`,
  `HR_DIRECTORY_URL` and `IDP_DIRECTORY_*`; `networkPolicy.hr.enabled`
  (off by default) lets the backend reach HR for the group picker, and
  `networkPolicy.keycloakAdmin.enabled` lets it reach Keycloak's admin API
  for the same picker when standalone. To show Wiki's roles in HR's access
  picker and receive HR's offboarding hook, add Wiki to HR's
  `ROLE_CATALOG_SOURCES` and `networkPolicy.moduleEgressNamespaces` (setup
  step 8).
- Development: `infra/docker-compose.yml` with SeaweedFS as the S3 server
  (`localhost:9040`, bucket `wiki-data-dev`), and
  `infra/frontend-config/compose.json` (dev sign-in) mounted as the web
  app's `/config.json`; set `FRONTEND_CONFIG` in `.env` to use another
  file. Building from source needs Go 1.27.3 or newer.
- CI on every pull request (tests, lint, Trivy scans) and signed images with
  an SBOM published to GHCR on version tags (linux/amd64; linux/arm64
  optional).

### Security

- Rate limits cannot be bypassed with a fake `X-Forwarded-For` header: the
  backend reads the client address from it only when the connection comes
  from a trusted proxy (`TRUSTED_PROXY_CIDRS`), and never reads
  `X-Real-IP` or `True-Client-IP`.
- On Kubernetes the database URL Den shows ends in
  `?sslmode=verify-full&sslrootcert=/etc/conservices-internal-ca/ca.crt`;
  the platform's PostgreSQL refuses connections without TLS.
- No image runs as root. The web app runs on
  `nginxinc/nginx-unprivileged:1.30.6-alpine` as UID 101 and listens on
  port 8080. All images are built with Go 1.27.3 and pass the CI image
  scan (Trivy, HIGH and CRITICAL issues that have a fix).
- A production build never falls back to dev sign-in: a `/config.json`
  without `authMode` means OIDC.
- Uploaded files cannot run script on the wiki's address. Only PNG, JPEG,
  GIF and WebP images open in the browser, and only when their bytes really
  are that format (the backend checks them at upload; a file that only
  claims to be an image is stored as `application/octet-stream`). SVG,
  HTML and every other file download as an attachment. Every download,
  including through public share links, carries
  `Content-Security-Policy: sandbox` and `X-Content-Type-Options: nosniff`.
- Safe defaults: `AUTH_MODE` defaults to `oidc` and `APP_ENV` to
  `production`; `AUTH_MODE=dev` stops the backend unless
  `APP_ENV=development`. Outside dev mode the `X-Dev-User` and
  `X-Dev-Groups` headers are removed from every request and are not allowed
  through CORS. Local setups (`.env.example`, Compose) set
  `APP_ENV=development` explicitly.
- The stored Den credential is encrypted with `DEN_LINK_ENCRYPTION_KEY`
  (AES-256-GCM; required unless `APP_ENV=development`;
  `DEN_LINK_ENCRYPTION_KEY_PREVIOUS` for rotation). A credential stored in
  plain text is encrypted on the first read.
- The API checks **access tokens** for its own audience (`OIDC_AUDIENCE`,
  default `wiki-api`, added by a Keycloak audience mapper on `wiki-spa`) and
  refuses ID tokens. The web app keeps tokens in memory only, never in
  `localStorage`, and signs in again silently after a reload.
- Every API response carries `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY` and
  `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'`
  (file downloads keep `sandbox`). JSON request bodies are limited to 1 MiB
  (`413 request_too_large`). Rate limits: public share links 120 a minute
  per client (bursts of 30), the offboarding hook 60 a minute per client,
  uploads 60 a minute per person (bursts of 20); over the limit answers
  `429 rate_limited` with `Retry-After`.
- The web app's nginx sends the shared security headers (Content Security
  Policy, `X-Frame-Options`, `Referrer-Policy`, `Permissions-Policy`).
- The people picker shows email addresses only to wiki admins, and only
  they can search people by email; everyone else sees names. The group
  picker no longer lists every group seen on anyone's sign-in.
