package gateway

import (
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// RequestPasswordResetHandler answers POST /auth/request-password-reset with one answer for every account state.
// It shares the resend throttle: pass the same perAddress.
func RequestPasswordResetHandler(authURL *url.URL, client *http.Client, minResponse time.Duration, perAddress *SignInThrottle, enforce bool, log *slog.Logger) http.Handler {
	return mailLinkHandler("reset-request", "recover", authURL.JoinPath("recover").String(),
		func(email string) map[string]string { return map[string]string{"email": email} },
		client, minResponse, perAddress, enforce, log)
}
