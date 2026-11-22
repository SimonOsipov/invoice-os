package erp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"

	"github.com/google/uuid"
)

const stateTailLen = 43 // base64url of 32 bytes, unpadded

// NewState returns "<tenant uuid>.<base64url of 32 random bytes>" and the sha256 Postgres stores.
func NewState(tenantID string) (state string, hash []byte) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	state = tenantID + "." + base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(state))
	return state, h[:]
}

// ParseState checks only the shape; the hash lookup under the tenant decides validity.
func ParseState(s string) (tenantID string, hash []byte, ok bool) {
	tenantID, tail, found := strings.Cut(s, ".")
	if !found || len(tenantID) != 36 || tenantID != strings.ToLower(tenantID) || len(tail) != stateTailLen {
		return "", nil, false
	}
	if _, err := uuid.Parse(tenantID); err != nil {
		return "", nil, false
	}
	if raw, err := base64.RawURLEncoding.Strict().DecodeString(tail); err != nil || len(raw) != 32 {
		return "", nil, false
	}
	h := sha256.Sum256([]byte(s))
	return tenantID, h[:], true
}
