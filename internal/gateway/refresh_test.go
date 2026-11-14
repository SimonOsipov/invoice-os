package gateway

import (
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const (
	refreshR0 = "refresh-presented-r0-9w3k"
	refreshA1 = "eyJhbGciOiJFUzI1NiJ9.renewed-access-a1.sig"
	refreshR1 = "refresh-renewed-r1-5m8t"

	msgRefreshRefused     = "invalid or expired refresh token"
	msgRefreshUnavailable = "renewal is unavailable"
	msgRefreshRequired    = "refresh_token is required"
)

// GoTrue v2.197.0 /token?grant_type=refresh_token answers (internal/api/token_refresh.go, internal/tokens/service.go).
var (
	gtRefreshed = `{"access_token":"` + refreshA1 + `","token_type":"bearer","expires_in":3600,"expires_at":1790000000,"refresh_token":"` + refreshR1 + `","user":{"id":"7f3c2a1e-0b7d-4f51-9a0e-5d1c2b3a4e5f","email":"renewed@corp.example"}}`

	gtRefreshNotFound    = `{"code":400,"error_code":"refresh_token_not_found","msg":"Invalid Refresh Token: Refresh Token Not Found"}`
	gtRefreshAlreadyUsed = `{"code":400,"error_code":"refresh_token_already_used","msg":"Invalid Refresh Token: Already Used"}`
	gtSessionNotFound    = `{"code":400,"error_code":"session_not_found","msg":"Invalid Refresh Token: No Valid Session Found"}`
	gtSessionExpired     = `{"code":400,"error_code":"session_expired","msg":"Invalid Refresh Token: Session Expired"}`
	gtRefreshUserBanned  = `{"code":400,"error_code":"user_banned","msg":"Invalid Refresh Token: User Banned"}`
	gtRefreshInvalid     = `{"code":400,"error_code":"validation_failed","msg":"Refresh token is not valid"}`
	// apierrors.NewOAuthError: a 400 with no error_code.
	gtRefreshOAuth = `{"error":"invalid_request","error_description":"refresh_token required"}`
)

func refreshBody(token string) string {
	b, _ := json.Marshal(map[string]string{"refresh_token": token})
	return string(b)
}

func newRefresh(authURL *url.URL, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return RefreshHandler(authURL, testClient(), log, nil)
}

func doRefresh(h http.Handler, body string) *httptest.ResponseRecorder {
	return serve(h, http.MethodPost, "/auth/refresh", body)
}

func TestRefresh_Success200BothTokensOnly(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtRefreshed)

	rec := doRefresh(newRefresh(fake.URL, nil), refreshBody(refreshR0))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if at, rt := requireAnswer(t, rec.Body.Bytes()); at != refreshA1 || rt != refreshR1 {
		t.Errorf("answer = (%q, %q), want (%q, %q)", at, rt, refreshA1, refreshR1)
	}
	if strings.Contains(rec.Body.String(), "renewed@corp.example") {
		t.Errorf("answer carries GoTrue's user object: %s", rec.Body.String())
	}

	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("GoTrue saw %d calls, want exactly 1: %+v", len(calls), calls)
	}
	if c := calls[0]; c.Method != http.MethodPost || c.Path != "/token" || c.RawQuery != "grant_type=refresh_token" {
		t.Errorf("GoTrue saw %s %s?%s, want POST /token?grant_type=refresh_token", c.Method, c.Path, c.RawQuery)
	}
	var sent map[string]any
	if err := json.Unmarshal(calls[0].Body, &sent); err != nil {
		t.Fatalf("token body %q is not JSON: %v", calls[0].Body, err)
	}
	if want := map[string]any{"refresh_token": refreshR0}; !maps.Equal(sent, want) {
		t.Errorf("token body = %v, want exactly %v", sent, want)
	}
}

