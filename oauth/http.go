package oauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	stx "github.com/stxapp/stx-go"
)

// Default endpoint paths, relative to the exchange's base URL. Discovery
// supplies absolute URLs that take precedence.
const (
	DefaultAuthorizePath  = "/oauth/authorize"
	DefaultTokenPath      = "/oauth/token"
	DefaultRevokePath     = "/oauth/revoke"
	DefaultIntrospectPath = "/oauth/introspect"
	DefaultRegisterPath   = "/oauth/register"

	wellKnownAuthorizationServer = "/.well-known/oauth-authorization-server"
	wellKnownProtectedResource   = "/.well-known/oauth-protected-resource"
	wellKnownOpenID              = "/.well-known/openid-configuration"
)

var userAgent = "stx-go/" + stx.Version

// defaultHTTPClient bounds every OAuth call when the caller gives no client.
var defaultHTTPClient = &http.Client{Timeout: 30 * time.Second}

var schemeRe = regexp.MustCompile(`(?i)^https?://`)

// normalizeBaseURL turns a host or URL into "scheme://host[:port]". An empty
// value falls back to STX_HOST, then STX_BASE_URL.
func normalizeBaseURL(host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		host = strings.TrimSpace(os.Getenv("STX_HOST"))
	}
	if host == "" {
		host = strings.TrimSpace(os.Getenv("STX_BASE_URL"))
	}
	if host == "" {
		return "", fmt.Errorf("%w: oauth: a base URL is required; set Config.BaseURL or STX_HOST", stx.ErrConfig)
	}
	if !schemeRe.MatchString(host) {
		return "https://" + strings.TrimRight(host, "/"), nil
	}
	u, err := url.Parse(host)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%w: oauth: cannot parse base URL %q", stx.ErrConfig, host)
	}
	return strings.ToLower(u.Scheme) + "://" + u.Host, nil
}

// joinURL resolves path against base; absolute URLs pass through.
func joinURL(base, path string) string {
	if schemeRe.MatchString(path) {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

// pathOf returns the path of an absolute URL, or s unchanged.
func pathOf(s string) string {
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		return u.Path
	}
	return s
}

func hostOf(s string) string {
	if u, err := url.Parse(s); err == nil {
		return u.Host
	}
	return s
}

// send performs one request and reads the whole body.
func send(ctx context.Context, hc *http.Client, method, target string, body io.Reader, header http.Header) (*http.Response, []byte, error) {
	if hc == nil {
		hc = defaultHTTPClient
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("User-Agent", userAgent)
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, nil, &stx.TransportError{Method: method, Path: pathOf(target), Err: err}
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, nil, &stx.TransportError{Method: method, Path: pathOf(target), Err: err}
	}
	return res, data, nil
}

// notAvailable reports an OAuth endpoint the exchange does not have: a 404,
// or a 403 from the edge in front of it.
func notAvailable(feature, target string, res *http.Response, body []byte) error {
	if res.StatusCode != http.StatusNotFound && !isEdgeBlock(res.StatusCode, body) {
		return nil
	}
	return &stx.NotAvailableError{Feature: feature, Host: hostOf(target), Err: httpError(res, body, pathOf(target))}
}
