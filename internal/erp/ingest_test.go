package erp

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

var errVendor = errors.New("vendor unavailable")

func (e *ingestEnv) differsAgainst(in invoice.CreateInput, invoiceID string) bool {
	e.t.Helper()
	var inv invoiceAmounts
	mustScan(e.t, e.super, "SELECT subtotal::text, vat::text, total::text FROM invoices WHERE id = $1", []any{invoiceID}, &inv.Subtotal, &inv.VAT, &inv.Total)
	rows, err := e.super.Query(context.Background(),
		"SELECT description, quantity::text, unit_price::text, line_total::text, line_tax::text FROM line_items WHERE invoice_id = $1 ORDER BY line_no", invoiceID)
	if err != nil {
		e.t.Fatalf("read lines: %v", err)
	}
	defer rows.Close()
	var lines []lineAmounts
	for rows.Next() {
		var l lineAmounts
		if err := rows.Scan(&l.Description, &l.Quantity, &l.UnitPrice, &l.LineTotal, &l.LineTax); err != nil {
			e.t.Fatalf("scan line: %v", err)
		}
		lines = append(lines, l)
	}
	return Differs(in, inv, lines)
}

func (e *ingestEnv) historyActors(invoiceID string) []string {
	e.t.Helper()
	rows, err := e.super.Query(context.Background(), "SELECT actor FROM invoice_status_history WHERE invoice_id = $1 ORDER BY changed_at, id", invoiceID)
	if err != nil {
		e.t.Fatalf("read history: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			e.t.Fatalf("scan history: %v", err)
		}
		out = append(out, a)
	}
	return out
}

func (e *ingestEnv) wantNoAudit(event string) {
	e.t.Helper()
	if n := len(e.audits(event)); n != 0 {
		e.t.Errorf("%s rows = %d, want 0", event, n)
	}
}

// --- a new document becomes a draft ---

func TestIngest_NewDocumentBecomesADraftThroughTheSharedPath(t *testing.T) {
	e := newIngest(t)
	e.val.blocking = true
	e.mustWork(docOne, "1")

	if n := e.invoices(); n != 1 {
		t.Fatalf("invoices = %d, want 1", n)
	}
	id, status, conn := e.invoiceByNumber(e.entity, numOne)
	if status != "draft" {
		t.Errorf("invoice status = %q, want draft (the stub validator blocks it)", status)
	}
	if conn == nil || *conn != e.conn {
		t.Errorf("invoices.erp_connection_id = %v, want %s", conn, e.conn)
	}
	d := e.wantState(docOne, "drafted")
	if d.InvoiceID == nil || *d.InvoiceID != id {
		t.Errorf("row invoice_id = %v, want %s", d.InvoiceID, id)
	}
	if d.Revision != "1" || d.Entity != e.entity || d.Claimed != nil || d.Failure != nil {
		t.Errorf("row revision/entity/claim/failure = %q/%q/%v/%v, want 1/%s/NULL/NULL", d.Revision, d.Entity, d.Claimed, d.Failure, e.entity)
	}
	if d.Hash == nil || !hex64.MatchString(*d.Hash) {
		t.Errorf("content_hash = %v, want 64 hex characters", d.Hash)
	}

	p := e.wantAudit("erp.document.pulled", pulledKeys)
	if p.Payload["invoice_number"] != numOne || p.Payload["invoice_id"] != id ||
		p.Payload["source_doc_id"] != docOne || p.Payload["source_revision"] != "1" {
		t.Errorf("pulled payload = %v, want invoice %s (%s) from %s rev 1", p.Payload, id, numOne, docOne)
	}
	e.wantNoAudit("erp.document.skipped_duplicate")

	if got := e.historyActors(id); len(got) != 1 || got[0] != "system" {
		t.Errorf("history actors = %v, want [system]", got)
	}
	created := e.audits("invoice.created")
	if len(created) != 1 || created[0].Actor != "system" {
		t.Errorf("invoice.created rows = %+v, want one by system", created)
	}
	if got := e.val.called(); len(got) != 1 || got[0] != id {
		t.Errorf("validator saw %v, want exactly [%s]", got, id)
	}
}

func TestIngest_CleanDocumentLandsValidated(t *testing.T) {
	e := newIngest(t)
	e.mustWork(docOne, "1")

	id, status, _ := e.invoiceByNumber(e.entity, numOne)
	if status != "validated" {
		t.Errorf("invoice status = %q, want validated", status)
	}
	e.wantState(docOne, "drafted")
	if n := countOf(t, e.super, "SELECT count(*) FROM submission_jobs WHERE tenant_id = $1", e.tenant); n != 0 {
		t.Errorf("submission_jobs = %d, want 0: an ERP invoice is never submitted", n)
	}
	if got := e.val.called(); len(got) != 1 || got[0] != id {
		t.Errorf("validator saw %v, want [%s]", got, id)
	}
}

func TestIngest_TenantComesFromTheArgsNotTheContext(t *testing.T) {
	a := newIngest(t)
	b := newIngestOn(t, a.super, a.app, keyOne)
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: a.tenant})

	if err := b.workAs(ctx, b.worker, b.args(docOne, "1", false), 1); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if n := b.invoices(); n != 1 {
		t.Fatalf("tenant B invoices = %d, want 1", n)
	}
	b.wantState(docOne, "drafted")
	if n := a.invoices(); n != 0 {
		t.Errorf("tenant A invoices = %d, want 0", n)
	}
	if n := countOf(t, a.super, "SELECT count(*) FROM erp_source_documents WHERE tenant_id = $1", a.tenant); n != 0 {
		t.Errorf("tenant A documents = %d, want 0", n)
	}
	if n := countOf(t, a.super, "SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event LIKE 'erp.document.%'", a.tenant); n != 0 {
		t.Errorf("tenant A erp.document audit rows = %d, want 0", n)
	}
}

func TestIngest_FailedDocumentWithANewRevisionBecomesADraft(t *testing.T) {
	e := newIngest(t)
	e.plantDocument(plantDoc{
		docID: docOne, state: "failed", failure: sp("unmappable"),
		differs: true, changed: tp(time.Now()), deleted: tp(time.Now()),
	})
	e.mustWork(docOne, "2")

	d := e.wantState(docOne, "drafted")
	if d.InvoiceID == nil || e.invoices() != 1 {
		t.Errorf("invoice_id = %v, invoices = %d, want a new invoice", d.InvoiceID, e.invoices())
	}
	if d.Revision != "2" || d.Failure != nil || d.Deleted != nil || d.Changed != nil || d.Differs {
		t.Errorf("row revision/failure/deleted/changed/differs = %q/%v/%v/%v/%v, want 2 and every stale mark cleared",
			d.Revision, d.Failure, d.Deleted, d.Changed, d.Differs)
	}
}

func TestIngest_UnvoidedDocumentBecomesADraft(t *testing.T) {
	e := newIngest(t)
	e.plantDocument(plantDoc{docID: docOne, state: "deleted", deleted: tp(time.Now())})
	e.mustWork(docOne, "2")

	d := e.wantState(docOne, "drafted")
	if d.Deleted != nil {
		t.Errorf("deleted_in_erp_at = %v, want NULL once the ref is live", d.Deleted)
	}
	if d.InvoiceID == nil || e.invoices() != 1 {
		t.Errorf("invoice_id = %v, invoices = %d, want one created invoice", d.InvoiceID, e.invoices())
	}
}

// --- a duplicate number links ---

func TestIngest_DuplicateNumberLinksAndCreatesNothing(t *testing.T) {
	e := newIngest(t)
	inv := e.plantInvoice(plantInv{number: numOne})
	before := e.snapshot(inv)
	e.mustWork(docOne, "1")

	d := e.wantState(docOne, "linked")
	if d.InvoiceID == nil || *d.InvoiceID != inv {
		t.Errorf("row invoice_id = %v, want %s", d.InvoiceID, inv)
	}
	e.wantFingerprint(docOne)
	if d.Differs {
		t.Error("differs_from_erp = true for identical amounts and lines")
	}
	if n := e.invoices(); n != 1 {
		t.Errorf("invoices = %d, want 1", n)
	}
	if after := e.snapshot(inv); after != before {
		t.Errorf("the existing invoice, lines or history changed:\nbefore %s\nafter  %s", before, after)
	}
	s := e.wantAudit("erp.document.skipped_duplicate", dupKeys)
	if s.Payload["linked"] != true || s.Payload["differs"] != false || s.Payload["invoice_number"] != numOne || s.Payload["invoice_id"] != inv {
		t.Errorf("skipped_duplicate payload = %v, want linked true, differs false, invoice %s (%s)", s.Payload, inv, numOne)
	}
	e.wantNoAudit("erp.document.pulled")
}

