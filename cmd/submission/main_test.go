// main_test.go: cmd/submission's wiring specs. main() is not unit-testable -- it calls
// log.Fatalf and connects a real pool -- so the claims are read off main.go's source, the
// cmd/gateway/main_test.go idiom: locate an anchor by name, t.Fatal if it is missing so a
// rename cannot make the scan vacuous, then assert inside a fixed window after it.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// TestSubmissionMainFatalsOnAdapterSelectError: AC-6 / AC-7 (Core AC-6's binary-level
// half). Static source scan proving the submission.Select( call site's error path
// terminates the process via log.Fatalf/log.Fatal, before the next top-level statement.
func TestSubmissionMainFatalsOnAdapterSelectError(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read cmd/submission/main.go: %v", err)
	}
	src := string(b)

	idx := strings.Index(src, "submission.Select(")
	if idx == -1 {
		t.Fatal(`cmd/submission/main.go does not contain a "submission.Select(" call site -- this test's anchor moved (or the wiring hasn't landed yet)`)
	}
	end := idx + 500
	if end > len(src) {
		end = len(src)
	}
	window := src[idx:end]

	if !strings.Contains(window, "log.Fatalf") && !strings.Contains(window, "log.Fatal(") {
		t.Errorf("no log.Fatalf/log.Fatal found within 500 bytes after the submission.Select( call site -- the adapter-selection error path must terminate the process (Core AC-6):\n%s", window)
	}
}

// TestSubmissionMain_FatalOnAdapterConfigError: M5-03-05 AC-7. Static source scan proving that
// main() reads the mock's config from the environment BEFORE it builds the registry, and that
// the config error path terminates the process.
//
// DELIBERATELY NOT a copy of the 500-byte window above. With this subtask's wiring
// `submission.Select(` sits roughly 120 bytes after `submission.MockConfigFromEnv(`, so a fixed
// 500-byte window anchored at the config call would SWALLOW the Select error path's own
// log.Fatalf (main.go's existing wiring) and pass with a full 100% green even if the config
// error were ignored entirely. The window here is bounded by the NEXT anchor instead, and the
// ordering between the two anchors is asserted rather than assumed -- which is the real
// requirement anyway: a config read that happened AFTER the registry was built could not have
// configured it.
//
// HOW MUCH THIS PROVES, honestly: a source scan shows that a token appears inside a byte range,
// not that the branch is reachable. The behavioural version needs an `adapterFromEnv` seam
// extracted out of main(), which would relocate the shipped M5-02 Select wiring and break the
// anchor of TestSubmissionMainFatalsOnAdapterSelectError above -- a deliberately-shipped
// assertion this story has no mandate to weaken. Recorded as an M5-04 follow-up; M5-04 consumes
// `adapter` and wants a testable seam regardless. The compensating control is that
// MockConfigFromEnv's three branches ARE genuinely unit-tested
// (internal/submission/mock_adapter_test.go's TestMockConfigFromEnv), which shrinks what this
// scan leaves unproven to "main calls it and fatals" -- two lines, verifiable by eye. It is also
// COMMENT-BLIND: strings.Index matches raw bytes, so a comment quoting either anchor would
// satisfy it. Same limitation as the Select scan above; the reason main.go's own TODO avoids
// writing the call-site form.
func TestSubmissionMain_FatalOnAdapterConfigError(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read cmd/submission/main.go: %v", err)
	}
	src := string(b)

	cfgIdx := strings.Index(src, "submission.MockConfigFromEnv(")
	if cfgIdx == -1 {
		t.Fatal(`cmd/submission/main.go does not contain a "submission.MockConfigFromEnv(" call site -- ` +
			`the mock's latency knob is read nowhere, so APP_ADAPTER_MOCK_LATENCY is inert (or this ` +
			`test's anchor moved)`)
	}
	selIdx := strings.Index(src, "submission.Select(")
	if selIdx == -1 {
		t.Fatal(`cmd/submission/main.go does not contain a "submission.Select(" call site -- this test's ` +
			`closing anchor moved`)
	}
	if selIdx <= cfgIdx {
		t.Fatalf("submission.Select( appears at byte %d, BEFORE submission.MockConfigFromEnv( at byte "+
			"%d -- the adapter config must be read before the registry is built, or the registry cannot "+
			"have been built from it", selIdx, cfgIdx)
	}

	window := src[cfgIdx:selIdx]

	if !strings.Contains(window, "if err != nil") {
		t.Errorf("no `if err != nil` between the submission.MockConfigFromEnv( call site and the "+
			"submission.Select( one -- MockConfigFromEnv's error is being discarded, so a malformed "+
			"APP_ADAPTER_MOCK_LATENCY would boot silently:\n%s", window)
	}
	if !strings.Contains(window, "log.Fatalf") && !strings.Contains(window, "log.Fatal(") {
		t.Errorf("no log.Fatalf/log.Fatal between the submission.MockConfigFromEnv( call site and the "+
			"submission.Select( one -- the adapter-config error path must terminate the process, exactly "+
			"as the Select path does:\n%s", window)
	}
	if !strings.Contains(window, "NewDefaultRegistry(") {
		t.Errorf("no NewDefaultRegistry( between the submission.MockConfigFromEnv( call site and the "+
			"submission.Select( one -- the config that was just read must be what the registry is built "+
			"from:\n%s", window)
	}
}

// TestSubmissionMain_NoNonProductionAdapterFallback: M5-04-08 AC-2. The two tests above
// prove a log.Fatalf sits somewhere inside a fixed window after submission.Select( -- true
// of both the OLD conditional fatal (IsProduction(...) || appAdapter != "") and the NEW
// unconditional one, so neither is sufficient on its own to prove the conditional fallback
// branch was actually deleted, not merely not-shown-by-the-window. This test asserts that
// positively:
//
//  1. the exact log.Printf string the non-production fallback used to emit
//     ("continuing with no adapter configured") is gone from the file entirely -- its
//     presence anywhere, even in a comment, would mean the fallback (or a vestige of it)
//     survived;
//  2. IsProduction( does not appear in the window between submission.Select( and the next
//     statement -- deliberately scoped to that window, not the whole file: registry.go and
//     other call sites may legitimately reference IsProduction elsewhere.
func TestSubmissionMain_NoNonProductionAdapterFallback(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read cmd/submission/main.go: %v", err)
	}
	src := string(b)

	if strings.Contains(src, "continuing with no adapter configured") {
		t.Error(`cmd/submission/main.go still contains "continuing with no adapter configured" -- ` +
			"the non-production fallback branch (or a vestige of it) was not fully removed; a failed " +
			"adapter Select must be fatal in EVERY environment")
	}

	idx := strings.Index(src, "submission.Select(")
	if idx == -1 {
		t.Fatal(`cmd/submission/main.go does not contain a "submission.Select(" call site -- this test's anchor moved`)
	}
	end := idx + 500
	if end > len(src) {
		end = len(src)
	}
	window := src[idx:end]

	if strings.Contains(window, "IsProduction(") {
		t.Errorf("found IsProduction( within 500 bytes after the submission.Select( call site -- the "+
			"adapter-selection error path must be an unconditional log.Fatalf, not gated on "+
			"IsProduction(...) || appAdapter != \"\":\n%s", window)
	}
}

// ---------------------------------------------------------------------------
// The extraction wiring: the four seams main() composes, and the guards that
// prove main() actually uses them.
// ---------------------------------------------------------------------------

// wtRepoRoot locates the worktree root; every module-wide path below is relative to it.
func wtRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		t.Fatal("git reported an empty worktree root; every scan below would read nothing")
	}
	return root
}

// wtCallName renders a call target: "queueConfigs" for a package function, "pkg.Name" for a
// qualified one. Anything else yields "".
func wtCallName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok {
			return x.Name + "." + f.Sel.Name
		}
		return "." + f.Sel.Name
	}
	return ""
}

// wtCloser counts closes so a leak (0) and a double close (2) are both visible.
type wtCloser struct {
	io.Reader
	closes int
}

func (c *wtCloser) Close() error { c.closes++; return nil }

// wtErrReader fails on the first Read, the shape a truncated object stream has.
type wtErrReader struct{ err error }

func (r wtErrReader) Read([]byte) (int, error) { return 0, r.err }

// wtScript is one scripted document.Service.Open outcome plus what the closure passed it.
type wtScript struct {
	doc document.Document
	obj document.Object
	err error

	calls int
	ctx   context.Context
	id    string
	rng   string
}

func (s *wtScript) open(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error) {
	s.calls++
	s.ctx, s.id, s.rng = ctx, id, rangeHeader
	return s.doc, s.obj, s.err
}

