package gateway

import (
	"log/slog"
	"net/http"
	"net/url"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// IAPSessionHandler is a compile stub for the red tests (CSSO-01-04); the executor replaces it.
// It answers POST /auth/iap-session: a verified IAP assertion becomes a hand-off code.
func IAPSessionHandler(v *auth.IAPVerifier, worker *url.URL, workerClient *http.Client, gatewayToken string, authURL *url.URL, gotrue *http.Client, store Handoffs, log *slog.Logger, events AccountEvents) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	})
}
