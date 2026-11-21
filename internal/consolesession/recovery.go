// Package consolesession turns a verified IAP identity into a GoTrue recovery token hash.
package consolesession

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth/gotrueadmin"
)

// RecoveryLinker mints a recovery link for an address.
type RecoveryLinker interface {
	GenerateRecoveryLink(ctx context.Context, email string) (gotrueadmin.RecoveryLink, error)
}

// RecoveryTokenHandler is a compile stub for the red tests (CSSO-01-03); the executor replaces it.
func RecoveryTokenHandler(v *auth.IAPVerifier, admin RecoveryLinker, now func() time.Time, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not implemented", http.StatusNotImplemented)
	})
}
