// The M5-06 QA Mode A RED harness for internal/reconciliation. Mirrors the repo's two
// established DB-backed test conventions:
//   - internal/platform/db's rls_harness_test.go: per-role pgxpool.Pool (super/mig/app),
//     TestMain-driven, self-skips when the DATABASE_* env is unset so a bare `go test ./...`
//     stays green with no database.
//   - internal/submission's failure_modes_test.go / exchange_db_test.go: fixtures seeded as
//     the SUPERUSER (BYPASSRLS, so a seed needs neither tenant context nor an app-role
//     grant — invoices_rls_test.go:87, submission_jobs_rls_test.go:162 do the same), and
//     a *queue.Client is available for the ReArm cases to call the real EnqueueTx outbox
//     through.
//
// GATING. Four DSNs — DATABASE_URL (invoice_app, the role Scan/ReArmPoll/audit actually
// run as in production), DATABASE_MIGRATION_URL (unused directly here but kept for parity
// with `make test-rls`/`make test-queue` invocations), DATABASE_SUPERUSER_URL (fixture
// seeding + cross-check reads), and — added in Round B (M5-06-05/06) — DATABASE_READER_URL
// (invoice_tenant_reader: SweepOnce's tenant-enumeration role, sweep_test.go).
//
// Every case in this package is named TestRLS_* per [[rls-testing-convention]] /
// [reconciliation-ci-registration] (M5-06 story, Decisions), so the CI `queue` job's
// `-run TestRLS ./internal/reconciliation/...` (once M5-06-07 registers it) picks these up
// with no further edit, and a SKIP here is a CI failure, not a pass — requireHarness is the
// ONLY skip site in this package.
//
// Local run (this worktree's compose DB on 5433):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5433/invoice_os?sslmode=disable" \
//	DATABASE_MIGRATION_URL="postgres://invoice_migrator:migrator@localhost:5433/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5433/invoice_os?sslmode=disable" \
//	DATABASE_READER_URL="postgres://invoice_tenant_reader:reader@localhost:5433/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/reconciliation/...
package reconciliation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

const (
	rcAdapter        = "firs-app"
	rcAdapterVersion = "v1"
)

// rcThresholds mirrors the M5-06-06 config defaults (RECONCILE_POLL_OVERDUE_GRACE=15m,
// RECONCILE_HOP_CEILING=20, RECONCILE_MAX_PENDING_AGE=24h) so fixtures below ("1h overdue",
// "attempts=99", "created 72h ago") are unambiguously past every threshold.
var rcThresholds = Thresholds{
	Grace:         15 * time.Minute,
	MaxPendingAge: 24 * time.Hour,
	HopCeiling:    20,
}

var errNoDB = errors.New("reconciliation: DATABASE_* env not set")

// harness holds one pool per role plus an insert-only queue.Client on the app pool — the
// exact shape `cmd/reconciliation` builds (M5-06 System Design).
type harness struct {
	super  *pgxpool.Pool // superuser: BYPASSRLS, seeds fixtures + cross-checks river_job/audit_log
	mig    *pgxpool.Pool // invoice_migrator: schema owner, used only for the pg_indexes checks
	app    *pgxpool.Pool // invoice_app: the role Scan/ReArmPoll/audit.Record actually run as
	reader *pgxpool.Pool // invoice_tenant_reader: SweepOnce's tenant-enumeration role (Round B)
	queue  *queue.Client // insert-only client on the app pool (ReArmPoll's EnqueueTx target)
}

var h *harness

func TestMain(m *testing.M) { os.Exit(run(m)) }

func run(m *testing.M) int {
	ctx := context.Background()
	hh, err := setupHarness(ctx)
	if err != nil {
		if errors.Is(err, errNoDB) {
			// Not configured: still run, so every case self-skips (requireHarness).
			return m.Run()
		}
		fmt.Fprintln(os.Stderr, "reconciliation suite setup failed:", err)
		return 1
	}
	h = hh
	code := m.Run()
	h.teardown(ctx)
	return code
}

