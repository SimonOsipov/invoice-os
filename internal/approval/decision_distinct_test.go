package approval

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// The story's copy, restated on purpose: the oracle for EarlierApproverRefusedReason.
const earlierSentence = "You approved an earlier step of this invoice, so another approver must approve this step."

// distinctWorld is a firm tenant with one invoice on an armed run of one approval step per key.
// A repeated key is one role. Nobody is an author: no test seeds a history row unless it says so.
type distinctWorld struct {
	super, app *pgxpool.Pool
	tenantID   string
	entityID   string
	invoiceID  string
	runID      string
	roleIDs    []string // per step
}

func newDistinctWorld(t *testing.T, super, app *pgxpool.Pool, name string, keys ...string) distinctWorld {
	t.Helper()
	w := distinctWorld{super: super, app: app}
	w.tenantID = policyTenant(t, super, name)
	w.entityID = seedBusinessEntity(t, super, w.tenantID, name+" Corp")
	w.invoiceID = seedInvoice(t, super, w.tenantID, w.entityID, name+"-inv")
	setInvoiceStatus(t, super, w.invoiceID, "validated")
	byKey := map[string]string{}
	for _, k := range keys {
		if _, ok := byKey[k]; !ok {
			byKey[k] = seedWorkflowRole(t, super, w.tenantID, k, k)
		}
		w.roleIDs = append(w.roleIDs, byKey[k])
	}
	policyID := seedApprovalPolicy(t, super, w.tenantID, name+" policy")
	versionID := seedApprovalPolicyVersionN(t, super, w.tenantID, policyID, 1)
	for ord, k := range keys {
		seedApprovalPolicyStepInLane(t, super, w.tenantID, versionID, seedStepSpec{
			Ord: ord, Kind: "approval", WorkflowRoleKey: ptr(k), SLAHours: ptr(48),
		})
	}
	activateApprovalPolicyVersion(t, super, versionID)
	res, err := arm(t, app, w.tenantID, w.invoiceID, "fp-"+name, "fixture-arm")
	if err != nil {
		t.Fatalf("arm: %v", err)
	}
	w.runID = res.RunID
	return w
}

// member seeds a member who holds the roles of the given step indexes.
func (w distinctWorld) member(t *testing.T, spec memberSpec, steps ...int) string {
	t.Helper()
	user := seedMemberOf(t, w.super, w.tenantID, spec)
	seen := map[string]bool{}
	for _, i := range steps {
		if seen[w.roleIDs[i]] {
			continue
		}
		seen[w.roleIDs[i]] = true
		var n int
		if err := w.super.QueryRow(context.Background(),
			`SELECT count(*) FROM workflow_role_members WHERE workflow_role_id = $1`, w.roleIDs[i]).Scan(&n); err != nil {
			t.Fatalf("count role members: %v", err)
		}
		staffWorkflowRole(t, w.super, w.tenantID, w.roleIDs[i], user, n)
	}
	return user
}

func (w distinctWorld) reviewer(t *testing.T, steps ...int) string {
	t.Helper()
	return w.member(t, memberSpec{role: "reviewer"}, steps...)
}

func (w distinctWorld) mustApprove(t *testing.T, user string) Run {
	t.Helper()
	run, err := approve(t, w.app, w.tenantID, user, w.invoiceID, nil)
	if err != nil {
		t.Fatalf("approve as %s: %v", user, err)
	}
	return run
}

// wantNothingMore asserts a refusal left want decision rows, the step at ord pending and the run open.
func (w distinctWorld) wantNothingMore(t *testing.T, want, ord int) {
	t.Helper()
	if n := len(decisionsForRun(t, w.super, w.runID)); n != want {
		t.Errorf("decisions for the run = %d, want %d", n, want)
	}
	steps := runStepsOf(t, w.super, w.runID)
	if len(steps) <= ord || steps[ord].State != "pending" {
		t.Errorf("run steps = %+v, want step %d still pending", steps, ord)
	}
	if st := oneApprovalRun(t, w.super, w.invoiceID).State; st != "open" {
		t.Errorf("run state = %q, want open", st)
	}
}

