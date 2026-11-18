package stx

import (
	"context"
	"crypto"
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// Client calls the STX API. Create one with [New]; it is safe for concurrent
// use and should be reused.
type Client struct {
	// Account: identity, balance, positions, profile and terms.
	Account *AccountService
	// Markets: listing markets, market types and creating markets.
	Markets *MarketsService
	// Events: listing events and their players.
	Events *EventsService
	// Orders: placing, listing and cancelling orders.
	Orders *OrdersService
	// Portfolio: fills, settlements, per-market stats and money movements.
	Portfolio *PortfolioService
	// Leaderboard: public rankings and your own standing.
	Leaderboard *LeaderboardService
	// Combos: combo requests for quote and quotes.
	Combos *CombosService

	baseURL    string
	socketURL  string
	host       string
	profile    string
	auth       Auth
	apiKey     *APIKey
	verifyTLS  bool
	httpClient *http.Client
	retry      RetryPolicy
	timeout    time.Duration
	logger     *slog.Logger
	onResponse func(ResponseEvent)
	userAgent  string
	sleep      func(context.Context, time.Duration) error

	caps capsCache
}

// Option configures a [Client].
type Option func(*options) error

type options struct {
	in          settingsInput
	environment *Environment
	auth        Auth
	signerID    string
	signer      crypto.Signer
	httpClient  *http.Client
	retry       *RetryPolicy
	timeout     time.Duration
	logger      *slog.Logger
	onResponse  func(ResponseEvent)
	uaSuffix    string
}

func ptr[T any](v T) *T { return &v }

// WithEnvironment selects a published environment, such as [USDemo].
// [WithHost] still wins over it.
func WithEnvironment(e Environment) Option {
	return func(o *options) error {
		if e.Region == "" || e.Env == "" {
			return configError("environment %q has no region or env", e.Name)
		}
		o.environment = &e
		return nil
	}
}

// WithEnvironmentName selects a published environment by name, such as
// "us-demo" or "ontario-production".
func WithEnvironmentName(name string) Option {
	return func(o *options) error {
		e, err := EnvironmentNamed(name)
		if err != nil {
			return err
		}
		o.environment = &e
		return nil
	}
}

// WithRegion selects the host by region ("us" or "ontario") and env ("demo"
// or "production"; "prod" and "live" are accepted).
func WithRegion(region, env string) Option {
	return func(o *options) error {
		o.in.region, o.in.env = ptr(region), ptr(env)
		return nil
	}
}

// WithHost sets the API host: a hostname ("demo.stxapp.io"), which gets
// https, or a URL ("http://localhost:4000") whose scheme is kept.
func WithHost(host string) Option {
	return func(o *options) error {
		o.in.host = ptr(host)
		return nil
	}
}

// WithKey sets the API key: its id and the Ed25519 private key as PKCS#8 PEM
// text or a path to a PEM file.
func WithKey(id, privateKey string) Option {
	return func(o *options) error {
		o.in.keyID, o.in.privateKey = ptr(id), ptr(privateKey)
		return nil
	}
}

// WithSigner sets the API key id and a [crypto.Signer] producing pure
// Ed25519 signatures, for keys held in an HSM or a cloud KMS.
func WithSigner(id string, signer crypto.Signer) Option {
	return func(o *options) error {
		o.signerID, o.signer = id, signer
		return nil
	}
}

// WithAuth authenticates with any [Auth] instead of an API key, such as an
// OAuth bearer token from the oauth package.
func WithAuth(a Auth) Option {
	return func(o *options) error {
		o.auth = a
		return nil
	}
}

// WithProfile reads the named section of ~/.stx/credentials.
func WithProfile(name string) Option {
	return func(o *options) error {
		o.in.profile = ptr(name)
		return nil
	}
}

// WithCredentialsFile reads profiles from path instead of ~/.stx/credentials.
func WithCredentialsFile(path string) Option {
	return func(o *options) error {
		o.in.credentialsFile = expandHome(path)
		return nil
	}
}

// WithHTTPClient sets the HTTP client, for proxies, custom TLS or tests. The
// default is a client with sensible connection pooling and no overall
// timeout (each request has its own, see [WithTimeout]).
func WithHTTPClient(hc *http.Client) Option {
	return func(o *options) error {
		o.httpClient = hc
		return nil
	}
}

// WithRetry sets the retry policy; [NoRetry] turns retries off.
func WithRetry(p RetryPolicy) Option {
	return func(o *options) error {
		o.retry = &p
		return nil
	}
}

// WithTimeout sets the time limit for each HTTP attempt. Default 30 s.
func WithTimeout(d time.Duration) Option {
	return func(o *options) error {
		o.timeout = d
		return nil
	}
}

// WithLogger logs requests, retries and stream events at debug level.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) error {
		o.logger = l
		return nil
	}
}

