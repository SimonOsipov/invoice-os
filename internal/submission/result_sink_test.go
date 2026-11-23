package submission_test

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

type sinkCall struct {
	tenant, invoice, status string
}

// recSink records each call and the invoice status it sees on the given tx.
// insertKey makes it write an idempotency_keys row before it fails.
type recSink struct {
	mu        sync.Mutex
	calls     []sinkCall
	err       error
	insertKey bool
}

func (s *recSink) ResultReached(ctx context.Context, tx pgx.Tx, tenantID, invoiceID string) error {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invoiceID).Scan(&status); err != nil {
		return err
	}
	s.mu.Lock()
	s.calls = append(s.calls, sinkCall{tenantID, invoiceID, status})
	s.mu.Unlock()
	if s.insertKey {
		if _, err := tx.Exec(ctx, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, $2)`, tenantID, "sink:"+uuid.NewString()); err != nil {
			return err
		}
	}
	return s.err
}

func (s *recSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

type capLog struct {
	mu   sync.Mutex
	errs []string
}

func (c *capLog) Enabled(context.Context, slog.Level) bool { return true }
func (c *capLog) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *capLog) WithGroup(string) slog.Handler            { return c }
func (c *capLog) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		c.mu.Lock()
		c.errs = append(c.errs, r.Message)
		c.mu.Unlock()
	}
	return nil
}

func rsSubmitWorker(f *effectsFixture, a submission.Adapter, sink submission.ResultSink, lg *slog.Logger) *submission.SubmitWorker {
	w := newTestWorker(f.app, a)
	w.Results, w.Logger = sink, lg
	return w
}

// rsPaths are the seven terminal paths; each returns Work's error. attempt is the River attempt (of 8).
var rsPaths = []struct {
	name   string
	want   string // invoice status after the path
	final  bool   // the path is only terminal on the final attempt
	submit scriptedOutcome
	poll   *scriptedOutcome
	xform  bool
}{
	{name: "submit accepted", want: "accepted", submit: scriptedOutcome{result: submission.Accepted{IRN: "NG-1", CSID: "C", QRPayload: "Q"}, evidence: submission.Evidence{ReachedWire: true}}},
	{name: "submit rejected", want: "rejected", submit: scriptedOutcome{result: submission.Rejected{Reasons: []submission.Reason{{Code: "E1", Message: "bad"}}}, evidence: submission.Evidence{ReachedWire: true}}},
	{name: "transform failed", want: "failed", xform: true},
	{name: "submit final retryable", want: "failed", final: true, submit: scriptedOutcome{result: submission.Retryable{Err: errors.New("503")}, evidence: submission.Evidence{ReachedWire: true}}},
	{name: "poll accepted", want: "accepted", poll: &scriptedOutcome{result: submission.Accepted{IRN: "NG-2", CSID: "C", QRPayload: "Q"}, evidence: submission.Evidence{ReachedWire: true}}},
	{name: "poll rejected", want: "rejected", poll: &scriptedOutcome{result: submission.Rejected{Reasons: []submission.Reason{{Code: "E2", Message: "bad"}}}, evidence: submission.Evidence{ReachedWire: true}}},
	{name: "poll final retryable", want: "failed", final: true, poll: &scriptedOutcome{result: submission.Retryable{Err: errors.New("503")}, evidence: submission.Evidence{ReachedWire: true}}},
}

// rsDrive runs path i on a fresh queued invoice and returns its ids and Work's error.
func rsDrive(t *testing.T, f *effectsFixture, i int, sink submission.ResultSink, lg *slog.Logger, attempt int) (tenantID, invoiceID string, firstJob int64, err error) {
	t.Helper()
	p := rsPaths[i]
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	t.Cleanup(cleanup)
	idem := rsIdem(invoiceID)
	args := submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idem}
	if p.poll == nil {
		a := newScriptedAdapter()
		if p.xform {
			a.transformErr = errors.New("cannot build")
		} else {
			a.submitQueue = []scriptedOutcome{p.submit}
		}
		return tenantID, invoiceID, 1, rsSubmitWorker(f, a, sink, lg).Work(ctx, newSubmitJob(1, attempt, 8, args))
	}
	a := newScriptedAdapter(scriptedOutcome{result: submission.Pending{Ref: "r1", PollAfter: time.Now().Add(time.Hour)}, evidence: submission.Evidence{ReachedWire: true}})
	if e := rsSubmitWorker(f, a, sink, lg).Work(ctx, newSubmitJob(1, 1, 8, args)); e != nil {
		t.Fatalf("submit to pending: %v", e)
	}
	wj := wjRequire(t, f, tenantID, idem)
	a.pollQueue = []scriptedOutcome{*p.poll}
	pw := newTestPollWorker(f.app, a)
	pw.Results, pw.Logger = sink, lg
	return tenantID, invoiceID, 2, pw.Work(ctx, newPollJob(2, attempt, 8, submission.PollArgs{TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: wj.id, Sequence: 1}))
}

func rsIdem(invoiceID string) string { return "req-rs:" + invoiceID }

// rsSinkKeys counts the idempotency_keys rows a recSink with insertKey left behind.
func rsSinkKeys(t *testing.T, f *effectsFixture, tenantID string) int {
	t.Helper()
	var keys int
	if err := db.WithinTenantTx(context.Background(), f.app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM idempotency_keys WHERE key LIKE 'sink:%'`).Scan(&keys)
	}); err != nil {
		t.Fatal(err)
	}
	return keys
}

