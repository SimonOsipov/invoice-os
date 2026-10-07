package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	sidIssuer = "https://gotrue.ascomply.test/auth/v1"
	subjectS1 = "5b0e7c1a-3f2d-4e8a-9c61-1d2e3f4a5b6c"
	subjectS2 = "8d4c2b1a-6e5f-4a3b-8c7d-9e0f1a2b3c4d"
	sid1      = "00a6daf2-8c5a-46d0-a9c5-cd9bf4fa2e08"
	sid2      = "1f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5b"

	msgSessionUnavailable = "session check unavailable"
)

// GoTrue v2.197.0 GET /user answers (internal/api/auth.go requireAuthentication, internal/api/user.go).
const gtUser = `{"id":"5b0e7c1a-3f2d-4e8a-9c61-1d2e3f4a5b6c","aud":"authenticated","role":"authenticated","email":"ada@corp.example"}`

func gtError(status int, code string) string {
	return fmt.Sprintf(`{"code":%d,"error_code":%q,"msg":"refused"}`, status, code)
}

// sidSigner is a second trusted issuer whose tokens carry session_id, as GoTrue's do;
// the mock issuer never mints one.
type sidSigner struct {
	kid string
	key *ecdsa.PrivateKey
	url string
}

func newSidSigner(t *testing.T) *sidSigner {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	s := &sidSigner{kid: "sid-signer", key: key}
	b64 := func(n interface{ FillBytes([]byte) []byte }) string {
		return base64.RawURLEncoding.EncodeToString(n.FillBytes(make([]byte, 32)))
	}
	set := fmt.Sprintf(`{"keys":[{"kty":"EC","crv":"P-256","alg":"ES256","use":"sig","kid":%q,"x":%q,"y":%q}]}`,
		s.kid, b64(key.X), b64(key.Y))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, set)
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

// token signs a GoTrue-shaped token; an empty sid omits the claim.
func (s *sidSigner) token(t *testing.T, sub, sid string) string {
	t.Helper()
	now := time.Now()
	c := jwt.MapClaims{
		"iss":          sidIssuer,
		"sub":          sub,
		"aud":          "authenticated",
		"iat":          now.Unix(),
		"exp":          now.Add(time.Hour).Unix(),
		"role":         testRole,
		"app_metadata": map[string]any{"tenant_id": testTenant},
	}
	if sid != "" {
		c["session_id"] = sid
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, c)
	tok.Header["kid"] = s.kid
	out, err := tok.SignedString(s.key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return out
}

// userFake is GoTrue's /user; answer may be swapped mid-test. Only GET /user is counted.
type userFake struct {
	URL    *url.URL
	hits   atomic.Int64
	mu     sync.Mutex
	auths  []string
	answer http.HandlerFunc
}

func newUserFake(t *testing.T, status int, body string) *userFake {
	t.Helper()
	f := &userFake{}
	f.set(status, body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/user" {
			f.hits.Add(1)
			f.mu.Lock()
			f.auths = append(f.auths, r.Header.Get("Authorization"))
			f.mu.Unlock()
		}
		f.mu.Lock()
		answer := f.answer
		f.mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse fake url: %v", err)
	}
	f.URL = u
	return f
}

func (f *userFake) set(status int, body string) {
	f.setAnswer(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

func (f *userFake) setAnswer(h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer = h
}

func (f *userFake) Hits() int { return int(f.hits.Load()) }

func (f *userFake) Auths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auths...)
}

// sessionUpstream is a goroutine-safe recording tenancy service.
type sessionUpstream struct {
	hits   atomic.Int64
	mu     sync.Mutex
	header http.Header
	method string
	path   string
}

