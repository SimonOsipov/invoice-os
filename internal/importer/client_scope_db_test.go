package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
	h := SavedMappingHandler(f.docSvc.OpenLinked, f.store.SavedMapping, f.store.EntityVisible, nil)
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
	h := SuggestMappingHandler(f.docSvc.OpenLinked, f.store.SavedMapping, f.store.EntityVisible, suggester, nil)

	rec := csGet(h, f.limitedID, "POST", "/v1/imports/suggest-mapping", suggestReqBody(f.entB, doc.ID))

	if rec.Code != http.StatusNotFound || strings.TrimSpace(rec.Body.String()) != `{"error":"not found"}` {
		t.Errorf("answer = %d %s, want 404 {\"error\":\"not found\"}", rec.Code, rec.Body.String())
	}
	if n := len(suggester.calls); n != 0 {
		t.Errorf("AI calls = %d, want 0", n)
	}
}

// csInvisible names the three entity ids a limited reviewer must get one answer for.
func csInvisible(f csFixture, foreign string) map[string]string {
	return map[string]string{"client B": f.entB, "unknown": uuid.NewString(), "another tenant's": foreign}
}

func TestClientScope_DocumentRoutesAnswerOneNotFoundAndWriteNothing(t *testing.T) {
	f := newCSFixture(t, "CS document routes")
	other := seedTenant(t, f.super, "CS document routes other")
	foreign := seedEntity(t, f.super, other, "Foreign client")
	docImp := docSeedDocument(t, f.super, f.tenant)
	docSeedExtraction(t, f.super, f.tenant, docImp, docCleanValues("CS-RT-1"))
	docNum := docSeedDocument(t, f.super, f.tenant)
	docSeedExtraction(t, f.super, f.tenant, docNum, docNoNumberValues())
	create := CreateDocumentHandler(f.svc.ImportDocumentFor, nil)
	supply := SupplyNumberHandler(f.svc.SupplyInvoiceNumber, nil)

	var bodies []string
	for name, entity := range csInvisible(f, foreign) {
		rec := csGet(create, f.limitedID, "POST", "/v1/imports/document", docJSONBody(entity, docImp))
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST /v1/imports/document (%s): status = %d, want 404 (body=%s)", name, rec.Code, rec.Body.String())
		}
		bodies = append(bodies, rec.Body.String())
		rec = csGet(supply, f.limitedID, "POST", "/v1/imports/document/invoice", supplyJSONBody(entity, docNum, "INV-9"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST /v1/imports/document/invoice (%s): status = %d, want 404 (body=%s)", name, rec.Code, rec.Body.String())
		}
		bodies = append(bodies, rec.Body.String())
	}
	if len(bodies) != 6 {
		t.Fatalf("collected %d bodies, want 6", len(bodies))
	}
	for _, b := range bodies {
		if strings.TrimSpace(b) != `{"error":"not found"}` {
			t.Errorf("refusal body = %s, want the one {\"error\":\"not found\"} for every id", b)
		}
	}
	if n := csCount(t, f.super, `SELECT count(*) FROM import_batches WHERE tenant_id IN ($1, $2)`, f.tenant, other); n != 0 {
		t.Errorf("import_batches after refusals = %d, want 0", n)
	}
	if n := csDocumentReads(t, f.super, f.tenant); n != 0 {
		t.Errorf("document.read rows after refusals = %d, want 0", n)
	}
	if n := csCount(t, f.super, `SELECT count(*) FROM invoices WHERE tenant_id IN ($1, $2)`, f.tenant, other); n != 0 {
		t.Errorf("invoices after refusals = %d, want 0", n)
	}

	if rec := csGet(create, f.limitedID, "POST", "/v1/imports/document", docJSONBody(f.entA, docImp)); rec.Code != http.StatusCreated {
		t.Errorf("POST /v1/imports/document for client A: status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	if rec := csGet(supply, f.limitedID, "POST", "/v1/imports/document/invoice", supplyJSONBody(f.entA, docNum, "INV-9")); rec.Code != http.StatusCreated {
		t.Errorf("POST /v1/imports/document/invoice for client A: status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	if n := csCount(t, f.super, `SELECT count(*) FROM invoices WHERE entity_id = $1`, f.entB); n != 0 {
		t.Errorf("invoices for B = %d, want 0", n)
	}
}

func TestClientScope_MappingRoutesAnswerOneNotFoundAndWriteNothing(t *testing.T) {
	f := newCSFixture(t, "CS mapping routes")
	other := seedTenant(t, f.super, "CS mapping routes other")
	foreign := seedEntity(t, f.super, other, "Foreign client")
	header := []string{"Inv No"}
	doc := storeDocumentAs(t, f.docSvc, f.tenant, "data.csv", "", csvBody(t, header, [][]string{{"1"}}))
	if err := f.store.SaveMapping(f.admin, f.entA, header, map[string]string{"invoice_number": "Inv No"}); err != nil {
		t.Fatalf("SaveMapping(A): %v", err)
	}
	suggester := &fakeSuggester{enabled: true, answer: map[string]any{"header_row": json.Number("1")}}
	saved := SavedMappingHandler(f.docSvc.OpenLinked, f.store.SavedMapping, f.store.EntityVisible, nil)
	suggest := SuggestMappingHandler(f.docSvc.OpenLinked, f.store.SavedMapping, f.store.EntityVisible, suggester, nil)
	before := csDocumentReads(t, f.super, f.tenant)

	var bodies []string
	for name, entity := range csInvisible(f, foreign) {
		rec := csGet(saved, f.limitedID, "GET", "/v1/imports/saved-mapping?entity_id="+entity+"&document_id="+doc.ID, "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET saved-mapping (%s): status = %d, want 404 (body=%s)", name, rec.Code, rec.Body.String())
		}
		bodies = append(bodies, rec.Body.String())
		rec = csGet(suggest, f.limitedID, "POST", "/v1/imports/suggest-mapping", suggestReqBody(entity, doc.ID))
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST suggest-mapping (%s): status = %d, want 404 (body=%s)", name, rec.Code, rec.Body.String())
		}
		bodies = append(bodies, rec.Body.String())
	}
	if len(bodies) != 6 {
		t.Fatalf("collected %d bodies, want 6", len(bodies))
	}
	for _, b := range bodies {
		if strings.TrimSpace(b) != `{"error":"not found"}` {
			t.Errorf("refusal body = %s, want the one {\"error\":\"not found\"} for every id", b)
		}
	}
	if n := len(suggester.calls); n != 0 {
		t.Errorf("AI calls after refusals = %d, want 0", n)
	}
	if n := csDocumentReads(t, f.super, f.tenant); n != before {
		t.Errorf("document.read rows after refusals = %d, want %d", n, before)
	}

	rec := csGet(saved, f.limitedID, "GET", "/v1/imports/saved-mapping?entity_id="+f.entA+"&document_id="+doc.ID, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"invoice_number":"Inv No"`) {
		t.Errorf("saved-mapping for A: %d %s, want 200 with the saved mapping", rec.Code, rec.Body.String())
	}
	rec = csGet(suggest, f.limitedID, "POST", "/v1/imports/suggest-mapping", suggestReqBody(f.entA, doc.ID))
	if rec.Code != http.StatusOK || len(suggester.calls) != 1 {
		t.Errorf("suggest for A: status = %d, AI calls = %d, want 200 and 1 (body=%s)", rec.Code, len(suggester.calls), rec.Body.String())
	}
}

func TestClientScope_AdminAndAllClientsStillReachEveryClient(t *testing.T) {
	f := newCSFixture(t, "CS full access")
	header := []string{"Inv No"}
	doc := storeDocumentAs(t, f.docSvc, f.tenant, "data.csv", "", csvBody(t, header, [][]string{{"1"}}))
	allClients := csMember(t, f.super, f.tenant, "preparer")
	if _, err := f.super.Exec(context.Background(), `UPDATE memberships SET all_clients = true WHERE user_id = $1`, allClients.Subject); err != nil {
		t.Fatalf("set all_clients: %v", err)
	}
	saved := SavedMappingHandler(f.docSvc.OpenLinked, f.store.SavedMapping, f.store.EntityVisible, nil)
	for name, id := range map[string]auth.Identity{
		"admin":       {Subject: csAdminSubject(t, f), Role: "authenticated", TenantID: f.tenant},
		"All clients": allClients,
	} {
		rec := csGet(saved, id, "GET", "/v1/imports/saved-mapping?entity_id="+f.entB+"&document_id="+doc.ID, "")
		if rec.Code != http.StatusOK {
			t.Errorf("%s reading saved-mapping of client B: status = %d, want 200 (body=%s)", name, rec.Code, rec.Body.String())
		}
	}
}

func csAdminSubject(t *testing.T, f csFixture) string {
	t.Helper()
	id, ok := auth.IdentityFromContext(f.admin)
	if !ok {
		t.Fatal("fixture admin has no identity")
	}
	return id.Subject
}

func TestClientScope_BatchRouteAnswersOneNotFound(t *testing.T) {
	f := newCSFixture(t, "CS batch route")
	batchA, batchB := csBatch(t, f.super, f.tenant, f.entA), csBatch(t, f.super, f.tenant, f.entB)
	h := http.NewServeMux()
	h.HandleFunc("GET /v1/imports/{id}", GetHandler(f.store.GetBatch, nil))

	b := csGet(h, f.limitedID, "GET", "/v1/imports/"+batchB, "")
	u := csGet(h, f.limitedID, "GET", "/v1/imports/"+uuid.NewString(), "")
	if b.Code != http.StatusNotFound || u.Code != http.StatusNotFound || b.Body.String() != u.Body.String() {
		t.Errorf("outside-set batch %d %s vs unknown batch %d %s, want the same 404", b.Code, b.Body.String(), u.Code, u.Body.String())
	}
	if rec := csGet(h, f.limitedID, "GET", "/v1/imports/"+batchA, ""); rec.Code != http.StatusOK {
		t.Errorf("own batch: status = %d, want 200", rec.Code)
	}
}

func TestClientScope_MalformedIdsAnswer400BeforeTheEntityCheck(t *testing.T) {
	f := newCSFixture(t, "CS malformed")
	doc := storeDocumentAs(t, f.docSvc, f.tenant, "data.csv", "", csvBody(t, []string{"Inv No"}, [][]string{{"1"}}))
	suggester := &fakeSuggester{enabled: true}
	saved := SavedMappingHandler(f.docSvc.OpenLinked, f.store.SavedMapping, f.store.EntityVisible, nil)
	suggest := SuggestMappingHandler(f.docSvc.OpenLinked, f.store.SavedMapping, f.store.EntityVisible, suggester, nil)
	create := CreateDocumentHandler(f.svc.ImportDocumentFor, nil)

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"saved-mapping, malformed entity":     csGet(saved, f.limitedID, "GET", "/v1/imports/saved-mapping?entity_id=nope&document_id="+doc.ID, ""),
		"saved-mapping, malformed document":   csGet(saved, f.limitedID, "GET", "/v1/imports/saved-mapping?entity_id="+f.entB+"&document_id=nope", ""),
		"saved-mapping, missing entity":       csGet(saved, f.limitedID, "GET", "/v1/imports/saved-mapping?document_id="+doc.ID, ""),
		"suggest, malformed entity":           csGet(suggest, f.limitedID, "POST", "/v1/imports/suggest-mapping", suggestReqBody("nope", doc.ID)),
		"suggest, malformed document":         csGet(suggest, f.limitedID, "POST", "/v1/imports/suggest-mapping", suggestReqBody(f.entB, "nope")),
		"import document, malformed entity":   csGet(create, f.limitedID, "POST", "/v1/imports/document", docJSONBody("nope", doc.ID)),
		"import document, malformed document": csGet(create, f.limitedID, "POST", "/v1/imports/document", docJSONBody(f.entB, "nope")),
	} {
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body=%s)", name, rec.Code, rec.Body.String())
		}
	}
	if n := len(suggester.calls); n != 0 {
		t.Errorf("AI calls = %d, want 0", n)
	}
}

func TestClientScope_ImportRouteOutsideTheSetIsNotFoundAndWritesNoBatch(t *testing.T) {
	f := newCSFixture(t, "CS import route")
	doc := storeDocumentAs(t, f.docSvc, f.tenant, "data.csv", "", csvBody(t, []string{"Inv No"}, [][]string{{"CS-R1"}}))
	h := CreateHandler(f.svc.Import, f.docSvc.OpenLinked, f.store.SaveMapping, f.store.EntityVisible, nil)
	post := func(entity, query string) *httptest.ResponseRecorder {
		body, ct := buildImportForm(t, entity, `{"invoice_number":"Inv No"}`, doc.ID)
		raw, _ := io.ReadAll(body)
		r := httptest.NewRequest("POST", "/v1/imports"+query, bytes.NewReader(raw))
		r.Header.Set("Content-Type", ct)
		r = r.WithContext(auth.WithIdentity(r.Context(), f.limitedID))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	readsBefore := csDocumentReads(t, f.super, f.tenant)
	for name, query := range map[string]string{"real": "", "dry run": "?dry_run=true"} {
		rec := post(f.entB, query)
		if rec.Code != http.StatusNotFound || strings.TrimSpace(rec.Body.String()) != `{"error":"not found"}` {
			t.Errorf("%s import for B: %d %s, want 404 not found", name, rec.Code, rec.Body.String())
		}
	}
	if n := csCount(t, f.super, `SELECT count(*) FROM import_batches WHERE tenant_id = $1`, f.tenant); n != 0 {
		t.Errorf("import_batches = %d, want 0", n)
	}
	if n := csCount(t, f.super, `SELECT count(*) FROM import_mappings WHERE entity_id = $1`, f.entB); n != 0 {
		t.Errorf("import_mappings for B = %d, want 0", n)
	}
	if n := csDocumentReads(t, f.super, f.tenant) - readsBefore; n != 0 {
		t.Errorf("document.read rows written by refused imports = %d, want 0", n)
	}
	if rec := post(f.entA, ""); rec.Code != http.StatusCreated {
		t.Errorf("import for A: %d %s, want 201", rec.Code, rec.Body.String())
	}
	if n := csDocumentReads(t, f.super, f.tenant) - readsBefore; n != 1 {
		t.Errorf("document.read rows after the visible import = %d, want 1", n)
	}
}

func TestClientScope_SheetRouteFollowsTheLink(t *testing.T) {
	f := newCSFixture(t, "CS sheet route")
	store := func(name string) string {
		return storeDocumentAs(t, f.docSvc, f.tenant, name+".csv", "", csvBody(t, []string{"Inv No"}, [][]string{{name}})).ID
	}
	unlinked, linkedB, linkedA, linkedBoth, invoiceB := store("z"), store("y"), store("w"), store("v"), store("u")
	batch := func(doc, entity string) {
		if _, err := f.super.Exec(context.Background(),
			`INSERT INTO import_batches (tenant_id, entity_id, document_id, status, rows_total, rows_valid, rows_invalid, errors)
			 VALUES ($1, $2, $3, 'completed', 1, 1, 0, '[]'::jsonb)`, f.tenant, entity, doc); err != nil {
			t.Fatalf("seed batch: %v", err)
		}
	}
	batch(linkedB, f.entB)
	batch(linkedA, f.entA)
	batch(linkedBoth, f.entA)
	batch(linkedBoth, f.entB)
	if _, err := f.super.Exec(context.Background(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, 'CS-SHEET-B', $3)`,
		f.tenant, f.entB, invoiceB); err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/documents/{id}/sheet", SheetHandler(f.docSvc.OpenLinked, nil))
	sheet := func(id auth.Identity, doc string) *httptest.ResponseRecorder {
		return csGet(mux, id, "GET", "/v1/documents/"+doc+"/sheet", "")
	}
	adminID, _ := auth.IdentityFromContext(f.admin)

	before := csDocumentReads(t, f.super, f.tenant)
	var bodies []string
	for name, doc := range map[string]string{"B's batch": linkedB, "B's invoice": invoiceB, "unknown": uuid.NewString()} {
		rec := sheet(f.limitedID, doc)
		if rec.Code != http.StatusNotFound {
			t.Errorf("limited reviewer, sheet of %s: status = %d, want 404", name, rec.Code)
		}
		bodies = append(bodies, rec.Body.String())
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] || !strings.Contains(b, "not found") {
			t.Errorf("refusal bodies = %q, want one identical not-found body", bodies)
			break
		}
	}
	if n := csDocumentReads(t, f.super, f.tenant); n != before {
		t.Errorf("document.read rows after refusals = %d, want %d", n, before)
	}
	if rec := sheet(f.limitedID, "nope"); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed id: status = %d, want 400", rec.Code)
	}

	for name, doc := range map[string]string{"A's batch": linkedA, "A's and B's batch": linkedBoth} {
		if rec := sheet(f.limitedID, doc); rec.Code != http.StatusOK {
			t.Errorf("limited reviewer, sheet of %s: status = %d, want 200 (body=%s)", name, rec.Code, rec.Body.String())
		}
	}
	for name, id := range map[string]auth.Identity{"limited reviewer": f.limitedID, "admin": adminID} {
		if rec := sheet(id, unlinked); rec.Code != http.StatusOK {
			t.Errorf("%s, sheet of the unlinked upload: status = %d, want 200", name, rec.Code)
		}
	}
}

