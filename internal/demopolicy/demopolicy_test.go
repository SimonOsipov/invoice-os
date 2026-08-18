// Stage 2.5 specs for internal/demopolicy, written before the seeder exists.
// demopolicy.go is a signatures-only stub whose Seed writes nothing, so every
// DB-backed spec below is red on its own target assertion.
//
// Gated on DATABASE_URL + DATABASE_SUPERUSER_URL — the two DSNs the package
// reads. A SKIP fails the CI step (scripts/ci/rls-test-gate.sh), so a DSN gap
// is loud rather than an `ok`.
//
//	Run: DATABASE_URL="postgres://invoice_app:app@localhost:5433/invoice_os?sslmode=disable" \
//	     DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5433/invoice_os?sslmode=disable" \
//	     go test -p 1 -count=1 ./internal/demopolicy/...
package demopolicy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/dashboard"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// ptr is the []approval.Step literal-builder's address-of helper — no such
// export exists outside the approval package's own _test.go files.
func ptr[T any](v T) *T { return &v }

const (
	inhouseDemoTenantID = "22222222-2222-2222-2222-222222222222"
	firmDemoTenantID    = "11111111-1111-1111-1111-111111111111"
	devTenantAID        = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	devTenantBID        = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

	// The seat the approval step must name. Hard-coded, never derived:
	// approval.newRoleKey slugs to HYPHENS while db/seed.dev.sql writes
	// underscores, and workflow_role_key has no FK, so a wrong key writes
	// cleanly and blocks silently.
	seededRoleKey = "fin_dir"
)

// The two names e2e/topology/roles.spec.ts's in-house sweep deletes
// (POLICY_NAME_SWEEP / UNSAVED_POLICY_NAME, :746-748). DeletePolicy deactivates
// the version in the same transaction, so a collision drops awaiting_approval to
// 0 mid-run with a green sweep and no error.
var (
	policyNameSweep   = regexp.MustCompile(`^E2E policy \d+$`)
	unsavedPolicyName = "Untitled policy"
)

// backlogTotals are the in-house demo tenant's four validated totals, measured
// against localhost:5433 (DEMO-2026-9001/9003/9002/8003). Three above 100,000,
// one below: awaiting_approval 3, counts.validated 4.
var backlogTotals = []string{"258000.00", "210700.00", "180600.00", "94600.00"}

// --- harness -----------------------------------------------------------------

func dbTestPools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	appURL := os.Getenv("DATABASE_URL")
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("demopolicy db-integration test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL")
	}
	ctx := context.Background()

	s, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping superuser (is the DB up and bootstrapped?): %v", err)
	}

	a, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	t.Cleanup(a.Close)

	return s, a
}

// fixture is a throwaway tenant shaped like the in-house demo tenant: one
// entity, a live fin_dir seat with an ACTIVE holder, plus whatever validated
// invoices the caller adds. Driving seedTenant against one of these is the
// demodocs precedent (store_test.go:5-7): Seed's allowlist is hard-coded, so a
// throwaway tenant is unreachable through it — which is the point.
type fixture struct {
	t        *testing.T
	super    *pgxpool.Pool
	app      *pgxpool.Pool
	tenantID string
	entityID string
	memberID string
	seats    int
}

func newFixture(t *testing.T, super, app *pgxpool.Pool, label string) *fixture {
	t.Helper()
	ctx := context.Background()
	f := &fixture{t: t, super: super, app: app, tenantID: uuid.NewString()}

	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'in_house')`, f.tenantID, label); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	// LIFO: the approval rows must go before the tenant delete — the seal guard
	// raises 23001 when a cascade reaches a sealed version.
	t.Cleanup(func() {
		if _, err := super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, f.tenantID); err != nil {
			t.Errorf("teardown tenant %s: %v", f.tenantID, err)
		}
	})
	t.Cleanup(func() { teardownApprovalRows(t, super, f.tenantID) })

	if err := super.QueryRow(ctx,
		`INSERT INTO business_entities (tenant_id, name) VALUES ($1, $2) RETURNING id`,
		f.tenantID, label+" supplier").Scan(&f.entityID); err != nil {
		t.Fatalf("seed business_entities: %v", err)
	}

	// Two ACTIVE holders, matching Honeywell's measured fin_dir staffing.
	f.memberID = f.addSeat(seededRoleKey, "Finance Director", "active")
	f.addHolder(seededRoleKey, "active")
	return f
}

// newFirmFixture is newFixture shaped for the firm plan: fin_mgr, fin_dir,
// cfo, compliance, each with one ACTIVE holder — polF1's four roles.
// seedTenantPlan(firmPlan) is the only entry point that reaches it; a
// random-uuid tenant never resolves to the firm plan through planFor (by ID,
// never by tenants.kind — kind is set to 'firm' here for realism only).
func newFirmFixture(t *testing.T, super, app *pgxpool.Pool, label string) *fixture {
	t.Helper()
	ctx := context.Background()
	f := &fixture{t: t, super: super, app: app, tenantID: uuid.NewString()}

	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm')`, f.tenantID, label); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		if _, err := super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, f.tenantID); err != nil {
			t.Errorf("teardown tenant %s: %v", f.tenantID, err)
		}
	})
	t.Cleanup(func() { teardownApprovalRows(t, super, f.tenantID) })

	if err := super.QueryRow(ctx,
		`INSERT INTO business_entities (tenant_id, name) VALUES ($1, $2) RETURNING id`,
		f.tenantID, label+" client").Scan(&f.entityID); err != nil {
		t.Fatalf("seed business_entities: %v", err)
	}

	for _, key := range []string{"fin_mgr", "fin_dir", "cfo", "compliance"} {
		holder := f.addSeat(key, key, "active")
		if f.memberID == "" {
			f.memberID = holder
		}
	}
	return f
}

// addSeat inserts one workflow_roles row and, unless memberStatus is "", one
// holder at that membership status. Returns the holder's user id.
func (f *fixture) addSeat(key, title, memberStatus string) string {
	f.t.Helper()
	if _, err := f.super.Exec(context.Background(),
		`INSERT INTO workflow_roles (tenant_id, key, title) VALUES ($1, $2, $3)`,
		f.tenantID, key, title); err != nil {
		f.t.Fatalf("seed workflow_roles %q: %v", key, err)
	}
	if memberStatus == "" {
		return ""
	}
	return f.addHolder(key, memberStatus)
}

func (f *fixture) addHolder(key, memberStatus string) string {
	f.t.Helper()
	ctx := context.Background()
	userID := uuid.NewString()
	if _, err := f.super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'admin', $3)`,
		f.tenantID, userID, memberStatus); err != nil {
		f.t.Fatalf("seed membership (%s): %v", memberStatus, err)
	}
	if _, err := f.super.Exec(ctx,
		`INSERT INTO workflow_role_members (tenant_id, workflow_role_id, user_id, ord)
		 SELECT r.tenant_id, r.id, $2, $3 FROM workflow_roles r
		  WHERE r.tenant_id = $1 AND r.key = $4 AND r.deleted_at IS NULL`,
		f.tenantID, userID, f.seats, key); err != nil {
		f.t.Fatalf("seed workflow_role_members (%s): %v", key, err)
	}
	f.seats++
	return userID
}

// addValidatedInvoice inserts one validated invoice. total nil writes NULL —
// what the import wizard's unmapped-column passthrough actually stores.
func (f *fixture) addValidatedInvoice(number string, total *string) string {
	f.t.Helper()
	var id string
	if err := f.super.QueryRow(context.Background(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, status, issue_date,
		                       buyer_tin, buyer_name, currency, subtotal, vat, total)
		 VALUES ($1, $2, $3, 'validated', '2026-06-02',
		         '20011122-0001', 'Zenith Freight', 'NGN', 1000.00, 75.00, $4::numeric)
		 RETURNING id`,
		f.tenantID, f.entityID, number, total).Scan(&id); err != nil {
		f.t.Fatalf("seed validated invoice %s: %v", number, err)
	}
	return id
}

// addBacklog reproduces the in-house tenant's measured validated set, in order.
func (f *fixture) addBacklog() []string {
	f.t.Helper()
	ids := make([]string, 0, len(backlogTotals))
	for i := range backlogTotals {
		ids = append(ids, f.addValidatedInvoice("DEMO-T-BACKLOG-"+string(rune('A'+i)), &backlogTotals[i]))
	}
	return ids
}

