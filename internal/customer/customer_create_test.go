package customer_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/customer"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	createdEvent = "customers.customer.created"

	// The 400 texts of customer.go: errTINRequired, errTINFormat, errBuyerType.
	msgTINRequired = "tin is required"
	msgTINFormat   = "tin must be NNNNNNNN-NNNN or RN- followed by digits"
	msgBuyerType   = "buyer_type must be B2B, B2G or B2C"
	// The 409 text of statusForErr for ErrDuplicateTIN.
	msgDuplicateTIN = "duplicate tin"
)

func countTIN(t *testing.T, super *pgxpool.Pool, tenantID, tin string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM customers WHERE tenant_id = $1 AND tin = $2`, tenantID, tin).Scan(&n); err != nil {
		t.Fatalf("count customers: %v", err)
	}
	return n
}

func countAll(t *testing.T, super *pgxpool.Pool, tenantID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM customers WHERE tenant_id = $1`, tenantID).Scan(&n); err != nil {
		t.Fatalf("count customers: %v", err)
	}
	return n
}

// idFor reads the id of the tenant's record for tin.
func idFor(t *testing.T, super *pgxpool.Pool, tenantID, tin string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`SELECT id FROM customers WHERE tenant_id = $1 AND tin = $2`, tenantID, tin).Scan(&id); err != nil {
		t.Fatalf("no customers row for tenant %s tin %q: %v", tenantID, tin, err)
	}
	return id
}

func rowTenant(t *testing.T, super *pgxpool.Pool, id string) string {
	t.Helper()
	var tenant string
	if err := super.QueryRow(context.Background(), `SELECT tenant_id::text FROM customers WHERE id = $1`, id).Scan(&tenant); err != nil {
		t.Fatalf("read tenant_id %s: %v", id, err)
	}
	return tenant
}

