package erp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"

	"github.com/SimonOsipov/invoice-os/internal/erp/erpweb"
)

// NewState returns "<tenant uuid>.<base64url of 32 random bytes>" and the sha256 Postgres stores.
func NewState(tenantID string) (state string, hash []byte) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	state = tenantID + "." + base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(state))
	return state, h[:]
}

// ParseState checks only the shape; the hash lookup under the tenant decides validity.
func ParseState(s string) (tenantID string, hash []byte, ok bool) { return erpweb.ParseState(s) }
