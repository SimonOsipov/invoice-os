package gateway

import (
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

const (
	ResendPerAddress = 3
	ResendPerIP      = 10
	ResendWindow     = time.Hour
	ResendMaxKeys    = 10_000
)

// clientKey is a stub until the resend handler lands.
func clientKey(*http.Request) (key, source string) { return "", "" }

// ResendVerificationHandler is a stub until the resend handler lands.
func ResendVerificationHandler(authURL *url.URL, client *http.Client, minResponse time.Duration, perAddress, perIP *SignInThrottle, enforce bool, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	})
}
