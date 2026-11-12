package stx_test

import (
	"context"
	"errors"
	"fmt"
	"log"

	stx "github.com/stxapp/stx-go"
)

// A client for the United States demo, with an API key from a PEM file.
func ExampleNew() {
	client, err := stx.New(
		stx.WithEnvironment(stx.USDemo),
		stx.WithKey("3f1c9a52-7d4e-4b8a-9c21-5e6f7a8b9c0d", "~/.stx/us-demo.pem"),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(client.BaseURL())
}

// A client from the "us-demo" section of ~/.stx/credentials.
func ExampleWithProfile() {
	client, err := stx.New(stx.WithProfile("us-demo"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(client.Host())
}

// Matching API errors.
func ExampleAPIError() {
	err := error(&stx.APIError{StatusCode: 422, Message: "Insufficient assets.", Method: "POST", Path: "/api/v1/orders"})
	var apiErr *stx.APIError
	if errors.As(err, &apiErr) {
		fmt.Println(apiErr.StatusCode, apiErr.Message)
	}
	fmt.Println(errors.Is(err, stx.ErrRejected))
	// Output:
	// 422 Insufficient assets.
	// true
}

// Decimals keep the exact digits the API sends.
func ExampleDecimal() {
	price := stx.MustDecimal("0.5600")
	fmt.Println(price, price.Cmp(stx.MustDecimal("0.56")) == 0)
	// Output: 0.5600 true
}

// List open markets with their best bid and offer.
func ExampleMarketsService_All() {
	ctx := context.Background()
	client, err := stx.New()
	if err != nil {
		log.Fatal(err)
	}
	for market, err := range client.Markets.All(ctx, &stx.MarketsQuery{Status: []string{"open"}, Trading: stx.Bool(true)}) {
		if err != nil {
			log.Fatal(err)
		}
		var bid, offer stx.Decimal
		if len(market.Bids) > 0 {
			bid = market.Bids[0].Price
		}
		if len(market.Offers) > 0 {
			offer = market.Offers[len(market.Offers)-1].Price // sorted highest first
		}
		fmt.Printf("%s  bid %s  offer %s\n", market.Symbol, bid, offer)
	}
}

// Place a limit order, then cancel it.
func ExampleOrdersService_Place() {
	ctx := context.Background()
	client, err := stx.New()
	if err != nil {
		log.Fatal(err)
	}
	order, err := client.Orders.Place(ctx, stx.OrderRequest{
		MarketID:      "42b861f8-8340-4046-ae06-83a0cec93456",
		Action:        stx.Buy,
		OrderType:     stx.Limit,
		Price:         stx.MustDecimal("0.01"),
		Quantity:      stx.MustDecimal("1"),
		ClientOrderID: "my-order-001",
	})
	if errors.Is(err, stx.ErrRejected) {
		log.Fatalf("refused by the exchange: %v", err)
	} else if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Placed", order.ID, order.Status)
	cancel, err := client.Orders.Cancel(ctx, order.ID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Cancelled:", cancel.Status)
}