func setupHarness(ctx context.Context) (*harness, error) {
	appURL := os.Getenv("DATABASE_URL")
	migURL := os.Getenv("DATABASE_MIGRATION_URL")
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	readerURL := os.Getenv("DATABASE_READER_URL")
	if appURL == "" || migURL == "" || superURL == "" || readerURL == "" {
		return nil, errNoDB
	}

	hh := &harness{}
	for _, c := range []struct {
		dst **pgxpool.Pool
		url string
		who string
	}{
		{&hh.super, superURL, "superuser"},
		{&hh.mig, migURL, "migrator"},
		{&hh.app, appURL, "app"},
		{&hh.reader, readerURL, "reader"},
	} {
		pool, err := pgxpool.New(ctx, c.url)
		if err != nil {
			return nil, fmt.Errorf("connect %s: %w", c.who, err)
		}
		*c.dst = pool
	}
	// A URL that is set but unreachable / not bootstrapped is a real error (e.g.
	// `make test-queue` without `make dev-db`), not a skip.
	if err := hh.super.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping superuser (is the DB up and bootstrapped?): %w", err)
	}

	qc, err := queue.New(hh.app, queue.Config{})
	if err != nil {
		return nil, fmt.Errorf("build insert-only queue client: %w", err)
	}
	hh.queue = qc

	return hh, nil
}

func (h *harness) teardown(_ context.Context) {
	h.reader.Close()
	h.app.Close()
	h.mig.Close()
	h.super.Close()
}

// requireHarness skips the calling test when the suite is not configured.
func requireHarness(t *testing.T) *harness {
	t.Helper()
	if h == nil {
		t.Skip("reconciliation suite skipped: set DATABASE_URL, DATABASE_MIGRATION_URL, " +
			"DATABASE_SUPERUSER_URL and DATABASE_READER_URL (the same four `make test-rls` uses)")
	}
	return h
}

// querier is the read surface shared by pgx.Tx and *pgxpool.Pool, so mustCount works
// against either (mirrors internal/platform/db/rls_harness_test.go).
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func mustCount(t *testing.T, q querier, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := q.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", sql, err)
	}
	return n
}

// findingEqual compares two Finding values BY VALUE — Finding.SubmissionJobID is a
// *string, so a bare == would compare pointer identity and could false-negative two
// findings that agree on every field. Used instead of == everywhere a test pins an exact
// Finding.
func findingEqual(a, b Finding) bool {
	if a.InvoiceID != b.InvoiceID || a.Kind != b.Kind || a.Healable != b.Healable {
		return false
	}
	// InvoiceNumber is compared too: a field this oracle cannot see is a field no exact-Finding
	// call site in this package can see drift in (TestRLS_AuditNumber_FindingEqualComparesTheNumber).
	if a.InvoiceNumber != b.InvoiceNumber {
		return false
	}
	if (a.SubmissionJobID == nil) != (b.SubmissionJobID == nil) {
		return false
	}
	return a.SubmissionJobID == nil || *a.SubmissionJobID == *b.SubmissionJobID
}

func containsKind(fs []Finding, k DriftKind) bool {
	for _, f := range fs {
		if f.Kind == k {
			return true
		}
	}
	return false
}

// rcScan runs Scan inside a tenant-scoped transaction on the APP pool — the exact role
// production runs it as (M5-06 System Design step 2a).
func rcScan(t *testing.T, h *harness, tenantID string, th Thresholds) ([]Finding, error) {
	t.Helper()
	ctx := context.Background()
	var got []Finding
	err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		var scanErr error
		got, scanErr = Scan(ctx, tx, th)
		return scanErr
	})
	return got, err
}

// --- fixture seeding ---------------------------------------------------------------------
//
// All seeded as the SUPERUSER (BYPASSRLS) — the same choice
// internal/platform/db/invoices_rls_test.go / submission_jobs_rls_test.go make, and the
// reason a DATABASE_SUPERUSER_URL is required here rather than the migrator+WithinTenantTx
// workaround internal/submission's exchange_db_test.go needs (that package's `make
// test-queue` invocation does NOT set DATABASE_SUPERUSER_URL; this package's harness does).

