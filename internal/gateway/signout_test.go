package gateway

import (
	"bufio"
	"context"
	"encoding/base64"
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
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

const (
	signOutR0 = "refresh-signout-r0-4q7z"
	signOutR1 = "refresh-signout-r1-8c2v"

	msgSignOutUnavailable = "sign-out is unavailable"
)

// signOutAccess is a GoTrue-shaped access token for sub; the handler reads sub without verifying it.
func signOutAccess(t *testing.T, sub string) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": sub, "session_id": "9c8b7a6d-5e4f-4321-8a9b-0c1d2e3f4a5b", "aud": "authenticated",
	}).SignedString([]byte("gotrue-private"))
	if err != nil {
		t.Fatalf("sign access token: %v", err)
	}
	return tok
}

func signOutGranted(access string) string {
	b, _ := json.Marshal(map[string]any{
		"access_token": access, "token_type": "bearer", "expires_in": 3600, "refresh_token": signOutR1,
		"user": map[string]string{"id": subjectS1, "email": "ada@corp.example"},
	})
	return string(b)
}

type signOutCall struct {
	Method, Path, RawQuery, Auth string
	Body                         []byte
}

// signOutFake is GoTrue's /token and /logout, matched by path suffix so a prefixed AUTH_URL works.
type signOutFake struct {
	URL    *url.URL
	mu     sync.Mutex
	calls  []signOutCall
	token  http.HandlerFunc
	logout http.HandlerFunc
}

func answer(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if body != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// dropped closes the connection with no answer: GoTrue unreachable mid-flow.
func dropped(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }

func newSignOutFake(t *testing.T, token, logout http.HandlerFunc) *signOutFake {
	t.Helper()
	f := &signOutFake{token: token, logout: logout}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, signOutCall{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), b})
		f.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			f.token(w, r)
		case strings.HasSuffix(r.URL.Path, "/logout"):
			f.logout(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse fake url: %v", err)
	}
	f.URL = u
	return f
}

func (f *signOutFake) Calls() []signOutCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *signOutFake) count(suffix string) int {
	n := 0
	for _, c := range f.Calls() {
		if strings.HasSuffix(c.Path, suffix) {
			n++
		}
	}
	return n
}

// signOutRig is a sign-out handler sharing one checker with the /api/ handler, S1's session already cached live.
type signOutRig struct {
	*sessionRig
	user    *userFake
	gotrue  *signOutFake
	handler http.Handler
	tokS1   string
}

func newSignOutRig(t *testing.T, gotrue *signOutFake, authURL *url.URL, log *slog.Logger) *signOutRig {
	t.Helper()
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	user := newUserFake(t, http.StatusOK, gtUser)
	rg := newSessionRig(t, user.URL, nil, nil)
	if authURL == nil {
		authURL = gotrue.URL
	}
	r := &signOutRig{
		sessionRig: rg,
		user:       user,
		gotrue:     gotrue,
		handler:    SignOutHandler(authURL, testClient(), rg.sessions, log),
		tokS1:      rg.signer.token(t, subjectS1, sid1),
	}
	if rec := rg.get(r.tokS1); rec.Code != http.StatusOK || user.Hits() != 1 {
		t.Fatalf("priming S1: status = %d, /user calls = %d, want 200 and 1", rec.Code, user.Hits())
	}
	return r
}

// evicted reports whether S1's next request had to ask GoTrue again.
func (r *signOutRig) evicted() bool {
	before := r.user.Hits()
	r.get(r.tokS1)
	return r.user.Hits() > before
}

func doSignOut(h http.Handler, body string) *httptest.ResponseRecorder {
	return serve(h, http.MethodPost, "/auth/sign-out", body)
}

