package gateway

import (
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSignIn_ExtraAndDuplicateFieldsNeverReachGoTrue(t *testing.T) {
	fake := newTokenFake(t, http.StatusBadRequest, gtInvalidCredentials)
	rig := newSignInRig(t, fake.URL, nil)
	s := randomState(t)
	const decoy, victim = "decoy@corp.example", "victim@corp.example"

	body := `{"email":"` + decoy + `","email":"` + victim + `","password":"` + regPassword + `","state":"` + s +
		`","grant_type":"refresh_token","refresh_token":"x","data":{"role":"admin"},"gotrue_meta_security":{"captcha_token":"c"}}`
	requireRefusal(t, rig.doSignIn(body), http.StatusUnauthorized, msgBadCredentials)

	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("GoTrue saw %d calls, want 1", len(calls))
	}
	var sent map[string]any
	if err := json.Unmarshal(calls[0].Body, &sent); err != nil {
		t.Fatalf("token body %q is not JSON: %v", calls[0].Body, err)
	}
	if want := map[string]any{"email": victim, "password": regPassword}; !maps.Equal(sent, want) {
		t.Errorf("token body = %v, want exactly %v", sent, want)
	}
	if calls[0].RawQuery != "grant_type=password" {
		t.Errorf("query = %q, want grant_type=password", calls[0].RawQuery)
	}
	// The throttle counted the address GoTrue saw, not the decoy.
	requireNothingCounted(t, rig.throttle, decoy)
	reserveN(t, rig.throttle, victim, SignInMaxFailures-1)
	if rig.throttle.Reserve(victim) {
		t.Error("victim has a full budget left; the refused attempt was not counted against it")
	}
}

// Case and surrounding space do not open a second budget, and a success under any spelling resets it.
func TestSignIn_ThrottleKeyIgnoresCaseAndSpace(t *testing.T) {
	fake := newTokenFake(t, http.StatusBadRequest, gtInvalidCredentials)
	rig := newSignInRig(t, fake.URL, nil)
	s := randomState(t)
	variants := []string{"Victim@Corp.Example", " victim@corp.example ", "VICTIM@CORP.EXAMPLE", "victim@corp.example\t"}

	for i := range SignInMaxFailures - 1 {
		requireRefusal(t, rig.doSignIn(signInBody(variants[i%len(variants)], regPassword, s)), http.StatusUnauthorized, msgBadCredentials)
	}
	fake.set(http.StatusOK, gtSession)
	requireCode(t, rig.doSignIn(signInBody("  VICTIM@corp.EXAMPLE", regPassword, s)))

	fake.set(http.StatusBadRequest, gtInvalidCredentials)
	for i := range SignInMaxFailures {
		requireRefusal(t, rig.doSignIn(signInBody(variants[i%len(variants)], regPassword, s)), http.StatusUnauthorized, msgBadCredentials)
	}
	requireRefusal(t, rig.doSignIn(signInBody("victim@corp.example", regPassword, s)), http.StatusTooManyRequests, msgTooMany)
	if want := 2 * SignInMaxFailures; fake.Hits() != want {
		t.Errorf("GoTrue saw %d calls, want %d", fake.Hits(), want)
	}
}

func TestSignIn_UserBannedCounts(t *testing.T) {
	fake := newTokenFake(t, http.StatusBadRequest, gtUserBanned)
	rig := newSignInRig(t, fake.URL, nil)
	s := randomState(t)

	for i := 1; i <= SignInMaxFailures; i++ {
		requireRefusal(t, rig.doSignIn(signInBody(regEmail, regPassword, s)), http.StatusUnauthorized, msgBadCredentials)
	}
	requireRefusal(t, rig.doSignIn(signInBody(regEmail, regPassword, s)), http.StatusTooManyRequests, msgTooMany)
	if fake.Hits() != SignInMaxFailures {
		t.Errorf("GoTrue saw %d calls, want %d", fake.Hits(), SignInMaxFailures)
	}
}

func TestSignIn_GoTrue429Refunds(t *testing.T) {
	fake := newTokenFake(t, http.StatusTooManyRequests, gtOverRequestRateLimit)
	rig := newSignInRig(t, fake.URL, nil)
	s := randomState(t)

	for i := 1; i <= SignInMaxFailures+2; i++ {
		requireRefusal(t, rig.doSignIn(signInBody(regEmail, regPassword, s)), http.StatusTooManyRequests, msgTooMany)
	}
	if want := SignInMaxFailures + 2; fake.Hits() != want {
		t.Errorf("GoTrue saw %d calls, want %d", fake.Hits(), want)
	}
	requireNothingCounted(t, rig.throttle, regEmail)
}

// The limit is in bytes, which bounds the throttle key's memory (D19 ceiling).
func TestSignIn_EmailLimitCountsBytes(t *testing.T) {
	const domain = "@x.io"
	over := strings.Repeat("é", 125) + domain     // 255 bytes, 130 runes
	at := strings.Repeat("é", 124) + "a" + domain // 254 bytes
	if len(over) != 255 || utf8.RuneCountInString(over) > maxEmailBytes || len(at) != 254 {
		t.Fatalf("fixture sizes %d/%d/%d", len(over), utf8.RuneCountInString(over), len(at))
	}
	fake := newTokenFake(t, http.StatusBadRequest, gtInvalidCredentials)
	rig := newSignInRig(t, fake.URL, nil)
	s := randomState(t)

	requireRefusal(t, rig.doSignIn(signInBody(over, regPassword, s)), http.StatusBadRequest, msgInvalidEmail)
	if fake.Hits() != 0 {
		t.Fatalf("GoTrue saw %d calls for a 255-byte email, want 0", fake.Hits())
	}
	requireRefusal(t, rig.doSignIn(signInBody(at, regPassword, s)), http.StatusUnauthorized, msgBadCredentials)
	if fake.Hits() != 1 {
		t.Errorf("GoTrue saw %d calls for a 254-byte email, want 1", fake.Hits())
	}
}

