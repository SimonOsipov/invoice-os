package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

const (
	// Hosted: the load balancer appends <client>, <rule IP>; the client is second from the right.
	gaXFF  = "198.51.100.1, 203.0.113.7, 130.211.0.1"
	gaWant = "203.0.113.7"
)

// gaAddress is every X-User-Address value a fake GoTrue received.
func gaAddress(r *http.Request) []string { return r.Header.Values(platform.HeaderUserIP) }

// gaRequest is a POST with the given X-Forwarded-For; an empty xff sends none.
func gaRequest(target, contentType, body, xff string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	return req
}

type gaCall struct {
	Method, Path string
	Address      []string
}

func (c gaCall) String() string { return c.Method + " " + c.Path }

// gaGoTrue answers every GoTrue route a gateway handler calls with a success body.
// A test swaps routes["METHOD /path"] before the first request.
type gaGoTrue struct {
	URL    *url.URL
	mu     sync.Mutex
	calls  []gaCall
	routes map[string]http.HandlerFunc
}

func newGaGoTrue(t *testing.T) *gaGoTrue {
	t.Helper()
	session := `{"access_token":"` + rpAccess + `","refresh_token":"` + rpRefresh + `","user":{"id":"` + subjectS1 + `","email":"` + rpEmail + `"}}`
	f := &gaGoTrue{routes: map[string]http.HandlerFunc{
		"POST /verify":  answer(http.StatusOK, session),
		"PUT /user":     answer(http.StatusOK, `{}`),
		"GET /user":     answer(http.StatusOK, gtUser),
		"POST /logout":  answer(http.StatusNoContent, ""),
		"POST /token":   answer(http.StatusOK, session),
		"POST /signup":  answer(http.StatusOK, gtNewUser),
		"POST /recover": answer(http.StatusOK, `{}`),
		"POST /resend":  answer(http.StatusOK, `{}`),
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, gaCall{r.Method, r.URL.Path, gaAddress(r)})
		route := f.routes[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		if route == nil {
			http.NotFound(w, r)
			return
		}
		route(w, r)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse fake url: %v", err)
	}
	f.URL = u
	return f
}

func (f *gaGoTrue) Calls() []gaCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func gaNames(calls []gaCall) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c.String())
	}
	return out
}

// gaRequireAddress fails unless calls is non-empty and every call carries exactly one X-User-Address, want.
func gaRequireAddress(t *testing.T, calls []gaCall, want string) {
	t.Helper()
	if len(calls) == 0 {
		t.Fatal("GoTrue saw no call")
	}
	for _, c := range calls {
		if !slices.Equal(c.Address, []string{want}) {
			t.Errorf("%s carried %s = %q, want exactly [%q]", c, platform.HeaderUserIP, c.Address, want)
		}
	}
}

