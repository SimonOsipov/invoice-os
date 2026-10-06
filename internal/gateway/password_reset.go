package gateway

import (
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// RequestPasswordResetHandler answers POST /auth/request-password-reset with one answer for every account state.
func RequestPasswordResetHandler(authURL *url.URL, client *http.Client, minResponse time.Duration, perAddress, perIP *SignInThrottle, enforce bool, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	})
}
