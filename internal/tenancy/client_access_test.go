package tenancy

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The read side asserts on the marshalled wire, so it compiles before MemberAccess exists.

var sixKeys = []string{"client_access", "display_name", "email", "role", "status", "user_id"}

// seedEntity inserts one active client of the tenant and returns its id.
func seedEntity(t *testing.T, super *pgxpool.Pool, tenant string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`, id, tenant, "client-"+id[:8]); err != nil {
		t.Fatalf("seed entity: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, id)
	})
	return id
}

// giveClients stores a set for a member: the flag false and exactly these rows.
func giveClients(t *testing.T, super *pgxpool.Pool, tenant, user string, entities ...string) {
	t.Helper()
	for _, e := range entities {
		if _, err := super.Exec(context.Background(),
			`INSERT INTO membership_clients (tenant_id, user_id, entity_id) VALUES ($1, $2, $3)`, tenant, user, e); err != nil {
			t.Fatalf("seed membership_clients: %v", err)
		}
	}
}

// giveAllClients sets the member's "All clients" flag.
func giveAllClients(t *testing.T, super *pgxpool.Pool, tenant, user string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`UPDATE memberships SET all_clients = true WHERE tenant_id = $1 AND user_id = $2`, tenant, user); err != nil {
		t.Fatalf("set all_clients: %v", err)
	}
}

// storedAccess reads a member's stored flag and set as the superuser, ids text-sorted.
func storedAccess(t *testing.T, super *pgxpool.Pool, tenant, user string) (all bool, ids []string) {
	t.Helper()
	ctx := context.Background()
	if err := super.QueryRow(ctx,
		`SELECT all_clients FROM memberships WHERE tenant_id = $1 AND user_id = $2`, tenant, user).Scan(&all); err != nil {
		t.Fatalf("read all_clients of %s: %v", user, err)
	}
	rows, err := super.Query(ctx,
		`SELECT entity_id::text FROM membership_clients WHERE tenant_id = $1 AND user_id = $2 ORDER BY entity_id::text`, tenant, user)
	if err != nil {
		t.Fatalf("read membership_clients: %v", err)
	}
	defer rows.Close()
	ids = []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan membership_clients: %v", err)
		}
		ids = append(ids, id)
	}
	return all, ids
}

// wireRows marshals the rows exactly as GET /v1/memberships does and keys them by user_id.
func wireRows(t *testing.T, rows any) map[string]map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal memberships: %v", err)
	}
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode memberships %s: %v", raw, err)
	}
	out := map[string]map[string]json.RawMessage{}
	for _, row := range list {
		var id string
		if err := json.Unmarshal(row["user_id"], &id); err != nil {
			t.Fatalf("row without a user_id: %s", raw)
		}
		out[id] = row
	}
	return out
}

func (w invWorld) listAs(t *testing.T, caller string) map[string]map[string]json.RawMessage {
	t.Helper()
	rows, err := w.store.ListMemberships(w.as(caller))
	if err != nil {
		t.Fatalf("ListMemberships as %s: %v", caller, err)
	}
	return wireRows(t, rows)
}

// requireAccess asserts the six keys and the raw client_access of one row.
func requireAccess(t *testing.T, who string, row map[string]json.RawMessage, want string) {
	t.Helper()
	if row == nil {
		t.Errorf("%s: no row in the list", who)
		return
	}
	got := make([]string, 0, len(row))
	for k := range row {
		got = append(got, k)
	}
	slices.Sort(got)
	if !slices.Equal(got, sixKeys) {
		t.Errorf("%s: keys = %v, want %v", who, got, sixKeys)
	}
	raw, ok := row["client_access"]
	if !ok {
		t.Errorf("%s: client_access is missing, want %s", who, want)
		return
	}
	if string(raw) != want {
		t.Errorf("%s: client_access = %s, want %s", who, raw, want)
	}
}

func inHouseWorld(t *testing.T) invWorld {
	t.Helper()
	super, app := dbTestPools(t)
	w := invWorld{super: super, app: app, store: NewStore(app)}
	w.tenant = seedKindedTenant(t, super, "client access in-house "+uuid.NewString()[:8], "in_house")
	w.admin = w.addMember(t, "admin", "active", "In-house admin")
	return w
}

func quoted(ids ...string) string {
	if len(ids) == 0 {
		return "[]"
	}
	return `["` + strings.Join(ids, `","`) + `"]`
}

func TestClientAccess_ListDefaultsToNoClientsForAFirmMember(t *testing.T) {
	w := newInvWorld(t, "client access default "+uuid.NewString()[:8], "Ada Admin")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")
	rev := w.addMember(t, "reviewer", "active", "Rex Reviewer")

	rows := w.listAs(t, w.admin)
	if len(rows) != 3 {
		t.Fatalf("%d rows, want 3", len(rows))
	}
	requireAccess(t, "admin", rows[w.admin], `"all"`)
	requireAccess(t, "preparer", rows[prep], `[]`)
	requireAccess(t, "reviewer", rows[rev], `[]`)
}

func TestClientAccess_ListReadsTheFlagAndTheSortedSet(t *testing.T) {
	w := newInvWorld(t, "client access sorted "+uuid.NewString()[:8], "Ada Admin")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")
	rev := w.addMember(t, "reviewer", "active", "Rex Reviewer")
	admin2 := w.addMember(t, "admin", "active", "Bo Admin")
	// Insertion order differs from text order, and three ids tell sorting from a coincidence.
	ents := []string{seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant)}
	slices.Sort(ents)
	lo, mid, hi := ents[0], ents[1], ents[2]
	giveClients(t, w.super, w.tenant, prep, mid, hi, lo)
	giveAllClients(t, w.super, w.tenant, rev)
	giveClients(t, w.super, w.tenant, admin2, lo) // the role rule wins over a stored row

	rows := w.listAs(t, w.admin)
	requireAccess(t, "preparer", rows[prep], quoted(lo, mid, hi))
	requireAccess(t, "reviewer", rows[rev], `"all"`)
	requireAccess(t, "second admin", rows[admin2], `"all"`)
	requireAccess(t, "first admin", rows[w.admin], `"all"`)
}

func TestClientAccess_ListAnswersNullInAnInHouseWorkspace(t *testing.T) {
	w := inHouseWorld(t)
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")
	// Data stored against an in-house member must not surface: the kind decides.
	giveAllClients(t, w.super, w.tenant, prep)

	rows := w.listAs(t, w.admin)
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2", len(rows))
	}
	requireAccess(t, "admin", rows[w.admin], `null`)
	requireAccess(t, "preparer", rows[prep], `null`)
}

func TestClientAccess_ListScopesOtherRowsForANonAdminCaller(t *testing.T) {
	w := newInvWorld(t, "client access scope "+uuid.NewString()[:8], "Ada Admin")
	p := w.addMember(t, "preparer", "active", "Pia Preparer")
	r := w.addMember(t, "reviewer", "active", "Rex Reviewer")
	e1 := seedEntity(t, w.super, w.tenant)
	giveClients(t, w.super, w.tenant, p, e1)
	giveAllClients(t, w.super, w.tenant, r)

	asP := w.listAs(t, p)
	if len(asP) != 3 {
		t.Fatalf("as preparer: %d rows, want 3 (a non-admin still lists everyone)", len(asP))
	}
	requireAccess(t, "as P: P", asP[p], quoted(e1))
	requireAccess(t, "as P: admin", asP[w.admin], `null`)
	requireAccess(t, "as P: R", asP[r], `null`)

	asR := w.listAs(t, r)
	requireAccess(t, "as R: R", asR[r], `"all"`)
	requireAccess(t, "as R: admin", asR[w.admin], `null`)
	requireAccess(t, "as R: P", asR[p], `null`)

	asAdmin := w.listAs(t, w.admin)
	requireAccess(t, "as admin: P", asAdmin[p], quoted(e1))
	requireAccess(t, "as admin: R", asAdmin[r], `"all"`)
	requireAccess(t, "as admin: admin", asAdmin[w.admin], `"all"`)

	// A member with no access reads its own empty set, not null.
	q := w.addMember(t, "preparer", "active", "Quin Preparer")
	asQ := w.listAs(t, q)
	requireAccess(t, "as Q: Q", asQ[q], `[]`)
	requireAccess(t, "as Q: P", asQ[p], `null`)
}

func TestClientAccess_PatchAnswersTheTargetsStoredAccess(t *testing.T) {
	w := newInvWorld(t, "client access patch "+uuid.NewString()[:8], "Ada Admin")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")
	rev := w.addMember(t, "reviewer", "active", "Rex Reviewer")
	e1 := seedEntity(t, w.super, w.tenant)
	giveClients(t, w.super, w.tenant, prep, e1)
	giveAllClients(t, w.super, w.tenant, rev)

	for _, tc := range []struct {
		name, user, status, want string
	}{
		{"suspend a preparer holding a set", prep, "suspended", quoted(e1)},
		{"re-suspend: the no-op path answers the same key", prep, "suspended", quoted(e1)},
		{"reactivate", prep, "active", quoted(e1)},
		{"suspend a reviewer holding the flag", rev, "suspended", `"all"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := w.store.SetMembershipStatus(w.adminCtx(), tc.user, tc.status)
			if err != nil {
				t.Fatalf("SetMembershipStatus: %v", err)
			}
			requireAccess(t, tc.name, wireRows(t, []any{got})[tc.user], tc.want)
		})
	}
}
