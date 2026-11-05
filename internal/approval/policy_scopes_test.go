package approval

import (
	"context"
	"errors"
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
	seedSealedActiveV1(t, super, tenantID, seedApprovalPolicy(t, super, tenantID, "Catch-all"))

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

	// Stored order c, a, b is neither key order nor seeding order: keys must follow storage.
	rb := seedWorkflowRole(t, super, tenantID, "r-b", "Role B")
	ra := seedWorkflowRole(t, super, tenantID, "r-a", "Role A")
	rc := seedWorkflowRole(t, super, tenantID, "r-c", "Role C")
	softDeleteWorkflowRole(t, super, ra)

	policyID := seedApprovalPolicy(t, super, tenantID, "Roles")
	v := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	setVersionScopes(t, super, v, `scope_workflow_role_ids = ARRAY[$2, $3, $4]::uuid[]`, rc, ra, rb)

	got, err := NewStore(app, stubFingerprinter, nil).GetPolicy(c, policyID)
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if want := []string{"r-c", "r-a", "r-b"}; !reflect.DeepEqual(got.Scopes.WorkflowRoleKeys, want) {
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

// seedCurrencies inserts NGN and USD into nrs_codes; cleanup deletes only the rows it inserted.
func seedCurrencies(t *testing.T, super *pgxpool.Pool) {
	t.Helper()
	var inserted []string
	for _, code := range []string{"NGN", "USD"} {
		tag, err := super.Exec(context.Background(),
			`INSERT INTO nrs_codes (list, code, entries) VALUES ('currencies', $1, '[{}]') ON CONFLICT DO NOTHING`, code)
		if err != nil {
			t.Fatalf("seed nrs_codes currencies/%s: %v", code, err)
		}
		if tag.RowsAffected() == 1 {
			inserted = append(inserted, code)
		}
	}
	t.Cleanup(func() {
		for _, code := range inserted {
			_, _ = super.Exec(context.Background(), `DELETE FROM nrs_codes WHERE list = 'currencies' AND code = $1`, code)
		}
	})
}

// storedScopeColumns is a version's six scope columns as text, the role ids raw.
type storedScopeColumns struct {
	Types, Entities, RoleIDs []string
	Lower, Upper, Currency   *string
}

func readScopeColumns(t *testing.T, super *pgxpool.Pool, where string, arg any) storedScopeColumns {
	t.Helper()
	var s storedScopeColumns
	if err := super.QueryRow(context.Background(),
		`SELECT scope_document_types, scope_entity_ids::text[], scope_workflow_role_ids::text[],
		        scope_amount_lower::text, scope_amount_upper::text, scope_amount_currency
		   FROM approval_policy_versions WHERE `+where, arg,
	).Scan(&s.Types, &s.Entities, &s.RoleIDs, &s.Lower, &s.Upper, &s.Currency); err != nil {
		t.Fatalf("read scope columns where %s: %v", where, err)
	}
	return s
}

func versionScopeColumns(t *testing.T, super *pgxpool.Pool, versionID string) storedScopeColumns {
	return readScopeColumns(t, super, `id = $1`, versionID)
}

func TestCreatePolicy_WritesScopesToVersionOne(t *testing.T) {
	super, app := dbTestPools(t)
	seedCurrencies(t, super)
	tenantID := policyTenant(t, super, "APSC-02-02 create-scopes")
	c, _ := activeAdmin(t, super, tenantID)
	entityID := seedBusinessEntity(t, super, tenantID, "Client A")
	roleID := seedWorkflowRole(t, super, tenantID, "scope-role", "Scope role")
	seedSealedActiveV1(t, super, tenantID, seedApprovalPolicy(t, super, tenantID, "Catch-all"))

	got, err := NewStore(app, stubFingerprinter, nil).CreatePolicy(c, "Scoped", &scopeInput{
		DocumentTypes:    []string{"credit_note", "debit_note"},
		EntityIDs:        []string{entityID},
		WorkflowRoleKeys: []string{"scope-role"},
		AmountBand:       &amountBandInput{Lower: ptr("5000000.00"), Upper: ptr("50000000.00"), Currency: "NGN"},
	})
	if err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}

	stored := readScopeColumns(t, super, `policy_id = $1 AND version = 1`, got.ID)
	want := storedScopeColumns{
		Types: []string{"credit_note", "debit_note"}, Entities: []string{entityID}, RoleIDs: []string{roleID},
		Lower: ptr("5000000.00"), Upper: ptr("50000000.00"), Currency: ptr("NGN"),
	}
	if !reflect.DeepEqual(stored, want) {
		t.Errorf("version 1 columns = %+v, want %+v", stored, want)
	}
	wantWire := Scopes{
		DocumentTypes: []string{"credit_note", "debit_note"}, EntityIDs: []string{entityID},
		WorkflowRoleKeys: []string{"scope-role"},
		AmountBand:       &AmountBand{Lower: ptr("5000000.00"), Upper: ptr("50000000.00"), Currency: "NGN"},
	}
	if !reflect.DeepEqual(got.Scopes, wantWire) {
		t.Errorf("answer scopes = %+v, want %+v", got.Scopes, wantWire)
	}
}

func TestPutDraft_WritesScopesToTheOpenDraft(t *testing.T) {
	super, app := dbTestPools(t)
	seedCurrencies(t, super)
	tenantID := policyTenant(t, super, "APSC-02-02 put-scopes")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedDraftWithSteps(t, super, tenantID, policyID, 1)
	seedSealedActiveV1(t, super, tenantID, seedApprovalPolicy(t, super, tenantID, "Catch-all"))

	got, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, &scopeInput{
		DocumentTypes: []string{"commercial_invoice"},
		AmountBand:    &amountBandInput{Upper: ptr("100.00"), Currency: "USD"},
	}, approvalStep("engagement-partner"))
	if err != nil {
		t.Fatalf("PutDraft: %v", err)
	}

	want := storedScopeColumns{
		Types: []string{"commercial_invoice"}, Entities: []string{}, RoleIDs: []string{},
		Upper: ptr("100.00"), Currency: ptr("USD"),
	}
	if stored := versionScopeColumns(t, super, versionID); !reflect.DeepEqual(stored, want) {
		t.Errorf("draft columns = %+v, want %+v", stored, want)
	}
	wantWire := Scopes{
		DocumentTypes: []string{"commercial_invoice"}, EntityIDs: []string{}, WorkflowRoleKeys: []string{},
		AmountBand: &AmountBand{Upper: ptr("100.00"), Currency: "USD"},
	}
	if !reflect.DeepEqual(got.Scopes, wantWire) {
		t.Errorf("answer scopes = %+v, want %+v", got.Scopes, wantWire)
	}
}

