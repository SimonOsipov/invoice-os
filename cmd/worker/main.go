// Command worker runs all background work, so a request-driven service that Cloud Run throttles
// or scales to zero holds none: the River queues of submission, extraction and contact delivery,
// the person erase job, the reconciliation sweep, the two custom-rule re-checks, the daily rollup snapshot, the
// system-health sampler, the newsletter purge, the workspace purge and the code-list sync. The other services only
// enqueue. It serves /healthz and /readyz and no other route.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/accountevents"
	"github.com/SimonOsipov/invoice-os/internal/aicalls"
	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/dashboard"
	"github.com/SimonOsipov/invoice-os/internal/docadapter"
	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/notifications"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth/gotrueadmin"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
	"github.com/SimonOsipov/invoice-os/internal/reconciliation"
	"github.com/SimonOsipov/invoice-os/internal/submission"
	"github.com/SimonOsipov/invoice-os/internal/systemhealth"
	"github.com/SimonOsipov/invoice-os/internal/validation/codelist"
	"github.com/SimonOsipov/invoice-os/internal/workspacepurge"
)

func main() {
	defer platform.ReportBootPanic()

	app, err := platform.New("worker")
	if err != nil {
		platform.Fatal(slog.Default(), "worker: startup: %v", err)
	}

	gw, err := systemhealth.GatewayURL(os.Getenv("GATEWAY_URL"))
	if err != nil {
		platform.Fatal(app.Logger, "worker: %v", err)
	}
	// A worker that cannot reach validation never runs the re-check; a silently-off compliance
	// step is worse than a crash.
	validationURL := mustEnv("VALIDATION_URL")
	s2sToken := mustEnv("S2S_TOKEN")

	ctx := context.Background()

	// invoice_app (NOBYPASSRLS) runs every job, each inside its own tenant transaction. The
	// reader role is the only one that enumerates tenants for the sweeps.
	pool, err := db.NewPool(ctx, mustEnv("DATABASE_URL"))
	if err != nil {
		platform.Fatal(app.Logger, "worker: db pool: %v", err)
	}
	defer pool.Close()
	readerPool, err := db.NewPool(ctx, mustEnv("DATABASE_READER_URL"))
	if err != nil {
		platform.Fatal(app.Logger, "worker: reader db pool: %v", err)
	}
	defer readerPool.Close()
	app.Ready("database", pool.Ping)
	app.Ready("reader-database", readerPool.Ping)

	docCfg, err := document.ConfigFromEnv()
	if err != nil {
		platform.Fatal(app.Logger, "worker: document config: %v", err)
	}
	docObjects, err := document.NewS3Store(docCfg, nil)
	if err != nil {
		platform.Fatal(app.Logger, "worker: document object store: %v", err)
	}

	// Every config below is fatal in every environment, even when the job that reads it never
	// runs: a typo found at boot beats one found on the first job.
	mockCfg, err := submission.MockConfigFromEnv()
	if err != nil {
		platform.Fatal(app.Logger, "worker: adapter config: %v", err)
	}
	reg := submission.NewDefaultRegistry(mockCfg)
	adapter, err := submission.Select(reg, app.Config.Environment, os.Getenv("APP_ADAPTER"))
	if err != nil {
		platform.Fatal(app.Logger, "worker: adapter: %v", err)
	}
	rateLimit, err := submission.RateLimitConfigFromEnv()
	if err != nil {
		platform.Fatal(app.Logger, "worker: rate limit config: %v", err)
	}
	// The per-tenant limiter is in memory: it holds only while one worker instance runs.
	limiter := submission.NewRateLimiter()
	invStore := invoice.NewStore(pool)

	// Queue is backfilled once queue.New, which needs this bundle, returns.
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

	// One EXTRACTOR read selects both the extractor and the text reader, so the two cannot drift.
	extractorName, doclingURL := os.Getenv("EXTRACTOR"), os.Getenv("DOCLING_URL")
	extractor, err := selectExtractor(extractorName, doclingURL)
	if err != nil {
		// ceiling: the error quotes the raw DOCLING_URL; internal host, no credentials today
		platform.Fatal(app.Logger, "worker: %v", err)
	}
	textReader, err := selectTextReader(extractorName, doclingURL)
	if err != nil {
		// ceiling: the error quotes the raw DOCLING_URL; internal host, no credentials today
		platform.Fatal(app.Logger, "worker: %v", err)
	}

	docSvc := document.NewService(document.NewStore(pool), docObjects)
	aiClient, err := ai.FromEnv(app.Logger)
	if err != nil {
		platform.Fatal(app.Logger, "worker: %v", err)
	}
	jevClient, err := jev.FromEnv(app.Logger)
	if err != nil {
		platform.Fatal(app.Logger, "worker: %v", err)
	}
	aiRecorder := aicalls.NewRecorder(pool, app.Logger)
	defer aiRecorder.Wait() // runs before pool.Close
	aiClient.SetRecorder(aiRecorder.Record)
	jevClient.SetRecorder(aiRecorder.Record)
	// PageStore.Reader stays go-pdfium whatever EXTRACTOR selects: it yields the page images
	// and the tokens Fingerprint and AnchorObservations are built from.
	ew := newExtractWorker(pool, extractor, docadapter.Opener(docSvc.Open),
		&extraction.PageStore{Reader: extraction.NewPDFiumReader(), Sink: docadapter.PageSink(docObjects)},
		newExtractionAuditor(), textReader, (&extraction.Store{Pool: pool}).AnchorRulesFor, aiClient, jevClient,
		docadapter.PageObjectReader(docObjects), app.Logger)

	mode, hubspot, resend, err := notifications.DeliveryClients(os.Getenv, platform.DeployEnv(), notifications.RealTransport())
	if err != nil {
		platform.Fatal(app.Logger, "worker: %v", err)
	}
	platform.Contacts = string(mode)
	nl, err := notifications.NewsletterClients(os.Getenv, platform.DeployEnv(), mode, notifications.RealTransport())
	if err != nil {
		platform.Fatal(app.Logger, "worker: %v", err)
	}
	cw := contactWorkers(pool, mode, hubspot, resend, nl, app.Logger)
	rw := &notifications.RegistrantWorker{Logger: app.Logger}
	account, err := gotrueadmin.FromEnv(os.Getenv, os.Getenv("K_SERVICE") != "", platform.IDTokenTransport(nil))
	if err != nil {
		platform.Fatal(app.Logger, "worker: %v", err)
	}
	xw := newEraseWorker(pool, mode, hubspot, resend, account, app.Logger)
	xbw := &notifications.EraseByIDWorker{Pool: pool, Logger: app.Logger}
	if account != nil {
		xbw.Account = account
	}

	pjw := &workspacepurge.PurgeWorker{}
	aw := &accountevents.AuditWorker{Pool: pool}

	q, err := queue.New(pool, queue.Config{
		Queues:  queueConfigs(cw != nil),
		Workers: workerBundle(sw, pw, ew, rw, cw, xw, xbw, pjw, aw),
		Logger:  app.Logger,
	})
	if err != nil {
		platform.Fatal(app.Logger, "worker: queue: %v", err)
	}
	sw.Queue, pw.Queue, xw.River = q, q, q.River()
	xbw.River = q.River()
	rw.Store = notifications.NewStore(pool, q.River())
	app.AddBackgroundWorker(q)

	// Each periodic task runs in one worker instance only.
	leader := db.NewLeader(mustEnv("DATABASE_URL"))

	fleet := systemhealth.NewFleetReader(gw, &http.Client{Transport: platform.TraceTransport(nil)}, mustEnv("GATEWAY_TOKEN"))
	sampler := &systemhealth.Sampler{Store: &systemhealth.Store{Pool: pool}, Fleet: fleet, Now: time.Now, Logger: app.Logger}
	app.AddBackgroundWorker(codelist.NewWorker(systemhealth.SampleInterval, leader.Lead("health-sample", sampler.Sample)))

	app.AddBackgroundWorker(codelist.NewWorker(notifications.HandoffInterval, leader.Lead("registrant-handoff", notifications.MoveRegistrantHandoffs(pool, q.River()))))
	app.AddBackgroundWorker(codelist.NewWorker(accountevents.HandoffInterval, leader.Lead("account-event-handoff", accountevents.MoveHandoffs(pool, q.River(), app.Logger))))

	app.AddBackgroundWorker(codelist.NewWorker(time.Hour, leader.Lead("newsletter-purge", notifications.NewNewsletterStore(pool, q.River()).PurgeExpired)))

	purger := &workspacepurge.Purger{Pool: pool, Reader: readerPool, River: q.River(), Objects: docObjects, Logger: app.Logger}
	if account != nil { // a nil *Client in the interface would pass the purger's nil check
		purger.Accounts = account
	}
	pjw.Purger = purger
	app.AddBackgroundWorker(codelist.NewWorker(time.Hour, leader.Lead("workspace-purge", purger.RunOnce)))

	app.AddBackgroundWorker(codelist.NewWorker(codelist.SyncInterval,
		leader.Lead("codelist-sync", codelist.NewSyncer(pool, codelist.DefaultBaseURL, nil, app.Logger).SyncAll)))

	cfg, err := reconciliation.ConfigFromEnv()
	if err != nil {
		platform.Fatal(app.Logger, "worker: reconciliation config: %v", err)
	}
	tenants := func(ctx context.Context) ([]string, error) { return reconciliation.EnumerateTenants(ctx, readerPool) }
	rec := &reconciliation.Reconciler{ReaderPool: readerPool, AppPool: pool, Queue: q, Cfg: cfg, Logger: app.Logger}
	app.AddBackgroundWorker(reconciliation.NewSweeper(cfg.Interval, leader.Lead("reconciliation-sweep", rec.SweepOnce)))

	// The re-check runs as invoice_app: RecheckCovered refuses a privileged role.
	rechecker := &invoice.Rechecker{
		Pool:    pool,
		Store:   invStore,
		Gate:    invoice.NewGate(invStore, invoice.NewValidator(validationURL, s2sToken, nil)),
		Tenants: tenants,
		Logger:  app.Logger,
	}
	app.AddBackgroundWorker(reconciliation.NewSweeper(cfg.Interval, leader.Lead("recheck", rechecker.RunOnce)))
	app.AddBackgroundWorker(reconciliation.NewSweeper(cfg.Interval, leader.Lead("recheck-custom", rechecker.RunCustomOnce)))

	snap := &dashboard.Snapshotter{Pool: pool, Tenants: tenants, Now: time.Now, Logger: app.Logger}
	app.AddBackgroundWorker(codelist.NewWorker(dashboard.SnapshotInterval, leader.Lead("rollup-snapshot", snap.RunOnce)))
	app.AddBackgroundWorker(leader) // last: Stop releases the locks after every task has stopped

	if err := app.Run(ctx); err != nil {
		platform.Fatal(app.Logger, "worker: %v", err)
	}
}

