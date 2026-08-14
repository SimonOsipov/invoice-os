// Package demopolicy gives the in-house demo tenant one active sealed approval
// policy and keeps its validated backlog armed, so awaiting_approval is non-zero
// and the Approvals badge is observable on the deploy gate.
//
// STAGE 2.5 STUB. This file declares types and signatures ONLY: DemoTenants is
// empty, Seed and seedTenant return a zero Result and issue no statement. Every
// spec in demopolicy_test.go is red against it. The implementation is Stage 3's.
package demopolicy

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DemoTenants is the tenant allowlist — the safety boundary, not ENVIRONMENT,
// which reads "development" on production and would gate fail-open. Never a
// parameter: a caller-supplied tenant is what would make "publish an approval
// policy over a real tenant's invoices" representable.
//
// Empty in the stub; TestSeed_AllowlistExcludesTheFirmTenant pins its value.
var DemoTenants []string

// Result is what one Seed call did. BacklogFound and RunsArmed are separate
// fields: equal means every candidate armed, a gap means ArmTx answered
// RunID == "" — which happens only when no active version exists.
type Result struct {
	VersionCreated bool   // the policy/version/step statements ran on THIS call
	VersionID      string // the tenant's active version, created now or found
	BacklogFound   int    // rows the anti-join returned, BEFORE arming
	RunsArmed      int    // ArmTx calls that wrote a run
	Note           string // which "did nothing, but why" cause applies
}

// Seed converges every allowlisted tenant on armed and reports what it did.
func Seed(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) (Result, error) {
	return Result{}, nil
}

// seedTenant converges ONE tenant, allowlisted or not. Unexported for the same
// reason demodocs.seedTenant is: Seed's allowlist is the boundary, and the
// per-tenant entry point exists so the suite can drive a throwaway tenant.
func seedTenant(ctx context.Context, pool *pgxpool.Pool, tenantID string) (Result, error) {
	return Result{}, nil
}
