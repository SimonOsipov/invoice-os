// worker_pipeline_db_test.go: EXTR-17-02. What Work writes once Text is wired -- Resolve,
// LineItems and Reconcile decide the result set instead of the Extractor. Package
// extraction_test, so it shares store_db_test.go's TestMain, per-role pools and single skip
// site.
//
// Every spec here drives Work directly rather than through River: the branch under test is
// inside Work, and a queue round trip only adds a clock.
package extraction_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// --- stubs ---------------------------------------------------------------------------

// wpReader is a PageReader over pages the spec writes by hand. TextChars is counted from the
// tokens with the same predicate both real readers use, so a fixture cannot claim text it does
// not carry -- that number is what selects the unreadable branch.
type wpReader struct {
	pages []extraction.Page
	err   error
}

func (*wpReader) Name() string    { return "extr-17-pipeline-reader" }
func (*wpReader) Version() string { return "v1" }

func (r *wpReader) Read(_ context.Context, _ extraction.Document, onPage func(extraction.Page) error) (extraction.PageResult, error) {
	if r.err != nil {
		return extraction.PageResult{}, r.err
	}
	res := extraction.PageResult{Pages: len(r.pages)}
	for _, p := range r.pages {
		chars := 0
		for _, tok := range p.Tokens {
			for _, c := range tok.Text {
				if !unicode.IsSpace(c) {
					chars++
				}
			}
		}
		res.TextChars += chars
		if chars > 0 {
			res.PagesWithText++
		}
		if err := onPage(p); err != nil {
			return extraction.PageResult{}, err
		}
	}
	return res, nil
}

// wpRules wraps a LoadAnchorRules and records what it was asked for and what it handed back.
// Both halves are load-bearing: the fingerprint proves the hoist (AC-6), and the count is what
// stops the cross-tenant and word-split specs from passing on a seam that returned nothing to
// everyone.
type wpRules struct {
	mu     sync.Mutex
	inner  extraction.LoadAnchorRules
	asked  []string
	served [][]extraction.AnchorRule
}

func (r *wpRules) load(ctx context.Context, tenantID, fingerprint string) ([]extraction.AnchorRule, error) {
	out, err := r.inner(ctx, tenantID, fingerprint)
	r.mu.Lock()
	r.asked = append(r.asked, fingerprint)
	r.served = append(r.served, out)
	r.mu.Unlock()
	return out, err
}

func (r *wpRules) calls() ([]string, [][]extraction.AnchorRule) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.asked), slices.Clone(r.served)
}

// wpOnlyCall returns the one fingerprint the seam was asked for and the rules it served, and
// fails when the seam was never reached -- the shape every assertion below depends on.
func (r *wpRules) wpOnlyCall(t *testing.T) (string, []extraction.AnchorRule) {
	t.Helper()
	asked, served := r.calls()
	if len(asked) != 1 {
		t.Fatalf("the Rules seam was called %d time(s), want exactly 1: %v", len(asked), asked)
	}
	return asked[0], served[0]
}

// --- harness -------------------------------------------------------------------------

// wpStoreRules is the production seam: (*Store).AnchorRulesFor over the app pool.
func wpStoreRules(t *testing.T) *wpRules {
	t.Helper()
	return &wpRules{inner: (&extraction.Store{Pool: stRequire(t).app}).AnchorRulesFor}
}

// wpWorker wires the text path: a real PDFium PageStore over the corpus fixture (the
// fingerprint source, CF-2), the caller's text reader, and the caller's rule seam.
func wpWorker(t *testing.T, ext extraction.Extractor, op *wkOpener, text extraction.PageReader, rules extraction.LoadAnchorRules, rec *wkAuditRecorder) *extraction.ExtractWorker {
	t.Helper()
	ew := wkWorkerPages(t, ext, op, wkPDFiumPages(&wkPageSink{}), rec)
	ew.Text = text
	ew.Rules = rules
	return ew
}

// wpCorpusOpener serves corpus_inline_labels.pdf's real bytes: PDFium renders them, so a
// placeholder would fail Ingest before the branch under test is reached.
func wpCorpusOpener(t *testing.T) *wkOpener {
	t.Helper()
	return &wkOpener{body: fxRead(t, dcCorpusFixture)}
}