func rsAttempt(i int) int {
	if rsPaths[i].final {
		return 8
	}
	return 1
}

func TestResultSink_CalledOnceOnEveryTerminalPath(t *testing.T) {
	f := requireExchangeDB(t)
	for i, p := range rsPaths {
		t.Run(p.name, func(t *testing.T) {
			sink := &recSink{}
			tenantID, invoiceID, _, _ := rsDrive(t, f, i, sink, nil, rsAttempt(i))
			if len(sink.calls) != 1 {
				t.Fatalf("sink calls = %d, want 1", len(sink.calls))
			}
			if got, want := sink.calls[0], (sinkCall{tenantID, invoiceID, p.want}); got != want {
				t.Errorf("sink call = %+v, want %+v", got, want)
			}
		})
	}
}

func TestResultSink_ErrorOnTheFirstAttemptKeepsTheResultAndNeverResubmits(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()
	idem := "req-" + uuid.NewString() + ":" + invoiceID
	args := submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idem}
	a := newScriptedAdapter(rsPaths[0].submit, rsPaths[0].submit)
	capture := &capLog{}

	sink := &recSink{err: errors.New("sink boom"), insertKey: true}
	if err := rsSubmitWorker(f, a, sink, slog.New(capture)).Work(ctx, newSubmitJob(1, 1, 8, args)); err != nil {
		t.Fatalf("Work = %v, want nil: the result stays", err)
	}
	if keys := rsSinkKeys(t, f, tenantID); keys != 0 {
		t.Errorf("sink key rows = %d, want 0 (rolled back to the savepoint)", keys)
	}
	if len(capture.errs) != 1 {
		t.Errorf("Error log records = %d, want 1", len(capture.errs))
	}
	if s := wiRead(t, f, tenantID, invoiceID).status; s != "accepted" {
		t.Errorf("invoice status = %q, want accepted", s)
	}
	if n := auditCount(t, f, tenantID, "submission.accepted"); n != 1 {
		t.Errorf("submission.accepted audit rows = %d, want 1", n)
	}
	if err := rsSubmitWorker(f, a, nil, nil).Work(ctx, newSubmitJob(1, 2, 8, args)); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if a.submitCalls != 1 {
		t.Errorf("adapter.Submit calls = %d, want 1: NRS already answered", a.submitCalls)
	}
}

