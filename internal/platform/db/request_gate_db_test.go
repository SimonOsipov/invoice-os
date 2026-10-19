// AUDIT-10-01/AUDIT-12-07: the request-seam membership gate. WithinRequestTenantTxOpts
// refuses a caller with no membership row in the current tenant, or one whose row
// exists and is not 'active', identically (ErrNotActiveMember) -- the strict rule;
// D-17's NARROW no-row exception is gone.
//
// Named TestRLS_ so ci.yml:331's `-run TestRLS` step reaches every case here —
// ci_filter_coverage_test.go fails any name no filter reaches.
package db_test

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// seedMembershipWithStatus is seedMembership plus an explicit status — the existing
// helper relies on the 'active' DEFAULT and cannot seed the rows this suite gates on.
// Superuser (BYPASSRLS), so no tenant context is needed.
func seedMembershipWithStatus(t *testing.T, tenantID, userID, role, status string) {
	t.Helper()
	id := uuid.NewString()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO memberships (id, tenant_id, user_id, role, status) VALUES ($1, $2, $3, $4, $5)`,
		id, tenantID, userID, role, status,
	); err != nil {
		t.Fatalf("seed membership (%s): %v", status, err)
	}
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM memberships WHERE id = $1`, id)
	})
}

// TestRLS_SeedMembershipWithStatusSeedsTheRequestedStatus (AUDIT-12-05): the
// sweep's fixture for TestRLS_WithinRequestTenantTxStillBeginsAPlainTransaction
// (userID := uuid.NewString(); seedMembershipWithStatus(t, tenantID, userID,
// role, "active")) must leave a real, readable-back row -- not merely not
// error. Deleting the INSERT must fail this test, not pass silently.
func TestRLS_SeedMembershipWithStatusSeedsTheRequestedStatus(t *testing.T) {
	h := requireHarness(t)
	userID := uuid.NewString()

	seedMembershipWithStatus(t, h.tenantA, userID, "admin", "active")

	var role, status string
	err := h.super.QueryRow(context.Background(),
		`SELECT role, status FROM memberships WHERE tenant_id = $1 AND user_id = $2`, h.tenantA, userID,
	).Scan(&role, &status)
	if err != nil {
		t.Fatalf("seedMembershipWithStatus seeded no memberships row: %v", err)
	}
	if role != "admin" {
		t.Fatalf("membership role = %q, want admin", role)
	}
	if status != "active" {
		t.Fatalf("membership status = %q, want active", status)
	}
}

// seamTracer records BOTH standalone statements and batched ones. pgx assigns the
// batch tracer by type-asserting ConnConfig.Tracer, so a QueryTracer-only type sees
// nothing a SendBatch carries — TestRLS_RequestSeamSkipsTheLookupForANonUUIDSubject
// would then pass having examined nothing.
type seamTracer struct {
	mu          sync.Mutex
	standalone  []string   // Query / QueryRow / Exec, including begin and commit
	batched     []string   // statements sent inside a SendBatch
	batchStarts [][]string // one entry per SendBatch: the SQL it queued, in order
}

var (
	_ pgx.QueryTracer = (*seamTracer)(nil)
	_ pgx.BatchTracer = (*seamTracer)(nil)
)

func (tr *seamTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.standalone = append(tr.standalone, d.SQL)
	return ctx
}

func (tr *seamTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (tr *seamTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceBatchStartData) context.Context {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var queued []string
	if d.Batch != nil {
		for _, qq := range d.Batch.QueuedQueries {
			queued = append(queued, qq.SQL)
		}
	}
	tr.batchStarts = append(tr.batchStarts, queued)
	return ctx
}

func (tr *seamTracer) TraceBatchQuery(_ context.Context, _ *pgx.Conn, d pgx.TraceBatchQueryData) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.batched = append(tr.batched, d.SQL)
}

func (tr *seamTracer) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}

// standaloneStmts returns only what a plain pgx.QueryTracer would have seen.
func (tr *seamTracer) standaloneStmts() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]string{}, tr.standalone...)
}

// allStmts returns every statement the connection carried, batched or not — both the
// queued SQL seen at SendBatch time and the per-statement callbacks.
func (tr *seamTracer) allStmts() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	out := append([]string{}, tr.standalone...)
	out = append(out, tr.batched...)
	for _, b := range tr.batchStarts {
		out = append(out, b...)
	}
	return out
}

func (tr *seamTracer) starts() [][]string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([][]string{}, tr.batchStarts...)
}

func countContaining(stmts []string, needle string) int {
	n := 0
	for _, s := range stmts {
		if strings.Contains(strings.ToLower(s), needle) {
			n++
		}
	}
	return n
}

// tracedSeamPool is tracedAppPool with a tracer that also sees batches.
func tracedSeamPool(t *testing.T) (*pgxpool.Pool, *seamTracer) {
	t.Helper()
	h := requireHarness(t)
	tr := &seamTracer{}
	cfg, err := pgxpool.ParseConfig(h.appURL)
	if err != nil {
		t.Fatalf("parse app url: %v", err)
	}
	cfg.ConnConfig.Tracer = tr
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new traced seam pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, tr
}

// requestCtx builds the ctx the auth middleware would hand the seam.
func requestCtx(subject, tenantID string) context.Context {
	return auth.WithIdentity(context.Background(),
		auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})
}

// TestRLS_RequestSeamRefusesASuspendedCaller (AC-1, AC-2): a suspended row in the
// current tenant refuses with ErrNotActiveMember and the closure never runs.
func TestRLS_RequestSeamRefusesASuspendedCaller(t *testing.T) {
	h := requireHarness(t)

	userID := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, userID, "admin", "suspended")

	ran := false
	err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantA), h.app, func(pgx.Tx) error {
		ran = true
		return nil
	})
	if !errors.Is(err, db.ErrNotActiveMember) {
		t.Fatalf("WithinRequestTenantTx err = %v, want db.ErrNotActiveMember", err)
	}
	if ran {
		t.Error("the closure ran for a suspended caller — the refusal must happen before user code touches the tx")
	}
	// AC-1's second export. Its value is pinned on the wire in AUDIT-10-07, not here.
	if db.NotActiveMemberMessage == "" {
		t.Error("db.NotActiveMemberMessage is empty — AUDIT-10-03 has no body to return")
	}
}

