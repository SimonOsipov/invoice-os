package approval

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Store reads and writes workflow roles as the invoice_app role. It holds the
// app-role pool (DATABASE_URL); every call runs one db.WithinRequestTenantTx, so
// the app.current_tenant GUC is set for the transaction and RLS is the only tenant
// filter — no statement below carries a tenant_id predicate.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps the app-role connection pool. The caller owns the pool's lifecycle.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// The handler seam 06 wires HTTP to. Declared beside the methods that satisfy them
// (internal/tenancy puts its function types next to the handlers instead).
type (
	RolesLister func(ctx context.Context) ([]Role, error)
	RoleCreator func(ctx context.Context, title, desc string) (Role, error)
	RoleUpdater func(ctx context.Context, key string, title, desc *string) (Role, error)
	RoleDeleter func(ctx context.Context, key string) (Role, error)
)

var (
	_ RolesLister = new(Store).ListRoles
	_ RoleCreator = new(Store).CreateRole
	_ RoleUpdater = new(Store).UpdateRole
	_ RoleDeleter = new(Store).DeleteRole
)

// ListRoles returns the tenant's live workflow roles, each with its members in that
// role's own `ord` order. Two statements, constant in the number of roles: the roles,
// then every role's staffing in ONE query, grouped in Go.
//
// No access-role gate and no memberships read at all — any caller holding a tenant
// claim may list, the same as GET /v1/memberships
// (TestWorkflowRole_ListNeedsNoAdminRole, TestWorkflowRole_ListRequiresNoMembershipRow).
func (s *Store) ListRoles(ctx context.Context) ([]Role, error) {
	roles := []Role{}
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// deleted_at IS NULL here — the OPPOSITE of CreateRole's key query, which
		// deliberately sees soft-deleted rows. Swapping the two breaks both paths.
		rows, err := tx.Query(ctx,
			`SELECT id, key, title, description
			   FROM workflow_roles
			  WHERE deleted_at IS NULL
			  ORDER BY created_at, key`)
		if err != nil {
			return err
		}
		idx := map[string]int{} // role id -> index into roles
		for rows.Next() {
			var id string
			// Members is constructed here, per role: the wire must carry [], never null.
			r := Role{Members: []string{}}
			if err := rows.Scan(&id, &r.Key, &r.Title, &r.Desc); err != nil {
				rows.Close()
				return err
			}
			idx[id] = len(roles)
			roles = append(roles, r)
		}
		// Closed explicitly rather than deferred: the members query below reuses this
		// transaction's connection.
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		members, err := tx.Query(ctx,
			`SELECT workflow_role_id, user_id
			   FROM workflow_role_members
			  ORDER BY workflow_role_id, ord`)
		if err != nil {
			return err
		}
		defer members.Close()
		for members.Next() {
			var roleID, userID string
			if err := members.Scan(&roleID, &userID); err != nil {
				return err
			}
			// A soft-deleted role's staffing is absent from idx and dropped.
			if i, ok := idx[roleID]; ok {
				roles[i].Members = append(roles[i].Members, userID)
			}
		}
		return members.Err()
	})
	if err != nil {
		return nil, err
	}
	return roles, nil
}

