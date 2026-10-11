package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// InvitationPreview is what a token holder sees before signing in.
type InvitationPreview struct{ Workspace, Role, Email, Account string }

// ErrInvitationNotValid means the token names no live invite.
var ErrInvitationNotValid = errors.New("gateway: invitation is not valid")

// InvitationPreviewer looks up the live invite a token names.
type InvitationPreviewer func(ctx context.Context, token string) (InvitationPreview, error)

// invitationPreviewTimeout bounds each tenancy call; tests shorten it.
var invitationPreviewTimeout = 5 * time.Second

const (
	msgInviteNotValid        = "this invite is no longer valid"
	msgInviteLookupDown      = "invitation lookup is unavailable"
	maxPreviewResponseBytes  = 4 << 10
	previewPath              = "internal/invitations/preview"
	pendingPath              = "internal/invitations/pending"
	claimRoute               = "internal/invitations/register"
	releaseRoute             = "internal/invitations/release"
	msgInviteeFieldsRequired = "token is required"
	msgAccountExists         = "account_exists"
	msgAccountUnconfirmed    = "account_unconfirmed"
	msgAccountMissing        = "account_missing"
	msgInviteTokenRequired   = "token is required"
	msgResendUnavailable     = "invitation resend is unavailable"
	// GoTrue's 60 s per-address mail cooldown; its instance-wide mail cap uses the same error_code with other wording.
	msgResendCooldownPrefix = "For security purposes, you can only request this after "
)

// postTenancy posts payload to an internal tenancy route with the gateway token and no redirect.
// On a 200 it decodes the body into out and fails if it cannot; on a 404 it decodes best-effort. It returns the status; its errors
// carry the label and the status only: never the token, the address, or a *url.Error, which holds the URL.
func postTenancy(ctx context.Context, c *http.Client, target, gatewayToken, label string, payload, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, invitationPreviewTimeout)
	defer cancel()
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, errors.New(label + ": encode request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(b))
	if err != nil {
		return 0, errors.New(label + ": build request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Token", gatewayToken)
	resp, err := c.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return 0, errors.New(label + ": tenancy timed out")
		}
		return 0, errors.New(label + ": tenancy unreachable")
	}
	defer resp.Body.Close()
	lr := io.LimitReader(resp.Body, maxPreviewResponseBytes)
	defer func() { _, _ = io.Copy(io.Discard, lr) }()
	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.NewDecoder(lr).Decode(out); err != nil {
			return resp.StatusCode, errors.New(label + ": tenancy answered 200 with an unreadable body")
		}
	case http.StatusNotFound:
		if out != nil {
			_ = json.NewDecoder(lr).Decode(out)
		}
	}
	return resp.StatusCode, nil
}

// noRedirect returns a copy of client that hands a redirect back instead of following it.
func noRedirect(client *http.Client) *http.Client {
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// NewHTTPInvitationPreviewer asks tenancy's internal preview route, with the gateway token and no identity.
func NewHTTPInvitationPreviewer(base *url.URL, client *http.Client, gatewayToken string) InvitationPreviewer {
	c := noRedirect(client)
	target := base.JoinPath(previewPath).String()
	return func(ctx context.Context, token string) (InvitationPreview, error) {
		var out struct {
			Workspace string `json:"workspace"`
			Role      string `json:"role"`
			Email     string `json:"email"`
			Account   string `json:"account"`
		}
		status, err := postTenancy(ctx, c, target, gatewayToken, "invitation preview", map[string]string{"token": token}, &out)
		switch {
		case err != nil:
			return InvitationPreview{}, err
		case status == http.StatusOK:
			switch out.Account {
			case "none", "unconfirmed", "confirmed":
			default:
				out.Account = "unknown"
			}
			return InvitationPreview{Workspace: out.Workspace, Role: out.Role, Email: out.Email, Account: out.Account}, nil
		case status == http.StatusNotFound:
			return InvitationPreview{}, ErrInvitationNotValid
		default:
			return InvitationPreview{}, fmt.Errorf("invitation preview: tenancy answered %d", status)
		}
	}
}

// PendingInviteLookup reports whether email has a pending, unexpired invite in any tenant.
type PendingInviteLookup func(ctx context.Context, email string) (bool, error)

// NewHTTPPendingInviteLookup asks tenancy's internal pending route. Any answer but 200 is an error,
// a 404 included: a tenancy without the route must not read as "no pending invite".
// A 200 without a boolean `pending` is an error too.
func NewHTTPPendingInviteLookup(base *url.URL, client *http.Client, gatewayToken string) PendingInviteLookup {
	c := noRedirect(client)
	target := base.JoinPath(pendingPath).String()
	return func(ctx context.Context, email string) (bool, error) {
		var out struct {
			Pending *bool `json:"pending"`
		}
		status, err := postTenancy(ctx, c, target, gatewayToken, "pending invite lookup", map[string]string{"email": email}, &out)
		if err != nil {
			return false, err
		}
		if status != http.StatusOK {
			return false, fmt.Errorf("pending invite lookup: tenancy answered %d", status)
		}
		if out.Pending == nil {
			return false, errors.New("pending invite lookup: tenancy answered without a verdict")
		}
		return *out.Pending, nil
	}
}

// previewToken looks up token, answering 404 or 502 itself and reporting false when it did.
// A token that is not 43 base64url characters is refused without a lookup.
func previewToken(w http.ResponseWriter, r *http.Request, preview InvitationPreviewer, log *slog.Logger, token string) (InvitationPreview, bool) {
	if !stateShape.MatchString(token) {
		writeError(w, http.StatusNotFound, msgInviteNotValid)
		return InvitationPreview{}, false
	}
	p, err := preview(r.Context(), token)
	switch {
	case err == nil:
		return p, true
	case errors.Is(err, ErrInvitationNotValid):
		writeError(w, http.StatusNotFound, msgInviteNotValid)
	default:
		log.WarnContext(r.Context(), "invitation: lookup failed", slog.String("error", err.Error()))
		writeError(w, http.StatusBadGateway, msgInviteLookupDown)
	}
	return InvitationPreview{}, false
}

// InvitationHandler answers POST /auth/invitation with the workspace, role, address and account state a live token names.
func InvitationHandler(preview InvitationPreviewer, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !postOnly(w, r) {
			return
		}
		var in struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExchangeBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		p, ok := previewToken(w, r, preview, log, in.Token)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"workspace": p.Workspace, "role": p.Role, "email": p.Email, "account": p.Account})
	})
}

