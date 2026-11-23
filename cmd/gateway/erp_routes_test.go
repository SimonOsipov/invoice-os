package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

// erpRouteState is a well-formed state: the tenant uuid, a dot and 43 base64url characters.
func erpRouteState() string {
	return uuid.NewString() + "." + strings.Repeat("Ab3-_", 8) + "Cd4"
}

func TestGatewayRoutes_ERPPagesFollowTheMode(t *testing.T) {
	var workerCalls atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"outcome\":\"connected\"}"))
	}))
	t.Cleanup(worker.Close)
	workerURL, _ := url.Parse(worker.URL)

	cases := []struct {
		name    string
		env     map[string]string
		pages   bool // both pages are registered
		wantErr bool
	}{
		{"production", map[string]string{"DEPLOY_ENV": "production"}, false, false},
		{"production with the fake flag off", map[string]string{"DEPLOY_ENV": "production", "ERP_FAKE": "false"}, false, false},
		{"a preview", map[string]string{"DEPLOY_ENV": "pr-3"}, true, false},
		{"the nightly", map[string]string{"DEPLOY_ENV": "nightly"}, true, false},
		{"local without the flag", map[string]string{}, false, false},
		{"local with the flag", map[string]string{"ERP_FAKE": "true"}, true, false},
		{"production with the fake flag on", map[string]string{"DEPLOY_ENV": "production", "ERP_FAKE": "true"}, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mux := http.NewServeMux()
			err := registerERPPages(mux, func(k string) string { return c.env[k] }, workerURL, "gw-token", slog.New(slog.DiscardHandler))
			if (err != nil) != c.wantErr {
				t.Fatalf("registerERPPages err = %v, want error %v", err, c.wantErr)
			}
			state := erpRouteState()
			do := func(method, target string) *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
				return rec
			}
			hookBefore := workerCalls.Load()
			hook := do(http.MethodPost, "/erp/webhooks/fake")
			if forwarded := workerCalls.Load() == hookBefore+1; forwarded != c.pages || (!c.pages && hook.Code != http.StatusNotFound) {
				t.Errorf("POST /erp/webhooks/fake = %d, forwarded %v, want forwarded exactly where the pages are mounted (404 elsewhere)", hook.Code, forwarded)
			}
			callback := do(http.MethodGet, "/erp/callback?state="+state+"&code=fake-co-1")
			consent := do(http.MethodGet, "/erp/fake/consent?state="+state)
			if !c.pages {
				if callback.Code != http.StatusNotFound || consent.Code != http.StatusNotFound {
					t.Errorf("GET /erp/callback = %d, GET /erp/fake/consent = %d, want both 404", callback.Code, consent.Code)
				}
				return
			}
			if callback.Code != http.StatusOK || !strings.Contains(callback.Body.String(), "data-outcome=\"connected\"") {
				t.Errorf("GET /erp/callback = %d, want the worker's outcome page (200 connected)", callback.Code)
			}
			if consent.Code != http.StatusOK || !strings.Contains(consent.Body.String(), "name=\"state\"") {
				t.Errorf("GET /erp/fake/consent = %d, want the fake consent form", consent.Code)
			}
			if bad := do(http.MethodGet, "/erp/fake/consent?state=x"); bad.Code != http.StatusBadRequest {
				t.Errorf("GET /erp/fake/consent with a malformed state = %d, want 400", bad.Code)
			}
			if bad := do(http.MethodGet, "/erp/callback?state=x"); bad.Code != http.StatusBadRequest {
				t.Errorf("GET /erp/callback with a malformed state = %d, want 400", bad.Code)
			}
			for _, path := range []string{"/erp/callback?state=" + state, "/erp/fake/consent?state=" + state} {
				if rec := do(http.MethodPost, path); rec.Code != http.StatusMethodNotAllowed {
					t.Errorf("POST %s = %d, want 405", path, rec.Code)
				}
			}
		})
	}
	if workerCalls.Load() == 0 {
		t.Error("no case reached the worker: the callback page is not wired to the worker URL")
	}
}
