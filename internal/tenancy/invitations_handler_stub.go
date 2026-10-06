package tenancy

// RED STUB (compile-only). The executor deletes this file and implements these
// symbols in invitations_handler.go and tenancy.go.

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

type Inviter struct {
	Store  *Store
	Sender accountmail.Sender
	Logger *slog.Logger
}

type InviteResult struct{}

func (i *Inviter) Invite(ctx context.Context, emails []string, role string) ([]InviteResult, error) {
	return nil, nil
}

func (i *Inviter) Resend(ctx context.Context, id string) (InviteResult, error) {
	return InviteResult{}, nil
}

type InviteFunc func(ctx context.Context, emails []string, role string) ([]InviteResult, error)

type InvitationsLister func(ctx context.Context) ([]Invitation, error)

type InviteResender func(ctx context.Context, id string) (InviteResult, error)

func stubHandler(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "stub")
}

func InvitationsCreateHandler(invite InviteFunc, log *slog.Logger) http.HandlerFunc {
	return stubHandler
}

func InvitationsListHandler(list InvitationsLister, log *slog.Logger) http.HandlerFunc {
	return stubHandler
}

func InvitationResendHandler(resend InviteResender, log *slog.Logger) http.HandlerFunc {
	return stubHandler
}
