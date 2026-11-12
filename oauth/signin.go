package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	stx "github.com/stxapp/stx-go"
)

// OpenIDConfiguration is the exchange's OpenID Connect discovery document,
// as far as [SignIn] uses it.
type OpenIDConfiguration struct {
	Issuer                           string   `json:"issuer"`
	AuthorizationEndpoint            string   `json:"authorization_endpoint"`
	TokenEndpoint                    string   `json:"token_endpoint"`
	JWKSURI                          string   `json:"jwks_uri"`
	UserinfoEndpoint                 string   `json:"userinfo_endpoint,omitempty"`
	RevocationEndpoint               string   `json:"revocation_endpoint,omitempty"`
	IDTokenSigningAlgValuesSupported []string `json:"id_token_signing_alg_values_supported,omitempty"`
}

// SignInConfig describes one confidential app using sign in with STX.
type SignInConfig struct {
	// Issuer is the exchange's public URL, such as "https://demo.stxapp.io".
	// It must equal the discovery document's issuer.
	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURL is the redirect URI registered with STX.
	RedirectURL string
	// Scopes are asked for besides openid, which is always added.
	Scopes []string
	// ClockSkew is the leeway when checking exp and iat. Default 60 seconds.
	ClockSkew  time.Duration
	HTTPClient *http.Client
}

// SignIn is sign in with STX: OpenID Connect on top of the authorization code
// flow, giving a server who the member is and, with member scopes, their
// grant, in one round trip. Server-side only: the secret and the tokens never
// belong in a browser. It is safe for concurrent use.
//
// See https://docs.stxapp.io/oauth/sign-in-with-your-stx-account/.
type SignIn struct {
	cfg    SignInConfig
	issuer string
	now    func() time.Time

	mu     sync.Mutex
	config *OpenIDConfiguration
	client *Client
	keys   []jwk
}

// NewSignIn returns a SignIn for cfg. It makes no request.
func NewSignIn(cfg SignInConfig) (*SignIn, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.RedirectURL == "" {
		return nil, fmt.Errorf("%w: oauth: Issuer, ClientID, ClientSecret and RedirectURL are required", stx.ErrConfig)
	}
	if cfg.ClockSkew <= 0 {
		cfg.ClockSkew = time.Minute
	}
	return &SignIn{cfg: cfg, issuer: strings.TrimRight(cfg.Issuer, "/"), now: time.Now}, nil
}

// Configuration returns the discovery document, fetched once and cached. An
// exchange without sign in with STX returns an error matching
// stx.ErrNotAvailable (feature "connect").
func (s *SignIn) Configuration(ctx context.Context) (*OpenIDConfiguration, error) {
	s.mu.Lock()
	cached := s.config
	s.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	target := s.issuer + wellKnownOpenID
	res, body, err := send(ctx, s.cfg.HTTPClient, http.MethodGet, target, nil, nil)
	if err != nil {
		return nil, err
	}
	if na := notAvailable("connect", target, res, body); na != nil {
		return nil, na
	}
	if res.StatusCode != http.StatusOK {
		return nil, httpError(res, body, wellKnownOpenID)
	}
	var c OpenIDConfiguration
	if json.Unmarshal(body, &c) != nil || c.Issuer == "" || c.AuthorizationEndpoint == "" || c.TokenEndpoint == "" || c.JWKSURI == "" {
		return nil, errorf("invalid_configuration", wellKnownOpenID, "the discovery document is incomplete")
	}
	if strings.TrimRight(c.Issuer, "/") != s.issuer {
		return nil, errorf("invalid_configuration", wellKnownOpenID, "the discovery issuer %s does not match %s", c.Issuer, s.issuer)
	}
	oc, err := New(Config{
		BaseURL:      s.issuer,
		ClientID:     s.cfg.ClientID,
		ClientSecret: s.cfg.ClientSecret,
		RedirectURL:  s.cfg.RedirectURL,
		HTTPClient:   s.cfg.HTTPClient,
		Endpoint:     Endpoint{AuthURL: c.AuthorizationEndpoint, TokenURL: c.TokenEndpoint, RevokeURL: c.RevocationEndpoint},
	})
	if err != nil {
		return nil, err
	}
	oc.now = s.now
	s.mu.Lock()
	s.config, s.client = &c, oc
	s.mu.Unlock()
	return &c, nil
}

