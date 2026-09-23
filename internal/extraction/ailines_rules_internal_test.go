// Specs for the line-item scoring core -- role readings, the five-way cell verdict, and the
// order-preserving three-pass row alignment. Helpers live in ailines_internal_test.go.
package extraction

import "testing"

// --- test-local fixture builders (no plan helper lives here) ---------------------------

// aliMenuLine builds one line-item row with quantity and no tax cell, the shape every
// repeated-menu row shares.
func aliMenuLine(index int, description, quantity, unitPrice, lineTotal string) DocLine {
	return DocLine{
		Index:       index,
		Description: aliStr(description),
		Quantity:    aliStr(quantity),
		UnitPrice:   aliStr(unitPrice),
		LineTotal:   aliStr(lineTotal),
	}
}

// aliTitleLine builds a row with only description and line_total -- the title-only fixture's
// shape, where quantity and unit_price are genuinely absent from the reader's table.
func aliTitleLine(index int, description, lineTotal string) DocLine {
	return DocLine{
		Index:       index,
		Description: aliStr(description),
		LineTotal:   aliStr(lineTotal),
	}
}

// aliRepeatedMenuKey is the 15-row key: three repeats of a five-item menu, quantity 80 and no
// tax line on every row. Verbatim from the source document -- menu items carry no personal data.
func aliRepeatedMenuKey() []DocLine {
	rows := [][3]string{
		{"SMOKEY JOLLOF RICE", "700", "56000.00"},
		{"FRIED RICE", "700", "56000.00"},
		{"SALAD", "6000", "480000.00"},
		{"TURKEY", "6500", "520000.00"},
		{"HAKE FISH", "2500", "200000.00"},
		{"SMOKEY JOLLOF RICE", "700", "56000.00"},
		{"FRIED RICE", "700", "56000.00"},
		{"SALAD", "6000", "480000.00"},
		{"FRIED PEPPEED CHICKEN", "4000", "320000.00"},
		{"HAKE FISH", "2500", "200000.00"},
		{"SMOKEY JOLLOF RICE", "700", "56000.00"},
		{"FRIED RICE", "700", "56000.00"},
		{"SALAD", "6000", "480000.00"},
		{"SMALL CHICKEN", "1500", "120000.00"},
		{"HAKE FISH", "2500", "200000.00"},
	}
	out := make([]DocLine, len(rows))
	for i, r := range rows {
		out[i] = aliMenuLine(i+1, r[0], "80", r[1], r[2])
	}
	return out
}

func intSliceEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// aliSweepCells classifies every cell of every pair. Fatal on an empty pair list: a right/wrong
// count over zero pairs would be vacuous.
func aliSweepCells(t *testing.T, pairs []aliPair, answer, key []DocLine) (right, wrong, missing int) {
	t.Helper()
	if len(pairs) == 0 {
		t.Fatal("no pairs to score")
	}
	for _, p := range pairs {
		a, k := answer[p.Answer-1], key[p.Key-1]
		for _, role := range LineRoles {
			switch aliClassifyCell(role, a.Cell(role), k.Cell(role)) {
			case aliRight:
				right++
			case aliWrong:
				wrong++
			case aliMissing:
				missing++
			}
		}
	}
	return right, wrong, missing
}

// aliCountValueAndNullCells splits a pair set's cells by whether the key side carries a value,
// so a flat "70 right" total can't hide 14 of those rights being trivial correct nulls.
func aliCountValueAndNullCells(pairs []aliPair, key []DocLine) (value, null int) {
	for _, p := range pairs {
		k := key[p.Key-1]
		for _, role := range LineRoles {
			if aliKeyHasValue(k.Cell(role)) {
				value++
			} else {
				null++
			}
		}
	}
	return value, null
}

// --- fixtures: each returns (answer, key) as literal DocLine slices, no file read ----------

func aliFixtureClean() (answer, key []DocLine) {
	return aliRepeatedMenuKey(), aliRepeatedMenuKey()
}

func aliFixtureMenu1Only() (answer, key []DocLine) {
	k := aliRepeatedMenuKey()
	return k[:5], k
}

