package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	aeIP     = "203.0.113.7"
	aeUA     = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	aeUserID = "7f3c2a1e-0b7d-4f51-9a0e-5d1c2b3a4e5f" // gtSession's user
	aeEmail  = "new@corp.example"                     // gtSession's user
)

// recEvents is an AccountEvents that keeps what it is given.
type recEvents struct {
	mu    sync.Mutex
	got   []AccountEvent
	err   error
	delay time.Duration
}

func (r *recEvents) Record(_ context.Context, e AccountEvent) error {
	time.Sleep(r.delay)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, e)
	return r.err
}

func (r *recEvents) events() []AccountEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]AccountEvent(nil), r.got...)
}

func (r *recEvents) requireNames(t *testing.T, want ...string) {
	t.Helper()
	var got []string
	for _, e := range r.events() {
		got = append(got, e.Event)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("recorded events = %v, want %v", got, want)
	}
}

// previewClient makes the request look like it came through a preview: the rightmost forwarded entry is the client.
func previewClient(t *testing.T, req *http.Request) {
	t.Helper()
	t.Setenv("DEPLOY_ENV", "pr-1")
	req.Header.Set("X-Forwarded-For", aeIP)
	req.Header.Set("User-Agent", aeUA)
}

func aeSignInHandler(t *testing.T, authURL *url.URL, th *SignInThrottle, store Handoffs, ev AccountEvents) http.Handler {
	t.Helper()
	if th == nil {
		th = NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, newTestClock().Now)
	}
	if store == nil {
		store = testHandoffStore()
	}
	return SignInHandler(authURL, testClient(), store, th, nopLog(), nil, ev)
}

func aeSignIn(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/sign-in", strings.NewReader(signInBody(aeEmail, "pw", randomState(t))))
	previewClient(t, req)
	h.ServeHTTP(rec, req)
	return rec
}

func TestAccountEvents_SignInSuccessRecordsOneSignedIn(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtSession)
	rec := &recEvents{}

	resp := aeSignIn(t, aeSignInHandler(t, fake.URL, nil, nil, rec))

	requireOnlyKey(t, resp, "code")
	rec.requireNames(t, EventSignedIn)
	want := AccountEvent{Event: EventSignedIn, UserID: aeUserID, Email: aeEmail, IP: aeIP, UserAgent: aeUA}
	if got := rec.events()[0]; got != want {
		t.Errorf("event = %+v, want %+v", got, want)
	}
}

func TestAccountEvents_WrongPasswordRecordsOneFailure(t *testing.T) {
	fake := newTokenFake(t, http.StatusBadRequest, gtInvalidCredentials)
	rec := &recEvents{}

	resp := aeSignIn(t, aeSignInHandler(t, fake.URL, nil, nil, rec))

	requireRefusal(t, resp, http.StatusUnauthorized, msgBadCredentials)
	rec.requireNames(t, EventSignInFailed)
	if e := rec.events()[0]; e.Email != aeEmail || e.UserID != "" {
		t.Errorf("event = %+v, want the email and no user id", e)
	}
}

func TestAccountEvents_ThrottleRefusalRecordsOneBlocked(t *testing.T) {
	fake := newTokenFake(t, http.StatusBadRequest, gtInvalidCredentials)
	th := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, newTestClock().Now)
	for range SignInMaxFailures {
		th.Reserve(aeEmail)
	}
	rec := &recEvents{}

	resp := aeSignIn(t, aeSignInHandler(t, fake.URL, th, nil, rec))

	requireRefusal(t, resp, http.StatusTooManyRequests, msgTooMany)
	rec.requireNames(t, EventSignInBlocked)
	if n := fake.Hits(); n != 0 {
		t.Errorf("GoTrue saw %d calls, want 0", n)
	}
}

