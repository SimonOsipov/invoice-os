package tenancy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	removeSentence    = "only an admin can remove a member"
	removedPatchWords = "a removed member must be invited again"
	lastAdminSentence = "this is the tenant's last active admin — make another member an active admin first"
)

// memberImage is one membership row and its child counts, comparable as a value.
type memberImage struct {
	Status, Role, Name, Email string
	AllClients                bool
	CreatedNano               int64
	Clients, Roles            int
}

func orNil(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return "v:" + *p
}

func imageOf(t *testing.T, super *pgxpool.Pool, tenant, user string) memberImage {
	t.Helper()
	var m memberImage
	var name, email *string
	var created time.Time
	if err := super.QueryRow(context.Background(),
		`SELECT status, role, display_name, email, all_clients, created_at,
		        (SELECT count(*) FROM membership_clients c WHERE c.tenant_id = m.tenant_id AND c.user_id = m.user_id),
		        (SELECT count(*) FROM workflow_role_members r WHERE r.tenant_id = m.tenant_id AND r.user_id = m.user_id)
		   FROM memberships m WHERE tenant_id = $1 AND user_id = $2`, tenant, user,
	).Scan(&m.Status, &m.Role, &name, &email, &m.AllClients, &created, &m.Clients, &m.Roles); err != nil {
		t.Fatalf("read back membership %s: %v", user, err)
	}
	m.Name, m.Email, m.CreatedNano = orNil(name), orNil(email), created.UnixNano()
	return m
}

