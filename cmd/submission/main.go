// Command submission is the 05 Submission context service. M2-08 booted the River worker
// pool (the async job spine) alongside the platform kit's /healthz + /readyz and drains
// in-flight jobs within the shutdown window on SIGINT/SIGTERM. The worker connects as the
// app role (invoice_app) and re-establishes tenant context per job — the worker-role
// pattern, docs/migrations.md §8. M5-04 wires the real handlers onto that spine:
// SubmitWorker drives the tx1 / adapter / tx2 submit flow and PollWorker follows a
// deferred verdict the same way (internal/submission/worker.go) — both registered, with
// ExtractWorker, on the single bundle workerBundle builds below.
//
// The domain HTTP surface is GET /v1/extractions (EXTR-07) and POST /v1/documents (EXTR-09),
// which stores the upload and enqueues its extraction on this service's own River client.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

func main() {
	app, err := platform.New("submission")
	if err != nil {
		log.Fatalf("submission: startup: %v", err)
	}

	ctx := context.Background()

	// Connect as the app role (invoice_app, NOBYPASSRLS) — never the migrator or
	// superuser (docs/migrations.md §1). The worker uses this pool both to operate River's
	// queue and, per job, to open tenant-scoped transactions via db.WithinTenantTx.
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		// pgx would otherwise build a config from ambient libpq env/defaults for an empty
		// DSN — fail fast so this service can only ever connect as its configured app role.
		log.Fatal("submission: DATABASE_URL is required")
	}
	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		log.Fatalf("submission: db pool: %v", err)
	}
	defer pool.Close()

	// /readyz now reflects the DB dependency the worker carries.
	app.Ready("database", pool.Ping)

	// The five DOCUMENT_* variables are required unconditionally, the way PORT and
	// MockConfigFromEnv are: gating this on whether extraction ever runs would defer the
	// failure to the moment a job first needs the store
	// (TestSubmissionMain_FatalOnDocumentConfigError).
	docCfg, err := document.ConfigFromEnv()
	if err != nil {
		log.Fatalf("submission: document config: %v", err)
	}
	docObjects, err := document.NewS3Store(docCfg, nil)
	if err != nil {
		log.Fatalf("submission: document object store: %v", err)
	}

	// M5-02-04: resolve the configured adapter against the fail-closed production
	// allowlist before the queue starts.
	//
	// M5-03-05: the mock's latency baseline is read from the environment BEFORE the registry is
	// built, and a malformed value is fatal. The fatal is UNCONDITIONAL -- it fires even when
	// APP_ADAPTER is unset and the mock is never selected -- deliberately, matching how PORT
	// behaves: gating it on `appAdapter == "mock"` would defer the failure to the moment someone
	// flips the adapter on a fleet, the worst time to discover a typo.
	mockCfg, err := submission.MockConfigFromEnv()
	if err != nil {
		log.Fatalf("submission: adapter config: %v", err)
	}
	reg := submission.NewDefaultRegistry(mockCfg)
	appAdapter := os.Getenv("APP_ADAPTER")
	// M5-04-08 tightens this: a failed Select is fatal in EVERY environment, not just
	// production -- SubmitWorker/PollWorker below need a real Adapter to do anything, so
	// booting with none configured is no longer a viable "continue anyway" state for any
	// fleet, dev included.
	adapter, err := submission.Select(reg, app.Config.Environment, appAdapter)
	if err != nil {
		log.Fatalf("submission: adapter: %v", err)
	}

	rateLimit, err := submission.RateLimitConfigFromEnv()
	if err != nil {
		log.Fatalf("submission: rate limit config: %v", err)
	}
	limiter := submission.NewRateLimiter()
	invStore := invoice.NewStore(pool)

	// SubmitWorker/PollWorker are built with every field except Queue, which does not exist
	// yet -- queue.New (below) needs their bundle (submission.Workers(sw, pw)) to construct
	// the client in the first place. Queue is backfilled onto these same pointers once
	// queue.New returns, before the client is registered as a background worker.
	sw := &submission.SubmitWorker{
		Pool:        pool,
		Adapter:     adapter,
		InvoicePort: invStore,
		Limiter:     limiter,
		RateLimit:   rateLimit,
		Logger:      app.Logger,
	}
	pw := &submission.PollWorker{
		Pool:        pool,
		Adapter:     adapter,
		InvoicePort: invStore,
		Logger:      app.Logger,
	}

	// Fatal in every environment, matching the Select call above: a fleet that names an
	// extractor it cannot build should not boot and quietly extract nothing.
	extractor, err := selectExtractor(os.Getenv("EXTRACTOR"), os.Getenv("DOCLING_URL"))
	if err != nil {
		log.Fatalf("submission: %v", err)
	}

	// ExtractWorker has no Queue field, so unlike sw/pw it needs no backfill. PageStore.Reader
	// stays go-pdfium whatever EXTRACTOR selects: it renders page images, not text.
	docSvc := document.NewService(document.NewStore(pool), docObjects)
	ew := newExtractWorker(pool, extractor, newDocumentOpener(docSvc.Open),
		&extraction.PageStore{Reader: extraction.NewPDFiumReader(), Sink: newPageSink(docObjects)},
		newExtractionAuditor(), app.Logger)

	// Build the working River client and register it on the platform kit's lifecycle, so it
	// starts alongside /healthz and drains on shutdown (decision #3).
	q, err := queue.New(pool, queue.Config{
		Queues:  queueConfigs(),
		Workers: workerBundle(sw, pw, ew),
		Logger:  app.Logger,
	})
	if err != nil {
		log.Fatalf("submission: queue: %v", err)
	}
	sw.Queue, pw.Queue = q, q
	app.AddBackgroundWorker(q)

	// Stub endpoint — proves routing end to end.
	app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"submission","status":"ok"}`))
	})

	// GET /v1/extractions -- reached as /api/submission/v1/extractions: the gateway routes
	// on the first segment under /api/ and forwards the subpath, so the pattern has no prefix.
	app.Mux.HandleFunc("GET /v1/extractions", extraction.JobsHandler((&extraction.Reader{Pool: pool}).JobsForDocument, app.Logger))

	// POST /v1/documents -- the upload that stores a source document and queues its
	// extraction. Two transactions on purpose: Service.Store commits its own, the enqueue
	// opens a second. A crash between them leaves a document with no job, which is the safe
	// direction; recovery is EXTR-17's (internal/extraction/enqueue.go).
	app.Mux.HandleFunc("POST /v1/documents", extraction.UploadHandler(
		newDocumentStorer(docSvc.Store), newExtractionEnqueuer(pool, q), app.Logger))

	if err := app.Run(ctx); err != nil {
		log.Fatalf("submission: %v", err)
	}
}

// documents.size_bytes CHECKs <= this (migrations/20260802163544_documents.sql).
const maxDocumentBytes = 15 << 20

// documentOpen is document.Service.Open's shape, so a test can substitute one.
type documentOpen func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error)

// newDocumentOpener adapts the document service to the extraction seam: whole object, no
// range, capped read, body closed exactly once on every path. ctx is forwarded untouched --
// the worker has already put the job's tenant on it, and RLS scopes the row lookup by that
// identity (TestNewDocumentOpener_ForwardsContextVerbatim).
func newDocumentOpener(open documentOpen) extraction.OpenDocument {
	return func(ctx context.Context, documentID string) (extraction.Document, error) {
		doc, obj, err := open(ctx, documentID, "")
		// Registered before the error branch: an open that hands back both a body and an
		// error still closes (TestNewDocumentOpener_ClosesBodyWhenOpenErrors).
		if obj.Body != nil {
			defer func() { _ = obj.Body.Close() }()
		}
		if err != nil {
			return extraction.Document{}, err
		}
		if obj.Body == nil {
			return extraction.Document{}, fmt.Errorf("submission: document %s opened with no body", documentID)
		}
		// +1 so an at-the-cap document still reads whole and only an over-cap one is
		// detectable. The cap bounds the READ, never obj.Size, which a ranged or lying
		// store understates (TestNewDocumentOpener_CapsAtDocumentSizeCeiling).
		b, err := io.ReadAll(io.LimitReader(obj.Body, maxDocumentBytes+1))
		if err != nil {
			return extraction.Document{}, err
		}
		if len(b) > maxDocumentBytes {
			return extraction.Document{}, fmt.Errorf(
				"submission: document %s exceeds the %d-byte ceiling", documentID, maxDocumentBytes)
		}
		var ct string
		if doc.DeclaredContentType != nil {
			ct = *doc.DeclaredContentType
		}
		return extraction.Document{Bytes: b, ContentType: ct}, nil
	}
}

// documentStore is document.Service.Store's shape, so a test can substitute one.
type documentStore func(ctx context.Context, filename, contentType string, size int64, body io.ReadSeeker) (document.Document, bool, error)

// newDocumentStorer adapts the document service to the upload seam: internal/extraction never
// imports internal/document, so the stored row is projected into its own shape here, the way
// newDocumentOpener projects an opened one. Every field comes off the STORED row -- filename
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

// newPageSink adapts the document object store to the extraction seam. bytes.Reader is handed
// over at offset 0 because Put transmits from the reader's CURRENT position, the same trap
// document.Service.Store rewinds for (TestNewPageSink_PutsToTheDocumentStore).
func newPageSink(objects document.ObjectStore) extraction.PageSink {
	return func(ctx context.Context, key string, body []byte) error {
		return objects.Put(ctx, key, bytes.NewReader(body), int64(len(body)))
	}
}

// newExtractionAuditor adapts the audit module to the extraction seam. Two call sites, each
// with its event name spelled once: a const identifier here reads as a non-literal to
// internal/platform/db's audit.Record scan and lands the site in no bucket.
func newExtractionAuditor() extraction.RecordExtractionAudit {
	return func(ctx context.Context, tx pgx.Tx, ev extraction.ExtractionAudit) error {
		if ev.Succeeded {
			return audit.Record(ctx, tx, "system", "extraction.succeeded", map[string]any{
				"document_id":       ev.DocumentID,
				"extraction_job_id": ev.ExtractionJobID,
				"extractor":         ev.Extractor,
				"extractor_version": ev.ExtractorVersion,
				"field_count":       ev.FieldCount,
				"flagged_count":     ev.FlaggedCount,
			})
		}
		// audit_log is append-only, so a failure_kind written wrong is wrong forever. Work()
		// assigns a kind on every error path, making "" unreachable today; this is what keeps
		// it unreachable (TestNewExtractionAuditor_FailedRefusesAnInvalidFailureKind).
		if !ev.FailureKind.Valid() {
			return fmt.Errorf("extraction audit: job %s reports failure with failure kind %q",
				ev.ExtractionJobID, ev.FailureKind)
		}
		return audit.Record(ctx, tx, "system", "extraction.failed", map[string]any{
			"document_id":       ev.DocumentID,
			"extraction_job_id": ev.ExtractionJobID,
			"extractor":         ev.Extractor,
			"extractor_version": ev.ExtractorVersion,
			"state":             ev.State,
			"failure_kind":      string(ev.FailureKind),
		})
	}
}

// selectExtractor resolves EXTRACTOR to an extraction.Extractor, the shape submission.Select
// already uses for APP_ADAPTER. Unset means mock, so a fleet that sets nothing behaves exactly
// as it did before the sidecar existed. An unrecognised value is an error, never a silent fall
// back to mock: a typo that quietly downgrades extraction is the failure this shape exists to
// prevent, and main() makes it fatal in every environment (M5-04-08's posture).
func selectExtractor(extractorName, doclingURL string) (extraction.Extractor, error) {
	switch extractorName {
	case "", "mock":
		return extraction.NewMockExtractor(), nil
	case "docling":
		if doclingURL == "" {
			return nil, errors.New("extractor: EXTRACTOR=docling requires DOCLING_URL")
		}
		// NewDoclingExtractor validates the URL, so a malformed one fails here at boot rather
		// than on the first job with a client pointed at nothing.
		ext, err := extraction.NewDoclingExtractor(doclingURL)
		if err != nil {
			return nil, fmt.Errorf("extractor: DOCLING_URL %q: %w", doclingURL, err)
		}
		return ext, nil
	default:
		return nil, fmt.Errorf("extractor: unrecognised EXTRACTOR %q, want mock or docling", extractorName)
	}
}

// newExtractWorker keeps every collaborator at one call site: a nil field compiles and fails
// only on the first job (TestNewExtractWorker_SetsEveryCollaborator).
func newExtractWorker(pool *pgxpool.Pool, ext extraction.Extractor, open extraction.OpenDocument, pages *extraction.PageStore, auditor extraction.RecordExtractionAudit, logger *slog.Logger) *extraction.ExtractWorker {
	return &extraction.ExtractWorker{Pool: pool, Extractor: ext, Open: open, Pages: pages, Audit: auditor, Logger: logger}
}

// queueConfigs is the one map the client fetches from. Extraction gets its own queue so a slow
// read cannot starve submission; river.NewClient refuses MaxWorkers < 1.
func queueConfigs() map[string]river.QueueConfig {
	return map[string]river.QueueConfig{
		river.QueueDefault:   {MaxWorkers: 10},
		extraction.QueueName: {MaxWorkers: 2},
	}
}

// workerBundle is the ONE bundle queue.New reads. A River client takes exactly one, so a
// second bundle would leave its workers unfetched (TestWorkerBundle_CarriesAllThreeKinds).
func workerBundle(sw *submission.SubmitWorker, pw *submission.PollWorker, ew *extraction.ExtractWorker) *river.Workers {
	bundle := submission.Workers(sw, pw)
	extraction.AddTo(bundle, ew)
	return bundle
}
