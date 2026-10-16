package invoice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/customer"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	cfTIN     = "12345678-0042"
	cfFailTIN = "99999999-0001"
)

type cfRec struct {
	ID, TIN                                                                     string
	Name, BuyerType, Email, Telephone, Street, City, PostalZone, Country, State *string
	LGA                                                                         *string
	Edited                                                                      []string
}

func cfV(p *string) string {
	if p == nil {
		return "<NULL>"
	}
	return *p
}

func cfCount(t *testing.T, super *pgxpool.Pool, tenantID string) int {
	t.Helper()
	return mustCount(t, super, `SELECT count(*) FROM customers WHERE tenant_id = $1`, tenantID)
}

// cfMust reads the tenant's record for tin and fails when there is none.
func cfMust(t *testing.T, super *pgxpool.Pool, tenantID, tin string) cfRec {
	t.Helper()
	var r cfRec
	err := super.QueryRow(context.Background(),
		`SELECT id, tin, name, buyer_type, email, telephone, street, city, postal_zone, country, state, lga, edited_fields
		 FROM customers WHERE tenant_id = $1 AND tin = $2`, tenantID, tin,
	).Scan(&r.ID, &r.TIN, &r.Name, &r.BuyerType, &r.Email, &r.Telephone, &r.Street, &r.City,
		&r.PostalZone, &r.Country, &r.State, &r.LGA, &r.Edited)
	if err != nil {
		t.Fatalf("no customers row for tenant %s tin %q: %v", tenantID, tin, err)
	}
	return r
}

func cfCorrect(t *testing.T, f nrsFixture, id string, in customer.UpdateInput) {
	t.Helper()
	if _, err := customer.NewStore(f.app).Update(f.c, id, in); err != nil {
		t.Fatalf("customer.Store.Update: %v", err)
	}
}

func cfEdit(t *testing.T, f nrsFixture, id string, in UpdateInput) {
	t.Helper()
	if _, err := f.store.Edit(f.c, id, EditInput{UpdateInput: in}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
}

// cfFailFill makes a customers INSERT for (tenantID, tin) raise, as the migrator; dropped on cleanup.
func cfFailFill(t *testing.T, tenantID, tin string) {
	t.Helper()
	url := os.Getenv("DATABASE_MIGRATION_URL")
	if url == "" {
		t.Skip("fill-failure test skipped: set DATABASE_MIGRATION_URL")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect migrator: %v", err)
	}
	t.Cleanup(pool.Close)
	name := "cf_fill_fail_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	for _, q := range []string{
		fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $f$
			BEGIN
				IF NEW.tenant_id = '%s'::uuid AND NEW.tin = '%s' THEN RAISE EXCEPTION 'cf fill refused'; END IF;
				RETURN NEW;
			END $f$`, name, tenantID, tin),
		fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON customers FOR EACH ROW EXECUTE FUNCTION %s()`, name, name),
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("install fill-failure trigger: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON customers`, name))
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, name))
	})
}

func TestFill_CreateMakesTheRecordFromTheBuyerFields(t *testing.T) {
	f := newNRSFixture(t, "CF-01")
	f.create(t, "CF-1", CreateInput{
		BuyerTIN: strPtr(cfTIN), BuyerName: strPtr("Buyer One Ltd"), InvoiceKind: strPtr("B2B"),
		BuyerEmail: strPtr("b@x.ng"), BuyerTelephone: strPtr("+2348000000002"), BuyerStreet: strPtr("2 Wuse"),
		BuyerCity: strPtr("Abuja"), BuyerPostalZone: strPtr("900001"), BuyerCountry: strPtr("NG"),
		BuyerState: strPtr("NG-FC"), BuyerLGA: strPtr("NG-FC-001"),
	})
	if n := cfCount(t, f.super, f.tenantID); n != 1 {
		t.Fatalf("customers rows after Create = %d, want 1", n)
	}
	r := cfMust(t, f.super, f.tenantID, cfTIN)
	got := []string{cfV(r.Name), cfV(r.BuyerType), cfV(r.Email), cfV(r.Telephone), cfV(r.Street), cfV(r.City),
		cfV(r.PostalZone), cfV(r.Country), cfV(r.State), cfV(r.LGA)}
	want := []string{"Buyer One Ltd", "B2B", "b@x.ng", "+2348000000002", "2 Wuse", "Abuja", "900001", "NG", "NG-FC", "NG-FC-001"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("record = %v, want %v", got, want)
	}
	if len(r.Edited) != 0 {
		t.Errorf("edited_fields = %v, want {}", r.Edited)
	}
}

func TestFill_LaterSaveUpdatesUneditedFields(t *testing.T) {
	f := newNRSFixture(t, "CF-02")
	f.create(t, "CF-2A", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerEmail: strPtr("a@x.ng"), BuyerTelephone: strPtr("0801")})
	f.create(t, "CF-2B", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerEmail: strPtr("b@x.ng")})
	r := cfMust(t, f.super, f.tenantID, cfTIN)
	if cfV(r.Email) != "b@x.ng" || cfV(r.Telephone) != "0801" {
		t.Errorf("email/telephone = %s/%s, want b@x.ng/0801 (latest non-blank wins, NULL keeps)", cfV(r.Email), cfV(r.Telephone))
	}
	if n := cfCount(t, f.super, f.tenantID); n != 1 {
		t.Errorf("customers rows = %d, want 1", n)
	}
}