func csLinkInvoice(t *testing.T, super *pgxpool.Pool, tenant, entity, doc, number string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, $3, $4)`,
		tenant, entity, number, doc); err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
}

func csLinkBatch(t *testing.T, super *pgxpool.Pool, tenant, entity, doc string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO import_batches (tenant_id, entity_id, document_id, status, rows_total, rows_valid, rows_invalid, errors)
		 VALUES ($1, $2, $3, 'completed', 1, 1, 0, '[]'::jsonb)`, tenant, entity, doc); err != nil {
		t.Fatalf("seed batch: %v", err)
	}
}

func TestClientScope_ADocumentOfAHiddenClientIsRefusedOnTheImportRoutes(t *testing.T) {
	f := newCSFixture(t, "CS hidden document imports")
	header := []string{"Inv No"}
	hidden := storeDocumentAs(t, f.docSvc, f.tenant, "b.csv", "text/csv", csvBody(t, header, [][]string{{"B-1"}})).ID
	csLinkBatch(t, f.super, f.tenant, f.entB, hidden)
	own := storeDocumentAs(t, f.docSvc, f.tenant, "a.csv", "text/csv", csvBody(t, header, [][]string{{"A-1"}})).ID
	if err := f.store.SaveMapping(f.admin, f.entA, header, map[string]string{"invoice_number": "Inv No"}); err != nil {
		t.Fatalf("SaveMapping(A): %v", err)
	}

	suggester := &fakeSuggester{enabled: true, answer: map[string]any{"header_row": json.Number("1")}}
	checker := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 1})}
	create := CreateHandler(f.svc.Import, f.docSvc.OpenLinked, f.store.SaveMapping, f.store.EntityVisible, nil)
	saved := SavedMappingHandler(f.docSvc.OpenLinked, f.store.SavedMapping, f.store.EntityVisible, nil)
	suggest := SuggestMappingHandler(f.docSvc.OpenLinked, f.store.SavedMapping, f.store.EntityVisible, suggester, nil)
	post := func(doc, query string) *httptest.ResponseRecorder {
		body, ct := buildImportForm(t, f.entA, `{"invoice_number":"Inv No"}`, doc)
		raw, _ := io.ReadAll(body)
		r := httptest.NewRequest("POST", "/v1/imports"+query, bytes.NewReader(raw))
		r.Header.Set("Content-Type", ct)
		r = r.WithContext(auth.WithIdentity(r.Context(), f.limitedID))
		rec := httptest.NewRecorder()
		create.ServeHTTP(rec, r)
		return rec
	}
	check := func(doc string) *httptest.ResponseRecorder {
		rec, _ := doCheckRequest(t, f.docSvc.OpenLinked, checker, nil, &f.limitedID, checkReqBody(t, doc, map[string]string{"invoice_number": "Inv No"}))
		return rec
	}

	readsBefore := csDocumentReads(t, f.super, f.tenant)
	answers := map[string]*httptest.ResponseRecorder{
		"import":        post(hidden, ""),
		"dry run":       post(hidden, "?dry_run=true"),
		"saved-mapping": csGet(saved, f.limitedID, "GET", "/v1/imports/saved-mapping?entity_id="+f.entA+"&document_id="+hidden, ""),
		"suggest":       csGet(suggest, f.limitedID, "POST", "/v1/imports/suggest-mapping", suggestReqBody(f.entA, hidden)),
		"check":         check(hidden),
	}
	for name, rec := range answers {
		if rec.Code != http.StatusNotFound || strings.TrimSpace(rec.Body.String()) != `{"error":"not found"}` {
			t.Errorf("%s of B's document as a reviewer limited to A: %d %s, want 404 not found", name, rec.Code, rec.Body.String())
		}
	}
	if n := csDocumentReads(t, f.super, f.tenant) - readsBefore; n != 0 {
		t.Errorf("document.read rows written by refused calls = %d, want 0", n)
	}
	if len(suggester.calls) != 0 || len(checker.calls) != 0 {
		t.Errorf("AI calls = %d suggest, %d check, want 0", len(suggester.calls), len(checker.calls))
	}
	if n := csCount(t, f.super, `SELECT count(*) FROM import_batches WHERE entity_id = $1`, f.entA); n != 0 {
		t.Errorf("import_batches for A = %d, want 0", n)
	}
	if n := csCount(t, f.super, `SELECT count(*) FROM invoices WHERE entity_id = $1`, f.entA); n != 0 {
		t.Errorf("invoices for A = %d, want 0", n)
	}

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"dry run":       post(own, "?dry_run=true"),
		"saved-mapping": csGet(saved, f.limitedID, "GET", "/v1/imports/saved-mapping?entity_id="+f.entA+"&document_id="+own, ""),
		"suggest":       csGet(suggest, f.limitedID, "POST", "/v1/imports/suggest-mapping", suggestReqBody(f.entA, own)),
		"check":         check(own),
		"import":        post(own, ""),
	} {
		if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
			t.Errorf("%s of the member's own new upload: %d %s, want 200 or 201", name, rec.Code, rec.Body.String())
		}
	}
}

