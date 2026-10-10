// ENGI-09-02 (Test-first) -- rule-set v5, dated 2027-01-01. Authored before
// migrations/<ts>_rule_set_v5.sql exists: each test fails at "v5 is missing", never on a compile error.
// Version-boundary tests use fixed dates only, so none reads today's date.
package validation

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	v5BuyerGuard    = `has(invoice.invoice_kind) && invoice.invoice_kind in ['B2B', 'B2G']`
	v5TINPattern    = `^(?:[0-9]{8}-[0-9]{4}|RN-[0-9]+)$`
	v5MigrationGlob = "*_rule_set_v5.sql"
)

// v5 seeds the code lists and returns the rule set in force on v5's start date.
func v5RuleSet(t *testing.T) RuleSet {
	t.Helper()
	super, app := dbTestPools(t)
	seedV5Lists(t, super)
	got, err := NewStore(app).LoadForDates(context.Background(), []string{v5Start})
	if err != nil {
		t.Fatalf("LoadForDates(%s): %v", v5Start, err)
	}
	rs := got[v5Start]
	if rs.Version != 5 {
		t.Fatalf("rule set in force on %s is v%d, want v5 -- rule_set_v5 migration is not applied", v5Start, rs.Version)
	}
	return rs
}

func evalV5(t *testing.T, rs RuleSet, p Payload) Result {
	t.Helper()
	res, err := NewDefaultEngine().Evaluate(p, rs)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return res
}

// wantKeys asserts the violations are exactly the given rule keys (sorted, one entry per violation).
func wantKeys(t *testing.T, what string, res Result, keys ...string) {
	t.Helper()
	got := violationKeys(res)
	if len(keys) == 0 && len(got) == 0 {
		return
	}
	if !reflect.DeepEqual(got, keys) {
		t.Errorf("%s: violations = %s, want exactly %v", what, showViolations(res), keys)
	}
}

func showViolations(res Result) string {
	out := []string{}
	for _, v := range res.Violations {
		out = append(out, v.RuleKey+"@"+v.Path+" "+ptrStr(v.Expected)+"/"+ptrStr(v.Actual))
	}
	return "[" + strings.Join(out, "; ") + "]"
}

func violationOf(t *testing.T, res Result, key string) Violation {
	t.Helper()
	for _, v := range res.Violations {
		if v.RuleKey == key {
			return v
		}
	}
	t.Fatalf("no %s violation in %s", key, showViolations(res))
	return Violation{}
}

func wantAt(t *testing.T, v Violation, path, expected, actual string) {
	t.Helper()
	if v.Path != path {
		t.Errorf("%s Path = %q, want %q", v.RuleKey, v.Path, path)
	}
	if v.Expected == nil || *v.Expected != expected || v.Actual == nil || *v.Actual != actual {
		t.Errorf("%s Expected/Actual = %s/%s, want %q/%q", v.RuleKey, ptrStr(v.Expected), ptrStr(v.Actual), expected, actual)
	}
}

func versionOnDate(t *testing.T, date string) int {
	t.Helper()
	_, app := dbTestPools(t)
	var v *int
	if err := app.QueryRow(context.Background(),
		`SELECT v.version FROM rule_set_versions v WHERE v.id = rule_set_version_for($1::date)`, date).Scan(&v); err != nil || v == nil {
		t.Fatalf("version in force on %s: %v (%v)", date, err, v)
	}
	return *v
}

func TestV5_IsScheduledAndSealed(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	var sealed bool
	var from, notes *string
	if err := app.QueryRow(ctx,
		`SELECT sealed, effective_from::text, notes FROM rule_set_versions WHERE version = 5`).Scan(&sealed, &from, &notes); err != nil {
		t.Fatalf("read v5: %v -- rule_set_v5 migration is not applied", err)
	}
	if !sealed {
		t.Error("v5.sealed = false, want true")
	}
	if from == nil || *from != v5Start {
		t.Errorf("v5.effective_from = %v, want %s", from, v5Start)
	}
	if notes == nil || !strings.HasPrefix(*notes, "MBS global rule-set v5") {
		t.Errorf("v5.notes = %v, want prefix %q", notes, "MBS global rule-set v5")
	}

	count := func(version int) int {
		var n int
		if err := app.QueryRow(ctx,
			`SELECT count(*) FROM rule_set_version_rechecks m JOIN rule_set_versions v ON v.id = m.rule_set_version_id
			  WHERE v.version = $1`, version).Scan(&n); err != nil {
			t.Fatalf("count rechecks of v%d: %v", version, err)
		}
		return n
	}
	if n := count(4); n != 1 {
		t.Fatalf("v4 recheck markers = %d, want 1 (the probe below would assert nothing)", n)
	}
	if n := count(5); n != 0 {
		t.Errorf("v5 recheck markers = %d, want 0 (the re-check writes it on %s)", n, v5Start)
	}
}

