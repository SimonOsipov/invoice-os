package customer_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/customer"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
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

// csWorld is one firm tenant with clients A and B and the seven directory rows of the
// design fixture. Links come from fills run by an admin over real invoices.
type csWorld struct {
	super, app     *pgxpool.Pool
	store          *customer.Store
	invs           *invoice.Store
	tenant         string
	entA, entB     string
	admin, limited context.Context // limited: a reviewer limited to A
	id             map[string]string
}

var csTIN = map[string]string{
	"aOnly": "12345678-0101", "bOnly": "12345678-0102", "ab": "12345678-0103",
	"aArch": "12345678-0104", "bArch": "12345678-0105", "adminOwn": "12345678-0106", "memberOwn": "12345678-0107",
}

func newCSWorld(t *testing.T, label string) csWorld {
	t.Helper()
	super, app := dbTestPools(t)
	w := csWorld{super: super, app: app, store: customer.NewStore(app), invs: invoice.NewStore(app), id: map[string]string{}}
	w.tenant = seedTenant(t, super, label)
	w.entA = seedEntity(t, super, w.tenant, "Client A")
	w.entB = seedEntity(t, super, w.tenant, "Client B")
	w.admin = csMember(t, super, w.tenant, "admin")
	w.limited = csMember(t, super, w.tenant, "reviewer", w.entA)

	fillCreate(w.admin, t, w.invs, w.entA, "CSW-A1", csTIN["aOnly"], "A Only Ltd", "a@x.ng")
	fillCreate(w.admin, t, w.invs, w.entB, "CSW-B1", csTIN["bOnly"], "B Only Ltd", "b@x.ng")
	fillCreate(w.admin, t, w.invs, w.entA, "CSW-A2", csTIN["ab"], "A And B Ltd", "ab@x.ng")
	fillCreate(w.admin, t, w.invs, w.entB, "CSW-B2", csTIN["ab"], "A And B Ltd", "ab@x.ng")
	fillCreate(w.admin, t, w.invs, w.entA, "CSW-A3", csTIN["aArch"], "A Archived Ltd", "aa@x.ng")
	fillCreate(w.admin, t, w.invs, w.entB, "CSW-B3", csTIN["bArch"], "B Archived Ltd", "ba@x.ng")
	for _, k := range []string{"aArch", "bArch"} {
		if _, err := super.Exec(context.Background(),
			`UPDATE customers SET status = 'archived' WHERE tenant_id = $1 AND tin = $2`, w.tenant, csTIN[k]); err != nil {
			t.Fatalf("archive %s: %v", k, err)
		}
	}
	for k, ctx := range map[string]context.Context{"adminOwn": w.admin, "memberOwn": w.limited} {
		if _, err := w.store.Create(ctx, customer.CreateInput{TIN: ptr(csTIN[k]), Fields: customer.UpdateInput{Name: ptr(k + " Ltd")}}); err != nil {
			t.Fatalf("create %s: %v", k, err)
		}
	}
	for k, tin := range csTIN {
		w.id[k] = csIDByTIN(t, super, w.tenant, tin)
	}
	return w
}

func csIDByTIN(t *testing.T, super *pgxpool.Pool, tenant, tin string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`SELECT id FROM customers WHERE tenant_id = $1 AND tin = $2`, tenant, tin).Scan(&id); err != nil {
		t.Fatalf("no customers row for tin %s: %v", tin, err)
	}
	return id
}

func csSubject(ctx context.Context) string {
	id, _ := auth.IdentityFromContext(ctx)
	return id.Subject
}

// csLinks reads created_by and client_ids through to_jsonb, so a column the migration has not
// added yet fails the assertion instead of the query. clients is sorted.
func csLinks(t *testing.T, super *pgxpool.Pool, tenant, tin string) (createdBy *string, clients []string) {
	t.Helper()
	var raw []byte
	if err := super.QueryRow(context.Background(),
		`SELECT to_jsonb(c) FROM customers c WHERE tenant_id = $1 AND tin = $2`, tenant, tin).Scan(&raw); err != nil {
		t.Fatalf("read customers row %s: %v", tin, err)
	}
	var m struct {
		CreatedBy *string  `json:"created_by"`
		ClientIDs []string `json:"client_ids"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode customers row %s: %v", tin, err)
	}
	sort.Strings(m.ClientIDs)
	return m.CreatedBy, m.ClientIDs
}

func csWantLinks(t *testing.T, super *pgxpool.Pool, tenant, tin string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if want == nil {
		want = []string{}
	}
	_, got := csLinks(t, super, tenant, tin)
	if got == nil {
		got = []string{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("client_ids of %s = %v, want %v", tin, got, want)
	}
}

func csAuditCount(t *testing.T, super *pgxpool.Pool, tenant string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event LIKE 'customers.customer.%'`, tenant).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}

