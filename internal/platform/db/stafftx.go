package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// ErrNotStaff is returned by WithinStaffTx when the context holds no caller admitted by auth.RequireStaff.
var ErrNotStaff = errors.New("db: not a checked staff caller")

// WithinStaffTx runs fn in a transaction with no tenant setting, for global staff data and staff_read_ functions.
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

// StaffActor returns the checked staff caller's subject as the UUID that staff_read_ functions take.
func StaffActor(ctx context.Context) (uuid.UUID, error) {
	id, ok := auth.StaffFromContext(ctx)
	if !ok {
		return uuid.Nil, ErrNotStaff
	}
	actor, err := uuid.Parse(id.Subject)
	if err != nil {
		return uuid.Nil, ErrNotStaff
	}
	return actor, nil
}

// StaffReadError maps only the function's own refusal to ErrNotStaff; a lost grant is also 42501 and stays a server fault.
func StaffReadError(op string, err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42501" && strings.HasSuffix(pgErr.Message, ": actor is not staff") {
		return ErrNotStaff
	}
	return fmt.Errorf("%s: %w", op, err)
}
