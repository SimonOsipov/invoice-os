package approval

// Store.PublishPolicy under a real Postgres: the publish gate, the seal, the tenant-wide
// deactivation and the audit. Adversarial coverage — the row lock, the admin gate, the
// concurrent-publish loser — is in policy_publish_adversarial_test.go.
//
// TWO KNOWN UNTESTED OBLIGATIONS, both recorded rather than faked. Both were discharged by
// reading policy_store.go, and both stay untested: mutating either leaves this suite green.
//
//  1. "seal and activate are ONE statement" has no oracle. Statement count inside a
//     transaction is invisible from outside it — it is observable only by scraping the
//     server log under log_statement=all, which is not enabled and is not a test oracle.
//     TestPublish_SealAndActivateAreOneStatement keeps its name and pins what IS
//     observable; the one-statement form itself is a code-review obligation.
//  2. "the audit error propagates" — i.e. the code is not `_ = audit.Record(...)` — has no
//     oracle here either. The xmin join proves the two rows share a transaction, but a
//     swallowed error still writes the row on the happy path, so the join stays green
//     while the guarantee is gone. Proving it needs a temporary BEFORE INSERT trigger on
//     audit_log, a shared migrator-owned table on a DB shared across worktrees, with no
//     precedent in this repo; it is deliberately NOT built. The vacuous "roll the tx back
//     and assert neither row exists" form is not a substitute and is not used.
//
// Run locally with `DEV_DB_PORT=5433 make test-approvals`; in CI the rls job's gate step
// fails the build on any skip.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// --- fixtures ---------------------------------------------------------------

// versionRow reads one version row by id. versionRows reads a whole policy's, which
// cannot address the outgoing version of a DIFFERENT policy the tenant-wide deactivation
// touches.
func versionRow(t *testing.T, super *pgxpool.Pool, versionID string) storedVersion {
	t.Helper()
	var v storedVersion
	if err := super.QueryRow(context.Background(),
		`SELECT id, version, sealed, is_active, published_at::text, published_by
		   FROM approval_policy_versions WHERE id = $1`, versionID,
	).Scan(&v.ID, &v.Version, &v.Sealed, &v.IsActive, &v.PublishedAt, &v.PublishedBy); err != nil {
		t.Fatalf("read version %s: %v", versionID, err)
	}
	return v
}

// activeVersionIDs reads the tenant's active version ids. Tenant-scoped, never
// policy-scoped: approval_policy_versions_one_active is ON (tenant_id), so "exactly one
// active version" is a property of the TENANT and a per-policy read cannot see a second
// policy holding the slot.
func activeVersionIDs(t *testing.T, super *pgxpool.Pool, tenantID string) []string {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT id FROM approval_policy_versions WHERE tenant_id = $1 AND is_active ORDER BY id`, tenantID)
	if err != nil {
		t.Fatalf("read active versions of tenant %s: %v", tenantID, err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan active version id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read active versions of tenant %s: %v", tenantID, err)
	}
	return ids
}

// assertActiveImpliesSealed is the invariant the one-statement form exists to protect,
// stated over the whole tenant rather than the published row.
func assertActiveImpliesSealed(t *testing.T, super *pgxpool.Pool, tenantID string) {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM approval_policy_versions WHERE tenant_id = $1 AND is_active AND NOT sealed`,
		tenantID).Scan(&n); err != nil {
		t.Fatalf("count active-but-unsealed versions: %v", err)
	}
	if n != 0 {
		t.Errorf("active-but-unsealed version rows = %d, want 0 — is_active is never true on a row that "+
			"is not already, or not simultaneously becoming, sealed", n)
	}
}

// seedApprovalDraft gives a policy an open, stepless version 1 — the smallest tree that
// publishes.
func seedApprovalDraft(t *testing.T, super *pgxpool.Pool, tenantID, policyID string) string {
	t.Helper()
	return seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
}