// aliFixtureTotalsBandTiny forces the answer-index tie-break: both answer rows disagree with
// the key's description (so pass 1 finds nothing), but both share the key's line_total (so pass
// 2's amount anchor sees two equally valid candidates for the one key row).
func aliFixtureTotalsBandTiny() (answer, key []DocLine) {
	key = []DocLine{aliMenuLine(1, "SYNTHETIC LINE", "1", "5000.00", "5000.00")}
	answer = []DocLine{
		aliMenuLine(1, "ITEM ONE", "1", "5000.00", "5000.00"),
		aliMenuLine(2, "TOTAL PAID", "1", "5000.00", "5000.00"),
	}
	return answer, key
}

func aliFixtureTitleOnly() (answer, key []DocLine) {
	key = []DocLine{
		aliTitleLine(1, "Advisory engagement review Reference AB-2026-14 - 62 hours", "310000.00"),
		aliTitleLine(2, "Legal consultation review Reference CD-2026-22 - 40 hours", "220000.00"),
		aliTitleLine(3, "Audit support review Reference EF-2026-31 - 18 hours", "95000.00"),
	}
	answer = []DocLine{
		aliTitleLine(1, "Advisory engagement review", "310000.00"),
		aliTitleLine(2, "Legal consultation review", "220000.00"),
		aliTitleLine(3, "Audit support review", "95000.00"),
	}
	return answer, key
}

func aliFixtureDroppedMiddle() (answer, key []DocLine) {
	key = []DocLine{
		aliMenuLine(1, "ITEM ALPHA", "2", "1000.00", "2000.00"),
		aliMenuLine(2, "ITEM BETA", "3", "1500.00", "4500.00"),
		aliMenuLine(3, "ITEM GAMMA", "1", "3000.00", "3000.00"),
		aliMenuLine(4, "ITEM DELTA", "5", "800.00", "4000.00"),
	}
	answer = []DocLine{key[0], key[2], key[3]}
	return answer, key
}

func aliFixtureZeroKey() (answer, key []DocLine) {
	answer = []DocLine{
		aliMenuLine(1, "ITEM ALPHA", "2", "1000.00", "2000.00"),
		aliMenuLine(2, "ITEM BETA", "3", "1500.00", "4500.00"),
	}
	return answer, []DocLine{}
}

func aliFixtureMisreadGap() (answer, key []DocLine) {
	key = []DocLine{
		aliMenuLine(1, "ITEM ALPHA", "2", "1000.00", "2000.00"),
		aliMenuLine(2, "ITEM BETA", "3", "1500.00", "4500.00"),
		aliMenuLine(3, "ITEM GAMMA", "1", "3000.00", "3000.00"),
	}
	answer = []DocLine{
		key[0],
		aliMenuLine(2, "GARBLED TEXT", "3", "1500.00", "9999.00"),
		key[2],
	}
	return answer, key
}

func aliFixtureShiftByOne() (answer, key []DocLine) {
	key = aliRepeatedMenuKey()
	return key[1:], key
}

// aliFixtureSwap swaps two 0-based positions of a fresh copy of the repeated-menu key, leaving
// the caller's own key copy untouched.
func aliFixtureSwap(i, j int) (answer, key []DocLine) {
	key = aliRepeatedMenuKey()
	answer = aliRepeatedMenuKey()
	answer[i], answer[j] = answer[j], answer[i]
	return answer, key
}

func aliFixtureMenusOutOfOrder() (answer, key []DocLine) {
	key = aliRepeatedMenuKey()
	answer = append(append(append([]DocLine{}, key[10:15]...), key[5:10]...), key[0:5]...)
	return answer, key
}

func aliFixtureTotalsBandFullKey() (answer, key []DocLine) {
	key = aliRepeatedMenuKey()
	extra := aliMenuLine(16, "S/TOTAL", "0", "0.00", "5776000.00")
	answer = append(aliRepeatedMenuKey(), extra)
	return answer, key
}

// --- role readings --------------------------------------------------------------------------

