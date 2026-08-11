package approval

// Store.DeletePolicy under a real Postgres: the soft-delete stamp, the deactivation of
// the policy's active version, the audit, the inert draft shape the response carries, and
// the outcome a publish racing the delete must reach.
//
// Written before the method body exists, so every spec here starts RED against
// policy_store.go's stub. Run locally with `DEV_DB_PORT=5433 make test-approvals`; in CI
// the rls job's gate step fails the build on any skip.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- fixtures ---------------------------------------------------------------

// policyDeletedAt reads the STAMP as text, nil when NULL. The column, never the response:
// a store that answered a deleted-looking Policy while writing nothing would pass every
// response-only assertion. pgx.ErrNoRows here means the row was hard-deleted.
func policyDeletedAt(t *testing.T, super *pgxpool.Pool, policyID string) *string {
	t.Helper()
	var at *string
	if err := super.QueryRow(context.Background(),
		`SELECT deleted_at::text FROM approval_policies WHERE id = $1`, policyID).Scan(&at); err != nil {
		t.Fatalf("read deleted_at of %s (no row means the policy was HARD-deleted, not soft-deleted): %v",
			policyID, err)
	}
	return at
}

// seedVersionWithSteps inserts one unsealed version at an explicit number carrying
// stepCount notify steps. Steps go in before any seal: approval_policy_steps_content_lock
// refuses an INSERT under a sealed row.
func seedVersionWithSteps(t *testing.T, super *pgxpool.Pool, tenantID, policyID string, version, stepCount int) string {
	t.Helper()
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, version)
	for i := 0; i < stepCount; i++ {
		seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
			Ord: i, Kind: "notify",
			NotifyTarget:  ptr(fmt.Sprintf("v%d-step-%d@example.com", version, i)),
			NotifyChannel: ptr("email"),
		})
	}
	return versionID
}

// seedPublishedVersion seals the version AND claims the tenant's one_active slot, the
// shape a published policy holds. published_at/by are stamped so the preserved-afterwards
// assertions are not vacuously NULL == NULL.
func seedPublishedVersion(t *testing.T, super *pgxpool.Pool, tenantID, policyID string, version, stepCount int) string {
	t.Helper()
	versionID := seedVersionWithSteps(t, super, tenantID, policyID, version, stepCount)
	publishApprovalPolicyVersion(t, super, versionID, "fixture-publisher")
	return versionID
}

// deletedAuditPayload reads the approval_policy.deleted payload for one policy. Callers
// assert the row COUNT separately: pgx takes the first row without complaining about a
// second.
func deletedAuditPayload(t *testing.T, super *pgxpool.Pool, tenantID, policyID string) map[string]any {
	t.Helper()
	var raw string
	if err := super.QueryRow(context.Background(),
		`SELECT payload::text FROM audit_log
		  WHERE tenant_id = $1 AND event = 'approval_policy.deleted'
		    AND (payload->>'policy_id')::uuid = $2::uuid`, tenantID, policyID).Scan(&raw); err != nil {
		t.Fatalf("read the approval_policy.deleted payload of %s (no row means nothing was audited): %v",
			policyID, err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("audit payload %q is not an object: %v", raw, err)
	}
	return body
}

// assertPayloadKeys pins the payload's key SET, so a store that added a field nobody
// designed fails rather than being ignored.
func assertPayloadKeys(t *testing.T, body map[string]any) {
	t.Helper()
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"policy_id", "version"}) {
		t.Errorf("audit payload keys = %v, want [policy_id version]", keys)
	}
}

// --- AC-1: the delete is soft -------------------------------------------------

