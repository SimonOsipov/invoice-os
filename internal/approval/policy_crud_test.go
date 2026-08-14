package approval

// The policy read/write seam under a real Postgres: CreatePolicy, ListPolicies and
// GetPolicy as invoice_app, through db.WithinRequestTenantTx, with RLS as the only
// tenant filter.
//
// Written before the method bodies exist, so every spec here starts RED against
// policy_store.go's stub. Run locally with `DEV_DB_PORT=5433 make test-approvals`;
// in CI the rls job's gate step fails the build on any skip.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- fixtures ---------------------------------------------------------------

// policyTenant seeds a tenant and registers teardownSealedApprovalFixture AFTER
// seedTenant's own cleanup, so LIFO runs the explicit bottom-up delete first. Applied
// to every tenant in this file rather than only the sealing ones: a sealed version
// makes the tenant cascade raise 23001, and seedTenant discards that error, so the
// leak is permanent and silent.
func policyTenant(t *testing.T, super *pgxpool.Pool, name string) string {
	t.Helper()
	tenantID := seedTenant(t, super, name)
	t.Cleanup(func() { teardownSealedApprovalFixture(t, super, tenantID) })
	return tenantID
}

// seedApprovalPolicyVersionN inserts one unsealed version at an explicit number.
// seedApprovalPolicyVersion hardcodes 1, so the sealed-v1 + open-v2 shape AC-3 and
// AC-5 both need is unseedable with it. Seal through sealApprovalPolicyVersion after
// the version's steps are in: the content lock refuses an INSERT under a sealed row,
// and approval_policy_versions_one_draft refuses a second unsealed version.
func seedApprovalPolicyVersionN(t *testing.T, super *pgxpool.Pool, tenantID, policyID string, version int) string {
	t.Helper()
	var id string
	err := super.QueryRow(context.Background(),
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version)
		 VALUES ($1, $2, $3) RETURNING id`,
		tenantID, policyID, version,
	).Scan(&id)
	if failIfUndefinedApprovalSchema(t, "seed approval_policy_versions", err) {
		return ""
	}
	if err != nil {
		t.Fatalf("seed approval_policy_versions v%d: %v", version, err)
	}
	return id
}

// seedStepSpec is one approval_policy_steps row for seedApprovalPolicyStepInLane.
type seedStepSpec struct {
	ParentStepID    *string
	Branch          *string // nil at root; "then" or "else" inside a lane
	Ord             int
	Kind            string
	WorkflowRoleKey *string
	SLAHours        *int
	CondOp          *string
	CondAmount      *string
	NotifyTarget    *string
	NotifyChannel   *string
}

// seedApprovalPolicyStepInLane writes a step including branch and the seven optional
// columns. seedApprovalPolicyStep writes neither, and both gaps are load-bearing: a
// branch-NULL child is silently promoted to a root by nestSteps, and two branch-NULL
// siblings at the same ord collide on approval_policy_steps_slot_uq, whose NULLS NOT
// DISTINCT makes a two-lane condition unseedable.
func seedApprovalPolicyStepInLane(t *testing.T, super *pgxpool.Pool, tenantID, versionID string, s seedStepSpec) string {
	t.Helper()
	var id string
	// cond_amount goes through ::text::numeric so the decimal text is what the column
	// parses, never a driver-side float.
	err := super.QueryRow(context.Background(),
		`INSERT INTO approval_policy_steps
		     (tenant_id, version_id, parent_step_id, branch, ord, kind,
		      workflow_role_key, sla_hours, cond_op, cond_amount, notify_target, notify_channel)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::text::numeric, $11, $12)
		 RETURNING id`,
		tenantID, versionID, s.ParentStepID, s.Branch, s.Ord, s.Kind,
		s.WorkflowRoleKey, s.SLAHours, s.CondOp, s.CondAmount, s.NotifyTarget, s.NotifyChannel,
	).Scan(&id)
	if failIfUndefinedApprovalSchema(t, "seed approval_policy_steps", err) {
		return ""
	}
	if err != nil {
		t.Fatalf("seed approval_policy_steps (kind %s, ord %d): %v", s.Kind, s.Ord, err)
	}
	return id
}

// softDeleteApprovalPolicy stamps deleted_at, and Fatals if no live row matched —
// without that guard a mis-seed turns the soft-delete specs into vacuous passes.
func softDeleteApprovalPolicy(t *testing.T, super *pgxpool.Pool, policyID string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE approval_policies SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL`, policyID)
	if err != nil {
		t.Fatalf("soft-delete approval policy %s: %v", policyID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("soft-delete approval policy %s affected %d rows, want 1", policyID, tag.RowsAffected())
	}
}

