// AUDIT-13-05: an account event is readable by an admin and by the member it is about.
// Reads go through Store.List as identities built from seeded memberships. Helpers use an ae* prefix.
package audit_test

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	aeSignedIn  = "account.signed_in"
	aeSignedOut = "account.signed_out"
)

// aeSix is the six names as a literal, so the filter does not read the list under test.
var aeSix = []string{
	"account.signed_in", "account.sign_in_failed", "account.sign_in_blocked",
	"account.signed_out", "account.password_reset_requested", "account.password_reset_completed",
}

// aeMeta is what the Go-side oracle filters on.
type aeMeta struct {
	actor, event string
	workspace    bool // entity_id is NULL
}

type aeWorld struct {
	p    pageFixture
	meta map[int64]aeMeta
}

func (w *aeWorld) add(t *testing.T, f *fixture, row filtRow) int64 {
	t.Helper()
	s := filtInsert(t, f, w.p, []filtRow{row})[0]
	if w.meta == nil {
		w.meta = map[int64]aeMeta{}
	}
	w.meta[s.id] = aeMeta{actor: row.actor, event: row.event, workspace: s.entity == nil}
	return s.id
}

func aePayload(device string) string {
	return fmt.Sprintf(`{"ip_address":"198.51.100.7","device":%q,"occurred_at":"2026-10-29T08:00:00Z"}`, device)
}

// account seeds an account row; the oracle needs it workspace-level, as the mover writes it.
func (w *aeWorld) account(t *testing.T, f *fixture, user, event, device string, age int) int64 {
	t.Helper()
	id := w.add(t, f, filtRow{event: event, actor: user, payload: aePayload(device), ageSeconds: age})
	if !w.meta[id].workspace {
		t.Fatalf("account row %d was attributed to a company: the trigger changed and the oracle is void", id)
	}
	return id
}

// client seeds one client with one invoice.created row, attributed to it.
func (w *aeWorld) client(t *testing.T, f *fixture, name string, age int) (entity string, row int64) {
	t.Helper()
	entity = pageSeedEntity(t, f, w.p, name)
	inv := pageSeedInvoice(t, f, w.p, entity)
	row = w.add(t, f, filtRow{event: "invoice.created", actor: "ae-system",
		payload: fmt.Sprintf(`{"id":%q,"invoice_number":%q}`, inv, "INV-"+inv[:8]), ageSeconds: age})
	if w.meta[row].workspace {
		t.Fatalf("client row %d has no entity_id: the trigger changed and the oracle is void", row)
	}
	return entity, row
}

func aeKind(t *testing.T, super *pgxpool.Pool, tenant, kind string) {
	t.Helper()
	if _, err := super.Exec(context.Background(), `UPDATE tenants SET kind = $2 WHERE id = $1`, tenant, kind); err != nil {
		t.Fatalf("set tenant kind %s: %v", kind, err)
	}
}

func aeName(t *testing.T, super *pgxpool.Pool, tenant, user, name string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`UPDATE memberships SET display_name = $3 WHERE tenant_id = $1 AND user_id = $2`, tenant, user, name); err != nil {
		t.Fatalf("set display name: %v", err)
	}
}

// aeFirm is a firm tenant: admin A, limited L (client C1), All-clients N and unassigned U. Each
// non-admin member has a signed-in and a signed-out row; A's rows carry Chrome, the others Safari.
type aeFirm struct {
	aeWorld
	ids        map[string]string // member -> user id
	ctx        map[string]context.Context
	acc        map[string][]int64 // member -> [signed_in, signed_out]
	c1, c2, ws int64
	c1Entity   string
}

func aeNewFirm(t *testing.T, f *fixture) *aeFirm {
	t.Helper()
	super := csSuper(t)
	w := &aeFirm{aeWorld: aeWorld{p: pageSeedTenant(t, f)}, ids: map[string]string{}, ctx: map[string]context.Context{}, acc: map[string][]int64{}}
	w.c1Entity, w.c1 = w.client(t, f, "Alpha Holdings Ltd", 200)
	_, w.c2 = w.client(t, f, "Bravo Trading Ltd", 190)
	for _, m := range []struct {
		name, role, device string
		all                bool
		clients            []string
	}{
		{"A", "admin", "Chrome on macOS", false, nil},
		{"L", "reviewer", "Safari on iOS", false, []string{w.c1Entity}},
		{"N", "preparer", "Safari on iOS", true, nil},
		{"U", "preparer", "Safari on iOS", false, nil},
	} {
		w.ids[m.name] = uuid.NewString()
		w.ctx[m.name] = csMemberUser(t, super, w.ids[m.name], w.p.tenant, m.role, m.all, m.clients...)
		aeName(t, super, w.p.tenant, w.ids[m.name], "Member "+m.name+" Okafor")
		w.acc[m.name] = []int64{
			w.account(t, f, w.ids[m.name], aeSignedIn, m.device, 600),
			w.account(t, f, w.ids[m.name], aeSignedOut, m.device, 595),
		}
	}
	w.ws = w.add(t, f, filtRow{event: "membership.removed", actor: w.ids["A"], payload: `{}`, ageSeconds: 180})
	if !w.meta[w.ws].workspace {
		t.Fatal("membership.removed was attributed to a company: the oracle is void")
	}
	return w
}

