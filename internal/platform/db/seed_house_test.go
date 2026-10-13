package db_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const otherHouseTenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

// isolateHouse leaves no house row and no dev house tenant, and restores both in t.Cleanup.
func isolateHouse(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var rowTenant *string
	err := pool.QueryRow(ctx, `SELECT tenant_id::text FROM public.house_company`).Scan(&rowTenant)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("read house row (is Migration A applied?): %v", err)
	}
	var devName, devKind *string
	err = pool.QueryRow(ctx, `SELECT name, kind FROM tenants WHERE id = $1`, devHouseTenant).Scan(&devName, &devKind)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("read dev house tenant: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM public.house_company`); err != nil {
			t.Errorf("restore: clear house row: %v", err)
		}
		if devName != nil {
			if _, err := pool.Exec(ctx,
				`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, $3) ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, kind = EXCLUDED.kind`,
				devHouseTenant, *devName, *devKind); err != nil {
				t.Errorf("restore: dev house tenant: %v", err)
			}
		}
		if rowTenant != nil {
			if _, err := pool.Exec(ctx, `INSERT INTO public.house_company (tenant_id) VALUES ($1)`, *rowTenant); err != nil {
				t.Errorf("restore: house row: %v", err)
			}
		}
	})
	if _, err := pool.Exec(ctx, `DELETE FROM public.house_company`); err != nil {
		t.Fatalf("clear house row: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, devHouseTenant); err != nil {
		t.Fatalf("remove dev house tenant (does it still hold rows?): %v", err)
	}
}

func houseRowTenant(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `SELECT tenant_id::text FROM public.house_company`).Scan(&id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("read the house row: %v", err)
	}
	return id // "" when the table is empty
}

func devHouseTenantName(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var name string
	err := pool.QueryRow(context.Background(), `SELECT name FROM tenants WHERE id = $1`, devHouseTenant).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("read dev house tenant: %v", err)
	}
	return name
}

func TestSeedNamesTheDevHouseCompanyOnAnEmptyTable(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	isolateHouse(t, pool)

	if err := db.Seed(context.Background(), superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if got := houseRowTenant(t, pool); got != devHouseTenant {
		t.Errorf("house row names %s, want the dev house tenant %s", got, devHouseTenant)
	}
	if got := devHouseTenantName(t, pool); got != "ASComply (dev)" {
		t.Errorf("dev house tenant name = %q, want %q", got, "ASComply (dev)")
	}
}

func TestSeedNeverReplacesAnExistingHouseRow(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	isolateHouse(t, pool)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO tenants (id, name) VALUES ($1, 'Tenant A (dev)') ON CONFLICT DO NOTHING`, otherHouseTenant); err != nil {
		t.Fatalf("ensure tenant: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO public.house_company (tenant_id) VALUES ($1)`, otherHouseTenant); err != nil {
		t.Fatalf("set the house row: %v", err)
	}

	if err := db.Seed(context.Background(), superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	// Pair: the seed did reach the new statements, so an unchanged row is a refusal, not a skipped seed.
	if got := devHouseTenantName(t, pool); got != "ASComply (dev)" {
		t.Errorf("seed did not create the dev house tenant: name = %q, want %q", got, "ASComply (dev)")
	}
	if got := houseRowTenant(t, pool); got != otherHouseTenant {
		t.Errorf("house row names %s after the seed, want it left on %s", got, otherHouseTenant)
	}
}

func TestSeedMakesNoSeededPersonaStaff(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()
	isolateHouse(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	// Pair: a house exists, so false below is not the empty-table false.
	if got := houseRowTenant(t, pool); got != devHouseTenant {
		t.Fatalf("house row names %s, want the dev house tenant %s", got, devHouseTenant)
	}
	var seeded int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE user_id::text LIKE 'c0000000-%'`).Scan(&seeded); err != nil {
		t.Fatalf("count seeded memberships: %v", err)
	}
	if seeded == 0 {
		t.Fatal("the seed holds no c0000000- memberships, so this test proves nothing")
	}

	for i := 1; i <= 13; i++ {
		user := fmt.Sprintf("c0000000-0000-0000-0000-%012d", i)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		var staff bool
		if _, err = tx.Exec(ctx, `SET LOCAL ROLE invoice_migrator`); err == nil {
			err = tx.QueryRow(ctx, `SELECT public.is_house_member($1::uuid)`, user).Scan(&staff)
		}
		_ = tx.Rollback(ctx)
		if err != nil {
			t.Fatalf("is_house_member(%s): %v", user, err)
		}
		if staff {
			t.Errorf("seeded persona %s is staff", user)
		}
	}

	var inHouse int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM memberships WHERE tenant_id = $1 AND user_id::text LIKE 'c0000000-%'`, devHouseTenant).Scan(&inHouse); err != nil {
		t.Fatalf("count house memberships: %v", err)
	}
	if inHouse != 0 {
		t.Errorf("the house tenant holds %d seeded c0000000- memberships, want 0", inHouse)
	}
}

func TestPurgeLeavesTheHouseRowAndItsMembers(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()
	isolateHouse(t, pool)

	// Pair: the purge reaches the house tenant, so the survivors below are the exclusion at work.
	if !slices.Contains(db.DemoTenants, devHouseTenant) {
		t.Errorf("db.DemoTenants lacks the dev house tenant %s: the purge never reaches it", devHouseTenant)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'ASComply (dev)') ON CONFLICT DO NOTHING`, devHouseTenant); err != nil {
		t.Fatalf("insert house tenant: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.house_company (tenant_id) VALUES ($1)`, devHouseTenant); err != nil {
		t.Fatalf("insert house row: %v", err)
	}
	member := uuid.NewString()
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role, status, email) VALUES ($1, $2, 'reviewer', 'active', 'purge-house@example.test')`,
		devHouseTenant, member); err != nil {
		t.Fatalf("insert house member: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM memberships WHERE tenant_id = $1 AND user_id = $2`, devHouseTenant, member); err != nil {
			t.Errorf("remove house member: %v", err)
		}
	})
	// Purge empties the demo tenants; reseed them so the dev DB is left as found.
	t.Cleanup(func() {
		if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
			t.Errorf("reseed: %v", err)
		}
	})

	if _, err := db.PurgeDemoTenants(ctx, superDSN); err != nil {
		t.Fatalf("PurgeDemoTenants: %v", err)
	}
	if got := houseRowTenant(t, pool); got != devHouseTenant {
		t.Errorf("house row names %s after the purge, want %s", got, devHouseTenant)
	}
	var kept int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE tenant_id = $1 AND user_id = $2`, devHouseTenant, member).Scan(&kept); err != nil {
		t.Fatalf("count house members: %v", err)
	}
	if kept != 1 {
		t.Errorf("house membership rows after the purge = %d, want 1", kept)
	}
}
