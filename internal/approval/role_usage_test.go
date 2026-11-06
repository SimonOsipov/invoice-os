package approval

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seedDeactivatedPolicy seeds a policy whose sealed v1 names the role in scope and a step
// but is no longer active; v2 is active and names nothing.
func seedDeactivatedPolicy(t *testing.T, super *pgxpool.Pool, tenantID, name string, position int, roleID, roleKey string) {
	t.Helper()
	policyID, v1 := seedScopedPolicyDraft(t, super, tenantID, name, position, scopeSpec{RoleIDs: []string{roleID}})
	seedApprovalPolicyStepInLane(t, super, tenantID, v1, seedStepSpec{Ord: 0, Kind: "approval", WorkflowRoleKey: ptr(roleKey)})
	activateApprovalPolicyVersion(t, super, v1)
	if _, err := super.Exec(context.Background(), `UPDATE approval_policy_versions SET is_active = false WHERE id = $1`, v1); err != nil {
		t.Fatalf("deactivate v1: %v", err)
	}
	activateApprovalPolicyVersion(t, super, seedApprovalPolicyVersionN(t, super, tenantID, policyID, 2))
}

func listUsage(t *testing.T, app *pgxpool.Pool, ctx context.Context) map[string][]RoleUse {
	t.Helper()
	usage, err := NewStore(app, stubFingerprinter, nil).ListRoleUsage(ctx)
	if err != nil {
		t.Fatalf("ListRoleUsage: %v", err)
	}
	if usage == nil {
		t.Fatal("ListRoleUsage returned a nil map")
	}
	return usage
}

func TestRoleUsage_ListsARoleAnActiveScopeNames(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-08 scope")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	p, _ := seedActivePolicy(t, super, tenantID, "Scoped", 1, []string{roleID})

	got := listUsage(t, app, c)["tax-reviewer"]
	if len(got) != 1 || got[0] != (RoleUse{ID: p, Name: "Scoped", Steps: 0, Scope: true}) {
		t.Errorf("usage = %+v, want one scope-only entry for %s", got, p)
	}
}

func TestRoleUsage_ListsARoleAnActiveStepNames(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-08 step")
	c, _ := activeAdmin(t, super, tenantID)
	seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	p, _ := seedActivePolicy(t, super, tenantID, "Sign-off", 1, nil, "tax-reviewer", "tax-reviewer")

	got := listUsage(t, app, c)["tax-reviewer"]
	if len(got) != 1 || got[0] != (RoleUse{ID: p, Name: "Sign-off", Steps: 2, Scope: false}) {
		t.Errorf("usage = %+v, want one entry with 2 steps, no scope", got)
	}
}