func TestIngest_DuplicateWithDifferentAmountsFlagsDiffers(t *testing.T) {
	e := newIngest(t)
	inv := e.plantInvoice(plantInv{number: numOne, subtotal: "5000.00", vat: "0.00", total: "5000.00"})
	before := e.snapshot(inv)
	e.mustWork(docOne, "1")

	d := e.wantState(docOne, "linked")
	if !d.Differs {
		t.Error("differs_from_erp = false for a different total")
	}
	if after := e.snapshot(inv); after != before {
		t.Errorf("the existing invoice changed:\nbefore %s\nafter  %s", before, after)
	}
	if s := e.wantAudit("erp.document.skipped_duplicate", dupKeys); s.Payload["differs"] != true || s.Payload["linked"] != true {
		t.Errorf("skipped_duplicate payload = %v, want linked and differs true", s.Payload)
	}
}

func TestIngest_DuplicateWithDifferentLinesFlagsDiffers(t *testing.T) {
	e := newIngest(t)
	inv := e.plantInvoice(plantInv{number: numOne, lineDesc: "A different service"})
	e.mustWork(docOne, "1")

	d := e.wantState(docOne, "linked")
	if !d.Differs {
		t.Error("differs_from_erp = false for a different line description with equal totals")
	}
	if d.InvoiceID == nil || *d.InvoiceID != inv {
		t.Errorf("row invoice_id = %v, want %s", d.InvoiceID, inv)
	}
}

func TestIngest_RacingCreateLinksInstead(t *testing.T) {
	e := newIngest(t)
	store := invoice.NewStore(e.app)
	var racer string
	e.worker = e.workerWith(createFn(func(ctx context.Context, in invoice.CreateInput) (invoice.Invoice, error) {
		racer = e.plantInvoice(plantInv{number: in.InvoiceNumber})
		return store.Create(ctx, in)
	}))
	e.mustWork(docOne, "1")

	if racer == "" {
		t.Fatal("the creator never ran: the ingest did not try to create")
	}
	if n := e.invoices(); n != 1 {
		t.Errorf("invoices = %d, want 1 (the racer's)", n)
	}
	d := e.wantState(docOne, "linked")
	if d.InvoiceID == nil || *d.InvoiceID != racer {
		t.Errorf("row invoice_id = %v, want the racer's %s", d.InvoiceID, racer)
	}
	if d.Claimed != nil {
		t.Errorf("claimed_number = %q, want cleared", *d.Claimed)
	}
	if s := e.wantAudit("erp.document.skipped_duplicate", dupKeys); s.Payload["linked"] != true {
		t.Errorf("skipped_duplicate payload = %v, want linked true", s.Payload)
	}
	e.wantNoAudit("erp.document.pulled")
}

// --- one invoice, one document per connection ---

func TestIngest_SecondDocumentOnALinkedInvoiceIsHeld(t *testing.T) {
	e := newIngest(t)
	inv := e.plantInvoice(plantInv{number: numOne})
	e.plantDocument(plantDoc{docID: "doc-A", state: "linked", invoiceID: &inv})
	before := e.snapshot(inv)
	e.mustWork(docOne, "1")

	d := e.wantState(docOne, "held")
	if d.InvoiceID != nil {
		t.Errorf("held row invoice_id = %s, want NULL", *d.InvoiceID)
	}
	if n := e.invoices(); n != 1 {
		t.Errorf("invoices = %d, want 1", n)
	}
	if d.Revision != "1" || d.Hash == nil {
		t.Errorf("held row revision/hash = %q/%v, want the revision and hash it read", d.Revision, d.Hash)
	}
	e.wantFingerprint(docOne)
	if after := e.snapshot(inv); after != before {
		t.Error("the linked invoice changed")
	}
	s := e.wantAudit("erp.document.skipped_duplicate", dupKeys)
	if s.Payload["linked"] != false || s.Payload["invoice_id"] != nil {
		t.Errorf("skipped_duplicate payload = %v, want linked false and invoice_id null", s.Payload)
	}
	if a := e.doc("doc-A"); a.State != "linked" {
		t.Errorf("doc-A state = %q, want it left linked", a.State)
	}
}

func TestIngest_SameNumberOnAnotherConnectionLinks(t *testing.T) {
	e := newIngest(t)
	c2 := e.addConnection(e.entity, "fake-co-2")
	inv := e.plantInvoice(plantInv{number: numOne})
	e.plantDocument(plantDoc{conn: c2, docID: "other-doc", state: "linked", invoiceID: &inv})
	e.mustWork(docOne, "1")

	d := e.wantState(docOne, "linked")
	if d.InvoiceID == nil || *d.InvoiceID != inv {
		t.Errorf("row invoice_id = %v, want %s: the unique key is per connection", d.InvoiceID, inv)
	}
	if n := e.invoices(); n != 1 {
		t.Errorf("invoices = %d, want 1", n)
	}
}

func TestIngest_HeldDocumentDraftsOnceItsNumberIsFree(t *testing.T) {
	e := newIngest(t)
	const docTwo, numTwo = "fake-co-1-inv-2", "FK1-0002"
	inv := e.plantInvoice(plantInv{number: numOne})
	e.plantDocument(plantDoc{docID: "doc-A", state: "linked", invoiceID: &inv})
	e.plantDocument(plantDoc{docID: docTwo, state: "held", differs: true})
	e.mustWork(docTwo, "2")

	d := e.wantState(docTwo, "drafted")
	id, _, _ := e.invoiceByNumber(e.entity, numTwo)
	if d.InvoiceID == nil || *d.InvoiceID != id {
		t.Errorf("row invoice_id = %v, want the new invoice %s", d.InvoiceID, id)
	}
	if n := e.invoices(); n != 2 {
		t.Errorf("invoices = %d, want 2", n)
	}
	if d.Revision != "2" || d.Failure != nil || d.Differs {
		t.Errorf("row revision/failure/differs = %q/%v/%v, want 2/NULL/false", d.Revision, d.Failure, d.Differs)
	}
}

// --- retries ---

func TestIngest_RetryAfterACommittedCreateAdoptsItsOwnInvoice(t *testing.T) {
	e := newIngest(t)
	inv := e.plantInvoice(plantInv{number: numOne, conn: &e.conn})
	e.plantDocument(plantDoc{docID: docOne, state: "pending", claimed: sp(numOne)})
	e.mustWork(docOne, "1")

	d := e.wantState(docOne, "drafted")
	if d.InvoiceID == nil || *d.InvoiceID != inv {
		t.Errorf("row invoice_id = %v, want the adopted %s", d.InvoiceID, inv)
	}
	if d.Claimed != nil {
		t.Errorf("claimed_number = %q, want cleared", *d.Claimed)
	}
	if n := e.invoices(); n != 1 {
		t.Errorf("invoices = %d, want 1: the retry must not create a second", n)
	}
	if p := e.wantAudit("erp.document.pulled", pulledKeys); p.Payload["invoice_id"] != inv {
		t.Errorf("pulled payload = %v, want invoice %s", p.Payload, inv)
	}
	if got := e.val.called(); len(got) != 1 || got[0] != inv {
		t.Errorf("validator saw %v, want the adopted draft [%s]", got, inv)
	}
	e.wantNoAudit("erp.document.skipped_duplicate")
}

func TestIngest_ImportedInvoiceWithTheNumberIsLinkedNotAdopted(t *testing.T) {
	cases := []struct {
		name  string
		plant func(e *ingestEnv) string
	}{
		{"an imported invoice", func(e *ingestEnv) string {
			return e.plantInvoice(plantInv{number: numOne})
		}},
		{"this connection's invoice with no claim", func(e *ingestEnv) string {
			return e.plantInvoice(plantInv{number: numOne, conn: &e.conn})
		}},
		{"another connection's invoice under this row's live claim", func(e *ingestEnv) string {
			c2 := e.addConnection(e.entity, "fake-co-2")
			e.plantDocument(plantDoc{docID: docOne, state: "pending", claimed: sp(numOne)})
			return e.plantInvoice(plantInv{number: numOne, conn: &c2})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newIngest(t)
			inv := tc.plant(e)
			e.mustWork(docOne, "1")

			d := e.wantState(docOne, "linked")
			if d.InvoiceID == nil || *d.InvoiceID != inv {
				t.Errorf("row invoice_id = %v, want %s", d.InvoiceID, inv)
			}
			if n := e.invoices(); n != 1 {
				t.Errorf("invoices = %d, want 1", n)
			}
			e.wantAudit("erp.document.skipped_duplicate", dupKeys)
			e.wantNoAudit("erp.document.pulled")
		})
	}
}

