// M3-10-05 (Core AC 5): golden snapshot suite pinning the seeded MBS v1
// engine's Result JSON for three representative payloads -- a byte-for-byte
// regression net that complements the ad-hoc field assertions in
// seed_test.go (TestSeed_DemoContract) and collect_all_integration_test.go
// (TestCollectAll_ManyViolationsBreadth). Reuses validInvoicePayload /
// badInvoicePayload (seed_test.go) and manyViolationsPayload
// (collect_all_integration_test.go) verbatim -- this file introduces zero
// new fixture payloads, only the golden-compare harness plus the three
// committed snapshots under testdata/golden/.
//
// A committed golden pins the exact pretty-printed Result JSON (2-space
// indent, trailing newline) for one payload against the migration-seeded
// v1 rule set. Any change to the engine, the seed migration's rule content,
// or a fixture payload that alters the Result for one of these three
// payloads reddens this suite with a readable diff, forcing a deliberate
// -update + review rather than a silent drift.
//
// Run (same env gate as the rest of the package):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -run TestGolden ./internal/validation/...
//
// Regenerate after an intentional engine/seed/fixture change (inspect the
// diff before committing -- an unexpected golden change usually means a bug,
// not a golden that needs updating):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -run TestGolden -update ./internal/validation/...
package validation

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// update, when set via -update, rewrites the golden files under
// testdata/golden/ from the engine's current output instead of comparing
// against them. Golden files are committed -- -update is for deliberate
// regeneration only, never run as part of the default test suite.
var update = flag.Bool("update", false, "rewrite golden files in testdata/golden/ instead of comparing against them")

// ruleSetVersionField matches the rule_set_version field in a pretty-printed
// Result.
var ruleSetVersionField = regexp.MustCompile(`"rule_set_version": \d+`)

// normalizeRuleSetVersion rewrites the rule_set_version field to a fixed
// placeholder, so the goldens pin the VIOLATIONS payload -- their actual
// purpose (rule keys, severities, messages, paths, and Decision N16's sort
// order) -- and not the active rule-set version, which changes on every
// version publish and is asserted ONCE, on purpose, by
// TestSeed_ActiveVersionLoads via activeSeedVersion.
//
// WHY a placeholder rather than the current number: baking the literal in
// means every version publish breaks all three goldens for a reason that has
// nothing to do with the engine's output, and the documented remedy (`-update`)
// silently re-pins them to the new literal -- re-arming the identical trap for
// the publish after that. That is the bug class
// [active-version-pinning-is-the-bug] exists to kill, and these goldens are a
// live instance of it: they are produced from loadActive (a real DB read of the
// seeded rule-set), so they are Category A under M4-04-01's triage. NOTE: the
// story's detection command does NOT find them -- in JSON the closing quote sits
// between `version` and the `:`, so `[Vv]ersion[[:space:]]*(:|...)` never
// matches. See this task's PR description; the command needs a fifth hardening.
func normalizeRuleSetVersion(b []byte) []byte {
	return ruleSetVersionField.ReplaceAll(b, []byte(`"rule_set_version": "<active>"`))
}

// TestGolden byte-compares the pretty-printed Result for each of the three
// representative payloads against its committed golden fixture, with the
// rule_set_version field normalized on both sides (see
// normalizeRuleSetVersion).
func TestGolden(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadActive(t, app)
	engine := NewDefaultEngine()

	cases := []struct {
		name    string
		payload func() Payload
	}{
		{"clean_invoice", validInvoicePayload},
		{"demo_bad_invoice", badInvoicePayload},
		{"many_violations", manyViolationsPayload},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := engine.Evaluate(tc.payload(), rs)
			if err != nil {
				t.Fatalf("Evaluate(%s): %v", tc.name, err)
			}

			got, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				t.Fatalf("MarshalIndent(%s result): %v", tc.name, err)
			}
			got = append(got, '\n')
			got = normalizeRuleSetVersion(got)

			path := filepath.Join("testdata", "golden", tc.name+".json")

			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatalf("write golden %s: %v", path, err)
				}
				t.Logf("updated golden %s", path)
				return
			}

			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s: %v (run with -update to generate it)", path, err)
			}

			if !bytes.Equal(got, want) {
				t.Errorf("golden mismatch for %s (%s) -- re-run with -update if this change is intended\n"+
					"--- want (golden, %s) ---\n%s\n--- got (fresh Evaluate output) ---\n%s",
					tc.name, path, path, want, got)
			}
		})
	}
}

