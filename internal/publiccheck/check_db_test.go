package publiccheck_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
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
