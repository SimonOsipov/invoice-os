// task-108 / M4-04-02 (Test-first: yes) -- Mode A RED specs, continued from
// payload_test.go: the real-rule half of PAY-01..21 (PAY-03, 04, 06, 08, 09,
// 10, 11, 13, 15, 16, 17, 18). See payload_test.go's header for the full
// context and the D2 package-split rationale.
//
// package invoice_test (an EXTERNAL test package) is deliberate: every
// evaluator in internal/validation is unexported (evaluators.go/
// evaluators_math.go/cel.go; registry.go's own header: "deliberately
// unexported ... this exported factory is the single seam"), so a rule can
// only be driven through the exported validation.NewDefaultEngine() +
// validation.RuleSet -- never by naming an evaluator type from inside
// package invoice. Being external, this file imports BOTH internal/invoice
// and internal/validation as an outside consumer -- architecturally honest
// (a contract test between two services, belonging to neither) and adds NO
// import edge to package invoice itself: `go list -deps ./internal/invoice`
// (production or internal test) stays validation-free, so [payload-mapper]'s
// 03-must-not-import-04 ban is provably intact. See task-108's Stage-1
// addendum D2.
//
// D1 (the CEL/json.Number blocker): cel-go maps json.Number to CEL `string`
// (it is a named string type), so feeding MBSPayload's OWN output straight
// to the engine makes every numeric CEL guard (type(x.unit_price)!=double)
// see a string and silently skip -- exactly what [payload-numerics] exists
// to avoid, and exactly what production does NOT do (03 marshals, 04
// decodes with a plain decoder, CEL sees float64/double). asWire below
// reproduces that wire crossing; every spec here that evaluates a payload
// against the real engine routes through it (rooted, in fact, since it also
// applies the "invoice" root -- [payload-mapper]/Decision N19).
//
// Every test below runs against internal/invoice/payload_qa_scaffold.go, a
// QA Mode-A compile scaffold (NOT the mapper -- see that file's header).
// Per-spec RED/pass status against that scaffold is called out in each
// test's comment; the QA report summarizes all 21.
//
// PAY-18 is the one DB-backed spec (Test Specs table): it loads the REAL
// active rule set (v2, 19 rules per task-111/M4-04-01) via
// validation.NewStore(pool).LoadActiveRuleSet -- same env gate as the rest
// of the repo's DB-integration suites:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -v ./internal/invoice/...
package invoice_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/validation"
)

// ptr is this file's own strPtr equivalent -- internal/invoice's strPtr
// (store_test.go) is unexported and this is a different (external) package.
func ptr(s string) *string { return &s }

// asWire marshals p then decodes it with a PLAIN decoder (no UseNumber) --
// exactly what 04 (internal/validation) does when it receives 03's
// marshaled payload over the wire. Every real-engine spec below routes
// through this (see file header, D1): fed directly, MBSPayload's
// json.Number money fields are a named STRING type to cel-go
// (indistinguishable from a raw string); asWire is what turns them into the
// float64 the production decode path (and CEL's `double`) actually sees.
func asWire(t *testing.T, p map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("asWire: marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("asWire: unmarshal: %v", err)
	}
	return out
}

// rooted wraps a MBSPayload() result under "invoice" (the CALLER's job per
// [payload-mapper]/Decision N19 -- MBSPayload itself returns the bare
// invoice object) and round-trips it through asWire, so real-engine specs
// evaluate exactly what 04 would decode in production.
func rooted(t *testing.T, inv invoice.Invoice) validation.Payload {
	t.Helper()
	return asWire(t, map[string]any{"invoice": invoice.MBSPayload(inv)})
}

// rulesAppPool returns the invoice_app-role pool for PAY-18, or skips when
// DATABASE_URL is unset -- same env-gated-skip convention as
// internal/validation/schema_test.go's dbTestPools and
// internal/invoice/store_test.go's dbTestPools.
func rulesAppPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("PAY-18 (DB-backed) skipped: set DATABASE_URL (or run `make test-rls`)")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect app pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping app pool (is the DB up?): %v", err)
	}
	return pool
}

