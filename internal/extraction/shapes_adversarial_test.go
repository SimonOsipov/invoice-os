// shapes_adversarial_test.go: edge, boundary and negative cases beyond S-01..S-13.
package extraction_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// wantOne asserts the shape emits exactly the one reading given.
func wantOne(t *testing.T, s extraction.Shape, raw, want string) {
	t.Helper()
	if got := s.Normalize(raw); !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("%s.Normalize(%q) = %v, want [%q]", s, raw, got, want)
	}
}

// wantNone asserts the shape rejects, which is an empty slice and never a fault.
func wantNone(t *testing.T, s extraction.Shape, raw string) {
	t.Helper()
	if got := s.Normalize(raw); len(got) != 0 {
		t.Errorf("%s.Normalize(%q) = %v, want an empty slice", s, raw, got)
	}
}

func TestShapeAmount_RejectsMalformedNumerics(t *testing.T) {
	rejects := []string{
		".50",      // no integer part
		"1,50.00",  // grouping is strict: every group after the first is exactly three
		"1,500.",   // trailing separator with no fraction
		"- 1500",   // the sign binds to the digits, not to the prefix slot
		"ngn 1500", // the prefix is an uppercase literal
		"N/A",      // stripping N leaves /A, which is no digit group
		"1500.005", // a third fraction digit rejects rather than rounds
		"1 500",
		"1500,00",
	}
	if len(rejects) == 0 {
		t.Fatal("fixture list is empty")
	}
	for _, raw := range rejects {
		wantNone(t, extraction.ShapeAmount, raw)
	}
	wantOne(t, extraction.ShapeAmount, "1,500.00", "1500.00")
}

// The plan sets no integer-digit cap: a value too wide for numeric(14,2) is EXTR-06's problem
// at write time, not a shape rejection. Adding a cap here would turn these red.
func TestShapeAmount_SignsAndPlacesNoIntegerCap(t *testing.T) {
	for raw, want := range map[string]string{
		"-1500.00":           "-1500.00",
		"NGN -1,500.00":      "-1500.00",
		"1,234,567.89":       "1234567.89",
		"1,500.0":            "1500.0", // a one-digit fraction is not zero-padded
		"0":                  "0",
		"1234567890123.45":   "1234567890123.45", // 13 integer digits, over the column
		"999999999999999.99": "999999999999999.99",
		"N 1500":             "1500",
	} {
		wantOne(t, extraction.ShapeAmount, raw, want)
	}
}

func TestShapeDate_ValidatesTheCalendarUnderBothReadings(t *testing.T) {
	wantOne(t, extraction.ShapeDate, "29/02/2024", "2024-02-29") // a real leap day
	wantNone(t, extraction.ShapeDate, "29/02/2026")              // not a leap year
	wantNone(t, extraction.ShapeDate, "2026-02-30")
	wantNone(t, extraction.ShapeDate, "2026-13-01")
	wantNone(t, extraction.ShapeDate, "00/03/2026")
	wantNone(t, extraction.ShapeDate, "12/03/26") // no two-digit-year layout: a century pivot reads the clock
}

// The month-first reading is appended only when it differs, so a self-symmetric date yields
// one candidate and not a duplicated pair.
func TestShapeDate_DedupesWhenBothReadingsAgree(t *testing.T) {
	got := extraction.ShapeDate.Normalize("05/05/2026")
	if want := []string{"2026-05-05"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ShapeDate.Normalize(%q) = %v, want exactly %v", "05/05/2026", got, want)
	}
}

// The numeric layouts are fixed-width, so a one-digit component is zero-padded before parsing.
// Without the padding these reject for a reason no rule states.
func TestShapeDate_ZeroPadsASingleDigitComponent(t *testing.T) {
	for raw, want := range map[string][]string{
		"3/12/2026": {"2026-12-03", "2026-03-12"},
		"1/2/2026":  {"2026-02-01", "2026-01-02"},
		"3/4/2026":  {"2026-04-03", "2026-03-04"},
	} {
		if got := extraction.ShapeDate.Normalize(raw); !reflect.DeepEqual(got, want) {
			t.Errorf("ShapeDate.Normalize(%q) = %v, want %v in this exact order", raw, got, want)
		}
	}
}

