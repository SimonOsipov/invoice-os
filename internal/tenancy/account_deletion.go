package tenancy

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// AccountDeletionOutcome is what deleting the caller's account does to the workspace.
type AccountDeletionOutcome string

const (
	OutcomeLeave           AccountDeletionOutcome = "leave"
	OutcomeHandOn          AccountDeletionOutcome = "hand_on"
	OutcomeDeleteWorkspace AccountDeletionOutcome = "delete_workspace"
)

// AccountDeletionPreview is the outcome the delete would take now and, for hand_on, the heir's label.
type AccountDeletionPreview struct {
	Outcome  AccountDeletionOutcome `json:"outcome"`
	HeirName *string                `json:"heir_name"`
}

type activeMember struct {
	UserID, Role string
	DisplayName  *string
	Email        *string
	CreatedAt    time.Time
}

// PreviewAccountDeletionFunc and DeleteMyAccountFunc are the store seams of the two handlers.
type PreviewAccountDeletionFunc func(ctx context.Context) (AccountDeletionPreview, error)
type DeleteMyAccountFunc func(ctx context.Context, want AccountDeletionOutcome) error

var errNotImplemented = errors.New("tenancy: account deletion not implemented")

// Stubs for DEL-08-01 Test-Spec: the red tests compile against these.
func decideAccountDeletion(caller string, active []activeMember) (AccountDeletionOutcome, *activeMember) {
	return "", nil
}

func (s *Store) PreviewAccountDeletion(ctx context.Context) (AccountDeletionPreview, error) {
	return AccountDeletionPreview{}, errNotImplemented
}

func (s *Store) DeleteMyAccount(ctx context.Context, want AccountDeletionOutcome) error {
	return errNotImplemented
}

func (s *Store) SetEraseQueue(rc *river.Client[pgx.Tx]) { s.eraseQueue = rc }

func AccountDeletionPreviewHandler(load PreviewAccountDeletionFunc, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotImplemented) }
}

func DeleteMyAccountHandler(del DeleteMyAccountFunc, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotImplemented) }
}
