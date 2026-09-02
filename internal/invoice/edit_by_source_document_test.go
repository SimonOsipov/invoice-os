// edit_by_source_document_test.go: EditBySourceDocumentTx -- the tx-taking entry the extraction
// review screen corrects an invoice through. It resolves the invoice by source_document_id and
// then runs editTx, so it inherits the fixable gate, the demotion, the kept-as-is clear and the
// invoice.updated row rather than restating any of them.
//
// Reuses store_test.go's dbTestPools/seedTenant/seedEntity/seedInvoice/mustCount/auditCount/
// strPtr harness and source_document_test.go's seedDocument.
//
// Helpers use an ebs* prefix.
package invoice

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	ebsTotalBefore = "100.00"
	ebsTotalAfter  = "1500.00"
)

type ebsFix struct {
	ctx        context.Context
	super, app *pgxpool.Pool
	store      *Store
	tenantID   string
	entityID   string
	documentID string
	invoiceID  string
}

// ebsSeed builds a tenant, an entity, a document and one draft invoice filed from that document.
func ebsSeed(t *testing.T, label string) *ebsFix {
	t.Helper()
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, label+" tenant")
	entityID := seedEntity(t, super, tenantID, label+" entity")
	documentID := seedDocument(t, super, tenantID)
	store := NewStore(app)
	c := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: memberSubject, Role: "authenticated", TenantID: tenantID,
	})

	inv, err := store.Create(c, CreateInput{
		EntityID:         entityID,
		InvoiceNumber:    label,
		Total:            strPtr(ebsTotalBefore),
		SourceDocumentID: &documentID,
	})
	if err != nil {
		t.Fatalf("seed the invoice filed from %s: %v", documentID, err)
	}
	return &ebsFix{
		ctx: c, super: super, app: app, store: store,
		tenantID: tenantID, entityID: entityID, documentID: documentID, invoiceID: inv.ID,
	}
}

// ebsEdit runs one EditBySourceDocumentTx inside a transaction the TEST opens, which is the
// posture the correction handler uses.
func (f *ebsFix) edit(t *testing.T, documentID string, in EditInput) (Invoice, error) {
	t.Helper()
	var out Invoice
	err := db.WithinRequestTenantTx(f.ctx, f.app, func(tx pgx.Tx) error {
		var err error
		out, err = f.store.EditBySourceDocumentTx(f.ctx, tx, documentID, in)
		return err
	})
	return out, err
}

func (f *ebsFix) total(t *testing.T, invoiceID string) string {
	t.Helper()
	var out *string
	if err := f.super.QueryRow(context.Background(),
		`SELECT total::text FROM invoices WHERE id = $1`, invoiceID).Scan(&out); err != nil {
		t.Fatalf("read invoices.total for %s: %v", invoiceID, err)
	}
	if out == nil {
		return "<null>"
	}
	return *out
}

func (f *ebsFix) status(t *testing.T, invoiceID string) Status {
	t.Helper()
	var s string
	if err := f.super.QueryRow(context.Background(),
		`SELECT status FROM invoices WHERE id = $1`, invoiceID).Scan(&s); err != nil {
		t.Fatalf("read invoices.status for %s: %v", invoiceID, err)
	}
	return Status(s)
}

// ebsSecondInvoiceOn files a second invoice from the same document, which is the ambiguous
// target the resolver must refuse rather than guess at.
func (f *ebsFix) secondInvoiceOn(t *testing.T, documentID, number string) string {
	t.Helper()
	var id string
	if err := f.super.QueryRow(context.Background(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, total, source_document_id)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		f.tenantID, f.entityID, number, ebsTotalBefore, documentID).Scan(&id); err != nil {
		t.Fatalf("seed the second invoice on document %s: %v", documentID, err)
	}
	return id
}

// --- resolution -----------------------------------------------------------------------------

