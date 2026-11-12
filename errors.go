package stx

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Sentinel errors. Match them with [errors.Is]; an *[APIError] matches the
// sentinel for its status, and the more specific ones listed below.
var (
	// ErrConfig: the client is misconfigured (unknown environment, missing key,
	// bad PEM, unreadable credentials file).
	ErrConfig = errors.New("stx: configuration error")

	// ErrBadRequest: 400, a parameter or body field was refused.
	ErrBadRequest = errors.New("stx: bad request")
	// ErrUnauthorized: 401, the signature, key id or timestamp was not
	// accepted. A machine clock more than 30 seconds off also lands here.
	ErrUnauthorized = errors.New("stx: unauthorized")
	// ErrInvalidToken: 401 on an OAuth bearer request whose access token is
	// missing, expired or revoked. Also matches ErrUnauthorized.
	ErrInvalidToken = errors.New("stx: invalid token")
	// ErrForbidden: 403, the key is read-only or the account may not do this.
	ErrForbidden = errors.New("stx: forbidden")
	// ErrInsufficientScope: 403 on an OAuth bearer request whose grant lacks a
	// scope; see APIError.RequiredScopes. Also matches ErrForbidden.
	ErrInsufficientScope = errors.New("stx: insufficient scope")
	// ErrTermsRequired: 403, the member has not accepted the current terms;
	// see APIError.TermsURL. Also matches ErrForbidden.
	ErrTermsRequired = errors.New("stx: terms not accepted")
	// ErrIPNotAllowed: 403, the write came from an address the app's IP
	// allow-list lacks. Also matches ErrForbidden.
	ErrIPNotAllowed = errors.New("stx: ip address not allowed")
	// ErrNotFound: 404, the resource does not exist or belongs to another
	// account.
	ErrNotFound = errors.New("stx: not found")
	// ErrRejected: 422, the exchange refused the request, for example
	// insufficient funds or a closed market.
	ErrRejected = errors.New("stx: rejected")
	// ErrRateLimited: 429, too many requests; see APIError.RetryAfter.
	ErrRateLimited = errors.New("stx: rate limited")
	// ErrServer: 5xx, the server failed.
	ErrServer = errors.New("stx: server error")

	// ErrTransport: the request got no answer (connection, DNS, TLS or
	// timeout). The error also wraps the underlying network error.
	ErrTransport = errors.New("stx: transport error")

	// ErrNotAvailable: this exchange does not have the feature yet, for
	// example combos where they are not switched on. Nothing is wrong with the
	// request or the credentials; see *NotAvailableError for the feature.
	ErrNotAvailable = errors.New("stx: not available")
)

// APIError is an error status answered by the API.
type APIError struct {
	// StatusCode is the HTTP status, for example 422.
	StatusCode int
	// Code is the machine-readable error code when the API sends one, such as
	// "terms_required" or "not_found"; otherwise empty.
	Code string
	// Message is the API's human-readable message, kept verbatim.
	Message string
	// RequestID is the X-Request-Id response header, when present. Quote it
	// when you contact support.
	RequestID string
	// Method and Path are the request that failed; Path includes the query.
	Method string
	Path   string
	// Body is the raw response body.
	Body []byte
	// RetryAfter is the server's Retry-After on a 429, or zero.
	RetryAfter time.Duration
	// RequiredScopes names the OAuth scopes that would satisfy a 403
	// insufficient-scope answer.
	RequiredScopes []string
	// TermsURL is the page where the member accepts the terms, on a
	// terms-required answer.
	TermsURL string

	bearerError string
}

// Error returns "stx: METHOD PATH: STATUS: message".
func (e *APIError) Error() string {
	return fmt.Sprintf("stx: %s %s: %d: %s", e.Method, e.Path, e.StatusCode, e.Message)
}

// Is matches the sentinel errors for this status and code.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrBadRequest:
		return e.StatusCode == 400
	case ErrUnauthorized:
		return e.StatusCode == 401
	case ErrInvalidToken:
		return e.StatusCode == 401 && e.bearerError == "invalid_token"
	case ErrForbidden:
		return e.StatusCode == 403
	case ErrInsufficientScope:
		return e.StatusCode == 403 && (e.bearerError == "insufficient_scope" || strings.Contains(strings.ToLower(e.Message), "insufficient scope"))
	case ErrTermsRequired:
		return e.StatusCode == 403 && e.Code == "terms_required"
	case ErrIPNotAllowed:
		return e.StatusCode == 403 && e.Code == "ip_not_allowed"
	case ErrNotFound:
		return e.StatusCode == 404
	case ErrRejected:
		return e.StatusCode == 422
	case ErrRateLimited:
		return e.StatusCode == 429
	case ErrServer:
		return e.StatusCode >= 500
	}
	return false
}

