package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const (
	scApp     = "https://app.ascomply.com"
	scOps     = "https://ops.ascomply.com"
	scSupport = "https://sup.ascomply.com"
	scLanding = "https://ascomply.com"
	scEvil    = "https://evil.example"

	msgOriginNotAllowed = "origin not allowed"
	scCookieOnly        = "refresh-cookie-only-k2x9" // a token that only a request cookie carries
)

func testSessionCookies(t *testing.T) *SessionCookies {
	t.Helper()
	c, err := ParseSessionOrigins("app=" + scApp + ",ops=" + scOps + ",support=" + scSupport)
	if err != nil {
		t.Fatalf("ParseSessionOrigins: %v", err)
	}
	return c
}

// cookieModeHeaders is a cookie-mode request from origin; "" sends no Origin.
func cookieModeHeaders(origin string) map[string]string {
	h := map[string]string{SessionHeader: "cookie"}
	if origin != "" {
		h["Origin"] = origin
	}
	return h
}

func rtCookie(aud, value string) *http.Cookie {
	return &http.Cookie{Name: "__Host-rt-" + aud, Value: value}
}

func doSession(h http.Handler, path, body string, hdr map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func requireNoSetCookie(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Values("Set-Cookie"); len(got) != 0 {
		t.Fatalf("Set-Cookie = %q, want none", got)
	}
}

// requireSetCookie asserts exactly one Set-Cookie holding name=value with the fixed attributes.
func requireSetCookie(t *testing.T, rec *httptest.ResponseRecorder, name, value string) *http.Cookie {
	t.Helper()
	got := rec.Result().Cookies()
	if len(got) != 1 {
		t.Fatalf("Set-Cookie = %q, want exactly one", rec.Header().Values("Set-Cookie"))
	}
	c := got[0]
	if c.Name != name || c.Value != value {
		t.Fatalf("cookie = %s=%s, want %s=%s", c.Name, c.Value, name, value)
	}
	if !c.HttpOnly || !c.Secure || c.Path != "/" || c.Domain != "" || c.MaxAge != 34560000 {
		t.Errorf("cookie attributes = %q, want HttpOnly, Secure, Path=/, no Domain, Max-Age=34560000", rec.Header().Get("Set-Cookie"))
	}
	return c
}

// requireClearCookie asserts exactly one Set-Cookie that empties name with Max-Age=0.
func requireClearCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	got := rec.Result().Cookies()
	if len(got) != 1 {
		t.Fatalf("Set-Cookie = %q, want exactly one clear of %s", rec.Header().Values("Set-Cookie"), name)
	}
	c := got[0]
	if c.Name != name || c.Value != "" {
		t.Fatalf("cookie = %s=%q, want an empty %s", c.Name, c.Value, name)
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "Max-Age=0") || !c.HttpOnly || !c.Secure || c.Path != "/" || c.Domain != "" {
		t.Errorf("clear = %q, want Max-Age=0, HttpOnly, Secure, Path=/, no Domain", rec.Header().Get("Set-Cookie"))
	}
	return c
}

func setFor(t *testing.T, c *SessionCookies, origin, site, host string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/exchange", nil)
	req.Header.Set("Origin", origin)
	if site != "" {
		req.Header.Set("Sec-Fetch-Site", site)
	}
	if host != "" {
		req.Host = host
	}
	rec := httptest.NewRecorder()
	c.set(rec, req, "app", "tok-1")
	return rec
}

func TestSessionCookie_SameSiteFetchGetsStrictUnpartitioned(t *testing.T) {
	c := testSessionCookies(t)
	for _, site := range []string{"same-site", "same-origin"} {
		t.Run(site, func(t *testing.T) {
			got := requireSetCookie(t, setFor(t, c, scApp, site, ""), "__Host-rt-app", "tok-1")
			if got.SameSite != http.SameSiteStrictMode || got.Partitioned {
				t.Errorf("SameSite = %v, Partitioned = %v, want Strict and unpartitioned", got.SameSite, got.Partitioned)
			}
		})
	}
}