func TestFill_BlankInvoiceValueNeverClears(t *testing.T) {
	f := newNRSFixture(t, "CF-03")
	f.create(t, "CF-3A", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerName: strPtr("First"), BuyerCity: strPtr("Ikeja")})
	f.create(t, "CF-3B", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerName: strPtr("Second"), BuyerCity: strPtr("  ")})
	r := cfMust(t, f.super, f.tenantID, cfTIN)
	if cfV(r.Name) != "Second" {
		t.Errorf("control: name = %s, want Second (the save filled)", cfV(r.Name))
	}
	if cfV(r.City) != "Ikeja" {
		t.Errorf("city after a blank save = %s, want Ikeja", cfV(r.City))
	}
	f.create(t, "CF-3C", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerName: strPtr("Third")})
	r = cfMust(t, f.super, f.tenantID, cfTIN)
	if cfV(r.Name) != "Third" || cfV(r.City) != "Ikeja" {
		t.Errorf("name/city after a NULL-city save = %s/%s, want Third/Ikeja", cfV(r.Name), cfV(r.City))
	}
}

func TestFill_MemberCorrectionIsNeverOverwritten(t *testing.T) {
	f := newNRSFixture(t, "CF-04")
	f.create(t, "CF-4A", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerEmail: strPtr("a@x.ng")})
	cfCorrect(t, f, cfMust(t, f.super, f.tenantID, cfTIN).ID, customer.UpdateInput{Email: strPtr("fixed@x.ng")})
	f.create(t, "CF-4B", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerEmail: strPtr("new@x.ng"), BuyerCity: strPtr("Abuja")})
	r := cfMust(t, f.super, f.tenantID, cfTIN)
	if cfV(r.City) != "Abuja" {
		t.Errorf("control: city = %s, want Abuja (an uncorrected field fills)", cfV(r.City))
	}
	if cfV(r.Email) != "fixed@x.ng" {
		t.Errorf("email = %s, want fixed@x.ng (the member's correction)", cfV(r.Email))
	}
}

// cfHandCreate makes the record through customer.Store.Create, as POST /v1/customers does.
func cfHandCreate(t *testing.T, f nrsFixture, in customer.CreateInput) {
	t.Helper()
	if _, err := customer.NewStore(f.app).Create(f.c, in); err != nil {
		t.Fatalf("customer.Store.Create: %v", err)
	}
}

func TestFill_HandCreatedRecordKeepsWhatThePersonTyped(t *testing.T) {
	f := newNRSFixture(t, "CF-HC1")
	cfHandCreate(t, f, customer.CreateInput{TIN: strPtr(cfTIN), Fields: customer.UpdateInput{Name: strPtr("Typed Ltd"), Email: strPtr("typed@x.ng")}})
	f.create(t, "CF-HC1", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerName: strPtr("Invoice Ltd"), BuyerEmail: strPtr("inv@x.ng"), BuyerCity: strPtr("Abuja")})
	r := cfMust(t, f.super, f.tenantID, cfTIN)
	if cfV(r.City) != "Abuja" {
		t.Errorf("control: city = %s, want Abuja (the fill ran)", cfV(r.City))
	}
	if cfV(r.Name) != "Typed Ltd" || cfV(r.Email) != "typed@x.ng" {
		t.Errorf("name/email = %s/%s, want Typed Ltd/typed@x.ng (what the person typed)", cfV(r.Name), cfV(r.Email))
	}
}

func TestFill_AFieldLeftBlankOnCreateIsFilled(t *testing.T) {
	f := newNRSFixture(t, "CF-HC2")
	cfHandCreate(t, f, customer.CreateInput{TIN: strPtr(cfTIN), Fields: customer.UpdateInput{Name: strPtr("Typed Ltd"), City: strPtr("")}})
	f.create(t, "CF-HC2", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerName: strPtr("Invoice Ltd"), BuyerCity: strPtr("Abuja")})
	r := cfMust(t, f.super, f.tenantID, cfTIN)
	if cfV(r.City) != "Abuja" {
		t.Errorf("city = %s, want Abuja (left blank on create, so the fill fills it)", cfV(r.City))
	}
	if cfV(r.Name) != "Typed Ltd" {
		t.Errorf("name = %s, want Typed Ltd (typed on create)", cfV(r.Name))
	}
}

func TestFill_OlderInvoiceReSavedKeepsTheCorrection(t *testing.T) {
	f := newNRSFixture(t, "CF-05")
	inv1 := f.create(t, "CF-5A", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerName: strPtr("Original Ltd"), BuyerEmail: strPtr("a@x.ng")})
	cfCorrect(t, f, cfMust(t, f.super, f.tenantID, cfTIN).ID, customer.UpdateInput{Name: strPtr("Corrected Ltd")})
	cfEdit(t, f, inv1.ID, UpdateInput{BuyerName: strPtr("Old Name"), BuyerEmail: strPtr("re@x.ng")})
	r := cfMust(t, f.super, f.tenantID, cfTIN)
	if cfV(r.Email) != "re@x.ng" {
		t.Errorf("control: email = %s, want re@x.ng (the re-save filled)", cfV(r.Email))
	}
	if cfV(r.Name) != "Corrected Ltd" {
		t.Errorf("name = %s, want Corrected Ltd", cfV(r.Name))
	}
}