type rcInvoiceOpts struct {
	status string  // "" defaults to "draft"
	irn    *string // nil leaves irn NULL
}

// rcSeedTenant seeds one fresh tenant + business_entity. Deepest-first cleanup order
// matches submission_jobs_rls_test.go's own convention.
func rcSeedTenant(t *testing.T, h *harness) (tenantID, entityID string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	tenantID = uuid.NewString()
	entityID = uuid.NewString()

	if _, err := h.super.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $2)`,
		tenantID, "M5-06 tenant "+tenantID[:8]); err != nil {
		t.Fatalf("seed tenant fixture: %v", err)
	}
	if _, err := h.super.Exec(ctx,
		`INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`,
		entityID, tenantID, "Reconciliation Corp"); err != nil {
		t.Fatalf("seed business_entity fixture: %v", err)
	}

	return tenantID, entityID, func() {
		cctx := context.Background()
		_, _ = h.super.Exec(cctx, `DELETE FROM business_entities WHERE id = $1`, entityID)
		_, _ = h.super.Exec(cctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
	}
}

// rcSeedInvoiceIn adds one more invoice under an EXISTING tenant/entity — for cases that
// need two invoices under the same tenant (e.g. a clean invoice alongside a dirty one, so
// a "no findings" assertion is never vacuously true against an empty tenant).
func rcSeedInvoiceIn(t *testing.T, h *harness, tenantID, entityID string, opts rcInvoiceOpts) (invoiceID string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	invoiceID = uuid.NewString()
	status := opts.status
	if status == "" {
		status = "draft"
	}

	if _, err := h.super.Exec(ctx,
		`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, status, irn)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		invoiceID, tenantID, entityID, "RC-"+invoiceID[:8], status, opts.irn,
	); err != nil {
		t.Fatalf("seed invoice fixture: %v", err)
	}

	return invoiceID, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, invoiceID)
	}
}

// rcSeedInvoice seeds a fresh tenant + business_entity + one invoice. Most Scan cases only
// need one invoice, so this is the common entry point; rcSeedTenant + rcSeedInvoiceIn are
// exposed separately for the cases that need a second invoice under the same tenant.
func rcSeedInvoice(t *testing.T, h *harness, opts rcInvoiceOpts) (tenantID, entityID, invoiceID string, cleanup func()) {
	t.Helper()
	tenantID, entityID, cleanupTenant := rcSeedTenant(t, h)
	invoiceID, cleanupInvoice := rcSeedInvoiceIn(t, h, tenantID, entityID, opts)
	return tenantID, entityID, invoiceID, func() {
		cleanupInvoice()
		cleanupTenant()
	}
}

type rcJobOpts struct {
	state      string // "" defaults to "queued"
	attempts   int
	nextPollAt *time.Time
	createdAt  *time.Time // nil defaults to time.Now()
	updatedAt  *time.Time // nil defaults to createdAt
}

// rcSeedJob seeds one submission_jobs row. created_at/updated_at are named explicitly in
// the INSERT (never left to the column DEFAULT) so PendingTooLong / SubmittingOrphan
// fixtures can backdate them — the updated_at BEFORE UPDATE trigger
// (submission_jobs_touch_updated_at) only fires on UPDATE, so an INSERT-time value is
// honoured verbatim.
func rcSeedJob(t *testing.T, h *harness, tenantID, invoiceID string, opts rcJobOpts) (jobID string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	jobID = uuid.NewString()
	state := opts.state
	if state == "" {
		state = "queued"
	}
	createdAt := time.Now()
	if opts.createdAt != nil {
		createdAt = *opts.createdAt
	}
	updatedAt := createdAt
	if opts.updatedAt != nil {
		updatedAt = *opts.updatedAt
	}

	if _, err := h.super.Exec(ctx,
		`INSERT INTO submission_jobs
		   (id, tenant_id, invoice_id, idempotency_key, adapter, adapter_version,
		    state, attempts, next_poll_at, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		jobID, tenantID, invoiceID, "rc-"+jobID, rcAdapter, rcAdapterVersion,
		state, opts.attempts, opts.nextPollAt, createdAt, updatedAt,
	); err != nil {
		t.Fatalf("seed submission_jobs fixture: %v", err)
	}

	return jobID, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM submission_jobs WHERE id = $1`, jobID)
	}
}