// wpDoclingReader is a real DoclingReader replaying golden, the same replay shape
// docling_align_test.go uses.
func wpDoclingReader(t *testing.T, golden []byte) extraction.PageReader {
	t.Helper()
	srv := wpGoldenServer(t, golden)
	r, err := extraction.NewDoclingReader(srv)
	if err != nil {
		t.Fatalf("NewDoclingReader(%q): %v", srv, err)
	}
	return r
}

// wpGoldenServer serves golden verbatim from POST /v1/read and returns its base URL.
func wpGoldenServer(t *testing.T, golden []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(golden)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// wpRow is one extraction_field_results row as these specs read it.
type wpRow struct {
	name   string
	value  *string
	reason *string
	rank   int
}

func (r wpRow) String() string {
	return fmt.Sprintf("{%s value=%s reason=%s rank=%d}", r.name, wkStr(r.value), wkStr(r.reason), r.rank)
}

// wpResults reads every field-result row for one job, ordered by name then rank. NOT by
// insertion: the PK is a uuid and created_at is the transaction's clock, so write order is not
// observable through this table -- HeaderFields ORDER stays pinned by reconcile_corpus_test.go,
// and what this file pins is membership and value.
func wpResults(t *testing.T, ctx context.Context, jobID string) []wpRow {
	t.Helper()
	rows, err := stRequire(t).super.Query(ctx,
		`SELECT field_name, value, reason_code, candidate_rank
		   FROM extraction_field_results WHERE extraction_job_id = $1
		  ORDER BY field_name, candidate_rank`, jobID)
	if err != nil {
		t.Fatalf("read field results for job %s: %v", jobID, err)
	}
	defer rows.Close()

	out := []wpRow{}
	for rows.Next() {
		var r wpRow
		if err := rows.Scan(&r.name, &r.value, &r.reason, &r.rank); err != nil {
			t.Fatalf("scan field result for job %s: %v", jobID, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read field results for job %s: %v", jobID, err)
	}
	if len(out) == 0 {
		t.Fatalf("job %s wrote no field-result row at all; every assertion over the set would hold vacuously", jobID)
	}
	return out
}

// wpRankZero returns the rank-0 row named name.
func wpRankZero(t *testing.T, rows []wpRow, name string) wpRow {
	t.Helper()
	for _, r := range rows {
		if r.name == name && r.rank == 0 {
			return r
		}
	}
	t.Fatalf("no rank-0 row named %q among %v", name, rows)
	return wpRow{}
}

// wpAssertRankZero checks one decided row against its expected value and reason. A nil want
// means SQL NULL.
func wpAssertRankZero(t *testing.T, rows []wpRow, name string, wantValue, wantReason *string) {
	t.Helper()
	got := wpRankZero(t, rows, name)
	if wkStr(got.value) != wkStr(wantValue) {
		t.Errorf("%s value is %s, want %s", name, wkStr(got.value), wkStr(wantValue))
	}
	if wkStr(got.reason) != wkStr(wantReason) {
		t.Errorf("%s reason_code is %s, want %s", name, wkStr(got.reason), wkStr(wantReason))
	}
}

// wpRankZeroNames lists every rank-0 row name, sorted.
func wpRankZeroNames(rows []wpRow) []string {
	out := []string{}
	for _, r := range rows {
		if r.rank == 0 {
			out = append(out, r.name)
		}
	}
	slices.Sort(out)
	return out
}

// wpCorpusTier1 is what the shipped Tier-1 set alone reads off corpus_inline_labels: measured
// against the committed golden, not asserted from the story text. line_items is absent because
// the golden carries "tables": [].
var wpCorpusTier1 = []struct{ name, value string }{
	{"invoice_number", "INV-1001"},
	{"issue_date", "2026-03-04"},
	{"supplier_tin", "99999999-0101"},
	{"supplier_name", "Adeyemi Trading Limited"},
	{"buyer_tin", "99999999-0102"},
	{"buyer_name", "Honeywell Group"},
	{"currency", "NGN"},
	{"subtotal", "1000.00"},
	{"vat", "75.00"},
	{"total", "1075.00"},
}

// wpLearnedSupplierTIN reads the BUYER's TIN into supplier_tin. Deliberate: Tier-1 reads
// 99999999-0101 for that field off the same page, so "the learned rule won" and "the Tier-1
// rule won" are two different stored values rather than one value reached two ways.
const wpLearnedSupplierTIN = `{"label":"(?i)\\bBuyer TIN\\b","relation":{"kind":"same_token","max_distance":0},"shape":"tin"}`

// wpLearnedBuyerTIN is its mirror, used only under a DIFFERENT fingerprint: it reads the
// SUPPLIER's TIN into buyer_tin, so a lookup that ignored the fingerprint is visible as a
// wrong buyer_tin rather than as an absence.
const wpLearnedBuyerTIN = `{"label":"(?i)\\bSupplier TIN\\b","relation":{"kind":"same_token","max_distance":0},"shape":"tin"}`

const (
	wpSupplierTIN = "99999999-0101"
	wpBuyerTIN    = "99999999-0102"
)

// --- specs ---------------------------------------------------------------------------

// AC-1. With Text wired, Resolve and Reconcile decide the result set and it reaches
// extraction_field_results. The Extract COUNTER is the assertion, not "the Extractor recorded
// zero calls": Name() and Version() are called on it by ensureJobTx and by the audit write on
// every job, on both branches.
func TestRLS_ExtractWorkerWritesReconciledResults(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	ext := wkOK()
	rules := wpStoreRules(t)
	ew := wpWorker(t, ext, wpCorpusOpener(t),
		wpDoclingReader(t, dcReadNamedGolden(t, dcCorpusGoldenName)), rules.load, &wkAuditRecorder{})

	const riverJobID = int64(917001)
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}

	if n := ext.count(); n != 0 {
		t.Errorf("Extractor.Extract ran %d time(s), want 0 -- a wired Text must take the resolve path", n)
	}

	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, xid, "succeeded")
	rows := wpResults(t, ctx, xid)

	want := []string{"line_items"}
	for _, f := range wpCorpusTier1 {
		want = append(want, f.name)
		wpAssertRankZero(t, rows, f.name, stPtr(f.value), nil)
	}
	slices.Sort(want)
	if got := wpRankZeroNames(rows); !slices.Equal(got, want) {
		t.Errorf("the job wrote rank-0 rows %v, want %v", got, want)
	}
	for _, r := range rows {
		if r.rank != 0 {
			t.Errorf("row %v carries rank %d; no field on this fixture is ambiguous", r, r.rank)
		}
		if r.value != nil && *r.value == "INV-EXTR-09" {
			t.Errorf("row %v carries the mock Extractor's value; the resolve path did not decide this result set", r)
		}
	}
}

// AC-2. A text read that yields no non-whitespace character writes exactly one row: the
// document_text_layer / unreadable verdict both shipped extractors already emit, rather than
// ten `missing` rows a human would have to read one at a time.
func TestRLS_ExtractWorkerReportsAnEmptyReadAsUnreadable(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	blank := &wpReader{pages: []extraction.Page{{Number: 1, WidthPt: 612, HeightPt: 792}}}
	rules := wpStoreRules(t)
	ew := wpWorker(t, wkOK(), wpCorpusOpener(t), blank, rules.load, &wkAuditRecorder{})

	const riverJobID = int64(917002)
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}

	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, xid, "succeeded")

	rows := wpResults(t, ctx, xid)
	if len(rows) != 1 {
		t.Fatalf("a blank read wrote %d row(s), want exactly 1: %v", len(rows), rows)
	}
	wpAssertRankZero(t, rows, "document_text_layer", nil, stPtr("unreadable"))

	// The short-circuit is before the rule lookup: nothing about a document with no text layer
	// is worth a query.
	if asked, _ := rules.calls(); len(asked) != 0 {
		t.Errorf("the Rules seam was called %d time(s) on an unreadable document, want 0: %v", len(asked), asked)
	}
}

