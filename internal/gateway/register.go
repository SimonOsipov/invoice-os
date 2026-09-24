package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

const (
	maxRegisterBodyBytes = 4 << 10
	// Caps what is read from GoTrue: an error body is small, and a session body is discarded.
	maxGoTrueBodyBytes = 64 << 10
)

// RegisterHandler answers POST /auth/register by calling GoTrue's /signup under authURL.
func RegisterHandler(authURL *url.URL, client *http.Client, log *slog.Logger) http.Handler {
	signup := authURL.JoinPath("signup").String()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRegisterBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if in.Email == "" || in.Password == "" {
			writeError(w, http.StatusBadRequest, "email and password are required")
			return
		}

		status, gt, err := postGoTrue(r, client, signup, in)
		if err != nil {
			log.WarnContext(r.Context(), "registration: gotrue unreachable", slog.String("error", err.Error()))
			writeError(w, http.StatusBadGateway, "registration is unavailable")
			return
		}

		// A repeat or confirmed address answers exactly like a new one.
		switch {
		case status == http.StatusOK,
			gt.ErrorCode == "user_already_exists",
			gt.ErrorCode == "email_exists":
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "verification_pending"})
		case gt.ErrorCode == "over_email_send_rate_limit":
			// ceiling: GoTrue's instance-wide mail cap (30/h) answers the same code, so this WARN is its only signal; raise GOTRUE_RATE_LIMIT_EMAIL_SENT when signups near it.
			log.WarnContext(r.Context(), "registration: gotrue email send rate limit", slog.Int("upstream_status", status))
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "verification_pending"})
		case status >= http.StatusInternalServerError && gt.Code == "23505":
			// The loser of two concurrent signups for one address gets GoTrue's unique-violation 500.
			log.WarnContext(r.Context(), "registration: gotrue concurrent duplicate signup")
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "verification_pending"})
		case gt.ErrorCode == "validation_failed",
			gt.ErrorCode == "weak_password",
			gt.ErrorCode == "email_address_invalid":
			writeError(w, http.StatusBadRequest, gt.Msg)
		case gt.ErrorCode == "signup_disabled":
			writeError(w, http.StatusServiceUnavailable, "registration is closed")
		case status == http.StatusTooManyRequests:
			writeError(w, http.StatusTooManyRequests, "too many requests")
		default:
			log.WarnContext(r.Context(), "registration: gotrue signup failed",
				slog.Int("upstream_status", status), slog.String("error_code", gt.ErrorCode))
			writeError(w, http.StatusBadGateway, "registration is unavailable")
		}
	})
}

// VerifyHandler answers the emailed link by calling GoTrue's /verify, then redirects to siteURL.
func VerifyHandler(authURL, siteURL *url.URL, client *http.Client, log *slog.Logger) http.Handler {
	if siteURL == nil {
		return RegistrationNotConfigured()
	}
	verify := authURL.JoinPath("verify").String()
	site := strings.TrimSuffix(siteURL.String(), "/")
	verified, failed := site+"/?verified=1", site+"/?verify=failed"

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A HEAD prefetch by a link scanner would consume the single-use token.
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// redirect_to is ignored: the target is server configuration, never a query value.
		q := r.URL.Query()
		token := q.Get("token")
		if token == "" || q.Get("type") != "signup" {
			http.Redirect(w, r, failed, http.StatusSeeOther)
			return
		}
		status, _, err := postGoTrue(r, client, verify, map[string]string{"type": "signup", "token_hash": token})
		switch {
		case err != nil:
			log.WarnContext(r.Context(), "verify: gotrue unreachable", slog.String("error", err.Error()))
			http.Redirect(w, r, failed, http.StatusSeeOther)
		case status != http.StatusOK:
			log.WarnContext(r.Context(), "verify: gotrue refused the link", slog.Int("upstream_status", status))
			http.Redirect(w, r, failed, http.StatusSeeOther)
		default:
			// ceiling: the session GoTrue issued is discarded but stays live until AUTH-07 builds revocation.
			http.Redirect(w, r, verified, http.StatusSeeOther)
		}
	})
}

// RegistrationNotConfigured answers both registration routes while AUTH_SITE_URL is unset.
func RegistrationNotConfigured() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusServiceUnavailable, "registration is not configured")
	})
}

// gotrueError is the only part of a GoTrue response the gateway reads.
type gotrueError struct {
	ErrorCode string `json:"error_code"`
	Msg       string `json:"msg"`
	// Numeric on API errors; a SQLSTATE string on an unhandled database error.
	Code any `json:"code"`
}

// postGoTrue posts body as JSON and returns the status and any error fields; the rest of
// the response, including a session, is discarded unread.
func postGoTrue(r *http.Request, client *http.Client, target string, body any) (int, gotrueError, error) {
	var gt gotrueError
	b, err := json.Marshal(body)
	if err != nil {
		return 0, gt, err
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(b))
	if err != nil {
		return 0, gt, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, gt, err
	}
	defer resp.Body.Close()
	lr := io.LimitReader(resp.Body, maxGoTrueBodyBytes)
	if resp.StatusCode != http.StatusOK {
		_ = json.NewDecoder(lr).Decode(&gt)
	}
	_, _ = io.Copy(io.Discard, lr)
	return resp.StatusCode, gt, nil
}
