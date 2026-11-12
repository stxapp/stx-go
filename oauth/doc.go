// Package oauth lets an app call STX for its members and as itself.
//
// An app acting for a member sends them through the authorization code flow
// with PKCE ([Client.Begin], [Client.Complete]), keeps their tokens in a
// [TokenStore], and calls the API through [Client.MemberClient], which
// refreshes the rotating tokens before expiry and once on a 401. An app
// acting as itself uses a client credentials token ([Client.AppToken],
// [Client.AppClient]) for the market and event catalogs, the market data
// channels and creating markets. [SignIn] is sign in with STX: OpenID
// Connect on the same flow, with the ID token verified.
//
// The token sources implement stx.Auth, so they also plug into stx.New with
// stx.WithAuth. Errors from OAuth endpoints are *[Error] values that match
// the sentinels in this package; an exchange without OAuth answers with an
// error matching stx.ErrNotAvailable.
//
// Flows, scopes, tokens and errors are described at https://docs.stxapp.io/oauth/.
package oauth
