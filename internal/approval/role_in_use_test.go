package approval

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seedActivePolicy seeds a live policy at position with a sealed, active version 1 whose
// scope holds scopeRoleIDs and whose root approval steps name stepKeys.
func seedActivePolicy(t *testing.T, super *pgxpool.Pool, tenantID, name string, position int, scopeRoleIDs []string, stepKeys ...string) (policyID, versionID string) {
	t.Helper()
	policyID, versionID = seedScopedPolicyDraft(t, super, tenantID, name, position, scopeSpec{RoleIDs: scopeRoleIDs})
	for i, k := range stepKeys {
		seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
			Ord: i, Kind: "approval", WorkflowRoleKey: ptr(k),
		})
	}
	activateApprovalPolicyVersion(t, super, versionID)
	return policyID, versionID
}

func assertRoleUntouched(t *testing.T, super *pgxpool.Pool, tenantID, roleID string) {
	t.Helper()
	if roleRow(t, super, roleID).DeletedAt != nil {
		t.Error("role deleted_at is set, want NULL — a refused delete writes nothing")
	}
	if n := auditCount(t, super, tenantID, "workflow_role.deleted"); n != 0 {
		t.Errorf("workflow_role.deleted audit rows = %d, want 0", n)
	}
}

func assertRoleDeleted(t *testing.T, super *pgxpool.Pool, tenantID, roleID string) {
	t.Helper()
	if roleRow(t, super, roleID).DeletedAt == nil {
		t.Error("role deleted_at is NULL, want set")
	}
	if n := auditCount(t, super, tenantID, "workflow_role.deleted"); n != 1 {
		t.Errorf("workflow_role.deleted audit rows = %d, want 1", n)
	}
}

func TestDeleteRole_RefusesARoleAnActiveScopeNames(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-06 in-use scope")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	seedActivePolicy(t, super, tenantID, "Scoped", 1, []string{roleID})

	if _, err := NewStore(app, stubFingerprinter, nil).DeleteRole(c, "tax-reviewer"); !errors.Is(err, ErrRoleInUse) {
		t.Fatalf("DeleteRole err = %v, want ErrRoleInUse", err)
	}
	assertRoleUntouched(t, super, tenantID, roleID)
}

func TestDeleteRole_RefusesARoleAnActiveStepNames(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-06 in-use step")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	seedActivePolicy(t, super, tenantID, "Sign-off", 1, nil, "tax-reviewer")

	if _, err := NewStore(app, stubFingerprinter, nil).DeleteRole(c, "tax-reviewer"); !errors.Is(err, ErrRoleInUse) {
		t.Fatalf("DeleteRole err = %v, want ErrRoleInUse", err)
	}
	assertRoleUntouched(t, super, tenantID, roleID)
}

func TestDeleteRole_RefusesARoleAStepInAConditionLaneNames(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-06 in-use lane")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	_, versionID := seedScopedPolicyDraft(t, super, tenantID, "Lane", 1, scopeSpec{})
	cond := seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("1000.00"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		ParentStepID: &cond, Branch: ptr("then"), Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("tax-reviewer"),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	if _, err := NewStore(app, stubFingerprinter, nil).DeleteRole(c, "tax-reviewer"); !errors.Is(err, ErrRoleInUse) {
		t.Fatalf("DeleteRole err = %v, want ErrRoleInUse", err)
	}
	assertRoleUntouched(t, super, tenantID, roleID)
}

func TestDeleteRole_RefusalListsEachPolicyInForceOnceInOrder(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-06 in-use list")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	s1, _ := seedActivePolicy(t, super, tenantID, "S1", 1, []string{roleID}, "tax-reviewer")
	s2, _ := seedActivePolicy(t, super, tenantID, "S2", 2, nil, "tax-reviewer")
	seedActivePolicy(t, super, tenantID, "Q", 3, nil, "other")

	_, err := NewStore(app, stubFingerprinter, nil).DeleteRole(c, "tax-reviewer")
	var inUse *roleInUse
	if !errors.As(err, &inUse) {
		t.Fatalf("DeleteRole err = %v, want *roleInUse", err)
	}
	want := []PolicyRef{{ID: s1, Name: "S1"}, {ID: s2, Name: "S2"}}
	if len(inUse.policies) != 2 || inUse.policies[0] != want[0] || inUse.policies[1] != want[1] {
		t.Errorf("policies = %+v, want %+v", inUse.policies, want)
	}
}

func TestDeleteRole_DeletesARoleOnlyADraftNames(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-06 draft only")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	_, versionID := seedScopedPolicyDraft(t, super, tenantID, "Draft", 1, scopeSpec{RoleIDs: []string{roleID}})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("tax-reviewer"),
	})

	if _, err := NewStore(app, stubFingerprinter, nil).DeleteRole(c, "tax-reviewer"); err != nil {
		t.Fatalf("DeleteRole err = %v, want success", err)
	}
	assertRoleDeleted(t, super, tenantID, roleID)
}

