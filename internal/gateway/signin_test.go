package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// GoTrue v2.197.0 /token?grant_type=password refusals (internal/api/token.go, story P15).
const (
	gtInvalidCredentials = `{"code":400,"error_code":"invalid_credentials","msg":"Invalid login credentials"}`
	gtUserBanned         = `{"code":400,"error_code":"user_banned","msg":"User is banned"}`
	gtEmailNotConfirmed  = `{"code":400,"error_code":"email_not_confirmed","msg":"Email not confirmed"}`
	gtNoAccessToken      = `{"token_type":"bearer","expires_in":3600,"refresh_token":"` + sessionRT + `","user":{"id":"7f3c2a1e-0b7d-4f51-9a0e-5d1c2b3a4e5f"}}`
	gtEmptyAccessToken   = `{"access_token":"","token_type":"bearer","expires_in":3600,"refresh_token":"` + sessionRT + `"}`
)

const (
	msgInvalidBody    = "invalid request body"
	msgFieldsRequired = "email and password are required"
	msgStateRequired  = "state is required"
	msgInvalidEmail   = "invalid email address"
	msgTooMany        = "too many requests"
	msgBadCredentials = "invalid email or password"
	msgNotVerified    = "email address not verified"
	msgUnavailable    = "sign-in is unavailable"
	msgBadCode        = "invalid or expired code"
)

type tokenCall struct {
	Method, Path, RawQuery string
	Body                   []byte
}

// tokenFake is a GoTrue whose answer can change mid-test; hits counts atomically.
type tokenFake struct {
	URL    *url.URL
	hits   atomic.Int64
	mu     sync.Mutex
	status int
	body   string
	delay  time.Duration
	calls  []tokenCall
}

func newTokenFake(t *testing.T, status int, body string) *tokenFake {
	t.Helper()
	f := &tokenFake{status: status, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, tokenCall{r.Method, r.URL.Path, r.URL.RawQuery, b})
		status, body, delay := f.status, f.body, f.delay
		f.mu.Unlock()
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse fake url: %v", err)
	}
	f.URL = u
	return f
}

func (f *tokenFake) set(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func (f *tokenFake) Hits() int { return int(f.hits.Load()) }

func (f *tokenFake) Calls() []tokenCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// signInRig wires both handlers to one store and one throttle on a fake clock.
type signInRig struct {
	store    *HandoffStore
	throttle *SignInThrottle
	clock    *testClock
	signIn   http.Handler
	exchange http.Handler
}

func newSignInRig(t *testing.T, authURL *url.URL, log *slog.Logger) *signInRig {
	t.Helper()
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	clk := newTestClock()
	store := NewHandoffStore(HandoffTTL, clk.Now)
	th := NewSignInThrottle(SignInMaxFailures, SignInMaxKeys, SignInWindow, clk.Now)
	return &signInRig{
		store:    store,
		throttle: th,
		clock:    clk,
		signIn:   SignInHandler(authURL, testClient(), store, th, log),
		exchange: ExchangeHandler(store),
	}
}

func serve(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}

func (r *signInRig) doSignIn(body string) *httptest.ResponseRecorder {
	return serve(r.signIn, http.MethodPost, "/auth/sign-in", body)
}

func (r *signInRig) doExchange(body string) *httptest.ResponseRecorder {
	return serve(r.exchange, http.MethodPost, "/auth/exchange", body)
}

func signInBody(email, password, state string) string {
	b, _ := json.Marshal(map[string]string{"email": email, "password": password, "state": state})
	return string(b)
}

func exchangeBody(code, state string) string {
	b, _ := json.Marshal(map[string]string{"code": code, "state": state})
	return string(b)
}

// requireRefusal asserts status and the exact flat error message.
func requireRefusal(t *testing.T, rec *httptest.ResponseRecorder, status int, msg string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	if got := errorBody(t, rec); got != msg {
		t.Fatalf("error = %q, want %q", got, msg)
	}
}

// requireOnlyKey asserts a 200 whose body is exactly {key: <string>} and returns the value.
func requireOnlyKey(t *testing.T, rec *httptest.ResponseRecorder, key string) string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if keys := slices.Sorted(maps.Keys(m)); !slices.Equal(keys, []string{key}) {
		t.Fatalf("body keys = %v, want exactly [%s]: %s", keys, key, rec.Body.String())
	}
	v, ok := m[key].(string)
	if !ok {
		t.Fatalf("%s is %T, want string", key, m[key])
	}
	return v
}

func requireCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	code := requireOnlyKey(t, rec, "code")
	if !codeShape.MatchString(code) {
		t.Fatalf("code %q is not 43 base64url characters", code)
	}
	return code
}

func TestSignIn_Success200CodeAndOnlyTokenCalled(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtSession)
	rig := newSignInRig(t, fake.URL, nil)
	s := randomState(t)

	code := requireCode(t, rig.doSignIn(signInBody(regEmail, regPassword, s)))

	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("GoTrue saw %d calls, want exactly 1: %+v", len(calls), calls)
	}
	if c := calls[0]; c.Method != http.MethodPost || c.Path != "/token" || c.RawQuery != "grant_type=password" {
		t.Errorf("GoTrue saw %s %s?%s, want POST /token?grant_type=password", c.Method, c.Path, c.RawQuery)
	}
	var sent map[string]any
	if err := json.Unmarshal(calls[0].Body, &sent); err != nil {
		t.Fatalf("token body %q is not JSON: %v", calls[0].Body, err)
	}
	if want := map[string]any{"email": regEmail, "password": regPassword}; !maps.Equal(sent, want) {
		t.Errorf("token body = %v, want exactly %v", sent, want)
	}
	if tok, ok := rig.store.Take(code, s); !ok || tok != sessionAT {
		t.Errorf("store.Take(code, state) = (%q, %v), want (%q, true)", tok, ok, sessionAT)
	}
}

func TestSignIn_ResponseCarriesNoToken(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtSession)
	rig := newSignInRig(t, fake.URL, nil)

	rec := rig.doSignIn(signInBody(regEmail, regPassword, randomState(t)))

	requireCode(t, rec)
	for _, secret := range []string{sessionAT, sessionRT} {
		if bytes.Contains(rec.Body.Bytes(), []byte(secret)) {
			t.Errorf("response body carries %q: %s", secret, rec.Body.String())
		}
		for k, vs := range rec.Header() {
			for _, v := range vs {
				if strings.Contains(v, secret) {
					t.Errorf("header %s carries %q", k, secret)
				}
			}
		}
	}
}

func TestSignIn_GoTrueErrorMapping(t *testing.T) {
	cases := []struct {
		name       string
		status     int // GoTrue's; 0 = unreachable
		body       string
		wantStatus int
		wantError  string
	}{
		{"invalid_credentials", http.StatusBadRequest, gtInvalidCredentials, http.StatusUnauthorized, msgBadCredentials},
		{"user_banned", http.StatusBadRequest, gtUserBanned, http.StatusUnauthorized, msgBadCredentials},
		{"email_not_confirmed", http.StatusBadRequest, gtEmailNotConfirmed, http.StatusForbidden, msgNotVerified},
		{"429", http.StatusTooManyRequests, gtOverRequestRateLimit, http.StatusTooManyRequests, msgTooMany},
		{"500", http.StatusInternalServerError, gtInternal, http.StatusBadGateway, msgUnavailable},
		{"200 without access_token", http.StatusOK, gtNoAccessToken, http.StatusBadGateway, msgUnavailable},
		{"200 with empty access_token", http.StatusOK, gtEmptyAccessToken, http.StatusBadGateway, msgUnavailable},
		{"400 other error_code", http.StatusBadRequest, gtValidationFailed, http.StatusBadGateway, msgUnavailable},
		{"unreachable", 0, "", http.StatusBadGateway, msgUnavailable},
	}
	bodies := map[string][]byte{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			authURL := closedURL(t)
			var fake *tokenFake
			if c.status != 0 {
				fake = newTokenFake(t, c.status, c.body)
				authURL = fake.URL
			}
			rig := newSignInRig(t, authURL, nil)

			rec := rig.doSignIn(signInBody(regEmail, regPassword, randomState(t)))

			if fake != nil && fake.Hits() != 1 {
				t.Fatalf("GoTrue saw %d calls, want 1", fake.Hits())
			}
			requireRefusal(t, rec, c.wantStatus, c.wantError)
			if n := storeMapEntries(rig.store); n != 0 {
				t.Errorf("store holds %d entries after a refusal, want 0", n)
			}
			bodies[c.name] = rec.Body.Bytes()
		})
	}
	// A banned address must not be told apart from a wrong password.
	if len(bodies["user_banned"]) == 0 || len(bodies["invalid_credentials"]) == 0 {
		t.Fatal("a credentials row recorded no body")
	}
	if !bytes.Equal(bodies["user_banned"], bodies["invalid_credentials"]) {
		t.Errorf("user_banned body %q != invalid_credentials body %q", bodies["user_banned"], bodies["invalid_credentials"])
	}
}

