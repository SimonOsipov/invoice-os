// worker_internal_test.go: the worker specs that need no database. Package extraction so they
// can name the unexported args type; everything needing a pool is in worker_db_test.go.
package extraction

import (
	"context"
	"go/ast"
	"testing"
	"time"

	"github.com/riverqueue/river"
)

// The kind is persisted as river_job.kind, so a rename orphans every already-queued row.
func TestExtractArgs_KindIsStable(t *testing.T) {
	if got, want := (extractArgs{}).Kind(), "extraction_extract"; got != want {
		t.Errorf("Kind() = %q, want %q", got, want)
	}
}

func TestExtractArgs_InsertOptsPinQueueAndAttempts(t *testing.T) {
	opts := (extractArgs{}).InsertOpts()
	if opts.Queue != QueueName {
		t.Errorf("InsertOpts().Queue = %q, want QueueName (%q)", opts.Queue, QueueName)
	}
	if opts.MaxAttempts != 3 {
		t.Errorf("InsertOpts().MaxAttempts = %d, want 3", opts.MaxAttempts)
	}
}

// QueueName is persisted as river_job.queue, so it is pinned to its literal exactly as Kind is:
// TestExtractArgs_InsertOptsPinQueueAndAttempts compares the opts against QueueName and is
// therefore blind to QueueName itself being wrong.
func TestExtractArgs_QueueIsNotRiverDefault(t *testing.T) {
	if got, want := QueueName, "extraction"; got != want {
		t.Errorf("QueueName = %q, want %q", got, want)
	}
	// Not implied by the literal above: a River release that renamed its default queue onto
	// this name would silently put extraction work back on the submission pool.
	if river.QueueDefault == QueueName {
		t.Errorf("river.QueueDefault is now %q, the same queue extraction claims; the extraction pool is no longer isolated", river.QueueDefault)
	}
}

// EXTR-05-06 AC-6: flaggedCount stops at the top level. An alternative never carries its own
// reason by FieldResult's own contract, but the count must not descend into Alternatives
// regardless -- two alternatives here, alongside the one decided field that is actually
// flagged, so a loop that flattened Alternatives in would inflate the count past 1.
func TestExtractWorker_FlaggedCountIgnoresAlternatives(t *testing.T) {
	v := "x"
	results := []FieldResult{
		{Field: Field{Name: "invoice_number", Value: &v, Reason: ReasonNone}},
		{
			Field: Field{Name: "issue_date", Value: &v, Reason: ReasonAmbiguous},
			Alternatives: []Field{
				{Name: "issue_date", Value: &v},
				{Name: "issue_date", Value: &v},
			},
		},
	}
	if got, want := flaggedCount(results), 1; got != want {
		t.Errorf("flaggedCount = %d, want %d -- one decided field is flagged; its two alternatives must not inflate the count", got, want)
	}
}

// River resolves cmp.Or(workUnit.Timeout(), clientJobTimeout), so a per-worker Timeout wins
// over the client default without raising it for SubmitWorker and PollWorker too.
func TestExtractWorker_TimeoutExceedsRiverDefault(t *testing.T) {
	got := (&ExtractWorker{}).Timeout(nil)
	if want := 10 * time.Minute; got != want {
		t.Errorf("Timeout() = %v, want %v", got, want)
	}
	if got <= river.JobTimeoutDefault {
		t.Errorf("Timeout() = %v, want > river.JobTimeoutDefault (%v)", got, river.JobTimeoutDefault)
	}
}

// --- EXTR-08-03 T3-4: the success emission is the closure's last statement -------------

// wiCalleeName spells a call target as written: "w.Audit", "queue.OncePerJob".
func wiCalleeName(fun ast.Expr) string {
	switch e := fun.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok {
			return x.Name + "." + e.Sel.Name
		}
	}
	return ""
}

// wiDirectCalls returns the calls to want made by body itself. It does not descend into
// nested function literals: a call from an inner closure is that closure's last statement,
// not this one's.
func wiDirectCalls(body *ast.BlockStmt, want string) []*ast.CallExpr {
	var out []*ast.CallExpr
	visit := func(n ast.Node) bool {
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		if c, ok := n.(*ast.CallExpr); ok && wiCalleeName(c.Fun) == want {
			out = append(out, c)
		}
		return true
	}
	for _, st := range body.List {
		ast.Inspect(st, visit)
	}
	return out
}

