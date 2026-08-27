// APPR-08-03 (task-497): the transmit gate inside Store.Transition. The permissive
// cases are controls that pin what the guard must NOT touch. Fixtures come from
// apply_validation_arming_test.go, the harness from store_test.go /
// transition_adversarial_test.go (same package).
//
// Run: DATABASE_URL=… DATABASE_SUPERUSER_URL=… go test -p 1 -count=1 ./internal/invoice/...
// (`make test-rls` runs internal/platform/db only; CI's rls job runs this package
// whole via scripts/ci/rls-test-gate.sh).
package invoice

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- harness ---------------------------------------------------------------

// gateFixture is one tenant's worth of gate inputs: an invoice at a chosen status
// and, unless the spec says otherwise, an ACTIVE one-step policy version.
type gateFixture struct {
	tenantID  string
	entityID  string
	versionID string
	invID     string
	ctx       context.Context
}

// seedGatedTenant seeds a tenant whose policy version is active — the only shape in
// which TransmitClearTx reaches its SQL path.
func seedGatedTenant(t *testing.T, super *pgxpool.Pool, label string, status Status) gateFixture {
	t.Helper()
	tenantID, entityID, versionID := seedOneStepActivePolicyTenant(t, super, label)
	return gateFixture{
		tenantID:  tenantID,
		entityID:  entityID,
		versionID: versionID,
		invID:     seedInvoiceAtStatus(t, super, tenantID, entityID, label, status),
		ctx:       gateCtx(tenantID),
	}
}

func gateCtx(tenantID string) context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{
		Subject: memberSubject, Role: "authenticated", TenantID: tenantID,
	})
}

// statusOf reads invoices.status out of band, as the superuser.
func statusOf(t *testing.T, super *pgxpool.Pool, invoiceID string) Status {
	t.Helper()
	var s string
	if err := super.QueryRow(context.Background(),
		`SELECT status FROM invoices WHERE id = $1`, invoiceID).Scan(&s); err != nil {
		t.Fatalf("read back status of %s: %v", invoiceID, err)
	}
	return Status(s)
}

// runStateOf reads approval_runs.state out of band, as the superuser.
func runStateOf(t *testing.T, super *pgxpool.Pool, runID string) string {
	t.Helper()
	var s string
	if err := super.QueryRow(context.Background(),
		`SELECT state FROM approval_runs WHERE id = $1`, runID).Scan(&s); err != nil {
		t.Fatalf("read back state of run %s: %v", runID, err)
	}
	return s
}

// sqlRecorder records the SQL of every statement its pool issues, so "no approval
// statement was issued" is assertable. Ported from internal/approval/
// workflow_roles_test.go, unexported and unreachable from this package.
//
// The request seam batches set_config + its membership read; pgx routes those
// through BatchTracer. Recorded apart so mentioning() keeps meaning "the store's
// own SQL".
type sqlRecorder struct {
	mu      sync.Mutex
	sql     []string
	batched []string
}

var (
	_ pgx.QueryTracer = (*sqlRecorder)(nil)
	_ pgx.BatchTracer = (*sqlRecorder)(nil)
)

func (r *sqlRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	r.sql = append(r.sql, d.SQL)
	r.mu.Unlock()
	return ctx
}

func (r *sqlRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (r *sqlRecorder) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return ctx
}

func (r *sqlRecorder) TraceBatchQuery(_ context.Context, _ *pgx.Conn, d pgx.TraceBatchQueryData) {
	r.mu.Lock()
	r.batched = append(r.batched, d.SQL)
	r.mu.Unlock()
}

func (r *sqlRecorder) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}

func (r *sqlRecorder) reset() {
	r.mu.Lock()
	r.sql = nil
	r.batched = nil
	r.mu.Unlock()
}

func (r *sqlRecorder) mentioning(substr string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return filterContaining(r.sql, substr)
}

// seamMentioning scans the batched statements only — the request seam's gate.
func (r *sqlRecorder) seamMentioning(substr string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return filterContaining(r.batched, substr)
}

