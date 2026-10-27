package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
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
	if tenant.ID != r.tenantID || tenant.Name != in.WorkspaceName || tenant.Kind != "in_house" {
		t.Errorf("returned tenant = %+v, want {ID:%s Name:%s Kind:in_house}", tenant, r.tenantID, in.WorkspaceName)
	}
	if subject != r.id.Subject {
		t.Errorf("returned subject = %q, want %q", subject, r.id.Subject)
	}

	tenants, members := provisionedRows(t, r.super, r.tenantID)
	if len(tenants) != 1 || tenants[0] != [2]string{in.WorkspaceName, "in_house"} {
		t.Fatalf("tenants at uuidv5(subject) %s = %v, want exactly [{%s in_house}]", r.tenantID, tenants, in.WorkspaceName)
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

func TestStoreProvisionWorkspace_AbsentKindIsInHouse(t *testing.T) {
	r := newRegistrant(t)
	tenant, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Default Kind", DisplayName: "Ada"})
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	if tenant.Kind != "in_house" {
		t.Errorf("returned kind = %q, want in_house", tenant.Kind)
	}
	tenants, _ := provisionedRows(t, r.super, r.tenantID)
	if len(tenants) != 1 || tenants[0][1] != "in_house" {
		t.Errorf("tenants = %v, want one row with kind in_house", tenants)
	}
}

func TestStoreProvisionWorkspace_ExplicitKindIsKept(t *testing.T) {
	for _, kind := range []string{"firm", "in_house"} {
		t.Run(kind, func(t *testing.T) {
			r := newRegistrant(t)
			tenant, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Explicit " + kind, DisplayName: "Ada", Kind: kind})
			if err != nil {
				t.Fatalf("ProvisionWorkspace: %v", err)
			}
			if tenant.Kind != kind {
				t.Errorf("returned kind = %q, want %q", tenant.Kind, kind)
			}
			tenants, _ := provisionedRows(t, r.super, r.tenantID)
			if len(tenants) != 1 || tenants[0][1] != kind {
				t.Errorf("tenants = %v, want one row with kind %s", tenants, kind)
			}
		})
	}
}

func TestStoreProvisionWorkspace_AbsentKindLeavesOtherTenantsAlone(t *testing.T) {
	r := newRegistrant(t)
	ctx := context.Background()
	seededID := uuid.NewString()
	if _, err := r.super.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'Seeded Without Kind')`, seededID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = r.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, seededID) })
	kindOf := func() string {
		var k string
		if err := r.super.QueryRow(ctx, `SELECT kind FROM tenants WHERE id = $1`, seededID).Scan(&k); err != nil {
			t.Fatalf("read seeded kind: %v", err)
		}
		return k
	}
	if k := kindOf(); k != "firm" {
		t.Fatalf("seeded kind before = %q, want the column default firm", k)
	}

	if _, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Newcomer", DisplayName: "Ada"}); err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	if k := kindOf(); k != "firm" {
		t.Errorf("seeded kind after = %q, want firm", k)
	}
}

func TestProvisionHandler_AbsentKindAnswersInHouse(t *testing.T) {
	r := newRegistrant(t)
	rec := postProvision(NewStore(r.app), r.ctx(), `{"workspace_name":"Acme","display_name":"Ada"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	var me meBody
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if me.Tenant.Kind != "in_house" {
		t.Errorf("tenant.kind = %q, want in_house (body=%s)", me.Tenant.Kind, rec.Body.String())
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

// With the guard in front, a tenant row at the workspace id with no membership reaches tenants_pkey.
func TestStoreProvisionWorkspace_MemberlessTenantAtTheWorkspaceIDIsAlreadyProvisioned(t *testing.T) {
	r := newRegistrant(t)
	if _, err := r.super.Exec(context.Background(), `INSERT INTO tenants (id, name) VALUES ($1, 'Orphan Works')`, r.tenantID); err != nil {
		t.Fatalf("seed memberless tenant: %v", err)
	}
	if n := subjectMemberships(t, r); n != 0 {
		t.Fatalf("subject memberships = %d, want 0 so the guard cannot be the one that refuses", n)
	}

	_, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Second Door", DisplayName: "Ada"})

	if !errors.Is(err, ErrAlreadyProvisioned) {
		t.Errorf("err = %v, want ErrAlreadyProvisioned", err)
	}
	tenants, members := provisionedRows(t, r.super, r.tenantID)
	if len(tenants) != 1 || tenants[0][0] != "Orphan Works" || len(members) != 0 {
		t.Errorf("tenants = %v, memberships = %+v, want only the seeded tenant", tenants, members)
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

// provisionedEvent is one workspace.provisioned audit row, read as the superuser.
type provisionedEvent struct {
	Actor    string
	EntityID *string
	Payload  map[string]any
}

func provisionedEvents(t *testing.T, super *pgxpool.Pool, tenantID string) []provisionedEvent {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT actor, entity_id::text, payload FROM audit_log WHERE tenant_id = $1 AND event = 'workspace.provisioned'`, tenantID)
	if err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	defer rows.Close()
	var out []provisionedEvent
	for rows.Next() {
		var e provisionedEvent
		var raw []byte
		if err := rows.Scan(&e.Actor, &e.EntityID, &raw); err != nil {
			t.Fatalf("scan audit row: %v", err)
		}
		if err := json.Unmarshal(raw, &e.Payload); err != nil {
			t.Fatalf("decode payload %s: %v", raw, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	return out
}

// failAuditWrites makes every audit_log insert for this registrant's workspace raise; no other test sees it.
func failAuditWrites(t *testing.T, r registrant) {
	t.Helper()
	ctx := context.Background()
	name := "prov_audit_fail_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := r.super.Exec(ctx, fmt.Sprintf(
		`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN RAISE EXCEPTION 'forced audit failure' USING ERRCODE = 'check_violation'; END; $$`, name)); err != nil {
		t.Fatalf("create the forced-failure function: %v", err)
	}
	t.Cleanup(func() {
		_, _ = r.super.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s() CASCADE`, name))
	})
	if _, err := r.super.Exec(ctx, fmt.Sprintf(
		`CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW
		 WHEN (NEW.tenant_id = %s::uuid) EXECUTE FUNCTION %s()`, name, quoteLiteral(r.tenantID), name)); err != nil {
		t.Fatalf("create the forced-failure trigger: %v", err)
	}
}

func TestStoreProvisionWorkspace_WritesOneProvisionedEvent(t *testing.T) {
	r := newRegistrant(t)
	in := ProvisionInput{WorkspaceName: "Audited Works", DisplayName: "Ada Registrant"}
	if _, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), in); err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}

	events := provisionedEvents(t, r.super, r.tenantID)
	if len(events) != 1 {
		t.Fatalf("workspace.provisioned rows at %s = %d, want exactly 1", r.tenantID, len(events))
	}
	e := events[0]
	if e.Actor != r.id.Subject {
		t.Errorf("actor = %q, want the caller's subject %q", e.Actor, r.id.Subject)
	}
	if e.EntityID != nil {
		t.Errorf("entity_id = %q, want NULL", *e.EntityID)
	}
	// kind is what the store resolved for an absent kind, not an input echo.
	want := map[string]any{"tenant_id": r.tenantID, "user_id": r.id.Subject, "name": in.WorkspaceName, "kind": "in_house"}
	if !reflect.DeepEqual(e.Payload, want) {
		t.Errorf("payload = %v, want exactly %v", e.Payload, want)
	}
}

