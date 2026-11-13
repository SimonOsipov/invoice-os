package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// listLit is the CEL literal [0,1,...,n-1].
func listLit(n int) string {
	els := make([]string, n)
	for i := range els {
		els[i] = fmt.Sprint(i)
	}
	return "[" + strings.Join(els, ",") + "]"
}

// nestedAll is one .all() per size, each over a literal list of that size.
func nestedAll(sizes ...int) string {
	vars := []string{"a", "b", "c", "d", "e"}
	body := vars[len(sizes)-1] + " >= 0"
	for i := len(sizes) - 1; i >= 0; i-- {
		body = fmt.Sprintf("%s.all(%s, %s)", listLit(sizes[i]), vars[i], body)
	}
	return body
}

func nested100(depth int) string {
	sizes := make([]int, depth)
	for i := range sizes {
		sizes[i] = 100
	}
	return nestedAll(sizes...)
}

func emptyInvoice() Payload { return Payload{"invoice": map[string]any{}} }

func celOneRule(expr string, when *string) RuleSet {
	r := celRule("k", "", expr)
	r.When = when
	return RuleSet{Rules: []Rule{r}}
}

func TestCEL_FiveNestedAllRefusedWithinBound(t *testing.T) {
	start := time.Now()
	_, err := evalCELBool(withCELCostLimit(context.Background(), celCostLimit), nested100(5), emptyInvoice())
	if !errors.Is(err, errCELTooExpensive) {
		t.Fatalf("err = %v, want errCELTooExpensive", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("refusal took %v, want under 5s", d)
	}
}

func TestCEL_DeadlineStopsUnlimitedEvaluation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := celGuard(ctx, nested100(5), emptyInvoice())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("stopped after %v, want under 2s", d)
	}
}

func cancelledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestCEL_CancelledContextStopsLinePass(t *testing.T) {
	p := linesPayload(map[string]any{}, map[string]any{}, map[string]any{})
	r := celRule("k", "line_items", "invoice.line_items.all(x, has(x.ok))")
	vs, err := celEvaluator{}.evalLinesContext(cancelledCtx(), p, r)
	if !errors.Is(err, context.Canceled) || len(vs) != 0 {
		t.Fatalf("vs=%v err=%v, want Canceled and no violations", vs, err)
	}
	// the line pass alone, without the whole-expression check in front of it
	if _, err := celLineViolations(cancelledCtx(), "invoice.line_items.all(x, has(x.ok))", p, r); !errors.Is(err, context.Canceled) {
		t.Errorf("celLineViolations err = %v, want Canceled", err)
	}
}

// endingCtx reports Canceled from its n-th Err call on.
type endingCtx struct {
	context.Context
	mu    sync.Mutex
	calls int
	n     int
	done  chan struct{}
}

func (c *endingCtx) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls < c.n {
		return nil
	}
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return context.Canceled
}

func (c *endingCtx) Done() <-chan struct{} { return c.done }

func TestCEL_ContextEndingMidLinePassStops(t *testing.T) {
	lines := make([]map[string]any, 10)
	for i := range lines {
		lines[i] = map[string]any{}
	}
	ctx := &endingCtx{Context: context.Background(), n: 4, done: make(chan struct{})}
	vs, err := celEvaluator{}.evalLinesContext(ctx, linesPayload(lines...), celRule("k", "line_items", "invoice.line_items.all(x, has(x.ok))"))
	if !errors.Is(err, context.Canceled) || len(vs) != 0 {
		t.Fatalf("got %d violations, err %v; want Canceled and none", len(vs), err)
	}
}

func TestCEL_CancelledContextStopsGuardWithoutComprehension(t *testing.T) {
	if _, err := celGuard(cancelledCtx(), "has(invoice.x)", emptyInvoice()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want Canceled", err)
	}
}

