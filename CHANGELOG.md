# Changelog

## 0.3.0 (2026-11-18)

- Post-only and fill-or-kill orders are sent as they are, and the exchange decides. Where a mode is not enabled the exchange rejects the order and the call returns a `*stx.NotAvailableError` (`orders.post_only` or `orders.fill_or_kill`, matching `stx.ErrNotAvailable`), as before. The client no longer asks `GET /api_version` before sending one. An exchange on a release from before fill modes places the order as a normal limit order. Which exchanges offer each mode: https://docs.stxapp.io/concepts/order-types/
- Breaking: `Client.SupportsFillMode`, `stx.FeatureOrdersPostOnly` and `stx.FeatureOrdersFillOrKill` are removed, and `Client.Capabilities` no longer reports `orders.post_only` or `orders.fill_or_kill`.

## 0.2.1 (2026-11-17)

- `APIError.Reference` on a request blocked by the firewall in front of the API is now the request's id from the `x-amz-cf-id` response header, the value support looks the block up by. 0.2.0 returned the firewall's placeholder text instead.

## 0.2.0 (2026-11-17)

Post-only and fill-or-kill orders:

- `OrderRequest.FillMode` takes `stx.PostOnly` (rest only as a maker) or `stx.FillOrKill` (fill in full or not at all) on a limit order, through `Orders.Place` and `Orders.PlaceBatch`. See https://docs.stxapp.io/sdks/go/trading/#post-only-and-fill-or-kill
- Before sending either mode the client checks, once per client, that the exchange offers it (`GET /api_version` lists it in `features`). Where it does not, the call returns a `*stx.NotAvailableError` (`orders.post_only` or `orders.fill_or_kill`, matching `stx.ErrNotAvailable`) and sends nothing, so the order is never placed as a normal one. `Client.SupportsFillMode` asks up front, and `Client.Capabilities` reports both features.
- A post-only order that would have matched returns a `*stx.PostOnlyWouldMatchError` (`Reason` `auto_matching` with the cancelled `Order`, or `match_with_self`), matching `stx.ErrPostOnlyWouldMatch` and `stx.ErrRejected`. A market that does not take the mode, such as a combination market, returns an error matching `stx.ErrFillModeNotAllowed`.

Blocked requests:

- A request refused by the firewall in front of the API matches `stx.ErrBlocked` and `stx.ErrRestrictedJurisdiction` or `stx.ErrUnusualNetwork`, with `APIError.Code`, `APIError.Message` and the new `APIError.Reference`; it is never retried. A firewall 429 matches `stx.ErrRateLimited` and carries `Reference`.
- A 403 with no JSON body matches `stx.ErrBlocked` (`Code` `blocked`) with a message about the `User-Agent` header the API requires.
- A 429 whose `Retry-After` is longer than `RetryPolicy.MaxBackoff` is no longer retried early: the error is returned with `RetryAfter`, as documented.

New generated fields: `Order.FillMode`, `Market.RulesVersion` and `Market.MarketRulesPdfURL`.

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
