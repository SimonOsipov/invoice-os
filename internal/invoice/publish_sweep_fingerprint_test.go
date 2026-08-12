// task-484 (APPR-06-08, Mode A): the one publish-sweep fingerprint test that must run
// from this package. internal/approval cannot import internal/invoice ("import cycle
// not allowed in test", proven against every one of its 23 package-approval test
// files), so every approval-package sweep test builds its Store with a local
// stubFingerprinter and this file is where the REAL invoice.FingerprintTx is
// exercised. Ported fixtures: apply_validation_arming_test.go set this precedent for
// the same import direction (internal/invoice -> internal/approval).
package invoice

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// seedActiveAdminFor seeds an active admin membership and returns a context carrying
// its identity. approval.PublishPolicy's requireActiveAdmin gate needs one; this
// package's own fixtures have no admin concept, ApplyValidation needs none.
func seedActiveAdminFor(t *testing.T, super *pgxpool.Pool, tenantID string) context.Context {
	t.Helper()
	userID := uuid.NewString()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'admin', 'active')`,
		tenantID, userID); err != nil {
		t.Fatalf("seed active admin membership: %v", err)
	}
	return auth.WithIdentity(context.Background(),
		auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID})
}

// TestPublish_SweepFingerprintMatchesInvoiceContent (task-484 AC-1): a validated
// invoice with no approval_runs row -> approval.NewStore(app, FingerprintTx).
// PublishPolicy arms it -> the armed run's content_fingerprint equals FingerprintTx
// computed independently, in its own transaction. Fails today: undefined:
// FingerprintTx.
func TestPublish_SweepFingerprintMatchesInvoiceContent(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-06-08 sweep-fingerprint tenant")
	entityID := seedEntity(t, super, tenantID, "APPR-06-08 sweep-fingerprint entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	store := NewStore(app)
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "sweep-fp-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ruleSetVersionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv, inv.LineItems)
	if _, err := store.ApplyValidation(c, inv.ID, []Violation{}, ruleSetVersionID, fp); err != nil {
		t.Fatalf("ApplyValidation (clean, no active policy yet): %v, want success", err)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1`, inv.ID); n != 0 {
		t.Fatalf("approval_runs rows before publish = %d, want 0 -- no active policy exists yet, so "+
			"ApplyValidation's own arming must be a no-op", n)
	}

	adminCtx := seedActiveAdminFor(t, super, tenantID)
	policyID := seedApprovalPolicyFor(t, super, tenantID, "APPR-06-08 sweep-fingerprint policy")
	seedApprovalPolicyVersionFor(t, super, tenantID, policyID) // zero steps -- the smallest publishable tree

	if _, err := approval.NewStore(app, FingerprintTx, nil).PublishPolicy(adminCtx, policyID); err != nil {
		t.Fatalf("PublishPolicy: %v, want success", err)
	}

	var stored string
	if err := super.QueryRow(ctx,
		`SELECT content_fingerprint FROM approval_runs WHERE invoice_id = $1`, inv.ID,
	).Scan(&stored); err != nil {
		t.Fatalf("read approval_runs.content_fingerprint: %v", err)
	}
	if stored == "" {
		t.Fatal(`approval_runs.content_fingerprint = "", want a real hash`)
	}

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin independent tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	want, err := FingerprintTx(ctx, tx, inv.ID)
	if err != nil {
		t.Fatalf("FingerprintTx: %v", err)
	}

	if stored != want {
		t.Errorf("approval_runs.content_fingerprint = %q, want FingerprintTx's independently computed %q",
			stored, want)
	}
}
