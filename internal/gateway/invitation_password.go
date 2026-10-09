package gateway

import (
	_ "embed"
	"log/slog"
	"net/http"
	"net/url"
)

// Filled by strings.NewReplacer, not html/template: see verifyPageHTML.
//
//go:embed invitation_password.html
var invitationPageHTML string

// InvitationPasswordHandler is a stub until RESEND2-01-04 lands.
func InvitationPasswordHandler(authURL, siteURL *url.URL, client *http.Client, sessions *SessionChecker, signIn *SignInThrottle, sink ContactSink, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	})
}