func TestIngest_SameRevisionAgainIsANoOp(t *testing.T) {
	e := newIngest(t)
	e.mustWork(docOne, "1")
	first := e.docJSON(docOne)
	if first == "none" {
		t.Fatal("the first Work wrote no row")
	}
	e.mustWork(docOne, "1")

	if got := e.docJSON(docOne); got != first {
		t.Errorf("the second Work changed the row:\nbefore %s\nafter  %s", first, got)
	}
	if n := e.invoices(); n != 1 {
		t.Errorf("invoices = %d, want 1", n)
	}
	e.wantAudit("erp.document.pulled", pulledKeys)
	if n := e.fetches.Load(); n != 1 {
		t.Errorf("Fetch called %d times, want 1: a taken revision needs no fetch", n)
	}
}

// --- changed after acceptance ---

// acceptedDraft takes docOne through the shared path and marks its invoice accepted.
func acceptedDraft(e *ingestEnv) string {
	e.t.Helper()
	e.mustWork(docOne, "1")
	inv := *e.wantState(docOne, "drafted").InvoiceID
	e.accept(inv)
	return inv
}

func pricier() Document {
	return fakeDocument(docOne, numOne, custOne, fakeLine{"Consulting services", "1", "200000.00", "7.5"})
}

func TestIngest_ChangedAfterAcceptanceMarksAndNeverTouchesTheInvoice(t *testing.T) {
	e := newIngest(t)
	inv := acceptedDraft(e)
	before := e.snapshot(inv)
	calls := e.val.callCount()

	doc := pricier()
	e.serve(doc)
	e.mustWork(docOne, "2")

	if after := e.snapshot(inv); after != before {
		t.Errorf("the accepted invoice, its lines, history or submission jobs changed:\nbefore %s\nafter  %s", before, after)
	}
	d := e.wantState(docOne, "drafted")
	if d.Changed == nil {
		t.Error("changed_after_clearance_at is NULL, want it set")
	}
	if d.Revision != "2" || d.Hash == nil || *d.Hash != ContentHash(e.mappedFrom(doc)) {
		t.Errorf("row revision/hash = %q/%v, want 2 and the new content's hash", d.Revision, d.Hash)
	}
	c := e.wantAudit("erp.document.changed_after_clearance", changedKeys)
	if c.Payload["invoice_number"] != numOne || c.Payload["deleted"] != false || c.Payload["invoice_id"] != inv || c.Payload["source_revision"] != "2" {
		t.Errorf("changed payload = %v, want %s, deleted false, invoice %s, revision 2", c.Payload, numOne, inv)
	}
	if got := e.val.callCount(); got != calls {
		t.Errorf("validator called %d more time(s) on an accepted invoice", got-calls)
	}
	if n := e.invoices(); n != 1 {
		t.Errorf("invoices = %d, want 1", n)
	}
}

func TestIngest_ChangedAfterAcceptanceOnALinkedInvoice(t *testing.T) {
	e := newIngest(t)
	inv := e.plantInvoice(plantInv{number: numOne, status: "accepted"})
	e.accept(inv)
	e.mustWork(docOne, "1")
	e.wantState(docOne, "linked")
	before := e.snapshot(inv)

	doc := pricier()
	e.serve(doc)
	e.mustWork(docOne, "2")

	if after := e.snapshot(inv); after != before {
		t.Errorf("the accepted invoice changed:\nbefore %s\nafter  %s", before, after)
	}
	d := e.wantState(docOne, "linked")
	if d.Changed == nil || d.Revision != "2" {
		t.Errorf("row changed/revision = %v/%q, want it set and 2", d.Changed, d.Revision)
	}
	if c := e.wantAudit("erp.document.changed_after_clearance", changedKeys); c.Payload["invoice_number"] != numOne || c.Payload["deleted"] != false {
		t.Errorf("changed payload = %v", c.Payload)
	}
}

func TestIngest_NewRevisionSameContentOnAnAcceptedInvoiceRecordsTheRevisionOnly(t *testing.T) {
	e := newIngest(t)
	inv := acceptedDraft(e)
	before := e.snapshot(inv)
	hash := *e.doc(docOne).Hash
	e.mustWork(docOne, "2")

	d := e.wantState(docOne, "drafted")
	if d.Revision != "2" {
		t.Errorf("source_revision = %q, want 2", d.Revision)
	}
	if d.Changed != nil {
		t.Errorf("changed_after_clearance_at = %v, want NULL for equal content", d.Changed)
	}
	if d.Hash == nil || *d.Hash != hash {
		t.Errorf("content_hash = %v, want it unchanged %s", d.Hash, hash)
	}
	e.wantNoAudit("erp.document.changed_after_clearance")
	if after := e.snapshot(inv); after != before {
		t.Error("the accepted invoice changed")
	}
	if n := e.fetches.Load(); n != 2 {
		t.Errorf("Fetch called %d times, want 2: a new revision is read", n)
	}
}

func TestIngest_SameContentRevisionOfALinkedAcceptedInvoiceIsQuiet(t *testing.T) {
	e := newIngest(t)
	inv := e.plantInvoice(plantInv{number: numOne, status: "accepted"})
	e.mustWork(docOne, "1")
	e.wantState(docOne, "linked")
	e.mustWork(docOne, "2")

	d := e.wantState(docOne, "linked")
	if d.Revision != "2" || d.Changed != nil {
		t.Errorf("row revision/changed = %q/%v, want 2/NULL", d.Revision, d.Changed)
	}
	if d.InvoiceID == nil || *d.InvoiceID != inv {
		t.Errorf("row invoice_id = %v, want %s", d.InvoiceID, inv)
	}
	e.wantNoAudit("erp.document.changed_after_clearance")
	e.wantAudit("erp.document.skipped_duplicate", dupKeys)
}