// rulesIdentity builds a fresh authenticated identity context -- rule_set_
// versions/rules are GLOBAL, untenanted tables (internal/validation/
// store.go's file header), so the specific tenant is arbitrary;
// LoadActiveRuleSet only needs db.WithinRequestTenantTx to see a valid
// identity in ctx (mirrors internal/validation/seed_test.go's
// newTestIdentity).
func rulesIdentity() context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{
		Subject: "qa-pay18", Role: "authenticated", TenantID: uuid.NewString(),
	})
}

// --- real v2 rule definitions, verified byte-identical against the live
// dev DB (task-111/M4-04-01's published v2, 19 rules) -- see the QA report
// for the verification query. Each PAY-* spec below is "unit" per the Test
// Specs table (only PAY-18 is DB-backed), so these are hand-built literals
// mirroring the real published content, driven through the REAL engine
// (validation.NewDefaultEngine()) rather than loaded from the DB per test.

func ruleSubtotalNonNegative() validation.Rule {
	return validation.Rule{
		Key: "subtotal-non-negative", Type: validation.TypeRange, Target: "subtotal",
		Params: json.RawMessage(`{"min":0}`), Severity: "error",
		Message: "Subtotal must be zero or positive.", Scope: "document", Enabled: true,
	}
}

func ruleVATStandardRate() validation.Rule {
	return validation.Rule{
		Key: "vat-standard-rate", Type: validation.TypeTaxMath,
		Params:   json.RawMessage(`{"base":"subtotal","rate":0.075,"expected":"vat","tolerance":0.005}`),
		Severity: "error", Message: "VAT must equal 7.5% of the subtotal.", Scope: "document", Enabled: true,
	}
}

func ruleCurrencyRequired() validation.Rule {
	return validation.Rule{
		Key: "currency-required", Type: validation.TypeRequired, Target: "currency",
		Params: json.RawMessage(`{}`), Severity: "error",
		Message: "Currency is required.", Scope: "document", Enabled: true,
	}
}

func ruleLineItemsRequired() validation.Rule {
	return validation.Rule{
		Key: "line-items-required", Type: validation.TypeRequired, Target: "line_items",
		Params: json.RawMessage(`{}`), Severity: "error",
		Message: "Invoice must include line items.", Scope: "document", Enabled: true,
	}
}

func ruleLineCostNonNegative() validation.Rule {
	return validation.Rule{
		Key: "line-cost-non-negative", Type: validation.TypeCEL,
		Params:   json.RawMessage(`{"expr":"!has(invoice.line_items) || invoice.line_items.all(x, !has(x.unit_price) || type(x.unit_price) != double || x.unit_price >= 0.0)"}`),
		Severity: "error", Message: "Line item cost must be zero or positive.", Scope: "document", Enabled: true,
	}
}

func ruleLineItemsSumSubtotal() validation.Rule {
	return validation.Rule{
		Key: "line-items-sum-subtotal", Type: validation.TypeLineSum,
		Params:   json.RawMessage(`{"items":"line_items","amount":"unit_price","expected":"subtotal","quantity":"quantity","tolerance":0.005}`),
		Severity: "error", Message: "Line item amounts must sum to the invoice subtotal.", Scope: "document", Enabled: true,
	}
}

func ruleNoDuplicateLineItems() validation.Rule {
	return validation.Rule{
		Key: "no-duplicate-line-items", Type: validation.TypeCEL,
		Params:   json.RawMessage(`{"expr":"!has(invoice.line_items) || invoice.line_items.all(x, !has(x.id) || invoice.line_items.filter(y, has(y.id) && y.id == x.id).size() <= 1)"}`),
		Severity: "error", Message: "Invoice contains duplicate line items (a line id appears more than once).", Scope: "document", Enabled: true,
	}
}

// ---------------------------------------------------------------------
// PAY-03/04 -- money crosses as numbers; the real range/tax_math rules see
// numbers and do not violate a correct invoice.
// ---------------------------------------------------------------------

