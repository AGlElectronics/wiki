package http

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/rs/zerolog/hlog"

	"github.com/AGlElectronics/wiki/backend/internal/auth"
	"github.com/AGlElectronics/wiki/backend/internal/denlink"
	"github.com/AGlElectronics/wiki/backend/internal/directory"
	"github.com/AGlElectronics/wiki/backend/internal/domain"
	"github.com/AGlElectronics/wiki/backend/internal/httperror"
	"github.com/AGlElectronics/wiki/backend/internal/live"
	"github.com/AGlElectronics/wiki/backend/internal/wiki"
)

// wikiHandler owns space and page routes. Authorization lives in the wiki
// service (every method takes the Caller); handlers only translate HTTP.
type wikiHandler struct {
	svc   *wiki.Service
	live  *live.Manager
	files *wiki.Files
	den   *denlink.Client
	// Group picker directories; nil turns one off.
	hrGroups  directory.Source
	idpGroups directory.Source

	orgMu sync.Mutex
	orgID uuid.UUID
}

// handle adapts a handler that returns (status, body, error).
func handle(fn func(r *http.Request, u *auth.User) (int, any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := auth.MustFromContext(r.Context())
		if err != nil {
			httperror.Render(w, r, httperror.ErrUnauthorized)
			return
		}
		status, body, err := fn(r, u)
		if err != nil {
			httperror.Render(w, r, err)
			return
		}
		if status == http.StatusNoContent {
			w.WriteHeader(status)
			return
		}
		writeJSON(w, status, body)
	}
}

// caller projects the request principal. Roles are org-scoped; this slice
// has one organization.
func (h *wikiHandler) caller(r *http.Request, u *auth.User) (wiki.Caller, error) {
	h.orgMu.Lock()
	if h.orgID == uuid.Nil {
		id, err := h.svc.DefaultOrganization(r.Context())
		if err != nil {
			h.orgMu.Unlock()
			return wiki.Caller{}, err
		}
		h.orgID = id
	}
	orgID := h.orgID
	h.orgMu.Unlock()
	return wiki.Caller{
		UserID:       u.ID,
		Groups:       u.Groups,
		Admin:        u.HasInOrg(domain.PermWikiManage, orgID),
		CreateSpaces: u.HasInOrg(domain.PermSpaceCreate, orgID),
	}, nil
}

// with wraps a handler that needs the Caller.
func (h *wikiHandler) with(fn func(r *http.Request, c wiki.Caller) (int, any, error)) http.HandlerFunc {
	return handle(func(r *http.Request, u *auth.User) (int, any, error) {
		c, err := h.caller(r, u)
		if err != nil {
			return 0, nil, err
		}
		return fn(r, c)
	})
}

func ok(body any, err error) (int, any, error) {
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, body, nil
}

func created(body any, err error) (int, any, error) {
	if err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, body, nil
}

func noContent(err error) (int, any, error) {
	if err != nil {
		return 0, nil, err
	}
	return http.StatusNoContent, nil, nil
}

// ----------------------------------------------------------------------------
// Spaces
// ----------------------------------------------------------------------------

func (h *wikiHandler) listSpaces(r *http.Request, c wiki.Caller) (int, any, error) {
	spaces, err := h.svc.ListSpaces(r.Context(), c)
	if err != nil {
		return 0, nil, err
	}
	shared, err := h.svc.Shared(r.Context(), c)
	return ok(map[string]any{"spaces": spaces, "shared": shared, "can_create": c.Admin || c.CreateSpaces}, err)
}

func (h *wikiHandler) createSpace(r *http.Request, c wiki.Caller) (int, any, error) {
	var in wiki.SpaceInput
	if err := decodeJSON(r, &in); err != nil {
		return 0, nil, err
	}
	return created(h.svc.CreateSpace(r.Context(), c, in))
}

func (h *wikiHandler) getSpace(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "space")
	if err != nil {
		return 0, nil, err
	}
	return ok(h.svc.GetSpace(r.Context(), c, id))
}

func (h *wikiHandler) updateSpace(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "space")
	if err != nil {
		return 0, nil, err
	}
	var in wiki.SpaceInput
	if err := decodeJSON(r, &in); err != nil {
		return 0, nil, err
	}
	return ok(h.svc.UpdateSpace(r.Context(), c, id, in))
}

