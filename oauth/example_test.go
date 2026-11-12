package oauth_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	stx "github.com/stxapp/stx-go"
	"github.com/stxapp/stx-go/oauth"
)

// An app token calls the API as the app itself.
func ExampleClient_AppClient() {
	app, err := oauth.New(oauth.Config{
		BaseURL:      "demo.stxapp.io",
		ClientID:     "stxapp_client_9f2c41d7",
		ClientSecret: "stxapp_cs_your_secret",
	})
	if err != nil {
		log.Fatal(err)
	}
	client, err := app.AppClient([]string{oauth.ScopeEvents})
	if err != nil {
		log.Fatal(err)
	}
	types, err := client.Markets.Types(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(types[stx.RulePlayerStatLine].PartnerCreate)
}

// Linking a member: Begin on your login route, Complete on the callback.
func ExampleClient_Begin() {
	app, _ := oauth.New(oauth.Config{
		BaseURL:      "demo.stxapp.io",
		ClientID:     "stxapp_client_9f2c41d7",
		ClientSecret: "stxapp_cs_your_secret",
		RedirectURL:  "https://your.app/stx/callback",
		Scopes:       []string{oauth.ScopeBalanceRead, oauth.ScopeOrdersWrite},
	})
	pending := &oauth.MemoryPendingStore{}
	tokens := oauth.NewFileTokenStore("stx-tokens.json")

	http.HandleFunc("/stx/login", func(w http.ResponseWriter, r *http.Request) {
		auth, err := app.Begin(r.Context(), pending, oauth.BeginParams{Data: map[string]string{"user": "alice"}})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, auth.URL, http.StatusFound)
	})
	http.HandleFunc("/stx/callback", func(w http.ResponseWriter, r *http.Request) {
		_, err := app.Complete(r.Context(), pending, r.URL.Query(), oauth.RedeemOptions{
			Store:         tokens,
			MemberKeyFunc: func(data map[string]string) string { return data["user"] },
		})
		switch {
		case errors.Is(err, oauth.ErrAccessDenied):
			fmt.Fprint(w, "Not connected.")
		case errors.Is(err, oauth.ErrAccountPending):
			fmt.Fprint(w, "Your STX account is still being verified.")
		case err != nil:
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			fmt.Fprint(w, "Connected.")
		}
	})
}

// Sign in with STX: Start on your sign-in route, Finish on the callback.
func ExampleSignIn() {
	signIn, err := oauth.NewSignIn(oauth.SignInConfig{
		Issuer:       "https://demo.stxapp.io",
		ClientID:     "stxapp_client_9f2c41d7",
		ClientSecret: "stxapp_cs_your_secret",
		RedirectURL:  "https://your.app/auth/stx/callback",
	})
	if err != nil {
		log.Fatal(err)
	}
	started, err := signIn.Start(context.Background(), oauth.StartParams{Connection: "google"})
	if err != nil {
		log.Fatal(err)
	}
	// Keep started.Saved in the user's session and redirect to started.URL.
	// On the callback:
	//
	//	done, err := signIn.Finish(ctx, r.URL.Query(), saved)
	//	userID := done.Identity.Subject
	fmt.Println(started.URL != "")
}
