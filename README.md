# STX Go SDK

`github.com/stxapp/stx-go` calls the [STX API](https://docs.stxapp.io): markets, orders, positions and account history over signed REST requests, and live data over the documented WebSocket channels.

```sh
go get github.com/stxapp/stx-go
```

Requires Go 1.24 or later.

## Quickstart

Create an API key on the exchange, save the private key as a PEM file, and add a profile to `~/.stx/credentials`:

```ini
[default]
region = us
env = demo
key_id = 3f1c9a52-7d4e-4b8a-9c21-5e6f7a8b9c0d
private_key = ~/.stx/stx-key.pem
```

The [environments page](https://docs.stxapp.io/environments/) lists every region and environment.

```go
package main

import (
	"context"
	"fmt"
	"log"

	stx "github.com/stxapp/stx-go"
)

func main() {
	ctx := context.Background()
	client, err := stx.New()
	if err != nil {
		log.Fatal(err)
	}

	// Five open markets with the best bid and offer.
	page, err := client.Markets.List(ctx, &stx.MarketsQuery{Status: []string{"open"}, Trading: stx.Bool(true), Limit: 5})
	if err != nil {
		log.Fatal(err)
	}
	for _, m := range page.Items {
		bid, offer := "-", "-"
		if len(m.Bids) > 0 {
			bid = m.Bids[0].Price.String()
		}
		if len(m.Offers) > 0 {
			offer = m.Offers[len(m.Offers)-1].Price.String()
		}
		fmt.Printf("%s bid %s offer %s\n", m.Symbol, bid, offer)
	}
	if len(page.Items) == 0 {
		return
	}

	// A 1-cent buy that will not fill, then cancel it.
	order, err := client.Orders.Place(ctx, stx.OrderRequest{
		MarketID:      page.Items[0].MarketID,
		Action:        stx.Buy,
		OrderType:     stx.Limit,
		Price:         stx.MustDecimal("0.01"),
		Quantity:      stx.MustDecimal("1"),
		ClientOrderID: "quickstart-0001",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("placed", order.ID, order.Status)

	canceled, err := client.Orders.Cancel(ctx, order.ID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("canceled", canceled.OrderID, canceled.Status)
}
```

```text
STXNHL-26OCT101600EDMSJ-GAMEEDM bid 0.5800 offer 0.6100
STXNHL-26OCT101600EDMSJ-SOGSJCMCDAVID209555-3.5 bid 0.4900 offer 0.5400
STXNHL-26OCT101600EDMSJ-SOGEDMCMCDAVID97-3.5 bid 0.6200 offer 0.6600
STXNBA-26OCT102000LALGSW-GAMELAL bid 0.4400 offer 0.4700
STXNBA-26OCT102000LALGSW-PTSLALLJAMES23-24.5 bid 0.5100 offer 0.5500
placed 8f023ead-6f2d-4cf5-9931-cd217e44a2ee accepted
canceled 8f023ead-6f2d-4cf5-9931-cd217e44a2ee cancelled
```

Placing an order needs a key with write access. Every call takes a `context.Context` and is safe to make from many goroutines on one client. Prices and quantities are `stx.Decimal` strings, never floats. Lists come back a page at a time (`List`), or as an iterator over every page (`All`). Errors from the exchange are `*stx.APIError`; check them with `errors.Is` against `stx.ErrRejected`, `stx.ErrNotFound`, `stx.ErrRateLimited` and the other sentinels, or `stx.ErrNotAvailable` for a feature an exchange does not offer.

The same client streams the [WebSocket channels](https://docs.stxapp.io/sdks/go/websockets/), trades [combos](https://docs.stxapp.io/sdks/go/combos/), and, with the `oauth` package, acts for members of your [app](https://docs.stxapp.io/sdks/go/apps/).

## Documentation

- [Go SDK guide](https://docs.stxapp.io/sdks/go/)
- [API reference](https://docs.stxapp.io/sdks/go/reference/)
- [Release notes](https://docs.stxapp.io/changelog/)
- [Support](https://docs.stxapp.io/support/)

MIT license. Use of the STX API and exchange is subject to the STX terms of use and privacy policy: United States ([terms of use](https://stxapp.io/legal/terms-of-use.pdf), [privacy policy](https://stxapp.io/legal/privacy-policy.pdf)), Ontario ([terms of use](https://stxapp.ca/legal/terms-of-use.pdf), [privacy policy](https://stxapp.ca/legal/privacy-policy.pdf)).