// InvitationRegistrations spends and returns the one registration an invite token backs.
type InvitationRegistrations struct {
	Claim   func(ctx context.Context, token string) (email string, first bool, err error)
	Release func(ctx context.Context, token string) error
}

// NewHTTPInvitationRegistrations asks tenancy's internal register and release routes, with the gateway token and no identity.
// Claim reads a 404 as an invalid token only when tenancy's JSON error says so: a tenancy without the route answers a bare 404.
// Release accepts only a 204.
func NewHTTPInvitationRegistrations(base *url.URL, client *http.Client, gatewayToken string) InvitationRegistrations {
	c := noRedirect(client)
	register := base.JoinPath(claimRoute).String()
	release := base.JoinPath(releaseRoute).String()
	return InvitationRegistrations{
		Claim: func(ctx context.Context, token string) (string, bool, error) {
			var out struct {
				Email string `json:"email"`
				First *bool  `json:"first"`
				Error string `json:"error"`
			}
			status, err := postTenancy(ctx, c, register, gatewayToken, "invitation claim", map[string]string{"token": token}, &out)
			switch {
			case err != nil:
				return "", false, err
			case status == http.StatusOK && out.Email != "" && out.First != nil:
				return out.Email, *out.First, nil
			case status == http.StatusOK:
				return "", false, errors.New("invitation claim: tenancy answered without an address or verdict")
			case status == http.StatusNotFound && out.Error == msgInviteNotValid:
				return "", false, ErrInvitationNotValid
			default:
				return "", false, fmt.Errorf("invitation claim: tenancy answered %d", status)
			}
		},
		Release: func(ctx context.Context, token string) error {
			status, err := postTenancy(ctx, c, release, gatewayToken, "invitation release", map[string]string{"token": token}, nil)
			if err != nil {
				return err
			}
			if status != http.StatusNoContent {
				return fmt.Errorf("invitation release: tenancy answered %d", status)
			}
			return nil
		},
	}
}

