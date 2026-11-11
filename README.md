# Wiki

> [!WARNING]
> ## **NOT READY FOR PRODUCTION**
>
> Con Services is under active development. Expect breaking changes, missing
> features and bugs. Do not use it for real events or real data yet.

Knowledge base module for the Con Services stack: Notion-style pages
nested in spaces, shared per space or per page. The plan, including live
editing and how page content is stored in S3, is in
[docs/plan.md](docs/plan.md).

Built: private and team spaces, the page tree (create, rename, move by
drag and drop, trash, restore, delete forever), sharing with people,
Keycloak groups or everyone signed in, page restriction, and the **live
block editor**: BlockNote, several people on a page at once with their
cursors and avatars, read-only for view access. On top of that:

- **Files:** image and file blocks uploaded through the backend (25 MB
  images, 50 MB files), downloads behind the page access check.
- **History:** automatic revisions (at most one per 10 minutes of
  editing), read-only preview, restore (open editors reload). Kept
  forever unless `WIKI_REVISION_RETENTION_DAYS` (or Den's module
  settings) sets a number of days.
- **Search and links:** Ctrl+K search over titles and page text,
  `@person` / `@page` mentions, a backlinks panel, favourites.
- **Comments:** inline threads on a piece of text (edit access) and
  page-level discussions (comment access), with replies, resolve and emoji
  reactions; the `comment` level replies to both without being able to
  edit the page.
- **Public share links:** read-only page (plus sub-pages) for anyone with
  the URL, switched per organisation (Den setting `wiki_public_links`,
  `PUBLIC_LINKS_ENABLED` standalone), hashed tokens, rate limited.
- **Templates and Markdown:** mark pages as templates, duplicate with
  content, import `.md` files, export pages as Markdown.
- **Databases:** Notion-style tables, boards and calendars over child
  pages with typed properties
  ([ADR 0002](docs/adr/0002-databases-as-pages.md)).
- **Offboarding:** HR's hook orphans the leaver's private space for the
  admins; a sweep deletes it after `WIKI_OFFBOARD_GRACE_DAYS` (30).

- **Backend:** Go 1.27, chi, pgx (hand-written queries), `coder/websocket`.
- **Frontend:** React 19 + Rsbuild + TanStack Router/Query + Tailwind,
  BlockNote + Yjs.
- **Database:** PostgreSQL 16, migrated via Goose (`cmd/migrate` one-shot).
- **Object storage:** S3 (`S3_*` env, bucket `wiki-data-dev` in dev).
  The backend creates the bucket on start and refuses to start if it
  cannot reach it. With `S3_ENDPOINT` empty, live editing, uploads,
  history and duplication are off (503 with a message) and everything
  else works. **Back the bucket up:** page bodies live only there, so
  `wiki-data-dev` must be in the Den operator's `FILES_S3_BUCKETS`.

## Live editing and where page content lives

The backend relays Yjs updates between the browsers on a page without
decoding them ([ADR 0001](docs/adr/0001-yjs-relay-in-go.md)). No extra
service.

- Each change is appended to `WIKI_BUFFER_DIR/<page>.log` on the
  backend's disk, shipped to S3 every 5 seconds as
  `wiki/pages/<page>/updates/<from>-<to>.bin`, and on shutdown.
- When a page has been quiet for 30 seconds, one editor's browser is asked
  for a snapshot: `wiki/pages/<page>/current.ydoc` (older ones kept under
  `revisions/`, at most one per 10 minutes). The same browser sends the
  page's text for search.
- A restart or deploy loses nothing (shutdown ships the buffer; buffer
  files left by a crash are shipped on start). Losing the disk without a
  clean shutdown loses at most the last ~5 seconds, and browsers still on
  the page resend those when they reconnect.
- **Run one backend replica** with a volume for `WIKI_BUFFER_DIR`
  (compose: `conservices-wiki-buffer`) and at least 30 seconds of stop
  grace. A reverse proxy in front needs websocket upgrades on
  `/api/v1/pages/*/live` (`infra/nginx*.conf` do this).

## Reserved ports

