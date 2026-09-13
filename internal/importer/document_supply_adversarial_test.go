// document_supply_adversarial_test.go: edge and refusal coverage for Service.CarriedReading and
// Service.SupplyInvoiceNumber beyond the Test Specs rows in document_service_db_test.go.
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// advWithLines adds n lines named "<prefix>-L<i>" to a copy of values.
func advWithLines(values map[string]*string, prefix string, n int) map[string]*string {
	out := make(map[string]*string, len(values)+2*n)
	for k, v := range values {
		out[k] = v
	}
	for i := 1; i <= n; i++ {
		out[fmt.Sprintf("line_items[%d].description", i)] = sxPtr(fmt.Sprintf("%s-L%d", prefix, i))
		out[fmt.Sprintf("line_items[%d].line_total", i)] = sxPtr("10.00")
	}
	return out
}

// advSeedReadingAt seeds one job in state at createdAt, with one rank-0 row per value.
func advSeedReadingAt(t *testing.T, super *pgxpool.Pool, tenantID, documentID, state string, createdAt time.Time, values map[string]*string) {
	t.Helper()
	job := seedExtractionJob(t, super, tenantID, documentID, state, createdAt)
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		seedExtractionField(t, super, tenantID, job, name, values[name], nil, 0, createdAt.Add(time.Duration(i)*time.Millisecond))
	}
}

// advWantLines asserts the carried reading holds exactly n lines named "<prefix>-L<i>".
func advWantLines(t *testing.T, svc *Service, ctx context.Context, documentID, prefix string, n int) {
	t.Helper()
	reading, err := svc.CarriedReading(ctx, documentID)
	if err != nil || reading == nil {
		t.Fatalf("CarriedReading(%s): reading=%v err=%v, want a non-nil reading", prefix, reading, err)
	}
	if len(reading.LineItems) != n {
		t.Fatalf("%s carried %d line(s), want %d -- another document's lines leaked in or its own were lost", prefix, len(reading.LineItems), n)
	}
	for i, li := range reading.LineItems {
		want := fmt.Sprintf("%s-L%d", prefix, i+1)
		if li.Description == nil || *li.Description != want {
			t.Errorf("%s line %d description = %v, want %q", prefix, i+1, li.Description, want)
		}
	}
}

// Two documents per tenant with 2/1 and 3/0 lines: a read or a check that loses its document
// or tenant scope shows up as a wrong line count or a wrong null.
func TestRLS_CarriedReadingAndSupplyReadOnlyTheirOwnDocument(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "ADV-01 tenant A")
	entityA := seedEntity(t, super, tenantA, "ADV-01 entity A")
	a1 := docSeedDocument(t, super, tenantA)
	docSeedExtraction(t, super, tenantA, a1, advWithLines(docNoNumberValues(), "A1", 2))
	a2 := docSeedDocument(t, super, tenantA)
	docSeedExtraction(t, super, tenantA, a2, advWithLines(docNoNumberValues(), "A2", 1))

	tenantB := seedTenant(t, super, "ADV-01 tenant B")
	entityB := seedEntity(t, super, tenantB, "ADV-01 entity B")
	b1 := docSeedDocument(t, super, tenantB)
	docSeedExtraction(t, super, tenantB, b1, advWithLines(docNoNumberValues(), "B1", 3))
	b2 := docSeedDocument(t, super, tenantB)
	docSeedExtraction(t, super, tenantB, b2, docNoNumberValues())

	svc := newTestServiceWithGate(app, &fakeGate{})
	ctxA, ctxB := sxIdentity(ctx, tenantA), sxIdentity(ctx, tenantB)

	advWantLines(t, svc, ctxA, a1, "A1", 2)
	advWantLines(t, svc, ctxA, a2, "A2", 1)
	advWantLines(t, svc, ctxB, b1, "B1", 3)
	advWantLines(t, svc, ctxB, b2, "B2", 0)

	for _, doc := range []string{b1, b2} {
		if reading, err := svc.CarriedReading(ctxA, doc); err != nil || reading != nil {
			t.Errorf("A reading B's document %s: reading=%v err=%v, want (nil, nil)", doc, reading, err)
		}
		if _, err := svc.SupplyInvoiceNumber(ctxA, entityA, doc, "ADV-01-X"); !errors.Is(err, ErrNotFound) {
			t.Errorf("A supplying to B's document %s: err = %v, want ErrNotFound", doc, err)
		}
		if got := countInvoicesCitingDocument(t, super, doc); got != 0 {
			t.Errorf("invoices citing B's document %s = %d, want 0", doc, got)
		}
	}

	invA, err := svc.SupplyInvoiceNumber(ctxA, entityA, a1, "ADV-01-A1")
	if err != nil {
		t.Fatalf("A filing a1: %v", err)
	}
	if got := countLineItems(t, super, invA.ID); got != 2 {
		t.Errorf("line_items filed from a1 = %d, want 2", got)
	}
	// Filing a1 must not mark a2 (same tenant) or b1 (other tenant) as filed.
	advWantLines(t, svc, ctxA, a2, "A2", 1)
	advWantLines(t, svc, ctxB, b1, "B1", 3)

	invB, err := svc.SupplyInvoiceNumber(ctxB, entityB, b1, "ADV-01-B1")
	if err != nil {
		t.Fatalf("B filing b1: %v", err)
	}
	if got := countLineItems(t, super, invB.ID); got != 3 {
		t.Errorf("line_items filed from b1 = %d, want 3", got)
	}
}