// seedRoleStaffedBy inserts a workflow role and staffs it with users in order.
func seedRoleStaffedBy(t *testing.T, super *pgxpool.Pool, tenant, key string, users ...string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO workflow_roles (tenant_id, key, title) VALUES ($1, $2, $2) RETURNING id`, tenant, key).Scan(&id); err != nil {
		t.Fatalf("seed workflow role: %v", err)
	}
	for i, u := range users {
		if _, err := super.Exec(context.Background(),
			`INSERT INTO workflow_role_members (tenant_id, workflow_role_id, user_id, ord) VALUES ($1, $2, $3, $4)`,
			tenant, id, u, i); err != nil {
			t.Fatalf("staff workflow role: %v", err)
		}
	}
	return id
}

// grantedMember seeds a named preparer with two clients, "All clients" set and one staffed role.
func (w invWorld) grantedMember(t *testing.T, name string) (id string, entities []string) {
	t.Helper()
	id = w.addMember(t, "preparer", "active", name)
	entities = []string{seedEntity(t, w.super, w.tenant), seedEntity(t, w.super, w.tenant)}
	giveClients(t, w.super, w.tenant, id, entities...)
	giveAllClients(t, w.super, w.tenant, id)
	seedRoleStaffedBy(t, w.super, w.tenant, "r"+id[:8], id)
	if img := imageOf(t, w.super, w.tenant, id); img.Clients != 2 || img.Roles != 1 || !img.AllClients {
		t.Fatalf("fixture: granted member = %+v, want 2 clients, 1 role, all_clients", img)
	}
	return id, entities
}

// removedRow seeds an already-removed membership: name kept, email cleared.
func (w invWorld) removedRow(t *testing.T, name string) string {
	t.Helper()
	id := uuid.NewString()
	seedIdentityMembership(t, w.super, w.tenant, id, "preparer", "removed", strp(name), nil)
	return id
}

func removeViaMux(t *testing.T, remove func(context.Context, string) (MemberAccess, error), logger *slog.Logger, id *auth.Identity, pathID string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/memberships/{user_id}/remove", RemoveMemberHandler(remove, logger))
	r := httptest.NewRequest("POST", "/v1/memberships/"+pathID+"/remove", nil)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

func TestRemoveMember_EndsTheMembership(t *testing.T) {
	w := newInvWorld(t, "DEL-02-02 ends", "Ada Obi")
	m, _ := w.grantedMember(t, "Grace Eze")
	before := imageOf(t, w.super, w.tenant, m)

	got, err := w.store.RemoveMember(w.adminCtx(), m)
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if got.Status != "removed" || got.Email != nil || got.DisplayName == nil || *got.DisplayName != "Grace Eze" || got.Role != "preparer" {
		t.Errorf("returned row: status=%q email-nil=%v name-kept=%v role=%q, want removed, nil, true, preparer",
			got.Status, got.Email == nil, got.DisplayName != nil, got.Role)
	}
	raw, _ := json.Marshal(got)
	if !strings.Contains(string(raw), `"email":null`) || !strings.Contains(string(raw), `"status":"removed"`) {
		t.Errorf("wire row lacks email:null and status:removed: %s", raw)
	}

	after := imageOf(t, w.super, w.tenant, m)
	want := before
	want.Status, want.Email, want.AllClients, want.Clients, want.Roles = "removed", "<nil>", false, 0, 0
	if after != want {
		t.Errorf("stored row = %+v, want %+v (name, role, created_at kept; email, all_clients, grants cleared)", after, want)
	}
}

func TestRemoveMember_KeepsOtherMembersGrants(t *testing.T) {
	w := newInvWorld(t, "DEL-02-02 others", "Ada Obi")
	a, entities := w.grantedMember(t, "Grace Eze")
	b := w.addMember(t, "preparer", "active", "Tunde Bello")
	giveClients(t, w.super, w.tenant, b, entities...)
	seedRoleStaffedBy(t, w.super, w.tenant, "shared", a, b)
	aBefore := imageOf(t, w.super, w.tenant, a)
	if aBefore.Roles != 2 {
		t.Fatalf("fixture: a holds %d roles, want 2", aBefore.Roles)
	}

	if _, err := w.store.RemoveMember(w.adminCtx(), b); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if after := imageOf(t, w.super, w.tenant, a); after != aBefore {
		t.Errorf("the other member's row and grants = %+v, want unchanged %+v", after, aBefore)
	}
	if img := imageOf(t, w.super, w.tenant, b); img.Status != "removed" || img.Clients != 0 || img.Roles != 0 {
		t.Errorf("removed member = %+v, want removed with no grants", img)
	}
}

func TestRemoveMember_AuditsInTheSameTx(t *testing.T) {
	w := newInvWorld(t, "DEL-02-02 audit", "Ada Obi")
	m := w.addMember(t, "reviewer", "suspended", "Grace Eze")

	if _, err := w.store.RemoveMember(w.adminCtx(), m); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if n := auditCount(t, w.app, w.tenant, "membership.removed"); n != 1 {
		t.Fatalf("membership.removed rows = %d, want 1", n)
	}
	p := auditPayload(t, w.app, w.tenant, "membership.removed", m)
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"from", "user_id"}) {
		t.Errorf("payload keys = %v, want exactly [from user_id] (no email, no display_name)", keys)
	}
	if p["from"] != "suspended" || p["user_id"] != m {
		t.Errorf("payload from=%v user_id-matches=%v, want from=suspended and the target id", p["from"], p["user_id"] == m)
	}
	if got := auditActor(t, w.app, w.tenant, "membership.removed"); got != w.admin {
		t.Errorf("audit actor = %q, want the removing admin %q", got, w.admin)
	}
}

func TestRemoveMember_FailedAuditRollsBack(t *testing.T) {
	w := newInvWorld(t, "DEL-02-02 audit rollback", "Ada Obi")
	m, _ := w.grantedMember(t, "Grace Eze")
	before := imageOf(t, w.super, w.tenant, m)

	ctx := context.Background()
	name := "qa_audit_fail_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := w.super.Exec(ctx, fmt.Sprintf(
		`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN RAISE EXCEPTION 'forced audit failure' USING ERRCODE = 'check_violation'; END; $$`, name)); err != nil {
		t.Fatalf("create trigger function: %v", err)
	}
	t.Cleanup(func() {
		_, _ = w.super.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s() CASCADE`, name))
	})
	if _, err := w.super.Exec(ctx, fmt.Sprintf(
		`CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW
		 WHEN (NEW.payload->>'user_id' = %s) EXECUTE FUNCTION %s()`, name, quoteLiteral(m), name)); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if _, err := w.store.RemoveMember(w.adminCtx(), m); pgCode(err) != "23514" {
		t.Fatalf("RemoveMember err = %v (SQLSTATE %q), want the forced audit failure 23514", err, pgCode(err))
	}
	if after := imageOf(t, w.super, w.tenant, m); after != before {
		t.Errorf("row after a failed audit = %+v, want rolled back to %+v", after, before)
	}
}

