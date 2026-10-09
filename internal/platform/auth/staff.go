package auth

import (
	"context"
	"encoding/json"
	"net/http"
)

// RequireRulesRole admits only a caller with Staff and RulesRole, tenant or not: 401 with no caller, 403 otherwise.
// It stores the checked caller where only StaffFromContext reads it.
func RequireRulesRole(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := IdentityFromContext(r.Context())
		if !ok {
			id, ok = TenantlessCallerFromContext(r.Context())
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !id.Staff || !id.RulesRole {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyStaff, id)))
	})
}

// StaffFromContext returns the caller RequireRulesRole admitted; none anywhere else.
func StaffFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKeyStaff).(Identity)
	return id, ok
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
