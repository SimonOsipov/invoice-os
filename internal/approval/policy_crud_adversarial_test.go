package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Edge, negative and boundary coverage for CreatePolicy/ListPolicies/GetPolicy, beyond
// the acceptance specs in policy_crud_test.go.

// --- fixtures ---------------------------------------------------------------

// publishApprovalPolicyVersion seals a version and stamps the publish columns. The seal
// guard refuses unsealing and deleting, never a later UPDATE of these three, and
// approval_policy_versions_active_is_sealed needs sealed set in the same statement.
func publishApprovalPolicyVersion(t *testing.T, super *pgxpool.Pool, versionID, by string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE approval_policy_versions
		    SET sealed = true, is_active = true, published_at = now(), published_by = $2
		  WHERE id = $1`, versionID, by)
	if err != nil {
		t.Fatalf("publish approval_policy_versions %s: %v", versionID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("publish approval_policy_versions %s affected %d rows, want 1", versionID, tag.RowsAffected())
	}
}

// stampPolicyCreatedAt forces created_at so the L1 ordering and its id tie-break are
// observable rather than at the mercy of now() resolution.
func stampPolicyCreatedAt(t *testing.T, super *pgxpool.Pool, policyID, at string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE approval_policies SET created_at = $2::timestamptz WHERE id = $1`, policyID, at)
	if err != nil {
		t.Fatalf("stamp created_at on %s: %v", policyID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("stamp created_at on %s affected %d rows, want 1", policyID, tag.RowsAffected())
	}
}

// showAmount renders a *string cond_amount for a failure message; %v on the pointer
// prints an address.
func showAmount(a *string) string {
	if a == nil {
		return "<nil>"
	}
	return *a
}

func policyByID(policies []Policy, id string) (Policy, bool) {
	for _, p := range policies {
		if p.ID == id {
			return p, true
		}
	}
	return Policy{}, false
}

func policyIDs(policies []Policy) []string {
	ids := make([]string, 0, len(policies))
	for _, p := range policies {
		ids = append(ids, p.ID)
	}
	return ids
}

// --- the money read -----------------------------------------------------------

// TestPolicy_CondAmountKeepsItsScaleAtZero pins the cond_amount::text cast at the one
// value that can see it. pgx scans numeric into a *string on its own, and for 1000.00
// it produces "1000.00" either way — so the acceptance spec's fixture cannot tell the
// cast from its absence. Zero can: measured on :5433, the uncast read yields "0" where
// the column holds 0.00. Zero is a supported cond_amount
// (TestPolicy_ValidateCondAmountKeepsZeroLegal), so dropping the cast is a live wire
// regression, not a hypothetical one.
func TestPolicy_CondAmountKeepsItsScaleAtZero(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 cond-amount-scale")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	for i, amount := range []string{"0.00", "0.10", "1000", "99999999999.99"} {
		seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
			Ord: i, Kind: "condition", CondOp: ptr(">="), CondAmount: ptr(amount),
		})
	}
	// What numeric(14,2) actually stores: "1000" arrives back scaled to two places.
	want := []string{"0.00", "0.10", "1000.00", "99999999999.99"}

	got, err := store.GetPolicy(c, policyID)
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if len(got.Steps) != len(want) {
		t.Fatalf("root lane has %d steps, want %d", len(got.Steps), len(want))
	}
	for i, w := range want {
		if got.Steps[i].CondAmount == nil || *got.Steps[i].CondAmount != w {
			t.Errorf("GetPolicy step %d cond_amount = %q, want the exact decimal text %q — "+
				"an uncast numeric read drops the scale", i, showAmount(got.Steps[i].CondAmount), w)
		}
	}

	list, err := store.ListPolicies(c)
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListPolicies returned %d policies, want 1", len(list))
	}
	for i, w := range want {
		if list[0].Steps[i].CondAmount == nil || *list[0].Steps[i].CondAmount != w {
			t.Errorf("ListPolicies step %d cond_amount = %q, want %q — both readers share one tree read",
				i, showAmount(list[0].Steps[i].CondAmount), w)
		}
	}
}

// --- which version Steps names -------------------------------------------------

