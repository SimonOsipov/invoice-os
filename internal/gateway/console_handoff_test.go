package gateway

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const msgConsoleHandoffUnavailable = "console hand-off is unavailable"

// consoleRig runs the handler behind the verifier and session check, as main wraps it.
type consoleRig struct {
	handler  http.Handler
	exchange http.Handler
	store    *HandoffStore
	clock    *testClock
	signer   *sidSigner
	mock     *auth.MockIssuer
	user     *userFake
}

func newConsoleRig(t *testing.T, log *slog.Logger) *consoleRig {
	t.Helper()
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
	user := newUserFake(t, http.StatusOK, gtUser)
	clk := newTestClock()
	store := NewHandoffStore(HandoffTTL, clk.Now)
	sessions := NewSessionChecker(user.URL, testClient(), time.Now, log)
	return &consoleRig{
		handler:  verifier.Middleware(sessions.Middleware(auth.RequireStaff(ConsoleHandoffHandler(store, log)))),
		exchange: ExchangeHandler(store),
		store:    store,
		clock:    clk,
		signer:   signer,
		mock:     mock,
		user:     user,
	}
}

func (rg *consoleRig) staffToken(t *testing.T, sid string) string {
	t.Helper()
	return rg.signer.tokenWith(t, subjectS1, sid, map[string]any{"staff": true})
}

func (rg *consoleRig) post(bearer, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/auth/console-handoff", strings.NewReader(body))
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	rg.handler.ServeHTTP(rec, r)
	return rec
}

