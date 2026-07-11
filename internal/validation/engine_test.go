// M3-04-02 (Test-first: yes) -- pipeline/dispatch tests for the engine core
// (Engine.Evaluate + resolvePath), authored BEFORE the pipeline exists
// (RALPH Phase 3.5 / QA Mode A). Both Evaluate and resolvePath currently
// panic("validation: not implemented") (see engine.go's STUB NOTICE), so
// this whole suite is RED until M3-04-02's implementation lands -- fixtures
// only, no real Nigerian rule content (that's M3-05; see story Out of
// Scope).
//
// Coverage (see M3-04-02 Test Specs):
//  1. TestEngine_StampsVersion       -- Result.RuleSetVersion == rs.Version; empty Violations is [] not nil.
//  2. TestEngine_CollectAllNotFailFast -- 3 applicable failing rules => all 3 violations returned (not fail-fast).
//  3. TestEngine_SkipsDisabled       -- Enabled=false rules never reach Eval (select-stage).
//  4. TestEngine_WhenGuardSkips      -- guard=false skips the rule; guard=true still collects it (non-vacuous).
//  5. TestEngine_DeterministicOrder  -- violations sorted by rule key ascending, stable across repeat runs.
//  6. TestEngine_UnknownTypeErrors   -- a RuleType absent from the registry is a non-nil error, zero-value Result.
//  7. TestPath_ResolveDotted         -- present/missing/through-non-map dotted-path resolution.
//
// Go's testing package does not isolate a panic to the single test that
// raised it -- an unrecovered panic in any goroutine crashes the whole test
// binary, so a later test in this file would silently never run. mustEvaluate
// / mustResolvePath below recover the current STUB panic and turn it into an
// ordinary t.Fatalf, so each test below fails independently and legibly
// during this RED phase; once the executor implements Evaluate/resolvePath
// for real, these helpers are simple no-op pass-throughs (no more panics to
// recover).
package validation

import "testing"

// fakeEval is a static Evaluator: every call returns the same (v, err),
// independent of the Rule or Payload passed in. Registering distinct
// fakeEval instances under distinct RuleTypes lets a single Evaluate call
// exercise several rules whose outcomes are independently controlled by the
// test.
type fakeEval struct {
	v   *Violation
	err error
}

func (f fakeEval) Eval(p Payload, r Rule) (*Violation, error) {
	return f.v, f.err
}

// newFakeGuard returns a GuardFunc that ignores its arguments and always
// reports (result, err) -- used by every test below except
// TestEngine_WhenGuardSkips, which varies the guard's result per sub-case.
func newFakeGuard(result bool, err error) GuardFunc {
	return func(expr string, p Payload) (bool, error) {
		return result, err
	}
}

// emptyInvoicePayload is a placeholder Payload for tests that don't resolve
// any target against the invoice body -- the fixture rules below use fake
// Evaluators that ignore p entirely.
func emptyInvoicePayload() Payload {
	return Payload{"invoice": map[string]any{}}
}

// mustEvaluate calls e.Evaluate and recovers Evaluate's current
// "not implemented" STUB panic into a t.Fatalf (see file-header comment for
// why this recovery is necessary at all). Once Evaluate is implemented for
// real, this is a plain pass-through.
func mustEvaluate(t *testing.T, e *Engine, p Payload, rs RuleSet) (Result, error) {
	t.Helper()
	var (
		result Result
		err    error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Engine.Evaluate panicked (pre-implementation STUB): %v", r)
			}
		}()
		result, err = e.Evaluate(p, rs)
	}()
	return result, err
}

// mustResolvePath calls resolvePath and recovers its current
// "not implemented" STUB panic into a t.Fatalf, mirroring mustEvaluate
// above.
func mustResolvePath(t *testing.T, p Payload, dotted string) (any, bool) {
	t.Helper()
	var (
		value   any
		present bool
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("resolvePath panicked (pre-implementation STUB): %v", r)
			}
		}()
		value, present = resolvePath(p, dotted)
	}()
	return value, present
}

