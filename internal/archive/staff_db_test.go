// staff_db_test.go: the staff evidence bundle against a real Postgres. Fixtures are committed (the stores open
// their own transactions) and purged by mustCommitFixture; the tests write no audit_log row.
package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const staffDevHouseTenant = "a5c0a5c0-a5c0-a5c0-a5c0-a5c0a5c0a5c0"

// staffCtx is the context auth.RequireStaff builds for a staff caller: the key is private to auth.
func staffCtx(t *testing.T, subject string) context.Context {
	t.Helper()
	var got context.Context
	h := auth.RequireStaff(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Context() }))
	ctx := auth.WithTenantlessCaller(context.Background(), auth.Identity{Subject: subject, Role: "authenticated", Staff: true})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/staff/tenants", nil).WithContext(ctx))
	if got == nil {
		t.Fatal("RequireStaff refused the staff caller")
	}
	return got
}

// houseStaffSubject seeds an active member of the house tenant (a dev house when none) and removes it after the test.
func houseStaffSubject(t *testing.T, super *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	var house string
	err := super.QueryRow(ctx, `SELECT tenant_id::text FROM public.house_company`).Scan(&house)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := super.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'ASComply (dev)') ON CONFLICT DO NOTHING`, staffDevHouseTenant); err != nil {
			t.Fatalf("seed the dev house tenant: %v", err)
		}
		if _, err := super.Exec(ctx, `INSERT INTO public.house_company (tenant_id) VALUES ($1) ON CONFLICT DO NOTHING`, staffDevHouseTenant); err != nil {
			t.Fatalf("seed the house row: %v", err)
		}
		err = super.QueryRow(ctx, `SELECT tenant_id::text FROM public.house_company`).Scan(&house)
	}
	if err != nil {
		t.Fatalf("read the house row: %v", err)
	}
	subject := uuid.NewString()
	if _, err := super.Exec(ctx, `INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'admin', 'active')`, house, subject); err != nil {
		t.Fatalf("seed house member: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM memberships WHERE tenant_id = $1 AND user_id = $2`, house, subject)
	})
	return subject
}

// bundleFx is one committed tenant with one company and its invoices.
type bundleFx struct {
	tenant, entity, admin string
	invoices              []string
	req                   Request
}

var staffPeriodFrom = time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)

// plantBundleTenant commits a tenant with an admin member, one company and n invoices in the period. The first
// invoice carries line items, a submission, history and an exchange with both bodies.
func plantBundleTenant(t *testing.T, super *pgxpool.Pool, label, invoiceNumber string, n int) bundleFx {
	t.Helper()
	fx := bundleFx{admin: uuid.NewString()}
	name := "Admin " + label
	fx.tenant = mustCommitFixture(t, super, func(tx pgx.Tx) string {
		tid := mustCreateTenant(t, tx, "archive-staff-"+label)
		mustCreateMembership(t, tx, tid, fx.admin, &name, nil)
		fx.entity = mustCreateEntity(t, tx, tid, "Staff "+label+" Co", "80000060-0001")
		for i := range n {
			fx.invoices = append(fx.invoices, mustCreateInvoice(t, tx, invoiceFixture{
				tenantID: tid, entityID: fx.entity, invoiceNumber: fmt.Sprintf("%s-%d", invoiceNumber, i), createdAt: staffPeriodFrom.Add(time.Duration(i) * time.Minute),
			}))
		}
		if n > 0 {
			mustCreateLineItem(t, tx, lineItemFixture{tenantID: tid, invoiceID: fx.invoices[0], lineNo: 1})
			job := mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tid, invoiceID: fx.invoices[0]})
			mustCreateHistoryRow(t, tx, historyFixture{tenantID: tid, invoiceID: fx.invoices[0], toStatus: "validated", actor: "system"})
			body := `{"label":"` + label + `"}`
			mustCreateExchange(t, tx, exchangeFixture{tenantID: tid, submissionJobID: job, invoiceID: fx.invoices[0], requestBody: &body, responseBody: &body})
		}
		return tid
	})
	fx.req = Request{EntityID: fx.entity, From: staffPeriodFrom, To: staffPeriodFrom.Add(time.Hour)}
	return fx
}

