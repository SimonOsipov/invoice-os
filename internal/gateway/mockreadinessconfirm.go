package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// MockReadinessConfirmTokenHandler answers POST /auth/mock/readiness-confirm-token by calling set with the parsed body.
// set reports whether the address had a live held check.
//
//go:noinline
func MockReadinessConfirmTokenHandler(set func(ctx context.Context, email, token string) (bool, error), log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !postOnly(w, r) {
			return
		}
		var in struct {
			Email string `json:"email"`
			Token string `json:"token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExchangeBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if strings.TrimSpace(in.Email) == "" || !blogConfirmTokenRE.MatchString(in.Token) {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		found, err := set(r.Context(), in.Email, in.Token)
		if err != nil {
			log.WarnContext(r.Context(), "mock readiness confirm token: set failed", slog.String("error", err.Error()))
			writeError(w, http.StatusBadGateway, "readiness confirm token unavailable")
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "held check not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
