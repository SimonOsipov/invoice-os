package db_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type grantedRow struct{ role, name, email, status string }

// grantTo grants through db.GrantMembership over the migrator DSN and removes the user's rows at cleanup.
func grantTo(t *testing.T, tenantID string, userID uuid.UUID, g grantedRow) error {
	t.Helper()
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM memberships WHERE user_id = $1`, userID)
	})
	return db.GrantMembership(context.Background(), os.Getenv("DATABASE_MIGRATION_URL"), db.MemberGrant{
		TenantID: uuid.MustParse(tenantID), UserID: userID, Role: g.role, DisplayName: g.name, Email: g.email,
	})
}

func readMemberships(t *testing.T, userID uuid.UUID) []grantedRow {
	t.Helper()
	rows, err := h.super.Query(context.Background(),
		`SELECT role, display_name, email, status FROM memberships WHERE user_id = $1 ORDER BY tenant_id`, userID)
	if err != nil {
		t.Fatalf("read memberships: %v", err)
	}
	defer rows.Close()
	var out []grantedRow
	for rows.Next() {
		var r grantedRow
		if err := rows.Scan(&r.role, &r.name, &r.email, &r.status); err != nil {
			t.Fatalf("scan membership: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read memberships: %v", err)
	}
	return out
}

func TestRLS_GrantMembershipInsertsActiveRow(t *testing.T) {
	h := requireHarness(t)
	user := uuid.New()
	want := grantedRow{"preparer", "Bola Adeyemi", "bola@example.test", "active"}
	if n := mustCount(t, h.super, `SELECT count(*) FROM memberships WHERE user_id = $1`, user); n != 0 {
		t.Fatalf("memberships for a fresh user = %d, want 0", n)
	}

	if err := grantTo(t, h.tenantA, user, grantedRow{want.role, want.name, want.email, ""}); err != nil {
		t.Fatalf("GrantMembership: %v", err)
	}

	got := readMemberships(t, user)
	if len(got) != 1 || got[0] != want {
		t.Errorf("memberships = %+v, want exactly [%+v]", got, want)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM memberships WHERE user_id = $1 AND tenant_id = $2`, user, h.tenantA); n != 1 {
		t.Errorf("rows in the granted tenant = %d, want 1", n)
	}
}

func TestRLS_GrantMembershipRepeatReactivates(t *testing.T) {
	h := requireHarness(t)
	user := uuid.New()
	if err := grantTo(t, h.tenantA, user, grantedRow{"admin", "First Name", "first@example.test", ""}); err != nil {
		t.Fatalf("first GrantMembership: %v", err)
	}
	if _, err := h.super.Exec(context.Background(),
		`UPDATE memberships SET status = 'suspended' WHERE user_id = $1`, user); err != nil {
		t.Fatalf("suspend the membership: %v", err)
	}
	if got := readMemberships(t, user); len(got) != 1 || got[0].status != "suspended" {
		t.Fatalf("memberships after the suspension = %+v, want one suspended row", got)
	}

	if err := grantTo(t, h.tenantA, user, grantedRow{"reviewer", "Second Name", "second@example.test", ""}); err != nil {
		t.Fatalf("repeat GrantMembership: %v", err)
	}

	want := grantedRow{"reviewer", "Second Name", "second@example.test", "active"}
	if got := readMemberships(t, user); len(got) != 1 || got[0] != want {
		t.Errorf("memberships after the repeat = %+v, want exactly [%+v]", got, want)
	}
}

func TestRLS_GrantMembershipHookProjectsTenant(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)
	user := uuid.New()
	if err := grantTo(t, h.tenantA, user, grantedRow{"admin", "Hook User", "hook@example.test", ""}); err != nil {
		t.Fatalf("GrantMembership: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM memberships WHERE user_id = $1`, user); n != 1 {
		t.Fatalf("memberships for the user = %d, want exactly the granted one", n)
	}

	md := appMetadataOf(t, mustHookClaims(t, auth, user.String()))

	if md["tenant_id"] != h.tenantA {
		t.Errorf("app_metadata.tenant_id = %v, want the granted %s: %v", md["tenant_id"], h.tenantA, md)
	}
}

