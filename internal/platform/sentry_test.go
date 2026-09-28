package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

// mockTransport records events instead of sending them over the network.
type mockTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (t *mockTransport) Configure(sentry.ClientOptions) {}

func (t *mockTransport) SendEvent(e *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, e)
}

func (t *mockTransport) Flush(time.Duration) bool { return true }

func (t *mockTransport) FlushWithContext(context.Context) bool { return true }

func (t *mockTransport) Close() {}

func (t *mockTransport) captured() []*sentry.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*sentry.Event(nil), t.events...)
}

func TestInitSentryDisabled(t *testing.T) {
	if err := initSentry(Config{Service: "svc"}); err != nil {
		t.Fatalf("initSentry with empty DSN should be a no-op, got: %v", err)
	}
	// Capture must be safe (no panic, no send) while disabled.
	CaptureError(context.Background(), errors.New("ignored"))
	capturePanic(context.Background(), "ignored")
}

func TestCaptureErrorTagsIDs(t *testing.T) {
	mt := &mockTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:       "https://public@example.com/1",
		Transport: mt,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	sentry.CurrentHub().BindClient(client)
	defer sentry.CurrentHub().BindClient(nil)

	ctx := WithTenantID(WithRequestID(context.Background(), "req-7"), "tnt-8")
	CaptureError(ctx, errors.New("boom"))
	sentry.Flush(time.Second)

	events := mt.captured()
	if len(events) != 1 {
		t.Fatalf("captured %d events, want 1", len(events))
	}
	if got := events[0].Tags["request_id"]; got != "req-7" {
		t.Errorf("request_id tag = %q, want req-7", got)
	}
	if got := events[0].Tags["tenant_id"]; got != "tnt-8" {
		t.Errorf("tenant_id tag = %q, want tnt-8", got)
	}
}

func TestCaptureErrorNil(t *testing.T) {
	// Must be a no-op even with a live client.
	mt := &mockTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{Dsn: "https://public@example.com/1", Transport: mt})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	sentry.CurrentHub().BindClient(client)
	defer sentry.CurrentHub().BindClient(nil)

	CaptureError(context.Background(), nil)
	if n := len(mt.captured()); n != 0 {
		t.Errorf("captured %d events for nil error, want 0", n)
	}
}

// healthzSentry returns /healthz's raw sentry value, whether the key is present,
// and the body for messages.
func healthzSentry(t *testing.T, h http.Handler) (any, bool, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if len(body) == 0 {
		t.Fatalf("/healthz returned an empty object (%q)", rec.Body.String())
	}
	v, ok := body["sentry"]
	return v, ok, rec.Body.String()
}

func assertHealthzSentry(t *testing.T, h http.Handler, want string) {
	t.Helper()
	got, ok, body := healthzSentry(t, h)
	if !ok {
		t.Fatalf("/healthz has no sentry key, want %q (body %s)", want, body)
	}
	if got != want {
		t.Errorf("/healthz sentry = %v, want %q (body %s)", got, want, body)
	}
}

// The deploy gate reads this key to prove a PR fork sends nothing to Sentry.
func TestHealthzCarriesSentryState(t *testing.T) {
	h := http.HandlerFunc(healthzHandler)

	t.Run("no_client", func(t *testing.T) {
		sentry.CurrentHub().BindClient(nil)
		t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })

		assertHealthzSentry(t, h, "off")
	})

	t.Run("dsn_client", func(t *testing.T) {
		client, err := sentry.NewClient(sentry.ClientOptions{Dsn: "https://public@example.com/1", Transport: &mockTransport{}})
		if err != nil {
			t.Fatalf("new client: %v", err)
		}
		sentry.CurrentHub().BindClient(client)
		t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })

		assertHealthzSentry(t, h, "on")
	})

	// sentry-go binds a no-op client for an empty DSN, so a bound client alone is not "on".
	t.Run("empty_dsn_client", func(t *testing.T) {
		t.Setenv("SENTRY_DSN", "")
		client, err := sentry.NewClient(sentry.ClientOptions{})
		if err != nil {
			t.Fatalf("new client: %v", err)
		}
		if client == nil || client.Options().Dsn != "" {
			t.Fatalf("precondition: want a client with an empty DSN, got %+v", client)
		}
		sentry.CurrentHub().BindClient(client)
		t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })
		if sentry.CurrentHub().Client() == nil {
			t.Fatal("precondition: the no-op client did not bind")
		}

		assertHealthzSentry(t, h, "off")
	})
}

// A service's own boot path decides the state: New reads SENTRY_DSN, nothing else does.
func TestNewServesSentryStateFromTheDSN(t *testing.T) {
	for _, c := range []struct {
		name, dsn, want string
	}{
		{"empty_dsn", "", "off"},
		{"valid_dsn", "https://public@example.com/1", "on"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("SENTRY_DSN", c.dsn)
			sentry.CurrentHub().BindClient(nil)
			t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })

			app, err := New("svc")
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			assertHealthzSentry(t, app.Mux, c.want)
		})
	}
}
