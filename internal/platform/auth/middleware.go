package auth

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// Middleware verifies the bearer token on each request, injects the resulting
// Identity into the context, and rejects anything that fails with a 401. It
// never answers 500 for an auth failure and never tells the client why a token
// was rejected — the reason is logged for operators only.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			v.reject(w, r, "missing or malformed Authorization header")
			return
		}
		id, err := v.Verify(r.Context(), token)
		if err != nil {
			v.reject(w, r, err.Error())
			return
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
	})
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header.
func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(h[len(prefix):])
	return token, token != ""
}

func (v *Verifier) reject(w http.ResponseWriter, r *http.Request, reason string) {
	v.log.WarnContext(r.Context(), "auth rejected", slog.String("reason", reason))
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
}