func TestFill_UnfileableTINMakesNoRecord(t *testing.T) {
	f := newNRSFixture(t, "CF-06")
	var ids []string
	bad := []*string{strPtr("1234567"), strPtr("rn-12"), strPtr(""), strPtr("   "), nil,
		strPtr("12345678-004"), strPtr("12345678-00421"), strPtr("123456780042"), strPtr("x12345678-0042"),
		strPtr("12345678-0042x"), strPtr("RN-"), strPtr("RN-1a"), strPtr("12345678\u20130042")}
	for i, tin := range bad {
		inv := f.create(t, fmt.Sprintf("CF-6-%d", i), CreateInput{BuyerTIN: tin, BuyerName: strPtr("No Record Ltd")})
		ids = append(ids, inv.ID)
	}
	f.create(t, "CF-6-OK", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerName: strPtr("Fileable Ltd")})
	f.create(t, "CF-6-RN", CreateInput{BuyerTIN: strPtr("RN-12"), BuyerName: strPtr("Registration Ltd")})
	for _, id := range ids {
		f.get(t, id)
	}
	cfMust(t, f.super, f.tenantID, cfTIN)
	cfMust(t, f.super, f.tenantID, "RN-12")
	if n := cfCount(t, f.super, f.tenantID); n != 2 {
		t.Errorf("customers rows = %d, want 2 (only the fileable TINs)", n)
	}
}

func TestFill_TINIsTrimmedHyphenKept(t *testing.T) {
	f := newNRSFixture(t, "CF-07")
	f.create(t, "CF-7", CreateInput{BuyerTIN: strPtr(" " + cfTIN + " "), BuyerName: strPtr("Padded Ltd")})
	if n := cfCount(t, f.super, f.tenantID); n != 1 {
		t.Fatalf("customers rows = %d, want 1", n)
	}
	if r := cfMust(t, f.super, f.tenantID, cfTIN); r.TIN != cfTIN {
		t.Errorf("tin = %q, want %q", r.TIN, cfTIN)
	}
}

func TestFill_BuyerTypeOutsideTheSetIsSkipped(t *testing.T) {
	f := newNRSFixture(t, "CF-08")
	f.create(t, "CF-8A", CreateInput{BuyerTIN: strPtr(cfTIN), InvoiceKind: strPtr("B2G")})
	if r := cfMust(t, f.super, f.tenantID, cfTIN); cfV(r.BuyerType) != "B2G" {
		t.Fatalf("buyer_type after the first save = %s, want B2G", cfV(r.BuyerType))
	}
	f.create(t, "CF-8B", CreateInput{BuyerTIN: strPtr(cfTIN), InvoiceKind: strPtr("b2b"), BuyerEmail: strPtr("b@x.ng")})
	r := cfMust(t, f.super, f.tenantID, cfTIN)
	if cfV(r.Email) != "b@x.ng" {
		t.Errorf("control: email = %s, want b@x.ng (the save filled)", cfV(r.Email))
	}
	if cfV(r.BuyerType) != "B2G" {
		t.Errorf("buyer_type after kind b2b = %s, want B2G (not in the set)", cfV(r.BuyerType))
	}
	f.create(t, "CF-8C", CreateInput{BuyerTIN: strPtr(cfTIN), InvoiceKind: strPtr("B2C")})
	if r := cfMust(t, f.super, f.tenantID, cfTIN); cfV(r.BuyerType) != "B2C" {
		t.Errorf("buyer_type after kind B2C = %s, want B2C", cfV(r.BuyerType))
	}
}

func TestFill_EditOfABuyerFieldFills(t *testing.T) {
	f := newNRSFixture(t, "CF-09")
	inv1 := f.create(t, "CF-9", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerEmail: strPtr("a@x.ng")})
	if r := cfMust(t, f.super, f.tenantID, cfTIN); cfV(r.Email) != "a@x.ng" {
		t.Fatalf("email after Create = %s, want a@x.ng", cfV(r.Email))
	}
	cfEdit(t, f, inv1.ID, UpdateInput{BuyerEmail: strPtr("c@x.ng")})
	if r := cfMust(t, f.super, f.tenantID, cfTIN); cfV(r.Email) != "c@x.ng" {
		t.Errorf("email after Edit = %s, want c@x.ng", cfV(r.Email))
	}
}

func TestFill_EditWithoutABuyerFieldLeavesTheRecord(t *testing.T) {
	f := newNRSFixture(t, "CF-10")
	inv1 := f.create(t, "CF-10A", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerEmail: strPtr("a@x.ng")})
	f.create(t, "CF-10B", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerEmail: strPtr("b@x.ng")})
	if r := cfMust(t, f.super, f.tenantID, cfTIN); cfV(r.Email) != "b@x.ng" {
		t.Fatalf("email after the second Create = %s, want b@x.ng", cfV(r.Email))
	}
	due := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	cfEdit(t, f, inv1.ID, UpdateInput{DueDate: &due})
	if r := cfMust(t, f.super, f.tenantID, cfTIN); cfV(r.Email) != "b@x.ng" {
		t.Errorf("email after a due_date edit of the older invoice = %s, want b@x.ng (no fill)", cfV(r.Email))
	}
	cfEdit(t, f, inv1.ID, UpdateInput{SupplierEmail: strPtr("sup@x.ng"), SupplierCity: strPtr("Lagos"), Currency: strPtr("USD")})
	if r := cfMust(t, f.super, f.tenantID, cfTIN); cfV(r.Email) != "b@x.ng" {
		t.Errorf("email after a supplier/currency edit of the older invoice = %s, want b@x.ng (no fill)", cfV(r.Email))
	}
}

