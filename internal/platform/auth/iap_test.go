package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

// The clock sits far from wall time, so a verifier that reads time.Now refuses every positive case.
var iapT0 = time.Unix(1_800_000_000, 0)

const (
	iapOpsAud   = "/projects/111111111111/global/backendServices/1111111111111111111"
	iapSupAud   = "/projects/111111111111/global/backendServices/2222222222222222222"
	iapOtherAud = "/projects/111111111111/global/backendServices/3333333333333333333"
	iapEmail    = "staff@ascomply.com"
	iapSub      = "accounts.google.com:118273645546372819"
)

type iapClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *iapClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *iapClock) at(d time.Duration) {
	c.mu.Lock()
	c.t = iapT0.Add(d)
	c.mu.Unlock()
}

type iapSigner struct {
	kid string
	key *ecdsa.PrivateKey
}

func newIAPSigner(t *testing.T, kid string) *iapSigner {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &iapSigner{kid: kid, key: k}
}

func iapB64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (s *iapSigner) xy() (x, y []byte) {
	return s.key.X.FillBytes(make([]byte, 32)), s.key.Y.FillBytes(make([]byte, 32))
}

func (s *iapSigner) jwk() map[string]string {
	x, y := s.xy()
	return map[string]string{"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": s.kid, "x": iapB64(x), "y": iapB64(y)}
}

func (s *iapSigner) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims(claims))
	tok.Header["kid"] = s.kid
	out, err := tok.SignedString(s.key)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// iapRaw joins a hand-built header and claims with the given signature bytes.
func iapRaw(t *testing.T, header, claims map[string]any, sig []byte) string {
	t.Helper()
	h, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	c, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return iapB64(h) + "." + iapB64(c) + "." + iapB64(sig)
}

// iapFlipSignature flips the last decoded signature byte; flipping the last base64 char could touch padding bits only.
func iapFlipSignature(t *testing.T, tok string) string {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) == 0 {
		t.Fatalf("signature does not decode: %v", err)
	}
	sig[len(sig)-1] ^= 0xff
	return parts[0] + "." + parts[1] + "." + iapB64(sig)
}

type iapJWKS struct {
	srv  *httptest.Server
	mu   sync.Mutex
	keys []map[string]string
	down bool
	gets atomic.Int32
}

func newIAPJWKS(t *testing.T, keys ...map[string]string) *iapJWKS {
	t.Helper()
	j := &iapJWKS{keys: keys}
	j.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		j.gets.Add(1)
		j.mu.Lock()
		defer j.mu.Unlock()
		if j.down {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": j.keys})
	}))
	t.Cleanup(j.srv.Close)
	return j
}

func (j *iapJWKS) set(down bool, keys ...map[string]string) {
	j.mu.Lock()
	j.down, j.keys = down, keys
	j.mu.Unlock()
}

// iapRig is one served signer, a fake clock and a verifier over {ops} unless told otherwise.
type iapRig struct {
	signer *iapSigner
	jwks   *iapJWKS
	clock  *iapClock
	v      *IAPVerifier
}

func newIAPRig(t *testing.T, auds ...string) *iapRig {
	t.Helper()
	if len(auds) == 0 {
		auds = []string{iapOpsAud}
	}
	s := newIAPSigner(t, "iap-key-1")
	r := &iapRig{signer: s, jwks: newIAPJWKS(t, s.jwk()), clock: &iapClock{}}
	r.clock.at(0)
	r.v = r.verifier(t, auds...)
	return r
}

// verifier builds another verifier over the same JWKS and clock.
func (r *iapRig) verifier(t *testing.T, auds ...string) *IAPVerifier {
	t.Helper()
	v, err := NewIAPVerifier(IAPConfig{Audiences: auds, JWKSURL: r.jwks.srv.URL, Client: r.jwks.srv.Client(), Now: r.clock.now})
	if err != nil {
		t.Fatalf("NewIAPVerifier: %v", err)
	}
	return v
}

// claims returns a fully valid claim set at the rig's current time.
func (r *iapRig) claims(aud string) map[string]any {
	now := r.clock.now()
	return map[string]any{
		"iss":   "https://cloud.google.com/iap",
		"aud":   aud,
		"sub":   iapSub,
		"email": iapEmail,
		"iat":   now.Add(-10 * time.Second).Unix(),
		"exp":   now.Add(10 * time.Minute).Unix(),
	}
}

func iapCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func iapAccepts(t *testing.T, v *IAPVerifier, tok, label string) IAPIdentity {
	t.Helper()
	id, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("%s: want accepted, got %v", label, err)
	}
	return id
}