func TestStoreProvisionWorkspace_AuditSharesTheTransaction(t *testing.T) {
	r := newRegistrant(t)
	if _, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "One Tx", DisplayName: "Ada"}); err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	ctx := context.Background()
	var tenantXmin string
	if err := r.super.QueryRow(ctx, `SELECT xmin::text FROM tenants WHERE id = $1`, r.tenantID).Scan(&tenantXmin); err != nil {
		t.Fatalf("read tenant xmin: %v", err)
	}
	var auditXmin string
	if err := r.super.QueryRow(ctx,
		`SELECT xmin::text FROM audit_log WHERE tenant_id = $1 AND event = 'workspace.provisioned'`, r.tenantID).Scan(&auditXmin); err != nil {
		t.Fatalf("read the audit row's xmin (no workspace.provisioned row?): %v", err)
	}
	if auditXmin != tenantXmin {
		t.Errorf("audit xmin = %s, tenant xmin = %s, want one transaction", auditXmin, tenantXmin)
	}
}

func TestStoreProvisionWorkspace_AuditFailureCreatesNothing(t *testing.T) {
	r := newRegistrant(t)
	failAuditWrites(t, r)

	_, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "No Trail", DisplayName: "Ada"})

	if err == nil {
		t.Error("ProvisionWorkspace succeeded with a failing audit write, want an error")
	}
	if errors.Is(err, ErrAlreadyProvisioned) {
		t.Errorf("err = %v, want a failure that is not ErrAlreadyProvisioned", err)
	}
	if tenants, members := provisionedRows(t, r.super, r.tenantID); len(tenants) != 0 || len(members) != 0 {
		t.Errorf("after a failed audit: tenants %v, memberships %+v, want none", tenants, members)
	}
}