func TestSignIn_BadBody400NoUpstreamCall(t *testing.T) {
	s := randomState(t)
	for _, c := range []struct{ name, body, want string }{
		{"empty object", `{}`, msgFieldsRequired},
		{"email only", `{"email":"a@b.c"}`, msgFieldsRequired},
		{"empty email", signInBody("", regPassword, s), msgFieldsRequired},
		{"empty password", signInBody(regEmail, "", s), msgFieldsRequired},
		{"not json", `not json`, msgInvalidBody},
		{"over 4 KiB", signInBody(regEmail, strings.Repeat("p", 5000), s), msgInvalidBody},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := newTokenFake(t, http.StatusOK, gtSession)
			rig := newSignInRig(t, fake.URL, nil)

			requireRefusal(t, rig.doSignIn(c.body), http.StatusBadRequest, c.want)
			if n := fake.Hits(); n != 0 {
				t.Errorf("GoTrue saw %d calls, want 0", n)
			}
		})
	}
	t.Run("positive control: a valid body reaches GoTrue", func(t *testing.T) {
		fake := newTokenFake(t, http.StatusOK, gtSession)
		rig := newSignInRig(t, fake.URL, nil)

		requireCode(t, rig.doSignIn(signInBody(regEmail, regPassword, s)))
		if n := fake.Hits(); n != 1 {
			t.Errorf("GoTrue saw %d calls, want 1", n)
		}
	})
}

func TestSignIn_ThrottleBlocksBeforeGoTrue(t *testing.T) {
	fake := newTokenFake(t, http.StatusBadRequest, gtInvalidCredentials)
	rig := newSignInRig(t, fake.URL, nil)
	s := randomState(t)

	for i := 1; i <= SignInMaxFailures; i++ {
		rec := rig.doSignIn(signInBody(regEmail, regPassword, s))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401: %s", i, rec.Code, rec.Body.String())
		}
	}
	requireRefusal(t, rig.doSignIn(signInBody(regEmail, regPassword, s)), http.StatusTooManyRequests, msgTooMany)
	if n := fake.Hits(); n != SignInMaxFailures {
		t.Errorf("GoTrue saw %d calls, want %d", n, SignInMaxFailures)
	}
}

func TestSignIn_SuccessResetsThrottle(t *testing.T) {
	fake := newTokenFake(t, http.StatusBadRequest, gtInvalidCredentials)
	rig := newSignInRig(t, fake.URL, nil)
	s := randomState(t)

	for i := 1; i <= SignInMaxFailures-1; i++ {
		requireRefusal(t, rig.doSignIn(signInBody(regEmail, regPassword, s)), http.StatusUnauthorized, msgBadCredentials)
	}
	fake.set(http.StatusOK, gtSession)
	requireCode(t, rig.doSignIn(signInBody(regEmail, regPassword, s)))

	fake.set(http.StatusBadRequest, gtInvalidCredentials)
	for i := 1; i <= SignInMaxFailures; i++ {
		rec := rig.doSignIn(signInBody(regEmail, regPassword, s))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("post-success attempt %d: status = %d, want 401: %s", i, rec.Code, rec.Body.String())
		}
	}
	// The throttle still bites after the reset.
	requireRefusal(t, rig.doSignIn(signInBody(regEmail, regPassword, s)), http.StatusTooManyRequests, msgTooMany)
	if want := 2 * SignInMaxFailures; fake.Hits() != want {
		t.Errorf("GoTrue saw %d calls, want %d", fake.Hits(), want)
	}
}

