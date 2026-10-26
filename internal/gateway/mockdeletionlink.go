package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
)

// MockDeletionLinkTokenHandler answers POST /auth/mock/deletion-link-token by calling set with the parsed body.
// set reports whether the admin had a live cancel link.
//
//go:noinline
func MockDeletionLinkTokenHandler(set func(ctx context.Context, tenantID, userID uuid.UUID, token string) (bool, error), log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !postOnly(w, r) {
			return
		}
		var in struct {
			TenantID string `json:"tenant_id"`
			UserID   string `json:"user_id"`
			Token    string `json:"token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExchangeBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		tenantID, okTenant := parseHyphenatedUUID(in.TenantID)
		userID, okUser := parseHyphenatedUUID(in.UserID)
		if !okTenant || !okUser || !stateShape.MatchString(in.Token) {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		found, err := set(r.Context(), tenantID, userID, in.Token)
		if err != nil {
			log.WarnContext(r.Context(), "mock deletion link token: set failed", slog.String("error", err.Error()))
			writeError(w, http.StatusBadGateway, "deletion link token unavailable")
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "deletion link not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
