package oauth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
)

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

// splitJWT decodes a compact JWS: its header, claims, the signed input and
// the signature.
func splitJWT(token string) (jwtHeader, map[string]any, []byte, []byte, error) {
	var h jwtHeader
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return h, nil, nil, nil, errNotJWT
	}
	hb, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	cb, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	sig, err3 := base64.RawURLEncoding.DecodeString(parts[2])
	var claims map[string]any
	if err1 != nil || err2 != nil || err3 != nil || json.Unmarshal(hb, &h) != nil || json.Unmarshal(cb, &claims) != nil {
		return h, nil, nil, nil, errors.New("the ID token is malformed")
	}
	return h, claims, []byte(parts[0] + "." + parts[1]), sig, nil
}

// jwk is one published signing key (RSA or P-256).
type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

var (
	oidECPublicKey = asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
	oidP256        = asn1.ObjectIdentifier{1, 2, 840, 10045, 3, 1, 7}
)

func (k *jwk) verify(alg string, signed, sig []byte) error {
	digest := sha256.Sum256(signed)
	bad := errors.New("the ID token signature does not verify")
	switch alg {
	case "RS256":
		if k.Kty != "RSA" {
			return bad
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
			return errors.New("the published RSA key is malformed")
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		if rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig) != nil {
			return bad
		}
		return nil
	case "ES256":
		if k.Kty != "EC" || k.Crv != "P-256" || len(sig) != 64 {
			return bad
		}
		pub, err := p256Key(k.X, k.Y)
		if err != nil {
			return err
		}
		r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
		if !ecdsa.Verify(pub, digest[:], r, s) {
			return bad
		}
		return nil
	}
	return errors.New("unsupported ID token algorithm " + alg)
}

// p256Key builds the public key through its SubjectPublicKeyInfo, which also
// checks the point is on the curve.
func p256Key(xs, ys string) (*ecdsa.PublicKey, error) {
	x, err1 := base64.RawURLEncoding.DecodeString(xs)
	y, err2 := base64.RawURLEncoding.DecodeString(ys)
	malformed := errors.New("the published EC key is malformed")
	if err1 != nil || err2 != nil || len(x) != 32 || len(y) != 32 {
		return nil, malformed
	}
	point := append(append([]byte{4}, x...), y...)
	type algorithm struct {
		Algorithm asn1.ObjectIdentifier
		Curve     asn1.ObjectIdentifier
	}
	der, err := asn1.Marshal(struct {
		Algorithm algorithm
		Key       asn1.BitString
	}{algorithm{oidECPublicKey, oidP256}, asn1.BitString{Bytes: point, BitLength: len(point) * 8}})
	if err != nil {
		return nil, malformed
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, malformed
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, malformed
	}
	return ec, nil
}
