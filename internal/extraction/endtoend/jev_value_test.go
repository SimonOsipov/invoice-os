// jev_value_test.go: AC-8 -- CHECK-01-03's seven non-invoice fixtures never enter a corpus_
// ratchet that would move a Tier-1 denominator. No database.
package endtoend

import (
	"slices"
	"strings"
	"testing"
)

// The seven non-invoice fixtures, declared here because internal/extraction's own table is in
// package extraction_test and unreachable. Held against that table by the mirror clause below.
var jvNonInvoicePDFs = []string{
	"noninvoice_receipt.pdf", "noninvoice_proforma.pdf", "noninvoice_quotation.pdf",
	"noninvoice_credit_note.pdf", "noninvoice_delivery_note.pdf", "noninvoice_statement.pdf",
	"noninvoice_purchase_order.pdf",
}

const jvNonInvoiceFile = "../noninvoice_test.go"
const jvNonInvoicePrefix = "noninvoice_"

// TestNonInvoice_NoFixtureEntersACorpusRatchet: AC-8, two-legged because the four ratchets in
// package extraction_test are unreachable as Go values from here. Half A checks this package's
// own ratchets directly; Half B reuses wildRatchets + wildVarBody, already proved by
// TestWildLayouts_DoNotEnterTheCorpusRatchets, for the other four.
func TestNonInvoice_NoFixtureEntersACorpusRatchet(t *testing.T) {
	if len(jvNonInvoicePDFs) != 7 {
		t.Fatalf("jvNonInvoicePDFs names %d PDF(s), want 7", len(jvNonInvoicePDFs))
	}
	if len(wildRatchets) < 4 {
		t.Fatalf("wildRatchets names %d collection(s), want at least 4", len(wildRatchets))
	}
	if len(expectByLayout) < wildRequireListPin {
		t.Fatalf("expectByLayout names %d row(s), want at least %d", len(expectByLayout), wildRequireListPin)
	}
	if len(requiredPDFs) < wildRequireListPin {
		t.Fatalf("requiredPDFs names %d entr(ies), want at least %d", len(requiredPDFs), wildRequireListPin)
	}
	// Control: the Go-value half below is reading a table that must still hold its known member.
	if !slices.Contains(requiredPDFs, "corpus_inline_labels.pdf") {
		t.Fatalf("requiredPDFs no longer names corpus_inline_labels.pdf; the Go-value half is reading a table that has been emptied")
	}

	for _, name := range jvNonInvoicePDFs {
		t.Run(name, func(t *testing.T) {
			if slices.Contains(wildLayouts, name) {
				t.Errorf("%s is in wildLayouts", name)
			}
			if slices.Contains(wildTextLayouts, name) {
				t.Errorf("%s is in wildTextLayouts", name)
			}
			if slices.Contains(requiredPDFs, name) {
				t.Errorf("%s is in requiredPDFs", name)
			}
			if _, ok := wildTokenFloor[name]; ok {
				t.Errorf("%s has a wildTokenFloor row", name)
			}
			if _, ok := eeLinesExpected[name]; ok {
				t.Errorf("%s has an eeLinesExpected row", name)
			}
			for _, row := range expectByLayout {
				if row.file == name {
					t.Errorf("%s has an expectByLayout row", name)
				}
			}

			golden := strings.TrimSuffix(name, ".pdf") + ".docling.json"
			if slices.Contains(requiredGoldens, golden) {
				t.Errorf("%s is in requiredGoldens", golden)
			}
		})
	}

	for _, r := range wildRatchets {
		body := wildVarBody(t, wildReadFile(t, r.file), r.decl, r.file)
		if n := strings.Count(body, r.needle); n < r.min {
			t.Fatalf("%s's %q names %d %q entr(ies), want at least %d; the scan is not reading the collection", r.file, r.decl, n, r.needle, r.min)
		}
		if strings.Contains(body, jvNonInvoicePrefix) {
			t.Errorf("%s's %q names a %s fixture; that moves the Tier-1 denominators the same way a wild_ layout would", r.file, r.decl, jvNonInvoicePrefix)
		}
	}

	// Mirror clause: a name added to internal/extraction's table and forgotten here.
	src := wildReadFile(t, jvNonInvoiceFile)
	for _, name := range jvNonInvoicePDFs {
		if !strings.Contains(src, name) {
			t.Errorf("%s does not mention %s; the two lists have drifted apart", jvNonInvoiceFile, name)
		}
	}
	if n := strings.Count(src, jvNonInvoicePrefix); n < 7 {
		t.Errorf("%s carries %d occurrence(s) of %q, want at least 7", jvNonInvoiceFile, n, jvNonInvoicePrefix)
	}
}
