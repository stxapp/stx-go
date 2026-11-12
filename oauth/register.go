package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// ClientRegistrationRequest is RFC 7591 client metadata. The exchange
// accepts absolute https redirect URIs, or http on a loopback address.
type ClientRegistrationRequest struct {
	ClientName   string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
	// Scope is space-delimited. Empty means the exchange's default member scopes.
	Scope string `json:"scope,omitempty"`
	// TokenEndpointAuthMethod is "client_secret_basic" (the default; a
	// one-time secret is returned) or "none" (a public client, PKCE only).
	TokenEndpointAuthMethod string `json:"token_endpoint_auth_method,omitempty"`
	// GrantTypes is a subset of authorization_code, refresh_token and
	// client_credentials. Default authorization_code and refresh_token.
	GrantTypes      []string `json:"grant_types,omitempty"`
	ResponseTypes   []string `json:"response_types,omitempty"`
	ClientURI       string   `json:"client_uri,omitempty"`
	LogoURI         string   `json:"logo_uri,omitempty"`
	SoftwareID      string   `json:"software_id,omitempty"`
	SoftwareVersion string   `json:"software_version,omitempty"`
}

// ClientRegistration is the RFC 7591 answer.
type ClientRegistration struct {
	ClientID         string `json:"client_id"`
	ClientIDIssuedAt int64  `json:"client_id_issued_at"`
	// ClientSecret is present for a confidential client only. It is shown
	// once and cannot be recovered.
	ClientSecret string `json:"client_secret,omitempty"`
	// ClientSecretExpiresAt is Unix seconds; 0 means never.
	ClientSecretExpiresAt   int64    `json:"client_secret_expires_at,omitempty"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	Scope                   string   `json:"scope"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	ClientURI               string   `json:"client_uri,omitempty"`
	LogoURI                 string   `json:"logo_uri,omitempty"`
	SoftwareID              string   `json:"software_id,omitempty"`
	SoftwareVersion         string   `json:"software_version,omitempty"`
}

// RegisterOptions tune [Register].
type RegisterOptions struct {
	// Endpoint is the registration_endpoint from discovery, or a path.
	// Default "/oauth/register".
	Endpoint   string
	HTTPClient *http.Client
}

// Register registers an OAuth client dynamically (RFC 7591). The
// registration is inert until a member consents. Refusals are *Error values
// (invalid_redirect_uri, invalid_client_metadata, or rate_limited with
// RetryAfter). An exchange without dynamic registration answers 404, which
// is an *Error with StatusCode 404; its discovery document then has no
// registration_endpoint.
func Register(ctx context.Context, baseURL string, req ClientRegistrationRequest, opts *RegisterOptions) (*ClientRegistration, error) {
	base, err := normalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	var o RegisterOptions
	if opts != nil {
		o = *opts
	}
	target := joinURL(base, orDefault(o.Endpoint, DefaultRegisterPath))
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	res, body, err := send(ctx, o.HTTPClient, http.MethodPost, target, bytes.NewReader(data), http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		return nil, err
	}
	path := pathOf(target)
	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK {
		return nil, httpError(res, body, path)
	}
	var reg ClientRegistration
	if json.Unmarshal(body, &reg) != nil || reg.ClientID == "" {
		return nil, errorf("invalid_response", path, "the registration answer has no client_id")
	}
	return &reg, nil
}

// TermsURLWithReturn returns the URL to send a member to so they can accept
// the current STX terms and come back to your app: termsURL (from
// stx.APIError.TermsURL, or Token.TermsURL after a refresh) with return_to
// set. returnTo must be one of your registered redirect URIs, or on the same
// origin as one. See https://docs.stxapp.io/oauth/terms-and-conditions/.
func TermsURLWithReturn(termsURL, returnTo string) string {
	u, err := url.Parse(termsURL)
	if err != nil {
		return termsURL
	}
	q := u.Query()
	q.Set("return_to", returnTo)
	u.RawQuery = q.Encode()
	return u.String()
}
