package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	exportStateForTest = "workspace-export:"
	exportDisposition  = `attachment; filename="ASComply_workspace_acme_20261026.zip"`
	exportButton       = `<button type="button">Back to ASComply</button>`

	exportMsgExpired = "This download link has expired. Go back and click Download export again."
	exportMsgSession = "Your session has ended. Sign in again, then download the export."
)

// exportUpstream is the invoice service: it records the last request and answers with answer.
type exportUpstream struct {
	URL *url.URL

	mu       sync.Mutex
	hits     int
	method   string
	path     string
	rawQuery string
	header   http.Header
	bodyLen  int
	answer   http.HandlerFunc
}

func newExportUpstream(t *testing.T) *exportUpstream {
	t.Helper()
	u := &exportUpstream{}
	u.answer = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		u.mu.Lock()
		u.hits++
		u.method, u.path, u.rawQuery, u.header, u.bodyLen = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), int(n)
		answer := u.answer
		u.mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(srv.Close)
	pu, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	u.URL = pu
	return u
}

func (u *exportUpstream) setAnswer(h http.HandlerFunc) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.answer = h
}

func (u *exportUpstream) Hits() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits
}

// exportRig wires the export routes as main does: the code route behind the verifier and the
// session check, the download route in front of the gateway's own /api/ handler.
type exportRig struct {
	store    *HandoffStore
	clk      *testClock
	signer   *sidSigner
	user     *userFake
	up       *exportUpstream
	mint     http.Handler
	download http.Handler
	exchange http.Handler
}

func newExportRig(t *testing.T) *exportRig {
	t.Helper()
	return newExportRigLogging(t, slog.New(slog.DiscardHandler))
}

func newExportRigLogging(t *testing.T, log *slog.Logger) *exportRig {
	t.Helper()
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
	user := newUserFake(t, http.StatusOK, gtUser)
	clk := newTestClock()
	store := NewHandoffStore(HandoffTTL, clk.Now)
	sessions := NewSessionChecker(user.URL, testClient(), clk.Now, log)
	up := newExportUpstream(t)
	api := Handler(Options{
		Verifier:     verifier,
		Sessions:     sessions,
		Upstreams:    map[string]*url.URL{"invoice": up.URL},
		Logger:       log,
		GatewayToken: testGatewayToken,
	})
	return &exportRig{
		store:    store,
		clk:      clk,
		signer:   signer,
		user:     user,
		up:       up,
		mint:     verifier.Middleware(sessions.Middleware(ExportCodeHandler(store, log))),
		download: ExportDownloadHandler(store, api, log),
		exchange: ExchangeHandler(store),
	}
}

func (rg *exportRig) bearer(t *testing.T) string {
	t.Helper()
	return rg.signer.token(t, subjectS1, sid1)
}

func (rg *exportRig) postCode(bearer, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/auth/export-code", strings.NewReader(body))
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	rg.mint.ServeHTTP(rec, r)
	return rec
}