// selectExtractor resolves EXTRACTOR to an extraction.Extractor, the shape submission.Select
// already uses for APP_ADAPTER. Unset means mock. An unrecognised value is an error, never a
// silent fall back to mock: a typo that quietly downgrades extraction is what this prevents.
func selectExtractor(extractorName, doclingURL string) (extraction.Extractor, error) {
	switch extractorName {
	case "", "mock":
		return extraction.NewMockExtractor(), nil
	case "docling":
		if doclingURL == "" {
			return nil, errors.New("extractor: EXTRACTOR=docling requires DOCLING_URL")
		}
		// Stays a *DoclingExtractor even though Extract is unreachable once Text is wired:
		// Name() and Version() still write extraction_jobs.extractor.
		ext, err := extraction.NewDoclingExtractor(doclingURL)
		if err != nil {
			return nil, fmt.Errorf("extractor: DOCLING_URL %q: %w", doclingURL, err)
		}
		return ext, nil
	default:
		return nil, fmt.Errorf("extractor: unrecognised EXTRACTOR %q, want mock or docling", extractorName)
	}
}

// selectTextReader resolves the SAME EXTRACTOR value to ExtractWorker.Text. mock and unset
// select no reader: a nil Text keeps Work on the Extractor branch. Every other arm returns the
// literal nil -- a typed nil compares non-nil as a PageReader and would panic on the first job
// (TestSelectTextReader_MockAndUnsetSelectNoReader).
func selectTextReader(extractorName, doclingURL string) (extraction.PageReader, error) {
	switch extractorName {
	case "", "mock":
		return nil, nil
	case "docling":
		if doclingURL == "" {
			return nil, errors.New("extractor: EXTRACTOR=docling requires DOCLING_URL")
		}
		r, err := extraction.NewDoclingReader(doclingURL)
		if err != nil {
			return nil, fmt.Errorf("extractor: DOCLING_URL %q: %w", doclingURL, err)
		}
		return r, nil
	default:
		return nil, fmt.Errorf("extractor: unrecognised EXTRACTOR %q, want mock or docling", extractorName)
	}
}

