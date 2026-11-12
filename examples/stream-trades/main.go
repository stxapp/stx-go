// Command stream-trades prints every trade on the exchange as it happens.
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

	sub, err := stream.Subscribe(ctx, stx.TradesChannel(stx.TradesFilter{}))
	if err != nil {
		log.Fatal(err)
	}
	for msg := range sub.Messages() {
		if t, ok := msg.Data.(*stx.PublicTrade); ok {
			// Action is the taker's side.
			fmt.Printf("%s %s %s @ %s\n", t.MarketSymbol, t.Action, t.Quantity, t.Price)
		}
	}
}
