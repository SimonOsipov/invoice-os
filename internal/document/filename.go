package document

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// maxKeptExt is the longest extension step 4's truncation preserves.
const maxKeptExt = 32

// SanitizeFilename coerces a multipart part's declared filename into something
// safe to store in a text column and render in a browser. Never errors: an
// unusable name yields "" and the store writes SQL NULL.
//
// It lives here rather than in internal/importer because both callers now need
// it and two copies of a security coercion drift apart.
//  1. strip any path segment the client sent: filepath.Base, then cut at the
//     last '\' (filepath.Base does not split on backslash on unix; legacy
//     browsers have sent "C:\Users\x\a.csv" verbatim).
//  2. drop C0 controls and DEL (0x00-0x1F, 0x7F): a NUL cannot be stored in a
//     Postgres text column at all (22021), and the rest render as garbage.
//  3. strings.ToValidUTF8(s, ""): an invalid byte sequence is 22021 on insert.
//  4. truncate to 255 RUNES (not bytes) -- the conventional filesystem limit --
//     keeping the extension: the stored name is the only thing the import path
//     can resolve a document's format from
//     (TestPreviewThenImport_LongFilenameResolvesSameFormat).
//  5. strings.TrimSpace; an empty result is "".
func SanitizeFilename(raw string) string {
	// 1. strip any path segment the client sent. filepath.Base("") is "." (a
	// Go footgun, not a real filename), so an empty input is left alone here
	// and falls out "" naturally via the trim in step 5.
	s := raw
	if s != "" {
		s = filepath.Base(s)
		if idx := strings.LastIndexByte(s, '\\'); idx != -1 {
			s = s[idx+1:]
		}
	}

	// 2. drop C0 controls and DEL, byte-by-byte -- an invalid UTF-8 byte
	// sequence passes through untouched here for step 3 to coerce.
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= 0x1F || c == 0x7F {
			continue
		}
		b.WriteByte(c)
	}
	s = b.String()

	// 3. coerce any invalid UTF-8 byte sequence.
	s = strings.ToValidUTF8(s, "")

	// 4. truncate to 255 RUNES (not bytes), cutting the stem rather than the
	// extension. maxKeptExt bounds what counts as one, so a hostile 300-rune
	// "extension" cannot ride through the cap intact.
	if n := utf8.RuneCountInString(s); n > 255 {
		r := []rune(s)
		ext := []rune(filepath.Ext(s))
		if len(ext) > 0 && len(ext) <= maxKeptExt {
			s = string(r[:255-len(ext)]) + string(ext)
		} else {
			s = string(r[:255])
		}
	}

	// 5. trim; an empty (or whitespace-only) result is "".
	return strings.TrimSpace(s)
}
