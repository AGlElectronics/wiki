package directory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HRPath is the endpoint HR exposes for the group picker.
//
// Contract (HR implements it; Wiki only reads):
//
//	GET /api/internal/directory/groups
//	Authorization: Bearer <HR_DIRECTORY_TOKEN>
//
//	200 {"groups": [
//	  {"name": "dept-security", "display_name": "Security", "kind": "department"},
//	  {"name": "wiki-editor",   "display_name": "Wiki editors", "kind": "access"}
//	]}
//
// name is the IdP group exactly as it appears in tokens; kind is
// "department" (an HR department's group) or "access" (a module role's
// group). 401 for a wrong token; any other non-200 is an error.
const HRPath = "/api/internal/directory/groups"

// HR reads departments and access groups from HR.
type HR struct {
	baseURL string
	token   string
	client  *http.Client
}

// NewHR builds the client. baseURL is HR's backend address, e.g.
// https://hr-backend.conservices-hr.svc.cluster.local:8080.
func NewHR(baseURL, token string) *HR {
	return &HR{baseURL: strings.TrimRight(baseURL, "/"), token: token, client: &http.Client{Timeout: 10 * time.Second}}
}

// Groups calls HR.
func (h *HR) Groups(ctx context.Context) ([]Group, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+HRPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Accept", "application/json")
	res, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hr directory: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hr directory: %s", res.Status)
	}
	var body struct {
		Groups []Group `json:"groups"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("hr directory: %w", err)
	}
	for i := range body.Groups {
		if body.Groups[i].Kind != KindDepartment {
			body.Groups[i].Kind = KindAccess
		}
	}
	return body.Groups, nil
}