// AC-3. A reader that found NO TABLE writes line_items = missing while subtotal keeps its own
// clean reason: the sum check never ran (EXTR-05 D-19). Tables == nil is asserted as the cause,
// because a table found with no parseable line_total reaches the same reason by another route
// (D-21) and the pair would otherwise be indistinguishable.
func TestRLS_ExtractWorkerFlagsLineItemsMissingWhenNoTableWasFound(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	page := extraction.Page{
		Number: 1, WidthPt: 612, HeightPt: 792,
		Tokens: []extraction.Token{
			{Text: "Sub-total: 1,000.00", Region: extraction.Region{Page: 1, X0: 0.1, Y0: 0.60, X1: 0.4, Y1: 0.63}},
		},
	}
	if page.Tables != nil {
		t.Fatalf("the fixture declares %d table(s); this spec is about the no-table route to line_items = missing", len(page.Tables))
	}
	rules := wpStoreRules(t)
	ew := wpWorker(t, wkOK(), wpCorpusOpener(t), &wpReader{pages: []extraction.Page{page}}, rules.load, &wkAuditRecorder{})

	const riverJobID = int64(917003)
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}

	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	rows := wpResults(t, ctx, xid)
	wpAssertRankZero(t, rows, "line_items", nil, stPtr("missing"))
	wpAssertRankZero(t, rows, "subtotal", stPtr("1000.00"), nil)

	for _, r := range rows {
		if r.reason != nil && *r.reason == "inconsistent" {
			t.Errorf("row %v is inconsistent; with no line total the sum check must not have run", r)
		}
		if strings.HasPrefix(r.name, "line_items[") {
			t.Errorf("row %v names a line-item cell on a document whose reader found no table", r)
		}
	}
}

