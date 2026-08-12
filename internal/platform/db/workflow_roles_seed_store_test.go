// QA adversarial coverage for the workflow-role seed (subtask 03), added on top
// of seed_demo_test.go's AC-derived suite. Deliberately a SEPARATE file: unlike
// every other TestSeed* test in this package, TestSeedFiveHolderStatesReachableThroughTheRealStore
// below additionally needs DATABASE_URL (app-role pool) to drive
// approval.Store.ListRoles -- the real read path an HTTP handler uses, not a raw
// SQL join. It self-skips without DATABASE_URL; CI's rls-tests-seed job already
// sets it alongside DATABASE_SUPERUSER_URL for the `TestSeed` run
// (.github/workflows/ci.yml).
package db_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// requireAppDSN mirrors provision_test.go's inline DATABASE_URL check: self-skip,
// never fail closed -- the same posture requireSuperuserDSN takes for its own var.
func requireAppDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("workflow-role store test skipped: set DATABASE_URL (app-role DSN)")
	}
	return dsn
}

// asTenant returns a context carrying a verified Identity for tenantID, the cheap
// stub auth.WithIdentity documents for "run as this tenant" without minting a
// token. ListRoles applies no access-role gate (store.go:53-55), so the caller's
// role only needs to be a plausible one, not a real membership row.
func asTenant(tenantID string) context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{
		Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID,
	})
}

// TestSeedFiveHolderStatesReachableThroughTheRealStore: Test Spec AC-8, re-asserted
// through approval.Store.ListRoles (the real read path GET /v1/workflow-roles
// exercises) instead of a raw SQL join, then the same holderIsApprover predicate
// seed_demo_test.go's raw-SQL version uses -- mirroring lib/roles.ts's
// resolution(). This catches a bug in ListRoles' own query (e.g. a wrong JOIN or
// ORDER BY) that a raw-SQL assertion re-deriving the same facts never could.
func TestSeedFiveHolderStatesReachableThroughTheRealStore(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	appDSN := requireAppDSN(t)
	superPool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	appPool, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		t.Fatalf("connect app pool: %v", err)
	}
	defer appPool.Close()
	store := approval.NewStore(appPool, nil, nil) // ListRoles only — fingerprinter/demoter are unused here

	holdersOf := func(tenantID, roleKey string) []holderMembership {
		t.Helper()
		roles, err := store.ListRoles(asTenant(tenantID))
		if err != nil {
			t.Fatalf("ListRoles(%s): %v", tenantID, err)
		}
		var memberIDs []string
		found := false
		for _, r := range roles {
			if r.Key == roleKey {
				memberIDs = r.Members
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("ListRoles(%s): role %q not present", tenantID, roleKey)
		}
		out := make([]holderMembership, 0, len(memberIDs))
		for _, uid := range memberIDs {
			var h holderMembership
			h.userID = uid
			if err := superPool.QueryRow(ctx,
				`SELECT role, status FROM memberships WHERE tenant_id = $1 AND user_id = $2`,
				tenantID, uid,
			).Scan(&h.role, &h.status); err != nil {
				t.Fatalf("read membership for holder %s: %v", uid, err)
			}
			out = append(out, h)
		}
		return out
	}

	lineMgr := holdersOf(honeywellTenantID, "line_mgr")
	if len(lineMgr) != 1 || !holderIsApprover(lineMgr[0].role, lineMgr[0].status) {
		t.Errorf("ok-one-holder via ListRoles: in-house line_mgr holders = %+v, want exactly 1 active admin/reviewer", lineMgr)
	}

	finDir := holdersOf(honeywellTenantID, "fin_dir")
	activeApprovers := 0
	for _, h := range finDir {
		if holderIsApprover(h.role, h.status) {
			activeApprovers++
		}
	}
	if len(finDir) != 2 || activeApprovers == 0 {
		t.Errorf("ok-several via ListRoles: in-house fin_dir holders = %+v, want 2 with at least 1 active approver", finDir)
	}

	if got := holdersOf(demoTenantID, "quality_reviewer"); len(got) != 0 {
		t.Errorf("nobody-holds via ListRoles: firm quality_reviewer holders = %+v, want 0", got)
	}
	if got := holdersOf(honeywellTenantID, "ceo"); len(got) != 0 {
		t.Errorf("nobody-holds via ListRoles: in-house ceo holders = %+v, want 0", got)
	}

	cfo := holdersOf(honeywellTenantID, "cfo")
	if len(cfo) != 1 || cfo[0].status != "suspended" {
		t.Errorf("suspended-only via ListRoles: in-house cfo holders = %+v, want exactly 1 with status=suspended", cfo)
	}
}