func TestEngine_EvaluateContextStopsBetweenRules(t *testing.T) {
	rs := RuleSet{Rules: []Rule{
		{Key: "a", Type: TypeRequired, Target: "x", Params: json.RawMessage(`{}`), Scope: "document", Enabled: true},
		{Key: "b", Type: TypeRequired, Target: "x", Params: json.RawMessage(`{}`), Scope: "document", Enabled: true},
		{Key: "c", Type: TypeRequired, Target: "x", Params: json.RawMessage(`{}`), Scope: "document", Enabled: true},
	}}
	res, err := NewDefaultEngine().EvaluateContext(cancelledCtx(), emptyInvoice(), rs)
	if !errors.Is(err, context.Canceled) || len(res.Violations) != 0 {
		t.Fatalf("res=%+v err=%v, want Canceled and an empty Result", res, err)
	}
}

type ctxProbeKey struct{}

func TestEngine_EvaluateContextPassesCtxToGuard(t *testing.T) {
	var got any
	guard := func(ctx context.Context, _ string, _ Payload) (bool, error) {
		got = ctx.Value(ctxProbeKey{})
		return true, nil
	}
	e := NewEngine(map[RuleType]Evaluator{TypeRequired: requiredEval{}}, guard)
	when := "true"
	rs := RuleSet{Rules: []Rule{{Key: "a", Type: TypeRequired, Target: "x", Params: json.RawMessage(`{}`), Scope: "document", Enabled: true, When: &when}}}
	ctx := context.WithValue(context.Background(), ctxProbeKey{}, "seen")
	if _, err := e.EvaluateContext(ctx, emptyInvoice(), rs); err != nil {
		t.Fatal(err)
	}
	if got != "seen" {
		t.Errorf("guard ctx value = %v, want the caller's ctx", got)
	}
}

func limited() context.Context { return withCELCostLimit(context.Background(), celCostLimit) }

func TestCEL_CostLimitOnWhen(t *testing.T) {
	when := nested100(3) + " && true"
	_, err := NewDefaultEngine().EvaluateContext(limited(), emptyInvoice(), celOneRule("true", &when))
	if !errors.Is(err, errCELTooExpensive) {
		t.Fatalf("err = %v, want errCELTooExpensive", err)
	}
}

func TestCEL_CostLimitOnExpr(t *testing.T) {
	_, err := NewDefaultEngine().EvaluateContext(limited(), emptyInvoice(), celOneRule(nested100(3), nil))
	if !errors.Is(err, errCELTooExpensive) {
		t.Fatalf("err = %v, want errCELTooExpensive", err)
	}
}

// A single element over the limit on its own, after the whole expression
// already went false within it, reports the invoice-level violation.
func TestCEL_CostLimitOnLinePass(t *testing.T) {
	expr := "invoice.line_items.all(x, !has(x.stop) && (has(x.ok) || " + nested100(3) + "))"
	p := linesPayload(map[string]any{"stop": true}, map[string]any{})
	r := celRule("k", "line_items", expr)
	vs, err := celEvaluator{}.evalLinesContext(limited(), p, r)
	if err != nil || len(vs) != 1 || vs[0].Path != r.Target {
		t.Fatalf("vs=%v err=%v, want one invoice-level violation at %q and no error", vs, err, r.Target)
	}
}

// A line pass that overruns its shared budget after the whole expression ran
// within the limit reports the invoice-level violation, not a cost fault.
func TestCEL_LinePassOverrunFallsBackToInvoiceViolation(t *testing.T) {
	expr := "invoice.line_items.all(x, !has(x.stop) && " + nested100(2) + " && has(x.ok))"
	lines := []map[string]any{{"stop": true}}
	for i := 0; i < 39; i++ {
		lines = append(lines, map[string]any{})
	}
	r := celRule("k", "line_items", expr)
	vs, err := celEvaluator{}.evalLinesContext(limited(), linesPayload(lines...), r)
	if err != nil || len(vs) != 1 || vs[0].Path != r.Target {
		t.Fatalf("vs=%v err=%v, want one invoice-level violation at %q and no error", vs, err, r.Target)
	}
}

