package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/AGlElectronics/wiki/backend/internal/authbase"
	"github.com/AGlElectronics/wiki/backend/internal/httperror"
)

// writeJSON serializes body with the standard content-type. Errors on the
// writer path (e.g. client disconnected) are intentionally swallowed — the
// standard access log already records the status.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

// maxJSONBody bounds every JSON request body. The largest legitimate one
// is a comment (64 KB) or a database row's values; uploads are multipart
// and live edits travel over the websocket.
const maxJSONBody = authbase.DefaultMaxBodyBytes

// decodeJSON strict-decodes the request body into dst with the shared
// decoder (size limit, unknown fields refused) and returns a typed error
// handlers can hand straight to httperror.Render: 400, 413 or 415.
func decodeJSON(r *http.Request, dst any) error {
	err := authbase.DecodeJSON(nil, r, dst, authbase.DecodeOptions{MaxBytes: maxJSONBody, DisallowUnknownFields: true})
	var de *authbase.DecodeError
	if errors.As(err, &de) {
		return httperror.New(httperror.Code(de.Code), de.Message).WithCause(err)
	}
	return err
}

// chiParam returns a raw chi URL parameter.
func chiParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}

// parseUUIDParam reads a chi URL parameter and parses it as a UUID. The
// `name` is used verbatim in the error message, so pass a human-friendly
// label like "organization" rather than the raw URL fragment name.
func parseUUIDParam(r *http.Request, name string) (uuid.UUID, error) {
	raw := chi.URLParam(r, name)
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, httperror.BadRequest("invalid " + name + " id: " + raw)
	}
	return id, nil
}