func TestV5_VersionForBoundary(t *testing.T) {
	for date, want := range map[string]int{"2026-12-31": 4, "2027-01-01": 5, "2027-06-30": 5} {
		if got := versionOnDate(t, date); got != want {
			t.Errorf("rule_set_version_for(%s) = v%d, want v%d", date, got, want)
		}
	}
}

var v5CarriedKeys = []string{
	"supplier-tin-required", "supplier-name-required", "invoice-number-required", "issue-date-required",
	"currency-required", "subtotal-required", "subtotal-non-negative", "vat-required", "vat-non-negative",
	"total-required", "total-non-negative", "line-items-required", "line-items-sum-subtotal",
	"line-cost-non-negative", "no-duplicate-line-items",
}

func TestV5_CarriesTheUnchangedV4RulesVerbatim(t *testing.T) {
	_, app := dbTestPools(t)
	v5 := ruleRowsByKey(t, app, 5)
	v4 := ruleRowsByKey(t, app, 4)
	if len(v5CarriedKeys) != 15 {
		t.Fatalf("carried key list holds %d keys, want 15", len(v5CarriedKeys))
	}
	for _, key := range v5CarriedKeys {
		want, ok := v4[key]
		if !ok {
			t.Errorf("%s is not a v4 rule", key)
			continue
		}
		got, ok := v5[key]
		if !ok {
			t.Errorf("%s missing from v5", key)
			continue
		}
		want.Enabled = true
		if !reflect.DeepEqual(got, want) {
			t.Errorf("v5 %s = %+v, want v4's row with enabled=true: %+v", key, got, want)
		}
	}
}

func TestV5_WhenGuards(t *testing.T) {
	_, app := dbTestPools(t)
	rows := ruleRowsByKey(t, app, 5)

	want := map[string]string{"vat-standard-rate-uncategorised": `!has(invoice.tax_subtotals)`}
	for _, f := range []string{"tin", "name", "email", "street", "city", "postal-zone", "lga", "state", "country"} {
		want["buyer-"+f+"-required"] = v5BuyerGuard
	}
	got := map[string]string{}
	for key, r := range rows {
		if r.When != nil {
			got[key] = *r.When
		}
	}
	if len(want) != 10 {
		t.Fatalf("expected guard table holds %d rules, want 10", len(want))
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("v5 rules with a when guard = %v, want exactly %v", got, want)
	}
}

func TestV5_V4IsUnchanged(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()
	loadRuleSetByVersion(t, app, 5) // precondition: "unchanged by the v5 publish" needs v5 to exist

	rows := ruleRowsByKey(t, app, 4)
	if len(rows) != 20 {
		t.Errorf("v4 holds %d rules, want 20", len(rows))
	}
	for key, r := range rows {
		if r.When != nil {
			t.Errorf("v4 %s carries a when guard %q, want none", key, *r.When)
		}
	}
	jsonIs := func(key, wantType, wantTarget, wantParams string) {
		t.Helper()
		r, ok := rows[key]
		if !ok {
			t.Errorf("v4 is missing %s", key)
			return
		}
		var got, want any
		if err := json.Unmarshal([]byte(r.Params), &got); err != nil {
			t.Fatalf("%s params %q: %v", key, r.Params, err)
		}
		if err := json.Unmarshal([]byte(wantParams), &want); err != nil {
			t.Fatal(err)
		}
		if r.Type != wantType || r.Target != wantTarget || !reflect.DeepEqual(got, want) {
			t.Errorf("v4 %s = %s %s %s, want %s %s %s", key, r.Type, r.Target, r.Params, wantType, wantTarget, wantParams)
		}
	}
	jsonIs("currency-allowed", "enum", "currency", `{"values":["NGN"]}`)
	jsonIs("vat-standard-rate", "tax_math", "vat", `{"base":"subtotal","rate":0.075,"expected":"vat","tolerance":0.005}`)
	jsonIs("supplier-tin-format", "format/regex", "supplier.tin", `{"pattern":"^[0-9]{8}-[0-9]{4}$"}`)
	jsonIs("buyer-tin-format", "format/regex", "buyer.tin", `{"pattern":"^[0-9]{8}-[0-9]{4}$"}`)
	jsonIs("buyer-tin-required", "required", "buyer.tin", `{}`)

	var sealed bool
	var from *string
	if err := app.QueryRow(ctx, `SELECT sealed, effective_from::text FROM rule_set_versions WHERE version = 4`).Scan(&sealed, &from); err != nil {
		t.Fatalf("read v4: %v", err)
	}
	if !sealed || from == nil || *from != "2026-08-06" {
		t.Errorf("v4 sealed=%t effective_from=%v, want sealed 2026-08-06", sealed, from)
	}
}