func (u *sessionUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.hits.Add(1)
	u.mu.Lock()
	u.header = r.Header.Clone()
	u.method, u.path = r.Method, r.URL.Path
	u.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (u *sessionUpstream) Hits() int { return int(u.hits.Load()) }

func (u *sessionUpstream) Last() (method, path string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.method, u.path
}

func (u *sessionUpstream) Header() http.Header {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.header
}

type sessionRig struct {
	handler  http.Handler
	sessions *SessionChecker
	clock    *testClock
	signer   *sidSigner
	mock     *auth.MockIssuer
	upstream *sessionUpstream
	other    *sessionUpstream // portfolio, to prove a near miss reaches no other service
}

// newSessionRig builds the /api/ handler with a checker against authURL (nil allowed).
// A nil client is testClient(); a nil log discards.
func newSessionRig(t *testing.T, authURL *url.URL, client *http.Client, log *slog.Logger) *sessionRig {
	t.Helper()
	if client == nil {
		client = testClient()
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	mock, err := auth.NewMockIssuer(testIssuer)
	if err != nil {
		t.Fatalf("mock issuer: %v", err)
	}
	mockJWKS := httptest.NewServer(mock.JWKSHandler())
	t.Cleanup(mockJWKS.Close)
	signer := newSidSigner(t)
	verifier, err := auth.NewVerifier(auth.Config{
		Issuer:     testIssuer,
		JWKSURL:    mockJWKS.URL,
		Additional: []auth.TrustedIssuer{{Issuer: sidIssuer, JWKSURL: signer.url}},
		Logger:     log,
	})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	up := &sessionUpstream{}
	upSrv := httptest.NewServer(up)
	t.Cleanup(upSrv.Close)
	upURL, _ := url.Parse(upSrv.URL)
	other := &sessionUpstream{}
	otherSrv := httptest.NewServer(other)
	t.Cleanup(otherSrv.Close)
	otherURL, _ := url.Parse(otherSrv.URL)

	clk := newTestClock()
	sessions := NewSessionChecker(authURL, client, clk.Now, log)
	return &sessionRig{
		handler: Handler(Options{
			Verifier:  verifier,
			Sessions:  sessions,
			Upstreams: map[string]*url.URL{"tenancy": upURL, "portfolio": otherURL},
			Logger:    log,

			GatewayToken: testGatewayToken,
		}),
		sessions: sessions,
		clock:    clk,
		signer:   signer,
		mock:     mock,
		upstream: up,
		other:    other,
	}
}

func (rg *sessionRig) get(bearer string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	rg.handler.ServeHTTP(rec, request(http.MethodGet, "/api/tenancy/v1/me", bearer))
	return rec
}

// refusal is the verifier's own 401 body, the bytes a revoked session must answer with.
func (rg *sessionRig) refusal(t *testing.T) []byte {
	t.Helper()
	rec := rg.get("")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-token request answered %d, want the verifier's 401", rec.Code)
	}
	return rec.Body.Bytes()
}

// liveSessions is a checker against a GoTrue that answers every /user with 200.
func liveSessions(t *testing.T) *SessionChecker {
	t.Helper()
	f := newUserFake(t, http.StatusOK, gtUser)
	return NewSessionChecker(f.URL, testClient(), time.Now, slog.New(slog.DiscardHandler))
}

func assertRevoked(t *testing.T, rec *httptest.ResponseRecorder, refusal []byte) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), refusal) {
		t.Errorf("body = %q, want the verifier's refusal %q", rec.Body.String(), refusal)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] != "unauthorized" {
		t.Errorf("body = %q, want {\"error\":\"unauthorized\"}", rec.Body.String())
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Errorf("WWW-Authenticate = %q, want Bearer", got)
	}
}

func assertUnavailable(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] != msgSessionUnavailable {
		t.Errorf("body = %q, want {\"error\":%q}", rec.Body.String(), msgSessionUnavailable)
	}
}

// waitFor polls cond until it holds or d passes.
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

func TestSessionCheck_RevokedNeverReachesUpstream(t *testing.T) {
	fake := newUserFake(t, http.StatusForbidden, gtError(403, "session_not_found"))
	rg := newSessionRig(t, fake.URL, nil, nil)
	refusal := rg.refusal(t)

	rec := rg.get(rg.signer.token(t, subjectS1, sid1))

	assertRevoked(t, rec, refusal)
	if n := rg.upstream.Hits(); n != 0 {
		t.Errorf("upstream received %d requests, want 0", n)
	}
	if n := fake.Hits(); n != 1 {
		t.Errorf("GoTrue /user saw %d calls, want 1", n)
	}
}

func TestSessionCheck_LiveSessionPassesThrough(t *testing.T) {
	fake := newUserFake(t, http.StatusOK, gtUser)
	rg := newSessionRig(t, fake.URL, nil, nil)
	tok := rg.signer.token(t, subjectS1, sid1)

	rec := rg.get(tok)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if n := rg.upstream.Hits(); n != 1 {
		t.Fatalf("upstream received %d requests, want 1", n)
	}
	h := rg.upstream.Header()
	if h.Get(headerUserID) != subjectS1 || h.Get(headerTenantID) != testTenant || h.Get(headerUserRole) != testRole {
		t.Errorf("identity headers = (%q, %q, %q), want (%q, %q, %q)",
			h.Get(headerUserID), h.Get(headerTenantID), h.Get(headerUserRole), subjectS1, testTenant, testRole)
	}
	auths := fake.Auths()
	if len(auths) != 1 {
		t.Fatalf("GoTrue saw %d GET /user calls, want exactly 1", len(auths))
	}
	if auths[0] != "Bearer "+tok {
		t.Errorf("GoTrue /user Authorization = %q, want the caller's bearer", auths[0])
	}
}

// GoTrue matches ^bearer (\S+$); a header the verifier accepts in another form must still reach it canonical.
func TestSessionCheck_SendsANormalizedBearer(t *testing.T) {
	for name, header := range map[string]string{
		"lowercase, two spaces": "bearer  %s",
		"trailing space":        "Bearer %s ",
	} {
		t.Run(name, func(t *testing.T) {
			fake := newUserFake(t, http.StatusOK, gtUser)
			rg := newSessionRig(t, fake.URL, nil, nil)
			tok := rg.signer.token(t, subjectS1, sid1)
			fake.setAnswer(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Header.Get("Authorization") != "Bearer "+tok {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = io.WriteString(w, gtError(401, "no_authorization"))
					return
				}
				_, _ = io.WriteString(w, gtUser)
			})
			req := httptest.NewRequest(http.MethodGet, "/api/tenancy/v1/me", nil)
			req.Header.Set("Authorization", fmt.Sprintf(header, tok))
			rec := httptest.NewRecorder()

			rg.handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK || rg.upstream.Hits() != 1 {
				t.Errorf("status = %d, upstream hits = %d, want 200 and 1: %s", rec.Code, rg.upstream.Hits(), rec.Body.String())
			}
			if auths := fake.Auths(); len(auths) != 1 || auths[0] != "Bearer "+tok {
				t.Errorf("GoTrue /user Authorization = %q, want exactly [\"Bearer <token>\"]", auths)
			}
		})
	}
}

