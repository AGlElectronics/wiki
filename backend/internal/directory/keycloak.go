package directory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Keycloak lists realm groups through Keycloak's admin API with a
// confidential client (client credentials grant) whose service account has
// realm-management's query-groups and view-users roles.
type Keycloak struct {
	baseURL, realm, clientID, clientSecret string
	client                                 *http.Client
}

// NewKeycloak builds the client. baseURL is Keycloak's origin, e.g.
// https://keycloak.conservices-identity.svc.cluster.local:8443.
func NewKeycloak(baseURL, realm, clientID, clientSecret string) *Keycloak {
	return &Keycloak{
		baseURL: strings.TrimRight(baseURL, "/"), realm: realm, clientID: clientID, clientSecret: clientSecret,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

type kcGroup struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Path          string    `json:"path"`
	SubGroupCount int       `json:"subGroupCount"`
	SubGroups     []kcGroup `json:"subGroups"`
}

// maxGroups bounds how many groups are listed (and child calls made).
const maxGroups = 2000

// Groups lists every realm group. A top-level group is offered by its name;
// a nested one by its full path, which is how it appears in tokens with
// Keycloak's "full group path" option on.
func (k *Keycloak) Groups(ctx context.Context) ([]Group, error) {
	token, err := k.token(ctx)
	if err != nil {
		return nil, err
	}
	var top []kcGroup
	if err := k.get(ctx, token, "/groups?briefRepresentation=true&max=1000", &top); err != nil {
		return nil, err
	}
	var out []Group
	var walk func([]kcGroup) error
	walk = func(gs []kcGroup) error {
		for _, g := range gs {
			if len(out) >= maxGroups {
				return nil
			}
			name := g.Path
			if strings.Count(g.Path, "/") <= 1 {
				name = g.Name
			}
			out = append(out, Group{Name: name, DisplayName: g.Name, Kind: KindIdP})
			children := g.SubGroups
			if len(children) == 0 && g.SubGroupCount > 0 {
				if err := k.get(ctx, token, "/groups/"+url.PathEscape(g.ID)+"/children?briefRepresentation=true&max=1000", &children); err != nil {
					return err
				}
			}
			if err := walk(children); err != nil {
				return err
			}
		}
		return nil
	}
	return out, walk(top)
}

func (k *Keycloak) token(ctx context.Context) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", k.clientID)
	form.Set("client_secret", k.clientSecret)
	endpoint := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", k.baseURL, url.PathEscape(k.realm))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := k.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("idp directory token: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("idp directory token: %s", res.Status)
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil || body.AccessToken == "" {
		return "", fmt.Errorf("idp directory token: unexpected response")
	}
	return body.AccessToken, nil
}

func (k *Keycloak) get(ctx context.Context, token, path string, out any) error {
	endpoint := fmt.Sprintf("%s/admin/realms/%s%s", k.baseURL, url.PathEscape(k.realm), path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := k.client.Do(req)
	if err != nil {
		return fmt.Errorf("idp directory: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("idp directory: %s", res.Status)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
}
