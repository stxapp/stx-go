// Command stream-orderbook prints the book of one market as it changes.
//
//	go run ./examples/stream-orderbook e00651a3-10cb-4bc1-9e1f-c5ea1796c83b
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	stx "github.com/stxapp/stx-go"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: stream-orderbook MARKET_ID")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client, err := stx.New()
	if err != nil {
		log.Fatal(err)
	}
	stream, err := client.Stream(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer stream.Close()

	sub, err := stream.Subscribe(ctx, stx.OrderbookChannel(os.Args[1]))
	if err != nil {
		log.Fatal(err)
	}
	for msg := range sub.Messages() {
		book, ok := msg.Data.(*stx.OrderbookUpdate)
		if !ok {
			continue
		}
		// Each message is the whole book: replace, never merge.
		bid, offer := "-", "-"
		if len(book.Bids) > 0 {
			bid = book.Bids[0].Price.String() + " x " + book.Bids[0].Quantity.String()
		}
		if len(book.Offers) > 0 {
			offer = book.Offers[0].Price.String() + " x " + book.Offers[0].Quantity.String()
		}
		fmt.Printf("%s  bid %s  offer %s\n", book.Timestamp, bid, offer)
	}
	if err := sub.Err(); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
