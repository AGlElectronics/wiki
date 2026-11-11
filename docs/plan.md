# Wiki — plan

A Notion-style knowledge base for the Con Services suite: nested pages in
spaces, a block editor, several people editing the same page live, files
in object storage, and per-page sharing. Same shape as every other module
(own database, own bucket, OIDC against the shared Keycloak, role catalog,
Den pairing, Den's nightly backups). Live editing runs inside the Go
backend; there is no extra service.

Status: **every milestone is built** (scaffold and suite wiring, spaces,
page tree, sharing and trash, live collaborative editing with BlockNote,
files, history with restore, search, mentions, backlinks and favourites,
comments, public share links, templates with Markdown import and export,
Notion-style databases, and offboarding). Trash with restore and delete
forever landed in milestone 2 instead of 5. Live editing uses the wiki's
own websocket framing rather than the y-websocket protocol; see
[ADR 0001](adr/0001-yjs-relay-in-go.md). Databases are pages whose rows
are child pages; see [ADR 0002](adr/0002-databases-as-pages.md).

## Decisions so far

| Question | Decision |
| --- | --- |
| Editor | Block editor (BlockNote: slash menu, drag handles, callouts, tables, embeds). |
| Multi-user | Realtime collaborative editing from the first slice (Yjs), relayed by the Go backend. No Node sidecar. |
| Access | Spaces, plus per-page sharing that can widen or restrict access, like Notion. |
| Page storage | Yjs snapshots and update segments in S3; a few seconds of edits buffered on the backend pod's local disk; Postgres holds metadata, permissions and the search text, never page content. |
| Name | Wiki — `wiki.conservices.test`, Keycloak groups `wiki-*`, client `wiki-spa`. |

## Stack (same as Learn unless noted)

- **Backend (`wiki-backend`):** Go 1.25, chi, pgx, Goose migrations
  (`cmd/migrate` one-shot), go-oidc, zerolog, `minio-go/v7` (copy of
  Stash's `internal/files` client), `coder/websocket` for live editing.
  Owns the database and the bucket.
- **Frontend (`wiki-frontend`):** React 19 + Rsbuild + TanStack
  Router/Query + Tailwind, the same Layout, brand, theme, login backdrop and
  Den-connection components as Learn. Editor: `@blocknote/react` with the
  Mantine variant, recoloured with the suite's CSS tokens and loaded
  lazily (it is about 1 MB), plus `yjs` and the wiki's own provider
  (`src/live/WikiProvider.ts`) for live editing and presence (cursors,
  avatars).

### Live editing without running Yjs on the server

Yjs is designed so the server does not have to understand documents. Yjs
changes ("updates") can be applied in any order, any number of times, and
every client ends up with the same page. So the backend can be a
**relay plus an append-only log**:

- It speaks a small wire protocol of its own (a frame type byte plus
  binary payloads, and JSON control messages). It never decodes the
  updates inside. The y-websocket protocol was the first idea, but its
  sync handshake needs the server to compute Yjs state vectors; the
  browser computes the diff instead (ADR 0001).
- Updates from people with `edit` access are logged and forwarded to
  everyone else on the page. Updates from `view`/`comment` connections are
  dropped, so read-only is enforced on the server.
- Presence messages (cursors, who is here) are forwarded, never stored.
- Anything that needs the decoded document runs **in the browser**, which
  has Yjs anyway: merging the log into one snapshot ("compaction") and
  extracting title, plain text and page links for search and backlinks.
  The server asks one connected editor to do it and stores the result.

Trade-off: the server trusts an editor's browser for the snapshot and the
search text. An editor can already write anything into the page, so this
gives them no new power. A buggy client could produce a bad snapshot; the
previous snapshot is always kept as a revision, and the server never
deletes log entries newer than what the snapshot claims to include.

If the server ever has to read documents itself (e.g. server-side Markdown
export or webhooks on content), the fallback is Yrs (the Rust Yjs) compiled
to WebAssembly and run in-process with `wazero`, which is pure Go and needs
no cgo or extra service. Not needed for anything in this plan.

Recorded in [`docs/adr/0001-yjs-relay-in-go.md`](adr/0001-yjs-relay-in-go.md).

## Storage: what lives where

Page content is a Yjs document (binary CRDT state). Every client keeps a
copy, and any number of clients can edit at once and still end up with the
same page. That decides the storage layout:

| Data | Where | Why |
| --- | --- | --- |
| Current Yjs document of each page | **S3** `wiki/pages/<page_id>/current.ydoc` | The canonical page body; S3 is cheap and in Den's backup scope. |
| Revisions (history) | **S3** `wiki/pages/<page_id>/revisions/<revision_id>.ydoc` | Full snapshots; restoring is just loading one. |
| Uploads (images, attachments) | **S3** `wiki/files/<page_id>/<uuid><ext>` | Same as Stash. |
| Edits of the last few seconds | **Local disk** of the backend pod, `$WIKI_BUFFER_DIR/<page_id>.log` | Temporary buffer only. S3 cannot append, and writing an object per keystroke would be slow and costly. |
| Edits since the last snapshot | **S3** `wiki/pages/<page_id>/updates/<from>-<to>.bin` (log segments) | Shipped from the disk buffer every ~5 seconds and on shutdown. Raw Yjs updates, concatenated; the server never decodes them. |
| Which snapshot and segments a page has | **Postgres** `page_snapshots`, `page_segments` | Metadata only (keys and sequence ranges), so opening a page needs no S3 listing. |
| Page tree, titles, icons, spaces, permissions, file metadata, revision index | **Postgres** | Needs transactions and joins. |
| Search text and links between pages | **Postgres** (`page_search`, `page_links`) | Full-text search and backlinks need a query engine. Regenerated from the document on save, never edited directly. |

Save cycle:

1. **Open.** The browser connects to the page's websocket. The backend
   sends `current.ydoc` and every segment after it from S3, plus whatever
   is still in the local buffer. The browser's Yjs merges them.
2. **Edit.** Each change the browser makes is one small binary update. The
   backend checks the connection has `edit` access, gives it the page's
   next sequence number, appends it to the page's buffer file and forwards
   it to the other people on the page.
3. **Ship.** Every ~5 seconds, for each page with new buffered updates,
   the backend uploads them to S3 as one segment, records it in
   `page_segments`, then truncates the buffer file. It does the same for
   every page when it receives SIGTERM, before closing the websockets, so a
   deploy or a normal restart loses nothing.
4. **Compact.** After 50 segments or 2000 buffered updates, or when the
   page has been quiet for 30 seconds, the backend asks one connected
   editor for a snapshot "up to sequence N". The browser sends the full
   document state, plain text and outgoing page links. The backend
   writes the snapshot to S3 as `current.ydoc` (the old one becomes a
   revision), updates `page_search` and `page_links`, and deletes the
   segments up to N.
5. **Nobody left to compact.** If everyone disconnects before compaction,
   the segments simply stay in S3; the next person to open the page gets
   snapshot + segments and is asked to compact.
6. **Start-up.** Anything left in the buffer directory (the pod restarted
   without a clean shutdown but kept its disk) is shipped to S3 before the
   backend accepts connections.

What a failure costs:

| Event | Edits lost | Downtime |
| --- | --- | --- |
| Deploy, normal restart, node drain | none (flushed on SIGTERM) | seconds to a few minutes |
| Backend process crash, pod kept | none (buffer still on disk, shipped on start-up) | until it restarts |
| Pod deleted or node lost without a clean shutdown | up to the last ~5 seconds of typing | until it is rescheduled |
| S3 unreachable | none while the disk has space; shipping retries, and editing pauses with a warning if the buffer passes a size limit | none |

Browsers also keep their own copy, and the provider re-sends anything the
server is missing when it reconnects. So in the last row, people who were
still on the page get their last seconds back as soon as they reconnect;
only someone who closed the tab in that window loses them.

Deployment consequences: the backend runs as **one replica** (it already
holds "who is on this page" in memory, so this adds no new limit). Use
`strategy: Recreate` so two pods never write the same pages, and give it
an `emptyDir` for `WIKI_BUFFER_DIR` with a `terminationGracePeriodSeconds`
long enough for the shutdown flush (30 seconds is plenty). A persistent
volume is not needed. In compose, a named volume for the same directory.

The buffer and the S3 shipping sit behind a small Go interface (append,
read after N, ship, delete up to N). If the wiki ever needs several
replicas, that can become a shared log (Postgres or Redis Streams) without
touching the websocket protocol or the S3 layout.

Revisions: at most one every 10 minutes of editing per page, plus one
before every restore. Kept forever by default; `WIKI_REVISION_RETENTION_DAYS`
(or Den's module settings, `retention.revision_days` in the integration
bundle) deletes revisions older than N days, hourly, S3 object first and
then the row.

## Access model

Two layers, like Learn and Rally: **module roles** from Keycloak groups via
`infra/access-config.toml`, and **page access** stored in the wiki itself.

Module roles (in the role catalog so HR can assign them):

| Keycloak group | Role | May do |
| --- | --- | --- |
| `wiki-admin` | `organization_admin` | Every space and page, trash, settings, Den pairing. |
| `wiki-editor` | `space_creator` | Create new team spaces. |

Every signed-in staff member can use the wiki (their own private space,
and anything shared with them); there is no "wiki-reader" group.

Page access:

- **Spaces** are the top level: one **private space** per person (created on
  first login, only they see it) and **team spaces**. A team space has
  grants to people or Keycloak groups (e.g. `dept-security` from HR
  departments, or "everyone in the organization") with a level: `view`,
  `comment`, `edit` or `full` (full = may share and delete).
- **Pages** inherit their parent's access. A page can **add** grants (share
  one page with someone outside the space) or be **restricted**, which stops
  inheritance: only its own grants apply, to it and its children. This is
  Notion's model.
- Effective access = walk up from the page to the nearest restricted
  ancestor (or the space), and take the highest level among the grants
  that match the person or one of their groups. One recursive query; add a
  cache only if it gets slow.
- Group grants use the `groups` claim of the token on every request, so
  removing someone from a Keycloak group revokes access on their next
  request, same as every other module. Names are compared trimmed,
  lower-cased and without one leading `/`; full paths stay full paths
  (`groupKey` in `internal/wiki/access.go`, to be replaced by the
  suite-wide `shared/groupmatch`).
- Pages without view access return 404 (never 403 confirming they exist),
  same rule as Learn's unpublished courses. Search, backlinks and the
  sidebar filter by the same check.

Live editing sessions: browsers cannot put an `Authorization` header on a
websocket, so the page first calls `POST /api/v1/pages/{id}/live-ticket`
(normal bearer auth). That returns a one-time ticket valid for 30 seconds,
which goes in the websocket URL. The backend resolves the person's access
when the socket opens (`view`, `comment` or `edit`), re-checks it every 60
seconds against their latest groups (the browser sends a fresh ticket over
the socket every minute; without one for 3 minutes the socket closes and
the browser reconnects), and closes the connection if access was removed.

## Schema sketch (first migration)

```text
organizations, users (issuer+subject), den_link    -- same as Learn
spaces        (id, org, kind private|team, owner_user_id, name, icon, everyone_level)
space_grants  (space, principal_kind user|group, principal, level)
pages         (id, space, parent_id, position, title, icon, cover_file_id,
               restricted bool, created_by, updated_by, created_at, updated_at,
               deleted_at)                          -- soft delete = trash
page_grants   (page, principal_kind, principal, level)
page_snapshots  (page PK, storage_key, upto_seq, next_seq, updated_at)  -- current.ydoc pointer
page_segments (page, from_seq, to_seq, storage_key, size_bytes, created_at; PK page+from_seq)
page_revisions  (id, page, storage_key, size_bytes, created_by_users uuid[],
                 created_at, reason auto|restore|manual)
page_search   (page PK, title, body_text, tsv tsvector GENERATED)
page_links    (from_page, to_page)                  -- backlinks
files         (id, page, storage_key, filename, content_type, size_bytes,
               uploaded_by, created_at, deleted_at)
favorites     (user, page, position)
```

Page title and icon live in Postgres (the sidebar must not open every
document); the editor's title block writes them through the regular API.

## API (backend, first slice)

Everything below /api/v1 except brand and the ticketed live websocket is
behind the auth middleware; see internal/http/router.go for the full list.

- `GET /api/v1/me`, `GET /api/role-catalog`, `/api/v1/den…` (pairing),
  brand — same as Learn.
- Spaces: list (only those the person can see), create, update, delete,
  grants.
- Pages: tree for a space (lazy children), get metadata + effective access,
  create (in a space or under a page), rename/icon/cover, move (parent +
  position), trash, restore from trash, delete forever (admins / full
  access), grants, restrict on/off.
- Revisions: list, preview (read-only document), restore.
- Files: multipart upload through the backend (Stash's approach, no browser
  CORS to S3), download through the backend after the page access check.
  25 MB images / 50 MB attachments, like Stash.
- Search: `GET /api/v1/search?q=` across titles and text, filtered by access.
- Backlinks: `GET /api/v1/pages/{id}/backlinks`.
- Live editing: `POST /api/v1/pages/{id}/live-ticket`, then the websocket at
  `GET /api/v1/pages/{id}/live?ticket=…` (the wiki's framing, ADR 0001:
  updates, batch on join, awareness, "compact up to N" from the server,
  snapshot + text + links from the browser). 1 MB per update, 32 MB per
  snapshot, 200 messages/s per connection (bursts of 400).

## Frontend

- Left sidebar: Favourites, team spaces (tree with drag to reorder/move),
  Private, Trash, search (Ctrl+K). Same sidebar look as the other modules.
- Page view: cover, icon, title, BlockNote editor, breadcrumbs, presence
  avatars, **Share** dialog (people/group picker, levels, restrict toggle),
  **History** panel (list revisions, preview, restore).
- Blocks in the first slice: text, headings, lists, to-do, toggle, quote,
  callout, code, divider, table, image, file, page link / `@page` mention,
  `@person` mention, sub-page block (creates a child page).
- Read-only view for `view`/`comment` access.
- Brand, light/dark mode and sign-in backdrop from Den, like every module.

## Suite wiring

| Thing | Value |
| --- | --- |
| Directory | `wiki/` (`backend/`, `frontend/`, `infra/`, `docs/`), same layout as Learn |
| Compose profile | `wiki` (`wiki-migrate`, `wiki-backend`, `wiki-frontend`, like every module) |
| Host ports | API **8140**, UI **3007**, standalone Postgres **5439**. nginx needs the websocket upgrade headers and a long read timeout on `/api/v1/pages/*/live` |
| Buffer | `WIKI_BUFFER_DIR=/var/lib/wiki/buffer`, named volume `conservices-wiki-buffer` in compose, `emptyDir` in Kubernetes; one replica, `Recreate` strategy |
| Database | `wiki` / user `wiki` (add to `infra/postgres/01-init-databases.sh`) |
| Bucket | `wiki-data-dev` (add to `minio-init`, and to the operator's `FILES_S3_BUCKETS` so Den backs up page content) |
| Keycloak | client `wiki-spa`, groups `wiki-admin`, `wiki-editor` in `realm-conservices.json` and `apply-mvp.sh`; give `admin` `wiki-admin` |
| Proxy | `wiki.conservices.test` in the Caddyfile, network alias, hosts entry in `scripts/setup-local-dns.ps1` |
| HR | add `wiki=http://wiki-backend:8080/api/role-catalog` to `ROLE_CATALOG_SOURCES` |
| Den | pairing (Settings → Den connection), same as Learn |
| Smoke | `scripts/smoke-wiki.ps1` |
| README | root table rows + a Wiki note, `wiki/README.md` like Learn's |

Because page bodies are in the bucket, adding `wiki-data-dev` to the
backup bucket list is required, not optional: without it Den's backup
would contain the page tree but no page content.

## Milestones

1. **Scaffold.** Copy Learn's skeleton into `wiki/` (auth, access config,
   role catalog, brand, Den pairing, Layout), add the S3 client from Stash,
   wire everything in the table above. Done when the empty app signs in
   through Keycloak, pairs with Den and appears in HR's access picker.
2. **Spaces, page tree, access.** Private and team spaces, create / move /
   trash pages, grants and restriction, with the authorization tests
   against a real Postgres (Learn's `TEST_DATABASE_URL` pattern). Pages have
   titles and an empty body.
3. **Live editing (done).** The relay in Go, the disk buffer, segment
   shipping and the shutdown flush, compaction to S3, BlockNote in the
   frontend, text and link extraction, presence. Covered by
   `internal/live/protocol_test.go` (framing) and
   `internal/http/live_test.go` (real websockets and Postgres: relay,
   view-only refusal, presence, shutdown flush and restart, recovery of a
   buffer left by a crash, compaction, access changes while connected,
   delete forever), plus the live section of `scripts/smoke-wiki.ps1`
   (through nginx, including `docker restart` mid-session). Two browser
   tabs were checked by hand: typing syncs, the other tab shows the avatar
   and cursor, and a reload shows the saved page.
4. **Files.** Image and file blocks, uploads to S3 through the backend,
   downloads after the access check, cleanup when a page is deleted
   forever.
5. **History and trash.** Revisions list, preview, restore; trash with
   restore and delete forever.
6. **Search, links, favourites.** Ctrl+K search, `@page` / `@person`
   mentions, backlinks panel, favourites.
7. **Smoke script and docs.** `scripts/smoke-wiki.ps1` (create space →
   page → share → edit over the websocket → upload → search → revoke
   access → 404), READMEs, operations notes.

## Built after the first slice

- Notion-style databases: table, board and calendar views of child pages
  with typed properties (ADR 0002).
- Comments and inline discussions; the `comment` level now writes them.
- Public share links for people without an account (hashed tokens, Den's
  `wiki_public_links` switch, rate limited, never indexed).
- Templates, duplication, Markdown import and export (export is lossy
  markdown from the browser).
- Offboarding: HR's hook orphans private spaces; a sweep deletes them
  after the grace period.

## Still out

- Notion import, PDF export.
- Per-space storage quotas.
- Linking from Learn courses or Rally events into wiki pages.
- Reusing the attendees realm for signed-in external guests.

## Decided (was: open questions)

- **New team spaces start closed** (WIKI-12, answered the same way as
  TASK-24): `everyone_level` defaults to none; the creator grants access
  explicitly. This is the current behaviour, now confirmed.
- **Offboarding** (WIKI-13): the person's private space is handed to the
  wiki admins (visible with an "orphaned" banner, exportable), then deleted
  after a grace period (`WIKI_OFFBOARD_GRACE_DAYS`, default 30), including
  its S3 content. HR calls the wiki's internal offboard endpoint.
- **No offline editing** (WIKI-14): edits made while disconnected are kept
  only in the open tab and sent on reconnect. Persisting them in IndexedDB
  (`y-indexeddb`) is not needed.
- **Websockets on Kubernetes** (was an open question): settled by the Helm
  chart `deploy/charts/wiki` in `con-services`. The web app's nginx
  forwards `^/api/v1/pages/[^/]+/live$` with the upgrade headers and a
  1-hour read timeout, and the HTTPRoute has a separate websocket rule
  without a request timeout (`route.websocketTimeout: 0s`). Production S3
  is any S3-compatible service (`S3_ENDPOINT`); the chart's defaults point
  at the platform's Garage.
- **Uploads** (review R4): only PNG, JPEG, GIF and WebP whose bytes match
  render inline; SVG, HTML and every other file download as attachments
  with `Content-Security-Policy: sandbox` and `nosniff`.
- **Comment level and inline threads** (review R10): an inline thread's
  anchor is a mark in the Yjs document, and the relay drops document
  updates below edit level without decoding them (ADR 0001). So anchored
  threads need edit access; comment level starts page-level discussions
  (`anchored: false`) and replies, resolves and reacts in both kinds.
  Revisit only if the server ever decodes documents (Yrs fallback above).
- **Share dialog privacy** (review R5): only wiki admins see and search
  email addresses. Groups come from HR (departments and access groups,
  `GET /api/internal/directory/groups`, while paired with Den), else from
  the IdP's admin API, else the caller's own groups; free text always
  works.
- **Offboarding identity** (review R3, R9): the hook matches on issuer and
  subject (subject alone, logged, when HR sends no issuer). The sweep, page
  deletion and space deletion delete rows and queue S3 content
  (`content_deletions`) in one transaction; the queue is retried hourly.

## Open questions

None at the moment.