// tenantTxCount counts set_config('app.current_tenant') across both slices: the
// request seam batches that statement, WithinTenantTx execs it plainly, and either
// way there is exactly one per transaction.
func (r *sqlRecorder) tenantTxCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	const needle = "set_config('app.current_tenant'"
	return len(filterContaining(r.sql, needle)) + len(filterContaining(r.batched, needle))
}

func filterContaining(stmts []string, substr string) []string {
	var out []string
	for _, s := range stmts {
		if strings.Contains(s, substr) {
			out = append(out, s)
		}
	}
	return out
}

// tracedAppPool is a second app-role pool whose statements are recorded. Callers
// must already have gone through dbTestPools, which owns the skip gate.
func tracedAppPool(t *testing.T) (*pgxpool.Pool, *sqlRecorder) {
	t.Helper()
	rec := &sqlRecorder{}
	return appPoolWithTracer(t, rec), rec
}

// --- AC-3/AC-7: an open run refuses the move into queued ---------------------

// TestTransition_QueuedRefusedWhenAwaitingApproval: flag ON, active policy, an open
// run -> ErrAwaitingApproval, and the whole tx rolls back (status, history and
// audit all unchanged).
func TestTransition_QueuedRefusedWhenAwaitingApproval(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-03-GATED", StatusValidated)
	seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID) // defaults to open

	store := NewStore(app, WithApprovalsEnforced(true))

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, fx.invID)
	beforeAudit := auditCount(t, app, fx.tenantID, "invoice.transitioned")

	_, err := store.Transition(fx.ctx, fx.invID, StatusQueued)
	if !errors.Is(err, ErrAwaitingApproval) {
		t.Fatalf("Transition(validated -> queued) with an open run: err = %v, want ErrAwaitingApproval", err)
	}
	if got := statusOf(t, super, fx.invID); got != StatusValidated {
		t.Errorf("invoice status = %q, want unchanged %q", got, StatusValidated)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, fx.invID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
	}
	if n := auditCount(t, app, fx.tenantID, "invoice.transitioned"); n != beforeAudit {
		t.Errorf("invoice.transitioned audit rows = %d, want unchanged %d", n, beforeAudit)
	}
}

// TestTransition_QueuedRefusedWhenValidatedWithNoRun: an invoice under an active
// policy with NO run at all is not clear either — TransmitClear fails closed on an
// absent answer, which is what the seeded backlog looks like.
func TestTransition_QueuedRefusedWhenValidatedWithNoRun(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-03-NORUN", StatusValidated)
	store := NewStore(app, WithApprovalsEnforced(true))

	_, err := store.Transition(fx.ctx, fx.invID, StatusQueued)
	if !errors.Is(err, ErrAwaitingApproval) {
		t.Fatalf("Transition(validated -> queued) with no run: err = %v, want ErrAwaitingApproval", err)
	}
	if got := statusOf(t, super, fx.invID); got != StatusValidated {
		t.Errorf("invoice status = %q, want unchanged %q", got, StatusValidated)
	}
}

// TestTransition_UppercaseIdOnAGatedInvoiceStillRefuses: normalising the id must not
// become a bypass — an uppercase uuid on a gated invoice is still refused. Fails
// today. Its permissive twin is
// TestTransition_UppercaseIdOnAnApprovedInvoiceReachesQueued.
func TestTransition_UppercaseIdOnAGatedInvoiceStillRefuses(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-03-UPPER-GATED", StatusValidated)
	seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)

	store := NewStore(app, WithApprovalsEnforced(true))

	upper := strings.ToUpper(fx.invID)
	if upper == fx.invID {
		t.Fatalf("fixture id %q has no lowercase hex digits — the case this test exists for is not exercised", fx.invID)
	}

	_, err := store.Transition(fx.ctx, upper, StatusQueued)
	if !errors.Is(err, ErrAwaitingApproval) {
		t.Fatalf("Transition(UPPERCASE id -> queued) with an open run: err = %v, want ErrAwaitingApproval", err)
	}
	if got := statusOf(t, super, fx.invID); got != StatusValidated {
		t.Errorf("invoice status = %q, want unchanged %q", got, StatusValidated)
	}
}

