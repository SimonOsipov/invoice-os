package approval

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	StrandedDeleted     = "deleted"
	StrandedNoHolder    = "no holder"
	StrandedOutOfReach  = "out of client reach"
	StrandedCreatorOnly = "creator only"
	BlocksNow           = "now"
	BlocksLater         = "later"
)

// StrandedRun is an open run on a validated invoice with a pending approval step nobody can decide.
type StrandedRun struct {
	WorkspaceID, Workspace, InvoiceID, InvoiceNumber, RoleKey, RoleTitle, Reason, Blocks string
	ArmedAt                                                                              time.Time
}

var ErrNotReadOnly = errors.New("approval: stranded-runs read needs a READ ONLY transaction")

func requireReadOnly(ctx context.Context, tx pgx.Tx) error {
	var on string
	if err := tx.QueryRow(ctx, `SELECT current_setting('transaction_read_only')`).Scan(&on); err != nil {
		return err
	}
	if on != "on" {
		return ErrNotReadOnly
	}
	return nil
}

// strandedSQL counts holders instead of testing for one: TestAxis2_ZeroWorkflowRoleMembersExistsPredicatesRemain.
// The second JOIN LATERAL is inner on purpose: a run with no pending approval step is not stranded.
const strandedSQL = `
SELECT t.id, t.name, i.id, i.invoice_number,
       COALESCE(d.key, ''), COALESCE(d.title, ''),
       CASE WHEN d.role_id IS NULL OR d.role_deleted_at IS NOT NULL THEN 'deleted' ELSE 'no holder' END,
       r.opened_at,
       CASE WHEN d.ord = cur.ord THEN 'now' ELSE 'later' END
  FROM approval_runs r
  JOIN invoices i ON i.tenant_id = r.tenant_id AND i.id = r.invoice_id
  JOIN tenants t ON t.id = r.tenant_id
  JOIN LATERAL (
        SELECT min(c.ord) AS ord
          FROM approval_run_steps c
         WHERE c.tenant_id = r.tenant_id AND c.run_id = r.id
           AND c.kind = 'approval' AND c.state = 'pending') cur ON true
  JOIN LATERAL (
        SELECT s.ord, s.workflow_role_key AS key, wr.id AS role_id, wr.title, wr.deleted_at AS role_deleted_at
          FROM approval_run_steps s
          LEFT JOIN workflow_roles wr ON wr.tenant_id = s.tenant_id AND wr.key = s.workflow_role_key
         WHERE s.tenant_id = r.tenant_id AND s.run_id = r.id
           AND s.kind = 'approval' AND s.state = 'pending'
           AND (wr.id IS NULL OR wr.deleted_at IS NOT NULL
                OR (SELECT count(*)
                      FROM workflow_role_members wrm
                      JOIN memberships m ON m.tenant_id = wrm.tenant_id AND m.user_id = wrm.user_id
                     WHERE wrm.tenant_id = wr.tenant_id AND wrm.workflow_role_id = wr.id
                       AND m.status = 'active' AND m.role IN ('admin', 'reviewer')) = 0)
         ORDER BY s.ord LIMIT 1) d ON true
 WHERE r.state = 'open' AND i.status = 'validated'
 ORDER BY r.opened_at, i.id`

// StrandedRunsTx lists the current tenant's stranded runs; tx must be READ ONLY and tenant-scoped.
func StrandedRunsTx(ctx context.Context, tx pgx.Tx) ([]StrandedRun, error) {
	if err := requireReadOnly(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, strandedSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StrandedRun
	for rows.Next() {
		var s StrandedRun
		if err := rows.Scan(&s.WorkspaceID, &s.Workspace, &s.InvoiceID, &s.InvoiceNumber,
			&s.RoleKey, &s.RoleTitle, &s.Reason, &s.ArmedAt, &s.Blocks); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListStranded reads every given tenant in its own READ ONLY transaction, in the given order.
func ListStranded(ctx context.Context, app *pgxpool.Pool, tenantIDs []string) ([]StrandedRun, error) {
	return listStranded(ctx, app, tenantIDs, nil)
}

// listStranded's probe runs inside each tenant's transaction after the read; tests use it to prove writes fail.
func listStranded(ctx context.Context, app *pgxpool.Pool, tenantIDs []string, probe func(pgx.Tx) error) ([]StrandedRun, error) {
	var all []StrandedRun
	for _, id := range tenantIDs {
		err := db.WithinTenantTxOpts(ctx, app, id, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			rows, err := StrandedRunsTx(ctx, tx)
			if err != nil {
				return err
			}
			all = append(all, rows...)
			if probe != nil {
				return probe(tx)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("stranded runs: workspace %s: %w", id, err)
		}
	}
	return all, nil
}