// wantApprovedAsSeq asserts the tenant's approval_approved rows, in write order, carry want.
func wantApprovedAsSeq(t *testing.T, super *pgxpool.Pool, tenantID string, want ...string) {
	t.Helper()
	rows := approvedPayloads(t, super, tenantID)
	var got []any
	for _, r := range rows {
		got = append(got, r["approved_as"])
	}
	if len(got) != len(want) {
		t.Fatalf("approved_as of %d approval_approved rows = %v, want %v", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("approved_as = %v, want %v", got, want)
			return
		}
	}
}

func TestDecide_Distinct_SameActorRefusedOnStepTwo(t *testing.T) {
	super, app := dbTestPools(t)
	w := newDistinctWorld(t, super, app, "SECAUD-40 same-actor", "k", "k")
	a := w.reviewer(t, 0, 1)
	w.reviewer(t, 0, 1) // B

	w.mustApprove(t, a)
	_, err := approve(t, app, w.tenantID, a, w.invoiceID, nil)
	wantRefused(t, err, earlierSentence)
	w.wantNothingMore(t, 1, 1)
}

func TestDecide_Distinct_TwoActorsCloseTheRun(t *testing.T) {
	super, app := dbTestPools(t)
	w := newDistinctWorld(t, super, app, "SECAUD-40 two-actors", "k", "k")
	a := w.reviewer(t, 0, 1)
	b := w.reviewer(t, 0, 1)

	w.mustApprove(t, a)
	if run := w.mustApprove(t, b); run.State != "approved" {
		t.Fatalf("run after the second actor = %q, want approved", run.State)
	}
	wantApprovedAsSeq(t, super, w.tenantID, "approver", "approver")
}

func TestDecide_Distinct_EarlierDelegateDecisionCounts(t *testing.T) {
	super, app := dbTestPools(t)
	w := newDistinctWorld(t, super, app, "SECAUD-40 earlier-delegate", "k0", "k1")
	a := w.reviewer(t, 1) // A is no holder of k0
	w.reviewer(t, 1)      // B
	setStepDelegation(t, super, oneApprovalStep(t, super, w.runID, 0), true, nil)

	w.mustApprove(t, a)
	if ds := decisionsForRun(t, super, w.runID); len(ds) != 1 || !ds[0].AsDelegate {
		t.Fatalf("decisions = %+v, want A's step-0 approval as a delegate", ds)
	}
	_, err := approve(t, app, w.tenantID, a, w.invoiceID, nil)
	wantRefused(t, err, earlierSentence)
	w.wantNothingMore(t, 1, 1)
}

func TestDecide_Distinct_LaterDelegateApproveIsRefused(t *testing.T) {
	super, app := dbTestPools(t)
	w := newDistinctWorld(t, super, app, "SECAUD-40 later-delegate", "k0", "k1")
	a := w.reviewer(t, 0)
	w.reviewer(t, 1) // B holds k1
	setStepDelegation(t, super, oneApprovalStep(t, super, w.runID, 1), true, &a)

	w.mustApprove(t, a)
	_, err := approve(t, app, w.tenantID, a, w.invoiceID, nil)
	wantRefused(t, err, earlierSentence)
	if errors.Is(err, ErrNotRoleHolder) {
		t.Error("err is also ErrNotRoleHolder, want the earlier-approver refusal alone")
	}
	w.wantNothingMore(t, 1, 1)
}

func TestDecide_Distinct_NoOtherSignerAddsTheSecondSentence(t *testing.T) {
	super, app := dbTestPools(t)
	w := newDistinctWorld(t, super, app, "SECAUD-40 no-other-signer", "k0", "k1")
	a := w.reviewer(t, 0, 1)
	w.reviewer(t) // B reaches the client and holds no role

	w.mustApprove(t, a)
	_, err := approve(t, app, w.tenantID, a, w.invoiceID, nil)
	wantRefused(t, err, earlierSentence+" "+noHolderSentence)
	w.wantNothingMore(t, 1, 1)
}