func TestProvisionHandler_AuditFailureIs500(t *testing.T) {
	r := newRegistrant(t)
	failAuditWrites(t, r)

	rec := postProvision(NewStore(r.app), r.ctx(), validProvisionBody)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (body=%s)", rec.Code, rec.Body.String())
	}
	var body struct{ Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error != "internal server error" {
		t.Errorf("body = %q (decode err %v), want error %q", rec.Body.String(), err, "internal server error")
	}
	if tenants, _ := provisionedRows(t, r.super, r.tenantID); len(tenants) != 0 {
		t.Errorf("tenants at uuidv5(subject) = %v, want none", tenants)
	}
}

func TestStoreProvisionWorkspace_RefusedCallWritesNoEvent(t *testing.T) {
	r := newRegistrant(t)
	withHistory(t, r, []string{"suspended"})

	// Control: a successful call is visible to the same query, so a zero below means refused.
	ok := newRegistrant(t)
	if _, _, err := NewStore(ok.app).ProvisionWorkspace(ok.ctx(), ProvisionInput{WorkspaceName: "Control Works", DisplayName: "Ada"}); err != nil {
		t.Fatalf("control ProvisionWorkspace: %v", err)
	}
	if n := len(provisionedEvents(t, ok.super, ok.tenantID)); n != 1 {
		t.Fatalf("control: workspace.provisioned rows = %d, want 1", n)
	}

	_, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Refused Works", DisplayName: "Ada"})
	if !errors.Is(err, ErrAlreadyProvisioned) {
		t.Fatalf("err = %v, want ErrAlreadyProvisioned", err)
	}

	var n int
	if err := r.super.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE event = 'workspace.provisioned' AND actor = $1`, r.id.Subject).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if n != 0 {
		t.Errorf("workspace.provisioned rows for the refused subject = %d, want 0", n)
	}
}

func TestAuditRead_NewAdminReadsTheProvisionedEvent(t *testing.T) {
	r := newRegistrant(t)
	if _, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Readable Works", DisplayName: "Ada Admin"}); err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}

	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: r.id.Subject, Role: "authenticated", TenantID: r.tenantID})
	resp, err := audit.NewStore(r.app).List(ctx, audit.Filter{Events: []string{"workspace.provisioned"}, Limit: 10})
	if err != nil {
		t.Fatalf("audit List as the new admin: %v", err)
	}

	if len(resp.Events) != 1 {
		t.Fatalf("events read = %d, want exactly 1 workspace.provisioned", len(resp.Events))
	}
	e := resp.Events[0]
	if e.CompanyScope != audit.ScopeWorkspace {
		t.Errorf("company_scope = %q, want %q", e.CompanyScope, audit.ScopeWorkspace)
	}
	if e.ActorName != "Ada Admin" || e.ActorKind != "person" {
		t.Errorf("actor = {%q %q}, want {Ada Admin person}", e.ActorName, e.ActorKind)
	}
}

func TestStoreProvisionWorkspace_EventCarriesTheStoredKindAndName(t *testing.T) {
	r := newRegistrant(t)
	in := ProvisionInput{WorkspaceName: "In House Works", Kind: "in_house", DisplayName: "Zed Distinct"}
	if _, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), in); err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}

	events := provisionedEvents(t, r.super, r.tenantID)
	if len(events) != 1 {
		t.Fatalf("workspace.provisioned rows = %d, want 1", len(events))
	}
	want := map[string]any{"tenant_id": r.tenantID, "user_id": r.id.Subject, "name": in.WorkspaceName, "kind": "in_house"}
	if !reflect.DeepEqual(events[0].Payload, want) {
		t.Errorf("payload = %v, want exactly %v (no display name, no email)", events[0].Payload, want)
	}
	for k, v := range events[0].Payload {
		if s, _ := v.(string); strings.Contains(s, "Zed") || strings.Contains(s, "@") {
			t.Errorf("payload[%q] = %q carries personal data", k, s)
		}
	}
}

func TestStoreProvisionWorkspace_RefusalsAfterASuccessWriteNoSecondEvent(t *testing.T) {
	r := newRegistrant(t)
	store := NewStore(r.app)
	if _, _, err := store.ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "First Door", DisplayName: "Ada"}); err != nil {
		t.Fatalf("first ProvisionWorkspace: %v", err)
	}
	if n := len(provisionedEvents(t, r.super, r.tenantID)); n != 1 {
		t.Fatalf("control: workspace.provisioned rows after the first call = %d, want 1", n)
	}

	bearing := r.id
	bearing.TenantID = r.tenantID
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"second call at the same workspace id", r.ctx(), ErrAlreadyProvisioned},
		{"caller already carries a tenant", auth.WithIdentity(context.Background(), bearing), ErrAlreadyProvisioned},
		{"no caller at all", context.Background(), db.ErrNoTenant},
		{"subject is not a uuid", auth.WithTenantlessCaller(context.Background(), auth.Identity{Subject: "not-a-uuid", Role: "authenticated"}), db.ErrNoTenant},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := store.ProvisionWorkspace(tc.ctx, ProvisionInput{WorkspaceName: "Second Door", DisplayName: "Ada"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			var n int
			if err := r.super.QueryRow(context.Background(),
				`SELECT count(*) FROM audit_log WHERE event = 'workspace.provisioned' AND (tenant_id = $1 OR actor = $2)`,
				r.tenantID, r.id.Subject).Scan(&n); err != nil {
				t.Fatalf("count audit rows: %v", err)
			}
			if n != 1 {
				t.Errorf("workspace.provisioned rows for this registrant = %d, want still 1", n)
			}
		})
	}
}

func TestAuditRead_AnotherWorkspaceAdminCannotReadTheProvisionedEvent(t *testing.T) {
	a, b := newRegistrant(t), newRegistrant(t)
	for i, r := range []registrant{a, b} {
		in := ProvisionInput{WorkspaceName: fmt.Sprintf("Isolated Works %d", i), DisplayName: fmt.Sprintf("Admin %d", i)}
		if _, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), in); err != nil {
			t.Fatalf("ProvisionWorkspace %d: %v", i, err)
		}
	}
	list := func(r registrant, f audit.Filter) audit.Response {
		t.Helper()
		ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: r.id.Subject, Role: "authenticated", TenantID: r.tenantID})
		resp, err := audit.NewStore(r.app).List(ctx, f)
		if err != nil {
			t.Fatalf("audit List: %v", err)
		}
		return resp
	}

	own := list(b, audit.Filter{Events: []string{"workspace.provisioned"}, Limit: 50})
	if len(own.Events) != 1 {
		t.Fatalf("control: B reads %d workspace.provisioned events of its own, want 1", len(own.Events))
	}
	if own.Events[0].Actor != b.id.Subject {
		t.Errorf("B reads an event whose actor is %q, want its own %q", own.Events[0].Actor, b.id.Subject)
	}
	for _, e := range own.Events {
		if e.Actor == a.id.Subject {
			t.Errorf("B reads A's workspace.provisioned event %+v", e)
		}
	}

	byActor := list(b, audit.Filter{Actors: []string{a.id.Subject}, Limit: 50})
	if len(byActor.Events) != 0 {
		t.Errorf("B filtering by A's subject reads %d events, want 0", len(byActor.Events))
	}
}

func TestProvision_SucceedsForAnIdentityRemovedElsewhere(t *testing.T) {
	r := newRegistrant(t)
	ctx := context.Background()
	otherID := uuid.NewString()
	if _, err := r.super.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'Removed From')`, otherID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = r.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, otherID) })
	if _, err := r.super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'preparer', 'removed')`,
		otherID, r.id.Subject); err != nil {
		t.Fatalf("seed a removed membership: %v", err)
	}

	tenant, _, err := NewStore(r.app).ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Second Chance Ltd", DisplayName: "Ada"})
	if err != nil {
		t.Fatalf("ProvisionWorkspace for a removed-only identity: %v", err)
	}
	if tenant.ID != r.tenantID {
		t.Errorf("tenant id = %s, want %s", tenant.ID, r.tenantID)
	}
	_, members := provisionedRows(t, r.super, r.tenantID)
	if len(members) != 1 || members[0].UserID != r.id.Subject || members[0].Role != "admin" || members[0].Status != "active" {
		t.Errorf("memberships in the new workspace = %+v, want one active admin row for the caller", members)
	}
	var removed int
	if err := r.super.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE tenant_id = $1 AND user_id = $2 AND status = 'removed'`,
		otherID, r.id.Subject).Scan(&removed); err != nil || removed != 1 {
		t.Errorf("removed rows in the old workspace = %d (%v), want 1 untouched", removed, err)
	}
}