// teardownApprovalRows removes a tenant's approval rows bottom-up under
// session_replication_role = 'replica': approval_policy_versions_seal_guard
// raises 23001 on deleting a SEALED version, so a plain DELETE — and the tenant
// cascade behind it — fails. Idiom from
// internal/approval/policy_immutability_test.go's teardownSealedApprovalFixture.
func teardownApprovalRows(t *testing.T, super *pgxpool.Pool, tenantID string) {
	t.Helper()
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Errorf("teardown approval rows for %s: begin: %v", tenantID, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = 'replica'`); err != nil {
		t.Errorf("teardown approval rows for %s: set session_replication_role: %v", tenantID, err)
		return
	}
	for _, table := range []string{
		"approval_decisions", "approval_run_steps", "approval_runs",
		"approval_policy_steps", "approval_policy_versions", "approval_policies",
	} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE tenant_id = $1`, tenantID); err != nil {
			t.Errorf("teardown approval rows for %s: delete %s: %v", tenantID, table, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("teardown approval rows for %s: commit: %v", tenantID, err)
	}
}

// wipeRuns removes the run ledger and leaves the three policy tables standing —
// what db.Reset does (internal/platform/db/reset.go's resetTables and the
// exclusion note above it), scoped to one tenant so the shared dev DB is not
// truncated out from under a sibling suite. None of the three carries a trigger,
// so no replica override is needed.
func wipeRuns(t *testing.T, super *pgxpool.Pool, tenantID string) {
	t.Helper()
	for _, table := range []string{"approval_decisions", "approval_run_steps", "approval_runs"} {
		if _, err := super.Exec(context.Background(),
			`DELETE FROM `+table+` WHERE tenant_id = $1`, tenantID); err != nil {
			t.Fatalf("wipe %s for %s: %v", table, tenantID, err)
		}
	}
}

// --- read-backs (superuser: RLS-bypassing, so a cross-tenant zero is real) ----

func countRows(t *testing.T, super *pgxpool.Pool, table, tenantID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, tenantID).Scan(&n); err != nil {
		t.Fatalf("count %s for %s: %v", table, tenantID, err)
	}
	return n
}

func validatedCount(t *testing.T, super *pgxpool.Pool, tenantID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM invoices WHERE tenant_id = $1 AND status = 'validated'`, tenantID).Scan(&n); err != nil {
		t.Fatalf("count validated invoices for %s: %v", tenantID, err)
	}
	return n
}

type versionRow struct {
	ID          string
	Sealed      bool
	IsActive    bool
	PublishedBy *string
}

func versionsOf(t *testing.T, super *pgxpool.Pool, tenantID string) []versionRow {
	t.Helper()
	// ORDER BY version ALONE is undefined once a tenant carries two policies:
	// each policy's version numbering restarts at 1
	// (approval_policy_versions_tenant_policy_version_uq is per-policy), so two
	// v1 rows tie. id is a stable, if arbitrary, tie-break — callers that need
	// THE active version specifically must use activeVersionOf, not index [0].
	rows, err := super.Query(context.Background(),
		`SELECT id::text, sealed, is_active, published_by FROM approval_policy_versions
		  WHERE tenant_id = $1 ORDER BY version, id`, tenantID)
	if err != nil {
		t.Fatalf("read approval_policy_versions for %s: %v", tenantID, err)
	}
	defer rows.Close()
	var out []versionRow
	for rows.Next() {
		var v versionRow
		if err := rows.Scan(&v.ID, &v.Sealed, &v.IsActive, &v.PublishedBy); err != nil {
			t.Fatalf("scan approval_policy_versions: %v", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read approval_policy_versions: %v", err)
	}
	return out
}

// activeVersionOf returns the tenant's one active version, deterministically
// — where versionsOf is not once two policies both sit at version 1.
func activeVersionOf(t *testing.T, super *pgxpool.Pool, tenantID string) versionRow {
	t.Helper()
	var v versionRow
	if err := super.QueryRow(context.Background(),
		`SELECT id::text, sealed, is_active, published_by FROM approval_policy_versions
		  WHERE tenant_id = $1 AND is_active`, tenantID).
		Scan(&v.ID, &v.Sealed, &v.IsActive, &v.PublishedBy); err != nil {
		t.Fatalf("read the active version for %s: %v", tenantID, err)
	}
	return v
}

// anyActiveMember finds one active member of a real (not throwaway) tenant,
// for rollupFor's subject — the real demo tenants carry no fixture-captured
// memberID.
func anyActiveMember(t *testing.T, super *pgxpool.Pool, tenantID string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`SELECT user_id FROM memberships WHERE tenant_id = $1 AND status = 'active' LIMIT 1`,
		tenantID).Scan(&id); err != nil {
		t.Fatalf("find an active member of %s: %v", tenantID, err)
	}
	return id
}

func policyNames(t *testing.T, super *pgxpool.Pool, tenantID string) []string {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT name FROM approval_policies WHERE tenant_id = $1 AND deleted_at IS NULL ORDER BY name`, tenantID)
	if err != nil {
		t.Fatalf("read approval_policies for %s: %v", tenantID, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan approval_policies: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read approval_policies: %v", err)
	}
	return out
}

// approvalStepRoleKeys returns the workflow_role_key of every kind='approval'
// step on the tenant's ACTIVE version. Scoped to is_active, not tenant-wide:
// the in-house plan's draft carries its own approval steps, and a tenant-wide
// read cannot tell "the published policy blocks" from "the draft does". A
// NULL key stays nil rather than "": the column is nullable text with no FK,
// and the two are different defects.
func approvalStepRoleKeys(t *testing.T, super *pgxpool.Pool, tenantID string) []*string {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT s.workflow_role_key FROM approval_policy_steps s
		   JOIN approval_policy_versions v ON v.id = s.version_id
		  WHERE s.tenant_id = $1 AND s.kind = 'approval' AND v.is_active
		  ORDER BY s.ord`, tenantID)
	if err != nil {
		t.Fatalf("read approval_policy_steps for %s: %v", tenantID, err)
	}
	defer rows.Close()
	var out []*string
	for rows.Next() {
		var key *string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("scan approval_policy_steps: %v", err)
		}
		out = append(out, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read approval_policy_steps: %v", err)
	}
	return out
}

// runStates counts the tenant's runs by state.
func runStates(t *testing.T, super *pgxpool.Pool, tenantID string) map[string]int {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT state, count(*) FROM approval_runs WHERE tenant_id = $1 GROUP BY state`, tenantID)
	if err != nil {
		t.Fatalf("read approval_runs for %s: %v", tenantID, err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			t.Fatalf("scan approval_runs: %v", err)
		}
		out[state] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read approval_runs: %v", err)
	}
	return out
}

// runOf returns one invoice's newest run — RowFactsTx's own DISTINCT ON shape —
// plus how many runs it carries at all.
func runOf(t *testing.T, super *pgxpool.Pool, invoiceID string) (state string, closedBy *string, runs int) {
	t.Helper()
	ctx := context.Background()
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM approval_runs WHERE invoice_id = $1`, invoiceID).Scan(&runs); err != nil {
		t.Fatalf("count runs of %s: %v", invoiceID, err)
	}
	if runs == 0 {
		return "", nil, 0
	}
	if err := super.QueryRow(ctx,
		`SELECT state, closed_by FROM approval_runs WHERE invoice_id = $1
		  ORDER BY opened_at DESC LIMIT 1`, invoiceID).Scan(&state, &closedBy); err != nil {
		t.Fatalf("read newest run of %s: %v", invoiceID, err)
	}
	return state, closedBy, runs
}

// demoStepRow is one approval_policy_steps row. Read flat, nested in Go —
// same idiom as internal/approval/policy_store.go's readPolicyTrees +
// policy.go's nestSteps, not a recursive CTE:
// approval_policy_steps_depth_cap forbids a condition CHILD, not depth, and
// every tree this suite writes is depth <= 2, so a flat read canonicalised
// here is simpler and matches the surrounding code.
type demoStepRow struct {
	id, parentStepID, branch            string
	hasParent                           bool
	ord                                 int
	kind                                string
	workflowRoleKey, condOp, condAmount *string
	slaHours                            *int
	notifyTarget, notifyChannel         *string
}

func stepRowsOf(t *testing.T, super *pgxpool.Pool, versionID string) []demoStepRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT id, parent_step_id, branch, ord, kind, workflow_role_key,
		        sla_hours, cond_op, cond_amount::text, notify_target, notify_channel
		   FROM approval_policy_steps WHERE version_id = $1 ORDER BY ord`, versionID)
	if err != nil {
		t.Fatalf("read approval_policy_steps for version %s: %v", versionID, err)
	}
	defer rows.Close()
	var out []demoStepRow
	for rows.Next() {
		var r demoStepRow
		var parent, branch *string
		if err := rows.Scan(&r.id, &parent, &branch, &r.ord, &r.kind, &r.workflowRoleKey,
			&r.slaHours, &r.condOp, &r.condAmount, &r.notifyTarget, &r.notifyChannel); err != nil {
			t.Fatalf("scan approval_policy_steps: %v", err)
		}
		if parent != nil && branch != nil {
			r.hasParent, r.parentStepID, r.branch = true, *parent, *branch
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read approval_policy_steps: %v", err)
	}
	return out
}