// AC-4. One rank-0 row per POPULATED cell, named line_items[N].<role>. The empty quantity cell
// is the control: it is present in the table and must produce no row at all.
func TestRLS_ExtractWorkerWritesOneRowPerPopulatedLineCell(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	cell := func(row, col int, text string) extraction.TableCell {
		return extraction.TableCell{Row: row, Col: col, RowSpan: 1, ColSpan: 1, Text: text}
	}
	page := extraction.Page{
		Number: 1, WidthPt: 612, HeightPt: 792,
		// One token, so the read is not classified unreadable and the AC-2 branch stays out of
		// the way. It names no anchor, so every header field reads `missing` and only the
		// line rows below carry values.
		Tokens: []extraction.Token{{Text: "INVOICE", Region: extraction.Region{Page: 1, X0: 0.1, Y0: 0.06, X1: 0.3, Y1: 0.09}}},
		Tables: []extraction.Table{{Rows: 4, Cols: 4, Cells: []extraction.TableCell{
			cell(0, 0, "Description"), cell(0, 1, "Qty"), cell(0, 2, "Unit Price"), cell(0, 3, "Amount"),
			cell(1, 0, "Widget A"), cell(1, 1, "2"), cell(1, 2, "100.00"), cell(1, 3, "200.00"),
			cell(2, 0, "Widget B"), cell(2, 1, ""), cell(2, 2, "50.00"), cell(2, 3, "150.00"),
			cell(3, 0, "Widget C"), cell(3, 1, "1"), cell(3, 2, "300.00"), cell(3, 3, "300.00"),
		}}},
	}
	ew := wpWorker(t, wkOK(), wpCorpusOpener(t), &wpReader{pages: []extraction.Page{page}}, wpStoreRules(t).load, &wkAuditRecorder{})

	const riverJobID = int64(917004)
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}

	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	rows := wpResults(t, ctx, xid)

	wantCells := []struct{ name, value string }{
		{"line_items[1].description", "Widget A"},
		{"line_items[1].quantity", "2"},
		{"line_items[1].unit_price", "100.00"},
		{"line_items[1].line_total", "200.00"},
		{"line_items[2].description", "Widget B"},
		{"line_items[2].unit_price", "50.00"},
		{"line_items[2].line_total", "150.00"},
		{"line_items[3].description", "Widget C"},
		{"line_items[3].quantity", "1"},
		{"line_items[3].unit_price", "300.00"},
		{"line_items[3].line_total", "300.00"},
	}
	if len(wantCells) == 0 {
		t.Fatalf("the expectation is empty; the loop below would assert nothing")
	}
	for _, w := range wantCells {
		wpAssertRankZero(t, rows, w.name, stPtr(w.value), nil)
	}

	got := []string{}
	for _, r := range rows {
		if !strings.HasPrefix(r.name, "line_items[") {
			continue
		}
		if r.rank != 0 {
			t.Errorf("line-item row %v carries rank %d; LineItemResults always emits an empty Alternatives", r, r.rank)
		}
		got = append(got, r.name)
	}
	want := []string{}
	for _, w := range wantCells {
		want = append(want, w.name)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("the job wrote line-item rows %v, want %v -- the empty quantity cell must produce none", got, want)
	}

	// The block row itself reconciles: three rows carried a parseable line total.
	wpAssertRankZero(t, rows, "line_items", nil, nil)
}