func TestResultSink_FinalAttemptSinkErrorKeepsTheResult(t *testing.T) {
	f := requireExchangeDB(t)
	for i, p := range rsPaths {
		t.Run(p.name, func(t *testing.T) {
			_, rec, want := sentrytest.Boot(t, "submission")
			capture := &capLog{}
			sink := &recSink{err: errors.New("sink boom"), insertKey: true}
			tenantID, invoiceID, _, err := rsDrive(t, f, i, sink, slog.New(capture), 8)
			_, _, _, nilErr := rsDrive(t, f, i, nil, nil, 8)

			if fmt.Sprint(err) != fmt.Sprint(nilErr) {
				t.Errorf("Work = %v, want what a nil sink returns: %v", err, nilErr)
			}
			if s := wiRead(t, f, tenantID, invoiceID).status; s != p.want {
				t.Errorf("invoice status = %q, want %q", s, p.want)
			}
			ev := "submission.failed"
			if p.want != "failed" {
				ev = "submission." + p.want
			}
			if n := auditCount(t, f, tenantID, ev); n != 1 {
				t.Errorf("%s audit rows = %d, want 1", ev, n)
			}
			if keys := rsSinkKeys(t, f, tenantID); keys != 0 {
				t.Errorf("sink key rows = %d, want 0 (rolled back to the savepoint)", keys)
			}
			if len(capture.errs) != 1 {
				t.Errorf("Error log records = %d, want 1", len(capture.errs))
			}
			rec.One(t, want)
		})
	}
}

// A sink that succeeds on the final attempt keeps its writes: the savepoint commits.
func TestResultSink_FinalAttemptSinkSuccessKeepsItsWrites(t *testing.T) {
	f := requireExchangeDB(t)
	for i, p := range rsPaths {
		t.Run(p.name, func(t *testing.T) {
			capture := &capLog{}
			sink := &recSink{insertKey: true}
			tenantID, invoiceID, _, err := rsDrive(t, f, i, sink, slog.New(capture), 8)
			if sink.count() != 1 {
				t.Fatalf("sink calls = %d, want 1 (Work = %v)", sink.count(), err)
			}
			if keys := rsSinkKeys(t, f, tenantID); keys != 1 {
				t.Errorf("sink key rows = %d, want the sink's 1 kept", keys)
			}
			if s := wiRead(t, f, tenantID, invoiceID).status; s != p.want {
				t.Errorf("invoice status = %q, want %q", s, p.want)
			}
			if len(capture.errs) != 0 {
				t.Errorf("Error log records = %d, want 0", len(capture.errs))
			}
		})
	}
}

func TestResultSink_RedeliveryDoesNotCallAgain(t *testing.T) {
	f := requireExchangeDB(t)
	sink := &recSink{}
	tenantID, invoiceID, _, _ := rsDrive(t, f, 0, sink, nil, 1)
	if n := sink.count(); n != 1 {
		t.Fatalf("sink calls after the first delivery = %d, want 1", n)
	}
	// Job id 1 already applied in rsDrive: the same job and key again must not reach the sink.
	args := submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: rsIdem(invoiceID)}
	if err := rsSubmitWorker(f, newScriptedAdapter(rsPaths[0].submit), sink, nil).Work(context.Background(), newSubmitJob(1, 1, 8, args)); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if n := sink.count(); n != 1 {
		t.Errorf("sink calls after the redelivery = %d, want 1", n)
	}
}

