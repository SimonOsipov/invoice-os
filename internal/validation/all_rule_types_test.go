// This file (all_rule_types_test.go) is the M3-10-04 subtask: a
// table-driven proof that ALL NINE RuleType constants (rule.go) are covered
// by at least one passing + one violating case, split across two functions
// by how their fixture data is sourced:
//
//   - TestAllRuleTypes ("seeded-6": required, format/regex, enum, range,
//     tax_math, cel) drives a representative rule from the migration-seeded
//     v1 rule set (seed_test.go's loadV1) through the real engine. It is
//     DB-backed and self-skips without DATABASE_URL/DATABASE_SUPERUSER_URL
//     (dbTestPools, schema_test.go).
//
//   - TestAbsentRuleTypes ("absent-3": cross_field, conditional, date -- the
//     three types with no representative in the v1 seed) drives an
//     in-memory fixture RuleSet literal through NewDefaultEngine().Evaluate
//     directly. Pure Go, no DB: this function must run and pass in BOTH the
//     `go` and `rls` CI jobs, so it must never call dbTestPools/loadV1.
//
// A final coverage subtest in TestAbsentRuleTypes asserts the union of
// typeLabels across both tables is exactly the nine RuleType consts --
// guarding against either table silently losing a case (mirroring
// registry_test.go's "if a new RuleType is added, it must be added here
// too" comment).
package validation

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// seededTypeCase pairs a RuleType label with a representative rule KEY from
// the migration-seeded v1 rule set (see seed_test.go's loadV1 / the wantKeys
// list in TestSeed_ActiveVersionLoads) and a mutation that flips a fresh
// validInvoicePayload() from passing to violating that rule.
type seededTypeCase struct {
	typeLabel string
	ruleKey   string
	mutate    func(inv map[string]any)
}

// seededRuleTypeCases is the seeded-6 table. Every mutate func operates on
// invoiceOf(p) in place; the unmodified validInvoicePayload() is the shared
// "pass" fixture for all six (TestSeed_DemoContract already proves it fires
// zero violations against the real v1 rule set).
func seededRuleTypeCases() []seededTypeCase {
	return []seededTypeCase{
		{
			typeLabel: string(TypeRequired),
			ruleKey:   "supplier-tin-required",
			mutate: func(inv map[string]any) {
				delete(inv["supplier"].(map[string]any), "tin")
			},
		},
		{
			typeLabel: string(TypeFormat),
			ruleKey:   "supplier-tin-format",
			mutate: func(inv map[string]any) {
				inv["supplier"].(map[string]any)["tin"] = "BADTIN"
			},
		},
		{
			typeLabel: string(TypeEnum),
			ruleKey:   "currency-allowed",
			mutate: func(inv map[string]any) {
				inv["currency"] = "USD"
			},
		},
		{
			typeLabel: string(TypeRange),
			ruleKey:   "subtotal-non-negative",
			mutate: func(inv map[string]any) {
				inv["subtotal"] = -5.0
			},
		},
		{
			typeLabel: string(TypeTaxMath),
			ruleKey:   "vat-standard-rate",
			mutate: func(inv map[string]any) {
				inv["vat"] = 70.0 // expected 75.00 for subtotal 1000 -- well outside tolerance.
			},
		},
		{
			typeLabel: string(TypeCEL),
			ruleKey:   "no-duplicate-line-items",
			mutate: func(inv map[string]any) {
				items, _ := inv["line_items"].([]any)
				inv["line_items"] = append(items, map[string]any{
					"id": "1", "description": "Widget (dup)", "quantity": 1.0, "unit_price": 5.0, "line_total": 5.0,
				})
			},
		},
	}
}

// TestAllRuleTypes (Core AC 4, seeded-6 half): each of required, format/
// regex, enum, range, tax_math, cel is exercised through a representative
// migration-seeded v1 rule via loadV1 + the real engine -- a fully valid
// payload passes it, and the table's mutation makes it violate. Self-skips
// (via dbTestPools) without DATABASE_URL/DATABASE_SUPERUSER_URL.
func TestAllRuleTypes(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadV1(t, app)
	engine := NewDefaultEngine()

	for _, tc := range seededRuleTypeCases() {
		t.Run(tc.typeLabel, func(t *testing.T) {
			t.Run("pass", func(t *testing.T) {
				result, err := engine.Evaluate(validInvoicePayload(), rs)
				if err != nil {
					t.Fatalf("Evaluate(valid payload): %v", err)
				}
				if hasViolation(result, tc.ruleKey) {
					t.Errorf("%s fired on a fully valid payload -- violations=%+v", tc.ruleKey, result.Violations)
				}
			})
			t.Run("violate", func(t *testing.T) {
				p := validInvoicePayload()
				tc.mutate(invoiceOf(p))
				result, err := engine.Evaluate(p, rs)
				if err != nil {
					t.Fatalf("Evaluate(mutated payload): %v", err)
				}
				if !hasViolation(result, tc.ruleKey) {
					t.Errorf("%s did not fire for the mutated payload -- violations=%+v", tc.ruleKey, result.Violations)
				}
			})
		})
	}
}

