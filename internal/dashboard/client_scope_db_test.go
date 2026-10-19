package dashboard

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

func csMember(t *testing.T, super *pgxpool.Pool, tenant, role string, clients ...string) context.Context {
	t.Helper()
	ctx := context.Background()
	user := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role, status, all_clients) VALUES ($1, $2, $3, 'active', false)`,
		tenant, user, role); err != nil {
		t.Fatalf("seed %s: %v", role, err)
	}
	for _, e := range clients {
		if _, err := super.Exec(ctx,
			`INSERT INTO membership_clients (tenant_id, user_id, entity_id) VALUES ($1, $2, $3)`, tenant, user, e); err != nil {
			t.Fatalf("seed membership_clients: %v", err)
		}
	}
	return auth.WithIdentity(ctx, auth.Identity{Subject: user, Role: "authenticated", TenantID: tenant})
}

type csDash struct {
	store                    *Store
	entA, entB               string
	admin, limited, unassign context.Context
}

// Client A: a draft with an error violation, a validated invoice. Client B: a rejected
// invoice with a violation and a draft whose latest approval run closed rejected.
func newCSDash(t *testing.T, label string) csDash {
	t.Helper()
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, label)
	d := csDash{store: NewStore(app)}
	d.entA = seedEntity(t, super, tenant, "Client A")
	d.entB = seedEntity(t, super, tenant, "Client B")
	seedInvoiceWithViolations(t, super, tenant, d.entA, "CS-A-1", "draft", `[{"rule_key":"cs-rA","severity":"error","message":"a"}]`)
	seedInvoiceAtStatus(t, super, tenant, d.entA, "CS-A-2", "validated")
	seedInvoiceWithViolations(t, super, tenant, d.entB, "CS-B-1", "rejected", `[{"rule_key":"cs-rB","severity":"error","message":"b"}]`)
	draftB := seedInvoiceAtStatus(t, super, tenant, d.entB, "CS-B-2", "draft")
	_, version := seedApprovalPolicy(t, super, tenant, false)
	seedApprovalRun(t, super, tenant, draftB, version, "rejected")
	d.admin = csMember(t, super, tenant, "admin")
	d.limited = csMember(t, super, tenant, "reviewer", d.entA)
	d.unassign = csMember(t, super, tenant, "preparer")
	return d
}

func TestClientScope_RollupCountsOnlyTheSet(t *testing.T) {
	d := newCSDash(t, "CS rollup limited")
	got, err := d.store.Rollup(d.limited)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 1 || got.Clients[0].EntityID != d.entA {
		t.Fatalf("limited Rollup clients = %+v, want only client A", got.Clients)
	}
	want := Counts{Draft: 1, Validated: 1}
	if got.Clients[0].Counts != want || got.Totals.Counts != want {
		t.Errorf("counts: client %+v, totals %+v, want %+v", got.Clients[0].Counts, got.Totals.Counts, want)
	}
	if got.Totals.NeedsAttention != 1 {
		t.Errorf("Totals.NeedsAttention = %d, want 1 (A's blocked draft; B's rejected-run draft adds nothing)", got.Totals.NeedsAttention)
	}
	if want := []RuleCount{{RuleKey: "cs-rA", Invoices: 1}}; !reflect.DeepEqual(got.TopViolations, want) {
		t.Errorf("TopViolations = %+v, want %+v", got.TopViolations, want)
	}
}

func TestClientScope_RollupOfAnUnassignedMemberIsEmpty(t *testing.T) {
	d := newCSDash(t, "CS rollup unassigned")
	got, err := d.store.Rollup(d.unassign)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if got.Clients == nil || len(got.Clients) != 0 {
		t.Errorf("Clients = %#v, want empty non-nil", got.Clients)
	}
	if got.TopViolations == nil || len(got.TopViolations) != 0 {
		t.Errorf("TopViolations = %#v, want empty non-nil", got.TopViolations)
	}
	if got.Totals.Counts != (Counts{}) || got.Totals.NeedsAttention != 0 || got.Totals.AwaitingApproval != 0 {
		t.Errorf("Totals = %+v, want every count 0", got.Totals)
	}
}

func TestClientScope_RollupAdminSeesEveryClient(t *testing.T) {
	d := newCSDash(t, "CS rollup admin")
	got, err := d.store.Rollup(d.admin)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(got.Clients) != 2 {
		t.Fatalf("admin Rollup clients = %d, want 2", len(got.Clients))
	}
	for _, c := range got.Clients {
		if c.EntityID == d.entB && c.NeedsAttention != 2 {
			t.Errorf("client B NeedsAttention = %d, want 2 (rejected + rejected run)", c.NeedsAttention)
		}
	}
	if got.Totals.NeedsAttention != 3 {
		t.Errorf("Totals.NeedsAttention = %d, want 3", got.Totals.NeedsAttention)
	}
}
