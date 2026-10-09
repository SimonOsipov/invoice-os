package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

const (
	ipPath   = "/auth/invitation/password"
	ipOK     = "https://site.example/?verified=1"
	ipFailed = "https://site.example/?verify=failed"
)

// newInviteRig is resetRig's priming (cached S1 verdict, exhausted sign-in failures) around the invitee handler.
func newInviteRig(t *testing.T, sink ContactSink) *resetRig {
	t.Helper()
	r := newResetRig(t)
	log := slog.New(slog.NewJSONHandler(r.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r.handler = InvitationPasswordHandler(r.gotrue.URL, siteURL(t), testClient(), r.sessions, r.throttle, sink, log)
	return r
}

func newInviteHandler(t *testing.T, f *resetGoTrue, log *slog.Logger, sink ContactSink) http.Handler {
	t.Helper()
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	th := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, newTestClock().Now)
	return InvitationPasswordHandler(f.URL, siteURL(t), testClient(), liveSessions(t), th, sink, log)
}

func ipPost(t *testing.T, h http.Handler, v url.Values) *httptest.ResponseRecorder {
	t.Helper()
	return rpPostRaw(t.Context(), h, ipPath, rpFormType, v.Encode())
}

func TestInvitationPassword_VerifiesSetsThePasswordThenSignsOutEverywhere(t *testing.T) {
	f := newResetGoTrue(t)

	rec := ipPost(t, newInviteHandler(t, f, nil, nil), rpValues(rpToken, "signup", rpPass))

	rpRequireRedirect(t, rec, ipOK)
	want := []string{"POST /verify", "PUT /user", "POST /logout"}
	if got := f.names(); !slices.Equal(got, want) {
		t.Fatalf("GoTrue calls = %v, want %v in that order", got, want)
	}
	calls := f.Calls()
	if got, want := rpJSON(t, calls[0].Body), map[string]any{"type": "signup", "token_hash": rpToken}; !maps.Equal(got, want) {
		t.Errorf("verify body = %v, want exactly %v", got, want)
	}
	if got, want := rpJSON(t, calls[1].Body), map[string]any{"password": rpPass}; !maps.Equal(got, want) {
		t.Errorf("PUT /user body = %v, want exactly %v", got, want)
	}
	if calls[1].Auth != "Bearer "+rpAccess || calls[2].Auth != "Bearer "+rpAccess {
		t.Errorf("bearers: PUT %q, logout %q; want the verify session for both", calls[1].Auth, calls[2].Auth)
	}
	if calls[2].RawQuery != "scope=global" {
		t.Errorf("logout query = %q, want scope=global", calls[2].RawQuery)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestInvitationPassword_EvictsTheSubjectAndClearsSignInFailures(t *testing.T) {
	rg := newInviteRig(t, nil)

	rpRequireRedirect(t, ipPost(t, rg.handler, rpValues(rpToken, "signup", rpPass)), ipOK)

	if !rg.evicted() {
		t.Error("the subject's cached verdicts survive: the next /api/ check did not ask GoTrue")
	}
	if !rg.throttleReset() {
		t.Error("the address is still refused after the password was set")
	}

	ctl := newInviteRig(t, nil)
	ctl.gotrue.verify = answer(http.StatusForbidden, rpOTPExpired)
	rpRequireRedirect(t, ipPost(t, ctl.handler, rpValues(rpToken, "signup", rpPass)), ipFailed)
	if n := ctl.gotrue.count(http.MethodPost, "/verify"); n != 1 {
		t.Fatalf("control: verify calls = %d, want 1", n)
	}
	if ctl.evicted() {
		t.Error("control: a refused link evicted the subject")
	}
	if ctl.throttleReset() {
		t.Error("control: a refused link cleared the address's failures")
	}
}

func TestInvitationPassword_HandsTheConfirmedUserToTheContactSink(t *testing.T) {
	sink := newRecSink(nil)

	rpRequireRedirect(t, ipPost(t, newInviteHandler(t, newResetGoTrue(t), nil, sink), rpValues(rpToken, "signup", rpPass)), ipOK)

	requireOneHandOff(t, sink, RegistrantContact{UserID: subjectS1, Email: rpEmail})
}

func TestInvitationPassword_PasswordOutsideTheBoundsRerendersThePage(t *testing.T) {
	for name, pw := range map[string]string{"5 bytes": "abcde", "73 bytes": strings.Repeat("a", 73)} {
		t.Run(name, func(t *testing.T) {
			f := newResetGoTrue(t)

			rec := ipPost(t, newInviteHandler(t, f, nil, nil), rpValues(rpToken, "signup", pw))

			if rec.Code != http.StatusBadRequest || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
				t.Fatalf("answer = %d %q, want 400 text/html", rec.Code, rec.Header().Get("Content-Type"))
			}
			if !strings.Contains(rec.Body.String(), rpAlert) {
				t.Errorf("the page lacks the hint %q: %q", rpAlert, rec.Body.String())
			}
			if got := rpTokenInput(t, rec.Body.String()); got != rpToken {
				t.Errorf("hidden token = %q, want the posted %q", got, rpToken)
			}
			forms := vpFind(vpParse(t, rec.Body.String()), vpTag("form"))
			if len(forms) != 1 {
				t.Fatalf("forms = %d, want 1", len(forms))
			}
			if got, _ := vpAttr(forms[0], "action"); got != ipPath {
				t.Errorf("form action = %q, want %s", got, ipPath)
			}
			if got := f.names(); len(got) != 0 {
				t.Errorf("GoTrue calls = %v, want none", got)
			}
		})
	}
	for name, pw := range map[string]string{"6 bytes": "abcdef", "72 bytes": strings.Repeat("a", 72)} {
		t.Run("control "+name, func(t *testing.T) {
			f := newResetGoTrue(t)
			rpRequireRedirect(t, ipPost(t, newInviteHandler(t, f, nil, nil), rpValues(rpToken, "signup", pw)), ipOK)
			if f.count(http.MethodPut, "/user") != 1 {
				t.Errorf("GoTrue calls = %v, want one PUT /user", f.names())
			}
		})
	}
}

func TestInvitationPassword_RefusedLinkOrPasswordIsTheFailedNotice(t *testing.T) {
	rows := []struct {
		name         string
		verify, user http.HandlerFunc
		noPut        bool
	}{
		{"verify 403 otp_expired", answer(http.StatusForbidden, rpOTPExpired), nil, true},
		{"verify 200 without access_token", answer(http.StatusOK, `{"user":{"id":"`+subjectS1+`","email":"`+rpEmail+`"}}`), nil, true},
		{"verify 202 with a complete session", answer(http.StatusAccepted, `{"access_token":"`+rpAccess+`","user":{"id":"`+subjectS1+`","email":"`+rpEmail+`"}}`), nil, true},
		{"PUT 422 weak_password", nil, answer(http.StatusUnprocessableEntity, rpWeakPassword), false},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			f := newResetGoTrue(t)
			if c.verify != nil {
				f.verify = c.verify
			}
			if c.user != nil {
				f.user = c.user
			}

			rec := ipPost(t, newInviteHandler(t, f, nil, nil), rpValues(rpToken, "signup", rpPass))

			rpRequireRedirect(t, rec, ipFailed)
			if f.count(http.MethodPost, "/verify") != 1 {
				t.Errorf("verify calls = %d, want 1", f.count(http.MethodPost, "/verify"))
			}
			if c.noPut && f.count(http.MethodPut, "/user") != 0 {
				t.Errorf("PUT /user was sent after a refused link: %v", f.names())
			}
			if !c.noPut && f.count(http.MethodPut, "/user") != 1 {
				t.Errorf("PUT /user calls = %d, want 1", f.count(http.MethodPut, "/user"))
			}
			if n := f.count(http.MethodPost, "/logout"); n != 0 {
				t.Errorf("sign-out calls = %d, want 0", n)
			}
		})
	}
	t.Run("control: the same form answered by a healthy GoTrue", func(t *testing.T) {
		rpRequireRedirect(t, ipPost(t, newInviteHandler(t, newResetGoTrue(t), nil, nil), rpValues(rpToken, "signup", rpPass)), ipOK)
	})
}