// The per-line pass stops once the shared budget is spent: it returns no line
// violations at all instead of blaming every remaining line.
func TestCEL_LinePassSharesOneBudget(t *testing.T) {
	expr := "invoice.line_items.all(x, !has(x.stop) && " + nested100(2) + " && has(x.ok))"
	lines := []map[string]any{{"stop": true}}
	for i := 0; i < 39; i++ {
		lines = append(lines, map[string]any{})
	}
	vs, err := celLineViolations(limited(), expr, linesPayload(lines...), celRule("k", "line_items", expr))
	if err != nil || vs != nil {
		t.Fatalf("vs=%d err=%v, want the pass to stop with no violations and no error", len(vs), err)
	}
	// the same lines with an unlimited budget are all blamed
	if vs, _ := celLineViolations(context.Background(), expr, linesPayload(lines...), celRule("k", "line_items", expr)); len(vs) != 40 {
		t.Errorf("unlimited blamed %d lines, want 40", len(vs))
	}
}

// flipCtx ends on its second Err call and closes Done on the first, so cel-go
// sees an ended context inside the first comprehension it runs.
type flipCtx struct {
	context.Context
	mu    sync.Mutex
	calls int
	done  chan struct{}
}

func newFlipCtx() *flipCtx {
	return &flipCtx{Context: context.Background(), done: make(chan struct{})}
}

func (c *flipCtx) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls == 1 {
		close(c.done)
		return nil
	}
	return context.Canceled
}

func (c *flipCtx) Done() <-chan struct{} { return c.done }

func bigLinesPayload(note bool) Payload {
	lines := make([]any, 250)
	for i := range lines {
		lines[i] = map[string]any{}
	}
	inv := map[string]any{"line_items": lines}
	if note {
		inv["note"] = "x"
	}
	return Payload{"invoice": inv}
}

// An interrupted comprehension is an error value that && / || can absorb into a
// bool; the context error must win.
func TestCEL_ContextEndingInComprehensionIsNotAbsorbed(t *testing.T) {
	cases := map[string]struct {
		expr string
		note bool
	}{
		"and": {"invoice.line_items.all(x, true) && has(invoice.note)", false},
		"or":  {"invoice.line_items.all(x, true) || has(invoice.note)", true},
	}
	for name, c := range cases {
		ctx := newFlipCtx()
		_ = ctx.Err() // the first call closes Done; every later call reports Canceled
		ok, err := evalCELBool(ctx, c.expr, bigLinesPayload(c.note))
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s: ok=%v err=%v, want the context error", name, ok, err)
		}
	}
}

// The same in the per-line pass: an element body that is absorbed, or that
// errors outright, must not hide the context error.
func TestCEL_ContextEndingInLineBodyIsNotAbsorbed(t *testing.T) {
	lines := make([]any, 1)
	lines[0] = map[string]any{"inner": bigLinesPayload(false)["invoice"].(map[string]any)["line_items"]}
	p := Payload{"invoice": map[string]any{"line_items": lines}}
	for name, expr := range map[string]string{
		"absorbed": "invoice.line_items.all(x, x.inner.all(y, true) && has(x.note))",
		"errored":  "invoice.line_items.all(x, x.inner.all(y, true))",
	} {
		ctx := newFlipCtx()
		_ = ctx.Err() // the first call closes Done; the loop has no check of its own before the first element
		vs, err := celLineViolations(ctx, expr, p, celRule("k", "line_items", expr))
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s: vs=%v err=%v, want the context error", name, vs, err)
		}
	}
}