// TestPolicy_GetStepsComeFromTheTopVersion: GetPolicy carries the HIGHEST version's tree.
// The acceptance specs pin this for ListPolicies only, and GetPolicy derives its version
// id on a separate code path — one that reading the lowest version instead would satisfy
// every other spec in the suite.
func TestPolicy_GetStepsComeFromTheTopVersion(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 get-top-version")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")

	v1 := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, v1, seedStepSpec{
		Ord: 0, Kind: "notify",
		NotifyTarget: ptr("v1-sealed-must-not-appear"), NotifyChannel: ptr("email"),
	})
	sealApprovalPolicyVersion(t, super, v1)

	v2 := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 2)
	seedApprovalPolicyStepInLane(t, super, tenantID, v2, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("engagement-partner"),
	})

	got, err := NewStore(app, stubFingerprinter, nil).GetPolicy(c, policyID)
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if got.Version != 2 || got.Sealed || got.Status != "draft" {
		t.Errorf("policy = (version %d, sealed %v, status %q), want (2, false, draft)",
			got.Version, got.Sealed, got.Status)
	}
	if len(got.Steps) != 1 || got.Steps[0].Kind != "approval" {
		t.Fatalf("steps = %+v, want the open v2's single approval step", got.Steps)
	}
	for _, s := range flattenTree(got.Steps) {
		if s.NotifyTarget != nil && *s.NotifyTarget == "v1-sealed-must-not-appear" {
			t.Errorf("the sealed v1 step surfaced: %+v", s)
		}
	}
}

// TestPolicy_GetAfterRepublishNamesTheDraftNotTheActiveTree pins the shape 07 and 09
// consume, and the gap in it. Once a draft is reopened over a published version, Steps is
// the DRAFT tree while a different version is the one in force. The only marker of the
// version in force is versions[].is_active, and the active version's tree is not reachable
// from this endpoint at all — a run evaluator must therefore not read its tree from here.
func TestPolicy_GetAfterRepublishNamesTheDraftNotTheActiveTree(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 republish-shape")
	c, publisher := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")

	v1 := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, v1, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("in-force-role"),
	})
	publishApprovalPolicyVersion(t, super, v1, publisher)

	v2 := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 2)
	seedApprovalPolicyStepInLane(t, super, tenantID, v2, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("draft-only-role"),
	})

	got, err := NewStore(app, stubFingerprinter, nil).GetPolicy(c, policyID)
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if len(got.Steps) != 1 || got.Steps[0].WorkflowRoleKey == nil ||
		*got.Steps[0].WorkflowRoleKey != "draft-only-role" {
		t.Fatalf("steps = %+v, want the open draft's tree", got.Steps)
	}
	if got.Version != 2 {
		t.Errorf("version = %d, want 2 — Version names the version Steps belongs to", got.Version)
	}
	if len(got.Versions) != 2 {
		t.Fatalf("versions = %+v, want two entries newest first", got.Versions)
	}
	if got.Versions[0].Version != 2 || got.Versions[0].IsActive {
		t.Errorf("versions[0] = %+v, want the open draft, inactive", got.Versions[0])
	}
	// The version in force is named ONLY here, and its tree is not in this response.
	if got.Versions[1].Version != 1 || !got.Versions[1].Sealed || !got.Versions[1].IsActive {
		t.Errorf("versions[1] = %+v, want the sealed active v1", got.Versions[1])
	}
	if got.Versions[1].PublishedAt == nil || got.Versions[1].PublishedBy == nil ||
		*got.Versions[1].PublishedBy != publisher {
		t.Errorf("versions[1] publish columns = (%v, %v), want both set with publisher %q",
			got.Versions[1].PublishedAt, got.Versions[1].PublishedBy, publisher)
	}
	// RFC3339 with a T separator, not the column text form ("2026-08-11 00:00:00+00").
	// The Z pins the UTC normalisation, which only bites where the process TZ is not UTC:
	// pgx hands back timestamptz in time.Local, so on a UTC runner an unnormalised value
	// renders Z anyway.
	var parsed struct {
		PublishedAt string `json:"published_at"`
	}
	raw, err := json.Marshal(got.Versions[1])
	if err != nil {
		t.Fatalf("marshal version: %v", err)
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal version: %v", err)
	}
	if len(parsed.PublishedAt) < 20 || parsed.PublishedAt[10] != 'T' ||
		parsed.PublishedAt[len(parsed.PublishedAt)-1] != 'Z' {
		t.Errorf("published_at on the wire = %q, want a UTC RFC3339 instant", parsed.PublishedAt)
	}
}

