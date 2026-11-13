package gateway

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// One shared per-IP count serves the confirm, reset and invitee set-password POSTs.
// Tests run hosted (DEPLOY_ENV=production), so gaXFF keys the count under gaWant.

const (
	lrVerifyFailed = siteURLValue + "/?verify=failed"
	lrResetFailed  = siteURLValue + "/?reset=failed"
)

type lrRoute struct {
	name, label, failed, target, typ string
	body                             func(token, typ, password string) string
	build                            func(t *testing.T, f *gaGoTrue, th *SignInThrottle, enforce bool, log *slog.Logger) http.Handler
}

func lrPasswordBody(token, typ, password string) string {
	return url.Values{"token": {token}, "type": {typ}, "password": {password}}.Encode()
}

// lrRoutes is V, R and I of the "Link-redeem count states" table.
func lrRoutes() []lrRoute {
	signIn := func() *SignInThrottle {
		return NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, time.Now)
	}
	return []lrRoute{
		{"verify", "verify", lrVerifyFailed, "/auth/verify", "signup",
			func(token, typ, _ string) string { return "token=" + token + "&type=" + typ },
			func(t *testing.T, f *gaGoTrue, th *SignInThrottle, enforce bool, log *slog.Logger) http.Handler {
				return VerifyHandler(f.URL, siteURL(t), testClient(), log, nil, testHandoffStore(), nil, th, enforce)
			}},
		{"reset", "reset-password", lrResetFailed, rpPath, "recovery", lrPasswordBody,
			func(t *testing.T, f *gaGoTrue, th *SignInThrottle, enforce bool, log *slog.Logger) http.Handler {
				sessions := NewSessionChecker(f.URL, testClient(), time.Now, log)
				return ResetPasswordHandler(f.URL, siteURL(t), testClient(), sessions, signIn(), log, nil, th, enforce)
			}},
		{"invitation", "invitation-password", lrVerifyFailed, ipPath, "signup", lrPasswordBody,
			func(t *testing.T, f *gaGoTrue, th *SignInThrottle, enforce bool, log *slog.Logger) http.Handler {
				sessions := NewSessionChecker(f.URL, testClient(), time.Now, log)
				return InvitationPasswordHandler(f.URL, siteURL(t), testClient(), sessions, signIn(), nil, log, testHandoffStore(), nil, th, enforce)
			}},
	}
}

// lrThrottle is the production-shaped link-redeem throttle, in-process.
func lrThrottle(now func() time.Time) *SignInThrottle {
	return NewSignInThrottle("link-redeem-ip", LinkRedeemPerIP, LinkRedeemMaxKeys, LinkRedeemIPWindow, now)
}

// lrToken is a distinct bogus 56-hex token.
func lrToken(i int) string { return fmt.Sprintf("%056x", i) }

func lrXFF(client string) string { return "198.51.100.1, " + client + ", 130.211.0.1" }

func (rt lrRoute) post(h http.Handler, token, xff string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, gaRequest(rt.target, rpFormType, rt.body(token, rt.typ, rpPass), xff))
	return rec
}

func lrVerifyCalls(f *gaGoTrue) int {
	n := 0
	for _, c := range f.Calls() {
		if c.String() == "POST /verify" {
			n++
		}
	}
	return n
}

func lrRequireSeeOther(t *testing.T, what string, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if got := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || got != want {
		t.Fatalf("%s = %d Location %q, want 303 %q", what, rec.Code, got, want)
	}
}

// lrFill counts the key to the limit with direct Admit calls.
func lrFill(t *testing.T, th *SignInThrottle, key string) {
	t.Helper()
	for i := range LinkRedeemPerIP {
		if held, err := th.Admit(key); !held || err != nil {
			t.Fatalf("priming Admit #%d = %v, %v, want true", i+1, held, err)
		}
	}
}

// lrVerifyRoute is the verify route on a fresh GoTrue fake, enforced.
func lrVerifyRoute(t *testing.T, th *SignInThrottle) (http.Handler, *gaGoTrue) {
	t.Helper()
	f := newGaGoTrue(t)
	return lrRoutes()[0].build(t, f, th, true, nopLog()), f
}

