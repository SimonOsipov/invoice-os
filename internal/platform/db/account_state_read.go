package db

import (
	"context"
	"errors"
)

func GrantAccountStateRead(ctx context.Context, authAdminDSN string) (bool, error) {
	return false, errors.New("not implemented")
}