// The checker speaks to GoTrue as the caller, never as the gateway: GoTrue is not a peer of the seven.
func TestSessionCheck_SendsGoTrueNoGatewayCredential(t *testing.T) {
	fake := newUserFake(t, http.StatusOK, gtUser)
	var mu sync.Mutex
	var seen []http.Header
	fake.setAnswer(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, gtUser)
	})
	rg := newSessionRig(t, fake.URL, nil, nil)
	tok := rg.signer.token(t, subjectS1, sid1)

	if rec := rg.get(tok); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	if got := rg.upstream.Header().Get("X-Gateway-Token"); got != testGatewayToken {
		t.Fatalf("tenancy saw X-Gateway-Token %q, want %q -- the request never went through the signing proxy", got, testGatewayToken)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("GoTrue /user saw %d call(s), want 1", len(seen))
	}
	for _, name := range []string{"X-Gateway-Token", "X-S2S-Token", "X-Tenant-ID", "X-User-ID", "X-User-Role"} {
		if got := seen[0].Values(name); len(got) != 0 {
			t.Errorf("GoTrue /user saw %s = %q, want none", name, got)
		}
	}
}

func TestSessionCheck_NoSessionIDSkipsGoTrue(t *testing.T) {
	fake := newUserFake(t, http.StatusForbidden, gtError(403, "session_not_found"))
	rg := newSessionRig(t, fake.URL, nil, nil)
	tok, err := rg.mock.Mint(auth.MintOptions{Subject: testSubject, Role: testRole, TenantID: testTenant})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	rec := rg.get(tok)

	if rec.Code != http.StatusOK || rg.upstream.Hits() != 1 {
		t.Fatalf("status = %d, upstream hits = %d, want 200 and 1", rec.Code, rg.upstream.Hits())
	}
	if n := fake.Hits(); n != 0 {
		t.Errorf("GoTrue /user saw %d calls for a token without session_id, want 0", n)
	}
}

func TestSessionCheck_TTLBoundary(t *testing.T) {
	fake := newUserFake(t, http.StatusOK, gtUser)
	rg := newSessionRig(t, fake.URL, nil, nil)
	tok := rg.signer.token(t, subjectS1, sid1)

	steps := []struct {
		at   time.Duration
		want int
	}{
		{0, 1},
		// The literal, not SessionCheckTTL: 30 s is the stale window the docs promise.
		{30*time.Second - time.Nanosecond, 1},
		{30 * time.Second, 2},
	}
	t0 := rg.clock.Now()
	for _, s := range steps {
		rg.clock.Advance(t0.Add(s.at).Sub(rg.clock.Now()))
		if rec := rg.get(tok); rec.Code != http.StatusOK {
			t.Errorf("t0+%v: status = %d, want 200", s.at, rec.Code)
		}
		if n := fake.Hits(); n != s.want {
			t.Errorf("t0+%v: GoTrue /user calls = %d, want %d", s.at, n, s.want)
		}
	}
}

func TestSessionCheck_RevokedIsCached(t *testing.T) {
	fake := newUserFake(t, http.StatusForbidden, gtError(403, "session_not_found"))
	rg := newSessionRig(t, fake.URL, nil, nil)
	refusal := rg.refusal(t)
	tok := rg.signer.token(t, subjectS1, sid1)

	assertRevoked(t, rg.get(tok), refusal)
	rg.clock.Advance(time.Second)
	assertRevoked(t, rg.get(tok), refusal)

	if n := fake.Hits(); n != 1 {
		t.Errorf("GoTrue /user calls = %d, want 1 (the revoked verdict is cached)", n)
	}
	if n := rg.upstream.Hits(); n != 0 {
		t.Errorf("upstream received %d requests, want 0", n)
	}
}

func TestSessionCheck_ColdMissesShareOneCall(t *testing.T) {
	fake := newUserFake(t, http.StatusOK, gtUser)
	release := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	t.Cleanup(open)
	fake.setAnswer(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, gtUser)
	})
	rg := newSessionRig(t, fake.URL, nil, nil)
	tok := rg.signer.token(t, subjectS1, sid1)

	const n = 10
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = rg.get(tok).Code
		}()
	}
	called := waitFor(2*time.Second, func() bool { return fake.Hits() >= 1 })
	// Without sharing, the other nine reach GoTrue well inside this window.
	waitFor(300*time.Millisecond, func() bool { return fake.Hits() > 1 })
	open()
	wg.Wait()

	if !called {
		t.Errorf("GoTrue /user was never called for a cold session_id")
	}
	if got := fake.Hits(); got != 1 {
		t.Errorf("GoTrue /user calls = %d for %d concurrent cold requests, want 1", got, n)
	}
	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("request %d: status = %d, want 200", i, c)
		}
	}
	if got := rg.upstream.Hits(); got != n {
		t.Errorf("upstream received %d requests, want %d", got, n)
	}
}