func (h *wikiHandler) deleteSpace(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "space")
	if err != nil {
		return 0, nil, err
	}
	return noContent(h.svc.DeleteSpace(r.Context(), c, id))
}

func (h *wikiHandler) setSpaceGrant(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "space")
	if err != nil {
		return 0, nil, err
	}
	var in wiki.GrantInput
	if err := decodeJSON(r, &in); err != nil {
		return 0, nil, err
	}
	return ok(h.svc.SetSpaceGrant(r.Context(), c, id, in))
}

func (h *wikiHandler) tree(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "space")
	if err != nil {
		return 0, nil, err
	}
	pages, err := h.svc.Tree(r.Context(), c, id)
	return ok(map[string]any{"pages": pages}, err)
}

func (h *wikiHandler) createPage(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "space")
	if err != nil {
		return 0, nil, err
	}
	var in wiki.PageInput
	if err := decodeJSON(r, &in); err != nil {
		return 0, nil, err
	}
	return created(h.svc.CreatePage(r.Context(), c, id, in))
}

func (h *wikiHandler) trash(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "space")
	if err != nil {
		return 0, nil, err
	}
	pages, err := h.svc.Trash(r.Context(), c, id)
	return ok(map[string]any{"pages": pages}, err)
}

// ----------------------------------------------------------------------------
// Pages
// ----------------------------------------------------------------------------

func (h *wikiHandler) getPage(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	return ok(h.svc.GetPage(r.Context(), c, id))
}

func (h *wikiHandler) updatePage(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	var in wiki.PageInput
	if err := decodeJSON(r, &in); err != nil {
		return 0, nil, err
	}
	return ok(h.svc.UpdatePage(r.Context(), c, id, in))
}

func (h *wikiHandler) movePage(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	var in wiki.MoveInput
	if err := decodeJSON(r, &in); err != nil {
		return 0, nil, err
	}
	return ok(h.svc.MovePage(r.Context(), c, id, in))
}

func (h *wikiHandler) trashPage(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	return noContent(h.svc.TrashPage(r.Context(), c, id))
}

func (h *wikiHandler) restorePage(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	return ok(h.svc.RestorePage(r.Context(), c, id))
}

func (h *wikiHandler) deletePage(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	return noContent(h.svc.DeletePage(r.Context(), c, id))
}

func (h *wikiHandler) setPageGrant(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	var in wiki.GrantInput
	if err := decodeJSON(r, &in); err != nil {
		return 0, nil, err
	}
	return ok(h.svc.SetPageGrant(r.Context(), c, id, in))
}

