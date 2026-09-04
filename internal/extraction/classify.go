// classify.go: the document types POST /v1/documents accepts, and the classifier that
// resolves one. Spreadsheets are refused here — they have their own route.
package extraction

import (
	"mime"
	"path/filepath"
	"strings"
)

// acceptedDocumentTypes maps a lowercased filename extension to the canonical content type
// recorded on the documents row. A package-level literal on purpose: EXTR-09-04's CLASSIFY-5
// reads this table and compares it to the picker's TypeScript one, so an inline switch would
// leave the two free to drift.
var acceptedDocumentTypes = map[string]string{
	".pdf":  "application/pdf",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
}

// pageImageFormats says, per canonical content type, whether extraction can render page images
// from it. Exhaustive over acceptedDocumentTypes' values on purpose, so a newly accepted format
// cannot silently default to the no-render branch
// (TestRendersPageImages_TableIsExhaustiveOverAcceptedTypes).
var pageImageFormats = map[string]bool{
	"application/pdf": true,
	"image/png":       false,
	"image/jpeg":      false,
	"image/webp":      false,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": false,
}

// RendersPageImages reports whether documents.declared_content_type names a format extraction
// renders page images from. A strict allowlist: that column is nullable, so an unknown or absent
// type takes the no-render branch.
func RendersPageImages(contentType string) bool { return pageImageFormats[contentType] }

// classifyDocumentType resolves the canonical content type from the filename extension
// first, then from the declared content type with its parameters stripped, mirroring
// detectFormat (internal/importer/handlers.go:142-162). "" means refuse.
func classifyDocumentType(filename, contentType string) string {
	if ct, ok := acceptedDocumentTypes[strings.ToLower(filepath.Ext(filename))]; ok {
		return ct
	}

	// mime.ParseMediaType strips any "; charset=..." parameters a client might send.
	base := contentType
	if parsed, _, err := mime.ParseMediaType(contentType); err == nil {
		base = parsed
	}
	// The table is keyed by extension, so the declared type is matched against its VALUES:
	// the canonical spelling is the only one accepted, and it is the value recorded.
	for _, ct := range acceptedDocumentTypes {
		if base == ct {
			return ct
		}
	}
	return ""
}
