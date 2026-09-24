package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var uuidV4RE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// runJWKCheck feeds stdin to the built binary's `jwk-check`, the same process boundary CI uses.
func runJWKCheck(t *testing.T, stdin string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(binPath, "jwk-check")
	cmd.Stdin = strings.NewReader(stdin)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("failed to run prenv jwk-check: %v", err)
		}
		exitCode = exitErr.ExitCode()
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// generateJWK runs `prenv jwk-es256` and returns its raw stdout and the one parsed key.
func generateJWK(t *testing.T) (string, map[string]any) {
	t.Helper()
	stdout, stderr, code := runCLI(t, "jwk-es256")
	if code != 0 {
		t.Fatalf("prenv jwk-es256 exit %d, want 0; stdout=%q stderr=%q", code, stdout, stderr)
	}
	var keys []map[string]any
	if err := json.Unmarshal([]byte(stdout), &keys); err != nil {
		t.Fatalf("prenv jwk-es256 stdout is not a JSON array of objects: %v; stdout=%q", err, stdout)
	}
	if len(keys) != 1 {
		t.Fatalf("prenv jwk-es256 printed %d keys, want exactly 1", len(keys))
	}
	return stdout, keys[0]
}

func b64Field(t *testing.T, key map[string]any, name string, wantLen int) []byte {
	t.Helper()
	s, _ := key[name].(string)
	if s == "" {
		t.Fatalf("key has no %q member", name)
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("%q is not unpadded base64url: %v", name, err)
	}
	if len(raw) != wantLen {
		t.Fatalf("%q decodes to %d bytes, want %d", name, len(raw), wantLen)
	}
	return raw
}

func TestJWKES256IsASingleSigningKeyGoTrueAccepts(t *testing.T) {
	out, key := generateJWK(t)

	for member, want := range map[string]string{"kty": "EC", "crv": "P-256", "alg": "ES256"} {
		if got, _ := key[member].(string); got != want {
			t.Errorf("%s = %q, want %q", member, got, want)
		}
	}
	ops, _ := key["key_ops"].([]any)
	if !slices.Contains(ops, any("sign")) {
		t.Errorf("key_ops = %v, want it to include \"sign\" (GoTrue needs exactly one signing key)", key["key_ops"])
	}
	if kid, _ := key["kid"].(string); !uuidV4RE.MatchString(kid) {
		t.Errorf("kid = %q, want a random (v4) UUID", kid)
	}

	// d must be the private scalar for (x, y), or GoTrue signs with a key its JWKS does not publish.
	d := b64Field(t, key, "d", 32)
	x := b64Field(t, key, "x", 32)
	y := b64Field(t, key, "y", 32)
	priv, err := ecdh.P256().NewPrivateKey(d)
	if err != nil {
		t.Fatalf("d is not a valid P-256 scalar: %v", err)
	}
	if want := append(append([]byte{4}, x...), y...); !bytes.Equal(priv.PublicKey().Bytes(), want) {
		t.Error("x/y are not the public point of d")
	}

	stdout, stderr, code := runJWKCheck(t, out)
	if code != 0 {
		t.Errorf("jwk-check refused jwk-es256's own output: exit %d, stdout=%q stderr=%q", code, stdout, stderr)
	}
	if dv := key["d"].(string); strings.Contains(stdout+stderr, dv) {
		t.Error("jwk-check echoed the private scalar d")
	}
}

func TestJWKES256IsFreshEachRun(t *testing.T) {
	_, a := generateJWK(t)
	_, b := generateJWK(t)
	for _, member := range []string{"kid", "d"} {
		av, _ := a[member].(string)
		bv, _ := b[member].(string)
		if av == "" || bv == "" {
			t.Fatalf("%s missing from a run (%q, %q)", member, av, bv)
		}
		if av == bv {
			t.Errorf("two runs printed the same %s; each run must generate a fresh key", member)
		}
	}
}

// fixtureJWK is a valid ES256 signing key built in-process; no private key is committed.
func fixtureJWK(t *testing.T, kid string) map[string]any {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e, err := k.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	pub := e.PublicKey().Bytes()
	enc := base64.RawURLEncoding.EncodeToString
	return map[string]any{
		"kty": "EC", "crv": "P-256", "alg": "ES256", "kid": kid,
		"key_ops": []any{"sign", "verify"},
		"x":       enc(pub[1:33]), "y": enc(pub[33:]), "d": enc(e.Bytes()),
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestJWKCheckRefusals(t *testing.T) {
	good := fixtureJWK(t, "5f0c6b1e-3a2d-4c1b-9e8f-7a6b5c4d3e2f")
	second := fixtureJWK(t, "0b9e8d7c-6f5a-4e3d-8c2b-1a0f9e8d7c6b")
	dVals := []string{good["d"].(string), second["d"].(string)}

	with := func(edit func(k map[string]any)) map[string]any {
		k := map[string]any{}
		for n, v := range good {
			k[n] = v
		}
		edit(k)
		return k
	}

	// Each refusal differs from the accepted control by one defect.
	cases := []struct{ name, stdin string }{
		{"empty input", ""},
		{"not an array", mustJSON(t, good)},
		{"two signing keys", mustJSON(t, []any{good, second})},
		{"no signing key", mustJSON(t, []any{with(func(k map[string]any) { k["key_ops"] = []any{"verify"} })})},
		{"missing alg", mustJSON(t, []any{with(func(k map[string]any) { delete(k, "alg") })})},
		{"alg HS256", mustJSON(t, []any{with(func(k map[string]any) { k["alg"] = "HS256" })})},
		{"missing kid", mustJSON(t, []any{with(func(k map[string]any) { delete(k, "kid") })})},
		{"public-only key", mustJSON(t, []any{with(func(k map[string]any) { delete(k, "d") })})},
	}
	if len(cases) != 8 {
		t.Fatalf("%d refusal cases, want the 8 the acceptance criterion lists", len(cases))
	}

	noLeak := func(t *testing.T, stdout, stderr string) {
		t.Helper()
		for _, d := range dVals {
			if d == "" {
				t.Fatal("fixture d is empty; the leak check would pass vacuously")
			}
			if strings.Contains(stdout+stderr, d) {
				t.Error("output contains a private scalar d")
			}
		}
		if strings.Contains(stdout+stderr, `"d"`) {
			t.Errorf("output contains the literal \"d\"; stdout=%q stderr=%q", stdout, stderr)
		}
	}

	t.Run("control: one valid signing key is accepted", func(t *testing.T) {
		stdout, stderr, code := runJWKCheck(t, mustJSON(t, []any{good}))
		if code != 0 {
			t.Errorf("exit %d, want 0; stdout=%q stderr=%q", code, stdout, stderr)
		}
		noLeak(t, stdout, stderr)
	})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, code := runJWKCheck(t, c.stdin)
			if code != 1 {
				t.Errorf("exit %d, want 1 (refused); stdout=%q stderr=%q", code, stdout, stderr)
			}
			noLeak(t, stdout, stderr)
		})
	}
}
