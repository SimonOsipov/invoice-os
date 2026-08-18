// demopurge.go — the demo-tenant purge primitive. Deletes the four seeded demo
// tenants' rows from every tenant-owned table except the four purgeExcludedTables
// spares, and reaches no other tenant. Provision (provision.go) calls it on every
// gated boot, between Reset and Seed.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// DemoTenants is the purge allowlist — the four tenants db/seed.dev.sql creates.
// A literal, never derived from the seed at runtime: derivation would let a
// tenant added to that file silently widen a destructive purge.
// TestPurgeAllowlistMatchesSeedFileTenants compares the two in both directions.
var DemoTenants = []string{
	"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
	"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
	"11111111-1111-1111-1111-111111111111",
	"22222222-2222-2222-2222-222222222222",
}

// purgeTables is the delete order, leaf-first, so all sixteen FK-bearing
// deletes succeed under full referential-integrity enforcement. It is the
// reverse of seed.dev.sql's parent-first inserts, which deadlocks two
// concurrent boots unless they are serialized (lockProvisionTail,
// provision.go). audit_log is
// last because it is the only statement issued under the 'replica' bypass, and
// that window is kept as small as possible ([bypass-narrowed-to-audit-log]).
//
// Deliberately WIDER than reset.go, which excludes invitations, workflow_roles
// and workflow_role_members: db.Seed follows the purge in the same Provision
// call and restores all 14 roles and 13 staffing rows under their literal
// seeded ids, and the seed inserts no invitation — zero is their seeded state
// ([include-workflow-roles]).
var purgeTables = []string{
	"approval_decisions",
	"approval_run_steps",
	"approval_runs",
	"app_exchange",
	"submission_jobs",
	"line_items",
	"invoice_status_history",
	"invoices",
	"import_batches",
	"business_entities",
	"documents",
	"idempotency_keys",
	"submission_rate_limits",
	"invitations",
	"workflow_role_members",
	"workflow_roles",
	auditLogTable,
}

// purgeExcludedTables carries tenant_id but is deliberately never purged:
// memberships has no runtime INSERT path, and internal/demopolicy rebuilds the
// approval-policy tables for the two persona tenants only, so purging them
// would leave the other two with no policy and nothing to restore it. Together
// with purgeTables it must equal the live schema's tenant_id-bearing set
// (TestPurgeTableListCoversEveryTenantOwnedTable).
var purgeExcludedTables = []string{
	"memberships",
	"approval_policies",
	"approval_policy_versions",
	"approval_policy_steps",
}

// auditLogTable names the replica window by table rather than by position, so
// reordering purgeTables cannot leave the bypass wrapped around some other
// statement.
const auditLogTable = "audit_log"

const (
	setRoleReplica = `SET LOCAL session_replication_role = 'replica'`
	setRoleOrigin  = `SET LOCAL session_replication_role = 'origin'`
)

// purgeGuards open the purge transaction. 'origin' is set explicitly because
// the bypass suppresses referential integrity transaction-wide: the sixteen
// ordered deletes must stay checked, so a future reorder of purgeTables fails
// loudly instead of silently orphaning rows. The purge runs at gateway boot, so
// the two timeouts make it fail rather than hold the boot open behind someone
// else's lock.
var purgeGuards = []string{
	setRoleOrigin,
	`SET LOCAL lock_timeout = '15s'`,
	`SET LOCAL statement_timeout = '60s'`,
}

// PurgeResult reports what one purge deleted, per table and in total. ByTable
// names only the tables that actually lost rows.
type PurgeResult struct {
	ByTable  map[string]int64
	Rows     int64
	Duration time.Duration
}

// PurgeOutcome is the outcome of one Provision call's purge. Nothing publishes
// it yet — /healthz carries no demo_purge field (DEMO-04-03).
type PurgeOutcome string

const (
	DemoPurgeRan     PurgeOutcome = "true"
	DemoPurgeSkipped PurgeOutcome = "false"
	DemoPurgeErrored PurgeOutcome = "error"
)

// DemoPurgeOutcome is set once per Provision call exactly as ResetWillRun() is
// today. Single-writer: only Provision assigns it.
var DemoPurgeOutcome PurgeOutcome

// purgeStmt builds the only statement form this package emits. The tenant
// predicate must live in the SAME string literal as DELETE FROM
// (TestPurgeHasNoUnscopedDeleteStatement scans for exactly that).
func purgeStmt(table string) string {
	return fmt.Sprintf("DELETE FROM %s WHERE tenant_id = ANY($1)", table)
}

// PurgeDemoTenants deletes the four DemoTenants' rows from every purgeTables
// table on a dedicated superuser connection, in one transaction.
func PurgeDemoTenants(ctx context.Context, superuserDSN string) (PurgeResult, error) {
	started := time.Now()

	conn, err := connectSuperuser(ctx, superuserDSN)
	if err != nil {
		return PurgeResult{}, err
	}
	defer func() { _ = conn.Close(ctx) }()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return PurgeResult{}, fmt.Errorf("db: purge: begin tx: %w", err)
	}
	// Rolled back on any early return; a no-op after a successful Commit.
	defer func() { _ = tx.Rollback(ctx) }()

	res, err := purgeWithin(ctx, tx, DemoTenants)
	if err != nil {
		return PurgeResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PurgeResult{}, fmt.Errorf("db: purge: commit: %w", err)
	}

	res.Duration = time.Since(started)
	return res, nil
}

// purgeWithin issues the whole purge inside a caller-owned transaction: the
// three SET LOCAL guards, the sixteen FK-bearing deletes under 'origin', then
// the audit_log delete inside the 'replica' window. The seam exists so a test
// can trace the statements and re-run the purge over an extended tenant list
// inside a rolled-back transaction.
func purgeWithin(ctx context.Context, tx pgx.Tx, tenants []string) (PurgeResult, error) {
	for _, guard := range purgeGuards {
		if err := setLocal(ctx, tx, guard); err != nil {
			return PurgeResult{}, err
		}
	}

	res := PurgeResult{ByTable: map[string]int64{}}
	for _, table := range purgeTables {
		// audit_log's append-only trigger refuses a DELETE under 'origin' even
		// for a superuser, and the bypass is transaction-wide while it is on —
		// so it opens around this one statement and closes again.
		if table == auditLogTable {
			if err := setLocal(ctx, tx, setRoleReplica); err != nil {
				return PurgeResult{}, err
			}
		}

		tag, err := tx.Exec(ctx, purgeStmt(table), tenants)
		if err != nil {
			// Worded without the two SQL keywords: obligation 2's scanner reads every
			// literal in this file and is deliberately case-insensitive.
			return PurgeResult{}, fmt.Errorf("db: purge: %s: %w", table, err)
		}

		if table == auditLogTable {
			if err := setLocal(ctx, tx, setRoleOrigin); err != nil {
				return PurgeResult{}, err
			}
		}

		if n := tag.RowsAffected(); n > 0 {
			res.ByTable[table] = n
			res.Rows += n
		}
	}
	return res, nil
}

func setLocal(ctx context.Context, tx pgx.Tx, stmt string) error {
	if _, err := tx.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("db: purge: %s: %w", stmt, err)
	}
	return nil
}
