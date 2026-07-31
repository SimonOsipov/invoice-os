// BULK-01-01 (task-305): Mode A RED specs for sanitizeFilename (filename.go),
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
// No DB needed -- these are pure unit tests over sanitizeFilename.
package importer

import (
	"strings"
	"testing"
	"unicode/utf8"
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
			if got := sanitizeFilename(tc.in); got != tc.want {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tc.in, got, tc.want)
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
	if got := sanitizeFilename("plain.csv"); got != "plain.csv" {
		t.Fatalf(`sanitizeFilename("plain.csv") = %q, want "plain.csv" unchanged (positive control)`, got)
	}
	if got := sanitizeFilename("a\x00b.csv"); got != "ab.csv" {
		t.Errorf(`sanitizeFilename("a\x00b.csv") = %q, want %q (NUL stripped)`, got, "ab.csv")
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
	got := sanitizeFilename(invalid)
	if !utf8.ValidString(got) {
		t.Errorf("sanitizeFilename(<invalid UTF-8>) = %q, want valid UTF-8 (coerced, e.g. via strings.ToValidUTF8)", got)
	}
}

// TestSanitizeFilename_TruncatesTo255Runes (BULK-01-7): a name far exceeding
// the conventional filesystem limit must be truncated to exactly 255 RUNES,
// not bytes -- truncating by byte count could split a multi-byte rune and
// produce invalid UTF-8 or an off-by-one rune count for non-ASCII input.
func TestSanitizeFilename_TruncatesTo255Runes(t *testing.T) {
	long := strings.Repeat("a", 5000)
	got := sanitizeFilename(long)
	if n := len([]rune(got)); n != 255 {
		t.Errorf("len([]rune(sanitizeFilename(<5000-rune name>))) = %d, want 255", n)
	}
}