func TestSignIn_Upstream200OverBodyCapIs502AndRefunds(t *testing.T) {
	big := `{"access_token":"` + sessionAT + `","user":{"user_metadata":{"pad":"` + strings.Repeat("x", maxGoTrueBodyBytes) + `"}}}`
	fake := newTokenFake(t, http.StatusOK, big)
	rig := newSignInRig(t, fake.URL, nil)
	s := randomState(t)

	requireRefusal(t, rig.doSignIn(signInBody(regEmail, regPassword, s)), http.StatusBadGateway, msgUnavailable)
	if n := storeMapEntries(rig.store); n != 0 {
		t.Errorf("store holds %d entries, want 0", n)
	}
	requireNothingCounted(t, rig.throttle, regEmail)

	// Positive control: the same session under the cap signs in.
	fake.set(http.StatusOK, `{"access_token":"`+sessionAT+`","user":{"user_metadata":{"pad":"x"}}}`)
	requireCode(t, newSignInRig(t, fake.URL, nil).doSignIn(signInBody(regEmail, regPassword, s)))
}

func TestSignIn_SlowGoTrueTimesOutAndRefunds(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtSession)
	fake.mu.Lock()
	fake.delay = 300 * time.Millisecond
	fake.mu.Unlock()
	clk := newTestClock()
	store := NewHandoffStore(HandoffTTL, clk.Now)
	th := NewSignInThrottle(SignInMaxFailures, SignInMaxKeys, SignInWindow, clk.Now)
	h := SignInHandler(fake.URL, &http.Client{Timeout: 50 * time.Millisecond}, store, th, slog.New(slog.DiscardHandler))
	s := randomState(t)

	for i := 1; i <= 2; i++ {
		requireRefusal(t, serve(h, http.MethodPost, "/auth/sign-in", signInBody(regEmail, regPassword, s)), http.StatusBadGateway, msgUnavailable)
	}
	if fake.Hits() != 2 {
		t.Errorf("GoTrue saw %d calls, want 2", fake.Hits())
	}
	if n := storeMapEntries(store); n != 0 {
		t.Errorf("store holds %d entries after a timeout, want 0", n)
	}
	requireNothingCounted(t, th, regEmail)
}

func TestSignIn_UnreachableLogOmitsAuthURLPassword(t *testing.T) {
	const secret = "gtSecretPw9"
	u := closedURL(t)
	u.User = url.UserPassword("svc", secret)
	log, buf := captureLog()
	rig := newSignInRig(t, u, log)

	requireRefusal(t, rig.doSignIn(signInBody(regEmail, regPassword, randomState(t))), http.StatusBadGateway, msgUnavailable)
	// Positive control: the logged error names the target, redacted by net/http.
	if !strings.Contains(buf.String(), "gotrue unreachable") || !strings.Contains(buf.String(), "svc:***@") {
		t.Fatalf("no redacted unreachable log line: %s", buf.String())
	}
	for _, s := range []string{secret, regEmail, regPassword} {
		if strings.Contains(buf.String(), s) {
			t.Errorf("log carries %q: %s", s, buf.String())
		}
	}
}

func TestExchange_ConcurrentRedeemOneWinner(t *testing.T) {
	const racers = 32
	rig := newSignInRig(t, closedURL(t), nil)
	s := randomState(t)
	code := rig.store.Put(sessionAT, stateHash(s))

	recs := make([]int, racers)
	bodies := make([]string, racers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := rig.doExchange(exchangeBody(code, s))
			recs[i], bodies[i] = rec.Code, rec.Body.String()
		}()
	}
	close(start)
	wg.Wait()

	wins := 0
	for i, c := range recs {
		switch c {
		case http.StatusOK:
			wins++
			if !strings.Contains(bodies[i], sessionAT) {
				t.Errorf("winner body %q lacks the token", bodies[i])
			}
		case http.StatusBadRequest:
			if !strings.Contains(bodies[i], msgBadCode) {
				t.Errorf("loser body %q, want %q", bodies[i], msgBadCode)
			}
		default:
			t.Errorf("racer %d: status %d", i, c)
		}
	}
	if wins != 1 {
		t.Errorf("%d exchanges won, want exactly 1", wins)
	}
}

func TestSignInAndExchange_OptionsAndHeadAre405WithAllow(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtSession)
	rig := newSignInRig(t, fake.URL, nil)
	for _, m := range []string{http.MethodOptions, http.MethodHead} {
		for route, h := range map[string]http.Handler{"/auth/sign-in": rig.signIn, "/auth/exchange": rig.exchange} {
			rec := serve(h, m, route, signInBody(regEmail, regPassword, randomState(t)))
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost || rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s %s: %d Allow=%q Cache-Control=%q, want 405 POST no-store", m, route, rec.Code, rec.Header().Get("Allow"), rec.Header().Get("Cache-Control"))
			}
		}
	}
	if fake.Hits() != 0 {
		t.Errorf("GoTrue saw %d calls, want 0", fake.Hits())
	}
}