// Zero, one and two invoices for one source_document_id. Two is an AMBIGUOUS target: writing the
// corrected value onto the wrong invoice is the silent-wrong outcome this route exists to
// prevent, so it refuses rather than picking the first row.
func TestRLS_EditBySourceDocumentTxResolvesExactlyOneInvoice(t *testing.T) {
	f := ebsSeed(t, "EBS-01")

	// one: the control, and the only arm that may write.
	got, err := f.edit(t, f.documentID, EditInput{UpdateInput: UpdateInput{Total: strPtr(ebsTotalAfter)}})
	if err != nil {
		t.Fatalf("one invoice on the document: want success, got %v", err)
	}
	if got.ID != f.invoiceID {
		t.Errorf("the edit landed on invoice %s, want the one filed from the document, %s", got.ID, f.invoiceID)
	}
	if stored := f.total(t, f.invoiceID); stored != ebsTotalAfter {
		t.Errorf("invoices.total = %s, want %s", stored, ebsTotalAfter)
	}

	// zero: a document nothing was filed from.
	empty := seedDocument(t, f.super, f.tenantID)
	if _, err := f.edit(t, empty, EditInput{UpdateInput: UpdateInput{Total: strPtr("7.00")}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a document with no invoice: err = %v, want ErrNotFound", err)
	}

	// two: the ambiguous target.
	second := f.secondInvoiceOn(t, f.documentID, "EBS-01-DUP")
	_, err = f.edit(t, f.documentID, EditInput{UpdateInput: UpdateInput{Total: strPtr("9.00")}})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("two invoices on one document: err = %v, want ErrNotFound -- an ambiguous target must never be guessed at", err)
	}
	if stored := f.total(t, f.invoiceID); stored != ebsTotalAfter {
		t.Errorf("the first invoice's total = %s after the ambiguous edit, want the unchanged %s", stored, ebsTotalAfter)
	}
	if stored := f.total(t, second); stored != ebsTotalBefore {
		t.Errorf("the second invoice's total = %s after the ambiguous edit, want the unchanged %s", stored, ebsTotalBefore)
	}
}

// --- the caller owns the transaction ----------------------------------------------------------

