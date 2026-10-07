package tenancy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

// InvitationPreview is what a token holder sees before signing in.
type InvitationPreview struct {
	Workspace string `json:"workspace"`
	Role      string `json:"role"`
	Email     string `json:"email"`
}

// InvitationPreviewFunc looks up a live invite by token.
type InvitationPreviewFunc func(ctx context.Context, token string) (InvitationPreview, error)

// AcceptInvitationFunc accepts an invite for the caller: tenant, subject, role.
type AcceptInvitationFunc func(ctx context.Context, token string) (Tenant, string, string, error)

// Refusal messages for the invite routes; statusForErr maps the sentinels to them.
const (
	msgInviteNotValid = "this invite is no longer valid"
	msgAlreadyMember  = "you already belong to a workspace"
	msgWrongAddress   = "this invite was sent to a different email address"
)

// maxInviteTokenBodyBytes bounds the {"token"} body before it is decoded.
const maxInviteTokenBodyBytes = 1 << 10

// inviteTokenRequest is the wire body of both invite routes.
type inviteTokenRequest struct {
	Token string `json:"token"`
}

// readInviteToken decodes the capped body; it answers 400 itself on failure.
func readInviteToken(w http.ResponseWriter, r *http.Request) (string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxInviteTokenBodyBytes)
	var req inviteTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return "", false
	}
	return req.Token, true
}

// InvitationPreviewHandler returns POST /internal/invitations/preview: capped
// decode (400), preview, 200 `{workspace, role, email}`. The token is the only
// credential; the handler reads no identity.
func InvitationPreviewHandler(preview InvitationPreviewFunc, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := readInviteToken(w, r)
		if !ok {
			return
		}
		p, err := preview(r.Context(), token)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "tenancy: preview invitation", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
}

// AcceptInvitationHandler returns POST /v1/invitations/accept: capped decode
// (400), accept, 200 `{tenant, user:{id, role}}`. The caller check is the
// store's: the handler cannot see a tenant-less caller.
func AcceptInvitationHandler(accept AcceptInvitationFunc, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := readInviteToken(w, r)
		if !ok {
			return
		}
		tenant, subject, role, err := accept(r.Context(), token)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "tenancy: accept invitation", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		var resp provisionResponse
		resp.Tenant.ID = tenant.ID
		resp.Tenant.Name = tenant.Name
		resp.Tenant.Kind = tenant.Kind
		resp.User.ID = subject
		resp.User.Role = role
		writeJSON(w, http.StatusOK, resp)
	}
}
