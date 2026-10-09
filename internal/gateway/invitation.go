package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
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
	msgInviteeFieldsRequired = "token and password are required"
	msgAccountExists         = "account_exists"
	msgAccountUnconfirmed    = "account_unconfirmed"
)

// NewHTTPInvitationPreviewer asks tenancy's internal preview route, with the gateway token and no identity.
// Its errors carry the status only: never the token, and no *url.Error, which holds the URL.
func NewHTTPInvitationPreviewer(base *url.URL, client *http.Client, gatewayToken string) InvitationPreviewer {
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	target := base.JoinPath(previewPath).String()
	return func(ctx context.Context, token string) (InvitationPreview, error) {
		ctx, cancel := context.WithTimeout(ctx, invitationPreviewTimeout)
		defer cancel()
		b, err := json.Marshal(map[string]string{"token": token})
		if err != nil {
			return InvitationPreview{}, errors.New("invitation preview: encode request")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(b))
		if err != nil {
			return InvitationPreview{}, errors.New("invitation preview: build request")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Gateway-Token", gatewayToken)
		resp, err := c.Do(req)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return InvitationPreview{}, errors.New("invitation preview: tenancy timed out")
			}
			return InvitationPreview{}, errors.New("invitation preview: tenancy unreachable")
		}
		defer resp.Body.Close()
		lr := io.LimitReader(resp.Body, maxPreviewResponseBytes)
		defer func() { _, _ = io.Copy(io.Discard, lr) }()
		switch resp.StatusCode {
		case http.StatusOK:
			var out struct {
				Workspace string `json:"workspace"`
				Role      string `json:"role"`
				Email     string `json:"email"`
				Account   string `json:"account"`
			}
			if err := json.NewDecoder(lr).Decode(&out); err != nil {
				return InvitationPreview{}, errors.New("invitation preview: tenancy answered 200 with an unreadable body")
			}
			switch out.Account {
			case "none", "unconfirmed", "confirmed":
			default:
				out.Account = "unknown"
			}
			return InvitationPreview{Workspace: out.Workspace, Role: out.Role, Email: out.Email, Account: out.Account}, nil
		case http.StatusNotFound:
			return InvitationPreview{}, ErrInvitationNotValid
		default:
			return InvitationPreview{}, fmt.Errorf("invitation preview: tenancy answered %d", resp.StatusCode)
		}
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

// InvitationRegisterHandler answers POST /auth/invitation/register: it signs the invited address up with GoTrue.
// The address comes from the invite, never from the body. Refusals before the signup answer at once;
// every other answer follows RegisterHandler's floor and its shared per-IP budget.
func InvitationRegisterHandler(authURL *url.URL, client *http.Client, minResponse time.Duration, perIP *SignInThrottle, enforce bool, log *slog.Logger, preview InvitationPreviewer) http.Handler {
	signup := authURL.JoinPath("signup").String()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !postOnly(w, r) {
			return
		}
		start := time.Now()
		var in struct {
			Token    string `json:"token"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRegisterBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if in.Token == "" || in.Password == "" {
			writeError(w, http.StatusBadRequest, msgInviteeFieldsRequired)
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
		case "unconfirmed":
			writeError(w, http.StatusConflict, msgAccountUnconfirmed)
			return
		}
		exists := func() { writeError(w, http.StatusConflict, msgAccountExists) }
		signUp(w, r, client, signup, map[string]any{"email": p.Email, "password": in.Password}, start, minResponse, perIP, enforce, log, exists)
	})
}
