package auth

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

// gotrueClaims is the subset of the GoTrue JWT claim set this system binds. The
// full contract (every claim GoTrue emits) is pinned by the golden-token
// fixture; here we model only what we verify and consume. It doubles as the
// mint payload for the mock issuer, so its JSON shape must match real GoTrue —
// in particular "aud" is a single string and "exp"/"iat" are numeric seconds.
type gotrueClaims struct {
	Issuer      string      `json:"iss"`
	Subject     string      `json:"sub"`
	Audience    audience    `json:"aud"`
	ExpiresAt   int64       `json:"exp"`
	IssuedAt    int64       `json:"iat,omitempty"`
	Role        string      `json:"role"`
	AppMetadata appMetadata `json:"app_metadata"`
	Email       string      `json:"email,omitempty"`
	SessionID   string      `json:"session_id,omitempty"`
}

// appMetadata is GoTrue's app_metadata object. Only tenant_id, staff and
// rules_role are bound; other keys (provider, providers, ...) are ignored.
type appMetadata struct {
	TenantID  string `json:"tenant_id"`
	Staff     bool   `json:"staff,omitempty"`
	RulesRole bool   `json:"rules_role,omitempty"`
}

// UnmarshalJSON matches the three keys exactly. encoding/json folds case, which
// would let a planted "Staff" or "Tenant_ID" count. A non-boolean staff or
// rules_role, or a non-string tenant_id, is an error; null reads as unset.
func (a *appMetadata) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	var out appMetadata
	if raw, ok := m["tenant_id"]; ok {
		if err := json.Unmarshal(raw, &out.TenantID); err != nil {
			return fmt.Errorf("auth: invalid tenant_id claim: %w", err)
		}
	}
	if raw, ok := m["staff"]; ok {
		if err := json.Unmarshal(raw, &out.Staff); err != nil {
			return fmt.Errorf("auth: invalid staff claim: %w", err)
		}
	}
	if raw, ok := m["rules_role"]; ok {
		if err := json.Unmarshal(raw, &out.RulesRole); err != nil {
			return fmt.Errorf("auth: invalid rules_role claim: %w", err)
		}
	}
	*a = out
	return nil
}

// audience models GoTrue's "aud", which is a single string ("authenticated").
// It marshals as a plain string (matching GoTrue) and, on verify, also tolerates
// the JSON-array form other issuers use.
type audience string

func (a *audience) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*a = audience(s)
		return nil
	}
	var arr []string
	if err := json.Unmarshal(b, &arr); err == nil && len(arr) > 0 {
		*a = audience(arr[0])
		return nil
	}
	return fmt.Errorf("auth: invalid aud claim")
}

// Valid satisfies jwt.Claims. Signature verification and issuer/audience/subject
// checks live in the Verifier; here we enforce only that the token carries an
// expiry and has not passed it, so ParseWithClaims rejects expired tokens.
func (c gotrueClaims) Valid() error {
	if c.ExpiresAt == 0 {
		return jwt.NewValidationError("token missing exp", jwt.ValidationErrorExpired)
	}
	if time.Now().After(time.Unix(c.ExpiresAt, 0)) {
		return jwt.NewValidationError("token is expired", jwt.ValidationErrorExpired)
	}
	return nil
}
