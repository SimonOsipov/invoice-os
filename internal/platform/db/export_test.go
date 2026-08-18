package db

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Test-only handles on the purge's unexported surface, for the db_test suite.
// PurgeTablesForTest shares purgeTables' backing array, so an in-place reorder
// in a test is the order purgeWithin then executes.
var (
	PurgeTablesForTest         = purgeTables
	PurgeExcludedTablesForTest = purgeExcludedTables
	PurgeStmtForTest           = purgeStmt
)

func PurgeWithinForTest(ctx context.Context, tx pgx.Tx, tenants []string) (PurgeResult, error) {
	return purgeWithin(ctx, tx, tenants)
}