// The whole point of the tx-taking entry: a caller that rolls back takes the edit with it, so
// the correction row and the invoice field commit together or not at all.
func TestRLS_EditBySourceDocumentTxSharesTheCallersTransaction(t *testing.T) {
	f := ebsSeed(t, "EBS-02")

	beforeUpdated := auditCount(t, f.app, f.tenantID, "invoice.updated")

	rollback := errors.New("ebs: the caller rolled back")
	err := db.WithinRequestTenantTx(f.ctx, f.app, func(tx pgx.Tx) error {
		inv, err := f.store.EditBySourceDocumentTx(f.ctx, tx, f.documentID,
			EditInput{UpdateInput: UpdateInput{Total: strPtr(ebsTotalAfter)}})
		if err != nil {
			return err
		}
		// Read back INSIDE the caller's transaction: if the method had opened its own, this
		// would still read the pre-edit value.
		var seen string
		if err := tx.QueryRow(f.ctx, `SELECT total::text FROM invoices WHERE id = $1`, inv.ID).Scan(&seen); err != nil {
			return err
		}
		if seen != ebsTotalAfter {
			t.Errorf("inside the caller's transaction invoices.total reads %s, want %s -- the write went somewhere else", seen, ebsTotalAfter)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("the caller's transaction returned %v, want the induced rollback", err)
	}

	if stored := f.total(t, f.invoiceID); stored != ebsTotalBefore {
		t.Errorf("invoices.total = %s after the caller rolled back, want the unchanged %s -- the method opened a transaction of its own", stored, ebsTotalBefore)
	}
	if n := auditCount(t, f.app, f.tenantID, "invoice.updated"); n != beforeUpdated {
		t.Errorf("invoice.updated rows = %d after the caller rolled back, want the unchanged %d", n, beforeUpdated)
	}
}

// --- the empty edit ---------------------------------------------------------------------------

// editTx does NOT carry Edit's step-1 guard: handed an all-nil EditInput it falls through to the
// fingerprint check, finds no change, and returns `before` with no error, no audit row and no
// demotion. A silent no-op is the wrong answer for a method other code will call, so this entry
// repeats the guard exactly as Edit does.
func TestRLS_EditBySourceDocumentTxRefusesAnEmptyEdit(t *testing.T) {
	f := ebsSeed(t, "EBS-03")

	beforeUpdated := auditCount(t, f.app, f.tenantID, "invoice.updated")

	// The control: Store.Edit refuses the same input, which is the behaviour being matched.
	if _, err := f.store.Edit(f.ctx, f.invoiceID, EditInput{}); !errors.Is(err, ErrValidation) {
		t.Fatalf("control: Store.Edit with an all-nil EditInput returned %v, want ErrValidation -- the claim below has no reference behaviour", err)
	}

	_, err := f.edit(t, f.documentID, EditInput{})
	if !errors.Is(err, ErrValidation) {
		t.Errorf("EditBySourceDocumentTx with an all-nil EditInput returned %v, want ErrValidation -- editTx alone answers a silent no-op", err)
	}
	if n := auditCount(t, f.app, f.tenantID, "invoice.updated"); n != beforeUpdated {
		t.Errorf("invoice.updated rows = %d after an empty edit, want the unchanged %d", n, beforeUpdated)
	}
	if got := f.status(t, f.invoiceID); got != StatusDraft {
		t.Errorf("invoices.status = %q after an empty edit, want %q", got, StatusDraft)
	}
}

// --- what reusing editTx buys ------------------------------------------------------------------

// A corrected invoice is re-validated rather than keeping a stale verdict. A raw UPDATE invoices
// passes every other case in this file and fails only this one.
func TestRLS_EditBySourceDocumentTxDemotesAValidatedInvoiceToDraft(t *testing.T) {
	f := ebsSeed(t, "EBS-04")
	if _, err := f.store.Transition(f.ctx, f.invoiceID, StatusValidated); err != nil {
		t.Fatalf("pre-hop Transition(-> validated): %v", err)
	}

	beforeHistory := mustCount(t, f.super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, f.invoiceID)
	beforeUpdated := auditCount(t, f.app, f.tenantID, "invoice.updated")
	beforeTransitioned := auditCount(t, f.app, f.tenantID, "invoice.transitioned")

	got, err := f.edit(t, f.documentID, EditInput{UpdateInput: UpdateInput{Total: strPtr(ebsTotalAfter)}})
	if err != nil {
		t.Fatalf("correcting a validated invoice: want success, got %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("the returned status = %q, want %q", got.Status, StatusDraft)
	}
	if stored := f.status(t, f.invoiceID); stored != StatusDraft {
		t.Errorf("invoices.status = %q, want %q -- a corrected invoice must be re-validated, not keep a stale verdict", stored, StatusDraft)
	}
	if n := mustCount(t, f.super,
		`SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = 'validated' AND to_status = 'draft'`,
		f.invoiceID); n != 1 {
		t.Errorf("%d (validated,draft) history row(s), want exactly 1", n)
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, f.invoiceID); n != beforeHistory+1 {
		t.Errorf("history rows = %d, want %d", n, beforeHistory+1)
	}
	if n := auditCount(t, f.app, f.tenantID, "invoice.updated"); n != beforeUpdated+1 {
		t.Errorf("invoice.updated rows = %d, want %d", n, beforeUpdated+1)
	}
	if n := auditCount(t, f.app, f.tenantID, "invoice.transitioned"); n != beforeTransitioned+1 {
		t.Errorf("invoice.transitioned rows = %d, want %d", n, beforeTransitioned+1)
	}
}

// A correction is a genuine content change, so it erases any recorded keep-as-is reason. That is
// deliberate, not incidental: the compliance decision was taken about the value the invoice used
// to carry.
func TestRLS_EditBySourceDocumentTxClearsTheKeptAsIsReason(t *testing.T) {
	f := ebsSeed(t, "EBS-05")

	mark := func() {
		t.Helper()
		if _, err := f.super.Exec(context.Background(),
			`UPDATE invoices SET kept_as_is_at = now(), kept_as_is_by = $1, kept_as_is_reason = $2 WHERE id = $3`,
			memberSubject, "the supplier confirmed the figure", f.invoiceID); err != nil {
			t.Fatalf("stamp the kept-as-is triple: %v", err)
		}
	}
	kept := func() bool {
		t.Helper()
		var at *string
		if err := f.super.QueryRow(context.Background(),
			`SELECT kept_as_is_at::text FROM invoices WHERE id = $1`, f.invoiceID).Scan(&at); err != nil {
			t.Fatalf("read kept_as_is_at: %v", err)
		}
		return at != nil
	}

	// The control: a correction that changes nothing takes the no-op path and must leave the
	// mark standing, so the clear below is gated on a real content change.
	mark()
	if _, err := f.edit(t, f.documentID, EditInput{UpdateInput: UpdateInput{Total: strPtr(ebsTotalBefore)}}); err != nil {
		t.Fatalf("a confirming correction: want success, got %v", err)
	}
	if !kept() {
		t.Errorf("a correction that changed nothing cleared the keep-as-is reason; the clear must follow a real content change")
	}

	if _, err := f.edit(t, f.documentID, EditInput{UpdateInput: UpdateInput{Total: strPtr(ebsTotalAfter)}}); err != nil {
		t.Fatalf("a real correction: want success, got %v", err)
	}
	if kept() {
		t.Errorf("invoices.kept_as_is_at survived a real correction, want NULL -- the compliance decision was taken about a value that no longer stands")
	}
}

// --- the refactor, named ------------------------------------------------------------------------

// ebsEditTxCallers returns every func in this package's non-test files whose body calls editTx.
func ebsEditTxCallers(t *testing.T) []string {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob *.go: %v", err)
	}
	var out []string
	var funcs int
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v -- a file the scan cannot read is a file it reports clean on", name, err)
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			funcs++
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "editTx" {
					out = append(out, name+":"+fd.Name.Name)
				}
				return true
			})
		}
	}
	if funcs < 50 {
		t.Fatalf("the scan walked %d func declaration(s) in internal/invoice, want at least 50 -- a walk that reached nothing reports no caller either", funcs)
	}
	return out
}