func TestAccountEvents_UnrecordedSignInOutcomes(t *testing.T) {
	full := testHandoffStore()
	fillStore(t, full, HandoffMaxLive)
	deadThrottle := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, time.Now)
	deadThrottle.Shared(deadPool(t), nopLog())
	cases := []struct {
		name     string
		status   int
		body     string
		authURL  *url.URL
		throttle *SignInThrottle
		store    Handoffs
		want     int
	}{
		{name: "throttle database error", status: 400, body: gtInvalidCredentials, throttle: deadThrottle, want: 503},
		{name: "user_banned", status: 400, body: gtUserBanned, want: 401},
		{name: "email_not_confirmed", status: 400, body: gtEmailNotConfirmed, want: 403},
		{name: "gotrue 429", status: 429, body: `{}`, want: 429},
		{name: "gotrue 500", status: 500, body: `{}`, want: 502},
		{name: "transport error", authURL: closedURL(t), want: 502},
		{name: "store full", status: 200, body: gtSession, store: full, want: 503},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			authURL := c.authURL
			if authURL == nil {
				authURL = newTokenFake(t, c.status, c.body).URL
			}
			rec := &recEvents{}

			resp := aeSignIn(t, aeSignInHandler(t, authURL, c.throttle, c.store, rec))

			if resp.Code != c.want {
				t.Fatalf("status = %d, want %d", resp.Code, c.want)
			}
			rec.requireNames(t)
		})
	}
}

func aeSignOut(t *testing.T, tokenAnswer, logoutAnswer http.HandlerFunc, rec AccountEvents) *httptest.ResponseRecorder {
	t.Helper()
	f := newSignOutFake(t, tokenAnswer, logoutAnswer)
	h := SignOutHandler(f.URL, testClient(), liveSessions(t), nopLog(), rec)
	return doSignOut(h, `{"refresh_token":"`+signOutR0+`"}`)
}

func TestAccountEvents_SignOutRecordsTheSubject(t *testing.T) {
	rec := &recEvents{}

	resp := aeSignOut(t, answer(200, signOutGranted(signOutAccess(t, subjectS1))), answer(204, ""), rec)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.Code)
	}
	rec.requireNames(t, EventSignedOut)
	if e := rec.events()[0]; e.UserID != subjectS1 {
		t.Errorf("user id = %q, want %q", e.UserID, subjectS1)
	}
}

func TestAccountEvents_RefusedSignOutRecordsNothing(t *testing.T) {
	rec := &recEvents{}

	resp := aeSignOut(t, answer(401, `{"error_code":"refresh_token_not_found"}`), answer(204, ""), rec)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.Code)
	}
	rec.requireNames(t)
}

func aeRequestReset(t *testing.T, fake *fakeGoTrue, floor time.Duration, th *SignInThrottle, enforce bool, rec AccountEvents, email string) (*httptest.ResponseRecorder, time.Duration) {
	t.Helper()
	h := RequestPasswordResetHandler(fake.URL, testClient(), floor, th, enforce, nopLog(), rec)
	return serveReset(t.Context(), h, resendBody(email))
}

func TestAccountEvents_ResetRequestRecordsWhenAdmitted(t *testing.T) {
	rec := &recEvents{}

	resp, _ := aeRequestReset(t, newFakeGoTrue(t, 200, `{}`), 0, resendThrottle(time.Now), true, rec, aeEmail)

	requireAccepted(t, resp, "reset")
	rec.requireNames(t, EventPasswordResetRequested)
	if e := rec.events()[0]; e.Email != aeEmail || e.UserID != "" {
		t.Errorf("event = %+v, want the email and no user id", e)
	}
}

func TestAccountEvents_ResetRequestRecordsNothingWhenRefusedOrInvalid(t *testing.T) {
	t.Run("budget spent", func(t *testing.T) {
		th := resendThrottle(time.Now)
		for range ResendPerAddress {
			th.Admit(aeEmail)
		}
		rec := &recEvents{}

		resp, _ := aeRequestReset(t, newFakeGoTrue(t, 200, `{}`), 0, th, true, rec, aeEmail)

		requireAccepted(t, resp, "reset")
		rec.requireNames(t)
	})
	t.Run("validation_failed", func(t *testing.T) {
		rec := &recEvents{}

		resp, _ := aeRequestReset(t, newFakeGoTrue(t, 400, `{"error_code":"validation_failed","msg":"x"}`), 0, resendThrottle(time.Now), true, rec, aeEmail)

		if resp.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.Code)
		}
		rec.requireNames(t)
	})
}