func TestFill_FailureRollsBackTheInvoice(t *testing.T) {
	f := newNRSFixture(t, "CF-11")
	cfFailFill(t, f.tenantID, cfFailTIN)
	ok := f.create(t, "CF-11-OK", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerName: strPtr("Fine Ltd")})
	cfMust(t, f.super, f.tenantID, cfTIN)

	in := CreateInput{EntityID: f.entityID, InvoiceNumber: "CF-11-FAIL", BuyerTIN: strPtr(cfFailTIN)}
	_, err := f.store.Create(f.c, in)
	if err == nil {
		t.Fatal("Create with a failing fill succeeded, want the fill error")
	}
	if errors.Is(err, ErrValidation) || errors.Is(err, ErrDuplicateNumber) {
		t.Errorf("Create err = %v, want the raw fill error, not a domain error", err)
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE tenant_id = $1 AND invoice_number = $2`, f.tenantID, "CF-11-FAIL"); n != 0 {
		t.Errorf("invoices CF-11-FAIL stored = %d, want 0 (rolled back)", n)
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM customers WHERE tenant_id = $1 AND tin = $2`, f.tenantID, cfFailTIN); n != 0 {
		t.Errorf("customers for the failing TIN = %d, want 0", n)
	}

	failTIN := cfFailTIN
	_, err = f.store.Edit(f.c, ok.ID, EditInput{UpdateInput: UpdateInput{BuyerTIN: &failTIN}})
	if err == nil {
		t.Error("Edit to a failing TIN succeeded, want the fill error")
	} else if errors.Is(err, ErrValidation) || errors.Is(err, ErrNotFound) {
		t.Errorf("Edit err = %v, want the raw fill error, not a domain error", err)
	}
	if got := cfV(f.get(t, ok.ID).BuyerTIN); got != cfTIN {
		t.Errorf("buyer_tin after the failed Edit = %s, want %s (rolled back)", got, cfTIN)
	}
}

func TestEditBySourceDocumentTx_FillsTheDirectory(t *testing.T) {
	f := newNRSFixture(t, "CF-12")
	docID := seedDocument(t, f.super, f.tenantID)
	f.create(t, "CF-12", CreateInput{SourceDocumentID: &docID, BuyerTIN: strPtr(cfTIN), BuyerName: strPtr("Doc Buyer")})
	if r := cfMust(t, f.super, f.tenantID, cfTIN); cfV(r.Name) != "Doc Buyer" {
		t.Fatalf("name after Create = %s, want Doc Buyer", cfV(r.Name))
	}
	err := db.WithinRequestTenantTx(f.c, f.app, func(tx pgx.Tx) error {
		_, err := f.store.EditBySourceDocumentTx(f.c, tx, docID, EditInput{UpdateInput: UpdateInput{BuyerName: strPtr("Edited Buyer")}})
		return err
	})
	if err != nil {
		t.Fatalf("EditBySourceDocumentTx: %v", err)
	}
	if r := cfMust(t, f.super, f.tenantID, cfTIN); cfV(r.Name) != "Edited Buyer" {
		t.Errorf("name after the document edit = %s, want Edited Buyer", cfV(r.Name))
	}
}

func TestFill_ConcurrentCreatesForOneNewTINMakeOneRecord(t *testing.T) {
	f := newNRSFixture(t, "CF-13")
	for round := 0; round < 6; round++ {
		tin := fmt.Sprintf("%08d-0042", 40000000+round)
		start := make(chan struct{})
		errs := make(chan error, 2)
		for g := 0; g < 2; g++ {
			go func(g int) {
				<-start
				_, err := f.store.Create(f.c, CreateInput{
					EntityID: f.entityID, InvoiceNumber: fmt.Sprintf("CF-13-%d-%d", round, g),
					BuyerTIN: &tin, BuyerName: strPtr(fmt.Sprintf("Racer %d", g)),
				})
				errs <- err
			}(g)
		}
		close(start)
		for g := 0; g < 2; g++ {
			if err := <-errs; err != nil {
				t.Errorf("round %d: concurrent Create: %v", round, err)
			}
		}
		if n := mustCount(t, f.super, `SELECT count(*) FROM customers WHERE tenant_id = $1 AND tin = $2`, f.tenantID, tin); n != 1 {
			t.Errorf("round %d: records for %s = %d, want 1", round, tin, n)
		}
	}
}

