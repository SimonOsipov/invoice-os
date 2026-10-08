package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	vhState      = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-abcde" // 43 base64url, stateShape
	vhOtherState = "ZyXwVuTsRqPoNmLkJiHgFeDcBa9876543210-_EDCBA"
	vhCodePrefix = siteURLValue + "/?verified=1&handoff="
)

var vhCodeShape = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func testHandoffStore() *HandoffStore { return NewHandoffStore(HandoffTTL, time.Now) }

// vhClick is the confirm click: the page's form POST carrying token, type and state.
func vhClick(h http.Handler, state string) *httptest.ResponseRecorder {
	q := verifyQuery
	if state != "" {
		q += "&state=" + url.QueryEscape(state)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, verifyRequest(context.Background(), q))
	return rec
}

func vhVerifier(t *testing.T, store *HandoffStore, status int, body string) (http.Handler, *fakeGoTrue) {
	t.Helper()
	fake := newFakeGoTrue(t, status, body)
	return VerifyHandler(fake.URL, siteURL(t), testClient(), nopLog(), nil, store), fake
}

func nopLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// vhCode requires the success Location and returns its code.
func vhCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, vhCodePrefix) {
		t.Fatalf("answer = %d Location %q, want 303 %s<code>", rec.Code, loc, vhCodePrefix)
	}
	code := strings.TrimPrefix(loc, vhCodePrefix)
	if !vhCodeShape.MatchString(code) {
		t.Fatalf("code %q is not 43 base64url characters", code)
	}
	return code
}

func vhExchange(store *HandoffStore, code, state string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"code": code, "state": state})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/exchange", strings.NewReader(string(body)))
	ExchangeHandler(store).ServeHTTP(rec, req)
	return rec
}

func vhLogRecords(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(raw) {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func TestVerify_ClickWithStateRedirectsWithACode(t *testing.T) {
	store := testHandoffStore()
	h, fake := vhVerifier(t, store, http.StatusOK, gtSession)

	rec := vhClick(h, vhState)

	vhCode(t, rec)
	if calls := fake.Calls(); len(calls) != 1 || calls[0].Method != http.MethodPost || calls[0].Path != "/verify" {
		t.Fatalf("GoTrue saw %+v, want exactly one POST /verify", calls)
	}
	if n := storeMapEntries(store); n != 1 {
		t.Errorf("store holds %d codes, want 1", n)
	}
}

func TestVerify_CodeRedeemsForTheSessionWithTheClickState(t *testing.T) {
	store := testHandoffStore()
	h, _ := vhVerifier(t, store, http.StatusOK, gtSession)
	code := vhCode(t, vhClick(h, vhState))

	rec := vhExchange(store, code, vhState)

	if rec.Code != http.StatusOK {
		t.Fatalf("exchange = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("exchange body %q: %v", rec.Body.String(), err)
	}
	if want := map[string]string{"access_token": sessionAT, "refresh_token": sessionRT}; !maps.Equal(got, want) {
		t.Errorf("exchange body = %v, want exactly %v", got, want)
	}
}

func TestVerify_CodeRefusesAnotherStateAndASecondUse(t *testing.T) {
	store := testHandoffStore()
	h, _ := vhVerifier(t, store, http.StatusOK, gtSession)
	spoiled := vhCode(t, vhClick(h, vhState))
	good := vhCode(t, vhClick(h, vhState))
	if spoiled == good {
		t.Fatalf("two clicks minted the same code %q", good)
	}

	rec := vhExchange(store, spoiled, vhOtherState)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("exchange with another state = %d, want 400", rec.Code)
	}
	for _, s := range []string{sessionAT, sessionRT} {
		if strings.Contains(rec.Body.String(), s) {
			t.Errorf("refusal body carries %q: %s", s, rec.Body.String())
		}
	}
	if rec := vhExchange(store, spoiled, vhState); rec.Code != http.StatusBadRequest {
		t.Errorf("exchange of a code spent by a wrong state = %d, want 400", rec.Code)
	}
	// Control: the other code redeems, so the refusals above are about the state and the spend.
	if rec := vhExchange(store, good, vhState); rec.Code != http.StatusOK {
		t.Errorf("exchange of the control code = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if rec := vhExchange(store, good, vhState); rec.Code != http.StatusBadRequest {
		t.Errorf("second exchange of one code = %d, want 400", rec.Code)
	}
}

func TestVerify_CodeExpiresAfterTheHandoffTTL(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	store := NewHandoffStore(HandoffTTL, func() time.Time { return now })
	h, _ := vhVerifier(t, store, http.StatusOK, gtSession)
	expiring := vhCode(t, vhClick(h, vhState))
	live := vhCode(t, vhClick(h, vhState))

	now = now.Add(HandoffTTL - time.Nanosecond)
	if rec := vhExchange(store, live, vhState); rec.Code != http.StatusOK {
		t.Fatalf("exchange just inside the TTL = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	now = now.Add(time.Nanosecond)
	if rec := vhExchange(store, expiring, vhState); rec.Code != http.StatusBadRequest {
		t.Errorf("exchange at the TTL = %d, want 400", rec.Code)
	}
}

func TestVerify_ConcurrentClicksMintAtMostOneCodePerVerifiedAnswer(t *testing.T) {
	var spent atomic.Bool
	gotrue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if spent.Swap(true) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(gtOTPExpired))
			return
		}
		_, _ = w.Write([]byte(gtSession))
	}))
	t.Cleanup(gotrue.Close)
	authURL, err := url.Parse(gotrue.URL)
	if err != nil {
		t.Fatal(err)
	}
	store := testHandoffStore()
	h := VerifyHandler(authURL, siteURL(t), testClient(), nopLog(), nil, store)

	recs := make([]*httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range recs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			recs[i] = vhClick(h, vhState)
		}()
	}
	close(start)
	wg.Wait()

	var withCode, failed []*httptest.ResponseRecorder
	for _, rec := range recs {
		switch loc := rec.Header().Get("Location"); {
		case strings.HasPrefix(loc, vhCodePrefix):
			withCode = append(withCode, rec)
		case loc == failedLocation:
			failed = append(failed, rec)
		}
	}
	if len(withCode) != 1 || len(failed) != 1 {
		t.Fatalf("%d answers with a code and %d failed, want 1 and 1: %q, %q",
			len(withCode), len(failed), recs[0].Header().Get("Location"), recs[1].Header().Get("Location"))
	}
	if n := storeMapEntries(store); n != 1 {
		t.Errorf("store holds %d codes, want 1", n)
	}
	if rec := vhExchange(store, vhCode(t, withCode[0]), vhState); rec.Code != http.StatusOK {
		t.Errorf("exchange of the one code = %d, want 200", rec.Code)
	}
}

