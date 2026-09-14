// Store.SaveMapping and its CreateHandler wiring, DB-backed through dbTestPools,
// seedTenant and memberSubject.
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// countImportMappings counts entityID's rows as the superuser, so RLS hides none.
func countImportMappings(t *testing.T, super *pgxpool.Pool, entityID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM import_mappings WHERE entity_id = $1`, entityID,
	).Scan(&n); err != nil {
		t.Fatalf("count import_mappings rows: %v", err)
	}
	return n
}

func TestStoreSaveMapping_InsertsRowForCallersTenant(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "SM-DB-01 tenant")
	entityID := seedEntity(t, super, tenantID, "SM-DB-01 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	store := NewStore(app)
	header := []string{"Invoice No", "Total"}
	mapping := map[string]string{"invoice_number": "Invoice No", "total": "Total"}

	if err := store.SaveMapping(c, entityID, header, mapping); err != nil {
		t.Fatalf("SaveMapping: %v", err)
	}

	var signature, mappingJSON string
	if err := super.QueryRow(ctx,
		`SELECT column_signature, mapping::text FROM import_mappings WHERE entity_id = $1`,
		entityID,
	).Scan(&signature, &mappingJSON); err != nil {
		t.Fatalf("read back import_mappings row: %v", err)
	}

	if n := countImportMappings(t, super, entityID); n != 1 {
		t.Fatalf("row count = %d, want 1", n)
	}
	if want := columnSignature(header); signature != want {
		t.Errorf("column_signature = %q, want %q", signature, want)
	}

	var got map[string]string
	if err := json.Unmarshal([]byte(mappingJSON), &got); err != nil {
		t.Fatalf("unmarshal stored mapping: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("stored mapping is empty")
	}
	if !reflect.DeepEqual(got, mapping) {
		t.Errorf("stored mapping = %v, want %v", got, mapping)
	}
}

// TestStoreSaveMapping_SecondSaveReplacesAndMovesSavedAt backdates saved_at first,
// so an upsert that leaves saved_at alone cannot pass on equality.
func TestStoreSaveMapping_SecondSaveReplacesAndMovesSavedAt(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "SM-DB-02 tenant")
	entityID := seedEntity(t, super, tenantID, "SM-DB-02 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	store := NewStore(app)
	header := []string{"Invoice No", "Total", "VAT"}
	firstMapping := map[string]string{"invoice_number": "Invoice No", "total": "Total", "vat": "VAT"}
	if err := store.SaveMapping(c, entityID, header, firstMapping); err != nil {
		t.Fatalf("first SaveMapping: %v", err)
	}

	var firstID string
	if err := super.QueryRow(ctx, `SELECT id FROM import_mappings WHERE entity_id = $1`, entityID).Scan(&firstID); err != nil {
		t.Fatalf("read back first row: %v", err)
	}
	if firstID == "" {
		t.Fatal("first row id is empty")
	}

	backdate := time.Now().Add(-time.Hour)
	if _, err := super.Exec(ctx, `UPDATE import_mappings SET saved_at = $1 WHERE id = $2`, backdate, firstID); err != nil {
		t.Fatalf("backdate saved_at: %v", err)
	}

	secondMapping := map[string]string{"invoice_number": "Invoice No", "total": "Total"} // vat dropped
	if err := store.SaveMapping(c, entityID, header, secondMapping); err != nil {
		t.Fatalf("second SaveMapping: %v", err)
	}

	if n := countImportMappings(t, super, entityID); n != 1 {
		t.Fatalf("row count = %d, want 1 -- the second save must replace, not insert a second row", n)
	}

	var id, mappingJSON string
	var savedAt time.Time
	if err := super.QueryRow(ctx,
		`SELECT id, mapping::text, saved_at FROM import_mappings WHERE entity_id = $1`,
		entityID,
	).Scan(&id, &mappingJSON, &savedAt); err != nil {
		t.Fatalf("read back replaced row: %v", err)
	}
	if id != firstID {
		t.Errorf("id = %q, want the same row replaced in place %q", id, firstID)
	}
	if !savedAt.After(backdate) {
		t.Errorf("saved_at = %s, want strictly after the backdated %s", savedAt, backdate)
	}

	var got map[string]string
	if err := json.Unmarshal([]byte(mappingJSON), &got); err != nil {
		t.Fatalf("unmarshal stored mapping: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("stored mapping is empty")
	}
	if !reflect.DeepEqual(got, secondMapping) {
		t.Errorf("stored mapping = %v, want the replaced mapping %v", got, secondMapping)
	}
}

func TestStoreSaveMapping_OtherTenantsEntityIsValidation(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "SM-DB-03 tenant")
	otherTenantID := seedTenant(t, super, "SM-DB-03 other tenant")
	otherEntityID := seedEntity(t, super, otherTenantID, "SM-DB-03 other entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	store := NewStore(app)
	header := []string{"Invoice No"}
	mapping := map[string]string{"invoice_number": "Invoice No"}

	err := store.SaveMapping(c, otherEntityID, header, mapping)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("SaveMapping err = %v, want ErrValidation", err)
	}
	if n := countImportMappings(t, super, otherEntityID); n != 0 {
		t.Fatalf("row count = %d, want 0 -- another tenant's entity must not accept a save", n)
	}

	// Control: the owning tenant's save lands a row the same count sees.
	owner := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: otherTenantID})
	if err := store.SaveMapping(owner, otherEntityID, header, mapping); err != nil {
		t.Fatalf("owner SaveMapping: %v", err)
	}
	if n := countImportMappings(t, super, otherEntityID); n != 1 {
		t.Fatalf("control: row count = %d, want 1", n)
	}
}

// TestStoreSaveMapping_SuspendedMemberIsRefused: the save rides the request seam's
// membership gate, so a suspended caller writes nothing.
func TestStoreSaveMapping_SuspendedMemberIsRefused(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "save mapping suspended tenant")
	entityID := seedEntity(t, super, tenantID, "save mapping suspended entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	store := NewStore(app)
	mapping := map[string]string{"invoice_number": "Invoice No"}

	// Control: while active, the caller's save lands a row the same count sees.
	if err := store.SaveMapping(c, entityID, []string{"Invoice No"}, mapping); err != nil {
		t.Fatalf("active SaveMapping: %v", err)
	}
	if n := countImportMappings(t, super, entityID); n != 1 {
		t.Fatalf("control: row count = %d, want 1", n)
	}

	if _, err := super.Exec(ctx,
		`UPDATE memberships SET status = 'suspended' WHERE tenant_id = $1 AND user_id = $2`, tenantID, memberSubject,
	); err != nil {
		t.Fatalf("suspend caller: %v", err)
	}

	// A different header, so a save that slipped past the gate would add a second row.
	err := store.SaveMapping(c, entityID, []string{"Invoice No", "Total"}, mapping)
	if !errors.Is(err, db.ErrNotActiveMember) {
		t.Fatalf("SaveMapping err = %v, want db.ErrNotActiveMember", err)
	}
	if n := countImportMappings(t, super, entityID); n != 1 {
		t.Errorf("row count = %d, want 1 -- a suspended caller must write nothing", n)
	}
}

// TestCreateHandler_SavesMappingOnCompletedImport builds the handler directly over
// the real svc.Import and store.SaveMapping: doImportUpload passes noSave.
func TestCreateHandler_SavesMappingOnCompletedImport(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})
	store := NewStore(app)

	tenantID := seedTenant(t, super, "SM-DB-04 tenant")
	entityID := seedEntity(t, super, tenantID, "SM-DB-04 entity")
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}

	header := []string{"Inv No", "Date", "Buyer", "Subtotal", "VAT", "Total"}
	rows := [][]string{{"SM-DB-04-1", "2026-01-15", "Acme Ltd", "100.00", "19.00", "119.00"}}
	mapping := map[string]string{
		"invoice_number": "Inv No", "issue_date": "Date", "buyer_name": "Buyer",
		"subtotal": "Subtotal", "vat": "VAT", "total": "Total",
	}
	mappingJSON, err := json.Marshal(mapping)
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}

	body, ct, open := dbStoredUpload(t, app, tenantID, entityID, string(mappingJSON), "data.csv", "", csvBody(t, header, rows))
	r := httptest.NewRequest("POST", "/v1/imports", body)
	r.Header.Set("Content-Type", ct)
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	CreateHandler(svc.Import, open, store.SaveMapping, nil).ServeHTTP(rec, r)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp importBatchBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "completed" {
		t.Fatalf("status = %q, want %q", resp.Status, "completed")
	}
	if n := countImportMappings(t, super, entityID); n != 1 {
		t.Fatalf("import_mappings rows for entity = %d, want exactly 1", n)
	}

	// A dry run into a second entity must save nothing.
	entityID2 := seedEntity(t, super, tenantID, "SM-DB-04 entity (dry run)")
	body2, ct2, open2 := dbStoredUpload(t, app, tenantID, entityID2, string(mappingJSON), "data.csv", "", csvBody(t, header, rows))
	r2 := httptest.NewRequest("POST", "/v1/imports?dry_run=true", body2)
	r2.Header.Set("Content-Type", ct2)
	r2 = r2.WithContext(auth.WithIdentity(r2.Context(), id))
	rec2 := httptest.NewRecorder()
	CreateHandler(svc.Import, open2, store.SaveMapping, nil).ServeHTTP(rec2, r2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("dry-run status = %d, want 200 (body=%s)", rec2.Code, rec2.Body.String())
	}
	if n := countImportMappings(t, super, entityID2); n != 0 {
		t.Errorf("import_mappings rows for the dry-run entity = %d, want 0", n)
	}
}

func TestCreateHandler_HeaderOnlyImportSavesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})
	store := NewStore(app)

	tenantID := seedTenant(t, super, "SM-DB-05 tenant")
	entityID := seedEntity(t, super, tenantID, "SM-DB-05 entity")
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}

	header := []string{"Inv No", "Total"}
	mapping := map[string]string{"invoice_number": "Inv No", "total": "Total"}
	mappingJSON, err := json.Marshal(mapping)
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}

	body, ct, open := dbStoredUpload(t, app, tenantID, entityID, string(mappingJSON), "data.csv", "", csvBody(t, header, nil))
	r := httptest.NewRequest("POST", "/v1/imports", body)
	r.Header.Set("Content-Type", ct)
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	CreateHandler(svc.Import, open, store.SaveMapping, nil).ServeHTTP(rec, r)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp importBatchBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "failed" {
		t.Fatalf("status = %q, want %q for a header-only file", resp.Status, "failed")
	}
	if n := countImportMappings(t, super, entityID); n != 0 {
		t.Fatalf("import_mappings rows = %d, want 0 for a failed batch", n)
	}

	// Control: a save for the same entity and header is visible to the same count.
	if err := store.SaveMapping(auth.WithIdentity(context.Background(), id), entityID, header, mapping); err != nil {
		t.Fatalf("control SaveMapping: %v", err)
	}
	if n := countImportMappings(t, super, entityID); n != 1 {
		t.Fatalf("control: import_mappings rows = %d, want 1", n)
	}
}

// TestCreateHandler_UnrememberedImportLeavesEarlierMapping builds import 2 from
// storeDocumentAs and buildImportForm: dbStoredUpload takes no extra parts.
func TestCreateHandler_UnrememberedImportLeavesEarlierMapping(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})
	store := NewStore(app)
	docSvc := document.NewService(document.NewStore(app), newMemObjects())

	tenantID := seedTenant(t, super, "SM-DB-08 tenant")
	entityID := seedEntity(t, super, tenantID, "SM-DB-08 entity")
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}

	header := []string{"Inv No", "Total"}
	m1 := map[string]string{"invoice_number": "Inv No"}
	m2 := map[string]string{"invoice_number": "Inv No", "total": "Total"}
	m1JSON := mustMappingJSON(t, m1)
	m2JSON := mustMappingJSON(t, m2)

	// Import 1: mapping M2, no remember_mapping (absent -> saves).
	doc1 := storeDocumentAs(t, docSvc, tenantID, "data1.csv", "", csvBody(t, header, [][]string{{"SM-DB-08-1", "119.00"}}))
	body1, ct1 := buildImportForm(t, entityID, m2JSON, doc1.ID)
	r1 := httptest.NewRequest("POST", "/v1/imports", body1)
	r1.Header.Set("Content-Type", ct1)
	r1 = r1.WithContext(auth.WithIdentity(r1.Context(), id))
	rec1 := httptest.NewRecorder()
	CreateHandler(svc.Import, docSvc.Open, store.SaveMapping, nil).ServeHTTP(rec1, r1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("import 1 status = %d, want 201 (body=%s)", rec1.Code, rec1.Body.String())
	}
	var resp1 importBatchBody
	if err := json.Unmarshal(rec1.Body.Bytes(), &resp1); err != nil {
		t.Fatalf("decode response 1: %v", err)
	}
	if resp1.Status != "completed" {
		t.Fatalf("import 1 status = %q, want %q", resp1.Status, "completed")
	}

	// Import 2: mapping M1, remember_mapping=false (must not overwrite M2).
	doc2 := storeDocumentAs(t, docSvc, tenantID, "data2.csv", "", csvBody(t, header, [][]string{{"SM-DB-08-2", "220.00"}}))
	body2, ct2 := buildImportForm(t, entityID, m1JSON, doc2.ID, importPart{field: "remember_mapping", content: []byte("false")})
	r2 := httptest.NewRequest("POST", "/v1/imports", body2)
	r2.Header.Set("Content-Type", ct2)
	r2 = r2.WithContext(auth.WithIdentity(r2.Context(), id))
	rec2 := httptest.NewRecorder()
	CreateHandler(svc.Import, docSvc.Open, store.SaveMapping, nil).ServeHTTP(rec2, r2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("import 2 status = %d, want 201 (body=%s)", rec2.Code, rec2.Body.String())
	}
	var resp2 importBatchBody
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("decode response 2: %v", err)
	}
	if resp2.Status != "completed" {
		t.Fatalf("import 2 status = %q, want %q", resp2.Status, "completed")
	}

	if n := countImportMappings(t, super, entityID); n != 1 {
		t.Fatalf("import_mappings rows = %d, want 1", n)
	}

	var mappingJSON string
	if err := super.QueryRow(context.Background(), `SELECT mapping::text FROM import_mappings WHERE entity_id = $1`, entityID).Scan(&mappingJSON); err != nil {
		t.Fatalf("read back mapping: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(mappingJSON), &got); err != nil {
		t.Fatalf("unmarshal stored mapping: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("stored mapping is empty")
	}
	if !reflect.DeepEqual(got, m2) {
		t.Errorf("stored mapping = %v, want M2 %v -- the unremembered import must not overwrite it", got, m2)
	}
}
