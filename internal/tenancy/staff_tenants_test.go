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
	staffGatewayToken = "staff-tenants-test-gateway-token"
	forbiddenBody     = `{"error":"forbidden"}`
	internalBody      = `{"error":"internal server error"}`
	devHouseTenant    = "a5c0a5c0-a5c0-a5c0-a5c0-a5c0a5c0a5c0"
)

var staffTenantKeys = []string{"active_members", "created_at", "demo", "id", "invoice_count", "kind", "last_activity_at", "name"}

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
	createdAt := map[string]time.Time{}
	for id := range want {
		var c time.Time
		if err := super.QueryRow(context.Background(), `SELECT created_at FROM tenants WHERE id = $1`, id).Scan(&c); err != nil {
			t.Fatalf("read created_at of %s: %v", id, err)
		}
		createdAt[id] = c
	}
	// The tenant header does not scope a staff read: a caller pinned to a customer tenant still sees every tenant.
	custTenant, _ := customerMember(t, super)
	for name, c := range map[string]staffCaller{
		"tenant-bearing staff":              {subject: subject, tenant: houseID, staff: true},
		"tenant-less staff":                 {subject: subject, staff: true},
		"staff pinned to a customer tenant": {subject: subject, tenant: custTenant, staff: true},
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
				for k, want := range map[string]string{"active_members": "0", "invoice_count": "0", "last_activity_at": "null", "demo": "false"} {
					if got := string(row[k]); got != want {
						t.Errorf("tenant %s %s = %s, want %s", id, k, got, want)
					}
				}
				if at, err := time.Parse(time.RFC3339Nano, created); err != nil {
					t.Errorf("tenant %s created_at %q is not RFC 3339: %v", id, created, err)
				} else if !at.Equal(createdAt[id]) {
					t.Errorf("tenant %s created_at = %s, want the row's %s", id, at, createdAt[id])
				}
			}
		})
	}
	if got := rig.calls.Load(); got != 3 {
		t.Errorf("StaffTenants called %d times, want 3", got)
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
	t.Run("staff claim with a subject that is not a uuid", func(t *testing.T) {
		wantBody(t, rig.get(staffCaller{subject: "not-a-uuid", staff: true}), http.StatusForbidden, forbiddenBody)
	})
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
	if _, err := store.StaffTenants(staffCtx(uuid.NewString())); err == nil || errors.Is(err, db.ErrNotStaff) {
		t.Errorf("uuid subject on an unreachable pool: err = %v, want a connection error", err)
	}
	if n := pool.Stat().TotalConns(); n != 0 {
		t.Errorf("pool opened %d connections, want 0", n)
	}
}

func TestStaffTenantsHandler_Contract(t *testing.T) {
	created := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	one := []StaffTenant{{ID: "t1", Name: "Acme", Kind: "firm", CreatedAt: created, Demo: true, ActiveMembers: 2, InvoiceCount: 3}}
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
			`{"tenants":[{"id":"t1","name":"Acme","kind":"firm","created_at":"2026-10-13T12:00:00Z","demo":true,"active_members":2,"invoice_count":3,"last_activity_at":null}]}`, false},
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
			if logged := strings.Contains(logs.String(), "tenancy: staff tenants response"); logged != tc.wantLog {
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
	if got := staffReadError("op", lost); got == nil || errors.Is(got, db.ErrNotStaff) {
		t.Errorf("staffReadError(lost grant) = %v, want a non-nil error that is not db.ErrNotStaff", got)
	}

	refused := call(false, strangerSubject)
	if pe := pgErr(refused); pe.Code != "42501" || !strings.HasSuffix(pe.Message, ": actor is not staff") {
		t.Fatalf("stranger = %s %q, want 42501 ...: actor is not staff", pe.Code, pe.Message)
	}
	if got := staffReadError("op", refused); !errors.Is(got, db.ErrNotStaff) {
		t.Errorf("staffReadError(the function's refusal) = %v, want db.ErrNotStaff", got)
	}
}

