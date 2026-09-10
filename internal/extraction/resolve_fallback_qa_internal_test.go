// resolve_fallback_qa_internal_test.go: what AC-2.4 leaves open. That spec walks five
// candidates and asserts exactly-one-of over every pair, which proves the order is total on
// that set -- but every pair there is separated by Tier or Distance, so a comparator that had
// LOST its Region, Value or RuleID key would still pass it. Here each pair ties through every
// earlier key and is separated by exactly one, named in its own failure message.
package extraction

import "testing"

func TestResolve_ComparatorBreaksEveryTieOnTheNextKeyOverThreeTiers(t *testing.T) {
	r1 := &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.20, Y1: 0.13}
	r2 := &Region{Page: 1, X0: 0.10, Y0: 0.40, X1: 0.20, Y1: 0.43} // strictly later in reading order

	base := Candidate{Field: "total", Value: "V", Region: r1, RuleID: "a", Tier: TierFallback, Distance: 0.10}

	// Each variant is base with ONE key changed to a strictly greater value, so base must sort
	// first in every row -- except byTier, where the variant's TierGeneric outranks TierFallback.
	rows := []struct {
		key        string
		other      Candidate
		baseIsLess bool
	}{
		{"Tier", func() Candidate { c := base; c.Tier = TierGeneric; return c }(), false},
		{"Distance", func() Candidate { c := base; c.Distance = 0.20; return c }(), true},
		{"Region", func() Candidate { c := base; c.Region = r2; return c }(), true},
		{"Value", func() Candidate { c := base; c.Value = "W"; return c }(), true},
		{"RuleID", func() Candidate { c := base; c.RuleID = "b"; return c }(), true},
	}
	if len(rows) != 5 {
		t.Fatalf("the chain walks %d key(s), want 5; compareCandidates has five and a row per key is what makes each one load-bearing", len(rows))
	}

	for _, row := range rows {
		fwd := compareCandidates(base, row.other)
		rev := compareCandidates(row.other, base)
		if fwd == 0 {
			t.Errorf("compareCandidates ties two candidates differing only in %s; that key no longer orders anything and the sort is not total", row.key)
			continue
		}
		if (fwd < 0) != (rev > 0) {
			t.Errorf("compareCandidates is not antisymmetric on the %s pair: got %d forward and %d reverse", row.key, fwd, rev)
		}
		if got := fwd < 0; got != row.baseIsLess {
			t.Errorf("compareCandidates puts base %s the %s variant, want %s: the key points the wrong way",
				map[bool]string{true: "before", false: "after"}[got], row.key,
				map[bool]string{true: "before", false: "after"}[row.baseIsLess])
		}
	}

	// Totality over the whole set, base included: no two of the six tie, and each is reflexive.
	set := make([]Candidate, 0, len(rows)+1)
	set = append(set, base)
	for _, row := range rows {
		set = append(set, row.other)
	}
	if len(set) != 6 {
		t.Fatalf("the set holds %d candidate(s), want 6; the pair walk below quantifies over it", len(set))
	}
	pairs := 0
	for i := range set {
		if got := compareCandidates(set[i], set[i]); got != 0 {
			t.Errorf("compareCandidates is not reflexive on index %d: got %d, want 0", i, got)
		}
		for j := i + 1; j < len(set); j++ {
			pairs++
			ij := compareCandidates(set[i], set[j])
			ji := compareCandidates(set[j], set[i])
			if ij == 0 {
				t.Errorf("compareCandidates(%d, %d) == 0 but the two differ; the order is not total", i, j)
			}
			if (ij > 0) != (ji < 0) || (ij < 0) != (ji > 0) {
				t.Errorf("compareCandidates is not antisymmetric on (%d, %d): got %d and %d", i, j, ij, ji)
			}
		}
	}
	if pairs != 15 {
		t.Fatalf("the walk compared %d pair(s), want 15; a shrunken set is a quantifier over nothing", pairs)
	}
}
