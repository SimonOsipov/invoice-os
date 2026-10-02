package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
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
	tenant, me, err := store.Me(ctx)
	role := me.Role
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

// refusedHistories are the membership histories that must stop a tenant-less caller provisioning.
var refusedHistories = []struct {
	name     string
	statuses []string
}{
	{"active", []string{"active"}},
	{"suspended", []string{"suspended"}},
	{"invited", []string{"invited"}},
	{"two active", []string{"active", "active"}},
}

// withHistory seeds the registrant's subject into one fresh tenant per status.
func withHistory(t *testing.T, r registrant, statuses []string) {
	t.Helper()
	for _, status := range statuses {
		seedMembership(t, r.super, seedTenant(t, r.super, "History "+status), r.id.Subject, "admin", status)
	}
}

func subjectMemberships(t *testing.T, r registrant) int {
	t.Helper()
	var n int
	if err := r.super.QueryRow(context.Background(), `SELECT count(*) FROM memberships WHERE user_id = $1`, r.id.Subject).Scan(&n); err != nil {
		t.Fatalf("count memberships: %v", err)
	}
	return n
}

func TestStoreProvisionWorkspace_RefusesAnyExistingMembership(t *testing.T) {
	for _, h := range refusedHistories {
		t.Run(h.name, func(t *testing.T) {
			r := newRegistrant(t)
			withHistory(t, r, h.statuses)
			before := subjectMemberships(t, r)
			if before != len(h.statuses) {
				t.Fatalf("seeded memberships = %d, want %d", before, len(h.statuses))
			}

			_, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Second Door", DisplayName: "Ada"})

			if !errors.Is(err, ErrAlreadyProvisioned) {
				t.Errorf("err = %v, want ErrAlreadyProvisioned", err)
			}
			if tenants, members := provisionedRows(t, r.super, r.tenantID); len(tenants) != 0 || len(members) != 0 {
				t.Errorf("workspace at uuidv5(subject) = tenants %v, memberships %+v, want none", tenants, members)
			}
			if after := subjectMemberships(t, r); after != before {
				t.Errorf("subject memberships %d -> %d, want unchanged", before, after)
			}
		})
	}
}

func TestProvisionHandler_ExistingMembershipIsTheSame409(t *testing.T) {
	const want = "this account already has a workspace"
	r := newRegistrant(t)
	withHistory(t, r, []string{"suspended"})
	store := NewStore(r.app)

	got := postProvision(store, r.ctx(), validProvisionBody)

	bearing := r.id
	bearing.TenantID = uuid.NewString()
	ref := postProvision(store, auth.WithIdentity(context.Background(), bearing), validProvisionBody)
	if ref.Code != http.StatusConflict {
		t.Fatalf("tenant-bearing reference answer = %d, want 409", ref.Code)
	}
	if got.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (body=%s)", got.Code, got.Body.String())
	}
	var body struct{ Error string }
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil || body.Error != want {
		t.Errorf("body = %q (decode err %v), want error %q", got.Body.String(), err, want)
	}
	if got.Body.String() != ref.Body.String() || got.Header().Get("Content-Type") != ref.Header().Get("Content-Type") {
		t.Errorf("answer %q (%s) differs from the tenant-bearing %q (%s)",
			got.Body.String(), got.Header().Get("Content-Type"), ref.Body.String(), ref.Header().Get("Content-Type"))
	}
	if tenants, _ := provisionedRows(t, r.super, r.tenantID); len(tenants) != 0 {
		t.Errorf("tenants at uuidv5(subject) = %v, want none", tenants)
	}
}

// tokenClaims is the hook's app_metadata for subject, read as the superuser (the function is DEFINER).
func tokenClaims(t *testing.T, r registrant) map[string]any {
	t.Helper()
	event, err := json.Marshal(map[string]any{
		"user_id": r.id.Subject,
		"claims":  map[string]any{"app_metadata": map[string]any{}},
	})
	if err != nil {
		t.Fatalf("marshal hook event: %v", err)
	}
	var out []byte
	if err := r.super.QueryRow(context.Background(), `SELECT public.custom_access_token_hook($1::jsonb)`, string(event)).Scan(&out); err != nil {
		t.Fatalf("call custom_access_token_hook: %v", err)
	}
	var decoded struct {
		Claims struct {
			AppMetadata map[string]any `json:"app_metadata"`
		} `json:"claims"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode hook output %s: %v", out, err)
	}
	return decoded.Claims.AppMetadata
}

func TestStoreProvisionWorkspace_EverySuccessReachesTheNextToken(t *testing.T) {
	t.Run("success projects the new tenant", func(t *testing.T) {
		r := newRegistrant(t)
		tenant, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Reachable", DisplayName: "Ada"})
		if err != nil {
			t.Fatalf("ProvisionWorkspace: %v", err)
		}
		if got := tokenClaims(t, r)["tenant_id"]; got != tenant.ID {
			t.Errorf("app_metadata.tenant_id = %v, want the new tenant %s", got, tenant.ID)
		}
	})

	for _, h := range refusedHistories {
		t.Run("refused "+h.name+" leaves the token as it was", func(t *testing.T) {
			r := newRegistrant(t)
			withHistory(t, r, h.statuses)
			before := tokenClaims(t, r)

			_, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Unreachable", DisplayName: "Ada"})

			if !errors.Is(err, ErrAlreadyProvisioned) {
				t.Errorf("err = %v, want ErrAlreadyProvisioned", err)
			}
			if after := tokenClaims(t, r); !reflect.DeepEqual(after, before) {
				t.Errorf("app_metadata %v -> %v, want identical", before, after)
			}
		})
	}
}