// AC-5. ORDERING. A Text.Read failure dead-letters as text_not_read AFTER the page images and
// the layout are already committed. The third clause is the one that makes this an ordering
// spec: without it a worker that read text BEFORE the page write would pass the first two.
func TestRLS_ExtractWorkerDeadLettersOnTextReadFailure(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	boom := errors.New("extr-17-02: the sidecar refused the read")
	rec := &wkAuditRecorder{}
	rules := wpStoreRules(t)
	ew := wpWorker(t, wkOK(), wpCorpusOpener(t), &wpReader{err: boom}, rules.load, rec)

	const riverJobID = int64(917005)
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 3, 3, tenantID, documentID, uuid.NewString())); !errors.Is(err, boom) {
		t.Fatalf("Work returned %v, want the reader's error", err)
	}

	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, xid, "dead_lettered")

	auditRows := wkAuditRowsForJob(t, ctx, tenantID, xid)
	if len(auditRows) != 1 {
		t.Fatalf("the job wrote %d extraction.* audit row(s), want exactly 1: %v", len(auditRows), auditRows)
	}
	if got, _ := auditRows[0].payload["failure_kind"].(string); got != string(extraction.FailureTextNotRead) {
		t.Errorf("stored failure_kind = %q, want %q", got, extraction.FailureTextNotRead)
	}

	// The ordering clause: both survive the failure because they committed before the read.
	if ids := wkPageRowIDs(t, ctx, documentID); len(ids) == 0 {
		t.Errorf("the document holds no extraction_page_images row; the text read must run AFTER that write commits")
	}
	var fingerprint *string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT layout_fingerprint FROM extraction_jobs WHERE id = $1`, xid).Scan(&fingerprint); err != nil {
		t.Fatalf("read layout_fingerprint for job %s: %v", xid, err)
	}
	if fingerprint == nil {
		t.Errorf("job %s stored no layout_fingerprint; the text read must run AFTER the layout write commits", xid)
	}

	if len(rec.events()) != 1 {
		t.Errorf("the worker emitted %d audit event(s), want 1", len(rec.events()))
	}
	stAssertFieldResultCount(t, ctx, xid, 0)
}

// AC-8. A w.Rules error dead-letters as the EXISTING extract_failed. Field extraction is the
// stage that failed; text_not_read names the read and would mislabel it, and a sixth
// FailureKind is scope this story did not buy. Swallowing it is refused outright -- a silently
// dropped rule set reads clean while the tenant's corrections are gone.
func TestRLS_ExtractWorkerDeadLettersWhenTheRuleLookupFails(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	boom := errors.New("extr-17-02: a stored rule the parser rejects")
	rec := &wkAuditRecorder{}
	failing := func(context.Context, string, string) ([]extraction.AnchorRule, error) { return nil, boom }
	ew := wpWorker(t, wkOK(), wpCorpusOpener(t),
		wpDoclingReader(t, dcReadNamedGolden(t, dcCorpusGoldenName)), failing, rec)

	const riverJobID = int64(917006)
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 3, 3, tenantID, documentID, uuid.NewString())); !errors.Is(err, boom) {
		t.Fatalf("Work returned %v, want the rule lookup's error", err)
	}

	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, xid, "dead_lettered")
	stAssertFieldResultCount(t, ctx, xid, 0)

	auditRows := wkAuditRowsForJob(t, ctx, tenantID, xid)
	if len(auditRows) != 1 {
		t.Fatalf("the job wrote %d extraction.* audit row(s), want exactly 1: %v", len(auditRows), auditRows)
	}
	if got, _ := auditRows[0].payload["failure_kind"].(string); got != string(extraction.FailureExtractFailed) {
		t.Errorf("stored failure_kind = %q, want %q", got, extraction.FailureExtractFailed)
	}
}

// AC-6. The fingerprint hoist, made observable: the value the rule lookup was asked for is the
// value the layout row stored. Two calls to Fingerprint over the same tokens would agree, so
// this does not prove the hoist alone -- it is the pair with AC-13 below, where the rule is
// SEEDED under the stored fingerprint and only reaches Resolve if the lookup used it.
func TestRLS_ExtractWorkerLooksUpRulesForTheFingerprintItStored(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	rules := wpStoreRules(t)
	ew := wpWorker(t, wkOK(), wpCorpusOpener(t),
		wpDoclingReader(t, dcReadNamedGolden(t, dcCorpusGoldenName)), rules.load, &wkAuditRecorder{})

	const riverJobID = int64(917007)
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}

	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	asked, _ := rules.wpOnlyCall(t)

	var stored *string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT layout_fingerprint FROM extraction_jobs WHERE id = $1`, xid).Scan(&stored); err != nil {
		t.Fatalf("read layout_fingerprint for job %s: %v", xid, err)
	}
	if stored == nil {
		t.Fatalf("job %s stored no layout_fingerprint; the comparison below has nothing to make", xid)
	}
	if asked != *stored {
		t.Errorf("the rule lookup asked for fingerprint %q, the layout row stored %q; they come from one Fingerprint call over the PDFium tokens",
			asked, *stored)
	}
	// Pinned rather than merely "equal to itself": the fingerprint of this fixture's PDFium
	// read is measured (fingerprint_test.go carries the same literal).
	const wantFingerprint = "v1:60be15050c9a80950f7d1ea69d21178fe23e6fb61021668a937cabfa139c086d"
	if asked != wantFingerprint {
		t.Errorf("the rule lookup asked for %q, want the corpus fixture's PDFium fingerprint %q", asked, wantFingerprint)
	}
}