// TestWorkerBundle_CarriesAllThreeKinds is the behavioural half of AC #1. river.AddWorkerSafely
// reports "already registered" rather than panicking, and type inference means this package
// never names the unexported args type. A second bundle, or a bundle built after queue.New has
// read the first, fails the positive arm.
func TestWorkerBundle_CarriesAllThreeKinds(t *testing.T) {
	probes := []struct {
		kind string
		add  func(*river.Workers) error
	}{
		{"extraction_extract", func(b *river.Workers) error {
			return river.AddWorkerSafely(b, &extraction.ExtractWorker{})
		}},
		{"submission_submit", func(b *river.Workers) error {
			return river.AddWorkerSafely(b, &submission.SubmitWorker{})
		}},
		{"submission_poll", func(b *river.Workers) error {
			return river.AddWorkerSafely(b, &submission.PollWorker{})
		}},
	}

	// Control: on an empty bundle every probe must report ABSENT, or a later "already
	// registered" verdict would mean nothing.
	for _, p := range probes {
		if err := p.add(river.NewWorkers()); err != nil {
			t.Fatalf("probe for %q rejected an EMPTY bundle (%v); it cannot tell present from absent, so the assertions below would pass vacuously", p.kind, err)
		}
	}

	// Absent arm on a REAL bundle: submission.Workers alone carries the two submission kinds
	// and not extraction.
	base := submission.Workers(&submission.SubmitWorker{}, &submission.PollWorker{})
	if err := probes[0].add(base); err != nil {
		t.Fatalf("submission.Workers already carries %q (%v); the positive arm below could then pass without extraction.AddTo being called at all", probes[0].kind, err)
	}
	for _, p := range probes[1:] {
		if err := p.add(submission.Workers(&submission.SubmitWorker{}, &submission.PollWorker{})); err == nil {
			t.Fatalf("submission.Workers does not carry %q; the probe is looking at the wrong kind", p.kind)
		}
	}

	bundle := workerBundle(&submission.SubmitWorker{}, &submission.PollWorker{}, &extraction.ExtractWorker{})
	if bundle == nil {
		t.Fatal("workerBundle returned nil; a River client takes exactly one bundle")
	}
	for _, p := range probes {
		err := p.add(bundle)
		if err == nil {
			t.Errorf("workerBundle does not carry %q: a worker missing from the ONE bundle queue.New reads never fetches a job", p.kind)
			continue
		}
		if !strings.Contains(err.Error(), "already registered") || !strings.Contains(err.Error(), strconv.Quote(p.kind)) {
			t.Errorf("probing %q returned %q, want the already-registered error naming that kind", p.kind, err)
		}
	}
}

// TestNewExtractWorker_SetsEveryCollaborator: a nil field compiles and fails only on the first
// job, so the constructor keeps every collaborator at one call site. Reflection over the
// non-embedded fields, so a collaborator added later is covered too.
func TestNewExtractWorker_SetsEveryCollaborator(t *testing.T) {
	pool := &pgxpool.Pool{}
	ext := extraction.NewMockExtractor()
	logger := slog.Default()
	const sentinel = "sentinel/content-type"
	open := func(context.Context, string) (extraction.Document, error) {
		return extraction.Document{ContentType: sentinel}, nil
	}

	pages := &extraction.PageStore{Reader: extraction.NewPDFiumReader(), Sink: func(context.Context, string, []byte) error { return nil }}

	audited := 0
	auditor := func(context.Context, pgx.Tx, extraction.ExtractionAudit) error {
		audited++
		return nil
	}

	// Test-local, non-nil: the reflection loop below errors on any nil collaborator, and main()'s
	// own Text is followed back to selectTextReader in TestSubmissionMain_WiresTheQueueSeams.
	textFake := extraction.NewPDFiumReader()
	ruled := 0
	rules := func(context.Context, string, string) ([]extraction.AnchorRule, error) {
		ruled++
		return nil, nil
	}

	ew := newExtractWorker(pool, ext, open, pages, auditor, textFake, rules, logger)
	if ew == nil {
		t.Fatal("newExtractWorker returned nil")
	}

	rv := reflect.ValueOf(*ew)
	checked := 0
	for i := range rv.NumField() {
		if rv.Type().Field(i).Anonymous {
			continue
		}
		f := rv.Field(i)
		switch f.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Func, reflect.Map, reflect.Slice:
			checked++
			if f.IsNil() {
				t.Errorf("ExtractWorker.%s is nil: it compiles and panics on the first job", rv.Type().Field(i).Name)
			}
		}
	}
	if checked < 8 {
		t.Fatalf("only %d nillable collaborator field(s) inspected on ExtractWorker, want at least 8 (Pool, Extractor, Open, Pages, Audit, Text, Rules, Logger) -- the loop above examined almost nothing", checked)
	}

	if ew.Pool != pool {
		t.Error("ExtractWorker.Pool is not the pool passed in")
	}
	if ew.Extractor != ext {
		t.Error("ExtractWorker.Extractor is not the extractor passed in")
	}
	if ew.Logger != logger {
		t.Error("ExtractWorker.Logger is not the logger passed in")
	}
	if ew.Pages != pages {
		t.Error("ExtractWorker.Pages is not the page store passed in")
	}
	if ew.Open == nil {
		t.Fatal("ExtractWorker.Open is nil")
	}
	doc, err := ew.Open(context.Background(), "doc")
	if err != nil || doc.ContentType != sentinel {
		t.Errorf("ExtractWorker.Open returned (%+v, %v), want the sentinel opener passed in", doc, err)
	}
	if ew.Audit == nil {
		t.Fatal("ExtractWorker.Audit is nil")
	}
	// Follows the identifier: a field set to some OTHER auditor is nil-free and reports nothing.
	if err := ew.Audit(context.Background(), nil, extraction.ExtractionAudit{}); err != nil || audited != 1 {
		t.Errorf("ExtractWorker.Audit ran %d time(s) and returned %v, want the recording auditor passed in to run exactly once", audited, err)
	}
	if ew.Text != textFake {
		t.Error("ExtractWorker.Text is not the reader passed in")
	}
	if ew.Rules == nil {
		t.Fatal("ExtractWorker.Rules is nil")
	}
	// Same reason as Audit above: a Rules set to some other loader is nil-free and proves nothing.
	if _, err := ew.Rules(context.Background(), "t", "fp"); err != nil || ruled != 1 {
		t.Errorf("ExtractWorker.Rules ran %d time(s) and returned %v, want the recording loader passed in to run exactly once", ruled, err)
	}
}

// TestQueueConfigs_NamesBothQueues: AC #2. Extraction gets its own queue so a slow read cannot
// starve submission; river.NewClient refuses MaxWorkers < 1.
func TestQueueConfigs_NamesBothQueues(t *testing.T) {
	got := queueConfigs()
	if len(got) == 0 {
		t.Fatal("queueConfigs returned an empty map; the assertions below would pass vacuously")
	}
	if extraction.QueueName == river.QueueDefault {
		t.Fatalf("extraction.QueueName is %q, the River default: the two-queue assertion below collapses to one", extraction.QueueName)
	}

	want := map[string]int{river.QueueDefault: 10, extraction.QueueName: 2}
	names := make([]string, 0, len(got))
	for name := range got {
		names = append(names, name)
	}
	sort.Strings(names)
	wantNames := []string{extraction.QueueName, river.QueueDefault}
	sort.Strings(wantNames)
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("queueConfigs names %v, want exactly %v", names, wantNames)
	}
	for name, n := range want {
		if got[name].MaxWorkers != n {
			t.Errorf("queue %q has MaxWorkers %d, want %d", name, got[name].MaxWorkers, n)
		}
	}
}

