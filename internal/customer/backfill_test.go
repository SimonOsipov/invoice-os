package customer_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/customer"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

// bfInv is one pre-fill invoice; a nil field stays NULL.
type bfInv struct {
	Number                       string
	At                           time.Time
	TIN, Name, Email, City, Kind *string
	Phone, Street, Postal        *string
	Country, State, LGA          *string
	ID                           string // empty takes the column default
}

var bfBase = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// bfAt orders invoices: a larger n is newer.
func bfAt(n int) time.Time { return bfBase.Add(time.Duration(n) * time.Hour) }

// bfInsert writes invoices through the superuser pool so the fill never runs.
func bfInsert(t *testing.T, super *pgxpool.Pool, tenantID, entityID string, invs ...bfInv) {
	t.Helper()
	for _, in := range invs {
		if _, err := super.Exec(context.Background(),
			`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, created_at, buyer_tin, buyer_name, buyer_email, buyer_city, invoice_kind,
				buyer_telephone, buyer_street, buyer_postal_zone, buyer_country, buyer_state, buyer_lga)
			 VALUES (coalesce(nullif($1, '')::uuid, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
			in.ID, tenantID, entityID, in.Number, in.At, in.TIN, in.Name, in.Email, in.City, in.Kind,
			in.Phone, in.Street, in.Postal, in.Country, in.State, in.LGA); err != nil {
			t.Fatalf("insert invoice %s: %v", in.Number, err)
		}
	}
}

func bfTenant(t *testing.T, super *pgxpool.Pool, label string) (tenantID, entityID string) {
	t.Helper()
	tenantID = seedTenant(t, super, label)
	return tenantID, seedEntity(t, super, tenantID, label+" entity")
}

// bfRun runs Backfill as invoice_app. A failed run is reported and the zero result returned.
func bfRun(t *testing.T, app *pgxpool.Pool, tenantID string, dryRun bool) customer.BackfillResult {
	t.Helper()
	res, err := customer.Backfill(context.Background(), app, tenantID, dryRun)
	if err != nil {
		t.Errorf("Backfill(dryRun=%v) = %v, want nil error", dryRun, err)
	}
	return res
}

// bfRecords maps tin to customers row for the tenant.
func bfRecords(t *testing.T, super *pgxpool.Pool, tenantID string) map[string]row {
	t.Helper()
	rows, err := super.Query(context.Background(), `SELECT id, tin FROM customers WHERE tenant_id = $1`, tenantID)
	if err != nil {
		t.Fatalf("list customers: %v", err)
	}
	ids := map[string]string{}
	for rows.Next() {
		var id, tin string
		if err := rows.Scan(&id, &tin); err != nil {
			t.Fatalf("scan customers: %v", err)
		}
		ids[tin] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate customers: %v", err)
	}
	out := map[string]row{}
	for tin, id := range ids {
		out[tin] = readRow(t, super, id)
	}
	return out
}

// bfSnapshot captures every customers row with its xmin, so an UPDATE that rewrites equal values shows.
func bfSnapshot(t *testing.T, super *pgxpool.Pool, tenantID string) []string {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT xmin::text || ' ' || to_jsonb(c)::text FROM customers c WHERE tenant_id = $1 ORDER BY tin`, tenantID)
	if err != nil {
		t.Fatalf("snapshot customers: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan snapshot: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate snapshot: %v", err)
	}
	return out
}

func bfCount(t *testing.T, super *pgxpool.Pool, query, tenantID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(), query, tenantID).Scan(&n); err != nil {
		t.Fatalf("count (%s): %v", query, err)
	}
	return n
}

const bfCountCustomers = `SELECT count(*) FROM customers WHERE tenant_id = $1`

func bfWant(t *testing.T, field string, got *string, want string) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s = %s, want %s", field, strVal(got), want)
	}
}

func bfWantNull(t *testing.T, field string, got *string) {
	t.Helper()
	if got != nil {
		t.Errorf("%s = %s, want NULL", field, strVal(got))
	}
}

func bfTINs(m map[string]row) []string {
	var out []string
	for tin := range m {
		out = append(out, tin)
	}
	return out
}