// TestPolicy_PublishedByWithoutPublishedAtRendersNullNotZero: the two columns are
// independent, so a half-stamped row must render published_at as null rather than an
// epoch instant.
func TestPolicy_PublishedByWithoutPublishedAtRendersNullNotZero(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 half-published")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)

	if _, err := super.Exec(context.Background(),
		`UPDATE approval_policy_versions SET published_by = $2 WHERE id = $1`,
		versionID, "someone@example.com"); err != nil {
		t.Fatalf("stamp published_by: %v", err)
	}

	got, err := NewStore(app, stubFingerprinter, nil).GetPolicy(c, policyID)
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if len(got.Versions) != 1 {
		t.Fatalf("versions = %+v, want one", got.Versions)
	}
	if got.Versions[0].PublishedAt != nil {
		t.Errorf("published_at = %q, want nil — the column is NULL", *got.Versions[0].PublishedAt)
	}
	if got.Versions[0].PublishedBy == nil || *got.Versions[0].PublishedBy != "someone@example.com" {
		t.Errorf("published_by = %v, want someone@example.com", got.Versions[0].PublishedBy)
	}
	raw, err := json.Marshal(got.Versions[0])
	if err != nil {
		t.Fatalf("marshal version: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal version: %v", err)
	}
	if v, ok := body["published_at"]; !ok || v != nil {
		t.Errorf("published_at on the wire = %v (present %v), want an explicit null", v, ok)
	}
}

// --- the zero-version shape ----------------------------------------------------

// TestPolicy_PolicyWithNoVersionRowsIsAnInertDraft pins the deviation newPolicy() takes.
// AC-5 read literally makes "no unsealed version exists" published, which would answer
// published alongside version 0, sealed false and an empty tree — a policy that claims to
// be live while naming no version at all. draft is the inert reading and it is what both
// readers must keep answering. The shape is reachable from the DB (a policy row with no
// version row), never from CreatePolicy, whose two inserts share one transaction.
func TestPolicy_PolicyWithNoVersionRowsIsAnInertDraft(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 no-versions")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)
	policyID := seedApprovalPolicy(t, super, tenantID, "Orphan")

	want := Policy{
		ID: policyID, Name: "Orphan", Scope: scopeAllInvoices,
		Status: "draft", Version: 0, Sealed: false,
		Steps: []Step{}, Versions: []PolicyVersion{},
	}

	got, err := store.GetPolicy(c, policyID)
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetPolicy = %+v, want %+v", got, want)
	}

	list, err := store.ListPolicies(c)
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListPolicies returned %d policies, want 1", len(list))
	}
	if !reflect.DeepEqual(list[0], want) {
		t.Errorf("ListPolicies[0] = %+v, want %+v", list[0], want)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal policy: %v", err)
	}
	for _, key := range []string{"steps", "versions"} {
		if _, ok := body[key].([]any); !ok {
			t.Errorf("%s on the wire = %v, want an array", key, body[key])
		}
	}
}

// --- list scaling and tenancy --------------------------------------------------

