package db

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// GrantStaff inserts a public.staff_members row for userID over one connection to dsn.
func GrantStaff(ctx context.Context, dsn string, userID uuid.UUID) error {
	return errors.New("db: GrantStaff is not implemented")
}
