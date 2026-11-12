// An app with its own accounts links each user's STX account once and acts
// for them (https://docs.stxapp.io/oauth/connect-your-stx-account/):
//
//   - /connect sends the signed-in user to STX to sign in and approve your
//     app (authorization code with PKCE); /callback stores their tokens under
//     your own user id.
//   - /api/link reports the link, refreshing the tokens when they are near
//     expiry (the refresh token rotates and is stored each time).
//   - /api/balance, /api/positions and /api/orders read the member's account
//     through a client that refreshes before expiry and on a 401.
//   - /api/markets/types reads the market types as the app itself, on an app
//     token.
//   - /api/unlink revokes the grant at STX and forgets the tokens.
//
// It is a confidential OAuth client: the secret and every token stay on this
// server. Tokens are kept per user in a file, so a restart keeps every link;
// a real deployment keeps them encrypted in its database. To stay short it
// takes the user from ?user=<id>; a real deployment takes it from its own
// login session.
//
//	STX_HOST=demo.stxapp.io STX_OAUTH_CLIENT_ID=... STX_OAUTH_CLIENT_SECRET=... \
//	  STX_OAUTH_REDIRECT_URI=http://127.0.0.1:8787/callback go run ./examples/connect-account
//
// Then open http://127.0.0.1:8787/connect?user=alice in a browser.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	stx "github.com/stxapp/stx-go"
	"github.com/stxapp/stx-go/oauth"
)

// linkScopes is the access the app asks each member for. Add
// oauth.ScopeOrdersWrite to trade for them; a write scope needs your
// server's addresses on the app's IP allow-list.
var linkScopes = []string{oauth.ScopeProfileRead, oauth.ScopeBalanceRead, oauth.ScopePortfolioRead, oauth.ScopeOrdersRead}

type backend struct {
	app     *oauth.Client
	tokens  oauth.TokenStore
	pending oauth.PendingStore
	asApp   *stx.Client
}

func main() {
	app, err := oauth.New(oauth.ConfigFromEnv())
	if err != nil {
		log.Fatal(err)
	}
	b, err := newBackend(app, oauth.NewFileTokenStore("stx-tokens.json"))
	if err != nil {
		log.Fatal(err)
	}
	log.Println("listening on http://127.0.0.1:8787")
	log.Fatal(http.ListenAndServe("127.0.0.1:8787", b.routes()))
}

func newBackend(app *oauth.Client, tokens oauth.TokenStore) (*backend, error) {
	asApp, err := app.AppClient([]string{oauth.ScopeMarketData, oauth.ScopeEvents})
	if err != nil {
		return nil, err
	}
	return &backend{app: app, tokens: tokens, pending: &oauth.MemoryPendingStore{}, asApp: asApp}, nil
}

func (b *backend) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /connect", b.connect)
	mux.HandleFunc("GET /callback", b.callback)
	mux.HandleFunc("GET /api/link", b.link)
	mux.HandleFunc("GET /api/balance", b.balance)
	mux.HandleFunc("GET /api/positions", b.positions)
	mux.HandleFunc("GET /api/orders", b.orders)
	mux.HandleFunc("GET /api/markets/types", b.appTypes)
	mux.HandleFunc("POST /api/unlink", b.unlink)
	return mux
}

func (b *backend) connect(w http.ResponseWriter, r *http.Request) {
	user := r.URL.Query().Get("user")
	if user == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_required"})
		return
	}
	auth, err := b.app.Begin(r.Context(), b.pending, oauth.BeginParams{
		AuthParams: oauth.AuthParams{Scopes: linkScopes},
		Data:       map[string]string{"user": user},
	})
	if err != nil {
		writeError(w, err)
		return
	}
	http.Redirect(w, r, auth.URL, http.StatusFound)
}

func (b *backend) callback(w http.ResponseWriter, r *http.Request) {
	done, err := b.app.Complete(r.Context(), b.pending, r.URL.Query(), oauth.RedeemOptions{
		Store:         b.tokens,
		MemberKeyFunc: func(data map[string]string) string { return data["user"] },
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"linked": done.MemberKey, "scopes": done.Scopes})
}

func (b *backend) link(w http.ResponseWriter, r *http.Request) {
	tok, err := b.app.Session(b.tokens, r.URL.Query().Get("user"), nil).Token(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"linked": true, "scopes": tok.Scopes(), "expires": tok.Expiry, "terms_required": tok.TermsRequired})
}

// memberClient calls the API as one linked member.
func (b *backend) memberClient(user string) (*stx.Client, error) {
	return b.app.MemberClient(b.tokens, user, &oauth.SessionOptions{
		OnGrantRevoked: func(context.Context, error) { log.Printf("%s revoked access; link again", user) },
	})
}

func (b *backend) balance(w http.ResponseWriter, r *http.Request) {
	member, err := b.memberClient(r.URL.Query().Get("user"))
	if err != nil {
		writeError(w, err)
		return
	}
	balance, err := member.Account.Balance(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, balance)
}

func (b *backend) positions(w http.ResponseWriter, r *http.Request) {
	member, err := b.memberClient(r.URL.Query().Get("user"))
	if err != nil {
		writeError(w, err)
		return
	}
	positions, err := member.Account.Positions(r.Context(), nil)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"positions": positions})
}

func (b *backend) orders(w http.ResponseWriter, r *http.Request) {
	member, err := b.memberClient(r.URL.Query().Get("user"))
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := member.Orders.List(r.Context(), &stx.OrdersQuery{Limit: 20})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": page.Items})
}

func (b *backend) appTypes(w http.ResponseWriter, r *http.Request) {
	types, err := b.asApp.Markets.Types(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, types)
}

func (b *backend) unlink(w http.ResponseWriter, r *http.Request) {
	revoked, err := b.app.Unlink(r.Context(), b.tokens, r.URL.Query().Get("user"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": revoked})
}

// writeError maps SDK errors to what the browser needs to decide: link
// again, finish verifying, ask for more scope, accept terms, or show the
// reason.
func writeError(w http.ResponseWriter, err error) {
	var apiErr *stx.APIError
	var oauthErr *oauth.Error
	switch {
	case errors.Is(err, oauth.ErrNotConnected), errors.Is(err, oauth.ErrGrantRevoked):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "link_required"})
	case errors.Is(err, oauth.ErrAccountPending):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "account_pending"})
	case errors.Is(err, oauth.ErrAccessDenied):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access_denied"})
	case errors.Is(err, stx.ErrTermsRequired) && errors.As(err, &apiErr):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "terms_required", "terms_url": oauth.TermsURLWithReturn(apiErr.TermsURL, "http://127.0.0.1:8787/")})
	case errors.Is(err, stx.ErrInsufficientScope) && errors.As(err, &apiErr):
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "insufficient_scope", "required_scopes": apiErr.RequiredScopes})
	case errors.As(err, &oauthErr):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": oauthErr.Code, "error_description": oauthErr.Description})
	case errors.As(err, &apiErr):
		writeJSON(w, apiErr.StatusCode, map[string]string{"error": apiErr.Message})
	default:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
