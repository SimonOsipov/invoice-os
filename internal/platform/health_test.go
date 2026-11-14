package platform

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	healthzHandler(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("body = %q, want status ok", rec.Body.String())
	}
}

func TestReadyzNoChecks(t *testing.T) {
	rd := &readiness{}
	rec := httptest.NewRecorder()
	rd.readyzHandler(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 with no checks", rec.Code)
	}
}

func TestReadyzPassing(t *testing.T) {
	rd := &readiness{}
	rd.add("db", func(context.Context) error { return nil })
	rec := httptest.NewRecorder()
	rd.readyzHandler(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 when checks pass", rec.Code)
	}
}

func TestReadyzFailing(t *testing.T) {
	rd := &readiness{}
	rd.add("db", func(context.Context) error { return errors.New("connection refused") })
	rd.add("queue", func(context.Context) error { return nil })
	rec := httptest.NewRecorder()
	rd.readyzHandler(rec, httptest.NewRequest("GET", "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when a check fails", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "db") || !strings.Contains(body, "connection refused") {
		t.Errorf("body = %q, want the failing dependency + reason", body)
	}
}

// newHealthApp builds a service app with the package health vars pinned; the vars are globals, so these tests stay serial.
func newHealthApp(t *testing.T, service string) *App {
	t.Helper()
	t.Setenv("SENTRY_DSN", "")
	prevLog, prevMock, prevIssuers, prevContacts := slog.Default(), MockIssuer, AuthIssuers, Contacts
	t.Cleanup(func() {
		slog.SetDefault(prevLog)
		MockIssuer, AuthIssuers, Contacts = prevMock, prevIssuers, prevContacts
	})
	MockIssuer, AuthIssuers, Contacts = "absent", "1", "fake"
	app, err := New(service)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// getHealth serves GET path through app.Handler(); token == nil sends no X-Gateway-Token header.
func getHealth(t *testing.T, app *App, path string, token *string) (int, map[string]string, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if token != nil {
		req.Header.Set(HeaderGatewayToken, *token)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET %s: decode %q: %v", path, rec.Body.String(), err)
	}
	return rec.Code, body, strings.TrimSpace(rec.Body.String())
}

const statusOnlyBody = `{"status":"ok"}`

func requireStatusOnly(t *testing.T, what string, code int, raw string) {
	t.Helper()
	if code != http.StatusOK {
		t.Errorf("%s: status = %d, want 200", what, code)
	}
	if raw != statusOnlyBody {
		t.Errorf("%s: body = %s, want exactly %s", what, raw, statusOnlyBody)
	}
}

func requireFullDetail(t *testing.T, what string, code int, body map[string]string) {
	t.Helper()
	if code != http.StatusOK {
		t.Fatalf("%s: status = %d, want 200", what, code)
	}
	if BuildSHA == "" {
		t.Fatal("BuildSHA is empty; the build assertion would be vacuous")
	}
	want := map[string]string{"status": "ok", "build": BuildSHA, "mock_issuer": "absent", "auth_issuers": "1", "contacts": "fake", "sentry": SentryState()}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s: %s = %q, want %q", what, k, body[k], v)
		}
	}
}

func TestHideHealthDetail_AnonymousCallerReadsStatusOnly(t *testing.T) {
	app := newHealthApp(t, "gateway")
	app.HideHealthDetail("tok")
	for _, path := range []string{"/health", "/healthz"} {
		code, body, raw := getHealth(t, app, path, nil)
		requireStatusOnly(t, "anonymous GET "+path, code, raw)
		if len(body) != 1 || body["status"] != "ok" {
			t.Errorf("anonymous GET %s: keys = %v, want only status=ok", path, body)
		}
		for _, k := range []string{"build", "mock_issuer", "auth_issuers", "sentry", "contacts"} {
			if _, present := body[k]; present {
				t.Errorf("anonymous GET %s: key %q is present, want it absent", path, k)
			}
		}
	}
}

// The token row in TestHideHealthDetail_TokenHolderReadsBuildDetail differs only in the header value, so tokenMatches is the only clause that rejects here.
func TestHideHealthDetail_WrongTokenReadsStatusOnly(t *testing.T) {
	app := newHealthApp(t, "gateway")
	app.HideHealthDetail("tok")
	wrong := []string{"tokx", "xtok", "to", "TOK", "tak", ""}
	if len(wrong) == 0 {
		t.Fatal("no wrong tokens to try")
	}
	for _, supplied := range wrong {
		for _, path := range []string{"/health", "/healthz"} {
			code, _, raw := getHealth(t, app, path, &supplied)
			requireStatusOnly(t, "GET "+path+" with token "+supplied, code, raw)
		}
	}
}

func TestHideHealthDetail_TokenHolderReadsBuildDetail(t *testing.T) {
	app := newHealthApp(t, "gateway")
	app.HideHealthDetail("tok")
	tok := "tok"
	for _, path := range []string{"/health", "/healthz"} {
		code, body, _ := getHealth(t, app, path, &tok)
		requireFullDetail(t, "token GET "+path, code, body)
	}
}

// Backends never call HideHealthDetail; the fleet probe reads their build with no token.
func TestHealth_ServiceWithoutHideHealthDetailKeepsTheFullBody(t *testing.T) {
	app := newHealthApp(t, "svc")
	app.RequireGateway("tok")
	tok := "tok"
	for _, header := range []*string{nil, &tok} {
		for _, path := range []string{"/health", "/healthz"} {
			code, body, _ := getHealth(t, app, path, header)
			requireFullDetail(t, "GET "+path+" without HideHealthDetail", code, body)
		}
	}
}

func TestHideHealthDetail_PanicsOnAnEmptyToken(t *testing.T) {
	app := newHealthApp(t, "gateway")
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("HideHealthDetail(\"\") did not panic, want a panic")
		}
		if msg, _ := r.(string); !strings.Contains(msg, "HideHealthDetail") || !strings.Contains(msg, "empty token") {
			t.Errorf("panic = %v, want a message naming HideHealthDetail and the empty token", r)
		}
	}()
	app.HideHealthDetail("")
}

func TestHideHealthDetail_StaysOpenBehindRequireGateway(t *testing.T) {
	app := newHealthApp(t, "gateway")
	app.RequireGateway("tok")
	app.HideHealthDetail("tok")
	code, _, raw := getHealth(t, app, "/health", nil)
	requireStatusOnly(t, "anonymous GET /health behind RequireGateway", code, raw)
	tok := "tok"
	code, body, _ := getHealth(t, app, "/health", &tok)
	requireFullDetail(t, "token GET /health behind RequireGateway", code, body)
}