// The wire answer for another tenant's document is byte-identical to the one for a document
// that does not exist, so the route is no existence oracle.
func TestRLS_ReadingHandlerAnswersNullForAnotherTenantsDocument(t *testing.T) {
	super, app := dbTestPools(t)

	tenantA := seedTenant(t, super, "ADV-02 tenant A")
	doc := docSeedDocument(t, super, tenantA)
	docSeedExtraction(t, super, tenantA, doc, docNoNumberValues())
	tenantB := seedTenant(t, super, "ADV-02 tenant B")

	svc := newTestServiceWithGate(app, &fakeGate{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/imports/document/reading", ReadingHandler(svc.CarriedReading, nil))
	get := func(tenantID, documentID string) (int, string) {
		r := httptest.NewRequest("GET", "/v1/imports/document/reading?document_id="+documentID, nil)
		r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec.Code, rec.Body.String()
	}

	code, body := get(tenantA, doc)
	if code != http.StatusOK || !strings.Contains(body, `"document_id":"`+doc+`"`) {
		t.Fatalf("owner read: %d %s, want 200 with the reading", code, body)
	}
	foreignCode, foreignBody := get(tenantB, doc)
	missingCode, missingBody := get(tenantB, uuid.NewString())
	if foreignCode != http.StatusOK || foreignBody != `{"reading":null}`+"\n" {
		t.Errorf("foreign read: %d %q, want 200 {\"reading\":null}", foreignCode, foreignBody)
	}
	if foreignCode != missingCode || foreignBody != missingBody {
		t.Errorf("foreign read (%d %q) differs from a missing document (%d %q)", foreignCode, foreignBody, missingCode, missingBody)
	}
}

func TestRLS_SupplyUnderAnotherTenantsEntityWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "ADV-03 tenant A")
	entityA := seedEntity(t, super, tenantA, "ADV-03 entity A")
	doc := docSeedDocument(t, super, tenantA)
	docSeedExtraction(t, super, tenantA, doc, docNoNumberValues())
	tenantB := seedTenant(t, super, "ADV-03 tenant B")
	entityB := seedEntity(t, super, tenantB, "ADV-03 entity B")

	g := &fakeGate{}
	svc := newTestServiceWithGate(app, g)
	ctxA := sxIdentity(ctx, tenantA)

	if _, err := svc.SupplyInvoiceNumber(ctxA, entityB, doc, "ADV-03-INV"); !errors.Is(err, invoice.ErrValidation) {
		t.Fatalf("supply under B's entity: err = %v, want invoice.ErrValidation", err)
	}
	if got := countInvoicesCitingDocument(t, super, doc); got != 0 {
		t.Errorf("invoices citing the document = %d, want 0", got)
	}
	if got := countInvoicesForEntity(t, super, entityB); got != 0 {
		t.Errorf("invoices on B's entity = %d, want 0", got)
	}
	if g.validateBatchCalls != 0 {
		t.Errorf("gate.ValidateBatch calls = %d, want 0", g.validateBatchCalls)
	}

	// No lost work: the same number still files under the caller's own entity.
	inv, err := svc.SupplyInvoiceNumber(ctxA, entityA, doc, "ADV-03-INV")
	if err != nil {
		t.Fatalf("supply under A's entity: %v", err)
	}
	if inv.EntityID != entityA {
		t.Errorf("EntityID = %q, want %q", inv.EntityID, entityA)
	}
}