func iapRefuses(t *testing.T, v *IAPVerifier, tok, label string) {
	t.Helper()
	if id, err := v.Verify(context.Background(), tok); err == nil {
		t.Fatalf("%s: want refused, got identity %+v", label, id)
	}
}

func TestIAPVerifier_AcceptsAWellFormedAssertion(t *testing.T) {
	r := newIAPRig(t)
	id := iapAccepts(t, r.v, r.signer.sign(t, r.claims(iapOpsAud)), "well-formed assertion")
	if id.Email != iapEmail || id.Subject != iapSub {
		t.Fatalf("identity = %+v, want email %q subject %q", id, iapEmail, iapSub)
	}
}

// Three audiences, each tried: a check of only the first or only the last would miss one.
func TestIAPVerifier_AcceptsEitherConfiguredAudience(t *testing.T) {
	r := newIAPRig(t, iapOpsAud, iapSupAud, iapOtherAud)
	for _, aud := range []string{iapOpsAud, iapSupAud, iapOtherAud} {
		iapAccepts(t, r.v, r.signer.sign(t, r.claims(aud)), "aud "+aud)
	}
}

func TestIAPVerifier_RefusesAlgNone(t *testing.T) {
	r := newIAPRig(t)
	claims := r.claims(iapOpsAud)
	none := iapRaw(t, map[string]any{"alg": "none", "typ": "JWT", "kid": r.signer.kid}, claims, nil)
	iapRefuses(t, r.v, none, "alg none")
	iapAccepts(t, r.v, r.signer.sign(t, claims), "same claims signed ES256")
}

func TestIAPVerifier_RefusesHS256WithThePublicKeyAsSecret(t *testing.T) {
	r := newIAPRig(t)
	claims := r.claims(iapOpsAud)
	x, y := r.signer.xy()
	secrets := map[string][]byte{
		"x||y":       append(append([]byte{}, x...), y...),
		"0x04||x||y": append(append([]byte{4}, x...), y...),
	}
	for name, secret := range secrets {
		tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims(claims))
		tok.Header["kid"] = r.signer.kid
		s, err := tok.SignedString(secret)
		if err != nil {
			t.Fatal(err)
		}
		iapRefuses(t, r.v, s, "HS256 secret "+name)
	}
	iapAccepts(t, r.v, r.signer.sign(t, claims), "same claims signed ES256")
}

func TestIAPVerifier_RefusesRS256(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	es := newIAPSigner(t, "iap-key-1")
	rsaJWK := map[string]string{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "rsa-1",
		"n": iapB64(rsaKey.N.Bytes()), "e": iapB64(big.NewInt(int64(rsaKey.E)).Bytes())}
	r := &iapRig{signer: es, jwks: newIAPJWKS(t, es.jwk(), rsaJWK), clock: &iapClock{}}
	r.clock.at(0)
	r.v = r.verifier(t, iapOpsAud)

	claims := r.claims(iapOpsAud)
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims(claims))
	tok.Header["kid"] = "rsa-1"
	s, err := tok.SignedString(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	iapRefuses(t, r.v, s, "RS256 under a served RSA kid")
	iapAccepts(t, r.v, es.sign(t, claims), "same claims signed ES256 by the served EC key")
}

func TestIAPVerifier_RefusesAnUnknownKidAfterOneRefetch(t *testing.T) {
	r := newIAPRig(t)
	rotated := newIAPSigner(t, "iap-key-2")
	tok := rotated.sign(t, r.claims(iapOpsAud))

	iapRefuses(t, r.v, tok, "kid absent from the set")
	if got := r.jwks.gets.Load(); got != 2 {
		t.Fatalf("JWKS GETs = %d, want 2 (initial + one forced)", got)
	}

	// Past the 30 s window the same kid, now published, is picked up by one refetch.
	r.clock.at(31 * time.Second)
	r.jwks.set(false, r.signer.jwk(), rotated.jwk())
	iapAccepts(t, r.v, rotated.sign(t, r.claims(iapOpsAud)), "kid published after the window")
	if got := r.jwks.gets.Load(); got != 3 {
		t.Fatalf("JWKS GETs = %d, want 3", got)
	}
}

func TestIAPVerifier_RefusesABadSignature(t *testing.T) {
	r := newIAPRig(t)
	good := r.signer.sign(t, r.claims(iapOpsAud))
	iapRefuses(t, r.v, iapFlipSignature(t, good), "last signature byte flipped")

	// Another key under the served kid.
	imposter := &iapSigner{kid: r.signer.kid, key: newIAPSigner(t, "x").key}
	iapRefuses(t, r.v, imposter.sign(t, r.claims(iapOpsAud)), "signed by a key the set does not serve")

	iapAccepts(t, r.v, good, "untouched signature")
}