func TestSessionCookie_OtherFetchGetsNonePartitioned(t *testing.T) {
	c := testSessionCookies(t)
	for name, tc := range map[string]struct{ site, host string }{
		"cross-site":       {"cross-site", ""},
		"none":             {"none", ""},
		"wrong case":       {"Same-Site", ""},
		"absent":           {"", ""},
		"api host, cross":  {"cross-site", "api.ascomply.com"}, // Host is never read
		"unrelated host":   {"cross-site", "gateway-pr-7.run.app"},
		"api host, absent": {"", "api.ascomply.com"},
	} {
		t.Run(name, func(t *testing.T) {
			got := requireSetCookie(t, setFor(t, c, scApp, tc.site, tc.host), "__Host-rt-app", "tok-1")
			if got.SameSite != http.SameSiteNoneMode || !got.Partitioned {
				t.Errorf("SameSite = %v, Partitioned = %v, want None and partitioned", got.SameSite, got.Partitioned)
			}
		})
	}
}

func TestSessionCookie_ClearMatchesTheSetAttributes(t *testing.T) {
	c := testSessionCookies(t)
	for name, site := range map[string]string{"strict arm": "same-site", "none arm": "cross-site"} {
		t.Run(name, func(t *testing.T) {
			set := requireSetCookie(t, setFor(t, c, scApp, site, ""), "__Host-rt-app", "tok-1")

			req := httptest.NewRequest(http.MethodPost, "/auth/sign-out", nil)
			req.Header.Set("Origin", scApp)
			req.Header.Set("Sec-Fetch-Site", site)
			rec := httptest.NewRecorder()
			c.clear(rec, req, "app")
			cleared := requireClearCookie(t, rec, "__Host-rt-app")

			if cleared.SameSite != set.SameSite || cleared.Partitioned != set.Partitioned {
				t.Errorf("clear SameSite/Partitioned = %v/%v, set sent %v/%v", cleared.SameSite, cleared.Partitioned, set.SameSite, set.Partitioned)
			}
		})
	}
}

func TestSessionCookies_ModeNeedsTheExactHeaderValue(t *testing.T) {
	c := testSessionCookies(t)
	req := func(origin, header string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if header != "" {
			r.Header.Set(SessionHeader, header)
		}
		return r
	}

	// Positive pair: the exact value from a session origin is cookie mode for that origin's audience.
	if aud, ok, err := c.mode(req(scOps, "cookie")); aud != "ops" || !ok || err != nil {
		t.Fatalf("mode(ops, cookie) = %q, %v, %v, want ops, true, nil", aud, ok, err)
	}
	if aud, ok, err := c.mode(req(scApp, "")); aud != "" || ok || err != nil {
		t.Errorf("mode(app, no header) = %q, %v, %v, want body mode", aud, ok, err)
	}
	for name, r := range map[string]*http.Request{
		"yes":              req(scApp, "yes"),
		"wrong case":       req(scApp, "Cookie"),
		"landing":          req(scLanding, "cookie"),
		"foreign":          req(scEvil, "cookie"),
		"null":             req("null", "cookie"),
		"no origin":        req("", "cookie"),
		"app origin+slash": req(scApp+"/", "cookie"),
	} {
		if _, ok, err := c.mode(r); ok || err == nil {
			t.Errorf("%s: mode = ok %v, err %v, want a refusal", name, ok, err)
		}
	}
	// No configured cookies: the header alone is refused, no header is body mode.
	var none *SessionCookies
	if _, ok, err := none.mode(req(scApp, "cookie")); ok || err == nil {
		t.Errorf("nil cookies, header: ok %v, err %v, want a refusal", ok, err)
	}
	if _, ok, err := none.mode(req(scApp, "")); ok || err != nil {
		t.Errorf("nil cookies, no header: ok %v, err %v, want body mode", ok, err)
	}

	// The route answers it: 403 before GoTrue.
	fake := newTokenFake(t, http.StatusOK, gtRefreshed)
	h := RefreshHandler(fake.URL, testClient(), slog.New(slog.DiscardHandler), c)
	rec := doSession(h, "/auth/refresh", ``, map[string]string{"Origin": scApp, SessionHeader: "yes"}, rtCookie("app", refreshR0))
	requireRefusal(t, rec, http.StatusForbidden, msgOriginNotAllowed)
	if n := fake.Hits(); n != 0 {
		t.Errorf("GoTrue saw %d calls, want 0", n)
	}
	requireNoSetCookie(t, rec)
}

