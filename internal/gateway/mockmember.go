package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// MemberGrant is the parsed body of POST /auth/mock/member.
type MemberGrant struct {
	TenantID    uuid.UUID
	UserID      uuid.UUID
	Role        string
	DisplayName string
	Email       string
}

const maxMemberDisplayName = 200

// parseHyphenatedUUID accepts only the 36-character hyphenated form, either case.
func parseHyphenatedUUID(s string) (uuid.UUID, bool) {
	id, err := uuid.Parse(s)
	return id, err == nil && id.String() == strings.ToLower(s)
}

// MockMemberHandler answers POST /auth/mock/member by calling grant with the parsed body.
//
//go:noinline
func MockMemberHandler(grant func(ctx context.Context, g MemberGrant) error, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !postOnly(w, r) {
			return
		}
		var in struct {
			UserID      string `json:"user_id"`
			TenantID    string `json:"tenant_id"`
			Role        string `json:"role"`
			DisplayName string `json:"display_name"`
			Email       string `json:"email"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExchangeBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		userID, okUser := parseHyphenatedUUID(in.UserID)
		tenantID, okTenant := parseHyphenatedUUID(in.TenantID)
		nameLen := utf8.RuneCountInString(in.DisplayName)
		validRole := in.Role == "admin" || in.Role == "preparer" || in.Role == "reviewer"
		if !okUser || !okTenant || !validRole || nameLen < 1 || nameLen > maxMemberDisplayName || in.Email == "" {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		g := MemberGrant{TenantID: tenantID, UserID: userID, Role: in.Role, DisplayName: in.DisplayName, Email: in.Email}
		if err := grant(r.Context(), g); err != nil {
			log.WarnContext(r.Context(), "mock member: grant failed", slog.String("error", err.Error()))
			writeError(w, http.StatusBadGateway, "membership grant unavailable")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
