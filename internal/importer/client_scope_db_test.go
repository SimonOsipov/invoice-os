package importer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// csFixture is one firm tenant with clients A and B, an admin and a reviewer limited to A.
type csFixture struct {
	super, app *pgxpool.Pool
	store      *Store
	svc        *Service
	docSvc     *document.Service
	tenant     string
	entA, entB string
	admin      context.Context
	limited    context.Context
	limitedID  auth.Identity
}

func csMember(t *testing.T, super *pgxpool.Pool, tenant, role string, clients ...string) auth.Identity {
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
	return auth.Identity{Subject: user, Role: "authenticated", TenantID: tenant}
}

func newCSFixture(t *testing.T, label string) csFixture {
	t.Helper()
	super, app := dbTestPools(t)
	f := csFixture{super: super, app: app, store: NewStore(app), svc: newTestService(app)}
	f.docSvc = document.NewService(document.NewStore(app), newMemObjects())
	f.tenant = seedTenant(t, super, label)
	f.entA = seedEntity(t, super, f.tenant, "Client A")
	f.entB = seedEntity(t, super, f.tenant, "Client B")
	f.admin = auth.WithIdentity(context.Background(), csMember(t, super, f.tenant, "admin"))
	f.limitedID = csMember(t, super, f.tenant, "reviewer", f.entA)
	f.limited = auth.WithIdentity(context.Background(), f.limitedID)
	return f
}

func csCount(t *testing.T, super *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count (%s): %v", query, err)
	}
	return n
}

