// images_db_test.go: AIR-05-03 T13-T17 -- the no-text image arm, real end to end: a document
// with no text layer in, an invoices row (or a quarantine reading) out.
package endtoend

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/importer"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// eeScannedFixture has no text layer: PDFium reads TextChars 0 on it (confirmed on production,
// AIR-05-01), so the worker takes the no-text arm eeWorker's Text reader is real PDFium over.
const eeScannedFixture = "scanned_invoice.pdf"

// eeImgBucket is a map-backed PageSink-and-PageObject pair: put stores a rendered page's bytes
// by key, object reads them back. The image arm's storage round trip, real end to end -- what
// the render PUTs is what the AI call reads.
type eeImgBucket struct {
	mu     sync.Mutex
	bodies map[string][]byte
}

func (b *eeImgBucket) put(_ context.Context, key string, body []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.bodies == nil {
		b.bodies = map[string][]byte{}
	}
	b.bodies[key] = append([]byte(nil), body...)
	return nil
}

func (b *eeImgBucket) object(_ context.Context, key string) (io.ReadCloser, int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	body := b.bodies[key]
	return io.NopCloser(bytes.NewReader(body)), int64(len(body)), nil
}

// eeWithPageBucket wires both halves of the round trip to one bucket.
func eeWithPageBucket() eeOpt {
	b := &eeImgBucket{}
	return func(ew *extraction.ExtractWorker) {
		ew.Pages.Sink = b.put
		ew.PageBytes = b.object
	}
}

// eeCarriedReading reads back the no-number reading ImportDocument stored for w's document, in
// the request-tenant posture.
func eeCarriedReading(t *testing.T, ctx context.Context, w eeWorld) *importer.CarriedReading {
	t.Helper()
	h := eeRequire(t)
	svc := importer.NewService(importer.NewStore(h.app), invoice.NewStore(h.app), eeGate{})
	rctx := auth.WithIdentity(ctx, auth.Identity{Subject: w.subject, Role: "authenticated", TenantID: w.tenantID})
	reading, err := svc.CarriedReading(rctx, w.documentID)
	if err != nil {
		t.Fatalf("CarriedReading(%s): %v", w.documentID, err)
	}
	return reading
}

// eeInvoiceTotal reads one invoice's total column directly; eeInvoice carries no total field.
func eeInvoiceTotal(t *testing.T, ctx context.Context, invoiceID string) *string {
	t.Helper()
	var total *string
	if err := eeRequire(t).super.QueryRow(ctx,
		`SELECT total FROM invoices WHERE id = $1`, invoiceID).Scan(&total); err != nil {
		t.Fatalf("read total for invoice %s: %v", invoiceID, err)
	}
	return total
}

// T13. AC-5: an image-read invoice number files a draft; with the AI absent the document
// quarantines exactly as it does today (the poor-scan message, no AI ever reached).
func TestRLS_EndToEndAnImageReadInvoiceNumberFilesADraft(t *testing.T) {
	ctx := t.Context()
	eeRequireFixtures(t, []string{eeScannedFixture})

	t.Run("AI on", func(t *testing.T) {
		w := eeSeed(t, ctx, eeScannedFixture)
		stub := &eeAI{enabled: true, answer: map[string]any{"invoice_number": "INV-5520", "total": "1935.00"}}
		eeExtract(t, ctx, w, eeScannedFixture, eeWithPageBucket(), eeWithAI(stub))
		res := eeImport(t, ctx, w)

		if res.ReadyInvoices != 1 {
			t.Fatalf("ReadyInvoices = %d, want 1: %+v", res.ReadyInvoices, res)
		}
		got := eeInvoices(t, ctx, w.entityID)
		if len(got) != 1 {
			t.Fatalf("entity %s holds %d invoice(s), want exactly 1", w.entityID, len(got))
		}
		if got[0].number != "INV-5520" {
			t.Errorf("invoice_number = %q, want %q", got[0].number, "INV-5520")
		}
		if total := eeInvoiceTotal(t, ctx, got[0].id); total == nil || *total != "1935.00" {
			t.Errorf("total = %v, want %q", total, "1935.00")
		}
		if got[0].sourceDocumentID == nil {
			t.Errorf("source_document_id is NULL, want %s", w.documentID)
		} else if *got[0].sourceDocumentID != w.documentID {
			t.Errorf("source_document_id = %q, want %q", *got[0].sourceDocumentID, w.documentID)
		}
	})

	t.Run("AI nil control", func(t *testing.T) {
		w := eeSeed(t, ctx, eeScannedFixture)
		eeExtract(t, ctx, w, eeScannedFixture, eeWithPageBucket())
		res := eeImport(t, ctx, w)

		if res.RowsInvalid != 1 {
			t.Fatalf("RowsInvalid = %d, want 1: %+v", res.RowsInvalid, res)
		}
		if len(res.Errors) == 0 || res.Errors[0].Field != "invoice_number" || res.Errors[0].Message != eePoorScanMessage {
			t.Errorf("Errors = %+v, want field invoice_number and message %q", res.Errors, eePoorScanMessage)
		}
	})
}

