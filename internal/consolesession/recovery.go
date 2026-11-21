// Package consolesession turns a verified IAP identity into a GoTrue recovery token hash.
package consolesession

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth/gotrueadmin"
)

const maxBody = 8 << 10

// RecoveryLinker mints a recovery link for an address.
type RecoveryLinker interface {
	GenerateRecoveryLink(ctx context.Context, email string) (gotrueadmin.RecoveryLink, error)
}

// RecoveryTokenHandler answers POST {assertion} with {token_hash} for a confirmed, unbanned account.
// The address comes only from the verified assertion, never from the body. Logs carry no identity.
func RecoveryTokenHandler(v *auth.IAPVerifier, admin RecoveryLinker, now func() time.Time, log *slog.Logger) http.Handler {
	reply := func(w http.ResponseWriter, status int, body map[string]string) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
			reply(w, http.StatusUnsupportedMediaType, map[string]string{"error": "content type must be application/json"})
			return
		}
		var in struct {
			Assertion string `json:"assertion"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&in) != nil {
			reply(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		id, err := v.Verify(r.Context(), in.Assertion)
		if err != nil {
			log.Warn("console recovery token: assertion refused")
			reply(w, http.StatusUnauthorized, map[string]string{"error": "no verified Google identity"})
			return
		}
		link, err := admin.GenerateRecoveryLink(r.Context(), id.Email)
		var se *gotrueadmin.StatusError
		switch {
		case errors.Is(err, gotrueadmin.ErrNoAccount):
			log.Info("console recovery token: no staff account")
			reply(w, http.StatusForbidden, map[string]string{"error": "no staff account"})
		case err != nil:
			status := 0
			if errors.As(err, &se) {
				status = se.Status
			}
			log.Error("console recovery token: GoTrue failed", "status", status)
			reply(w, http.StatusBadGateway, map[string]string{"error": "console sign-in is unavailable"})
		case !link.Confirmed || (link.BannedUntil != nil && link.BannedUntil.After(now())):
			log.Info("console recovery token: account not confirmed or banned")
			reply(w, http.StatusForbidden, map[string]string{"error": "no staff account"})
		default:
			reply(w, http.StatusOK, map[string]string{"token_hash": link.TokenHash})
		}
	})
}