func TestRefresh_GoTrueErrorMapping(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantMsg    string
	}{
		{"refresh_token_not_found", http.StatusBadRequest, gtRefreshNotFound, http.StatusUnauthorized, msgRefreshRefused},
		{"refresh_token_already_used", http.StatusBadRequest, gtRefreshAlreadyUsed, http.StatusUnauthorized, msgRefreshRefused},
		{"session_not_found", http.StatusBadRequest, gtSessionNotFound, http.StatusUnauthorized, msgRefreshRefused},
		{"session_expired", http.StatusBadRequest, gtSessionExpired, http.StatusUnauthorized, msgRefreshRefused},
		{"user_banned", http.StatusBadRequest, gtRefreshUserBanned, http.StatusUnauthorized, msgRefreshRefused},
		{"validation_failed", http.StatusBadRequest, gtRefreshInvalid, http.StatusUnauthorized, msgRefreshRefused},
		{"oauth invalid_request", http.StatusBadRequest, gtRefreshOAuth, http.StatusUnauthorized, msgRefreshRefused},
		{"401", http.StatusUnauthorized, `{"code":401,"msg":"unauthorized"}`, http.StatusUnauthorized, msgRefreshRefused},
		{"403", http.StatusForbidden, `{"code":403,"msg":"forbidden"}`, http.StatusUnauthorized, msgRefreshRefused},
		{"404", http.StatusNotFound, `{"code":404,"msg":"not found"}`, http.StatusUnauthorized, msgRefreshRefused},
		{"429", http.StatusTooManyRequests, gtOverRequestRateLimit, http.StatusTooManyRequests, msgTooMany},
		{"500", http.StatusInternalServerError, gtInternal, http.StatusBadGateway, msgRefreshUnavailable},
		{"503", http.StatusServiceUnavailable, `upstream down`, http.StatusBadGateway, msgRefreshUnavailable},
		{"200 without access_token", http.StatusOK, `{"refresh_token":"` + refreshR1 + `"}`, http.StatusBadGateway, msgRefreshUnavailable},
		{"200 without refresh_token", http.StatusOK, `{"access_token":"` + refreshA1 + `"}`, http.StatusBadGateway, msgRefreshUnavailable},
		{"200 empty access_token", http.StatusOK, `{"access_token":"","refresh_token":"` + refreshR1 + `"}`, http.StatusBadGateway, msgRefreshUnavailable},
		{"200 empty refresh_token", http.StatusOK, `{"access_token":"` + refreshA1 + `","refresh_token":""}`, http.StatusBadGateway, msgRefreshUnavailable},
	}
	var refusal string
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := newTokenFake(t, c.status, c.body)
			log, buf := captureLog()
			rec := doRefresh(newRefresh(fake.URL, log), refreshBody(refreshR0))
			requireRefusal(t, rec, c.wantStatus, c.wantMsg)
			if n := fake.Hits(); n != 1 {
				t.Errorf("GoTrue saw %d calls, want 1", n)
			}
			if c.wantStatus == http.StatusBadGateway {
				if !strings.Contains(buf.String(), `"level":"WARN"`) || !logHasValue(buf, c.status) {
					t.Errorf("a 502 must log at WARN with upstream status %d: %q", c.status, buf.String())
				}
			}
			// Every refused 4xx answers the same bytes: the client cannot tell the reasons apart.
			if c.wantStatus == http.StatusUnauthorized {
				if refusal == "" {
					refusal = rec.Body.String()
				} else if rec.Body.String() != refusal {
					t.Errorf("body = %q, want the same bytes as every other refusal %q", rec.Body.String(), refusal)
				}
			}
		})
	}

	t.Run("unreachable", func(t *testing.T) {
		closed := closedURL(t)
		log, buf := captureLog()
		requireRefusal(t, doRefresh(newRefresh(closed, log), refreshBody(refreshR0)), http.StatusBadGateway, msgRefreshUnavailable)
		// The transport error names the address it could not reach.
		var line struct{ Level, Error string }
		if err := json.Unmarshal(buf.Bytes(), &line); err != nil || line.Level != "WARN" || !strings.Contains(line.Error, closed.Host+": connect:") {
			t.Errorf("unreachable must log one WARN line whose error dials %s: %q", closed.Host, buf.String())
		}
	})
}

