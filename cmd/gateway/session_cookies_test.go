package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestSessionCookies_RequiredOnCloudRun(t *testing.T) {
	cors := []string{"https://a.x"}

	cookies, err := sessionCookies(envOf(nil), cors, true)

	if err == nil || !strings.Contains(err.Error(), "AUTH_SESSION_ORIGINS") {
		t.Fatalf("sessionCookies(unset, cloud run) = %v, %v, want an error naming AUTH_SESSION_ORIGINS", cookies, err)
	}

	// Positive pair: a set, valid value boots.
	cookies, err = sessionCookies(envOf(map[string]string{"AUTH_SESSION_ORIGINS": "app=https://a.x"}), cors, true)
	if err != nil || cookies == nil {
		t.Errorf("sessionCookies(valid, cloud run) = %v, %v, want cookies and no error", cookies, err)
	}
}

func TestSessionCookies_MustBeCORSAllowed(t *testing.T) {
	env := envOf(map[string]string{"AUTH_SESSION_ORIGINS": "ops=https://o.x"})

	if cookies, err := sessionCookies(env, []string{"https://a.x"}, false); err == nil {
		t.Fatalf("sessionCookies(origin outside CORS_ALLOWED_ORIGINS) = %v, nil, want an error", cookies)
	}

	// Positive pair: the same value with the origin allowed boots.
	if cookies, err := sessionCookies(env, []string{"https://a.x", "https://o.x"}, false); err != nil || cookies == nil {
		t.Errorf("sessionCookies(origin in CORS_ALLOWED_ORIGINS) = %v, %v, want cookies and no error", cookies, err)
	}
}

func TestSessionCookies_UnsetLocallyRefusesCookieMode(t *testing.T) {
	cookies, err := sessionCookies(envOf(nil), []string{"https://a.x"}, false)
	if err != nil || cookies != nil {
		t.Fatalf("sessionCookies(unset, local) = %v, %v, want nil, nil", cookies, err)
	}

	// With nil cookies every route answers a cookie-mode request 403 before its store or GoTrue.
	authURL := &url.URL{Scheme: "http", Host: "127.0.0.1:1"}
	log := slog.New(slog.DiscardHandler)
	h := handoffHandlers(authURL, nil, gateway.NewSessionChecker(nil, nil, time.Now, log), log, nil, nil, nil, cookies, nil)
	for path, handler := range map[string]http.Handler{"/auth/exchange": h.Exchange, "/auth/refresh": h.Refresh, "/auth/sign-out": h.SignOut} {
		for _, origin := range []string{"https://a.x", "https://evil.example", ""} {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"code":"c","state":"s"}`))
			req.Header.Set("X-Auth-Session", "cookie")
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || len(rec.Header().Values("Set-Cookie")) != 0 {
				t.Errorf("POST %s from %q = %d %q, want 403 and no cookie", path, origin, rec.Code, rec.Body.String())
			}
		}
	}
}
