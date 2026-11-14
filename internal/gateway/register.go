package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/platform"
)

const (
	maxRegisterBodyBytes = 4 << 10
	// Caps what is read from GoTrue: an error body and a session body are both small.
	maxGoTrueBodyBytes = 64 << 10
	maxSettingsBytes   = 16 << 10
	// Repeats tenancy's maxNameChars on purpose: the gateway does not import tenancy.
	maxAnswerNameChars  = 200
	maxConsentTextChars = 500
)

// tokenLookupTimeout bounds the unsubscribe token lookup; the floor still holds after it.
const tokenLookupTimeout = time.Second

// DefaultRegisterMinResponse is the shortest time any non-400 register answer takes.
const DefaultRegisterMinResponse = 2 * time.Second

// RegisterHandler answers POST /auth/register by calling GoTrue's /signup under authURL, except for an invited address (TestRegister_InvitedAddressCreatesNothing).
// Every answer except a 400 arrives no earlier than minResponse after the request; 0 means no wait.
// A nil tokens signs up without the unsubscribe token.
// It sends the caller's password only when forward is true and GoTrue's GET /settings reports mailer_autoconfirm true; otherwise a random one.
func RegisterHandler(authURL *url.URL, client *http.Client, minResponse time.Duration, log *slog.Logger, pending PendingInviteLookup, tokens accountmail.TokenFunc, forward bool) http.Handler {
	signup := authURL.JoinPath("signup").String()
	settings := authURL.JoinPath("settings").String()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Not postOnly: it sets headers on a POST, and a gone client must see no write.
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if pending == nil {
			writeError(w, http.StatusServiceUnavailable, "registration is not configured")
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
		if in.Email == "" {
			writeError(w, http.StatusBadRequest, "email is required")
			return
		}
		if isFreeMail(in.Email) {
			writeError(w, http.StatusBadRequest, "a business email address is required; personal email providers are not accepted")
			return
		}

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
		password := randomPassword()
		if forward && in.Password != "" && idpAutoconfirms(r, client, settings, log) {
			password = in.Password
		}
		body := map[string]any{"email": in.Email, "password": password}
		if len(data) > 0 {
			body["data"] = data
		}

		invited := func(ctx context.Context) (bool, error) { return pending(ctx, in.Email) }
		_ = signUp(w, r, client, signup, body, start, minResponse, log, invited, nil, tokens, in.Email)
	})
}

// randomPassword returns 32 random bytes as base64url; nobody learns it.
func randomPassword() string {
	pw := make([]byte, 32)
	rand.Read(pw) // never fails on Go 1.24+
	return base64.RawURLEncoding.EncodeToString(pw)
}

// idpAutoconfirms reports whether GoTrue's GET /settings answers 200 with mailer_autoconfirm true.
// An unreadable answer logs one WARN with the status only and reports false.
func idpAutoconfirms(r *http.Request, client *http.Client, settings string, log *slog.Logger) bool {
	status := 0
	var got struct {
		Autoconfirm *bool `json:"mailer_autoconfirm"`
	}
	ok := false
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, settings, nil)
	if err == nil {
		req.Header.Set(platform.HeaderUserIP, gotrueAddress(r))
		var resp *http.Response
		if resp, err = client.Do(req); err == nil {
			defer resp.Body.Close()
			status = resp.StatusCode
			lr := io.LimitReader(resp.Body, maxSettingsBytes)
			ok = status == http.StatusOK && json.NewDecoder(lr).Decode(&got) == nil && got.Autoconfirm != nil
			_, _ = io.Copy(io.Discard, lr)
		}
	}
	if !ok {
		log.WarnContext(r.Context(), "registration: idp settings unreadable", slog.Int("upstream_status", status))
		return false
	}
	return *got.Autoconfirm
}

