package erp

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/submission"
	"github.com/SimonOsipov/invoice-os/internal/validation/codelist"
)

// seedCommercialInvoiceType gives SubmitWorker's invoice-type self-check its synced 381 row (CI's DB has no
// synced lists) and removes only what it inserted.
func seedCommercialInvoiceType(t *testing.T, super *pgxpool.Pool) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`INSERT INTO nrs_codes (list, code, entries) VALUES ('invoice-types', '381', '[{"code":"381","value":"Commercial Invoice"}]'::jsonb) ON CONFLICT DO NOTHING`)
	if err != nil {
		t.Fatalf("seed invoice-types 381: %v", err)
	}
	if tag.RowsAffected() == 1 {
		t.Cleanup(func() {
			_, _ = super.Exec(context.Background(), `DELETE FROM nrs_codes WHERE list = 'invoice-types' AND code = '381'`)
		})
	}
}

// mockWriteBack drives a pulled fake invoice with the given buyer TIN through the real SubmitWorker
// (mock adapter, invoice store, the enqueuer as sink), then runs the enqueued job.
func mockWriteBack(t *testing.T, tin string) (e *ingestEnv, invoiceID string) {
	t.Helper()
	e = newIngest(t)
	seedCommercialInvoiceType(t, e.super)
	e.mustWork(docOne, "1")
	d := e.wantState(docOne, "drafted")
	invoiceID = *d.InvoiceID
	mustExec(t, e.super, "UPDATE invoices SET buyer_tin = $2, status = 'queued' WHERE id = $1", invoiceID, tin)

	q := realQueue(t, e.app)
	w := &submission.SubmitWorker{
		InvoiceTypeLabels: codelist.LabelsTx,
		Pool:              e.app,
		Adapter:           submission.NewMockAdapter(submission.MockConfig{}),
		InvoicePort:       invoice.NewStore(e.app),
		Limiter:           submission.NewRateLimiter(),
		RateLimit:         1000,
		Queue:             q,
		Results:           &WriteBackEnqueuer{Queue: q},
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	args := submission.SubmitArgs{TenantID: e.tenant, InvoiceID: invoiceID, IdempotencyKey: "req-" + uuid.NewString() + ":" + invoiceID}
	job := &river.Job[submission.SubmitArgs]{JobRow: &rivertype.JobRow{ID: 1, Attempt: 1, MaxAttempts: 8}, Args: args}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := w.Work(ctx, job); err != nil {
		t.Fatalf("SubmitWorker.Work: %v", err)
	}

	jobs, _ := wbJobs(t, e.super, e.tenant)
	if len(jobs) != 1 {
		t.Fatalf("erp.writeback jobs = %d, want 1", len(jobs))
	}
	wbw := &WriteBackWorker{Pool: e.app, Registry: e.reg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	wj := &river.Job[WriteBackArgs]{JobRow: &rivertype.JobRow{ID: 2, Attempt: 1, MaxAttempts: 8}, Args: jobs[0]}
	if err := wbw.Work(ctx, wj); err != nil {
		t.Fatalf("WriteBackWorker.Work: %v", err)
	}
	return e, invoiceID
}

func TestWriteBack_MockIRNAppearsOnTheFakeDocument(t *testing.T) {
	e, inv := mockWriteBack(t, "99999999-0001")
	var status, irn string
	mustScan(t, e.super, "SELECT status, coalesce(irn, '') FROM invoices WHERE id = $1", []any{inv}, &status, &irn)
	if status != "accepted" || irn == "" {
		t.Fatalf("invoice = %s/%q, want accepted with an IRN", status, irn)
	}
	wb, ok := e.script.Connector.(*Fake).LastWriteBack(keyOne, docOne)
	if !ok {
		t.Fatal("the fake document has no write-back")
	}
	if wb.IRN != irn || !regexp.MustCompile(`^[A-Z0-9-]+-FBMOCK01-[0-9]{8}$`).MatchString(wb.IRN) {
		t.Errorf("written IRN = %q, want the invoice's %q in the mock format", wb.IRN, irn)
	}
	if !strings.Contains(wb.Note, irn) {
		t.Errorf("note = %q, want it to hold the IRN", wb.Note)
	}
}

func TestWriteBack_MockRejectionReasonAppearsOnTheFakeDocument(t *testing.T) {
	e, inv := mockWriteBack(t, "99999999-0002")
	var status, code string
	mustScan(t, e.super, "SELECT status, rejection_reasons->0->>'code' FROM invoices WHERE id = $1", []any{inv}, &status, &code)
	if status != "rejected" || code == "" {
		t.Fatalf("invoice = %s, first reason code %q, want rejected with a reason", status, code)
	}
	wb, ok := e.script.Connector.(*Fake).LastWriteBack(keyOne, docOne)
	if !ok {
		t.Fatal("the fake document has no write-back")
	}
	if wb.IRN != "" || len(wb.Fields) != 1 || wb.Fields[0] != "rejected" {
		t.Errorf("write-back = %+v, want no IRN and field [rejected]", wb)
	}
	if !strings.HasPrefix(wb.Note, "NRS rejected: ") || !strings.Contains(wb.Note, code) {
		t.Errorf("note = %q, want \"NRS rejected: \" and the code %s", wb.Note, code)
	}
}
