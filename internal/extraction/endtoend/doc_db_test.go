// doc_db_test.go: the doc's three tables compared row by row against a live walk. Its own walk,
// not one shared with the score specs: a shared walk would let one broken measurement satisfy
// both oracles.
package endtoend

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// eeDocInt parses one table cell.
func eeDocInt(t *testing.T, row, what, raw string) int {
	t.Helper()
	n, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("%s row %q: %s %q is not a number: %v", eeDocFile, row, what, raw, err)
	}
	return n
}

// eeRowsByName indexes a walk's report rows.
func eeRowsByName(rows []eeScoreRow) map[string]eeScoreRow {
	out := make(map[string]eeScoreRow, len(rows))
	for _, r := range rows {
		out[r.name] = r
	}
	return out
}

// AC-4. Every row of every table in the two new doc sections, against one live walk. A table
// that sums correctly with the numbers in the wrong rows is still red.
func TestRLS_EndToEndDocRecordsTheMeasuredTables(t *testing.T) {
	eeRequire(t)
	ctx := t.Context()

	s := eeScoreCorpus(t, ctx)
	report := eeRenderReport(s)

	// Floors first: an empty walk satisfies every comparison below.
	if len(s.byLayout) != eeLayoutCount || len(s.byField) != len(writtenFields) {
		t.Fatalf("the walk reported %d layout row(s) and %d field row(s), want %d and %d", len(s.byLayout), len(s.byField), eeLayoutCount, len(writtenFields))
	}
	if s.total != eeCorpusCells {
		t.Fatalf("the walk scored %d cell(s), want %d", s.total, eeCorpusCells)
	}
	if s.hits == 0 {
		t.Fatalf("the walk scored 0 hit(s); the tables below would be compared against a broken measurement:\n%s", report)
	}

	doc := wildReadFile(t, eeDocFile)
	acc := eeDocSection(t, doc, eeDocAccSection)
	// Re-asserted here: this test parses too, and may not trust the pure spec having run.
	eeDocAssertRowCounts(t, acc)

	byLayout := eeRowsByName(s.byLayout)
	layoutHits, layoutCells := 0, 0
	named := map[string]bool{}
	for _, m := range eeDocLayoutRowRE.FindAllStringSubmatch(acc, -1) {
		h := eeDocInt(t, m[1], "hits", m[2])
		n := eeDocInt(t, m[1], "cells", m[3])
		if named[m[1]] {
			t.Errorf("%s's per-layout table names %s twice", eeDocFile, m[1])
		}
		named[m[1]] = true
		layoutHits += h
		layoutCells += n

		want, ok := byLayout[m[1]]
		if !ok {
			t.Errorf("%s has a per-layout row for %q, which is not a scored layout", eeDocFile, m[1])
			continue
		}
		if h != want.hits || n != want.total {
			t.Errorf("%s says %s is %d/%d; it measures %d/%d", eeDocFile, m[1], h, n, want.hits, want.total)
		}
	}
	for _, want := range expectByLayout {
		if !named[want.file] {
			t.Errorf("%s's per-layout table has no row for %s", eeDocFile, want.file)
		}
	}
	if layoutHits != eeCorpusHits || layoutCells != eeCorpusCells {
		t.Errorf("%s's per-layout table sums to %d/%d, want %d/%d; the doc and the constants disagree", eeDocFile, layoutHits, layoutCells, eeCorpusHits, eeCorpusCells)
	}

	byField := eeRowsByName(s.byField)
	fieldHits, fieldCells := 0, 0
	namedField := map[string]bool{}
	for _, m := range eeDocFieldRowRE.FindAllStringSubmatch(acc, -1) {
		h := eeDocInt(t, m[1], "hits", m[2])
		n := eeDocInt(t, m[1], "cells", m[3])
		if namedField[m[1]] {
			t.Errorf("%s's per-field table names %s twice", eeDocFile, m[1])
		}
		namedField[m[1]] = true
		fieldHits += h
		fieldCells += n

		want, ok := byField[m[1]]
		if !ok {
			t.Errorf("%s has a per-field row for %q, which is not a written field", eeDocFile, m[1])
			continue
		}
		if h != want.hits || n != want.total {
			t.Errorf("%s says %s is %d/%d; it measures %d/%d", eeDocFile, m[1], h, n, want.hits, want.total)
		}
	}
	for _, field := range writtenFields {
		if !namedField[field] {
			t.Errorf("%s's per-field table has no row for %s", eeDocFile, field)
		}
	}
	if fieldHits != eeCorpusHits || fieldCells != eeCorpusCells {
		t.Errorf("%s's per-field table sums to %d/%d, want %d/%d; the doc and the constants disagree", eeDocFile, fieldHits, fieldCells, eeCorpusHits, eeCorpusCells)
	}
	// The two sums alone report one table updated and the other not as the same defect twice.
	if layoutHits != fieldHits || layoutCells != fieldCells {
		t.Errorf("%s's per-layout table sums to %d/%d and its per-field table to %d/%d; one was re-measured and the other was not", eeDocFile, layoutHits, layoutCells, fieldHits, fieldCells)
	}

	line := eeDocSection(t, doc, eeDocLineSection)
	rows := eeDocLineRowRE.FindAllStringSubmatch(line, -1)
	if len(rows) != eeLayoutCount {
		t.Fatalf("%s's %q section holds %d four-column row(s), want %d; a row reads exactly: | `layout.pdf` | <reached> | <expected> | <priced> |", eeDocFile, eeDocLineSection, len(rows), eeLayoutCount)
	}
	reached := eeRowsByName(s.linesReached)
	priced := eeRowsByName(s.linesPriced)
	namedLine := map[string]bool{}
	for _, m := range rows {
		r := eeDocInt(t, m[1], "reached", m[2])
		e := eeDocInt(t, m[1], "expected", m[3])
		p := eeDocInt(t, m[1], "priced", m[4])
		if namedLine[m[1]] {
			t.Errorf("%s's line table names %s twice", eeDocFile, m[1])
		}
		namedLine[m[1]] = true

		wantReached, ok := reached[m[1]]
		if !ok {
			t.Errorf("%s has a line row for %q, which is not a scored layout", eeDocFile, m[1])
			continue
		}
		wantPriced := priced[m[1]]
		if r != wantReached.hits {
			t.Errorf("%s says %s reached %d line(s); it measures %d", eeDocFile, m[1], r, wantReached.hits)
		}
		if e != eeLinesExpected[m[1]] || e != wantReached.total {
			t.Errorf("%s says %s carries %d line(s); eeLinesExpected says %d and the walk scored against %d", eeDocFile, m[1], e, eeLinesExpected[m[1]], wantReached.total)
		}
		if p != wantPriced.hits {
			t.Errorf("%s says %s priced %d line(s); it measures %d", eeDocFile, m[1], p, wantPriced.hits)
		}
		// priced counts a subset of reached, so reached IS its denominator.
		if wantPriced.total != wantReached.hits {
			t.Errorf("the walk scored %s's priced figure against %d, want %d", m[1], wantPriced.total, wantReached.hits)
		}
	}
	for _, want := range expectByLayout {
		if !namedLine[want.file] {
			t.Errorf("%s's line table has no row for %s", eeDocFile, want.file)
		}
	}
	if control := fmt.Sprintf("%d / %d", eeLineControlReached, eeLineControlPriced); !strings.Contains(line, control) {
		t.Errorf("%s's %q section does not carry the control's %s; every zero above is satisfied by a scorer that reads nothing", eeDocFile, eeDocLineSection, control)
	}
}
