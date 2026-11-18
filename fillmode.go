package stx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
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

// SupportsFillMode no longer checks anything and always returns true and a
// nil error, without a request.
//
// Deprecated: the exchange no longer publishes which fill modes it takes.
// Send the order with the mode and handle the answer: where the mode is not
// enabled the exchange rejects the order and the call returns an error
// matching [ErrNotAvailable]. An exchange on a release from before fill modes
// may instead place the order as a normal limit order; check
// [Order.FillMode] on the placed order. mode and refresh are ignored.
func (c *Client) SupportsFillMode(ctx context.Context, mode string, refresh bool) (bool, error) {
	return true, nil
}

// apiVersion is the build GET /api_version names, "" when the exchange
// does not serve the route.
type apiVersion struct {
	version string
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
		return &apiVersion{}, nil
	}
	if res.StatusCode == http.StatusForbidden && res.Header.Get("X-Stx-Error") == "" && !json.Valid(body) {
		// The exchange's firewall does not let /api_version through.
		return &apiVersion{}, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, newAPIError(res.StatusCode, http.MethodGet, path, body, res.Header.Get)
	}
	var doc struct {
		APIVersion string `json:"api_version"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, &APIError{StatusCode: res.StatusCode, Method: http.MethodGet, Path: path, Body: body,
			Message: "expected a JSON object, got " + truncate(string(body), 200)}
	}
	return &apiVersion{version: doc.APIVersion}, nil
}

// fillModeError makes a fill-mode refusal of an order POST specific, or
// returns nil. A mode the exchange has not enabled is a 400 "fill_mode
// post_only is not enabled", and becomes a *[NotAvailableError]; a market that does not take it is a 422 "This
// market does not accept the post_only fill mode"; a post-only order whose
// first match is the member's own order is a 422 match_with_self.
func (c *Client) fillModeError(err error, mode string) error {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return nil
	}
	switch {
	case (apiErr.StatusCode == 400 || apiErr.StatusCode == 422) && notEnabled(apiErr.Message, mode):
		return &NotAvailableError{Feature: fillModeFeature(mode), Host: c.host, Err: err}
	case apiErr.StatusCode == 422 && mode == PostOnly && strings.Contains(apiErr.Message, "would match with your other order"):
		return &PostOnlyWouldMatchError{Reason: "match_with_self", Err: err}
	}
	// A 422 naming the fill mode already matches ErrFillModeNotAllowed.
	return nil
}

// notEnabled reports whether msg is the exchange saying mode is not enabled.
func notEnabled(msg, mode string) bool {
	return msg == "fill_mode "+mode+" is not enabled" || strings.HasPrefix(msg, "The "+mode+" fill mode is not enabled")
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