// The shipped edit_test.go suite is the real oracle for editTx's body. This names the refactor,
// so a failure reads as "the extraction changed behaviour" rather than as an unrelated edit bug,
// and it pins the fix-loop rules to ONE owner: both entries must go through editTx.
func TestStoreEdit_BodyIsUnchangedByTheEditTxExtraction(t *testing.T) {
	// Structural half: exactly two callers, and they are the two entries.
	src, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	if !strings.Contains(string(src), "func editTx(") {
		t.Fatal("store.go declares no editTx, so the caller scan below reads nothing")
	}
	got := ebsEditTxCallers(t)
	want := []string{"store.go:Edit", "store.go:EditBySourceDocumentTx"}
	if len(got) != len(want) {
		t.Errorf("editTx is called from %v, want exactly %v -- a second copy of the fix-loop rules is how the two entries drift apart", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("editTx is called from %v, want exactly %v", got, want)
				break
			}
		}
	}

	// Behavioural half: Store.Edit's three shipped outcomes, unchanged by the extraction.
	f := ebsSeed(t, "EBS-06")

	if _, err := f.store.Edit(f.ctx, f.invoiceID, EditInput{}); !errors.Is(err, ErrValidation) {
		t.Errorf("Store.Edit(all-nil) = %v, want ErrValidation before any transaction opens", err)
	}
	if _, err := f.store.Edit(f.ctx, uuid.NewString(), EditInput{UpdateInput: UpdateInput{Total: strPtr("5.00")}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Store.Edit(unknown id) = %v, want ErrNotFound", err)
	}

	beforeUpdated := auditCount(t, f.app, f.tenantID, "invoice.updated")
	if _, err := f.store.Edit(f.ctx, f.invoiceID, EditInput{UpdateInput: UpdateInput{Total: strPtr(ebsTotalBefore)}}); err != nil {
		t.Fatalf("Store.Edit(no-op): %v", err)
	}
	if n := auditCount(t, f.app, f.tenantID, "invoice.updated"); n != beforeUpdated {
		t.Errorf("a no-op Store.Edit wrote %d invoice.updated row(s), want the unchanged %d", n-beforeUpdated, beforeUpdated)
	}

	if _, err := f.store.Transition(f.ctx, f.invoiceID, StatusValidated); err != nil {
		t.Fatalf("pre-hop Transition(-> validated): %v", err)
	}
	if _, err := f.store.Edit(f.ctx, f.invoiceID, EditInput{UpdateInput: UpdateInput{Total: strPtr(ebsTotalAfter)}}); err != nil {
		t.Fatalf("Store.Edit(content change on a validated invoice): %v", err)
	}
	if got := f.status(t, f.invoiceID); got != StatusDraft {
		t.Errorf("Store.Edit left a validated invoice at %q, want the demotion to %q", got, StatusDraft)
	}
	if n := auditCount(t, f.app, f.tenantID, "invoice.updated"); n != beforeUpdated+1 {
		t.Errorf("invoice.updated rows = %d, want %d", n, beforeUpdated+1)
	}
}