// provisionAttempts counts the provision_workspace calls a traced pool sent.
func provisionAttempts(tr *sqlTrace) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	n := 0
	for _, s := range tr.stmts {
		if strings.Contains(s.sql, "public.provision_workspace") {
			n++
		}
	}
	return n
}

// dropTenantsOf deletes every tenant the subject holds a row in, after the test.
func dropTenantsOf(t *testing.T, super *pgxpool.Pool, subject string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(),
			`DELETE FROM tenants WHERE id IN (SELECT tenant_id FROM memberships WHERE user_id = $1)`, subject)
	})
}

// removedCreator is r after it provisioned its workspace and a second admin removed it.
func removedCreator(t *testing.T) registrant {
	t.Helper()
	r := newRegistrant(t)
	dropTenantsOf(t, r.super, r.id.Subject)
	store := NewStore(r.app)
	if _, _, err := store.ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "First Works", DisplayName: "Ada Creator"}); err != nil {
		t.Fatalf("fixture: provision the first workspace: %v", err)
	}
	other := uuid.NewString()
	seedIdentityMembership(t, r.super, r.tenantID, other, "admin", "active", strp("Bola Eze"), strp(other+"@members.test"))
	admin := auth.WithIdentity(context.Background(), auth.Identity{Subject: other, Role: "authenticated", TenantID: r.tenantID})
	if _, err := store.RemoveMember(admin, r.id.Subject); err != nil {
		t.Fatalf("fixture: remove the creator: %v", err)
	}
	return r
}