// stepTreeOf reads one version's steps and nests them into approval.Step, the
// same shape internal/approval's own store returns to callers.
func stepTreeOf(t *testing.T, super *pgxpool.Pool, versionID string) []approval.Step {
	t.Helper()
	rows := stepRowsOf(t, super, versionID)
	roots := make([]demoStepRow, 0, len(rows))
	kids := map[string][]demoStepRow{}
	for _, r := range rows {
		if !r.hasParent {
			roots = append(roots, r)
			continue
		}
		key := r.parentStepID + "\x00" + r.branch
		kids[key] = append(kids[key], r)
	}
	return nestDemoLane(roots, kids)
}

func nestDemoLane(lane []demoStepRow, kids map[string][]demoStepRow) []approval.Step {
	sort.SliceStable(lane, func(i, j int) bool { return lane[i].ord < lane[j].ord })
	out := make([]approval.Step, 0, len(lane))
	for _, r := range lane {
		out = append(out, approval.Step{
			Kind:            r.kind,
			WorkflowRoleKey: r.workflowRoleKey,
			SLAHours:        r.slaHours,
			CondOp:          r.condOp,
			CondAmount:      r.condAmount,
			NotifyTarget:    r.notifyTarget,
			NotifyChannel:   r.notifyChannel,
			Then:            nestDemoLane(kids[r.id+"\x00then"], kids),
			Else:            nestDemoLane(kids[r.id+"\x00else"], kids),
		})
	}
	return out
}

// canonSteps zeroes what a DB round-trip cannot pin (server-minted ids) and
// turns a nil lane into [], matching nestSteps's own contract, so a literal
// written with nil lanes (the polF1()-style fixture convention) compares
// equal to a lane read back as [].
func canonSteps(steps []approval.Step) []approval.Step {
	out := make([]approval.Step, len(steps))
	for i, s := range steps {
		s.ID = ""
		s.Then = canonSteps(s.Then)
		s.Else = canonSteps(s.Else)
		out[i] = s
	}
	return out
}

// assertStepTree fails with a JSON side-by-side diff on mismatch — Step's own
// MarshalJSON already renders [] not null, so the dump matches what a client
// would see over the wire.
func assertStepTree(t *testing.T, got, want []approval.Step, label string) {
	t.Helper()
	if reflect.DeepEqual(canonSteps(got), canonSteps(want)) {
		return
	}
	gj, _ := json.MarshalIndent(got, "", "  ")
	wj, _ := json.MarshalIndent(want, "", "  ")
	t.Errorf("%s step tree mismatch\ngot:\n%s\nwant:\n%s", label, gj, wj)
}

// activeHolders is AC-5's resolution query: how many LIVE workflow_roles rows
// carry the key, and how many of their holders are ACTIVE members. The
// memberships join is the whole point — a member-row count passes a seat whose
// only holder is suspended, producing a policy nobody can ever satisfy.
func activeHolders(t *testing.T, super *pgxpool.Pool, tenantID, roleKey string) (roles, active int) {
	t.Helper()
	if err := super.QueryRow(context.Background(),
		`SELECT count(DISTINCT r.id),
		        count(*) FILTER (WHERE ms.status = 'active')
		   FROM workflow_roles r
		   LEFT JOIN workflow_role_members m
		          ON m.tenant_id = r.tenant_id AND m.workflow_role_id = r.id
		   LEFT JOIN memberships ms
		          ON ms.tenant_id = m.tenant_id AND ms.user_id = m.user_id
		  WHERE r.tenant_id = $1 AND r.key = $2 AND r.deleted_at IS NULL`,
		tenantID, roleKey).Scan(&roles, &active); err != nil {
		t.Fatalf("resolve %q for %s: %v", roleKey, tenantID, err)
	}
	return roles, active
}

// rollupFor reads the dashboard rollup as a member of tenantID — the same
// Store.Rollup that GET /api/dashboard/v1/rollup serves the badge from.
func rollupFor(t *testing.T, app *pgxpool.Pool, tenantID, subject string) dashboard.Rollup {
	t.Helper()
	ctx := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})
	r, err := dashboard.NewStore(app).Rollup(ctx)
	if err != nil {
		t.Fatalf("dashboard Rollup for %s: %v", tenantID, err)
	}
	return r
}

// rowFactsOf reads the list-row approval standing the wire serves, through the
// real read path rather than a hand-rolled query.
func rowFactsOf(t *testing.T, app *pgxpool.Pool, tenantID string, ids []string) map[string]approval.RowFacts {
	t.Helper()
	var facts map[string]approval.RowFacts
	if err := db.WithinTenantTx(context.Background(), app, tenantID, func(tx pgx.Tx) error {
		var err error
		facts, err = approval.RowFactsTx(context.Background(), tx, ids)
		return err
	}); err != nil {
		t.Fatalf("RowFactsTx for %s: %v", tenantID, err)
	}
	return facts
}

// --- specs -------------------------------------------------------------------

// AC-1/AC-2/AC-9. DemoTenants is the safety boundary, not ENVIRONMENT, and it
// is asserted by VALUE so widening the blast radius cannot be a silent diff.
// It now names BOTH persona tenants — re-aimed from
// TestSeed_AllowlistExcludesTheFirmTenant, which asserted the firm tenant's
// ABSENCE; task-568 puts it on the allowlist deliberately.
func TestSeed_AllowlistHoldsBothPersonaTenants(t *testing.T) {
	for _, want := range []string{inhouseDemoTenantID, firmDemoTenantID} {
		if !slices.Contains(DemoTenants, want) {
			t.Errorf("DemoTenants = %v, want it to contain %s", DemoTenants, want)
		}
	}
	if len(DemoTenants) != 2 {
		t.Errorf("DemoTenants has %d entries (%v), want exactly 2", len(DemoTenants), DemoTenants)
	}
	for _, forbidden := range []string{devTenantAID, devTenantBID} {
		if slices.Contains(DemoTenants, forbidden) {
			t.Errorf("DemoTenants contains %s; only the two persona demo tenants may carry a seeded policy", forbidden)
		}
	}
}

// AC-1. planFor dispatches by the tenant's exact ID, never by tenants.kind:
// the firm demo tenant's literal ID resolves to firmPlan, and everything else
// — including the in-house demo tenant and a random uuid — resolves to
// inhousePlan.
func TestSeed_PlanIsChosenByTenantIdNotKind(t *testing.T) {
	if got := planFor(firmDemoTenantID); got != firmPlan {
		t.Errorf("planFor(the firm demo tenant) = %+v, want firmPlan", got)
	}
	for _, id := range []string{inhouseDemoTenantID, uuid.NewString()} {
		if got := planFor(id); got != inhousePlan {
			t.Errorf("planFor(%s) = %+v, want inhousePlan", id, got)
		}
	}
}

// controlNeedle is what proves the scan below read the package's PRODUCTION
// source rather than nothing at all. It is checked against the non-test files
// only: this file names it in half a dozen messages, so scanning itself would
// satisfy the control no matter what demopolicy.go holds.
const controlNeedle = "DemoTenants"

