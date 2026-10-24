package platform

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"
)

// authClient binds the client InitSentry builds for service onto a recording transport.
func authClient(t *testing.T, service string) *mockTransport {
	t.Helper()
	sentry.CurrentHub().BindClient(nil)
	mt := &mockTransport{}
	cfg := Config{Service: service, Environment: "test", SentryEnvironment: "test", SentryDSN: filterTestDSN}
	if err := InitSentry(cfg, mt); err != nil {
		t.Fatalf("InitSentry: %v", err)
	}
	t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })
	return mt
}

func captureWithUser(t *testing.T, mt *mockTransport, u sentry.User) *sentry.Event {
	t.Helper()
	hub := sentry.CurrentHub().Clone()
	hub.ConfigureScope(func(s *sentry.Scope) { s.SetUser(u) })
	hub.CaptureException(&testErr{"boom"})
	return oneEvent(t, mt, "")
}

type testErr struct{ s string }

func (e *testErr) Error() string { return e.s }

func TestAuthFilter_KeepsOnlyTheEmail(t *testing.T) {
	mt := authClient(t, AuthService)
	ev := captureWithUser(t, mt, sentry.User{
		ID: "u1", Email: "a@b.c", IPAddress: "1.2.3.4", Username: "un", Name: "N",
		Data: map[string]string{"k": "v"},
	})
	if ev.User.Email != "a@b.c" {
		t.Errorf("Email = %q, want a@b.c", ev.User.Email)
	}
	if ev.User.ID != "" || ev.User.IPAddress != "" || ev.User.Username != "" || ev.User.Name != "" || len(ev.User.Data) != 0 {
		t.Errorf("User carries more than the email: %+v", ev.User)
	}
}

func TestAuthFilter_GoServicesNeverCarryAnEmail(t *testing.T) {
	services := []string{"gateway", "tenancy", "portfolio", "invoice", "validation", "dashboard", "submission", "notifications", "worker", "migrate"}
	for _, svc := range services {
		t.Run(svc, func(t *testing.T) {
			mt := authClient(t, svc)
			if ev := captureWithUser(t, mt, sentry.User{Email: "a@b.c"}); !ev.User.IsEmpty() {
				t.Errorf("%s event carries User %+v, want empty", svc, ev.User)
			}
		})
	}
	t.Run("control: auth keeps it", func(t *testing.T) {
		mt := authClient(t, AuthService)
		if ev := captureWithUser(t, mt, sentry.User{Email: "a@b.c"}); ev.User.Email != "a@b.c" {
			t.Errorf("auth Email = %q, want a@b.c", ev.User.Email)
		}
	})
}

func TestAuthFilter_AnEventClaimingAuthOnAGoClientLosesTheEmail(t *testing.T) {
	mt := authClient(t, "gateway")
	hub := sentry.CurrentHub().Clone()
	hub.CaptureEvent(&sentry.Event{Message: "x", ServerName: AuthService, User: sentry.User{Email: "a@b.c"}})
	if ev := oneEvent(t, mt, ""); !ev.User.IsEmpty() {
		t.Errorf("User = %+v, want empty", ev.User)
	}
}

func TestAuthFilter_KeepsEveryOtherScrub(t *testing.T) {
	mt := authClient(t, AuthService)
	req := httptest.NewRequest(http.MethodPost, "/token?q="+markerTIN, strings.NewReader(markerAmt))
	req.AddCookie(&http.Cookie{Name: "c", Value: markerCred})
	req.Header.Set("X-S2S-Token", markerCred)
	hub := sentry.CurrentHub().Clone()
	hub.ConfigureScope(func(s *sentry.Scope) {
		s.SetRequest(req)
		s.SetUser(sentry.User{Email: "a@b.c"})
	})
	hub.CaptureException(&testErr{`bad value ` + `"` + markerIRN + `"`})
	assertNoLeak(t, mt.captured())
	if ev := oneEvent(t, mt, ""); ev.User.Email != "a@b.c" {
		t.Errorf("Email = %q, want a@b.c", ev.User.Email)
	}
}

func TestAuthFilter_DropsAnInvalidEmail(t *testing.T) {
	cases := []struct {
		name, email string
		keep        bool
	}{
		{"empty", "", false},
		{"space", "a b@c", false},
		{"no at", "x", false},
		{"two ats", "a@b@c", false},
		{"quote", `"q"@x`, false},
		{"255 bytes", strings.Repeat("a", 253) + "@b", false},
		{"254 bytes", strings.Repeat("a", 252) + "@b", true},
		{"control", "ok@example.com", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mt := authClient(t, AuthService)
			ev := captureWithUser(t, mt, sentry.User{Email: c.email})
			if c.keep && ev.User.Email != c.email {
				t.Errorf("Email = %q, want kept", ev.User.Email)
			}
			if !c.keep && !ev.User.IsEmpty() {
				t.Errorf("User = %+v, want empty", ev.User)
			}
		})
	}
}

func TestInitSentry_EmptyDSNSendsNothing(t *testing.T) {
	sentry.CurrentHub().BindClient(nil)
	t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })
	rec := &mockTransport{}
	if err := InitSentry(Config{Service: AuthService}, rec); err != nil {
		t.Fatalf("InitSentry: %v", err)
	}
	if got := SentryState(); got != "off" {
		t.Errorf("SentryState = %q, want off", got)
	}
	sentry.CaptureException(&testErr{"boom"})
	if n := len(rec.captured()); n != 0 {
		t.Errorf("transport holds %d events, want 0", n)
	}

	rec = authClient(t, AuthService)
	sentry.CaptureException(&testErr{"boom"})
	if n := len(rec.captured()); n != 1 {
		t.Errorf("control: transport holds %d events, want 1", n)
	}
}

func TestAuthFilter_SendsNoTransaction(t *testing.T) {
	for _, c := range []struct {
		service string
		want    int
	}{{"gateway", 1}, {AuthService, 0}} {
		t.Run(c.service, func(t *testing.T) {
			mt := authClient(t, c.service)
			sentry.StartTransaction(t.Context(), "op").Finish()
			if got := len(eventsOfType(mt, "transaction")); got != c.want {
				t.Errorf("%s transactions = %d, want %d", c.service, got, c.want)
			}
		})
	}
}