func TestClientScope_ADocumentFiledUnderAHiddenClientCannotBeFiledAgain(t *testing.T) {
	f := newCSFixture(t, "CS hidden filed document")
	filedUnderB := docSeedDocument(t, f.super, f.tenant)
	docSeedExtraction(t, f.super, f.tenant, filedUnderB, docNoNumberValues())
	csLinkInvoice(t, f.super, f.tenant, f.entB, filedUnderB, "CS-FILED-B")
	supply := SupplyNumberHandler(f.svc.SupplyInvoiceNumber, nil)
	reading := ReadingHandler(f.svc.CarriedReading, nil)

	rec := csGet(supply, f.limitedID, "POST", "/v1/imports/document/invoice", supplyJSONBody(f.entA, filedUnderB, "INV-2"))
	if rec.Code != http.StatusNotFound || strings.TrimSpace(rec.Body.String()) != `{"error":"not found"}` {
		t.Errorf("supply a number for a document filed under B: %d %s, want 404 not found", rec.Code, rec.Body.String())
	}
	if n := countInvoicesCitingDocument(t, f.super, filedUnderB); n != 1 {
		t.Errorf("invoices citing the document = %d, want 1 (B's)", n)
	}
	if got, err := f.svc.CarriedReading(f.limited, filedUnderB); err != nil || got != nil {
		t.Errorf("CarriedReading of B's document as a reviewer limited to A = %v, %v, want nil, nil", got, err)
	}
	if rec := csGet(reading, f.limitedID, "GET", "/v1/imports/document/reading?document_id="+filedUnderB, ""); !strings.Contains(rec.Body.String(), `"reading":null`) {
		t.Errorf("reading route for B's document = %d %s, want reading null", rec.Code, rec.Body.String())
	}
	if _, err := f.svc.ImportDocument(f.limited, f.entA, filedUnderB); !errors.Is(err, ErrNotFound) {
		t.Errorf("ImportDocument(B's document, entity A): err = %v, want ErrNotFound", err)
	}
	if n := csCount(t, f.super, `SELECT count(*) FROM import_batches WHERE entity_id = $1`, f.entA); n != 0 {
		t.Errorf("import_batches for A = %d, want 0", n)
	}
	if _, err := f.svc.SupplyInvoiceNumber(f.admin, f.entA, filedUnderB, "INV-2"); !errors.Is(err, ErrDocumentAlreadyFiled) {
		t.Errorf("control: admin supplying a number for the filed document: err = %v, want ErrDocumentAlreadyFiled", err)
	}
}