func TestV5_VATPerCategory(t *testing.T) {
	rs := v5RuleSet(t)

	wantKeys(t, "compliant B2B", evalV5(t, rs, v5B2BPayload()))

	lines := v5StandardLines()
	lines[0]["line_tax"] = 70.0
	res := evalV5(t, rs, v5Invoice("B2B", lines)) // vat 70 = the sum, so only the category rate is wrong
	wantKeys(t, "STANDARD_VAT tax 70", res, "vat-standard-rate")
	wantAt(t, violationOf(t, res, "vat-standard-rate"), "tax_subtotals[1]", "75", "70")

	p := v5B2BPayload()
	invoiceOf(p)["vat"] = 80.0
	res = evalV5(t, rs, p)
	wantKeys(t, "vat 80 against subtotals 75", res, "vat-equals-tax-subtotals")
	wantAt(t, violationOf(t, res, "vat-equals-tax-subtotals"), "vat", "75", "80")

	// 1.00 x 7.5% = 0.075 against a rounded 0.08 sits exactly on the 0.005 tolerance.
	wantKeys(t, "rounded boundary", evalV5(t, rs, v5Invoice("B2B", []map[string]any{v5Line("1", "STANDARD_VAT", 1.0, 7.5, 0.08)})))
}

func TestV5_PercentMustMatchCategory(t *testing.T) {
	rs := v5RuleSet(t)

	lines := v5StandardLines()
	lines[0]["tax_percent"] = 5.0
	res := evalV5(t, rs, v5Invoice("B2B", lines))
	wantKeys(t, "STANDARD_VAT at 5%", res, "tax-percent-matches-category")
	if v := violationOf(t, res, "tax-percent-matches-category"); v.Path != "line_items[1]" {
		t.Errorf("Path = %q, want line_items[1]", v.Path)
	}

	// STAMP_DUTY is not one of the three rated categories, so its percent is not judged.
	lines = v5StandardLines()
	lines[1] = v5Line("2", "STAMP_DUTY", 500, 3, 15)
	wantKeys(t, "STAMP_DUTY at 3%", evalV5(t, rs, v5Invoice("B2B", lines)))
}

func TestV5_UncategorisedVATIsJudgedAtTheHeader(t *testing.T) {
	rs := v5RuleSet(t)
	uncategorised := func(vat float64) Payload {
		line := v5Line("1", "STANDARD_VAT", 1000, 7.5, 75)
		delete(line, "tax_category")
		delete(line, "tax_percent")
		p := v5Invoice("B2B", []map[string]any{line})
		inv := invoiceOf(p)
		if _, has := inv["tax_subtotals"]; has {
			t.Fatal("fixture still carries tax_subtotals")
		}
		inv["vat"] = vat
		return p
	}

	res := evalV5(t, rs, uncategorised(50))
	wantAt(t, violationOf(t, res, "vat-standard-rate-uncategorised"), "vat", "75", "50")
	for _, key := range []string{"vat-standard-rate", "vat-equals-tax-subtotals"} {
		if hasViolation(res, key) {
			t.Errorf("%s ran on an invoice with no tax_subtotals: %s", key, showViolations(res))
		}
	}

	if res := evalV5(t, rs, uncategorised(75)); hasViolation(res, "vat-standard-rate-uncategorised") {
		t.Errorf("vat 75 on subtotal 1000 still violates: %s", showViolations(res))
	}

	lines := v5StandardLines()
	lines[0]["line_tax"] = 70.0
	res = evalV5(t, rs, v5Invoice("B2B", lines))
	if !hasViolation(res, "vat-standard-rate") || hasViolation(res, "vat-standard-rate-uncategorised") {
		t.Errorf("categorised invoice: violations = %v, want vat-standard-rate and not vat-standard-rate-uncategorised", violationKeys(res))
	}
}

