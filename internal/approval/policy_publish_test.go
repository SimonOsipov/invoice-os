package approval

// Store.PublishPolicy under a real Postgres: the publish gate, the seal, the deactivation of
// the policy's own previous version and the audit. Adversarial coverage — the row lock, the admin gate, the
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
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- fixtures ---------------------------------------------------------------

// versionRow reads one version row by id. versionRows reads a whole policy's, which
// cannot address the version of a DIFFERENT policy that must stay active.
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

// activeVersionIDs reads the tenant's active version ids. Tenant-scoped, so a second policy
// holding an active version is visible.
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

// stubFingerprintValue is a well-formed 64-char hex literal -- the shape a real
// content_fingerprint must have, without computing one.
var stubFingerprintValue = strings.Repeat("f", 64)

// stubFingerprinter is the local Fingerprinter double every approval-package sweep
// test builds its Store with (D1, task-484): internal/approval cannot import
// internal/invoice ("import cycle not allowed in test", proven against every one of
// this package's 23 test files), so the real invoice.FingerprintTx is exercised only
// from internal/invoice's TestPublish_SweepFingerprintMatchesInvoiceContent. This is
// the func-type seam NewStore's new parameter exists for.
func stubFingerprinter(ctx context.Context, tx pgx.Tx, invoiceID string) (string, error) {
	return stubFingerprintValue, nil
}

// bulkSeedValidatedInvoices inserts n validated invoices under one business entity in a
// single INSERT ... SELECT FROM generate_series -- ~65ms at n=5000 (measured dev :5433).
// Numbers are prefix-g for uniqueness; only tenant_id, entity_id and invoice_number
// lack column defaults.
func bulkSeedValidatedInvoices(t *testing.T, super *pgxpool.Pool, tenantID, entityID, prefix string, n int) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, status)
		 SELECT $1, $2, $3 || '-' || g, 'validated'
		   FROM generate_series(1, $4) AS g`,
		tenantID, entityID, prefix, n,
	); err != nil {
		t.Fatalf("bulk-seed %d validated invoices: %v", n, err)
	}
}

// approvalRunCountForInvoice counts approval_runs rows for one invoice, through the
// superuser pool -- all three ledger tables are FORCE RLS, so an app-role count with
// no tenant GUC set would read 0 whatever was written.
func approvalRunCountForInvoice(t *testing.T, super *pgxpool.Pool, invoiceID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM approval_runs WHERE invoice_id = $1`, invoiceID).Scan(&n); err != nil {
		t.Fatalf("count approval_runs for invoice %s: %v", invoiceID, err)
	}
	return n
}

// --- AC-1: an approval step must name a live role -----------------------------