// TestPolicy_ListStaysThreeStatementsAsPoliciesGrow: the acceptance spec counts statements
// at two policies, where an N+1 tree read costs two — close enough to three to be a
// coincidence. Twenty makes the difference structural.
func TestPolicy_ListStaysThreeStatementsAsPoliciesGrow(t *testing.T) {
	super, app := dbTestPools(t)
	_ = app

	for _, n := range []int{5, 20} {
		t.Run(fmt.Sprintf("%d policies", n), func(t *testing.T) {
			tenantID := policyTenant(t, super, fmt.Sprintf("APPR-05 scale-%d", n))
			c, _ := activeAdmin(t, super, tenantID)
			for i := 0; i < n; i++ {
				policyID := seedApprovalPolicy(t, super, tenantID, fmt.Sprintf("Policy %02d", i))
				// Two versions each: the sealed one must never reach the tree read.
				v1 := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
				seedApprovalPolicyStepInLane(t, super, tenantID, v1, seedStepSpec{
					Ord: 0, Kind: "autoapprove",
				})
				sealApprovalPolicyVersion(t, super, v1)
				v2 := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 2)
				cond := seedApprovalPolicyStepInLane(t, super, tenantID, v2, seedStepSpec{
					Ord: 0, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("100.00"),
				})
				seedApprovalPolicyStepInLane(t, super, tenantID, v2, seedStepSpec{
					ParentStepID: &cond, Branch: ptr("then"), Ord: 0, Kind: "approval",
					WorkflowRoleKey: ptr("engagement-partner"),
				})
			}

			traced, rec := tracedAppPool(t)
			rec.reset()
			got, err := NewStore(traced, stubFingerprinter, nil).ListPolicies(c)
			if err != nil {
				t.Fatalf("ListPolicies: %v", err)
			}
			if len(got) != n {
				t.Fatalf("ListPolicies returned %d policies, want %d", len(got), n)
			}
			sql := rec.mentioning("approval_polic")
			if len(sql) != 3 {
				t.Fatalf("ListPolicies issued %d statements against the policy tables at %d policies, want 3:\n%v",
					len(sql), n, sql)
			}
			// The step array is built in Go from the version scan, so exactly one tree read
			// carries every top version at once.
			for _, p := range got {
				if p.Version != 2 || len(p.Steps) != 1 || len(p.Steps[0].Then) != 1 {
					t.Fatalf("policy %q = (version %d, %d roots), want the open v2's nested tree",
						p.Name, p.Version, len(p.Steps))
				}
			}
		})
	}
}

// TestPolicy_ListIsBoundedToTheTenantByRLSAlone: the version and step reads carry no
// WHERE on the tenant at all, so RLS is the only thing standing between a tenant and its
// neighbour's version history. A two-tenant fixture proves it, and the statement text
// proves no predicate was quietly added instead.
func TestPolicy_ListIsBoundedToTheTenantByRLSAlone(t *testing.T) {
	super, app := dbTestPools(t)
	_ = app

	tenantA := policyTenant(t, super, "APPR-05 rls-bound A")
	for i := 0; i < 3; i++ {
		policyA := seedApprovalPolicy(t, super, tenantA, fmt.Sprintf("A policy %d", i))
		for v := 1; v <= 3; v++ {
			vid := seedApprovalPolicyVersionN(t, super, tenantA, policyA, v)
			seedApprovalPolicyStepInLane(t, super, tenantA, vid, seedStepSpec{
				Ord: 0, Kind: "notify",
				NotifyTarget: ptr("a-step-must-not-leak"), NotifyChannel: ptr("email"),
			})
			if v < 3 {
				sealApprovalPolicyVersion(t, super, vid)
			}
		}
	}

	tenantB := policyTenant(t, super, "APPR-05 rls-bound B")
	cB, _ := activeAdmin(t, super, tenantB)
	policyB := seedApprovalPolicy(t, super, tenantB, "B policy")
	vB := seedApprovalPolicyVersionN(t, super, tenantB, policyB, 1)
	seedApprovalPolicyStepInLane(t, super, tenantB, vB, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("engagement-partner"),
	})

	traced, rec := tracedAppPool(t)
	rec.reset()
	got, err := NewStore(traced, stubFingerprinter, nil).ListPolicies(cB)
	if err != nil {
		t.Fatalf("ListPolicies as B: %v", err)
	}
	if len(got) != 1 || got[0].ID != policyB {
		t.Fatalf("ListPolicies as B = %v, want only %s", policyIDs(got), policyB)
	}
	if len(got[0].Versions) != 1 || got[0].Versions[0].Version != 1 {
		t.Errorf("B's versions = %+v, want only its own v1 — the version read is unfiltered",
			got[0].Versions)
	}
	for _, s := range flattenTree(got[0].Steps) {
		if s.NotifyTarget != nil && *s.NotifyTarget == "a-step-must-not-leak" {
			t.Errorf("a step of tenant A surfaced under B: %+v", s)
		}
	}

	sql := rec.mentioning("approval_polic")
	if len(sql) != 3 {
		t.Fatalf("ListPolicies issued %d statements, want 3:\n%v", len(sql), sql)
	}
	for _, s := range sql {
		if strings.Contains(strings.ToLower(s), "tenant_id") {
			t.Errorf("a list statement carries a tenant_id predicate; RLS is the only tenant filter:\n%s", s)
		}
	}
}

