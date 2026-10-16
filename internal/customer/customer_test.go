package customer_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/customer"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	tin1 = "12345678-0001"
	tin2 = "12345678-0002"
	tin3 = "12345678-0003"
	tin4 = "12345678-0004"

	updatedEvent = "customers.customer.updated"
)

func ptr(s string) *string { return &s }

// dbTestPools returns the superuser (seed) and app-role (Store) pools, or skips
// when the DSNs are unset, like every other DB-backed package.
func dbTestPools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	appURL, superURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("customer db test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-customer`)")
	}
	ctx := context.Background()
	s, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping superuser: %v", err)
	}
	a, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	t.Cleanup(a.Close)
	return s, a
}

func seedTenant(t *testing.T, super *pgxpool.Pool, label string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm')`, id, label); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id) })
	return id
}

// seedMember seeds a membership row for a fresh subject and returns that
// subject with a context carrying its identity.
func seedMember(t *testing.T, super *pgxpool.Pool, tenantID, role, status string) (string, context.Context) {
	t.Helper()
	subject := uuid.NewString()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, $3, $4)`,
		tenantID, subject, role, status); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	return subject, auth.WithIdentity(context.Background(),
		auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})
}

// seedTenantWithAdmin seeds a tenant with one active admin caller.
func seedTenantWithAdmin(t *testing.T, super *pgxpool.Pool, label string) (string, string, context.Context) {
	t.Helper()
	tenantID := seedTenant(t, super, label)
	subject, ctx := seedMember(t, super, tenantID, "admin", "active")
	return tenantID, subject, ctx
}

// seed is one customers row; nil leaves the column NULL.
type seed struct {
	TIN                                     string
	Name, BuyerType, Email, Telephone, City *string
	State                                   *string
	Edited                                  []string
	Status                                  string // "" keeps the column default
}

func seedCustomer(t *testing.T, super *pgxpool.Pool, tenantID string, s seed) string {
	t.Helper()
	edited := s.Edited
	if edited == nil {
		edited = []string{}
	}
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO customers (tenant_id, tin, name, buyer_type, email, telephone, city, state, edited_fields, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, COALESCE(NULLIF($10, ''), 'active')) RETURNING id`,
		tenantID, s.TIN, s.Name, s.BuyerType, s.Email, s.Telephone, s.City, s.State, edited, s.Status).Scan(&id); err != nil {
		t.Fatalf("seed customers row %q: %v", s.TIN, err)
	}
	return id
}

type row struct {
	TIN                                                                                             string
	Name, ContactPerson, BuyerType, Email, Telephone, Street, City, PostalZone, Country, State, LGA *string
	Edited                                                                                          []string
}

func readRow(t *testing.T, super *pgxpool.Pool, id string) row {
	t.Helper()
	var r row
	if err := super.QueryRow(context.Background(),
		`SELECT tin, name, contact_person, buyer_type, email, telephone, street, city, postal_zone, country, state, lga, edited_fields
		 FROM customers WHERE id = $1`, id,
	).Scan(&r.TIN, &r.Name, &r.ContactPerson, &r.BuyerType, &r.Email, &r.Telephone, &r.Street, &r.City,
		&r.PostalZone, &r.Country, &r.State, &r.LGA, &r.Edited); err != nil {
		t.Fatalf("read customers row %s: %v", id, err)
	}
	return r
}

func rowStatus(t *testing.T, super *pgxpool.Pool, id string) string {
	t.Helper()
	var st string
	if err := super.QueryRow(context.Background(), `SELECT status FROM customers WHERE id = $1`, id).Scan(&st); err != nil {
		t.Fatalf("read customers status %s: %v", id, err)
	}
	return st
}

func strVal(p *string) string {
	if p == nil {
		return "<NULL>"
	}
	return *p
}

type auditRow struct {
	Actor   string
	Payload map[string]any
	Raw     string
}

