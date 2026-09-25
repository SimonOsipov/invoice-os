package tenancy

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// registrant is a fresh, unmembered identity and the workspace id it must get.
type registrant struct {
	super, app *pgxpool.Pool
	id         auth.Identity
	tenantID   string // uuidv5(workspaceNamespace, subject)
}

// newRegistrant is the one source of fresh registrants; it deletes their workspace after the test.
func newRegistrant(t *testing.T) registrant {
	t.Helper()
	super, app := dbTestPools(t)
	subject := uuid.NewString()
	r := registrant{
		super:    super,
		app:      app,
		id:       auth.Identity{Subject: subject, Role: "authenticated", Email: "registrant-" + subject[:8] + "@example.com"},
		tenantID: uuid.NewSHA1(workspaceNamespace, []byte(subject)).String(),
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, r.tenantID)
	})
	return r
}

func (r registrant) ctx() context.Context {
	return auth.WithTenantlessCaller(context.Background(), r.id)
}

type provisionedMember struct {
	UserID, Role, Status string
	DisplayName, Email   *string
}

// provisionedRows reads a workspace as the superuser: tenant rows (name, kind) and its memberships.
func provisionedRows(t *testing.T, super *pgxpool.Pool, tenantID string) (tenants [][2]string, members []provisionedMember) {
	t.Helper()
	ctx := context.Background()
	rows, err := super.Query(ctx, `SELECT name, kind FROM tenants WHERE id = $1`, tenantID)
	if err != nil {
		t.Fatalf("read tenants: %v", err)
	}
	for rows.Next() {
		var nk [2]string
		if err := rows.Scan(&nk[0], &nk[1]); err != nil {
			t.Fatalf("scan tenant: %v", err)
		}
		tenants = append(tenants, nk)
	}
	rows.Close()
	rows, err = super.Query(ctx,
		`SELECT user_id::text, role, status, display_name, email FROM memberships WHERE tenant_id = $1`, tenantID)
	if err != nil {
		t.Fatalf("read memberships: %v", err)
	}
	for rows.Next() {
		var m provisionedMember
		if err := rows.Scan(&m.UserID, &m.Role, &m.Status, &m.DisplayName, &m.Email); err != nil {
			t.Fatalf("scan membership: %v", err)
		}
		members = append(members, m)
	}
	rows.Close()
	return tenants, members
}

func TestStoreProvisionWorkspace_CreatesTenantAndActiveAdmin(t *testing.T) {
	r := newRegistrant(t)
	in := ProvisionInput{WorkspaceName: "Registrant Works Ltd", DisplayName: "Ada Registrant"}

	tenant, subject, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), in)
	if err != nil {
		t.Errorf("ProvisionWorkspace: %v", err)
	}
	if tenant.ID != r.tenantID || tenant.Name != in.WorkspaceName || tenant.Kind != "firm" {
		t.Errorf("returned tenant = %+v, want {ID:%s Name:%s Kind:firm}", tenant, r.tenantID, in.WorkspaceName)
	}
	if subject != r.id.Subject {
		t.Errorf("returned subject = %q, want %q", subject, r.id.Subject)
	}

	tenants, members := provisionedRows(t, r.super, r.tenantID)
	if len(tenants) != 1 || tenants[0] != [2]string{in.WorkspaceName, "firm"} {
		t.Fatalf("tenants at uuidv5(subject) %s = %v, want exactly [{%s firm}]", r.tenantID, tenants, in.WorkspaceName)
	}
	if len(members) != 1 {
		t.Fatalf("memberships = %+v, want exactly one", members)
	}
	m := members[0]
	if m.UserID != r.id.Subject || m.Role != "admin" || m.Status != "active" {
		t.Errorf("membership = {%s %s %s}, want {%s admin active}", m.UserID, m.Role, m.Status, r.id.Subject)
	}
	if m.DisplayName == nil || *m.DisplayName != in.DisplayName {
		t.Errorf("display_name = %v, want %q", m.DisplayName, in.DisplayName)
	}
	if m.Email == nil || *m.Email != r.id.Email {
		t.Errorf("email = %v, want the caller's %q", m.Email, r.id.Email)
	}
}

func TestStoreProvisionWorkspace_KindDefaultsToFirm(t *testing.T) {
	r := newRegistrant(t)
	if _, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Default Kind", DisplayName: "Ada"}); err != nil {
		t.Errorf("ProvisionWorkspace: %v", err)
	}
	tenants, _ := provisionedRows(t, r.super, r.tenantID)
	if len(tenants) != 1 || tenants[0][1] != "firm" {
		t.Errorf("tenants = %v, want one row with kind firm", tenants)
	}
}

func TestStoreProvisionWorkspace_KeepsInHouse(t *testing.T) {
	r := newRegistrant(t)
	if _, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "In House", DisplayName: "Ada", Kind: "in_house"}); err != nil {
		t.Errorf("ProvisionWorkspace: %v", err)
	}
	tenants, _ := provisionedRows(t, r.super, r.tenantID)
	if len(tenants) != 1 || tenants[0][1] != "in_house" {
		t.Errorf("tenants = %v, want one row with kind in_house", tenants)
	}
}