func TestInvitationPassword_BadFormsAreTheFailedNoticeWithoutACall(t *testing.T) {
	good := rpValues(rpToken, "signup", "newpass1").Encode()
	big := good + "&pad=" + strings.Repeat("a", 3<<10)
	rows := []struct{ name, contentType, body string }{
		{"missing token", rpFormType, url.Values{"type": {"signup"}, "password": {"newpass1"}}.Encode()},
		{"257-byte token", rpFormType, rpValues(strings.Repeat("a", 257), "signup", "newpass1").Encode()},
		{"type recovery", rpFormType, rpValues(rpToken, "recovery", "newpass1").Encode()},
		{"3 KiB body", rpFormType, big},
	}
	if len(big) < 3<<10 {
		t.Fatalf("test body is %d bytes, want at least 3 KiB", len(big))
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			f := newResetGoTrue(t)

			rec := rpPostRaw(t.Context(), newInviteHandler(t, f, nil, nil), ipPath, c.contentType, c.body)

			rpRequireRedirect(t, rec, ipFailed)
			if got := f.names(); len(got) != 0 {
				t.Errorf("GoTrue calls = %v, want none", got)
			}
		})
	}
	t.Run("control: the same form without the defect", func(t *testing.T) {
		f := newResetGoTrue(t)
		rpRequireRedirect(t, rpPostRaw(t.Context(), newInviteHandler(t, f, nil, nil), ipPath, rpFormType, good), ipOK)
		if f.count(http.MethodPost, "/verify") != 1 {
			t.Errorf("GoTrue calls = %v, want one POST /verify", f.names())
		}
	})
}