func auditRows(t *testing.T, super *pgxpool.Pool, tenantID, event string) []auditRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT actor, payload FROM audit_log WHERE tenant_id = $1 AND event = $2 ORDER BY id`, tenantID, event)
	if err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var a auditRow
		var raw []byte
		if err := rows.Scan(&a.Actor, &raw); err != nil {
			t.Fatalf("scan audit_log: %v", err)
		}
		a.Raw = string(raw)
		if err := json.Unmarshal(raw, &a.Payload); err != nil {
			t.Fatalf("decode audit payload %s: %v", raw, err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate audit_log: %v", err)
	}
	return out
}

func newMux(store *customer.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/customers", customer.ListHandler(store.List, nil))
	mux.HandleFunc("PATCH /v1/customers/{id}", customer.UpdateHandler(store.Update, nil))
	return mux
}

func do(t *testing.T, h http.Handler, ctx context.Context, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func errorBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var b map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("response is not the {\"error\"} envelope: %q (%v)", rec.Body.String(), err)
	}
	return b["error"]
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, status, rec.Body.String())
	}
}

func ids(cs []customer.Customer) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func tins(cs []customer.Customer) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.TIN
	}
	return out
}

// --- list -----------------------------------------------------------------

func TestStoreList_ReturnsOnlyTheCallersTenant(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer list A")
	tenantB, _, ctxB := seedTenantWithAdmin(t, super, "customer list B")
	a1 := seedCustomer(t, super, tenantA, seed{TIN: tin1})
	a2 := seedCustomer(t, super, tenantA, seed{TIN: tin2})
	b1 := seedCustomer(t, super, tenantB, seed{TIN: tin1})

	got, total, err := store.List(ctxA, customer.ListFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List as A: %v", err)
	}
	gotIDs := ids(got)
	sort.Strings(gotIDs)
	wantIDs := []string{a1, a2}
	sort.Strings(wantIDs)
	if total != 2 || !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("List as A = ids %v total %d, want ids %v total 2", gotIDs, total, wantIDs)
	}

	gotB, totalB, err := store.List(ctxB, customer.ListFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List as B: %v", err)
	}
	if totalB != 1 || !reflect.DeepEqual(ids(gotB), []string{b1}) {
		t.Errorf("List as B = ids %v total %d, want [%s] total 1", ids(gotB), totalB, b1)
	}
}

func TestStoreList_OrdersByNameNullsLastThenTIN(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer order")
	seedCustomer(t, super, tenantA, seed{TIN: tin3, Name: ptr("Zed")})
	seedCustomer(t, super, tenantA, seed{TIN: tin2})
	seedCustomer(t, super, tenantA, seed{TIN: tin4, Name: ptr("Abe")})
	seedCustomer(t, super, tenantA, seed{TIN: tin1})

	got, total, err := store.List(ctxA, customer.ListFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{tin4, tin3, tin1, tin2} // Abe, Zed, then the unnamed by TIN
	if total != 4 || !reflect.DeepEqual(tins(got), want) {
		t.Errorf("List order = %v total %d, want %v total 4", tins(got), total, want)
	}
}

func TestStoreList_TINFilterIsExact(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer tin filter")
	want := seedCustomer(t, super, tenantA, seed{TIN: tin1})
	seedCustomer(t, super, tenantA, seed{TIN: "RN-12345678"})

	all, totalAll, err := store.List(ctxA, customer.ListFilter{Limit: 50})
	if err != nil || totalAll != 2 || len(all) != 2 {
		t.Fatalf("control: unfiltered List = %d items total %d err %v, want 2 and 2", len(all), totalAll, err)
	}

	for _, partial := range []string{"RN-1234", "12345678", "12345678-000"} {
		got, total, err := store.List(ctxA, customer.ListFilter{TIN: partial, Limit: 50})
		if err != nil {
			t.Fatalf("List tin=%q: %v", partial, err)
		}
		if len(got) != 0 || total != 0 {
			t.Errorf("List tin=%q = %v total %d, want none (exact match, no prefix or substring)", partial, tins(got), total)
		}
	}

	got, total, err := store.List(ctxA, customer.ListFilter{TIN: " " + tin1 + " ", Limit: 50})
	if err != nil {
		t.Fatalf("List padded tin: %v", err)
	}
	if total != 1 || !reflect.DeepEqual(ids(got), []string{want}) {
		t.Errorf("List tin=%q = ids %v total %d, want [%s] total 1", " "+tin1+" ", ids(got), total, want)
	}
}

type listBody struct {
	Customers  []map[string]any `json:"customers"`
	Pagination struct {
		Limit, Offset, Total int
	} `json:"pagination"`
}

func TestListHandler_PaginationEnvelope(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer page")
	_, _, ctxEmpty := seedTenantWithAdmin(t, super, "customer page empty")
	seedCustomer(t, super, tenantA, seed{TIN: tin1, Name: ptr("Abe")})
	seedCustomer(t, super, tenantA, seed{TIN: tin2, Name: ptr("Bea")})
	seedCustomer(t, super, tenantA, seed{TIN: tin3, Name: ptr("Cy")})

	rec := do(t, h, ctxA, http.MethodGet, "/v1/customers?limit=1&offset=1", "")
	wantStatus(t, rec, http.StatusOK)
	var body listBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode list: %v (%s)", err, rec.Body.String())
	}
	if len(body.Customers) != 1 || body.Customers[0]["tin"] != tin2 {
		t.Fatalf("customers = %v, want only the second record (tin %s)", body.Customers, tin2)
	}
	if body.Pagination.Limit != 1 || body.Pagination.Offset != 1 || body.Pagination.Total != 3 {
		t.Errorf("pagination = %+v, want {limit 1 offset 1 total 3}", body.Pagination)
	}
	wantKeys := []string{"city", "contact_person", "country", "created_at", "email", "id", "lga", "name",
		"buyer_type", "postal_zone", "state", "status", "street", "telephone", "tin"}
	sort.Strings(wantKeys)
	var gotKeys []string
	for k := range body.Customers[0] {
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Errorf("customer keys = %v, want exactly %v (edited_fields stays off the wire)", gotKeys, wantKeys)
	}
	if v, present := body.Customers[0]["email"]; !present || v != nil {
		t.Errorf("unset email = %v (present %v), want JSON null", v, present)
	}

	rec = do(t, h, ctxEmpty, http.MethodGet, "/v1/customers", "")
	wantStatus(t, rec, http.StatusOK)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode empty list: %v", err)
	}
	if string(raw["customers"]) != "[]" {
		t.Errorf("empty tenant customers = %s, want []", raw["customers"])
	}
	var empty listBody
	_ = json.Unmarshal(rec.Body.Bytes(), &empty)
	if empty.Pagination.Limit != 50 || empty.Pagination.Offset != 0 || empty.Pagination.Total != 0 {
		t.Errorf("empty pagination = %+v, want {limit 50 offset 0 total 0}", empty.Pagination)
	}
}

func TestListHandler_RejectsBadLimitAndOffset(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer bad paging")
	seedCustomer(t, super, tenantA, seed{TIN: tin1})

	for _, tc := range []struct{ query, msg string }{
		{"limit=0", "limit must be >= 1"},
		{"limit=x", "limit must be an integer"},
		{"offset=-1", "offset must be >= 0"},
		{"offset=x", "offset must be an integer"},
	} {
		rec := do(t, h, ctxA, http.MethodGet, "/v1/customers?"+tc.query, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET ?%s = %d, want 400 (body %q)", tc.query, rec.Code, rec.Body.String())
			continue
		}
		if got := errorBody(t, rec); got != tc.msg {
			t.Errorf("GET ?%s error = %q, want %q", tc.query, got, tc.msg)
		}
	}

	rec := do(t, h, ctxA, http.MethodGet, "/v1/customers?limit=500", "")
	wantStatus(t, rec, http.StatusOK)
	var body listBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if body.Pagination.Limit != 200 || len(body.Customers) != 1 {
		t.Errorf("limit=500 → pagination.limit %d with %d item(s), want 200 with 1", body.Pagination.Limit, len(body.Customers))
	}
}

// --- update ---------------------------------------------------------------

func TestStoreUpdate_TrimsAndStores(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer trim")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1, Name: ptr("Keep Ltd"), Email: ptr("old@x.ng")})

	got, err := store.Update(ctxA, id, customer.UpdateInput{Email: ptr("  b@x.ng ")})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if strVal(got.Email) != "b@x.ng" || got.ID != id || got.TIN != tin1 {
		t.Errorf("Update returned id %q tin %q email %q, want %q %q %q", got.ID, got.TIN, strVal(got.Email), id, tin1, "b@x.ng")
	}
	r := readRow(t, super, id)
	if strVal(r.Email) != "b@x.ng" {
		t.Errorf("stored email = %q, want trimmed %q", strVal(r.Email), "b@x.ng")
	}
	if strVal(r.Name) != "Keep Ltd" {
		t.Errorf("unsent name = %q, want unchanged %q", strVal(r.Name), "Keep Ltd")
	}
}

func TestStoreUpdate_EmptyStringClearsToNull(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer clear")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1, City: ptr("Lagos"), State: ptr("LA")})

	got, err := store.Update(ctxA, id, customer.UpdateInput{City: ptr("   ")})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.City != nil {
		t.Errorf("Update returned city %q, want nil (blank after trim stores NULL)", *got.City)
	}
	r := readRow(t, super, id)
	if r.City != nil {
		t.Errorf("stored city = %q, want NULL", *r.City)
	}
	if strVal(r.State) != "LA" {
		t.Errorf("unsent state = %q, want unchanged LA", strVal(r.State))
	}
	if !reflect.DeepEqual(r.Edited, []string{"city"}) {
		t.Errorf("edited_fields = %v, want [city] (a cleared field is still a correction)", r.Edited)
	}
}

func TestStoreUpdate_AddsSentFieldsToEditedFields(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer edited")

	for _, tc := range []struct {
		name   string
		before []string
		in     customer.UpdateInput
		want   []string
	}{
		{"adds new, keeps old", []string{"email"}, customer.UpdateInput{Email: ptr("a@x.ng"), State: ptr("LA")}, []string{"email", "state"}},
		{"no duplicates, sorted", []string{"state"}, customer.UpdateInput{State: ptr("OG"), City: ptr("Abeokuta")}, []string{"city", "state"}},
		{"keeps an old key that was not sent", []string{"city"}, customer.UpdateInput{State: ptr("OG")}, []string{"city", "state"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tin := map[string]string{"adds new, keeps old": tin1, "no duplicates, sorted": tin2, "keeps an old key that was not sent": tin3}[tc.name]
			id := seedCustomer(t, super, tenantA, seed{TIN: tin, Edited: tc.before})
			if _, err := store.Update(ctxA, id, tc.in); err != nil {
				t.Fatalf("Update: %v", err)
			}
			if got := readRow(t, super, id).Edited; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("edited_fields = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUpdateHandler_BuyerTypeOutsideTheSetIs400(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer buyer type")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1, BuyerType: ptr("B2B"), Email: ptr("old@x.ng")})

	for _, body := range []string{`{"buyer_type":"b2b"}`, `{"buyer_type":"B2X"}`, `{"email":"new@x.ng","buyer_type":"b2b"}`} {
		rec := do(t, h, ctxA, http.MethodPatch, "/v1/customers/"+id, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("PATCH %s = %d, want 400 (body %q)", body, rec.Code, rec.Body.String())
			continue
		}
		if got := errorBody(t, rec); got != "buyer_type must be B2B, B2G or B2C" {
			t.Errorf("PATCH %s error = %q, want %q", body, got, "buyer_type must be B2B, B2G or B2C")
		}
	}
	r := readRow(t, super, id)
	if strVal(r.BuyerType) != "B2B" || strVal(r.Email) != "old@x.ng" || len(r.Edited) != 0 {
		t.Errorf("row after rejected PATCHes = buyer_type %q email %q edited %v, want B2B old@x.ng []",
			strVal(r.BuyerType), strVal(r.Email), r.Edited)
	}
	if n := len(auditRows(t, super, tenantA, updatedEvent)); n != 0 {
		t.Errorf("audit rows after rejected PATCHes = %d, want 0", n)
	}

	rec := do(t, h, ctxA, http.MethodPatch, "/v1/customers/"+id, `{"buyer_type":"B2G"}`)
	wantStatus(t, rec, http.StatusOK)
	r = readRow(t, super, id)
	if strVal(r.BuyerType) != "B2G" || !reflect.DeepEqual(r.Edited, []string{"buyer_type"}) {
		t.Errorf("control: row after B2G = buyer_type %q edited %v, want B2G [buyer_type]", strVal(r.BuyerType), r.Edited)
	}
}

func TestUpdateHandler_BuyerTypeEmptyClears(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer buyer type clear")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1, BuyerType: ptr("B2B")})

	rec := do(t, h, ctxA, http.MethodPatch, "/v1/customers/"+id, `{"buyer_type":""}`)
	wantStatus(t, rec, http.StatusOK)
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v, present := got["buyer_type"]; !present || v != nil {
		t.Errorf("response buyer_type = %v (present %v), want JSON null", v, present)
	}
	if r := readRow(t, super, id); r.BuyerType != nil {
		t.Errorf("stored buyer_type = %q, want NULL", *r.BuyerType)
	}
}

func TestStoreUpdate_WritesOneAuditRowWithFieldNames(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, subject, ctxA := seedTenantWithAdmin(t, super, "customer audit")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1})

	const email, phone = "audit-secret@x.ng", "08031234567"
	if _, err := store.Update(ctxA, id, customer.UpdateInput{Email: ptr(email), Telephone: ptr(phone)}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	rows := auditRows(t, super, tenantA, updatedEvent)
	if len(rows) != 1 {
		t.Fatalf("audit rows for %s = %d, want 1", updatedEvent, len(rows))
	}
	a := rows[0]
	if a.Actor != subject {
		t.Errorf("audit actor = %q, want the caller %q", a.Actor, subject)
	}
	if a.Payload["id"] != id || a.Payload["tin"] != tin1 {
		t.Errorf("audit payload id/tin = %v/%v, want %s/%s", a.Payload["id"], a.Payload["tin"], id, tin1)
	}
	var fields []string
	if raw, ok := a.Payload["fields"].([]any); ok {
		for _, f := range raw {
			fields = append(fields, f.(string))
		}
	}
	sort.Strings(fields)
	if !reflect.DeepEqual(fields, []string{"email", "telephone"}) {
		t.Errorf("audit payload fields = %v, want [email telephone]", a.Payload["fields"])
	}
	if len(a.Payload) != 3 {
		t.Errorf("audit payload keys = %v, want exactly id, tin, fields", a.Payload)
	}
	if strings.Contains(a.Raw, email) || strings.Contains(a.Raw, phone) {
		t.Errorf("audit payload %s carries a personal value; field names only", a.Raw)
	}
}

func TestStoreUpdate_AuditFailureRollsBackTheCorrection(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer atomic")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1, Email: ptr("old@x.ng"), Edited: []string{"state"}})

	// A non-UUID subject skips the membership lookup, and 256 characters fail
	// audit_log's actor length CHECK after the UPDATE has run.
	badCtx := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: strings.Repeat("a", 256), Role: "authenticated", TenantID: tenantA})
	_, err := store.Update(badCtx, id, customer.UpdateInput{Email: ptr("new@x.ng")})
	if err == nil {
		t.Fatal("Update with an audit-failing actor succeeded, want an error")
	}
	// audit_log's actor CHECK is also SQLSTATE 23514; it is an operational 500, not a 400.
	if errors.Is(err, customer.ErrValidation) {
		t.Errorf("audit-write failure = %v, want an error that is not ErrValidation", err)
	}
	r := readRow(t, super, id)
	if strVal(r.Email) != "old@x.ng" || !reflect.DeepEqual(r.Edited, []string{"state"}) {
		t.Errorf("row after failed audit = email %q edited %v, want old@x.ng [state] (rolled back)", strVal(r.Email), r.Edited)
	}
	if n := len(auditRows(t, super, tenantA, updatedEvent)); n != 0 {
		t.Errorf("audit rows after failed audit = %d, want 0", n)
	}

	if _, err := store.Update(ctxA, id, customer.UpdateInput{Email: ptr("new@x.ng")}); err != nil {
		t.Fatalf("control: Update with a normal actor: %v", err)
	}
	if got := strVal(readRow(t, super, id).Email); got != "new@x.ng" {
		t.Errorf("control: stored email = %q, want new@x.ng", got)
	}
}

// --- access and addressing ------------------------------------------------

func TestHandlers_NoIdentityIs401(t *testing.T) {
	called := false
	list := func(context.Context, customer.ListFilter) ([]customer.Customer, int, error) {
		called = true
		return nil, 0, nil
	}
	update := func(context.Context, string, customer.UpdateInput) (customer.Customer, error) {
		called = true
		return customer.Customer{}, nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/customers", customer.ListHandler(list, nil))
	mux.HandleFunc("PATCH /v1/customers/{id}", customer.UpdateHandler(update, nil))

	for _, tc := range []struct{ method, target, body string }{
		{http.MethodGet, "/v1/customers", ""},
		{http.MethodPatch, "/v1/customers/" + uuid.NewString(), `{"email":"a@x.ng"}`},
	} {
		rec := do(t, mux, context.Background(), tc.method, tc.target, tc.body)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without identity = %d, want 401 (body %q)", tc.method, rec.Code, rec.Body.String())
			continue
		}
		if got := errorBody(t, rec); got != "unauthorized" {
			t.Errorf("%s without identity error = %q, want unauthorized", tc.method, got)
		}
	}
	if called {
		t.Error("the store function ran for a request with no identity")
	}
}

func TestUpdateHandler_UnknownAndMalformedIDAre404(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer unknown id")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1, Email: ptr("old@x.ng")})

	for _, bad := range []string{uuid.NewString(), "not-a-uuid"} {
		rec := do(t, h, ctxA, http.MethodPatch, "/v1/customers/"+bad, `{"email":"new@x.ng"}`)
		if rec.Code != http.StatusNotFound {
			t.Errorf("PATCH id %q = %d, want 404 (body %q)", bad, rec.Code, rec.Body.String())
			continue
		}
		if got := errorBody(t, rec); got != "not found" {
			t.Errorf("PATCH id %q error = %q, want not found", bad, got)
		}
	}
	if got := strVal(readRow(t, super, id).Email); got != "old@x.ng" {
		t.Errorf("email after 404s = %q, want unchanged old@x.ng", got)
	}
	if n := len(auditRows(t, super, tenantA, updatedEvent)); n != 0 {
		t.Errorf("audit rows after 404s = %d, want 0", n)
	}

	rec := do(t, h, ctxA, http.MethodPatch, "/v1/customers/"+id, `{"email":"new@x.ng"}`)
	wantStatus(t, rec, http.StatusOK)
}

func TestStoreUpdate_PreparerCanCorrect(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	h := newMux(store)
	tenantA := seedTenant(t, super, "customer preparer")
	_, ctxPreparer := seedMember(t, super, tenantA, "preparer", "active")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1, Name: ptr("Old Name")})

	rec := do(t, h, ctxPreparer, http.MethodPatch, "/v1/customers/"+id, `{"name":"New Name"}`)
	wantStatus(t, rec, http.StatusOK)
	var got customer.Customer
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != id || strVal(got.Name) != "New Name" {
		t.Errorf("response id %q name %q, want %q New Name", got.ID, strVal(got.Name), id)
	}
	if name := strVal(readRow(t, super, id).Name); name != "New Name" {
		t.Errorf("stored name = %q, want New Name", name)
	}
}

func TestUpdateHandler_NoCorrectableFieldIs400(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer no fields")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1, Name: ptr("Keep")})

	for _, body := range []string{`{}`, `{"tin":"` + tin2 + `"}`, `{"nonsense":"x"}`} {
		rec := do(t, h, ctxA, http.MethodPatch, "/v1/customers/"+id, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("PATCH %s = %d, want 400 (body %q)", body, rec.Code, rec.Body.String())
			continue
		}
		if got := errorBody(t, rec); got != "no fields to update" {
			t.Errorf("PATCH %s error = %q, want %q", body, got, "no fields to update")
		}
	}
	r := readRow(t, super, id)
	if r.TIN != tin1 || len(r.Edited) != 0 {
		t.Errorf("row after rejected PATCHes = tin %q edited %v, want %s []", r.TIN, r.Edited, tin1)
	}
	if n := len(auditRows(t, super, tenantA, updatedEvent)); n != 0 {
		t.Errorf("audit rows after rejected PATCHes = %d, want 0", n)
	}

	// tin beside a correctable key is ignored, not applied.
	rec := do(t, h, ctxA, http.MethodPatch, "/v1/customers/"+id, `{"tin":"`+tin2+`","name":"Changed"}`)
	wantStatus(t, rec, http.StatusOK)
	r = readRow(t, super, id)
	if r.TIN != tin1 || strVal(r.Name) != "Changed" || !reflect.DeepEqual(r.Edited, []string{"name"}) {
		t.Errorf("control: row = tin %q name %q edited %v, want %s Changed [name]", r.TIN, strVal(r.Name), r.Edited, tin1)
	}
}

func TestListHandler_EveryRowCarriesItsStatus(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer wire status")
	seedCustomer(t, super, tenantA, seed{TIN: tin1})
	seedCustomer(t, super, tenantA, seed{TIN: tin2, Status: "archived"})

	rec := do(t, h, ctxA, http.MethodGet, "/v1/customers", "")
	wantStatus(t, rec, http.StatusOK)
	var resp struct {
		Customers []customer.Customer `json:"customers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]string{}
	for _, c := range resp.Customers {
		got[c.TIN] = c.Status
	}
	if want := map[string]string{tin1: "active", tin2: "archived"}; !reflect.DeepEqual(got, want) {
		t.Errorf("status by tin = %v, want %v", got, want)
	}
}

