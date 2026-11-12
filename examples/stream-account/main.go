// Command stream-account keeps a live view of your account and prints it
// whenever it changes.
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
	stream, err := client.Stream(ctx, stx.WithOnReconnect(func() {
		// The view reseeds itself; reload anything else you read over REST.
		log.Print("reconnected")
	}))
	if err != nil {
		log.Fatal(err)
	}
	defer stream.Close()

	view, err := stream.AccountView(ctx, stx.AccountViewOptions{})
	if err != nil {
		log.Fatal(err)
	}
	for change := range view.Changes() {
		st := view.Snapshot()
		fmt.Printf("%-9s available %s, %d open orders, %d positions\n",
			change.Kind, st.Balance.AvailableBalance, len(st.OpenOrders), len(st.Positions))
	}
}