func TestClientScope_AnInvoiceUnderAHiddenClientStillCountsAsFiledForAVisibleBatch(t *testing.T) {
	f := newCSFixture(t, "CS hidden filed, visible batch")
	doc := docSeedDocument(t, f.super, f.tenant)
	docSeedExtraction(t, f.super, f.tenant, doc, docNoNumberValues())
	csLinkBatch(t, f.super, f.tenant, f.entA, doc)
	csLinkInvoice(t, f.super, f.tenant, f.entB, doc, "CS-FILED-B2")

	if _, err := f.svc.SupplyInvoiceNumber(f.limited, f.entA, doc, "INV-3"); !errors.Is(err, ErrDocumentAlreadyFiled) {
		t.Errorf("SupplyInvoiceNumber as a reviewer limited to A: err = %v, want ErrDocumentAlreadyFiled", err)
	}
	if got, err := f.svc.CarriedReading(f.limited, doc); err != nil || got != nil {
		t.Errorf("CarriedReading = %v, %v, want nil, nil (filed)", got, err)
	}
	if n := countInvoicesCitingDocument(t, f.super, doc); n != 1 {
		t.Errorf("invoices citing the document = %d, want 1", n)
	}
}

// csUnassignAfterBatch makes the tenant's next import_batches insert unassign every client
// from every member, so the entity is visible to EntityVisible and gone by the invoice Create.
func csUnassignAfterBatch(t *testing.T, super *pgxpool.Pool, tenant string) {
	t.Helper()
	name := "qa_unassign_" + strings.ReplaceAll(tenant, "-", "")
	for _, q := range []string{
		`CREATE FUNCTION ` + name + `() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER AS $$
		 BEGIN DELETE FROM membership_clients WHERE tenant_id = NEW.tenant_id; RETURN NEW; END $$`,
		`CREATE TRIGGER ` + name + ` AFTER INSERT ON import_batches FOR EACH ROW
		 WHEN (NEW.tenant_id = '` + tenant + `') EXECUTE FUNCTION ` + name + `()`,
	} {
		if _, err := super.Exec(context.Background(), q); err != nil {
			t.Fatalf("seed unassign trigger: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DROP TRIGGER IF EXISTS `+name+` ON import_batches`)
		_, _ = super.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+name+`()`)
	})
}