// TestSubmissionMain_WiresTheQueueSeams: the seams above are only worth their assertions if
// main() is what calls them. AST, not a byte scan, so reformatting main.go cannot break it.
func TestSubmissionMain_WiresTheQueueSeams(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/submission/main.go: %v", err)
	}

	var configs []*ast.CompositeLit
	addWorkerHits := 0
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CompositeLit:
			if wtCallName(x.Type) == "queue.Config" {
				configs = append(configs, x)
			}
		case *ast.SelectorExpr:
			if x.Sel.Name == "AddWorker" {
				addWorkerHits++
			}
		}
		return true
	})

	if len(configs) != 1 {
		t.Fatalf("cmd/submission/main.go builds %d queue.Config literal(s), want exactly 1 -- with none, every assertion below is vacuous; with two, the one queue.New reads is ambiguous", len(configs))
	}
	seams := map[string]string{"Queues": "queueConfigs", "Workers": "workerBundle"}
	var bundleArgs []ast.Expr
	for _, elt := range configs[0].Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		want, tracked := seams[key.Name]
		if !tracked {
			continue
		}
		delete(seams, key.Name)
		call, ok := kv.Value.(*ast.CallExpr)
		if !ok || wtCallName(call.Fun) != want {
			t.Errorf("queue.Config.%s is not a %s(...) call (%T) -- an inline literal here is not the value the seam test above inspects, so the two can disagree", key.Name, want, kv.Value)
			continue
		}
		if key.Name == "Workers" {
			bundleArgs = call.Args
		}
	}
	for key, want := range seams {
		t.Errorf("queue.Config carries no %s key, so %s is unreachable from the composition root", key, want)
	}

	// D-21: one bundle, built by extraction.AddTo. A bare river.AddWorker here is how a second
	// bundle gets created.
	if addWorkerHits != 0 {
		t.Errorf("cmd/submission/main.go selects AddWorker %d time(s); registration belongs in submission.Workers and extraction.AddTo", addWorkerHits)
	}
	control := filepath.Join(wtRepoRoot(t), "internal", "submission", "worker.go")
	cf, err := parser.ParseFile(token.NewFileSet(), control, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", control, err)
	}
	controlHits := 0
	ast.Inspect(cf, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "AddWorker" {
			controlHits++
		}
		return true
	})
	if controlHits < 2 {
		t.Fatalf("the AddWorker matcher found %d hit(s) in internal/submission/worker.go, want at least 2 -- the absence found in main.go above is the matcher being broken, not a clean file", controlHits)
	}

	// Every seam above can be called correctly and still be handed something else. These three
	// follow the identifiers: the worker queue.New registers, the opener that worker reads
	// through, and the store that opener reaches.
	poolName, _ := wtOneCall(t, f, "db.NewPool")
	objName, _ := wtOneCall(t, f, "document.NewS3Store")
	svcName, svcArgs := wtOneCall(t, f, "document.NewService")
	ewName, ewArgs := wtOneCall(t, f, "newExtractWorker")

	if len(ewArgs) != 8 {
		t.Fatalf("newExtractWorker is called with %d argument(s), want 8 (pool, extractor, opener, pages, auditor, text, rules, logger)", len(ewArgs))
	}
	// EXTR-17-03 AC-8: the walk covers all 8 arguments. Argument 5's exemption is gone with the
	// literal nil it protected.
	const ewTextArg = 5
	for i, arg := range ewArgs {
		if id, ok := arg.(*ast.Ident); ok && id.Name == "nil" {
			t.Errorf("newExtractWorker argument %d is nil: it compiles, registers, and breaks on the first job -- a nil auditor errors out on Work's own guard, the rest panic", i)
		}
	}

	// EXTR-17-03 AC-4/AC-7. This replaces EXTR-17-01's literal-nil pin on argument 5 and proves
	// strictly more: both the extractor and the text reader are followed back to the selector
	// that built them, each selector is called from main() exactly once (wtOneCall), and the
	// two are shown to read the SAME two values. Without that last pair one selector could read
	// EXTRACTOR and the other EXTRACTOR_TEXT while every behavioural test still passed -- which
	// is how "one env var selects both seams" gets violated while green.
	extName, extractorArgs := wtOneCall(t, f, "selectExtractor")
	if id, ok := ewArgs[1].(*ast.Ident); !ok || id.Name != extName {
		t.Errorf("newExtractWorker's extractor argument is %s, want %s -- the extractor selectExtractor built and main() already fatals on. Any other value here labels extraction_jobs.extractor with something EXTRACTOR did not select", wtRender(ewArgs[1]), extName)
	}
	textName, textArgs := wtOneCall(t, f, "selectTextReader")
	if id, ok := ewArgs[ewTextArg].(*ast.Ident); !ok || id.Name != textName {
		t.Errorf("newExtractWorker's text argument is %s, want %s -- the reader selectTextReader built and main() already fatals on. Any other value here reads text through something EXTRACTOR did not select", wtRender(ewArgs[ewTextArg]), textName)
	}
	if len(extractorArgs) != 2 || len(textArgs) != 2 {
		t.Fatalf("selectExtractor is called with %d argument(s) and selectTextReader with %d, want 2 each (EXTRACTOR, DOCLING_URL)", len(extractorArgs), len(textArgs))
	}
	for i, label := range []string{"EXTRACTOR", "DOCLING_URL"} {
		e, eok := extractorArgs[i].(*ast.Ident)
		x, xok := textArgs[i].(*ast.Ident)
		if !eok || !xok || e.Name != x.Name {
			t.Errorf("selectExtractor's %s argument is %s and selectTextReader's is %s, want one identifier read once and handed to both -- two separate reads can drift onto different variables", label, wtRender(extractorArgs[i]), wtRender(textArgs[i]))
		}
	}

	// The rules loader is real and non-nil at this call site whatever EXTRACTOR selects.
	if sel, ok := ewArgs[6].(*ast.SelectorExpr); !ok || sel.Sel.Name != "AnchorRulesFor" {
		t.Errorf("newExtractWorker's rules argument is %s, want an ...AnchorRulesFor method value: the learned-rule read is only reachable if main() builds it", wtRender(ewArgs[6]))
	}

	// 1. The worker on the bundle is the one newExtractWorker built. A bare
	//    &extraction.ExtractWorker{} here compiles, registers the kind, and fails job one on
	//    Work's nil-Audit guard before its nil pool can panic.
	if len(bundleArgs) != 3 {
		t.Fatalf("workerBundle is called with %d argument(s), want 3 (sw, pw, ew)", len(bundleArgs))
	}
	if id, ok := bundleArgs[2].(*ast.Ident); !ok || id.Name != ewName {
		t.Errorf("workerBundle's third argument is %s, want %s -- the worker newExtractWorker built. Any other value here registers the kind with nil collaborators and works no job", wtRender(bundleArgs[2]), ewName)
	}

	// 2. That worker reads through an opener built over the document service's own Open.
	if call, ok := ewArgs[2].(*ast.CallExpr); !ok || wtCallName(call.Fun) != "newDocumentOpener" {
		t.Errorf("newExtractWorker's opener argument is %s, want a newDocumentOpener(...) call: the capped, always-closing closure is only reached if main() builds it", wtRender(ewArgs[2]))
	} else if len(call.Args) != 1 {
		t.Errorf("newDocumentOpener is called with %d argument(s), want 1", len(call.Args))
	} else if sel, ok := call.Args[0].(*ast.SelectorExpr); !ok || wtCallName(sel) != svcName+".Open" {
		t.Errorf("newDocumentOpener is built over %s, want %s.Open: an opener over anything else reads no document, and nothing else in this file would notice", wtRender(call.Args[0]), svcName)
	}

	// 3. The page store that worker renders and PUTs through: the real reader, and a sink over
	//    the same object store the source-document path already uses. A sink over anything else
	//    writes page images somewhere nothing can read them back.
	pagesLit, ok := ewArgs[3].(*ast.UnaryExpr)
	if !ok || pagesLit.Op != token.AND {
		t.Errorf("newExtractWorker's page-store argument is %s, want a &extraction.PageStore{...} literal", wtRender(ewArgs[3]))
	} else if lit, ok := pagesLit.X.(*ast.CompositeLit); !ok || wtCallName(lit.Type) != "extraction.PageStore" {
		t.Errorf("newExtractWorker's page-store argument points at %s, want an extraction.PageStore literal", wtRender(pagesLit.X))
	} else {
		fields := map[string]ast.Expr{}
		for _, elt := range lit.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if key, ok := kv.Key.(*ast.Ident); ok {
					fields[key.Name] = kv.Value
				}
			}
		}
		if call, ok := fields["Reader"].(*ast.CallExpr); !ok || wtCallName(call.Fun) != "extraction.NewPDFiumReader" {
			t.Errorf("extraction.PageStore.Reader is %s, want an extraction.NewPDFiumReader() call", wtRender(fields["Reader"]))
		}
		if call, ok := fields["Sink"].(*ast.CallExpr); !ok || wtCallName(call.Fun) != "newPageSink" {
			t.Errorf("extraction.PageStore.Sink is %s, want a newPageSink(...) call: the adapter onto the object store is only reached if main() builds it", wtRender(fields["Sink"]))
		} else if len(call.Args) != 1 {
			t.Errorf("newPageSink is called with %d argument(s), want 1", len(call.Args))
		} else if id, ok := call.Args[0].(*ast.Ident); !ok || id.Name != objName {
			t.Errorf("newPageSink is built over %s, want %s -- the store document.NewS3Store built and main() already fatals on", wtRender(call.Args[0]), objName)
		}
	}

	// 4. That service is built over the shared pool and the object store AC #8 fatals on. Without
	//    this, document.ConfigFromEnv can fatal at boot over a store nothing ever reaches.
	if len(svcArgs) != 2 {
		t.Fatalf("document.NewService is called with %d argument(s), want 2 (row store, object store)", len(svcArgs))
	}
	if store, ok := svcArgs[0].(*ast.CallExpr); !ok || wtCallName(store.Fun) != "document.NewStore" {
		t.Errorf("document.NewService's row store is %s, want a document.NewStore(...) call", wtRender(svcArgs[0]))
	} else if len(store.Args) != 1 {
		t.Errorf("document.NewStore is called with %d argument(s), want 1 (the pool)", len(store.Args))
	} else if id, ok := store.Args[0].(*ast.Ident); !ok || id.Name != poolName {
		t.Errorf("document.NewStore is given %s, want the shared pool %s that db.NewPool returned", wtRender(store.Args[0]), poolName)
	}
	if id, ok := svcArgs[1].(*ast.Ident); !ok || id.Name != objName {
		t.Errorf("document.NewService's object store is %s, want %s -- the store document.NewS3Store built and main() already fatals on", wtRender(svcArgs[1]), objName)
	}

	// 5. The auditor that worker writes its terminal outcome through, and the logger still last.
	//    A bare func literal here compiles and writes rows nothing in this file can read.
	if call, ok := ewArgs[4].(*ast.CallExpr); !ok || wtCallName(call.Fun) != "newExtractionAuditor" {
		t.Errorf("newExtractWorker's auditor argument is %s, want a newExtractionAuditor() call: the two literal event names and their inline payloads are only reached if main() builds it", wtRender(ewArgs[4]))
	} else if len(call.Args) != 0 {
		t.Errorf("newExtractionAuditor is called with %d argument(s), want 0", len(call.Args))
	}
	if sel, ok := ewArgs[7].(*ast.SelectorExpr); !ok || sel.Sel.Name != "Logger" {
		t.Errorf("newExtractWorker's last argument is %s, want the logger: every constructor in this file keeps logger last, and an auditor appended after it silently swaps the two", wtRender(ewArgs[7]))
	}
}