// ResendVerificationHandler takes no recorder and mailLinkHandler gets a nil onAdmitted from it.
func TestAccountEvents_ResendVerificationNeverRecords(t *testing.T) {
	fake := newFakeGoTrue(t, 200, `{}`)
	h := mailLinkHandler("resend-verification", "resend", fake.URL.JoinPath("resend").String(),
		func(email string) map[string]string { return map[string]string{"email": email} },
		testClient(), 0, resendThrottle(time.Now), true, nopLog(), nil)

	resp, _ := serveResend(t.Context(), h, resendBody(aeEmail))

	requireAccepted(t, resp, "resend")
	if n := callsTo(fake, "/resend"); n != 1 {
		t.Errorf("GoTrue /resend calls = %d, want 1", n)
	}
}

func TestAccountEvents_ResetRequestTimingUnchanged(t *testing.T) {
	const floor = 50 * time.Millisecond
	for _, email := range []string{"member@corp.example", "nobody@corp.example"} {
		rec := &recEvents{delay: 10 * time.Millisecond}

		resp, took := aeRequestReset(t, newFakeGoTrue(t, 200, `{}`), floor, resendThrottle(time.Now), true, rec, email)

		requireAccepted(t, resp, "reset")
		if took < floor {
			t.Errorf("%s: answered after %v, want no earlier than %v", email, took, floor)
		}
		rec.requireNames(t, EventPasswordResetRequested)
	}
}

func aeResetHandler(t *testing.T, f *resetGoTrue, rec AccountEvents) http.Handler {
	t.Helper()
	th := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, newTestClock().Now)
	return ResetPasswordHandler(f.URL, siteURL(t), testClient(), liveSessions(t), th, nopLog(), rec)
}

func aeResetPost(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	return rpPostRaw(t.Context(), h, rpPath, rpFormType, rpValues(rpToken, "recovery", rpPass).Encode())
}

func TestAccountEvents_ResetCompletedRecordsUserAndEmail(t *testing.T) {
	rec := &recEvents{}

	resp := aeResetPost(t, aeResetHandler(t, newResetGoTrue(t), rec))

	rpRequireRedirect(t, resp, rpOK)
	rec.requireNames(t, EventPasswordResetCompleted)
	if e := rec.events()[0]; e.UserID != subjectS1 || e.Email != rpEmail {
		t.Errorf("event = %+v, want user %s and email %s", e, subjectS1, rpEmail)
	}
}

func TestAccountEvents_FailedResetAndInvitePasswordRecordNothing(t *testing.T) {
	t.Run("failed link", func(t *testing.T) {
		f := newResetGoTrue(t)
		f.verify = answer(403, rpOTPExpired)
		rec := &recEvents{}

		resp := aeResetPost(t, aeResetHandler(t, f, rec))

		rpRequireRedirect(t, resp, rpFailed)
		rec.requireNames(t)
	})
	t.Run("invitation set-password", func(t *testing.T) {
		rec := &recEvents{}
		h := aeInviteHandler(t, newResetGoTrue(t), rec)

		resp := ipPost(t, h, aeInviteValues())

		if !strings.Contains(resp.Header().Get("Location"), "&handoff=") {
			t.Fatalf("Location = %q, want a hand-off", resp.Header().Get("Location"))
		}
		for _, e := range rec.events() {
			if e.Event == EventPasswordResetCompleted {
				t.Errorf("invitation flow recorded %s", e.Event)
			}
		}
	})
}

func TestAccountEvents_SpentResetTokenRecordsOnce(t *testing.T) {
	f := newResetGoTrue(t)
	var verifies atomic.Int32
	ok := rpVerifyAnswer(subjectS1, rpEmail)
	f.verify = func(w http.ResponseWriter, r *http.Request) {
		if verifies.Add(1) == 1 {
			ok(w, r)
			return
		}
		answer(403, rpOTPExpired)(w, r)
	}
	rec := &recEvents{}
	h := aeResetHandler(t, f, rec)

	first, second := aeResetPost(t, h), aeResetPost(t, h)

	rpRequireRedirect(t, first, rpOK)
	rpRequireRedirect(t, second, rpFailed)
	rec.requireNames(t, EventPasswordResetCompleted)
}

func aeInviteValues() url.Values {
	v := rpValues(rpToken, "signup", rpPass)
	v.Set("state", vhState)
	return v
}

