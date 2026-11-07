package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// The sentence restated on purpose: the oracle for the shipped constant.
const memberLastSignerSentence = "this change would leave a workflow role an open approval run is waiting on with no active admin or reviewer — add another admin or reviewer to the role, or decide the run or edit its invoice to cancel it, first"

func (f runFixture) staffedRoleKeys(t *testing.T, subject string) []string {
	t.Helper()
	var keys []string
	if err := db.WithinTenantTx(context.Background(), f.app, f.tenantID, func(tx pgx.Tx) error {
		var err error
		keys, err = StaffedRoleKeysTx(context.Background(), tx, subject)
		return err
	}); err != nil {
		t.Fatalf("StaffedRoleKeysTx: %v", err)
	}
	return keys
}

func (f runFixture) refuseUnsigned(keys ...string) error {
	return db.WithinTenantTx(context.Background(), f.app, f.tenantID, func(tx pgx.Tx) error {
		return RefuseUnsignedWaitingRolesTx(context.Background(), tx, keys)
	})
}

func (f runFixture) wantRefusedUnsigned(t *testing.T, keys ...string) *roleInUse {
	t.Helper()
	err := f.refuseUnsigned(keys...)
	var inUse *roleInUse
	if !errors.Is(err, ErrRoleInUse) || !errors.As(err, &inUse) {
		t.Fatalf("RefuseUnsignedWaitingRolesTx(%v) err = %v, want *roleInUse", keys, err)
	}
	if !inUse.lastSigner {
		t.Error("lastSigner = false, want true")
	}
	return inUse
}

func (f runFixture) wantPassedUnsigned(t *testing.T, keys ...string) {
	t.Helper()
	if err := f.refuseUnsigned(keys...); err != nil {
		t.Fatalf("RefuseUnsignedWaitingRolesTx(%v) err = %v, want nil", keys, err)
	}
}

// seedRunsOn opens n runs pending on key; numbers carry prefix so two roles never collide.
func (f runFixture) seedRunsOn(t *testing.T, key, prefix string, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, fmt.Sprintf("%s-%02d", prefix, i), f.versionID, pendingOn(key))
		ids = append(ids, id)
	}
	return ids
}

func TestStaffedRoleKeysTx_ListsTheLiveRolesTheSubjectIsStaffedIn(t *testing.T) {
	f := newRunFixture(t, "APSC-13 staffed keys")
	roleB := seedWorkflowRole(t, f.super, f.tenantID, "b", "B")
	roleA := seedWorkflowRole(t, f.super, f.tenantID, "a", "A")
	roleC := seedWorkflowRole(t, f.super, f.tenantID, "c", "C")
	roleD := seedWorkflowRole(t, f.super, f.tenantID, "d", "D")
	subject := seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "reviewer"})
	other := seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "reviewer"})
	for _, id := range []string{roleB, roleA, roleC} {
		staffWorkflowRole(t, f.super, f.tenantID, id, subject, 0)
	}
	staffWorkflowRole(t, f.super, f.tenantID, roleD, other, 0)
	softDeleteWorkflowRole(t, f.super, roleC)

	got := f.staffedRoleKeys(t, subject)
	if want := []string{"a", "b"}; !slices.Equal(got, want) {
		t.Errorf("keys = %v, want %v (key order, live roles only, only this subject's)", got, want)
	}
}

func TestStaffedRoleKeysTx_IgnoresStatusAndAccessRole(t *testing.T) {
	f := newRunFixture(t, "APSC-13 staffed keys any status")
	for _, c := range []struct{ name, role, status string }{
		{"preparer", "preparer", "active"},
		{"suspended reviewer", "reviewer", "suspended"},
		{"invited admin", "admin", "invited"},
		{"active reviewer", "reviewer", "active"},
	} {
		t.Run(c.name, func(t *testing.T) {
			key := "r-" + strings.ReplaceAll(c.name, " ", "-")
			roleID := seedWorkflowRole(t, f.super, f.tenantID, key, c.name)
			subject := seedMemberOf(t, f.super, f.tenantID, memberSpec{role: c.role, status: c.status, roleID: roleID})
			if got := f.staffedRoleKeys(t, subject); !slices.Equal(got, []string{key}) {
				t.Errorf("keys = %v, want [%s]", got, key)
			}
		})
	}

	loner := seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "reviewer"})
	if got := f.staffedRoleKeys(t, loner); len(got) != 0 {
		t.Errorf("a member in no role: keys = %v, want none", got)
	}
}

