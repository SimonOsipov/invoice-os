package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/notifications"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
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

var errNoEraseQueue = errors.New("tenancy: erase queue not configured")

// maxDeleteAccountBodyBytes bounds the POST /v1/me/deletion body before it is decoded.
const maxDeleteAccountBodyBytes = 4 << 10

// decideAccountDeletion answers what the caller's delete does. The heir is the earliest-joined
// other active member, ties to the lower user id.
func decideAccountDeletion(caller string, active []activeMember) (AccountDeletionOutcome, *activeMember) {
	var self *activeMember
	var heir *activeMember
	otherAdmins := false
	for i := range active {
		m := &active[i]
		if m.UserID == caller {
			self = m
			continue
		}
		if m.Role == "admin" {
			otherAdmins = true
		}
		if heir == nil || m.CreatedAt.Before(heir.CreatedAt) ||
			(m.CreatedAt.Equal(heir.CreatedAt) && m.UserID < heir.UserID) {
			heir = m
		}
	}
	switch {
	case heir == nil:
		return OutcomeDeleteWorkspace, nil
	case self == nil || self.Role != "admin" || otherAdmins:
		return OutcomeLeave, nil
	}
	return OutcomeHandOn, heir
}

// activeMembers reads the tenant's active rows by user id; lock adds FOR UPDATE.
func activeMembers(ctx context.Context, tx pgx.Tx, lock bool) ([]activeMember, error) {
	q := `SELECT user_id, role, display_name, email, created_at FROM memberships WHERE status = 'active' ORDER BY user_id`
	if lock {
		q += ` FOR UPDATE`
	}
	rows, err := tx.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []activeMember
	for rows.Next() {
		var m activeMember
		if err := rows.Scan(&m.UserID, &m.Role, &m.DisplayName, &m.Email, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PreviewAccountDeletion answers the outcome the delete would take now. Read-only, no row lock,
// so it also answers on a locked workspace.
func (s *Store) PreviewAccountDeletion(ctx context.Context) (AccountDeletionPreview, error) {
	var out AccountDeletionPreview
	err := db.WithinRequestTenantTxOpts(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		caller, _ := auth.IdentityFromContext(ctx)
		active, err := activeMembers(ctx, tx, false)
		if err != nil {
			return err
		}
		outcome, heir := decideAccountDeletion(caller.Subject, active)
		out = AccountDeletionPreview{Outcome: outcome}
		if heir != nil {
			out.HeirName = heirLabel(heir)
		}
		return nil
	})
	if err != nil {
		return AccountDeletionPreview{}, err
	}
	return out, nil
}

// heirLabel is the heir's display name, else the email when the name is blank.
func heirLabel(m *activeMember) *string {
	if m.DisplayName != nil && strings.TrimSpace(*m.DisplayName) != "" {
		return m.DisplayName
	}
	return m.Email
}

// DeleteMyAccount ends the caller and queues their erasure; the sole admin first hands admin to
// the heir. want must equal the outcome under the row locks.
// ceiling: the heir is chosen at commit, not named in the body; it changes only if the named heir
// deletes their own account at the same moment. Name the heir in the body if that bites.
func (s *Store) DeleteMyAccount(ctx context.Context, want AccountDeletionOutcome) error {
	if s.eraseQueue == nil {
		return errNoEraseQueue
	}
	return db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		caller, _ := auth.IdentityFromContext(ctx)
		active, err := activeMembers(ctx, tx, true)
		if err != nil {
			return err
		}
		got, heir := decideAccountDeletion(caller.Subject, active)
		if got != want {
			return ErrDeletionOutcomeChanged
		}
		var self *activeMember
		for i := range active {
			if active[i].UserID == caller.Subject {
				self = &active[i]
			}
		}
		if self == nil {
			return db.ErrNotActiveMember
		}
		email := caller.Email
		if self.Email != nil && strings.TrimSpace(*self.Email) != "" {
			email = *self.Email
		}
		if strings.TrimSpace(email) == "" {
			return ErrNoEraseEmail
		}
		if got == OutcomeHandOn {
			if _, err := tx.Exec(ctx, `UPDATE memberships SET role = 'admin' WHERE user_id = $1`, heir.UserID); err != nil {
				return err
			}
			if err := audit.Record(ctx, tx, caller.Subject, "membership.admin_handed_on",
				map[string]any{"user_id": heir.UserID, "from": heir.Role}); err != nil {
				return err
			}
		}
		if err := endMembership(ctx, tx, caller.Subject); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, caller.Subject, "membership.account_deleted",
			map[string]any{"user_id": caller.Subject, "role": self.Role}); err != nil {
			return err
		}
		return notifications.EnqueueErasure(ctx, tx, s.eraseQueue, caller.Subject, email)
	})
}

func (s *Store) SetEraseQueue(rc *river.Client[pgx.Tx]) { s.eraseQueue = rc }

// AccountDeletionPreviewHandler returns GET /v1/me/deletion: identity (401), preview, 200.
func AccountDeletionPreviewHandler(load PreviewAccountDeletionFunc, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		p, err := load(r.Context())
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "tenancy: preview account deletion", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
}

type deleteMyAccountRequest struct {
	Outcome AccountDeletionOutcome `json:"outcome"`
}

// DeleteMyAccountHandler returns POST /v1/me/deletion: identity (401), capped decode and outcome
// check (400), delete, 200 with the outcome. The body names no user.
func DeleteMyAccountHandler(del DeleteMyAccountFunc, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxDeleteAccountBodyBytes)
		var req deleteMyAccountRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
			(req.Outcome != OutcomeLeave && req.Outcome != OutcomeHandOn) {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := del(r.Context(), req.Outcome); err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "tenancy: delete my account", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, deleteMyAccountRequest{Outcome: req.Outcome})
	}
}