type staffRig struct {
	super *pgxpool.Pool
	app   *pgxpool.Pool
	rec   *sqlRecorder
	store *Store
	ctx   context.Context
	staff string
}

func newStaffRig(t *testing.T) *staffRig {
	t.Helper()
	super := dbSuperPool(t)
	app, rec := dbAppPoolTraced(t)
	staff := houseStaffSubject(t, super)
	return &staffRig{super: super, app: app, rec: rec, store: NewStore(app), ctx: staffCtx(t, staff), staff: staff}
}

func (r *staffRig) assemble(t *testing.T, tenant string, req Request) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := r.store.StaffAssemble(r.ctx, tenant, req, &buf, nil); err != nil {
		t.Fatalf("StaffAssemble: %v", err)
	}
	return buf.Bytes()
}

// tenantBundle is what a member of the tenant downloads.
func (r *staffRig) tenantBundle(t *testing.T, fx bundleFx) []byte {
	t.Helper()
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: fx.admin, TenantID: fx.tenant})
	var buf bytes.Buffer
	if err := r.store.Assemble(ctx, fx.req, &buf, nil); err != nil {
		t.Fatalf("Store.Assemble as the tenant admin: %v", err)
	}
	return buf.Bytes()
}

func zipEntries(t *testing.T, raw []byte) (names []string, body map[string][]byte) {
	t.Helper()
	zr := mustReadZip(t, raw)
	body = map[string][]byte{}
	for _, f := range zr.File {
		names = append(names, f.Name)
		body[f.Name] = mustReadZipEntry(t, zr, f.Name)
	}
	return names, body
}

// manifestWithoutVolatile is the manifest minus generated_at, generated_by and the manifest's own checksum.
func manifestWithoutVolatile(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	delete(doc, "generated_at")
	delete(doc, "generated_by")
	entries, _ := doc["entries"].([]any)
	kept := []any{}
	for _, e := range entries {
		if m, ok := e.(map[string]any); ok && m["name"] == "manifest.json" {
			continue
		}
		kept = append(kept, e)
	}
	doc["entries"] = kept
	return doc
}

// requireSameBundle fails unless staff and tenant bundles agree entry by entry, the manifests after the volatile fields.
func requireSameBundle(t *testing.T, staff, tenant []byte) {
	t.Helper()
	sNames, sBody := zipEntries(t, staff)
	tNames, tBody := zipEntries(t, tenant)
	if !slices.Equal(sNames, tNames) {
		t.Fatalf("staff entries = %v, tenant entries = %v, want the same names in the same order", sNames, tNames)
	}
	for _, n := range sNames {
		if n == "manifest.json" {
			continue
		}
		if !bytes.Equal(sBody[n], tBody[n]) {
			t.Errorf("entry %s differs between the staff and the tenant bundle", n)
		}
	}
	if !reflect.DeepEqual(manifestWithoutVolatile(t, sBody["manifest.json"]), manifestWithoutVolatile(t, tBody["manifest.json"])) {
		t.Errorf("manifests differ beyond generated_at, generated_by and the manifest checksum:\nstaff  %s\ntenant %s", sBody["manifest.json"], tBody["manifest.json"])
	}
}

