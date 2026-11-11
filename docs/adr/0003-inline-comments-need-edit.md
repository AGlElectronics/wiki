# ADR 0003 — Inline comments need edit access; comment level starts page discussions

Status: accepted (pre-1.0.0 review, item R10).

## Context

An inline comment thread is anchored to text by a comment mark that
BlockNote writes into the page's Yjs document. The relay accepts document
updates only from connections with edit access and never decodes them
([ADR 0001](0001-yjs-relay-in-go.md)). A person with `comment` access
could create the thread row through the REST API, but the mark would be
dropped on the server: the thread would exist with no anchor, invisible in
the editor.

## Decision

- Threads have an `anchored` flag. `POST /api/v1/pages/{page}/threads`
  creates an anchored thread by default, which needs **edit** access.
- With `"anchored": false` the thread is a **page-level discussion**, which
  needs **comment** access. The page shows these under the editor
  ("Discussion").
- Replying, resolving, reopening and reacting need comment access on both
  kinds; deleting a whole thread needs edit, as before.
- The editor's thread store only holds anchored threads and only lets
  editors start one, so the UI never offers what the server refuses.

## Alternatives not taken

- **Let the server accept comment-mark-only updates.** It would have to
  decode Yjs to tell a mark from a text change, which ADR 0001 rules out.
  Revisit if the server ever runs Yrs (the WebAssembly fallback in
  `docs/plan.md`).
- **Store anchors outside the document** (positions in the REST API).
  Positions drift as people edit; keeping them right needs the same
  decoding.

## Consequences

People with comment access can discuss a page and take part in inline
threads, but cannot point at a sentence. The user and API docs say so.