func (rg *consoleRig) redeem(code, state string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(map[string]string{"code": code, "state": state})
	r := httptest.NewRequest(http.MethodPost, "/auth/exchange", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	rg.exchange.ServeHTTP(rec, r)
	return rec
}

func stateBody(state string) string {
	b, _ := json.Marshal(map[string]string{"state": state})
	return string(b)
}

// mint posts a valid state with bearer and returns the minted code.
func (rg *consoleRig) mint(t *testing.T, bearer, state string) string {
	t.Helper()
	rec := rg.post(bearer, stateBody(state))
	if rec.Code != http.StatusOK {
		t.Fatalf("mint = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !codeShape.MatchString(out["code"]) {
		t.Fatalf("mint body = %s, want {\"code\": 43 base64url}", rec.Body.String())
	}
	return out["code"]
}

func assertErrorBody(t *testing.T, rec *httptest.ResponseRecorder, status int, msg string) {
	t.Helper()
	var body map[string]string
	if rec.Code != status || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body["error"] != msg {
		t.Errorf("answer = %d %s, want %d {\"error\":%q}", rec.Code, rec.Body.String(), status, msg)
	}
}

func TestConsoleHandoff_StaffBearerMintsACodeThatExchangesForThatBearer(t *testing.T) {
	rg := newConsoleRig(t, nil)
	bearer, state := rg.staffToken(t, sid1), randomState(t)

	code := rg.mint(t, bearer, state)
	if got := rg.user.Hits(); got != 1 {
		t.Errorf("GoTrue /user hits = %d, want exactly 1 (the session check ran)", got)
	}

	rec := rg.redeem(code, state)
	if rec.Code != http.StatusOK {
		t.Fatalf("exchange = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("exchange body %q: %v", rec.Body.String(), err)
	}
	if len(got) != 1 || got["access_token"] != bearer {
		t.Errorf("exchange body = %s, want exactly {\"access_token\": <the bearer>}", rec.Body.String())
	}
	if _, has := got["refresh_token"]; has {
		t.Error("exchange body carries refresh_token; the borrowed session must hold none")
	}
}

func TestConsoleHandoff_NonStaffIs403AndStoresNothing(t *testing.T) {
	rg := newConsoleRig(t, nil)
	state := randomState(t)
	cases := map[string]string{
		"no staff claim": rg.signer.tokenWith(t, subjectS1, sid1, map[string]any{"tenant_id": testTenant}),
		"staff false":    rg.signer.tokenWith(t, subjectS1, sid1, map[string]any{"tenant_id": testTenant, "staff": false}),
	}
	for name, bearer := range cases {
		t.Run(name, func(t *testing.T) {
			before := rg.store.LiveForTest()
			rec := rg.post(bearer, stateBody(state))
			assertErrorBody(t, rec, http.StatusForbidden, "forbidden")
			if got := rg.store.LiveForTest(); got != before {
				t.Errorf("live codes = %d, want %d: a refused caller stored a code", got, before)
			}
		})
	}

	// Control: a staff bearer on the same rig mints, so the refusals above are the staff gate's.
	before := rg.store.LiveForTest()
	rg.mint(t, rg.staffToken(t, sid2), state)
	if got := rg.store.LiveForTest(); got != before+1 {
		t.Errorf("control: live codes = %d, want %d", got, before+1)
	}
}

func TestConsoleHandoff_TenantlessStaffMints(t *testing.T) {
	rg := newConsoleRig(t, nil)
	// A mock-issuer token has no session_id, so the session check is skipped; only staff decides.
	bearer, err := rg.mock.Mint(auth.MintOptions{Subject: testSubject, Staff: true})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	before := rg.store.LiveForTest()
	rg.mint(t, bearer, randomState(t))
	if got := rg.store.LiveForTest(); got != before+1 {
		t.Errorf("live codes = %d, want %d", got, before+1)
	}
}

func TestConsoleHandoff_UnauthenticatedCallersGetNoCode(t *testing.T) {
	rg := newConsoleRig(t, nil)
	state := randomState(t)
	otherKey, err := auth.NewMockIssuer(testIssuer)
	if err != nil {
		t.Fatalf("mock issuer: %v", err)
	}
	badSig, _ := otherKey.Mint(auth.MintOptions{Subject: testSubject, Staff: true})
	expired, _ := rg.mock.Mint(auth.MintOptions{Subject: testSubject, Staff: true, TTL: -time.Hour})
	cases := map[string]string{"no bearer": "", "bad signature": badSig, "expired": expired}

	for name, bearer := range cases {
		t.Run(name, func(t *testing.T) {
			before := rg.store.LiveForTest()
			rec := rg.post(bearer, stateBody(state))
			assertErrorBody(t, rec, http.StatusUnauthorized, "unauthorized")
			if got := rg.store.LiveForTest(); got != before {
				t.Errorf("live codes = %d, want %d: an unauthenticated caller stored a code", got, before)
			}
		})
	}

	// Control: the same mock issuer, unexpired and signed by the trusted key, mints.
	good, _ := rg.mock.Mint(auth.MintOptions{Subject: testSubject, Staff: true})
	rg.mint(t, good, state)
}

func TestConsoleHandoff_RevokedSessionGetsNoCode(t *testing.T) {
	rg := newConsoleRig(t, nil)
	state := randomState(t)
	revoked, live := rg.staffToken(t, sid1), rg.staffToken(t, sid2)
	rg.user.setAnswer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") == "Bearer "+revoked {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(gtError(http.StatusForbidden, "session_not_found")))
			return
		}
		_, _ = w.Write([]byte(gtUser))
	})

	before := rg.store.LiveForTest()
	rec := rg.post(revoked, stateBody(state))
	assertErrorBody(t, rec, http.StatusUnauthorized, "unauthorized")
	if got := rg.user.Hits(); got != 1 {
		t.Errorf("GoTrue /user hits = %d, want exactly 1", got)
	}
	if got := rg.store.LiveForTest(); got != before {
		t.Errorf("live codes = %d, want %d: a revoked session stored a code", got, before)
	}

	// Control: a staff token whose session GoTrue still knows mints.
	rg.mint(t, live, state)
	if got := rg.store.LiveForTest(); got != before+1 {
		t.Errorf("control: live codes = %d, want %d", got, before+1)
	}
}

