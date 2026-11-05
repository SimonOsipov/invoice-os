package approval

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// setVersionScopes writes scope columns as the superuser; the version must be unsealed.
func setVersionScopes(t *testing.T, super *pgxpool.Pool, versionID, sql string, args ...any) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE approval_policy_versions SET `+sql+` WHERE id = $1`, append([]any{versionID}, args...)...)
	if err != nil {
		t.Fatalf("set scopes on %s: %v", versionID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("set scopes on %s affected %d rows, want 1", versionID, tag.RowsAffected())
	}
}

func TestPolicy_ListAndGetReadTheTopVersionsScopes(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02 top-version-scopes")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app, stubFingerprinter, nil)

	policyID := seedApprovalPolicy(t, super, tenantID, "Scoped")
	v1 := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	setVersionScopes(t, super, v1, `scope_document_types = '{credit_note}'`)
	sealApprovalPolicyVersion(t, super, v1)
	v2 := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 2)
	entityID := uuid.NewString()
	roleID := seedWorkflowRole(t, super, tenantID, "scope-role", "Scope role")
	setVersionScopes(t, super, v2,
		`scope_entity_ids = ARRAY[$2]::uuid[], scope_workflow_role_ids = ARRAY[$3]::uuid[],
		 scope_amount_lower = 5000000, scope_amount_currency = 'NGN'`, entityID, roleID)

	want := Scopes{
		DocumentTypes:    []string{},
		EntityIDs:        []string{entityID},
		WorkflowRoleKeys: []string{"scope-role"},
		AmountBand:       &AmountBand{Lower: ptr("5000000.00"), Currency: "NGN"},
	}
	var wantPosition int
	if err := super.QueryRow(context.Background(),
		`SELECT position FROM approval_policies WHERE id = $1`, policyID).Scan(&wantPosition); err != nil {
		t.Fatalf("read position: %v", err)
	}

	list, err := store.ListPolicies(c)
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	got, err := store.GetPolicy(c, policyID)
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListPolicies returned %d policies, want 1", len(list))
	}
	for name, p := range map[string]Policy{"list": list[0], "get": got} {
		if !reflect.DeepEqual(p.Scopes, want) {
			t.Errorf("%s scopes = %+v, want the open v2's %+v", name, p.Scopes, want)
		}
		if p.Position != wantPosition {
			t.Errorf("%s position = %d, want %d", name, p.Position, wantPosition)
		}
	}
}

func TestPublish_AnswersTheSealedVersionsScopesAndPosition(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02 publish-scopes")
	c, _ := activeAdmin(t, super, tenantID)

	seedApprovalPolicy(t, super, tenantID, "First")
	policyID := seedApprovalPolicy(t, super, tenantID, "Second")
	v := seedApprovalDraft(t, super, tenantID, policyID)
	setVersionScopes(t, super, v, `scope_document_types = '{debit_note}'`)

	got, err := NewStore(app, stubFingerprinter, nil).PublishPolicy(c, policyID)
	if err != nil {
		t.Fatalf("PublishPolicy: %v", err)
	}
	if want := []string{"debit_note"}; !reflect.DeepEqual(got.Scopes.DocumentTypes, want) {
		t.Errorf("scopes.document_types = %v, want %v", got.Scopes.DocumentTypes, want)
	}
	if got.Position != 2 {
		t.Errorf("position = %d, want 2", got.Position)
	}
}

func TestPolicy_BandBoundsKeepTheirScale(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02 band-scale")
	c, _ := activeAdmin(t, super, tenantID)

	policyID := seedApprovalPolicy(t, super, tenantID, "Banded")
	v := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	setVersionScopes(t, super, v,
		`scope_amount_lower = 0, scope_amount_upper = 10, scope_amount_currency = 'NGN'`)

	got, err := NewStore(app, stubFingerprinter, nil).GetPolicy(c, policyID)
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	b := got.Scopes.AmountBand
	if b == nil || b.Lower == nil || b.Upper == nil || *b.Lower != "0.00" || *b.Upper != "10.00" {
		t.Fatalf("amount_band = %+v, want lower 0.00 upper 10.00", b)
	}
}

func TestPolicy_ScopesReadADeletedRolesKey(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02 deleted-role-key")
	c, _ := activeAdmin(t, super, tenantID)

	// Stored order is b, a ids-wise unrelated to key order: key order must follow storage.
	rb := seedWorkflowRole(t, super, tenantID, "r-b", "Role B")
	ra := seedWorkflowRole(t, super, tenantID, "r-a", "Role A")
	_ = rb
	softDeleteWorkflowRole(t, super, ra)

	policyID := seedApprovalPolicy(t, super, tenantID, "Roles")
	v := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	setVersionScopes(t, super, v, `scope_workflow_role_ids = ARRAY[$2, $3]::uuid[]`, ra, rb)

	got, err := NewStore(app, stubFingerprinter, nil).GetPolicy(c, policyID)
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if want := []string{"r-a", "r-b"}; !reflect.DeepEqual(got.Scopes.WorkflowRoleKeys, want) {
		t.Errorf("workflow_role_keys = %v, want %v", got.Scopes.WorkflowRoleKeys, want)
	}
}

func TestPolicy_ListReadsScopesInOneStatement(t *testing.T) {
	super, _ := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02 one-scope-read")
	c, _ := activeAdmin(t, super, tenantID)
	for i := 0; i < 5; i++ {
		id := seedApprovalPolicy(t, super, tenantID, "P"+string(rune('A'+i)))
		seedApprovalPolicyVersionN(t, super, tenantID, id, 1)
	}

	traced, rec := tracedAppPool(t)
	rec.reset()
	if _, err := NewStore(traced, stubFingerprinter, nil).ListPolicies(c); err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	if sql := rec.mentioning("scope_document_types"); len(sql) != 1 {
		t.Errorf("%d statements mention scope_document_types, want 1:\n%v", len(sql), sql)
	}
}
