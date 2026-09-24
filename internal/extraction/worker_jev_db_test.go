package extraction_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

// wkJev is a recording JevAsker: no network; it keeps every request and the tenant on the
// caller's context, and answers resp/err.
type wkJev struct {
	mu      sync.Mutex
	enabled bool
	resp    jev.Response
	err     error
	calls   int
	reqs    []jev.Request
	tenants []string
}

func (s *wkJev) Enabled() bool { return s.enabled }

func (s *wkJev) Ask(ctx context.Context, req jev.Request) (jev.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.reqs = append(s.reqs, req)
	if id, ok := auth.IdentityFromContext(ctx); ok {
		s.tenants = append(s.tenants, id.TenantID)
	}
	return s.resp, s.err
}

func (s *wkJev) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// wkRunJev is wkRunAI with the Jev seam set too. Pass a literal nil for off: a nil *wkJev
// would be a non-nil JevAsker.
func wkRunJev(t *testing.T, ctx context.Context, riverJobID int64, pages []extraction.Page, reader extraction.AIReader, asker extraction.JevAsker) wkAIRun {
	t.Helper()
	tenantID, documentID := wkFixture(t, ctx)
	rec := &wkAuditRecorder{}
	ew := wpWorker(t, wkOK(), wpCorpusOpener(t), &wpReader{pages: pages}, wpStoreRules(t).load, rec)
	ew.AI = reader
	ew.Jev = asker
	if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work: %v", err)
	}
	xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, xid, "succeeded")
	return wkAIRun{tenantID: tenantID, jobID: xid, rows: wpResults(t, ctx, xid), boxes: wkFieldBoxes(t, ctx, xid), audit: rec.events()}
}

// wjPage: the engine refuses the all-digit number, so only the AI can decide it; the total
// the engine decides on its own.
func wjPage() extraction.Page {
	return extraction.Page{Number: 1, WidthPt: 612, HeightPt: 792, Tokens: []extraction.Token{
		{Text: "Invoice Number: 20417", Region: extraction.Region{Page: 1, X0: 0.1, Y0: 0.10, X1: 0.5, Y1: 0.12}},
		{Text: "Total: 1,935.00", Region: extraction.Region{Page: 1, X0: 0.1, Y0: 0.18, X1: 0.5, Y1: 0.20}},
	}}
}

func wjAI() *wkAI { return &wkAI{enabled: true, answer: map[string]any{"invoice_number": "20417"}} }

func wjNoul(scores map[string]float64) jev.Response {
	answers := map[string]jev.Answer{}
	for id, n := range scores {
		answers[id] = jev.Answer{Type: jev.TypeNoul, Noul: n}
	}
	return jev.Response{Answers: answers}
}

// wjReceipt adds a confident receipt verdict to wjNoul's answers.
func wjReceipt(scores map[string]float64) jev.Response {
	return wjWithType(wjNoul(scores), "receipt", 1)
}

func wjWithType(resp jev.Response, choice string, confidence float64) jev.Response {
	resp.Answers["document_type"] = jev.Answer{Type: jev.TypeChoice, Choice: choice, Confidence: confidence}
	return resp
}

