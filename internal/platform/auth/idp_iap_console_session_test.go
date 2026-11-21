package auth_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/consolesession"
	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth/gotrueadmin"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth/iaptest"
)

func TestIdP_IAPConsoleSession_UnknownAddressCreatesNoAccount(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	admin := adminClient(t, base)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const countSQL = `SELECT count(*) FROM auth.users WHERE lower(email) = lower($1)`

	// Control: a registered address gets a link, so ErrNoAccount below is GoTrue's answer for the unknown one.
	known := signUp(t, conn, base)
	link, err := admin.GenerateRecoveryLink(ctx, known.email)
	if err != nil || link.TokenHash == "" {
		t.Fatalf("control: GenerateRecoveryLink for a registered address = %+v, %v; want a token hash", link, err)
	}

	unknown := "iap-unknown-" + uuid.NewString() + "@example.test"
	if n := countAuthRows(t, conn, countSQL, unknown); n != 0 {
		t.Fatalf("control: %d accounts exist for the unknown address before the call", n)
	}
	got, err := admin.GenerateRecoveryLink(ctx, unknown)
	if !errors.Is(err, gotrueadmin.ErrNoAccount) {
		t.Errorf("GenerateRecoveryLink for an unknown address = %+v, %v; want ErrNoAccount", got, err)
	}
	if n := countAuthRows(t, conn, countSQL, unknown); n != 0 {
		t.Errorf("auth.users holds %d rows for the unknown address after the call, want 0", n)
	}
}

const (
	iapStackAud    = "/projects/111111111111/global/backendServices/1111111111111111111"
	iapStackOrigin = "https://ops.example"
	iapWorkerRoute = "/internal/console-sessions/recovery-token"
)

// iapTransport records each GoTrue call the gateway makes and counts the /verify calls that opened a session.
type iapTransport struct {
	mu       sync.Mutex
	seen     []string // "METHOD /path?query status"
	verified atomic.Int32
}

func (rt *iapTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	rt.mu.Lock()
	rt.seen = append(rt.seen, fmt.Sprintf("%s %s?%s %d", req.Method, req.URL.Path, req.URL.RawQuery, resp.StatusCode))
	rt.mu.Unlock()
	if req.URL.Path == "/verify" && resp.StatusCode == http.StatusOK {
		rt.verified.Add(1)
	}
	return resp, nil
}

func (rt *iapTransport) calls(prefix string) []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var out []string
	for _, s := range rt.seen {
		if strings.HasPrefix(s, prefix) {
			out = append(out, s)
		}
	}
	return out
}

// iapStack is the gateway's IAP route wired to the real consolesession handler and the real GoTrue at base.
type iapStack struct {
	signer   *iaptest.Signer
	store    *gateway.HandoffStore
	gate     http.Handler
	exchange http.Handler
	rt       *iapTransport
}

