package stx_test

import (
	"context"
	"fmt"
	"log"

	stx "github.com/stxapp/stx-go"
)

// Stream the book of one market and print the best bid.
func ExampleClient_Stream() {
	ctx := context.Background()
	client, err := stx.New(stx.WithEnvironment(stx.USDemo))
	if err != nil {
		log.Fatal(err)
	}
	stream, err := client.Stream(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer stream.Close()

	sub, err := stream.Subscribe(ctx, stx.OrderbookChannel("e00651a3-10cb-4bc1-9e1f-c5ea1796c83b"))
	if err != nil {
		log.Fatal(err)
	}
	for msg := range sub.Messages() {
		if book, ok := msg.Data.(*stx.OrderbookUpdate); ok && len(book.Bids) > 0 {
			fmt.Println(book.MarketID, book.Bids[0].Price)
		}
	}
}

// Your orders, with cancel-on-disconnect: the stream pings at a third of the
// granted timeout for as long as the topic is joined.
func ExampleOrdersChannel() {
	ctx := context.Background()
	client, err := stx.New(stx.WithProfile("us-demo"))
	if err != nil {
		log.Fatal(err)
	}
	stream, err := client.Stream(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer stream.Close()

	sub, err := stream.Subscribe(ctx, stx.OrdersChannel(stx.OrdersFilter{CancelOnDisconnect: true}))
	if err != nil {
		log.Fatal(err)
	}
	for msg := range sub.Messages() {
		switch d := msg.Data.(type) {
		case *stx.OrdersSnapshot:
			fmt.Println(len(d.Orders), "open orders")
		case *stx.Order:
			fmt.Println(d.ID, d.Status, d.Filled)
		}
	}
}

// A live view of your account.
func ExampleStream_AccountView() {
	ctx := context.Background()
	client, err := stx.New(stx.WithProfile("us-demo"))
	if err != nil {
		log.Fatal(err)
	}
	stream, err := client.Stream(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer stream.Close()

	view, err := stream.AccountView(ctx, stx.AccountViewOptions{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(view.Snapshot().Balance.AvailableBalance) // "746.8600"
	for change := range view.Changes() {
		fmt.Println(change.Kind, len(view.Snapshot().OpenOrders))
	}
}

// Ask for quotes on a combination and accept the best one.
func ExampleComboPlayer() {
	ctx := context.Background()
	client, err := stx.New(stx.WithProfile("us-demo"))
	if err != nil {
		log.Fatal(err)
	}
	player, err := client.Combos.Player(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer player.Close()

	req, err := player.RequestQuote(ctx, []stx.ComboLeg{
		{MarketID: "71692895-2991-440b-923d-7dfb6783cb17", Outcome: stx.ComboWon},
		{MarketID: "e00651a3-10cb-4bc1-9e1f-c5ea1796c83b", Outcome: stx.ComboWon},
	}, "250.00")
	if err != nil {
		log.Fatal(err)
	}
	ev, err := player.WaitFor(ctx, stx.ComboWait{Events: []string{"best_quote"}, RFQID: req.RFQID})
	if err != nil {
		log.Fatal(err)
	}
	best := ev.Data.(*stx.ComboBestQuoteEvent)
	placed, err := player.Accept(ctx, req.RFQID, best.QuoteID, best.Amount)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(placed.MarketID, placed.Quantity, placed.Price)
}
