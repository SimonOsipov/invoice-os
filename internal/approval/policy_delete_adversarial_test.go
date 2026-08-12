package approval

// Adversarial coverage for Store.DeletePolicy, beyond the acceptance criteria: the admin
// gate, the statement order the serialisation depends on, the tables the delete must never
// reach, a delete racing another delete, the state the tenant is left in and what every
// other reader then answers, and the id spellings the parse has to accept.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// --- the admin gate -----------------------------------------------------------

// TestDeletePolicy_NeedsAnActiveAdmin: delete is a write, so it carries CreatePolicy's
// gate. Removing requireActiveAdmin from the delete path leaves every acceptance-criteria
// spec green, because all of them call it as an active admin — measured.
func TestDeletePolicy_NeedsAnActiveAdmin(t *testing.T) {
	callers := []struct{ name, role, status string }{
		{"a preparer", "preparer", "active"},
		{"a suspended admin", "admin", "suspended"},
	}
	for _, tc := range callers {
		t.Run(tc.name, func(t *testing.T) {
			super, _ := dbTestPools(t)
			tenantID := policyTenant(t, super, "APPR-05 delete-permission "+tc.name)
			c, _ := callerCtx(t, super, tenantID, tc.role, tc.status)

			policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
			versionID := seedPublishedVersion(t, super, tenantID, policyID, 1, 0)

			traced, rec := tracedAppPool(t)
			rec.reset()
			_, err := NewStore(traced, stubFingerprinter).DeletePolicy(c, policyID)
			if !errors.Is(err, ErrNotPermitted) {
				t.Errorf("DeletePolicy as %s: err = %v, want ErrNotPermitted", tc.name, err)
			}
			if sql := rec.mentioning("memberships"); len(sql) == 0 {
				t.Error("no memberships statement was issued — requireActiveAdmin did not run")
			}
			if sql := rec.mentioning("approval_polic"); len(sql) != 0 {
				t.Errorf("the refused caller still issued %d approval_polic* statements: %v — the "+
					"gate must precede the stamp", len(sql), sql)
			}
			if at := policyDeletedAt(t, super, policyID); at != nil {
				t.Errorf("deleted_at = %q after a refused delete, want NULL", *at)
			}
			if ids := activeVersionIDs(t, super, tenantID); len(ids) != 1 || ids[0] != versionID {
				t.Errorf("active version ids = %v, want still [%s] — a refused delete deactivates nothing",
					ids, versionID)
			}
			if n := auditCount(t, super, tenantID, "approval_policy.deleted"); n != 0 {
				t.Errorf("approval_policy.deleted audit rows = %d, want 0", n)
			}
		})
	}

	// Control: the same store deletes for an active admin, so the refusals above are not a
	// store that refuses everyone.
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 delete-permission control")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	seedPublishedVersion(t, super, tenantID, policyID, 1, 0)
	if _, err := NewStore(app, stubFingerprinter).DeletePolicy(c, policyID); err != nil {
		t.Fatalf("control: DeletePolicy as an active admin: %v, want success", err)
	}
}

// --- the statement order --------------------------------------------------------