func TestUpdateHandler_StatusIsNotPatchable(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer status patch")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1, Status: "archived"})

	rec := do(t, h, ctxA, http.MethodPatch, "/v1/customers/"+id, `{"status":"active"}`)
	wantStatus(t, rec, http.StatusBadRequest)
	if got := errorBody(t, rec); got != "no fields to update" {
		t.Errorf("PATCH status alone error = %q, want %q", got, "no fields to update")
	}
	if st := rowStatus(t, super, id); st != "archived" {
		t.Errorf("status after rejected PATCH = %q, want archived", st)
	}
	if n := len(auditRows(t, super, tenantA, updatedEvent)); n != 0 {
		t.Errorf("audit rows after rejected PATCH = %d, want 0", n)
	}

	rec = do(t, h, ctxA, http.MethodPatch, "/v1/customers/"+id, `{"status":"active","name":"X"}`)
	wantStatus(t, rec, http.StatusOK)
	r := readRow(t, super, id)
	if strVal(r.Name) != "X" || !reflect.DeepEqual(r.Edited, []string{"name"}) {
		t.Errorf("row = name %q edited %v, want X [name]", strVal(r.Name), r.Edited)
	}
	if st := rowStatus(t, super, id); st != "archived" {
		t.Errorf("status after PATCH with a name = %q, want archived", st)
	}
}