func TestAliRoleReadings_AnAmountIgnoresTrailingZeros(t *testing.T) {
	a := aliRoleReadings(LineRoleUnitPrice, "700")
	b := aliRoleReadings(LineRoleUnitPrice, "700.00")
	if len(a) == 0 || len(b) == 0 {
		t.Fatalf("readings = %v, %v, want both non-empty", a, b)
	}
	if !hasCommonReading(a, b) {
		t.Errorf("700 and 700.00 share no reading: %v vs %v", a, b)
	}
}

func TestAliRoleReadings_AQuantityIgnoresTrailingZeros(t *testing.T) {
	a := aliRoleReadings(LineRoleQuantity, "1.00")
	b := aliRoleReadings(LineRoleQuantity, "1")
	if len(a) == 0 || len(b) == 0 {
		t.Fatalf("readings = %v, %v, want both non-empty", a, b)
	}
	if !hasCommonReading(a, b) {
		t.Errorf("1.00 and 1 share no reading: %v vs %v", a, b)
	}
}

func TestAliRoleReadings_ADifferentAmountStaysDifferent(t *testing.T) {
	a := aliRoleReadings(LineRoleLineTotal, "56000.00")
	b := aliRoleReadings(LineRoleLineTotal, "5600.00")
	if len(a) == 0 || len(b) == 0 {
		t.Fatalf("readings = %v, %v, want both non-empty", a, b)
	}
	if hasCommonReading(a, b) {
		t.Errorf("56000.00 and 5600.00 share a reading: %v vs %v", a, b)
	}
}

func TestAliRoleReadings_ADescriptionFoldsCaseAndDoubleSpaces(t *testing.T) {
	a := aliRoleReadings(LineRoleDescription, "LOCAL  TICKET   ROUTE ABC-DEF-ABC  01 JANUARY 2026")
	b := aliRoleReadings(LineRoleDescription, "local ticket route abc-def-abc 01 january 2026")
	if len(a) == 0 || len(b) == 0 {
		t.Fatalf("readings = %v, %v, want both non-empty", a, b)
	}
	if !hasCommonReading(a, b) {
		t.Errorf("case/space variants share no reading: %v vs %v", a, b)
	}
}

func TestAliRoleReadings_ATitleOnlyDescriptionDiffersFromTheJoinedTruth(t *testing.T) {
	a := aliRoleReadings(LineRoleDescription, "Advisory engagement review Reference AB-2026-14 - 62 hours")
	b := aliRoleReadings(LineRoleDescription, "Advisory engagement review")
	if len(a) == 0 || len(b) == 0 {
		t.Fatalf("readings = %v, %v, want both non-empty", a, b)
	}
	if hasCommonReading(a, b) {
		t.Errorf("a title-only description matched the fully joined line: %v vs %v", a, b)
	}
}

func TestAliRoleReadings_ABlankRawHasNoReading(t *testing.T) {
	if len(LineRoles) != 5 {
		t.Fatalf("len(LineRoles) = %d, want 5", len(LineRoles))
	}
	for _, role := range LineRoles {
		if got := aliRoleReadings(role, "  "); len(got) != 0 {
			t.Errorf("aliRoleReadings(%q, blank) = %v, want no reading", role, got)
		}
	}
}

func TestAliRoleReadings_ARefusedNumericRawHasNoReading(t *testing.T) {
	if got := aliRoleReadings(LineRoleQuantity, "80 pcs"); len(got) != 0 {
		t.Errorf("aliRoleReadings(quantity, %q) = %v, want no reading", "80 pcs", got)
	}
	if got := aliRoleReadings(LineRoleLineTax, "7.50%"); len(got) != 0 {
		t.Errorf("aliRoleReadings(line_tax, %q) = %v, want no reading", "7.50%", got)
	}
}

// --- cell verdicts --------------------------------------------------------------------------

func TestAliClassifyCell_AMatchingReadingAgainstAKeyValueIsRight(t *testing.T) {
	if got := aliClassifyCell(LineRoleLineTotal, aliStr("56000"), aliStr("56000.00")); got != aliRight {
		t.Errorf("aliClassifyCell = %q, want %q", got, aliRight)
	}
}