// want is what each member may read, from the FIRMS-04 rules plus the account rule.
func (w *aeFirm) want() map[string][]int64 {
	all := slices.Concat(w.acc["A"], w.acc["L"], w.acc["N"], w.acc["U"], []int64{w.c1, w.c2, w.ws})
	return map[string][]int64{
		"A": all,
		"L": slices.Concat(w.acc["L"], []int64{w.c1}),
		"N": slices.Concat(w.acc["N"], []int64{w.c1, w.c2, w.ws}),
		"U": w.acc["U"],
	}
}

// restrict applies the filter's own predicates to a visible set: the oracle for a filtered view.
func (w *aeWorld) restrict(ids []int64, fl audit.Filter) []int64 {
	var out []int64
	for _, id := range ids {
		m := w.meta[id]
		if len(fl.Events) > 0 && !slices.Contains(fl.Events, m.event) {
			continue
		}
		if len(fl.Actors) > 0 && !slices.Contains(fl.Actors, m.actor) {
			continue
		}
		if fl.Company.Mode() == audit.ModeWorkspaceOnly && !m.workspace {
			continue
		}
		out = append(out, id)
	}
	return out
}

// aeWalk pages as the app's CSV export does: the same endpoint, the same filter, until no page remains.
func aeWalk(t *testing.T, f *fixture, ctx context.Context, fl audit.Filter) []int64 {
	t.Helper()
	var ids []int64
	for page := 0; page < 40; page++ {
		got := csList(t, f, ctx, fl)
		for _, e := range got.Events {
			var id int64
			if _, err := fmt.Sscan(e.ID, &id); err != nil {
				t.Fatalf("event id %q: %v", e.ID, err)
			}
			ids = append(ids, id)
		}
		if !got.Page.HasMore {
			return ids
		}
		cur, err := audit.DecodeCursor(*got.Page.NextCursor)
		if err != nil {
			t.Fatalf("decode cursor: %v", err)
		}
		fl.Cursor = &cur
	}
	t.Fatal("export walk did not end")
	return nil
}

func aeSum(fs []audit.Facet) (n int) {
	for _, b := range fs {
		n += b.Count
	}
	return n
}

func aeFacetValues(fs []audit.Facet) []string {
	var out []string
	for _, b := range fs {
		if b.Value != nil {
			out = append(out, *b.Value)
		}
	}
	return out
}

// aeMatrix lists and exports every filter for every member and compares each with the oracle.
func aeMatrix(t *testing.T, f *fixture, w *aeWorld, users map[string]string, ctxs map[string]context.Context, want map[string][]int64) {
	t.Helper()
	filters := map[string]audit.Filter{
		"all":           {},
		"six events":    {Events: aeSix},
		"signed_in":     {Events: []string{aeSignedIn}},
		"workspace":     {Company: audit.WorkspaceOnly()},
		"six+workspace": {Events: aeSix, Company: audit.WorkspaceOnly()},
	}
	for name, id := range users {
		filters["actor "+name] = audit.Filter{Actors: []string{id}}
		filters["actor "+name+" signed_in"] = audit.Filter{Actors: []string{id}, Events: []string{aeSignedIn}}
	}
	for member, ctx := range ctxs {
		if len(want[member]) == 0 {
			t.Fatalf("%s: the oracle expects no rows at all, so nothing proves the member reads anything", member)
		}
		for name, fl := range filters {
			label := member + ", " + name
			exp := w.restrict(want[member], fl)
			list := fl
			list.Limit = 50
			got := csList(t, f, ctx, list)
			csAssertRows(t, label+" (list)", got, exp)
			if len(exp) == 0 && got.LogIsEmpty {
				t.Errorf("%s: log_is_empty = true, want false: the filter emptied the page, not the log", label)
			}
			page := fl
			page.Limit = 3
			page.From, page.To = time.Now().Add(-2*time.Hour), time.Now().Add(time.Minute)
			exported := aeWalk(t, f, ctx, page)
			if !slices.Equal(csSorted(exported), csSorted(exp)) {
				t.Errorf("%s (export): walked rows %v, want %v", label, csSorted(exported), csSorted(exp))
			}
		}
		all := csList(t, f, ctx, audit.Filter{Limit: 50})
		if n := aeSum(all.Facets.Event); n != len(want[member]) {
			t.Errorf("%s: event facet sums to %d, want %d", member, n, len(want[member]))
		}
		if n := aeSum(all.Facets.Actor); n != len(want[member]) {
			t.Errorf("%s: actor facet sums to %d, want %d", member, n, len(want[member]))
		}
	}
}

