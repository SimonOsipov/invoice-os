// APPR-08-03 (task-497, Mode B): adversarial coverage ON TOP OF transition_gate_test.go.
// Mode A's specs came from the acceptance criteria; these came from mutating the shipped
// guard and from the edges the ACs do not name — the non-approved closed run states, the
// placement AC no behaviour can observe, a TransmitClearTx that errors, cross-tenant, the
// uuid forms Postgres accepts besides lowercase, all seven statuses in one oracle, and the
// [approved-run-not-latest-run] invariant no DB constraint holds.
//
//	Run: DATABASE_URL=… DATABASE_SUPERUSER_URL=… DATABASE_READER_URL=… \
//	     go test -p 1 -count=1 ./internal/invoice/...
package invoice

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- only "approved" clears: the other closed states must still refuse -------

// TestTransition_NonApprovedClosedRunStatesStillRefuse: TransmitClear reads
// EXISTS(state='approved'), so a run that ended any other way leaves the invoice gated.
// A predicate written as "no OPEN run" instead would let both of these through.
func TestTransition_NonApprovedClosedRunStatesStillRefuse(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app, WithApprovalsEnforced(true))

	for _, state := range []string{"cancelled", "rejected"} {
		t.Run(state, func(t *testing.T) {
			fx := seedGatedTenant(t, super, "APPR-08-03-CLOSED-"+state, StatusValidated)
			runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
			closeApprovalRunFor(t, super, runID, state, "fixture")

			_, err := store.Transition(fx.ctx, fx.invID, StatusQueued)
			if !errors.Is(err, ErrAwaitingApproval) {
				t.Fatalf("Transition(validated -> queued) with a %q run: err = %v, want ErrAwaitingApproval", state, err)
			}
			if s := statusOf(t, super, fx.invID); s != StatusValidated {
				t.Errorf("invoice status = %q, want unchanged %q", s, StatusValidated)
			}
		})
	}
}

// --- AC #3's placement, which no behaviour can observe -----------------------

// TestTransition_GuardSitsBetweenTheRedundancyCheckAndTransitionTx pins AC #3's ordering
// at the SOURCE, because nothing observable pins it: the canTransition conjunct makes the
// guard and the redundancy check mutually exclusive (redundancy needs current==target,
// the guard needs a LEGAL edge), and moving the guard below transitionTx only means the
// rolled-back tx did more work first. Both mutants survive the whole suite.
//
// The function BODY is printed, not the file: comments live on the *ast.File and are not
// emitted when a BlockStmt is printed alone, so the doc comment above Transition — which
// names all four markers in this same order — cannot satisfy this.
func TestTransition_GuardSitsBetweenTheRedundancyCheckAndTransitionTx(t *testing.T) {
	body := transitionBodySource(t)

	markers := []string{"FOR UPDATE", "ErrRedundantTransition", "ErrAwaitingApproval", "transitionTx("}
	at := make([]int, len(markers))
	for i, m := range markers {
		if n := strings.Count(body, m); n != 1 {
			t.Fatalf("Store.Transition's body mentions %q %d times, want exactly 1 — the ordering below is only meaningful while each marker is unique", m, n)
		}
		at[i] = strings.Index(body, m)
	}
	for i := 1; i < len(markers); i++ {
		if at[i] <= at[i-1] {
			t.Errorf("Store.Transition: %q appears before %q, want the AC #3 order %v", markers[i], markers[i-1], markers)
		}
	}
}

// transitionBodySource returns Store.Transition's body as comment-free Go source.
func transitionBodySource(t *testing.T) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "store.go", nil, 0)
	if err != nil {
		t.Fatalf("parse store.go: %v", err)
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Transition" || fn.Recv == nil || fn.Body == nil {
			continue
		}
		var sb strings.Builder
		if err := printer.Fprint(&sb, fset, fn.Body); err != nil {
			t.Fatalf("print Store.Transition's body: %v", err)
		}
		return sb.String()
	}
	t.Fatal("no func (…) Transition with a body in store.go")
	return ""
}

// --- a TransmitClearTx that errors must not read as an answer ----------------

