package gateway

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ihRig is the invitee handler over a fake GoTrue and one hand-off store.
type ihRig struct {
	f     *resetGoTrue
	store *HandoffStore
	h     http.Handler
	log   *bytes.Buffer
}

func newIHRig(t *testing.T, store *HandoffStore) *ihRig {
	t.Helper()
	f := newResetGoTrue(t)
	log, buf := captureLog()
	th := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, newTestClock().Now)
	h := InvitationPasswordHandler(f.URL, siteURL(t), testClient(), liveSessions(t), th, nil, log, store)
	return &ihRig{f: f, store: store, h: h, log: buf}
}

// submit posts the set-password form; an empty state sends no state field.
func (g *ihRig) submit(t *testing.T, state string) *httptest.ResponseRecorder {
	t.Helper()
	v := rpValues(rpToken, "signup", rpPass)
	if state != "" {
		v.Set("state", state)
	}
	return ipPost(t, g.h, v)
}

// ihControlMints requires that a valid-state submit mints a code, so a "no code" assertion cannot pass on a handler that never mints.
func ihControlMints(t *testing.T) {
	t.Helper()
	g := newIHRig(t, testHandoffStore())
	vhCode(t, g.submit(t, vhState))
	if n := storeMapEntries(g.store); n != 1 {
		t.Fatalf("control: store holds %d codes, want 1", n)
	}
}