func TestRLS_GrantMembershipSecondTenantStripsClaim(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)
	user := uuid.New()
	for _, tenant := range []string{h.tenantA, h.tenantB} {
		if err := grantTo(t, tenant, user, grantedRow{"admin", "Two Tenants", "two@example.test", ""}); err != nil {
			t.Fatalf("GrantMembership into %s: %v", tenant, err)
		}
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM memberships WHERE user_id = $1 AND status = 'active'`, user); n != 2 {
		t.Fatalf("active memberships for the user = %d, want 2, one per tenant", n)
	}

	md := appMetadataOf(t, mustHookClaims(t, auth, user.String()))

	if v, has := md["tenant_id"]; has {
		t.Errorf("app_metadata.tenant_id = %v with two active memberships, want absent", v)
	}
}

func TestRLS_GrantMembershipUnknownTenant(t *testing.T) {
	h := requireHarness(t)
	user := uuid.New()
	unknown := uuid.NewString()
	if n := mustCount(t, h.super, `SELECT count(*) FROM tenants WHERE id = $1`, unknown); n != 0 {
		t.Fatalf("tenant %s exists, want it unknown", unknown)
	}

	if err := grantTo(t, unknown, user, grantedRow{"admin", "Nobody", "nobody@example.test", ""}); err == nil {
		t.Error("GrantMembership into an unknown tenant returned no error")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM memberships WHERE user_id = $1`, user); n != 0 {
		t.Errorf("memberships for the user after the refused grant = %d, want 0", n)
	}

	// Positive control: the same call succeeds for a tenant that exists.
	if err := grantTo(t, h.tenantA, user, grantedRow{"admin", "Somebody", "somebody@example.test", ""}); err != nil {
		t.Fatalf("control: GrantMembership into a known tenant: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM memberships WHERE user_id = $1`, user); n != 1 {
		t.Errorf("control: memberships for the user = %d, want 1", n)
	}
}

type memberSnapshot struct{ id, role, name, email, status string }

// snapshotMember reads one (tenant, user) row, id included, so a rewritten row shows.
func snapshotMember(t *testing.T, tenantID string, userID uuid.UUID) memberSnapshot {
	t.Helper()
	var s memberSnapshot
	if err := h.super.QueryRow(context.Background(),
		`SELECT id::text, role, display_name, email, status FROM memberships WHERE tenant_id = $1 AND user_id = $2`,
		tenantID, userID).Scan(&s.id, &s.role, &s.name, &s.email, &s.status); err != nil {
		t.Fatalf("read the membership of %s in %s: %v", userID, tenantID, err)
	}
	return s
}