// AC-6, and a POSITIVE CONTROL: green before the implementation lands, and it
// must stay green. The assertion ORDER is load-bearing — a glob that stops
// matching, or a package that loses DemoTenants, fails loudly here instead of
// reading as a clean scan of nothing.
func TestSeed_DoesNotReadApprovalsEnforced(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob *.go: %v", err)
	}
	if len(files) < 2 {
		t.Fatalf("scanned %d .go file(s) in internal/demopolicy, want at least 2 — the absence assertion below would prove nothing", len(files))
	}

	var all, production strings.Builder
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		all.Write(b)
		if !strings.HasSuffix(f, "_test.go") {
			production.Write(b)
		}
	}
	if production.Len() == 0 {
		t.Fatalf("no non-test .go file among %v — the scan would only be reading its own suite", files)
	}
	if !strings.Contains(production.String(), controlNeedle) {
		t.Fatalf("the control needle %s is absent from the package's production source — this scan cannot tell a clean package from a broken read", controlNeedle)
	}

	// Concatenated so the scan cannot match this file's own literal. Checked
	// over every file, tests included: nothing here may touch the flag.
	flag := "APPROVALS" + "_ENFORCED"
	if strings.Contains(all.String(), flag) {
		t.Errorf("internal/demopolicy mentions %s; the flag is APPR-14's and this package must neither read nor write it", flag)
	}
}

// AC-4/AC-5. Fails on the slug mismatch AND on the unstaffed seat: the fixture
// carries all three of Honeywell's measured shapes, so an assertion that passed
// for cfo or fin_mgr would be caught by the preconditions first.
func TestSeed_ResolvesTheWorkflowRoleKeyToAStaffedSeat(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy staffed seat")
	f.addSeat("cfo", "Chief Financial Officer", "suspended")
	f.addSeat("fin_mgr", "Finance Manager", "")
	f.addValidatedInvoice("DEMO-T-STAFF-1", &backlogTotals[0])

	for _, pre := range []struct {
		key                string
		wantRoles, wantAct int
		why                string
	}{
		{seededRoleKey, 1, 2, "the staffed seat"},
		{"cfo", 1, 0, "one holder, suspended — a member-row count would pass this"},
		{"fin_mgr", 1, 0, "the unstaffed seat"},
		{"fin-dir", 0, 0, "the hyphen slug newRoleKey mints must resolve to nothing"},
	} {
		roles, active := activeHolders(t, super, f.tenantID, pre.key)
		if roles != pre.wantRoles || active != pre.wantAct {
			t.Fatalf("fixture %q = %d role(s)/%d active holder(s), want %d/%d (%s)",
				pre.key, roles, active, pre.wantRoles, pre.wantAct, pre.why)
		}
	}

	if _, err := seedTenant(context.Background(), app, f.tenantID); err != nil {
		t.Fatalf("seedTenant: %v", err)
	}

	keys := approvalStepRoleKeys(t, super, f.tenantID)
	if len(keys) != 1 {
		t.Fatalf("the seeded version carries %d approval step(s), want exactly 1", len(keys))
	}
	if keys[0] == nil {
		t.Fatal("the approval step's workflow_role_key is NULL; the column has no FK, so it writes cleanly and blocks silently")
	}
	roles, active := activeHolders(t, super, f.tenantID, *keys[0])
	if roles == 0 {
		t.Errorf("workflow_role_key %q resolves to no live workflow_roles row", *keys[0])
	}
	if active == 0 {
		t.Errorf("workflow_role_key %q has no ACTIVE holder — published and armed, every step of it blocks, with no error anywhere", *keys[0])
	}
}

