package tenancy

import (
	"context"
	"log/slog"
	"net/http"
)

// DEL-02-03 red-commit stubs. The executor swaps each for the real signature
// (D11, D13, D26), repoints the tests that call it, and deletes this file.

// MyInvitations is the GET /v1/invitations/mine body (D11).
type MyInvitations struct {
	Invitations []PendingInvite `json:"invitations"`
	Removed     bool            `json:"removed"`
}

func checkName(field, v string) string { return "" }

func (s *Store) AcceptInvitationNamed(ctx context.Context, token string, _ *string) (Tenant, string, string, error) {
	return s.AcceptInvitation(ctx, token)
}

func (s *Store) AcceptInvitationByIDNamed(ctx context.Context, id string, _ *string) (Tenant, string, string, error) {
	return s.AcceptInvitationByID(ctx, id)
}

func (s *Store) MyInvitationsMine(ctx context.Context) (MyInvitations, error) {
	list, err := s.MyPendingInvitations(ctx)
	return MyInvitations{Invitations: list}, err
}

func AcceptInvitationNamedHandler(accept func(ctx context.Context, token string, displayName *string) (Tenant, string, string, error), log *slog.Logger) http.HandlerFunc {
	return AcceptInvitationHandler(func(ctx context.Context, token string) (Tenant, string, string, error) {
		return accept(ctx, token, nil)
	}, log)
}

func AcceptInvitationByIDNamedHandler(accept func(ctx context.Context, id string, displayName *string) (Tenant, string, string, error), log *slog.Logger) http.HandlerFunc {
	return AcceptInvitationByIDHandler(func(ctx context.Context, id string) (Tenant, string, string, error) {
		return accept(ctx, id, nil)
	}, log)
}

func InvitationsMineStateHandler(list func(context.Context) (MyInvitations, error), log *slog.Logger) http.HandlerFunc {
	return InvitationsMineHandler(func(ctx context.Context) ([]PendingInvite, error) {
		m, err := list(ctx)
		return m.Invitations, err
	}, log)
}