// TestEngine_StampsVersion (Test Spec #1): a RuleSet with 0 rules still
// stamps Result.RuleSetVersion with rs.Version, and Violations is an empty
// non-nil slice (not nil) -- the wire shape must marshal as
// "violations":[], never "violations":null.
func TestEngine_StampsVersion(t *testing.T) {
	e := NewEngine(map[RuleType]Evaluator{}, newFakeGuard(true, nil))

	got, err := mustEvaluate(t, e, emptyInvoicePayload(), RuleSet{Version: 7})
	if err != nil {
		t.Fatalf("Evaluate() unexpected error: %v", err)
	}
	if got.RuleSetVersion != 7 {
		t.Errorf("RuleSetVersion = %d, want 7", got.RuleSetVersion)
	}
	if got.Violations == nil {
		t.Error("Violations = nil, want an empty non-nil slice ([] on the wire, not null)")
	}
	if len(got.Violations) != 0 {
		t.Errorf("len(Violations) = %d, want 0 for an empty RuleSet", len(got.Violations))
	}
}

// TestEngine_CollectAllNotFailFast (Test Spec #2): 3 enabled, applicable
// rules whose fake Evaluators each report a distinct violation must ALL
// come back -- proving evaluate-ALL / collect-all semantics (story Core AC
// #4), not fail-fast-on-first-violation.
func TestEngine_CollectAllNotFailFast(t *testing.T) {
	const (
		typeA RuleType = "fake-collect-a"
		typeB RuleType = "fake-collect-b"
		typeC RuleType = "fake-collect-c"
	)
	rules := []Rule{
		{Key: "rule-a", Type: typeA, Severity: "error", Message: "a failed", Scope: "document", Enabled: true},
		{Key: "rule-b", Type: typeB, Severity: "warning", Message: "b failed", Scope: "document", Enabled: true},
		{Key: "rule-c", Type: typeC, Severity: "info", Message: "c failed", Scope: "document", Enabled: true},
	}
	registry := map[RuleType]Evaluator{
		typeA: fakeEval{v: &Violation{RuleKey: "rule-a", Severity: "error", Message: "a failed"}},
		typeB: fakeEval{v: &Violation{RuleKey: "rule-b", Severity: "warning", Message: "b failed"}},
		typeC: fakeEval{v: &Violation{RuleKey: "rule-c", Severity: "info", Message: "c failed"}},
	}
	e := NewEngine(registry, newFakeGuard(true, nil))

	got, err := mustEvaluate(t, e, emptyInvoicePayload(), RuleSet{Version: 1, Rules: rules})
	if err != nil {
		t.Fatalf("Evaluate() unexpected error: %v", err)
	}
	if len(got.Violations) != 3 {
		t.Fatalf("len(Violations) = %d, want 3 (collect-ALL: every applicable rule's violation must be returned, not just the first)", len(got.Violations))
	}

	want := map[string]Violation{
		"rule-a": {RuleKey: "rule-a", Severity: "error", Message: "a failed"},
		"rule-b": {RuleKey: "rule-b", Severity: "warning", Message: "b failed"},
		"rule-c": {RuleKey: "rule-c", Severity: "info", Message: "c failed"},
	}
	for _, v := range got.Violations {
		wantV, ok := want[v.RuleKey]
		if !ok {
			t.Errorf("unexpected violation for rule key %q: %+v", v.RuleKey, v)
			continue
		}
		if v != wantV {
			t.Errorf("violation for rule key %q = %+v, want %+v", v.RuleKey, v, wantV)
		}
		delete(want, v.RuleKey)
	}
	if len(want) != 0 {
		t.Errorf("missing violations for rule keys: %v", want)
	}
}

