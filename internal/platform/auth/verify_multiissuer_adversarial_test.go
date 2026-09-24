package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

// signRawPayload signs a hand-written JSON payload with iss's key and kid, for
// claim shapes the typed minters cannot produce.
func signRawPayload(t *testing.T, iss *MockIssuer, payload string) string {
	t.Helper()
	header := `{"alg":"ES256","typ":"JWT","kid":"` + iss.kid + `"}`
	ss := jwt.EncodeSegment([]byte(header)) + "." + jwt.EncodeSegment([]byte(payload))
	sig, err := jwt.SigningMethodES256.Sign(ss, iss.key)
	if err != nil {
		t.Fatalf("signRawPayload: %v", err)
	}
	return ss + "." + sig
}

// rawClaims is a valid payload whose iss is issuerJSON, with extra appended verbatim.
func rawClaims(issuerJSON, extra string) string {
	now := time.Now()
	return fmt.Sprintf(`{"iss":%s,"sub":%q,"aud":"authenticated","iat":%d,"exp":%d,"role":"authenticated","app_metadata":{"tenant_id":"tenant-raw"}%s}`,
		issuerJSON, testSubject, now.Unix(), now.Add(time.Hour).Unix(), extra)
}

func unsigned(t *testing.T, c gotrueClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodNone, c).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none: %v", err)
	}
	return s
}

func TestVerify_UntrustedIssuerShapesRefusedWithoutFetch(t *testing.T) {
	a := newCountedIssuer(t, testIssuer)
	b := newCountedIssuer(t, additionalIssuer)
	v := multiVerifier(t, a, b)

	unknown := validClaims(a.iss)
	unknown.Issuer = unknownIssuer
	// Each signed token carries A's real key and kid, so only the iss gate can refuse it.
	tokens := map[string]string{
		"number iss":             signRawPayload(t, a.iss, rawClaims(`42`, "")),
		"array iss":              signRawPayload(t, a.iss, rawClaims(fmt.Sprintf(`[%q]`, testIssuer), "")),
		"null iss":               signRawPayload(t, a.iss, rawClaims(`null`, "")),
		"object iss":             signRawPayload(t, a.iss, rawClaims(fmt.Sprintf(`{"iss":%q}`, testIssuer), "")),
		"empty iss":              signRawPayload(t, a.iss, rawClaims(`""`, "")),
		"upper-case iss":         signRawPayload(t, a.iss, rawClaims(fmt.Sprintf("%q", strings.ToUpper(testIssuer)), "")),
		"trailing-slash iss":     signRawPayload(t, a.iss, rawClaims(fmt.Sprintf("%q", testIssuer+"/"), "")),
		"padded iss":             signRawPayload(t, a.iss, rawClaims(fmt.Sprintf("%q", " "+testIssuer), "")),
		"upper-case additional":  signRawPayload(t, a.iss, rawClaims(fmt.Sprintf("%q", strings.ToUpper(additionalIssuer)), "")),
		"missing iss":            signRawPayload(t, a.iss, fmt.Sprintf(`{"sub":%q,"aud":"authenticated","exp":%d,"role":"authenticated"}`, testSubject, time.Now().Add(time.Hour).Unix())),
		"unsigned, unknown iss":  unsigned(t, unknown),
		"empty token":            "",
		"two segments":           "a.b",
		"undecodable claims":     "eyJhbGciOiJFUzI1NiJ9.!!!.sig",
		"claims are not objects": jwt.EncodeSegment([]byte(`{"alg":"ES256"}`)) + "." + jwt.EncodeSegment([]byte(`[1]`)) + ".sig",
	}
	if len(tokens) == 0 {
		t.Fatal("no cases")
	}
	for name, tok := range tokens {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("err = %v, want ErrUnauthorized", err)
			}
		})
	}
	if na, nb := a.hits.count(), b.hits.count(); na != 0 || nb != 0 {
		t.Fatalf("JWKS hits after untrusted-iss tokens: primary=%d additional=%d, want 0 and 0", na, nb)
	}

	// The raw signer is sound: the same payload with the exact iss verifies and fetches.
	assertVerifies(t, v, signRawPayload(t, a.iss, rawClaims(fmt.Sprintf("%q", testIssuer), "")), "tenant-raw", "raw primary token")
	if n := a.hits.count(); n != 1 {
		t.Fatalf("primary JWKS hits = %d after a raw primary token, want 1", n)
	}
}

func TestVerify_UnsignedTokenFromTrustedIssuerRefused(t *testing.T) {
	a := newCountedIssuer(t, testIssuer)
	b := newCountedIssuer(t, additionalIssuer)
	v := multiVerifier(t, a, b)

	for _, iss := range []*MockIssuer{a.iss, b.iss} {
		if _, err := v.Verify(context.Background(), unsigned(t, validClaims(iss))); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("alg=none from %s: err = %v, want ErrUnauthorized", iss.issuer, err)
		}
	}
}

