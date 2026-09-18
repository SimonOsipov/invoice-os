// ai_db_test.go: AIR-03-03's end-to-end AI seam -- an AI-only invoice number that files a
// draft, and an AI value that fails a check and never reaches the invoice.
package endtoend

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/importer"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// eeNoInvoiceNumberMessage is internal/importer's noInvoiceNumberMessage, unexported there and
// reproduced here rather than imported (the importer's literal-scan test reads that package
// alone).
const eeNoInvoiceNumberMessage = "This document was read, but no invoice number was found on it. Enter this invoice manually to carry on."

// eeAIUnavailableMessage is internal/importer's aiUnavailableMessage, reproduced (unexported there).
const eeAIUnavailableMessage = "AI reading was unavailable when this document was imported, so no invoice fields were taken from it. Enter this invoice manually to carry on."

// eeAI is a fixed-answer AIReader stub: no network, always the same answer or the same error.
type eeAI struct {
	enabled bool
	answer  map[string]any
	err     error
}

func (a *eeAI) Enabled() bool { return a.enabled }

func (a *eeAI) Call(context.Context, ai.Request) (map[string]any, error) {
	return a.answer, a.err
}

// eeWithAI swaps the worker's AI seam.
func eeWithAI(r extraction.AIReader) eeOpt {
	return func(ew *extraction.ExtractWorker) { ew.AI = r }
}

// aiWireToken/aiWirePage mirror the docling wire response's own JSON shape, reproduced here so a
// spec can hand-build the exact tokens askAI/checkAI read, rather than replaying a fixed golden.
type aiWireToken struct {
	Text string  `json:"text"`
	X0   float64 `json:"x0"`
	Y0   float64 `json:"y0"`
	X1   float64 `json:"x1"`
	Y1   float64 `json:"y1"`
}

type aiWirePage struct {
	Number   int           `json:"number"`
	WidthPt  float64       `json:"width_pt"`
	HeightPt float64       `json:"height_pt"`
	Tables   []any         `json:"tables"`
	Tokens   []aiWireToken `json:"tokens"`
}

// aiTok is one token on aiWireBody's one page, left to right at the given y.
func aiTok(text string, x0, y0, x1, y1 float64) aiWireToken {
	return aiWireToken{Text: text, X0: x0, Y0: y0, X1: x1, Y1: y1}
}

// aiWireBody marshals one page carrying tokens into the docling wire response eeReplayReader
// takes.
func aiWireBody(t *testing.T, tokens ...aiWireToken) []byte {
	t.Helper()
	body, err := json.Marshal(struct {
		Pages []aiWirePage `json:"pages"`
	}{Pages: []aiWirePage{{Number: 1, WidthPt: 612, HeightPt: 792, Tables: []any{}, Tokens: tokens}}})
	if err != nil {
		t.Fatalf("marshal docling wire body: %v", err)
	}
	return body
}

// AC-6. An invoice_number the engine cannot read (a bare digit string, rejected as an amount)
// but the AI answers and the label check (d) admits: the AI-on run files a draft; the AI-nil
// control quarantines with the same message a human sees today.
func TestRLS_EndToEndAnAIOnlyInvoiceNumberFilesADraft(t *testing.T) {
	ctx := t.Context()
	eeRequireFixtures(t, []string{eeLineFixture})
	body := aiWireBody(t, aiTok("Invoice Number: 20417", 0.10, 0.10, 0.40, 0.12))

	t.Run("AI on", func(t *testing.T) {
		w := eeSeed(t, ctx, eeLineFixture)
		stub := &eeAI{enabled: true, answer: map[string]any{"invoice_number": "20417"}}
		eeExtract(t, ctx, w, eeLineFixture, eeWithText(eeReplayReader(t, body)), eeWithAI(stub))
		res := eeImport(t, ctx, w)

		if res.QuarantinedInvoices != 0 {
			t.Fatalf("ImportDocument reported QuarantinedInvoices = %d, want 0: %v", res.QuarantinedInvoices, res.Errors)
		}
		got := eeInvoices(t, ctx, w.entityID)
		if len(got) != 1 {
			t.Fatalf("entity %s holds %d invoices row(s), want exactly 1", w.entityID, len(got))
		}
		if got[0].number != "20417" {
			t.Errorf("invoice_number = %q, want %q", got[0].number, "20417")
		}
	})

	t.Run("AI nil control", func(t *testing.T) {
		w := eeSeed(t, ctx, eeLineFixture)
		eeExtract(t, ctx, w, eeLineFixture, eeWithText(eeReplayReader(t, body)))
		res := eeImport(t, ctx, w)

		if res.QuarantinedInvoices != 1 {
			t.Fatalf("ImportDocument reported QuarantinedInvoices = %d, want 1", res.QuarantinedInvoices)
		}
		if len(res.Errors) == 0 || res.Errors[0].Field != "invoice_number" || res.Errors[0].Message != eeNoInvoiceNumberMessage {
			t.Errorf("ImportDocument reported errors %v, want field invoice_number and message %q", res.Errors, eeNoInvoiceNumberMessage)
		}
	})
}