func TestStaffReadError_OnlyTheFunctionsOwnRefusalIsNotStaff(t *testing.T) {
	refusal := "staff_read_tenants: actor is not staff"
	cases := []struct {
		name     string
		err      error
		notStaff bool
	}{
		{"the function's refusal", &pgconn.PgError{Code: "42501", Message: refusal}, true},
		{"the refusal, wrapped", fmt.Errorf("w: %w", &pgconn.PgError{Code: "42501", Message: refusal}), true},
		{"a lost function grant", &pgconn.PgError{Code: "42501", Message: "permission denied for function staff_read_tenants"}, false},
		{"a lost column grant", &pgconn.PgError{Code: "42501", Message: "permission denied for table tenants"}, false},
		{"the refusal text under another SQLSTATE", &pgconn.PgError{Code: "P0001", Message: refusal}, false},
		{"the refusal text from a non-database error", errors.New(refusal), false},
		{"the refusal text with a suffix after it", &pgconn.PgError{Code: "42501", Message: refusal + " (retry)"}, false},
		{"the refusal text without its separator", &pgconn.PgError{Code: "42501", Message: "actor is not staff"}, false},
		{"an unrelated error", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := staffReadError("op", tc.err)
			if got == nil {
				t.Fatal("staffReadError returned nil for an error")
			}
			if errors.Is(got, db.ErrNotStaff) != tc.notStaff {
				t.Errorf("errors.Is(ErrNotStaff) = %v, want %v (got %v)", !tc.notStaff, tc.notStaff, got)
			}
			if !tc.notStaff && !errors.Is(got, tc.err) {
				t.Errorf("a server fault must keep its cause: %v does not wrap %v", got, tc.err)
			}
		})
	}
	if got := staffReadError("op", nil); got != nil {
		t.Errorf("staffReadError(nil) = %v, want nil", got)
	}
}

// seedTenantAt commits a firm tenant created at a fixed time, so ordering specs control created_at.
func seedTenantAt(t *testing.T, super *pgxpool.Pool, name string, createdAt time.Time) string {
	t.Helper()
	id := uuid.NewString()
	seedTenantRowAt(t, super, id, name, createdAt)
	return id
}

func seedTenantRowAt(t *testing.T, super *pgxpool.Pool, id, name string, createdAt time.Time) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO tenants (id, name, kind, created_at) VALUES ($1, $2, 'firm', $3)`, id, name, createdAt); err != nil {
		t.Fatalf("seed tenant %s: %v", name, err)
	}
	t.Cleanup(func() { _, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id) })
}

// seedInvoices adds n invoices; the cleanup runs before the tenant's own (invoices.entity_id is RESTRICT).
func seedInvoices(t *testing.T, super *pgxpool.Pool, tenant string, n int) {
	t.Helper()
	ctx := context.Background()
	entity := uuid.NewString()
	if _, err := super.Exec(ctx, `INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`, entity, tenant, "entity-"+entity[:8]); err != nil {
		t.Fatalf("seed an entity: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM invoices WHERE tenant_id = $1`, tenant)
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE tenant_id = $1`, tenant)
	})
	for i := 0; i < n; i++ {
		if _, err := super.Exec(ctx,
			`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, status) VALUES ($1, $2, $3, $4, 'draft')`,
			uuid.NewString(), tenant, entity, fmt.Sprintf("ST-%d", i)); err != nil {
			t.Fatalf("seed invoice %d: %v", i, err)
		}
	}
}

// seedAuditAt appends an audit row; audit_log is append-only, so the row stays.
func seedAuditAt(t *testing.T, super *pgxpool.Pool, tenant string, at time.Time) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO audit_log (tenant_id, actor, event, created_at) VALUES ($1, 'staff-tenants-test', 'staff_tenants.seed', $2)`, tenant, at); err != nil {
		t.Fatalf("seed an audit row: %v", err)
	}
}