// CreateRole mints the key server-side from the title and audits the insert in the
// same transaction. Statement order is the security property:
//
// The caller's role is read FIRST, unlocked and taking no target argument, so a
// non-admin is refused before any workflow_roles row is read. That read carries
// AND status = 'active', so a suspended or invited admin is refused as firmly as a
// preparer (TestWorkflowRole_CreateRequiresActiveAdmin). This is the CALLER axis
// only; who may be STAFFED into a role is unrestricted.
//
// Duplicate titles are legal — the SPA gates on an empty name and nothing else — so
// only the key is suffixed.
func (s *Store) CreateRole(ctx context.Context, title, desc string) (Role, error) {
	// The shipped modal trims both fields (RoleModal.tsx:82-83); the trimmed title is
	// what is stored and what feeds the key.
	title, desc = strings.TrimSpace(title), strings.TrimSpace(desc)
	// Checked before the tx, like portfolio.Create's ValidateTIN: an argument that can
	// never be stored needs no transaction, and a direct caller fails closed.
	if title == "" {
		return Role{}, ErrValidation
	}

	created := Role{Members: []string{}} // [] on the wire, never null
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

		// Bare on purpose — NO deleted_at filter: workflow_roles_tenant_key_uq spans
		// soft-deleted rows, so filtering would mint a key the constraint then rejects,
		// or one already naming a sealed policy step. Unlocked: there is no row to lock
		// for a key that does not exist yet, and the 409 below is the answer.
		rows, err := tx.Query(ctx, `SELECT key FROM workflow_roles`)
		if err != nil {
			return err
		}
		taken := map[string]bool{}
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				return err
			}
			taken[key] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		// tenant_id is explicit: the column has no DEFAULT and the RLS WITH CHECK ties
		// it to the GUC.
		if err := tx.QueryRow(ctx,
			`INSERT INTO workflow_roles (tenant_id, key, title, description)
			 VALUES ($1, $2, $3, $4)
			 RETURNING key, title, description`,
			caller.TenantID, newRoleKey(taken, title), title, desc,
		).Scan(&created.Key, &created.Title, &created.Desc); err != nil {
			// A concurrent create can take the key between the query above and this
			// INSERT. No retry: it would hand back a key the client's title does not imply.
			if uniqueViolationOn(err, "workflow_roles_tenant_key_uq") {
				return ErrConflict
			}
			return err
		}

		// Last statement in the closure: a failing audit write rolls the insert back
		// (TestWorkflowRole_CreateAuditsInSameTx). tenant_id comes from the GUC via the
		// audit_log column DEFAULT; the key is the RETURNING row's, not the minted local.
		return audit.Record(ctx, tx, caller.Subject, "workflow_role.created", map[string]any{
			"key":   created.Key,
			"title": created.Title,
		})
	})
	if err != nil {
		return Role{}, err
	}
	return created, nil
}

// UpdateRole renames a role and/or rewrites its blurb, auditing the change in the same
// transaction. title and desc are pointers so an omitted field is distinguishable from
// an empty one — clearing the blurb is a real edit — and only what was sent is written.
//
// `key` is NEVER in the SET list: it is minted once, at create, and a sealed policy step
// may already name it. It follows that no unique constraint and no RLS WITH CHECK can
// fire here, so this method has no ErrConflict path.
//
// Guard order is the security property: argument validation (above the tx, reading no
// row) -> the caller's role -> the target row. A non-admin is therefore refused before
// any workflow_roles row is read, identically whether the key exists or not
// (TestWorkflowRole_UpdateAndDeletePermissionCheckedBeforeRowRead).
func (s *Store) UpdateRole(ctx context.Context, key string, title, desc *string) (Role, error) {
	// Trimmed like CreateRole and the shipped modal (RoleModal.tsx:82-83). Copied rather
	// than trimmed in place: the pointers belong to the caller.
	if title != nil {
		trimmed := strings.TrimSpace(*title)
		title = &trimmed
	}
	if desc != nil {
		trimmed := strings.TrimSpace(*desc)
		desc = &trimmed
	}
	// Checked before the tx, like CreateRole's: an argument that can never be stored needs
	// no transaction, and a direct caller fails closed.
	if title == nil && desc == nil {
		return Role{}, ErrValidation
	}
	if title != nil && *title == "" {
		return Role{}, ErrValidation
	}

	updated := Role{Members: []string{}} // [] on the wire, never null
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

		// FOR UPDATE because the pre-image below is read in Go and then written as audit
		// fact: unlocked, two concurrent renames both report the same from_title
		// (TestWorkflowRole_ConcurrentRenamesChainInTheAudit). deleted_at IS NULL is this
		// resource's existence predicate.
		var id, fromTitle, fromDesc string
		if err := tx.QueryRow(ctx,
			`SELECT id, title, description
			   FROM workflow_roles
			  WHERE key = $1 AND deleted_at IS NULL
			    FOR UPDATE`, key,
		).Scan(&id, &fromTitle, &fromDesc); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}

		// coalesce rather than Go-merged values: only the fields that were sent appear in
		// the effective write. WHERE id is the locked row's, so the write cannot resolve to
		// a different row than the pre-image above.
		if err := tx.QueryRow(ctx,
			`UPDATE workflow_roles
			    SET title = coalesce($2, title), description = coalesce($3, description)
			  WHERE id = $1
			RETURNING key, title, description`,
			id, title, desc,
		).Scan(&updated.Key, &updated.Title, &updated.Desc); err != nil {
			return err
		}

		// The response is a full Role: the SPA's replaceRole swaps the card wholesale, so
		// an empty members here would blank the avatar stack.
		members, err := tx.Query(ctx,
			`SELECT user_id FROM workflow_role_members WHERE workflow_role_id = $1 ORDER BY ord`, id)
		if err != nil {
			return err
		}
		for members.Next() {
			var userID string
			if err := members.Scan(&userID); err != nil {
				members.Close()
				return err
			}
			updated.Members = append(updated.Members, userID)
		}
		members.Close()
		if err := members.Err(); err != nil {
			return err
		}

		// Last statement in the closure (TestWorkflowRole_UpdateAuditsInSameTx). Both field
		// pairs: a desc-only edit would otherwise log a from_title == to_title rename, and
		// this log is the only record of what a role used to be called.
		return audit.Record(ctx, tx, caller.Subject, "workflow_role.updated", map[string]any{
			"key":        updated.Key,
			"from_title": fromTitle,
			"to_title":   updated.Title,
			"from_desc":  fromDesc,
			"to_desc":    updated.Desc,
		})
	})
	if err != nil {
		return Role{}, err
	}
	return updated, nil
}