func TestBackfill_CreatesOneRecordPerFileableTIN(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf create")
	padded := " " + tin1 + " "
	bfInsert(t, super, tenant, entity,
		bfInv{Number: "C-1", At: bfAt(1), TIN: &padded},
		bfInv{Number: "C-2", At: bfAt(2), TIN: &padded},
		bfInv{Number: "C-3", At: bfAt(3), TIN: ptr(tin1)},
		bfInv{Number: "C-4", At: bfAt(4), TIN: ptr(tin2)})

	res := bfRun(t, app, tenant, false)

	if res.InvoicesRead != 4 || res.TINs != 2 || res.Created != 2 || res.Existing != 0 {
		t.Errorf("result = InvoicesRead %d, TINs %d, Created %d, Existing %d; want 4, 2, 2, 0",
			res.InvoicesRead, res.TINs, res.Created, res.Existing)
	}
	recs := bfRecords(t, super, tenant)
	if len(recs) != 2 {
		t.Fatalf("records = %v, want exactly %s and %s", bfTINs(recs), tin1, tin2)
	}
	for _, tin := range []string{tin1, tin2} {
		if _, ok := recs[tin]; !ok {
			t.Errorf("no record with the trimmed TIN %q; have %v", tin, bfTINs(recs))
		}
	}
}

func TestBackfill_EachFieldTakesTheNewestNonBlankValue(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf newest")
	bfInsert(t, super, tenant, entity,
		bfInv{Number: "N-1", At: bfAt(1), TIN: ptr(tin1), Name: ptr("Old Ltd"), Email: ptr("old@x.ng"), City: ptr("Kano")},
		bfInv{Number: "N-2", At: bfAt(2), TIN: ptr(tin1), Name: ptr("  "), Email: ptr("  "), City: ptr("Abuja")},
		bfInv{Number: "N-3", At: bfAt(3), TIN: ptr(tin1), Name: ptr("New Ltd")})

	bfRun(t, app, tenant, false)

	recs := bfRecords(t, super, tenant)
	r, ok := recs[tin1]
	if !ok {
		t.Fatalf("no record for %s; have %v", tin1, bfTINs(recs))
	}
	bfWant(t, "name", r.Name, "New Ltd")
	bfWant(t, "email", r.Email, "old@x.ng")
	bfWant(t, "city", r.City, "Abuja")
	bfWantNull(t, "telephone", r.Telephone)
	bfWantNull(t, "contact_person", r.ContactPerson)
	if r.Edited == nil || len(r.Edited) != 0 {
		t.Errorf("edited_fields = %v, want empty", r.Edited)
	}
}

func TestBackfill_BuyerTypeComesOnlyFromInvoiceKind(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf kind")
	bfInsert(t, super, tenant, entity,
		bfInv{Number: "K-1", At: bfAt(1), TIN: ptr(tin1), Kind: ptr(" B2G ")},
		bfInv{Number: "K-2", At: bfAt(2), TIN: ptr(tin1), Kind: ptr("b2b")},
		bfInv{Number: "K-3", At: bfAt(3), TIN: ptr(tin2)})

	bfRun(t, app, tenant, false)

	recs := bfRecords(t, super, tenant)
	if len(recs) != 2 {
		t.Fatalf("records = %v, want 2", bfTINs(recs))
	}
	bfWant(t, tin1+" buyer_type", recs[tin1].BuyerType, "B2G")
	bfWantNull(t, tin2+" buyer_type", recs[tin2].BuyerType)
}

func TestBackfill_RNTINIsFileable(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf rn")
	bfInsert(t, super, tenant, entity, bfInv{Number: "RN-1", At: bfAt(1), TIN: ptr("RN-123")})

	res := bfRun(t, app, tenant, false)

	recs := bfRecords(t, super, tenant)
	if _, ok := recs["RN-123"]; !ok || len(recs) != 1 {
		t.Errorf("records = %v, want exactly RN-123", bfTINs(recs))
	}
	if res.Created != 1 || len(res.Skipped) != 0 {
		t.Errorf("Created %d, Skipped %v; want 1 and none", res.Created, res.Skipped)
	}
}

func TestBackfill_DryRunWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf dry")
	bfInsert(t, super, tenant, entity,
		bfInv{Number: "D-1", At: bfAt(1), TIN: ptr(tin1), Name: ptr("A Ltd")},
		bfInv{Number: "D-2", At: bfAt(2), TIN: ptr(tin2), Name: ptr("B Ltd")})

	dry := bfRun(t, app, tenant, true)

	if dry.Created != 2 || dry.TINs != 2 {
		t.Errorf("dry run Created %d, TINs %d; want 2, 2", dry.Created, dry.TINs)
	}
	if n := bfCount(t, super, bfCountCustomers, tenant); n != 0 {
		t.Errorf("customers after the dry run = %d, want 0", n)
	}
	real := bfRun(t, app, tenant, false)
	if !reflect.DeepEqual(dry, real) {
		t.Errorf("dry run reported %+v, the real run %+v; want the same counts", dry, real)
	}
	if n := bfCount(t, super, bfCountCustomers, tenant); n != 2 {
		t.Errorf("control: customers after the real run = %d, want 2", n)
	}
}