func TestLinkRedeemLimit_TheThreeRoutesShareOneBudget(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "production")
	f := newGaGoTrue(t)
	f.routes["POST /verify"] = answer(http.StatusForbidden, gtOTPExpired)
	th := lrThrottle(time.Now)
	routes := lrRoutes()
	handlers := make([]http.Handler, len(routes))
	for i, rt := range routes {
		handlers[i] = rt.build(t, f, th, true, nopLog())
	}

	n := 0
	for range 10 {
		for i, rt := range routes {
			// GoTrue refuses the link, which lands on the same notice as a limit refusal; the call count tells them apart.
			lrRequireSeeOther(t, rt.name, rt.post(handlers[i], lrToken(n), lrXFF(gaWant)), rt.failed)
			n++
		}
	}
	if got := lrVerifyCalls(f); got != 3*10 {
		t.Fatalf("GoTrue /verify calls after 30 counted POSTs = %d, want 30", got)
	}

	for i, rt := range routes {
		lrRequireSeeOther(t, rt.name+" #31", rt.post(handlers[i], lrToken(n), lrXFF(gaWant)), rt.failed)
		n++
	}
	if got := lrVerifyCalls(f); got != 30 {
		t.Errorf("GoTrue /verify calls after 3 POSTs over the limit = %d, want still 30: the routes do not share one count", got)
	}
}

func TestLinkRedeemLimit_TheThirtiethIsAdmittedTheThirtyFirstIsNot(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "production")
	h, f := lrVerifyRoute(t, lrThrottle(time.Now))
	rt := lrRoutes()[0]
	for i := range 29 {
		rt.post(h, lrToken(i), lrXFF(gaWant))
	}
	if got := lrVerifyCalls(f); got != 29 {
		t.Fatalf("GoTrue /verify calls after 29 POSTs = %d, want 29", got)
	}
	rt.post(h, lrToken(29), lrXFF(gaWant))
	if got := lrVerifyCalls(f); got != 30 {
		t.Fatalf("the 30th POST did not reach GoTrue: /verify calls = %d, want 30", got)
	}
	rec := rt.post(h, lrToken(30), lrXFF(gaWant))
	lrRequireSeeOther(t, "the 31st POST", rec, rt.failed)
	if got := lrVerifyCalls(f); got != 30 {
		t.Errorf("the 31st POST reached GoTrue: /verify calls = %d, want 30", got)
	}
}

func TestLinkRedeemLimit_AFreshWindowAdmitsAgain(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "production")
	clock := newTestClock()
	h, f := lrVerifyRoute(t, lrThrottle(clock.Now))
	rt := lrRoutes()[0]
	for i := range LinkRedeemPerIP {
		rt.post(h, lrToken(i), lrXFF(gaWant))
	}
	if got := lrVerifyCalls(f); got != LinkRedeemPerIP {
		t.Fatalf("GoTrue /verify calls after the limit = %d, want %d", got, LinkRedeemPerIP)
	}

	clock.Advance(59*time.Minute + 59*time.Second)
	lrRequireSeeOther(t, "POST at 59m59s", rt.post(h, lrToken(100), lrXFF(gaWant)), rt.failed)
	if got := lrVerifyCalls(f); got != LinkRedeemPerIP {
		t.Fatalf("a POST at 59m59s reached GoTrue: /verify calls = %d, want %d", got, LinkRedeemPerIP)
	}

	clock.Advance(time.Second)
	rt.post(h, lrToken(101), lrXFF(gaWant))
	if got := lrVerifyCalls(f); got != LinkRedeemPerIP+1 {
		t.Errorf("a POST at 1h did not reach GoTrue: /verify calls = %d, want %d", got, LinkRedeemPerIP+1)
	}
}

func TestLinkRedeemLimit_OverTheLimitEachRouteLandsOnItsFailedNotice(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "production")
	for _, rt := range lrRoutes() {
		t.Run(rt.name, func(t *testing.T) {
			// Control: under the limit the same POST reaches GoTrue, so the refusal below is the limit.
			control := newGaGoTrue(t)
			rt.post(rt.build(t, control, lrThrottle(time.Now), true, nopLog()), lrToken(1), lrXFF(gaWant))
			if got := lrVerifyCalls(control); got != 1 {
				t.Fatalf("control: GoTrue /verify calls = %d, want 1", got)
			}

			f := newGaGoTrue(t)
			th := lrThrottle(time.Now)
			lrFill(t, th, gaWant)
			log, buf := captureLog()
			rec := rt.post(rt.build(t, f, th, true, log), lrToken(2), lrXFF(gaWant))

			lrRequireSeeOther(t, rt.name, rec, rt.failed)
			if calls := f.Calls(); len(calls) != 0 {
				t.Errorf("GoTrue saw %v, want no call (no /verify, PUT /user, /logout or /token)", gaNames(calls))
			}
			recs := recordsNamed(t, buf, rt.label+": limit reached")
			if len(recs) != 1 {
				t.Fatalf("want one %q line, got %d: %s", rt.label+": limit reached", len(recs), buf.String())
			}
			if recs[0]["level"] != "WARN" || recs[0]["limit"] != "ip" || recs[0]["enforced"] != true {
				t.Errorf("limit line = %v, want WARN with limit=ip enforced=true", recs[0])
			}
			if strings.Contains(buf.String(), gaWant) {
				t.Errorf("a log line holds the address: %s", buf.String())
			}
		})
	}
}

