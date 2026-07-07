package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"math/big"
)

// jwks is a JSON Web Key Set as served at an issuer's
// /.well-known/jwks.json — the mock issuer's and, post-M8, Supabase GoTrue's.
type jwks struct {
	Keys []jwk `json:"keys"`
}

// jwk is a single JSON Web Key. Only the fields needed to build EC and RSA
// public keys are modeled.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg,omitempty"`
	Use string `json:"use,omitempty"`
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	Y   string `json:"y,omitempty"`
	N   string `json:"n,omitempty"`
	E   string `json:"e,omitempty"`
}

// publicKeys parses the set into kid → public key. Keys of unsupported type are
// skipped so one exotic key can't disable verification for the whole set.
func (s jwks) publicKeys() (map[string]crypto.PublicKey, error) {
	out := make(map[string]crypto.PublicKey, len(s.Keys))
	for _, k := range s.Keys {
		if k.Kid == "" {
			continue
		}
		pub, err := k.publicKey()
		if err != nil {
			return nil, err
		}
		if pub != nil {
			out[k.Kid] = pub
		}
	}
	return out, nil
}

func (k jwk) publicKey() (crypto.PublicKey, error) {
	switch k.Kty {
	case "EC":
		return k.ecPublicKey()
	case "RSA":
		return k.rsaPublicKey()
	default:
		return nil, nil // unsupported key type: skip
	}
}

func (k jwk) ecPublicKey() (*ecdsa.PublicKey, error) {
	var curve elliptic.Curve
	switch k.Crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, fmt.Errorf("auth: unsupported EC curve %q", k.Crv)
	}
	x, err := b64uint(k.X)
	if err != nil {
		return nil, err
	}
	y, err := b64uint(k.Y)
	if err != nil {
		return nil, err
	}
	return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
}

func (k jwk) rsaPublicKey() (*rsa.PublicKey, error) {
	n, err := b64uint(k.N)
	if err != nil {
		return nil, err
	}
	e, err := b64uint(k.E)
	if err != nil {
		return nil, err
	}
	if !e.IsInt64() || e.Int64() <= 0 {
		return nil, fmt.Errorf("auth: invalid RSA exponent")
	}
	return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
}

// b64uint decodes an unpadded base64url big-endian integer, as JWKs encode the
// EC coordinates and RSA modulus/exponent.
func b64uint(s string) (*big.Int, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("auth: invalid base64url in jwk: %w", err)
	}
	return new(big.Int).SetBytes(b), nil
}