// newExtractWorker keeps every collaborator at one call site: a nil field compiles and fails
// only on the first job (TestNewExtractWorker_SetsEveryCollaborator).
func newExtractWorker(pool *pgxpool.Pool, ext extraction.Extractor, open extraction.OpenDocument,
	pages *extraction.PageStore, auditor extraction.RecordExtractionAudit,
	text extraction.PageReader, rules extraction.LoadAnchorRules,
	aiReader extraction.AIReader, jevAsker extraction.JevAsker, pageBytes extraction.PageObject,
	logger *slog.Logger) *extraction.ExtractWorker {
	return &extraction.ExtractWorker{Pool: pool, Extractor: ext, Open: open, Pages: pages,
		Audit: auditor, Text: text, Rules: rules, AI: aiReader, Jev: jevAsker, PageBytes: pageBytes, Logger: logger}
}

// newExtractionAuditor adapts the audit module to the extraction seam. Each event name is
// spelled once, inline: a const identifier reads as a non-literal to internal/platform/db's
// audit.Record scan and lands the site in no bucket.
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
		// audit_log is append-only, so a failure_kind written wrong is wrong forever
		// (TestNewExtractionAuditor_FailedRefusesAnInvalidFailureKind).
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

// contactWorkerSet holds the contact delivery workers.
type contactWorkerSet struct {
	deliver *notifications.DeliverWorker
	deal    *notifications.DemoDealWorker
	blog    *notifications.BlogSyncWorker // nil when the newsletter is off
}