func csListIDs(t *testing.T, store *customer.Store, ctx context.Context, f customer.ListFilter) ([]string, int) {
	t.Helper()
	if f.Limit == 0 {
		f.Limit = 50
	}
	items, total, err := store.List(ctx, f)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := ids(items)
	sort.Strings(got)
	return got, total
}

func csSorted(in ...string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// Visible-rule clauses: aOnly and ab reach the member only through client_ids, memberOwn only
// through created_by; bOnly and bArch are out of reach; adminOwn is reachable by full access alone.
func TestClientScope_ALimitedMemberListsOnlyTheirClientsBuyersAndTheirOwn(t *testing.T) {
	w := newCSWorld(t, "CS dir list")

	got, total := csListIDs(t, w.store, w.limited, customer.ListFilter{})
	want := csSorted(w.id["aOnly"], w.id["ab"], w.id["aArch"], w.id["memberOwn"])
	if total != 4 || !reflect.DeepEqual(got, want) {
		t.Errorf("limited List = %v, total %d, want %v, total 4 (client_ids clause: aOnly, ab, aArch; created_by clause: memberOwn)", got, total, want)
	}
}

func TestClientScope_ALimitedMembersTINAndStatusFiltersStayInReach(t *testing.T) {
	w := newCSWorld(t, "CS dir filters")

	if got, total := csListIDs(t, w.store, w.limited, customer.ListFilter{TIN: csTIN["aOnly"]}); total != 1 || !slices.Equal(got, []string{w.id["aOnly"]}) {
		t.Errorf("List{TIN: A-only} = %v, total %d, want the A-only buyer, total 1", got, total)
	}
	if got, total := csListIDs(t, w.store, w.limited, customer.ListFilter{TIN: csTIN["bOnly"]}); total != 0 || len(got) != 0 {
		t.Errorf("List{TIN: B-only} = %v, total %d, want none, total 0 (client_ids clause)", got, total)
	}
	if got, total := csListIDs(t, w.store, w.limited, customer.ListFilter{Status: customer.StatusArchived}); total != 1 || !slices.Equal(got, []string{w.id["aArch"]}) {
		t.Errorf("List{Status: archived} = %v, total %d, want only the archived A-only buyer, total 1 (the archived B-only buyer is out of reach)", got, total)
	}
}

func TestClientScope_AMemberWithNoClientsSeesOnlyWhatTheyAdded(t *testing.T) {
	w := newCSWorld(t, "CS dir no clients")
	adder := csMember(t, w.super, w.tenant, "preparer")
	other := csMember(t, w.super, w.tenant, "preparer")
	own, err := w.store.Create(adder, customer.CreateInput{TIN: ptr("12345678-0201"), Fields: customer.UpdateInput{Name: ptr("Own Ltd")}})
	if err != nil {
		t.Fatalf("Create as a preparer with no clients: %v", err)
	}

	if got, total := csListIDs(t, w.store, adder, customer.ListFilter{}); total != 1 || !slices.Equal(got, []string{own.ID}) {
		t.Errorf("adder List = %v, total %d, want only %s, total 1 (created_by clause)", got, total, own.ID)
	}
	if got, total := csListIDs(t, w.store, other, customer.ListFilter{}); total != 0 || len(got) != 0 {
		t.Errorf("List as a preparer with no clients and no buyers = %v, total %d, want none, total 0", got, total)
	}
}

type csAction struct{ name, method, suffix, body string }

var csActions = []csAction{
	{"PATCH", http.MethodPatch, "", `{"name":"x"}`},
	{"archive", http.MethodPost, "/archive", ""},
	{"restore", http.MethodPost, "/restore", ""},
}

// Each hidden id dies at the client_ids clause of USING (the UPDATE matches 0 rows).
func TestClientScope_HiddenBuyerWritesAnswerAsAnUnknownID(t *testing.T) {
	w := newCSWorld(t, "CS dir hidden writes")
	h := newMux(w.store)
	hidden := map[string]string{"B-only active": w.id["bOnly"], "B-only archived": w.id["bArch"]}
	type before struct {
		status string
		edited []string
	}
	snap := map[string]before{}
	for name, id := range hidden {
		r := readRow(t, w.super, id)
		snap[name] = before{rowStatus(t, w.super, id), r.Edited}
	}
	auditBefore := csAuditCount(t, w.super, w.tenant)

	for _, a := range csActions {
		unknown := do(t, h, w.limited, a.method, "/v1/customers/"+uuid.NewString()+a.suffix, a.body)
		wantStatus(t, unknown, http.StatusNotFound)
		if errorBody(t, unknown) != "not found" {
			t.Fatalf("%s on an unknown id: body %q, want the not found envelope", a.name, unknown.Body.String())
		}
		cells := map[string]string{"not-a-uuid": "not-a-uuid"}
		for name, id := range hidden {
			cells[name] = id
		}
		for name, id := range cells {
			rec := do(t, h, w.limited, a.method, "/v1/customers/"+id+a.suffix, a.body)
			if rec.Code != http.StatusNotFound || rec.Body.String() != unknown.Body.String() {
				t.Errorf("%s on %s as a member limited to A: %d %q, want 404 %q", a.name, name, rec.Code, rec.Body.String(), unknown.Body.String())
			}
		}
	}

	for name, id := range hidden {
		r := readRow(t, w.super, id)
		if got := rowStatus(t, w.super, id); got != snap[name].status || !reflect.DeepEqual(r.Edited, snap[name].edited) {
			t.Errorf("%s after the hidden writes: status %s, edited_fields %v, want %s and %v", name, got, r.Edited, snap[name].status, snap[name].edited)
		}
	}
	if n := csAuditCount(t, w.super, w.tenant); n != auditBefore {
		t.Errorf("customers.customer.* audit rows = %d, want %d", n, auditBefore)
	}
}

func TestClientScope_ALimitedMemberWritesTheirClientsBuyerAsToday(t *testing.T) {
	w := newCSWorld(t, "CS dir visible writes")
	h := newMux(w.store)
	auditBefore := csAuditCount(t, w.super, w.tenant)
	type step struct {
		action csAction
		code   int
		errMsg string
	}
	// Order keeps each buyer's status stable until its own transition.
	cases := []struct {
		label string
		steps []step
	}{
		{"aOnly", []step{{csActions[0], 200, ""}, {csActions[2], 409, "redundant transition"}, {csActions[1], 200, ""}}},
		{"aArch", []step{{csActions[0], 200, ""}, {csActions[1], 409, "redundant transition"}, {csActions[2], 200, ""}}},
	}
	wantAudit := 0
	for _, c := range cases {
		for _, s := range c.steps {
			rec := do(t, h, w.limited, s.action.method, "/v1/customers/"+w.id[c.label]+s.action.suffix, s.action.body)
			if rec.Code != s.code || (s.errMsg != "" && errorBody(t, rec) != s.errMsg) {
				t.Errorf("%s on %s: %d %q, want %d %s", s.action.name, c.label, rec.Code, rec.Body.String(), s.code, s.errMsg)
			}
			if s.code == 200 {
				wantAudit++
			}
		}
	}
	if n := csAuditCount(t, w.super, w.tenant) - auditBefore; n != wantAudit {
		t.Errorf("new audit rows = %d, want %d (one per 200)", n, wantAudit)
	}
}

// memberOwn-style reach: only the created_by clause admits the creator.
func TestClientScope_ALimitedMemberKeepsTheBuyerTheyAdded(t *testing.T) {
	w := newCSWorld(t, "CS dir keeps own")
	h := newMux(w.store)
	const tin = "12345678-0301"

	rec := do(t, h, w.limited, http.MethodPost, "/v1/customers", `{"tin":"`+tin+`","name":"Fresh Ltd"}`)
	wantStatus(t, rec, http.StatusCreated)
	var c customer.Customer
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode created buyer: %v", err)
	}
	createdBy, clients := csLinks(t, w.super, w.tenant, tin)
	if createdBy == nil || *createdBy != csSubject(w.limited) || len(clients) != 0 {
		t.Errorf("created buyer: created_by %v, client_ids %v, want the member and none", createdBy, clients)
	}
	if got, _ := csListIDs(t, w.store, w.limited, customer.ListFilter{TIN: tin}); !slices.Equal(got, []string{c.ID}) {
		t.Errorf("creator List{TIN} = %v, want the new buyer", got)
	}
	wantStatus(t, do(t, h, w.limited, http.MethodPatch, "/v1/customers/"+c.ID, `{"name":"Renamed"}`), http.StatusOK)
	wantStatus(t, do(t, h, w.limited, http.MethodPost, "/v1/customers/"+c.ID+"/archive", ""), http.StatusOK)

	second := csMember(t, w.super, w.tenant, "reviewer", w.entA)
	if got, _ := csListIDs(t, w.store, second, customer.ListFilter{TIN: tin}); len(got) != 0 {
		t.Errorf("another member limited to A lists %v, want the hand-added buyer hidden", got)
	}
}

