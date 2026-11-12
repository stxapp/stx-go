package stx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"
)

// Feature ids reported by [Client.Capabilities] and named by
// [NotAvailableError].
const (
	FeatureOAuth         = "oauth"
	FeatureOAuthMember   = "oauth.member"
	FeatureOAuthApp      = "oauth.app"
	FeatureConnect       = "connect"
	FeatureTermsSTXPage  = "terms.stx_page"
	FeatureMarketsCreate = "markets.create"
	FeatureMarketTypes   = "market_types"
	FeatureEventPlayers  = "event_players"
	FeatureLeaderboard   = "leaderboard"
	FeatureCombos        = "combos"
)

// Features returns every feature id [Client.Capabilities] reports.
func Features() []string {
	return []string{FeatureOAuth, FeatureOAuthMember, FeatureOAuthApp, FeatureConnect, FeatureTermsSTXPage,
		FeatureMarketsCreate, FeatureMarketTypes, FeatureEventPlayers, FeatureLeaderboard, FeatureCombos}
}

// memberScopes: any one of them in scopes_supported means members can
// connect apps.
var memberScopes = []string{"profile.read", "balance.read", "portfolio.read", "orders.read", "transfers.read", "orders.write"}

// nobody is an id no event or market has.
const nobody = "00000000-0000-0000-0000-000000000000"

// Capabilities is what an exchange showed it has.
type Capabilities struct {
	// Host is the exchange host, such as "demo.stxapp.io".
	Host string
	// APIVersion is GET /api_version, or "" when it did not answer.
	APIVersion string
	// CheckedAt is when the checks ran.
	CheckedAt time.Time
	// ScopesSupported are the scopes the exchange's OAuth discovery document
	// lists; empty without OAuth.
	ScopesSupported []string
	// Features maps every feature id to true (the exchange has it), false
	// (it does not) or nil (not known: the check needs credentials this
	// client lacks, or the exchange did not answer).
	Features map[string]*bool
}

// Has reports whether the exchange is known to have feature.
func (c *Capabilities) Has(feature string) bool {
	v := c.Features[feature]
	return v != nil && *v
}

// Known reports whether the check for feature had an answer.
func (c *Capabilities) Known(feature string) bool { return c.Features[feature] != nil }

// Require returns a *NotAvailableError when the exchange is known to lack
// feature, and nil otherwise. A feature that could not be checked does not
// fail here: the call that needs it will say.
func (c *Capabilities) Require(feature string) error {
	if v := c.Features[feature]; v != nil && !*v {
		return &NotAvailableError{Feature: feature, Host: c.Host}
	}
	return nil
}

// probeCombos joins a combos topic nobody owns and reports whether the
// exchange has combos (nil when unknown). The WebSocket code sets it; until
// then combos are reported unknown.
var probeCombos func(ctx context.Context, c *Client) *bool

type capsCache struct {
	mu   sync.Mutex
	caps *Capabilities
}

// Capabilities reports which features this exchange has, from what it
// publishes: its OAuth discovery document, /api_version, GETs (signed when
// the client has credentials) of the routes not every exchange has yet, and
// a join of a combos topic nobody owns. Nothing is created or changed. The
// answer is cached per client; pass refresh to check again.
//
// A missing feature never fails here; the call that needs it returns an
// error matching [ErrNotAvailable].
func (c *Client) Capabilities(ctx context.Context, refresh bool) (*Capabilities, error) {
	cache := &c.caps
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.caps != nil && !refresh {
		return cache.caps, nil
	}
	caps := c.discoverCapabilities(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cache.caps = caps
	return caps, nil
}

type rawAnswer struct {
	status int
	body   []byte
}

func (a *rawAnswer) object() map[string]any {
	if a == nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(a.body, &m) != nil {
		return nil
	}
	return m
}

// missingRoute is a 404 from the router: {"error": "not_found"} alone.
func (a *rawAnswer) missingRoute() bool {
	if a == nil || a.status != 404 {
		return false
	}
	m := a.object()
	return len(m) == 1 && m["error"] == "not_found"
}

func (c *Client) rawGet(ctx context.Context, path string, signed bool) *rawAnswer {
	if signed && c.auth == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, min(c.timeout, 15*time.Second))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil
	}
	if signed {
		h, err := c.auth.AuthHeaders(ctx, http.MethodGet, path)
		if err != nil {
			return nil
		}
		for k, v := range h {
			req.Header[k] = v
		}
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil
	}
	return &rawAnswer{status: res.StatusCode, body: body}
}