func csBatch(t *testing.T, super *pgxpool.Pool, tenant, entity string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO import_batches (tenant_id, entity_id, status, rows_total, rows_valid, rows_invalid, errors)
		 VALUES ($1, $2, 'completed', 0, 0, 0, '[]'::jsonb) RETURNING id`, tenant, entity).Scan(&id); err != nil {
		t.Fatalf("seed import_batches: %v", err)
	}
	return id
}

func csDocumentReads(t *testing.T, super *pgxpool.Pool, tenant string) int {
	return csCount(t, super, `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'document.read'`, tenant)
}

func TestClientScope_ImportOutsideTheSetIsNotFound(t *testing.T) {
	f := newCSFixture(t, "CS import outside")
	mapping := map[string]string{"invoice_number": "Inv No"}
	header := []string{"Inv No"}
	rows := [][]string{{"CS-1"}}

	_, err := f.svc.Import(f.limited, f.entB, "data.csv", "", 1, mapping, header, rows, false, "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Import(entity B) as a reviewer limited to A: err = %v, want ErrNotFound", err)
	}
	if n := csCount(t, f.super, `SELECT count(*) FROM import_batches WHERE entity_id = $1`, f.entB); n != 0 {
		t.Errorf("batches for B = %d, want 0", n)
	}
	if _, err := f.svc.Import(f.limited, f.entA, "data.csv", "", 1, mapping, header, rows, false, ""); err != nil {
		t.Errorf("Import(entity A) as a reviewer limited to A: %v", err)
	}
}

func TestClientScope_BatchOfAnotherClientIsNotFound(t *testing.T) {
	f := newCSFixture(t, "CS batch")
	batchA, batchB := csBatch(t, f.super, f.tenant, f.entA), csBatch(t, f.super, f.tenant, f.entB)

	if _, err := f.store.GetBatch(f.limited, batchB); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetBatch(B's batch) as a reviewer limited to A: err = %v, want ErrNotFound", err)
	}
	if b, err := f.store.GetBatch(f.limited, batchA); err != nil || b.ID != batchA {
		t.Errorf("GetBatch(A's batch) = %v, %v, want the batch", b.ID, err)
	}
	if b, err := f.store.GetBatch(f.admin, batchB); err != nil || b.ID != batchB {
		t.Errorf("GetBatch(B's batch) as admin = %v, %v, want the batch", b.ID, err)
	}
}

func TestClientScope_ImportDocumentOutsideTheSetIsNotFound(t *testing.T) {
	f := newCSFixture(t, "CS import document")
	doc := docSeedDocument(t, f.super, f.tenant)
	docSeedExtraction(t, f.super, f.tenant, doc, docCleanValues("CS-DOC-1"))

	_, err := f.svc.ImportDocument(f.limited, f.entB, doc)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ImportDocument(B) as a reviewer limited to A: err = %v, want ErrNotFound", err)
	}
	if n := csCount(t, f.super, `SELECT count(*) FROM import_batches WHERE tenant_id = $1`, f.tenant); n != 0 {
		t.Errorf("import_batches = %d, want 0", n)
	}
	if _, err := f.svc.ImportDocument(f.limited, f.entA, doc); err != nil {
		t.Errorf("ImportDocument(A) as a reviewer limited to A: %v", err)
	}
}

func TestClientScope_ImportDocumentWithAForeignEntityIsNotFound(t *testing.T) {
	f := newCSFixture(t, "CS import document foreign")
	other := seedTenant(t, f.super, "CS import document foreign other")
	foreign := seedEntity(t, f.super, other, "Foreign client")
	doc := docSeedDocument(t, f.super, f.tenant)
	docSeedExtraction(t, f.super, f.tenant, doc, docCleanValues("CS-DOC-2"))

	for name, entity := range map[string]string{"another tenant's": foreign, "unknown": uuid.NewString()} {
		if _, err := f.svc.ImportDocument(f.admin, entity, doc); !errors.Is(err, ErrNotFound) {
			t.Errorf("ImportDocument(%s entity) as admin: err = %v, want ErrNotFound", name, err)
		}
	}
	if n := csCount(t, f.super, `SELECT count(*) FROM import_batches WHERE tenant_id IN ($1, $2)`, f.tenant, other); n != 0 {
		t.Errorf("import_batches = %d, want 0", n)
	}
	if _, err := f.svc.ImportDocument(f.admin, "nope", doc); !errors.Is(err, ErrValidation) {
		t.Errorf("ImportDocument(malformed entity): err = %v, want ErrValidation", err)
	}
}

func TestClientScope_SupplyNumberOutsideTheSetIsNotFound(t *testing.T) {
	f := newCSFixture(t, "CS supply number")
	doc := docSeedDocument(t, f.super, f.tenant)
	docSeedExtraction(t, f.super, f.tenant, doc, docNoNumberValues())

	_, err := f.svc.SupplyInvoiceNumber(f.limited, f.entB, doc, "INV-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("SupplyInvoiceNumber(B) as a reviewer limited to A: err = %v, want ErrNotFound", err)
	}
	if n := countInvoicesCitingDocument(t, f.super, doc); n != 0 {
		t.Errorf("invoices citing the document = %d, want 0", n)
	}
	if _, err := f.svc.SupplyInvoiceNumber(f.limited, f.entA, doc, "INV-1"); err != nil {
		t.Errorf("SupplyInvoiceNumber(A) as a reviewer limited to A: %v", err)
	}
}

func csGet(h http.Handler, id auth.Identity, method, target, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestClientScope_SavedMappingOutsideTheSetIsNotFound(t *testing.T) {
	f := newCSFixture(t, "CS saved mapping")
	header := []string{"Inv No"}
	if err := f.store.SaveMapping(f.admin, f.entB, header, map[string]string{"invoice_number": "Inv No"}); err != nil {
		t.Fatalf("SaveMapping(B): %v", err)
	}
	doc := storeDocumentAs(t, f.docSvc, f.tenant, "data.csv", "", csvBody(t, header, [][]string{{"1"}}))
	h := SavedMappingHandler(f.docSvc.Open, f.store.SavedMapping, f.store.EntityVisible, nil)
	before := csDocumentReads(t, f.super, f.tenant)

	for name, entity := range map[string]string{"client B": f.entB, "unknown": uuid.NewString()} {
		rec := csGet(h, f.limitedID, "GET", "/v1/imports/saved-mapping?entity_id="+entity+"&document_id="+doc.ID, "")
		if rec.Code != http.StatusNotFound || strings.TrimSpace(rec.Body.String()) != `{"error":"not found"}` {
			t.Errorf("%s: answer = %d %s, want 404 {\"error\":\"not found\"}", name, rec.Code, rec.Body.String())
		}
	}
	if n := csDocumentReads(t, f.super, f.tenant); n != before {
		t.Errorf("document.read rows = %d, want %d", n, before)
	}
	rec := csGet(h, f.limitedID, "GET", "/v1/imports/saved-mapping?entity_id="+f.entA+"&document_id="+doc.ID, "")
	if rec.Code != http.StatusOK {
		t.Errorf("client A: status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestClientScope_SuggestOutsideTheSetIsNotFoundWithoutAnAICall(t *testing.T) {
	f := newCSFixture(t, "CS suggest")
	doc := storeDocumentAs(t, f.docSvc, f.tenant, "data.csv", "", csvBody(t, []string{"Inv No"}, [][]string{{"1"}}))
	suggester := &fakeSuggester{enabled: true}
	h := SuggestMappingHandler(f.docSvc.Open, f.store.SavedMapping, f.store.EntityVisible, suggester, nil)

	rec := csGet(h, f.limitedID, "POST", "/v1/imports/suggest-mapping", suggestReqBody(f.entB, doc.ID))

	if rec.Code != http.StatusNotFound || strings.TrimSpace(rec.Body.String()) != `{"error":"not found"}` {
		t.Errorf("answer = %d %s, want 404 {\"error\":\"not found\"}", rec.Code, rec.Body.String())
	}
	if n := len(suggester.calls); n != 0 {
		t.Errorf("AI calls = %d, want 0", n)
	}
}
