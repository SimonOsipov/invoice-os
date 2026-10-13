// Rule-set v5, dated 2027-01-01. Content tests read the migration file re-applied in a
// rolled-back tx (v5ReappliedTx), so an edit to the file is observable without re-migrating.
// Version-boundary tests use fixed dates only, so none reads today's date.
package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/validation/codelist"
	"github.com/jackc/pgx/v5"
)

const (
	v5BuyerGuard    = `has(invoice.invoice_kind) && invoice.invoice_kind in ['B2B', 'B2G']`
	v5TINPattern    = `^(?:[0-9]{8}-[0-9]{4}|RN-[0-9]+)$`
	v5MigrationGlob = "*_rule_set_v5.sql"
)

// v5RuleSet seeds the code lists and loads the rule set in force on v5's start date from the
// migration file re-applied in a rolled-back tx (see v5ReappliedTx).
func v5RuleSet(t *testing.T) RuleSet {
	t.Helper()
	super, _ := dbTestPools(t)
	seedV5Lists(t, super)
	return v5LoadOn(t, v5ReappliedTx(t, super), v5Start, 5)
}

// v5LoadOn loads the rule set in force on date through tx and fails unless it is wantVersion.
func v5LoadOn(t *testing.T, tx pgx.Tx, date string, wantVersion int) RuleSet {
	t.Helper()
	got, err := loadForDatesTx(context.Background(), tx, []string{date})
	if err != nil {
		t.Fatalf("load the rule set in force on %s: %v", date, err)
	}
	rs := got[date]
	if rs.Version != wantVersion {
		t.Fatalf("rule set in force on %s is v%d, want v%d", date, rs.Version, wantVersion)
	}
	if len(rs.Rules) == 0 {
		t.Fatalf("v%d has no rules", wantVersion)
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

func versionOnDate(t *testing.T, q queryRower, date string) int {
	t.Helper()
	var v *int
	if err := q.QueryRow(context.Background(),
		`SELECT v.version FROM rule_set_versions v WHERE v.id = rule_set_version_for($1::date)`, date).Scan(&v); err != nil || v == nil {
		t.Fatalf("version in force on %s: %v (%v)", date, err, v)
	}
	return *v
}

func TestV5_IsScheduledAndSealed(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()
	app := v5ReappliedTx(t, super)

	var sealed bool
	var from, notes *string
	if err := app.QueryRow(ctx,
		`SELECT sealed, effective_from::text, notes FROM rule_set_versions WHERE version = 5`).Scan(&sealed, &from, &notes); err != nil {
		t.Fatalf("read v5: %v", err)
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
	super, _ := dbTestPools(t)
	tx := v5ReappliedTx(t, super)
	for date, want := range map[string]int{"2026-12-31": 4, "2027-01-01": 5, "2027-06-30": 5} {
		if got := versionOnDate(t, tx, date); got != want {
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
	super, _ := dbTestPools(t)
	app := v5ReappliedTx(t, super)
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
	super, _ := dbTestPools(t)
	rows := ruleRowsByKey(t, v5ReappliedTx(t, super), 5)

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
	super, _ := dbTestPools(t)
	ctx := context.Background()
	app := v5ReappliedTx(t, super) // v4 as the v5 publish leaves it

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

	// 1.00 x 7.5% = 0.075 against a rounded 0.08 sits exactly on the 0.005 tolerance; 0.081 is just past it.
	wantKeys(t, "rounded boundary", evalV5(t, rs, v5Invoice("B2B", []map[string]any{v5Line("1", "STANDARD_VAT", 1.0, 7.5, 0.08)})))
	wantKeys(t, "past the boundary", evalV5(t, rs, v5Invoice("B2B", []map[string]any{v5Line("1", "STANDARD_VAT", 1.0, 7.5, 0.081)})), "vat-standard-rate")

	// ZERO_VAT and EXEMPTED are rated at 0: any tax on them is a violation, each at its own element.
	for _, cat := range []string{"ZERO_VAT", "EXEMPTED"} {
		lines := v5StandardLines()
		lines[1], lines[2] = v5Line("2", "ZERO_VAT", 500, 0, 0), v5Line("3", "EXEMPTED", 200, 0, 0)
		idx := map[string]int{"ZERO_VAT": 1, "EXEMPTED": 2}[cat]
		lines[idx]["line_tax"] = 10.0
		res := evalV5(t, rs, v5Invoice("B2B", lines))
		wantKeys(t, cat+" taxed", res, "vat-standard-rate")
		wantAt(t, violationOf(t, res, "vat-standard-rate"), fmt.Sprintf("tax_subtotals[%d]", idx+1), "0", "10")
	}
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
		for _, tin := range []string{"RN-", "rn-123", "RN-12A", "12345678-001", "BADTIN", "x12345678-0001", "12345678-0001-", "RN-847789\n", " RN-847789"} {
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

	// D10: matching is exact. NRS holds no "NGN " and no "ngn", so neither passes.
	for _, bad := range []string{"NGN ", "ngn", " NGN"} {
		invoiceOf(p)["currency"] = bad
		wantKeys(t, "currency "+bad, evalV5(t, rs, p), "currency-allowed")
	}

	// The tax currency is judged by its own rule against the same list.
	p = v5B2BPayload()
	invoiceOf(p)["tax_currency_code"] = "USD"
	wantKeys(t, "tax currency USD", evalV5(t, rs, p))
	for _, bad := range []string{"XXX", "NGN "} {
		invoiceOf(p)["tax_currency_code"] = bad
		wantKeys(t, "tax currency "+bad, evalV5(t, rs, p), "tax-currency-allowed")
	}
}

func TestV5_BuyerRulesFollowTheInvoiceKind(t *testing.T) {
	rs := v5RuleSet(t)
	presence := []string{
		"buyer-tin-required", "buyer-name-required", "buyer-email-required", "buyer-street-required", "buyer-city-required",
		"buyer-postal-zone-required", "buyer-lga-required", "buyer-state-required", "buyer-country-required",
	}
	with := func(kind string, buyer map[string]any) Payload {
		p := v5B2BPayload()
		inv := invoiceOf(p)
		if kind == "" {
			delete(inv, "invoice_kind")
		} else {
			inv["invoice_kind"] = kind
		}
		if buyer == nil {
			delete(inv, "buyer")
		} else {
			inv["buyer"] = buyer
		}
		return p
	}
	sameKeys := func(what string, res Result, keys ...string) {
		t.Helper()
		got, want := violationKeys(res), slices.Clone(keys)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: violations = %s, want exactly %v", what, showViolations(res), want)
		}
	}

	for _, buyer := range []map[string]any{nil, {}} {
		sameKeys("B2C, buyer block absent or empty", evalV5(t, rs, with("B2C", buyer)))
	}
	for _, kind := range []string{"B2B", "B2G"} {
		for _, buyer := range []map[string]any{nil, {}} {
			sameKeys(kind+", buyer block absent or empty", evalV5(t, rs, with(kind, buyer)), presence...)
		}
		noTIN := v5Party("", "Buyer Ltd", "NG-FC-AML", "NG-FC")
		delete(noTIN, "tin")
		res := evalV5(t, rs, with(kind, noTIN))
		sameKeys(kind+", buyer without a TIN", res, "buyer-tin-required")
		if v := violationOf(t, res, "buyer-tin-required"); v.Path != "buyer.tin" {
			t.Errorf("Path = %q, want buyer.tin", v.Path)
		}
		blankTIN := v5Party("   ", "Buyer Ltd", "NG-FC-AML", "NG-FC")
		if res := evalV5(t, rs, with(kind, blankTIN)); !hasViolation(res, "buyer-tin-required") {
			t.Errorf("%s, blank buyer TIN: %s, want buyer-tin-required", kind, showViolations(res))
		}
	}

	// D7: no kind, or a kind outside the list, is the kind rules' failure and never a buyer failure (and never an engine error).
	sameKeys("no invoice_kind", evalV5(t, rs, with("", nil)), "invoice-kind-required")
	for _, kind := range []string{"b2b", "B2X", "G2B"} {
		sameKeys("invoice_kind "+kind, evalV5(t, rs, with(kind, nil)), "invoice-kind-allowed")
	}
}

func TestV5_V4StillJudgesTheDayBefore(t *testing.T) {
	super, _ := dbTestPools(t)
	seedV5Lists(t, super)
	tx := v5ReappliedTx(t, super)
	v4 := v5LoadOn(t, tx, "2026-12-31", 4)
	v5 := v5LoadOn(t, tx, v5Start, 5)
	if len(v4.Rules) != 20 {
		t.Fatalf("v4 holds %d rules, want 20", len(v4.Rules))
	}

	lines := []map[string]any{v5Line("1", "STANDARD_VAT", 1000, 7.5, 75)}
	wantKeys(t, "compliant B2B under v4", evalV5(t, v4, v5Invoice("B2B", lines)))

	// Each payload below is a v4 failure and a v5 pass: the issue date alone picks the judge.
	usd := v5Invoice("B2B", lines)
	invoiceOf(usd)["currency"] = "USD"
	rn := v5Invoice("B2B", lines)
	invoiceOf(rn)["supplier"].(map[string]any)["tin"] = "RN-847789"
	for _, c := range []struct {
		name string
		p    Payload
		key  string
	}{
		{"USD", usd, "currency-allowed"},
		{"RN- supplier TIN", rn, "supplier-tin-format"},
		{"B2C without a buyer block", v5Invoice("B2C", lines), "buyer-tin-required"},
	} {
		wantKeys(t, c.name+" under v4", evalV5(t, v4, c.p), c.key)
		wantKeys(t, c.name+" under v5", evalV5(t, v5, c.p))
	}
}

func TestV5_MissingListFailsLoud(t *testing.T) {
	super, _ := dbTestPools(t)
	seedV5Lists(t, super)
	ctx := context.Background()
	if len(v5Lists) != 8 {
		t.Fatalf("v5 names %d lists, want 8", len(v5Lists))
	}
	for list := range v5Lists {
		t.Run(list, func(t *testing.T) {
			tx := v5ReappliedTx(t, super)
			v5LoadOn(t, tx, v5Start, 5) // the load succeeds while the list is present
			if _, err := tx.Exec(ctx, `DELETE FROM nrs_codes WHERE list = $1`, list); err != nil {
				t.Fatalf("delete %s: %v", list, err)
			}
			_, err := loadForDatesTx(ctx, tx, []string{v5Start})
			if !errors.Is(err, ErrCodeListMissing) {
				t.Fatalf("load without %s: err = %v, want ErrCodeListMissing", list, err)
			}
			if !strings.Contains(err.Error(), list) {
				t.Errorf("err %q does not name the %s list", err, list)
			}
		})
	}
}

// The lists v5's rules name are exactly Design's eight, and each is an ENGI-03 list.
func TestV5_EnumRulesNameOnlyENGI03Lists(t *testing.T) {
	super, _ := dbTestPools(t)
	named := map[string]bool{}
	for key, r := range ruleRowsByKey(t, v5ReappliedTx(t, super), 5) {
		var p struct{ List string }
		if err := json.Unmarshal([]byte(r.Params), &p); err != nil {
			t.Fatalf("%s params %q: %v", key, r.Params, err)
		}
		if r.Type != "enum" || p.List == "" {
			continue
		}
		named[p.List] = true
		if !slices.ContainsFunc(codelist.Lists, func(l codelist.List) bool { return l.Name == p.List }) {
			t.Errorf("%s names list %q, which is not an ENGI-03 list", key, p.List)
		}
	}
	want := []string{"countries", "currencies", "hs-codes", "invoice-quantity-codes", "lgas", "services-codes", "states", "tax-categories"}
	got := make([]string, 0, len(named))
	for n := range named {
		got = append(got, n)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("lists named by v5 enum rules = %v, want %v", got, want)
	}
}

func TestV5_DownRemovesV5(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	down := gooseDownStatements(t, v5MigrationSQL(t))
	if len(down) == 0 {
		t.Fatal("the v5 migration has no Down statements")
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

	// A Down that leaves a guard disabled would silently drop the immutability of every sealed version.
	for _, tc := range []struct{ table, trigger string }{{"rules", "rules_content_lock"}, {"rule_set_versions", "rule_set_versions_seal_guard"}} {
		var enabled string
		if err := tx.QueryRow(ctx,
			`SELECT tgenabled FROM pg_trigger WHERE tgname = $1 AND tgrelid = $2::regclass`, tc.trigger, tc.table).Scan(&enabled); err != nil {
			t.Fatalf("read tgenabled of %s on %s: %v", tc.trigger, tc.table, err)
		}
		if enabled != "O" {
			t.Errorf("%s on %s tgenabled = %q after the Down, want \"O\" (re-enabled)", tc.trigger, tc.table, enabled)
		}
	}
}