func TestCreatePolicy_RefusesEachBadScopeWithNothingWritten(t *testing.T) {
	super, app := dbTestPools(t)
	seedCurrencies(t, super)
	tenantID := policyTenant(t, super, "APSC-02-02 refuse-create")
	otherTenant := policyTenant(t, super, "APSC-02-02 refuse-create-other")
	c, _ := activeAdmin(t, super, tenantID)
	foreignEntity := seedBusinessEntity(t, super, otherTenant, "Their client")
	seedWorkflowRole(t, super, tenantID, "live-role", "Live")
	deleted := seedWorkflowRole(t, super, tenantID, "gone-role", "Gone")
	softDeleteWorkflowRole(t, super, deleted)

	cases := []struct {
		name string
		in   scopeInput
		want error
	}{
		{"unknown entity", scopeInput{EntityIDs: []string{uuid.NewString()}}, ErrScopeEntity},
		{"another tenant's entity", scopeInput{EntityIDs: []string{foreignEntity}}, ErrScopeEntity},
		{"currency off the list", scopeInput{AmountBand: &amountBandInput{Lower: ptr("1.00"), Currency: "XYZ"}}, ErrScopeCurrency},
		{"unknown role key", scopeInput{WorkflowRoleKeys: []string{"live-role", "no-such-role"}}, ErrScopeRole},
		{"soft-deleted role key", scopeInput{WorkflowRoleKeys: []string{"gone-role"}}, ErrScopeRole},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			_, err := NewStore(app, stubFingerprinter, nil).CreatePolicy(c, "Refused", &in)
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if n := rowCount(t, super, "approval_policies", tenantID); n != 0 {
				t.Errorf("approval_policies rows = %d, want 0", n)
			}
			if n := auditCount(t, super, tenantID, "approval_policy.created"); n != 0 {
				t.Errorf("approval_policy.created audit rows = %d, want 0", n)
			}
		})
	}
}