func TestLinkRedeemLimit_KeysLikeTheReadinessCheck(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "production")
	rt := lrRoutes()[0]
	const shapeA, shapeB = "203.0.113.7", "198.51.100.1, 10.0.0.5, 130.211.0.1" // one entry, then a private second from the right

	t.Run("another IPv4 address is not limited", func(t *testing.T) {
		h, f := lrVerifyRoute(t, lrThrottle(time.Now))
		for i := range LinkRedeemPerIP {
			rt.post(h, lrToken(i), lrXFF("203.0.113.7"))
		}
		if got := lrVerifyCalls(f); got != LinkRedeemPerIP {
			t.Fatalf("GoTrue /verify calls = %d, want %d", got, LinkRedeemPerIP)
		}
		rt.post(h, lrToken(100), lrXFF("203.0.113.8"))
		if got := lrVerifyCalls(f); got != LinkRedeemPerIP+1 {
			t.Errorf("203.0.113.8 did not reach GoTrue: /verify calls = %d, want %d", got, LinkRedeemPerIP+1)
		}
		rt.post(h, lrToken(101), lrXFF("203.0.113.7"))
		if got := lrVerifyCalls(f); got != LinkRedeemPerIP+1 {
			t.Errorf("203.0.113.7 reached GoTrue over the limit: /verify calls = %d, want %d", got, LinkRedeemPerIP+1)
		}
	})

	t.Run("one IPv6 /64 shares a count", func(t *testing.T) {
		h, f := lrVerifyRoute(t, lrThrottle(time.Now))
		for i := range LinkRedeemPerIP {
			rt.post(h, lrToken(i), lrXFF("2001:db8:1:2::1"))
		}
		if got := lrVerifyCalls(f); got != LinkRedeemPerIP {
			t.Fatalf("GoTrue /verify calls = %d, want %d", got, LinkRedeemPerIP)
		}
		lrRequireSeeOther(t, "same /64", rt.post(h, lrToken(100), lrXFF("2001:db8:1:2::abcd")), rt.failed)
		if got := lrVerifyCalls(f); got != LinkRedeemPerIP {
			t.Errorf("a second address in the same /64 reached GoTrue: /verify calls = %d, want %d", got, LinkRedeemPerIP)
		}
		rt.post(h, lrToken(101), lrXFF("2001:db8:1:3::1"))
		if got := lrVerifyCalls(f); got != LinkRedeemPerIP+1 {
			t.Errorf("another /64 did not reach GoTrue: /verify calls = %d, want %d", got, LinkRedeemPerIP+1)
		}
	})

	t.Run("requests with no usable address share unattributable", func(t *testing.T) {
		h, f := lrVerifyRoute(t, lrThrottle(time.Now))
		for i := range LinkRedeemPerIP {
			xff := shapeA
			if i%2 == 1 {
				xff = shapeB
			}
			rt.post(h, lrToken(i), xff)
		}
		if got := lrVerifyCalls(f); got != LinkRedeemPerIP {
			t.Fatalf("GoTrue /verify calls = %d, want %d", got, LinkRedeemPerIP)
		}
		for i, xff := range []string{shapeA, shapeB} {
			lrRequireSeeOther(t, "31st with no address", rt.post(h, lrToken(100+i), xff), rt.failed)
		}
		if got := lrVerifyCalls(f); got != LinkRedeemPerIP {
			t.Errorf("an unattributable request reached GoTrue over the limit: /verify calls = %d, want %d", got, LinkRedeemPerIP)
		}
		rt.post(h, lrToken(102), lrXFF("203.0.113.7"))
		if got := lrVerifyCalls(f); got != LinkRedeemPerIP+1 {
			t.Errorf("a request with an address did not reach GoTrue: /verify calls = %d, want %d", got, LinkRedeemPerIP+1)
		}
	})
}

func TestLinkRedeemLimit_AClientAddressHeaderDoesNotChooseTheKey(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "production")
	h, f := lrVerifyRoute(t, lrThrottle(time.Now))
	rt := lrRoutes()[0]
	for i := range LinkRedeemPerIP + 1 {
		req := gaRequest(rt.target, rpFormType, rt.body(lrToken(i), rt.typ, ""), lrXFF(gaWant))
		req.Header.Set("X-User-Address", fmt.Sprintf("192.0.2.%d", i+1))
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	calls := f.Calls()
	if len(calls) != LinkRedeemPerIP {
		t.Fatalf("GoTrue saw %d calls for 31 POSTs with 31 client headers, want %d: the client chose the key", len(calls), LinkRedeemPerIP)
	}
	gaRequireAddress(t, calls, gaWant)
}

