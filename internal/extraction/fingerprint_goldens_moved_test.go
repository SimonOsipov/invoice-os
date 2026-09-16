// fingerprint_goldens_moved_test.go: the half AC-8 leaves open -- the frozen-baseline guard in
// fingerprint_goldens_test.go permits the two Chrome rows to differ but never asserts they do.
// Own file so an edit to the pinned value is named in the PR's changed-file list.
package extraction_test

import "testing"

// The two Chrome rows' value before the word stage, pinned a second time so a one-file
// "refresh" of the frozen table cannot pass unnoticed.
const frozenChromeRow = "v3:8f353f4c92d9135c022a096463ddd2cca5c355397a19ee95e422f65bc35fda72"

// The frozen Chrome rows still hold the pre-merge value, and the live rows moved off it.
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
