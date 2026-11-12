package oauth

import (
	"context"
	"encoding/base64"
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

// Endpoint holds the OAuth endpoints, as paths on the base URL or absolute
// URLs. An empty field takes the default path.
type Endpoint struct {
	AuthURL       string
	TokenURL      string
	RevokeURL     string
	IntrospectURL string
}

// Config describes one registered app. A process serving several apps makes
// one [Client] per app.
type Config struct {
	// BaseURL is the exchange's host or URL, such as "demo.stxapp.io". Empty
	// falls back to the STX_HOST environment variable.
	BaseURL string
	// ClientID is your app's client id.
	ClientID string
	// ClientSecret is the secret of a confidential client, sent as HTTP Basic
	// credentials. Leave it empty for a public client: it identifies with
	// client_id in the form and PKCE is its proof. A public client cannot
	// mint app tokens.
	ClientSecret string
	// RedirectURL is the redirect URI registered with STX (matched exactly).
	// The authorization code flow needs it.
	RedirectURL string
	// Scopes are the scopes asked for when a call names none.
	Scopes []string
	// Endpoint overrides the endpoint paths.
	Endpoint Endpoint
	// Metadata is the exchange's discovery document, when you have it
	// ([Discover] sets it). With it, an invalid_scope answer for a scope the
	// exchange does not list becomes a *stx.NotAvailableError.
	Metadata *AuthorizationServerMetadata
	// HTTPClient sends the OAuth requests. Nil uses a client with a 30
	// second timeout.
	HTTPClient *http.Client
}

// Client runs the OAuth flows for one app: the authorization code flow with
// PKCE for a member, refresh with rotation, revocation, introspection, and
// client credentials app tokens. It is safe for concurrent use.
//
// See https://docs.stxapp.io/oauth/ for the flow and the scopes.
type Client struct {
	cfg      Config
	baseURL  string
	endpoint Endpoint
	now      func() time.Time

	mu        sync.Mutex
	appTokens map[string]*AppToken
	refreshes map[refreshKey]*call
}

// New returns a client for cfg. It makes no request.
func New(cfg Config) (*Client, error) {
	base, err := normalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("%w: oauth: ClientID is required", stx.ErrConfig)
	}
	ep := cfg.Endpoint
	ep.AuthURL = orDefault(ep.AuthURL, DefaultAuthorizePath)
	ep.TokenURL = orDefault(ep.TokenURL, DefaultTokenPath)
	ep.RevokeURL = orDefault(ep.RevokeURL, DefaultRevokePath)
	ep.IntrospectURL = orDefault(ep.IntrospectURL, DefaultIntrospectPath)
	return &Client{
		cfg:       cfg,
		baseURL:   base,
		endpoint:  ep,
		now:       time.Now,
		appTokens: map[string]*AppToken{},
		refreshes: map[refreshKey]*call{},
	}, nil
}