func TestIAPVerifier_RefusesAnotherIssuer(t *testing.T) {
	r := newIAPRig(t)
	claims := r.claims(iapOpsAud)
	claims["iss"] = "https://accounts.google.com"
	iapRefuses(t, r.v, r.signer.sign(t, claims), "iss accounts.google.com")
	claims["iss"] = "https://cloud.google.com/iap"
	iapAccepts(t, r.v, r.signer.sign(t, claims), "right iss")
}

func TestIAPVerifier_RefusesAnUnconfiguredAudience(t *testing.T) {
	r := newIAPRig(t, iapOpsAud, iapSupAud)
	iapRefuses(t, r.v, r.signer.sign(t, r.claims(iapOtherAud)), "aud of a third backend service")
	iapAccepts(t, r.v, r.signer.sign(t, r.claims(iapSupAud)), "aud of a configured service")

	// The set is whatever the config says: the same token passes one verifier and not the other.
	sup := r.signer.sign(t, r.claims(iapSupAud))
	iapRefuses(t, r.verifier(t, iapOpsAud), sup, "sup token against an ops-only config")
	iapAccepts(t, r.verifier(t, iapOpsAud, iapSupAud), sup, "sup token against a config naming sup")
}

func TestIAPVerifier_RefusesAnAudienceArray(t *testing.T) {
	r := newIAPRig(t)
	claims := r.claims(iapOpsAud)
	claims["aud"] = []string{iapOpsAud}
	iapRefuses(t, r.v, r.signer.sign(t, claims), "aud as a one-element array")
	claims["aud"] = iapOpsAud
	iapAccepts(t, r.v, r.signer.sign(t, claims), "aud as a string")
}

func TestIAPVerifier_RefusesAnExpiredAssertion(t *testing.T) {
	r := newIAPRig(t)
	claims := r.claims(iapOpsAud)
	exp := time.Unix(claims["exp"].(int64), 0).Sub(iapT0)
	tok := r.signer.sign(t, claims)

	r.clock.at(exp - time.Second)
	iapAccepts(t, r.v, tok, "one second before exp")
	r.clock.at(exp + time.Second)
	iapRefuses(t, r.v, tok, "one second after exp")
}

func TestIAPVerifier_RefusesAFutureIat(t *testing.T) {
	r := newIAPRig(t)
	claims := r.claims(iapOpsAud)
	claims["iat"] = iapT0.Add(31 * time.Second).Unix()
	iapRefuses(t, r.v, r.signer.sign(t, claims), "iat 31 s ahead")
	claims["iat"] = iapT0.Add(29 * time.Second).Unix()
	iapAccepts(t, r.v, r.signer.sign(t, claims), "iat 29 s ahead")
}

func TestIAPVerifier_RefusesAnAbsentRequiredClaim(t *testing.T) {
	r := newIAPRig(t)
	for _, field := range []string{"exp", "iat", "iss", "aud"} {
		t.Run(field, func(t *testing.T) {
			claims := r.claims(iapOpsAud)
			iapAccepts(t, r.v, r.signer.sign(t, claims), field+" present")
			delete(claims, field)
			iapRefuses(t, r.v, r.signer.sign(t, claims), field+" absent")
		})
	}
}

func TestIAPVerifier_RefusesAnUnusableEmailOrSubject(t *testing.T) {
	r := newIAPRig(t)
	const tail = "@ascomply.com"
	long := func(n int) string { return strings.Repeat("a", n-len(tail)) + tail }
	cases := []struct {
		name, field, value string
		ok                 bool
	}{
		{"254-byte email", "email", long(254), true},
		{"empty email", "email", "", false},
		{"email without at", "email", "staff.ascomply.com", false},
		{"255-byte email", "email", long(255), false},
		{"empty sub", "sub", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			claims := r.claims(iapOpsAud)
			claims[c.field] = c.value
			tok := r.signer.sign(t, claims)
			if c.ok {
				iapAccepts(t, r.v, tok, c.name)
				return
			}
			iapRefuses(t, r.v, tok, c.name)
		})
	}
}

func TestIAPVerifier_RefusesMalformedTokens(t *testing.T) {
	r := newIAPRig(t)
	iapAccepts(t, r.v, r.signer.sign(t, r.claims(iapOpsAud)), "well-formed control")
	for name, tok := range map[string]string{
		"empty":               "",
		"two parts":           "a.b",
		"four parts":          "a.b.c.d",
		"non-base64 header":   "!!!.e30.c2ln",
		"header not JSON":     iapB64([]byte("notjson")) + ".e30.c2ln",
		"three empty parts":   "..",
		"whitespace only":     "   ",
		"bearer-prefixed JWT": "Bearer " + r.signer.sign(t, r.claims(iapOpsAud)),
	} {
		t.Run(name, func(t *testing.T) { iapRefuses(t, r.v, tok, name) })
	}
}