// The ambiguity fork is on the components, not on the slash: treating 12-03-2026 as
// unambiguously day-first would be the silent default D-10 forbids.
func TestShapeDate_ForksOnEveryNumericSeparator(t *testing.T) {
	want := []string{"2026-03-12", "2026-12-03"}
	for _, raw := range []string{"12/03/2026", "12-03-2026", "12.03.2026"} {
		if got := extraction.ShapeDate.Normalize(raw); !reflect.DeepEqual(got, want) {
			t.Errorf("ShapeDate.Normalize(%q) = %v, want %v in this exact order", raw, got, want)
		}
	}
	wantNone(t, extraction.ShapeDate, "12/03-2026") // mixed separators match no layout
	wantNone(t, extraction.ShapeDate, "12.03-2026")
}

// The pattern is ASCII, so a lookalike separator or digit rejects. Over-rejection is the safe
// direction: a non-match is not a fault.
func TestShapeTIN_RejectsLookalikeSeparatorsAndDigits(t *testing.T) {
	for _, raw := range []string{
		"12345678 9012",   // space instead of the hyphen
		"12345678–9012",   // en dash
		"１２３４５６７８-９０１２",   // full-width digits
		" 12345678-9012 ", // RE2 \s is ASCII-only, so NBSP does not trim
		"12345678-9012 x",
		"12345678--9012",
	} {
		wantNone(t, extraction.ShapeTIN, raw)
	}
	wantOne(t, extraction.ShapeTIN, "\t12345678-9012\n", "12345678-9012") // ASCII space does trim
}

// There is no NGN allowlist here: any three ASCII letters pass through upper-cased, because
// enumEval is what rejects a non-NGN code later.
func TestShapeCurrency_PassesAnyThreeLetterCodeThroughUpperCased(t *testing.T) {
	for raw, want := range map[string]string{"usd": "USD", "Ngn": "NGN", "EUR": "EUR", " NgN ": "NGN"} {
		wantOne(t, extraction.ShapeCurrency, raw, want)
	}
	for _, raw := range []string{"NGNX", "NG", "₦₦", "ngń", "N G N", ""} {
		wantNone(t, extraction.ShapeCurrency, raw)
	}
}

// The bound is 256 RUNES on the trimmed string, not bytes: a 256-rune accented name is 512
// bytes and must still pass.
func TestShapeName_BoundsOn256Runes(t *testing.T) {
	for _, c := range []struct {
		runes int
		unit  string
		ok    bool
	}{
		{256, "a", true},
		{257, "a", false},
		{256, "é", true}, // 512 bytes
		{257, "é", false},
		{256, "日", true}, // 768 bytes
		{257, "日", false},
	} {
		v := strings.Repeat(c.unit, c.runes)
		got := extraction.ShapeName.Normalize(v)
		if c.ok {
			wantOne(t, extraction.ShapeName, v, v)
		} else if len(got) != 0 {
			t.Errorf("ShapeName.Normalize(<%d x %q>) = %d readings, want an empty slice", c.runes, c.unit, len(got))
		}
	}
	// The count is taken after trimming, so padding a 256-rune name does not push it over.
	padded := "  " + strings.Repeat("a", 256) + "  "
	wantOne(t, extraction.ShapeName, padded, strings.Repeat("a", 256))
	for _, raw := range []string{"", "   ", "\t\n"} {
		wantNone(t, extraction.ShapeName, raw)
	}
}

// Only the amount guard rejects a bare number: 1500 passes the identifier pattern. Removing
// that guard leaves every S-01..S-13 spec green, so this test is what holds it in place.
func TestShapeInvoiceNumber_RejectsABareNumberThroughTheAmountGuard(t *testing.T) {
	for _, raw := range []string{"1500", "0042", "0", "1500.00"} {
		if !reflect.DeepEqual(extraction.ShapeInvoiceNumber.Normalize(raw), []string(nil)) {
			t.Errorf("ShapeInvoiceNumber.Normalize(%q) accepted a bare amount", raw)
		}
	}
	// A consequence of reAmount treating a bare N as the naira prefix: N1500 reads as an
	// amount and so is not an invoice number.
	wantNone(t, extraction.ShapeInvoiceNumber, "N1500")
	for _, raw := range []string{"INV-001", "NGN", "A1", "2026/INV/0042"} {
		wantOne(t, extraction.ShapeInvoiceNumber, raw, raw)
	}
}