// rowJSON is the whole row as text, for a byte-equal before/after comparison.
func rowJSON(t *testing.T, super *pgxpool.Pool, id string) string {
	t.Helper()
	var s string
	if err := super.QueryRow(context.Background(), `SELECT to_jsonb(c)::text FROM customers c WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("read row %s: %v", id, err)
	}
	return s
}

func decodeCustomer(t *testing.T, body []byte) customer.Customer {
	t.Helper()
	var c customer.Customer
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatalf("decode customer %q: %v", body, err)
	}
	return c
}

func createBody(tin string) string { return `{"tin":"` + tin + `"}` }

// --- 201: the record --------------------------------------------------------

func TestCreateHandler_CreatesTheRecord(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer create")

	rec := do(t, h, ctxA, http.MethodPost, "/v1/customers",
		`{"tin":" `+tin1+` ","name":"  Acme Ltd ","email":"a@x.ng","buyer_type":"B2B"}`)
	wantStatus(t, rec, http.StatusCreated)

	got := decodeCustomer(t, rec.Body.Bytes())
	if got.TIN != tin1 || strVal(got.Name) != "Acme Ltd" || strVal(got.Email) != "a@x.ng" ||
		strVal(got.BuyerType) != "B2B" || got.Status != "active" {
		t.Errorf("response = tin %q name %q email %q buyer_type %q status %q, want %s/Acme Ltd/a@x.ng/B2B/active",
			got.TIN, strVal(got.Name), strVal(got.Email), strVal(got.BuyerType), got.Status, tin1)
	}
	if got.ID == "" || got.CreatedAt.IsZero() {
		t.Errorf("response id %q created_at %v, want both set", got.ID, got.CreatedAt)
	}
	if n := countAll(t, super, tenantA); n != 1 {
		t.Fatalf("customers rows after POST = %d, want 1", n)
	}
	id := idFor(t, super, tenantA, tin1)
	if id != got.ID {
		t.Errorf("stored id = %q, want the response id %q", id, got.ID)
	}
	if tenant := rowTenant(t, super, id); tenant != tenantA {
		t.Errorf("stored tenant_id = %q, want the caller's %q", tenant, tenantA)
	}
	if st := rowStatus(t, super, id); st != "active" {
		t.Errorf("stored status = %q, want active", st)
	}
}

func TestCreateHandler_EachKeyLandsInItsOwnColumn(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer create each key")

	rec := do(t, h, ctxA, http.MethodPost, "/v1/customers", `{"tin":"`+tin1+`","name":"v-name","contact_person":"v-contact",
		"buyer_type":"B2G","email":"v-email","telephone":"v-tel","street":"v-street","city":"v-city",
		"postal_zone":"v-zone","country":"v-country","state":"v-state","lga":"v-lga"}`)
	wantStatus(t, rec, http.StatusCreated)

	r := readRow(t, super, idFor(t, super, tenantA, tin1))
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
	resp := decodeCustomer(t, rec.Body.Bytes())
	if strVal(resp.Street) != "v-street" || strVal(resp.LGA) != "v-lga" || strVal(resp.PostalZone) != "v-zone" || strVal(resp.ContactPerson) != "v-contact" {
		t.Errorf("response = street %q lga %q postal_zone %q contact_person %q, want the sent values",
			strVal(resp.Street), strVal(resp.LGA), strVal(resp.PostalZone), strVal(resp.ContactPerson))
	}
	rows := auditRows(t, super, tenantA, createdEvent)
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	if f, _ := rows[0].Payload["fields"].([]any); len(f) != 11 {
		t.Errorf("audit fields = %v, want all 11 names", rows[0].Payload["fields"])
	}
}

func TestCreateHandler_IgnoresStatusInTheBody(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer create status key")

	rec := do(t, h, ctxA, http.MethodPost, "/v1/customers", `{"tin":"`+tin1+`","status":"archived","nonsense":"x"}`)
	wantStatus(t, rec, http.StatusCreated)
	if st := rowStatus(t, super, idFor(t, super, tenantA, tin1)); st != "active" {
		t.Errorf("stored status = %q, want active (a create is always active)", st)
	}
}

func TestStoreCreate_BlankFieldStoresNull(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer create blank")

	c, err := store.Create(ctxA, customer.CreateInput{TIN: ptr(tin1),
		Fields: customer.UpdateInput{Name: ptr(" Acme "), City: ptr("   "), Email: ptr("")}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	r := readRow(t, super, c.ID)
	if strVal(r.Name) != "Acme" {
		t.Errorf("control: name = %q, want Acme (trimmed)", strVal(r.Name))
	}
	if r.City != nil || r.Email != nil {
		t.Errorf("city/email = %s/%s, want NULL/NULL", strVal(r.City), strVal(r.Email))
	}
	if c.City != nil || c.Email != nil {
		t.Errorf("returned city/email = %s/%s, want nil/nil", strVal(c.City), strVal(c.Email))
	}
	if n := countAll(t, super, tenantA); n != 1 {
		t.Errorf("customers rows = %d, want 1", n)
	}
}

func TestStoreCreate_RNTINIsAccepted(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	_, _, ctxA := seedTenantWithAdmin(t, super, "customer create rn")

	c, err := store.Create(ctxA, customer.CreateInput{TIN: ptr(" RN-12345 ")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.TIN != "RN-12345" || readRow(t, super, c.ID).TIN != "RN-12345" {
		t.Errorf("returned/stored tin = %q/%q, want RN-12345", c.TIN, readRow(t, super, c.ID).TIN)
	}
}

// --- 400 --------------------------------------------------------------------

func TestCreateHandler_TINRequired(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer create tin required")

	for _, body := range []string{`{}`, `{"tin":null}`, `{"tin":"   "}`, `{"name":"No Tin Ltd"}`} {
		rec := do(t, h, ctxA, http.MethodPost, "/v1/customers", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400 (body %q)", body, rec.Code, rec.Body.String())
			continue
		}
		if got := errorBody(t, rec); got != msgTINRequired {
			t.Errorf("POST %s error = %q, want %q", body, got, msgTINRequired)
		}
	}
	if n := countAll(t, super, tenantA); n != 0 {
		t.Errorf("customers rows after rejected POSTs = %d, want 0", n)
	}
	if n := len(auditRows(t, super, tenantA, createdEvent)); n != 0 {
		t.Errorf("audit rows after rejected POSTs = %d, want 0", n)
	}

	wantStatus(t, do(t, h, ctxA, http.MethodPost, "/v1/customers", createBody(tin1)), http.StatusCreated)
}

func TestCreateHandler_UnfileableTINIs400WithTheTINMessage(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer create tin format")

	bad := []string{"12345678", "123456780001", "rn-12", "RN-", "12345678-00012", "1234567-0001"}
	for _, tin := range bad {
		rec := do(t, h, ctxA, http.MethodPost, "/v1/customers", createBody(tin))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST tin %q = %d, want 400 (body %q)", tin, rec.Code, rec.Body.String())
			continue
		}
		got := errorBody(t, rec)
		if got == msgBuyerType {
			t.Errorf("POST tin %q error = the buyer-type message, want the TIN message", tin)
		}
		if got != msgTINFormat {
			t.Errorf("POST tin %q error = %q, want %q", tin, got, msgTINFormat)
		}
	}
	if n := countAll(t, super, tenantA); n != 0 {
		t.Errorf("customers rows after rejected POSTs = %d, want 0", n)
	}

	for _, tin := range []string{tin1, "RN-12"} {
		wantStatus(t, do(t, h, ctxA, http.MethodPost, "/v1/customers", createBody(tin)), http.StatusCreated)
	}
}

func TestCreateHandler_BadBuyerTypeIs400(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer create buyer type")

	rec := do(t, h, ctxA, http.MethodPost, "/v1/customers", `{"tin":"`+tin1+`","buyer_type":"b2b"}`)
	wantStatus(t, rec, http.StatusBadRequest)
	if got := errorBody(t, rec); got != msgBuyerType {
		t.Errorf("error = %q, want %q", got, msgBuyerType)
	}
	if n := countAll(t, super, tenantA); n != 0 {
		t.Errorf("customers rows after the rejected POST = %d, want 0", n)
	}
	if n := len(auditRows(t, super, tenantA, createdEvent)); n != 0 {
		t.Errorf("audit rows after the rejected POST = %d, want 0", n)
	}

	// The same TIN with a valid type creates: the rejected POST left nothing behind.
	wantStatus(t, do(t, h, ctxA, http.MethodPost, "/v1/customers", `{"tin":"`+tin1+`","buyer_type":"B2B"}`), http.StatusCreated)
}

func TestCreateHandler_MalformedBodyIs400(t *testing.T) {
	called := false
	create := func(context.Context, customer.CreateInput) (customer.Customer, error) {
		called = true
		return customer.Customer{}, nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/customers", customer.CreateHandler(create, nil))
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated"})

	for _, body := range []string{`{`, `[]`} {
		rec := do(t, mux, ctx, http.MethodPost, "/v1/customers", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400 (body %q)", body, rec.Code, rec.Body.String())
			continue
		}
		if got := errorBody(t, rec); got != "invalid request body" {
			t.Errorf("POST %s error = %q, want invalid request body", body, got)
		}
	}
	if called {
		t.Error("the store function ran for an undecodable body")
	}

	wantStatus(t, do(t, mux, ctx, http.MethodPost, "/v1/customers", createBody(tin1)), http.StatusCreated)
	if !called {
		t.Error("control: a valid body did not reach the store function")
	}
}

// --- 409 --------------------------------------------------------------------

func TestCreateHandler_HeldTINIs409(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer create held")
	activeID := seedCustomer(t, super, tenantA, seed{TIN: tin1, Name: ptr("Held Active")})
	archivedID := seedCustomer(t, super, tenantA, seed{TIN: tin2, Name: ptr("Held Archived"), Status: "archived"})
	before := map[string]string{activeID: rowJSON(t, super, activeID), archivedID: rowJSON(t, super, archivedID)}

	for _, tin := range []string{tin1, tin2} {
		rec := do(t, h, ctxA, http.MethodPost, "/v1/customers", `{"tin":"`+tin+`","name":"Newcomer","email":"n@x.ng"}`)
		if rec.Code != http.StatusConflict {
			t.Errorf("POST held tin %s = %d, want 409 (body %q)", tin, rec.Code, rec.Body.String())
			continue
		}
		if got := errorBody(t, rec); got != msgDuplicateTIN {
			t.Errorf("POST held tin %s error = %q, want %q", tin, got, msgDuplicateTIN)
		}
	}
	for id, want := range before {
		if got := rowJSON(t, super, id); got != want {
			t.Errorf("row %s after the refused POSTs = %s, want unchanged %s", id, got, want)
		}
	}
	if n := countAll(t, super, tenantA); n != 2 {
		t.Errorf("customers rows = %d, want 2", n)
	}
	if n := len(auditRows(t, super, tenantA, createdEvent)); n != 0 {
		t.Errorf("created audit rows after the refused POSTs = %d, want 0", n)
	}

	wantStatus(t, do(t, h, ctxA, http.MethodPost, "/v1/customers", createBody(tin3)), http.StatusCreated)
}

// raceHeldInsert holds an uncommitted insert of (tenant, tin), starts attempt, waits until the
// attempt blocks on a lock, then commits the held insert. It returns whether the wait happened.
func raceHeldInsert(t *testing.T, super, app *pgxpool.Pool, ctx context.Context, tin string, attempt func()) bool {
	t.Helper()
	caller, _ := auth.IdentityFromContext(ctx)
	held, release := make(chan struct{}), make(chan struct{})
	holdDone := make(chan error, 1)
	go func() {
		holdDone <- db.WithinRequestTenantTx(ctx, app, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx,
				`INSERT INTO customers (tenant_id, tin, name) VALUES ($1::uuid, $2, 'Fill Racer')`, caller.TenantID, tin); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-holdDone:
		t.Fatalf("held tx ended before holding the insert: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("held tx never held the insert")
	}

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		attempt()
	}()
	blocked := waitForInsertLockWait(super, finished)
	close(release)
	if err := <-holdDone; err != nil {
		t.Fatalf("held tx: %v", err)
	}
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("the create never returned after the held insert committed")
	}
	return blocked
}

// waitForInsertLockWait polls until an INSERT INTO customers waits on a lock, or done fires.
func waitForInsertLockWait(super *pgxpool.Pool, done <-chan struct{}) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		_ = super.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity
			 WHERE wait_event_type = 'Lock' AND query ILIKE '%insert%into%customers%'`).Scan(&n)
		if n > 0 {
			return true
		}
		select {
		case <-done:
			return false
		case <-time.After(20 * time.Millisecond):
		}
	}
	return false
}

