package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

// GenerateVerifier returns a PKCE code verifier (RFC 7636): 32 random bytes,
// base64url encoded, 43 characters.
func GenerateVerifier() string { return randomString(32) }

// S256ChallengeFromVerifier returns the S256 code challenge for verifier:
// base64url(SHA-256(verifier)). STX refuses the plain method.
func S256ChallengeFromVerifier(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// GenerateState returns an unguessable state value that ties an authorize
// redirect to its callback.
func GenerateState() string { return randomString(24) }

func randomString(n int) string {
	b := make([]byte, n)
	// crypto/rand.Read never returns an error on supported platforms.
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
