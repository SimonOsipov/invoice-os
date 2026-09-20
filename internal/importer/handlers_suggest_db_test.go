// handlers_suggest_db_test.go: Test Specs rows 5-7 (saved-mapping precedence, keyed on the
// resolved header) and row 14 (cross-tenant 404), DB-backed through dbTestPools/seedTenant,
// the real document.Service and the real Store.SavedMapping/SaveMapping.
package importer

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- row 5 ---------------------------------------------------------------------------

func TestSuggestHandler_ASavedMappingAtTheResolvedRowWinsOverTheAI(t *testing.T) {
	super, app := dbTestPools(t)
	docSvc := document.NewService(document.NewStore(app), newMemObjects())
	impStore := NewStore(app)

	tenantID := seedTenant(t, super, "AIR-07-02 saved-wins tenant")
	entityID := seedEntity(t, super, tenantID, "AIR-07-02 saved-wins entity")
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}

	// Header on row 3: ["Inv No", "Total"].
	doc := storeDocumentAs(t, docSvc, tenantID, "data.csv", "text/csv", buildTitleRowsFixture(1))

	resolvedHeader := []string{"Inv No", "Total"}
	savedMapping := map[string]string{"invoice_number": "Inv No"}
	saveCtx := auth.WithIdentity(context.Background(), id)
	if err := impStore.SaveMapping(saveCtx, entityID, resolvedHeader, savedMapping); err != nil {
		t.Fatalf("SaveMapping: %v", err)
	}

	// A different placement from the AI: proves the saved row wins, not the AI's.
	suggester := &fakeSuggester{enabled: true, answer: map[string]any{
		"header_row": json.Number("3"), "invoice_number": "Total",
	}}

	rec, raw := doSuggestRequest(t, docSvc.Open, impStore.SavedMapping, suggester, nil, &id, suggestReqBody(entityID, doc.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	// The lookup key is the resolved header, so the model is asked first even when the
	// saved row will win -- the accepted cost of one call per second import (D-08).
	if len(suggester.calls) != 1 {
		t.Errorf("suggester calls = %d, want exactly 1 -- the resolved row is what keys the lookup", len(suggester.calls))
	}
	resp := mustDecodeSuggest(t, raw)
	if resp.Source != "saved" {
		t.Fatalf("source = %q, want %q (body=%s)", resp.Source, "saved", raw)
	}
	if !reflect.DeepEqual(resp.Mapping, savedMapping) {
		t.Errorf("mapping = %v, want the stored mapping %v, not the AI's", resp.Mapping, savedMapping)
	}
	if resp.SavedAt == nil {
		t.Fatal("saved_at is nil, want non-nil for a saved hit")
	}
	// saved_at must be the stored row's own timestamp, not a response-time clock.
	stored, err := impStore.SavedMapping(saveCtx, entityID, resolvedHeader)
	if err != nil || stored == nil {
		t.Fatalf("SavedMapping readback: %v (row=%v)", err, stored)
	}
	if !resp.SavedAt.Equal(stored.SavedAt) {
		t.Errorf("saved_at = %v, want the stored row's %v", resp.SavedAt, stored.SavedAt)
	}
}

// --- row 6 ---------------------------------------------------------------------------

func TestSuggestHandler_NoSavedRowReturnsTheGuardedAIMapping(t *testing.T) {
	super, app := dbTestPools(t)
	docSvc := document.NewService(document.NewStore(app), newMemObjects())
	impStore := NewStore(app)

	tenantID := seedTenant(t, super, "AIR-07-02 ai-mapping tenant")
	entityID := seedEntity(t, super, tenantID, "AIR-07-02 ai-mapping entity")
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}

	doc := storeDocumentAs(t, docSvc, tenantID, "data.csv", "text/csv",
		csvBody(t, []string{"Inv No", "Total"}, [][]string{{"INV-1", "100"}}))

	suggester := &fakeSuggester{enabled: true, answer: map[string]any{
		"invoice_number": "Inv No", "total": "Total",
	}}

	rec, raw := doSuggestRequest(t, docSvc.Open, impStore.SavedMapping, suggester, nil, &id, suggestReqBody(entityID, doc.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustDecodeSuggest(t, raw)
	if resp.Source != "ai" {
		t.Fatalf("source = %q, want %q (body=%s)", resp.Source, "ai", raw)
	}
	if resp.SavedAt != nil {
		t.Errorf("saved_at = %v, want nil for an ai-sourced mapping", resp.SavedAt)
	}
	want := map[string]string{"invoice_number": "Inv No", "total": "Total"}
	if !reflect.DeepEqual(resp.Mapping, want) {
		t.Errorf("mapping = %v, want %v", resp.Mapping, want)
	}
}

// --- row 7 ---------------------------------------------------------------------------

func TestSuggestHandler_TheSavedLookupUsesTheResolvedHeaderNotRowOne(t *testing.T) {
	super, app := dbTestPools(t)
	docSvc := document.NewService(document.NewStore(app), newMemObjects())
	impStore := NewStore(app)

	tenantID := seedTenant(t, super, "AIR-07-02 resolved-lookup tenant")
	entityID := seedEntity(t, super, tenantID, "AIR-07-02 resolved-lookup entity")
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}

	// Row 1's header is ["Title"]; the saved row is keyed on row 3's ["Inv No", "Total"] only.
	doc := storeDocumentAs(t, docSvc, tenantID, "data.csv", "text/csv", buildTitleRowsFixture(1))

	resolvedHeader := []string{"Inv No", "Total"}
	savedMapping := map[string]string{"invoice_number": "Inv No"}
	saveCtx := auth.WithIdentity(context.Background(), id)
	if err := impStore.SaveMapping(saveCtx, entityID, resolvedHeader, savedMapping); err != nil {
		t.Fatalf("SaveMapping: %v", err)
	}

	suggester := &fakeSuggester{enabled: true, answer: map[string]any{"header_row": json.Number("3")}}

	rec, raw := doSuggestRequest(t, docSvc.Open, impStore.SavedMapping, suggester, nil, &id, suggestReqBody(entityID, doc.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustDecodeSuggest(t, raw)
	if resp.Source != "saved" {
		t.Errorf("source = %q, want %q -- a row-1 lookup would miss this saved row (body=%s)", resp.Source, "saved", raw)
	}
}

// --- row 14 --------------------------------------------------------------------------

func TestSuggestHandler_ACrossTenantDocumentIs404NotFiveHundred(t *testing.T) {
	super, app := dbTestPools(t)
	docSvc := document.NewService(document.NewStore(app), newMemObjects())
	impStore := NewStore(app)

	tenantA := seedTenant(t, super, "AIR-07-02 cross-tenant A")
	tenantB := seedTenant(t, super, "AIR-07-02 cross-tenant B")
	entityA := seedEntity(t, super, tenantA, "AIR-07-02 cross-tenant A entity")

	docB := storeDocumentAs(t, docSvc, tenantB, "data.csv", "text/csv",
		csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))

	// Control: the document is readable by its owner, so tenant A's 404 is a refusal
	// and not a fixture that stored nothing.
	entityB := seedEntity(t, super, tenantB, "AIR-07-02 cross-tenant B entity")
	idB := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantB}
	recB, rawB := doSuggestRequest(t, docSvc.Open, impStore.SavedMapping, &fakeSuggester{enabled: true}, nil, &idB, suggestReqBody(entityB, docB.ID))
	if recB.Code != http.StatusOK {
		t.Fatalf("owner status = %d, want 200 (body=%s) -- the control failed, so the 404 below proves nothing", recB.Code, rawB)
	}

	idA := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantA}
	suggester := &fakeSuggester{enabled: true}

	rec, raw := doSuggestRequest(t, docSvc.Open, impStore.SavedMapping, suggester, nil, &idA, suggestReqBody(entityA, docB.ID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, raw)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &errBody); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if errBody.Error != "not found" {
		t.Errorf("error = %q, want %q", errBody.Error, "not found")
	}
	if len(suggester.calls) != 0 {
		t.Errorf("suggester calls = %d, want 0 -- a 404 must never reach the AI", len(suggester.calls))
	}
}