func TestParseSessionOrigins_RefusesMalformed(t *testing.T) {
	for _, v := range []string{
		"app",
		"=https://a.x",
		"App=https://a.x",
		"app=https://a.x,app=https://b.x",
		"app=https://a.x,ops=https://a.x",
		"app=https://a.x/p",
		"app=http://a.x",
		"app=https://a.x?q",
	} {
		if c, err := ParseSessionOrigins(v); err == nil {
			t.Errorf("ParseSessionOrigins(%q) = %v, nil, want an error", v, c)
		}
	}

	c, err := ParseSessionOrigins("app=https://a.x:8443,ops=https://o.x")
	if err != nil {
		t.Fatalf("a well-formed value: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	r.Header.Set(SessionHeader, "cookie")
	r.Header.Set("Origin", "https://a.x:8443")
	if aud, ok, err := c.mode(r); aud != "app" || !ok || err != nil {
		t.Errorf("mode for the :8443 origin = %q, %v, %v, want app, true, nil", aud, ok, err)
	}
}

// ExchangeHandler takes no logger, so the exchange rows cannot log.
func TestSessionCookie_NeverLogsTheToken(t *testing.T) {
	log, buf := captureLog()
	cookies := testSessionCookies(t)
	access := signOutAccess(t, subjectS1)
	granted := answer(http.StatusOK, signOutGranted(access))
	ok := answer(http.StatusNoContent, "")

	type row struct {
		name   string
		status int
		do     func() *httptest.ResponseRecorder
	}
	refresh := func(authURL *url.URL, body string) func() *httptest.ResponseRecorder {
		return func() *httptest.ResponseRecorder {
			return doCookieRefresh(RefreshHandler(authURL, testClient(), log, cookies), scApp, body, rtCookie("app", scCookieOnly))
		}
	}
	signOut := func(token, logout http.HandlerFunc, authURL *url.URL, events AccountEvents) func() *httptest.ResponseRecorder {
		return func() *httptest.ResponseRecorder {
			f := newSignOutFake(t, token, logout)
			if authURL == nil {
				authURL = f.URL
			}
			return doCookieSignOut(SignOutHandler(authURL, testClient(), liveSessions(t), log, events, cookies), scApp, ``, rtCookie("app", scCookieOnly))
		}
	}
	rows := []row{
		{"refresh 200", 200, refresh(newTokenFake(t, 200, gtRefreshed).URL, ``)},
		{"refresh adoption", 200, refresh(newTokenFake(t, 200, gtRefreshed).URL, refreshBody(refreshR0))},
		{"refresh 400", 401, refresh(newTokenFake(t, 400, gtRefreshAlreadyUsed).URL, ``)},
		{"refresh 429", 429, refresh(newTokenFake(t, 429, gtOverRequestRateLimit).URL, ``)},
		{"refresh 500", 502, refresh(newTokenFake(t, 500, gtInternal).URL, ``)},
		{"refresh 200 without refresh_token", 502, refresh(newTokenFake(t, 200, `{"access_token":"`+refreshA1+`"}`).URL, ``)},
		{"refresh unreachable", 502, refresh(closedURL(t), ``)},
		{"refresh bad body", 400, refresh(newTokenFake(t, 200, gtRefreshed).URL, `not json`)},
		{"sign-out 204", 204, signOut(granted, ok, nil, &recEvents{})},
		{"sign-out grant 400", 401, signOut(answer(400, gtRefreshNotFound), ok, nil, nil)},
		{"sign-out grant 500", 502, signOut(answer(500, gtInternal), ok, nil, nil)},
		{"sign-out logout 500", 502, signOut(granted, answer(500, gtInternal), nil, nil)},
		{"sign-out logout unreachable", 502, signOut(granted, dropped, nil, nil)},
		{"sign-out grant unreachable", 502, signOut(nil, nil, closedURL(t), nil)},
		{"sign-out recorder failure", 204, signOut(granted, ok, nil, &recEvents{err: context.DeadlineExceeded})},
	}
	for _, r := range rows {
		if rec := r.do(); rec.Code != r.status {
			t.Errorf("%s: status = %d, want %d: %s", r.name, rec.Code, r.status, rec.Body.String())
		}
	}

	out := buf.String()
	// Positive control: the failing rows logged at WARN with their upstream status.
	if !strings.Contains(out, `"level":"WARN"`) || !logHasValue(buf, http.StatusInternalServerError) {
		t.Fatalf("no WARN line names upstream status 500; the scan would be blind: %q", out)
	}
	for _, secret := range []string{scCookieOnly, "__Host-rt-", refreshR0, refreshR1, refreshA1, signOutR0, signOutR1, access, subjectS1} {
		if strings.Contains(out, secret) {
			t.Errorf("log carries %q:\n%s", secret, out)
		}
	}
}
