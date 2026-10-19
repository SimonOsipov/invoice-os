package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// csFixture is one firm tenant with clients A and B, an invoice on each, an admin, a
// reviewer limited to A and a preparer with no clients.
type csFixture struct {
	super, app *pgxpool.Pool
	store      *Store
	tenant     string
	entA, entB string
	invA, invB string
	admin      context.Context
	limited    context.Context
	unassigned context.Context
}

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

func newCSFixture(t *testing.T, label string) csFixture {
	t.Helper()
	super, app := dbTestPools(t)
	f := csFixture{super: super, app: app, store: NewStore(app)}
	f.tenant = seedTenant(t, super, label)
	f.entA = seedEntity(t, super, f.tenant, "Client A")
	f.entB = seedEntity(t, super, f.tenant, "Client B")
	f.invA = seedInvoice(t, super, f.tenant, f.entA, "CS-A-1")
	f.invB = seedInvoiceAtStatus(t, super, f.tenant, f.entB, "CS-B-1", StatusValidated)
	f.admin = csMember(t, super, f.tenant, "admin")
	f.limited = csMember(t, super, f.tenant, "reviewer", f.entA)
	f.unassigned = csMember(t, super, f.tenant, "preparer")
	return f
}

func TestClientScope_CreateOutsideTheSetIsNotFound(t *testing.T) {
	f := newCSFixture(t, "CS create outside")
	before := auditCount(t, f.app, f.tenant, "invoice.created")

	_, err := f.store.Create(f.limited, CreateInput{
		EntityID: f.entB, InvoiceNumber: "CS-NEW",
		LineItems: []LineItemInput{{Description: strPtr("x")}},
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Create(entity B) as a reviewer limited to A: err = %v, want ErrNotFound", err)
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE entity_id = $1 AND invoice_number = 'CS-NEW'`, f.entB); n != 0 {
		t.Errorf("invoices written = %d, want 0", n)
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM line_items l JOIN invoices i ON i.id = l.invoice_id WHERE i.tenant_id = $1 AND i.invoice_number = 'CS-NEW'`, f.tenant); n != 0 {
		t.Errorf("line items written = %d, want 0", n)
	}
	if n := auditCount(t, f.app, f.tenant, "invoice.created"); n != before {
		t.Errorf("invoice.created rows = %d, want %d", n, before)
	}
}

func TestClientScope_CreateUnknownAndForeignEntityAreNotFound(t *testing.T) {
	f := newCSFixture(t, "CS create unknown")
	otherTenant := seedTenant(t, f.super, "CS create foreign")
	foreign := seedEntity(t, f.super, otherTenant, "Foreign client")

	for name, entity := range map[string]string{"unknown": uuid.NewString(), "another tenant's": foreign} {
		_, err := f.store.Create(f.admin, CreateInput{EntityID: entity, InvoiceNumber: "CS-X"})
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Create(%s entity) as admin: err = %v, want ErrNotFound", name, err)
		}
	}
}

func TestClientScope_CreateInsideTheSetSucceeds(t *testing.T) {
	f := newCSFixture(t, "CS create inside")
	inv, err := f.store.Create(f.limited, CreateInput{EntityID: f.entA, InvoiceNumber: "CS-A-NEW"})
	if err != nil {
		t.Fatalf("Create(entity A) as a reviewer limited to A: %v", err)
	}
	if inv.EntityID != f.entA {
		t.Errorf("entity = %s, want %s", inv.EntityID, f.entA)
	}
}

func TestClientScope_CreateMalformedEntityStaysValidation(t *testing.T) {
	f := newCSFixture(t, "CS create malformed")
	_, err := f.store.Create(f.admin, CreateInput{EntityID: "x", InvoiceNumber: "CS-M"})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Create(entity \"x\") err = %v, want ErrValidation", err)
	}
}

func TestClientScope_ListByEntityOutsideTheSetIsNotFound(t *testing.T) {
	f := newCSFixture(t, "CS list by entity")
	for name, entity := range map[string]string{"client B": f.entB, "unknown": uuid.NewString()} {
		if _, _, err := f.store.List(f.limited, ListFilter{Limit: 50, EntityID: entity}); !errors.Is(err, ErrNotFound) {
			t.Errorf("List(%s) as a reviewer limited to A: err = %v, want ErrNotFound", name, err)
		}
	}
	items, total, err := f.store.List(f.limited, ListFilter{Limit: 50, EntityID: f.entA})
	if err != nil || total != 1 || len(items) != 1 || items[0].ID != f.invA {
		t.Errorf("List(client A) = %d items, total %d, err %v, want [%s]", len(items), total, err, f.invA)
	}
}