func TestDecide_Distinct_SoleApproverFallsBackAndIsRecorded(t *testing.T) {
	super, app := dbTestPools(t)
	w := newDistinctWorld(t, super, app, "SECAUD-40 sole-approver", "k", "k")
	a := w.reviewer(t, 0, 1)
	// None of these counts as another eligible approver.
	entB := seedBusinessEntity(t, super, w.tenantID, "SECAUD-40 sole-approver other client")
	w.member(t, memberSpec{role: "preparer"}, 0, 1)
	w.member(t, memberSpec{role: "reviewer", status: "suspended"}, 0, 1)
	w.member(t, memberSpec{role: "reviewer", limited: true, entityIDs: []string{entB}}, 0, 1)

	w.mustApprove(t, a)
	if run := w.mustApprove(t, a); run.State != "approved" {
		t.Fatalf("run after the sole approver's second approve = %q, want approved", run.State)
	}
	wantApprovedAsSeq(t, super, w.tenantID, "approver", "repeat_approved")
}

func TestDecide_Distinct_SecondApproverEndsTheFallback(t *testing.T) {
	super, app := dbTestPools(t)
	w := newDistinctWorld(t, super, app, "SECAUD-40 fallback-ends", "k", "k")
	a := w.reviewer(t, 0, 1)

	w.mustApprove(t, a)
	w.reviewer(t) // B joins: reaches the client, no role
	_, err := approve(t, app, w.tenantID, a, w.invoiceID, nil)
	wantRefused(t, err, earlierSentence+" "+noHolderSentence)
	w.wantNothingMore(t, 1, 1)
}

// Author precedence: the creator who approved step 0 under the author fallback gets the creator sentence.
func TestDecide_Distinct_CreatorSentenceWinsForAnAuthorWhoApprovedEarlier(t *testing.T) {
	super, app := dbTestPools(t)
	w := newDistinctWorld(t, super, app, "SECAUD-40 author-wins", "k", "k")
	a := w.reviewer(t, 0, 1)
	seedHistory(t, super, w.tenantID, w.invoiceID, nil, a)

	w.mustApprove(t, a)
	wantApprovedAsSeq(t, super, w.tenantID, "self_approved")
	w.reviewer(t, 0, 1) // C: non-author, reaches the client, holds the role
	_, err := approve(t, app, w.tenantID, a, w.invoiceID, nil)
	wantRefused(t, err, CreatorRefusedReason)
	w.wantNothingMore(t, 1, 1)
}

// Each clause excludes the other person, so the pair cannot deadlock.
func TestDecide_Distinct_AnAuthorIsNotAnotherEligibleApprover(t *testing.T) {
	super, app := dbTestPools(t)
	w := newDistinctWorld(t, super, app, "SECAUD-40 author-not-eligible", "k0", "k1")
	a := w.reviewer(t, 0, 1)
	y := w.reviewer(t, 1)
	seedHistory(t, super, w.tenantID, w.invoiceID, nil, y)

	w.mustApprove(t, a)
	_, err := approve(t, app, w.tenantID, y, w.invoiceID, nil)
	wantRefused(t, err, CreatorRefusedReason)
	w.wantNothingMore(t, 1, 1)

	if run := w.mustApprove(t, a); run.State != "approved" {
		t.Fatalf("run after A's repeat approve = %q, want approved", run.State)
	}
	wantApprovedAsSeq(t, super, w.tenantID, "approver", "repeat_approved")
}

func TestDecide_Distinct_EarlierApproverMayReject(t *testing.T) {
	super, app := dbTestPools(t)
	w := newDistinctWorld(t, super, app, "SECAUD-40 may-reject", "k0", "k1")
	a := w.reviewer(t, 0, 1)
	w.reviewer(t, 0, 1) // B exists

	w.mustApprove(t, a)
	var calls []demoterCall
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: a, Role: "authenticated", TenantID: w.tenantID})
	run, err := NewStore(app, stubFingerprinter, spyDemoter(&calls)).Decide(c, w.invoiceID, "rejected", ptr("wrong buyer"))
	if err != nil || run.State != "rejected" {
		t.Fatalf("earlier approver rejects step 1 = %q, %v, want rejected", run.State, err)
	}
	if len(calls) != 1 {
		t.Errorf("demoter calls = %d, want 1", len(calls))
	}
	if ds := decisionsForRun(t, super, w.runID); len(ds) != 2 || ds[1].Decision != "rejected" || ds[1].Actor != a {
		t.Errorf("decisions = %+v, want A's approval then A's rejection", ds)
	}
}

