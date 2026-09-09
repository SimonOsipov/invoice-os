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

// liRoleReturnedFor maps liClassifyHeader's four named return values back onto the role that
// owns each, so the sweep below can check "my role's slot is 0, the other three are -1" for an
// arbitrary lexicon key without hand-listing every case.
func liRoleReturnedFor(role liRole, descCol, qtyCol, priceCol, totalCol int) (mine int, others []int) {
	switch role {
	case liRoleDescription:
		return descCol, []int{qtyCol, priceCol, totalCol}
	case liRoleQuantity:
		return qtyCol, []int{descCol, priceCol, totalCol}
	case liRoleUnitPrice:
		return priceCol, []int{descCol, qtyCol, totalCol}
	case liRoleLineTotal:
		return totalCol, []int{descCol, qtyCol, priceCol}
	}
	return -1, nil
}

// Every key liLexicon already carries today must keep classifying after this subtask -- the
// header-strip must widen what reaches the lexicon, not narrow what the lexicon itself accepts.
func TestLiClassifyHeader_EveryPreExistingLexiconKeyStillClassifies(t *testing.T) {
	if len(liLexicon) == 0 {
		t.Fatal("liLexicon is empty; the sweep below would hold vacuously")
	}
	for key, role := range liLexicon {
		descCol, qtyCol, priceCol, totalCol := liClassifyHeader(liHeaderRoleCell(key))
		mine, others := liRoleReturnedFor(role, descCol, qtyCol, priceCol, totalCol)
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
	descCol, qtyCol, priceCol, totalCol := liClassifyHeader(
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
	} {
		if tc.got != tc.want {
			t.Errorf("liClassifyHeader %s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}