// T14. AC-5: an image read with no invoice number quarantines under today's no-number message
// and carries the rest of its reading forward for manual entry.
func TestRLS_EndToEndAnImageReadWithNoNumberQuarantinesAsNoNumber(t *testing.T) {
	ctx := t.Context()
	eeRequireFixtures(t, []string{eeScannedFixture})
	w := eeSeed(t, ctx, eeScannedFixture)
	stub := &eeAI{enabled: true, answer: map[string]any{"total": "1935.00"}}
	eeExtract(t, ctx, w, eeScannedFixture, eeWithPageBucket(), eeWithAI(stub))
	res := eeImport(t, ctx, w)

	if len(res.Errors) == 0 || res.Errors[0].Field != "invoice_number" || res.Errors[0].Message != eeNoInvoiceNumberMessage {
		t.Fatalf("Errors = %+v, want field invoice_number and message %q", res.Errors, eeNoInvoiceNumberMessage)
	}
	if got := eeInvoices(t, ctx, w.entityID); len(got) != 0 {
		t.Errorf("entity %s holds %d invoice(s), want 0", w.entityID, len(got))
	}
	reading := eeCarriedReading(t, ctx, w)
	if reading == nil {
		t.Fatal("CarriedReading = nil, want a carried total")
	}
	if reading.Total == nil || *reading.Total != "1935.00" {
		t.Errorf("CarriedReading.Total = %v, want %q", reading.Total, "1935.00")
	}
}

// T15. AC-5: a blank image answer stays a poor scan -- no engine text and no decided AI value
// leaves the worker's original text-layer verdict standing.
func TestRLS_EndToEndABlankImageReadStaysAPoorScan(t *testing.T) {
	ctx := t.Context()
	eeRequireFixtures(t, []string{eeScannedFixture})
	w := eeSeed(t, ctx, eeScannedFixture)
	stub := &eeAI{enabled: true, answer: map[string]any{}}
	eeExtract(t, ctx, w, eeScannedFixture, eeWithPageBucket(), eeWithAI(stub))
	res := eeImport(t, ctx, w)

	if res.ReadyInvoices != 0 {
		t.Errorf("ReadyInvoices = %d, want 0", res.ReadyInvoices)
	}
	if res.RowsInvalid != 1 {
		t.Errorf("RowsInvalid = %d, want 1", res.RowsInvalid)
	}
	if res.QuarantinedInvoices != 1 {
		t.Errorf("QuarantinedInvoices = %d, want 1", res.QuarantinedInvoices)
	}
	if len(res.Errors) != 1 || res.Errors[0].Field != "invoice_number" || res.Errors[0].Message != eePoorScanMessage {
		t.Fatalf("Errors = %+v, want exactly [{invoice_number, %q}]", res.Errors, eePoorScanMessage)
	}
	if got := eeInvoices(t, ctx, w.entityID); len(got) != 0 {
		t.Errorf("entity %s holds %d invoice(s), want 0", w.entityID, len(got))
	}
	if reading := eeCarriedReading(t, ctx, w); reading != nil {
		t.Errorf("CarriedReading = %+v, want nil", reading)
	}
}

// T16. AC-6: an unavailable image read goes to manual entry under the AI's own sentence, never
// the poor-scan one, and carries no reading forward.
func TestRLS_EndToEndAnUnavailableImageReadGoesToManualEntry(t *testing.T) {
	ctx := t.Context()
	eeRequireFixtures(t, []string{eeScannedFixture})
	w := eeSeed(t, ctx, eeScannedFixture)
	stub := &eeAI{enabled: true, err: ai.ErrUnavailable}
	eeExtract(t, ctx, w, eeScannedFixture, eeWithPageBucket(), eeWithAI(stub))
	res := eeImport(t, ctx, w)

	if len(res.Errors) == 0 {
		t.Fatalf("Errors = %+v, want at least one", res.Errors)
	}
	if res.Errors[0].Message != eeAIUnavailableMessage {
		t.Errorf("Errors[0].Message = %q, want %q", res.Errors[0].Message, eeAIUnavailableMessage)
	}
	if reading := eeCarriedReading(t, ctx, w); reading != nil {
		t.Errorf("CarriedReading = %+v, want nil", reading)
	}
}

// T17. AC-4: a value the format-only reading fails (buyer_tin's digit count) never reaches the
// invoice; the draft still files on the invoice number alone.
func TestRLS_EndToEndAFailedImageValueNeverReachesTheInvoice(t *testing.T) {
	ctx := t.Context()
	eeRequireFixtures(t, []string{eeScannedFixture})
	w := eeSeed(t, ctx, eeScannedFixture)
	stub := &eeAI{enabled: true, answer: map[string]any{"invoice_number": "INV-5520", "buyer_tin": "9999999-1202"}}
	eeExtract(t, ctx, w, eeScannedFixture, eeWithPageBucket(), eeWithAI(stub))
	res := eeImport(t, ctx, w)

	if res.ReadyInvoices != 1 {
		t.Fatalf("ReadyInvoices = %d, want 1: %+v", res.ReadyInvoices, res)
	}
	got := eeInvoices(t, ctx, w.entityID)
	if len(got) != 1 {
		t.Fatalf("entity %s holds %d invoice(s), want exactly 1", w.entityID, len(got))
	}
	var buyerTIN *string
	if err := eeRequire(t).super.QueryRow(ctx,
		`SELECT buyer_tin FROM invoices WHERE id = $1`, got[0].id).Scan(&buyerTIN); err != nil {
		t.Fatalf("read buyer_tin for invoice %s: %v", got[0].id, err)
	}
	if buyerTIN != nil {
		t.Errorf("invoices.buyer_tin = %q, want NULL -- a value the format-only reading failed must not reach the invoice", *buyerTIN)
	}
}
