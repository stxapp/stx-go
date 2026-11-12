package stx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// operation is one REST route from the spec; see operations_gen.go.
type operation struct {
	ID          string
	Method      string
	Path        string
	Query       []string
	PathParams  []string
	HasBody     bool
	ResponseKey string
	Model       string
	IsList      bool
	Paginated   bool
}

// params is an ordered query string. Lists are comma-separated, never
// repeated keys; unset values are left out.
type params []param

type param struct{ key, value string }

func (p *params) str(key, v string) {
	if v != "" {
		*p = append(*p, param{key, v})
	}
}

func (p *params) list(key string, v []string) {
	if len(v) > 0 {
		*p = append(*p, param{key, strings.Join(v, ",")})
	}
}

func (p *params) boolean(key string, v *bool) {
	if v != nil {
		*p = append(*p, param{key, strconv.FormatBool(*v)})
	}
}

func (p *params) integer(key string, v int64) {
	if v != 0 {
		*p = append(*p, param{key, strconv.FormatInt(v, 10)})
	}
}

// escape percent-encodes everything except the RFC 3986 unreserved
// characters (and, when keepComma, the comma). The path is signed exactly as
// sent, so the encoding must be stable.
func escape(s string, keepComma bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9',
			c == '-', c == '.', c == '_', c == '~', keepComma && c == ',':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func (p params) encode() string {
	parts := make([]string, 0, len(p))
	for _, kv := range p {
		parts = append(parts, escape(kv.key, false)+"="+escape(kv.value, true))
	}
	return strings.Join(parts, "&")
}

// callArgs are the inputs of one REST call.
type callArgs struct {
	pathParams map[string]string
	query      params
	body       any
}

// prepared is one REST call, ready to sign and send.
type prepared struct {
	op   operation
	path string // path plus ?query; this is what gets signed
	body []byte
}

func (r prepared) idempotent() bool {
	return r.op.Method == http.MethodGet || r.op.Method == http.MethodDelete
}

// buildRequest resolves the operation, fills the path, encodes the query and
// marshals the body. A query key the operation does not declare is an error,
// which catches a method drifting from the spec.
func buildRequest(opID string, args callArgs) (prepared, error) {
	op, ok := operations[opID]
	if !ok {
		return prepared{}, fmt.Errorf("stx: unknown operation %q", opID)
	}
	path := op.Path
	for _, name := range op.PathParams {
		v := args.pathParams[name]
		if v == "" {
			return prepared{}, fmt.Errorf("stx: %s is required", name)
		}
		path = strings.Replace(path, "{"+name+"}", escape(v, false), 1)
	}
	for _, kv := range args.query {
		if !slices.Contains(op.Query, kv.key) {
			return prepared{}, fmt.Errorf("stx: %s does not take %s", opID, kv.key)
		}
	}
	if qs := args.query.encode(); qs != "" {
		path += "?" + qs
	}
	var body []byte
	if args.body != nil || (op.HasBody && op.Method != http.MethodDelete) {
		b := args.body
		if b == nil {
			b = struct{}{}
		}
		var err error
		if body, err = json.Marshal(b); err != nil {
			return prepared{}, fmt.Errorf("stx: encoding %s body: %w", opID, err)
		}
	}
	return prepared{op: op, path: path, body: body}, nil
}

// ResponseEvent describes one HTTP attempt, for [WithResponseHook].
type ResponseEvent struct {
	// OperationID is the spec's operation id, such as "orders_post".
	OperationID string
	Method      string
	// Path includes the query string, as sent.
	Path string
	// StatusCode is the HTTP status, or 0 when no response arrived.
	StatusCode int
	// Attempt is 1 for the first try, higher after a retry or a token refresh.
	Attempt  int
	Duration time.Duration
	// AuthKind is the [Auth] kind, such as "api_key".
	AuthKind string
	// RequestBody and ResponseBody are passed through unredacted. They can
	// carry order details, balances and account data: redact before storing
	// or displaying them.
	RequestBody  []byte
	ResponseBody []byte
	// Err is the error this attempt ended with, if any.
	Err error
}

// gatedOperations are routes not every exchange has yet. A router 404 on one
// of them means the feature is missing, and becomes a *NotAvailableError.
var gatedOperations = map[string]string{
	"types_get":              "market_types",
	"events__id_players_get": "event_players",
	"markets_post":           "markets.create",
	"leaderboard_get":        "leaderboard",
	"leaderboard_me_get":     "leaderboard",
	"me_profile_patch":       "leaderboard",
	"portfolio_loyalty_get":  "loyalty",
}

// featureOff is the 404 message where a gated feature is switched off on an
// exchange that has the route.
var featureOff = map[string]string{
	"leaderboard": "Leaderboard is not enabled",
	"loyalty":     "Loyalty program is not enabled",
}

// call sends one operation with retries and returns the decoded JSON object.
func (c *Client) call(ctx context.Context, opID string, args callArgs) (map[string]json.RawMessage, error) {
	req, err := buildRequest(opID, args)
	if err != nil {
		return nil, err
	}
	body, err := c.send(ctx, req)
	if err == nil {
		return body, nil
	}
	if feature, gated := gatedOperations[opID]; gated {
		var apiErr *APIError
		if errors.As(err, &apiErr) && (apiErr.missingRoute() || (apiErr.StatusCode == 404 && apiErr.Message == featureOff[feature])) {
			return nil, &NotAvailableError{Feature: feature, Host: c.host, Err: err}
		}
	}
	return nil, err
}

func (c *Client) send(ctx context.Context, req prepared) (map[string]json.RawMessage, error) {
	if c.auth == nil {
		return nil, fmt.Errorf("%w: %v", ErrConfig, errNoAuth)
	}
	attempt, sent := 1, 0
	reauthenticated := false
	for {
		sent++
		body, err := c.sendOnce(ctx, req, sent)
		if err == nil {
			return body, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized && !reauthenticated {
			if r, ok := c.auth.(Reauthenticator); ok {
				reauthenticated = true
				again, rerr := r.Reauthenticate(ctx)
				if rerr != nil {
					// A terminal reason (a revoked grant) says more than the 401.
					return nil, rerr
				}
				if again {
					continue
				}
			}
		}
		if !c.retry.shouldRetry(err, attempt, req.idempotent()) {
			return nil, err
		}
		wait := c.retry.backoff(attempt, err)
		c.logger.DebugContext(ctx, "stx: retrying", "operation", req.op.ID, "attempt", attempt, "wait", wait, "error", err)
		if serr := c.sleep(ctx, wait); serr != nil {
			return nil, err
		}
		attempt++
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// sendOnce makes one attempt. Every attempt is signed afresh: the timestamp
// must be within 30 seconds of the server clock.
func (c *Client) sendOnce(ctx context.Context, req prepared, attempt int) (result map[string]json.RawMessage, err error) {
	started := time.Now()
	var status int
	var respBody []byte
	defer func() { c.report(req, status, attempt, started, respBody, err) }()

	authHeaders, err := c.auth.AuthHeaders(ctx, req.op.Method, req.path)
	if err != nil {
		return nil, err
	}
	actx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var reader io.Reader
	if req.body != nil {
		reader = bytes.NewReader(req.body)
	}
	httpReq, err := http.NewRequestWithContext(actx, req.op.Method, c.baseURL+req.path, reader)
	if err != nil {
		return nil, err
	}
	for k, vs := range authHeaders {
		httpReq.Header[k] = vs
	}
	httpReq.Header.Set("User-Agent", c.userAgent)
	httpReq.Header.Set("Accept", "application/json")
	if req.body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, &TransportError{Method: req.op.Method, Path: req.path, Err: err}
	}
	defer resp.Body.Close()
	status = resp.StatusCode
	respBody, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, &TransportError{Method: req.op.Method, Path: req.path, Err: err}
	}
	return decodeResponse(req, resp.StatusCode, respBody, resp.Header.Get)
}

// decodeResponse returns the JSON object of a 2xx answer, or the *APIError.
func decodeResponse(req prepared, status int, body []byte, header func(string) string) (map[string]json.RawMessage, error) {
	if status < 200 || status >= 300 {
		return nil, newAPIError(status, req.op.Method, req.path, body, header)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(body, &out); err != nil || out == nil {
		return nil, &APIError{
			StatusCode: status, Method: req.op.Method, Path: req.path, Body: body,
			Message:   "expected a JSON object, got " + truncate(string(body), 200),
			RequestID: header("X-Request-Id"),
		}
	}
	return out, nil
}

func (c *Client) report(req prepared, status, attempt int, started time.Time, respBody []byte, err error) {
	c.logger.Debug("stx: request", "operation", req.op.ID, "method", req.op.Method, "path", req.path, "status", status, "attempt", attempt, "duration", time.Since(started))
	if c.onResponse == nil {
		return
	}
	defer func() { _ = recover() }() // an observer must never break the call it observes
	c.onResponse(ResponseEvent{
		OperationID: req.op.ID, Method: req.op.Method, Path: req.path, StatusCode: status,
		Attempt: attempt, Duration: time.Since(started), AuthKind: c.auth.Kind(),
		RequestBody: req.body, ResponseBody: respBody, Err: err,
	})
}

// unwrap decodes the operation's response key (or the whole body when the
// operation has none) into T.
func unwrap[T any](op operation, body map[string]json.RawMessage) (T, error) {
	var out T
	var raw []byte
	if op.ResponseKey == "" {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return out, err
		}
	} else {
		raw = body[op.ResponseKey]
	}
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("stx: decoding %s response: %w", op.ID, err)
	}
	return out, nil
}

// doOne calls opID and decodes its response key into T.
func doOne[T any](ctx context.Context, c *Client, opID string, args callArgs) (T, error) {
	body, err := c.call(ctx, opID, args)
	if err != nil {
		var zero T
		return zero, err
	}
	return unwrap[T](operations[opID], body)
}

// doPage calls a list operation and returns one page.
func doPage[T any](ctx context.Context, c *Client, opID string, query params) (*Page[T], error) {
	body, err := c.call(ctx, opID, callArgs{query: query})
	if err != nil {
		return nil, err
	}
	items, err := unwrap[[]T](operations[opID], body)
	if err != nil {
		return nil, err
	}
	var cursor string
	if raw, ok := body["cursor"]; ok {
		_ = json.Unmarshal(raw, &cursor)
	}
	return &Page[T]{Items: items, Cursor: cursor}, nil
}
