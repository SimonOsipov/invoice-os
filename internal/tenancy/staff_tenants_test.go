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
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	staffGatewayToken  = "staff-tenants-test-gateway-token"
	forbiddenBody      = `{"error":"forbidden"}`
	internalBody       = `{"error":"internal server error"}`
	unreachableSubject = "11111111-1111-1111-1111-111111111111"
	devHouseTenant     = "a5c0a5c0-a5c0-a5c0-a5c0-a5c0a5c0a5c0"
)

var staffTenantKeys = []string{"created_at", "id", "kind", "name"}

// staffCtx returns the context auth.RequireStaff builds for a staff caller: the key is private to auth.
func staffCtx(subject string) context.Context {
	var got context.Context
	h := auth.RequireStaff(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Context() }))
	ctx := auth.WithTenantlessCaller(context.Background(), auth.Identity{Subject: subject, Role: "authenticated", Staff: true})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/staff/tenants", nil).WithContext(ctx))
	return got
}

// adoptHouseTenant returns the committed house tenant, writing the dev house row only when the table is empty.
func adoptHouseTenant(t *testing.T, super *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	read := func() (string, error) {
		var id string
		err := super.QueryRow(ctx, `SELECT tenant_id::text FROM public.house_company`).Scan(&id)
		return id, err
	}
	id, err := read()
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := super.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'ASComply (dev)') ON CONFLICT DO NOTHING`, devHouseTenant); err != nil {
			t.Fatalf("seed the dev house tenant: %v", err)
		}
		if _, err := super.Exec(ctx, `INSERT INTO public.house_company (tenant_id) VALUES ($1) ON CONFLICT DO NOTHING`, devHouseTenant); err != nil {
			t.Fatalf("seed the house row: %v", err)
		}
		id, err = read()
	}
	if err != nil {
		t.Fatalf("read the house row: %v", err)
	}
	return id
}

// houseMember seeds a membership of the house tenant and deletes it after the test; the house tenant stays.
func houseMember(t *testing.T, super *pgxpool.Pool, status string) (houseID, subject string) {
	t.Helper()
	houseID = adoptHouseTenant(t, super)
	subject = uuid.NewString()
	seedMembership(t, super, houseID, subject, "admin", status)
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM memberships WHERE tenant_id = $1 AND user_id = $2`, houseID, subject)
	})
	return houseID, subject
}

// customerMember seeds an active member of a fresh customer tenant.
func customerMember(t *testing.T, super *pgxpool.Pool) (tenantID, subject string) {
	t.Helper()
	tenantID = seedTenant(t, super, "staff-tenants customer "+uuid.NewString()[:8])
	subject = uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin", "active")
	return tenantID, subject
}

func seedKindedTenant(t *testing.T, super *pgxpool.Pool, name, kind string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := super.Exec(context.Background(), `INSERT INTO tenants (id, name, kind) VALUES ($1, $2, $3)`, id, name, kind); err != nil {
		t.Fatalf("seed %s tenant: %v", kind, err)
	}
	t.Cleanup(func() { _, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id) })
	return id
}

type staffRig struct {
	h     http.Handler
	calls atomic.Int32 // StaffTenants calls the handler made
}

// newStaffRig serves the route through a gateway-guarded platform.App over the real store.
func newStaffRig(t *testing.T, store *Store) *staffRig {
	t.Helper()
	t.Setenv("SENTRY_DSN", "")
	app, err := platform.New("tenancy")
	if err != nil {
		t.Fatal(err)
	}
	rig := &staffRig{}
	app.Mux.HandleFunc("GET /v1/staff/tenants", StaffTenantsHandler(func(ctx context.Context) ([]StaffTenant, error) {
		rig.calls.Add(1)
		return store.StaffTenants(ctx)
	}, slog.Default()))
	app.RequireGateway(staffGatewayToken)
	rig.h = app.Handler()
	return rig
}

// staffCaller is one gateway-forwarded identity: no subject means no identity headers at all.
type staffCaller struct {
	subject, tenant string
	staff           bool
}