// cfWaitForLockWait polls until an INSERT INTO customers waits on a lock, or done fires.
func cfWaitForLockWait(super *pgxpool.Pool, done <-chan struct{}) bool {
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

func TestFill_RacingCorrectionKeepsTheMembersValue(t *testing.T) {
	f := newNRSFixture(t, "CF-14")
	tin := cfTIN
	if _, err := f.super.Exec(context.Background(),
		`INSERT INTO customers (tenant_id, tin, name, email) VALUES ($1, $2, 'Race Buyer', 'old@x.ng')`, f.tenantID, tin); err != nil {
		t.Fatalf("seed the record: %v", err)
	}

	held, release := make(chan struct{}), make(chan struct{})
	aDone := make(chan error, 1)
	go func() {
		aDone <- db.WithinRequestTenantTx(f.c, f.app, func(tx pgx.Tx) error {
			if _, err := tx.Exec(f.c,
				`UPDATE customers SET email = 'fixed@x.ng',
				        edited_fields = ARRAY(SELECT DISTINCT unnest(edited_fields || '{email}'::text[])) WHERE tin = $1`, tin); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-aDone:
		t.Fatalf("correction tx ended before holding the row: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("correction tx never held the row")
	}

	bDone := make(chan error, 1)
	bFinished := make(chan struct{})
	go func() {
		_, err := f.store.Create(f.c, CreateInput{
			EntityID: f.entityID, InvoiceNumber: "CF-14-B", BuyerTIN: &tin,
			BuyerEmail: strPtr("new@x.ng"), BuyerCity: strPtr("Abuja"),
		})
		bDone <- err
		close(bFinished)
	}()
	blocked := cfWaitForLockWait(f.super, bFinished)
	close(release)
	if err := <-aDone; err != nil {
		t.Fatalf("correction tx: %v", err)
	}
	if err := <-bDone; err != nil {
		t.Fatalf("racing Create: %v", err)
	}

	if !blocked {
		t.Error("the fill never waited on the corrector's row lock: the race was not exercised")
	}
	r := cfMust(t, f.super, f.tenantID, tin)
	if cfV(r.City) != "Abuja" {
		t.Errorf("control: city = %s, want Abuja (the fill ran)", cfV(r.City))
	}
	if cfV(r.Email) != "fixed@x.ng" {
		t.Errorf("email = %s, want fixed@x.ng (the member's value survives the race)", cfV(r.Email))
	}
}

// cfField is one directory field a save fills: how the invoice carries it, the member's
// correction input, and where the record holds it.
type cfField struct {
	col, a, b, fixed string
	create           func(*CreateInput, *string)
	edit             func(*UpdateInput, *string)
	fix              func(*customer.UpdateInput, *string)
	got              func(cfRec) *string
}

var cfFields = []cfField{
	{"name", "Name A", "Name B", "Name Fixed",
		func(c *CreateInput, v *string) { c.BuyerName = v }, func(u *UpdateInput, v *string) { u.BuyerName = v },
		func(u *customer.UpdateInput, v *string) { u.Name = v }, func(r cfRec) *string { return r.Name }},
	{"buyer_type", "B2B", "B2G", "B2C",
		func(c *CreateInput, v *string) { c.InvoiceKind = v }, func(u *UpdateInput, v *string) { u.InvoiceKind = v },
		func(u *customer.UpdateInput, v *string) { u.BuyerType = v }, func(r cfRec) *string { return r.BuyerType }},
	{"email", "a@x.ng", "b@x.ng", "fixed@x.ng",
		func(c *CreateInput, v *string) { c.BuyerEmail = v }, func(u *UpdateInput, v *string) { u.BuyerEmail = v },
		func(u *customer.UpdateInput, v *string) { u.Email = v }, func(r cfRec) *string { return r.Email }},
	{"telephone", "0801", "0802", "0803",
		func(c *CreateInput, v *string) { c.BuyerTelephone = v }, func(u *UpdateInput, v *string) { u.BuyerTelephone = v },
		func(u *customer.UpdateInput, v *string) { u.Telephone = v }, func(r cfRec) *string { return r.Telephone }},
	{"street", "1 A St", "2 B St", "3 C St",
		func(c *CreateInput, v *string) { c.BuyerStreet = v }, func(u *UpdateInput, v *string) { u.BuyerStreet = v },
		func(u *customer.UpdateInput, v *string) { u.Street = v }, func(r cfRec) *string { return r.Street }},
	{"city", "Aba", "Bida", "Calabar",
		func(c *CreateInput, v *string) { c.BuyerCity = v }, func(u *UpdateInput, v *string) { u.BuyerCity = v },
		func(u *customer.UpdateInput, v *string) { u.City = v }, func(r cfRec) *string { return r.City }},
	{"postal_zone", "100001", "200002", "300003",
		func(c *CreateInput, v *string) { c.BuyerPostalZone = v }, func(u *UpdateInput, v *string) { u.BuyerPostalZone = v },
		func(u *customer.UpdateInput, v *string) { u.PostalZone = v }, func(r cfRec) *string { return r.PostalZone }},
	{"country", "NG", "GH", "KE",
		func(c *CreateInput, v *string) { c.BuyerCountry = v }, func(u *UpdateInput, v *string) { u.BuyerCountry = v },
		func(u *customer.UpdateInput, v *string) { u.Country = v }, func(r cfRec) *string { return r.Country }},
	{"state", "NG-LA", "NG-FC", "NG-KN",
		func(c *CreateInput, v *string) { c.BuyerState = v }, func(u *UpdateInput, v *string) { u.BuyerState = v },
		func(u *customer.UpdateInput, v *string) { u.State = v }, func(r cfRec) *string { return r.State }},
	{"lga", "NG-LA-001", "NG-LA-002", "NG-LA-003",
		func(c *CreateInput, v *string) { c.BuyerLGA = v }, func(u *UpdateInput, v *string) { u.BuyerLGA = v },
		func(u *customer.UpdateInput, v *string) { u.LGA = v }, func(r cfRec) *string { return r.LGA }},
}

func cfCreateWith(tin string, val func(cfField) *string) CreateInput {
	in := CreateInput{BuyerTIN: &tin}
	for _, fd := range cfFields {
		fd.create(&in, val(fd))
	}
	return in
}

func cfEditWith(val func(cfField) *string) UpdateInput {
	var in UpdateInput
	for _, fd := range cfFields {
		fd.edit(&in, val(fd))
	}
	return in
}

func cfExpect(t *testing.T, r cfRec, step string, want func(cfField) string) {
	t.Helper()
	for _, fd := range cfFields {
		if got := cfV(fd.got(r)); got != want(fd) {
			t.Errorf("%s: %s = %q, want %q", step, fd.col, got, want(fd))
		}
	}
}

func TestFill_EveryFieldIsWrittenKeptOnBlankAndTrimmed(t *testing.T) {
	f := newNRSFixture(t, "CF-16")
	if len(cfFields) != 10 {
		t.Fatalf("cfFields = %d entries, want the 10 directory fields", len(cfFields))
	}
	a := func(fd cfField) string { return fd.a }
	b := func(fd cfField) string { return fd.b }

	f.create(t, "CF-16-1", cfCreateWith(cfTIN, func(fd cfField) *string { return &fd.a }))
	cfExpect(t, cfMust(t, f.super, f.tenantID, cfTIN), "first save", a)
	f.create(t, "CF-16-2", cfCreateWith(cfTIN, func(fd cfField) *string { return &fd.b }))
	cfExpect(t, cfMust(t, f.super, f.tenantID, cfTIN), "second save", b)

	f.create(t, "CF-16-3", cfCreateWith(cfTIN, func(cfField) *string { return strPtr("   ") }))
	cfExpect(t, cfMust(t, f.super, f.tenantID, cfTIN), "all-blank save", b)
	f.create(t, "CF-16-4", cfCreateWith(cfTIN, func(cfField) *string { return nil }))
	cfExpect(t, cfMust(t, f.super, f.tenantID, cfTIN), "all-NULL save", b)

	inv := f.create(t, "CF-16-5", cfCreateWith(cfTIN, func(fd cfField) *string { return strPtr(" " + fd.a + " ") }))
	cfExpect(t, cfMust(t, f.super, f.tenantID, cfTIN), "padded save", a)

	cfEdit(t, f, inv.ID, cfEditWith(func(fd cfField) *string { return strPtr(fd.b) }))
	cfExpect(t, cfMust(t, f.super, f.tenantID, cfTIN), "edit to b", b)
	cfEdit(t, f, inv.ID, cfEditWith(func(cfField) *string { return ClearText }))
	cfExpect(t, cfMust(t, f.super, f.tenantID, cfTIN), "edit clearing every column", b)

	if n := cfCount(t, f.super, f.tenantID); n != 1 {
		t.Errorf("customers rows = %d, want 1", n)
	}
}

func TestFill_ACorrectedFieldIsKeptWhileItsSiblingsFill(t *testing.T) {
	f := newNRSFixture(t, "CF-17")
	if len(cfFields) != 10 {
		t.Fatalf("cfFields = %d entries, want the 10 directory fields", len(cfFields))
	}
	for i, corrected := range cfFields {
		t.Run(corrected.col, func(t *testing.T) {
			tin := fmt.Sprintf("%08d-0042", 50000000+i)
			inv1 := f.create(t, fmt.Sprintf("CF-17-%d-A", i), cfCreateWith(tin, func(fd cfField) *string { return &fd.a }))
			var fix customer.UpdateInput
			corrected.fix(&fix, &corrected.fixed)
			cfCorrect(t, f, cfMust(t, f.super, f.tenantID, tin).ID, fix)

			want := func(own string) func(cfField) string {
				return func(fd cfField) string {
					if fd.col == corrected.col {
						return corrected.fixed
					}
					if own == "a" {
						return fd.a
					}
					return fd.b
				}
			}
			f.create(t, fmt.Sprintf("CF-17-%d-B", i), cfCreateWith(tin, func(fd cfField) *string { return &fd.b }))
			r := cfMust(t, f.super, f.tenantID, tin)
			cfExpect(t, r, "later save", want("b"))
			if len(r.Edited) != 1 || r.Edited[0] != corrected.col {
				t.Errorf("edited_fields = %v, want [%s]", r.Edited, corrected.col)
			}

			cfEdit(t, f, inv1.ID, cfEditWith(func(fd cfField) *string { return strPtr(fd.a) }))
			r = cfMust(t, f.super, f.tenantID, tin)
			cfExpect(t, r, "re-saved older invoice", want("a"))
			if len(r.Edited) != 1 || r.Edited[0] != corrected.col {
				t.Errorf("edited_fields after the re-save = %v, want [%s]", r.Edited, corrected.col)
			}
		})
	}
}

func TestFill_ACorrectionToBlankStaysBlank(t *testing.T) {
	f := newNRSFixture(t, "CF-18")
	f.create(t, "CF-18-A", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerEmail: strPtr("a@x.ng"), BuyerCity: strPtr("Aba")})
	cfCorrect(t, f, cfMust(t, f.super, f.tenantID, cfTIN).ID, customer.UpdateInput{Email: strPtr("")})
	f.create(t, "CF-18-B", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerEmail: strPtr("b@x.ng"), BuyerCity: strPtr("Bida")})
	r := cfMust(t, f.super, f.tenantID, cfTIN)
	if cfV(r.City) != "Bida" {
		t.Errorf("control: city = %s, want Bida", cfV(r.City))
	}
	if r.Email != nil {
		t.Errorf("email = %s, want NULL (the member cleared it)", cfV(r.Email))
	}
}

func TestFill_EditOfEachBuyerColumnFillsTheRecord(t *testing.T) {
	f := newNRSFixture(t, "CF-19")
	cols := []struct {
		col  string
		edit func(*UpdateInput, string)
		got  func(cfRec) *string
	}{{"buyer_tin", func(u *UpdateInput, v string) { u.BuyerTIN = &v }, nil}}
	for _, fd := range cfFields {
		cols = append(cols, struct {
			col  string
			edit func(*UpdateInput, string)
			got  func(cfRec) *string
		}{fd.col, func(u *UpdateInput, v string) { fd.edit(u, &v) }, fd.got})
	}
	if len(cols) != 11 {
		t.Fatalf("columns = %d, want the 11 buyer columns of the fill", len(cols))
	}
	for i, c := range cols {
		t.Run(c.col, func(t *testing.T) {
			tin := fmt.Sprintf("%08d-0042", 51000000+i)
			inv1 := f.create(t, fmt.Sprintf("CF-19-%d-A", i), cfCreateWith(tin, func(fd cfField) *string { return &fd.a }))
			f.create(t, fmt.Sprintf("CF-19-%d-B", i), cfCreateWith(tin, func(fd cfField) *string { return &fd.b }))
			if c.got == nil {
				moved := fmt.Sprintf("%08d-0042", 52000000+i)
				var in UpdateInput
				c.edit(&in, moved)
				cfEdit(t, f, inv1.ID, in)
				if r := cfMust(t, f.super, f.tenantID, moved); cfV(r.Email) != "a@x.ng" {
					t.Errorf("record for the new TIN: email = %s, want a@x.ng", cfV(r.Email))
				}
				return
			}
			want := "edited-" + c.col
			if c.col == "buyer_type" {
				want = "B2C"
			}
			var in UpdateInput
			c.edit(&in, want)
			cfEdit(t, f, inv1.ID, in)
			if got := cfV(c.got(cfMust(t, f.super, f.tenantID, tin))); got != want {
				t.Errorf("%s after an edit setting only it = %q, want %q", c.col, got, want)
			}
		})
	}
}

// Pinned: a re-saved older invoice writes its older values into every uncorrected field (D4 b).
func TestFill_AReSavedOlderInvoiceWritesItsOlderValuesIntoUncorrectedFields(t *testing.T) {
	f := newNRSFixture(t, "CF-20")
	inv1 := f.create(t, "CF-20-A", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerEmail: strPtr("old@x.ng"), BuyerCity: strPtr("Aba")})
	f.create(t, "CF-20-B", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerEmail: strPtr("new@x.ng"), BuyerCity: strPtr("Bida")})
	cfEdit(t, f, inv1.ID, UpdateInput{BuyerTelephone: strPtr("0801")})
	r := cfMust(t, f.super, f.tenantID, cfTIN)
	if cfV(r.Telephone) != "0801" {
		t.Fatalf("control: telephone = %s, want 0801", cfV(r.Telephone))
	}
	if cfV(r.Email) != "old@x.ng" || cfV(r.City) != "Aba" {
		t.Errorf("email/city = %s/%s, want old@x.ng/Aba (the older invoice's values win, D4 b)", cfV(r.Email), cfV(r.City))
	}
}