func (s *SignIn) oauth(ctx context.Context) (*Client, error) {
	if _, err := s.Configuration(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client, nil
}

// StartParams are the options of [SignIn.Start].
type StartParams struct {
	// Connection goes straight to a sign-in provider: "google", "apple" or "x".
	Connection string
	// LoginHint is the member's email, if known.
	LoginHint string
	// Scopes override SignInConfig.Scopes for this sign-in.
	Scopes []string
	// RedirectURL overrides SignInConfig.RedirectURL.
	RedirectURL string
	// Prompt is "none" for a silent reconnect, or "login" or "consent".
	Prompt string
}

// SavedSignIn is what to keep in your server session between
// [SignIn.Start] and [SignIn.Finish]. It marshals to JSON.
type SavedSignIn struct {
	State        string `json:"state"`
	CodeVerifier string `json:"code_verifier"`
	Nonce        string `json:"nonce"`
	RedirectURL  string `json:"redirect_url,omitempty"`
}

// StartedSignIn is a prepared sign-in.
type StartedSignIn struct {
	// URL is where to send the member's browser.
	URL string
	// Saved goes in your server session for Finish.
	Saved SavedSignIn
}

// Start prepares a sign-in: PKCE, state and nonce, and the authorize URL with
// openid added to the scopes.
func (s *SignIn) Start(ctx context.Context, p StartParams) (*StartedSignIn, error) {
	c, err := s.oauth(ctx)
	if err != nil {
		return nil, err
	}
	verifier, state, nonce := GenerateVerifier(), GenerateState(), GenerateState()
	redirect := orDefault(p.RedirectURL, s.cfg.RedirectURL)
	extra := url.Values{"nonce": {nonce}}
	if p.Connection != "" {
		extra.Set("connection", p.Connection)
	}
	if p.LoginHint != "" {
		extra.Set("login_hint", p.LoginHint)
	}
	scopes := p.Scopes
	if scopes == nil {
		scopes = s.cfg.Scopes
	}
	u, err := c.authorizeURL(state, S256ChallengeFromVerifier(verifier), withOpenID(scopes), p.Prompt, redirect, extra)
	if err != nil {
		return nil, err
	}
	return &StartedSignIn{URL: u, Saved: SavedSignIn{State: state, CodeVerifier: verifier, Nonce: nonce, RedirectURL: redirect}}, nil
}

func withOpenID(scopes []string) string {
	out := []string{ScopeOpenID}
	for _, s := range scopes {
		for _, f := range strings.Fields(s) {
			if f != ScopeOpenID && !contains(out, f) {
				out = append(out, f)
			}
		}
	}
	return FormatScopes(out)
}

// Identity is the member, from the verified ID token or the userinfo
// endpoint.
type Identity struct {
	// Subject is the member's stable id for your app. Key your user record on
	// it, not on the email.
	Subject       string
	Email         string
	EmailVerified bool
	// Claims holds every claim.
	Claims map[string]any
}

// FinishedSignIn is a completed sign-in.
type FinishedSignIn struct {
	Token    *Token
	Identity *Identity
}

// Finish completes a sign-in from the callback's query and the saved state:
// it checks the state, exchanges the code, and verifies the ID token
// (signature, issuer, audience, expiry, nonce). Callback errors match
// [ErrAccessDenied], [ErrAccountPending] or [ErrTermsNotAccepted]; a state
// that is not this sign-in's matches [ErrInvalidState].
func (s *SignIn) Finish(ctx context.Context, query url.Values, saved SavedSignIn) (*FinishedSignIn, error) {
	conf, err := s.Configuration(ctx)
	if err != nil {
		return nil, err
	}
	where := pathOf(conf.AuthorizationEndpoint)
	code, state, cbErr := query.Get("code"), query.Get("state"), query.Get("error")
	if cbErr == "" && (code == "" || state == "") {
		return nil, &Error{Code: "invalid_request", Description: "the callback carries no code or no state", StatusCode: 400, Endpoint: where}
	}
	if state == "" || saved.State == "" || !constantTimeEqual(state, saved.State) {
		return nil, &Error{Code: "invalid_state", Description: "the state does not match this sign-in; start again", StatusCode: 400, Endpoint: where}
	}
	if cbErr != "" {
		return nil, callbackError(cbErr, query.Get("error_description"), where)
	}
	c, err := s.oauth(ctx)
	if err != nil {
		return nil, err
	}
	tok, err := c.Exchange(ctx, code, saved.CodeVerifier, orDefault(saved.RedirectURL, s.cfg.RedirectURL))
	if err != nil {
		return nil, err
	}
	if tok.IDToken == "" {
		return nil, errorf("invalid_id_token", pathOf(conf.TokenEndpoint), "the token response has no id_token; ask for the openid scope")
	}
	claims, err := s.verifyIDToken(ctx, conf, tok.IDToken, saved.Nonce)
	if err != nil {
		return nil, err
	}
	return &FinishedSignIn{Token: tok, Identity: identityFrom(claims)}, nil
}

// UserInfo reads the member's claims from the userinfo endpoint with their
// access token, which must carry openid. Pass the subject you stored for the
// member as expectedSubject and an answer about anyone else is refused; ""
// skips that check. A dead token, or one without openid, is an *Error with
// Code "invalid_token".
func (s *SignIn) UserInfo(ctx context.Context, accessToken, expectedSubject string) (*Identity, error) {
	conf, err := s.Configuration(ctx)
	if err != nil {
		return nil, err
	}
	if conf.UserinfoEndpoint == "" {
		return nil, errorf("invalid_configuration", wellKnownOpenID, "the discovery document names no userinfo endpoint")
	}
	where := pathOf(conf.UserinfoEndpoint)
	res, body, err := send(ctx, s.cfg.HTTPClient, http.MethodGet, conf.UserinfoEndpoint, nil, bearer(accessToken))
	if err != nil {
		return nil, err
	}
	if na := notAvailable("connect", conf.UserinfoEndpoint, res, body); na != nil {
		return nil, na
	}
	var claims map[string]any
	_ = json.Unmarshal(body, &claims)
	if res.StatusCode/100 != 2 {
		e := httpError(res, body, where)
		if e.Code == "http_error" && res.StatusCode == http.StatusUnauthorized {
			e.Code, e.Description = "invalid_token", ""
		}
		return nil, e
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return nil, errorf("invalid_userinfo", where, "the userinfo answer has no subject")
	}
	// OpenID Connect Core 5.3.2: an answer about another subject must not be used.
	if expectedSubject != "" && sub != expectedSubject {
		return nil, errorf("invalid_userinfo", where, "the userinfo answer is about a different member")
	}
	return identityFrom(claims), nil
}

// Refresh trades a refresh token for a new pair. Keep the refresh token that
// comes back: STX rotates it.
func (s *SignIn) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	c, err := s.oauth(ctx)
	if err != nil {
		return nil, err
	}
	return c.Refresh(ctx, refreshToken)
}

