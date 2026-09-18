package importer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// aiMarkerNearMisses are field sets one step off the worker's marker; each must take the
// read-document branch.
func aiMarkerNearMisses() []struct {
	name   string
	fields []extractedField
} {
	marker := func(name string, reason *string) extractedField {
		return extractedField{Name: name, Value: nil, Reason: reason}
	}
	return []struct {
		name   string
		fields []extractedField
	}{
		{"empty set", []extractedField{}},
		{"nil set", nil},
		{"two marker rows", []extractedField{marker("document_ai_reading", mpPtr("unreadable")), marker("document_ai_reading", mpPtr("unreadable"))}},
		{"name title case", []extractedField{marker("Document_AI_Reading", mpPtr("unreadable"))}},
		{"name upper case", []extractedField{marker("DOCUMENT_AI_READING", mpPtr("unreadable"))}},
		{"name trailing space", []extractedField{marker("document_ai_reading ", mpPtr("unreadable"))}},
		{"name prefix only", []extractedField{marker("document_ai", mpPtr("unreadable"))}},
		{"reason title case", []extractedField{marker("document_ai_reading", mpPtr("Unreadable"))}},
		{"reason ambiguous", []extractedField{marker("document_ai_reading", mpPtr("ambiguous"))}},
		{"reason inconsistent", []extractedField{marker("document_ai_reading", mpPtr("inconsistent"))}},
		{"reason missing", []extractedField{marker("document_ai_reading", mpPtr("missing"))}},
		{"poor-scan row alone", []extractedField{marker("document_text_layer", mpPtr("unreadable"))}},
	}
}

func TestIsAIUnavailable_EveryNearMissTakesTheReadDocumentBranch(t *testing.T) {
	cases := aiMarkerNearMisses()
	if len(cases) == 0 {
		t.Fatal("no near-miss cases; the loop below would pass vacuously")
	}
	want := ac2Message(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if isAIUnavailable(tc.fields) {
				t.Errorf("isAIUnavailable = true, want false")
			}
			_, rowErr := documentCreateInput("entity-1", "doc-1", SettledExtraction{Fields: tc.fields})
			if rowErr == nil {
				t.Fatal("rowErr = nil, want a quarantine -- none of these sets carries an invoice number")
			}
			if tc.name == "poor-scan row alone" {
				return // the scan branch owns it; TestIsPoorScan_TheAIMarkerIsNotAPoorScan pins that
			}
			if rowErr.Message != want {
				t.Errorf("Message = %q, want the read-document message %q", rowErr.Message, want)
			}
		})
	}
	// Control: the exact marker still takes the AI branch, so the negatives are not vacuous.
	if _, rowErr := documentCreateInput("entity-1", "doc-1", aiUnavailableFieldSet()); rowErr == nil || rowErr.Message != aiUnavailableMessage {
		t.Fatalf("exact marker: rowErr = %+v, want aiUnavailableMessage", rowErr)
	}
}

// The worker never writes a marker value; if one appears it still names no invoice.
func TestDocumentCreateInput_AMarkerWithAValueStillQuarantinesAndCarriesNothing(t *testing.T) {
	ex := SettledExtraction{Fields: []extractedField{
		{Name: "document_ai_reading", Value: mpPtr("INV-9"), Reason: mpPtr("unreadable")},
	}}
	_, rowErr := documentCreateInput("entity-1", "doc-1", ex)
	if rowErr == nil {
		t.Fatal("rowErr = nil, want a quarantine -- a marker value is not an invoice number")
	}
	if rowErr.Field != "invoice_number" || rowErr.RuleKey != "" {
		t.Errorf("rowErr = %+v, want Field invoice_number and no RuleKey", rowErr)
	}
	if _, ok := carriedInput("doc-1", ex); ok {
		t.Error("carriedInput ok = true, want false")
	}
}

// Every marker-shaped set refuses to carry. Refusal comes from two gates (the message and the
// all-null floor); only a mutation dropping both reds this.
func TestCarriedInput_NoMarkerShapeIsCarried(t *testing.T) {
	sets := map[string]SettledExtraction{
		"exact marker":      aiUnavailableFieldSet(),
		"two marker rows":   {Fields: []extractedField{{Name: "document_ai_reading", Reason: mpPtr("unreadable")}, {Name: "document_ai_reading", Reason: mpPtr("unreadable")}}},
		"marker, no reason": {Fields: []extractedField{{Name: "document_ai_reading"}}},
	}
	if len(sets) == 0 {
		t.Fatal("no sets; the loop below would pass vacuously")
	}
	for name, ex := range sets {
		if _, ok := carriedInput("doc-1", ex); ok {
			t.Errorf("%s: carriedInput ok = true, want false", name)
		}
	}
	if _, ok := carriedInput("doc-1", readDocumentNoNumberFieldSet()); !ok {
		t.Fatal("control: carriedInput(read-document, no number) ok = false, want true")
	}
}