// requireNothingCounted proves email's throttle count is zero: a full window of reservations passes.
func requireNothingCounted(t *testing.T, th *SignInThrottle, email string) {
	t.Helper()
	reserveN(t, th, email, SignInMaxFailures)
	if th.Reserve(email) {
		t.Fatalf("Reserve #%d = true, want false (throttle not counting)", SignInMaxFailures+1)
	}
}

func TestSignIn_EmailNotConfirmedDoesNotCount(t *testing.T) {
	fake := newTokenFake(t, http.StatusBadRequest, gtEmailNotConfirmed)
	rig := newSignInRig(t, fake.URL, nil)
	s := randomState(t)

	for i := 1; i <= SignInMaxFailures+2; i++ {
		rec := rig.doSignIn(signInBody(regEmail, regPassword, s))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: status = %d, want 403: %s", i, rec.Code, rec.Body.String())
		}
	}
	if want := SignInMaxFailures + 2; fake.Hits() != want {
		t.Errorf("GoTrue saw %d calls, want %d", fake.Hits(), want)
	}
	requireNothingCounted(t, rig.throttle, regEmail)
}

func TestSignIn_UpstreamErrorRefunds(t *testing.T) {
	t.Run("500", func(t *testing.T) {
		fake := newTokenFake(t, http.StatusInternalServerError, gtInternal)
		rig := newSignInRig(t, fake.URL, nil)
		s := randomState(t)

		for i := 1; i <= SignInMaxFailures+2; i++ {
			rec := rig.doSignIn(signInBody(regEmail, regPassword, s))
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("attempt %d: status = %d, want 502: %s", i, rec.Code, rec.Body.String())
			}
		}
		if want := SignInMaxFailures + 2; fake.Hits() != want {
			t.Errorf("GoTrue saw %d calls, want %d", fake.Hits(), want)
		}
		requireNothingCounted(t, rig.throttle, regEmail)
	})
	t.Run("unreachable", func(t *testing.T) {
		rig := newSignInRig(t, closedURL(t), nil)
		s := randomState(t)

		for i := 1; i <= SignInMaxFailures+2; i++ {
			rec := rig.doSignIn(signInBody(regEmail, regPassword, s))
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("attempt %d: status = %d, want 502: %s", i, rec.Code, rec.Body.String())
			}
		}
		requireNothingCounted(t, rig.throttle, regEmail)
	})
}

func TestSignIn_ConcurrentBurstBounded(t *testing.T) {
	const burst = 50
	fake := newTokenFake(t, http.StatusBadRequest, gtInvalidCredentials)
	fake.mu.Lock()
	fake.delay = 100 * time.Millisecond
	fake.mu.Unlock()
	rig := newSignInRig(t, fake.URL, nil)
	s := randomState(t)

	codes := make([]int, burst)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range burst {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes[i] = rig.doSignIn(signInBody(regEmail, regPassword, s)).Code
		}()
	}
	close(start)
	wg.Wait()

	counts := map[int]int{}
	for _, c := range codes {
		counts[c]++
	}
	hits := fake.Hits()
	// Reserve-before-call admits exactly the window's budget.
	if hits != SignInMaxFailures {
		t.Errorf("GoTrue saw %d calls, want %d (statuses %v)", hits, SignInMaxFailures, counts)
	}
	if counts[http.StatusUnauthorized] != hits || counts[http.StatusTooManyRequests] != burst-hits {
		t.Errorf("statuses = %v, want %d×401 and %d×429", counts, hits, burst-hits)
	}
}

