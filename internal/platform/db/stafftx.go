package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// ErrNotStaff is returned by WithinStaffTx when the context holds no caller admitted by auth.RequireRulesRole.
var ErrNotStaff = errors.New("db: not a checked staff caller")

// WithinStaffTx runs fn in a transaction with no tenant setting, for global staff data.
// It refuses with ErrNotStaff before any statement unless auth.StaffFromContext reports a caller.
func WithinStaffTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	if _, ok := auth.StaffFromContext(ctx); !ok {
		return ErrNotStaff
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("db: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit tx: %w", err)
	}
	return nil
}