// Every numeric-date layout is a date the guard must also catch, not only the ISO one.
func TestShapeInvoiceNumber_RejectsEveryDateLayout(t *testing.T) {
	for _, raw := range []string{"2026-03-12", "12/03/2026", "12-03-2026", "12.03.2026", "3/12/2026"} {
		wantNone(t, extraction.ShapeInvoiceNumber, raw)
	}
	wantOne(t, extraction.ShapeInvoiceNumber, "INV-001", "INV-001")
}

func TestShape_UnknownShapeReturnsEmptyAndDoesNotPanic(t *testing.T) {
	for _, s := range []extraction.Shape{extraction.Shape("bogus"), extraction.Shape(""), extraction.Shape("TIN")} {
		if got := s.Normalize("12345678-9012"); got != nil {
			t.Errorf("Shape(%q).Normalize = %v, want nil", string(s), got)
		}
	}
}

// adversarialFixtures spans all six shapes with both accepting and rejecting inputs.
func adversarialFixtures() []struct {
	shape   extraction.Shape
	raw     string
	accepts bool
} {
	return []struct {
		shape   extraction.Shape
		raw     string
		accepts bool
	}{
		{extraction.ShapeTIN, " 12345678 - 9012 ", true},
		{extraction.ShapeTIN, "12345678 9012", false},
		{extraction.ShapeAmount, "-1,500.00", true},
		{extraction.ShapeAmount, ".50", false},
		{extraction.ShapeDate, "3/12/2026", true},
		{extraction.ShapeDate, "05/05/2026", true},
		{extraction.ShapeDate, "29/02/2026", false},
		{extraction.ShapeCurrency, "usd", true},
		{extraction.ShapeCurrency, "NGNX", false},
		{extraction.ShapeInvoiceNumber, "2026/INV/0042", true},
		{extraction.ShapeInvoiceNumber, "1500", false},
		{extraction.ShapeName, "  Honeywell Group  ", true},
		{extraction.ShapeName, "   ", false},
	}
}

// Two calls must not share backing storage: reflect.DeepEqual alone cannot tell a fresh slice
// from a cached one, so this mutates the first result and re-reads the second.
func TestShapes_DoNotShareBackingStorageAcrossCalls(t *testing.T) {
	fixtures := adversarialFixtures()
	if len(fixtures) == 0 {
		t.Fatal("fixture list is empty")
	}
	accepting := 0
	for _, f := range fixtures {
		if !f.accepts {
			continue
		}
		accepting++
		first := f.shape.Normalize(f.raw)
		second := f.shape.Normalize(f.raw)
		if len(first) == 0 {
			t.Fatalf("%s.Normalize(%q) returned nothing; fixture is mislabelled", f.shape, f.raw)
		}
		before := append([]string(nil), second...)
		for i := range first {
			first[i] = "MUTATED"
		}
		if !reflect.DeepEqual(second, before) {
			t.Errorf("%s.Normalize(%q): mutating one result changed another: %v, want %v",
				f.shape, f.raw, second, before)
		}
	}
	if accepting == 0 {
		t.Fatal("no accepting fixture exercised")
	}
}

// Determinism over the adversarial fixture set, which reaches all six shapes and both signs of
// the accept decision.
func TestShapes_AreDeterministicOverTheAdversarialFixtures(t *testing.T) {
	fixtures := adversarialFixtures()
	if len(fixtures) == 0 {
		t.Fatal("fixture list is empty")
	}
	seen := map[extraction.Shape]bool{}
	for _, f := range fixtures {
		seen[f.shape] = true
		first := f.shape.Normalize(f.raw)
		for i := 0; i < 5; i++ {
			if got := f.shape.Normalize(f.raw); !reflect.DeepEqual(got, first) {
				t.Errorf("%s.Normalize(%q) call %d = %v, first call = %v", f.shape, f.raw, i+2, got, first)
			}
		}
		if f.accepts == (len(first) == 0) {
			t.Errorf("%s.Normalize(%q) = %v, accepts=%v", f.shape, f.raw, first, f.accepts)
		}
	}
	if len(seen) != 6 {
		t.Errorf("fixtures cover %d shapes, want all 6", len(seen))
	}
}