func TestDeleteRole_DeletesARoleOnlyADeactivatedVersionNames(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-06 deactivated")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	policyID, v1 := seedScopedPolicyDraft(t, super, tenantID, "P", 1, scopeSpec{RoleIDs: []string{roleID}})
	seedApprovalPolicyStepInLane(t, super, tenantID, v1, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("tax-reviewer"),
	})
	activateApprovalPolicyVersion(t, super, v1)
	if _, err := super.Exec(context.Background(),
		`UPDATE approval_policy_versions SET is_active = false WHERE id = $1`, v1); err != nil {
		t.Fatalf("deactivate v1: %v", err)
	}
	v2 := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 2)
	activateApprovalPolicyVersion(t, super, v2)

	if _, err := NewStore(app, stubFingerprinter, nil).DeleteRole(c, "tax-reviewer"); err != nil {
		t.Fatalf("DeleteRole err = %v, want success", err)
	}
	assertRoleDeleted(t, super, tenantID, roleID)
}

func TestDeleteRole_DeletesARoleOnlyADeletedPolicyNamed(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-06 deleted policy")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	p, _ := seedActivePolicy(t, super, tenantID, "P", 1, nil, "tax-reviewer")
	seedActivePolicy(t, super, tenantID, "C", 2, nil)
	if _, err := store.DeletePolicy(c, p); err != nil {
		t.Fatalf("DeletePolicy: %v", err)
	}

	if _, err := store.DeleteRole(c, "tax-reviewer"); err != nil {
		t.Fatalf("DeleteRole err = %v, want success", err)
	}
	assertRoleDeleted(t, super, tenantID, roleID)
}

func TestDeleteRole_IgnoresASoftDeletedPolicyWhoseVersionStaysActive(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-06 deleted policy active version")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	p, _ := seedActivePolicy(t, super, tenantID, "P", 1, nil, "tax-reviewer")
	if _, err := super.Exec(context.Background(),
		`UPDATE approval_policies SET deleted_at = now() WHERE id = $1`, p); err != nil {
		t.Fatalf("soft-delete policy: %v", err)
	}

	if _, err := NewStore(app, stubFingerprinter, nil).DeleteRole(c, "tax-reviewer"); err != nil {
		t.Fatalf("DeleteRole err = %v, want success", err)
	}
	assertRoleDeleted(t, super, tenantID, roleID)
}

func TestDeleteRole_IgnoresAKeyOnANonApprovalStep(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-06 notify key")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	_, versionID := seedScopedPolicyDraft(t, super, tenantID, "Notify", 1, scopeSpec{})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "notify", WorkflowRoleKey: ptr("tax-reviewer"),
		NotifyTarget: ptr("ops@example.com"), NotifyChannel: ptr("email"),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	if _, err := NewStore(app, stubFingerprinter, nil).DeleteRole(c, "tax-reviewer"); err != nil {
		t.Fatalf("DeleteRole err = %v, want success", err)
	}
	assertRoleDeleted(t, super, tenantID, roleID)
}

func TestDeleteRole_AlreadyDeletedRoleInForceIsNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-06 deleted in force")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	seedActivePolicy(t, super, tenantID, "P", 1, []string{roleID}, "tax-reviewer")
	softDeleteWorkflowRole(t, super, roleID)
	seeded := roleRow(t, super, roleID)

	if _, err := NewStore(app, stubFingerprinter, nil).DeleteRole(c, "tax-reviewer"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteRole err = %v, want ErrNotFound", err)
	}
	if after := roleRow(t, super, roleID); !after.equal(seeded) {
		t.Errorf("role row = %+v, want unchanged %+v — no re-stamp", after, seeded)
	}
}

