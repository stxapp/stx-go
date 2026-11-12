package oauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors. An *[Error] matches the one that describes it, so test
// with [errors.Is].
var (
	// ErrAccessDenied: the member came back without connecting
	// (error=access_denied): they declined, or their STX account cannot be
	// connected. Nothing was granted.
	ErrAccessDenied = errors.New("oauth: access denied")
	// ErrAccountPending: the member's STX account is still being verified, so
	// STX issues no tokens yet. On a refresh the connection has not ended:
	// the stored tokens are kept, and the same refresh token works once the
	// member is verified. See https://docs.stxapp.io/oauth/members-being-verified/.
	ErrAccountPending = errors.New("oauth: account pending verification")
	// ErrTermsNotAccepted: the member left the STX terms page without
	// agreeing (error=terms_required on the callback). Nothing was granted. A
	// member already connected when new terms take effect gets
	// stx.ErrTermsRequired from their calls instead.
	ErrTermsNotAccepted = errors.New("oauth: terms not accepted")
	// ErrGrantRevoked: a refresh was refused with invalid_grant. The member
	// revoked the app, or a reused refresh token tripped reuse detection and
	// the whole grant was revoked. Send the member through consent again.
	ErrGrantRevoked = errors.New("oauth: grant revoked; the member must authorize again")
	// ErrInvalidState: the callback's state is unknown, used or expired.
	ErrInvalidState = errors.New("oauth: invalid state")
	// ErrNotConnected: the token store holds no tokens for the member.
	ErrNotConnected = errors.New("oauth: member not connected")
)

// Error is an OAuth protocol error: an RFC 6749 section 5.2 answer from an
// OAuth endpoint, an error carried on the authorize callback, or a check
// this package made (invalid_state, invalid_id_token). See
// https://docs.stxapp.io/oauth/discovery-and-errors/.
type Error struct {
	// Code is the RFC error code, such as "invalid_grant" or "invalid_scope".
	Code string
	// Description is the error_description, when there is one.
	Description string
	// Reason is STX's narrower reason (error_reason), such as
	// "account_pending" on an invalid_grant.
	Reason string
	// StatusCode is the HTTP status, or 0 for a check made locally.
	StatusCode int
	// Endpoint is the path that answered, such as "/oauth/token".
	Endpoint string
	// RetryAfter is the wait the endpoint asked for on a 429, or zero.
	RetryAfter time.Duration
	// RequestedScope is the scope the sign-in asked for, on an invalid_scope
	// from the authorize callback.
	RequestedScope string

	revoked bool
}

// Error returns "oauth: ENDPOINT STATUS CODE: description".
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("oauth: ")
	if e.Endpoint != "" {
		b.WriteString(e.Endpoint + " ")
	}
	if e.StatusCode != 0 {
		b.WriteString(strconv.Itoa(e.StatusCode) + " ")
	}
	b.WriteString(e.Code)
	if e.Description != "" {
		b.WriteString(": " + e.Description)
	}
	return b.String()
}

// Is matches the sentinel errors that describe e.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrAccessDenied:
		return e.Code == "access_denied"
	case ErrAccountPending:
		return e.Code == "account_pending" || e.Reason == "account_pending"
	case ErrTermsNotAccepted:
		return e.Code == "terms_required"
	case ErrGrantRevoked:
		return e.revoked
	case ErrInvalidState:
		return e.Code == "invalid_state"
	}
	return false
}

type rfcBody struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
	Reason      string `json:"error_reason"`
}

// parseRFCError reads an RFC 6749 error body, or returns nil when body is
// not one.
func parseRFCError(body []byte) *rfcBody {
	var b rfcBody
	if json.Unmarshal(body, &b) != nil || b.Error == "" {
		return nil
	}
	return &b
}

// httpError builds the *Error for a non-2xx answer.
func httpError(res *http.Response, body []byte, endpoint string) *Error {
	e := &Error{StatusCode: res.StatusCode, Endpoint: endpoint, RetryAfter: retryAfter(res)}
	if b := parseRFCError(body); b != nil {
		e.Code, e.Description, e.Reason = b.Error, b.Description, b.Reason
		return e
	}
	e.Code = "http_error"
	e.Description = truncate(strings.TrimSpace(string(body)), 300)
	return e
}

func retryAfter(res *http.Response) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(res.Header.Get("Retry-After"))); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	return 0
}

// callbackError is the error an authorize callback carries.
func callbackError(code, description, endpoint string) *Error {
	return &Error{Code: code, Description: description, StatusCode: http.StatusBadRequest, Endpoint: endpoint}
}

// isEdgeBlock reports a 403 that is not JSON: the edge in front of an
// exchange without OAuth, not the exchange itself.
func isEdgeBlock(status int, body []byte) bool {
	return status == http.StatusForbidden && !json.Valid(body)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func errorf(code, endpoint, format string, args ...any) *Error {
	return &Error{Code: code, Endpoint: endpoint, Description: fmt.Sprintf(format, args...)}
}