// storedPolicyScope reads the scope COLUMN as the superuser. The read-back, not the
// returned struct, is what AC-2 turns on.
func storedPolicyScope(t *testing.T, super *pgxpool.Pool, policyID string) string {
	t.Helper()
	var scope string
	if err := super.QueryRow(context.Background(),
		`SELECT scope FROM approval_policies WHERE id = $1`, policyID).Scan(&scope); err != nil {
		t.Fatalf("read back scope of %s: %v", policyID, err)
	}
	return scope
}

// flattenTree walks a nested tree depth-first, for "no step from anywhere else leaked
// in" assertions.
func flattenTree(steps []Step) []Step {
	out := []Step{}
	for _, s := range steps {
		out = append(out, s)
		out = append(out, flattenTree(s.Then)...)
		out = append(out, flattenTree(s.Else)...)
	}
	return out
}

func policyNames(policies []Policy) []string {
	names := make([]string, 0, len(policies))
	for _, p := range policies {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return names
}

// --- AC-1: create ------------------------------------------------------------

// TestPolicy_CreateInsertsVersionOne: one policy row and one draft version 1, and a
// returned Policy whose lanes are [] rather than nil.
func TestPolicy_CreateInsertsVersionOne(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 create-v1")
	c, _ := activeAdmin(t, super, tenantID)

	got, err := NewStore(app, stubFingerprinter, nil).CreatePolicy(c, "Sign-off", "")
	if err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}
	if _, err := uuid.Parse(got.ID); err != nil {
		t.Fatalf("returned id %q is not a uuid: %v", got.ID, err)
	}
	if got.Steps == nil {
		t.Error("steps is nil; the producer must construct it as []Step{} or the wire renders null")
	}
	want := Policy{
		ID:       got.ID,
		Name:     "Sign-off",
		Scope:    scopeAllInvoices,
		Status:   "draft",
		Version:  1,
		Sealed:   false,
		Steps:    []Step{},
		Versions: []PolicyVersion{{Version: 1}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CreatePolicy = %+v, want %+v", got, want)
	}

	var name, scope string
	var deletedAt *string
	if err := super.QueryRow(context.Background(),
		`SELECT name, scope, deleted_at::text FROM approval_policies WHERE tenant_id = $1`,
		tenantID).Scan(&name, &scope, &deletedAt); err != nil {
		t.Fatalf("read back the policy row: %v", err)
	}
	if name != "Sign-off" || scope != scopeAllInvoices {
		t.Errorf("stored policy = (%q, %q), want (Sign-off, %q)", name, scope, scopeAllInvoices)
	}
	if deletedAt != nil {
		t.Errorf("stored deleted_at = %v, want NULL — a new policy is live", *deletedAt)
	}

	// Counted separately: pgx QueryRow scans the first row and discards the rest,
	// so a second version row is invisible to the scan below.
	if n := rowCount(t, super, "approval_policy_versions", tenantID); n != 1 {
		t.Errorf("version rows = %d, want exactly 1 — create mints one", n)
	}

	var version int
	var sealed, isActive bool
	var publishedAt, publishedBy *string
	if err := super.QueryRow(context.Background(),
		`SELECT version, sealed, is_active, published_at::text, published_by
		   FROM approval_policy_versions WHERE tenant_id = $1`,
		tenantID).Scan(&version, &sealed, &isActive, &publishedAt, &publishedBy); err != nil {
		t.Fatalf("read back the version row: %v", err)
	}
	if version != 1 || sealed || isActive {
		t.Errorf("stored version row = (version %d, sealed %v, is_active %v), want (1, false, false)",
			version, sealed, isActive)
	}
	if publishedAt != nil || publishedBy != nil {
		t.Errorf("stored published_at/by = (%v, %v), want both NULL — nothing is published at create",
			publishedAt, publishedBy)
	}
	if n := rowCount(t, super, "approval_policy_steps", tenantID); n != 0 {
		t.Errorf("step rows = %d, want 0 — a new policy has an empty tree", n)
	}
}

// TestPolicy_CreateAuditsInSameTx proves atomicity positively: rows sharing an xmin
// were inserted by one transaction. The rollback form ("neither row exists") would
// pass vacuously against a two-transaction store, since any failure raised before the
// audit statement also leaves neither row behind. This form fails one outright.
func TestPolicy_CreateAuditsInSameTx(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 create-audit-atomicity")
	c, adminID := activeAdmin(t, super, tenantID)

	created, err := NewStore(app, stubFingerprinter, nil).CreatePolicy(c, "Sign-off", "")
	if err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}

	var policyXmin, versionXmin, auditXmin, actor, payload string
	if err := super.QueryRow(context.Background(),
		`SELECT p.xmin::text, v.xmin::text, a.xmin::text, a.actor, a.payload::text
		   FROM approval_policies p, approval_policy_versions v, audit_log a
		  WHERE p.tenant_id = $1 AND p.id = $2::uuid
		    AND v.tenant_id = $1 AND v.policy_id = p.id
		    AND a.tenant_id = $1 AND a.event = 'approval_policy.created'
		    AND a.payload->>'policy_id' = $3`,
		tenantID, created.ID, created.ID,
	).Scan(&policyXmin, &versionXmin, &auditXmin, &actor, &payload); err != nil {
		t.Fatalf("xmin join (no row means the policy, its version and its audit event do not all exist): %v", err)
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
		t.Errorf("xmin: policies = %s, versions = %s, audit_log = %s — all three writes must share one transaction",
			policyXmin, versionXmin, auditXmin)
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
	if body["policy_id"] != created.ID {
		t.Errorf("audit payload policy_id = %v, want the RETURNING id %q", body["policy_id"], created.ID)
	}
	if body["version"] != float64(1) {
		t.Errorf("audit payload version = %v, want 1", body["version"])
	}
	if n := auditCount(t, super, tenantID, "approval_policy.created"); n != 1 {
		t.Errorf("approval_policy.created audit rows = %d, want 1", n)
	}
}

// --- AC-2: normalization above the transaction --------------------------------

// TestCreatePolicy_ForeignScopeRejected: each of the five retired scopes the server
// refuses is ErrValidation with nothing written. A raw *pgconn.PgError
// with code 23514 fails this test — policyStatusForErr takes sentinels only, so an
// unmapped check violation answers 500 where the design promises 400. The trailing
// positive control keeps the zero-row assertions from passing against a store that
// refuses everything.
func TestCreatePolicy_ForeignScopeRejected(t *testing.T) {
	super, app := dbTestPools(t)

	for _, scope := range removedScopes {
		t.Run(scope, func(t *testing.T) {
			tenantID := policyTenant(t, super, "APPR-05 foreign-scope")
			c, _ := activeAdmin(t, super, tenantID)

			_, err := NewStore(app, stubFingerprinter, nil).CreatePolicy(c, "Sign-off", scope)
			if !errors.Is(err, ErrValidation) {
				t.Errorf("CreatePolicy(scope %q) err = %v, want ErrValidation", scope, err)
			}
			if code := pgCode(err); code != "" {
				t.Errorf("CreatePolicy(scope %q) surfaced a raw Postgres error (SQLSTATE %s) — "+
					"policyStatusForErr maps sentinels only, so this answers 500 instead of 400", scope, code)
			}
			if n := rowCount(t, super, "approval_policies", tenantID); n != 0 {
				t.Errorf("approval_policies rows = %d, want 0 — the scope is refused above the transaction", n)
			}
			if n := rowCount(t, super, "approval_policy_versions", tenantID); n != 0 {
				t.Errorf("approval_policy_versions rows = %d, want 0", n)
			}
			if n := auditCount(t, super, tenantID, "approval_policy.created"); n != 0 {
				t.Errorf("approval_policy.created audit rows = %d, want 0", n)
			}
		})
	}

	t.Run("control: the accepted scope still writes", func(t *testing.T) {
		tenantID := policyTenant(t, super, "APPR-05 foreign-scope-control")
		c, _ := activeAdmin(t, super, tenantID)
		if _, err := NewStore(app, stubFingerprinter, nil).CreatePolicy(c, "Sign-off", scopeAllInvoices); err != nil {
			t.Fatalf("CreatePolicy(%q): %v — the refusals above are vacuous unless this succeeds", scopeAllInvoices, err)
		}
		if n := rowCount(t, super, "approval_policies", tenantID); n != 1 {
			t.Errorf("approval_policies rows = %d, want 1", n)
		}
	})
}

// TestCreatePolicy_EmptyNameRejected: a whitespace-only name is ErrValidation with
// nothing written, and the same caller can still create a named policy.
func TestCreatePolicy_EmptyNameRejected(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 empty-name")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	_, err := store.CreatePolicy(c, "   ", "")
	if !errors.Is(err, ErrValidation) {
		t.Errorf("CreatePolicy with a blank name: err = %v, want ErrValidation", err)
	}
	if code := pgCode(err); code != "" {
		t.Errorf("CreatePolicy with a blank name surfaced SQLSTATE %s — the name never reaches SQL", code)
	}
	if n := rowCount(t, super, "approval_policies", tenantID); n != 0 {
		t.Errorf("approval_policies rows = %d, want 0", n)
	}
	if n := rowCount(t, super, "approval_policy_versions", tenantID); n != 0 {
		t.Errorf("approval_policy_versions rows = %d, want 0", n)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.created"); n != 0 {
		t.Errorf("approval_policy.created audit rows = %d, want 0", n)
	}

	created, err := store.CreatePolicy(c, "  Sign-off  ", "")
	if err != nil {
		t.Fatalf("CreatePolicy with a real name: %v — the refusal above is vacuous unless this succeeds", err)
	}
	if created.Name != "Sign-off" {
		t.Errorf("returned name = %q, want the trimmed %q", created.Name, "Sign-off")
	}
	var stored string
	if err := super.QueryRow(context.Background(),
		`SELECT name FROM approval_policies WHERE id = $1`, created.ID).Scan(&stored); err != nil {
		t.Fatalf("read back the name: %v", err)
	}
	if stored != "Sign-off" {
		t.Errorf("stored name = %q, want the trimmed %q", stored, "Sign-off")
	}
}

// TestCreatePolicy_EmptyScopeNormalizedBeforeSQL: an absent scope is written as the
// default, not as "". Reading the stored COLUMN is the load-bearing part — a store
// that echoes the normalized value while writing the raw one passes a response-only
// assertion. An explicit "" reaching SQL raises 23514 approval_policies_scope_check,
// so a Postgres error here IS the regression.
func TestCreatePolicy_EmptyScopeNormalizedBeforeSQL(t *testing.T) {
	super, app := dbTestPools(t)

	for _, in := range []string{"", "   ", scopeAllInvoices} {
		t.Run("scope "+in, func(t *testing.T) {
			tenantID := policyTenant(t, super, "APPR-05 scope-normalized")
			c, _ := activeAdmin(t, super, tenantID)

			created, err := NewStore(app, stubFingerprinter, nil).CreatePolicy(c, "Sign-off", in)
			if err != nil {
				t.Fatalf("CreatePolicy(scope %q): %v (SQLSTATE %q) — an unnormalized scope reaching SQL "+
					"raises 23514 on approval_policies_scope_check", in, err, pgCode(err))
			}
			if got := storedPolicyScope(t, super, created.ID); got != scopeAllInvoices {
				t.Errorf("STORED scope column = %q, want %q", got, scopeAllInvoices)
			}
			if created.Scope != scopeAllInvoices {
				t.Errorf("returned scope = %q, want %q", created.Scope, scopeAllInvoices)
			}
		})
	}
}

// --- AC-3: list ---------------------------------------------------------------

// TestPolicy_ListReturnsTreeAndVersions: a sealed v1 and an open v2 carrying a
// two-lane condition. The list carries the highest version's whole tree, its versions
// newest first, and nothing from the sealed version.
func TestPolicy_ListReturnsTreeAndVersions(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 list-tree")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")

	// v1 gets a step of its own, then is sealed: if the store read every version's
	// steps rather than only the top one, this marker would surface in the tree.
	v1 := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, v1, seedStepSpec{
		Ord: 0, Kind: "notify",
		NotifyTarget: ptr("v1-step-must-not-appear"), NotifyChannel: ptr("email"),
	})
	sealApprovalPolicyVersion(t, super, v1)

	v2 := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 2)
	cond := seedApprovalPolicyStepInLane(t, super, tenantID, v2, seedStepSpec{
		Ord: 0, Kind: "condition", CondOp: ptr(">="), CondAmount: ptr("1000.00"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, v2, seedStepSpec{
		ParentStepID: &cond, Branch: ptr("then"), Ord: 0, Kind: "approval",
		WorkflowRoleKey: ptr("engagement-partner"), SLAHours: ptr(48),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, v2, seedStepSpec{
		ParentStepID: &cond, Branch: ptr("else"), Ord: 0, Kind: "notify",
		NotifyTarget: ptr("finance@example.com"), NotifyChannel: ptr("email"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, v2, seedStepSpec{Ord: 1, Kind: "autoapprove"})

	got, err := NewStore(app, stubFingerprinter, nil).ListPolicies(c)
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListPolicies returned %d policies, want 1: %+v", len(got), got)
	}
	p := got[0]
	if p.ID != policyID || p.Name != "Sign-off" || p.Scope != scopeAllInvoices {
		t.Errorf("policy = (%q, %q, %q), want (%q, Sign-off, %q)", p.ID, p.Name, p.Scope, policyID, scopeAllInvoices)
	}
	if p.Version != 2 || p.Sealed || p.Status != "draft" {
		t.Errorf("policy = (version %d, sealed %v, status %q), want (2, false, draft)", p.Version, p.Sealed, p.Status)
	}
	wantVersions := []PolicyVersion{{Version: 2}, {Version: 1, Sealed: true}}
	if !reflect.DeepEqual(p.Versions, wantVersions) {
		t.Errorf("versions = %+v, want %+v (newest first)", p.Versions, wantVersions)
	}

	if len(p.Steps) != 2 {
		t.Fatalf("root lane has %d steps, want 2: %+v", len(p.Steps), p.Steps)
	}
	root := p.Steps[0]
	if root.Kind != "condition" || root.CondOp == nil || *root.CondOp != ">=" {
		t.Errorf("root step = (kind %q, cond_op %v), want (condition, >=)", root.Kind, root.CondOp)
	}
	// The exact decimal text. It does NOT pin the ::text cast — pgx renders a bare
	// numeric as "1000.00" too, and only zero tells the two apart
	// (TestPolicy_CondAmountKeepsItsScaleAtZero).
	if root.CondAmount == nil || *root.CondAmount != "1000.00" {
		t.Errorf("root cond_amount = %v, want the exact decimal text 1000.00", root.CondAmount)
	}
	if len(root.Then) != 1 || len(root.Else) != 1 {
		t.Fatalf("condition lanes = (then %d, else %d), want (1, 1): %+v", len(root.Then), len(root.Else), root)
	}
	thenStep := root.Then[0]
	if thenStep.Kind != "approval" || thenStep.WorkflowRoleKey == nil || *thenStep.WorkflowRoleKey != "engagement-partner" {
		t.Errorf("then lane = (kind %q, role %v), want (approval, engagement-partner)", thenStep.Kind, thenStep.WorkflowRoleKey)
	}
	if thenStep.SLAHours == nil || *thenStep.SLAHours != 48 {
		t.Errorf("then lane sla_hours = %v, want 48", thenStep.SLAHours)
	}
	if thenStep.Then == nil || thenStep.Else == nil {
		t.Error("a leaf step has a nil lane; nestLane must build [] so the wire never renders null")
	}
	elseStep := root.Else[0]
	if elseStep.Kind != "notify" || elseStep.NotifyTarget == nil || *elseStep.NotifyTarget != "finance@example.com" {
		t.Errorf("else lane = (kind %q, target %v), want (notify, finance@example.com)", elseStep.Kind, elseStep.NotifyTarget)
	}
	if p.Steps[1].Kind != "autoapprove" {
		t.Errorf("second root step kind = %q, want autoapprove (roots in ord order)", p.Steps[1].Kind)
	}
	for _, s := range flattenTree(p.Steps) {
		if s.NotifyTarget != nil && *s.NotifyTarget == "v1-step-must-not-appear" {
			t.Errorf("the sealed v1 step surfaced in the tree — steps must come from the highest version only: %+v", s)
		}
	}
}

// TestPolicy_ListOmitsSoftDeleted: deleted_at IS NULL is the list predicate, and the
// deleted policy's steps are never read. The statement count is the other half of
// AC-3: three statements whatever the policy count.
func TestPolicy_ListOmitsSoftDeleted(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 list-soft-deleted")
	c, _ := activeAdmin(t, super, tenantID)

	for _, name := range []string{"Live A", "Live C"} {
		id := seedApprovalPolicy(t, super, tenantID, name)
		v := seedApprovalPolicyVersionN(t, super, tenantID, id, 1)
		seedApprovalPolicyStepInLane(t, super, tenantID, v, seedStepSpec{
			Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("engagement-partner"),
		})
	}
	deletedID := seedApprovalPolicy(t, super, tenantID, "Deleted B")
	deletedVersion := seedApprovalPolicyVersionN(t, super, tenantID, deletedID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, deletedVersion, seedStepSpec{
		Ord: 0, Kind: "notify",
		NotifyTarget: ptr("deleted-step-must-not-appear"), NotifyChannel: ptr("email"),
	})
	softDeleteApprovalPolicy(t, super, deletedID)

	got, err := NewStore(app, stubFingerprinter, nil).ListPolicies(c)
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	if want := []string{"Live A", "Live C"}; !reflect.DeepEqual(policyNames(got), want) {
		t.Fatalf("ListPolicies names = %v, want %v", policyNames(got), want)
	}
	for _, p := range got {
		if p.ID == deletedID {
			t.Errorf("the soft-deleted policy %s is in the list", deletedID)
		}
		for _, s := range flattenTree(p.Steps) {
			if s.NotifyTarget != nil && *s.NotifyTarget == "deleted-step-must-not-appear" {
				t.Errorf("a step of the soft-deleted policy surfaced under %q: %+v", p.Name, s)
			}
		}
	}

	// Three statements, constant in policy count: an N+1 tree read would issue one
	// per policy and is otherwise invisible, since the results are identical.
	t.Run("three statements, constant in policy count", func(t *testing.T) {
		traced, rec := tracedAppPool(t)
		rec.reset()
		if _, err := NewStore(traced, stubFingerprinter, nil).ListPolicies(c); err != nil {
			t.Fatalf("ListPolicies on the traced pool: %v", err)
		}
		sql := rec.mentioning("approval_polic")
		if len(sql) != 3 {
			t.Errorf("ListPolicies issued %d statements against the policy tables, want 3:\n%v", len(sql), sql)
		}
	})
}

// --- AC-4: get ----------------------------------------------------------------

// TestPolicy_GetUnknownAndMalformedAreNotFound: an unknown uuid, a malformed id and a
// soft-deleted policy are all ErrPolicyNotFound. A malformed id must never reach SQL —
// 22P02 carries no constraint name, so nothing downstream can map it off 500.
func TestPolicy_GetUnknownAndMalformedAreNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 get-not-found")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	liveID := seedApprovalPolicy(t, super, tenantID, "Live")
	seedApprovalPolicyVersionN(t, super, tenantID, liveID, 1)
	deletedID := seedApprovalPolicy(t, super, tenantID, "Deleted")
	seedApprovalPolicyVersionN(t, super, tenantID, deletedID, 1)
	softDeleteApprovalPolicy(t, super, deletedID)

	for _, tc := range []struct {
		name string
		id   string
	}{
		{"unknown uuid", uuid.NewString()},
		{"malformed id", "not-a-uuid"},
		{"soft-deleted policy", deletedID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.GetPolicy(c, tc.id)
			if !errors.Is(err, ErrPolicyNotFound) {
				t.Errorf("GetPolicy(%q) err = %v, want ErrPolicyNotFound", tc.id, err)
			}
			if code := pgCode(err); code != "" {
				t.Errorf("GetPolicy(%q) surfaced a raw Postgres error (SQLSTATE %s) — it answers 500, not 404", tc.id, code)
			}
		})
	}

	// Control: the refusals above are vacuous unless a live policy is found.
	got, err := store.GetPolicy(c, liveID)
	if err != nil {
		t.Fatalf("GetPolicy on the live policy: %v", err)
	}
	if got.ID != liveID || got.Name != "Live" {
		t.Errorf("GetPolicy = (%q, %q), want (%q, Live)", got.ID, got.Name, liveID)
	}
	if got.Steps == nil || got.Versions == nil {
		t.Errorf("GetPolicy returned nil steps or versions (%+v); both must be [] on the wire", got)
	}
}

// --- AC-5: derived status -----------------------------------------------------

// TestPolicy_StatusIsDraftIffUnsealedVersionExists: status is derived, never stored,
// and it must come from the same row Sealed does.
func TestPolicy_StatusIsDraftIffUnsealedVersionExists(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 derived-status")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")

	v1 := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	sealApprovalPolicyVersion(t, super, v1)

	got, err := store.GetPolicy(c, policyID)
	if err != nil {
		t.Fatalf("GetPolicy with only a sealed version: %v", err)
	}
	if got.Status != "published" || !got.Sealed || got.Version != 1 {
		t.Errorf("sealed-only policy = (status %q, sealed %v, version %d), want (published, true, 1)",
			got.Status, got.Sealed, got.Version)
	}

	seedApprovalPolicyVersionN(t, super, tenantID, policyID, 2)

	got, err = store.GetPolicy(c, policyID)
	if err != nil {
		t.Fatalf("GetPolicy after opening a draft: %v", err)
	}
	if got.Status != "draft" || got.Sealed || got.Version != 2 {
		t.Errorf("policy with an open draft = (status %q, sealed %v, version %d), want (draft, false, 2)",
			got.Status, got.Sealed, got.Version)
	}
}

