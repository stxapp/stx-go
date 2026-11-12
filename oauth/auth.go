package oauth

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"time"
)

// Header names the OAuth token travels in.
const (
	// WebSocketTokenHeader carries the access token on the WebSocket
	// handshake.
	WebSocketTokenHeader = "x-stx-oauth-token"
)

// detachedTimeout bounds a mint or refresh that runs apart from the caller's
// context, so a canceled caller never abandons a rotating refresh halfway.
const detachedTimeout = 60 * time.Second

func wsHeader(token string) http.Header {
	h := http.Header{}
	h.Set(WebSocketTokenHeader, token)
	return h
}

func bearer(token string) http.Header {
	return http.Header{"Authorization": {"Bearer " + token}}
}

// StaticToken is an access token used as is, with no refresh: it implements
// stx.Auth for a one-off script. Pass it to stx.WithAuth.
type StaticToken string

// Kind returns "oauth".
func (StaticToken) Kind() string { return "oauth" }

// AuthHeaders returns the Bearer header.
func (t StaticToken) AuthHeaders(context.Context, string, string) (http.Header, error) {
	return bearer(string(t)), nil
}

// WebSocketHeaders returns the handshake header.
func (t StaticToken) WebSocketHeaders(context.Context, bool) (http.Header, error) {
	return wsHeader(string(t)), nil
}

// call is one in-flight mint or refresh that concurrent callers share.
type call struct {
	done chan struct{}
	tok  *Token
	err  error
}

