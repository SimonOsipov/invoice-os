// [M4-21-03] Test-first (RED) suite for db.Seed (task-127, Test Spec row 7 /
// AC-7). Pre-authored BEFORE bootstrap.go's real Seed body exists — the shipped
// stub always returns a non-nil "not implemented" error, so the one test below
// fails immediately on that assertion, never on a missing symbol.
//
// Design mirrors bootstrap_test.go's conventions in this package: env-gated skip
// on DATABASE_SUPERUSER_URL only (Seed runs exclusively as the superuser —
// tenants is FORCE RLS), reusing its requireSuperuserDSN / bootstrapSuperuserPool
// helpers rather than duplicating them. The "migrated schema" precondition is
// already satisfied by this package's shared dev/CI Postgres (every other test in
// this package depends on the same already-migrated schema), so this test does
// not re-migrate; it only exercises Seed itself.
package db_test

import (
	"context"
	"testing"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// seedTenants mirrors db/seed.dev.sql's fixed tenant rows (id, name, kind)
// exactly, so a stale/incomplete embed or a half-applying Seed is caught by a
// mismatched id/name/kind, not just "no error".
var seedTenants = []struct {
	id, name, kind string
}{
	{"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "Tenant A (dev)", "firm"},
	{"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "Tenant B (dev)", "firm"},
	{"11111111-1111-1111-1111-111111111111", "Okafor & Partners", "firm"},
	{"22222222-2222-2222-2222-222222222222", "Honeywell Group", "in_house"},
}

// seedMemberships mirrors db/seed.dev.sql's fixed membership rows exactly.
var seedMemberships = []struct {
	tenantID, userID, role string
}{
	{"11111111-1111-1111-1111-111111111111", "c0000000-0000-0000-0000-000000000001", "admin"},
	{"11111111-1111-1111-1111-111111111111", "c0000000-0000-0000-0000-000000000003", "preparer"},
	{"11111111-1111-1111-1111-111111111111", "c0000000-0000-0000-0000-000000000004", "reviewer"},
	{"22222222-2222-2222-2222-222222222222", "c0000000-0000-0000-0000-000000000002", "admin"},
}

// TestSeedFromEmbeddedIsIdempotent: Test Spec row 7 / AC-7. Calls db.Seed twice
// against dbsql.FS (the embedded copy of db/seed.dev.sql, never the on-disk
// file); neither call may error, and each of the 4 seeded tenants + 4 memberships
// must appear EXACTLY ONCE with the expected kind/role — proving both idempotency
// (ON CONFLICT DO UPDATE / DO NOTHING actually holds across a repeated call) and
// that the embedded file matches the on-disk fixture data, not a stale copy.
func TestSeedFromEmbeddedIsIdempotent(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("first Seed: %v", err)
	}
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed (idempotency): %v", err)
	}

	for _, tc := range seedTenants {
		var count int
		var kind string
		err := pool.QueryRow(ctx,
			`SELECT count(*), max(kind) FROM tenants WHERE id = $1 AND name = $2 GROUP BY kind`,
			tc.id, tc.name,
		).Scan(&count, &kind)
		if err != nil {
			t.Fatalf("query tenant %s (%s): %v (does it exist at all after Seed?)", tc.id, tc.name, err)
		}
		if count != 1 {
			t.Errorf("tenant %s (%s): found %d rows after two Seed calls, want exactly 1", tc.id, tc.name, count)
		}
		if kind != tc.kind {
			t.Errorf("tenant %s (%s): kind = %q, want %q", tc.id, tc.name, kind, tc.kind)
		}
	}

	for _, tc := range seedMemberships {
		var count int
		var role string
		err := pool.QueryRow(ctx,
			`SELECT count(*), max(role) FROM memberships WHERE tenant_id = $1 AND user_id = $2 GROUP BY role`,
			tc.tenantID, tc.userID,
		).Scan(&count, &role)
		if err != nil {
			t.Fatalf("query membership %s/%s: %v (does it exist at all after Seed?)", tc.tenantID, tc.userID, err)
		}
		if count != 1 {
			t.Errorf("membership %s/%s: found %d rows after two Seed calls, want exactly 1", tc.tenantID, tc.userID, count)
		}
		if role != tc.role {
			t.Errorf("membership %s/%s: role = %q, want %q", tc.tenantID, tc.userID, role, tc.role)
		}
	}
}