// TestGoldenSetUnchangedForOmittingRules (INVCR-01-12 AC-6, D9; Stage 2
// correction C2): the golden JSON files above are a byte-for-byte compare,
// so a diff there proves SOMETHING changed but not that the RIGHT set of
// violations changed -- an always-absent omitempty field is invisible to a
// byte-diff review, so it can't by itself distinguish "correctly omitted"
// from "population code never ran". This test asserts the semantic property
// directly and typed, against the same two fixture payloads TestGolden
// pins:
//   - every violation an OMITTING-type rule fires (required/cel -- the only
//     two omitting types the seeded corpus exercises, Stage 2 correction
//     C1) has Expected == nil && Actual == nil, unconditionally.
//   - every violation a POPULATING-type rule fires (format/regex, enum,
//     range, tax_math, line_sum) carries the EXACT Expected/Actual this
//     subtask's evaluators must compute -- not merely non-nil.
//
// Correction C2's own prose undercounts this corpus: demo_bad_invoice's
// supplier-tin-format is ALSO a populating type (format/regex, not just
// vat-standard-rate's tax_math), and many_violations additionally exercises
// enum (currency-allowed) and range (subtotal-non-negative) -- 7
// populating-type violations fire across the two payloads, not 2, and only
// 4 distinct omitting-type violations fire, not all 11 seeded omitting
// rules (the other 7 never fire against either fixture; the direct-eval
// omission tests in evaluators_test.go/evaluators_math_test.go/cel_test.go
// cover required/date/cross_field/conditional/cel independently of this
// corpus). Verified by reading both golden files and the fixture builders
// directly (badInvoicePayload/manyViolationsPayload), not assumed from the
// plan text.
func TestGoldenSetUnchangedForOmittingRules(t *testing.T) {
	_, app := dbTestPools(t)
	rs := loadActive(t, app)
	engine := NewDefaultEngine()

	type want struct {
		expected *string
		actual   *string
	}

	cases := []struct {
		name    string
		payload func() Payload
		want    map[string]want
	}{
		{
			name:    "demo_bad_invoice",
			payload: badInvoicePayload,
			want: map[string]want{
				// format/regex: pattern only, no Actual (population table).
				"supplier-tin-format": {expected: ptr("^[0-9]{8}-[0-9]{4}$")},
				// tax_math: base(subtotal=1000)*rate(0.075)=75; Actual is
				// the resolved "expected"(param) operand, vat=70.
				"vat-standard-rate": {expected: ptr("75"), actual: ptr("70")},
			},
		},
		{
			name:    "many_violations",
			payload: manyViolationsPayload,
			want: map[string]want{
				// enum: allowed values joined " · " (one value: NGN); Actual
				// is the resolved (non-matching) value, currency=USD.
				"currency-allowed": {expected: ptr("NGN"), actual: ptr("USD")},
				// required (omitting).
				"invoice-number-required": {},
				"issue-date-required":     {},
				// line_sum: folded line total (100*10 + 5*1 = 1005) vs the
				// declared "expected"(param), subtotal=-5.
				"line-items-sum-subtotal": {expected: ptr("1005"), actual: ptr("-5")},
				// cel (omitting).
				"no-duplicate-line-items": {},
				// range (min:0 only): ">= 0"; Actual is the resolved value,
				// subtotal=-5.
				"subtotal-non-negative": {expected: ptr(">= 0"), actual: ptr("-5")},
				// required (omitting).
				"supplier-name-required": {},
				// format/regex, same shape as demo_bad_invoice's.
				"supplier-tin-format": {expected: ptr("^[0-9]{8}-[0-9]{4}$")},
				// tax_math: base(subtotal=-5)*rate(0.075)=-0.375; Actual is
				// the resolved "expected"(param) operand, vat=999.
				"vat-standard-rate": {expected: ptr("-0.375"), actual: ptr("999")},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := engine.Evaluate(tc.payload(), rs)
			if err != nil {
				t.Fatalf("Evaluate(%s): %v", tc.name, err)
			}
			if len(result.Violations) != len(tc.want) {
				t.Fatalf("len(Violations) = %d, want %d for %s -- this test's want map must name EXACTLY the violations "+
					"this fixture produces; a drift here means the fixture changed and this want map needs updating "+
					"alongside it (not a signal to loosen the assertion)",
					len(result.Violations), len(tc.want), tc.name)
			}
			seen := make(map[string]bool, len(tc.want))
			for _, v := range result.Violations {
				w, ok := tc.want[v.RuleKey]
				if !ok {
					t.Errorf("unexpected violation key %q -- not in this test's want map", v.RuleKey)
					continue
				}
				seen[v.RuleKey] = true
				if !strPtrEqual(v.Expected, w.expected) {
					t.Errorf("%s: Expected = %s, want %s", v.RuleKey, stringPtrDebug(v.Expected), stringPtrDebug(w.expected))
				}
				if !strPtrEqual(v.Actual, w.actual) {
					t.Errorf("%s: Actual = %s, want %s", v.RuleKey, stringPtrDebug(v.Actual), stringPtrDebug(w.actual))
				}
			}
			for key := range tc.want {
				if !seen[key] {
					t.Errorf("want map names violation key %q but %s produced no such violation", key, tc.name)
				}
			}
		})
	}
}

// ptr returns a pointer to s -- shorthand for the want-map literals above.
func ptr(s string) *string { return &s }

// strPtrEqual reports whether two *string are both nil or both non-nil with
// equal pointed-to values.
func strPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