// TestSeedRenamedRoleTitleReconvergesOnReseed: AC-5's title-converges half, on a
// LIVE (non-corrupted, non-deleted) rename -- the shape an admin's real PATCH
// leaves behind, distinct from TestSeedWorkflowRolesRepairASoftDeletedSeat's
// corrupt-and-soft-delete scenario. Documents the actual product decision AC-5
// states outright: title/description always converge to the shipped value on
// reseed, so a demo-tenant admin's own rename does not survive the next deploy.
func TestSeedRenamedRoleTitleReconvergesOnReseed(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()
	t.Cleanup(func() {
		if err := db.Seed(context.Background(), superDSN, dbsql.FS); err != nil {
			t.Errorf("restore curated demo state: %v", err)
		}
	})

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("baseline Seed: %v", err)
	}

	// Simulate a live admin rename via UpdateRole -- title only, no soft-delete.
	if _, err := pool.Exec(ctx,
		`UPDATE workflow_roles SET title = 'Chief Financial Renamer' WHERE tenant_id = $1 AND key = 'cfo'`,
		demoTenantID,
	); err != nil {
		t.Fatalf("simulate admin rename: %v", err)
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("re-seed after rename: %v", err)
	}

	var title string
	if err := pool.QueryRow(ctx,
		`SELECT title FROM workflow_roles WHERE tenant_id = $1 AND key = 'cfo'`, demoTenantID,
	).Scan(&title); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if title != "Engagement Partner" {
		t.Errorf("title after reseed following a live rename = %q, want the shipped title %q -- AC-5's DO UPDATE reconverges every title, admin edits included", title, "Engagement Partner")
	}
}