// ihExchanged requires a 200 exchange and returns its body decoded.
func ihExchanged(t *testing.T, store *HandoffStore, code, state string) map[string]string {
	t.Helper()
	rec := vhExchange(store, code, state)
	if rec.Code != http.StatusOK {
		t.Fatalf("exchange = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("exchange body %q: %v", rec.Body.String(), err)
	}
	return got
}

func TestInvitationPassword_StateAndSuccessRedirectsWithACode(t *testing.T) {
	g := newIHRig(t, testHandoffStore())

	rec := g.submit(t, vhState)

	vhCode(t, rec)
	if n := storeMapEntries(g.store); n != 1 {
		t.Errorf("store holds %d codes, want 1", n)
	}
	if n := g.f.count(http.MethodPost, "/token"); n != 1 {
		t.Errorf("POST /token calls = %d, want 1", n)
	}
}

func TestInvitationPassword_CodeRedeemsForTheGrantSessionWithTheClickState(t *testing.T) {
	g := newIHRig(t, testHandoffStore())
	code := vhCode(t, g.submit(t, vhState))

	got := ihExchanged(t, g.store, code, vhState)

	// Keys are the exchange answer SignInHandler stores.
	want := map[string]string{"access_token": rpGrantAccess, "refresh_token": rpGrantRefresh}
	if !maps.Equal(got, want) {
		t.Errorf("exchange body = %v, want exactly the grant's session %v, not the verify session (%s, %s)", got, want, rpAccess, rpRefresh)
	}
}

func TestInvitationPassword_CodeRefusesAnotherStateAndASecondUse(t *testing.T) {
	g := newIHRig(t, testHandoffStore())
	spoiled := vhCode(t, g.submit(t, vhState))
	good := vhCode(t, g.submit(t, vhState))
	if spoiled == good {
		t.Fatalf("two submits minted the same code %q", good)
	}

	rec := vhExchange(g.store, spoiled, vhOtherState)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("exchange with another state = %d, want 400", rec.Code)
	}
	for _, s := range []string{rpGrantAccess, rpGrantRefresh} {
		if strings.Contains(rec.Body.String(), s) {
			t.Errorf("refusal body carries %q: %s", s, rec.Body.String())
		}
	}
	if rec := vhExchange(g.store, spoiled, vhState); rec.Code != http.StatusBadRequest {
		t.Errorf("exchange of a code spent by a wrong state = %d, want 400", rec.Code)
	}
	// Control: the other code redeems, so the refusals above are about the state and the spend.
	if rec := vhExchange(g.store, good, vhState); rec.Code != http.StatusOK {
		t.Errorf("exchange of the control code = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if rec := vhExchange(g.store, good, vhState); rec.Code != http.StatusBadRequest {
		t.Errorf("second exchange of one code = %d, want 400", rec.Code)
	}
}

func TestInvitationPassword_CodeExpiresAfterTheHandoffTTL(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	g := newIHRig(t, NewHandoffStore(HandoffTTL, func() time.Time { return now }))
	expiring := vhCode(t, g.submit(t, vhState))
	live := vhCode(t, g.submit(t, vhState))

	now = now.Add(HandoffTTL - time.Nanosecond)
	if rec := vhExchange(g.store, live, vhState); rec.Code != http.StatusOK {
		t.Fatalf("exchange just inside the TTL = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	now = now.Add(time.Nanosecond)
	if rec := vhExchange(g.store, expiring, vhState); rec.Code != http.StatusBadRequest {
		t.Errorf("exchange at the TTL = %d, want 400", rec.Code)
	}
}

func TestInvitationPassword_ConcurrentSubmitsMintAtMostOneCode(t *testing.T) {
	g := newIHRig(t, testHandoffStore())
	var spent atomic.Bool
	ok := rpVerifyAnswer(subjectS1, rpEmail)
	refused := answer(http.StatusForbidden, rpOTPExpired)
	g.f.verify = func(w http.ResponseWriter, r *http.Request) {
		if spent.Swap(true) {
			refused(w, r)
			return
		}
		ok(w, r)
	}

	recs := make([]*httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range recs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			recs[i] = g.submit(t, vhState)
		}()
	}
	close(start)
	wg.Wait()

	var withCode, failed []*httptest.ResponseRecorder
	for _, rec := range recs {
		switch loc := rec.Header().Get("Location"); {
		case strings.HasPrefix(loc, vhCodePrefix):
			withCode = append(withCode, rec)
		case loc == ipFailed:
			failed = append(failed, rec)
		}
	}
	if len(withCode) != 1 || len(failed) != 1 {
		t.Fatalf("%d answers with a code and %d failed, want 1 and 1: %q, %q",
			len(withCode), len(failed), recs[0].Header().Get("Location"), recs[1].Header().Get("Location"))
	}
	if n := g.f.count(http.MethodPost, "/token"); n != 1 {
		t.Errorf("POST /token calls = %d, want 1", n)
	}
	if n := storeMapEntries(g.store); n != 1 {
		t.Errorf("store holds %d codes, want 1", n)
	}
	if rec := vhExchange(g.store, vhCode(t, withCode[0]), vhState); rec.Code != http.StatusOK {
		t.Errorf("exchange of the one code = %d, want 200", rec.Code)
	}
}

func TestInvitationPassword_GrantRunsAfterTheGlobalSignOutWithTheChosenPassword(t *testing.T) {
	const invitee = "grant-probe@corp.example"
	g := newIHRig(t, testHandoffStore())
	g.f.verify = rpVerifyAnswer(subjectS1, invitee)

	rec := g.submit(t, vhState)

	vhCode(t, rec)
	want := []string{"POST /verify", "PUT /user", "POST /logout", "POST /token"}
	if got := g.f.names(); !slices.Equal(got, want) {
		t.Fatalf("GoTrue calls = %v, want %v in that order", got, want)
	}
	calls := g.f.Calls()
	if calls[2].RawQuery != "scope=global" {
		t.Errorf("logout query = %q, want scope=global", calls[2].RawQuery)
	}
	grant := calls[3]
	if grant.RawQuery != "grant_type=password" {
		t.Errorf("token query = %q, want grant_type=password", grant.RawQuery)
	}
	if got, want := rpJSON(t, grant.Body), map[string]any{"email": invitee, "password": rpPass}; !maps.Equal(got, want) {
		t.Errorf("token body = %v, want exactly the verify answer's email and the submitted password %v", got, want)
	}
	if grant.Auth != "" {
		t.Errorf("token Authorization = %q, want none", grant.Auth)
	}
	if !strings.HasPrefix(grant.ContentType, "application/json") {
		t.Errorf("token Content-Type = %q, want application/json", grant.ContentType)
	}
}

func TestInvitationPassword_SamePasswordWithASignOutStillHandsOff(t *testing.T) {
	g := newIHRig(t, testHandoffStore())
	g.f.user = answer(http.StatusUnprocessableEntity, rpSamePassword)

	rec := g.submit(t, vhState)

	vhCode(t, rec)
	want := []string{"POST /verify", "PUT /user", "POST /logout", "POST /token"}
	if got := g.f.names(); !slices.Equal(got, want) {
		t.Errorf("GoTrue calls = %v, want %v", got, want)
	}
}

func TestInvitationPassword_NoOrBadStateConfirmsWithoutACodeAndMakesNoGrant(t *testing.T) {
	t.Run("control: a valid state mints a code", ihControlMints)

	plus := strings.Replace(vhState, "_", "+", 1)
	rows := []struct {
		name  string
		state []string
	}{
		{"no state", nil},
		{"short state", []string{"short"}},
		{"state with plus", []string{plus}},
		{"44 characters", []string{vhState + "A"}},
		{"42 characters", []string{vhState[:42]}},
		{"repeated, malformed first", []string{"short", vhState}},
		{"repeated, identical valid states", []string{vhState, vhState}},
		{"repeated, different valid states", []string{vhState, vhOtherState}},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			g := newIHRig(t, testHandoffStore())
			v := rpValues(rpToken, "signup", rpPass)
			v["state"] = c.state

			rpRequireRedirect(t, ipPost(t, g.h, v), ipOK)

			want := []string{"POST /verify", "PUT /user", "POST /logout"}
			if got := g.f.names(); !slices.Equal(got, want) {
				t.Errorf("GoTrue calls = %v, want exactly the three pre-existing %v", got, want)
			}
			if n := storeMapEntries(g.store); n != 0 {
				t.Errorf("store holds %d codes, want 0", n)
			}
		})
	}
}

