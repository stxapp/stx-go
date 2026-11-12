// Package stx is a Go client for the STX exchange API.
//
// It calls the documented REST API with signed requests and streams the
// WebSocket channels. Every method takes a [context.Context] first, returns
// errors instead of panicking, and is safe for concurrent use.
//
// Create a client from a profile in ~/.stx/credentials, environment
// variables, or options:
//
//	client, err := stx.New(stx.WithEnvironment(stx.USDemo), stx.WithKey("my-key-id", "~/.stx/us-demo.pem"))
//	if err != nil {
//		log.Fatal(err)
//	}
//	page, err := client.Markets.List(ctx, &stx.MarketsQuery{Status: []string{"open"}, Limit: 10})
//
// Money and quantities are [Decimal] values, exact decimal strings such as
// "0.5600", never float64.
//
// Errors from the API are *[APIError] values; match them with [errors.As],
// or with [errors.Is] against sentinels such as [ErrNotFound],
// [ErrRateLimited] and [ErrNotAvailable].
//
// Authentication, environments, rate limits and error codes are described at
// https://docs.stxapp.io.
package stx