// A client writes the leftmost forwarded entries; only the one the load balancer appended keys the count.
func TestLinkRedeemLimit_AForgedLeftmostForwardedEntryDoesNotChooseTheKey(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "production")
	h, f := lrVerifyRoute(t, lrThrottle(time.Now))
	rt := lrRoutes()[0]
	for i := range LinkRedeemPerIP + 1 {
		rt.post(h, lrToken(i), fmt.Sprintf("192.0.2.%d, %s, 130.211.0.1", i+1, gaWant))
	}
	if got := lrVerifyCalls(f); got != LinkRedeemPerIP {
		t.Errorf("GoTrue /verify calls for 31 POSTs with 31 forged leftmost entries = %d, want %d: the client chose the key", got, LinkRedeemPerIP)
	}
}

// ::ffff:a.b.c.d is a.b.c.d. Left mapped it would key every IPv4 client under one /64 and share one count.
func TestLinkRedeemLimit_AnIPv4MappedAddressCountsAsItsIPv4(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "production")
	h, f := lrVerifyRoute(t, lrThrottle(time.Now))
	rt := lrRoutes()[0]
	for i := range LinkRedeemPerIP {
		rt.post(h, lrToken(i), lrXFF("::ffff:203.0.113.7"))
	}
	if got := lrVerifyCalls(f); got != LinkRedeemPerIP {
		t.Fatalf("GoTrue /verify calls = %d, want %d", got, LinkRedeemPerIP)
	}
	lrRequireSeeOther(t, "plain IPv4 of the mapped address", rt.post(h, lrToken(100), lrXFF("203.0.113.7")), rt.failed)
	rt.post(h, lrToken(101), lrXFF("::ffff:203.0.113.8"))
	if got := lrVerifyCalls(f); got != LinkRedeemPerIP+1 {
		t.Errorf("another mapped IPv4 address did not reach GoTrue: /verify calls = %d, want %d", got, LinkRedeemPerIP+1)
	}
}

func TestLinkRedeemLimit_ARequestRefusedBeforeVerifyCountsNothing(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "production")
	rows := []struct {
		name         string
		body         func(rt lrRoute) string
		wantCode     int
		passwordOnly bool
	}{
		{"unparsable body", func(rt lrRoute) string { return "token=%zz&type=" + rt.typ }, http.StatusSeeOther, false},
		{"empty token", func(rt lrRoute) string { return rt.body("", rt.typ, rpPass) }, http.StatusSeeOther, false},
		{"oversize token", func(rt lrRoute) string { return rt.body(strings.Repeat("a", maxVerifyTokenBytes+1), rt.typ, rpPass) }, http.StatusSeeOther, false},
		{"wrong type", func(rt lrRoute) string { return rt.body(lrToken(1), "magiclink", rpPass) }, http.StatusSeeOther, false},
		{"password of 5 characters", func(rt lrRoute) string { return rt.body(lrToken(1), rt.typ, "12345") }, http.StatusBadRequest, true},
	}
	for _, rt := range lrRoutes() {
		for _, c := range rows {
			if c.passwordOnly && rt.name == "verify" {
				continue
			}
			t.Run(rt.name+"/"+c.name, func(t *testing.T) {
				f := newGaGoTrue(t)
				h := rt.build(t, f, lrThrottle(time.Now), true, nopLog())
				body := c.body(rt)
				for i := range LinkRedeemPerIP {
					req := gaRequest(rt.target, rpFormType, body, lrXFF(gaWant))
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, req)
					if rec.Code != c.wantCode {
						t.Fatalf("refused POST #%d = %d, want %d as today", i+1, rec.Code, c.wantCode)
					}
				}
				if calls := f.Calls(); len(calls) != 0 {
					t.Fatalf("GoTrue saw %v, want no call", gaNames(calls))
				}
				rt.post(h, lrToken(2), lrXFF(gaWant))
				if got := lrVerifyCalls(f); got != 1 {
					t.Errorf("a valid POST after 30 refused ones: GoTrue /verify calls = %d, want 1: a refused request was counted", got)
				}
			})
		}
	}
}

