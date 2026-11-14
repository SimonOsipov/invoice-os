package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
)

// RefreshHandler answers POST /auth/refresh by renewing a session through GoTrue.
// ceiling: no rate limit; revisit when a per-client-IP limit lands.
func RefreshHandler(authURL *url.URL, client *http.Client, log *slog.Logger, cookies *SessionCookies) http.Handler {
	tokenURL := authURL.JoinPath("token")
	// GoTrue reads grant_type through FormValue, so it rides the query beside a JSON body.
	tokenURL.RawQuery = "grant_type=refresh_token"
	token := tokenURL.String()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !postOnly(w, r) {
			return
		}
		aud, cookieMode, err := cookies.mode(r)
		if err != nil {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		var in struct {
			RefreshToken string `json:"refresh_token"`
		}
		// Cookie mode accepts an empty body; a body token is legacy adoption.
		// ceiling: body mode for a browser Origin serves pre-deploy tabs, remove 30 days after the production deploy
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExchangeBodyBytes)).Decode(&in); err != nil && !(cookieMode && errors.Is(err, io.EOF)) {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		fromBody := in.RefreshToken != ""
		if cookieMode && !fromBody {
			in.RefreshToken = cookies.read(r, aud)
			if in.RefreshToken == "" {
				cookies.clear(w, r, aud)
				writeError(w, http.StatusUnauthorized, "invalid or expired refresh token")
				return
			}
		}
		if in.RefreshToken == "" {
			writeError(w, http.StatusBadRequest, "refresh_token is required")
			return
		}

		var sess struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		}
		status, _, err := postGoTrue(r, client, token, map[string]string{"refresh_token": in.RefreshToken}, &sess)
		switch {
		case err != nil:
			log.WarnContext(r.Context(), "refresh: gotrue unreachable", slog.String("error", err.Error()))
			writeError(w, http.StatusBadGateway, "renewal is unavailable")
		case status == http.StatusOK && sess.AccessToken != "" && sess.RefreshToken != "":
			if cookieMode {
				cookies.set(w, r, aud, sess.RefreshToken)
				writeJSON(w, http.StatusOK, map[string]string{"access_token": sess.AccessToken})
				return
			}
			writeJSON(w, http.StatusOK, sess)
		case status == http.StatusTooManyRequests:
			writeError(w, http.StatusTooManyRequests, "too many requests")
		// The client cannot repair any 4xx, so every reason answers the same bytes.
		case status >= 400 && status < 500:
			// A refused body token may not be the cookie's: leave the cookie, which may hold another tab's live token.
			if cookieMode && !fromBody {
				cookies.clear(w, r, aud)
			}
			writeError(w, http.StatusUnauthorized, "invalid or expired refresh token")
		default:
			log.WarnContext(r.Context(), "refresh: gotrue token failed", slog.Int("upstream_status", status))
			writeError(w, http.StatusBadGateway, "renewal is unavailable")
		}
	})
}