// wtMainPackageDeps runs go list once over the module and returns, per import path of interest,
// the package-main packages whose TRANSITIVE dependencies include it. A library-level import
// links a package into a binary just as surely as a direct one, which a file's own import block
// cannot show. Also returns the package-main population so a caller can floor it.
func wtMainPackageDeps(t *testing.T, root string, paths []string) (map[string][]string, []string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", "list", "-f", "{{.ImportPath}}|{{.Name}}|{{join .Deps \" \"}}", "./...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("go list ./... in %s: %v: %s -- a failed listing reads exactly like a clean one", root, err, ee.Stderr)
		}
		t.Fatalf("go list ./... in %s: %v", root, err)
	}

	want := map[string]bool{}
	for _, p := range paths {
		want[p] = true
	}
	hits := map[string][]string{}
	var mains []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 || parts[1] != "main" {
			continue
		}
		mains = append(mains, parts[0])
		for _, dep := range strings.Fields(parts[2]) {
			if want[dep] {
				hits[dep] = append(hits[dep], parts[0])
			}
		}
	}
	for p := range hits {
		sort.Strings(hits[p])
	}
	sort.Strings(mains)
	return hits, mains
}

// TestSubmissionMain_AddsNoNewBinary: Core AC-8. The directory check alone is evaded by any
// name other than "extraction", so the second half asserts that the submission binary is the
// only one that LINKS internal/extraction -- transitively, since a library-level import reaches
// a binary that names neither package.
func TestSubmissionMain_AddsNoNewBinary(t *testing.T) {
	entries, err := os.ReadDir("..")
	if err != nil {
		t.Fatalf("read cmd/: %v -- a silent read error here reads exactly like a clean directory", err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	wantDirs := []string{
		"dashboard", "gateway", "invoice", "notifications", "opsconsole",
		"portfolio", "reconciliation", "submission", "tenancy", "validation",
	}
	if !reflect.DeepEqual(dirs, wantDirs) {
		t.Errorf("cmd/ holds %v, want exactly %v: extraction runs inside the submission binary, and a new service directory is a new Railway service nobody provisioned", dirs, wantDirs)
	}

	const (
		modulePkg     = "github.com/SimonOsipov/invoice-os/"
		extractionPkg = modulePkg + "internal/extraction"
		submissionPkg = modulePkg + "internal/submission"
	)
	hits, mains := wtMainPackageDeps(t, wtRepoRoot(t), []string{extractionPkg, submissionPkg})

	if len(mains) < 10 {
		t.Fatalf("go list reported %d package-main package(s) in this module, want at least 10 -- a clean report over an empty listing means nothing", len(mains))
	}

	// Control needle, same listing and same matcher. internal/submission is imported directly by
	// cmd/submission alone but LINKED into five binaries; cmd/invoice reaches it only through
	// internal/invoice. Its presence here is what proves this check follows the whole graph
	// rather than one file's import block -- the difference the extraction assertion rests on.
	control := hits[submissionPkg]
	for _, want := range []string{modulePkg + "cmd/invoice", modulePkg + "cmd/submission"} {
		if !slices.Contains(control, want) {
			t.Fatalf("the dependency matcher reports internal/submission linked into %v, want that to include %s; cmd/invoice reaches it only transitively, so its absence means this check no longer follows the graph and the extraction assertion below would pass having examined nothing", control, want)
		}
	}

	if want := []string{modulePkg + "cmd/submission"}; !reflect.DeepEqual(hits[extractionPkg], want) {
		t.Errorf("internal/extraction is linked into %v, want exactly %v: the mock extractor ships inside the submission binary and no other, and one library-level import is enough to put it in a binary nobody provisioned", hits[extractionPkg], want)
	}
}

// TestNewDocumentOpener_CapsAtDocumentSizeCeiling: AC #4. The cap bounds the READ, never
// obj.Size -- Object.Size is what the store reports, and on a 206 it is the range length.
func TestNewDocumentOpener_CapsAtDocumentSizeCeiling(t *testing.T) {
	// documents.size_bytes CHECKs <= this, so a document of exactly this length is legal.
	const ceiling = 15728640
	if maxDocumentBytes != ceiling {
		t.Fatalf("maxDocumentBytes = %d, want %d (the documents.size_bytes CHECK)", maxDocumentBytes, ceiling)
	}

	t.Run("at the ceiling reads whole", func(t *testing.T) {
		body := &wtCloser{Reader: bytes.NewReader(make([]byte, ceiling))}
		s := &wtScript{obj: document.Object{Body: body, Size: ceiling}}
		doc, err := newDocumentOpener(s.open)(context.Background(), "doc")
		if err != nil {
			t.Fatalf("a document of exactly %d bytes is legal and must read whole, got %v", ceiling, err)
		}
		if len(doc.Bytes) != ceiling {
			t.Errorf("read %d bytes, want %d", len(doc.Bytes), ceiling)
		}
		if body.closes != 1 {
			t.Errorf("body closed %d time(s), want exactly 1", body.closes)
		}
	})

	t.Run("one byte over is refused even when Size lies", func(t *testing.T) {
		body := &wtCloser{Reader: bytes.NewReader(make([]byte, ceiling+1))}
		// Size deliberately understates the stream: a cap on obj.Size alone would let a lying
		// or ranged store push unbounded bytes into memory.
		s := &wtScript{obj: document.Object{Body: body, Size: 3}}
		doc, err := newDocumentOpener(s.open)(context.Background(), "doc")
		if err == nil {
			t.Fatalf("a %d-byte body was accepted, and %d bytes came back presented as success", ceiling+1, len(doc.Bytes))
		}
		if len(doc.Bytes) != 0 {
			t.Errorf("the refused document still carries %d bytes; the error path must return the zero Document", len(doc.Bytes))
		}
		if body.closes != 1 {
			t.Errorf("body closed %d time(s), want exactly 1", body.closes)
		}
	})
}

// TestNewDocumentOpener_ClosesBodyOnReadError: AC #4. Exactly once -- a defer plus an explicit
// Close in the error branch is a double close, which only the count catches.
func TestNewDocumentOpener_ClosesBodyOnReadError(t *testing.T) {
	boom := errors.New("boom")
	body := &wtCloser{Reader: wtErrReader{err: boom}}
	s := &wtScript{obj: document.Object{Body: body, Size: 3}}

	_, err := newDocumentOpener(s.open)(context.Background(), "doc")
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the read error", err)
	}
	if body.closes != 1 {
		t.Errorf("body closed %d time(s), want exactly 1", body.closes)
	}
}

// TestNewDocumentOpener_ClosesBodyWhenOpenErrors: the leak two callers in this repo still have
// -- registering the defer after the error return drops a body handed back WITH an error.
func TestNewDocumentOpener_ClosesBodyWhenOpenErrors(t *testing.T) {
	boom := errors.New("open failed")
	body := &wtCloser{Reader: bytes.NewReader([]byte("abc"))}
	s := &wtScript{obj: document.Object{Body: body, Size: 3}, err: boom}

	_, err := newDocumentOpener(s.open)(context.Background(), "doc")
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the open error", err)
	}
	if body.closes != 1 {
		t.Errorf("body closed %d time(s) after an open that returned BOTH a body and an error, want exactly 1", body.closes)
	}
}