func TestRemoveMember_NonAdminRefusedAlikeForEveryTarget(t *testing.T) {
	w := newInvWorld(t, "DEL-02-02 non-admin", "Ada Obi")
	other := newInvWorld(t, "DEL-02-02 non-admin other", "Zed Okoro")
	caller := w.addMember(t, "preparer", "active", "Pat Eze")
	real := w.addMember(t, "reviewer", "active", "Grace Eze")
	cross := other.addMember(t, "reviewer", "active", "Tunde Bello")
	realBefore, crossBefore := imageOf(t, w.super, w.tenant, real), imageOf(t, w.super, other.tenant, cross)

	id := auth.Identity{Subject: caller, Role: "authenticated", TenantID: w.tenant}
	var wantCode int
	var wantBody string
	for i, tc := range []struct{ name, target string }{
		{"a real member", real}, {"an unknown id", uuid.NewString()}, {"a cross-tenant member", cross},
	} {
		if _, err := w.store.RemoveMember(w.as(caller), tc.target); !errors.Is(err, ErrRemoveNotPermitted) {
			t.Errorf("%s: store err = %v, want ErrRemoveNotPermitted", tc.name, err)
		}
		rec := removeViaMux(t, w.store.RemoveMember, nil, &id, tc.target)
		if i == 0 {
			wantCode, wantBody = rec.Code, rec.Body.String()
			if wantCode != http.StatusForbidden || !strings.Contains(wantBody, removeSentence) {
				t.Fatalf("%s: wire = %d %s, want 403 with %q", tc.name, wantCode, wantBody, removeSentence)
			}
		}
		if rec.Code != wantCode || rec.Body.String() != wantBody {
			t.Errorf("%s: wire = %d %s, want byte-identical to the first (%d %s)", tc.name, rec.Code, rec.Body.String(), wantCode, wantBody)
		}
	}
	if imageOf(t, w.super, w.tenant, real) != realBefore || imageOf(t, w.super, other.tenant, cross) != crossBefore {
		t.Error("a refused remove changed a target row")
	}
	if _, err := w.store.RemoveMember(w.adminCtx(), real); err != nil {
		t.Errorf("control: an admin removing the same member: %v", err)
	}
}

func TestRemoveMember_SuspendedAdminRefused(t *testing.T) {
	w := newInvWorld(t, "DEL-02-02 suspended admin", "Ada Obi")
	susp := w.addMember(t, "admin", "suspended", "Sue Pend")
	m := w.addMember(t, "reviewer", "active", "Grace Eze")
	before := imageOf(t, w.super, w.tenant, m)

	if _, err := w.store.RemoveMember(w.as(susp), m); !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("err = %v, want db.ErrNotActiveMember from the seam", err)
	}
	if imageOf(t, w.super, w.tenant, m) != before {
		t.Error("a suspended admin's remove changed the target")
	}
	if _, err := w.store.RemoveMember(w.adminCtx(), m); err != nil {
		t.Errorf("control: an active admin removing the same member: %v", err)
	}
}

func TestRemoveMember_LastActiveAdminRefused(t *testing.T) {
	w := newInvWorld(t, "DEL-02-02 last admin", "Ada Obi")
	w.addMember(t, "admin", "suspended", "Sue Pend")
	before := imageOf(t, w.super, w.tenant, w.admin)

	if _, err := w.store.RemoveMember(w.adminCtx(), w.admin); !errors.Is(err, ErrLastActiveAdmin) {
		t.Fatalf("err = %v, want ErrLastActiveAdmin", err)
	}
	if imageOf(t, w.super, w.tenant, w.admin) != before {
		t.Error("the refused remove changed the sole admin")
	}
	if n := auditCount(t, w.app, w.tenant, "membership.removed"); n != 0 {
		t.Errorf("membership.removed rows = %d, want 0", n)
	}
	id := auth.Identity{Subject: w.admin, Role: "authenticated", TenantID: w.tenant}
	rec := removeViaMux(t, w.store.RemoveMember, nil, &id, w.admin)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), lastAdminSentence) {
		t.Errorf("wire = %d %s, want 409 with the last-admin sentence", rec.Code, rec.Body.String())
	}
}