func TestRefresh_BadBody400NoUpstreamCall(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtRefreshed)
	h := newRefresh(fake.URL, nil)

	// refreshBody adds 20 bytes around the token.
	oneOver := refreshBody(strings.Repeat("r", maxExchangeBodyBytes-19))
	for _, c := range []struct{ name, body, msg string }{
		{"not json", `not json`, msgInvalidBody},
		{"no body", ``, msgInvalidBody},
		{"array", `[]`, msgInvalidBody},
		{"one byte over 1 KiB", oneOver, msgInvalidBody},
		{"number token", `{"refresh_token":123}`, msgInvalidBody},
		{"array token", `{"refresh_token":["` + refreshR0 + `"]}`, msgInvalidBody},
		{"object token", `{"refresh_token":{"value":"` + refreshR0 + `"}}`, msgInvalidBody},
		{"bool token", `{"refresh_token":true}`, msgInvalidBody},
		{"empty object", `{}`, msgRefreshRequired},
		{"empty token", `{"refresh_token":""}`, msgRefreshRequired},
		{"null token", `{"refresh_token":null}`, msgRefreshRequired},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := fake.Hits()
			requireRefusal(t, doRefresh(h, c.body), http.StatusBadRequest, c.msg)
			if n := fake.Hits() - before; n != 0 {
				t.Errorf("GoTrue saw %d calls from a refused body, want 0", n)
			}
		})
	}
	if len(oneOver) != maxExchangeBodyBytes+1 {
		t.Fatalf("one-over body is %d bytes, want %d", len(oneOver), maxExchangeBodyBytes+1)
	}

	// Positive pair: a well-formed body, and one of exactly 1 KiB, on the same handler do reach GoTrue.
	atLimit := refreshBody(strings.Repeat("r", maxExchangeBodyBytes-20))
	if len(atLimit) != maxExchangeBodyBytes {
		t.Fatalf("at-limit body is %d bytes, want %d", len(atLimit), maxExchangeBodyBytes)
	}
	for i, body := range []string{refreshBody(refreshR0), atLimit} {
		before := fake.Hits()
		if rec := doRefresh(h, body); rec.Code != http.StatusOK {
			t.Errorf("body %d (%d bytes): status = %d, want 200: %s", i, len(body), rec.Code, rec.Body.String())
		}
		if n := fake.Hits() - before; n != 1 {
			t.Errorf("body %d (%d bytes): GoTrue saw %d calls, want 1", i, len(body), n)
		}
	}
}

// The handler forwards no client path, query or extra body key to GoTrue.
func TestRefresh_ForwardsNoClientPathQueryOrKey(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtRefreshed)
	h := newRefresh(fake.URL, nil)
	body := `{"refresh_token":"` + refreshR0 + `","grant_type":"password","email":"x@corp.example","password":"pw"}`

	rec := serve(h, http.MethodPost, "/auth/refresh/../admin/users?grant_type=password&redirect_to=https://evil.example", body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("GoTrue saw %d calls, want exactly 1: %+v", len(calls), calls)
	}
	if c := calls[0]; c.Path != "/token" || c.RawQuery != "grant_type=refresh_token" {
		t.Errorf("GoTrue saw %s?%s, want /token?grant_type=refresh_token", c.Path, c.RawQuery)
	}
	var sent map[string]any
	if err := json.Unmarshal(calls[0].Body, &sent); err != nil {
		t.Fatalf("token body %q is not JSON: %v", calls[0].Body, err)
	}
	if want := map[string]any{"refresh_token": refreshR0}; !maps.Equal(sent, want) {
		t.Errorf("token body = %v, want exactly %v", sent, want)
	}
}

