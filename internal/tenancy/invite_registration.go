package tenancy

import (
	"context"
	"log/slog"
	"net/http"
)

// Red-phase stubs (RESEND2-01-02): zero values, no behaviour. The executor replaces this file.

// InvitationPendingForEmail reports whether the address has a live invite in any tenant.
func (s *Store) InvitationPendingForEmail(ctx context.Context, email string) (bool, error) {
	return false, nil
}

// ClaimInvitationRegistration spends the token's one registration; first is false after the first claim.
func (s *Store) ClaimInvitationRegistration(ctx context.Context, token string) (email string, first bool, err error) {
	return "", false, nil
}

// ReleaseInvitationRegistration clears the token's own claim.
func (s *Store) ReleaseInvitationRegistration(ctx context.Context, token string) error {
	return nil
}

// InvitationPendingHandler returns POST /internal/invitations/pending.
func InvitationPendingHandler(pending func(ctx context.Context, email string) (bool, error), log *slog.Logger) http.HandlerFunc {
	return func(http.ResponseWriter, *http.Request) {}
}

// InvitationRegisterClaimHandler returns POST /internal/invitations/register.
func InvitationRegisterClaimHandler(claim func(ctx context.Context, token string) (string, bool, error), log *slog.Logger) http.HandlerFunc {
	return func(http.ResponseWriter, *http.Request) {}
}

// InvitationRegisterReleaseHandler returns POST /internal/invitations/release.
func InvitationRegisterReleaseHandler(release func(ctx context.Context, token string) error, log *slog.Logger) http.HandlerFunc {
	return func(http.ResponseWriter, *http.Request) {}
}
