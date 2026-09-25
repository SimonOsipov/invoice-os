package gateway

import (
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
)

const (
	maxExchangeBodyBytes = 1 << 10
	maxEmailBytes        = 254
)

// stateShape is a 32-byte state in unpadded base64url (D25).
var stateShape = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// SignInHandler answers POST /auth/sign-in with a single-use exchange code.
func SignInHandler(authURL *url.URL, client *http.Client, store *HandoffStore, throttle *SignInThrottle, log *slog.Logger) http.Handler {
	tokenURL := authURL.JoinPath("token")
	tokenURL.RawQuery = "grant_type=password"
	token := tokenURL.String()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !postOnly(w, r) {
			return
		}
		var in struct {
			Email    string `json:"email"`
			Password string `json:"password"`
			State    string `json:"state"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRegisterBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if in.Email == "" || in.Password == "" {
			writeError(w, http.StatusBadRequest, "email and password are required")
			return
		}
		if !stateShape.MatchString(in.State) {
			writeError(w, http.StatusBadRequest, "state is required")
			return
		}
		if len(in.Email) > maxEmailBytes {
			writeError(w, http.StatusBadRequest, "invalid email address")
			return
		}
		// Reserving before the call bounds a parallel burst to the window's budget.
		if !throttle.Reserve(in.Email) {
			writeError(w, http.StatusTooManyRequests, "too many requests")
			return
		}

		var sess struct {
			AccessToken string `json:"access_token"`
		}
		status, gt, err := postGoTrue(r, client, token, map[string]string{"email": in.Email, "password": in.Password}, &sess)
		switch {
		case err != nil:
			throttle.Refund(in.Email)
			log.WarnContext(r.Context(), "sign-in: gotrue unreachable", slog.String("error", err.Error()))
			writeError(w, http.StatusBadGateway, "sign-in is unavailable")
		case status == http.StatusOK && sess.AccessToken != "":
			throttle.Reset(in.Email)
			code, _ := store.Put(sess.AccessToken, sha256.Sum256([]byte(in.State)))
			writeJSON(w, http.StatusOK, map[string]string{"code": code})
		// A banned address answers exactly like a wrong password; the reservation stands.
		case gt.ErrorCode == "invalid_credentials", gt.ErrorCode == "user_banned":
			writeError(w, http.StatusUnauthorized, "invalid email or password")
		case gt.ErrorCode == "email_not_confirmed":
			throttle.Refund(in.Email)
			writeError(w, http.StatusForbidden, "email address not verified")
		case status == http.StatusTooManyRequests:
			throttle.Refund(in.Email)
			writeError(w, http.StatusTooManyRequests, "too many requests")
		default:
			throttle.Refund(in.Email)
			log.WarnContext(r.Context(), "sign-in: gotrue token failed", slog.Int("upstream_status", status))
			writeError(w, http.StatusBadGateway, "sign-in is unavailable")
		}
	})
}

// ExchangeHandler answers POST /auth/exchange by redeeming a code for its access token.
func ExchangeHandler(store *HandoffStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !postOnly(w, r) {
			return
		}
		var in struct {
			Code  string `json:"code"`
			State string `json:"state"`
		}
		// One answer for every refusal: unknown, spent, expired and mis-bound codes look alike.
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExchangeBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid or expired code")
			return
		}
		tok, ok := store.Take(in.Code, in.State)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid or expired code")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"access_token": tok})
	})
}

// postOnly marks every answer uncacheable and refuses any method but POST.
func postOnly(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	return true
}