func TestSessionCheck_GoneCodesAreRevoked(t *testing.T) {
	cases := []struct {
		status int
		code   string
	}{
		{http.StatusForbidden, "session_not_found"},
		{http.StatusForbidden, "user_not_found"},
		{http.StatusForbidden, "user_banned"},
		{http.StatusUnauthorized, "session_expired"},
		{http.StatusForbidden, "session_expired"},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%d %s", c.status, c.code), func(t *testing.T) {
			fake := newUserFake(t, c.status, gtError(c.status, c.code))
			rg := newSessionRig(t, fake.URL, nil, nil)
			refusal := rg.refusal(t)
			tok := rg.signer.token(t, subjectS1, sid1)

			assertRevoked(t, rg.get(tok), refusal)
			rg.clock.Advance(time.Second)
			assertRevoked(t, rg.get(tok), refusal)

			if n := fake.Hits(); n != 1 {
				t.Errorf("GoTrue /user calls = %d, want 1 (cached)", n)
			}
			if n := rg.upstream.Hits(); n != 0 {
				t.Errorf("upstream received %d requests, want 0", n)
			}
		})
	}
}

func TestSessionCheck_OtherAnswersAre503(t *testing.T) {
	plain := func(status int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		}
	}
	cases := []struct {
		name   string
		answer http.HandlerFunc
	}{
		{"403 bad_jwt", plain(403, gtError(403, "bad_jwt"))},
		{"400 validation_failed", plain(400, gtError(400, "validation_failed"))},
		{"401 no_authorization", plain(401, gtError(401, "no_authorization"))},
		{"404", plain(404, "404 page not found\n")},
		// A followed redirect would reach a 200.
		{"302", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/user" {
				plain(200, gtUser)(w, r)
				return
			}
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		}},
		{"204", plain(204, "")},
		{"403 without error_code", plain(403, `{"code":403,"msg":"refused"}`)},
		{"403 not JSON", plain(403, "<html>forbidden</html>")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := newUserFake(t, http.StatusOK, gtUser)
			fake.setAnswer(c.answer)
			rg := newSessionRig(t, fake.URL, nil, nil)
			tok := rg.signer.token(t, subjectS1, sid1)

			assertUnavailable(t, rg.get(tok))
			assertUnavailable(t, rg.get(tok))

			if n := fake.Hits(); n != 2 {
				t.Errorf("GoTrue /user calls = %d over two requests, want 2 (not cached)", n)
			}
			if n := rg.upstream.Hits(); n != 0 {
				t.Errorf("upstream received %d requests, want 0", n)
			}
		})
	}
}

// A caller's client that follows redirects must not turn a GoTrue 302 into a live session.
func TestSessionCheck_BareClientNeverFollowsARedirect(t *testing.T) {
	var targetHits atomic.Int64
	fake := newUserFake(t, http.StatusOK, gtUser)
	fake.setAnswer(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			targetHits.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, gtUser)
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	bare := &http.Client{}
	rg := newSessionRig(t, fake.URL, bare, nil)
	tok := rg.signer.token(t, subjectS1, sid1)

	assertUnavailable(t, rg.get(tok))

	if n := fake.Hits(); n != 1 {
		t.Errorf("GoTrue /user saw %d calls, want 1", n)
	}
	if n := targetHits.Load(); n != 0 {
		t.Errorf("redirect target received %d requests, want 0", n)
	}
	if n := rg.upstream.Hits(); n != 0 {
		t.Errorf("upstream received %d requests, want 0", n)
	}
	if bare.CheckRedirect != nil {
		t.Error("NewSessionChecker mutated the caller's client")
	}
}

func TestSessionCheck_UnavailableIs503AndNotCached(t *testing.T) {
	short := testClient()
	short.Timeout = 100 * time.Millisecond
	cases := []struct {
		name   string
		client *http.Client
		answer http.HandlerFunc
	}{
		{"429", nil, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(429)
			_, _ = io.WriteString(w, gtError(429, "over_request_rate_limit"))
		}},
		{"500", nil, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(500)
			_, _ = io.WriteString(w, `{"code":500,"error_code":"unexpected_failure","msg":"Internal Server Error"}`)
		}},
		{"connection dropped", nil, func(w http.ResponseWriter, _ *http.Request) {
			conn, _, err := http.NewResponseController(w).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}},
		{"slower than the timeout", short, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(3 * time.Second):
			}
			w.WriteHeader(200)
			_, _ = io.WriteString(w, gtUser)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := newUserFake(t, http.StatusOK, gtUser)
			fake.setAnswer(c.answer)
			rg := newSessionRig(t, fake.URL, c.client, nil)
			tok := rg.signer.token(t, subjectS1, sid1)

			assertUnavailable(t, rg.get(tok))
			assertUnavailable(t, rg.get(tok))

			if n := fake.Hits(); n != 2 {
				t.Errorf("GoTrue /user calls = %d over two requests, want 2 (not cached)", n)
			}
			if n := rg.upstream.Hits(); n != 0 {
				t.Errorf("upstream received %d requests, want 0", n)
			}
		})
	}
	t.Run("closed listener", func(t *testing.T) {
		rg := newSessionRig(t, closedURL(t), nil, nil)
		tok := rg.signer.token(t, subjectS1, sid1)

		assertUnavailable(t, rg.get(tok))
		assertUnavailable(t, rg.get(tok))
		if n := rg.upstream.Hits(); n != 0 {
			t.Errorf("upstream received %d requests, want 0", n)
		}
	})
}