func (rig *staffRig) get(c staffCaller) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/v1/staff/tenants", nil)
	r.Header.Set(platform.HeaderGatewayToken, staffGatewayToken)
	if c.subject != "" {
		r.Header.Set("X-User-ID", c.subject)
		r.Header.Set("X-User-Role", "authenticated")
	}
	if c.tenant != "" {
		r.Header.Set("X-Tenant-ID", c.tenant)
	}
	if c.staff {
		r.Header.Set("X-User-Staff", "true")
	}
	rec := httptest.NewRecorder()
	rig.h.ServeHTTP(rec, r)
	return rec
}

func tenantsOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]map[string]json.RawMessage {
	t.Helper()
	var body struct {
		Tenants []map[string]json.RawMessage `json:"tenants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	out := map[string]map[string]json.RawMessage{}
	for _, row := range body.Tenants {
		var id string
		_ = json.Unmarshal(row["id"], &id)
		out[id] = row
	}
	return out
}

func wantBody(t *testing.T, rec *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if rec.Code != status || strings.TrimSpace(rec.Body.String()) != body {
		t.Errorf("got %d %q, want %d %q", rec.Code, rec.Body.String(), status, body)
	}
}

// The staff control proves the route is live, so the 403s below are not a missing route.
func TestStaffTenantsRoute_RefusesEveryNonStaffCaller(t *testing.T) {
	super, app := dbTestPools(t)
	rig := newStaffRig(t, NewStore(app))
	seeded := seedKindedTenant(t, super, "staff-tenants refuse in_house", "in_house")
	custTenant, custSubject := customerMember(t, super)
	_, staffSubject := houseMember(t, super, "active")

	for name, c := range map[string]staffCaller{
		"customer":               {subject: custSubject, tenant: custTenant},
		"tenant-less, not staff": {subject: custSubject},
		"no identity":            {},
	} {
		t.Run(name, func(t *testing.T) {
			wantBody(t, rig.get(c), http.StatusForbidden, forbiddenBody)
		})
	}
	if got := rig.calls.Load(); got != 0 {
		t.Errorf("StaffTenants called %d times for non-staff callers, want 0", got)
	}

	rec := rig.get(staffCaller{subject: staffSubject, staff: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("staff control = %d %q, want 200", rec.Code, rec.Body.String())
	}
	if _, ok := tenantsOf(t, rec)[seeded]; !ok {
		t.Errorf("staff control did not list the seeded tenant %s", seeded)
	}
}

func TestStaffTenantsRoute_StaffSeesEveryTenant(t *testing.T) {
	super, app := dbTestPools(t)
	rig := newStaffRig(t, NewStore(app))
	inHouse := seedKindedTenant(t, super, "staff-tenants in_house", "in_house")
	firm := seedKindedTenant(t, super, "staff-tenants firm", "firm")
	houseID, subject := houseMember(t, super, "active")

	want := map[string][2]string{inHouse: {"staff-tenants in_house", "in_house"}, firm: {"staff-tenants firm", "firm"}}
	for name, c := range map[string]staffCaller{
		"tenant-bearing staff": {subject: subject, tenant: houseID, staff: true},
		"tenant-less staff":    {subject: subject, staff: true},
	} {
		t.Run(name, func(t *testing.T) {
			rec := rig.get(c)
			if rec.Code != http.StatusOK {
				t.Fatalf("got %d %q, want 200", rec.Code, rec.Body.String())
			}
			got := tenantsOf(t, rec)
			for id, nk := range want {
				row, ok := got[id]
				if !ok {
					t.Errorf("tenant %s missing from the list", id)
					continue
				}
				keys := make([]string, 0, len(row))
				for k := range row {
					keys = append(keys, k)
				}
				slices.Sort(keys)
				if !slices.Equal(keys, staffTenantKeys) {
					t.Errorf("tenant %s keys = %v, want %v", id, keys, staffTenantKeys)
				}
				var name, kind, created string
				_ = json.Unmarshal(row["name"], &name)
				_ = json.Unmarshal(row["kind"], &kind)
				_ = json.Unmarshal(row["created_at"], &created)
				if name != nk[0] || kind != nk[1] {
					t.Errorf("tenant %s = name %q kind %q, want %q %q", id, name, kind, nk[0], nk[1])
				}
				if _, err := time.Parse(time.RFC3339Nano, created); err != nil {
					t.Errorf("tenant %s created_at %q is not RFC 3339: %v", id, created, err)
				}
			}
		})
	}
	if got := rig.calls.Load(); got != 2 {
		t.Errorf("StaffTenants called %d times, want 2", got)
	}
}

// The staff header is a claim, not proof: the function re-checks the subject in the database.
func TestStaffTenantsRoute_AForgedStaffHeaderIsRefusedByTheFunction(t *testing.T) {
	super, app := dbTestPools(t)
	rig := newStaffRig(t, NewStore(app))
	custTenant, custSubject := customerMember(t, super)
	_, houseSubject := houseMember(t, super, "active")

	for name, c := range map[string]staffCaller{
		"tenant-bearing": {subject: custSubject, tenant: custTenant, staff: true},
		"tenant-less":    {subject: custSubject, staff: true},
	} {
		t.Run(name, func(t *testing.T) {
			wantBody(t, rig.get(c), http.StatusForbidden, forbiddenBody)
		})
	}
	if got := rig.calls.Load(); got != 2 {
		t.Errorf("StaffTenants called %d times, want 2: the refusal must come from the function, not the platform", got)
	}
	if rec := rig.get(staffCaller{subject: houseSubject, staff: true}); rec.Code != http.StatusOK {
		t.Errorf("house member control = %d %q, want 200", rec.Code, rec.Body.String())
	}
}

func TestStaffTenantsRoute_ASuspendedHouseMemberIsRefused(t *testing.T) {
	super, app := dbTestPools(t)
	rig := newStaffRig(t, NewStore(app))
	_, suspended := houseMember(t, super, "suspended")
	_, active := houseMember(t, super, "active")

	wantBody(t, rig.get(staffCaller{subject: suspended, staff: true}), http.StatusForbidden, forbiddenBody)
	if rec := rig.get(staffCaller{subject: active, staff: true}); rec.Code != http.StatusOK {
		t.Errorf("active house member control = %d %q, want 200", rec.Code, rec.Body.String())
	}
}

func TestStoreStaffTenants_RefusesWithoutAStaffCaller(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, "postgres://invoice_app:x@127.0.0.1:1/invoice_os?sslmode=disable&connect_timeout=2")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := NewStore(pool)

	if _, err := store.StaffTenants(ctx); !errors.Is(err, db.ErrNotStaff) {
		t.Errorf("no staff caller: err = %v, want db.ErrNotStaff", err)
	}
	for _, subject := range []string{"not-a-uuid", ""} {
		if _, err := store.StaffTenants(staffCtx(subject)); !errors.Is(err, db.ErrNotStaff) {
			t.Errorf("subject %q: err = %v, want db.ErrNotStaff", subject, err)
		}
	}
	// Control: a UUID subject passes the parse and reaches the pool, so the refusals above are specific.
	if _, err := store.StaffTenants(staffCtx(unreachableSubject)); err == nil || errors.Is(err, db.ErrNotStaff) {
		t.Errorf("uuid subject on an unreachable pool: err = %v, want a connection error", err)
	}
	if n := pool.Stat().TotalConns(); n != 0 {
		t.Errorf("pool opened %d connections, want 0", n)
	}
}

func TestStaffTenantsHandler_Contract(t *testing.T) {
	created := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	one := []StaffTenant{{ID: "t1", Name: "Acme", Kind: "firm", CreatedAt: created}}
	lostGrant := fmt.Errorf("tenancy: staff list tenants: %w", &pgconn.PgError{Code: "42501", Message: "permission denied for function staff_read_tenants"})
	cases := []struct {
		name    string
		list    func(context.Context) ([]StaffTenant, error)
		status  int
		body    string
		wantLog bool
	}{
		{"nil list is an empty array", func(context.Context) ([]StaffTenant, error) { return nil, nil }, 200, `{"tenants":[]}`, false},
		{"empty list is an empty array", func(context.Context) ([]StaffTenant, error) { return []StaffTenant{}, nil }, 200, `{"tenants":[]}`, false},
		{"one tenant", func(context.Context) ([]StaffTenant, error) { return one, nil }, 200,
			`{"tenants":[{"id":"t1","name":"Acme","kind":"firm","created_at":"2026-10-13T12:00:00Z"}]}`, false},
		{"not staff", func(context.Context) ([]StaffTenant, error) { return nil, db.ErrNotStaff }, 403, forbiddenBody, false},
		{"wrapped not staff", func(context.Context) ([]StaffTenant, error) { return nil, fmt.Errorf("x: %w", db.ErrNotStaff) }, 403, forbiddenBody, false},
		{"other error", func(context.Context) ([]StaffTenant, error) { return nil, errors.New("x") }, 500, internalBody, true},
		{"a lost grant is a server error", func(context.Context) ([]StaffTenant, error) { return nil, lostGrant }, 500, internalBody, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			rec := httptest.NewRecorder()
			StaffTenantsHandler(tc.list, slog.New(slog.NewTextHandler(&logs, nil))).
				ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/staff/tenants", nil))
			wantBody(t, rec, tc.status, tc.body)
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			if logged := strings.Contains(logs.String(), "tenancy: staff list tenants"); logged != tc.wantLog {
				t.Errorf("logged = %v, want %v (log %q)", logged, tc.wantLog, logs.String())
			}
		})
	}
}

// A revoked EXECUTE grant raises the same 42501 as the function's own refusal; only the refusal may become 403.
func TestStoreStaffTenants_ALostGrantIsAServerError(t *testing.T) {
	super, _ := dbTestPools(t)
	_, houseSubject := houseMember(t, super, "active")
	_, strangerSubject := customerMember(t, super)

	call := func(revoke bool, subject string) error {
		ctx := context.Background()
		tx, err := super.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if revoke {
			if _, err := tx.Exec(ctx, `REVOKE EXECUTE ON FUNCTION public.staff_read_tenants(uuid) FROM invoice_app`); err != nil {
				t.Fatalf("revoke: %v", err)
			}
		}
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE invoice_app`); err != nil {
			t.Fatalf("set role: %v", err)
		}
		_, err = tx.Exec(ctx, `SELECT id::text, name, kind, created_at FROM public.staff_read_tenants($1)`, subject)
		return err
	}
	pgErr := func(err error) *pgconn.PgError {
		var pe *pgconn.PgError
		if !errors.As(err, &pe) {
			t.Fatalf("err = %v, want a *pgconn.PgError", err)
		}
		return pe
	}

	if err := call(false, houseSubject); err != nil {
		t.Fatalf("control: a house member with the grant intact: %v", err)
	}

	lost := call(true, houseSubject)
	if pe := pgErr(lost); pe.Code != "42501" || !strings.Contains(pe.Message, "permission denied") {
		t.Fatalf("lost grant = %s %q, want 42501 permission denied", pe.Code, pe.Message)
	}
	if got := staffReadError(lost); got == nil || errors.Is(got, db.ErrNotStaff) {
		t.Errorf("staffReadError(lost grant) = %v, want a non-nil error that is not db.ErrNotStaff", got)
	}

	refused := call(false, strangerSubject)
	if pe := pgErr(refused); pe.Code != "42501" || !strings.HasSuffix(pe.Message, ": actor is not staff") {
		t.Fatalf("stranger = %s %q, want 42501 ...: actor is not staff", pe.Code, pe.Message)
	}
	if got := staffReadError(refused); !errors.Is(got, db.ErrNotStaff) {
		t.Errorf("staffReadError(the function's refusal) = %v, want db.ErrNotStaff", got)
	}
}
