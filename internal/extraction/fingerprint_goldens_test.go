// fingerprint_goldens_test.go: the Fingerprint of every committed testdata PDF on the shipped
// reader, captured before the word stage exists. A row that moves is a rule namespace silently
// orphaned. See fingerprint_goldens_premerge_test.go for the frozen comparison copy.
package extraction_test

import (
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// chrome_register.pdf and chrome_register_twin.pdf: since EXTR-36-03's word stage both read as
// advisory_register.pdf's own layout -- the per-glyph print and its word-level twin are one
// arrangement. The pre-merge value (a "vat"-only anchor list) is kept in
// fingerprintGoldensPreMerge. The twin differs only in the invoice number and the amounts, so
// both Chrome rows hold one value.
var fingerprintGoldens = map[string]string{
	"ai_lines_invoice.pdf":                  "v3:d7fd646ccc6d992349063e9a6a7eebaf48db7589c42b88b10fee51e66dcb41e4",
	"ai_steered_invoice.pdf":                "v3:fc3afe3868932fe3274fbffa1d713c010b77ccae47f358aa71f7fdb7296082b2",
	"ai_unavailable_invoice.pdf":            "v3:053f25807f20c119ecb7e375a101c9e70c54e0621a79777c5ce7b7cdf76ad7e3",
	"advisory_dense.pdf":                    "v3:fe469d7afac0a666956002d69238ca46fc252def84fe910cb24820ec432e311b",
	"advisory_register.pdf":                 "v3:d89450d1d00688a442c17a083f9ab9b832d152ee249c671ff6ac17146a2a4bee",
	"advisory_register_unspaced.pdf":        "v3:d89450d1d00688a442c17a083f9ab9b832d152ee249c671ff6ac17146a2a4bee",
	"chrome_register.pdf":                   "v3:d89450d1d00688a442c17a083f9ab9b832d152ee249c671ff6ac17146a2a4bee",
	"chrome_register_twin.pdf":              "v3:d89450d1d00688a442c17a083f9ab9b832d152ee249c671ff6ac17146a2a4bee",
	"corpus_ambiguous_date.pdf":             "v3:aa3c59add58181ab233b5b690b57dee82ef1fdd1428daa1b2aba85a439161207",
	"corpus_inline_labels.pdf":              "v3:8570015f135eac949cd519b49f47c985fe0f310b717d1a36909f7dd6a4e73945",
	"corpus_split_labels.pdf":               "v3:4b916b2c1aa4239089ee79cda743da1bec379a6385bb85a5b82609cf3059bcf1",
	"corpus_stacked_labels.pdf":             "v3:fdd95d43c0d4a79dbe0e3c5c3ea09b23a8bba6b3bed73c3a7d51dfb23e4e1846",
	"corpus_totals_block.pdf":               "v3:0da5ad8436bb5d80e3c523f3695e09acea833fa469474279c5c008f7937fd556",
	"corpus_two_column.pdf":                 "v3:02a5a7038b265c0df8ceb8a4633568cc8ac77d827361211e7bd2436d2ce2938c",
	"dense_invoice.pdf":                     "v3:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	"hybrid_invoice.pdf":                    "v3:e5a2e2bbd74717a0a69fe1f33899477d152f27b5a5ff56dd234a26eb5f4b288a",
	"learned_two_party.pdf":                 "v3:f7595e9a6f5be109935a8bb9bc4fef5e6482be32133b85ba59842c662f6d7689",
	"learned_typed_total.pdf":               "v3:614948e82bdde21efd352bcbb767056213401e829e767bb94ddbe0edca47251f",
	"learned_typed_total_twin.pdf":          "v3:614948e82bdde21efd352bcbb767056213401e829e767bb94ddbe0edca47251f",
	"native_3page.pdf":                      "v3:874c6fee4e45319de7448527ede8996bddc8e0a8c330760a847f3136af4547dd",
	"native_invoice.pdf":                    "v3:e5a2e2bbd74717a0a69fe1f33899477d152f27b5a5ff56dd234a26eb5f4b288a",
	"noninvoice_credit_note.pdf":            "v3:82fbec0dcd7dcaabdd792eba1d9b504f7ca20742647727022c6c8d656699c848",
	"noninvoice_delivery_note.pdf":          "v3:7e1798b2a2c2027f56478daa1a5163584bb6aeead0e767365c3bf56e28ee9f73",
	"noninvoice_proforma.pdf":               "v3:c1aac67fe1a1e56382d5e9ff1aa7e04f2032537078fe51e92ea02564a3d6bd7d",
	"noninvoice_purchase_order.pdf":         "v3:29659ea7c32d9102be3877e1e199bf72c090c23fc8ec69ce2c22b73984554efd",
	"noninvoice_quotation.pdf":              "v3:bebd2daebaec87a53acffd6a75b45761d81b08c4eae999e68df7e62f7d010da9",
	"noninvoice_receipt.pdf":                "v3:314325e5a67a8ee4b54952d3ee1d5e4e014756292a872ac2b2cce4af1966361b",
	"noninvoice_statement.pdf":              "v3:96af0259464316493140f50bb3b7f80790abdcc17716a5a32e48de796f3dbc14",
	"rich_invoice.pdf":                      "v3:3c92b558e53727cd09be01dedebd23574b13396a36d7fdc392df24e840de4f24",
	"scanned_invoice.pdf":                   "v3:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	"table_invoice.pdf":                     "v3:2ad7da8ebfcd584cce00835da09ed6848cadf74621e177967423407daf5578c4",
	"wild_rc_due_naira.pdf":                 "v3:d49e1c6d5aa0da37f80db44d645ffe9934896639d3010dec86274d1a06648cb5",
	"wild_ruled_lines_totals.pdf":           "v3:54173c47bd124176383e124f76b915b6bd83a1a33a7f910a5b920757a25584c7",
	"wild_ruled_lines_totals_asprinted.pdf": "v3:115f97ab82be93b6ce78c6e34eae0cc76eaa7bad509046aed62686c44d600c52",
	"wild_scanned_no_number.pdf":            "v3:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	"wild_stacked_borderless.pdf":           "v3:7db04898701fd2534d9ffb2bf32ea945d184e93ba4f8f6db9dfda1d91ccc136c",
	"wild_stacked_borderless_asprinted.pdf": "v3:35b987486b33ea8af6db58e1dc41dde4cff59cd837441779e1e4478652eaa668",
	"wild_two_party_bare_tin.pdf":           "v3:38a85f02c95f25ab08f56f76766886054faaf2d3e1e550776a5d2fc472c6c434",
	"wild_two_party_bare_tin_asprinted.pdf": "v3:f0670e9a3d8ad55774744f2bb9ff9dc54e2b8ba74816b445def147563a4b40cf",
}

// fgGoldenFloor is the committed-PDF count as of CHECK-01-03 (32 prior plus the 7 non-invoice
// fixtures). A floor to assert, not a comment -- a table silently truncated to zero would
// satisfy every loop below.
const fgGoldenFloor = 39

// AC-6: every committed testdata PDF must fingerprint to its golden row on the shipped reader.
func TestFingerprint_GoldensHoldForEveryCommittedFixture(t *testing.T) {
	if len(fingerprintGoldens) < fgGoldenFloor {
		t.Fatalf("fingerprintGoldens holds %d row(s), want at least %d", len(fingerprintGoldens), fgGoldenFloor)
	}

	for _, name := range slices.Sorted(maps.Keys(fingerprintGoldens)) {
		t.Run(name, func(t *testing.T) {
			pages := rvCorpusPages(t, name)
			if got, want := extraction.Fingerprint(pages), fingerprintGoldens[name]; got != want {
				t.Errorf("Fingerprint(%s) = %s, want golden %s", name, got, want)
			}
		})
	}
}

// fpUngoldened is every committed name with no fingerprintGoldens row, mirroring fxUngenerated's
// shape (fixtures_test.go).
func fpUngoldened(committed []string) []string {
	var out []string
	for _, n := range committed {
		if _, ok := fingerprintGoldens[n]; !ok {
			out = append(out, n)
		}
	}
	return out
}

// AC-7: the table has to be complete, and the completeness scan has to be able to find an
// absence -- as TestFixtures_EveryCommittedWildArrangementHasAGenerator already proves at
// fixtures_test.go:1745-1747.
func TestFingerprint_GoldenTableCoversEveryCommittedFixture(t *testing.T) {
	entries, err := os.ReadDir(fxDir)
	if err != nil {
		t.Fatalf("read %s: %v", fxDir, err)
	}
	var committed []string
	for _, e := range entries {
		if n := e.Name(); !e.IsDir() && strings.HasSuffix(n, ".pdf") {
			committed = append(committed, n)
		}
	}
	if len(committed) < fgGoldenFloor {
		t.Fatalf("%s holds %d .pdf file(s), want at least %d", fxDir, len(committed), fgGoldenFloor)
	}

	if missing := fpUngoldened(committed); len(missing) != 0 {
		t.Errorf("%v committed with no fingerprintGoldens row", missing)
	}

	t.Run("planted", func(t *testing.T) {
		got := fpUngoldened(append(slices.Clone(committed), "chrome_planted.pdf"))
		if !slices.Equal(got, []string{"chrome_planted.pdf"}) {
			t.Errorf("a planted chrome_planted.pdf reports %v, want exactly [chrome_planted.pdf]", got)
		}
	})
}

// fgChromeNames is the only pair AC-8 permits a difference at.
var fgChromeNames = map[string]bool{chrRegister: true, chrRegisterTwin: true}

// AC-8: the live table may differ from the frozen baseline only at the two Chrome rows.
func TestFingerprint_OnlyTheChromeRowsMayDifferFromTheFrozenBaseline(t *testing.T) {
	if len(fingerprintGoldens) < fgGoldenFloor || len(fingerprintGoldensPreMerge) < fgGoldenFloor {
		t.Fatalf("live holds %d row(s), premerge holds %d, want at least %d each",
			len(fingerprintGoldens), len(fingerprintGoldensPreMerge), fgGoldenFloor)
	}

	for name, live := range fingerprintGoldens {
		pre, ok := fingerprintGoldensPreMerge[name]
		if !ok {
			continue // TestFingerprint_TheFrozenBaselineIsTheSameShapeAsTheLiveTable is the key-set oracle
		}
		if live != pre && !fgChromeNames[name] {
			t.Errorf("%s differs from the frozen baseline (%s -> %s) and is not one of the two Chrome rows", name, pre, live)
		}
	}
}

// AC-8: a row dropped from either table must be red here, not silently excluded from the subset
// comparison above.
func TestFingerprint_TheFrozenBaselineIsTheSameShapeAsTheLiveTable(t *testing.T) {
	live := slices.Sorted(maps.Keys(fingerprintGoldens))
	pre := slices.Sorted(maps.Keys(fingerprintGoldensPreMerge))
	if len(live) < fgGoldenFloor || len(pre) < fgGoldenFloor {
		t.Fatalf("live holds %d name(s), premerge holds %d, want at least %d each", len(live), len(pre), fgGoldenFloor)
	}

	if !slices.Equal(live, pre) {
		t.Errorf("the live and frozen key sets differ:\nlive:     %v\npremerge: %v", live, pre)
	}
}

// Fingerprint is the sha256 of the joined observation list, so a fixture that anchors NOTHING
// fingerprints to the hash of the empty string -- a row that pins no reading at all. Three
// image-only rows legitimately hold it; the two Chrome rows must not, or AC-4's vat-only pin and
// AC-8's baseline would both be satisfied by a fixture the reader cannot see.
func TestFingerprint_TheChromeRowsAreNotTheEmptyObservationHash(t *testing.T) {
	empty := extraction.Fingerprint(nil)
	if !strings.HasSuffix(empty, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855") {
		t.Fatalf("Fingerprint(nil) = %s, want the sha256 of the empty string -- this guard is comparing against the wrong value", empty)
	}

	for _, name := range []string{chrRegister, chrRegisterTwin} {
		if fingerprintGoldens[name] == empty {
			t.Errorf("%s's golden row is the empty-observation fingerprint -- the fixture anchors nothing and pins nothing", name)
		}
		if fingerprintGoldensPreMerge[name] == empty {
			t.Errorf("%s's frozen row is the empty-observation fingerprint", name)
		}
	}
}