func TestAliClassifyCell_ADifferentReadingAgainstAKeyValueIsWrong(t *testing.T) {
	if got := aliClassifyCell(LineRoleLineTotal, aliStr("5600.00"), aliStr("56000.00")); got != aliWrong {
		t.Errorf("aliClassifyCell = %q, want %q", got, aliWrong)
	}
}

func TestAliClassifyCell_NoAnswerAgainstAKeyValueIsMissing(t *testing.T) {
	if got := aliClassifyCell(LineRoleLineTotal, nil, aliStr("56000.00")); got != aliMissing {
		t.Errorf("aliClassifyCell = %q, want %q", got, aliMissing)
	}
}

func TestAliClassifyCell_AnAnswerAgainstANullKeyCellIsWrong(t *testing.T) {
	if got := aliClassifyCell(LineRoleLineTax, aliStr("11.25"), nil); got != aliWrong {
		t.Errorf("aliClassifyCell = %q, want %q", got, aliWrong)
	}
}

func TestAliClassifyCell_NoAnswerAgainstANullKeyCellIsRight(t *testing.T) {
	if aliKeyHasValue(nil) {
		t.Fatal("aliKeyHasValue(nil) = true, want false")
	}
	if got := aliClassifyCell(LineRoleLineTax, nil, nil); got != aliRight {
		t.Errorf("aliClassifyCell = %q, want %q", got, aliRight)
	}
}

// --- monotone matching ----------------------------------------------------------------------

func TestAliMonotoneMatch_NeverCrossesTwoPairs(t *testing.T) {
	const classA, classB = 0, 1
	answer := []int{classB, classA}
	key := []int{classA, classB}
	compatible := func(a, k int) bool { return a == k }

	got := aliMonotoneMatch(answer, key, compatible)
	if len(got) == 0 {
		t.Fatal("no pairs, want the size-1 monotone maximum")
	}
	if want := (aliPairIdx{Answer: 1, Key: 0}); len(got) != 1 || got[0] != want {
		t.Fatalf("got %v, want [%v]", got, want)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Answer <= got[i-1].Answer || got[i].Key <= got[i-1].Key {
			t.Errorf("pair %d is not strictly increasing after pair %d: %v", i, i-1, got)
		}
	}
}

func TestAliMonotoneMatch_TiesTakeTheEarliestKeyRow(t *testing.T) {
	const class = 7
	answer := []int{class}
	key := []int{class, class}
	compatible := func(a, k int) bool { return a == k }

	got := aliMonotoneMatch(answer, key, compatible)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].Key != 0 {
		t.Errorf("got[0].Key = %d, want 0 (the earliest of two identical key rows)", got[0].Key)
	}
}

// --- the three passes, in order -------------------------------------------------------------

func TestAliAlign_TheBukkaHutMenusPairInPrintedOrder(t *testing.T) {
	answer, key := aliFixtureClean()

	got := aliAlign(answer, key)
	aliCheckPartition(t, got, len(answer), len(key))
	if len(got.Pairs) != 15 {
		t.Fatalf("len(Pairs) = %d, want 15", len(got.Pairs))
	}
	for i, p := range got.Pairs {
		want := i + 1
		if p.Answer != want || p.Key != want || p.Pass != aliPassStrong {
			t.Errorf("pair %d = %+v, want (answer %d, key %d, strong anchor)", i, p, want, want)
		}
	}
	if len(got.Invented) != 0 {
		t.Errorf("Invented = %v, want none", got.Invented)
	}
	if len(got.Dropped) != 0 {
		t.Errorf("Dropped = %v, want none", got.Dropped)
	}
	if got.Misaligned() != 0 {
		t.Errorf("Misaligned() = %d, want 0", got.Misaligned())
	}

	right, wrong, missing := aliSweepCells(t, got.Pairs, answer, key)
	if total := right + wrong + missing; total != 75 {
		t.Fatalf("scored %d cells, want 75", total)
	}
	if right != 75 || wrong != 0 || missing != 0 {
		t.Errorf("right/wrong/missing = %d/%d/%d, want 75/0/0", right, wrong, missing)
	}
}

