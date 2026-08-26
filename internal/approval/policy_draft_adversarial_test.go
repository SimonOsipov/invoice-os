package approval

// Adversarial coverage for Store.PutDraft, beyond the acceptance specs in
// policy_draft_test.go. Four of these close mutations that survived the acceptance set:
// the version list the response carries, the admin gate, the copy-don't-alias
// normalisation, and the FOR UPDATE that serialises concurrent forks.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- the version list the PUT response carries ---------------------------------

// versionPairs renders a version list as "N:sealed:active" strings, so a mismatch names
// the row rather than printing two pointer-laden structs.
func versionPairs(vs []PolicyVersion) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, fmt.Sprintf("v%d sealed=%v active=%v", v.Version, v.Sealed, v.IsActive))
	}
	return out
}

// TestPutDraft_ResponseVersionsMatchTheNextGet: the PUT answers a whole Policy, and 07
// renders it without re-fetching, so a version list that disagrees with the next GET is
// invisible until the SPA reloads. The fork fixture is what makes this bite — the list
// must carry the new v2 AND the sealed v1, newest first.
func TestPutDraft_ResponseVersionsMatchTheNextGet(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-response-versions")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	seedSealedActiveV1(t, super, tenantID, policyID)
	store := NewStore(app, stubFingerprinter, nil)

	put, err := store.PutDraft(c, policyID, nil, nil, approvalStep("engagement-partner"))
	if err != nil {
		t.Fatalf("PutDraft: %v", err)
	}

	want := []string{"v2 sealed=false active=false", "v1 sealed=true active=true"}
	if got := versionPairs(put.Versions); !reflect.DeepEqual(got, want) {
		t.Errorf("PUT response versions = %v, want %v — newest first, the forked draft and the sealed v1", got, want)
	}

	got, err := store.GetPolicy(c, policyID)
	if err != nil {
		t.Fatalf("GetPolicy after the PUT: %v", err)
	}
	if !reflect.DeepEqual(put.Versions, got.Versions) {
		t.Errorf("the PUT response and the next GET disagree on versions:\n  PUT %v\n  GET %v",
			versionPairs(put.Versions), versionPairs(got.Versions))
	}
	if put.Versions[0].PublishedAt != nil || put.Versions[0].PublishedBy != nil {
		t.Errorf("the forked draft reports published_at/by = (%v, %v), want both nil",
			put.Versions[0].PublishedAt, put.Versions[0].PublishedBy)
	}
	// The sealed row's publication metadata rides along, so the assertion above is not
	// passing against a list whose every field is nil.
	if put.Versions[1].PublishedAt == nil || put.Versions[1].PublishedBy == nil {
		t.Errorf("the sealed v1 reports published_at/by = (%v, %v), want both set by the fixture",
			put.Versions[1].PublishedAt, put.Versions[1].PublishedBy)
	}

	// A rewrite of the SAME draft leaves the list alone: no version is minted.
	again, err := store.PutDraft(c, policyID, nil, nil, approvalStep("tax-reviewer"))
	if err != nil {
		t.Fatalf("second PutDraft: %v", err)
	}
	if got := versionPairs(again.Versions); !reflect.DeepEqual(got, want) {
		t.Errorf("versions after a second PUT = %v, want the unchanged %v — rewriting a draft mints nothing", got, want)
	}
}

// --- the admin gate -------------------------------------------------------------