// rcSeedRiverJob seeds one river_job row directly (superuser — river_job is cross-tenant
// infra with no RLS, but going through invoice_app here would only add noise). args is
// marshalled to jsonb verbatim; state is cast explicitly (`::river_job_state`) so pgx never
// has to infer the enum's OID from an untyped text parameter.
func rcSeedRiverJob(t *testing.T, h *harness, kind, state string, args map[string]any) (id int64, cleanup func()) {
	t.Helper()
	ctx := context.Background()

	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal river_job args: %v", err)
	}

	if err := h.super.QueryRow(ctx,
		`INSERT INTO river_job (kind, state, args) VALUES ($1, $2::river_job_state, $3) RETURNING id`,
		kind, state, string(argsJSON),
	).Scan(&id); err != nil {
		t.Fatalf("seed river_job fixture: %v", err)
	}

	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM river_job WHERE id = $1`, id)
	}
}

// --- M5-06-05/06 sweep helpers (Round B) ------------------------------------------------

// rcConfig mirrors rcThresholds (above) as a Config — a built Reconciler's Cfg carries the
// same three Scan thresholds plus a ticker Interval that SweepOnce itself never reads
// (only Sweeper does; sweeper_test.go's pure suite covers the ticker separately with its
// own short fake intervals).
var rcConfig = Config{
	Interval:         time.Minute,
	PollOverdueGrace: rcThresholds.Grace,
	MaxPendingAge:    rcThresholds.MaxPendingAge,
	HopCeiling:       rcThresholds.HopCeiling,
}

// rcReconciler builds the Reconciler shape production wires — the harness's own pools and
// queue client plus rcConfig — with a silent JSON logger so a real Logger call in the
// eventual implementation never panics against a nil field (mirrors
// internal/platform/middleware_test.go's silent-logger helper).
func rcReconciler(h *harness) *Reconciler {
	return &Reconciler{
		ReaderPool: h.reader,
		AppPool:    h.app,
		Queue:      h.queue,
		Cfg:        rcConfig,
		Logger:     slog.New(slog.NewJSONHandler(io.Discard, nil)),
	}
}

// --- APPR-06-09 approval-drift fixtures -------------------------------------------------
//
// Spelled here rather than aliased to ApprovalRunOrphaned/ApprovalBlockedUnstaffed: a test
// that asserts against the constant it is checking asserts nothing (internal/approval/
// policy_test.go:35). TestRLS_ApprovalDriftKindConstantsMatchScanQuery binds the two
// spellings — and the SQL literals — back together.
const (
	rcApprovalRunOrphaned      = DriftKind("approval_run_orphaned")
	rcApprovalBlockedUnstaffed = DriftKind("approval_blocked_unstaffed")
)

// The compiler binds neither exported DriftKind constant to the kind literal scanQuery
// selects — editing one and not the other makes Scan return findings no consumer comparing
// against the constant can match. Pure, no harness: the TestRLS_ prefix is the CI job's
// `-run TestRLS` selector (ci.yml), not a claim that this case touches the database.
func TestRLS_ApprovalDriftKindConstantsMatchScanQuery(t *testing.T) {
	for _, c := range []struct {
		name     string
		exported DriftKind
		spelled  DriftKind
	}{
		{"ApprovalRunOrphaned", ApprovalRunOrphaned, rcApprovalRunOrphaned},
		{"ApprovalBlockedUnstaffed", ApprovalBlockedUnstaffed, rcApprovalBlockedUnstaffed},
	} {
		if c.exported != c.spelled {
			t.Errorf("%s = %q, want %q (the spelling every test in this package asserts against)",
				c.name, c.exported, c.spelled)
		}
		if lit := "'" + string(c.exported) + "'"; !strings.Contains(scanQuery, lit) {
			t.Errorf("scanQuery selects no %s literal; %s would never appear in a Finding", lit, c.name)
		}
	}
}

// countForInvoice counts fs entries for one invoice — the two new arms are shaped (LATERAL
// LIMIT 1, NOT EXISTS never LEFT JOIN) to never fan out more than one row per invoice; tests
// assert that directly instead of just checking presence.
func countForInvoice(fs []Finding, invoiceID string) int {
	n := 0
	for _, f := range fs {
		if f.InvoiceID == invoiceID {
			n++
		}
	}
	return n
}

// containsFindingFor reports whether fs holds a Finding for invoiceID with kind k.
func containsFindingFor(fs []Finding, invoiceID string, k DriftKind) bool {
	for _, f := range fs {
		if f.InvoiceID == invoiceID && f.Kind == k {
			return true
		}
	}
	return false
}

// rcTeardownApproval deletes one tenant's approval rows bottom-up, ported from
// internal/approval/policy_immutability_test.go's teardownSealedApprovalFixture minus its
// trailing `DELETE FROM tenants` (rcSeedTenant's own cleanup owns that). Reports every
// failure with t.Errorf, never a swallowed `_, _ =` — approval_runs -> invoices is ON
// DELETE RESTRICT, so a silently-discarded failure here strands the tenant's invoices and
// aborts the tenant cascade (the exact shape that broke CI in subtask 06, fixed in 0281257).
func rcTeardownApproval(t *testing.T, h *harness, tenantID string) {
	t.Helper()
	ctx := context.Background()

	tx, err := h.super.Begin(ctx)
	if err != nil {
		t.Errorf("teardown approval fixture %s: begin tx: %v", tenantID, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = 'replica'`); err != nil {
		t.Errorf("teardown approval fixture %s: set session_replication_role: %v", tenantID, err)
		return
	}
	for _, table := range []string{
		"approval_decisions", "approval_run_steps", "approval_runs",
		"approval_policy_steps", "approval_policy_versions", "approval_policies",
	} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE tenant_id = $1`, tenantID); err != nil {
			t.Errorf("teardown approval fixture %s: delete %s: %v", tenantID, table, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("teardown approval fixture %s: commit: %v", tenantID, err)
	}
}

// rcSeedApprovalPolicy seeds one approval_policies row plus one UNSEALED, INACTIVE
// approval_policy_versions row — neither new SQL arm reads either table, the version exists
// only to satisfy approval_runs_tenant_version_fk, and leaving it unsealed keeps the seal
// guard off the teardown path entirely. The returned cleanup is a closure the CALLER must
// defer — not t.Cleanup: Go runs every defer before any t.Cleanup, so a t.Cleanup-registered
// teardown here would run AFTER cleanupInvoice/cleanupTenant instead of before, and
// approval_runs' RESTRICT FK to invoices would abort that cascade.
func rcSeedApprovalPolicy(t *testing.T, h *harness, tenantID string) (versionID string, cleanup func()) {
	t.Helper()
	ctx := context.Background()

	var policyID string
	if err := h.super.QueryRow(ctx,
		`INSERT INTO approval_policies (tenant_id, name) VALUES ($1, $2) RETURNING id`,
		tenantID, "Reconciliation fixture policy",
	).Scan(&policyID); err != nil {
		t.Fatalf("seed approval_policies fixture: %v", err)
	}

	if err := h.super.QueryRow(ctx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version, sealed, is_active)
		 VALUES ($1, $2, 1, false, false) RETURNING id`,
		tenantID, policyID,
	).Scan(&versionID); err != nil {
		t.Fatalf("seed approval_policy_versions fixture: %v", err)
	}

	return versionID, func() { rcTeardownApproval(t, h, tenantID) }
}

