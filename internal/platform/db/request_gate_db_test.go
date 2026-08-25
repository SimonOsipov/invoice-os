// AUDIT-10-01: the request-seam membership gate. WithinRequestTenantTxOpts refuses a
// caller whose memberships row in the current tenant exists and is not 'active'
// (ErrNotActiveMember); no row at all still proceeds (D-17, NARROW).
//
// Named TestRLS_ so ci.yml:331's `-run TestRLS` step reaches every case here —
// ci_filter_coverage_test.go fails any name no filter reaches.
package db_test

import (
	"context"
	"errors"
	"os/exec"
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

// TestRLS_RequestSeamAllowsACallerWithNoMembershipRow (AC-3) pins D-6/D-17's NARROW
// rule: no row is NOT a refusal. AUDIT-12 owns the strict flip, and flipping it must
// show up as a change to this test.
func TestRLS_RequestSeamAllowsACallerWithNoMembershipRow(t *testing.T) {
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
	}); err != nil {
		t.Fatalf("WithinRequestTenantTx: a caller with no membership row must proceed, got %v", err)
	}
	if !ran {
		t.Error("the closure never ran for a caller with no membership row")
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
