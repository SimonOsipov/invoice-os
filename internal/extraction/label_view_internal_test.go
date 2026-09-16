// label_view_internal_test.go: EXTR-35-01 specs for labelView and the lexicon it feeds.
package extraction

import (
	"slices"
	"strings"
	"testing"
	"unicode"
)

// lvIDs is every anchorLabelMatchers id that matches text, in lexicon order.
func lvIDs(t *testing.T, text string) []string {
	t.Helper()
	if len(anchorLabelMatchers) == 0 {
		t.Fatal("anchorLabelMatchers is empty")
	}
	out := []string{}
	for _, m := range anchorLabelMatchers {
		if m.RE.MatchString(text) {
			out = append(out, m.ID)
		}
	}
	return out
}

func TestLabelView_JoinsASpacedLabelWhateverTheWordGap(t *testing.T) {
	tests := []struct{ in, want string }{
		{"I N V O I C E N U M B E R", "INVOICENUMBER"},
		{"I N V O I C E   N U M B E R", "INVOICENUMBER"},
		{"I S S U E D", "ISSUED"},
		{"I S S U E D ", "ISSUED"},
		{"I\tS S U E D", "ISSUED"},
	}
	for _, tt := range tests {
		if got := labelView(tt.in); got != tt.want {
			t.Errorf("labelView(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLabelView_ReadsEveryOtherTokenAsPrinted(t *testing.T) {
	tests := []string{
		"INVOICE NUMBER", "Invoice No", "A", " A ", "", "   ",
		"1 2 3", "V A T 7.5%", "INVOICE N O", "I N V O I C E N O :", "₦ 1 5 0 0",
	}
	if len(tests) < 11 {
		t.Fatalf("have %d cases, want at least 11", len(tests))
	}
	for _, in := range tests {
		if got := labelView(in); got != in {
			t.Errorf("labelView(%q) = %q, want %q (byte-identical)", in, got, in)
		}
	}
}

func TestLabelView_ASpacedLabelMatchesTheEntryItsWordMatches(t *testing.T) {
	if len(anchorLabelMatchers) == 0 {
		t.Fatal("anchorLabelMatchers is empty")
	}
	tests := []struct {
		spaced, word string
		want         []string
	}{
		{"I N V O I C E N U M B E R", "INVOICE NUMBER", []string{"invoice_no"}},
		{"I N V O I C E   N U M B E R", "INVOICE NUMBER", []string{"invoice_no"}},
		{"I S S U E D", "ISSUED", []string{"issue_date"}},
	}
	for _, tt := range tests {
		wordIDs := lvIDs(t, tt.word)
		if !slices.Equal(wordIDs, tt.want) {
			t.Fatalf("lvIDs(%q) = %v, want %v", tt.word, wordIDs, tt.want)
		}
		if got := lvIDs(t, labelView(tt.spaced)); !slices.Equal(got, wordIDs) {
			t.Errorf("ids = %v, want %v", got, wordIDs)
		}
	}
}

func TestLabelView_SpacedTextThatIsNoLabelMatchesNothing(t *testing.T) {
	if len(anchorLabelMatchers) == 0 {
		t.Fatal("anchorLabelMatchers is empty")
	}
	if ids := lvIDs(t, labelView("I N V O I C E N O")); !slices.Contains(ids, "invoice_no") {
		t.Fatalf("the positive control matched nothing: ids = %v, want invoice_no", ids)
	}

	negatives := []string{
		"C O M M E R C I A L   I N V O I C E",
		"I N V O I C E   N O T E S",
		"U P D A T E",
		"U P   D A T E",
		"P A I D",
		"Q X Z W",
	}
	checked := 0
	for _, s := range negatives {
		checked++
		if ids := lvIDs(t, labelView(s)); len(ids) > 0 {
			t.Errorf("lvIDs(labelView(%q)) = %v, want none", s, ids)
		}
	}
	if checked != 6 {
		t.Fatalf("checked %d negatives, want 6", checked)
	}
	if ids := lvIDs(t, "COMMERCIAL INVOICE"); len(ids) > 0 {
		t.Errorf("lvIDs(%q) = %v, want none", "COMMERCIAL INVOICE", ids)
	}
}

// lvLettersOnly is every lexicon entry's label with its spaces removed, except signature
// (its pattern requires \s+, so it can never match a letters-only string).
var lvLettersOnly = []string{
	"INVOICENUMBER", "INVOICENO", "PROFORMAINVOICENO", "ISSUED", "DATE", "DUEDATE",
	"CURRENCY", "FROM", "BILLEDTO", "BILLTO", "INVOICETO", "TIN", "SUPPLIERTIN",
	"CUSTOMERNO", "ACCOUNTNUMBER", "BUYERSIGNATURE", "SUBTOTAL", "AMOUNTPAYABLE",
	"GRANDTOTAL", "TOTAL", "VAT", "VATNO", "VATREGNO", "TAXIDNO", "TAXINVOICE",
	"WITHHOLDINGTAX", "SUPPLIER", "CUSTOMER", "RCNO",
}

func TestAnchorLexicon_EveryMatchOnALettersOnlyViewSpansIt(t *testing.T) {
	if len(anchorLabelMatchers) == 0 {
		t.Fatal("anchorLabelMatchers is empty")
	}
	for _, s := range lvLettersOnly {
		for _, r := range s {
			if !unicode.IsLetter(r) {
				t.Fatalf("%q is not letters-only", s)
			}
		}
	}

	cannotMatchLettersOnly := []string{"signature"}
	for _, id := range cannotMatchLettersOnly {
		found := false
		for _, m := range anchorLabelMatchers {
			if m.ID == id {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("cannotMatchLettersOnly names %q, which no matcher declares", id)
		}
	}

	hits := make([]int, len(anchorLabelMatchers))
	total := 0
	for _, s := range lvLettersOnly {
		for i, m := range anchorLabelMatchers {
			for _, loc := range m.RE.FindAllStringIndex(s, -1) {
				total++
				hits[i]++
				if loc[0] != 0 || loc[1] != len(s) {
					t.Errorf("%s matches %q at %v, want [0 %d]", m.ID, s, loc, len(s))
				}
			}
		}
	}

	for i, m := range anchorLabelMatchers {
		listed := slices.Contains(cannotMatchLettersOnly, m.ID)
		switch {
		case listed && hits[i] > 0:
			t.Errorf("entry %s is listed as unable to match a letters-only string and matched %d", m.ID, hits[i])
		case !listed && hits[i] == 0:
			t.Errorf("entry %s is exercised by no string", m.ID)
		}
	}

	if total < 29 {
		t.Fatalf("total matches = %d, want at least 29", total)
	}
}

func TestAnchorLexicon_NoEntryMatchesSpacedLettersAsPrinted(t *testing.T) {
	if len(anchorLabelMatchers) == 0 {
		t.Fatal("anchorLabelMatchers is empty")
	}
	if ids := lvIDs(t, "TIN"); !slices.Contains(ids, "bare_tin") {
		t.Fatalf("the positive control matched nothing: lvIDs(%q) = %v, want bare_tin", "TIN", ids)
	}

	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	var seqs []string
	var build func(prefix []byte, depth int)
	build = func(prefix []byte, depth int) {
		if depth > 0 {
			seqs = append(seqs, string(prefix))
		}
		if depth == 3 {
			return
		}
		for i := 0; i < len(letters); i++ {
			build(append(prefix, letters[i]), depth+1)
		}
	}
	build(nil, 0)

	generated := 0
	hitCount := 0
	var hitExamples []string
	for _, seq := range seqs {
		for _, sep := range []string{" ", "   "} {
			s := strings.Join(strings.Split(seq, ""), sep)
			generated++
			for _, m := range anchorLabelMatchers {
				if m.RE.MatchString(s) {
					hitCount++
					if len(hitExamples) < 5 {
						hitExamples = append(hitExamples, m.ID+":"+s)
					}
				}
			}
		}
	}
	if generated != 36556 {
		t.Fatalf("generated %d strings, want 36556", generated)
	}
	if hitCount > 0 {
		t.Errorf("%d single-letter spaced strings matched a lexicon entry, e.g. %v", hitCount, hitExamples)
	}

	// lvWords is the 29 lvLettersOnly labels split into words, plus six strings that must
	// keep matching nothing however they are spaced (mirrors 01-T4's negatives).
	lvWords := [][]string{
		{"INVOICE", "NUMBER"}, {"INVOICE", "NO"}, {"PROFORMA", "INVOICE", "NO"}, {"ISSUED"},
		{"DATE"}, {"DUE", "DATE"}, {"CURRENCY"}, {"FROM"}, {"BILLED", "TO"}, {"BILL", "TO"},
		{"INVOICE", "TO"}, {"TIN"}, {"SUPPLIER", "TIN"}, {"CUSTOMER", "NO"}, {"ACCOUNT", "NUMBER"},
		{"BUYER", "SIGNATURE"}, {"SUBTOTAL"}, {"AMOUNT", "PAYABLE"}, {"GRAND", "TOTAL"}, {"TOTAL"},
		{"VAT"}, {"VAT", "NO"}, {"VAT", "REG", "NO"}, {"TAX", "ID", "NO"}, {"TAX", "INVOICE"},
		{"WITHHOLDING", "TAX"}, {"SUPPLIER"}, {"CUSTOMER"}, {"RC", "NO"},
		{"COMMERCIAL", "INVOICE"}, {"INVOICE", "NOTES"}, {"UPDATE"}, {"UP", "DATE"}, {"PAID"}, {"QXZW"},
	}
	if len(lvWords) != 35 {
		t.Fatalf("lvWords has %d entries, want 35", len(lvWords))
	}

	for _, s := range lvLettersOnly {
		covered := false
		for _, w := range lvWords {
			if s == strings.Join(w, "") {
				covered = true
				break
			}
		}
		if !covered {
			t.Fatalf("%q names no entry in lvWords", s)
		}
	}

	forms := 0
	var labelHits []string
	for _, w := range lvWords {
		var spacedWords []string
		for _, word := range w {
			spacedWords = append(spacedWords, strings.Join(strings.Split(word, ""), " "))
		}
		for _, sep := range []string{" ", "   "} {
			s := strings.Join(spacedWords, sep)
			forms++
			for _, m := range anchorLabelMatchers {
				if m.RE.MatchString(s) {
					labelHits = append(labelHits, m.ID+":"+s)
				}
			}
		}
	}
	if forms != 70 {
		t.Fatalf("forms = %d, want 70", forms)
	}
	if len(labelHits) > 0 {
		t.Errorf("%d label forms matched a lexicon entry: %v", len(labelHits), labelHits)
	}
}

// Any unicode.IsSpace rune separates, any unicode.IsLetter unit counts, and each letter keeps its case.
func TestLabelView_JoinsOnUnicodeSpaceAndKeepsEachLetter(t *testing.T) {
	tests := []struct{ in, want string }{
		{"I S S U E D", "ISSUED"},
		{"I n v o i c e   N u m b e r", "InvoiceNumber"},
		{"É T É", "ÉTÉ"},
	}
	for _, tt := range tests {
		if got := labelView(tt.in); got != tt.want {
			t.Errorf("labelView(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLabelView_ALowerCaseOrWideGapLabelMatchesItsWord(t *testing.T) {
	tests := []struct {
		spaced, word string
		want         []string
	}{
		{"i n v o i c e   n u m b e r", "invoice number", []string{"invoice_no"}},
		{"I   S   S   U   E   D", "ISSUED", []string{"issue_date"}},
	}
	for _, tt := range tests {
		wordIDs := lvIDs(t, tt.word)
		if !slices.Equal(wordIDs, tt.want) {
			t.Fatalf("lvIDs(%q) = %v, want %v", tt.word, wordIDs, tt.want)
		}
		if got := lvIDs(t, labelView(tt.spaced)); !slices.Equal(got, wordIDs) {
			t.Errorf("lvIDs(labelView(%q)) = %v, want %v", tt.spaced, got, wordIDs)
		}
	}
}

// A unit carrying punctuation or a symbol is no letter, so the token reads as printed (Ceilings).
func TestLabelView_AttachedPunctuationOrASymbolReadsAsPrinted(t *testing.T) {
	if ids := lvIDs(t, "ISSUED:"); !slices.Contains(ids, "issue_date") {
		t.Fatalf("the positive control matched nothing: lvIDs(%q) = %v, want issue_date", "ISSUED:", ids)
	}
	for _, in := range []string{"I S S U E D:", "₦ N G N"} {
		got := labelView(in)
		if got != in {
			t.Errorf("labelView(%q) = %q, want %q (byte-identical)", in, got, in)
		}
		if ids := lvIDs(t, got); len(ids) > 0 {
			t.Errorf("lvIDs(labelView(%q)) = %v, want none", in, ids)
		}
	}
}