// --- the permissive controls: what the guard must NOT bite ------------------

// TestTransition_QueuedAllowedWhenRunApproved is the positive half of
// TestTransition_QueuedRefusedWhenAwaitingApproval: an approved run clears the door.
// A gate that always refuses cannot pass both.
func TestTransition_QueuedAllowedWhenRunApproved(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-03-APPROVED", StatusValidated)
	runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
	closeApprovalRunFor(t, super, runID, "approved", "fixture")

	store := NewStore(app, WithApprovalsEnforced(true))

	got, err := store.Transition(fx.ctx, fx.invID, StatusQueued)
	if err != nil {
		t.Fatalf("Transition(validated -> queued) with an approved run: %v (want nil)", err)
	}
	if got.Status != StatusQueued {
		t.Errorf("returned status = %q, want %q", got.Status, StatusQueued)
	}
	if s := statusOf(t, super, fx.invID); s != StatusQueued {
		t.Errorf("stored status = %q, want %q", s, StatusQueued)
	}
}

// TestTransition_UppercaseIdOnAnApprovedInvoiceReachesQueued (AC #9): the id reaching
// Store.Transition is r.PathValue("id") verbatim, and TransmitClearTx keys its SQL-path
// map by Postgres's canonical lowercase. Keying the gate on the caller's string
// instead of the LOCKED row's id refuses an approved invoice.
func TestTransition_UppercaseIdOnAnApprovedInvoiceReachesQueued(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-03-UPPER-OK", StatusValidated)
	runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
	closeApprovalRunFor(t, super, runID, "approved", "fixture")

	store := NewStore(app, WithApprovalsEnforced(true))

	upper := strings.ToUpper(fx.invID)
	if upper == fx.invID {
		t.Fatalf("fixture id %q has no lowercase hex digits — the case this test exists for is not exercised", fx.invID)
	}

	if _, err := store.Transition(fx.ctx, upper, StatusQueued); err != nil {
		t.Fatalf("Transition(UPPERCASE id -> queued) with an approved run: %v (want nil)", err)
	}
	if s := statusOf(t, super, fx.invID); s != StatusQueued {
		t.Errorf("stored status = %q, want %q", s, StatusQueued)
	}
}

// TestTransition_QueuedAllowedWhenNoActivePolicy: a tenant that has published no
// policy is clear even with an open run — TransmitClearTx short-circuits on
// is_active. The version below is seeded and left INACTIVE.
func TestTransition_QueuedAllowedWhenNoActivePolicy(t *testing.T) {
	super, app := dbTestPools(t)

	const label = "APPR-08-03-NOPOLICY"
	tenantID := seedTenant(t, super, label+" tenant")
	entityID := seedEntity(t, super, tenantID, label+" entity")
	policyID := seedApprovalPolicyFor(t, super, tenantID, label+" policy")
	versionID := seedApprovalPolicyVersionFor(t, super, tenantID, policyID)
	seedApprovalStepFor(t, super, tenantID, versionID, approvalStepSpecFor{
		Ord: 0, Kind: "approval", WorkflowRoleKey: strPtr("finance-lead"),
	})
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, label, StatusValidated)
	seedApprovalRunFor(t, super, tenantID, invID, versionID)

	store := NewStore(app, WithApprovalsEnforced(true))

	if _, err := store.Transition(gateCtx(tenantID), invID, StatusQueued); err != nil {
		t.Fatalf("Transition(validated -> queued) with no ACTIVE policy version: %v (want nil)", err)
	}
	if s := statusOf(t, super, invID); s != StatusQueued {
		t.Errorf("stored status = %q, want %q", s, StatusQueued)
	}
}