// AC-13. A learned rule stored for the job's OWN fingerprint reaches Resolve at TierLearned and
// outranks Tier-1 for the same field, over the real Docling golden. The rule reads the buyer's
// TIN into supplier_tin, so the learned reading and the Tier-1 reading are different stored
// values -- "tenant got the Tier-1 answer" is not confusable with "tenant got the learned one".
func TestRLS_ExtractWorkerAppliesLearnedRulesForItsOwnFingerprint(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	pages := rvCorpusPages(t, dcCorpusFixture)
	fingerprint := extraction.Fingerprint(pages)
	ruleID := stSeedAnchorRule(t, ctx, tenantID, fingerprint, "supplier_tin", wpLearnedSupplierTIN, extraction.RuleSchemaVersion)

	rules := wpStoreRules(t)
	ew := wpWorker(t, wkOK(), wpCorpusOpener(t),
		wpDoclingReader(t, dcReadNamedGolden(t, dcCorpusGoldenName)), rules.load, &wkAuditRecorder{})

	const riverJobID = int64(917008)
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}

	asked, served := rules.wpOnlyCall(t)
	if asked != fingerprint {
		t.Errorf("the rule lookup asked for %q, want the job's own fingerprint %q", asked, fingerprint)
	}
	if len(served) != 1 || served[0].ID != ruleID {
		t.Fatalf("the seam served %+v, want exactly the seeded rule %s; the assertion below would be about Tier-1 either way", served, ruleID)
	}

	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	rows := wpResults(t, ctx, xid)
	wpAssertRankZero(t, rows, "supplier_tin", stPtr(wpBuyerTIN), nil)

	// The paired control, in the same document: buyer_tin has no learned rule and keeps the
	// Tier-1 reading. Without it, a worker that wrote the buyer's TIN everywhere would pass.
	wpAssertRankZero(t, rows, "buyer_tin", stPtr(wpBuyerTIN), nil)
	wpAssertRankZero(t, rows, "invoice_number", stPtr("INV-1001"), nil)
}