// AC-5. buyer_name's AI reading sits behind a payment-account label ("ACCOUNT NAME"), so check
// (c) refuses it: the invoice's own buyer_name stays NULL and extraction_field_results carries
// the failed value at rank 1, never rank 0.
func TestRLS_EndToEndAFailedAIValueNeverReachesTheInvoice(t *testing.T) {
	ctx := t.Context()
	eeRequireFixtures(t, []string{eeLineFixture})
	body := aiWireBody(t,
		aiTok("Invoice Number: 20417", 0.10, 0.10, 0.40, 0.12),
		aiTok("ACCOUNT NAME", 0.10, 0.20, 0.30, 0.22),
		aiTok("ZENITH HOLDINGS LIMITED", 0.32, 0.20, 0.70, 0.22),
	)

	w := eeSeed(t, ctx, eeLineFixture)
	stub := &eeAI{enabled: true, answer: map[string]any{
		"invoice_number": "20417",
		"buyer_name":     "ZENITH HOLDINGS LIMITED",
	}}
	jobID := eeExtract(t, ctx, w, eeLineFixture, eeWithText(eeReplayReader(t, body)), eeWithAI(stub))
	res := eeImport(t, ctx, w)
	if res.QuarantinedInvoices != 0 {
		t.Fatalf("ImportDocument reported QuarantinedInvoices = %d, want 0: %v", res.QuarantinedInvoices, res.Errors)
	}

	var buyerName *string
	if err := eeRequire(t).super.QueryRow(ctx,
		`SELECT buyer_name FROM invoices WHERE source_document_id = $1`, w.documentID).Scan(&buyerName); err != nil {
		t.Fatalf("read buyer_name for document %s: %v", w.documentID, err)
	}
	if buyerName != nil {
		t.Errorf("invoices.buyer_name = %q, want NULL -- a value that failed a check must not reach the invoice", *buyerName)
	}

	var rank0, rank1 *eeRow
	for _, r := range eeFieldResults(t, ctx, jobID) {
		r := r
		if r.name != "buyer_name" {
			continue
		}
		switch r.rank {
		case 0:
			rank0 = &r
		case 1:
			rank1 = &r
		}
	}
	if rank0 == nil {
		t.Fatal("no rank-0 buyer_name row")
	}
	if rank0.value != nil {
		t.Errorf("buyer_name rank-0 value = %q, want NULL", *rank0.value)
	}
	if rank0.reason == nil || *rank0.reason != "unreadable" {
		t.Errorf("buyer_name rank-0 reason_code = %v, want %q", eeReasonStr(rank0.reason), "unreadable")
	}
	if rank1 == nil {
		t.Fatal("no rank-1 buyer_name row")
	}
	if rank1.value == nil || *rank1.value != "ZENITH HOLDINGS LIMITED" {
		t.Errorf("buyer_name rank-1 value = %v, want %q", eeReasonStr(rank1.value), "ZENITH HOLDINGS LIMITED")
	}
}

// eeReasonStr renders a nullable column for an error message.
func eeReasonStr(p *string) string {
	if p == nil {
		return "NULL"
	}
	return *p
}