// Documents carry no entity, so carry-once is per document: a second entity of the same
// tenant cannot file the reading again.
func TestServiceSupplyInvoiceNumber_CarryOnceHoldsAcrossEntities(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-04 tenant")
	e1 := seedEntity(t, super, tenantID, "ADV-04 entity 1")
	e2 := seedEntity(t, super, tenantID, "ADV-04 entity 2")
	doc := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, doc, docNoNumberValues())

	svc := newTestServiceWithGate(app, &fakeGate{})
	callCtx := sxIdentity(ctx, tenantID)

	if _, err := svc.SupplyInvoiceNumber(callCtx, e1, doc, "ADV-04-INV"); err != nil {
		t.Fatalf("supply under entity 1: %v", err)
	}
	if _, err := svc.SupplyInvoiceNumber(callCtx, e2, doc, "ADV-04-INV"); !errors.Is(err, ErrDocumentAlreadyFiled) {
		t.Errorf("supply under entity 2: err = %v, want ErrDocumentAlreadyFiled", err)
	}
	if got := countInvoicesCitingDocument(t, super, doc); got != 1 {
		t.Errorf("invoices citing the document = %d, want 1", got)
	}
	if got := countInvoicesForEntity(t, super, e2); got != 0 {
		t.Errorf("invoices on entity 2 = %d, want 0", got)
	}
}

// Only the newest succeeded extraction is the reading. A job in any other state is no reading,
// and a newer succeeded reading that found a number replaces the no-number one.
func TestServiceSupplyInvoiceNumber_OnlyTheNewestSucceededNoNumberReadingFiles(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-05 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-05 entity")
	callCtx := sxIdentity(ctx, tenantID)
	now := time.Now().UTC()

	seedDoc := func(jobs ...func(doc string)) string {
		doc := docSeedDocument(t, super, tenantID)
		for _, j := range jobs {
			j(doc)
		}
		return doc
	}
	job := func(state string, at time.Time, values map[string]*string) func(string) {
		return func(doc string) { advSeedReadingAt(t, super, tenantID, doc, state, at, values) }
	}
	badDate := docNoNumberValues()
	badDate["issue_date"] = sxPtr("13/02/2026")

	cases := []struct {
		name    string
		doc     string
		wantErr error
	}{
		{"failed job only", seedDoc(job("failed", now, docNoNumberValues())), ErrNotFound},
		{"extracting job only", seedDoc(job("extracting", now, docNoNumberValues())), ErrNotFound},
		{"queued job only", seedDoc(job("queued", now, docNoNumberValues())), ErrNotFound},
		{"unreadable issue date", seedDoc(job("succeeded", now, badDate)), ErrReadingNotCarried},
		{"re-read with a number", seedDoc(
			job("succeeded", now.Add(-time.Hour), docNoNumberValues()),
			job("succeeded", now, docCleanValues("ADV-05-READ")),
		), ErrReadingNotCarried},
	}

	g := &fakeGate{}
	svc := newTestServiceWithGate(app, g)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if reading, err := svc.CarriedReading(callCtx, tc.doc); err != nil || reading != nil {
				t.Errorf("CarriedReading: reading=%v err=%v, want (nil, nil)", reading, err)
			}
			if _, err := svc.SupplyInvoiceNumber(callCtx, entityID, tc.doc, "ADV-05-X"); !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if got := countInvoicesCitingDocument(t, super, tc.doc); got != 0 {
				t.Errorf("invoices citing the document = %d, want 0", got)
			}
		})
	}
	if g.validateBatchCalls != 0 {
		t.Errorf("gate.ValidateBatch calls = %d, want 0", g.validateBatchCalls)
	}

	// Control: a newer failed job leaves the older succeeded no-number reading fileable.
	control := seedDoc(
		job("succeeded", now.Add(-time.Hour), docNoNumberValues()),
		job("failed", now, map[string]*string{}),
	)
	if _, err := svc.SupplyInvoiceNumber(callCtx, entityID, control, "ADV-05-CONTROL"); err != nil {
		t.Fatalf("control supply: %v", err)
	}
}

