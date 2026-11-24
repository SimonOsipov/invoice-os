package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

const (
	erpWebhookMaxBody   = 64 << 10
	erpWebhookMaxHeader = 16 << 10         // of the JSON encoding, so the envelope stays under the worker's 128 KiB
	ERPWebhookTimeout   = 10 * time.Second // the client timeout cmd/gateway sets; past it the vendor gets 503 and retries
)

// erpWebhookDropped never reach the worker: they would carry the caller's address or a session.
var erpWebhookDropped = []string{"Cookie", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Real-Ip", "Via"}

// ERPWebhookHandler forwards a vendor's change notification to the worker, which verifies the
// signature. It reads no database and logs no body, header or address.
//
// ceiling: no throttle or Armor rule; mounted only when the ERP mode is fake.
func ERPWebhookHandler(worker *url.URL, client *http.Client, gatewayToken, system string, log *slog.Logger) http.Handler {
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	target := worker.JoinPath("internal", "erp", "webhooks", system).String()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, erpWebhookMaxBody))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeError(w, http.StatusRequestEntityTooLarge, "request too large")
			} else {
				writeError(w, http.StatusBadRequest, "unreadable request")
			}
			return
		}
		headers := r.Header.Clone()
		for _, h := range erpWebhookDropped {
			headers.Del(h)
		}
		encoded, err := json.Marshal(headers)
		if err != nil || len(encoded) > erpWebhookMaxHeader {
			writeError(w, http.StatusRequestHeaderFieldsTooLarge, "headers too large")
			return
		}
		raw, err := json.Marshal(struct {
			Headers http.Header `json:"headers"`
			Body    []byte      `json:"body"`
		}{headers, body})
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "try again later")
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(raw))
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "try again later")
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Gateway-Token", gatewayToken)
		resp, err := c.Do(req)
		if err != nil {
			log.WarnContext(r.Context(), "erp-webhook: worker call failed")
			writeError(w, http.StatusServiceUnavailable, "try again later")
			return
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			w.WriteHeader(http.StatusOK)
		case http.StatusBadRequest:
			writeError(w, http.StatusBadRequest, "bad request")
		case http.StatusUnauthorized:
			writeError(w, http.StatusUnauthorized, "unauthorized")
		case http.StatusNotFound:
			writeError(w, http.StatusNotFound, "not found")
		case http.StatusRequestEntityTooLarge:
			writeError(w, http.StatusRequestEntityTooLarge, "request too large")
		default:
			log.WarnContext(r.Context(), "erp-webhook: worker answered unusably", slog.Int("upstream_status", resp.StatusCode))
			writeError(w, http.StatusServiceUnavailable, "try again later")
		}
	})
}