// TestPutDraft_PermissionCheckedBeforeAnyPolicyRow: PutDraft is a write, so a preparer and
// a suspended admin are both ErrNotPermitted — and the check runs before any policy table
// is touched, which is what stops the refusal from doubling as an existence oracle for a
// policy the caller may not read.
func TestPutDraft_PermissionCheckedBeforeAnyPolicyRow(t *testing.T) {
	super, app := dbTestPools(t)

	// The request seam refuses a non-active caller before the store reads anything
	// (db.WithinRequestTenantTxOpts); an ACTIVE preparer is still a role refusal.
	for _, tc := range []struct {
		name   string
		role   string
		status string
		want   error
		bySeam bool
	}{
		{"preparer", "preparer", "active", ErrNotPermitted, false},
		{"suspended admin", "admin", "suspended", db.ErrNotActiveMember, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tenantID := policyTenant(t, super, "APPR-05 put-permission "+tc.name)
			c, _ := callerCtx(t, super, tenantID, tc.role, tc.status)
			policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
			versionID := seedDraftWithSteps(t, super, tenantID, policyID, 2)
			before := readStoredSteps(t, super, versionID)

			traced, rec := tracedAppPool(t)
			rec.reset()
			_, err := NewStore(traced, stubFingerprinter, nil).PutDraft(c, policyID, ptr("Hijacked"), nil, approvalStep("engagement-partner"))
			if !errors.Is(err, tc.want) {
				t.Errorf("PutDraft as a %s: err = %v, want %v", tc.name, err, tc.want)
			}
			if tc.bySeam {
				if sql := rec.seamMentioning("FROM memberships"); len(sql) == 0 {
					t.Error("no memberships statement was issued — the seam gate did not run")
				}
				if sql := rec.mentioning("memberships"); len(sql) != 0 {
					t.Errorf("the store read memberships itself despite the seam's refusal:\n%v", sql)
				}
			} else if sql := rec.mentioning("memberships"); len(sql) == 0 {
				t.Error("no memberships statement was issued — requireActiveAdmin did not run")
			}
			if sql := rec.mentioning("approval_polic"); len(sql) != 0 {
				t.Errorf("a policy-table statement ran despite the refusal, so the permission check is not first:\n%v", sql)
			}
			if got := storedPolicyName(t, super, policyID); got != "Sign-off" {
				t.Errorf("stored name = %q, want the untouched %q", got, "Sign-off")
			}
			if got := readStoredSteps(t, super, versionID); !reflect.DeepEqual(got, before) {
				t.Errorf("the step set changed despite the refusal:\n  before %v\n  after  %v", before, got)
			}
			if n := auditCount(t, super, tenantID, "approval_policy.updated"); n != 0 {
				t.Errorf("approval_policy.updated audit rows = %d, want 0", n)
			}

			// Control: an active admin of the same tenant writes, so the refusal is not a
			// store that refuses everyone.
			admin, _ := activeAdmin(t, super, tenantID)
			if _, err := NewStore(app, stubFingerprinter, nil).PutDraft(admin, policyID, nil, nil, approvalStep("engagement-partner")); err != nil {
				t.Fatalf("PutDraft as an active admin: %v — the refusal above is vacuous unless this succeeds", err)
			}
		})
	}
}

// --- copy, don't alias ----------------------------------------------------------

// TestPutDraft_NormalizersLeaveTheCallersPointeesAlone: both normalizers reassign the
// PARAMETER to a fresh local (UpdateRole's precedent), never write through the caller's
// pointer. 07 decodes putDraftRequest and hands over &req.Name — a store that normalised
// in place would silently rewrite the handler's own request struct, which is a
// caller-visible side effect no round trip through the database can show.
func TestPutDraft_NormalizersLeaveTheCallersPointeesAlone(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-no-aliasing")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	seedDraftWithSteps(t, super, tenantID, policyID, 1)

	name, scope := "  Renamed  ", ""
	if _, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, &name, &scope, approvalStep("engagement-partner")); err != nil {
		t.Fatalf("PutDraft: %v", err)
	}

	// The stored values ARE normalised, so the assertions below are about aliasing and
	// not about a store that normalised nothing.
	if got := storedPolicyName(t, super, policyID); got != "Renamed" {
		t.Fatalf("stored name = %q, want the trimmed %q", got, "Renamed")
	}
	if got := storedPolicyScope(t, super, policyID); got != scopeAllInvoices {
		t.Fatalf("stored scope = %q, want %q", got, scopeAllInvoices)
	}

	if name != "  Renamed  " {
		t.Errorf("the caller's name variable reads %q, want the untouched %q — the normalizer wrote through the pointer", name, "  Renamed  ")
	}
	if scope != "" {
		t.Errorf("the caller's scope variable reads %q, want the untouched empty string — the normalizer wrote through the pointer", scope)
	}
}

// --- the row lock ---------------------------------------------------------------