// seedApprovalDraftNamingRole gives a policy an open version 1 whose only step is an
// approval naming roleKey, and returns the version and step ids.
func seedApprovalDraftNamingRole(t *testing.T, super *pgxpool.Pool, tenantID, policyID, roleKey string) (versionID, stepID string) {
	t.Helper()
	versionID = seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	stepID = seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr(roleKey),
	})
	return versionID, stepID
}

// --- AC-1: an approval step must name a live role -----------------------------

// TestPublish_RejectsDanglingRole: the door refuses a NEW version naming a soft-deleted
// role. The sentinel is asserted positively AND the empty-lanes one negatively — a gate
// that answered the wrong 409 would be indistinguishable through the status code alone.
func TestPublish_RejectsDanglingRole(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 publish-dangling-role")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app)

	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	softDeleteWorkflowRole(t, super, roleID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID, _ := seedApprovalDraftNamingRole(t, super, tenantID, policyID, "tax-reviewer")

	_, err := store.PublishPolicy(c, policyID)
	if !errors.Is(err, ErrPolicyStepRole) {
		t.Errorf("PublishPolicy naming a soft-deleted role: err = %v, want ErrPolicyStepRole", err)
	}
	if errors.Is(err, ErrPolicyEmptyBranches) {
		t.Errorf("PublishPolicy: err = %v, want the step-role sentinel, not the empty-lanes one", err)
	}

	// Nothing is written: the version stays a draft and no publish event exists. This is
	// the no-write-after-409 half of the audit-atomicity proof.
	v := versionRow(t, super, versionID)
	if v.Sealed || v.IsActive {
		t.Errorf("version row = %+v, want still (sealed false, is_active false)", v)
	}
	if v.PublishedAt != nil || v.PublishedBy != nil {
		t.Errorf("published_at/by = (%s, %s), want both NULL after a refused publish",
			strOrNull(v.PublishedAt), strOrNull(v.PublishedBy))
	}
	if n := auditCount(t, super, tenantID, "approval_policy.published"); n != 0 {
		t.Errorf("approval_policy.published audit rows = %d, want 0", n)
	}
	if ids := activeVersionIDs(t, super, tenantID); len(ids) != 0 {
		t.Errorf("active version ids = %v, want none — a refused publish activates nothing", ids)
	}

	// Control: the same shape naming a LIVE role publishes, so the refusal above is not a
	// store that refuses everything.
	seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")
	livePolicyID := seedApprovalPolicy(t, super, tenantID, "Live sign-off")
	seedApprovalDraftNamingRole(t, super, tenantID, livePolicyID, "engagement-partner")
	if _, err := store.PublishPolicy(c, livePolicyID); err != nil {
		t.Fatalf("control: PublishPolicy naming a live role: %v, want success — the refusal above "+
			"is vacuous unless this succeeds", err)
	}
}

// TestPublish_RejectsEmptyRoleKey: "empty" covers both spellings the column allows. NULL
// is the one PutDraft can actually produce — stepInput.WorkflowRoleKey is a *string and
// validateTree lets an approval step through without it — so testing only "" would leave
// the reachable shape uncovered.
func TestPublish_RejectsEmptyRoleKey(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 publish-empty-role-key")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app)

	// A live role exists, so a gate that answered ErrPolicyStepRole because the tenant has
	// no roles at all would not pass this.
	seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")

	cases := []struct {
		name string
		key  *string
	}{
		{"the empty string", ptr("")},
		{"NULL", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off "+tc.name)
			versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
			seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
				Ord: 0, Kind: "approval", WorkflowRoleKey: tc.key,
			})

			_, err := store.PublishPolicy(c, policyID)
			if !errors.Is(err, ErrPolicyStepRole) {
				t.Errorf("PublishPolicy with role key %s: err = %v, want ErrPolicyStepRole", tc.name, err)
			}
			v := versionRow(t, super, versionID)
			if v.Sealed || v.IsActive {
				t.Errorf("version row = %+v, want still (sealed false, is_active false)", v)
			}
		})
	}
	if n := auditCount(t, super, tenantID, "approval_policy.published"); n != 0 {
		t.Errorf("approval_policy.published audit rows = %d, want 0", n)
	}
}