// Discover fetches the exchange's discovery document and returns a client
// that uses the endpoints it advertises. On an exchange without OAuth it
// returns an error matching stx.ErrNotAvailable, so an app can tell "not
// here yet" from a misconfiguration at start-up.
func Discover(ctx context.Context, cfg Config) (*Client, error) {
	m, err := DiscoverMetadata(ctx, cfg.BaseURL, cfg.HTTPClient)
	if err != nil {
		return nil, err
	}
	cfg.Metadata = m
	if cfg.BaseURL == "" {
		cfg.BaseURL = m.Issuer
	}
	cfg.Endpoint = Endpoint{
		AuthURL:       m.AuthorizationEndpoint,
		TokenURL:      m.TokenEndpoint,
		RevokeURL:     m.RevocationEndpoint,
		IntrospectURL: m.IntrospectionEndpoint,
	}
	return New(cfg)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// BaseURL returns the exchange's base URL, such as "https://demo.stxapp.io".
func (c *Client) BaseURL() string { return c.baseURL }

// Host returns the exchange host, such as "demo.stxapp.io".
func (c *Client) Host() string { return hostOf(c.baseURL) }

// ClientID returns the app's client id.
func (c *Client) ClientID() string { return c.cfg.ClientID }

// IsPublic reports whether the client has no secret.
func (c *Client) IsPublic() bool { return c.cfg.ClientSecret == "" }

// Metadata returns the discovery document the client was built from, or nil.
func (c *Client) Metadata() *AuthorizationServerMetadata { return c.cfg.Metadata }

// AuthParams are the options of one authorize redirect.
type AuthParams struct {
	// Scopes to ask for; empty uses Config.Scopes.
	Scopes []string
	// State overrides the generated state.
	State string
	// Prompt is "none" for a silent reconnect, or "login" or "consent".
	Prompt string
	// RedirectURL overrides Config.RedirectURL.
	RedirectURL string
	// Connection goes straight to a sign-in provider instead of the STX
	// login page: "google", "apple" or "x".
	Connection string
	// LoginHint is the member's email, if you know it, so the provider can
	// preselect the account.
	LoginHint string
	// Extra holds further authorize parameters, sent as given.
	Extra url.Values
}

// Authorization is one prepared authorize redirect.
type Authorization struct {
	// URL is where to send the member's browser.
	URL string
	// State ties the callback to this redirect. Check it on return.
	State string
	// CodeVerifier is the PKCE verifier to keep server-side for the exchange.
	CodeVerifier string
	// CodeChallenge is the S256 challenge carried in URL.
	CodeChallenge string
	// RedirectURL is the redirect URI carried in URL.
	RedirectURL string
}

// AuthorizationURL creates a PKCE pair and a state, and builds the authorize
// URL. Keep State and CodeVerifier server-side for the callback, or use
// [Client.Begin] and [Client.Complete], which do that for you.
func (c *Client) AuthorizationURL(p AuthParams) (*Authorization, error) {
	verifier := GenerateVerifier()
	challenge := S256ChallengeFromVerifier(verifier)
	state := p.State
	if state == "" {
		state = GenerateState()
	}
	extra := url.Values{}
	if p.Connection != "" {
		extra.Set("connection", p.Connection)
	}
	if p.LoginHint != "" {
		extra.Set("login_hint", p.LoginHint)
	}
	for k, v := range p.Extra {
		extra[k] = v
	}
	redirect := orDefault(p.RedirectURL, c.cfg.RedirectURL)
	scope := c.scope(p.Scopes)
	if scope == "" {
		// STX refuses an authorize request without a scope, after the member has
		// already been sent there.
		return nil, fmt.Errorf("%w: oauth: no scopes to ask for; set Config.Scopes or AuthParams.Scopes", stx.ErrConfig)
	}
	u, err := c.authorizeURL(state, challenge, scope, p.Prompt, redirect, extra)
	if err != nil {
		return nil, err
	}
	return &Authorization{URL: u, State: state, CodeVerifier: verifier, CodeChallenge: challenge, RedirectURL: redirect}, nil
}

func (c *Client) authorizeURL(state, challenge, scope, prompt, redirect string, extra url.Values) (string, error) {
	if redirect == "" {
		return "", fmt.Errorf("%w: oauth: RedirectURL is required to build an authorize URL", stx.ErrConfig)
	}
	u, err := url.Parse(joinURL(c.baseURL, c.endpoint.AuthURL))
	if err != nil {
		return "", fmt.Errorf("%w: oauth: bad authorize endpoint: %v", stx.ErrConfig, err)
	}
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.cfg.ClientID},
		"redirect_uri":          {redirect},
		"scope":                 {scope},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	if prompt != "" {
		q.Set("prompt", prompt)
	}
	for k, v := range extra {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (c *Client) scope(scopes []string) string {
	if len(scopes) == 0 {
		scopes = c.cfg.Scopes
	}
	return FormatScopes(scopes)
}

// BeginParams are the options of [Client.Begin].
type BeginParams struct {
	AuthParams
	// Data is carried to the callback in the pending entry.
	Data map[string]string
	// TTL is how long the sign-in may take. Default 10 minutes.
	TTL time.Duration
}

// Begin starts a sign-in: it prepares the redirect, saves the verifier and
// Data in pending under the state, and returns the redirect. Finish with
// [Client.Complete] on the callback.
func (c *Client) Begin(ctx context.Context, pending PendingStore, p BeginParams) (*Authorization, error) {
	auth, err := c.AuthorizationURL(p.AuthParams)
	if err != nil {
		return nil, err
	}
	ttl := p.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	entry := &PendingAuthorization{
		CodeVerifier: auth.CodeVerifier,
		RedirectURL:  auth.RedirectURL,
		Scope:        c.scope(p.Scopes),
		Expires:      c.now().Add(ttl),
		Data:         p.Data,
	}
	if err := pending.Put(ctx, auth.State, entry); err != nil {
		return nil, err
	}
	return auth, nil
}

// Callback is a validated authorize callback: the code and the pending
// sign-in its state named (now used up).
type Callback struct {
	Code    string
	Pending *PendingAuthorization
}

// ReadCallback validates an authorize callback's query and takes its
// pending sign-in. The state is checked and used up before anything else,
// so an error on the callback counts only for a sign-in this app started.
// It makes no request.
//
// Errors are *Error values: invalid_request (no code or state),
// invalid_state (matches [ErrInvalidState]), or the callback's own error
// (matching [ErrAccessDenied], [ErrAccountPending] or [ErrTermsNotAccepted]).
func ReadCallback(ctx context.Context, pending PendingStore, query url.Values) (*Callback, error) {
	return readCallback(ctx, pending, query, DefaultAuthorizePath)
}

// ReadCallback is [ReadCallback] with errors naming this client's
// authorize endpoint.
func (c *Client) ReadCallback(ctx context.Context, pending PendingStore, query url.Values) (*Callback, error) {
	return readCallback(ctx, pending, query, pathOf(c.endpoint.AuthURL))
}

func readCallback(ctx context.Context, pending PendingStore, q url.Values, where string) (*Callback, error) {
	code, state, cbErr := q.Get("code"), q.Get("state"), q.Get("error")
	if cbErr == "" && (code == "" || state == "") {
		return nil, &Error{Code: "invalid_request", Description: "the callback carries no code or no state", StatusCode: 400, Endpoint: where}
	}
	var entry *PendingAuthorization
	if state != "" {
		var err error
		if entry, err = pending.Take(ctx, state); err != nil {
			return nil, err
		}
	}
	if entry == nil {
		return nil, &Error{Code: "invalid_state", Description: "unknown, used or expired state; start the sign-in again", StatusCode: 400, Endpoint: where}
	}
	if cbErr != "" {
		e := callbackError(cbErr, q.Get("error_description"), where)
		e.RequestedScope = entry.Scope
		return nil, e
	}
	return &Callback{Code: code, Pending: entry}, nil
}

// Completed is a finished sign-in.
type Completed struct {
	Token *Token
	// Scopes are the granted scopes, which may be narrower than asked.
	Scopes []string
	// Data is what was passed to Begin.
	Data map[string]string
	// MemberKey is the key the tokens were stored under, when a store was given.
	MemberKey string
}

// RedeemOptions say where [Client.Complete] and [Client.Redeem] keep the
// tokens. Leave Store nil to keep them yourself.
type RedeemOptions struct {
	Store TokenStore
	// MemberKey is the key to store under. MemberKeyFunc, when set, derives
	// it from the Data given to Begin instead.
	MemberKey     string
	MemberKeyFunc func(data map[string]string) string
}

// Complete finishes a sign-in from the callback's query: it reads the
// callback (see [ReadCallback]), exchanges the code with the stored PKCE
// verifier and, with a store, saves the tokens.
func (c *Client) Complete(ctx context.Context, pending PendingStore, query url.Values, opts RedeemOptions) (*Completed, error) {
	cb, err := c.ReadCallback(ctx, pending, query)
	if err != nil {
		var oe *Error
		if errors.As(err, &oe) && oe.Code == "invalid_scope" {
			if na := c.unofferedScope(orDefault(oe.RequestedScope, c.scope(nil)), err); na != nil {
				return nil, na
			}
		}
		return nil, err
	}
	return c.Redeem(ctx, cb, opts)
}

// Redeem exchanges a callback read with [ReadCallback]. Use the pair when
// one callback URL serves several apps and the app is known only from the
// pending entry's Data.
func (c *Client) Redeem(ctx context.Context, cb *Callback, opts RedeemOptions) (*Completed, error) {
	tok, err := c.Exchange(ctx, cb.Code, cb.Pending.CodeVerifier, cb.Pending.RedirectURL)
	if err != nil {
		return nil, err
	}
	done := &Completed{Token: tok, Scopes: tok.Scopes(), Data: cb.Pending.Data}
	if opts.Store != nil {
		key := opts.MemberKey
		if opts.MemberKeyFunc != nil {
			key = opts.MemberKeyFunc(cb.Pending.Data)
		}
		if key == "" {
			return nil, fmt.Errorf("%w: oauth: a member key is required with a store", stx.ErrConfig)
		}
		if err := opts.Store.Set(ctx, key, tok); err != nil {
			return nil, err
		}
		done.MemberKey = key
	}
	return done, nil
}

// Exchange trades an authorization code for tokens, proving PKCE with the
// verifier. redirectURL must repeat the authorize request's; empty uses
// Config.RedirectURL.
func (c *Client) Exchange(ctx context.Context, code, verifier, redirectURL string) (*Token, error) {
	redirect := orDefault(redirectURL, c.cfg.RedirectURL)
	if redirect == "" {
		return nil, fmt.Errorf("%w: oauth: RedirectURL is required to exchange a code", stx.ErrConfig)
	}
	return c.postToken(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirect},
		"code_verifier": {verifier},
	})
}