// AC 1/2/3 (AIR-04-02). A document the engine and a blank AI would both file on their own
// prints reads: an unavailable AI call quarantines it under the AI's own sentence and carries
// no reading forward; the blank-AI control on the same document files a draft.
func TestRLS_EndToEndAnUnavailableAIQuarantinesTheDocument(t *testing.T) {
	ctx := t.Context()
	eeRequireFixtures(t, []string{eeLineFixture})
	body := aiWireBody(t,
		aiTok("Invoice Number: INV-4410", 0.10, 0.10, 0.45, 0.12),
		aiTok("Invoice Date: 2026-07-14", 0.10, 0.14, 0.45, 0.16),
		aiTok("Total: 2,150.00", 0.10, 0.18, 0.45, 0.20),
	)

	t.Run("AI unavailable", func(t *testing.T) {
		h := eeRequire(t)
		w := eeSeed(t, ctx, eeLineFixture)
		stub := &eeAI{enabled: true, err: ai.ErrUnavailable}
		eeExtract(t, ctx, w, eeLineFixture, eeWithText(eeReplayReader(t, body)), eeWithAI(stub))
		res := eeImport(t, ctx, w)

		if res.RowsInvalid != 1 {
			t.Fatalf("RowsInvalid = %d, want 1: %+v", res.RowsInvalid, res)
		}
		if res.QuarantinedInvoices != 1 {
			t.Errorf("QuarantinedInvoices = %d, want 1", res.QuarantinedInvoices)
		}
		if len(res.Errors) != 1 || res.Errors[0].Field != "invoice_number" || res.Errors[0].Message != eeAIUnavailableMessage {
			t.Errorf("Errors = %+v, want exactly one {Field: invoice_number, Message: %q}", res.Errors, eeAIUnavailableMessage)
		}

		if got := eeInvoices(t, ctx, w.entityID); len(got) != 0 {
			t.Errorf("entity %s holds %d invoice(s), want 0 -- an unavailable AI call must not file a draft", w.entityID, len(got))
		}

		svc := importer.NewService(importer.NewStore(h.app), invoice.NewStore(h.app), eeGate{})
		rctx := auth.WithIdentity(ctx, auth.Identity{Subject: w.subject, Role: "authenticated", TenantID: w.tenantID})
		reading, err := svc.CarriedReading(rctx, w.documentID)
		if err != nil || reading != nil {
			t.Errorf("CarriedReading = (%v, %v), want (nil, nil)", reading, err)
		}
	})

	t.Run("AI blank control", func(t *testing.T) {
		w := eeSeed(t, ctx, eeLineFixture)
		stub := &eeAI{enabled: true}
		eeExtract(t, ctx, w, eeLineFixture, eeWithText(eeReplayReader(t, body)), eeWithAI(stub))
		res := eeImport(t, ctx, w)

		if res.ReadyInvoices != 1 {
			t.Fatalf("ReadyInvoices = %d, want 1: %+v", res.ReadyInvoices, res)
		}
		got := eeInvoices(t, ctx, w.entityID)
		if len(got) != 1 {
			t.Fatalf("entity %s holds %d invoice(s), want exactly 1", w.entityID, len(got))
		}
		if got[0].number != "INV-4410" {
			t.Errorf("invoice_number = %q, want %q", got[0].number, "INV-4410")
		}
	})
}

// --- AIR-04-02 T09 -- GUARD -----------------------------------------------------------------

// AC-6. With no AI configured, the import behaves exactly as today: no document_ai_reading row,
// and the document files under its own printed number.
func TestRLS_EndToEndNoKeyWritesNoMarker(t *testing.T) {
	ctx := t.Context()
	eeRequireFixtures(t, []string{eeLineFixture})
	body := aiWireBody(t,
		aiTok("Invoice Number: INV-4410", 0.10, 0.10, 0.45, 0.12),
		aiTok("Invoice Date: 2026-07-14", 0.10, 0.14, 0.45, 0.16),
		aiTok("Total: 2,150.00", 0.10, 0.18, 0.45, 0.20),
	)

	w := eeSeed(t, ctx, eeLineFixture)
	jobID := eeExtract(t, ctx, w, eeLineFixture, eeWithText(eeReplayReader(t, body)))
	res := eeImport(t, ctx, w)

	rows := eeFieldResults(t, ctx, jobID)
	if len(rows) == 0 {
		t.Fatal("eeFieldResults returned 0 rows; the absence check below would pass vacuously")
	}
	for _, r := range rows {
		if r.name == "document_ai_reading" {
			t.Fatalf("field_results carries a document_ai_reading row %+v; the AI is nil, so no marker should ever be written", r)
		}
	}

	if res.ReadyInvoices != 1 {
		t.Fatalf("ReadyInvoices = %d, want 1: %+v", res.ReadyInvoices, res)
	}
	got := eeInvoices(t, ctx, w.entityID)
	if len(got) != 1 {
		t.Fatalf("entity %s holds %d invoice(s), want exactly 1", w.entityID, len(got))
	}
	if got[0].number != "INV-4410" {
		t.Errorf("invoice_number = %q, want %q", got[0].number, "INV-4410")
	}
}