// contactWorkers is nil in off mode: the contacts queue then has no fetcher and its jobs wait.
func contactWorkers(pool *pgxpool.Pool, mode notifications.Mode, hs notifications.HubSpotClient,
	rs notifications.ResendClient, nl notifications.NewsletterWiring, logger *slog.Logger) *contactWorkerSet {
	if mode == notifications.ModeOff {
		return nil
	}
	cw := &contactWorkerSet{
		deliver: &notifications.DeliverWorker{Pool: pool, HubSpot: hs, Resend: rs, Mode: mode, Logger: logger},
		deal:    &notifications.DemoDealWorker{Pool: pool, HubSpot: hs, Logger: logger},
	}
	if nl.Enabled {
		cw.blog = &notifications.BlogSyncWorker{Pool: pool, Resend: nl.Resend, Mode: nl.Mode, Logger: logger}
	}
	return cw
}

// newEraseWorker builds the erase worker in every contacts mode. The erasers are nil in off mode;
// Account stays a nil interface without a client (TestEraseWorker_FollowsTheModes).
func newEraseWorker(pool *pgxpool.Pool, mode notifications.Mode, hs notifications.HubSpotClient,
	rs notifications.ResendClient, account *gotrueadmin.Client, logger *slog.Logger) *notifications.EraseWorker {
	w := &notifications.EraseWorker{Pool: pool, Mode: mode, Logger: logger}
	w.HubSpot, _ = hs.(notifications.HubSpotEraser)
	w.Resend, _ = rs.(notifications.ResendEraser)
	if account != nil {
		w.Account = account
	}
	return w
}

// queueConfigs is the one map the client fetches from. Extraction gets its own queue so a slow
// read cannot starve submission; river.NewClient refuses MaxWorkers < 1.
func queueConfigs(contacts bool) map[string]river.QueueConfig {
	qs := map[string]river.QueueConfig{
		river.QueueDefault:   {MaxWorkers: 10},
		extraction.QueueName: {MaxWorkers: 2},
	}
	if contacts {
		qs[notifications.QueueContacts] = river.QueueConfig{MaxWorkers: 2}
	}
	return qs
}

// workerBundle is the ONE bundle queue.New reads. A River client takes exactly one, so a second
// bundle would leave its workers unfetched (TestWorkerBundle_CarriesEveryKind).
func workerBundle(sw *submission.SubmitWorker, pw *submission.PollWorker, ew *extraction.ExtractWorker, rw *notifications.RegistrantWorker, cw *contactWorkerSet, xw *notifications.EraseWorker, xbw *notifications.EraseByIDWorker, pjw *workspacepurge.PurgeWorker, aw *accountevents.AuditWorker) *river.Workers {
	bundle := submission.Workers(sw, pw)
	extraction.AddTo(bundle, ew)
	notifications.AddRegistrantTo(bundle, rw)
	notifications.AddEraseTo(bundle, xw)
	notifications.AddEraseByIDTo(bundle, xbw)
	workspacepurge.AddPurgeTo(bundle, pjw)
	accountevents.AddAuditTo(bundle, aw)
	if cw != nil {
		notifications.AddTo(bundle, cw.deliver, cw.deal, cw.blog)
	}
	return bundle
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		platform.Fatal(slog.Default(), "worker: %s is required", key)
	}
	return v
}
