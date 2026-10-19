package customrule_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/customrule"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// csRules is a firm tenant with clients A and B, an admin and a reviewer limited to A.
type csRules struct {
	ws         workspace
	entA, entB string
	limited    context.Context
	store      *customrule.Store
}

func newCSRules(t *testing.T, super, app *pgxpool.Pool, label string) csRules {
	t.Helper()
	x := csRules{ws: seedWorkspace(t, super, label)}
	x.entA = seedEntity(t, super, x.ws.id, "A")
	x.entB = seedEntity(t, super, x.ws.id, "B")
	user := uuid.NewString()
	for _, q := range []string{
		`INSERT INTO memberships (tenant_id, user_id, role, status, all_clients) VALUES ($1, $2, 'reviewer', 'active', false)`,
		`INSERT INTO membership_clients (tenant_id, user_id, entity_id) VALUES ($1, $2, '` + x.entA + `')`,
	} {
		if _, err := super.Exec(context.Background(), q, x.ws.id, user); err != nil {
			t.Fatalf("seed limited reviewer: %v", err)
		}
	}
	x.limited = auth.WithIdentity(context.Background(), auth.Identity{Subject: user, Role: "authenticated", TenantID: x.ws.id})
	x.store = customrule.NewStore(app, (&fakeCheck{}).fn)
	return x
}

func (x csRules) listed(t *testing.T, ctx context.Context) []string {
	t.Helper()
	rules, err := x.store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return messages(rules)
}

func TestClientScope_RuleListShowsFirmWideAndOwnClientRules(t *testing.T) {
	super, app := dbTestPools(t)
	x := newCSRules(t, super, app, "CS rules list")
	insertRev(t, super, seedRev{Tenant: x.ws.id, RuleID: uuid.NewString(), Revision: 1, Severity: "error", Message: "firm", Enabled: true})
	insertRev(t, super, seedRev{Tenant: x.ws.id, RuleID: uuid.NewString(), Revision: 1, Entity: &x.entA, Severity: "error", Message: "client-a", Enabled: true})
	insertRev(t, super, seedRev{Tenant: x.ws.id, RuleID: uuid.NewString(), Revision: 1, Entity: &x.entB, Severity: "error", Message: "client-b", Enabled: true})

	if got, want := x.listed(t, x.limited), []string{"client-a@1", "firm@1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("limited reviewer List = %v, want %v", got, want)
	}
	if got, want := x.listed(t, x.ws.admin), []string{"client-a@1", "client-b@1", "firm@1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("admin List = %v, want %v", got, want)
	}
}

func TestClientScope_ARuleMovedToAnotherClientLeavesTheList(t *testing.T) {
	super, app := dbTestPools(t)
	x := newCSRules(t, super, app, "CS rules moved")
	rule := uuid.NewString()
	insertRev(t, super, seedRev{Tenant: x.ws.id, RuleID: rule, Revision: 1, Entity: &x.entA, Severity: "error", Message: "moved", Enabled: true})
	insertRev(t, super, seedRev{Tenant: x.ws.id, RuleID: rule, Revision: 2, Entity: &x.entB, Severity: "error", Message: "moved", Enabled: true})

	if got := x.listed(t, x.limited); len(got) != 0 {
		t.Errorf("limited reviewer List = %v, want none", got)
	}
	if got := currentTx(t, app, x.limited, []string{x.entA}); len(got) != 0 {
		t.Errorf("CurrentTx([A]) = %v, want none", messages(got))
	}
	if got, want := x.listed(t, x.ws.admin), []string{"moved@2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("admin List = %v, want %v", got, want)
	}
}

func TestClientScope_ARuleMovedFromFirmWideToAnotherClientLeavesTheList(t *testing.T) {
	super, app := dbTestPools(t)
	x := newCSRules(t, super, app, "CS rules firm to client")
	rule := uuid.NewString()
	insertRev(t, super, seedRev{Tenant: x.ws.id, RuleID: rule, Revision: 1, Severity: "error", Message: "was-firm", Enabled: true})
	insertRev(t, super, seedRev{Tenant: x.ws.id, RuleID: rule, Revision: 2, Entity: &x.entB, Severity: "error", Message: "was-firm", Enabled: true})

	if got := x.listed(t, x.limited); len(got) != 0 {
		t.Errorf("limited reviewer List = %v, want none", got)
	}
}

func TestClientScope_ARuleMovedIntoTheSetOrToFirmWideAppears(t *testing.T) {
	super, app := dbTestPools(t)
	x := newCSRules(t, super, app, "CS rules moved in")
	into, wide := uuid.NewString(), uuid.NewString()
	insertRev(t, super, seedRev{Tenant: x.ws.id, RuleID: into, Revision: 1, Entity: &x.entB, Severity: "error", Message: "into-a", Enabled: true})
	insertRev(t, super, seedRev{Tenant: x.ws.id, RuleID: into, Revision: 2, Entity: &x.entA, Severity: "error", Message: "into-a", Enabled: true})
	insertRev(t, super, seedRev{Tenant: x.ws.id, RuleID: wide, Revision: 1, Entity: &x.entB, Severity: "error", Message: "to-firm", Enabled: true})
	insertRev(t, super, seedRev{Tenant: x.ws.id, RuleID: wide, Revision: 2, Severity: "error", Message: "to-firm", Enabled: true})

	if got, want := x.listed(t, x.limited), []string{"into-a@2", "to-firm@2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("limited reviewer List = %v, want %v", got, want)
	}
}

// A custom-rule write is admin-only and an admin is never limited, so no write meets a client
// outside the set: the entity check answers one 400 for unknown, foreign and any other id.
func TestClientScope_AnAdminHoldingOneClientWritesForAnyClientAndALimitedMemberIsRefusedFirst(t *testing.T) {
	super, app := dbTestPools(t)
	x := newCSRules(t, super, app, "CS rules write")
	adminUser := uuid.NewString()
	for _, q := range []string{
		`INSERT INTO memberships (tenant_id, user_id, role, status, all_clients) VALUES ($1, $2, 'admin', 'active', false)`,
		`INSERT INTO membership_clients (tenant_id, user_id, entity_id) VALUES ($1, $2, '` + x.entA + `')`,
	} {
		if _, err := super.Exec(context.Background(), q, x.ws.id, adminUser); err != nil {
			t.Fatalf("seed admin: %v", err)
		}
	}
	admin := auth.WithIdentity(context.Background(), auth.Identity{Subject: adminUser, Role: "authenticated", TenantID: x.ws.id})

	if _, err := x.store.Create(admin, input(&x.entB, "for-b")); err != nil {
		t.Errorf("admin holding only client A, Create for client B: %v, want success", err)
	}
	if _, err := x.store.Create(x.limited, input(&x.entB, "limited-b")); !errors.Is(err, customrule.ErrNotPermitted) {
		t.Errorf("reviewer limited to A, Create for client B: err = %v, want ErrNotPermitted", err)
	}
	unknown := uuid.NewString()
	_, errUnknown := x.store.Create(admin, input(&unknown, "unknown"))
	if ie, ok := asInvalid(errUnknown); !ok || ie.Msg != entityMsg {
		t.Errorf("admin, Create for an unknown client: err = %v, want InvalidError %q", errUnknown, entityMsg)
	}
}
