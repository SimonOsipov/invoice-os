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

// seededPersonasThatAreStaff returns the seed's c0000000-…001..013 users for whom is_house_member is true.
func seededPersonasThatAreStaff(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	ctx := context.Background()
	var staffUsers []string
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
			staffUsers = append(staffUsers, user)
		}
	}
	return staffUsers
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

	if staff := seededPersonasThatAreStaff(t, pool); len(staff) != 0 {
		t.Errorf("seeded personas are staff: %v", staff)
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

func TestSeedNamesTheHouseWhenTheDevTenantAlreadyExists(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	isolateHouse(t, pool)
	if _, err := pool.Exec(context.Background(), `INSERT INTO tenants (id, name, kind) VALUES ($1, 'Leftover', 'firm')`, devHouseTenant); err != nil {
		t.Fatalf("insert the dev tenant: %v", err)
	}
	if got := houseRowTenant(t, pool); got != "" {
		t.Fatalf("house row names %s before the seed, want an empty table", got)
	}

	if err := db.Seed(context.Background(), superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if got := houseRowTenant(t, pool); got != devHouseTenant {
		t.Errorf("house row names %q, want the dev house tenant %s: a tenant without a house row must still be named", got, devHouseTenant)
	}
}

func TestSeedTwiceLeavesOneHouseRowOnTheDevTenant(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	isolateHouse(t, pool)

	for i := 0; i < 2; i++ {
		if err := db.Seed(context.Background(), superDSN, dbsql.FS); err != nil {
			t.Fatalf("Seed run %d: %v", i+1, err)
		}
		if n := mustCount(t, pool, `SELECT count(*) FROM public.house_company`); n != 1 {
			t.Errorf("house rows after seed run %d = %d, want 1", i+1, n)
		}
		if got := houseRowTenant(t, pool); got != devHouseTenant {
			t.Errorf("house row names %q after seed run %d, want %s", got, i+1, devHouseTenant)
		}
	}
}

func TestPurgeKeepsAHouseRowThatNamesAPurgedDemoTenant(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()
	isolateHouse(t, pool)
	// Purge empties the demo tenants; reseed them so the dev DB is left as found.
	t.Cleanup(func() {
		if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
			t.Errorf("reseed: %v", err)
		}
	})
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	const purgedTenant = "11111111-1111-1111-1111-111111111111"
	if !slices.Contains(db.DemoTenants, purgedTenant) {
		t.Fatalf("%s is not a demo tenant: the purge never reaches it", purgedTenant)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM public.house_company`); err != nil {
		t.Fatalf("clear house row: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.house_company (tenant_id) VALUES ($1)`, purgedTenant); err != nil {
		t.Fatalf("set the house row: %v", err)
	}

	res, err := db.PurgeDemoTenants(ctx, superDSN)
	if err != nil {
		t.Fatalf("PurgeDemoTenants: %v", err)
	}
	// Pair: the purge deleted rows of the named tenant, so the surviving house row is the exclusion at work.
	if res.Rows == 0 {
		t.Fatal("the purge deleted nothing, so this test proves nothing")
	}
	if _, listed := res.ByTable["house_company"]; listed {
		t.Errorf("the purge reports deleting house_company rows: %v", res.ByTable)
	}
	if got := houseRowTenant(t, pool); got != purgedTenant {
		t.Errorf("house row names %q after the purge, want %s", got, purgedTenant)
	}
}

func TestProvisionSeedsNoHouseRowInProduction(t *testing.T) {
	e := newShardEnv(t)
	isolateHouse(t, e.super)

	cfg := e.prConfig()
	cfg.Environment = "production"
	cfg.RailwayEnvironmentName = "production"
	cfg.ResetFlag = ""
	if err := db.Provision(context.Background(), cfg); err != nil {
		t.Fatalf("Provision (production): %v", err)
	}
	if got := houseRowTenant(t, e.super); got != "" {
		t.Errorf("a production boot named a house company: %s", got)
	}
	if got := devHouseTenantName(t, e.super); got != "" {
		t.Errorf("a production boot seeded the dev house tenant %q", got)
	}

	// Pair: the same config on the allowlisted environment does seed, so the empty table above is the gate.
	cfg.Environment = "development"
	if err := db.Provision(context.Background(), cfg); err != nil {
		t.Fatalf("Provision (development): %v", err)
	}
	if got := houseRowTenant(t, e.super); got != devHouseTenant {
		t.Errorf("a development boot named house %q, want %s", got, devHouseTenant)
	}
}

func TestProvisionOnAPREnvironmentKeepsTheHouseRowAndMakesNoShardOrPersonaStaff(t *testing.T) {
	for _, tc := range []struct {
		name, preset, want string
	}{
		{"empty table", "", devHouseTenant},
		{"row on another tenant", otherHouseTenant, otherHouseTenant},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newShardEnv(t)
			isolateHouse(t, e.super)
			if tc.preset != "" {
				if _, err := e.super.Exec(context.Background(), `INSERT INTO tenants (id, name) VALUES ($1, 'Tenant A (dev)') ON CONFLICT DO NOTHING`, tc.preset); err != nil {
					t.Fatalf("ensure tenant: %v", err)
				}
				if _, err := e.super.Exec(context.Background(), `INSERT INTO public.house_company (tenant_id) VALUES ($1)`, tc.preset); err != nil {
					t.Fatalf("set the house row: %v", err)
				}
			}

			e.provisionReset(t)

			e.requireAllShardTenants(t, "after a reset-on Provision") // the shard seed ran
			if got := houseRowTenant(t, e.super); got != tc.want {
				t.Errorf("house row names %q after the PR boot, want %s", got, tc.want)
			}
			if n := mustCount(t, e.super, `SELECT count(*) FROM public.house_company`); n != 1 {
				t.Errorf("house rows = %d, want 1", n)
			}
			if n := mustCount(t, e.super,
				`SELECT count(*) FROM memberships WHERE tenant_id = $1 AND user_id::text LIKE 'c0000000-%'`, devHouseTenant); n != 0 {
				t.Errorf("the dev house tenant holds %d seeded personas, want 0", n)
			}
			if tc.preset == "" {
				if staff := seededPersonasThatAreStaff(t, e.super); len(staff) != 0 {
					t.Errorf("seeded personas are staff after the PR boot: %v", staff)
				}
			}
		})
	}
}
