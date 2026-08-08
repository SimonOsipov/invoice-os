package tenancy

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Store reads tenancy data as the invoice_app role. It holds the app-role pool
// (DATABASE_URL); every read goes through db.WithinRequestTenantTx, so the
// app.current_tenant GUC is set for the transaction and RLS enforces isolation.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps the app-role connection pool. The caller owns the pool's lifecycle.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Me returns the caller's tenant (id, name, kind) and their domain role, both
// resolved under RLS: SELECT id, name, kind FROM tenants (bare — the
// app.current_tenant GUC is the filter, not a WHERE clause) then SELECT role FROM
// memberships WHERE user_id = $1 (identity.Subject, read inside the closure — RLS
// scopes the row set to the current tenant). No visible tenant row maps to
// ErrTenantNotFound; no membership row maps to ErrNoMembership (never defaulted).
//
// Both queries run inside the SAME db.WithinRequestTenantTx call, so a missing
// tenant row surfaces as ErrTenantNotFound before the membership query ever runs.
func (s *Store) Me(ctx context.Context) (Tenant, string, error) {
	var t Tenant
	var role string
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id, name, kind FROM tenants`).Scan(&t.ID, &t.Name, &t.Kind); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTenantNotFound
			}
			return err
		}

		// The identity is guaranteed present here: WithinRequestTenantTx already
		// resolved it (as the tenant id) before this closure ran, returning
		// db.ErrNoTenant otherwise.
		id, _ := auth.IdentityFromContext(ctx)
		if err := tx.QueryRow(ctx,
			`SELECT role FROM memberships WHERE user_id = $1`, id.Subject,
		).Scan(&role); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoMembership
			}
			return err
		}
		return nil
	})
	if err != nil {
		return Tenant{}, "", err
	}
	return t, role, nil
}

// ListMemberships lists the caller's tenant's memberships (user_id, role,
// status, display_name, email), RLS-scoped: SELECT user_id, role, status,
// display_name, email FROM memberships ORDER BY created_at, user_id -- bare
// (no WHERE tenant_id), same as Me's tenant query, inside a SINGLE
// db.WithinRequestTenantTx call. An empty tenant returns an empty non-nil
// slice and a nil error (never nil, nil).
func (s *Store) ListMemberships(ctx context.Context) ([]Membership, error) {
	memberships := []Membership{}
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT user_id, role, status, display_name, email FROM memberships ORDER BY created_at, user_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m Membership
			if err := rows.Scan(&m.UserID, &m.Role, &m.Status, &m.DisplayName, &m.Email); err != nil {
				return err
			}
			memberships = append(memberships, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return memberships, nil
}

// SetMembershipStatus applies an admin-directed status change to one membership
// row and audits it in the same transaction. Statement order is the security
// property, not a style choice:
//
// The caller's role is read FIRST, unlocked and taking no target argument, so a
// non-admin is refused identically whether the target exists, belongs to another
// tenant, or is garbage — there is no 403-vs-404 existence oracle
// (TestMembership_PermissionCheckedBeforeRowRead). That read carries
// AND status = 'active' so a suspended admin cannot reactivate themselves
// (TestMembership_SuspendedAdminCannotAdminister).
//
// The second statement locks the target AND the whole active-admin set in
// user_id order: the last-active-admin guard then runs over rows no concurrent
// PATCH can move under it, and contention waits instead of deadlocking. A plain
// count, or a lock on the target alone, would let two concurrent PATCHes strand
// the tenant at zero active admins (TestMembership_ConcurrentLastTwoAdmins).
// Both statements are bare of any tenant_id clause — RLS scopes them, so a
// cross-tenant target is simply absent from the result.
//
// Guards run in this order: target missing -> target invited -> already at the
// requested status (200 no-op, no UPDATE, no audit) -> last active admin. The
// no-op must precede the last-admin guard, or re-suspending an already-suspended
// sole admin would 409 a request that changes nothing.
func (s *Store) SetMembershipStatus(ctx context.Context, userID, status string) (Membership, error) {
	// Re-validated here, not only in the handler, so a direct caller fails
	// closed rather than writing an out-of-vocabulary status.
	if status != "active" && status != "suspended" {
		return Membership{}, ErrInvalidStatus
	}

	var updated Membership
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Guaranteed present: WithinRequestTenantTx resolved it before this ran.
		caller, _ := auth.IdentityFromContext(ctx)

		var callerRole string
		if err := tx.QueryRow(ctx,
			`SELECT role FROM memberships WHERE user_id = $1 AND status = 'active'`, caller.Subject,
		).Scan(&callerRole); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotPermitted
			}
			return err
		}
		if callerRole != "admin" {
			return ErrNotPermitted
		}

		// is_target is computed by Postgres so which row is the target never
		// depends on the text form of a uuid agreeing between SQL and Go.
		rows, err := tx.Query(ctx,
			`SELECT user_id, role, status, display_name, email, user_id = $1 AS is_target
			   FROM memberships
			  WHERE user_id = $1 OR (role = 'admin' AND status = 'active')
			  ORDER BY user_id
			    FOR UPDATE`, userID)
		if err != nil {
			return err
		}
		var locked []Membership
		targetIdx := -1
		for rows.Next() {
			var m Membership
			var isTarget bool
			if err := rows.Scan(&m.UserID, &m.Role, &m.Status, &m.DisplayName, &m.Email, &isTarget); err != nil {
				rows.Close()
				return err
			}
			if isTarget {
				targetIdx = len(locked)
			}
			locked = append(locked, m)
		}
		// Closed explicitly rather than deferred: the UPDATE below reuses this
		// transaction's connection.
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		if targetIdx < 0 {
			return ErrMembershipNotFound
		}
		target := locked[targetIdx]
		if target.Status == "invited" {
			return ErrInvitedNotTransitionable
		}
		if target.Status == status {
			updated = target
			return nil
		}
		if status == "suspended" && target.Role == "admin" {
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

		from := target.Status
		if err := tx.QueryRow(ctx,
			`UPDATE memberships SET status = $2 WHERE user_id = $1
			 RETURNING user_id, role, status, display_name, email`, userID, status,
		).Scan(&updated.UserID, &updated.Role, &updated.Status, &updated.DisplayName, &updated.Email); err != nil {
			return err
		}

		event := "membership.reactivated"
		if status == "suspended" {
			event = "membership.suspended"
		}
		// Last statement in the closure: a failing audit write aborts the tx and
		// rolls the status change back.
		return audit.Record(ctx, tx, caller.Subject, event, map[string]any{
			"user_id": userID,
			"from":    from,
			"to":      status,
		})
	})
	if err != nil {
		return Membership{}, err
	}
	return updated, nil
}
