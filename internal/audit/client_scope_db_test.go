// The audit reader limits a member to the clients they hold.
// Reads go through Store.List with identities built from seeded membership rows; an empty
// Subject takes the unscoped arm and would prove nothing. Helpers use a cs* prefix.
package audit_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// csSuper opens a superuser pool: all_clients, membership_clients and tenants.kind are not
// writable by invoice_app. A missing URL fails the test; a skip would prove nothing.
func csSuper(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_SUPERUSER_URL")
	if url == "" {
		t.Fatal("DATABASE_SUPERUSER_URL is unset: the client-scope audit tests seed memberships as the superuser")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("superuser pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// csMember seeds an active member and returns an identity context for Store.List.
func csMember(t *testing.T, super *pgxpool.Pool, tenant, role string, allClients bool, clients ...string) context.Context {
	t.Helper()
	return csMemberUser(t, super, uuid.NewString(), tenant, role, allClients, clients...)
}

// csMemberUser is csMember for a given user id, so one user can hold two tenants.
func csMemberUser(t *testing.T, super *pgxpool.Pool, user, tenant, role string, allClients bool, clients ...string) context.Context {
	t.Helper()
	ctx := context.Background()
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role, status, all_clients) VALUES ($1, $2, $3, 'active', $4)`,
		tenant, user, role, allClients); err != nil {
		t.Fatalf("seed %s member: %v", role, err)
	}
	for _, e := range clients {
		if _, err := super.Exec(ctx,
			`INSERT INTO membership_clients (tenant_id, user_id, entity_id) VALUES ($1, $2, $3)`,
			tenant, user, e); err != nil {
			t.Fatalf("seed membership_clients: %v", err)
		}
	}
	return auth.WithIdentity(ctx, auth.Identity{Subject: user, Role: "authenticated", TenantID: tenant})
}

func csList(t *testing.T, f *fixture, ctx context.Context, filter audit.Filter) audit.Response {
	t.Helper()
	got, err := audit.NewStore(f.app).List(ctx, filter)
	if err != nil {
		t.Fatalf("Store.List: %v", err)
	}
	return got
}

// csClient is one client with its invoice and the two attributed audit rows the corpus gives it.
type csClient struct {
	entity, invoice, name, number string
	rows                          []int64 // invoice.created, portfolio.entity.updated
}

// csSeedClient seeds a client, one invoice and two attributed audit rows. The entity-updated
// payload carries the client name so a free-text search can reach it through the payload arm.
func csSeedClient(t *testing.T, f *fixture, p pageFixture, name string) csClient {
	t.Helper()
	c := csClient{name: name}
	c.entity = pageSeedEntity(t, f, p, name)
	c.invoice = pageSeedInvoice(t, f, p, c.entity)
	c.number = "INV-" + c.invoice[:8]
	seeded := filtInsert(t, f, p, []filtRow{
		{event: "invoice.created", actor: "cs-actor-" + name, payload: fmt.Sprintf(`{"id":%q,"invoice_number":%q}`, c.invoice, c.number), ageSeconds: 100},
		{event: "portfolio.entity.updated", actor: "cs-actor-" + name, payload: fmt.Sprintf(`{"id":%q,"name":%q}`, c.entity, name), ageSeconds: 90},
	})
	for _, s := range seeded {
		if s.entity == nil || *s.entity != c.entity {
			t.Fatalf("row %s was attributed to %v, want client %s: the trigger changed and the oracle is void", s.event, s.entity, c.entity)
		}
		c.rows = append(c.rows, s.id)
	}
	return c
}

// csSeedWorkspaceRows seeds the first n of two NULL-entity rows: firm-wide, then unattributed.
func csSeedWorkspaceRows(t *testing.T, f *fixture, p pageFixture, n int) []int64 {
	t.Helper()
	seeded := filtInsert(t, f, p, []filtRow{
		{event: "membership.suspended", actor: "cs-actor-ws", payload: `{}`, ageSeconds: 80},
		{event: "document.created", actor: "cs-actor-ws", payload: `{}`, ageSeconds: 70},
	}[:n])
	ids := make([]int64, 0, len(seeded))
	for _, s := range seeded {
		if s.entity != nil {
			t.Fatalf("workspace row %s was attributed to %s, want NULL", s.event, *s.entity)
		}
		ids = append(ids, s.id)
	}
	return ids
}

// csCorpus is the Description's corpus: a firm tenant with clients A and B plus two workspace
// rows, and an in_house tenant with one client row and one workspace row.
type csCorpus struct {
	firm, house    pageFixture
	a, b           csClient
	ws             []int64
	houseClient    csClient
	houseWS        []int64
	admin, limited context.Context
	unassigned     context.Context
	allClients     context.Context
	houseAdmin     context.Context
	housePreparer  context.Context
}

func csBuildCorpus(t *testing.T, f *fixture) csCorpus {
	t.Helper()
	super := csSuper(t)
	c := csCorpus{firm: pageSeedTenant(t, f)}
	c.a = csSeedClient(t, f, c.firm, "Alpha Holdings Ltd")
	c.b = csSeedClient(t, f, c.firm, "Bravo Trading Ltd")
	c.ws = csSeedWorkspaceRows(t, f, c.firm, 2)
	c.admin = csMember(t, super, c.firm.tenant, "admin", false)
	c.limited = csMember(t, super, c.firm.tenant, "reviewer", false, c.a.entity)
	c.unassigned = csMember(t, super, c.firm.tenant, "preparer", false)
	c.allClients = csMember(t, super, c.firm.tenant, "preparer", true)

	c.house = pageSeedTenant(t, f)
	if _, err := super.Exec(context.Background(), `UPDATE tenants SET kind = 'in_house' WHERE id = $1`, c.house.tenant); err != nil {
		t.Fatalf("make the second tenant in_house: %v", err)
	}
	c.houseClient = csSeedClient(t, f, c.house, "House Client")
	c.houseWS = csSeedWorkspaceRows(t, f, c.house, 1)
	c.houseAdmin = csMember(t, super, c.house.tenant, "admin", false)
	c.housePreparer = csMember(t, super, c.house.tenant, "preparer", false)
	return c
}

func csSorted(ids ...[]int64) []int64 {
	var out []int64
	for _, s := range ids {
		out = append(out, s...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// csAssertRows asserts the page holds exactly want (any order) and that Total agrees.
func csAssertRows(t *testing.T, label string, got audit.Response, want []int64) {
	t.Helper()
	have := make([]int64, 0, len(got.Events))
	for _, e := range got.Events {
		var id int64
		if _, err := fmt.Sscan(e.ID, &id); err != nil {
			t.Fatalf("%s: event id %q is not a bigint: %v", label, e.ID, err)
		}
		have = append(have, id)
	}
	sort.Slice(have, func(i, j int) bool { return have[i] < have[j] })
	want = csSorted(want)
	if len(have) != len(want) {
		t.Errorf("%s: read %d rows %v, want %d rows %v", label, len(have), have, len(want), want)
	} else {
		for i := range have {
			if have[i] != want[i] {
				t.Errorf("%s: read rows %v, want %v", label, have, want)
				break
			}
		}
	}
	if got.Total != len(want) {
		t.Errorf("%s: total = %d, want %d", label, got.Total, len(want))
	}
}

func TestClientScope_AuditLimitedMemberReadsOnlyItsClientsRows(t *testing.T) {
	f := requireFixture(t)
	c := csBuildCorpus(t, f)

	got := csList(t, f, c.limited, audit.Filter{Limit: 50})

	csAssertRows(t, "limited to A", got, c.a.rows)
	for _, e := range got.Events {
		if e.EntityID == nil {
			t.Errorf("limited member read workspace row %s (%s)", e.ID, e.Event)
		}
	}
}

// A set of two discriminates "first id only" and "last id only".
func TestClientScope_AuditTwoOfThreeClients(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	a := csSeedClient(t, f, p, "Alpha Holdings Ltd")
	b := csSeedClient(t, f, p, "Bravo Trading Ltd")
	cc := csSeedClient(t, f, p, "Charlie Exports Ltd")
	member := csMember(t, csSuper(t), p.tenant, "preparer", false, a.entity, cc.entity)

	got := csList(t, f, member, audit.Filter{Limit: 50})

	csAssertRows(t, "limited to A and C", got, csSorted(a.rows, cc.rows))
	if len(b.rows) != 2 {
		t.Fatalf("client B seeded %d rows, want 2", len(b.rows))
	}
}

// --- today's access (the guard against over-scoping) ---

func TestClientScope_AuditAllClientsMemberKeepsWorkspaceRows(t *testing.T) {
	f := requireFixture(t)
	c := csBuildCorpus(t, f)

	got := csList(t, f, c.allClients, audit.Filter{Limit: 50})

	csAssertRows(t, "All clients preparer", got, csSorted(c.a.rows, c.b.rows, c.ws))
}

func TestClientScope_AuditInHousePreparerKeepsWorkspaceRows(t *testing.T) {
	f := requireFixture(t)
	c := csBuildCorpus(t, f)

	got := csList(t, f, c.housePreparer, audit.Filter{Limit: 50})

	csAssertRows(t, "in-house preparer", got, csSorted(c.houseClient.rows, c.houseWS))
}

func TestClientScope_AuditAdminReadsWorkspaceRows(t *testing.T) {
	f := requireFixture(t)
	c := csBuildCorpus(t, f)

	csAssertRows(t, "firm admin", csList(t, f, c.admin, audit.Filter{Limit: 50}),
		csSorted(c.a.rows, c.b.rows, c.ws))
	csAssertRows(t, "in-house admin", csList(t, f, c.houseAdmin, audit.Filter{Limit: 50}),
		csSorted(c.houseClient.rows, c.houseWS))
}

func TestClientScope_AuditUnassignedMemberSeesAnEmptyLog(t *testing.T) {
	f := requireFixture(t)
	c := csBuildCorpus(t, f)

	got := csList(t, f, c.unassigned, audit.Filter{Limit: 50})
	if len(got.Events) != 0 || got.Total != 0 {
		t.Errorf("unassigned member read %d rows, total %d, want none", len(got.Events), got.Total)
	}
	if !got.LogIsEmpty {
		t.Error("unassigned member: log_is_empty = false, want true")
	}

	if admin := csList(t, f, c.admin, audit.Filter{Limit: 50}); admin.LogIsEmpty || admin.Total == 0 {
		t.Errorf("admin control: log_is_empty = %v, total = %d, want a populated log", admin.LogIsEmpty, admin.Total)
	}
}

func TestClientScope_AuditWorkspaceOnlyLogIsEmptyForALimitedMember(t *testing.T) {
	f := requireFixture(t)
	super := csSuper(t)
	p := pageSeedTenant(t, f)
	a := pageSeedEntity(t, f, p, "Quiet Client Ltd")
	csSeedWorkspaceRows(t, f, p, 1)
	limited := csMember(t, super, p.tenant, "reviewer", false, a)
	allClients := csMember(t, super, p.tenant, "preparer", true)

	got := csList(t, f, limited, audit.Filter{Limit: 50})
	if len(got.Events) != 0 || got.Total != 0 {
		t.Errorf("limited member read %d rows, total %d, want none", len(got.Events), got.Total)
	}
	if !got.LogIsEmpty {
		t.Error("limited member of a workspace-only log: log_is_empty = false, want true (the probe must apply the scope, D6)")
	}

	all := csList(t, f, allClients, audit.Filter{Limit: 50})
	if len(all.Events) != 1 || all.LogIsEmpty {
		t.Errorf("All clients control read %d rows, log_is_empty = %v, want 1 row and false", len(all.Events), all.LogIsEmpty)
	}
}

func TestClientScope_AuditCompanyFilterStopsAtTheSet(t *testing.T) {
	f := requireFixture(t)
	c := csBuildCorpus(t, f)

	csAssertRows(t, "limited, company=A", csList(t, f, c.limited,
		audit.Filter{Limit: 50, Company: audit.NamedCompany(c.a.entity)}), c.a.rows)
	csAssertRows(t, "limited, company=B", csList(t, f, c.limited,
		audit.Filter{Limit: 50, Company: audit.NamedCompany(c.b.entity)}), nil)
	csAssertRows(t, "admin, company=B", csList(t, f, c.admin,
		audit.Filter{Limit: 50, Company: audit.NamedCompany(c.b.entity)}), c.b.rows)
}

func TestClientScope_AuditWorkspaceFilterIsEmptyForALimitedMember(t *testing.T) {
	f := requireFixture(t)
	c := csBuildCorpus(t, f)
	ws := audit.Filter{Limit: 50, Company: audit.WorkspaceOnly()}

	csAssertRows(t, "All clients, workspace", csList(t, f, c.allClients, ws), c.ws)
	csAssertRows(t, "admin, workspace", csList(t, f, c.admin, ws), c.ws)
	csAssertRows(t, "in-house preparer, workspace", csList(t, f, c.housePreparer, ws), c.houseWS)
	csAssertRows(t, "limited, workspace", csList(t, f, c.limited, ws), nil)
	csAssertRows(t, "unassigned, workspace", csList(t, f, c.unassigned, ws), nil)
}

// The client name rides on A's and B's entity-updated payloads, so the payload arm reaches
// B's row unless the scope predicate fences it. B's invoice number is reachable only through
// the resolved invoices arm, which FIRMS-03 RLS already empties: that half is a guard.
func TestClientScope_AuditSearchCannotReachAHiddenClient(t *testing.T) {
	f := requireFixture(t)
	c := csBuildCorpus(t, f)

	for _, q := range []string{c.b.name, c.b.number} {
		csAssertRows(t, "limited, q="+q, csList(t, f, c.limited, audit.Filter{Limit: 50, Q: q}), nil)
		csAssertRows(t, "admin, q="+q, csList(t, f, c.admin, audit.Filter{Limit: 50, Q: q}),
			wantSearchRows(c, q))
	}

	csAssertRows(t, "limited, q=A's name", csList(t, f, c.limited, audit.Filter{Limit: 50, Q: c.a.name}),
		c.a.rows)
	csAssertRows(t, "limited, q=A's number", csList(t, f, c.limited, audit.Filter{Limit: 50, Q: c.a.number}),
		c.a.rows[:1])
}

// wantSearchRows is what an unscoped admin reads for q: B's name is in B's entity-updated
// payload and its company arm matches both of B's rows; B's number matches B's invoice rows.
func wantSearchRows(c csCorpus, q string) []int64 {
	if q == c.b.name {
		return c.b.rows
	}
	return c.b.rows[:1]
}

func TestClientScope_AuditFacetsCountOnlyVisibleRows(t *testing.T) {
	f := requireFixture(t)
	c := csBuildCorpus(t, f)

	sum := func(fs []audit.Facet) (n int) {
		for _, b := range fs {
			n += b.Count
		}
		return n
	}
	companies := func(fs []audit.Facet) (ids map[string]bool, hasNull bool) {
		ids = map[string]bool{}
		for _, b := range fs {
			if b.Value == nil {
				hasNull = true
				continue
			}
			ids[*b.Value] = true
		}
		return ids, hasNull
	}

	lim := csList(t, f, c.limited, audit.Filter{Limit: 50})
	if n := sum(lim.Facets.Event); n != 2 {
		t.Errorf("limited: event facet sums to %d, want 2", n)
	}
	if n := sum(lim.Facets.Actor); n != 2 {
		t.Errorf("limited: actor facet sums to %d, want 2", n)
	}
	ids, hasNull := companies(lim.Facets.Company)
	if len(ids) != 1 || !ids[c.a.entity] {
		t.Errorf("limited: company facet holds %v, want only A (%s)", ids, c.a.entity)
	}
	if hasNull {
		t.Error("limited: company facet holds a NULL bucket, want it absent")
	}

	for label, ctx := range map[string]context.Context{"All clients": c.allClients, "admin": c.admin} {
		got := csList(t, f, ctx, audit.Filter{Limit: 50})
		ids, hasNull := companies(got.Facets.Company)
		if len(ids) != 2 || !ids[c.a.entity] || !ids[c.b.entity] || !hasNull {
			t.Errorf("%s: company facet holds %v (NULL bucket %v), want A, B and the NULL bucket", label, ids, hasNull)
		}
	}
}

// --- QA adversarial coverage --------------------------------------------------------

// csWalk pages through the log with the given page size and returns every row id and the page count.
func csWalk(t *testing.T, f *fixture, ctx context.Context, limit int) (ids []int64, pages int, totals []int) {
	t.Helper()
	filter := audit.Filter{Limit: limit}
	for pages < 10 {
		pages++
		got := csList(t, f, ctx, filter)
		totals = append(totals, got.Total)
		for _, e := range got.Events {
			var id int64
			if _, err := fmt.Sscan(e.ID, &id); err != nil {
				t.Fatalf("event id %q: %v", e.ID, err)
			}
			ids = append(ids, id)
		}
		if !got.Page.HasMore {
			return ids, pages, totals
		}
		cur, err := audit.DecodeCursor(*got.Page.NextCursor)
		if err != nil {
			t.Fatalf("decode cursor: %v", err)
		}
		filter.Cursor = &cur
	}
	t.Fatal("cursor walk did not end")
	return nil, 0, nil
}

// Page size 1 puts A's and B's rows on separate pages, so a page that dropped the scope shows B.
func TestClientScope_AuditCursorPagesStayScoped(t *testing.T) {
	f := requireFixture(t)
	c := csBuildCorpus(t, f)

	ids, pages, totals := csWalk(t, f, c.limited, 1)
	if pages != 2 {
		t.Errorf("limited member paged %d times, want 2 (one per row of A)", pages)
	}
	if len(ids) == 0 {
		t.Fatal("limited member walked no rows")
	}
	if got, want := csSorted(ids), csSorted(c.a.rows); !reflect.DeepEqual(got, want) {
		t.Errorf("limited member walked rows %v, want A's %v", got, want)
	}
	for _, n := range totals {
		if n != 2 {
			t.Errorf("total on a page = %d, want 2 on every page", n)
		}
	}

	all, allPages, _ := csWalk(t, f, c.allClients, 2)
	if want := csSorted(c.a.rows, c.b.rows, c.ws); len(all) != len(want) || allPages != 3 {
		t.Errorf("All clients control walked %d rows in %d pages, want %d rows in 3", len(all), allPages, len(want))
	}
}

// The company facet omits its own filter but keeps the scope: it may list A, never B.
func TestClientScope_AuditFacetsKeepTheScopeUnderACompanyFilter(t *testing.T) {
	f := requireFixture(t)
	c := csBuildCorpus(t, f)
	count := func(fs []audit.Facet) (n int) {
		for _, b := range fs {
			n += b.Count
		}
		return n
	}
	hasEntity := func(fs []audit.Facet, id string) bool {
		for _, b := range fs {
			if b.Value != nil && *b.Value == id {
				return true
			}
		}
		return false
	}

	hidden := csList(t, f, c.limited, audit.Filter{Limit: 50, Company: audit.NamedCompany(c.b.entity)})
	if n := count(hidden.Facets.Event) + count(hidden.Facets.Actor); n != 0 {
		t.Errorf("limited, company=B: event and actor facets count %d rows, want 0", n)
	}
	if len(hidden.Facets.Company) != 1 || !hasEntity(hidden.Facets.Company, c.a.entity) || hasEntity(hidden.Facets.Company, c.b.entity) {
		t.Errorf("limited, company=B: company facet = %+v, want only A", hidden.Facets.Company)
	}

	own := csList(t, f, c.limited, audit.Filter{Limit: 50, Company: audit.NamedCompany(c.a.entity)})
	if n := count(own.Facets.Event); n != 2 {
		t.Errorf("limited, company=A: event facet sums to %d, want 2", n)
	}

	adm := csList(t, f, c.admin, audit.Filter{Limit: 50, Company: audit.NamedCompany(c.b.entity)})
	if len(adm.Facets.Company) != 3 || !hasEntity(adm.Facets.Company, c.a.entity) || !hasEntity(adm.Facets.Company, c.b.entity) {
		t.Errorf("admin, company=B: company facet = %+v, want A, B and the NULL bucket", adm.Facets.Company)
	}
}

func TestClientScope_AuditUnassignedMemberFacetsAreEmpty(t *testing.T) {
	f := requireFixture(t)
	c := csBuildCorpus(t, f)

	adm := csList(t, f, c.admin, audit.Filter{Limit: 50})
	if len(adm.Facets.Event) == 0 || len(adm.Facets.Actor) == 0 || len(adm.Facets.Company) == 0 {
		t.Fatalf("admin control facets are empty: %+v", adm.Facets)
	}

	got := csList(t, f, c.unassigned, audit.Filter{Limit: 50})
	if got.Facets.Event == nil || got.Facets.Actor == nil || got.Facets.Company == nil {
		t.Errorf("unassigned facets must be empty slices, not nil: %+v", got.Facets)
	}
	if len(got.Facets.Event)+len(got.Facets.Actor)+len(got.Facets.Company) != 0 {
		t.Errorf("unassigned member read facets %+v, want none", got.Facets)
	}
}

// Search arms are OR-ed; each must stay inside the scope, workspace events included.
func TestClientScope_AuditSearchCannotReachAWorkspaceRow(t *testing.T) {
	f := requireFixture(t)
	c := csBuildCorpus(t, f)

	csAssertRows(t, "limited, q=suspended", csList(t, f, c.limited, audit.Filter{Limit: 50, Q: "suspended"}), nil)
	csAssertRows(t, "admin, q=suspended", csList(t, f, c.admin, audit.Filter{Limit: 50, Q: "suspended"}), c.ws[:1])

	csAssertRows(t, "limited, q=created", csList(t, f, c.limited, audit.Filter{Limit: 50, Q: "created"}), c.a.rows[:1])
	csAssertRows(t, "admin, q=created", csList(t, f, c.admin, audit.Filter{Limit: 50, Q: "created"}),
		csSorted(c.a.rows[:1], c.b.rows[:1], c.ws[1:]))
}

func TestClientScope_AuditFiltersComposeWithTheScope(t *testing.T) {
	f := requireFixture(t)
	c := csBuildCorpus(t, f)
	bActor := "cs-actor-" + c.b.name

	csAssertRows(t, "limited, event=invoice.created",
		csList(t, f, c.limited, audit.Filter{Limit: 50, Events: []string{"invoice.created"}}), c.a.rows[:1])
	csAssertRows(t, "limited, event=document.created (workspace)",
		csList(t, f, c.limited, audit.Filter{Limit: 50, Events: []string{"document.created"}}), nil)
	csAssertRows(t, "admin, event=document.created",
		csList(t, f, c.admin, audit.Filter{Limit: 50, Events: []string{"document.created"}}), c.ws[1:])
	csAssertRows(t, "limited, B's actor",
		csList(t, f, c.limited, audit.Filter{Limit: 50, Actors: []string{bActor}}), nil)
	csAssertRows(t, "admin, B's actor",
		csList(t, f, c.admin, audit.Filter{Limit: 50, Actors: []string{bActor}}), c.b.rows)
	csAssertRows(t, "limited, company=A and workspace-only event",
		csList(t, f, c.limited, audit.Filter{Limit: 50, Company: audit.NamedCompany(c.a.entity), Events: []string{"membership.suspended"}}), nil)
}

// The probe applies the scope (a client with no rows reads an empty log although B has rows)
// and no request filter (a filter that matches nothing is not an empty log).
func TestClientScope_AuditProbeAppliesTheScopeAndNoRequestFilter(t *testing.T) {
	f := requireFixture(t)
	c := csBuildCorpus(t, f)
	super := csSuper(t)
	quiet := pageSeedEntity(t, f, c.firm, "Quiet Client Ltd")
	onlyQuiet := csMember(t, super, c.firm.tenant, "reviewer", false, quiet)

	got := csList(t, f, onlyQuiet, audit.Filter{Limit: 50})
	if got.Total != 0 || !got.LogIsEmpty {
		t.Errorf("member of a client with no rows: total %d, log_is_empty %v, want 0 and true", got.Total, got.LogIsEmpty)
	}

	filtered := csList(t, f, c.limited, audit.Filter{Limit: 50, Q: "no-such-text-anywhere"})
	if filtered.Total != 0 || filtered.LogIsEmpty {
		t.Errorf("limited member, unmatched q: total %d, log_is_empty %v, want 0 and false", filtered.Total, filtered.LogIsEmpty)
	}
}

// The seam reads the assignment on every request: a removed client leaves the log at once.
func TestClientScope_AuditScopeIsReadOnEveryRequest(t *testing.T) {
	f := requireFixture(t)
	c := csBuildCorpus(t, f)
	super := csSuper(t)
	user := uuid.NewString()
	member := csMemberUser(t, super, user, c.firm.tenant, "reviewer", false, c.a.entity)

	csAssertRows(t, "before the removal", csList(t, f, member, audit.Filter{Limit: 50}), c.a.rows)

	if _, err := super.Exec(context.Background(), `DELETE FROM membership_clients WHERE user_id = $1`, user); err != nil {
		t.Fatalf("remove the client: %v", err)
	}
	got := csList(t, f, member, audit.Filter{Limit: 50})
	csAssertRows(t, "after the removal", got, nil)
	if !got.LogIsEmpty {
		t.Error("after the removal: log_is_empty = false, want true")
	}

	if _, err := super.Exec(context.Background(),
		`INSERT INTO membership_clients (tenant_id, user_id, entity_id) VALUES ($1, $2, $3)`, c.firm.tenant, user, c.b.entity); err != nil {
		t.Fatalf("assign B: %v", err)
	}
	csAssertRows(t, "after assigning B", csList(t, f, member, audit.Filter{Limit: 50}), c.b.rows)
}

// Cross-tenant: one user limited in the firm and admin of the in-house tenant. Each tenant
// answers by its own scope; a tenant the user does not belong to is refused.
func TestClientScope_AuditOneUserTwoTenants(t *testing.T) {
	f := requireFixture(t)
	c := csBuildCorpus(t, f)
	super := csSuper(t)
	user := uuid.NewString()
	inFirm := csMemberUser(t, super, user, c.firm.tenant, "reviewer", false, c.a.entity)
	inHouse := csMemberUser(t, super, user, c.house.tenant, "admin", false)

	csAssertRows(t, "limited in the firm", csList(t, f, inFirm, audit.Filter{Limit: 50}), c.a.rows)
	csAssertRows(t, "admin in-house", csList(t, f, inHouse, audit.Filter{Limit: 50}), csSorted(c.houseClient.rows, c.houseWS))

	stranger := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: c.firm.tenant})
	if _, err := audit.NewStore(f.app).List(stranger, audit.Filter{Limit: 50}); !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("non-member of the firm: err = %v, want ErrNotActiveMember", err)
	}
}

// Pinned: a non-UUID subject (in-process actors) gets no scope and reads the whole tenant.
func TestClientScope_AuditNonUUIDSubjectReadsTheWholeTenant(t *testing.T) {
	f := requireFixture(t)
	c := csBuildCorpus(t, f)
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: "extraction-worker", Role: "authenticated", TenantID: c.firm.tenant})

	csAssertRows(t, "non-UUID subject", csList(t, f, ctx, audit.Filter{Limit: 50}), csSorted(c.a.rows, c.b.rows, c.ws))
}