func TestClientScope_AClientLeavingTheSetMidImportAnswers404NotAFiveHundred(t *testing.T) {
	f := newCSFixture(t, "CS client leaves mid-import")
	header := []string{"Inv No"}
	mapping := map[string]string{"invoice_number": "Inv No"}
	csUnassignAfterBatch(t, f.super, f.tenant)

	_, err := f.svc.Import(f.limited, f.entA, "data.csv", "", 1, mapping, header, [][]string{{"CS-LEAVE-1"}, {"CS-LEAVE-2"}}, false, "")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Import after the client left the set: err = %v, want ErrNotFound", err)
	}
	if n := csCount(t, f.super, `SELECT count(*) FROM invoices WHERE entity_id = $1`, f.entA); n != 0 {
		t.Errorf("invoices for A = %d, want 0", n)
	}
}

func TestClientScope_AClientLeavingTheSetMidDocumentImportIsNotFound(t *testing.T) {
	f := newCSFixture(t, "CS client leaves mid-document-import")
	doc := docSeedDocument(t, f.super, f.tenant)
	docSeedExtraction(t, f.super, f.tenant, doc, docCleanValues("CS-LEAVE-D"))
	csUnassignAfterBatch(t, f.super, f.tenant)

	if _, err := f.svc.ImportDocument(f.limited, f.entA, doc); !errors.Is(err, ErrNotFound) {
		t.Errorf("ImportDocument after the client left the set: err = %v, want ErrNotFound", err)
	}
	if n := countInvoicesCitingDocument(t, f.super, doc); n != 0 {
		t.Errorf("invoices citing the document = %d, want 0", n)
	}
}

