// cel.go holds the `type: cel` Evaluator and the GuardFunc backend for a
// rule's `when` clause. The CEL activation binds one top-level variable,
// "invoice", to p["invoice"], so every expression references it with the
// "invoice." prefix (Decision N19), unlike the parameterized evaluators.
// A compile fault, an eval fault or a non-bool result is a config fault
// (Decision N15), never a violation. A false element-wise rule reports one
// violation per failing line via evalLines (D3/D4).
package validation

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/interpreter"
	"github.com/google/cel-go/parser"
)

// celCostLimit caps one custom-rule CEL evaluation, in cel-go cost units.
const celCostLimit uint64 = 1_000_000

// celInterruptEvery is how many comprehension iterations pass between context polls.
const celInterruptEvery = 100

// errCELTooExpensive reports an evaluation that overran the cost limit.
var errCELTooExpensive = errors.New("validation: cel cost limit exceeded")

type celCostLimitKey struct{}

// withCELCostLimit makes every CEL evaluation under ctx run with a cost limit of n.
func withCELCostLimit(ctx context.Context, n uint64) context.Context {
	return context.WithValue(ctx, celCostLimitKey{}, n)
}

// celCostLimitFrom returns the limit ctx carries, 0 for none.
func celCostLimitFrom(ctx context.Context) uint64 {
	n, _ := ctx.Value(celCostLimitKey{}).(uint64)
	return n
}

func celProgramOpts(ctx context.Context) []cel.ProgramOption {
	opts := []cel.ProgramOption{cel.InterruptCheckFrequency(celInterruptEvery)}
	if n := celCostLimitFrom(ctx); n > 0 {
		opts = append(opts, cel.CostLimit(n))
	}
	return opts
}

// celEvalErr maps a cost overrun to errCELTooExpensive; other errors, context
// errors included, pass through unchanged.
func celEvalErr(err error) error {
	var ce interpreter.EvalCancelledError
	if errors.As(err, &ce) && ce.Cause == interpreter.CostLimitExceeded {
		return fmt.Errorf("%w: %w", errCELTooExpensive, err)
	}
	return err
}

// ctxErr wraps the context error once ctx has ended, else nil. cel-go polls the
// context only inside a comprehension.
func ctxErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("validation: evaluation stopped: %w", err)
	}
	return nil
}

// evalCELBool compiles and evaluates a CEL expression against a payload and
// requires a bool result. The activation binds the single top-level variable
// "invoice" to p["invoice"] (Decision N19 -- every CEL expression references
// invoice via the "invoice."-prefixed form). A compile error, an eval error,
// or a non-bool result type are all engine/config faults (Decision N15: fail
// loud, never a silent pass) surfaced as a non-nil error -- the result is
// never coerced. Shared verbatim by celEvaluator.Eval and celGuard so the
// `type: cel` rule's expr and any rule's `when` guard compile+evaluate
// identically.
func evalCELBool(ctx context.Context, expr string, p Payload) (bool, error) {
	env, err := newCELEnv()
	if err != nil {
		return false, err
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return false, fmt.Errorf("validation: cel compile %q: %w", expr, iss.Err())
	}
	prg, err := env.Program(ast, celProgramOpts(ctx)...)
	if err != nil {
		return false, fmt.Errorf("validation: cel program %q: %w", expr, err)
	}
	out, _, err := prg.ContextEval(ctx, map[string]any{"invoice": p["invoice"]})
	if err != nil {
		return false, fmt.Errorf("validation: cel eval %q: %w", expr, celEvalErr(err))
	}
	// a comprehension stopped by the context yields an error value that && / || can absorb into a bool
	if err := ctxErr(ctx); err != nil {
		return false, err
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("validation: cel expression %q did not evaluate to bool (got %T)", expr, out.Value())
	}
	return b, nil
}

// newCELEnv is the one environment evalCELBool and compileCEL share.
func newCELEnv() (*cel.Env, error) {
	env, err := cel.NewEnv(cel.Variable("invoice", cel.DynType))
	if err != nil {
		return nil, fmt.Errorf("validation: cel env: %w", err)
	}
	return env, nil
}

