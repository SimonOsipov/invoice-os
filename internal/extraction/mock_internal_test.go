// mock_internal_test.go: mockLineRegions' own contract (mock.go:170-171) -- a role a line never
// emits gets no box, so widening LineRoles must not mint a zero-width region for it at a
// LineRoles... call site that was not updated to an explicit role list. package extraction:
// mockDefaultLines, mockLineRegions and mockLineColumns are all unexported, and no test file
// reaches them yet.
package extraction

import "testing"

// TestMockLineRegions_CarriesNoBoxForACellItNeverEmits pins AC-7's mock hazard: every entry in a
// mock line's Regions map must be non-degenerate (X1 > X0) and must name a role that
// LineItemResults actually emits for that line -- never dead data a widened LineRoles... call
// left behind.
func TestMockLineRegions_CarriesNoBoxForACellItNeverEmits(t *testing.T) {
	emitted := make(map[string]bool, len(mockDefaultLines)*len(LineRoles))
	for _, fr := range LineItemResults(mockDefaultLines) {
		emitted[fr.Name] = true
	}
	if len(emitted) == 0 {
		t.Fatal("LineItemResults emitted nothing for mockDefaultLines; the sweep below would hold vacuously")
	}

	var scanned int
	for _, line := range mockDefaultLines {
		for role, region := range line.Regions {
			scanned++
			if region == nil {
				t.Errorf("line %d Regions[%q] is nil in the map itself", line.Index, role)
				continue
			}
			if region.X1 <= region.X0 {
				t.Errorf("line %d Regions[%q] is zero-width or inverted: %+v -- a role the line never emits must carry no box at all, not a degenerate one", line.Index, role, *region)
			}
			name := LineFieldName(line.Index, role)
			if !emitted[name] {
				t.Errorf("line %d Regions[%q] carries a box for %q, but LineItemResults never emits it -- dead data mockLineRegions' own doc comment forbids", line.Index, role, name)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("mockDefaultLines carries no Regions entry at all; the sweep above examined nothing")
	}
}
