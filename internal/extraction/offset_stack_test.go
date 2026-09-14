// offset_stack_test.go: 01-T1..01-T6, the clause predicate over the story's own offsets and
// the stacked twin. External package: every spec reaches only exported symbols.
package extraction_test

import (
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// stkAnchor is 01-T1..01-T4's shared label box.
var stkAnchor = rvBox(0.10, 0.10, 0.25, 0.13)

// 01-T1
func TestOffsetStack_RightRejectsATokenLeftOfTheAnchor(t *testing.T) {
	value := rvBox(0.05, 0.10, 0.09, 0.13)

	order, distance, overlap := extraction.RelationClausesForTest(stkAnchor, value, extraction.RelRight, 0.35, 0)
	if !order {
		t.Errorf("order = %v, want true -- the value sits left of the anchor", order)
	}
	if distance {
		t.Errorf("distance = %v, want false", distance)
	}
	if overlap {
		t.Errorf("overlap = %v, want false", overlap)
	}
}

// 01-T2
func TestOffsetStack_RightRejectsATokenBeyondMaxDistance(t *testing.T) {
	// 0.60 - 0.25 is exactly 0.35 in float64; 0.60001 clears the cap by 0.00001.
	value := rvBox(0.60001, 0.10, 0.70, 0.13)

	order, distance, overlap := extraction.RelationClausesForTest(stkAnchor, value, extraction.RelRight, 0.35, 0)
	if order {
		t.Errorf("order = %v, want false", order)
	}
	if !distance {
		t.Errorf("distance = %v, want true -- the gap clears max_distance by 0.00001", distance)
	}
	if overlap {
		t.Errorf("overlap = %v, want false", overlap)
	}
}

// 01-T3
func TestOffsetStack_RightRejectsATokenOnTheNextLine(t *testing.T) {
	value := rvBox(0.30, 0.16, 0.40, 0.19)

	order, distance, overlap := extraction.RelationClausesForTest(stkAnchor, value, extraction.RelRight, 0.35, 0)
	if order {
		t.Errorf("order = %v, want false", order)
	}
	if distance {
		t.Errorf("distance = %v, want false", distance)
	}
	if !overlap {
		t.Errorf("overlap = %v, want true -- the two Y bands do not overlap", overlap)
	}
}

// 01-T4: the guard. No clause fails, and the same admitted pair welds to Resolve through a
// learned below rule -- so the seam cannot drift from production.
func TestOffsetStack_BelowAdmitsTheStackedPairAndWeldsToResolve(t *testing.T) {
	value := rvBox(0.10, 0.15, 0.20, 0.18)

	order, distance, overlap := extraction.RelationClausesForTest(stkAnchor, value, extraction.RelBelow, extraction.Tier1MaxDistanceBelowForTest, 0)
	if order || distance || overlap {
		t.Fatalf("order=%v distance=%v overlap=%v, want all false -- this pair must clear every clause", order, distance, overlap)
	}

	anchor := rvTok("Total", 0.10, 0.10, 0.25, 0.13)
	valueTok := rvTok("NGN 1,500.00", 0.10, 0.15, 0.20, 0.18)
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		rvLearned(t, "rule-1", "total", rvLabelTotal, extraction.RelBelow, extraction.Tier1MaxDistanceBelowForTest, extraction.ShapeAmount),
	}}

	got := extraction.Resolve(rvPage(anchor, valueTok), rules)
	rvFloor(t, got, "a below rule welded to the same admitted pair")
	if len(got) != 1 {
		t.Fatalf("got %d candidate(s), want exactly 1: %+v", len(got), got)
	}
	if got[0].Value != "1500.00" {
		t.Errorf("Value = %q, want %q", got[0].Value, "1500.00")
	}
}

// stkFields are the six label/value pairs the stacked twin prints, matched by trimmed text.
var stkFields = []struct{ label, value string }{
	{"Issue Date", "2026-07-30"},
	{"Buyer", "Honeywell Group"},
	{"Currency", "NGN"},
	{"Sub total", "1,500.00"},
	{"VAT", "112.50"},
	{"Total", "1,612.50"},
}

// stkFindToken is the one token on page 1 whose trimmed text equals want. pdfium text carries
// trailing spaces ("Buyer "); trimmed equality is what stops "Total" matching "Sub total".
func stkFindToken(t *testing.T, pages []extraction.TokenPage, want string) extraction.Region {
	t.Helper()

	var hits []extraction.Region
	for _, p := range pages {
		if p.Number != 1 {
			continue
		}
		for _, tok := range p.Tokens {
			if strings.TrimSpace(tok.Text) == want {
				hits = append(hits, tok.Region)
			}
		}
	}
	if len(hits) != 1 {
		t.Fatalf("page 1 carries %d token(s) matching %q, want exactly 1", len(hits), want)
	}
	return hits[0]
}

// stkAssertVerdicts is 01-T5 and 01-T6's shared body: pdfium and docling name the same twin, so
// both readers must agree on every clause.
func stkAssertVerdicts(t *testing.T, pages []extraction.TokenPage, reader string) {
	t.Helper()
	if len(pages) == 0 {
		t.Fatalf("%s yielded no page; every field assertion below would find no token", reader)
	}

	for _, f := range stkFields {
		label := stkFindToken(t, pages, f.label)
		value := stkFindToken(t, pages, f.value)

		if order, distance, overlap := extraction.RelationClausesForTest(label, value, extraction.RelBelow, extraction.Tier1MaxDistanceBelowForTest, 0); !order || distance || !overlap {
			t.Errorf("%s below %s->%s: order=%v distance=%v overlap=%v, want order=true distance=false overlap=true", reader, f.label, f.value, order, distance, overlap)
		}
		if order, distance, overlap := extraction.RelationClausesForTest(label, value, extraction.RelRight, extraction.Tier1MaxDistanceRightForTest, 0); order || distance || !overlap {
			t.Errorf("%s right %s->%s: order=%v distance=%v overlap=%v, want order=false distance=false overlap=true", reader, f.label, f.value, order, distance, overlap)
		}
	}
}

// 01-T5
func TestOffsetStack_PdfiumTwinFailsOnlyTheOverlapClauseOnRight(t *testing.T) {
	pages := rvCorpusPages(t, "wild_stacked_borderless.pdf")
	stkAssertVerdicts(t, pages, "pdfium")
}

// 01-T6
func TestOffsetStack_DoclingTwinFailsOnlyTheOverlapClauseOnRight(t *testing.T) {
	golden := dcReadNamedGolden(t, "wild_stacked_borderless.docling.json")
	_, tokens, _ := dcServeGolden(t, golden)
	stkAssertVerdicts(t, tokens, "docling")
}