// A's approval on I2 is the control: it makes A an earlier approver there, not on I1.
func TestDecide_Distinct_AnotherInvoicesApprovalDoesNotCount(t *testing.T) {
	super, app := dbTestPools(t)
	w := newDistinctWorld(t, super, app, "SECAUD-40 other-invoice", "k0", "k1")
	a := w.reviewer(t, 0, 1)
	b := w.reviewer(t, 0, 1)
	i2 := seedInvoice(t, super, w.tenantID, w.entityID, "distinct-other-invoice-i2")
	setInvoiceStatus(t, super, i2, "validated")
	if _, err := arm(t, app, w.tenantID, i2, "fp-distinct-i2", "fixture-arm"); err != nil {
		t.Fatalf("arm I2: %v", err)
	}

	if _, err := approve(t, app, w.tenantID, a, i2, nil); err != nil {
		t.Fatalf("A approves step 0 of I2: %v", err)
	}
	w.mustApprove(t, b)
	if run := w.mustApprove(t, a); run.State != "approved" {
		t.Fatalf("run of I1 after A's step 1 = %q, want approved", run.State)
	}
	var got []any
	for _, p := range approvedPayloads(t, super, w.tenantID) {
		if p["invoice_id"] == w.invoiceID {
			got = append(got, p["approved_as"])
		}
	}
	if len(got) != 2 || got[0] != "approver" || got[1] != "approver" {
		t.Errorf("approved_as of I1 = %v, want [approver approver]", got)
	}
}

// C makes a wrongly counted earlier run visible as a refusal instead of a quiet fallback.
func TestDecide_Distinct_AClosedEarlierRunDoesNotCount(t *testing.T) {
	super, app := dbTestPools(t)
	w := newDistinctWorld(t, super, app, "SECAUD-40 closed-run", "k0", "k1")
	a := w.reviewer(t, 0)
	b := w.reviewer(t, 1)
	w.reviewer(t, 0) // C

	w.mustApprove(t, a)
	if run, err := reject(t, app, w.tenantID, b, w.invoiceID, ptr("wrong buyer")); err != nil || run.State != "rejected" {
		t.Fatalf("B rejects step 1 = %q, %v, want rejected", run.State, err)
	}
	res, err := arm(t, app, w.tenantID, w.invoiceID, "fp-closed-run-2", "fixture-arm")
	if err != nil {
		t.Fatalf("arm the new run: %v", err)
	}
	if res.RunID == w.runID {
		t.Fatalf("new run reuses %s, want a new run", w.runID)
	}
	if run := w.mustApprove(t, a); run.State != "open" {
		t.Errorf("run after A approves step 0 of the new run = %q, want open", run.State)
	}
	if n := len(decisionsForRun(t, super, res.RunID)); n != 1 {
		t.Errorf("decisions on the new run = %d, want 1", n)
	}
}

func TestDecide_Distinct_TwoEarlierDecidersAreBothRefused(t *testing.T) {
	super, app := dbTestPools(t)
	w := newDistinctWorld(t, super, app, "SECAUD-40 two-deciders", "k0", "k1", "k2")
	a := w.reviewer(t, 0, 2)
	b := w.reviewer(t, 1, 2)
	c := w.reviewer(t, 2)

	w.mustApprove(t, a)
	w.mustApprove(t, b)
	_, err := approve(t, app, w.tenantID, a, w.invoiceID, nil)
	wantRefused(t, err, earlierSentence)
	_, err = approve(t, app, w.tenantID, b, w.invoiceID, nil)
	wantRefused(t, err, earlierSentence)
	w.wantNothingMore(t, 2, 2)

	if run := w.mustApprove(t, c); run.State != "approved" {
		t.Fatalf("run after C = %q, want approved", run.State)
	}
	wantApprovedAsSeq(t, super, w.tenantID, "approver", "approver", "approver")
}

func TestDecide_Distinct_TwoEarlierDecidersShareTheFallback(t *testing.T) {
	for _, tc := range []struct {
		name  string
		third func(a, b string) string
	}{
		{"A approves step 2", func(a, b string) string { return a }},
		{"B approves step 2", func(a, b string) string { return b }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			super, app := dbTestPools(t)
			w := newDistinctWorld(t, super, app, "SECAUD-40 shared-fallback", "k0", "k1", "k2")
			a := w.reviewer(t, 0, 2)
			b := w.reviewer(t, 1, 2)

			w.mustApprove(t, a)
			w.mustApprove(t, b)
			if run := w.mustApprove(t, tc.third(a, b)); run.State != "approved" {
				t.Fatalf("run after the third approve = %q, want approved", run.State)
			}
			wantApprovedAsSeq(t, super, w.tenantID, "approver", "approver", "repeat_approved")
		})
	}
}