func TestV5_TINFormatAcceptsRN(t *testing.T) {
	rs := v5RuleSet(t)
	for _, who := range []string{"supplier", "buyer"} {
		key := who + "-tin-format"
		for _, tin := range []string{"12345678-0001", "RN-847789"} {
			p := v5B2BPayload()
			invoiceOf(p)[who].(map[string]any)["tin"] = tin
			wantKeys(t, who+" TIN "+tin, evalV5(t, rs, p))
		}
		for _, tin := range []string{"RN-", "rn-123", "RN-12A", "12345678-001", "BADTIN"} {
			p := v5B2BPayload()
			invoiceOf(p)[who].(map[string]any)["tin"] = tin
			res := evalV5(t, rs, p)
			wantKeys(t, who+" TIN "+tin, res, key)
			if v := violationOf(t, res, key); v.Expected == nil || *v.Expected != v5TINPattern {
				t.Errorf("%s %q Expected = %s, want %q", key, tin, ptrStr(v.Expected), v5TINPattern)
			}
		}
	}
}

func TestV5_CurrencyAgainstTheList(t *testing.T) {
	rs := v5RuleSet(t)

	p := v5B2BPayload()
	invoiceOf(p)["currency"] = "USD"
	wantKeys(t, "currency USD", evalV5(t, rs, p))

	invoiceOf(p)["currency"] = "XXX"
	res := evalV5(t, rs, p)
	wantKeys(t, "currency XXX", res, "currency-allowed")
	if v := violationOf(t, res, "currency-allowed"); v.Expected == nil || *v.Expected != "NRS list: currencies" || v.Actual == nil || *v.Actual != "XXX" {
		t.Errorf("currency-allowed Expected/Actual = %s/%s, want NRS list: currencies/XXX", ptrStr(v.Expected), ptrStr(v.Actual))
	}
}

func TestV5_MissingListFailsLoud(t *testing.T) {
	super, _ := dbTestPools(t)
	seedV5Lists(t, super)
	ctx := context.Background()
	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	got, err := loadForDatesTx(ctx, tx, []string{v5Start})
	if err != nil || got[v5Start].Version != 5 {
		t.Fatalf("load with the lists present = v%d, %v; want v5 and no error", got[v5Start].Version, err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM nrs_codes WHERE list = 'currencies'`); err != nil {
		t.Fatalf("delete currencies: %v", err)
	}
	_, err = loadForDatesTx(ctx, tx, []string{v5Start})
	if !errors.Is(err, ErrCodeListMissing) {
		t.Fatalf("load without currencies: err = %v, want ErrCodeListMissing", err)
	}
	if !strings.Contains(err.Error(), "currencies") {
		t.Errorf("err %q does not name the currencies list", err)
	}
}

func TestV5_DownRemovesV5(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	matches, err := fs.Glob(migrations.FS, v5MigrationGlob)
	if err != nil || len(matches) != 1 {
		t.Fatalf("want exactly one migrations/%s, got %v (err %v)", v5MigrationGlob, matches, err)
	}
	raw, err := fs.ReadFile(migrations.FS, matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	down := gooseDownStatements(t, string(raw))
	if len(down) == 0 {
		t.Fatalf("%s has no Down statements", matches[0])
	}

	tx := edBegin(t, ctx, super)
	var v5ID string
	var v5Rules int
	if err := tx.QueryRow(ctx,
		`SELECT v.id, (SELECT count(*) FROM rules WHERE rule_set_version_id = v.id) FROM rule_set_versions v WHERE v.version = 5`).Scan(&v5ID, &v5Rules); err != nil || v5Rules == 0 {
		t.Fatalf("before the Down: v5 id %q with %d rules (%v), want a v5 with rules", v5ID, v5Rules, err)
	}
	for _, stmt := range down {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("Down statement failed: %v\n%s", err, stmt)
		}
	}

	var versions, orphans, v4Rules int
	var sealed bool
	var from *string
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM rule_set_versions WHERE version = 5`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM rules WHERE rule_set_version_id = $1`, v5ID).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT v.sealed, v.effective_from::text, (SELECT count(*) FROM rules WHERE rule_set_version_id = v.id)
		   FROM rule_set_versions v WHERE v.version = 4`).Scan(&sealed, &from, &v4Rules); err != nil {
		t.Fatalf("read v4 after the Down: %v", err)
	}
	if versions != 0 || orphans != 0 {
		t.Errorf("after the Down: %d v5 rows, %d v5 rules, want 0 and 0", versions, orphans)
	}
	if !sealed || from == nil || *from != "2026-08-06" || v4Rules != 20 {
		t.Errorf("v4 after the Down: sealed=%t from=%v rules=%d, want sealed, 2026-08-06, 20", sealed, from, v4Rules)
	}
	edWant(t, edVersionFor(t, ctx, tx, v5Start), versionIDByVersion(t, ctx, tx, 4), "rule_set_version_for("+v5Start+") after the Down")
}