// TestTransition_FlagOffLeavesQueuedUnchanged (AC #4): with the flag off the store
// must be byte-for-byte the store it was — not merely permissive, but silent. The
// traced pool proves no approval statement is issued at all.
func TestTransition_FlagOffLeavesQueuedUnchanged(t *testing.T) {
	super, _ := dbTestPools(t)
	tracedApp, rec := tracedAppPool(t)

	fx := seedGatedTenant(t, super, "APPR-08-03-FLAGOFF", StatusValidated)
	runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)

	store := NewStore(tracedApp) // flag OFF

	rec.reset()
	if _, err := store.Transition(fx.ctx, fx.invID, StatusQueued); err != nil {
		t.Fatalf("Transition(validated -> queued) with the flag off: %v (want nil)", err)
	}
	if s := statusOf(t, super, fx.invID); s != StatusQueued {
		t.Errorf("stored status = %q, want %q", s, StatusQueued)
	}
	if got := rec.mentioning("approval_runs"); len(got) != 0 {
		t.Errorf("flag-off Transition issued %d statement(s) mentioning approval_runs: %v", len(got), got)
	}
	if got := rec.mentioning("approval_policy_versions"); len(got) != 0 {
		t.Errorf("flag-off Transition issued %d statement(s) mentioning approval_policy_versions: %v", len(got), got)
	}
	if s := runStateOf(t, super, runID); s != "open" {
		t.Errorf("run state = %q, want unchanged %q", s, "open")
	}
}

// TestTransition_GateOnlyBitesTargetQueued (AC #5/#6): validated -> draft still
// succeeds under a gated invoice and still cancels the live run. A guard that
// forgot its target == queued conjunct fails here.
func TestTransition_GateOnlyBitesTargetQueued(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-03-TODRAFT", StatusValidated)
	runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)

	store := NewStore(app, WithApprovalsEnforced(true))

	if _, err := store.Transition(fx.ctx, fx.invID, StatusDraft); err != nil {
		t.Fatalf("Transition(validated -> draft) under the flag: %v (want nil)", err)
	}
	if s := statusOf(t, super, fx.invID); s != StatusDraft {
		t.Errorf("stored status = %q, want %q", s, StatusDraft)
	}
	if s := runStateOf(t, super, runID); s != "cancelled" {
		t.Errorf("run state after -> draft = %q, want %q", s, "cancelled")
	}
}

// TestTransition_RedundantTransitionStillPrecedesTheGate ([D4]): redundancy is
// checked before legality AND before the gate, so an already-queued invoice reads
// ErrRedundantTransition even under an open run.
func TestTransition_RedundantTransitionStillPrecedesTheGate(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-03-REDUNDANT", StatusQueued)
	seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)

	store := NewStore(app, WithApprovalsEnforced(true))

	_, err := store.Transition(fx.ctx, fx.invID, StatusQueued)
	if errors.Is(err, ErrAwaitingApproval) {
		t.Fatalf("Transition(queued -> queued): err = ErrAwaitingApproval, want ErrRedundantTransition — the gate jumped the [D4] redundancy check")
	}
	if !errors.Is(err, ErrRedundantTransition) {
		t.Fatalf("Transition(queued -> queued): err = %v, want ErrRedundantTransition", err)
	}
}

// TestTransition_IllegalEdgeIntoQueuedStillReadsIllegal (AC #11): validated is the
// ONLY legal `from` for queued, so every other source stays ErrIllegalTransition —
// on invoices carrying an open run, which is exactly what a guard missing its
// canTransition conjunct would answer ErrAwaitingApproval for.
func TestTransition_IllegalEdgeIntoQueuedStillReadsIllegal(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app, WithApprovalsEnforced(true))

	for _, from := range []Status{StatusDraft, StatusSubmitted, StatusAccepted, StatusRejected, StatusFailed} {
		t.Run(string(from)+"->queued", func(t *testing.T) {
			label := "APPR-08-03-ILLEGAL-" + string(from)
			fx := seedGatedTenant(t, super, label, from)
			seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)

			_, err := store.Transition(fx.ctx, fx.invID, StatusQueued)
			if errors.Is(err, ErrAwaitingApproval) {
				t.Fatalf("Transition(%s -> queued): err = ErrAwaitingApproval, want ErrIllegalTransition — the guard answered for an edge that does not exist", from)
			}
			if !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("Transition(%s -> queued): err = %v, want ErrIllegalTransition", from, err)
			}
			if s := statusOf(t, super, fx.invID); s != from {
				t.Errorf("stored status = %q, want unchanged %q", s, from)
			}
		})
	}
}