func (rg *exportRig) mintCode(t *testing.T, bearer, state string) string {
	t.Helper()
	rec := rg.postCode(bearer, stateBody(state))
	if rec.Code != http.StatusOK {
		t.Fatalf("mint = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !codeShape.MatchString(out["code"]) {
		t.Fatalf("mint body = %s, want {\"code\": 43 base64url}", rec.Body.String())
	}
	return out["code"]
}

// seed stores a code as the mint route would, so a redeem test fails on the redeem route alone.
func (rg *exportRig) seed(t *testing.T, bearer, state string) string {
	t.Helper()
	answer, _ := json.Marshal(map[string]string{"access_token": bearer})
	code, ok := rg.store.Put(string(answer), stateHash(exportStateForTest+state))
	if !ok {
		t.Fatal("store refused a seeded code")
	}
	return code
}

func formRequest(target string, form url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func (rg *exportRig) redeem(code, state string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	rg.download.ServeHTTP(rec, formRequest("/auth/export-download", url.Values{"code": {code}, "state": {state}}))
	return rec
}

func assertRefusalPage(t *testing.T, rec *httptest.ResponseRecorder, status int, sentence string) {
	t.Helper()
	body := rec.Body.String()
	if rec.Code != status {
		t.Errorf("status = %d, want %d: %.200s", rec.Code, status, body)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
	}
	if !strings.Contains(body, sentence) {
		t.Errorf("body lacks %q: %.300s", sentence, body)
	}
	if !strings.Contains(body, exportButton) {
		t.Errorf("body lacks %q", exportButton)
	}
}

func TestExportCode_MintsForAVerifiedBearer(t *testing.T) {
	rg := newExportRig(t)
	bearer, state := rg.bearer(t), strings.Repeat("a", 43)

	rec := rg.postCode(bearer, stateBody(state))
	if rec.Code != http.StatusOK {
		t.Fatalf("mint = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out) != 1 || len(out["code"]) != 43 {
		t.Fatalf("mint body = %s, want exactly {\"code\": <43 characters>}", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rg.store.LiveForTest(); got != 1 {
		t.Errorf("live codes = %d, want 1", got)
	}
	if got := rg.user.Hits(); got != 1 {
		t.Errorf("GoTrue /user hits = %d, want 1 (the session check ran)", got)
	}
	// The answer is the caller's own access token, bound to the prefixed state.
	answer, ok := rg.store.Take(out["code"], exportStateForTest+state)
	var stored map[string]string
	if !ok || json.Unmarshal([]byte(answer), &stored) != nil || len(stored) != 1 || stored["access_token"] != bearer {
		t.Errorf("stored answer = (%q, %v), want ({\"access_token\": <the bearer>}, true) under %q+state", answer, ok, exportStateForTest)
	}
}

func TestExportCode_RefusesNoBearerABadStateAndAFullStore(t *testing.T) {
	rg := newExportRig(t)
	bearer, state := rg.bearer(t), randomState(t)

	rg.mintCode(t, bearer, state) // control: the rig mints
	base := rg.store.LiveForTest()
	if base != 1 {
		t.Fatalf("live codes after the control mint = %d, want 1", base)
	}

	cases := []struct {
		name   string
		bearer string
		body   string
		status int
		msg    string
	}{
		{"no bearer", "", stateBody(state), http.StatusUnauthorized, "unauthorized"},
		{"short state", bearer, stateBody("short"), http.StatusBadRequest, "state is required"},
		{"empty state", bearer, stateBody(""), http.StatusBadRequest, "state is required"},
		{"broken json", bearer, "{", http.StatusBadRequest, "invalid request body"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertErrorBody(t, rg.postCode(c.bearer, c.body), c.status, c.msg)
			if got := rg.store.LiveForTest(); got != base {
				t.Errorf("live codes = %d, want %d: a refused call stored a code", got, base)
			}
		})
	}

	t.Run("get", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/auth/export-code", nil)
		r.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		rg.mint.ServeHTTP(rec, r)
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
			t.Errorf("GET = %d Allow %q, want 405 POST", rec.Code, rec.Header().Get("Allow"))
		}
	})

	t.Run("full store", func(t *testing.T) {
		fillToCap(t, rg.store)
		assertErrorBody(t, rg.postCode(bearer, stateBody(state)), http.StatusServiceUnavailable, "the export is unavailable")
		if got := rg.store.LiveForTest(); got != HandoffMaxLive {
			t.Errorf("live codes = %d, want %d", got, HandoffMaxLive)
		}
	})
}

func TestExportDownload_ServesTheExportWithTheStoredIdentity(t *testing.T) {
	rg := newExportRig(t)
	want := make([]byte, 200<<10)
	for i := range want {
		want[i] = byte(i * 7)
	}
	rg.up.setAnswer(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", exportDisposition)
		_, _ = w.Write(want)
	})
	state := randomState(t)
	code := rg.mintCode(t, rg.bearer(t), state)

	r := formRequest("/auth/export-download?a=1", url.Values{"code": {code}, "state": {state}})
	r.Header.Set("Cookie", "x=1")
	r.Header.Set("X-User-ID", "forged")
	r.Header.Set("X-User-Email", "forged@evil.example")
	r.Header.Set("Authorization", "Bearer forged")
	rec := httptest.NewRecorder()
	rg.download.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("redeem = %d, want 200: %.200s", rec.Code, rec.Body.String())
	}
	rg.up.mu.Lock()
	defer rg.up.mu.Unlock()
	if rg.up.hits != 1 || rg.up.method != http.MethodGet || rg.up.path != "/v1/workspace/export" || rg.up.rawQuery != "" {
		t.Errorf("upstream saw %d hit(s), %s %s?%s; want 1 hit, GET /v1/workspace/export with no query", rg.up.hits, rg.up.method, rg.up.path, rg.up.rawQuery)
	}
	if rg.up.bodyLen != 0 {
		t.Errorf("upstream read %d body bytes, want 0", rg.up.bodyLen)
	}
	h := rg.up.header
	if h.Get(headerUserID) != subjectS1 || h.Get(headerTenantID) != testTenant {
		t.Errorf("upstream identity = (%q, %q), want (%q, %q) from the stored token", h.Get(headerUserID), h.Get(headerTenantID), subjectS1, testTenant)
	}
	if h.Get("Cookie") != "" || h.Get("Content-Type") != "" || strings.Contains(h.Get(headerUserEmail), "forged") {
		t.Errorf("upstream got client headers: Cookie %q, Content-Type %q, X-User-Email %q", h.Get("Cookie"), h.Get("Content-Type"), h.Get(headerUserEmail))
	}
	if got := rec.Header().Get("Content-Type"); got != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != exportDisposition {
		t.Errorf("Content-Disposition = %q, want %q", got, exportDisposition)
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Errorf("body = %d bytes, want the upstream's %d bytes unchanged", rec.Body.Len(), len(want))
	}
}

