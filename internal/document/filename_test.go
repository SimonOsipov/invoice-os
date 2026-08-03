// BULK-01-01 (task-305): Mode A RED specs for SanitizeFilename (filename.go),
// written BEFORE the real implementation exists -- RED against filename.go's
// STUB (which returns raw unchanged, see that file's doc comment), so every
// assertion below fails on a value mismatch, never a compile error.
//
// Spec-to-test map (Test Specs table, task-305):
//
//	BULK-01-4 TestSanitizeFilename_StripsPathSegmentsBothSeparators
//	BULK-01-5 TestSanitizeFilename_DropsC0ControlsAndDEL (unit half; the
//	          DB-backed "still commits and persists the stripped name" half
//	          is TestStoreCreateBatch_NULCharacterInFilenameStrippedAndPersisted
//	          in store_test.go)
//	BULK-01-6 TestSanitizeFilename_InvalidUTF8CoercedToValid
//	BULK-01-7 TestSanitizeFilename_TruncatesTo255Runes
//
// No DB needed -- these are pure unit tests over SanitizeFilename.
package document_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/SimonOsipov/invoice-os/internal/document"
)

// TestSanitizeFilename_StripsPathSegmentsBothSeparators (BULK-01-4): a client
// that sends a path (either a unix-style relative traversal, or a legacy
// Windows-style absolute path in the multipart filename field) must have it
// reduced to the base name only -- never storing (or rendering back to
// another user) a path the client didn't intend as a bare filename.
func TestSanitizeFilename_StripsPathSegmentsBothSeparators(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"unix-style relative traversal", "../../etc/passwd.csv", "passwd.csv"},
		{"legacy windows-style absolute path", `C:\Users\bob\a.csv`, "a.csv"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := document.SanitizeFilename(tc.in); got != tc.want {
				t.Errorf("document.SanitizeFilename(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSanitizeFilename_DropsC0ControlsAndDEL (BULK-01-5, unit half): a NUL (or
// any other C0 control byte) in the declared filename must be stripped --
// Postgres rejects a NUL in a text column outright (22021), and the rest
// render as garbage in a browser. Paired with a positive control (a filename
// with no control bytes passes through unchanged) so this cannot vacuously
// pass by e.g. always returning "".
func TestSanitizeFilename_DropsC0ControlsAndDEL(t *testing.T) {
	// Positive control: an ordinary filename is untouched by this rule.
	if got := document.SanitizeFilename("plain.csv"); got != "plain.csv" {
		t.Fatalf(`document.SanitizeFilename("plain.csv") = %q, want "plain.csv" unchanged (positive control)`, got)
	}
	if got := document.SanitizeFilename("a\x00b.csv"); got != "ab.csv" {
		t.Errorf(`document.SanitizeFilename("a\x00b.csv") = %q, want %q (NUL stripped)`, got, "ab.csv")
	}
}

// TestSanitizeFilename_InvalidUTF8CoercedToValid (BULK-01-6): a filename
// containing an invalid UTF-8 byte sequence must be coerced to valid UTF-8,
// never stored/round-tripped as-is -- Postgres rejects invalid UTF-8 in a
// text column (22021) at insert time.
func TestSanitizeFilename_InvalidUTF8CoercedToValid(t *testing.T) {
	invalid := string([]byte{0xff, 0xfe}) + ".csv"
	if utf8.ValidString(invalid) {
		t.Fatal("fixture assumption broken: input is already valid UTF-8")
	}
	got := document.SanitizeFilename(invalid)
	if !utf8.ValidString(got) {
		t.Errorf("document.SanitizeFilename(<invalid UTF-8>) = %q, want valid UTF-8 (coerced, e.g. via strings.ToValidUTF8)", got)
	}
}

// TestSanitizeFilename_TruncatesTo255Runes (BULK-01-7): a name far exceeding
// the conventional filesystem limit must be truncated to exactly 255 RUNES,
// not bytes -- truncating by byte count could split a multi-byte rune and
// produce invalid UTF-8 or an off-by-one rune count for non-ASCII input.
func TestSanitizeFilename_TruncatesTo255Runes(t *testing.T) {
	long := strings.Repeat("a", 5000)
	got := document.SanitizeFilename(long)
	if n := len([]rune(got)); n != 255 {
		t.Errorf("len([]rune(document.SanitizeFilename(<5000-rune name>))) = %d, want 255", n)
	}
}

// --- QA Mode B additions (task-305): adversarial/edge coverage beyond the
// 11 Test Specs -- these hunt for siblings of the filepath.Base("") -> "."
// footgun the executor already guarded (step 1's doc comment), plus
// encoding/length adversarials the spec table didn't enumerate. -----------

// TestSanitizeFilename_DotAndDotDotSegmentsPassThroughUnchanged: "." and ".."
// are NOT the filepath.Base("") footgun (that only bites an EMPTY input,
// which step 1 special-cases) -- filepath.Base(".") and filepath.Base("..")
// return the string unchanged, and neither is a C0 control, invalid UTF-8, or
// over the rune cap, so both round-trip as themselves. Locked in explicitly
// so a future edit that "fixes" the empty-input special case broadly (e.g.
// by removing the `s != ""` guard) doesn't silently start mangling these too.
func TestSanitizeFilename_DotAndDotDotSegmentsPassThroughUnchanged(t *testing.T) {
	for _, in := range []string{".", ".."} {
		if got := document.SanitizeFilename(in); got != in {
			t.Errorf("document.SanitizeFilename(%q) = %q, want %q (unchanged)", in, got, in)
		}
	}
}

// TestSanitizeFilename_LoneAndTrailingSeparators: siblings of BULK-01-4's own
// path-stripping cases, at the boundary where the separator IS (or is nearly)
// the entire input.
//   - "/" -> "/": filepath.Base("/") returns "/" itself (Base's own
//     documented rule: "if the path consists entirely of slashes, Base
//     returns \"/\"") -- there is no backslash to cut on afterward, so this
//     is the one single-character input step 1 does NOT reduce to "".
//   - "\\" (a lone backslash) -> "": on this OS, filepath.Base does not treat
//     backslash as a separator (it is Unix-only in this build), so Base
//     returns "\\" unchanged; the SECOND cut (LastIndexByte on '\\') then
//     removes everything up to and including it, leaving "".
//   - "a/" -> "a", "a\\" -> "": the same two rules composed with one leading
//     real character.
func TestSanitizeFilename_LoneAndTrailingSeparators(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/", "/"},
		{"\\", ""},
		{"a/", "a"},
		{"a\\", ""},
	}
	for _, tc := range tests {
		if got := document.SanitizeFilename(tc.in); got != tc.want {
			t.Errorf("document.SanitizeFilename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSanitizeFilename_OnlyControlCharactersYieldsEmpty: a name that is
// NOTHING but C0 controls/DEL (no ordinary characters at all) must reduce to
// "" rather than, say, panicking or leaving a non-empty garbage remnant --
// the general case BULK-01-5's own `"a\x00b.csv"` doesn't exercise (that
// input has real characters surrounding the NUL).
func TestSanitizeFilename_OnlyControlCharactersYieldsEmpty(t *testing.T) {
	if got := document.SanitizeFilename("\x00\x01\x02\x1F\x7F"); got != "" {
		t.Errorf("document.SanitizeFilename(<all C0/DEL>) = %q, want \"\"", got)
	}
}

// TestSanitizeFilename_RTLOverridePassesThroughUncoveredByAC3: a Unicode
// bidi-override character (U+202E RIGHT-TO-LEFT OVERRIDE) is NOT a C0
// control/DEL, not invalid UTF-8, and not over the rune cap -- so AC #3, as
// specified, does not require it to be touched, and SanitizeFilename does not
// touch it. This is a DELIBERATE scope note, not a silent gap: a bidi
// override in a rendered filename can visually disguise an extension (e.g.
// "invoice<RLO>exe.csv" can render as if the extension were reversed) --
// worth flagging to the executor as an optional hardening follow-up, but
// out of scope for this AC, which only names C0 controls and DEL.
func TestSanitizeFilename_RTLOverridePassesThroughUncoveredByAC3(t *testing.T) {
	const in = "invoice\u202eexe.csv"
	if got := document.SanitizeFilename(in); got != in {
		t.Errorf("document.SanitizeFilename(%q) = %q, want %q unchanged (AC #3 scopes control-stripping to C0/DEL only, not bidi controls)", in, got, in)
	}
}

// TestSanitizeFilename_CombiningCharactersTruncationStaysValidUTF8: a name
// built from many two-rune grapheme clusters (a base letter + a combining
// acute accent, i.e. NFD "é") that exceeds 255 runes must still
// truncate to EXACTLY 255 runes and remain valid UTF-8 -- even though the
// rune-based cut can (and here does) land mid-grapheme, leaving a lone
// combining mark at the end. That is within spec (truncate to 255 RUNES, not
// grapheme clusters) -- this pins "never invalid UTF-8", not grapheme
// integrity.
func TestSanitizeFilename_CombiningCharactersTruncationStaysValidUTF8(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 130; i++ { // 130 * 2 runes = 260 runes, over the 255 cap
		b.WriteString("é")
	}
	long := b.String()
	if n := utf8.RuneCountInString(long); n != 260 {
		t.Fatalf("fixture assumption broken: input has %d runes, want 260", n)
	}

	got := document.SanitizeFilename(long)
	if !utf8.ValidString(got) {
		t.Fatalf("document.SanitizeFilename(<260-rune combining-char name>) = %q, not valid UTF-8", got)
	}
	if n := utf8.RuneCountInString(got); n != 255 {
		t.Errorf("utf8.RuneCountInString(document.SanitizeFilename(...)) = %d, want 255", n)
	}
}

// TestSanitizeFilename_EmojiAtRuneBoundaryNeverSplitsACodepoint: a name whose
// 255th rune is a single non-BMP emoji (U+1F600, one rune in Go regardless of
// its 4-byte UTF-8 encoding -- Go strings have no UTF-16 surrogate-pair
// concept to split) must keep that whole emoji and drop only what comes
// after it, never emit a truncated/invalid byte sequence.
func TestSanitizeFilename_EmojiAtRuneBoundaryNeverSplitsACodepoint(t *testing.T) {
	name := strings.Repeat("a", 254) + "\U0001F600" + "\U0001F601" // 256 runes total
	if n := utf8.RuneCountInString(name); n != 256 {
		t.Fatalf("fixture assumption broken: input has %d runes, want 256", n)
	}

	got := document.SanitizeFilename(name)
	if !utf8.ValidString(got) {
		t.Fatalf("document.SanitizeFilename(<emoji at boundary>) = %q, not valid UTF-8", got)
	}
	want := strings.Repeat("a", 254) + "\U0001F600"
	if got != want {
		t.Errorf("document.SanitizeFilename(<emoji at boundary>) = %q, want %q (keep the 255th rune whole, drop the 256th)", got, want)
	}
}