func TestUpdateHandler_ArchivedRecordIsCorrectable(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer archived correct")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1, Status: "archived"})

	rec := do(t, h, ctxA, http.MethodPatch, "/v1/customers/"+id, `{"email":"new@x.ng"}`)
	wantStatus(t, rec, http.StatusOK)
	var got customer.Customer
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strVal(got.Email) != "new@x.ng" || got.Status != "archived" {
		t.Errorf("response email %q status %q, want new@x.ng archived", strVal(got.Email), got.Status)
	}
	r := readRow(t, super, id)
	if strVal(r.Email) != "new@x.ng" || !reflect.DeepEqual(r.Edited, []string{"email"}) || rowStatus(t, super, id) != "archived" {
		t.Errorf("row = email %q edited %v status %q, want new@x.ng [email] archived", strVal(r.Email), r.Edited, rowStatus(t, super, id))
	}
	if n := len(auditRows(t, super, tenantA, updatedEvent)); n != 1 {
		t.Errorf("customers.customer.updated rows = %d, want 1", n)
	}
}

func TestStoreList_StatusFilter(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer status filter")
	a := seedCustomer(t, super, tenantA, seed{TIN: tin1, Name: ptr("A")})
	b := seedCustomer(t, super, tenantA, seed{TIN: tin2, Name: ptr("B"), Status: "archived"})
	c := seedCustomer(t, super, tenantA, seed{TIN: tin3, Name: ptr("C")})

	for _, tc := range []struct {
		status string
		want   []string
	}{{"active", []string{a, c}}, {"archived", []string{b}}, {"", []string{a, b, c}}} {
		got, total, err := store.List(ctxA, customer.ListFilter{Status: tc.status, Limit: 50})
		if err != nil {
			t.Fatalf("List status %q: %v", tc.status, err)
		}
		if total != len(tc.want) || !reflect.DeepEqual(ids(got), tc.want) {
			t.Errorf("List status %q = ids %v total %d, want %v total %d", tc.status, ids(got), total, tc.want, len(tc.want))
		}
	}
}