// TestPolicy_ListWithNoPoliciesIsEmptyNotNull: a fresh tenant lists as [], and the empty
// version-id array reaches the tree read without a special case.
func TestPolicy_ListWithNoPoliciesIsEmptyNotNull(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 empty-list")
	c, _ := activeAdmin(t, super, tenantID)

	got, err := NewStore(app, stubFingerprinter, nil).ListPolicies(c)
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	if got == nil {
		t.Fatal("ListPolicies returned nil; the wire must render []")
	}
	if len(got) != 0 {
		t.Fatalf("ListPolicies = %+v, want empty", got)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != "[]" {
		t.Errorf("marshalled = %s, want []", raw)
	}
}

// --- ordering ------------------------------------------------------------------

// TestPolicy_ListOrderIsStableAcrossEqualCreatedAt: created_at alone does not order a
// batch created inside one clock tick, so id is the tie-break and the order must not move
// between calls.
func TestPolicy_ListOrderIsStableAcrossEqualCreatedAt(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 order-stability")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	ids := []string{}
	for i := 0; i < 8; i++ {
		id := seedApprovalPolicy(t, super, tenantID, fmt.Sprintf("Policy %d", i))
		stampPolicyCreatedAt(t, super, id, "2026-01-01 00:00:00+00")
		seedApprovalPolicyVersionN(t, super, tenantID, id, 1)
		ids = append(ids, id)
	}

	first, err := store.ListPolicies(c)
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	order := policyIDs(first)
	if len(order) != len(ids) {
		t.Fatalf("ListPolicies returned %d policies, want %d", len(order), len(ids))
	}
	for i := 0; i < 4; i++ {
		again, err := store.ListPolicies(c)
		if err != nil {
			t.Fatalf("ListPolicies (call %d): %v", i+2, err)
		}
		if !reflect.DeepEqual(policyIDs(again), order) {
			t.Fatalf("call %d order = %v, want the stable %v — created_at ties need the id tie-break",
				i+2, policyIDs(again), order)
		}
	}
	sorted := append([]string(nil), order...)
	for i := 1; i < len(sorted); i++ {
		if sorted[i-1] >= sorted[i] {
			t.Errorf("order is not ascending by id at %d: %v", i, order)
			break
		}
	}
}

// --- names ---------------------------------------------------------------------

// TestPolicy_NameWithSQLAndUnicodeRoundTripsByte: the name is stored and read back
// byte-exact through both readers. normalizeName trims and nothing else, so quoting,
// wildcards and combining marks must survive the INSERT and both reads unchanged.
func TestPolicy_NameWithSQLAndUnicodeRoundTripsByte(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app, stubFingerprinter, nil)

	names := []string{
		`Robert"); DROP TABLE approval_policies;--`,
		`100% _wildcard_ \ backslash`,
		"Ekwuo · B2G  approvals",
		"مرحبا 🌍 Ĥéllo",
		"é vs é", // combining acute vs precomposed: not normalized, not equal
		"tab\tand\nnewline",
	}
	for i, name := range names {
		t.Run(fmt.Sprintf("name %d", i), func(t *testing.T) {
			tenantID := policyTenant(t, super, fmt.Sprintf("APPR-05 name-roundtrip %d", i))
			c, _ := activeAdmin(t, super, tenantID)

			created, err := store.CreatePolicy(c, name, "")
			if err != nil {
				t.Fatalf("CreatePolicy(%q): %v", name, err)
			}
			if created.Name != name {
				t.Errorf("returned name = %q, want %q", created.Name, name)
			}
			var stored string
			if err := super.QueryRow(context.Background(),
				`SELECT name FROM approval_policies WHERE id = $1`, created.ID).Scan(&stored); err != nil {
				t.Fatalf("read back name: %v", err)
			}
			if stored != name {
				t.Errorf("STORED name = %q, want %q", stored, name)
			}
			got, err := store.GetPolicy(c, created.ID)
			if err != nil {
				t.Fatalf("GetPolicy: %v", err)
			}
			if got.Name != name {
				t.Errorf("GetPolicy name = %q, want %q", got.Name, name)
			}
			list, err := store.ListPolicies(c)
			if err != nil {
				t.Fatalf("ListPolicies: %v", err)
			}
			if len(list) != 1 || list[0].Name != name {
				t.Errorf("ListPolicies names = %v, want [%q]", policyNames(list), name)
			}
		})
	}
}

