// wiring_test.go: cmd/worker's wiring specs. main() is not unit-testable -- it calls
// platform.Fatal and connects a real pool -- so the claims are read off main.go's source, the
// cmd/gateway/main_test.go idiom: locate an anchor by name, t.Fatal if it is missing so a
// rename cannot make the scan vacuous, then assert inside a fixed window after it.
package main

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
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

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/notifications"
	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// TestWorkerMain_FatalsOnAdapterSelectError: AC-6 / AC-7 (Core AC-6's binary-level
// half). Static source scan proving the submission.Select( call site's error path
// terminates the process via platform.Fatal, before the next top-level statement.
func TestWorkerMain_FatalsOnAdapterSelectError(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/worker/main.go: %v", err)
	}

	calls, fatal := wtFatalAfter(t, f, "submission.Select")
	if calls != 1 {
		t.Fatalf(`main() assigns from submission.Select %d time(s) at the top level, want 1 -- this test's anchor moved (or the wiring hasn't landed yet)`, calls)
	}
	if !fatal {
		t.Error("the statement after submission.Select is not an `if err != nil` that calls platform.Fatal -- the adapter-selection error path must terminate the process (Core AC-6)")
	}
}

// TestWorkerMain_FatalOnAdapterConfigError: M5-03-05 AC-7. Static source scan proving that
// main() reads the mock's config from the environment BEFORE it builds the registry, and that
// the config error path terminates the process.
//
// DELIBERATELY NOT a copy of the 500-byte window above. With this subtask's wiring
// `submission.Select(` sits roughly 120 bytes after `submission.MockConfigFromEnv(`, so a fixed
// 500-byte window anchored at the config call would SWALLOW the Select error path's own
// platform.Fatal (main.go's existing wiring) and pass with a full 100% green even if the config
// error were ignored entirely. The window here is bounded by the NEXT anchor instead, and the
// ordering between the two anchors is asserted rather than assumed -- which is the real
// requirement anyway: a config read that happened AFTER the registry was built could not have
// configured it.
//
// HOW MUCH THIS PROVES, honestly: a source scan shows that a token appears inside a byte range,
// not that the branch is reachable. The behavioural version needs an `adapterFromEnv` seam
// extracted out of main(), which would relocate the shipped M5-02 Select wiring and break the
// anchor of TestWorkerMain_FatalsOnAdapterSelectError above -- a deliberately-shipped
// assertion this story has no mandate to weaken. Recorded as an M5-04 follow-up; M5-04 consumes
// `adapter` and wants a testable seam regardless. The compensating control is that
// MockConfigFromEnv's three branches ARE genuinely unit-tested
// (internal/submission/mock_adapter_test.go's TestMockConfigFromEnv), which shrinks what this
// scan leaves unproven to "main calls it and fatals" -- two lines, verifiable by eye. It is also
// scan reads comment-stripped source, so a comment quoting either anchor cannot satisfy it.
func TestWorkerMain_FatalOnAdapterConfigError(t *testing.T) {
	src := wtSourceWithoutComments(t, "main.go")

	cfgIdx := strings.Index(src, "submission.MockConfigFromEnv(")
	if cfgIdx == -1 {
		t.Fatal(`cmd/worker/main.go does not contain a "submission.MockConfigFromEnv(" call site -- ` +
			`the mock's latency knob is read nowhere, so APP_ADAPTER_MOCK_LATENCY is inert (or this ` +
			`test's anchor moved)`)
	}
	selIdx := strings.Index(src, "submission.Select(")
	if selIdx == -1 {
		t.Fatal(`cmd/worker/main.go does not contain a "submission.Select(" call site -- this test's ` +
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
	if !strings.Contains(window, "platform.Fatal(") {
		t.Errorf("no platform.Fatal between the submission.MockConfigFromEnv( call site and the "+
			"submission.Select( one -- the adapter-config error path must terminate the process, exactly "+
			"as the Select path does:\n%s", window)
	}
	if !strings.Contains(window, "NewDefaultRegistry(") {
		t.Errorf("no NewDefaultRegistry( between the submission.MockConfigFromEnv( call site and the "+
			"submission.Select( one -- the config that was just read must be what the registry is built "+
			"from:\n%s", window)
	}
}

// TestWorkerMain_NoNonProductionAdapterFallback: M5-04-08 AC-2. The two tests above
// prove a platform.Fatal sits somewhere inside a fixed window after submission.Select( -- true
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
func TestWorkerMain_NoNonProductionAdapterFallback(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read cmd/worker/main.go: %v", err)
	}
	src := string(b)

	if strings.Contains(src, "continuing with no adapter configured") {
		t.Error(`cmd/worker/main.go still contains "continuing with no adapter configured" -- ` +
			"the non-production fallback branch (or a vestige of it) was not fully removed; a failed " +
			"adapter Select must be fatal in EVERY environment")
	}

	idx := strings.Index(src, "submission.Select(")
	if idx == -1 {
		t.Fatal(`cmd/worker/main.go does not contain a "submission.Select(" call site -- this test's anchor moved`)
	}
	end := idx + 500
	if end > len(src) {
		end = len(src)
	}
	window := src[idx:end]

	if strings.Contains(window, "IsProduction(") {
		t.Errorf("found IsProduction( within 500 bytes after the submission.Select( call site -- the "+
			"adapter-selection error path must be an unconditional platform.Fatal, not gated on "+
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

// wtSourceWithoutComments re-prints path's Go source without comments, so a byte-window scan
// reads code only.
func wtSourceWithoutComments(t *testing.T, path string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var b strings.Builder
	if err := printer.Fprint(&b, fset, f); err != nil {
		t.Fatalf("print %s: %v", path, err)
	}
	if !strings.Contains(b.String(), "func main()") {
		t.Fatalf("%s stripped to no func main(); a scan of it would read nothing", path)
	}
	return b.String()
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

// TestWorkerBundle_CarriesEveryKind is the behavioural half of AC #1. river.AddWorkerSafely
// reports "already registered" rather than panicking, and type inference means this package
// never names the unexported args type. A second bundle, or a bundle built after queue.New has
// read the first, fails the positive arm.
func TestWorkerBundle_CarriesEveryKind(t *testing.T) {
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
	contactProbes := []struct {
		kind string
		add  func(*river.Workers) error
	}{
		{"contact_deliver", func(b *river.Workers) error {
			return river.AddWorkerSafely(b, &notifications.DeliverWorker{})
		}},
		{"demo_deal", func(b *river.Workers) error {
			return river.AddWorkerSafely(b, &notifications.DemoDealWorker{})
		}},
		{"blog_subscriber_sync", func(b *river.Workers) error {
			return river.AddWorkerSafely(b, &notifications.BlogSyncWorker{})
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

	cw := &contactWorkerSet{deliver: &notifications.DeliverWorker{}, deal: &notifications.DemoDealWorker{}, blog: &notifications.BlogSyncWorker{}}
	bundle := workerBundle(&submission.SubmitWorker{}, &submission.PollWorker{}, &extraction.ExtractWorker{}, cw)
	if bundle == nil {
		t.Fatal("workerBundle returned nil; a River client takes exactly one bundle")
	}
	for _, p := range append(probes, contactProbes...) {
		err := p.add(bundle)
		if err == nil {
			t.Errorf("workerBundle does not carry %q: a worker missing from the ONE bundle queue.New reads never fetches a job", p.kind)
			continue
		}
		if !strings.Contains(err.Error(), "already registered") || !strings.Contains(err.Error(), strconv.Quote(p.kind)) {
			t.Errorf("probing %q returned %q, want the already-registered error naming that kind", p.kind, err)
		}
	}

	// Contacts off: no contact kind is registered, so its queued jobs wait untouched.
	off := workerBundle(&submission.SubmitWorker{}, &submission.PollWorker{}, &extraction.ExtractWorker{}, nil)
	for _, p := range contactProbes {
		if err := p.add(off); err != nil {
			t.Errorf("workerBundle with contacts off carries %q (%v), want it absent", p.kind, err)
		}
	}
	// The newsletter off: the blog sync worker is absent, the other two are not.
	cw.blog = nil
	noBlog := workerBundle(&submission.SubmitWorker{}, &submission.PollWorker{}, &extraction.ExtractWorker{}, cw)
	for i, p := range contactProbes {
		if err := p.add(noBlog); (err == nil) != (i == 2) {
			t.Errorf("workerBundle with the newsletter off: probing %q returned %v", p.kind, err)
		}
	}
}

// contactWorkers builds nothing in off mode and the blog sync worker only when the newsletter is on.
func TestContactWorkers_FollowTheModes(t *testing.T) {
	if cw := contactWorkers(nil, notifications.ModeOff, nil, nil, notifications.NewsletterWiring{Enabled: true}, slog.Default()); cw != nil {
		t.Errorf("contactWorkers in off mode = %+v, want nil", cw)
	}
	cw := contactWorkers(nil, notifications.ModeFake, notifications.FakeHubSpot{}, notifications.FakeResend{}, notifications.NewsletterWiring{}, slog.Default())
	if cw == nil || cw.deliver == nil || cw.deal == nil || cw.blog != nil {
		t.Fatalf("contactWorkers in fake mode, newsletter off = %+v, want deliver and deal only", cw)
	}
	if cw.deliver.Mode != notifications.ModeFake || cw.deliver.HubSpot == nil || cw.deliver.Resend == nil || cw.deal.HubSpot == nil {
		t.Errorf("deliver %+v, deal %+v: want the mode and both clients set", cw.deliver, cw.deal)
	}
	nl := notifications.NewsletterWiring{Enabled: true, Mode: notifications.ModeFake, Resend: notifications.FakeBlogResend{}}
	cw = contactWorkers(nil, notifications.ModeFake, notifications.FakeHubSpot{}, notifications.FakeResend{}, nl, slog.Default())
	if cw == nil || cw.blog == nil || cw.blog.Mode != notifications.ModeFake || cw.blog.Resend == nil {
		t.Errorf("contactWorkers with the newsletter on = %+v, want a blog sync worker in fake mode", cw)
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
	// own Text is followed back to selectTextReader in TestWorkerMain_WiresTheQueueSeams.
	textFake := extraction.NewPDFiumReader()
	ruled := 0
	rules := func(context.Context, string, string) ([]extraction.AnchorRule, error) {
		ruled++
		return nil, nil
	}

	// Real, non-nil AIReader: fake mode needs no network and Enabled() reports true.
	t.Setenv(ai.EnvFake, "true")
	aiClient, err := ai.FromEnv(nil)
	if err != nil {
		t.Fatalf("ai.FromEnv: %v", err)
	}

	// Real, non-nil JevAsker: fake mode needs no network and Enabled() reports true.
	t.Setenv(jev.EnvFake, "true")
	t.Setenv(jev.EnvKey, "")
	jevClient, err := jev.FromEnv(nil)
	if err != nil {
		t.Fatalf("jev.FromEnv: %v", err)
	}
	if !jevClient.Enabled() {
		t.Fatal("jev.FromEnv under JEV_FAKE=true returned a disabled client")
	}

	// Sentinel, non-nil PageObject: reaching it proves the constructor wired the argument
	// through rather than leaving the field nil.
	const pageBytesSentinel = "sentinel/page-bytes"
	pageBytesCalled := 0
	pageBytes := func(context.Context, string) (io.ReadCloser, int64, error) {
		pageBytesCalled++
		return io.NopCloser(strings.NewReader(pageBytesSentinel)), int64(len(pageBytesSentinel)), nil
	}

	ew := newExtractWorker(pool, ext, open, pages, auditor, textFake, rules, aiClient, jevClient, pageBytes, logger)
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
	if checked < 11 {
		t.Fatalf("only %d nillable collaborator field(s) inspected on ExtractWorker, want at least 11 (Pool, Extractor, Open, Pages, Audit, Text, Rules, AI, Jev, PageBytes, Logger) -- the loop above examined almost nothing", checked)
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
	if ew.AI != extraction.AIReader(aiClient) {
		t.Error("ExtractWorker.AI is not the ai.FromEnv client passed in")
	}
	if ew.Jev != extraction.JevAsker(jevClient) {
		t.Errorf("ExtractWorker.Jev is %v, want the jev.FromEnv client passed in", ew.Jev)
	}
	if ew.PageBytes == nil {
		t.Fatal("ExtractWorker.PageBytes is nil")
	}
	body, size, err := ew.PageBytes(context.Background(), "k")
	if err != nil || pageBytesCalled != 1 {
		t.Fatalf("ExtractWorker.PageBytes ran %d time(s) and returned %v, want the sentinel reader passed in to run exactly once", pageBytesCalled, err)
	}
	got, err := io.ReadAll(body)
	if err != nil || string(got) != pageBytesSentinel || size != int64(len(pageBytesSentinel)) {
		t.Errorf("ExtractWorker.PageBytes returned (%q, %d, %v), want (%q, %d, nil)", got, size, err, pageBytesSentinel, len(pageBytesSentinel))
	}
}

// TestQueueConfigs_NamesEveryQueue: AC #2. Extraction gets its own queue so a slow read cannot
// starve submission; river.NewClient refuses MaxWorkers < 1. Contacts get a queue only when on.
func TestQueueConfigs_NamesEveryQueue(t *testing.T) {
	if _, ok := queueConfigs(false)[notifications.QueueContacts]; ok {
		t.Errorf("queueConfigs(false) names %q; with contacts off nothing may fetch them", notifications.QueueContacts)
	}
	got := queueConfigs(true)
	if len(got) == 0 {
		t.Fatal("queueConfigs returned an empty map; the assertions below would pass vacuously")
	}
	if extraction.QueueName == river.QueueDefault {
		t.Fatalf("extraction.QueueName is %q, the River default: the two-queue assertion below collapses to one", extraction.QueueName)
	}

	want := map[string]int{river.QueueDefault: 10, extraction.QueueName: 2, notifications.QueueContacts: 2}
	names := make([]string, 0, len(got))
	for name := range got {
		names = append(names, name)
	}
	sort.Strings(names)
	wantNames := []string{extraction.QueueName, river.QueueDefault, notifications.QueueContacts}
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

// TestWorkerMain_WiresTheQueueSeams: the seams above are only worth their assertions if
// main() is what calls them. AST, not a byte scan, so reformatting main.go cannot break it.
func TestWorkerMain_WiresTheQueueSeams(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/worker/main.go: %v", err)
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
		t.Fatalf("cmd/worker/main.go builds %d queue.Config literal(s), want exactly 1 -- with none, every assertion below is vacuous; with two, the one queue.New reads is ambiguous", len(configs))
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
		t.Errorf("cmd/worker/main.go selects AddWorker %d time(s); registration belongs in submission.Workers and extraction.AddTo", addWorkerHits)
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
	poolName := wtAppPool(t, f)
	objName, _ := wtOneCall(t, f, "document.NewS3Store")
	svcName, svcArgs := wtOneCall(t, f, "document.NewService")
	ewName, ewArgs := wtOneCall(t, f, "newExtractWorker")

	if len(ewArgs) != 11 {
		t.Fatalf("newExtractWorker is called with %d argument(s), want 11 (pool, extractor, opener, pages, auditor, text, rules, ai, jev, pageBytes, logger)", len(ewArgs))
	}
	// EXTR-17-03 AC-8: the walk covers all 11 arguments. Argument 5's exemption is gone with the
	// literal nil it protected.
	const ewTextArg = 5
	for i, arg := range ewArgs {
		if id, ok := arg.(*ast.Ident); ok && id.Name == "nil" {
			t.Errorf("newExtractWorker argument %d is nil: it compiles, registers, and breaks on the first job -- a nil auditor errors out on Work's own guard, a nil Jev silently turns the value check off, the rest panic", i)
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

	// AIR-03-03: the AI client, followed back to the one ai.FromEnv call main() fatals on.
	aiName, aiArgs := wtOneCall(t, f, "ai.FromEnv")
	if id, ok := ewArgs[7].(*ast.Ident); !ok || id.Name != aiName {
		t.Errorf("newExtractWorker's ai argument is %s, want %s -- the client ai.FromEnv built and main() already fatals on", wtRender(ewArgs[7]), aiName)
	}
	if len(aiArgs) != 1 {
		t.Fatalf("ai.FromEnv is called with %d argument(s), want 1 (the logger)", len(aiArgs))
	}
	if sel, ok := aiArgs[0].(*ast.SelectorExpr); !ok || sel.Sel.Name != "Logger" {
		t.Errorf("ai.FromEnv's argument is %s, want the logger", wtRender(aiArgs[0]))
	}

	// The Jev client, followed back to the one jev.FromEnv call main() fatals on.
	jevName, jevArgs := wtOneCall(t, f, "jev.FromEnv")
	if id, ok := ewArgs[8].(*ast.Ident); !ok || id.Name != jevName {
		t.Errorf("newExtractWorker's jev argument is %s, want %s -- the client jev.FromEnv built and main() already fatals on", wtRender(ewArgs[8]), jevName)
	}
	if len(jevArgs) != 1 {
		t.Fatalf("jev.FromEnv is called with %d argument(s), want 1 (the logger)", len(jevArgs))
	}
	if sel, ok := jevArgs[0].(*ast.SelectorExpr); !ok || sel.Sel.Name != "Logger" {
		t.Errorf("jev.FromEnv's argument is %s, want the logger", wtRender(jevArgs[0]))
	}

	// 1. The worker on the bundle is the one newExtractWorker built. A bare
	//    &extraction.ExtractWorker{} here compiles, registers the kind, and fails job one on
	//    Work's nil-Audit guard before its nil pool can panic.
	if len(bundleArgs) != 4 {
		t.Fatalf("workerBundle is called with %d argument(s), want 4 (sw, pw, ew, cw)", len(bundleArgs))
	}
	if id, ok := bundleArgs[2].(*ast.Ident); !ok || id.Name != ewName {
		t.Errorf("workerBundle's third argument is %s, want %s -- the worker newExtractWorker built. Any other value here registers the kind with nil collaborators and works no job", wtRender(bundleArgs[2]), ewName)
	}

	// 2. That worker reads through an opener built over the document service's own Open.
	if call, ok := ewArgs[2].(*ast.CallExpr); !ok || wtCallName(call.Fun) != "docadapter.Opener" {
		t.Errorf("newExtractWorker's opener argument is %s, want a docadapter.Opener(...) call: the capped, always-closing closure is only reached if main() builds it", wtRender(ewArgs[2]))
	} else if len(call.Args) != 1 {
		t.Errorf("docadapter.Opener is called with %d argument(s), want 1", len(call.Args))
	} else if sel, ok := call.Args[0].(*ast.SelectorExpr); !ok || wtCallName(sel) != svcName+".Open" {
		t.Errorf("docadapter.Opener is built over %s, want %s.Open: an opener over anything else reads no document, and nothing else in this file would notice", wtRender(call.Args[0]), svcName)
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
		if call, ok := fields["Sink"].(*ast.CallExpr); !ok || wtCallName(call.Fun) != "docadapter.PageSink" {
			t.Errorf("extraction.PageStore.Sink is %s, want a docadapter.PageSink(...) call: the adapter onto the object store is only reached if main() builds it", wtRender(fields["Sink"]))
		} else if len(call.Args) != 1 {
			t.Errorf("docadapter.PageSink is called with %d argument(s), want 1", len(call.Args))
		} else if id, ok := call.Args[0].(*ast.Ident); !ok || id.Name != objName {
			t.Errorf("docadapter.PageSink is built over %s, want %s -- the store document.NewS3Store built and main() already fatals on", wtRender(call.Args[0]), objName)
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
	// AIR-05-03: the page-image reader, followed back to a docadapter.PageObjectReader(...) call
	// the pages route above already builds. Any other value here reads no page image the
	// render actually wrote.
	if call, ok := ewArgs[9].(*ast.CallExpr); !ok || wtCallName(call.Fun) != "docadapter.PageObjectReader" {
		t.Errorf("newExtractWorker's pageBytes argument is %s, want a docadapter.PageObjectReader(...) call", wtRender(ewArgs[9]))
	} else if len(call.Args) != 1 {
		t.Errorf("docadapter.PageObjectReader is called with %d argument(s), want 1", len(call.Args))
	} else if id, ok := call.Args[0].(*ast.Ident); !ok || id.Name != objName {
		t.Errorf("docadapter.PageObjectReader is given %s, want %s -- the store document.NewS3Store built and main() already fatals on", wtRender(call.Args[0]), objName)
	}

	if sel, ok := ewArgs[10].(*ast.SelectorExpr); !ok || sel.Sel.Name != "Logger" {
		t.Errorf("newExtractWorker's last argument is %s, want the logger: every constructor in this file keeps logger last, and an auditor appended after it silently swaps the two", wtRender(ewArgs[10]))
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

// TestWorkerMain_AddsNoNewBinary: Core AC-8. The directory check alone is evaded by any
// name other than "extraction", so the second half asserts that submission and the worker are
// the only binaries that LINK internal/extraction -- transitively, since a library-level import reaches
// a binary that names neither package.
func TestWorkerMain_AddsNoNewBinary(t *testing.T) {
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
		"blog", "dashboard", "gateway", "invoice", "notifications", "opsconsole",
		"portfolio", "submission", "tenancy", "validation", "worker",
	}
	if !reflect.DeepEqual(dirs, wantDirs) {
		t.Errorf("cmd/ holds %v, want exactly %v: extraction runs inside the worker binary, and a new service directory is a new service nobody provisioned (blog is the binary of the existing blog service)", dirs, wantDirs)
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

	if want := []string{modulePkg + "cmd/submission", modulePkg + "cmd/worker"}; !reflect.DeepEqual(hits[extractionPkg], want) {
		t.Errorf("internal/extraction is linked into %v, want exactly %v: the worker runs extraction and submission serves its review routes, and one library-level import is enough to put it in a binary nobody provisioned", hits[extractionPkg], want)
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
	t.Fatal("cmd/worker/main.go declares no main(); every assertion below would be vacuous")
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
		t.Fatalf("cmd/worker/main.go assigns from %s at the top level of main() %d time(s), want exactly 1", want, calls)
	}
	if lhs == "" {
		t.Fatalf("the %s call assigns to no identifier, so nothing below can follow the value it built", want)
	}
	return lhs, args
}

// wtAppPool returns the identifier of the one top-level db.NewPool whose DSN is DATABASE_URL:
// the invoice_app pool every job runs on. The reader pool is the other db.NewPool call.
func wtAppPool(t *testing.T, f *ast.File) string {
	t.Helper()
	var names []string
	for _, stmt := range wtMainBody(t, f) {
		as, ok := stmt.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || wtCallName(call.Fun) != "db.NewPool" || len(call.Args) != 2 {
			continue
		}
		dsn, ok := call.Args[1].(*ast.CallExpr)
		if !ok || wtCallName(dsn.Fun) != "mustEnv" || len(dsn.Args) != 1 {
			continue
		}
		if lit, ok := dsn.Args[0].(*ast.BasicLit); ok && lit.Value == `"DATABASE_URL"` {
			if id, ok := as.Lhs[0].(*ast.Ident); ok {
				names = append(names, id.Name)
			}
		}
	}
	if len(names) != 1 {
		t.Fatalf("main() builds %d db.NewPool(ctx, mustEnv(\"DATABASE_URL\")) pool(s), want exactly 1", len(names))
	}
	return names[0]
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
		// The call's own err, tested `err != nil`, fatal in the body: `x, _ :=`, `err == nil` or
		// a fatal only in an else checks nothing.
		if last, ok := as.Lhs[len(as.Lhs)-1].(*ast.Ident); !ok || last.Name != "err" {
			continue
		}
		ifs, ok := body[i+1].(*ast.IfStmt)
		if !ok || ifs.Init != nil || !wtIsErrNotNil(ifs.Cond) {
			continue
		}
		ast.Inspect(ifs.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if wtCallName(c.Fun) == "platform.Fatal" {
					fatal = true
				}
			}
			return true
		})
	}
	return calls, fatal
}

func wtIsErrNotNil(e ast.Expr) bool {
	b, ok := e.(*ast.BinaryExpr)
	if !ok || b.Op != token.NEQ {
		return false
	}
	x, xok := b.X.(*ast.Ident)
	y, yok := b.Y.(*ast.Ident)
	return xok && yok && x.Name == "err" && y.Name == "nil"
}

// TestWorkerMain_FatalOnDocumentConfigError: AC #8. Both object-store calls sit at the top
// level of main(), so the refusal is unconditional the way PORT and MockConfigFromEnv already
// are -- gating it on whether extraction ever runs defers the failure to the worst moment.
//
// NOTE for the operator: the submission service carries no DOCUMENT_* variables today, so this
// wiring crash-loops it until they are set. tools/prenv/dsn.go now states that requirement.
func TestWorkerMain_FatalOnDocumentConfigError(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/worker/main.go: %v", err)
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
			t.Errorf("the statement after %s is not an error check that calls platform.Fatal: a malformed object-store configuration must refuse to boot, exactly as PORT and MockConfigFromEnv do", want)
		}
	}
}

// TestWorkerMain_FatalOnExtractorSelectionError: the boot half of AC-5.
// TestSelectTextReader_DoclingRequiresAUsableURL proves each selector RETURNS an error; this
// proves main() dies on it. Without it, dropping either error check ships a fleet that names
// docling, cannot build it, and quietly runs the Extractor branch instead.
func TestWorkerMain_FatalOnExtractorSelectionError(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/worker/main.go: %v", err)
	}

	// Control: the same matcher against a known-good boot fatal in the same file, so a false
	// below is a finding rather than a broken matcher.
	if calls, fatal := wtFatalAfter(t, f, "submission.MockConfigFromEnv"); calls != 1 || !fatal {
		t.Fatalf("the matcher found %d unconditional submission.MockConfigFromEnv call(s), fatal=%v, want 1 and true -- it can no longer recognise a known-good boot fatal, so the findings below mean nothing", calls, fatal)
	}

	for _, want := range []string{"selectExtractor", "selectTextReader"} {
		calls, fatal := wtFatalAfter(t, f, want)
		if calls != 1 {
			t.Errorf("main() assigns from %s %d time(s) at the top level, want 1: the selection must be unconditional, not gated on whether extraction ever runs", want, calls)
			continue
		}
		if !fatal {
			t.Errorf("the statement after %s is not an error check that calls platform.Fatal: an EXTRACTOR the fleet cannot build must refuse to boot rather than fall back to a seam nobody configured", want)
		}
	}
}

// An unparseable AI_FAKE must refuse to boot, not run the worker with the AI silently off.
func TestWorkerMain_FatalOnAIClientError(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/worker/main.go: %v", err)
	}
	if calls, fatal := wtFatalAfter(t, f, "submission.MockConfigFromEnv"); calls != 1 || !fatal {
		t.Fatalf("the matcher found %d unconditional submission.MockConfigFromEnv call(s), fatal=%v, want 1 and true -- it cannot recognise a known-good boot fatal", calls, fatal)
	}
	calls, fatal := wtFatalAfter(t, f, "ai.FromEnv")
	if calls != 1 {
		t.Fatalf("main() assigns from ai.FromEnv %d time(s) at the top level, want 1", calls)
	}
	if !fatal {
		t.Error("the statement after ai.FromEnv is not an error check that calls platform.Fatal")
	}
}

// A JEV_FAKE the process cannot parse, or fake plus a key, must refuse to boot.
func TestWorkerMain_FatalOnJevClientError(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/worker/main.go: %v", err)
	}
	if calls, fatal := wtFatalAfter(t, f, "submission.MockConfigFromEnv"); calls != 1 || !fatal {
		t.Fatalf("the matcher found %d unconditional submission.MockConfigFromEnv call(s), fatal=%v, want 1 and true -- it cannot recognise a known-good boot fatal", calls, fatal)
	}
	calls, fatal := wtFatalAfter(t, f, "jev.FromEnv")
	if calls != 1 {
		t.Fatalf("main() assigns from jev.FromEnv %d time(s) at the top level, want 1", calls)
	}
	if !fatal {
		t.Error("the statement after jev.FromEnv is not an error check that calls platform.Fatal")
	}
}

// The Jev client is built after the AI client and before the extract worker and the queue, so a
// bad JEV_FAKE stops the process before River starts; nothing reassigns it on the way.
func TestWorkerMain_JevClientIsBuiltOnceBeforeTheQueue(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/worker/main.go: %v", err)
	}
	body := wtMainBody(t, f)
	at := func(want string) int {
		t.Helper()
		idx := -1
		for i, stmt := range body {
			as, ok := stmt.(*ast.AssignStmt)
			if !ok || len(as.Rhs) != 1 {
				continue
			}
			if call, ok := as.Rhs[0].(*ast.CallExpr); ok && wtCallName(call.Fun) == want {
				if idx != -1 {
					t.Fatalf("main() assigns from %s twice at top level", want)
				}
				idx = i
			}
		}
		if idx == -1 {
			t.Fatalf("main() has no top-level assignment from %s", want)
		}
		return idx
	}
	aiAt, jevAt, ewAt, qAt := at("ai.FromEnv"), at("jev.FromEnv"), at("newExtractWorker"), at("queue.New")
	if !(aiAt < jevAt && jevAt < ewAt && ewAt < qAt) {
		t.Errorf("statement order ai.FromEnv=%d jev.FromEnv=%d newExtractWorker=%d queue.New=%d, want strictly increasing", aiAt, jevAt, ewAt, qAt)
	}

	// The fatal names the service, like every other boot fatal in main().
	ifs, ok := body[jevAt+1].(*ast.IfStmt)
	if !ok || len(ifs.Body.List) != 1 {
		t.Fatalf("the statement after jev.FromEnv is %T, want an if with one statement", body[jevAt+1])
	}
	var call *ast.CallExpr
	if es, ok := ifs.Body.List[0].(*ast.ExprStmt); ok {
		call, _ = es.X.(*ast.CallExpr)
	}
	if call == nil || wtCallName(call.Fun) != "platform.Fatal" || len(call.Args) < 2 {
		t.Fatal("the jev.FromEnv error branch is not one platform.Fatal call")
	}
	if got := wtRender(call.Args[0]); got != "app.Logger" {
		t.Errorf("the jev.FromEnv fatal logs on %s, want app.Logger", got)
	}
	if lit, ok := call.Args[1].(*ast.BasicLit); !ok || !strings.HasPrefix(lit.Value, `"worker: `) {
		t.Errorf("the jev.FromEnv fatal format is %s, want it to start with \"worker: \"", wtRender(call.Args[1]))
	}

	// Exactly one write to the client's name anywhere in main(), nested blocks included.
	jevName := body[jevAt].(*ast.AssignStmt).Lhs[0].(*ast.Ident).Name
	writes := 0
	ast.Inspect(&ast.BlockStmt{List: body}, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for _, l := range x.Lhs {
				if id, ok := l.(*ast.Ident); ok && id.Name == jevName {
					writes++
				}
			}
		case *ast.ValueSpec:
			for _, id := range x.Names {
				if id.Name == jevName {
					writes++
				}
			}
		}
		return true
	})
	if writes != 1 {
		t.Errorf("main() writes %s %d time(s), want 1: a later write hands newExtractWorker something jev.FromEnv did not build", jevName, writes)
	}
}

// jev may not import ai (TestJevPackage_StaysInsideThePlatformFence), so the shared key is proved here.
func TestWorkerMain_JevAndAIReadOneKey(t *testing.T) {
	if ai.EnvKey != "OPENROUTER_API_KEY" {
		t.Errorf("ai.EnvKey = %q, want %q", ai.EnvKey, "OPENROUTER_API_KEY")
	}
	if jev.EnvKey != ai.EnvKey {
		t.Errorf("jev.EnvKey = %q, want ai.EnvKey %q: Jev and the AI client read one key", jev.EnvKey, ai.EnvKey)
	}
}

// An unset key boots with the client off: the worker gets a non-nil asker that asks nothing.
func TestNewExtractWorker_AnOffJevClientIsWiredAndDisabled(t *testing.T) {
	t.Setenv(jev.EnvKey, "")
	t.Setenv(jev.EnvFake, "")
	c, err := jev.FromEnv(nil)
	if err != nil {
		t.Fatalf("jev.FromEnv with no key: %v", err)
	}
	ew := newExtractWorker(&pgxpool.Pool{}, extraction.NewMockExtractor(), nil, nil, nil, nil, nil, nil, c, nil, nil)
	if ew.Jev == nil {
		t.Fatal("ExtractWorker.Jev is nil, want the off client")
	}
	if ew.Jev != extraction.JevAsker(c) {
		t.Error("ExtractWorker.Jev is not the jev.FromEnv client passed in")
	}
	if ew.Jev.Enabled() {
		t.Error("ExtractWorker.Jev.Enabled() = true with no key and JEV_FAKE unset, want false")
	}
}

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
// already exists in TestWorkerMain_WiresTheQueueSeams above, which reads the literal's
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

// TestSelectors_IgnoreDoclingURLUnderMockAndUnset: EXTRACTOR is the only selector, so a
// DOCLING_URL set beside an unset or mock EXTRACTOR is inert in BOTH selectors -- the posture
// cmd/gateway/main.go loadUpstreams already ships for an unrouted <NAME>_URL. Every other mock
// and unset row in this file passes "" for the URL, so nothing else here exercises the pair.
func TestSelectors_IgnoreDoclingURLUnderMockAndUnset(t *testing.T) {
	const valid = "http://docling.railway.internal:8080"
	for _, name := range []string{"", "mock"} {
		t.Run("EXTRACTOR="+name, func(t *testing.T) {
			got, err := selectTextReader(name, valid)
			if err != nil {
				t.Fatalf("selectTextReader(%q, %q) returned error %v, want a nil reader and no error", name, valid, err)
			}
			if got != nil {
				t.Errorf("selectTextReader(%q, %q) = %T, want nil -- DOCLING_URL alone must not switch a mock fleet onto the sidecar", name, valid, got)
			}

			ext, err := selectExtractor(name, valid)
			if err != nil {
				t.Fatalf("selectExtractor(%q, %q) returned error %v, want a *MockExtractor and no error", name, valid, err)
			}
			if _, ok := ext.(*extraction.MockExtractor); !ok {
				t.Errorf("selectExtractor(%q, %q) = %T, want *extraction.MockExtractor -- the URL is inert until EXTRACTOR names docling, or extraction_jobs.extractor stops matching what an operator configured", name, valid, ext)
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
	cases := []struct {
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
		// A pasted Railway value. Trimming inside both selectors leaves their case sets equal,
		// so TestSelectors_ShareOneCaseSet stays green and only this row reds.
		{"whitespace near-miss", " docling", valid, true},
	}
	// The table drives every assertion below, so an empty one is a green run that checked
	// nothing. Both accepted spellings and both rejection modes must stay represented.
	if len(cases) < 8 {
		t.Fatalf("the agreement table carries %d row(s), want at least 8 -- fewer means the docling arm or a rejection mode went unexercised", len(cases))
	}
	for _, tc := range cases {
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
		t.Fatalf("parse cmd/worker/main.go: %v", err)
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
		t.Fatalf("cmd/worker/main.go switches on extractorName in %v, want exactly [selectExtractor selectTextReader] -- with fewer, the set comparison below is vacuous", names)
	}
	if len(ext) < 4 {
		t.Fatalf("selectExtractor's case set is %v, want at least 4 entries (\"\", mock, docling, <default>) -- a set this small means the scan read almost nothing", ext)
	}
	if !reflect.DeepEqual(ext, text) {
		t.Errorf("selectExtractor accepts %v and selectTextReader accepts %v: any value one accepts the other must accept, or an EXTRACTOR that builds an Extractor leaves Text unselected", ext, text)
	}
}

// TestWorkerMain_WiresOneAICallRecorderOntoBothClients: one recorder, built on pool and app.Logger, reaches both clients before
// the extraction worker receives them. AST, so reformatting cannot break it.
func TestWorkerMain_WiresOneAICallRecorderOntoBothClients(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/worker/main.go: %v", err)
	}
	mainBody := f
	var builds, aiSet, jevSet int
	var recVar string
	var setPos []token.Pos
	ast.Inspect(mainBody, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if len(x.Rhs) == 1 && len(x.Lhs) == 1 && isSelectorCall(x.Rhs[0], "aicalls", "NewRecorder") {
				call := x.Rhs[0].(*ast.CallExpr)
				if len(call.Args) == 2 && isIdent(call.Args[0], "pool") && isSelector(call.Args[1], "app", "Logger") {
					builds++
					if id, ok := x.Lhs[0].(*ast.Ident); ok {
						recVar = id.Name
					}
				}
			}
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "SetRecorder" || len(x.Args) != 1 {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok || !isSelector(x.Args[0], recVar, "Record") {
				return true
			}
			switch recv.Name {
			case "aiClient":
				aiSet++
				setPos = append(setPos, x.Pos())
			case "jevClient":
				jevSet++
				setPos = append(setPos, x.Pos())
			}
		}
		return true
	})
	if builds != 1 || recVar == "" {
		t.Fatalf("aicalls.NewRecorder(pool, app.Logger) is assigned %d time(s), want exactly 1", builds)
	}
	if aiSet != 1 || jevSet != 1 {
		t.Fatalf("SetRecorder(%s.Record) on aiClient %d time(s), on jevClient %d time(s), want 1 each", recVar, aiSet, jevSet)
	}
	first := token.NoPos
	ast.Inspect(mainBody, func(n ast.Node) bool {
		if isNewExtractWorkerCall(n) && (first == token.NoPos || n.Pos() < first) {
			first = n.Pos()
		}
		return true
	})
	if first == token.NoPos {
		t.Fatal("control: the newExtractWorker call not found -- the position check below is vacuous")
	}
	for _, p := range setPos {
		if p >= first {
			t.Errorf("a SetRecorder call sits after the newExtractWorker call; the clients would run unrecorded")
		}
	}
}

func isNewExtractWorkerCall(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	return ok && wtCallName(call.Fun) == "newExtractWorker"
}

// TestWorkerMain_WaitsForTheAICallRecorderBeforeThePoolCloses: a top-level `defer <recorder>.Wait()` sits after `defer pool.Close()`, so it runs first and no
// in-flight row hits a closed pool. A defer in a nested func literal does not count.
func TestWorkerMain_WaitsForTheAICallRecorderBeforeThePoolCloses(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	body := wtMainBody(t, f)
	var recVar string
	var closePos, waitPos token.Pos
	var waits int
	for _, st := range body {
		switch x := st.(type) {
		case *ast.AssignStmt:
			if len(x.Rhs) == 1 && len(x.Lhs) == 1 && isSelectorCall(x.Rhs[0], "aicalls", "NewRecorder") {
				if id, ok := x.Lhs[0].(*ast.Ident); ok {
					recVar = id.Name
				}
			}
		case *ast.DeferStmt:
			if isSelectorCall(x.Call, "pool", "Close") {
				closePos = x.Pos()
			}
			if recVar != "" && isSelectorCall(x.Call, recVar, "Wait") {
				waits++
				waitPos = x.Pos()
			}
		}
	}
	if recVar == "" || closePos == token.NoPos {
		t.Fatalf("control: recorder var %q, defer pool.Close at %v -- the walk is broken", recVar, closePos)
	}
	if waits != 1 {
		t.Fatalf("defer %s.Wait() appears %d time(s), want 1", recVar, waits)
	}
	if waitPos < closePos {
		t.Errorf("defer %s.Wait() sits before defer pool.Close(); it would run after the pool closes", recVar)
	}
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func isSelector(e ast.Expr, recv, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == recv
}

func isSelectorCall(e ast.Expr, pkg, name string) bool {
	call, ok := e.(*ast.CallExpr)
	return ok && isSelector(call.Fun, pkg, name)
}

// The matcher is the one TestWorkerMain_FatalOnDocumentConfigError uses.
func TestWorkerMain_FatalOnGatewayURLError(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/worker/main.go: %v", err)
	}
	if calls, fatal := wtFatalAfter(t, f, "submission.MockConfigFromEnv"); calls != 1 || !fatal {
		t.Fatalf("the matcher found %d unconditional submission.MockConfigFromEnv call(s), fatal=%v, want 1 and true -- it can no longer recognise a known-good boot fatal, so the findings below mean nothing", calls, fatal)
	}
	calls, fatal := wtFatalAfter(t, f, "systemhealth.GatewayURL")
	if calls != 1 {
		t.Fatalf("main() assigns from systemhealth.GatewayURL %d time(s) at the top level, want 1: GATEWAY_URL is required at every boot", calls)
	}
	if !fatal {
		t.Error("the statement after systemhealth.GatewayURL is not an error check that calls platform.Fatal: a missing or malformed GATEWAY_URL must refuse to boot")
	}
	_, args := wtOneCall(t, f, "systemhealth.GatewayURL")
	var getenv *ast.CallExpr
	if len(args) == 1 {
		getenv, _ = args[0].(*ast.CallExpr)
	}
	var lit *ast.BasicLit
	if getenv != nil && wtCallName(getenv.Fun) == "os.Getenv" && len(getenv.Args) == 1 {
		lit, _ = getenv.Args[0].(*ast.BasicLit)
	}
	if lit == nil || lit.Value != `"GATEWAY_URL"` {
		t.Error(`systemhealth.GatewayURL is not called with os.Getenv("GATEWAY_URL")`)
	}
}

// The sampler runs here, once, on its interval; the staff health route stays in cmd/submission.
func TestWorkerMain_RunsTheHealthSampler(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/worker/main.go: %v", err)
	}
	var samplers int
	var controlQueue bool
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || wtCallName(call.Fun) != "app.AddBackgroundWorker" || len(call.Args) != 1 {
			return true
		}
		if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == "q" {
			controlQueue = true
		}
		if w, ok := call.Args[0].(*ast.CallExpr); ok && wtCallName(w.Fun) == "codelist.NewWorker" && len(w.Args) == 2 &&
			isSelector(w.Args[0], "systemhealth", "SampleInterval") && wtRender(w.Args[1]) == "sampler.Sample" {
			samplers++
		}
		return true
	})
	if !controlQueue {
		t.Fatal("control needle: no app.AddBackgroundWorker(q) found -- the AST walk is broken, so the count below is vacuous")
	}
	if samplers != 1 {
		t.Errorf("codelist.NewWorker(systemhealth.SampleInterval, sampler.Sample) is registered %d time(s), want exactly 1", samplers)
	}
}

func TestWorkerMain_GatewayURLFatalSitsBeforeThePool(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/worker/main.go: %v", err)
	}
	pos := map[string]token.Pos{}
	for _, stmt := range wtMainBody(t, f) {
		ast.Inspect(stmt, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := wtCallName(call.Fun)
			switch {
			case name == "platform.New" || name == "systemhealth.GatewayURL":
				if _, seen := pos[name]; !seen {
					pos[name] = call.Pos()
				}
			case (name == "os.Getenv" || name == "mustEnv") && len(call.Args) == 1:
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Value == `"DATABASE_URL"` {
					pos["DATABASE_URL"] = call.Pos()
				}
			}
			return true
		})
	}
	for _, name := range []string{"platform.New", "systemhealth.GatewayURL", "DATABASE_URL"} {
		if pos[name] == token.NoPos {
			t.Fatalf("main() has no %s call: the order below cannot be read", name)
		}
	}
	if !(pos["platform.New"] < pos["systemhealth.GatewayURL"] && pos["systemhealth.GatewayURL"] < pos["DATABASE_URL"]) {
		t.Errorf("order is platform.New@%d gatewayURL@%d DATABASE_URL@%d, want platform.New < gatewayURL < DATABASE_URL: the GATEWAY_TOKEN refusal stays first and no database is contacted before the GATEWAY_URL refusal", pos["platform.New"], pos["systemhealth.GatewayURL"], pos["DATABASE_URL"])
	}
}

// AC 2: Cloud Run throttles a request-driven service between requests, so only the worker may
// register a background worker or a River client that fetches jobs.
func TestOnlyTheWorkerRunsBackgroundWork(t *testing.T) {
	scan := func(path string) (adds, fetchingQueues int) {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "AddBackgroundWorker" {
					adds++
				}
			case *ast.CompositeLit:
				if wtCallName(x.Type) != "queue.Config" {
					return true
				}
				for _, elt := range x.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if key, ok := kv.Key.(*ast.Ident); ok && (key.Name == "Workers" || key.Name == "Queues") {
							fetchingQueues++
						}
					}
				}
			}
			return true
		})
		return adds, fetchingQueues
	}

	// Control: the worker's own main must register both, or the zeros below mean nothing.
	if adds, fetching := scan("main.go"); adds == 0 || fetching == 0 {
		t.Fatalf("cmd/worker/main.go: %d AddBackgroundWorker call(s), %d fetching queue.Config key(s); want both non-zero -- the matcher is broken", adds, fetching)
	}

	mains, err := filepath.Glob(filepath.Join("..", "*", "main.go"))
	if err != nil || len(mains) < 10 {
		t.Fatalf("found %d cmd/*/main.go file(s) (%v), want at least 10", len(mains), err)
	}
	for _, path := range mains {
		if filepath.Base(filepath.Dir(path)) == "worker" {
			continue
		}
		if adds, fetching := scan(path); adds != 0 || fetching != 0 {
			t.Errorf("%s: %d AddBackgroundWorker call(s), %d queue.Config Workers/Queues key(s); want none -- move the work to cmd/worker and keep an insert-only queue.Config{}", path, adds, fetching)
		}
	}
}
