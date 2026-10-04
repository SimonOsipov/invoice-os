package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxRegisterBodyBytes = 4 << 10
	// Caps what is read from GoTrue: an error body and a session body are both small.
	maxGoTrueBodyBytes = 64 << 10
	// Repeats tenancy's maxNameChars on purpose: the gateway does not import tenancy.
	maxAnswerNameChars  = 200
	maxConsentTextChars = 500
)

// DefaultRegisterMinResponse is the shortest time any non-400 register answer takes.
const DefaultRegisterMinResponse = 2 * time.Second

// RegisterHandler answers POST /auth/register by calling GoTrue's /signup under authURL.
// Every answer except a 400 arrives no earlier than minResponse after the request; 0 means no wait.
func RegisterHandler(authURL *url.URL, client *http.Client, minResponse time.Duration, log *slog.Logger) http.Handler {
	signup := authURL.JoinPath("signup").String()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Not postOnly: it sets headers on a POST, and a gone client must see no write.
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		start := time.Now()
		var in struct {
			Email         string  `json:"email"`
			Password      string  `json:"password"`
			WorkspaceName *string `json:"workspace_name"`
			DisplayName   *string `json:"display_name"`
			Kind          *string `json:"kind"`
			ConsentText   *string `json:"marketing_consent_text"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRegisterBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if in.Email == "" || in.Password == "" {
			writeError(w, http.StatusBadRequest, "email and password are required")
			return
		}
		if isFreeMail(in.Email) {
			writeError(w, http.StatusBadRequest, "a business email address is required; personal email providers are not accepted")
			return
		}

		body := map[string]any{"email": in.Email, "password": in.Password}
		data := map[string]any{}
		if in.WorkspaceName != nil || in.DisplayName != nil || in.Kind != nil {
			reg, msg := registrationAnswers(in.WorkspaceName, in.DisplayName, in.Kind)
			if msg != "" {
				writeError(w, http.StatusBadRequest, msg)
				return
			}
			data["registration"] = reg
		}
		if t := in.ConsentText; t != nil {
			// Stored as sent: the text is what the person was shown.
			if n := utf8.RuneCountInString(*t); strings.TrimSpace(*t) == "" || n > maxConsentTextChars || strings.ContainsRune(*t, 0) {
				writeError(w, http.StatusBadRequest, "marketing_consent_text must be 1 to 500 characters")
				return
			}
			data["marketing_consent"] = map[string]string{"text": *t, "at": time.Now().UTC().Format(time.RFC3339)}
		}
		if len(data) > 0 {
			body["data"] = data
		}

		status, gt, err := postGoTrue(r, client, signup, body, nil)
		upstream := time.Since(start)
		pending := func() { writeJSON(w, http.StatusAccepted, map[string]string{"status": "verification_pending"}) }
		var send func()
		if err != nil {
			log.WarnContext(r.Context(), "registration: gotrue unreachable", slog.String("error", err.Error()))
			send = func() { writeError(w, http.StatusBadGateway, "registration is unavailable") }
		}

		// A repeat or confirmed address answers exactly like a new one.
		switch {
		case err != nil:
		case status == http.StatusOK,
			gt.ErrorCode == "user_already_exists",
			gt.ErrorCode == "email_exists":
			send = pending
		case gt.ErrorCode == "over_email_send_rate_limit":
			// ceiling: GoTrue's instance-wide mail cap (30/h) answers the same code, so this WARN is its only signal; raise GOTRUE_RATE_LIMIT_EMAIL_SENT when signups near it.
			log.WarnContext(r.Context(), "registration: gotrue email send rate limit", slog.Int("upstream_status", status))
			send = pending
		case status >= http.StatusInternalServerError && gt.Code == "23505":
			// The loser of two concurrent signups for one address gets GoTrue's unique-violation 500.
			log.WarnContext(r.Context(), "registration: gotrue concurrent duplicate signup")
			send = pending
		case gt.ErrorCode == "validation_failed",
			gt.ErrorCode == "weak_password",
			gt.ErrorCode == "email_address_invalid":
			writeError(w, http.StatusBadRequest, gt.Msg)
			return
		case gt.ErrorCode == "signup_disabled":
			send = func() { writeError(w, http.StatusServiceUnavailable, "registration is closed") }
		case status == http.StatusTooManyRequests:
			send = func() { writeError(w, http.StatusTooManyRequests, "too many requests") }
		default:
			log.WarnContext(r.Context(), "registration: gotrue signup failed",
				slog.Int("upstream_status", status), slog.String("error_code", gt.ErrorCode))
			send = func() { writeError(w, http.StatusBadGateway, "registration is unavailable") }
		}

		if holdMinimum(r.Context(), log, start, upstream, minResponse) {
			send()
		}
	})
}

// registrationAnswers trims and validates the answers with tenancy.ProvisionHandler's rules and
// wording (internal/tenancy/tenancy.go). It returns a refusal message, or the answers to store.
func registrationAnswers(workspace, display, kind *string) (map[string]string, string) {
	var w, d string
	if workspace != nil {
		w = strings.TrimSpace(*workspace)
	}
	if display != nil {
		d = strings.TrimSpace(*display)
	}
	if n := utf8.RuneCountInString(w); n == 0 || n > maxAnswerNameChars {
		return nil, "workspace_name must be 1 to 200 characters"
	}
	if n := utf8.RuneCountInString(d); n == 0 || n > maxAnswerNameChars {
		return nil, "display_name must be 1 to 200 characters"
	}
	if strings.ContainsRune(w, 0) {
		return nil, "workspace_name must not contain a NUL byte"
	}
	if strings.ContainsRune(d, 0) {
		return nil, "display_name must not contain a NUL byte"
	}
	out := map[string]string{"workspace_name": w, "display_name": d}
	if kind != nil {
		if *kind != "firm" && *kind != "in_house" {
			return nil, `kind must be "firm" or "in_house"`
		}
		out["kind"] = *kind
	}
	return out, ""
}

// holdMinimum logs the signup timing and waits out what is left of minResponse since start.
// It reports false when the client went away first. A minResponse of 0 neither logs nor waits.
func holdMinimum(ctx context.Context, log *slog.Logger, start time.Time, upstream, minResponse time.Duration) bool {
	if minResponse <= 0 {
		return true
	}
	level := slog.LevelWarn
	if upstream < minResponse {
		level = slog.LevelInfo
	}
	// ceiling: a GoTrue answer slower than the minimum still leaks timing; revisit when this line logs WARN.
	// ceiling: each waiting request holds a connection for up to the minimum and register has no per-client limit; revisit with the per-client-IP limit owed before U3.
	log.Log(ctx, level, "registration: signup timing",
		slog.Int64("upstream_ms", upstream.Milliseconds()), slog.Int64("min_ms", minResponse.Milliseconds()))
	timer := time.NewTimer(max(0, minResponse-time.Since(start)))
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// VerifyHandler answers the emailed link by calling GoTrue's /verify, then redirects to siteURL.
func VerifyHandler(authURL, siteURL *url.URL, client *http.Client, log *slog.Logger, sink ContactSink) http.Handler {
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
		var confirmed struct {
			User gotrueUser `json:"user"`
		}
		status, _, err := postGoTrue(r, client, verify, map[string]string{"type": "signup", "token_hash": token}, &confirmed)
		switch {
		case err != nil:
			log.WarnContext(r.Context(), "verify: gotrue unreachable", slog.String("error", err.Error()))
			http.Redirect(w, r, failed, http.StatusSeeOther)
		case status != http.StatusOK:
			log.WarnContext(r.Context(), "verify: gotrue refused the link", slog.Int("upstream_status", status))
			http.Redirect(w, r, failed, http.StatusSeeOther)
		default:
			// ceiling: the session GoTrue issued is never delivered; any global sign-out or staff cut-off deletes it. Revisit when verifying should sign the user in.
			handOffRegistrant(r.Context(), log, "verify", sink, confirmed.User.contact())
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

type gotrueError struct {
	ErrorCode string `json:"error_code"`
	Msg       string `json:"msg"`
	// Numeric on API errors; a SQLSTATE string on an unhandled database error.
	Code any `json:"code"`
}

// postGoTrue posts body as JSON and returns the status and any error fields. A 200 body is
// decoded into ok when ok is non-nil, and discarded otherwise.
func postGoTrue(r *http.Request, client *http.Client, target string, body, ok any) (int, gotrueError, error) {
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
	switch {
	case resp.StatusCode != http.StatusOK:
		_ = json.NewDecoder(lr).Decode(&gt)
	case ok != nil:
		_ = json.NewDecoder(lr).Decode(ok)
	}
	_, _ = io.Copy(io.Discard, lr)
	return resp.StatusCode, gt, nil
}

// postGoTrueBearer posts an empty body with bearer as the Authorization and returns the status and sessionGone.
func postGoTrueBearer(ctx context.Context, client *http.Client, target, bearer string) (status int, gone bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	gone = sessionGone(resp.StatusCode, resp.Body)
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxGoTrueBodyBytes))
	return resp.StatusCode, gone, nil
}