func TestStoreList_StatusFilterTotalIgnoresPaging(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer status paging")
	seedCustomer(t, super, tenantA, seed{TIN: tin1, Name: ptr("A")})
	b := seedCustomer(t, super, tenantA, seed{TIN: tin2, Name: ptr("B"), Status: "archived"})
	seedCustomer(t, super, tenantA, seed{TIN: tin3, Name: ptr("C")})
	d := seedCustomer(t, super, tenantA, seed{TIN: "12345678-0004", Name: ptr("D"), Status: "archived"})

	got, total, err := store.List(ctxA, customer.ListFilter{Status: "archived", Limit: 1, Offset: 1})
	if err != nil || total != 2 || !reflect.DeepEqual(ids(got), []string{d}) {
		t.Errorf("archived page 2 = %v total %d err %v, want [%s] total 2", ids(got), total, err, d)
	}
	got, total, err = store.List(ctxA, customer.ListFilter{Status: "archived", Limit: 1})
	if err != nil || total != 2 || !reflect.DeepEqual(ids(got), []string{b}) {
		t.Errorf("archived page 1 = %v total %d err %v, want [%s] total 2", ids(got), total, err, b)
	}
}

func TestStoreList_StatusAndTINCombine(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer status tin")
	arch := seedCustomer(t, super, tenantA, seed{TIN: tin1, Status: "archived"})
	seedCustomer(t, super, tenantA, seed{TIN: tin2})

	got, total, err := store.List(ctxA, customer.ListFilter{TIN: tin1, Status: "active", Limit: 50})
	if err != nil || total != 0 || len(got) != 0 {
		t.Errorf("List active+tin1 = %v total %d err %v, want empty total 0", ids(got), total, err)
	}
	got, total, err = store.List(ctxA, customer.ListFilter{TIN: tin1, Status: "archived", Limit: 50})
	if err != nil || total != 1 || !reflect.DeepEqual(ids(got), []string{arch}) {
		t.Errorf("List archived+tin1 = %v total %d err %v, want [%s] total 1", ids(got), total, err, arch)
	}
}