func TestResultSink_NonTerminalPathsNeverCall(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	retry := scriptedOutcome{result: submission.Retryable{Err: errors.New("503")}, evidence: submission.Evidence{ReachedWire: true}}
	pending := scriptedOutcome{result: submission.Pending{Ref: "r1", PollAfter: time.Now().Add(time.Hour)}, evidence: submission.Evidence{ReachedWire: true}}
	cases := []struct {
		name  string
		setup func(tenantID, invoiceID string, w *submission.SubmitWorker)
		out   scriptedOutcome
	}{
		{"pending", nil, pending},
		{"mid-budget retryable", nil, retry},
		{"already cleared", func(tn, inv string, _ *submission.SubmitWorker) { seedSiblingJob(t, f, tn, inv, "accepted") }, acceptedOnce()},
		{"content changed", func(_, _ string, w *submission.SubmitWorker) { w.InvoicePort = testInvoicePort{contentChanged: true} }, acceptedOnce()},
		{"rate limited", func(_, _ string, w *submission.SubmitWorker) { w.RateLimit = 0 }, acceptedOnce()},
		{"locked", func(tn, _ string, _ *submission.SubmitWorker) { setWorkspaceLock(t, f, tn, true) }, acceptedOnce()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
			defer cleanup()
			sink := &recSink{}
			a := newScriptedAdapter(c.out)
			w := rsSubmitWorker(f, a, sink, nil)
			if c.setup != nil {
				c.setup(tenantID, invoiceID, w)
			}
			args := submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: "req-" + uuid.NewString() + ":" + invoiceID}
			_ = w.Work(ctx, newSubmitJob(1, 1, 8, args))
			if c.name == "pending" {
				// the poll hop that stays pending
				wj := wjRequire(t, f, tenantID, args.IdempotencyKey)
				a.pollQueue = []scriptedOutcome{pending}
				pw := newTestPollWorker(f.app, a)
				pw.Results = sink
				_ = pw.Work(ctx, newPollJob(2, 1, 8, submission.PollArgs{TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: wj.id, Sequence: 1}))
			}
			if n := sink.count(); n != 0 {
				t.Errorf("sink calls = %d, want 0", n)
			}
		})
	}
}

func TestResultSink_NilSinkChangesNothing(t *testing.T) {
	f := requireExchangeDB(t)
	tenantID, invoiceID, _, err := rsDrive(t, f, 0, nil, nil, 1)
	if err != nil {
		t.Fatalf("Work = %v, want nil", err)
	}
	if s := wiRead(t, f, tenantID, invoiceID).status; s != "accepted" {
		t.Errorf("invoice status = %q, want accepted", s)
	}
	if n := auditCount(t, f, tenantID, "submission.accepted"); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}
}

// Every closure that marks a result calls reachResult once after the Mark call and ends in its audit return.
func TestResultSink_EveryTerminalClosureReachesTheSinkBeforeItsAudit(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "worker.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	marks := map[string]bool{"MarkAccepted": true, "MarkRejected": true, "MarkFailed": true}
	var matches, reaching int
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.FuncLit)
		if !ok {
			return true
		}
		var markPos, reachPos []token.Pos
		ast.Inspect(lit.Body, func(m ast.Node) bool {
			if _, nested := m.(*ast.FuncLit); nested {
				return false
			}
			c, ok := m.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := c.Fun.(type) {
			case *ast.SelectorExpr:
				if marks[fn.Sel.Name] {
					markPos = append(markPos, c.Pos())
				}
			case *ast.Ident:
				if fn.Name == "reachResult" {
					reachPos = append(reachPos, c.Pos())
				}
			}
			return true
		})
		if len(reachPos) > 0 {
			reaching++
		}
		if len(markPos) == 0 {
			return true
		}
		matches++
		if len(markPos) != 1 || len(reachPos) != 1 || reachPos[0] < markPos[0] {
			t.Errorf("closure at %s: %d Mark call(s), %d reachResult call(s), want one each in that order", fset.Position(lit.Pos()), len(markPos), len(reachPos))
			return true
		}
		last, ok := lit.Body.List[len(lit.Body.List)-1].(*ast.ReturnStmt)
		var name string
		if ok && len(last.Results) == 1 {
			if c, isCall := last.Results[0].(*ast.CallExpr); isCall {
				if id, isID := c.Fun.(*ast.Ident); isID {
					name = id.Name
				}
			}
		}
		if name != "recordVerdictAudit" && name != "recordFailureAudit" {
			t.Errorf("closure at %s does not end in its audit return", fset.Position(lit.Pos()))
		}
		if reachPos[0] > last.Pos() {
			t.Errorf("closure at %s reaches the sink after its audit return", fset.Position(lit.Pos()))
		}
		return true
	})
	if matches != 7 || reaching != 7 {
		t.Errorf("closures marking a result = %d, closures calling reachResult = %d, want 7 and 7", matches, reaching)
	}
}