func TestSignIn_OverlongEmail400BeforeThrottle(t *testing.T) {
	const domain = "@corp.example"
	overlong := strings.Repeat("a", 255-len(domain)) + domain
	atLimit := strings.Repeat("b", 254-len(domain)) + domain
	if len(overlong) != 255 || len(atLimit) != 254 {
		t.Fatalf("fixture lengths %d/%d, want 255/254", len(overlong), len(atLimit))
	}
	fake := newTokenFake(t, http.StatusBadRequest, gtInvalidCredentials)
	rig := newSignInRig(t, fake.URL, nil)
	s := randomState(t)

	requireRefusal(t, rig.doSignIn(signInBody(overlong, regPassword, s)), http.StatusBadRequest, msgInvalidEmail)
	if n := fake.Hits(); n != 0 {
		t.Fatalf("GoTrue saw %d calls for a 255-character email, want 0", n)
	}

	for i := 1; i <= SignInMaxFailures; i++ {
		requireRefusal(t, rig.doSignIn(signInBody(regEmail, regPassword, s)), http.StatusUnauthorized, msgBadCredentials)
	}
	requireRefusal(t, rig.doSignIn(signInBody(atLimit, regPassword, s)), http.StatusUnauthorized, msgBadCredentials)
	if want := SignInMaxFailures + 1; fake.Hits() != want {
		t.Errorf("GoTrue saw %d calls, want %d", fake.Hits(), want)
	}

	// Nothing was reserved for the overlong address, and the length check precedes the throttle.
	requireNothingCounted(t, rig.throttle, overlong)
	requireRefusal(t, rig.doSignIn(signInBody(overlong, regPassword, s)), http.StatusBadRequest, msgInvalidEmail)
}

func TestSignIn_StateRequired(t *testing.T) {
	fake := newTokenFake(t, http.StatusBadRequest, gtInvalidCredentials)
	rig := newSignInRig(t, fake.URL, nil)
	s := randomState(t)

	absent, _ := json.Marshal(map[string]string{"email": regEmail, "password": regPassword})
	for _, c := range []struct{ name, body string }{
		{"absent", string(absent)},
		{"empty", signInBody(regEmail, regPassword, "")},
		{"short", signInBody(regEmail, regPassword, "short")},
		{"43 with +", signInBody(regEmail, regPassword, s[:42]+"+")},
		{"43 with /", signInBody(regEmail, regPassword, s[:42]+"/")},
		{"42 chars", signInBody(regEmail, regPassword, s[:42])},
		{"44 chars", signInBody(regEmail, regPassword, s+"A")},
		{"padded", signInBody(regEmail, regPassword, s+"=")},
	} {
		t.Run(c.name, func(t *testing.T) {
			requireRefusal(t, rig.doSignIn(c.body), http.StatusBadRequest, msgStateRequired)
		})
	}
	if n := fake.Hits(); n != 0 {
		t.Fatalf("GoTrue saw %d calls for bad states, want 0", n)
	}

	// Positive control: a valid state reaches GoTrue.
	requireRefusal(t, rig.doSignIn(signInBody("other@corp.example", regPassword, s)), http.StatusUnauthorized, msgBadCredentials)
	if n := fake.Hits(); n != 1 {
		t.Fatalf("GoTrue saw %d calls, want 1 after one valid-state attempt", n)
	}

	requireNothingCounted(t, rig.throttle, regEmail)

	// The state check precedes the throttle.
	const blocked = "blocked@corp.example"
	reserveN(t, rig.throttle, blocked, SignInMaxFailures)
	requireRefusal(t, rig.doSignIn(signInBody(blocked, regPassword, "short")), http.StatusBadRequest, msgStateRequired)
}

func TestExchange_RedeemsOnce(t *testing.T) {
	rig := newSignInRig(t, closedURL(t), nil)
	s := randomState(t)
	code, _ := rig.store.Put(sessionAT, stateHash(s))

	tok := requireOnlyKey(t, rig.doExchange(exchangeBody(code, s)), "access_token")
	if tok != sessionAT {
		t.Errorf("access_token = %q, want %q", tok, sessionAT)
	}
	requireRefusal(t, rig.doExchange(exchangeBody(code, s)), http.StatusBadRequest, msgBadCode)
}

