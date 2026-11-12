# Changelog

## 0.1.0 (2026-11-12)

First release.

- `stx.New` with functional options, credential profiles in `~/.stx/credentials`, environment variables and the published environments.
- Ed25519 request signing with an API key from PEM text, a PEM file or any `crypto.Signer`.
- Retries for idempotent calls and rate limits, honoring `Retry-After`.
- Typed errors (`*stx.APIError`, `*stx.NotAvailableError`, `*stx.TransportError`) and sentinels for `errors.Is`.
- `stx.Decimal` for money and quantities, exact through JSON.
- Paging with `iter.Seq2` iterators.
- Every REST operation: `client.Account` (identity, balance, positions, profile, terms), `client.Markets` (list, get, market types), `client.Events` (list, players), `client.Orders` (place, batch place, get, list, cancel, batch cancel, cancel all), `client.Portfolio` (fills, settlements, per-market stats, deposits, withdrawals, adjustments, fees, loyalty) and `client.Leaderboard`.
- `Client.Capabilities` reports which features an exchange has; calls to a missing feature return an error matching `stx.ErrNotAvailable`.
- Creating markets: `Markets.Create` (up to 25 per call, dry run, local validation) and `Events.StatsFor`.
- Package `oauth`: app tokens (client credentials, cached and renewed before expiry), member connect (authorization code with PKCE, refresh with rotation, revocation, introspection), memory and file token stores, scopes, pending terms, dynamic client registration and sign in with STX (OpenID Connect with ID token verification).
- `client.Stream`: one WebSocket connection for every documented channel, with typed messages, automatic reconnect and rejoin, and the cancel-on-disconnect ping kept healthy.
- `Stream.AccountView`: a live copy of your balance, open orders, fills and positions.
- Combos over the stream: `ComboPlayer` (request quotes, accept, cash out) and `ComboMaker` (receive requests, quote), also through `client.Combos`.
- `Stream.OrderSlip`: the cost of an order before you place it.
- Examples for linking a member's STX account to your app's own accounts (`examples/connect-account`), signing in with STX (`examples/sign-in-with-stx`), using STX through a login service (`examples/login-service`), streaming, combos and creating markets.
- `examples/quickstart`: checks your key, lists markets, reads your balance and positions, and with `-place-order` places and cancels a 1-cent order.