func TestGoTrueAddress_EveryGoTrueCallCarriesTheHeader(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "production")
	f := newGaGoTrue(t)
	log := nopLog()
	sessions := NewSessionChecker(f.URL, testClient(), time.Now, log)
	throttle := func() *SignInThrottle {
		return NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, time.Now)
	}
	site := siteURL(t)
	noPending := func(context.Context, string) (bool, error) { return false, nil }
	const jsonType = "application/json"
	withState := rpValues(rpToken, "signup", rpPass)
	withState.Set("state", vhState)

	rows := []struct {
		name              string
		h                 http.Handler
		target, ctype, in string
		want              []string
	}{
		{"confirm", VerifyHandler(f.URL, site, testClient(), log, nil, testHandoffStore(), nil, lrThrottle(time.Now), true), "/auth/verify", rpFormType, verifyQuery, []string{"POST /verify"}},
		{"reset", ResetPasswordHandler(f.URL, site, testClient(), sessions, throttle(), log, nil, lrThrottle(time.Now), true), rpPath, rpFormType, rpValues(rpToken, "recovery", rpPass).Encode(),
			[]string{"POST /verify", "PUT /user", "POST /logout"}},
		{"invitee set-password and hand-off", InvitationPasswordHandler(f.URL, site, testClient(), sessions, throttle(), nil, log, testHandoffStore(), nil, lrThrottle(time.Now), true), ipPath, rpFormType, withState.Encode(),
			[]string{"POST /verify", "PUT /user", "POST /logout", "POST /token"}},
		{"sign-in", SignInHandler(f.URL, testClient(), testHandoffStore(), throttle(), log, nil, nil), "/auth/sign-in", jsonType, signInBody(regEmail, regPassword, vhState), []string{"POST /token"}},
		{"refresh", RefreshHandler(f.URL, testClient(), log, nil), "/auth/refresh", jsonType, refreshBody(rpRefresh), []string{"POST /token"}},
		{"sign-out", SignOutHandler(f.URL, testClient(), sessions, log, nil, nil), "/auth/sign-out", jsonType, refreshBody(rpRefresh), []string{"POST /token", "POST /logout"}},
		{"register", RegisterHandler(f.URL, testClient(), 0, log, noPending, nil), "/auth/register", jsonType, `{"email":"` + regEmail + `","password":"` + regPassword + `"}`, []string{"POST /signup"}},
		{"invitation register", InvitationRegisterHandler(f.URL, testClient(), 0, log, invitePreviewing("none").preview, claiming(inviteAddress, true, nil).registrations(), nil),
			"/auth/invitation/register", jsonType, tokenBody(inviteToken), []string{"POST /signup"}},
		{"resend verification", ResendVerificationHandler(f.URL, testClient(), 0, resendThrottle(time.Now), true, log), "/auth/resend-verification", jsonType, resendBody(regEmail), []string{"POST /resend"}},
		{"request password reset", RequestPasswordResetHandler(f.URL, testClient(), 0, resendThrottle(time.Now), true, log, nil), "/auth/request-password-reset", jsonType, resendBody(regEmail), []string{"POST /recover"}},
		{"invitation resend", InvitationResendHandler(f.URL, testClient(), resendThrottle(time.Now), true, log, invitePreviewing("unconfirmed").preview), "/auth/invitation/resend", jsonType, tokenBody(inviteToken), []string{"POST /resend"}},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			before := len(f.Calls())
			c.h.ServeHTTP(httptest.NewRecorder(), gaRequest(c.target, c.ctype, c.in, gaXFF))
			got := f.Calls()[before:]
			if names := gaNames(got); !slices.Equal(names, c.want) {
				t.Fatalf("GoTrue calls = %v, want %v", names, c.want)
			}
			gaRequireAddress(t, got, gaWant)
		})
	}

	// The session check asks GoTrue as the same gateway but keys no limiter, so it sends none.
	t.Run("session check sends none", func(t *testing.T) {
		before := len(f.Calls())
		sessions.ask(t.Context(), "Bearer "+sessionAT)
		got := f.Calls()[before:]
		if len(got) != 1 || got[0].String() != "GET /user" {
			t.Fatalf("GoTrue calls = %v, want one GET /user", gaNames(got))
		}
		if len(got[0].Address) != 0 {
			t.Errorf("GET /user carried %s = %q, want none", platform.HeaderUserIP, got[0].Address)
		}
	})
}

// gaVerifyAddress posts the confirm form under env and returns the address GoTrue saw.
func gaVerifyAddress(t *testing.T, env, xff, remote string) []string {
	t.Helper()
	t.Setenv("DEPLOY_ENV", env)
	h, fake := vhVerifier(t, testHandoffStore(), http.StatusOK, gtSession)
	req := gaRequest("/auth/verify", rpFormType, verifyQuery, xff)
	if remote != "" {
		req.RemoteAddr = remote
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("GoTrue saw %d calls, want 1", len(calls))
	}
	return calls[0].Address
}

func TestGoTrueAddress_FollowsThePosture(t *testing.T) {
	rows := []struct{ name, env, remote, want string }{
		{"hosted: second from the right", "production", "", gaWant},
		{"preview: rightmost", "pr-1", "", "130.211.0.1"},
		{"nightly: rightmost", "nightly", "", "130.211.0.1"},
		{"local: the RemoteAddr host, never the header", "", "192.0.2.10:5555", "192.0.2.10"},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			got := gaVerifyAddress(t, c.env, gaXFF, c.remote)
			if !slices.Equal(got, []string{c.want}) {
				t.Errorf("%s = %q, want exactly [%q]", platform.HeaderUserIP, got, c.want)
			}
		})
	}
}

func TestGoTrueAddress_IPv6IsSentAsItsSlash64(t *testing.T) {
	got := gaVerifyAddress(t, "production", "2001:db8:1:2:3:4:5:6, 130.211.0.1", "")
	if want := []string{"2001:db8:1:2::/64"}; !slices.Equal(got, want) {
		t.Errorf("%s = %q, want %q", platform.HeaderUserIP, got, want)
	}
}