func TestExportDownload_ACodeRedeemsOnce(t *testing.T) {
	rg := newExportRig(t)
	state := randomState(t)
	code := rg.seed(t, rg.bearer(t), state)

	if rec := rg.redeem(code, state); rec.Code != http.StatusOK {
		t.Fatalf("first redeem = %d, want 200: %.200s", rec.Code, rec.Body.String())
	}
	assertRefusalPage(t, rg.redeem(code, state), http.StatusBadRequest, exportMsgExpired)
	if got := rg.up.Hits(); got != 1 {
		t.Errorf("upstream hits = %d, want 1", got)
	}
}

func TestExportDownload_AWrongStateSpendsTheCode(t *testing.T) {
	rg := newExportRig(t)
	stateA, stateB := randomState(t), randomState(t)
	code := rg.seed(t, rg.bearer(t), stateA)

	assertRefusalPage(t, rg.redeem(code, stateB), http.StatusBadRequest, exportMsgExpired)
	assertRefusalPage(t, rg.redeem(code, stateA), http.StatusBadRequest, exportMsgExpired)
	if got := rg.up.Hits(); got != 0 {
		t.Errorf("upstream hits = %d, want 0", got)
	}
}

func TestExportDownload_CodesDoNotCrossRoutes(t *testing.T) {
	rg := newExportRig(t)
	state := randomState(t)

	code := rg.seed(t, rg.bearer(t), state)
	b, _ := json.Marshal(map[string]string{"code": code, "state": state})
	rec := httptest.NewRecorder()
	rg.exchange.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/exchange", bytes.NewReader(b)))
	assertErrorBody(t, rec, http.StatusBadRequest, "invalid or expired code")

	// A sign-in code is bound to sha256(state), with no prefix.
	signInCode, ok := rg.store.Put(`{"access_token":"`+rg.bearer(t)+`"}`, stateHash(state))
	if !ok {
		t.Fatal("store refused a sign-in style code")
	}
	assertRefusalPage(t, rg.redeem(signInCode, state), http.StatusBadRequest, exportMsgExpired)
	if got := rg.up.Hits(); got != 0 {
		t.Errorf("upstream hits = %d, want 0 after both cross-route attempts", got)
	}

	// Control: an export code redeems on its own route.
	own := rg.mintCode(t, rg.bearer(t), state)
	if rec := rg.redeem(own, state); rec.Code != http.StatusOK {
		t.Errorf("control redeem = %d, want 200: %.200s", rec.Code, rec.Body.String())
	}
}

