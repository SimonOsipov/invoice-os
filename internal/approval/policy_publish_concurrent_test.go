package approval

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TestPublish_ConcurrentPublishesArmOneRun: publish A holds its tx open after its sweep
// read the invoice; publish B of another policy starts, then A commits. Without the
// tenant publish lock B sweeps the same invoice and arms a second open run (23505).
func TestPublish_ConcurrentPublishesArmOneRun(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-01 concurrent-publishes")
	c, _ := activeAdmin(t, super, tenantID)

	entityID := seedBusinessEntity(t, super, tenantID, "Race Corp")
	bulkSeedValidatedInvoices(t, super, tenantID, entityID, "race", 1)
	member := uuid.NewString()
	seedMembership(t, super, tenantID, member, "preparer", "active")
	roleID := seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")
	staffWorkflowRole(t, super, tenantID, roleID, member, 0)
	policyA := seedApprovalPolicy(t, super, tenantID, "Policy A")
	seedApprovalDraftNamingRole(t, super, tenantID, policyA, "engagement-partner")
	policyB := seedApprovalPolicy(t, super, tenantID, "Policy B")
	seedApprovalDraftNamingRole(t, super, tenantID, policyB, "engagement-partner")

	inSweep := make(chan int, 1)
	release := make(chan struct{})
	var once, relOnce sync.Once
	releaseHold := func() { relOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseHold)
	holdA := func(ctx context.Context, tx pgx.Tx, invoiceID string) (string, error) {
		once.Do(func() {
			inSweep <- backendPID(t, ctx, tx)
			<-release
		})
		return stubFingerprintValue, nil
	}

	doneA := publishInBackground(NewStore(app, holdA, nil), c, policyA)
	holderPID := <-inSweep
	doneB := publishInBackground(NewStore(app, stubFingerprinter, nil), c, policyB)
	waitUntilBlockedBy(t, super, holderPID, doneB, "second PublishPolicy")
	releaseHold()

	if err := awaitPublish(t, doneA); err != nil {
		t.Errorf("first PublishPolicy: %v, want nil", err)
	}
	if err := awaitPublish(t, doneB); err != nil {
		t.Errorf("second PublishPolicy: %v, want nil", err)
	}
	var runs int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM approval_runs WHERE tenant_id = $1`, tenantID).Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runs != 1 {
		t.Errorf("approval_runs = %d, want exactly 1 for the one validated invoice", runs)
	}
}