func TestPutDraft_NonAdminRefusedBeforeScopeLookup(t *testing.T) {
	super, _ := dbTestPools(t)
	seedCurrencies(t, super)
	tenantID := policyTenant(t, super, "APSC-02-02 nonadmin-scopes")
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	seedDraftWithSteps(t, super, tenantID, policyID, 1)
	c, _ := callerCtx(t, super, tenantID, "preparer", "active")

	traced, rec := tracedAppPool(t)
	_, err := NewStore(traced, stubFingerprinter, nil).PutDraft(c, policyID, nil, &scopeInput{
		AmountBand: &amountBandInput{Lower: ptr("1.00"), Currency: "XYZ"},
	}, approvalStep("engagement-partner"))
	if !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("err = %v, want ErrNotPermitted", err)
	}
	if got := rec.mentioning("nrs_codes"); len(got) != 0 {
		t.Errorf("a refused non-admin ran %d nrs_codes statements: %v", len(got), got)
	}
}

func TestPutDraft_OmittedScopesKeepTheDraftsScopes(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-02 omitted-keep")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedDraftWithSteps(t, super, tenantID, policyID, 2)
	setVersionScopes(t, super, versionID, `scope_document_types = '{credit_note}'`)
	seedSealedActiveV1(t, super, tenantID, seedApprovalPolicy(t, super, tenantID, "Catch-all"))

	got, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, nil, approvalStep("engagement-partner"))
	if err != nil {
		t.Fatalf("PutDraft: %v", err)
	}
	if want := []string{"credit_note"}; !reflect.DeepEqual(got.Scopes.DocumentTypes, want) {
		t.Errorf("answer document_types = %v, want %v", got.Scopes.DocumentTypes, want)
	}
	if stored := versionScopeColumns(t, super, versionID); !reflect.DeepEqual(stored.Types, []string{"credit_note"}) {
		t.Errorf("stored document_types = %v, want [credit_note]", stored.Types)
	}
	if steps := readStoredSteps(t, super, versionID); len(steps) != 1 || steps[0].Kind != "approval" {
		t.Errorf("draft steps = %v, want the one new approval step", steps)
	}
}

func TestPutDraft_ForkCopiesThePublishedVersionsScopes(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-02 fork-copies")
	c, _ := activeAdmin(t, super, tenantID)
	entityID := seedBusinessEntity(t, super, tenantID, "Client A")
	roleID := seedWorkflowRole(t, super, tenantID, "fork-role", "Fork role")
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	v1 := seedSealedActiveV1Scoped(t, super, tenantID, policyID, entityID, roleID)
	before := versionScopeColumns(t, super, v1)
	seedSealedActiveV1(t, super, tenantID, seedApprovalPolicy(t, super, tenantID, "Catch-all"))

	got, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, nil, approvalStep("engagement-partner"))
	if err != nil {
		t.Fatalf("PutDraft: %v", err)
	}
	if got.Version != 2 || got.Status != "draft" {
		t.Fatalf("answer = version %d status %q, want a v2 draft", got.Version, got.Status)
	}
	draft := openDraft(t, super, policyID)
	if after := versionScopeColumns(t, super, draft.ID); !reflect.DeepEqual(after, before) {
		t.Errorf("fork columns = %+v, want v1's %+v", after, before)
	}
	if after := versionScopeColumns(t, super, v1); !reflect.DeepEqual(after, before) {
		t.Errorf("v1 columns changed to %+v from %+v", after, before)
	}
	if before.Types == nil || len(before.Types) != 1 || before.Currency == nil || len(before.Entities) != 1 || len(before.RoleIDs) != 1 {
		t.Fatalf("fixture v1 is not fully scoped: %+v — the copy assertions are vacuous", before)
	}
}