func TestStoreCreate_RacingFillInsertIs409(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	h := newMux(store)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer create race")

	var createErr error
	blocked := raceHeldInsert(t, super, app, ctxA, tin1, func() {
		_, createErr = store.Create(ctxA, customer.CreateInput{TIN: ptr(tin1), Fields: customer.UpdateInput{Name: ptr("Racer")}})
	})
	if !blocked {
		t.Error("Store.Create never waited on the held insert: the race was not exercised")
	}
	if !errors.Is(createErr, customer.ErrDuplicateTIN) {
		t.Errorf("Store.Create after the held insert committed = %v, want ErrDuplicateTIN", createErr)
	}

	var rec *httptest.ResponseRecorder
	blockedHTTP := raceHeldInsert(t, super, app, ctxA, tin2, func() {
		rec = do(t, h, ctxA, http.MethodPost, "/v1/customers", `{"tin":"`+tin2+`","name":"Racer"}`)
	})
	if !blockedHTTP {
		t.Error("POST never waited on the held insert: the race was not exercised")
	}
	if rec == nil {
		t.Fatal("POST never returned")
	}
	if rec.Code != http.StatusConflict {
		t.Errorf("POST after the held insert committed = %d, want 409 (body %q)", rec.Code, rec.Body.String())
	} else if got := errorBody(t, rec); got != msgDuplicateTIN {
		t.Errorf("POST error = %q, want %q", got, msgDuplicateTIN)
	}

	for _, tin := range []string{tin1, tin2} {
		if n := countTIN(t, super, tenantA, tin); n != 1 {
			t.Errorf("rows for %s = %d, want 1 (the committed fill insert)", tin, n)
		}
	}
	if got := strVal(readRow(t, super, idFor(t, super, tenantA, tin1)).Name); got != "Fill Racer" {
		t.Errorf("name of the fill's row = %q, want Fill Racer (the create wrote nothing)", got)
	}
	if n := len(auditRows(t, super, tenantA, createdEvent)); n != 0 {
		t.Errorf("created audit rows after the lost races = %d, want 0", n)
	}
}