// TestEngine_SkipsDisabled (Test Spec #3): a disabled rule and an enabled
// rule share the SAME fake Evaluator (which unconditionally reports a
// violation). Because the fake cannot itself distinguish which rule invoked
// it, the count is the only signal available -- and it is a complete one:
// 0 violations would mean the enabled rule was ALSO wrongly skipped, 2
// would mean the disabled rule was wrongly evaluated, so exactly 1 is only
// reachable by "the disabled rule never called Eval, the enabled rule did"
// -- select-stage skip of Enabled=false (story Core AC #4/#6).
func TestEngine_SkipsDisabled(t *testing.T) {
	const fakeType RuleType = "fake-disabled-probe"
	registry := map[RuleType]Evaluator{
		fakeType: fakeEval{v: &Violation{RuleKey: "would-violate", Severity: "error", Message: "should never run for the disabled rule"}},
	}
	rules := []Rule{
		{Key: "disabled-rule", Type: fakeType, Severity: "error", Message: "disabled", Scope: "document", Enabled: false},
		{Key: "enabled-rule", Type: fakeType, Severity: "error", Message: "enabled", Scope: "document", Enabled: true},
	}
	e := NewEngine(registry, newFakeGuard(true, nil))

	got, err := mustEvaluate(t, e, emptyInvoicePayload(), RuleSet{Version: 1, Rules: rules})
	if err != nil {
		t.Fatalf("Evaluate() unexpected error: %v", err)
	}
	if len(got.Violations) != 1 {
		t.Fatalf("len(Violations) = %d, want exactly 1 -- the disabled rule must be skipped at select-stage, the enabled rule must still be evaluated", len(got.Violations))
	}
}

// TestEngine_WhenGuardSkips (Test Spec #4): a single rule carries a
// non-nil When guard expression. When the injected GuardFunc reports false
// for it, the rule is select-stage-skipped (0 violations) even though its
// Evaluator would otherwise report one. The second sub-case flips the
// guard to true and asserts the SAME rule IS collected -- pairing the
// negative assertion with a positive one so this test cannot pass
// vacuously (e.g. by a stub that always returns 0 violations).
func TestEngine_WhenGuardSkips(t *testing.T) {
	const fakeType RuleType = "fake-when-probe"
	guardExpr := "invoice.country == 'NG'"
	registry := map[RuleType]Evaluator{
		fakeType: fakeEval{v: &Violation{RuleKey: "guarded-rule", Severity: "error", Message: "guarded rule fired"}},
	}
	rule := Rule{
		Key: "guarded-rule", Type: fakeType, Severity: "error", Message: "guarded rule fired",
		Scope: "document", Enabled: true, When: &guardExpr,
	}

	t.Run("guard false skips the rule", func(t *testing.T) {
		e := NewEngine(registry, newFakeGuard(false, nil))
		got, err := mustEvaluate(t, e, emptyInvoicePayload(), RuleSet{Version: 1, Rules: []Rule{rule}})
		if err != nil {
			t.Fatalf("Evaluate() unexpected error: %v", err)
		}
		if len(got.Violations) != 0 {
			t.Fatalf("len(Violations) = %d, want 0 -- guard=false must select-stage-skip the rule, regardless of what its Evaluator would report", len(got.Violations))
		}
	})

	t.Run("guard true still collects the rule", func(t *testing.T) {
		e := NewEngine(registry, newFakeGuard(true, nil))
		got, err := mustEvaluate(t, e, emptyInvoicePayload(), RuleSet{Version: 1, Rules: []Rule{rule}})
		if err != nil {
			t.Fatalf("Evaluate() unexpected error: %v", err)
		}
		if len(got.Violations) != 1 {
			t.Fatalf("len(Violations) = %d, want 1 -- guard=true must NOT skip the rule (this is the positive counterpart proving the false-case above isn't vacuous)", len(got.Violations))
		}
	})
}

// TestEngine_DeterministicOrder (Test Spec #5): three rules keyed "b", "a",
// "c" (deliberately out of order), all applicable and all violating, must
// come back sorted by rule key ascending -- "a", "b", "c" -- and Evaluate
// run twice against the identical input must produce the identical order
// both times (Decision N16 -- stable output for the M3-10 golden suite).
func TestEngine_DeterministicOrder(t *testing.T) {
	const (
		typeB RuleType = "fake-order-b"
		typeA RuleType = "fake-order-a"
		typeC RuleType = "fake-order-c"
	)
	rules := []Rule{
		{Key: "b", Type: typeB, Severity: "error", Message: "b failed", Scope: "document", Enabled: true},
		{Key: "a", Type: typeA, Severity: "error", Message: "a failed", Scope: "document", Enabled: true},
		{Key: "c", Type: typeC, Severity: "error", Message: "c failed", Scope: "document", Enabled: true},
	}
	registry := map[RuleType]Evaluator{
		typeB: fakeEval{v: &Violation{RuleKey: "b", Severity: "error", Message: "b failed"}},
		typeA: fakeEval{v: &Violation{RuleKey: "a", Severity: "error", Message: "a failed"}},
		typeC: fakeEval{v: &Violation{RuleKey: "c", Severity: "error", Message: "c failed"}},
	}
	e := NewEngine(registry, newFakeGuard(true, nil))
	rs := RuleSet{Version: 1, Rules: rules}
	payload := emptyInvoicePayload()
	wantOrder := []string{"a", "b", "c"}

	first, err := mustEvaluate(t, e, payload, rs)
	if err != nil {
		t.Fatalf("Evaluate() [first run] unexpected error: %v", err)
	}
	assertRuleKeyOrder(t, first.Violations, wantOrder)

	second, err := mustEvaluate(t, e, payload, rs)
	if err != nil {
		t.Fatalf("Evaluate() [second run] unexpected error: %v", err)
	}
	assertRuleKeyOrder(t, second.Violations, wantOrder)
}

