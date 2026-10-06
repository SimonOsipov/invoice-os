package tenancy

import (
	"context"
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

// InvitationPreviewHandler is a red-phase stub.
func InvitationPreviewHandler(preview InvitationPreviewFunc, log *slog.Logger) http.HandlerFunc {
	return func(http.ResponseWriter, *http.Request) {}
}

// AcceptInvitationHandler is a red-phase stub.
func AcceptInvitationHandler(accept AcceptInvitationFunc, log *slog.Logger) http.HandlerFunc {
	return func(http.ResponseWriter, *http.Request) {}
}