// absentTypeCase pairs a RuleType label with an in-memory fixture Rule (no
// seeded counterpart exists for these three types) and the pass/violate
// payload builders that drive it.
type absentTypeCase struct {
	typeLabel      string
	rule           Rule
	passPayload    func() Payload
	violatePayload func() Payload
}

// absentRuleTypeCases is the absent-3 table: cross_field, conditional, date.
// Every Rule is Scope:"document", Enabled:true, Severity:"error", When:nil
// (zero value) -- the same shape the seeded rules carry. Param shapes are
// pinned against evaluators_math.go (cross_field/conditional) and
// evaluators.go (date); paths are relative to the invoice object, no
// "invoice." prefix (resolvePath, engine.go).
func absentRuleTypeCases() []absentTypeCase {
	return []absentTypeCase{
		{
			typeLabel: string(TypeCrossField),
			rule: Rule{
				Key:    "fixture-cross-field-total-ge-subtotal",
				Type:   TypeCrossField,
				Target: "",
				Params: json.RawMessage(`{"left":"total","op":"ge","right":"subtotal"}`),

				Severity: "error",
				Message:  "total must be >= subtotal",
				Scope:    "document",
				Enabled:  true,
			},
			passPayload: func() Payload {
				return validInvoicePayload() // total 1075.0 >= subtotal 1000.0
			},
			violatePayload: func() Payload {
				p := validInvoicePayload()
				invoiceOf(p)["subtotal"] = 2000.0 // total 1075.0 < subtotal 2000.0
				return p
			},
		},
		{
			typeLabel: string(TypeConditional),
			rule: Rule{
				Key:    "fixture-conditional-ngn-requires-supplier-tin",
				Type:   TypeConditional,
				Target: "",
				Params: json.RawMessage(`{"if":{"field":"currency","op":"eq","value":"NGN"},"then":{"field":"supplier.tin","required":true}}`),

				Severity: "error",
				Message:  "NGN invoices require supplier.tin",
				Scope:    "document",
				Enabled:  true,
			},
			passPayload: func() Payload {
				return validInvoicePayload() // currency NGN, supplier.tin present
			},
			violatePayload: func() Payload {
				p := validInvoicePayload()
				delete(invoiceOf(p)["supplier"].(map[string]any), "tin")
				return p
			},
		},
		{
			typeLabel: string(TypeDate),
			rule: Rule{
				Key:    "fixture-date-issue-not-after-today",
				Type:   TypeDate,
				Target: "issue_date",
				Params: json.RawMessage(`{"not_after":"today"}`),

				Severity: "error",
				Message:  "issue date must not be in the future",
				Scope:    "document",
				Enabled:  true,
			},
			passPayload: func() Payload {
				p := validInvoicePayload()
				invoiceOf(p)["issue_date"] = "2020-01-01" // fixed, always before "today"
				return p
			},
			violatePayload: func() Payload {
				p := validInvoicePayload()
				invoiceOf(p)["issue_date"] = "2999-12-31" // fixed, always after "today"
				return p
			},
		},
	}
}

// TestAbsentRuleTypes (Core AC 4, absent-3 half): each of cross_field,
// conditional, date is exercised through an in-memory fixture RuleSet
// literal, entirely without a DB connection -- must run and pass in both the
// `go` and `rls` CI jobs. A trailing coverage subtest asserts the seeded-6 +
// absent-3 tables together name all nine RuleType consts.
func TestAbsentRuleTypes(t *testing.T) {
	engine := NewDefaultEngine()
	cases := absentRuleTypeCases()

	for _, tc := range cases {
		t.Run(tc.typeLabel, func(t *testing.T) {
			rs := RuleSet{Version: 1, Rules: []Rule{tc.rule}}

			t.Run("pass", func(t *testing.T) {
				result, err := engine.Evaluate(tc.passPayload(), rs)
				if err != nil {
					t.Fatalf("Evaluate(pass payload): %v", err)
				}
				if hasViolation(result, tc.rule.Key) {
					t.Errorf("%s fired on the pass fixture -- violations=%+v", tc.rule.Key, result.Violations)
				}
			})
			t.Run("violate", func(t *testing.T) {
				result, err := engine.Evaluate(tc.violatePayload(), rs)
				if err != nil {
					t.Fatalf("Evaluate(violate payload): %v", err)
				}
				if !hasViolation(result, tc.rule.Key) {
					t.Errorf("%s did not fire on the violate fixture -- violations=%+v", tc.rule.Key, result.Violations)
				}
			})
		})
	}

	t.Run("coverage: seeded-6 + absent-3 together name all nine rule types", func(t *testing.T) {
		got := make([]string, 0, 9)
		for _, tc := range seededRuleTypeCases() {
			got = append(got, tc.typeLabel)
		}
		for _, tc := range cases {
			got = append(got, tc.typeLabel)
		}
		sort.Strings(got)

		want := []string{
			string(TypeRequired), string(TypeFormat), string(TypeEnum), string(TypeRange),
			string(TypeTaxMath), string(TypeCrossField), string(TypeConditional), string(TypeDate), string(TypeCEL),
		}
		sort.Strings(want)

		if !reflect.DeepEqual(got, want) {
			t.Errorf("rule types covered = %v, want %v (all nine RuleType consts, each exercised exactly once)", got, want)
		}
	})
}
