package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// MockStaffHandler answers POST /auth/mock/staff by calling grant with the body's user_id and optional rules_role.
//
//go:noinline
func MockStaffHandler(grant func(ctx context.Context, userID uuid.UUID, rulesRole bool) error, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !postOnly(w, r) {
			return
		}
		var in struct {
			UserID    string          `json:"user_id"`
			RulesRole json.RawMessage `json:"rules_role"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExchangeBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		userID, err := uuid.Parse(in.UserID)
		// uuid.Parse also accepts 32-, 36+2- and urn:uuid: forms; only the hyphenated one is a user_id.
		if err != nil || userID.String() != strings.ToLower(in.UserID) {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		// Raw, so JSON null is refused like any non-bool instead of reading as absent.
		var rulesRole bool
		if in.RulesRole != nil && (string(in.RulesRole) == "null" || json.Unmarshal(in.RulesRole, &rulesRole) != nil) {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := grant(r.Context(), userID, rulesRole); err != nil {
			log.WarnContext(r.Context(), "mock staff: grant failed", slog.String("error", err.Error()))
			writeError(w, http.StatusBadGateway, "staff grant unavailable")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