// Every caller class of a firm: admin, limited, All clients and unassigned (also "a different non-admin").
func TestAccountEvents_FirmCallersReadOnlyWhatTheyMay(t *testing.T) {
	f := requireFixture(t)
	w := aeNewFirm(t, f)
	aeMatrix(t, f, &w.aeWorld, w.ids, w.ctx, w.want())
}

// An in-house tenant has no client scope: only the account rule separates admin from non-admin.
func TestAccountEvents_InHouseCallersReadOnlyWhatTheyMay(t *testing.T) {
	f := requireFixture(t)
	super := csSuper(t)
	w := &aeWorld{p: pageSeedTenant(t, f)}
	aeKind(t, super, w.p.tenant, "in_house")
	_, client := w.client(t, f, "House Client", 200)
	ids := map[string]string{"A": uuid.NewString(), "P": uuid.NewString(), "Q": uuid.NewString()}
	ctxs := map[string]context.Context{}
	acc := map[string][]int64{}
	for name, role := range map[string]string{"A": "admin", "P": "preparer", "Q": "preparer"} {
		ctxs[name] = csMemberUser(t, super, ids[name], w.p.tenant, role, false)
		acc[name] = []int64{
			w.account(t, f, ids[name], aeSignedIn, "Safari on iOS", 600),
			w.account(t, f, ids[name], aeSignedOut, "Safari on iOS", 595),
		}
	}
	ws := w.add(t, f, filtRow{event: "membership.suspended", actor: ids["A"], payload: `{}`, ageSeconds: 180})

	aeMatrix(t, f, w, ids, ctxs, map[string][]int64{
		"A": slices.Concat(acc["A"], acc["P"], acc["Q"], []int64{client, ws}),
		"P": slices.Concat(acc["P"], []int64{client, ws}),
		"Q": slices.Concat(acc["Q"], []int64{client, ws}),
	})
}

func TestAccountEvents_AdminSeesEveryMembersAccountRows(t *testing.T) {
	f := requireFixture(t)
	super := csSuper(t)
	w := &aeWorld{p: pageSeedTenant(t, f)}
	a, p := uuid.NewString(), uuid.NewString()
	admin := csMemberUser(t, super, a, w.p.tenant, "admin", false)
	prep := csMemberUser(t, super, p, w.p.tenant, "preparer", true)
	w.account(t, f, a, aeSignedIn, "Chrome on macOS", 300)
	w.account(t, f, p, aeSignedIn, "Safari on iOS", 290)
	w.client(t, f, "Alpha Holdings Ltd", 280)

	got := csList(t, f, admin, audit.Filter{Limit: 50})

	if len(got.Events) != 3 || got.Total != 3 {
		t.Errorf("admin read %d rows, total %d, want 3 and 3", len(got.Events), got.Total)
	}
	if len(got.Facets.Event) == 0 {
		t.Fatal("event facet is empty")
	}
	for _, b := range got.Facets.Event {
		if b.Value != nil && *b.Value == aeSignedIn && b.Count != 2 {
			t.Errorf("event facet %s count = %d, want 2", aeSignedIn, b.Count)
		}
	}
	if actors := aeFacetValues(got.Facets.Actor); !slices.Contains(actors, a) || !slices.Contains(actors, p) {
		t.Errorf("actor facet holds %v, want %s and %s", actors, a, p)
	}
	// Paired control: the same rows read as a non-admin are fewer, so the admin's 3 is the role's doing.
	if n := csList(t, f, prep, audit.Filter{Limit: 50}).Total; n != 2 {
		t.Errorf("non-admin control read %d rows, want 2 (its own account row and the invoice row)", n)
	}
}