func TestSignOut_RefreshThenGlobalLogout(t *testing.T) {
	access := signOutAccess(t, subjectS1)
	fake := newSignOutFake(t, answer(http.StatusOK, signOutGranted(access)), answer(http.StatusNoContent, ""))
	rg := newSignOutRig(t, fake, nil, nil)

	rec := doSignOut(rg.handler, refreshBody(signOutR0))

	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("answer = %d %q, want 204 with no body", rec.Code, rec.Body.String())
	}
	calls := fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("GoTrue saw %d calls, want exactly [refresh grant, logout]: %+v", len(calls), calls)
	}
	if c := calls[0]; c.Method != http.MethodPost || c.Path != "/token" || c.RawQuery != "grant_type=refresh_token" {
		t.Errorf("first call = %s %s?%s, want POST /token?grant_type=refresh_token", c.Method, c.Path, c.RawQuery)
	}
	var sent map[string]any
	if err := json.Unmarshal(calls[0].Body, &sent); err != nil {
		t.Fatalf("token body %q is not JSON: %v", calls[0].Body, err)
	}
	if want := map[string]any{"refresh_token": signOutR0}; !maps.Equal(sent, want) {
		t.Errorf("token body = %v, want exactly %v", sent, want)
	}
	if c := calls[1]; c.Method != http.MethodPost || c.Path != "/logout" || c.RawQuery != "scope=global" {
		t.Errorf("second call = %s %s?%s, want POST /logout?scope=global", c.Method, c.Path, c.RawQuery)
	}
	if got, want := calls[1].Auth, "Bearer "+access; got != want {
		t.Errorf("logout Authorization = %q, want the grant's access token %q", got, want)
	}
}

func TestSignOut_EvictsTheSubject(t *testing.T) {
	fake := newSignOutFake(t, answer(http.StatusOK, signOutGranted(signOutAccess(t, subjectS1))), answer(http.StatusNoContent, ""))
	rg := newSignOutRig(t, fake, nil, nil)
	s1b := rg.signer.token(t, subjectS1, sid2)
	s2 := rg.signer.token(t, subjectS2, "2a3b4c5d-6e7f-4809-9a1b-2c3d4e5f6a7b")
	for _, tok := range []string{s1b, s2, rg.tokS1, s1b, s2} {
		rg.get(tok)
	}
	if n := rg.user.Hits(); n != 3 {
		t.Fatalf("/user calls = %d before sign-out, want 3 (three sessions, each cached)", n)
	}

	if rec := doSignOut(rg.handler, refreshBody(signOutR0)); rec.Code != http.StatusNoContent {
		t.Errorf("sign-out = %d %s, want 204", rec.Code, rec.Body.String())
	}

	for _, s := range []struct {
		name string
		tok  string
		want int
	}{
		{"S1 sid1 asks again", rg.tokS1, 4},
		{"S1 sid2 asks again", s1b, 5},
		{"S2 stays cached", s2, 5},
	} {
		if rec := rg.get(s.tok); rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", s.name, rec.Code)
		}
		if n := rg.user.Hits(); n != s.want {
			t.Errorf("%s: /user calls = %d, want %d", s.name, n, s.want)
		}
	}
}