func TestRLS_GrantMembershipUpsertTouchesOnlyItsOwnRow(t *testing.T) {
	h := requireHarness(t)
	subject, bystander := uuid.New(), uuid.New()
	for _, g := range []struct {
		tenant string
		user   uuid.UUID
		row    grantedRow
	}{
		{h.tenantA, subject, grantedRow{"admin", "Subject In A", "subject-a@example.test", ""}},
		{h.tenantB, subject, grantedRow{"reviewer", "Subject In B", "subject-b@example.test", ""}},
		{h.tenantA, bystander, grantedRow{"preparer", "Bystander In A", "bystander@example.test", ""}},
	} {
		if err := grantTo(t, g.tenant, g.user, g.row); err != nil {
			t.Fatalf("seed GrantMembership into %s: %v", g.tenant, err)
		}
	}
	// Suspended rows show a stray status = 'active' write as well as a stray role or name.
	if _, err := h.super.Exec(context.Background(),
		`UPDATE memberships SET status = 'suspended' WHERE (tenant_id = $1 AND user_id = $2) OR (tenant_id = $3 AND user_id = $4)`,
		h.tenantB, subject, h.tenantA, bystander); err != nil {
		t.Fatalf("suspend the other rows: %v", err)
	}
	subjectInA := snapshotMember(t, h.tenantA, subject)
	subjectInB := snapshotMember(t, h.tenantB, subject)
	bystanderInA := snapshotMember(t, h.tenantA, bystander)
	if subjectInB.status != "suspended" || bystanderInA.status != "suspended" || subjectInA.status != "active" {
		t.Fatalf("setup statuses = %q/%q/%q, want the two other rows suspended and the subject's A row active",
			subjectInB.status, bystanderInA.status, subjectInA.status)
	}

	if err := grantTo(t, h.tenantA, subject, grantedRow{"reviewer", "Subject Renamed", "renamed@example.test", ""}); err != nil {
		t.Fatalf("repeat GrantMembership: %v", err)
	}

	want := memberSnapshot{subjectInA.id, "reviewer", "Subject Renamed", "renamed@example.test", "active"}
	if got := snapshotMember(t, h.tenantA, subject); got != want {
		t.Errorf("the granted row = %+v, want %+v (same id: updated in place)", got, want)
	}
	if got := snapshotMember(t, h.tenantB, subject); got != subjectInB {
		t.Errorf("the same subject's row in the other tenant = %+v, want unchanged %+v", got, subjectInB)
	}
	if got := snapshotMember(t, h.tenantA, bystander); got != bystanderInA {
		t.Errorf("another user's row in the granted tenant = %+v, want unchanged %+v", got, bystanderInA)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM memberships WHERE user_id = $1`, subject); n != 2 {
		t.Errorf("rows for the subject = %d, want 2, one per tenant", n)
	}
}

func TestRLS_GrantMembershipUnknownRoleWritesNothing(t *testing.T) {
	h := requireHarness(t)
	user := uuid.New()

	if err := grantTo(t, h.tenantA, user, grantedRow{"owner", "Not A Role", "owner@example.test", ""}); err == nil {
		t.Error("GrantMembership with a role outside roles(name) returned no error")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM memberships WHERE user_id = $1`, user); n != 0 {
		t.Errorf("memberships after the refused grant = %d, want 0", n)
	}

	if err := grantTo(t, h.tenantA, user, grantedRow{"reviewer", "A Role", "role@example.test", ""}); err != nil {
		t.Fatalf("control: GrantMembership with a real role: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM memberships WHERE user_id = $1`, user); n != 1 {
		t.Errorf("control: memberships = %d, want 1", n)
	}
}

func TestRLS_GrantMembershipReactivationRestoresTheProjection(t *testing.T) {
	h := requireHarness(t)
	auth := authAdminPool(t)
	user := uuid.New()
	grant := func() {
		t.Helper()
		if err := grantTo(t, h.tenantA, user, grantedRow{"admin", "Back Again", "back@example.test", ""}); err != nil {
			t.Fatalf("GrantMembership: %v", err)
		}
	}
	grant()
	if md := appMetadataOf(t, mustHookClaims(t, auth, user.String())); md["tenant_id"] != h.tenantA {
		t.Fatalf("app_metadata.tenant_id after the grant = %v, want %s", md["tenant_id"], h.tenantA)
	}
	if _, err := h.super.Exec(context.Background(), `UPDATE memberships SET status = 'suspended' WHERE user_id = $1`, user); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if md := appMetadataOf(t, mustHookClaims(t, auth, user.String())); md["tenant_id"] != nil {
		t.Fatalf("app_metadata.tenant_id while suspended = %v, want absent, so the projection below proves the repeat grant", md["tenant_id"])
	}

	grant()

	if md := appMetadataOf(t, mustHookClaims(t, auth, user.String())); md["tenant_id"] != h.tenantA {
		t.Errorf("app_metadata.tenant_id after the repeat grant = %v, want %s", md["tenant_id"], h.tenantA)
	}
}