// TestRLS_RequestSeamRefusesAnInvitedCaller (AC-2, D-4): 'invited' is refused with the
// SAME sentinel — the gate reads "not active", not "suspended".
func TestRLS_RequestSeamRefusesAnInvitedCaller(t *testing.T) {
	h := requireHarness(t)

	userID := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, userID, "preparer", "invited")

	ran := false
	err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantA), h.app, func(pgx.Tx) error {
		ran = true
		return nil
	})
	if !errors.Is(err, db.ErrNotActiveMember) {
		t.Fatalf("WithinRequestTenantTx err = %v, want db.ErrNotActiveMember", err)
	}
	if ran {
		t.Error("the closure ran for an invited caller")
	}
}

// TestRLS_RequestSeamAdmitsAnActiveCaller (AC-2): an active row proceeds, and the
// closure's write commits — the gate must not leave the tx half-open or rolled back.
func TestRLS_RequestSeamAdmitsAnActiveCaller(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	userID := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, userID, "admin", "active")

	payload := "audit-10-admit-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM rls_fixture WHERE payload = $1`, payload)
	})

	ran := false
	if err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantA), h.app, func(tx pgx.Tx) error {
		ran = true
		_, err := tx.Exec(ctx, `INSERT INTO rls_fixture (tenant_id, payload) VALUES ($1, $2)`, h.tenantA, payload)
		return err
	}); err != nil {
		t.Fatalf("WithinRequestTenantTx: unexpected error for an active caller: %v", err)
	}
	if !ran {
		t.Fatal("the closure never ran for an active caller")
	}

	var n int
	if err := h.super.QueryRow(ctx, `SELECT count(*) FROM rls_fixture WHERE payload = $1`, payload).Scan(&n); err != nil {
		t.Fatalf("count fixture rows: %v", err)
	}
	if n != 1 {
		t.Errorf("committed fixture rows = %d, want 1 — the gate rolled back a tx it admitted", n)
	}
}

// TestRLS_RequestSeamAdmitsAReactivatedCaller (Core AC 4/5, D-2): status is read fresh
// per request, so reactivation takes effect in the SAME process with no cache flush
// and no new JWT.
func TestRLS_RequestSeamAdmitsAReactivatedCaller(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	userID := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, userID, "admin", "suspended")

	if err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantA), h.app, func(pgx.Tx) error {
		return nil
	}); !errors.Is(err, db.ErrNotActiveMember) {
		t.Fatalf("before reactivation: err = %v, want db.ErrNotActiveMember", err)
	}

	if _, err := h.super.Exec(ctx,
		`UPDATE memberships SET status = 'active' WHERE tenant_id = $1 AND user_id = $2`,
		h.tenantA, userID); err != nil {
		t.Fatalf("reactivate membership: %v", err)
	}

	ran := false
	if err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantA), h.app, func(pgx.Tx) error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("after reactivation: unexpected error: %v", err)
	}
	if !ran {
		t.Error("the closure never ran after reactivation")
	}
}

// TestRLS_RequestSeamRefusesACallerWithNoMembershipRow (AC-3, AUDIT-12): the strict
// rule -- no row is now refused exactly like a suspended one. Was
// TestRLS_RequestSeamAllowsACallerWithNoMembershipRow under D-6/D-17's NARROW rule.
func TestRLS_RequestSeamRefusesACallerWithNoMembershipRow(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	userID := uuid.NewString() // never seeded into memberships

	var n int
	if err := h.super.QueryRow(ctx,
		`SELECT count(*) FROM memberships WHERE user_id = $1`, userID).Scan(&n); err != nil {
		t.Fatalf("count memberships: %v", err)
	}
	if n != 0 {
		t.Fatalf("fixture is wrong: %d membership rows for a never-seeded user", n)
	}

	ran := false
	if err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantA), h.app, func(pgx.Tx) error {
		ran = true
		return nil
	}); !errors.Is(err, db.ErrNotActiveMember) {
		t.Fatalf("WithinRequestTenantTx: a caller with no membership row must be refused, got %v", err)
	}
	if ran {
		t.Error("the closure ran for a caller with no membership row")
	}
}

// TestRLS_RequestSeamSkipsTheLookupForANonUUIDSubject (AC-4): memberships.user_id is
// uuid, so a non-uuid subject can match no row — and a 22P02 inside the batch would
// poison the transaction. The seam must issue no membership statement at all.
func TestRLS_RequestSeamSkipsTheLookupForANonUUIDSubject(t *testing.T) {
	h := requireHarness(t)
	pool, tr := tracedSeamPool(t)

	ran := false
	if err := db.WithinRequestTenantTx(requestCtx("backfill-source-rows", h.tenantA), pool, func(pgx.Tx) error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("WithinRequestTenantTx: a non-uuid subject must proceed, got %v", err)
	}
	if !ran {
		t.Error("the closure never ran for a non-uuid subject")
	}

	stmts := tr.allStmts()
	// Control needle: without it the memberships assertion below can pass having
	// examined nothing (a tracer that saw no batch looks identical to a clean run).
	if countContaining(stmts, "set_config") == 0 {
		t.Fatalf("the tracer saw no set_config at all — it is not observing the seam's statements: %q", stmts)
	}
	if n := countContaining(stmts, "memberships"); n != 0 {
		t.Errorf("the seam issued %d memberships statement(s) for a non-uuid subject, want 0: %q", n, stmts)
	}
}