func TestExportDownload_ARevokedSessionIs401(t *testing.T) {
	rg := newExportRig(t)
	state := randomState(t)
	code := rg.seed(t, rg.bearer(t), state)

	rg.user.setAnswer(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(gtError(http.StatusForbidden, "session_not_found")))
	})

	rec := rg.redeem(code, state)
	assertRefusalPage(t, rec, http.StatusUnauthorized, exportMsgSession)
	if got := rec.Header().Get("WWW-Authenticate"); got != "" {
		t.Errorf("WWW-Authenticate = %q, want none on the refusal page", got)
	}
	if got := rg.user.Hits(); got != 1 {
		t.Errorf("GoTrue /user hits = %d, want 1 (the redeem's own session check)", got)
	}
	if got := rg.up.Hits(); got != 0 {
		t.Errorf("upstream hits = %d, want 0", got)
	}
}

func TestExportDownload_AnExpiredCodeIs400(t *testing.T) {
	rg := newExportRig(t)
	state := randomState(t)

	// Control: one tick inside the TTL still redeems.
	live := rg.seed(t, rg.bearer(t), state)
	rg.clk.Advance(HandoffTTL - time.Second)
	if rec := rg.redeem(live, state); rec.Code != http.StatusOK {
		t.Fatalf("redeem inside the TTL = %d, want 200: %.200s", rec.Code, rec.Body.String())
	}

	late := rg.seed(t, rg.bearer(t), state)
	rg.clk.Advance(HandoffTTL)
	assertRefusalPage(t, rg.redeem(late, state), http.StatusBadRequest, exportMsgExpired)
	if got := rg.up.Hits(); got != 1 {
		t.Errorf("upstream hits = %d, want 1 (the control only)", got)
	}
}

func TestExportDownload_RefusesAnOversizeFormAndGET(t *testing.T) {
	rg := newExportRig(t)
	state := randomState(t)
	code := rg.seed(t, rg.bearer(t), state)

	form := url.Values{"code": {code}, "state": {state}, "pad": {strings.Repeat("x", 5<<10)}}
	rec := httptest.NewRecorder()
	rg.download.ServeHTTP(rec, formRequest("/auth/export-download", form))
	assertRefusalPage(t, rec, http.StatusBadRequest, exportMsgExpired)
	if got := rg.up.Hits(); got != 0 {
		t.Errorf("upstream hits = %d after an oversize form carrying a live pair, want 0", got)
	}

	rec = httptest.NewRecorder()
	rg.download.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/export-download", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
		t.Errorf("GET = %d Allow %q, want 405 POST", rec.Code, rec.Header().Get("Allow"))
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("405 body = %q, want the JSON error (no form sends a GET)", rec.Body.String())
	}
}

func TestExportDownload_ReadsTheCodeFromTheBodyOnly(t *testing.T) {
	rg := newExportRig(t)
	state := randomState(t)
	code := rg.seed(t, rg.bearer(t), state)

	r := httptest.NewRequest(http.MethodPost, "/auth/export-download?code="+code+"&state="+state, nil)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	rg.download.ServeHTTP(rec, r)
	assertRefusalPage(t, rec, http.StatusBadRequest, exportMsgExpired)
	if got := rg.up.Hits(); got != 0 {
		t.Fatalf("upstream hits = %d after a query-only pair, want 0", got)
	}

	// The same pair in the body still works: the query attempt did not spend the code.
	if rec := rg.redeem(code, state); rec.Code != http.StatusOK {
		t.Errorf("body redeem = %d, want 200 (the query-only attempt spent the code): %.200s", rec.Code, rec.Body.String())
	}
}

func TestExportDownload_AnUpstreamAbortReachesTheClient(t *testing.T) {
	rg := newExportRig(t)
	rg.up.setAnswer(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(bytes.Repeat([]byte("z"), 10<<10))
		panic(http.ErrAbortHandler)
	})
	srv := httptest.NewServer(rg.download)
	t.Cleanup(srv.Close)
	state := randomState(t)
	code := rg.seed(t, rg.bearer(t), state)

	resp, err := http.PostForm(srv.URL+"/auth/export-download", url.Values{"code": {code}, "state": {state}})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the abort comes after the first byte)", resp.StatusCode)
	}
	if _, err := io.ReadAll(resp.Body); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("ReadAll error = %v, want io.ErrUnexpectedEOF: a broken export must not end as a clean body", err)
	}
}