// TestTransition_MalformedIdUnderTheFlagIsStillValidation: a non-uuid id raises
// 22P02 in the lock SELECT, before the gate can have an opinion.
func TestTransition_MalformedIdUnderTheFlagIsStillValidation(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-03-MALFORMED", StatusValidated)
	store := NewStore(app, WithApprovalsEnforced(true))

	_, err := store.Transition(fx.ctx, "not-a-uuid", StatusQueued)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Transition(\"not-a-uuid\" -> queued): err = %v, want ErrValidation", err)
	}
}

// --- the gate reads the fact AFTER the row lock, never before ----------------

// TestTransition_GateRunsAfterTheRowLock: a second transaction holds the invoice's
// row lock; Store.Transition is launched and VERIFIED blocked on it; the run is then
// decided and the holder released. A gate that read the run before taking the lock
// answers on a fact that was already stale.
//
// The blocked-check is the load-bearing step — without it a goroutine that has not
// yet reached the lock produces a silent false green. It asks pg_stat_activity for a
// backend blocked BY THE HOLDER specifically (pg_blocking_pids), so neither this
// test's own polling backend nor an unrelated lock wait can satisfy it.
func TestTransition_GateRunsAfterTheRowLock(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	// transitionWhileLocked holds the row lock, launches Transition, waits until it is
	// genuinely blocked, applies decide (which may close the run), then releases.
	transitionWhileLocked := func(t *testing.T, label string, decide func(runID string)) error {
		t.Helper()

		fx := seedGatedTenant(t, super, label, StatusValidated)
		runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
		store := NewStore(app, WithApprovalsEnforced(true))

		holder, err := super.Begin(ctx)
		if err != nil {
			t.Fatalf("begin holder tx: %v", err)
		}
		defer func() { _ = holder.Rollback(ctx) }()

		var holderPID int
		if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
			t.Fatalf("read holder pid: %v", err)
		}
		var locked string
		if err := holder.QueryRow(ctx, `SELECT id FROM invoices WHERE id = $1 FOR UPDATE`, fx.invID).Scan(&locked); err != nil {
			t.Fatalf("holder lock the invoice row: %v", err)
		}

		done := make(chan error, 1)
		go func() {
			_, err := store.Transition(fx.ctx, fx.invID, StatusQueued)
			done <- err
		}()

		waitBlockedOn(t, super, holderPID)
		decide(runID)
		_ = holder.Rollback(ctx)

		select {
		case err := <-done:
			if err == nil && statusOf(t, super, fx.invID) != StatusQueued {
				t.Errorf("Transition returned nil but the invoice is %q, want %q", statusOf(t, super, fx.invID), StatusQueued)
			}
			return err
		case <-time.After(15 * time.Second):
			t.Fatal("Store.Transition never returned after the row lock was released")
			return nil
		}
	}

	t.Run("run approved while blocked reaches queued", func(t *testing.T) {
		err := transitionWhileLocked(t, "APPR-08-03-LOCK-OK", func(runID string) {
			closeApprovalRunFor(t, super, runID, "approved", "fixture")
		})
		if err != nil {
			t.Fatalf("Transition after the lock released: err = %v, want nil — the gate read the run before taking the lock", err)
		}
	})

	t.Run("run left open still refuses", func(t *testing.T) {
		err := transitionWhileLocked(t, "APPR-08-03-LOCK-BLOCKED", func(string) {})
		if !errors.Is(err, ErrAwaitingApproval) {
			t.Fatalf("Transition after the lock released with the run still open: err = %v, want ErrAwaitingApproval", err)
		}
	})
}