// --- the clear sentinels ----------------------------------------------------------------------

func ebsShow(p *string) string {
	if p == nil {
		return "NULL"
	}
	return `"` + *p + `"`
}

// ebsColumn reads one header column as text, as the SUPERUSER, so a NULL is distinguishable
// from a row RLS filtered away.
func (f *ebsFix) column(t *testing.T, invoiceID, col string) *string {
	t.Helper()
	var out *string
	if err := f.super.QueryRow(context.Background(),
		`SELECT (to_jsonb(i) ->> $1) FROM invoices i WHERE i.id = $2`, col, invoiceID).Scan(&out); err != nil {
		t.Fatalf("read invoices.%s for %s: %v", col, invoiceID, err)
	}
	return out
}

// ClearText/ClearDate write SQL NULL, over TEXT, NUMERIC and DATE alike -- the three column
// shapes the extraction vocabulary spans, and the reason "" is not the alternative: vat and
// total are numeric(14,2) and an empty string cast to numeric raises 22P02. The audit payload must still NAME the
// column, or the trail says an undo changed nothing.
func TestRLS_EditBySourceDocumentTxClearsAColumnToNull(t *testing.T) {
	for _, tc := range []struct {
		col   string
		seed  string
		clear func(*UpdateInput)
	}{
		{"buyer_tin", "31775208-0003", func(in *UpdateInput) { in.BuyerTIN = ClearText }},
		{"buyer_name", "Honeywell Group", func(in *UpdateInput) { in.BuyerName = ClearText }},
		{"currency", "NGN", func(in *UpdateInput) { in.Currency = ClearText }},
		{"subtotal", "950.00", func(in *UpdateInput) { in.Subtotal = ClearText }},
		{"vat", "75.00", func(in *UpdateInput) { in.VAT = ClearText }},
		{"total", "1500.00", func(in *UpdateInput) { in.Total = ClearText }},
		{"issue_date", "2026-03-01", func(in *UpdateInput) { in.IssueDate = ClearDate }},
	} {
		t.Run(tc.col, func(t *testing.T) {
			f := ebsSeed(t, "EBS-CLEAR-"+tc.col)
			if _, err := f.super.Exec(context.Background(),
				`UPDATE invoices SET `+tc.col+` = $1 WHERE id = $2`, tc.seed, f.invoiceID); err != nil {
				t.Fatalf("seed %s: %v", tc.col, err)
			}
			// The floor: the column really holds something, so the NULL below is a real clear
			// and not a column that was empty all along.
			if got := f.column(t, f.invoiceID, tc.col); got == nil {
				t.Fatalf("%s is already NULL before the clear -- every claim below is vacuous", tc.col)
			}

			var in UpdateInput
			tc.clear(&in)
			if _, err := f.edit(t, f.documentID, EditInput{UpdateInput: in}); err != nil {
				t.Fatalf("clearing %s: %v", tc.col, err)
			}

			if got := f.column(t, f.invoiceID, tc.col); got != nil {
				t.Errorf("invoices.%s = %q after the clear, want SQL NULL", tc.col, *got)
			}
			if fields := auditFields(t, f.app, f.tenantID, "invoice.updated"); !slices.Contains(fields, tc.col) {
				t.Errorf("the invoice.updated payload names %v, which omits %s -- the trail says the clear changed nothing", fields, tc.col)
			}
		})
	}
}

// --- lines-only edits (EXTR-13-04's AC-10 oracle, link 1 of 3: the demotion rule, proven
// through EditBySourceDocumentTx itself, not through Store.Edit) -----------------------------

