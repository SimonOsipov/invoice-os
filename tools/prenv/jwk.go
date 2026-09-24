package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/google/uuid"
)

// esJWK is the private JWK shape GoTrue reads from GOTRUE_JWT_KEYS.
type esJWK struct {
	Kty    string   `json:"kty"`
	Crv    string   `json:"crv"`
	Alg    string   `json:"alg"`
	Kid    string   `json:"kid"`
	KeyOps []string `json:"key_ops"`
	X      string   `json:"x"`
	Y      string   `json:"y"`
	D      string   `json:"d"`
}

// RunJWKES256 writes a one-element JSON array holding a fresh P-256 private
// JWK to out and returns the process exit code.
func RunJWKES256(out io.Writer) int {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		fmt.Fprintln(out, "jwk-es256: key generation failed")
		return 1
	}
	e, err := k.ECDH()
	if err != nil {
		fmt.Fprintln(out, "jwk-es256: key conversion failed")
		return 1
	}
	pub := e.PublicKey().Bytes() // 0x04 || x || y
	enc := base64.RawURLEncoding.EncodeToString
	// GoTrue signs HS256 unless alg is ES256, and needs exactly one key with "sign".
	raw, err := json.Marshal([]esJWK{{
		Kty: "EC", Crv: "P-256", Alg: "ES256", Kid: uuid.NewString(),
		KeyOps: []string{"sign", "verify"},
		X:      enc(pub[1:33]), Y: enc(pub[33:]), D: enc(e.Bytes()),
	}})
	if err != nil {
		fmt.Fprintln(out, "jwk-es256: encoding failed")
		return 1
	}
	fmt.Fprintln(out, string(raw))
	return 0
}

// RunJWKCheck reads a JWK array from in and returns 0 only for exactly one
// ES256 signing key. It never echoes the key.
func RunJWKCheck(in io.Reader, out io.Writer) int {
	if reason := jwkDefect(in); reason != "" {
		fmt.Fprintf(out, "jwk-check: refused: %s\n", reason)
		return 1
	}
	fmt.Fprintln(out, "jwk-check: ok: one ES256 signing key")
	return 0
}

// jwkDefect returns a fixed reason string; parser errors are dropped because
// they can quote the input.
func jwkDefect(in io.Reader) string {
	raw, err := io.ReadAll(in)
	if err != nil {
		return "unreadable input"
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "empty input"
	}
	// A map keeps member names exact; struct decoding folds case, and RFC 7517 names are case-sensitive.
	var keys []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return "not a JSON array of JWK objects"
	}
	if len(keys) != 1 {
		return fmt.Sprintf("%d keys, want exactly 1", len(keys))
	}
	var k esJWK
	for name, dst := range map[string]any{
		"kty": &k.Kty, "crv": &k.Crv, "alg": &k.Alg, "kid": &k.Kid,
		"key_ops": &k.KeyOps, "x": &k.X, "y": &k.Y, "d": &k.D,
	} {
		if v, ok := keys[0][name]; ok && json.Unmarshal(v, dst) != nil {
			return "a member has the wrong JSON type"
		}
	}
	switch {
	case k.Kty != "EC" || k.Crv != "P-256":
		return "not an EC P-256 key"
	case k.Alg != "ES256":
		return "alg is not ES256"
	case k.Kid == "":
		return "no kid"
	case !slices.Contains(k.KeyOps, "sign"):
		return "key_ops does not include sign"
	case k.D == "":
		return "public-only key, no private scalar"
	}
	dec := base64.RawURLEncoding.DecodeString
	d, errD := dec(k.D)
	x, errX := dec(k.X)
	y, errY := dec(k.Y)
	if errD != nil || errX != nil || errY != nil {
		return "a coordinate or the private scalar is not unpadded base64url"
	}
	priv, err := ecdh.P256().NewPrivateKey(d)
	if err != nil {
		return "the private scalar is not a valid P-256 scalar"
	}
	if !bytes.Equal(priv.PublicKey().Bytes(), append(append([]byte{4}, x...), y...)) {
		return "x/y are not the public point of the private scalar"
	}
	return ""
}