// cancelOnSQL fails exactly one statement by cancelling the context pgx runs it under:
// conn.Query reassigns ctx from the tracer's return value (pgx v5 conn.go:753), so this is
// the surgical way to make one specific query error without touching grants or schema.
type cancelOnSQL struct{ match string }

func (c cancelOnSQL) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if !strings.Contains(d.SQL, c.match) {
		return ctx
	}
	dead, cancel := context.WithCancel(ctx)
	cancel()
	return dead
}

func (cancelOnSQL) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// TestTransition_TransmitClearTxErrorIsReturnedNotSwallowed: swallowing the error
// (clear, _ := …) leaves a nil map, which reads false and answers ErrAwaitingApproval —
// a database outage reported to the caller as a settled approval verdict, 409 not 500.
// The whole Mode A suite passes with the error swallowed; this is the spec that does not.
func TestTransition_TransmitClearTxErrorIsReturnedNotSwallowed(t *testing.T) {
	super, _ := dbTestPools(t)
	failing := appPoolWithTracer(t, cancelOnSQL{match: "approval_policy_versions"})

	fx := seedGatedTenant(t, super, "APPR-08-03-GATEERR", StatusValidated)
	runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
	closeApprovalRunFor(t, super, runID, "approved", "fixture") // would otherwise CLEAR

	store := NewStore(failing, WithApprovalsEnforced(true))

	_, err := store.Transition(fx.ctx, fx.invID, StatusQueued)
	if err == nil {
		t.Fatal("Transition with a failing TransmitClearTx returned nil — the gate's error was swallowed")
	}
	if errors.Is(err, ErrAwaitingApproval) {
		t.Errorf("Transition err = ErrAwaitingApproval, want the raw failure — an unreadable gate is not a verdict")
	}
	if status, _ := statusForErr(err); status != http.StatusInternalServerError {
		t.Errorf("statusForErr(%v) = %d, want 500", err, status)
	}
	if s := statusOf(t, super, fx.invID); s != StatusValidated {
		t.Errorf("invoice status = %q, want unchanged %q — the tx must roll back", s, StatusValidated)
	}
}

// --- the approval read happens AFTER the lock statement ----------------------

// TestTransition_GateStatementsFollowTheLockStatement is the cheap, direct half of
// TestTransition_GateRunsAfterTheRowLock: that one proves the gate decides while genuinely
// blocked, this one proves the statements leave in the AC #3 order at all.
func TestTransition_GateStatementsFollowTheLockStatement(t *testing.T) {
	super, _ := dbTestPools(t)
	tracedApp, rec := tracedAppPool(t)

	fx := seedGatedTenant(t, super, "APPR-08-03-STMTORDER", StatusValidated)
	runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
	closeApprovalRunFor(t, super, runID, "approved", "fixture")

	store := NewStore(tracedApp, WithApprovalsEnforced(true))

	rec.reset()
	if _, err := store.Transition(fx.ctx, fx.invID, StatusQueued); err != nil {
		t.Fatalf("Transition(validated -> queued) with an approved run: %v (want nil)", err)
	}

	lockAt := rec.firstIndexMentioning(t, "FOR UPDATE")
	policyAt := rec.firstIndexMentioning(t, "approval_policy_versions")
	runsAt := rec.firstIndexMentioning(t, "approval_runs")
	if policyAt <= lockAt {
		t.Errorf("the policy read is statement %d and the row lock is statement %d — the gate read the fact before locking the row", policyAt, lockAt)
	}
	if runsAt <= lockAt {
		t.Errorf("the run read is statement %d and the row lock is statement %d — the gate read the fact before locking the row", runsAt, lockAt)
	}
}

// --- cross-tenant: RLS answers first, the gate never gets an opinion ---------

