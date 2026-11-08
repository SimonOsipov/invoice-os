package publiccheck_test

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/aicalls"
	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
	"github.com/SimonOsipov/invoice-os/internal/publiccheck"
	"github.com/SimonOsipov/invoice-os/internal/validation"
)

// dbChecker skips without DB env, like internal/aicalls.
func dbChecker(t *testing.T) *publiccheck.Checker {
	t.Helper()
	appURL := os.Getenv("DATABASE_URL")
	if appURL == "" || os.Getenv("DATABASE_SUPERUSER_URL") == "" {
		t.Skip("publiccheck db test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
	}
	pool, err := pgxpool.New(context.Background(), appURL)
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return &publiccheck.Checker{
		LoadRules: validation.NewStore(pool).LoadForBatch,
		Engine:    validation.NewDefaultEngine(),
		Readers:   extraction.CheckReaders{},
		Now:       fixedNow,
	}
}

// seedCodeLists adds one code to each list the v5 rules name, so the 2027 rule set loads on a bare database.
func seedCodeLists(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	super, err := pgxpool.New(ctx, os.Getenv("DATABASE_SUPERUSER_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(super.Close)
	codes := map[string]string{
		"currencies": "NGN", "countries": "NG", "states": "NG-LA", "lgas": "NG-LA-AGE",
		"invoice-quantity-codes": "EA", "hs-codes": "8471.30", "services-codes": "6201", "tax-categories": "STANDARD_VAT",
	}
	for list, code := range codes {
		tag, err := super.Exec(ctx,
			`INSERT INTO nrs_codes (list, code, entries) VALUES ($1, $2, '[{}]') ON CONFLICT DO NOTHING`, list, code)
		if err != nil {
			t.Fatalf("seed nrs_codes %s: %v", list, err)
		}
		if tag.RowsAffected() == 1 {
			t.Cleanup(func() {
				_, _ = super.Exec(context.Background(), `DELETE FROM nrs_codes WHERE list = $1 AND code = $2`, list, code)
			})
		}
	}
}

func ruleKeys(inv publiccheck.Invoice) []string {
	var out []string
	for _, f := range inv.Failures {
		out = append(out, f.RuleKey)
	}
	return out
}

func TestCheckDB_MissingSupplierFailsBothSupplierRules(t *testing.T) {
	c := dbChecker(t)
	ctx := context.Background()

	res, err := c.Check(ctx, csvReq("Inv,Total,Date\nA,1,2026-11-01\n", "issue_date", "Date"))
	if err != nil {
		t.Fatal(err)
	}
	keys := ruleKeys(res.Invoices[0])
	for _, want := range []string{"supplier-name-required", "supplier-tin-required"} {
		if !slices.Contains(keys, want) {
			t.Errorf("no supplier: failures %v lack %s", keys, want)
		}
	}

	res, err = c.Check(ctx, csvReq("Inv,Total,Date,Seller,TIN\nA,1,2026-11-01,Acme Ltd,123456780001\n",
		"issue_date", "Date", "supplier_name", "Seller", "supplier_tin", "TIN"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range ruleKeys(res.Invoices[0]) {
		if k == "supplier-name-required" || k == "supplier-tin-required" || k == "supplier-tin-format" {
			t.Errorf("filled supplier still fails %s", k)
		}
	}
}

func TestCheckDB_CompanyProfileRulesAreNotCheckedFrom2027(t *testing.T) {
	c := dbChecker(t)
	seedCodeLists(t)
	ctx := context.Background()
	const csv = "Inv,Total,Date,Seller,TIN\nA,1,%s,Acme Ltd,123456780001\n"
	mk := func(date string) publiccheck.Request {
		return csvReq(strings.Replace(csv, "%s", date, 1),
			"issue_date", "Date", "supplier_name", "Seller", "supplier_tin", "TIN")
	}

	res, err := c.Check(ctx, mk("2027-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range ruleKeys(res.Invoices[0]) {
		if strings.HasPrefix(k, "supplier-") {
			t.Errorf("failure %s on a supplier rule, want none", k)
		}
	}
	want := []string{"supplier-city-required", "supplier-country-allowed", "supplier-country-required",
		"supplier-email-length", "supplier-email-required", "supplier-lga-allowed", "supplier-lga-required",
		"supplier-postal-zone-required", "supplier-state-allowed", "supplier-state-required", "supplier-street-required"}
	if !slices.Equal(res.NotChecked, want) {
		t.Errorf("NotChecked = %v, want %v", res.NotChecked, want)
	}

	res, err = c.Check(ctx, mk("2026-11-01"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NotChecked) != 0 {
		t.Errorf("2026 NotChecked = %v, want none", res.NotChecked)
	}
}

// countingAI counts the calls the check really makes through the fake client.
type countingAI struct {
	*ai.Client
	calls int
}

func (c *countingAI) Call(ctx context.Context, req ai.Request) (map[string]any, error) {
	c.calls++
	return c.Client.Call(ctx, req)
}

var storedTables = []string{"documents", "extraction_jobs", "extraction_field_results",
	"extraction_page_images", "import_batches", "invoices", "river_job"}

func TestCheckDB_StoresNoDocumentExtractionInvoiceOrJobRow(t *testing.T) {
	c := dbChecker(t)
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	ctx := context.Background()
	super, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("superuser pool: %v", err)
	}
	t.Cleanup(super.Close)

	count := func(q string) int {
		t.Helper()
		var n int
		if err := super.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	counts := func() []int {
		out := make([]int, len(storedTables))
		for i, tbl := range storedTables {
			out[i] = count("SELECT count(*) FROM " + tbl)
		}
		return out
	}
	const nullCalls = "SELECT count(*) FROM ai_calls WHERE tenant_id IS NULL"

	var start time.Time
	if err := super.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := super.Exec(ctx, "DELETE FROM ai_calls WHERE id IN (SELECT id FROM ai_calls WHERE tenant_id IS NULL AND created_at >= $1)", start); err != nil {
			t.Errorf("delete NULL-tenant ai_calls: %v", err)
		}
	})

	t.Setenv(ai.EnvFake, "1")
	fake, err := ai.FromEnv(slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	app, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	rec := aicalls.NewRecorder(app, slog.New(slog.DiscardHandler))
	fake.SetRecorder(rec.Record)
	counter := &countingAI{Client: fake}
	c.Readers = extraction.CheckReaders{Text: &pdfText{chars: 20}, AI: counter}

	before, callsBefore := counts(), count(nullCalls)

	if _, err := c.Check(ctx, csvReq("Inv,Total\nA,1\nB,2\n")); err != nil {
		t.Fatalf("csv check: %v", err)
	}
	if counter.calls != 0 {
		t.Fatalf("spreadsheet check made %d AI calls, want 0", counter.calls)
	}
	if _, err := c.Check(ctx, publiccheck.Request{File: []byte("%PDF"), Format: "pdf"}); err != nil {
		t.Fatalf("pdf check: %v", err)
	}
	rec.Wait()

	if counter.calls < 1 {
		t.Fatalf("document check made %d AI calls, want >= 1", counter.calls)
	}
	after := counts()
	for i, tbl := range storedTables {
		if after[i] != before[i] {
			t.Errorf("%s rows = %d, want %d", tbl, after[i], before[i])
		}
	}
	if got, want := count(nullCalls), callsBefore+counter.calls; got != want {
		t.Errorf("NULL-tenant ai_calls = %d, want %d (control: the counting query sees the check's writes)", got, want)
	}
}