// TestDeletePolicy_IsSoftAndKeepsVersions: the policy row survives with deleted_at set,
// and neither table below it loses a row. invoice_app holds no DELETE grant on
// approval_policy_versions, so a store that tried would fail loudly; what this pins is
// that the delete does not reach for the steps it CAN delete either.
func TestDeletePolicy_IsSoftAndKeepsVersions(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 delete-is-soft")
	c, _ := activeAdmin(t, super, tenantID)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	sealedV1 := seedPublishedVersion(t, super, tenantID, policyID, 1, 1)
	openV2 := seedVersionWithSteps(t, super, tenantID, policyID, 2, 2)

	if _, err := NewStore(app).DeletePolicy(c, policyID); err != nil {
		t.Fatalf("DeletePolicy: %v", err)
	}

	if policyDeletedAt(t, super, policyID) == nil {
		t.Error("deleted_at is NULL after DeletePolicy — the stamp is the whole delete")
	}
	if vs := versionRows(t, super, policyID); len(vs) != 2 {
		t.Errorf("version rows = %d, want the 2 seeded: %+v — no version row is ever removed", len(vs), vs)
	}
	if n := len(readStoredSteps(t, super, sealedV1)); n != 1 {
		t.Errorf("v1 step rows = %d, want the 1 seeded", n)
	}
	if n := len(readStoredSteps(t, super, openV2)); n != 2 {
		t.Errorf("v2 step rows = %d, want the 2 seeded", n)
	}
	if n := rowCount(t, super, "approval_policy_steps", tenantID); n != 3 {
		t.Errorf("tenant step rows = %d, want the 3 seeded", n)
	}
}

// --- AC-2: the active version is deactivated ----------------------------------

// TestDeletePolicy_DeactivatesTheActiveVersion: without this the soft-deleted policy keeps
// governing every invoice while being invisible to every read. Deactivating a SEALED row
// is legal — seal_guard refuses only sealed -> unsealed — and it is not un-publishing, so
// published_at and published_by survive.
func TestDeletePolicy_DeactivatesTheActiveVersion(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 delete-deactivates")
	c, _ := activeAdmin(t, super, tenantID)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedPublishedVersion(t, super, tenantID, policyID, 1, 0)

	// The fixture really holds the tenant's slot, or the assertion below is vacuous.
	if ids := activeVersionIDs(t, super, tenantID); len(ids) != 1 || ids[0] != versionID {
		t.Fatalf("fixture active version ids = %v, want exactly [%s]", ids, versionID)
	}

	if _, err := NewStore(app).DeletePolicy(c, policyID); err != nil {
		t.Fatalf("DeletePolicy: %v", err)
	}

	v := versionRow(t, super, versionID)
	if !v.Sealed || v.IsActive {
		t.Errorf("version row = %+v, want (sealed true, is_active false)", v)
	}
	if v.PublishedAt == nil || v.PublishedBy == nil {
		t.Errorf("published_at/by = (%s, %s), want both preserved — deactivation is not un-publishing",
			strOrNull(v.PublishedAt), strOrNull(v.PublishedBy))
	}
	// Tenant-scoped, never policy-scoped: one_active is ON (tenant_id), so "no active
	// policy" is a property of the tenant.
	if ids := activeVersionIDs(t, super, tenantID); len(ids) != 0 {
		t.Errorf("active version ids = %v, want none — the tenant is left with no active policy", ids)
	}
}

// --- AC-3: the second delete ---------------------------------------------------

// TestDeletePolicy_SecondDeleteIsNotFoundAndDoesNotRestamp: idempotence comes from the
// predicate. A second delete matches nothing, so it answers ErrPolicyNotFound, re-stamps
// nothing and audits nothing. A store that dropped `AND deleted_at IS NULL` would move the
// stamp and write a second audit row while every other spec here stayed green.
func TestDeletePolicy_SecondDeleteIsNotFoundAndDoesNotRestamp(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 delete-twice")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	seedPublishedVersion(t, super, tenantID, policyID, 1, 0)

	if _, err := store.DeletePolicy(c, policyID); err != nil {
		t.Fatalf("the first DeletePolicy: %v", err)
	}
	first := policyDeletedAt(t, super, policyID)
	if first == nil {
		t.Fatalf("deleted_at is NULL after the first delete — the re-stamp assertion below needs a stamp")
	}

	_, err := store.DeletePolicy(c, policyID)
	if !errors.Is(err, ErrPolicyNotFound) {
		t.Errorf("the second DeletePolicy: err = %v, want ErrPolicyNotFound", err)
	}
	if code := pgCode(err); code != "" {
		t.Errorf("the second DeletePolicy surfaced a raw Postgres error (SQLSTATE %s) — it answers 500, not 404", code)
	}
	if second := policyDeletedAt(t, super, policyID); second == nil || *second != *first {
		t.Errorf("deleted_at = %s after the second delete, want the unchanged %s",
			strOrNull(second), *first)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.deleted"); n != 1 {
		t.Errorf("approval_policy.deleted audit rows = %d, want 1 — a refused delete audits nothing", n)
	}
}