func TestRefuseUnsignedWaitingRoles_RefusesARoleLeftWithNoSigner(t *testing.T) {
	f := newLastHolderFixture(t, "APSC-13 refuses")
	r := seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "reviewer", roleID: f.roleID})
	f.wantPassedUnsigned(t, "tax-reviewer") // R signs, so the role is fine

	setMembershipStatus(t, f.super, f.tenantID, r, "suspended")

	inUse := f.wantRefusedUnsigned(t, "tax-reviewer")
	want := InvoiceRef{ID: f.invoiceID, Number: "INV-1", RoleKey: "tax-reviewer", RoleTitle: "Tax Reviewer"}
	if len(inUse.invoices) != 1 || inUse.invoices[0] != want || inUse.invoicesTotal != 1 {
		t.Errorf("invoices = %+v, total = %d, want [%+v] and 1", inUse.invoices, inUse.invoicesTotal, want)
	}
	if len(inUse.policies) != 0 {
		t.Errorf("policies = %+v, want none", inUse.policies)
	}
}

func TestRefuseUnsignedWaitingRoles_AnotherSignerLetsItPass(t *testing.T) {
	for _, signer := range []string{"reviewer", "admin"} {
		t.Run(signer, func(t *testing.T) {
			f := newLastHolderFixture(t, "APSC-13 another signer "+signer)
			seedMemberOf(t, f.super, f.tenantID, memberSpec{role: "reviewer", status: "suspended", roleID: f.roleID})
			f.wantRefusedUnsigned(t, "tax-reviewer") // the suspended member alone signs nothing

			seedMemberOf(t, f.super, f.tenantID, memberSpec{role: signer, roleID: f.roleID})
			f.wantPassedUnsigned(t, "tax-reviewer")
		})
	}
}

func TestRefuseUnsignedWaitingRoles_AgreesWithTheStaffingRefusal(t *testing.T) {
	dbTestPools(t) // skip here, before the both-outcomes guard counts empty subtests
	cases := []struct {
		name    string
		members []memberSpec
	}{
		{"empty", nil},
		{"preparer", []memberSpec{{role: "preparer"}}},
		{"suspended reviewer", []memberSpec{{role: "reviewer", status: "suspended"}}},
		{"invited admin", []memberSpec{{role: "admin", status: "invited"}}},
		{"preparer and active reviewer", []memberSpec{{role: "preparer"}, {role: "reviewer"}}},
		{"active admin", []memberSpec{{role: "admin"}}},
	}
	var refused, passed int
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newLastHolderFixture(t, "APSC-13 agrees "+c.name)
			ids := []string{}
			for _, m := range c.members {
				m.roleID = f.roleID
				ids = append(ids, seedMemberOf(t, f.super, f.tenantID, m))
			}

			ours := f.refuseUnsigned("tax-reviewer")
			_, theirs := f.store.SetRoleMembers(f.admin, "tax-reviewer", ids)

			if ours != nil && !errors.Is(ours, ErrRoleInUse) {
				t.Fatalf("RefuseUnsignedWaitingRolesTx err = %v, want nil or ErrRoleInUse", ours)
			}
			if theirs != nil && !errors.Is(theirs, ErrRoleInUse) {
				t.Fatalf("SetRoleMembers err = %v, want nil or ErrRoleInUse", theirs)
			}
			if (ours != nil) != (theirs != nil) {
				t.Errorf("RefuseUnsignedWaitingRolesTx err = %v, SetRoleMembers err = %v, want both refuse or both pass", ours, theirs)
			}
			if theirs != nil {
				refused++
			} else {
				passed++
			}
		})
	}
	if refused == 0 || passed == 0 {
		t.Errorf("refused = %d, passed = %d, want the table to hold both outcomes", refused, passed)
	}
}