// A fork copies the highest version's scopes, not an older sealed version's.
func TestPutDraft_ForkCopiesTheTopVersionsScopesNotTheOldest(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-02 fork-top")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	v1 := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	setVersionScopes(t, super, v1, `scope_document_types = '{credit_note}'`)
	if _, err := super.Exec(context.Background(),
		`UPDATE approval_policy_versions SET sealed = true, published_at = now(), published_by = 'fixture' WHERE id = $1`, v1); err != nil {
		t.Fatalf("seal v1: %v", err)
	}
	v2 := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 2)
	setVersionScopes(t, super, v2, `scope_document_types = '{debit_note}'`)
	activateApprovalPolicyVersion(t, super, v2)
	seedSealedActiveV1(t, super, tenantID, seedApprovalPolicy(t, super, tenantID, "Catch-all"))

	got, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, nil, approvalStep("engagement-partner"))
	if err != nil {
		t.Fatalf("PutDraft: %v", err)
	}
	if got.Version != 3 {
		t.Fatalf("answer version = %d, want a v3 fork", got.Version)
	}
	want := []string{"debit_note"}
	if got.Scopes.DocumentTypes == nil || !reflect.DeepEqual(got.Scopes.DocumentTypes, want) {
		t.Errorf("fork answer document_types = %v, want v2's %v", got.Scopes.DocumentTypes, want)
	}
	if after := versionScopeColumns(t, super, openDraft(t, super, policyID).ID); !reflect.DeepEqual(after.Types, want) {
		t.Errorf("fork stored document types = %v, want v2's %v", after.Types, want)
	}
}

// seedSealedActiveV1Scoped is a sealed, active v1 with all four scopes set.
func seedSealedActiveV1Scoped(t *testing.T, super *pgxpool.Pool, tenantID, policyID, entityID, roleID string) string {
	t.Helper()
	v := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	setVersionScopes(t, super, v,
		`scope_document_types = '{credit_note}', scope_entity_ids = ARRAY[$2]::uuid[],
		 scope_workflow_role_ids = ARRAY[$3]::uuid[], scope_amount_lower = 1, scope_amount_upper = 10,
		 scope_amount_currency = 'NGN'`, entityID, roleID)
	activateApprovalPolicyVersion(t, super, v)
	return v
}

func TestPutDraft_EmptyScopesObjectClearsToCatchAll(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-02 empty-clears")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Sign-off")
	versionID := seedDraftWithSteps(t, super, tenantID, policyID, 1)
	setVersionScopes(t, super, versionID,
		`scope_document_types = '{credit_note}', scope_amount_lower = 1, scope_amount_currency = 'NGN'`)

	if _, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, &scopeInput{}, approvalStep("engagement-partner")); err != nil {
		t.Fatalf("PutDraft: %v", err)
	}
	want := storedScopeColumns{Types: []string{}, Entities: []string{}, RoleIDs: []string{}}
	if stored := versionScopeColumns(t, super, versionID); !reflect.DeepEqual(stored, want) {
		t.Errorf("columns = %+v, want the catch-all %+v", stored, want)
	}
}

func TestPutDraft_RefusedScopeRollsBackTheWholeWrite(t *testing.T) {
	super, app := dbTestPools(t)
	seedCurrencies(t, super)
	tenantID := policyTenant(t, super, "APSC-02-02 refused-rolls-back")
	c, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "A")
	versionID := seedDraftWithSteps(t, super, tenantID, policyID, 1)
	setVersionScopes(t, super, versionID, `scope_document_types = '{credit_note}'`)
	stepsBefore := readStoredSteps(t, super, versionID)
	scopesBefore := versionScopeColumns(t, super, versionID)
	auditBefore := auditCount(t, super, tenantID, "approval_policy.updated")

	_, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, ptr("B"), &scopeInput{
		AmountBand: &amountBandInput{Lower: ptr("1.00"), Currency: "XYZ"},
	}, []stepInput{})
	if !errors.Is(err, ErrScopeCurrency) {
		t.Fatalf("err = %v, want ErrScopeCurrency", err)
	}
	if name := storedPolicyName(t, super, policyID); name != "A" {
		t.Errorf("name = %q, want A", name)
	}
	if !reflect.DeepEqual(readStoredSteps(t, super, versionID), stepsBefore) {
		t.Error("steps changed on a refused write")
	}
	if after := versionScopeColumns(t, super, versionID); !reflect.DeepEqual(after, scopesBefore) {
		t.Errorf("scopes = %+v, want %+v", after, scopesBefore)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.updated"); n != auditBefore {
		t.Errorf("updated audit rows = %d, want %d", n, auditBefore)
	}
}

