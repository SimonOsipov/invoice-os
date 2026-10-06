package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
)

// MockInvitationTokenHandler answers POST /auth/mock/invitation-token by calling set with the parsed body.
// set reports whether a pending invite matched.
//
//go:noinline
func MockInvitationTokenHandler(set func(ctx context.Context, tenantID, invitationID uuid.UUID, token string) (bool, error), log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !postOnly(w, r) {
			return
		}
		var in struct {
			TenantID     string `json:"tenant_id"`
			InvitationID string `json:"invitation_id"`
			Token        string `json:"token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExchangeBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		tenantID, okTenant := parseHyphenatedUUID(in.TenantID)
		invitationID, okInvitation := parseHyphenatedUUID(in.InvitationID)
		if !okTenant || !okInvitation || !stateShape.MatchString(in.Token) {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		found, err := set(r.Context(), tenantID, invitationID, in.Token)
		if err != nil {
			log.WarnContext(r.Context(), "mock invitation token: set failed", slog.String("error", err.Error()))
			writeError(w, http.StatusBadGateway, "invitation token unavailable")
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "invitation not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
