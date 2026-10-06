package gateway

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
)

// MockInvitationTokenHandler is a stub: the implementation replaces its body.
//
//go:noinline
func MockInvitationTokenHandler(set func(ctx context.Context, tenantID, invitationID uuid.UUID, token string) (bool, error), log *slog.Logger) http.Handler {
	return invitationStub()
}
