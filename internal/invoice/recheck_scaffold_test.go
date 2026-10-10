package invoice

// Mode A scaffold for ENGI-04-06: the Design's signatures as not-implemented stubs so
// recheck_test.go compiles and fails on its assertions. The executor deletes this file when
// revalidate.go defines DueVersion, DueRechecks, RecheckCovered and MarkRechecked.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var errRecheckNotImplemented = errors.New("ENGI-04-06: not implemented")

type DueVersion struct {
	ID            string
	Version       int
	EffectiveFrom time.Time
}

func DueRechecks(ctx context.Context, pool *pgxpool.Pool, today time.Time) ([]DueVersion, error) {
	return nil, errRecheckNotImplemented
}

func RecheckCovered(ctx context.Context, pool *pgxpool.Pool, store *Store, gate *Gate, tenantID string, v DueVersion, today time.Time) (RevalidateResult, error) {
	return RevalidateResult{}, errRecheckNotImplemented
}

func MarkRechecked(ctx context.Context, pool *pgxpool.Pool, versionID string) error {
	return errRecheckNotImplemented
}