// AC-6c. The negative control for AC-13, and what makes the tokenisation observable at all: the
// SAME rule and the same job, over a golden whose lines have been split into words. The rule
// still reaches the worker -- it just matches nothing -- so supplier_tin falls back to the
// Tier-1 reading. Asserting only "not the learned value" would pass on a worker that never
// looked, hence the positive half.
func TestRLS_ExtractWorkerLosesTheLearnedRuleUnderWordSplitTokens(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)

	pages := rvCorpusPages(t, dcCorpusFixture)
	fingerprint := extraction.Fingerprint(pages)
	ruleID := stSeedAnchorRule(t, ctx, tenantID, fingerprint, "supplier_tin", wpLearnedSupplierTIN, extraction.RuleSchemaVersion)

	rules := wpStoreRules(t)
	split := dcSplitWords(t, dcReadNamedGolden(t, dcCorpusGoldenName))
	ew := wpWorker(t, wkOK(), wpCorpusOpener(t), wpDoclingReader(t, split), rules.load, &wkAuditRecorder{})

	const riverJobID = int64(917009)
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}

	// The rule was found. The split changes what it MATCHES, never whether it was loaded.
	_, served := rules.wpOnlyCall(t)
	if len(served) != 1 || served[0].ID != ruleID {
		t.Fatalf("the seam served %+v, want the seeded rule %s; a rule that never loaded makes this control vacuous", served, ruleID)
	}

	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	rows := wpResults(t, ctx, xid)
	got := wpRankZero(t, rows, "supplier_tin")
	if wkStr(got.value) == wkStr(stPtr(wpBuyerTIN)) {
		t.Errorf("supplier_tin is still the learned reading %s under word-split tokens; the rule's label spans a whole line and cannot match one word", wpBuyerTIN)
	}
	wpAssertRankZero(t, rows, "supplier_tin", stPtr(wpSupplierTIN), stPtr("ambiguous"))
}

// AC-14. A rule stored for tenant B is invisible to tenant A's job. Vacuous alone -- it passes
// on a seam returning nil to everyone -- so AC-13 above is its positive control and the two are
// one pair. It proves tenant SCOPING, not RLS: anchor_store.go carries `tenant_id = $1` on top
// of the policy, so the predicate alone isolates even with RLS off.
//
// Two tenants, different rule counts, and tenant A's own rule sits under a DIFFERENT
// fingerprint reading the SUPPLIER's TIN into buyer_tin -- so a lookup that ignored the tenant
// and one that ignored the fingerprint are two distinguishable wrong answers, not one absence.
func TestRLS_ExtractWorkerDoesNotSeeAnotherTenantsLearnedRule(t *testing.T) {
	ctx := t.Context()
	tenantA, documentA := wkFixture(t, ctx)
	tenantB, _ := wkFixture(t, ctx)

	pages := rvCorpusPages(t, dcCorpusFixture)
	fingerprint := extraction.Fingerprint(pages)

	stSeedAnchorRule(t, ctx, tenantB, fingerprint, "supplier_tin", wpLearnedSupplierTIN, extraction.RuleSchemaVersion)
	stSeedAnchorRule(t, ctx, tenantB, fingerprint, "buyer_tin", wpLearnedSupplierTIN, extraction.RuleSchemaVersion)
	stSeedAnchorRule(t, ctx, tenantA, fingerprint+"-other", "buyer_tin", wpLearnedBuyerTIN, extraction.RuleSchemaVersion)

	rules := wpStoreRules(t)
	ew := wpWorker(t, wkOK(), wpCorpusOpener(t),
		wpDoclingReader(t, dcReadNamedGolden(t, dcCorpusGoldenName)), rules.load, &wkAuditRecorder{})

	const riverJobID = int64(917010)
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantA, documentA, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}

	asked, served := rules.wpOnlyCall(t)
	if asked != fingerprint {
		t.Errorf("the rule lookup asked for %q, want tenant A's own fingerprint %q", asked, fingerprint)
	}
	if len(served) != 0 {
		t.Errorf("the seam served tenant A %d rule(s) (%+v); the three seeded rows all belong to tenant B or to another fingerprint", len(served), served)
	}

	xid := wkExtractionJobID(t, ctx, tenantA, riverJobID)
	rows := wpResults(t, ctx, xid)
	wpAssertRankZero(t, rows, "supplier_tin", stPtr(wpSupplierTIN), nil)
	wpAssertRankZero(t, rows, "buyer_tin", stPtr(wpBuyerTIN), nil)
}