func TestProvision_RemovedCreatorGetsANewWorkspace(t *testing.T) {
	r := removedCreator(t)
	before := imageOf(t, r.super, r.tenantID, r.id.Subject)
	store, tr := tracedStore(t)
	tenantsBefore := tenantCount(t, r.super)

	tenant, subject, err := store.ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Second Works", DisplayName: "Ada Again"})
	if err != nil {
		t.Fatalf("ProvisionWorkspace for a removed creator: %v", err)
	}
	if subject != r.id.Subject {
		t.Errorf("subject = %q, want the caller's %q", subject, r.id.Subject)
	}
	if _, perr := uuid.Parse(tenant.ID); perr != nil || tenant.ID == r.tenantID {
		t.Fatalf("tenant id = %q, want a uuid other than the old workspace and uuidv5(subject) %s", tenant.ID, r.tenantID)
	}
	if tenant.Name != "Second Works" {
		t.Errorf("tenant name = %q, want Second Works", tenant.Name)
	}
	if n := provisionAttempts(tr); n != 2 {
		t.Errorf("provision_workspace calls = %d, want 2 (the derived id, then one random id)", n)
	}
	if got := tenantCount(t, r.super); got != tenantsBefore+1 {
		t.Errorf("tenants %d -> %d, want exactly one more", tenantsBefore, got)
	}

	_, members := provisionedRows(t, r.super, tenant.ID)
	if len(members) != 1 || members[0].UserID != r.id.Subject || members[0].Role != "admin" || members[0].Status != "active" {
		t.Errorf("memberships in the new workspace = %+v, want the caller as its one active admin", members)
	}
	if n := len(provisionedEvents(t, r.super, tenant.ID)); n != 1 {
		t.Errorf("workspace.provisioned rows in the new workspace = %d, want 1 (the failed first attempt wrote none)", n)
	}
	if after := imageOf(t, r.super, r.tenantID, r.id.Subject); after != before || after.Status != "removed" {
		t.Errorf("old workspace row = %+v, want the removed row untouched (was %+v)", after, before)
	}
}