// Refresh trades a refresh token for a new pair. STX rotates the refresh
// token, so keep the one that comes back. A refused refresh matches
// [ErrGrantRevoked]; a member still being verified matches
// [ErrAccountPending] (keep the tokens). While the member has terms to
// accept the refresh succeeds with TermsRequired set.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	return c.postToken(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}})
}

// ClientCredentials mints an app token: the app acting as itself, with app
// scopes only and no refresh token. No scopes asks for every app scope the
// client holds. For a cached token that renews itself, use [Client.AppToken].
func (c *Client) ClientCredentials(ctx context.Context, scopes ...string) (*Token, error) {
	if c.IsPublic() {
		return nil, fmt.Errorf("%w: oauth: client credentials need a client secret", stx.ErrConfig)
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	if s := FormatScopes(scopes); s != "" {
		form.Set("scope", s)
	}
	return c.postToken(ctx, form)
}

// Revoke revokes a token (RFC 7009). A refresh token ends the member's whole
// grant; an access token revokes only its pair. hint is "access_token",
// "refresh_token" or empty.
func (c *Client) Revoke(ctx context.Context, token, hint string) error {
	form := url.Values{"token": {token}}
	if hint != "" {
		form.Set("token_type_hint", hint)
	}
	_, _, err := c.postForm(ctx, c.endpoint.RevokeURL, form)
	return err
}

// Unlink revokes a member's grant at STX (best effort) and deletes their
// tokens from store. It reports whether STX confirmed the revocation; the
// local tokens are deleted either way.
func (c *Client) Unlink(ctx context.Context, store TokenStore, memberKey string) (bool, error) {
	tok, err := store.Get(ctx, memberKey)
	if err != nil {
		return false, err
	}
	revoked := false
	if tok != nil {
		switch {
		case tok.RefreshToken != "":
			revoked = c.Revoke(ctx, tok.RefreshToken, "refresh_token") == nil
		case tok.AccessToken != "":
			revoked = c.Revoke(ctx, tok.AccessToken, "access_token") == nil
		}
	}
	return revoked, store.Delete(ctx, memberKey)
}

// Introspection is an RFC 7662 answer.
type Introspection struct {
	Active   bool   `json:"active"`
	Scope    string `json:"scope,omitempty"`
	ClientID string `json:"client_id,omitempty"`
	// Raw holds every field of the answer.
	Raw map[string]any `json:"-"`
}

// Introspect reports whether a token is active, and its scope. Confidential
// clients only.
func (c *Client) Introspect(ctx context.Context, token string) (*Introspection, error) {
	_, body, err := c.postForm(ctx, c.endpoint.IntrospectURL, url.Values{"token": {token}})
	if err != nil {
		return nil, err
	}
	var in Introspection
	if json.Unmarshal(body, &in) != nil || json.Unmarshal(body, &in.Raw) != nil {
		return nil, errorf("invalid_response", pathOf(c.endpoint.IntrospectURL), "the response body is not JSON")
	}
	return &in, nil
}

// postForm posts a form to an OAuth endpoint with the client's credentials.
func (c *Client) postForm(ctx context.Context, endpoint string, form url.Values) (*http.Response, []byte, error) {
	header := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
	if c.IsPublic() {
		form.Set("client_id", c.cfg.ClientID)
	} else {
		// RFC 6749 section 2.3.1: both parts are form-encoded before base64.
		raw := url.QueryEscape(c.cfg.ClientID) + ":" + url.QueryEscape(c.cfg.ClientSecret)
		header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(raw)))
	}
	target := joinURL(c.baseURL, endpoint)
	res, body, err := send(ctx, c.cfg.HTTPClient, http.MethodPost, target, strings.NewReader(form.Encode()), header)
	if err != nil {
		return nil, nil, err
	}
	if res.StatusCode/100 == 2 {
		return res, body, nil
	}
	if na := notAvailable("oauth", target, res, body); na != nil {
		return nil, nil, na
	}
	return nil, nil, httpError(res, body, pathOf(target))
}

