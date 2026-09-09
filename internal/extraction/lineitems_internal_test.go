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
