package gateway

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
)

// MemberGrant is the parsed body of POST /auth/mock/member.
type MemberGrant struct {
	TenantID    uuid.UUID
	UserID      uuid.UUID
	Role        string
	DisplayName string
	Email       string
}

// MockMemberHandler is a signature-only stub (AUTH-15-02 Mode A): it grants a zero value and answers 204.
//
//go:noinline
func MockMemberHandler(grant func(ctx context.Context, g MemberGrant) error, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = grant(r.Context(), MemberGrant{})
		w.WriteHeader(http.StatusNoContent)
	})
}
