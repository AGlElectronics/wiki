package http

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/rs/zerolog/hlog"

	"github.com/AGlElectronics/wiki/backend/internal/httperror"
	"github.com/AGlElectronics/wiki/backend/internal/wiki"
)

// internalHandler serves /api/internal/* — server-to-server calls guarded
// by WIKI_SERVICE_TOKEN (the Learn pattern: constant-time compare, and the
// whole surface answers 404 when no token is configured).
type internalHandler struct {
	svc   *wiki.Service
	token string
}

func (h *internalHandler) authorized(r *http.Request) bool {
	if h.token == "" {
		return false
	}
	header := r.Header.Get("Authorization")
	want := "Bearer " + h.token
	return subtle.ConstantTimeCompare([]byte(header), []byte(want)) == 1
}

// offboarded is HR's hook after someone's account is deleted: the person's
// private space is handed to the admins and swept after the grace period.
// The person is matched on issuer and subject; without an issuer (an older
// HR) on the subject alone, which is logged. Idempotent; a person the wiki
// never saw is still a 204.
func (h *internalHandler) offboarded(w http.ResponseWriter, r *http.Request) {
	if h.token == "" {
		httperror.Render(w, r, httperror.NotFound("resource"))
		return
	}
	if !h.authorized(r) {
		httperror.Render(w, r, httperror.ErrUnauthorized)
		return
	}
	var in struct {
		// Issuer is the OIDC issuer URL the subject belongs to.
		Issuer string `json:"issuer"`
		// Subject is the IdP subject (the Keycloak user id).
		Subject  string `json:"subject"`
		Username string `json:"username"`
	}
	if err := decodeJSON(r, &in); err != nil {
		httperror.Render(w, r, err)
		return
	}
	in.Issuer, in.Subject = strings.TrimSpace(in.Issuer), strings.TrimSpace(in.Subject)
	if in.Subject == "" {
		httperror.Render(w, r, httperror.Validation("invalid request", map[string]string{"subject": "required"}))
		return
	}
	log := hlog.FromRequest(r)
	if in.Issuer == "" {
		log.Warn().Str("subject", in.Subject).Str("username", in.Username).
			Msg("offboarding: no issuer sent; matching on the subject alone")
	}
	n, err := h.svc.OrphanPrivateSpaces(r.Context(), in.Issuer, in.Subject)
	if err != nil {
		httperror.Render(w, r, err)
		return
	}
	log.Info().Str("issuer", in.Issuer).Str("subject", in.Subject).Str("username", in.Username).
		Int("spaces", n).Msg("offboarding: private spaces orphaned")
	w.WriteHeader(http.StatusNoContent)
}