func TestExchange_WrongStateRefusedIdentically(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtSession)
	rig := newSignInRig(t, fake.URL, nil)
	s1, s2 := randomState(t), randomState(t)

	unknown := rig.doExchange(exchangeBody(randomState(t), s1))
	requireRefusal(t, unknown, http.StatusBadRequest, msgBadCode)

	code := requireCode(t, rig.doSignIn(signInBody(regEmail, regPassword, s1)))

	wrong := rig.doExchange(exchangeBody(code, s2))
	if wrong.Code != unknown.Code || !bytes.Equal(wrong.Body.Bytes(), unknown.Body.Bytes()) {
		t.Errorf("wrong state answered %d %q, want the unknown-code %d %q", wrong.Code, wrong.Body.String(), unknown.Code, unknown.Body.String())
	}
	if got, want := wrong.Header().Get("Content-Type"), unknown.Header().Get("Content-Type"); got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	// The wrong state spent the code.
	spent := rig.doExchange(exchangeBody(code, s1))
	if spent.Code != unknown.Code || !bytes.Equal(spent.Body.Bytes(), unknown.Body.Bytes()) {
		t.Errorf("right state after a wrong one answered %d %q, want %d %q", spent.Code, spent.Body.String(), unknown.Code, unknown.Body.String())
	}
}

func TestExchange_MissingStateRefused(t *testing.T) {
	rig := newSignInRig(t, closedURL(t), nil)
	s := randomState(t)
	for _, c := range []struct {
		name string
		body func(code string) string
	}{
		{"absent", func(code string) string { return `{"code":"` + code + `"}` }},
		{"empty", func(code string) string { return exchangeBody(code, "") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, _ := rig.store.Put(sessionAT, stateHash(s))

			requireRefusal(t, rig.doExchange(c.body(code)), http.StatusBadRequest, msgBadCode)
			if tok, ok := rig.store.Take(code, s); ok {
				t.Errorf("code still redeemable after a stateless exchange (got %q)", tok)
			}
		})
	}
}

func TestSignInThenExchange_RoundTrip(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtSession)
	rig := newSignInRig(t, fake.URL, nil)
	s := randomState(t)

	code := requireCode(t, rig.doSignIn(signInBody(regEmail, regPassword, s)))

	tok := requireOnlyKey(t, rig.doExchange(exchangeBody(code, s)), "access_token")
	if tok != sessionAT {
		t.Errorf("access_token = %q, want %q", tok, sessionAT)
	}
	requireRefusal(t, rig.doExchange(exchangeBody(code, s)), http.StatusBadRequest, msgBadCode)
}

func TestExchange_RefusalsAreIdentical(t *testing.T) {
	rig := newSignInRig(t, closedURL(t), nil)
	s := randomState(t)
	live, _ := rig.store.Put(sessionAT, stateHash(s))
	expiring, _ := rig.store.Put(sessionAT, stateHash(s))
	oversized, _ := rig.store.Put(sessionAT, stateHash(s))

	ref := rig.doExchange(exchangeBody(randomState(t), s))
	requireRefusal(t, ref, http.StatusBadRequest, msgBadCode)

	answers := map[string]*httptest.ResponseRecorder{
		"empty code": rig.doExchange(exchangeBody("", s)),
		"malformed":  rig.doExchange(`{`),
		"empty body": rig.doExchange(``),
		// A live code in a body over 1 KiB must still be refused.
		"over 1 KiB": rig.doExchange(`{"code":"` + oversized + `","state":"` + s + `","pad":"` + strings.Repeat("x", 1100) + `"}`),
	}

	// Positive control: a live code redeems, so the refusals are not a dead handler.
	if tok := requireOnlyKey(t, rig.doExchange(exchangeBody(live, s)), "access_token"); tok != sessionAT {
		t.Fatalf("access_token = %q, want %q", tok, sessionAT)
	}
	rig.clock.Advance(HandoffTTL)
	answers["expired"] = rig.doExchange(exchangeBody(expiring, s))

	if len(answers) == 0 {
		t.Fatal("no refusals collected")
	}
	for name, rec := range answers {
		if rec.Code != ref.Code || !bytes.Equal(rec.Body.Bytes(), ref.Body.Bytes()) {
			t.Errorf("%s: answered %d %q, want %d %q", name, rec.Code, rec.Body.String(), ref.Code, ref.Body.String())
		}
	}
}