func TestRLS_StaffAssembleEqualsTheTenantBundle(t *testing.T) {
	r := newStaffRig(t)
	fx := plantBundleTenant(t, r.super, "equal", "INV-STAFF-EQ", 2)

	tenant := r.tenantBundle(t, fx)
	staff := r.assemble(t, fx.tenant, fx.req)

	names, body := zipEntries(t, staff)
	if len(names) < 7 || !slices.ContainsFunc(names, func(n string) bool { return strings.HasPrefix(n, "bodies/") }) {
		t.Fatalf("staff bundle entries = %v, want the CSVs, bodies/ files and the manifest (the fixture must carry bodies)", names)
	}
	requireSameBundle(t, staff, tenant)

	var doc manifestDoc
	if err := json.Unmarshal(body["manifest.json"], &doc); err != nil {
		t.Fatalf("unmarshal staff manifest: %v", err)
	}
	if doc.GeneratedBy != (manifestActor{Name: "ASComply support", Kind: "raw"}) {
		t.Errorf("staff generated_by = %+v, want {ASComply support raw}", doc.GeneratedBy)
	}
	if doc.TenantID != fx.tenant {
		t.Errorf("staff manifest tenant_id = %q, want %q", doc.TenantID, fx.tenant)
	}
	var tenantDoc manifestDoc
	_, tBody := zipEntries(t, tenant)
	if err := json.Unmarshal(tBody["manifest.json"], &tenantDoc); err != nil {
		t.Fatalf("unmarshal tenant manifest: %v", err)
	}
	if tenantDoc.GeneratedBy.Name != "Admin equal" || tenantDoc.GeneratedBy.Kind == doc.GeneratedBy.Kind {
		t.Errorf("tenant generated_by = %+v, want the member's own label (Admin equal), not the staff actor", tenantDoc.GeneratedBy)
	}
}

func TestRLS_StaffAssembleWithNoInvoicesWritesHeaderOnlyCSVs(t *testing.T) {
	r := newStaffRig(t)
	fx := plantBundleTenant(t, r.super, "empty", "INV-STAFF-EMPTY", 0)

	p, err := r.store.StaffPreview(r.ctx, fx.tenant, fx.req)
	if err != nil {
		t.Fatalf("StaffPreview: %v", err)
	}
	if p.Counts != (manifestCounts{}) || p.OverLimit {
		t.Errorf("preview = counts %+v over_limit %v, want all zero", p.Counts, p.OverLimit)
	}

	staff := r.assemble(t, fx.tenant, fx.req)
	zr := mustReadZip(t, staff)
	for _, csv := range []string{"invoices.csv", "line_items.csv", "status_history.csv", "submissions.csv", "exchange.csv"} {
		if n := csvDataRecordCount(t, zr, csv); n != 0 {
			t.Errorf("%s holds %d data row(s), want only the header", csv, n)
		}
	}
	if got := manifestOracleCounts(t, zr); got != (manifestCounts{}) {
		t.Errorf("manifest counts = %+v, want all zero", got)
	}
	requireSameBundle(t, staff, r.tenantBundle(t, fx))
}

