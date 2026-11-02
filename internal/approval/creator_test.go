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
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newStandingWorld(t, super, "APPR-17 who-counts")
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
				seedMemberOf(t, super, w.tenantID, spec)
			}
			got := readStanding(t, app, w.tenantID, w.creator, w.probe())[w.invoice]
			if !got.IsCreator {
				t.Fatalf("IsCreator = false, want true: the first history row's actor is the caller")
			}
			if got.OtherApprover != c.wantOther {
				t.Errorf("OtherApprover = %t, want %t", got.OtherApprover, c.wantOther)
			}
			if got.OtherRoleHolder != c.wantHolds {
				t.Errorf("OtherRoleHolder = %t, want %t", got.OtherRoleHolder, c.wantHolds)
			}
		})
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
	page := []CreatorProbe{{byOther, key}, {bySystem, key}, {noRow, key}}

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