// --- AC-2: a condition must have somewhere to go ------------------------------

// TestPublish_RejectsEmptyBothLanes: a condition with two empty lanes is refused, and a
// condition with ONE populated lane publishes. Both halves are needed — a gate that
// refused every condition would pass the refusal alone.
func TestPublish_RejectsEmptyBothLanes(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 publish-empty-lanes")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app)

	deadPolicyID := seedApprovalPolicy(t, super, tenantID, "Two empty lanes")
	deadVersionID := seedApprovalPolicyVersionN(t, super, tenantID, deadPolicyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, deadVersionID, seedStepSpec{
		Ord: 0, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("1000.00"),
	})

	_, err := store.PublishPolicy(c, deadPolicyID)
	if !errors.Is(err, ErrPolicyEmptyBranches) {
		t.Errorf("PublishPolicy with two empty lanes: err = %v, want ErrPolicyEmptyBranches", err)
	}
	if errors.Is(err, ErrPolicyStepRole) {
		t.Errorf("PublishPolicy: err = %v, want the empty-lanes sentinel, not the step-role one", err)
	}
	v := versionRow(t, super, deadVersionID)
	if v.Sealed || v.IsActive {
		t.Errorf("version row = %+v, want still (sealed false, is_active false)", v)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.published"); n != 0 {
		t.Errorf("approval_policy.published audit rows = %d, want 0", n)
	}

	// Positive control: one step in the ELSE lane is enough. A notify step, so this stays
	// a test of the branch rule and never of the role rule.
	livePolicyID := seedApprovalPolicy(t, super, tenantID, "One populated lane")
	liveVersionID := seedApprovalPolicyVersionN(t, super, tenantID, livePolicyID, 1)
	condID := seedApprovalPolicyStepInLane(t, super, tenantID, liveVersionID, seedStepSpec{
		Ord: 0, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("1000.00"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, liveVersionID, seedStepSpec{
		ParentStepID: &condID, Branch: ptr("else"), Ord: 0, Kind: "notify",
		NotifyTarget: ptr("ops@example.com"), NotifyChannel: ptr("email"),
	})
	if _, err := store.PublishPolicy(c, livePolicyID); err != nil {
		t.Fatalf("control: PublishPolicy with one populated lane: %v, want success", err)
	}
}

// --- AC-3: an empty policy publishes ------------------------------------------

// TestPublish_EmptyPolicyAllowed: zero steps is a legal published tree.
//
// The returned Policy is asserted too, and specifically its Versions[0]: the version list
// must be read AFTER the seal statement. Read before it, the response answers
// sealed:false, is_active:false, published_at:null on a row that is none of those things,
// and only a later GET would contradict it.
func TestPublish_EmptyPolicyAllowed(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 publish-empty-policy")
	c, adminID := activeAdmin(t, super, tenantID)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalDraft(t, super, tenantID, policyID)

	got, err := NewStore(app).PublishPolicy(c, policyID)
	if err != nil {
		t.Fatalf("PublishPolicy of a stepless draft: %v, want success", err)
	}

	v := versionRow(t, super, versionID)
	if !v.Sealed || !v.IsActive {
		t.Errorf("version row = %+v, want (sealed true, is_active true)", v)
	}
	if ids := activeVersionIDs(t, super, tenantID); !reflect.DeepEqual(ids, []string{versionID}) {
		t.Errorf("active version ids = %v, want exactly [%s]", ids, versionID)
	}
	if n := rowCount(t, super, "approval_policy_steps", tenantID); n != 0 {
		t.Errorf("step rows = %d, want 0 — publishing mints no steps", n)
	}

	if got.ID != policyID || got.Version != 1 || !got.Sealed || got.Status != "published" {
		t.Errorf("returned policy = (id %q, version %d, sealed %v, status %q), want (%q, 1, true, published)",
			got.ID, got.Version, got.Sealed, got.Status, policyID)
	}
	if got.Steps == nil {
		t.Error("returned steps is nil; it must be []Step{} or the wire renders null")
	}
	if len(got.Steps) != 0 {
		t.Errorf("returned tree has %d root steps, want 0: %+v", len(got.Steps), got.Steps)
	}
	if len(got.Versions) != 1 {
		t.Fatalf("returned versions = %+v, want exactly one", got.Versions)
	}
	pv := got.Versions[0]
	if pv.Version != 1 || !pv.Sealed || !pv.IsActive {
		t.Errorf("returned versions[0] = %+v, want (version 1, sealed true, is_active true) — "+
			"the version list must be read AFTER the seal statement", pv)
	}
	if pv.PublishedAt == nil {
		t.Error("returned versions[0].published_at is null, want the publish timestamp — the version " +
			"list must be read AFTER the seal statement")
	}
	if pv.PublishedBy == nil || *pv.PublishedBy != adminID {
		t.Errorf("returned versions[0].published_by = %s, want the caller's subject %q",
			strOrNull(pv.PublishedBy), adminID)
	}
}

// --- AC-4: the deactivation is TENANT-wide ------------------------------------

// TestPublish_DeactivatesTheTenantsOtherPolicy: approval_policy_versions_one_active is
// ON (tenant_id) WHERE is_active, so publishing policy B deactivates policy A's version.
// Deactivation is not un-publishing: A keeps sealed, published_at and published_by.
func TestPublish_DeactivatesTheTenantsOtherPolicy(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 publish-deactivates-other")
	c, _ := activeAdmin(t, super, tenantID)

	policyA := seedApprovalPolicy(t, super, tenantID, "A policy")
	versionA := seedApprovalPolicyVersionN(t, super, tenantID, policyA, 1)
	publishApprovalPolicyVersion(t, super, versionA, "earlier-publisher")
	beforeA := versionRow(t, super, versionA)
	if beforeA.PublishedAt == nil {
		t.Fatal("fixture: A's published_at is NULL, so the retention assertion below would be vacuous")
	}

	policyB := seedApprovalPolicy(t, super, tenantID, "B policy")
	versionB := seedApprovalDraft(t, super, tenantID, policyB)

	if _, err := NewStore(app).PublishPolicy(c, policyB); err != nil {
		t.Fatalf("PublishPolicy(B): %v, want success", err)
	}

	afterB := versionRow(t, super, versionB)
	if !afterB.Sealed || !afterB.IsActive {
		t.Errorf("B's version = %+v, want (sealed true, is_active true)", afterB)
	}
	afterA := versionRow(t, super, versionA)
	if !afterA.Sealed || afterA.IsActive {
		t.Errorf("A's version = %+v, want (sealed true, is_active false) — the deactivation statement "+
			"carries no policy predicate", afterA)
	}
	if afterA.PublishedAt == nil || *afterA.PublishedAt != *beforeA.PublishedAt {
		t.Errorf("A's published_at = %s, want the unchanged %s — deactivation is not un-publishing",
			strOrNull(afterA.PublishedAt), strOrNull(beforeA.PublishedAt))
	}
	if afterA.PublishedBy == nil || *afterA.PublishedBy != "earlier-publisher" {
		t.Errorf("A's published_by = %s, want the unchanged %q", strOrNull(afterA.PublishedBy), "earlier-publisher")
	}
	if ids := activeVersionIDs(t, super, tenantID); !reflect.DeepEqual(ids, []string{versionB}) {
		t.Errorf("active version ids = %v, want exactly [%s]", ids, versionB)
	}
}

// --- AC-5: the seal, the activation and the order they happen in --------------

// TestPublish_SealAndActivateAreOneStatement keeps the Core AC's name, but the name
// overstates what any test can see: statement count inside a transaction is unobservable
// from outside it, and controls (i) and (ii) below are properties of the SCHEMA, true
// whatever the store does. The whole test passes against a seal-then-activate
// two-statement store. One-statement is a CODE-REVIEW obligation, recorded in this file's
// header; what is pinned here is the invariant it protects:
//
//	(1) is_active is never true on a row that is not already, or not simultaneously
//	    becoming, sealed — neither 23514 nor 23505 is ever raised; and
//	(2) the tenant's outgoing active version is deactivated in an EARLIER statement, so
//	    the end state is exactly one active row.
//
// The plan's stated reason — "any two-statement form of step 8 transiently violates a
// constraint" — is false: deactivate, then SET sealed=true, then SET is_active=true as
// three statements all succeed. Only activate-before-seal is illegal.
func TestPublish_SealAndActivateAreOneStatement(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := policyTenant(t, super, "APPR-05 publish-seal-and-activate")
	c, _ := activeAdmin(t, super, tenantID)

	outgoingPolicy := seedApprovalPolicy(t, super, tenantID, "Outgoing")
	outgoingVersion := seedApprovalPolicyVersionN(t, super, tenantID, outgoingPolicy, 1)
	publishApprovalPolicyVersion(t, super, outgoingVersion, "earlier-publisher")

	incomingPolicy := seedApprovalPolicy(t, super, tenantID, "Incoming")
	incomingVersion := seedApprovalDraft(t, super, tenantID, incomingPolicy)

	// The two controls run in their own transaction, rolled back BEFORE the real publish:
	// a savepoint rollback releases no row lock, so leaving this tx open would block the
	// publish's UPDATE of the same row.
	func() {
		tx, err := super.Begin(ctx)
		if err != nil {
			t.Fatalf("begin control tx: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		setTenantGUC(t, ctx, tx, tenantID)

		// (i) activate before seal.
		assertSQLState(t, attemptWithSavepoint(t, ctx, tx,
			`UPDATE approval_policy_versions SET is_active = true WHERE id = $1`, incomingVersion), "23514")

		// (ii) seal + activate while another version holds the tenant's slot.
		opErr := attemptWithSavepoint(t, ctx, tx,
			`UPDATE approval_policy_versions SET sealed = true, is_active = true WHERE id = $1`, incomingVersion)
		assertSQLState(t, opErr, "23505")
		if name := pgConstraint(opErr); name != "approval_policy_versions_one_active" {
			t.Errorf("constraint = %q, want approval_policy_versions_one_active", name)
		}
	}()

	// Nothing above committed, so the real publish starts from the seeded state.
	if v := versionRow(t, super, incomingVersion); v.Sealed || v.IsActive {
		t.Fatalf("incoming version = %+v after the rolled-back controls, want still (sealed false, is_active false)", v)
	}

	if _, err := NewStore(app).PublishPolicy(c, incomingPolicy); err != nil {
		t.Fatalf("PublishPolicy: %v, want success — the deactivation must precede the activation, or "+
			"this is the 23505 control (ii) just measured", err)
	}

	if ids := activeVersionIDs(t, super, tenantID); !reflect.DeepEqual(ids, []string{incomingVersion}) {
		t.Errorf("active version ids = %v, want exactly [%s]", ids, incomingVersion)
	}
	if v := versionRow(t, super, incomingVersion); !v.Sealed || !v.IsActive {
		t.Errorf("incoming version = %+v, want (sealed true, is_active true)", v)
	}
	if v := versionRow(t, super, outgoingVersion); !v.Sealed || v.IsActive {
		t.Errorf("outgoing version = %+v, want (sealed true, is_active false)", v)
	}
	assertActiveImpliesSealed(t, super, tenantID)
}

// --- AC-6: the actor and the transaction timestamp ----------------------------

// TestPublish_StampsActorAndTxTimestamp: published_by is auth.Identity.Subject and
// published_at is now(), so it equals the audit row's created_at. Both columns are
// asserted together — no schema CHECK enforces both-or-neither (recorded as a gap for the
// epic; this story makes one schema change and it is not this one), so the only guard is
// this assertion and AC-4's retention one.
func TestPublish_StampsActorAndTxTimestamp(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 publish-stamps-actor")
	c, adminID := activeAdmin(t, super, tenantID)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalDraft(t, super, tenantID, policyID)

	if _, err := NewStore(app).PublishPolicy(c, policyID); err != nil {
		t.Fatalf("PublishPolicy: %v, want success", err)
	}

	v := versionRow(t, super, versionID)
	if (v.PublishedAt == nil) != (v.PublishedBy == nil) {
		t.Errorf("published_at/by = (%s, %s), want both set or both NULL",
			strOrNull(v.PublishedAt), strOrNull(v.PublishedBy))
	}
	if v.PublishedBy == nil || *v.PublishedBy != adminID {
		t.Errorf("published_by = %s, want the caller's subject %q — never a body field",
			strOrNull(v.PublishedBy), adminID)
	}
	if v.PublishedAt == nil {
		t.Fatal("published_at is NULL after a successful publish")
	}

	// now() is the TRANSACTION timestamp, so it is the same instant the audit row's
	// created_at DEFAULT takes. A Go clock, or a statement_timestamp(), would not be.
	var sameInstant bool
	if err := super.QueryRow(context.Background(),
		`SELECT v.published_at = a.created_at
		   FROM approval_policy_versions v, audit_log a
		  WHERE v.id = $1::uuid
		    AND a.tenant_id = $2 AND a.event = 'approval_policy.published'
		    AND (a.payload->>'policy_id')::uuid = $3::uuid`,
		versionID, tenantID, policyID,
	).Scan(&sameInstant); err != nil {
		t.Fatalf("compare published_at with the audit row's created_at (no row means the audit event "+
			"does not exist): %v", err)
	}
	if !sameInstant {
		t.Error("published_at differs from the audit row's created_at — published_at must be now(), " +
			"the transaction timestamp")
	}
}

// --- AC-7: nothing to publish -------------------------------------------------

// TestPublish_SecondPublishIsNothingToPublish: the resolution predicate is
// "WHERE policy_id = $1 AND NOT sealed", so once the draft is sealed there is nothing
// left to resolve. The second call must not re-stamp the sealed row.
func TestPublish_SecondPublishIsNothingToPublish(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 publish-nothing-to-publish")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app)

	seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID, stepID := seedApprovalDraftNamingRole(t, super, tenantID, policyID, "engagement-partner")

	if _, err := store.PublishPolicy(c, policyID); err != nil {
		t.Fatalf("first PublishPolicy: %v, want success", err)
	}
	beforeStep := stepSnapshot(t, super, stepID)
	beforeVersion := versionRow(t, super, versionID)
	if beforeVersion.PublishedAt == nil {
		t.Fatal("the first publish left published_at NULL, so the did-not-move assertion below would be vacuous")
	}

	_, err := store.PublishPolicy(c, policyID)
	if !errors.Is(err, ErrPolicyNothingToPublish) {
		t.Errorf("second PublishPolicy: err = %v, want ErrPolicyNothingToPublish", err)
	}

	if after := stepSnapshot(t, super, stepID); after != beforeStep {
		t.Errorf("step snapshot after the refused second publish = %s, want unchanged %s", after, beforeStep)
	}
	afterVersion := versionRow(t, super, versionID)
	if afterVersion.PublishedAt == nil || *afterVersion.PublishedAt != *beforeVersion.PublishedAt {
		t.Errorf("published_at = %s after the refused second publish, want the unchanged %s",
			strOrNull(afterVersion.PublishedAt), strOrNull(beforeVersion.PublishedAt))
	}
	if !afterVersion.Sealed || !afterVersion.IsActive {
		t.Errorf("version row = %+v, want still (sealed true, is_active true)", afterVersion)
	}
	if n := len(versionRows(t, super, policyID)); n != 1 {
		t.Errorf("policy has %d versions, want 1 — a refused publish mints nothing", n)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.published"); n != 1 {
		t.Errorf("approval_policy.published audit rows = %d, want 1 — the refusal audits nothing", n)
	}
}

// --- AC-8: a role deleted AFTER publish ---------------------------------------

// TestPublish_RoleDeletedAfterPublishLeavesVersionActive is the other half of the door
// distinction: the sealed version keeps its step and stays in force, and only the NEXT
// publish of that same tree is refused. TestPublish_RejectsDanglingRole is the pair.
func TestPublish_RoleDeletedAfterPublishLeavesVersionActive(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 publish-role-deleted-after")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app)

	seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID, stepID := seedApprovalDraftNamingRole(t, super, tenantID, policyID, "tax-reviewer")

	if _, err := store.PublishPolicy(c, policyID); err != nil {
		t.Fatalf("PublishPolicy naming a live role: %v, want success", err)
	}
	before := stepSnapshot(t, super, stepID)

	if _, err := store.DeleteRole(c, "tax-reviewer"); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}

	v := versionRow(t, super, versionID)
	if !v.Sealed || !v.IsActive {
		t.Errorf("version row = %+v after the role was deleted, want still (sealed true, is_active true)", v)
	}
	if after := stepSnapshot(t, super, stepID); after != before {
		t.Errorf("step snapshot after the role was deleted = %s, want unchanged %s — workflow_role_key "+
			"carries no FK", after, before)
	}

	// The pair: republishing the same tree is now refused at the door.
	if _, err := store.PutDraft(c, policyID, nil, nil, approvalStep("tax-reviewer")); err != nil {
		t.Fatalf("PutDraft of the same tree: %v, want success — only PUBLISH is gated on live roles", err)
	}
	if _, err := store.PublishPolicy(c, policyID); !errors.Is(err, ErrPolicyStepRole) {
		t.Errorf("PublishPolicy of the re-drafted tree: err = %v, want ErrPolicyStepRole", err)
	}
	if v := versionRow(t, super, versionID); !v.Sealed || !v.IsActive {
		t.Errorf("the published version = %+v after the refused republish, want still "+
			"(sealed true, is_active true)", v)
	}
}

// --- AC-9: the audit ----------------------------------------------------------

// TestPublish_AuditsInSameTx proves atomicity positively: rows sharing an xmin were
// written by one transaction. The rollback form ("neither row exists") passes vacuously
// against a two-transaction store, because any failure raised before the audit statement
// also leaves neither row behind.
//
// The join is against the VERSION row only — publish never writes approval_policies or
// approval_policy_steps, so their xmin is still the seed's xid and including them would
// fail a correct implementation. See this file's header for what the join does NOT prove.
func TestPublish_AuditsInSameTx(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 publish-audit-atomicity")
	c, adminID := activeAdmin(t, super, tenantID)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalDraft(t, super, tenantID, policyID)

	if _, err := NewStore(app).PublishPolicy(c, policyID); err != nil {
		t.Fatalf("PublishPolicy: %v, want success", err)
	}

	// policy_id is compared as a uuid on both sides: bare, $2 resolves to uuid at v.id and
	// the payload leg then asks for text = uuid, so the join fails to PLAN whatever the
	// store wrote.
	var versionXmin, auditXmin, actor, payload string
	if err := super.QueryRow(context.Background(),
		`SELECT v.xmin::text, a.xmin::text, a.actor, a.payload::text
		   FROM approval_policy_versions v, audit_log a
		  WHERE v.id = $1::uuid
		    AND a.tenant_id = $2 AND a.event = 'approval_policy.published'
		    AND (a.payload->>'policy_id')::uuid = $3::uuid`,
		versionID, tenantID, policyID,
	).Scan(&versionXmin, &auditXmin, &actor, &payload); err != nil {
		t.Fatalf("xmin join (no row means the sealed version and its audit event do not both exist): %v", err)
	}
	// Frozen or invalid xids read as 2 and 0; either would make the comparison meaningless.
	for label, x := range map[string]string{"approval_policy_versions": versionXmin, "audit_log": auditXmin} {
		if x == "0" || x == "2" {
			t.Fatalf("%s.xmin = %s — a frozen/invalid xid makes this proof vacuous", label, x)
		}
	}
	if versionXmin != auditXmin {
		t.Errorf("xmin: versions = %s, audit_log = %s — the seal and the audit must share one transaction",
			versionXmin, auditXmin)
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
	if body["version"] != float64(1) {
		t.Errorf("audit payload version = %v, want the SEALED version's number 1", body["version"])
	}
	if n := auditCount(t, super, tenantID, "approval_policy.published"); n != 1 {
		t.Errorf("approval_policy.published audit rows = %d, want 1", n)
	}
}

// --- AC-10: tenancy -----------------------------------------------------------

// TestPublish_CrossTenantIsNotFound: RLS is the only tenant filter, so tenant B's admin
// cannot publish tenant A's draft. B holds an ACTIVE ADMIN of its own — without one,
// requireActiveAdmin answers ErrNotPermitted first and the refusal would prove nothing
// about tenancy.
func TestPublish_CrossTenantIsNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	tenantA := policyTenant(t, super, "APPR-05 publish-cross-tenant A")
	cA, _ := activeAdmin(t, super, tenantA)
	policyA := seedApprovalPolicy(t, super, tenantA, "A policy")
	versionA := seedApprovalDraft(t, super, tenantA, policyA)

	tenantB := policyTenant(t, super, "APPR-05 publish-cross-tenant B")
	cB, _ := activeAdmin(t, super, tenantB)

	_, err := store.PublishPolicy(cB, policyA)
	if !errors.Is(err, ErrPolicyNotFound) {
		t.Errorf("PublishPolicy(A's policy) as B: err = %v, want ErrPolicyNotFound", err)
	}
	if code := pgCode(err); code != "" {
		t.Errorf("PublishPolicy(A's policy) as B surfaced a raw Postgres error (SQLSTATE %s) — it answers "+
			"500, not 404", code)
	}
	if v := versionRow(t, super, versionA); v.Sealed || v.IsActive {
		t.Errorf("A's version = %+v, want still (sealed false, is_active false)", v)
	}
	if n := auditCount(t, super, tenantB, "approval_policy.published"); n != 0 {
		t.Errorf("approval_policy.published audit rows under B = %d, want 0", n)
	}

	// Control: A can still publish its own, so the refusal is not a store that refuses
	// everyone.
	if _, err := store.PublishPolicy(cA, policyA); err != nil {
		t.Fatalf("PublishPolicy(A's policy) as A: %v — the refusal above is vacuous unless this succeeds", err)
	}
}

// --- AC-11: no run is armed ---------------------------------------------------

// TestPublish_CreatesNoApprovalRun: arming is APPR-06's, not this subtask's. The counts
// go through the SUPERUSER pool — all three tables are FORCE RLS, so an app-role count
// with no tenant GUC set reads 0 whatever was written.
func TestPublish_CreatesNoApprovalRun(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 publish-no-run")
	c, _ := activeAdmin(t, super, tenantID)

	// Validated invoices are the population an arming sweep would walk.
	entityID := seedBusinessEntity(t, super, tenantID, "No-run Corp")
	for _, number := range []string{"no-run-invoice-1", "no-run-invoice-2"} {
		invoiceID := seedInvoice(t, super, tenantID, entityID, number)
		if _, err := super.Exec(context.Background(),
			`UPDATE invoices SET status = 'validated' WHERE id = $1`, invoiceID); err != nil {
			t.Fatalf("validate invoice %s: %v", number, err)
		}
	}
	var validated int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM invoices WHERE tenant_id = $1 AND status = 'validated'`, tenantID,
	).Scan(&validated); err != nil {
		t.Fatalf("count validated invoices: %v", err)
	}
	if validated != 2 {
		t.Fatalf("validated invoices = %d, want 2 — with none, this spec passes vacuously", validated)
	}

	seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	seedApprovalDraftNamingRole(t, super, tenantID, policyID, "engagement-partner")

	if _, err := NewStore(app).PublishPolicy(c, policyID); err != nil {
		t.Fatalf("PublishPolicy: %v, want success — a failed publish would make the counts below vacuous", err)
	}

	for _, table := range []string{"approval_runs", "approval_run_steps", "approval_decisions"} {
		if n := rowCount(t, super, table, tenantID); n != 0 {
			t.Errorf("%s rows = %d, want 0 — publish arms nothing", table, n)
		}
	}
}