func TestSignOut_GoTrueMapping(t *testing.T) {
	granted := signOutGranted(signOutAccess(t, subjectS1))
	ok := answer(http.StatusNoContent, "")
	cases := []struct {
		name          string
		token, logout http.HandlerFunc
		closed        bool
		wantStatus    int
		wantMsg       string // empty: 204 with no body
		wantLogouts   int
		wantEvicted   *bool // nil: not specified by the contract
	}{
		{"refresh 400", answer(400, gtRefreshNotFound), ok, false, 401, msgRefreshRefused, 0, ptr(false)},
		{"refresh 401", answer(401, `{"code":401,"msg":"unauthorized"}`), ok, false, 401, msgRefreshRefused, 0, ptr(false)},
		{"refresh 403", answer(403, `{"code":403,"msg":"forbidden"}`), ok, false, 401, msgRefreshRefused, 0, ptr(false)},
		{"refresh 404", answer(404, `{"code":404,"msg":"not found"}`), ok, false, 401, msgRefreshRefused, 0, ptr(false)},
		{"refresh 429", answer(429, gtOverRequestRateLimit), ok, false, 429, msgTooMany, 0, ptr(false)},
		{"logout 429", answer(200, granted), answer(429, gtOverRequestRateLimit), false, 429, msgTooMany, 1, ptr(false)},
		{"logout 401", answer(200, granted), answer(401, gtError(401, "session_not_found")), false, 204, "", 1, ptr(true)},
		{"logout 403", answer(200, granted), answer(403, gtError(403, "session_not_found")), false, 204, "", 1, ptr(true)},
		{"logout 403 bad_jwt", answer(200, granted), answer(403, gtError(403, "bad_jwt")), false, 502, msgSignOutUnavailable, 1, ptr(false)},
		{"logout 401 without body", answer(200, granted), answer(401, ""), false, 502, msgSignOutUnavailable, 1, ptr(false)},
		{"logout 200", answer(200, granted), answer(200, `{}`), false, 204, "", 1, ptr(true)},
		{"logout 202", answer(200, granted), answer(202, ""), false, 204, "", 1, ptr(true)},
		{"logout 404", answer(200, granted), answer(404, `{"code":404,"msg":"not found"}`), false, 502, msgSignOutUnavailable, 1, ptr(false)},
		{"logout 302", answer(200, granted), answer(302, ""), false, 502, msgSignOutUnavailable, 1, ptr(false)},
		{"refresh 201", answer(201, granted), ok, false, 502, msgSignOutUnavailable, 0, ptr(false)},
		{"refresh 302", answer(302, ""), ok, false, 502, msgSignOutUnavailable, 0, ptr(false)},
		{"refresh 500", answer(500, gtInternal), ok, false, 502, msgSignOutUnavailable, 0, ptr(false)},
		{"refresh 200 without access_token", answer(200, `{"refresh_token":"`+signOutR1+`"}`), ok, false, 502, msgSignOutUnavailable, 0, ptr(false)},
		{"refresh 200 empty access_token", answer(200, `{"access_token":"","refresh_token":"`+signOutR1+`"}`), ok, false, 502, msgSignOutUnavailable, 0, ptr(false)},
		{"refresh unreachable", nil, nil, true, 502, msgSignOutUnavailable, 0, ptr(false)},
		{"logout 500", answer(200, granted), answer(500, gtInternal), false, 502, msgSignOutUnavailable, 1, ptr(false)},
		{"logout unreachable", answer(200, granted), dropped, false, 502, msgSignOutUnavailable, 1, ptr(false)},
	}
	var refusal string
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := newSignOutFake(t, c.token, c.logout)
			var authURL *url.URL
			if c.closed {
				authURL = closedURL(t)
			}
			rg := newSignOutRig(t, fake, authURL, nil)

			rec := doSignOut(rg.handler, refreshBody(signOutR0))

			if c.wantMsg == "" {
				if rec.Code != c.wantStatus || rec.Body.Len() != 0 {
					t.Errorf("answer = %d %q, want %d with no body", rec.Code, rec.Body.String(), c.wantStatus)
				}
			} else {
				requireRefusal(t, rec, c.wantStatus, c.wantMsg)
			}
			if n := fake.count("/logout"); n != c.wantLogouts {
				t.Errorf("/logout calls = %d, want %d", n, c.wantLogouts)
			}
			if c.wantEvicted != nil {
				if got := rg.evicted(); got != *c.wantEvicted {
					t.Errorf("S1 evicted = %v, want %v", got, *c.wantEvicted)
				}
			}
			// Every refused grant answers the same bytes: the client cannot tell the reasons apart.
			if c.wantStatus == http.StatusUnauthorized {
				if refusal == "" {
					refusal = rec.Body.String()
				} else if rec.Body.String() != refusal {
					t.Errorf("body = %q, want the same bytes as every other refusal %q", rec.Body.String(), refusal)
				}
			}
		})
	}
}