func TestAliAlign_OneMenuOnlyDropsTheOtherTenInOrder(t *testing.T) {
	answer, key := aliFixtureMenu1Only()

	got := aliAlign(answer, key)
	aliCheckPartition(t, got, len(answer), len(key))
	if len(got.Pairs) != 5 {
		t.Fatalf("len(Pairs) = %d, want 5", len(got.Pairs))
	}
	for i, p := range got.Pairs {
		want := i + 1
		if p.Answer != want || p.Key != want {
			t.Errorf("pair %d = %+v, want (answer %d, key %d)", i, p, want, want)
		}
	}
	if len(got.Invented) != 0 {
		t.Errorf("Invented = %v, want none", got.Invented)
	}
	wantDropped := []int{6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	if !intSliceEqual(got.Dropped, wantDropped) {
		t.Errorf("Dropped = %v, want %v", got.Dropped, wantDropped)
	}

	right, wrong, missing := aliSweepCells(t, got.Pairs, answer, key)
	if total := right + wrong + missing; total != 25 {
		t.Fatalf("scored %d cells, want 25", total)
	}
	if right != 25 || wrong != 0 || missing != 0 {
		t.Errorf("right/wrong/missing = %d/%d/%d, want 25/0/0", right, wrong, missing)
	}
}

func TestAliAlign_ATotalsBandRowIsInvented(t *testing.T) {
	answer, key := aliFixtureTotalsBandTiny()

	got := aliAlign(answer, key)
	aliCheckPartition(t, got, len(answer), len(key))
	if len(got.Pairs) != 1 {
		t.Fatalf("len(Pairs) = %d, want 1", len(got.Pairs))
	}
	if p := got.Pairs[0]; p.Answer != 1 || p.Key != 1 {
		t.Errorf("pair = %+v, want (answer 1, key 1) -- the answer-index tie-break", p)
	}
	if want := []int{2}; !intSliceEqual(got.Invented, want) {
		t.Errorf("Invented = %v, want %v", got.Invented, want)
	}
	if len(got.Dropped) != 0 {
		t.Errorf("Dropped = %v, want none", got.Dropped)
	}
}

func TestAliAlign_ATitleOnlyDescriptionStillPairsOnItsAmount(t *testing.T) {
	answer, key := aliFixtureTitleOnly()

	got := aliAlign(answer, key)
	aliCheckPartition(t, got, len(answer), len(key))
	if len(got.Pairs) != 3 {
		t.Fatalf("len(Pairs) = %d, want 3", len(got.Pairs))
	}
	for i, p := range got.Pairs {
		want := i + 1
		if p.Answer != want || p.Key != want || p.Pass != aliPassAmount || p.Outcome != aliFound {
			t.Errorf("pair %d = %+v, want (answer %d, key %d, amount anchor, found)", i, p, want, want)
		}
	}
	if len(got.Invented) != 0 {
		t.Errorf("Invented = %v, want none", got.Invented)
	}
	if len(got.Dropped) != 0 {
		t.Errorf("Dropped = %v, want none", got.Dropped)
	}
	if got.Misaligned() != 0 {
		t.Errorf("Misaligned() = %d, want 0", got.Misaligned())
	}
	for i, p := range got.Pairs {
		a, k := answer[p.Answer-1], key[p.Key-1]
		if v := aliClassifyCell(LineRoleDescription, a.Cell(LineRoleDescription), k.Cell(LineRoleDescription)); v != aliWrong {
			t.Errorf("pair %d description verdict = %q, want %q", i, v, aliWrong)
		}
	}
}

func TestAliAlign_ADroppedRowDoesNotShiftTheRowsAfterIt(t *testing.T) {
	answer, key := aliFixtureDroppedMiddle()

	got := aliAlign(answer, key)
	aliCheckPartition(t, got, len(answer), len(key))
	if len(got.Pairs) != 3 {
		t.Fatalf("len(Pairs) = %d, want 3", len(got.Pairs))
	}
	wantKey := []int{1, 3, 4}
	for i, p := range got.Pairs {
		if p.Answer != i+1 || p.Key != wantKey[i] || p.Pass != aliPassStrong {
			t.Errorf("pair %d = %+v, want (answer %d, key %d, strong anchor)", i, p, i+1, wantKey[i])
		}
	}
	if len(got.Invented) != 0 {
		t.Errorf("Invented = %v, want none", got.Invented)
	}
	if want := []int{2}; !intSliceEqual(got.Dropped, want) {
		t.Errorf("Dropped = %v, want %v", got.Dropped, want)
	}
}

func TestAliAlign_AZeroRowControlCountsEveryAnswerRowAsInvented(t *testing.T) {
	answer, key := aliFixtureZeroKey()
	if len(answer) != 2 {
		t.Fatalf("len(answer) = %d, want 2", len(answer))
	}

	got := aliAlign(answer, key)
	aliCheckPartition(t, got, len(answer), len(key))
	if len(got.Pairs) != 0 {
		t.Fatalf("len(Pairs) = %d, want 0", len(got.Pairs))
	}
	if want := []int{1, 2}; !intSliceEqual(got.Invented, want) {
		t.Errorf("Invented = %v, want %v", got.Invented, want)
	}
	if len(got.Dropped) != 0 {
		t.Errorf("Dropped = %v, want none", got.Dropped)
	}
}

// --- misaligned and the partition invariant -------------------------------------------------

func TestAliAlign_AMisreadRowInAGapStillPairsPositionally(t *testing.T) {
	answer, key := aliFixtureMisreadGap()

	got := aliAlign(answer, key)
	aliCheckPartition(t, got, len(answer), len(key))
	if len(got.Pairs) != 3 {
		t.Fatalf("len(Pairs) = %d, want 3", len(got.Pairs))
	}
	if p := got.Pairs[0]; p.Pass != aliPassStrong || p.Outcome != aliFound {
		t.Errorf("pair 0 = %+v, want strong anchor / found", p)
	}
	if p := got.Pairs[2]; p.Pass != aliPassStrong || p.Outcome != aliFound {
		t.Errorf("pair 2 = %+v, want strong anchor / found", p)
	}
	mid := got.Pairs[1]
	if mid.Answer != 2 || mid.Key != 2 || mid.Pass != aliPassPositional || mid.Outcome != aliMisaligned {
		t.Errorf("pair 1 = %+v, want (answer 2, key 2, positional, misaligned)", mid)
	}
	if got.Found() != 2 {
		t.Errorf("Found() = %d, want 2", got.Found())
	}
	if got.Misaligned() != 1 {
		t.Errorf("Misaligned() = %d, want 1", got.Misaligned())
	}
}

func TestAliAlign_TheOutcomesPartitionBothSides(t *testing.T) {
	fixtures := []struct {
		name string
		fn   func() (answer, key []DocLine)
	}{
		{"clean", aliFixtureClean},
		{"menu1_only", aliFixtureMenu1Only},
		{"totals_band_tiny", aliFixtureTotalsBandTiny},
		{"title_only", aliFixtureTitleOnly},
		{"dropped_middle", aliFixtureDroppedMiddle},
		{"zero_key", aliFixtureZeroKey},
		{"misread_gap", aliFixtureMisreadGap},
		{"shift_by_one", aliFixtureShiftByOne},
		{"swap_1_12", func() (answer, key []DocLine) { return aliFixtureSwap(0, 11) }},
		{"swap_salad_3_13", func() (answer, key []DocLine) { return aliFixtureSwap(2, 12) }},
		{"menus_out_of_order", aliFixtureMenusOutOfOrder},
		{"totals_band_full_key", aliFixtureTotalsBandFullKey},
	}
	if len(fixtures) == 0 {
		t.Fatal("no fixtures")
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			answer, key := f.fn()
			got := aliAlign(answer, key)
			aliCheckPartition(t, got, len(answer), len(key))
		})
	}
}