func aeInviteHandler(t *testing.T, f *resetGoTrue, rec AccountEvents) http.Handler {
	t.Helper()
	th := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, newTestClock().Now)
	return InvitationPasswordHandler(f.URL, siteURL(t), testClient(), liveSessions(t), th, nil, nopLog(), testHandoffStore(), rec)
}

func TestAccountEvents_VerifyHandoffRecordsASignIn(t *testing.T) {
	rec := &recEvents{}
	h := VerifyHandler(newFakeGoTrue(t, 200, gtSession).URL, siteURL(t), testClient(), nopLog(), nil, testHandoffStore(), rec)

	vhCode(t, vhClick(h, vhState))

	rec.requireNames(t, EventSignedIn)
	if e := rec.events()[0]; e.UserID != aeUserID || e.Email != aeEmail {
		t.Errorf("event = %+v, want user %s and email %s", e, aeUserID, aeEmail)
	}

	resp := vhClick(h, "")
	if loc := resp.Header().Get("Location"); strings.Contains(loc, "handoff=") {
		t.Fatalf("a verify with no state minted a code: %q", loc)
	}
	rec.requireNames(t, EventSignedIn)
}

func TestAccountEvents_InvitationPasswordRecordsASignInNotAReset(t *testing.T) {
	f := newResetGoTrue(t)
	rec := &recEvents{}

	resp := ipPost(t, aeInviteHandler(t, f, rec), aeInviteValues())

	if !strings.Contains(resp.Header().Get("Location"), "&handoff=") {
		t.Fatalf("Location = %q, want a hand-off", resp.Header().Get("Location"))
	}
	rec.requireNames(t, EventSignedIn)
	if e := rec.events()[0]; e.UserID != subjectS1 || e.Email != rpEmail {
		t.Errorf("event = %+v, want user %s and email %s", e, subjectS1, rpEmail)
	}

	f2 := newResetGoTrue(t)
	f2.token = answer(400, gtInvalidCredentials)
	rec2 := &recEvents{}
	ipPost(t, aeInviteHandler(t, f2, rec2), aeInviteValues())
	rec2.requireNames(t)
}

func TestAccountEvents_UserAgentIsCleanedAndCut(t *testing.T) {
	ua := strings.Repeat("a", 100) + "\xff" + strings.Repeat("é", 500)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header["User-Agent"] = []string{ua}
	rec := &recEvents{}

	recordAccountEvent(req, rec, nopLog(), AccountEvent{Event: EventSignedOut, UserID: aeUserID})

	got := rec.events()[0].UserAgent
	if n := len([]rune(got)); n != maxUserAgentRunes {
		t.Errorf("recorded user agent has %d runes, want %d", n, maxUserAgentRunes)
	}
	if strings.ContainsRune(got, '�') || strings.Contains(got, "\xff") {
		t.Error("recorded user agent keeps the invalid byte")
	}
}

func TestAccountEvents_RecorderFailureKeepsTheAnswer(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtSession)
	log, buf := captureLog()
	th := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, newTestClock().Now)
	h := SignInHandler(fake.URL, testClient(), testHandoffStore(), th, log, nil, &recEvents{err: context.DeadlineExceeded})

	resp := aeSignIn(t, h)

	requireOnlyKey(t, resp, "code")
	out := buf.String()
	if n := strings.Count(out, `"level":"ERROR"`); n != 1 {
		t.Fatalf("%d ERROR lines, want 1: %s", n, out)
	}
	if !strings.Contains(out, EventSignedIn) {
		t.Errorf("the ERROR line does not name %s: %s", EventSignedIn, out)
	}
	for _, secret := range []string{aeEmail, aeIP} {
		if strings.Contains(out, secret) {
			t.Errorf("log carries %q: %s", secret, out)
		}
	}
}

func TestAccountEvents_NilPoolLogsOnly(t *testing.T) {
	log, buf := captureLog()

	if err := NewAccountEvents(nil, log).Record(t.Context(), AccountEvent{Event: EventSignedIn, Email: aeEmail, IP: aeIP}); err != nil {
		t.Fatalf("Record = %v, want nil", err)
	}

	out := buf.String()
	if !strings.Contains(out, EventSignedIn) {
		t.Errorf("log does not name the event: %s", out)
	}
	for _, secret := range []string{aeEmail, aeIP} {
		if strings.Contains(out, secret) {
			t.Errorf("log carries %q: %s", secret, out)
		}
	}
}