// TestPutDraft_ConcurrentPutsConvergeOnOneDraft: concurrent PUTs against a policy with NO
// open draft must all succeed on ONE fork, never race approval_policy_versions_one_draft
// into a raw 23505. The serialisation is a write lock on the policy row, which PutDraft
// takes twice over — the FOR UPDATE and then the unconditional coalesced UPDATE. Removing
// only the FOR UPDATE therefore does not break this; publish and delete hold no such lock
// unless they ask for it.
func TestPutDraft_ConcurrentPutsConvergeOnOneDraft(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-concurrent-fork")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	v1 := seedSealedActiveV1(t, super, tenantID, policyID)
	store := NewStore(app, stubFingerprinter, nil)

	const n = 6
	errs := make([]error, n)
	got := make([]Policy, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			got[i], errs[i] = store.PutDraft(c, policyID, nil, nil, approvalStep(fmt.Sprintf("racer-%d", i)))
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent PutDraft %d: %v (SQLSTATE %q) — every caller must serialise on the "+
				"policy row, not race approval_policy_versions_one_draft", i, err, pgCode(err))
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	versions := versionRows(t, super, policyID)
	if len(versions) != 2 {
		t.Fatalf("policy has %d versions, want 2 — %d concurrent PUTs fork exactly one: %+v", len(versions), n, versions)
	}
	if versions[0].ID != v1 || !versions[0].Sealed {
		t.Errorf("v1 = %+v, want the seeded sealed %s", versions[0], v1)
	}
	draft := versions[1]
	if draft.Version != 2 || draft.Sealed {
		t.Errorf("the forked version = %+v, want (version 2, sealed false)", draft)
	}
	for i, p := range got {
		if p.Version != 2 {
			t.Errorf("caller %d saw version %d, want the one shared draft 2", i, p.Version)
		}
	}

	// The last writer wins the whole tree: one step, from exactly one of the racers.
	steps := readStoredSteps(t, super, draft.ID)
	if len(steps) != 1 {
		t.Fatalf("the draft holds %d steps, want 1 — each PUT is a whole-tree replace: %v", len(steps), steps)
	}
	if steps[0].WorkflowRoleKey == nil {
		t.Fatalf("the surviving step names no role: %v", steps[0])
	}
	survivors := map[string]bool{}
	for i := 0; i < n; i++ {
		survivors[fmt.Sprintf("racer-%d", i)] = true
	}
	if !survivors[*steps[0].WorkflowRoleKey] {
		t.Errorf("the surviving step names %q, want one of the racers", *steps[0].WorkflowRoleKey)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.updated"); n != 6 {
		t.Errorf("approval_policy.updated audit rows = %d, want 6 — every PUT that returned nil audits", n)
	}
	if got := readStoredSteps(t, super, v1); len(got) != 2 {
		t.Errorf("the sealed v1 holds %d steps, want its original 2: %v", len(got), got)
	}
}

// --- D7: the draft resolution is NOT sealed, never "the active version" ---------

// sealedInactiveVersion seals a version WITHOUT activating it — the shape D7 turns on.
func sealedInactiveVersion(t *testing.T, super *pgxpool.Pool, tenantID, policyID string, version int, marker string) string {
	t.Helper()
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, version)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "notify", NotifyTarget: ptr(marker), NotifyChannel: ptr("email"),
	})
	sealApprovalPolicyVersion(t, super, versionID)
	return versionID
}