// seedRuleParams returns the JSON params of one seeded MBS rule, read from the migration so
// this test tracks the shipped rule rather than a copy of it.
func seedRuleParams(t *testing.T, ruleCode string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", "20260711121327_seed_mbs_v1.sql"))
	if err != nil {
		t.Fatalf("read the MBS seed migration: %v", err)
	}
	re := regexp.MustCompile(`\('` + regexp.QuoteMeta(ruleCode) + `',\s*'[^']*',\s*'[^']*',\s*'([^']*)'`)
	m := re.FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("rule %q not found in the MBS seed migration", ruleCode)
	}
	return m[1]
}

// AC #1 names the shipped rule by file and line. Assert against the pattern the migration
// actually seeds, so a change to either side shows up here.
func TestShapeTIN_OutputSatisfiesTheShippedFormatRule(t *testing.T) {
	var params struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal([]byte(seedRuleParams(t, "supplier-tin-format")), &params); err != nil {
		t.Fatalf("decode supplier-tin-format params: %v", err)
	}
	shipped := regexp.MustCompile(params.Pattern)

	accepted := []string{"12345678-9012", " 12345678 - 9012 ", "\t00000000-0000\n", "99999999-0001"}
	if len(accepted) == 0 {
		t.Fatal("fixture list is empty")
	}
	emitted := 0
	for _, raw := range accepted {
		got := extraction.ShapeTIN.Normalize(raw)
		if len(got) != 1 {
			t.Fatalf("ShapeTIN.Normalize(%q) = %v, want one reading", raw, got)
		}
		emitted++
		if !shipped.MatchString(got[0]) {
			t.Errorf("ShapeTIN.Normalize(%q) = %q, which fails the seeded rule %q", raw, got[0], params.Pattern)
		}
	}
	if emitted == 0 {
		t.Fatal("no TIN reading was checked against the shipped rule")
	}
}

// AC #5's upper-casing only earns its place if the result equals the seeded enum value under
// the exact comparison enumEval makes (reflect.DeepEqual over the JSON-decoded value).
func TestShapeCurrency_OutputEqualsTheSeededEnumValue(t *testing.T) {
	var params struct {
		Values []any `json:"values"`
	}
	if err := json.Unmarshal([]byte(seedRuleParams(t, "currency-allowed")), &params); err != nil {
		t.Fatalf("decode currency-allowed params: %v", err)
	}
	if len(params.Values) == 0 {
		t.Fatal("the seeded currency enum has no values")
	}
	for _, raw := range []string{"ngn", "NGN", "Ngn", "₦"} {
		got := extraction.ShapeCurrency.Normalize(raw)
		if len(got) != 1 {
			t.Fatalf("ShapeCurrency.Normalize(%q) = %v, want one reading", raw, got)
		}
		matched := false
		for _, want := range params.Values {
			if reflect.DeepEqual(want, any(got[0])) {
				matched = true
			}
		}
		if !matched {
			t.Errorf("ShapeCurrency.Normalize(%q) = %q, which no seeded enum value %v equals",
				raw, got[0], params.Values)
		}
	}
}

// AC #3 names the importer's parse by file and line. Every emitted reading must survive that
// exact layout, which is the whole point of normalising here.
func TestShapeDate_OutputParsesUnderTheImporterLayout(t *testing.T) {
	raws := []string{"2026-03-12", "12 Mar 2026", "Mar 12, 2026", "12/03/2026", "3/12/2026", "29/02/2024", "12.03.2026"}
	readings := 0
	for _, raw := range raws {
		got := extraction.ShapeDate.Normalize(raw)
		if len(got) == 0 {
			t.Fatalf("ShapeDate.Normalize(%q) returned nothing; fixture is mislabelled", raw)
		}
		for _, iso := range got {
			readings++
			parsed, err := time.Parse("2006-01-02", iso) // internal/importer/service.go
			if err != nil {
				t.Errorf("ShapeDate.Normalize(%q) emitted %q, which the importer rejects: %v", raw, iso, err)
				continue
			}
			if round := parsed.Format("2006-01-02"); round != iso {
				t.Errorf("ShapeDate.Normalize(%q) emitted %q, which does not round-trip (%q)", raw, iso, round)
			}
		}
	}
	if readings == 0 {
		t.Fatal("no date reading was checked against the importer layout")
	}
}

