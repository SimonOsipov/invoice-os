// main_test.go: M5-02-04 RED spec (Mode A) for the submission.Select boot-refusal wiring.
// cmd/submission/ had no test files before this one (main() itself isn't unit-testable --
// it calls log.Fatalf and connects a real DB pool). Mirrors cmd/gateway/main_test.go's
// source-scan idiom exactly: os.ReadFile a relative sibling path, strings.Index an anchor,
// t.Fatal by name if the anchor isn't found (so a future rename can't make this test
// silently vacuous), then assert inside a fixed window following the anchor.
//
// This subtask does NOT wire submission.Select into main.go -- that is the executor's job.
// This test is therefore RED against the stub tree: the "submission.Select(" anchor is not
// yet present in main.go, so it fails via the named t.Fatal below, not the log.Fatal
// assertion further down. That is the expected and correct RED for this stage.
package main

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

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

	ew := newExtractWorker(pool, ext, open, logger)
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
	if checked < 4 {
		t.Fatalf("only %d nillable collaborator field(s) inspected on ExtractWorker, want at least 4 (Pool, Extractor, Open, Logger) -- the loop above examined almost nothing", checked)
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
	if ew.Open == nil {
		t.Fatal("ExtractWorker.Open is nil")
	}
	doc, err := ew.Open(context.Background(), "doc")
	if err != nil || doc.ContentType != sentinel {
		t.Errorf("ExtractWorker.Open returned (%+v, %v), want the sentinel opener passed in", doc, err)
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
	var addWorkerHits, extractWorkerCalls int
	var extractWorkerArgs []ast.Expr
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
		case *ast.CallExpr:
			if wtCallName(x.Fun) == "newExtractWorker" {
				extractWorkerCalls++
				extractWorkerArgs = x.Args
			}
		}
		return true
	})

	if len(configs) != 1 {
		t.Fatalf("cmd/submission/main.go builds %d queue.Config literal(s), want exactly 1 -- with none, every assertion below is vacuous; with two, the one queue.New reads is ambiguous", len(configs))
	}
	seams := map[string]string{"Queues": "queueConfigs", "Workers": "workerBundle"}
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

	if extractWorkerCalls != 1 {
		t.Fatalf("cmd/submission/main.go calls newExtractWorker %d time(s), want exactly 1", extractWorkerCalls)
	}
	if len(extractWorkerArgs) != 4 {
		t.Fatalf("newExtractWorker is called with %d argument(s), want 4 (pool, extractor, opener, logger)", len(extractWorkerArgs))
	}
	for i, arg := range extractWorkerArgs {
		if id, ok := arg.(*ast.Ident); ok && id.Name == "nil" {
			t.Errorf("newExtractWorker argument %d is nil: it compiles, registers, and panics on the first job", i)
		}
	}
}

// wtMainPackageImporters walks every non-test .go file in the module and returns, per import
// path of interest, the package-main files that import it. Also returns the parsed population
// so a caller can floor it.
func wtMainPackageImporters(t *testing.T, root string, paths []string) (map[string][]string, []string) {
	t.Helper()
	want := map[string]bool{}
	for _, p := range paths {
		want[p] = true
	}
	hits := map[string][]string{}
	var parsed []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch filepath.Base(path) {
			case ".git", ".claude", "node_modules", "frontend", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		parsed = append(parsed, rel)
		if f.Name.Name != "main" {
			return nil
		}
		for _, imp := range f.Imports {
			p, uerr := strconv.Unquote(imp.Path.Value)
			if uerr == nil && want[p] {
				hits[p] = append(hits[p], rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	for p := range hits {
		sort.Strings(hits[p])
	}
	return hits, parsed
}

// TestSubmissionMain_AddsNoNewBinary: Core AC-8. The directory check alone is evaded by any
// name other than "extraction", so the second half asserts that the submission binary is the
// only one that reaches internal/extraction at all.
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
		extractionPkg = "github.com/SimonOsipov/invoice-os/internal/extraction"
		submissionPkg = "github.com/SimonOsipov/invoice-os/internal/submission"
	)
	hits, parsed := wtMainPackageImporters(t, wtRepoRoot(t), []string{extractionPkg, submissionPkg})

	cmdFiles := 0
	for _, rel := range parsed {
		if strings.HasPrefix(rel, "cmd/") {
			cmdFiles++
		}
	}
	if len(parsed) < 130 || cmdFiles < 8 {
		t.Fatalf("the walk parsed %d non-test .go file(s), %d under cmd/, want at least 130 and at least 8 -- a clean report over a broken walk means nothing", len(parsed), cmdFiles)
	}

	// Control needle, same walk and same matcher: internal/submission has exactly one
	// package-main importer today, so an empty extraction result below cannot be the import
	// matcher having stopped working.
	wantOne := []string{"cmd/submission/main.go"}
	if !reflect.DeepEqual(hits[submissionPkg], wantOne) {
		t.Fatalf("the package-main importers of internal/submission are %v, want %v -- the import matcher is broken, so the extraction assertion below would pass having examined nothing", hits[submissionPkg], wantOne)
	}
	if !reflect.DeepEqual(hits[extractionPkg], wantOne) {
		t.Errorf("the package-main importers of internal/extraction are %v, want %v: extraction must be reachable from the submission binary and from no other", hits[extractionPkg], wantOne)
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

// wtFatalAfter reports whether main() assigns from a call named want as a TOP-LEVEL statement
// (so it is unconditional) and whether the statement right after it is an `if err != nil` that
// terminates the process. calls counts the call sites found, so a zero can be told from a miss.
func wtFatalAfter(t *testing.T, f *ast.File, want string) (calls int, fatal bool) {
	t.Helper()
	var body []ast.Stmt
	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "main" {
			body = fd.Body.List
		}
	}
	if body == nil {
		t.Fatal("cmd/submission/main.go declares no main(); every assertion below would be vacuous")
	}
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
