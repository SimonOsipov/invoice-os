// Package filesig answers whether a file's leading bytes match its declared format.
package filesig

import "bytes"

// HeadLen is the longest signature; callers read at most this many bytes.
const HeadLen = 5

var (
	pdfSig = []byte("%PDF-")
	zipSig = []byte("PK\x03\x04")
)

// Matches reports whether b starts with format's signature: "pdf" needs "%PDF-",
// "docx" and "xlsx" need "PK\x03\x04", "csv" always matches, any other format never does.
func Matches(format string, b []byte) bool {
	switch format {
	case "pdf":
		return bytes.HasPrefix(b, pdfSig)
	case "docx", "xlsx":
		return bytes.HasPrefix(b, zipSig)
	case "csv":
		return true
	}
	return false
}