func TestClientScope_ListWithoutEntityShowsOnlyTheSet(t *testing.T) {
	f := newCSFixture(t, "CS list all")
	a2 := seedInvoice(t, f.super, f.tenant, f.entA, "CS-A-2")
	seedInvoice(t, f.super, f.tenant, f.entB, "CS-B-2")
	seedInvoice(t, f.super, f.tenant, f.entB, "CS-B-3")

	items, total, err := f.store.List(f.limited, ListFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List as a reviewer limited to A: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("limited List = %d items, total %d, want 2 and 2", len(items), total)
	}
	for _, inv := range items {
		if inv.EntityID != f.entA || (inv.ID != f.invA && inv.ID != a2) {
			t.Errorf("limited List returned %s of entity %s", inv.ID, inv.EntityID)
		}
	}
	if _, total, err := f.store.List(f.unassigned, ListFilter{Limit: 50}); err != nil || total != 0 {
		t.Errorf("unassigned List: total %d, err %v, want 0", total, err)
	}
	if _, total, err := f.store.List(f.admin, ListFilter{Limit: 50}); err != nil || total != 5 {
		t.Errorf("admin List: total %d, err %v, want 5", total, err)
	}
}

func TestClientScope_EveryInvoiceRouteRefusesAnotherClientsInvoice(t *testing.T) {
	f := newCSFixture(t, "CS every route")
	snapshot := func() string {
		var row string
		if err := f.super.QueryRow(context.Background(), `SELECT row_to_json(i)::text FROM invoices i WHERE id = $1`, f.invB).Scan(&row); err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		return row
	}
	history := func() int {
		return mustCount(t, f.super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, f.invB)
	}
	rowBefore, historyBefore := snapshot(), history()
	total := "999.99"
	gate := NewGate(f.store, NewValidator("http://127.0.0.1:1", gapiS2SToken, nil))

	calls := map[string]func() error{
		"Get":            func() error { _, err := f.store.Get(f.limited, f.invB); return err },
		"History":        func() error { _, err := f.store.History(f.limited, f.invB); return err },
		"SourceDocument": func() error { _, err := f.store.SourceDocument(f.limited, f.invB); return err },
		"Edit": func() error {
			_, err := f.store.Edit(f.limited, f.invB, EditInput{UpdateInput: UpdateInput{Total: &total}})
			return err
		},
		"Transition":       func() error { _, err := f.store.Transition(f.limited, f.invB, StatusDraft); return err },
		"KeepAsIs":         func() error { _, err := f.store.KeepAsIs(f.limited, f.invB, "r"); return err },
		"UnkeepAsIs":       func() error { _, err := f.store.UnkeepAsIs(f.limited, f.invB); return err },
		"ResolveOutside":   func() error { _, err := f.store.ResolveOutside(f.limited, f.invB, "r"); return err },
		"UnresolveOutside": func() error { _, err := f.store.UnresolveOutside(f.limited, f.invB); return err },
		"Gate.Validate":    func() error { _, _, err := gate.Validate(f.limited, f.invB); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s on client B's invoice: err = %v, want ErrNotFound", name, err)
		}
	}
	if rowAfter := snapshot(); rowAfter != rowBefore {
		t.Errorf("client B's invoice changed:\n before %s\n after  %s", rowBefore, rowAfter)
	}
	if n := history(); n != historyBefore {
		t.Errorf("status history rows = %d, want %d", n, historyBefore)
	}
	if _, err := f.store.Get(f.limited, f.invA); err != nil {
		t.Errorf("Get(own invoice) control: %v", err)
	}
}

func TestClientScope_ExplainAndUBLRefuseAnotherClientsInvoice(t *testing.T) {
	f := newCSFixture(t, "CS explain ubl")
	ident, _ := auth.IdentityFromContext(f.limited)

	rec := doExplain(t, NewExplainer(f.store.Get, newExplainRecorder()).Explain, nil, &ident, f.invB, explainBody)
	if rec.Code != http.StatusNotFound || explainErrorOf(t, rec) != "not found" {
		t.Errorf("explain: got %d %s, want 404 not found", rec.Code, rec.Body.String())
	}

	r := httptest.NewRequest(http.MethodGet, "/v1/invoices/"+f.invB+"/ubl", nil)
	r.SetPathValue("id", f.invB)
	r = r.WithContext(auth.WithIdentity(r.Context(), ident))
	w := httptest.NewRecorder()
	UBLHandler(f.store.Get, nil).ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("ubl: got %d %s, want 404", w.Code, w.Body.String())
	}
}