func TestListHandler_UnknownStatusIs400(t *testing.T) {
	called := false
	list := func(context.Context, customer.ListFilter) ([]customer.Customer, int, error) {
		called = true
		return nil, 0, nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/customers", customer.ListHandler(list, nil))
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString()})

	for _, q := range []string{"deleted", "ACTIVE", "archived,active"} {
		rec := do(t, mux, ctx, http.MethodGet, "/v1/customers?status="+q, "")
		wantStatus(t, rec, http.StatusBadRequest)
		if got := errorBody(t, rec); got != `status must be "active" or "archived"` {
			t.Errorf("?status=%s error = %q", q, got)
		}
	}
	if called {
		t.Error("the list function ran for an unknown status")
	}
	wantStatus(t, do(t, mux, ctx, http.MethodGet, "/v1/customers?status=", ""), http.StatusOK)
	if !called {
		t.Error("control: empty status did not reach the list function")
	}
}

func TestListHandler_RepeatedStatusUsesTheFirst(t *testing.T) {
	var got customer.ListFilter
	list := func(_ context.Context, f customer.ListFilter) ([]customer.Customer, int, error) {
		got = f
		return nil, 0, nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/customers", customer.ListHandler(list, nil))
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString()})

	wantStatus(t, do(t, mux, ctx, http.MethodGet, "/v1/customers?status=archived&status=active", ""), http.StatusOK)
	if got.Status != "archived" {
		t.Errorf("filter status = %q, want archived (the first value)", got.Status)
	}
	wantStatus(t, do(t, mux, ctx, http.MethodGet, "/v1/customers?status=archived&status=deleted", ""), http.StatusOK)
}

func TestUpdateHandler_NullStatusIsNotPatchable(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer status null")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1, Status: "archived"})

	rec := do(t, h, ctxA, http.MethodPatch, "/v1/customers/"+id, `{"status":null}`)
	wantStatus(t, rec, http.StatusBadRequest)
	if st := rowStatus(t, super, id); st != "archived" {
		t.Errorf("status after PATCH null = %q, want archived", st)
	}
}