// InvitationRegisterHandler answers POST /auth/invitation/register: it signs the invited address up with GoTrue
// under a password nobody keeps; the invitee chooses theirs from the confirmation mail.
// The address comes from the claim, never from the body. A token backs one sign-up: a repeat answers 202 with no GoTrue call.
// A confirmed or unconfirmed preview is refused before the claim and the sign-up. A first claim whose sign-up left no account is released after the answer.
func InvitationRegisterHandler(authURL *url.URL, client *http.Client, minResponse time.Duration, perIP *SignInThrottle, enforce bool, log *slog.Logger, preview InvitationPreviewer, registrations InvitationRegistrations) http.Handler {
	signup := authURL.JoinPath("signup").String()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !postOnly(w, r) {
			return
		}
		if registrations.Claim == nil || registrations.Release == nil {
			writeError(w, http.StatusServiceUnavailable, "registration is not configured")
			return
		}
		start := time.Now()
		var in struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRegisterBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if in.Token == "" {
			writeError(w, http.StatusBadRequest, msgInviteeFieldsRequired)
			return
		}
		if !stateShape.MatchString(in.Token) {
			writeError(w, http.StatusNotFound, msgInviteNotValid)
			return
		}
		p, ok := previewToken(w, r, preview, log, in.Token)
		if !ok {
			return
		}
		refusal := ""
		switch p.Account {
		case "confirmed":
			refusal = msgAccountExists
		case "unconfirmed":
			refusal = msgAccountUnconfirmed
		}
		if refusal != "" {
			// Mails nobody, so the slot is refunded as on the GoTrue existing-account 409; the budget check and the floor stay.
			key, held, proceed := reserveSignUp(w, r, perIP, enforce, log, start, minResponse)
			if !proceed {
				return
			}
			if held {
				perIP.Refund(key)
			}
			if holdMinimum(r.Context(), log, "registration: signup timing", start, time.Since(start), minResponse) {
				writeError(w, http.StatusConflict, refusal)
			}
			return
		}
		email, first, err := registrations.Claim(r.Context(), in.Token)
		switch {
		case errors.Is(err, ErrInvitationNotValid):
			writeError(w, http.StatusNotFound, msgInviteNotValid)
			return
		case err != nil:
			log.WarnContext(r.Context(), "invitation: lookup failed", slog.String("error", err.Error()))
			writeError(w, http.StatusBadGateway, msgInviteLookupDown)
			return
		}
		if !first {
			// A repeat takes the same budget and floor, and mails nobody.
			_ = signUp(w, r, client, signup, nil, start, minResponse, perIP, enforce, log, func(context.Context) (bool, error) { return true, nil }, nil)
			return
		}
		pw := make([]byte, 32)
		rand.Read(pw) // never fails on Go 1.24+
		body := map[string]any{
			"email":    email,
			"password": base64.RawURLEncoding.EncodeToString(pw),
			"data":     map[string]any{"invited": true},
		}
		exists := func() { writeError(w, http.StatusConflict, msgAccountExists) }
		if mayExist := signUp(w, r, client, signup, body, start, minResponse, perIP, enforce, log, nil, exists); !mayExist {
			if err := registrations.Release(context.WithoutCancel(r.Context()), in.Token); err != nil {
				log.WarnContext(r.Context(), "invitation: release failed", slog.String("error", err.Error()))
			}
		}
	})
}

// InvitationResendHandler answers POST /auth/invitation/resend. Unlike the anonymous resend it says whether GoTrue
// mailed (sent), held on its cooldown (held) or could not tell (maybe); a GoTrue 4xx mails nothing, so its budget is refunded.
func InvitationResendHandler(authURL *url.URL, client *http.Client, perAddress, perIP *SignInThrottle, enforce bool, log *slog.Logger, preview InvitationPreviewer) http.Handler {
	endpoint := authURL.JoinPath("resend").String()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !postOnly(w, r) {
			return
		}
		var in struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExchangeBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if in.Token == "" {
			writeError(w, http.StatusBadRequest, msgInviteTokenRequired)
			return
		}
		p, ok := previewToken(w, r, preview, log, in.Token)
		if !ok {
			return
		}
		switch p.Account {
		case "confirmed":
			writeError(w, http.StatusConflict, msgAccountExists)
			return
		case "none":
			writeError(w, http.StatusConflict, msgAccountMissing)
			return
		}

		// IP first: a request refused for its IP spends no address count.
		key, source := clientKey(r)
		ipHeld := perIP.Reserve(key)
		addrHeld := false
		limited := func(limit string) {
			log.WarnContext(r.Context(), "invitation-resend: limit reached",
				slog.String("limit", limit), slog.String("key_source", source), slog.Bool("enforced", enforce))
		}
		if !ipHeld {
			limited("ip")
		} else if addrHeld = perAddress.Reserve(p.Email); !addrHeld {
			limited("address")
		}
		if (!ipHeld || !addrHeld) && enforce {
			writeError(w, http.StatusTooManyRequests, "too many requests")
			return
		}

		status, gt, err := postGoTrue(r, client, endpoint, map[string]string{"type": "signup", "email": p.Email}, nil)
		if err == nil && status >= http.StatusBadRequest && status < http.StatusInternalServerError {
			if ipHeld {
				perIP.Refund(key)
			}
			if addrHeld {
				perAddress.Refund(p.Email)
			}
		}
		switch {
		case err != nil:
			log.WarnContext(r.Context(), "invitation-resend: gotrue unreachable", slog.String("error", err.Error()))
		case status == http.StatusOK && p.Account == "unconfirmed":
			writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
			return
		case status == http.StatusOK:
			writeJSON(w, http.StatusOK, map[string]string{"status": "maybe"})
			return
		case status == http.StatusTooManyRequests && gt.ErrorCode == "over_email_send_rate_limit" && strings.HasPrefix(gt.Msg, msgResendCooldownPrefix):
			writeJSON(w, http.StatusOK, map[string]string{"status": "held"})
			return
		case gt.ErrorCode == "over_email_send_rate_limit":
			log.WarnContext(r.Context(), "invitation-resend: gotrue email send rate limit", slog.Int("upstream_status", status))
		default:
			log.WarnContext(r.Context(), "invitation-resend: gotrue resend failed",
				slog.Int("upstream_status", status), slog.String("error_code", gt.ErrorCode))
		}
		writeError(w, http.StatusBadGateway, msgResendUnavailable)
	})
}
