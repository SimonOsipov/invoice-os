// worker_db_test.go: the DB-backed ExtractWorker specs. Package extraction_test, so it shares
// store_db_test.go's TestMain, per-role pools and single skip site; export_test.go is what lets
// it reach the unexported args type from out here.
//
// Every extraction.* row this suite sees is its own debris. Production enqueues only through
// POST /v1/documents (cmd/submission/main.go), which nothing in this file drives, so never read
// these rows as evidence that extraction audit events are emitted in production.
//
// This file leaves none behind -- wkPurgeAuditLog drops each fixture tenant's audit rows at
// teardown. internal/audit's fixtures do not, and audit_log carries no foreign key for
// DELETE FROM tenants to cascade through, so those rows accumulate under dead tenant ids.
package extraction_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

const (
	wkExtractorName    = "extr-09-worker"
	wkExtractorVersion = "v1"

	// The worker's synthesised subject, pinned from outside the package: it lands in
	// audit_log.actor for every document.read the worker causes.
	wkActor = "extraction-worker"

	// The long case sleeps past River's 60s client default. The budget is an upper bound, not
	// a sleep, so the green path returns the moment the row finalizes and never spends it.
	wkLongExtract = 65 * time.Second
	wkLongBudget  = 90 * time.Second

	wkFastBudget = 30 * time.Second
	wkStopBudget = 5 * time.Second
	// Longer than River's 1s fetch poll, so a client that was going to fetch has had turns.
	wkSettle = 2 * time.Second

	// wkOtherQueue is a queue no other suite configures. The "wrong queue" client below sits
	// here rather than on river.QueueDefault because CI's queue job runs ./internal/submission
	// against this same Postgres first and leaves submission_poll rows available on
	// river.QueueDefault; a default-queue client here would fetch them, fail them as an
	// unhandled kind, and spend the control's budget doing it.
	wkOtherQueue = "extraction-test-other-queue"

	// The two terminal-outcome events and the actor they carry. "system", not wkActor: the
	// worker's synthesised subject lands on the document.read rows it causes, not on the
	// outcome of the job. Spelled here as cmd/submission's adapter spells them.
	wkSucceededEvent = "extraction.succeeded"
	wkFailedEvent    = "extraction.failed"
	wkAuditActor     = "system"
)

var wkErrRollback = errors.New("worker suite: intentional rollback")

// --- stubs ---------------------------------------------------------------------------

// wkExtract is the Extractor the worker specs drive. fn decides the outcome; the count is
// what proves how many attempts actually reached it.
type wkExtract struct {
	mu    sync.Mutex
	calls int
	fn    func(context.Context, extraction.Document) ([]extraction.FieldResult, error)
}

func (e *wkExtract) Name() string    { return wkExtractorName }
func (e *wkExtract) Version() string { return wkExtractorVersion }

func (e *wkExtract) Extract(ctx context.Context, doc extraction.Document) ([]extraction.FieldResult, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	return e.fn(ctx, doc)
}

func (e *wkExtract) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func wkOK() *wkExtract {
	return &wkExtract{fn: func(context.Context, extraction.Document) ([]extraction.FieldResult, error) {
		return wkOneField(), nil
	}}
}

func wkFailing(err error) *wkExtract {
	return &wkExtract{fn: func(context.Context, extraction.Document) ([]extraction.FieldResult, error) {
		return nil, err
	}}
}

// wkSlow honours ctx: without ExtractWorker's own Timeout the executor cancels the job at 60s
// and this returns that cancellation rather than sleeping on regardless.
func wkSlow(d time.Duration) *wkExtract {
	return &wkExtract{fn: func(ctx context.Context, _ extraction.Document) ([]extraction.FieldResult, error) {
		select {
		case <-time.After(d):
			return wkOneField(), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
}

func wkOneField() []extraction.FieldResult {
	return []extraction.FieldResult{{Field: extraction.Field{Name: "invoice_number", Value: stPtr("INV-EXTR-09")}, Alternatives: []extraction.Field{}}}
}

// wkSeen is one OpenDocument call: the identity the worker synthesised onto the seam's
// context, and the document it asked for.
type wkSeen struct {
	id  auth.Identity
	ok  bool
	doc string
}

// wkOpener stands in for the closure cmd/submission builds over document.Service.Open. The
// call count is this package's proxy for "one document.read row per attempt": the audit write
// lives in internal/document, which deps_test.go's fence keeps out of here, and the one-Open-
// one-row half is already pinned by internal/document/store_adversarial_test.go:521.
type wkOpener struct {
	mu   sync.Mutex
	seen []wkSeen
	body []byte
	err  error
}

func (o *wkOpener) open(ctx context.Context, documentID string) (extraction.Document, error) {
	id, ok := auth.IdentityFromContext(ctx)
	o.mu.Lock()
	o.seen = append(o.seen, wkSeen{id: id, ok: ok, doc: documentID})
	o.mu.Unlock()
	if o.err != nil {
		return extraction.Document{}, o.err
	}
	return extraction.Document{Bytes: o.body, ContentType: "application/pdf"}, nil
}

func (o *wkOpener) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.seen)
}

func (o *wkOpener) first(t *testing.T) wkSeen {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.seen) == 0 {
		t.Fatalf("OpenDocument was never called; the assertions below would pass vacuously")
	}
	return o.seen[0]
}

func wkNewOpener() *wkOpener { return &wkOpener{body: []byte("extr-09 worker fixture")} }

// wkForeignArgs is the other worker TestRLS_ExtractAddToRegistersOnTheCallersBundle puts on
// the shared bundle. It is declared here rather than borrowed from another package because
// deps_test.go's import fence would reject the import.
type wkForeignArgs struct {
	TenantID string `json:"tenant_id"`
	Note     string `json:"note"`
}

func (wkForeignArgs) Kind() string     { return "extraction_test_foreign" }
func (a wkForeignArgs) Tenant() string { return a.TenantID }

func (wkForeignArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: wkOtherQueue, MaxAttempts: 1}
}

type wkForeignWorker struct {
	river.WorkerDefaults[wkForeignArgs]
}

func (*wkForeignWorker) Work(context.Context, *river.Job[wkForeignArgs]) error { return nil }

// wkImmediate walks a failing job through its attempts in seconds. The backoff schedule is
// not this subtask's claim.
type wkImmediate struct{}

func (wkImmediate) NextRetry(*rivertype.JobRow) time.Time { return time.Now() }

// --- harness -------------------------------------------------------------------------

// wkStubReader is a PageReader over arbitrary bytes. Most specs here carry a text fixture
// pdfium refuses, and page rendering is not what they are about; the two that need real pixels
// build their own PageStore over PDFiumReader.
type wkStubReader struct {
	pages int
	// zeroWidth yields ImageWidth: 0. PageStore.Ingest copies it through unvalidated, so the
	// row reaches writePageImagesTx and extraction_page_images_width_px_check refuses it with
	// SQLSTATE 23514. The only lever in this suite that fails the page-row write and nothing
	// upstream of it.
	zeroWidth bool

	// tokens supplies page i+1's Tokens when set (index i), mirroring psReader
	// (pagestore_test.go). Left nil for specs that do not care, so Page.Tokens stays nil.
	tokens [][]extraction.Token
}

func (*wkStubReader) Name() string    { return "extr-09-stub-reader" }
func (*wkStubReader) Version() string { return "v1" }

func (r *wkStubReader) Read(_ context.Context, _ extraction.Document, onPage func(extraction.Page) error) (extraction.PageResult, error) {
	width := prLetterWidthPx
	if r.zeroWidth {
		width = 0
	}
	for i := 1; i <= r.pages; i++ {
		var toks []extraction.Token
		if i-1 < len(r.tokens) {
			toks = r.tokens[i-1]
		}
		if err := onPage(extraction.Page{
			Number:      i,
			WidthPt:     612,
			HeightPt:    792,
			ImageWidth:  width,
			ImageHeight: prLetterHeightPx,
			ImagePNG:    []byte("\x89PNG\r\n\x1a\nextr-09 stub page"),
			Tokens:      toks,
		}); err != nil {
			return extraction.PageResult{}, err
		}
	}
	return extraction.PageResult{Pages: r.pages}, nil
}

// wkErrReader is the only PageReader in this file whose Read fails. FailureKindPerStage's
// text lever needs one: wkStubReader carries no error field, and the internal suite's rtReader
// is in package extraction and invisible from out here.
type wkErrReader struct{ err error }

func (*wkErrReader) Name() string    { return "extr-17-err-reader" }
func (*wkErrReader) Version() string { return "v1" }

func (r *wkErrReader) Read(context.Context, extraction.Document, func(extraction.Page) error) (extraction.PageResult, error) {
	return extraction.PageResult{}, r.err
}

// wkPageSink records the worker's PUTs and, per call, whether the context it arrived on
// carried an auth identity. failOn > 0 errors on the call of that number.
type wkPageSink struct {
	mu       sync.Mutex
	keys     []string
	identity []bool
	seen     int
	failOn   int
	err      error
}

func (s *wkPageSink) put(ctx context.Context, key string, _ []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen++
	_, ok := auth.IdentityFromContext(ctx)
	s.identity = append(s.identity, ok)
	if s.failOn > 0 && s.seen == s.failOn {
		return s.err
	}
	s.keys = append(s.keys, key)
	return nil
}

func (s *wkPageSink) snapshot() (keys []string, identity []bool, calls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.keys), slices.Clone(s.identity), s.seen
}

// wkAuditRecorder is the Audit every worker in this file carries. It records the event AND
// writes the row on the tx it was handed, so the row shares the worker's transaction and is
// readable per tenant. A no-op stub would satisfy the nil guard while observing nothing.
//
// The payload map mirrors cmd/submission's newExtractionAuditor. It is a copy on purpose:
// deps_test.go's fence keeps internal/audit out of this package, and the mapping itself is
// pinned from that end in cmd/submission/extraction_audit_test.go. What the copy buys here is
// the transaction, not the key names.
type wkAuditRecorder struct {
	mu   sync.Mutex
	seen []extraction.ExtractionAudit
}

