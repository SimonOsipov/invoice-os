// filename_truncation_test.go: step 4's extension-preserving truncation.
// SanitizeFilename's 255-rune cut now removes stem runes and keeps the
// extension (bounded at 32 runes) because the stored name is the only carrier
// of format on the import path. Every case here is >255 runes: the three
// shipped truncation specs in filename_test.go all use ext-less inputs and so
// exercise only the fallback branch.
package document_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/SimonOsipov/invoice-os/internal/document"
)

// TestSanitizeFilename_TruncationKeepsTheExtension: the whole point of the
// branch. Each input is over the cap; each output is exactly 255 runes and
// still ends in the extension the client sent.
func TestSanitizeFilename_TruncationKeepsTheExtension(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			// The shipped case: a 304-rune name whose .csv is the only format signal.
			"long stem, short extension",
			strings.Repeat("a", 300) + ".csv",
			strings.Repeat("a", 251) + ".csv",
		},
		{
			// Long but still within maxKeptExt.
			"long stem, 19-rune extension",
			strings.Repeat("a", 300) + ".spreadsheet-backup",
			strings.Repeat("a", 236) + ".spreadsheet-backup",
		},
		{
			// filepath.Ext takes the LAST dot; the earlier one is stem and is cut
			// like any other stem rune.
			"multiple dots: the last one wins",
			strings.Repeat("a", 100) + ".tar" + strings.Repeat("b", 200) + ".gz",
			strings.Repeat("a", 100) + ".tar" + strings.Repeat("b", 148) + ".gz",
		},
		{
			// A bare trailing dot is a 1-rune extension. Harmless, but it must not
			// fall into the 255-rune fallback and shift the cut by one.
			"trailing dot",
			strings.Repeat("a", 300) + ".",
			strings.Repeat("a", 254) + ".",
		},
		{
			// No dot at all: filepath.Ext is "", so the fallback cuts a plain prefix.
			"no extension",
			strings.Repeat("a", 300),
			strings.Repeat("a", 255),
		},
		{
			// A dotfile's whole name is its extension (filepath.Ext scans back to the
			// FIRST dot when it is at index 0). 301 runes is well over maxKeptExt, so
			// this takes the fallback and keeps no suffix.
			"all extension",
			"." + strings.Repeat("a", 300),
			"." + strings.Repeat("a", 254),
		},
		{
			// One rune over the cap: the minimal input that reaches the branch.
			"256 runes, one over the cap",
			strings.Repeat("a", 252) + ".csv",
			strings.Repeat("a", 251) + ".csv",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if n := utf8.RuneCountInString(tc.in); n <= 255 {
				t.Fatalf("fixture assumption broken: input is %d runes, want >255 so the truncation branch runs", n)
			}
			got := document.SanitizeFilename(tc.in)
			if got != tc.want {
				t.Errorf("document.SanitizeFilename(%d-rune name) = %q, want %q",
					utf8.RuneCountInString(tc.in), abbrev(got), abbrev(tc.want))
			}
			if n := utf8.RuneCountInString(got); n != 255 {
				t.Errorf("output is %d runes, want exactly 255", n)
			}
		})
	}
}

// TestSanitizeFilename_TruncationDropsAnOversizedExtension: maxKeptExt is what
// stops a hostile 300-rune "extension" riding through the cap intact. Pinned
// behaviourally at the boundary — a 32-rune extension survives, a 33-rune one
// does not — so a silent bump to the constant fails here.
func TestSanitizeFilename_TruncationDropsAnOversizedExtension(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			"32-rune extension is kept",
			strings.Repeat("a", 300) + "." + strings.Repeat("e", 31),
			strings.Repeat("a", 223) + "." + strings.Repeat("e", 31),
		},
		{
			"33-rune extension is dropped",
			strings.Repeat("a", 300) + "." + strings.Repeat("e", 32),
			strings.Repeat("a", 255),
		},
		{
			"a 300-rune hostile extension cannot ride through",
			strings.Repeat("a", 100) + "." + strings.Repeat("e", 300),
			strings.Repeat("a", 100) + "." + strings.Repeat("e", 154),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := document.SanitizeFilename(tc.in)
			if got != tc.want {
				t.Errorf("document.SanitizeFilename(%d-rune name) = %q, want %q",
					utf8.RuneCountInString(tc.in), abbrev(got), abbrev(tc.want))
			}
			if n := utf8.RuneCountInString(got); n != 255 {
				t.Errorf("output is %d runes, want exactly 255", n)
			}
		})
	}
}