func ptr(b bool) *bool { return &b }

func TestSignOut_BadBody400NoUpstreamCall(t *testing.T) {
	fake := newSignOutFake(t, answer(http.StatusOK, signOutGranted(signOutAccess(t, subjectS1))), answer(http.StatusNoContent, ""))
	h := SignOutHandler(fake.URL, testClient(), liveSessions(t), slog.New(slog.DiscardHandler))

	// The contract's limit, not the constant: refreshBody adds 20 bytes around the token.
	const oneKiB = 1024
	oneOver := refreshBody(strings.Repeat("r", oneKiB-19))
	if len(oneOver) != oneKiB+1 {
		t.Fatalf("one-over body is %d bytes, want %d", len(oneOver), oneKiB+1)
	}
	for _, c := range []struct{ name, body, msg string }{
		{"not json", `not json`, msgInvalidBody},
		{"no body", ``, msgInvalidBody},
		{"array", `[]`, msgInvalidBody},
		{"one byte over 1 KiB", oneOver, msgInvalidBody},
		{"number token", `{"refresh_token":123}`, msgInvalidBody},
		{"object token", `{"refresh_token":{"value":"` + signOutR0 + `"}}`, msgInvalidBody},
		{"empty object", `{}`, msgRefreshRequired},
		{"empty token", `{"refresh_token":""}`, msgRefreshRequired},
		{"null token", `{"refresh_token":null}`, msgRefreshRequired},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := len(fake.Calls())
			requireRefusal(t, doSignOut(h, c.body), http.StatusBadRequest, c.msg)
			if n := len(fake.Calls()) - before; n != 0 {
				t.Errorf("GoTrue saw %d calls from a refused body, want 0", n)
			}
		})
	}

	// Positive pair: a well-formed body, and one of exactly 1 KiB, on the same handler do reach GoTrue.
	atLimit := refreshBody(strings.Repeat("r", oneKiB-20))
	if len(atLimit) != oneKiB {
		t.Fatalf("at-limit body is %d bytes, want %d", len(atLimit), oneKiB)
	}
	for i, body := range []string{refreshBody(signOutR0), atLimit} {
		before := len(fake.Calls())
		if rec := doSignOut(h, body); rec.Code != http.StatusNoContent {
			t.Errorf("body %d (%d bytes): status = %d, want 204: %s", i, len(body), rec.Code, rec.Body.String())
		}
		if n := len(fake.Calls()) - before; n != 2 {
			t.Errorf("body %d (%d bytes): GoTrue saw %d calls, want 2", i, len(body), n)
		}
	}
}

func TestSignOut_NoStoreAndPostOnly(t *testing.T) {
	granted := answer(http.StatusOK, signOutGranted(signOutAccess(t, subjectS1)))
	ok := answer(http.StatusNoContent, "")
	handler := func(token, logout http.HandlerFunc) (http.Handler, *signOutFake) {
		f := newSignOutFake(t, token, logout)
		return SignOutHandler(f.URL, testClient(), liveSessions(t), slog.New(slog.DiscardHandler)), f
	}
	h, fake := handler(granted, ok)
	answers := map[string]*httptest.ResponseRecorder{}

	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		rec := serve(h, m, "/auth/sign-out", refreshBody(signOutR0))
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
			t.Errorf("%s: %d Allow=%q, want 405 Allow POST", m, rec.Code, rec.Header().Get("Allow"))
		}
		answers[m] = rec
	}
	if n := len(fake.Calls()); n != 0 {
		t.Errorf("GoTrue saw %d calls from non-POST methods, want 0", n)
	}

	answers["204"] = doSignOut(h, refreshBody(signOutR0))
	answers["400 body"] = doSignOut(h, `not json`)
	answers["400 token"] = doSignOut(h, `{}`)
	h401, _ := handler(answer(400, gtRefreshNotFound), ok)
	answers["401"] = doSignOut(h401, refreshBody(signOutR0))
	h429, _ := handler(answer(429, gtOverRequestRateLimit), ok)
	answers["429"] = doSignOut(h429, refreshBody(signOutR0))
	h502, _ := handler(granted, answer(500, gtInternal))
	answers["502"] = doSignOut(h502, refreshBody(signOutR0))

	// Positive pair: the answers above are the statuses they claim to be.
	for name, want := range map[string]int{"204": 204, "400 body": 400, "400 token": 400, "401": 401, "429": 429, "502": 502} {
		if got := answers[name].Code; got != want {
			t.Errorf("%s: status = %d, want %d", name, got, want)
		}
	}
	for name, rec := range answers {
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", name, got)
		}
	}
}

