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

// strandedStepsSQL lists the pending approval steps of open runs on validated invoices, run by run, lowest ord first.
// Joins stay inner: a run with no pending approval step is not stranded. The title is read even for a deleted role.
const strandedStepsSQL = `
SELECT r.id, t.id, t.name, i.id, i.invoice_number, s.ord, s.workflow_role_key,
       COALESCE(wr.title, ''), r.opened_at,
       min(s.ord) OVER (PARTITION BY r.id)
  FROM approval_runs r
  JOIN invoices i ON i.tenant_id = r.tenant_id AND i.id = r.invoice_id
  JOIN tenants t ON t.id = r.tenant_id
  JOIN approval_run_steps s ON s.tenant_id = r.tenant_id AND s.run_id = r.id
       AND s.kind = 'approval' AND s.state = 'pending'
  LEFT JOIN workflow_roles wr ON wr.tenant_id = s.tenant_id AND wr.key = s.workflow_role_key
 WHERE r.state = 'open' AND i.status = 'validated'
 ORDER BY r.opened_at, i.id, r.id, s.ord`

type strandedStep struct {
	runID    string
	run      StrandedRun
	ord, cur int
}

// StrandedRunsTx lists the current tenant's stranded runs; tx must be READ ONLY and tenant-scoped.
// A run is stranded on its lowest-ord pending step that unsignableReasonsTx gives a reason.
func StrandedRunsTx(ctx context.Context, tx pgx.Tx) ([]StrandedRun, error) {
	if err := requireReadOnly(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, strandedStepsSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var steps []strandedStep
	var probes []CreatorProbe
	for rows.Next() {
		var s strandedStep
		var key *string
		if err := rows.Scan(&s.runID, &s.run.WorkspaceID, &s.run.Workspace, &s.run.InvoiceID, &s.run.InvoiceNumber,
			&s.ord, &key, &s.run.RoleTitle, &s.run.ArmedAt, &s.cur); err != nil {
			return nil, err
		}
		if key != nil {
			s.run.RoleKey = *key
		}
		steps = append(steps, s)
		probes = append(probes, CreatorProbe{InvoiceID: s.run.InvoiceID, RoleKey: key})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	reasons, err := unsignableReasonsTx(ctx, tx, probes)
	if err != nil {
		return nil, err
	}
	var out []StrandedRun
	done := map[string]bool{}
	for i, s := range steps {
		if reasons[i] == "" || done[s.runID] {
			continue
		}
		done[s.runID] = true
		s.run.Reason = reasons[i]
		s.run.Blocks = BlocksLater
		if s.ord == s.cur {
			s.run.Blocks = BlocksNow
		}
		out = append(out, s.run)
	}
	return out, nil
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
