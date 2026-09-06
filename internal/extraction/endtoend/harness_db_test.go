// harness_db_test.go: the end-to-end suite -- document bytes in, an invoices row out. This is
// the seam neither internal/extraction nor internal/importer may cross, so the suite lives in
// its own test-only package and imports both.
//
// Local run (two DB-backed packages under one glob share one Postgres, hence -p 1):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5433/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5433/invoice_os?sslmode=disable" \
//	go test -p 1 -count=1 ./internal/extraction/...
package endtoend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/importer"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

const (
	// eeLayout is the layout AC-4 scores, and eeWantNumber is what Tier-1 must read off it
	// (internal/extraction/corpus_test.go's corpusExpect).
	eeLayout     = "corpus_inline_labels.pdf"
	eeWantNumber = "INV-1001"

	// eeFxDir is the sibling package's flat fixture directory, reached by path: a test
	// binary's CWD is its own package directory.
	eeFxDir = "../testdata"

	// eeMinRequiredFixtures: a shrunken require-list scores fewer layouts while still passing.
	eeMinRequiredFixtures = 6

	eeContentType = "application/pdf"

	// Upper bounds, not sleeps: the poll returns the moment the row is terminal. The extract
	// budget is under ExtractWorker.Timeout (10m) and over a cold PDFium render.
	eeExtractBudget = 3 * time.Minute
	eePollEvery     = 100 * time.Millisecond
	eeStopBudget    = 5 * time.Second
)

// requiredPDFs and requiredGoldens are hard-coded, never a directory walk: a walk cannot see a
// file that is missing.
var requiredPDFs = []string{
	"corpus_inline_labels.pdf",
	"corpus_split_labels.pdf",
	"corpus_stacked_labels.pdf",
	"corpus_two_column.pdf",
	"corpus_ambiguous_date.pdf",
	"corpus_totals_block.pdf",
}

var requiredGoldens = []string{
	"corpus_inline_labels.docling.json",
	"corpus_split_labels.docling.json",
	"corpus_stacked_labels.docling.json",
	"corpus_two_column.docling.json",
	"corpus_ambiguous_date.docling.json",
	"corpus_totals_block.docling.json",
}

var (
	eeH       *eeHarness
	eeErrNoDB = errors.New("end-to-end suite not configured")
)

type eeHarness struct {
	app   *pgxpool.Pool
	super *pgxpool.Pool
}

func TestMain(m *testing.M) {
	ctx := context.Background()
	h, err := eeSetup(ctx)
	if err != nil && !errors.Is(err, eeErrNoDB) {
		fmt.Fprintf(os.Stderr, "end-to-end suite setup: %v\n", err)
		os.Exit(1)
	}
	eeH = h

	code := m.Run()

	if eeH != nil {
		eeH.app.Close()
		eeH.super.Close()
	}
	os.Exit(code)
}

func eeSetup(ctx context.Context) (*eeHarness, error) {
	appURL := os.Getenv("DATABASE_URL")
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		return nil, eeErrNoDB
	}

	h := &eeHarness{}
	for _, c := range []struct {
		dst **pgxpool.Pool
		url string
		who string
	}{
		{&h.super, superURL, "superuser"},
		{&h.app, appURL, "app"},
	} {
		pool, err := pgxpool.New(ctx, c.url)
		if err != nil {
			return nil, fmt.Errorf("connect %s: %w", c.who, err)
		}
		*c.dst = pool
	}
	// A DSN that is set but unreachable is an error, not a skip. Ping cannot see an
	// unmigrated database; that surfaces as a per-test failure, still never a skip.
	if err := h.super.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping superuser (is the DB up?): %w", err)
	}
	return h, nil
}

// eeRequire is this package's ONE skip site. Mandatory: ci.yml's `go` job runs a bare
// `go test ./...` with no DSNs. The gated queue step turns the skip back into a failure.
func eeRequire(t *testing.T) *eeHarness {
	t.Helper()
	if eeH == nil {
		t.Skip("end-to-end suite skipped: set DATABASE_URL (invoice_app) and " +
			"DATABASE_SUPERUSER_URL (fixtures and cross-check reads)")
	}
	return eeH
}

// eeWorld is one seeded tenant with everything both halves of the path need. subject is a
// fresh uuid with an ACTIVE membership: a non-uuid subject bypasses the membership gate
// (internal/platform/db/tenant.go:61-63) instead of passing it, and an empty one trips
// invoice_status_history's actor CHECK.
type eeWorld struct {
	tenantID   string
	subject    string
	entityID   string
	documentID string
}