// TestRLS_RequestSeamIsScopedToTheCurrentTenant (AC-2): the lookup is RLS-scoped by
// app.current_tenant, so being active elsewhere must not rescue a suspension here.
func TestRLS_RequestSeamIsScopedToTheCurrentTenant(t *testing.T) {
	h := requireHarness(t)

	userID := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, userID, "admin", "active")
	seedMembershipWithStatus(t, h.tenantB, userID, "admin", "suspended")

	if err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantB), h.app, func(pgx.Tx) error {
		return nil
	}); !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("acting as tenant B (suspended there): err = %v, want db.ErrNotActiveMember", err)
	}

	ran := false
	if err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantA), h.app, func(pgx.Tx) error {
		ran = true
		return nil
	}); err != nil {
		t.Errorf("acting as tenant A (active there): unexpected error %v", err)
	}
	if !ran {
		t.Error("the closure never ran in the tenant where the caller is active")
	}
}

// TestRLS_RequestSeamIssuesOneRoundTripForTheGate (D-1): the membership lookup rides
// the set_config the seam already sends. A second round trip measured +144..+213us/op
// against +12..+42us/op for the batch.
func TestRLS_RequestSeamIssuesOneRoundTripForTheGate(t *testing.T) {
	h := requireHarness(t)
	pool, tr := tracedSeamPool(t)

	userID := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, userID, "admin", "active")

	if err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantA), pool, func(pgx.Tx) error {
		return nil
	}); err != nil {
		t.Fatalf("WithinRequestTenantTx: %v", err)
	}

	starts := tr.starts()
	if len(starts) != 1 {
		t.Fatalf("the seam opened %d batch(es), want exactly 1 — the gate must not be a separate round trip: %q", len(starts), starts)
	}
	queued := starts[0]
	if len(queued) != 2 {
		t.Fatalf("the gate's batch queued %d statement(s), want exactly 2: %q", len(queued), queued)
	}
	if !strings.Contains(strings.ToLower(queued[0]), "set_config") {
		t.Errorf("batch statement 1 = %q, want the set_config — the membership select is RLS-scoped by the GUC it sets, so it must be queued first", queued[0])
	}
	if !strings.Contains(strings.ToLower(queued[1]), "memberships") {
		t.Errorf("batch statement 2 = %q, want the memberships select", queued[1])
	}

	standalone := tr.standaloneStmts()
	if len(standalone) == 0 {
		t.Fatal("the tracer recorded no standalone statement at all — begin/commit should be there, so it is not attached")
	}
	if n := countContaining(standalone, "set_config"); n != 0 {
		t.Errorf("the seam still sent %d unbatched set_config statement(s): %q — the gate must ride the existing round trip, not add one", n, standalone)
	}
}

// TestRLS_WorkerSeamIsNotGatedByMembership (AC-8): WithinTenantTx is the identity-free
// core the M5 workers, seeders, backfills and tools/* CLIs run on. An identity in ctx
// must not reach it — gating it would break every one of them.
func TestRLS_WorkerSeamIsNotGatedByMembership(t *testing.T) {
	h := requireHarness(t)
	pool, tr := tracedSeamPool(t)

	userID := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, userID, "admin", "suspended")

	ran := false
	if err := db.WithinTenantTx(requestCtx(userID, h.tenantA), pool, h.tenantA, func(pgx.Tx) error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx: the worker core must not be gated, got %v", err)
	}
	if !ran {
		t.Error("the closure never ran on the worker core")
	}

	stmts := tr.allStmts()
	if countContaining(stmts, "set_config") == 0 {
		t.Fatalf("the tracer saw no set_config at all — it is not observing the core's statements: %q", stmts)
	}
	if n := countContaining(stmts, "memberships"); n != 0 {
		t.Errorf("the worker core issued %d memberships statement(s), want 0: %q", n, stmts)
	}
}

// TestRLS_RequestSeamHonoursTxOptionsWhileGating (AC-8): internal/archive is the only
// caller passing non-zero TxOptions. The gate builds its own tx, so it must keep
// handing opts to BeginTx — and both batched statements must be legal READ ONLY.
func TestRLS_RequestSeamHonoursTxOptionsWhileGating(t *testing.T) {
	h := requireHarness(t)
	pool, tr := tracedSeamPool(t)

	userID := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, userID, "admin", "active")

	// internal/archive/assemble.go:16 bundleTxOptions, verbatim.
	opts := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}

	ran := false
	if err := db.WithinRequestTenantTxOpts(requestCtx(userID, h.tenantA), pool, opts, func(tx pgx.Tx) error {
		ran = true
		var one int
		return tx.QueryRow(context.Background(), `SELECT 1`).Scan(&one)
	}); err != nil {
		t.Fatalf("WithinRequestTenantTxOpts: %v", err)
	}
	if !ran {
		t.Fatal("the closure never ran")
	}

	begins := onlyBegins(tr.standaloneStmts())
	if len(begins) != 1 {
		t.Fatalf("the seam issued %d begin-like statement(s), want exactly 1: %q", len(begins), begins)
	}
	const want = "begin isolation level repeatable read read only"
	if begins[0] != want {
		t.Errorf("begin sql = %q, want %q — the gate dropped the caller's TxOptions", begins[0], want)
	}
}

