package approval

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// readStanding reads the standing as the app role inside a tenant tx, like decideTx does.
func readStanding(t *testing.T, app *pgxpool.Pool, tenantID, subject string, probes ...CreatorProbe) map[string]CreatorStanding {
	t.Helper()
	var got map[string]CreatorStanding
	err := db.WithinTenantTx(context.Background(), app, tenantID, func(tx pgx.Tx) error {
		var err error
		got, err = CreatorStandingTx(context.Background(), tx, subject, probes)
		return err
	})
	if err != nil {
		t.Fatalf("CreatorStandingTx: %v", err)
	}
	return got
}

// standingWorld is a firm tenant with clients A and B, an invoice of A created by creator.
type standingWorld struct {
	tenantID, entA, entB, roleID, invoice, creator string
}

func newStandingWorld(t *testing.T, super *pgxpool.Pool, name string) standingWorld {
	t.Helper()
	w := standingWorld{tenantID: seedTenant(t, super, name)}
	w.entA = seedBusinessEntity(t, super, w.tenantID, name+" A")
	w.entB = seedBusinessEntity(t, super, w.tenantID, name+" B")
	w.roleID = seedWorkflowRole(t, super, w.tenantID, "standing-role", "Standing role")
	w.invoice = seedInvoice(t, super, w.tenantID, w.entA, name+"-inv")
	w.creator = seedMemberOf(t, super, w.tenantID, memberSpec{role: "reviewer"})
	seedHistory(t, super, w.tenantID, w.invoice, nil, w.creator)
	return w
}

func (w standingWorld) probe() CreatorProbe {
	return CreatorProbe{InvoiceID: w.invoice, RoleKey: ptr("standing-role")}
}

func TestCreatorStanding_WhoCounts(t *testing.T) {
	super, app := dbTestPools(t)
	cases := []struct {
		name                 string
		spec                 memberSpec
		holds                bool
		limitToA, limitToB   bool
		wantOther, wantHolds bool
	}{
		{"limited with the client", memberSpec{role: "reviewer", limited: true}, false, true, false, true, false},
		{"limited with the client and the role", memberSpec{role: "reviewer", limited: true}, true, true, false, true, true},
		{"limited without the client, holds the role", memberSpec{role: "reviewer", limited: true}, true, false, true, false, false},
		{"all-clients reviewer", memberSpec{role: "reviewer"}, false, false, false, true, false},
		{"admin limited to no client", memberSpec{role: "admin", limited: true}, false, false, false, true, false},
		{"preparer holding the role", memberSpec{role: "preparer"}, true, false, false, false, false},
		{"suspended reviewer holding the role", memberSpec{role: "reviewer", status: "suspended"}, true, false, false, false, false},
		{"invited reviewer holding the role", memberSpec{role: "reviewer", status: "invited"}, true, false, false, false, false},
		{"nobody but the caller", memberSpec{}, false, false, false, false, false},
	}
	// How the step delegates: who it names (none, the member, a different member, the creator).
	delegations := []struct {
		name     string
		allow    bool
		delegate string // "", "member", "different", "creator"
	}{
		{"anyone", true, ""},
		{"names the member", true, "member"},
		{"names a different member", true, "different"},
		{"off, names the member", false, "member"},
		{"names the creator", true, "creator"},
	}
	for _, c := range cases {
		for _, d := range delegations {
			if d.delegate == "member" && c.spec.role == "" {
				continue // no member seeded to name
			}
			t.Run(c.name+"/"+d.name, func(t *testing.T) {
				w := newStandingWorld(t, super, "APPR-17 who-counts")
				member := ""
				if c.spec.role != "" {
					spec := c.spec
					if c.holds {
						spec.roleID = w.roleID
					}
					if c.limitToA {
						spec.entityIDs = []string{w.entA}
					}
					if c.limitToB {
						spec.entityIDs = []string{w.entB}
					}
					member = seedMemberOf(t, super, w.tenantID, spec)
				}
				probe := w.probe()
				probe.AllowDelegation = d.allow
				switch d.delegate {
				case "member":
					probe.DelegateUserID = ptr(member)
				case "different":
					probe.DelegateUserID = ptr(seedMemberOf(t, super, w.tenantID, memberSpec{role: "preparer"}))
				case "creator":
					probe.DelegateUserID = ptr(w.creator)
				}
				// The member counts as a delegate only when it is an other approver and the step lets it decide.
				wantDelegate := c.wantOther && DelegateMayDecide(d.allow, probe.DelegateUserID, member)
				got := readStanding(t, app, w.tenantID, w.creator, probe)[w.invoice]
				if !got.IsCreator {
					t.Fatalf("IsCreator = false, want true: the first history row's actor is the caller")
				}
				if got.OtherApprover != c.wantOther {
					t.Errorf("OtherApprover = %t, want %t", got.OtherApprover, c.wantOther)
				}
				if got.OtherRoleHolder != c.wantHolds {
					t.Errorf("OtherRoleHolder = %t, want %t", got.OtherRoleHolder, c.wantHolds)
				}
				if got.OtherDelegate != wantDelegate {
					t.Errorf("OtherDelegate = %t, want %t", got.OtherDelegate, wantDelegate)
				}
			})
		}
	}
}

