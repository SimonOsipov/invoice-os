// demopurge.go — the demo-tenant purge primitive. Deletes every tenant-owned
// row belonging to the four seeded demo tenants and nothing else. Nothing calls
// it yet; DEMO-04-02 wires it into Provision.
package db

import (
	"context"
	"errors"
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
// deletes succeed under full referential-integrity enforcement. audit_log is
// last because it is the only statement issued under the 'replica' bypass, and
// that window is kept as small as possible ([bypass-narrowed-to-audit-log]).
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
	"audit_log",
}

// purgeExcludedTables carries tenant_id but is deliberately never purged:
// memberships has no runtime INSERT path, and the three approval-policy tables
// are configuration reset.go excludes for the same reasons. Together with
// purgeTables it must equal the live schema's tenant_id-bearing set
// (TestPurgeTableListCoversEveryTenantOwnedTable).
var purgeExcludedTables = []string{
	"memberships",
	"approval_policies",
	"approval_policy_versions",
	"approval_policy_steps",
}

// PurgeResult reports what one purge deleted, per table and in total.
type PurgeResult struct {
	ByTable  map[string]int64
	Rows     int64
	Duration time.Duration
}

// DemoPurgeOutcome is what cmd/gateway/main.go publishes on /healthz as
// demo_purge, set once per Provision call exactly as ResetWillRun() is today.
type DemoPurgeOutcome string

const (
	DemoPurgeRan     DemoPurgeOutcome = "true"
	DemoPurgeSkipped DemoPurgeOutcome = "false"
	DemoPurgeErrored DemoPurgeOutcome = "error"
)

// errPurgeNotImplemented is the Test-Spec stage's stub answer: every DB-backed
// spec fails on this returned error, never on a missing symbol.
var errPurgeNotImplemented = errors.New("db: PurgeDemoTenants not implemented")

// purgeStmt builds the only statement form this package emits. The tenant
// predicate must live in the SAME string literal as DELETE FROM
// (TestPurgeHasNoUnscopedDeleteStatement scans for exactly that).
func purgeStmt(table string) string { return "" }

// PurgeDemoTenants deletes every tenant-owned row of the four DemoTenants on a
// dedicated superuser connection, in one transaction.
func PurgeDemoTenants(ctx context.Context, superuserDSN string) (PurgeResult, error) {
	return PurgeResult{}, errPurgeNotImplemented
}

// purgeWithin issues the whole purge inside a caller-owned transaction: the
// three SET LOCAL guards, the sixteen FK-bearing deletes under 'origin', then
// the audit_log delete inside the 'replica' window. The seam exists so a test
// can trace the statements and re-run the purge over an extended tenant list
// inside a rolled-back transaction.
func purgeWithin(ctx context.Context, tx pgx.Tx, tenants []string) (PurgeResult, error) {
	return PurgeResult{}, errPurgeNotImplemented
}
