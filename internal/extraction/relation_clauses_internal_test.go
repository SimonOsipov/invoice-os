// relation_clauses_internal_test.go: relatedTokens against its clause predicate. Package
// extraction: relatedTokens is unexported.
package extraction

import "testing"

// relAnchor sits mid-page so a grid lands on every side of it.
var relAnchor = Region{Page: 1, X0: 0.40, Y0: 0.40, X1: 0.55, Y1: 0.43}

// relOnePage is a page carrying one token at box.
func relOnePage(box Region) TokenPage {
	return TokenPage{Number: 1, Tokens: []Token{{Text: "v", Region: box}}}
}

// relatedTokens admits a token exactly when no clause fails. The per-clause floors make an
// ignored flag change the admitted set; the arithmetic itself is pinned by
// TestRelationClauses_HalfOverlapIsTheStrictBoundary and the Resolve specs.
func TestRelatedTokens_AdmitsExactlyWhenNoClauseFails(t *testing.T) {
	var page TokenPage
	page.Number = 1
	for x := 0; x < 20; x++ {
		for y := 0; y < 20; y++ {
			x0, y0 := float64(x)*0.05, float64(y)*0.05
			page.Tokens = append(page.Tokens, Token{Text: "v", Region: Region{Page: 1, X0: x0, Y0: y0, X1: min(x0+0.08, 1), Y1: min(y0+0.03, 1)}})
		}
	}

	for _, rel := range []Relation{{Kind: RelRight, MaxDistance: 0.35}, {Kind: RelBelow, MaxDistance: 0.06}} {
		t.Run(string(rel.Kind), func(t *testing.T) {
			got := relatedTokens(page, relAnchor, rel, 0)
			if len(got) == 0 {
				t.Fatal("the grid admitted nothing; the equivalence below would hold against a relatedTokens that admits nothing")
			}
			admitted := make([]bool, len(page.Tokens))
			for _, r := range got {
				admitted[r.index] = true
			}

			var onlyOrder, onlyDistance, onlyOverlap int
			for i, tok := range page.Tokens {
				order, distance, overlap := RelationClausesForTest(relAnchor, tok.Region, rel.Kind, rel.MaxDistance, 0)
				if want := !order && !distance && !overlap; admitted[i] != want {
					t.Errorf("token %v: admitted=%v, clauses order=%v distance=%v overlap=%v", tok.Region, admitted[i], order, distance, overlap)
				}
				switch {
				case order && !distance && !overlap:
					onlyOrder++
				case distance && !order && !overlap:
					onlyDistance++
				case overlap && !order && !distance:
					onlyOverlap++
				}
			}
			if onlyOrder == 0 || onlyDistance == 0 || onlyOverlap == 0 {
				t.Fatalf("sole-failure counts order=%d distance=%d overlap=%d, want each > 0; an ignored flag would go unseen", onlyOrder, onlyDistance, onlyOverlap)
			}
		})
	}
}