// TestPutDraft_ForksAboveTheNewestSealedWhenNoVersionIsActive: policy A holds v1 and v2,
// both sealed and NEITHER active, because approval_policy_versions_one_active spans the
// TENANT and policy B holds that slot. "The active version" of A does not exist; the
// newest sealed one is v2. The fork must number max(version)+1 = 3 — a store deriving the
// number from the active version, or defaulting to 1, breaks only on this shape.
func TestPutDraft_ForksAboveTheNewestSealedWhenNoVersionIsActive(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-d7-no-active")
	c, _ := activeAdmin(t, super, tenantID)

	holder := seedApprovalPolicy(t, super, tenantID, "Holds the tenant's active slot")
	holderV1 := seedSealedActiveV1(t, super, tenantID, holder)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	v1 := sealedInactiveVersion(t, super, tenantID, policyID, 1, "v1-must-not-be-copied")
	v2 := sealedInactiveVersion(t, super, tenantID, policyID, 2, "v2-must-not-be-copied")

	// The fixture's whole point: this policy has no active version at all.
	for _, v := range versionRows(t, super, policyID) {
		if v.IsActive {
			t.Fatalf("fixture: version %+v is active, so D7's divergence is not under test", v)
		}
	}

	got, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, nil, approvalStep("engagement-partner"))
	if err != nil {
		t.Fatalf("PutDraft: %v (SQLSTATE %q)", err, pgCode(err))
	}

	versions := versionRows(t, super, policyID)
	if len(versions) != 3 {
		t.Fatalf("policy has %d versions, want 3: %+v", len(versions), versions)
	}
	draft := versions[2]
	if draft.Version != 3 || draft.Sealed || draft.IsActive {
		t.Errorf("the forked version = %+v, want (version 3, sealed false, is_active false) — max(version)+1, "+
			"not a number derived from an active version this policy does not have", draft)
	}
	if got.Version != 3 || got.Status != "draft" {
		t.Errorf("returned policy = (version %d, status %q), want (3, draft)", got.Version, got.Status)
	}

	newSteps := readStoredSteps(t, super, draft.ID)
	if len(newSteps) != 1 {
		t.Fatalf("v3 holds %d steps, want exactly the 1 submitted: %v", len(newSteps), newSteps)
	}
	if newSteps[0].NotifyTarget != nil {
		t.Errorf("a sealed step was copied into the fork: %v", newSteps[0])
	}
	for _, untouched := range []struct {
		label string
		id    string
		steps int
	}{{"v1", v1, 1}, {"v2", v2, 1}, {"policy B's v1", holderV1, 2}} {
		if got := readStoredSteps(t, super, untouched.id); len(got) != untouched.steps {
			t.Errorf("%s holds %d steps after the PUT, want its original %d: %v",
				untouched.label, len(got), untouched.steps, got)
		}
	}
	// The tenant's one active slot is still policy B's.
	for _, v := range versionRows(t, super, holder) {
		if v.ID == holderV1 && !v.IsActive {
			t.Errorf("policy B's active version was deactivated by a PUT against policy A: %+v", v)
		}
	}
}

// --- whole-request writes -------------------------------------------------------

// TestPutDraft_RenamesAndRewritesInOneCall: name, scope and the tree all move in one
// transaction, against a policy that has to FORK first. One audit row, not one per field.
func TestPutDraft_RenamesAndRewritesInOneCall(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-rename-and-rewrite")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Original name")
	v1 := seedSealedActiveV1(t, super, tenantID, policyID)

	tree := []stepInput{
		{Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("500.25"),
			Then: []stepInput{{Kind: "approval", WorkflowRoleKey: ptr("engagement-partner"), SLAHours: ptr(24)}}},
		{Kind: "notify", NotifyTarget: ptr("finance@example.com"), NotifyChannel: ptr("email")},
	}

	got, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, ptr("  Renamed in the same call  "), ptr(""), tree)
	if err != nil {
		t.Fatalf("PutDraft: %v", err)
	}

	if stored := storedPolicyName(t, super, policyID); stored != "Renamed in the same call" {
		t.Errorf("stored name = %q, want the trimmed rename", stored)
	}
	if stored := storedPolicyScope(t, super, policyID); stored != scopeAllInvoices {
		t.Errorf("stored scope = %q, want %q", stored, scopeAllInvoices)
	}
	if got.Name != "Renamed in the same call" || got.Scope != scopeAllInvoices {
		t.Errorf("returned policy = (%q, %q), want the renamed policy at the default scope", got.Name, got.Scope)
	}

	draft := openDraft(t, super, policyID)
	if draft.Version != 2 {
		t.Fatalf("the open draft is version %d, want the forked 2", draft.Version)
	}
	stored := readStoredSteps(t, super, draft.ID)
	if len(stored) != 3 {
		t.Fatalf("the draft holds %d steps, want 3 (a condition, its then member and a notify): %v", len(stored), stored)
	}
	roots := lane(stored, "", "")
	if len(roots) != 2 || roots[0].Kind != "condition" || roots[1].Kind != "notify" {
		t.Fatalf("root lane = %v, want a condition then a notify", roots)
	}
	if roots[0].CondAmount == nil || *roots[0].CondAmount != "500.25" {
		t.Errorf("condition cond_amount = %v, want %q", roots[0].CondAmount, "500.25")
	}
	if then := lane(stored, roots[0].ID, "then"); len(then) != 1 || then[0].SLAHours == nil || *then[0].SLAHours != 24 {
		t.Errorf("then lane = %v, want one approval at sla_hours 24", then)
	}

	if n := auditCount(t, super, tenantID, "approval_policy.updated"); n != 1 {
		t.Errorf("approval_policy.updated audit rows = %d, want 1 — a PUT is one event, not one per field", n)
	}
	if steps := readStoredSteps(t, super, v1); len(steps) != 2 {
		t.Errorf("the sealed v1 holds %d steps, want its original 2: %v", len(steps), steps)
	}
}