func TestBackfill_DryRunCountsAnExistingRecordAsUnchanged(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf dry existing")
	seedCustomer(t, super, tenant, seed{TIN: tin1, Name: ptr("Seeded Ltd")})
	bfInsert(t, super, tenant, entity,
		bfInv{Number: "E-1", At: bfAt(1), TIN: ptr(tin1), Name: ptr("Invoice Ltd")},
		bfInv{Number: "E-2", At: bfAt(2), TIN: ptr(tin2)})
	before := bfSnapshot(t, super, tenant)
	if len(before) != 1 {
		t.Fatalf("control: seeded rows = %d, want 1", len(before))
	}

	res := bfRun(t, app, tenant, true)

	if res.TINs != 2 || res.Created != 1 || res.Existing != 1 {
		t.Errorf("TINs %d, Created %d, Existing %d; want 2, 1, 1", res.TINs, res.Created, res.Existing)
	}
	if after := bfSnapshot(t, super, tenant); !reflect.DeepEqual(before, after) {
		t.Errorf("customers changed by a dry run: before %v, after %v", before, after)
	}
}

func TestBackfill_DryRunAfterTheWriteHasNothingToCreate(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf dry after")
	bfInsert(t, super, tenant, entity,
		bfInv{Number: "A-1", At: bfAt(1), TIN: ptr(tin1)},
		bfInv{Number: "A-2", At: bfAt(2), TIN: ptr(tin2)})

	if first := bfRun(t, app, tenant, false); first.Created != 2 {
		t.Errorf("real run Created = %d, want 2", first.Created)
	}
	dry := bfRun(t, app, tenant, true)

	if dry.Created != 0 || dry.Existing != 2 {
		t.Errorf("dry run after the write: Created %d, Existing %d; want 0, 2", dry.Created, dry.Existing)
	}
}

func TestBackfill_TenantWithNoInvoicesReportsZeros(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, _ := bfTenant(t, super, "bf empty")

	res := bfRun(t, app, tenant, false)

	if res.InvoicesRead != 0 || res.TINs != 0 || res.Created != 0 || res.Existing != 0 || res.NoTIN != 0 {
		t.Errorf("counts = %+v, want all zero", res)
	}
	if len(res.Skipped) != 0 || len(res.Collisions) != 0 {
		t.Errorf("Skipped %v, Collisions %v; want both empty", res.Skipped, res.Collisions)
	}
}

func TestBackfill_SkippedAndCollisionsAreOrdered(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf order")
	bfInsert(t, super, tenant, entity,
		bfInv{Number: "MAL-B", At: bfAt(1), TIN: ptr("999")},
		bfInv{Number: "MAL-A", At: bfAt(2), TIN: ptr("abc")},
		bfInv{Number: "O-1", At: bfAt(3), TIN: ptr(tin2), Name: ptr("D Ltd")},
		bfInv{Number: "O-2", At: bfAt(4), TIN: ptr(tin2), Name: ptr("C Ltd")},
		bfInv{Number: "O-3", At: bfAt(5), TIN: ptr(tin1), Name: ptr("B Ltd")},
		bfInv{Number: "O-4", At: bfAt(6), TIN: ptr(tin1), Name: ptr("A Ltd")})

	res := bfRun(t, app, tenant, true)

	wantSkipped := []customer.SkippedInvoice{{InvoiceNumber: "MAL-A", TIN: "abc"}, {InvoiceNumber: "MAL-B", TIN: "999"}}
	if !reflect.DeepEqual(res.Skipped, wantSkipped) {
		t.Errorf("Skipped = %+v, want %+v", res.Skipped, wantSkipped)
	}
	wantCollisions := []customer.Collision{
		{TIN: tin1, Names: []string{"A Ltd", "B Ltd"}, Chosen: "A Ltd"},
		{TIN: tin2, Names: []string{"C Ltd", "D Ltd"}, Chosen: "C Ltd"},
	}
	if !reflect.DeepEqual(res.Collisions, wantCollisions) {
		t.Errorf("Collisions = %+v, want %+v", res.Collisions, wantCollisions)
	}
}