func (h *wikiHandler) setRestricted(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	var in struct {
		Restricted bool `json:"restricted"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return 0, nil, err
	}
	return ok(h.svc.SetRestricted(r.Context(), c, id, in.Restricted))
}

// ----------------------------------------------------------------------------
// Files
// ----------------------------------------------------------------------------

var errFilesOff = httperror.Unavailable("uploads need object storage (S3_ENDPOINT)")

// uploadFile stores one multipart file on a page the caller can edit.
func (h *wikiHandler) uploadFile(r *http.Request, c wiki.Caller) (int, any, error) {
	if h.files == nil {
		return 0, nil, errFilesOff
	}
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	// The limit + form overhead; the service enforces the per-type limit.
	r.Body = http.MaxBytesReader(nil, r.Body, wiki.MaxFileBytes+(1<<20))
	file, hdr, err := r.FormFile("file")
	if err != nil {
		return 0, nil, httperror.BadRequest("multipart form with a \"file\" part expected: " + err.Error())
	}
	defer file.Close() //nolint:errcheck
	out, err := h.files.Upload(r.Context(), c, id, hdr.Filename, hdr.Header.Get("Content-Type"), hdr.Size, file)
	return created(out, err)
}

// downloadFile streams a file after the page access check. Raster images
// render inline (the editor shows them); everything else downloads.
func (h *wikiHandler) downloadFile(w http.ResponseWriter, r *http.Request) {
	if h.files == nil {
		httperror.Render(w, r, errFilesOff)
		return
	}
	u, err := auth.MustFromContext(r.Context())
	if err != nil {
		httperror.Render(w, r, httperror.ErrUnauthorized)
		return
	}
	c, err := h.caller(r, u)
	if err != nil {
		httperror.Render(w, r, err)
		return
	}
	id, err := parseUUIDParam(r, "file")
	if err != nil {
		httperror.Render(w, r, err)
		return
	}
	f, rc, err := h.files.Open(r.Context(), c, id)
	if err != nil {
		httperror.Render(w, r, err)
		return
	}
	defer rc.Close() //nolint:errcheck
	writeFileHeaders(w, f)
	_, _ = io.Copy(w, rc)
}

// writeFileHeaders sets the headers of an upload download. Only raster
// images render inline; everything else is an attachment. The sandbox CSP
// keeps a file opened directly from running script on the wiki's address.
func writeFileHeaders(w http.ResponseWriter, f wiki.File) {
	disposition := "attachment"
	if wiki.Inline(f.ContentType) {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(f.SizeBytes, 10))
	w.Header().Set("Content-Disposition", disposition+`; filename="`+strings.ReplaceAll(f.Filename, `"`, "")+`"`)
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
}

// ----------------------------------------------------------------------------
// History
// ----------------------------------------------------------------------------

// listRevisions lists a page's history, newest first (view access), and
// how long revisions are kept.
func (h *wikiHandler) listRevisions(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	revs, err := h.svc.ListRevisions(r.Context(), c, id)
	if err != nil {
		return 0, nil, err
	}
	retention, err := h.svc.RevisionRetention(r.Context())
	return ok(map[string]any{
		"revisions": revs, "retention_days": retention.Days, "retention_source": retention.Source,
	}, err)
}

// revisionBytes streams one revision's Yjs document for the read-only
// preview (view access).
func (h *wikiHandler) revisionBytes(w http.ResponseWriter, r *http.Request) {
	if h.live == nil {
		httperror.Render(w, r, errLiveOff)
		return
	}
	u, err := auth.MustFromContext(r.Context())
	if err != nil {
		httperror.Render(w, r, httperror.ErrUnauthorized)
		return
	}
	c, err := h.caller(r, u)
	if err != nil {
		httperror.Render(w, r, err)
		return
	}
	id, err := parseUUIDParam(r, "revision")
	if err != nil {
		httperror.Render(w, r, err)
		return
	}
	data, err := h.live.OpenRevision(r.Context(), c, id)
	if err != nil {
		httperror.Render(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, max-age=86400") // revisions never change
	_, _ = w.Write(data)
}

// restoreRevision makes a revision the page's current document (edit
// access) and returns the page.
func (h *wikiHandler) restoreRevision(r *http.Request, c wiki.Caller) (int, any, error) {
	if h.live == nil {
		return 0, nil, errLiveOff
	}
	id, err := parseUUIDParam(r, "revision")
	if err != nil {
		return 0, nil, err
	}
	page, err := h.live.Restore(r.Context(), c, id)
	if err != nil {
		return 0, nil, err
	}
	return ok(h.svc.GetPage(r.Context(), c, page))
}

// ----------------------------------------------------------------------------
// Live editing
// ----------------------------------------------------------------------------

var errLiveOff = httperror.Unavailable("live editing needs object storage (S3_ENDPOINT)")

func (h *wikiHandler) liveTicket(r *http.Request, c wiki.Caller) (int, any, error) {
	if h.live == nil {
		return 0, nil, errLiveOff
	}
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	ticket, level, err := h.live.Ticket(r.Context(), c, id)
	return ok(map[string]any{"ticket": ticket, "level": level}, err)
}

func (h *wikiHandler) serveLive(w http.ResponseWriter, r *http.Request) {
	if h.live == nil {
		httperror.Render(w, r, errLiveOff)
		return
	}
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		httperror.Render(w, r, err)
		return
	}
	h.live.Serve(w, r, id)
}

// ----------------------------------------------------------------------------
// Databases (ADR 0002)
// ----------------------------------------------------------------------------

func (h *wikiHandler) getDatabase(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	return ok(h.svc.GetDatabase(r.Context(), c, id))
}

func (h *wikiHandler) createProperty(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	var in wiki.PropertyInput
	if err := decodeJSON(r, &in); err != nil {
		return 0, nil, err
	}
	return created(h.svc.CreateProperty(r.Context(), c, id, in))
}

func (h *wikiHandler) updateProperty(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "property")
	if err != nil {
		return 0, nil, err
	}
	var in wiki.PropertyInput
	if err := decodeJSON(r, &in); err != nil {
		return 0, nil, err
	}
	return ok(h.svc.UpdateProperty(r.Context(), c, id, in))
}

func (h *wikiHandler) deleteProperty(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "property")
	if err != nil {
		return 0, nil, err
	}
	return noContent(h.svc.DeleteProperty(r.Context(), c, id))
}