// rcSeedWorkflowRole seeds one workflow_roles row, soft-deleted (deleted_at = now()) when
// deleted is true. No individual cleanup: workflow_roles references tenants(id) ON DELETE
// CASCADE with no RESTRICT on that path, so rcSeedTenant's own cleanup sweeps it.
func rcSeedWorkflowRole(t *testing.T, h *harness, tenantID, key string, deleted bool) (roleID string) {
	t.Helper()
	ctx := context.Background()

	var deletedAt *time.Time
	if deleted {
		now := time.Now()
		deletedAt = &now
	}

	if err := h.super.QueryRow(ctx,
		`INSERT INTO workflow_roles (tenant_id, key, title, deleted_at) VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, key, key, deletedAt,
	).Scan(&roleID); err != nil {
		t.Fatalf("seed workflow_roles fixture: %v", err)
	}
	return roleID
}

// rcSeedMember seeds one memberships row with a fresh uuid user_id. role is one of
// admin|preparer|reviewer (FK to roles, seeded by migration 20260709151759_roles.sql, not by
// db/seed.dev.sql); status is one of active|invited|suspended. No individual cleanup — same
// CASCADE-only reasoning as rcSeedWorkflowRole.
func rcSeedMember(t *testing.T, h *harness, tenantID, role, status string) (userID string) {
	t.Helper()
	ctx := context.Background()
	userID = uuid.NewString()

	if _, err := h.super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, $3, $4)`,
		tenantID, userID, role, status,
	); err != nil {
		t.Fatalf("seed memberships fixture: %v", err)
	}
	return userID
}