// TestRLS_PlatformDBDependsOnlyOnAuthPackage (AC-7, D-14): the gate must not pull a new
// repo package into internal/platform/db. internal/document/api_surface_test.go's
// TestDocument_ImportsNoRepoPackage fences the far side of the same edge transitively.
func TestRLS_PlatformDBDependsOnlyOnAuthPackage(t *testing.T) {
	ctx := t.Context()
	root, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}

	const module = "github.com/SimonOsipov/invoice-os"
	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "./internal/platform/db")
	cmd.Dir = strings.TrimSpace(string(root))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps ./internal/platform/db: %v\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		t.Fatalf("go list -deps returned %d line(s) — the command did not resolve the package, so the check below "+
			"is vacuous:\n%s", len(lines), out)
	}

	var repoDeps []string
	for _, line := range lines {
		dep := strings.TrimSpace(line)
		if strings.HasPrefix(dep, module) {
			repoDeps = append(repoDeps, dep)
		}
	}
	// Vacuity floor: the package must at least see itself.
	if len(repoDeps) == 0 {
		t.Fatalf("go list -deps found no %s package at all — the prefix match is broken:\n%s", module, out)
	}
	for _, dep := range repoDeps {
		if dep == module+"/internal/platform/db" || dep == module+"/internal/platform/auth" {
			continue
		}
		t.Errorf("internal/platform/db imports %s — it may depend only on internal/platform/auth", dep)
	}
}

// poisonedMembershipsPool shadows memberships with a pg_temp view that raises at
// execution time, so the gate's SECOND batched statement fails server-side while the
// set_config ahead of it succeeds. pg_temp is searched before public, so the shadow
// lives and dies with this connection and touches no shared schema.
func poisonedMembershipsPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := requireHarness(t)
	cfg, err := pgxpool.ParseConfig(h.appURL)
	if err != nil {
		t.Fatalf("parse app url: %v", err)
	}
	cfg.MaxConns = 1
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, `CREATE TEMP VIEW memberships AS
			SELECT (1/(random()*0)::int)::text::uuid AS user_id, 'active' AS status,
			       nullif(current_setting('app.current_tenant', true), '')::uuid AS tenant_id,
			       'preparer' AS role, false AS all_clients`)
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new poisoned pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestRLS_RequestSeamWrapsAMembershipReadError: a server-side failure on the membership
// select must surface as "read caller membership", never as the batch's close error.
// br.Close() returns the batch's stored error first, so classifying closeErr ahead of
// scanErr makes this wrap unreachable for every server-side error.
func TestRLS_RequestSeamWrapsAMembershipReadError(t *testing.T) {
	h := requireHarness(t)
	pool := poisonedMembershipsPool(t)

	ran := false
	err := db.WithinRequestTenantTx(requestCtx(uuid.NewString(), h.tenantA), pool, func(pgx.Tx) error {
		ran = true
		return nil
	})
	if err == nil {
		t.Fatal("the gate admitted a caller whose membership read failed server-side")
	}
	if ran {
		t.Error("the closure ran after the membership read failed - a poisoned tx must never reach user code")
	}
	if errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("a read failure was reported as a suspension: %v", err)
	}
	// Control: prove the injection fired rather than the select quietly returning no
	// rows, which would make the two assertions below vacuous.
	if !strings.Contains(err.Error(), "division by zero") {
		t.Fatalf("err = %v, want the injected division-by-zero - the shadow view never fired", err)
	}
	if !strings.Contains(err.Error(), "read caller membership") {
		t.Errorf("err = %v, want it wrapped as \"db: read caller membership\"", err)
	}
	if strings.Contains(err.Error(), "close batch") {
		t.Errorf("err = %v, want scanErr classified before closeErr", err)
	}
}

// TestRLS_RequestSeamRefusalIsNotTheUnauthenticatedSentinel (D-3): AUDIT-10-03 maps
// ErrNotActiveMember to 403 and ErrNoTenant to 401. Aliasing the two sentinels would
// satisfy every errors.Is assertion in this file while shipping the wrong status code.
func TestRLS_RequestSeamRefusalIsNotTheUnauthenticatedSentinel(t *testing.T) {
	h := requireHarness(t)

	userID := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, userID, "admin", "suspended")

	suspended := db.WithinRequestTenantTx(requestCtx(userID, h.tenantA), h.app, func(pgx.Tx) error { return nil })
	if !errors.Is(suspended, db.ErrNotActiveMember) {
		t.Fatalf("suspended caller err = %v, want db.ErrNotActiveMember", suspended)
	}
	if errors.Is(suspended, db.ErrNoTenant) {
		t.Errorf("the 403 refusal also satisfies db.ErrNoTenant (401): %v", suspended)
	}

	anon := db.WithinRequestTenantTx(context.Background(), h.app, func(pgx.Tx) error { return nil })
	if !errors.Is(anon, db.ErrNoTenant) {
		t.Fatalf("identity-free caller err = %v, want db.ErrNoTenant", anon)
	}
	if errors.Is(anon, db.ErrNotActiveMember) {
		t.Errorf("the 401 refusal also satisfies db.ErrNotActiveMember (403): %v", anon)
	}
}

// TestRLS_RequestSeamSkipsTheLookupForAnEmptySubject (AC-4): empty is not a uuid, so the
// lookup is skipped rather than sent as the 22P02 that would poison the batch.
func TestRLS_RequestSeamSkipsTheLookupForAnEmptySubject(t *testing.T) {
	h := requireHarness(t)
	pool, tr := tracedSeamPool(t)

	ran := false
	if err := db.WithinRequestTenantTx(requestCtx("", h.tenantA), pool, func(pgx.Tx) error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("an empty subject must proceed, got %v", err)
	}
	if !ran {
		t.Error("the closure never ran for an empty subject")
	}

	stmts := tr.allStmts()
	if countContaining(stmts, "set_config") == 0 {
		t.Fatalf("the tracer saw no set_config at all - it is not observing the seam: %q", stmts)
	}
	if n := countContaining(stmts, "memberships"); n != 0 {
		t.Errorf("the seam issued %d memberships statement(s) for an empty subject, want 0: %q", n, stmts)
	}
}

