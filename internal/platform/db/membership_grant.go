package db

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// MemberGrant is one membership row to write.
type MemberGrant struct {
	TenantID    uuid.UUID
	UserID      uuid.UUID
	Role        string
	DisplayName string
	Email       string
}

// GrantMembership is a signature-only stub (AUTH-15-02 Mode A).
func GrantMembership(ctx context.Context, dsn string, g MemberGrant) error {
	return errors.New("not implemented")
}
