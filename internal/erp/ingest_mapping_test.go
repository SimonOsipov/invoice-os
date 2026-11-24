package erp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

const (
	tinX = "12345678-0001"
	tinY = "87654321-0002"
)

func (e *ingestEnv) override(entity, system, target, path string) {
	e.t.Helper()
	mustExec(e.t, e.super,
		"INSERT INTO erp_field_mappings (tenant_id, entity_id, system, target, source_path) VALUES ($1, $2, $3, $4, $5)",
		e.tenant, entity, system, target, path)
}

func (e *ingestEnv) lookup(entity, system, company string, l Lookup) {
	e.t.Helper()
	mustExec(e.t, e.super,
		"INSERT INTO erp_lookups (tenant_id, entity_id, system, external_key, kind, erp_key, tax_category, exemption_code, hsn_code, service_code, tin, state, lga)"+
			" VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)",
		e.tenant, entity, system, company, l.Kind, l.ERPKey, l.TaxCategory, l.ExemptionCode, l.HSNCode, l.ServiceCode, l.TIN, l.State, l.LGA)
}

func (e *ingestEnv) workOn(conn, docID, rev string) {
	e.t.Helper()
	a := IngestArgs{TenantID: e.tenant, ConnectionID: conn, SourceDocID: docID, SourceRevision: rev}
	if err := e.workAs(context.Background(), e.worker, a, 1); err != nil {
		e.t.Fatalf("Work(%s on %s): %v", docID, conn, err)
	}
}

// draftOn is the invoice a connection's document landed as.
func (e *ingestEnv) draftOn(conn, docID string) string {
	e.t.Helper()
	d, ok := e.docOn(conn, docID)
	if !ok || d.InvoiceID == nil {
		e.t.Fatalf("document %s on %s has no invoice (row %+v)", docID, conn, d)
	}
	return *d.InvoiceID
}

type draftLine struct{ Desc, TaxCategory, HSN, ISIC *string }

func (e *ingestEnv) draftLines(invoiceID string) []draftLine {
	e.t.Helper()
	rows, err := e.super.Query(context.Background(),
		"SELECT description, tax_category, hsn_code, isic_code FROM line_items WHERE invoice_id = $1 ORDER BY line_no", invoiceID)
	if err != nil {
		e.t.Fatalf("lines: %v", err)
	}
	defer rows.Close()
	var out []draftLine
	for rows.Next() {
		var l draftLine
		if err := rows.Scan(&l.Desc, &l.TaxCategory, &l.HSN, &l.ISIC); err != nil {
			e.t.Fatalf("scan line: %v", err)
		}
		out = append(out, l)
	}
	return out
}

type buyerRow struct{ TIN, State, LGA *string }

func (e *ingestEnv) buyer(invoiceID string) buyerRow {
	e.t.Helper()
	var b buyerRow
	mustScan(e.t, e.super, "SELECT buyer_tin, buyer_state, buyer_lga FROM invoices WHERE id = $1", []any{invoiceID}, &b.TIN, &b.State, &b.LGA)
	return b
}

// serveBy serves each document by its id.
func (e *ingestEnv) serveBy(docs map[string]Document) {
	e.onFetch = func(_ context.Context, ref DocRef) (Document, error) {
		d, ok := docs[ref.SourceDocID]
		if !ok {
			return Document{}, ErrNotFound
		}
		return d, nil
	}
}