func TestRemoveMember_OneOfTwoAdminsRemoved(t *testing.T) {
	w := newInvWorld(t, "DEL-02-02 two admins", "Ada Obi")
	other := w.addMember(t, "admin", "active", "Zed Okoro")

	got, err := w.store.RemoveMember(w.adminCtx(), other)
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if got.Status != "removed" || mustStatus(t, w.super, w.admin) != "active" {
		t.Errorf("removed status=%q caller status=%q, want removed and active", got.Status, mustStatus(t, w.super, w.admin))
	}
}

func TestRemoveMember_ConcurrentLastTwoAdmins(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	for round := range 8 {
		tenantID := seedTenant(t, super, "DEL-02-02 concurrent admins")
		x, y := uuid.NewString(), uuid.NewString()
		seedMembership(t, super, tenantID, x, "admin", "active")
		seedMembership(t, super, tenantID, y, "admin", "active")
		cX := auth.WithIdentity(ctx, auth.Identity{Subject: x, Role: "authenticated", TenantID: tenantID})
		cY := auth.WithIdentity(ctx, auth.Identity{Subject: y, Role: "authenticated", TenantID: tenantID})

		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, errs[0] = store.RemoveMember(cX, y) }()
		go func() { defer wg.Done(); <-start; _, errs[1] = store.RemoveMember(cY, x) }()
		close(start)
		wg.Wait()

		oks := 0
		for i, err := range errs {
			switch {
			case err == nil:
				oks++
			case errors.Is(err, ErrLastActiveAdmin), errors.Is(err, ErrRemoveNotPermitted):
			default:
				t.Fatalf("round %d: errs[%d] = %v, want nil or ErrLastActiveAdmin (a deadlock is a defect)", round, i, err)
			}
		}
		if oks != 1 {
			t.Fatalf("round %d: results = %v, want exactly one success", round, errs)
		}
		var removed, active int
		if err := super.QueryRow(ctx,
			`SELECT count(*) FILTER (WHERE status = 'removed'), count(*) FILTER (WHERE status = 'active')
			   FROM memberships WHERE tenant_id = $1 AND role = 'admin'`, tenantID).Scan(&removed, &active); err != nil {
			t.Fatalf("round %d: count admins: %v", round, err)
		}
		if removed != 1 || active != 1 {
			t.Fatalf("round %d: admins = %d removed / %d active, want 1 / 1", round, removed, active)
		}
	}
}

func TestRemoveMember_SuspendedAdminTargetRemovable(t *testing.T) {
	w := newInvWorld(t, "DEL-02-02 suspended admin target", "Ada Obi")
	susp := w.addMember(t, "admin", "suspended", "Sue Pend")

	got, err := w.store.RemoveMember(w.adminCtx(), susp)
	if err != nil {
		t.Fatalf("RemoveMember of a suspended admin by the sole active admin: %v", err)
	}
	if got.Status != "removed" || mustStatus(t, w.super, w.admin) != "active" {
		t.Errorf("target status=%q caller status=%q, want removed and active", got.Status, mustStatus(t, w.super, w.admin))
	}
}