func TestGoTrueAddress_NoUsableAddressIsUnattributable(t *testing.T) {
	rows := []struct{ name, env, xff string }{
		{"hosted with one entry", "production", "203.0.113.7"},
		{"hosted with a private second from the right", "production", "198.51.100.1, 10.0.0.5, 130.211.0.1"},
		{"preview with no X-Forwarded-For", "pr-1", ""},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			got := gaVerifyAddress(t, c.env, c.xff, "")
			// The literal, not the constant: it is the value GoTrue keys on.
			if !slices.Equal(got, []string{"unattributable"}) {
				t.Errorf("%s = %q, want exactly [\"unattributable\"] and never an empty value", platform.HeaderUserIP, got)
			}
		})
	}
}

func TestGoTrueAddress_AClientCopyNeverReachesGoTrue(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "production")
	const forged = "192.0.2.99"
	f := newGaGoTrue(t)
	th := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, time.Now)
	sessions := NewSessionChecker(f.URL, testClient(), time.Now, nopLog())
	rows := []struct {
		name, ctype, body, target string
		h                         http.Handler
	}{
		{"sign-in", "application/json", signInBody(regEmail, regPassword, vhState), "/auth/sign-in", SignInHandler(f.URL, testClient(), testHandoffStore(), th, nopLog(), nil, nil)},
		{"reset", rpFormType, rpValues(rpToken, "recovery", rpPass).Encode(), rpPath, ResetPasswordHandler(f.URL, siteURL(t), testClient(), sessions, th, nopLog(), nil, lrThrottle(time.Now), true)},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			before := len(f.Calls())
			req := gaRequest(c.target, c.ctype, c.body, gaXFF)
			req.Header.Add(platform.HeaderUserIP, forged)
			req.Header.Add(platform.HeaderUserIP, forged)
			c.h.ServeHTTP(httptest.NewRecorder(), req)
			got := f.Calls()[before:]
			gaRequireAddress(t, got, gaWant)
			for _, call := range got {
				if slices.Contains(call.Address, forged) {
					t.Errorf("%s forwarded the client's %s", call, platform.HeaderUserIP)
				}
			}
		})
	}
}

func TestGoTrueAddress_NoLogHoldsTheAddress(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "production")
	rows := []struct {
		name, handler string
		route         http.HandlerFunc
		warn          string
	}{
		{"confirm: link refused", "verify", answer(http.StatusForbidden, gtOTPExpired), "verify: gotrue refused the link"},
		{"confirm: 502", "verify", answer(http.StatusBadGateway, gtInternal), "verify: gotrue refused the link"},
		{"confirm: outage", "verify", dropped, "verify: gotrue unreachable"},
		{"reset: link refused", "reset", answer(http.StatusForbidden, rpOTPExpired), rpMsgRefusedLink},
		{"reset: 502", "reset", answer(http.StatusBadGateway, gtInternal), rpMsgRefusedLink},
		{"reset: outage", "reset", dropped, rpMsgUnreachable},
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			f := newGaGoTrue(t)
			f.routes["POST /verify"] = c.route
			log, buf := captureLog()
			var h http.Handler
			var req *http.Request
			if c.handler == "verify" {
				h = VerifyHandler(f.URL, siteURL(t), testClient(), log, nil, testHandoffStore(), nil, lrThrottle(time.Now), true)
				req = gaRequest("/auth/verify", rpFormType, verifyQuery, gaXFF)
			} else {
				th := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, time.Now)
				h = ResetPasswordHandler(f.URL, siteURL(t), testClient(), NewSessionChecker(f.URL, testClient(), time.Now, log), th, log, nil, lrThrottle(time.Now), true)
				req = gaRequest(rpPath, rpFormType, rpValues(rpToken, "recovery", rpPass).Encode(), gaXFF)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)

			// The address was in play, and the warn is there; only then does its absence from the log mean something.
			gaRequireAddress(t, f.Calls(), gaWant)
			if n := len(recordsNamed(t, buf, c.warn)); n != 1 {
				t.Fatalf("want one %q line, got %d: %s", c.warn, n, buf.String())
			}
			if strings.Contains(buf.String(), gaWant) {
				t.Errorf("a log line holds the address: %s", buf.String())
			}
		})
	}
}