func TestRefresh_NoStoreAnd405(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtRefreshed)
	h := newRefresh(fake.URL, nil)
	answers := map[string]*httptest.ResponseRecorder{}

	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		rec := serve(h, m, "/auth/refresh", refreshBody(refreshR0))
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
			t.Errorf("%s: %d Allow=%q, want 405 Allow POST", m, rec.Code, rec.Header().Get("Allow"))
		}
		answers[m] = rec
	}
	if n := fake.Hits(); n != 0 {
		t.Errorf("GoTrue saw %d calls from non-POST methods, want 0", n)
	}

	answers["200"] = doRefresh(h, refreshBody(refreshR0))
	answers["400 body"] = doRefresh(h, `not json`)
	answers["400 token"] = doRefresh(h, `{}`)
	answers["401"] = doRefresh(newRefresh(newTokenFake(t, http.StatusBadRequest, gtRefreshAlreadyUsed).URL, nil), refreshBody(refreshR0))
	answers["429"] = doRefresh(newRefresh(newTokenFake(t, http.StatusTooManyRequests, gtOverRequestRateLimit).URL, nil), refreshBody(refreshR0))
	answers["502"] = doRefresh(newRefresh(closedURL(t), nil), refreshBody(refreshR0))

	// Positive pair: the answers above are the statuses they claim to be.
	for name, want := range map[string]int{"200": 200, "400 body": 400, "400 token": 400, "401": 401, "429": 429, "502": 502} {
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

func TestRefresh_NeverLogsSecrets(t *testing.T) {
	log, buf := captureLog()
	body := refreshBody(refreshR0)

	doRefresh(newRefresh(newTokenFake(t, http.StatusOK, gtRefreshed).URL, log), body)
	doRefresh(newRefresh(newTokenFake(t, http.StatusBadRequest, gtRefreshAlreadyUsed).URL, log), body)
	doRefresh(newRefresh(newTokenFake(t, http.StatusInternalServerError, gtInternal).URL, log), body)
	doRefresh(newRefresh(newTokenFake(t, http.StatusOK, `{"access_token":"`+refreshA1+`"}`).URL, log), body)
	doRefresh(newRefresh(newTokenFake(t, http.StatusOK, `{"refresh_token":"`+refreshR1+`"}`).URL, log), body)
	doRefresh(newRefresh(closedURL(t), log), body)

	// Positive control: a 502 logs at WARN with its upstream status.
	if !strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Fatalf("no WARN line was logged; a 502 must log its upstream status or error: %q", buf.String())
	}
	if !logHasValue(buf, http.StatusInternalServerError) {
		t.Errorf("no log attribute carries the upstream status 500: %s", buf.String())
	}
	for _, secret := range []string{refreshR0, refreshA1, refreshR1} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("log carries %q: %s", secret, buf.String())
		}
	}
}

func TestRefresh_JoinsUnderAnAuthURLPrefix(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtRefreshed)

	rec := doRefresh(newRefresh(fake.URL.JoinPath("prefix"), nil), refreshBody(refreshR0))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("GoTrue saw %d calls, want exactly 1: %+v", len(calls), calls)
	}
	if c := calls[0]; c.Path != "/prefix/token" || c.RawQuery != "grant_type=refresh_token" {
		t.Errorf("GoTrue saw %s?%s, want /prefix/token?grant_type=refresh_token", c.Path, c.RawQuery)
	}
}

func newCookieRefresh(t *testing.T, authURL *url.URL, log *slog.Logger) http.Handler {
	t.Helper()
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return RefreshHandler(authURL, testClient(), log, testSessionCookies(t))
}

// doCookieRefresh renews in cookie mode from origin with the given request cookies.
func doCookieRefresh(h http.Handler, origin, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	return doSession(h, "/auth/refresh", body, cookieModeHeaders(origin), cookies...)
}

// sentTokens returns the refresh_token of each call GoTrue saw.
func sentTokens(t *testing.T, calls []tokenCall) []string {
	t.Helper()
	var out []string
	for _, c := range calls {
		var m map[string]string
		if err := json.Unmarshal(c.Body, &m); err != nil {
			t.Fatalf("token body %q is not JSON: %v", c.Body, err)
		}
		out = append(out, m["refresh_token"])
	}
	return out
}