func assertRuleKeyOrder(t *testing.T, violations []Violation, want []string) {
	t.Helper()
	if len(violations) != len(want) {
		t.Fatalf("len(Violations) = %d, want %d", len(violations), len(want))
	}
	got := make([]string, len(violations))
	for i, v := range violations {
		got[i] = v.RuleKey
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Violations rule-key order = %v, want %v (sorted ascending by rule key)", got, want)
		}
	}
}

// TestEngine_UnknownTypeErrors (Test Spec #6): a rule whose Type is absent
// from the registry is an engine/config fault, not a silently-skipped or
// silently-passed rule (Decision N15). Evaluate must return a non-nil error
// AND the zero-value Result -- never a partial Result alongside the error.
func TestEngine_UnknownTypeErrors(t *testing.T) {
	e := NewEngine(map[RuleType]Evaluator{}, newFakeGuard(true, nil)) // empty registry: every type is "unknown"
	rules := []Rule{
		{Key: "unregistered-rule", Type: RuleType("not-a-real-type"), Severity: "error", Message: "x", Scope: "document", Enabled: true},
	}

	got, err := mustEvaluate(t, e, emptyInvoicePayload(), RuleSet{Version: 9, Rules: rules})
	if err == nil {
		t.Fatalf("Evaluate() with an unregistered rule type: want a non-nil error, got nil (result=%+v) -- an unknown type must fail loud, never silently pass (Decision N15)", got)
	}
	if got.RuleSetVersion != 0 || len(got.Violations) != 0 {
		t.Errorf("Evaluate() returned a non-zero-value Result (%+v) alongside the error -- want the zero Result, not a partial one", got)
	}
}

// TestPath_ResolveDotted (Test Spec #7): resolvePath resolves a dotted
// target against the invoice object rooted at p["invoice"] -- NO
// "invoice." prefix (Decision N19). Covers a present nested path, a
// missing sibling key, and a path that walks through a non-map value.
func TestPath_ResolveDotted(t *testing.T) {
	payload := Payload{
		"invoice": map[string]any{
			"supplier": map[string]any{
				"tin": "x",
			},
		},
	}

	t.Run("present nested path resolves", func(t *testing.T) {
		got, present := mustResolvePath(t, payload, "supplier.tin")
		if !present {
			t.Fatal(`resolvePath(payload, "supplier.tin") present = false, want true`)
		}
		if got != "x" {
			t.Errorf(`resolvePath(payload, "supplier.tin") = %v, want "x"`, got)
		}
	})

	t.Run("missing sibling key is absent", func(t *testing.T) {
		got, present := mustResolvePath(t, payload, "supplier.vat")
		if present {
			t.Errorf(`resolvePath(payload, "supplier.vat") present = true (value=%v), want false -- key does not exist`, got)
		}
	})

	t.Run("path through a non-map value is absent", func(t *testing.T) {
		// "supplier.tin" resolves to the string "x" -- walking one more
		// segment past a non-map value must report absent, not panic and
		// not falsely resolve to some zero value.
		got, present := mustResolvePath(t, payload, "supplier.tin.extra")
		if present {
			t.Errorf(`resolvePath(payload, "supplier.tin.extra") present = true (value=%v), want false -- "tin" is a string, not a map`, got)
		}
	})
}
