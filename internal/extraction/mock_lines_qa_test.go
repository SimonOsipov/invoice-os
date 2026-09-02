// mock_lines_qa_test.go: QA (Mode B) coverage for the four-line block EXTR-13-02 adds to
// mockDefaultResult. The RED specs pin the block's SHAPE -- 23 rows, the hole at line 3, the
// flag on line 2. These pin the two properties that shape leaves open: the boxes must be
// DISJOINT (not merely unequal), and every row must survive the column CHECKs
// migrations/20260827100320_extraction_field_results.sql declares.
//
// Every clause counts what it examined and calls Fatalf at zero, and each absence claim is
// paired with a control that shows the same predicate finding what IS there.
package extraction_test

import (
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// mqOverlap is a strict interval overlap on both axes, same page. Two boxes that merely touch
// at an edge do not overlap; two that share any area do.
func mqOverlap(a, b extraction.Region) bool {
	return a.Page == b.Page && a.X0 < b.X1 && b.X0 < a.X1 && a.Y0 < b.Y1 && b.Y0 < a.Y1
}

// mqIsLineName reports whether name is the block row or one of the 16 LineFieldName shapes.
func mqIsLineName(name string) bool {
	if name == "line_items" {
		return true
	}
	for n := 1; n <= 4; n++ {
		for _, role := range extraction.LineRoles {
			if extraction.LineFieldName(n, role) == name {
				return true
			}
		}
	}
	return false
}

type mqBox struct {
	name   string
	region extraction.Region
}

// mqSplitBoxes returns every box the default result carries, split into line cells and header
// readings. A header ALTERNATIVE carries its own box, so it is collected too: issue_date's
// third reading sits at 0.10-0.38 x 0.50-0.55, directly under the grid's last row.
func mqSplitBoxes(t *testing.T) (cells, headers []mqBox) {
	t.Helper()
	for _, r := range mxDefault(t) {
		if r.Region != nil {
			b := mqBox{name: r.Name, region: *r.Region}
			if mqIsLineName(r.Name) {
				cells = append(cells, b)
			} else {
				headers = append(headers, b)
			}
		}
		for i, alt := range r.Alternatives {
			if alt.Region == nil {
				continue
			}
			b := mqBox{name: r.Name + "/alternative-" + string(rune('1'+i)), region: *alt.Region}
			if mqIsLineName(r.Name) {
				cells = append(cells, b)
			} else {
				headers = append(headers, b)
			}
		}
	}
	return cells, headers
}

// TestMockExtractor_LineCellBoxesOverlapNothing: the shipped AC-6 spec compares regions with
// ==, which two boxes overlapping by all but a hair would pass. The point-at-it highlight and
// the field<->region hover both need DISJOINT areas, and the grid must also clear every header
// box -- issue_date's third reading being the near one.
func TestMockExtractor_LineCellBoxesOverlapNothing(t *testing.T) {
	// The control comes first: a predicate that returned false for everything would make every
	// claim below vacuous.
	one := extraction.Region{Page: 1, X0: 0.10, Y0: 0.30, X1: 0.44, Y1: 0.34}
	if !mqOverlap(one, extraction.Region{Page: 1, X0: 0.20, Y0: 0.31, X1: 0.50, Y1: 0.33}) {
		t.Fatal("mqOverlap missed a pair sharing most of their area; every claim below would be vacuous")
	}
	if mqOverlap(one, extraction.Region{Page: 2, X0: 0.10, Y0: 0.30, X1: 0.44, Y1: 0.34}) {
		t.Fatal("mqOverlap ignores the page; two boxes on different pages cannot overlap")
	}

	cells, headers := mqSplitBoxes(t)
	if len(cells) != 15 {
		t.Fatalf("collected %d line-cell box(es), want 15; the sweeps below would examine the wrong population", len(cells))
	}
	if len(headers) < 6 {
		t.Fatalf("collected %d header box(es), want at least 6 -- the two issue_date alternatives included; the clearance sweep would under-examine", len(headers))
	}

	for i := range cells {
		for j := i + 1; j < len(cells); j++ {
			if mqOverlap(cells[i].region, cells[j].region) {
				t.Errorf("%s %+v overlaps %s %+v; a cell highlight would cover its neighbour",
					cells[i].name, cells[i].region, cells[j].name, cells[j].region)
			}
		}
	}
	for _, c := range cells {
		for _, h := range headers {
			if mqOverlap(c.region, h.region) {
				t.Errorf("%s %+v overlaps the header box %s %+v; pointing at a header would land inside the grid",
					c.name, c.region, h.name, h.region)
			}
		}
	}
}

// TestMockExtractor_LineRowsSurviveTheStoredColumnChecks: writeFieldResultRowTx binds these
// straight into extraction_field_results, whose CHECKs are the only validation there is
// (store.go:133-136). A row that violates one surfaces as a 23514 inside the worker, on the
// deployed fleet, with no unit test between here and there.
func TestMockExtractor_LineRowsSurviveTheStoredColumnChecks(t *testing.T) {
	var examined, valued, longest int
	for _, r := range mxDefault(t) {
		if !mqIsLineName(r.Name) {
			continue
		}
		examined++

		if n := len(r.Name); n == 0 || n > 128 {
			t.Errorf("%s: field_name is %d character(s); the column CHECK admits 1..128", r.Name, n)
		}
		if r.Value != nil {
			valued++
			if strings.TrimSpace(*r.Value) == "" {
				t.Errorf("%s: value is %q; the column CHECK admits NULL or a non-empty string, and a blank reads as a value that is not there", r.Name, *r.Value)
			}
			if n := len(*r.Value); n > longest {
				longest = n
			}
		}
		if r.Region != nil {
			g := *r.Region
			if g.Page < 1 {
				t.Errorf("%s: page = %d; the column CHECK admits NULL or >= 1", r.Name, g.Page)
			}
			if !(g.X0 >= 0 && g.X0 <= g.X1 && g.X1 <= 1 && g.Y0 >= 0 && g.Y0 <= g.Y1 && g.Y1 <= 1) {
				t.Errorf("%s: box %+v is outside extraction_field_results_bbox_normalised", r.Name, g)
			}
		}
		// Alternatives are written as their own rows at ranks 1..N; a line cell carries none,
		// but say so rather than leaving the arm unexamined.
		if len(r.Alternatives) != 0 {
			t.Errorf("%s carries %d alternative(s); a line cell has no competing reading", r.Name, len(r.Alternatives))
		}
	}

	if examined != 16 {
		t.Fatalf("examined %d line row(s), want 16 -- the block row plus 15 cells", examined)
	}
	if valued != 15 {
		t.Errorf("%d line row(s) carry a value, want 15 -- the block row carries none and every cell carries one", valued)
	}
	// The control on the value sweep: line 2's description is long by design (it is what
	// exercises the grid column's width), so a fixture flattened to short values would leave
	// the length claim above proving nothing about a realistic row.
	if longest < 60 {
		t.Errorf("the longest line value is %d character(s); line 2's description is meant to be long enough to exercise the grid column", longest)
	}
}