func (r *wkAuditRecorder) record(ctx context.Context, tx pgx.Tx, ev extraction.ExtractionAudit) error {
	r.mu.Lock()
	r.seen = append(r.seen, ev)
	r.mu.Unlock()

	if tx == nil {
		return errors.New("the audit port was handed a nil tx; the row cannot share the worker's transaction")
	}
	event, payload, err := wkAuditWire(ev)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO audit_log (actor, event, payload) VALUES ($1, $2, $3)`,
		wkAuditActor, event, payload)
	return err
}

func (r *wkAuditRecorder) events() []extraction.ExtractionAudit {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.seen)
}

// wkAuditWire renders one event the way the adapter does: the branch picks the event name and
// the two keys that branch owns.
func wkAuditWire(ev extraction.ExtractionAudit) (event, payload string, err error) {
	m := map[string]any{
		"document_id":       ev.DocumentID,
		"extraction_job_id": ev.ExtractionJobID,
		"extractor":         ev.Extractor,
		"extractor_version": ev.ExtractorVersion,
	}
	event = wkFailedEvent
	if ev.Succeeded {
		event = wkSucceededEvent
		m["field_count"] = ev.FieldCount
		m["flagged_count"] = ev.FlaggedCount
	} else {
		m["state"] = ev.State
		m["failure_kind"] = string(ev.FailureKind)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", "", fmt.Errorf("marshal audit payload: %w", err)
	}
	return event, string(b), nil
}

// wkAuditRow is one stored extraction.* audit row. raw is kept alongside the decoded map: a
// key stored with an empty value is present in the text and absent from a %v of the map.
type wkAuditRow struct {
	event   string
	raw     string
	payload map[string]any
}

func (r wkAuditRow) keys() []string {
	out := make([]string, 0, len(r.payload))
	for k := range r.payload {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// wkAuditRows reads every extraction.* audit row for a tenant, oldest first. Superuser: the
// rows are read back outside any tenant transaction.
func wkAuditRows(t *testing.T, ctx context.Context, tenantID string) []wkAuditRow {
	t.Helper()
	rows, err := stRequire(t).super.Query(ctx,
		`SELECT event, payload::text FROM audit_log
		  WHERE tenant_id = $1 AND event LIKE 'extraction.%' ORDER BY id`, tenantID)
	if err != nil {
		t.Fatalf("read audit_log for tenant %s: %v", tenantID, err)
	}
	defer rows.Close()

	out := []wkAuditRow{}
	for rows.Next() {
		var r wkAuditRow
		if err := rows.Scan(&r.event, &r.raw); err != nil {
			t.Fatalf("scan audit_log row for tenant %s: %v", tenantID, err)
		}
		if err := json.Unmarshal([]byte(r.raw), &r.payload); err != nil {
			t.Fatalf("stored payload %q is not a JSON object: %v", r.raw, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read audit_log for tenant %s: %v", tenantID, err)
	}
	return out
}

// wkAuditRowsForJob narrows to one extraction_jobs row, so arms sharing a tenant do not count
// each other's events.
func wkAuditRowsForJob(t *testing.T, ctx context.Context, tenantID, extractionJobID string) []wkAuditRow {
	t.Helper()
	out := []wkAuditRow{}
	for _, r := range wkAuditRows(t, ctx, tenantID) {
		if id, _ := r.payload["extraction_job_id"].(string); id == extractionJobID {
			out = append(out, r)
		}
	}
	return out
}

// wkPageKeys is the key list a PageStore must produce for one document's bytes.
func wkPageKeys(tenantID string, body []byte, pages int) []string {
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	out := make([]string, 0, pages)
	for i := 1; i <= pages; i++ {
		out = append(out, stPageKey(tenantID, hash, i))
	}
	return out
}

// wkPageRowIDs returns a document's page-image row ids in page order. A whole-set replace mints
// new ids; a replace that never ran leaves the old ones, which a row COUNT cannot see because
// the keys are content-derived and identical either way.
func wkPageRowIDs(t *testing.T, ctx context.Context, documentID string) []string {
	t.Helper()
	rows, err := stRequire(t).super.Query(ctx,
		`SELECT id::text FROM extraction_page_images WHERE document_id = $1 ORDER BY page_number`,
		documentID)
	if err != nil {
		t.Fatalf("read page-image row ids for document %s: %v", documentID, err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan page-image row id for document %s: %v", documentID, err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read page-image row ids for document %s: %v", documentID, err)
	}
	return out
}

// wkWorker wires a stub PageStore and a fresh recording auditor: Work renders unconditionally
// and refuses a nil Audit, so a spec that leaves either unset fails on the first job rather
// than quietly skipping the page inventory or the audit row.
func wkWorker(t *testing.T, ext extraction.Extractor, op *wkOpener) *extraction.ExtractWorker {
	t.Helper()
	return wkWorkerAudit(t, ext, op, &wkAuditRecorder{})
}

// wkWorkerAudit is wkWorker with the caller's recorder, for the specs that read back what the
// worker emitted.
func wkWorkerAudit(t *testing.T, ext extraction.Extractor, op *wkOpener, rec *wkAuditRecorder) *extraction.ExtractWorker {
	t.Helper()
	return wkWorkerPages(t, ext, op, &extraction.PageStore{
		Reader: &wkStubReader{pages: 1},
		Sink:   (&wkPageSink{}).put,
	}, rec)
}

func wkWorkerPages(t *testing.T, ext extraction.Extractor, op *wkOpener, pages *extraction.PageStore, rec *wkAuditRecorder) *extraction.ExtractWorker {
	t.Helper()
	return &extraction.ExtractWorker{
		Pool: stRequire(t).app, Extractor: ext, Open: op.open, Pages: pages, Audit: rec.record,
	}
}

// wkWorkerText is wkWorkerAudit with the text seam wired. Rules is set too, and must be: Work
// calls it on the text branch, so a nil func would panic instead of failing the stage under
// test.
func wkWorkerText(t *testing.T, ext extraction.Extractor, op *wkOpener, text extraction.PageReader, rec *wkAuditRecorder) *extraction.ExtractWorker {
	t.Helper()
	ew := wkWorkerAudit(t, ext, op, rec)
	ew.Text = text
	ew.Rules = func(context.Context, string, string) ([]extraction.AnchorRule, error) {
		return []extraction.AnchorRule{}, nil
	}
	return ew
}

// wkPDFiumPages is the page store the two corpus specs drive: the real renderer, a recording
// sink.
func wkPDFiumPages(sink *wkPageSink) *extraction.PageStore {
	return &extraction.PageStore{Reader: extraction.NewPDFiumReader(), Sink: sink.put}
}

// wkClient builds a working client over queues, registering ew through extraction.AddTo --
// the same call cmd/submission will make.
func wkClient(t *testing.T, ew *extraction.ExtractWorker, queues ...string) *queue.Client {
	t.Helper()
	bundle := river.NewWorkers()
	extraction.AddTo(bundle, ew)

	qs := make(map[string]river.QueueConfig, len(queues))
	for _, q := range queues {
		qs[q] = river.QueueConfig{MaxWorkers: 2}
	}
	c, err := queue.New(stRequire(t).app, queue.Config{
		Queues: qs, Workers: bundle, RetryPolicy: wkImmediate{},
	})
	if err != nil {
		t.Fatalf("build worker client: %v", err)
	}
	return c
}

// wkInsertClient enqueues without fetching. Workers stays nil: River's InsertTx checks kind
// registration whenever the bundle is non-nil, so an empty bundle would reject the insert.
func wkInsertClient(t *testing.T) *queue.Client {
	t.Helper()
	c, err := queue.New(stRequire(t).app, queue.Config{})
	if err != nil {
		t.Fatalf("build insert-only client: %v", err)
	}
	return c
}

// wkStart starts the pool and drains it at test end. A spec that fails mid-job falls back to
// StopAndCancel rather than leaving a worker goroutine running into the next test.
func wkStart(t *testing.T, c *queue.Client) {
	t.Helper()
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start worker pool: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), wkStopBudget)
		defer cancel()
		if err := c.Stop(stopCtx); err == nil {
			return
		}
		hardCtx, hardCancel := context.WithTimeout(context.Background(), wkStopBudget)
		defer hardCancel()
		if err := c.River().StopAndCancel(hardCtx); err != nil {
			t.Errorf("stop worker pool: %v", err)
		}
	})
}

// wkCleanupInfra deletes what DELETE FROM tenants does not reach: river_job carries no
// tenant_id, and idempotency_keys has no foreign key to tenants. Register it AFTER stTenant so
// LIFO drains these first.
func wkCleanupInfra(t *testing.T, tenantID string) {
	t.Helper()
	h := stRequire(t)
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
		wkPurgeAuditLog(t, ctx, tenantID)
	})
}

// wkPurgeAuditLog deletes a tenant's audit rows. audit_log has no foreign key to tenants, so
// DELETE FROM tenants does not reach them, and audit_log_append_only() refuses DELETE for
// every role including the owner -- session_replication_role='replica' is the one bypass, is
// superuser-only, and SET LOCAL confines it to this transaction.
func wkPurgeAuditLog(t *testing.T, ctx context.Context, tenantID string) {
	t.Helper()
	tx, err := stRequire(t).super.Begin(ctx)
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
}

// wkFixture seeds a tenant and a document and registers both teardowns.
func wkFixture(t *testing.T, ctx context.Context) (tenantID, documentID string) {
	t.Helper()
	tenantID, documentID = stTenant(t, ctx)
	wkCleanupInfra(t, tenantID)
	return tenantID, documentID
}

// wkEnqueue inserts one extraction job through the outbox. A nil opts leaves the queue and
// max_attempts on the row to extractArgs.InsertOpts() -- the wire values
// TestRLS_ExtractJobLandsOnTheExtractionQueue reads back.
func wkEnqueue(t *testing.T, ctx context.Context, c *queue.Client, tenantID, documentID string, opts *river.InsertOpts) int64 {
	t.Helper()
	pool := stRequire(t).app
	key := uuid.NewString()
	err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		skipped, e := c.EnqueueTx(ctx, tx, tenantID, key,
			extraction.NewExtractArgsForTest(tenantID, documentID, key), opts)
		if e != nil {
			return e
		}
		if skipped {
			return errors.New("EnqueueTx reported skipped for a fresh key")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("enqueue extraction job: %v", err)
	}

	var id int64
	if err := pool.QueryRow(ctx,
		`SELECT id FROM river_job WHERE args->>'idempotency_key' = $1`, key).Scan(&id); err != nil {
		t.Fatalf("look up the river_job row for key %s: %v", key, err)
	}
	return id
}

type wkRiverRow struct {
	state       string
	attempt     int
	maxAttempts int
	queue       string
	kind        string
}

func wkRiverJob(t *testing.T, id int64) wkRiverRow {
	t.Helper()
	var r wkRiverRow
	if err := stRequire(t).app.QueryRow(context.Background(),
		`SELECT state::text, attempt, max_attempts, queue, kind FROM river_job WHERE id = $1`, id).
		Scan(&r.state, &r.attempt, &r.maxAttempts, &r.queue, &r.kind); err != nil {
		t.Fatalf("read river_job %d: %v", id, err)
	}
	return r
}

func wkAwaitRiverState(t *testing.T, id int64, want string, budget time.Duration, why string) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		r := wkRiverJob(t, id)
		if r.state == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("river_job %d is %q at attempt %d after %v, want %q -- %s",
				id, r.state, r.attempt, budget, want, why)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func wkExtractionJobID(t *testing.T, ctx context.Context, tenantID string, riverJobID int64) string {
	t.Helper()
	var id string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT id FROM extraction_jobs WHERE tenant_id = $1 AND river_job_id = $2`,
		tenantID, riverJobID).Scan(&id); err != nil {
		t.Fatalf("look up the extraction_jobs row for river job %d: %v", riverJobID, err)
	}
	return id
}

func wkExtractionJobCount(t *testing.T, ctx context.Context, tenantID string) int {
	t.Helper()
	var n int
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT count(*) FROM extraction_jobs WHERE tenant_id = $1`, tenantID).Scan(&n); err != nil {
		t.Fatalf("count extraction_jobs for tenant %s: %v", tenantID, err)
	}
	return n
}

func wkAssertJobAttempts(t *testing.T, ctx context.Context, jobID string, want int) {
	t.Helper()
	var got int
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT attempts FROM extraction_jobs WHERE id = $1`, jobID).Scan(&got); err != nil {
		t.Fatalf("read attempts for job %s: %v", jobID, err)
	}
	if got != want {
		t.Errorf("extraction job %s records %d attempt(s), want %d", jobID, got, want)
	}
}

// wkAssertJobExtractor pins the worker's own wiring: Name() and Version() reach their own
// columns, in that order. store_db_test.go pins that ensureJobTx binds them; nothing else
// pins which one the worker passes first.
// wkStr renders a nullable text column for a failure message: %v on a *string prints an address.
func wkStr(p *string) string {
	if p == nil {
		return "NULL"
	}
	return strconv.Quote(*p)
}