func TestAccountEvents_InHousePreparerSeesOnlyOwn(t *testing.T) {
	f := requireFixture(t)
	super := csSuper(t)
	w := &aeWorld{p: pageSeedTenant(t, f)}
	aeKind(t, super, w.p.tenant, "in_house")
	a, p := uuid.NewString(), uuid.NewString()
	csMemberUser(t, super, a, w.p.tenant, "admin", false)
	prep := csMemberUser(t, super, p, w.p.tenant, "preparer", false)
	w.account(t, f, a, aeSignedIn, "Chrome on macOS", 300)
	own := w.account(t, f, p, aeSignedIn, "Safari on iOS", 290)
	_, inv := w.client(t, f, "House Client", 280)

	got := csList(t, f, prep, audit.Filter{Limit: 50})

	csAssertRows(t, "in-house preparer", got, []int64{own, inv})
	actors := aeFacetValues(got.Facets.Actor)
	if !slices.Contains(actors, p) {
		t.Errorf("actor facet holds %v, want the preparer %s", actors, p)
	}
	if slices.Contains(actors, a) {
		t.Errorf("actor facet holds the admin %s: a facet is counted from rows the member cannot read", a)
	}
}

func TestAccountEvents_AllClientsMemberSeesOnlyOwn(t *testing.T) {
	f := requireFixture(t)
	w := aeNewFirm(t, f)

	got := csList(t, f, w.ctx["N"], audit.Filter{Limit: 50})

	csAssertRows(t, "All clients member", got, w.want()["N"])
	for _, id := range w.acc["A"] {
		for _, e := range got.Events {
			if e.ID == fmt.Sprint(id) {
				t.Errorf("All clients member read the admin's account row %d", id)
			}
		}
	}
}

func TestAccountEvents_LimitedMemberSeesOwnAndClientRows(t *testing.T) {
	f := requireFixture(t)
	w := aeNewFirm(t, f)

	got := csList(t, f, w.ctx["L"], audit.Filter{Limit: 50})

	csAssertRows(t, "limited member", got, slices.Concat(w.acc["L"], []int64{w.c1}))
	if len(got.Events) == 0 {
		t.Fatal("limited member read nothing")
	}
	for _, e := range got.Events {
		if e.Event == "membership.removed" {
			t.Errorf("limited member read the workspace row %s", e.ID)
		}
	}
}

func TestAccountEvents_UnassignedMemberSeesOnlyOwn(t *testing.T) {
	f := requireFixture(t)
	w := aeNewFirm(t, f)

	got := csList(t, f, w.ctx["U"], audit.Filter{Limit: 50})

	csAssertRows(t, "unassigned member", got, w.acc["U"])
	if got.LogIsEmpty {
		t.Error("unassigned member with account rows: log_is_empty = true, want false")
	}
}

func TestAccountEvents_WorkspaceFilterForALimitedMember(t *testing.T) {
	f := requireFixture(t)
	w := aeNewFirm(t, f)
	ws := audit.Filter{Limit: 50, Company: audit.WorkspaceOnly()}

	csAssertRows(t, "limited, workspace", csList(t, f, w.ctx["L"], ws), w.acc["L"])
	csAssertRows(t, "All clients, workspace", csList(t, f, w.ctx["N"], ws), slices.Concat(w.acc["N"], []int64{w.ws}))
	if admin := csList(t, f, w.ctx["A"], ws); admin.Total != 9 {
		t.Errorf("admin control, workspace: total = %d, want 9 (8 account rows and the workspace row)", admin.Total)
	}
}

func TestAccountEvents_EventFilterStaysSelfOnly(t *testing.T) {
	f := requireFixture(t)
	w := aeNewFirm(t, f)
	byEvent := audit.Filter{Limit: 50, Events: []string{aeSignedIn}}

	csAssertRows(t, "N, event=signed_in", csList(t, f, w.ctx["N"], byEvent), w.acc["N"][:1])
	if admin := csList(t, f, w.ctx["A"], byEvent); admin.Total != 4 {
		t.Errorf("admin control, event=signed_in: total = %d, want 4", admin.Total)
	}

	// Naming the member or the event must not open the row: N asks for A's rows by actor.
	byActor := csList(t, f, w.ctx["N"], audit.Filter{Limit: 50, Actors: []string{w.ids["A"]}, Events: []string{aeSignedIn}})
	if byActor.Total != 0 || len(byActor.Events) != 0 {
		t.Errorf("N filtered by A's id and event: read %d rows, total %d, want none", len(byActor.Events), byActor.Total)
	}
	if byActor.LogIsEmpty {
		t.Error("N's log is not empty: the filter emptied the page, so log_is_empty must be false")
	}
}

