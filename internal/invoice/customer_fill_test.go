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
	for i, tin := range []*string{strPtr("1234567"), strPtr("rn-12"), strPtr(""), nil} {
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
	if _, err := f.store.Edit(f.c, ok.ID, EditInput{UpdateInput: UpdateInput{BuyerTIN: &failTIN}}); err == nil {
		t.Error("Edit to a failing TIN succeeded, want the fill error")
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