func wkAssertJobExtractor(t *testing.T, ctx context.Context, jobID string) {
	t.Helper()
	var name, version string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT extractor, extractor_version FROM extraction_jobs WHERE id = $1`, jobID).
		Scan(&name, &version); err != nil {
		t.Fatalf("read extractor columns for job %s: %v", jobID, err)
	}
	if name != wkExtractorName || version != wkExtractorVersion {
		t.Errorf("extraction job %s stored {extractor %q, extractor_version %q}, want {%q, %q}",
			jobID, name, version, wkExtractorName, wkExtractorVersion)
	}
}

// --- specs ---------------------------------------------------------------------------

// Core AC-9 on the wire: River applies cmp.Or(workUnit.Timeout(), clientJobTimeout), so
// without ExtractWorker's own Timeout the executor cancels this at the 60s client default and
// River retries it.
func TestRLS_ExtractWorkerJobExceedingSixtySecondsCompletes(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	op := wkNewOpener()
	ext := wkSlow(wkLongExtract)
	c := wkClient(t, wkWorker(t, ext, op), extraction.QueueName)
	id := wkEnqueue(t, ctx, c, tenantID, documentID, nil)
	wkStart(t, c)

	wkAwaitRiverState(t, id, "completed", wkLongBudget,
		"without ExtractWorker.Timeout the executor cancels this at River's 60s client default and retries it")

	if r := wkRiverJob(t, id); r.attempt != 1 {
		t.Errorf("river_job %d completed on attempt %d, want 1; a 60s cancellation would have retried it", id, r.attempt)
	}
	if n := ext.count(); n != 1 {
		t.Errorf("the extractor ran %d time(s), want 1", n)
	}
	stAssertJobState(t, ctx, wkExtractionJobID(t, ctx, tenantID, id), "succeeded")
}

// Core AC-10 on the wire: the queue and the attempt cap the row carries come from
// extractArgs.InsertOpts(), not from a caller passing opts by hand.
func TestRLS_ExtractJobLandsOnTheExtractionQueue(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	id := wkEnqueue(t, ctx, wkInsertClient(t), tenantID, documentID, nil)

	r := wkRiverJob(t, id)
	if r.queue != "extraction" {
		t.Errorf("river_job %d landed on queue %q, want %q", id, r.queue, "extraction")
	}
	if r.maxAttempts != 3 {
		t.Errorf("river_job %d carries max_attempts %d, want 3", id, r.maxAttempts)
	}
	if r.kind != "extraction_extract" {
		t.Errorf("river_job %d carries kind %q, want %q", id, r.kind, "extraction_extract")
	}
}

// AC #5's half-wiring: extractArgs.InsertOpts names the extraction queue but the client's
// Config.Queues never does, so nothing ever fetches the work -- the shape cmd/submission lands
// in if EXTR-01-10 registers the worker and forgets the queue. The control is what stops a
// client that cannot fetch AT ALL from passing this for the wrong reason.
func TestRLS_ExtractJobIsNotFetchedByAClientWithoutTheExtractionQueue(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	op := wkNewOpener()
	ext := wkOK()
	c := wkClient(t, wkWorker(t, ext, op), wkOtherQueue)

	id := wkEnqueue(t, ctx, c, tenantID, documentID, nil)
	// The control is the SAME kind on the SAME client, forced onto the queue that client does
	// configure. It is what makes the queue -- not the kind, not a dead client -- the only
	// difference between the two jobs.
	control := wkEnqueue(t, ctx, c, tenantID, documentID,
		&river.InsertOpts{Queue: wkOtherQueue, MaxAttempts: 1})
	wkStart(t, c)

	wkAwaitRiverState(t, control, "completed", wkFastBudget,
		"the control is the same kind on a queue this client configures, so a client that cannot fetch it at all would leave the assertions below passing for the wrong reason")
	time.Sleep(wkSettle)

	if got := wkRiverJob(t, id); got.state != "available" || got.attempt != 0 {
		t.Errorf("the extraction job is {state %q, attempt %d}, want {available, 0}; a client configured only for %q fetched a job on the %q queue",
			got.state, got.attempt, wkOtherQueue, extraction.QueueName)
	}
	if n := ext.count(); n != 1 {
		t.Errorf("the extractor ran %d time(s), want 1 -- the control only", n)
	}
	if n := op.count(); n != 1 {
		t.Errorf("OpenDocument was called %d time(s), want 1 -- the control only", n)
	}
}

// The positive half of the pair above: the same job on a client that DOES configure the
// extraction queue runs to completion. The Open count is D-11 made executable on the success
// path -- one attempt reads the bytes once.
func TestRLS_ExtractJobIsFetchedByTheExtractionQueueClient(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	op := wkNewOpener()
	ext := wkOK()
	c := wkClient(t, wkWorker(t, ext, op), extraction.QueueName)
	id := wkEnqueue(t, ctx, c, tenantID, documentID, nil)
	wkStart(t, c)

	wkAwaitRiverState(t, id, "completed", wkFastBudget,
		"a client that configures the extraction queue must fetch and finish this job")

	if r := wkRiverJob(t, id); r.attempt != 1 {
		t.Errorf("river_job %d completed on attempt %d, want 1", id, r.attempt)
	}
	if n := op.count(); n != 1 {
		t.Errorf("OpenDocument was called %d time(s) across one attempt, want 1", n)
	}
	if seen := op.first(t); seen.doc != documentID {
		t.Errorf("OpenDocument was asked for document %q, want the args' %q", seen.doc, documentID)
	}

	xid := wkExtractionJobID(t, ctx, tenantID, id)
	stAssertJobState(t, ctx, xid, "succeeded")
	stAssertFieldResultCount(t, ctx, xid, len(wkOneField()))
	wkAssertJobAttempts(t, ctx, xid, 1)
	wkAssertJobExtractor(t, ctx, xid)

	// attempts follows River's attempt rather than a constant: a job that succeeds on a later
	// attempt records that attempt. One run at attempt 1 cannot tell the two apart.
	const secondAttemptRiverJobID = int64(909501)
	if err := wkWorker(t, wkOK(), wkNewOpener()).Work(ctx,
		extraction.NewExtractJobForTest(secondAttemptRiverJobID, 2, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work on attempt 2: %v", err)
	}
	wkAssertJobAttempts(t, ctx, wkExtractionJobID(t, ctx, tenantID, secondAttemptRiverJobID), 2)
}

// Core AC-8. extraction_field_results has no unique key on (extraction_job_id, field_name) and
// the insert is bare, so a doubled write really does double the rows. The state assertion
// after the replay is the load-bearing half: it is what fails if the tx1 terminal short-circuit
// goes and the marker alone is left to skip the tx2 closure, stranding the row in "extracting".
func TestRLS_ExtractWorkerWritesOneResultSetPerJobOnReplay(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	op := wkNewOpener()
	ew := wkWorker(t, wkOK(), op)
	const riverJobID = int64(909001)
	key := uuid.NewString()

	if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, key)); err != nil {
		t.Fatalf("first Work: %v", err)
	}
	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertFieldResultCount(t, ctx, xid, len(wkOneField()))
	stAssertJobState(t, ctx, xid, "succeeded")

	// The replay River performs after a worker committed its effect and died before the ack.
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 2, 3, tenantID, documentID, key)); err != nil {
		t.Fatalf("replayed Work: %v", err)
	}
	stAssertFieldResultCount(t, ctx, xid, len(wkOneField()))
	stAssertJobState(t, ctx, xid, "succeeded")

	if n := wkExtractionJobCount(t, ctx, tenantID); n != 1 {
		t.Errorf("the tenant holds %d extraction_jobs row(s) after a replay of one river job, want 1", n)
	}

	// dead_lettered is terminal on replay for the same reason succeeded is. The short-circuit
	// covers both states, and the replay above exercises only one of them.
	const deadRiverJobID = int64(909002)
	boom := errors.New("extr-09 replay: the extractor always fails")
	if err := wkWorker(t, wkFailing(boom), wkNewOpener()).Work(ctx,
		extraction.NewExtractJobForTest(deadRiverJobID, 3, 3, tenantID, documentID, uuid.NewString())); !errors.Is(err, boom) {
		t.Fatalf("Work on the final attempt returned %v, want the extractor's error", err)
	}
	did := wkExtractionJobID(t, ctx, tenantID, deadRiverJobID)
	stAssertJobState(t, ctx, did, "dead_lettered")

	replayExt, replayOp := wkOK(), wkNewOpener()
	if err := wkWorker(t, replayExt, replayOp).Work(ctx,
		extraction.NewExtractJobForTest(deadRiverJobID, 4, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("replayed Work on a dead-lettered job: %v", err)
	}
	stAssertJobState(t, ctx, did, "dead_lettered")
	stAssertFieldResultCount(t, ctx, did, 0)
	if n := replayExt.count(); n != 0 {
		t.Errorf("the replay of a dead-lettered job ran the extractor %d time(s), want 0", n)
	}
	if n := replayOp.count(); n != 0 {
		t.Errorf("the replay of a dead-lettered job called OpenDocument %d time(s), want 0", n)
	}
}

// The other half of Core AC-8, and the only spec that isolates queue.OncePerJob: with the
// marker already present the result write must not happen even on a job the worker has never
// seen. The unmarked control in the same test is what stops "0 rows" from passing on a worker
// that writes no results at all.
func TestRLS_ExtractWorkerResultWriteIsGuardedByTheJobMarker(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	op := wkNewOpener()
	ew := wkWorker(t, wkOK(), op)
	const (
		controlRiverJobID = int64(909101)
		markedRiverJobID  = int64(909102)
	)

	// Control: no marker, so the write happens and the assertion below is about the marker.
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(controlRiverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("control Work: %v", err)
	}
	control := wkExtractionJobID(t, ctx, tenantID, controlRiverJobID)
	stAssertFieldResultCount(t, ctx, control, len(wkOneField()))

	// Plant the marker an earlier attempt would have committed. idempotency_keys is keyed
	// (tenant_id, key), so this is the row OncePerJob's ON CONFLICT DO NOTHING collides with.
	if err := db.WithinTenantTx(ctx, stRequire(t).app, tenantID, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, $2)`,
			tenantID, fmt.Sprintf("job:%d", markedRiverJobID))
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 1 {
			return fmt.Errorf("planted %d marker row(s), want 1", ct.RowsAffected())
		}
		return nil
	}); err != nil {
		t.Fatalf("plant the job marker: %v", err)
	}

	if err := ew.Work(ctx, extraction.NewExtractJobForTest(markedRiverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work on an already-marked job returned %v, want nil: the job is acked, not retried", err)
	}
	marked := wkExtractionJobID(t, ctx, tenantID, markedRiverJobID)
	stAssertFieldResultCount(t, ctx, marked, 0)
	// A job whose result write was skipped must not report success: the marker, the rows and
	// the state advance share one fate only while the advance sits inside the closure.
	stAssertJobState(t, ctx, marked, "extracting")
}

// AC #4 of the story, corrected against the live schema: dead_lettered and failed are
// extraction_jobs states, never river_job states, and River's column is attempt, not attempts.
// Both tables have to agree or the queue and the domain disagree about the same job. The Open
// count is D-11 made executable on the failure path -- three attempts read the bytes three
// times, and a worker that memoised them across attempts would report one.
func TestRLS_ExtractWorkerFailureIsTerminalAfterThreeAttempts(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	boom := errors.New("extr-09: the extractor always fails")

	// The non-terminal arm, driven directly: an attempt below the cap leaves the job "failed".
	// The client path below settles on the terminal state and never observes this one.
	const probeRiverJobID = int64(909401)
	if err := wkWorker(t, wkFailing(boom), wkNewOpener()).Work(ctx,
		extraction.NewExtractJobForTest(probeRiverJobID, 1, 3, tenantID, documentID, uuid.NewString())); !errors.Is(err, boom) {
		t.Fatalf("Work on attempt 1 of 3 returned %v, want the extractor's error", err)
	}
	pid := wkExtractionJobID(t, ctx, tenantID, probeRiverJobID)
	stAssertJobState(t, ctx, pid, "failed")
	wkAssertJobAttempts(t, ctx, pid, 1)
	if got := stJobLastError(t, ctx, pid); got == nil || *got != boom.Error() {
		t.Errorf("extraction job %s records last_error %s, want %q", pid, wkStr(got), boom.Error())
	}

	op := wkNewOpener()
	ext := wkFailing(boom)
	c := wkClient(t, wkWorker(t, ext, op), extraction.QueueName)
	id := wkEnqueue(t, ctx, c, tenantID, documentID, nil)
	wkStart(t, c)

	wkAwaitRiverState(t, id, "discarded", wkFastBudget,
		"an extractor that always fails must exhaust MaxAttempts rather than retrying forever")

	if r := wkRiverJob(t, id); r.attempt != 3 {
		t.Errorf("river_job %d was discarded at attempt %d, want 3", id, r.attempt)
	}
	xid := wkExtractionJobID(t, ctx, tenantID, id)
	stAssertJobState(t, ctx, xid, "dead_lettered")
	wkAssertJobAttempts(t, ctx, xid, 3)

	if got := stJobLastError(t, ctx, xid); got == nil || *got != boom.Error() {
		t.Errorf("extraction job %s records last_error %s, want %q", xid, wkStr(got), boom.Error())
	}
	if n := op.count(); n != 3 {
		t.Errorf("OpenDocument was called %d time(s) across 3 attempts, want 3", n)
	}
	if n := ext.count(); n != 3 {
		t.Errorf("the extractor ran %d time(s) across 3 attempts, want 3", n)
	}
	stAssertFieldResultCount(t, ctx, xid, 0)
}

// D-12's containment made executable. The worker synthesises its identity from the job's own
// args; a caller's context is never the tenant authority, because a River worker's context
// belongs to the client, not to whoever enqueued the work.
func TestRLS_ExtractWorkerBuildsIdentityFromArgsNotContext(t *testing.T) {
	ctx := t.Context()
	tenantA, documentA := wkFixture(t, ctx)
	tenantB, _ := wkFixture(t, ctx)

	ctxB := auth.WithIdentity(ctx, auth.Identity{
		Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB,
	})
	if got, ok := auth.IdentityFromContext(ctxB); !ok || got.TenantID != tenantB {
		t.Fatalf("the context plant did not take (present %v, tenant %q); every assertion below would pass vacuously", ok, got.TenantID)
	}

	op := wkNewOpener()
	ew := wkWorker(t, wkOK(), op)
	if err := ew.Work(ctxB, extraction.NewExtractJobForTest(909201, 1, 3, tenantA, documentA, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}

	seen := op.first(t)
	if !seen.ok {
		t.Errorf("OpenDocument was handed a context carrying no identity; the worker dropped the caller's context without synthesising one of its own")
	}
	if seen.id.TenantID != tenantA {
		t.Errorf("OpenDocument saw tenant %q, want args.TenantID %q; the worker passed the caller's context through", seen.id.TenantID, tenantA)
	}
	if seen.id.Subject != wkActor {
		t.Errorf("OpenDocument saw subject %q, want %q; that value lands in audit_log.actor for every document.read the worker causes", seen.id.Subject, wkActor)
	}
}

// A cancelled context must stop the worker before it touches anything. "Writes nothing" alone
// is satisfied by a Work that never got far enough to write, so the live-context control below
// is what makes the three zero-assertions mean something.
func TestRLS_ExtractWorkerRespectsContextCancellation(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	op := wkNewOpener()
	ext := wkOK()
	ew := wkWorker(t, ext, op)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	err := ew.Work(cancelled, extraction.NewExtractJobForTest(909301, 1, 3, tenantID, documentID, uuid.NewString()))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Work on a cancelled context returned %v, want an error wrapping context.Canceled", err)
	}
	if n := wkExtractionJobCount(t, ctx, tenantID); n != 0 {
		t.Errorf("a cancelled Work left %d extraction_jobs row(s), want 0", n)
	}
	if n := op.count(); n != 0 {
		t.Errorf("a cancelled Work called OpenDocument %d time(s), want 0", n)
	}
	if n := ext.count(); n != 0 {
		t.Errorf("a cancelled Work ran the extractor %d time(s), want 0", n)
	}

	// Control: the identical call on a live context does all three things.
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(909302, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("control Work on a live context: %v; the zero-assertions above prove nothing without it", err)
	}
	if n := wkExtractionJobCount(t, ctx, tenantID); n != 1 {
		t.Errorf("the control Work left %d extraction_jobs row(s), want 1; the zero-assertions above prove nothing without it", n)
	}
	if n := op.count(); n != 1 {
		t.Errorf("the control Work called OpenDocument %d time(s), want 1; the zero-assertions above prove nothing without it", n)
	}
}

// AC #9. A River client takes exactly one Workers bundle, and river.AddWorker panics on a
// duplicate kind. cmd/submission already gets its bundle from submission.Workers(sw, pw), so an
// extraction package that returned a second one would leave one bundle's workers unfetched.
func TestRLS_ExtractAddToRegistersOnTheCallersBundle(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	if n := reflect.TypeOf(extraction.AddTo).NumOut(); n != 0 {
		t.Errorf("AddTo returns %d value(s), want 0; a bundle it handed back would be a second bundle", n)
	}

	// The caller's bundle, with the caller's own worker on it first.
	bundle := river.NewWorkers()
	river.AddWorker(bundle, &wkForeignWorker{})
	extraction.AddTo(bundle, &extraction.ExtractWorker{Pool: stRequire(t).app})

	c, err := queue.New(stRequire(t).app, queue.Config{
		Queues:  map[string]river.QueueConfig{wkOtherQueue: {MaxWorkers: 1}},
		Workers: bundle,
	})
	if err != nil {
		t.Fatalf("build a client over the shared bundle: %v", err)
	}

	// InsertTx refuses a kind the client's bundle does not carry, so inserting BOTH kinds on
	// one client is the observable. Rolled back: this client is never started, and a stray
	// extraction job would be left for another spec's pool to fetch.
	key := uuid.NewString()
	err = db.WithinTenantTx(ctx, stRequire(t).app, tenantID, func(tx pgx.Tx) error {
		if _, e := c.EnqueueTx(ctx, tx, tenantID, key,
			extraction.NewExtractArgsForTest(tenantID, documentID, key), nil); e != nil {
			return fmt.Errorf("insert extraction_extract onto the shared bundle: %w", e)
		}
		if _, e := c.EnqueueTx(ctx, tx, tenantID, uuid.NewString(),
			wkForeignArgs{TenantID: tenantID, Note: uuid.NewString()}, nil); e != nil {
			return fmt.Errorf("insert the foreign kind onto the shared bundle: %w", e)
		}
		return wkErrRollback
	})
	if !errors.Is(err, wkErrRollback) {
		t.Fatalf("%v", err)
	}
}