// vhControlMintsACode requires that a click with a valid state on this store mints a code,
// so a "no code" assertion below cannot pass on a handler that never mints.
func vhControlMintsACode(t *testing.T, store *HandoffStore) {
	t.Helper()
	h, _ := vhVerifier(t, store, http.StatusOK, gtSession)
	vhCode(t, vhClick(h, vhState))
	if n := storeMapEntries(store); n != 1 {
		t.Fatalf("control: store holds %d codes, want 1", n)
	}
}

func TestVerify_StatelessClickConfirmsWithoutACode(t *testing.T) {
	vhControlMintsACode(t, testHandoffStore())

	plus := strings.Replace(vhState, "_", "+", 1)
	for _, c := range []struct{ name, state string }{
		{"no state", ""},
		{"short state", "short"},
		{"state with plus", plus},
		{"44 characters", vhState + "A"},
		{"42 characters", vhState[:42]},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := testHandoffStore()
			fake := newFakeGoTrue(t, http.StatusOK, coSession(coUser(coMetaFull)))
			sink := newRecSink(nil)
			h := VerifyHandler(fake.URL, siteURL(t), testClient(), nopLog(), sink, store)

			requireRedirect(t, vhClick(h, c.state), verifiedLocation)

			requireOneHandOff(t, sink, coWant)
			if n := storeMapEntries(store); n != 0 {
				t.Errorf("store holds %d codes, want 0", n)
			}
		})
	}
}