func TestSessionCheck_EvictionBeatsAnInFlightCheck(t *testing.T) {
	fake := newUserFake(t, http.StatusOK, gtUser)
	release := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	t.Cleanup(open)
	var calls atomic.Int64
	fake.setAnswer(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, gtUser)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, gtError(403, "session_not_found"))
	})
	rg := newSessionRig(t, fake.URL, nil, nil)
	refusal := rg.refusal(t)
	tok := rg.signer.token(t, subjectS1, sid1)

	first := make(chan int, 1)
	go func() { first <- rg.get(tok).Code }()
	if !waitFor(2*time.Second, func() bool { return fake.Hits() == 1 }) {
		open()
		<-first
		t.Fatalf("GoTrue /user was never called for request 1 (calls = %d)", fake.Hits())
	}
	rg.sessions.EvictSubject(subjectS1)
	open()

	if code := <-first; code != http.StatusOK {
		t.Errorf("request 1: status = %d, want 200 (its in-flight answer was live)", code)
	}
	if n := rg.upstream.Hits(); n != 1 {
		t.Errorf("upstream received %d requests after request 1, want 1", n)
	}
	assertRevoked(t, rg.get(tok), refusal)
	if n := fake.Hits(); n != 2 {
		t.Errorf("GoTrue /user calls = %d, want 2 (the evicted call's live answer was not cached)", n)
	}
}

func TestSessionCheck_EvictSubject(t *testing.T) {
	fake := newUserFake(t, http.StatusOK, gtUser)
	rg := newSessionRig(t, fake.URL, nil, nil)
	tok1 := rg.signer.token(t, subjectS1, sid1)
	tok2 := rg.signer.token(t, subjectS2, sid2)

	rg.get(tok1)
	rg.get(tok2)
	rg.get(tok1)
	rg.get(tok2)
	if n := fake.Hits(); n != 2 {
		t.Fatalf("GoTrue /user calls = %d before eviction, want 2 (both cached)", n)
	}

	rg.sessions.EvictSubject(subjectS1)

	if rec := rg.get(tok1); rec.Code != http.StatusOK {
		t.Errorf("S1 after eviction: status = %d, want 200", rec.Code)
	}
	if n := fake.Hits(); n != 3 {
		t.Errorf("GoTrue /user calls = %d after S1's next request, want 3", n)
	}
	rg.get(tok2)
	if n := fake.Hits(); n != 3 {
		t.Errorf("GoTrue /user calls = %d after S2's next request, want 3 (S2's entry stays)", n)
	}
	if auths := fake.Auths(); len(auths) == 3 && auths[2] != "Bearer "+tok1 {
		t.Errorf("the post-eviction call carried %q, want S1's bearer", auths[2])
	}
}

func TestSessionCheck_FullCacheStillChecks(t *testing.T) {
	fake := newUserFake(t, http.StatusOK, gtUser)
	rg := newSessionRig(t, fake.URL, nil, nil)
	rg.sessions.SetMaxEntriesForTest(2)
	tok := func(sid string) string { return rg.signer.token(t, subjectS1, sid) }
	a, b, c, d := tok(sid1), tok(sid2), tok("2a3b4c5d-6e7f-4809-9a1b-2c3d4e5f6a7b"), tok("3b4c5d6e-7f80-4912-8b2c-3d4e5f6a7b8c")

	steps := []struct {
		name    string
		advance time.Duration
		token   string
		want    int
	}{
		{"a fills", 0, a, 1},
		{"b fills", 0, b, 2},
		{"c over the cap is checked", 0, c, 3},
		{"c is checked again, uncached", 0, c, 4},
		{"a stays cached", 0, a, 4},
		{"d after TTL: sweep admits it", SessionCheckTTL, d, 5},
		{"d is cached", 0, d, 5},
	}
	for _, s := range steps {
		rg.clock.Advance(s.advance)
		if rec := rg.get(s.token); rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", s.name, rec.Code)
		}
		if n := fake.Hits(); n != s.want {
			t.Errorf("%s: GoTrue /user calls = %d, want %d", s.name, n, s.want)
		}
	}
}