func TestFill_SaveNeverWritesTheMembersOnlyColumns(t *testing.T) {
	f := newNRSFixture(t, "CF-21")
	inv1 := f.create(t, "CF-21-A", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerName: strPtr("Before")})
	before := cfMust(t, f.super, f.tenantID, cfTIN)
	cfCorrect(t, f, before.ID, customer.UpdateInput{ContactPerson: strPtr("Ada"), Email: strPtr("fixed@x.ng")})
	past := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := f.super.Exec(context.Background(), `UPDATE customers SET created_at = $1 WHERE id = $2`, past, before.ID); err != nil {
		t.Fatalf("pin created_at: %v", err)
	}
	f.create(t, "CF-21-B", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerName: strPtr("After"), BuyerEmail: strPtr("new@x.ng")})
	cfEdit(t, f, inv1.ID, UpdateInput{BuyerName: strPtr("After Edit")})

	var contact *string
	var created time.Time
	var edited []string
	var id string
	if err := f.super.QueryRow(context.Background(),
		`SELECT id, contact_person, created_at, edited_fields FROM customers WHERE tenant_id = $1 AND tin = $2`,
		f.tenantID, cfTIN).Scan(&id, &contact, &created, &edited); err != nil {
		t.Fatalf("read record: %v", err)
	}
	if r := cfMust(t, f.super, f.tenantID, cfTIN); cfV(r.Name) != "After Edit" {
		t.Fatalf("control: name = %s, want After Edit (the saves filled)", cfV(r.Name))
	}
	if id != before.ID {
		t.Errorf("record id = %s, want %s", id, before.ID)
	}
	if cfV(contact) != "Ada" {
		t.Errorf("contact_person = %s, want Ada", cfV(contact))
	}
	if !created.Equal(past) {
		t.Errorf("created_at = %v, want %v", created, past)
	}
	if strings.Join(edited, ",") != "contact_person,email" {
		t.Errorf("edited_fields = %v, want [contact_person email]", edited)
	}
}