func TestSignOut_NeverLogsSecrets(t *testing.T) {
	access := signOutAccess(t, subjectS1)
	sig := access[strings.LastIndex(access, ".")+1:]
	granted := answer(http.StatusOK, signOutGranted(access))
	ok := answer(http.StatusNoContent, "")
	cases := []struct {
		name          string
		token, logout http.HandlerFunc
		closed        bool
	}{
		{"204", granted, ok, false},
		{"refresh 401", answer(400, gtRefreshNotFound), ok, false},
		{"logout 401", granted, answer(401, gtError(401, "session_not_found")), false},
		{"refresh 500", answer(500, gtInternal), ok, false},
		{"refresh 200 without access_token", answer(200, `{"refresh_token":"`+signOutR1+`"}`), ok, false},
		{"logout 500", granted, answer(500, gtInternal), false},
		{"refresh unreachable", nil, nil, true},
		{"logout unreachable", granted, dropped, false},
	}
	logs := map[string]string{}
	for _, c := range cases {
		fake := newSignOutFake(t, c.token, c.logout)
		authURL := fake.URL
		if c.closed {
			authURL = closedURL(t)
		}
		log, buf := captureLog()
		doSignOut(SignOutHandler(authURL, testClient(), liveSessions(t), log), refreshBody(signOutR0))
		logs[c.name] = buf.String()
		for _, secret := range []string{signOutR0, signOutR1, access, sig, subjectS1} {
			if strings.Contains(buf.String(), secret) {
				t.Errorf("%s: log carries %q:\n%s", c.name, secret, buf.String())
			}
		}
	}

	// Positive control: each 500 logs one WARN naming its upstream status, and the logout step says so.
	refresh500 := warnsNaming(logs["refresh 500"], 500)
	logout500 := warnsNaming(logs["logout 500"], 500)
	if len(refresh500) != 1 || len(logout500) != 1 {
		t.Fatalf("WARN lines naming upstream_status=500: refresh step %d, logout step %d, want 1 each:\n%s\n%s",
			len(refresh500), len(logout500), logs["refresh 500"], logs["logout 500"])
	}
	if !strings.Contains(logout500[0], "logout") {
		t.Errorf("the logout-step WARN does not name the step: %s", logout500[0])
	}
	if strings.Contains(refresh500[0], "logout") {
		t.Errorf("the refresh-step WARN names the logout step: %s", refresh500[0])
	}
}

// warnsNaming returns the WARN lines whose upstream_status is status.
func warnsNaming(logs string, status int) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(logs))
	for sc.Scan() {
		var line map[string]any
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if s, ok := line["upstream_status"].(float64); ok && int(s) == status && line["level"] == "WARN" {
			out = append(out, sc.Text())
		}
	}
	return out
}