// Tenant A goes through the real save (the fill runs); tenant B holds the same buyer
// values inserted directly. The backfill must reproduce the fill's record.
func TestBackfill_MatchesWhatTheFillWrites(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "bf parity A")
	entityA := seedEntity(t, super, tenantA, "A entity")
	tenantB, entityB := bfTenant(t, super, "bf parity B")
	store := invoice.NewStore(app)

	saves := []struct {
		number                  string
		name, email, city, kind *string
	}{
		{"P-1", ptr("One Ltd"), ptr("a@x.ng"), ptr("Kano"), ptr("B2B")},
		{"P-2", ptr("Two Ltd"), ptr("  "), nil, nil},
		{"P-3", ptr("  "), ptr("c@x.ng"), ptr("Abuja"), ptr("B2G")},
	}
	for i, s := range saves {
		if _, err := store.Create(ctxA, invoice.CreateInput{
			EntityID: entityA, InvoiceNumber: s.number, BuyerTIN: ptr(tin1), BuyerName: s.name,
			BuyerEmail: s.email, BuyerCity: s.city, InvoiceKind: s.kind,
		}); err != nil {
			t.Fatalf("Create(%s): %v", s.number, err)
		}
		bfInsert(t, super, tenantB, entityB,
			bfInv{Number: s.number, At: bfAt(i + 1), TIN: ptr(tin1), Name: s.name, Email: s.email, City: s.city, Kind: s.kind})
	}
	a, ok := bfRecords(t, super, tenantA)[tin1]
	if !ok {
		t.Fatalf("control: the fill wrote no record for %s", tin1)
	}
	bfWant(t, "control: fill name", a.Name, "Two Ltd")
	bfWant(t, "control: fill buyer_type", a.BuyerType, "B2G")
	bfWant(t, "control: fill email", a.Email, "c@x.ng")
	bfWant(t, "control: fill city", a.City, "Abuja")

	bfRun(t, app, tenantB, false)

	b, ok := bfRecords(t, super, tenantB)[tin1]
	if !ok {
		t.Fatalf("backfill wrote no record for %s", tin1)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("backfilled record differs from the fill's:\n fill     %+v\n backfill %+v", deref(a), deref(b))
	}
}

func deref(r row) map[string]string {
	out := map[string]string{"tin": r.TIN}
	for k, p := range map[string]*string{
		"name": r.Name, "contact_person": r.ContactPerson, "buyer_type": r.BuyerType, "email": r.Email,
		"telephone": r.Telephone, "street": r.Street, "city": r.City, "postal_zone": r.PostalZone,
		"country": r.Country, "state": r.State, "lga": r.LGA,
	} {
		out[k] = strVal(p)
	}
	return out
}

func TestBackfill_MalformedTINIsSkippedAndReported(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf malformed")
	bfInsert(t, super, tenant, entity,
		bfInv{Number: "MAL-1", At: bfAt(1), TIN: ptr("12345678")},
		bfInv{Number: "MAL-2", At: bfAt(2)},
		bfInv{Number: "MAL-3", At: bfAt(3), TIN: ptr("  ")},
		bfInv{Number: "OK-1", At: bfAt(4), TIN: ptr(tin1)})

	res := bfRun(t, app, tenant, false)

	wantSkipped := []customer.SkippedInvoice{{InvoiceNumber: "MAL-1", TIN: "12345678"}}
	if !reflect.DeepEqual(res.Skipped, wantSkipped) {
		t.Errorf("Skipped = %+v, want %+v", res.Skipped, wantSkipped)
	}
	if res.NoTIN != 2 || res.InvoicesRead != 4 || res.TINs != 1 || res.Created != 1 {
		t.Errorf("NoTIN %d, InvoicesRead %d, TINs %d, Created %d; want 2, 4, 1, 1", res.NoTIN, res.InvoicesRead, res.TINs, res.Created)
	}
	recs := bfRecords(t, super, tenant)
	if _, ok := recs[tin1]; !ok || len(recs) != 1 {
		t.Errorf("records = %v, want exactly %s", bfTINs(recs), tin1)
	}
	if _, ok := recs["12345678"]; ok {
		t.Error("a record holds the malformed TIN 12345678")
	}
}

func TestBackfill_TwoNamesOnOneTINCreateOneRecordWithTheNewestName(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf two names")
	bfInsert(t, super, tenant, entity,
		bfInv{Number: "T-1", At: bfAt(1), TIN: ptr(tin1), Name: ptr("Alpha Ltd")},
		bfInv{Number: "T-2", At: bfAt(2), TIN: ptr(tin1), Name: ptr("Alpha Ltd")},
		bfInv{Number: "T-3", At: bfAt(3), TIN: ptr(tin1), Name: ptr("Beta Ltd")})

	res := bfRun(t, app, tenant, false)

	recs := bfRecords(t, super, tenant)
	if len(recs) != 1 {
		t.Fatalf("records = %v, want one", bfTINs(recs))
	}
	bfWant(t, "name", recs[tin1].Name, "Beta Ltd")
	want := []customer.Collision{{TIN: tin1, Names: []string{"Alpha Ltd", "Beta Ltd"}, Chosen: "Beta Ltd", Existing: false}}
	if !reflect.DeepEqual(res.Collisions, want) {
		t.Errorf("Collisions = %+v, want %+v", res.Collisions, want)
	}
}