// Revoke revokes one token. A refresh token ends the member's connection.
func (s *SignIn) Revoke(ctx context.Context, token, hint string) error {
	c, err := s.oauth(ctx)
	if err != nil {
		return err
	}
	return c.Revoke(ctx, token, hint)
}

// Disconnect ends the member's connection by revoking the refresh token, or
// the access token when there is no refresh token.
func (s *SignIn) Disconnect(ctx context.Context, t *Token) error {
	if t.RefreshToken != "" {
		return s.Revoke(ctx, t.RefreshToken, "refresh_token")
	}
	return s.Revoke(ctx, t.AccessToken, "access_token")
}

func identityFrom(claims map[string]any) *Identity {
	id := &Identity{Claims: claims}
	id.Subject, _ = claims["sub"].(string)
	id.Email, _ = claims["email"].(string)
	switch v := claims["email_verified"].(type) {
	case bool:
		id.EmailVerified = v
	case string:
		id.EmailVerified = v == "true"
	}
	return id
}

// jwks returns the issuer's signing keys, fetched again when refresh is set.
func (s *SignIn) jwks(ctx context.Context, conf *OpenIDConfiguration, refresh bool) ([]jwk, error) {
	s.mu.Lock()
	keys := s.keys
	s.mu.Unlock()
	if keys != nil && !refresh {
		return keys, nil
	}
	res, body, err := send(ctx, s.cfg.HTTPClient, http.MethodGet, conf.JWKSURI, nil, nil)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, httpError(res, body, pathOf(conf.JWKSURI))
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if json.Unmarshal(body, &set) != nil || set.Keys == nil {
		return nil, errorf("invalid_configuration", pathOf(conf.JWKSURI), "the key set has no keys")
	}
	s.mu.Lock()
	s.keys = set.Keys
	s.mu.Unlock()
	return set.Keys, nil
}