func eeSeed(t *testing.T, ctx context.Context, filename string) eeWorld {
	t.Helper()
	h := eeRequire(t)

	w := eeWorld{tenantID: uuid.NewString(), subject: uuid.NewString(), documentID: uuid.NewString()}

	if _, err := h.super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm')`,
		w.tenantID, "extr-21 "+w.tenantID[:8]); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		if _, err := h.super.Exec(context.Background(),
			`DELETE FROM tenants WHERE id = $1`, w.tenantID); err != nil {
			t.Errorf("teardown tenant %s: %v", w.tenantID, err)
		}
	})
	eeCleanupInfra(t, w.tenantID)

	if _, err := h.super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'preparer', 'active')`,
		w.tenantID, w.subject); err != nil {
		t.Fatalf("seed caller membership: %v", err)
	}
	if err := h.super.QueryRow(ctx,
		`INSERT INTO business_entities (tenant_id, name) VALUES ($1, $2) RETURNING id`,
		w.tenantID, "Adeyemi Trading Limited").Scan(&w.entityID); err != nil {
		t.Fatalf("seed business entity: %v", err)
	}
	if _, err := h.super.Exec(ctx,
		`INSERT INTO documents (id, tenant_id, storage_key, content_hash, size_bytes, filename, declared_content_type)
		 VALUES ($1, $2, $3, $4, $5, $6, 'application/pdf')`,
		w.documentID, w.tenantID, "extr-21/"+w.documentID,
		strings.Repeat("a", 64), len(eeFixtureBytes(t, filename)), filename); err != nil {
		t.Fatalf("seed document: %v", err)
	}
	return w
}

// eeCleanupInfra deletes what DELETE FROM tenants does not reach. Registered after the tenant
// cleanup so LIFO drains these first. audit_log_append_only() refuses DELETE for every role;
// session_replication_role='replica' is the one bypass and is superuser-only.
func eeCleanupInfra(t *testing.T, tenantID string) {
	t.Helper()
	h := eeRequire(t)
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := h.super.Exec(ctx,
			`DELETE FROM river_job WHERE args->>'tenant_id' = $1`, tenantID); err != nil {
			t.Errorf("teardown river_job for tenant %s: %v", tenantID, err)
		}
		if _, err := h.super.Exec(ctx,
			`DELETE FROM idempotency_keys WHERE tenant_id = $1`, tenantID); err != nil {
			t.Errorf("teardown idempotency_keys for tenant %s: %v", tenantID, err)
		}

		tx, err := h.super.Begin(ctx)
		if err != nil {
			t.Errorf("teardown audit_log for tenant %s: begin: %v", tenantID, err)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = 'replica'`); err != nil {
			t.Errorf("teardown audit_log for tenant %s: set session_replication_role: %v", tenantID, err)
			return
		}
		if _, err := tx.Exec(ctx, `DELETE FROM audit_log WHERE tenant_id = $1`, tenantID); err != nil {
			t.Errorf("teardown audit_log for tenant %s: %v", tenantID, err)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			t.Errorf("teardown audit_log for tenant %s: commit: %v", tenantID, err)
		}
	})
}

func eeFixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(eeFxDir, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// eeAudit is the worker's audit port. It writes on the tx it was handed, so the row shares the
// worker's transaction; a nil tx is the contract violation, not a no-op.
func eeAudit(ctx context.Context, tx pgx.Tx, ev extraction.ExtractionAudit) error {
	if tx == nil {
		return errors.New("the audit port was handed a nil tx; the row cannot share the worker's transaction")
	}
	event := "extraction.failed"
	if ev.Succeeded {
		event = "extraction.succeeded"
	}
	payload, err := json.Marshal(map[string]any{
		"document_id":       ev.DocumentID,
		"extraction_job_id": ev.ExtractionJobID,
		"extractor":         ev.Extractor,
		"extractor_version": ev.ExtractorVersion,
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO audit_log (actor, event, payload) VALUES ($1, $2, $3)`,
		"system", event, string(payload))
	return err
}

// eeOpt adjusts the worker one run uses. Additive: with no opts the worker is unchanged, so
// every existing call site keeps the production wiring.
type eeOpt func(*extraction.ExtractWorker)

// eeWorker wires the production seams, not stubs: the real PDFium reader on both the render
// and the text branch, and the tenant's own learned anchor rules.
func eeWorker(t *testing.T, body []byte, opts ...eeOpt) *extraction.ExtractWorker {
	t.Helper()
	h := eeRequire(t)
	ew := &extraction.ExtractWorker{
		Pool: h.app,
		// Never reached on the Text branch: Work calls Name()/Version() only.
		Extractor: extraction.NewPDFiumExtractor(),
		Open: func(context.Context, string) (extraction.Document, error) {
			return extraction.Document{Bytes: body, ContentType: eeContentType}, nil
		},
		Pages: &extraction.PageStore{
			Reader: extraction.NewPDFiumReader(),
			Sink:   func(context.Context, string, []byte) error { return nil },
		},
		Audit: eeAudit,
		Text:  extraction.NewPDFiumReader(),
		Rules: (&extraction.Store{Pool: h.app}).AnchorRulesFor,
	}
	for _, opt := range opts {
		opt(ew)
	}
	return ew
}

// eeExtract drives stage 1: document bytes -> extraction_field_results. It returns the
// extraction_jobs id so a caller can read rank-0 rows BEFORE the import hop.
//
// The job goes through a real River client because ExtractWorker.Work cannot be called from
// out here -- extractArgs is unexported -- and because that adds the fetch/execute hop.
func eeExtract(t *testing.T, ctx context.Context, w eeWorld, layout string, opts ...eeOpt) string {
	t.Helper()
	h := eeRequire(t)

	bundle := river.NewWorkers()
	extraction.AddTo(bundle, eeWorker(t, eeFixtureBytes(t, layout), opts...))
	c, err := queue.New(h.app, queue.Config{
		Queues:  map[string]river.QueueConfig{extraction.QueueName: {MaxWorkers: 1}},
		Workers: bundle,
	})
	if err != nil {
		t.Fatalf("build extraction worker client: %v", err)
	}

	if err := db.WithinTenantTx(ctx, h.app, w.tenantID, func(tx pgx.Tx) error {
		skipped, e := extraction.EnqueueExtraction(ctx, tx, c, w.tenantID, w.documentID)
		if e != nil {
			return e
		}
		if skipped {
			return errors.New("EnqueueExtraction reported skipped for a fresh document")
		}
		return nil
	}); err != nil {
		t.Fatalf("enqueue extraction for document %s: %v", w.documentID, err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start extraction worker pool: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), eeStopBudget)
		defer cancel()
		if err := c.Stop(stopCtx); err == nil {
			return
		}
		hardCtx, hardCancel := context.WithTimeout(context.Background(), eeStopBudget)
		defer hardCancel()
		if err := c.River().StopAndCancel(hardCtx); err != nil {
			t.Errorf("stop extraction worker pool: %v", err)
		}
	})

	return eeAwaitSucceeded(t, ctx, w, layout)
}

// eeAwaitSucceeded polls extraction_jobs until the row is terminal. "failed" is an attempt
// with retries left, not an outcome, so only succeeded and dead_lettered end the wait.
func eeAwaitSucceeded(t *testing.T, ctx context.Context, w eeWorld, layout string) string {
	t.Helper()
	h := eeRequire(t)
	deadline := time.Now().Add(eeExtractBudget)
	var id, state, lastErr string
	for {
		err := h.super.QueryRow(ctx,
			`SELECT id, state, coalesce(last_error, '')
			   FROM extraction_jobs WHERE tenant_id = $1 AND document_id = $2`,
			w.tenantID, w.documentID).Scan(&id, &state, &lastErr)
		switch {
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			t.Fatalf("read the extraction_jobs row for document %s: %v", w.documentID, err)
		case err == nil && state == "succeeded":
			return id
		case err == nil && state == "dead_lettered":
			t.Fatalf("extraction of %s dead-lettered: %s", layout, lastErr)
		}
		if time.Now().After(deadline) {
			t.Fatalf("extraction of %s is %q after %v (last_error %q), want succeeded",
				layout, state, eeExtractBudget, lastErr)
		}
		time.Sleep(eePollEvery)
	}
}

// eeGate stands in for the validate gate. ImportDocument never consults it, so a call is a
// regression this harness should surface rather than absorb.
type eeGate struct{}

var eeErrGateCalled = errors.New("ImportDocument must not consult the gate")

func (eeGate) Evaluate(context.Context, []invoice.EvalItem) (invoice.EvalResult, error) {
	return invoice.EvalResult{}, eeErrGateCalled
}

func (eeGate) ValidateBatch(context.Context, []invoice.Invoice) (invoice.BatchOutcome, error) {
	return invoice.BatchOutcome{}, eeErrGateCalled
}

// eeImport drives stage 2: the missing hop, run as the seeded member in the request-tenant
// posture.
func eeImport(t *testing.T, ctx context.Context, w eeWorld) importer.BatchResult {
	t.Helper()
	h := eeRequire(t)

	svc := importer.NewService(importer.NewStore(h.app), invoice.NewStore(h.app), eeGate{})
	rctx := auth.WithIdentity(ctx, auth.Identity{
		Subject: w.subject, Role: "authenticated", TenantID: w.tenantID,
	})
	res, err := svc.ImportDocument(rctx, w.entityID, w.documentID)
	if err != nil {
		t.Fatalf("ImportDocument(%s, %s): %v", w.entityID, w.documentID, err)
	}
	return res
}

type eeRow struct {
	name   string
	value  *string
	reason *string
	rank   int
}

func eeFieldResults(t *testing.T, ctx context.Context, jobID string) []eeRow {
	t.Helper()
	rows, err := eeRequire(t).super.Query(ctx,
		`SELECT field_name, value, reason_code, candidate_rank
		   FROM extraction_field_results
		  WHERE extraction_job_id = $1
		  ORDER BY field_name, candidate_rank`, jobID)
	if err != nil {
		t.Fatalf("read field results for job %s: %v", jobID, err)
	}
	defer rows.Close()

	var out []eeRow
	for rows.Next() {
		var r eeRow
		if err := rows.Scan(&r.name, &r.value, &r.reason, &r.rank); err != nil {
			t.Fatalf("scan field result: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate field results: %v", err)
	}
	return out
}

type eeInvoice struct {
	id               string
	number           string
	sourceDocumentID *string
}

func eeInvoices(t *testing.T, ctx context.Context, entityID string) []eeInvoice {
	t.Helper()
	rows, err := eeRequire(t).super.Query(ctx,
		`SELECT id, invoice_number, source_document_id
		   FROM invoices WHERE entity_id = $1 ORDER BY invoice_number`, entityID)
	if err != nil {
		t.Fatalf("read invoices for entity %s: %v", entityID, err)
	}
	defer rows.Close()

	var out []eeInvoice
	for rows.Next() {
		var inv eeInvoice
		if err := rows.Scan(&inv.id, &inv.number, &inv.sourceDocumentID); err != nil {
			t.Fatalf("scan invoice: %v", err)
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate invoices: %v", err)
	}
	return out
}

// eeFataler is the narrow surface eeRequireFixtures takes, so fatal-not-skip is itself
// observable. Precedent: fenceT, internal/extraction/deps_test.go.
type eeFataler interface {
	Helper()
	Fatalf(format string, args ...any)
	Skip(args ...any)
}

// eeRequireFixtures fails naming EVERY absent file. A first-miss abort would hide the rest,
// and a skip would green-light a suite that measured nothing.
func eeRequireFixtures(t eeFataler, names []string) {
	t.Helper()
	var missing []string
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(eeFxDir, name)); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return
	}
	t.Fatalf("%d fixture(s) absent under %s/: %s -- regenerate with `go test ./internal/extraction/ -run TestFixtures_MatchTheirGenerator -update`",
		len(missing), eeFxDir, strings.Join(missing, ", "))
}

// eeFixtureRecorder observes eeRequireFixtures without ending the test.
type eeFixtureRecorder struct {
	fatals []string
	skips  int
}

func (*eeFixtureRecorder) Helper() {}

func (r *eeFixtureRecorder) Fatalf(format string, args ...any) {
	r.fatals = append(r.fatals, fmt.Sprintf(format, args...))
}

func (r *eeFixtureRecorder) Skip(args ...any) {
	r.skips++
	_ = args
}

// --- the specs --------------------------------------------------------------

// AC-4. The seam no existing test crosses: real PDF bytes through the worker, then through
// ImportDocument, into one invoices row.
func TestRLS_EndToEndWritesAnInvoiceFromADocument(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()
	w := eeSeed(t, ctx, eeLayout)

	eeExtract(t, ctx, w, eeLayout)
	eeImport(t, ctx, w)

	got := eeInvoices(t, ctx, w.entityID)
	if len(got) != 1 {
		t.Fatalf("entity %s holds %d invoices row(s) after %s ran end to end, want exactly 1",
			w.entityID, len(got), eeLayout)
	}
	if got[0].number != eeWantNumber {
		t.Errorf("invoice_number = %q, want %q", got[0].number, eeWantNumber)
	}
	if got[0].sourceDocumentID == nil {
		t.Errorf("source_document_id is NULL, want %s -- the row does not trace back to the document it came from", w.documentID)
	} else if *got[0].sourceDocumentID != w.documentID {
		t.Errorf("source_document_id = %q, want %q", *got[0].sourceDocumentID, w.documentID)
	}
}

// AC-4. The read sits BETWEEN the two hops, so a zero here is the worker writing nothing
// rather than the mapper dropping it. Floor, not an exact count: EXTR-21-02 owns the score.
func TestRLS_EndToEndTheWorkerWroteRankZeroRowsFirst(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()
	w := eeSeed(t, ctx, eeLayout)

	jobID := eeExtract(t, ctx, w, eeLayout)

	var rankZero []string
	for _, r := range eeFieldResults(t, ctx, jobID) {
		if r.rank == 0 {
			rankZero = append(rankZero, r.name)
		}
	}
	if len(rankZero) < 1 {
		t.Fatalf("extraction job %s wrote %d extraction_field_results row(s) at candidate_rank 0 for %s, want at least 1",
			jobID, len(rankZero), eeLayout)
	}
	// Positive control: the field AC-4's invoice row is keyed on must be one of them.
	if !slices.Contains(rankZero, "invoice_number") {
		t.Errorf("rank-0 fields are %v; invoice_number is absent, so the invoices row above cannot carry %s",
			rankZero, eeWantNumber)
	}
}

// AC-6. An absent fixture fails naming every missing file -- it never skips, and it never
// stops at the first miss.
func TestEndToEnd_AbsentFixtureFatalsRatherThanSkips(t *testing.T) {
	if len(requiredPDFs) < eeMinRequiredFixtures || len(requiredGoldens) < eeMinRequiredFixtures {
		t.Fatalf("requiredPDFs=%d requiredGoldens=%d, want at least %d each -- a shrunken list scores fewer layouts while still passing",
			len(requiredPDFs), len(requiredGoldens), eeMinRequiredFixtures)
	}
	for i, pdf := range requiredPDFs {
		if want := strings.TrimSuffix(pdf, ".pdf") + ".docling.json"; requiredGoldens[i] != want {
			t.Fatalf("requiredGoldens[%d] = %q, want %q -- the two lists have drifted apart", i, requiredGoldens[i], want)
		}
	}
	// The hard-coded list can see a missing file; this walk sees a list that quietly shrank
	// below what is committed.
	onDisk, err := filepath.Glob(filepath.Join(eeFxDir, "corpus_*.pdf"))
	if err != nil {
		t.Fatalf("glob committed corpus layouts: %v", err)
	}
	if len(onDisk) < eeMinRequiredFixtures {
		t.Fatalf("%d corpus_*.pdf on disk under %s, want at least %d -- the walk is reading the wrong directory", len(onDisk), eeFxDir, eeMinRequiredFixtures)
	}
	for _, p := range onDisk {
		if !slices.Contains(requiredPDFs, filepath.Base(p)) {
			t.Errorf("%s is committed but absent from requiredPDFs -- the suite would silently stop scoring it", filepath.Base(p))
		}
	}

	const absentPDF = "corpus_not_committed.pdf"
	const absentGolden = "corpus_not_committed.docling.json"
	rec := &eeFixtureRecorder{}
	eeRequireFixtures(rec, []string{requiredPDFs[0], absentPDF, absentGolden})

	if rec.skips != 0 {
		t.Errorf("eeRequireFixtures skipped %d time(s) on an absent fixture; a suite that measures nothing must fail, not skip", rec.skips)
	}
	if len(rec.fatals) != 1 {
		t.Fatalf("eeRequireFixtures recorded %d fatal(s) on an absent fixture, want exactly 1 naming both %s and %s",
			len(rec.fatals), absentPDF, absentGolden)
	}
	msg := rec.fatals[0]
	for _, name := range []string{absentPDF, absentGolden} {
		if !strings.Contains(msg, name) {
			t.Errorf("fatal %q does not name %s -- a first-miss abort hides the rest", msg, name)
		}
	}
	// Negative control: a committed fixture must not be reported missing.
	if strings.Contains(msg, requiredPDFs[0]) {
		t.Errorf("fatal %q names %s, which IS committed -- the helper reports every name, not the absent ones", msg, requiredPDFs[0])
	}

	// The other direction: every required name is on disk today, so the helper must stay quiet.
	quiet := &eeFixtureRecorder{}
	eeRequireFixtures(quiet, append(append([]string{}, requiredPDFs...), requiredGoldens...))
	if len(quiet.fatals) != 0 || quiet.skips != 0 {
		t.Errorf("eeRequireFixtures reported %d fatal(s) and %d skip(s) over the committed fixtures: %v",
			len(quiet.fatals), quiet.skips, quiet.fatals)
	}
}