// TestPublish_RejectsDanglingRole: the door refuses a NEW version naming a soft-deleted
// role. The sentinel is asserted positively AND the empty-lanes one negatively — a gate
// that answered the wrong 409 would be indistinguishable through the status code alone.
func TestPublish_RejectsDanglingRole(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 publish-dangling-role")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

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
	store := NewStore(app, stubFingerprinter, nil)

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
	store := NewStore(app, stubFingerprinter, nil)

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

	got, err := NewStore(app, stubFingerprinter, nil).PublishPolicy(c, policyID)
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

// --- AC-4: the deactivation is per policy ------------------------------------

// TestPublish_LeavesTheTenantsOtherPolicyActive: publishing policy B leaves policy A's
// active version active and sealed, with its published_at and published_by unchanged.
func TestPublish_LeavesTheTenantsOtherPolicyActive(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-01 publish-leaves-other")
	c, _ := activeAdmin(t, super, tenantID)

	policyA := seedApprovalPolicy(t, super, tenantID, "A policy")
	versionA := seedApprovalPolicyVersionN(t, super, tenantID, policyA, 1)
	publishApprovalPolicyVersion(t, super, versionA, "earlier-publisher")
	beforeA := versionRow(t, super, versionA)
	if beforeA.PublishedAt == nil || !beforeA.IsActive {
		t.Fatalf("fixture: A's version = %+v, want published and active", beforeA)
	}

	policyB := seedApprovalPolicy(t, super, tenantID, "B policy")
	versionB := seedApprovalDraft(t, super, tenantID, policyB)

	if _, err := NewStore(app, stubFingerprinter, nil).PublishPolicy(c, policyB); err != nil {
		t.Fatalf("PublishPolicy(B): %v, want success", err)
	}

	afterB := versionRow(t, super, versionB)
	if !afterB.Sealed || !afterB.IsActive {
		t.Errorf("B's version = %+v, want (sealed true, is_active true)", afterB)
	}
	afterA := versionRow(t, super, versionA)
	if !afterA.Sealed || !afterA.IsActive {
		t.Errorf("A's version = %+v, want (sealed true, is_active true): publishing B must not touch A", afterA)
	}
	if afterA.PublishedAt == nil || *afterA.PublishedAt != *beforeA.PublishedAt {
		t.Errorf("A's published_at = %s, want the unchanged %s", strOrNull(afterA.PublishedAt), strOrNull(beforeA.PublishedAt))
	}
	if afterA.PublishedBy == nil || *afterA.PublishedBy != "earlier-publisher" {
		t.Errorf("A's published_by = %s, want the unchanged %q", strOrNull(afterA.PublishedBy), "earlier-publisher")
	}
	want := []string{versionA, versionB}
	sort.Strings(want)
	if ids := activeVersionIDs(t, super, tenantID); !reflect.DeepEqual(ids, want) {
		t.Errorf("active version ids = %v, want both %v", ids, want)
	}
}

// TestPublish_DeactivatesThePublishedPolicysPreviousVersion: publishing A v2 retires A v1
// (sealed, published_at kept) and leaves B's active version alone.
func TestPublish_DeactivatesThePublishedPolicysPreviousVersion(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-01 publish-deactivates-own-previous")
	c, _ := activeAdmin(t, super, tenantID)

	policyA := seedApprovalPolicy(t, super, tenantID, "A policy")
	a1 := seedApprovalPolicyVersionN(t, super, tenantID, policyA, 1)
	publishApprovalPolicyVersion(t, super, a1, "earlier-publisher")
	a2 := seedApprovalPolicyVersionN(t, super, tenantID, policyA, 2)
	policyB := seedApprovalPolicy(t, super, tenantID, "B policy")
	b1 := seedApprovalPolicyVersionN(t, super, tenantID, policyB, 1)
	publishApprovalPolicyVersion(t, super, b1, "other-publisher")
	beforeA1 := versionRow(t, super, a1)
	if beforeA1.PublishedAt == nil {
		t.Fatal("fixture: A v1 published_at is NULL, so the retention assertion below would be vacuous")
	}

	if _, err := NewStore(app, stubFingerprinter, nil).PublishPolicy(c, policyA); err != nil {
		t.Fatalf("PublishPolicy(A): %v, want success", err)
	}

	afterA1 := versionRow(t, super, a1)
	if !afterA1.Sealed || afterA1.IsActive {
		t.Errorf("A v1 = %+v, want (sealed true, is_active false)", afterA1)
	}
	if afterA1.PublishedAt == nil || *afterA1.PublishedAt != *beforeA1.PublishedAt {
		t.Errorf("A v1 published_at = %s, want the unchanged %s", strOrNull(afterA1.PublishedAt), strOrNull(beforeA1.PublishedAt))
	}
	if v := versionRow(t, super, a2); !v.Sealed || !v.IsActive {
		t.Errorf("A v2 = %+v, want (sealed true, is_active true)", v)
	}
	if v := versionRow(t, super, b1); !v.IsActive {
		t.Errorf("B v1 = %+v, want still active: publishing A must not touch B", v)
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
//	(2) the policy's outgoing active version is deactivated in an EARLIER statement, so
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

	incomingPolicy := seedApprovalPolicy(t, super, tenantID, "Incoming")
	outgoingVersion := seedApprovalPolicyVersionN(t, super, tenantID, incomingPolicy, 1)
	publishApprovalPolicyVersion(t, super, outgoingVersion, "earlier-publisher")
	incomingVersion := seedApprovalPolicyVersionN(t, super, tenantID, incomingPolicy, 2)

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

		// (ii) seal + activate while an earlier version of the same policy holds its slot.
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

	if _, err := NewStore(app, stubFingerprinter, nil).PublishPolicy(c, incomingPolicy); err != nil {
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

	if _, err := NewStore(app, stubFingerprinter, nil).PublishPolicy(c, policyID); err != nil {
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
	store := NewStore(app, stubFingerprinter, nil)

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
// The store now refuses this delete (TestDeleteRole_RefusesARoleAnActiveStepNames), so
// the superuser seed stands in for legacy data.
func TestPublish_RoleDeletedAfterPublishLeavesVersionActive(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 publish-role-deleted-after")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID, stepID := seedApprovalDraftNamingRole(t, super, tenantID, policyID, "tax-reviewer")

	if _, err := store.PublishPolicy(c, policyID); err != nil {
		t.Fatalf("PublishPolicy naming a live role: %v, want success", err)
	}
	before := stepSnapshot(t, super, stepID)

	softDeleteWorkflowRole(t, super, roleID)

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

	if _, err := NewStore(app, stubFingerprinter, nil).PublishPolicy(c, policyID); err != nil {
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
	store := NewStore(app, stubFingerprinter, nil)

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

// --- AC-1,2: the publish sweep arms the validated backlog ----------------------

// TestPublish_SweepArmsBacklog replaces TestPublish_CreatesNoApprovalRun (task-484):
// arming a tenant's validated backlog on publish is this subtask's whole point, not
// deferred work the way APPR-05 shipped it. A second publish then arms zero (AC-2)
// -- every invoice the first sweep touched now carries a run, so the anti-join finds
// nothing left to arm.
func TestPublish_SweepArmsBacklog(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 publish-sweep-arms-backlog")
	c, _ := activeAdmin(t, super, tenantID)

	entityID := seedBusinessEntity(t, super, tenantID, "Sweep Corp")
	var validatedIDs []string
	for _, number := range []string{"sweep-validated-1", "sweep-validated-2", "sweep-validated-3"} {
		id := seedInvoice(t, super, tenantID, entityID, number)
		if _, err := super.Exec(context.Background(),
			`UPDATE invoices SET status = 'validated' WHERE id = $1`, id); err != nil {
			t.Fatalf("validate invoice %s: %v", number, err)
		}
		validatedIDs = append(validatedIDs, id)
	}
	queuedID := seedInvoice(t, super, tenantID, entityID, "sweep-queued")
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET status = 'queued' WHERE id = $1`, queuedID); err != nil {
		t.Fatalf("queue invoice: %v", err)
	}
	draftID := seedInvoice(t, super, tenantID, entityID, "sweep-draft") // stays 'draft' by default

	member := uuid.NewString()
	seedMembership(t, super, tenantID, member, "preparer", "active")
	roleID := seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")
	staffWorkflowRole(t, super, tenantID, roleID, member, 0)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	seedApprovalDraftNamingRole(t, super, tenantID, policyID, "engagement-partner")

	if _, err := NewStore(app, stubFingerprinter, nil).PublishPolicy(c, policyID); err != nil {
		t.Fatalf("PublishPolicy: %v, want success", err)
	}

	if n := rowCount(t, super, "approval_runs", tenantID); n != 3 {
		t.Errorf("approval_runs rows = %d, want 3 — one per validated invoice", n)
	}
	for _, id := range validatedIDs {
		if n := approvalRunCountForInvoice(t, super, id); n != 1 {
			t.Errorf("approval_runs rows for validated invoice %s = %d, want 1", id, n)
		}
	}
	for label, id := range map[string]string{"queued": queuedID, "draft": draftID} {
		if n := approvalRunCountForInvoice(t, super, id); n != 0 {
			t.Errorf("approval_runs rows for the %s invoice = %d, want 0 — only validated is swept", label, n)
		}
	}
	if n := auditCount(t, super, tenantID, "invoice.approval_armed"); n != 3 {
		t.Errorf("invoice.approval_armed audit rows = %d, want 3", n)
	}

	// AC-2: a second publish arms zero — every candidate the first sweep found now
	// carries an open run, so the anti-join excludes all three.
	secondPolicyID := seedApprovalPolicy(t, super, tenantID, "Second sign-off")
	seedApprovalDraftNamingRole(t, super, tenantID, secondPolicyID, "engagement-partner")
	if _, err := NewStore(app, stubFingerprinter, nil).PublishPolicy(c, secondPolicyID); err != nil {
		t.Fatalf("second PublishPolicy: %v, want success", err)
	}
	if n := rowCount(t, super, "approval_runs", tenantID); n != 3 {
		t.Errorf("approval_runs rows after the second publish = %d, want still 3 — a second publish arms zero", n)
	}
	if n := auditCount(t, super, tenantID, "invoice.approval_armed"); n != 3 {
		t.Errorf("invoice.approval_armed audit rows after the second publish = %d, want still 3", n)
	}
}

// --- the sweep arms each invoice with its own first match -----------------------

// sweepValidated seeds one validated invoice with no run.
func sweepValidated(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number string) string {
	t.Helper()
	id := seedInvoice(t, super, tenantID, entityID, number)
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET status = 'validated' WHERE id = $1`, id); err != nil {
		t.Fatalf("validate invoice %s: %v", number, err)
	}
	return id
}

// staffedRoleKey creates a live workflow role with one active holder so a draft naming it publishes.
func staffedRoleKey(t *testing.T, super *pgxpool.Pool, tenantID, key string) {
	t.Helper()
	member := uuid.NewString()
	seedMembership(t, super, tenantID, member, "preparer", "active")
	staffWorkflowRole(t, super, tenantID, seedWorkflowRole(t, super, tenantID, key, key), member, 0)
}

// TestPublish_SweepArmsEachInvoiceWithItsOwnFirstMatch: A (position 1, client E1) is active,
// the catch-all B (position 2) is published; the E1 invoice stays on A, the E2 invoice takes B.
func TestPublish_SweepArmsEachInvoiceWithItsOwnFirstMatch(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03 sweep own first match")
	c, _ := activeAdmin(t, super, tenantID)
	staffedRoleKey(t, super, tenantID, "engagement-partner")

	e1 := seedBusinessEntity(t, super, tenantID, "Sweep E1")
	e2 := seedBusinessEntity(t, super, tenantID, "Sweep E2")
	i1 := sweepValidated(t, super, tenantID, e1, "sweep-own-1")
	i2 := sweepValidated(t, super, tenantID, e2, "sweep-own-2")

	_, versionA := seedScopedPolicy(t, super, tenantID, "Client E1", 1, scopeSpec{EntityIDs: []string{e1}}, "engagement-partner")
	policyB := seedApprovalPolicy(t, super, tenantID, "Catch-all")
	versionB, _ := seedApprovalDraftNamingRole(t, super, tenantID, policyB, "engagement-partner")
	if versionA == versionB {
		t.Fatal("fixture: A and B share a version")
	}

	if _, err := NewStore(app, stubFingerprinter, nil).PublishPolicy(c, policyB); err != nil {
		t.Fatalf("PublishPolicy(B): %v, want success", err)
	}

	if got := oneApprovalRun(t, super, i1).PolicyVersionID; got != versionA {
		t.Errorf("E1 invoice armed on version %s, want policy A's %s: its own first match, not the published one", got, versionA)
	}
	if got := oneApprovalRun(t, super, i2).PolicyVersionID; got != versionB {
		t.Errorf("E2 invoice armed on version %s, want the published catch-all's %s", got, versionB)
	}
	if n := auditCount(t, super, tenantID, "invoice.approval_armed"); n != 2 {
		t.Errorf("invoice.approval_armed audit rows = %d, want 2", n)
	}
}

// TestPublish_SweepLeavesAnUnmatchedInvoiceWithoutARun: the only policy in force is scoped to E1,
// so an E2 invoice matches nothing; the sweep succeeds and the E1 control gets its run. The state
// needs a scoped policy in force with no catch-all, which the publish guard refuses, so the
// fixture writes it directly and the test calls the sweep PublishPolicy runs.
func TestPublish_SweepLeavesAnUnmatchedInvoiceWithoutARun(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03 sweep unmatched")
	e1 := seedBusinessEntity(t, super, tenantID, "Unmatched E1")
	e2 := seedBusinessEntity(t, super, tenantID, "Unmatched E2")
	matched := sweepValidated(t, super, tenantID, e1, "sweep-unmatched-e1")
	unmatched := sweepValidated(t, super, tenantID, e2, "sweep-unmatched-e2")
	_, versionA := seedScopedPolicy(t, super, tenantID, "Client E1", 1, scopeSpec{EntityIDs: []string{e1}}, "engagement-partner")

	store := NewStore(app, stubFingerprinter, nil)
	if err := db.WithinTenantTx(context.Background(), app, tenantID, func(tx pgx.Tx) error {
		return store.sweepValidatedBacklog(context.Background(), tx, tenantID, "test-actor")
	}); err != nil {
		t.Fatalf("sweepValidatedBacklog: %v, want success although an invoice matches no policy", err)
	}

	if got := oneApprovalRun(t, super, matched).PolicyVersionID; got != versionA {
		t.Errorf("E1 invoice armed on version %s, want %s", got, versionA)
	}
	if n := approvalRunCountForInvoice(t, super, unmatched); n != 0 {
		t.Errorf("approval_runs rows for the unmatched invoice = %d, want 0", n)
	}
	if n := auditCount(t, super, tenantID, "invoice.approval_armed"); n != 1 {
		t.Errorf("invoice.approval_armed audit rows = %d, want 1 (the matched invoice only)", n)
	}
}

// TestPublish_LeavesOpenRunsOnTheirVersion: an open run keeps its version when a policy is
// published ahead of its own, and when its own policy publishes a new version; no publish
// writes a run or an armed row for it.
func TestPublish_LeavesOpenRunsOnTheirVersion(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-04 publish leaves open runs")
	c, _ := activeAdmin(t, super, tenantID)
	staffedRoleKey(t, super, tenantID, "engagement-partner")
	entityID := seedBusinessEntity(t, super, tenantID, "Open Run Corp")
	invoiceID := sweepValidated(t, super, tenantID, entityID, "open-run-1")

	policyA, versionA := seedScopedPolicy(t, super, tenantID, "A catch-all", 1, scopeSpec{}, "engagement-partner")
	if got := armedVersion(t, super, app, tenantID, invoiceID); got != versionA {
		t.Fatalf("fixture: armed version = %s, want A's %s", got, versionA)
	}
	policyB := seedApprovalPolicy(t, super, tenantID, "B catch-all")
	seedApprovalDraftNamingRole(t, super, tenantID, policyB, "engagement-partner")
	if _, err := super.Exec(context.Background(),
		`UPDATE approval_policies SET position = CASE id WHEN $1 THEN 1 ELSE 2 END WHERE id IN ($1, $2)`,
		policyB, policyA); err != nil {
		t.Fatalf("move B ahead of A: %v", err)
	}

	assertUntouched := func(label string) {
		t.Helper()
		run := oneApprovalRun(t, super, invoiceID)
		if run.PolicyVersionID != versionA || run.State != "open" {
			t.Errorf("%s: run = {version %s, state %s}, want {A v1 %s, open}", label, run.PolicyVersionID, run.State, versionA)
		}
		if n := rowCount(t, super, "approval_runs", tenantID); n != 1 {
			t.Errorf("%s: approval_runs rows = %d, want 1", label, n)
		}
		if n := auditCount(t, super, tenantID, "invoice.approval_armed"); n != 1 {
			t.Errorf("%s: invoice.approval_armed rows = %d, want 1", label, n)
		}
	}

	store := NewStore(app, stubFingerprinter, nil)
	if _, err := store.PublishPolicy(c, policyB); err != nil {
		t.Fatalf("PublishPolicy(B): %v, want success", err)
	}
	assertUntouched("after B published ahead")

	a2 := seedApprovalPolicyVersionN(t, super, tenantID, policyA, 2)
	seedApprovalPolicyStepInLane(t, super, tenantID, a2, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("engagement-partner"),
	})
	if _, err := store.PublishPolicy(c, policyA); err != nil {
		t.Fatalf("PublishPolicy(A v2): %v, want success", err)
	}
	if v := versionRow(t, super, versionA); v.IsActive {
		t.Errorf("A v1 = %+v, want deactivated by A v2", v)
	}
	assertUntouched("after A published v2")
}

// --- AC-3,4: the cap refuses the whole publish before arming anything ----------

// TestPublish_SweepAboveCapReturns409: sweepCap+1 (5,001) validated invoices is one
// over the literal cap (AC-4) -> PublishPolicy refuses with ErrSweepCapExceeded
// BEFORE arming anything: approval_runs stays empty and the version stays unsealed
// and inactive, so a retry after trimming the backlog starts from the same
// unpublished draft. Runs in ~0.1s — LIMIT sweepCap+1 lets the refusal fire before
// any ArmTx call.
func TestPublish_SweepAboveCapReturns409(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 publish-sweep-above-cap")
	c, _ := activeAdmin(t, super, tenantID)

	entityID := seedBusinessEntity(t, super, tenantID, "Above-cap Corp")
	bulkSeedValidatedInvoices(t, super, tenantID, entityID, "above-cap", sweepCap+1)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalDraft(t, super, tenantID, policyID)

	_, err := NewStore(app, stubFingerprinter, nil).PublishPolicy(c, policyID)
	if !errors.Is(err, ErrSweepCapExceeded) {
		t.Fatalf("PublishPolicy over the sweep cap: err = %v, want ErrSweepCapExceeded", err)
	}

	if n := rowCount(t, super, "approval_runs", tenantID); n != 0 {
		t.Errorf("approval_runs rows = %d, want 0 — the cap refusal rolls back the whole transaction", n)
	}
	if v := versionRow(t, super, versionID); v.Sealed || v.IsActive {
		t.Errorf("version row = %+v, want still (sealed false, is_active false)", v)
	}
}

// --- publish refuses a scope that names a deleted role ----------------

func scopeDraftNaming(t *testing.T, super *pgxpool.Pool, tenantID, name string, position int, roleIDs ...string) (policyID, versionID string) {
	t.Helper()
	policyID, versionID = seedScopedPolicyDraft(t, super, tenantID, name, position, scopeSpec{RoleIDs: roleIDs})
	seedActivePolicy(t, super, tenantID, name+" catch-all", position+1, nil)
	return policyID, versionID
}

func TestPublish_RefusalNamesEveryDeletedScopeRoleInOrder(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-09 order")
	c, _ := activeAdmin(t, super, tenantID)
	live := seedWorkflowRole(t, super, tenantID, "marlin", "Marlin Signer")
	zeta := seedWorkflowRole(t, super, tenantID, "zeta", "Zeta Auditor")
	quill := seedWorkflowRole(t, super, tenantID, "quill", "Quill Reviewer")
	softDeleteWorkflowRole(t, super, zeta)
	softDeleteWorkflowRole(t, super, quill)
	policyID, _ := scopeDraftNaming(t, super, tenantID, "S", 1, live, zeta, quill)

	_, err := NewStore(app, stubFingerprinter, nil).PublishPolicy(c, policyID)
	if !errors.Is(err, ErrPolicyStepRole) {
		t.Fatalf("PublishPolicy err = %v, want ErrPolicyStepRole", err)
	}
	_, msg := policyStatusForErr(err)
	z, q := strings.Index(msg, "\u201cZeta Auditor\u201d"), strings.Index(msg, "\u201cQuill Reviewer\u201d")
	if z < 0 || q < 0 || z > q {
		t.Errorf("msg = %q, want \u201cZeta Auditor\u201d before \u201cQuill Reviewer\u201d", msg)
	}
	if strings.Contains(msg, "Marlin Signer") {
		t.Errorf("msg = %q, names the live role", msg)
	}
}

func TestPublish_RefusalEchoesATitleVerbatimAndNamesARepeatedTitleOnce(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-09 verbatim")
	c, _ := activeAdmin(t, super, tenantID)
	const odd = `Say "hi" \ ok`
	ids := []string{
		seedWorkflowRole(t, super, tenantID, "odd", odd),
		seedWorkflowRole(t, super, tenantID, "twin-a", "Twin"),
		seedWorkflowRole(t, super, tenantID, "twin-b", "Twin"),
	}
	for _, id := range ids {
		softDeleteWorkflowRole(t, super, id)
	}
	policyID, _ := scopeDraftNaming(t, super, tenantID, "S", 1, ids...)

	_, err := NewStore(app, stubFingerprinter, nil).PublishPolicy(c, policyID)
	_, msg := policyStatusForErr(err)
	if !strings.Contains(msg, "\u201c"+odd+"\u201d") {
		t.Errorf("msg = %q, want the title %q verbatim in curly quotes", msg, odd)
	}
	if n := strings.Count(msg, "\u201cTwin\u201d"); n != 1 {
		t.Errorf("msg = %q, \u201cTwin\u201d occurs %d times, want 1", msg, n)
	}
}

func TestPublish_RefusesAForkThatKeepsADeletedScopeRole(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-09 fork")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)
	seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")
	roleID := seedWorkflowRole(t, super, tenantID, "sealed-role", "Sealed role")
	policyID := seedApprovalPolicy(t, super, tenantID, "P")
	v1 := seedSealedActiveV1Scoped(t, super, tenantID, policyID, seedBusinessEntity(t, super, tenantID, "Client A"), roleID)
	seedSealedActiveV1(t, super, tenantID, seedApprovalPolicy(t, super, tenantID, "Catch-all"))
	softDeleteWorkflowRole(t, super, roleID)

	if _, err := store.PutDraft(c, policyID, nil, &scopeInput{WorkflowRoleKeys: []string{"sealed-role"}},
		approvalStep("engagement-partner")); err != nil {
		t.Fatalf("PutDraft: %v", err)
	}
	var scopeRefusal *scopeRoleDeleted
	if _, err := store.PublishPolicy(c, policyID); !errors.As(err, &scopeRefusal) {
		t.Fatalf("PublishPolicy err = %v, want scopeRoleDeleted", err)
	}
	if v := versionRow(t, super, v1); !v.Sealed || !v.IsActive {
		t.Errorf("v1 = %+v, want still (sealed true, is_active true)", v)
	}
	if d := openDraft(t, super, policyID); d.Sealed || d.IsActive {
		t.Errorf("v2 = %+v, want unsealed and inactive", d)
	}
}

func TestPublish_PublishesOnceTheDeletedScopeRoleIsRemoved(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-09 fixed")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)
	seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")
	roleID := seedWorkflowRole(t, super, tenantID, "gone", "Gone")
	softDeleteWorkflowRole(t, super, roleID)
	policyID, versionID := scopeDraftNaming(t, super, tenantID, "S", 1, roleID)

	if _, err := store.PublishPolicy(c, policyID); !errors.Is(err, ErrPolicyStepRole) {
		t.Fatalf("first PublishPolicy err = %v, want ErrPolicyStepRole", err)
	}
	if _, err := store.PutDraft(c, policyID, nil, &scopeInput{DocumentTypes: []string{"credit_note"}, WorkflowRoleKeys: []string{}},
		approvalStep("engagement-partner")); err != nil {
		t.Fatalf("PutDraft dropping the role: %v", err)
	}
	if _, err := store.PublishPolicy(c, policyID); err != nil {
		t.Fatalf("PublishPolicy after the fix: %v, want success", err)
	}
	if v := versionRow(t, super, versionID); !v.Sealed || !v.IsActive {
		t.Errorf("version = %+v, want (sealed true, is_active true)", v)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.published"); n != 1 {
		t.Errorf("approval_policy.published audit rows = %d, want 1", n)
	}
}

func TestPublish_ALiveScopeRolePublishes(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-09 live")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "live", "Live")
	policyID, _ := scopeDraftNaming(t, super, tenantID, "S", 1, roleID)

	if _, err := NewStore(app, stubFingerprinter, nil).PublishPolicy(c, policyID); err != nil {
		t.Fatalf("PublishPolicy naming a live scope role: %v, want success", err)
	}
}

func TestPublish_DeadStepRoleKeepsTheStepSentence(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-09 step first")
	c, _ := activeAdmin(t, super, tenantID)
	scopeRole := seedWorkflowRole(t, super, tenantID, "gone-scope", "Gone scope")
	stepRole := seedWorkflowRole(t, super, tenantID, "gone-step", "Gone step")
	softDeleteWorkflowRole(t, super, scopeRole)
	softDeleteWorkflowRole(t, super, stepRole)
	policyID, versionID := scopeDraftNaming(t, super, tenantID, "S", 1, scopeRole)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("gone-step"),
	})

	_, err := NewStore(app, stubFingerprinter, nil).PublishPolicy(c, policyID)
	if _, msg := policyStatusForErr(err); msg != "an approval step names a workflow role that no longer exists" {
		t.Errorf("msg = %q, want the step sentence", msg)
	}
}