func TestVerify_SessionlessAnswerConfirmsWithoutACode(t *testing.T) {
	vhControlMintsACode(t, testHandoffStore())

	user := `"user":` + coUser(coMetaFull)
	for _, c := range []struct{ name, body string }{
		{"user only", `{` + user + `}`},
		{"access token only", `{"access_token":"` + sessionAT + `",` + user + `}`},
		{"refresh token only", `{"refresh_token":"` + sessionRT + `",` + user + `}`},
		{"empty access token", `{"access_token":"","refresh_token":"` + sessionRT + `",` + user + `}`},
		{"empty refresh token", `{"access_token":"` + sessionAT + `","refresh_token":"",` + user + `}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := testHandoffStore()
			fake := newFakeGoTrue(t, http.StatusOK, c.body)
			sink := newRecSink(nil)
			h := VerifyHandler(fake.URL, siteURL(t), testClient(), nopLog(), sink, store)

			requireRedirect(t, vhClick(h, vhState), verifiedLocation)

			requireOneHandOff(t, sink, coWant)
			if n := storeMapEntries(store); n != 0 {
				t.Errorf("store holds %d codes, want 0", n)
			}
		})
	}
}

func TestVerify_FullStoreConfirmsWithoutACodeAndWarns(t *testing.T) {
	// The control runs last, so the WARN assertion below is observed on its own.
	t.Cleanup(func() { vhControlMintsACode(t, testHandoffStore()) })

	store := testHandoffStore()
	for range HandoffMaxLive {
		if _, ok := store.Put("x", [32]byte{}); !ok {
			t.Fatal("could not fill the store")
		}
	}
	fake := newFakeGoTrue(t, http.StatusOK, coSession(coUser(coMetaFull)))
	sink := newRecSink(nil)
	log, buf := captureLog()
	h := VerifyHandler(fake.URL, siteURL(t), testClient(), log, sink, store)

	requireRedirect(t, vhClick(h, vhState), verifiedLocation)

	requireOneHandOff(t, sink, coWant)
	if n := storeMapEntries(store); n != HandoffMaxLive {
		t.Errorf("store holds %d codes, want the %d it was filled with", n, HandoffMaxLive)
	}
	warns := 0
	for _, rec := range vhLogRecords(t, buf.String()) {
		if rec["level"] == "WARN" && rec["msg"] == "verify: hand-off store full" {
			warns++
		}
	}
	if warns != 1 {
		t.Errorf("%d WARN records %q, want 1: %s", warns, "verify: hand-off store full", buf.String())
	}
	for _, s := range []string{vhState, sessionAT, sessionRT, verifyToken} {
		if strings.Contains(buf.String(), s) {
			t.Errorf("log carries %q: %s", s, buf.String())
		}
	}
}

func TestVerify_RefusalLogsTheGoTrueErrorCode(t *testing.T) {
	store := testHandoffStore()
	fake := newFakeGoTrue(t, http.StatusForbidden, gtOTPExpired)
	log, buf := captureLog()
	h := VerifyHandler(fake.URL, siteURL(t), testClient(), log, nil, store)

	requireRedirect(t, vhClick(h, vhState), failedLocation)

	var warns []map[string]any
	for _, rec := range vhLogRecords(t, buf.String()) {
		if rec["level"] == "WARN" {
			warns = append(warns, rec)
		}
	}
	if len(warns) != 1 {
		t.Fatalf("%d WARN records, want 1: %s", len(warns), buf.String())
	}
	w := warns[0]
	if w["msg"] != "verify: gotrue refused the link" || w["upstream_status"] != float64(http.StatusForbidden) || w["error_code"] != "otp_expired" {
		t.Errorf("WARN = %v, want msg %q, upstream_status 403, error_code %q", w, "verify: gotrue refused the link", "otp_expired")
	}
	if strings.Contains(buf.String(), "Email link is invalid") {
		t.Errorf("log carries GoTrue's msg text: %s", buf.String())
	}
	if n := storeMapEntries(store); n != 0 {
		t.Errorf("store holds %d codes after a refusal, want 0", n)
	}
}

// Three clicks, two states: each code redeems its own click's session, for its own state only.
// A repeated state field binds the first value, as the token and type do.
func TestVerify_EachCodeRedeemsItsOwnClickSession(t *testing.T) {
	var n atomic.Int32
	gotrue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		i := n.Add(1)
		_, _ = w.Write([]byte(`{"access_token":"at-` + string(rune('0'+i)) + `","refresh_token":"rt-` + string(rune('0'+i)) + `","user":{}}`))
	}))
	t.Cleanup(gotrue.Close)
	authURL, err := url.Parse(gotrue.URL)
	if err != nil {
		t.Fatal(err)
	}
	store := testHandoffStore()
	h := VerifyHandler(authURL, siteURL(t), testClient(), nopLog(), nil, store)

	first := vhCode(t, vhClick(h, vhState))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, verifyRequest(context.Background(), verifyQuery+"&state="+vhOtherState+"&state="+vhState))
	second := vhCode(t, rec)

	if rec := vhExchange(store, second, vhState); rec.Code != http.StatusBadRequest {
		t.Errorf("second code with the later repeated state = %d, want 400", rec.Code)
	}
	third := vhCode(t, vhClick(h, vhOtherState))
	for _, c := range []struct{ code, state, want string }{
		{first, vhState, `{"access_token":"at-1","refresh_token":"rt-1"}`},
		{third, vhOtherState, `{"access_token":"at-3","refresh_token":"rt-3"}`},
	} {
		rec := vhExchange(store, c.code, c.state)
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != c.want {
			t.Errorf("exchange = %d %q, want 200 %s", rec.Code, rec.Body.String(), c.want)
		}
	}
}