// rcStaffRole seeds one workflow_role_members row, linking an existing role and member. Must
// run after rcSeedWorkflowRole and rcSeedMember — its FK to memberships is composite
// (tenant_id, user_id). No individual cleanup — same CASCADE-only reasoning as
// rcSeedWorkflowRole.
func rcStaffRole(t *testing.T, h *harness, tenantID, roleID, userID string, ord int) {
	t.Helper()
	ctx := context.Background()

	if _, err := h.super.Exec(ctx,
		`INSERT INTO workflow_role_members (tenant_id, workflow_role_id, user_id, ord) VALUES ($1, $2, $3, $4)`,
		tenantID, roleID, userID, ord,
	); err != nil {
		t.Fatalf("seed workflow_role_members fixture: %v", err)
	}
}

// rcSeedRun seeds one approval_runs row. content_fingerprint is NOT NULL with no default, so
// a literal is always supplied. closed_at/closed_by are populated for any non-'open' state
// (approval_runs itself has no CHECK requiring this; the two new SQL arms only ever key off
// state, not these columns — set anyway so a seeded closed run never looks mid-flight).
func rcSeedRun(t *testing.T, h *harness, tenantID, invoiceID, versionID, state string) (runID string) {
	t.Helper()
	ctx := context.Background()
	runID = uuid.NewString()

	var closedAt *time.Time
	var closedBy *string
	if state != "open" {
		now := time.Now()
		closedAt = &now
		by := "system"
		closedBy = &by
	}

	if _, err := h.super.Exec(ctx,
		`INSERT INTO approval_runs
		   (id, tenant_id, invoice_id, policy_version_id, state, content_fingerprint, closed_at, closed_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		runID, tenantID, invoiceID, versionID, state, "fixture-fp-"+runID[:8], closedAt, closedBy,
	); err != nil {
		t.Fatalf("seed approval_runs fixture: %v", err)
	}
	return runID
}

// rcSeedRunStep seeds one approval_run_steps row. roleKey is a *string so the NULL-key case
// (TestRLS_ScanApprovalBlockedUnstaffedRoleKeyEdges) is expressible.
func rcSeedRunStep(t *testing.T, h *harness, tenantID, runID string, ord int, kind string, roleKey *string, state string) {
	t.Helper()
	ctx := context.Background()
	stepID := uuid.NewString()

	if _, err := h.super.Exec(ctx,
		`INSERT INTO approval_run_steps (id, tenant_id, run_id, ord, kind, workflow_role_key, state)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		stepID, tenantID, runID, ord, kind, roleKey, state,
	); err != nil {
		t.Fatalf("seed approval_run_steps fixture: %v", err)
	}
}
