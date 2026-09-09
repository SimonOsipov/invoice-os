// lineitems_internal_test.go: liNormalizeHeaderForRole's decoration strip and the shared-fold
// regression guard on reconcile.go's supplier-name path. Package extraction: both are unexported.
package extraction

import "testing"

// liHeaderRoleCell builds a single-column, single-row header table so liClassifyHeader has
// nothing to disambiguate but the one header cell under test.
func liHeaderRoleCell(text string) Table {
	return Table{Rows: 1, Cols: 1, Cells: []TableCell{{Row: 0, Col: 0, RowSpan: 1, ColSpan: 1, Text: text}}}
}

func TestLiNormalizeHeaderForRole_StripsTrailingNairaDecoration(t *testing.T) {
	got := liNormalizeHeaderForRole("Amount ₦")
	if got != "amount" {
		t.Fatalf("liNormalizeHeaderForRole(%q) = %q, want %q", "Amount ₦", got, "amount")
	}
	if role := liLexicon[got]; role != liRoleLineTotal {
		t.Errorf("liLexicon[%q] = %v, want liRoleLineTotal", got, role)
	}
}

func TestLiNormalizeHeaderForRole_StripsAStandaloneNGNToken(t *testing.T) {
	got := liNormalizeHeaderForRole("Amount NGN")
	if got != "amount" {
		t.Fatalf("liNormalizeHeaderForRole(%q) = %q, want %q", "Amount NGN", got, "amount")
	}
	if role := liLexicon[got]; role != liRoleLineTotal {
		t.Errorf("liLexicon[%q] = %v, want liRoleLineTotal", got, role)
	}
}

func TestLiNormalizeHeaderForRole_StripsParenthesisedQualifier(t *testing.T) {
	got := liNormalizeHeaderForRole("RATE (N)")
	if got != "rate" {
		t.Fatalf("liNormalizeHeaderForRole(%q) = %q, want %q", "RATE (N)", got, "rate")
	}
	if role := liLexicon[got]; role != liRoleUnitPrice {
		t.Errorf("liLexicon[%q] = %v, want liRoleUnitPrice", got, role)
	}
}

func TestLiNormalizeHeaderForRole_StripsALeadingParenthesisedQualifier(t *testing.T) {
	got := liNormalizeHeaderForRole("(NGN) Amount")
	if got != "amount" {
		t.Fatalf("liNormalizeHeaderForRole(%q) = %q, want %q", "(NGN) Amount", got, "amount")
	}
	if role := liLexicon[got]; role != liRoleLineTotal {
		t.Errorf("liLexicon[%q] = %v, want liRoleLineTotal", got, role)
	}
}

// The strip drops any parenthesised group, not only a currency one -- "(nos)" carries no
// currency token at all.
func TestLiNormalizeHeaderForRole_TheParenStripIsNotGatedOnCurrency(t *testing.T) {
	got := liNormalizeHeaderForRole("Qty (nos)")
	if got != "qty" {
		t.Fatalf("liNormalizeHeaderForRole(%q) = %q, want %q", "Qty (nos)", got, "qty")
	}
	if role := liLexicon[got]; role != liRoleQuantity {
		t.Errorf("liLexicon[%q] = %v, want liRoleQuantity", got, role)
	}
}

// A header whose entire text IS a currency token must normalise to that token, never to "" --
// an empty header could then match something unintended (AC-3, header-strip-is-not-currency-detection).
func TestLiNormalizeHeaderForRole_ACurrencyOnlyHeaderIsNotEmptied(t *testing.T) {
	cases := []struct{ header, want string }{
		{"₦", "₦"},
		{"N", "n"},
		{"NGN", "ngn"},
		{"(N)", "(n)"},
	}
	for _, tc := range cases {
		got := liNormalizeHeaderForRole(tc.header)
		if got != tc.want {
			t.Errorf("liNormalizeHeaderForRole(%q) = %q, want %q -- must fall back to the plain fold, never empty", tc.header, got, tc.want)
			continue
		}
		if role := liLexicon[got]; role != liRoleNone {
			t.Errorf("liLexicon[%q] = %v, want liRoleNone", got, role)
		}
	}
}