// The real flow: the import quarantines first, then the operator supplies the number. The
// invoice cites the document and no batch, and the quarantined batch row does not change.
func TestServiceSupplyInvoiceNumber_TheQuarantinedImportsBatchIsLeftAlone(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-06 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-06 entity")
	doc := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, doc, docNoNumberValues())

	svc := newTestServiceWithGate(app, &fakeGate{})
	callCtx := sxIdentity(ctx, tenantID)

	res, err := svc.ImportDocument(callCtx, entityID, doc)
	if err != nil || res.QuarantinedInvoices != 1 || res.ID == "" {
		t.Fatalf("ImportDocument: res=%+v err=%v, want one quarantined document and a batch id", res, err)
	}
	batchID, status, total, valid, invalid := docBatchRowByEntity(t, super, entityID)

	if reading, err := svc.CarriedReading(callCtx, doc); err != nil || reading == nil {
		t.Fatalf("CarriedReading after the quarantine: reading=%v err=%v, want a non-nil reading", reading, err)
	}
	inv, err := svc.SupplyInvoiceNumber(callCtx, entityID, doc, "ADV-06-INV")
	if err != nil {
		t.Fatalf("SupplyInvoiceNumber: %v", err)
	}
	if inv.ImportBatchID != nil {
		t.Errorf("returned ImportBatchID = %v, want nil", *inv.ImportBatchID)
	}
	gotDoc, gotBatch := docInvoiceLinks(t, super, inv.ID)
	if gotDoc != doc || gotBatch != "" {
		t.Errorf("stored links = (doc=%q batch=%q), want (doc=%q batch=\"\")", gotDoc, gotBatch, doc)
	}
	if got := countImportBatchesForEntity(t, super, entityID); got != 1 {
		t.Errorf("import_batches for entity = %d, want 1", got)
	}
	id2, status2, total2, valid2, invalid2 := docBatchRowByEntity(t, super, entityID)
	if id2 != batchID || status2 != status || total2 != total || valid2 != valid || invalid2 != invalid {
		t.Errorf("batch row moved: (%s %s %d/%d/%d) -> (%s %s %d/%d/%d)", batchID, status, total, valid, invalid, id2, status2, total2, valid2, invalid2)
	}
}

// The worker writes no row for an absent line cell (extraction.LineItemResults), so a line can
// lack any role. Absent cells carry as JSON null and file as NULL columns.
func TestServiceCarriedReading_ALineWithAbsentCellsCarriesThemAsNull(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-07 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-07 entity")
	doc := docSeedDocument(t, super, tenantID)
	values := docNoNumberValues()
	values["line_items[1].description"] = sxPtr("Description only")
	values["line_items[2].line_total"] = sxPtr("5.00")
	values["line_items[4].quantity"] = sxPtr("3")
	docSeedExtraction(t, super, tenantID, doc, values)

	svc := newTestServiceWithGate(app, &fakeGate{})
	callCtx := sxIdentity(ctx, tenantID)

	reading, err := svc.CarriedReading(callCtx, doc)
	if err != nil || reading == nil {
		t.Fatalf("CarriedReading: reading=%v err=%v", reading, err)
	}
	if len(reading.LineItems) != 3 {
		t.Fatalf("len(LineItems) = %d, want 3 (index 3 is a hole, not a line)", len(reading.LineItems))
	}
	wantJSON := []string{
		`{"description":"Description only","quantity":null,"unit_price":null,"line_total":null,"line_tax":null}`,
		`{"description":null,"quantity":null,"unit_price":null,"line_total":"5.00","line_tax":null}`,
		`{"description":null,"quantity":"3","unit_price":null,"line_total":null,"line_tax":null}`,
	}
	for i, li := range reading.LineItems {
		b, err := json.Marshal(li)
		if err != nil {
			t.Fatalf("marshal line %d: %v", i+1, err)
		}
		if string(b) != wantJSON[i] {
			t.Errorf("line %d = %s, want %s", i+1, b, wantJSON[i])
		}
	}

	inv, err := svc.SupplyInvoiceNumber(callCtx, entityID, doc, "ADV-07-INV")
	if err != nil {
		t.Fatalf("SupplyInvoiceNumber: %v", err)
	}
	if len(inv.LineItems) != 3 {
		t.Fatalf("filed LineItems = %d, want 3", len(inv.LineItems))
	}
	if inv.LineItems[0].Description == nil || inv.LineItems[1].Description != nil || inv.LineItems[2].Description != nil {
		t.Errorf("filed descriptions = %v/%v/%v, want set/nil/nil", inv.LineItems[0].Description, inv.LineItems[1].Description, inv.LineItems[2].Description)
	}
	if inv.LineItems[2].Quantity == nil || inv.LineItems[0].Quantity != nil {
		t.Errorf("filed quantities = %v/%v, want nil then set", inv.LineItems[0].Quantity, inv.LineItems[2].Quantity)
	}
}

