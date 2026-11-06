package demopolicy

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestSeed_BacklogSweepWaitsForAHumanPublish: a human publish holds its tx open after its
// sweep read the invoice; the seeder starts, then the publish commits. Without the shared
// tenant publish lock the seeder sweeps the same invoice and arms a second open run.
func TestSeed_BacklogSweepWaitsForAHumanPublish(t *testing.T) {
	super, app := dbTestPools(t)
	f := newFixture(t, super, app, "demopolicy seeder vs publish")
	f.addValidatedInvoice("DEMO-T-RACE", nil)
	ctx := context.Background()

	var policyID, versionID string
	if err := super.QueryRow(ctx,
		`INSERT INTO approval_policies (tenant_id, name) VALUES ($1, 'Human policy') RETURNING id::text`,
		f.tenantID).Scan(&policyID); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	if err := super.QueryRow(ctx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version) VALUES ($1, $2, 1) RETURNING id::text`,
		f.tenantID, policyID).Scan(&versionID); err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	if _, err := super.Exec(ctx,
		`INSERT INTO approval_policy_steps (tenant_id, version_id, ord, kind, workflow_role_key)
		 VALUES ($1, $2, 0, 'approval', $3)`, f.tenantID, versionID, seededRoleKey); err != nil {
		t.Fatalf("seed step: %v", err)
	}

	inSweep := make(chan struct{})
	release := make(chan struct{})
	var once, relOnce sync.Once
	releaseHold := func() { relOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseHold)
	hold := func(ctx context.Context, tx pgx.Tx, invoiceID string) (string, error) {
		once.Do(func() {
			close(inSweep)
			<-release
		})
		return "f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0", nil
	}
	caller := auth.WithIdentity(ctx,
		auth.Identity{Subject: f.memberID, Role: "authenticated", TenantID: f.tenantID})
	pubDone := make(chan error, 1)
	go func() {
		_, err := approval.NewStore(app, hold, nil).PublishPolicy(caller, policyID)
		pubDone <- err
	}()
	<-inSweep

	seedDone := make(chan error, 1)
	go func() {
		_, err := seedTenant(ctx, app, f.tenantID)
		seedDone <- err
	}()
	waitForBlockedTenantLock(t, super, f.tenantID)
	releaseHold()

	if err := <-pubDone; err != nil {
		t.Errorf("PublishPolicy: %v, want nil", err)
	}
	if err := <-seedDone; err != nil {
		t.Errorf("seedTenant: %v, want nil", err)
	}
	if got := countRows(t, super, "approval_runs", f.tenantID); got != 1 {
		t.Errorf("approval_runs = %d, want exactly 1 for the one validated invoice", got)
	}
}
