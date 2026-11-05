package approval

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Clause names used in failure messages: check 1 = last live policy's top version is scoped;
// check 2 = last policy in force has a scoped active version; check 3 = the last live policy
// leaves the last place while a scoped policy exists (DeletePolicy only).

// seedPolicyShape seeds a live policy with a stepless v1: scoped (document type) or catch-all,
// sealed+active or an open draft. Policies sit in the order they are seeded.
func seedPolicyShape(t *testing.T, super *pgxpool.Pool, tenantID, name string, scoped, active bool) (policyID, versionID string) {
	t.Helper()
	policyID = seedApprovalPolicy(t, super, tenantID, name)
	versionID = seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	if scoped {
		setVersionScopes(t, super, versionID, `scope_document_types = '{credit_note}'`)
	}
	if active {
		activateApprovalPolicyVersion(t, super, versionID)
	}
	return policyID, versionID
}

func listedNames(t *testing.T, store *Store, c context.Context) []string {
	t.Helper()
	list, err := store.ListPolicies(c)
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	names := make([]string, 0, len(list))
	for _, p := range list {
		names = append(names, p.Name)
	}
	return names
}

func policyIsLive(t *testing.T, super *pgxpool.Pool, policyID string) bool {
	t.Helper()
	return policyDeletedAt(t, super, policyID) == nil
}

