package portfolio

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// tinCase is one row of a ValidateTIN acceptance table. wantCanon is only
// checked when wantValid is true.
type tinCase struct {
	name      string
	raw       string
	wantValid bool
	wantCanon string
}

func runTINCases(t *testing.T, cases []tinCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canonical, err := ValidateTIN(tc.raw)

			if tc.wantValid {
				if err != nil {
					t.Fatalf("ValidateTIN(%q) unexpected error: %v", tc.raw, err)
				}
				if canonical != tc.wantCanon {
					t.Errorf("ValidateTIN(%q) canonical = %q, want %q", tc.raw, canonical, tc.wantCanon)
				}
				return
			}

			if !errors.Is(err, ErrInvalidTIN) {
				t.Fatalf("ValidateTIN(%q) err = %v, want errors.Is(err, ErrInvalidTIN)", tc.raw, err)
			}
			if canonical != "" {
				t.Errorf("ValidateTIN(%q) canonical = %q, want empty string on failure", tc.raw, canonical)
			}
		})
	}
}

// TestValidateTIN_JTB10Valid: a bare 10-digit JTB-issued TIN with a passing
// Luhn checksum is accepted and returned unchanged as the canonical form.
func TestValidateTIN_JTB10Valid(t *testing.T) {
	runTINCases(t, []tinCase{
		{name: "10-digit JTB TIN", raw: "1234567897", wantValid: true, wantCanon: "1234567897"},
	})
}

// TestValidateTIN_FIRSDashValid: the FIRS dash-formatted TIN (8 digits, "-",
// 4 digits) is accepted and canonicalized to its 12-digit hyphen-free form.
func TestValidateTIN_FIRSDashValid(t *testing.T) {
	runTINCases(t, []tinCase{
		{name: "FIRS dash-formatted TIN", raw: "12345678-0006", wantValid: true, wantCanon: "123456780006"},
	})
}

// TestValidateTIN_ChecksumRejected: input that is structurally valid (10
// digits) but fails the Luhn (mod-10) checksum must be rejected.
func TestValidateTIN_ChecksumRejected(t *testing.T) {
	runTINCases(t, []tinCase{
		{name: "format-valid but Luhn checksum fails", raw: "1234567890", wantValid: false},
	})
}

// TestValidateTIN_TooShort: fewer than 10 digits never matches either
// accepted shape and is rejected before checksum is even considered.
func TestValidateTIN_TooShort(t *testing.T) {
	runTINCases(t, []tinCase{
		{name: "5-digit input", raw: "12345", wantValid: false},
	})
}

// TestValidateTIN_NonDigit: letters embedded among digits fail both the
// 10-digit and 8-4 dash shapes.
func TestValidateTIN_NonDigit(t *testing.T) {
	runTINCases(t, []tinCase{
		{name: "letters embedded in digits", raw: "12ab567897", wantValid: false},
	})
}

// TestValidateTIN_TooLong: more than 12 digits never matches either accepted
// shape.
func TestValidateTIN_TooLong(t *testing.T) {
	runTINCases(t, []tinCase{
		{name: "13-digit input", raw: "1234567890123", wantValid: false},
	})
}

// TestValidateTIN_Empty: an empty or whitespace-only TIN is rejected as
// required, prior to any format check.
func TestValidateTIN_Empty(t *testing.T) {
	runTINCases(t, []tinCase{
		{name: "empty string", raw: "", wantValid: false},
		{name: "whitespace only", raw: "  ", wantValid: false},
	})
}

// --- QA adversarial/edge coverage added post-implementation (task-35 Mode B) ---
//
// These cases extend, but do not weaken, the AC table above. They probe the
// boundaries the AC fixtures don't reach: Luhn on the wider \d{12} surface,
// format adversarial inputs (misplaced separators, signs, embedded
// whitespace, boundary lengths, non-ASCII digits), whitespace trimming with
// non-space characters, and round-trip idempotency of the canonical form.