// signUp posts body to GoTrue's /signup and answers with the floor held.
// RegisterHandler and InvitationRegisterHandler share it.
// When invited reports true, it answers as for a new address without calling GoTrue.
// A non-nil existing is sent instead of the 202 when GoTrue reports an address that already has an account.
// After that check it adds the address's unsubscribe token to body's data; a nil tokens, an error or a lookup slower than tokenLookupTimeout leaves it out.
// It reports whether an account may exist for the address afterwards: GoTrue 200, an existing address or the 23505 race.
func signUp(w http.ResponseWriter, r *http.Request, client *http.Client, signup string, body map[string]any, start time.Time, minResponse time.Duration, log *slog.Logger, invited func(context.Context) (bool, error), existing func(), tokens accountmail.TokenFunc, email string) (accountMayExist bool) {
	if invited != nil {
		yes, lerr := invited(r.Context())
		if lerr != nil || yes {
			send := func() { writeJSON(w, http.StatusAccepted, map[string]string{"status": "verification_pending"}) }
			if lerr != nil {
				log.WarnContext(r.Context(), "registration: pending invite lookup failed", slog.String("error", lerr.Error()))
				send = func() { writeError(w, http.StatusBadGateway, "registration is unavailable") }
			}
			if holdMinimum(r.Context(), log, "registration: signup timing", start, time.Since(start), minResponse) {
				send()
			}
			return false
		}
	}

	if tokens != nil && body != nil {
		addUnsubscribeToken(r.Context(), log, body, tokens, email)
	}

	var created any
	var sanitized struct {
		Identities *[]json.RawMessage `json:"identities"`
	}
	if existing != nil {
		created = &sanitized
	}
	status, gt, err := postGoTrue(r, client, signup, body, created)
	upstream := time.Since(start)
	// Sent only on the invite route: the public route cannot tell an existing address from a new one.
	existingAccount := existing != nil && status == http.StatusOK && sanitized.Identities != nil && len(*sanitized.Identities) == 0
	pending := func() { writeJSON(w, http.StatusAccepted, map[string]string{"status": "verification_pending"}) }
	var send func()
	mayExist := false
	if err != nil {
		log.WarnContext(r.Context(), "registration: gotrue unreachable", slog.String("error", err.Error()))
		send = func() { writeError(w, http.StatusBadGateway, "registration is unavailable") }
	}

	// Without existing, a repeat or confirmed address answers exactly like a new one.
	switch {
	case err != nil:
	case existing != nil && (existingAccount || gt.ErrorCode == "user_already_exists" || gt.ErrorCode == "email_exists"):
		send = existing
		mayExist = true
	case status == http.StatusOK,
		gt.ErrorCode == "user_already_exists",
		gt.ErrorCode == "email_exists":
		send = pending
		mayExist = true
	case gt.ErrorCode == "over_email_send_rate_limit":
		// ceiling: GoTrue's instance-wide mail cap (30/h) answers the same code, so this WARN is its only signal; raise GOTRUE_RATE_LIMIT_EMAIL_SENT when signups near it.
		log.WarnContext(r.Context(), "registration: gotrue email send rate limit", slog.Int("upstream_status", status))
		send = pending
	case status >= http.StatusInternalServerError && gt.Code == "23505":
		// The loser of two concurrent signups for one address gets GoTrue's unique-violation 500.
		log.WarnContext(r.Context(), "registration: gotrue concurrent duplicate signup")
		send = pending
		mayExist = true
	case gt.ErrorCode == "validation_failed",
		gt.ErrorCode == "weak_password",
		gt.ErrorCode == "email_address_invalid":
		writeError(w, http.StatusBadRequest, gt.Msg)
		return false
	case gt.ErrorCode == "signup_disabled":
		send = func() { writeError(w, http.StatusServiceUnavailable, "registration is closed") }
	case status == http.StatusTooManyRequests:
		send = func() { writeError(w, http.StatusTooManyRequests, "too many requests") }
	default:
		log.WarnContext(r.Context(), "registration: gotrue signup failed",
			slog.Int("upstream_status", status), slog.String("error_code", gt.ErrorCode))
		send = func() { writeError(w, http.StatusBadGateway, "registration is unavailable") }
	}

	if holdMinimum(r.Context(), log, "registration: signup timing", start, upstream, minResponse) {
		send()
	}
	return mayExist
}

// addUnsubscribeToken sets data.unsubscribe, creating data when absent. The WARN holds no address.
func addUnsubscribeToken(ctx context.Context, log *slog.Logger, body map[string]any, tokens accountmail.TokenFunc, email string) {
	ctx, cancel := context.WithTimeout(ctx, tokenLookupTimeout)
	defer cancel()
	token, err := tokens(ctx, email)
	if err != nil || token == "" {
		log.WarnContext(ctx, "registration: unsubscribe token unavailable, signing up without it")
		return
	}
	data, _ := body["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
		body["data"] = data
	}
	data[accountmail.UnsubscribeMetadataKey] = token
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

// holdMinimum logs the timing line msg and waits out what is left of minResponse since start.
// It reports false when the client went away first. A minResponse of 0 neither logs nor waits.
func holdMinimum(ctx context.Context, log *slog.Logger, msg string, start time.Time, upstream, minResponse time.Duration) bool {
	if minResponse <= 0 {
		return true
	}
	level := slog.LevelWarn
	if upstream < minResponse {
		level = slog.LevelInfo
	}
	// ceiling: a GoTrue answer slower than the minimum still leaks timing; revisit when this line logs WARN.
	// ceiling: each waiting request holds a connection for up to the minimum.
	log.Log(ctx, level, msg,
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

// RegistrationNotConfigured answers 503 while AUTH_SITE_URL is unset.
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
	req.Header.Set(platform.HeaderUserIP, gotrueAddress(r))
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
func postGoTrueBearer(ctx context.Context, client *http.Client, target, bearer, address string) (status int, gone bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set(platform.HeaderUserIP, address)
	resp, err := client.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	gone = sessionGone(resp.StatusCode, resp.Body)
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxGoTrueBodyBytes))
	return resp.StatusCode, gone, nil
}

// putGoTrueBearerJSON puts body as JSON with bearer as the Authorization and returns the status and any error fields.
func putGoTrueBearerJSON(ctx context.Context, client *http.Client, target, bearer, address string, body any) (int, gotrueError, error) {
	var gt gotrueError
	b, err := json.Marshal(body)
	if err != nil {
		return 0, gt, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(b))
	if err != nil {
		return 0, gt, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set(platform.HeaderUserIP, address)
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
