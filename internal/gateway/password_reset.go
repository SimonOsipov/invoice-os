package gateway

import (
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// RequestPasswordResetHandler answers POST /auth/request-password-reset with one answer for every account state.
// It shares the resend throttles: pass the same perAddress and perIP.
func RequestPasswordResetHandler(authURL *url.URL, client *http.Client, minResponse time.Duration, perAddress, perIP *SignInThrottle, enforce bool, log *slog.Logger) http.Handler {
	return mailLinkHandler("reset-request", "recover", authURL.JoinPath("recover").String(),
		func(email string) map[string]string { return map[string]string{"email": email} },
		client, minResponse, perAddress, perIP, enforce, log)
}
