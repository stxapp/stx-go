// Create player prop markets on an upcoming event as your app, then send the
// same request again to show that a repeat is safe.
//
// It needs an OAuth app that STX has granted markets.write, run from an
// address on the app's IP allow-list:
//
//	STX_HOST=demo.stxapp.io STX_OAUTH_CLIENT_ID=... STX_OAUTH_CLIENT_SECRET=... \
//	  go run ./examples/create-markets
//
// Markets can be created only before the event starts, once its roster is
// published. Lines must be positive and end in .5 (18.5, not 18).
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	stx "github.com/stxapp/stx-go"
	"github.com/stxapp/stx-go/oauth"
)

func main() {
	ctx := context.Background()
	app, err := oauth.New(oauth.ConfigFromEnv())
	if err != nil {
		log.Fatal(err)
	}
	client, err := app.AppClient([]string{oauth.ScopeEvents, oauth.ScopeMarketsWrite})
	if err != nil {
		log.Fatal(err)
	}

	// Discovery first: say so plainly where this exchange cannot create markets.
	caps, err := client.Capabilities(ctx, false)
	if err != nil {
		log.Fatal(err)
	}
	if err := caps.Require(stx.FeatureMarketsCreate); err != nil {
		log.Fatal(err)
	}

	// An event starting in more than an hour, with a published roster.
	eventID, roster, err := upcomingRoster(ctx, client)
	if err != nil {
		log.Fatal(err)
	}
	if roster == nil {
		fmt.Println("No upcoming event with a published roster.")
		return
	}
	fmt.Println("Event", eventID)

	// Two markets for the first player listed, on the event's first stats.
	player := roster.Teams[0].Players[0]
	var markets []stx.NewMarket
	for _, s := range roster.Stats[:min(2, len(roster.Stats))] {
		markets = append(markets, stx.NewMarket{PlayerID: player.PlayerID, Stat: s.Stat, Line: 10.5})
	}

	show := func(title string, opts *stx.CreateMarketsOptions) {
		results, err := client.Markets.Create(ctx, eventID, markets, opts)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(title)
		for _, r := range results {
			fmt.Printf("  %-8s %s %.1f %s%s\n", r.Status, r.Market.Stat, r.Market.Line, r.MarketID, r.Reason)
		}
	}
	show("Dry run (nothing is created):", &stx.CreateMarketsOptions{DryRun: true})
	show("Creating:", &stx.CreateMarketsOptions{Validate: true})
	show("Sending again (each comes back exists):", nil)
}

// upcomingRoster returns the first event starting in more than an hour whose
// roster is published, or "" when there is none.
func upcomingRoster(ctx context.Context, client *stx.Client) (string, *stx.EventPlayers, error) {
	soon := time.Now().Add(time.Hour)
	q := &stx.EventsQuery{Status: "scheduled", SortBy: "start_time", SortDirection: "asc"}
	for e, err := range client.Events.All(ctx, q) {
		if err != nil {
			return "", nil, err
		}
		if e.StartTimeISO.Before(soon) {
			continue
		}
		players, err := client.Events.Players(ctx, e.EventID)
		if err != nil {
			return "", nil, err
		}
		if len(players.Teams) > 0 && len(players.Teams[0].Players) > 0 && len(players.Stats) > 0 {
			return e.EventID, players, nil
		}
	}
	return "", nil, nil
}