func TestBackfill_OneNameIsNoCollision(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf one name")
	bfInsert(t, super, tenant, entity,
		bfInv{Number: "S-1", At: bfAt(1), TIN: ptr(tin1), Name: ptr(" Alpha Ltd ")},
		bfInv{Number: "S-2", At: bfAt(2), TIN: ptr(tin1), Name: ptr("Alpha Ltd")})

	res := bfRun(t, app, tenant, false)

	if res.Created != 1 {
		t.Errorf("control: Created = %d, want 1", res.Created)
	}
	if len(res.Collisions) != 0 {
		t.Errorf("Collisions = %+v, want none", res.Collisions)
	}
}

func TestBackfill_ExistingEditedRecordStaysExactlyAsItWas(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf existing edited")
	id := seedCustomer(t, super, tenant, seed{
		TIN: tin1, Name: ptr("Typed"), Email: ptr("typed@x.ng"), Edited: []string{"name", "email"}})
	bfInsert(t, super, tenant, entity,
		bfInv{Number: "X-1", At: bfAt(1), TIN: ptr(tin1), Name: ptr("Other Ltd"), Email: ptr("o@x.ng"), City: ptr("Lagos"), Kind: ptr("B2B")})
	before := readRow(t, super, id)
	snap := bfSnapshot(t, super, tenant)

	res := bfRun(t, app, tenant, false)

	if res.Existing != 1 || res.Created != 0 {
		t.Errorf("Existing %d, Created %d; want 1, 0", res.Existing, res.Created)
	}
	if after := readRow(t, super, id); !reflect.DeepEqual(before, after) {
		t.Errorf("record changed:\n before %+v\n after  %+v", deref(before), deref(after))
	}
	if after := bfSnapshot(t, super, tenant); !reflect.DeepEqual(snap, after) {
		t.Errorf("row rewritten: before %v, after %v", snap, after)
	}
}

func TestBackfill_ExistingRecordCollisionReportsTheRecordName(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf existing collision")
	seedCustomer(t, super, tenant, seed{TIN: tin1, Name: ptr("Typed")})
	bfInsert(t, super, tenant, entity,
		bfInv{Number: "Y-1", At: bfAt(1), TIN: ptr(tin1), Name: ptr("A Ltd")},
		bfInv{Number: "Y-2", At: bfAt(2), TIN: ptr(tin1), Name: ptr("B Ltd")})

	res := bfRun(t, app, tenant, false)

	want := []customer.Collision{{TIN: tin1, Names: []string{"A Ltd", "B Ltd"}, Chosen: "Typed", Existing: true}}
	if !reflect.DeepEqual(res.Collisions, want) {
		t.Errorf("Collisions = %+v, want %+v", res.Collisions, want)
	}
}

func TestBackfill_SecondRunIsANoOp(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf second run")
	bfInsert(t, super, tenant, entity,
		bfInv{Number: "R-1", At: bfAt(1), TIN: ptr(tin1), Name: ptr("A Ltd")},
		bfInv{Number: "R-2", At: bfAt(2), TIN: ptr(tin2), Name: ptr("B Ltd")})

	if first := bfRun(t, app, tenant, false); first.Created != 2 {
		t.Errorf("first run Created = %d, want 2", first.Created)
	}
	snap := bfSnapshot(t, super, tenant)
	if len(snap) != 2 {
		t.Fatalf("control: rows after the first run = %d, want 2", len(snap))
	}

	second := bfRun(t, app, tenant, false)

	if second.Created != 0 || second.Existing != 2 {
		t.Errorf("second run Created %d, Existing %d; want 0, 2", second.Created, second.Existing)
	}
	if after := bfSnapshot(t, super, tenant); !reflect.DeepEqual(snap, after) {
		t.Errorf("second run changed rows: before %v, after %v", snap, after)
	}
}

func TestBackfill_WritesNoAuditRow(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf audit")
	bfInsert(t, super, tenant, entity,
		bfInv{Number: "U-1", At: bfAt(1), TIN: ptr(tin1)},
		bfInv{Number: "U-2", At: bfAt(2), TIN: ptr(tin2)})

	bfRun(t, app, tenant, false)

	if n := bfCount(t, super, bfCountCustomers, tenant); n != 2 {
		t.Errorf("control: customers = %d, want 2", n)
	}
	if n := bfCount(t, super, `SELECT count(*) FROM audit_log WHERE tenant_id = $1`, tenant); n != 0 {
		t.Errorf("audit_log rows for the tenant = %d, want 0", n)
	}
}

