package stx

import (
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryPolicy decides how often a REST call is tried and how long to wait
// between tries.
//
// A 429 is retried on every method: the server refused the request before
// acting on it. A 5xx or a transport failure is retried on GET and DELETE
// only. POST and PATCH are never repeated after a 5xx or a dropped
// connection, because the order may already be on the book; set
// OrderRequest.ClientOrderID and look the order up instead. Nothing else is
// retried. Backoff is exponential with jitter, capped at MaxBackoff; a
// Retry-After header on a 429 sets the wait.
type RetryPolicy struct {
	// MaxAttempts is the total number of tries, including the first. Values
	// below 1 mean 1.
	MaxAttempts int
	// InitialBackoff is the wait before the first retry.
	InitialBackoff time.Duration
	// MaxBackoff caps any one wait, including one set by Retry-After.
	MaxBackoff time.Duration
	// NoJitter turns off the randomization of each wait (between 50% and
	// 100% of its value).
	NoJitter bool
}

// DefaultRetryPolicy is 3 attempts, 500 ms initial backoff doubling to a 30 s
// cap, with jitter.
var DefaultRetryPolicy = RetryPolicy{MaxAttempts: 3, InitialBackoff: 500 * time.Millisecond, MaxBackoff: 30 * time.Second}

// NoRetry tries every call once.
var NoRetry = RetryPolicy{MaxAttempts: 1}

// shouldRetry reports whether err, ending attempt number attempt, is worth
// another try.
func (p RetryPolicy) shouldRetry(err error, attempt int, idempotent bool) bool {
	if attempt >= max(p.MaxAttempts, 1) {
		return false
	}
	if errors.Is(err, ErrRateLimited) {
		return true
	}
	return idempotent && (errors.Is(err, ErrServer) || errors.Is(err, ErrTransport))
}

// backoff is the wait after attempt (1-based) failed with err.
func (p RetryPolicy) backoff(attempt int, err error) time.Duration {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests && apiErr.RetryAfter > 0 {
		if p.MaxBackoff > 0 {
			return min(apiErr.RetryAfter, p.MaxBackoff)
		}
		return apiErr.RetryAfter
	}
	d := p.InitialBackoff << (attempt - 1)
	if d < 0 || (p.MaxBackoff > 0 && d > p.MaxBackoff) {
		d = p.MaxBackoff
	}
	if !p.NoJitter && d > 0 {
		d = time.Duration(float64(d) * (0.5 + rand.Float64()*0.5))
	}
	return d
}

// parseRetryAfter reads delta-seconds or an HTTP date.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