func TestCreatePolicy_LegacyScopeColumnKeepsItsDefault(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-02-02 legacy-scope")
	c, _ := activeAdmin(t, super, tenantID)
	seedSealedActiveV1(t, super, tenantID, seedApprovalPolicy(t, super, tenantID, "Catch-all"))

	got, err := NewStore(app, stubFingerprinter, nil).CreatePolicy(c, "Sign-off",
		&scopeInput{DocumentTypes: []string{"credit_note"}})
	if err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}
	if stored := storedPolicyScope(t, super, got.ID); stored != scopeAllInvoices {
		t.Errorf("stored scope column = %q, want %q", stored, scopeAllInvoices)
	}
	if got.Scope != scopeAllInvoices {
		t.Errorf("answer scope = %q, want %q", got.Scope, scopeAllInvoices)
	}
}

func TestPutDraft_KeepsADeletedRoleTheDraftAlreadyHolds(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-04-01 keep-draft")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "kept-role", "Kept role")
	policyID := seedApprovalPolicy(t, super, tenantID, "P")
	versionID := seedDraftWithSteps(t, super, tenantID, policyID, 1)
	setVersionScopes(t, super, versionID, `scope_workflow_role_ids = ARRAY[$2]::uuid[]`, roleID)
	seedSealedActiveV1(t, super, tenantID, seedApprovalPolicy(t, super, tenantID, "Catch-all"))
	softDeleteWorkflowRole(t, super, roleID)

	got, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, &scopeInput{
		DocumentTypes: []string{"credit_note"}, WorkflowRoleKeys: []string{"kept-role"},
	}, approvalStep("engagement-partner"))
	if err != nil {
		t.Fatalf("PutDraft: %v", err)
	}
	stored := versionScopeColumns(t, super, versionID)
	if !reflect.DeepEqual(stored.RoleIDs, []string{roleID}) || !reflect.DeepEqual(stored.Types, []string{"credit_note"}) {
		t.Errorf("draft columns = %+v, want role [%s] and types [credit_note]", stored, roleID)
	}
	if want := []string{"kept-role"}; !reflect.DeepEqual(got.Scopes.WorkflowRoleKeys, want) {
		t.Errorf("answer workflow_role_keys = %v, want %v", got.Scopes.WorkflowRoleKeys, want)
	}
}

func TestPutDraft_ForkKeepsADeletedRoleTheSealedVersionHolds(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-04-01 keep-fork")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "sealed-role", "Sealed role")
	policyID := seedApprovalPolicy(t, super, tenantID, "P")
	seedSealedActiveV1Scoped(t, super, tenantID, policyID, seedBusinessEntity(t, super, tenantID, "Client A"), roleID)
	seedSealedActiveV1(t, super, tenantID, seedApprovalPolicy(t, super, tenantID, "Catch-all"))
	softDeleteWorkflowRole(t, super, roleID)

	got, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, &scopeInput{
		WorkflowRoleKeys: []string{"sealed-role"}, DocumentTypes: []string{"debit_note"},
	}, approvalStep("engagement-partner"))
	if err != nil {
		t.Fatalf("PutDraft: %v", err)
	}
	if got.Version != 2 || got.Status != "draft" {
		t.Fatalf("answer = version %d status %q, want a v2 draft", got.Version, got.Status)
	}
	if stored := versionScopeColumns(t, super, openDraft(t, super, policyID).ID); !reflect.DeepEqual(stored.RoleIDs, []string{roleID}) {
		t.Errorf("fork role ids = %v, want [%s]", stored.RoleIDs, roleID)
	}
}