// TestValidateTIN_LuhnBoundary_RandomFailsChecksum: a format-valid (bare
// 12-digit) string whose Luhn sum is not a multiple of 10 must still be
// rejected. This proves that accepting bare \d{12} input (the executor's
// extension beyond the story's literal step-2 prose -- see [A2]'s
// "12345678-0001 and 123456780001 collide" + the committed
// TestValidateTIN_CanonicalDedup anchor) is not a blanket "any 12 digits"
// pass: the checksum gate still applies to every accepted shape.
//
// Luhn over "581274639202" (rightmost digit undoubled, alternating leftward):
//
//	digit:    5  8  1  2  7  4  6  3  9  2  0  2
//	doubled:  1  0  1  0  1  0  1  0  1  0  1  0   (1 = doubled, applied right-to-left)
//	-> reading right-to-left: 2(x1)=2, 0(x2)=0, 2(x1)=2, 9(x2)=18-9=9,
//	   3(x1)=3, 6(x2)=12-9=3, 4(x1)=4, 7(x2)=14-9=5, 2(x1)=2, 1(x2)=2,
//	   8(x1)=8, 5(x2)=10-9=1
//	sum = 2+0+2+9+3+3+4+5+2+2+8+1 = 41 -> 41%10 = 1 != 0 -> Luhn FAILS.
func TestValidateTIN_LuhnBoundary_RandomFailsChecksum(t *testing.T) {
	runTINCases(t, []tinCase{
		{name: "random 12-digit, format-valid, Luhn-failing", raw: "581274639202", wantValid: false},
	})
}

// TestValidateTIN_LuhnBoundary_AllZeros: an all-zero 10-digit string passes
// both the format gate (exactly 10 digits) and the Luhn checksum -- every
// digit is 0, so doubling or not doubling never changes it, and the sum is
// trivially 0 (a multiple of 10). This is a known, documented limitation of a
// self-contained structural check, NOT a bug: story Decision [A1] states
// Luhn "does not prove the TIN is a real, FIRS/JTB-registered taxpayer ...
// It will therefore ... accept structurally-arbitrary Luhn-passing numbers."
// Pinning the current accept behavior here means a future change to reject
// degenerate all-same-digit TINs is a deliberate, visible diff, not a silent
// regression either way.
func TestValidateTIN_LuhnBoundary_AllZeros(t *testing.T) {
	runTINCases(t, []tinCase{
		{name: "all-zeros 10-digit (Luhn sum 0, accepted per [A1])", raw: "0000000000", wantValid: true, wantCanon: "0000000000"},
	})
}

// TestValidateTIN_FormatAdversarial: inputs shaped to look almost-valid but
// that must be rejected by the format gate -- misplaced hyphens, multiple
// hyphens, a leading sign character, embedded internal whitespace, the two
// untested boundary lengths immediately adjacent to the accepted shapes (9
// and 11 digits), a fresh 13-digit boundary case, and non-ASCII Unicode
// digits.
func TestValidateTIN_FormatAdversarial(t *testing.T) {
	runTINCases(t, []tinCase{
		{name: "hyphen in wrong position (7+5 not 8+4)", raw: "1234567-80006", wantValid: false},
		{name: "hyphen in wrong position (9+2 not 8+4)", raw: "123456789-06", wantValid: false},
		{name: "multiple hyphens", raw: "12345678-00-06", wantValid: false},
		{name: "leading plus sign on otherwise-valid 12 digits", raw: "+123456780006", wantValid: false},
		{name: "leading minus sign on otherwise-valid 12 digits", raw: "-123456780006", wantValid: false},
		{name: "embedded internal whitespace splits a valid 10-digit TIN", raw: "12345 67897", wantValid: false},
		{name: "9-digit boundary (one below bare-10)", raw: "123456789", wantValid: false},
		{name: "11-digit boundary (between bare-10 and bare-12)", raw: "12345678901", wantValid: false},
		{name: "13-digit boundary (one above bare-12)", raw: "9876543210987", wantValid: false},
		// Arabic-Indic digits U+0661..U+0669,U+0660 spelling "1234567890".
		{name: "Arabic-Indic Unicode digits, not ASCII \\d", raw: "١٢٣٤٥٦٧٨٩٠", wantValid: false},
		// Full-width digits U+FF11..U+FF19,U+FF17 spelling "1234567897"
		// (the JTB-valid AC #1 anchor digits, in full-width form).
		{name: "full-width Unicode digits, not ASCII \\d", raw: "１２３４５６７８９７", wantValid: false},
	})
}