// stripStepIDs blanks every id in a nested tree, so two trees can be compared on
// structure and content alone.
func stripStepIDs(steps []Step) []Step {
	out := make([]Step, 0, len(steps))
	for _, s := range steps {
		s.ID = ""
		s.Then = stripStepIDs(s.Then)
		s.Else = stripStepIDs(s.Else)
		out = append(out, s)
	}
	return out
}

// TestPutDraft_RepeatedPutsChurnIdsButKeepTheTree: the id churn AC-4 pins must not be a
// tree churn. Three identical PUTs produce three disjoint id sets and one unchanged
// structure — the id is the only thing that moves, at every depth.
func TestPutDraft_RepeatedPutsChurnIdsButKeepTheTree(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-id-churn")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	store := NewStore(app, stubFingerprinter, nil)

	tree := []stepInput{{
		Kind: "condition", CondOp: ptr("<="), CondAmount: ptr("42.00"),
		Then: []stepInput{{Kind: "approval", WorkflowRoleKey: ptr("engagement-partner")}},
		Else: []stepInput{{Kind: "notify", NotifyTarget: ptr("ops@example.com"), NotifyChannel: ptr("slack")}},
	}}

	var wantShape []Step
	seen := map[string]int{}
	for round := 0; round < 3; round++ {
		got, err := store.PutDraft(c, policyID, nil, nil, tree)
		if err != nil {
			t.Fatalf("PutDraft round %d: %v", round, err)
		}
		shape := stripStepIDs(got.Steps)
		if round == 0 {
			wantShape = shape
		} else if !reflect.DeepEqual(shape, wantShape) {
			t.Errorf("round %d rebuilt a different tree:\n  round 0 %+v\n  round %d %+v", round, wantShape, round, shape)
		}
		stored := readStoredSteps(t, super, versionID)
		if len(stored) != 3 {
			t.Fatalf("round %d: the draft holds %d steps, want 3: %v", round, len(stored), stored)
		}
		for _, s := range stored {
			seen[s.ID]++
		}
		if n := len(versionRows(t, super, policyID)); n != 1 {
			t.Fatalf("round %d: policy has %d versions, want the 1 seeded", round, n)
		}
	}

	if len(seen) != 9 {
		t.Errorf("three rounds of three steps produced %d distinct ids, want 9 — every save mints fresh ones", len(seen))
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("step id %s appeared in %d rounds, want 1", id, count)
		}
	}
}

// --- the tree's structural edges ------------------------------------------------