// TestDeletePolicy_UnknownAndMalformedAreNotFound: an unknown uuid and a malformed id are
// both ErrPolicyNotFound, never a 400 that would confirm the id's shape and never a raw
// 22P02 that answers 500. The id must be parsed ABOVE the transaction — 22P02 carries no
// constraint name, so nothing downstream can map it off 500 (the GetPolicy precedent).
func TestDeletePolicy_UnknownAndMalformedAreNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 delete-not-found")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app)

	for _, id := range []string{"not-a-uuid", "", uuid.NewString()} {
		_, err := store.DeletePolicy(c, id)
		if !errors.Is(err, ErrPolicyNotFound) {
			t.Errorf("DeletePolicy(%q): err = %v, want ErrPolicyNotFound", id, err)
		}
		if code := pgCode(err); code != "" {
			t.Errorf("DeletePolicy(%q) surfaced a raw Postgres error (SQLSTATE %s) — it answers 500, not 404",
				id, code)
		}
	}
	if n := auditCount(t, super, tenantID, "approval_policy.deleted"); n != 0 {
		t.Errorf("approval_policy.deleted audit rows = %d, want 0", n)
	}

	// Control: the refusals above are vacuous unless a live policy is deletable.
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	if _, err := store.DeletePolicy(c, policyID); err != nil {
		t.Fatalf("control: DeletePolicy of a live policy: %v, want success", err)
	}
}

// --- AC-4: the never-published policy ------------------------------------------

// TestDeletePolicy_UnpublishedPolicyKeepsItsDraft: the deactivation matches zero rows here,
// which is the normal never-published case and NOT an error — a RowsAffected guard on it
// would turn this into a spurious failure. The draft row and its steps come out
// byte-identical.
func TestDeletePolicy_UnpublishedPolicyKeepsItsDraft(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 delete-unpublished")
	c, _ := activeAdmin(t, super, tenantID)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	stepID := seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("engagement-partner"), SLAHours: ptr(48),
	})
	beforeVersion := versionSnapshot(t, super, versionID)
	beforeStep := stepSnapshot(t, super, stepID)

	if _, err := NewStore(app).DeletePolicy(c, policyID); err != nil {
		t.Fatalf("DeletePolicy of a never-published policy: %v — the deactivation matching 0 rows is legal", err)
	}

	if policyDeletedAt(t, super, policyID) == nil {
		t.Error("deleted_at is NULL — a never-published policy is still stamped")
	}
	if after := versionSnapshot(t, super, versionID); after != beforeVersion {
		t.Errorf("version row = %s, want the unchanged %s", after, beforeVersion)
	}
	if after := stepSnapshot(t, super, stepID); after != beforeStep {
		t.Errorf("step row = %s, want the unchanged %s", after, beforeStep)
	}
	if v := versionRow(t, super, versionID); v.Sealed || v.IsActive {
		t.Errorf("version row = %+v, want still (sealed false, is_active false) — the delete seals nothing", v)
	}
}

// --- AC-5: the response's collections ------------------------------------------

