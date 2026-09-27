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
	return RefreshHandler(authURL, testClient(), log)
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
			rec := doRefresh(newRefresh(fake.URL, nil), refreshBody(refreshR0))
			requireRefusal(t, rec, c.wantStatus, c.wantMsg)
			if n := fake.Hits(); n != 1 {
				t.Errorf("GoTrue saw %d calls, want 1", n)
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
		requireRefusal(t, doRefresh(newRefresh(closedURL(t), nil), refreshBody(refreshR0)), http.StatusBadGateway, msgRefreshUnavailable)
	})
}

func TestRefresh_BadBody400NoUpstreamCall(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtRefreshed)
	h := newRefresh(fake.URL, nil)

	oversized := refreshBody(strings.Repeat("r", maxExchangeBodyBytes))
	for _, c := range []struct{ name, body, msg string }{
		{"not json", `not json`, msgInvalidBody},
		{"over 1 KiB", oversized, msgInvalidBody},
		{"empty object", `{}`, msgRefreshRequired},
		{"empty token", `{"refresh_token":""}`, msgRefreshRequired},
	} {
		requireRefusal(t, doRefresh(h, c.body), http.StatusBadRequest, c.msg)
	}
	if n := fake.Hits(); n != 0 {
		t.Errorf("GoTrue saw %d calls from refused bodies, want 0", n)
	}

	// Positive pair: a well-formed body on the same handler does reach GoTrue.
	if rec := doRefresh(h, refreshBody(refreshR0)); rec.Code != http.StatusOK {
		t.Errorf("well-formed body: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if n := fake.Hits(); n != 1 {
		t.Errorf("GoTrue saw %d calls after one well-formed body, want 1", n)
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
