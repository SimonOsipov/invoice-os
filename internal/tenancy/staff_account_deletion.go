package tenancy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/notifications"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// targetUserID is the canonical path user id; a malformed one is no member.
func targetUserID(userID string) (string, error) {
	id, err := uuid.Parse(userID)
	if err != nil {
		return "", ErrMembershipNotFound
	}
	return id.String(), nil
}

// refuseHouseTenant answers ErrHouseWorkspace when the path tenant is the house one: the staff caller is its member.
func refuseHouseTenant(ctx context.Context, tx pgx.Tx, actor uuid.UUID) error {
	var house bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM memberships WHERE user_id = $1 AND status = 'active')`, actor).Scan(&house); err != nil {
		return err
	}
	if house {
		return ErrHouseWorkspace
	}
	return nil
}

// StaffPreviewAccountDeletion answers the decideAccountDeletion outcome for the path member. Read-only, so it also answers on a locked workspace.
func (s *Store) StaffPreviewAccountDeletion(ctx context.Context, tenantID, userID string) (AccountDeletionPreview, error) {
	var out AccountDeletionPreview
	err := s.withinStaffTenant(ctx, tenantID, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx, _, actor uuid.UUID) error {
		if err := refuseHouseTenant(ctx, tx, actor); err != nil {
			return err
		}
		target, err := targetUserID(userID)
		if err != nil {
			return err
		}
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM memberships WHERE user_id = $1`, target).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrMembershipNotFound
			}
			return err
		}
		if status == "removed" {
			return ErrMembershipNotFound
		}
		active, err := activeMembers(ctx, tx, false)
		if err != nil {
			return err
		}
		outcome, heir := decideAccountDeletion(target, active)
		out = AccountDeletionPreview{Outcome: outcome}
		if heir != nil {
			out.HeirName = heirLabel(heir)
			out.HeirUserID = &heir.UserID
		}
		return nil
	})
	if err != nil {
		return AccountDeletionPreview{}, err
	}
	return out, nil
}

// StaffDeleteAccount is DeleteMyAccount for the path member, with a staff row beside each customer row.
// want and, for hand_on, heirUserID must equal the decision under the row locks.
func (s *Store) StaffDeleteAccount(ctx context.Context, tenantID, userID string, want AccountDeletionOutcome, heirUserID, reason string) error {
	if s.eraseQueue == nil {
		return errNoEraseQueue
	}
	return s.withinStaffTenant(ctx, tenantID, pgx.TxOptions{}, func(tx pgx.Tx, tenant, actor uuid.UUID) error {
		if err := refuseHouseTenant(ctx, tx, actor); err != nil {
			return err
		}
		if locked, err := db.WorkspaceLocked(ctx, tx); err != nil {
			return err
		} else if locked {
			return db.ErrWorkspaceLocked
		}
		target, err := targetUserID(userID)
		if err != nil {
			return err
		}
		// Same lock order as DeleteMyAccount: every active row and the target, by user id.
		rows, err := tx.Query(ctx, `SELECT user_id, role, status, display_name, email, created_at FROM memberships
			WHERE status = 'active' OR user_id = $1 ORDER BY user_id FOR UPDATE`, target)
		if err != nil {
			return err
		}
		defer rows.Close()
		var active []activeMember
		var self *activeMember
		for rows.Next() {
			var m activeMember
			var status string
			if err := rows.Scan(&m.UserID, &m.Role, &status, &m.DisplayName, &m.Email, &m.CreatedAt); err != nil {
				return err
			}
			if status == "active" {
				active = append(active, m)
			}
			if m.UserID == target && status != "removed" {
				c := m
				self = &c
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		if self == nil {
			return ErrMembershipNotFound
		}
		got, heir := decideAccountDeletion(target, active)
		if got == OutcomeDeleteWorkspace {
			return ErrLastActiveMember
		}
		if got != want || (got == OutcomeHandOn && heir.UserID != heirUserID) {
			return ErrDeletionOutcomeChanged
		}
		if got == OutcomeHandOn {
			if _, err := tx.Exec(ctx, `UPDATE memberships SET role = 'admin' WHERE user_id = $1`, heir.UserID); err != nil {
				return err
			}
			if err := audit.RecordStaff(ctx, tx, audit.StaffEvent{
				Actor: actor, Event: "membership.admin_handed_on", TenantID: tenant, SubjectID: uuid.MustParse(heir.UserID),
				Payload: map[string]any{"user_id": heir.UserID, "from": heir.Role, "to": "admin", "reason": reason},
			}); err != nil {
				return err
			}
		}
		if err := endMembership(ctx, tx, target); err != nil {
			return err
		}
		if err := audit.RecordStaff(ctx, tx, audit.StaffEvent{
			Actor: actor, Event: "membership.account_deleted", TenantID: tenant, SubjectID: uuid.MustParse(target),
			Payload: map[string]any{"user_id": target, "role": self.Role, "reason": reason},
		}); err != nil {
			return err
		}
		if self.Email != nil && notifications.UsableEraseEmail(*self.Email) {
			return notifications.EnqueueErasure(ctx, tx, s.eraseQueue, target, *self.Email)
		}
		return notifications.EnqueueErasureByID(ctx, tx, s.eraseQueue, target)
	})
}

// StaffAccountDeletionPreviewHandler repeats no staff check: auth.RequireStaff runs before it.
func StaffAccountDeletionPreviewHandler(load func(ctx context.Context, tenantID, userID string) (AccountDeletionPreview, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := load(r.Context(), r.PathValue("id"), r.PathValue("user_id"))
		if err != nil {
			writeStaffDeletionError(w, log, "tenancy: staff preview account deletion response", err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
}

type staffDeleteAccountRequest struct {
	deleteMyAccountRequest
	Reason string `json:"reason"`
}

// StaffDeleteAccountHandler repeats no staff check: auth.RequireStaff runs before it.
func StaffDeleteAccountHandler(del func(ctx context.Context, tenantID, userID string, want AccountDeletionOutcome, heirUserID, reason string) error, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req staffDeleteAccountRequest
		if !readStaffBody(w, r, &req) {
			return
		}
		heir, ok := deleteBodyHeir(req.deleteMyAccountRequest)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		reason, ok := staffReason(req.Reason)
		if !ok {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("reason must be 1 to %d characters", maxStaffReasonRunes))
			return
		}
		if err := del(r.Context(), r.PathValue("id"), r.PathValue("user_id"), req.Outcome, heir, reason); err != nil {
			writeStaffDeletionError(w, log, "tenancy: staff delete account response", err)
			return
		}
		writeJSON(w, http.StatusOK, deleteMyAccountResponse{Outcome: req.Outcome})
	}
}