func TestFill_EditToAnUnfileableTINSavesAndLeavesTheRecords(t *testing.T) {
	f := newNRSFixture(t, "CF-22")
	inv := f.create(t, "CF-22-A", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerName: strPtr("Keep Ltd")})
	for _, bad := range []*string{strPtr("1234567"), ClearText} {
		cfEdit(t, f, inv.ID, UpdateInput{BuyerTIN: bad, BuyerName: strPtr("Renamed Ltd")})
	}
	if n := cfCount(t, f.super, f.tenantID); n != 1 {
		t.Errorf("customers rows = %d, want 1", n)
	}
	if r := cfMust(t, f.super, f.tenantID, cfTIN); cfV(r.Name) != "Keep Ltd" {
		t.Errorf("record name = %s, want Keep Ltd (an unfileable edit fills nothing)", cfV(r.Name))
	}
	if got := f.get(t, inv.ID); cfV(got.BuyerName) != "Renamed Ltd" {
		t.Errorf("invoice buyer_name = %s, want Renamed Ltd (the edit saved)", cfV(got.BuyerName))
	}
}

func TestFill_AFailureAfterTheFillRollsTheRecordBack(t *testing.T) {
	f := newNRSFixture(t, "CF-23")
	bad := LineItemInput{Description: strPtr("Bad"), Quantity: strPtr("abc"), UnitPrice: strPtr("1"), LineTotal: strPtr("1"), LineTax: strPtr("0")}

	freshTIN := "12345678-0077"
	_, err := f.store.Create(f.c, CreateInput{EntityID: f.entityID, InvoiceNumber: "CF-23-BAD", BuyerTIN: &freshTIN,
		BuyerName: strPtr("Ghost Ltd"), LineItems: []LineItemInput{bad}})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Create with a bad line err = %v, want ErrValidation", err)
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE tenant_id = $1 AND invoice_number = 'CF-23-BAD'`, f.tenantID); n != 0 {
		t.Errorf("invoices stored = %d, want 0", n)
	}
	if n := cfCount(t, f.super, f.tenantID); n != 0 {
		t.Errorf("customers rows after the failed Create = %d, want 0 (the fill rolled back)", n)
	}

	inv := f.create(t, "CF-23-OK", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerEmail: strPtr("a@x.ng")})
	if _, err := f.store.Edit(f.c, inv.ID, EditInput{
		UpdateInput: UpdateInput{BuyerEmail: strPtr("new@x.ng")}, LineItems: &[]LineItemInput{bad},
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Edit with a bad line err = %v, want ErrValidation", err)
	}
	if r := cfMust(t, f.super, f.tenantID, cfTIN); cfV(r.Email) != "a@x.ng" {
		t.Errorf("record email after the failed Edit = %s, want a@x.ng (the fill rolled back)", cfV(r.Email))
	}
	if got := cfV(f.get(t, inv.ID).BuyerEmail); got != "a@x.ng" {
		t.Errorf("invoice email after the failed Edit = %s, want a@x.ng", got)
	}
}

func TestFill_ConcurrentSavesForOneTINDoNotDeadlockOrDuplicate(t *testing.T) {
	f := newNRSFixture(t, "CF-24")
	const home = "12345678-0100"
	var seeds []Invoice
	for i := 0; i < 3; i++ {
		seeds = append(seeds, f.create(t, fmt.Sprintf("CF-24-S%d", i), CreateInput{BuyerTIN: strPtr(home), BuyerCity: strPtr("Aba")}))
	}
	homeID := cfMust(t, f.super, f.tenantID, home).ID

	for round := 0; round < 6; round++ {
		tin := fmt.Sprintf("%08d-0042", 60000000+round)
		start := make(chan struct{})
		var jobs []func() error
		for g := 0; g < 8; g++ {
			jobs = append(jobs, func() error {
				_, err := f.store.Create(f.c, CreateInput{EntityID: f.entityID, InvoiceNumber: fmt.Sprintf("CF-24-%d-%d", round, g),
					BuyerTIN: &tin, BuyerName: strPtr(fmt.Sprintf("Racer %d", g)), BuyerCity: strPtr("Bida")})
				return err
			})
		}
		for i, s := range seeds {
			jobs = append(jobs, func() error {
				_, err := f.store.Edit(f.c, s.ID, EditInput{UpdateInput: UpdateInput{BuyerTIN: &tin, BuyerEmail: strPtr(fmt.Sprintf("r%d-%d@x.ng", round, i))}})
				if err == nil {
					_, err = f.store.Edit(f.c, s.ID, EditInput{UpdateInput: UpdateInput{BuyerTIN: strPtr(home), BuyerCity: strPtr("Aba")}})
				}
				return err
			})
		}
		jobs = append(jobs, func() error {
			_, err := customer.NewStore(f.app).Update(f.c, homeID, customer.UpdateInput{City: strPtr("Fixed")})
			return err
		})
		errs := make(chan error, len(jobs))
		for _, j := range jobs {
			go func() { <-start; errs <- j() }()
		}
		close(start)
		for range jobs {
			if err := <-errs; err != nil {
				t.Errorf("round %d: %v", round, err)
			}
		}
		if n := mustCount(t, f.super, `SELECT count(*) FROM customers WHERE tenant_id = $1 AND tin = $2`, f.tenantID, tin); n != 1 {
			t.Errorf("round %d: records for %s = %d, want 1", round, tin, n)
		}
		if got := cfV(cfMust(t, f.super, f.tenantID, home).City); got != "Fixed" {
			t.Errorf("round %d: home city = %s, want Fixed (the member's correction survives the fills)", round, got)
		}
	}
}

func TestFill_ArchivedRecordStillFillsAndStaysArchived(t *testing.T) {
	f := newNRSFixture(t, "CF-ARCH")
	f.create(t, "CF-AR-A", CreateInput{BuyerTIN: strPtr(cfTIN)})
	id := cfMust(t, f.super, f.tenantID, cfTIN).ID
	cfCorrect(t, f, id, customer.UpdateInput{Email: strPtr("fixed@x.ng")})
	if _, err := customer.NewStore(f.app).SetStatus(f.c, id, customer.StatusArchived); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	f.create(t, "CF-AR-B", CreateInput{BuyerTIN: strPtr(cfTIN), BuyerCity: strPtr("Abuja"), BuyerEmail: strPtr("new@x.ng")})

	r := cfMust(t, f.super, f.tenantID, cfTIN)
	if cfV(r.City) != "Abuja" {
		t.Errorf("city = %s, want Abuja (an unedited field fills)", cfV(r.City))
	}
	if cfV(r.Email) != "fixed@x.ng" {
		t.Errorf("email = %s, want fixed@x.ng (the edited field is kept)", cfV(r.Email))
	}
	if st := mustCount(t, f.super, `SELECT count(*) FROM customers WHERE id = $1 AND status = 'archived'`, id); st != 1 {
		t.Errorf("archived rows for the record = %d, want 1 (stays archived)", st)
	}
	if n := cfCount(t, f.super, f.tenantID); n != 1 {
		t.Errorf("customers rows = %d, want 1", n)
	}
}
