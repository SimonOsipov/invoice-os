package gateway

import (
	"log/slog"
	"net/http"
)

// ConsoleHandoffHandler answers POST /auth/console-handoff with a code whose answer is the caller's own access token.
// Stub: answers 501 until SUPP-11-01 is implemented.
func ConsoleHandoffHandler(store *HandoffStore, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	})
}