func TestRefresh_CookieModeRotatesTheCookie(t *testing.T) {
	for name, body := range map[string]string{"empty body": ``, "empty object": `{}`} {
		t.Run(name, func(t *testing.T) {
			fake := newTokenFake(t, http.StatusOK, gtRefreshed)

			rec := doCookieRefresh(newCookieRefresh(t, fake.URL, nil), scApp, body, rtCookie("app", refreshR0))

			if got := requireOnlyKey(t, rec, "access_token"); got != refreshA1 {
				t.Errorf("access_token = %q, want %q", got, refreshA1)
			}
			if got := sentTokens(t, fake.Calls()); len(got) != 1 || got[0] != refreshR0 {
				t.Errorf("GoTrue saw refresh tokens %q, want only the cookie's %q", got, refreshR0)
			}
			requireSetCookie(t, rec, "__Host-rt-app", refreshR1)
			if strings.Contains(rec.Body.String(), refreshR1) {
				t.Errorf("body holds the rotated token: %s", rec.Body.String())
			}
		})
	}
}

func TestRefresh_CookieModeAdoptsABodyToken(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtRefreshed)

	rec := doCookieRefresh(newCookieRefresh(t, fake.URL, nil), scApp, refreshBody(refreshR0), rtCookie("app", scCookieOnly))

	if got := requireOnlyKey(t, rec, "access_token"); got != refreshA1 {
		t.Errorf("access_token = %q, want %q", got, refreshA1)
	}
	if got := sentTokens(t, fake.Calls()); len(got) != 1 || got[0] != refreshR0 {
		t.Errorf("GoTrue saw refresh tokens %q, want only the body's %q", got, refreshR0)
	}
	requireSetCookie(t, rec, "__Host-rt-app", refreshR1)
}