func TestSessionCheck_NeverLogsTheToken(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		closed bool
	}{
		{"200", http.StatusOK, gtUser, false},
		{"403", http.StatusForbidden, gtError(403, "session_not_found"), false},
		{"500", http.StatusInternalServerError, `{"code":500,"error_code":"unexpected_failure","msg":"Internal Server Error"}`, false},
		{"unreachable", 0, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			authURL := closedURL(t)
			if !c.closed {
				authURL = newUserFake(t, c.status, c.body).URL
			}
			log, buf := captureLog()
			rg := newSessionRig(t, authURL, nil, log)
			tok := rg.signer.token(t, subjectS1, sid1)
			sig := tok[strings.LastIndex(tok, ".")+1:]

			rg.get(tok)

			if strings.Contains(buf.String(), tok) || strings.Contains(buf.String(), sig) {
				t.Errorf("a log line carries the bearer token:\n%s", buf.String())
			}
			if c.status != http.StatusInternalServerError {
				return
			}
			var warns, named int
			sc := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
			for sc.Scan() {
				var line map[string]any
				if json.Unmarshal(sc.Bytes(), &line) != nil {
					continue
				}
				if s, ok := line["upstream_status"].(float64); ok && s == 500 {
					named++
					if line["level"] == "WARN" {
						warns++
					}
				}
			}
			if named != 1 || warns != 1 {
				t.Errorf("lines naming upstream_status=500: %d (WARN: %d), want exactly one WARN:\n%s", named, warns, buf.String())
			}
		})
	}
}

func TestSessionCheck_NilAuthURLIs503(t *testing.T) {
	rg := newSessionRig(t, nil, nil, nil)

	assertUnavailable(t, rg.get(rg.signer.token(t, subjectS1, sid1)))
	if n := rg.upstream.Hits(); n != 0 {
		t.Errorf("upstream received %d requests, want 0", n)
	}

	// A token without session_id is never checked, so a nil URL still routes it.
	tok, err := rg.mock.Mint(auth.MintOptions{Subject: testSubject, Role: testRole, TenantID: testTenant})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if rec := rg.get(tok); rec.Code != http.StatusOK || rg.upstream.Hits() != 1 {
		t.Errorf("no-sid token: status = %d, upstream hits = %d, want 200 and 1", rec.Code, rg.upstream.Hits())
	}
}

// held answers GoTrue's 200 once release closes; a request abandoned first gets nothing.
func held(release <-chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, gtUser)
	}
}

func gate(t *testing.T) (<-chan struct{}, func()) {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	t.Cleanup(open)
	return release, open
}

// AUTH_URL may carry a path prefix; /user joins under it.
func TestSessionCheck_PrefixedAuthURLJoinsUser(t *testing.T) {
	for name, prefix := range map[string]string{"bare prefix": "/auth/v1", "trailing slash": "/auth/v1/"} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				if r.URL.Path != "/auth/v1/user" {
					http.NotFound(w, r)
					return
				}
				_, _ = io.WriteString(w, gtUser)
			}))
			t.Cleanup(srv.Close)
			authURL, err := url.Parse(srv.URL + prefix)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			rg := newSessionRig(t, authURL, nil, nil)

			if rec := rg.get(rg.signer.token(t, subjectS1, sid1)); rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			mu.Lock()
			defer mu.Unlock()
			if len(paths) != 1 || paths[0] != "/auth/v1/user" {
				t.Errorf("GoTrue saw paths %v, want exactly [/auth/v1/user]", paths)
			}
		})
	}
}

// The checker's own 5 s bound, with a client that has no timeout of its own.
func TestSessionCheck_SlowerThanSessionCheckTimeoutIs503(t *testing.T) {
	const bound = 5 * time.Second
	fake := newUserFake(t, http.StatusOK, gtUser)
	fake.setAnswer(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * bound):
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, gtUser)
	})
	noTimeout := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	rg := newSessionRig(t, fake.URL, noTimeout, nil)

	start := time.Now()
	rec := rg.get(rg.signer.token(t, subjectS1, sid1))
	elapsed := time.Since(start)

	assertUnavailable(t, rec)
	if elapsed < bound || elapsed > bound+1500*time.Millisecond {
		t.Errorf("answered after %v, want about %v", elapsed, bound)
	}
	if n := rg.upstream.Hits(); n != 0 {
		t.Errorf("upstream received %d requests, want 0", n)
	}
}

// A waiter whose own request ends answers 503 at once; the shared call runs on for the rest.
func TestSessionCheck_CancelledWaiterLeavesTheSharedCall(t *testing.T) {
	fake := newUserFake(t, http.StatusOK, gtUser)
	release, open := gate(t)
	fake.setAnswer(held(release))
	rg := newSessionRig(t, fake.URL, nil, nil)
	tok := rg.signer.token(t, subjectS1, sid1)

	leader := make(chan int, 1)
	go func() { leader <- rg.get(tok).Code }()
	if !waitFor(2*time.Second, func() bool { return fake.Hits() == 1 }) {
		open()
		<-leader
		t.Fatalf("GoTrue /user was never called for the leader (calls = %d)", fake.Hits())
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiter := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		rg.handler.ServeHTTP(rec, request(http.MethodGet, "/api/tenancy/v1/me", tok).WithContext(ctx))
		waiter <- rec.Code
	}()
	cancel()

	select {
	case code := <-waiter:
		if code != http.StatusServiceUnavailable {
			t.Errorf("cancelled waiter: status = %d, want 503", code)
		}
	case <-time.After(2 * time.Second):
		t.Error("cancelled waiter is still blocked on the held call")
	}
	if n := fake.Hits(); n != 1 {
		t.Errorf("GoTrue /user calls = %d while the leader's call is held, want 1 (shared)", n)
	}
	open()
	if code := <-leader; code != http.StatusOK {
		t.Errorf("leader: status = %d, want 200", code)
	}
	if rec := rg.get(tok); rec.Code != http.StatusOK || fake.Hits() != 1 {
		t.Errorf("next request: status = %d, GoTrue calls = %d, want 200 and 1 (the live answer was cached)", rec.Code, fake.Hits())
	}
}