func TestSignInAndExchange_NoStoreAnd405(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtSession)
	rig := newSignInRig(t, fake.URL, nil)
	s := randomState(t)
	answers := map[string]*httptest.ResponseRecorder{}

	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for route, h := range map[string]http.Handler{"/auth/sign-in": rig.signIn, "/auth/exchange": rig.exchange} {
			rec := serve(h, m, route, signInBody(regEmail, regPassword, s))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: status = %d, want 405", m, route, rec.Code)
			}
			answers[m+" "+route] = rec
		}
	}
	if n := fake.Hits(); n != 0 {
		t.Errorf("GoTrue saw %d calls from non-POST methods, want 0", n)
	}

	signIn200 := rig.doSignIn(signInBody(regEmail, regPassword, s))
	code := requireCode(t, signIn200)
	answers["sign-in 200"] = signIn200
	answers["sign-in 400 body"] = rig.doSignIn(`not json`)
	answers["sign-in 400 state"] = rig.doSignIn(signInBody(regEmail, regPassword, "short"))
	answers["exchange 200"] = rig.doExchange(exchangeBody(code, s))
	answers["exchange 400"] = rig.doExchange(exchangeBody(code, s))

	refusing := newSignInRig(t, newTokenFake(t, http.StatusBadRequest, gtInvalidCredentials).URL, nil)
	for i := 1; i <= SignInMaxFailures; i++ {
		answers["sign-in 401"] = refusing.doSignIn(signInBody(regEmail, regPassword, s))
	}
	answers["sign-in 429"] = refusing.doSignIn(signInBody(regEmail, regPassword, s))
	answers["sign-in 502"] = newSignInRig(t, closedURL(t), nil).doSignIn(signInBody(regEmail, regPassword, s))

	if len(answers) == 0 {
		t.Fatal("no answers collected")
	}
	for name, rec := range answers {
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s (%d): Cache-Control = %q, want no-store", name, rec.Code, got)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Errorf("%s (%d): Content-Type = %q, want application/json", name, rec.Code, got)
		}
	}
}

func TestSignIn_JoinsUnderAnAuthURLPrefix(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtSession)
	rig := newSignInRig(t, fake.URL.JoinPath("prefix"), nil)

	requireCode(t, rig.doSignIn(signInBody(regEmail, regPassword, randomState(t))))

	var got []string
	for _, c := range fake.Calls() {
		got = append(got, c.Method+" "+c.Path+"?"+c.RawQuery)
	}
	if want := []string{"POST /prefix/token?grant_type=password"}; !slices.Equal(got, want) {
		t.Errorf("GoTrue saw %v, want %v", got, want)
	}
}

func TestSignIn_NeverLogsSecrets(t *testing.T) {
	log, buf := captureLog()
	s := randomState(t)
	body := signInBody(regEmail, regPassword, s)

	refusing := newSignInRig(t, newTokenFake(t, http.StatusBadRequest, gtInvalidCredentials).URL, log)
	for i := 0; i <= SignInMaxFailures; i++ { // 10 refusals, then a throttled 429
		refusing.doSignIn(body)
	}
	newSignInRig(t, newTokenFake(t, http.StatusBadRequest, gtEmailNotConfirmed).URL, log).doSignIn(body)
	newSignInRig(t, newTokenFake(t, http.StatusInternalServerError, gtInternal).URL, log).doSignIn(body)
	newSignInRig(t, closedURL(t), log).doSignIn(body)

	// Positive control: failures are logged, with the upstream status.
	if buf.Len() == 0 {
		t.Fatal("nothing was logged; a 502 must log its upstream status")
	}
	if !logHasValue(buf, http.StatusInternalServerError) {
		t.Errorf("no log attribute carries the upstream status 500: %s", buf.String())
	}

	ok := newSignInRig(t, newTokenFake(t, http.StatusOK, gtSession).URL, log)
	code := requireCode(t, ok.doSignIn(body))
	ok.doExchange(exchangeBody(code, s))
	for _, secret := range []string{regEmail, regPassword, code, sessionAT, sessionRT, "Invalid login credentials"} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("log carries %q: %s", secret, buf.String())
		}
	}
}