// numeric(14,2) has two fraction digits, so no emitted amount may carry a third.
func TestShapeAmount_OutputFitsTheColumnScale(t *testing.T) {
	raws := []string{"NGN 1,500.00", "₦1,500.00", "1500", "1,500.0", "-1500.00", "1,234,567.89", "0"}
	checked := 0
	for _, raw := range raws {
		got := extraction.ShapeAmount.Normalize(raw)
		if len(got) != 1 {
			t.Fatalf("ShapeAmount.Normalize(%q) = %v, want one reading", raw, got)
		}
		checked++
		v := got[0]
		if strings.ContainsAny(v, ",₦N ") {
			t.Errorf("ShapeAmount.Normalize(%q) = %q, which still carries a separator or prefix", raw, v)
		}
		if i := strings.IndexByte(v, '.'); i >= 0 && len(v)-i-1 > 2 {
			t.Errorf("ShapeAmount.Normalize(%q) = %q, which has more than two fraction digits", raw, v)
		}
	}
	if checked == 0 {
		t.Fatal("no amount reading was checked against the column scale")
	}
}

// isBareAnchorLabel refuses a value one lexicon entry matches WHOLE, so an owning phrase is a
// label in its entirety and no shape may read one as a value.
func TestShapes_AnOwningPhraseIsABareAnchorLabel(t *testing.T) {
	for _, raw := range []string{
		"Customer No.", "Buyer's Signature", "Supplier's Signature", "Account No.", "Invoice to",
		// The supplier spellings of both entries, which the buyer cases above cannot reach.
		"Supplier No.", "Vendor Code", "Seller Signature",
		// A registration identifier, a company number and a document title are whole labels too.
		"VAT REG NO", "TAX REGISTRATION NUMBER", "RC NUMBER", "CAC NO", "TAX INVOICE",
	} {
		wantNone(t, extraction.ShapeName, raw)
	}
	// The controls: only a WHOLE-value match is refused. "Customer Nominee Ltd" is the boundary
	// -- party_ref's \b after "no" is what keeps it a name.
	wantOne(t, extraction.ShapeName, "Adeyemi Trading Limited", "Adeyemi Trading Limited")
	wantOne(t, extraction.ShapeName, "Customer Nominee Ltd", "Customer Nominee Ltd")
	wantOne(t, extraction.ShapeName, "Vendor Code Systems Plc", "Vendor Code Systems Plc")
	// The label plus its value is not a whole match, so the token keeps its name reading.
	wantOne(t, extraction.ShapeName, "RC NUMBER: RC-000142", "RC NUMBER: RC-000142")

	// The separator groups are fully optional, so an unspaced compound matches whole and stops
	// being an invoice number too. Kept deliberately: no shipped layout prints one.
	wantNone(t, extraction.ShapeInvoiceNumber, "accountno")
	wantNone(t, extraction.ShapeInvoiceNumber, "customerno")
	wantNone(t, extraction.ShapeInvoiceNumber, "Customer No.")
	for _, raw := range []string{"RCNO", "RCNUMBER", "CACNO", "VATNO", "TAXNO", "TAXINVOICE"} {
		wantNone(t, extraction.ShapeInvoiceNumber, raw)
	}
	// reInvNum forbids the space and the colon, so the spaced spelling is refused before
	// isBareAnchorLabel is consulted. A pin on the printed form, never this entry's oracle.
	wantNone(t, extraction.ShapeInvoiceNumber, "RC NUMBER: RC-000142")

	// The boundaries that bound the class: a digit after the suffix defeats party_ref's \b, a
	// hyphen is no separator the company-number entry takes, an extra character either side
	// defeats its \b, and "NO" running straight on defeats the title's.
	wantOne(t, extraction.ShapeInvoiceNumber, "customerno1", "customerno1")
	wantOne(t, extraction.ShapeInvoiceNumber, "INV-2103", "INV-2103")
	wantOne(t, extraction.ShapeInvoiceNumber, "RC-000142", "RC-000142")
	wantOne(t, extraction.ShapeInvoiceNumber, "RCNOX", "RCNOX")
	wantOne(t, extraction.ShapeInvoiceNumber, "XRCNO", "XRCNO")
	wantOne(t, extraction.ShapeInvoiceNumber, "TAXINVOICENO", "TAXINVOICENO")
}