func TestParseIAPAudiences_AcceptsTheProductionShape(t *testing.T) {
	got, err := ParseIAPAudiences("  " + iapOpsAud + " ,  " + iapSupAud + "  ")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 2 || got[0] != iapOpsAud || got[1] != iapSupAud {
		t.Fatalf("got %q, want [%q %q]", got, iapOpsAud, iapSupAud)
	}
	one, err := ParseIAPAudiences(iapOpsAud)
	if err != nil || len(one) != 1 || one[0] != iapOpsAud {
		t.Fatalf("single value: got %q, err %v", one, err)
	}
}

func TestParseIAPAudiences_EmptyIsOff(t *testing.T) {
	// A shape that parses is the control: off must not be "parses nothing at all".
	if got, err := ParseIAPAudiences(iapOpsAud); err != nil || len(got) != 1 {
		t.Fatalf("control: got %q, err %v", got, err)
	}
	for _, in := range []string{"", "  "} {
		got, err := ParseIAPAudiences(in)
		if err != nil || got != nil {
			t.Fatalf("ParseIAPAudiences(%q) = %#v, %v; want nil, nil", in, got, err)
		}
	}
}

func TestParseIAPAudiences_RefusesOtherShapes(t *testing.T) {
	if _, err := ParseIAPAudiences(iapOpsAud + "," + iapSupAud); err != nil {
		t.Fatalf("control shape refused: %v", err)
	}
	for name, in := range map[string]string{
		"a URL":                 "https://example.com/projects/1/global/backendServices/1",
		"non-numeric project":   "/projects/x/global/backendServices/1",
		"non-numeric service":   "/projects/1/global/backendServices/x",
		"trailing slash":        iapOpsAud + "/",
		"no leading slash":      strings.TrimPrefix(iapOpsAud, "/"),
		"duplicate":             iapOpsAud + "," + iapOpsAud,
		"duplicate after trim":  iapOpsAud + ", " + iapOpsAud,
		"empty item":            iapOpsAud + ",," + iapSupAud,
		"trailing comma":        iapOpsAud + ",",
		"leading comma":         "," + iapOpsAud,
		"space inside a value":  "/projects/1 2/global/backendServices/1",
		"newline-separated":     iapOpsAud + "\n" + iapSupAud,
		"regional service path": "/projects/1/regions/europe-west1/backendServices/1",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := ParseIAPAudiences(in); err == nil {
				t.Fatalf("ParseIAPAudiences(%q) = %q, want an error", in, got)
			}
		})
	}
}

func TestIAPVerifier_FetchBudgetAndStaleKeys(t *testing.T) {
	r := newIAPRig(t)
	stranger := newIAPSigner(t, "iap-key-9")

	iapRefuses(t, r.v, stranger.sign(t, r.claims(iapOpsAud)), "unknown kid")
	if got := r.jwks.gets.Load(); got != 2 {
		t.Fatalf("after the first unknown kid: GETs = %d, want 2", got)
	}

	r.clock.at(5 * time.Second)
	iapRefuses(t, r.v, stranger.sign(t, r.claims(iapOpsAud)), "unknown kid again inside 30 s")
	iapAccepts(t, r.v, r.signer.sign(t, r.claims(iapOpsAud)), "known kid inside 30 s")
	if got := r.jwks.gets.Load(); got != 2 {
		t.Fatalf("inside the window: GETs = %d, want still 2", got)
	}

	// Past the window one more forced fetch is allowed; it fails and the cached keys keep serving.
	r.jwks.set(true)
	r.clock.at(31 * time.Second)
	iapRefuses(t, r.v, stranger.sign(t, r.claims(iapOpsAud)), "unknown kid after the window, JWKS down")
	if got := r.jwks.gets.Load(); got != 3 {
		t.Fatalf("after the window: GETs = %d, want 3", got)
	}
	iapAccepts(t, r.v, r.signer.sign(t, r.claims(iapOpsAud)), "known kid after a failed refetch")

	// Past the 1 h cache life the refresh is tried, fails, and the stale keys still serve.
	r.clock.at(70 * time.Minute)
	iapAccepts(t, r.v, r.signer.sign(t, r.claims(iapOpsAud)), "known kid past the cache life, JWKS down")
	if got := r.jwks.gets.Load(); got < 4 {
		t.Fatalf("past the cache life: GETs = %d, want a refresh attempt (>= 4)", got)
	}
}