func TestRLS_CustomerBackfillRefusesABypassRLSRole(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf refuse")
	bfInsert(t, super, tenant, entity, bfInv{Number: "Z-1", At: bfAt(1), TIN: ptr(tin1)})

	res, err := customer.Backfill(context.Background(), super, tenant, false)

	if !errors.Is(err, customer.ErrBackfillPrivilegedRole) {
		t.Errorf("Backfill over the superuser pool = %v, want ErrBackfillPrivilegedRole", err)
	}
	if !reflect.DeepEqual(res, customer.BackfillResult{}) {
		t.Errorf("result on refusal = %+v, want the zero value", res)
	}
	if n := bfCount(t, super, bfCountCustomers, tenant); n != 0 {
		t.Errorf("customers after the refusal = %d, want 0", n)
	}
	if dry := bfRun(t, app, tenant, true); dry.Created != 1 {
		t.Errorf("control: invoice_app dry run Created = %d, want 1 (the refusal was the only obstacle)", dry.Created)
	}
}

func TestRLS_CustomerBackfillDoesNotCrossTenants(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA, entityA := bfTenant(t, super, "bf cross A")
	tenantB, entityB := bfTenant(t, super, "bf cross B")
	bfInsert(t, super, tenantA, entityA, bfInv{Number: "XA-1", At: bfAt(1), TIN: ptr(tin1), Name: ptr("A Ltd")})
	bfInsert(t, super, tenantB, entityB,
		bfInv{Number: "XB-1", At: bfAt(1), TIN: ptr(tin1), Name: ptr("B Ltd")},
		bfInv{Number: "XB-2", At: bfAt(2), TIN: ptr(tin2), Name: ptr("B Two Ltd")})

	res := bfRun(t, app, tenantA, false)

	if res.InvoicesRead != 1 || res.TINs != 1 {
		t.Errorf("tenant A run read %d invoices, %d TINs; want 1, 1", res.InvoicesRead, res.TINs)
	}
	recsA := bfRecords(t, super, tenantA)
	if len(recsA) != 1 {
		t.Fatalf("tenant A records = %v, want exactly %s", bfTINs(recsA), tin1)
	}
	bfWant(t, "A's name", recsA[tin1].Name, "A Ltd")
	if n := bfCount(t, super, bfCountCustomers, tenantB); n != 0 {
		t.Errorf("tenant B records after A's run = %d, want 0", n)
	}
	snapA := bfSnapshot(t, super, tenantA)

	bfRun(t, app, tenantB, false)

	recsB := bfRecords(t, super, tenantB)
	if len(recsB) != 2 {
		t.Fatalf("tenant B records after its own run = %v, want 2", bfTINs(recsB))
	}
	bfWant(t, "B's name", recsB[tin1].Name, "B Ltd")
	if after := bfSnapshot(t, super, tenantA); !reflect.DeepEqual(snapA, after) {
		t.Errorf("tenant A's record changed by B's run: before %v, after %v", snapA, after)
	}
}

func TestBackfill_EveryColumnComesFromItsOwnInvoiceField(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf columns")
	bfInsert(t, super, tenant, entity,
		bfInv{Number: "F-1", At: bfAt(1), TIN: ptr(tin1), Name: ptr("old name"), Kind: ptr("B2B"), Email: ptr("old email"),
			Phone: ptr("old phone"), Street: ptr("old street"), City: ptr("old city"), Postal: ptr("old postal"),
			Country: ptr("old country"), State: ptr("old state"), LGA: ptr("old lga")},
		bfInv{Number: "F-2", At: bfAt(2), TIN: ptr(tin1), Name: ptr(" "), Kind: ptr(" "), Email: ptr(" "),
			Phone: ptr(" "), Street: ptr(" "), City: ptr(" "), Postal: ptr(" "),
			Country: ptr(" "), State: ptr(" "), LGA: ptr(" ")},
		bfInv{Number: "F-3", At: bfAt(3), TIN: ptr(tin1), Name: ptr(" new name "), Kind: ptr("B2C"), Email: ptr("new email"),
			Phone: ptr("new phone"), Street: ptr("new street"), City: ptr("new city"), Postal: ptr("new postal"),
			Country: ptr("new country"), State: ptr("new state")})

	bfRun(t, app, tenant, false)

	r, ok := bfRecords(t, super, tenant)[tin1]
	if !ok {
		t.Fatalf("no record for %s", tin1)
	}
	for _, c := range []struct {
		col  string
		got  *string
		want string
	}{
		{"name", r.Name, "new name"}, {"buyer_type", r.BuyerType, "B2C"}, {"email", r.Email, "new email"},
		{"telephone", r.Telephone, "new phone"}, {"street", r.Street, "new street"}, {"city", r.City, "new city"},
		{"postal_zone", r.PostalZone, "new postal"}, {"country", r.Country, "new country"},
		{"state", r.State, "new state"}, {"lga", r.LGA, "old lga"},
	} {
		bfWant(t, c.col, c.got, c.want)
	}
	bfWantNull(t, "contact_person", r.ContactPerson)
}