func TestCreatorStanding_InHouseEveryActiveApproverCounts(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := uuid.NewString()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'APPR-17 in-house', 'in_house')`, tenantID); err != nil {
		t.Fatalf("seed in-house tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID) })
	entity := seedBusinessEntity(t, super, tenantID, "In-house entity")
	invoice := seedInvoice(t, super, tenantID, entity, "in-house-inv")
	creator := seedMemberOf(t, super, tenantID, memberSpec{role: "reviewer"})
	seedHistory(t, super, tenantID, invoice, nil, creator)
	seedMemberOf(t, super, tenantID, memberSpec{role: "reviewer", limited: true}) // no client rows

	got := readStanding(t, app, tenantID, creator, CreatorProbe{InvoiceID: invoice})[invoice]
	if !got.IsCreator {
		t.Fatal("IsCreator = false, want true")
	}
	if !got.OtherApprover {
		t.Error("OtherApprover = false, want true: client scope does not apply in an in-house workspace")
	}
}

func TestCreatorStanding_LimitedCallerSeesEveryOtherApprover(t *testing.T) {
	super, app := dbTestPools(t)
	w := newStandingWorld(t, super, "APPR-17 limited-caller")
	// A limited creator: RLS scopes the caller's own client list, never the member list.
	limitMemberTo(t, super, w.tenantID, w.creator, w.entA)
	seedMemberOf(t, super, w.tenantID, memberSpec{role: "reviewer", limited: true, entityIDs: []string{w.entA}, roleID: w.roleID})

	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: w.creator, Role: "authenticated", TenantID: w.tenantID})
	var got map[string]CreatorStanding
	err := db.WithinRequestTenantTx(ctx, app, func(tx pgx.Tx) error {
		var err error
		got, err = CreatorStandingTx(ctx, tx, w.creator, []CreatorProbe{w.probe()})
		return err
	})
	if err != nil {
		t.Fatalf("CreatorStandingTx under the request seam: %v", err)
	}
	if !got[w.invoice].IsCreator {
		t.Fatal("IsCreator = false, want true")
	}
	if !got[w.invoice].OtherApprover || !got[w.invoice].OtherRoleHolder {
		t.Errorf("standing = %+v, want OtherApprover and OtherRoleHolder: a limited caller reads every member's client list", got[w.invoice])
	}
}

func TestCreatorStanding_TenantIsolated(t *testing.T) {
	super, app := dbTestPools(t)
	w := newStandingWorld(t, super, "APPR-17 isolated")
	other := newStandingWorld(t, super, "APPR-17 isolated other")
	// One user id: a preparer here (no count), an approver holding the role in the other tenant.
	shared := uuid.NewString()
	seedMembership(t, super, w.tenantID, shared, "preparer", "active")
	seedMembership(t, super, other.tenantID, shared, "reviewer", "active")
	staffWorkflowRole(t, super, other.tenantID, other.roleID, shared, 0)

	got := readStanding(t, app, w.tenantID, w.creator, w.probe())[w.invoice]
	if !got.IsCreator {
		t.Fatal("IsCreator = false, want true")
	}
	if got.OtherApprover || got.OtherRoleHolder {
		t.Errorf("standing = %+v, want no other approver: the user's approver rows belong to another tenant", got)
	}
}

func TestCreatorStanding_NonCreatorStopsAfterOneStatement(t *testing.T) {
	super, _ := dbTestPools(t)
	w := newStandingWorld(t, super, "APPR-17 one-statement")
	other := seedMemberOf(t, super, w.tenantID, memberSpec{role: "reviewer", roleID: w.roleID})
	byOther := seedInvoice(t, super, w.tenantID, w.entA, "one-stmt-by-other")
	seedHistory(t, super, w.tenantID, byOther, nil, other)
	bySystem := seedInvoice(t, super, w.tenantID, w.entA, "one-stmt-by-system")
	seedHistory(t, super, w.tenantID, bySystem, nil, "system")
	noRow := seedInvoice(t, super, w.tenantID, w.entA, "one-stmt-no-row")
	key := ptr("standing-role")
	page := []CreatorProbe{{InvoiceID: byOther, RoleKey: key}, {InvoiceID: bySystem, RoleKey: key}, {InvoiceID: noRow, RoleKey: key}}

	traced, rec := tracedAppPool(t)
	rec.reset()
	got := readStanding(t, traced, w.tenantID, w.creator, page...)
	if n := len(rec.mentioning("invoice_status_history")); n != 1 {
		t.Errorf("statements mentioning invoice_status_history = %d, want 1", n)
	}
	if n := len(rec.mentioning("memberships")); n != 0 {
		t.Errorf("statements mentioning memberships = %d, want 0: a page with no creator row stops after the first read", n)
	}
	for _, p := range page {
		if got[p.InvoiceID].IsCreator {
			t.Errorf("invoice %s: IsCreator = true for a non-creator", p.InvoiceID)
		}
	}

	// Control: one creator row on the page buys the second statement, and only one.
	rec.reset()
	got = readStanding(t, traced, w.tenantID, w.creator, append(page, w.probe())...)
	if !got[w.invoice].IsCreator || !got[w.invoice].OtherApprover {
		t.Errorf("standing of the creator's invoice = %+v, want IsCreator and OtherApprover", got[w.invoice])
	}
	if n := len(rec.mentioning("memberships")); n != 1 {
		t.Errorf("statements mentioning memberships = %d, want exactly 1 for a page holding a creator row", n)
	}
}

// One page, several clients and role keys: each invoice keeps its own client scope and role key.
// The non-creator row comes first so a key list misaligned with the creator rows would show.
func TestCreatorStanding_OnePageKeepsEachInvoiceOwnClientAndRole(t *testing.T) {
	super, app := dbTestPools(t)
	w := newStandingWorld(t, super, "APPR-17 page-scope")
	seedWorkflowRole(t, super, w.tenantID, "unheld-role", "Unheld role")
	limited := seedMemberOf(t, super, w.tenantID, memberSpec{role: "reviewer", limited: true, entityIDs: []string{w.entA}, roleID: w.roleID})

	other := seedMemberOf(t, super, w.tenantID, memberSpec{role: "reviewer"})
	byOther := seedInvoice(t, super, w.tenantID, w.entA, "page-by-other")
	seedHistory(t, super, w.tenantID, byOther, nil, other)
	unheldOnA := seedInvoice(t, super, w.tenantID, w.entA, "page-unheld-on-a")
	seedHistory(t, super, w.tenantID, unheldOnA, nil, w.creator)
	onB := seedInvoice(t, super, w.tenantID, w.entB, "page-on-b")
	seedHistory(t, super, w.tenantID, onB, nil, w.creator)
	namedOnA := seedInvoice(t, super, w.tenantID, w.entA, "page-named-on-a")
	seedHistory(t, super, w.tenantID, namedOnA, nil, w.creator)
	namedSuspended := seedInvoice(t, super, w.tenantID, w.entA, "page-named-suspended")
	seedHistory(t, super, w.tenantID, namedSuspended, nil, w.creator)
	// The all-clients reviewer `other` reaches B, so drop it from the count by suspending it.
	if _, err := super.Exec(context.Background(),
		`UPDATE memberships SET status = 'suspended' WHERE tenant_id = $1 AND user_id = $2`, w.tenantID, other); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	held, unheld := ptr("standing-role"), ptr("unheld-role")
	got := readStanding(t, app, w.tenantID, w.creator,
		CreatorProbe{InvoiceID: byOther, RoleKey: unheld}, CreatorProbe{InvoiceID: w.invoice, RoleKey: held},
		CreatorProbe{InvoiceID: unheldOnA, RoleKey: unheld, AllowDelegation: true},
		CreatorProbe{InvoiceID: onB, RoleKey: held, AllowDelegation: true},
		CreatorProbe{InvoiceID: namedOnA, RoleKey: unheld, AllowDelegation: true, DelegateUserID: &limited},
		CreatorProbe{InvoiceID: namedSuspended, RoleKey: unheld, AllowDelegation: true, DelegateUserID: &other})
	if len(got) != 5 {
		t.Fatalf("standings = %d, want 5 (the creator's invoices only): %+v", len(got), got)
	}
	if got[byOther].IsCreator {
		t.Error("an invoice created by someone else reads IsCreator")
	}
	if want := (CreatorStanding{IsCreator: true, OtherApprover: true, OtherRoleHolder: true}); got[w.invoice] != want {
		t.Errorf("client A, key held by the limited reviewer = %+v, want %+v", got[w.invoice], want)
	}
	if want := (CreatorStanding{IsCreator: true, OtherApprover: true, OtherRoleHolder: false, OtherDelegate: true}); got[unheldOnA] != want {
		t.Errorf("client A, key nobody holds, step open to anyone = %+v, want %+v", got[unheldOnA], want)
	}
	if want := (CreatorStanding{IsCreator: true}); got[onB] != want {
		t.Errorf("client B, only a reviewer limited to A = %+v, want %+v", got[onB], want)
	}
	if want := (CreatorStanding{IsCreator: true, OtherApprover: true, OtherDelegate: true}); got[namedOnA] != want {
		t.Errorf("client A, step names the limited reviewer = %+v, want %+v", got[namedOnA], want)
	}
	if want := (CreatorStanding{IsCreator: true, OtherApprover: true}); got[namedSuspended] != want {
		t.Errorf("client A, step names a suspended reviewer = %+v, want %+v", got[namedSuspended], want)
	}
}

func TestCreatorStanding_DelegateAgreesWithDelegateMayDecide(t *testing.T) {
	super, app := dbTestPools(t)
	w := newStandingWorld(t, super, "BUG-118 agrees")
	other := seedMemberOf(t, super, w.tenantID, memberSpec{role: "reviewer"})
	stranger := uuid.NewString()
	for _, allow := range []bool{false, true} {
		for _, delegate := range []*string{nil, &other, &stranger, &w.creator} {
			p := w.probe()
			p.AllowDelegation, p.DelegateUserID = allow, delegate
			got := readStanding(t, app, w.tenantID, w.creator, p)[w.invoice]
			if want := DelegateMayDecide(allow, delegate, other); got.OtherDelegate != want {
				t.Errorf("allow=%t delegate=%v: OtherDelegate = %t, DelegateMayDecide = %t", allow, delegate, got.OtherDelegate, want)
			}
		}
	}
}

func TestCreatorStanding_Refusal(t *testing.T) {
	both := CreatorRefusedReason + " " + CreatorNoRoleHolderReason
	for _, c := range []struct {
		name        string
		s           CreatorStanding
		want        string
		wantRefused bool
	}{
		{"not the creator", CreatorStanding{OtherApprover: true}, "", false},
		{"creator, no other approver", CreatorStanding{IsCreator: true}, "", false},
		{"holder only", CreatorStanding{IsCreator: true, OtherApprover: true, OtherRoleHolder: true}, CreatorRefusedReason, true},
		{"delegate only", CreatorStanding{IsCreator: true, OtherApprover: true, OtherDelegate: true}, CreatorRefusedReason, true},
		{"neither", CreatorStanding{IsCreator: true, OtherApprover: true}, both, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, refused := c.s.Refusal()
			if got != c.want || refused != c.wantRefused {
				t.Errorf("Refusal() = %q, %t, want %q, %t", got, refused, c.want, c.wantRefused)
			}
		})
	}
}

// Another tenant's approvers and invoices stay invisible to the read, whoever they are.
func TestCreatorStanding_AnotherTenantsApproversAndInvoicesStayInvisible(t *testing.T) {
	super, app := dbTestPools(t)
	w := newStandingWorld(t, super, "APPR-17 xt-home")
	o := newStandingWorld(t, super, "APPR-17 xt-other")
	seedMemberOf(t, super, o.tenantID, memberSpec{role: "reviewer", roleID: o.roleID})
	seedMemberOf(t, super, o.tenantID, memberSpec{role: "admin", roleID: o.roleID})
	// An invoice in the other tenant whose first row names the home creator.
	foreign := seedInvoice(t, super, o.tenantID, o.entA, "xt-foreign")
	seedHistory(t, super, o.tenantID, foreign, nil, w.creator)

	got := readStanding(t, app, w.tenantID, w.creator, w.probe(), CreatorProbe{InvoiceID: foreign, RoleKey: ptr("standing-role")})
	if !got[w.invoice].IsCreator {
		t.Fatalf("home invoice standing = %+v, want IsCreator", got[w.invoice])
	}
	if got[w.invoice].OtherApprover || got[w.invoice].OtherRoleHolder {
		t.Errorf("home standing = %+v: the other tenant's approvers must not count", got[w.invoice])
	}
	if s, ok := got[foreign]; ok {
		t.Errorf("a foreign tenant's invoice read as %+v, want absent", s)
	}
}
