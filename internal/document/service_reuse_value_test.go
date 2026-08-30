// service_reuse_value_test.go: Service.Store's reuse flag by VALUE.
//
// service_reuse_flag_test.go pins only the SIGNATURE. A flag returned as Upsert's `created`
// rather than `!created` has the same type, compiles, and passes that pin -- so every caller
// would read reuse exactly inverted with nothing red. These specs read both arms off a real
// dedupe, which is the only place that inversion has to fail.
package document_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/document"
)

// pvPurgeAuditLog drops a tenant's audit rows at teardown. audit_log carries no foreign key to
// tenants, so seedTenant's DELETE FROM tenants does not reach them and they accumulate under
// dead ids -- and that debris is NOT inert: internal/platform/db's
// TestRLS_AuditReadTenantQualIsAnIndexCondOnEveryNewIndex asserts a planner index CHOICE, which
// moves with audit_log's global row count. The two specs below tipped it before this purge.
//
// audit_log_append_only() refuses DELETE for every role including the owner;
// session_replication_role='replica' is the one bypass, is superuser-only, and SET LOCAL
// confines it to this transaction. Mirrors wkPurgeAuditLog (internal/extraction).
func pvPurgeAuditLog(t *testing.T, super *pgxpool.Pool, tenantID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		tx, err := super.Begin(ctx)
		if err != nil {
			t.Errorf("teardown audit_log for tenant %s: begin: %v", tenantID, err)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = 'replica'`); err != nil {
			t.Errorf("teardown audit_log for tenant %s: set session_replication_role: %v", tenantID, err)
			return
		}
		if _, err := tx.Exec(ctx, `DELETE FROM audit_log WHERE tenant_id = $1`, tenantID); err != nil {
			t.Errorf("teardown audit_log for tenant %s: %v", tenantID, err)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			t.Errorf("teardown audit_log for tenant %s: commit: %v", tenantID, err)
		}
	})
}

// TestServiceStore_ReuseFlagIsFalseThenTrueForTheSameBytes: the same bytes twice.
//
// The same-ID assertion between the two arms is the control: "false then true" says nothing
// about a dedupe if the second call minted a second row, and it must be the dedupe -- not a
// call counter -- that drives the flag.
func TestServiceStore_ReuseFlagIsFalseThenTrueForTheSameBytes(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "svc reuse flag by value")
	pvPurgeAuditLog(t, super, tenantID)
	c := identity(ctx, tenantID, memberSubject)
	svc := document.NewService(document.NewStore(app), &fakeObjects{})
	body := []byte("%PDF-1.7 the same bytes twice")

	first, reusedFirst, err := svc.Store(c, "a.pdf", "application/pdf", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("first Store: %v", err)
	}
	if first.ID == "" {
		t.Fatalf("first Store returned no document id; the arms below would compare two empty strings")
	}
	if reusedFirst {
		t.Errorf("first Store of bytes this tenant had never held reported reused=true, want false -- the flag is Upsert's created flag INVERTED, and true here is that inversion missing")
	}

	second, reusedSecond, err := svc.Store(c, "a.pdf", "application/pdf", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("second Store: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("the second Store minted document %s, want the first's %s; nothing deduped, so the reuse assertion below would not be about a reuse", second.ID, first.ID)
	}
	if !reusedSecond {
		t.Errorf("second Store of identical bytes reported reused=false, want true -- POST /v1/documents would tell the caller it had stored a new document")
	}
}

// TestServiceStore_ReuseFlagIsPerTenantNotPerContentHash: tenant B storing bytes tenant A
// already holds is a FIRST store for B, because the unique index is (tenant_id, content_hash).
// A flag driven by the hash alone, or by any process-global memory of what has been seen,
// reports reuse here and tells B its brand-new upload was a duplicate.
//
// A's own false-then-true pair runs first as the control: a flag hardcoded to false would
// satisfy B's assertion on its own.
func TestServiceStore_ReuseFlagIsPerTenantNotPerContentHash(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "svc reuse per-tenant A")
	tenantB := seedTenant(t, super, "svc reuse per-tenant B")
	pvPurgeAuditLog(t, super, tenantA)
	pvPurgeAuditLog(t, super, tenantB)
	cA := identity(ctx, tenantA, memberSubject)
	cB := identity(ctx, tenantB, memberSubject)
	svc := document.NewService(document.NewStore(app), &fakeObjects{})
	body := []byte("%PDF-1.7 bytes two tenants both hold")

	docA1, reusedA1, err := svc.Store(cA, "a.pdf", "application/pdf", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Store as A: %v", err)
	}
	if reusedA1 {
		t.Fatalf("A's first Store reported reused=true; the per-tenant claim below rests on this being false")
	}
	_, reusedA2, err := svc.Store(cA, "a.pdf", "application/pdf", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("second Store as A: %v", err)
	}
	if !reusedA2 {
		t.Fatalf("A's second Store of identical bytes reported reused=false; the flag never reaches true, so B's assertion below would pass vacuously")
	}

	docB, reusedB, err := svc.Store(cB, "a.pdf", "application/pdf", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Store as B: %v", err)
	}
	if reusedB {
		t.Errorf("B's FIRST Store of bytes only A had held reported reused=true; the dedupe is (tenant_id, content_hash), so B stored a new document and must be told so")
	}
	if docB.ID == docA1.ID {
		t.Errorf("B's document is %s, the same row A holds; the tenants share a documents row and the flag is the least of it", docB.ID)
	}
}
