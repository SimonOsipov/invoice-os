package gateway

import (
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// RequestPasswordResetHandler answers POST /auth/request-password-reset with one answer for every account state.
// It shares the resend throttle: pass the same perAddress. A non-nil reset bounds each client IP and the instance's reset mail.
func RequestPasswordResetHandler(authURL *url.URL, client *http.Client, minResponse time.Duration, perAddress *SignInThrottle, enforce bool, log *slog.Logger, events AccountEvents, reset *ResetMail) http.Handler {
	return mailLinkHandler("reset-request", "recover", authURL.JoinPath("recover").String(),
		func(email string) map[string]string { return map[string]string{"email": email} },
		client, minResponse, perAddress, enforce, log,
		func(r *http.Request, email string) {
			recordAccountEvent(r, events, log, AccountEvent{Event: EventPasswordResetRequested, Email: email})
		}, nil, reset)
}
