package http

import (
	"encoding/base64"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/AGlElectronics/wiki/backend/internal/config"
	"github.com/AGlElectronics/wiki/backend/internal/denlink"
	"github.com/AGlElectronics/wiki/backend/internal/httperror"
	"github.com/AGlElectronics/wiki/backend/internal/live"
	"github.com/AGlElectronics/wiki/backend/internal/wiki"
)

// PublicLinksSetting is the Den org setting that switches share links on.
const PublicLinksSetting = "wiki_public_links"

// shareHandler serves /api/public/v1/shares/* — read-only page access for
// anyone holding a link token, without an account. Everything answers 404
// when the feature is off, the token is unknown, revoked or expired, or
// the page is in the trash, so responses never confirm anything exists.
type shareHandler struct {
	svc  *wiki.Service
	live *live.Manager
	cfg  *config.Config
	den  *denlink.Client
}

// enabled: Den's setting wins once paired; standalone uses the env switch.
func (h *shareHandler) enabled() bool {
	if v, ok := h.den.BoolSetting(PublicLinksSetting); ok {
		return v
	}
	return h.cfg.PublicLinksEnabled
}

// scope resolves the token in the URL, or answers the uniform 404.
func (h *shareHandler) scope(w http.ResponseWriter, r *http.Request) *wiki.ShareScope {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if !h.enabled() {
		httperror.Render(w, r, httperror.NotFound("share"))
		return nil
	}
	token := chiParam(r, "token")
	if len(token) < 20 || len(token) > 200 {
		httperror.Render(w, r, httperror.NotFound("share"))
		return nil
	}
	sc, err := h.svc.ResolveShare(r.Context(), token)
	if err != nil {
		httperror.Render(w, r, err)
		return nil
	}
	return sc
}

// get returns the shared page (and subtree, when included).
func (h *shareHandler) get(w http.ResponseWriter, r *http.Request) {
	sc := h.scope(w, r)
	if sc == nil {
		return
	}
	writeJSON(w, http.StatusOK, sc.View())
}

// doc returns one page's document as base64 updates the browser merges.
func (h *shareHandler) doc(w http.ResponseWriter, r *http.Request) {
	sc := h.scope(w, r)
	if sc == nil {
		return
	}
	if h.live == nil {
		httperror.Render(w, r, errLiveOff)
		return
	}
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		httperror.Render(w, r, err)
		return
	}
	if !sc.Allows(id) {
		httperror.Render(w, r, httperror.NotFound("page"))
		return
	}
	updates, err := h.live.LoadDocument(r.Context(), id)
	if err != nil {
		httperror.Render(w, r, err)
		return
	}
	encoded := make([]string, len(updates))
	for i, u := range updates {
		encoded[i] = base64.StdEncoding.EncodeToString(u)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"updates": encoded})
}

// file streams an upload referenced by a shared page.
func (h *shareHandler) file(w http.ResponseWriter, r *http.Request) {
	sc := h.scope(w, r)
	if sc == nil {
		return
	}
	if h.live == nil {
		httperror.Render(w, r, errFilesOff)
		return
	}
	id, err := parseUUIDParam(r, "file")
	if err != nil {
		httperror.Render(w, r, err)
		return
	}
	f, key, err := h.svc.ShareFile(r.Context(), sc, id)
	if err != nil {
		httperror.Render(w, r, err)
		return
	}
	rc, err := h.live.OpenObject(r.Context(), key)
	if err != nil {
		httperror.Render(w, r, err)
		return
	}
	defer rc.Close() //nolint:errcheck
	disposition := "attachment"
	if strings.HasPrefix(f.ContentType, "image/") {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(f.SizeBytes, 10))
	w.Header().Set("Content-Disposition", disposition+`; filename="`+strings.ReplaceAll(f.Filename, `"`, "")+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = io.Copy(w, rc)
}

// ----------------------------------------------------------------------------
// Authenticated link management (full access on the page)
// ----------------------------------------------------------------------------

func (h *wikiHandler) listShareLinks(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	links, err := h.svc.ListShareLinks(r.Context(), c, id)
	return ok(map[string]any{"links": links}, err)
}

func (h *wikiHandler) createShareLink(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "page")
	if err != nil {
		return 0, nil, err
	}
	var in wiki.ShareLinkInput
	if err := decodeJSON(r, &in); err != nil {
		return 0, nil, err
	}
	link, token, err := h.svc.CreateShareLink(r.Context(), c, id, in)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, map[string]any{"link": link, "token": token}, nil
}

func (h *wikiHandler) revokeShareLink(r *http.Request, c wiki.Caller) (int, any, error) {
	id, err := parseUUIDParam(r, "link")
	if err != nil {
		return 0, nil, err
	}
	return noContent(h.svc.RevokeShareLink(r.Context(), c, id))
}