// --- edited_fields ----------------------------------------------------------

func TestStoreCreate_MarksOnlyTheFieldsItSet(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	_, _, ctxA := seedTenantWithAdmin(t, super, "customer create edited")

	c, err := store.Create(ctxA, customer.CreateInput{TIN: ptr(tin1),
		Fields: customer.UpdateInput{Name: ptr("Acme"), Email: ptr("a@x.ng"), City: ptr(""), Street: ptr("   ")}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := readRow(t, super, c.ID).Edited; !reflect.DeepEqual(got, []string{"email", "name"}) {
		t.Errorf("edited_fields = %v, want [email name]", got)
	}

	bare, err := store.Create(ctxA, customer.CreateInput{TIN: ptr(tin2)})
	if err != nil {
		t.Fatalf("Create with a TIN alone: %v", err)
	}
	if got := readRow(t, super, bare.ID).Edited; got == nil || len(got) != 0 {
		t.Errorf("edited_fields of a TIN-only create = %#v, want an empty array", got)
	}
}

// --- access -----------------------------------------------------------------

func TestStoreCreate_PreparerCanCreate(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA := seedTenant(t, super, "customer create preparer")
	_, ctxPreparer := seedMember(t, super, tenantA, "preparer", "active")

	rec := do(t, h, ctxPreparer, http.MethodPost, "/v1/customers", `{"tin":"`+tin1+`","name":"By Preparer"}`)
	wantStatus(t, rec, http.StatusCreated)
	if n := countTIN(t, super, tenantA, tin1); n != 1 {
		t.Errorf("rows for %s = %d, want 1", tin1, n)
	}
}

func TestCreateHandler_InvalidBodyFromASuspendedMemberIs403(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA := seedTenant(t, super, "customer create suspended invalid")
	_, ctxSuspended := seedMember(t, super, tenantA, "admin", "suspended")
	_, ctxActive := seedMember(t, super, tenantA, "admin", "active")

	rec := do(t, h, ctxSuspended, http.MethodPost, "/v1/customers", `{"tin":"bad"}`)
	wantStatus(t, rec, http.StatusForbidden)
	if got := errorBody(t, rec); got != db.NotActiveMemberMessage {
		t.Errorf("error = %q, want db.NotActiveMemberMessage %q", got, db.NotActiveMemberMessage)
	}

	// The same body from an active member is a 400: the gate, not the body, made the 403.
	rec = do(t, h, ctxActive, http.MethodPost, "/v1/customers", `{"tin":"bad"}`)
	wantStatus(t, rec, http.StatusBadRequest)
	if got := errorBody(t, rec); got != msgTINFormat {
		t.Errorf("control: active member error = %q, want %q", got, msgTINFormat)
	}
}

func TestCreateHandler_UndecodableBodyIs400BeforeTheGate(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA := seedTenant(t, super, "customer create suspended undecodable")
	_, ctxSuspended := seedMember(t, super, tenantA, "admin", "suspended")

	called := false
	spy := func(ctx context.Context, in customer.CreateInput) (customer.Customer, error) {
		called = true
		return store.Create(ctx, in)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/customers", customer.CreateHandler(spy, nil))

	rec := do(t, mux, ctxSuspended, http.MethodPost, "/v1/customers", `{`)
	wantStatus(t, rec, http.StatusBadRequest)
	if got := errorBody(t, rec); got != "invalid request body" {
		t.Errorf("error = %q, want invalid request body", got)
	}
	if called {
		t.Error("the store function ran for an undecodable body")
	}

	// A decodable body from the same member reaches the gate.
	wantStatus(t, do(t, mux, ctxSuspended, http.MethodPost, "/v1/customers", createBody(tin1)), http.StatusForbidden)
	if !called {
		t.Error("control: a decodable body did not reach the store function")
	}
}

// --- audit ------------------------------------------------------------------

func TestStoreCreate_WritesOneAuditRow(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, subject, ctxA := seedTenantWithAdmin(t, super, "customer create audit")

	const name, email = "Audit Secret Ltd", "audit-secret@x.ng"
	c, err := store.Create(ctxA, customer.CreateInput{TIN: ptr(tin1), Fields: customer.UpdateInput{Name: ptr(name), Email: ptr(email)}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rows := auditRows(t, super, tenantA, createdEvent)
	if len(rows) != 1 {
		t.Fatalf("audit rows for %s = %d, want 1", createdEvent, len(rows))
	}
	a := rows[0]
	if a.Actor != subject {
		t.Errorf("audit actor = %q, want the caller %q", a.Actor, subject)
	}
	if a.Payload["id"] != c.ID || a.Payload["tin"] != tin1 {
		t.Errorf("audit payload id/tin = %v/%v, want %s/%s", a.Payload["id"], a.Payload["tin"], c.ID, tin1)
	}
	var fields []string
	if raw, ok := a.Payload["fields"].([]any); ok {
		for _, f := range raw {
			fields = append(fields, f.(string))
		}
	}
	sort.Strings(fields)
	if !reflect.DeepEqual(fields, []string{"email", "name"}) {
		t.Errorf("audit payload fields = %v, want [email name]", a.Payload["fields"])
	}
	if len(a.Payload) != 3 {
		t.Errorf("audit payload keys = %v, want exactly id, tin, fields", a.Payload)
	}
	if strings.Contains(a.Raw, name) || strings.Contains(a.Raw, email) {
		t.Errorf("audit payload %s carries a value; field names only", a.Raw)
	}
}

func TestStoreCreate_AuditFailureRollsBackTheRecord(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer create atomic")

	// A non-UUID subject skips the membership lookup, and 256 characters fail
	// audit_log's actor length CHECK after the INSERT has run.
	badCtx := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: strings.Repeat("a", 256), Role: "authenticated", TenantID: tenantA})
	_, err := store.Create(badCtx, customer.CreateInput{TIN: ptr(tin1), Fields: customer.UpdateInput{Email: ptr("a@x.ng")}})
	if err == nil {
		t.Fatal("Create with an audit-failing actor succeeded, want an error")
	}
	// audit_log's actor CHECK is also SQLSTATE 23514; it is an operational 500, not a 400.
	if errors.Is(err, customer.ErrValidation) {
		t.Errorf("audit-write failure = %v, want an error that is not ErrValidation", err)
	}
	if n := countTIN(t, super, tenantA, tin1); n != 0 {
		t.Errorf("rows for %s after the failed audit = %d, want 0 (rolled back)", tin1, n)
	}

	if _, err := store.Create(ctxA, customer.CreateInput{TIN: ptr(tin1), Fields: customer.UpdateInput{Email: ptr("a@x.ng")}}); err != nil {
		t.Fatalf("control: Create with a normal actor: %v", err)
	}
	if n := countTIN(t, super, tenantA, tin1); n != 1 {
		t.Errorf("control: rows for %s = %d, want 1", tin1, n)
	}
}
