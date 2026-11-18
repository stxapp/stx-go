package stx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
)

// Fill modes for a limit order; see OrderRequest.FillMode.
const (
	// FillNormal matches what it can and rests the rest. The default.
	FillNormal = "normal"
	// PostOnly rests only as a maker. When it would match on arrival the
	// exchange does not place it and [OrdersService.Place] returns a
	// *[PostOnlyWouldMatchError].
	PostOnly = "post_only"
	// FillOrKill fills in full or not at all. The placement answers
	// "accepted" (or "delayed"); the order then ends "filled", or "cancelled"
	// with nothing filled.
	FillOrKill = "fill_or_kill"
)

// fillModeFeature is the feature id of a fill mode, such as
// "orders.post_only".
func fillModeFeature(mode string) string { return "orders." + mode }

// specialFillMode reports whether mode asks for something other than a
// normal order.
func specialFillMode(mode string) bool { return mode != "" && mode != FillNormal }

// apiVersion is what GET /api_version answered. features is nil for a build
// without the list, and for an exchange without the route.
type apiVersion struct {
	version  string
	features []string
}

type apiVersionCache struct {
	mu sync.Mutex
	v  *apiVersion
}

// apiVersionInfo returns GET /api_version, cached on the client. A 200 or a
// 404 is kept until refresh; any other answer is an error and is asked again
// next time.
func (c *Client) apiVersionInfo(ctx context.Context, refresh bool) (*apiVersion, error) {
	cache := &c.apiVersion
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.v != nil && !refresh {
		return cache.v, nil
	}
	v, err := c.fetchAPIVersion(ctx)
	if err != nil {
		return nil, err
	}
	cache.v = v
	return v, nil
}

func (c *Client) forgetAPIVersion() {
	c.apiVersion.mu.Lock()
	c.apiVersion.v = nil
	c.apiVersion.mu.Unlock()
}

func (c *Client) fetchAPIVersion(ctx context.Context) (*apiVersion, error) {
	const path = "/api_version"
	actx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &TransportError{Method: http.MethodGet, Path: path, Err: err}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, &TransportError{Method: http.MethodGet, Path: path, Err: err}
	}
	if res.StatusCode == http.StatusNotFound {
		// An exchange from before /api_version has none of the newer features.
		return &apiVersion{}, nil
	}
	if res.StatusCode == http.StatusForbidden && res.Header.Get("X-Stx-Error") == "" && !json.Valid(body) {
		// The exchange's firewall does not let /api_version through, so it
		// publishes no features: none is assumed rather than sending a mode
		// that might be dropped.
		return &apiVersion{}, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, newAPIError(res.StatusCode, http.MethodGet, path, body, res.Header.Get)
	}
	var doc struct {
		APIVersion string   `json:"api_version"`
		Features   []string `json:"features"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, &APIError{StatusCode: res.StatusCode, Method: http.MethodGet, Path: path, Body: body,
			Message: "expected a JSON object, got " + truncate(string(body), 200)}
	}
	// No features field: a build from before the list, so none of the
	// listed features.
	return &apiVersion{version: doc.APIVersion, features: doc.Features}, nil
}

// SupportsFillMode reports whether this exchange takes mode ([PostOnly],
// [FillOrKill]) on a limit order: GET /api_version lists "order_<mode>" in
// its features. An exchange whose answer has no features, or that does not
// serve /api_version (a 404, or a 403 from its firewall with no JSON), takes
// neither. [FillNormal] is always taken. The answer is
// cached on the client; pass refresh to ask again. An error means the
// exchange could not be asked.
func (c *Client) SupportsFillMode(ctx context.Context, mode string, refresh bool) (bool, error) {
	if !specialFillMode(mode) {
		return true, nil
	}
	v, err := c.apiVersionInfo(ctx, refresh)
	if err != nil {
		return false, err
	}
	return slices.Contains(v.features, "order_"+mode), nil
}

// requireFillMode returns a *NotAvailableError unless the exchange takes
// mode.
func (c *Client) requireFillMode(ctx context.Context, mode string) error {
	ok, err := c.SupportsFillMode(ctx, mode, false)
	if err != nil {
		return fmt.Errorf("stx: checking whether %s takes %s orders: %w", c.host, mode, err)
	}
	if !ok {
		return &NotAvailableError{Feature: fillModeFeature(mode), Host: c.host}
	}
	return nil
}

// fillModeError makes a fill-mode refusal of an order POST specific, or
// returns nil. A mode switched off on the exchange is a 400 "fill_mode
// post_only is not enabled"; a market that does not take it is a 422 "This
// market does not accept the post_only fill mode"; a post-only order whose
// first match is the member's own order is a 422 match_with_self.
func (c *Client) fillModeError(err error, mode string) error {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return nil
	}
	switch {
	case apiErr.StatusCode == 400 && apiErr.Message == "fill_mode "+mode+" is not enabled":
		c.forgetAPIVersion()
		return &NotAvailableError{Feature: fillModeFeature(mode), Host: c.host, Err: err}
	case apiErr.StatusCode == 422 && mode == PostOnly && strings.Contains(apiErr.Message, "would match with your other order"):
		return &PostOnlyWouldMatchError{Reason: "match_with_self", Err: err}
	}
	// A 422 naming the fill mode already matches ErrFillModeNotAllowed.
	return nil
}

// PostOnlyWouldMatchError reports a [PostOnly] order that would have matched
// on arrival, so the exchange did not rest it. Nothing traded and the book is
// unchanged; re-price and place again. It matches [ErrPostOnlyWouldMatch]
// and [ErrRejected].
type PostOnlyWouldMatchError struct {
	// Reason is "auto_matching" when it would have matched another member's
	// order: the exchange cancelled it and Order is the cancelled order. It is
	// "match_with_self" when the first order it would have matched is your
	// own: the exchange rejected it (a 422) and Order is nil.
	Reason string
	// Order is the cancelled order, for "auto_matching".
	Order *Order
	// Err is the 422, for "match_with_self".
	Err error
}

// Error describes the refusal.
func (e *PostOnlyWouldMatchError) Error() string {
	if e.Reason == "match_with_self" {
		return "stx: the post-only order would have matched your own order, so the exchange rejected it"
	}
	return "stx: the post-only order would have matched on arrival, so the exchange cancelled it; nothing traded"
}

// Is matches [ErrPostOnlyWouldMatch] and [ErrRejected].
func (e *PostOnlyWouldMatchError) Is(target error) bool {
	return target == ErrPostOnlyWouldMatch || target == ErrRejected
}

// Unwrap returns the 422, if any.
func (e *PostOnlyWouldMatchError) Unwrap() error { return e.Err }
