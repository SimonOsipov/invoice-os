package gateway

import (
	"log/slog"
	"net/http"
)

// refusalPageWriter turns a non-200 upstream answer into the export refusal page.
type refusalPageWriter struct {
	http.ResponseWriter
}

// ExportCodeHandler answers POST /auth/export-code.
func ExportCodeHandler(store Handoffs, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not implemented", http.StatusNotImplemented)
	})
}

// ExportDownloadHandler answers POST /auth/export-download.
func ExportDownloadHandler(store Handoffs, api http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not implemented", http.StatusNotImplemented)
	})
}