func TestIngest_HashVersionChangeNeverMarksAnAcceptedInvoice(t *testing.T) {
	e := newIngest(t)
	inv := acceptedDraft(e)
	row := e.doc(docOne)
	older := HashVersion + 1 // any version but the current one
	mustExec(t, e.super, "UPDATE erp_source_documents SET hash_version = $2, content_hash = $3 WHERE id = $1",
		row.ID, older, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	before := e.snapshot(inv)

	doc := pricier()
	e.serve(doc)
	mapped := e.mappedFrom(doc)
	e.mustWork(docOne, "2")

	d := e.wantState(docOne, "drafted")
	if d.Changed != nil {
		t.Errorf("changed_after_clearance_at = %v, want NULL: a projection change is not a vendor change", d.Changed)
	}
	if d.Hash == nil || *d.Hash != ContentHash(mapped) {
		t.Errorf("content_hash = %v, want ContentHash(mapped)", d.Hash)
	}
	if d.HashVersion == nil || int(*d.HashVersion) != HashVersion {
		t.Errorf("hash_version = %v, want %d", d.HashVersion, HashVersion)
	}
	if want := e.differsAgainst(mapped, inv); !want || d.Differs != want {
		t.Errorf("differs_from_erp = %v, want true (Differs = %v)", d.Differs, want)
	}
	e.wantNoAudit("erp.document.changed_after_clearance")
	if after := e.snapshot(inv); after != before {
		t.Error("the accepted invoice changed")
	}
}

// --- a draft is never overwritten ---

func TestIngest_NewRevisionOfADraftNeverOverwritesIt(t *testing.T) {
	e := newIngest(t)
	e.val.blocking = true
	e.mustWork(docOne, "1")
	inv := *e.wantState(docOne, "drafted").InvoiceID
	if st := e.invoiceStatus(inv); st != "draft" {
		t.Fatalf("setup: invoice status = %q, want draft", st)
	}
	before := e.snapshot(inv)
	calls := e.val.callCount()

	doc := pricier()
	e.serve(doc)
	e.mustWork(docOne, "2")

	if after := e.snapshot(inv); after != before {
		t.Errorf("the draft changed:\nbefore %s\nafter  %s", before, after)
	}
	d := e.wantState(docOne, "drafted")
	if d.Revision != "2" || d.Hash == nil || *d.Hash != ContentHash(e.mappedFrom(doc)) {
		t.Errorf("row revision/hash = %q/%v, want 2 and the new hash", d.Revision, d.Hash)
	}
	if !d.Differs {
		t.Error("differs_from_erp = false, want true for a new total")
	}
	if n := e.erpAudits(); n != 1 {
		t.Errorf("erp.document audit rows = %d, want only the first pull", n)
	}
	if got := e.val.callCount(); got != calls {
		t.Errorf("validator called on a draft that already carries a verdict")
	}
}

// --- deleted refs ---

func TestIngest_DeletedRefOnADraftMarksTheDocumentOnly(t *testing.T) {
	e := newIngest(t)
	e.val.blocking = true
	e.mustWork(docOne, "1")
	inv := *e.wantState(docOne, "drafted").InvoiceID
	before := e.snapshot(inv)
	fetched := e.fetches.Load()

	e.mustWorkDeleted(docOne, "1")

	d := e.wantState(docOne, "drafted")
	if d.Deleted == nil {
		t.Error("deleted_in_erp_at is NULL, want it set")
	}
	if d.Changed != nil {
		t.Errorf("changed_after_clearance_at = %v, want NULL for a draft", d.Changed)
	}
	if after := e.snapshot(inv); after != before {
		t.Error("the draft changed")
	}
	if n := e.invoices(); n != 1 {
		t.Errorf("invoices = %d, want 1: no invoice is deleted", n)
	}
	if got := e.fetches.Load(); got != fetched {
		t.Errorf("Fetch called for a deleted ref")
	}
	if n := e.erpAudits(); n != 1 {
		t.Errorf("erp.document audit rows = %d, want only the first pull", n)
	}
}

func TestIngest_DeletedRefOnAnAcceptedInvoiceIsHighlighted(t *testing.T) {
	e := newIngest(t)
	inv := acceptedDraft(e)
	before := e.snapshot(inv)
	e.mustWorkDeleted(docOne, "1")

	d := e.wantState(docOne, "drafted")
	if d.Deleted == nil || d.Changed == nil {
		t.Errorf("deleted/changed = %v/%v, want both set", d.Deleted, d.Changed)
	}
	if after := e.snapshot(inv); after != before {
		t.Error("the accepted invoice changed")
	}
	c := e.wantAudit("erp.document.changed_after_clearance", changedKeys)
	if c.Payload["deleted"] != true || c.Payload["invoice_number"] != numOne {
		t.Errorf("changed payload = %v, want deleted true for %s", c.Payload, numOne)
	}
}

func TestIngest_DeletedRefNeverTakenRecordsADeletedDocument(t *testing.T) {
	super, app := storePools(t)
	e := newIngestOn(t, super, app, "fake-co-3")
	e.onFetch = func(context.Context, DocRef) (Document, error) {
		return Document{}, errors.New("a deleted ref must not be fetched")
	}
	const voided = "fake-co-3-inv-6"
	e.mustWorkDeleted(voided, "1")

	d := e.wantState(voided, "deleted")
	if d.Deleted == nil {
		t.Error("deleted_in_erp_at is NULL, want it set")
	}
	if d.InvoiceID != nil || e.invoices() != 0 {
		t.Errorf("invoice_id = %v, invoices = %d, want none", d.InvoiceID, e.invoices())
	}
	if d.Revision != "1" {
		t.Errorf("source_revision = %q, want 1", d.Revision)
	}
	if n := e.fetches.Load(); n != 0 {
		t.Errorf("Fetch called %d times, want 0", n)
	}
	if n := e.erpAudits(); n != 0 {
		t.Errorf("erp.document audit rows = %d, want 0", n)
	}
}

// --- failures that end the document ---

func (e *ingestEnv) wantFailed(docID, failure string) {
	e.t.Helper()
	d := e.wantState(docID, "failed")
	if d.Failure == nil || *d.Failure != failure {
		e.t.Errorf("failure = %v, want %q", d.Failure, failure)
	}
	if d.InvoiceID != nil || e.invoices() != 0 {
		e.t.Errorf("invoice_id = %v, invoices = %d, want none", d.InvoiceID, e.invoices())
	}
	if d.Claimed != nil {
		e.t.Errorf("claimed_number = %q, want cleared", *d.Claimed)
	}
	if n := e.erpAudits(); n != 0 {
		e.t.Errorf("erp.document audit rows = %d, want 0", n)
	}
}

func TestIngest_UnmappableDocumentFails(t *testing.T) {
	e := newIngest(t)
	e.serve(Document{Ref: DocRef{SourceDocID: docOne, SourceRevision: "1"}, ContentType: jsonType, Body: []byte("{")})
	if err := e.work(docOne, "1"); err != nil {
		t.Fatalf("Work: %v, want nil: an unmappable body is the document's end, not a retry", err)
	}
	e.wantFailed(docOne, "unmappable")
}

// renamedConnector answers to another system name with the same behaviour; own replaces the default mapping.
type renamedConnector struct {
	Connector
	name string
	own  bool
	m    Mapping
}

func (r renamedConnector) System() string { return r.name }

func (r renamedConnector) DefaultMapping() Mapping {
	if r.own {
		return r.m
	}
	return r.Connector.DefaultMapping()
}

func TestIngest_SystemWithoutAMappingFails(t *testing.T) {
	e := newIngest(t)
	var grant, conn string
	mustScan(t, e.super, "INSERT INTO erp_grants (tenant_id, system, kind) VALUES ($1, 'nomap', 'api_key') RETURNING id::text", []any{e.tenant}, &grant)
	mustScan(t, e.super,
		"INSERT INTO erp_connections (tenant_id, entity_id, system, external_key, status, grant_id) VALUES ($1, $2, 'nomap', $3, 'active', $4) RETURNING id::text",
		[]any{e.tenant, e.entity, keyOne, grant}, &conn)
	reg, err := NewRegistry(renamedConnector{Connector: e.script, name: "nomap", own: true})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	e.reg, e.conn = reg, conn
	e.worker = e.workerWith(invoiceStore(e))

	if err := e.work(docOne, "1"); err != nil {
		t.Fatalf("Work: %v, want nil", err)
	}
	e.wantFailed(docOne, "no_mapping")
}

func TestIngest_InvalidDefaultMappingFails(t *testing.T) {
	e := newIngest(t)
	var grant, conn string
	mustScan(t, e.super, "INSERT INTO erp_grants (tenant_id, system, kind) VALUES ($1, 'badmap', 'api_key') RETURNING id::text", []any{e.tenant}, &grant)
	mustScan(t, e.super,
		"INSERT INTO erp_connections (tenant_id, entity_id, system, external_key, status, grant_id) VALUES ($1, $2, 'badmap', $3, 'active', $4) RETURNING id::text",
		[]any{e.tenant, e.entity, keyOne, grant}, &conn)
	bad := testFake(t).DefaultMapping()
	bad["line_quantity"] = "qty"
	reg, err := NewRegistry(renamedConnector{Connector: e.script, name: "badmap", own: true, m: bad})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	e.reg, e.conn = reg, conn
	e.worker = e.workerWith(invoiceStore(e))

	if err := e.work(docOne, "1"); err != nil {
		t.Fatalf("Work: %v, want nil", err)
	}
	e.wantFailed(docOne, "no_mapping")
	if n := e.invoices(); n != 0 {
		t.Errorf("invoices = %d, want 0", n)
	}
	if n := e.erpAudits(); n != 0 {
		t.Errorf("erp audits = %d, want 0", n)
	}
}

func TestIngest_FetchNotFoundFails(t *testing.T) {
	e := newIngest(t)
	e.onFetch = func(context.Context, DocRef) (Document, error) {
		return Document{}, fmt.Errorf("vendor: %w", ErrNotFound)
	}
	if err := e.work(docOne, "1"); err != nil {
		t.Fatalf("Work: %v, want nil", err)
	}
	e.wantFailed(docOne, "not_found")
}

func TestIngest_CreateRefusalFails(t *testing.T) {
	e := newIngest(t)
	e.worker = e.workerWith(createFn(func(context.Context, invoice.CreateInput) (invoice.Invoice, error) {
		return invoice.Invoice{}, invoice.ErrValidation
	}))
	if err := e.work(docOne, "1"); err != nil {
		t.Fatalf("Work: %v, want nil: a domain refusal ends the document", err)
	}
	e.wantFailed(docOne, "refused")
}

// --- sync stops, errors retry ---

func TestIngest_SyncStoppedDoesNothing(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, e *ingestEnv, a *IngestArgs)
		exist bool // the connection row exists
	}{
		{"connection missing", func(t *testing.T, e *ingestEnv, a *IngestArgs) { a.ConnectionID = uuid.NewString() }, false},
		{"disconnected", func(t *testing.T, e *ingestEnv, _ *IngestArgs) {
			mustExec(t, e.super, "UPDATE erp_connections SET status = 'disconnected', grant_id = NULL WHERE id = $1", e.conn)
		}, true},
		{"reconsent", func(t *testing.T, e *ingestEnv, _ *IngestArgs) {
			mustExec(t, e.super, "UPDATE erp_connections SET status = 'reconsent' WHERE id = $1", e.conn)
		}, true},
		{"system not in the registry", func(t *testing.T, e *ingestEnv, _ *IngestArgs) {
			reg, err := NewRegistry(renamedConnector{Connector: e.script, name: "other"})
			if err != nil {
				t.Fatalf("registry: %v", err)
			}
			e.reg = reg
			e.worker = e.workerWith(invoiceStore(e))
		}, true},
		{"workspace scheduled for deletion", func(t *testing.T, e *ingestEnv, _ *IngestArgs) {
			lockWorkspace(t, e.super, e.tenant, uuid.NewString())
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newIngest(t)
			mustExec(t, e.super, "UPDATE erp_connections SET cursor = '\\x0102' WHERE id = $1", e.conn)
			a := e.args(docOne, "1", false)
			tc.setup(t, e, &a)

			if err := e.workAs(context.Background(), e.worker, a, 1); err != nil {
				t.Fatalf("Work: %v, want nil: sync stopped is not an error", err)
			}
			e.noDoc(docOne)
			if n := e.invoices(); n != 0 {
				t.Errorf("invoices = %d, want 0", n)
			}
			if n := e.fetches.Load(); n != 0 {
				t.Errorf("Fetch called %d times, want 0", n)
			}
			if !tc.exist {
				return
			}
			var cursor []byte
			var epoch int64
			mustScan(t, e.super, "SELECT cursor, ingest_epoch FROM erp_connections WHERE id = $1", []any{e.conn}, &cursor, &epoch)
			if cursor != nil || epoch != 1 {
				t.Errorf("cursor/ingest_epoch = %v/%d, want NULL/1: the skipped revision must not stay spent", cursor, epoch)
			}
		})
	}
}