// deadlineRecorder is a connection stand-in: it records Flush and SetWriteDeadline.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline time.Time
	flushes  int
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error { d.deadline = t; return nil }

func (d *deadlineRecorder) Flush() { d.flushes++; d.ResponseRecorder.Flush() }

func TestExportDownload_ExtendsItsWriteDeadline(t *testing.T) {
	store := NewHandoffStore(HandoffTTL, time.Now)
	state := randomState(t)
	code, _ := store.Put(`{"access_token":"tok"}`, stateHash(exportStateForTest+state))
	dr := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	var atCall time.Time
	calls := 0
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		atCall = dr.deadline
		w.WriteHeader(http.StatusOK)
	})

	before := time.Now()
	ExportDownloadHandler(store, api, slog.New(slog.DiscardHandler)).ServeHTTP(dr, formRequest("/auth/export-download", url.Values{"code": {code}, "state": {state}}))

	if calls != 1 {
		t.Fatalf("api calls = %d, want 1 (status %d)", calls, dr.Code)
	}
	if atCall.IsZero() {
		t.Fatal("the write deadline was not set before the handler delegated")
	}
	if atCall.Before(before.Add(59 * time.Minute)) {
		t.Errorf("deadline = %v, want at least 59 minutes after %v", atCall, before)
	}
}

