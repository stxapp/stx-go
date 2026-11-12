package stx

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Headers carried by every signed request. See
// https://docs.stxapp.io for the signing scheme.
const (
	HeaderAccessKey       = "X-STX-ACCESS-KEY"
	HeaderAccessTimestamp = "X-STX-ACCESS-TIMESTAMP"
	HeaderAccessSignature = "X-STX-ACCESS-SIGNATURE"
)

// socketPath is the WebSocket endpoint; the handshake signs GET and this path.
const socketPath = "/socket/websocket"

// SigningMessage returns the string an API key signs: the Unix time in
// milliseconds, the uppercase HTTP method and the request path (including
// any query string), concatenated without separators.
func SigningMessage(timestampMs int64, method, path string) string {
	return strconv.FormatInt(timestampMs, 10) + strings.ToUpper(method) + path
}

// APIKey is an STX API key: the key id sent with every request, and an
// Ed25519 signer. The private key never leaves the process; only signatures
// are sent. It implements [Auth].
type APIKey struct {
	id     string
	signer crypto.Signer
	now    func() time.Time
}

// NewAPIKey builds an API key from its id and an Ed25519 private key given
// as PKCS#8 PEM text or as a path to a PEM file (a leading ~ is expanded).
func NewAPIKey(id, privateKey string) (*APIKey, error) {
	key, err := LoadPrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	return NewAPIKeyWithSigner(id, key)
}

// NewAPIKeyWithSigner builds an API key around any [crypto.Signer] that
// produces pure Ed25519 signatures, for keys held in an HSM or a cloud KMS.
// An [ed25519.PrivateKey] works as is.
func NewAPIKeyWithSigner(id string, signer crypto.Signer) (*APIKey, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, configError("API key id is required")
	}
	if signer == nil {
		return nil, configError("a signer is required")
	}
	return &APIKey{id: id, signer: signer, now: time.Now}, nil
}

// ID returns the key id.
func (k *APIKey) ID() string { return k.id }

// String returns a description that never includes key material.
func (k APIKey) String() string { return "APIKey(" + strconv.Quote(k.id) + ")" }

// GoString is what %#v prints: the key id, never key material.
func (k APIKey) GoString() string { return "stx.APIKey(" + strconv.Quote(k.id) + ")" }

// Kind returns "api_key".
func (k *APIKey) Kind() string { return "api_key" }

// Sign returns the raw Ed25519 signature over message.
func (k *APIKey) Sign(message []byte) ([]byte, error) {
	return k.signer.Sign(rand.Reader, message, crypto.Hash(0))
}

// SignedHeaders returns the three access headers for method and path at the
// given time. The server rejects a timestamp more than 30 seconds from its
// clock, so sign every request afresh.
func (k *APIKey) SignedHeaders(method, path string, at time.Time) (http.Header, error) {
	ts := at.UnixMilli()
	sig, err := k.Sign([]byte(SigningMessage(ts, method, path)))
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	h.Set(HeaderAccessKey, k.id)
	h.Set(HeaderAccessTimestamp, strconv.FormatInt(ts, 10))
	h.Set(HeaderAccessSignature, base64.StdEncoding.EncodeToString(sig))
	return h, nil
}

// AuthHeaders signs one HTTP request.
func (k *APIKey) AuthHeaders(_ context.Context, method, path string) (http.Header, error) {
	return k.SignedHeaders(method, path, k.now())
}

// WebSocketHeaders signs the WebSocket handshake: GET and the socket path,
// without the query string.
func (k *APIKey) WebSocketHeaders(_ context.Context, _ bool) (http.Header, error) {
	return k.SignedHeaders(http.MethodGet, socketPath, k.now())
}

// LoadPrivateKey parses a PKCS#8 Ed25519 private key from PEM text or from a
// path to a PEM file (a leading ~ is expanded).
func LoadPrivateKey(value string) (ed25519.PrivateKey, error) {
	var data []byte
	if strings.Contains(value, "-----BEGIN") {
		data = []byte(value)
	} else {
		path := expandHome(value)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, configError("private key not found at %s; pass the path to a PEM file or the PEM text itself", describePath(path))
		}
		if data, err = os.ReadFile(path); err != nil {
			return nil, configError("reading private key %s: %v", describePath(path), err)
		}
	}
	return ParsePrivateKeyPEM(data)
}

// ParsePrivateKeyPEM parses a PKCS#8 Ed25519 private key from PEM bytes.
func ParsePrivateKeyPEM(data []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, configError("private key is not valid PEM; expected a PKCS#8 block beginning '-----BEGIN PRIVATE KEY-----'")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, configError("private key is not valid PEM; expected a PKCS#8 block beginning '-----BEGIN PRIVATE KEY-----'")
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, configError("private key is not an Ed25519 key; STX API keys use Ed25519")
	}
	return key, nil
}

// describePath names a key path in an error, unless the value does not look
// like a path at all: key material given without its PEM armor must never be
// echoed into an error or a log.
func describePath(path string) string {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); err == nil && (dir != "." || filepath.Ext(path) != "") {
		return path
	}
	return "the given path (not shown: it does not name an existing directory and may be key material)"
}

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}

// apiKeyFromSettings returns the configured key, nil when none is configured
// at all, or an error when only half of one is.
func apiKeyFromSettings(keyID, privateKey string) (*APIKey, error) {
	switch {
	case keyID == "" && privateKey == "":
		return nil, nil
	case privateKey == "":
		return nil, configError("key id is set but no private key was given; pass it to WithKey, set STX_PRIVATE_KEY, or add private_key to the profile in ~/.stx/credentials")
	case keyID == "":
		return nil, configError("a private key was given but no key id; pass it to WithKey, set STX_KEY_ID, or add key_id to the profile in ~/.stx/credentials")
	}
	return NewAPIKey(keyID, privateKey)
}

var errNoAuth = errors.New("no API key configured: pass WithKey (or set STX_KEY_ID and STX_PRIVATE_KEY, or use a profile in ~/.stx/credentials); every request is signed")
