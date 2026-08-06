package invoice

import (
	"context"
	"log/slog"
	"net/http"
)

// Stage-2.5 stub (BUG-04-02): ubl_test.go's specs are RED against these
// not-implemented bodies. The real handler lands in Stage 3.

// UBLHandler returns GET /v1/invoices/{id}/ubl.
func UBLHandler(get func(ctx context.Context, id string) (Invoice, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	}
}

// ublBlockedReason is nil when nothing is missing -- never a pointer to "".
// The stub returns exactly what that contract forbids, so the AC #5 spec is
// red rather than trivially satisfied.
func ublBlockedReason(missing []string) *string {
	empty := ""
	return &empty
}
