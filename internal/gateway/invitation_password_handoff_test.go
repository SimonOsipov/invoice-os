package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ihRig is the invitee handler over a fake GoTrue.
type ihRig struct {
	f   *resetGoTrue
	h   http.Handler
	log *bytes.Buffer
}

// newConfirmRig is the handler over a fake GoTrue.
func newConfirmRig(t *testing.T) *ihRig { return newIHRigSink(t, nil) }

// ihWantConfirmOnly requires 303 <site>/?verified=1 exactly and the three pre-existing GoTrue calls.
func ihWantConfirmOnly(t *testing.T, g *ihRig, rec *httptest.ResponseRecorder) {
	t.Helper()
	rpRequireRedirect(t, rec, ipOK)
	if want := []string{"POST /verify", "PUT /user", "POST /logout"}; !slices.Equal(g.f.names(), want) {
		t.Errorf("GoTrue calls = %v, want %v and no POST /token", g.f.names(), want)
	}
}

// newIHRigSink is newConfirmRig for a registrant: /verify confirms the coMetaFull user and the sink hears the hand-off.
func newIHRigSink(t *testing.T, sink ContactSink) *ihRig {
	t.Helper()
	f := newResetGoTrue(t)
	if sink != nil {
		f.verify = answer(http.StatusOK, coSession(coUser(coMetaFull)))
	}
	log, buf := captureLog()
	th := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, newTestClock().Now)
	h := InvitationPasswordHandler(f.URL, siteURL(t), testClient(), liveSessions(t), th, sink, log, lrThrottle(time.Now), true, nil)
	return &ihRig{f: f, h: h, log: buf}
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

func TestInvitationPassword_AStatedSuccessAnswersVerifiedWithNoCode(t *testing.T) {
	g := newConfirmRig(t)

	ihWantConfirmOnly(t, g, g.submit(t, vhState))
}

func TestInvitationPassword_ConcurrentSubmitsMakeNoGrant(t *testing.T) {
	g := newConfirmRig(t)
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

	recs := make([]*httptest.ResponseRecorder, 8)
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

	confirmed := 0
	for _, rec := range recs {
		switch loc := rec.Header().Get("Location"); loc {
		case ipOK:
			confirmed++
		case ipFailed:
		default:
			t.Errorf("Location %q, want %s or %s", loc, ipOK, ipFailed)
		}
	}
	if confirmed != 1 {
		t.Errorf("%d submits confirmed, want exactly 1", confirmed)
	}
	if n := g.f.count(http.MethodPost, "/token"); n != 0 {
		t.Errorf("POST /token calls = %d, want 0", n)
	}
}

func TestInvitationPassword_NoGrantAfterTheGlobalSignOut(t *testing.T) {
	g := newConfirmRig(t)

	rpRequireRedirect(t, g.submit(t, vhState), ipOK)

	names := g.f.names()
	if len(names) == 0 || names[len(names)-1] != "POST /logout" {
		t.Fatalf("GoTrue calls = %v, want the last to be POST /logout", names)
	}
	calls := g.f.Calls()
	if got := calls[len(calls)-1].RawQuery; got != "scope=global" {
		t.Errorf("last call query = %q, want scope=global", got)
	}
}

func TestInvitationPassword_SamePasswordWithASignOutStillConfirms(t *testing.T) {
	g := newConfirmRig(t)
	g.f.user = answer(http.StatusUnprocessableEntity, rpSamePassword)

	ihWantConfirmOnly(t, g, g.submit(t, vhState))
}

func TestInvitationPassword_NoOrBadStateConfirmsWithoutACodeAndMakesNoGrant(t *testing.T) {
	t.Run("control: a valid state confirms without a code", func(t *testing.T) {
		g := newConfirmRig(t)
		ihWantConfirmOnly(t, g, g.submit(t, vhState))
	})

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
			g := newConfirmRig(t)
			v := rpValues(rpToken, "signup", rpPass)
			v["state"] = c.state

			ihWantConfirmOnly(t, g, ipPost(t, g.h, v))
		})
	}
}

func TestInvitationPassword_FailedSignOutFallsBackToVerifiedAndMakesNoGrant(t *testing.T) {
	t.Run("control: a valid state confirms without a code", func(t *testing.T) {
		g := newConfirmRig(t)
		ihWantConfirmOnly(t, g, g.submit(t, vhState))
	})

	g := newConfirmRig(t)
	g.f.logout = answer(http.StatusInternalServerError, `{}`)

	ihWantConfirmOnly(t, g, g.submit(t, vhState))
	rpRequireWarn(t, g.log, "invitation-password: global sign-out failed", map[string]any{"upstream_status": float64(http.StatusInternalServerError)})
}

func TestInvitationPassword_GoneSignOutConfirmsWithoutACodeAndMakesNoGrant(t *testing.T) {
	t.Run("control: a valid state confirms without a code", func(t *testing.T) {
		g := newConfirmRig(t)
		ihWantConfirmOnly(t, g, g.submit(t, vhState))
	})

	if len(goneCodes) == 0 {
		t.Fatal("goneCodes is empty, so the loop below proves nothing")
	}
	for code := range goneCodes {
		t.Run(code, func(t *testing.T) {
			g := newConfirmRig(t)
			g.f.logout = answer(http.StatusUnauthorized, `{"error_code":"`+code+`"}`)

			ihWantConfirmOnly(t, g, g.submit(t, vhState))
		})
	}
}

func TestInvitationPassword_RefusalsMintNoGrantAndNoCode(t *testing.T) {
	t.Run("control: a valid state confirms without a code", func(t *testing.T) {
		g := newConfirmRig(t)
		ihWantConfirmOnly(t, g, g.submit(t, vhState))
	})

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
			g := newConfirmRig(t)
			c.set(g.f)

			rpRequireRedirect(t, g.submit(t, vhState), ipFailed)

			if got := g.f.names(); !slices.Equal(got, c.calls) {
				t.Errorf("GoTrue calls = %v, want %v and no POST /token", got, c.calls)
			}
		})
	}
}

func TestInvitationPassword_HandsOffTheRegistrantOnEveryConfirm(t *testing.T) {
	for name, state := range map[string]string{"stated": vhState, "stateless": ""} {
		t.Run(name, func(t *testing.T) {
			sink := newRecSink(nil)
			g := newIHRigSink(t, sink)

			rpRequireRedirect(t, g.submit(t, state), ipOK)

			requireOneHandOff(t, sink, coWant)
		})
	}
}

// A plain link and an invite link render the same form; posting either answers the same.
func TestInvitationPassword_InviteAndPlainAnswerTheSame(t *testing.T) {
	page := vpHandler(t)
	for name, query := range map[string]string{"plain": vpQuery(vpToken), "invite": vpInviteQuery(vpToken)} {
		t.Run(name, func(t *testing.T) {
			doc := vpParse(t, vpDo(t, page, http.MethodGet, query).Body.String())
			v := rpValues(vpInputValue(doc, "token"), vpInputValue(doc, "type"), rpPass)
			v.Set("state", vpInputValue(doc, "state"))
			g := newConfirmRig(t)

			ihWantConfirmOnly(t, g, ipPost(t, g.h, v))
		})
	}
}