// TestDeletePolicy_AnswersEmptyCollections: a policy that HAS both still answers steps: []
// and versions: [] — never nil, and never the content the rows still hold. A deleted policy
// has no addressable content (the DeleteRole precedent, store.go:309-312).
func TestDeletePolicy_AnswersEmptyCollections(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 delete-empty-collections")
	c, _ := activeAdmin(t, super, tenantID)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	seedPublishedVersion(t, super, tenantID, policyID, 1, 2)
	seedVersionWithSteps(t, super, tenantID, policyID, 2, 1)

	got, err := NewStore(app).DeletePolicy(c, policyID)
	if err != nil {
		t.Fatalf("DeletePolicy: %v", err)
	}
	if got.Steps == nil || got.Versions == nil {
		t.Fatalf("returned policy = %+v; steps and versions must be [] or the wire renders null", got)
	}
	if len(got.Steps) != 0 {
		t.Errorf("returned steps = %+v, want empty", got.Steps)
	}
	if len(got.Versions) != 0 {
		t.Errorf("returned versions = %+v, want empty", got.Versions)
	}

	// The empty collections are a response shape, not a delete: the rows are still there.
	if n := rowCount(t, super, "approval_policy_steps", tenantID); n != 3 {
		t.Errorf("tenant step rows = %d, want the 3 seeded", n)
	}
	if n := len(versionRows(t, super, policyID)); n != 2 {
		t.Errorf("version rows = %d, want the 2 seeded", n)
	}
}

// --- AC-6: the audit ------------------------------------------------------------

// TestDeletePolicy_AuditsInSameTx proves atomicity positively: rows sharing an xmin were
// written by one transaction. The rollback form ("neither row exists") passes vacuously
// against a two-transaction store, because any failure raised before the audit statement
// also leaves neither row behind.
//
// The join includes the DEACTIVATED version row, which is only valid on a PUBLISHED
// fixture: on a never-published policy the delete writes no version row at all and its
// xmin is still the seed's, exactly the trap policy_publish_test.go:594 documents. v1 is
// sealed but inactive, so it is untouched and deliberately outside the join.
func TestDeletePolicy_AuditsInSameTx(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 delete-audit-atomicity")
	c, adminID := activeAdmin(t, super, tenantID)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	sealApprovalPolicyVersion(t, super, seedVersionWithSteps(t, super, tenantID, policyID, 1, 0))
	activeVersion := seedPublishedVersion(t, super, tenantID, policyID, 2, 0)

	if _, err := NewStore(app).DeletePolicy(c, policyID); err != nil {
		t.Fatalf("DeletePolicy: %v", err)
	}

	// policy_id is compared as a uuid on both sides: bare, the payload leg would ask for
	// text = uuid and the join would fail to PLAN whatever the store wrote.
	var policyXmin, versionXmin, auditXmin, actor, payload string
	if err := super.QueryRow(context.Background(),
		`SELECT p.xmin::text, v.xmin::text, a.xmin::text, a.actor, a.payload::text
		   FROM approval_policies p, approval_policy_versions v, audit_log a
		  WHERE p.id = $1::uuid AND v.id = $2::uuid
		    AND a.tenant_id = $3 AND a.event = 'approval_policy.deleted'
		    AND (a.payload->>'policy_id')::uuid = $1::uuid`,
		policyID, activeVersion, tenantID,
	).Scan(&policyXmin, &versionXmin, &auditXmin, &actor, &payload); err != nil {
		t.Fatalf("xmin join (no row means the policy, its deactivated version and its audit event do not all exist): %v", err)
	}
	// Frozen or invalid xids read as 2 and 0; either would make the comparison meaningless.
	for label, x := range map[string]string{
		"approval_policies":        policyXmin,
		"approval_policy_versions": versionXmin,
		"audit_log":                auditXmin,
	} {
		if x == "0" || x == "2" {
			t.Fatalf("%s.xmin = %s — a frozen/invalid xid makes this proof vacuous", label, x)
		}
	}
	if policyXmin != versionXmin || policyXmin != auditXmin {
		t.Errorf("xmin: policies = %s, versions = %s, audit_log = %s — the stamp, the deactivation and "+
			"the audit must share one transaction", policyXmin, versionXmin, auditXmin)
	}
	if actor != adminID {
		t.Errorf("audit actor = %q, want the caller's subject %q", actor, adminID)
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(payload), &body); err != nil {
		t.Fatalf("audit payload %q is not an object: %v", payload, err)
	}
	assertPayloadKeys(t, body)
	if body["policy_id"] != policyID {
		t.Errorf("audit payload policy_id = %v, want %q", body["policy_id"], policyID)
	}
	if body["version"] != float64(2) {
		t.Errorf("audit payload version = %v, want the HIGHEST version number 2", body["version"])
	}
	if n := auditCount(t, super, tenantID, "approval_policy.deleted"); n != 1 {
		t.Errorf("approval_policy.deleted audit rows = %d, want 1", n)
	}
}