// The leader's own cancellation must not fail the call it shares: its answer is still cached.
func TestSessionCheck_CancelledLeaderStillCachesTheAnswer(t *testing.T) {
	fake := newUserFake(t, http.StatusOK, gtUser)
	release, open := gate(t)
	fake.setAnswer(held(release))
	rg := newSessionRig(t, fake.URL, nil, nil)
	tok := rg.signer.token(t, subjectS1, sid1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		rg.handler.ServeHTTP(httptest.NewRecorder(), request(http.MethodGet, "/api/tenancy/v1/me", tok).WithContext(ctx))
	}()
	if !waitFor(2*time.Second, func() bool { return fake.Hits() == 1 }) {
		open()
		<-done
		t.Fatalf("GoTrue /user was never called (calls = %d)", fake.Hits())
	}
	cancel()
	open()
	<-done

	if rec := rg.get(tok); rec.Code != http.StatusOK {
		t.Errorf("next request: status = %d, want 200", rec.Code)
	}
	if n := fake.Hits(); n != 1 {
		t.Errorf("GoTrue /user calls = %d, want 1 (the cancelled leader's call still cached its answer)", n)
	}
}

// An evicted call that finishes late must not drop the registration of the call that replaced it.
func TestSessionCheck_EvictedCallKeepsANewerRegistration(t *testing.T) {
	fake := newUserFake(t, http.StatusOK, gtUser)
	rel1, open1 := gate(t)
	rel2, open2 := gate(t)
	var calls atomic.Int64
	fake.setAnswer(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			held(rel1)(w, r)
		case 2:
			held(rel2)(w, r)
		default:
			_, _ = io.WriteString(w, gtUser)
		}
	})
	rg := newSessionRig(t, fake.URL, nil, nil)
	tok := rg.signer.token(t, subjectS1, sid1)
	get := func(out chan<- int) { out <- rg.get(tok).Code }

	r1, r2, r3 := make(chan int, 1), make(chan int, 1), make(chan int, 1)
	go get(r1)
	if !waitFor(2*time.Second, func() bool { return fake.Hits() == 1 }) {
		t.Fatalf("GoTrue /user calls = %d, want request 1's call", fake.Hits())
	}
	rg.sessions.EvictSubject(subjectS1)
	go get(r2)
	if !waitFor(2*time.Second, func() bool { return fake.Hits() == 2 }) {
		t.Fatalf("GoTrue /user calls = %d after eviction, want request 2's own call", fake.Hits())
	}
	open1()
	if code := <-r1; code != http.StatusOK {
		t.Errorf("request 1: status = %d, want 200", code)
	}
	go get(r3)
	// A dropped registration lets request 3 start a third call well inside this window.
	waitFor(300*time.Millisecond, func() bool { return fake.Hits() > 2 })
	open2()
	for i, ch := range []chan int{r2, r3} {
		if code := <-ch; code != http.StatusOK {
			t.Errorf("request %d: status = %d, want 200", i+2, code)
		}
	}
	if n := fake.Hits(); n != 2 {
		t.Errorf("GoTrue /user calls = %d, want 2 (request 3 shares request 2's call)", n)
	}
	rg.get(tok)
	if n := fake.Hits(); n != 2 {
		t.Errorf("GoTrue /user calls = %d after a fourth request, want 2 (request 2's live answer was cached)", n)
	}
}

// A checker built by NewSessionChecker caps its cache at 100,000 entries.
func TestSessionCheck_DefaultCapIsSessionCheckMaxEntries(t *testing.T) {
	fake := newUserFake(t, http.StatusOK, gtUser)
	rg := newSessionRig(t, fake.URL, nil, nil)
	rg.sessions.FillForTest(100_000-1, subjectS2)
	last, over := rg.signer.token(t, subjectS1, sid1), rg.signer.token(t, subjectS1, sid2)

	steps := []struct {
		name  string
		token string
		want  int
	}{
		{"the last free slot is checked", last, 1},
		{"the last free slot is cached", last, 1},
		{"one over the cap is checked", over, 2},
		{"one over the cap is not cached", over, 3},
	}
	for _, s := range steps {
		if rec := rg.get(s.token); rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", s.name, rec.Code)
		}
		if n := fake.Hits(); n != s.want {
			t.Errorf("%s: GoTrue /user calls = %d, want %d", s.name, n, s.want)
		}
	}
}

// confirmedJoinToken is a tenant-less sid token whose email GoTrue's /user confirms.
func confirmedJoinToken(t *testing.T, rg *sessionRig, sid string) string {
	t.Helper()
	return rg.signer.tenantlessToken(t, subjectS1, tenantlessClaims{email: joinEmail, sid: sid})
}