func csServe(h http.HandlerFunc, ctx context.Context, method, target, id, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body)).WithContext(ctx)
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestClientScope_InvoiceRoutesAnswerOneNotFoundForOutsideUnknownAndForeign(t *testing.T) {
	f := newCSFixture(t, "CS same 404")
	otherTenant := seedTenant(t, f.super, "CS same 404 foreign")
	foreignInv := seedInvoiceAtStatus(t, f.super, otherTenant, seedEntity(t, f.super, otherTenant, "Foreign client"), "CS-F-1", StatusValidated)
	gate := NewGate(f.store, NewValidator("http://127.0.0.1:1", gapiS2SToken, nil))

	routes := []struct {
		name, method, body string
		h                  http.HandlerFunc
	}{
		{"GET /v1/invoices/{id}", "GET", "", GetHandler(f.store.Get, f.store.CallerRole, f.store.ApprovalFacts, nil)},
		{"GET /v1/invoices/{id}/history", "GET", "", HistoryHandler(f.store.History, nil)},
		{"GET /v1/invoices/{id}/source-document", "GET", "", SourceDocumentHandler(f.store.SourceDocument, nil)},
		{"GET /v1/invoices/{id}/ubl", "GET", "", UBLHandler(f.store.Get, nil)},
		{"PATCH /v1/invoices/{id}", "PATCH", `{"total":"999.99"}`, EditHandler(f.store.Edit, nil)},
		{"POST /v1/invoices/{id}/transitions", "POST", `{"target":"draft"}`, TransitionHandler(f.store.Transition, f.store.CallerRole, nil)},
		{"POST /v1/invoices/{id}/keep-as-is", "POST", `{"reason":"r"}`, KeepAsIsHandler(f.store.KeepAsIs, nil)},
		{"DELETE /v1/invoices/{id}/keep-as-is", "DELETE", "", UnkeepAsIsHandler(f.store.UnkeepAsIs, nil)},
		{"POST /v1/invoices/{id}/resolved-outside", "POST", `{"reason":"r"}`, ResolveOutsideHandler(f.store.ResolveOutside, nil)},
		{"DELETE /v1/invoices/{id}/resolved-outside", "DELETE", "", UnresolveOutsideHandler(f.store.UnresolveOutside, nil)},
		{"POST /v1/invoices/{id}/validate", "POST", "", ValidateHandler(gate.Validate, nil)},
	}
	for _, rt := range routes {
		outside := csServe(rt.h, f.limited, rt.method, "/", f.invB, rt.body)
		if outside.Code != http.StatusNotFound || !strings.Contains(outside.Body.String(), "not found") {
			t.Errorf("%s on client B's invoice: %d %s, want 404 not found", rt.name, outside.Code, outside.Body.String())
			continue
		}
		for who, id := range map[string]string{"unknown": uuid.NewString(), "another tenant's": foreignInv} {
			got := csServe(rt.h, f.limited, rt.method, "/", id, rt.body)
			if got.Code != outside.Code || got.Body.String() != outside.Body.String() {
				t.Errorf("%s on %s id: %d %s, differs from the outside-the-set answer %d %s",
					rt.name, who, got.Code, got.Body.String(), outside.Code, outside.Body.String())
			}
		}
	}
	if w := csServe(routes[0].h, f.limited, "GET", "/", f.invA, ""); w.Code != http.StatusOK {
		t.Errorf("GET own invoice control: %d %s, want 200", w.Code, w.Body.String())
	}

	ident, _ := auth.IdentityFromContext(f.limited)
	explain := NewExplainer(f.store.Get, newExplainRecorder()).Explain
	outside := doExplain(t, explain, nil, &ident, f.invB, explainBody)
	for who, id := range map[string]string{"unknown": uuid.NewString(), "another tenant's": foreignInv} {
		got := doExplain(t, explain, nil, &ident, id, explainBody)
		if outside.Code != http.StatusNotFound || got.Code != outside.Code || got.Body.String() != outside.Body.String() {
			t.Errorf("explain on %s id: %d %s, outside-the-set %d %s", who, got.Code, got.Body.String(), outside.Code, outside.Body.String())
		}
	}
}