// TestRLS_RequestSeamRefusesACallerWhoseRowWasDeleted (AUDIT-12): removing a member
// now refuses exactly like suspending one. Was
// TestRLS_RequestSeamStillAdmitsACallerWhoseRowWasDeleted under D-6/D-17's NARROW rule.
func TestRLS_RequestSeamRefusesACallerWhoseRowWasDeleted(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	userID := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, userID, "admin", "active")

	if err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantA), h.app, func(pgx.Tx) error {
		return nil
	}); err != nil {
		t.Fatalf("before deletion: %v", err)
	}

	tag, err := h.super.Exec(ctx, `DELETE FROM memberships WHERE tenant_id = $1 AND user_id = $2`, h.tenantA, userID)
	if err != nil {
		t.Fatalf("delete membership: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("deleted %d row(s), want 1 - the fixture never landed", tag.RowsAffected())
	}

	ran := false
	if err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantA), h.app, func(pgx.Tx) error {
		ran = true
		return nil
	}); !errors.Is(err, db.ErrNotActiveMember) {
		t.Fatalf("after deletion: a caller with no row must be refused, got %v", err)
	}
	if ran {
		t.Error("the closure ran for a removed member")
	}
}

// TestRLS_RequestSeamRefusesAfterALiveSuspension (Core AC 4/5, D-2): status is read fresh
// every request, so a suspension bites the very next call - no cache to flush, no new
// JWT. TestRLS_RequestSeamAdmitsAReactivatedCaller is the other direction.
func TestRLS_RequestSeamRefusesAfterALiveSuspension(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	userID := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, userID, "admin", "active")

	if err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantA), h.app, func(pgx.Tx) error {
		return nil
	}); err != nil {
		t.Fatalf("before suspension: %v", err)
	}

	tag, err := h.super.Exec(ctx,
		`UPDATE memberships SET status = 'suspended' WHERE tenant_id = $1 AND user_id = $2`, h.tenantA, userID)
	if err != nil {
		t.Fatalf("suspend membership: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("suspended %d row(s), want 1 - the fixture never landed", tag.RowsAffected())
	}

	ran := false
	if err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantA), h.app, func(pgx.Tx) error {
		ran = true
		return nil
	}); !errors.Is(err, db.ErrNotActiveMember) {
		t.Fatalf("after suspension: err = %v, want db.ErrNotActiveMember", err)
	}
	if ran {
		t.Error("the closure ran for a caller suspended since their last request")
	}
}

// TestRLS_RequestSeamDoesNotSeeAnotherTenantsMembershipRow (AC-2, AUDIT-12): the lookup
// carries no tenant_id - RLS supplies it. A row owned by another tenant must be
// invisible, so a caller suspended elsewhere is refused here on the no-row rule, not
// admitted on a leaked row. The claim strengthens from admitted to refused under the
// strict rule; the name still fits.
func TestRLS_RequestSeamDoesNotSeeAnotherTenantsMembershipRow(t *testing.T) {
	h := requireHarness(t)

	userID := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantB, userID, "admin", "suspended")

	// Control: the row does refuse in the tenant that owns it. Without this the
	// refusal below could pass on a seed that never landed.
	if err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantB), h.app, func(pgx.Tx) error {
		return nil
	}); !errors.Is(err, db.ErrNotActiveMember) {
		t.Fatalf("acting as the owning tenant: err = %v, want db.ErrNotActiveMember", err)
	}

	ran := false
	if err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantA), h.app, func(pgx.Tx) error {
		ran = true
		return nil
	}); !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("acting as a tenant the caller has no row in: err = %v, want db.ErrNotActiveMember", err)
	}
	if ran {
		t.Error("the closure ran in the tenant the caller has no row in -- B's suspended row must not leak in as an admission either")
	}
}

// TestRLS_RequestSeamIssuesNoStatementForAMalformedRequest: identity, tenant uuid and
// subject uuid are all resolved before BeginTx, so a malformed request opens no
// transaction at all. Red if BeginTx is hoisted above the checks.
func TestRLS_RequestSeamIssuesNoStatementForAMalformedRequest(t *testing.T) {
	h := requireHarness(t)
	pool, tr := tracedSeamPool(t)

	if err := db.WithinRequestTenantTx(context.Background(), pool, func(pgx.Tx) error {
		return nil
	}); !errors.Is(err, db.ErrNoTenant) {
		t.Fatalf("no identity: err = %v, want db.ErrNoTenant", err)
	}
	if err := db.WithinRequestTenantTx(requestCtx(uuid.NewString(), "not-a-uuid"), pool, func(pgx.Tx) error {
		return nil
	}); !errors.Is(err, db.ErrNoTenant) {
		t.Fatalf("malformed tenant id: err = %v, want db.ErrNoTenant", err)
	}
	if got := tr.allStmts(); len(got) != 0 {
		t.Errorf("a malformed request issued %d statement(s), want 0: %q", len(got), got)
	}

	// Control: the same pool and tracer DO record statements for a well-formed
	// request, so the zero above is an observation and not a blind spot.
	userID := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, userID, "admin", "active")
	if err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantA), pool, func(pgx.Tx) error {
		return nil
	}); err != nil {
		t.Fatalf("control call: %v", err)
	}
	if countContaining(tr.allStmts(), "memberships") == 0 {
		t.Fatal("the tracer recorded no memberships statement even for a well-formed request - it is not attached")
	}
}