// Core AC-3 and AC-4 at the HTTP layer, over the real service: the import response and the
// stored batch carry the sentence verbatim, the reading route answers null, supply refuses.
func TestDocumentRoutes_AnAIUnavailableDocumentQuarantinesAndCarriesNothing(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "AIR-04-02 tenant")
	entityID := seedEntity(t, super, tenantID, "AIR-04-02 entity")
	aiDoc := docSeedDocument(t, super, tenantID)
	docSeedAIUnavailableExtraction(t, super, tenantID, aiDoc)
	controlDoc := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, controlDoc, docNoNumberValues())

	svc := newTestService(app)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/imports/document", CreateDocumentHandler(svc.ImportDocument, nil))
	mux.HandleFunc("GET /v1/imports/document/reading", ReadingHandler(svc.CarriedReading, nil))
	mux.HandleFunc("POST /v1/imports/document/invoice", SupplyNumberHandler(svc.SupplyInvoiceNumber, nil))
	mux.HandleFunc("GET /v1/imports/{id}", GetHandler(NewStore(app).GetBatch, nil))

	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}
	do := func(method, target, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r = r.WithContext(auth.WithIdentity(r.Context(), id))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec
	}
	wantErrors := func(where string, raw []byte) {
		t.Helper()
		var body struct {
			Errors []map[string]any `json:"errors"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("%s: decode %q: %v", where, raw, err)
		}
		if len(body.Errors) != 1 {
			t.Fatalf("%s: errors = %v, want exactly one", where, body.Errors)
		}
		e := body.Errors[0]
		if e["message"] != aiUnavailableMessage || e["field"] != "invoice_number" {
			t.Errorf("%s: errors[0] = %v, want field invoice_number, message %q", where, e, aiUnavailableMessage)
		}
		if _, has := e["rule_key"]; has {
			t.Errorf("%s: errors[0] carries rule_key %v, want none", where, e["rule_key"])
		}
	}

	rec := do("POST", "/v1/imports/document", docJSONBody(entityID, aiDoc))
	if rec.Code != http.StatusCreated {
		t.Fatalf("import status = %d, want 201 -- body %s", rec.Code, rec.Body.String())
	}
	wantErrors("POST /v1/imports/document", rec.Body.Bytes())
	var created struct {
		ID                  string `json:"id"`
		QuarantinedInvoices int    `json:"quarantined_invoices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("decode batch id: %v (body %s)", err, rec.Body.String())
	}
	if created.QuarantinedInvoices != 1 {
		t.Errorf("quarantined_invoices = %d, want 1", created.QuarantinedInvoices)
	}

	rec = do("GET", "/v1/imports/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("batch read status = %d, want 200 -- body %s", rec.Code, rec.Body.String())
	}
	wantErrors("GET /v1/imports/{id}", rec.Body.Bytes())

	rec = do("GET", "/v1/imports/document/reading?document_id="+aiDoc, "")
	if rec.Code != http.StatusOK || rec.Body.String() != `{"reading":null}`+"\n" {
		t.Errorf("reading = %d %q, want 200 {\"reading\":null}", rec.Code, rec.Body.String())
	}

	rec = do("POST", "/v1/imports/document/invoice", supplyJSONBody(entityID, aiDoc, "AIR04-1"))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), readingNotCarriedReason) {
		t.Errorf("supply = %d %q, want 409 with %q", rec.Code, rec.Body.String(), readingNotCarriedReason)
	}
	if got := countInvoicesCitingDocument(t, super, aiDoc); got != 0 {
		t.Errorf("invoices citing the AI-unavailable document = %d, want 0", got)
	}

	// Control: the same wiring carries a no-number reading, so the null above is not the route's default.
	rec = do("GET", "/v1/imports/document/reading?document_id="+controlDoc, "")
	var ctl struct {
		Reading *CarriedReading `json:"reading"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ctl); err != nil || ctl.Reading == nil || ctl.Reading.DocumentID != controlDoc {
		t.Fatalf("control reading = %d %q, want a non-null reading for %s", rec.Code, rec.Body.String(), controlDoc)
	}
}