func TestRemoveMember_AlreadyRemovedIsANoOp(t *testing.T) {
	w := newInvWorld(t, "DEL-02-02 already removed", "Ada Obi")
	m := w.removedRow(t, "Grace Eze")
	before := imageOf(t, w.super, w.tenant, m)

	got, err := w.store.RemoveMember(w.adminCtx(), m)
	if err != nil {
		t.Fatalf("RemoveMember of a removed row: %v, want 200", err)
	}
	if got.Status != "removed" || got.Email != nil {
		t.Errorf("returned status=%q email-nil=%v, want the removed row", got.Status, got.Email == nil)
	}
	if imageOf(t, w.super, w.tenant, m) != before {
		t.Error("a repeat remove changed the row")
	}
	if n := auditCount(t, w.app, w.tenant, "membership.removed"); n != 0 {
		t.Errorf("membership.removed rows = %d, want 0 for a no-op", n)
	}
}

func TestRemoveMember_UnknownOrCrossTenantIsNotFound(t *testing.T) {
	w := newInvWorld(t, "DEL-02-02 not found", "Ada Obi")
	other := newInvWorld(t, "DEL-02-02 not found other", "Zed Okoro")
	cross := other.addMember(t, "reviewer", "active", "Tunde Bello")
	before := imageOf(t, w.super, other.tenant, cross)

	for name, target := range map[string]string{"unknown": uuid.NewString(), "cross-tenant": cross} {
		if _, err := w.store.RemoveMember(w.adminCtx(), target); !errors.Is(err, ErrMembershipNotFound) {
			t.Errorf("%s: err = %v, want ErrMembershipNotFound", name, err)
		}
	}
	if imageOf(t, w.super, other.tenant, cross) != before {
		t.Error("a cross-tenant remove changed the other tenant's row")
	}
	if _, err := w.store.RemoveMember(w.adminCtx(), w.addMember(t, "reviewer", "active", "Grace Eze")); err != nil {
		t.Errorf("control: removing a member of the caller's tenant: %v", err)
	}
}

func TestRemoveMemberHandler_Mapping(t *testing.T) {
	member := uuid.NewString()
	id := &auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	name := "Grace Eze"
	ok := MemberAccess{Membership: Membership{UserID: member, Role: "preparer", Status: "removed", DisplayName: &name}}
	cases := []struct {
		name     string
		store    error
		identity *auth.Identity
		path     string
		want     int
		wantMsg  string
		called   bool
	}{
		{"removed", nil, id, member, 200, "", true},
		{"not an admin", ErrRemoveNotPermitted, id, member, 403, removeSentence, true},
		{"not found", ErrMembershipNotFound, id, member, 404, "membership not found", true},
		{"last admin", ErrLastActiveAdmin, id, member, 409, lastAdminSentence, true},
		{"seam refusal", db.ErrNotActiveMember, id, member, 403, db.NotActiveMemberMessage, true},
		{"internal", errors.New("boom"), id, member, 500, "internal server error", true},
		{"no identity", nil, nil, member, 401, "unauthorized", false},
		{"malformed id", nil, id, "not-a-uuid", 404, "membership not found", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			remove := func(_ context.Context, u string) (MemberAccess, error) {
				called = true
				if u != member {
					t.Errorf("store got user id %q, want the canonical path id", u)
				}
				return ok, tc.store
			}
			rec := removeViaMux(t, remove, nil, tc.identity, tc.path)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if called != tc.called {
				t.Errorf("store called = %v, want %v", called, tc.called)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body %q: %v", rec.Body.String(), err)
			}
			if tc.want == 200 {
				if body["status"] != "removed" || body["email"] != nil || body["user_id"] != member {
					t.Errorf("200 body = %v, want the removed member row with email null", body)
				}
				return
			}
			if body["error"] != tc.wantMsg {
				t.Errorf("error = %v, want %q", body["error"], tc.wantMsg)
			}
		})
	}
}

func TestRemoveMember_RemovedCallerIsGated(t *testing.T) {
	w := newInvWorld(t, "DEL-02-02 gated", "Ada Obi")
	m := w.addMember(t, "reviewer", "active", "Grace Eze")
	if _, err := w.store.ListMemberships(w.as(m)); err != nil {
		t.Fatalf("control: an active member lists memberships: %v", err)
	}
	if _, err := w.store.RemoveMember(w.adminCtx(), m); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if _, err := w.store.ListMemberships(w.as(m)); !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("ListMemberships as the removed member: err = %v, want db.ErrNotActiveMember", err)
	}
}

