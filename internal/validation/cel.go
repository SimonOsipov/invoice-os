// cel.go holds the `type: cel` Evaluator and the GuardFunc backend for a
// rule's `when` clause. The CEL activation binds one top-level variable,
// "invoice", to p["invoice"], so every expression references it with the
// "invoice." prefix (Decision N19), unlike the parameterized evaluators.
// A compile fault, an eval fault or a non-bool result is a config fault
// (Decision N15), never a violation. A false element-wise rule reports one
// violation per failing line via evalLines (D3/D4).
package validation

import (
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/parser"
)

// evalCELBool compiles and evaluates a CEL expression against a payload and
// requires a bool result. The activation binds the single top-level variable
// "invoice" to p["invoice"] (Decision N19 -- every CEL expression references
// invoice via the "invoice."-prefixed form). A compile error, an eval error,
// or a non-bool result type are all engine/config faults (Decision N15: fail
// loud, never a silent pass) surfaced as a non-nil error -- the result is
// never coerced. Shared verbatim by celEvaluator.Eval and celGuard so the
// `type: cel` rule's expr and any rule's `when` guard compile+evaluate
// identically.
func evalCELBool(expr string, p Payload) (bool, error) {
	env, err := cel.NewEnv(cel.Variable("invoice", cel.DynType))
	if err != nil {
		return false, fmt.Errorf("validation: cel env: %w", err)
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return false, fmt.Errorf("validation: cel compile %q: %w", expr, iss.Err())
	}
	prg, err := env.Program(ast)
	if err != nil {
		return false, fmt.Errorf("validation: cel program %q: %w", expr, err)
	}
	out, _, err := prg.Eval(map[string]any{"invoice": p["invoice"]})
	if err != nil {
		return false, fmt.Errorf("validation: cel eval %q: %w", expr, err)
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("validation: cel expression %q did not evaluate to bool (got %T)", expr, out.Value())
	}
	return b, nil
}

// compileCEL compiles expr in the evalCELBool environment without evaluating it.
func compileCEL(expr string) error {
	env, err := cel.NewEnv(cel.Variable("invoice", cel.DynType))
	if err != nil {
		return fmt.Errorf("validation: cel env: %w", err)
	}
	if _, iss := env.Compile(expr); iss != nil && iss.Err() != nil {
		return fmt.Errorf("validation: cel compile %q: %w", expr, iss.Err())
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

func (celEvaluator) evalLines(p Payload, r Rule) ([]Violation, error) {
	var params struct {
		Expr string `json:"expr"`
	}
	if err := decodeParams(r.Params, &params); err != nil {
		return nil, fmt.Errorf("validation: cel rule %q params: %w", r.Key, err)
	}
	if params.Expr == "" {
		return nil, fmt.Errorf("validation: cel rule %q: empty expr", r.Key)
	}
	ok, err := evalCELBool(params.Expr, p)
	if err != nil {
		return nil, fmt.Errorf("validation: cel rule %q: %w", r.Key, err)
	}
	if ok {
		return nil, nil
	}
	// ceiling: one violation per failing line, unbounded below the batch body cap; cap it with a measure of the import report size
	if vs := celLineViolations(params.Expr, p, r); len(vs) > 0 {
		return vs, nil
	}
	return []Violation{*violation(r)}, nil
}

// celLineViolations returns one violation per element whose body is false, or
// nil when the expression is not element-wise or no element is blamed. An
// element whose body errors is not blamed (P13: CEL absorbs it in the whole).
func celLineViolations(expr string, p Payload, r Rule) []Violation {
	if r.Target == "" {
		return nil
	}
	v, body, ok := elementWiseBody(expr, r.Target)
	if !ok {
		return nil
	}
	env, err := cel.NewEnv(cel.Variable(v, cel.DynType))
	if err != nil {
		return nil
	}
	a, iss := env.Compile(body)
	if iss != nil && iss.Err() != nil {
		return nil
	}
	prg, err := env.Program(a)
	if err != nil {
		return nil
	}
	raw, present := resolvePath(p, r.Target)
	if !present {
		return nil
	}
	els, isList := raw.([]any)
	if !isList {
		return nil
	}
	var out []Violation
	for i, el := range els {
		res, _, err := prg.Eval(map[string]any{v: el})
		if err != nil {
			continue
		}
		if b, isBool := res.Value().(bool); isBool && !b {
			out = append(out, *violation(r))
			out[len(out)-1].Path = linePath(r.Target, i+1, "")
		}
	}
	return out
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
func celGuard(expr string, p Payload) (bool, error) {
	return evalCELBool(expr, p)
}