// --- the off-by-one: load-bearing, numbers measured -----------------------------------------

func TestAliAlign_AShiftByOneReportsOneDroppedRowNotFifteenWrongCells(t *testing.T) {
	answer, key := aliFixtureShiftByOne()
	if len(key) != 15 {
		t.Fatalf("len(key) = %d, want 15", len(key))
	}
	if len(answer) != 14 {
		t.Fatalf("len(answer) = %d, want 14", len(answer))
	}

	got := aliAlign(answer, key)
	aliCheckPartition(t, got, len(answer), len(key))
	if len(got.Pairs) != 14 {
		t.Fatalf("len(Pairs) = %d, want 14", len(got.Pairs))
	}
	for i, p := range got.Pairs {
		if p.Answer != i+1 || p.Key != i+2 {
			t.Errorf("pair %d = %+v, want (answer %d, key %d)", i, p, i+1, i+2)
		}
	}
	if got.Found() != 14 {
		t.Errorf("Found() = %d, want 14", got.Found())
	}
	if got.Misaligned() != 0 {
		t.Errorf("Misaligned() = %d, want 0", got.Misaligned())
	}
	if len(got.Invented) != 0 {
		t.Errorf("Invented = %v, want none", got.Invented)
	}
	if want := []int{1}; !intSliceEqual(got.Dropped, want) {
		t.Errorf("Dropped = %v, want %v", got.Dropped, want)
	}

	right, wrong, missing := aliSweepCells(t, got.Pairs, answer, key)
	if total := right + wrong + missing; total != 70 {
		t.Fatalf("scored %d cells, want 70", total)
	}
	if right != 70 || wrong != 0 || missing != 0 {
		t.Errorf("right/wrong/missing = %d/%d/%d, want 70/0/0", right, wrong, missing)
	}

	valueCells, nullCells := aliCountValueAndNullCells(got.Pairs, key)
	if total := valueCells + nullCells; total != 70 {
		t.Fatalf("value+null cells = %d, want 70", total)
	}
	if valueCells != 56 || nullCells != 14 {
		t.Errorf("value/null cells = %d/%d, want 56/14", valueCells, nullCells)
	}
}