// TestPolicy_NameWithANULIsRefusedNotA500: a NUL is the one byte text will not take —
// Postgres raises 22021, which carries no constraint name, so policyStatusForErr falls to
// its default and answers 500 on input a client chose. normalizeName is above the
// transaction, so the refusal writes nothing.
func TestPolicy_NameWithANULIsRefusedNotA500(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app, stubFingerprinter, nil)

	for i, name := range []string{"Sign\x00off", "\x00", "Sign-off\x00", "\x00Sign-off"} {
		t.Run(fmt.Sprintf("name %d", i), func(t *testing.T) {
			tenantID := policyTenant(t, super, fmt.Sprintf("APPR-05 name-nul %d", i))
			c, _ := activeAdmin(t, super, tenantID)

			_, err := store.CreatePolicy(c, name, "")
			if !errors.Is(err, ErrValidation) {
				t.Errorf("CreatePolicy(%q) err = %v, want ErrValidation", name, err)
			}
			if code := pgCode(err); code != "" {
				t.Errorf("CreatePolicy(%q) surfaced a raw Postgres error (SQLSTATE %s) — "+
					"policyStatusForErr maps sentinels only, so this answers 500 instead of 400", name, code)
			}
			if n := rowCount(t, super, "approval_policies", tenantID); n != 0 {
				t.Errorf("approval_policies rows = %d, want 0 — the name is refused above the transaction", n)
			}
			if n := rowCount(t, super, "approval_policy_versions", tenantID); n != 0 {
				t.Errorf("approval_policy_versions rows = %d, want 0", n)
			}
			if n := auditCount(t, super, tenantID, "approval_policy.created"); n != 0 {
				t.Errorf("approval_policy.created audit rows = %d, want 0", n)
			}
		})
	}

	t.Run("control: the same name without the NUL still writes", func(t *testing.T) {
		tenantID := policyTenant(t, super, "APPR-05 name-nul-control")
		c, _ := activeAdmin(t, super, tenantID)
		if _, err := store.CreatePolicy(c, "Signoff", ""); err != nil {
			t.Fatalf("CreatePolicy: %v — the refusals above are vacuous unless this succeeds", err)
		}
		if n := rowCount(t, super, "approval_policies", tenantID); n != 1 {
			t.Errorf("approval_policies rows = %d, want 1", n)
		}
	})
}

// TestPolicy_DuplicateNamesAreLegalAndBothAddressable: nothing constrains the name, so two
// policies may share one and each must stay reachable by its own id.
func TestPolicy_DuplicateNamesAreLegalAndBothAddressable(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 duplicate-names")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	a, err := store.CreatePolicy(c, "Sign-off", "")
	if err != nil {
		t.Fatalf("CreatePolicy (first): %v", err)
	}
	b, err := store.CreatePolicy(c, "Sign-off", "")
	if err != nil {
		t.Fatalf("CreatePolicy (second): %v", err)
	}
	if a.ID == b.ID {
		t.Fatalf("both creates returned %s", a.ID)
	}
	for _, id := range []string{a.ID, b.ID} {
		got, err := store.GetPolicy(c, id)
		if err != nil {
			t.Fatalf("GetPolicy(%s): %v", id, err)
		}
		if got.ID != id || got.Version != 1 {
			t.Errorf("GetPolicy(%s) = (%q, version %d), want (%s, 1)", id, got.ID, got.Version, id)
		}
	}
	list, err := store.ListPolicies(c)
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	if want := []string{"Sign-off", "Sign-off"}; !reflect.DeepEqual(policyNames(list), want) {
		t.Errorf("ListPolicies names = %v, want %v", policyNames(list), want)
	}
}