// WithResponseHook calls fn once per HTTP attempt, after it completes or
// fails: an audit or activity log hook. A panic in fn is recovered.
func WithResponseHook(fn func(ResponseEvent)) Option {
	return func(o *options) error {
		o.onResponse = fn
		return nil
	}
}

// WithUserAgent appends product to the User-Agent header, for example
// "my-app/1.2".
func WithUserAgent(product string) Option {
	return func(o *options) error {
		o.uaSuffix = product
		return nil
	}
}

// WithInsecureSkipVerify turns off TLS certificate checks. Use it only for a
// local server. It is ignored when WithHTTPClient is given.
func WithInsecureSkipVerify() Option {
	return func(o *options) error {
		o.in.verifyTLS = ptr(false)
		return nil
	}
}

// New returns a client configured from opts, then environment variables, then
// a profile in ~/.stx/credentials. A client without credentials is allowed
// (the public market data channels accept an unsigned socket), but REST
// calls then fail with [ErrConfig].
func New(opts ...Option) (*Client, error) {
	var o options
	for _, opt := range opts {
		if err := opt(&o); err != nil {
			return nil, err
		}
	}
	if o.environment != nil {
		if o.in.region != nil || o.in.env != nil {
			return nil, configError("use WithEnvironment or WithRegion, not both")
		}
		o.in.region, o.in.env = ptr(o.environment.Region), ptr(o.environment.Env)
	}
	explicitKey := o.in.keyID != nil
	if o.auth != nil || o.signer != nil {
		if explicitKey || (o.auth != nil && o.signer != nil) {
			return nil, configError("use one of WithKey, WithSigner and WithAuth")
		}
		// The key from the environment or a profile is not used.
		o.in.keyID, o.in.privateKey = ptr(""), ptr("")
	}
	s, err := resolveSettings(o.in)
	if err != nil {
		return nil, err
	}
	base, err := baseURLFor(s.region, s.env, s.host)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(base)
	c := &Client{
		baseURL:    base,
		socketURL:  socketURLFor(base),
		host:       u.Host,
		profile:    s.profile,
		verifyTLS:  s.verifyTLS,
		retry:      DefaultRetryPolicy,
		timeout:    30 * time.Second,
		logger:     o.logger,
		onResponse: o.onResponse,
		userAgent:  userAgent,
		sleep:      sleepContext,
	}
	switch {
	case o.auth != nil:
		c.auth = o.auth
	case o.signer != nil:
		if c.apiKey, err = NewAPIKeyWithSigner(o.signerID, o.signer); err != nil {
			return nil, err
		}
		c.auth = c.apiKey
	default:
		if c.apiKey, err = apiKeyFromSettings(s.keyID, s.privateKey); err != nil {
			return nil, err
		}
		if c.apiKey != nil {
			c.auth = c.apiKey
		}
	}
	if o.retry != nil {
		c.retry = *o.retry
	}
	if o.timeout > 0 {
		c.timeout = o.timeout
	}
	if c.logger == nil {
		c.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.uaSuffix != "" {
		c.userAgent += " " + o.uaSuffix
	}
	c.httpClient = o.httpClient
	if c.httpClient == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.MaxIdleConnsPerHost = 16
		if !c.verifyTLS {
			t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opted in for a local server
		}
		c.httpClient = &http.Client{Transport: t}
	}
	c.Account = &AccountService{c: c}
	c.Markets = &MarketsService{c: c}
	c.Events = &EventsService{c: c}
	c.Orders = &OrdersService{c: c}
	c.Portfolio = &PortfolioService{c: c}
	c.Leaderboard = &LeaderboardService{c: c}
	c.Combos = &CombosService{c: c}
	return c, nil
}

// BaseURL returns the API base URL, such as "https://demo.stxapp.io".
func (c *Client) BaseURL() string { return c.baseURL }

// Host returns the exchange host, such as "demo.stxapp.io": the name
// [NotAvailableError] reports.
func (c *Client) Host() string { return c.host }

// SocketURL returns the WebSocket URL.
func (c *Client) SocketURL() string { return c.socketURL }

// Profile returns the name of the credentials profile in use.
func (c *Client) Profile() string { return c.profile }

// APIKey returns the configured API key, or nil when the client uses another
// [Auth] or none.
func (c *Client) APIKey() *APIKey { return c.apiKey }

// Auth returns what authenticates requests, or nil.
func (c *Client) Auth() Auth { return c.auth }

// Logger returns the client's logger (a discarding one when none was set).
func (c *Client) Logger() *slog.Logger { return c.logger }

// HTTPClient returns the HTTP client in use.
func (c *Client) HTTPClient() *http.Client { return c.httpClient }

// UserAgent returns the User-Agent header the client sends.
func (c *Client) UserAgent() string { return c.userAgent }