// --- AC-7: tenancy --------------------------------------------------------------

// TestDeletePolicy_CrossTenantIsNotFound: RLS is the only tenant filter. B holds an ACTIVE
// ADMIN of its own — without one, requireActiveAdmin answers ErrNotPermitted first and the
// refusal would prove nothing about tenancy.
func TestDeletePolicy_CrossTenantIsNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	tenantA := policyTenant(t, super, "APPR-05 delete-cross-tenant A")
	cA, _ := activeAdmin(t, super, tenantA)
	policyA := seedApprovalPolicy(t, super, tenantA, "A policy")
	versionA := seedPublishedVersion(t, super, tenantA, policyA, 1, 0)

	tenantB := policyTenant(t, super, "APPR-05 delete-cross-tenant B")
	cB, _ := activeAdmin(t, super, tenantB)

	if _, err := store.DeletePolicy(cB, policyA); !errors.Is(err, ErrPolicyNotFound) {
		t.Errorf("DeletePolicy(A's policy) as B: err = %v, want ErrPolicyNotFound", err)
	}
	if at := policyDeletedAt(t, super, policyA); at != nil {
		t.Errorf("A's deleted_at = %q after B's refused delete, want NULL", *at)
	}
	if ids := activeVersionIDs(t, super, tenantA); len(ids) != 1 || ids[0] != versionA {
		t.Errorf("A's active version ids = %v, want still [%s] — B's refused call deactivates nothing",
			ids, versionA)
	}
	for _, tc := range []struct {
		label    string
		tenantID string
	}{{"A", tenantA}, {"B", tenantB}} {
		if n := auditCount(t, super, tc.tenantID, "approval_policy.deleted"); n != 0 {
			t.Errorf("approval_policy.deleted audit rows under tenant %s = %d, want 0", tc.label, n)
		}
	}

	// Control: A still deletes its own, so the refusal is not a store that refuses everyone.
	if _, err := store.DeletePolicy(cA, policyA); err != nil {
		t.Fatalf("DeletePolicy(A's policy) as A: %v — the refusal above is vacuous unless this succeeds", err)
	}
}

// --- the returned shape ----------------------------------------------------------