func policyHasActiveVersion(t *testing.T, super *pgxpool.Pool, policyID string) bool {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM approval_policy_versions WHERE policy_id = $1 AND is_active`, policyID).Scan(&n); err != nil {
		t.Fatalf("count active versions of %s: %v", policyID, err)
	}
	return n > 0
}

// assertLiveAndActive is the "refused delete changed nothing" read-back.
func assertLiveAndActive(t *testing.T, super *pgxpool.Pool, label, policyID string) {
	t.Helper()
	if !policyIsLive(t, super, policyID) {
		t.Errorf("%s was soft-deleted by a refused delete", label)
	}
	if !policyHasActiveVersion(t, super, policyID) {
		t.Errorf("%s lost its active version in a refused delete", label)
	}
}

// lastInForceIsScoped reads the last live policy that has an active version, by position.
func lastInForceIsScoped(t *testing.T, super *pgxpool.Pool, tenantID string) (scoped, found bool) {
	t.Helper()
	err := super.QueryRow(context.Background(),
		`SELECT (cardinality(v.scope_document_types) > 0 OR cardinality(v.scope_entity_ids) > 0
		      OR cardinality(v.scope_workflow_role_ids) > 0 OR v.scope_amount_currency IS NOT NULL)
		   FROM approval_policies p
		   JOIN approval_policy_versions v ON v.policy_id = p.id AND v.is_active
		  WHERE p.tenant_id = $1 AND p.deleted_at IS NULL
		  ORDER BY p.position DESC LIMIT 1`, tenantID).Scan(&scoped)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false
	}
	if err != nil {
		t.Fatalf("read the last policy in force: %v", err)
	}
	return scoped, true
}

func creditNoteScope() *scopeInput { return &scopeInput{DocumentTypes: []string{"credit_note"}} }

// --- AC1: placement of a new policy -------------------------------------------

func TestCreatePolicy_GoesDirectlyAboveTheLastPolicy(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-03 placement")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)
	seedPolicyShape(t, super, tenantID, "X", false, false)
	seedPolicyShape(t, super, tenantID, "C", false, false)

	a, err := store.CreatePolicy(c, "A", nil)
	if err != nil {
		t.Fatalf("CreatePolicy A: %v", err)
	}
	b, err := store.CreatePolicy(c, "B", nil)
	if err != nil {
		t.Fatalf("CreatePolicy B: %v", err)
	}

	if got, want := listedNames(t, store, c), []string{"X", "A", "B", "C"}; !reflect.DeepEqual(got, want) {
		t.Errorf("list order = %v, want %v: a new policy goes directly above the last one and the existing order stays", got, want)
	}
	list, err := store.ListPolicies(c)
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("listed %d policies, want 4", len(list))
	}
	pos := map[string]int{}
	for _, p := range list {
		pos[p.ID] = p.Position
	}
	if pos[a.ID] != a.Position || pos[b.ID] != b.Position {
		t.Errorf("the answer's position (A %d, B %d) differs from the stored one (A %d, B %d)",
			a.Position, b.Position, pos[a.ID], pos[b.ID])
	}
}

// Passes before the guard exists; pins that the swap does not fire when no other live policy exists.
func TestCreatePolicy_FirstLivePolicyKeepsTheTriggerPosition(t *testing.T) {
	super, _ := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-03 first-position")
	c, _ := activeAdmin(t, super, tenantID)
	gone, _ := seedPolicyShape(t, super, tenantID, "Gone", false, false)
	softDeleteApprovalPolicy(t, super, gone)

	traced, rec := tracedAppPool(t)
	rec.reset()
	got, err := NewStore(traced, stubFingerprinter, nil).CreatePolicy(c, "P", nil)
	if err != nil {
		t.Fatalf("CreatePolicy with only a soft-deleted policy: %v", err)
	}
	if got.Position != 2 {
		t.Errorf("position = %d, want 2 (the trigger's max+1 over the deleted position 1)", got.Position)
	}
	for _, s := range rec.mentioning("UPDATE approval_policies") {
		t.Errorf("CreatePolicy swapped positions with no other live policy:\n%s", s)
	}
}

// --- AC2: a scoped create needs a catch-all ----------------------------------

func TestCreatePolicy_ScopedFirstPolicyIs409(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-03 scoped-first")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	_, err := store.CreatePolicy(c, "Scoped", creditNoteScope())
	if !errors.Is(err, ErrPolicyCatchAll) {
		t.Fatalf("scoped create in an empty workspace: err = %v, want ErrPolicyCatchAll (check 1: the last live policy's top version is scoped)", err)
	}
	if n := rowCount(t, super, "approval_policies", tenantID); n != 0 {
		t.Errorf("approval_policies rows = %d, want 0 after a refused create", n)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.created"); n != 0 {
		t.Errorf("approval_policy.created rows = %d, want 0", n)
	}

	// Positive pair: an unscoped first policy is allowed (a workspace with no policy has no catch-all rule).
	if _, err := store.CreatePolicy(c, "Plain", nil); err != nil {
		t.Errorf("unscoped create in an empty workspace: %v, want success", err)
	}
}

func TestCreatePolicy_ScopedPolicyBesideADraftCatchAllIsCreated(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-03 scoped-beside-draft")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)
	seedPolicyShape(t, super, tenantID, "C", false, false)

	if _, err := store.CreatePolicy(c, "S", creditNoteScope()); err != nil {
		t.Fatalf("scoped create beside a draft catch-all: %v, want success", err)
	}
	if got, want := listedNames(t, store, c), []string{"S", "C"}; !reflect.DeepEqual(got, want) {
		t.Errorf("list order = %v, want %v", got, want)
	}
}

// --- AC3: PUT draft ----------------------------------------------------------

func TestPutDraft_ScopeOnTheLastPolicyIs409(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-03 put-last")
	c, _ := activeAdmin(t, super, tenantID)
	seedPolicyShape(t, super, tenantID, "S", true, false)
	lastID := seedApprovalPolicy(t, super, tenantID, "C")
	versionID := seedDraftWithSteps(t, super, tenantID, lastID, 1)
	stepsBefore := readStoredSteps(t, super, versionID)
	if len(stepsBefore) != 1 {
		t.Fatalf("fixture has %d steps, want 1 — the unchanged-steps assertion would be vacuous", len(stepsBefore))
	}

	_, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, lastID, ptr("Renamed"), creditNoteScope(), approvalStep("engagement-partner"))
	if !errors.Is(err, ErrPolicyCatchAll) {
		t.Fatalf("scope on the last policy: err = %v, want ErrPolicyCatchAll (check 1)", err)
	}
	if name := storedPolicyName(t, super, lastID); name != "C" {
		t.Errorf("name = %q, want C unchanged", name)
	}
	if !reflect.DeepEqual(readStoredSteps(t, super, versionID), stepsBefore) {
		t.Error("steps changed on a refused write")
	}
	want := storedScopeColumns{Types: []string{}, Entities: []string{}, RoleIDs: []string{}}
	if got := versionScopeColumns(t, super, versionID); !reflect.DeepEqual(got, want) {
		t.Errorf("scopes = %+v, want the catch-all %+v", got, want)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.updated"); n != 0 {
		t.Errorf("approval_policy.updated rows = %d, want 0", n)
	}
}

func TestPutDraft_ScopeOnAPolicyAboveTheLastSucceeds(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-03 put-above")
	c, _ := activeAdmin(t, super, tenantID)
	upper, versionID := seedPolicyShape(t, super, tenantID, "U", false, false)
	seedPolicyShape(t, super, tenantID, "C", false, false)

	if _, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, upper, nil, creditNoteScope(), approvalStep("engagement-partner")); err != nil {
		t.Fatalf("scope on a policy above the last: %v, want success", err)
	}
	if got := versionScopeColumns(t, super, versionID); !reflect.DeepEqual(got.Types, []string{"credit_note"}) {
		t.Errorf("stored document types = %v, want [credit_note]", got.Types)
	}
}

// --- AC4: deleting the last policy -------------------------------------------

func TestDeletePolicy_LastCatchAllUnderAScopedPolicyIs409(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-03 delete-last")
	c, _ := activeAdmin(t, super, tenantID)
	seedPolicyShape(t, super, tenantID, "S", true, true)
	last, _ := seedPolicyShape(t, super, tenantID, "C", false, true)

	_, err := NewStore(app, stubFingerprinter, nil).DeletePolicy(c, last)
	if !errors.Is(err, ErrPolicyCatchAll) {
		t.Fatalf("delete of the last catch-all under a scoped policy: err = %v, want ErrPolicyCatchAll (check 2: S would be the last in force; check 3 also holds)", err)
	}
	assertLiveAndActive(t, super, "C", last)
	if n := auditCount(t, super, tenantID, "approval_policy.deleted"); n != 0 {
		t.Errorf("approval_policy.deleted rows = %d, want 0", n)
	}
}

// Only check 3 refuses here: after the delete U is last, unscoped and in force, so checks 1 and 2 pass.
func TestDeletePolicy_LastCatchAllWithAnotherCatchAllAboveIs409(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-03 delete-last-pinned")
	c, _ := activeAdmin(t, super, tenantID)
	seedPolicyShape(t, super, tenantID, "S", true, true)
	seedPolicyShape(t, super, tenantID, "U", false, true)
	last, _ := seedPolicyShape(t, super, tenantID, "C", false, true)

	_, err := NewStore(app, stubFingerprinter, nil).DeletePolicy(c, last)
	if !errors.Is(err, ErrPolicyCatchAll) {
		t.Fatalf("delete of the last policy with a catch-all above: err = %v, want ErrPolicyCatchAll (check 3: the last policy is pinned while S is scoped)", err)
	}
	assertLiveAndActive(t, super, "C", last)
	if n := auditCount(t, super, tenantID, "approval_policy.deleted"); n != 0 {
		t.Errorf("approval_policy.deleted rows = %d, want 0", n)
	}
}

// S counts as scoped by its active v1 although its top v2 is a catch-all draft (check 3's active-version leg).
func TestDeletePolicy_LastPolicyUnderAnOnlyActiveScopedPolicyIs409(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-03 delete-last-active-scoped")
	c, _ := activeAdmin(t, super, tenantID)
	s, _ := seedPolicyShape(t, super, tenantID, "S", true, true)
	v2 := seedApprovalPolicyVersionN(t, super, tenantID, s, 2)
	if got := versionScopeColumns(t, super, v2); len(got.Types) != 0 {
		t.Fatalf("fixture v2 is scoped: %+v", got)
	}
	last, _ := seedPolicyShape(t, super, tenantID, "C", false, true)

	_, err := NewStore(app, stubFingerprinter, nil).DeletePolicy(c, last)
	if !errors.Is(err, ErrPolicyCatchAll) {
		t.Fatalf("err = %v, want ErrPolicyCatchAll (check 3: S is scoped by its active version)", err)
	}
	assertLiveAndActive(t, super, "C", last)
}

func TestDeletePolicy_LastPolicyDeletesOnceNoScopeIsLeft(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-03 delete-until-empty")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)
	s, _ := seedPolicyShape(t, super, tenantID, "S", true, true)
	last, _ := seedPolicyShape(t, super, tenantID, "C", false, true)

	if _, err := store.DeletePolicy(c, s); err != nil {
		t.Fatalf("delete of the scoped policy above the last: %v", err)
	}
	if got, want := listedNames(t, store, c), []string{"C"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after deleting S the list = %v, want %v", got, want)
	}
	if _, err := store.DeletePolicy(c, last); err != nil {
		t.Fatalf("delete of the last policy with no scope left: %v", err)
	}
	if policyIsLive(t, super, last) {
		t.Error("C is still live after a successful delete")
	}
}

// --- AC5: deleting above the last ---------------------------------------------

func TestDeletePolicy_InForceCatchAllAboveADraftLastIs409(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-03 delete-in-force")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)
	seedPolicyShape(t, super, tenantID, "S", true, true)
	u, _ := seedPolicyShape(t, super, tenantID, "U", false, true)
	last, _ := seedPolicyShape(t, super, tenantID, "C", false, false)

	_, err := store.DeletePolicy(c, u)
	if !errors.Is(err, ErrPolicyCatchAll) {
		t.Fatalf("delete of the in-force catch-all above a draft-only last: err = %v, want ErrPolicyCatchAll (check 2: S would be the last in force; check 1 passes)", err)
	}
	assertLiveAndActive(t, super, "U", u)
	if n := auditCount(t, super, tenantID, "approval_policy.deleted"); n != 0 {
		t.Errorf("approval_policy.deleted rows = %d, want 0", n)
	}

	if _, err := store.PublishPolicy(c, last); err != nil {
		t.Fatalf("publish C: %v", err)
	}
	if _, err := store.DeletePolicy(c, u); err != nil {
		t.Errorf("delete of U once C is in force: %v, want success", err)
	}
}

// --- AC6: publish -------------------------------------------------------------

func TestPublish_ScopedPolicyWithNoCatchAllInForceIs409(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-03 publish-no-catchall")
	c, _ := activeAdmin(t, super, tenantID)
	entityID := seedBusinessEntity(t, super, tenantID, "Publish Corp")
	bulkSeedValidatedInvoices(t, super, tenantID, entityID, "ca", 1)
	var invoiceID string
	if err := super.QueryRow(context.Background(),
		`SELECT id FROM invoices WHERE tenant_id = $1`, tenantID).Scan(&invoiceID); err != nil {
		t.Fatalf("read the seeded invoice: %v", err)
	}
	s, sv := seedPolicyShape(t, super, tenantID, "S", true, false)
	seedPolicyShape(t, super, tenantID, "C", false, false)

	_, err := NewStore(app, stubFingerprinter, nil).PublishPolicy(c, s)
	if !errors.Is(err, ErrPolicyCatchAll) {
		t.Fatalf("publish of S with no catch-all in force: err = %v, want ErrPolicyCatchAll (check 2: S would be the last in force; check 1 passes)", err)
	}
	var sealed bool
	if err := super.QueryRow(context.Background(),
		`SELECT sealed FROM approval_policy_versions WHERE id = $1`, sv).Scan(&sealed); err != nil {
		t.Fatalf("read S's version: %v", err)
	}
	if sealed || policyHasActiveVersion(t, super, s) {
		t.Errorf("S's version after a refused publish: sealed=%v active=%v, want both false", sealed, policyHasActiveVersion(t, super, s))
	}
	if n := approvalRunCountForInvoice(t, super, invoiceID); n != 0 {
		t.Errorf("approval_runs for the validated invoice = %d, want 0 (no run armed)", n)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.published"); n != 0 {
		t.Errorf("approval_policy.published rows = %d, want 0", n)
	}
}

func TestPublish_ScopedPolicyAboveAnInForceCatchAllPublishes(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-03 publish-above-catchall")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)
	s, _ := seedPolicyShape(t, super, tenantID, "S", true, false)
	last, _ := seedPolicyShape(t, super, tenantID, "C", false, false)

	if _, err := store.PublishPolicy(c, last); err != nil {
		t.Fatalf("publish C: %v", err)
	}
	if _, err := store.PublishPolicy(c, s); err != nil {
		t.Fatalf("publish S above the in-force catch-all: %v, want success", err)
	}
	if !policyHasActiveVersion(t, super, s) {
		t.Error("S has no active version after a successful publish")
	}
}

// --- AC7: what sits above the last policy always deletes -----------------------

func TestDeletePolicy_ProbeAboveADraftOnlySeededPolicyDeletes(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-03 probe-above-draft-only")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)
	probe, _ := seedPolicyShape(t, super, tenantID, "Probe Policy 1", false, true)
	last, _ := seedPolicyShape(t, super, tenantID, "Seeded", false, false)

	if _, err := store.DeletePolicy(c, probe); err != nil {
		t.Fatalf("delete of a published probe above a draft-only seeded policy: %v", err)
	}
	if !policyIsLive(t, super, last) {
		t.Error("the seeded policy is not live")
	}
}

func TestDeletePolicy_AnyPolicyAboveTheLastCatchAllDeletes(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-03 any-above-last")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)
	above := map[string]string{}
	for _, shape := range []struct {
		name           string
		scoped, active bool
	}{{"scoped active", true, true}, {"scoped draft", true, false}, {"plain active", false, true}, {"plain draft", false, false}} {
		above[shape.name], _ = seedPolicyShape(t, super, tenantID, shape.name, shape.scoped, shape.active)
	}
	last, _ := seedPolicyShape(t, super, tenantID, "C", false, true)
	if len(above) != 4 {
		t.Fatalf("seeded %d policies above the last, want 4", len(above))
	}

	for _, name := range []string{"scoped active", "scoped draft", "plain active", "plain draft"} {
		if _, err := store.DeletePolicy(c, above[name]); err != nil {
			t.Errorf("delete of the %s policy above the last catch-all: %v, want success", name, err)
		}
	}
	assertLiveAndActive(t, super, "C", last)
}

// Restore-helper flow: the e2e helpers create, publish and delete a probe while the seeded policy sits last.
func TestDeletePolicy_ProbeCreatedThroughTheAPIDeletesWhileTheSeededPolicySitsLast(t *testing.T) {
	for _, shape := range []struct {
		name   string
		active bool
	}{{"firm shard, seeded policy published", true}, {"in-house shard, seeded policy draft-only", false}} {
		t.Run(shape.name, func(t *testing.T) {
			super, app := dbTestPools(t)
			tenantID := policyTenant(t, super, "APSC-02-03 probe-flow")
			c, _ := activeAdmin(t, super, tenantID)
			store := NewStore(app, stubFingerprinter, nil)
			seeded, _ := seedPolicyShape(t, super, tenantID, "Seeded", false, shape.active)

			probe, err := store.CreatePolicy(c, "Probe Policy 1", nil)
			if err != nil {
				t.Fatalf("create probe: %v", err)
			}
			if _, err := store.PutDraft(c, probe.ID, nil, nil, []stepInput{}); err != nil {
				t.Fatalf("draft probe: %v", err)
			}
			if _, err := store.PublishPolicy(c, probe.ID); err != nil {
				t.Fatalf("publish probe: %v", err)
			}
			if got, want := listedNames(t, store, c), []string{"Probe Policy 1", "Seeded"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("list order = %v, want %v: the probe must sit above the seeded policy", got, want)
			}
			if _, err := store.DeletePolicy(c, probe.ID); err != nil {
				t.Fatalf("delete probe: %v", err)
			}
			if !policyIsLive(t, super, seeded) || policyHasActiveVersion(t, super, seeded) != shape.active {
				t.Errorf("seeded policy changed by the probe's delete: live=%v active=%v, want live, active=%v",
					policyIsLive(t, super, seeded), policyHasActiveVersion(t, super, seeded), shape.active)
			}
		})
	}
}

// --- AC8: no scope, no refusal -------------------------------------------------

func TestCatchAll_NoScopedPolicyNeverRefuses(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-03 no-scope")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)
	ids := make([]string, 3)
	for i, name := range []string{"One", "Two", "Three"} {
		ids[i], _ = seedPolicyShape(t, super, tenantID, name, false, true)
	}

	for _, i := range []int{2, 0, 1} {
		if _, err := store.DeletePolicy(c, ids[i]); err != nil {
			t.Errorf("delete of policy %d with no scoped policy: %v, want success", i+1, err)
		}
	}
	if names := listedNames(t, store, c); len(names) != 0 {
		t.Errorf("list after deleting all = %v, want empty", names)
	}
}

// --- AC9: the tenant lock ------------------------------------------------------

// Publish holds its tx in the sweep after its guard passed; a delete that takes no tenant lock
// passes its own guard against the old state and both succeed.
func TestCatchAll_ConcurrentPublishAndDeleteLeaveACatchAllInForce(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-03 concurrent")
	c, _ := activeAdmin(t, super, tenantID)
	entityID := seedBusinessEntity(t, super, tenantID, "Race Corp")
	bulkSeedValidatedInvoices(t, super, tenantID, entityID, "race", 1)
	s, _ := seedPolicyShape(t, super, tenantID, "S", true, false)
	u, _ := seedPolicyShape(t, super, tenantID, "U", false, true)
	seedPolicyShape(t, super, tenantID, "C", false, false)

	inSweep := make(chan int, 1)
	release := make(chan struct{})
	var once, relOnce sync.Once
	releaseHold := func() { relOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseHold)
	hold := func(ctx context.Context, tx pgx.Tx, invoiceID string) (string, error) {
		once.Do(func() {
			inSweep <- backendPID(t, ctx, tx)
			<-release
		})
		return stubFingerprintValue, nil
	}

	publishDone := publishInBackground(NewStore(app, hold, nil), c, s)
	var holderPID int
	select {
	case holderPID = <-inSweep:
	case err := <-publishDone:
		t.Fatalf("publish of S ended (err = %v) before reaching its sweep", err)
	case <-time.After(lockWaitDeadline):
		t.Fatal("publish of S never reached its sweep")
	}

	deleteDone := make(chan error, 1)
	go func() {
		_, err := NewStore(app, stubFingerprinter, nil).DeletePolicy(c, u)
		deleteDone <- err
	}()

	deadline := time.After(lockWaitDeadline)
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	var deleteErr error
	deleteEarly := false
wait:
	for {
		select {
		case deleteErr = <-deleteDone:
			deleteEarly = true
			break wait
		case <-deadline:
			t.Fatal("delete neither blocked on the publish nor returned")
		case <-tick.C:
			var n int
			if err := super.QueryRow(context.Background(),
				`SELECT count(*) FROM pg_stat_activity
				  WHERE datname = current_database() AND $1 = ANY(pg_blocking_pids(pid))`,
				holderPID).Scan(&n); err != nil {
				t.Fatalf("poll pg_blocking_pids: %v", err)
			}
			if n > 0 {
				break wait
			}
		}
	}
	if deleteEarly {
		t.Errorf("DeletePolicy(U) returned (err = %v) while PublishPolicy(S) held its tx: it takes no tenant lock", deleteErr)
	}
	releaseHold()

	publishErr := awaitPublish(t, publishDone)
	if !deleteEarly {
		select {
		case deleteErr = <-deleteDone:
		case <-time.After(lockWaitDeadline):
			t.Fatal("delete did not return after the publish ended")
		}
	}

	failures := 0
	for _, err := range []error{publishErr, deleteErr} {
		if errors.Is(err, ErrPolicyCatchAll) {
			failures++
		} else if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if failures != 1 {
		t.Errorf("publish err = %v, delete err = %v: want one success and one ErrPolicyCatchAll (check 2)", publishErr, deleteErr)
	}
	scoped, found := lastInForceIsScoped(t, super, tenantID)
	if !found || scoped {
		t.Errorf("last policy in force: found=%v scoped=%v, want an unscoped active version", found, scoped)
	}
}

// Create, PutDraft, Publish and Delete each take the tenant lock as the statement after the admin check.
func TestPolicyWrites_TakeTheTenantLockAfterTheAdminCheck(t *testing.T) {
	const lock = "SELECT pg_advisory_xact_lock(hashtext($1))"
	cases := []struct {
		name string
		run  func(t *testing.T, store *Store, c context.Context, super *pgxpool.Pool, tenantID string) error
	}{
		{"CreatePolicy", func(t *testing.T, store *Store, c context.Context, _ *pgxpool.Pool, _ string) error {
			_, err := store.CreatePolicy(c, "New", nil)
			return err
		}},
		{"PutDraft", func(t *testing.T, store *Store, c context.Context, super *pgxpool.Pool, tenantID string) error {
			id, _ := seedPolicyShape(t, super, tenantID, "D", false, false)
			_, err := store.PutDraft(c, id, nil, nil, approvalStep("engagement-partner"))
			return err
		}},
		{"PublishPolicy", func(t *testing.T, store *Store, c context.Context, super *pgxpool.Pool, tenantID string) error {
			id, _ := seedPolicyShape(t, super, tenantID, "P", false, false)
			_, err := store.PublishPolicy(c, id)
			return err
		}},
		{"DeletePolicy", func(t *testing.T, store *Store, c context.Context, super *pgxpool.Pool, tenantID string) error {
			id, _ := seedPolicyShape(t, super, tenantID, "X", false, true)
			_, err := store.DeletePolicy(c, id)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			super, _ := dbTestPools(t)
			tenantID := policyTenant(t, super, "APSC-02-03 lock-order "+tc.name)
			c, _ := activeAdmin(t, super, tenantID)
			traced, rec := tracedAppPool(t)
			store := NewStore(traced, stubFingerprinter, nil)

			rec.reset()
			if err := tc.run(t, store, c, super, tenantID); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			stmts := rec.mentioning("")
			adminAt := -1
			for i, s := range stmts {
				if strings.Contains(s, "SELECT role FROM memberships") {
					adminAt = i
					break
				}
			}
			if adminAt < 0 {
				t.Fatalf("no admin check among the statements:\n%v", stmts)
			}
			if adminAt+1 >= len(stmts) || strings.TrimSpace(stmts[adminAt+1]) != lock {
				next := "<none>"
				if adminAt+1 < len(stmts) {
					next = stmts[adminAt+1]
				}
				t.Errorf("the statement after the admin check is:\n%s\nwant %s", next, lock)
			}
		})
	}
}

// Another tenant's scoped policy pins nothing here: the guards read through RLS.
func TestCatchAll_AnotherTenantsScopedPolicyPinsNothing(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := policyTenant(t, super, "APSC-02-03 guard-tenant-a")
	tenantB := policyTenant(t, super, "APSC-02-03 guard-tenant-b")
	c, _ := activeAdmin(t, super, tenantA)
	seedPolicyShape(t, super, tenantB, "B scoped", true, true)
	if scoped, found := lastInForceIsScoped(t, super, tenantB); !found || !scoped {
		t.Fatalf("fixture: tenant B's last in force scoped = %v, found = %v, want scoped", scoped, found)
	}
	store := NewStore(app, stubFingerprinter, nil)
	u, _ := seedPolicyShape(t, super, tenantA, "A upper", false, true)
	last, _ := seedPolicyShape(t, super, tenantA, "A last", false, true)

	if _, err := store.ReorderPolicies(c, []string{last, u}); err != nil {
		t.Errorf("reorder in A with B scoped: err = %v, want success (check 3 reads A only)", err)
	}
	if _, err := store.DeletePolicy(c, last); err != nil {
		t.Errorf("delete in A with B scoped: err = %v, want success", err)
	}
	if _, err := store.DeletePolicy(c, u); err != nil {
		t.Errorf("delete of A's only policy with B scoped: err = %v, want success", err)
	}
	if _, err := store.CreatePolicy(c, "A scoped first", creditNoteScope()); !errors.Is(err, ErrPolicyCatchAll) {
		t.Errorf("scoped create in an empty A: err = %v, want ErrPolicyCatchAll (B's policies are no catch-all of A)", err)
	}
}