// A job whose args name another tenant's connection sees no row under RLS: it must fetch, write and
// bump nothing, in the other tenant and in its own.
func TestIngest_AnotherTenantsConnectionIsNeverTouched(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprintf("deleted=%v", deleted), func(t *testing.T) {
			a := newIngest(t)
			b := newIngestOn(t, a.super, a.app, keyOne)
			mustExec(t, b.super, "UPDATE erp_connections SET cursor = '\\x0102' WHERE id = $1", b.conn)
			args := a.args(docOne, "1", deleted)
			args.ConnectionID = b.conn

			if err := a.workAs(context.Background(), a.worker, args, 1); err != nil {
				t.Fatalf("Work: %v, want nil: the connection is not visible to the args' tenant", err)
			}
			if n := countOf(t, a.super, "SELECT count(*) FROM erp_source_documents WHERE connection_id = $1", b.conn); n != 0 {
				t.Errorf("documents on B's connection = %d, want 0", n)
			}
			if a.invoices() != 0 || b.invoices() != 0 {
				t.Errorf("invoices in A/B = %d/%d, want 0/0", a.invoices(), b.invoices())
			}
			if n := a.fetches.Load(); n != 0 {
				t.Errorf("Fetch called %d times, want 0", n)
			}
			var cursor []byte
			var epoch int64
			mustScan(t, b.super, "SELECT cursor, ingest_epoch FROM erp_connections WHERE id = $1", []any{b.conn}, &cursor, &epoch)
			if string(cursor) != "\x01\x02" || epoch != 0 {
				t.Errorf("B's cursor/ingest_epoch = %x/%d, want 0102/0: A's skipped ingest must not reopen B's connection", cursor, epoch)
			}
		})
	}
}

// A header amount that differs while every line is equal must still flag differs, one amount at a time.
func TestIngest_DuplicateWithOnlyAHeaderAmountDifferentFlagsDiffers(t *testing.T) {
	for _, col := range []string{"subtotal", "vat", "total"} {
		t.Run(col, func(t *testing.T) {
			e := newIngest(t)
			inv := e.plantInvoice(plantInv{number: numOne})
			mustExec(t, e.super, "UPDATE invoices SET "+col+" = 1.00 WHERE id = $1", inv)
			e.mustWork(docOne, "1")
			if d := e.wantState(docOne, "linked"); !d.Differs {
				t.Errorf("differs_from_erp = false with only the header %s different", col)
			}
		})
	}
}

// Two different lines in order: a comparison that reads the stored lines in another order flags a false difference.
func TestIngest_DuplicateWithEqualLinesInOrderDoesNotFlagDiffers(t *testing.T) {
	e := newIngest(t)
	e.serve(fakeDocument(docOne, numOne, custOne,
		fakeLine{"Alpha", "1", "10.00", "7.5"}, fakeLine{"Beta", "2", "20.00", "7.5"}))
	inv := e.plantInvoice(plantInv{number: numOne})
	mustExec(t, e.super, "UPDATE invoices SET subtotal = 50.00, vat = 3.75, total = 53.75 WHERE id = $1", inv)
	mustExec(t, e.super, "DELETE FROM line_items WHERE invoice_id = $1", inv)
	for i, l := range [][5]string{{"Alpha", "1", "10.00", "10.00", "0.75"}, {"Beta", "2", "20.00", "40.00", "3.00"}} {
		mustExec(t, e.super, `INSERT INTO line_items (tenant_id, invoice_id, line_no, description, quantity, unit_price, line_total, line_tax, tax_percent)
			VALUES ($1, $2, $3, $4, $5::numeric, $6::numeric, $7::numeric, $8::numeric, 7.5)`, e.tenant, inv, i+1, l[0], l[1], l[2], l[3], l[4])
	}
	e.mustWork(docOne, "1")
	d := e.wantState(docOne, "linked")
	if d.Differs {
		t.Error("differs_from_erp = true for a stored invoice equal to the mapped document, line for line")
	}
}

// A settled row at the same revision is never taken again: no fetch, no second audit row.
func TestIngest_SettledStatesAreNotRetakenAtTheSameRevision(t *testing.T) {
	t.Run("held", func(t *testing.T) {
		e := newIngest(t)
		e.plantDocument(plantDoc{docID: docOne, state: "held", updatedAgo: time.Hour})
		e.mustWork(docOne, "1")
		if n := e.fetches.Load(); n != 0 {
			t.Errorf("Fetch called %d times, want 0", n)
		}
		e.wantNoAudit("erp.document.skipped_duplicate")
	})
	t.Run("linked", func(t *testing.T) {
		e := newIngest(t)
		inv := e.plantInvoice(plantInv{number: numOne})
		e.takenDoc(docOne, "linked", inv)
		e.mustWork(docOne, "1")
		if n := e.fetches.Load(); n != 0 {
			t.Errorf("Fetch called %d times, want 0", n)
		}
		e.wantNoAudit("erp.document.skipped_duplicate")
	})
	t.Run("deleted", func(t *testing.T) {
		e := newIngest(t)
		e.plantDocument(plantDoc{docID: docOne, state: "deleted", deleted: tp(time.Now().Add(-time.Hour)), updatedAgo: time.Hour})
		before := e.doc(docOne)
		e.mustWorkDeleted(docOne, "1")
		if after := e.doc(docOne); !after.UpdatedAt.Equal(before.UpdatedAt) {
			t.Errorf("updated_at moved %v -> %v: a deleted ref at the same revision was taken again", before.UpdatedAt, after.UpdatedAt)
		}
	})
}

// A claim on a number whose invoice another document of the connection already links is held, never adopted.
func TestIngest_OwnClaimOnAnInvoiceAnotherDocumentLinksIsHeld(t *testing.T) {
	e := newIngest(t)
	inv := e.plantInvoice(plantInv{number: numOne, conn: &e.conn})
	e.plantDocument(plantDoc{docID: "fake-co-1-inv-9", state: "linked", invoiceID: &inv})
	e.plantDocument(plantDoc{docID: docOne, state: "pending", claimed: sp(numOne)})
	e.mustWork(docOne, "1")
	d := e.wantState(docOne, "held")
	if d.InvoiceID != nil || d.Claimed != nil {
		t.Errorf("held row invoice_id/claim = %v/%v, want NULL/NULL", d.InvoiceID, d.Claimed)
	}
}

func TestIngest_DeletedRefOnAFailedDocumentClearsItsFailure(t *testing.T) {
	e := newIngest(t)
	e.plantDocument(plantDoc{docID: docOne, state: "failed", failure: sp("unmappable")})
	e.mustWorkDeleted(docOne, "2")
	d := e.wantState(docOne, "deleted")
	if d.Failure != nil || d.Deleted == nil || d.Revision != "2" {
		t.Errorf("failure/deleted/revision = %v/%v/%q, want NULL/set/2", d.Failure, d.Deleted, d.Revision)
	}
}

// A draft that already carries a verdict (blocking violations) is not validated again by a later revision.
func TestIngest_NextRevisionDoesNotRevalidateADraftThatHasAVerdict(t *testing.T) {
	e := newIngest(t)
	e.val.blocking = true
	e.mustWork(docOne, "1")
	id, status, _ := e.invoiceByNumber(e.entity, numOne)
	if status != "draft" || e.ruleSetOf(id) == nil {
		t.Fatalf("setup: invoice status %q, verdict %v, want a draft with a verdict", status, e.ruleSetOf(id))
	}
	e.mustWork(docOne, "2")
	if n := e.val.callCount(); n != 1 {
		t.Errorf("validator calls = %d, want 1: a draft with a verdict is not validated again", n)
	}
}