// TestValidateTIN_WhitespaceTrimmed_NonSpaceChars: leading/trailing tabs and
// newlines (not just plain spaces) around an otherwise-valid TIN must be
// trimmed and the TIN accepted -- strings.TrimSpace trims everything
// unicode.IsSpace reports, not just U+0020, so this must not regress to a
// narrower (space-only) trim.
func TestValidateTIN_WhitespaceTrimmed_NonSpaceChars(t *testing.T) {
	runTINCases(t, []tinCase{
		{name: "tabs and newlines around valid 10-digit JTB TIN", raw: "\t\n 1234567897 \n\t", wantValid: true, wantCanon: "1234567897"},
	})
}

// TestValidateTIN_Idempotent: feeding a prior canonical output back into
// ValidateTIN must return the identical canonical string and a nil error.
// This is the round-trip property that justifies accepting bare \d{12} input
// in the first place (see [A2] + TestValidateTIN_CanonicalDedup): the
// persisted column is digits-only, so re-submitting an entity's
// already-stored TIN (e.g. an unchanged field in a PATCH body) must
// re-validate successfully, not 400.
func TestValidateTIN_Idempotent(t *testing.T) {
	const dashInput = "12345678-0006"

	firstPass, err := ValidateTIN(dashInput)
	if err != nil {
		t.Fatalf("ValidateTIN(%q) unexpected error: %v", dashInput, err)
	}

	secondPass, err := ValidateTIN(firstPass)
	if err != nil {
		t.Fatalf("ValidateTIN(%q) [round-trip of canonical output] unexpected error: %v", firstPass, err)
	}
	if secondPass != firstPass {
		t.Errorf("ValidateTIN(%q) = %q, want identical round-trip %q", firstPass, secondPass, firstPass)
	}
}

// TestValidateTIN_CanonicalDedup: the dash-formatted and plain-digit spellings
// of the same TIN must canonicalize to the identical persisted string, so
// table-level uniqueness (enforced elsewhere) actually catches the duplicate.
func TestValidateTIN_CanonicalDedup(t *testing.T) {
	const want = "123456780006"

	dashCanonical, dashErr := ValidateTIN("12345678-0006")
	if dashErr != nil {
		t.Fatalf("ValidateTIN(%q) unexpected error: %v", "12345678-0006", dashErr)
	}
	if dashCanonical != want {
		t.Errorf("ValidateTIN(%q) canonical = %q, want %q", "12345678-0006", dashCanonical, want)
	}

	plainCanonical, plainErr := ValidateTIN("123456780006")
	if plainErr != nil {
		t.Fatalf("ValidateTIN(%q) unexpected error: %v", "123456780006", plainErr)
	}
	if plainCanonical != want {
		t.Errorf("ValidateTIN(%q) canonical = %q, want %q", "123456780006", plainCanonical, want)
	}

	if dashCanonical != plainCanonical {
		t.Errorf("canonical mismatch: dash form = %q, plain form = %q, want identical", dashCanonical, plainCanonical)
	}
}

// reasonCase is one input and the reason ValidateTIN must give for it.
type reasonCase struct {
	name string
	raw  string
	want string
}

func runReasonCases(t *testing.T, cases []reasonCase) {
	t.Helper()
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canonical, err := ValidateTIN(tc.raw)
			var te *TINError
			if !errors.As(err, &te) {
				t.Fatalf("ValidateTIN(%q) err = %v, want a *TINError", tc.raw, err)
			}
			if te.Reason != tc.want {
				t.Errorf("ValidateTIN(%q) Reason = %q, want %q", tc.raw, te.Reason, tc.want)
			}
			if canonical != "" {
				t.Errorf("ValidateTIN(%q) canonical = %q, want empty on failure", tc.raw, canonical)
			}
		})
	}
}

