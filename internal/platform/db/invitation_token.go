package db

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// SetInvitationToken is a stub: the implementation replaces its body.
func SetInvitationToken(ctx context.Context, dsn string, tenantID, invitationID uuid.UUID, token string) (bool, error) {
	return false, errors.New("db: set invitation token: not implemented")
}