// --- AC-6: permissions --------------------------------------------------------

// TestPolicy_ReadNeedsNoAdminRoleWriteDoes: reads are ungated exactly as
// GET /v1/workflow-roles is; writes need an active admin.
func TestPolicy_ReadNeedsNoAdminRoleWriteDoes(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 read-ungated")
	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	store := NewStore(app, stubFingerprinter, nil)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)

	list, err := store.ListPolicies(c)
	if err != nil {
		t.Fatalf("ListPolicies as a preparer: %v", err)
	}
	if len(list) != 1 || list[0].ID != policyID {
		t.Errorf("ListPolicies as a preparer = %+v, want the one seeded policy %s", list, policyID)
	}
	if _, err := store.GetPolicy(c, policyID); err != nil {
		t.Fatalf("GetPolicy as a preparer: %v", err)
	}

	if _, err := store.CreatePolicy(c, "Second policy", ""); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("CreatePolicy as a preparer: err = %v, want ErrNotPermitted", err)
	}
	if n := rowCount(t, super, "approval_policies", tenantID); n != 1 {
		t.Errorf("approval_policies rows = %d, want the 1 seeded row", n)
	}
}

// TestPolicy_CreatePermissionCheckedBeforeRowRead: a suspended admin is refused as
// firmly as a preparer, before any policy row is touched. The argument validators sit
// ABOVE the transaction, so a blank name is ErrValidation even for a caller who would
// also be refused — that ordering is what keeps an unstorable argument from opening a
// transaction at all.
func TestPolicy_CreatePermissionCheckedBeforeRowRead(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 write-permission")
	c, _ := callerCtx(t, super, tenantID, "admin", "suspended")

	traced, rec := tracedAppPool(t)
	rec.reset()
	_, err := NewStore(traced, stubFingerprinter, nil).CreatePolicy(c, "Sign-off", "")
	if !errors.Is(err, ErrNotPermitted) {
		t.Errorf("CreatePolicy as a suspended admin: err = %v, want ErrNotPermitted", err)
	}
	if sql := rec.mentioning("memberships"); len(sql) == 0 {
		t.Error("no memberships statement was issued — requireActiveAdmin did not run")
	}
	if sql := rec.mentioning("approval_polic"); len(sql) != 0 {
		t.Errorf("a policy-table statement ran despite the refusal, so the permission check is not first:\n%v", sql)
	}
	if n := rowCount(t, super, "approval_policies", tenantID); n != 0 {
		t.Errorf("approval_policies rows = %d, want 0", n)
	}
	if n := rowCount(t, super, "approval_policy_versions", tenantID); n != 0 {
		t.Errorf("approval_policy_versions rows = %d, want 0", n)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.created"); n != 0 {
		t.Errorf("approval_policy.created audit rows = %d, want 0", n)
	}

	if _, err := NewStore(app, stubFingerprinter, nil).CreatePolicy(c, "   ", ""); !errors.Is(err, ErrValidation) {
		t.Errorf("CreatePolicy with a blank name as a suspended admin: err = %v, want ErrValidation — "+
			"the normalizers run above the transaction, so they answer before the permission check", err)
	}
}

