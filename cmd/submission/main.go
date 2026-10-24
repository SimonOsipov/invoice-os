// Command submission is the 05 Submission context service: the extraction review routes, the
// source-document upload, the staff jobs and health reads. It enqueues on an insert-only River
// client; cmd/worker runs every job.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/docadapter"
	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
	"github.com/SimonOsipov/invoice-os/internal/staffjobs"
	"github.com/SimonOsipov/invoice-os/internal/systemhealth"
)

func main() {
	defer platform.ReportBootPanic()

	app, err := platform.New("submission")
	if err != nil {
		platform.Fatal(slog.Default(), "submission: startup: %v", err)
	}

	gw, err := systemhealth.GatewayURL(os.Getenv("GATEWAY_URL"))
	if err != nil {
		platform.Fatal(app.Logger, "submission: %v", err)
	}

	ctx := context.Background()

	// Connect as the app role (invoice_app, NOBYPASSRLS) — never the migrator or
	// superuser (.claude/rules/db-migrations.md).
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		// pgx would otherwise build a config from ambient libpq env/defaults for an empty
		// DSN — fail fast so this service can only ever connect as its configured app role.
		platform.Fatal(app.Logger, "submission: DATABASE_URL is required")
	}
	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		platform.Fatal(app.Logger, "submission: db pool: %v", err)
	}
	defer pool.Close()

	// /readyz reflects the DB dependency.
	app.Ready("database", pool.Ping)

	// The five DOCUMENT_* variables are required unconditionally: the upload and page routes
	// read the store (TestSubmissionMain_FatalOnDocumentConfigError).
	docCfg, err := document.ConfigFromEnv()
	if err != nil {
		platform.Fatal(app.Logger, "submission: document config: %v", err)
	}
	docObjects, err := document.NewS3Store(docCfg, nil)
	if err != nil {
		platform.Fatal(app.Logger, "submission: document object store: %v", err)
	}

	invStore := invoice.NewStore(pool)
	docSvc := document.NewService(document.NewStore(pool), docObjects)

	// Insert-only: the worker (cmd/worker) fetches and runs every job this service enqueues.
	q, err := queue.New(pool, queue.Config{})
	if err != nil {
		platform.Fatal(app.Logger, "submission: queue: %v", err)
	}

	healthStore := &systemhealth.Store{Pool: pool}
	fleet := systemhealth.NewFleetReader(gw, &http.Client{Transport: platform.TraceTransport(nil)}, mustEnv("GATEWAY_TOKEN"))
	app.Mux.HandleFunc("GET /v1/staff/health", systemhealth.Handler(healthStore.StaffSnapshot, fleet, time.Now, app.Logger))
	metricsSource := systemhealth.NewMetricsSource(systemhealth.NewGoogle(&http.Client{}), time.Now, app.Logger)
	app.Mux.HandleFunc("GET /v1/staff/metrics", systemhealth.MetricsHandler(healthStore.StaffCheck, metricsSource.Get, app.Logger))

	// Stub endpoint — proves routing end to end.
	app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"submission","status":"ok"}`))
	})

	// GET /v1/extractions and GET /v1/extractions/{id} -- reached as
	// /api/submission/v1/extractions…: the gateway routes on the first segment under /api/ and
	// forwards the subpath, so the patterns have no prefix. One reader serves both; only Detail
	// audits (TestSubmissionMain_WiresTheDocumentReadAuditorOntoAReader).
	reader := &extraction.Reader{Pool: pool, Audit: newDocumentReadAuditor()}
	app.Mux.HandleFunc("GET /v1/extractions", extraction.JobsHandler(reader.JobsForDocument, app.Logger))
	app.Mux.HandleFunc("GET /v1/extractions/{id}", extraction.DetailHandler(reader.Detail, app.Logger))

	// GET /v1/extractions/{id}/pages/{n} streams one rendered page from the same bucket
	// the worker's docadapter.PageSink writes to. It audits nothing: one open screen owes one document.read row,
	// from the detail route above, not one per page.
	app.Mux.HandleFunc("GET /v1/extractions/{id}/pages/{n}",
		extraction.PageImageHandler(reader.PageImageKey, docadapter.PageObjectReader(docObjects), app.Logger))

	// POST /v1/extractions/{id}/fields/{name}/corrections -- one transaction that appends the
	// correction row, writes the value onto the invoice filed from the job's document, and
	// audits the pair (TestSubmissionMain_WiresTheCorrectionRouteAndItsCollaborators).
	app.Mux.HandleFunc("POST /v1/extractions/{id}/fields/{name}/corrections",
		extraction.CorrectionHandler(pool, newInvoiceFieldApplier(invStore.EditBySourceDocumentTx),
			newFieldCorrectedAuditor(), newAnchorLearnedAuditor(),
			extraction.PageOneReader(docadapter.Opener(docSvc.Open), extraction.NewPDFiumReader()), app.Logger))

	// POST /v1/extractions/{id}/line-items -- the same transaction shape as the correction
	// route, replacing the invoice's whole line set
	// (TestSubmissionMain_WiresTheLineItemsRouteAndItsCollaborators).
	app.Mux.HandleFunc("POST /v1/extractions/{id}/line-items",
		extraction.LineItemsHandler(pool, newInvoiceLineItemsApplier(invStore.EditBySourceDocumentTx),
			newFieldCorrectedAuditor(), app.Logger))

	// POST /v1/documents -- the upload that stores a source document and queues its
	// extraction. Two transactions on purpose: Service.Store commits its own, the enqueue
	// opens a second. A crash between them leaves a document with no job, which is the safe
	// direction; recovery is EXTR-17's (internal/extraction/enqueue.go).
	app.Mux.HandleFunc("POST /v1/documents", extraction.UploadHandler(
		newDocumentStorer(docSvc.Store), newExtractionEnqueuer(pool, q), app.Logger))

	// Staff Submissions reads, reached as /api/submission/v1/staff/jobs…; the staff class gates the path.
	jobs := staffjobs.NewStore(pool, invStore, q.River())
	app.Mux.HandleFunc("GET /v1/staff/jobs", staffjobs.ListHandler(jobs.List, app.Logger))
	app.Mux.HandleFunc("GET /v1/staff/jobs/{id}", staffjobs.GetHandler(jobs.Get, app.Logger))
	app.Mux.HandleFunc("POST /v1/staff/jobs/{id}/redrive", staffjobs.RedriveHandler(jobs.Redrive, app.Logger))
	app.Mux.HandleFunc("POST /v1/staff/jobs/{id}/cancel", staffjobs.CancelHandler(jobs.Cancel, app.Logger))

	app.RequireGateway(mustEnv("GATEWAY_TOKEN"))

	if err := app.Run(ctx); err != nil {
		platform.Fatal(app.Logger, "submission: %v", err)
	}
}

// documentStore is document.Service.Store's shape, so a test can substitute one.
type documentStore func(ctx context.Context, filename, contentType string, size int64, body io.ReadSeeker) (document.Document, bool, error)

// newDocumentStorer adapts the document service to the upload seam: internal/extraction never
// imports internal/document, so the stored row is projected into its own shape here, the way
// docadapter.Opener projects an opened one. Every field comes off the STORED row -- filename
// and size are the sanitized, server-computed values, not what the caller declared.
func newDocumentStorer(store documentStore) func(ctx context.Context, filename, contentType string, size int64, body io.ReadSeeker) (extraction.StoredDocument, error) {
	return func(ctx context.Context, filename, contentType string, size int64, body io.ReadSeeker) (extraction.StoredDocument, error) {
		doc, reused, err := store(ctx, filename, contentType, size, body)
		if err != nil {
			return extraction.StoredDocument{}, err
		}
		out := extraction.StoredDocument{ID: doc.ID, SizeBytes: doc.SizeBytes, Reused: reused}
		if doc.Filename != nil {
			out.Filename = *doc.Filename
		}
		if doc.DeclaredContentType != nil {
			out.ContentType = *doc.DeclaredContentType
		}
		return out, nil
	}
}

// newExtractionEnqueuer adapts the sanctioned enqueue seam to the upload handler. The tenant
// comes off the request identity, never the wire, and the business key and the job insert
// share one transaction (internal/extraction/enqueue.go).
func newExtractionEnqueuer(pool *pgxpool.Pool, q *queue.Client) func(ctx context.Context, documentID string) (bool, error) {
	return func(ctx context.Context, documentID string) (bool, error) {
		id, ok := auth.IdentityFromContext(ctx)
		if !ok {
			return false, db.ErrNoTenant
		}
		var skipped bool
		err := db.WithinRequestTenantTx(ctx, pool, func(tx pgx.Tx) error {
			var e error
			skipped, e = extraction.EnqueueExtraction(ctx, tx, q, id.TenantID, documentID)
			return e
		})
		return skipped, err
	}
}

// newDocumentReadAuditor adapts the audit module to the detail seam. The event name is spelled
// here rather than in internal/extraction: a const identifier there reads as a non-literal to
// internal/platform/db's audit.Record scan and lands the site in no bucket
// (TestNewDocumentReadAuditor_SpellsTheEventInCmd).
func newDocumentReadAuditor() extraction.RecordDocumentRead {
	return func(ctx context.Context, tx pgx.Tx, subject, documentID string) error {
		return audit.Record(ctx, tx, subject, "document.read", map[string]any{"id": documentID})
	}
}

// invoiceFieldEdit is (*invoice.Store).EditBySourceDocumentTx's shape, so a test can
// substitute one -- the documentOpen idiom.
type invoiceFieldEdit func(ctx context.Context, tx pgx.Tx, documentID string, in invoice.EditInput) (invoice.Invoice, error)

// newFieldCorrectedAuditor adapts the audit module to the correction seam. The event name is
// spelled here rather than in internal/extraction, for newDocumentReadAuditor's reason. No
// value and no region: audit_log is append-only and the corrected value is business content
// (TestNewFieldCorrectedAuditor_WritesTheInvoiceIdPayload).
func newFieldCorrectedAuditor() extraction.RecordFieldCorrected {
	return func(ctx context.Context, tx pgx.Tx, subject string, c extraction.FieldCorrection) error {
		return audit.Record(ctx, tx, subject, "extraction.field_corrected", map[string]any{
			"invoice_id": c.InvoiceID,
			"field":      c.FieldName,
			"method":     string(c.Method),
		})
	}
}

// newAnchorLearnedAuditor adapts the audit module to the rule-learning seam. The event name is
// spelled here for newFieldCorrectedAuditor's reason
// (TestNewAnchorLearnedAuditor_WritesExactlyTheSixKeys).
func newAnchorLearnedAuditor() extraction.RecordAnchorLearned {
	return func(ctx context.Context, tx pgx.Tx, subject string, a extraction.AnchorLearned) error {
		return audit.Record(ctx, tx, subject, "extraction.anchor.learned", map[string]any{
			"invoice_id":         a.InvoiceID,
			"field":              a.FieldName,
			"layout_fingerprint": a.LayoutFingerprint,
			"anchor_rule_id":     a.RuleID,
			"relation":           string(a.Relation),
			"shape":              string(a.Shape),
		})
	}
}

// newInvoiceFieldApplier adapts the invoice store to the extraction seam. Each domain outcome
// crosses as one of the extraction sentinels so statusForErr maps it by identity; anything else
// passes through raw and stays a 500 (TestNewInvoiceFieldApplier_MapsEachDomainError). The
// rename's own two refusals map the same way (TestNewInvoiceFieldApplier_MapsTheRenameRefusals).
func newInvoiceFieldApplier(edit invoiceFieldEdit) extraction.ApplyFieldToInvoice {
	return func(ctx context.Context, tx pgx.Tx, documentID, field string, value *string, _ extraction.CorrectionMethod) (string, error) {
		in, err := invoiceEditFor(field, value)
		if err != nil {
			return "", err
		}
		inv, err := edit(ctx, tx, documentID, in)
		switch {
		case errors.Is(err, invoice.ErrNotFound):
			return "", extraction.ErrNoInvoiceForDocument
		case errors.Is(err, invoice.ErrNotFixable):
			return "", extraction.ErrInvoiceNotEditable
		case errors.Is(err, invoice.ErrValidation):
			return "", extraction.ErrValueRefused
		case errors.Is(err, invoice.ErrNumberTaken):
			return "", extraction.ErrInvoiceNumberTaken
		case errors.Is(err, invoice.ErrNumberFixed):
			return "", extraction.ErrInvoiceNumberFixed
		case err != nil:
			return "", err
		}
		return inv.ID, nil
	}
}

// newInvoiceLineItemsApplier adapts the invoice store to the line-set seam, mapping each domain
// outcome onto an extraction sentinel exactly as newInvoiceFieldApplier does. The LineItems
// pointer is always non-nil so an empty array means "remove every line" rather than "leave them
// alone" ([line-items-optional]). LineTax is carried through, not recomputed, so a replace-all
// save cannot erase a cell the user never touched, and the value reaches the filed envelope.
func newInvoiceLineItemsApplier(edit invoiceFieldEdit) extraction.ApplyLineItemsToInvoice {
	return func(ctx context.Context, tx pgx.Tx, documentID string, lines []extraction.LineItemInput) (string, error) {
		converted := make([]invoice.LineItemInput, len(lines))
		for i, l := range lines {
			converted[i] = invoice.LineItemInput{
				Description: l.Description,
				Quantity:    l.Quantity,
				UnitPrice:   l.UnitPrice,
				LineTotal:   l.LineTotal,
				LineTax:     l.LineTax,

				TaxCategory:               invoice.TrimmedOrNil(l.TaxCategory),
				HSNCode:                   invoice.TrimmedOrNil(l.HSNCode),
				ISICCode:                  invoice.TrimmedOrNil(l.ISICCode),
				SellersItemIdentification: invoice.TrimmedOrNil(l.SellersItemIdentification),
				PriceUnit:                 invoice.TrimmedOrNil(l.PriceUnit),
			}
		}
		inv, err := edit(ctx, tx, documentID, invoice.EditInput{LineItems: &converted})
		switch {
		case errors.Is(err, invoice.ErrNotFound):
			return "", extraction.ErrNoInvoiceForDocument
		case errors.Is(err, invoice.ErrNotFixable):
			return "", extraction.ErrInvoiceNotEditable
		case errors.Is(err, invoice.ErrValidation):
			return "", extraction.ErrValueRefused
		case err != nil:
			return "", err
		}
		return inv.ID, nil
	}
}

// invoiceEditFor puts one corrected value on its own EditInput/UpdateInput member. It lives here
// because internal/extraction cannot name invoice.EditInput; the handler has already refused
// every field name this switch does not name, and issue_date/due_date arrive normalised to ISO
// (TestNewInvoiceFieldApplier_MapsEachWritableFieldOntoItsColumn).
//
// A NIL value is an undo of a field the extractor never read: the column goes back to holding
// nothing, which is what the screen shows
// (TestInvoiceEditFor_ANilValueClearsEveryWritableColumn). invoice_number cannot be cleared this
// way -- a number is never NULL -- so a nil there stays ErrValueRefused, unchanged.
func invoiceEditFor(field string, value *string) (invoice.EditInput, error) {
	var in invoice.UpdateInput
	if field == "issue_date" || field == "due_date" {
		date := &in.IssueDate
		if field == "due_date" {
			date = &in.DueDate
		}
		switch {
		case value == nil:
			*date = invoice.ClearDate
		default:
			at, err := time.Parse(time.DateOnly, *value)
			if err != nil {
				return invoice.EditInput{}, fmt.Errorf("%w: %s %q", extraction.ErrValueRefused, field, *value)
			}
			*date = &at
		}
		return invoice.EditInput{UpdateInput: in}, nil
	}
	if field == "invoice_number" {
		if value == nil {
			return invoice.EditInput{}, fmt.Errorf("%w: invoice_number cannot be cleared", extraction.ErrValueRefused)
		}
		return invoice.EditInput{InvoiceNumber: value}, nil
	}

	var column **string
	switch field {
	case "supplier_tin":
		column = &in.SupplierTIN
	case "supplier_name":
		column = &in.SupplierName
	case "buyer_tin":
		column = &in.BuyerTIN
	case "buyer_name":
		column = &in.BuyerName
	case "currency":
		column = &in.Currency
	case "subtotal":
		column = &in.Subtotal
	case "vat":
		column = &in.VAT
	case "total":
		column = &in.Total
	case "buyer_email":
		column = &in.BuyerEmail
	case "buyer_telephone":
		column = &in.BuyerTelephone
	case "buyer_street":
		column = &in.BuyerStreet
	case "buyer_city":
		column = &in.BuyerCity
	case "buyer_postal_zone":
		column = &in.BuyerPostalZone
	case "buyer_country":
		column = &in.BuyerCountry
	case "buyer_state":
		column = &in.BuyerState
	case "buyer_lga":
		column = &in.BuyerLGA
	default:
		return invoice.EditInput{}, fmt.Errorf("%w: %q is not an invoice-writable field", extraction.ErrValueRefused, field)
	}
	if value == nil {
		*column = invoice.ClearText
	} else {
		*column = value
	}
	return invoice.EditInput{UpdateInput: in}, nil
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		platform.Fatal(slog.Default(), "submission: %s is required", key)
	}
	return v
}