func TestInvitationPassword_NonPostIs405AndUnconfiguredIs503(t *testing.T) {
	f := newResetGoTrue(t)
	h := newInviteHandler(t, f, nil, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ipPath+"?"+rpQuery(rpToken), nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
		t.Errorf("GET: status %d Allow %q, want 405 POST", rec.Code, rec.Header().Get("Allow"))
	}
	if got := f.names(); len(got) != 0 {
		t.Errorf("GoTrue calls = %v, want none", got)
	}
	rpRequireRedirect(t, ipPost(t, h, rpValues(rpToken, "signup", rpPass)), ipOK)

	th := NewSignInThrottle("sign-in", 10, 10, SignInWindow, newTestClock().Now)
	log := slog.New(slog.DiscardHandler)
	for name, h := range map[string]http.Handler{
		"nil authURL": InvitationPasswordHandler(nil, siteURL(t), testClient(), liveSessions(t), th, nil, log),
		"nil siteURL": InvitationPasswordHandler(f.URL, nil, testClient(), liveSessions(t), th, nil, log),
	} {
		rec := ipPost(t, h, rpValues(rpToken, "signup", rpPass))
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "registration is not configured") {
			t.Errorf("%s: answer = %d %q, want 503 registration is not configured", name, rec.Code, rec.Body.String())
		}
	}
}

func TestInvitationPassword_LogsCarryNoTokenPasswordOrAddress(t *testing.T) {
	const (
		token = "leak-token-9f3a7c"
		pass  = "leak-pass-7c1d4e"
		mail  = "leak-probe@corp.example"
	)
	// GoTrue bodies echo the secrets, so a handler that logs a body or a message leaks them.
	echo := func(status int, code string) http.HandlerFunc {
		return answer(status, `{"code":`+strconv.Itoa(status)+`,"error_code":"`+code+`","msg":"`+pass+` `+mail+` `+token+`"}`)
	}
	rows := []struct {
		name                 string
		verify, user, logout http.HandlerFunc
		want                 string
		warn                 bool
	}{
		{name: "success", want: ipOK},
		{name: "PUT 200, sign-out 500", logout: echo(500, "unexpected_failure"), want: ipOK, warn: true},
		{name: "verify 403", verify: echo(403, "otp_expired"), want: ipFailed, warn: true},
		{name: "verify unreachable", verify: dropped, want: ipFailed, warn: true},
		{name: "verify incomplete", verify: answer(200, `{"access_token":"","user":{"id":"u1","email":"`+mail+`"}}`), want: ipFailed, warn: true},
		{name: "verify 302", verify: redirectTo("/user"), want: ipFailed, warn: true},
		{name: "verify 200 not JSON", verify: answer(200, "<html>"+mail+" "+pass+" "+token+"</html>"), want: ipFailed, warn: true},
		{name: "PUT 422 weak_password", user: echo(422, "weak_password"), want: ipFailed, warn: true},
		{name: "PUT 500", user: echo(500, "unexpected_failure"), want: ipFailed, warn: true},
		{name: "PUT unreachable", user: dropped, want: ipFailed, warn: true},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			f := newResetGoTrue(t)
			f.verify = rpVerifyAnswer("u1", mail)
			for _, set := range []struct {
				dst *http.HandlerFunc
				src http.HandlerFunc
			}{{&f.verify, c.verify}, {&f.user, c.user}, {&f.logout, c.logout}} {
				if set.src != nil {
					*set.dst = set.src
				}
			}
			log, buf := captureLog()

			rec := ipPost(t, newInviteHandler(t, f, log, nil), rpValues(token, "signup", pass))

			rpRequireRedirect(t, rec, c.want)
			if c.warn && warnCount(t, buf) == 0 {
				t.Fatalf("the outcome logged no WARN, so the log check proves nothing")
			}
			for _, secret := range []string{token, pass, mail} {
				if strings.Contains(buf.String(), secret) {
					t.Errorf("the log holds %q: %s", secret, buf.String())
				}
			}
		})
	}
}