// TestPutDraft_DepthLimitTreeRoundTripsAndDeeperIsRefused: depth two is the Go-side
// invariant validateTree owns — approval_policy_steps_depth_cap forbids a condition
// CHILD, which is not the same statement. The legal shape is a condition at the root with
// members in both lanes; one level deeper is ErrValidation above the transaction, so
// nothing is written and no version is forked.
func TestPutDraft_DepthLimitTreeRoundTripsAndDeeperIsRefused(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-depth-limit")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	store := NewStore(app, stubFingerprinter, nil)

	atLimit := []stepInput{{
		Kind: "condition", CondOp: ptr(">="), CondAmount: ptr("1000.00"),
		Then: []stepInput{{Kind: "approval", WorkflowRoleKey: ptr("engagement-partner")}},
		Else: []stepInput{{Kind: "autoapprove"}},
	}}
	if _, err := store.PutDraft(c, policyID, nil, nil, atLimit); err != nil {
		t.Fatalf("PutDraft at the depth limit: %v", err)
	}
	stored := readStoredSteps(t, super, versionID)
	if len(stored) != 3 {
		t.Fatalf("the draft holds %d steps, want 3: %v", len(stored), stored)
	}
	atLimitSnapshot := stored

	// One level deeper: a condition nested inside a lane.
	tooDeep := []stepInput{{
		Kind: "condition", CondOp: ptr(">="), CondAmount: ptr("1000.00"),
		Then: []stepInput{{
			Kind: "condition", CondOp: ptr("<"), CondAmount: ptr("50.00"),
			Then: []stepInput{{Kind: "approval", WorkflowRoleKey: ptr("engagement-partner")}},
		}},
	}}
	_, err := store.PutDraft(c, policyID, ptr("Should not land"), nil, tooDeep)
	if !errors.Is(err, ErrValidation) {
		t.Errorf("PutDraft one level past the limit: err = %v, want ErrValidation", err)
	}
	if code := pgCode(err); code != "" {
		t.Errorf("a too-deep tree surfaced a raw Postgres error (SQLSTATE %s) — validateTree sits above the "+
			"transaction so the depth cap is never the thing that refuses", code)
	}
	if got := readStoredSteps(t, super, versionID); !reflect.DeepEqual(got, atLimitSnapshot) {
		t.Errorf("the step set changed despite the refusal:\n  before %v\n  after  %v", atLimitSnapshot, got)
	}
	if got := storedPolicyName(t, super, policyID); got != "Sign-off" {
		t.Errorf("stored name = %q, want the untouched %q — the refusal is above the transaction", got, "Sign-off")
	}
	if n := len(versionRows(t, super, policyID)); n != 1 {
		t.Errorf("policy has %d versions, want the 1 seeded — a refused tree forks nothing", n)
	}
}

// TestPutDraft_ConditionLanesOfDifferentKindsRoundTrip: both lanes non-empty and holding
// DIFFERENT kinds, each carrying only its own kind's columns. The two lanes travel in one
// children batch, so a lane-blind write puts a notify's target on the approval row and a
// tree-shaped read still looks plausible.
func TestPutDraft_ConditionLanesOfDifferentKindsRoundTrip(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-mixed-lanes")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)

	tree := []stepInput{{
		Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("250.00"),
		Then: []stepInput{
			{Kind: "approval", WorkflowRoleKey: ptr("engagement-partner"), SLAHours: ptr(72)},
			{Kind: "autoapprove"},
		},
		Else: []stepInput{
			{Kind: "notify", NotifyTarget: ptr("finance@example.com"), NotifyChannel: ptr("email")},
		},
	}}

	got, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, nil, tree)
	if err != nil {
		t.Fatalf("PutDraft: %v", err)
	}

	stored := readStoredSteps(t, super, versionID)
	if len(stored) != 4 {
		t.Fatalf("the draft holds %d steps, want 4: %v", len(stored), stored)
	}
	roots := lane(stored, "", "")
	if len(roots) != 1 {
		t.Fatalf("root lane = %v, want one condition", roots)
	}
	cond := roots[0]

	thenLane := lane(stored, cond.ID, "then")
	if len(thenLane) != 2 {
		t.Fatalf("then lane has %d members, want 2: %v", len(thenLane), thenLane)
	}
	assertStoredStep(t, "then 0 (approval)", thenLane[0], storedStep{
		ParentStepID: &cond.ID, Branch: ptr("then"), Ord: 0, Kind: "approval",
		WorkflowRoleKey: ptr("engagement-partner"), SLAHours: ptr(72),
	})
	assertStoredStep(t, "then 1 (autoapprove, every optional column NULL)", thenLane[1], storedStep{
		ParentStepID: &cond.ID, Branch: ptr("then"), Ord: 1, Kind: "autoapprove",
	})

	elseLane := lane(stored, cond.ID, "else")
	if len(elseLane) != 1 {
		t.Fatalf("else lane has %d members, want 1: %v", len(elseLane), elseLane)
	}
	assertStoredStep(t, "else 0 (notify)", elseLane[0], storedStep{
		ParentStepID: &cond.ID, Branch: ptr("else"), Ord: 0, Kind: "notify",
		NotifyTarget: ptr("finance@example.com"), NotifyChannel: ptr("email"),
	})

	// The nested response keeps the lanes apart too.
	if len(got.Steps) != 1 {
		t.Fatalf("returned tree = %+v, want one condition", got.Steps)
	}
	c0 := got.Steps[0]
	if len(c0.Then) != 2 || len(c0.Else) != 1 {
		t.Fatalf("returned condition has (%d then, %d else), want (2, 1)", len(c0.Then), len(c0.Else))
	}
	if c0.Then[0].Kind != "approval" || c0.Then[1].Kind != "autoapprove" || c0.Else[0].Kind != "notify" {
		t.Errorf("returned lane kinds = (then %q, %q; else %q), want (approval, autoapprove; notify)",
			c0.Then[0].Kind, c0.Then[1].Kind, c0.Else[0].Kind)
	}
	if c0.Else[0].WorkflowRoleKey != nil || c0.Then[0].NotifyTarget != nil {
		t.Errorf("a lane's columns leaked across: else notify carries role %v, then approval carries target %v",
			c0.Else[0].WorkflowRoleKey, c0.Then[0].NotifyTarget)
	}
}

