package approval

// task-491 (APPR-07-06, Mode B / QA phase): DB-backed proof that the WIRED handler --
// decisionMux -> DecideHandler -> the real (*Store).DecideSeam -> the real Decide ->
// commitDecisionTx -- works end to end. Every test in decision_handlers_test.go injects
// a hand-written Decider closure, so nothing there proves DecideSeam's own "" -> nil
// reason conversion (decision.go:75-81) ever runs at all; it has no test of its own
// anywhere in the package. This file is that missing seam.
//
// Run locally with `DEV_DB_PORT=5433 make test-approvals`; in CI the rls job's gate
// step fails the build on any skip.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// decideOverHTTP builds the real production mux (decisionMux, DecideSeam wired to a
// real *Store) and drives one POST through it -- serveDecision's shape, but against a
// live DecideSeam instead of a hand-written Decider.
func decideOverHTTP(t *testing.T, store *Store, tenantID, subject, invoiceID, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := decisionMux(store.DecideSeam, nil)
	r := httptest.NewRequest("POST", "/v1/invoices/"+invoiceID+"/approvals", strings.NewReader(body))
	r = r.WithContext(auth.WithIdentity(r.Context(),
		auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// TestDecideHandler_ApproveOverRealHTTPReturnsThePostDecisionRunState (item 3): the
// seam a stub-only suite hides -- approve a real armed run through the actual wired
// handler + DecideSeam + Decide + Postgres, not a mocked Decider. Response state must
// match the read-back state, and the underlying step/decision rows must be exactly
// what a direct Decide() call would have written (TestApprove_AdminWithWorkflowRoleAllowed's
// own assertions, reused here against the HTTP path instead).
func TestDecideHandler_ApproveOverRealHTTPReturnsThePostDecisionRunState(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 http-approve-real-store", "http-approve-real-store-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	store := NewStore(app, stubFingerprinter, nil)
	rec := decideOverHTTP(t, store, f.tenantID, adminID, f.invoiceID, `{"decision":"approved","reason":"looks right"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got Run
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if got.RunID != f.runID {
		t.Errorf("response run_id = %q, want %q", got.RunID, f.runID)
	}
	if got.State != "approved" {
		t.Errorf("response state = %q, want approved -- the lone approval step just closed it", got.State)
	}
	if got.ClosedBy == nil || *got.ClosedBy != adminID {
		t.Errorf("response closed_by = %v, want %q", got.ClosedBy, adminID)
	}

	// The write actually happened, through the real wiring -- not merely a 200.
	steps := runStepsOf(t, super, f.runID)
	if len(steps) != 1 || steps[0].State != "satisfied" {
		t.Errorf("run steps = %+v, want the single step satisfied", steps)
	}
	decisions := decisionsForRun(t, super, f.runID)
	if len(decisions) != 1 || decisions[0].Decision != "approved" || decisions[0].Actor != adminID {
		t.Errorf("decisions = %+v, want exactly one {decision:approved actor:%q}", decisions, adminID)
	}
	if decisions[0].Reason == nil || *decisions[0].Reason != "looks right" {
		t.Errorf("stored reason = %v, want %q", decisions[0].Reason, "looks right")
	}
}

// TestDecideHandler_AbsentReasonOnApproveIsStoredAsSQLNullThroughTheRealSeam: AC-6
// ("a blank reason on an approve reaches the store as an empty string and is stored as
// SQL NULL") names DecideSeam's OWN conversion (decision.go:75-81), which no other test
// in this package exercises -- decision_handlers_test.go's mocked Decider never runs
// DecideSeam, and decision_test.go's approve() helper calls Decide directly with a Go
// nil literal, never DecideSeam's "" branch. Mutation-tested: replacing DecideSeam's
// body with `return s.Decide(ctx, invoiceID, decision, &reason)` (always a non-nil
// pointer, even for "") is invisible to every other test in the package and changes
// this test's stored column from NULL to "".
func TestDecideHandler_AbsentReasonOnApproveIsStoredAsSQLNullThroughTheRealSeam(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 http-absent-reason-null", "http-absent-reason-null-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	store := NewStore(app, stubFingerprinter, nil)
	rec := decideOverHTTP(t, store, f.tenantID, adminID, f.invoiceID, `{"decision":"approved"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	decisions := decisionsForRun(t, super, f.runID)
	if len(decisions) != 1 {
		t.Fatalf("approval_decisions rows = %d, want 1", len(decisions))
	}
	if decisions[0].Reason != nil {
		t.Errorf("stored reason = %q, want SQL NULL (Go nil) for an absent reason field -- DecideSeam must convert \"\" to nil, not pass &\"\"", *decisions[0].Reason)
	}
}

// TestDecideHandler_RejectOverRealHTTPWritesTheReasonAndCallsTheDemoter: reject's own
// half of the same wiring proof, using stubDemoter (a real invoice-package demotion is
// exercised separately in internal/invoice, which internal/approval cannot import).
func TestDecideHandler_RejectOverRealHTTPWritesTheReasonAndCallsTheDemoter(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 http-reject-real-store", "http-reject-real-store-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	store := NewStore(app, stubFingerprinter, stubDemoter)
	rec := decideOverHTTP(t, store, f.tenantID, adminID, f.invoiceID, `{"decision":"rejected","reason":"wrong VAT"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got Run
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if got.State != "rejected" {
		t.Errorf("response state = %q, want rejected", got.State)
	}
	decisions := decisionsForRun(t, super, f.runID)
	if len(decisions) != 1 || decisions[0].Decision != "rejected" {
		t.Errorf("decisions = %+v, want exactly one rejected row", decisions)
	}
	if decisions[0].Reason == nil || *decisions[0].Reason != "wrong VAT" {
		t.Errorf("stored reason = %v, want %q", decisions[0].Reason, "wrong VAT")
	}
	steps := runStepsOf(t, super, f.runID)
	if len(steps) != 1 || steps[0].State != "rejected" {
		t.Errorf("run steps = %+v, want the single step rejected", steps)
	}
}

// TestDecideHandler_ResponseBodyCarriesTheSameStepsAndDecisionsAFreshGETReturns
// (defect finding, item 3): AC-8 and the design's own §2.1/§2.3 say POST's 200 body is
// "the same Run body as GET /v1/invoices/{id}/approval returns" -- the §2.3 example
// shows a populated steps array and a decisions array carrying the just-made decision.
// Proven here by driving BOTH routes against the identical just-decided run and diffing
// the two bodies directly, rather than trusting either route's own key-set-only test.
//
// FAILS TODAY: decideTx's own return (decision.go:191-198) only ever populates
// RunID/State/OpenedAt/ClosedAt/ClosedBy -- Steps and Decisions are never assigned, so
// Run.MarshalJSON's []-never-null rule always substitutes an EMPTY array for both on
// this route, never the real ones. Root cause lives in decision.go (subtask
// APPR-07-04/05's decideTx), outside this subtask's file list -- reported as a defect,
// not fixed here (QA does not implement store changes).
func TestDecideHandler_ResponseBodyCarriesTheSameStepsAndDecisionsAFreshGETReturns(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 http-post-vs-get-defect", "http-post-vs-get-defect-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	store := NewStore(app, stubFingerprinter, nil)
	postRec := decideOverHTTP(t, store, f.tenantID, adminID, f.invoiceID, `{"decision":"approved","reason":"looks right"}`)
	if postRec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200: %s", postRec.Code, postRec.Body.String())
	}

	runReadFn := func(ctx context.Context, invoiceID string) (Run, error) { return store.ApprovalRun(ctx, invoiceID) }
	getMux := runMux(runReadFn, nil)
	getReq := httptest.NewRequest("GET", "/v1/invoices/"+f.invoiceID+"/approval", nil)
	getReq = getReq.WithContext(auth.WithIdentity(getReq.Context(),
		auth.Identity{Subject: adminID, Role: "authenticated", TenantID: f.tenantID}))
	getRec := httptest.NewRecorder()
	getMux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200: %s", getRec.Code, getRec.Body.String())
	}

	var postRun, getRun Run
	if err := json.Unmarshal(postRec.Body.Bytes(), &postRun); err != nil {
		t.Fatalf("decode POST body: %v", err)
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getRun); err != nil {
		t.Fatalf("decode GET body: %v", err)
	}

	if len(getRun.Steps) == 0 {
		t.Fatalf("test setup: GET returned zero steps for a just-armed run, want at least 1 -- the assertion below would be vacuous")
	}
	if len(postRun.Steps) != len(getRun.Steps) {
		t.Errorf("POST steps = %d, want %d (the same run's GET, per AC-8) -- decideTx must assemble the full read model, the same as ApprovalRun does", len(postRun.Steps), len(getRun.Steps))
	}
	if len(getRun.Decisions) == 0 {
		t.Fatalf("test setup: GET returned zero decisions right after a decide, want at least 1 -- the assertion below would be vacuous")
	}
	if len(postRun.Decisions) != len(getRun.Decisions) {
		t.Errorf("POST decisions = %d, want %d (the same run's GET, per AC-8)", len(postRun.Decisions), len(getRun.Decisions))
	}
}

// TestDecideHandler_ConcurrentPOSTsToTheSameInvoiceSerializeCleanly: two goroutines POST
// the SAME single-step armed run through the REAL http.Handler at once (not two direct
// Decide() calls, TestApprove_ConcurrentSingleDecision's own precedent) -- proves the
// row-lock discipline holds when entered from the wire path, where each request builds
// its own tx from scratch inside the handler rather than sharing a caller-managed one.
func TestDecideHandler_ConcurrentPOSTsToTheSameInvoiceSerializeCleanly(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 http-concurrent-same-invoice", "http-concurrent-same-invoice-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	store := NewStore(app, stubFingerprinter, nil)
	const n = 2
	recs := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			<-start
			recs[i] = decideOverHTTP(t, store, f.tenantID, adminID, f.invoiceID, `{"decision":"approved"}`)
		}()
	}
	close(start)
	wg.Wait()

	var okCount, conflictCount int
	for _, rec := range recs {
		switch rec.Code {
		case http.StatusOK:
			okCount++
		case http.StatusConflict:
			conflictCount++
			if got := errorMessage(t, rec.Body.Bytes()); got != "this approval run is already closed" {
				t.Errorf("conflict body error = %q, want the ErrRunClosed message", got)
			}
		default:
			t.Errorf("unexpected status %d: %s", rec.Code, rec.Body.String())
		}
	}
	if okCount != 1 || conflictCount != 1 {
		t.Errorf("okCount = %d, conflictCount = %d, want exactly 1 and 1", okCount, conflictCount)
	}
	if n := len(decisionsForRun(t, super, f.runID)); n != 1 {
		t.Errorf("approval_decisions rows for the run = %d, want exactly 1 -- no double-write under concurrency", n)
	}
}

// --- re-verification pass (fix-verify): the three fixed defects, proven through HTTP --

// TestDecideHandler_NULByteInReasonOverHTTPIs400NotAnOpaque500: the fix's third defect
// (a NUL byte used to surface as a raw SQLSTATE 22021 Postgres error, which
// decisionStatusForErr's default case turned into a bare 500) closed at the Decide
// level (hasNUL, decision.go:44-49) and the mapper level (ErrValidation -> 400
// "invalid reason", handlers.go:238-241). TestDecide_NULByteInReasonIsRefusedNotStored
// proves the Decide half directly; this proves the mapper half is actually REACHABLE
// through the real wired handler, not merely correct in isolation -- nothing else in
// the package drives a NUL byte through decisionMux -> DecideHandler -> DecideSeam ->
// Decide -> Postgres.
func TestDecideHandler_NULByteInReasonOverHTTPIs400NotAnOpaque500(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 http-nul-byte-reason", "http-nul-byte-reason-role")
	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	store := NewStore(app, stubFingerprinter, nil)
	body, err := json.Marshal(decisionRequest{Decision: "approved", Reason: "bad\x00reason"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rec := decideOverHTTP(t, store, f.tenantID, adminID, f.invoiceID, string(body))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (not 500 -- the raw Postgres 22021 must never reach the client): %s", rec.Code, rec.Body.String())
	}
	if got := errorMessage(t, rec.Body.Bytes()); got != "invalid reason" {
		t.Errorf("error = %q, want %q", got, "invalid reason")
	}
	assertNothingWritten(t, super, f)
}

// TestDecideHandler_ErrValidationDoesNotShadowAnotherSentinel: decisionStatusForErr's
// new ErrValidation case (handlers.go:238-241) sits between ErrNotAwaitingApproval and
// default -- confirms every OTHER sentinel in the six-case table still maps to its own
// status/message, i.e. the new case did not accidentally widen to catch errors it
// should not (errors.Is only matches ErrValidation itself; this is a regression guard
// on the whole switch, not just the new arm).
func TestDecideHandler_ErrValidationDoesNotShadowAnotherSentinel(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{"no tenant", db.ErrNoTenant, http.StatusUnauthorized, "unauthorized"},
		{"not an approver", ErrNotPermitted, http.StatusForbidden, "only an approver can decide an approval step"},
		{"not the role holder", ErrNotRoleHolder, http.StatusForbidden, "you do not hold the workflow role this step is waiting on"},
		{"no run", ErrRunNotFound, http.StatusNotFound, "no approval run for this invoice"},
		{"run closed", ErrRunClosed, http.StatusConflict, "this approval run is already closed"},
		{"not awaiting approval", ErrNotAwaitingApproval, http.StatusConflict, "this invoice is no longer awaiting approval"},
		{"validation (the new case)", ErrValidation, http.StatusBadRequest, "invalid reason"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := caller()
			decide := Decider(func(context.Context, string, string, string) (Run, error) { return Run{}, c.err })
			rec := serveDecision(t, decide, nil, "/v1/invoices/"+decisionHandlerTestID+"/approvals", `{"decision":"approved"}`, &id)

			if rec.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, c.wantStatus, rec.Body.String())
			}
			if got := errorMessage(t, rec.Body.Bytes()); got != c.wantMsg {
				t.Errorf("error = %q, want %q -- ErrValidation's new case must not shadow this sentinel", got, c.wantMsg)
			}
		})
	}
}

// TestDecideHandler_PostReturnsTheCurrentRunNotACancelledPriorOneOverHTTP mirrors
// TestApprovalRun_CancelledRunDoesNotShadowTheCurrentRun's fixture exactly
// (read_model_adversarial_test.go), but decides over HTTP instead of reading via GET --
// the re-verification pass's item 3-bullet-2 ask: does decideTx's header re-read return
// the run it just decided, never a different run sharing the same invoice_id? decideTx
// re-reads by the SAME runID pinned earlier in the function (not a fresh
// invoice_id-keyed lookup), so this also guards against a future edit that swaps that
// re-read for one keyed on invoice_id + ORDER BY opened_at DESC, which WOULD be exposed
// to exactly the trap ApprovalRun's own precedent test exists for.
func TestDecideHandler_PostReturnsTheCurrentRunNotACancelledPriorOneOverHTTP(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 http-multi-run-current")
	entityID := seedBusinessEntity(t, super, tenantID, "HTTP Multi Run Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "http-multi-run-invoice-1")
	setInvoiceStatus(t, super, invoiceID, "validated")

	policyID := seedApprovalPolicy(t, super, tenantID, "HTTP multi-run policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("http-multi-run-role"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	firstRes, err := arm(t, app, tenantID, invoiceID, "fp-http-multi-run-1", "test-actor")
	if err != nil {
		t.Fatalf("arm (first): %v", err)
	}
	backdateRunOpenedAt(t, super, firstRes.RunID, time.Now().Add(-time.Hour))

	if _, err := cancel(t, app, tenantID, invoiceID, "test-actor"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	secondRes, err := arm(t, app, tenantID, invoiceID, "fp-http-multi-run-2", "test-actor")
	if err != nil {
		t.Fatalf("arm (second): %v", err)
	}
	if secondRes.RunID == firstRes.RunID {
		t.Fatal("second arm reused the first run's id -- the fixture is broken, not the assertion below")
	}

	adminID := uuid.NewString()
	seedMembership(t, super, tenantID, adminID, "admin", "active")
	roleID := seedWorkflowRole(t, super, tenantID, "http-multi-run-role", "http-multi-run-role")
	staffWorkflowRole(t, super, tenantID, roleID, adminID, 0)

	store := NewStore(app, stubFingerprinter, nil)
	rec := decideOverHTTP(t, store, tenantID, adminID, invoiceID, `{"decision":"approved"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got Run
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if got.RunID != secondRes.RunID {
		t.Errorf("response run_id = %q, want the current (second) run %q, never the cancelled first run %q", got.RunID, secondRes.RunID, firstRes.RunID)
	}
	if got.State != "approved" {
		t.Errorf("response state = %q, want approved", got.State)
	}

	// The cancelled run is untouched -- this decided the CURRENT run, not the stale one.
	// Queried by each run's own known id (not oneApprovalRun, which requires exactly one
	// row for the invoice and would fail here -- there are deliberately two).
	var firstState, secondState string
	if err := super.QueryRow(context.Background(), `SELECT state FROM approval_runs WHERE id = $1`, firstRes.RunID).Scan(&firstState); err != nil {
		t.Fatalf("read back first (cancelled) run state: %v", err)
	}
	if err := super.QueryRow(context.Background(), `SELECT state FROM approval_runs WHERE id = $1`, secondRes.RunID).Scan(&secondState); err != nil {
		t.Fatalf("read back second (current) run state: %v", err)
	}
	if firstState != "cancelled" {
		t.Errorf("first run state = %q, want unchanged cancelled -- the decide must not have touched it", firstState)
	}
	if secondState != "approved" {
		t.Errorf("second run state = %q, want approved", secondState)
	}
}
