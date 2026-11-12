// Command combos-taker asks for quotes on a two-leg combination for a $1.00
// stake and accepts the first quote.
//
//	go run ./examples/combos-taker 71692895-2991-440b-923d-7dfb6783cb17 e00651a3-10cb-4bc1-9e1f-c5ea1796c83b
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	stx "github.com/stxapp/stx-go"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: combos-taker MARKET_ID MARKET_ID")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client, err := stx.New()
	if err != nil {
		log.Fatal(err)
	}
	player, err := client.Combos.Player(ctx)
	if errors.Is(err, stx.ErrNotAvailable) {
		log.Fatal("combos are not available on ", client.Host())
	}
	if err != nil {
		log.Fatal(err)
	}
	defer player.Close()

	legs := []stx.ComboLeg{
		{MarketID: os.Args[1], Outcome: stx.ComboWon},
		{MarketID: os.Args[2], Outcome: stx.ComboWon},
	}
	req, err := player.RequestQuote(ctx, legs, "1.00")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("request", req.RFQID)

	for ev := range player.Events() {
		switch e := ev.Data.(type) {
		case *stx.ComboBestQuoteEvent:
			if e.QuoteID == "" {
				continue // nothing live
			}
			fmt.Println("best quote", e.Amount)
			placed, err := player.Accept(ctx, req.RFQID, e.QuoteID, e.Amount)
			var refused *stx.ComboRefusedError
			if errors.Is(err, stx.ErrComboPriceMoved) && errors.As(err, &refused) {
				fmt.Println("price moved; best is now", refused.Best.Amount)
				continue
			}
			if err != nil {
				log.Fatal(err)
			}
			fmt.Printf("placed %s contracts at %s on %s\n", placed.Quantity, placed.Price, placed.MarketID)
			return
		case *stx.ComboRequestClosedEvent:
			log.Fatal("request closed: ", e.Reason)
		}
	}
	log.Fatal(player.Err())
}