// csImportRoutes runs every document-taking import route for one caller and one document.
func csImportRoutes(t *testing.T, f csFixture, entity string, suggester *fakeSuggester, checker *mcStub) map[string]func(auth.Identity, string) *httptest.ResponseRecorder {
	t.Helper()
	create := CreateHandler(f.svc.Import, f.docSvc.OpenLinked, f.store.SaveMapping, f.store.EntityVisible, nil)
	saved := SavedMappingHandler(f.docSvc.OpenLinked, f.store.SavedMapping, f.store.EntityVisible, nil)
	suggest := SuggestMappingHandler(f.docSvc.OpenLinked, f.store.SavedMapping, f.store.EntityVisible, suggester, nil)
	sheet := http.NewServeMux()
	sheet.HandleFunc("GET /v1/documents/{id}/sheet", SheetHandler(f.docSvc.OpenLinked, nil))
	post := func(id auth.Identity, doc, query string) *httptest.ResponseRecorder {
		body, ct := buildImportForm(t, entity, `{"invoice_number":"Inv No"}`, doc)
		raw, _ := io.ReadAll(body)
		r := httptest.NewRequest("POST", "/v1/imports"+query, bytes.NewReader(raw))
		r.Header.Set("Content-Type", ct)
		r = r.WithContext(auth.WithIdentity(r.Context(), id))
		rec := httptest.NewRecorder()
		create.ServeHTTP(rec, r)
		return rec
	}
	return map[string]func(auth.Identity, string) *httptest.ResponseRecorder{
		"dry run": func(id auth.Identity, doc string) *httptest.ResponseRecorder { return post(id, doc, "?dry_run=true") },
		"saved-mapping": func(id auth.Identity, doc string) *httptest.ResponseRecorder {
			return csGet(saved, id, "GET", "/v1/imports/saved-mapping?entity_id="+entity+"&document_id="+doc, "")
		},
		"suggest": func(id auth.Identity, doc string) *httptest.ResponseRecorder {
			return csGet(suggest, id, "POST", "/v1/imports/suggest-mapping", suggestReqBody(entity, doc))
		},
		"check": func(id auth.Identity, doc string) *httptest.ResponseRecorder {
			rec, _ := doCheckRequest(t, f.docSvc.OpenLinked, checker, nil, &id, checkReqBody(t, doc, map[string]string{"invoice_number": "Inv No"}))
			return rec
		},
		"sheet": func(id auth.Identity, doc string) *httptest.ResponseRecorder {
			return csGet(sheet, id, "GET", "/v1/documents/"+doc+"/sheet", "")
		},
		"import": func(id auth.Identity, doc string) *httptest.ResponseRecorder { return post(id, doc, "") },
	}
}

