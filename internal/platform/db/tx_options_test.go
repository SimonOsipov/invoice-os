// tx_options_test.go: a regression guard for AUDIT-05-07/D-33, which adds
// WithinTenantTxOpts/WithinRequestTenantTxOpts and makes the two existing helpers
// delegate to them with a zero pgx.TxOptions. This file proves the existing helpers
// still emit a bare "begin" today, so a future edit to the delegation cannot silently
// widen every one of this helper's 53 call sites to a stronger isolation level.
//
// Named TestRLS_ (not semantically an RLS test) so the CI rls job's -run TestRLS
// (.github/workflows/ci.yml) and `make test-rls` still pick it up -- a distinct
// prefix here would be invisible to CI (see tenants_kind_test.go for the same
// convention). Reuses the shared harness (rls_harness_test.go) for h.appURL/h.tenantA
// and demopurge_test.go's stmtTracer for tracing.
package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// tracedAppPool opens a fresh pool on h.appURL with sql tracing, so the exact BEGIN
// statement WithinTenantTx/WithinRequestTenantTx emit can be asserted.
func tracedAppPool(t *testing.T) (*pgxpool.Pool, *stmtTracer) {
	t.Helper()
	h := requireHarness(t)
	tr := &stmtTracer{}
	cfg, err := pgxpool.ParseConfig(h.appURL)
	if err != nil {
		t.Fatalf("parse app url: %v", err)
	}
	cfg.ConnConfig.Tracer = tr
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new traced app pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, tr
}

// onlyBegin returns the begin-like statements among stmts ("begin" or "begin ...").
func onlyBegins(stmts []string) []string {
	var out []string
	for _, s := range stmts {
		if s == "begin" || strings.HasPrefix(s, "begin ") {
			out = append(out, s)
		}
	}
	return out
}

func TestRLS_WithinTenantTxStillBeginsAPlainTransaction(t *testing.T) {
	h := requireHarness(t)
	pool, tr := tracedAppPool(t)

	if err := db.WithinTenantTx(context.Background(), pool, h.tenantA, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("WithinTenantTx: unexpected error: %v", err)
	}

	stmts := tr.recorded()
	if len(stmts) == 0 {
		t.Fatal("the tracer recorded no statements at all -- it is not attached to the connection WithinTenantTx ran on")
	}
	begins := onlyBegins(stmts)
	if len(begins) != 1 {
		t.Fatalf("WithinTenantTx issued %d begin-like statement(s), want exactly 1: %q (all: %q)", len(begins), begins, stmts)
	}
	if begins[0] != "begin" {
		t.Errorf("WithinTenantTx begin sql = %q, want exactly %q -- a future edit must not silently widen every tenant-scoped caller to a stronger isolation level", begins[0], "begin")
	}
}

func TestRLS_WithinRequestTenantTxStillBeginsAPlainTransaction(t *testing.T) {
	h := requireHarness(t)
	pool, tr := tracedAppPool(t)

	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), TenantID: h.tenantA})
	if err := db.WithinRequestTenantTx(ctx, pool, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("WithinRequestTenantTx: unexpected error: %v", err)
	}

	stmts := tr.recorded()
	if len(stmts) == 0 {
		t.Fatal("the tracer recorded no statements at all -- it is not attached to the connection WithinRequestTenantTx ran on")
	}
	begins := onlyBegins(stmts)
	if len(begins) != 1 {
		t.Fatalf("WithinRequestTenantTx issued %d begin-like statement(s), want exactly 1: %q (all: %q)", len(begins), begins, stmts)
	}
	if begins[0] != "begin" {
		t.Errorf("WithinRequestTenantTx begin sql = %q, want exactly %q", begins[0], "begin")
	}
}