func TestRoleUsage_ListsARoleAStepInAConditionLaneNames(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-08 lane")
	c, _ := activeAdmin(t, super, tenantID)
	seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	_, versionID := seedScopedPolicyDraft(t, super, tenantID, "Lane", 1, scopeSpec{})
	cond := seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("1000.00"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		ParentStepID: &cond, Branch: ptr("then"), Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("tax-reviewer"),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	got := listUsage(t, app, c)["tax-reviewer"]
	if len(got) != 1 || got[0].Steps != 1 || got[0].Scope {
		t.Errorf("usage = %+v, want one entry with 1 step", got)
	}
}

func TestRoleUsage_ListsAPolicyOnceWhenScopeAndStepsBothNameTheRole(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-08 once")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	seedActivePolicy(t, super, tenantID, "Both", 1, []string{roleID}, "tax-reviewer", "tax-reviewer")

	got := listUsage(t, app, c)["tax-reviewer"]
	if len(got) != 1 || got[0].Steps != 2 || !got[0].Scope {
		t.Errorf("usage = %+v, want one entry with 2 steps and scope", got)
	}
}

func TestRoleUsage_OrdersPoliciesByPositionThenID(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-08 order")
	c, _ := activeAdmin(t, super, tenantID)
	seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	second, _ := seedActivePolicy(t, super, tenantID, "Second", 2, nil, "tax-reviewer")
	first, _ := seedActivePolicy(t, super, tenantID, "First", 1, nil, "tax-reviewer")

	got := listUsage(t, app, c)["tax-reviewer"]
	if len(got) != 2 || got[0].ID != first || got[1].ID != second {
		t.Errorf("usage = %+v, want [%s %s]", got, first, second)
	}
}

func TestRoleUsage_IgnoresADraftOnlyReference(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-08 draft only")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	_, versionID := seedScopedPolicyDraft(t, super, tenantID, "Draft", 1, scopeSpec{RoleIDs: []string{roleID}})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("tax-reviewer")})

	if got := listUsage(t, app, c); len(got) != 0 {
		t.Errorf("usage = %+v, want empty", got)
	}
}

func TestRoleUsage_IgnoresADeactivatedVersionAndADeletedPolicy(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-08 deactivated")
	c, _ := activeAdmin(t, super, tenantID)
	roleA := seedWorkflowRole(t, super, tenantID, "deactivated", "Deactivated")
	seedWorkflowRole(t, super, tenantID, "deleted-policy", "Deleted Policy")
	seedDeactivatedPolicy(t, super, tenantID, "Old", 1, roleA, "deactivated")
	p, _ := seedActivePolicy(t, super, tenantID, "Gone", 2, nil, "deleted-policy")
	softDeleteApprovalPolicy(t, super, p)

	if got := listUsage(t, app, c); len(got) != 0 {
		t.Errorf("usage = %+v, want empty", got)
	}
}

func TestRoleUsage_IgnoresAKeyOnANonApprovalStep(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-08 notify")
	c, _ := activeAdmin(t, super, tenantID)
	seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	_, versionID := seedScopedPolicyDraft(t, super, tenantID, "Notify", 1, scopeSpec{})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "notify", WorkflowRoleKey: ptr("tax-reviewer"),
		NotifyTarget: ptr("ops@example.com"), NotifyChannel: ptr("email"),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	if got := listUsage(t, app, c); len(got) != 0 {
		t.Errorf("usage = %+v, want empty", got)
	}
}

func TestRoleUsage_AnotherTenantsPolicyIsInvisible(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := policyTenant(t, super, "APSC-08 tenant A")
	tenantB := policyTenant(t, super, "APSC-08 tenant B")
	c, _ := activeAdmin(t, super, tenantA)
	seedWorkflowRole(t, super, tenantA, "tax-reviewer", "Tax Reviewer")
	roleB := seedWorkflowRole(t, super, tenantB, "tax-reviewer", "Tax Reviewer")
	seedActivePolicy(t, super, tenantB, "B policy", 1, []string{roleB}, "tax-reviewer")

	if got := listUsage(t, app, c); len(got) != 0 {
		t.Errorf("usage = %+v, want empty", got)
	}
}

func TestRoleUsage_NamesNoDeletedRole(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-08 deleted role")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	softDeleteWorkflowRole(t, super, roleID)

	if got := listUsage(t, app, c); len(got) != 0 {
		t.Errorf("usage = %+v, want empty", got)
	}
}

func TestRoleUsage_KeepsTheActiveVersionUnderADraftThatDropsTheRole(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-08 active under draft")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	policyID, _ := seedActivePolicy(t, super, tenantID, "P", 1, []string{roleID}, "tax-reviewer")
	seedApprovalPolicyVersionN(t, super, tenantID, policyID, 2)

	got := listUsage(t, app, c)["tax-reviewer"]
	if len(got) != 1 || got[0] != (RoleUse{ID: policyID, Name: "P", Steps: 1, Scope: true}) {
		t.Errorf("usage = %+v, want the active version's entry", got)
	}
}

// The read comes first: a DeleteRole that is not refused soft-deletes the role.
func TestRoleUsage_AgreesWithDeleteRole(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-08 parity")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	scopeRole := seedWorkflowRole(t, super, tenantID, "scope-only", "Scope Only")
	seedWorkflowRole(t, super, tenantID, "step-only", "Step Only")
	seedWorkflowRole(t, super, tenantID, "lane-step", "Lane Step")
	draftRole := seedWorkflowRole(t, super, tenantID, "draft-only", "Draft Only")
	deactRole := seedWorkflowRole(t, super, tenantID, "deactivated", "Deactivated")
	seedWorkflowRole(t, super, tenantID, "deleted-policy", "Deleted Policy")
	seedWorkflowRole(t, super, tenantID, "non-approval", "Non Approval")
	seedWorkflowRole(t, super, tenantID, "unused", "Unused")

	seedActivePolicy(t, super, tenantID, "Scope", 1, []string{scopeRole})
	seedActivePolicy(t, super, tenantID, "Step", 2, nil, "step-only")

	_, laneV := seedScopedPolicyDraft(t, super, tenantID, "Lane", 3, scopeSpec{})
	cond := seedApprovalPolicyStepInLane(t, super, tenantID, laneV, seedStepSpec{
		Ord: 0, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("1000.00"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, laneV, seedStepSpec{
		ParentStepID: &cond, Branch: ptr("then"), Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("lane-step"),
	})
	activateApprovalPolicyVersion(t, super, laneV)

	_, draftV := seedScopedPolicyDraft(t, super, tenantID, "Draft", 4, scopeSpec{RoleIDs: []string{draftRole}})
	seedApprovalPolicyStepInLane(t, super, tenantID, draftV, seedStepSpec{Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("draft-only")})

	seedDeactivatedPolicy(t, super, tenantID, "Old", 5, deactRole, "deactivated")

	gone, _ := seedActivePolicy(t, super, tenantID, "Gone", 6, nil, "deleted-policy")
	softDeleteApprovalPolicy(t, super, gone)

	_, notifyV := seedScopedPolicyDraft(t, super, tenantID, "Notify", 7, scopeSpec{})
	seedApprovalPolicyStepInLane(t, super, tenantID, notifyV, seedStepSpec{
		Ord: 0, Kind: "notify", WorkflowRoleKey: ptr("non-approval"),
		NotifyTarget: ptr("ops@example.com"), NotifyChannel: ptr("email"),
	})
	activateApprovalPolicyVersion(t, super, notifyV)

	usage := listUsage(t, app, c)
	for _, key := range []string{"scope-only", "step-only", "lane-step", "draft-only", "deactivated", "deleted-policy", "non-approval", "unused"} {
		_, err := store.DeleteRole(c, key)
		refused := errors.Is(err, ErrRoleInUse)
		if refused != (len(usage[key]) > 0) {
			t.Errorf("%s: DeleteRole refused = %v (err %v), usage entries = %d", key, refused, err, len(usage[key]))
			continue
		}
		if !refused {
			if err != nil {
				t.Errorf("%s: DeleteRole err = %v, want success", key, err)
			}
			continue
		}
		var inUse *roleInUse
		if !errors.As(err, &inUse) || len(inUse.policies) != len(usage[key]) {
			t.Errorf("%s: refusal policies = %+v, usage = %+v", key, inUse, usage[key])
			continue
		}
		for i, ref := range inUse.policies {
			if ref.ID != usage[key][i].ID || ref.Name != usage[key][i].Name {
				t.Errorf("%s: refusal policy %d = %+v, usage = %+v", key, i, ref, usage[key][i])
			}
		}
	}
	for _, key := range []string{"scope-only", "step-only", "lane-step"} {
		if len(usage[key]) == 0 {
			t.Errorf("%s: no usage entry, want one (the parity loop is vacuous otherwise)", key)
		}
	}
}
