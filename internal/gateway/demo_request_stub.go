package gateway

import (
	"log/slog"
	"net/http"
)

// DemoRequestHandler is a compile-only stub for AUTH-17-06; move the real handler to contacts.go and delete this file.
func DemoRequestHandler(sink ContactSink, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	})
}
