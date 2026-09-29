package gateway

import (
	"log/slog"
	"net/http"
	"net/url"
)

// SignOutHandler answers POST /auth/sign-out by revoking every session of the account through GoTrue.
func SignOutHandler(authURL *url.URL, client *http.Client, sessions *SessionChecker, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	})
}
