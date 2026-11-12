// An app with no login of its own: people register or log in with their STX
// account, and the app gets back who they are and tokens to act for them
// (https://docs.stxapp.io/oauth/sign-in-with-your-stx-account/).
//
// Server-side only: the client secret, the tokens and the PKCE verifier stay
// on this server, and the browser gets an HttpOnly session cookie. Sign-ins
// in progress and sessions live in memory here; use your own store.
//
//	STX_HOST=demo.stxapp.io STX_OAUTH_CLIENT_ID=... STX_OAUTH_CLIENT_SECRET=... \
//	  STX_OAUTH_REDIRECT_URI=http://localhost:3000/auth/stx/callback go run ./examples/sign-in-with-stx
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"sync"

	"github.com/stxapp/stx-go/oauth"
)

type server struct {
	signIn *oauth.SignIn
	// app acts for signed-in members with the tokens in tokens, keyed on
	// the member's subject.
	app    *oauth.Client
	tokens oauth.TokenStore

	mu       sync.Mutex
	pending  map[string]oauth.SavedSignIn // sign-in cookie -> saved sign-in
	sessions map[string]string            // session cookie -> subject
	emails   map[string]string            // subject -> email
}

func main() {
	cfg := oauth.ConfigFromEnv()
	if cfg.RedirectURL == "" {
		cfg.RedirectURL = "http://localhost:3000/auth/stx/callback"
	}
	s, err := newServer(cfg)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("listening on http://localhost:3000")
	log.Fatal(http.ListenAndServe("localhost:3000", s.routes()))
}

func newServer(cfg oauth.Config) (*server, error) {
	app, err := oauth.New(cfg)
	if err != nil {
		return nil, err
	}
	signIn, err := oauth.NewSignIn(oauth.SignInConfig{
		Issuer:       app.BaseURL(),
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Scopes:       []string{oauth.ScopeProfileRead, oauth.ScopeBalanceRead},
	})
	if err != nil {
		return nil, err
	}
	return &server{
		signIn: signIn, app: app, tokens: &oauth.MemoryTokenStore{},
		pending: map[string]oauth.SavedSignIn{}, sessions: map[string]string{}, emails: map[string]string{},
	}, nil
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/stx/start", s.start)
	mux.HandleFunc("GET /auth/stx/callback", s.callback)
	mux.HandleFunc("GET /api/balance", s.balance)
	mux.HandleFunc("POST /auth/stx/disconnect", s.disconnect)
	mux.HandleFunc("GET /{$}", s.home)
	return mux
}

func (s *server) start(w http.ResponseWriter, r *http.Request) {
	// connection=google skips the STX login page and goes straight to Google.
	started, err := s.signIn.Start(r.Context(), oauth.StartParams{Connection: r.URL.Query().Get("connection")})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	id := randomID()
	s.mu.Lock()
	s.pending[id] = started.Saved
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "signin", Value: id, Path: "/auth/stx", MaxAge: 600, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, started.URL, http.StatusFound)
}

func (s *server) callback(w http.ResponseWriter, r *http.Request) {
	id := cookieValue(r, "signin")
	s.mu.Lock()
	saved, ok := s.pending[id]
	delete(s.pending, id)
	s.mu.Unlock()
	if !ok {
		http.Error(w, "No sign-in in progress; start again.", http.StatusBadRequest)
		return
	}
	done, err := s.signIn.Finish(r.Context(), r.URL.Query(), saved)
	if err != nil {
		// oauth.ErrAccessDenied and oauth.ErrAccountPending leave the member signed out.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Key your user record on the subject, the member's stable id for your app.
	subject := done.Identity.Subject
	if err := s.tokens.Set(r.Context(), subject, done.Token); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	session := randomID()
	s.mu.Lock()
	s.sessions[session] = subject
	s.emails[subject] = done.Identity.Email
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "session", Value: session, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/", http.StatusFound)
}

// member returns the signed-in member's subject, or "".
func (s *server) member(r *http.Request) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[cookieValue(r, "session")]
}

func (s *server) home(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	subject := s.member(r)
	if subject == "" {
		fmt.Fprint(w, `<a href="/auth/stx/start">Continue with your STX account</a>`)
		return
	}
	s.mu.Lock()
	email := s.emails[subject]
	s.mu.Unlock()
	fmt.Fprintf(w, "Signed in as %s", html.EscapeString(email))
}

func (s *server) balance(w http.ResponseWriter, r *http.Request) {
	subject := s.member(r)
	if subject == "" {
		http.Error(w, "Sign in first.", http.StatusUnauthorized)
		return
	}
	// The client refreshes the member's tokens before expiry and stores the
	// rotated pair.
	member, err := s.app.MemberClient(s.tokens, subject, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	balance, err := member.Account.Balance(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(balance)
}

func (s *server) disconnect(w http.ResponseWriter, r *http.Request) {
	subject := s.member(r)
	if subject != "" {
		if _, err := s.app.Unlink(r.Context(), s.tokens, subject); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		s.mu.Lock()
		delete(s.sessions, cookieValue(r, "session"))
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}