// TestTransition_CrossTenantQueuedIsNotFound: the lock SELECT is RLS-scoped, so another
// tenant's invoice is zero rows -> ErrNotFound. Both run states are exercised because a
// leak would show up as a DIFFERENT wrong answer in each: success for the approved run,
// ErrAwaitingApproval for the open one.
func TestTransition_CrossTenantQueuedIsNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app, WithApprovalsEnforced(true))

	intruder := gateCtx(seedTenant(t, super, "APPR-08-03-INTRUDER tenant"))

	for _, runState := range []string{"open", "approved"} {
		t.Run(runState+" run", func(t *testing.T) {
			fx := seedGatedTenant(t, super, "APPR-08-03-XTENANT-"+runState, StatusValidated)
			runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
			if runState != "open" {
				closeApprovalRunFor(t, super, runID, runState, "fixture")
			}

			_, err := store.Transition(intruder, fx.invID, StatusQueued)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("Transition on another tenant's invoice: err = %v, want ErrNotFound", err)
			}
			if s := statusOf(t, super, fx.invID); s != StatusValidated {
				t.Errorf("victim invoice status = %q, want unchanged %q", s, StatusValidated)
			}
		})
	}
}

// --- every uuid form Postgres accepts, and one it does not -------------------

// TestTransition_NonCanonicalIdFormsUnderTheFlag widens AC #9 past uppercase. Postgres
// accepts braces and bare hex as well, and each reaches TransmitClearTx as the caller
// typed it — keying the gate on the LOCKED row's id is what makes all of them work, and
// a uuid.Parse normalisation would have covered only two of the three.
func TestTransition_NonCanonicalIdFormsUnderTheFlag(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app, WithApprovalsEnforced(true))

	forms := map[string]func(string) string{
		"braced":    func(id string) string { return "{" + id + "}" },
		"unhyphen":  func(id string) string { return strings.ReplaceAll(id, "-", "") },
		"uppercase": strings.ToUpper,
	}

	for name, form := range forms {
		t.Run(name+"/approved reaches queued", func(t *testing.T) {
			fx := seedGatedTenant(t, super, "APPR-08-03-FORM-OK-"+name, StatusValidated)
			runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
			closeApprovalRunFor(t, super, runID, "approved", "fixture")

			addressed := form(fx.invID)
			if addressed == fx.invID {
				t.Fatalf("form %q left the id %q unchanged — this subtest exercises nothing", name, fx.invID)
			}
			if _, err := store.Transition(fx.ctx, addressed, StatusQueued); err != nil {
				t.Fatalf("Transition(%s id -> queued) with an approved run: %v (want nil)", name, err)
			}
			if s := statusOf(t, super, fx.invID); s != StatusQueued {
				t.Errorf("stored status = %q, want %q", s, StatusQueued)
			}
		})

		t.Run(name+"/open still refuses", func(t *testing.T) {
			fx := seedGatedTenant(t, super, "APPR-08-03-FORM-NO-"+name, StatusValidated)
			seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)

			_, err := store.Transition(fx.ctx, form(fx.invID), StatusQueued)
			if !errors.Is(err, ErrAwaitingApproval) {
				t.Fatalf("Transition(%s id -> queued) with an open run: err = %v, want ErrAwaitingApproval — normalisation is not a bypass", name, err)
			}
		})
	}

	// urn:uuid: is the one common form Postgres rejects: 22P02 in the lock SELECT, before
	// the gate exists (TestTransition_MalformedIdUnderTheFlagIsStillValidation's sibling).
	t.Run("urn form is validation, not a gate answer", func(t *testing.T) {
		fx := seedGatedTenant(t, super, "APPR-08-03-FORM-URN", StatusValidated)
		seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)

		_, err := store.Transition(fx.ctx, "urn:uuid:"+fx.invID, StatusQueued)
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("Transition(urn:uuid:… -> queued): err = %v, want ErrValidation", err)
		}
	})
}

// --- all seven statuses, one oracle -----------------------------------------