func (s *SignIn) verifyIDToken(ctx context.Context, conf *OpenIDConfiguration, token, nonce string) (map[string]any, error) {
	where := pathOf(conf.TokenEndpoint)
	fail := func(why string) error { return errorf("invalid_id_token", where, "%s", why) }
	h, claims, signed, sig, err := splitJWT(token)
	if err != nil {
		return nil, fail(err.Error())
	}
	pick := func(keys []jwk) *jwk {
		for i := range keys {
			k := &keys[i]
			if (h.Kid == "" || k.Kid == h.Kid) && (k.Alg == "" || k.Alg == h.Alg) && k.Use != "enc" {
				return k
			}
		}
		return nil
	}
	keys, err := s.jwks(ctx, conf, false)
	if err != nil {
		return nil, err
	}
	key := pick(keys)
	if key == nil && h.Kid != "" {
		// The issuer may have rotated its keys since they were cached.
		if keys, err = s.jwks(ctx, conf, true); err != nil {
			return nil, err
		}
		key = pick(keys)
	}
	if key == nil {
		return nil, fail("no published key matches the ID token")
	}
	// Only an algorithm the issuer says it signs ID tokens with, whatever the
	// token's header claims (RS256 when discovery names none).
	algs := conf.IDTokenSigningAlgValuesSupported
	if len(algs) == 0 {
		algs = []string{"RS256"}
	}
	if !contains(algs, h.Alg) {
		return nil, fail("the ID token algorithm " + h.Alg + " is not one the issuer signs with")
	}
	if err := key.verify(h.Alg, signed, sig); err != nil {
		return nil, fail(err.Error())
	}
	if iss, _ := claims["iss"].(string); iss != conf.Issuer {
		return nil, fail("the ID token was issued by someone else")
	}
	var auds []string
	switch a := claims["aud"].(type) {
	case string:
		auds = []string{a}
	case []any:
		for _, v := range a {
			if s, ok := v.(string); ok {
				auds = append(auds, s)
			}
		}
	}
	if !contains(auds, s.cfg.ClientID) {
		return nil, fail("the ID token is for another app")
	}
	if azp, _ := claims["azp"].(string); len(auds) > 1 && azp != s.cfg.ClientID {
		return nil, fail("the ID token is for another app")
	}
	now := s.now()
	skew := s.cfg.ClockSkew
	exp, ok := numericDate(claims["exp"])
	if !ok || exp.Add(skew).Before(now) {
		return nil, fail("the ID token has expired")
	}
	if iat, ok := numericDate(claims["iat"]); ok && iat.Add(-skew).After(now) {
		return nil, fail("the ID token is not valid yet")
	}
	if n, _ := claims["nonce"].(string); n == "" || !constantTimeEqual(n, nonce) {
		return nil, fail("the ID token nonce does not match this sign-in")
	}
	if sub, _ := claims["sub"].(string); sub == "" {
		return nil, fail("the ID token has no subject")
	}
	return claims, nil
}

func numericDate(v any) (time.Time, bool) {
	f, ok := v.(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}

var errNotJWT = errors.New("the ID token is not a JWT")

// VerifyIDToken verifies an ID token from this issuer for this app (the
// signature against the published keys, issuer, audience, expiry and nonce)
// and returns its claims. [SignIn.Finish] already does this; use it for an ID
// token obtained another way.
func (s *SignIn) VerifyIDToken(ctx context.Context, idToken, nonce string) (*Identity, error) {
	conf, err := s.Configuration(ctx)
	if err != nil {
		return nil, err
	}
	claims, err := s.verifyIDToken(ctx, conf, idToken, nonce)
	if err != nil {
		return nil, err
	}
	return identityFrom(claims), nil
}