// TestDeletePolicy_StampsThePolicyRowBeforeTheVersionWrite pins the ordering rule itself.
// The race spec cannot: it only proves the delete contends for the policy row at SOME point
// before committing, so an unlocked SELECT followed by a later stamp passes it — measured.
// What that shape actually costs is the lock order: taking the version row first and the
// policy row second inverts PublishPolicy's order and makes the pair deadlockable.
func TestDeletePolicy_StampsThePolicyRowBeforeTheVersionWrite(t *testing.T) {
	super, _ := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 delete-statement-order")
	c, _ := activeAdmin(t, super, tenantID)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	seedPublishedVersion(t, super, tenantID, policyID, 1, 1)

	traced, rec := tracedAppPool(t)
	rec.reset()
	if _, err := NewStore(traced, stubFingerprinter).DeletePolicy(c, policyID); err != nil {
		t.Fatalf("DeletePolicy: %v", err)
	}

	sql := rec.mentioning("approval_polic")
	if len(sql) != 3 {
		t.Fatalf("DeletePolicy issued %d approval_polic* statements, want 3 (the stamp, the "+
			"deactivation, the max read):\n%v", len(sql), sql)
	}

	first := strings.ToLower(strings.TrimSpace(sql[0]))
	if !strings.HasPrefix(first, "update approval_policies") {
		t.Errorf("the first policy statement is:\n%s\nwant the UPDATE that stamps deleted_at — a bare "+
			"SELECT takes no row lock, and a FOR UPDATE taken after the version write inverts "+
			"PublishPolicy's lock order", sql[0])
	}
	if !strings.Contains(first, "deleted_at is null") {
		t.Errorf("the stamping statement is:\n%s\nwant the deleted_at IS NULL predicate that makes it "+
			"the existence check and the idempotency mechanism", sql[0])
	}
	for i, s := range sql[1:] {
		if !strings.Contains(s, "approval_policy_versions") {
			t.Errorf("statement %d is:\n%s\nwant a version statement, below the stamp", i+2, s)
		}
	}
	for _, s := range sql {
		if strings.Contains(strings.ToLower(s), "tenant_id") {
			t.Errorf("a delete statement carries a tenant_id predicate; RLS is the only tenant "+
				"filter:\n%s", s)
		}
	}
}

// TestDeletePolicy_IssuesNoStatementAgainstStepsOrTheRunLedger: this deletes a POLICY, not
// an approval DECISION. The step table is the one below it invoice_app CAN delete from
// (GRANT DELETE on approval_policy_steps and nowhere else in this epic), so "the rows
// survive" is weaker than "no statement was ever issued" — a future step write would pass
// the row counts by putting the rows back.
func TestDeletePolicy_IssuesNoStatementAgainstStepsOrTheRunLedger(t *testing.T) {
	super, _ := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 delete-touches-nothing-else")
	c, _ := activeAdmin(t, super, tenantID)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	seedPublishedVersion(t, super, tenantID, policyID, 1, 2)

	traced, rec := tracedAppPool(t)
	rec.reset()
	if _, err := NewStore(traced, stubFingerprinter).DeletePolicy(c, policyID); err != nil {
		t.Fatalf("DeletePolicy: %v", err)
	}

	// Every statement, not a filtered subset: mentioning("") matches all of them.
	for _, forbidden := range []string{"approval_policy_steps", "approval_run", "approval_decision"} {
		for _, s := range rec.mentioning("") {
			if strings.Contains(s, forbidden) {
				t.Errorf("DeletePolicy issued a statement against %s:\n%s", forbidden, s)
			}
		}
	}
	if n := rowCount(t, super, "approval_policy_steps", tenantID); n != 2 {
		t.Errorf("tenant step rows = %d, want the 2 seeded", n)
	}
}

// --- a delete racing a delete ----------------------------------------------------

