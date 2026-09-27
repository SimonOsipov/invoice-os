package gateway

import (
	"log/slog"
	"net/http"
	"net/url"
)

// RefreshHandler answers POST /auth/refresh by renewing a session through GoTrue.
func RefreshHandler(authURL *url.URL, client *http.Client, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	})
}
