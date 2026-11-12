package oauth

import stx "github.com/stxapp/stx-go"

// MemberClient returns an *stx.Client acting for one connected member: every
// REST call carries the member's access token, refreshed before expiry and
// once on a 401, and WebSocket handshakes send it too. opts may add client
// options such as stx.WithLogger; the host and auth come from c. sessionOpts
// may be nil.
func (c *Client) MemberClient(store TokenStore, memberKey string, sessionOpts *SessionOptions, opts ...stx.Option) (*stx.Client, error) {
	return c.stxClient(c.Session(store, memberKey, sessionOpts), opts)
}

// AppClient returns an *stx.Client acting as the app with a cached client
// credentials token for scopes: the market and event catalogs, the market
// data channels and, with markets.write, creating markets. No scopes asks
// for every app scope the client holds.
func (c *Client) AppClient(scopes []string, opts ...stx.Option) (*stx.Client, error) {
	return c.stxClient(c.AppToken(scopes...), opts)
}

func (c *Client) stxClient(auth stx.Auth, opts []stx.Option) (*stx.Client, error) {
	all := append([]stx.Option{}, opts...)
	all = append(all, stx.WithHost(c.baseURL), stx.WithAuth(auth))
	if c.cfg.HTTPClient != nil {
		all = append([]stx.Option{stx.WithHTTPClient(c.cfg.HTTPClient)}, all...)
	}
	return stx.New(all...)
}

// Compile-time checks that the token sources plug into stx.Client.
var (
	_ stx.Auth            = (*AppToken)(nil)
	_ stx.Reauthenticator = (*AppToken)(nil)
	_ stx.WebSocketAuth   = (*AppToken)(nil)
	_ stx.Auth            = (*Session)(nil)
	_ stx.Reauthenticator = (*Session)(nil)
	_ stx.WebSocketAuth   = (*Session)(nil)
	_ stx.Auth            = StaticToken("")
	_ stx.WebSocketAuth   = StaticToken("")
	_ TokenSource         = (*AppToken)(nil)
	_ TokenSource         = (*Session)(nil)
)
