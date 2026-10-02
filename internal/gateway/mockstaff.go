package gateway

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
)

// MockStaffHandler answers POST /auth/mock/staff by calling grant with the body's user_id.
func MockStaffHandler(grant func(ctx context.Context, userID uuid.UUID) error, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	})
}