// waitBlockedOn fails the test unless some backend blocks on blockerPID within 5s.
func waitBlockedOn(t *testing.T, super *pgxpool.Pool, blockerPID int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := super.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity
			  WHERE pid <> pg_backend_pid() AND $1 = ANY (pg_blocking_pids(pid))`, blockerPID,
		).Scan(&n); err != nil {
			t.Fatalf("poll pg_stat_activity: %v", err)
		}
		if n > 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no backend blocked on the holder (pid %d) within 5s — Store.Transition decided without ever taking the row lock, so this spec would be a false green", blockerPID)
}

// --- APPR-16-05: no live run outlives the promotion it belonged to ---------
//
// CancelLiveRunTx claims "a stale run can never outlive the promotion it
// belonged to" (engine.go:255-256). These specs prove both halves: forward
// edges out of validated never touch a live run, and every path back to
// draft cancels it.

// TestTransition_ApprovedRunSurvivesForwardWalkToAccepted: an approved run
// clears every forward edge out of validated (TransmitClear, gate.go:43) and
// is never cancelled along the way -- only the target==StatusDraft branch
// (store.go:1712) calls CancelLiveRunTx.
func TestTransition_ApprovedRunSurvivesForwardWalkToAccepted(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-16-05-FORWARD", StatusValidated)
	runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)

	// Control: the fixture must really have armed an open run before anything
	// below can be trusted.
	if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1 AND state = 'open'`, fx.invID); n != 1 {
		t.Fatalf("open approval_runs after arming = %d, want 1 -- fixture did not arm", n)
	}
	closeApprovalRunFor(t, super, runID, "approved", "fixture")

	store := NewStore(app, WithApprovalsEnforced(true))

	assertRunSurvives := func(t *testing.T, step string) {
		t.Helper()
		if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1 AND state = 'open'`, fx.invID); n != 0 {
			t.Errorf("%s: open approval_runs = %d, want 0", step, n)
		}
		if s := runStateOf(t, super, runID); s != "approved" {
			t.Errorf("%s: run %s state = %q, want unchanged %q", step, runID, s, "approved")
		}
	}

	if _, err := store.Transition(fx.ctx, fx.invID, StatusQueued); err != nil {
		t.Fatalf("Transition(validated -> queued) with an approved run: %v (want nil)", err)
	}
	assertRunSurvives(t, "validated->queued")

	if _, err := store.Transition(fx.ctx, fx.invID, StatusSubmitted); err != nil {
		t.Fatalf("Transition(queued -> submitted): %v (want nil)", err)
	}
	assertRunSurvives(t, "queued->submitted")

	if _, err := store.Transition(fx.ctx, fx.invID, StatusAccepted); err != nil {
		t.Fatalf("Transition(submitted -> accepted): %v (want nil)", err)
	}
	assertRunSurvives(t, "submitted->accepted")

	if s := statusOf(t, super, fx.invID); s != StatusAccepted {
		t.Errorf("final invoice status = %q, want %q", s, StatusAccepted)
	}
}

// TestTransition_TerminalRefusalEdgesLeaveNoOpenRun: the four terminal-refusal
// edges out of queued/submitted also never touch an approved run -- none of
// them target StatusDraft.
func TestTransition_TerminalRefusalEdgesLeaveNoOpenRun(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app, WithApprovalsEnforced(true))

	cases := []struct {
		name string
		path []Status // walked in order, starting from validated
	}{
		{"queued->rejected", []Status{StatusQueued, StatusRejected}},
		{"queued->failed", []Status{StatusQueued, StatusFailed}},
		{"submitted->rejected", []Status{StatusQueued, StatusSubmitted, StatusRejected}},
		{"submitted->failed", []Status{StatusQueued, StatusSubmitted, StatusFailed}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := seedGatedTenant(t, super, "APPR-16-05-TERM-"+tc.name, StatusValidated)
			runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
			closeApprovalRunFor(t, super, runID, "approved", "fixture")

			// Control: the fixture must really have closed an approved run before
			// the walk below can prove it survives untouched.
			if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1 AND state = 'approved'`, fx.invID); n != 1 {
				t.Fatalf("approved approval_runs before walk = %d, want 1 -- fixture did not arm", n)
			}

			for _, target := range tc.path {
				if _, err := store.Transition(fx.ctx, fx.invID, target); err != nil {
					t.Fatalf("Transition(-> %s): %v (want nil)", target, err)
				}
				if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1 AND state = 'open'`, fx.invID); n != 0 {
					t.Errorf("after -> %s: open approval_runs = %d, want 0", target, n)
				}
			}

			want := tc.path[len(tc.path)-1]
			if s := statusOf(t, super, fx.invID); s != want {
				t.Errorf("final invoice status = %q, want %q", s, want)
			}
			if s := runStateOf(t, super, runID); s != "approved" {
				t.Errorf("run %s state after reaching a terminal refusal = %q, want unchanged %q", runID, s, "approved")
			}
		})
	}
}

// TestTransition_BackToDraftPathsCancelEveryLiveRun: every path back to draft
// -- Transition, Edit's auto-demote, and DemoteRevalidatedTx -- cancels EVERY
// live run, not just an open one (CancelLiveRunTx's WHERE clause,
// engine.go:270, is IN ('open','approved')).
func TestTransition_BackToDraftPathsCancelEveryLiveRun(t *testing.T) {
	super, app := dbTestPools(t)
	ruleSetVersionID := seedRuleSetVersionID(t, super)

	cases := []struct {
		name        string
		startStatus Status
		act         func(fx gateFixture) error
	}{
		{"validated->draft", StatusValidated, func(fx gateFixture) error {
			_, err := NewStore(app).Transition(fx.ctx, fx.invID, StatusDraft)
			return err
		}},
		{"rejected->draft", StatusRejected, func(fx gateFixture) error {
			_, err := NewStore(app).Transition(fx.ctx, fx.invID, StatusDraft)
			return err
		}},
		{"edit demotion", StatusValidated, func(fx gateFixture) error {
			_, err := NewStore(app).Edit(fx.ctx, fx.invID, EditInput{UpdateInput: UpdateInput{VAT: strPtr("7.00")}})
			return err
		}},
		{"revalidate demotion", StatusValidated, func(fx gateFixture) error {
			return db.WithinTenantTx(context.Background(), app, fx.tenantID, func(tx pgx.Tx) error {
				_, err := NewStore(app).DemoteRevalidatedTx(context.Background(), tx, fx.invID, fx.tenantID, []Violation{}, ruleSetVersionID)
				return err
			})
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := seedGatedTenant(t, super, "APPR-16-05-DRAFT-"+tc.name, tc.startStatus)
			approvedID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
			closeApprovalRunFor(t, super, approvedID, "approved", "fixture")
			openID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)

			// Control: the fixture must really have armed one open AND one approved
			// run before the demotion below can prove both were cancelled.
			if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1 AND state = 'approved'`, fx.invID); n != 1 {
				t.Fatalf("%s: approved approval_runs before demotion = %d, want 1 -- fixture did not arm", tc.name, n)
			}
			if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1 AND state = 'open'`, fx.invID); n != 1 {
				t.Fatalf("%s: open approval_runs before demotion = %d, want 1 -- fixture did not arm", tc.name, n)
			}

			if err := tc.act(fx); err != nil {
				t.Fatalf("%s: %v (want nil)", tc.name, err)
			}

			if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1 AND state IN ('open','approved')`, fx.invID); n != 0 {
				t.Errorf("%s: live (open/approved) approval_runs = %d, want 0", tc.name, n)
			}
			if s := runStateOf(t, super, approvedID); s != "cancelled" {
				t.Errorf("%s: approved run %s state = %q, want %q", tc.name, approvedID, s, "cancelled")
			}
			if s := runStateOf(t, super, openID); s != "cancelled" {
				t.Errorf("%s: open run %s state = %q, want %q", tc.name, openID, s, "cancelled")
			}
		})
	}
}