func TestPutDraft_RefusesANewlyAddedDeletedRole(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-04-01 refuse-new")
	c, _ := activeAdmin(t, super, tenantID)
	live := seedWorkflowRole(t, super, tenantID, "live-role", "Live role")
	gone := seedWorkflowRole(t, super, tenantID, "gone-role", "Gone role")
	softDeleteWorkflowRole(t, super, gone)
	policyID := seedApprovalPolicy(t, super, tenantID, "P")
	versionID := seedDraftWithSteps(t, super, tenantID, policyID, 1)
	setVersionScopes(t, super, versionID, `scope_workflow_role_ids = ARRAY[$2]::uuid[]`, live)
	auditBefore := auditCount(t, super, tenantID, "approval_policy.updated")

	_, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, &scopeInput{
		WorkflowRoleKeys: []string{"live-role", "gone-role"},
	}, approvalStep("engagement-partner"))
	if !errors.Is(err, ErrScopeRole) {
		t.Fatalf("err = %v, want ErrScopeRole", err)
	}
	if stored := versionScopeColumns(t, super, versionID); !reflect.DeepEqual(stored.RoleIDs, []string{live}) {
		t.Errorf("draft role ids = %v, want [%s]", stored.RoleIDs, live)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.updated"); n != auditBefore {
		t.Errorf("updated audit rows = %d, want %d", n, auditBefore)
	}
}

func TestPutDraft_RefusesADeletedRoleAnotherPolicyHolds(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-04-01 other-policy")
	c, _ := activeAdmin(t, super, tenantID)
	gone := seedWorkflowRole(t, super, tenantID, "gone-role", "Gone role")
	q := seedApprovalPolicy(t, super, tenantID, "Q")
	setVersionScopes(t, super, seedDraftWithSteps(t, super, tenantID, q, 1), `scope_workflow_role_ids = ARRAY[$2]::uuid[]`, gone)
	softDeleteWorkflowRole(t, super, gone)
	p := seedApprovalPolicy(t, super, tenantID, "P")
	versionID := seedDraftWithSteps(t, super, tenantID, p, 1)

	_, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, p, nil, &scopeInput{
		WorkflowRoleKeys: []string{"gone-role"},
	}, approvalStep("engagement-partner"))
	if !errors.Is(err, ErrScopeRole) {
		t.Fatalf("err = %v, want ErrScopeRole", err)
	}
	if stored := versionScopeColumns(t, super, versionID); len(stored.RoleIDs) != 0 {
		t.Errorf("P role ids = %v, want none", stored.RoleIDs)
	}
}

func TestPutDraft_RefusesAnUnknownRoleKeyEvenWithAStoredDeletedRole(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-04-01 unknown-key")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "kept-role", "Kept role")
	policyID := seedApprovalPolicy(t, super, tenantID, "P")
	versionID := seedDraftWithSteps(t, super, tenantID, policyID, 1)
	setVersionScopes(t, super, versionID, `scope_workflow_role_ids = ARRAY[$2]::uuid[]`, roleID)
	softDeleteWorkflowRole(t, super, roleID)
	auditBefore := auditCount(t, super, tenantID, "approval_policy.updated")

	_, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, &scopeInput{
		WorkflowRoleKeys: []string{"kept-role", "no-such-role"}, DocumentTypes: []string{"credit_note"},
	}, approvalStep("engagement-partner"))
	if !errors.Is(err, ErrScopeRole) {
		t.Fatalf("err = %v, want ErrScopeRole", err)
	}
	stored := versionScopeColumns(t, super, versionID)
	if !reflect.DeepEqual(stored.RoleIDs, []string{roleID}) || len(stored.Types) != 0 {
		t.Errorf("draft columns = %+v, want unchanged", stored)
	}
	if n := auditCount(t, super, tenantID, "approval_policy.updated"); n != auditBefore {
		t.Errorf("updated audit rows = %d, want %d", n, auditBefore)
	}
}