// --- EXTR-25-01: a decorated naira token still normalises to NGN --------------

// AC-1.2. A money token with no naira glyph never reads as currency, however currency-shaped it
// looks. "N1,500.00" and "RATE (N)" are the two the bare-N amount prefix (reAmount) could tempt
// into a false accept here; they still reject. Paired with a positive control so an all-reject
// regexp cannot pass this test.
func TestShapeCurrency_RejectsAMoneyTokenWithNoNaira(t *testing.T) {
	rejects := []string{
		"N1,500.00", "RATE (N)", "1,500.00", "Total: NGN 3,225.00", "TOTAL DUE (NGN)",
		"₦₦", "N G N", "NG", "NGNX", "",
	}
	if len(rejects) == 0 {
		t.Fatal("fixture list is empty")
	}
	for _, raw := range rejects {
		wantNone(t, extraction.ShapeCurrency, raw)
	}
	wantOne(t, extraction.ShapeCurrency, "₦1,500.00", "NGN")
}

// AC-1.2b. Both bounds around the naira glyph are load-bearing. Subtests name which bound a
// failure belongs to: the 24-rune decoration cap on each side, or the exactly-one-₦ requirement.
func TestShapeCurrency_BoundsTheDecorationAroundTheNaira(t *testing.T) {
	pad24, pad25 := strings.Repeat("a", 24), strings.Repeat("a", 25)

	t.Run("24_runes_either_side_accepts", func(t *testing.T) {
		wantOne(t, extraction.ShapeCurrency, pad24+"₦", "NGN")
		wantOne(t, extraction.ShapeCurrency, "₦"+pad24, "NGN")
	})
	t.Run("25_runes_either_side_rejects", func(t *testing.T) {
		wantNone(t, extraction.ShapeCurrency, pad25+"₦")
		wantNone(t, extraction.ShapeCurrency, "₦"+pad25)
	})
	t.Run("a_second_naira_glyph_rejects", func(t *testing.T) {
		wantNone(t, extraction.ShapeCurrency, "a₦b₦c")
		wantNone(t, extraction.ShapeCurrency, "₦₦")
	})
}

// AC-1.3. Over a generated sweep of decorated naira tokens, the naira arm's output set is the
// singleton {"NGN"} -- reCurrency can never take a token holding ₦ (not three ASCII letters),
// and reNaira has one return literal. Every prefix here carries a non-whitespace word, so none
// of the 200 collapses to the bare "\s*₦\s*" shape the unwidened pattern already accepts -- the
// floor below is what makes this test red before the widening lands.
func TestShapeCurrency_TheNairaArmEmitsOnlyNGN(t *testing.T) {
	prefixes := []string{"Amount ", "Unit rate ", "VAT ", "Total: ", "TAX ", "Rate "}
	suffixes := []string{"", " 1,500.00", " (2,687.50)", "/kg", " due", " only"}

	accepted := 0
	for i := 0; i < 200; i++ {
		raw := prefixes[i%len(prefixes)] + "₦" + suffixes[(i/len(prefixes))%len(suffixes)]
		for _, v := range extraction.ShapeCurrency.Normalize(raw) {
			accepted++
			if v != "NGN" {
				t.Errorf("ShapeCurrency.Normalize(%q) = %q, want only %q", raw, v, "NGN")
			}
		}
	}
	if accepted == 0 {
		t.Fatal("no generated ₦-bearing token was accepted; the naira arm was not exercised")
	}
}

// AC-1.4. ShapeCurrency is still the only reader of reNaira -- a non-recursive scan of this
// package's non-test files proves reNaira appears in exactly two places, its declaration and
// normalizeCurrency, so the widening's blast radius is the currency field alone.
func TestShapeCurrency_ReNairaHasOneCaller(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/extraction: %v", err)
	}
	files, uses := 0, 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files++
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		uses += strings.Count(string(b), "reNaira")
	}
	if files < 20 {
		t.Fatalf("scanned %d non-test files, want at least 20 -- reading the wrong directory", files)
	}
	if uses != 2 {
		t.Errorf("reNaira appears %d times across %d non-test files, want exactly 2 (its declaration and normalizeCurrency)", uses, files)
	}
}