// AC #1's other half. D-21 keeps the args type unexported so no package outside
// internal/extraction can construct one; Tenant() is what EnqueueTx fails closed on, and every
// other spec only proves it transitively.
func TestExtractArgsTypeIsUnexported(t *testing.T) {
	tenantID := uuid.NewString()
	args := extraction.NewExtractArgsForTest(tenantID, uuid.NewString(), uuid.NewString())

	rt := reflect.TypeOf(args)
	if rt == nil || rt.Name() == "" {
		t.Fatalf("the extraction args have no named type (%v); the case check below would pass vacuously", rt)
	}
	if r := []rune(rt.Name())[0]; !unicode.IsLower(r) {
		t.Errorf("the extraction args type is %q, which is exported", rt.Name())
	}
	if rt.PkgPath() != extractionPkg {
		t.Errorf("the extraction args type comes from %q, want %q", rt.PkgPath(), extractionPkg)
	}

	ts, ok := args.(queue.TenantScoped)
	if !ok {
		t.Fatalf("the extraction args do not implement queue.TenantScoped; EnqueueTx refuses args that cannot declare their tenant")
	}
	if got := ts.Tenant(); got != tenantID {
		t.Errorf("Tenant() = %q, want the args' TenantID %q", got, tenantID)
	}
}

// D-19 on the wire: the objects and the rows, end to end, with the real renderer over the
// committed corpus. The dimension assertion is the render's own grid, the number a canvas
// scales a normalised box by.
func TestRLS_ExtractWorkerWritesPageImagesThroughTheSink(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	raw := fxRead(t, fxNative3)
	op := &wkOpener{body: raw}
	ext := wkOK()
	sink := &wkPageSink{}

	const riverJobID = int64(909601)
	if err := wkWorkerPages(t, ext, op, wkPDFiumPages(sink), &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}

	want := wkPageKeys(tenantID, raw, 3)
	keys, identity, puts := sink.snapshot()
	if puts != 3 {
		t.Fatalf("the sink saw %d PUT(s), want 3 -- one per page of the corpus", puts)
	}
	if !slices.Equal(keys, want) {
		t.Errorf("the worker PUT\n  %v\nwant\n  %v", keys, want)
	}

	rows := stPageRows(t, ctx, documentID)
	if len(rows) != 3 {
		t.Fatalf("the document holds %d extraction_page_images row(s), want 3", len(rows))
	}
	for i, r := range rows {
		if r.Page != i+1 {
			t.Errorf("row %d records page %d, want %d", i, r.Page, i+1)
		}
		if r.StorageKey != want[i] {
			t.Errorf("page %d's row names %q, want the key the sink was handed, %q", i+1, r.StorageKey, want[i])
		}
		if r.WidthPx != prLetterWidthPx || r.HeightPx != prLetterHeightPx {
			t.Errorf("page %d's row records %dx%d px, want the render's own %dx%d",
				i+1, r.WidthPx, r.HeightPx, prLetterWidthPx, prLetterHeightPx)
		}
	}

	// ctx, not octx: the sink's credentials come from config, so it is fenced from a tenant
	// identity the way the extractor is. The opener, which RLS scopes, is the control -- it
	// DOES see one, so this is not simply a worker that synthesises no identity at all.
	for i, ok := range identity {
		if ok {
			t.Errorf("page %d's PUT arrived on a context carrying an auth identity; Ingest was handed the worker's tenant-scoped context", i+1)
		}
	}
	if seen := op.first(t); !seen.ok {
		t.Error("OpenDocument saw no identity either, so the identity-free assertions above prove nothing")
	}

	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, xid, "succeeded")
	stAssertFieldResultCount(t, ctx, xid, len(wkOneField()))
	if n := ext.count(); n != 1 {
		t.Errorf("the extractor ran %d time(s), want 1", n)
	}
}

// The other half of D-19's ordering: objects first, rows last. A sink that fails mid-render
// leaves orphan objects and NO rows, which reads as "no page images" and retries cleanly; the
// reverse order would leave a committed row naming a 404.
func TestRLS_ExtractWorkerFailsTheJobWhenThePageSinkFails(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	raw := fxRead(t, fxNative3)
	boom := errors.New("extr-09: the object store refused the page PUT")

	ext := wkOK()
	sink := &wkPageSink{failOn: 2, err: boom}
	const riverJobID = int64(909651)
	if err := wkWorkerPages(t, ext, &wkOpener{body: raw}, wkPDFiumPages(sink), &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); !errors.Is(err, boom) {
		t.Fatalf("Work returned %v, want the sink's error", err)
	}

	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, xid, "failed")
	if got := stJobLastError(t, ctx, xid); got == nil || *got != boom.Error() {
		t.Errorf("extraction job %s records last_error %s, want %q", xid, wkStr(got), boom.Error())
	}
	stAssertFieldResultCount(t, ctx, xid, 0)
	if rows := stPageRows(t, ctx, documentID); len(rows) != 0 {
		t.Errorf("a failed page PUT left %d extraction_page_images row(s), want 0: a committed row must name an object that was written", len(rows))
	}
	// The rows commit before Extract runs, so a sink failure means the extractor never ran.
	if n := ext.count(); n != 0 {
		t.Errorf("the extractor ran %d time(s) after the page sink failed, want 0 -- the page ingest is upstream of it", n)
	}
	if _, _, puts := sink.snapshot(); puts != 2 {
		t.Errorf("the sink saw %d call(s), want 2: the ingest ran on past the page that failed", puts)
	}

	// Control: the same document, the same worker shape, a sink that works. Without it the
	// zero-row assertion above passes on a worker that writes no page rows at all.
	const controlRiverJobID = int64(909653)
	if err := wkWorkerPages(t, wkOK(), &wkOpener{body: raw}, wkPDFiumPages(&wkPageSink{}), &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(controlRiverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("control Work: %v", err)
	}
	if rows := stPageRows(t, ctx, documentID); len(rows) != 3 {
		t.Fatalf("the control left %d page row(s), want 3; the zero-row assertion above proves nothing without it", len(rows))
	}

	// The final attempt is terminal for a sink failure the same way it is for an extractor one.
	const deadRiverJobID = int64(909652)
	if err := wkWorkerPages(t, wkOK(), &wkOpener{body: raw}, wkPDFiumPages(&wkPageSink{failOn: 1, err: boom}), &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(deadRiverJobID, 3, 3, tenantID, documentID, uuid.NewString())); !errors.Is(err, boom) {
		t.Fatalf("Work on the final attempt returned %v, want the sink's error", err)
	}
	stAssertJobState(t, ctx, wkExtractionJobID(t, ctx, tenantID, deadRiverJobID), "dead_lettered")
}

// --- EXTR-14-03: the worker records the layout it just read ---------------------------

// W-01, W-02: a succeeded job carries the v1: fingerprint and the anchor list its own page
// read observed, computed independently here so the test is not echoing the worker's own
// arithmetic back at itself.
func TestRLS_ExtractWorkerRecordsLayoutOnSuccess(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	raw := fxRead(t, fxCorpusTwoColumn)
	const riverJobID = int64(909901)
	if err := wkWorkerPages(t, wkOK(), &wkOpener{body: raw}, wkPDFiumPages(&wkPageSink{}), &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}
	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, xid, "succeeded")

	pages := rvCorpusPages(t, fxCorpusTwoColumn)
	wantObs := extraction.AnchorObservations(pages)
	if len(wantObs) != 7 {
		t.Fatalf("the independent computation over %s found %d anchor observation(s), want 7; the fixture drifted and every assertion below would compare against the wrong number", fxCorpusTwoColumn, len(wantObs))
	}
	wantFingerprint := extraction.Fingerprint(pages)
	if !strings.HasPrefix(wantFingerprint, "v1:") {
		t.Fatalf("the independent Fingerprint computation is %q, which does not start with v1:; FingerprintVersion or the fixture drifted", wantFingerprint)
	}

	row := stJobLayout(t, ctx, xid)
	if row.Fingerprint == nil {
		t.Fatalf("job %s has layout_fingerprint NULL after a succeeded run over a corpus document, want %q", xid, wantFingerprint)
	}
	if *row.Fingerprint != wantFingerprint {
		t.Errorf("job %s carries layout_fingerprint %q, want %q (computed independently over the same pages)", xid, *row.Fingerprint, wantFingerprint)
	}
	if !strings.HasPrefix(*row.Fingerprint, "v1:") {
		t.Errorf("layout_fingerprint %q does not start with v1:", *row.Fingerprint)
	}

	if row.Anchors == nil {
		t.Fatalf("job %s has layout_anchors NULL after a succeeded run, want a %d-element array", xid, len(wantObs))
	}
	gotObs, err := extraction.UnmarshalAnchorObservations([]byte(*row.Anchors))
	if err != nil {
		t.Fatalf("decode layout_anchors for job %s: %v", xid, err)
	}
	if len(gotObs) != 7 {
		t.Fatalf("job %s stored %d anchor observation(s), want 7 -- the stored array is not merely non-empty, it is the wrong length", xid, len(gotObs))
	}
	if !reflect.DeepEqual(gotObs, wantObs) {
		t.Errorf("job %s stored anchors\n  %+v\nwant (computed independently)\n  %+v", xid, gotObs, wantObs)
	}
}

// W-03 (AC-2): the layout write sits inside the page-row transaction, upstream of Extract, so
// a job whose extractor fails still carries both columns. Proof of ordering, not just that the
// columns can be populated at all.
func TestRLS_ExtractWorkerRecordsLayoutBeforeExtractFails(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	raw := fxRead(t, fxCorpusTwoColumn)
	boom := errors.New("extr-14-03: the extractor always fails")
	const riverJobID = int64(909902)
	if err := wkWorkerPages(t, wkFailing(boom), &wkOpener{body: raw}, wkPDFiumPages(&wkPageSink{}), &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); !errors.Is(err, boom) {
		t.Fatalf("Work returned %v, want the extractor's error", err)
	}
	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, xid, "failed")

	pages := rvCorpusPages(t, fxCorpusTwoColumn)
	wantFingerprint := extraction.Fingerprint(pages)
	wantObs := extraction.AnchorObservations(pages)

	row := stJobLayout(t, ctx, xid)
	if row.Fingerprint == nil || *row.Fingerprint != wantFingerprint {
		t.Errorf("job %s carries layout_fingerprint %s, want %q -- the layout write must survive an Extract failure because it happens before Extract runs", xid, wkStr(row.Fingerprint), wantFingerprint)
	}
	if row.Anchors == nil {
		t.Fatalf("job %s has layout_anchors NULL after Extract failed, want the %d observation(s) its own page read found", xid, len(wantObs))
	}
	gotObs, err := extraction.UnmarshalAnchorObservations([]byte(*row.Anchors))
	if err != nil {
		t.Fatalf("decode layout_anchors for job %s: %v", xid, err)
	}
	if !reflect.DeepEqual(gotObs, wantObs) {
		t.Errorf("job %s stored anchors %+v after Extract failed, want %+v -- the write happens before Extract regardless of its outcome", xid, gotObs, wantObs)
	}
}

// W-04 (AC-3): a page-ingest failure carries neither column, and still classifies as
// pages_not_rendered. The control proves the NULL assertions are not vacuous -- the same
// document through a working sink leaves both columns non-NULL.
func TestRLS_ExtractWorkerLeavesLayoutNullWhenPagesFail(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	raw := fxRead(t, fxCorpusTwoColumn)
	boom := errors.New("extr-14-03: the page sink refused the PUT")

	const riverJobID = int64(909903)
	if err := wkWorkerPages(t, wkOK(), &wkOpener{body: raw}, wkPDFiumPages(&wkPageSink{failOn: 1, err: boom}), &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(riverJobID, 3, 3, tenantID, documentID, uuid.NewString())); !errors.Is(err, boom) {
		t.Fatalf("Work returned %v, want the sink's error", err)
	}
	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, xid, "dead_lettered")

	row := stJobLayout(t, ctx, xid)
	if row.Fingerprint != nil {
		t.Errorf("job %s carries layout_fingerprint %s after its page ingest failed, want NULL: the transaction that would set it never committed", xid, wkStr(row.Fingerprint))
	}
	if row.Anchors != nil {
		t.Errorf("job %s carries layout_anchors %s after its page ingest failed, want NULL", xid, wkStr(row.Anchors))
	}

	rows := wkAuditRowsForJob(t, ctx, tenantID, xid)
	if len(rows) != 1 {
		t.Fatalf("wrote %d extraction.* audit row(s), want exactly 1: %v", len(rows), rows)
	}
	if got, _ := rows[0].payload["failure_kind"].(string); got != string(extraction.FailurePagesNotRendered) {
		t.Errorf("stored failure_kind = %q, want %q", got, extraction.FailurePagesNotRendered)
	}

	// Control: the same document through a working sink leaves both non-NULL. Without it the
	// NULL assertions above would pass on a worker that never writes the layout at all.
	const controlRiverJobID = int64(909904)
	if err := wkWorkerPages(t, wkOK(), &wkOpener{body: raw}, wkPDFiumPages(&wkPageSink{}), &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(controlRiverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("control Work: %v", err)
	}
	cid := wkExtractionJobID(t, ctx, tenantID, controlRiverJobID)
	control := stJobLayout(t, ctx, cid)
	if control.Fingerprint == nil || control.Anchors == nil {
		t.Fatalf("the control job %s carries layout_fingerprint %s and layout_anchors %s; want both non-NULL, or the NULL assertions above are vacuous", cid, wkStr(control.Fingerprint), wkStr(control.Anchors))
	}
}