func (c *call) wait(ctx context.Context) (*Token, error) {
	select {
	case <-c.done:
		return c.tok, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// AppToken is a cached client credentials token that mints itself again
// shortly before it expires. Concurrent callers share one mint. It
// implements stx.Auth (with reauthentication on a 401 and WebSocket
// handshakes), so pass it to stx.WithAuth to call the API as the app. Get
// one from [Client.AppToken].
type AppToken struct {
	c      *Client
	scopes []string
	skew   time.Duration

	mu       sync.Mutex
	cached   *Token
	inflight *call
}

// AppToken returns the cached app token for scopes. The same scopes return
// the same *AppToken, so every client built from this one shares a token.
// No scopes asks for every app scope the client holds.
func (c *Client) AppToken(scopes ...string) *AppToken {
	key := FormatScopes(scopes)
	c.mu.Lock()
	defer c.mu.Unlock()
	if a, ok := c.appTokens[key]; ok {
		return a
	}
	a := &AppToken{c: c, scopes: append([]string(nil), scopes...), skew: time.Minute}
	c.appTokens[key] = a
	return a
}

// Token returns a valid app token, minting one when there is none or it
// expires within a minute. A token without an expiry is kept until
// [AppToken.Invalidate].
func (a *AppToken) Token(ctx context.Context) (*Token, error) {
	a.mu.Lock()
	if t := a.cached; t != nil && !t.expiresWithin(a.c.now(), a.skew) {
		a.mu.Unlock()
		return t, nil
	}
	cl := a.inflight
	if cl == nil {
		cl = &call{done: make(chan struct{})}
		a.inflight = cl
		go a.mint(context.WithoutCancel(ctx), cl)
	}
	a.mu.Unlock()
	return cl.wait(ctx)
}

func (a *AppToken) mint(ctx context.Context, cl *call) {
	ctx, cancel := context.WithTimeout(ctx, detachedTimeout)
	defer cancel()
	cl.tok, cl.err = a.c.ClientCredentials(ctx, a.scopes...)
	a.mu.Lock()
	if cl.err == nil {
		a.cached = cl.tok
	}
	a.inflight = nil
	a.mu.Unlock()
	close(cl.done)
}

// Invalidate drops the cached token, so the next call mints one.
func (a *AppToken) Invalidate() {
	a.mu.Lock()
	a.cached = nil
	a.mu.Unlock()
}

// Fresh drops the cached token and mints one now.
func (a *AppToken) Fresh(ctx context.Context) (*Token, error) {
	a.Invalidate()
	return a.Token(ctx)
}

// GrantedScope returns the scope granted to the current token, or "" before
// the first mint.
func (a *AppToken) GrantedScope() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cached == nil {
		return ""
	}
	return a.cached.Scope
}

// Kind returns "oauth_app".
func (a *AppToken) Kind() string { return "oauth_app" }

// AuthHeaders returns the Bearer header for one request.
func (a *AppToken) AuthHeaders(ctx context.Context, _, _ string) (http.Header, error) {
	t, err := a.Token(ctx)
	if err != nil {
		return nil, err
	}
	return bearer(t.AccessToken), nil
}

// Reauthenticate mints a new token after a 401.
func (a *AppToken) Reauthenticate(ctx context.Context) (bool, error) {
	if _, err := a.Fresh(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// WebSocketHeaders returns the handshake header. A reconnect mints a new
// token rather than reuse the one from the connection that dropped.
func (a *AppToken) WebSocketHeaders(ctx context.Context, reconnect bool) (http.Header, error) {
	var t *Token
	var err error
	if reconnect {
		t, err = a.Fresh(ctx)
	} else {
		t, err = a.Token(ctx)
	}
	if err != nil {
		return nil, err
	}
	return wsHeader(t.AccessToken), nil
}

// SessionOptions tune a member [Session].
type SessionOptions struct {
	// Skew is how long before expiry the access token is refreshed. Default
	// 30 seconds.
	Skew time.Duration
	// OnRefresh runs after every refresh, once the new tokens are stored.
	OnRefresh func(ctx context.Context, t *Token)
	// OnGrantRevoked runs once when the grant is found revoked, after the
	// stored tokens are deleted.
	OnGrantRevoked func(ctx context.Context, err error)
	// OnTermsRequired runs after a refresh that says the member has new STX
	// terms to accept (the tokens are stored and work). Send the member to
	// termsURL when they are next in your app.
	OnTermsRequired func(ctx context.Context, termsURL string)
}

// Session acts for one connected member, with tokens kept in a
// [TokenStore]. It implements stx.Auth: every request carries the member's
// access token, refreshed before expiry and once on a 401, with the rotated
// refresh token stored each time. A revoked grant deletes the stored tokens
// and returns an error matching [ErrGrantRevoked].
//
// Refreshes are shared per store and member across every Session of one
// [Client], so concurrent requests refresh once. Across processes, give the
// store a [Locker].
type Session struct {
	c     *Client
	store TokenStore
	key   string
	opts  SessionOptions

	mu       sync.Mutex
	lastSent string
}

// Session returns the member session for memberKey in store. opts may be nil.
func (c *Client) Session(store TokenStore, memberKey string, opts *SessionOptions) *Session {
	s := &Session{c: c, store: store, key: memberKey}
	if opts != nil {
		s.opts = *opts
	}
	if s.opts.Skew <= 0 {
		s.opts.Skew = 30 * time.Second
	}
	return s
}

// MemberKey returns the key the member's tokens are stored under.
func (s *Session) MemberKey() string { return s.key }

func (s *Session) load(ctx context.Context) (*Token, error) {
	t, err := s.store.Get(ctx, s.key)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, ErrNotConnected
	}
	return t, nil
}

// Token returns the member's tokens, refreshed first when they expire within
// the skew. If a refresh fails for a passing reason while the access token
// still works, the current tokens are returned.
func (s *Session) Token(ctx context.Context) (*Token, error) {
	t, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	if t.RefreshToken == "" || !t.expiresWithin(s.c.now(), s.opts.Skew) {
		return t, nil
	}
	fresh, err := s.refresh(ctx, t.RefreshToken)
	if err == nil {
		return fresh, nil
	}
	if terminal(err) || t.expiresWithin(s.c.now(), 0) {
		return nil, err
	}
	return t, nil
}

// Scopes returns the granted scopes of the stored tokens.
func (s *Session) Scopes(ctx context.Context) ([]string, error) {
	t, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	return t.Scopes(), nil
}

// Kind returns "oauth".
func (s *Session) Kind() string { return "oauth" }

// AuthHeaders returns the Bearer header for one request.
func (s *Session) AuthHeaders(ctx context.Context, _, _ string) (http.Header, error) {
	t, err := s.Token(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.lastSent = t.AccessToken
	s.mu.Unlock()
	return bearer(t.AccessToken), nil
}

// Reauthenticate handles a 401: when another request has already replaced
// the token it retries with that one, otherwise it refreshes.
func (s *Session) Reauthenticate(ctx context.Context) (bool, error) {
	t, err := s.load(ctx)
	if errors.Is(err, ErrNotConnected) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	last := s.lastSent
	s.mu.Unlock()
	if last != "" && t.AccessToken != last {
		return true, nil
	}
	if t.RefreshToken == "" {
		return false, nil
	}
	if _, err := s.refresh(ctx, t.RefreshToken); err != nil {
		if terminal(err) {
			return false, err
		}
		return false, nil
	}
	return true, nil
}

// WebSocketHeaders returns the handshake header, refreshing first when the
// token is near expiry, so a reconnect after expiry gets a fresh one.
func (s *Session) WebSocketHeaders(ctx context.Context, _ bool) (http.Header, error) {
	t, err := s.Token(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.lastSent = t.AccessToken
	s.mu.Unlock()
	return wsHeader(t.AccessToken), nil
}

// terminal reports refresh errors that must reach the caller.
func terminal(err error) bool {
	return errors.Is(err, ErrGrantRevoked) || errors.Is(err, ErrAccountPending) || errors.Is(err, ErrNotConnected)
}

type refreshKey struct {
	store any
	key   string
}

// refresh rotates the member's tokens once, however many callers ask.
func (s *Session) refresh(ctx context.Context, used string) (*Token, error) {
	var storeID any = s
	if reflect.TypeOf(s.store).Comparable() {
		storeID = s.store
	}
	k := refreshKey{store: storeID, key: s.key}
	s.c.mu.Lock()
	cl, ok := s.c.refreshes[k]
	if !ok {
		cl = &call{done: make(chan struct{})}
		s.c.refreshes[k] = cl
		go func() {
			dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), detachedTimeout)
			defer cancel()
			if l, ok := s.store.(Locker); ok {
				cl.err = l.WithLock(dctx, s.key, func(ctx context.Context) error {
					var err error
					cl.tok, err = s.runRefresh(ctx, used)
					return err
				})
			} else {
				cl.tok, cl.err = s.runRefresh(dctx, used)
			}
			s.c.mu.Lock()
			delete(s.c.refreshes, k)
			s.c.mu.Unlock()
			close(cl.done)
		}()
	}
	s.c.mu.Unlock()
	return cl.wait(ctx)
}

func (s *Session) runRefresh(ctx context.Context, used string) (*Token, error) {
	current, err := s.store.Get(ctx, s.key)
	if err != nil {
		return nil, err
	}
	// Another process (holding the lock first) already rotated: use its pair.
	if current != nil && current.RefreshToken != "" && current.RefreshToken != used {
		return current, nil
	}
	t, err := s.c.Refresh(ctx, used)
	if err != nil {
		if errors.Is(err, ErrGrantRevoked) {
			_ = s.store.Delete(ctx, s.key)
			if s.opts.OnGrantRevoked != nil {
				s.opts.OnGrantRevoked(ctx, err)
			}
		}
		return nil, err
	}
	if t.RefreshToken == "" {
		t.RefreshToken = used
	}
	if t.Scope == "" && current != nil {
		t.Scope = current.Scope
	}
	if err := s.store.Set(ctx, s.key, t); err != nil {
		return nil, err
	}
	if s.opts.OnRefresh != nil {
		s.opts.OnRefresh(ctx, t)
	}
	if t.TermsRequired && t.TermsURL != "" && s.opts.OnTermsRequired != nil {
		s.opts.OnTermsRequired(ctx, t.TermsURL)
	}
	return t, nil
}