// AC-D2. On the success path the audit write is the LAST statement inside the
// queue.OncePerJob closure, so the marker, the field results, the advance to succeeded and the
// audit row share one transaction and one fate. A statement placed after it would commit
// outside that guarantee's reach. Mirrors internal/submission's
// TestSubmissionAudit_FailureWriteIsLastInItsClosure.
func TestExtractWorker_AuditWriteIsLastInItsClosure(t *testing.T) {
	fset, files, parsed := auditPkgFiles(t)

	var scanned, onceClosures, auditClosures, matched int
	var sites []string

	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.FuncLit)
			if ok && lit.Body != nil {
				scanned++
				if len(wiDirectCalls(lit.Body, "w.Audit")) > 0 {
					auditClosures++
				}
			}

			call, ok := n.(*ast.CallExpr)
			if !ok || wiCalleeName(call.Fun) != "queue.OncePerJob" {
				return true
			}
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.FuncLit)
				if !ok || lit.Body == nil {
					continue
				}
				onceClosures++
				calls := wiDirectCalls(lit.Body, "w.Audit")
				if len(calls) == 0 {
					continue
				}
				matched++
				loc := fset.Position(lit.Pos()).String()
				sites = append(sites, loc)
				if len(calls) != 1 {
					t.Errorf("the queue.OncePerJob closure at %s calls w.Audit %d times, want exactly 1 -- one terminal success is one event", loc, len(calls))
					continue
				}
				if len(lit.Body.List) == 0 {
					t.Errorf("the closure at %s has an empty body yet matched a call -- the walk is wrong", loc)
					continue
				}
				last := lit.Body.List[len(lit.Body.List)-1]
				ret, isReturn := last.(*ast.ReturnStmt)
				if !isReturn {
					t.Errorf("the queue.OncePerJob closure at %s ends in %T, want its w.Audit call to be the final return -- anything after it commits outside OncePerJob's reach", loc, last)
					continue
				}
				if len(ret.Results) != 1 || ret.Results[0] != calls[0] {
					t.Errorf("the queue.OncePerJob closure at %s ends in a return that is not its w.Audit call (returns %d expression(s)) -- the audit write must be the last statement", loc, len(ret.Results))
				}
			}
			return true
		})
	}

	// Floors: a broken walk finds no literals and reports every claim above as satisfied.
	if scanned < 10 {
		t.Fatalf("walked %d function literal(s) across %v, want >= 10 -- the walk is broken, so the counts below are vacuous", scanned, parsed)
	}
	if onceClosures < 1 {
		t.Fatalf("found %d queue.OncePerJob closure(s), want >= 1 -- the success path is not where this walk thinks it is", onceClosures)
	}
	if matched != 1 {
		t.Fatalf("queue.OncePerJob closures calling w.Audit = %d, want exactly 1 (found at %v) -- the set is closed; a second success-path emitter is a decision, not something this test waves through", matched, sites)
	}
	// The two terminal points, and only those two: the OncePerJob closure and the
	// dead_lettered branch's own transaction.
	if auditClosures != 2 {
		t.Errorf("closures calling w.Audit = %d, want exactly 2 -- one per terminal point", auditClosures)
	}
}

// TestMockExtractor_DefaultResultFlaggedCountIgnoresAlternatives (RED-FIRST): AC-9, over the
// REAL default result rather than a hand-built one. TestExtractWorker_FlaggedCountIgnoresAlternatives
// above proves the loop stops at the top level; nothing connected that to what the mock actually
// ships, so a mock whose alternatives were lifted into decided fields would pass it.
func TestMockExtractor_DefaultResultFlaggedCountIgnoresAlternatives(t *testing.T) {
	results, err := NewMockExtractor().Extract(context.Background(),
		Document{Bytes: []byte("no fixture claims these bytes"), ContentType: "application/pdf"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	var decided, alternatives, flagged int
	for _, r := range results {
		decided++
		alternatives += len(r.Alternatives)
		if r.Reason != ReasonNone {
			flagged++
		}
	}
	// Floors: with no alternatives to descend into, the count below cannot tell a loop that
	// stops at the top level from one that does not.
	if alternatives == 0 {
		t.Fatalf("the default result carries %d decided field(s) and no alternatives; this spec would pass on a flaggedCount that flattened them", decided)
	}

	if got, want := flaggedCount(results), 7; got != want {
		t.Errorf("flaggedCount over the default result = %d, want %d -- %d decided field(s) carry a reason and %d alternative(s) must not be counted",
			got, want, flagged, alternatives)
	}
}
