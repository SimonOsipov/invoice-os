package main

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
)

// jwkEdit copies base and applies edit, so each case differs from the accepted control by one defect.
func jwkEdit(base map[string]any, edit func(k map[string]any)) map[string]any {
	k := map[string]any{}
	for n, v := range base {
		k[n] = v
	}
	edit(k)
	return k
}

func assertNoKeyInOutput(t *testing.T, stdout, stderr string, secrets ...string) {
	t.Helper()
	if len(secrets) == 0 {
		t.Fatal("no secrets to look for; the leak check would pass vacuously")
	}
	out := stdout + stderr
	for _, s := range secrets {
		if s == "" {
			t.Fatal("a secret is empty; the leak check would pass vacuously")
		}
		if strings.Contains(out, s) {
			t.Error("output contains key material")
		}
	}
	if strings.Contains(out, `"d"`) {
		t.Errorf("output contains the literal \"d\"; stdout=%q stderr=%q", stdout, stderr)
	}
	if strings.Contains(out, "panic") || strings.Contains(out, "goroutine") {
		t.Errorf("jwk-check panicked: %s", out)
	}
}

func TestJWKCheckRefusesKeysGoTrueCannotSignWith(t *testing.T) {
	good := fixtureJWK(t, "5f0c6b1e-3a2d-4c1b-9e8f-7a6b5c4d3e2f")
	other := fixtureJWK(t, "0b9e8d7c-6f5a-4e3d-8c2b-1a0f9e8d7c6b")
	secrets := []string{good["d"].(string), other["d"].(string)}

	stdout, stderr, code := runJWKCheck(t, mustJSON(t, []any{good}))
	if code != 0 {
		t.Fatalf("control refused: exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}

	one := func(edit func(k map[string]any)) string { return mustJSON(t, []any{jwkEdit(good, edit)}) }
	cases := []struct{ name, stdin string }{
		// A mismatched public point: GoTrue would sign with d but publish x/y, and every token fails verification.
		{"x/y of another key", one(func(k map[string]any) { k["x"], k["y"] = other["x"], other["y"] })},
		{"kty oct on an EC key", one(func(k map[string]any) { k["kty"] = "oct" })},
		{"crv P-384", one(func(k map[string]any) { k["crv"] = "P-384" })},
		{"crv secp256k1", one(func(k map[string]any) { k["crv"] = "secp256k1" })},
		{"RSA key claiming ES256", mustJSON(t, []any{map[string]any{
			"kty": "RSA", "alg": "ES256", "kid": "k", "key_ops": []any{"sign"},
			"n": "AQAB", "e": "AQAB", "d": good["d"],
		}})},
		{"RSA RS256 key", mustJSON(t, []any{map[string]any{
			"kty": "RSA", "alg": "RS256", "kid": "k", "key_ops": []any{"sign"},
			"n": "AQAB", "e": "AQAB", "d": good["d"],
		}})},
		{"key_ops encrypt, no sign", one(func(k map[string]any) { k["key_ops"] = []any{"encrypt", "decrypt"} })},
		{"key_ops empty", one(func(k map[string]any) { k["key_ops"] = []any{} })},
		{"key_ops missing", one(func(k map[string]any) { delete(k, "key_ops") })},
		{"key_ops a string, not a list", one(func(k map[string]any) { k["key_ops"] = "sign" })},
		{"d padded base64", one(func(k map[string]any) { k["d"] = k["d"].(string) + "=" })},
		{"d not a valid scalar", one(func(k map[string]any) { k["d"] = "AAAA" })},
		{"JWKS object, not an array", mustJSON(t, map[string]any{"keys": []any{good}})},
		{"null element", "[null]"},
		{"trailing garbage after the array", mustJSON(t, []any{good}) + "xyz"},
		{"empty array", "[]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, code := runJWKCheck(t, c.stdin)
			if code != 1 {
				t.Errorf("exit %d, want 1 (refused); stdout=%q stderr=%q", code, stdout, stderr)
			}
			assertNoKeyInOutput(t, stdout, stderr, secrets...)
		})
	}
}

// GoTrue picks the signing key by key_ops and republishes every public key with use "sig",
// so a stray use member changes nothing it does; jwk-check agrees.
func TestJWKCheckAcceptsAStrayUseMemberAsGoTrueDoes(t *testing.T) {
	good := fixtureJWK(t, "5f0c6b1e-3a2d-4c1b-9e8f-7a6b5c4d3e2f")
	for _, use := range []string{"sig", "enc"} {
		t.Run(use, func(t *testing.T) {
			stdin := mustJSON(t, []any{jwkEdit(good, func(k map[string]any) { k["use"] = use })})
			stdout, stderr, code := runJWKCheck(t, stdin)
			if code != 0 {
				t.Errorf("exit %d, want 0; stdout=%q stderr=%q", code, stdout, stderr)
			}
			assertNoKeyInOutput(t, stdout, stderr, good["d"].(string))
		})
	}
}

func TestJWKCheckHugeOrBinaryInputIsRefusedQuietly(t *testing.T) {
	d := fixtureJWK(t, "5f0c6b1e-3a2d-4c1b-9e8f-7a6b5c4d3e2f")["d"].(string)
	noise := make([]byte, 64<<10)
	if _, err := rand.Read(noise); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, stdin string }{
		{"16 MiB unterminated array", "[" + strings.Repeat("A", 16<<20)},
		{"16 MiB string holding d", `[{"d":"` + d + strings.Repeat("A", 16<<20) + `"`},
		{"random bytes", string(noise)},
		{"NUL and invalid UTF-8 around d", "\x00\xff\xfe\"d\":\"" + d + "\"\x00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, code := runJWKCheck(t, c.stdin)
			if code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
			if !strings.Contains(stdout, "jwk-check: refused:") {
				t.Errorf("stdout = %.200q, want the refusal line", stdout)
			}
			if n := len(stdout) + len(stderr); n > 200 {
				t.Errorf("jwk-check wrote %d bytes; it echoes its input", n)
			}
			assertNoKeyInOutput(t, stdout, stderr, d, strings.Repeat("A", 64))
			if bytes.ContainsRune([]byte(stdout+stderr), 0) {
				t.Error("output carries a NUL byte from the input")
			}
		})
	}
}