// --- the equal-amount swap: load-bearing -----------------------------------------------------

func TestAliAlign_TwoEqualAmountRowsSwappedAcrossMenusBreakTheCleanPass(t *testing.T) {
	answer, key := aliFixtureSwap(0, 11) // positions 1 and 12: SMOKEY JOLLOF RICE <-> FRIED RICE

	got := aliAlign(answer, key)
	aliCheckPartition(t, got, len(answer), len(key))
	if len(got.Pairs) != 15 {
		t.Fatalf("len(Pairs) = %d, want 15", len(got.Pairs))
	}
	if got.Found() != 15 {
		t.Errorf("Found() = %d, want 15", got.Found())
	}
	if got.Misaligned() != 0 {
		t.Errorf("Misaligned() = %d, want 0 -- pass 2 pairs the swap by amount before pass 3 sees it", got.Misaligned())
	}
	if len(got.Invented) != 0 {
		t.Errorf("Invented = %v, want none", got.Invented)
	}
	if len(got.Dropped) != 0 {
		t.Errorf("Dropped = %v, want none", got.Dropped)
	}

	right, wrong, missing := aliSweepCells(t, got.Pairs, answer, key)
	if total := right + wrong + missing; total != 75 {
		t.Fatalf("scored %d cells, want 75", total)
	}
	if right != 73 || wrong != 2 {
		t.Errorf("right/wrong = %d/%d, want 73/2 -- the two swapped description cells", right, wrong)
	}
	if missing != 0 {
		t.Errorf("missing = %d, want 0", missing)
	}

	// Vacuity control in the same test: the byte-identical SALAD swap must still report a clean
	// pass, proving the 73/2 split above is caused by the swap and not by scoring 15 rows at all.
	saladAnswer, saladKey := aliFixtureSwap(2, 12)
	saladGot := aliAlign(saladAnswer, saladKey)
	aliCheckPartition(t, saladGot, len(saladAnswer), len(saladKey))
	if len(saladGot.Pairs) != 15 {
		t.Fatalf("SALAD control: len(Pairs) = %d, want 15", len(saladGot.Pairs))
	}
	if saladGot.Found() != 15 || saladGot.Misaligned() != 0 {
		t.Errorf("SALAD control: Found/Misaligned = %d/%d, want 15/0", saladGot.Found(), saladGot.Misaligned())
	}
	if len(saladGot.Invented) != 0 || len(saladGot.Dropped) != 0 {
		t.Errorf("SALAD control: Invented/Dropped = %v/%v, want none/none", saladGot.Invented, saladGot.Dropped)
	}
	sRight, sWrong, sMissing := aliSweepCells(t, saladGot.Pairs, saladAnswer, saladKey)
	if total := sRight + sWrong + sMissing; total != 75 {
		t.Fatalf("SALAD control: scored %d cells, want 75", total)
	}
	if sRight != 75 || sWrong != 0 {
		t.Errorf("SALAD control: right/wrong = %d/%d, want 75/0 -- a SALAD-only swap would be a no-op on any implementation", sRight, sWrong)
	}
	if sMissing != 0 {
		t.Errorf("SALAD control: missing = %d, want 0", sMissing)
	}
}

