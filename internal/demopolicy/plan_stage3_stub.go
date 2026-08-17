// STAGE 2.5 STUB for task-568 (APPR-14-02). plan / firmPlan / inhousePlan /
// planFor / seedTenantPlan / reactivateSealedVersion are the plan-dispatch
// surface Stage 3 implements for real inside demopolicy.go. Declared here,
// signature-only and inert, so demopolicy_test.go and demopolicy_qa_test.go
// compile and every new spec goes red on its target assertion rather than on
// an undefined identifier. Stage 3 absorbs these declarations into
// demopolicy.go (with real bodies) and deletes this file.
package demopolicy

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// plan names which tree a tenant seeds. Opaque placeholder — Stage 3 owns its
// real shape (steps, requiredSeats, draft).
type plan struct {
	name string
}

var (
	firmPlan    = plan{name: "firm"}
	inhousePlan = plan{name: "inhouse"}
)

// planFor picks a tenant's plan by ID, never by tenants.kind. Stub always
// answers inhousePlan.
func planFor(tenantID string) plan { return inhousePlan }

// seedTenantPlan lets the suite drive a named plan against a throwaway
// tenant, bypassing planFor's ID dispatch. Stub is a no-op.
func seedTenantPlan(ctx context.Context, pool *pgxpool.Pool, tenantID string, p plan) (Result, error) {
	return Result{}, nil
}

// reactivateSealedVersion clears the tenant's active slot and reactivates an
// existing sealed version. Stub is a no-op.
func reactivateSealedVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID string) error {
	return nil
}
