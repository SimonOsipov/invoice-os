package staffjobs

// Compile stubs for SUPP-06-05 so the red tests fail at their assertions. The executor
// replaces them in store.go and handlers.go and deletes this file.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/audit"
)

var (
	ErrJobState       = errors.New("staffjobs: job is not in the required state")
	ErrAcknowledged   = errors.New("staffjobs: the Access Point acknowledged this job")
	ErrRiverDuplicate = errors.New("staffjobs: another River job holds this submission")
	ErrRiverRunning   = errors.New("staffjobs: the previous River job is running")
)

// recordStaff is a var so a test can force an audit failure.
var recordStaff = audit.RecordStaff

func (s *Store) Redrive(ctx context.Context, id, reason string) (Detail, error) {
	return Detail{}, errors.New("not implemented")
}

func RedriveHandler(redrive func(ctx context.Context, id, reason string) (Detail, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotImplemented) }
}