// TestPolicy_RecreatingASoftDeletedNameFindsOnlyTheLiveOne: soft delete leaves the old row
// in place, so the reused name must resolve to the new policy and the old id must stay
// unreachable.
func TestPolicy_RecreatingASoftDeletedNameFindsOnlyTheLiveOne(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 recreate-name")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	old, err := store.CreatePolicy(c, "Sign-off", "")
	if err != nil {
		t.Fatalf("CreatePolicy (original): %v", err)
	}
	softDeleteApprovalPolicy(t, super, old.ID)

	fresh, err := store.CreatePolicy(c, "Sign-off", "")
	if err != nil {
		t.Fatalf("CreatePolicy (recreated): %v — a soft-deleted name must not block the reuse", err)
	}
	if fresh.ID == old.ID {
		t.Fatal("the recreate returned the soft-deleted id")
	}
	if _, err := store.GetPolicy(c, old.ID); err == nil {
		t.Error("GetPolicy on the soft-deleted id succeeded, want ErrPolicyNotFound")
	}
	got, err := store.GetPolicy(c, fresh.ID)
	if err != nil {
		t.Fatalf("GetPolicy on the recreated policy: %v", err)
	}
	if got.Version != 1 || got.Status != "draft" {
		t.Errorf("recreated = (version %d, status %q), want (1, draft)", got.Version, got.Status)
	}
	list, err := store.ListPolicies(c)
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	if len(list) != 1 || list[0].ID != fresh.ID {
		t.Errorf("ListPolicies = %v, want only the live %s", policyIDs(list), fresh.ID)
	}
	// Both version rows survive; only the live policy's is reachable.
	if n := rowCount(t, super, "approval_policy_versions", tenantID); n != 2 {
		t.Errorf("approval_policy_versions rows = %d, want 2 — soft delete cascades nothing", n)
	}
}

// --- concurrency ---------------------------------------------------------------

// TestPolicy_ConcurrentCreatesEachGetTheirOwnDraft: approval_policy_versions_one_draft is
// UNIQUE (tenant_id, policy_id) WHERE NOT sealed, and every create mints a fresh policy id,
// so concurrent creates cannot collide on it. If the index is ever narrowed to the tenant,
// this is what fails.
func TestPolicy_ConcurrentCreatesEachGetTheirOwnDraft(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-05 concurrent-create")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	const n = 8
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			p, err := store.CreatePolicy(c, fmt.Sprintf("Concurrent %d", i), "")
			ids[i], errs[i] = p.ID, err
		}(i)
	}
	close(start)
	wg.Wait()

	seen := map[string]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent CreatePolicy %d: %v", i, err)
		}
		if _, perr := uuid.Parse(ids[i]); perr != nil {
			t.Fatalf("create %d returned %q, not a uuid", i, ids[i])
		}
		if seen[ids[i]] {
			t.Fatalf("create %d reused id %s", i, ids[i])
		}
		seen[ids[i]] = true
	}
	if got := rowCount(t, super, "approval_policies", tenantID); got != n {
		t.Errorf("approval_policies rows = %d, want %d", got, n)
	}
	if got := rowCount(t, super, "approval_policy_versions", tenantID); got != n {
		t.Errorf("approval_policy_versions rows = %d, want %d — one draft each", got, n)
	}
	if got := auditCount(t, super, tenantID, "approval_policy.created"); got != n {
		t.Errorf("approval_policy.created audit rows = %d, want %d", got, n)
	}

	list, err := store.ListPolicies(c)
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	if len(list) != n {
		t.Fatalf("ListPolicies returned %d policies, want %d", len(list), n)
	}
	for _, id := range ids {
		p, ok := policyByID(list, id)
		if !ok {
			t.Fatalf("policy %s is missing from the list", id)
		}
		if len(p.Versions) != 1 || p.Versions[0].Version != 1 || p.Versions[0].Sealed {
			t.Errorf("policy %s versions = %+v, want one unsealed v1", id, p.Versions)
		}
	}
}