func TestClientScope_CreateAndListRoutesAnswerOneNotFoundAndWriteNothing(t *testing.T) {
	f := newCSFixture(t, "CS create list 404")
	otherTenant := seedTenant(t, f.super, "CS create list foreign")
	foreign := seedEntity(t, f.super, otherTenant, "Foreign client")
	create := CreateHandler(f.store.Create, nil)
	list := ListHandler(f.store.List, f.store.RowFacts, nil)
	before := auditCount(t, f.app, f.tenant, "invoice.created")

	post := func(entity string) *httptest.ResponseRecorder {
		return csServe(create, f.limited, "POST", "/v1/invoices", "", `{"entity_id":"`+entity+`","invoice_number":"CS-H"}`)
	}
	get := func(entity string) *httptest.ResponseRecorder {
		return csServe(list, f.limited, "GET", "/v1/invoices?entity_id="+entity, "", "")
	}
	outsidePost, outsideGet := post(f.entB), get(f.entB)
	if outsidePost.Code != http.StatusNotFound || outsideGet.Code != http.StatusNotFound {
		t.Fatalf("outside the set: POST %d %s, GET %d %s, want 404 and 404", outsidePost.Code, outsidePost.Body.String(), outsideGet.Code, outsideGet.Body.String())
	}
	for who, e := range map[string]string{"unknown": uuid.NewString(), "another tenant's": foreign} {
		if got := post(e); got.Code != outsidePost.Code || got.Body.String() != outsidePost.Body.String() {
			t.Errorf("POST with %s entity: %d %s, differs from outside-the-set %d %s", who, got.Code, got.Body.String(), outsidePost.Code, outsidePost.Body.String())
		}
		if got := get(e); got.Code != outsideGet.Code || got.Body.String() != outsideGet.Body.String() {
			t.Errorf("GET with %s entity: %d %s, differs from outside-the-set %d %s", who, got.Code, got.Body.String(), outsideGet.Code, outsideGet.Body.String())
		}
	}
	if w := post("x"); w.Code != http.StatusBadRequest {
		t.Errorf("POST malformed entity_id: %d, want 400", w.Code)
	}
	if w := get("x"); w.Code != http.StatusBadRequest {
		t.Errorf("GET malformed entity_id: %d, want 400", w.Code)
	}
	if w := csServe(create, f.limited, "POST", "/v1/invoices", "", `{"invoice_number":"CS-H"}`); w.Code != http.StatusBadRequest {
		t.Errorf("POST missing entity_id: %d, want 400", w.Code)
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE tenant_id = $1 AND invoice_number = 'CS-H'`, f.tenant); n != 0 {
		t.Errorf("invoices written by refused POSTs = %d, want 0", n)
	}
	if n := auditCount(t, f.app, f.tenant, "invoice.created"); n != before {
		t.Errorf("invoice.created rows = %d, want %d", n, before)
	}

	var page struct {
		Invoices   []struct{ ID string }
		Pagination struct{ Total int }
	}
	for _, c := range []struct {
		target string
		want   int
	}{{"/v1/invoices?entity_id=" + f.entA, 1}, {"/v1/invoices", 1}} {
		w := csServe(list, f.limited, "GET", c.target, "", "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s, want 200", c.target, w.Code, w.Body.String())
		}
		page.Invoices = nil
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode %s: %v", c.target, err)
		}
		if len(page.Invoices) != c.want || page.Pagination.Total != c.want || page.Invoices[0].ID != f.invA {
			t.Errorf("GET %s: %d rows, total %d, want %d (client A's invoice)", c.target, len(page.Invoices), page.Pagination.Total, c.want)
		}
	}
}

func TestClientScope_AdminAndAllClientsMemberStillReachEveryClient(t *testing.T) {
	f := newCSFixture(t, "CS full access")
	allUser := uuid.NewString()
	if _, err := f.super.Exec(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id, role, status, all_clients) VALUES ($1, $2, 'preparer', 'active', true)`,
		f.tenant, allUser); err != nil {
		t.Fatalf("seed all-clients member: %v", err)
	}
	allClients := auth.WithIdentity(context.Background(), auth.Identity{Subject: allUser, Role: "authenticated", TenantID: f.tenant})

	for name, ctx := range map[string]context.Context{"admin": f.admin, "all-clients preparer": allClients} {
		if _, err := f.store.Get(ctx, f.invB); err != nil {
			t.Errorf("%s Get(client B's invoice): %v", name, err)
		}
		items, total, err := f.store.List(ctx, ListFilter{Limit: 50, EntityID: f.entB})
		if err != nil || total != 1 || len(items) != 1 || items[0].ID != f.invB {
			t.Errorf("%s List(client B) = %d items, total %d, err %v, want [%s]", name, len(items), total, err, f.invB)
		}
	}
	for name, ctx := range map[string]context.Context{"admin": f.admin, "all-clients preparer": allClients} {
		inv, err := f.store.Create(ctx, CreateInput{EntityID: f.entB, InvoiceNumber: "CS-FULL-" + name[:3]})
		if err != nil || inv.EntityID != f.entB {
			t.Errorf("%s Create(client B): %v", name, err)
		}
	}
}