func (h *wikiHandler) createView(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	var in wiki.ViewInput
	if err := decodeJSON(r, &in); err != nil {
		return 0, nil, err
	}
	return created(h.svc.CreateView(r.Context(), c, id, in))
}

func (h *wikiHandler) updateView(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "view")
	if err != nil {
		return 0, nil, err
	}
	var in wiki.ViewInput
	if err := decodeJSON(r, &in); err != nil {
		return 0, nil, err
	}
	return ok(h.svc.UpdateView(r.Context(), c, id, in))
}

func (h *wikiHandler) deleteView(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "view")
	if err != nil {
		return 0, nil, err
	}
	return noContent(h.svc.DeleteView(r.Context(), c, id))
}

func (h *wikiHandler) listRows(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	var viewID *uuid.UUID
	if raw := r.URL.Query().Get("view"); raw != "" {
		v, err := uuid.Parse(raw)
		if err != nil {
			return 0, nil, httperror.BadRequest("invalid view id: " + raw)
		}
		viewID = &v
	}
	rows, err := h.svc.Rows(r.Context(), c, id, viewID)
	return ok(map[string]any{"rows": rows}, err)
}

func (h *wikiHandler) setValues(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	var in struct {
		Values map[uuid.UUID]json.RawMessage `json:"values"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return 0, nil, err
	}
	return ok(h.svc.SetValues(r.Context(), c, id, in.Values))
}

// ----------------------------------------------------------------------------
// Duplicate (templates)
// ----------------------------------------------------------------------------

// duplicatePage copies a page and its subtree (used by "new from
// template" and plain duplication). Content is copied byte for byte when
// object storage is on; without it the copies are empty pages.
func (h *wikiHandler) duplicatePage(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	var in wiki.DuplicateInput
	if err := decodeJSON(r, &in); err != nil {
		return 0, nil, err
	}
	page, mapping, err := h.svc.DuplicatePage(r.Context(), c, id, in)
	if err != nil {
		return 0, nil, err
	}
	if h.live != nil {
		if err := h.live.CopyContent(r.Context(), mapping); err != nil {
			// The pages exist; some content may be missing. Say so rather
			// than failing the whole copy.
			return http.StatusCreated, map[string]any{"page": page, "warning": "some page content could not be copied"}, nil
		}
	}
	return http.StatusCreated, map[string]any{"page": page}, nil
}

// ----------------------------------------------------------------------------
// Comments
// ----------------------------------------------------------------------------

type commentBody struct {
	Body     json.RawMessage `json:"body"`
	Metadata json.RawMessage `json:"metadata"`
}

func (h *wikiHandler) listThreads(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	threads, err := h.svc.Threads(r.Context(), c, id)
	return ok(map[string]any{"threads": threads}, err)
}

func (h *wikiHandler) createThread(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	var in commentBody
	if err := decodeJSON(r, &in); err != nil {
		return 0, nil, err
	}
	return created(h.svc.CreateThread(r.Context(), c, id, in.Body, in.Metadata))
}

func (h *wikiHandler) addComment(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "thread")
	if err != nil {
		return 0, nil, err
	}
	var in commentBody
	if err := decodeJSON(r, &in); err != nil {
		return 0, nil, err
	}
	return created(h.svc.AddComment(r.Context(), c, id, in.Body))
}

func (h *wikiHandler) updateComment(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "comment")
	if err != nil {
		return 0, nil, err
	}
	var in commentBody
	if err := decodeJSON(r, &in); err != nil {
		return 0, nil, err
	}
	return ok(h.svc.UpdateComment(r.Context(), c, id, in.Body))
}

func (h *wikiHandler) deleteComment(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "comment")
	if err != nil {
		return 0, nil, err
	}
	return ok(h.svc.DeleteComment(r.Context(), c, id))
}

func (h *wikiHandler) deleteThread(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "thread")
	if err != nil {
		return 0, nil, err
	}
	return noContent(h.svc.DeleteThread(r.Context(), c, id))
}

func (h *wikiHandler) resolveThread(resolved bool) func(*http.Request, wiki.Caller) (int, any, error) {
	return func(r *http.Request, c wiki.Caller) (int, any, error) {
		id, err := parseUUIDParam(r, "thread")
		if err != nil {
			return 0, nil, err
		}
		return ok(h.svc.SetResolved(r.Context(), c, id, resolved))
	}
}

func (h *wikiHandler) setReaction(add bool) func(*http.Request, wiki.Caller) (int, any, error) {
	return func(r *http.Request, c wiki.Caller) (int, any, error) {
		id, err := parseUUIDParam(r, "comment")
		if err != nil {
			return 0, nil, err
		}
		var in struct {
			Emoji string `json:"emoji"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return 0, nil, err
		}
		return ok(h.svc.SetReaction(r.Context(), c, id, in.Emoji, add))
	}
}