// TestPayloadEngine_SubtotalNonNegative_NumberAccepted (PAY-03): RED against
// the scaffold -- subtotal is a raw *string, so after asWire it is a JSON
// string; toFloat rejects strings, so rangeEval violates a correct invoice.
func TestPayloadEngine_SubtotalNonNegative_NumberAccepted(t *testing.T) {
	inv := invoice.Invoice{Subtotal: ptr("1058875.00")}
	p := rooted(t, inv)

	res, err := validation.NewDefaultEngine().Evaluate(p, validation.RuleSet{
		Rules: []validation.Rule{ruleSubtotalNonNegative()},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v [PAY-03]", err)
	}
	if len(res.Violations) != 0 {
		t.Errorf("subtotal-non-negative violations = %v, want none -- a well-formed "+
			"number must be accepted by toFloat, not silently mistaken for a bad string [PAY-03]", res.Violations)
	}
}

// TestPayloadEngine_VATStandardRate_NumbersReconcile (PAY-04): RED against
// the scaffold -- subtotal/vat are raw *string, so taxMathEval's
// resolveNumericOperand rejects them as non-numeric DATA -> violation.
func TestPayloadEngine_VATStandardRate_NumbersReconcile(t *testing.T) {
	inv := invoice.Invoice{Subtotal: ptr("1000.00"), VAT: ptr("75.00")}
	p := rooted(t, inv)

	res, err := validation.NewDefaultEngine().Evaluate(p, validation.RuleSet{
		Rules: []validation.Rule{ruleVATStandardRate()},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v [PAY-04]", err)
	}
	if len(res.Violations) != 0 {
		t.Errorf("vat-standard-rate violations = %v, want none -- 1000.00 * 7.5%% = "+
			"75.00 exactly [PAY-04]", res.Violations)
	}
}

// ---------------------------------------------------------------------
// PAY-06 -- the real currency-required rule fires on the omitted key.
// ---------------------------------------------------------------------

// TestPayloadEngine_NilCurrency_RequiredFires (PAY-06): passes even against
// the scaffold -- requiredEval treats a present-null value exactly like an
// absent one (evaluators.go's own doc comment: "A present-but-JSON-null
// value is ... a violation for required (a null is not present)"), so this
// holds under ANY stub shape that doesn't fabricate a real currency string.
// Kept as written; see the QA report.
func TestPayloadEngine_NilCurrency_RequiredFires(t *testing.T) {
	inv := invoice.Invoice{Currency: nil}
	p := rooted(t, inv)

	res, err := validation.NewDefaultEngine().Evaluate(p, validation.RuleSet{
		Rules: []validation.Rule{ruleCurrencyRequired()},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v [PAY-06]", err)
	}
	if len(res.Violations) != 1 || res.Violations[0].RuleKey != "currency-required" {
		t.Errorf("violations = %v, want exactly one currency-required violation on the "+
			"omitted key [PAY-06]", res.Violations)
	}
}

// ---------------------------------------------------------------------
// PAY-08/09/10 -- a line-less invoice violates line-items-required (an []
// would wrongly pass); the real line-cost-non-negative CEL rule and
// line-items-sum-subtotal both pass with NO error (a null would fault).
// ---------------------------------------------------------------------

// TestPayloadEngine_EmptyLineItems_RequiredFires (PAY-08): RED against the
// scaffold -- line_items is always emitted as [] (present, non-null), and
// requiredEval does not fire on a present empty list, only on absent/null.
func TestPayloadEngine_EmptyLineItems_RequiredFires(t *testing.T) {
	inv := invoice.Invoice{LineItems: nil}
	p := rooted(t, inv)

	res, err := validation.NewDefaultEngine().Evaluate(p, validation.RuleSet{
		Rules: []validation.Rule{ruleLineItemsRequired()},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v [PAY-08]", err)
	}
	if len(res.Violations) != 1 || res.Violations[0].RuleKey != "line-items-required" {
		t.Errorf("violations = %v, want exactly one line-items-required violation -- an "+
			"empty invoice must OMIT line_items so required fires (an emitted [] would "+
			"wrongly pass) [PAY-08]", res.Violations)
	}
}

// TestPayloadEngine_EmptyLineItems_CostRuleNoError (PAY-09): passes even
// against the scaffold -- whether line_items is represented as an absent
// key (`!has()` short-circuits) or a present EMPTY array (`.all()` over an
// empty list is vacuously true in CEL), the CEL guard never errors either
// way; only a present-but-NULL value would fault it. This invariant holds
// regardless of implementation. Kept as written; see the QA report.
func TestPayloadEngine_EmptyLineItems_CostRuleNoError(t *testing.T) {
	inv := invoice.Invoice{LineItems: nil}
	p := rooted(t, inv)

	res, err := validation.NewDefaultEngine().Evaluate(p, validation.RuleSet{
		Rules: []validation.Rule{ruleLineCostNonNegative()},
	})
	if err != nil {
		t.Fatalf("Evaluate: want no engine error (a present-but-null line_items would "+
			"fault the CEL .all() -- Decision N15 would surface it as this error), got: %v [PAY-09]", err)
	}
	if len(res.Violations) != 0 {
		t.Errorf("violations = %v, want none [PAY-09]", res.Violations)
	}
}

// TestPayloadEngine_EmptyLineItems_SumRuleNoError (PAY-10): passes even
// against the scaffold -- lineSumEval treats "path absent" and "resolves to
// a non-list/empty list" identically (both "not applicable" -> pass, nil);
// this invariant holds regardless of implementation. Kept as written; see
// the QA report.
func TestPayloadEngine_EmptyLineItems_SumRuleNoError(t *testing.T) {
	inv := invoice.Invoice{LineItems: nil, Subtotal: ptr("0.00")}
	p := rooted(t, inv)

	res, err := validation.NewDefaultEngine().Evaluate(p, validation.RuleSet{
		Rules: []validation.Rule{ruleLineItemsSumSubtotal()},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v [PAY-10]", err)
	}
	if len(res.Violations) != 0 {
		t.Errorf("violations = %v, want none -- line-items-required owns the absence "+
			"case [PAY-10]", res.Violations)
	}
}

// ---------------------------------------------------------------------
// PAY-11 -- line items carry their id; the real no-duplicate-line-items
// rule passes on distinct uuids.
// ---------------------------------------------------------------------

// TestPayloadEngine_LineItemsCarryID_NoDuplicateRulePasses (PAY-11): RED
// against the scaffold on the id-presence assertion -- the scaffold
// deliberately omits "id" from each mapped line.
func TestPayloadEngine_LineItemsCarryID_NoDuplicateRulePasses(t *testing.T) {
	idA, idB := uuid.NewString(), uuid.NewString()
	inv := invoice.Invoice{LineItems: []invoice.LineItem{
		{ID: idA, LineNo: 1, UnitPrice: ptr("100.00")},
		{ID: idB, LineNo: 2, UnitPrice: ptr("50.00")},
	}}
	p := rooted(t, inv)

	invoiceMap, ok := p["invoice"].(map[string]any)
	if !ok {
		t.Fatalf(`payload["invoice"] not a map: %#v [PAY-11]`, p["invoice"])
	}
	lines, ok := invoiceMap["line_items"].([]any)
	if !ok || len(lines) != 2 {
		t.Fatalf("payload invoice.line_items = %#v, want a 2-element list [PAY-11]", invoiceMap["line_items"])
	}
	for i, want := range []string{idA, idB} {
		line, ok := lines[i].(map[string]any)
		if !ok {
			t.Fatalf("line_items[%d] not a map: %#v [PAY-11]", i, lines[i])
		}
		if got := line["id"]; got != want {
			t.Errorf(`line_items[%d]["id"] = %#v, want %q -- each mapped line must `+
				`carry its id [PAY-11]`, i, got, want)
		}
	}

	res, err := validation.NewDefaultEngine().Evaluate(p, validation.RuleSet{
		Rules: []validation.Rule{ruleNoDuplicateLineItems()},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v [PAY-11]", err)
	}
	if len(res.Violations) != 0 {
		t.Errorf("no-duplicate-line-items violations = %v, want none -- distinct uuids [PAY-11]", res.Violations)
	}
}

// ---------------------------------------------------------------------
// PAY-13 -- a store-invalid numeric ("NaN") fires its own range rule.
// ---------------------------------------------------------------------

// TestPayloadEngine_NaNSubtotal_RangeViolates (PAY-13): passes even against
// the scaffold -- the scaffold always emits the raw *string, which
// coincides with jsonNumber's documented not-well-formed fallback for
// "NaN" specifically (both correctly produce a bad-DATA violation here).
// Kept as written; see the QA report.
func TestPayloadEngine_NaNSubtotal_RangeViolates(t *testing.T) {
	inv := invoice.Invoice{Subtotal: ptr("NaN")}
	p := rooted(t, inv)

	res, err := validation.NewDefaultEngine().Evaluate(p, validation.RuleSet{
		Rules: []validation.Rule{ruleSubtotalNonNegative()},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v [PAY-13]", err)
	}
	if len(res.Violations) != 1 || res.Violations[0].RuleKey != "subtotal-non-negative" {
		t.Errorf(`violations = %v, want exactly one subtotal-non-negative violation -- `+
			`"NaN" is bad DATA, not a config fault [PAY-13]`, res.Violations)
	}
}

// ---------------------------------------------------------------------
// PAY-15 -- a negative UnitPrice fires the real line-cost-non-negative CEL
// rule, proving CEL sees `double`, not a string. THE critical D1 spec.
// ---------------------------------------------------------------------

// TestPayloadEngine_NegativeUnitPrice_CELSeesDouble (PAY-15): RED against
// the scaffold -- unit_price is a raw *string, so after asWire it's a JSON
// string; CEL's `type(x.unit_price) != double` is true for a string, so the
// guard short-circuits and the rule silently skips (no violation), exactly
// the D1 failure mode this spec exists to catch.
func TestPayloadEngine_NegativeUnitPrice_CELSeesDouble(t *testing.T) {
	inv := invoice.Invoice{LineItems: []invoice.LineItem{
		{ID: uuid.NewString(), LineNo: 1, UnitPrice: ptr("-5.00")},
	}}
	p := rooted(t, inv)

	res, err := validation.NewDefaultEngine().Evaluate(p, validation.RuleSet{
		Rules: []validation.Rule{ruleLineCostNonNegative()},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v [PAY-15]", err)
	}
	if len(res.Violations) != 1 || res.Violations[0].RuleKey != "line-cost-non-negative" {
		t.Errorf("violations = %v, want exactly one line-cost-non-negative violation -- "+
			"CEL must see a double, not a string (type(x.unit_price)!=double would be "+
			"true for a string and silently skip) [PAY-15]", res.Violations)
	}
}

// ---------------------------------------------------------------------
// PAY-16/17 -- quantity-weighted line-items-sum-subtotal reconciles when
// correct and violates when not.
// ---------------------------------------------------------------------

// TestPayloadEngine_LineSum_Reconciles (PAY-16): RED against the scaffold --
// quantity/unit_price are raw *string, so lineSumEval's toFloat rejects
// them as non-numeric DATA -> violation, even though the math reconciles.
func TestPayloadEngine_LineSum_Reconciles(t *testing.T) {
	inv := invoice.Invoice{
		Subtotal: ptr("250.00"),
		LineItems: []invoice.LineItem{
			{ID: uuid.NewString(), LineNo: 1, Quantity: ptr("2"), UnitPrice: ptr("100.00")},
			{ID: uuid.NewString(), LineNo: 2, Quantity: ptr("1"), UnitPrice: ptr("50.00")},
		},
	}
	p := rooted(t, inv)

	res, err := validation.NewDefaultEngine().Evaluate(p, validation.RuleSet{
		Rules: []validation.Rule{ruleLineItemsSumSubtotal()},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v [PAY-16]", err)
	}
	if len(res.Violations) != 0 {
		t.Errorf("violations = %v, want none -- 100.00*2 + 50.00*1 = 250.00 [PAY-16]", res.Violations)
	}
}

// TestPayloadEngine_LineSum_Violates (PAY-17): passes even against the
// scaffold -- lineSumEval already violates on the raw-string DATA fault
// (same mechanism as PAY-16), which happens to coincide with "expects a
// violation" here regardless of the underlying reconciliation math. Kept as
// written; see the QA report.
func TestPayloadEngine_LineSum_Violates(t *testing.T) {
	inv := invoice.Invoice{
		Subtotal: ptr("999.00"),
		LineItems: []invoice.LineItem{
			{ID: uuid.NewString(), LineNo: 1, Quantity: ptr("2"), UnitPrice: ptr("100.00")},
			{ID: uuid.NewString(), LineNo: 2, Quantity: ptr("1"), UnitPrice: ptr("50.00")},
		},
	}
	p := rooted(t, inv)

	res, err := validation.NewDefaultEngine().Evaluate(p, validation.RuleSet{
		Rules: []validation.Rule{ruleLineItemsSumSubtotal()},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v [PAY-17]", err)
	}
	if len(res.Violations) != 1 || res.Violations[0].RuleKey != "line-items-sum-subtotal" {
		t.Errorf("violations = %v, want exactly one line-items-sum-subtotal violation -- "+
			"250.00 != 999.00 [PAY-17]", res.Violations)
	}
}

// ---------------------------------------------------------------------
// PAY-18 -- a fully valid invoice evaluates to ZERO violations against the
// REAL v2 rule set (DB-backed; the discriminating mis-rooting test,
// [batch-payload-rooting], AC #1).
// ---------------------------------------------------------------------

// TestPayloadEngine_ValidInvoice_ZeroViolationsAgainstRealV2 (PAY-18): RED
// against the scaffold -- supplier/buyer stay flat (never nested), so
// supplier-tin-required/supplier-name-required (v2 rules resolving
// "supplier.tin"/"supplier.name") both fire on the resulting absence, and
// every money field is a string (not a number), tripping range/tax_math/
// line_sum too. Uses the pinned fixture from task-108's Stage-1 addendum
// D4, checked rule-by-rule against all 19 live v2 rules.
func TestPayloadEngine_ValidInvoice_ZeroViolationsAgainstRealV2(t *testing.T) {
	pool := rulesAppPool(t)
	store := validation.NewStore(pool)

	rs, err := store.LoadActiveRuleSet(rulesIdentity())
	if err != nil {
		t.Fatalf("LoadActiveRuleSet: %v [PAY-18]", err)
	}
	if rs.Version != 2 || len(rs.Rules) != 19 {
		t.Fatalf("active rule set = version %d with %d rules, want version 2 with 19 "+
			"rules -- dev DB drifted from the pinned state [PAY-18 precondition]", rs.Version, len(rs.Rules))
	}

	idA, idB := uuid.NewString(), uuid.NewString()
	issueDate := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	inv := invoice.Invoice{
		InvoiceNumber: "INV-001",
		IssueDate:     &issueDate,
		Currency:      ptr("NGN"),
		SupplierTIN:   ptr("12345678-0001"),
		SupplierName:  ptr("Acme Ltd"),
		BuyerTIN:      ptr("87654321-0002"),
		BuyerName:     ptr("Beta Ltd"),
		Subtotal:      ptr("250.00"),
		VAT:           ptr("18.75"),
		Total:         ptr("268.75"),
		LineItems: []invoice.LineItem{
			{ID: idA, LineNo: 1, Quantity: ptr("2"), UnitPrice: ptr("100.00"), LineTotal: ptr("200.00")},
			{ID: idB, LineNo: 2, Quantity: ptr("1"), UnitPrice: ptr("50.00"), LineTotal: ptr("50.00")},
		},
	}
	p := rooted(t, inv)

	res, err := validation.NewDefaultEngine().Evaluate(p, rs)
	if err != nil {
		t.Fatalf("Evaluate: %v [PAY-18]", err)
	}
	if len(res.Violations) != 0 {
		t.Errorf("violations = %v, want zero -- a fully valid invoice must satisfy all "+
			"19 v2 rules; a non-empty result here also catches mis-rooting (a mis-rooted "+
			"payload fires every required rule, not zero) [PAY-18/AC#1/batch-payload-rooting]", res.Violations)
	}
}