func TestLinkRedeemLimit_ASuccessDoesNotClearTheCount(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "production")
	f := newGaGoTrue(t)
	var accept atomic.Bool
	refuse := answer(http.StatusForbidden, rpOTPExpired)
	accepted := f.routes["POST /verify"]
	f.routes["POST /verify"] = func(w http.ResponseWriter, r *http.Request) {
		if accept.Load() {
			accepted(w, r)
			return
		}
		refuse(w, r)
	}
	th := lrThrottle(time.Now)
	routes := lrRoutes()
	reset := routes[1].build(t, f, th, true, nopLog())
	verify := routes[0].build(t, f, th, true, nopLog())

	for i := range LinkRedeemPerIP - 1 {
		lrRequireSeeOther(t, "refused reset", routes[1].post(reset, lrToken(i), lrXFF(gaWant)), routes[1].failed)
	}
	accept.Store(true)
	lrRequireSeeOther(t, "the accepted reset", routes[1].post(reset, lrToken(100), lrXFF(gaWant)), rpOK)
	if names := gaNames(f.Calls()); len(names) < 2 || names[len(names)-2] != "PUT /user" || names[len(names)-1] != "POST /logout" {
		t.Fatalf("the accepted reset did not finish end to end: GoTrue calls %v", names)
	}
	before := len(f.Calls())

	lrRequireSeeOther(t, "verify after a success", routes[0].post(verify, lrToken(101), lrXFF(gaWant)), routes[0].failed)
	if after := len(f.Calls()); after != before {
		t.Errorf("a POST after the success reached GoTrue: %d calls, want %d; the success cleared the count", after, before)
	}
}

func TestLinkRedeemLimit_UnenforcedLogsAndAdmits(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "pr-1")
	for _, rt := range lrRoutes() {
		t.Run(rt.name, func(t *testing.T) {
			f := newGaGoTrue(t)
			th := lrThrottle(time.Now)
			lrFill(t, th, "130.211.0.1") // a preview keys on the rightmost entry
			log, buf := captureLog()
			rt.post(rt.build(t, f, th, false, log), lrToken(1), lrXFF(gaWant))

			if got := lrVerifyCalls(f); got != 1 {
				t.Errorf("GoTrue /verify calls = %d, want 1: an unenforced limit must admit", got)
			}
			recs := recordsNamed(t, buf, rt.label+": limit reached")
			if len(recs) != 1 {
				t.Fatalf("want one %q line, got %d: %s", rt.label+": limit reached", len(recs), buf.String())
			}
			if recs[0]["limit"] != "ip" || recs[0]["enforced"] != false {
				t.Errorf("limit line = %v, want limit=ip enforced=false", recs[0])
			}
		})
	}
}

func TestLinkRedeemLimit_AnUnreadableCountRefusesWhereEnforced(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "production")
	for _, rt := range lrRoutes() {
		t.Run(rt.name, func(t *testing.T) {
			f := newGaGoTrue(t)
			log, buf := captureLog()
			th := lrThrottle(time.Now)
			th.Shared(deadPool(t), log)
			rec := rt.post(rt.build(t, f, th, true, log), lrToken(1), lrXFF(gaWant))

			lrRequireSeeOther(t, rt.name, rec, rt.failed)
			if calls := f.Calls(); len(calls) != 0 {
				t.Errorf("GoTrue saw %v, want no call", gaNames(calls))
			}
			// The throttle reports the database error itself; only then is the missing limit line meaningful.
			if n := len(recordsNamed(t, buf, "link-redeem-ip throttle: reserve")); n != 1 {
				t.Fatalf("want the throttle's one reserve error, got %d: %s", n, buf.String())
			}
			if n := len(recordsNamed(t, buf, rt.label+": limit reached")); n != 0 {
				t.Errorf("an unreadable count logged %d limit lines, want none", n)
			}
		})
	}
}

func TestLinkRedeemLimit_AnUnreadableCountUnenforcedStillVerifies(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "pr-1")
	f := newGaGoTrue(t)
	log, buf := captureLog()
	th := lrThrottle(time.Now)
	th.Shared(deadPool(t), log)
	rt := lrRoutes()[0]
	rt.post(rt.build(t, f, th, false, log), lrToken(1), lrXFF(gaWant))

	if got := lrVerifyCalls(f); got != 1 {
		t.Errorf("GoTrue /verify calls = %d, want 1", got)
	}
	if n := len(recordsNamed(t, buf, "link-redeem-ip throttle: reserve")); n != 1 {
		t.Fatalf("want the throttle's one reserve error, got %d: %s", n, buf.String())
	}
	if n := len(recordsNamed(t, buf, "verify: limit reached")); n != 0 {
		t.Errorf("an unreadable count logged %d limit lines, want none", n)
	}
}