func TestClientScope_ADocumentCitedByBothClientsAndTheFullAccessRolesReadOnEveryImportRoute(t *testing.T) {
	f := newCSFixture(t, "CS both clients")
	header := []string{"Inv No"}
	both := storeDocumentAs(t, f.docSvc, f.tenant, "both.csv", "text/csv", csvBody(t, header, [][]string{{"BOTH-1"}})).ID
	csLinkBatch(t, f.super, f.tenant, f.entA, both)
	csLinkInvoice(t, f.super, f.tenant, f.entB, both, "CS-BOTH-B")
	hidden := storeDocumentAs(t, f.docSvc, f.tenant, "hidden.csv", "text/csv", csvBody(t, header, [][]string{{"HID-1"}})).ID
	csLinkBatch(t, f.super, f.tenant, f.entB, hidden)

	suggester := &fakeSuggester{enabled: true, answer: map[string]any{"header_row": json.Number("1")}}
	checker := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 1})}
	routes := csImportRoutes(t, f, f.entA, suggester, checker)
	if len(routes) != 6 {
		t.Fatalf("routes = %d, want 6", len(routes))
	}
	ok := func(who, route, doc string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
			t.Errorf("%s, %s of %s: %d %s, want 200 or 201", who, route, doc, rec.Code, rec.Body.String())
		}
	}

	for route, call := range routes {
		ok("reviewer limited to A", route, "a document cited by A and B", call(f.limitedID, both))
	}
	adminID, _ := auth.IdentityFromContext(f.admin)
	allClients := csMember(t, f.super, f.tenant, "preparer")
	if _, err := f.super.Exec(context.Background(), `UPDATE memberships SET all_clients = true WHERE user_id = $1`, allClients.Subject); err != nil {
		t.Fatalf("set all_clients: %v", err)
	}
	for who, id := range map[string]auth.Identity{"admin": adminID, "All clients": allClients} {
		for route, call := range routes {
			if route == "import" {
				continue
			}
			ok(who, route, "a document of B only", call(id, hidden))
		}
	}
	if _, err := f.super.Exec(context.Background(), `UPDATE tenants SET kind = 'in_house' WHERE id = $1`, f.tenant); err != nil {
		t.Fatalf("set in_house: %v", err)
	}
	for route, call := range routes {
		if route == "import" {
			continue
		}
		ok("in-house member", route, "a document of B only", call(f.limitedID, hidden))
	}
	if n := csCount(t, f.super, `SELECT count(*) FROM import_batches WHERE entity_id = $1`, f.entA); n < 2 {
		t.Errorf("import_batches for A = %d, want the seeded one and the real import", n)
	}
}