func TestLookupByTIN_ArchivedIsNil(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer lookup archived")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1})
	set := func(st string) {
		if _, err := super.Exec(context.Background(), `UPDATE customers SET status = $1 WHERE id = $2`, st, id); err != nil {
			t.Fatalf("set status %s: %v", st, err)
		}
	}

	set("archived")
	if got, err := lookup(t, app, ctxA, tin1); err != nil || got != nil {
		t.Errorf("LookupByTIN(archived) = %v, %v, want nil, nil", got, err)
	}
	set("active")
	if got, err := lookup(t, app, ctxA, tin1); err != nil || got == nil || got.ID != id {
		t.Errorf("LookupByTIN(active again) = %v, %v, want the record", got, err)
	}
}

// --- LookupByTIN (D26) ----------------------------------------------------

func lookup(t *testing.T, app *pgxpool.Pool, ctx context.Context, tin string) (*customer.Customer, error) {
	t.Helper()
	var got *customer.Customer
	err := db.WithinRequestTenantTx(ctx, app, func(tx pgx.Tx) error {
		c, err := customer.LookupByTIN(ctx, tx, tin)
		got = c
		return err
	})
	return got, err
}

func TestLookupByTIN_ReturnsTheTenantsRecord(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer lookup")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1, Name: ptr("Acme"), BuyerType: ptr("B2G")})
	seedCustomer(t, super, tenantA, seed{TIN: tin2})

	got, err := lookup(t, app, ctxA, " "+tin1+" ")
	if err != nil {
		t.Fatalf("LookupByTIN: %v", err)
	}
	if got == nil {
		t.Fatal("LookupByTIN(padded tin) = nil, want the record")
	}
	if got.ID != id || got.TIN != tin1 || strVal(got.BuyerType) != "B2G" || strVal(got.Name) != "Acme" {
		t.Errorf("LookupByTIN = id %q tin %q buyer_type %q name %q, want %q %q B2G Acme",
			got.ID, got.TIN, strVal(got.BuyerType), strVal(got.Name), id, tin1)
	}
}

func TestLookupByTIN_AbsentIsNilNil(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer lookup absent")
	seedCustomer(t, super, tenantA, seed{TIN: tin1})

	got, err := lookup(t, app, ctxA, "RN-9")
	if err != nil || got != nil {
		t.Errorf("LookupByTIN(absent) = %v, %v, want nil, nil", got, err)
	}
	if got, err := lookup(t, app, ctxA, tin1); err != nil || got == nil {
		t.Errorf("control: LookupByTIN(present) = %v, %v, want the record", got, err)
	}
}

// --- adversarial ----------------------------------------------------------

func TestListHandler_LimitAndOffsetBoundaries(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer bounds")
	seedCustomer(t, super, tenantA, seed{TIN: tin1, Name: ptr("Abe")})
	seedCustomer(t, super, tenantA, seed{TIN: tin2, Name: ptr("Bea")})

	for _, tc := range []struct {
		query                      string
		wantLimit, wantOffset, len int
	}{
		{"limit=1", 1, 0, 1},
		{"limit=200", 200, 0, 2},
		{"limit=201", 200, 0, 2},
		{"offset=0", 50, 0, 2},
		{"offset=2", 50, 2, 0},
		{"offset=99", 50, 99, 0},
	} {
		rec := do(t, h, ctxA, http.MethodGet, "/v1/customers?"+tc.query, "")
		wantStatus(t, rec, http.StatusOK)
		var body listBody
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s: %v", tc.query, err)
		}
		if body.Pagination.Limit != tc.wantLimit || body.Pagination.Offset != tc.wantOffset ||
			body.Pagination.Total != 2 || len(body.Customers) != tc.len {
			t.Errorf("?%s → %+v with %d item(s), want limit %d offset %d total 2 with %d",
				tc.query, body.Pagination, len(body.Customers), tc.wantLimit, tc.wantOffset, tc.len)
		}
	}

	rec := do(t, h, ctxA, http.MethodGet, "/v1/customers?limit=-1", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("?limit=-1 = %d, want 400", rec.Code)
	}
}