func TestProvision_FreshIdentityKeepsTheDerivedID(t *testing.T) {
	r := newRegistrant(t)
	store, tr := tracedStore(t)

	tenant, _, err := store.ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Fresh Works", DisplayName: "Ada"})
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	if want := uuid.NewSHA1(workspaceNamespace, []byte(r.id.Subject)).String(); tenant.ID != want {
		t.Errorf("tenant id = %s, want uuidv5(subject) %s", tenant.ID, want)
	}
	if n := provisionAttempts(tr); n != 1 {
		t.Errorf("provision_workspace calls = %d, want 1 (no retry on success)", n)
	}
}

func TestProvision_LiveMemberIsNotRetried(t *testing.T) {
	for _, status := range []string{"active", "suspended", "invited"} {
		t.Run(status, func(t *testing.T) {
			r := newRegistrant(t)
			seedMembership(t, r.super, seedTenant(t, r.super, "Elsewhere "+status), r.id.Subject, "admin", status)
			store, tr := tracedStore(t)
			tenantsBefore := tenantCount(t, r.super)

			_, _, err := store.ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Refused Works", DisplayName: "Ada"})
			if !errors.Is(err, ErrAlreadyProvisioned) {
				t.Fatalf("err = %v, want ErrAlreadyProvisioned", err)
			}
			if n := provisionAttempts(tr); n != 1 {
				t.Errorf("provision_workspace calls = %d, want 1 (one_workspace_per_identity never retries)", n)
			}
			if got := tenantCount(t, r.super); got != tenantsBefore {
				t.Errorf("tenants %d -> %d, want unchanged", tenantsBefore, got)
			}
		})
	}
}