func TestSessionCheck_CacheHitCarriesTheConfirmedEmail(t *testing.T) {
	rg, fake := joinRig(t, confirmedUser(t, nil))
	tok := confirmedJoinToken(t, rg, sid1)

	for i := range 2 {
		rec := rg.do(http.MethodGet, joinMinePath, tok)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200: %s", i+1, rec.Code, rec.Body.String())
		}
		assertHeader(t, rg.upstream.Header(), "X-User-Email", joinEmail)
		rg.clock.Advance(time.Second)
	}
	if n := fake.Hits(); n != 1 {
		t.Fatalf("GoTrue /user calls = %d, want 1 (the second request is a cache hit)", n)
	}

	fake.set(http.StatusOK, confirmedUser(t, func(m map[string]any) { m["email_confirmed_at"] = nil }))
	rg.clock.Advance(30 * time.Second)
	rec := rg.do(http.MethodGet, joinMinePath, tok)
	if rec.Code != http.StatusForbidden {
		t.Errorf("after the TTL: status = %d, want 403 (GoTrue no longer confirms)", rec.Code)
	}
	if n := fake.Hits(); n != 2 {
		t.Errorf("GoTrue /user calls = %d after the TTL, want 2", n)
	}
	if n := rg.upstream.Hits(); n != 2 {
		t.Errorf("tenancy hits = %d, want 2 (the refused request reached nothing)", n)
	}
}

func TestSessionCheck_SharedCallCarriesTheConfirmedEmail(t *testing.T) {
	rg, fake := joinRig(t, confirmedUser(t, nil))
	release, open := gate(t)
	body := confirmedUser(t, nil)
	fake.setAnswer(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	})
	tok := confirmedJoinToken(t, rg, sid1)

	const n = 4
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = rg.do(http.MethodGet, joinMinePath, tok).Code
		}()
	}
	called := waitFor(2*time.Second, func() bool { return fake.Hits() >= 1 })
	// Without sharing, the others reach GoTrue well inside this window.
	waitFor(300*time.Millisecond, func() bool { return fake.Hits() > 1 })
	open()
	wg.Wait()

	if !called {
		t.Fatalf("GoTrue /user was never called")
	}
	if got := fake.Hits(); got != 1 {
		t.Errorf("GoTrue /user calls = %d for %d concurrent cold requests, want 1", got, n)
	}
	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("request %d: status = %d, want 200", i, c)
		}
	}
	if got := rg.upstream.Hits(); got != n {
		t.Errorf("tenancy hits = %d, want %d", got, n)
	}
	assertHeader(t, rg.upstream.Header(), "X-User-Email", joinEmail)
}

// countingTransport counts the response-body bytes the session check reads.
type countingTransport struct {
	base http.RoundTripper
	read *atomic.Int64
}

func (c countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.base.RoundTrip(r)
	if err == nil {
		resp.Body = countingBody{resp.Body, c.read}
	}
	return resp, err
}

type countingBody struct {
	io.ReadCloser
	read *atomic.Int64
}

func (b countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.read.Add(int64(n))
	return n, err
}

func TestSessionCheck_ReadsAndDrainsTheUserBodyAtSixteenKiB(t *testing.T) {
	const limit = 16 << 10
	cases := []struct {
		name     string
		body     string
		confirms bool
		wantRead int64 // 0: the whole body
	}{
		{"one byte under", confirmedUserSized(t, limit-1), true, 0},
		{"exactly the limit", confirmedUserSized(t, limit), true, 0},
		{"one byte over", confirmedUserSized(t, limit+1), false, 0},
		// The object ends early; the drain reads the rest, through the same limit.
		{"trailing bytes past the object", confirmedUser(t, nil) + strings.Repeat(" ", 20<<10), true, limit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newUserFake(t, http.StatusOK, tc.body)
			var read atomic.Int64
			client := &http.Client{Transport: countingTransport{http.DefaultTransport, &read}}
			rg := newSessionRig(t, fake.URL, client, nil)

			rec := rg.do(http.MethodGet, joinMinePath, confirmedJoinToken(t, rg, sid1))
			got := read.Load()

			if tc.confirms && (rec.Code != http.StatusOK || rg.upstream.Hits() != 1) {
				t.Errorf("join route = %d, tenancy hits = %d, want 200 and 1", rec.Code, rg.upstream.Hits())
			}
			if !tc.confirms {
				assertForbiddenNoUpstream(t, rg, rec, "join route")
			}
			want := tc.wantRead
			if want == 0 && tc.confirms {
				want = int64(len(tc.body))
			}
			if got == 0 || got > limit {
				t.Errorf("session check read %d bytes, want between 1 and %d", got, limit)
			}
			if want != 0 && got != want {
				t.Errorf("session check read %d bytes, want %d", got, want)
			}

			// A body that does not confirm is still a live session on every other route.
			if rec := rg.get(rg.signer.token(t, subjectS1, sid2)); rec.Code != http.StatusOK {
				t.Errorf("GET /me = %d, want 200 (live)", rec.Code)
			}
		})
	}
}