func TestInvitationPassword_FailedSignOutFallsBackToVerifiedAndMakesNoGrant(t *testing.T) {
	t.Run("control: a valid state mints a code", ihControlMints)

	g := newIHRig(t, testHandoffStore())
	g.f.logout = answer(http.StatusInternalServerError, `{}`)

	rpRequireRedirect(t, g.submit(t, vhState), ipOK)

	want := []string{"POST /verify", "PUT /user", "POST /logout"}
	if got := g.f.names(); !slices.Equal(got, want) {
		t.Errorf("GoTrue calls = %v, want %v and no POST /token", got, want)
	}
	if n := storeMapEntries(g.store); n != 0 {
		t.Errorf("store holds %d codes, want 0", n)
	}
	rpRequireWarn(t, g.log, "invitation-password: global sign-out failed", map[string]any{"upstream_status": float64(http.StatusInternalServerError)})
}

// A gone logout answer (a goneCodes error_code) still confirms, but only a 2xx sign-out hands off.
func TestInvitationPassword_GoneSignOutConfirmsWithoutACodeAndMakesNoGrant(t *testing.T) {
	t.Run("control: a valid state mints a code", ihControlMints)

	for code := range goneCodes {
		t.Run(code, func(t *testing.T) {
			g := newIHRig(t, testHandoffStore())
			g.f.logout = answer(http.StatusUnauthorized, `{"error_code":"`+code+`"}`)

			rpRequireRedirect(t, g.submit(t, vhState), ipOK)

			want := []string{"POST /verify", "PUT /user", "POST /logout"}
			if got := g.f.names(); !slices.Equal(got, want) {
				t.Errorf("GoTrue calls = %v, want %v and no POST /token", got, want)
			}
			if n := storeMapEntries(g.store); n != 0 {
				t.Errorf("store holds %d codes, want 0", n)
			}
		})
	}
}

func TestInvitationPassword_FailedOrIncompleteGrantFallsBackToVerified(t *testing.T) {
	rows := []struct {
		name     string
		token    http.HandlerFunc
		attrs    map[string]any
		wantKeys []string
	}{
		{"400 invalid_credentials", answer(http.StatusBadRequest, `{"code":400,"error_code":"invalid_credentials","msg":"Invalid login credentials"}`),
			map[string]any{"upstream_status": float64(http.StatusBadRequest), "error_code": "invalid_credentials"}, nil},
		{"200 with only access_token", answer(http.StatusOK, `{"access_token":"`+rpGrantAccess+`"}`),
			map[string]any{"upstream_status": float64(http.StatusOK)}, nil},
		{"200 with only refresh_token", answer(http.StatusOK, `{"refresh_token":"`+rpGrantRefresh+`"}`),
			map[string]any{"upstream_status": float64(http.StatusOK)}, nil},
		{"transport error", dropped, nil, []string{"error"}},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			g := newIHRig(t, testHandoffStore())
			g.f.token = c.token

			rpRequireRedirect(t, g.submit(t, vhState), ipOK)

			if n := g.f.count(http.MethodPost, "/token"); n != 1 {
				t.Fatalf("POST /token calls = %d, want 1, so the grant ran and failed", n)
			}
			rpRequireWarn(t, g.log, "invitation-password: sign-in after set failed", c.attrs)
			for _, k := range c.wantKeys {
				if recs := recordsNamed(t, g.log, "invitation-password: sign-in after set failed"); len(recs) != 1 || recs[0][k] == nil {
					t.Errorf("the WARN lacks attribute %q: %s", k, g.log.String())
				}
			}
			if n := storeMapEntries(g.store); n != 0 {
				t.Errorf("store holds %d codes, want 0", n)
			}
		})
	}
}

func TestInvitationPassword_FullStoreFallsBackAndWarns(t *testing.T) {
	store := testHandoffStore()
	for range HandoffMaxLive {
		if _, ok := store.Put("x", [32]byte{}); !ok {
			t.Fatal("could not fill the store")
		}
	}
	g := newIHRig(t, store)

	rpRequireRedirect(t, g.submit(t, vhState), ipOK)

	if n := g.f.count(http.MethodPost, "/token"); n != 1 {
		t.Fatalf("POST /token calls = %d, want 1, so the grant ran before the store refused", n)
	}
	rpRequireWarn(t, g.log, "invitation-password: hand-off store full", nil)
	if n := storeMapEntries(store); n != HandoffMaxLive {
		t.Errorf("store holds %d codes, want the %d it was filled with", n, HandoffMaxLive)
	}
}