// ov == 0.5*span admits and a smaller overlap fails on overlap alone, for both kinds, and
// relatedTokens agrees with the clauses on both sides of that boundary.
func TestRelationClauses_HalfOverlapIsTheStrictBoundary(t *testing.T) {
	const maxDistance = 0.25
	cases := []struct {
		kind                RelationKind
		anchor, half, under Region
	}{
		{RelBelow, Region{1, 0.25, 0.125, 0.75, 0.25}, Region{1, 0.5, 0.3125, 1.0, 0.4375}, Region{1, 0.5625, 0.3125, 1.0, 0.4375}},
		{RelRight, Region{1, 0.125, 0.25, 0.25, 0.75}, Region{1, 0.3125, 0.5, 0.4375, 1.0}, Region{1, 0.3125, 0.5625, 0.4375, 1.0}},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			// Binary fractions, so the boundary is exact in float64 and the spec cannot go vacuous.
			a, h := tc.anchor, tc.half
			ov, span := overlap1D(a.Y0, a.Y1, h.Y0, h.Y1), min(a.Y1-a.Y0, h.Y1-h.Y0)
			if tc.kind == RelBelow {
				ov, span = overlap1D(a.X0, a.X1, h.X0, h.X1), min(a.X1-a.X0, h.X1-h.X0)
			}
			if ov != 0.5*span {
				t.Fatalf("half overlaps by %v against a half-span of %v; it is no longer the boundary", ov, 0.5*span)
			}

			if order, distance, overlap := RelationClausesForTest(tc.anchor, tc.half, tc.kind, maxDistance, 0); order || distance || overlap {
				t.Errorf("half: order=%v distance=%v overlap=%v, want all false", order, distance, overlap)
			}
			if order, distance, overlap := RelationClausesForTest(tc.anchor, tc.under, tc.kind, maxDistance, 0); order || distance || !overlap {
				t.Errorf("under: order=%v distance=%v overlap=%v, want only overlap", order, distance, overlap)
			}

			page := TokenPage{Number: 1, Tokens: []Token{{Text: "half", Region: tc.half}, {Text: "under", Region: tc.under}}}
			got := relatedTokens(page, tc.anchor, Relation{Kind: tc.kind, MaxDistance: maxDistance}, 0)
			if len(got) != 1 || got[0].index != 0 {
				t.Errorf("relatedTokens = %+v, want exactly the half token (index 0)", got)
			}
		})
	}
}

// Only right and below are geometric: any other kind relates to nothing and fails a clause.
func TestRelatedTokens_AnUnknownKindRelatesToNothing(t *testing.T) {
	value := Region{Page: 1, X0: 0.60, Y0: 0.40, X1: 0.70, Y1: 0.43}
	if got := relatedTokens(relOnePage(value), relAnchor, Relation{Kind: RelRight, MaxDistance: 0.35}, 0); len(got) != 1 {
		t.Fatalf("control: right relates %d token(s), want 1; the zeros below would prove nothing", len(got))
	}

	for _, kind := range []RelationKind{RelSameToken, "diagonal", ""} {
		if got := relatedTokens(relOnePage(value), relAnchor, Relation{Kind: kind, MaxDistance: 0.35}, 0); len(got) != 0 {
			t.Errorf("kind %q relates %d token(s), want 0", kind, len(got))
		}
		if order, distance, overlap := RelationClausesForTest(relAnchor, value, kind, 0.35, 0); !order && !distance && !overlap {
			t.Errorf("kind %q fails no clause, want at least one", kind)
		}
	}
}

// An unusable box relates to nothing even where every clause admits its geometry.
func TestRelatedTokens_SkipsAnUnusableBoxTheClausesAdmit(t *testing.T) {
	rel := Relation{Kind: RelRight, MaxDistance: 0.35}
	good := Region{Page: 1, X0: 0.60, Y0: 0.40, X1: 0.70, Y1: 0.43}
	if got := relatedTokens(relOnePage(good), relAnchor, rel, 0); len(got) != 1 {
		t.Fatalf("control: a usable pair relates %d token(s), want 1", len(got))
	}

	cases := []struct {
		name          string
		anchor, value Region
	}{
		{"value on page 0", relAnchor, Region{Page: 0, X0: 0.60, Y0: 0.40, X1: 0.70, Y1: 0.43}},
		{"value past the right edge", relAnchor, Region{Page: 1, X0: 0.60, Y0: 0.40, X1: 1.20, Y1: 0.43}},
		{"anchor on page 0", Region{Page: 0, X0: 0.40, Y0: 0.40, X1: 0.55, Y1: 0.43}, good},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if order, distance, overlap := RelationClausesForTest(tc.anchor, tc.value, rel.Kind, rel.MaxDistance, 0); order || distance || overlap {
				t.Fatalf("order=%v distance=%v overlap=%v; a clause rejects this pair, so the usableBox guard is not what it reaches", order, distance, overlap)
			}
			if got := relatedTokens(relOnePage(tc.value), tc.anchor, rel, 0); len(got) != 0 {
				t.Errorf("relates %d token(s), want 0", len(got))
			}
		})
	}
}