// AC-1. seedTenantPlan(firmPlan) against a firm-shaped throwaway tenant
// writes polF1 verbatim: one sealed active version named "Standard approval
// policy", seven steps in (parent, branch, ord) order, no sla_hours anywhere
// (a product decision distinct from the SPA fixture, which carries SLA
// hours on every node).
func TestSeed_FirmPolicyIsActiveWithPolF1sTree(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFirmFixture(t, super, app, "demopolicy firm tree")
	ctx := context.Background()

	res, err := seedTenantPlan(ctx, app, f.tenantID, firmPlan)
	if err != nil {
		t.Fatalf("seedTenantPlan(firmPlan): %v", err)
	}
	if !res.VersionCreated {
		t.Fatal("VersionCreated = false; nothing had published a firm policy yet")
	}

	versions := versionsOf(t, super, f.tenantID)
	if len(versions) != 1 {
		t.Fatalf("the firm tenant holds %d approval_policy_versions row(s), want exactly 1", len(versions))
	}
	if !versions[0].Sealed || !versions[0].IsActive {
		t.Errorf("the firm version is sealed=%v is_active=%v, want both true", versions[0].Sealed, versions[0].IsActive)
	}

	names := policyNames(t, super, f.tenantID)
	if len(names) != 1 || names[0] != "Standard approval policy" {
		t.Errorf("the firm tenant's policy names = %v, want exactly [\"Standard approval policy\"]", names)
	}

	got := stepTreeOf(t, super, versions[0].ID)
	want := []approval.Step{
		{Kind: "approval", WorkflowRoleKey: ptr("fin_mgr")},
		{Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("250000000.00"),
			Then: []approval.Step{{Kind: "approval", WorkflowRoleKey: ptr("fin_dir")}}},
		{Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("1000000000.00"),
			Then: []approval.Step{
				{Kind: "approval", WorkflowRoleKey: ptr("cfo")},
				{Kind: "notify", NotifyTarget: ptr("Audit Committee"), NotifyChannel: ptr("Email")},
			}},
		{Kind: "approval", WorkflowRoleKey: ptr("compliance")},
	}
	assertStepTree(t, got, want, "polF1")

	var withSLA int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM approval_policy_steps WHERE version_id = $1 AND sla_hours IS NOT NULL`,
		versions[0].ID).Scan(&withSLA); err != nil {
		t.Fatalf("count steps carrying sla_hours: %v", err)
	}
	if withSLA != 0 {
		t.Errorf("%d step(s) carry sla_hours, want 0 — no seeded step names a deadline", withSLA)
	}
}

// AC-2. The firm's validated backlog arms on fin_mgr first: every invoice at
// or below polF1's 250,000,000 condition gets exactly two pending steps,
// fin_mgr at ord 0 then compliance at ord 1, and never touches fin_dir/cfo/
// notify. A control invoice above the threshold DOES reach fin_dir, proving
// this loop can tell a real condition from a policy with none.
func TestSeed_FirmBacklogArmsOnFinMgrFirst(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFirmFixture(t, super, app, "demopolicy firm backlog")
	below := []string{
		f.addValidatedInvoice("DEMO-T-FIRM-BELOW-1", ptr("1000.00")),
		f.addValidatedInvoice("DEMO-T-FIRM-BELOW-2", ptr("50000000.00")),
		f.addValidatedInvoice("DEMO-T-FIRM-BELOW-3", ptr("249999999.99")),
	}
	control := f.addValidatedInvoice("DEMO-T-FIRM-CONTROL-ABOVE", ptr("300000000.00"))
	ctx := context.Background()

	if _, err := seedTenantPlan(ctx, app, f.tenantID, firmPlan); err != nil {
		t.Fatalf("seedTenantPlan(firmPlan): %v", err)
	}

	for _, id := range below {
		var steps []struct {
			ord             int
			kind            string
			workflowRoleKey *string
		}
		rows, err := super.Query(ctx,
			`SELECT rs.ord, rs.kind, rs.workflow_role_key
			   FROM approval_run_steps rs JOIN approval_runs r ON r.id = rs.run_id
			  WHERE r.invoice_id = $1 AND rs.state = 'pending' ORDER BY rs.ord`, id)
		if err != nil {
			t.Fatalf("read pending run steps for %s: %v", id, err)
		}
		for rows.Next() {
			var s struct {
				ord             int
				kind            string
				workflowRoleKey *string
			}
			if err := rows.Scan(&s.ord, &s.kind, &s.workflowRoleKey); err != nil {
				t.Fatalf("scan run step: %v", err)
			}
			steps = append(steps, s)
		}
		rows.Close()
		if len(steps) != 2 {
			t.Errorf("invoice %s carries %d pending step(s), want exactly 2", id, len(steps))
			continue
		}
		if steps[0].workflowRoleKey == nil || *steps[0].workflowRoleKey != "fin_mgr" || steps[0].ord != 0 {
			t.Errorf("invoice %s step 0 = %+v, want fin_mgr at ord 0", id, steps[0])
		}
		if steps[1].workflowRoleKey == nil || *steps[1].workflowRoleKey != "compliance" || steps[1].ord != 1 {
			t.Errorf("invoice %s step 1 = %+v, want compliance at ord 1", id, steps[1])
		}
		for _, forbidden := range []string{"fin_dir", "cfo"} {
			for _, s := range steps {
				if s.workflowRoleKey != nil && *s.workflowRoleKey == forbidden {
					t.Errorf("invoice %s names %q, which is above this invoice's amount", id, forbidden)
				}
			}
		}
	}

	var controlNamesFinDir bool
	if err := super.QueryRow(ctx,
		`SELECT EXISTS (
		    SELECT 1 FROM approval_run_steps rs JOIN approval_runs r ON r.id = rs.run_id
		     WHERE r.invoice_id = $1 AND rs.workflow_role_key = 'fin_dir'
		 )`, control).Scan(&controlNamesFinDir); err != nil {
		t.Fatalf("check the control invoice's steps: %v", err)
	}
	if !controlNamesFinDir {
		t.Error("the 300,000,000 control invoice never materialises fin_dir — this loop cannot tell a real condition from a policy with none")
	}
}

// D-34. Both sides of the threshold, against the real materialise/ArmTx path.
func TestSeed_AboveThresholdInvoicesGetAnOpenRunAndBelowGetApproved(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy threshold")
	above := f.addValidatedInvoice("DEMO-T-THR-ABOVE", &backlogTotals[2]) // 180,600
	below := f.addValidatedInvoice("DEMO-T-THR-BELOW", &backlogTotals[3]) // 94,600

	if _, err := seedTenant(context.Background(), app, f.tenantID); err != nil {
		t.Fatalf("seedTenant: %v", err)
	}

	state, closedBy, runs := runOf(t, super, above)
	if runs != 1 {
		t.Errorf("the 180,600 invoice carries %d run(s), want 1", runs)
	} else {
		if state != "open" {
			t.Errorf("the 180,600 invoice's run is %q, want open (the then-lane approval)", state)
		}
		if closedBy != nil {
			t.Errorf("the open run names closed_by %q, want NULL", *closedBy)
		}
	}

	state, closedBy, runs = runOf(t, super, below)
	if runs != 1 {
		t.Errorf("the 94,600 invoice carries %d run(s), want 1", runs)
	} else {
		if state != "approved" {
			t.Errorf("the 94,600 invoice's run is %q, want approved (the else-lane autoapprove)", state)
		}
		if closedBy == nil || *closedBy != "system" {
			t.Errorf("the approved run names closed_by %v, want \"system\"", closedBy)
		}
	}
}

// AC-3. Q10 decided: the in-house active policy gains a trailing root-level
// notify step, written before the seal. It never gates on amount (both an
// above- and a below-threshold invoice materialise it, always "skipped" —
// notify never blocks) and never changes either run's terminal state.
func TestSeed_ActivePolicyCarriesTheNotifyStepQ10Decided(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy notify step")
	above := f.addValidatedInvoice("DEMO-T-NOTIFY-ABOVE", &backlogTotals[2]) // 180,600
	below := f.addValidatedInvoice("DEMO-T-NOTIFY-BELOW", &backlogTotals[3]) // 94,600
	ctx := context.Background()

	if _, err := seedTenant(ctx, app, f.tenantID); err != nil {
		t.Fatalf("seedTenant: %v", err)
	}

	active := activeVersionOf(t, super, f.tenantID)
	var notifyCount int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM approval_policy_steps WHERE version_id = $1 AND kind = 'notify'`,
		active.ID).Scan(&notifyCount); err != nil {
		t.Fatalf("count notify steps: %v", err)
	}
	if notifyCount != 1 {
		t.Fatalf("the active version carries %d notify step(s), want exactly 1", notifyCount)
	}

	var parentIsNull bool
	var ord int
	var target, channel *string
	var sla *int
	if err := super.QueryRow(ctx,
		`SELECT parent_step_id IS NULL, ord, notify_target, notify_channel, sla_hours
		   FROM approval_policy_steps WHERE version_id = $1 AND kind = 'notify'`, active.ID).
		Scan(&parentIsNull, &ord, &target, &channel, &sla); err != nil {
		t.Fatalf("read the notify step: %v", err)
	}
	if !parentIsNull {
		t.Error("the notify step's parent_step_id is not NULL; it must be a root step")
	}
	if ord != 1 {
		t.Errorf("the notify step's ord = %d, want 1", ord)
	}
	if target == nil || *target != "Tax Team" {
		t.Errorf("notify_target = %v, want \"Tax Team\"", target)
	}
	if channel == nil || *channel != "In-app" {
		t.Errorf("notify_channel = %v, want \"In-app\"", channel)
	}
	if sla != nil {
		t.Errorf("the notify step carries sla_hours = %d, want NULL", *sla)
	}

	for _, inv := range []struct{ label, id, wantState string }{
		{"the above-threshold", above, "open"},
		{"the below-threshold", below, "approved"},
	} {
		state, _, runs := runOf(t, super, inv.id)
		if runs != 1 {
			t.Errorf("%s invoice carries %d run(s), want 1", inv.label, runs)
			continue
		}
		if state != inv.wantState {
			t.Errorf("%s invoice's run is %q, want %q — the notify step must not change run state", inv.label, state, inv.wantState)
		}
	}

	var skipped int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM approval_run_steps rs JOIN approval_runs r ON r.id = rs.run_id
		  WHERE r.invoice_id IN ($1, $2) AND rs.kind = 'notify' AND rs.state = 'skipped'`,
		above, below).Scan(&skipped); err != nil {
		t.Fatalf("count skipped notify run steps: %v", err)
	}
	if skipped != 2 {
		t.Errorf("skipped notify run_steps across both runs = %d, want 2 — the notify step must materialise on BOTH lanes", skipped)
	}
}

// AC-11's named tripwire, re-founded on the shape the import actually produces:
// the wizard maps only Invoice No, so total lands NULL and evalCondition folds
// it to zero. The live consumer is e2e/topology/persona-surfaces.spec.ts:305-310,
// which validates two in-house invoices at total 1075 on every gate run.
func TestSeed_InhouseCanFileFixtureStaysOnTheAutoapproveLane(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy autoapprove lane")

	small := "107.50"
	gate := "1075.00"
	lanes := []struct{ label, id string }{
		{"a NULL total (what the import wizard stores)", f.addValidatedInvoice("DEMO-T-LANE-NULL", nil)},
		{"total 1075 (persona-surfaces.spec.ts's createValidatedInvoice)", f.addValidatedInvoice("DEMO-T-LANE-GATE", &gate)},
		{"total 107.50", f.addValidatedInvoice("DEMO-T-LANE-SMALL", &small)},
	}
	// Control: with no above-threshold row, a seeder carrying no condition at
	// all — everything autoapproves — would satisfy the loop below.
	control := f.addValidatedInvoice("DEMO-T-LANE-ABOVE", &backlogTotals[0])

	if _, err := seedTenant(context.Background(), app, f.tenantID); err != nil {
		t.Fatalf("seedTenant: %v", err)
	}

	for _, lane := range lanes {
		state, _, runs := runOf(t, super, lane.id)
		if runs != 1 {
			t.Errorf("%s carries %d run(s), want 1", lane.label, runs)
			continue
		}
		if state != "approved" {
			t.Errorf("%s closed %q, want approved — lowering the 100,000 threshold breaks [inhouse-can-file] on the deploy gate with nothing linking cause to effect", lane.label, state)
		}
	}
	if state, _, _ := runOf(t, super, control); state != "open" {
		t.Errorf("the above-threshold control closed %q, want open — this test cannot tell an else-lane autoapprove from a policy with no condition", state)
	}
}

// AC-3's oracle. The name is already forward-referenced by
// e2e/topology/persona-surfaces.spec.ts:322-329 — do not rename it.
func TestSeed_AwaitingApprovalIsNonZeroAndBelowValidated(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy rollup oracle")
	f.addBacklog()

	if _, err := seedTenant(context.Background(), app, f.tenantID); err != nil {
		t.Fatalf("seedTenant: %v", err)
	}

	r := rollupFor(t, app, f.tenantID, f.memberID)
	if got := r.Totals.AwaitingApproval; got != 3 {
		t.Errorf("awaiting_approval = %d, want 3", got)
	}
	if got := r.Totals.Counts.Validated; got != 4 {
		t.Errorf("counts.validated = %d, want 4", got)
	}
	// The two properties persona-surfaces.spec.ts asserts on the live build,
	// restated so a regression names the surface it breaks.
	if r.Totals.AwaitingApproval == 0 {
		t.Error("awaiting_approval is 0, so the Approvals badge never renders and the gate's first guard goes red")
	}
	if r.Totals.AwaitingApproval == r.Totals.Counts.Validated {
		t.Error("awaiting_approval equals counts.validated, so the topology oracle cannot tell the two fields apart")
	}
}

// D-34. The Go-side mirror of isRowSelectable (frontend/app/src/lib/invoices.ts:1170-1172),
// so a broken journey fails in `go test` and not first on the deploy gate.
func TestSeed_SelectableRowsSurviveArming(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy selectable rows")
	ids := f.addBacklog()

	if _, err := seedTenant(context.Background(), app, f.tenantID); err != nil {
		t.Fatalf("seedTenant: %v", err)
	}

	facts := rowFactsOf(t, app, f.tenantID, ids)
	below := 0
	for i, id := range ids {
		fact, ok := facts[id]
		if !ok {
			t.Errorf("the invoice at %s has no approval facts; the wire sends approval:null and isRowSelectable fails OPEN", backlogTotals[i])
			continue
		}
		// backlogTotals[3] is the only entry at or below 100,000.
		if i == len(backlogTotals)-1 {
			below++
			if fact.RunState == "open" {
				t.Errorf("the invoice at %s reports run_state=open, so isRowSelectable drops it from the batch", backlogTotals[i])
			}
			continue
		}
		if fact.RunState != "open" {
			t.Errorf("the invoice at %s reports run_state=%q, want open — this mirror cannot discriminate if every row is selectable", backlogTotals[i], fact.RunState)
		}
	}
	if below == 0 {
		t.Fatal("no at-or-below-threshold invoice in the fixture, so the selectability loop asserted nothing")
	}
}

// draftVersionOf returns the tenant's Executive escalation draft version id.
func draftVersionOf(t *testing.T, super *pgxpool.Pool, tenantID string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`SELECT v.id::text FROM approval_policy_versions v JOIN approval_policies p ON p.id = v.policy_id
		  WHERE v.tenant_id = $1 AND p.name = 'Executive escalation' AND p.deleted_at IS NULL`, tenantID).
		Scan(&id); err != nil {
		t.Fatalf("the Executive escalation draft is absent for %s: %v", tenantID, err)
	}
	return id
}

// AC-4. seedTenant writes the Executive escalation draft alongside the
// active in-house policy: polH2's shape (three roots, five steps — cfo and
// ceo sit in the condition's then-lane, not at the root), one unsealed
// inactive version, published_by NULL. "Capital expenditure" (polH2's SPA
// name) must appear nowhere — this seeder renames it.
func TestSeed_WritesTheExecutiveEscalationDraft(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy draft")
	ctx := context.Background()

	if _, err := seedTenant(ctx, app, f.tenantID); err != nil {
		t.Fatalf("seedTenant: %v", err)
	}

	names := policyNames(t, super, f.tenantID)
	if !slices.Contains(names, "Executive escalation") {
		t.Fatalf("policy names = %v, want \"Executive escalation\" among them", names)
	}
	if slices.Contains(names, "Capital expenditure") {
		t.Error("\"Capital expenditure\" (polH2's SPA name) appears; this seeder must rename it to \"Executive escalation\"")
	}

	versionID := draftVersionOf(t, super, f.tenantID)
	var sealed, isActive bool
	var publishedBy *string
	if err := super.QueryRow(ctx,
		`SELECT sealed, is_active, published_by FROM approval_policy_versions WHERE id = $1`, versionID).
		Scan(&sealed, &isActive, &publishedBy); err != nil {
		t.Fatalf("read the draft version: %v", err)
	}
	if sealed {
		t.Error("the draft version is sealed, want unsealed")
	}
	if isActive {
		t.Error("the draft version is active, want inactive")
	}
	if publishedBy != nil {
		t.Errorf("the draft version's published_by = %q, want NULL", *publishedBy)
	}

	got := stepTreeOf(t, super, versionID)
	want := []approval.Step{
		{Kind: "approval", WorkflowRoleKey: ptr("line_mgr")},
		{Kind: "approval", WorkflowRoleKey: ptr("fin_dir")},
		{Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("1000000000.00"),
			Then: []approval.Step{
				{Kind: "approval", WorkflowRoleKey: ptr("cfo")},
				{Kind: "approval", WorkflowRoleKey: ptr("ceo")},
			}},
	}
	assertStepTree(t, got, want, "Executive escalation draft (polH2)")
}

// AC-4. The draft's approval_policies.id is DERIVED from (tenant_id, name),
// not server-minted — the one write with no uniqueness guard behind it. A
// racing boot that lost the ON CONFLICT DO NOTHING must not duplicate it: a
// hard-deleted draft, re-seeded, comes back under the SAME id.
func TestSeed_TheDraftCarriesADeterministicIdSoARacingBootCannotDuplicateIt(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy draft deterministic id")
	ctx := context.Background()

	if _, err := seedTenant(ctx, app, f.tenantID); err != nil {
		t.Fatalf("first seedTenant: %v", err)
	}
	var policyID string
	if err := super.QueryRow(ctx,
		`SELECT id::text FROM approval_policies WHERE tenant_id = $1 AND name = 'Executive escalation'`,
		f.tenantID).Scan(&policyID); err != nil {
		t.Fatalf("read the draft's policy id: %v", err)
	}

	// Hard-delete under replica mode: the content lock refuses a plain DELETE
	// on nothing here (the draft is unsealed), but this mirrors
	// teardownApprovalRows's idiom for consistency.
	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM approval_policy_steps WHERE tenant_id = $1 AND version_id IN
		(SELECT id FROM approval_policy_versions WHERE policy_id = $2)`, f.tenantID, policyID); err != nil {
		t.Fatalf("delete draft steps: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM approval_policy_versions WHERE policy_id = $1`, policyID); err != nil {
		t.Fatalf("delete draft version: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM approval_policies WHERE id = $1`, policyID); err != nil {
		t.Fatalf("delete draft policy: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the draft's removal: %v", err)
	}

	if _, err := seedTenant(ctx, app, f.tenantID); err != nil {
		t.Fatalf("second seedTenant: %v", err)
	}
	var reseededID string
	if err := super.QueryRow(ctx,
		`SELECT id::text FROM approval_policies WHERE tenant_id = $1 AND name = 'Executive escalation'`,
		f.tenantID).Scan(&reseededID); err != nil {
		t.Fatalf("read the re-seeded draft's policy id: %v", err)
	}
	if reseededID != policyID {
		t.Errorf("the re-seeded draft's id = %s, want the SAME derived id %s", reseededID, policyID)
	}
	var n int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM approval_policies WHERE tenant_id = $1 AND name = 'Executive escalation'`,
		f.tenantID).Scan(&n); err != nil {
		t.Fatalf("count draft policies: %v", err)
	}
	if n != 1 {
		t.Errorf("%d \"Executive escalation\" polic(y/ies), want exactly 1 — a deterministic id must not duplicate", n)
	}
}

// AC-4. The draft must never govern. The never-arms half alone is
// structurally guaranteed by ArmTx's WHERE is_active (engine.go:134) and
// proves nothing by itself; this test also asserts the draft EXISTS.
func TestSeed_TheDraftIsNeverActivatedAndNeverArms(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy draft never arms")
	f.addBacklog()
	ctx := context.Background()

	if _, err := seedTenant(ctx, app, f.tenantID); err != nil {
		t.Fatalf("seedTenant: %v", err)
	}

	versionID := draftVersionOf(t, super, f.tenantID) // fails loudly if the draft is absent
	// EXISTS is only half of it: a draft that got sealed and activated would
	// still satisfy every count below, because it would be the version the runs
	// name rather than an extra one.
	assertDraftUntouched(t, super, f.tenantID)

	var runsOnDraft int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM approval_runs WHERE tenant_id = $1 AND policy_version_id = $2`,
		f.tenantID, versionID).Scan(&runsOnDraft); err != nil {
		t.Fatalf("count runs on the draft version: %v", err)
	}
	if runsOnDraft != 0 {
		t.Errorf("%d run(s) name the draft version, want 0 — a draft must never govern", runsOnDraft)
	}

	var ceoSteps int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM approval_run_steps WHERE tenant_id = $1 AND workflow_role_key = 'ceo'`,
		f.tenantID).Scan(&ceoSteps); err != nil {
		t.Fatalf("count ceo run steps: %v", err)
	}
	if ceoSteps != 0 {
		t.Errorf("%d run step(s) name ceo, want 0 — ceo exists only on the draft, which never arms", ceoSteps)
	}
}

// AC-7. A second boot must write nothing and raise nothing: a step INSERT after
// the seal is 23001, is_active before sealed is 23514, a second active version
// is 23505.
func TestSeed_IsIdempotentAcrossBoots(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy idempotent")
	f.addBacklog()
	ctx := context.Background()

	first, err := seedTenant(ctx, app, f.tenantID)
	if err != nil {
		t.Fatalf("first seedTenant: %v", err)
	}
	if !first.VersionCreated {
		t.Error("the first boot reports VersionCreated=false; nothing had published a policy yet")
	}
	if first.RunsArmed != len(backlogTotals) {
		t.Errorf("the first boot armed %d run(s), want %d", first.RunsArmed, len(backlogTotals))
	}

	second, err := seedTenant(ctx, app, f.tenantID)
	if err != nil {
		t.Fatalf("second seedTenant raised: %v", err)
	}
	if second.VersionCreated {
		t.Error("the second boot created another version; one active version per tenant is a UNIQUE index")
	}
	if second.VersionID != first.VersionID {
		t.Errorf("the second boot reports version %s, want the first boot's %s", second.VersionID, first.VersionID)
	}
	if second.BacklogFound != 0 || second.RunsArmed != 0 {
		t.Errorf("the second boot found %d and armed %d, want 0 and 0 — the anti-join excludes every invoice the first sweep touched",
			second.BacklogFound, second.RunsArmed)
	}
	for table, want := range map[string]int{
		"approval_policies":        2, // the active policy plus the Executive escalation draft
		"approval_policy_versions": 2,
		"approval_policy_steps":    9, // 4 active (with the Q10 notify) + 5 draft (polH2)
		"approval_runs":            len(backlogTotals),
	} {
		if got := countRows(t, super, table, f.tenantID); got != want {
			t.Errorf("%s holds %d row(s) after two boots, want %d", table, got, want)
		}
	}
}

// D-34, THE tripwire — the difference between converging and insert-if-absent.
// db.PurgeDemoTenants empties approval_runs on every gated boot and deliberately
// spares the three policy tables (docs/demo-reset.md); db.Reset truncates the same
// rows, but only in a pr-<N> environment. awaiting_approval's NOT EXISTS (approved
// run) is satisfied VACUOUSLY by an invoice with zero runs. So an insert-if-absent
// seeder finds its policy on deploy 2, no-ops, arms nothing, and awaiting_approval
// silently becomes counts.validated. Its absence would ship a story whose oracle
// dies on the second deploy.
func TestSeed_AfterResetRearmsSeededInvoices(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy post-reset re-arm")
	f.addBacklog()
	ctx := context.Background()

	first, err := seedTenant(ctx, app, f.tenantID)
	if err != nil {
		t.Fatalf("first seedTenant: %v", err)
	}
	if got := rollupFor(t, app, f.tenantID, f.memberID).Totals.AwaitingApproval; got != 3 {
		t.Fatalf("awaiting_approval after the first boot = %d, want 3", got)
	}
	if states := runStates(t, super, f.tenantID); states["open"] != 3 || states["approved"] != 1 {
		t.Fatalf("runs after the first boot = %v, want 3 open + 1 approved", states)
	}

	wipeRuns(t, super, f.tenantID)

	// Three controls. Without them the second boot's "3" could be the first
	// boot's leftovers, or this test could stop exercising convergence at all.
	if got := countRows(t, super, "approval_runs", f.tenantID); got != 0 {
		t.Fatalf("the run wipe left %d run(s); the assertions below would read the first boot's work", got)
	}
	if got := countRows(t, super, "approval_policy_versions", f.tenantID); got != 2 {
		t.Fatalf("the wipe left %d policy version(s), want 2 (the active version plus the draft) — db.Reset excludes the three policy tables, so this test no longer exercises the insert-if-absent branch", got)
	}
	assertDraftUntouched(t, super, f.tenantID)
	if got := rollupFor(t, app, f.tenantID, f.memberID).Totals.AwaitingApproval; got != 4 {
		t.Fatalf("awaiting_approval with the policy standing and no runs = %d, want 4 — the vacuous NOT EXISTS this whole test exists to catch", got)
	}

	second, err := seedTenant(ctx, app, f.tenantID)
	if err != nil {
		t.Fatalf("second seedTenant: %v", err)
	}
	if second.VersionCreated {
		t.Error("the second boot created a version; step 1 must no-op while step 2 still runs")
	}
	if second.VersionID != first.VersionID {
		t.Errorf("the second boot reports version %s, want the first boot's %s", second.VersionID, first.VersionID)
	}
	if second.RunsArmed != len(backlogTotals) {
		t.Errorf("the second boot armed %d run(s), want %d — an insert-if-absent seeder finds its policy, no-ops and arms nothing",
			second.RunsArmed, len(backlogTotals))
	}

	r := rollupFor(t, app, f.tenantID, f.memberID)
	if r.Totals.AwaitingApproval != 3 {
		t.Errorf("awaiting_approval after the re-arm = %d, want 3 and NOT 4; at 4 the badge equals counts.validated and the topology oracle stops discriminating", r.Totals.AwaitingApproval)
	}
	if r.Totals.Counts.Validated != 4 {
		t.Errorf("counts.validated = %d, want 4", r.Totals.Counts.Validated)
	}
	if states := runStates(t, super, f.tenantID); states["open"] != 3 || states["approved"] != 1 {
		t.Errorf("runs after the re-arm = %v, want 3 open + 1 approved rebuilt", states)
	}
	assertDraftUntouched(t, super, f.tenantID)
}

// assertDraftUntouched confirms the Executive escalation draft survived
// whatever just ran: still exactly one version, unsealed and inactive. Reset
// truncates approval_runs only — the draft, like the active version, is a
// config-table row Reset deliberately leaves standing.
func assertDraftUntouched(t *testing.T, super *pgxpool.Pool, tenantID string) {
	t.Helper()
	var sealed, isActive bool
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*), bool_or(v.sealed), bool_or(v.is_active)
		   FROM approval_policy_versions v JOIN approval_policies p ON p.id = v.policy_id
		  WHERE v.tenant_id = $1 AND p.name = 'Executive escalation'`, tenantID).
		Scan(&n, &sealed, &isActive); err != nil {
		t.Fatalf("read the Executive escalation draft for %s: %v", tenantID, err)
	}
	if n != 1 {
		t.Errorf("Executive escalation carries %d version(s), want exactly 1", n)
	}
	if sealed {
		t.Error("Executive escalation's version is sealed, want unsealed — it must never be published")
	}
	if isActive {
		t.Error("Executive escalation's version is active, want inactive — a draft must never govern")
	}
}