// advGateReading is a no-number reading the real rule set passes (IMPV-CLEAN-1's figures) when
// vat is "18.75", and blocks on vat-standard-rate only when vat is wrong.
func advGateReading(vat string) map[string]*string {
	return map[string]*string{
		"invoice_number":            nil,
		"issue_date":                sxPtr("2026-07-01"),
		"buyer_tin":                 sxPtr("87654321-0002"),
		"buyer_name":                sxPtr("Beta Ltd"),
		"currency":                  sxPtr("NGN"),
		"subtotal":                  sxPtr("250.00"),
		"vat":                       sxPtr(vat),
		"total":                     sxPtr("268.75"),
		"line_items[1].description": sxPtr("Item1"),
		"line_items[1].quantity":    sxPtr("2"),
		"line_items[1].unit_price":  sxPtr("100.00"),
		"line_items[2].description": sxPtr("Item2"),
		"line_items[2].quantity":    sxPtr("1"),
		"line_items[2].unit_price":  sxPtr("50.00"),
	}
}

// The returned invoice is the one after validation, so the operator lands on the verdict. A
// return of the pre-gate invoice reads draft with no verdict on both documents.
func TestServiceSupplyInvoiceNumber_ReturnsTheRealGatesVerdict(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-08 tenant")
	entityID := seedEntityWithTIN(t, super, tenantID, "ADV-08 entity", "12345678-0001")
	cleanDoc := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, cleanDoc, advGateReading("18.75"))
	blockedDoc := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, blockedDoc, advGateReading("1.00"))

	srv := startInProcess04ForImporter(t, app)
	realGate := invoice.NewGate(invoice.NewStore(app), invoice.NewValidator(srv.URL, impvS2SToken, nil))
	svc := newTestServiceWithGate(app, realGate)
	callCtx := sxIdentity(ctx, tenantID)

	clean, err := svc.SupplyInvoiceNumber(callCtx, entityID, cleanDoc, "ADV-08-CLEAN")
	if err != nil {
		t.Fatalf("supply clean: %v", err)
	}
	if clean.Status != invoice.StatusValidated || clean.RuleSetVersionID == nil {
		t.Errorf("clean: status = %q, rule_set_version_id = %v, want validated and stamped -- violations %s", clean.Status, clean.RuleSetVersionID, clean.Violations)
	}
	var stored string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, clean.ID).Scan(&stored); err != nil {
		t.Fatalf("read stored status: %v", err)
	}
	if stored != string(clean.Status) {
		t.Errorf("stored status %q differs from returned %q", stored, clean.Status)
	}

	blocked, err := svc.SupplyInvoiceNumber(callCtx, entityID, blockedDoc, "ADV-08-VAT")
	if err != nil {
		t.Fatalf("supply blocked: %v", err)
	}
	if blocked.Status != invoice.StatusDraft {
		t.Errorf("blocked: status = %q, want draft", blocked.Status)
	}
	var vs []invoice.Violation
	if err := json.Unmarshal(blocked.Violations, &vs); err != nil {
		t.Fatalf("unmarshal violations %s: %v", blocked.Violations, err)
	}
	var sawVAT bool
	for _, v := range vs {
		sawVAT = sawVAT || v.RuleKey == "vat-standard-rate"
	}
	if !sawVAT {
		t.Errorf("blocked violations = %+v, want one naming vat-standard-rate", vs)
	}
}