// TestValidateTIN_ReasonRequired: AC (a).
func TestValidateTIN_ReasonRequired(t *testing.T) {
	runReasonCases(t, []reasonCase{
		{"empty", "", TINRequiredMessage},
		{"spaces", "  ", TINRequiredMessage},
		{"tab and newline", "\t\n", TINRequiredMessage},
	})
}

// TestValidateTIN_ReasonShape_Characters: AC (b). The Arabic-Indic and
// full-width rows are the ones in TestValidateTIN_FormatAdversarial.
func TestValidateTIN_ReasonShape_Characters(t *testing.T) {
	runReasonCases(t, []reasonCase{
		{"letters only", "BADTIN", TINShapeMessage},
		{"letters among digits", "12ab567897", TINShapeMessage},
		{"leading plus", "+123456780006", TINShapeMessage},
		{"inner space", "12345 67897", TINShapeMessage},
		{"Arabic-Indic digits", "١٢٣٤٥٦٧٨٩٠", TINShapeMessage},
		{"full-width digits", "１２３４５６７８９７", TINShapeMessage},
	})
}

// TestValidateTIN_ReasonLength: AC (c). The count in the message is the digit
// count, so a hyphen is not counted.
func TestValidateTIN_ReasonLength(t *testing.T) {
	runReasonCases(t, []reasonCase{
		{"5 digits", "12345", fmt.Sprintf(TINLengthMessage, 5)},
		{"9 digits", "123456789", fmt.Sprintf(TINLengthMessage, 9)},
		{"11 digits", "12345678901", fmt.Sprintf(TINLengthMessage, 11)},
		{"13 digits", "9876543210987", fmt.Sprintf(TINLengthMessage, 13)},
		{"11 digits with a hyphen at 8", "12345678-000", fmt.Sprintf(TINLengthMessage, 11)},
		{"11 digits with a hyphen at 9", "123456789-06", fmt.Sprintf(TINLengthMessage, 11)},
	})
}

// TestValidateTIN_ReasonShape_Hyphen: AC (d). "12345678-90" has 10 digits, so
// any hyphen is a shape fault.
func TestValidateTIN_ReasonShape_Hyphen(t *testing.T) {
	runReasonCases(t, []reasonCase{
		{"hyphen at 7", "1234567-80006", TINShapeMessage},
		{"two hyphens", "12345678-00-06", TINShapeMessage},
		{"leading hyphen", "-123456780006", TINShapeMessage},
		{"10 digits, hyphen at 6", "123456-7890", TINShapeMessage},
		{"10 digits, hyphen at 8", "12345678-90", TINShapeMessage},
	})
}

// TestValidateTIN_ReasonChecksum: AC (e).
func TestValidateTIN_ReasonChecksum(t *testing.T) {
	runReasonCases(t, []reasonCase{
		{"10 digits", "1234567890", TINChecksumMessage},
		{"8-4 hyphenated", "12345678-0001", TINChecksumMessage},
		{"bare 12 digits", "581274639202", TINChecksumMessage},
	})
}

// TestValidateTIN_EveryRefusalIsErrInvalidTIN: AC (g), every class, bare and
// wrapped. The wrapped error still reaches a caller's errors.As and errors.Is.
func TestValidateTIN_EveryRefusalIsErrInvalidTIN(t *testing.T) {
	inputs := []string{"", "BADTIN", "+12345", "12345", "12345678-90", "123456780006-", "1234567890"}
	for _, raw := range inputs {
		_, err := ValidateTIN(raw)
		if err == nil {
			t.Fatalf("ValidateTIN(%q) accepted, want a refusal", raw)
		}
		if !errors.Is(err, ErrInvalidTIN) {
			t.Errorf("ValidateTIN(%q) err = %v, want errors.Is(err, ErrInvalidTIN)", raw, err)
		}
		wrapped := fmt.Errorf("create entity: %w", err)
		var te *TINError
		if !errors.Is(wrapped, ErrInvalidTIN) || !errors.As(wrapped, &te) || te.Reason == "" {
			t.Errorf("ValidateTIN(%q) wrapped = %v, want errors.Is(ErrInvalidTIN) and a *TINError with a Reason", raw, wrapped)
		}
		if errors.Is(err, ErrValidation) || errors.Is(err, ErrDuplicateTIN) {
			t.Errorf("ValidateTIN(%q) err = %v, must not match another sentinel", raw, err)
		}
	}
}