// The line-items route posts EditInput{LineItems: &lines} with every header field nil. editTx
// does not special-case that shape (INVED-01-04's widened guard), so a lines-only edit must
// demote a validated invoice exactly like a header-only one does
// (TestRLS_EditBySourceDocumentTxDemotesAValidatedInvoiceToDraft) -- but that test never posts a
// line, so it cannot be cited as proof of THIS path.
func TestRLS_EditBySourceDocumentTxDemotesOnALinesOnlyEdit(t *testing.T) {
	f := ebsSeed(t, "EBS-07")
	if _, err := f.store.Transition(f.ctx, f.invoiceID, StatusValidated); err != nil {
		t.Fatalf("pre-hop Transition(-> validated): %v", err)
	}
	// The control: the fixture really reaches validated before the edit, or the demotion
	// asserted below proves nothing.
	if got := f.status(t, f.invoiceID); got != StatusValidated {
		t.Fatalf("control: invoices.status = %q after the pre-hop Transition, want %q", got, StatusValidated)
	}

	beforeHistory := mustCount(t, f.super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, f.invoiceID)

	desc1, qty1, price1, total1 := "Widget", "2", "10.00", "20.00"
	desc2, qty2, price2, total2 := "Gadget", "1", "5.00", "5.00"
	lines := []LineItemInput{
		{Description: &desc1, Quantity: &qty1, UnitPrice: &price1, LineTotal: &total1},
		{Description: &desc2, Quantity: &qty2, UnitPrice: &price2, LineTotal: &total2},
	}

	got, err := f.edit(t, f.documentID, EditInput{LineItems: &lines})
	if err != nil {
		t.Fatalf("a lines-only correction of a validated invoice: want success, got %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("the returned status = %q, want %q", got.Status, StatusDraft)
	}
	if stored := f.status(t, f.invoiceID); stored != StatusDraft {
		t.Errorf("invoices.status = %q, want %q -- a lines-only correction must demote too, not only a header one", stored, StatusDraft)
	}
	if n := mustCount(t, f.super,
		`SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = 'validated' AND to_status = 'draft'`,
		f.invoiceID); n != 1 {
		t.Errorf("%d (validated,draft) history row(s), want exactly 1", n)
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, f.invoiceID); n != beforeHistory+1 {
		t.Errorf("history rows = %d, want %d", n, beforeHistory+1)
	}

	stored := readLineItemsForTest(t, f.super, f.invoiceID)
	if len(stored) != 2 {
		t.Fatalf("%d line_items row(s) stored, want 2", len(stored))
	}
	if stored[0].LineNo != 1 || stored[1].LineNo != 2 {
		t.Errorf("line_no sequence = [%d,%d], want [1,2]", stored[0].LineNo, stored[1].LineNo)
	}
	if got := stored[0].Description; got == nil || *got != desc1 {
		t.Errorf("line 1 description = %s, want %q", ebsShow(got), desc1)
	}
	if got := stored[1].Description; got == nil || *got != desc2 {
		t.Errorf("line 2 description = %s, want %q", ebsShow(got), desc2)
	}
}

// A member holding a COPY of a sentinel is a caller who dereferenced and re-took an address.
// Pointer identity no longer matches, so without this guard the copy is written as its own
// CONTENTS. MEASURED with the guard removed: the two text-shaped arms raise 22021 (the NUL byte
// is not valid UTF8), which no sentinel maps and which surfaces as a 500, and the date arm
// commits a 1970 timestamp with no error at all. All three are wrong in a different way; the
// guard makes all three one ErrValidation the boundary can report.
func TestUpdateContentTx_RefusesACopiedClearSentinel(t *testing.T) {
	f := ebsSeed(t, "EBS-CLEAR-COPY")

	copied := *ClearText
	copiedDate := *ClearDate
	for _, tc := range []struct {
		col string
		in  UpdateInput
	}{
		{"buyer_tin", UpdateInput{BuyerTIN: &copied}},
		{"total", UpdateInput{Total: &copied}},
		{"issue_date", UpdateInput{IssueDate: &copiedDate}},
	} {
		t.Run(tc.col, func(t *testing.T) {
			before := f.column(t, f.invoiceID, tc.col)

			_, err := f.edit(t, f.documentID, EditInput{UpdateInput: tc.in})
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("a copied sentinel on %s: err = %v, want ErrValidation", tc.col, err)
			}
			if got := f.column(t, f.invoiceID, tc.col); !strPtrEqual(before, got) {
				t.Errorf("invoices.%s moved from %s to %s on a refused edit", tc.col, ebsShow(before), ebsShow(got))
			}
		})
	}

	// The control: the sentinel ITSELF still clears, so the guard above rejects the copy and
	// not the mechanism.
	if _, err := f.edit(t, f.documentID, EditInput{UpdateInput: UpdateInput{Total: ClearText}}); err != nil {
		t.Fatalf("the sentinel itself: %v", err)
	}
	if got := f.column(t, f.invoiceID, "total"); got != nil {
		t.Errorf("invoices.total = %q, want SQL NULL", *got)
	}
}
