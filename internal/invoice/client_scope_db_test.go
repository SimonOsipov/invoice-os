package invoice

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