// TestNewDocumentOpener_RejectsANilBody: io.ReadAll over a nil ReadCloser panics, and an empty
// document would otherwise be extracted as if it were the real one.
func TestNewDocumentOpener_RejectsANilBody(t *testing.T) {
	s := &wtScript{obj: document.Object{Body: nil, Size: 0}}

	doc, err := newDocumentOpener(s.open)(context.Background(), "doc-42")
	if err == nil {
		t.Fatalf("a nil body was accepted and yielded %+v", doc)
	}
	if !strings.Contains(err.Error(), "doc-42") {
		t.Errorf("err = %q, want the document id in it", err)
	}
	if len(doc.Bytes) != 0 || doc.ContentType != "" {
		t.Errorf("the refused document is %+v, want the zero Document", doc)
	}
}

// TestNewDocumentOpener_PassesEmptyRangeHeader: a stray range would extract a fragment and
// report success.
func TestNewDocumentOpener_PassesEmptyRangeHeader(t *testing.T) {
	body := &wtCloser{Reader: bytes.NewReader([]byte("abc"))}
	s := &wtScript{obj: document.Object{Body: body, Size: 3}}

	if _, err := newDocumentOpener(s.open)(context.Background(), "doc-7"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.calls != 1 {
		t.Fatalf("the document service was opened %d time(s), want 1", s.calls)
	}
	if s.rng != "" {
		t.Errorf("rangeHeader = %q, want the empty string", s.rng)
	}
	if s.id != "doc-7" {
		t.Errorf("document id = %q, want %q", s.id, "doc-7")
	}
}

// TestNewDocumentOpener_CarriesDeclaredContentType: nil means unknown, not a missing field.
func TestNewDocumentOpener_CarriesDeclaredContentType(t *testing.T) {
	pdf := "application/pdf"
	for _, tc := range []struct {
		name     string
		declared *string
		want     string
	}{
		{"nil becomes empty", nil, ""},
		{"set is carried verbatim", &pdf, pdf},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &wtCloser{Reader: bytes.NewReader([]byte("abc"))}
			s := &wtScript{
				doc: document.Document{DeclaredContentType: tc.declared},
				obj: document.Object{Body: body, Size: 3},
			}
			doc, err := newDocumentOpener(s.open)(context.Background(), "doc")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if doc.ContentType != tc.want {
				t.Errorf("ContentType = %q, want %q", doc.ContentType, tc.want)
			}
			if string(doc.Bytes) != "abc" {
				t.Errorf("Bytes = %q, want %q", doc.Bytes, "abc")
			}
		})
	}
}

// TestNewDocumentOpener_ForwardsContextVerbatim: the worker has already put the job tenant on
// the ctx, and document.Service.Open resolves its tenant from that identity. A closure that
// wrapped ctx in a second identity would read another tenant rows, silently.
func TestNewDocumentOpener_ForwardsContextVerbatim(t *testing.T) {
	type ctxKey struct{}
	body := &wtCloser{Reader: bytes.NewReader([]byte("abc"))}
	s := &wtScript{obj: document.Object{Body: body, Size: 3}}

	ctx := context.WithValue(context.Background(), ctxKey{}, "tenant-marker")
	if _, err := newDocumentOpener(s.open)(ctx, "doc"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.calls != 1 {
		t.Fatalf("the document service was opened %d time(s), want 1", s.calls)
	}
	if s.ctx == nil {
		t.Fatal("the document service got a nil context")
	}
	if got := s.ctx.Value(ctxKey{}); got != "tenant-marker" {
		t.Errorf("the caller context value did not survive: got %v", got)
	}
	if s.ctx != ctx {
		t.Errorf("the context was wrapped before it reached the document service (%T); the job tenant identity is on the caller context, and wrapping it overwrites the tenant RLS scopes the row lookup by", s.ctx)
	}
}

// wtMainBody returns main()'s top-level statements. Fatal on no main(), so a rename fails
// loudly rather than emptying every scan below.
func wtMainBody(t *testing.T, f *ast.File) []ast.Stmt {
	t.Helper()
	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "main" {
			return fd.Body.List
		}
	}
	t.Fatal("cmd/submission/main.go declares no main(); every assertion below would be vacuous")
	return nil
}

// wtOneCall requires exactly one top-level `x := want(...)` in main(), returning x and the
// call's arguments. Two call sites make "the value main built" ambiguous.
func wtOneCall(t *testing.T, f *ast.File, want string) (string, []ast.Expr) {
	t.Helper()
	var lhs string
	var args []ast.Expr
	calls := 0
	for _, stmt := range wtMainBody(t, f) {
		as, ok := stmt.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 || len(as.Lhs) == 0 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || wtCallName(call.Fun) != want {
			continue
		}
		calls++
		args = call.Args
		if id, ok := as.Lhs[0].(*ast.Ident); ok {
			lhs = id.Name
		}
	}
	if calls != 1 {
		t.Fatalf("cmd/submission/main.go assigns from %s at the top level of main() %d time(s), want exactly 1", want, calls)
	}
	if lhs == "" {
		t.Fatalf("the %s call assigns to no identifier, so nothing below can follow the value it built", want)
	}
	return lhs, args
}

// wtRender names an expression for a failure message.
func wtRender(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.CallExpr:
		return wtCallName(x.Fun) + "(...)"
	case *ast.SelectorExpr:
		return wtCallName(x)
	}
	return fmt.Sprintf("%T", e)
}

// wtFatalAfter reports whether main() assigns from a call named want as a TOP-LEVEL statement
// (so it is unconditional) and whether the statement right after it is an `if err != nil` that
// terminates the process. calls counts the call sites found, so a zero can be told from a miss.
func wtFatalAfter(t *testing.T, f *ast.File, want string) (calls int, fatal bool) {
	t.Helper()
	body := wtMainBody(t, f)
	for i, stmt := range body {
		as, ok := stmt.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || wtCallName(call.Fun) != want {
			continue
		}
		calls++
		if i+1 >= len(body) {
			continue
		}
		ifs, ok := body[i+1].(*ast.IfStmt)
		if !ok {
			continue
		}
		ast.Inspect(ifs, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if name := wtCallName(c.Fun); name == "log.Fatalf" || name == "log.Fatal" {
					fatal = true
				}
			}
			return true
		})
	}
	return calls, fatal
}

// TestSubmissionMain_FatalOnDocumentConfigError: AC #8. Both object-store calls sit at the top
// level of main(), so the refusal is unconditional the way PORT and MockConfigFromEnv already
// are -- gating it on whether extraction ever runs defers the failure to the worst moment.
//
// NOTE for the operator: the submission service carries no DOCUMENT_* variables today, so this
// wiring crash-loops it until they are set. tools/prenv/dsn.go now states that requirement.
func TestSubmissionMain_FatalOnDocumentConfigError(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/submission/main.go: %v", err)
	}

	// Control: the shipped adapter-config site, same matcher, same file. Its fatal is known
	// good, so a false here is the matcher being broken rather than a real finding.
	if calls, fatal := wtFatalAfter(t, f, "submission.MockConfigFromEnv"); calls != 1 || !fatal {
		t.Fatalf("the matcher found %d unconditional submission.MockConfigFromEnv call(s), fatal=%v, want 1 and true -- it can no longer recognise a known-good boot fatal, so the findings below mean nothing", calls, fatal)
	}

	for _, want := range []string{"document.ConfigFromEnv", "document.NewS3Store"} {
		calls, fatal := wtFatalAfter(t, f, want)
		if calls == 0 {
			t.Errorf("main() has no unconditional %s call: the object store must be built at boot, not lazily on the first extraction job", want)
			continue
		}
		if calls != 1 {
			t.Errorf("main() calls %s %d times, want 1", want, calls)
		}
		if !fatal {
			t.Errorf("the statement after %s is not an error check that calls log.Fatal: a malformed object-store configuration must refuse to boot, exactly as PORT and MockConfigFromEnv do", want)
		}
	}
}

// psObjects is newPageSink's only collaborator: a fake document.ObjectStore that records the
// one Put and, crucially, the reader's position when it arrived.
type psObjects struct {
	calls  int
	key    string
	size   int64
	offset int64
	body   []byte
	err    error
}

var _ document.ObjectStore = (*psObjects)(nil)

func (o *psObjects) Put(_ context.Context, key string, body io.ReadSeeker, size int64) error {
	o.calls++
	o.key, o.size = key, size

	pos, err := body.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	o.offset = pos

	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	o.body = b
	return o.err
}

func (o *psObjects) Get(context.Context, string, string) (document.Object, error) {
	return document.Object{}, errors.New("newPageSink never reads an object")
}