// newIAPStack wires the stack. With overlap set, the first two worker calls both generate a hash before
// either returns, and a third waits until some /verify has opened a session: two mints always overlap
// and the loser's retry cannot spend the winner's hash.
func newIAPStack(t *testing.T, base string, events gateway.AccountEvents, overlap bool) *iapStack {
	t.Helper()
	signer, jwks := iaptest.NewSigner(t)
	v, err := auth.NewIAPVerifier(auth.IAPConfig{Audiences: []string{iapStackAud}, JWKSURL: jwks})
	if err != nil {
		t.Fatalf("NewIAPVerifier: %v", err)
	}
	log := slog.New(slog.DiscardHandler)
	s := &iapStack{signer: signer, store: gateway.NewHandoffStore(gateway.HandoffTTL, time.Now), rt: &iapTransport{}}

	recovery := consolesession.RecoveryTokenHandler(v, adminClient(t, base), time.Now, log)
	var calls, generated atomic.Int32
	waitFor := func(cond func() bool) {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && !cond(); {
			time.Sleep(10 * time.Millisecond)
		}
	}
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !overlap {
			recovery.ServeHTTP(w, r)
			return
		}
		n := calls.Add(1)
		if n >= 3 {
			waitFor(func() bool { return s.rt.verified.Load() >= 1 })
		}
		rec := httptest.NewRecorder()
		recovery.ServeHTTP(rec, r)
		if n <= 2 {
			generated.Add(1)
			waitFor(func() bool { return generated.Load() >= 2 })
		}
		maps.Copy(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(worker.Close)
	workerURL, _ := url.Parse(worker.URL)
	authURL, _ := url.Parse(base)
	client := &http.Client{Timeout: 20 * time.Second, Transport: s.rt, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	s.gate = gateway.IAPSessionHandler(v, workerURL, &http.Client{Timeout: 30 * time.Second}, "gateway-token-for-idp-tests", authURL, client, s.store, log, events)
	cookies, err := gateway.ParseSessionOrigins("ops=" + iapStackOrigin)
	if err != nil {
		t.Fatal(err)
	}
	s.exchange = gateway.ExchangeHandler(s.store, cookies)
	return s
}

func (s *iapStack) assertion(email string) string {
	now := time.Now()
	return s.signer.Sign(map[string]any{"iss": auth.IAPIssuer, "aud": iapStackAud, "sub": "accounts.google.com:118273645546372819", "email": email,
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(10 * time.Minute).Unix()})
}

// mint posts a mint for email with state; it never fails the test, so goroutines may call it.
func (s *iapStack) mint(email, state string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/auth/iap-session", strings.NewReader(`{"state":"`+state+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Iap-Jwt-Assertion", s.assertion(email))
	rec := httptest.NewRecorder()
	s.gate.ServeHTTP(rec, req)
	return rec
}

func iapState() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// code asserts a 200 whose body is exactly {code}.
func iapCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var m map[string]string
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &m) != nil || len(m) != 1 || m["code"] == "" {
		t.Fatalf("mint = %d %s, want 200 with exactly {code}", rec.Code, rec.Body.String())
	}
	return m["code"]
}

// requireIAPNoStaff asserts the 403 body and that no code was minted.
func requireIAPNoStaff(t *testing.T, label string, rec *httptest.ResponseRecorder, email string) {
	t.Helper()
	var got map[string]string
	if rec.Code != http.StatusForbidden || json.Unmarshal(rec.Body.Bytes(), &got) != nil ||
		!maps.Equal(got, map[string]string{"error": "no staff account", "email": email}) {
		t.Fatalf("%s: %d %s, want 403 {error: no staff account, email: %q}", label, rec.Code, rec.Body.String(), email)
	}
}

// redeem exchanges code in cookie mode and returns the access token; it asserts the refresh cookie is set.
func (s *iapStack) redeem(t *testing.T, code, state string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code, "state": state})
	req := httptest.NewRequest(http.MethodPost, "/auth/exchange", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(gateway.SessionHeader, "cookie")
	req.Header.Set("Origin", iapStackOrigin)
	rec := httptest.NewRecorder()
	s.exchange.ServeHTTP(rec, req)
	var got struct {
		AccessToken string `json:"access_token"`
		Renewable   bool   `json:"renewable"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.AccessToken == "" || !got.Renewable {
		t.Fatalf("exchange = %d %s, want 200 with an access token and renewable true", rec.Code, rec.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "__Host-rt-ops" {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value == "" || !cookie.HttpOnly || !cookie.Secure {
		t.Fatalf("exchange set cookie %+v, want a non-empty HttpOnly Secure __Host-rt-ops", cookie)
	}
	return got.AccessToken
}

// staffUser is a signed-up account with the house staff grant.
func staffUser(t *testing.T, conn *pgx.Conn, base string) idpUser {
	t.Helper()
	u := signUp(t, conn, base)
	grantHouseStaff(t, conn, u)
	return u
}

func TestIdP_IAPConsoleSession_StaffOpensWithAnyCase(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u := staffUser(t, conn, base)
	s := newIAPStack(t, base, nil, false)
	mixed := strings.ToUpper(u.email[:5]) + u.email[5:]
	if mixed == u.email {
		t.Fatalf("control: %q has no letters to change", u.email)
	}
	state := iapState()

	rec := s.mint(mixed, state)

	code := iapCode(t, rec)
	if strings.Contains(rec.Body.String(), "access_token") || strings.Contains(rec.Body.String(), "refresh_token") {
		t.Errorf("200 body %s holds a token", rec.Body.String())
	}
	tok := s.redeem(t, code, state)
	verifiedStaff(t, base, "exchanged IAP token", tok, true)
	requireStaff(t, "exchanged IAP token", tok)
}

func TestIdP_IAPConsoleSession_NonStaffGetsNoSessionLeftOpen(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u := signUp(t, conn, base) // confirmed, no house membership
	s := newIAPStack(t, base, nil, false)
	// signUp logs in once (immediate login after signup), which leaves a session; clear it so the count isolates the mint.
	exec(t, conn, `DELETE FROM auth.sessions WHERE user_id = $1`, u.id)
	if n := countAuthRows(t, conn, sessionsSQL, u.id); n != 0 {
		t.Fatalf("control: %d sessions before the call", n)
	}

	rec := s.mint(u.email, iapState())

	requireIAPNoStaff(t, "non-staff", rec, u.email)
	// The control: GoTrue did open a session, and the gateway ended it.
	if v, l := s.rt.calls("POST /verify? 200"), s.rt.calls("POST /logout?scope=local 2"); len(v) != 1 || len(l) != 1 {
		t.Errorf("GoTrue calls: %v then %v, want one opened /verify and one local /logout", v, l)
	}
	if n := countAuthRows(t, conn, sessionsSQL, u.id); n != 0 {
		t.Errorf("auth.sessions holds %d rows for the account after the call, want 0", n)
	}
}

func TestIdP_IAPConsoleSession_SuspendedStaffIsRefused(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u := staffUser(t, conn, base)
	s := newIAPStack(t, base, nil, false)
	// Control: before the suspension the same assertion opens.
	iapCode(t, s.mint(u.email, iapState()))

	runbook(t, conn, suspendHouseStaffSQL, u)
	rec := s.mint(u.email, iapState())

	requireIAPNoStaff(t, "suspended staff", rec, u.email)
}

func TestIdP_IAPConsoleSession_UnconfirmedAndBannedAreRefusedAndUnchanged(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	s := newIAPStack(t, base, nil, false)

	t.Run("unconfirmed", func(t *testing.T) {
		u := staffUser(t, conn, base)
		iapCode(t, s.mint(u.email, iapState())) // control: confirmed and staff opens
		exec(t, conn, `UPDATE auth.users SET email_confirmed_at = NULL WHERE id = $1`, u.id)

		rec := s.mint(u.email, iapState())

		requireIAPNoStaff(t, "unconfirmed", rec, u.email)
		if n := countAuthRows(t, conn, `SELECT count(*) FROM auth.users WHERE id = $1::uuid AND email_confirmed_at IS NULL`, u.id); n != 1 {
			t.Errorf("email_confirmed_at is set after the refused call (%d unconfirmed rows), want it left null", n)
		}
	})
	t.Run("banned", func(t *testing.T) {
		u := staffUser(t, conn, base)
		iapCode(t, s.mint(u.email, iapState())) // control
		exec(t, conn, `UPDATE auth.users SET banned_until = now() + interval '1 hour' WHERE id = $1`, u.id)

		rec := s.mint(u.email, iapState())

		requireIAPNoStaff(t, "banned", rec, u.email)
	})
}

func TestIdP_IAPConsoleSession_NearMissAddressIsNoAccount(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	tag := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	acct := signUpAs(t, conn, base, "a.b"+tag+"@example.test")
	grantHouseStaff(t, conn, acct)
	s := newIAPStack(t, base, nil, false)
	// Control: the exact address opens, so the refusals below are the near-miss, not the setup.
	iapCode(t, s.mint(acct.email, iapState()))
	before := countAuthRows(t, conn, sessionsSQL, acct.id)

	for _, miss := range []string{"ab" + tag + "@example.test", " " + acct.email} {
		rec := s.mint(miss, iapState())

		requireIAPNoStaff(t, fmt.Sprintf("%q", miss), rec, miss)
		if n := countAuthRows(t, conn, `SELECT count(*) FROM auth.users WHERE lower(email) = lower($1)`, miss); n != 0 {
			t.Errorf("%q: %d accounts exist for the near-miss address, want 0", miss, n)
		}
	}
	if n := countAuthRows(t, conn, sessionsSQL, acct.id); n != before {
		t.Errorf("sessions for the real account = %d after the near misses, want the %d from the control", n, before)
	}
}

// overlappingMints runs two mints for email at once, with different states, on an overlap stack.
func overlappingMints(s *iapStack, email string) (states [2]string, recs [2]*httptest.ResponseRecorder) {
	states = [2]string{iapState(), iapState()}
	var wg sync.WaitGroup
	for i := range states {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recs[i] = s.mint(email, states[i])
		}()
	}
	wg.Wait()
	return states, recs
}

func TestIdP_IAPConsoleSession_TwoOverlappingMintsBothOpen(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u := staffUser(t, conn, base)
	admin := adminClient(t, base)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// The premise: a second generated link spends the first.
	h1, err1 := admin.GenerateRecoveryLink(ctx, u.email)
	_, err2 := admin.GenerateRecoveryLink(ctx, u.email)
	if err1 != nil || err2 != nil {
		t.Fatalf("GenerateRecoveryLink: %v, %v", err1, err2)
	}
	status, body := postJSON(t, base+"/verify", map[string]string{"type": "recovery", "token_hash": h1.TokenHash})
	if status != http.StatusForbidden || body["error_code"] != "otp_expired" {
		t.Fatalf("/verify of the superseded hash = %d %v, want 403 otp_expired", status, body)
	}

	s := newIAPStack(t, base, nil, true)
	states, recs := overlappingMints(s, u.email)

	for i, rec := range recs {
		code := iapCode(t, rec)
		verifiedStaff(t, base, fmt.Sprintf("mint %d", i), s.redeem(t, code, states[i]), true)
	}
}

func TestIdP_IAPConsoleSession_WritesOneSignedInEvent(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	pool, err := pgxpool.New(context.Background(), requireEnv(t, "DATABASE_GATEWAY_URL"))
	if err != nil {
		t.Fatalf("open gateway pool: %v", err)
	}
	t.Cleanup(pool.Close)
	events := gateway.NewAccountEvents(pool, slog.New(slog.DiscardHandler))
	staff, plain := staffUser(t, conn, base), signUp(t, conn, base)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DELETE FROM account_event_handoffs WHERE user_id::text = ANY($1)`, []string{staff.id, plain.id})
	})
	rows := func(u idpUser) int {
		var n int
		if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM account_event_handoffs WHERE user_id::text = $1 AND event = 'account.signed_in'`, u.id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	start := rows(staff)

	iapCode(t, newIAPStack(t, base, events, false).mint(staff.email, iapState()))
	if n := rows(staff); n != start+1 {
		t.Fatalf("signed_in rows after a staff mint = %d, want %d", n, start+1)
	}

	_, recs := overlappingMints(newIAPStack(t, base, events, true), staff.email)
	for _, rec := range recs {
		iapCode(t, rec)
	}
	if n := rows(staff); n != start+3 {
		t.Errorf("signed_in rows after two overlapping mints = %d, want %d (one per code)", n, start+3)
	}

	requireIAPNoStaff(t, "non-staff", newIAPStack(t, base, events, false).mint(plain.email, iapState()), plain.email)
	if n := rows(plain); n != 0 {
		t.Errorf("signed_in rows for a non-staff account = %d, want 0", n)
	}
}