| Service        | Host port | Container port |
| -------------- | --------- | -------------- |
| backend (API)  | **8140**  | 8080           |
| frontend (SPA) | **3007**  | 80             |
| postgres       | **5439**  | 5432           |
| s3 (SeaweedFS) | **9040**  | 9000           |

Postgres and S3 ports only apply to the standalone stack
(`infra/docker-compose.yml`); in the suite the shared ones are used.

## Access model

Module roles come from IdP groups via `infra/access-config.toml`, checked
on every request:

| Keycloak group | Role                 | May do                                                   |
| -------------- | -------------------- | -------------------------------------------------------- |
| `wiki-admin`   | `organization_admin` | Full access to every team space and page; Den pairing.   |
| `wiki-editor`  | `space_creator`      | Create team spaces (and gets full access to the ones they create). |

**Reading and editing is not a role.** It comes from grants inside the
wiki, at one of four levels: view, comment, edit, full (full = can share,
rename and delete).

- **Private space:** everyone gets one on first sign-in. Only its owner
  sees it; `wiki-admin` does not. Single pages from it can be shared.
- **Team spaces:** closed by default. Shared with people, groups, or
  "everyone signed in" at a chosen level.
- **Pages** inherit access from their parent page (or the space) and can
  add grants of their own. A **restricted** page stops inheriting: only
  its own grants apply, to it and everything under it (`wiki-admin` keeps
  access). Restricting a page you would lose access to gives you a full
  grant on it first.
- Pages you cannot see answer 404, not 403, so their titles don't leak.
  Pages shared with you in a space you cannot see appear under "Shared
  with you".
- Trashing needs full access, or edit access on a page you created.
  Deleting forever needs full access.

Group grants match the groups claim on the token (`OIDC_GROUPS_CLAIM`,
default `groups`; never `roles`) with the suite's shared rule, ignoring case and a
leading `/` (a nested group keeps its full path), so removing someone from
a Keycloak group revokes that access on their next request. The share
dialog's group picker lists HR's departments and access groups (paired
with Den), the IdP's groups (standalone) or your own groups; any name can
be typed in.

## Run it

In the suite (from the repo root):

```powershell
docker compose --profile wiki up --build -d
```

Then https://wiki.conservices.test (or http://localhost:3007), signed in
through Keycloak. Pair it with Den under Settings → Den connection.

Standalone, with dev auth (X-Dev-User / X-Dev-Groups headers):

```powershell
cd wiki
docker compose -f infra/docker-compose.yml up --build
```

## Tests

```powershell
cd wiki/backend
$env:TEST_DATABASE_URL = 'postgres://user:pass@localhost:5432/wiki_test?sslmode=disable'
go test ./...
```

The tests drop and recreate the `public` schema of that database, so
point them at a throwaway one. `internal/http/live_test.go` opens real
websockets against an in-memory bucket; files, history, search, comments,
share links, templates, databases and offboarding each have their own
suite next to it. Frontend unit tests run with `npm test` in
`wiki/frontend`. Against the running suite, `scripts/smoke-wiki.ps1`
creates throwaway Keycloak users and checks spaces, sharing, restriction,
trash, live editing through nginx (including a backend restart
mid-session), uploads, search, comments, duplication, databases, share
links and the offboarding hook.

## Operations notes

- **One backend replica**, always: live tickets, open pages and the edit
  buffer are in-process. Deploy with `strategy: Recreate`, a volume (or
  `emptyDir`) for `WIKI_BUFFER_DIR` and ≥30 s of stop grace.
- The production ingress must pass websocket upgrades on
  `/api/v1/pages/*/live` with a long read timeout (see `infra/nginx*.conf`).
- Back up Postgres **and** the bucket; page bodies exist only in S3.
- `WIKI_SERVICE_TOKEN` guards `/api/internal/offboarded` (HR's hook);
  leave it empty to turn the internal API off.
- Public share links stay off until Den's `wiki_public_links` setting (or
  `PUBLIC_LINKS_ENABLED` standalone) switches them on.
- Uploads other than PNG, JPEG, GIF and WebP always download as sandboxed
  attachments, so an uploaded SVG or HTML file cannot run script.
- Every setting is on the
  [configuration page](https://aglelectronics.github.io/con-services/modules/wiki/configuration/).
