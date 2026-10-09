package db

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
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

// LockProvisionTailForTest isolates the tail lock from Bootstrap's, which
// Provision takes first on the same key.
var LockProvisionTailForTest = lockProvisionTail

// LogPurgeResultForTest exposes the log shape without exporting it: the line is
// a contract with operators and with dev-env.yml, not with any caller in Go.
var LogPurgeResultForTest = logPurgeResult

// StaffCtxForTest returns the context a handler sees behind RequireRulesRole for id.
func StaffCtxForTest(t *testing.T, id auth.Identity) context.Context {
	t.Helper()
	var ctx context.Context
	h := auth.RequireRulesRole(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/staff/x", nil).WithContext(auth.WithIdentity(context.Background(), id)))
	if ctx == nil {
		t.Fatal("RequireRulesRole refused the staff caller")
	}
	return ctx
}
