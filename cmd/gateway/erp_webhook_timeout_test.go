package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
)

// TestGatewayRoutes_ERPWebhookClientTimesOut goes through registerERPPages, so it fails if the
// route's client drops or changes gateway.ERPWebhookTimeout. It takes that long on purpose.
func TestGatewayRoutes_ERPWebhookClientTimesOut(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(gateway.ERPWebhookTimeout + 5*time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(worker.Close)
	t.Cleanup(func() { close(release) })
	workerURL, _ := url.Parse(worker.URL)

	mux := http.NewServeMux()
	if err := registerERPPages(mux, func(k string) string { return map[string]string{"ERP_FAKE": "true"}[k] }, workerURL, "gw-token", slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/erp/webhooks/fake", nil))
	elapsed := time.Since(start)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("slow worker: status %d, want 503", rec.Code)
	}
	if elapsed < gateway.ERPWebhookTimeout-time.Second || elapsed > gateway.ERPWebhookTimeout+2*time.Second {
		t.Errorf("answered after %v, want about %v", elapsed, gateway.ERPWebhookTimeout)
	}
}
