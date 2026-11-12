// STX as a login option inside a login service such as Auth0, Clerk, Okta
// or Cognito (https://docs.stxapp.io/oauth/sign-in-with-your-stx-account/login-vendor/).
//
// The service talks to STX, so your Go server does two things:
//
//	go run ./examples/login-service settings
//
// prints the values to enter in the service's custom OpenID Connect
// connection, read from the exchange's discovery documents, and
//
//	STX_MEMBER_REFRESH_TOKEN=... go run ./examples/login-service act user-1
//
// takes the member's STX refresh token, as your service passes it on to your
// server, stores it under your own user id and acts for the member. STX
// rotates the refresh token on every refresh, so from then on your server
// refreshes and the service must not. If your service does not pass the STX
// tokens on, keep your login and link STX accounts from your server instead
// (see the connect-account example).
//
// Both read STX_HOST, STX_OAUTH_CLIENT_ID, STX_OAUTH_CLIENT_SECRET and
// STX_OAUTH_REDIRECT_URI (the service's callback URL).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/stxapp/stx-go/oauth"
)

func main() {
	ctx := context.Background()
	cfg := oauth.ConfigFromEnv()
	app, err := oauth.Discover(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	switch {
	case len(os.Args) == 2 && os.Args[1] == "settings":
		if err := printSettings(ctx, app, cfg); err != nil {
			log.Fatal(err)
		}
	case len(os.Args) == 3 && os.Args[1] == "act":
		if err := act(ctx, app, os.Args[2], os.Getenv("STX_MEMBER_REFRESH_TOKEN")); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatal("usage: login-service settings | login-service act <your user id>")
	}
}

// printSettings prints the connection settings for the login service.
func printSettings(ctx context.Context, app *oauth.Client, cfg oauth.Config) error {
	signIn, err := oauth.NewSignIn(oauth.SignInConfig{Issuer: app.BaseURL(), ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: cfg.RedirectURL})
	if err != nil {
		return err
	}
	conf, err := signIn.Configuration(ctx)
	if err != nil {
		return err
	}
	m := app.Metadata()
	fmt.Printf("Discovery URL:          %s/.well-known/openid-configuration\n", conf.Issuer)
	fmt.Printf("Issuer:                 %s\n", conf.Issuer)
	fmt.Printf("Client ID:              %s\n", cfg.ClientID)
	fmt.Printf("Callback URL:           %s (registered with STX)\n", cfg.RedirectURL)
	fmt.Printf("Scopes:                 openid plus the member scopes you registered (offered: %s)\n", strings.Join(m.ScopesSupported, " "))
	fmt.Printf("Response type:          code\n")
	fmt.Printf("PKCE:                   required, %s\n", strings.Join(m.CodeChallengeMethodsSupported, " "))
	fmt.Printf("Token endpoint auth:    client_secret_basic\n")
	fmt.Printf("ID token algorithm:     %s, keys at %s\n", strings.Join(conf.IDTokenSigningAlgValuesSupported, " "), conf.JWKSURI)
	fmt.Printf("User id / email claims: sub / email, email_verified\n")
	return nil
}

// act stores the member's refresh token under userID and reads their balance.
func act(ctx context.Context, app *oauth.Client, userID, refreshToken string) error {
	if refreshToken == "" {
		return fmt.Errorf("set STX_MEMBER_REFRESH_TOKEN")
	}
	tokens := oauth.NewFileTokenStore("stx-tokens.json")
	// No access token yet: the first call refreshes and stores the new pair.
	if err := tokens.Set(ctx, userID, &oauth.Token{RefreshToken: refreshToken, Expiry: time.Now()}); err != nil {
		return err
	}
	member, err := app.MemberClient(tokens, userID, nil)
	if err != nil {
		return err
	}
	balance, err := member.Account.Balance(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%s: available balance %s\n", userID, balance.AvailableBalance)
	return nil
}
