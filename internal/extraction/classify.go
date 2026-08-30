// classify.go: the document types POST /v1/documents accepts, and the classifier that
// resolves one. Spreadsheets are refused here — they have their own route.
package extraction

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

// classifyDocumentType resolves the canonical content type from the filename extension
// first, then from the declared content type with its parameters stripped, mirroring
// detectFormat (internal/importer/handlers.go:142-162). "" means refuse.
//
// Stage-2.5 stub: refuses everything. Stage 3 owns the real resolution.
func classifyDocumentType(filename, contentType string) string {
	_, _ = filename, contentType
	return ""
}