func TestSignOut_LogoutSurvivesClientCancel(t *testing.T) {
	access := signOutAccess(t, subjectS1)
	arrived := make(chan struct{})
	release, open := gate(t)
	var mu sync.Mutex
	var completed []string // bearers of /logout calls answered on a live connection
	fake := newSignOutFake(t, answer(http.StatusOK, signOutGranted(access)), func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-release
		// A caller that abandoned the call closes the connection; give the server time to see it.
		select {
		case <-r.Context().Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
		mu.Lock()
		completed = append(completed, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	rg := newSignOutRig(t, fake, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/auth/sign-out", strings.NewReader(refreshBody(signOutR0)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		rg.handler.ServeHTTP(rec, req)
	}()

	select {
	case <-arrived:
		cancel() // the app gives up once the grant has rotated the token
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler neither finished nor reached /logout within 5s")
	}
	open()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not finish within 5s of the logout answer")
	}

	mu.Lock()
	got := slices.Clone(completed)
	mu.Unlock()
	if want := []string{"Bearer " + access}; !slices.Equal(got, want) {
		t.Errorf("completed /logout calls = %q, want %q", got, want)
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204: the logout outlived the caller: %s", rec.Code, rec.Body.String())
	}
	if !rg.evicted() {
		t.Error("S1 is still cached after a completed logout")
	}
}

// The app aborts at 5s while GoTrue may already have rotated the token, so the grant must outlive the caller too.
func TestSignOut_GrantSurvivesClientCancel(t *testing.T) {
	access := signOutAccess(t, subjectS1)
	arrived := make(chan struct{})
	release, open := gate(t)
	var mu sync.Mutex
	var completed []string // bearers of /logout calls answered on a live connection
	fake := newSignOutFake(t, func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-release
		select {
		case <-r.Context().Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
		answer(http.StatusOK, signOutGranted(access))(w, r)
	}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		completed = append(completed, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	rg := newSignOutRig(t, fake, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/auth/sign-out", strings.NewReader(refreshBody(signOutR0)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		rg.handler.ServeHTTP(rec, req)
	}()

	select {
	case <-arrived:
		cancel() // the app gives up while the grant is in flight
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler neither finished nor reached /token within 5s")
	}
	open()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not finish within 5s of the grant answer")
	}

	mu.Lock()
	got := slices.Clone(completed)
	mu.Unlock()
	if want := []string{"Bearer " + access}; !slices.Equal(got, want) {
		t.Errorf("completed /logout calls = %q, want %q", got, want)
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if !rg.evicted() {
		t.Error("S1 is still cached after a completed logout")
	}
}

func TestSignOut_JoinsUnderAnAuthURLPrefix(t *testing.T) {
	fake := newSignOutFake(t, answer(http.StatusOK, signOutGranted(signOutAccess(t, subjectS1))), answer(http.StatusNoContent, ""))
	h := SignOutHandler(fake.URL.JoinPath("prefix"), testClient(), liveSessions(t), slog.New(slog.DiscardHandler))

	if rec := doSignOut(h, refreshBody(signOutR0)); rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	var got []string
	for _, c := range fake.Calls() {
		got = append(got, c.Path+"?"+c.RawQuery)
	}
	if want := []string{"/prefix/token?grant_type=refresh_token", "/prefix/logout?scope=global"}; !slices.Equal(got, want) {
		t.Errorf("GoTrue saw %v, want %v", got, want)
	}
}

// headerProbe runs probe when the handler commits its status, before any client could read it.
type headerProbe struct {
	*httptest.ResponseRecorder
	probe func()
}

func (p *headerProbe) WriteHeader(code int) {
	p.probe()
	p.ResponseRecorder.WriteHeader(code)
}

func TestSignOut_EvictsBeforeItAnswers(t *testing.T) {
	fake := newSignOutFake(t, answer(http.StatusOK, signOutGranted(signOutAccess(t, subjectS1))), answer(http.StatusNoContent, ""))
	rg := newSignOutRig(t, fake, nil, nil)
	var probes []bool
	w := &headerProbe{httptest.NewRecorder(), func() { probes = append(probes, rg.evicted()) }}
	req := httptest.NewRequest(http.MethodPost, "/auth/sign-out", strings.NewReader(refreshBody(signOutR0)))
	req.Header.Set("Content-Type", "application/json")

	rg.handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", w.Code, w.Body.String())
	}
	if len(probes) != 1 {
		t.Fatalf("status committed %d times, want once", len(probes))
	}
	if !probes[0] {
		t.Error("S1 was still cached when the 204 was committed; a client could reuse it before the eviction")
	}
}

// D21: a grant whose access token names no readable subject still logs out; the answer follows the logout.
func TestSignOut_SubjectlessAccessTokenStillLogsOut(t *testing.T) {
	enc := base64.RawURLEncoding.EncodeToString
	noSub, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"session_id": "9c8b7a6d-5e4f-4321-8a9b-0c1d2e3f4a5b", "aud": "authenticated",
	}).SignedString([]byte("gotrue-private"))
	if err != nil {
		t.Fatalf("sign access token: %v", err)
	}
	// sub decodes before exp fails: a partial parse must not evict the half-read subject.
	halfRead := enc([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + enc([]byte(`{"sub":"`+subjectS1+`","exp":"soon"}`)) + "." + enc([]byte("sig"))
	const opaque = "opaque-access-7k3m9q"
	cases := []struct {
		name, access string
		logout       http.HandlerFunc
		wantStatus   int
		wantMsg      string
		wantWarns    int
	}{
		{"opaque token logout 204", opaque, answer(http.StatusNoContent, ""), 204, "", 1},
		{"JWT without sub logout 204", noSub, answer(http.StatusNoContent, ""), 204, "", 1},
		{"sub then undecodable exp logout 204", halfRead, answer(http.StatusNoContent, ""), 204, "", 1},
		{"opaque token logout 401", opaque, answer(401, gtError(401, "session_not_found")), 204, "", 1},
		{"opaque token logout 500", opaque, answer(500, gtInternal), 502, msgSignOutUnavailable, 2},
		{"opaque token logout 429", opaque, answer(429, gtOverRequestRateLimit), 429, msgTooMany, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := newSignOutFake(t, answer(http.StatusOK, signOutGranted(c.access)), c.logout)
			log, buf := captureLog()
			rg := newSignOutRig(t, fake, nil, log)

			rec := doSignOut(rg.handler, refreshBody(signOutR0))

			if c.wantMsg == "" {
				if rec.Code != c.wantStatus || rec.Body.Len() != 0 {
					t.Errorf("answer = %d %q, want %d with no body", rec.Code, rec.Body.String(), c.wantStatus)
				}
			} else {
				requireRefusal(t, rec, c.wantStatus, c.wantMsg)
			}
			var bearers []string
			for _, call := range fake.Calls() {
				if strings.HasSuffix(call.Path, "/logout") {
					bearers = append(bearers, call.Auth)
				}
			}
			if want := []string{"Bearer " + c.access}; !slices.Equal(bearers, want) {
				t.Errorf("/logout bearers = %q, want %q", bearers, want)
			}
			if rg.evicted() {
				t.Error("S1 was evicted on a token that names no readable subject")
			}
			var warns []string
			sc := bufio.NewScanner(strings.NewReader(buf.String()))
			for sc.Scan() {
				var line map[string]any
				if json.Unmarshal(sc.Bytes(), &line) == nil && line["level"] == "WARN" {
					warns = append(warns, sc.Text())
				}
			}
			if len(warns) != c.wantWarns {
				t.Errorf("WARN lines = %d, want %d:\n%s", len(warns), c.wantWarns, buf.String())
			}
			for _, secret := range []string{c.access, signOutR0, signOutR1, subjectS1} {
				if strings.Contains(buf.String(), secret) {
					t.Errorf("log carries %q:\n%s", secret, buf.String())
				}
			}
		})
	}
}
