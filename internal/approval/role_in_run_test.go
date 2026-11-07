package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

type runStepSeed struct {
	Ord         int
	Kind, State string
	Key         *string
}

// seedOpenRun seeds a validated invoice with an open run on versionID and the given steps.
func seedOpenRun(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number, versionID string, steps ...runStepSeed) (invoiceID, runID string) {
	t.Helper()
	invoiceID = seedInvoice(t, super, tenantID, entityID, number)
	setInvoiceStatus(t, super, invoiceID, "validated")
	runID = seedApprovalRun(t, super, tenantID, invoiceID, versionID)
	for _, s := range steps {
		if _, err := super.Exec(context.Background(),
			`INSERT INTO approval_run_steps (tenant_id, run_id, ord, kind, state, workflow_role_key)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			tenantID, runID, s.Ord, s.Kind, s.State, s.Key); err != nil {
			t.Fatalf("seed approval_run_steps: %v", err)
		}
	}
	return invoiceID, runID
}

// pendingOn is the one-step shape most rows need: a pending approval step keyed key.
func pendingOn(key string) runStepSeed {
	return runStepSeed{Ord: 0, Kind: "approval", State: "pending", Key: ptr(key)}
}

// runFixture is a tenant with an admin, an entity and one policy version no run needs to be active.
type runFixture struct {
	super, app *pgxpool.Pool
	tenantID   string
	entityID   string
	versionID  string
	admin      context.Context
	store      *Store
}

func newRunFixture(t *testing.T, name string) runFixture {
	t.Helper()
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, name)
	admin, _ := activeAdmin(t, super, tenantID)
	policyID := seedApprovalPolicy(t, super, tenantID, "Run policy")
	return runFixture{
		super: super, app: app, tenantID: tenantID, admin: admin,
		entityID:  seedBusinessEntity(t, super, tenantID, "Run Corp"),
		versionID: seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1),
		store:     NewStore(app, stubFingerprinter, nil),
	}
}

func (f runFixture) wantInUse(t *testing.T, key string) *roleInUse {
	t.Helper()
	_, err := f.store.DeleteRole(f.admin, key)
	if !errors.Is(err, ErrRoleInUse) {
		t.Fatalf("DeleteRole err = %v, want ErrRoleInUse", err)
	}
	var inUse *roleInUse
	if !errors.As(err, &inUse) {
		t.Fatalf("DeleteRole err = %v, want *roleInUse", err)
	}
	return inUse
}

func TestDeleteRole_RefusesARoleOnlyAnOpenRunWaitsOn(t *testing.T) {
	f := newRunFixture(t, "APSC-10 run refuses")
	roleID := seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("tax-reviewer"))

	inUse := f.wantInUse(t, "tax-reviewer")
	if len(inUse.policies) != 0 || len(inUse.invoices) != 1 {
		t.Errorf("policies = %+v, invoices = %+v, want none and one", inUse.policies, inUse.invoices)
	}
	assertRoleUntouched(t, f.super, f.tenantID, roleID)
}

func TestDeleteRole_RefusesWhenTheNamingStepIsNotYetReached(t *testing.T) {
	f := newRunFixture(t, "APSC-10 not yet reached")
	roleID := seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID,
		pendingOn("other"),
		runStepSeed{Ord: 1, Kind: "approval", State: "pending", Key: ptr("tax-reviewer")})

	f.wantInUse(t, "tax-reviewer")
	assertRoleUntouched(t, f.super, f.tenantID, roleID)
}

func TestDeleteRole_RefusesWhenTheCurrentStepIsAnotherRoleAndRIsLater(t *testing.T) {
	f := newRunFixture(t, "APSC-10 current is other")
	roleID := seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID,
		runStepSeed{Ord: 0, Kind: "approval", State: "satisfied", Key: ptr("other")},
		runStepSeed{Ord: 1, Kind: "approval", State: "pending", Key: ptr("tax-reviewer")})

	f.wantInUse(t, "tax-reviewer")
	assertRoleUntouched(t, f.super, f.tenantID, roleID)
}

func TestDeleteRole_RefusalListsEachWaitingInvoiceOnceOldestFirst(t *testing.T) {
	f := newRunFixture(t, "APSC-10 list order")
	seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	a, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-A", f.versionID,
		pendingOn("tax-reviewer"),
		runStepSeed{Ord: 1, Kind: "approval", State: "pending", Key: ptr("tax-reviewer")})
	b, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-B", f.versionID, pendingOn("tax-reviewer"))
	seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-C", f.versionID, pendingOn("other"))

	inUse := f.wantInUse(t, "tax-reviewer")
	want := []InvoiceRef{{ID: a, Number: "INV-A"}, {ID: b, Number: "INV-B"}}
	if len(inUse.invoices) != 2 || inUse.invoices[0] != want[0] || inUse.invoices[1] != want[1] {
		t.Errorf("invoices = %+v, want %+v", inUse.invoices, want)
	}
	if inUse.invoicesTotal != 2 {
		t.Errorf("invoicesTotal = %d, want 2", inUse.invoicesTotal)
	}
}

func seedWaitingRuns(t *testing.T, f runFixture, n int) (firstInvoice string) {
	t.Helper()
	for i := 0; i < n; i++ {
		id, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, fmt.Sprintf("INV-%02d", i), f.versionID, pendingOn("tax-reviewer"))
		if i == 0 {
			firstInvoice = id
		}
	}
	return firstInvoice
}

func TestDeleteRole_RefusalCapsTheInvoiceListAtTwentyAndCountsAll(t *testing.T) {
	f := newRunFixture(t, "APSC-10 cap 21")
	seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	first := seedWaitingRuns(t, f, 21)

	inUse := f.wantInUse(t, "tax-reviewer")
	if len(inUse.invoices) != 20 || inUse.invoicesTotal != 21 {
		t.Errorf("len(invoices) = %d, total = %d, want 20 and 21", len(inUse.invoices), inUse.invoicesTotal)
	}
	if inUse.invoices[0].ID != first {
		t.Errorf("first invoice = %s, want the oldest run's %s", inUse.invoices[0].ID, first)
	}
}

func TestDeleteRole_RefusalAtExactlyTwentyListsAllTwentyAndTotalsTwenty(t *testing.T) {
	f := newRunFixture(t, "APSC-10 cap 20")
	seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	seedWaitingRuns(t, f, 20)

	inUse := f.wantInUse(t, "tax-reviewer")
	if len(inUse.invoices) != 20 || inUse.invoicesTotal != 20 {
		t.Errorf("len(invoices) = %d, total = %d, want 20 and 20", len(inUse.invoices), inUse.invoicesTotal)
	}
}

func TestDeleteRole_DeletesOnceTheRunIsApproved(t *testing.T) {
	f := newRunFixture(t, "APSC-10 approved")
	roleID := seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	_, runID := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID,
		runStepSeed{Ord: 0, Kind: "approval", State: "satisfied", Key: ptr("tax-reviewer")},
		runStepSeed{Ord: 1, Kind: "approval", State: "pending", Key: ptr("tax-reviewer")}) // run state alone closes it
	closeApprovalRunFor(t, f.super, runID, "approved", "someone")

	if _, err := f.store.DeleteRole(f.admin, "tax-reviewer"); err != nil {
		t.Fatalf("DeleteRole err = %v, want success", err)
	}
	assertRoleDeleted(t, f.super, f.tenantID, roleID)
}

func TestDeleteRole_DeletesOnceTheRunIsRejectedEvenWithLaterStepsPending(t *testing.T) {
	f := newRunFixture(t, "APSC-10 rejected")
	roleID := seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	_, runID := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID,
		runStepSeed{Ord: 0, Kind: "approval", State: "rejected", Key: ptr("other")},
		runStepSeed{Ord: 1, Kind: "approval", State: "pending", Key: ptr("tax-reviewer")})
	closeApprovalRunFor(t, f.super, runID, "rejected", "someone")

	if _, err := f.store.DeleteRole(f.admin, "tax-reviewer"); err != nil {
		t.Fatalf("DeleteRole err = %v, want success", err)
	}
	assertRoleDeleted(t, f.super, f.tenantID, roleID)
}

func TestDeleteRole_RefusesThenDeletesOnceAnEditCancelsTheRun(t *testing.T) {
	f := newRunFixture(t, "APSC-10 edit cancels")
	roleID := seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	invoiceID, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("tax-reviewer"))

	f.wantInUse(t, "tax-reviewer")
	if cancelled, err := cancel(t, f.app, f.tenantID, invoiceID, "editor"); err != nil || !cancelled {
		t.Fatalf("cancel = %v, %v, want true, nil", cancelled, err)
	}
	if _, err := f.store.DeleteRole(f.admin, "tax-reviewer"); err != nil {
		t.Fatalf("DeleteRole err = %v, want success", err)
	}
	assertRoleDeleted(t, f.super, f.tenantID, roleID)
}

func TestDeleteRole_IgnoresStepsThatCannotNeedTheRole(t *testing.T) {
	f := newRunFixture(t, "APSC-10 ignored steps")
	roleID := seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID,
		pendingOn("other"),
		runStepSeed{Ord: 1, Kind: "approval", State: "satisfied", Key: ptr("tax-reviewer")},
		runStepSeed{Ord: 2, Kind: "approval", State: "skipped", Key: ptr("tax-reviewer")},
		runStepSeed{Ord: 3, Kind: "notify", State: "pending", Key: ptr("tax-reviewer")})

	if _, err := f.store.DeleteRole(f.admin, "tax-reviewer"); err != nil {
		t.Fatalf("DeleteRole err = %v, want success", err)
	}
	assertRoleDeleted(t, f.super, f.tenantID, roleID)
}

func TestDeleteRole_IgnoresARunOnAnInvoiceThatLeftValidated(t *testing.T) {
	for _, status := range []string{"queued", "submitted", "rejected"} {
		t.Run(status, func(t *testing.T) {
			f := newRunFixture(t, "APSC-10 left validated "+status)
			roleID := seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
			invoiceID, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("tax-reviewer"))
			setInvoiceStatus(t, f.super, invoiceID, status)

			if _, err := f.store.DeleteRole(f.admin, "tax-reviewer"); err != nil {
				t.Fatalf("DeleteRole err = %v, want success", err)
			}
			assertRoleDeleted(t, f.super, f.tenantID, roleID)
		})
	}
}

func TestDeleteRole_ListsOnlyTheValidatedInvoiceAmongRuns(t *testing.T) {
	f := newRunFixture(t, "APSC-10 only validated")
	seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	a, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-A", f.versionID, pendingOn("tax-reviewer"))
	b, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-B", f.versionID, pendingOn("tax-reviewer"))
	setInvoiceStatus(t, f.super, b, "queued")

	inUse := f.wantInUse(t, "tax-reviewer")
	if len(inUse.invoices) != 1 || inUse.invoices[0] != (InvoiceRef{ID: a, Number: "INV-A"}) || inUse.invoicesTotal != 1 {
		t.Errorf("invoices = %+v, total = %d, want [A] and 1", inUse.invoices, inUse.invoicesTotal)
	}
}

func TestDeleteRole_RefusalCarriesPoliciesAndInvoicesTogether(t *testing.T) {
	f := newRunFixture(t, "APSC-10 both")
	seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	policyID, _ := seedActivePolicy(t, f.super, f.tenantID, "Sign-off", 2, nil, "tax-reviewer")
	inv, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("tax-reviewer"))

	inUse := f.wantInUse(t, "tax-reviewer")
	if len(inUse.policies) != 1 || inUse.policies[0] != (PolicyRef{ID: policyID, Name: "Sign-off"}) {
		t.Errorf("policies = %+v, want [Sign-off]", inUse.policies)
	}
	if len(inUse.invoices) != 1 || inUse.invoices[0] != (InvoiceRef{ID: inv, Number: "INV-1"}) {
		t.Errorf("invoices = %+v, want [INV-1]", inUse.invoices)
	}
	if got, want := inUse.message(), roleInUseMessage+"; "+roleInRunMessage; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

func TestRoleInUseMessage_ByCase(t *testing.T) {
	pol := []PolicyRef{{ID: "p1", Name: "A"}}
	inv := []InvoiceRef{{ID: "i1", Number: "INV-1"}}
	cases := []struct {
		name string
		e    roleInUse
		want string
	}{
		{"policies", roleInUse{policies: pol}, roleInUseMessage},
		{"invoices", roleInUse{invoices: inv, invoicesTotal: 1}, roleInRunMessage},
		{"both", roleInUse{policies: pol, invoices: inv, invoicesTotal: 1}, roleInUseMessage + "; " + roleInRunMessage},
		{"empty", roleInUse{}, roleInUseMessage},
		{"last holder, invoices only", roleInUse{lastHolder: true, invoices: inv, invoicesTotal: 1}, roleLastHolderMessage},
		{"last holder, policies set", roleInUse{lastHolder: true, policies: pol, invoices: inv, invoicesTotal: 1}, roleLastHolderMessage},
		{"last signer, invoices", roleInUse{lastSigner: true, invoices: inv, invoicesTotal: 1}, memberLastSignerMessage},
		{"last signer beats last holder", roleInUse{lastSigner: true, lastHolder: true, invoices: inv, invoicesTotal: 1}, memberLastSignerMessage},
	}
	for _, c := range cases {
		if got := c.e.message(); got != c.want {
			t.Errorf("%s: message = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDeleteRole_AnotherTenantsOpenRunDoesNotBlock(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := policyTenant(t, super, "APSC-10 tenant A")
	tenantB := policyTenant(t, super, "APSC-10 tenant B")
	adminA, _ := activeAdmin(t, super, tenantA)
	roleA := seedWorkflowRole(t, super, tenantA, "tax-reviewer", "Tax Reviewer")
	roleB := seedWorkflowRole(t, super, tenantB, "tax-reviewer", "Tax Reviewer")
	policyB := seedApprovalPolicy(t, super, tenantB, "B policy")
	versionB := seedApprovalPolicyVersionN(t, super, tenantB, policyB, 1)
	entityB := seedBusinessEntity(t, super, tenantB, "B Corp")
	seedOpenRun(t, super, tenantB, entityB, "INV-B", versionB, pendingOn("tax-reviewer"))

	if _, err := NewStore(app, stubFingerprinter, nil).DeleteRole(adminA, "tax-reviewer"); err != nil {
		t.Fatalf("DeleteRole err = %v, want success", err)
	}
	if roleRow(t, super, roleA).DeletedAt == nil {
		t.Error("tenant A's role deleted_at is NULL, want set")
	}
	if roleRow(t, super, roleB).DeletedAt != nil {
		t.Error("tenant B's role was deleted")
	}
}

func TestDeleteRole_AlreadyDeletedRoleWithAnOpenRunIsNotFound(t *testing.T) {
	f := newRunFixture(t, "APSC-10 deleted role")
	roleID := seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	softDeleteWorkflowRole(t, f.super, roleID)
	before := roleRow(t, f.super, roleID)
	seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("tax-reviewer"))

	if _, err := f.store.DeleteRole(f.admin, "tax-reviewer"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteRole err = %v, want ErrNotFound", err)
	}
	if after := roleRow(t, f.super, roleID); !after.equal(before) {
		t.Errorf("deleted role changed: %+v -> %+v", before, after)
	}
}

func TestDeleteRole_UnknownKeyWithAnOpenRunStepIsNotFound(t *testing.T) {
	f := newRunFixture(t, "APSC-10 ghost key")
	seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("ghost-key"))

	if _, err := f.store.DeleteRole(f.admin, "ghost-key"); !errors.Is(err, ErrNotFound) || errors.Is(err, ErrRoleInUse) {
		t.Fatalf("DeleteRole err = %v, want ErrNotFound", err)
	}
}

func TestDeleteRole_NonAdminGetsForbiddenNotWaiting(t *testing.T) {
	f := newRunFixture(t, "APSC-10 non-admin")
	roleID := seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("tax-reviewer"))
	preparer, _ := callerCtx(t, f.super, f.tenantID, "preparer", "active")

	_, err := f.store.DeleteRole(preparer, "tax-reviewer")
	if !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("DeleteRole err = %v, want ErrNotPermitted", err)
	}
	if status, _ := statusForErr(err); status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", status)
	}
	assertRoleUntouched(t, f.super, f.tenantID, roleID)
}

func TestDeleteRoleHandler_RealStoreRunRefusalHasEmptyPoliciesArray(t *testing.T) {
	f := newRunFixture(t, "APSC-10 real store handler")
	seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
	inv, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-1", f.versionID, pendingOn("tax-reviewer"))

	req := httptest.NewRequest("DELETE", "/v1/workflow-roles/tax-reviewer", nil).WithContext(f.admin)
	req.SetPathValue("key", "tax-reviewer")
	rec := httptest.NewRecorder()
	DeleteRoleHandler(f.store.DeleteRole, slog.New(slog.NewJSONHandler(io.Discard, nil))).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	if got := strings.Join(keySet(t, body), ","); got != "error,invoices,invoices_total,policies" {
		t.Errorf("body keys = %s, want error,invoices,invoices_total,policies", got)
	}
	if got := rawField(t, body, "policies"); got != "[]" {
		t.Errorf("policies = %s, want []", got)
	}
	if got := rawField(t, body, "invoices"); got != fmt.Sprintf(`[{"id":%q,"invoice_number":"INV-1"}]`, inv) {
		t.Errorf("invoices = %s, want items with only id and invoice_number", got)
	}
	var decoded struct {
		Invoices []InvoiceRef `json:"invoices"`
		Total    int          `json:"invoices_total"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Invoices) != 1 || decoded.Invoices[0] != (InvoiceRef{ID: inv, Number: "INV-1"}) || decoded.Total != 1 {
		t.Errorf("invoices = %+v, total = %d, want [INV-1] and 1", decoded.Invoices, decoded.Total)
	}
	if got := errorMessage(t, body); got != roleInRunMessage {
		t.Errorf("error = %q, want roleInRunMessage", got)
	}
}

func TestRoleInUse_ErrorIsCauseNeutral(t *testing.T) {
	e := &roleInUse{invoices: []InvoiceRef{{ID: "i1", Number: "INV-1"}}, invoicesTotal: 1}
	if strings.Contains(e.Error(), "policy") {
		t.Errorf("Error() = %q, names a policy for a run-only refusal", e.Error())
	}
}

func TestStatusForErr_RoleInUseComposesTheSentence(t *testing.T) {
	pol := []PolicyRef{{ID: "p1", Name: "A"}}
	inv := []InvoiceRef{{ID: "i1", Number: "INV-1"}}
	cases := []struct {
		name string
		e    *roleInUse
		want string
	}{
		{"run only", &roleInUse{invoices: inv, invoicesTotal: 1}, roleInRunMessage},
		{"both", &roleInUse{policies: pol, invoices: inv, invoicesTotal: 1}, roleInUseMessage + "; " + roleInRunMessage},
		{"last holder", &roleInUse{lastHolder: true, invoices: inv, invoicesTotal: 1}, roleLastHolderMessage},
	}
	for _, c := range cases {
		status, msg := statusForErr(fmt.Errorf("wrapped: %w", c.e))
		if status != http.StatusConflict || msg != c.want {
			t.Errorf("%s: got %d %q, want 409 %q", c.name, status, msg, c.want)
		}
	}
}
