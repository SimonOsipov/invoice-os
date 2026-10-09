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
type InvitationPreview struct{ Workspace, Role, Email string }

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
	msgInviteeFieldsRequired = "token and password are required"
)

// postTenancy posts payload to an internal tenancy route with the gateway token and no redirect.
// On a 200 it decodes the body into out and fails if it cannot. It returns the status; its errors
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
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(lr).Decode(out); err != nil {
			return resp.StatusCode, errors.New(label + ": tenancy answered 200 with an unreadable body")
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
		}
		status, err := postTenancy(ctx, c, target, gatewayToken, "invitation preview", map[string]string{"token": token}, &out)
		switch {
		case err != nil:
			return InvitationPreview{}, err
		case status == http.StatusOK:
			return InvitationPreview{Workspace: out.Workspace, Role: out.Role, Email: out.Email}, nil
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
func NewHTTPPendingInviteLookup(base *url.URL, client *http.Client, gatewayToken string) PendingInviteLookup {
	c := noRedirect(client)
	target := base.JoinPath(pendingPath).String()
	return func(ctx context.Context, email string) (bool, error) {
		var out struct {
			Pending bool `json:"pending"`
		}
		status, err := postTenancy(ctx, c, target, gatewayToken, "pending invite lookup", map[string]string{"email": email}, &out)
		if err != nil {
			return false, err
		}
		if status != http.StatusOK {
			return false, fmt.Errorf("pending invite lookup: tenancy answered %d", status)
		}
		return out.Pending, nil
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

// InvitationHandler answers POST /auth/invitation with the workspace, role and address a live token names.
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
		writeJSON(w, http.StatusOK, map[string]string{"workspace": p.Workspace, "role": p.Role, "email": p.Email})
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
		signUp(w, r, client, signup, map[string]any{"email": p.Email, "password": in.Password}, start, minResponse, perIP, enforce, log, nil)
	})
}