// Two provisions by a removed creator are held on the identity lock until both queue, so they overlap on every run.
func TestProvision_ConcurrentRemovedCreatorProvisionsOnce(t *testing.T) {
	r := removedCreator(t)
	store := NewStore(r.app)
	tenantsBefore := tenantCount(t, r.super)

	release := holdIdentityLock(t, r.super, r.id.Subject)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, _, err := store.ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Race Works", DisplayName: "Ada"})
			errs <- err
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for identityLockRequests(t, r.super, r.id.Subject, false) < 2 {
		select {
		case err := <-errs:
			t.Fatalf("a provision returned while the identity lock was held (err %v)", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("pg_locks shows %d queued requests on the identity key after 5 s, want 2", identityLockRequests(t, r.super, r.id.Subject, false))
		}
		time.Sleep(25 * time.Millisecond)
	}
	release()

	var created, refused int
	for range 2 {
		select {
		case err := <-errs:
			switch {
			case err == nil:
				created++
			case errors.Is(err, ErrAlreadyProvisioned):
				refused++
			default:
				t.Errorf("a provision failed with %v, want success or ErrAlreadyProvisioned", err)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("a provision did not return after the lock was released")
		}
	}
	if created != 1 || refused != 1 {
		t.Errorf("results: %d created, %d refused; want exactly 1 and 1", created, refused)
	}
	var live, all int
	if err := r.super.QueryRow(context.Background(),
		`SELECT count(*) FILTER (WHERE status <> 'removed'), count(*) FROM memberships WHERE user_id = $1`, r.id.Subject).Scan(&live, &all); err != nil {
		t.Fatal(err)
	}
	if live != 1 || all != 2 {
		t.Errorf("caller rows: %d live of %d, want 1 live of 2 (the removed row and one new)", live, all)
	}
	if got := tenantCount(t, r.super); got != tenantsBefore+1 {
		t.Errorf("tenants %d -> %d, want exactly one more", tenantsBefore, got)
	}
}

func TestProvision_RetryNeedsTheCallersRemovedRowAtTheDerivedID(t *testing.T) {
	atDerivedID := func(t *testing.T, r registrant) {
		t.Helper()
		if _, err := r.super.Exec(context.Background(), `INSERT INTO tenants (id, name) VALUES ($1, 'Orphan Works')`, r.tenantID); err != nil {
			t.Fatalf("seed the tenant at the derived id: %v", err)
		}
	}
	for _, c := range []struct {
		name  string
		setup func(t *testing.T) registrant
		want  bool // a new workspace
	}{
		{"the caller's removed row is in another tenant", func(t *testing.T) registrant {
			r := newRegistrant(t)
			atDerivedID(t, r)
			seedMembership(t, r.super, seedTenant(t, r.super, "Left Behind"), r.id.Subject, "preparer", "removed")
			return r
		}, false},
		{"only another member's removed row is at the derived id", func(t *testing.T) registrant {
			r := newRegistrant(t)
			atDerivedID(t, r)
			seedMembership(t, r.super, r.tenantID, uuid.NewString(), "preparer", "removed")
			return r
		}, false},
		{"an active row elsewhere beside the removed row at the derived id", func(t *testing.T) registrant {
			r := removedCreator(t)
			seedMembership(t, r.super, seedTenant(t, r.super, "Joined Since"), r.id.Subject, "admin", "active")
			return r
		}, false},
		// Control: the caller's own removed row at the derived id, beside another member's, retries.
		{"the caller's removed row is at the derived id", func(t *testing.T) registrant {
			r := newRegistrant(t)
			dropTenantsOf(t, r.super, r.id.Subject)
			atDerivedID(t, r)
			seedMembership(t, r.super, r.tenantID, uuid.NewString(), "preparer", "removed")
			seedMembership(t, r.super, r.tenantID, r.id.Subject, "admin", "removed")
			return r
		}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := c.setup(t)
			store, tr := tracedStore(t)
			before := tenantCount(t, r.super)

			tenant, _, err := store.ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Retry Works", DisplayName: "Ada"})

			if c.want {
				if err != nil || tenant.ID == r.tenantID || provisionAttempts(tr) != 2 || tenantCount(t, r.super) != before+1 {
					t.Errorf("tenant %q, err %v, attempts %d, tenants %d -> %d; want a new workspace after 2 attempts", tenant.ID, err, provisionAttempts(tr), before, tenantCount(t, r.super))
				}
				return
			}
			if !errors.Is(err, ErrAlreadyProvisioned) {
				t.Errorf("err = %v, want ErrAlreadyProvisioned", err)
			}
			if n := provisionAttempts(tr); n != 1 {
				t.Errorf("provision_workspace calls = %d, want 1 (no retry)", n)
			}
			if got := tenantCount(t, r.super); got != before {
				t.Errorf("tenants %d -> %d, want unchanged", before, got)
			}
		})
	}
}

func TestProvision_RetryIDIsRandomAndNotDerivable(t *testing.T) {
	r := removedCreator(t)
	store := NewStore(r.app)
	derived := uuid.NewSHA1(workspaceNamespace, []byte(r.id.Subject)).String()

	first, _, err := store.ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Second Works", DisplayName: "Ada"})
	if err != nil {
		t.Fatalf("first retry: %v", err)
	}
	// Remove the creator from the new workspace too; the next provision retries again.
	other := uuid.NewString()
	seedIdentityMembership(t, r.super, first.ID, other, "admin", "active", strp("Bola Eze"), strp(other+"@members.test"))
	admin := auth.WithIdentity(context.Background(), auth.Identity{Subject: other, Role: "authenticated", TenantID: first.ID})
	if _, err := store.RemoveMember(admin, r.id.Subject); err != nil {
		t.Fatalf("remove the creator from the new workspace: %v", err)
	}
	second, _, err := store.ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Third Works", DisplayName: "Ada"})
	if err != nil {
		t.Fatalf("second retry: %v", err)
	}

	if first.ID == second.ID {
		t.Errorf("two retries got the same tenant id %s, want a fresh one each", first.ID)
	}
	for _, id := range []string{first.ID, second.ID} {
		parsed, perr := uuid.Parse(id)
		if perr != nil || parsed.Version() != 4 {
			t.Errorf("retry id %q: version %d, err %v; want a random v4 uuid", id, parsed.Version(), perr)
		}
		if id == derived {
			t.Errorf("retry id %s is uuidv5(subject)", id)
		}
	}
}

func TestHoldsRemovedRow(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	user, other := uuid.NewString(), uuid.NewString()
	at := func(status, who string) string {
		tenant := seedTenant(t, super, "Probe "+status)
		seedMembership(t, super, tenant, who, "preparer", status)
		return tenant
	}
	for _, c := range []struct {
		name   string
		tenant string
		want   bool
	}{
		{"the caller's removed row", at("removed", user), true},
		{"the caller's active row", at("active", user), false},
		{"the caller's suspended row", at("suspended", user), false},
		{"the caller's invited row", at("invited", user), false},
		{"another member's removed row", at("removed", other), false},
		{"no row at all", seedTenant(t, super, "Probe empty"), false},
		{"a tenant that does not exist", uuid.NewString(), false},
		{"a malformed tenant id", "not-a-uuid", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := store.holdsRemovedRow(context.Background(), c.tenant, user)
			if wantErr := c.tenant == "not-a-uuid"; (err != nil) != wantErr {
				t.Errorf("holdsRemovedRow err = %v, want error %v", err, wantErr)
			}
			if got != c.want {
				t.Errorf("holdsRemovedRow = %v, want %v", got, c.want)
			}
		})
	}
}