// The "N" strip is a whole-token match: strings.Fields makes "s/n" one token, so it must not eat
// the letter inside it.
func TestLiNormalizeHeaderForRole_LeavesSlashNAlone(t *testing.T) {
	got := liNormalizeHeaderForRole("S/N")
	if got != "s/n" {
		t.Fatalf("liNormalizeHeaderForRole(%q) = %q, want %q", "S/N", got, "s/n")
	}
	if role := liLexicon[got]; role != liRoleNone {
		t.Errorf("liLexicon[%q] = %v, want liRoleNone", got, role)
	}
}

// Tripwire for a future author who re-merges liNormalizeHeaderText and liNormalizeHeaderForRole:
// the plain fold that reconcile.go:224-225 uses for supplier-name equality must strip nothing.
func TestLiNormalizeHeaderText_TheSharedFoldIsUnchangedForTheSupplierNamePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"XYZ (Nigeria) Ltd", "xyz (nigeria) ltd"},
		{"Acme N Ltd", "acme n ltd"},
		{"NGN", "ngn"},
		{"Amount ₦", "amount ₦"},
	}
	for _, tc := range cases {
		if got := liNormalizeHeaderText(tc.in); got != tc.want {
			t.Errorf("liNormalizeHeaderText(%q) = %q, want %q -- the shared fold must not gain a strip", tc.in, got, tc.want)
		}
	}
}

// liRoleReturnedFor maps liClassifyHeader's five named return values back onto the role that
// owns each, so the sweep below can check "my role's slot is 0, the other four are -1" for an
// arbitrary lexicon key without hand-listing every case.
func liRoleReturnedFor(role liRole, descCol, qtyCol, priceCol, totalCol, taxCol int) (mine int, others []int) {
	switch role {
	case liRoleDescription:
		return descCol, []int{qtyCol, priceCol, totalCol, taxCol}
	case liRoleQuantity:
		return qtyCol, []int{descCol, priceCol, totalCol, taxCol}
	case liRoleUnitPrice:
		return priceCol, []int{descCol, qtyCol, totalCol, taxCol}
	case liRoleLineTotal:
		return totalCol, []int{descCol, qtyCol, priceCol, taxCol}
	case liRoleLineTax:
		return taxCol, []int{descCol, qtyCol, priceCol, totalCol}
	}
	return -1, nil
}

// Every key liLexicon already carries today must keep classifying after this subtask -- the
// header-strip must widen what reaches the lexicon, not narrow what the lexicon itself accepts.
// Ranges over liLexicon directly, so the 15 -> 17 key widening this subtask makes is covered
// without a hand-listed count here.
func TestLiClassifyHeader_EveryPreExistingLexiconKeyStillClassifies(t *testing.T) {
	if len(liLexicon) == 0 {
		t.Fatal("liLexicon is empty; the sweep below would hold vacuously")
	}
	for key, role := range liLexicon {
		descCol, qtyCol, priceCol, totalCol, taxCol := liClassifyHeader(liHeaderRoleCell(key))
		mine, others := liRoleReturnedFor(role, descCol, qtyCol, priceCol, totalCol, taxCol)
		if mine != 0 {
			t.Errorf("liClassifyHeader(%q) role column = %d, want 0", key, mine)
		}
		for _, o := range others {
			if o != -1 {
				t.Errorf("liClassifyHeader(%q) an unrelated role column = %d, want -1", key, o)
			}
		}
	}
}