// TestValidateTIN_ShapeBeatsLength: a stray character is a shape fault even
// when the digit count is also wrong, so the message does not blame the length.
func TestValidateTIN_ShapeBeatsLength(t *testing.T) {
	runReasonCases(t, []reasonCase{
		{"plus", "+12345", TINShapeMessage},
		{"inner space", "12345 678", TINShapeMessage},
		{"inner tab", "123\t45", TINShapeMessage},
		{"inner no-break space", "12345\u00a0678", TINShapeMessage},
		{"letter", "A1234", TINShapeMessage},
		{"dot", "1234.5678", TINShapeMessage},
		{"NUL byte", "1234\x00", TINShapeMessage},
		{"Arabic-Indic digits after ASCII", "12345٦٧٨٩٠٠", TINShapeMessage},
		{"full-width digits after ASCII", "12345６７８９", TINShapeMessage},
	})
}

// TestValidateTIN_EdgeInputs: whitespace around the TIN is dropped before any
// class is decided; hyphens at either end and very long inputs land in the
// class the order of checks gives.
func TestValidateTIN_EdgeInputs(t *testing.T) {
	runReasonCases(t, []reasonCase{
		{"padded shape", "  BADTIN  ", TINShapeMessage},
		{"padded length", "\t12345\n", fmt.Sprintf(TINLengthMessage, 5)},
		{"padded checksum", " 1234567890 ", TINChecksumMessage},
		{"trailing hyphen on bare 12", "123456780006-", TINShapeMessage},
		{"trailing hyphen on 8-4", "12345678-0006-", TINShapeMessage},
		{"leading hyphen on 10", "-1234567897", TINShapeMessage},
		{"trailing hyphen on 10", "1234567897-", TINShapeMessage},
		{"hyphen at 9", "123456789-006", TINShapeMessage},
		{"hyphen at 11", "12345678000-6", TINShapeMessage},
		{"three hyphens", "1234-5678-00-06", TINShapeMessage},
		{"hyphen only", "-", fmt.Sprintf(TINLengthMessage, 0)},
		{"hyphens only", "---", fmt.Sprintf(TINLengthMessage, 0)},
		{"13 digits with a hyphen at 8", "12345678-00001", fmt.Sprintf(TINLengthMessage, 13)},
		{"very long digits", strings.Repeat("1", 10000), fmt.Sprintf(TINLengthMessage, 10000)},
		{"very long letters", strings.Repeat("A", 10000), TINShapeMessage},
		{"very long hyphens", strings.Repeat("-", 10000), fmt.Sprintf(TINLengthMessage, 0)},
	})
	for _, raw := range []string{" 12345678-0006 ", "\u00a01234567897\u00a0"} {
		if _, err := ValidateTIN(raw); err != nil {
			t.Errorf("ValidateTIN(%q) err = %v, want accepted", raw, err)
		}
	}
}

// oracleShape is the accepted-shape pattern ValidateTIN used before AUTH-10.
var oracleShape = regexp.MustCompile(`^(\d{10}|\d{12}|\d{8}-\d{4})$`)

// luhnCheckDigit mirrors freshTin's check digit (e2e/api/fixtures.ts).
func luhnCheckDigit(digits string) byte {
	sum, double := 0, true
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return byte('0' + (10-sum%10)%10)
}

// insertAt puts ins into s at byte index i.
func insertAt(s string, i int, ins string) string { return s[:i] + ins + s[i:] }

