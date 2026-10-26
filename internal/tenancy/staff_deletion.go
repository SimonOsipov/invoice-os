package tenancy

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

var errStaffDeletionStub = errors.New("tenancy: staff deletion not implemented")

// StaffScheduleWorkspaceDeletion is a red-test stub; DEL-09-02 replaces it.
func (s *Store) StaffScheduleWorkspaceDeletion(ctx context.Context, tenantID, reason string) (time.Time, error) {
	return time.Time{}, errStaffDeletionStub
}

// StaffCancelWorkspaceDeletion is a red-test stub; DEL-09-02 replaces it.
func (s *Store) StaffCancelWorkspaceDeletion(ctx context.Context, tenantID, reason string) error {
	return errStaffDeletionStub
}

// StaffScheduleDeletionHandler is a red-test stub; DEL-09-02 replaces it.
func StaffScheduleDeletionHandler(schedule func(ctx context.Context, tenantID, reason string) (time.Time, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotImplemented) }
}

// StaffCancelDeletionHandler is a red-test stub; DEL-09-02 replaces it.
func StaffCancelDeletionHandler(cancel func(ctx context.Context, tenantID, reason string) error, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotImplemented) }
}
