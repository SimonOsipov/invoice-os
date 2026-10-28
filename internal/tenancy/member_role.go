package tenancy

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

var (
	ErrRoleChangeNotPermitted = errors.New("tenancy: role change not permitted")
	ErrInvalidRole            = errors.New("tenancy: invalid role")
)

// SetMemberRole changes one member's role and audits it in the same transaction.
// Guards: caller admin -> target found -> invited -> removed -> same role (no-op) -> last active admin.
func (s *Store) SetMemberRole(ctx context.Context, userID, role string) (MemberAccess, error) {
	if role != "admin" && role != "preparer" && role != "reviewer" {
		return MemberAccess{}, ErrInvalidRole
	}

	var out MemberAccess
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		caller, _ := auth.IdentityFromContext(ctx)
		if err := requireActiveAdminCaller(ctx, tx, caller.Subject, ErrRoleChangeNotPermitted); err != nil {
			return err
		}
		locked, targetIdx, err := lockTargetAndAdmins(ctx, tx, userID)
		if err != nil {
			return err
		}
		if targetIdx < 0 {
			return ErrMembershipNotFound
		}
		target := locked[targetIdx]
		if target.Status == "invited" {
			return ErrInvitedNotTransitionable
		}
		if target.Status == "removed" {
			return ErrRemovedNotTransitionable
		}
		if target.Role == role {
			return readTarget(ctx, tx, target.UserID, &out)
		}
		if target.Role == "admin" && target.Status == "active" {
			others := 0
			for i, m := range locked {
				if i != targetIdx && m.Role == "admin" && m.Status == "active" {
					others++
				}
			}
			if others == 0 {
				return ErrLastActiveAdmin
			}
		}

		var id string
		if err := tx.QueryRow(ctx,
			`UPDATE memberships SET role = $2 WHERE user_id = $1 RETURNING user_id::text`, userID, role,
		).Scan(&id); err != nil {
			return err
		}

		// A demoted firm admin keeps every client; "all" has one stored form (SetMemberClients).
		if target.Role == "admin" {
			var kind string
			if err := tx.QueryRow(ctx, `SELECT kind FROM tenants`).Scan(&kind); err != nil {
				return err
			}
			if kind == "firm" {
				if _, err := tx.Exec(ctx, `UPDATE memberships SET all_clients = true WHERE user_id = $1`, id); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `DELETE FROM membership_clients WHERE user_id = $1`, id); err != nil {
					return err
				}
			}
		}

		if err := audit.Record(ctx, tx, caller.Subject, "membership.role_changed", map[string]any{
			"user_id": id,
			"from":    target.Role,
			"to":      role,
		}); err != nil {
			return err
		}
		return readTarget(ctx, tx, id, &out)
	})
	if err != nil {
		return MemberAccess{}, err
	}
	return out, nil
}
