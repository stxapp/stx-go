package oauth

import "strings"

// Member scopes, granted to a member token through consent, in the
// exchange's canonical order. See https://docs.stxapp.io/oauth/scopes/.
// No scope moves money: there is no scope that starts, approves or cancels a
// transfer.
const (
	// ScopeProfileRead: who the member is (GET /api/v1/me).
	ScopeProfileRead = "profile.read"
	// ScopeBalanceRead: the cash balance and the balances channel.
	ScopeBalanceRead = "balance.read"
	// ScopePortfolioRead: positions, settlements and the ledgers, and their channels.
	ScopePortfolioRead = "portfolio.read"
	// ScopeOrdersRead: order and fill history, and the orders and fills channels.
	ScopeOrdersRead = "orders.read"
	// ScopeTransfersRead: deposit and withdrawal history (read only).
	ScopeTransfersRead = "transfers.read"
	// ScopeOrdersWrite: place and cancel orders, and arm cancel on disconnect.
	ScopeOrdersWrite = "orders.write"
)

// App scopes, carried by a client credentials app token (no member, no
// consent).
const (
	// ScopeMarketData: the market catalog and the market data channels.
	ScopeMarketData = "market_data"
	// ScopeEvents: the event catalog, an event's players and the events channel.
	ScopeEvents = "events"
	// ScopeMarketsWrite: create markets (POST /api/v1/markets). Granted by STX only.
	ScopeMarketsWrite = "markets.write"
)

// ScopeOpenID asks for an ID token, for sign in with STX.
const ScopeOpenID = "openid"

// MemberScopes returns every member scope, in canonical order.
func MemberScopes() []string {
	return []string{ScopeProfileRead, ScopeBalanceRead, ScopePortfolioRead, ScopeOrdersRead, ScopeTransfersRead, ScopeOrdersWrite}
}

// AppScopes returns every app scope, in canonical order.
func AppScopes() []string {
	return []string{ScopeMarketData, ScopeEvents, ScopeMarketsWrite}
}

// legacyScopes are older names the exchange still accepts and maps forward.
var legacyScopes = map[string]string{
	"identity":  ScopeProfileRead,
	"balance":   ScopeBalanceRead,
	"portfolio": ScopePortfolioRead,
	"history":   ScopeOrdersRead,
	"transfers": ScopeTransfersRead,
	"trade":     ScopeOrdersWrite,
}

// retiredScopes no longer exist; a member accepts the terms on an STX page.
var retiredScopes = map[string]bool{"terms.write": true, "onboarding": true}

// CanonicalScope returns the current name for scope: older names map
// forward, anything else is returned unchanged.
func CanonicalScope(scope string) string {
	if s, ok := legacyScopes[scope]; ok {
		return s
	}
	return scope
}

// ParseScopes splits a space-delimited scope string into canonical,
// de-duplicated scopes, leaving out retired ones.
func ParseScopes(scope string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.Fields(scope) {
		if retiredScopes[raw] {
			continue
		}
		s := CanonicalScope(raw)
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// FormatScopes joins scopes into the space-delimited wire form.
func FormatScopes(scopes []string) string { return strings.Join(scopes, " ") }

// IsMemberScope reports whether scope is a member scope.
func IsMemberScope(scope string) bool { return contains(MemberScopes(), scope) }

// IsAppScope reports whether scope is an app scope.
func IsAppScope(scope string) bool { return contains(AppScopes(), scope) }

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