func TestBackfill_BuyerTypeAcceptsExactlyB2BB2GB2C(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf kinds")
	tins := []string{tin1, tin2, tin3, tin4}
	kinds := []string{"B2B", "B2G", "B2C", "B2X"}
	for i, k := range kinds {
		bfInsert(t, super, tenant, entity, bfInv{Number: "KD-" + k, At: bfAt(i + 1), TIN: ptr(tins[i]), Kind: ptr(k)})
	}

	bfRun(t, app, tenant, false)

	recs := bfRecords(t, super, tenant)
	if len(recs) != 4 {
		t.Fatalf("records = %v, want 4", bfTINs(recs))
	}
	bfWant(t, "B2B", recs[tin1].BuyerType, "B2B")
	bfWant(t, "B2G", recs[tin2].BuyerType, "B2G")
	bfWant(t, "B2C", recs[tin3].BuyerType, "B2C")
	bfWantNull(t, "B2X", recs[tin4].BuyerType)
}

func TestBackfill_EqualCreatedAtTakesTheNewerID(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf tie")
	bfInsert(t, super, tenant, entity,
		bfInv{Number: "TI-1", At: bfAt(1), ID: "00000000-0000-4000-8000-000000000002", TIN: ptr(tin1), Name: ptr("Higher Id Ltd")},
		bfInv{Number: "TI-2", At: bfAt(1), ID: "00000000-0000-4000-8000-000000000001", TIN: ptr(tin1), Name: ptr("Lower Id Ltd")})

	res := bfRun(t, app, tenant, false)

	bfWant(t, "name", bfRecords(t, super, tenant)[tin1].Name, "Higher Id Ltd")
	if len(res.Collisions) != 1 || res.Collisions[0].Chosen != "Higher Id Ltd" {
		t.Errorf("Collisions = %+v, want one choosing Higher Id Ltd", res.Collisions)
	}
}

func TestBackfill_CollisionNamesSortByByteOrder(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf name sort")
	bfInsert(t, super, tenant, entity,
		bfInv{Number: "NS-1", At: bfAt(1), TIN: ptr(tin1), Name: ptr("a Ltd")},
		bfInv{Number: "NS-2", At: bfAt(2), TIN: ptr(tin1), Name: ptr("B Ltd")})

	res := bfRun(t, app, tenant, true)

	if len(res.Collisions) != 1 || !reflect.DeepEqual(res.Collisions[0].Names, []string{"B Ltd", "a Ltd"}) {
		t.Errorf("Collisions = %+v, want one with Names [B Ltd a Ltd]", res.Collisions)
	}
}

func TestBackfill_CollisionsFollowTINOrderNotInsertOrder(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf tin order")
	tins := []string{tin4, tin2, tin3, tin1}
	for i, tin := range tins {
		bfInsert(t, super, tenant, entity,
			bfInv{Number: "TO-" + tin + "-a", At: bfAt(2 * i), TIN: ptr(tin), Name: ptr("X Ltd")},
			bfInv{Number: "TO-" + tin + "-b", At: bfAt(2*i + 1), TIN: ptr(tin), Name: ptr("Y Ltd")})
	}

	res := bfRun(t, app, tenant, true)

	var got []string
	for _, c := range res.Collisions {
		got = append(got, c.TIN)
	}
	want := append([]string(nil), tins...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("collision TINs = %v, want ascending %v", got, want)
	}
}

func TestBackfill_TINWithNoNameStillGetsARecord(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf no name")
	bfInsert(t, super, tenant, entity,
		bfInv{Number: "NN-1", At: bfAt(1), TIN: ptr(tin1), Name: ptr("  ")},
		bfInv{Number: "NN-2", At: bfAt(2), TIN: ptr(tin1)})

	res := bfRun(t, app, tenant, false)

	if res.Created != 1 || len(res.Collisions) != 0 {
		t.Errorf("Created %d, Collisions %+v; want 1 and none", res.Created, res.Collisions)
	}
	r, ok := bfRecords(t, super, tenant)[tin1]
	if !ok {
		t.Fatalf("no record for %s", tin1)
	}
	bfWantNull(t, "name", r.Name)
}