// A rejected row cannot sit on an open run through the API; seeded, it must still make no earlier approver.
func TestDecide_Distinct_OnlyAnApprovedDecisionMakesAnEarlierApprover(t *testing.T) {
	super, app := dbTestPools(t)
	w := newDistinctWorld(t, super, app, "SECAUD-40 only-approved", "k0", "k1")
	a := w.reviewer(t, 0, 1)
	w.reviewer(t, 0, 1) // B reaches and holds both roles
	seedApprovalDecision(t, super, w.tenantID, w.runID, oneApprovalStep(t, super, w.runID, 0), "rejected", a, nil)
	probe := CreatorProbe{InvoiceID: w.invoiceID, RoleKey: ptr("k0")}

	if got := readStanding(t, app, w.tenantID, a, probe)[w.invoiceID]; got.EarlierApprover {
		t.Errorf("standing after a rejected row = %+v, want EarlierApprover false", got)
	}
	w.mustApprove(t, a)
	if got := readStanding(t, app, w.tenantID, a, probe)[w.invoiceID]; !got.EarlierApprover {
		t.Errorf("standing after A's approval = %+v, want EarlierApprover true", got)
	}
}

// B decided on a closed run of this invoice and on another invoice's open run: neither makes B a decider here.
func TestDecide_Distinct_DecidersOfAClosedRunOrAnotherInvoiceStayFresh(t *testing.T) {
	super, app := dbTestPools(t)
	w := newDistinctWorld(t, super, app, "SECAUD-40 stale-deciders", "k0", "k1")
	a := w.reviewer(t, 0, 1)
	b := w.reviewer(t, 0, 1)
	i2 := seedInvoice(t, super, w.tenantID, w.entityID, "distinct-stale-i2")
	setInvoiceStatus(t, super, i2, "validated")
	if _, err := arm(t, app, w.tenantID, i2, "fp-stale-i2", "fixture-arm"); err != nil {
		t.Fatalf("arm I2: %v", err)
	}
	if _, err := approve(t, app, w.tenantID, b, i2, nil); err != nil {
		t.Fatalf("B approves step 0 of I2: %v", err)
	}

	w.mustApprove(t, b)
	if run, err := reject(t, app, w.tenantID, a, w.invoiceID, ptr("wrong buyer")); err != nil || run.State != "rejected" {
		t.Fatalf("A rejects step 1 = %q, %v, want rejected", run.State, err)
	}
	if _, err := arm(t, app, w.tenantID, w.invoiceID, "fp-stale-2", "fixture-arm"); err != nil {
		t.Fatalf("arm the new run: %v", err)
	}
	if run := w.mustApprove(t, a); run.State != "open" {
		t.Fatalf("run after A approves step 0 of the new run = %q, want open", run.State)
	}
	_, err := approve(t, app, w.tenantID, a, w.invoiceID, nil)
	wantRefused(t, err, earlierSentence)
}

// A delegate of step 0 who decided nothing is no holder of step 1.
func TestDecide_Distinct_AStepZeroDelegateWhoDecidedNothingIsNoHolderOfStepOne(t *testing.T) {
	super, app := dbTestPools(t)
	w := newDistinctWorld(t, super, app, "SECAUD-40 delegate-not-forward", "k0", "k1")
	b := w.reviewer(t, 0)
	c := w.reviewer(t, 1)
	d := w.reviewer(t)
	setStepDelegation(t, super, oneApprovalStep(t, super, w.runID, 0), true, &d)

	w.mustApprove(t, b)
	_, err := approve(t, app, w.tenantID, d, w.invoiceID, nil)
	if !errors.Is(err, ErrNotRoleHolder) {
		t.Fatalf("D on step 1 err = %v, want ErrNotRoleHolder", err)
	}
	if run := w.mustApprove(t, c); run.State != "approved" {
		t.Fatalf("run after C = %q, want approved", run.State)
	}
}