// ----------------------------------------------------------------------------
// Search, backlinks, favourites
// ----------------------------------------------------------------------------

func (h *wikiHandler) search(r *http.Request, c wiki.Caller) (int, any, error) {
	hits, err := h.svc.Search(r.Context(), c, r.URL.Query().Get("q"))
	return ok(map[string]any{"hits": hits}, err)
}

func (h *wikiHandler) backlinks(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	hits, err := h.svc.Backlinks(r.Context(), c, id)
	return ok(map[string]any{"hits": hits}, err)
}

func (h *wikiHandler) listFavorites(r *http.Request, c wiki.Caller) (int, any, error) {
	favs, err := h.svc.ListFavorites(r.Context(), c)
	return ok(map[string]any{"favorites": favs}, err)
}

func (h *wikiHandler) addFavorite(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	return noContent(h.svc.AddFavorite(r.Context(), c, id))
}

func (h *wikiHandler) removeFavorite(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	return noContent(h.svc.RemoveFavorite(r.Context(), c, id))
}

// ----------------------------------------------------------------------------
// Share dialog pickers
// ----------------------------------------------------------------------------

func (h *wikiHandler) people(r *http.Request, c wiki.Caller) (int, any, error) {
	// ?ids= looks up specific people (comment authors); ?q= searches.
	if raw := r.URL.Query().Get("ids"); raw != "" {
		var ids []uuid.UUID
		for _, part := range strings.Split(raw, ",") {
			id, err := uuid.Parse(strings.TrimSpace(part))
			if err != nil {
				return 0, nil, httperror.BadRequest("invalid user id: " + part)
			}
			ids = append(ids, id)
		}
		people, err := h.svc.PeopleByIDs(r.Context(), c, ids)
		return ok(map[string]any{"people": people}, err)
	}
	people, err := h.svc.ListPeople(r.Context(), c, r.URL.Query().Get("q"))
	return ok(map[string]any{"people": people}, err)
}

// groupSource picks the group picker's directory: HR while paired with
// Den, else the identity provider, else none (the caller's own groups).
func (h *wikiHandler) groupSource() (directory.Source, string) {
	switch {
	case h.hrGroups != nil && h.den != nil && h.den.Paired():
		return h.hrGroups, "hr"
	case h.idpGroups != nil:
		return h.idpGroups, "idp"
	}
	return nil, "token"
}

// groups lists groups for the share dialog. Any group name can still be
// typed in; this only helps picking. A failing directory falls back to the
// caller's own groups with a warning.
func (h *wikiHandler) groups(r *http.Request, c wiki.Caller) (int, any, error) {
	src, name := h.groupSource()
	if src != nil {
		groups, err := src.Groups(r.Context())
		if err == nil {
			return ok(map[string]any{"groups": groups, "source": name}, nil)
		}
		hlog.FromRequest(r).Warn().Err(err).Str("source", name).Msg("group directory unavailable; offering the caller's groups")
		return ok(map[string]any{
			"groups": directory.FromToken(c.Groups), "source": "token",
			"warning": "The group directory is unavailable; showing your own groups. You can still type any group name.",
		}, nil)
	}
	return ok(map[string]any{"groups": directory.FromToken(c.Groups), "source": "token"}, nil)
}
