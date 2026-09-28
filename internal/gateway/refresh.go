package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
)

// RefreshHandler answers POST /auth/refresh by renewing a session through GoTrue.
// ceiling: no rate limit; revisit when a per-client-IP limit lands.
func RefreshHandler(authURL *url.URL, client *http.Client, log *slog.Logger) http.Handler {
	tokenURL := authURL.JoinPath("token")
	// GoTrue reads grant_type through FormValue, so it rides the query beside a JSON body.
	tokenURL.RawQuery = "grant_type=refresh_token"
	token := tokenURL.String()

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
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		}
		status, _, err := postGoTrue(r, client, token, map[string]string{"refresh_token": in.RefreshToken}, &sess)
		switch {
		case err != nil:
			log.WarnContext(r.Context(), "refresh: gotrue unreachable", slog.String("error", err.Error()))
			writeError(w, http.StatusBadGateway, "renewal is unavailable")
		case status == http.StatusOK && sess.AccessToken != "" && sess.RefreshToken != "":
			writeJSON(w, http.StatusOK, sess)
		case status == http.StatusTooManyRequests:
			writeError(w, http.StatusTooManyRequests, "too many requests")
		// The client cannot repair any 4xx, so every reason answers the same bytes.
		case status >= 400 && status < 500:
			writeError(w, http.StatusUnauthorized, "invalid or expired refresh token")
		default:
			log.WarnContext(r.Context(), "refresh: gotrue token failed", slog.Int("upstream_status", status))
			writeError(w, http.StatusBadGateway, "renewal is unavailable")
		}
	})
}
