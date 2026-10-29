package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ceiling: with no pool, counts are in-process; a restart clears them and replicas do not share them.
// ceiling: an attacker can spend a victim address's 3 per hour; with no pool, a full map (10,000 keys) refuses every new key, so a flood switches resend off for new addresses.
const (
	ResendPerAddress = 3
	ResendWindow     = time.Hour
	ResendMaxKeys    = 10_000
)

// ResendVerificationHandler answers POST /auth/resend-verification with one answer for every account state.
// Every answer except a 400 arrives no earlier than minResponse after the request; 0 means no wait.
func ResendVerificationHandler(authURL *url.URL, client *http.Client, minResponse time.Duration, perAddress *SignInThrottle, enforce bool, log *slog.Logger) http.Handler {
	return mailLinkHandler("resend-verification", "resend", authURL.JoinPath("resend").String(),
		func(email string) map[string]string { return map[string]string{"type": "signup", "email": email} },
		client, minResponse, perAddress, enforce, log, nil)
}

// mailLinkHandler serves the mail-link routes (resend, reset); both share one per-address throttle.
// name prefixes every log line; verb names the GoTrue call in the failure line.
func mailLinkHandler(name, verb, endpoint string, payload func(email string) map[string]string, client *http.Client, minResponse time.Duration, perAddress *SignInThrottle, enforce bool, log *slog.Logger, onAdmitted func(r *http.Request, email string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Not postOnly: it sets headers on a POST, and a gone client must see no write.
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		start := time.Now()
		var in struct {
			Email string `json:"email"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExchangeBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		email := strings.TrimSpace(in.Email)
		if email == "" {
			writeError(w, http.StatusBadRequest, "email is required")
			return
		}
		if len(email) > maxEmailBytes {
			writeError(w, http.StatusBadRequest, "invalid email address")
			return
		}

		addrHeld, admitErr := perAddress.Admit(email)
		refused := false
		if !addrHeld {
			// A database error is already logged by the throttle.
			if admitErr == nil {
				log.WarnContext(r.Context(), name+": limit reached",
					slog.String("limit", "address"), slog.Bool("enforced", enforce))
			}
			refused = enforce
		}

		var upstream time.Duration
		if !refused {
			status, gt, err := postGoTrue(r, client, endpoint, payload(email), nil)
			upstream = time.Since(start)
			// GoTrue mails nothing when it answers 4xx; 2xx, 5xx and transport errors may have mailed.
			if addrHeld && err == nil && status >= http.StatusBadRequest && status < http.StatusInternalServerError {
				perAddress.Refund(email)
			}
			switch {
			case err != nil:
				log.WarnContext(r.Context(), name+": gotrue unreachable", slog.String("error", err.Error()))
			case status == http.StatusOK:
			case gt.ErrorCode == "validation_failed":
				writeError(w, http.StatusBadRequest, "invalid email address")
				return
			case gt.ErrorCode == "over_email_send_rate_limit":
				log.WarnContext(r.Context(), name+": gotrue email send rate limit", slog.Int("upstream_status", status))
			default:
				log.WarnContext(r.Context(), name+": gotrue "+verb+" failed",
					slog.Int("upstream_status", status), slog.String("error_code", gt.ErrorCode))
			}
			// Inside the floor: the insert time is absorbed for a member and a non-member alike.
			if addrHeld && onAdmitted != nil {
				onAdmitted(r, email)
			}
		}

		if holdMinimum(r.Context(), log, name+": timing", start, upstream, minResponse) {
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
		}
	})
}