func TestExportDownload_RefusalsAnswerThePage(t *testing.T) {
	sentences := map[int]string{
		http.StatusBadRequest:          exportMsgExpired,
		http.StatusUnauthorized:        exportMsgSession,
		http.StatusForbidden:           "Only an active admin can download the workspace export.",
		http.StatusConflict:            "An export of this workspace is already running. Wait for it to finish, then try again.",
		http.StatusServiceUnavailable:  "Another workspace export is running. Try again in a few minutes.",
		http.StatusInternalServerError: "The export could not start. Try again.",
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusServiceUnavailable, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			rg := newExportRig(t)
			rg.up.setAnswer(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Disposition", `attachment; filename="leak.zip"`)
				w.Header().Set("X-Leak", "yes")
				if status == http.StatusServiceUnavailable {
					w.Header().Set("Retry-After", "60")
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"upstream words"}`))
			})
			state := randomState(t)
			rec := rg.redeem(rg.seed(t, rg.bearer(t), state), state)

			assertRefusalPage(t, rec, status, sentences[status])
			if got := rg.up.Hits(); got != 1 {
				t.Errorf("upstream hits = %d, want 1 (the refusal is the upstream's)", got)
			}
			assertNoUpstreamLeak(t, rec)
		})
	}

	t.Run("the gateway's own 400", func(t *testing.T) {
		rg := newExportRig(t)
		state := randomState(t)
		code := rg.seed(t, rg.bearer(t), state)
		if rec := rg.redeem(code, state); rec.Code != http.StatusOK {
			t.Fatalf("first redeem = %d, want 200", rec.Code)
		}
		rec := rg.redeem(code, state)
		assertRefusalPage(t, rec, http.StatusBadRequest, sentences[http.StatusBadRequest])
		assertNoUpstreamLeak(t, rec)
	})
}

func assertNoUpstreamLeak(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	for _, h := range []string{"Retry-After", "Content-Disposition", "X-Leak"} {
		if got := rec.Header().Get(h); got != "" {
			t.Errorf("%s = %q, want none on the refusal page", h, got)
		}
	}
	body := rec.Body.String()
	if strings.Contains(body, `{"error"`) || strings.Contains(body, "upstream words") {
		t.Errorf("body carries the upstream's bytes: %.300s", body)
	}
	if n := strings.Count(body, "<button"); n != 1 {
		t.Errorf("page holds %d buttons, want exactly 1", n)
	}
}

var pageScript = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

func TestExportDownload_ThePageScriptMatchesItsCSPHash(t *testing.T) {
	rg := newExportRig(t)
	rec := rg.redeem("unknown-code", randomState(t))
	assertRefusalPage(t, rec, http.StatusBadRequest, exportMsgExpired)

	m := pageScript.FindStringSubmatch(rec.Body.String())
	if m == nil || strings.TrimSpace(m[1]) == "" {
		t.Fatalf("page holds no script: %.300s", rec.Body.String())
	}
	sum := sha256.Sum256([]byte(m[1]))
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{
		"script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'",
		"default-src 'none'",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP = %q, want it to hold %q", csp, want)
		}
	}
}

func TestRefusalPageWriter_PassesA200AndReachesTheConnection(t *testing.T) {
	payload := []byte("PK\x03\x04 zip bytes \x00\xff")
	dr := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	w := &refusalPageWriter{ResponseWriter: dr}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", exportDisposition)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if dr.Code != http.StatusOK || !bytes.Equal(dr.Body.Bytes(), payload) {
		t.Errorf("recorded %d, %d bytes; want 200 and the payload byte for byte", dr.Code, dr.Body.Len())
	}
	if dr.Header().Get("Content-Type") != "application/zip" || dr.Header().Get("Content-Disposition") != exportDisposition {
		t.Errorf("headers = %v, want the upstream's passed through", dr.Header())
	}

	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil || dr.flushes != 1 {
		t.Errorf("Flush = (%v, %d reached), want (nil, 1)", err, dr.flushes)
	}
	want := time.Now().Add(time.Hour)
	if err := rc.SetWriteDeadline(want); err != nil || !dr.deadline.Equal(want) {
		t.Errorf("SetWriteDeadline = (%v, %v reached), want (nil, %v)", err, dr.deadline, want)
	}

	// A Write before any WriteHeader is a 200.
	dr2 := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	w2 := &refusalPageWriter{ResponseWriter: dr2}
	if _, err := w2.Write(payload); err != nil {
		t.Fatalf("Write without WriteHeader: %v", err)
	}
	if !bytes.Equal(dr2.Body.Bytes(), payload) || dr2.Code != http.StatusOK {
		t.Errorf("headerless write recorded %d, %d bytes; want 200 and the payload", dr2.Code, dr2.Body.Len())
	}
}

// expiredBearer is signed by the rig's issuer with an exp one hour in the past.
func (rg *exportRig) expiredBearer(t *testing.T) string {
	t.Helper()
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": sidIssuer, "sub": subjectS1, "aud": "authenticated", "role": testRole, "session_id": sid1,
		"iat": now.Add(-2 * time.Hour).Unix(), "exp": now.Add(-time.Hour).Unix(),
		"app_metadata": map[string]any{"tenant_id": testTenant},
	})
	tok.Header["kid"] = rg.signer.kid
	out, err := tok.SignedString(rg.signer.key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return out
}

func TestExportCode_RefusesARevokedOrExpiredSessionAndStoresNothing(t *testing.T) {
	rg := newExportRig(t)
	state := randomState(t)
	rg.mintCode(t, rg.bearer(t), state) // control: the rig mints
	base := rg.store.LiveForTest()

	assertErrorBody(t, rg.postCode(rg.expiredBearer(t), stateBody(state)), http.StatusUnauthorized, "unauthorized")
	if got := rg.store.LiveForTest(); got != base {
		t.Errorf("live codes = %d after an expired token, want %d", got, base)
	}

	rg.user.setAnswer(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(gtError(http.StatusForbidden, "session_not_found")))
	})
	rg.clk.Advance(SessionCheckTTL + time.Second) // past the cached live verdict
	assertErrorBody(t, rg.postCode(rg.bearer(t), stateBody(state)), http.StatusUnauthorized, "unauthorized")
	if got := rg.store.LiveForTest(); got != base {
		t.Errorf("live codes = %d after a revoked session, want %d", got, base)
	}
}

func TestExportDownload_ATokenThatExpiredAfterTheMintIsRefusedAs401(t *testing.T) {
	rg := newExportRig(t)
	state := randomState(t)
	code := rg.seed(t, rg.expiredBearer(t), state)

	assertRefusalPage(t, rg.redeem(code, state), http.StatusUnauthorized, exportMsgSession)
	if got := rg.up.Hits(); got != 0 {
		t.Errorf("upstream hits = %d, want 0", got)
	}
}

func TestExportDownload_RefusalPageRendersNoRequestValue(t *testing.T) {
	rg := newExportRig(t)
	const hostile = `"><script>alert(1)</script>`
	form := url.Values{"code": {hostile}, "state": {hostile}, "message": {hostile}}
	r := formRequest("/auth/export-download?code="+url.QueryEscape(hostile)+"&message="+url.QueryEscape(hostile), form)
	r.Header.Set("Referer", hostile)
	rec := httptest.NewRecorder()
	rg.download.ServeHTTP(rec, r)

	assertRefusalPage(t, rec, http.StatusBadRequest, exportMsgExpired)
	body := rec.Body.String()
	if strings.Contains(body, "alert(1)") || strings.Contains(body, hostile) {
		t.Errorf("refusal page echoes a request value: %.400s", body)
	}
	if n := len(pageScript.FindAllString(body, -1)); n != 1 {
		t.Errorf("page holds %d <script> elements, want only the pinned one", n)
	}
}

func TestExportDownload_ANonFormBodyIsRefusedAndSpendsNothing(t *testing.T) {
	rg := newExportRig(t)
	state := randomState(t)
	code := rg.seed(t, rg.bearer(t), state)

	for name, c := range map[string]struct{ ctype, body string }{
		"json":      {"application/json", `{"code":"` + code + `","state":"` + state + `"}`},
		"plain":     {"text/plain", "code=" + code + "&state=" + state},
		"multipart": {"multipart/form-data; boundary=x", "--x\r\nContent-Disposition: form-data; name=\"code\"\r\n\r\n" + code + "\r\n--x--\r\n"},
	} {
		r := httptest.NewRequest(http.MethodPost, "/auth/export-download", strings.NewReader(c.body))
		r.Header.Set("Content-Type", c.ctype)
		rec := httptest.NewRecorder()
		rg.download.ServeHTTP(rec, r)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s body = %d, want 400 refusal page", name, rec.Code)
		}
	}
	if got := rg.up.Hits(); got != 0 {
		t.Fatalf("upstream hits = %d after non-form bodies, want 0", got)
	}
	if rec := rg.redeem(code, state); rec.Code != http.StatusOK {
		t.Errorf("urlencoded redeem = %d, want 200 (a non-form body spent the code)", rec.Code)
	}
}

// The form cap is 4 KiB: exactly 4096 encoded bytes redeem, 4097 do not.
func TestExportDownload_TheFormCapIsExactly4KiB(t *testing.T) {
	rg := newExportRig(t)
	state := randomState(t)
	send := func(total int) *httptest.ResponseRecorder {
		code := rg.seed(t, rg.bearer(t), state)
		base := url.Values{"code": {code}, "state": {state}}.Encode() + "&pad="
		r := httptest.NewRequest(http.MethodPost, "/auth/export-download", strings.NewReader(base+strings.Repeat("x", total-len(base))))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		rg.download.ServeHTTP(rec, r)
		return rec
	}
	if rec := send(4096); rec.Code != http.StatusOK {
		t.Errorf("4096-byte form = %d, want 200", rec.Code)
	}
	assertRefusalPage(t, send(4097), http.StatusBadRequest, exportMsgExpired)
}

func TestExportDownload_ARaceRedeemsOnce(t *testing.T) {
	rg := newExportRig(t)
	state := randomState(t)
	code := rg.seed(t, rg.bearer(t), state)

	const n = 16
	var ok, refused atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			switch rec := rg.redeem(code, state); rec.Code {
			case http.StatusOK:
				ok.Add(1)
			case http.StatusBadRequest:
				refused.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if ok.Load() != 1 || refused.Load() != n-1 || rg.up.Hits() != 1 {
		t.Errorf("%d concurrent redeems: %d ok, %d refused, %d upstream hits; want 1, %d, 1", n, ok.Load(), refused.Load(), rg.up.Hits(), n-1)
	}
}

func TestExportDownload_AnUpstreamThatDiesBeforeTheFirstByteAnswersThe502Page(t *testing.T) {
	rg := newExportRig(t)
	rg.up.setAnswer(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })
	state := randomState(t)
	rec := rg.redeem(rg.seed(t, rg.bearer(t), state), state)

	assertRefusalPage(t, rec, http.StatusBadGateway, exportRefusalDefault)
	assertNoUpstreamLeak(t, rec)
	if strings.Contains(rec.Body.String(), `"error"`) || strings.Contains(rec.Body.String(), "bad gateway") {
		t.Errorf("502 body carries the gateway's JSON words: %.300s", rec.Body.String())
	}
}

// A sign-in code never redeems here; the converse is D6's accepted residue: /auth/exchange does not
// shape-check the state, so an export code redeems there under the prefixed state, for the holder of both.
func TestExportDownload_ExchangeRedeemsAnExportCodeOnlyUnderThePrefixedState(t *testing.T) {
	rg := newExportRig(t)
	state := randomState(t)
	bearer := rg.bearer(t)
	code := rg.seed(t, bearer, state)

	ex := func(code, state string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"code": code, "state": state})
		rec := httptest.NewRecorder()
		rg.exchange.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/exchange", bytes.NewReader(b)))
		return rec
	}
	rec := ex(code, exportStateForTest+state)
	var got map[string]string
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got["access_token"] != bearer || len(got) != 1 {
		t.Errorf("exchange under the prefixed state = %d %s, want 200 with only the holder's own access token", rec.Code, rec.Body.String())
	}
	if again := ex(code, exportStateForTest+state); again.Code != http.StatusBadRequest {
		t.Errorf("second exchange = %d, want 400", again.Code)
	}
}