// A's name and A's device are in A's rows; neither reaches them from another member. The workspace row
// authored by A is not an account row, so a member who may read it still finds it by A's name.
func TestAccountEvents_SearchCannotReachAnotherMembersRow(t *testing.T) {
	f := requireFixture(t)
	w := aeNewFirm(t, f)

	byName := csList(t, f, w.ctx["N"], audit.Filter{Limit: 50, Q: "Member A Okafor"})
	csAssertRows(t, "N, q=A's name", byName, []int64{w.ws})
	csAssertRows(t, "N, q=Chrome", csList(t, f, w.ctx["N"], audit.Filter{Limit: 50, Q: "Chrome"}), nil)
	csAssertRows(t, "N, q=Safari", csList(t, f, w.ctx["N"], audit.Filter{Limit: 50, Q: "Safari"}), w.acc["N"])
	csAssertRows(t, "L, q=A's name", csList(t, f, w.ctx["L"], audit.Filter{Limit: 50, Q: "Member A Okafor"}), nil)
	csAssertRows(t, "L, q=Chrome", csList(t, f, w.ctx["L"], audit.Filter{Limit: 50, Q: "Chrome"}), nil)

	csAssertRows(t, "admin, q=Chrome", csList(t, f, w.ctx["A"], audit.Filter{Limit: 50, Q: "Chrome"}), w.acc["A"])
}

func TestAccountEvents_CursorPagesStaySelfOnly(t *testing.T) {
	f := requireFixture(t)
	super := csSuper(t)
	w := &aeWorld{p: pageSeedTenant(t, f)}
	aeKind(t, super, w.p.tenant, "in_house")
	a, p := uuid.NewString(), uuid.NewString()
	admin := csMemberUser(t, super, a, w.p.tenant, "admin", false)
	prep := csMemberUser(t, super, p, w.p.tenant, "preparer", false)
	var own []int64
	for i := 0; i < 30; i++ {
		w.account(t, f, a, aeSignedIn, "Chrome on macOS", 1000-2*i)
		own = append(own, w.account(t, f, p, aeSignedIn, "Safari on iOS", 999-2*i))
	}

	ids, pages, totals := csWalk(t, f, prep, 25)

	if len(ids) == 0 {
		t.Fatal("preparer walked no rows")
	}
	if !slices.Equal(csSorted(ids), csSorted(own)) || pages != 2 {
		t.Errorf("preparer walked %d rows in %d pages, want its own 30 in 2", len(ids), pages)
	}
	for _, n := range totals {
		if n != 30 {
			t.Errorf("total on a page = %d, want 30", n)
		}
	}
	if all, allPages, _ := csWalk(t, f, admin, 25); len(all) != 60 || allPages != 3 {
		t.Errorf("admin control walked %d rows in %d pages, want 60 in 3", len(all), allPages)
	}
}

func TestAccountEvents_ProbeAppliesTheRule(t *testing.T) {
	f := requireFixture(t)
	super := csSuper(t)

	for _, kind := range []string{"in_house", "firm"} {
		t.Run(kind, func(t *testing.T) {
			w := &aeWorld{p: pageSeedTenant(t, f)}
			aeKind(t, super, w.p.tenant, kind)
			a := uuid.NewString()
			admin := csMemberUser(t, super, a, w.p.tenant, "admin", false)
			prep := csMemberUser(t, super, uuid.NewString(), w.p.tenant, "preparer", kind == "in_house")
			w.account(t, f, a, aeSignedIn, "Chrome on macOS", 300)
			w.account(t, f, a, aeSignedOut, "Chrome on macOS", 290)

			if got := csList(t, f, admin, audit.Filter{Limit: 50}); got.Total != 2 || got.LogIsEmpty {
				t.Fatalf("admin control: total %d, log_is_empty %v, want 2 and false", got.Total, got.LogIsEmpty)
			}
			got := csList(t, f, prep, audit.Filter{Limit: 50})
			if got.Total != 0 || len(got.Events) != 0 {
				t.Errorf("preparer read %d rows, total %d, want none", len(got.Events), got.Total)
			}
			if !got.LogIsEmpty {
				t.Error("preparer: log_is_empty = false, want true: the probe counted another member's account rows")
			}
		})
	}
}