// compileCEL compiles expr in the evalCELBool environment without evaluating it,
// and rejects an output type that is neither bool nor dyn.
func compileCEL(expr string) error {
	env, err := newCELEnv()
	if err != nil {
		return err
	}
	a, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return fmt.Errorf("validation: cel compile %q: %w", expr, iss.Err())
	}
	if t := a.OutputType(); !t.IsExactType(cel.BoolType) && !t.IsExactType(cel.DynType) {
		return fmt.Errorf("validation: cel expression %q yields %s, not bool", expr, t)
	}
	return nil
}

// celEvaluator implements Evaluator and lineEvaluator for `type: cel` rules:
// params {"expr": <CEL string>}. A true expr passes; false is a violation. A
// rule shaped `[!has(invoice.T) ||] invoice.T.all(v, body)` with T = r.Target
// and a body that reads only v reports one violation per false element at
// T[N]; any other false rule reports one violation at r.Target.
type celEvaluator struct{}

func (c celEvaluator) Eval(p Payload, r Rule) (*Violation, error) {
	vs, err := c.evalLines(p, r)
	if err != nil || len(vs) == 0 {
		return nil, err
	}
	return &vs[0], nil
}

func (c celEvaluator) evalLines(p Payload, r Rule) ([]Violation, error) {
	return c.evalLinesContext(context.Background(), p, r)
}

func (celEvaluator) evalLinesContext(ctx context.Context, p Payload, r Rule) ([]Violation, error) {
	var params struct {
		Expr string `json:"expr"`
	}
	if err := decodeParams(r.Params, &params); err != nil {
		return nil, fmt.Errorf("validation: cel rule %q params: %w", r.Key, err)
	}
	if params.Expr == "" {
		return nil, fmt.Errorf("validation: cel rule %q: empty expr", r.Key)
	}
	ok, err := evalCELBool(ctx, params.Expr, p)
	if err != nil {
		return nil, fmt.Errorf("validation: cel rule %q: %w", r.Key, err)
	}
	if ok {
		return nil, nil
	}
	// ceiling: one violation per failing line, unbounded below the batch body cap; cap it with a measure of the import report size
	vs, err := celLineViolations(ctx, params.Expr, p, r)
	if err != nil {
		return nil, fmt.Errorf("validation: cel rule %q: %w", r.Key, err)
	}
	if len(vs) > 0 {
		return vs, nil
	}
	return []Violation{*violation(r)}, nil
}

// celLineViolations returns one violation per element whose body is false, or
// nil when the expression is not element-wise or no element is blamed. An
// element whose body errors is not blamed (P13: CEL absorbs it in the whole),
// but an ended context is returned. Under a cost limit the elements share one
// budget; any overrun, per element or shared, stops the pass and returns nil
// (the caller reports the invoice-level violation).
func celLineViolations(ctx context.Context, expr string, p Payload, r Rule) ([]Violation, error) {
	if r.Target == "" {
		return nil, nil
	}
	v, body, ok := elementWiseBody(expr, r.Target)
	if !ok {
		return nil, nil
	}
	env, err := cel.NewEnv(cel.Variable(v, cel.DynType))
	if err != nil {
		return nil, nil
	}
	a, iss := env.Compile(body)
	if iss != nil && iss.Err() != nil {
		return nil, nil
	}
	prg, err := env.Program(a, celProgramOpts(ctx)...)
	if err != nil {
		return nil, nil
	}
	raw, present := resolvePath(p, r.Target)
	if !present {
		return nil, nil
	}
	els, isList := raw.([]any)
	if !isList {
		return nil, nil
	}
	field := judgedField(a.NativeRep().Expr(), v)
	var out []Violation
	budget := celCostLimitFrom(ctx)
	for i, el := range els {
		res, det, err := prg.ContextEval(ctx, map[string]any{v: el})
		if err != nil {
			if errors.Is(celEvalErr(err), errCELTooExpensive) {
				return nil, nil // see the budget case below
			}
			if cerr := ctxErr(ctx); cerr != nil {
				return nil, cerr
			}
			continue
		}
		if cerr := ctxErr(ctx); cerr != nil {
			return nil, cerr
		}
		if budget > 0 {
			var spent uint64
			if c := det.ActualCost(); c != nil {
				spent = *c
			}
			if spent > budget {
				// the whole expression already ran within the limit: stop blaming lines, report the invoice-level violation
				return nil, nil
			}
			budget -= spent
		}
		if b, isBool := res.Value().(bool); isBool && !b {
			out = append(out, *violation(r))
			out[len(out)-1].Path = linePath(r.Target, i+1, "")
			out[len(out)-1].Field = field
		}
	}
	return out, nil
}