func (c *Client) discoverCapabilities(ctx context.Context) *Capabilities {
	var version, discovery, types, players, board *rawAnswer
	var combos *bool
	var wg sync.WaitGroup
	run := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }
	run(func() { version = c.rawGet(ctx, "/api_version", false) })
	run(func() { discovery = c.rawGet(ctx, "/.well-known/oauth-authorization-server", false) })
	run(func() { types = c.rawGet(ctx, "/api/v1/types", true) })
	run(func() { players = c.rawGet(ctx, "/api/v1/events/"+nobody+"/players", true) })
	run(func() { board = c.rawGet(ctx, "/api/v1/leaderboard?limit=1", true) })
	if probeCombos != nil && c.auth != nil {
		run(func() { combos = probeCombos(ctx, c) })
	}
	wg.Wait()

	caps := &Capabilities{Host: c.host, CheckedAt: time.Now(), Features: map[string]*bool{}}
	if v, ok := version.object()["api_version"].(string); ok {
		caps.APIVersion = v
	}
	meta, known := oauthMetadataFrom(discovery)
	scopes, grants := stringList(meta["scopes_supported"]), stringList(meta["grant_types_supported"])
	caps.ScopesSupported = scopes
	fromMeta := func(decide func() bool) *bool {
		if !known {
			return nil
		}
		return ptr(meta != nil && decide())
	}
	has := func(list []string, s string) bool { return slices.Contains(list, s) }
	member := fromMeta(func() bool {
		return has(grants, "authorization_code") && slices.ContainsFunc(scopes, func(s string) bool { return has(memberScopes, s) })
	})
	app := fromMeta(func() bool { return has(grants, "client_credentials") })
	f := caps.Features
	f[FeatureOAuth] = fromMeta(func() bool { return true })
	f[FeatureOAuthMember] = member
	f[FeatureOAuthApp] = app
	f[FeatureConnect] = fromMeta(func() bool { return has(scopes, "openid") })
	f[FeatureTermsSTXPage] = fromMeta(func() bool { return *member && !has(scopes, "terms.write") })
	f[FeatureMarketsCreate] = fromMeta(func() bool { return *app && has(scopes, "markets.write") })
	switch {
	case types == nil:
	case types.status >= 200 && types.status < 300:
		f[FeatureMarketTypes] = ptr(true)
	case types.missingRoute():
		f[FeatureMarketTypes] = ptr(false)
	}
	switch {
	case players == nil:
	case players.missingRoute():
		f[FeatureEventPlayers] = ptr(false)
	case players.status == 404 || players.status < 300:
		// A 404 about the event, not the route, means the route is there.
		f[FeatureEventPlayers] = ptr(true)
	}
	switch {
	case board == nil:
	case board.status >= 200 && board.status < 300:
		f[FeatureLeaderboard] = ptr(true)
	case board.status == 404:
		// Switched off answers 404 too.
		f[FeatureLeaderboard] = ptr(false)
	}
	f[FeatureCombos] = combos
	for _, id := range Features() {
		if _, ok := f[id]; !ok {
			f[id] = nil
		}
	}
	return caps
}

// oauthMetadataFrom returns the OAuth discovery document and whether the
// answer was conclusive: a nil document with known set means no OAuth (a 404,
// or a 403 from the edge that is not JSON).
func oauthMetadataFrom(a *rawAnswer) (map[string]any, bool) {
	if a == nil {
		return nil, false
	}
	body := a.object()
	switch {
	case a.status == 404:
		return nil, true
	case a.status == 403 && !json.Valid(a.body):
		return nil, true
	case a.status != 200 || body == nil:
		return nil, false
	}
	for _, k := range []string{"issuer", "authorization_endpoint", "token_endpoint"} {
		if _, ok := body[k].(string); !ok {
			return nil, false
		}
	}
	return body, true
}

func stringList(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