func TestClientScope_TheSettledExtractionRoutesFollowTheDocumentRule(t *testing.T) {
	f := newCSFixture(t, "CS settled extraction rule")
	seed := func(values map[string]*string, link func(doc string)) string {
		doc := docSeedDocument(t, f.super, f.tenant)
		docSeedExtraction(t, f.super, f.tenant, doc, values)
		if link != nil {
			link(doc)
		}
		return doc
	}
	onlyB := seed(docCleanValues("CS-SET-B"), func(d string) { csLinkBatch(t, f.super, f.tenant, f.entB, d) })
	onlyBNumberless := seed(docNoNumberValues(), func(d string) { csLinkBatch(t, f.super, f.tenant, f.entB, d) })

	if _, err := f.svc.ImportDocument(f.limited, f.entA, onlyB); !errors.Is(err, ErrNotFound) {
		t.Errorf("ImportDocument(document cited only by B's batch) as a reviewer limited to A: err = %v, want ErrNotFound", err)
	}
	if _, err := f.svc.SupplyInvoiceNumber(f.limited, f.entA, onlyBNumberless, "INV-9"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SupplyInvoiceNumber(document cited only by B's batch): err = %v, want ErrNotFound", err)
	}
	if n := csCount(t, f.super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, f.tenant); n != 0 {
		t.Errorf("invoices = %d, want 0 after two refusals", n)
	}
	if n := csCount(t, f.super, `SELECT count(*) FROM import_batches WHERE entity_id = $1`, f.entA); n != 0 {
		t.Errorf("import_batches for A = %d, want 0", n)
	}

	bothClients := seed(docCleanValues("CS-SET-AB"), func(d string) {
		csLinkBatch(t, f.super, f.tenant, f.entA, d)
		csLinkBatch(t, f.super, f.tenant, f.entB, d)
	})
	if res, err := f.svc.ImportDocument(f.limited, f.entA, bothClients); err != nil || res.ReadyInvoices+res.QuarantinedInvoices != 1 {
		t.Errorf("ImportDocument(document cited by A's and B's batch) as a reviewer limited to A = %+v, %v, want one invoice filed", res, err)
	}

	adminID, _ := auth.IdentityFromContext(f.admin)
	adminDoc := seed(docCleanValues("CS-SET-ADMIN"), func(d string) { csLinkBatch(t, f.super, f.tenant, f.entB, d) })
	if res, err := f.svc.ImportDocument(auth.WithIdentity(context.Background(), adminID), f.entA, adminDoc); err != nil || res.ReadyInvoices+res.QuarantinedInvoices != 1 {
		t.Errorf("ImportDocument(document cited only by B's batch) as admin = %+v, %v, want one invoice filed", res, err)
	}
	allClients := csMember(t, f.super, f.tenant, "preparer")
	if _, err := f.super.Exec(context.Background(), `UPDATE memberships SET all_clients = true WHERE user_id = $1`, allClients.Subject); err != nil {
		t.Fatalf("set all_clients: %v", err)
	}
	doc := seed(docCleanValues("CS-SET-ALL"), func(d string) { csLinkBatch(t, f.super, f.tenant, f.entB, d) })
	if res, err := f.svc.ImportDocument(auth.WithIdentity(context.Background(), allClients), f.entA, doc); err != nil || res.ReadyInvoices+res.QuarantinedInvoices != 1 {
		t.Errorf("ImportDocument(document cited only by B's batch) as an All clients member = %+v, %v, want one invoice filed", res, err)
	}
	if _, err := f.super.Exec(context.Background(), `UPDATE tenants SET kind = 'in_house' WHERE id = $1`, f.tenant); err != nil {
		t.Fatalf("set in_house: %v", err)
	}
	doc = seed(docCleanValues("CS-SET-IH"), func(d string) { csLinkBatch(t, f.super, f.tenant, f.entB, d) })
	if res, err := f.svc.ImportDocument(f.limited, f.entA, doc); err != nil || res.ReadyInvoices+res.QuarantinedInvoices != 1 {
		t.Errorf("ImportDocument(document cited only by B's batch) as an in-house member = %+v, %v, want one invoice filed", res, err)
	}
}

func TestClientScope_DocumentHasInvoiceSeesAnInvoiceUnderAHiddenClient(t *testing.T) {
	f := newCSFixture(t, "CS has invoice hidden")
	filed := docSeedDocument(t, f.super, f.tenant)
	csLinkInvoice(t, f.super, f.tenant, f.entB, filed, "CS-HAS-B")
	batchOnly := docSeedDocument(t, f.super, f.tenant)
	csLinkBatch(t, f.super, f.tenant, f.entB, batchOnly)

	if has, err := f.store.DocumentHasInvoice(f.limited, filed); err != nil || !has {
		t.Errorf("DocumentHasInvoice(filed under B) as a reviewer limited to A = %v, %v, want true", has, err)
	}
	if has, err := f.store.DocumentHasInvoice(f.limited, batchOnly); err != nil || has {
		t.Errorf("DocumentHasInvoice(only a batch under B) = %v, %v, want false", has, err)
	}
	if has, err := f.store.DocumentHasInvoice(f.limited, docSeedDocument(t, f.super, f.tenant)); err != nil || has {
		t.Errorf("DocumentHasInvoice(unlinked) = %v, %v, want false", has, err)
	}
}

func TestClientScope_AHeldDocumentFollowsItsBatch(t *testing.T) {
	f := newCSFixture(t, "CS held document")
	own := docSeedDocument(t, f.super, f.tenant)
	docSeedExtraction(t, f.super, f.tenant, own, docCleanValues("CS-HELD-A"))
	dtSetVerdict(t, f.super, own, "credit note")
	csLinkBatch(t, f.super, f.tenant, f.entA, own)
	other := docSeedDocument(t, f.super, f.tenant)
	docSeedExtraction(t, f.super, f.tenant, other, docCleanValues("CS-HELD-B"))
	dtSetVerdict(t, f.super, other, "credit note")
	csLinkBatch(t, f.super, f.tenant, f.entB, other)

	if ex, err := f.store.SettledExtraction(f.limited, own); err != nil || ex.DocumentType == nil || *ex.DocumentType != "credit note" {
		t.Errorf("SettledExtraction of the batch in A = %+v, %v, want the credit note verdict", ex, err)
	}
	if _, err := f.store.SettledExtraction(f.limited, other); !errors.Is(err, ErrNotFound) {
		t.Errorf("SettledExtraction of the batch in B: err = %v, want ErrNotFound", err)
	}
	if _, err := f.svc.ImportDocument(f.limited, f.entA, other); !errors.Is(err, ErrNotFound) {
		t.Errorf("ImportDocument(B's held document): err = %v, want ErrNotFound", err)
	}
	if n := csCount(t, f.super, `SELECT count(*) FROM import_batches WHERE document_id = $1`, other); n != 1 {
		t.Errorf("batches for B's document = %d, want the seeded one only", n)
	}
}

func TestClientScope_AFiledDocumentHidesAnInvisibleInvoiceID(t *testing.T) {
	f := newCSFixture(t, "CS filed hidden id")
	doc := docSeedDocument(t, f.super, f.tenant)
	docSeedExtraction(t, f.super, f.tenant, doc, docCleanValues("CS-FH-1"))
	dtSetVerdict(t, f.super, doc, "debit note")
	csLinkBatch(t, f.super, f.tenant, f.entA, doc)
	csLinkInvoice(t, f.super, f.tenant, f.entB, doc, "CS-FH-FILED")

	res, err := f.svc.ImportDocument(f.limited, f.entA, doc)
	if err != nil || len(res.Errors) != 1 {
		t.Fatalf("ImportDocument: %+v, %v, want one row", res, err)
	}
	if got := res.Errors[0]; got.InvoiceID != "" || got.RuleKey != ruleKeyDuplicateInvoiceNumber || got.Message != documentAlreadyFiledReason {
		t.Errorf("row = %+v, want the sentence and rule_key with no invoice_id", got)
	}
}
