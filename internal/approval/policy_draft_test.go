package approval

// Store.PutDraft under a real Postgres: the whole-tree write, the fork, server-minted
// ids and the audit. Written before the method body exists, so every spec here starts
// RED against policy_store.go's stub.
//
// Separate from policy_crud_test.go, which belongs to the create/list/get subtask.
// Run locally with `DEV_DB_PORT=5433 make test-approvals`; in CI the rls job's gate
// step fails the build on any skip.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- fixtures ---------------------------------------------------------------

// activateApprovalPolicyVersion seals AND activates a version, the shape a published
// policy holds. approval_policy_versions_active_is_sealed refuses is_active without
// sealed, so the two move in one statement. published_at/by are set so the "unchanged
// afterwards" assertions are not vacuously NULL == NULL.
func activateApprovalPolicyVersion(t *testing.T, super *pgxpool.Pool, versionID string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE approval_policy_versions
		    SET sealed = true, is_active = true,
		        published_at = now(), published_by = 'fixture-publisher'
		  WHERE id = $1`, versionID)
	if err != nil {
		t.Fatalf("activate approval_policy_versions %s: %v", versionID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("activate approval_policy_versions %s affected %d rows, want 1", versionID, tag.RowsAffected())
	}
}

// storedStep is one approval_policy_steps row read column by column. The nested wire
// tree cannot express parent/branch/ord, and it is where a misaligned unnest batch
// hides — a NULL that landed in the wrong column still nests into a plausible tree.
type storedStep struct {
	ID              string
	ParentStepID    *string
	Branch          *string
	Ord             int
	Kind            string
	WorkflowRoleKey *string
	SLAHours        *int
	CondOp          *string
	CondAmount      *string
	NotifyTarget    *string
	NotifyChannel   *string
}

func (s storedStep) String() string {
	return fmt.Sprintf("{parent:%s branch:%s ord:%d kind:%s role:%s sla:%s op:%s amount:%s target:%s channel:%s}",
		strOrNull(s.ParentStepID), strOrNull(s.Branch), s.Ord, s.Kind,
		strOrNull(s.WorkflowRoleKey), intOrNull(s.SLAHours), strOrNull(s.CondOp),
		strOrNull(s.CondAmount), strOrNull(s.NotifyTarget), strOrNull(s.NotifyChannel))
}

func strOrNull(p *string) string {
	if p == nil {
		return "NULL"
	}
	return fmt.Sprintf("%q", *p)
}

func intOrNull(p *int) string {
	if p == nil {
		return "NULL"
	}
	return fmt.Sprintf("%d", *p)
}

// readStoredSteps reads every step row of one version as the superuser, roots first
// then lane members, each group in ord order. cond_amount goes through ::text for the
// reason readPolicyTrees does: a bare numeric drops its scale at zero.
func readStoredSteps(t *testing.T, super *pgxpool.Pool, versionID string) []storedStep {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT id, parent_step_id, branch, ord, kind, workflow_role_key, sla_hours,
		        cond_op, cond_amount::text, notify_target, notify_channel
		   FROM approval_policy_steps
		  WHERE version_id = $1
		  ORDER BY parent_step_id NULLS FIRST, branch NULLS FIRST, ord`, versionID)
	if err != nil {
		t.Fatalf("read steps of version %s: %v", versionID, err)
	}
	defer rows.Close()
	out := []storedStep{}
	for rows.Next() {
		var s storedStep
		if err := rows.Scan(&s.ID, &s.ParentStepID, &s.Branch, &s.Ord, &s.Kind,
			&s.WorkflowRoleKey, &s.SLAHours, &s.CondOp, &s.CondAmount,
			&s.NotifyTarget, &s.NotifyChannel); err != nil {
			t.Fatalf("scan step of version %s: %v", versionID, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read steps of version %s: %v", versionID, err)
	}
	return out
}

// lane returns the members of one lane in ord order. parent "" means the root lane.
func lane(steps []storedStep, parent, branch string) []storedStep {
	out := []storedStep{}
	for _, s := range steps {
		if parent == "" {
			if s.ParentStepID == nil && s.Branch == nil {
				out = append(out, s)
			}
			continue
		}
		if s.ParentStepID != nil && *s.ParentStepID == parent && s.Branch != nil && *s.Branch == branch {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ord < out[j].Ord })
	return out
}

// stepIDs is the id set of a flat row list, for the churn and never-echoed assertions.
func stepIDs(steps []storedStep) []string {
	ids := make([]string, 0, len(steps))
	for _, s := range steps {
		ids = append(ids, s.ID)
	}
	sort.Strings(ids)
	return ids
}

// storedVersion is the version row's mutable image, for the fork and
// sealed-untouched assertions.
type storedVersion struct {
	ID          string
	Version     int
	Sealed      bool
	IsActive    bool
	PublishedAt *string
	PublishedBy *string
}

// versionRows reads every version of a policy, lowest number first.
func versionRows(t *testing.T, super *pgxpool.Pool, policyID string) []storedVersion {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT id, version, sealed, is_active, published_at::text, published_by
		   FROM approval_policy_versions
		  WHERE policy_id = $1
		  ORDER BY version`, policyID)
	if err != nil {
		t.Fatalf("read versions of policy %s: %v", policyID, err)
	}
	defer rows.Close()
	out := []storedVersion{}
	for rows.Next() {
		var v storedVersion
		if err := rows.Scan(&v.ID, &v.Version, &v.Sealed, &v.IsActive, &v.PublishedAt, &v.PublishedBy); err != nil {
			t.Fatalf("scan version of policy %s: %v", policyID, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read versions of policy %s: %v", policyID, err)
	}
	return out
}

// openDraft returns the policy's one unsealed version, Fataling when there is none —
// approval_policy_versions_one_draft makes "one" the only other legal count.
func openDraft(t *testing.T, super *pgxpool.Pool, policyID string) storedVersion {
	t.Helper()
	drafts := []storedVersion{}
	for _, v := range versionRows(t, super, policyID) {
		if !v.Sealed {
			drafts = append(drafts, v)
		}
	}
	if len(drafts) != 1 {
		t.Fatalf("policy %s has %d unsealed versions, want exactly 1: %+v", policyID, len(drafts), drafts)
	}
	return drafts[0]
}

// versionSnapshot reads a version's whole row as jsonb text, the stepSnapshot idiom
// one table up.
func versionSnapshot(t *testing.T, super *pgxpool.Pool, versionID string) string {
	t.Helper()
	var snap string
	if err := super.QueryRow(context.Background(),
		`SELECT to_jsonb(v)::text FROM approval_policy_versions v WHERE id = $1`, versionID).Scan(&snap); err != nil {
		t.Fatalf("snapshot version %s: %v", versionID, err)
	}
	return snap
}

// storedPolicyName is storedPolicyScope's sibling, for the absent-name spec.
func storedPolicyName(t *testing.T, super *pgxpool.Pool, policyID string) string {
	t.Helper()
	var name string
	if err := super.QueryRow(context.Background(),
		`SELECT name FROM approval_policies WHERE id = $1`, policyID).Scan(&name); err != nil {
		t.Fatalf("read back name of %s: %v", policyID, err)
	}
	return name
}

// approvalStep is the one-step body most specs here submit: the smallest tree that is
// still distinguishable from whatever the fixture already held.
func approvalStep(roleKey string) []stepInput {
	return []stepInput{{Kind: "approval", WorkflowRoleKey: ptr(roleKey)}}
}

// seedDraftWithSteps gives a policy an open version 1 carrying n marker notify steps,
// and returns the version id. Marker targets make "these exact rows are gone" provable
// rather than inferred from a count.
func seedDraftWithSteps(t *testing.T, super *pgxpool.Pool, tenantID, policyID string, n int) string {
	t.Helper()
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	for i := 0; i < n; i++ {
		seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
			Ord: i, Kind: "notify",
			NotifyTarget:  ptr(fmt.Sprintf("seeded-step-%d-must-not-survive", i)),
			NotifyChannel: ptr("email"),
		})
	}
	return versionID
}

// seedSealedActiveV1 is the fork fixture: a policy whose ONLY version is v1, sealed and
// active, carrying two steps. D7's "the active version" and "the newest sealed version"
// coincide here, which is why the fork spec keeps its name over this shape.
func seedSealedActiveV1(t *testing.T, super *pgxpool.Pool, tenantID, policyID string) string {
	t.Helper()
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("sealed-v1-role"), SLAHours: ptr(24),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "notify",
		NotifyTarget: ptr("sealed-v1-step-must-not-be-copied"), NotifyChannel: ptr("email"),
	})
	activateApprovalPolicyVersion(t, super, versionID)
	return versionID
}

// --- AC-1: the open draft is replaced wholesale --------------------------------

// TestPutDraft_ReplacesOpenDraftWholesale: three steps in, one step out, on the SAME
// version row. The version number not changing is the half that separates a replace
// from a fork, and the marker targets prove the old rows are gone rather than merely
// outnumbered.
func TestPutDraft_ReplacesOpenDraftWholesale(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-replaces-draft")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedDraftWithSteps(t, super, tenantID, policyID, 3)

	got, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, nil, approvalStep("engagement-partner"))
	if err != nil {
		t.Fatalf("PutDraft: %v", err)
	}

	versions := versionRows(t, super, policyID)
	if len(versions) != 1 {
		t.Fatalf("policy has %d versions, want 1 — an open draft is rewritten, never forked: %+v", len(versions), versions)
	}
	v := versions[0]
	if v.ID != versionID || v.Version != 1 || v.Sealed || v.IsActive {
		t.Errorf("version row = %+v, want the seeded %s at (version 1, sealed false, is_active false)", v, versionID)
	}

	stored := readStoredSteps(t, super, versionID)
	if len(stored) != 1 {
		t.Fatalf("draft holds %d steps, want exactly 1 — the write is a whole-tree replace: %v", len(stored), stored)
	}
	s := stored[0]
	if s.Kind != "approval" || s.WorkflowRoleKey == nil || *s.WorkflowRoleKey != "engagement-partner" {
		t.Errorf("stored step = %v, want an approval naming engagement-partner", s)
	}
	if s.ParentStepID != nil || s.Branch != nil || s.Ord != 0 {
		t.Errorf("stored step = %v, want a root at ord 0", s)
	}
	for _, r := range stored {
		if r.NotifyTarget != nil {
			t.Errorf("a seeded step survived the replace: %v", r)
		}
	}

	// The response describes the draft that was written: 07 renders it and the SPA
	// re-reads nothing, so a response disagreeing with the row is invisible until a
	// later GET contradicts it.
	if got.ID != policyID || got.Name != "Sign-off" || got.Scope != scopeAllInvoices {
		t.Errorf("returned policy = (%q, %q, %q), want (%q, Sign-off, %q)",
			got.ID, got.Name, got.Scope, policyID, scopeAllInvoices)
	}
	if got.Version != 1 || got.Sealed || got.Status != "draft" {
		t.Errorf("returned policy = (version %d, sealed %v, status %q), want (1, false, draft)",
			got.Version, got.Sealed, got.Status)
	}
	if len(got.Steps) != 1 {
		t.Fatalf("returned tree has %d root steps, want 1: %+v", len(got.Steps), got.Steps)
	}
	if got.Steps[0].ID != s.ID {
		t.Errorf("returned step id = %q, want the persisted %q", got.Steps[0].ID, s.ID)
	}
}

// TestPutDraft_TwoLaneConditionKeepsPerLaneOrd: ord is per lane, not per statement. A
// children batch deriving ord from array position writes then:0,1 else:2; the correct
// write is then:0,1 else:0, because approval_policy_steps_slot_uq keys on
// (version_id, parent_step_id, branch, ord) and each lane restarts.
func TestPutDraft_TwoLaneConditionKeepsPerLaneOrd(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-per-lane-ord")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)

	tree := []stepInput{{
		Kind: "condition", CondOp: ptr(">="), CondAmount: ptr("1000.00"),
		Then: []stepInput{
			{Kind: "approval", WorkflowRoleKey: ptr("then-a")},
			{Kind: "approval", WorkflowRoleKey: ptr("then-b")},
		},
		Else: []stepInput{
			{Kind: "approval", WorkflowRoleKey: ptr("else-x")},
		},
	}}

	got, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, nil, tree)
	if err != nil {
		t.Fatalf("PutDraft: %v", err)
	}

	stored := readStoredSteps(t, super, versionID)
	if len(stored) != 4 {
		t.Fatalf("draft holds %d steps, want 4 (one condition and three lane members): %v", len(stored), stored)
	}
	roots := lane(stored, "", "")
	if len(roots) != 1 || roots[0].Kind != "condition" {
		t.Fatalf("root lane = %v, want one condition", roots)
	}
	cond := roots[0]
	if cond.Ord != 0 {
		t.Errorf("condition ord = %d, want 0", cond.Ord)
	}

	thenLane := lane(stored, cond.ID, "then")
	if len(thenLane) != 2 {
		t.Fatalf("then lane has %d members, want 2: %v", len(thenLane), thenLane)
	}
	for i, want := range []struct {
		ord  int
		role string
	}{{0, "then-a"}, {1, "then-b"}} {
		if thenLane[i].Ord != want.ord {
			t.Errorf("then lane member %d ord = %d, want %d", i, thenLane[i].Ord, want.ord)
		}
		if thenLane[i].WorkflowRoleKey == nil || *thenLane[i].WorkflowRoleKey != want.role {
			t.Errorf("then lane member %d = %v, want role %q", i, thenLane[i], want.role)
		}
	}

	elseLane := lane(stored, cond.ID, "else")
	if len(elseLane) != 1 {
		t.Fatalf("else lane has %d members, want 1: %v", len(elseLane), elseLane)
	}
	if elseLane[0].Ord != 0 {
		t.Errorf("else lane member ord = %d, want 0 — ord restarts in every lane, so a globally "+
			"sequential counter (WITH ORDINALITY over the children array) is the regression", elseLane[0].Ord)
	}
	if elseLane[0].WorkflowRoleKey == nil || *elseLane[0].WorkflowRoleKey != "else-x" {
		t.Errorf("else lane member = %v, want role else-x", elseLane[0])
	}

	// The nested response must carry the same lane order.
	if len(got.Steps) != 1 || len(got.Steps[0].Then) != 2 || len(got.Steps[0].Else) != 1 {
		t.Fatalf("returned tree = %+v, want one condition with (2 then, 1 else)", got.Steps)
	}
	if got.Steps[0].Then[0].WorkflowRoleKey == nil || *got.Steps[0].Then[0].WorkflowRoleKey != "then-a" {
		t.Errorf("returned then lane head = %+v, want then-a first", got.Steps[0].Then[0])
	}
}

// TestPutDraft_NullableColumnsRoundTripInOneBatch: every nullable column, NULL and
// non-NULL, in ONE unnest batch per level. Nothing in this repo binds a []*string or
// []*int yet — the only unnest INSERT that ships (SetRoleMembers) binds a plain
// []string — so column/array misalignment inside a mixed-NULL batch is untested
// ground, and it is invisible except by reading every column back: a NULL that landed
// one column over still nests into a plausible tree.
//
// sla_hours 0 sits beside sla_hours NULL on purpose: a driver or SQL shape that
// collapses the two reads as a plausible value rather than as a bug.
func TestPutDraft_NullableColumnsRoundTripInOneBatch(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-null-round-trip")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)

	tree := []stepInput{
		// Root 0: every optional column populated.
		{
			Kind: "approval", WorkflowRoleKey: ptr("alpha"), SLAHours: ptr(48),
			CondOp: ptr("<"), CondAmount: ptr("12.34"),
			NotifyTarget: ptr("alpha@example.com"), NotifyChannel: ptr("email"),
		},
		// Root 1: every optional column NULL, in the same batch as root 0.
		{Kind: "autoapprove"},
		// Root 2: a condition whose sla_hours is the explicit zero.
		{
			Kind: "condition", CondOp: ptr(">="), CondAmount: ptr("0"), SLAHours: ptr(0),
			Then: []stepInput{
				// Child with everything NULL but its kind.
				{Kind: "autoapprove"},
				// Child with a value, in the same children batch as the NULL one.
				{Kind: "notify", SLAHours: ptr(7), CondAmount: ptr("999999999999.99"),
					NotifyTarget: ptr("ops@example.com"), NotifyChannel: ptr("slack")},
			},
			Else: []stepInput{
				{Kind: "approval", WorkflowRoleKey: ptr("omega")},
			},
		},
	}

	if _, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, nil, tree); err != nil {
		t.Fatalf("PutDraft: %v", err)
	}

	stored := readStoredSteps(t, super, versionID)
	if len(stored) != 6 {
		t.Fatalf("draft holds %d steps, want 6: %v", len(stored), stored)
	}
	roots := lane(stored, "", "")
	if len(roots) != 3 {
		t.Fatalf("root lane has %d members, want 3: %v", len(roots), roots)
	}

	assertStoredStep(t, "root 0", roots[0], storedStep{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("alpha"), SLAHours: ptr(48),
		CondOp: ptr("<"), CondAmount: ptr("12.34"),
		NotifyTarget: ptr("alpha@example.com"), NotifyChannel: ptr("email"),
	})
	assertStoredStep(t, "root 1 (all-NULL, batched beside root 0)", roots[1], storedStep{
		Ord: 1, Kind: "autoapprove",
	})
	cond := roots[2]
	assertStoredStep(t, "root 2 (condition, sla_hours is the explicit zero)", cond, storedStep{
		Ord: 2, Kind: "condition", CondOp: ptr(">="), CondAmount: ptr("0.00"), SLAHours: ptr(0),
	})

	thenLane := lane(stored, cond.ID, "then")
	if len(thenLane) != 2 {
		t.Fatalf("then lane has %d members, want 2: %v", len(thenLane), thenLane)
	}
	assertStoredStep(t, "then 0 (all-NULL child)", thenLane[0], storedStep{
		ParentStepID: &cond.ID, Branch: ptr("then"), Ord: 0, Kind: "autoapprove",
	})
	assertStoredStep(t, "then 1 (populated child, batched beside the all-NULL one)", thenLane[1], storedStep{
		ParentStepID: &cond.ID, Branch: ptr("then"), Ord: 1, Kind: "notify", SLAHours: ptr(7),
		CondAmount: ptr("999999999999.99"),
		// The numeric(14,2) ceiling, to catch a scale or precision truncation the
		// smaller values would round past.
		NotifyTarget: ptr("ops@example.com"), NotifyChannel: ptr("slack"),
	})

	elseLane := lane(stored, cond.ID, "else")
	if len(elseLane) != 1 {
		t.Fatalf("else lane has %d members, want 1: %v", len(elseLane), elseLane)
	}
	assertStoredStep(t, "else 0", elseLane[0], storedStep{
		ParentStepID: &cond.ID, Branch: ptr("else"), Ord: 0, Kind: "approval",
		WorkflowRoleKey: ptr("omega"),
	})
}

// assertStoredStep compares every column except id, which is server-minted. want's
// ParentStepID/Branch are compared by value.
func assertStoredStep(t *testing.T, label string, got, want storedStep) {
	t.Helper()
	got.ID = ""
	want.ID = ""
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v,\n  want %v", label, got, want)
	}
}

// --- AC-2: the fork ------------------------------------------------------------

// TestPutDraft_ForksFromActiveWhenNoDraft: the policy's only version is v1, sealed and
// active. The PUT opens v2 carrying only the submitted tree; v1 keeps its two steps and
// stays the active one.
//
// The name is the Core AC's, kept over a fixture where "the active version" and "the
// newest sealed version" coincide. The resolution predicate the store uses is NOT
// sealed, because approval_policy_versions_one_active spans the tenant, so a policy can
// hold sealed versions and no active one.
func TestPutDraft_ForksFromActiveWhenNoDraft(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-forks")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	v1 := seedSealedActiveV1(t, super, tenantID, policyID)

	got, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, nil, approvalStep("engagement-partner"))
	if err != nil {
		t.Fatalf("PutDraft: %v", err)
	}

	versions := versionRows(t, super, policyID)
	if len(versions) != 2 {
		t.Fatalf("policy has %d versions, want 2 — with no open draft the PUT forks: %+v", len(versions), versions)
	}
	if versions[0].ID != v1 || versions[0].Version != 1 || !versions[0].Sealed || !versions[0].IsActive {
		t.Errorf("v1 = %+v, want the seeded %s still (sealed true, is_active true)", versions[0], v1)
	}
	v2 := versions[1]
	if v2.Version != 2 || v2.Sealed || v2.IsActive {
		t.Errorf("v2 = %+v, want (version 2, sealed false, is_active false)", v2)
	}
	if v2.PublishedAt != nil || v2.PublishedBy != nil {
		t.Errorf("v2 published_at/by = (%v, %v), want both NULL — a fork publishes nothing",
			v2.PublishedAt, v2.PublishedBy)
	}

	newSteps := readStoredSteps(t, super, v2.ID)
	if len(newSteps) != 1 {
		t.Fatalf("v2 holds %d steps, want exactly the 1 submitted: %v", len(newSteps), newSteps)
	}
	if newSteps[0].WorkflowRoleKey == nil || *newSteps[0].WorkflowRoleKey != "engagement-partner" {
		t.Errorf("v2 step = %v, want an approval naming engagement-partner", newSteps[0])
	}
	for _, s := range newSteps {
		if s.NotifyTarget != nil && *s.NotifyTarget == "sealed-v1-step-must-not-be-copied" {
			t.Errorf("a v1 step was copied into the fork: %v", s)
		}
	}

	if oldSteps := readStoredSteps(t, super, v1); len(oldSteps) != 2 {
		t.Errorf("v1 holds %d steps, want its original 2: %v", len(oldSteps), oldSteps)
	}
	if got.Version != 2 || got.Sealed || got.Status != "draft" {
		t.Errorf("returned policy = (version %d, sealed %v, status %q), want the fork (2, false, draft)",
			got.Version, got.Sealed, got.Status)
	}
}

// TestPutDraft_ForkCopiesNoSteps: the same fixture with an EMPTY tree. A fork is not a
// copy — the new version's content comes entirely from the body, so v2 holds zero
// steps even though v1 holds two.
func TestPutDraft_ForkCopiesNoSteps(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-fork-copies-nothing")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	v1 := seedSealedActiveV1(t, super, tenantID, policyID)

	got, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, nil, []stepInput{})
	if err != nil {
		t.Fatalf("PutDraft with an empty tree: %v", err)
	}

	versions := versionRows(t, super, policyID)
	if len(versions) != 2 {
		t.Fatalf("policy has %d versions, want 2: %+v", len(versions), versions)
	}
	v2 := versions[1]
	if v2.Version != 2 || v2.Sealed {
		t.Errorf("v2 = %+v, want (version 2, sealed false)", v2)
	}
	if steps := readStoredSteps(t, super, v2.ID); len(steps) != 0 {
		t.Errorf("v2 holds %d steps, want 0 — a fork copies nothing: %v", len(steps), steps)
	}
	if steps := readStoredSteps(t, super, v1); len(steps) != 2 {
		t.Errorf("v1 holds %d steps, want its original 2: %v", len(steps), steps)
	}
	if got.Steps == nil {
		t.Error("returned steps is nil; the producer must build []Step{} or the wire renders null")
	}
	if len(got.Steps) != 0 {
		t.Errorf("returned tree = %+v, want empty", got.Steps)
	}
}

// --- AC-3: a sealed version is never touched -----------------------------------

// TestPutDraft_LeavesSealedVersionByteIdentical: a sealed+active v1 beside an open v2.
// The PUT rewrites v2; every v1 step row and the v1 version row are byte-identical
// afterwards. approval_policy_steps_content_lock would raise 23001 if the store reached
// them, so a green here also says the NOT sealed resolution predicate held.
func TestPutDraft_LeavesSealedVersionByteIdentical(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-sealed-untouched")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	v1 := seedSealedActiveV1(t, super, tenantID, policyID)

	v2 := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 2)
	seedApprovalPolicyStepInLane(t, super, tenantID, v2, seedStepSpec{
		Ord: 0, Kind: "notify",
		NotifyTarget: ptr("v2-step-must-not-survive"), NotifyChannel: ptr("email"),
	})

	sealedSteps := readStoredSteps(t, super, v1)
	if len(sealedSteps) != 2 {
		t.Fatalf("fixture: v1 holds %d steps, want 2", len(sealedSteps))
	}
	before := map[string]string{}
	for _, s := range sealedSteps {
		before[s.ID] = stepSnapshot(t, super, s.ID)
	}
	versionBefore := versionSnapshot(t, super, v1)

	if _, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, ptr("Renamed"), nil, approvalStep("engagement-partner")); err != nil {
		t.Fatalf("PutDraft: %v", err)
	}

	after := readStoredSteps(t, super, v1)
	if len(after) != len(before) {
		t.Fatalf("v1 holds %d steps after the PUT, want its original %d: %v", len(after), len(before), after)
	}
	for _, s := range after {
		want, ok := before[s.ID]
		if !ok {
			t.Errorf("a step appeared under the sealed v1: %v", s)
			continue
		}
		if got := stepSnapshot(t, super, s.ID); got != want {
			t.Errorf("sealed step %s changed:\n  before %s\n  after  %s", s.ID, want, got)
		}
	}
	if got := versionSnapshot(t, super, v1); got != versionBefore {
		t.Errorf("the sealed version row changed:\n  before %s\n  after  %s", versionBefore, got)
	}

	// Control: the open v2 IS rewritten, so the assertions above are not a store that
	// wrote nothing at all.
	rewritten := readStoredSteps(t, super, v2)
	if len(rewritten) != 1 || rewritten[0].Kind != "approval" {
		t.Fatalf("v2 holds %v, want the one submitted approval step", rewritten)
	}
}

// --- AC-4: server-minted ids ---------------------------------------------------

// TestPutDraft_ClientStepIdsIgnored: a body decoded from JSON carrying an id on every
// node. stepInput declares no id field, so the value is dropped at decode rather than
// refused — decoding through putDraftRequest is what pins that, since a store-level
// literal could not carry one at all. Ids churn per save by design; nothing reads a
// step id back from a server.
func TestPutDraft_ClientStepIdsIgnored(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-client-ids")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)

	body := []byte(`{"steps":[
	  {"id":"wn1000","kind":"condition","cond_op":">=","cond_amount":"1000.00",
	   "then":[{"id":"wn1001","kind":"approval","workflow_role_key":"engagement-partner"}],
	   "else":[{"id":"wn1002","kind":"notify","notify_target":"finance@example.com","notify_channel":"email"}]}
	]}`)
	var req putDraftRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode the request body: %v", err)
	}
	if req.Steps == nil {
		t.Fatal("decoded steps is nil; the fixture body carries a steps array")
	}
	clientIDs := map[string]bool{"wn1000": true, "wn1001": true, "wn1002": true}

	store := NewStore(app, stubFingerprinter, nil)
	first, err := store.PutDraft(c, policyID, nil, nil, *req.Steps)
	if err != nil {
		t.Fatalf("PutDraft: %v", err)
	}

	firstStored := readStoredSteps(t, super, versionID)
	if len(firstStored) != 3 {
		t.Fatalf("draft holds %d steps, want 3: %v", len(firstStored), firstStored)
	}
	for _, s := range firstStored {
		if clientIDs[s.ID] {
			t.Errorf("a client-supplied step id was persisted: %v", s)
		}
		if _, err := uuid.Parse(s.ID); err != nil {
			t.Errorf("persisted step id %q is not a uuid: %v", s.ID, err)
		}
	}
	for _, s := range flattenTree(first.Steps) {
		if clientIDs[s.ID] {
			t.Errorf("a client-supplied step id was echoed in the response: %+v", s)
		}
		if _, err := uuid.Parse(s.ID); err != nil {
			t.Errorf("returned step id %q is not a uuid: %v", s.ID, err)
		}
	}

	// A second identical PUT mints different ids. Churn is the design: pinning it here
	// is what stops a later "stable ids" change from landing silently.
	if _, err := store.PutDraft(c, policyID, nil, nil, *req.Steps); err != nil {
		t.Fatalf("second PutDraft: %v", err)
	}
	secondStored := readStoredSteps(t, super, versionID)
	if len(secondStored) != 3 {
		t.Fatalf("draft holds %d steps after the second PUT, want 3: %v", len(secondStored), secondStored)
	}
	if reflect.DeepEqual(stepIDs(firstStored), stepIDs(secondStored)) {
		t.Errorf("both PUTs persisted the same step ids %v — every save mints fresh ones", stepIDs(firstStored))
	}
}

// TestPutDraft_CondAmountScaleIsCanonicalInTheResponse: numeric(14,2) normalises scale
// on write, so "0" is stored as 0.00 and "1000.5" as 1000.50. A response assembled from
// the request would therefore say "0" while the very next GET says "0.00" — the same
// wire field disagreeing with itself across two calls. The response must be read back
// from the database inside the same transaction.
func TestPutDraft_CondAmountScaleIsCanonicalInTheResponse(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-cond-amount-scale")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)

	tree := []stepInput{
		{Kind: "condition", CondOp: ptr(">="), CondAmount: ptr("0")},
		{Kind: "condition", CondOp: ptr("<"), CondAmount: ptr("1000.5")},
	}

	store := NewStore(app, stubFingerprinter, nil)
	put, err := store.PutDraft(c, policyID, nil, nil, tree)
	if err != nil {
		t.Fatalf("PutDraft: %v", err)
	}
	if len(put.Steps) != 2 {
		t.Fatalf("returned tree has %d root steps, want 2: %+v", len(put.Steps), put.Steps)
	}
	for i, want := range []string{"0.00", "1000.50"} {
		got := put.Steps[i].CondAmount
		if got == nil || *got != want {
			t.Errorf("returned step %d cond_amount = %v, want the column's canonical text %q — "+
				"the response must be read back, not echoed from the request", i, got, want)
		}
	}

	got, err := store.GetPolicy(c, policyID)
	if err != nil {
		t.Fatalf("GetPolicy after the PUT: %v", err)
	}
	if !reflect.DeepEqual(put.Steps, got.Steps) {
		t.Errorf("the PUT response and the next GET disagree:\n  PUT %+v\n  GET %+v", put.Steps, got.Steps)
	}
	if put.Version != got.Version {
		t.Errorf("PUT version = %d, GET version = %d — both name the same draft", put.Version, got.Version)
	}
}

// --- AC-5: scope ---------------------------------------------------------------

// TestPutDraft_ForeignScopeRejected: each of the five scopes the SPA still offers and
// the server refuses is ErrValidation with nothing written — not the raw 23514 the
// column would raise, which policyStatusForErr answers 500 rather than 400. The
// trailing control keeps the zero-write assertions from passing against a store that
// refuses everything.
func TestPutDraft_ForeignScopeRejected(t *testing.T) {
	super, app := dbTestPools(t)

	for _, scope := range removedScopes {
		t.Run(scope, func(t *testing.T) {
			tenantID := policyTenant(t, super, "APPR-05 put-foreign-scope")
			c, _ := activeAdmin(t, super, tenantID)
			policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
			versionID := seedDraftWithSteps(t, super, tenantID, policyID, 1)
			seeded := readStoredSteps(t, super, versionID)

			_, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, ptr(scope), approvalStep("engagement-partner"))
			if !errors.Is(err, ErrValidation) {
				t.Errorf("PutDraft(scope %q) err = %v, want ErrValidation", scope, err)
			}
			if code := pgCode(err); code != "" {
				t.Errorf("PutDraft(scope %q) surfaced a raw Postgres error (SQLSTATE %s) — "+
					"policyStatusForErr maps sentinels only, so this answers 500 instead of 400", scope, code)
			}
			if got := storedPolicyScope(t, super, policyID); got != scopeAllInvoices {
				t.Errorf("stored scope = %q, want the untouched %q", got, scopeAllInvoices)
			}
			if got := readStoredSteps(t, super, versionID); !reflect.DeepEqual(got, seeded) {
				t.Errorf("the step set changed despite the refusal:\n  before %v\n  after  %v", seeded, got)
			}
			if n := len(versionRows(t, super, policyID)); n != 1 {
				t.Errorf("policy has %d versions, want the 1 seeded — a refused scope forks nothing", n)
			}
			if n := auditCount(t, super, tenantID, "approval_policy.updated"); n != 0 {
				t.Errorf("approval_policy.updated audit rows = %d, want 0", n)
			}
		})
	}

	t.Run("control: the accepted scope still writes", func(t *testing.T) {
		tenantID := policyTenant(t, super, "APPR-05 put-foreign-scope-control")
		c, _ := activeAdmin(t, super, tenantID)
		policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
		versionID := seedDraftWithSteps(t, super, tenantID, policyID, 1)

		if _, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, ptr(scopeAllInvoices), approvalStep("engagement-partner")); err != nil {
			t.Fatalf("PutDraft(%q): %v — the refusals above are vacuous unless this succeeds", scopeAllInvoices, err)
		}
		steps := readStoredSteps(t, super, versionID)
		if len(steps) != 1 || steps[0].Kind != "approval" {
			t.Errorf("draft holds %v, want the one submitted approval step", steps)
		}
	})
}

// TestPutDraft_EmptyScopePointerNormalized: a NON-NIL pointer to the empty string is
// the asymmetry — it means "the default scope", not "no change". normalizeScope is the
// sole producer of the stored value, so "" must reach SQL as the default; sent raw it
// raises 23514 approval_policies_scope_check.
//
// The paired nil-pointer control asserts the stored COLUMN, not a statement count: the
// coalesce($3, scope) form always issues a scope write, so "issues no scope write" is
// unenforceable. The column assertion still bites — a store binding the nil pointer
// directly instead of coalescing writes NULL and raises 23502.
func TestPutDraft_EmptyScopePointerNormalized(t *testing.T) {
	super, app := dbTestPools(t)

	for _, tc := range []struct {
		name  string
		scope *string
	}{
		{"empty string", ptr("")},
		{"whitespace", ptr("   ")},
		{"the literal default", ptr(scopeAllInvoices)},
		{"nil pointer (the control: the column is unchanged)", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tenantID := policyTenant(t, super, "APPR-05 put-scope-normalized")
			c, _ := activeAdmin(t, super, tenantID)
			policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
			versionID := seedDraftWithSteps(t, super, tenantID, policyID, 1)

			got, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, tc.scope, approvalStep("engagement-partner"))
			if err != nil {
				t.Fatalf("PutDraft(scope %s): %v (SQLSTATE %q) — an unnormalized scope reaching SQL raises "+
					"23514, and a nil one bound directly raises 23502", strOrNull(tc.scope), err, pgCode(err))
			}
			if stored := storedPolicyScope(t, super, policyID); stored != scopeAllInvoices {
				t.Errorf("STORED scope column = %q, want %q", stored, scopeAllInvoices)
			}
			if got.Scope != scopeAllInvoices {
				t.Errorf("returned scope = %q, want %q", got.Scope, scopeAllInvoices)
			}
			// The write still happened, so the scope assertions are not passing against a
			// store that refused the call.
			if steps := readStoredSteps(t, super, versionID); len(steps) != 1 || steps[0].Kind != "approval" {
				t.Errorf("draft holds %v, want the one submitted approval step", steps)
			}
		})
	}
}

// --- AC-6: a dangling role key is legal at draft time --------------------------

// TestPutDraft_AcceptsDanglingRoleKey: workflow_role_key is a deliberate non-FK and the
// live-role gate lives at publish's door and nowhere else. A draft naming a role that
// does not exist is accepted and persisted verbatim — that is what lets a role be
// deleted without silently demoting a draft that names it.
func TestPutDraft_AcceptsDanglingRoleKey(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-dangling-role")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)

	// The fixture is only meaningful while no such role exists.
	for _, key := range liveRoleKeys(t, super, tenantID) {
		if key == "ghost-role" {
			t.Fatalf("fixture: tenant %s already holds a ghost-role workflow role", tenantID)
		}
	}

	if _, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, nil, approvalStep("ghost-role")); err != nil {
		t.Fatalf("PutDraft naming an unknown workflow role: %v — the live-role gate is publish's, not the draft's", err)
	}

	steps := readStoredSteps(t, super, versionID)
	if len(steps) != 1 {
		t.Fatalf("draft holds %d steps, want 1: %v", len(steps), steps)
	}
	if steps[0].WorkflowRoleKey == nil || *steps[0].WorkflowRoleKey != "ghost-role" {
		t.Errorf("stored step = %v, want the key persisted verbatim as ghost-role", steps[0])
	}
}

// --- AC-7: absent fields ------------------------------------------------------

// TestPutDraft_AbsentNameLeavesColumnUnchanged: name and scope are pointers, and nil
// means "no change" — the steps are still replaced. The rename control keeps the
// unchanged assertion from passing against a store that never writes the name at all.
func TestPutDraft_AbsentNameLeavesColumnUnchanged(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-absent-name")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Original name")
	versionID := seedDraftWithSteps(t, super, tenantID, policyID, 2)
	store := NewStore(app, stubFingerprinter, nil)

	got, err := store.PutDraft(c, policyID, nil, nil, approvalStep("engagement-partner"))
	if err != nil {
		t.Fatalf("PutDraft with name and scope both nil: %v", err)
	}
	if stored := storedPolicyName(t, super, policyID); stored != "Original name" {
		t.Errorf("stored name = %q, want the untouched %q", stored, "Original name")
	}
	if stored := storedPolicyScope(t, super, policyID); stored != scopeAllInvoices {
		t.Errorf("stored scope = %q, want the untouched %q", stored, scopeAllInvoices)
	}
	if got.Name != "Original name" {
		t.Errorf("returned name = %q, want the stored %q", got.Name, "Original name")
	}
	if steps := readStoredSteps(t, super, versionID); len(steps) != 1 || steps[0].Kind != "approval" {
		t.Errorf("draft holds %v, want the one submitted approval step — an absent name does not "+
			"make the tree write optional", steps)
	}

	// Control: a non-nil name IS written, and trimmed the way normalizeName trims.
	renamed, err := store.PutDraft(c, policyID, ptr("  Renamed  "), nil, approvalStep("engagement-partner"))
	if err != nil {
		t.Fatalf("PutDraft with a name: %v — the unchanged assertions above are vacuous unless this writes", err)
	}
	if stored := storedPolicyName(t, super, policyID); stored != "Renamed" {
		t.Errorf("stored name = %q, want the trimmed %q", stored, "Renamed")
	}
	if renamed.Name != "Renamed" {
		t.Errorf("returned name = %q, want the trimmed %q", renamed.Name, "Renamed")
	}

	// A blank name is ErrValidation above the transaction, so nothing is written.
	if _, err := store.PutDraft(c, policyID, ptr("   "), nil, approvalStep("engagement-partner")); !errors.Is(err, ErrValidation) {
		t.Errorf("PutDraft with a blank name: err = %v, want ErrValidation", err)
	}
	if stored := storedPolicyName(t, super, policyID); stored != "Renamed" {
		t.Errorf("stored name = %q after the refused rename, want %q", stored, "Renamed")
	}
}

// TestPutDraft_NilStepsClearsTheTree: nil and an empty slice are the same store state,
// which is exactly why the handler owns the presence check — a body with no steps key
// must be a 400 there, because it cannot be told apart down here.
func TestPutDraft_NilStepsClearsTheTree(t *testing.T) {
	super, app := dbTestPools(t)

	for _, tc := range []struct {
		name  string
		steps []stepInput
	}{
		{"nil slice", nil},
		{"empty slice", []stepInput{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tenantID := policyTenant(t, super, "APPR-05 put-clears-tree")
			c, _ := activeAdmin(t, super, tenantID)
			policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
			versionID := seedDraftWithSteps(t, super, tenantID, policyID, 3)

			got, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, nil, tc.steps)
			if err != nil {
				t.Fatalf("PutDraft with %s steps: %v", tc.name, err)
			}
			if steps := readStoredSteps(t, super, versionID); len(steps) != 0 {
				t.Errorf("draft holds %d steps, want 0: %v", len(steps), steps)
			}
			if n := len(versionRows(t, super, policyID)); n != 1 {
				t.Errorf("policy has %d versions, want the 1 seeded — clearing rewrites the open draft", n)
			}
			if got.Steps == nil {
				t.Error("returned steps is nil; the producer must build []Step{} or the wire renders null")
			}
			if len(got.Steps) != 0 {
				t.Errorf("returned tree = %+v, want empty", got.Steps)
			}
		})
	}
}

// --- AC-8: the audit ----------------------------------------------------------

// TestPutDraft_AuditsInSameTx proves atomicity positively: rows sharing an xmin were
// written by one transaction. The rollback form ("neither row exists") passes vacuously
// against a two-transaction store, because any failure raised before the audit
// statement also leaves neither row behind.
//
// The fork fixture is what makes the version row's xmin the transaction's; the name is
// non-nil so the approval_policies UPDATE is unambiguously required too.
//
// policy_id is compared as a uuid on both sides. Bare, $2 resolves to uuid at p.id and the
// payload leg then asks for text = uuid, so the join fails to PLAN (42883) whatever the
// store wrote.
func TestPutDraft_AuditsInSameTx(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-audit-atomicity")
	c, adminID := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	seedSealedActiveV1(t, super, tenantID, policyID)

	if _, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, ptr("Renamed"), nil, approvalStep("engagement-partner")); err != nil {
		t.Fatalf("PutDraft: %v", err)
	}
	draft := openDraft(t, super, policyID)
	if draft.Version != 2 {
		t.Fatalf("the open draft is version %d, want the forked 2", draft.Version)
	}

	var policyXmin, versionXmin, stepXmin, auditXmin, actor, payload string
	if err := super.QueryRow(context.Background(),
		`SELECT p.xmin::text, v.xmin::text, s.xmin::text, a.xmin::text, a.actor, a.payload::text
		   FROM approval_policies p, approval_policy_versions v, approval_policy_steps s, audit_log a
		  WHERE p.tenant_id = $1 AND p.id = $2::uuid
		    AND v.id = $3::uuid
		    AND s.version_id = v.id
		    AND a.tenant_id = $1 AND a.event = 'approval_policy.updated'
		    AND (a.payload->>'policy_id')::uuid = $2::uuid`,
		tenantID, policyID, draft.ID,
	).Scan(&policyXmin, &versionXmin, &stepXmin, &auditXmin, &actor, &payload); err != nil {
		t.Fatalf("xmin join (no row means the policy, its forked version, its step and its audit event "+
			"do not all exist): %v", err)
	}
	// Frozen or invalid xids read as 2 and 0; either would make the comparison meaningless.
	for label, x := range map[string]string{
		"approval_policies":        policyXmin,
		"approval_policy_versions": versionXmin,
		"approval_policy_steps":    stepXmin,
		"audit_log":                auditXmin,
	} {
		if x == "0" || x == "2" {
			t.Fatalf("%s.xmin = %s — a frozen/invalid xid makes this proof vacuous", label, x)
		}
	}
	if policyXmin != versionXmin || policyXmin != stepXmin || policyXmin != auditXmin {
		t.Errorf("xmin: policies = %s, versions = %s, steps = %s, audit_log = %s — "+
			"every write must share one transaction", policyXmin, versionXmin, stepXmin, auditXmin)
	}
	if actor != adminID {
		t.Errorf("audit actor = %q, want the caller's subject %q", actor, adminID)
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(payload), &body); err != nil {
		t.Fatalf("audit payload %q is not an object: %v", payload, err)
	}
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"policy_id", "version"}) {
		t.Errorf("audit payload keys = %v, want [policy_id version]", keys)
	}
	if body["policy_id"] != policyID {
		t.Errorf("audit payload policy_id = %v, want %q", body["policy_id"], policyID)
	}
	if body["version"] != float64(2) {
		t.Errorf("audit payload version = %v, want the WRITTEN draft's version 2 — not the version "+
			"the policy held before the fork", body["version"])
	}
	if n := auditCount(t, super, tenantID, "approval_policy.updated"); n != 1 {
		t.Errorf("approval_policy.updated audit rows = %d, want 1", n)
	}
}

// --- AC-9: tenancy and not-found ----------------------------------------------

// TestPutDraft_CrossTenantIsNotFound: RLS is the only tenant filter, so tenant B's
// admin cannot rewrite tenant A's draft. B holds an ACTIVE ADMIN of its own — without
// one, requireActiveAdmin answers ErrNotPermitted first and the refusal would prove
// nothing about tenancy.
func TestPutDraft_CrossTenantIsNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app, stubFingerprinter, nil)

	tenantA := policyTenant(t, super, "APPR-05 put-cross-tenant A")
	cA, _ := activeAdmin(t, super, tenantA)
	policyA := seedApprovalPolicy(t, super, tenantA, "A policy")
	versionA := seedDraftWithSteps(t, super, tenantA, policyA, 1)
	before := readStoredSteps(t, super, versionA)

	tenantB := policyTenant(t, super, "APPR-05 put-cross-tenant B")
	cB, _ := activeAdmin(t, super, tenantB)
	policyB := seedApprovalPolicy(t, super, tenantB, "B policy")
	seedApprovalPolicyVersionN(t, super, tenantB, policyB, 1)

	_, err := store.PutDraft(cB, policyA, ptr("Hijacked"), nil, approvalStep("engagement-partner"))
	if !errors.Is(err, ErrPolicyNotFound) {
		t.Errorf("PutDraft(A's policy) as B: err = %v, want ErrPolicyNotFound", err)
	}
	if code := pgCode(err); code != "" {
		t.Errorf("PutDraft(A's policy) as B surfaced a raw Postgres error (SQLSTATE %s) — it answers 500, not 404", code)
	}
	if got := storedPolicyName(t, super, policyA); got != "A policy" {
		t.Errorf("A's name = %q, want the untouched %q", got, "A policy")
	}
	if got := readStoredSteps(t, super, versionA); !reflect.DeepEqual(got, before) {
		t.Errorf("A's steps changed:\n  before %v\n  after  %v", before, got)
	}
	if n := len(versionRows(t, super, policyA)); n != 1 {
		t.Errorf("A's policy has %d versions, want the 1 seeded — a refused cross-tenant PUT forks nothing", n)
	}
	if n := auditCount(t, super, tenantB, "approval_policy.updated"); n != 0 {
		t.Errorf("approval_policy.updated audit rows under B = %d, want 0", n)
	}

	// Control: A can still rewrite its own, so the refusal is not a store that refuses
	// everyone.
	if _, err := store.PutDraft(cA, policyA, nil, nil, approvalStep("engagement-partner")); err != nil {
		t.Fatalf("PutDraft(A's policy) as A: %v — the refusal above is vacuous unless this succeeds", err)
	}
}

// TestPutDraft_MalformedIdIsNotFound: a malformed id must never reach SQL. 22P02 carries
// no constraint name, so nothing downstream can map it off 500, and 400 against 404 on a
// path resource would be an existence oracle. The unknown-uuid and soft-deleted cases
// ride along: deleted_at IS NULL is the existence predicate, and a PUT that reopens a
// deleted policy's draft would resurrect it.
func TestPutDraft_MalformedIdIsNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 put-not-found")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	liveID := seedApprovalPolicy(t, super, tenantID, "Live")
	liveVersion := seedDraftWithSteps(t, super, tenantID, liveID, 1)
	deletedID := seedApprovalPolicy(t, super, tenantID, "Deleted")
	deletedVersion := seedDraftWithSteps(t, super, tenantID, deletedID, 1)
	softDeleteApprovalPolicy(t, super, deletedID)
	deletedBefore := readStoredSteps(t, super, deletedVersion)

	for _, tc := range []struct {
		name string
		id   string
	}{
		{"malformed id", "not-a-uuid"},
		{"unknown uuid", uuid.NewString()},
		{"soft-deleted policy", deletedID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.PutDraft(c, tc.id, ptr("Renamed"), nil, approvalStep("engagement-partner"))
			if !errors.Is(err, ErrPolicyNotFound) {
				t.Errorf("PutDraft(%q) err = %v, want ErrPolicyNotFound", tc.id, err)
			}
			if code := pgCode(err); code != "" {
				t.Errorf("PutDraft(%q) surfaced a raw Postgres error (SQLSTATE %s) — it answers 500, not 404", tc.id, code)
			}
		})
	}

	if got := readStoredSteps(t, super, deletedVersion); !reflect.DeepEqual(got, deletedBefore) {
		t.Errorf("the soft-deleted policy's steps changed:\n  before %v\n  after  %v", deletedBefore, got)
	}
	if got := storedPolicyName(t, super, deletedID); got != "Deleted" {
		t.Errorf("the soft-deleted policy's name = %q, want the untouched %q", got, "Deleted")
	}
	if n := auditCount(t, super, tenantID, "approval_policy.updated"); n != 0 {
		t.Errorf("approval_policy.updated audit rows = %d, want 0", n)
	}

	// Control: the live policy is still writable, so the refusals are not a store that
	// finds nothing.
	if _, err := store.PutDraft(c, liveID, nil, nil, approvalStep("engagement-partner")); err != nil {
		t.Fatalf("PutDraft on the live policy: %v — the refusals above are vacuous unless this succeeds", err)
	}
	if steps := readStoredSteps(t, super, liveVersion); len(steps) != 1 || steps[0].Kind != "approval" {
		t.Errorf("the live draft holds %v, want the one submitted approval step", steps)
	}
}