func (c *Client) postToken(ctx context.Context, form url.Values) (*Token, error) {
	grant, scope := form.Get("grant_type"), form.Get("scope")
	_, body, err := c.postForm(ctx, c.endpoint.TokenURL, form)
	if err != nil {
		var oe *Error
		var na *stx.NotAvailableError
		if errors.As(err, &na) || !errors.As(err, &oe) {
			return nil, err
		}
		if oe.Code == "invalid_scope" {
			if na := c.unofferedScope(scope, err); na != nil {
				return nil, na
			}
		}
		// A member still being verified keeps their grant: checked first, so
		// a refresh is not taken for a dead grant.
		if oe.Code == "invalid_grant" && oe.Reason != "account_pending" && grant == "refresh_token" && oe.StatusCode == http.StatusBadRequest {
			oe.revoked = true
		}
		return nil, oe
	}
	return parseToken(body, pathOf(c.endpoint.TokenURL), c.now())
}

// unofferedScope returns a *stx.NotAvailableError for the first scope in
// requested that the discovery document does not list, or nil.
func (c *Client) unofferedScope(requested string, cause error) error {
	m := c.cfg.Metadata
	if m == nil || m.ScopesSupported == nil || requested == "" {
		return nil
	}
	for _, s := range ParseScopes(requested) {
		if !contains(m.ScopesSupported, s) {
			return &stx.NotAvailableError{Feature: "scope:" + s, Host: c.Host(), Err: cause}
		}
	}
	return nil
}