func TestUpdateHandler_MalformedBodyIs400(t *testing.T) {
	called := false
	update := func(context.Context, string, customer.UpdateInput) (customer.Customer, error) {
		called = true
		return customer.Customer{}, nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /v1/customers/{id}", customer.UpdateHandler(update, nil))
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated"})

	for _, tc := range []struct{ body, msg string }{
		{`{`, "invalid request body"},
		{``, "invalid request body"},
		{`[]`, "invalid request body"},
		{`"x"`, "invalid request body"},
		{`{"name":5}`, "invalid request body"},
		{`{"name":null}`, "no fields to update"},
	} {
		rec := do(t, mux, ctx, http.MethodPatch, "/v1/customers/"+uuid.NewString(), tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("PATCH %q = %d, want 400 (body %q)", tc.body, rec.Code, rec.Body.String())
			continue
		}
		if got := errorBody(t, rec); got != tc.msg {
			t.Errorf("PATCH %q error = %q, want %q", tc.body, got, tc.msg)
		}
	}
	if called {
		t.Error("the update function ran for a body with no correctable key")
	}

	rec := do(t, mux, ctx, http.MethodPatch, "/v1/customers/"+uuid.NewString(), `{"name":"x"}`)
	wantStatus(t, rec, http.StatusOK)
	if !called {
		t.Error("control: a valid body did not reach the update function")
	}
}

func TestUpdateHandler_AuditWriteFailureIs500NotBadRequest(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA := seedTenant(t, super, "customer audit 500")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1, Email: ptr("old@x.ng")})
	badCtx := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: strings.Repeat("a", 256), Role: "authenticated", TenantID: tenantA})

	rec := do(t, h, badCtx, http.MethodPatch, "/v1/customers/"+id, `{"email":"new@x.ng"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("PATCH with an audit-failing actor = %d, want 500 (body %q)", rec.Code, rec.Body.String())
	}
	if got := errorBody(t, rec); got != "internal server error" {
		t.Errorf("error = %q, want the generic message", got)
	}
	if got := strVal(readRow(t, super, id).Email); got != "old@x.ng" {
		t.Errorf("email after failed audit = %q, want unchanged old@x.ng", got)
	}
}

func TestUpdateHandler_EveryAllowedBuyerTypeIsAccepted(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer buyer types")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1})

	for _, tc := range []struct{ sent, want string }{{"B2B", "B2B"}, {"B2G", "B2G"}, {"B2C", "B2C"}, {" B2C ", "B2C"}} {
		rec := do(t, h, ctxA, http.MethodPatch, "/v1/customers/"+id, `{"buyer_type":"`+tc.sent+`"}`)
		wantStatus(t, rec, http.StatusOK)
		if got := strVal(readRow(t, super, id).BuyerType); got != tc.want {
			t.Errorf("buyer_type after sending %q = %q, want %q", tc.sent, got, tc.want)
		}
	}
}

func TestUpdateHandler_EachKeyLandsInItsOwnColumn(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer each key")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1})

	rec := do(t, h, ctxA, http.MethodPatch, "/v1/customers/"+id, `{"name":"v-name","contact_person":"v-contact",
		"buyer_type":"B2G","email":"v-email","telephone":"v-tel","street":"v-street","city":"v-city",
		"postal_zone":"v-zone","country":"v-country","state":"v-state","lga":"v-lga"}`)
	wantStatus(t, rec, http.StatusOK)

	r := readRow(t, super, id)
	for col, got := range map[string]*string{"name": r.Name, "contact_person": r.ContactPerson, "email": r.Email,
		"telephone": r.Telephone, "street": r.Street, "city": r.City, "postal_zone": r.PostalZone,
		"country": r.Country, "state": r.State, "lga": r.LGA} {
		if want := "v-" + strings.NewReplacer("_person", "", "telephone", "tel", "postal_zone", "zone").Replace(col); strVal(got) != want {
			t.Errorf("column %s = %q, want %q", col, strVal(got), want)
		}
	}
	if strVal(r.BuyerType) != "B2G" {
		t.Errorf("buyer_type = %q, want B2G", strVal(r.BuyerType))
	}
	wantFields := []string{"buyer_type", "city", "contact_person", "country", "email", "lga", "name", "postal_zone", "state", "street", "telephone"}
	if !reflect.DeepEqual(r.Edited, wantFields) {
		t.Errorf("edited_fields = %v, want %v", r.Edited, wantFields)
	}
	var resp customer.Customer
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strVal(resp.Street) != "v-street" || strVal(resp.LGA) != "v-lga" || strVal(resp.PostalZone) != "v-zone" || strVal(resp.ContactPerson) != "v-contact" {
		t.Errorf("response = street %q lga %q postal_zone %q contact_person %q, want the sent values",
			strVal(resp.Street), strVal(resp.LGA), strVal(resp.PostalZone), strVal(resp.ContactPerson))
	}
	rows := auditRows(t, super, tenantA, updatedEvent)
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	if f, _ := rows[0].Payload["fields"].([]any); len(f) != 11 {
		t.Errorf("audit fields = %v, want all 11 names", rows[0].Payload["fields"])
	}
}

func TestHandlers_IdentityWithoutTenantIs401(t *testing.T) {
	_, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated"})

	for _, tc := range []struct{ method, target, body string }{
		{http.MethodGet, "/v1/customers", ""},
		{http.MethodPatch, "/v1/customers/" + uuid.NewString(), `{"email":"a@x.ng"}`},
	} {
		rec := do(t, h, ctx, tc.method, tc.target, tc.body)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with no tenant on the identity = %d, want 401 (body %q)", tc.method, rec.Code, rec.Body.String())
		}
	}
}

func TestStoreUpdate_EmptyInputIsValidationAndWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer empty input")
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1, Name: ptr("Keep")})

	if _, err := store.Update(ctxA, id, customer.UpdateInput{}); !errors.Is(err, customer.ErrValidation) {
		t.Errorf("Update with no field = %v, want ErrValidation", err)
	}
	r := readRow(t, super, id)
	if len(r.Edited) != 0 || strVal(r.Name) != "Keep" {
		t.Errorf("row after empty Update = name %q edited %v, want Keep []", strVal(r.Name), r.Edited)
	}
	if n := len(auditRows(t, super, tenantA, updatedEvent)); n != 0 {
		t.Errorf("audit rows after empty Update = %d, want 0", n)
	}
}

func TestListHandler_TINQueryParamFilters(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer tin param")
	seedCustomer(t, super, tenantA, seed{TIN: tin1})
	seedCustomer(t, super, tenantA, seed{TIN: tin2})

	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"tin=" + tin2, []string{tin2}},
		{"tin=RN-9", nil},
		{"tin=", []string{tin1, tin2}},
	} {
		rec := do(t, h, ctxA, http.MethodGet, "/v1/customers?"+tc.query, "")
		wantStatus(t, rec, http.StatusOK)
		var body listBody
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s: %v", tc.query, err)
		}
		var got []string
		for _, c := range body.Customers {
			got = append(got, c["tin"].(string))
		}
		if !reflect.DeepEqual(got, tc.want) || body.Pagination.Total != len(tc.want) {
			t.Errorf("?%s → tins %v total %d, want %v total %d", tc.query, got, body.Pagination.Total, tc.want, len(tc.want))
		}
	}
}
