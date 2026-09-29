package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/golang-jwt/jwt/v4"
)

// SignOutHandler answers POST /auth/sign-out by revoking every session of the account through GoTrue.
// GoTrue's /logout needs a live access token, so a refresh grant mints one first.
func SignOutHandler(authURL *url.URL, client *http.Client, sessions *SessionChecker, log *slog.Logger) http.Handler {
	tokenURL := authURL.JoinPath("token")
	tokenURL.RawQuery = "grant_type=refresh_token"
	token := tokenURL.String()
	logoutURL := authURL.JoinPath("logout")
	logoutURL.RawQuery = "scope=global"
	logout := logoutURL.String()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !postOnly(w, r) {
			return
		}
		var in struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExchangeBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if in.RefreshToken == "" {
			writeError(w, http.StatusBadRequest, "refresh_token is required")
			return
		}

		var sess struct {
			AccessToken string `json:"access_token"`
		}
		status, _, err := postGoTrue(r, client, token, map[string]string{"refresh_token": in.RefreshToken}, &sess)
		switch {
		case err != nil:
			log.WarnContext(r.Context(), "sign-out: gotrue refresh grant unreachable", slog.String("error", err.Error()))
			writeError(w, http.StatusBadGateway, "sign-out is unavailable")
			return
		case status == http.StatusOK && sess.AccessToken != "":
		case status == http.StatusTooManyRequests:
			writeError(w, http.StatusTooManyRequests, "too many requests")
			return
		case status >= 400 && status < 500:
			writeError(w, http.StatusUnauthorized, "invalid or expired refresh token")
			return
		default:
			log.WarnContext(r.Context(), "sign-out: gotrue refresh grant failed", slog.Int("upstream_status", status))
			writeError(w, http.StatusBadGateway, "sign-out is unavailable")
			return
		}

		// Unverified: the token is GoTrue's own answer over the private network, not caller input.
		var claims jwt.RegisteredClaims
		if _, _, err := jwt.NewParser().ParseUnverified(sess.AccessToken, &claims); err != nil || claims.Subject == "" {
			log.WarnContext(r.Context(), "sign-out: access token names no subject; nothing will be evicted")
			claims.Subject = ""
		}

		// The grant has rotated the refresh token, so a client that gives up must not cancel the logout.
		status, gone, err := postGoTrueBearer(context.WithoutCancel(r.Context()), client, logout, sess.AccessToken)
		switch {
		case err != nil:
			log.WarnContext(r.Context(), "sign-out: gotrue logout unreachable", slog.String("error", err.Error()))
			writeError(w, http.StatusBadGateway, "sign-out is unavailable")
		case status == http.StatusTooManyRequests:
			writeError(w, http.StatusTooManyRequests, "too many requests")
		// gone: a 401/403 naming a gone-code; the session vanished between the two calls, which sign-out wants.
		case status >= 200 && status < 300, gone:
			if claims.Subject != "" {
				sessions.EvictSubject(claims.Subject)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			log.WarnContext(r.Context(), "sign-out: gotrue logout failed", slog.Int("upstream_status", status))
			writeError(w, http.StatusBadGateway, "sign-out is unavailable")
		}
	})
}
