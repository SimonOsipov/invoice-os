package platform

import (
	"context"
	"errors"
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