// TestTransition_FlagOnAllSevenStatusesIntoQueued is the flag-ON mirror of
// TestTransition_ExhaustiveMatrixLocksLegalEdgeTable's queued column: every one of the 7
// starting statuses, under an ACTIVE policy and an open run, with the exact sentinel each
// must answer. validated is the ONE cell the flag moves; a guard that widened by even one
// status flips a cell here.
func TestTransition_FlagOnAllSevenStatusesIntoQueued(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app, WithApprovalsEnforced(true))

	want := map[Status]error{
		StatusDraft:     ErrIllegalTransition,
		StatusValidated: ErrAwaitingApproval,
		StatusQueued:    ErrRedundantTransition,
		StatusSubmitted: ErrIllegalTransition,
		StatusAccepted:  ErrIllegalTransition,
		StatusRejected:  ErrIllegalTransition,
		StatusFailed:    ErrIllegalTransition,
	}
	if len(want) != len(allStatuses) {
		t.Fatalf("the oracle covers %d statuses but the package has %d — a new status needs a cell here", len(want), len(allStatuses))
	}

	for _, from := range allStatuses {
		t.Run(string(from), func(t *testing.T) {
			fx := seedGatedTenant(t, super, "APPR-08-03-ALL7-"+string(from), from)
			seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)

			_, err := store.Transition(fx.ctx, fx.invID, StatusQueued)
			if !errors.Is(err, want[from]) {
				t.Fatalf("Transition(%s -> queued) under the flag: err = %v, want %v", from, err, want[from])
			}
			if s := statusOf(t, super, fx.invID); s != from {
				t.Errorf("stored status = %q, want unchanged %q", s, from)
			}
		})
	}
}

// --- [approved-run-not-latest-run]: characterised, not asserted --------------

// TestTransition_AnApprovedRunAlongsideAnOpenOneStillClearsTheDoor characterises the
// invariant, it does not endorse it. TransmitClear reads EXISTS(state='approved') over
// EVERY run, deliberately (internal/approval TestGateFactsTx_ApprovedRunIsExistsNotLatestRun),
// so an invoice holding BOTH an approved run and a newer open one passes — and the schema
// permits exactly that pair, since approval_runs_one_open constrains only 'open'.
//
// Nothing in the DATABASE stops the pair arising; two code paths do, and this test exists
// so that a change to either one lands here instead of on the transmit door:
//   - CancelLiveRunTx demotes state IN ('open','approved') together (engine.go), so the
//     edit -> draft -> re-validate route cancels the approved run on the way past;
//   - sweepValidatedBacklog anti-joins the same two states (policy_store.go), so
//     publishing a second policy version does not arm over an approved run.
//
// Widen either predicate to 'open' alone and an approved-then-edited invoice transmits
// while its new run is still pending.
func TestTransition_AnApprovedRunAlongsideAnOpenOneStillClearsTheDoor(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedGatedTenant(t, super, "APPR-08-03-STALEAPPROVED", StatusValidated)
	approved := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
	closeApprovalRunFor(t, super, approved, "approved", "fixture")
	fresh := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID) // open

	store := NewStore(app, WithApprovalsEnforced(true))

	if _, err := store.Transition(fx.ctx, fx.invID, StatusQueued); err != nil {
		t.Fatalf("Transition(validated -> queued) with an approved AND an open run: %v (want nil — EXISTS over every run)", err)
	}
	if s := statusOf(t, super, fx.invID); s != StatusQueued {
		t.Errorf("stored status = %q, want %q", s, StatusQueued)
	}
	// The open run is left alone, so reconciliation's approval_run_orphaned detector
	// (state='open' AND status<>'validated') still reports the pair.
	if s := runStateOf(t, super, fresh); s != "open" {
		t.Errorf("the newer run's state = %q, want unchanged %q", s, "open")
	}
}

// --- shared helpers ----------------------------------------------------------

// appPoolWithTracer is tracedAppPool's general form. Callers must already have gone
// through dbTestPools, which owns the skip gate.
func appPoolWithTracer(t *testing.T, tracer pgx.QueryTracer) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	cfg.ConnConfig.Tracer = tracer
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect traced app pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// firstIndexMentioning is the statement index of the first recorded SQL containing substr.
func (r *sqlRecorder) firstIndexMentioning(t *testing.T, substr string) int {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, s := range r.sql {
		if strings.Contains(s, substr) {
			return i
		}
	}
	t.Fatalf("no recorded statement mentions %q; recorded: %v", substr, r.sql)
	return -1
}