// TestSeedFullyUnstaffedMemberIsRestoredOnReseed: documents the ACTUAL behaviour
// of the DO NOTHING clause on workflow_role_members_tenant_role_user_uq, which
// diverges from the seed's own comment claim ("a live staffing edit is the
// tenant admin's own choice and is never seed-repaired", db/seed.dev.sql).
// DO NOTHING only protects a row that still EXISTS from a conflicting write; it
// cannot protect a row the admin fully DELETED (SetRoleMembers' unstaff path,
// store.go:413-414) from being re-inserted, because a deleted row leaves no
// conflict target. Flagging this for the executor/architect: either the comment
// overclaims, or the mechanism should change (e.g. DO NOTHING on the ROLE side
// keyed differently, or a tombstone) -- not this subtask's call to make.
func TestSeedFullyUnstaffedMemberIsRestoredOnReseed(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()
	t.Cleanup(func() {
		if err := db.Seed(context.Background(), superDSN, dbsql.FS); err != nil {
			t.Errorf("restore curated demo state: %v", err)
		}
	})

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("baseline Seed: %v", err)
	}

	// Simulate SetRoleMembers unstaffing the firm cfo role's sole holder: the
	// staffing row is DELETEd, exactly as store.go:413-414 does on an empty PUT.
	tag, err := pool.Exec(ctx,
		`DELETE FROM workflow_role_members wrm USING workflow_roles r
		  WHERE wrm.workflow_role_id = r.id AND r.tenant_id = $1 AND r.key = 'cfo'`,
		demoTenantID,
	)
	if err != nil {
		t.Fatalf("simulate unstaff: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("precondition: unstaff deleted %d rows, want 1", tag.RowsAffected())
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("re-seed after unstaff: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM workflow_role_members wrm JOIN workflow_roles r ON r.id = wrm.workflow_role_id
		  WHERE r.tenant_id = $1 AND r.key = 'cfo'`,
		demoTenantID,
	).Scan(&count); err != nil {
		t.Fatalf("read back: %v", err)
	}
	// This asserts the CURRENT shipped behaviour (restaffed), not an endorsement of
	// it -- see the doc comment above. A future fix that makes unstaffing durable
	// across a reseed should flip this assertion to count == 0, deliberately.
	if count != 1 {
		t.Errorf("cfo staffing row count after reseed following a full unstaff = %d, want 1 (current DO NOTHING behaviour re-inserts a fully-removed row -- if this now reads 0, the mechanism changed and the doc comment above should be updated to match)", count)
	}
}

// TestSeedRoleRecreatedUnderANewIDStillConverges: a role whose original row was
// hard-deleted (an operator/rollback scenario superuser can reach even though
// invoice_app holds no DELETE grant on workflow_roles) and re-created under a
// DIFFERENT id with the SAME (tenant_id, key) -- the same "id moved" shape Part 2's
// JOIN-vs-literal-id analysis covers, exercised end-to-end through Seed here.
// CreateRole itself can never reproduce this (its taken-key scan spans deleted
// rows, store.go:144-146, so the real key uq refuses a same-key duplicate) --
// this is deliberately an out-of-band DB state, not a live-app-reachable one.
func TestSeedRoleRecreatedUnderANewIDStillConverges(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()
	t.Cleanup(func() {
		// A plain re-seed cannot repair this: DO UPDATE never touches created_at
		// (AC-3), so the out-of-band row's now() would stick forever. Delete first
		// so the INSERT path restores the pinned literal id and created_at.
		cctx := context.Background()
		if _, err := pool.Exec(cctx, `DELETE FROM workflow_roles WHERE tenant_id = $1 AND key = 'line_mgr'`, honeywellTenantID); err != nil {
			t.Errorf("cleanup: delete out-of-band line_mgr row: %v", err)
		}
		if err := db.Seed(cctx, superDSN, dbsql.FS); err != nil {
			t.Errorf("restore curated demo state: %v", err)
		}
	})

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("baseline Seed: %v", err)
	}

	newID := uuid.NewString()
	// ON DELETE CASCADE on workflow_role_members_tenant_role_fk clears staffing too.
	if _, err := pool.Exec(ctx, `DELETE FROM workflow_roles WHERE tenant_id = $1 AND key = 'line_mgr'`, honeywellTenantID); err != nil {
		t.Fatalf("hard-delete line_mgr role (setup): %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO workflow_roles (id, tenant_id, key, title, description, created_at)
		 VALUES ($1, $2, 'line_mgr', 'Operator Recreated', 'out-of-band row', now())`,
		newID, honeywellTenantID,
	); err != nil {
		t.Fatalf("recreate line_mgr under a new id (setup): %v", err)
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("re-seed after out-of-band recreate: %v", err)
	}

	var liveID, title string
	if err := pool.QueryRow(ctx,
		`SELECT id::text, title FROM workflow_roles WHERE tenant_id = $1 AND key = 'line_mgr' AND deleted_at IS NULL`,
		honeywellTenantID,
	).Scan(&liveID, &title); err != nil {
		t.Fatalf("read back role: %v", err)
	}
	if liveID != newID {
		t.Errorf("live line_mgr id after reseed = %s, want the new id %s (DO UPDATE never touches id)", liveID, newID)
	}
	if title != "Line Manager" {
		t.Errorf("live line_mgr title after reseed = %q, want the shipped title %q", title, "Line Manager")
	}

	var stagedRoleID string
	if err := pool.QueryRow(ctx,
		`SELECT workflow_role_id::text FROM workflow_role_members WHERE tenant_id = $1 AND user_id = 'c0000000-0000-0000-0000-000000000009'`,
		honeywellTenantID,
	).Scan(&stagedRoleID); err != nil {
		t.Fatalf("read back staffing: %v", err)
	}
	if stagedRoleID != newID {
		t.Errorf("line_mgr staffing row's workflow_role_id = %s, want the new id %s -- the JOIN must resolve to the CURRENT row", stagedRoleID, newID)
	}
}