// A failed final attempt at a new revision keeps the invoice the document already has.
func TestIngest_FinalAttemptFailureKeepsATakenInvoice(t *testing.T) {
	e := newIngest(t)
	inv := e.plantInvoice(plantInv{number: numOne, conn: &e.conn})
	e.takenDoc(docOne, "drafted", inv)
	e.onFetch = func(context.Context, DocRef) (Document, error) { return Document{}, errVendor }
	if err := e.workAs(context.Background(), e.worker, e.args(docOne, "2", false), 8); !errors.Is(err, errVendor) {
		t.Fatalf("Work err = %v, want the vendor error", err)
	}
	d := e.wantState(docOne, "drafted")
	if d.InvoiceID == nil || *d.InvoiceID != inv {
		t.Errorf("row invoice_id = %v, want %s kept", d.InvoiceID, inv)
	}
}

// The number is state-blind, as the unique index is: a discarded invoice still holds it, so the
// document links to it and nothing is created.
func TestIngest_DiscardedInvoiceWithTheNumberIsStillLinked(t *testing.T) {
	e := newIngest(t)
	inv := e.plantInvoice(plantInv{number: numOne, status: "discarded"})
	e.mustWork(docOne, "1")
	d := e.wantState(docOne, "linked")
	if d.InvoiceID == nil || *d.InvoiceID != inv || e.invoices() != 1 {
		t.Errorf("row invoice_id = %v, invoices = %d, want a link to the discarded %s and no new invoice", d.InvoiceID, e.invoices(), inv)
	}
}

// A new revision that cannot be mapped is recorded but leaves the invoice the document already has.
func TestIngest_UnmappableNewRevisionKeepsTheTakenInvoice(t *testing.T) {
	e := newIngest(t)
	inv := e.plantInvoice(plantInv{number: numOne, conn: &e.conn})
	e.takenDoc(docOne, "drafted", inv)
	e.serve(Document{Ref: DocRef{SourceDocID: docOne, SourceRevision: "2"}, ContentType: jsonType, Body: []byte("{")})
	e.mustWork(docOne, "2")
	d := e.wantState(docOne, "drafted")
	if d.InvoiceID == nil || *d.InvoiceID != inv || d.Revision != "2" {
		t.Errorf("row invoice_id/revision = %v/%q, want %s/2", d.InvoiceID, d.Revision, inv)
	}
}

// Another client's live claim on the same number does not hold this client's document.
func TestIngest_AnotherClientsLiveClaimOnTheNumberDoesNotSnooze(t *testing.T) {
	e := newIngest(t)
	y := e.addClient("ingest client Y")
	yConn := e.addConnection(y, "fake-co-2")
	e.plantDocument(plantDoc{conn: yConn, docID: "fake-co-2-inv-1", state: "pending", claimed: sp(numOne)})
	e.mustWork(docOne, "1")
	e.wantState(docOne, "drafted")
}

func TestIngest_OperationalErrorIsReturnedForRetry(t *testing.T) {
	e := newIngest(t)
	e.onFetch = func(context.Context, DocRef) (Document, error) { return Document{}, errVendor }
	err := e.workAs(context.Background(), e.worker, e.args(docOne, "1", false), 1)
	if !errors.Is(err, errVendor) {
		t.Fatalf("Work err = %v, want the vendor error so River retries", err)
	}
	e.noDoc(docOne)
	if n := e.invoices(); n != 0 {
		t.Errorf("invoices = %d, want 0", n)
	}
}

func TestIngest_ValidationOutageIsRetriedUntilValidated(t *testing.T) {
	e := newIngest(t)
	errGate := errors.New("validation gate unreachable")
	e.val.errs = []error{errGate}

	err := e.workAs(context.Background(), e.worker, e.args(docOne, "1", false), 1)
	if !errors.Is(err, errGate) {
		t.Fatalf("first Work err = %v, want the gate error", err)
	}
	if n := e.invoices(); n != 1 {
		t.Fatalf("invoices = %d, want 1 draft left by the failed gate", n)
	}
	id, status, _ := e.invoiceByNumber(e.entity, numOne)
	if status != "draft" {
		t.Errorf("invoice status = %q, want draft", status)
	}
	d := e.wantState(docOne, "pending")
	if d.Claimed == nil || *d.Claimed != numOne {
		t.Errorf("claimed_number = %v, want %s kept for the retry", d.Claimed, numOne)
	}
	e.wantNoAudit("erp.document.pulled")

	if err := e.workAs(context.Background(), e.worker, e.args(docOne, "1", false), 2); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if n := e.invoices(); n != 1 {
		t.Errorf("invoices after retry = %d, want 1", n)
	}
	if st := e.invoiceStatus(id); st != "validated" {
		t.Errorf("invoice status after retry = %q, want validated", st)
	}
	d = e.wantState(docOne, "drafted")
	if d.InvoiceID == nil || *d.InvoiceID != id || d.Claimed != nil {
		t.Errorf("row invoice_id/claim = %v/%v, want %s/NULL", d.InvoiceID, d.Claimed, id)
	}
	e.wantAudit("erp.document.pulled", pulledKeys)
	if got := e.val.called(); len(got) != 2 || got[0] != id || got[1] != id {
		t.Errorf("validator saw %v, want the same draft twice", got)
	}
}

func TestIngest_NextRevisionValidatesAnUnvalidatedDraft(t *testing.T) {
	e := newIngest(t)
	inv := e.plantInvoice(plantInv{number: numOne, conn: &e.conn})
	e.takenDoc(docOne, "drafted", inv)
	if e.ruleSetOf(inv) != nil {
		t.Fatal("setup: the planted draft already has a verdict")
	}
	before := e.withoutValidation(inv)
	e.mustWork(docOne, "2")

	if got := e.val.called(); len(got) != 1 || got[0] != inv {
		t.Errorf("validator saw %v, want [%s]", got, inv)
	}
	if e.ruleSetOf(inv) == nil {
		t.Error("rule_set_version_id is still NULL after the validation")
	}
	if after := e.withoutValidation(inv); after != before {
		t.Errorf("the draft changed beyond its validation:\nbefore %s\nafter  %s", before, after)
	}
	if d := e.wantState(docOne, "drafted"); d.Revision != "2" {
		t.Errorf("source_revision = %q, want 2", d.Revision)
	}
	if n := e.erpAudits(); n != 0 {
		t.Errorf("erp.document audit rows = %d, want 0", n)
	}
}

// --- concurrency and claims ---

