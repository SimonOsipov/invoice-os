// QA adversarial coverage for APPR-15-04: ties subtask 03's callerRoleTx
// predicate (status = 'active') to subtask 04's REAL curated seed data,
// rather than a synthetic uuid.NewString() membership
// (TestStore_ResolveOutside_SuspendedApproverRefused already covers the
// synthetic case). Confirms the actual db/seed.dev.sql suspended reviewer
// c0000000-...-0007 cannot ResolveOutside a real seeded failed invoice.
package invoice

import (
	"context"
	"errors"
	"os"
	"testing"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func TestStore_ResolveOutside_SeededSuspendedReviewerRefused(t *testing.T) {
	super, app := dbTestPools(t)
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	ctx := context.Background()

	if err := db.Seed(ctx, superURL, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	const demoTenant = "11111111-1111-1111-1111-111111111111"
	const suspendedSubject = "c0000000-0000-0000-0000-000000000007" // curated: Halima Yusuf, reviewer, suspended

	var role, status string
	if err := super.QueryRow(ctx,
		`SELECT role, status FROM memberships WHERE tenant_id = $1 AND user_id = $2`,
		demoTenant, suspendedSubject,
	).Scan(&role, &status); err != nil {
		t.Fatalf("precondition: read seeded membership: %v", err)
	}
	if role != "reviewer" || status != "suspended" {
		t.Fatalf("precondition: seeded membership = (role=%q, status=%q), want (reviewer, suspended)", role, status)
	}

	var invID string
	if err := super.QueryRow(ctx,
		`SELECT id FROM invoices WHERE tenant_id = $1 AND invoice_number = 'DEMO-2026-1004'`, demoTenant,
	).Scan(&invID); err != nil {
		t.Fatalf("precondition: find seeded failed invoice DEMO-2026-1004: %v", err)
	}

	c := auth.WithIdentity(ctx, auth.Identity{Subject: suspendedSubject, Role: "authenticated", TenantID: demoTenant})
	store := NewStore(app)
	// The request seam refuses a non-active caller before the store reads anything
	// (db.WithinRequestTenantTxOpts).
	if _, err := store.ResolveOutside(c, invID, "qa-adversarial"); !errors.Is(err, db.ErrNotActiveMember) {
		t.Fatalf("ResolveOutside (seeded suspended reviewer) err = %v, want db.ErrNotActiveMember", err)
	}

	at, by, reason := mustKeptAsIsTriple(t, super, invID)
	if at != nil || by != nil || reason != nil {
		t.Errorf("kept_as_is triple after refused seeded-suspended ResolveOutside = (%v,%v,%v), want all NULL", at, by, reason)
	}
}