// TestRLS_RequestSeamRefusesACallerActiveOnlyInAnotherTenant (AUDIT-12): active in A,
// acting as B, is refused -- A's row is invisible under RLS, and B has none.
func TestRLS_RequestSeamRefusesACallerActiveOnlyInAnotherTenant(t *testing.T) {
	h := requireHarness(t)

	userID := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, userID, "admin", "active")

	ran := false
	if err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantB), h.app, func(pgx.Tx) error {
		ran = true
		return nil
	}); !errors.Is(err, db.ErrNotActiveMember) {
		t.Fatalf("acting as tenant B (active only in A): err = %v, want db.ErrNotActiveMember", err)
	}
	if ran {
		t.Error("the closure ran for a caller active only in another tenant")
	}

	// Control: the same caller IS admitted in the tenant they are actually active in.
	ranA := false
	if err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantA), h.app, func(pgx.Tx) error {
		ranA = true
		return nil
	}); err != nil {
		t.Errorf("acting as tenant A (active there): unexpected error %v", err)
	}
	if !ranA {
		t.Error("the closure never ran in the tenant the caller is active in")
	}
}

// TestRLS_RequestSeamRefusalIsIdenticalForSuspendedAndForNoRow (Core AC 2): the two
// refusal paths are indistinguishable -- both wrap ErrNotActiveMember AND their error
// strings are byte-equal, so nothing downstream can tell a no-row caller from a
// suspended one.
func TestRLS_RequestSeamRefusalIsIdenticalForSuspendedAndForNoRow(t *testing.T) {
	h := requireHarness(t)

	suspendedID := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, suspendedID, "admin", "suspended")
	noRowID := uuid.NewString() // never seeded

	suspendedErr := db.WithinRequestTenantTx(requestCtx(suspendedID, h.tenantA), h.app, func(pgx.Tx) error { return nil })
	noRowErr := db.WithinRequestTenantTx(requestCtx(noRowID, h.tenantA), h.app, func(pgx.Tx) error { return nil })

	if !errors.Is(suspendedErr, db.ErrNotActiveMember) {
		t.Fatalf("suspended caller err = %v, want db.ErrNotActiveMember", suspendedErr)
	}
	if !errors.Is(noRowErr, db.ErrNotActiveMember) {
		t.Fatalf("no-row caller err = %v, want db.ErrNotActiveMember", noRowErr)
	}
	if suspendedErr.Error() != noRowErr.Error() {
		t.Errorf("error strings differ: suspended=%q no-row=%q, want byte-identical", suspendedErr.Error(), noRowErr.Error())
	}
}

// rsMember seeds an active member of tenant as the superuser: its all_clients flag and
// its membership_clients rows. It returns the user id, the subject of the request identity.
func rsMember(t *testing.T, tenant, role string, allClients bool, clients ...*csClient) string {
	t.Helper()
	ctx := context.Background()
	userID := uuid.NewString()
	seedMembershipWithStatus(t, tenant, userID, role, "active")
	if allClients {
		if _, err := h.super.Exec(ctx,
			`UPDATE memberships SET all_clients = true WHERE tenant_id = $1 AND user_id = $2`, tenant, userID); err != nil {
			t.Fatalf("seed all_clients: %v", err)
		}
	}
	for _, c := range clients {
		if _, err := h.super.Exec(ctx,
			`INSERT INTO membership_clients (tenant_id, user_id, entity_id) VALUES ($1, $2, $3)`,
			tenant, userID, c.entity); err != nil {
			t.Fatalf("seed membership_clients: %v", err)
		}
	}
	// Control: the row the seam will read is the row this test meant to seed.
	var gotFlag bool
	var nRows int
	if err := h.super.QueryRow(ctx,
		`SELECT m.all_clients, (SELECT count(*) FROM membership_clients c WHERE c.tenant_id = m.tenant_id AND c.user_id = m.user_id)
		   FROM memberships m WHERE m.tenant_id = $1 AND m.user_id = $2`, tenant, userID).Scan(&gotFlag, &nRows); err != nil {
		t.Fatalf("read back the seeded member: %v", err)
	}
	if gotFlag != allClients || nRows != len(clients) {
		t.Fatalf("seeded member has all_clients=%v and %d client rows, want %v and %d", gotFlag, nRows, allClients, len(clients))
	}
	return userID
}

// rsInHouse turns a seeded throwaway tenant into an in-house one.
func rsInHouse(t *testing.T, ten *csTenant) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(), `UPDATE tenants SET kind = 'in_house' WHERE id = $1`, ten.id); err != nil {
		t.Fatalf("make tenant in_house: %v", err)
	}
}

