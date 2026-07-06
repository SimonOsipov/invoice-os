package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewRegistersHealthAndMiddleware(t *testing.T) {
	t.Setenv("SENTRY_DSN", "")
	app, err := New("tenancy")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("expected X-Request-ID header from the middleware chain")
	}
}

func TestAppRoutesAndReadiness(t *testing.T) {
	app, err := New("svc")
	if err != nil {
		t.Fatal(err)
	}
	app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	app.Ready("dep", func(context.Context) error { return nil })

	rec := httptest.NewRecorder()
	app.handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/ping", nil))
	if rec.Code != http.StatusNoContent {
		t.Errorf("/v1/ping status = %d, want 204", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	app.handler().ServeHTTP(rec2, httptest.NewRequest("GET", "/readyz", nil))
	if rec2.Code != http.StatusOK {
		t.Errorf("/readyz status = %d, want 200", rec2.Code)
	}
}

func TestRunGracefulShutdown(t *testing.T) {
	t.Setenv("PORT", "0") // ephemeral port
	app, err := New("svc")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	time.Sleep(100 * time.Millisecond) // give the listener time to bind
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error on graceful shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not shut down within 5s of cancellation")
	}
}