func TestRefresh_CookieModeBadBodyIs400(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtRefreshed)
	h := newCookieRefresh(t, fake.URL, nil)

	rec := doCookieRefresh(h, scApp, `not json`, rtCookie("app", refreshR0))
	requireRefusal(t, rec, http.StatusBadRequest, msgInvalidBody)
	requireNoSetCookie(t, rec)
	if n := fake.Hits(); n != 0 {
		t.Errorf("GoTrue saw %d calls from a refused body, want 0", n)
	}

	// Positive pair: `{}` is a valid cookie-mode body on the same handler.
	if rec := doCookieRefresh(h, scApp, `{}`, rtCookie("app", refreshR0)); rec.Code != http.StatusOK {
		t.Errorf("`{}` = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestRefresh_CookieModeRefusesAnOriginOutsideTheList(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtRefreshed)
	h := newCookieRefresh(t, fake.URL, nil)

	for name, origin := range map[string]string{"landing": scLanding, "foreign": scEvil, "null": "null", "none": ""} {
		t.Run(name, func(t *testing.T) {
			rec := doCookieRefresh(h, origin, ``, rtCookie("app", refreshR0))
			requireRefusal(t, rec, http.StatusForbidden, msgOriginNotAllowed)
			requireNoSetCookie(t, rec)
		})
	}
	if n := fake.Hits(); n != 0 {
		t.Errorf("GoTrue saw %d calls, want 0", n)
	}

	// Positive pair: the same cookie from the app origin renews.
	if rec := doCookieRefresh(h, scApp, ``, rtCookie("app", refreshR0)); rec.Code != http.StatusOK {
		t.Errorf("app origin = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestRefresh_BodyModeIgnoresTheCookie(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtRefreshed)
	h := newCookieRefresh(t, fake.URL, nil)
	cookie := rtCookie("app", scCookieOnly)

	// A pre-deploy tab: no header, a body token and a stray cookie. The cookie is neither read nor written.
	rec := doSession(h, "/auth/refresh", refreshBody(refreshR0), map[string]string{"Origin": scApp}, cookie)
	if at, rt := requireAnswer(t, rec.Body.Bytes()); at != refreshA1 || rt != refreshR1 {
		t.Errorf("answer = (%q, %q), want (%q, %q)", at, rt, refreshA1, refreshR1)
	}
	if got := sentTokens(t, fake.Calls()); len(got) != 1 || got[0] != refreshR0 {
		t.Errorf("GoTrue saw %q, want only the body's %q", got, refreshR0)
	}
	requireNoSetCookie(t, rec)

	// A cross-site form POST: the cookie and no header give 400, and GoTrue is not called.
	before := fake.Hits()
	rec = doSession(h, "/auth/refresh", ``, map[string]string{"Origin": scEvil}, cookie)
	requireRefusal(t, rec, http.StatusBadRequest, msgInvalidBody)
	requireNoSetCookie(t, rec)
	if n := fake.Hits() - before; n != 0 {
		t.Errorf("GoTrue saw %d calls from a cookie without the header, want 0", n)
	}

	// Positive pair: with the header, the same cookie is read.
	if got := doCookieRefresh(h, scApp, ``, cookie); got.Code != http.StatusOK {
		t.Errorf("cookie mode with the same cookie = %d, want 200: %s", got.Code, got.Body.String())
	}
}

func TestRefresh_ReadsOnlyItsOwnAudienceCookie(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtRefreshed)

	rec := doCookieRefresh(newCookieRefresh(t, fake.URL, nil), scApp, ``, rtCookie("ops", refreshR0))

	requireRefusal(t, rec, http.StatusUnauthorized, msgRefreshRefused)
	if n := fake.Hits(); n != 0 {
		t.Errorf("GoTrue saw %d calls, want 0", n)
	}
	requireClearCookie(t, rec, "__Host-rt-app")
}

func TestRefresh_CookieModeWithoutACookieIs401AndClears(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtRefreshed)

	rec := doCookieRefresh(newCookieRefresh(t, fake.URL, nil), scApp, `{}`)

	requireRefusal(t, rec, http.StatusUnauthorized, msgRefreshRefused)
	if n := fake.Hits(); n != 0 {
		t.Errorf("GoTrue saw %d calls, want 0", n)
	}
	requireClearCookie(t, rec, "__Host-rt-app")
}

func TestRefresh_CookieModeRefusedBodyTokenKeepsTheCookie(t *testing.T) {
	fake := newTokenFake(t, http.StatusBadRequest, gtRefreshNotFound)
	h := newCookieRefresh(t, fake.URL, nil)

	rec := doCookieRefresh(h, scApp, refreshBody(refreshR0), rtCookie("app", scCookieOnly))

	requireRefusal(t, rec, http.StatusUnauthorized, msgRefreshRefused)
	if got := sentTokens(t, fake.Calls()); len(got) != 1 || got[0] != refreshR0 {
		t.Errorf("GoTrue saw %q, want only the body's %q", got, refreshR0)
	}
	requireNoSetCookie(t, rec)

	// Contrast: the cookie's own token, refused the same way, is cleared.
	requireClearCookie(t, doCookieRefresh(h, scApp, ``, rtCookie("app", scCookieOnly)), "__Host-rt-app")
}

func TestRefresh_CookieModeMapping(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		closed     bool
		wantStatus int
		wantMsg    string
		wantClear  bool
	}{
		{"400", http.StatusBadRequest, gtRefreshAlreadyUsed, false, http.StatusUnauthorized, msgRefreshRefused, true},
		{"401", http.StatusUnauthorized, `{"code":401,"msg":"unauthorized"}`, false, http.StatusUnauthorized, msgRefreshRefused, true},
		{"429", http.StatusTooManyRequests, gtOverRequestRateLimit, false, http.StatusTooManyRequests, msgTooMany, false},
		{"500", http.StatusInternalServerError, gtInternal, false, http.StatusBadGateway, msgRefreshUnavailable, false},
		{"unreachable", 0, ``, true, http.StatusBadGateway, msgRefreshUnavailable, false},
		{"200 without refresh_token", http.StatusOK, `{"access_token":"` + refreshA1 + `"}`, false, http.StatusBadGateway, msgRefreshUnavailable, false},
		{"200 without access_token", http.StatusOK, `{"refresh_token":"` + refreshR1 + `"}`, false, http.StatusBadGateway, msgRefreshUnavailable, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			authURL := closedURL(t)
			if !c.closed {
				authURL = newTokenFake(t, c.status, c.body).URL
			}

			rec := doCookieRefresh(newCookieRefresh(t, authURL, nil), scApp, ``, rtCookie("app", refreshR0))

			requireRefusal(t, rec, c.wantStatus, c.wantMsg)
			if c.wantClear {
				requireClearCookie(t, rec, "__Host-rt-app")
			} else {
				requireNoSetCookie(t, rec)
			}
		})
	}
}
