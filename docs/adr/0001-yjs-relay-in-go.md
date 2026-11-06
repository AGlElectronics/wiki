# ADR 0001: Live editing is a Yjs relay inside the Go backend

Status: accepted (milestone 3)

## Context

Pages are edited by several people at once (Notion-style). The editor is
BlockNote, which stores its document in Yjs, a CRDT. The usual server for
Yjs is `y-websocket` (Node) or Hocuspocus (Node). The suite is Go-only and
the wiki should not add a sidecar service.

Yjs updates can be applied in any order and any number of times, and
every copy converges. So the server does not need to understand documents:
it can relay updates between browsers and store them.

## Decision

The Go backend (`internal/live`) relays and stores Yjs updates without
decoding them. Anything that needs the decoded document runs in a browser.

**Not the y-websocket protocol.** y-websocket's sync step 1/2 needs the
server to compute a state vector and a diff, which means decoding Yjs. The
wiki uses its own small framing instead (`internal/live/protocol.go`,
`frontend/src/live/WikiProvider.ts`):

| Frame | Direction | Content |
| --- | --- | --- |
| binary `1` update | both | one Yjs update |
| binary `2` batch | to browser, on join | the stored snapshot, then every update after it (u32 length + bytes, repeated) |
| binary `3` awareness | both | a y-protocols awareness update (cursors, who is here); relayed, never stored |
| binary `5` snapshot | to server | u32 header length, JSON header `{upto, text, links}`, full Yjs state |
| text `synced` | to browser | after the batch; carries the access level and last sequence number |
| text `access` | to browser | access level changed while connected |
| text `compact` | to browser | "send a snapshot covering updates up to N" |
| text `error` | to browser | e.g. a view-only person's change was refused |
| text `refresh` | to server | a new one-time ticket, every minute, so group changes apply |

Close codes: `4401` session expired (reconnect with a new ticket), `4404`
page deleted or access removed (stop), `4409` a revision was restored
(drop the local document and reconnect with a fresh one — the old copy
belongs to the replaced timeline, and re-syncing it would undo the
restore), `1012` server restarting (reconnect).

Restore works server-side without decoding Yjs: the revision's snapshot
becomes `current.ydoc`, every update segment and buffered edit of the old
timeline is dropped, and the snapshot's sequence number jumps past
everything ever written so stray old records can never re-apply. The
replaced state is kept as a `restore` revision first. Because the server
cannot merge segments into a snapshot, that kept revision is the last
compacted state; edits newer than it survive only in the restored-away
timeline's history.

The browser does the sync step itself: after the batch it computes the
server's state vector from the updates it received
(`Y.encodeStateVectorFromUpdate(Y.mergeUpdates(batch))`) and sends
whatever it has that the server lacks, i.e. edits made while disconnected.

Storage: each update gets the page's next sequence number and is appended
to `$WIKI_BUFFER_DIR/<page>.log`. Every 5 seconds (and on shutdown) the
file is rotated and uploaded to S3 as a segment
`wiki/pages/<page>/updates/<from>-<to>.bin`, indexed in `page_segments`.
When a page has been quiet for 30 seconds, or has 50 segments or 2000
buffered updates, the server asks one connected editor for a snapshot up
to the last sequence number. The browser sends the full state plus the
page's plain text and linked page ids; the server writes `current.ydoc`
(keeping the previous one as a revision at most every 10 minutes), updates
`page_search` and `page_links`, and deletes the covered segments.

Access: a browser opens the socket with a one-time ticket (30 seconds,
from `POST /pages/{id}/live-ticket`). The server re-checks access every
minute and when a fresh ticket arrives; updates from connections below
`edit` are refused.

## Consequences

- One Go process, no sidecar. One backend replica (live state is in
  memory and the buffer is on its disk); `Recreate` deploys.
- The server trusts an editor's browser for snapshots and search text. An
  editor can already write anything into the page, so this grants nothing
  new. A buggy client could produce a bad snapshot; the previous one is
  kept as a revision, and the server only accepts a snapshot from the
  editor it asked, for a sequence number it has.
- A page nobody has open is never compacted; its segments wait for the
  next visitor. Opening such a page costs one S3 read per segment.
- If the server ever has to read documents (server-side export, webhooks
  on content), run Yrs (Rust Yjs) as WebAssembly in-process with `wazero`
  (pure Go, no cgo). Nothing planned needs it.
- Standard Yjs providers (y-websocket, Hocuspocus) cannot connect; the
  wiki's own `WikiProvider` (about 250 lines) is the only client.
