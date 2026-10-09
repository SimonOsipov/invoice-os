package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
)

// InvitationPreview is what a token holder sees before signing in.
type InvitationPreview struct {
	Workspace string `json:"workspace"`
	Role      string `json:"role"`
	Email     string `json:"email"`
	Account   string `json:"account"` // none, unconfirmed, confirmed or unknown

	accountErr error // set with Account "unknown"; the handler logs its SQLSTATE
}

// logAccountStateErr warns with the SQLSTATE of a failed state read, never the address.
func logAccountStateErr(ctx context.Context, log *slog.Logger, err error) {
	if err == nil {
		return
	}
	state := "unknown"
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		state = pgErr.Code
	}
	log.WarnContext(ctx, "tenancy: invitee account state unavailable", slog.String("sqlstate", state))
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
// decode (400), preview, 200 `{workspace, role, email, account}`. The token is the only
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
		logAccountStateErr(r.Context(), log, p.accountErr)
		writeJSON(w, http.StatusOK, p)
	}
}

// writeAccepted writes the 200 `{tenant, user:{id, role}}` body both accept routes share.
func writeAccepted(w http.ResponseWriter, tenant Tenant, subject, role string) {
	var resp provisionResponse
	resp.Tenant.ID = tenant.ID
	resp.Tenant.Name = tenant.Name
	resp.Tenant.Kind = tenant.Kind
	resp.User.ID = subject
	resp.User.Role = role
	writeJSON(w, http.StatusOK, resp)
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
		writeAccepted(w, tenant, subject, role)
	}
}

// MyPendingInvitationsFunc lists the live invites for the caller's email.
type MyPendingInvitationsFunc func(context.Context) ([]PendingInvite, error)

// AcceptInvitationByIDFunc accepts invite id for the caller: tenant, subject, role.
type AcceptInvitationByIDFunc func(context.Context, string) (Tenant, string, string, error)

// InvitationsMineHandler returns GET /v1/invitations/mine: 200 `{"invitations":[…]}`,
// never null. The store reads the caller's email from the identity header.
func InvitationsMineHandler(list MyPendingInvitationsFunc, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		invites, err := list(r.Context())
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "tenancy: list pending invitations", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Invitations []PendingInvite `json:"invitations"`
		}{invites})
	}
}

// AcceptInvitationByIDHandler returns POST /v1/invitations/{id}/accept: no body
// is read; 200 in the token-accept shape. The store maps a non-uuid id to 404.
func AcceptInvitationByIDHandler(accept AcceptInvitationByIDFunc, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		tenant, subject, role, err := accept(r.Context(), r.PathValue("id"))
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "tenancy: accept invitation by id", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeAccepted(w, tenant, subject, role)
	}
}