// judgedField returns the one field name selected from v in e, or "" when v
// is used any other way (bare, indexed, passed to a call) or zero or several
// names are read.
func judgedField(e ast.Expr, v string) string {
	names := map[string]bool{}
	other := false
	var walk func(ast.Expr)
	walk = func(e ast.Expr) {
		switch e.Kind() {
		case ast.IdentKind:
			if e.AsIdent() == v {
				other = true
			}
		case ast.SelectKind:
			s := e.AsSelect()
			if op := s.Operand(); op.Kind() == ast.IdentKind && op.AsIdent() == v {
				names[s.FieldName()] = true
				return
			}
			walk(s.Operand())
		case ast.CallKind:
			c := e.AsCall()
			if c.IsMemberFunction() {
				walk(c.Target())
			}
			for _, a := range c.Args() {
				walk(a)
			}
		case ast.ListKind:
			for _, x := range e.AsList().Elements() {
				walk(x)
			}
		case ast.MapKind:
			for _, en := range e.AsMap().Entries() {
				me := en.AsMapEntry()
				walk(me.Key())
				walk(me.Value())
			}
		case ast.StructKind:
			for _, f := range e.AsStruct().Fields() {
				walk(f.AsStructField().Value())
			}
		case ast.ComprehensionKind:
			c := e.AsComprehension()
			for _, x := range []ast.Expr{c.IterRange(), c.AccuInit(), c.LoopCondition(), c.LoopStep(), c.Result()} {
				walk(x)
			}
		}
	}
	walk(e)
	if other || len(names) != 1 {
		return ""
	}
	for n := range names {
		return n
	}
	return ""
}

// elementWiseBody matches `[!has(invoice.T) ||] invoice.T.all(v, body)` on a
// macro-free parse and returns v and the unparsed body.
func elementWiseBody(expr, target string) (v, body string, ok bool) {
	env, err := cel.NewEnv(cel.ClearMacros(), cel.Variable("invoice", cel.DynType))
	if err != nil {
		return "", "", false
	}
	parsed, iss := env.Parse(expr)
	if iss != nil && iss.Err() != nil {
		return "", "", false
	}
	info := parsed.NativeRep().SourceInfo()
	e := parsed.NativeRep().Expr()
	if e.Kind() == ast.CallKind && e.AsCall().FunctionName() == "_||_" && len(e.AsCall().Args()) == 2 {
		guard, err := parser.Unparse(e.AsCall().Args()[0], info)
		if err != nil || guard != "!has(invoice."+target+")" {
			return "", "", false
		}
		e = e.AsCall().Args()[1]
	}
	if e.Kind() != ast.CallKind {
		return "", "", false
	}
	all := e.AsCall()
	if all.FunctionName() != "all" || !all.IsMemberFunction() || len(all.Args()) != 2 || all.Args()[0].Kind() != ast.IdentKind {
		return "", "", false
	}
	tgt, err := parser.Unparse(all.Target(), info)
	if err != nil || tgt != "invoice."+target {
		return "", "", false
	}
	body, err = parser.Unparse(all.Args()[1], info)
	if err != nil {
		return "", "", false
	}
	return all.Args()[0].AsIdent(), body, true
}

// celGuard is the production GuardFunc (rule.go's GuardFunc type) backend
// for a rule's optional `when` select-stage clause: expr is compiled and
// evaluated the same way as celEvaluator.Eval's expr (activation variable
// "invoice" bound to p["invoice"], Decision N19), but the polarity is guard
// semantics rather than violation semantics -- true => the rule is
// applicable and evaluation proceeds; false => the rule is skipped with no
// violation. A compile error or an eval error is a non-nil error (same
// engine/config-fault class as celEvaluator -- Decision N15), NOT a silent
// skip. The executor wires this as the Engine's guard (engine.go's
// NewEngine second argument) at task-47 (M3-04-08 wiring subtask).
func celGuard(ctx context.Context, expr string, p Payload) (bool, error) {
	return evalCELBool(ctx, expr, p)
}