// rsSeamScope returns app.entity_scope as the seam's closure sees it.
func rsSeamScope(t *testing.T, pool *pgxpool.Pool, userID, tenant string) string {
	t.Helper()
	var got string
	if err := db.WithinRequestTenantTx(requestCtx(userID, tenant), pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT coalesce(current_setting('app.entity_scope', true), '<null>')`).Scan(&got)
	}); err != nil {
		t.Fatalf("seam read of app.entity_scope: %v", err)
	}
	return got
}

// rsSeamIDs returns the sorted ids tb shows to the member through the request seam.
func rsSeamIDs(t *testing.T, userID, tenant string, tb csTable) []string {
	t.Helper()
	var got []string
	if err := db.WithinRequestTenantTx(requestCtx(userID, tenant), h.app, func(tx pgx.Tx) error {
		ids, err := csQueryIDs(tx, tb.name)
		got = ids
		return err
	}); err != nil {
		t.Fatalf("seam read of %s: %v", tb.name, err)
	}
	return got
}

// rsExpectSeamIDs asserts the member reads exactly want from tb; want must be non-empty.
func rsExpectSeamIDs(t *testing.T, userID, tenant string, tb csTable, want []string) {
	t.Helper()
	if len(want) == 0 {
		t.Fatalf("%s: no expected rows, the assertion would be vacuous", tb.name)
	}
	if got := rsSeamIDs(t, userID, tenant, tb); !slices.Equal(got, want) {
		t.Errorf("%s through the seam: ids = %v, want %v", tb.name, got, want)
	}
}

// TestRLS_RequestSeamWritesTheEntityScopeInTheGateStatement: the scope rides statement 2 of
// the one gate batch; no standalone set_config carries it.
func TestRLS_RequestSeamWritesTheEntityScopeInTheGateStatement(t *testing.T) {
	h := requireHarness(t)
	pool, tr := tracedSeamPool(t)

	userID := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, userID, "admin", "active")

	if err := db.WithinRequestTenantTx(requestCtx(userID, h.tenantA), pool, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("WithinRequestTenantTx: %v", err)
	}

	starts := tr.starts()
	if len(starts) != 1 || len(starts[0]) != 2 {
		t.Fatalf("want exactly one batch of two statements, got %q", starts)
	}
	if !strings.Contains(starts[0][0], "app.current_tenant") {
		t.Errorf("batch statement 1 = %q, want the app.current_tenant set_config", starts[0][0])
	}
	if !strings.Contains(starts[0][1], "app.entity_scope") {
		t.Errorf("batch statement 2 = %q, want it to write app.entity_scope", starts[0][1])
	}
	if strings.Contains(starts[0][0], "app.entity_scope") {
		t.Errorf("batch statement 1 = %q writes app.entity_scope before the membership is read", starts[0][0])
	}

	standalone := tr.standaloneStmts()
	if len(standalone) == 0 {
		t.Fatal("the tracer recorded no standalone statement, so it is not attached")
	}
	if n := countContaining(standalone, "set_config"); n != 0 {
		t.Errorf("the seam sent %d standalone set_config statement(s): %q", n, standalone)
	}
	if n := countContaining(standalone, "entity_scope"); n != 0 {
		t.Errorf("the seam sent %d standalone statement(s) naming entity_scope: %q", n, standalone)
	}
}

// TestRLS_RequestSeamScopeIsStarForAdminAllClientsAndInHouse: the seam writes '*' for an
// admin that stores all_clients = false, for an "All clients" firm member and for an in-house member.
func TestRLS_RequestSeamScopeIsStarForAdminAllClientsAndInHouse(t *testing.T) {
	requireHarness(t)
	firm := csSeedClients(t, 2)
	inHouse := csSeedClients(t, 2)
	rsInHouse(t, inHouse)

	cases := []struct {
		name, tenant, kind, user string
	}{
		{"firm admin, flag false, no rows", firm.id, "firm", rsMember(t, firm.id, "admin", false)},
		{"firm preparer with All clients", firm.id, "firm", rsMember(t, firm.id, "preparer", true)},
		{"in-house preparer, flag false, no rows", inHouse.id, "in_house", rsMember(t, inHouse.id, "preparer", false)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var kind string
			if err := h.super.QueryRow(context.Background(), `SELECT kind FROM tenants WHERE id = $1`, c.tenant).Scan(&kind); err != nil || kind != c.kind {
				t.Fatalf("tenant kind = %q (err %v), want %q", kind, err, c.kind)
			}
			if got := rsSeamScope(t, h.app, c.user, c.tenant); got != "*" {
				t.Errorf("app.entity_scope in the seam = %q, want %q", got, "*")
			}
		})
	}
}

// TestRLS_RequestSeamScopeIsTheMembersSet: a limited firm member gets its client ids in
// sorted order, whatever order the rows were inserted in.
func TestRLS_RequestSeamScopeIsTheMembersSet(t *testing.T) {
	requireHarness(t)
	ten := csSeedClients(t, 2)
	lo, hi := ten.clients[0], ten.clients[1]
	if lo.entity > hi.entity {
		lo, hi = hi, lo
	}
	user := rsMember(t, ten.id, "reviewer", false, hi, lo) // hi inserted first

	want := "{" + lo.entity + "," + hi.entity + "}"
	if got := rsSeamScope(t, h.app, user, ten.id); got != want {
		t.Errorf("app.entity_scope in the seam = %q, want %q", got, want)
	}
}

// TestRLS_RequestSeamScopeIsEmptyForAnUnassignedMember: a firm member with the flag off and
// no rows gets '{}', which matches no client.
func TestRLS_RequestSeamScopeIsEmptyForAnUnassignedMember(t *testing.T) {
	requireHarness(t)
	ten := csSeedClients(t, 2)
	user := rsMember(t, ten.id, "preparer", false)

	if got := rsSeamScope(t, h.app, user, ten.id); got != "{}" {
		t.Errorf("app.entity_scope in the seam = %q, want %q", got, "{}")
	}
}

// TestRLS_RequestSeamUnassignedMemberReadsNoClient: through the seam a member with no
// assignment reads no row of the six tables, in a tenant that holds two clients' rows.
func TestRLS_RequestSeamUnassignedMemberReadsNoClient(t *testing.T) {
	requireHarness(t)
	ten := csSeedClients(t, 2)
	a, b := ten.clients[0], ten.clients[1]
	admin := rsMember(t, ten.id, "admin", false)
	unassigned := rsMember(t, ten.id, "preparer", false)

	for _, tb := range csTables {
		t.Run(tb.name, func(t *testing.T) {
			// Control: the same seam shows this tenant's rows to an admin.
			rsExpectSeamIDs(t, admin, ten.id, tb, csWant(tb, a, b))
			if got := rsSeamIDs(t, unassigned, ten.id, tb); len(got) != 0 {
				t.Errorf("%s: an unassigned member reads %v, want no row", tb.name, got)
			}
		})
	}
}

// TestRLS_RequestSeamLimitedMemberReadsOnlyItsClients: a reviewer holding A reads A's rows
// and none of B's on every table; line items and history follow A's invoice.
func TestRLS_RequestSeamLimitedMemberReadsOnlyItsClients(t *testing.T) {
	requireHarness(t)
	ten := csSeed(t, [2]int{2, 2}, [2]int{3, 1})
	a, b := ten.clients[0], ten.clients[1]
	reviewer := rsMember(t, ten.id, "reviewer", false, a)

	for _, tb := range csTables {
		t.Run(tb.name, func(t *testing.T) {
			if len(csWant(tb, b)) == 0 {
				t.Fatalf("%s: client B has no rows, so excluding them proves nothing", tb.name)
			}
			rsExpectSeamIDs(t, reviewer, ten.id, tb, csWant(tb, a))
		})
	}
}

// TestRLS_RequestSeamFullAccessMembersReadEveryClient: an admin, an "All clients" member and
// an in-house member each read both clients' rows on every table.
func TestRLS_RequestSeamFullAccessMembersReadEveryClient(t *testing.T) {
	requireHarness(t)
	firm := csSeedClients(t, 2)
	inHouse := csSeedClients(t, 2)
	rsInHouse(t, inHouse)

	cases := []struct {
		name string
		ten  *csTenant
		user string
	}{
		{"admin", firm, rsMember(t, firm.id, "admin", false)},
		{"All clients preparer", firm, rsMember(t, firm.id, "preparer", true)},
		{"in-house preparer", inHouse, rsMember(t, inHouse.id, "preparer", false)},
	}
	for _, c := range cases {
		for _, tb := range csTables {
			t.Run(c.name+"/"+tb.name, func(t *testing.T) {
				rsExpectSeamIDs(t, c.user, c.ten.id, tb, csWant(tb, c.ten.clients...))
			})
		}
	}
}

// TestRLS_WorkerCoreReadsEveryClient: WithinTenantTx ignores the identity in ctx, so the
// unassigned member's identity still reads every client.
func TestRLS_WorkerCoreReadsEveryClient(t *testing.T) {
	requireHarness(t)
	ten := csSeedClients(t, 2)
	unassigned := rsMember(t, ten.id, "preparer", false)

	for _, tb := range csTables {
		t.Run(tb.name, func(t *testing.T) {
			want := csWant(tb, ten.clients...)
			if len(want) == 0 {
				t.Fatalf("%s: no expected rows", tb.name)
			}
			var got []string
			if err := db.WithinTenantTx(requestCtx(unassigned, ten.id), h.app, ten.id, func(tx pgx.Tx) error {
				ids, err := csQueryIDs(tx, tb.name)
				got = ids
				return err
			}); err != nil {
				t.Fatalf("WithinTenantTx: %v", err)
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s through the worker core: ids = %v, want %v", tb.name, got, want)
			}
		})
	}
}

// TestRLS_InProcessActorReadsEveryClient: the seam's non-UUID-subject arm writes no scope, so
// an in-process actor reads every client, and it issues no memberships statement.
func TestRLS_InProcessActorReadsEveryClient(t *testing.T) {
	requireHarness(t)
	pool, tr := tracedSeamPool(t)
	ten := csSeedClients(t, 2)

	for _, tb := range csTables {
		want := csWant(tb, ten.clients...)
		if len(want) == 0 {
			t.Fatalf("%s: no expected rows", tb.name)
		}
		var got []string
		if err := db.WithinRequestTenantTx(requestCtx("extraction-worker", ten.id), pool, func(tx pgx.Tx) error {
			ids, err := csQueryIDs(tx, tb.name)
			got = ids
			return err
		}); err != nil {
			t.Fatalf("WithinRequestTenantTx as extraction-worker: %v", err)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s as an in-process actor: ids = %v, want %v", tb.name, got, want)
		}
	}

	stmts := tr.allStmts()
	if countContaining(stmts, "set_config") == 0 {
		t.Fatalf("the tracer saw no set_config, so it is not observing the seam: %q", stmts)
	}
	if n := countContaining(stmts, "memberships"); n != 0 {
		t.Errorf("the in-process arm issued %d memberships statement(s), want 0: %q", n, stmts)
	}
}

// TestRLS_EntityScopeDoesNotReachTheNextBorrower: on a one-connection pool, a seam
// transaction that set '{}' leaves nothing behind for the next WithinTenantTx.
func TestRLS_EntityScopeDoesNotReachTheNextBorrower(t *testing.T) {
	h := requireHarness(t)
	cfg, err := pgxpool.ParseConfig(h.appURL)
	if err != nil {
		t.Fatalf("parse app url: %v", err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new one-connection pool: %v", err)
	}
	t.Cleanup(pool.Close)

	ten := csSeedClients(t, 2)
	unassigned := rsMember(t, ten.id, "preparer", false)
	ctx := context.Background()

	var seamPID int
	var seamScope string
	var seamEntities []string
	if err := db.WithinRequestTenantTx(requestCtx(unassigned, ten.id), pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT pg_backend_pid(), coalesce(current_setting('app.entity_scope', true), '<null>')`).Scan(&seamPID, &seamScope); err != nil {
			return err
		}
		ids, err := csQueryIDs(tx, "business_entities")
		seamEntities = ids
		return err
	}); err != nil {
		t.Fatalf("seam transaction: %v", err)
	}
	// Precondition: the first borrower really ran with the empty set, or the read below proves nothing.
	if seamScope != "{}" || len(seamEntities) != 0 {
		t.Fatalf("the seam transaction ran with scope %q and read %v entities, want scope {} and none", seamScope, seamEntities)
	}

	for _, tb := range []csTable{csTables[0], csTables[1]} {
		var pid int
		var got []string
		if err := db.WithinTenantTx(ctx, pool, ten.id, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				return err
			}
			ids, err := csQueryIDs(tx, tb.name)
			got = ids
			return err
		}); err != nil {
			t.Fatalf("WithinTenantTx after the seam: %v", err)
		}
		if pid != seamPID {
			t.Fatalf("the second transaction ran on backend %d, the seam on %d: not the same connection", pid, seamPID)
		}
		if want := csWant(tb, ten.clients...); !slices.Equal(got, want) {
			t.Errorf("%s in the next borrower's transaction: ids = %v, want every client's %v", tb.name, got, want)
		}
	}
}
