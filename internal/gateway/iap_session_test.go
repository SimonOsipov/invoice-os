package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth/iaptest"
)

const (
	iapAud         = "/projects/111111111111/global/backendServices/1111111111111111111"
	iapOtherAud    = "/projects/111111111111/global/backendServices/2222222222222222222"
	iapEmail       = "Sam.Staff@Ascomply.Example" // mixed case: a 403 must echo it as asserted
	iapHeader      = "X-Goog-Iap-Jwt-Assertion"
	iapWorkerPath  = "/internal/console-sessions/recovery-token"
	iapGatewayTok  = "gateway-token-for-iap-tests"
	iapRT          = "iap-refresh-token-8w3k"
	msgNoIdentity  = "no verified Google identity"
	msgNoStaff     = "no staff account"
	msgConsoleDown = "console sign-in is unavailable"
	iapBanned      = `{"code":403,"error_code":"user_banned","msg":"User is banned"}`
	iapRateLimited = `{"code":429,"error_code":"over_request_rate_limit","msg":"rate limited"}`
)

// iapHash is the n-th recovery token hash the fake worker mints.
func iapHash(n int) string { return fmt.Sprintf("minted-hash-%d-zq7", n) }

// iapJWT is a GoTrue-shaped access token carrying appMeta as app_metadata; the gateway reads it unverified.
func iapJWT(appMeta string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"ES256","typ":"JWT"}`)) + "." +
		enc([]byte(`{"sub":"`+aeUserID+`","app_metadata":`+appMeta+`}`)) + "." + enc([]byte("sig-"+appMeta))
}

// iapSessionAnswer is GoTrue's /verify answer for gtSession's user with appMeta in the access token.
func iapSessionAnswer(appMeta string) (accessToken, body string) {
	accessToken = iapJWT(appMeta)
	return accessToken, `{"access_token":"` + accessToken + `","token_type":"bearer","expires_in":3600,"refresh_token":"` + iapRT +
		`","user":{"id":"` + aeUserID + `","email":"` + aeEmail + `","email_confirmed_at":"2026-09-24T10:05:00Z"}}`
}

const iapStaffMeta = `{"staff":true}`

type iapCall struct {
	Method, Path, RawQuery, Auth string
	Body                         []byte
	Header                       http.Header
}

// iapRig is one gateway handler on a fake worker and a fake GoTrue. Set worker, verify, workerURL or the store before the first request.
type iapRig struct {
	t         *testing.T
	signer    *iaptest.Signer
	verifier  *auth.IAPVerifier
	store     *HandoffStore
	events    *recEvents
	log       *slog.Logger
	logBuf    *bytes.Buffer
	workerURL *url.URL
	authURL   *url.URL

	mu          sync.Mutex
	workerCalls []iapCall
	gtCalls     []iapCall
	// worker answers the n-th (1-based) worker call.
	worker func(n int) (int, string)
	// verify answers a /verify for tokenHash.
	verify        func(tokenHash string) (int, string)
	lastAssertion string
}

func newIAPRig(t *testing.T) *iapRig {
	t.Helper()
	signer, jwks := iaptest.NewSigner(t)
	v, err := auth.NewIAPVerifier(auth.IAPConfig{Audiences: []string{iapAud}, JWKSURL: jwks})
	if err != nil {
		t.Fatalf("NewIAPVerifier: %v", err)
	}
	log, buf := captureLog()
	rg := &iapRig{t: t, signer: signer, verifier: v, store: testHandoffStore(), events: &recEvents{}, log: log, logBuf: buf}
	rg.worker = func(n int) (int, string) { return http.StatusOK, `{"token_hash":"` + iapHash(n) + `"}` }
	_, staff := iapSessionAnswer(iapStaffMeta)
	rg.verify = func(string) (int, string) { return http.StatusOK, staff }

	record := func(r *http.Request) iapCall {
		b, _ := io.ReadAll(r.Body)
		return iapCall{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), b, r.Header.Clone()}
	}
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := record(r)
		rg.mu.Lock()
		rg.workerCalls = append(rg.workerCalls, c)
		n := len(rg.workerCalls)
		rg.mu.Unlock()
		status, body := rg.worker(n)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(worker.Close)
	gt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := record(r)
		rg.mu.Lock()
		rg.gtCalls = append(rg.gtCalls, c)
		rg.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/verify":
			var in struct {
				Type      string `json:"type"`
				TokenHash string `json:"token_hash"`
			}
			_ = json.Unmarshal(c.Body, &in)
			status, body := rg.verify(in.TokenHash)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		case "/logout":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("fake GoTrue got unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(gt.Close)
	rg.workerURL, _ = url.Parse(worker.URL)
	rg.authURL, _ = url.Parse(gt.URL)
	return rg
}