// The key set is picked by the exact "iss" key; the typed decode also matches
// "ISS" and takes the last duplicate, so only the issuer gate sees the clash.
func TestVerify_IssuerGateHoldsWhenClaimKeysDisagree(t *testing.T) {
	a := newCountedIssuer(t, testIssuer)
	b := newCountedIssuer(t, additionalIssuer)
	v := multiVerifier(t, a, b)
	issB := fmt.Sprintf("%q", additionalIssuer)

	assertVerifies(t, v, signRawPayload(t, b.iss, rawClaims(issB, "")), "tenant-raw", "raw additional token")

	for name, tok := range map[string]string{
		"ISS names the primary":         signRawPayload(t, b.iss, rawClaims(issB, fmt.Sprintf(`,"ISS":%q`, testIssuer))),
		"last duplicate iss is primary": signRawPayload(t, b.iss, rawClaims(issB, fmt.Sprintf(`,"iss":%q`, testIssuer))),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("B-signed token naming A: err = %v, want ErrUnauthorized", err)
			}
		})
	}
}

func TestVerify_ConcurrentAcrossIssuers(t *testing.T) {
	a := newCountedIssuer(t, testIssuer)
	b := newCountedIssuer(t, additionalIssuer)
	v := multiVerifier(t, a, b)
	tokA := mustMint(t, a.iss, MintOptions{Subject: testSubject, TenantID: "tenant-a"})
	tokB := mustMint(t, b.iss, MintOptions{Subject: testSubject, TenantID: "tenant-b"})

	const n = 64
	type result struct {
		want string
		id   Identity
		err  error
	}
	results := make(chan result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		tok, want := tokA, "tenant-a"
		if i%2 == 1 {
			tok, want = tokB, "tenant-b"
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := v.Verify(context.Background(), tok)
			results <- result{want, id, err}
		}()
	}
	wg.Wait()
	close(results)

	got := 0
	for r := range results {
		got++
		if r.err != nil || r.id.TenantID != r.want {
			t.Fatalf("Verify = %+v, %v; want tenant %s", r.id, r.err, r.want)
		}
	}
	if got != n {
		t.Fatalf("got %d results, want %d", got, n)
	}
	if a.hits.count() == 0 || b.hits.count() == 0 {
		t.Fatalf("JWKS hits primary=%d additional=%d, want both > 0", a.hits.count(), b.hits.count())
	}
}

func TestNewVerifier_RefusesDuplicatesThatParseAccepts(t *testing.T) {
	set, err := ParseTrustedIssuers(`[{"issuer":"urn:x","jwks_url":"https://a.example/jwks"},{"issuer":"urn:x","jwks_url":"https://b.example/jwks"}]`)
	if err != nil {
		t.Fatalf("ParseTrustedIssuers: %v", err)
	}
	if len(set) != 2 {
		t.Fatalf("parsed %d issuers, want 2", len(set))
	}
	cfg := Config{Issuer: testIssuer, JWKSURL: "https://p.example/jwks", Logger: discardLogger()}
	cfg.Additional = set[:1]
	if _, err := NewVerifier(cfg); err != nil {
		t.Fatalf("NewVerifier with one entry: %v", err)
	}
	cfg.Additional = set
	if _, err := NewVerifier(cfg); err == nil {
		t.Fatal("NewVerifier accepted a repeated additional issuer")
	}
}

func TestParseTrustedIssuers_Edges(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantLen int
		wantErr bool
	}{
		{name: "empty array", raw: `[]`},
		{name: "upper-case scheme", raw: `[{"issuer":"urn:x","jwks_url":"HTTPS://auth.example/jwks"}]`, wantLen: 1},
		{name: "surrounding whitespace", raw: " \n" + `[{"issuer":"urn:x","jwks_url":"http://auth.example/jwks"}]` + "\n ", wantLen: 1},
		{name: "object, not array", raw: `{"issuer":"urn:x","jwks_url":"https://auth.example/jwks"}`, wantErr: true},
		{name: "array of scalars", raw: `[1]`, wantErr: true},
		{name: "trailing data", raw: `[] []`, wantErr: true},
		{name: "schemeless jwks_url", raw: `[{"issuer":"urn:x","jwks_url":"auth.example/jwks"}]`, wantErr: true},
		{name: "javascript jwks_url", raw: `[{"issuer":"urn:x","jwks_url":"javascript:alert(1)"}]`, wantErr: true},
		{name: "file jwks_url", raw: `[{"issuer":"urn:x","jwks_url":"file:///etc/passwd"}]`, wantErr: true},
		{name: "unparseable jwks_url", raw: `[{"issuer":"urn:x","jwks_url":"http://[::1"}]`, wantErr: true},
		{name: "missing jwks_url", raw: `[{"issuer":"urn:x"}]`, wantErr: true},
		{name: "second entry invalid", raw: `[{"issuer":"urn:x","jwks_url":"https://a.example/jwks"},{"issuer":"urn:y","jwks_url":"ftp://b.example/jwks"}]`, wantErr: true},
		{name: "non-string issuer", raw: `[{"issuer":1,"jwks_url":"https://a.example/jwks"}]`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTrustedIssuers(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseTrustedIssuers(%q) = %+v, nil; want an error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTrustedIssuers(%q): %v", tc.raw, err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("ParseTrustedIssuers(%q) returned %d issuers, want %d", tc.raw, len(got), tc.wantLen)
			}
		})
	}
}
