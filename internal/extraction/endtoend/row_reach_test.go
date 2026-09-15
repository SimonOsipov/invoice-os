// row_reach_test.go: the shipped-vs-zero-RowReach differential over requiredPDFs and
// requiredGoldens, plus advisory_dense.pdf, both readers, no database.
package endtoend

import (
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	rrRegisterPDF = "advisory_register.pdf"
	rrDensePDF    = "advisory_dense.pdf"
	rrReadsWalked = 29
)

var rrRegisterAdditions = []bdRow{
	bdMake("subtotal", "14800000.00", "t1.subtotal.right", 0.614732),
	bdMake("vat", "1110000.00", "t1.vat.right", 0.613281),
	bdMake("total", "14430000.00", "t1.total.right", 0.460405),
}

// rrWithoutRowReach clones the shipped set with RowReach cleared on every rule.
func rrWithoutRowReach() []extraction.Tier1Rule {
	out := slices.Clone(extraction.Tier1Rules)
	for i := range out {
		out[i].RowReach = false
	}
	return out
}

func rrRowReachDiff(pages []extraction.TokenPage) (added, removed []bdRow) {
	shipped := ddRows(extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules}))
	zero := ddRows(extraction.Resolve(pages, extraction.RuleSet{Tier1: rrWithoutRowReach()}))
	return bdDiff(shipped, zero)
}

// The reach has no dial, so this differential pins "nothing admitted outside the register" at
// every reach at once: the advisory PDFs are not scored (P10), so only this test and the
// register control below watch what the reach touches beyond the register.
func TestTier1_TheRowReachAddsNothingOnTheScoredLayouts(t *testing.T) {
	if len(requiredPDFs) != len(requiredGoldens) {
		t.Fatalf("requiredPDFs holds %d name(s), requiredGoldens holds %d", len(requiredPDFs), len(requiredGoldens))
	}

	type read struct {
		name  string
		pages []extraction.TokenPage
	}
	var reads []read
	walked := 0
	for _, layout := range requiredPDFs {
		if layout == dbScannedLayout {
			walked++
			if n := ddTokenCount(ddScannedRead(t)); n != 0 {
				t.Errorf("%s (pdfium) read %d token(s), want 0: this layout is image-only", layout, n)
			}
		} else {
			reads = append(reads, read{layout + " (pdfium)", eeTokenPages(t, layout)})
		}
		reads = append(reads, read{layout + " (docling)", bdGoldenTokenPages(t, layout)})
	}
	reads = append(reads, read{rrDensePDF + " (pdfium)", eeTokenPages(t, rrDensePDF)})

	for _, r := range reads {
		walked++
		if ddTokenCount(r.pages) == 0 {
			t.Fatalf("%s read 0 token(s); the differential over it compares nothing to nothing", r.name)
		}
		added, removed := rrRowReachDiff(r.pages)
		for _, row := range added {
			t.Errorf("%s gained %s under the row reach", r.name, row)
		}
		for _, row := range removed {
			t.Errorf("%s lost %s under the row reach", r.name, row)
		}
	}
	if walked != rrReadsWalked {
		t.Errorf("walked %d read(s), want %d", walked, rrReadsWalked)
	}

	t.Run("control: the register gains exactly its three far-right amounts", func(t *testing.T) {
		added, removed := rrRowReachDiff(eeTokenPages(t, rrRegisterPDF))
		for _, row := range removed {
			t.Errorf("%s lost %s under the row reach", rrRegisterPDF, row)
		}
		extra, missing := bdDiff(added, rrRegisterAdditions)
		for _, row := range extra {
			t.Errorf("%s gained %s, which is not a pinned addition", rrRegisterPDF, row)
		}
		for _, row := range missing {
			t.Errorf("%s did not gain %s", rrRegisterPDF, row)
		}
		if len(added) != len(rrRegisterAdditions) {
			t.Errorf("%s gained %d row(s), want %d", rrRegisterPDF, len(added), len(rrRegisterAdditions))
		}
	})
}
