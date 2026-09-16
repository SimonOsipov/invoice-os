// fingerprint_goldens_moved_test.go: closes the one-sided half of AC-8 -- the frozen-baseline
// guard in fingerprint_goldens_test.go never asserts the two Chrome rows actually moved. Own file
// so an edit to either golden table's Chrome rows shows up named in the PR's changed-file list.
package extraction_test

import "testing"

// frozenChromeRow is chrome_register.pdf's and chrome_register_twin.pdf's value before
// EXTR-36-03's word stage, pinned a second time here so the frozen table cannot drift unnoticed.
const frozenChromeRow = "v3:8f353f4c92d9135c022a096463ddd2cca5c355397a19ee95e422f65bc35fda72"

// AC-3's missing half: the two Chrome rows must actually differ from the frozen baseline, not
// merely be permitted to by TestFingerprint_OnlyTheChromeRowsMayDifferFromTheFrozenBaseline.
// Pass-on-arrival, not red-first -- a guard, not a driver; proven by a mutation control that
// "refreshes" a frozen Chrome row to its live value.
func TestFingerprint_TheTwoChromeRowsDidMoveFromTheFrozenBaseline(t *testing.T) {
	for _, name := range []string{chrRegister, chrRegisterTwin} {
		pre, ok := fingerprintGoldensPreMerge[name]
		if !ok {
			t.Fatalf("%s has no fingerprintGoldensPreMerge row", name)
		}
		if pre != frozenChromeRow {
			t.Errorf("fingerprintGoldensPreMerge[%s] = %s, want the pinned pre-merge value %s", name, pre, frozenChromeRow)
		}

		live, ok := fingerprintGoldens[name]
		if !ok {
			t.Fatalf("%s has no fingerprintGoldens row", name)
		}
		if live == pre {
			t.Errorf("fingerprintGoldens[%s] = %s, unchanged from the frozen baseline -- the row did not move", name, live)
		}
	}
}