// TestSanitizeFilename_TruncationWithMultiByteExtensionCountsRunes: the cap is
// 255 RUNES and the kept extension is measured in runes too. A byte-counted
// implementation of either half lands somewhere other than 255 here, and a
// byte-sliced one splits a codepoint.
func TestSanitizeFilename_TruncationWithMultiByteExtensionCountsRunes(t *testing.T) {
	const ext = ".日本語" // 4 runes, 10 bytes
	if utf8.RuneCountInString(ext) != 4 || len(ext) != 10 {
		t.Fatalf("fixture assumption broken: ext is %d runes / %d bytes, want 4 / 10", utf8.RuneCountInString(ext), len(ext))
	}

	got := document.SanitizeFilename(strings.Repeat("a", 300) + ext)

	if want := strings.Repeat("a", 251) + ext; got != want {
		t.Errorf("document.SanitizeFilename(<300 runes + %q>) = %q, want %q", ext, abbrev(got), abbrev(want))
	}
	if n := utf8.RuneCountInString(got); n != 255 {
		t.Errorf("output is %d runes, want 255", n)
	}
	if len(got) != 261 {
		t.Errorf("output is %d bytes, want 261 — 255 runes of which the last 4 are multi-byte", len(got))
	}
	if !utf8.ValidString(got) {
		t.Errorf("output %q is not valid UTF-8 — the cut split a codepoint", abbrev(got))
	}
}

// TestSanitizeFilename_ExactlyAtTheCapIsUntouched: the positive control for
// every case above. 255 runes is NOT over the cap, so the branch must not run
// at all and the name passes through byte-identical — otherwise the specs above
// could be satisfied by a function that rewrites every name it sees.
func TestSanitizeFilename_ExactlyAtTheCapIsUntouched(t *testing.T) {
	in := strings.Repeat("a", 251) + ".csv"
	if n := utf8.RuneCountInString(in); n != 255 {
		t.Fatalf("fixture assumption broken: input is %d runes, want exactly 255", n)
	}
	if got := document.SanitizeFilename(in); got != in {
		t.Errorf("document.SanitizeFilename(<exactly 255 runes>) = %q, want it unchanged", abbrev(got))
	}
}

// TestSanitizeFilename_TruncationIntroducesNothing: truncation is a
// prefix+suffix of an already-coerced string, so it cannot put back anything
// steps 1-3 removed. Asserted as a rune-set containment (the output's runes are
// a subset of the input's) plus idempotence — either would fail if the branch
// ever synthesised a character, and together they cover the path separators,
// control bytes and quotes the download handler's Content-Disposition depends
// on being handled.
func TestSanitizeFilename_TruncationIntroducesNothing(t *testing.T) {
	hostile := map[string]string{
		"unix traversal + long name":  "../../etc/" + strings.Repeat("a", 300) + ".csv",
		"windows path + long name":    `C:\Users\bob\` + strings.Repeat("a", 300) + ".csv",
		"NUL inside the extension":    strings.Repeat("a", 300) + ".c\x00sv",
		"DEL inside the stem":         strings.Repeat("a", 150) + "\x7f" + strings.Repeat("a", 150) + ".csv",
		"quotes in stem and ext":      strings.Repeat(`a"`, 150) + `.c"sv`,
		"semicolon parameter break":   strings.Repeat("a", 300) + `;b="c".csv`,
		"invalid utf-8 in the stem":   strings.Repeat("a", 300) + string([]byte{0xff, 0xfe}) + ".csv",
		"bidi override before an ext": strings.Repeat("a", 300) + "\u202eexe.csv",
		"combining marks":             strings.Repeat("é", 200) + ".csv", // NFD: 2 runes each
		"emoji stem":                  strings.Repeat("\U0001F600", 300) + ".csv",
	}

	for label, in := range hostile {
		t.Run(label, func(t *testing.T) {
			if n := utf8.RuneCountInString(in); n <= 255 {
				t.Fatalf("fixture assumption broken: input is %d runes, want >255", n)
			}
			got := document.SanitizeFilename(in)

			// Positive control first: an empty output would satisfy every
			// assertion below vacuously.
			if got == "" {
				t.Fatal("output is empty — the assertions below would pass vacuously")
			}
			if n := utf8.RuneCountInString(got); n > 255 {
				t.Errorf("output is %d runes, want <= 255", n)
			}
			if !utf8.ValidString(got) {
				t.Errorf("output %q is not valid UTF-8", abbrev(got))
			}
			if i := strings.IndexAny(got, `/\`); i != -1 {
				t.Errorf("output %q carries a path separator at %d — truncation must never put one back", abbrev(got), i)
			}
			for i, r := range got {
				if r <= 0x1F || r == 0x7F {
					t.Errorf("output %q carries a control byte %#x at %d", abbrev(got), r, i)
				}
			}

			// Nothing synthesised: every rune of the result already appeared in
			// the input.
			seen := map[rune]bool{}
			for _, r := range in {
				seen[r] = true
			}
			for _, r := range got {
				if !seen[r] {
					t.Errorf("output carries rune %q, which is not in the input — truncation must not synthesise characters", r)
					break
				}
			}

			// Idempotent: a second pass has nothing left to coerce, so the
			// truncated value is a fixed point of the whole pipeline.
			if again := document.SanitizeFilename(got); again != got {
				t.Errorf("SanitizeFilename is not idempotent: %q -> %q", abbrev(got), abbrev(again))
			}
		})
	}
}

// abbrev keeps a 255-rune failure message readable.
func abbrev(s string) string {
	r := []rune(s)
	if len(r) <= 40 {
		return s
	}
	return string(r[:16]) + "…(" + string(rune('0'+len(r)/100%10)) + string(rune('0'+len(r)/10%10)) + string(rune('0'+len(r)%10)) + " runes)…" + string(r[len(r)-16:])
}