// ipOffline answers GoTrue's three routes without a network; an override with status 0 is a refused connection.
func ipOffline(override map[string]ipAnswer) *http.Client {
	routes := map[string]ipAnswer{
		"verify": {200, `{"access_token":"` + rpAccess + `","user":{"id":"` + subjectS1 + `","email":"` + rpEmail + `"}}`},
		"user":   {200, `{}`},
		"logout": {204, ""},
	}
	maps.Copy(routes, override)
	return &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		a := routes[path.Base(r.URL.Path)]
		if a.status == 0 {
			return nil, errors.New("connection refused")
		}
		return &http.Response{StatusCode: a.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(a.body)), Request: r}, nil
	})}
}

type ipAnswer struct {
	status int
	body   string
}

// ipOfflineHandler is the invitee handler over an in-process GoTrue, safe inside a synctest bubble.
func ipOfflineHandler(t *testing.T, client *http.Client, sink ContactSink, log *slog.Logger) http.Handler {
	t.Helper()
	th := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, time.Now)
	sessions := NewSessionChecker(offlineAuth, offlineClient(http.StatusOK, `{}`), time.Now, log)
	return InvitationPasswordHandler(offlineAuth, siteURL(t), client, sessions, th, sink, log)
}

func TestInvitationPassword_OnlyAConfirmedPasswordedUserReachesTheContactSink(t *testing.T) {
	weak := ipAnswer{http.StatusUnprocessableEntity, rpWeakPassword}
	rows := []struct {
		name     string
		override map[string]ipAnswer
		wantPut  bool
	}{
		{"verify 403 otp_expired", map[string]ipAnswer{"verify": {http.StatusForbidden, rpOTPExpired}}, false},
		{"verify unreachable", map[string]ipAnswer{"verify": {}}, false},
		{"verify without access_token", map[string]ipAnswer{"verify": {200, `{"user":{"id":"` + subjectS1 + `","email":"` + rpEmail + `"}}`}}, false},
		{"verify without user id", map[string]ipAnswer{"verify": {200, `{"access_token":"` + rpAccess + `","user":{"email":"` + rpEmail + `"}}`}}, false},
		{"verify without email", map[string]ipAnswer{"verify": {200, `{"access_token":"` + rpAccess + `","user":{"id":"` + subjectS1 + `"}}`}}, false},
		{"PUT 422 weak_password", map[string]ipAnswer{"user": weak}, true},
		{"PUT unreachable", map[string]ipAnswer{"user": {}}, true},
		{"same_password with a failed sign-out", map[string]ipAnswer{"user": {http.StatusUnprocessableEntity, rpSamePassword}, "logout": {http.StatusInternalServerError, "{}"}}, true},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sink := newRecSink(nil)
				puts := 0
				client := ipOffline(c.override)
				base := client.Transport
				client.Transport = rtFunc(func(r *http.Request) (*http.Response, error) {
					if r.Method == http.MethodPut {
						puts++
					}
					return base.RoundTrip(r)
				})

				rec := ipPost(t, ipOfflineHandler(t, client, sink, slog.New(slog.DiscardHandler)), rpValues(rpToken, "signup", rpPass))
				synctest.Wait()
				time.Sleep(time.Minute)
				synctest.Wait()

				rpRequireRedirect(t, rec, ipFailed)
				if (puts == 1) != c.wantPut {
					t.Fatalf("PUT /user calls = %d, want a call: %v; the row does not reach the stage it names", puts, c.wantPut)
				}
				if got := sink.got(); len(got) != 0 {
					t.Errorf("the contact sink saw %+v, want no call for a link or password GoTrue refused", got)
				}
			})
		})
	}
	t.Run("control: a confirmed user with a set password is handed off once", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			sink := newRecSink(nil)

			rec := ipPost(t, ipOfflineHandler(t, ipOffline(nil), sink, slog.New(slog.DiscardHandler)), rpValues(rpToken, "signup", rpPass))
			synctest.Wait()

			rpRequireRedirect(t, rec, ipOK)
			if got := sink.got(); len(got) != 1 || got[0].UserID != subjectS1 || got[0].Email != rpEmail {
				t.Errorf("sink calls = %+v, want one for %s", got, subjectS1)
			}
		})
	})
}

