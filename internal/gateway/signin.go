package gateway

import (
	"log/slog"
	"net/http"
	"net/url"
)

// SignInHandler answers POST /auth/sign-in with a single-use exchange code.
func SignInHandler(authURL *url.URL, client *http.Client, store *HandoffStore, throttle *SignInThrottle, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	})
}

// ExchangeHandler answers POST /auth/exchange by redeeming a code for its access token.
func ExchangeHandler(store *HandoffStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	})
}