// untilDone runs Work again each time it answers a River snooze, as River would after the delay.
func (e *ingestEnv) untilDone(w *IngestWorker, a IngestArgs) error {
	for range 40 {
		err := e.workAs(context.Background(), w, a, 1)
		if _, snoozed := snoozeOf(err); !snoozed {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("still snoozing after 40 runs")
}

func invoiceStore(e *ingestEnv) *invoice.Store { return invoice.NewStore(e.app) }

func TestIngest_ConcurrentIngestsOfOneDocumentCreateOneInvoice(t *testing.T) {
	e := newIngest(t)
	store := invoiceStore(e)
	slow := e.workerWith(createFn(func(ctx context.Context, in invoice.CreateInput) (invoice.Invoice, error) {
		time.Sleep(300 * time.Millisecond) // the second ingest arrives while the first holds its claim
		return store.Create(ctx, in)
	}))
	errs := race(
		func() error { return e.untilDone(slow, e.args(docOne, "1", false)) },
		func() error { return e.untilDone(slow, e.args(docOne, "2", false)) },
	)
	for i, err := range errs {
		if err != nil {
			t.Errorf("ingest %d: %v, want nil (no 23505 may surface)", i+1, err)
		}
	}
	if n := e.invoices(); n != 1 {
		t.Errorf("invoices = %d, want 1", n)
	}
	e.wantState(docOne, "drafted")
	e.wantAudit("erp.document.pulled", pulledKeys)
	e.wantNoAudit("erp.document.skipped_duplicate")
}

func TestIngest_TwoDocumentsOneNumberOneOwner(t *testing.T) {
	e := newIngest(t)
	docA := fakeDocument("doc-A", numOne, custOne, fakeLine{"Doc A line", "1", "100.00", "7.5"})
	docB := fakeDocument("doc-B", numOne, custOne, fakeLine{"Doc B line", "1", "100.00", "7.5"})
	e.onFetch = func(_ context.Context, ref DocRef) (Document, error) {
		if ref.SourceDocID == "doc-A" {
			return docA, nil
		}
		return docB, nil
	}
	store := invoiceStore(e)
	created, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	holdA := e.workerWith(createFn(func(ctx context.Context, in invoice.CreateInput) (invoice.Invoice, error) {
		inv, err := store.Create(ctx, in)
		close(created)
		<-release
		return inv, err
	}))

	aDone := make(chan error, 1)
	go func() { aDone <- e.workAs(context.Background(), holdA, e.args("doc-A", "1", false), 1) }()
	select {
	case <-created:
	case <-time.After(20 * time.Second):
		t.Fatal("doc-A never reached its create")
	}

	// A's invoice is committed, its claim is live, and no row links the invoice yet.
	if _, snoozed := snoozeOf(e.workAs(context.Background(), e.worker, e.args("doc-B", "1", false), 1)); !snoozed {
		t.Error("doc-B did not snooze while doc-A holds a live claim on the number")
	}
	e.noDoc("doc-B")

	close(release)
	if err := <-aDone; err != nil {
		t.Fatalf("doc-A: %v", err)
	}
	if err := e.untilDone(e.worker, e.args("doc-B", "1", false)); err != nil {
		t.Fatalf("doc-B: %v, want nil (no 23505 may surface)", err)
	}

	if n := e.invoices(); n != 1 {
		t.Fatalf("invoices = %d, want 1", n)
	}
	a := e.wantState("doc-A", "drafted")
	b := e.wantState("doc-B", "held")
	if b.InvoiceID != nil || a.InvoiceID == nil {
		t.Errorf("doc-A invoice = %v, doc-B invoice = %v, want A's only", a.InvoiceID, b.InvoiceID)
	}
	var desc string
	mustScan(t, e.super, "SELECT description FROM line_items WHERE invoice_id = $1", []any{*a.InvoiceID}, &desc)
	if desc != "Doc A line" {
		t.Errorf("the invoice's line = %q, want doc-A's body", desc)
	}
}

func TestIngest_HoldsOneConnectionAtATime(t *testing.T) {
	e := newIngest(t)
	one := poolWithOneConn(t)
	w := &IngestWorker{Pool: one, Registry: e.reg, Invoices: invoice.NewStore(one), Gate: e.val}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.workAs(ctx, w, e.args(docOne, "1", false), 1); err != nil {
		t.Fatalf("Work on a one-connection pool: %v (a nested acquisition waits for a second connection)", err)
	}
	e.wantState(docOne, "drafted")
	if n := e.invoices(); n != 1 {
		t.Errorf("invoices = %d, want 1", n)
	}
}

func TestIngest_StaleClaimIsTakenOver(t *testing.T) {
	e := newIngest(t)
	e.plantDocument(plantDoc{docID: docOne, state: "pending", claimed: sp(numOne), updatedAgo: 20 * time.Minute})
	e.mustWork(docOne, "2")

	d := e.wantState(docOne, "drafted")
	if d.Revision != "2" || d.Claimed != nil {
		t.Errorf("row revision/claim = %q/%v, want 2/NULL", d.Revision, d.Claimed)
	}
	if n := e.invoices(); n != 1 {
		t.Errorf("invoices = %d, want 1", n)
	}
}

func TestIngest_StaleClaimWithAnExistingInvoiceIsLinked(t *testing.T) {
	e := newIngest(t)
	inv := e.plantInvoice(plantInv{number: numOne, conn: &e.conn, subtotal: "5000.00", vat: "0.00", total: "5000.00"})
	e.plantDocument(plantDoc{docID: docOne, state: "pending", claimed: sp(numOne), updatedAgo: 20 * time.Minute})
	before := e.snapshot(inv)
	e.mustWork(docOne, "2")

	d := e.wantState(docOne, "linked")
	if d.InvoiceID == nil || *d.InvoiceID != inv {
		t.Errorf("row invoice_id = %v, want %s", d.InvoiceID, inv)
	}
	if want := e.differsAgainst(e.mapped(docOne), inv); !want || d.Differs != want {
		t.Errorf("differs_from_erp = %v, want true (Differs = %v)", d.Differs, want)
	}
	if d.Claimed != nil {
		t.Errorf("claimed_number = %q, want cleared", *d.Claimed)
	}
	if n := e.invoices(); n != 1 {
		t.Errorf("invoices = %d, want 1", n)
	}
	if after := e.snapshot(inv); after != before {
		t.Error("the invoice changed")
	}
	if s := e.wantAudit("erp.document.skipped_duplicate", dupKeys); s.Payload["linked"] != true {
		t.Errorf("skipped_duplicate payload = %v, want linked true", s.Payload)
	}
	e.wantNoAudit("erp.document.pulled")
}

func TestIngest_LiveClaimOfAnotherRevisionSnoozes(t *testing.T) {
	e := newIngest(t)
	e.plantDocument(plantDoc{docID: docOne, state: "pending", claimed: sp(numOne)})
	before := e.docJSON(docOne)
	err := e.work(docOne, "2")

	d, snoozed := snoozeOf(err)
	if !snoozed {
		t.Fatalf("Work err = %v, want a River snooze", err)
	}
	if d != 30*time.Second {
		t.Errorf("snooze = %s, want 30s", d)
	}
	if after := e.docJSON(docOne); after != before {
		t.Errorf("the row changed:\nbefore %s\nafter  %s", before, after)
	}
	if n := e.invoices(); n != 0 {
		t.Errorf("invoices = %d, want 0", n)
	}
}

// --- skipped ingests reopen the connection, the final attempt leaves a row ---

func TestIngest_SkippedIngestReopensTheConnection(t *testing.T) {
	e := newIngest(t)
	mustExec(t, e.super, "UPDATE erp_connections SET cursor = '\\x0a0b' WHERE id = $1", e.conn)
	mustExec(t, e.super, "UPDATE erp_connections SET status = 'disconnected', grant_id = NULL WHERE id = $1", e.conn)

	for want := int64(1); want <= 2; want++ {
		e.mustWork(docOne, "1")
		var cursor []byte
		var epoch int64
		mustScan(t, e.super, "SELECT cursor, ingest_epoch FROM erp_connections WHERE id = $1", []any{e.conn}, &cursor, &epoch)
		if cursor != nil || epoch != want {
			t.Fatalf("after %d skipped ingest(s): cursor/ingest_epoch = %v/%d, want NULL/%d", want, cursor, epoch, want)
		}
	}
	e.noDoc(docOne)
	if n := e.fetches.Load(); n != 0 {
		t.Errorf("Fetch called %d times, want 0", n)
	}
}

func TestIngest_FinalAttemptLeavesAnExhaustedRow(t *testing.T) {
	e := newIngest(t)
	e.onFetch = func(context.Context, DocRef) (Document, error) { return Document{}, errVendor }

	if err := e.workAs(context.Background(), e.worker, e.args(docOne, "1", false), 7); !errors.Is(err, errVendor) {
		t.Fatalf("attempt 7 err = %v, want the vendor error", err)
	}
	e.noDoc(docOne)

	if err := e.workAs(context.Background(), e.worker, e.args(docOne, "1", false), 8); !errors.Is(err, errVendor) {
		t.Fatalf("attempt 8 err = %v, want the vendor error returned after the row is written", err)
	}
	e.wantFailed(docOne, "exhausted")
}

func TestIngest_FinalAttemptAfterCreateLeavesADraft(t *testing.T) {
	e := newIngest(t)
	errGate := errors.New("validation gate unreachable")
	e.val.failAlways = errGate

	if err := e.workAs(context.Background(), e.worker, e.args(docOne, "1", false), 8); !errors.Is(err, errGate) {
		t.Fatalf("attempt 8 err = %v, want the gate error", err)
	}
	id, status, _ := e.invoiceByNumber(e.entity, numOne)
	d := e.wantState(docOne, "drafted")
	if d.InvoiceID == nil || *d.InvoiceID != id {
		t.Errorf("row invoice_id = %v, want %s", d.InvoiceID, id)
	}
	if status != "draft" || e.ruleSetOf(id) != nil {
		t.Errorf("invoice status/rule set = %q/%v, want an unvalidated draft", status, e.ruleSetOf(id))
	}
	if d.Claimed != nil {
		t.Errorf("claimed_number = %q, want cleared", *d.Claimed)
	}
	if n := e.invoices(); n != 1 {
		t.Errorf("invoices = %d, want 1", n)
	}
}

func TestIngest_ExhaustedRowIsFetchedAgainAtItsOwnRevision(t *testing.T) {
	t.Run("exhausted is retaken", func(t *testing.T) {
		e := newIngest(t)
		e.plantDocument(plantDoc{docID: docOne, state: "failed", failure: sp("exhausted")})
		e.mustWork(docOne, "1")

		if n := e.fetches.Load(); n != 1 {
			t.Errorf("Fetch called %d times, want 1", n)
		}
		d := e.wantState(docOne, "drafted")
		if d.Failure != nil || d.InvoiceID == nil {
			t.Errorf("failure/invoice_id = %v/%v, want NULL and an invoice", d.Failure, d.InvoiceID)
		}
	})
	t.Run("another failure is not", func(t *testing.T) {
		e := newIngest(t)
		e.plantDocument(plantDoc{docID: docOne, state: "failed", failure: sp("unmappable")})
		before := e.docJSON(docOne)
		e.mustWork(docOne, "1")

		if n := e.fetches.Load(); n != 0 {
			t.Errorf("Fetch called %d times, want 0: a settled failure is short-circuited", n)
		}
		if after := e.docJSON(docOne); after != before {
			t.Error("the row changed")
		}
		if n := e.invoices(); n != 0 {
			t.Errorf("invoices = %d, want 0", n)
		}
	})
}

func TestIngest_ExhaustedRowIsRetakenAtANewRevision(t *testing.T) {
	e := newIngest(t)
	e.plantDocument(plantDoc{docID: docOne, state: "failed", failure: sp("exhausted")})
	e.mustWork(docOne, "2")

	d := e.wantState(docOne, "drafted")
	if d.Failure != nil || d.Revision != "2" || d.InvoiceID == nil {
		t.Errorf("failure/revision/invoice_id = %v/%q/%v, want NULL/2/an invoice", d.Failure, d.Revision, d.InvoiceID)
	}
}

// --- every taken row stores the hash ---

func TestIngest_EveryTakenRowStoresTheHash(t *testing.T) {
	cases := []struct {
		name, state string
		plant       func(e *ingestEnv)
	}{
		{"row 1: a new draft", "drafted", func(*ingestEnv) {}},
		{"row 2: an adopted retry", "drafted", func(e *ingestEnv) {
			e.plantInvoice(plantInv{number: numOne, conn: &e.conn})
			e.plantDocument(plantDoc{docID: docOne, rev: "7", state: "pending", claimed: sp(numOne)})
		}},
		{"row 3: a link", "linked", func(e *ingestEnv) { e.plantInvoice(plantInv{number: numOne}) }},
		{"row 4: held", "held", func(e *ingestEnv) {
			inv := e.plantInvoice(plantInv{number: numOne})
			e.plantDocument(plantDoc{docID: "doc-A", state: "linked", invoiceID: &inv})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newIngest(t)
			tc.plant(e)
			e.mustWork(docOne, "7")

			d := e.wantState(docOne, tc.state)
			if want := ContentHash(e.mapped(docOne)); d.Hash == nil || *d.Hash != want {
				t.Errorf("content_hash = %v, want ContentHash(mapped) %s", d.Hash, want)
			}
			if d.HashVersion == nil || int(*d.HashVersion) != HashVersion {
				t.Errorf("hash_version = %v, want %d", d.HashVersion, HashVersion)
			}
			if d.Revision != "7" {
				t.Errorf("source_revision = %q, want the ref's 7", d.Revision)
			}
		})
	}
}

// --- the connection's client only ---

func TestIngest_DraftLandsInTheConnectionsClient(t *testing.T) {
	e := newIngest(t)
	y := e.addClient("client Y")
	e.mustWork(docOne, "1")

	d := e.wantState(docOne, "drafted")
	if d.Entity != e.entity {
		t.Errorf("row entity_id = %s, want the connection's %s", d.Entity, e.entity)
	}
	if n := e.invoicesIn(e.entity); n != 1 {
		t.Errorf("invoices in client X = %d, want 1", n)
	}
	if n := e.invoicesIn(y); n != 0 {
		t.Errorf("invoices in client Y = %d, want 0", n)
	}
	if n := countOf(t, e.super, "SELECT count(*) FROM erp_source_documents WHERE entity_id = $1", y); n != 0 {
		t.Errorf("documents in client Y = %d, want 0", n)
	}
}

func TestIngest_SameNumberInAnotherClientIsNotLinked(t *testing.T) {
	e := newIngest(t)
	y := e.addClient("client Y")
	theirs := e.plantInvoice(plantInv{number: numOne, entity: y})
	before := e.snapshot(theirs)
	e.mustWork(docOne, "1")

	d := e.wantState(docOne, "drafted")
	mine, _, _ := e.invoiceByNumber(e.entity, numOne)
	if d.InvoiceID == nil || *d.InvoiceID != mine || mine == theirs {
		t.Errorf("row invoice_id = %v, want a new invoice in client X (not %s)", d.InvoiceID, theirs)
	}
	if after := e.snapshot(theirs); after != before {
		t.Error("client Y's invoice changed")
	}
	if n := countOf(t, e.super, "SELECT count(*) FROM erp_source_documents WHERE invoice_id = $1", theirs); n != 0 {
		t.Errorf("rows linking client Y's invoice = %d, want 0", n)
	}
	e.wantNoAudit("erp.document.skipped_duplicate")
}

func TestIngest_AnotherClientsInvoiceIsNeverAdopted(t *testing.T) {
	e := newIngest(t)
	y := e.addClient("client Y")
	theirs := e.plantInvoice(plantInv{number: numOne, entity: y, conn: &e.conn})
	e.plantDocument(plantDoc{docID: docOne, state: "pending", claimed: sp(numOne)})
	before := e.snapshot(theirs)
	e.mustWork(docOne, "1")

	d := e.wantState(docOne, "drafted")
	mine, _, _ := e.invoiceByNumber(e.entity, numOne)
	if d.InvoiceID == nil || *d.InvoiceID != mine || mine == theirs {
		t.Errorf("row invoice_id = %v, want a new invoice in client X (not %s)", d.InvoiceID, theirs)
	}
	if after := e.snapshot(theirs); after != before {
		t.Error("client Y's invoice changed")
	}
	if n := countOf(t, e.super, "SELECT count(*) FROM erp_source_documents WHERE invoice_id = $1", theirs); n != 0 {
		t.Errorf("rows linking client Y's invoice = %d, want 0", n)
	}
	if n := e.invoicesIn(e.entity); n != 1 {
		t.Errorf("invoices in client X = %d, want 1", n)
	}
}

func TestIngest_NumberLockSerializesDocumentsOfOneNumber(t *testing.T) {
	e := newIngest(t)
	// The connection row lock already serializes one connection's claims, so the number lock is
	// what orders two connections of one client; a third, with another number, must not wait.
	const numTwo = "FK1-0002"
	c2 := e.addConnection(e.entity, "fake-co-2")
	c3 := e.addConnection(e.entity, "fake-co-3")
	docs := map[string]Document{
		"doc-A": fakeDocument("doc-A", numOne, custOne, fakeLine{"Doc A line", "1", "100.00", "7.5"}),
		"doc-B": fakeDocument("doc-B", numOne, custOne, fakeLine{"Doc B line", "1", "100.00", "7.5"}),
		"doc-C": fakeDocument("doc-C", numTwo, custOne, fakeLine{"Doc C line", "1", "100.00", "7.5"}),
	}
	e.onFetch = func(_ context.Context, ref DocRef) (Document, error) { return docs[ref.SourceDocID], nil }
	ingest := func(conn, docID string) error {
		a := e.args(docID, "1", false)
		a.ConnectionID = conn
		return e.untilDone(e.worker, a)
	}
	// Without the lock both claims read "no invoice" together; here it is held from outside so they must wait.
	ctx := context.Background()
	holder, err := e.super.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx) //nolint:errcheck
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('erp_number:' || $1 || ':' || $2, 0))`, e.entity, numOne); err != nil {
		t.Fatal(err)
	}
	errs, other := make(chan error, 2), make(chan error, 1)
	go func() { errs <- ingest(e.conn, "doc-A") }()
	go func() { errs <- ingest(c2, "doc-B") }()
	// Both claims are parked on the advisory lock, not merely slow.
	deadline := time.Now().Add(5 * time.Second)
	for waiting := 0; waiting < 2; {
		waiting = countOf(t, e.super, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted
			AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`)
		if waiting < 2 && time.Now().After(deadline) {
			t.Errorf("claims waiting on the number lock = %d, want 2", waiting)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, c := range []string{e.conn, c2} {
		if d, ok := e.docOn(c, "doc-A"); ok {
			t.Errorf("a claim wrote a row in state %q while the number lock was held", d.State)
		}
		if d, ok := e.docOn(c, "doc-B"); ok {
			t.Errorf("a claim wrote a row in state %q while the number lock was held", d.State)
		}
	}
	go func() { other <- ingest(c3, "doc-C") }()
	select {
	case err := <-other:
		if err != nil {
			t.Errorf("another number's document: %v, want nil", err)
		}
		if d, ok := e.docOn(c3, "doc-C"); !ok || d.State != "drafted" {
			t.Errorf("doc-C = %+v (found %v) while the lock of %s is held, want drafted", d, ok, numOne)
		}
	case <-time.After(5 * time.Second):
		t.Error("a document of another number waited on the lock of " + numOne)
	}

	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("Work: %v, want nil (no 23505 may surface)", err)
		}
	}
	if n := e.invoices(); n != 2 {
		t.Fatalf("invoices = %d, want 2 (one for %s, one for %s)", n, numOne, numTwo)
	}
	states := map[string]int{}
	for conn, id := range map[string]string{e.conn: "doc-A", c2: "doc-B"} {
		d, ok := e.docOn(conn, id)
		if !ok {
			t.Fatalf("no row for %s", id)
		}
		states[d.State]++
	}
	if states["drafted"] != 1 || states["linked"] != 1 {
		t.Errorf("states = %v, want one drafted and one linked to its invoice", states)
	}
}