// DeleteRole soft-deletes a role and audits it in the same transaction. It never refuses
// a live role and nothing cascades: the staffing rows survive, inert, unreachable while
// deleted_at is set. invoice_app holds no DELETE grant on workflow_roles, so a hard
// delete is structurally unreachable, and there is no undelete path.
//
// The returned Role carries no members — a deleted role has no addressable holders, and
// the SPA's removeRole drops the card without reading the body (roles.ts:103-105).
func (s *Store) DeleteRole(ctx context.Context, key string) (Role, error) {
	deleted := Role{Members: []string{}} // [] on the wire, never null
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		caller, _ := auth.IdentityFromContext(ctx)

		// Same caller gate as CreateRole's and UpdateRole's, first and unlocked: no
		// 403-vs-404 existence oracle.
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

		// One statement, no explicit lock: deleted_at IS NULL is both the existence
		// predicate and the idempotency mechanism — under READ COMMITTED the second (or
		// concurrent) delete re-evaluates it, matches nothing, and is ErrNotFound rather
		// than a re-stamp (TestWorkflowRole_SecondDeleteIsNotFoundAndDoesNotRestamp).
		// now(), not a Go clock: deleted_at is the transaction timestamp the audit row's
		// created_at DEFAULT also takes (TestWorkflowRole_DeleteAuditsInSameTx).
		if err := tx.QueryRow(ctx,
			`UPDATE workflow_roles
			    SET deleted_at = now()
			  WHERE key = $1 AND deleted_at IS NULL
			RETURNING key, title, description`, key,
		).Scan(&deleted.Key, &deleted.Title, &deleted.Desc); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}

		// The title as stored at delete time: after this the row is unreachable from every
		// HTTP door, and it may not be the title the create event recorded.
		return audit.Record(ctx, tx, caller.Subject, "workflow_role.deleted", map[string]any{
			"key":   deleted.Key,
			"title": deleted.Title,
		})
	})
	if err != nil {
		return Role{}, err
	}
	return deleted, nil
}

// uniqueViolationOn reports whether err is a 23505 on exactly this constraint.
// Checked by name, never by SQLSTATE alone: a 23505 on any other unique — a
// gen_random_uuid collision on workflow_roles_tenant_id_id_uq, say — must stay a 500.
func uniqueViolationOn(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
