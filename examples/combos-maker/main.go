// Command combos-maker quotes on every buy request it sees: three times the
// stake, standing for 30 seconds.
package main

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/signal"
	"time"

	stx "github.com/stxapp/stx-go"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client, err := stx.New()
	if err != nil {
		log.Fatal(err)
	}
	maker, err := client.Combos.Maker(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer maker.Close()

	for ev := range maker.Events() {
		switch e := ev.Data.(type) {
		case *stx.ComboRFQOpenedEvent:
			if e.Side != "buy" {
				continue // a cash out; this maker only quotes buys
			}
			stake, ok := e.Amount.Rat()
			if !ok {
				continue
			}
			amount := stx.Decimal(new(big.Rat).Mul(stake, big.NewRat(3, 1)).FloatString(2))
			q, err := maker.Quote(ctx, stx.ComboQuoteRequest{RFQID: e.RFQID, Amount: amount, ExpiresIn: 30 * time.Second})
			if err != nil {
				log.Print(err)
				continue
			}
			fmt.Printf("quoted %s on %s (%d legs)\n", q.Amount, e.RFQID, len(e.Legs))
		case *stx.ComboMakerPlacedEvent:
			fmt.Printf("bound: %s contracts at %s, order %s\n", e.Quantity, e.Price, e.OrderID)
		case *stx.ComboQuoteClosedEvent:
			fmt.Printf("quote %s closed: %s\n", e.QuoteID, e.Reason)
		}
	}
}