// TestNewPageSink_PutsToTheDocumentStore: the offset assertion is the load-bearing one.
// document.Service.Store rewinds before its own Put for the reason recorded at
// internal/document/service.go:45 -- Put transmits from the reader's CURRENT offset, so an
// already-read reader sends zero bytes under a declared length and the object lands empty.
func TestNewPageSink_PutsToTheDocumentStore(t *testing.T) {
	objects := &psObjects{}
	sink := newPageSink(objects)

	const key = "tenants/3f2a1c88-0b6d-4e19-9f31-5c7a2d840e11/pages/" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef/v1/p0001.png"
	body := []byte("\x89PNG\r\n\x1a\nextr-09 page one bytes")

	if err := sink(context.Background(), key, body); err != nil {
		t.Fatalf("newPageSink: %v", err)
	}
	if objects.calls != 1 {
		t.Fatalf("the object store saw %d Put(s), want 1; every assertion below would read a zero value", objects.calls)
	}
	if objects.key != key {
		t.Errorf("Put was given key %q, want %q", objects.key, key)
	}
	if objects.size != int64(len(body)) {
		t.Errorf("Put was given size %d, want %d -- the declared length is what the store transmits", objects.size, len(body))
	}
	if objects.offset != 0 {
		t.Errorf("Put was handed a reader at offset %d, want 0: Put transmits from the reader's current offset, so a pre-read reader sends zero bytes under a declared length", objects.offset)
	}
	if !bytes.Equal(objects.body, body) {
		t.Errorf("Put read back %d byte(s), want the %d handed to the sink", len(objects.body), len(body))
	}

	objects.err = errors.New("the object store refused the PUT")
	if err := sink(context.Background(), key, body); !errors.Is(err, objects.err) {
		t.Errorf("newPageSink returned %v over a failing store, want its error: a swallowed PUT failure would commit a page row naming nothing", err)
	}
}

// TestPageKey_MatchesTheDocumentStorageKeyPrefix: extraction_page_images_key_tenant_scoped
// checks starts_with(storage_key, 'tenants/' || tenant_id::text || '/'), the same prefix
// document.StorageKey already builds. This test lives here rather than in internal/extraction
// because deps_test.go's scan B covers test imports too, so no extraction test may reach
// internal/document.
// ---------------------------------------------------------------------------
// EXTR-03-07: the EXTRACTOR selector. selectExtractor is main()'s own testable
// function, the same shape newExtractWorker/newPageSink already use -- reading
// the two env vars stays in main(), so no test here needs t.Setenv on a global.
// Mode A (test-spec) RED: main.go's selectExtractor is still the not-implemented
// stub, so every test below fails on its own assertion, not a compile error.
// ---------------------------------------------------------------------------

// TestSelectExtractor_MockIsTheDefault: T-07-1 and T-07-2. EXTRACTOR unset or "mock" must be
// byte-identical to the fleet's behaviour before this story: extraction.NewMockExtractor().
func TestSelectExtractor_MockIsTheDefault(t *testing.T) {
	for _, name := range []string{"", "mock"} {
		t.Run("EXTRACTOR="+name, func(t *testing.T) {
			got, err := selectExtractor(name, "")
			if err != nil {
				t.Fatalf("selectExtractor(%q, \"\") returned error %v, want a *MockExtractor and no error", name, err)
			}
			if _, ok := got.(*extraction.MockExtractor); !ok {
				t.Errorf("selectExtractor(%q, \"\") = %T, want *extraction.MockExtractor", name, got)
			}
		})
	}
}

// TestSelectExtractor_DoclingWithValidURL: T-07-3.
func TestSelectExtractor_DoclingWithValidURL(t *testing.T) {
	const doclingURL = "http://docling.railway.internal:8080"
	got, err := selectExtractor("docling", doclingURL)
	if err != nil {
		t.Fatalf("selectExtractor(\"docling\", %q) returned error %v, want a *DoclingExtractor and no error", doclingURL, err)
	}
	ext, ok := got.(*extraction.DoclingExtractor)
	if !ok {
		t.Fatalf("selectExtractor(\"docling\", %q) = %T, want *extraction.DoclingExtractor", doclingURL, got)
	}
	if ext.Name() != "docling" {
		t.Errorf("Name() = %q, want %q", ext.Name(), "docling")
	}
}

// TestSelectExtractor_DoclingRequiresURL: T-07-4. An empty DOCLING_URL is fatal at boot in
// every environment, matching submission.Select's M5-04-08 posture -- the error must name the
// variable an operator needs to set, not the empty string it tried to parse.
func TestSelectExtractor_DoclingRequiresURL(t *testing.T) {
	got, err := selectExtractor("docling", "")
	if err == nil {
		t.Fatal("selectExtractor(\"docling\", \"\") returned a nil error, want one naming DOCLING_URL")
	}
	if got != nil {
		t.Errorf("selectExtractor(\"docling\", \"\") = %T on the error path, want nil", got)
	}
	if !strings.Contains(err.Error(), "DOCLING_URL") {
		t.Errorf("err = %q, want it to name DOCLING_URL", err.Error())
	}
	// "Unset" and "malformed" are separate cases in the AC and read differently to whoever is
	// looking at a boot log. Without this, deleting the empty check passes: NewDoclingExtractor
	// rejects "" on its own and the wrapper still says DOCLING_URL, so the operator gets a
	// complaint about a URL scheme instead of "you have not set this".
	if _, bad := selectExtractor("docling", "://nope"); bad == nil || err.Error() == bad.Error() {
		t.Errorf("the empty-DOCLING_URL error %q does not read differently from the malformed one", err.Error())
	}
	if !strings.Contains(err.Error(), "requires") {
		t.Errorf("err = %q, want it to say DOCLING_URL is required rather than report a parse failure", err.Error())
	}
}

// TestSelectExtractor_DoclingRejectsMalformedURL: T-07-5. A malformed DOCLING_URL must fail at
// selection, not produce a *DoclingExtractor pointed at a broken address.
func TestSelectExtractor_DoclingRejectsMalformedURL(t *testing.T) {
	const bad = "://nope"
	got, err := selectExtractor("docling", bad)
	if err == nil {
		t.Fatalf("selectExtractor(\"docling\", %q) returned a nil error, want one rejecting the URL", bad)
	}
	if got != nil {
		t.Errorf("selectExtractor(\"docling\", %q) = %T on the error path, want nil -- not a client pointed at a broken URL", bad, got)
	}
	if !strings.Contains(err.Error(), bad) {
		t.Errorf("err = %q, want it to name the malformed URL %q", err.Error(), bad)
	}
}

// TestSelectExtractor_UnrecognisedIsFatal: T-07-6. An unrecognised EXTRACTOR value must never
// fall back to mock -- it is fatal, naming the value.
func TestSelectExtractor_UnrecognisedIsFatal(t *testing.T) {
	got, err := selectExtractor("typo", "")
	if err == nil {
		t.Fatal(`selectExtractor("typo", "") returned a nil error, want one naming the unrecognised value`)
	}
	if got != nil {
		t.Errorf(`selectExtractor("typo", "") = %T, want nil -- an unrecognised value must never silently select mock`, got)
	}
	if !strings.Contains(err.Error(), "typo") {
		t.Errorf("err = %q, want it to name the unrecognised value %q", err.Error(), "typo")
	}
}

// T-07-7 (PageStore.Reader stays a *PDFiumReader) has no test of its own here: the assertion
// already exists in TestSubmissionMain_WiresTheQueueSeams above, which reads the literal's
// Reader field off the AST. A source-substring scan beside it would be a weaker duplicate --
// it breaks on a reformat and passes on a comment quoting the string. EXTR-17-03 AC-10 rests on
// that same assertion: Pages stays PDFium while Text becomes Docling.

// ---------------------------------------------------------------------------
// EXTR-17-03: the text-reader selector. selectTextReader mirrors selectExtractor's switch, so
// one EXTRACTOR value selects both seams. Locals are passed directly, the TestSelectExtractor_*
// idiom -- the two env reads stay in main() and no test here needs t.Setenv.
// Mode A (test-spec) RED: main.go's selectTextReader is the not-implemented stub, so each test
// below fails on its own assertion rather than on a compile error.
// ---------------------------------------------------------------------------

// TestSelectTextReader_MockAndUnsetSelectNoReader: AC-1. EXTRACTOR unset or "mock" selects NO
// reader, so the deployed mock path stays byte-identical to today -- a nil Text is what keeps
// Work on the Extractor branch.
//
// The oracle is `got != nil` on the extraction.PageReader INTERFACE, deliberately NOT
// reflect.ValueOf(got).IsNil(). A typed nil -- a nil *DoclingReader boxed in the interface --
// reports IsNil() true and would pass this test, while Work's own `w.Text != nil` check reads
// it as non-nil, takes the text branch, and panics dereferencing the nil receiver. That typed
// nil is the precise bug this subtask owns, and only the interface comparison can see it.
func TestSelectTextReader_MockAndUnsetSelectNoReader(t *testing.T) {
	for _, name := range []string{"", "mock"} {
		t.Run("EXTRACTOR="+name, func(t *testing.T) {
			got, err := selectTextReader(name, "")
			if err != nil {
				t.Fatalf("selectTextReader(%q, \"\") returned error %v, want a nil reader and no error", name, err)
			}
			if got != nil {
				t.Errorf("selectTextReader(%q, \"\") = %T, want an untyped nil PageReader: anything else -- including a nil *DoclingReader -- moves Work off the Extractor branch and panics on the first job", name, got)
			}
		})
	}
}