// --- NUL bytes in the text columns -----------------------------------------------

// TestPutDraft_NULInAnyTextFieldIsRefusedNotA500: a NUL is the one byte text will not
// take — Postgres raises 22021, which carries no constraint name, so policyStatusForErr
// falls to its default and answers 500 on input a client chose. PUT is the wider door of
// the two: it carries the name AND the whole step tree. Every refusal is above the
// transaction, so the seeded draft, the name and the version count are all untouched.
func TestPutDraft_NULInAnyTextFieldIsRefusedNotA500(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app, stubFingerprinter, nil)

	for _, tc := range []struct {
		field string
		name  *string
		steps []stepInput
	}{
		{"name", ptr("Sign\x00off"), approvalStep("engagement-partner")},
		{"workflow_role_key", nil, []stepInput{approvalIn("engagement\x00partner")}},
		{"notify_target", nil, []stepInput{notifyIn("prep\x00arer", "email")}},
		{"notify_channel", nil, []stepInput{notifyIn("preparer", "em\x00ail")}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			tenantID := policyTenant(t, super, "APPR-05 put-nul "+tc.field)
			c, _ := activeAdmin(t, super, tenantID)
			policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
			versionID := seedDraftWithSteps(t, super, tenantID, policyID, 1)
			seeded := readStoredSteps(t, super, versionID)

			_, err := store.PutDraft(c, policyID, tc.name, nil, tc.steps)
			if !errors.Is(err, ErrValidation) {
				t.Errorf("PutDraft with a NUL in %s: err = %v, want ErrValidation", tc.field, err)
			}
			if code := pgCode(err); code != "" {
				t.Errorf("PutDraft with a NUL in %s surfaced a raw Postgres error (SQLSTATE %s) — "+
					"policyStatusForErr maps sentinels only, so this answers 500 instead of 400", tc.field, code)
			}
			if got := storedPolicyName(t, super, policyID); got != "Sign-off" {
				t.Errorf("stored name = %q, want the untouched %q", got, "Sign-off")
			}
			if got := readStoredSteps(t, super, versionID); !reflect.DeepEqual(got, seeded) {
				t.Errorf("the step set changed despite the refusal:\n  before %v\n  after  %v", seeded, got)
			}
			if n := len(versionRows(t, super, policyID)); n != 1 {
				t.Errorf("policy has %d versions, want the 1 seeded — a refused write forks nothing", n)
			}
			if n := auditCount(t, super, tenantID, "approval_policy.updated"); n != 0 {
				t.Errorf("approval_policy.updated audit rows = %d, want 0", n)
			}
		})
	}

	t.Run("control: the same call without the NULs still writes", func(t *testing.T) {
		tenantID := policyTenant(t, super, "APPR-05 put-nul-control")
		c, _ := activeAdmin(t, super, tenantID)
		policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
		versionID := seedDraftWithSteps(t, super, tenantID, policyID, 1)

		if _, err := store.PutDraft(c, policyID, ptr("Signoff"), nil,
			[]stepInput{approvalIn("engagementpartner"), notifyIn("preparer", "email")}); err != nil {
			t.Fatalf("PutDraft: %v — the refusals above are vacuous unless this succeeds", err)
		}
		if got := storedPolicyName(t, super, policyID); got != "Signoff" {
			t.Errorf("stored name = %q, want %q", got, "Signoff")
		}
		if steps := readStoredSteps(t, super, versionID); len(steps) != 2 {
			t.Errorf("draft holds %v, want the two submitted steps", steps)
		}
	})
}