// --- AC-7: tenancy ------------------------------------------------------------

// TestPolicy_CrossTenantGetIsNotFound: RLS is the only tenant filter, so tenant B
// neither fetches nor lists tenant A's policy.
func TestPolicy_CrossTenantGetIsNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app, stubFingerprinter, nil)

	tenantA := policyTenant(t, super, "APPR-05 cross-tenant A")
	cA, _ := activeAdmin(t, super, tenantA)
	policyA := seedApprovalPolicy(t, super, tenantA, "A policy")
	seedApprovalPolicyVersionN(t, super, tenantA, policyA, 1)

	tenantB := policyTenant(t, super, "APPR-05 cross-tenant B")
	cB, _ := activeAdmin(t, super, tenantB)
	policyB := seedApprovalPolicy(t, super, tenantB, "B policy")
	seedApprovalPolicyVersionN(t, super, tenantB, policyB, 1)

	if _, err := store.GetPolicy(cB, policyA); !errors.Is(err, ErrPolicyNotFound) {
		t.Errorf("GetPolicy(A's policy) as B: err = %v, want ErrPolicyNotFound", err)
	}
	listB, err := store.ListPolicies(cB)
	if err != nil {
		t.Fatalf("ListPolicies as B: %v", err)
	}
	if want := []string{"B policy"}; !reflect.DeepEqual(policyNames(listB), want) {
		t.Errorf("ListPolicies as B = %v, want %v", policyNames(listB), want)
	}

	// Control: A still sees its own, so the refusals above are not a store that
	// returns nothing to anyone.
	got, err := store.GetPolicy(cA, policyA)
	if err != nil {
		t.Fatalf("GetPolicy(A's policy) as A: %v — the refusals above are vacuous unless this succeeds", err)
	}
	if got.ID != policyA {
		t.Errorf("GetPolicy as A = %q, want %q", got.ID, policyA)
	}
}

