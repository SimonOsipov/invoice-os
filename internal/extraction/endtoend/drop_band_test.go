// drop_band_test.go: the shipped-vs-zero-Drop differential over requiredPDFs and requiredGoldens,
// both readers, no database.
package endtoend

import (
	"slices"
	"strconv"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	dbTwinPDF    = "wild_stacked_borderless.pdf"
	dbSiblingPDF = "wild_stacked_borderless_asprinted.pdf"

	// dbScannedLayout is image-only: pdfium reads zero tokens off it, so it gets its own read
	// that asserts the zero instead of eeTokenPages' fatal.
	dbScannedLayout = "wild_scanned_no_number.pdf"

	// ddDropRight mirrors tier1DropRight for the control; Tier1DropRightForTest is unreachable
	// from this package. Nothing pins the two equal.
	ddDropRight = 0.97
)

// dbFieldAdd is one field the drop band admits, with its own distance per reader: pdfium and
// docling render the same text at slightly different box edges.
type dbFieldAdd struct {
	field, value, ruleID    string
	pdfiumDist, doclingDist float64
}

// dbTwinAdditions is the twin's nine, measured 2026-09-14 under the shipped dial.
var dbTwinAdditions = []dbFieldAdd{
	{"issue_date", "2026-07-30", "t1.issue_date.right", 0.280118, 0.278824},
	{"supplier_tin", "99999999-1101", "t1.supplier_tin.right", 0.265902, 0.263588},
	{"supplier_name", "Adeyemi Trading Limited", "t1.supplier_name.right", 0.300922, 0.300627},
	{"buyer_tin", "99999999-1102", "t1.buyer_tin.right", 0.286608, 0.284294},
	{"buyer_name", "Honeywell Group", "t1.buyer_name.right", 0.322863, 0.321333},
	{"currency", "NGN", "t1.currency.right", 0.294725, 0.293020},
	{"subtotal", "1500.00", "t1.subtotal.right", 0.298451, 0.295157},
	{"vat", "112.50", "t1.vat.right", 0.336667, 0.334412},
	{"total", "1612.50", "t1.total.right", 0.332255, 0.328961},
}

// dbSiblingAdditions is the twin's nine at the sibling's own offsets.
var dbSiblingAdditions = []dbFieldAdd{
	{"issue_date", "2026-07-30", "t1.issue_date.right", 0.273745, 0.272294},
	{"supplier_tin", "99999999-1101", "t1.supplier_tin.right", 0.265902, 0.263588},
	{"supplier_name", "Adeyemi Trading Limited", "t1.supplier_name.right", 0.316510, 0.314824},
	{"buyer_tin", "99999999-1102", "t1.buyer_tin.right", 0.286608, 0.284294},
	{"buyer_name", "Honeywell Group", "t1.buyer_name.right", 0.274569, 0.272294},
	{"currency", "NGN", "t1.currency.right", 0.263216, 0.261451},
	{"subtotal", "1500.00", "t1.subtotal.right", 0.303902, 0.300608},
	{"vat", "112.50", "t1.vat.right", 0.287020, 0.284275},
	{"total", "1612.50", "t1.total.right", 0.233627, 0.230863},
}

// dbLayoutAdditions is every required layout's pinned gain; a layout absent here must gain
// nothing at all.
var dbLayoutAdditions = map[string][]dbFieldAdd{
	dbTwinPDF:    dbTwinAdditions,
	dbSiblingPDF: dbSiblingAdditions,
}

// dbWantRows renders one layout's pinned additions for one reader as bdRow, so bdDiff applies.
func dbWantRows(fields []dbFieldAdd, docling bool) []bdRow {
	out := make([]bdRow, len(fields))
	for i, f := range fields {
		d := f.pdfiumDist
		if docling {
			d = f.doclingDist
		}
		out[i] = bdMake(f.field, f.value, f.ruleID, d)
	}
	return out
}

// ddRow keeps a candidate's own Tier, unlike bdMake: this diff runs over every candidate, not
// only the rightward ones bdRightward filters to.
func ddRow(c extraction.Candidate) bdRow {
	return bdRow{field: c.Field, value: c.Value, ruleID: c.RuleID, tier: c.Tier, distance: strconv.FormatFloat(c.Distance, 'f', 6, 64)}
}

func ddRows(cs []extraction.Candidate) []bdRow {
	out := make([]bdRow, len(cs))
	for i, c := range cs {
		out[i] = ddRow(c)
	}
	return out
}

// ddZeroed is the shipped set with every Drop zeroed: a struct copy, no re-parse.
func ddZeroed() []extraction.Tier1Rule {
	out := slices.Clone(extraction.Tier1Rules)
	for i := range out {
		out[i].Drop = 0
	}
	return out
}