func TestRefuseUnsignedWaitingRoles_EmptyKeysIsNil(t *testing.T) {
	f := newLastHolderFixture(t, "APSC-13 empty keys")
	f.wantRefusedUnsigned(t, "tax-reviewer") // an unsigned, waited-on role exists

	f.wantPassedUnsigned(t)
	f.wantPassedUnsigned(t, []string{}...)
	if err := f.refuseUnsigned(nil...); err != nil {
		t.Errorf("nil keys err = %v, want nil", err)
	}
}

func TestRefuseUnsignedWaitingRoles_ARunThatCannotWaitDoesNotRefuse(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f runFixture)
	}{
		{"run cancelled", func(t *testing.T, f runFixture) {
			inv, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-X", f.versionID, pendingOn("tax-reviewer"))
			if ok, err := cancel(t, f.app, f.tenantID, inv, "editor"); err != nil || !ok {
				t.Fatalf("cancel = %v, %v, want true, nil", ok, err)
			}
		}},
		{"run approved", func(t *testing.T, f runFixture) {
			_, run := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-X", f.versionID, pendingOn("tax-reviewer"))
			closeApprovalRunFor(t, f.super, run, "approved", "someone")
		}},
		{"invoice queued", func(t *testing.T, f runFixture) {
			inv, _ := seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-X", f.versionID, pendingOn("tax-reviewer"))
			setInvoiceStatus(t, f.super, inv, "queued")
		}},
		{"pending step on another role", func(t *testing.T, f runFixture) {
			seedWorkflowRole(t, f.super, f.tenantID, "other", "Other")
			seedOpenRun(t, f.super, f.tenantID, f.entityID, "INV-X", f.versionID, pendingOn("other"))
		}},
		{"open run in another tenant on the same key", func(t *testing.T, f runFixture) {
			tenantB := policyTenant(t, f.super, "APSC-13 tenant B")
			seedWorkflowRole(t, f.super, tenantB, "tax-reviewer", "Tax Reviewer")
			policyB := seedApprovalPolicy(t, f.super, tenantB, "B policy")
			versionB := seedApprovalPolicyVersionN(t, f.super, tenantB, policyB, 1)
			entityB := seedBusinessEntity(t, f.super, tenantB, "B Corp")
			seedOpenRun(t, f.super, tenantB, entityB, "INV-B", versionB, pendingOn("tax-reviewer"))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newRunFixture(t, "APSC-13 cannot wait "+c.name)
			seedWorkflowRole(t, f.super, f.tenantID, "tax-reviewer", "Tax Reviewer")
			c.setup(t, f)
			f.wantPassedUnsigned(t, "tax-reviewer")

			// The role is unsigned: an ordinary waiting run refuses, so the nil above was the case's doing.
			control := f.seedRunsOn(t, "tax-reviewer", "CTL", 1)
			inUse := f.wantRefusedUnsigned(t, "tax-reviewer")
			if len(inUse.invoices) != 1 || inUse.invoices[0].ID != control[0] || inUse.invoicesTotal != 1 {
				t.Errorf("invoices = %+v, total = %d, want only the control run %s", inUse.invoices, inUse.invoicesTotal, control[0])
			}
		})
	}
}

func TestRefuseUnsignedWaitingRoles_SkipsADeletedRole(t *testing.T) {
	f := newLastHolderFixture(t, "APSC-13 deleted role")
	f.wantRefusedUnsigned(t, "tax-reviewer")

	softDeleteWorkflowRole(t, f.super, f.roleID)

	f.wantPassedUnsigned(t, "tax-reviewer")
	f.wantPassedUnsigned(t, "ghost-key")
}