// TestDeletePolicy_ConcurrentDeletesLeaveOneStamp: two admins of one tenant delete the same
// policy. The predicate is re-evaluated under READ COMMITTED, so the loser matches nothing
// and answers ErrPolicyNotFound rather than moving the stamp — the AC-3 mechanism, reached
// through real contention instead of two sequential calls.
//
// Deterministic because the lock queue is FIFO: the first delete is observed waiting on the
// blocker before the second is started.
func TestDeletePolicy_ConcurrentDeletesLeaveOneStamp(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := policyTenant(t, super, "APPR-05 delete-races-delete")
	cFirst, _ := activeAdmin(t, super, tenantID)
	cSecond, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedPublishedVersion(t, super, tenantID, policyID, 1, 0)

	blocker, holderPID := blockerTx(t, ctx, app, tenantID)
	if _, err := blocker.Exec(ctx,
		`SELECT id FROM approval_policies WHERE id = $1 FOR UPDATE`, policyID); err != nil {
		t.Fatalf("blocker holds the policy row: %v", err)
	}

	firstDone := deleteInBackground(store, cFirst, policyID)
	waitUntilNBlockedBy(t, super, holderPID, 1, firstDone, "the first DeletePolicy")
	secondDone := deleteInBackground(store, cSecond, policyID)
	waitUntilNBlockedBy(t, super, holderPID, 2, secondDone, "the second DeletePolicy")

	if err := blocker.Rollback(ctx); err != nil {
		t.Fatalf("release the blocker: %v", err)
	}
	if err := awaitCall(t, firstDone, "the first DeletePolicy"); err != nil {
		t.Fatalf("the first DeletePolicy: %v, want success", err)
	}
	if err := awaitCall(t, secondDone, "the second DeletePolicy"); !errors.Is(err, ErrPolicyNotFound) {
		t.Errorf("the second DeletePolicy: err = %v, want ErrPolicyNotFound", err)
	}

	if policyDeletedAt(t, super, policyID) == nil {
		t.Error("deleted_at is NULL after two deletes — the winner stamped nothing")
	}
	if n := auditCount(t, super, tenantID, "approval_policy.deleted"); n != 1 {
		t.Errorf("approval_policy.deleted audit rows = %d, want 1 — the loser audits nothing", n)
	}
	v := versionRow(t, super, versionID)
	if !v.Sealed || v.IsActive {
		t.Errorf("version row = %+v, want (sealed true, is_active false)", v)
	}
	if v.PublishedAt == nil || v.PublishedBy == nil {
		t.Errorf("published_at/by = (%s, %s), want both preserved",
			strOrNull(v.PublishedAt), strOrNull(v.PublishedBy))
	}
}

// --- what the tenant is left with -------------------------------------------------

// TestDeletePolicy_LeavesNoActivePolicyAndEveryReaderRefuses walks the state D4 describes:
// the tenant's only active policy goes, so no policy governs any invoice, and the deleted
// row is unreachable through every other store method. Reached through the store's own
// delete rather than the superuser helper the publish and draft specs use, which is the
// part none of them covers.
//
// v2 is an OPEN draft carrying steps, so PutDraft and PublishPolicy would both otherwise
// succeed — without it their refusals could be "nothing to publish" rather than "no such
// policy".
func TestDeletePolicy_LeavesNoActivePolicyAndEveryReaderRefuses(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 delete-leaves-nothing-active")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	sealedV1 := seedPublishedVersion(t, super, tenantID, policyID, 1, 1)
	openV2 := seedVersionWithSteps(t, super, tenantID, policyID, 2, 1)
	beforeV2 := versionSnapshot(t, super, openV2)
	beforeV2Steps := readStoredSteps(t, super, openV2)

	if ids := activeVersionIDs(t, super, tenantID); len(ids) != 1 || ids[0] != sealedV1 {
		t.Fatalf("fixture active version ids = %v, want exactly [%s]", ids, sealedV1)
	}

	if _, err := store.DeletePolicy(c, policyID); err != nil {
		t.Fatalf("DeletePolicy: %v", err)
	}

	if ids := activeVersionIDs(t, super, tenantID); len(ids) != 0 {
		t.Errorf("active version ids = %v, want none — no policy governs the tenant's invoices", ids)
	}
	got, err := store.ListPolicies(c)
	if err != nil {
		t.Fatalf("ListPolicies after the delete: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListPolicies = %v, want empty", policyIDs(got))
	}

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"GetPolicy", func() error { _, err := store.GetPolicy(c, policyID); return err }},
		{"PutDraft", func() error {
			_, err := store.PutDraft(c, policyID, ptr("Renamed"), nil, approvalStep("engagement-partner"))
			return err
		}},
		{"PublishPolicy", func() error { _, err := store.PublishPolicy(c, policyID); return err }},
		{"DeletePolicy", func() error { _, err := store.DeletePolicy(c, policyID); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if !errors.Is(err, ErrPolicyNotFound) {
				t.Errorf("%s on the deleted policy: err = %v, want ErrPolicyNotFound", tc.name, err)
			}
			if code := pgCode(err); code != "" {
				t.Errorf("%s surfaced a raw Postgres error (SQLSTATE %s) — it answers 500, not 404",
					tc.name, code)
			}
		})
	}

	// The refusals above wrote nothing: the sealed version keeps its publication, and the
	// open draft is byte-identical.
	v1 := versionRow(t, super, sealedV1)
	if !v1.Sealed || v1.IsActive || v1.PublishedAt == nil || v1.PublishedBy == nil {
		t.Errorf("v1 = %+v, want sealed, inactive, and still published", v1)
	}
	if after := versionSnapshot(t, super, openV2); after != beforeV2 {
		t.Errorf("v2 row = %s, want the unchanged %s", after, beforeV2)
	}
	if after := readStoredSteps(t, super, openV2); !reflect.DeepEqual(after, beforeV2Steps) {
		t.Errorf("v2 steps:\n  before %v\n  after  %v", beforeV2Steps, after)
	}
	if n := rowCount(t, super, "approval_policy_steps", tenantID); n != 2 {
		t.Errorf("tenant step rows = %d, want the 2 seeded", n)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.deleted"); n != 1 {
		t.Errorf("approval_policy.deleted audit rows = %d, want 1", n)
	}
}