// samplePayload is the embedded sample invoice the save-time check probes with.
func samplePayload(t *testing.T) Payload {
	t.Helper()
	var p Payload
	if err := json.Unmarshal(sampleInvoiceJSON, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// manyLines is the valid invoice with n copies of its line item.
func manyLines(n int) Payload {
	p := validInvoicePayload()
	inv := invoiceOf(p)
	lines := make([]any, n)
	for i := range lines {
		lines[i] = map[string]any{"id": fmt.Sprint(i + 1), "description": "Widget", "quantity": 10.0, "unit_price": 100.0, "line_total": 1000.0}
	}
	inv["line_items"] = lines
	return p
}

// sameWithAndWithoutLimit fails unless rs gives the same Result and error presence both ways.
func sameWithAndWithoutLimit(t *testing.T, name string, p Payload, rs RuleSet) {
	t.Helper()
	eng := NewDefaultEngine()
	free, freeErr := eng.EvaluateContext(context.Background(), p, rs)
	lim, limErr := eng.EvaluateContext(limited(), p, rs)
	if (freeErr != nil) != (limErr != nil) || !reflect.DeepEqual(free, lim) {
		t.Errorf("%s: unlimited (%+v, %v) != limited (%+v, %v)", name, free, freeErr, lim, limErr)
	}
}

func TestCEL_CustomExamplesSameResultUnderLimit(t *testing.T) {
	exprs := []string{
		"has(invoice.total) ? invoice.total >= 0.0 : true",
		"invoice.total > 5",
		"invoice.supplier.tin.size() > 0",
		"has(invoice.buyer) ? invoice.buyer.type == 'B2B' : true",
		"invoice.missing.x == 1",
	}
	payloads := map[string]Payload{
		"valid": validInvoicePayload(), "bad": badInvoicePayload(), "100 lines": manyLines(100), "sample": samplePayload(t),
	}
	for _, expr := range exprs {
		for name, p := range payloads {
			sameWithAndWithoutLimit(t, expr+" on "+name, p, celOneRule(expr, nil))
		}
	}
}

// Golden rules carry no cost limit: no-duplicate-line-items is O(n^2) and a golden
// fault would fail a whole batch with a 500.
func TestCEL_GoldenRulesCarryNoCostLimit(t *testing.T) {
	lines := make([]any, 500)
	for i := range lines {
		lines[i] = map[string]any{"id": fmt.Sprint(i + 1)}
	}
	p := Payload{"invoice": map[string]any{"line_items": lines}}
	rs := RuleSet{Rules: []Rule{celRule("no-duplicate-line-items", "line_items", dupIDsExpr)}}
	eng := NewDefaultEngine()
	free, err := eng.EvaluateContext(context.Background(), p, rs)
	if err != nil {
		t.Fatalf("unlimited err = %v, want none", err)
	}
	plain, _ := eng.Evaluate(p, rs)
	if !reflect.DeepEqual(free, plain) {
		t.Errorf("EvaluateContext %+v != Evaluate %+v", free, plain)
	}
	if _, err := eng.EvaluateContext(limited(), p, rs); !errors.Is(err, errCELTooExpensive) {
		t.Errorf("limited err = %v, want errCELTooExpensive (the input must exceed the limit)", err)
	}
}

func TestCEL_EndedContextWinsOverTheCostLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(limited())
	cancel()
	_, err := NewDefaultEngine().EvaluateContext(ctx, emptyInvoice(), celOneRule(nested100(3), nil))
	if !errors.Is(err, context.Canceled) || errors.Is(err, errCELTooExpensive) {
		t.Fatalf("err = %v, want Canceled and not errCELTooExpensive", err)
	}
}

func TestCEL_CostOverrunIsNotAContextError(t *testing.T) {
	_, err := NewDefaultEngine().EvaluateContext(limited(), emptyInvoice(), celOneRule(nested100(3), nil))
	if !errors.Is(err, errCELTooExpensive) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want errCELTooExpensive only", err)
	}
}

func TestCEL_LimitBoundaryTwoNestedAcceptedThreeRefused(t *testing.T) {
	if _, err := NewDefaultEngine().EvaluateContext(limited(), emptyInvoice(), celOneRule(nested100(2), nil)); err != nil {
		t.Errorf("two nested (cost 31,411): err = %v, want none", err)
	}
	if _, err := NewDefaultEngine().EvaluateContext(limited(), emptyInvoice(), celOneRule(nested100(3), nil)); !errors.Is(err, errCELTooExpensive) {
		t.Errorf("three nested (cost 3.1M): err = %v, want errCELTooExpensive", err)
	}
}

func TestCEL_LinePassWithinBudgetStillBlamesFailingLines(t *testing.T) {
	expr := "invoice.line_items.all(x, has(x.ok) && " + nested100(2) + ")"
	vs, err := celEvaluator{}.evalLinesContext(limited(), linesPayload(map[string]any{}, map[string]any{"ok": true}, map[string]any{}), celRule("k", "line_items", expr))
	if err != nil || len(vs) != 2 {
		t.Fatalf("vs=%v err=%v, want 2 line violations and no error", vs, err)
	}
}