// missingRoute reports a 404 from the exchange's router for a route it does
// not have ({"error": "not_found"} and nothing else), as opposed to a 404
// about a missing resource on a route it has.
func (e *APIError) missingRoute() bool {
	if e.StatusCode != 404 {
		return false
	}
	var body map[string]any
	if json.Unmarshal(e.Body, &body) != nil {
		return false
	}
	return len(body) == 1 && body["error"] == "not_found"
}

// NotAvailableError reports a feature this exchange does not have yet. It
// matches [ErrNotAvailable].
type NotAvailableError struct {
	// Feature is a stable id: "combos", "markets.create", "market_types",
	// "event_players", "leaderboard", "oauth", "oauth.member", "oauth.app",
	// "connect", "terms.stx_page", "loyalty", or "scope:<name>".
	Feature string
	// Host is the exchange that lacks it, for example "demo.stxapp.ca".
	Host string
	// Err is the answer the decision came from, when there was one.
	Err error
}

var featurePhrases = map[string]string{
	"oauth":          "OAuth is",
	"loyalty":        "The loyalty program is",
	"oauth.member":   "Member sign-in with OAuth is",
	"oauth.app":      "App tokens are",
	"connect":        "Sign in with STX is",
	"terms.stx_page": "Accepting terms on an STX page is",
	"markets.create": "Creating markets is",
	"market_types":   "Market types are",
	"event_players":  "Event players are",
	"leaderboard":    "The leaderboard is",
	"combos":         "Combos are",
}

// Error returns a sentence such as "stx: Combos are not available on
// demo.stxapp.ca yet."
func (e *NotAvailableError) Error() string {
	phrase, ok := featurePhrases[e.Feature]
	switch {
	case strings.HasPrefix(e.Feature, "scope:"):
		phrase = "The " + strings.TrimPrefix(e.Feature, "scope:") + " scope is"
	case !ok:
		phrase = e.Feature + " is"
	}
	return fmt.Sprintf("stx: %s not available on %s yet.", phrase, e.Host)
}

// Is matches [ErrNotAvailable].
func (e *NotAvailableError) Is(target error) bool { return target == ErrNotAvailable }

// Unwrap returns the underlying answer, if any.
func (e *NotAvailableError) Unwrap() error { return e.Err }

// TransportError is a request that got no answer. It matches [ErrTransport]
// and unwraps to the network error.
type TransportError struct {
	Method string
	Path   string
	Err    error
}

// Error describes the failed request.
func (e *TransportError) Error() string {
	return fmt.Sprintf("stx: %s %s: %v", e.Method, e.Path, e.Err)
}

// Is matches [ErrTransport].
func (e *TransportError) Is(target error) bool { return target == ErrTransport }

// Unwrap returns the network error.
func (e *TransportError) Unwrap() error { return e.Err }

func configError(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrConfig}, args...)...)
}

var (
	bearerErrorRe = regexp.MustCompile(`error\s*=\s*"([^"]*)"`)
	bearerScopeRe = regexp.MustCompile(`scope\s*=\s*"([^"]*)"`)
	bearerPrefix  = regexp.MustCompile(`(?i)^\s*Bearer\b`)
)

// newAPIError builds the error for one non-2xx response.
func newAPIError(status int, method, path string, body []byte, header func(string) string) *APIError {
	e := &APIError{StatusCode: status, Method: method, Path: path, Body: body, RequestID: header("X-Request-Id")}
	var fields map[string]any
	_ = json.Unmarshal(body, &fields)
	errText, _ := fields["error"].(string)
	description, _ := fields["error_description"].(string)
	switch {
	case description != "":
		e.Code, e.Message = errText, description
	case errText != "":
		e.Message = errText
		if isCode(errText) {
			e.Code = errText
		}
	case strings.TrimSpace(string(body)) != "":
		e.Message = truncate(strings.TrimSpace(string(body)), 500)
	default:
		e.Message = fmt.Sprintf("HTTP %d", status)
	}
	if u, ok := fields["terms_url"].(string); ok {
		e.TermsURL = u
	}
	if challenge := header("WWW-Authenticate"); bearerPrefix.MatchString(challenge) {
		if m := bearerErrorRe.FindStringSubmatch(challenge); m != nil {
			e.bearerError = m[1]
		}
		if m := bearerScopeRe.FindStringSubmatch(challenge); m != nil {
			e.RequiredScopes = strings.Fields(m[1])
		}
	}
	if status == 403 && len(e.RequiredScopes) == 0 {
		if s, ok := fields["scope"].(string); ok {
			e.RequiredScopes = strings.Fields(s)
		}
	}
	if status == 429 {
		e.RetryAfter = parseRetryAfter(header("Retry-After"))
	}
	return e
}

var codeRe = regexp.MustCompile(`^[a-z][a-z0-9_.]*$`)

func isCode(s string) bool { return codeRe.MatchString(s) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