// TestDeletePolicy_FreesTheNameForANewPolicy: the delete is soft, so the old row is still
// there under the name the new one takes. Nothing stops that — approval_policies carries no
// unique index on name, only (tenant_id, id) — and the deleted row stays out of every read.
func TestDeletePolicy_FreesTheNameForANewPolicy(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 delete-frees-the-name")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter)

	first, err := store.CreatePolicy(c, "Sign-off", scopeAllInvoices)
	if err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}
	if _, err := store.DeletePolicy(c, first.ID); err != nil {
		t.Fatalf("DeletePolicy: %v", err)
	}

	second, err := store.CreatePolicy(c, "Sign-off", scopeAllInvoices)
	if err != nil {
		t.Fatalf("CreatePolicy reusing a deleted policy's name: %v, want success", err)
	}
	if second.ID == first.ID {
		t.Fatalf("the second CreatePolicy returned the deleted policy's id %s — it resurrected the "+
			"row rather than inserting one", first.ID)
	}
	if policyDeletedAt(t, super, first.ID) == nil {
		t.Error("the first policy's deleted_at is NULL — creating the name back un-deleted it")
	}
	if policyDeletedAt(t, super, second.ID) != nil {
		t.Error("the second policy is already deleted")
	}

	got, err := store.ListPolicies(c)
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	if len(got) != 1 || got[0].ID != second.ID {
		t.Errorf("ListPolicies = %v, want only the new %s", policyIDs(got), second.ID)
	}
}

// --- the id spellings the parse accepts --------------------------------------------

// TestDeletePolicy_AcceptsANonCanonicalUuidSpelling: uuid.Parse accepts spellings ::uuid
// rejects, and it is what canonicalises them before the bind. The urn form is the one that
// proves it — measured on the dev DB, "urn:uuid:..."::uuid raises 22P02, so a store binding
// the caller's raw string answers 500 where this seam promises 404 (or, worse, 404 on a
// policy that exists).
func TestDeletePolicy_AcceptsANonCanonicalUuidSpelling(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 delete-uuid-spelling")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter)

	for _, tc := range []struct {
		name  string
		spell func(string) string
	}{
		{"urn", func(id string) string { return "urn:uuid:" + id }},
		{"braced", func(id string) string { return "{" + id + "}" }},
		{"upper case", strings.ToUpper},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off "+tc.name)
			got, err := store.DeletePolicy(c, tc.spell(policyID))
			if err != nil {
				t.Fatalf("DeletePolicy(%q): %v, want success", tc.spell(policyID), err)
			}
			if got.ID != policyID {
				t.Errorf("returned id = %q, want the canonical %q", got.ID, policyID)
			}
			if policyDeletedAt(t, super, policyID) == nil {
				t.Error("deleted_at is NULL — the spelling resolved to no row")
			}
		})
	}

	// Control: a well-formed uuid naming nothing is still a refusal, so the successes above
	// are not a store that deletes whatever it is handed.
	if _, err := store.DeletePolicy(c, "urn:uuid:"+uuid.NewString()); !errors.Is(err, ErrPolicyNotFound) {
		t.Errorf("DeletePolicy of an unknown urn-spelled id: err = %v, want ErrPolicyNotFound", err)
	}
}