// withLineFields sets the given keys on each line of doc's body.
func withLineFields(t *testing.T, doc Document, per []map[string]any) Document {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(doc.Body, &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	lines := body["lines"].([]any)
	if len(lines) != len(per) {
		t.Fatalf("%d lines, %d field sets", len(lines), len(per))
	}
	for i, l := range lines {
		for k, v := range per[i] {
			l.(map[string]any)[k] = v
		}
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	doc.Body = b
	return doc
}

// withBodyKey sets (or, for nil, removes) a header key of doc's body.
func withBodyKey(t *testing.T, doc Document, key string, v any) Document {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(doc.Body, &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if v == nil {
		delete(body, key)
	} else {
		body[key] = v
	}
	b, _ := json.Marshal(body)
	doc.Body = b
	return doc
}

// mappedUnder is what mapping m makes of doc, as the ingest carries it before lookups.
func (e *ingestEnv) mappedUnder(m Mapping, doc Document) invoice.CreateInput {
	e.t.Helper()
	f := testFake(e.t)
	id, err := CustomerIDOf(m, doc)
	if err != nil {
		e.t.Fatalf("customer: %v", err)
	}
	var party *Party
	if p, err := f.FetchParty(context.Background(), Credentials{}, e.key, id); err == nil {
		party = &p
	}
	mapped, err := Apply(m, doc, party)
	if err != nil {
		e.t.Fatalf("map: %v", err)
	}
	mapped.In.EntityID = e.entity
	mapped.In.ERPConnectionID = &e.conn
	return mapped.In
}

func (e *ingestEnv) seededDoc(docID string) Document {
	e.t.Helper()
	doc, err := testFake(e.t).Fetch(context.Background(), Credentials{}, DocRef{SourceDocID: docID, SourceRevision: fakeRevision, Scope: e.key})
	if err != nil {
		e.t.Fatalf("fetch: %v", err)
	}
	return doc
}

// wantFingerprint is the default mapping's fingerprint on the document's row: every state that stores a hash stores it.
func (e *ingestEnv) wantFingerprint(docID string) {
	e.t.Helper()
	var fp *string
	mustScan(e.t, e.super, "SELECT mapping_fingerprint FROM erp_source_documents WHERE connection_id = $1 AND source_doc_id = $2", []any{e.conn, docID}, &fp)
	if want := Fingerprint(testFake(e.t).DefaultMapping()); fp == nil || *fp != want {
		e.t.Errorf("document %s mapping_fingerprint = %v, want %s", docID, fp, want)
	}
}

func TestIngest_ClientOverrideWinsOverTheDefault(t *testing.T) {
	e := newIngest(t)
	e.override(e.entity, "fake", "line_description", "lines[].item_id")
	e.mustWork(docOne, "1")

	inv := e.draftOn(e.conn, docOne)
	ls := e.draftLines(inv)
	if len(ls) != 1 || str0(ls[0].Desc) != "fake-item-consulting" {
		t.Fatalf("lines = %+v, want one line described fake-item-consulting", ls)
	}
	var same bool
	mustScan(t, e.super, `SELECT l.quantity = 1 AND l.unit_price = 100000 AND i.subtotal = 100000 AND i.vat = 7500 AND i.total = 107500
		FROM line_items l JOIN invoices i ON i.id = l.invoice_id WHERE i.id = $1`, []any{inv}, &same)
	if !same {
		t.Error("quantity, price or totals moved with the description override")
	}
}

func TestIngest_OverridesOfAnotherClientOrSystemAreNotRead(t *testing.T) {
	e := newIngest(t)
	y := e.addClient("ingest client Y")
	yConn := e.addConnection(y, keyOne)
	e.override(e.entity, "fake", "line_description", "lines[].item_id")
	e.override(y, "other", "line_description", "lines[].item_id")

	e.workOn(yConn, docOne, "1")
	if got := e.draftLines(e.draftOn(yConn, docOne)); str0(got[0].Desc) != "Consulting services" {
		t.Errorf("Y's description = %s, want the default's", str0(got[0].Desc))
	}
	e.workOn(e.conn, docOne, "1")
	if got := e.draftLines(e.draftOn(e.conn, docOne)); str0(got[0].Desc) != "fake-item-consulting" {
		t.Errorf("X's description = %s, want its override's", str0(got[0].Desc))
	}
}

func TestIngest_MappedTaxCodeItemAndCustomerReachTheDraft(t *testing.T) {
	e := newIngest(t)
	doc := withLineFields(t, fakeDocument(docOne, numOne, custOne, seedLine, seedLine, seedLine), []map[string]any{
		{"tax_code": "A", "item_id": "i1", "tax_category": "body-cat-1", "hsn_code": "body-hsn-1", "isic_code": "body-isic-1"},
		{"tax_code": "B", "item_id": "", "tax_category": "body-cat-2", "hsn_code": "body-hsn-2", "isic_code": "body-isic-2"},
		{"tax_code": "A", "item_id": "i2", "tax_category": "body-cat-3", "hsn_code": "body-hsn-3", "isic_code": "body-isic-3"},
	})
	e.serve(doc)
	e.lookup(e.entity, "fake", keyOne, Lookup{Kind: "tax_code", ERPKey: "A", TaxCategory: sp("ZERO_VAT"), ExemptionCode: sp("2915.3100")})
	e.lookup(e.entity, "fake", keyOne, Lookup{Kind: "tax_code", ERPKey: "B", TaxCategory: sp("EXEMPTED")})
	e.lookup(e.entity, "fake", keyOne, Lookup{Kind: "item", ERPKey: "i1", HSNCode: sp("H")})
	e.lookup(e.entity, "fake", keyOne, Lookup{Kind: "item", ERPKey: "i2", ServiceCode: sp("S")})
	e.lookup(e.entity, "fake", keyOne, Lookup{Kind: "customer", ERPKey: custOne, TIN: sp(tinX), State: sp("Lagos"), LGA: sp("Ikeja")})
	e.mustWork(docOne, "1")

	inv := e.draftOn(e.conn, docOne)
	ls := e.draftLines(inv)
	want := []draftLine{
		{TaxCategory: sp("ZERO_VAT"), HSN: sp("H"), ISIC: sp("<NULL>")},
		{TaxCategory: sp("EXEMPTED"), HSN: sp("body-hsn-2"), ISIC: sp("body-isic-2")},
		{TaxCategory: sp("ZERO_VAT"), HSN: sp("<NULL>"), ISIC: sp("S")},
	}
	if len(ls) != 3 {
		t.Fatalf("lines = %d, want 3", len(ls))
	}
	for i, w := range want {
		if str0(ls[i].TaxCategory) != *w.TaxCategory || str0(ls[i].HSN) != *w.HSN || str0(ls[i].ISIC) != *w.ISIC {
			t.Errorf("line %d = %s/%s/%s, want %s/%s/%s", i+1, str0(ls[i].TaxCategory), str0(ls[i].HSN), str0(ls[i].ISIC), *w.TaxCategory, *w.HSN, *w.ISIC)
		}
	}
	if b := e.buyer(inv); str0(b.TIN) != tinX || str0(b.State) != "Lagos" || str0(b.LGA) != "Ikeja" {
		t.Errorf("buyer = %s/%s/%s, want the lookup's", str0(b.TIN), str0(b.State), str0(b.LGA))
	}
	var n int
	mustScan(t, e.super, `SELECT (SELECT count(*) FROM invoices i WHERE to_jsonb(i)::text LIKE '%2915.3100%')
		+ (SELECT count(*) FROM line_items l WHERE to_jsonb(l)::text LIKE '%2915.3100%')`, nil, &n)
	if n != 0 {
		t.Errorf("%d rows hold the exemption code", n)
	}
}

func TestIngest_KeysWithoutALookupKeepTheMappedValues(t *testing.T) {
	e := newIngest(t)
	e.lookup(e.entity, "fake", keyOne, Lookup{Kind: "tax_code", ERPKey: "OTHER", TaxCategory: sp("ZERO_VAT")})
	e.lookup(e.entity, "fake", keyOne, Lookup{Kind: "customer", ERPKey: "fake-co-1-cust-2", TIN: sp(tinX), State: sp("Lagos")})
	e.mustWork(docOne, "1")

	inv := e.draftOn(e.conn, docOne)
	want := e.mapped(docOne)
	l := e.draftLines(inv)[0]
	if str0(l.TaxCategory) != str0(want.LineItems[0].TaxCategory) || str0(l.HSN) != str0(want.LineItems[0].HSNCode) || str0(l.ISIC) != str0(want.LineItems[0].ISICCode) {
		t.Errorf("line codes = %s/%s/%s, want the mapped ones", str0(l.TaxCategory), str0(l.HSN), str0(l.ISIC))
	}
	if b := e.buyer(inv); str0(b.TIN) != str0(want.BuyerTIN) || str0(b.State) != str0(want.BuyerState) || str0(b.LGA) != str0(want.BuyerLGA) {
		t.Errorf("buyer = %s/%s/%s, want the mapped one", str0(b.TIN), str0(b.State), str0(b.LGA))
	}
}

func TestIngest_LookupsOfAnotherClientSystemOrKindAreIgnored(t *testing.T) {
	e := newIngest(t)
	y := e.addClient("ingest client Y")
	yConn := e.addConnection(y, keyOne)
	e.lookup(e.entity, "fake", keyOne, Lookup{Kind: "customer", ERPKey: custOne, TIN: sp(tinX)})
	e.lookup(y, "other", keyOne, Lookup{Kind: "customer", ERPKey: custOne, TIN: sp(tinY)})
	e.lookup(y, "fake", keyOne, Lookup{Kind: "item", ERPKey: custOne, HSNCode: sp("H")})

	e.workOn(e.conn, docOne, "1")
	e.workOn(yConn, docOne, "1")
	if b := e.buyer(e.draftOn(e.conn, docOne)); str0(b.TIN) != tinX {
		t.Errorf("X's buyer TIN = %s, want %s", str0(b.TIN), tinX)
	}
	yInv := e.draftOn(yConn, docOne)
	if b := e.buyer(yInv); str0(b.TIN) != "1234567897" {
		t.Errorf("Y's buyer TIN = %s, want the ERP's", str0(b.TIN))
	}
	if l := e.draftLines(yInv)[0]; str0(l.HSN) == "H" {
		t.Error("an item lookup applied to a customer key")
	}
}

func TestIngest_LookupsOfAnotherCompanyOfTheClientAreIgnored(t *testing.T) {
	e := newIngest(t)
	conn2 := e.addConnection(e.entity, "fake-co-2")
	// fake-co-2's customer id: unknown to fake-co-1, a real party on fake-co-2.
	const cust = "fake-co-2-cust-1"
	e.lookup(e.entity, "fake", "fake-co-1", Lookup{Kind: "customer", ERPKey: cust, TIN: sp(tinX)})
	e.onFetch = func(_ context.Context, ref DocRef) (Document, error) {
		number := "NA-" + ref.Scope
		return fakeDocument(ref.SourceDocID, number, cust, seedLine), nil
	}
	e.workOn(e.conn, "d1", "1")
	e.workOn(conn2, "d2", "1")

	if b := e.buyer(e.draftOn(e.conn, "d1")); str0(b.TIN) != tinX {
		t.Errorf("company 1's buyer TIN = %s, want the lookup's %s", str0(b.TIN), tinX)
	}
	if b := e.buyer(e.draftOn(conn2, "d2")); str0(b.TIN) != "3456789019" {
		t.Errorf("company 2's buyer TIN = %s, want the party's", str0(b.TIN))
	}
}

func TestIngest_TINEnteredOnceReachesTheNextInvoiceOfThatCustomer(t *testing.T) {
	e := newIngest(t)
	e.serveBy(map[string]Document{
		"doc-1": fakeDocument("doc-1", "N-1", custOne, seedLine),
		"doc-2": fakeDocument("doc-2", "N-2", custOne, seedLine),
		"doc-3": fakeDocument("doc-3", "N-3", "fake-co-1-cust-2", seedLine),
	})
	e.mustWork("doc-1", "1")
	inv1 := e.draftOn(e.conn, "doc-1")
	if b := e.buyer(inv1); str0(b.TIN) != "1234567897" {
		t.Fatalf("first TIN = %s, want the ERP's partial one", str0(b.TIN))
	}
	before := e.snapshot(inv1)

	admin := seedMember(t, e.super, e.tenant, "admin", "active", true)
	store := storeOver(t, e.app, nil, testFake(t))
	if _, err := store.SaveLookups(admin, e.entity, "fake", keyOne, []Lookup{{Kind: "customer", ERPKey: custOne, TIN: sp(tinX)}}); err != nil {
		t.Fatalf("SaveLookups: %v", err)
	}
	e.mustWork("doc-2", "1")
	e.mustWork("doc-3", "1")

	if b := e.buyer(e.draftOn(e.conn, "doc-2")); str0(b.TIN) != tinX {
		t.Errorf("doc-2 TIN = %s, want %s", str0(b.TIN), tinX)
	}
	if b := e.buyer(e.draftOn(e.conn, "doc-3")); str0(b.TIN) != "2345678904" {
		t.Errorf("doc-3 TIN = %s, want its own", str0(b.TIN))
	}
	if after := e.snapshot(inv1); after != before {
		t.Error("the earlier draft changed")
	}
}

func TestIngest_CustomerWithoutAPartyUsesItsLookup(t *testing.T) {
	e := newIngest(t)
	e.serveBy(map[string]Document{
		"doc-1": fakeDocument("doc-1", "N-1", "cust-none", seedLine),
		"doc-2": fakeDocument("doc-2", "N-2", "cust-none", seedLine),
	})
	e.mustWork("doc-1", "1")
	if b := e.buyer(e.draftOn(e.conn, "doc-1")); b.TIN != nil {
		t.Errorf("buyer TIN = %s, want nil without a party or lookup", str0(b.TIN))
	}
	e.lookup(e.entity, "fake", keyOne, Lookup{Kind: "customer", ERPKey: "cust-none", TIN: sp(tinX), State: sp("Lagos"), LGA: sp("Ikeja")})
	e.mustWork("doc-2", "1")
	if b := e.buyer(e.draftOn(e.conn, "doc-2")); str0(b.TIN) != tinX || str0(b.State) != "Lagos" || str0(b.LGA) != "Ikeja" {
		t.Errorf("buyer = %s/%s/%s, want the lookup's", str0(b.TIN), str0(b.State), str0(b.LGA))
	}
}

func TestIngest_NoCustomerIdReadsNoCustomerLookup(t *testing.T) {
	e := newIngest(t)
	e.lookup(e.entity, "fake", keyOne, Lookup{Kind: "customer", ERPKey: custOne, TIN: sp(tinX), State: sp("Lagos")})
	e.serveBy(map[string]Document{
		"doc-1": withBodyKey(t, fakeDocument("doc-1", "N-1", custOne, seedLine), "customer_id", nil),
		"doc-2": fakeDocument("doc-2", "N-2", "  ", seedLine),
	})
	for _, id := range []string{"doc-1", "doc-2"} {
		e.mustWork(id, "1")
		if b := e.buyer(e.draftOn(e.conn, id)); b.TIN != nil || b.State != nil {
			t.Errorf("%s buyer = %s/%s, want nothing: no customer id", id, str0(b.TIN), str0(b.State))
		}
	}
	if n := e.partyFetches.Load(); n != 0 {
		t.Errorf("FetchParty called %d times, want 0: no customer id to ask for", n)
	}
}

func TestIngest_LookupsDoNotMoveTheContentHash(t *testing.T) {
	e := newIngest(t)
	e.lookup(e.entity, "fake", keyOne, Lookup{Kind: "customer", ERPKey: custOne, TIN: sp(tinX)})
	e.lookup(e.entity, "fake", keyOne, Lookup{Kind: "tax_code", ERPKey: "VAT-STD", TaxCategory: sp("ZERO_VAT")})
	e.mustWork(docOne, "1")

	d := e.wantState(docOne, "drafted")
	pre := e.mappedUnder(testFake(t).DefaultMapping(), e.seededDoc(docOne))
	post := pre
	post.BuyerTIN = sp(tinX)
	if d.Hash == nil || *d.Hash != ContentHash(pre) {
		t.Errorf("content_hash = %v, want the pre-lookup hash", d.Hash)
	}
	if *d.Hash == ContentHash(post) {
		t.Error("content_hash equals the post-lookup hash")
	}
}

func TestIngest_InvalidEffectiveMappingFails(t *testing.T) {
	e := newIngest(t)
	e.override(e.entity, "fake", "line_quantity", "rows[].q")
	e.mustWork(docOne, "1")
	e.wantFailed(docOne, "no_mapping")
}

func TestIngest_MappingEditDoesNotMarkAnAcceptedInvoice(t *testing.T) {
	e := newIngest(t)
	inv := acceptedDraft(e)
	var first *string
	mustScan(t, e.super, "SELECT mapping_fingerprint FROM erp_source_documents WHERE connection_id = $1 AND source_doc_id = $2", []any{e.conn, docOne}, &first)
	if first == nil || *first != Fingerprint(testFake(t).DefaultMapping()) {
		t.Fatalf("first ingest wrote mapping_fingerprint %v, want the default mapping's", first)
	}
	e.override(e.entity, "fake", "line_description", "lines[].item_id")
	e.mustWork(docOne, "2")

	eff := testFake(t).DefaultMapping().Merge(Mapping{"line_description": "lines[].item_id"})
	d := e.wantState(docOne, "drafted")
	if d.Changed != nil {
		t.Errorf("changed_after_clearance_at = %v, want NULL: a mapping edit is not an ERP change", d.Changed)
	}
	e.wantNoAudit("erp.document.changed_after_clearance")
	var fp *string
	mustScan(t, e.super, "SELECT mapping_fingerprint FROM erp_source_documents WHERE id = $1", []any{d.ID}, &fp)
	if fp == nil || *fp != Fingerprint(eff) {
		t.Errorf("mapping_fingerprint = %v, want the effective mapping's", fp)
	}
	if want := ContentHash(e.mappedUnder(eff, e.seededDoc(docOne))); d.Hash == nil || *d.Hash != want {
		t.Errorf("content_hash = %v, want the new projection's", d.Hash)
	}
	if !d.Differs {
		t.Error("differs_from_erp = false, want true: the description now differs")
	}
	if got := e.draftLines(inv)[0].Desc; str0(got) != "Consulting services" {
		t.Errorf("the accepted invoice's line changed to %s", str0(got))
	}
}

func TestIngest_UnchangedMappingStillMarksAChangedAcceptedInvoice(t *testing.T) {
	e := newIngest(t)
	acceptedDraft(e)
	e.serve(pricier())
	e.mustWork(docOne, "2")
	if d := e.wantState(docOne, "drafted"); d.Changed == nil {
		t.Error("changed_after_clearance_at is NULL, want set")
	}
	e.wantAudit("erp.document.changed_after_clearance", changedKeys)
}

func TestIngest_NullFingerprintReadsAsTheDefaultMapping(t *testing.T) {
	e := newIngest(t)
	acceptedDraft(e)
	mustExec(t, e.super, "UPDATE erp_source_documents SET mapping_fingerprint = NULL WHERE connection_id = $1 AND source_doc_id = $2", e.conn, docOne)

	e.serve(pricier())
	e.mustWork(docOne, "2")
	if d := e.wantState(docOne, "drafted"); d.Changed == nil {
		t.Error("changed_after_clearance_at is NULL, want set: a NULL fingerprint is the default mapping's")
	}
	e.wantAudit("erp.document.changed_after_clearance", changedKeys)
}

func TestIngest_EditOfANonHashedTargetStillMarksAChangedAcceptedInvoice(t *testing.T) {
	e := newIngest(t)
	acceptedDraft(e)
	e.override(e.entity, "fake", "line_hsn_code", "lines[].isic_code")
	e.serve(pricier())
	e.mustWork(docOne, "2")
	if d := e.wantState(docOne, "drafted"); d.Changed == nil {
		t.Error("changed_after_clearance_at is NULL, want set: the edited target is outside the content hash")
	}
	e.wantAudit("erp.document.changed_after_clearance", changedKeys)
}

func TestIngest_EditOfAHashedTargetRebaselinesWithoutAMark(t *testing.T) {
	e := newIngest(t)
	acceptedDraft(e)
	e.override(e.entity, "fake", "line_description", "lines[].item_id")
	e.serve(pricier())
	e.mustWork(docOne, "2")
	if d := e.wantState(docOne, "drafted"); d.Changed != nil {
		t.Errorf("changed_after_clearance_at = %v, want NULL: a hashed mapping edit re-baselines", d.Changed)
	}
	e.wantNoAudit("erp.document.changed_after_clearance")
}

func TestIngest_LookupsReadHoldsOneConnection(t *testing.T) {
	e := newIngest(t)
	e.lookup(e.entity, "fake", keyOne, Lookup{Kind: "customer", ERPKey: custOne, TIN: sp(tinX)})
	one := poolWithOneConn(t)
	w := &IngestWorker{Pool: one, Registry: e.reg, Invoices: invoice.NewStore(one), Gate: e.val}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.workAs(ctx, w, e.args(docOne, "1", false), 1); err != nil {
		t.Fatalf("Work on a one-connection pool: %v", err)
	}
	if b := e.buyer(e.draftOn(e.conn, docOne)); !strings.Contains(str0(b.TIN), tinX) {
		t.Errorf("buyer TIN = %s, want the lookup's", str0(b.TIN))
	}
}