// TestDeletePolicy_ReturnsAnInertDraftShape pins the WHOLE returned struct. The
// coalesce(max(version), 0) the audit payload needs is sitting right there and assigning it
// to Version would make the response claim v3 has no steps — Version names the version
// Steps belongs to, and Steps is []. status "draft" is newPolicy()'s documented inert
// state, sealed is false, and only ID/Name/Scope are carried through from the RETURNING.
func TestDeletePolicy_ReturnsAnInertDraftShape(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 delete-inert-shape")
	c, _ := activeAdmin(t, super, tenantID)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	sealApprovalPolicyVersion(t, super, seedVersionWithSteps(t, super, tenantID, policyID, 1, 0))
	sealApprovalPolicyVersion(t, super, seedVersionWithSteps(t, super, tenantID, policyID, 2, 0))
	seedPublishedVersion(t, super, tenantID, policyID, 3, 1)

	got, err := NewStore(app).DeletePolicy(c, policyID)
	if err != nil {
		t.Fatalf("DeletePolicy: %v", err)
	}
	want := Policy{
		ID:       policyID,
		Name:     "Sign-off",
		Scope:    scopeAllInvoices,
		Status:   "draft",
		Version:  0,
		Sealed:   false,
		Steps:    []Step{},
		Versions: []PolicyVersion{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeletePolicy = %+v, want %+v — version 3 is the AUDIT payload's number and nothing else's",
			got, want)
	}

	// The wire form is the second belt: Policy.MarshalJSON substitutes [] for a nil slice,
	// so the DeepEqual above is what catches nil and this catches a tag regression.
	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal the returned policy: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(blob, &wire); err != nil {
		t.Fatalf("the marshalled policy %s is not an object: %v", blob, err)
	}
	for _, key := range []string{"steps", "versions"} {
		if string(wire[key]) != "[]" {
			t.Errorf("wire %s = %s, want []", key, wire[key])
		}
	}

	// The max the audit reads really is 3, so the Version assertion above is not vacuous.
	body := deletedAuditPayload(t, super, tenantID, policyID)
	if body["version"] != float64(3) {
		t.Errorf("audit payload version = %v, want 3", body["version"])
	}
}

// --- the zero-version policy ------------------------------------------------------

// TestDeletePolicy_ZeroVersionPolicyAuditsVersionZero: a policy carrying no version row at
// all is reachable from a direct DB row, and coalesce(max(version), 0) IS the handling —
// the audit records 0 rather than failing to scan a NULL.
func TestDeletePolicy_ZeroVersionPolicyAuditsVersionZero(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 delete-zero-version")
	c, _ := activeAdmin(t, super, tenantID)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	if n := len(versionRows(t, super, policyID)); n != 0 {
		t.Fatalf("fixture has %d version rows, want 0", n)
	}

	got, err := NewStore(app).DeletePolicy(c, policyID)
	if err != nil {
		t.Fatalf("DeletePolicy of a version-less policy: %v, want success", err)
	}
	if got.Version != 0 {
		t.Errorf("returned version = %d, want 0", got.Version)
	}
	if policyDeletedAt(t, super, policyID) == nil {
		t.Error("deleted_at is NULL — a version-less policy is still stamped")
	}

	body := deletedAuditPayload(t, super, tenantID, policyID)
	assertPayloadKeys(t, body)
	if body["version"] != float64(0) {
		t.Errorf("audit payload version = %v, want 0 — coalesce(max(version), 0) is the handling", body["version"])
	}
	if n := auditCount(t, super, tenantID, "approval_policy.deleted"); n != 1 {
		t.Errorf("approval_policy.deleted audit rows = %d, want 1", n)
	}
}

// --- the delete racing a publish ---------------------------------------------------

// deleteInBackground runs one delete and reports its error exactly once, the
// publishInBackground shape.
func deleteInBackground(store *Store, ctx context.Context, policyID string) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := store.DeletePolicy(ctx, policyID)
		done <- err
	}()
	return done
}

// waitUntilNBlockedBy is waitUntilBlockedBy with the waiter COUNT named: the delete and the
// publish queue on the same policy row, and only the second arrival distinguishes "both
// waiting" from "the delete alone". Same outcome oracle — a call that returned while the
// row is locked never took the lock, so the racing branch fails immediately rather than
// after a sleep.
func waitUntilNBlockedBy(t *testing.T, super *pgxpool.Pool, holderPID, want int, done <-chan error, what string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.After(lockWaitDeadline)
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			t.Fatalf("%s returned (err = %v) while the policy row was locked by pid %d — it never took "+
				"the row lock, so it cannot serialise against a concurrent publish", what, err, holderPID)
		case <-deadline:
			t.Fatalf("%s neither blocked on pid %d nor returned within %s", what, holderPID, lockWaitDeadline)
		case <-tick.C:
			var n int
			if err := super.QueryRow(ctx,
				`SELECT count(*) FROM pg_stat_activity
				  WHERE datname = current_database() AND $1 = ANY(pg_blocking_pids(pid))`,
				holderPID).Scan(&n); err != nil {
				t.Fatalf("poll pg_blocking_pids: %v", err)
			}
			if n >= want {
				return
			}
		}
	}
}

