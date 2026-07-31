package importer

// sanitizeFilename coerces a multipart part's declared filename into something
// safe to store in a text column and render in a browser. Never errors: an
// unusable name yields "" and the store writes SQL NULL.
//  1. strip any path segment the client sent: filepath.Base, then cut at the
//     last '\' (filepath.Base does not split on backslash on unix; legacy
//     browsers have sent "C:\Users\x\a.csv" verbatim).
//  2. drop C0 controls and DEL (0x00-0x1F, 0x7F): a NUL cannot be stored in a
//     Postgres text column at all (22021), and the rest render as garbage.
//  3. strings.ToValidUTF8(s, ""): an invalid byte sequence is 22021 on insert.
//  4. truncate to 255 RUNES (not bytes) -- the conventional filesystem limit.
//  5. strings.TrimSpace; an empty result is "".
//
// STUB (BULK-01-01, test-first): deliberate non-implementation -- returns raw
// unchanged, so every BULK-01-4..8 sanitisation spec fails on a value
// mismatch rather than a compile error. Real implementation must perform the
// five steps above, in order.
func sanitizeFilename(raw string) string {
	return raw
}