func TestConsoleHandoff_CodeIsSingleUse(t *testing.T) {
	rg := newConsoleRig(t, nil)
	state := randomState(t)
	code := rg.mint(t, rg.staffToken(t, sid1), state)

	if rec := rg.redeem(code, state); rec.Code != http.StatusOK {
		t.Fatalf("first exchange = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	assertErrorBody(t, rg.redeem(code, state), http.StatusBadRequest, "invalid or expired code")
}

func TestConsoleHandoff_CodeExpiresAtTTL(t *testing.T) {
	rg := newConsoleRig(t, nil)
	state, bearer := randomState(t), rg.staffToken(t, sid1)

	live := rg.mint(t, bearer, state)
	rg.clock.Advance(HandoffTTL - time.Nanosecond)
	if rec := rg.redeem(live, state); rec.Code != http.StatusOK {
		t.Fatalf("exchange at TTL-1ns = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	late := rg.mint(t, bearer, state)
	rg.clock.Advance(HandoffTTL)
	assertErrorBody(t, rg.redeem(late, state), http.StatusBadRequest, "invalid or expired code")
}

func TestConsoleHandoff_WrongStateRefusesAndSpends(t *testing.T) {
	rg := newConsoleRig(t, nil)
	s, other := randomState(t), randomState(t)
	bearer := rg.staffToken(t, sid1)
	code := rg.mint(t, bearer, s)
	control := rg.mint(t, bearer, s)

	assertErrorBody(t, rg.redeem(code, other), http.StatusBadRequest, "invalid or expired code")
	assertErrorBody(t, rg.redeem(code, s), http.StatusBadRequest, "invalid or expired code")
	// Control: an untouched code for the same state still redeems.
	if rec := rg.redeem(control, s); rec.Code != http.StatusOK {
		t.Errorf("control exchange = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestConsoleHandoff_BadRequestsAre400(t *testing.T) {
	rg := newConsoleRig(t, nil)
	bearer := rg.staffToken(t, sid1)
	valid := randomState(t)
	cases := []struct{ name, body, msg string }{
		{"not JSON", "not json", "invalid request body"},
		{"empty body", "", "invalid request body"},
		{"state is a number", `{"state":7}`, "invalid request body"},
		{"over 1 KiB", `{"state":"` + valid + `","pad":"` + strings.Repeat("x", 2048) + `"}`, "invalid request body"},
		{"no state", `{}`, "state is required"},
		{"42 characters", stateBody(valid[:42]), "state is required"},
		{"44 characters", stateBody(valid + "A"), "state is required"},
		{"43 characters, not base64url", stateBody(valid[:42] + "+"), "state is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := rg.store.LiveForTest()
			assertErrorBody(t, rg.post(bearer, c.body), http.StatusBadRequest, c.msg)
			if got := rg.store.LiveForTest(); got != before {
				t.Errorf("live codes = %d, want %d: a bad request stored a code", got, before)
			}
		})
	}

	// Control: the 43-character state itself mints, so the 42/44 refusals are about length.
	rg.mint(t, bearer, valid)
}

func TestConsoleHandoff_GetIs405(t *testing.T) {
	rg := newConsoleRig(t, nil)
	r := httptest.NewRequest(http.MethodGet, "/auth/console-handoff", nil)
	r.Header.Set("Authorization", "Bearer "+rg.staffToken(t, sid1))
	rec := httptest.NewRecorder()
	before := rg.store.LiveForTest()
	rg.handler.ServeHTTP(rec, r)

	assertErrorBody(t, rec, http.StatusMethodNotAllowed, "method not allowed")
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow = %q, want POST", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rg.store.LiveForTest(); got != before {
		t.Errorf("live codes = %d, want %d", got, before)
	}
}

// fillToCap tops the store up to HandoffMaxLive live codes.
func fillToCap(t *testing.T, s *HandoffStore) {
	t.Helper()
	fillStore(t, s, HandoffMaxLive-s.LiveForTest())
	if got := s.LiveForTest(); got != HandoffMaxLive {
		t.Fatalf("live codes = %d, want HandoffMaxLive %d", got, HandoffMaxLive)
	}
}

func TestConsoleHandoff_FullStoreIs503(t *testing.T) {
	rg := newConsoleRig(t, nil)
	bearer := rg.staffToken(t, sid1)
	fillToCap(t, rg.store)

	rec := rg.post(bearer, stateBody(randomState(t)))
	assertErrorBody(t, rec, http.StatusServiceUnavailable, msgConsoleHandoffUnavailable)
	if got := rg.store.LiveForTest(); got != HandoffMaxLive {
		t.Errorf("live codes = %d, want %d", got, HandoffMaxLive)
	}
}

func TestConsoleHandoff_LogsNoSecret(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rg := newConsoleRig(t, log)
	bearer, okState, fullState := rg.staffToken(t, sid1), randomState(t), randomState(t)

	code := rg.mint(t, bearer, okState)
	fillToCap(t, rg.store)
	assertErrorBody(t, rg.post(bearer, stateBody(fullState)), http.StatusServiceUnavailable, msgConsoleHandoffUnavailable)

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("log holds no WARN for the full-store 503; nothing to inspect: %q", out)
	}
	for name, secret := range map[string]string{"bearer": bearer, "code": code, "ok state": okState, "full-store state": fullState, "escaped bearer": url.QueryEscape(bearer)} {
		if strings.Contains(out, secret) {
			t.Errorf("log holds the %s %q:\n%s", name, secret, out)
		}
	}
}