func TestDeleteRole_NonAdminGetsForbiddenNotInUse(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-06 non-admin")
	preparer, _ := callerCtx(t, super, tenantID, "preparer", "active")
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	seedActivePolicy(t, super, tenantID, "P", 1, nil, "tax-reviewer")

	_, err := NewStore(app, stubFingerprinter, nil).DeleteRole(preparer, "tax-reviewer")
	if !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("DeleteRole err = %v, want ErrNotPermitted", err)
	}
	status, msg := statusForErr(err)
	if status != http.StatusForbidden || msg != "only an admin can change workflow roles" {
		t.Errorf("statusForErr = %d %q, want 403 admin-only", status, msg)
	}
	assertRoleUntouched(t, super, tenantID, roleID)
}

func TestDeleteRole_AnotherTenantsPolicyDoesNotBlock(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := policyTenant(t, super, "APSC-06 tenant A")
	tenantB := policyTenant(t, super, "APSC-06 tenant B")
	c, _ := activeAdmin(t, super, tenantA)
	roleA := seedWorkflowRole(t, super, tenantA, "tax-reviewer", "Tax Reviewer")
	roleB := seedWorkflowRole(t, super, tenantB, "tax-reviewer", "Tax Reviewer")
	seedActivePolicy(t, super, tenantB, "B policy", 1, []string{roleB}, "tax-reviewer")

	if _, err := NewStore(app, stubFingerprinter, nil).DeleteRole(c, "tax-reviewer"); err != nil {
		t.Fatalf("DeleteRole err = %v, want success", err)
	}
	assertRoleDeleted(t, super, tenantA, roleA)
	if roleRow(t, super, roleB).DeletedAt != nil {
		t.Error("tenant B's role is deleted, want live")
	}
}

func TestDeleteRole_ConcurrentPublishAndDeleteNeverBothSucceed(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app, stubFingerprinter, nil)
	for round := 0; round < 8; round++ {
		tenantID := policyTenant(t, super, "APSC-06 race")
		c, _ := activeAdmin(t, super, tenantID)
		seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
		policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
		seedApprovalDraftNamingRole(t, super, tenantID, policyID, "tax-reviewer")

		var wg sync.WaitGroup
		start := make(chan struct{})
		var pubErr, delErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, pubErr = store.PublishPolicy(c, policyID)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, delErr = store.DeleteRole(c, "tax-reviewer")
		}()
		close(start)
		wg.Wait()

		publishWon := pubErr == nil && errors.Is(delErr, ErrRoleInUse)
		deleteWon := delErr == nil && errors.Is(pubErr, ErrPolicyStepRole)
		if !publishWon && !deleteWon {
			t.Fatalf("round %d: publish err = %v, delete err = %v, want exactly one of (publish ok, delete ErrRoleInUse) or (delete ok, publish ErrPolicyStepRole)", round, pubErr, delErr)
		}

		var dangling int
		if err := super.QueryRow(context.Background(),
			`SELECT count(*)
			   FROM approval_policy_versions v
			   JOIN approval_policy_steps s ON s.version_id = v.id AND s.kind = 'approval'
			   JOIN workflow_roles r ON r.tenant_id = v.tenant_id AND r.key = s.workflow_role_key
			  WHERE v.tenant_id = $1 AND v.is_active AND r.deleted_at IS NOT NULL`, tenantID,
		).Scan(&dangling); err != nil {
			t.Fatalf("read dangling: %v", err)
		}
		if dangling != 0 {
			t.Fatalf("round %d: %d active approval steps name a deleted role", round, dangling)
		}
	}
}