func TestInvitationPassword_RefusalsMintNoGrantAndNoCode(t *testing.T) {
	t.Run("control: a valid state mints a code", ihControlMints)

	sessionless := `{"refresh_token":"` + rpRefresh + `","user":{"id":"` + subjectS1 + `","email":"` + rpEmail + `"}}`
	rows := []struct {
		name  string
		set   func(f *resetGoTrue)
		calls []string
	}{
		{"verify 403 otp_expired", func(f *resetGoTrue) { f.verify = answer(http.StatusForbidden, rpOTPExpired) },
			[]string{"POST /verify"}},
		{"verify 200 without access_token", func(f *resetGoTrue) { f.verify = answer(http.StatusOK, sessionless) },
			[]string{"POST /verify"}},
		{"PUT 500", func(f *resetGoTrue) { f.user = answer(http.StatusInternalServerError, `{}`) },
			[]string{"POST /verify", "PUT /user"}},
		{"same_password with a failed sign-out", func(f *resetGoTrue) {
			f.user = answer(http.StatusUnprocessableEntity, rpSamePassword)
			f.logout = answer(http.StatusInternalServerError, `{}`)
		}, []string{"POST /verify", "PUT /user", "POST /logout"}},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			g := newIHRig(t, testHandoffStore())
			c.set(g.f)

			rpRequireRedirect(t, g.submit(t, vhState), ipFailed)

			if got := g.f.names(); !slices.Equal(got, c.calls) {
				t.Errorf("GoTrue calls = %v, want %v and no POST /token", got, c.calls)
			}
			if n := storeMapEntries(g.store); n != 0 {
				t.Errorf("store holds %d codes, want 0", n)
			}
		})
	}
}

func TestInvitationPassword_EachSubmitGetsItsOwnCodeBoundToItsOwnState(t *testing.T) {
	g := newIHRig(t, testHandoffStore())

	first := g.submit(t, vhState)
	second := g.submit(t, vhOtherState)

	code1, code2 := vhCode(t, first), vhCode(t, second)
	if code1 == code2 {
		t.Fatalf("two submits minted the same code %q", code1)
	}
	if loc := second.Header().Get("Location"); loc != vhCodePrefix+code2 || strings.Contains(loc, code1) {
		t.Errorf("second Location = %q, want %s%s and no trace of the first code", loc, vhCodePrefix, code2)
	}
	if rec := vhExchange(g.store, code2, vhState); rec.Code != http.StatusBadRequest {
		t.Errorf("second code with the first state = %d, want 400", rec.Code)
	}
	if rec := vhExchange(g.store, code1, vhState); rec.Code != http.StatusOK {
		t.Errorf("first code with its own state = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// Each parallel submit chooses its own password and state; a code must redeem only the grant minted for that submit.
func TestInvitationPassword_ParallelSubmitsNeverCrossCodesOrSessions(t *testing.T) {
	const n = 12
	g := newIHRig(t, testHandoffStore())
	g.f.token = func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Password string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		answer(http.StatusOK, `{"access_token":"at-`+in.Password+`","refresh_token":"rt-`+in.Password+`"}`)(w, r)
	}
	state := func(i int) string { return strings.Repeat(string(rune('A'+i)), 43) }

	recs := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range recs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			v := rpValues(rpToken, "signup", "pw-"+strconv.Itoa(i)+"-padded")
			v.Set("state", state(i))
			recs[i] = ipPost(t, g.h, v)
		}()
	}
	close(start)
	wg.Wait()

	seen := map[string]bool{}
	for i, rec := range recs {
		code := vhCode(t, rec)
		if seen[code] {
			t.Fatalf("code %q minted twice", code)
		}
		seen[code] = true
		if rec.Header().Get("Location") != vhCodePrefix+code {
			t.Errorf("submit %d Location = %q, want exactly one code", i, rec.Header().Get("Location"))
		}
		if i%2 == 1 {
			if rec := vhExchange(g.store, code, state((i+1)%n)); rec.Code != http.StatusBadRequest {
				t.Errorf("submit %d code with a neighbour's state = %d, want 400", i, rec.Code)
			}
			continue
		}
		want := map[string]string{"access_token": "at-pw-" + strconv.Itoa(i) + "-padded", "refresh_token": "rt-pw-" + strconv.Itoa(i) + "-padded"}
		if got := ihExchanged(t, g.store, code, state(i)); !maps.Equal(got, want) {
			t.Errorf("submit %d session = %v, want its own grant %v", i, got, want)
		}
	}
	if len(seen) != n {
		t.Fatalf("%d codes for %d submits", len(seen), n)
	}
}