// AC-1/AC-2/AC-9, asserted by VALUE against live data: the journey-safety
// guarantee, not a reasoned one. Touches the REAL firm AND in-house demo
// tenants — re-aimed from TestSeed_ArmsOnlyTheInHouseDemoTenant, which
// asserted the firm tenant held ZERO rows; task-568 seeds it too.
func TestSeed_ArmsOnlyTheDemoTenants(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	// Self-seeds rather than assuming a seeded database: the CI job bootstraps
	// and migrates but never runs db/seed.dev.sql, so the demo tenants' invoices
	// exist only because some earlier suite seeded them. Same call and reason as
	// internal/demodocs/store_test.go's TestRLS_DemoDocsLeavesTheSeededNoSourceInvoiceUnlinked.
	// Last test in the file: db.Seed re-anchors created_at and re-enables every rule.
	if err := db.Seed(ctx, os.Getenv("DATABASE_SUPERUSER_URL"), dbsql.FS); err != nil {
		t.Fatalf("db.Seed (establish the real demo fixtures): %v", err)
	}

	// The blanket teardown below restores the baseline exactly only if the
	// baseline is empty. Assert that rather than assume it, and register the
	// teardown before Seed so a mid-way failure still cleans up. Both real demo
	// tenants now: DemoTenants carries both.
	for _, tenantID := range []string{inhouseDemoTenantID, firmDemoTenantID} {
		for _, table := range []string{"approval_policies", "approval_policy_versions", "approval_policy_steps", "approval_runs"} {
			if got := countRows(t, super, table, tenantID); got != 0 {
				t.Fatalf("tenant %s already holds %d %s row(s); this test's teardown would delete rows it did not create", tenantID, got, table)
			}
		}
		id := tenantID
		t.Cleanup(func() { teardownApprovalRows(t, super, id) })
	}

	// A throwaway tenant shaped exactly like a seedable one. Outside the
	// allowlist it must stay untouched — the value assertion AC-2 demands.
	outsider := newFixture(t, super, app, "demopolicy outside the allowlist")
	outsider.addBacklog()
	if slices.Contains(DemoTenants, outsider.tenantID) {
		t.Fatalf("the throwaway tenant %s collided with the allowlist", outsider.tenantID)
	}

	// Control needles: "zero policy rows" proves nothing about a tenant that
	// had nothing to arm in the first place.
	for _, tenant := range []struct{ label, id string }{
		{"the in-house demo tenant", inhouseDemoTenantID},
		{"the firm demo tenant", firmDemoTenantID},
		{"the throwaway tenant", outsider.tenantID},
	} {
		if n := validatedCount(t, super, tenant.id); n == 0 {
			t.Fatalf("%s holds no validated invoice, so the assertions below would pass vacuously (has db/seed.dev.sql run?)", tenant.label)
		}
	}

	if _, err := Seed(ctx, app, nil); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	// In-house: two policies (the active one plus the Executive escalation
	// draft), two versions, nine steps (4 active + 5 draft).
	inNames := policyNames(t, super, inhouseDemoTenantID)
	if len(inNames) != 2 {
		t.Fatalf("the in-house tenant holds %d named polic(y/ies) %v, want 2", len(inNames), inNames)
	}
	if got := countRows(t, super, "approval_policy_versions", inhouseDemoTenantID); got != 2 {
		t.Errorf("in-house approval_policy_versions = %d, want 2", got)
	}
	if got := countRows(t, super, "approval_policy_steps", inhouseDemoTenantID); got != 9 {
		t.Errorf("in-house approval_policy_steps = %d, want 9 (4 active + 5 draft)", got)
	}
	inActive := activeVersionOf(t, super, inhouseDemoTenantID)
	if !inActive.Sealed || !inActive.IsActive {
		t.Errorf("the in-house active version is sealed=%v is_active=%v, want both true", inActive.Sealed, inActive.IsActive)
	}
	if n := countRows(t, super, "approval_runs", inhouseDemoTenantID); n == 0 {
		t.Error("the in-house tenant got no run at all; the untouched assertions below are satisfied by a seeder that does nothing")
	}

	// The policy NAME must match neither of roles.spec.ts's in-house sweeps:
	// DeletePolicy deactivates the governed version in the same transaction, so
	// a collision drops awaiting_approval to 0 mid-run with nothing red.
	for _, name := range inNames {
		if name == unsavedPolicyName || policyNameSweep.MatchString(name) {
			t.Errorf("an in-house policy is named %q, which e2e/topology/roles.spec.ts's in-house afterAll sweep deletes", name)
		}
	}

	// Firm: one policy, one version, seven steps, one run per validated
	// invoice — the measured oracle (7 validated, largest 193,500, so nothing
	// reaches polF1's 250,000,000 condition).
	firmNames := policyNames(t, super, firmDemoTenantID)
	if len(firmNames) != 1 || firmNames[0] != "Standard approval policy" {
		t.Errorf("the firm tenant's policy names = %v, want exactly [\"Standard approval policy\"]", firmNames)
	}
	if got := countRows(t, super, "approval_policy_versions", firmDemoTenantID); got != 1 {
		t.Errorf("firm approval_policy_versions = %d, want 1", got)
	}
	if got := countRows(t, super, "approval_policy_steps", firmDemoTenantID); got != 7 {
		t.Errorf("firm approval_policy_steps = %d, want 7", got)
	}
	if got := countRows(t, super, "approval_runs", firmDemoTenantID); got != 7 {
		t.Errorf("firm approval_runs = %d, want 7 (one per validated invoice)", got)
	}

	// AC-2 on the REAL firm tenant, not a fixture. polF1's two conditions sit at
	// 250,000,000 and 1,000,000,000 while the largest seeded firm invoice is
	// 193,500, so every run takes the same two-step lane: fin_mgr then compliance.
	// A run that materialised fin_dir or cfo would mean the thresholds moved.
	firmRuns, err := super.Query(ctx,
		`SELECT r.id::text, coalesce(string_agg(rs.workflow_role_key, ',' ORDER BY rs.ord)
		            FILTER (WHERE rs.state = 'pending'), '')
		   FROM approval_runs r LEFT JOIN approval_run_steps rs ON rs.run_id = r.id
		  WHERE r.tenant_id = $1 GROUP BY r.id`, firmDemoTenantID)
	if err != nil {
		t.Fatalf("read the firm tenant's run shapes: %v", err)
	}
	shapes := 0
	for firmRuns.Next() {
		var runID, pending string
		if err := firmRuns.Scan(&runID, &pending); err != nil {
			t.Fatalf("scan a firm run shape: %v", err)
		}
		shapes++
		if pending != "fin_mgr,compliance" {
			t.Errorf("firm run %s has pending steps %q, want \"fin_mgr,compliance\" (ord 0 then ord 1)", runID, pending)
		}
	}
	firmRuns.Close()
	if err := firmRuns.Err(); err != nil {
		t.Fatalf("read the firm tenant's run shapes: %v", err)
	}
	if shapes != 7 {
		t.Errorf("walked %d firm run shape(s), want 7 — an empty collection would satisfy the loop above vacuously", shapes)
	}

	// AC-9: in-house discriminates (3 of 4), the firm cannot (7 of 7) — expected,
	// not a bug, per the measured oracle.
	inRollup := rollupFor(t, app, inhouseDemoTenantID, anyActiveMember(t, super, inhouseDemoTenantID))
	if inRollup.Totals.AwaitingApproval != 3 || inRollup.Totals.Counts.Validated != 4 {
		t.Errorf("in-house awaiting_approval/validated = %d/%d, want 3/4", inRollup.Totals.AwaitingApproval, inRollup.Totals.Counts.Validated)
	}
	firmRollup := rollupFor(t, app, firmDemoTenantID, anyActiveMember(t, super, firmDemoTenantID))
	if firmRollup.Totals.AwaitingApproval != 7 || firmRollup.Totals.Counts.Validated != 7 {
		t.Errorf("firm awaiting_approval/validated = %d/%d, want 7/7 — the firm badge cannot discriminate, and that is expected",
			firmRollup.Totals.AwaitingApproval, firmRollup.Totals.Counts.Validated)
	}

	if n := countRows(t, super, "approval_policy_versions", outsider.tenantID); n != 0 {
		t.Errorf("a throwaway tenant outside the allowlist holds %d approval_policy_versions row(s), want 0", n)
	}
	if n := countRows(t, super, "approval_runs", outsider.tenantID); n != 0 {
		t.Errorf("a throwaway tenant outside the allowlist holds %d approval_runs row(s), want 0", n)
	}
}
