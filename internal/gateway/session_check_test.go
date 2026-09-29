package gateway

import (
	"bufio"
	"bytes"
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
}

func (u *sessionUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.hits.Add(1)
	u.mu.Lock()
	u.header = r.Header.Clone()
	u.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (u *sessionUpstream) Hits() int { return int(u.hits.Load()) }

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

	clk := newTestClock()
	sessions := NewSessionChecker(authURL, client, clk.Now, log)
	return &sessionRig{
		handler: Handler(Options{
			Verifier:  verifier,
			Sessions:  sessions,
			Upstreams: map[string]*url.URL{"tenancy": upURL},
			Logger:    log,
		}),
		sessions: sessions,
		clock:    clk,
		signer:   signer,
		mock:     mock,
		upstream: up,
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

// Regression guard: green against the pass-through stub.
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
		{SessionCheckTTL - time.Nanosecond, 1},
		{SessionCheckTTL, 2},
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