// A code, state, bearer or stored answer must reach no log line, at any level.
func TestExportRoutes_NeverLogACodeAStateOrAToken(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	log := slog.New(slog.NewTextHandler(&lockedWriter{mu: &mu, w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rg := newExportRigLogging(t, log)
	bearer := rg.bearer(t)
	var secrets []string
	note := func(vs ...string) { secrets = append(secrets, vs...) }

	state := randomState(t)
	code := rg.mintCode(t, bearer, state)
	note(state, code, bearer)
	_ = rg.redeem(code, state) // 200
	_ = rg.redeem(code, state) // spent

	s2 := randomState(t)
	c2 := rg.seed(t, bearer, s2)
	_ = rg.redeem(c2, randomState(t)) // wrong state
	note(s2, c2)

	s3 := randomState(t)
	c3, _ := rg.store.Put("not json "+bearer, stateHash(exportStateForTest+s3)) // unusable stored answer
	_ = rg.redeem(c3, s3)
	note(s3, c3)

	s4 := randomState(t)
	rg.up.setAnswer(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	_ = rg.redeem(rg.seed(t, bearer, s4), s4)
	note(s4)

	rg.user.setAnswer(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(gtError(http.StatusForbidden, "session_not_found")))
	})
	rg.clk.Advance(SessionCheckTTL + time.Second)
	s5 := randomState(t)
	_ = rg.redeem(rg.seed(t, bearer, s5), s5)
	_ = rg.postCode(bearer, stateBody(s5))
	note(s5)

	full := newExportRigLogging(t, log) // a fresh session cache: the revoked verdict above is still cached
	fillToCap(t, full.store)
	s6 := randomState(t)
	_ = full.postCode(full.bearer(t), stateBody(s6)) // full store: the one warning the route logs
	note(s6)

	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if !strings.Contains(out, "export code: code store is full") || !strings.Contains(out, "stored answer is unusable") {
		t.Fatalf("control: the capture holds neither expected warning, so it is blind: %q", out)
	}
	for _, sec := range secrets {
		if sec != "" && strings.Contains(out, sec) {
			t.Errorf("log holds a secret (%.8s...): %s", sec, out)
		}
	}
	if strings.Contains(out, "access_token") {
		t.Errorf("log holds a stored answer: %s", out)
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func TestExportDownload_AStoredAnswerWithoutATokenIsRefused(t *testing.T) {
	rg := newExportRig(t)
	for _, answer := range []string{`{}`, `{"access_token":""}`, `[]`} {
		state := randomState(t)
		code, ok := rg.store.Put(answer, stateHash(exportStateForTest+state))
		if !ok {
			t.Fatal("store refused a code")
		}
		assertRefusalPage(t, rg.redeem(code, state), http.StatusBadRequest, exportMsgExpired)
	}
	if got := rg.up.Hits(); got != 0 {
		t.Errorf("upstream hits = %d, want 0", got)
	}
}