// awaitCall takes a background call's result once the blocker has let go.
func awaitCall(t *testing.T, done <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(lockWaitDeadline):
		t.Fatalf("%s did not return after the blocking transaction ended", what)
		return nil
	}
}

// TestDeletePolicy_ConcurrentPublishLosesAsNotFound pins the outcome the ordering rule
// exists to produce, which nothing else covers: the delete's opening UPDATE of the policy
// row is the lock that serialises it against a publish, and a publish that loses the race
// answers ErrPolicyNotFound with nothing sealed.
//
// The interleaving is deterministic because the lock queue is FIFO: the delete is observed
// waiting on the blocker BEFORE the publish is started, so it acquires the row first and
// COMMITS, and the publish's SELECT ... FOR UPDATE then re-checks deleted_at IS NULL under
// READ COMMITTED (EvalPlanQual) against the committed stamp.
//
// What it pins and what it does not: the first wait fails outright if DeletePolicy never
// contends for the policy row, and the outcome legs fail if the delete does not commit its
// stamp before the publish resumes. It cannot see the ORDER of statements inside the
// delete's own transaction — that stays a code-review obligation.
func TestDeletePolicy_ConcurrentPublishLosesAsNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := policyTenant(t, super, "APPR-05 delete-races-publish")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalDraft(t, super, tenantID, policyID)

	// A real statement from a writer the delete races, not a lock it would never meet.
	blocker, holderPID := blockerTx(t, ctx, app, tenantID)
	if _, err := blocker.Exec(ctx,
		`SELECT id FROM approval_policies WHERE id = $1 FOR UPDATE`, policyID); err != nil {
		t.Fatalf("blocker holds the policy row: %v", err)
	}

	deleteDone := deleteInBackground(store, c, policyID)
	waitUntilNBlockedBy(t, super, holderPID, 1, deleteDone, "DeletePolicy")

	// Still live while the delete waits: it is queued, not racing ahead of the blocker.
	if at := policyDeletedAt(t, super, policyID); at != nil {
		t.Errorf("deleted_at = %q while the policy row is locked, want still NULL", *at)
	}

	publishDone := publishInBackground(store, c, policyID)
	waitUntilNBlockedBy(t, super, holderPID, 2, publishDone, "PublishPolicy")

	if err := blocker.Rollback(ctx); err != nil {
		t.Fatalf("release the blocker: %v", err)
	}
	if err := awaitCall(t, deleteDone, "DeletePolicy"); err != nil {
		t.Fatalf("DeletePolicy after the lock was released: %v, want success", err)
	}
	if err := awaitCall(t, publishDone, "PublishPolicy"); !errors.Is(err, ErrPolicyNotFound) {
		t.Errorf("PublishPolicy behind a committed delete: err = %v, want ErrPolicyNotFound", err)
	}

	v := versionRow(t, super, versionID)
	if v.Sealed || v.IsActive {
		t.Errorf("version row = %+v, want still (sealed false, is_active false) — the losing publish seals nothing", v)
	}
	if v.PublishedAt != nil || v.PublishedBy != nil {
		t.Errorf("published_at/by = (%s, %s), want both NULL",
			strOrNull(v.PublishedAt), strOrNull(v.PublishedBy))
	}
	if n := auditCount(t, super, tenantID, "approval_policy.published"); n != 0 {
		t.Errorf("approval_policy.published audit rows = %d, want 0", n)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.deleted"); n != 1 {
		t.Errorf("approval_policy.deleted audit rows = %d, want 1", n)
	}
}
