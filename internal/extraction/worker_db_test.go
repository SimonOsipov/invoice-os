// worker_db_test.go: the DB-backed ExtractWorker specs. Package extraction_test, so it shares
// store_db_test.go's TestMain, per-role pools and single skip site; export_test.go is what lets
// it reach the unexported args type from out here.
package extraction_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
)

var wkErrRollback = errors.New("worker suite: intentional rollback")

// --- stubs ---------------------------------------------------------------------------

// wkExtract is the Extractor the worker specs drive. fn decides the outcome; the count is
// what proves how many attempts actually reached it.
type wkExtract struct {
	mu    sync.Mutex
	calls int
	fn    func(context.Context, extraction.Document) ([]extraction.Field, error)
}

func (e *wkExtract) Name() string    { return wkExtractorName }
func (e *wkExtract) Version() string { return wkExtractorVersion }

func (e *wkExtract) Extract(ctx context.Context, doc extraction.Document) ([]extraction.Field, error) {
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
	return &wkExtract{fn: func(context.Context, extraction.Document) ([]extraction.Field, error) {
		return wkOneField(), nil
	}}
}

func wkFailing(err error) *wkExtract {
	return &wkExtract{fn: func(context.Context, extraction.Document) ([]extraction.Field, error) {
		return nil, err
	}}
}

// wkSlow honours ctx: without ExtractWorker's own Timeout the executor cancels the job at 60s
// and this returns that cancellation rather than sleeping on regardless.
func wkSlow(d time.Duration) *wkExtract {
	return &wkExtract{fn: func(ctx context.Context, _ extraction.Document) ([]extraction.Field, error) {
		select {
		case <-time.After(d):
			return wkOneField(), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
}

func wkOneField() []extraction.Field {
	return []extraction.Field{{Name: "invoice_number", Value: stPtr("INV-EXTR-09")}}
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
type wkStubReader struct{ pages int }

func (*wkStubReader) Name() string    { return "extr-09-stub-reader" }
func (*wkStubReader) Version() string { return "v1" }

func (r *wkStubReader) Read(_ context.Context, _ extraction.Document, onPage func(extraction.Page) error) (extraction.PageResult, error) {
	for i := 1; i <= r.pages; i++ {
		if err := onPage(extraction.Page{
			Number:      i,
			WidthPt:     612,
			HeightPt:    792,
			ImageWidth:  prLetterWidthPx,
			ImageHeight: prLetterHeightPx,
			ImagePNG:    []byte("\x89PNG\r\n\x1a\nextr-09 stub page"),
		}); err != nil {
			return extraction.PageResult{}, err
		}
	}
	return extraction.PageResult{Pages: r.pages}, nil
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

// wkWorker wires a stub PageStore: Work renders unconditionally, so a nil Pages panics on the
// first job rather than quietly skipping the page inventory.
func wkWorker(t *testing.T, ext extraction.Extractor, op *wkOpener) *extraction.ExtractWorker {
	t.Helper()
	return wkWorkerPages(t, ext, op, &extraction.PageStore{
		Reader: &wkStubReader{pages: 1},
		Sink:   (&wkPageSink{}).put,
	})
}

func wkWorkerPages(t *testing.T, ext extraction.Extractor, op *wkOpener, pages *extraction.PageStore) *extraction.ExtractWorker {
	t.Helper()
	return &extraction.ExtractWorker{
		Pool: stRequire(t).app, Extractor: ext, Open: op.open, Pages: pages,
	}
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
	})
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
	if err := wkWorkerPages(t, ext, op, wkPDFiumPages(sink)).Work(ctx,
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
	if err := wkWorkerPages(t, ext, &wkOpener{body: raw}, wkPDFiumPages(sink)).Work(ctx,
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
	if err := wkWorkerPages(t, wkOK(), &wkOpener{body: raw}, wkPDFiumPages(&wkPageSink{})).Work(ctx,
		extraction.NewExtractJobForTest(controlRiverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("control Work: %v", err)
	}
	if rows := stPageRows(t, ctx, documentID); len(rows) != 3 {
		t.Fatalf("the control left %d page row(s), want 3; the zero-row assertion above proves nothing without it", len(rows))
	}

	// The final attempt is terminal for a sink failure the same way it is for an extractor one.
	const deadRiverJobID = int64(909652)
	if err := wkWorkerPages(t, wkOK(), &wkOpener{body: raw}, wkPDFiumPages(&wkPageSink{failOn: 1, err: boom})).Work(ctx,
		extraction.NewExtractJobForTest(deadRiverJobID, 3, 3, tenantID, documentID, uuid.NewString())); !errors.Is(err, boom) {
		t.Fatalf("Work on the final attempt returned %v, want the sink's error", err)
	}
	stAssertJobState(t, ctx, wkExtractionJobID(t, ctx, tenantID, deadRiverJobID), "dead_lettered")
}

// D-20 end to end. The row ids are the oracle, not the count: the keys are content-derived and
// byte-identical across runs, so a replace that never ran leaves a row set a count cannot tell
// from a replaced one.
func TestRLS_ExtractWorkerRetryReplacesTheRowSet(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	raw := fxRead(t, fxNative3)
	sink := &wkPageSink{}
	ew := wkWorkerPages(t, wkOK(), &wkOpener{body: raw}, wkPDFiumPages(sink))

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