// W-05 (AC-4): a River replay of a terminal job must not touch the layout columns again. The
// oracle is extraction_jobs_set_updated_at, a BEFORE UPDATE trigger that stamps updated_at on
// every UPDATE regardless of whether the SET values actually changed. A replay that recomputed
// and rewrote the SAME deterministic fingerprint/anchors would pass an equality check on those
// two columns alone -- updated_at is what a hoisted write into tx1 (upstream of the terminal
// short-circuit at line ~96) cannot hide from. Neither
// TestRLS_ExtractWorkerWritesOneResultSetPerJobOnReplay nor
// TestRLS_ExtractWorkerReplayEmitsNoSecondRow reads these columns at all.
func TestRLS_ExtractWorkerReplayWritesNoSecondLayout(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)
	raw := fxRead(t, fxCorpusTwoColumn)

	// succeeded arm.
	const succeededRiverJobID = int64(909905)
	if err := wkWorkerPages(t, wkOK(), &wkOpener{body: raw}, wkPDFiumPages(&wkPageSink{}), &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(succeededRiverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("first Work: %v", err)
	}
	sid := wkExtractionJobID(t, ctx, tenantID, succeededRiverJobID)
	stAssertJobState(t, ctx, sid, "succeeded")

	before := stJobLayout(t, ctx, sid)
	if before.Fingerprint == nil {
		t.Fatalf("job %s has layout_fingerprint NULL before the replay; an unchanged NULL would prove nothing about AC-4", sid)
	}

	if err := wkWorkerPages(t, wkOK(), wkNewOpener(), wkPDFiumPages(&wkPageSink{}), &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(succeededRiverJobID, 2, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("replayed Work: %v", err)
	}
	after := stJobLayout(t, ctx, sid)
	if !reflect.DeepEqual(after.Fingerprint, before.Fingerprint) {
		t.Errorf("replaying a succeeded job changed layout_fingerprint from %s to %s", wkStr(before.Fingerprint), wkStr(after.Fingerprint))
	}
	if !reflect.DeepEqual(after.Anchors, before.Anchors) {
		t.Errorf("replaying a succeeded job changed layout_anchors from %s to %s", wkStr(before.Anchors), wkStr(after.Anchors))
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("replaying a succeeded job moved updated_at from %s to %s; extraction_jobs_set_updated_at fires on every UPDATE, so an unchanged value proves no UPDATE ran",
			before.UpdatedAt.Format(time.RFC3339Nano), after.UpdatedAt.Format(time.RFC3339Nano))
	}

	// dead_lettered arm.
	boom := errors.New("extr-14-03: replay dead-letter fixture")
	const deadRiverJobID = int64(909906)
	if err := wkWorkerPages(t, wkFailing(boom), &wkOpener{body: raw}, wkPDFiumPages(&wkPageSink{}), &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(deadRiverJobID, 3, 3, tenantID, documentID, uuid.NewString())); !errors.Is(err, boom) {
		t.Fatalf("Work on the final attempt returned %v, want the extractor's error", err)
	}
	did := wkExtractionJobID(t, ctx, tenantID, deadRiverJobID)
	stAssertJobState(t, ctx, did, "dead_lettered")

	deadBefore := stJobLayout(t, ctx, did)
	if deadBefore.Fingerprint == nil {
		t.Fatalf("dead-lettered job %s has layout_fingerprint NULL before the replay; an unchanged NULL would prove nothing", did)
	}

	if err := wkWorkerPages(t, wkFailing(boom), wkNewOpener(), wkPDFiumPages(&wkPageSink{}), &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(deadRiverJobID, 4, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("replayed Work on a dead-lettered job: %v", err)
	}
	deadAfter := stJobLayout(t, ctx, did)
	if !reflect.DeepEqual(deadAfter.Fingerprint, deadBefore.Fingerprint) {
		t.Errorf("replaying a dead-lettered job changed layout_fingerprint from %s to %s", wkStr(deadBefore.Fingerprint), wkStr(deadAfter.Fingerprint))
	}
	if !reflect.DeepEqual(deadAfter.Anchors, deadBefore.Anchors) {
		t.Errorf("replaying a dead-lettered job changed layout_anchors from %s to %s", wkStr(deadBefore.Anchors), wkStr(deadAfter.Anchors))
	}
	if !deadAfter.UpdatedAt.Equal(deadBefore.UpdatedAt) {
		t.Errorf("replaying a dead-lettered job moved updated_at from %s to %s",
			deadBefore.UpdatedAt.Format(time.RFC3339Nano), deadAfter.UpdatedAt.Format(time.RFC3339Nano))
	}
}

// W-08 (AC-1's zero-page boundary): a document that yields zero pages still writes both layout
// columns -- an empty array, never NULL, and the fingerprint over nothing.
func TestRLS_ExtractWorkerRecordsEmptyLayoutForZeroPages(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	const riverJobID = int64(909907)
	pages := &extraction.PageStore{Reader: &wkStubReader{pages: 0}, Sink: (&wkPageSink{}).put}
	if err := wkWorkerPages(t, wkOK(), wkNewOpener(), pages, &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}
	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, xid, "succeeded")

	wantFingerprint := extraction.Fingerprint(nil)
	row := stJobLayout(t, ctx, xid)
	if row.Fingerprint == nil || *row.Fingerprint != wantFingerprint {
		t.Errorf("a zero-page job carries layout_fingerprint %s, want %q", wkStr(row.Fingerprint), wantFingerprint)
	}
	if row.Anchors == nil {
		t.Fatalf("a zero-page job has layout_anchors NULL, want the empty array \"[]\"")
	}
	if *row.Anchors != "[]" {
		t.Errorf("a zero-page job carries layout_anchors %s, want \"[]\"", *row.Anchors)
	}
}

// QA (EXTR-14-03): a document that has real page-1 content that happens to match no anchor
// label, plus later pages that would match, still writes the empty-list fingerprint and "[]" --
// distinct from W-08 (zero pages) because CollectTokens actually gathers more than one
// TokenPage here and AnchorObservations must exclude page 2's hits by content, not by absence
// of pages. This is also the shape that would trip a hand-rolled json.Marshal into "null" if
// AnchorObservations ever returned a nil slice for a non-matching page 1.
func TestRLS_ExtractWorkerRecordsEmptyLayoutWhenPageOneMatchesNoAnchor(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	reader := &wkStubReader{pages: 2, tokens: [][]extraction.Token{
		{{Text: "Thank you for your continued partnership this quarter."}},
		{{Text: "Invoice No: 4471"}},
	}}
	const riverJobID = int64(909908)
	pages := &extraction.PageStore{Reader: reader, Sink: (&wkPageSink{}).put}
	if err := wkWorkerPages(t, wkOK(), wkNewOpener(), pages, &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}
	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, xid, "succeeded")

	wantFingerprint := extraction.Fingerprint(nil)
	row := stJobLayout(t, ctx, xid)
	if row.Fingerprint == nil || *row.Fingerprint != wantFingerprint {
		t.Errorf("a non-matching-page-1 job carries layout_fingerprint %s, want %q -- page 2's Invoice No hit must not leak in", wkStr(row.Fingerprint), wantFingerprint)
	}
	if row.Anchors == nil {
		t.Fatalf("a non-matching-page-1 job has layout_anchors NULL, want the empty array \"[]\"")
	}
	if *row.Anchors != "[]" {
		t.Errorf("a non-matching-page-1 job carries layout_anchors %s, want \"[]\"", *row.Anchors)
	}
}

// QA (EXTR-14-03): AC-3, a boundary distinct from W-04. The sink fails on page 2 of 3, not page
// 1, so Ingest has already made one successful PUT before it aborts. Both columns must still be
// NULL and page 3 must never be attempted -- proving the abort is not merely "fails on the very
// first page" but genuinely stops mid-read, matching docs/page-image-storage.md's "leaves orphan
// objects and no rows at all".
func TestRLS_ExtractWorkerLeavesLayoutNullWhenIngestFailsPartway(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	raw := fxRead(t, fxNative3)
	boom := errors.New("extr-14-03: the page sink refused the PUT on page 2")
	sink := &wkPageSink{failOn: 2, err: boom}

	const riverJobID = int64(909909)
	if err := wkWorkerPages(t, wkOK(), &wkOpener{body: raw}, wkPDFiumPages(sink), &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(riverJobID, 3, 3, tenantID, documentID, uuid.NewString())); !errors.Is(err, boom) {
		t.Fatalf("Work returned %v, want the sink's error", err)
	}
	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, xid, "dead_lettered")

	if _, _, seen := sink.snapshot(); seen != 2 {
		t.Fatalf("the sink was called %d time(s), want 2: page 3 must never be attempted once page 2 aborted the read", seen)
	}
	if keys, _, _ := sink.snapshot(); len(keys) != 1 {
		t.Errorf("the sink recorded %d successful PUT(s), want 1 (page 1 only) -- page 1's object is the orphan the doc describes", len(keys))
	}

	row := stJobLayout(t, ctx, xid)
	if row.Fingerprint != nil {
		t.Errorf("job %s carries layout_fingerprint %s after a partway ingest failure, want NULL", xid, wkStr(row.Fingerprint))
	}
	if row.Anchors != nil {
		t.Errorf("job %s carries layout_anchors %s after a partway ingest failure, want NULL", xid, wkStr(row.Anchors))
	}
	if n := wkPageRowIDs(t, ctx, documentID); len(n) != 0 {
		t.Errorf("a partway ingest failure left %d page image row(s), want 0: the transaction that would write them never committed", len(n))
	}
}

// QA (EXTR-14-03): a job in "failed" (an attempt with retries left) is NOT terminal by
// ExtractWorker's own check (only "succeeded" and "dead_lettered" short-circuit), so a second
// Work call over it re-runs Ingest and rewrites the layout -- unlike AC-4's guard, which applies
// only once a job is terminal. This pins that as the real, intended behaviour: updated_at DOES
// move, so a future change that starts short-circuiting "failed" jobs too is visible here rather
// than only being caught by surprise elsewhere.
func TestRLS_ExtractWorkerNonTerminalReplayRewritesLayout(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)
	raw := fxRead(t, fxCorpusTwoColumn)
	boom := errors.New("extr-14-03: attempt 1 of 3 fails")

	const riverJobID = int64(909910)
	if err := wkWorkerPages(t, wkFailing(boom), &wkOpener{body: raw}, wkPDFiumPages(&wkPageSink{}), &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); !errors.Is(err, boom) {
		t.Fatalf("Work on attempt 1 of 3 returned %v, want the extractor's error", err)
	}
	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, xid, "failed")

	before := stJobLayout(t, ctx, xid)
	if before.Fingerprint == nil {
		t.Fatalf("job %s has layout_fingerprint NULL after attempt 1; an unchanged NULL would prove nothing about this test's claim", xid)
	}

	if err := wkWorkerPages(t, wkOK(), &wkOpener{body: raw}, wkPDFiumPages(&wkPageSink{}), &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(riverJobID, 2, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work on attempt 2 of 3: %v", err)
	}
	stAssertJobState(t, ctx, xid, "succeeded")
	after := stJobLayout(t, ctx, xid)

	if !reflect.DeepEqual(after.Fingerprint, before.Fingerprint) {
		t.Errorf("attempt 2 recomputed layout_fingerprint as %s, want the same %s: the document did not change", wkStr(after.Fingerprint), wkStr(before.Fingerprint))
	}
	if !reflect.DeepEqual(after.Anchors, before.Anchors) {
		t.Errorf("attempt 2 recomputed layout_anchors as %s, want the same %s", wkStr(after.Anchors), wkStr(before.Anchors))
	}
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("attempt 2 left updated_at at %s (was %s); a non-terminal job's second Work call must re-run the write, unlike AC-4's terminal replay guard",
			after.UpdatedAt.Format(time.RFC3339Nano), before.UpdatedAt.Format(time.RFC3339Nano))
	}
}

