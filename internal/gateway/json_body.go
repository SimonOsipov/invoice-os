package gateway

import (
	"mime"
	"net/http"
)

const msgJSONOnly = "Content-Type must be application/json"

// jsonOnly answers 415 unless the body is declared as JSON; a CORS-safelisted type needs no preflight.
func jsonOnly(w http.ResponseWriter, r *http.Request) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && mt == "application/json" {
		return true
	}
	writeError(w, http.StatusUnsupportedMediaType, msgJSONOnly)
	return false
}