func TestClientScope_ARemovedMembersBuyerStaysHidden(t *testing.T) {
	w := newCSWorld(t, "CS dir removed adder")
	adder := csMember(t, w.super, w.tenant, "reviewer", w.entA)
	added, err := w.store.Create(adder, customer.CreateInput{TIN: ptr("12345678-0302")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.super.Exec(context.Background(),
		`UPDATE memberships SET status = 'removed' WHERE tenant_id = $1 AND user_id = $2`, w.tenant, csSubject(adder)); err != nil {
		t.Fatalf("remove the adder: %v", err)
	}

	second := csMember(t, w.super, w.tenant, "reviewer", w.entA)
	if got, _ := csListIDs(t, w.store, second, customer.ListFilter{}); slices.Contains(got, added.ID) || len(got) != 3 {
		t.Errorf("another member limited to A lists %v, want the A buyers without the removed member's (created_by clause)", got)
	}
	if got, _ := csListIDs(t, w.store, w.admin, customer.ListFilter{}); !slices.Contains(got, added.ID) {
		t.Errorf("admin lists %v, want the removed member's buyer", got)
	}
}

func TestClientScope_CreatingAHiddenTINIsAConflictWithNoBuyerData(t *testing.T) {
	w := newCSWorld(t, "CS dir hidden tin")
	h := newMux(w.store)

	rec := do(t, h, w.limited, http.MethodPost, "/v1/customers", `{"tin":"`+csTIN["bOnly"]+`"}`)
	wantStatus(t, rec, http.StatusConflict)
	if got := rec.Body.String(); got != "{\"error\":\"duplicate tin\"}\n" {
		t.Errorf("409 body = %q, want only the duplicate tin envelope", got)
	}
	got, _ := csListIDs(t, w.store, w.limited, customer.ListFilter{})
	if slices.Contains(got, w.id["bOnly"]) || !slices.Contains(got, w.id["aOnly"]) {
		t.Errorf("List after the conflict = %v, want A-only present and B-only absent (client_ids clause)", got)
	}
}

func csAllClients(t *testing.T, super *pgxpool.Pool, ctx context.Context, tenant string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`UPDATE memberships SET all_clients = true WHERE tenant_id = $1 AND user_id = $2`, tenant, csSubject(ctx)); err != nil {
		t.Fatalf("grant all_clients: %v", err)
	}
}

// The full-access clause (entity_scope ” or '*') is the only one that admits adminOwn.
func TestClientScope_FullAccessCallersSeeTheWholeDirectory(t *testing.T) {
	w := newCSWorld(t, "CS dir full access")
	all := csMember(t, w.super, w.tenant, "reviewer")
	csAllClients(t, w.super, all, w.tenant)
	inHouse := csMember(t, w.super, w.tenant, "reviewer", w.entA)

	check := func(name string, ctx context.Context) {
		t.Helper()
		got, total := csListIDs(t, w.store, ctx, customer.ListFilter{})
		if total != 7 || len(got) != 7 {
			t.Errorf("%s List = %d items, total %d, want 7 and 7 (full-access clause)", name, len(got), total)
		}
		rec := do(t, newMux(w.store), ctx, http.MethodPatch, "/v1/customers/"+w.id["bOnly"], `{"name":"By `+name+`"}`)
		if rec.Code != http.StatusOK {
			t.Errorf("%s PATCH on the B-only buyer: %d %q, want 200", name, rec.Code, rec.Body.String())
		}
	}
	check("admin", w.admin)
	check("all_clients reviewer", all)
	if _, err := w.super.Exec(context.Background(), `UPDATE tenants SET kind = 'in_house' WHERE id = $1`, w.tenant); err != nil {
		t.Fatalf("make the tenant in-house: %v", err)
	}
	check("in-house reviewer", inHouse)
}

func TestClientScope_ANonUUIDActorListsAndEditsEveryBuyer(t *testing.T) {
	w := newCSWorld(t, "CS dir non-uuid actor")
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: "system", TenantID: w.tenant})

	if got, total := csListIDs(t, w.store, ctx, customer.ListFilter{}); total != 7 || len(got) != 7 {
		t.Errorf("List as a non-uuid actor = %d items, total %d, want 7 and 7", len(got), total)
	}
	if _, err := w.store.Update(ctx, w.id["bOnly"], customer.UpdateInput{Name: ptr("By system")}); err != nil {
		t.Errorf("Update of the B-only buyer as a non-uuid actor: %v, want nil", err)
	}
}