func TestRemoveMemberHandler_RefusalsAndLogsCarryNoNameOrEmail(t *testing.T) {
	const adaName, adaEmail = "Ada Obi", "ada.obi@example.test"
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "DEL-02-02 no pii")
	ada, boss, prep := uuid.NewString(), uuid.NewString(), uuid.NewString()
	seedIdentityMembership(t, super, tenant, ada, "admin", "active", strp(adaName), strp(adaEmail))
	seedMembership(t, super, tenant, prep, "preparer", "active")
	store := NewStore(app)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	idOf := func(u string) *auth.Identity {
		return &auth.Identity{Subject: u, Role: "authenticated", TenantID: tenant}
	}

	var bodies []string
	step := func(label string, rec *httptest.ResponseRecorder, want int) {
		t.Helper()
		bodies = append(bodies, rec.Body.String())
		if rec.Code != want {
			t.Errorf("%s: status = %d, want %d", label, rec.Code, want)
		}
	}
	step("preparer removes", removeViaMux(t, store.RemoveMember, logger, idOf(prep), ada), 403)
	step("sole admin removes self", removeViaMux(t, store.RemoveMember, logger, idOf(ada), ada), 409)

	seedMembership(t, super, tenant, boss, "admin", "active")
	step("second admin removes Ada", removeViaMux(t, store.RemoveMember, logger, idOf(boss), ada), 200)
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /v1/memberships/{user_id}", SetMembershipStatusHandler(store.SetMembershipStatus, logger))
	r := httptest.NewRequest("PATCH", "/v1/memberships/"+ada, strings.NewReader(`{"status":"active"}`))
	r = r.WithContext(auth.WithIdentity(r.Context(), *idOf(boss)))
	patch := httptest.NewRecorder()
	mux.ServeHTTP(patch, r)
	step("PATCH on the removed row", patch, 409)
	if !strings.Contains(patch.Body.String(), removedPatchWords) {
		t.Errorf("PATCH body lacks %q", removedPatchWords)
	}

	closed, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	closed.Close()
	step("remove on a closed pool", removeViaMux(t, NewStore(closed).RemoveMember, logger, idOf(boss), prep), 500)
	if logs.Len() == 0 {
		t.Fatal("the 500 wrote no log line, so the absence checks below read nothing")
	}

	all := strings.ToLower(logs.String() + strings.Join(bodies, "\n"))
	for _, needle := range []string{adaName, adaEmail} {
		if strings.Contains(all, strings.ToLower(needle)) {
			t.Errorf("a response body or log line carries the fixture name or address %q", needle[:3]+"...")
		}
	}
}

func TestRemoveMember_BesideARolePutLeavesNoGrant(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store, roles := NewStore(app), approval.NewStore(app, nil, nil)

	for round := range 20 {
		tenant := seedTenant(t, super, "DEL-02-02 role put race")
		admin, b := uuid.NewString(), uuid.NewString()
		seedMembership(t, super, tenant, admin, "admin", "active")
		seedMembership(t, super, tenant, b, "preparer", "active")
		seedRoleStaffedBy(t, super, tenant, "approver")
		c := auth.WithIdentity(ctx, auth.Identity{Subject: admin, Role: "authenticated", TenantID: tenant})

		var wg sync.WaitGroup
		var putErr, rmErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, putErr = roles.SetRoleMembers(c, "approver", []string{b}) }()
		go func() { defer wg.Done(); <-start; _, rmErr = store.RemoveMember(c, b) }()
		close(start)
		wg.Wait()

		if rmErr != nil {
			t.Fatalf("round %d: RemoveMember: %v", round, rmErr)
		}
		if putErr != nil && !errors.Is(putErr, approval.ErrValidation) {
			t.Fatalf("round %d: SetRoleMembers: %v, want nil or ErrValidation", round, putErr)
		}
		if img := imageOf(t, super, tenant, b); img.Status != "removed" || img.Roles != 0 {
			t.Fatalf("round %d: member = %+v, want removed with zero workflow_role_members rows", round, img)
		}
	}
}
