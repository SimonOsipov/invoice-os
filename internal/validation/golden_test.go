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