// TestPutDraft_ForeignCondOpOnANonConditionIsRefusedNotA500: cond_op is written whatever
// the kind carries it and nothing above the column checked it outside case "condition", so
// a notify step naming a foreign operator reached SQL and raised 23514 on
// approval_policy_steps_cond_op_check — no sentinel, a 500 on input a client chose. The
// control is the same step with a legal operator, which the column accepts on any kind.
func TestPutDraft_ForeignCondOpOnANonConditionIsRefusedNotA500(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app, stubFingerprinter, nil)

	refused := notifyIn("preparer", "email")
	refused.CondOp = ptr("BOOM")

	t.Run("refused above the transaction", func(t *testing.T) {
		tenantID := policyTenant(t, super, "APPR-05 put-foreign-cond-op")
		c, _ := activeAdmin(t, super, tenantID)
		policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
		versionID := seedDraftWithSteps(t, super, tenantID, policyID, 1)
		seeded := readStoredSteps(t, super, versionID)

		_, err := store.PutDraft(c, policyID, nil, nil, []stepInput{refused})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("PutDraft err = %v, want ErrValidation", err)
		}
		if code := pgCode(err); code != "" {
			t.Errorf("PutDraft surfaced a raw Postgres error (SQLSTATE %s) — policyStatusForErr maps "+
				"sentinels only, so this answers 500 instead of 400", code)
		}
		if got := readStoredSteps(t, super, versionID); !reflect.DeepEqual(got, seeded) {
			t.Errorf("the step set changed despite the refusal:\n  before %v\n  after  %v", seeded, got)
		}
		if n := len(versionRows(t, super, policyID)); n != 1 {
			t.Errorf("policy has %d versions, want the 1 seeded — a refused write forks nothing", n)
		}
		if n := auditCount(t, super, tenantID, "approval_policy.updated"); n != 0 {
			t.Errorf("approval_policy.updated audit rows = %d, want 0", n)
		}
	})

	t.Run("control: a legal cond_op on the same notify step still writes", func(t *testing.T) {
		tenantID := policyTenant(t, super, "APPR-05 put-foreign-cond-op-control")
		c, _ := activeAdmin(t, super, tenantID)
		policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
		versionID := seedDraftWithSteps(t, super, tenantID, policyID, 1)

		legal := refused
		legal.CondOp = ptr(">")
		if _, err := store.PutDraft(c, policyID, nil, nil, []stepInput{legal}); err != nil {
			t.Fatalf("PutDraft: %v — the refusal above is vacuous unless this succeeds, and the "+
				"column accepts an operator on any kind", err)
		}
		steps := readStoredSteps(t, super, versionID)
		if len(steps) != 1 || steps[0].Kind != "notify" || steps[0].CondOp == nil || *steps[0].CondOp != ">" {
			t.Errorf("draft holds %v, want the one notify step carrying cond_op >", steps)
		}
	})
}

// --- teardown residue -----------------------------------------------------------

// TestPutDraft_LeavesNoPolicyRowsBehind: everything a fork writes must carry the caller's
// tenant_id, or teardownSealedApprovalFixture's bottom-up delete cannot reach it and the
// tenant leaks permanently (DELETE FROM tenants raises 23001 under a sealed version).
func TestPutDraft_LeavesNoPolicyRowsBehind(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-residue")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	seedSealedActiveV1(t, super, tenantID, policyID)

	if _, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, nil, approvalStep("engagement-partner")); err != nil {
		t.Fatalf("PutDraft: %v", err)
	}

	// Everything the PUT wrote is under this tenant, so teardownSealedApprovalFixture's
	// bottom-up delete reaches all of it.
	for table, want := range map[string]int{
		"approval_policies": 1, "approval_policy_versions": 2, "approval_policy_steps": 3,
	} {
		if got := rowCount(t, super, table, tenantID); got != want {
			t.Errorf("%s holds %d rows under the tenant, want %d — a row outside the tenant is uncollectable",
				table, got, want)
		}
	}
	var orphans int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM approval_policy_versions v
		  WHERE v.tenant_id = $1
		    AND NOT EXISTS (SELECT 1 FROM approval_policies p WHERE p.id = v.policy_id)`,
		tenantID).Scan(&orphans); err != nil {
		t.Fatalf("count orphaned versions: %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d forked versions have no policy row", orphans)
	}
}