func TestClientScope_BatchSubmitRefusesAnotherClientsInvoice(t *testing.T) {
	f := newCSFixture(t, "CS batch submit")
	submitter := NewSubmitter(f.store, nil)
	_, err := submitter.BatchSubmit(f.limited, BatchSubmitInput{InvoiceIDs: []string{f.invB}, IdempotencyKey: "cs-batch"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("BatchSubmit(client B's invoice) err = %v, want ErrNotFound", err)
	}
	if got := mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE id = $1 AND status = 'validated'`, f.invB); got != 1 {
		t.Errorf("client B's invoice no longer validated after a refused submit")
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND to_status = 'queued'`, f.invB); n != 0 {
		t.Errorf("queued history rows for client B's invoice = %d, want 0", n)
	}
}

func TestClientScope_ViolationSummaryCountsOnlyTheSet(t *testing.T) {
	f := newCSFixture(t, "CS violation summary")
	bA := seedImportBatch(t, f.super, f.tenant, f.entA)
	bB := seedImportBatch(t, f.super, f.tenant, f.entB)
	r1 := `[{"rule_key":"cs-r1","severity":"error","message":"a"}]`
	seedInvoiceWithBatchAndStatus(t, f.super, f.tenant, f.entA, "CS-VS-A1", bA, string(StatusDraft), r1)
	seedInvoiceWithBatchAndStatus(t, f.super, f.tenant, f.entA, "CS-VS-A2", bA, string(StatusDraft), r1)
	seedInvoiceWithBatchAndStatus(t, f.super, f.tenant, f.entB, "CS-VS-B1", bB, string(StatusDraft),
		`[{"rule_key":"cs-rB","severity":"error","message":"b"}]`)

	got, err := f.store.ViolationSummary(f.limited, []string{bA, bB})
	if err != nil {
		t.Fatalf("limited ViolationSummary([bA, bB]): %v", err)
	}
	if len(got) != 1 || got[0] != (RuleCount{RuleKey: "cs-r1", Invoices: 2}) {
		t.Errorf("limited ViolationSummary([bA, bB]) = %+v, want [{cs-r1 2}] (client A only)", got)
	}
	got, err = f.store.ViolationSummary(f.limited, []string{bB})
	if err != nil || len(got) != 0 {
		t.Errorf("limited ViolationSummary([bB]) = %+v, err %v, want []", got, err)
	}
	got, err = f.store.ViolationSummary(f.admin, []string{bA, bB})
	if err != nil {
		t.Fatalf("admin ViolationSummary: %v", err)
	}
	want := []RuleCount{{RuleKey: "cs-r1", Invoices: 2}, {RuleKey: "cs-rB", Invoices: 1}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("admin ViolationSummary([bA, bB]) = %+v, want %+v", got, want)
	}
}

func TestClientScope_AwaitingApprovalListStopsAtTheSet(t *testing.T) {
	f := newCSFixture(t, "CS awaiting approval")
	policy := seedApprovalPolicyFor(t, f.super, f.tenant, "CS policy")
	version := seedApprovalPolicyVersionFor(t, f.super, f.tenant, policy)
	seedApprovalStepFor(t, f.super, f.tenant, version, approvalStepSpecFor{
		Ord: 0, Kind: "approval", WorkflowRoleKey: strPtr("finance-lead"),
	})
	activateApprovalPolicyVersionFor(t, f.super, version)
	validA := seedInvoiceAtStatus(t, f.super, f.tenant, f.entA, "CS-AW-A", StatusValidated)

	awaiting := func(ctx context.Context) ([]Invoice, int) {
		items, total, err := f.store.List(ctx, ListFilter{Limit: 50, AwaitingApproval: true})
		if err != nil {
			t.Fatalf("List(awaiting approval): %v", err)
		}
		return items, total
	}
	items, total := awaiting(f.limited)
	if total != 1 || len(items) != 1 || items[0].ID != validA {
		t.Errorf("limited awaiting = %d items, total %d, want [%s]", len(items), total, validA)
	}
	if items, total := awaiting(f.unassigned); total != 0 || len(items) != 0 {
		t.Errorf("unassigned awaiting = %d items, total %d, want 0", len(items), total)
	}
	items, total = awaiting(f.admin)
	if total != 2 || len(items) != 2 || !idSet(items)[validA] || !idSet(items)[f.invB] {
		t.Errorf("admin awaiting = %v, total %d, want [%s %s]", sortedIDs(idSet(items)), total, validA, f.invB)
	}
}
