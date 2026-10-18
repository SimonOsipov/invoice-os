package tenancy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const clientAccessEvent = "membership.client_access_set"

func setIDs(ids ...string) ClientAccess { return ClientAccess{IDs: ids} }

var allClients = ClientAccess{All: true}

func (w invWorld) set(caller, target string, access ClientAccess) (MemberAccess, error) {
	return w.store.SetMemberClients(w.as(caller), target, access)
}

func (w invWorld) requireStored(t *testing.T, who, user string, wantAll bool, wantIDs ...string) {
	t.Helper()
	all, ids := storedAccess(t, w.super, w.tenant, user)
	slices.Sort(wantIDs)
	if wantIDs == nil {
		wantIDs = []string{}
	}
	if all != wantAll || !slices.Equal(ids, wantIDs) {
		t.Errorf("%s: stored flag=%v ids=%v, want flag=%v ids=%v", who, all, ids, wantAll, wantIDs)
	}
}

func (w invWorld) auditCountOf(t *testing.T, event string) int {
	t.Helper()
	var n int
	if err := w.super.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = $2`, w.tenant, event).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}

// snapshot is every membership flag and set of the tenants, as one comparable string.
func snapshot(t *testing.T, super *pgxpool.Pool, tenants ...string) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	for _, tn := range tenants {
		rows, err := super.Query(ctx,
			`SELECT m.user_id::text, m.all_clients, coalesce(string_agg(c.entity_id::text, ',' ORDER BY c.entity_id::text), '')
			   FROM memberships m LEFT JOIN membership_clients c ON c.tenant_id = m.tenant_id AND c.user_id = m.user_id
			  WHERE m.tenant_id = $1 GROUP BY m.user_id, m.all_clients ORDER BY m.user_id::text`, tn)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		for rows.Next() {
			var u, set string
			var all bool
			if err := rows.Scan(&u, &all, &set); err != nil {
				t.Fatalf("snapshot scan: %v", err)
			}
			fmt.Fprintf(&b, "%s|%s|%v|%s\n", tn, u, all, set)
		}
		rows.Close()
	}
	return b.String()
}

func requireSameAsList(t *testing.T, w invWorld, user string, got MemberAccess) {
	t.Helper()
	want := w.listAs(t, w.admin)[user]
	have := wireRows(t, []MemberAccess{got})[user]
	if len(want) == 0 || len(have) == 0 {
		t.Fatalf("no row to compare for %s (list %v, answer %v)", user, want, have)
	}
	for k, wv := range want {
		if string(have[k]) != string(wv) {
			t.Errorf("answer %s = %s, but the list answers %s", k, have[k], wv)
		}
	}
}

func TestClientAccess_SetReplacesTheWholeSet(t *testing.T) {
	w := newInvWorld(t, "client access set "+uuid.NewString()[:8], "Ada Admin")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")
	other := w.addMember(t, "reviewer", "active", "Rex Reviewer")
	e1, e2, e3 := seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant)
	giveClients(t, w.super, w.tenant, other, e1, e3)

	steps := []struct {
		name   string
		access ClientAccess
		all    bool
		ids    []string
	}{
		{"two clients", setIDs(e2, e1), false, []string{e1, e2}},
		{"a smaller set", setIDs(e2), false, []string{e2}},
		{"all clients deletes the rows", allClients, true, nil},
		{"a set clears the flag", setIDs(e1), false, []string{e1}},
		{"all again", allClients, true, nil},
		{"empty clears both", setIDs(), false, nil},
	}
	for _, s := range steps {
		got, err := w.set(w.admin, prep, s.access)
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		w.requireStored(t, s.name, prep, s.all, s.ids...)
		requireSameAsList(t, w, prep, got)
	}
	w.requireStored(t, "another member's set is not touched", other, false, e1, e3)
}

func TestClientAccess_SetAcceptsAnArchivedClient(t *testing.T) {
	w := newInvWorld(t, "client access archived "+uuid.NewString()[:8], "Ada Admin")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")
	e1 := seedEntity(t, w.super, w.tenant)
	if _, err := w.super.Exec(context.Background(), `UPDATE business_entities SET status = 'archived' WHERE id = $1`, e1); err != nil {
		t.Fatalf("archive: %v", err)
	}

	got, err := w.set(w.admin, prep, setIDs(e1))
	if err != nil {
		t.Fatalf("set an archived client: %v", err)
	}
	w.requireStored(t, "archived", prep, false, e1)
	requireAccess(t, "list", w.listAs(t, w.admin)[prep], quoted(e1))
	requireSameAsList(t, w, prep, got)
}

func TestClientAccess_SetRefusesAnEntityOfAnotherTenant(t *testing.T) {
	w := newInvWorld(t, "client access foreign A "+uuid.NewString()[:8], "Ada Admin")
	b := newInvWorld(t, "client access foreign B "+uuid.NewString()[:8], "Bea Admin")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")
	e1 := seedEntity(t, w.super, w.tenant)
	x := seedEntity(t, w.super, b.tenant)
	r := uuid.NewString()
	giveClients(t, w.super, w.tenant, prep, e1)
	before := snapshot(t, w.super, w.tenant, b.tenant)

	for name, access := range map[string]ClientAccess{
		"a list with a foreign client": setIDs(e1, x),
		"only a foreign client":        setIDs(x),
		"an unknown uuid":              setIDs(r),
	} {
		if _, err := w.set(w.admin, prep, access); !errors.Is(err, ErrUnknownClient) {
			t.Errorf("%s: err = %v, want ErrUnknownClient", name, err)
		}
	}
	if got := snapshot(t, w.super, w.tenant, b.tenant); got != before {
		t.Errorf("a refused write changed the stored access:\nbefore %s\nafter  %s", before, got)
	}
	if n := w.auditCountOf(t, clientAccessEvent); n != 0 {
		t.Errorf("%d %s rows after refused writes, want 0", n, clientAccessEvent)
	}

	// The flag survives a refused set: the whole write rolls back.
	giveAllClients(t, w.super, w.tenant, prep)
	if _, err := w.set(w.admin, prep, setIDs(x)); !errors.Is(err, ErrUnknownClient) {
		t.Fatalf("err = %v, want ErrUnknownClient", err)
	}
	if all, _ := storedAccess(t, w.super, w.tenant, prep); !all {
		t.Error("a refused set cleared the all_clients flag")
	}
	// Control: the same call with a client of this tenant succeeds.
	if _, err := w.set(w.admin, prep, setIDs(e1)); err != nil {
		t.Errorf("control write: %v", err)
	}
}

func TestClientAccess_SetRefusedForNonAdminForEveryTarget(t *testing.T) {
	w := newInvWorld(t, "client access nonadmin A "+uuid.NewString()[:8], "Ada Admin")
	b := newInvWorld(t, "client access nonadmin B "+uuid.NewString()[:8], "Bea Admin")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")
	rev := w.addMember(t, "reviewer", "active", "Rex Reviewer")
	member := w.addMember(t, "preparer", "active", "Mo Member")
	bMember := b.addMember(t, "preparer", "active", "Bo Member")
	e1 := seedEntity(t, w.super, w.tenant)
	giveClients(t, w.super, w.tenant, member, e1)
	giveAllClients(t, w.super, b.tenant, bMember)
	before := snapshot(t, w.super, w.tenant, b.tenant)

	for _, caller := range []string{prep, rev} {
		for name, target := range map[string]string{
			"an unknown uuid": uuid.NewString(), "another tenant's member": bMember,
			"self": caller, "the admin": w.admin, "a member": member,
		} {
			_, err := w.set(caller, target, allClients)
			if !errors.Is(err, ErrClientAccessNotPermitted) {
				t.Errorf("caller %s, target %s: err = %v, want ErrClientAccessNotPermitted", caller, name, err)
			}
		}
	}
	if got := snapshot(t, w.super, w.tenant, b.tenant); got != before {
		t.Errorf("a refused write changed the stored access:\nbefore %s\nafter  %s", before, got)
	}
	if n := w.auditCountOf(t, clientAccessEvent) + b.auditCountOf(t, clientAccessEvent); n != 0 {
		t.Errorf("%d %s rows, want 0", n, clientAccessEvent)
	}
	// Control: the admin's identical call succeeds, so the refusals were about the caller.
	if _, err := w.set(w.admin, member, allClients); err != nil {
		t.Errorf("control: admin write: %v", err)
	}
}

func TestClientAccess_SetBySuspendedAdminIsNotActiveMember(t *testing.T) {
	w := newInvWorld(t, "client access suspended "+uuid.NewString()[:8], "Ada Admin")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")
	e1 := seedEntity(t, w.super, w.tenant)
	giveClients(t, w.super, w.tenant, prep, e1)
	other := w.addMember(t, "admin", "active", "Bo Admin")
	if _, err := w.super.Exec(context.Background(), `UPDATE memberships SET status = 'suspended' WHERE user_id = $1`, w.admin); err != nil {
		t.Fatalf("suspend admin: %v", err)
	}
	before := snapshot(t, w.super, w.tenant)

	for name, caller := range map[string]string{"a suspended admin": w.admin, "no membership at all": uuid.NewString()} {
		_, err := w.set(caller, prep, allClients)
		if !errors.Is(err, db.ErrNotActiveMember) {
			t.Errorf("%s: err = %v, want db.ErrNotActiveMember", name, err)
		}
		if errors.Is(err, ErrClientAccessNotPermitted) {
			t.Errorf("%s: answered as a non-admin, want the seam's refusal", name)
		}
	}
	if got := snapshot(t, w.super, w.tenant); got != before {
		t.Errorf("a refused write changed the stored access:\nbefore %s\nafter  %s", before, got)
	}
	if _, err := w.set(other, prep, allClients); err != nil {
		t.Errorf("control: an active admin writes: %v", err)
	}
}

func TestClientAccess_SetTargets(t *testing.T) {
	w := newInvWorld(t, "client access targets "+uuid.NewString()[:8], "Ada Admin")
	b := newInvWorld(t, "client access targets B "+uuid.NewString()[:8], "Bea Admin")
	h := inHouseWorld(t)
	hMember := h.addMember(t, "preparer", "active", "Hal Member")
	bMember := b.addMember(t, "preparer", "active", "Bo Member")
	admin2 := w.addMember(t, "admin", "active", "Bo Admin")
	e1 := seedEntity(t, w.super, w.tenant)
	x := seedEntity(t, w.super, b.tenant)

	cases := []struct {
		name   string
		w      invWorld
		target string
		access ClientAccess
		want   error
	}{
		{"unknown uuid", w, uuid.NewString(), setIDs(e1), ErrMembershipNotFound},
		{"another tenant's member", w, bMember, setIDs(e1), ErrMembershipNotFound},
		{"an admin target", w, admin2, setIDs(e1), ErrClientAccessAdmin},
		{"the caller itself, an admin", w, w.admin, allClients, ErrClientAccessAdmin},
		{"an admin target beats a foreign client", w, admin2, setIDs(x), ErrClientAccessAdmin},
		{"an unknown target beats a foreign client", w, uuid.NewString(), setIDs(x), ErrMembershipNotFound},
		{"an in-house member", h, hMember, allClients, ErrClientAccessNotFirm},
		{"an in-house unknown uuid", h, uuid.NewString(), allClients, ErrClientAccessNotFirm},
		{"an in-house admin", h, h.admin, setIDs(), ErrClientAccessNotFirm},
	}
	before := snapshot(t, w.super, w.tenant, b.tenant, h.tenant)
	for _, tc := range cases {
		_, err := tc.w.set(tc.w.admin, tc.target, tc.access)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if got := snapshot(t, w.super, w.tenant, b.tenant, h.tenant); got != before {
		t.Errorf("a refused write changed the stored access:\nbefore %s\nafter  %s", before, got)
	}
	for _, tn := range []invWorld{w, b, h} {
		if n := tn.auditCountOf(t, clientAccessEvent); n != 0 {
			t.Errorf("tenant %s: %d %s rows, want 0", tn.tenant, n, clientAccessEvent)
		}
	}
	// Control: a firm member of the caller's tenant is writable.
	if _, err := w.set(w.admin, w.addMember(t, "reviewer", "active", "Rex"), setIDs(e1)); err != nil {
		t.Errorf("control: %v", err)
	}
}

func TestClientAccess_SetAuditsInTheSameTransaction(t *testing.T) {
	w := newInvWorld(t, "client access audit "+uuid.NewString()[:8], "Ada Admin")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")
	ents := []string{seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant)}
	slices.Sort(ents)
	e1, e2 := ents[0], ents[1]

	if _, err := w.set(w.admin, prep, setIDs(e2, e1)); err != nil {
		t.Fatalf("set: %v", err)
	}
	rows := auditPayloads(t, w.super, w.tenant, clientAccessEvent)
	if len(rows) != 1 {
		t.Fatalf("%d audit rows, want 1", len(rows))
	}
	keys := make([]string, 0, 3)
	for k := range rows[0] {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"all_clients", "client_ids", "user_id"}) {
		t.Errorf("payload keys = %v, want exactly all_clients, client_ids, user_id", keys)
	}
	if rows[0]["user_id"] != prep || rows[0]["all_clients"] != false {
		t.Errorf("payload = %v, want user_id %s and all_clients false", rows[0], prep)
	}
	if ids, ok := rows[0]["client_ids"].([]any); !ok || len(ids) != 2 || ids[0] != e1 || ids[1] != e2 {
		t.Errorf("client_ids = %v, want the sorted [%s %s]", rows[0]["client_ids"], e1, e2)
	}
	var actor string
	var entity *string
	if err := w.super.QueryRow(context.Background(),
		`SELECT actor, entity_id::text FROM audit_log WHERE tenant_id = $1 AND event = $2`, w.tenant, clientAccessEvent).Scan(&actor, &entity); err != nil {
		t.Fatalf("read the audit row: %v", err)
	}
	if actor != w.admin {
		t.Errorf("actor = %s, want the admin %s", actor, w.admin)
	}
	if entity != nil {
		t.Errorf("entity_id = %s, want NULL: the event is firm-wide", *entity)
	}

	// A non-HTTP caller's upper-case id is audited as the row's canonical id.
	if _, err := w.set(w.admin, strings.ToUpper(prep), allClients); err != nil {
		t.Fatalf("set all: %v", err)
	}
	rows = auditPayloads(t, w.super, w.tenant, clientAccessEvent)
	if len(rows) != 2 {
		t.Fatalf("%d audit rows after the second write, want 2", len(rows))
	}
	if rows[1]["all_clients"] != true {
		t.Errorf("all_clients = %v, want true", rows[1]["all_clients"])
	}
	if rows[1]["user_id"] != prep {
		t.Errorf("user_id = %v, want the canonical %s", rows[1]["user_id"], prep)
	}
	if ids, ok := rows[1]["client_ids"].([]any); !ok || len(ids) != 0 {
		t.Errorf("client_ids = %#v, want an empty array (not null)", rows[1]["client_ids"])
	}
}

func TestClientAccess_SetRollsBackWhenTheAuditFails(t *testing.T) {
	w := newInvWorld(t, "client access rollback "+uuid.NewString()[:8], "Ada Admin")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")
	flagged := w.addMember(t, "reviewer", "active", "Rex Reviewer")
	e1, e2 := seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant)
	giveClients(t, w.super, w.tenant, prep, e1)
	giveAllClients(t, w.super, w.tenant, flagged)
	drop := failAudit(t, w.super, w.tenant, clientAccessEvent)

	if _, err := w.set(w.admin, prep, setIDs(e2)); err == nil {
		t.Fatal("a set succeeded although its audit write fails")
	}
	w.requireStored(t, "set rolled back", prep, false, e1)
	if _, err := w.set(w.admin, flagged, setIDs(e1)); err == nil {
		t.Fatal("a set succeeded although its audit write fails")
	}
	w.requireStored(t, "flag rolled back", flagged, true)
	if _, err := w.set(w.admin, prep, allClients); err == nil {
		t.Fatal("an all write succeeded although its audit write fails")
	}
	w.requireStored(t, "all rolled back", prep, false, e1)

	drop()
	if _, err := w.set(w.admin, prep, setIDs(e2)); err != nil {
		t.Fatalf("write after the trigger is gone: %v", err)
	}
	w.requireStored(t, "retry", prep, false, e2)
}

func TestClientAccess_ConcurrentSetsDoNotMerge(t *testing.T) {
	w := newInvWorld(t, "client access race "+uuid.NewString()[:8], "Ada Admin")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")
	e := []string{seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant)}

	race := func(a, b ClientAccess) {
		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, 2)
		for i, acc := range []ClientAccess{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = w.set(w.admin, prep, acc)
			}()
		}
		close(start)
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("a concurrent write failed instead of waiting: %v", err)
			}
		}
	}
	sorted := func(ids ...string) []string { s := slices.Clone(ids); slices.Sort(s); return s }

	for i := range 15 {
		race(setIDs(e[0], e[1]), setIDs(e[2], e[3]))
		all, ids := storedAccess(t, w.super, w.tenant, prep)
		if all || (!slices.Equal(ids, sorted(e[0], e[1])) && !slices.Equal(ids, sorted(e[2], e[3]))) {
			t.Fatalf("round %d: stored flag=%v ids=%v, want exactly one submitted set", i, all, ids)
		}
	}
	// "All clients" against a set: never the flag together with rows.
	for i := range 15 {
		race(allClients, setIDs(e[0], e[1]))
		all, ids := storedAccess(t, w.super, w.tenant, prep)
		if !(all && len(ids) == 0) && !(!all && slices.Equal(ids, sorted(e[0], e[1]))) {
			t.Fatalf("round %d: stored flag=%v ids=%v, want exactly one submitted write", i, all, ids)
		}
	}
	if n := w.auditCountOf(t, clientAccessEvent); n != 60 {
		t.Errorf("%d audit rows, want 60: one per committed write", n)
	}
}

func TestClientAccess_SetLeavesTheSameUsersOtherTenantAlone(t *testing.T) {
	a := newInvWorld(t, "client access twin A "+uuid.NewString()[:8], "Ada Admin")
	b := newInvWorld(t, "client access twin B "+uuid.NewString()[:8], "Bea Admin")
	twin := a.addMember(t, "preparer", "active", "Tia Twin")
	name := "Tia Twin"
	seedIdentityMembership(t, a.super, b.tenant, twin, "preparer", "active", &name, strp(twin+"@twin.test"))
	ea, eb := seedEntity(t, a.super, a.tenant), seedEntity(t, a.super, b.tenant)
	giveClients(t, a.super, b.tenant, twin, eb)

	for _, acc := range []ClientAccess{setIDs(ea), allClients, setIDs()} {
		if _, err := a.set(a.admin, twin, acc); err != nil {
			t.Fatalf("set in A: %v", err)
		}
		if all, ids := storedAccess(t, a.super, b.tenant, twin); all || !slices.Equal(ids, []string{eb}) {
			t.Errorf("after %+v in A, B holds flag=%v ids=%v, want its own [%s]", acc, all, ids, eb)
		}
	}
	if _, err := a.set(a.admin, twin, setIDs(ea)); err != nil {
		t.Fatalf("set in A: %v", err)
	}
	requireAccess(t, "A lists the twin", a.listAs(t, a.admin)[twin], quoted(ea))
	requireAccess(t, "B lists the twin", b.listAs(t, b.admin)[twin], quoted(eb))
	if n := b.auditCountOf(t, clientAccessEvent); n != 0 {
		t.Errorf("tenant B holds %d %s rows, want 0", n, clientAccessEvent)
	}
}