func TestStoreProvisionWorkspace_NullEmailWhenAbsent(t *testing.T) {
	r := newRegistrant(t)
	r.id.Email = ""
	if _, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "No Email", DisplayName: "Ada"}); err != nil {
		t.Errorf("ProvisionWorkspace: %v", err)
	}
	_, members := provisionedRows(t, r.super, r.tenantID)
	if len(members) != 1 {
		t.Fatalf("memberships = %+v, want exactly one", members)
	}
	if members[0].Email != nil {
		t.Errorf("email = %q, want NULL for a caller with no email", *members[0].Email)
	}
}

func TestStoreProvisionWorkspace_SecondCallIsAlreadyProvisioned(t *testing.T) {
	r := newRegistrant(t)
	ctx := context.Background()
	// The first workspace comes straight from the function, so this red is the second call's.
	if err := db.WithinTenantTx(ctx, r.super, r.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT public.provision_workspace($1, 'First Works', NULL, $2, 'Ada', NULL)`, r.tenantID, r.id.Subject)
		return err
	}); err != nil {
		t.Fatalf("seed first workspace: %v", err)
	}

	_, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Second Works", DisplayName: "Bob"})
	if !errors.Is(err, ErrAlreadyProvisioned) {
		t.Errorf("second ProvisionWorkspace err = %v, want ErrAlreadyProvisioned", err)
	}
	tenants, members := provisionedRows(t, r.super, r.tenantID)
	if len(tenants) != 1 || tenants[0][0] != "First Works" {
		t.Errorf("tenants = %v, want only the first workspace", tenants)
	}
	if len(members) != 1 || members[0].DisplayName == nil || *members[0].DisplayName != "Ada" {
		t.Errorf("memberships = %+v, want only the first admin", members)
	}
}

func TestStoreProvisionWorkspace_RefusesATenantBearingIdentity(t *testing.T) {
	r := newRegistrant(t)
	id := r.id
	id.TenantID = uuid.NewString()
	ctx := auth.WithIdentity(context.Background(), id)

	_, _, err := NewStore(r.app).ProvisionWorkspace(ctx, ProvisionInput{WorkspaceName: "Has Tenant", DisplayName: "Ada"})
	if !errors.Is(err, ErrAlreadyProvisioned) {
		t.Errorf("err = %v, want ErrAlreadyProvisioned", err)
	}
	if tenants, _ := provisionedRows(t, r.super, r.tenantID); len(tenants) != 0 {
		t.Errorf("tenants at uuidv5(subject) = %v, want none", tenants)
	}
}

func TestStoreProvisionWorkspace_RefusesNoCaller(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	in := ProvisionInput{WorkspaceName: "Nobody", DisplayName: "Ada"}

	t.Run("no caller", func(t *testing.T) {
		before := tenantCount(t, super)
		_, _, err := store.ProvisionWorkspace(context.Background(), in)
		if !errors.Is(err, db.ErrNoTenant) {
			t.Errorf("err = %v, want db.ErrNoTenant", err)
		}
		if after := tenantCount(t, super); after != before {
			t.Errorf("tenants count %d -> %d, want unchanged", before, after)
		}
	})
	t.Run("non-UUID subject", func(t *testing.T) {
		const subject = "not-a-uuid"
		wouldBe := uuid.NewSHA1(workspaceNamespace, []byte(subject)).String()
		t.Cleanup(func() {
			_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, wouldBe)
		})
		ctx := auth.WithTenantlessCaller(context.Background(), auth.Identity{Subject: subject, Role: "authenticated"})
		_, _, err := store.ProvisionWorkspace(ctx, in)
		if !errors.Is(err, db.ErrNoTenant) {
			t.Errorf("err = %v, want db.ErrNoTenant", err)
		}
		if tenants, _ := provisionedRows(t, super, wouldBe); len(tenants) != 0 {
			t.Errorf("tenants at uuidv5(%q) = %v, want none", subject, tenants)
		}
	})
}

func TestStoreMe_AnswersForAProvisionedWorkspace(t *testing.T) {
	r := newRegistrant(t)
	store := NewStore(r.app)
	if _, _, err := store.ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Me Works", DisplayName: "Ada", Kind: "in_house"}); err != nil {
		t.Errorf("ProvisionWorkspace: %v", err)
	}

	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: r.id.Subject, Role: "authenticated", TenantID: r.tenantID})
	tenant, role, err := store.Me(ctx)
	if err != nil {
		t.Fatalf("Me for the provisioned workspace: %v", err)
	}
	if tenant.ID != r.tenantID || tenant.Name != "Me Works" || tenant.Kind != "in_house" {
		t.Errorf("Me tenant = %+v, want {%s Me Works in_house}", tenant, r.tenantID)
	}
	if role != "admin" {
		t.Errorf("Me role = %q, want admin", role)
	}
}

func tenantCount(t *testing.T, super *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(), `SELECT count(*) FROM tenants`).Scan(&n); err != nil {
		t.Fatalf("count tenants: %v", err)
	}
	return n
}