func TestAccountEvents_NonUUIDSubjectReadsEveryRow(t *testing.T) {
	f := requireFixture(t)
	w := aeNewFirm(t, f)
	system := auth.WithIdentity(context.Background(), auth.Identity{Subject: "system", Role: "authenticated", TenantID: w.p.tenant})

	got := csList(t, f, system, audit.Filter{Limit: 50})

	csAssertRows(t, "non-UUID subject", got, w.want()["A"])
	if n := csList(t, f, w.ctx["N"], audit.Filter{Limit: 50}).Total; n >= got.Total {
		t.Errorf("control: a non-admin member read %d rows, the non-UUID subject %d, want fewer", n, got.Total)
	}
}

// The reader in another workspace reads that workspace only, admin or not.
func TestAccountEvents_CrossTenantReaderSeesNothing(t *testing.T) {
	f := requireFixture(t)
	super := csSuper(t)
	x := aeNewFirm(t, f)
	y := &aeWorld{p: pageSeedTenant(t, f)}
	z := uuid.NewString()
	zAdmin := csMemberUser(t, super, z, y.p.tenant, "admin", false)
	zRow := y.account(t, f, z, aeSignedIn, "Chrome on macOS", 300)

	got := csList(t, f, zAdmin, audit.Filter{Limit: 50})

	csAssertRows(t, "the other workspace's admin", got, []int64{zRow})
	for _, ids := range x.want() {
		for _, id := range ids {
			for _, e := range got.Events {
				if e.ID == fmt.Sprint(id) {
					t.Errorf("a reader in another workspace read row %d", id)
				}
			}
		}
	}
}

// Admin in one workspace, preparer in another: the role is the one of the tenant being read.
func TestAccountEvents_AdminElsewhereIsNotAdminHere(t *testing.T) {
	f := requireFixture(t)
	super := csSuper(t)
	x := aeNewFirm(t, f)
	y := &aeWorld{p: pageSeedTenant(t, f)}
	user := uuid.NewString()
	ctxX := csMemberUser(t, super, user, x.p.tenant, "preparer", true)
	ctxY := csMemberUser(t, super, user, y.p.tenant, "admin", false)
	other := uuid.NewString()
	csMemberUser(t, super, other, y.p.tenant, "preparer", true)
	xOwn := x.account(t, f, user, aeSignedIn, "Safari on iOS", 100)
	yOwn := y.account(t, f, user, aeSignedIn, "Safari on iOS", 100)
	yOther := y.account(t, f, other, aeSignedIn, "Safari on iOS", 90)

	csAssertRows(t, "admin of Y, reading Y", csList(t, f, ctxY, audit.Filter{Limit: 50}), []int64{yOwn, yOther})
	csAssertRows(t, "preparer of X, reading X", csList(t, f, ctxX, audit.Filter{Limit: 50}),
		[]int64{xOwn, x.c1, x.c2, x.ws})
}

// FIRMS-04 stays as it was for rows that are not account events; the account rows are the only difference.
func TestAccountEvents_NonAccountRowsKeepTheirFirms04Rules(t *testing.T) {
	f := requireFixture(t)
	w := aeNewFirm(t, f)
	nonAccount := func(member string) []int64 {
		got := csList(t, f, w.ctx[member], audit.Filter{Limit: 50})
		var ids []int64
		for _, e := range got.Events {
			if len(e.Event) < 8 || e.Event[:8] != "account." {
				var id int64
				fmt.Sscan(e.ID, &id)
				ids = append(ids, id)
			}
		}
		return ids
	}
	for member, want := range map[string][]int64{
		"A": {w.c1, w.c2, w.ws},
		"N": {w.c1, w.c2, w.ws}, // the workspace row is authored by A and stays readable
		"L": {w.c1},
		"U": nil,
	} {
		if got := nonAccount(member); !slices.Equal(csSorted(got), csSorted(want)) {
			t.Errorf("%s: non-account rows %v, want %v", member, csSorted(got), csSorted(want))
		}
	}
	// The paired positive: the same view holds the member's own account rows and not another's.
	got := csList(t, f, w.ctx["N"], audit.Filter{Limit: 50})
	var have []string
	for _, e := range got.Events {
		if e.Event == aeSignedIn {
			have = append(have, e.Actor)
		}
	}
	sort.Strings(have)
	if want := []string{w.ids["N"]}; !slices.Equal(have, want) {
		t.Errorf("N reads signed_in rows of %v, want only its own %v", have, want)
	}
}