// ddWithDrop clones the shipped set and sets Drop on every right rule. Package-local copy of
// dbWithDrop (internal/extraction/drop_band_test.go): that helper lives in extraction_test, a
// different package, and is unreachable from here.
func ddWithDrop(drop float64) []extraction.Tier1Rule {
	out := slices.Clone(extraction.Tier1Rules)
	for i := range out {
		if out[i].Rule.Relation.Kind == extraction.RelRight {
			out[i].Drop = drop
		}
	}
	return out
}

// ddScannedRead reads the image-only layout without eeTokenPages' zero-page fatal.
func ddScannedRead(t *testing.T) []extraction.TokenPage {
	t.Helper()
	var pages []extraction.TokenPage
	doc := extraction.Document{Bytes: eeFixtureBytes(t, dbScannedLayout), ContentType: eeContentType}
	if _, err := extraction.NewPDFiumReader().Read(t.Context(), doc, extraction.CollectTokens(&pages)); err != nil {
		t.Fatalf("read %s with the PDFium reader: %v", dbScannedLayout, err)
	}
	return pages
}

func ddTokenCount(pages []extraction.TokenPage) int {
	n := 0
	for _, p := range pages {
		n += len(p.Tokens)
	}
	return n
}

// The drop band adds candidates on the stacked pair alone, on both readers, and removes none.
func TestTier1_TheDropBandAddsOnlyTheStackedPairsReads(t *testing.T) {
	if len(requiredPDFs) != len(requiredGoldens) {
		t.Fatalf("requiredPDFs holds %d name(s), requiredGoldens holds %d; the two readers would not cover the same layouts", len(requiredPDFs), len(requiredGoldens))
	}

	shipped := extraction.RuleSet{Tier1: extraction.Tier1Rules}
	zero := extraction.RuleSet{Tier1: ddZeroed()}

	for _, layout := range requiredPDFs {
		want := dbLayoutAdditions[layout]

		var pdfiumPages []extraction.TokenPage
		if layout == dbScannedLayout {
			pdfiumPages = ddScannedRead(t)
		} else {
			pdfiumPages = eeTokenPages(t, layout)
		}

		readers := []struct {
			name    string
			pages   []extraction.TokenPage
			docling bool
		}{
			{layout + " (pdfium)", pdfiumPages, false},
			{layout + " (docling)", bdGoldenTokenPages(t, layout), true},
		}

		for _, r := range readers {
			tokens := ddTokenCount(r.pages)
			if layout == dbScannedLayout && !r.docling {
				if tokens != 0 {
					t.Errorf("%s read %d token(s), want 0: this layout is image-only under pdfium", r.name, tokens)
				}
				continue // Resolve over zero tokens compares nothing to nothing on both sides.
			}
			if tokens == 0 {
				t.Fatalf("%s read 0 token(s); the differential over it compares nothing to nothing", r.name)
			}

			added, removed := bdDiff(ddRows(extraction.Resolve(r.pages, shipped)), ddRows(extraction.Resolve(r.pages, zero)))
			for _, row := range removed {
				t.Errorf("%s: the shipped set no longer produces %s, which the zero-drop set does", r.name, row)
			}

			wantAdd := dbWantRows(want, r.docling)
			extra, missing := bdDiff(added, wantAdd)
			for _, row := range extra {
				t.Errorf("%s gained %s, which is not one of the pinned additions", r.name, row)
			}
			for _, row := range missing {
				t.Errorf("%s did not gain %s", r.name, row)
			}
			if len(added) != len(wantAdd) {
				t.Errorf("%s gained %d candidate(s), want %d", r.name, len(added), len(wantAdd))
			}
		}
	}

	// Control: the oracle must be able to see a real difference, or the zero-removal assertion
	// above is vacuous. Same pair, arguments swapped: the pinned nine report as removed.
	t.Run("control_the_oracle_sees_a_real_difference", func(t *testing.T) {
		pages := eeTokenPages(t, dbTwinPDF)
		withDrop := extraction.RuleSet{Tier1: ddWithDrop(ddDropRight)}
		atZero := extraction.RuleSet{Tier1: ddWithDrop(0)}

		added, removed := bdDiff(ddRows(extraction.Resolve(pages, atZero)), ddRows(extraction.Resolve(pages, withDrop)))
		if len(added) != 0 {
			t.Errorf("the zero-drop variant gained %s over the with-drop variant; the drop band is supposed to admit, never exclude", bdShow(added))
		}
		want := dbWantRows(dbTwinAdditions, false)
		extra, missing := bdDiff(removed, want)
		for _, row := range extra {
			t.Errorf("the control reported %s removed, which is not one of the pinned nine", row)
		}
		for _, row := range missing {
			t.Errorf("the control did not report %s removed; the oracle would not have caught a real regression", row)
		}
	})
}
