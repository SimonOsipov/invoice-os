package tenancy

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// ScheduleDeletionFunc schedules the caller's workspace for deletion and returns the due time.
type ScheduleDeletionFunc func(ctx context.Context, typedName string) (time.Time, error)

// CancelDeletionFunc clears the caller's workspace deletion schedule.
type CancelDeletionFunc func(ctx context.Context) error

var errNotImplemented = errors.New("tenancy: workspace deletion not implemented")

// ScheduleWorkspaceDeletion is an inert stub; DEL-04-03 replaces it.
func (s *Store) ScheduleWorkspaceDeletion(ctx context.Context, typedName string) (time.Time, error) {
	return time.Time{}, errNotImplemented
}

// CancelWorkspaceDeletion is an inert stub; DEL-04-03 replaces it.
func (s *Store) CancelWorkspaceDeletion(ctx context.Context) error {
	return errNotImplemented
}

// ScheduleDeletionHandler is an inert stub; DEL-04-03 replaces it.
func ScheduleDeletionHandler(schedule ScheduleDeletionFunc, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	}
}

// CancelDeletionHandler is an inert stub; DEL-04-03 replaces it.
func CancelDeletionHandler(cancel CancelDeletionFunc, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	}
}