// D-20 end to end. The row ids are the oracle, not the count: the keys are content-derived and
// byte-identical across runs, so a replace that never ran leaves a row set a count cannot tell
// from a replaced one.
func TestRLS_ExtractWorkerRetryReplacesTheRowSet(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	raw := fxRead(t, fxNative3)
	sink := &wkPageSink{}
	ew := wkWorkerPages(t, wkOK(), &wkOpener{body: raw}, wkPDFiumPages(sink), &wkAuditRecorder{})

	// Two DIFFERENT river jobs over one document: extraction_jobs carries no UNIQUE over
	// (tenant_id, document_id), so duplicate jobs are expected, and a replay of the SAME job
	// short-circuits on its terminal state and never re-renders. That replay is the control
	// at the end.
	const (
		firstRiverJobID  = int64(909701)
		secondRiverJobID = int64(909702)
	)
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(firstRiverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("first Work: %v", err)
	}
	firstIDs := wkPageRowIDs(t, ctx, documentID)
	if len(firstIDs) != 3 {
		t.Fatalf("the first run left %d page row(s), want 3", len(firstIDs))
	}

	if err := ew.Work(ctx, extraction.NewExtractJobForTest(secondRiverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("second Work: %v", err)
	}
	rows := stPageRows(t, ctx, documentID)
	if len(rows) != 3 {
		t.Fatalf("two jobs over one document left %d page row(s), want 3: the inventory is document-keyed and a re-render replaces it whole", len(rows))
	}
	want := wkPageKeys(tenantID, raw, 3)
	for i, r := range rows {
		if r.StorageKey != want[i] {
			t.Errorf("after the second run page %d names %q, want %q", i+1, r.StorageKey, want[i])
		}
	}
	secondIDs := wkPageRowIDs(t, ctx, documentID)
	for i := range secondIDs {
		if secondIDs[i] == firstIDs[i] {
			t.Errorf("page %d still carries row id %s after a second job over the same document; the replace never ran, and the identical content-derived keys hide that from a row count", i+1, secondIDs[i])
		}
	}

	// Control: a replay of a SUCCEEDED job is terminal in tx1 and renders nothing at all.
	_, _, before := sink.snapshot()
	if before != 6 {
		t.Fatalf("the sink saw %d PUT(s) across two runs, want 6", before)
	}
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(secondRiverJobID, 2, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("replayed Work: %v", err)
	}
	if _, _, after := sink.snapshot(); after != before {
		t.Errorf("a replay of a succeeded job made %d further PUT(s); the terminal short-circuit is what stops it re-rendering", after-before)
	}
	if got := wkPageRowIDs(t, ctx, documentID); !slices.Equal(got, secondIDs) {
		t.Errorf("a replay of a succeeded job rewrote the page rows: ids %v, want the unchanged %v", got, secondIDs)
	}
}

// --- EXTR-08-03: emission at the two terminal points ----------------------------------

// wkFlaggedFields is three fields of which two carry a reason. field_count and flagged_count
// must differ and neither may be 0 or 1: a swapped pair reads as clean otherwise.
func wkFlaggedFields() []extraction.FieldResult {
	return []extraction.FieldResult{
		{Field: extraction.Field{Name: "invoice_number", Value: stPtr("INV-EXTR-08-03")}, Alternatives: []extraction.Field{}},
		{Field: extraction.Field{Name: "total_amount", Value: stPtr("100.00"), Reason: extraction.ReasonAmbiguous}, Alternatives: []extraction.Field{}},
		{Field: extraction.Field{Name: "supplier_tin", Value: stPtr("?"), Reason: extraction.ReasonUnreadable}, Alternatives: []extraction.Field{}},
	}
}

func wkFieldsExtractor(fields []extraction.FieldResult) *wkExtract {
	return &wkExtract{fn: func(context.Context, extraction.Document) ([]extraction.FieldResult, error) {
		return fields, nil
	}}
}

// wkSucceededKeys and wkFailedKeys are the six keys of each event, sorted for set equality.
var (
	wkSucceededKeys = []string{
		"document_id", "extraction_job_id", "extractor", "extractor_version",
		"field_count", "flagged_count",
	}
	wkFailedKeys = []string{
		"document_id", "extraction_job_id", "extractor", "extractor_version",
		"failure_kind", "state",
	}

	// The spellings an emitter reaching for extraction_jobs.last_error would use.
	wkForbiddenKeys = []string{"body", "detail", "error", "last_error", "message", "raw", "wire"}
)

// T3-1. The success emission, read back as a stored row: exactly one, with the six keys of the
// payload schema carrying the counts the extractor actually produced.
func TestRLS_ExtractWorkerEmitsSucceededOnce(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	rec := &wkAuditRecorder{}
	fields := wkFlaggedFields()
	const riverJobID = int64(909801)
	if err := wkWorkerAudit(t, wkFieldsExtractor(fields), wkNewOpener(), rec).Work(ctx,
		extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}
	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, xid, "succeeded")

	if n := len(rec.events()); n != 1 {
		t.Fatalf("the worker called the audit port %d time(s), want exactly 1", n)
	}
	rows := wkAuditRows(t, ctx, tenantID)
	if len(rows) != 1 {
		t.Fatalf("the tenant holds %d extraction.* audit row(s), want exactly 1: %v", len(rows), rows)
	}
	if rows[0].event != wkSucceededEvent {
		t.Fatalf("the stored event is %q, want %q", rows[0].event, wkSucceededEvent)
	}
	if got := rows[0].keys(); !slices.Equal(got, wkSucceededKeys) {
		t.Errorf("stored payload keys = %v, want exactly %v", got, wkSucceededKeys)
	}
	want := map[string]any{
		"document_id":       documentID,
		"extraction_job_id": xid,
		"extractor":         wkExtractorName,
		"extractor_version": wkExtractorVersion,
		"field_count":       float64(len(fields)),
		"flagged_count":     float64(2),
	}
	if !reflect.DeepEqual(rows[0].payload, want) {
		t.Errorf("stored payload = %v, want %v -- six right-named keys carrying the wrong values read as clean on a key-set check alone", rows[0].payload, want)
	}
}

// EXTR-05-06 AC-6: the extractor's own output carries no alternatives, so every row the
// worker writes for it lands at rank 0.
func TestRLS_ExtractWorkerWritesRankZeroForEveryExtractorField(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	fields := wkFlaggedFields()
	const riverJobID = int64(909901)
	if err := wkWorker(t, wkFieldsExtractor(fields), wkNewOpener()).Work(ctx,
		extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}
	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)

	rows, err := stRequire(t).super.Query(ctx,
		`SELECT candidate_rank FROM extraction_field_results WHERE extraction_job_id = $1`, xid)
	if err != nil {
		t.Fatalf("read candidate_rank column: %v", err)
	}
	defer rows.Close()

	var n int
	for rows.Next() {
		var rank int
		if err := rows.Scan(&rank); err != nil {
			t.Fatalf("scan candidate_rank: %v", err)
		}
		if rank != 0 {
			t.Errorf("row has candidate_rank %d, want 0 -- the extractor's own output carries no alternatives", rank)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read candidate_rank column: %v", err)
	}
	if n != len(fields) {
		t.Fatalf("found %d field result row(s), want %d", n, len(fields))
	}
}

// T3-2. AC-D3: a failed-but-will-retry attempt is non-terminal in substance and must emit
// nothing. The dead-lettered arm below is the population floor that stops the retry arm's zero
// from passing on a worker that emits nothing at all.
func TestRLS_ExtractWorkerEmitsFailedOnlyWhenDeadLettered(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	boom := errors.New("extr-08-03: the extractor always fails")

	retryRec := &wkAuditRecorder{}
	const retryRiverJobID = int64(909811)
	if err := wkWorkerAudit(t, wkFailing(boom), wkNewOpener(), retryRec).Work(ctx,
		extraction.NewExtractJobForTest(retryRiverJobID, 1, 3, tenantID, documentID, uuid.NewString())); !errors.Is(err, boom) {
		t.Fatalf("Work on attempt 1 of 3 returned %v, want the extractor's error", err)
	}
	retryJobID := wkExtractionJobID(t, ctx, tenantID, retryRiverJobID)
	stAssertJobState(t, ctx, retryJobID, "failed")
	if n := len(retryRec.events()); n != 0 {
		t.Errorf("attempt 1 of 3 called the audit port %d time(s), want 0: the job is still retrying", n)
	}
	if rows := wkAuditRowsForJob(t, ctx, tenantID, retryJobID); len(rows) != 0 {
		t.Errorf("attempt 1 of 3 wrote %d extraction.* audit row(s), want 0: %v", len(rows), rows)
	}

	deadRec := &wkAuditRecorder{}
	const deadRiverJobID = int64(909812)
	if err := wkWorkerAudit(t, wkFailing(boom), wkNewOpener(), deadRec).Work(ctx,
		extraction.NewExtractJobForTest(deadRiverJobID, 3, 3, tenantID, documentID, uuid.NewString())); !errors.Is(err, boom) {
		t.Fatalf("Work on attempt 3 of 3 returned %v, want the extractor's error", err)
	}
	deadJobID := wkExtractionJobID(t, ctx, tenantID, deadRiverJobID)
	stAssertJobState(t, ctx, deadJobID, "dead_lettered")

	if n := len(deadRec.events()); n != 1 {
		t.Fatalf("attempt 3 of 3 called the audit port %d time(s), want exactly 1 -- and without this the zero above proves nothing", n)
	}
	rows := wkAuditRowsForJob(t, ctx, tenantID, deadJobID)
	if len(rows) != 1 {
		t.Fatalf("attempt 3 of 3 wrote %d extraction.* audit row(s), want exactly 1 -- and without this the zero above proves nothing: %v", len(rows), rows)
	}
	if rows[0].event != wkFailedEvent {
		t.Fatalf("the stored event is %q, want %q", rows[0].event, wkFailedEvent)
	}
	if got := rows[0].keys(); !slices.Equal(got, wkFailedKeys) {
		t.Errorf("stored payload keys = %v, want exactly %v", got, wkFailedKeys)
	}
	if got := rows[0].payload["state"]; got != "dead_lettered" {
		t.Errorf("the stored state is %v, want %q: the event is written only when the job is over", got, "dead_lettered")
	}
	if got := rows[0].payload["failure_kind"]; got != string(extraction.FailureExtractFailed) {
		t.Errorf("the stored failure_kind is %v, want %q", got, extraction.FailureExtractFailed)
	}
}