// orderedIDs returns the listed ids in served order, restricted to the wanted set.
func orderedIDs(t *testing.T, rec *httptest.ResponseRecorder, among ...string) []string {
	t.Helper()
	var body struct {
		Tenants []struct {
			ID string `json:"id"`
		} `json:"tenants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	var out []string
	for _, row := range body.Tenants {
		if slices.Contains(among, row.ID) {
			out = append(out, row.ID)
		}
	}
	return out
}

func TestStaffTenantsRoute_CountsAndLastActivity(t *testing.T) {
	super, app := dbTestPools(t)
	rig := newStaffRig(t, NewStore(app))
	_, staff := houseMember(t, super, "active")
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	x := seedTenant(t, super, "staff-tenants counts X "+uuid.NewString()[:8])
	for _, m := range []struct{ role, status string }{{"admin", "active"}, {"reviewer", "active"}, {"preparer", "suspended"}, {"preparer", "invited"}} {
		seedMembership(t, super, x, uuid.NewString(), m.role, m.status)
	}
	seedInvoices(t, super, x, 3)
	seedAuditAt(t, super, x, t0)
	seedAuditAt(t, super, x, t0.Add(time.Minute))
	y := seedTenant(t, super, "staff-tenants counts Y "+uuid.NewString()[:8])
	// z has different counts: a global count, or a members x invoices join, would leak into x.
	z := seedTenant(t, super, "staff-tenants counts Z "+uuid.NewString()[:8])
	seedMembership(t, super, z, uuid.NewString(), "admin", "active")
	seedInvoices(t, super, z, 1)
	seedAuditAt(t, super, z, t0.Add(-time.Minute))

	rec := rig.get(staffCaller{subject: staff, staff: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %q, want 200", rec.Code, rec.Body.String())
	}
	got := tenantsOf(t, rec)
	rowX, okX := got[x]
	rowY, okY := got[y]
	rowZ, okZ := got[z]
	if !okX || !okY || !okZ {
		t.Fatalf("seeded tenants missing from the list: X=%v Y=%v Z=%v", okX, okY, okZ)
	}

	wantCounts := func(name string, row map[string]json.RawMessage, active, invoices string) {
		for k, want := range map[string]string{"active_members": active, "invoice_count": invoices} {
			if raw, present := row[k]; !present || string(raw) != want {
				t.Errorf("%s: %s = %s (present=%v), want %s", name, k, raw, present, want)
			}
		}
	}
	wantCounts("X", rowX, "2", "3")
	wantCounts("Y", rowY, "0", "0")
	wantCounts("Z", rowZ, "1", "1")
	if raw, present := rowY["last_activity_at"]; !present || string(raw) != "null" {
		t.Errorf("Y: last_activity_at = %s (present=%v), want a present null", raw, present)
	}
	for name, c := range map[string]struct {
		row  map[string]json.RawMessage
		want time.Time
	}{"X": {rowX, t0.Add(time.Minute)}, "Z": {rowZ, t0.Add(-time.Minute)}} {
		var s string
		if err := json.Unmarshal(c.row["last_activity_at"], &s); err != nil {
			t.Errorf("%s: last_activity_at = %s, want an RFC 3339 string: %v", name, c.row["last_activity_at"], err)
			continue
		}
		if at, err := time.Parse(time.RFC3339Nano, s); err != nil || !at.Equal(c.want) {
			t.Errorf("%s: last_activity_at = %q, want %s (the newest audit row)", name, s, c.want.Format(time.RFC3339))
		}
	}
}

func TestStaffTenantsRoute_OrdersByLastActivity(t *testing.T) {
	super, app := dbTestPools(t)
	rig := newStaffRig(t, NewStore(app))
	_, staff := houseMember(t, super, "active")
	now := time.Now().UTC().Truncate(time.Second)
	t0 := now.Add(-time.Hour)
	suffix := uuid.NewString()[:8]

	// P is the oldest tenant but has the newest activity; R is the newest tenant but has none.
	p := seedTenantAt(t, super, "staff-tenants order P "+suffix, now.Add(-4*time.Hour))
	q := seedTenantAt(t, super, "staff-tenants order Q "+suffix, now.Add(-3*time.Hour))
	s := seedTenantAt(t, super, "staff-tenants order S "+suffix, now.Add(-2*time.Hour))
	r := seedTenantAt(t, super, "staff-tenants order R "+suffix, now.Add(-30*time.Minute))
	seedAuditAt(t, super, p, t0.Add(2*time.Minute))
	seedAuditAt(t, super, q, t0)

	// Equal created_at and no activity: id ascending breaks the tie.
	tie := now.Add(-5 * time.Hour)
	u1, u2 := uuid.NewString(), uuid.NewString()
	if u1 > u2 {
		u1, u2 = u2, u1
	}
	seedTenantRowAt(t, super, u2, "staff-tenants order U2 "+suffix, tie)
	seedTenantRowAt(t, super, u1, "staff-tenants order U1 "+suffix, tie)

	rec := rig.get(staffCaller{subject: staff, staff: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %q, want 200", rec.Code, rec.Body.String())
	}
	got := orderedIDs(t, rec, p, q, r, s, u1, u2)
	want := []string{p, q, r, s, u1, u2}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want P, Q, R, S, U1, U2 = %v", got, want)
	}
}

func TestStaffTenantsRoute_DemoFollowsDemoTenants(t *testing.T) {
	super, app := dbTestPools(t)
	rig := newStaffRig(t, NewStore(app))
	_, staff := houseMember(t, super, "active")
	fresh := seedTenant(t, super, "staff-tenants fresh "+uuid.NewString()[:8])

	// db.DemoTenants[0]; the cleanup removes the row only when this test created it.
	demo := db.DemoTenants[0]
	tag, err := super.Exec(context.Background(), `INSERT INTO tenants (id, name) VALUES ($1, 'staff-tenants demo') ON CONFLICT DO NOTHING`, demo)
	if err != nil {
		t.Fatalf("seed the demo tenant: %v", err)
	}
	if tag.RowsAffected() == 1 {
		t.Cleanup(func() { _, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, demo) })
	}

	rec := rig.get(staffCaller{subject: staff, staff: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %q, want 200", rec.Code, rec.Body.String())
	}
	got := tenantsOf(t, rec)
	if len(got) == 0 {
		t.Fatal("the list is empty")
	}
	if raw := string(got[demo]["demo"]); raw != "true" {
		t.Errorf("demo tenant %s demo = %q, want true", demo, raw)
	}
	if raw := string(got[fresh]["demo"]); raw != "false" {
		t.Errorf("fresh tenant demo = %q, want false", raw)
	}
	var wrong []string
	for id, row := range got {
		if !slices.Contains(db.DemoTenants, id) && string(row["demo"]) != "false" {
			wrong = append(wrong, id)
		}
	}
	if len(wrong) > 0 {
		t.Errorf("%d tenants outside db.DemoTenants do not read demo = false, e.g. %s", len(wrong), wrong[0])
	}
}

func TestStaffTenantsRoute_ReadsWriteNoAuditRow(t *testing.T) {
	super, app := dbTestPools(t)
	rig := newStaffRig(t, NewStore(app))
	houseID, staff := houseMember(t, super, "active")
	x := seedTenant(t, super, "staff-tenants no-audit "+uuid.NewString()[:8])
	auditAt := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	seedAuditAt(t, super, x, auditAt)

	count := func() [3]int {
		var n [3]int
		for i, q := range []struct{ sql, arg string }{
			{`SELECT count(*) FROM audit_log WHERE tenant_id = $1`, x},
			{`SELECT count(*) FROM audit_log WHERE tenant_id = $1`, houseID},
			{`SELECT count(*) FROM public.staff_audit_log WHERE actor = $1`, staff},
		} {
			if err := super.QueryRow(context.Background(), q.sql, q.arg).Scan(&n[i]); err != nil {
				t.Fatalf("count: %v", err)
			}
		}
		return n
	}
	before := count()
	if before[0] != 1 {
		t.Fatalf("control: X has %d audit rows before the reads, want 1", before[0])
	}
	for i := 0; i < 2; i++ {
		rec := rig.get(staffCaller{subject: staff, staff: true})
		if rec.Code != http.StatusOK {
			t.Fatalf("read %d = %d %q, want 200", i, rec.Code, rec.Body.String())
		}
		row, ok := tenantsOf(t, rec)[x]
		if !ok {
			t.Fatalf("read %d did not list X", i)
		}
		// Control: the read really ran the activity query over X's audit row.
		var last string
		_ = json.Unmarshal(row["last_activity_at"], &last)
		if at, err := time.Parse(time.RFC3339Nano, last); err != nil || !at.Equal(auditAt) {
			t.Fatalf("read %d: X last_activity_at = %s, want %s", i, row["last_activity_at"], auditAt.Format(time.RFC3339))
		}
	}
	if after := count(); after != before {
		t.Errorf("audit counts (X, house, staff_audit_log by actor) = %v after the reads, want %v", after, before)
	}
}
