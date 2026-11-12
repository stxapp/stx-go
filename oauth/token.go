package oauth

import (
	"context"
	"encoding/json"
	"time"
)

// Token is an OAuth token set: the answer of the token endpoint, and what a
// [TokenStore] keeps for a member. It marshals to JSON for storage.
type Token struct {
	// AccessToken goes in the Authorization header as a Bearer token.
	AccessToken string `json:"access_token"`
	// TokenType is "Bearer".
	TokenType string `json:"token_type,omitempty"`
	// RefreshToken renews the access token. STX rotates it on every refresh,
	// so always keep the one that came back last. Empty on an app token.
	RefreshToken string `json:"refresh_token,omitempty"`
	// Expiry is when the access token expires, or zero when unknown.
	Expiry time.Time `json:"expiry,omitzero"`
	// Scope is the granted scope, space-delimited. It may be narrower than
	// the scope asked for.
	Scope string `json:"scope,omitempty"`
	// IDToken is the OpenID Connect ID token, when openid was granted.
	// [SignIn.Finish] verifies it; do not trust it otherwise.
	IDToken string `json:"id_token,omitempty"`
	// TermsRequired is true when a refresh said the member has not accepted
	// the current STX terms. The tokens work, but orders are refused until
	// the member accepts at TermsURL. See [TermsURLWithReturn].
	TermsRequired bool `json:"terms_required,omitempty"`
	// TermsURL is the STX page where the member accepts the terms.
	TermsURL string `json:"terms_url,omitempty"`
}

// Scopes returns the granted scopes, canonical names.
func (t *Token) Scopes() []string { return ParseScopes(t.Scope) }

// Valid reports whether t has an access token that has not expired.
func (t *Token) Valid() bool {
	return t != nil && t.AccessToken != "" && (t.Expiry.IsZero() || time.Now().Before(t.Expiry))
}

// expiresWithin reports whether t expires within d of now.
func (t *Token) expiresWithin(now time.Time, d time.Duration) bool {
	return !t.Expiry.IsZero() && !now.Add(d).Before(t.Expiry)
}

// TokenSource supplies a valid access token. [*AppToken] and [*Session]
// implement it.
type TokenSource interface {
	Token(ctx context.Context) (*Token, error)
}

type tokenResponse struct {
	AccessToken   string          `json:"access_token"`
	TokenType     string          `json:"token_type"`
	RefreshToken  string          `json:"refresh_token"`
	ExpiresIn     json.Number     `json:"expires_in"`
	Scope         string          `json:"scope"`
	IDToken       string          `json:"id_token"`
	TermsRequired json.RawMessage `json:"terms_required"`
	TermsURL      string          `json:"terms_url"`
}

// parseToken reads an RFC 6749 section 5.1 token response.
func parseToken(body []byte, endpoint string, now time.Time) (*Token, error) {
	var r tokenResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, errorf("invalid_response", endpoint, "the response body is not a token response")
	}
	if r.AccessToken == "" {
		return nil, errorf("invalid_response", endpoint, "the token response has no access_token")
	}
	t := &Token{
		AccessToken:  r.AccessToken,
		TokenType:    r.TokenType,
		RefreshToken: r.RefreshToken,
		Scope:        r.Scope,
		IDToken:      r.IDToken,
	}
	if secs, err := r.ExpiresIn.Float64(); err == nil && secs > 0 {
		t.Expiry = now.Add(time.Duration(secs * float64(time.Second)))
	}
	if string(r.TermsRequired) == "true" {
		t.TermsRequired = true
		t.TermsURL = r.TermsURL
	}
	return t, nil
}