// T3-3. AC-D4: a River replay of an already-terminal job re-emits nothing. before == 1 on each
// arm is load-bearing -- a 0 -> 0 count passes identically on a worker that emits nothing.
func TestRLS_ExtractWorkerReplayEmitsNoSecondRow(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	const succeededRiverJobID = int64(909821)
	key := uuid.NewString()
	if err := wkWorker(t, wkOK(), wkNewOpener()).Work(ctx,
		extraction.NewExtractJobForTest(succeededRiverJobID, 1, 3, tenantID, documentID, key)); err != nil {
		t.Fatalf("first Work: %v", err)
	}
	succeededJobID := wkExtractionJobID(t, ctx, tenantID, succeededRiverJobID)
	stAssertJobState(t, ctx, succeededJobID, "succeeded")
	if n := len(wkAuditRowsForJob(t, ctx, tenantID, succeededJobID)); n != 1 {
		t.Fatalf("the succeeded job holds %d audit row(s) before the replay, want 1; an unchanged count means nothing from 0", n)
	}

	replayRec := &wkAuditRecorder{}
	if err := wkWorkerAudit(t, wkOK(), wkNewOpener(), replayRec).Work(ctx,
		extraction.NewExtractJobForTest(succeededRiverJobID, 2, 3, tenantID, documentID, key)); err != nil {
		t.Fatalf("replayed Work on a succeeded job: %v", err)
	}
	if n := len(replayRec.events()); n != 0 {
		t.Errorf("the replay of a succeeded job called the audit port %d time(s), want 0", n)
	}
	if n := len(wkAuditRowsForJob(t, ctx, tenantID, succeededJobID)); n != 1 {
		t.Errorf("the succeeded job holds %d audit row(s) after the replay, want the unchanged 1", n)
	}

	boom := errors.New("extr-08-03 replay: the extractor always fails")
	const deadRiverJobID = int64(909822)
	if err := wkWorker(t, wkFailing(boom), wkNewOpener()).Work(ctx,
		extraction.NewExtractJobForTest(deadRiverJobID, 3, 3, tenantID, documentID, uuid.NewString())); !errors.Is(err, boom) {
		t.Fatalf("Work on the final attempt returned %v, want the extractor's error", err)
	}
	deadJobID := wkExtractionJobID(t, ctx, tenantID, deadRiverJobID)
	stAssertJobState(t, ctx, deadJobID, "dead_lettered")
	if n := len(wkAuditRowsForJob(t, ctx, tenantID, deadJobID)); n != 1 {
		t.Fatalf("the dead-lettered job holds %d audit row(s) before the replay, want 1; an unchanged count means nothing from 0", n)
	}

	deadReplayRec := &wkAuditRecorder{}
	if err := wkWorkerAudit(t, wkOK(), wkNewOpener(), deadReplayRec).Work(ctx,
		extraction.NewExtractJobForTest(deadRiverJobID, 4, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("replayed Work on a dead-lettered job: %v", err)
	}
	if n := len(deadReplayRec.events()); n != 0 {
		t.Errorf("the replay of a dead-lettered job called the audit port %d time(s), want 0", n)
	}
	if n := len(wkAuditRowsForJob(t, ctx, tenantID, deadJobID)); n != 1 {
		t.Errorf("the dead-lettered job holds %d audit row(s) after the replay, want the unchanged 1", n)
	}
}

// T3-5. A nil Audit is a wiring mistake, and a worker that skipped the row silently would leave
// no trace of it. The guard runs before any state change, so the job row is absent -- and the
// recording control in the same test is what stops that absence from passing against a worker
// that refuses every job.
func TestExtractWorker_NilAuditFailsBeforeAnyStateChange(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	op := wkNewOpener()
	ext := wkOK()
	ew := &extraction.ExtractWorker{
		Pool: stRequire(t).app, Extractor: ext, Open: op.open,
		Pages: &extraction.PageStore{Reader: &wkStubReader{pages: 1}, Sink: (&wkPageSink{}).put},
	}
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(909831, 1, 3, tenantID, documentID, uuid.NewString())); err == nil {
		t.Errorf("Work with a nil Audit returned nil, want an error: a skipped audit row must not read as success")
	}
	if n := wkExtractionJobCount(t, ctx, tenantID); n != 0 {
		t.Errorf("Work with a nil Audit left %d extraction_jobs row(s), want 0: the guard runs before any state change", n)
	}
	if n := op.count(); n != 0 {
		t.Errorf("Work with a nil Audit called OpenDocument %d time(s), want 0", n)
	}
	if n := ext.count(); n != 0 {
		t.Errorf("Work with a nil Audit ran the extractor %d time(s), want 0", n)
	}

	// Control: the identical call with a recording Audit does the whole job.
	rec := &wkAuditRecorder{}
	const controlRiverJobID = int64(909832)
	if err := wkWorkerAudit(t, wkOK(), wkNewOpener(), rec).Work(ctx,
		extraction.NewExtractJobForTest(controlRiverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("control Work: %v; the zero-assertions above prove nothing without it", err)
	}
	if n := wkExtractionJobCount(t, ctx, tenantID); n != 1 {
		t.Fatalf("the control left %d extraction_jobs row(s), want 1; the zero-assertions above prove nothing without it", n)
	}
	stAssertJobState(t, ctx, wkExtractionJobID(t, ctx, tenantID, controlRiverJobID), "succeeded")
	if n := len(rec.events()); n != 1 {
		t.Errorf("the control called the audit port %d time(s), want 1", n)
	}
}

// T3-6. AC-4: the failed event names the stage that failed. Five levers, one per error path in
// Work()'s if err == nil chain, each driven at attempt 3 of 3 because the kind only reaches a
// row on the dead_lettered branch.
func TestExtractWorker_FailureKindPerStage(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	boom := errors.New("extr-08-03: the stage under test refused")

	cases := []struct {
		name       string
		riverJobID int64
		worker     func(*wkAuditRecorder) *extraction.ExtractWorker
		// wantSQLState is set only for the arm whose lever is a constraint violation: that
		// lever is new, and an arm that stopped reaching writePageImagesTx would otherwise
		// classify some other stage and still look right.
		wantSQLState string
		want         extraction.FailureKind
	}{
		{
			name:       "w.Open",
			riverJobID: 909841,
			worker: func(rec *wkAuditRecorder) *extraction.ExtractWorker {
				return wkWorkerAudit(t, wkOK(), &wkOpener{err: boom}, rec)
			},
			want: extraction.FailureDocumentUnavailable,
		},
		{
			name:       "w.Pages.Ingest",
			riverJobID: 909842,
			worker: func(rec *wkAuditRecorder) *extraction.ExtractWorker {
				pages := &extraction.PageStore{
					Reader: &wkStubReader{pages: 1},
					Sink:   (&wkPageSink{failOn: 1, err: boom}).put,
				}
				return wkWorkerPages(t, wkOK(), wkNewOpener(), pages, rec)
			},
			want: extraction.FailurePagesNotRendered,
		},
		{
			name:       "writePageImagesTx",
			riverJobID: 909843,
			worker: func(rec *wkAuditRecorder) *extraction.ExtractWorker {
				pages := &extraction.PageStore{
					Reader: &wkStubReader{pages: 1, zeroWidth: true},
					Sink:   (&wkPageSink{}).put,
				}
				return wkWorkerPages(t, wkOK(), wkNewOpener(), pages, rec)
			},
			wantSQLState: "23514",
			want:         extraction.FailurePageRowsNotWritten,
		},
		{
			name:       "w.Extractor.Extract",
			riverJobID: 909844,
			worker: func(rec *wkAuditRecorder) *extraction.ExtractWorker {
				return wkWorkerAudit(t, wkFailing(boom), wkNewOpener(), rec)
			},
			want: extraction.FailureExtractFailed,
		},
		{
			name:       "w.Text.Read",
			riverJobID: 909845,
			worker: func(rec *wkAuditRecorder) *extraction.ExtractWorker {
				// Every upstream stage succeeds: wkOK, a fresh opener and the default page
				// store. An earlier failure would win the short-circuit and classify some
				// other stage.
				return wkWorkerText(t, wkOK(), wkNewOpener(), &wkErrReader{err: boom}, rec)
			},
			want: extraction.FailureTextNotRead,
		},
	}

	// Subtests, not a bare loop: one arm that cannot reach its stage must not stop the other
	// three from proving theirs.
	written := map[extraction.FailureKind]string{}
	onRow := map[extraction.FailureKind]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &wkAuditRecorder{}
			err := tc.worker(rec).Work(ctx,
				extraction.NewExtractJobForTest(tc.riverJobID, 3, 3, tenantID, documentID, uuid.NewString()))
			if err == nil {
				t.Fatalf("Work returned nil, want the stage's error")
			}
			if tc.wantSQLState != "" {
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.SQLState() != tc.wantSQLState {
					t.Fatalf("Work returned %v, want SQLSTATE %s from extraction_page_images_width_px_check -- the lever no longer reaches the page-row write",
						err, tc.wantSQLState)
				}
			}
			jobID := wkExtractionJobID(t, ctx, tenantID, tc.riverJobID)
			stAssertJobState(t, ctx, jobID, "dead_lettered")

			rows := wkAuditRowsForJob(t, ctx, tenantID, jobID)
			if len(rows) != 1 {
				t.Fatalf("wrote %d extraction.* audit row(s), want exactly 1: %v", len(rows), rows)
			}
			got, _ := rows[0].payload["failure_kind"].(string)
			if got != string(tc.want) {
				t.Errorf("stored failure_kind = %q, want %q", got, tc.want)
			}
			if prior, dup := written[extraction.FailureKind(got)]; dup {
				t.Errorf("stored failure_kind %q was already written by %s -- the four stages must be distinguishable", got, prior)
			}
			written[extraction.FailureKind(got)] = tc.name

			// EXTR-15-01 FK-7 (AC-9): the same kind on the ROW, not only in the audit payload.
			// Runs after the payload assertions so a pre-migration schema fails on the column
			// rather than hiding the shipped half of this arm.
			stRequireFailureKind(t, ctx)
			row := stJobFailureKind(t, ctx, jobID)
			if row == nil || *row != string(tc.want) {
				t.Fatalf("extraction_jobs.failure_kind = %v, want %q -- the payload carries the kind, the row does not", row, tc.want)
			}
			if prior, dup := onRow[extraction.FailureKind(*row)]; dup {
				t.Errorf("row failure_kind %q was already written by %s", *row, prior)
			}
			onRow[extraction.FailureKind(*row)] = tc.name
		})
	}

	// Set equality against the whole vocabulary: five distinct values is satisfied by five
	// values that are not the five FailureKind declares.
	want := map[extraction.FailureKind]bool{
		extraction.FailureDocumentUnavailable: true,
		extraction.FailurePagesNotRendered:    true,
		extraction.FailurePageRowsNotWritten:  true,
		extraction.FailureExtractFailed:       true,
		extraction.FailureTextNotRead:         true,
	}
	if len(written) != len(want) {
		t.Fatalf("the levers wrote %d distinct failure_kind value(s) (%v), want %d", len(written), written, len(want))
	}
	for k := range want {
		if _, ok := written[k]; !ok {
			t.Errorf("no lever wrote failure_kind %q; that stage is unreachable or misclassified", k)
		}
	}
	for k := range written {
		if !want[k] {
			t.Errorf("a lever wrote failure_kind %q, which FailureKind does not declare", k)
		}
	}

	// FK-7: the same set equality over what reached the ROW. Asserted separately from the
	// payload set so a writer that filled the payload from one source and the column from
	// another cannot satisfy both with one value.
	if len(onRow) != len(want) {
		t.Fatalf("the levers wrote %d distinct extraction_jobs.failure_kind value(s) (%v), want %d",
			len(onRow), onRow, len(want))
	}
	for k := range want {
		if _, ok := onRow[k]; !ok {
			t.Errorf("no lever wrote failure_kind %q onto the row", k)
		}
	}
}

// T3-7. AC-D5: no free-text error string enters a payload. The forbidden-key half is
// type-guaranteed -- ExtractionAudit carries no field holding err.Error() -- so the key-set
// equality and the needle below are what actually bite.
func TestExtractWorker_PayloadCarriesNoErrorText(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	const needle = "extr-08-03-needle: connection refused reading tenants/secret/key.pdf"
	boom := errors.New(needle)

	rec := &wkAuditRecorder{}
	const riverJobID = int64(909851)
	if err := wkWorkerAudit(t, wkFailing(boom), wkNewOpener(), rec).Work(ctx,
		extraction.NewExtractJobForTest(riverJobID, 3, 3, tenantID, documentID, uuid.NewString())); !errors.Is(err, boom) {
		t.Fatalf("Work returned %v, want the extractor's error", err)
	}
	jobID := wkExtractionJobID(t, ctx, tenantID, riverJobID)

	// (c) The row-count floor. Every assertion below reads rows[0].
	rows := wkAuditRowsForJob(t, ctx, tenantID, jobID)
	if len(rows) != 1 {
		t.Fatalf("the job wrote %d extraction.* audit row(s), want exactly 1: the checks below would read nothing", len(rows))
	}

	// The control needle: the text exists and this matcher finds it where it legitimately
	// lives. Without it, "the payload does not contain the needle" is a matcher that found
	// nothing anywhere.
	stored := stJobLastError(t, ctx, jobID)
	if stored == nil || !strings.Contains(*stored, needle) {
		t.Fatalf("extraction_jobs.last_error is %s, want it to carry the needle; the absence checks below would pass on a matcher that finds nothing", wkStr(stored))
	}

	// (a) The forbidden keys.
	for _, k := range wkForbiddenKeys {
		if v, ok := rows[0].payload[k]; ok {
			t.Errorf("the stored payload carries the forbidden key %q = %v; that text is adapter-shaped and the jobs table already holds it", k, v)
		}
	}
	// (b) Full key-set equality: a seventh key cannot slip in under an unforbidden name.
	if got := rows[0].keys(); !slices.Equal(got, wkFailedKeys) {
		t.Errorf("stored payload keys = %v, want exactly %v", got, wkFailedKeys)
	}
	// The needle reaches neither the stored JSON nor any field of the value the worker handed
	// the port.
	if strings.Contains(rows[0].raw, needle) {
		t.Errorf("the stored payload %s carries the error text", rows[0].raw)
	}
	events := rec.events()
	if len(events) != 1 {
		t.Fatalf("the worker called the audit port %d time(s), want exactly 1", len(events))
	}
	ev := reflect.ValueOf(events[0])
	for i := 0; i < ev.NumField(); i++ {
		f := ev.Field(i)
		if f.Kind() != reflect.String {
			continue
		}
		if strings.Contains(f.String(), needle) {
			t.Errorf("ExtractionAudit.%s = %q carries the error text", ev.Type().Field(i).Name, f.String())
		}
	}
}

// --- EXTR-08-03: the two residuals, characterized ------------------------------------

// Characterization of a ratified trade: the dead-letter emission shares the worker's
// transaction, so a port that refuses rolls the terminal advance back with it -- the job stays
// `extracting` and the error surfaces. internal/submission's terminal-failure branch is the same.
func TestExtractWorker_DeadLetterAuditFailureRollsBackTheAdvance(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	stageBoom := errors.New("extr-08-03: the extractor refused")
	auditBoom := errors.New("extr-08-03: the audit port refused")

	ew := wkWorkerAudit(t, wkFailing(stageBoom), wkNewOpener(), &wkAuditRecorder{})
	ew.Audit = func(context.Context, pgx.Tx, extraction.ExtractionAudit) error { return auditBoom }

	const riverJobID = int64(909861)
	job := extraction.NewExtractJobForTest(riverJobID, 3, 3, tenantID, documentID, uuid.NewString())
	if err := ew.Work(ctx, job); !errors.Is(err, auditBoom) {
		t.Fatalf("Work returned %v, want the audit port's error -- an unwritten terminal event must not read as a finished job", err)
	}

	jobID := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, jobID, "extracting")
	if e := stJobLastError(t, ctx, jobID); e != nil {
		t.Errorf("extraction_jobs.last_error is %s, want NULL -- the whole advance rolls back, not just the state", wkStr(e))
	}
	if rows := wkAuditRows(t, ctx, tenantID); len(rows) != 0 {
		t.Errorf("the rolled-back attempt left %d extraction.* audit row(s), want 0: %v", len(rows), rows)
	}

	// Control: the same River job, a working port. It reaches dead_lettered and writes the
	// row -- so the assertions above are the rollback, not a worker that never gets there.
	rec := &wkAuditRecorder{}
	ew.Audit = rec.record
	if err := ew.Work(ctx, job); !errors.Is(err, stageBoom) {
		t.Fatalf("the control Work returned %v, want the stage's error; the assertions above prove nothing without it", err)
	}
	stAssertJobState(t, ctx, jobID, "dead_lettered")
	if n := len(rec.events()); n != 1 {
		t.Errorf("the control called the audit port %d time(s), want 1", n)
	}
	rows := wkAuditRowsForJob(t, ctx, tenantID, jobID)
	if len(rows) != 1 {
		t.Fatalf("the control wrote %d extraction.* audit row(s), want exactly 1: %v", len(rows), rows)
	}
	if rows[0].event != wkFailedEvent {
		t.Errorf("the control stored event %q, want %q", rows[0].event, wkFailedEvent)
	}
}