func TestRLS_CustomerBackfillIgnoresAnotherTenantsRecord(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA, entityA := bfTenant(t, super, "bf other rec A")
	tenantB, _ := bfTenant(t, super, "bf other rec B")
	seedCustomer(t, super, tenantB, seed{TIN: tin1, Name: ptr("B Typed")})
	bfInsert(t, super, tenantA, entityA, bfInv{Number: "OR-1", At: bfAt(1), TIN: ptr(tin1), Name: ptr("A Ltd")})

	res := bfRun(t, app, tenantA, false)

	if res.Created != 1 || res.Existing != 0 {
		t.Errorf("Created %d, Existing %d; want 1, 0 (tenant B's record is not tenant A's)", res.Created, res.Existing)
	}
	bfWant(t, "A's name", bfRecords(t, super, tenantA)[tin1].Name, "A Ltd")
	bfWant(t, "B's name", bfRecords(t, super, tenantB)[tin1].Name, "B Typed")
}

// A BYPASSRLS role that is not a superuser must be refused too.
func TestRLS_CustomerBackfillRefusesANonSuperuserBypassRLSRole(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenant, entity := bfTenant(t, super, "bf bypass")
	bfInsert(t, super, tenant, entity, bfInv{Number: "BY-1", At: bfAt(1), TIN: ptr(tin1)})

	role := "bf_bypass_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if _, err := super.Exec(ctx, `CREATE ROLE `+role+` LOGIN BYPASSRLS PASSWORD 'bf-pass'`); err != nil {
		t.Fatalf("create role: %v", err)
	}
	t.Cleanup(func() { _, _ = super.Exec(ctx, `DROP ROLE `+role) })
	var isSuper, bypass bool
	if err := super.QueryRow(ctx, `SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = $1`, role).Scan(&isSuper, &bypass); err != nil {
		t.Fatalf("read role: %v", err)
	}
	if isSuper || !bypass {
		t.Fatalf("control: role rolsuper=%v rolbypassrls=%v, want false, true", isSuper, bypass)
	}
	if _, err := super.Exec(ctx, `GRANT invoice_app TO `+role); err != nil {
		t.Fatalf("grant invoice_app: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_SUPERUSER_URL"))
	if err != nil {
		t.Fatalf("parse superuser url: %v", err)
	}
	cfg.ConnConfig.User, cfg.ConnConfig.Password = role, "bf-pass"
	bypassPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect as %s: %v", role, err)
	}
	t.Cleanup(bypassPool.Close)

	res, err := customer.Backfill(ctx, bypassPool, tenant, false)

	if !errors.Is(err, customer.ErrBackfillPrivilegedRole) {
		t.Errorf("Backfill over a BYPASSRLS pool = %v, want ErrBackfillPrivilegedRole", err)
	}
	if !reflect.DeepEqual(res, customer.BackfillResult{}) {
		t.Errorf("result on refusal = %+v, want the zero value", res)
	}
	if n := bfCount(t, super, bfCountCustomers, tenant); n != 0 {
		t.Errorf("customers after the refusal = %d, want 0", n)
	}
	if dry := bfRun(t, app, tenant, true); dry.Created != 1 {
		t.Errorf("control: invoice_app dry run Created = %d, want 1", dry.Created)
	}
}

func TestBackfill_NearMissTINsAreSkippedWithTheirRawValue(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, entity := bfTenant(t, super, "bf near miss")
	raw := []string{"x" + tin1, tin1 + "x", tin1 + "9", "RN-12a", "rn-123", " 12345678 "}
	var invs []bfInv
	for i, tin := range raw {
		invs = append(invs, bfInv{Number: fmt.Sprintf("NM-%d", i), At: bfAt(i), TIN: ptr(tin)})
	}
	invs = append(invs, bfInv{Number: "NM-ok", At: bfAt(9), TIN: ptr(tin1)})
	bfInsert(t, super, tenant, entity, invs...)

	res := bfRun(t, app, tenant, false)

	var want []customer.SkippedInvoice
	for i, tin := range raw {
		want = append(want, customer.SkippedInvoice{InvoiceNumber: fmt.Sprintf("NM-%d", i), TIN: tin})
	}
	if !reflect.DeepEqual(res.Skipped, want) {
		t.Errorf("Skipped = %+v, want %+v", res.Skipped, want)
	}
	if recs := bfRecords(t, super, tenant); len(recs) != 1 || res.Created != 1 {
		t.Errorf("records = %v, Created %d; want only %s", bfTINs(recs), res.Created, tin1)
	}
}