// wjDocumentType reads extraction_jobs.document_type as the superuser; nil is SQL NULL.
func wjDocumentType(t *testing.T, ctx context.Context, jobID string) *string {
	t.Helper()
	var got *string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT document_type FROM extraction_jobs WHERE id = $1`, jobID).Scan(&got); err != nil {
		t.Fatalf("read document_type for job %s: %v", jobID, err)
	}
	return got
}

func wjAssertNoVerdict(t *testing.T, ctx context.Context, label, jobID string) {
	t.Helper()
	if got := wjDocumentType(t, ctx, jobID); got != nil {
		t.Errorf("%s: document_type = %q, want NULL", label, *got)
	}
}

func wjAssertVerdict(t *testing.T, ctx context.Context, label, jobID, want string) {
	t.Helper()
	if got := wjDocumentType(t, ctx, jobID); got == nil || *got != want {
		t.Errorf("%s: document_type = %s, want %q", label, wkStr(got), want)
	}
}

// wjDoubtAll doubts both fields wjPage decides with wjAI.
func wjDoubtAll() jev.Response { return wjNoul(map[string]float64{"invoice_number": 0, "total": 0}) }

func wjRender(r wkAIRun) []string { return append(wkStrRows(r.rows), wkStrBoxes(r.boxes)...) }

func wjAuditCounts(t *testing.T, r wkAIRun) (fieldCount, flaggedCount int) {
	t.Helper()
	if len(r.audit) != 1 {
		t.Fatalf("audit events = %d, want exactly 1", len(r.audit))
	}
	return r.audit[0].FieldCount, r.audit[0].FlaggedCount
}

func wjFlaggedRankZero(rows []wpRow) int {
	n := 0
	for _, r := range rows {
		if r.rank == 0 && r.reason != nil {
			n++
		}
	}
	return n
}

// wjAssertWritesToday runs wjPage with asker and with Jev nil, and requires equal rows, boxes and
// audit counts, plus wantCalls on the asker so the equality cannot hold over a never-asked seam.
func wjAssertWritesToday(t *testing.T, riverJobID int64, asker *wkJev, wantCalls int) (off, got wkAIRun) {
	t.Helper()
	ctx := t.Context()
	pages := []extraction.Page{wjPage()}
	off = wkRunJev(t, ctx, riverJobID, pages, wjAI(), nil)
	got = wkRunJev(t, ctx, riverJobID+1, pages, wjAI(), asker)

	if n := asker.count(); n != wantCalls {
		t.Errorf("the Jev seam saw %d call(s), want %d", n, wantCalls)
	}
	if a, b := wjRender(off), wjRender(got); !slices.Equal(a, b) {
		t.Errorf("rows and boxes differ from the Jev-nil run:\n nil: %v\n got: %v", a, b)
	}
	of, ofl := wjAuditCounts(t, off)
	gf, gfl := wjAuditCounts(t, got)
	if of != gf || ofl != gfl {
		t.Errorf("audit {FieldCount %d, FlaggedCount %d}, want the Jev-nil run's {%d, %d}", gf, gfl, of, ofl)
	}
	return off, got
}

func TestRLS_ExtractWorkerAsksJevOnceAfterTheDecision(t *testing.T) {
	ctx := t.Context()
	page := wjPage()
	stub := &wkJev{enabled: true, resp: wjNoul(map[string]float64{"invoice_number": 1, "total": 1})}
	r := wkRunJev(t, ctx, 954001, []extraction.Page{page}, wjAI(), stub)

	if n := stub.count(); n != 1 {
		t.Fatalf("the Jev seam saw %d call(s), want exactly 1 per document on the text branch", n)
	}
	req := stub.reqs[0]
	ids := slices.Sorted(maps.Keys(req.Questions))
	if want := []string{"document_type", "invoice_number", "total"}; !slices.Equal(ids, want) {
		t.Errorf("question ids = %v, want %v", ids, want)
	}
	if req.Purpose != jev.PurposeValueCheck {
		t.Errorf("Purpose = %q, want %q", req.Purpose, jev.PurposeValueCheck)
	}
	var tokens []extraction.TokenPage
	if err := extraction.CollectTokens(&tokens)(page); err != nil {
		t.Fatalf("CollectTokens: %v", err)
	}
	want := extraction.DoclingPromptText(tokens)
	if !strings.Contains(want, "20417") {
		t.Fatalf("DoclingPromptText of the page = %q, want it to carry the page text", want)
	}
	if req.State != want {
		t.Errorf("State = %q, want DoclingPromptText of the text reader's tokens %q", req.State, want)
	}
	if want := []string{r.tenantID}; !slices.Equal(stub.tenants, want) {
		t.Errorf("the Jev call carried tenant(s) %v, want %v", stub.tenants, want)
	}

	// Control: with the AI off, invoice_number stays missing and only document_type and total are asked.
	ctl := &wkJev{enabled: true, resp: wjNoul(map[string]float64{"total": 1})}
	off := wkRunJev(t, ctx, 954002, []extraction.Page{page}, nil, ctl)
	wpAssertRankZero(t, off.rows, "invoice_number", nil, stPtr("missing"))
	if n := ctl.count(); n != 1 {
		t.Fatalf("the control's Jev seam saw %d call(s), want 1", n)
	}
	if ids := slices.Sorted(maps.Keys(ctl.reqs[0].Questions)); !slices.Equal(ids, []string{"document_type", "total"}) {
		t.Errorf("control question ids = %v, want [document_type total]", ids)
	}
}

func TestRLS_ExtractWorkerNeverAsksJevWithoutDoclingText(t *testing.T) {
	ctx := t.Context()

	t.Run("mock arm, Text is nil", func(t *testing.T) {
		tenantID, documentID := wkFixture(t, ctx)
		stub := &wkJev{enabled: true, resp: wjDoubtAll()}
		ew := wkWorker(t, wkOK(), wkNewOpener())
		ew.Jev = stub
		if err := ew.Work(ctx, extraction.NewExtractJobForTest(954010, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
			t.Fatalf("Work: %v", err)
		}
		xid := wkExtractionJobID(t, ctx, tenantID, 954010)
		if len(wpResults(t, ctx, xid)) == 0 {
			t.Fatal("the mock arm wrote no rows")
		}
		if n := stub.count(); n != 0 {
			t.Errorf("the Jev seam saw %d call(s) on the mock arm, want 0", n)
		}
		wjAssertNoVerdict(t, ctx, "the mock arm", xid)
	})

	t.Run("zero-character text read", func(t *testing.T) {
		stub := &wkJev{enabled: true, resp: wjDoubtAll()}
		blank := []extraction.Page{{Number: 1, WidthPt: 612, HeightPt: 792}}
		r := wkRunJev(t, ctx, 954011, blank, nil, stub)
		wpAssertRankZero(t, r.rows, "document_text_layer", nil, stPtr("unreadable"))
		if n := stub.count(); n != 0 {
			t.Errorf("the Jev seam saw %d call(s) on a zero-character read, want 0", n)
		}
		wjAssertNoVerdict(t, ctx, "a zero-character read", r.jobID)
	})

	t.Run("zero-character read filled by the image read", func(t *testing.T) {
		tenantID, documentID := wkFixture(t, ctx)
		aiStub := &wkAI{enabled: true, answer: map[string]any{"invoice_number": "INV-5520", "total": "1935.00"}}
		stub := &wkJev{enabled: true, resp: wjDoubtAll()}
		ew := wkImageWorker(t, fxRead(t, fxScanned), extraction.NewPDFiumReader(), &wkPageBucket{}, wpStoreRules(t).load, aiStub, &wkAuditRecorder{})
		ew.Jev = stub
		if err := ew.Work(ctx, extraction.NewExtractJobForTest(954012, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
			t.Fatalf("Work: %v", err)
		}
		if n := aiStub.count(); n != 1 {
			t.Fatalf("the image read ran %d time(s), want 1 -- the arm under test was not reached", n)
		}
		xid := wkExtractionJobID(t, ctx, tenantID, 954012)
		rows := wpResults(t, ctx, xid)
		wpAssertRankZero(t, rows, "invoice_number", stPtr("INV-5520"), nil)
		if n := stub.count(); n != 0 {
			t.Errorf("the Jev seam saw %d call(s) on an image read, want 0", n)
		}
		wjAssertNoVerdict(t, ctx, "an image read", xid)
	})

	t.Run("control: the text branch asks", func(t *testing.T) {
		stub := &wkJev{enabled: true, resp: wjDoubtAll()}
		wkRunJev(t, ctx, 954013, []extraction.Page{wjPage()}, wjAI(), stub)
		if n := stub.count(); n != 1 {
			t.Errorf("the Jev seam saw %d call(s) on the text branch, want 1 -- the zero counts above prove nothing", n)
		}
	})
}

func TestRLS_ExtractWorkerNeverAsksJevWhenTheAIFailedOrTheRulesFailed(t *testing.T) {
	ctx := t.Context()

	t.Run("the AI call failed", func(t *testing.T) {
		stub := &wkJev{enabled: true, resp: wjDoubtAll()}
		aiStub := &wkAI{enabled: true, answer: map[string]any{"invoice_number": "20417"}, err: errors.New("ai: refused: HTTP 402")}
		r := wkRunJev(t, ctx, 954020, []extraction.Page{wjPage()}, aiStub, stub)
		wkAssertOnlyMarker(t, ctx, r.jobID, r.rows)
		if n := stub.count(); n != 0 {
			t.Errorf("the Jev seam saw %d call(s) after the AI failed, want 0", n)
		}
		wjAssertNoVerdict(t, ctx, "the AI-failed job", r.jobID)
	})

	t.Run("the Rules load failed", func(t *testing.T) {
		tenantID, documentID := wkFixture(t, ctx)
		rulesErr := errors.New("rules store down")
		rules := &wpRules{inner: func(context.Context, string, string) ([]extraction.AnchorRule, error) { return nil, rulesErr }}
		stub := &wkJev{enabled: true, resp: wjDoubtAll()}
		ew := wpWorker(t, wkOK(), wpCorpusOpener(t), &wpReader{pages: []extraction.Page{wjPage()}}, rules.load, &wkAuditRecorder{})
		ew.AI = wjAI()
		ew.Jev = stub
		err := ew.Work(ctx, extraction.NewExtractJobForTest(954021, 1, 3, tenantID, documentID, uuid.NewString()))
		if !errors.Is(err, rulesErr) {
			t.Fatalf("Work returned %v, want the rules error", err)
		}
		if asked, _ := rules.calls(); len(asked) != 1 {
			t.Fatalf("the Rules seam was called %d time(s), want 1 -- the path under test was not reached", len(asked))
		}
		if n := stub.count(); n != 0 {
			t.Errorf("the Jev seam saw %d call(s) after the Rules load failed, want 0", n)
		}
	})

	t.Run("control: an answered AI call asks", func(t *testing.T) {
		stub := &wkJev{enabled: true, resp: wjDoubtAll()}
		wkRunJev(t, ctx, 954022, []extraction.Page{wjPage()}, wjAI(), stub)
		if n := stub.count(); n != 1 {
			t.Errorf("the Jev seam saw %d call(s) on an answered job, want 1 -- the zero counts above prove nothing", n)
		}
	})
}

func TestRLS_ExtractWorkerWritesADoubtAsUnreadableWithItsValue(t *testing.T) {
	ctx := t.Context()
	pages := []extraction.Page{wjPage()}
	off := wkRunJev(t, ctx, 954030, pages, wjAI(), nil)
	wpAssertRankZero(t, off.rows, "total", stPtr("1935.00"), nil)

	stub := &wkJev{enabled: true, resp: wjNoul(map[string]float64{"total": 0.0, "invoice_number": 1.0})}
	on := wkRunJev(t, ctx, 954031, pages, wjAI(), stub)

	wpAssertRankZero(t, on.rows, "total", stPtr("1935.00"), stPtr("unreadable"))
	wpAssertRankZero(t, on.rows, "invoice_number", stPtr("20417"), nil)

	if len(on.rows) != len(off.rows) {
		t.Fatalf("the doubt run wrote %d row(s), the Jev-nil run %d:\n nil: %v\n on:  %v", len(on.rows), len(off.rows), off.rows, on.rows)
	}
	for i, want := range off.rows {
		if want.name == "total" && want.rank == 0 {
			want.reason = stPtr("unreadable")
		}
		if got := on.rows[i]; got.String() != want.String() {
			t.Errorf("row %d = %v, want %v", i, got, want)
		}
	}
	if len(off.boxes) == 0 {
		t.Fatal("the Jev-nil run wrote no box")
	}
	if a, b := wkStrBoxes(off.boxes), wkStrBoxes(on.boxes); !slices.Equal(a, b) {
		t.Errorf("boxes differ from the Jev-nil run:\n nil: %v\n on:  %v", a, b)
	}
}

func TestRLS_ExtractWorkerJevOffWritesToday(t *testing.T) {
	// Control: the same doubting answer from an enabled asker does change the rows.
	ctx := t.Context()
	pages := []extraction.Page{wjPage()}
	off := wkRunJev(t, ctx, 954040, pages, wjAI(), nil)
	on := wkRunJev(t, ctx, 954041, pages, wjAI(), &wkJev{enabled: true, resp: wjDoubtAll()})
	if slices.Equal(wjRender(off), wjRender(on)) {
		t.Errorf("an enabled asker doubting every field left the rows unchanged; the comparison below holds over nothing")
	}

	wjAssertWritesToday(t, 954042, &wkJev{enabled: false, resp: wjDoubtAll()}, 0)
}

func TestRLS_ExtractWorkerJevRefusedWritesToday(t *testing.T) {
	wjAssertWritesToday(t, 954050, &wkJev{enabled: true, resp: wjDoubtAll(), err: fmt.Errorf("%w: refused", jev.ErrCheckSkipped)}, 1)
}

func TestRLS_ExtractWorkerJevUnavailableWritesToday(t *testing.T) {
	wjAssertWritesToday(t, 954060, &wkJev{enabled: true, resp: wjDoubtAll(), err: fmt.Errorf("%w: unavailable", jev.ErrCheckSkipped)}, 1)
}

func TestRLS_ExtractWorkerJevDeadlineWritesToday(t *testing.T) {
	err := fmt.Errorf("%w: unavailable: %w", jev.ErrCheckSkipped, context.DeadlineExceeded)
	if !errors.Is(err, jev.ErrCheckSkipped) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the error %v must wrap both ErrCheckSkipped and DeadlineExceeded", err)
	}
	wjAssertWritesToday(t, 954070, &wkJev{enabled: true, resp: wjDoubtAll(), err: err}, 1)
}

func TestRLS_ExtractWorkerJevMissingAnswerWritesToday(t *testing.T) {
	wjAssertWritesToday(t, 954080, &wkJev{enabled: true, resp: wjNoul(map[string]float64{"invoice_number": 0})}, 1)
}

func TestRLS_ExtractWorkerJevWrongTypeWritesToday(t *testing.T) {
	resp := wjNoul(map[string]float64{"invoice_number": 0})
	resp.Answers["total"] = jev.Answer{Type: jev.TypeChoice, Choice: "no", Confidence: 1}
	wjAssertWritesToday(t, 954090, &wkJev{enabled: true, resp: resp}, 1)
}

func TestRLS_ExtractWorkerAuditCountsTheDoubtedFields(t *testing.T) {
	ctx := t.Context()
	pages := []extraction.Page{wjPage()}
	off := wkRunJev(t, ctx, 954100, pages, wjAI(), nil)
	on := wkRunJev(t, ctx, 954101, pages, wjAI(), &wkJev{enabled: true, resp: wjNoul(map[string]float64{"total": 0.0, "invoice_number": 1.0})})

	for _, c := range []struct {
		name string
		run  wkAIRun
	}{{"Jev nil", off}, {"doubt", on}} {
		_, flagged := wjAuditCounts(t, c.run)
		if want := wjFlaggedRankZero(c.run.rows); flagged != want {
			t.Errorf("%s: audit FlaggedCount = %d, want %d (rank-0 rows with a reason)", c.name, flagged, want)
		}
	}
	_, offFlagged := wjAuditCounts(t, off)
	_, onFlagged := wjAuditCounts(t, on)
	if onFlagged-offFlagged != 1 {
		t.Errorf("audit FlaggedCount doubt run %d minus Jev-nil run %d = %d, want 1", onFlagged, offFlagged, onFlagged-offFlagged)
	}
}

func TestRLS_ExtractWorkerAsksJevUnderEachJobsOwnTenant(t *testing.T) {
	ctx := t.Context()
	t.Setenv(jev.EnvFake, "true")
	t.Setenv(jev.EnvKey, "")
	var buf bytes.Buffer
	client, err := jev.FromEnv(slog.New(slog.NewJSONHandler(&buf, nil)))
	if err != nil {
		t.Fatalf("jev.FromEnv: %v", err)
	}
	pages := []extraction.Page{wjPage()}

	a := wkRunJev(t, ctx, 954110, pages, wjAI(), client)
	b := wkRunJev(t, ctx, 954111, pages, wjAI(), client)

	var tenants, purposes, outcomes []any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("unmarshal log line %q: %v", line, err)
		}
		if m["msg"] == "jev call" {
			tenants = append(tenants, m["tenant_id"])
			purposes = append(purposes, m["purpose"])
			outcomes = append(outcomes, m["outcome"])
		}
	}
	if len(tenants) != 2 {
		t.Fatalf("the logger recorded %d %q line(s), want exactly 2 (one per job): %q", len(tenants), "jev call", buf.String())
	}
	if want := []any{a.tenantID, b.tenantID}; !slices.Equal(tenants, want) {
		t.Errorf("jev call lines carry tenant_id %v, want %v in job order", tenants, want)
	}
	if want := []any{"value_check", "value_check"}; !slices.Equal(purposes, want) {
		t.Errorf("jev call lines carry purpose %v, want %v", purposes, want)
	}
	if want := []any{"fake", "fake"}; !slices.Equal(outcomes, want) {
		t.Errorf("jev call lines carry outcome %v, want %v", outcomes, want)
	}
}

// A decided header row never carries rank >= 1 rows in the pipeline, so the candidates checked
// here belong to an ambiguous field the check must neither ask nor touch.
func TestRLS_ExtractWorkerJevLeavesAnAmbiguousFieldsCandidatesAlone(t *testing.T) {
	ctx := t.Context()
	page := wjPage()
	page.Tokens = append(page.Tokens,
		extraction.Token{Text: "Issue Date: 2026-03-01", Region: extraction.Region{Page: 1, X0: 0.1, Y0: 0.30, X1: 0.5, Y1: 0.32}},
		extraction.Token{Text: "Issue Date: 2026-03-02", Region: extraction.Region{Page: 1, X0: 0.1, Y0: 0.40, X1: 0.5, Y1: 0.42}},
	)
	pages := []extraction.Page{page}
	off := wkRunJev(t, ctx, 954120, pages, wjAI(), nil)
	wpAssertRankZero(t, off.rows, "issue_date", stPtr("2026-03-01"), stPtr("ambiguous"))
	candidates := wjRankOneOrMore(off.rows)
	if len(candidates) == 0 {
		t.Fatalf("the Jev-nil run wrote no rank >= 1 row; the comparison below holds over nothing: %v", off.rows)
	}

	stub := &wkJev{enabled: true, resp: wjDoubtAll()}
	on := wkRunJev(t, ctx, 954121, pages, wjAI(), stub)
	if n := stub.count(); n != 1 {
		t.Fatalf("the Jev seam saw %d call(s), want 1", n)
	}
	if ids := slices.Sorted(maps.Keys(stub.reqs[0].Questions)); !slices.Equal(ids, []string{"document_type", "invoice_number", "total"}) {
		t.Errorf("question ids = %v, want [document_type invoice_number total]: an ambiguous field is never asked", ids)
	}
	wpAssertRankZero(t, on.rows, "total", stPtr("1935.00"), stPtr("unreadable"))
	wpAssertRankZero(t, on.rows, "invoice_number", stPtr("20417"), stPtr("unreadable"))
	wpAssertRankZero(t, on.rows, "issue_date", stPtr("2026-03-01"), stPtr("ambiguous"))
	if got := wjRankOneOrMore(on.rows); !slices.Equal(got, candidates) {
		t.Errorf("rank >= 1 rows = %v, want the Jev-nil run's %v", got, candidates)
	}
	if a, b := wkStrBoxes(off.boxes), wkStrBoxes(on.boxes); !slices.Equal(a, b) {
		t.Errorf("boxes differ from the Jev-nil run:\n nil: %v\n on:  %v", a, b)
	}
}

func wjRankOneOrMore(rows []wpRow) []string {
	var out []string
	for _, r := range rows {
		if r.rank >= 1 {
			out = append(out, r.String())
		}
	}
	return out
}

// One asker serves every tenant's jobs; each request carries only its own job's page and tenant.
func TestRLS_ExtractWorkerJevRequestCarriesOnlyItsOwnJobsDocument(t *testing.T) {
	ctx := t.Context()
	pageB := extraction.Page{Number: 1, WidthPt: 612, HeightPt: 792, Tokens: []extraction.Token{
		{Text: "Total: 7,264.50", Region: extraction.Region{Page: 1, X0: 0.1, Y0: 0.18, X1: 0.5, Y1: 0.20}},
	}}
	stub := &wkJev{enabled: true, resp: wjNoul(map[string]float64{"invoice_number": 1, "total": 1})}
	a := wkRunJev(t, ctx, 954130, []extraction.Page{wjPage()}, wjAI(), stub)
	b := wkRunJev(t, ctx, 954131, []extraction.Page{pageB}, nil, stub)

	if a.tenantID == b.tenantID {
		t.Fatalf("both jobs ran on tenant %s; the isolation below proves nothing", a.tenantID)
	}
	if n := stub.count(); n != 2 {
		t.Fatalf("the Jev seam saw %d call(s), want 2 (one per job)", n)
	}
	if want := []string{a.tenantID, b.tenantID}; !slices.Equal(stub.tenants, want) {
		t.Errorf("the Jev calls carried tenant(s) %v, want %v in job order", stub.tenants, want)
	}
	for _, c := range []struct {
		job                              string
		req                              jev.Request
		own, other, ownPrint, otherPrint string
	}{
		{"A", stub.reqs[0], "1935.00", "7264.50", "1,935.00", "7,264.50"},
		{"B", stub.reqs[1], "7264.50", "1935.00", "7,264.50", "1,935.00"},
	} {
		q, ok := c.req.Questions["total"]
		if !ok {
			t.Errorf("job %s: total was not asked: %v", c.job, slices.Sorted(maps.Keys(c.req.Questions)))
			continue
		}
		if !strings.Contains(q.Instructions, c.own) || strings.Contains(q.Instructions, c.other) {
			t.Errorf("job %s: the total question %q, want %s and never %s", c.job, q.Instructions, c.own, c.other)
		}
		if !strings.Contains(c.req.State, c.ownPrint) || strings.Contains(c.req.State, c.otherPrint) {
			t.Errorf("job %s: State %q, want its own page's %s and never %s", c.job, c.req.State, c.ownPrint, c.otherPrint)
		}
	}
	if _, ok := stub.reqs[1].Questions["invoice_number"]; ok {
		t.Errorf("job B was asked invoice_number, which only job A's page decides")
	}
}

func TestRLS_ExtractWorkerRecordsAConfidentReceiptAndNothingElse(t *testing.T) {
	ctx := t.Context()
	pages := []extraction.Page{wjPage()}
	off := wkRunJev(t, ctx, 954200, pages, wjAI(), nil)
	stub := &wkJev{enabled: true, resp: wjReceipt(map[string]float64{"invoice_number": 1, "total": 1})}
	got := wkRunJev(t, ctx, 954201, pages, wjAI(), stub)

	if n := stub.count(); n != 1 {
		t.Fatalf("the Jev seam saw %d call(s), want 1", n)
	}
	wjAssertVerdict(t, ctx, "the steered job", got.jobID, "receipt")
	wjAssertNoVerdict(t, ctx, "the Jev-nil twin", off.jobID)

	if len(off.rows) == 0 || len(off.boxes) == 0 {
		t.Fatalf("the Jev-nil run wrote %d row(s) and %d box(es); the equality below holds over nothing", len(off.rows), len(off.boxes))
	}
	if a, b := wjRender(off), wjRender(got); !slices.Equal(a, b) {
		t.Errorf("rows and boxes differ from the Jev-nil run:\n nil: %v\n got: %v", a, b)
	}
	of, ofl := wjAuditCounts(t, off)
	gf, gfl := wjAuditCounts(t, got)
	if of != gf || ofl != gfl {
		t.Errorf("audit {FieldCount %d, FlaggedCount %d}, want the Jev-nil run's {%d, %d}", gf, gfl, of, ofl)
	}
}

func TestRLS_ExtractWorkerRecordsNoVerdictWhenSkippedOrUnsure(t *testing.T) {
	ctx := t.Context()
	scores := map[string]float64{"invoice_number": 1, "total": 1}
	skipped := func(reason error) error { return fmt.Errorf("%w: %w", jev.ErrCheckSkipped, reason) }

	for _, tc := range []struct {
		name      string
		river     int64
		asker     *wkJev
		wantCalls int
	}{
		// Every skipped asker still carries a receipt answer: only the skip may drop it.
		{"off", 954210, &wkJev{enabled: false, resp: wjReceipt(scores)}, 0},
		{"refused", 954212, &wkJev{enabled: true, resp: wjReceipt(scores), err: skipped(errors.New("refused"))}, 1},
		{"unavailable", 954214, &wkJev{enabled: true, resp: wjReceipt(scores), err: skipped(errors.New("unavailable"))}, 1},
		{"deadline", 954216, &wkJev{enabled: true, resp: wjReceipt(scores), err: skipped(context.DeadlineExceeded)}, 1},
		{"tax invoice", 954218, &wkJev{enabled: true, resp: wjWithType(wjNoul(scores), "tax invoice", 1)}, 1},
		{"below threshold", 954220, &wkJev{enabled: true, resp: wjWithType(wjNoul(scores), "receipt", 0.89)}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.asker.err != nil && !errors.Is(tc.asker.err, jev.ErrCheckSkipped) {
				t.Fatalf("the error %v must wrap ErrCheckSkipped", tc.asker.err)
			}
			off, got := wjAssertWritesToday(t, tc.river, tc.asker, tc.wantCalls)
			wjAssertNoVerdict(t, ctx, "the Jev-nil run", off.jobID)
			wjAssertNoVerdict(t, ctx, "the asker run", got.jobID)
		})
	}

	t.Run("control: a receipt at 1 records", func(t *testing.T) {
		off, got := wjAssertWritesToday(t, 954222, &wkJev{enabled: true, resp: wjReceipt(scores)}, 1)
		wjAssertNoVerdict(t, ctx, "the Jev-nil run", off.jobID)
		wjAssertVerdict(t, ctx, "the asker run", got.jobID, "receipt")
	})
}

func TestRLS_ExtractWorkerRecordsTheFakesSteeredReceipt(t *testing.T) {
	ctx := t.Context()
	t.Setenv(jev.EnvFake, "true")
	t.Setenv(jev.EnvKey, "")
	var buf bytes.Buffer
	client, err := jev.FromEnv(slog.New(slog.NewJSONHandler(&buf, nil)))
	if err != nil {
		t.Fatalf("jev.FromEnv: %v", err)
	}
	steered := wjPage()
	steered.Tokens = append(steered.Tokens, extraction.Token{
		Text:   "JEVFAKE-CHOICE-cmVjZWlwdA",
		Region: extraction.Region{Page: 1, X0: 0.1, Y0: 0.95, X1: 0.5, Y1: 0.97},
	})

	a := wkRunJev(t, ctx, 954230, []extraction.Page{steered}, wjAI(), client)
	b := wkRunJev(t, ctx, 954231, []extraction.Page{wjPage()}, wjAI(), client)
	wjAssertVerdict(t, ctx, "the steered page", a.jobID, "receipt")
	wjAssertNoVerdict(t, ctx, "the plain page", b.jobID)

	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("unmarshal log line %q: %v", line, err)
		}
		if m["msg"] == "jev call" {
			lines = append(lines, m)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("the logger recorded %d %q line(s), want 2 (one per job): %q", len(lines), "jev call", buf.String())
	}
	for i, m := range lines {
		if m["outcome"] != "fake" || m["purpose"] != "value_check" || m["question_count"] != float64(3) {
			t.Errorf("jev call line %d = outcome %v, purpose %v, question_count %v; want fake, value_check, 3", i, m["outcome"], m["purpose"], m["question_count"])
		}
	}
}

// A verdict written outside the result transaction would survive the rolled-back attempt.
func TestRLS_ExtractWorkerVerdictSharesTheResultTransactionsFate(t *testing.T) {
	ctx := t.Context()
	auditBoom := errors.New("the audit port refused")
	scores := map[string]float64{"invoice_number": 1, "total": 1}

	// run drives one river job twice: attempt 1 with a failing audit port, attempt 2 with a working one.
	run := func(t *testing.T, riverJobID int64, second jev.Response) string {
		t.Helper()
		tenantID, documentID := wkFixture(t, ctx)
		ew := wpWorker(t, wkOK(), wpCorpusOpener(t), &wpReader{pages: []extraction.Page{wjPage()}}, wpStoreRules(t).load, &wkAuditRecorder{})
		ew.AI = wjAI()
		ew.Audit = func(context.Context, pgx.Tx, extraction.ExtractionAudit) error { return auditBoom }
		first := &wkJev{enabled: true, resp: wjReceipt(scores)}
		ew.Jev = first
		key := uuid.NewString()

		if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, key)); !errors.Is(err, auditBoom) {
			t.Fatalf("attempt 1: Work returned %v, want the audit port's error", err)
		}
		if n := first.count(); n != 1 {
			t.Fatalf("attempt 1: the Jev seam saw %d call(s), want 1 -- the receipt answer was never read", n)
		}
		xid := wkExtractionJobID(t, ctx, tenantID, riverJobID)
		wjAssertNoVerdict(t, ctx, "after the rolled-back attempt", xid)

		rec := &wkAuditRecorder{}
		ew.Audit = rec.record
		ew.Jev = &wkJev{enabled: true, resp: second}
		if err := ew.Work(ctx, extraction.NewExtractJobForTest(riverJobID, 2, 3, tenantID, documentID, key)); err != nil {
			t.Fatalf("attempt 2: Work: %v", err)
		}
		stAssertJobState(t, ctx, xid, "succeeded")
		if n := len(rec.events()); n != 1 {
			t.Errorf("attempt 2 called the audit port %d time(s), want 1", n)
		}
		return xid
	}

	xid := run(t, 954240, wjWithType(wjNoul(scores), "tax invoice", 1))
	wjAssertNoVerdict(t, ctx, "attempt 2 answered tax invoice", xid)

	t.Run("control: attempt 2 answered receipt records", func(t *testing.T) {
		xid := run(t, 954241, wjReceipt(scores))
		wjAssertVerdict(t, ctx, "attempt 2 answered receipt", xid, "receipt")
	})
}