// Characterization: exactly-once is scoped to the River job, not the document. queue.OncePerJob
// keys its marker job:<river id> and ensureJobTx keys on river_job_id, so two River jobs over one
// document write two succeeded rows -- one per extraction job, each naming its own
// extraction_job_id. Correct under D-6; the same scoping is a hole in internal/submission only
// because the entity in its payload is the invoice.
func TestRLS_ExtractWorkerTwoRiverJobsOneDocumentEmitTwice(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	riverJobIDs := []int64{909871, 909872}
	rec := &wkAuditRecorder{}
	for _, id := range riverJobIDs {
		if err := wkWorkerAudit(t, wkOK(), wkNewOpener(), rec).Work(ctx,
			extraction.NewExtractJobForTest(id, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
			t.Fatalf("Work for river job %d: %v", id, err)
		}
	}

	if n := wkExtractionJobCount(t, ctx, tenantID); n != 2 {
		t.Fatalf("two River jobs left %d extraction_jobs row(s), want 2 -- there is no unique index on (tenant_id, document_id)", n)
	}
	if n := len(rec.events()); n != 2 {
		t.Fatalf("the worker called the audit port %d time(s), want 2", n)
	}

	rows := wkAuditRows(t, ctx, tenantID)
	if len(rows) != 2 {
		t.Fatalf("one document holds %d extraction.* audit row(s), want 2: %v", len(rows), rows)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.event != wkSucceededEvent {
			t.Errorf("stored event %q, want %q", r.event, wkSucceededEvent)
		}
		if got := r.payload["document_id"]; got != documentID {
			t.Errorf("stored document_id = %v, want %s -- both rows name the one document", got, documentID)
		}
		id, _ := r.payload["extraction_job_id"].(string)
		if id == "" || seen[id] {
			t.Errorf("stored extraction_job_id = %q, want a distinct non-empty id per row (seen %v)", id, seen)
		}
		seen[id] = true
	}
	for _, id := range riverJobIDs {
		if x := wkExtractionJobID(t, ctx, tenantID, id); !seen[x] {
			t.Errorf("river job %d minted extraction job %s, which no audit row names (rows name %v)", id, x, seen)
		}
	}
}

// wkAuditRowID is the id of a tenant's one extraction.* audit row. Superuser, because the
// lookup runs outside any tenant transaction; the count is what keeps a caller from reading a
// row some other arm wrote.
func wkAuditRowID(t *testing.T, ctx context.Context, tenantID string) int64 {
	t.Helper()
	var n int
	var id int64
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT count(*), coalesce(min(id), 0) FROM audit_log
		  WHERE tenant_id = $1 AND event LIKE 'extraction.%'`, tenantID).Scan(&n, &id); err != nil {
		t.Fatalf("read the extraction.* audit row id for tenant %s: %v", tenantID, err)
	}
	if n != 1 {
		t.Fatalf("tenant %s holds %d extraction.* audit row(s), want exactly 1", tenantID, n)
	}
	return id
}

// wkAuditRowVisibleTo counts one audit row by bare id as the caller's tenant. app, never super:
// stRequire(t).super is rolbypassrls = t, so a read through it consults no policy at all and
// answers 1 under both tenants. And by bare id: measured, a tenant_id predicate here makes
// tenant B read zero on its own, staying green through a total collapse of row-level security.
func wkAuditRowVisibleTo(t *testing.T, ctx context.Context, tenantID string, rowID int64) int {
	t.Helper()
	var n int
	if err := db.WithinTenantTx(ctx, stRequire(t).app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE id = $1`, rowID).Scan(&n)
	}); err != nil {
		t.Fatalf("count audit row %d as tenant %s: %v", rowID, tenantID, err)
	}
	return n
}

// T5-1. tenant_isolation over an extraction.* row, read by bare id.
//
// This is a coverage claim, not a novel RLS proof: TestAudit_TenantIsolation
// (internal/audit/audit_test.go) already asserts both directions on a synthetic event name.
// What it adds is the subject -- the row here is the worker's own output, written on the
// worker's transaction as invoice_app with tenant_id defaulted from app.current_tenant, so the
// policy is exercised against this story's event names and payload shape. It is NOT proof that
// the production adapter is tenant-safe: deps_test.go's fence keeps internal/audit out of this
// package, so the row goes in through wkAuditRecorder's copy of newExtractionAuditor's mapping,
// and cmd/submission has no DB harness that drives the real adapter against a database at all.
func TestRLS_ExtractionAuditRowIsNotVisibleAcrossTenants(t *testing.T) {
	ctx := t.Context()
	tenantA, documentA := wkFixture(t, ctx)
	tenantB, _ := wkFixture(t, ctx)

	if err := wkWorker(t, wkOK(), wkNewOpener()).Work(ctx,
		extraction.NewExtractJobForTest(909891, 1, 3, tenantA, documentA, uuid.NewString())); err != nil {
		t.Fatalf("Work under tenant A: %v", err)
	}
	rowID := wkAuditRowID(t, ctx, tenantA)

	// The positive case first: an id nobody wrote reads zero under every tenant, which would
	// make the refusal below true of a database with no row-level security whatsoever.
	if n := wkAuditRowVisibleTo(t, ctx, tenantA, rowID); n != 1 {
		t.Fatalf("tenant A reads %d row(s) for its own audit row %d, want 1", n, rowID)
	}
	if n := wkAuditRowVisibleTo(t, ctx, tenantB, rowID); n != 0 {
		t.Errorf("tenant B reads %d row(s) for tenant A's audit row %d, want 0", n, rowID)
	}
}

// --- EXTR-12-01: alternatives reach the table at rank 1 and rank 2 ---------------

// wkFieldResultRow is one extraction_field_results row as the rank spec reads it.
type wkFieldResultRow struct {
	rank  int
	value *string
	page  *int
}

// wkFieldRowsByName reads every row one job wrote for one field, in candidate_rank order.
func wkFieldRowsByName(t *testing.T, ctx context.Context, jobID, field string) []wkFieldResultRow {
	t.Helper()
	rows, err := stRequire(t).super.Query(ctx,
		`SELECT candidate_rank, value, page FROM extraction_field_results
		  WHERE extraction_job_id = $1 AND field_name = $2
		  ORDER BY candidate_rank`, jobID, field)
	if err != nil {
		t.Fatalf("read %s rows: %v", field, err)
	}
	defer rows.Close()

	var out []wkFieldResultRow
	for rows.Next() {
		var r wkFieldResultRow
		if err := rows.Scan(&r.rank, &r.value, &r.page); err != nil {
			t.Fatalf("scan %s row: %v", field, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s rows: %v", field, err)
	}
	return out
}

// AC-7 (RED-FIRST). The real MockExtractor over bytes no fixture claims, so the ambiguous field
// and its two alternatives are the production ones, not a hand-built result.
// TestRLS_ExtractWorkerWritesRankZeroForEveryExtractorField covers the no-alternatives arm;
// this is the arm that reaches rank > 0.
func TestRLS_ExtractWorkerWritesAlternativeRowsAtRankOneAndTwo(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	// The opener's bytes miss both fixture hashes, so Extract takes the default arm.
	ext := extraction.NewMockExtractor()
	want, err := ext.Extract(ctx, extraction.Document{Bytes: []byte("extr-09 worker fixture"), ContentType: "application/pdf"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	var ambiguous extraction.FieldResult
	for _, r := range want {
		if len(r.Alternatives) == 2 {
			ambiguous = r
			break
		}
	}
	if ambiguous.Name == "" {
		t.Fatal("the default result carries no field with two alternatives; there are no rank>0 rows to read back")
	}

	const riverJobID = int64(912001)
	if err := wkWorker(t, ext, wkNewOpener()).Work(ctx,
		extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}
	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)

	got := wkFieldRowsByName(t, ctx, xid, ambiguous.Name)
	if len(got) != 3 {
		t.Fatalf("%q has %d row(s), want 3: one decided reading at rank 0 and two alternatives", ambiguous.Name, len(got))
	}

	// Slice order IS rank order: the alternative a reviewer sees second must not arrive first.
	wantRows := []struct {
		rank  int
		value *string
	}{
		{0, ambiguous.Value},
		{1, ambiguous.Alternatives[0].Value},
		{2, ambiguous.Alternatives[1].Value},
	}
	for i, w := range wantRows {
		if got[i].rank != w.rank {
			t.Errorf("%q row %d has candidate_rank %d, want %d", ambiguous.Name, i, got[i].rank, w.rank)
		}
		switch {
		case w.value == nil && got[i].value != nil:
			t.Errorf("%q rank %d has value %q, want NULL", ambiguous.Name, w.rank, *got[i].value)
		case w.value != nil && got[i].value == nil:
			t.Errorf("%q rank %d has a NULL value, want %q", ambiguous.Name, w.rank, *w.value)
		case w.value != nil && *got[i].value != *w.value:
			t.Errorf("%q rank %d has value %q, want %q -- the alternatives are written in slice order", ambiguous.Name, w.rank, *got[i].value, *w.value)
		}
	}

	// The three readings point at three different places, so a page-only check cannot tell
	// them apart; the distinct boxes are what EXTR-12-05 highlights.
	if got[0].page == nil || got[1].page == nil || got[2].page == nil {
		t.Errorf("%q wrote a NULL page for one of its three readings; each carries a region", ambiguous.Name)
	}

	// The alternatives never carry a reason of their own: FieldResult's contract puts it on the
	// decided reading alone.
	var reasons int
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT count(*) FROM extraction_field_results
		  WHERE extraction_job_id = $1 AND field_name = $2 AND candidate_rank > 0 AND reason_code IS NOT NULL`,
		xid, ambiguous.Name).Scan(&reasons); err != nil {
		t.Fatalf("count rank>0 reason codes: %v", err)
	}
	if reasons != 0 {
		t.Errorf("%d alternative row(s) carry a reason_code, want 0 -- only the decided reading does", reasons)
	}
}

// EXTR-17-01 AC-10. A nil Text keeps Work on the Extractor branch byte for byte
// (worker_pipeline_db_test.go's specs set Text, which is what makes the other branch
// observable). EXTR-17-03 wired cmd/submission/main.go's Text from selectTextReader, so nil is
// now the mock and unset configuration rather than the only one there is -- that is the path
// the whole deployed fleet runs, and this pins it.
//
// In this file, not worker_internal_test.go: Work's first act after the nil-Audit guard is
// db.WithinTenantTx, so it needs a pool.
func TestExtractWorker_NilTextKeepsTheExtractorPath(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	op := wkNewOpener()
	ext := wkOK()
	rec := &wkAuditRecorder{}
	ew := wkWorkerAudit(t, ext, op, rec)

	// The premise, asserted rather than assumed: a fixture that quietly set Text would make
	// every assertion below evidence for the wrong branch.
	if ew.Text != nil {
		t.Fatalf("ExtractWorker.Text is %T, want nil -- this spec is about the Extractor branch", ew.Text)
	}

	const riverJobID = int64(912101)
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}

	if n := ext.count(); n != 1 {
		t.Errorf("the extractor ran %d time(s), want 1 -- a nil Text must leave the Extractor branch untouched", n)
	}
	if n := op.count(); n != 1 {
		t.Errorf("OpenDocument ran %d time(s), want 1", n)
	}

	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, xid, "succeeded")
	stAssertFieldResultCount(t, ctx, xid, len(wkOneField()))

	// The terminal event is the success one: a text read that ran and failed would have
	// emitted a failure carrying a kind instead.
	evs := rec.events()
	if len(evs) != 1 {
		t.Fatalf("the worker emitted %d audit event(s), want 1", len(evs))
	}
	if !evs[0].Succeeded {
		t.Errorf("the worker emitted a failure carrying kind %q, want the success event", evs[0].FailureKind)
	}
	if evs[0].FailureKind != "" {
		t.Errorf("the success event carries FailureKind %q, want empty", evs[0].FailureKind)
	}
}

// EXTR-15-01 QA. River replays a non-terminal job, so the column is written more than once for
// one row. Each advance overwrites: the second stage's kind replaces the first's rather than
// accumulating beside it, and the attempt that finally succeeds clears it. Three attempts on
// ONE extraction_jobs row -- ensureJobTx keys off river_job_id, so the shared id is what makes
// this a replay rather than three separate jobs.
func TestExtractWorker_FailureKindOverwritesOnReplayAndClearsOnSuccess(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)
	boom := errors.New("extr-15-01: the stage under test refused")

	const riverJobID = int64(901504)

	// Attempt 1 of 3: the opener fails. Retries remain, so the state is "failed", which Work
	// does not treat as terminal -- attempt 2 re-enters the stage chain.
	if err := wkWorkerAudit(t, wkOK(), &wkOpener{err: boom}, &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); !errors.Is(err, boom) {
		t.Fatalf("attempt 1 returned %v, want the opener's error", err)
	}
	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, xid, "failed")
	if got := stJobFailureKind(t, ctx, xid); got == nil || *got != string(extraction.FailureDocumentUnavailable) {
		t.Fatalf("attempt 1 stored failure_kind %v, want %q -- attempt 2 would then prove nothing",
			got, extraction.FailureDocumentUnavailable)
	}

	// Attempt 2 of 3: a DIFFERENT stage fails on the same row.
	if err := wkWorkerText(t, wkOK(), wkNewOpener(), &wkErrReader{err: boom}, &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(riverJobID, 2, 3, tenantID, documentID, uuid.NewString())); !errors.Is(err, boom) {
		t.Fatalf("attempt 2 returned %v, want the text reader's error", err)
	}
	stAssertJobState(t, ctx, xid, "failed")
	got := stJobFailureKind(t, ctx, xid)
	if got == nil {
		t.Fatalf("attempt 2 left failure_kind NULL, want %q", extraction.FailureTextNotRead)
	}
	// Equality, not Contains: a COALESCE keeps attempt 1's kind and a concatenating writer
	// keeps both, and only an exact match refuses each.
	if *got != string(extraction.FailureTextNotRead) {
		t.Errorf("the replay stored failure_kind %q, want exactly %q -- the write accumulated rather than overwrote",
			*got, extraction.FailureTextNotRead)
	}

	// Attempt 3 of 3 settles cleanly. A reader that sees state='succeeded' beside a non-NULL
	// failure_kind is the bug the clear-on-clean-advance rule closes.
	if err := wkWorkerAudit(t, wkOK(), wkNewOpener(), &wkAuditRecorder{}).Work(ctx,
		extraction.NewExtractJobForTest(riverJobID, 3, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("attempt 3: %v", err)
	}
	stAssertJobState(t, ctx, xid, "succeeded")
	if got := stJobFailureKind(t, ctx, xid); got != nil {
		t.Errorf("the succeeding attempt left failure_kind %q, want SQL NULL", *got)
	}
}