// TestSelectTextReader_DoclingSelectsTheSidecarReader: AC-3. EXTRACTOR=docling with a usable
// DOCLING_URL selects the sidecar reader itself, not the DoclingExtractor that wraps one.
func TestSelectTextReader_DoclingSelectsTheSidecarReader(t *testing.T) {
	const doclingURL = "http://docling.railway.internal:8080"
	got, err := selectTextReader("docling", doclingURL)
	if err != nil {
		t.Fatalf("selectTextReader(\"docling\", %q) returned error %v, want a *DoclingReader and no error", doclingURL, err)
	}
	r, ok := got.(*extraction.DoclingReader)
	if !ok {
		t.Fatalf("selectTextReader(\"docling\", %q) = %T, want *extraction.DoclingReader", doclingURL, got)
	}
	if r.Name() != "docling" {
		t.Errorf("Name() = %q, want %q -- the name lands in extraction_jobs.extractor", r.Name(), "docling")
	}
}

// TestSelectTextReader_DoclingRequiresAUsableURL: AC-5. Empty and malformed are both fatal at
// boot and both name the variable an operator must set. They must also read differently: with
// one message, deleting the empty check still passes -- NewDoclingReader rejects "" on its own
// and the wrapper still says DOCLING_URL, so the operator gets a URL-scheme complaint instead
// of "you have not set this". Same clause as TestSelectExtractor_DoclingRequiresURL, asserted
// here against selectTextReader directly: main() fatals on selectExtractor first, so this
// selector's message can never be read off a boot log.
func TestSelectTextReader_DoclingRequiresAUsableURL(t *testing.T) {
	const bad = "://nope"
	empty, emptyErr := selectTextReader("docling", "")
	if emptyErr == nil {
		t.Fatal("selectTextReader(\"docling\", \"\") returned a nil error, want one naming DOCLING_URL")
	}
	if empty != nil {
		t.Errorf("selectTextReader(\"docling\", \"\") = %T on the error path, want nil", empty)
	}
	if !strings.Contains(emptyErr.Error(), "DOCLING_URL") {
		t.Errorf("err = %q, want it to name DOCLING_URL", emptyErr.Error())
	}
	if !strings.Contains(emptyErr.Error(), "requires") {
		t.Errorf("err = %q, want it to say DOCLING_URL is required rather than report a parse failure", emptyErr.Error())
	}

	malformed, badErr := selectTextReader("docling", bad)
	if badErr == nil {
		t.Fatalf("selectTextReader(\"docling\", %q) returned a nil error, want one rejecting the URL", bad)
	}
	if malformed != nil {
		t.Errorf("selectTextReader(\"docling\", %q) = %T on the error path, want nil -- not a reader pointed at a broken address", bad, malformed)
	}
	if !strings.Contains(badErr.Error(), "DOCLING_URL") {
		t.Errorf("err = %q, want it to name DOCLING_URL", badErr.Error())
	}
	if !strings.Contains(badErr.Error(), bad) {
		t.Errorf("err = %q, want it to name the malformed URL %q", badErr.Error(), bad)
	}
	if emptyErr.Error() == badErr.Error() {
		t.Errorf("the empty-DOCLING_URL error %q does not read differently from the malformed one", emptyErr.Error())
	}
}

// TestSelectors_AcceptTheSameValueSet: AC-6, behavioural half. The table carries the URL
// dimension -- without it the docling arm is never exercised at all -- plus a capitalised
// near-miss both selectors must reject. wantErr is asserted beside the agreement check so two
// selectors that rejected everything could not pass merely by agreeing.
func TestSelectors_AcceptTheSameValueSet(t *testing.T) {
	const valid = "http://docling.railway.internal:8080"
	for _, tc := range []struct {
		name, extractor, url string
		wantErr              bool
	}{
		{"unset", "", "", false},
		{"mock", "mock", "", false},
		{"docling with a usable URL", "docling", valid, false},
		{"docling with no URL", "docling", "", true},
		{"docling with a malformed URL", "docling", "://nope", true},
		{"unrecognised", "nonsense", "", true},
		{"capitalised near-miss", "Docling", valid, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, extErr := selectExtractor(tc.extractor, tc.url)
			_, textErr := selectTextReader(tc.extractor, tc.url)
			if (extErr != nil) != tc.wantErr {
				t.Errorf("selectExtractor(%q, %q) error = %v, want an error: %v", tc.extractor, tc.url, extErr, tc.wantErr)
			}
			if (textErr != nil) != tc.wantErr {
				t.Errorf("selectTextReader(%q, %q) error = %v, want an error: %v", tc.extractor, tc.url, textErr, tc.wantErr)
			}
			if (extErr != nil) != (textErr != nil) {
				t.Errorf("the selectors disagree on (%q, %q): selectExtractor err = %v, selectTextReader err = %v -- one env var must select both seams, or a fleet boots with an Extractor and no reader", tc.extractor, tc.url, extErr, textErr)
			}
		})
	}
}

// TestSelectors_ShareOneCaseSet: AC-6, the half that makes "cannot disagree" true rather than
// "do not disagree on seven rows". The table above survives a fifth arm added to one switch
// only -- a `case "pdfium":` in selectExtractor passes it untouched. Comparing the two case
// SETS off the AST does not.
func TestSelectors_ShareOneCaseSet(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/submission/main.go: %v", err)
	}
	sets := map[string]map[string]bool{}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ast.Inspect(fd, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			if id, ok := sw.Tag.(*ast.Ident); !ok || id.Name != "extractorName" {
				return true
			}
			set := map[string]bool{}
			for _, stmt := range sw.Body.List {
				cc, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				if cc.List == nil {
					set["<default>"] = true
					continue
				}
				for _, e := range cc.List {
					lit, ok := e.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Errorf("%s switches on extractorName with the non-literal case %s: the set comparison below cannot see what it accepts", fd.Name.Name, wtRender(e))
						continue
					}
					v, uerr := strconv.Unquote(lit.Value)
					if uerr != nil {
						t.Errorf("%s case %s does not unquote: %v", fd.Name.Name, lit.Value, uerr)
						continue
					}
					set[v] = true
				}
			}
			sets[fd.Name.Name] = set
			return true
		})
	}

	names := make([]string, 0, len(sets))
	for name := range sets {
		names = append(names, name)
	}
	sort.Strings(names)
	ext, text := sets["selectExtractor"], sets["selectTextReader"]
	if len(sets) != 2 || ext == nil || text == nil {
		t.Fatalf("cmd/submission/main.go switches on extractorName in %v, want exactly [selectExtractor selectTextReader] -- with fewer, the set comparison below is vacuous", names)
	}
	if len(ext) < 4 {
		t.Fatalf("selectExtractor's case set is %v, want at least 4 entries (\"\", mock, docling, <default>) -- a set this small means the scan read almost nothing", ext)
	}
	if !reflect.DeepEqual(ext, text) {
		t.Errorf("selectExtractor accepts %v and selectTextReader accepts %v: any value one accepts the other must accept, or an EXTRACTOR that builds an Extractor leaves Text unselected", ext, text)
	}
}

func TestPageKey_MatchesTheDocumentStorageKeyPrefix(t *testing.T) {
	const (
		tenant = "3f2a1c88-0b6d-4e19-9f31-5c7a2d840e11"
		hash   = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	)
	prefix := "tenants/" + tenant + "/"

	docKey := document.StorageKey(tenant, hash)
	pageKey := extraction.PageKey(tenant, hash, 1)

	if !strings.HasPrefix(docKey, prefix) {
		t.Fatalf("document.StorageKey = %q, which does not start with %q; the comparison below has no baseline", docKey, prefix)
	}
	if !strings.HasPrefix(pageKey, prefix) {
		t.Errorf("extraction.PageKey = %q, which does not start with %q -- the storage_key CHECK refuses it", pageKey, prefix)
	}
	if pageKey == docKey || strings.HasPrefix(pageKey, docKey) {
		t.Errorf("extraction.PageKey = %q sits at or under document.StorageKey's %q; a rendered page would overwrite the source document it came from", pageKey, docKey)
	}
}