func TestRefuseUnsignedWaitingRoles_ListsEachInvoiceWithItsRoleAcrossRoles(t *testing.T) {
	f := newRunFixture(t, "APSC-13 across roles")
	seedWorkflowRole(t, f.super, f.tenantID, "a", "Role A")
	seedWorkflowRole(t, f.super, f.tenantID, "b", "Role B")
	a1 := f.seedRunsOn(t, "a", "A1", 1)[0]
	b1 := f.seedRunsOn(t, "b", "B1", 1)[0] // between a's two runs: grouping is by role, not by age
	a2 := f.seedRunsOn(t, "a", "A2", 1)[0]

	inUse := f.wantRefusedUnsigned(t, "a", "b")

	want := []InvoiceRef{
		{ID: a1, Number: "A1-00", RoleKey: "a", RoleTitle: "Role A"},
		{ID: a2, Number: "A2-00", RoleKey: "a", RoleTitle: "Role A"},
		{ID: b1, Number: "B1-00", RoleKey: "b", RoleTitle: "Role B"},
	}
	if !slices.Equal(inUse.invoices, want) || inUse.invoicesTotal != 3 {
		t.Errorf("invoices = %+v, total = %d, want %+v and 3", inUse.invoices, inUse.invoicesTotal, want)
	}
}

func TestRefuseUnsignedWaitingRoles_CapsAtTwentyAndCountsAll(t *testing.T) {
	t.Run("two roles", func(t *testing.T) {
		f := newRunFixture(t, "APSC-13 cap two roles")
		seedWorkflowRole(t, f.super, f.tenantID, "a", "Role A")
		seedWorkflowRole(t, f.super, f.tenantID, "b", "Role B")
		f.seedRunsOn(t, "a", "A", 15)
		f.seedRunsOn(t, "b", "B", 10)

		inUse := f.wantRefusedUnsigned(t, "a", "b")

		if len(inUse.invoices) != 20 || inUse.invoicesTotal != 25 {
			t.Fatalf("listed = %d, total = %d, want 20 and 25", len(inUse.invoices), inUse.invoicesTotal)
		}
		var a, b int
		for i, ref := range inUse.invoices {
			switch ref.RoleKey {
			case "a":
				a++
				if i >= 15 {
					t.Errorf("item %d is role a after role b started", i)
				}
			case "b":
				b++
			}
		}
		if a != 15 || b != 5 {
			t.Errorf("a items = %d, b items = %d, want 15 and 5", a, b)
		}
	})

	t.Run("exactly twenty in one role", func(t *testing.T) {
		f := newRunFixture(t, "APSC-13 cap exactly twenty")
		seedWorkflowRole(t, f.super, f.tenantID, "a", "Role A")
		f.seedRunsOn(t, "a", "A", 20)

		inUse := f.wantRefusedUnsigned(t, "a")

		if len(inUse.invoices) != 20 || inUse.invoicesTotal != 20 {
			t.Errorf("listed = %d, total = %d, want 20 and 20", len(inUse.invoices), inUse.invoicesTotal)
		}
	})
}

func TestWriteRoleInUse_MemberRefusalBody(t *testing.T) {
	id := uuid.NewString()
	err := &roleInUse{
		lastSigner:    true,
		invoices:      []InvoiceRef{{ID: id, Number: "INV-1", RoleKey: "approver", RoleTitle: "Approver"}},
		invoicesTotal: 1,
	}
	rec := httptest.NewRecorder()

	WriteRoleInUse(rec, err)

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
	if got := errorMessage(t, body); got != memberLastSignerSentence {
		t.Errorf("error = %q, want the member sentence %q", got, memberLastSignerSentence)
	}
	if memberLastSignerMessage != memberLastSignerSentence {
		t.Errorf("memberLastSignerMessage = %q, want %q", memberLastSignerMessage, memberLastSignerSentence)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(rawField(t, body, "invoices")), &items); err != nil || len(items) != 1 {
		t.Fatalf("invoices = %s, err = %v, want one item", rawField(t, body, "invoices"), err)
	}
	var keys []string
	for k := range items[0] {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if got := strings.Join(keys, ","); got != "id,invoice_number,workflow_role_key,workflow_role_title" {
		t.Errorf("item keys = %s, want id,invoice_number,workflow_role_key,workflow_role_title", got)
	}
}