// acceptedSetCorpus is deterministic: Luhn-valid and Luhn-invalid strings of
// 9-13 digits, each with hyphen, plus and space variants.
func acceptedSetCorpus() []string {
	corpus := []string{
		"", "  ", "\t\n", "BADTIN", "12345", "123456789", "12345678901", "9876543210987",
		"1234567897", "12345678-0006", "1234567890", "12ab567897", "1234567-80006",
		"123456789-06", "12345678-00-06", "+123456780006", "-123456780006", "12345 67897",
		"581274639202", "0000000000", "\t\n 1234567897 \n\t", "123456780006",
		"12345678-0001", "123456-7890", "12345678-90", "12345678-000",
		"١٢٣٤٥٦٧٨٩٠", "１２３４５６７８９７", "\u00a01234567897\u00a0", " 12345678-0006 ",
	}
	prefixes := []string{"123456789012", "581274639202", "000000000000", "999999999999", "314159265358", "271828182845"}
	for n := 9; n <= 13; n++ {
		for _, pre := range prefixes {
			body := pre[:n-1]
			valid := body + string(luhnCheckDigit(body))
			invalid := body + string('0'+(valid[n-1]-'0'+1)%10)
			for _, base := range []string{valid, invalid} {
				corpus = append(corpus, base, "+"+base, insertAt(base, n/2, " "), "\t"+base+" \n")
				for i := 0; i <= len(base); i++ {
					corpus = append(corpus, insertAt(base, i, "-"))
				}
				if len(base) >= 10 {
					corpus = append(corpus, insertAt(insertAt(base, 8, "-"), 10, "-"))
				}
			}
		}
	}
	return corpus
}

// TestValidateTIN_AcceptedSetUnchanged: ValidateTIN accepts exactly what
// oracleShape + Luhn accepted before AUTH-10, every refusal gets the reason of
// its class, and the corpus reaches every accepted shape and every class.
func TestValidateTIN_AcceptedSetUnchanged(t *testing.T) {
	var jtb, firs12, firs84, empty, chars, length, hyphen, checksum int
	for _, raw := range acceptedSetCorpus() {
		trimmed := strings.TrimSpace(raw)
		canonical := strings.Replace(trimmed, "-", "", 1)
		want := oracleShape.MatchString(trimmed) && canonical[len(canonical)-1] == luhnCheckDigit(canonical[:len(canonical)-1])

		got, err := ValidateTIN(raw)
		if (err == nil) != want {
			t.Fatalf("ValidateTIN(%q) accepted = %v, want %v (err = %v)", raw, err == nil, want, err)
		}
		if want {
			if got != canonical {
				t.Errorf("ValidateTIN(%q) canonical = %q, want %q", raw, got, canonical)
			}
			switch {
			case len(trimmed) == 10:
				jtb++
			case strings.Contains(trimmed, "-"):
				firs84++
			default:
				firs12++
			}
			continue
		}
		if !errors.Is(err, ErrInvalidTIN) {
			t.Errorf("ValidateTIN(%q) err = %v, want errors.Is(err, ErrInvalidTIN)", raw, err)
		}

		digits := 0
		hyphens := 0
		other := false
		for _, r := range trimmed {
			switch {
			case r >= '0' && r <= '9':
				digits++
			case r == '-':
				hyphens++
			default:
				other = true
			}
		}
		var wantReason string
		switch {
		case trimmed == "":
			empty++
			wantReason = TINRequiredMessage
		case other:
			chars++
			wantReason = TINShapeMessage
		case digits != 10 && digits != 12:
			length++
			wantReason = fmt.Sprintf(TINLengthMessage, digits)
		case !oracleShape.MatchString(trimmed):
			hyphen++
			wantReason = TINShapeMessage
		default:
			checksum++
			wantReason = TINChecksumMessage
		}
		var te *TINError
		if !errors.As(err, &te) || te.Reason != wantReason {
			t.Errorf("ValidateTIN(%q) err = %v, want a *TINError with Reason %q", raw, err, wantReason)
		}
	}
	for name, n := range map[string]int{
		"accepted 10-digit": jtb, "accepted bare 12-digit": firs12, "accepted 8-4": firs84,
		"empty": empty, "bad characters": chars, "bad length": length, "bad hyphen": hyphen, "checksum": checksum,
	} {
		if n == 0 {
			t.Errorf("corpus holds no %s case", name)
		}
	}
}