func TestClientScope_ASharedBuyerIsOneRecord(t *testing.T) {
	w := newCSWorld(t, "CS dir shared record")
	memberB := csMember(t, w.super, w.tenant, "reviewer", w.entB)

	wantStatus(t, do(t, newMux(w.store), w.limited, http.MethodPatch, "/v1/customers/"+w.id["ab"], `{"email":"new@x.ng"}`), http.StatusOK)

	items, _, err := w.store.List(memberB, customer.ListFilter{Limit: 50, TIN: csTIN["ab"]})
	if err != nil || len(items) != 1 || items[0].Email == nil || *items[0].Email != "new@x.ng" {
		t.Errorf("member of B reads %+v (err %v), want the A member's email edit", items, err)
	}
}

func TestClientScope_TheBackfillLinksNewTINsAndLeavesExistingOnes(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "CS dir backfill")
	entA, entB := seedEntity(t, super, tenant, "Client A"), seedEntity(t, super, tenant, "Client B")
	admin := csMember(t, super, tenant, "admin")
	const existing, n1, n2 = "12345678-0401", "12345678-0402", "12345678-0403"
	fillCreate(admin, t, invoice.NewStore(app), entB, "BF-B0", existing, "Existing Ltd", "e@x.ng")
	for i, r := range []struct{ ent, tin string }{{entA, n1}, {entB, n1}, {entB, n2}, {entA, existing}} {
		if _, err := super.Exec(context.Background(),
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number, buyer_tin) VALUES ($1, $2, $3, $4)`,
			tenant, r.ent, "BF-"+string(rune('1'+i)), r.tin); err != nil {
			t.Fatalf("seed invoice: %v", err)
		}
	}

	res, err := customer.Backfill(context.Background(), app, tenant, false)
	if err != nil || res.Created != 2 || res.Existing != 1 {
		t.Fatalf("Backfill = %+v (err %v), want Created 2 and Existing 1", res, err)
	}
	csWantLinks(t, super, tenant, n1, entA, entB)
	csWantLinks(t, super, tenant, n2, entB)
	csWantLinks(t, super, tenant, existing, entB)
	for _, tin := range []string{n1, n2} {
		if by, _ := csLinks(t, super, tenant, tin); by != nil {
			t.Errorf("backfilled %s has created_by %s, want NULL", tin, *by)
		}
	}
	memberA := csMember(t, super, tenant, "reviewer", entA)
	items, _, err := customer.NewStore(app).List(memberA, customer.ListFilter{Limit: 50})
	if got := tins(items); err != nil || !slices.Equal(got, []string{n1}) {
		t.Errorf("member of A lists %v (err %v), want only %s: N2 is B-only and the existing record keeps its links", got, err, n1)
	}
}

// csWaitForTxWaiter polls until a session waits on another transaction's lock; it fails if the
// second writer returns first, since then no ordering was exercised.
func csWaitForTxWaiter(t *testing.T, super *pgxpool.Pool, done <-chan error) {
	t.Helper()
	for range 500 {
		select {
		case err := <-done:
			t.Fatalf("the second fill returned (%v) without waiting on the first", err)
		default:
		}
		var n int
		if err := super.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity
			  WHERE wait_event_type = 'Lock' AND wait_event IN ('transactionid', 'tuple') AND datname = current_database()`).Scan(&n); err != nil {
			t.Fatalf("read waiters: %v", err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no session is waiting on the first fill's transaction")
}

// raceFills runs the first fill and holds its transaction open, starts the second, waits until the
// second blocks, then commits the first. It returns the second fill's error.
func raceFills(t *testing.T, w csWorld, ctx1 context.Context, inv1 string, ctx2 context.Context, inv2 string) error {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	defer free()
	filled, done1, done2 := make(chan error, 1), make(chan error, 1), make(chan error, 1)
	go func() {
		done1 <- db.WithinRequestTenantTx(ctx1, w.app, func(tx pgx.Tx) error {
			err := customer.FillFromInvoiceTx(ctx1, tx, inv1)
			filled <- err
			<-release
			return err
		})
	}()
	if err := <-filled; err != nil {
		free()
		t.Fatalf("first fill: %v", err)
	}
	go func() {
		done2 <- db.WithinRequestTenantTx(ctx2, w.app, func(tx pgx.Tx) error {
			return customer.FillFromInvoiceTx(ctx2, tx, inv2)
		})
	}()
	csWaitForTxWaiter(t, w.super, done2)
	free()
	if err := <-done1; err != nil {
		t.Fatalf("first fill commit: %v", err)
	}
	return <-done2
}

// csBlankInvoice files an invoice with no buyer TIN, then sets the TIN as the superuser so no fill has run.
func csBlankInvoice(t *testing.T, w csWorld, entity, number, tin string) string {
	t.Helper()
	inv, err := w.invs.Create(w.admin, invoice.CreateInput{EntityID: entity, InvoiceNumber: number})
	if err != nil {
		t.Fatalf("Create %s: %v", number, err)
	}
	if _, err := w.super.Exec(context.Background(), `UPDATE invoices SET buyer_tin = $1 WHERE id = $2`, tin, inv.ID); err != nil {
		t.Fatalf("set buyer_tin: %v", err)
	}
	return inv.ID
}

func TestClientScope_RacingFirstFillsOfOneTINKeepEveryReachableLink(t *testing.T) {
	const tin = "12345678-0501"
	cases := []struct {
		name   string
		second func(t *testing.T, w csWorld) context.Context
		want   func(w csWorld) []string
	}{
		{"second caller admin", func(t *testing.T, w csWorld) context.Context { return w.admin },
			func(w csWorld) []string { return []string{w.entA, w.entB} }},
		{"second caller limited to A and B", func(t *testing.T, w csWorld) context.Context {
			return csMember(t, w.super, w.tenant, "reviewer", w.entA, w.entB)
		}, func(w csWorld) []string { return []string{w.entA, w.entB} }},
		{"second caller limited to B only", func(t *testing.T, w csWorld) context.Context {
			return csMember(t, w.super, w.tenant, "reviewer", w.entB)
		}, func(w csWorld) []string { return []string{w.entA} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newCSWorld(t, "CS dir race "+c.name)
			invA, invB := csBlankInvoice(t, w, w.entA, "RACE-A", tin), csBlankInvoice(t, w, w.entB, "RACE-B", tin)
			if err := raceFills(t, w, w.limited, invA, c.second(t, w), invB); err != nil {
				t.Errorf("second fill: %v, want nil", err)
			}
			csWantLinks(t, w.super, w.tenant, tin, c.want(w)...)
			if by, _ := csLinks(t, w.super, w.tenant, tin); by != nil {
				t.Errorf("created_by = %s, want NULL", *by)
			}
		})
	}

	t.Run("two admin fills of an existing record", func(t *testing.T) {
		w := newCSWorld(t, "CS dir race existing")
		entX := seedEntity(t, w.super, w.tenant, "Client X")
		fillCreate(w.admin, t, w.invs, entX, "RACE-X", tin, "Existing Ltd", "x@x.ng")
		invA, invB := csBlankInvoice(t, w, w.entA, "RACE-A", tin), csBlankInvoice(t, w, w.entB, "RACE-B", tin)
		if err := raceFills(t, w, w.admin, invA, w.admin, invB); err != nil {
			t.Errorf("second fill: %v, want nil", err)
		}
		csWantLinks(t, w.super, w.tenant, tin, entX, w.entA, w.entB)
	})
}
