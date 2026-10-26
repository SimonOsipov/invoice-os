package tenancy

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
)

// Signature stubs until DEL-09-05 is implemented.

func (s *Store) StaffPreviewAccountDeletion(ctx context.Context, tenantID, userID string) (AccountDeletionPreview, error) {
	return AccountDeletionPreview{}, errors.New("tenancy: StaffPreviewAccountDeletion not implemented")
}

func (s *Store) StaffDeleteAccount(ctx context.Context, tenantID, userID string, want AccountDeletionOutcome, heirUserID, reason string) error {
	return errors.New("tenancy: StaffDeleteAccount not implemented")
}

func StaffAccountDeletionPreviewHandler(load func(ctx context.Context, tenantID, userID string) (AccountDeletionPreview, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	}
}

func StaffDeleteAccountHandler(del func(ctx context.Context, tenantID, userID string, want AccountDeletionOutcome, heirUserID, reason string) error, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	}
}
