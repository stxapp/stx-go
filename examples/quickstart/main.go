// Command quickstart checks your STX API key, lists five open markets, reads
// your balance and positions, and with -place-order places a 1-cent limit buy
// and cancels it.
//
// It reads a profile from ~/.stx/credentials:
//
//	[ontario-production]
//	region   = ontario
//	env      = production
//	key_id   = your-key-id
//	key_file = ~/.stx/ontario-production.pem
//
// Run it:
//
//	go run . -profile ontario-production               # read only
//	go run . -profile ontario-production -place-order  # also a 1-cent round trip
//
// Without -profile it uses STX_PROFILE, or [default]. STX_REGION, STX_ENV,
// STX_HOST, STX_KEY_ID and STX_PRIVATE_KEY override the profile, so it also
// runs with no credentials file at all.
//
// On production -place-order uses real money: one contract at $0.01, which
// rests below the market and is cancelled straight away.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	stx "github.com/stxapp/stx-go"
)

func main() {
	profile := flag.String("profile", "", "a profile in ~/.stx/credentials (default: STX_PROFILE, or [default])")
	placeOrder := flag.Bool("place-order", false, "also place a 1-cent limit buy and cancel it")
	flag.Parse()

	var opts []stx.Option
	if *profile != "" {
		opts = append(opts, stx.WithProfile(*profile))
	}
	client, err := stx.New(opts...)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	fmt.Printf("Exchange: %s (profile %s)\n", client.BaseURL(), client.Profile())

	// 1. Who is this key?
	me, err := client.Account.Me(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Key %s belongs to user %s (scope %s)\n", me.KeyID, me.UserID, me.Scope)

	// 2. Markets: amounts are exact decimal strings, as the API sends them.
	page, err := client.Markets.List(ctx, &stx.MarketsQuery{Status: []string{"open"}, Trading: stx.Bool(true), Limit: 5})
	if err != nil {
		log.Fatal(err)
	}
	for _, m := range page.Items {
		bid, offer := stx.Decimal("-"), stx.Decimal("-")
		if len(m.Bids) > 0 {
			bid = m.Bids[0].Price // bids are sorted highest first
		}
		if len(m.Offers) > 0 {
			offer = m.Offers[len(m.Offers)-1].Price // offers are sorted highest first, so the best is last
		}
		fmt.Printf("  %s  bid %s  offer %s  last %s\n", m.Symbol, bid, offer, m.LastTradedPrice)
	}

	// 3. Balance and positions.
	balance, err := client.Account.Balance(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Available balance: %s\n", balance.AvailableBalance)
	positions, err := client.Account.Positions(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	open := 0
	for _, p := range positions {
		if !p.Position.IsZero() {
			open++
		}
	}
	fmt.Printf("Open positions: %d\n", open)

	// 4. Optional: a 1-cent limit buy, then cancel it.
	if !*placeOrder {
		return
	}
	// Try scheduled markets until one takes the order; a market can be open
	// for viewing while not accepting orders.
	q := &stx.MarketsQuery{Status: []string{"open"}, Trading: stx.Bool(true), SortBy: "event_start", SortDirection: "desc"}
	tried := 0
	for market, err := range client.Markets.All(ctx, q) {
		if err != nil {
			log.Fatal(err)
		}
		if market.EventStatus != "scheduled" {
			continue
		}
		order, err := client.Orders.Place(ctx, stx.OrderRequest{
			MarketID:      market.MarketID,
			Action:        stx.Buy,
			OrderType:     stx.Limit,
			Price:         stx.MustDecimal("0.01"),
			Quantity:      stx.MustDecimal("1"),
			ClientOrderID: fmt.Sprintf("quickstart-%d", time.Now().UnixMilli()),
		})
		var apiErr *stx.APIError
		if errors.As(err, &apiErr) {
			fmt.Printf("%s refused the order (%d): %s\n", market.Symbol, apiErr.StatusCode, apiErr.Message)
			if tried++; tried < 5 && errors.Is(err, stx.ErrRejected) {
				continue
			}
			return
		} else if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Placed %s on %s: %s %s @ %s\n", order.ID, market.Symbol, order.Status, order.Quantity, order.Price)
		cancelled, err := client.Orders.Cancel(ctx, order.ID)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Cancelled: %s\n", cancelled.Status)
		return
	}
	fmt.Println("No open market with an unstarted event right now.")
}
