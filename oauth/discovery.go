package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// AuthorizationServerMetadata is the exchange's RFC 8414 discovery document.
// The exchange builds every URL on the host the request used, so discover
// from the same base URL you send members to.
type AuthorizationServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RevocationEndpoint                string   `json:"revocation_endpoint,omitempty"`
	IntrospectionEndpoint             string   `json:"introspection_endpoint,omitempty"`
	RegistrationEndpoint              string   `json:"registration_endpoint,omitempty"`
	ScopesSupported                   []string `json:"scopes_supported,omitempty"`
	ResponseTypesSupported            []string `json:"response_types_supported,omitempty"`
	GrantTypesSupported               []string `json:"grant_types_supported,omitempty"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported,omitempty"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`
}

// ProtectedResourceMetadata is the exchange's RFC 9728 document.
type ProtectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported,omitempty"`
	BearerMethodsSupported []string `json:"bearer_methods_supported,omitempty"`
	ResourceName           string   `json:"resource_name,omitempty"`
}

// DiscoverMetadata fetches baseURL/.well-known/oauth-authorization-server.
// On an exchange without OAuth it returns a *stx.NotAvailableError (feature
// "oauth") that matches stx.ErrNotAvailable. hc may be nil.
func DiscoverMetadata(ctx context.Context, baseURL string, hc *http.Client) (*AuthorizationServerMetadata, error) {
	var m AuthorizationServerMetadata
	if err := fetchWellKnown(ctx, baseURL, wellKnownAuthorizationServer, hc, &m); err != nil {
		return nil, err
	}
	if m.Issuer == "" || m.AuthorizationEndpoint == "" || m.TokenEndpoint == "" {
		return nil, errorf("invalid_configuration", wellKnownAuthorizationServer, "the document has no issuer, authorization_endpoint or token_endpoint")
	}
	return &m, nil
}

// DiscoverProtectedResource fetches
// baseURL/.well-known/oauth-protected-resource. hc may be nil.
func DiscoverProtectedResource(ctx context.Context, baseURL string, hc *http.Client) (*ProtectedResourceMetadata, error) {
	var m ProtectedResourceMetadata
	if err := fetchWellKnown(ctx, baseURL, wellKnownProtectedResource, hc, &m); err != nil {
		return nil, err
	}
	if m.Resource == "" || m.AuthorizationServers == nil {
		return nil, errorf("invalid_configuration", wellKnownProtectedResource, "the document has no resource or authorization_servers")
	}
	return &m, nil
}

func fetchWellKnown(ctx context.Context, baseURL, path string, hc *http.Client, into any) error {
	base, err := normalizeBaseURL(baseURL)
	if err != nil {
		return err
	}
	target := joinURL(base, path)
	res, body, err := send(ctx, hc, http.MethodGet, target, nil, nil)
	if err != nil {
		return err
	}
	if err := notAvailable("oauth", target, res, body); err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return httpError(res, body, path)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return errorf("invalid_configuration", path, "the document is not JSON: %v", fmt.Sprint(err))
	}
	return nil
}