// --- negative control ---------------------------------------------------------

// TestPolicy_BranchlessChildPromotesToRootNotDetected pins nestSteps fail-soft
// behaviour: a row with a parent but no branch is silently promoted to a root, and a
// child whose parent is missing from the set is silently dropped. Neither is an error,
// so a partially read step set yields a WRONG tree rather than a failure — which is
// why the tree read carries no per-step predicate beyond version_id. Change this test
// only by making nestSteps detect the shape, never by loosening that read.
func TestPolicy_BranchlessChildPromotesToRootNotDetected(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 branchless-child")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)

	parent := seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("500.00"),
	})
	// A real lane member carries branch. This one does not, and ord 1 keeps it off the
	// root slot the parent already holds.
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		ParentStepID: &parent, Ord: 1, Kind: "approval",
		WorkflowRoleKey: ptr("engagement-partner"),
	})

	got, err := NewStore(app, stubFingerprinter, nil).GetPolicy(c, policyID)
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if len(got.Steps) != 2 {
		t.Fatalf("root lane has %d steps, want 2 — the branch-less child is promoted, not nested: %+v",
			len(got.Steps), got.Steps)
	}
	if got.Steps[0].ID != parent {
		t.Errorf("first root step = %q, want the seeded parent %q", got.Steps[0].ID, parent)
	}
	if len(got.Steps[0].Then) != 0 || len(got.Steps[0].Else) != 0 {
		t.Errorf("the parent has lane members (%d then, %d else); a branch-less child cannot be placed in a lane",
			len(got.Steps[0].Then), len(got.Steps[0].Else))
	}
	if got.Steps[1].Kind != "approval" {
		t.Errorf("second root step kind = %q, want the promoted approval", got.Steps[1].Kind)
	}
}