func (rg *iapRig) handler() http.Handler {
	return IAPSessionHandler(rg.verifier, rg.workerURL, testClient(), iapGatewayTok, rg.authURL, testClient(), rg.store, rg.log, rg.events)
}

func (rg *iapRig) claims(email, aud string) map[string]any {
	now := time.Now()
	return map[string]any{"iss": auth.IAPIssuer, "aud": aud, "sub": "accounts.google.com:118273645546372819", "email": email,
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(10 * time.Minute).Unix()}
}

func (rg *iapRig) assertion(email string) string {
	return rg.signer.Sign(rg.claims(email, iapAud))
}

// request is a POST with a valid assertion for iapEmail; each mutate runs last.
func (rg *iapRig) request(state string, mutate ...func(*http.Request)) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/auth/iap-session", strings.NewReader(`{"state":"`+state+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rg.lastAssertion = rg.assertion(iapEmail)
	req.Header.Set(iapHeader, rg.lastAssertion)
	for _, m := range mutate {
		m(req)
	}
	return req
}

func (rg *iapRig) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	rg.handler().ServeHTTP(rec, req)
	return rec
}

// mint serves a valid request for a fresh state.
func (rg *iapRig) mint(mutate ...func(*http.Request)) (*httptest.ResponseRecorder, string) {
	state := randomState(rg.t)
	return rg.serve(rg.request(state, mutate...)), state
}

func (rg *iapRig) workers() []iapCall {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	return append([]iapCall(nil), rg.workerCalls...)
}

func (rg *iapRig) gotrue() []iapCall {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	return append([]iapCall(nil), rg.gtCalls...)
}

func (rg *iapRig) gotrueAt(path string) []iapCall {
	var out []iapCall
	for _, c := range rg.gotrue() {
		if c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

func requireNoStaff403(t *testing.T, rec *httptest.ResponseRecorder, email string) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %q is not a string map: %v", rec.Body.String(), err)
	}
	if want := map[string]string{"error": msgNoStaff, "email": email}; !maps.Equal(got, want) {
		t.Fatalf("body = %v, want %v", got, want)
	}
}

func TestIAPSession_StaffGetsACodeAndNoToken(t *testing.T) {
	rg := newIAPRig(t)
	at, _ := iapSessionAnswer(iapStaffMeta)
	state := randomState(t)

	rec := rg.serve(rg.request(state))

	code := requireCode(t, rec)
	for _, secret := range []string{at, iapRT} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("200 body carries %q, want a code only", secret)
		}
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	stored, ok := rg.store.Take(code, state)
	if !ok {
		t.Fatal("store.Take(code, state) = false, want the stored answer")
	}
	if gotAT, gotRT := requireAnswer(t, []byte(stored)); gotAT != at || gotRT != iapRT {
		t.Errorf("stored answer = (%q, %q), want the minted tokens", gotAT, gotRT)
	}

	w := rg.workers()
	if len(w) != 1 || w[0].Method != http.MethodPost || w[0].Path != iapWorkerPath {
		t.Fatalf("worker calls = %+v, want one POST %s", w, iapWorkerPath)
	}
	if got := w[0].Header.Get(platform.HeaderGatewayToken); got != iapGatewayTok {
		t.Errorf("worker call %s = %q, want the gateway token", platform.HeaderGatewayToken, got)
	}
	var wb map[string]string
	if err := json.Unmarshal(w[0].Body, &wb); err != nil || !maps.Equal(wb, map[string]string{"assertion": rg.lastAssertion}) {
		t.Errorf("worker body = %s, want exactly {assertion: <the raw assertion>}", w[0].Body)
	}
	v := rg.gotrueAt("/verify")
	if len(v) != 1 || v[0].Method != http.MethodPost {
		t.Fatalf("GoTrue /verify calls = %+v, want one POST", v)
	}
	var vb map[string]string
	if err := json.Unmarshal(v[0].Body, &vb); err != nil || !maps.Equal(vb, map[string]string{"type": "recovery", "token_hash": iapHash(1)}) {
		t.Errorf("/verify body = %s, want a recovery of the worker's hash", v[0].Body)
	}
	if got := v[0].Header.Values(platform.HeaderUserIP); len(got) != 1 || got[0] == "" {
		t.Errorf("/verify %s = %v, want one value", platform.HeaderUserIP, got)
	}
	if n := len(rg.gotrueAt("/logout")); n != 0 {
		t.Errorf("a staff session saw %d logouts, want 0", n)
	}
	// The one INFO line names the user id; the log holds no identity or secret.
	if recs := recordsNamed(t, rg.logBuf, "iap session: minted"); len(recs) != 1 || recs[0]["user_id"] != aeUserID {
		t.Errorf("minted log records = %v, want one with user_id %s", recs, aeUserID)
	}
	requireNoLeak(t, rg.logBuf, iapEmail, strings.ToLower(iapEmail), aeEmail, rg.lastAssertion, at, iapRT, iapHash(1), code)
}

func TestIAPSession_CodeIsBoundToTheState(t *testing.T) {
	rg := newIAPRig(t)
	s1, s2 := randomState(t), randomState(t)

	wrong := requireCode(t, rg.serve(rg.request(s1)))
	if _, ok := rg.store.Take(wrong, s2); ok {
		t.Error("Take(code of S1, S2) = true, want false")
	}
	// A wrong state spends the code, so the positive pair needs a fresh mint.
	right := requireCode(t, rg.serve(rg.request(s1)))
	if _, ok := rg.store.Take(right, s1); !ok {
		t.Error("Take(code of S1, S1) = false, want true")
	}
}

func TestIAPSession_SendsTheVerifiedAssertionNotAClientEmail(t *testing.T) {
	rg := newIAPRig(t)
	state := randomState(t)
	req := rg.request(state, func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(`{"state":"` + state + `","email":"evil@x"}`))
		r.Header.Set("X-Goog-Authenticated-User-Email", "accounts.google.com:evil@x")
		r.Header.Set("X-Goog-Authenticated-User-Id", "accounts.google.com:evil-id")
		r.Header.Set("X-User-Email", "evil@x")
	})

	rec := rg.serve(req)

	requireCode(t, rec) // the control: the request was accepted, so the checks below are not vacuous
	w := rg.workers()
	if len(w) != 1 {
		t.Fatalf("worker calls = %d, want 1", len(w))
	}
	var wb map[string]string
	if err := json.Unmarshal(w[0].Body, &wb); err != nil || !maps.Equal(wb, map[string]string{"assertion": rg.lastAssertion}) {
		t.Errorf("worker body = %s, want exactly {assertion: <the raw assertion>}", w[0].Body)
	}
	for _, c := range append(rg.workers(), rg.gotrue()...) {
		if all := strings.ToLower(fmt.Sprint(c.Header) + string(c.Body) + c.RawQuery + c.Path); strings.Contains(all, "evil") {
			t.Errorf("%s %s carries the client's address: %s", c.Method, c.Path, all)
		}
	}
	if e := rg.events.events(); len(e) != 1 || strings.Contains(e[0].Email, "evil") {
		t.Errorf("events = %+v, want one without the client's address", e)
	}
}

func TestIAPSession_NoAccountIs403WithTheAssertionEmail(t *testing.T) {
	rg := newIAPRig(t)
	rg.worker = func(int) (int, string) { return http.StatusForbidden, `{"error":"no staff account"}` }

	rec, _ := rg.mint()

	requireNoStaff403(t, rec, iapEmail)
	if n := len(rg.workers()); n != 1 {
		t.Errorf("worker calls = %d, want 1", n)
	}
	if n := len(rg.gotrue()); n != 0 {
		t.Errorf("GoTrue calls = %d, want 0", n)
	}
	if n := storeMapEntries(rg.store); n != 0 {
		t.Errorf("store holds %d entries, want 0", n)
	}
}

func TestIAPSession_NonStaffTokenIs403AndTheSessionEnds(t *testing.T) {
	for _, meta := range []string{"{}", `{"staff":"true"}`, `{"Staff":true}`} {
		t.Run(meta, func(t *testing.T) {
			rg := newIAPRig(t)
			at, body := iapSessionAnswer(meta)
			rg.verify = func(string) (int, string) { return http.StatusOK, body }

			rec, _ := rg.mint()

			requireNoStaff403(t, rec, iapEmail)
			for _, secret := range []string{at, iapRT} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Errorf("403 body carries %q", secret)
				}
			}
			out := rg.gotrueAt("/logout")
			if len(out) != 1 || out[0].Method != http.MethodPost || out[0].RawQuery != "scope=local" || out[0].Auth != "Bearer "+at {
				t.Errorf("logout calls = %+v, want one POST /logout?scope=local with the minted access token", out)
			}
			if n := storeMapEntries(rg.store); n != 0 {
				t.Errorf("store holds %d entries, want 0", n)
			}
		})
	}
}

func TestIAPSession_BannedAtVerifyIs403(t *testing.T) {
	rg := newIAPRig(t)
	rg.verify = func(string) (int, string) { return http.StatusForbidden, iapBanned }

	rec, _ := rg.mint()

	requireNoStaff403(t, rec, iapEmail)
	if n := len(rg.workers()); n != 1 {
		t.Errorf("worker calls = %d, want 1 (a ban is not retried)", n)
	}
	if n := len(rg.gotrueAt("/logout")); n != 0 {
		t.Errorf("logouts = %d, want 0: no session was minted", n)
	}
}

func TestIAPSession_AssertionShapeRefusals(t *testing.T) {
	rg := newIAPRig(t)
	// Control: a valid assertion is admitted, so the refusals below come from the assertion.
	requireCode(t, mustRec(rg.mint()))
	baseW, baseG := len(rg.workers()), len(rg.gotrue())
	if baseW != 1 || baseG != 1 {
		t.Fatalf("control made %d worker and %d GoTrue calls, want 1 and 1", baseW, baseG)
	}
	other, _ := iaptest.NewSigner(t)
	cases := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"no header", func(r *http.Request) { r.Header.Del(iapHeader) }},
		{"two header values", func(r *http.Request) { r.Header.Add(iapHeader, r.Header.Get(iapHeader)) }},
		{"9 KiB header", func(r *http.Request) {
			c := rg.claims(iapEmail, iapAud)
			c["pad"] = strings.Repeat("x", 9<<10)
			r.Header.Set(iapHeader, rg.signer.Sign(c))
		}},
		{"forged signature", func(r *http.Request) { r.Header.Set(iapHeader, other.Sign(rg.claims(iapEmail, iapAud))) }},
		{"wrong audience", func(r *http.Request) { r.Header.Set(iapHeader, rg.signer.Sign(rg.claims(iapEmail, iapOtherAud))) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, _ := rg.mint(tc.mutate)

			requireRefusal(t, rec, http.StatusUnauthorized, msgNoIdentity)
			if w, g := len(rg.workers()), len(rg.gotrue()); w != baseW || g != baseG {
				t.Errorf("calls after the refusal: worker %d, GoTrue %d; want %d and %d", w, g, baseW, baseG)
			}
		})
	}
}

func mustRec(rec *httptest.ResponseRecorder, _ string) *httptest.ResponseRecorder { return rec }

// iapFailure is one request that must not yield a code.
type iapFailure struct {
	name     string
	upstream bool // a worker or GoTrue failure: counted by TestIAPSession_UpstreamFailures
	setup    func(t *testing.T, rg *iapRig)
	mutate   func(*http.Request)
	status   int
	msg      string // the error text; empty for the 403, which carries the email too
}

func iapFailures() []iapFailure {
	_, nonStaff := iapSessionAnswer("{}")
	return []iapFailure{
		{name: "no assertion", mutate: func(r *http.Request) { r.Header.Del(iapHeader) }, status: 401, msg: msgNoIdentity},
		{name: "worker: no account", setup: func(_ *testing.T, rg *iapRig) {
			rg.worker = func(int) (int, string) { return 403, `{"error":"no staff account"}` }
		}, status: 403},
		{name: "non-staff token", setup: func(_ *testing.T, rg *iapRig) {
			rg.verify = func(string) (int, string) { return 200, nonStaff }
		}, status: 403},
		{name: "banned at verify", setup: func(_ *testing.T, rg *iapRig) {
			rg.verify = func(string) (int, string) { return 403, iapBanned }
		}, status: 403},
		{name: "worker 502", upstream: true, setup: func(_ *testing.T, rg *iapRig) {
			rg.worker = func(int) (int, string) { return 502, `{"error":"console sign-in is unavailable"}` }
		}, status: 502, msg: msgConsoleDown},
		{name: "worker 401 (assertion expired)", setup: func(_ *testing.T, rg *iapRig) {
			rg.worker = func(int) (int, string) { return 401, `{"error":"no verified Google identity"}` }
		}, status: 401, msg: msgNoIdentity},
		{name: "worker unreachable", upstream: true, setup: func(t *testing.T, rg *iapRig) { rg.workerURL = closedURL(t) },
			status: 502, msg: msgConsoleDown},
		{name: "verify 500", upstream: true, setup: func(_ *testing.T, rg *iapRig) {
			rg.verify = func(string) (int, string) { return 500, `{"code":500,"msg":"boom"}` }
		}, status: 502, msg: msgConsoleDown},
		{name: "verify 429", upstream: true, setup: func(_ *testing.T, rg *iapRig) {
			rg.verify = func(string) (int, string) { return 429, iapRateLimited }
		}, status: 429, msg: msgTooMany},
		{name: "second otp_expired", upstream: true, setup: func(_ *testing.T, rg *iapRig) {
			rg.verify = func(string) (int, string) { return 403, gtOTPExpired }
		}, status: 502, msg: msgConsoleDown},
		{name: "store full", upstream: true, setup: func(t *testing.T, rg *iapRig) { fillStore(t, rg.store, HandoffMaxLive) },
			status: 503, msg: msgConsoleDown},
	}
}

// runFailure serves c on a fresh rig and checks its status, message and headers.
func runFailure(t *testing.T, c iapFailure) *iapRig {
	t.Helper()
	rg := newIAPRig(t)
	if c.setup != nil {
		c.setup(t, rg)
	}
	var mutate []func(*http.Request)
	if c.mutate != nil {
		mutate = append(mutate, c.mutate)
	}
	rec, _ := rg.mint(mutate...)
	if c.status == http.StatusForbidden {
		requireNoStaff403(t, rec, iapEmail)
	} else {
		requireRefusal(t, rec, c.status, c.msg)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store on every answer", cc)
	}
	return rg
}

func TestIAPSession_UpstreamFailures(t *testing.T) {
	n := 0
	for _, c := range iapFailures() {
		if !c.upstream {
			continue
		}
		n++
		t.Run(c.name, func(t *testing.T) {
			rg := runFailure(t, c)

			// Every secret the request touched: the address, the assertion, the minted session and hash.
			at, _ := iapSessionAnswer(iapStaffMeta)
			requireNoLeak(t, rg.logBuf, iapEmail, strings.ToLower(iapEmail), aeEmail, rg.lastAssertion, at, iapRT, iapHash(1), iapHash(2))
		})
	}
	if n != 6 {
		t.Fatalf("%d upstream cases, want 6 (worker 502, unreachable, verify 500, 429, second otp_expired, store full)", n)
	}
}

func TestIAPSession_DeadlineWritesNoEventAndStoresNoCode(t *testing.T) {
	old := iapDeadline
	iapDeadline = 50 * time.Millisecond
	t.Cleanup(func() { iapDeadline = old })
	rg := newIAPRig(t)
	rg.worker = func(int) (int, string) {
		time.Sleep(300 * time.Millisecond)
		return http.StatusOK, `{"token_hash":"` + iapHash(1) + `"}`
	}

	rec, state := rg.mint()

	requireRefusal(t, rec, http.StatusBadGateway, msgConsoleDown)
	rg.events.requireNames(t)
	if got := rg.store.LiveForTest(); got != 0 {
		t.Errorf("%d codes stored after the deadline, want none (state %s)", got, state)
	}
}

func TestIAPSession_MethodAndBody(t *testing.T) {
	rg := newIAPRig(t)
	rec := rg.serve(rg.request(randomState(t), func(r *http.Request) { r.Method = http.MethodGet }))
	requireRefusal(t, rec, http.StatusMethodNotAllowed, "method not allowed")
	if rec.Header().Get("Allow") != http.MethodPost || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Allow = %q, Cache-Control = %q, want POST and no-store", rec.Header().Get("Allow"), rec.Header().Get("Cache-Control"))
	}

	cases := []struct{ name, body, msg string }{
		{"empty state", `{"state":""}`, msgStateRequired},
		{"no state", "{}", msgStateRequired},
		{"short state", `{"state":"` + strings.Repeat("a", 42) + `"}`, msgStateRequired},
		{"long state", `{"state":"` + strings.Repeat("a", 44) + `"}`, msgStateRequired},
		{"state with a plus", `{"state":"` + strings.Repeat("a", 42) + `+"}`, msgStateRequired},
		{"not JSON", "{", msgInvalidBody},
		{"over 1 KiB", `{"state":"` + strings.Repeat("a", 43) + `","pad":"` + strings.Repeat("x", 2<<10) + `"}`, msgInvalidBody},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(rg.workers())
			rec := rg.serve(rg.request(randomState(t), func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(tc.body)) }))
			requireRefusal(t, rec, http.StatusBadRequest, tc.msg)
			if n := len(rg.workers()); n != before {
				t.Errorf("worker calls grew by %d, want 0", n-before)
			}
		})
	}
	// Control: the same assertion with a well-formed state mints a code.
	requireCode(t, mustRec(rg.mint()))
}

func TestIAPSession_RetriesOnceWhenAConcurrentMintSpentTheToken(t *testing.T) {
	rg := newIAPRig(t)
	at, staff := iapSessionAnswer(iapStaffMeta)
	rg.verify = func(h string) (int, string) {
		if h == iapHash(1) {
			return http.StatusForbidden, gtOTPExpired
		}
		return http.StatusOK, staff
	}

	rec, state := rg.mint()

	code := requireCode(t, rec)
	if n := len(rg.workers()); n != 2 {
		t.Errorf("worker calls = %d, want exactly 2", n)
	}
	var hashes []string
	for _, c := range rg.gotrueAt("/verify") {
		var in struct {
			TokenHash string `json:"token_hash"`
		}
		_ = json.Unmarshal(c.Body, &in)
		hashes = append(hashes, in.TokenHash)
	}
	if strings.Join(hashes, ",") != iapHash(1)+","+iapHash(2) {
		t.Errorf("/verify hashes = %v, want the first then the second", hashes)
	}
	stored, ok := rg.store.Take(code, state)
	if !ok {
		t.Fatal("Take = false, want the answer")
	}
	if gotAT, _ := requireAnswer(t, []byte(stored)); gotAT != at {
		t.Errorf("stored access token is not the retry's")
	}
}

func TestIAPSession_SecondSpentTokenIs502(t *testing.T) {
	rg := newIAPRig(t)
	rg.verify = func(string) (int, string) { return http.StatusForbidden, gtOTPExpired }

	rec, _ := rg.mint()

	requireRefusal(t, rec, http.StatusBadGateway, msgConsoleDown)
	if n := len(rg.workers()); n != 2 {
		t.Errorf("worker calls = %d, want exactly 2 and no third", n)
	}
	if n := len(rg.gotrueAt("/verify")); n != 2 {
		t.Errorf("/verify calls = %d, want exactly 2", n)
	}
	if n := storeMapEntries(rg.store); n != 0 {
		t.Errorf("store holds %d entries, want 0", n)
	}
}

func TestIAPSession_StaffMintRecordsOneSignedIn(t *testing.T) {
	rg := newIAPRig(t)
	// The assertion's address differs in case from the account's: the event carries the /verify answer's.
	req := rg.request(randomState(t))
	previewClient(t, req)

	rec := rg.serve(req)

	requireCode(t, rec)
	rg.events.requireNames(t, EventSignedIn)
	want := AccountEvent{Event: EventSignedIn, UserID: aeUserID, Email: aeEmail, IP: aeIP, UserAgent: aeUA}
	if got := rg.events.events()[0]; got != want || got.FirstSession {
		t.Errorf("event = %+v, want %+v with FirstSession false", got, want)
	}
}

func TestIAPSession_RecordsTheDeviceAsSignInDoes(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "production")
	headers := func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "198.51.100.9, 203.0.113.7, 35.191.0.1")
		r.Header.Set("User-Agent", aeUA)
	}

	signInEvents := &recEvents{}
	fake := newTokenFake(t, http.StatusOK, gtSession)
	th := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, newTestClock().Now)
	signIn := SignInHandler(fake.URL, testClient(), testHandoffStore(), th, nopLog(), nil, signInEvents)
	req := httptest.NewRequest(http.MethodPost, "/auth/sign-in", strings.NewReader(signInBody(aeEmail, "pw", randomState(t))))
	req.Header.Set("Content-Type", "application/json")
	headers(req)
	requireCode(t, serveRecorder(signIn, req))

	rg := newIAPRig(t)
	requireCode(t, mustRec(rg.mint(headers)))

	signInEvents.requireNames(t, EventSignedIn)
	rg.events.requireNames(t, EventSignedIn)
	a, b := signInEvents.events()[0], rg.events.events()[0]
	if a.IP == "" || a.UserAgent == "" {
		t.Fatalf("control: the sign-in event has IP %q and user agent %q, want both", a.IP, a.UserAgent)
	}
	if a.IP != b.IP || a.UserAgent != b.UserAgent {
		t.Errorf("IAP event device = (%q, %q), sign-in's = (%q, %q); want equal", b.IP, b.UserAgent, a.IP, a.UserAgent)
	}
}

func serveRecorder(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIAPSession_RetryStillRecordsOnce(t *testing.T) {
	rg := newIAPRig(t)
	_, staff := iapSessionAnswer(iapStaffMeta)
	rg.verify = func(h string) (int, string) {
		if h == iapHash(1) {
			return http.StatusForbidden, gtOTPExpired
		}
		return http.StatusOK, staff
	}

	rec, _ := rg.mint()

	requireCode(t, rec)
	rg.events.requireNames(t, EventSignedIn)
}

func TestIAPSession_NoEventWithoutACode(t *testing.T) {
	cases := iapFailures()
	if len(cases) < 8 {
		t.Fatalf("%d failure cases, want at least the eight named by the spec", len(cases))
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rg := runFailure(t, c)
			rg.events.requireNames(t)
		})
	}
}

func TestIAPSession_RecorderFailureKeepsTheCode(t *testing.T) {
	rg := newIAPRig(t)
	rg.events.err = errors.New("outbox down")

	rec, state := rg.mint()

	code := requireCode(t, rec)
	if _, ok := rg.store.Take(code, state); !ok {
		t.Error("Take(code, state) = false, want the answer")
	}
	rg.events.requireNames(t, EventSignedIn)
	recs := recordsNamed(t, rg.logBuf, "account-event: record failed")
	if len(recs) != 1 || recs[0]["event"] != EventSignedIn {
		t.Errorf("record-failed log records = %v, want one naming %s", recs, EventSignedIn)
	}
	requireNoLeak(t, rg.logBuf, iapEmail, strings.ToLower(iapEmail), aeEmail)
}
