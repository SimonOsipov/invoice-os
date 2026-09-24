package gateway

import (
	"log/slog"
	"net/http"
	"net/url"
)

// RegisterHandler answers POST /auth/register by calling GoTrue's /signup under authURL.
func RegisterHandler(authURL *url.URL, client *http.Client, log *slog.Logger) http.Handler {
	return notImplemented()
}

// VerifyHandler answers the emailed link by calling GoTrue's /verify, then redirects to siteURL.
func VerifyHandler(authURL, siteURL *url.URL, client *http.Client, log *slog.Logger) http.Handler {
	return notImplemented()
}

// notImplemented is the scaffold body; it calls no upstream.
func notImplemented() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	})
}