// AC-1: a VAT header claims the line-tax role, and the other four columns are unaffected.
func TestLiClassifyHeader_VatColumnClaimsTheLineTaxRole(t *testing.T) {
	descCol, qtyCol, priceCol, totalCol, taxCol := liClassifyHeader(
		liHeaderRoleRow("Description", "Qty", "Unit price", "Amount", "VAT ₦"))
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"descCol", descCol, 0},
		{"qtyCol", qtyCol, 1},
		{"priceCol", priceCol, 2},
		{"totalCol", totalCol, 3},
		{"taxCol", taxCol, 4},
	} {
		if tc.got != tc.want {
			t.Errorf("liClassifyHeader %s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// liHeaderRoleRow builds a one-row header table with each text in its own column, so a whole
// decorated header row can be classified at once.
func liHeaderRoleRow(texts ...string) Table {
	cells := make([]TableCell, 0, len(texts))
	for i, text := range texts {
		cells = append(cells, TableCell{Row: 0, Col: i, RowSpan: 1, ColSpan: 1, Text: text})
	}
	return Table{Rows: 1, Cols: len(texts), Cells: cells}
}

// liWantHeaderRole asserts one header's normalised form and the role it then looks up.
func liWantHeaderRole(t *testing.T, header, want string, wantRole liRole) {
	t.Helper()
	got := liNormalizeHeaderForRole(header)
	if got != want {
		t.Errorf("liNormalizeHeaderForRole(%q) = %q, want %q", header, got, want)
		return
	}
	if role := liLexicon[got]; role != wantRole {
		t.Errorf("liLexicon[%q] = %v, want %v", got, role, wantRole)
	}
}

// The strip removes decoration, it does not sanitise: a header carrying no word survives whole
// and still names no role. Paired with a header that does strip, so neither half passes alone.
func TestLiNormalizeHeaderForRole_PunctuationOnlyHeadersSurviveWhole(t *testing.T) {
	cases := []struct{ header, want string }{
		{"---", "---"},
		{"()", "()"},
		{"( )", "( )"},
		{"//", "//"},
		{"n n n", "n n n"},
	}
	if len(cases) == 0 {
		t.Fatal("no cases; the sweep below would hold vacuously")
	}
	for _, tc := range cases {
		liWantHeaderRole(t, tc.header, tc.want, liRoleNone)
	}
	liWantHeaderRole(t, "Amount (N)", "amount", liRoleLineTotal)
}

// The empty-result fallback covers the paren strip too, not only the currency strip: a wholly
// parenthesised header keeps its parens rather than becoming the bare word inside.
func TestLiNormalizeHeaderForRole_AWhollyParenthesisedHeaderKeepsItsParens(t *testing.T) {
	liWantHeaderRole(t, "(Amount)", "(amount)", liRoleNone)
	liWantHeaderRole(t, "Amount (NGN)", "amount", liRoleLineTotal)
}

// The fallback returns the base fold, so it cannot invent content a blank header never had.
func TestLiNormalizeHeaderForRole_AWhitespaceOnlyHeaderIsEmptyBothWays(t *testing.T) {
	if got := liNormalizeHeaderText("   \t\n "); got != "" {
		t.Fatalf("liNormalizeHeaderText of a blank header = %q, want %q", got, "")
	}
	liWantHeaderRole(t, "   \t\n ", "", liRoleNone)
	liWantHeaderRole(t, "  Amount  ", "amount", liRoleLineTotal)
}

// An unbalanced paren fails closed: the header is left as folded and names no role, rather than
// being guessed at.
func TestLiNormalizeHeaderForRole_UnbalancedParensAreLeftAlone(t *testing.T) {
	cases := []struct{ header, want string }{
		{"Amount (N", "amount (n"},
		{"(N Amount", "(n amount"},
		{"Amount N)", "amount n)"},
	}
	if len(cases) == 0 {
		t.Fatal("no cases; the sweep below would hold vacuously")
	}
	for _, tc := range cases {
		liWantHeaderRole(t, tc.header, tc.want, liRoleNone)
	}
	liWantHeaderRole(t, "Amount (N)", "amount", liRoleLineTotal)
}

// ceiling: only a leading or trailing group is stripped -- widen when a corpus header carries a
// mid-string qualifier.
func TestLiNormalizeHeaderForRole_AParenGroupInTheMiddleIsNotStripped(t *testing.T) {
	liWantHeaderRole(t, "Unit (N) price", "unit (n) price", liRoleNone)
	liWantHeaderRole(t, "Unit price (N)", "unit price", liRoleUnitPrice)
}

// ceiling: one group per header -- widen when a corpus header carries two.
func TestLiNormalizeHeaderForRole_OnlyOneParenGroupIsStripped(t *testing.T) {
	cases := []struct{ header, want string }{
		{"(a) Rate (N)", "rate (n)"},
		{"Rate (N) (each)", "rate (n)"},
	}
	if len(cases) == 0 {
		t.Fatal("no cases; the sweep below would hold vacuously")
	}
	for _, tc := range cases {
		liWantHeaderRole(t, tc.header, tc.want, liRoleNone)
	}
	liWantHeaderRole(t, "Rate (N)", "rate", liRoleUnitPrice)
}

// "ngn" is dropped as a whole token only, so a word merely containing those letters is intact.
func TestLiNormalizeHeaderForRole_NGNInsideAWordIsNotAToken(t *testing.T) {
	cases := []struct{ header, want string }{
		{"Ngntech Ltd", "ngntech ltd"},
		{"Amount in NGNs", "amount in ngns"},
		{"NGNAmount", "ngnamount"},
	}
	if len(cases) == 0 {
		t.Fatal("no cases; the sweep below would hold vacuously")
	}
	for _, tc := range cases {
		liWantHeaderRole(t, tc.header, tc.want, liRoleNone)
	}
	liWantHeaderRole(t, "Amount NGN", "amount", liRoleLineTotal)
}

// ceiling: the token match is ASCII, so a full-width naira letter survives and its header names
// no role -- widen when a corpus header carries one.
func TestLiNormalizeHeaderForRole_FullWidthCurrencyLettersAreNotStripped(t *testing.T) {
	cases := []struct{ header, want string }{
		{"Ｎ", "ｎ"},
		{"ＮＧＮ", "ｎｇｎ"},
		{"Amount Ｎ", "amount ｎ"},
	}
	if len(cases) == 0 {
		t.Fatal("no cases; the sweep below would hold vacuously")
	}
	for _, tc := range cases {
		liWantHeaderRole(t, tc.header, tc.want, liRoleNone)
	}
	liWantHeaderRole(t, "Amount N", "amount", liRoleLineTotal)
}

// The only unit-level pin on the role lookup's call site: a decorated header row must reach
// liClassifyHeader stripped, and the index column must claim nothing.
func TestLiClassifyHeader_ADecoratedHeaderRowMapsEveryColumn(t *testing.T) {
	descCol, qtyCol, priceCol, totalCol, taxCol := liClassifyHeader(
		liHeaderRoleRow("S/N", "Description", "Qty", "RATE (N)", "Amount ₦"))
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"descCol", descCol, 1},
		{"qtyCol", qtyCol, 2},
		{"priceCol", priceCol, 3},
		{"totalCol", totalCol, 4},
		{"taxCol", taxCol, -1},
	} {
		if tc.got != tc.want {
			t.Errorf("liClassifyHeader %s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// AC-1: the measured production header -- a weak "Item" at column 0 must not beat a strong
// "Service description" arriving later in reading order. Also the measured header's own VAT
// column, which now claims the line-tax role instead of falling through unclassified.
func TestLiClassifyHeader_TheMeasuredDenseHeaderPutsDescriptionOnTheNamedColumn(t *testing.T) {
	descCol, qtyCol, priceCol, totalCol, taxCol := liClassifyHeader(liHeaderRoleRow(
		"Item", "Service description", "Reference", "Qty", "Unit rate ₦", "Amount ₦", "VAT ₦"))
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"descCol", descCol, 1},
		{"qtyCol", qtyCol, 3},
		{"priceCol", priceCol, 4},
		{"totalCol", totalCol, 5},
		{"taxCol", taxCol, 6},
	} {
		if tc.got != tc.want {
			t.Errorf("liClassifyHeader %s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// AC-2: tier beats position from either side. "item first" is the shape the pre-tier classifier
// got wrong; "description first" is the control that catches a fix inverting the rule to
// "last wins".
func TestLiClassifyHeader_StrongBeatsWeakFromEitherSide(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  Table
		want int
	}{
		{"description first", liHeaderRoleRow("Description", "Item", "Qty", "Amount"), 0},
		{"item first", liHeaderRoleRow("Item", "Description", "Qty", "Amount"), 1},
	} {
		descCol, _, _, _, _ := liClassifyHeader(tc.row)
		if descCol != tc.want {
			t.Errorf("%s: descCol = %d, want %d", tc.name, descCol, tc.want)
		}
	}
}

// AC-3: item is demoted, not removed -- alone in its tier, the weak fallback still fires. The
// only coverage anywhere of the ordinary Item|Qty|Price invoice shape.
func TestLiClassifyHeader_ItemStillClaimsDescriptionWhenAloneInItsTier(t *testing.T) {
	descCol, _, _, _, _ := liClassifyHeader(liHeaderRoleRow("Item", "Qty", "Unit price", "Amount"))
	if descCol != 0 {
		t.Errorf("descCol = %d, want 0", descCol)
	}
}

// AC-4: both new strong keys, both decorated -- DESCRIPTION OF GOODS case-folds, Unit rate ₦
// strips its currency decoration.
func TestLiClassifyHeader_TheDecoratedGoodsHeaderMapsAllFourRoles(t *testing.T) {
	descCol, qtyCol, priceCol, totalCol, taxCol := liClassifyHeader(liHeaderRoleRow(
		"S/N", "DESCRIPTION OF GOODS", "Qty", "Unit rate ₦", "Amount ₦"))
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"descCol", descCol, 1},
		{"qtyCol", qtyCol, 2},
		{"priceCol", priceCol, 3},
		{"totalCol", totalCol, 4},
		{"taxCol", taxCol, -1},
	} {
		if tc.got != tc.want {
			t.Errorf("liClassifyHeader %s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// AC-5: paired with the row-level positive columns (qtyCol, totalCol) so an all -1 classifier
// cannot pass by only checking absence.
func TestLiClassifyHeader_IndexAndReferenceColumnsClaimNoRole(t *testing.T) {
	liWantHeaderRole(t, "S/N", "s/n", liRoleNone)
	liWantHeaderRole(t, "Reference", "reference", liRoleNone)

	descCol, qtyCol, priceCol, totalCol, taxCol := liClassifyHeader(liHeaderRoleRow("S/N", "Reference", "Qty", "Amount"))
	if qtyCol != 2 {
		t.Errorf("qtyCol = %d, want 2 -- positive companion, else an all -1 classifier would pass silently", qtyCol)
	}
	if totalCol != 3 {
		t.Errorf("totalCol = %d, want 3", totalCol)
	}
	if priceCol != -1 {
		t.Errorf("priceCol = %d, want -1 (no unit-price column present)", priceCol)
	}
	if taxCol != -1 {
		t.Errorf("taxCol = %d, want -1 (no VAT column present)", taxCol)
	}
	for _, got := range []int{descCol, qtyCol, priceCol, totalCol, taxCol} {
		if got == 0 || got == 1 {
			t.Errorf("a role resolved to column %d, want no role claiming S/N or Reference", got)
		}
	}
}

// AC-6: two columns in the same strong tier -- position remains the tiebreak WITHIN a tier.
func TestLiClassifyHeader_ASecondStrongColumnDoesNotDisplaceTheFirst(t *testing.T) {
	descCol, qtyCol, _, totalCol, _ := liClassifyHeader(liHeaderRoleRow("Amount", "Total", "Qty", "Description"))
	if totalCol != 0 {
		t.Errorf("totalCol = %d, want 0", totalCol)
	}
	if descCol != 3 {
		t.Errorf("descCol = %d, want 3 -- positive companion", descCol)
	}
	if qtyCol != 2 {
		t.Errorf("qtyCol = %d, want 2", qtyCol)
	}
}

// AC-6: a weak key that names no liLexicon role would be a fallback that can never fire -- the
// goldens source scan bounds only "var liLexicon = ", not this set.
func TestLiWeakHeaders_EveryWeakKeyIsALexiconKey(t *testing.T) {
	if len(liWeakHeaders) == 0 {
		t.Fatal("liWeakHeaders is empty; the sweep below would hold vacuously")
	}
	for key := range liWeakHeaders {
		if role := liLexicon[key]; role == liRoleNone {
			t.Errorf("liWeakHeaders has key %q, which liLexicon does not classify", key)
		}
	}
}

// A reader may emit header cells in any column order, so leftmost-wins rests on liSortInts, not on
// the input. Only an out-of-order cell slice can tell that the sort still runs.
func TestLiClassifyHeader_LeftmostWinsWhenCellsArriveOutOfColumnOrder(t *testing.T) {
	byCol := map[int]string{0: "Description", 1: "Particulars", 2: "Qty", 3: "Amount"}
	cells := make([]TableCell, 0, len(byCol))
	for _, col := range []int{1, 3, 0, 2} {
		cells = append(cells, TableCell{Row: 0, Col: col, RowSpan: 1, ColSpan: 1, Text: byCol[col]})
	}
	descCol, qtyCol, priceCol, totalCol, taxCol := liClassifyHeader(Table{Rows: 1, Cols: len(byCol), Cells: cells})
	if descCol != 0 {
		t.Errorf("descCol = %d, want 0 -- two strong description columns, the leftmost wins whatever order the cells arrive in", descCol)
	}
	if qtyCol != 2 {
		t.Errorf("qtyCol = %d, want 2", qtyCol)
	}
	if priceCol != -1 {
		t.Errorf("priceCol = %d, want -1", priceCol)
	}
	if totalCol != 3 {
		t.Errorf("totalCol = %d, want 3", totalCol)
	}
	if taxCol != -1 {
		t.Errorf("taxCol = %d, want -1 (no VAT column present)", taxCol)
	}
}

// The tier rule must not rest on Go's randomised map iteration order. Every role here has two
// rival strong columns, so a ranged map would return a distribution rather than one tuple.
func TestLiClassifyHeader_RepeatedCallsReturnOneResult(t *testing.T) {
	row := liHeaderRoleRow("Item", "Service description", "Particulars", "Reference",
		"Qty", "Quantity", "Unit rate ₦", "Rate", "Amount ₦", "Total")
	type result struct{ desc, qty, price, total int }
	const runs = 500
	seen := make(map[result]int)
	for i := 0; i < runs; i++ {
		descCol, qtyCol, priceCol, totalCol, _ := liClassifyHeader(row)
		seen[result{descCol, qtyCol, priceCol, totalCol}]++
	}
	want := result{desc: 1, qty: 4, price: 6, total: 8}
	if len(seen) != 1 {
		t.Fatalf("liClassifyHeader returned %d distinct results over %d runs (%v), want 1", len(seen), runs, seen)
	}
	if seen[want] != runs {
		t.Errorf("liClassifyHeader = %v, want %v on all %d runs", seen, want, runs)
	}
}

// Header shapes the worked traces never covered. Every case names all four roles, so an absence is
// always read beside a column that did resolve. "Two rival strong description columns" has no
// semantically right answer -- the rule is leftmost, and this pins that it stays leftmost.
func TestLiClassifyHeader_EdgeHeaderShapes(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		texts                   []string
		desc, qty, price, total int
	}{
		{"identical duplicate headers", []string{"Amount", "Amount", "Qty"}, -1, 2, -1, 0},
		{"two rival strong description columns", []string{"Description", "Service description", "Qty"}, 0, 2, -1, -1},
		{"every column weak", []string{"Item", "Item"}, 0, -1, -1, -1},
		{"a weak column repeated beside a strong one", []string{"Item", "Item", "Description", "Qty"}, 2, 3, -1, -1},
		{"no column names a role", []string{"S/N", "Reference", "Notes", "Sr"}, -1, -1, -1, -1},
		{"a single strong column", []string{"Description"}, 0, -1, -1, -1},
		{"a single weak column", []string{"Item"}, 0, -1, -1, -1},
	} {
		// None of the cases above names a VAT column, so taxCol is discarded here rather than
		// added as a fifth always-(-1) field to every case.
		descCol, qtyCol, priceCol, totalCol, _ := liClassifyHeader(liHeaderRoleRow(tc.texts...))
		for _, role := range []struct {
			name string
			got  int
			want int
		}{
			{"descCol", descCol, tc.desc},
			{"qtyCol", qtyCol, tc.qty},
			{"priceCol", priceCol, tc.price},
			{"totalCol", totalCol, tc.total},
		} {
			if role.got != role.want {
				t.Errorf("%s: %v %s = %d, want %d", tc.name, tc.texts, role.name, role.got, role.want)
			}
		}
	}
}