// --- menus out of order: the design's own rationale, and the only fixture that misaligns ----

func TestAliAlign_MenusReturnedOutOfOrderMisalignsThePositionalPairs(t *testing.T) {
	answer, key := aliFixtureMenusOutOfOrder()
	if len(answer) != 15 {
		t.Fatalf("len(answer) = %d, want 15", len(answer))
	}

	got := aliAlign(answer, key)
	aliCheckPartition(t, got, len(answer), len(key))
	if len(got.Pairs) == 0 {
		t.Fatal("no pairs")
	}
	if got.Found() != 13 {
		t.Errorf("Found() = %d, want 13", got.Found())
	}
	if got.Misaligned() != 2 {
		t.Errorf("Misaligned() = %d, want 2 -- the only fixture where misaligned fires", got.Misaligned())
	}
	if len(got.Invented) != 0 {
		t.Errorf("Invented = %v, want none", got.Invented)
	}
	if len(got.Dropped) != 0 {
		t.Errorf("Dropped = %v, want none", got.Dropped)
	}

	right, wrong, missing := aliSweepCells(t, got.Pairs, answer, key)
	if total := right + wrong + missing; total != 75 {
		t.Fatalf("scored %d cells, want 75", total)
	}
	if right != 69 || wrong != 6 {
		t.Errorf("right/wrong = %d/%d, want 69/6", right, wrong)
	}
	if missing != 0 {
		t.Errorf("missing = %d, want 0", missing)
	}
}

// --- a partial reader and an invented totals band, on the full menu key ---------------------

func TestAliAlign_AnInventedTotalsRowOnTheFullMenuKeyStaysInvented(t *testing.T) {
	answer, key := aliFixtureTotalsBandFullKey()
	if len(answer) != 16 {
		t.Fatalf("len(answer) = %d, want 16", len(answer))
	}

	got := aliAlign(answer, key)
	aliCheckPartition(t, got, len(answer), len(key))
	if len(got.Pairs) != 15 {
		t.Fatalf("len(Pairs) = %d, want 15", len(got.Pairs))
	}
	if got.Found() != 15 {
		t.Errorf("Found() = %d, want 15", got.Found())
	}
	if got.Misaligned() != 0 {
		t.Errorf("Misaligned() = %d, want 0", got.Misaligned())
	}
	if want := []int{16}; !intSliceEqual(got.Invented, want) {
		t.Errorf("Invented = %v, want %v", got.Invented, want)
	}
	if len(got.Dropped) != 0 {
		t.Errorf("Dropped = %v, want none", got.Dropped)
	}

	right, wrong, missing := aliSweepCells(t, got.Pairs, answer, key)
	if total := right + wrong + missing; total != 75 {
		t.Fatalf("scored %d cells, want 75", total)
	}
	if right != 75 || wrong != 0 || missing != 0 {
		t.Errorf("right/wrong/missing = %d/%d/%d, want 75/0/0", right, wrong, missing)
	}
}