// TestSubmissionMain_RegistersTheExtractionsRoute: GET /v1/extractions must be mounted on
// app.Mux dispatching to extraction.JobsHandler(...), GET /v1/extractions/{id} to
// extraction.DetailHandler(...), GET /v1/extractions/{id}/pages/{n} to
// extraction.PageImageHandler(...), and POST /v1/documents on the same mux
// dispatching to extraction.UploadHandler(...). AST, not a byte scan, so
// gofmt cannot break the anchor. The GET /v1/ping needle is a control: it proves the argument
// matcher still finds a real, already-shipped registration before a negative result is trusted.
// The receiver is asserted too -- a route on a locally built mux is registered and unreachable.
func TestSubmissionMain_RegistersTheExtractionsRoute(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/submission/main.go: %v", err)
	}

	var foundPing, pingOnAppMux, foundExtractions, foundDetail, foundPageImage, foundUpload bool
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" || len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		switch strings.Trim(lit.Value, `"`) {
		case "GET /v1/ping":
			foundPing = true
			pingOnAppMux = wtRender(sel.X) == "app.Mux"
		case "GET /v1/extractions":
			foundExtractions = true
			if got := wtRender(sel.X); got != "app.Mux" {
				t.Errorf(`GET /v1/extractions is registered on %s, want app.Mux -- only app.Mux is served, so any other mux answers nothing at /api/submission/v1/extractions`, got)
			}
			handlerCall, ok := call.Args[1].(*ast.CallExpr)
			if !ok {
				t.Errorf(`GET /v1/extractions' second argument is %T, want a call expression`, call.Args[1])
				return true
			}
			hsel, ok := handlerCall.Fun.(*ast.SelectorExpr)
			if !ok || hsel.Sel.Name != "JobsHandler" {
				t.Errorf(`GET /v1/extractions' handler call is not ....JobsHandler(...), got %s`, wtRender(handlerCall.Fun))
				return true
			}
			if pkg, ok := hsel.X.(*ast.Ident); !ok || pkg.Name != "extraction" {
				t.Errorf(`GET /v1/extractions' handler is not extraction.JobsHandler(...), got %s`, wtRender(handlerCall.Fun))
			}
		case "GET /v1/extractions/{id}":
			foundDetail = true
			if got := wtRender(sel.X); got != "app.Mux" {
				t.Errorf(`GET /v1/extractions/{id} is registered on %s, want app.Mux -- only app.Mux is served, so any other mux answers nothing at /api/submission/v1/extractions/{id}`, got)
			}
			handlerCall, ok := call.Args[1].(*ast.CallExpr)
			if !ok {
				t.Errorf(`GET /v1/extractions/{id}' second argument is %T, want a call expression`, call.Args[1])
				return true
			}
			hsel, ok := handlerCall.Fun.(*ast.SelectorExpr)
			if !ok || hsel.Sel.Name != "DetailHandler" {
				t.Errorf(`GET /v1/extractions/{id}' handler call is not ....DetailHandler(...), got %s`, wtRender(handlerCall.Fun))
				return true
			}
			if pkg, ok := hsel.X.(*ast.Ident); !ok || pkg.Name != "extraction" {
				t.Errorf(`GET /v1/extractions/{id}' handler is not extraction.DetailHandler(...), got %s`, wtRender(handlerCall.Fun))
			}
		case "GET /v1/extractions/{id}/pages/{n}":
			foundPageImage = true
			if got := wtRender(sel.X); got != "app.Mux" {
				t.Errorf(`GET /v1/extractions/{id}/pages/{n} is registered on %s, want app.Mux -- only app.Mux is served, so any other mux answers nothing at /api/submission/v1/extractions/{id}/pages/{n}`, got)
			}
			handlerCall, ok := call.Args[1].(*ast.CallExpr)
			if !ok {
				t.Errorf(`GET /v1/extractions/{id}/pages/{n}' second argument is %T, want a call expression`, call.Args[1])
				return true
			}
			hsel, ok := handlerCall.Fun.(*ast.SelectorExpr)
			if !ok || hsel.Sel.Name != "PageImageHandler" {
				t.Errorf(`GET /v1/extractions/{id}/pages/{n}' handler call is not ....PageImageHandler(...), got %s`, wtRender(handlerCall.Fun))
				return true
			}
			if pkg, ok := hsel.X.(*ast.Ident); !ok || pkg.Name != "extraction" {
				t.Errorf(`GET /v1/extractions/{id}/pages/{n}' handler is not extraction.PageImageHandler(...), got %s`, wtRender(handlerCall.Fun))
			}
			// The object seam is unit-tested over a fake store (page_image_route_test.go), so
			// only this scan can say the handler is built over the REAL bucket. An adapter over
			// anything else streams from nowhere the renderer wrote.
			if len(handlerCall.Args) != 3 {
				t.Errorf("extraction.PageImageHandler is called with %d argument(s), want 3 (key, object, logger)", len(handlerCall.Args))
				return true
			}
			if call, ok := handlerCall.Args[1].(*ast.CallExpr); !ok || wtCallName(call.Fun) != "newPageObjectReader" {
				t.Errorf("PageImageHandler's object argument is %s, want a newPageObjectReader(...) call", wtRender(handlerCall.Args[1]))
			}
		case "POST /v1/documents":
			foundUpload = true
			if got := wtRender(sel.X); got != "app.Mux" {
				t.Errorf(`POST /v1/documents is registered on %s, want app.Mux -- only app.Mux is served, so any other mux answers nothing at /api/submission/v1/documents`, got)
			}
			handlerCall, ok := call.Args[1].(*ast.CallExpr)
			if !ok {
				t.Errorf(`POST /v1/documents' second argument is %T, want a call expression`, call.Args[1])
				return true
			}
			hsel, ok := handlerCall.Fun.(*ast.SelectorExpr)
			if !ok || hsel.Sel.Name != "UploadHandler" {
				t.Errorf(`POST /v1/documents' handler call is not ....UploadHandler(...), got %s`, wtRender(handlerCall.Fun))
				return true
			}
			if pkg, ok := hsel.X.(*ast.Ident); !ok || pkg.Name != "extraction" {
				t.Errorf(`POST /v1/documents' handler is not extraction.UploadHandler(...), got %s`, wtRender(handlerCall.Fun))
			}
			// The two adapters are unit-tested (upload_storer_test.go) over injected seams, so
			// only this scan can say they are built over the REAL service and pool. A storer
			// over anything but the document service stores nowhere the reader can find.
			if len(handlerCall.Args) != 3 {
				t.Errorf("extraction.UploadHandler is called with %d argument(s), want 3 (store, enqueue, logger)", len(handlerCall.Args))
				return true
			}
			if call, ok := handlerCall.Args[0].(*ast.CallExpr); !ok || wtCallName(call.Fun) != "newDocumentStorer" {
				t.Errorf("UploadHandler's store argument is %s, want a newDocumentStorer(...) call", wtRender(handlerCall.Args[0]))
			} else if len(call.Args) != 1 {
				t.Errorf("newDocumentStorer is called with %d argument(s), want 1", len(call.Args))
			} else if sel, ok := call.Args[0].(*ast.SelectorExpr); !ok || sel.Sel.Name != "Store" {
				t.Errorf("newDocumentStorer is built over %s, want the document service's .Store: the reuse flag and the sanitized filename both come off that method", wtRender(call.Args[0]))
			}
			if call, ok := handlerCall.Args[1].(*ast.CallExpr); !ok || wtCallName(call.Fun) != "newExtractionEnqueuer" {
				t.Errorf("UploadHandler's enqueue argument is %s, want a newExtractionEnqueuer(...) call: only that closure puts the business key and the job insert in one transaction", wtRender(handlerCall.Args[1]))
			}
		}
		return true
	})

	if !foundPing {
		t.Fatal("control needle: no GET /v1/ping registration found -- the AST walk itself is broken, so the assertion below is vacuous")
	}
	if !pingOnAppMux {
		t.Fatal("control needle: the GET /v1/ping receiver does not render as app.Mux -- the receiver check above cannot fail, so it proves nothing")
	}
	if !foundExtractions {
		t.Error(`no app.Mux.HandleFunc("GET /v1/extractions", extraction.JobsHandler(...)) registration found in cmd/submission/main.go`)
	}
	if !foundDetail {
		t.Error(`no app.Mux.HandleFunc("GET /v1/extractions/{id}", extraction.DetailHandler(...)) registration found in cmd/submission/main.go`)
	}
	if !foundPageImage {
		t.Error(`no app.Mux.HandleFunc("GET /v1/extractions/{id}/pages/{n}", extraction.PageImageHandler(...)) registration found in cmd/submission/main.go`)
	}
	if !foundUpload {
		t.Error(`no app.Mux.HandleFunc("POST /v1/documents", extraction.UploadHandler(...)) registration found in cmd/submission/main.go`)
	}
}