func TestDeleteRole_RefusesWhileTheActiveVersionNamesItUnderADraftThatDropsIt(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-06 active under draft")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	policyID, _ := seedActivePolicy(t, super, tenantID, "P", 1, []string{roleID}, "tax-reviewer")
	seedApprovalPolicyVersionN(t, super, tenantID, policyID, 2)

	_, err := NewStore(app, stubFingerprinter, nil).DeleteRole(c, "tax-reviewer")
	var inUse *roleInUse
	if !errors.As(err, &inUse) {
		t.Fatalf("DeleteRole err = %v, want *roleInUse", err)
	}
	if len(inUse.policies) != 1 || inUse.policies[0] != (PolicyRef{ID: policyID, Name: "P"}) {
		t.Errorf("policies = %+v, want [{%s P}]", inUse.policies, policyID)
	}
	assertRoleUntouched(t, super, tenantID, roleID)
}

func TestDeleteRole_DraftScopeRoleDeletedThenPublishIsRefused(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-06 draft scope then publish")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	policyID, versionID := seedScopedPolicyDraft(t, super, tenantID, "S", 1, scopeSpec{RoleIDs: []string{roleID}})
	_, catchAllVersion := seedActivePolicy(t, super, tenantID, "C", 2, nil)

	if _, err := store.DeleteRole(c, "tax-reviewer"); err != nil {
		t.Fatalf("DeleteRole err = %v, want success", err)
	}
	_, err := store.PublishPolicy(c, policyID)
	if !errors.Is(err, ErrPolicyStepRole) {
		t.Fatalf("PublishPolicy err = %v, want ErrPolicyStepRole", err)
	}
	if status, msg := policyStatusForErr(err); status != http.StatusConflict || !strings.Contains(msg, "“Tax Reviewer”") {
		t.Errorf("policyStatusForErr = %d %q, want 409 naming “Tax Reviewer”", status, msg)
	}
	if v := versionRow(t, super, versionID); v.Sealed || v.IsActive {
		t.Errorf("draft version = %+v, want still (sealed false, is_active false)", v)
	}
	if ids := activeVersionIDs(t, super, tenantID); !reflect.DeepEqual(ids, []string{catchAllVersion}) {
		t.Errorf("active versions = %v, want only the catch-all %s", ids, catchAllVersion)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.published"); n != 0 {
		t.Errorf("approval_policy.published audit rows = %d, want 0", n)
	}
	if got := versionScopeColumns(t, super, versionID).RoleIDs; !reflect.DeepEqual(got, []string{roleID}) {
		t.Errorf("draft scope role ids = %v, want [%s] kept", got, roleID)
	}
	assertRoleDeleted(t, super, tenantID, roleID)
}

func TestPublish_ConcurrentScopeRoleDeleteAndPublishNeverBothSucceed(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app, stubFingerprinter, nil)
	for round := 0; round < 8; round++ {
		tenantID := policyTenant(t, super, "APSC-09 scope race")
		c, _ := activeAdmin(t, super, tenantID)
		roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
		policyID, _ := seedScopedPolicyDraft(t, super, tenantID, "S", 1, scopeSpec{RoleIDs: []string{roleID}})
		seedActivePolicy(t, super, tenantID, "C", 2, nil)

		var wg sync.WaitGroup
		start := make(chan struct{})
		var pubErr, delErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, pubErr = store.PublishPolicy(c, policyID)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, delErr = store.DeleteRole(c, "tax-reviewer")
		}()
		close(start)
		wg.Wait()

		publishWon := pubErr == nil && errors.Is(delErr, ErrRoleInUse)
		deleteWon := delErr == nil && errors.Is(pubErr, ErrPolicyStepRole)
		if !publishWon && !deleteWon {
			t.Fatalf("round %d: publish err = %v, delete err = %v, want exactly one of (publish ok, delete ErrRoleInUse) or (delete ok, publish ErrPolicyStepRole)", round, pubErr, delErr)
		}

		var dangling int
		if err := super.QueryRow(context.Background(),
			`SELECT count(*)
			   FROM approval_policy_versions v
			   JOIN workflow_roles r ON r.id = ANY(v.scope_workflow_role_ids)
			  WHERE v.tenant_id = $1 AND v.is_active AND r.deleted_at IS NOT NULL`, tenantID,
		).Scan(&dangling); err != nil {
			t.Fatalf("read dangling: %v", err)
		}
		if dangling != 0 {
			t.Fatalf("round %d: %d active versions name a deleted role in their scope", round, dangling)
		}
	}
}