func TestRLS_StaffPreviewEqualsTheTenantPreview(t *testing.T) {
	r := newStaffRig(t)
	fx := plantBundleTenant(t, r.super, "preview", "INV-STAFF-PV", 2)

	tenantCtx := auth.WithIdentity(context.Background(), auth.Identity{Subject: fx.admin, TenantID: fx.tenant})
	want, err := r.store.Preview(tenantCtx, fx.req)
	if err != nil {
		t.Fatalf("Store.Preview as the tenant admin: %v", err)
	}
	if want.Counts.Invoices != 2 || want.Counts.BodyFiles == 0 {
		t.Fatalf("tenant preview counts = %+v, want 2 invoices and body files (fixture control)", want.Counts)
	}
	got, err := r.store.StaffPreview(r.ctx, fx.tenant, fx.req)
	if err != nil {
		t.Fatalf("StaffPreview: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("StaffPreview = %+v, want the tenant preview %+v", got, want)
	}
}

func TestRLS_StaffEntitiesListsOneTenantInNameOrder(t *testing.T) {
	r := newStaffRig(t)
	var alpha, beta string
	var gamma []string
	tin := "80000061-0001"
	tenant := mustCommitFixture(t, r.super, func(tx pgx.Tx) string {
		tid := mustCreateTenant(t, tx, "archive-staff-entities")
		beta = mustCreateEntity(t, tx, tid, "Beta", tin)
		alpha = mustCreateEntity(t, tx, tid, "Alpha", "")
		gamma = append(gamma, mustCreateEntity(t, tx, tid, "Gamma", ""), mustCreateEntity(t, tx, tid, "Gamma", ""))
		return tid
	})
	var other string
	mustCommitFixture(t, r.super, func(tx pgx.Tx) string {
		other = mustCreateTenant(t, tx, "archive-staff-entities-other")
		mustCreateEntity(t, tx, other, "Aardvark", "")
		return other
	})
	sort.Strings(gamma)

	got, err := r.store.StaffEntities(r.ctx, tenant)
	if err != nil {
		t.Fatalf("StaffEntities: %v", err)
	}
	want := []StaffEntity{
		{ID: alpha, Name: "Alpha"},
		{ID: beta, Name: "Beta", TIN: &tin},
		{ID: gamma[0], Name: "Gamma"},
		{ID: gamma[1], Name: "Gamma"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("StaffEntities = %+v, want %+v (name then id, NULL TIN as nil, no entity of another tenant)", got, want)
	}
}

func TestRLS_StaffEntitiesEmptyTenantIsAnEmptyList(t *testing.T) {
	r := newStaffRig(t)
	tenant := mustCommitFixture(t, r.super, func(tx pgx.Tx) string { return mustCreateTenant(t, tx, "archive-staff-no-company") })

	got, err := r.store.StaffEntities(r.ctx, tenant)
	if err != nil {
		t.Fatalf("StaffEntities: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("StaffEntities = %#v, want a non-nil empty list", got)
	}
}

func TestRLS_StaffAssembleRefusesAnotherTenantsEntity(t *testing.T) {
	r := newStaffRig(t)
	own := plantBundleTenant(t, r.super, "refuse-own", "INV-STAFF-RF", 1)
	other := plantBundleTenant(t, r.super, "refuse-other", "INV-STAFF-RF", 1)

	if _, err := r.store.StaffPreview(r.ctx, other.tenant, other.req); err != nil {
		t.Fatalf("control: StaffPreview of the entity under its own tenant: %v", err)
	}

	var w countingWriter
	err := r.store.StaffAssemble(r.ctx, own.tenant, other.req, &w, nil)
	if !errors.Is(err, ErrEntityNotFound) {
		t.Errorf("StaffAssemble(own tenant, other's entity) error = %v, want ErrEntityNotFound", err)
	}
	if w.n != 0 {
		t.Errorf("StaffAssemble wrote %d byte(s), want 0", w.n)
	}
	if _, err := r.store.StaffPreview(r.ctx, own.tenant, other.req); !errors.Is(err, ErrEntityNotFound) {
		t.Errorf("StaffPreview(own tenant, other's entity) error = %v, want ErrEntityNotFound", err)
	}
}

func TestRLS_StaffBundleHoldsOnlyThePathTenant(t *testing.T) {
	r := newStaffRig(t)
	a := plantBundleTenant(t, r.super, "only-a", "INV-STAFF-SAME", 1)
	b := plantBundleTenant(t, r.super, "only-b", "INV-STAFF-SAME", 1)

	staff := r.assemble(t, a.tenant, a.req)
	names, body := zipEntries(t, staff)
	if len(names) == 0 {
		t.Fatal("empty bundle")
	}
	invoices := string(body["invoices.csv"])
	if !strings.Contains(invoices, a.invoices[0]) {
		t.Errorf("invoices.csv does not hold the path tenant's invoice %s", a.invoices[0])
	}
	for n, content := range body {
		for _, foreign := range []string{b.tenant, b.invoices[0], b.entity} {
			if strings.Contains(string(content), foreign) {
				t.Errorf("entry %s holds %s of the other tenant", n, foreign)
			}
		}
	}
}

func TestRLS_StaffBundleUnknownTenantIsNotFound(t *testing.T) {
	r := newStaffRig(t)
	known := plantBundleTenant(t, r.super, "unknown", "INV-STAFF-UN", 1)
	req := Request{EntityID: uuid.NewString(), From: staffPeriodFrom, To: staffPeriodFrom.Add(time.Hour)}

	for _, id := range []string{uuid.NewString(), "nope"} {
		r.rec.reset()
		if _, err := r.store.StaffEntities(r.ctx, id); !errors.Is(err, ErrTenantNotFound) {
			t.Errorf("StaffEntities(%q) error = %v, want ErrTenantNotFound", id, err)
		}
		if _, err := r.store.StaffPreview(r.ctx, id, req); !errors.Is(err, ErrTenantNotFound) {
			t.Errorf("StaffPreview(%q) error = %v, want ErrTenantNotFound", id, err)
		}
		var w countingWriter
		if err := r.store.StaffAssemble(r.ctx, id, req, &w, nil); !errors.Is(err, ErrTenantNotFound) {
			t.Errorf("StaffAssemble(%q) error = %v, want ErrTenantNotFound", id, err)
		}
		if w.n != 0 {
			t.Errorf("StaffAssemble(%q) wrote %d byte(s), want 0", id, w.n)
		}
		if got := r.rec.mentioning("set_config('app.current_tenant'"); len(got) != 0 {
			t.Errorf("tenant id %q opened a tenant transaction: %q", id, got)
		}
	}

	r.rec.reset()
	if _, err := r.store.StaffEntities(r.ctx, known.tenant); err != nil {
		t.Fatalf("control: StaffEntities of a known tenant: %v", err)
	}
	if got := r.rec.mentioning("set_config('app.current_tenant'"); len(got) != 1 {
		t.Errorf("control: a known tenant recorded %d tenant statement(s), want 1 (the recorder must see them)", len(got))
	}
}

func TestRLS_StaffBundleNonHouseActorIsNotStaff(t *testing.T) {
	r := newStaffRig(t)
	fx := plantBundleTenant(t, r.super, "non-house", "INV-STAFF-NH", 1)

	callers := map[string]context.Context{
		"a customer member as staff": staffCtx(t, fx.admin),
		"no staff identity":          context.Background(),
	}
	for name, ctx := range callers {
		t.Run(name, func(t *testing.T) {
			r.rec.reset()
			if _, err := r.store.StaffEntities(ctx, fx.tenant); !errors.Is(err, db.ErrNotStaff) {
				t.Errorf("StaffEntities error = %v, want db.ErrNotStaff", err)
			}
			if _, err := r.store.StaffPreview(ctx, fx.tenant, fx.req); !errors.Is(err, db.ErrNotStaff) {
				t.Errorf("StaffPreview error = %v, want db.ErrNotStaff", err)
			}
			var w countingWriter
			if err := r.store.StaffAssemble(ctx, fx.tenant, fx.req, &w, nil); !errors.Is(err, db.ErrNotStaff) {
				t.Errorf("StaffAssemble error = %v, want db.ErrNotStaff", err)
			}
			if w.n != 0 {
				t.Errorf("StaffAssemble wrote %d byte(s), want 0", w.n)
			}
			if got := r.rec.mentioning("set_config('app.current_tenant'"); len(got) != 0 {
				t.Errorf("a non-staff caller opened a tenant transaction: %q", got)
			}
		})
	}
}

func TestRLS_StaffBundleOverTheCapRefusesBeforeTheFirstByte(t *testing.T) {
	r := newStaffRig(t)
	fx := plantBundleTenant(t, r.super, "cap", "INV-STAFF-CAP", 3)
	r.store.maxInvoices = 2

	var w countingWriter
	err := r.store.StaffAssemble(r.ctx, fx.tenant, fx.req, &w, nil)
	var tooMany *TooManyInvoicesError
	if !errors.As(err, &tooMany) || tooMany.Count != 3 || tooMany.Limit != 2 {
		t.Errorf("StaffAssemble error = %v, want *TooManyInvoicesError{3, 2}", err)
	}
	if w.n != 0 {
		t.Errorf("StaffAssemble wrote %d byte(s), want 0", w.n)
	}
	p, err := r.store.StaffPreview(r.ctx, fx.tenant, fx.req)
	if err != nil {
		t.Fatalf("StaffPreview: %v", err)
	}
	if !p.OverLimit || p.Counts.Invoices != 3 {
		t.Errorf("StaffPreview = over_limit %v, invoices %d, want true and 3", p.OverLimit, p.Counts.Invoices)
	}
}

func TestRLS_StaffBundleRunsReadOnlyAndWritesNoAuditRow(t *testing.T) {
	r := newStaffRig(t)
	fx := plantBundleTenant(t, r.super, "readonly", "INV-STAFF-RO", 2)
	var start time.Time
	if err := r.super.QueryRow(context.Background(), `SELECT clock_timestamp()`).Scan(&start); err != nil {
		t.Fatalf("read the clock: %v", err)
	}

	calls := map[string]func() error{
		"StaffEntities": func() error { _, err := r.store.StaffEntities(r.ctx, fx.tenant); return err },
		"StaffPreview":  func() error { _, err := r.store.StaffPreview(r.ctx, fx.tenant, fx.req); return err },
		"StaffAssemble": func() error { return r.store.StaffAssemble(r.ctx, fx.tenant, fx.req, io.Discard, nil) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			r.rec.reset()
			if err := call(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			readOnly := 0
			for _, s := range r.rec.statements() {
				l := strings.ToLower(s)
				switch {
				case l == "begin isolation level repeatable read read only":
					readOnly++
				case strings.HasPrefix(l, "insert into"), strings.HasPrefix(l, "update "), strings.HasPrefix(l, "delete from"):
					t.Errorf("%s ran a write: %q", name, s)
				}
			}
			if readOnly != 1 {
				t.Errorf("%s began %d REPEATABLE READ READ ONLY transaction(s), want 1; statements: %q", name, readOnly, r.rec.statements())
			}
			if !r.rec.setConfigAfterReadOnlyBegin() {
				t.Errorf("%s set the tenant before beginning the READ ONLY transaction (or never): %q", name, r.rec.statements())
			}
		})
	}

	ctx := context.Background()
	var staffRows, tenantRows int
	if err := r.super.QueryRow(ctx, `SELECT count(*) FROM staff_audit_log WHERE actor::text = $1`, r.staff).Scan(&staffRows); err != nil {
		t.Fatalf("count staff_audit_log: %v", err)
	}
	if err := r.super.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND created_at >= $2`, fx.tenant, start).Scan(&tenantRows); err != nil {
		t.Fatalf("count audit_log: %v", err)
	}
	if staffRows != 0 || tenantRows != 0 {
		t.Errorf("reads wrote %d staff_audit_log row(s) and %d audit_log row(s), want none", staffRows, tenantRows)
	}
}

// statements is a copy of the recorded SQL in order.
func (r *sqlRecorder) statements() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sql)
}

// setConfigAfterReadOnlyBegin reports whether the tenant GUC is set after the READ ONLY begin, not before.
func (r *sqlRecorder) setConfigAfterReadOnlyBegin() bool {
	begin, set := -1, -1
	for i, s := range r.statements() {
		switch {
		case strings.EqualFold(s, "begin isolation level repeatable read read only"):
			begin = i
		case strings.Contains(s, "set_config('app.current_tenant'"):
			set = i
		}
	}
	return begin >= 0 && set > begin
}

// A path tenant id in a non-canonical spelling reaches the same tenant, and the manifest names it canonically.
func TestRLS_StaffAssembleSpellsTheTenantCanonically(t *testing.T) {
	r := newStaffRig(t)
	fx := plantBundleTenant(t, r.super, "spelling", "INV-STAFF-SP", 1)

	for name, id := range map[string]string{
		"upper case": strings.ToUpper(fx.tenant),
		"braced":     "{" + fx.tenant + "}",
		"no hyphens": strings.ReplaceAll(fx.tenant, "-", ""),
	} {
		t.Run(name, func(t *testing.T) {
			_, body := zipEntries(t, r.assemble(t, id, fx.req))
			var doc manifestDoc
			if err := json.Unmarshal(body["manifest.json"], &doc); err != nil {
				t.Fatalf("unmarshal manifest: %v", err)
			}
			if doc.TenantID != fx.tenant {
				t.Errorf("manifest tenant_id = %q for path %q, want the canonical %q", doc.TenantID, id, fx.tenant)
			}
		})
	}
}
