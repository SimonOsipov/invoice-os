package gateway

import (
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// ConsoleHandoffHandler answers POST /auth/console-handoff with a code whose answer is the caller's own access token.
// It runs behind the verifier, the session check and auth.RequireStaff; it reads no staff claim itself.
func ConsoleHandoffHandler(store *HandoffStore, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !postOnly(w, r) {
			return
		}
		var in struct {
			State string `json:"state"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExchangeBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if !stateShape.MatchString(in.State) {
			writeError(w, http.StatusBadRequest, "state is required")
			return
		}
		bearer, _ := auth.BearerToken(r) // the verifier already admitted it
		answer, _ := json.Marshal(map[string]string{"access_token": bearer})
		code, ok := store.Put(string(answer), sha256.Sum256([]byte(in.State)))
		if !ok {
			log.WarnContext(r.Context(), "console hand-off: code store is full")
			writeError(w, http.StatusServiceUnavailable, "console hand-off is unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"code": code})
	})
}
