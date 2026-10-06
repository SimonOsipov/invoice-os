package gateway

import (
	"log/slog"
	"net/http"
	"net/url"
)

// ResetPasswordPageHandler serves the set-new-password page; it holds no GoTrue client, so opening the link spends nothing.
func ResetPasswordPageHandler(siteURL *url.URL) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	})
}

// ResetPasswordHandler answers the page's form POST: verify, set the password, end every session.
func ResetPasswordHandler(authURL, siteURL *url.URL, client *http.Client, sessions *SessionChecker, signIn *SignInThrottle, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	})
}