func TestInvitationPassword_HandOffFailureLogsNoTokenPasswordOrAddress(t *testing.T) {
	const (
		token = "leak-token-9f3a7c"
		pass  = "leak-pass-7c1d4e"
		mail  = "leak-probe@corp.example"
	)
	synctest.Test(t, func(t *testing.T) {
		down := func(context.Context, int, RegistrantContact) error {
			return errors.New("notifications refused " + mail + " " + pass + " " + token)
		}
		sink := newRecSink(down)
		client := ipOffline(map[string]ipAnswer{"verify": {200, `{"access_token":"` + rpAccess + `","user":{"id":"` + subjectS1 + `","email":"` + mail + `"}}`}})
		log, buf := captureLog()

		rec := ipPost(t, ipOfflineHandler(t, client, sink, log), rpValues(token, "signup", pass))
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()

		rpRequireRedirect(t, rec, ipOK)
		if n := len(sink.got()); n != len(handOffDelays)+1 {
			t.Fatalf("sink calls = %d, want %d attempts, so the failure WARN is reached", n, len(handOffDelays)+1)
		}
		if warnCount(t, buf) != 1 {
			t.Fatalf("WARN lines = %d, want the one hand-off failure: %s", warnCount(t, buf), buf.String())
		}
		for _, secret := range []string{token, pass, mail} {
			if strings.Contains(buf.String(), secret) {
				t.Errorf("the log holds %q: %s", secret, buf.String())
			}
		}
	})
}

func TestInvitationPassword_OnlyTheFirstOfADuplicatedFieldIsUsed(t *testing.T) {
	t.Run("two tokens verify the first only", func(t *testing.T) {
		f := newResetGoTrue(t)
		body := url.Values{"token": {rpToken, "other-token"}, "type": {"signup"}, "password": {rpPass}}.Encode()

		rpRequireRedirect(t, rpPostRaw(t.Context(), newInviteHandler(t, f, nil, nil), ipPath, rpFormType, body), ipOK)

		if f.count(http.MethodPost, "/verify") != 1 {
			t.Fatalf("GoTrue calls = %v, want one POST /verify", f.names())
		}
		if got := rpJSON(t, f.Calls()[0].Body)["token_hash"]; got != rpToken {
			t.Errorf("verified token = %v, want the first %q", got, rpToken)
		}
	})
	t.Run("a recovery type ahead of signup is refused without a call", func(t *testing.T) {
		f := newResetGoTrue(t)
		body := url.Values{"token": {rpToken}, "type": {"recovery", "signup"}, "password": {rpPass}}.Encode()

		rpRequireRedirect(t, rpPostRaw(t.Context(), newInviteHandler(t, f, nil, nil), ipPath, rpFormType, body), ipFailed)

		if got := f.names(); len(got) != 0 {
			t.Errorf("GoTrue calls = %v, want none", got)
		}
	})
}

func TestInvitationPassword_EveryMethodButPostIs405WithoutACall(t *testing.T) {
	for _, m := range []string{http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		t.Run(m, func(t *testing.T) {
			f := newResetGoTrue(t)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(m, ipPath, strings.NewReader(rpValues(rpToken, "signup", rpPass).Encode()))
			req.Header.Set("Content-Type", rpFormType)

			newInviteHandler(t, f, nil, nil).ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
				t.Errorf("status %d Allow %q, want 405 POST", rec.Code, rec.Header().Get("Allow"))
			}
			if rec.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Errorf("the route answered a CORS grant: %v", rec.Header())
			}
			if got := f.names(); len(got) != 0 {
				t.Errorf("GoTrue calls = %v, want none", got)
			}
		})
	}
}
