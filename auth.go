package stx

import (
	"context"
	"net/http"
)

// Auth authenticates requests. [*APIKey] (Ed25519 request signing) is the
// usual one; the oauth package provides bearer-token implementations for apps
// acting on a member's behalf. Pass one with [WithAuth].
type Auth interface {
	// Kind is a stable label for logs, such as "api_key" or "oauth".
	Kind() string
	// AuthHeaders returns the headers for one HTTP request. It is called
	// afresh for every attempt. path includes the query string, exactly as
	// sent.
	AuthHeaders(ctx context.Context, method, path string) (http.Header, error)
}

// Reauthenticator is an [Auth] that can recover from a 401, for example by
// refreshing an OAuth access token. After a 401 the client calls
// Reauthenticate once; when it returns true the request is sent again.
type Reauthenticator interface {
	Reauthenticate(ctx context.Context) (bool, error)
}

// WebSocketAuth is an [Auth] that can authenticate the WebSocket handshake.
// reconnect is true when the handshake follows a dropped connection.
type WebSocketAuth interface {
	WebSocketHeaders(ctx context.Context, reconnect bool) (http.Header, error)
}
