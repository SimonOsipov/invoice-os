package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/validation"
)

const nrsS2SToken = "nrs-qa-test-s2s-token"

// Stub list rules: no shipped rule set checks a code list yet.
const (
	ruleHSList    = "stub-hs-code-list"
	ruleTaxList   = "stub-tax-category-list"
	ruleStateList = "stub-buyer-state-list"
)

func nrsListRule(key, target, list, items string, codes ...string) validation.Rule {
	params := `{"list":"` + list + `"`
	if items != "" {
		params += `,"items":"` + items + `"`
	}
	r := validation.Rule{
		Key: key, Type: validation.TypeEnum, Target: target, Params: json.RawMessage(params + `}`),
		Severity: "error", Message: key + " failed", Scope: "document", Enabled: true, Codes: validation.CodeSet{},
	}
	for _, c := range codes {
		r.Codes[c] = struct{}{}
	}
	return r
}

func nrsListRules() []validation.Rule {
	return []validation.Rule{
		nrsListRule(ruleHSList, "hsn_code", "hs-codes", "line_items", "8471.30", "0101.21"),
		nrsListRule(ruleTaxList, "tax_category", "tax-categories", "line_items", "VAT", "EXEMPT"),
		nrsListRule(ruleStateList, "buyer.state", "states", "", "LA", "OG"),
	}
}

// recorder keeps the body of every batch-validate request the importer's gate sends.
type recorder struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (r *recorder) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(b))
		r.mu.Lock()
		r.bodies = append(r.bodies, b)
		r.mu.Unlock()
		h.ServeHTTP(w, req)
	})
}

// invoices decodes request i into its invoice objects, numbers kept as exact text.
func (r *recorder) invoices(t *testing.T, i int) []map[string]any {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= len(r.bodies) {
		t.Fatalf("gate received %d requests, want at least %d", len(r.bodies), i+1)
	}
	var req struct {
		Invoices []struct {
			Invoice map[string]any `json:"invoice"`
		} `json:"invoices"`
	}
	dec := json.NewDecoder(bytes.NewReader(r.bodies[i]))
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		t.Fatalf("decode gate request %d: %v", i, err)
	}
	out := make([]map[string]any, len(req.Invoices))
	for j, it := range req.Invoices {
		out[j] = it.Invoice
	}
	return out
}

// nrsGateRun wires the real gate to an in-process 04 that records its requests
// and judges the live rule set plus extra stub rules.
type nrsGateRun struct {
	*nrsRun
	rec *recorder
}

func newNRSGateRun(t *testing.T, extra []validation.Rule) *nrsGateRun {
	t.Helper()
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "ENGI-07-02 gate tenant")
	entityID := seedEntityWithTIN(t, super, tenantID, "ENGI-07-02 gate entity", "12345678-0001")

	vstore := validation.NewStore(app)
	load := func(ctx context.Context, dates []string) (map[string]validation.RuleSet, error) {
		base, err := vstore.LoadForDates(ctx, dates)
		if err != nil {
			return nil, err
		}
		out := make(map[string]validation.RuleSet, len(base))
		for d, rs := range base {
			rs.Rules = append(slices.Clone(rs.Rules), extra...)
			out[d] = rs
		}
		return out, nil
	}
	rec := &recorder{}
	handler := validation.S2SMiddleware(nrsS2SToken)(rec.wrap(validation.BatchValidateHandler(load, validation.NewDefaultEngine(), nil, nil)))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	gate := invoice.NewGate(invoice.NewStore(app), invoice.NewValidator(srv.URL, nrsS2SToken, nil))
	return &nrsGateRun{
		rec: rec,
		nrsRun: &nrsRun{
			t: t, super: super, tenantID: tenantID, entityID: entityID,
			svc: newTestServiceWithGate(app, gate),
			ctx: auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}),
		},
	}
}

// onlyInvoice returns the one invoice object of gate request i, with line ids dropped.
func (g *nrsGateRun) onlyInvoice(i int) map[string]any {
	g.t.Helper()
	invs := g.rec.invoices(g.t, i)
	if len(invs) != 1 {
		g.t.Fatalf("request %d holds %d invoices, want 1", i, len(invs))
	}
	inv := invs[0]
	lines, _ := inv["line_items"].([]any)
	for _, l := range lines {
		if m, ok := l.(map[string]any); ok {
			delete(m, "id")
		}
	}
	return inv
}

func lineOf(t *testing.T, inv map[string]any, n int) map[string]any {
	t.Helper()
	lines, _ := inv["line_items"].([]any)
	if n >= len(lines) {
		t.Fatalf("invoice has %d lines, want line %d", len(lines), n+1)
	}
	return lines[n].(map[string]any)
}

func num(v any) string {
	if n, ok := v.(json.Number); ok {
		return n.String()
	}
	return "<not a number>"
}

func nested(inv map[string]any, key, field string) any {
	m, _ := inv[key].(map[string]any)
	return m[field]
}

// Canonical text: the real run reads Postgres text, so both runs agree.
func nrsCanonicalRows(num string) []map[string]string {
	hdr := []string{
		"invoice_kind", "B2B", "tax_currency_code", "USD", "due_date", "2026-07-31", "issue_time", "9:05",
		"tax_point_date", "2026-07-02", "payment_status", "PENDING", "buyer_email", "ada@beta.example",
		"buyer_telephone", "+2348012345678", "buyer_street", "1 Marina Road", "buyer_city", "Lagos",
		"buyer_postal_zone", "101233", "buyer_country", "NG", "buyer_state", "LA", "buyer_lga", "IKJ",
		"subtotal", "150.00", "vat", "11.25", "total", "161.25",
	}
	l1 := nrsRow(num, hdr...)
	for k, v := range map[string]string{
		"line_description": "Laptop", "line_quantity": "2.000", "line_unit_price": "50.00", "line_total": "100.00", "line_tax": "7.50",
		"line_tax_category": "VAT", "line_hsn_code": "8471.30", "line_isic_code": "6201", "line_product_category": "Electronics",
		"line_service_category": "Consulting", "line_sellers_item_identification": "SKU-42", "line_price_unit": "EA",
		"line_tax_percent": "7.50%", "line_base_quantity": "2.000",
	} {
		l1[k] = v
	}
	l2 := nrsRow(num, hdr...)
	for k, v := range map[string]string{
		"line_description": "Mouse", "line_quantity": "1.000", "line_unit_price": "50.00", "line_total": "50.00", "line_tax": "3.75",
		"line_tax_category": "VAT", "line_hsn_code": "0101.21", "line_isic_code": "6202", "line_product_category": "Peripherals",
		"line_service_category": "Support", "line_sellers_item_identification": "SKU-43", "line_price_unit": "EA",
		"line_tax_percent": "7.50 %", "line_base_quantity": "1.000",
	} {
		l2[k] = v
	}
	return []map[string]string{l1, l2}
}

func TestImportNRS_DryRunAndRealRunSendTheSamePayload(t *testing.T) {
	g := newNRSGateRun(t, nil)
	rows := nrsCanonicalRows("PAR-1")

	dry := g.do(true, rows...)
	if dry.ReadyInvoices != 1 || len(dry.Errors) != 0 {
		t.Fatalf("dry run ready = %d, errors = %+v", dry.ReadyInvoices, dry.Errors)
	}
	real := g.do(false, rows...)
	if real.ReadyInvoices != 1 || len(real.Errors) != 0 {
		t.Fatalf("real run ready = %d, errors = %+v", real.ReadyInvoices, real.Errors)
	}
	dryInv, realInv := g.onlyInvoice(0), g.onlyInvoice(1)

	// Positive first: the payload must hold the NRS values at all.
	l1, l2 := lineOf(t, dryInv, 0), lineOf(t, dryInv, 1)
	checkWants(t, []nrsWant{
		{"invoice_kind", sv(strp(dryInv["invoice_kind"])), "B2B"},
		{"tax_currency_code (differs from currency)", sv(strp(dryInv["tax_currency_code"])), "USD"},
		{"payment_status", sv(strp(dryInv["payment_status"])), "PENDING"},
		{"buyer.telephone", sv(strp(nested(dryInv, "buyer", "telephone"))), "+2348012345678"},
		{"buyer.street", sv(strp(nested(dryInv, "buyer", "street"))), "1 Marina Road"},
		{"buyer.city", sv(strp(nested(dryInv, "buyer", "city"))), "Lagos"},
		{"buyer.postal_zone", sv(strp(nested(dryInv, "buyer", "postal_zone"))), "101233"},
		{"buyer.country", sv(strp(nested(dryInv, "buyer", "country"))), "NG"},
		{"buyer.lga", sv(strp(nested(dryInv, "buyer", "lga"))), "IKJ"},
		{"line 1 isic_code", sv(strp(l1["isic_code"])), "6201"},
		{"line 1 product_category", sv(strp(l1["product_category"])), "Electronics"},
		{"line 1 service_category", sv(strp(l1["service_category"])), "Consulting"},
		{"issue_time", sv(strp(dryInv["issue_time"])), "09:05:00"},
		{"due_date", sv(strp(dryInv["due_date"])), "2026-07-31"},
		{"tax_point_date", sv(strp(dryInv["tax_point_date"])), "2026-07-02"},
		{"buyer.state", sv(strp(nested(dryInv, "buyer", "state"))), "LA"},
		{"buyer.email", sv(strp(nested(dryInv, "buyer", "email"))), "ada@beta.example"},
		{"line 1 hsn_code", sv(strp(l1["hsn_code"])), "8471.30"},
		{"line 1 tax_category", sv(strp(l1["tax_category"])), "VAT"},
		{"line 1 line_total", num(l1["line_total"]), "100.00"},
		{"line 1 line_tax", num(l1["line_tax"]), "7.50"},
		{"line 1 tax_percent", num(l1["tax_percent"]), "7.50"},
		{"line 1 base_quantity", num(l1["base_quantity"]), "2.000"},
		{"line 2 sellers_item_identification", sv(strp(l2["sellers_item_identification"])), "SKU-43"},
		{"line 2 hsn_code (a line field is read per row)", sv(strp(l2["hsn_code"])), "0101.21"},
		{"line 2 isic_code", sv(strp(l2["isic_code"])), "6202"},
		{"line 2 product_category", sv(strp(l2["product_category"])), "Peripherals"},
		{"line 2 service_category", sv(strp(l2["service_category"])), "Support"},
		{"line 2 line_total", num(l2["line_total"]), "50.00"},
		{"line 2 line_tax", num(l2["line_tax"]), "3.75"},
		{"line 2 base_quantity", num(l2["base_quantity"]), "1.000"},
		{"line 2 price_unit", sv(strp(l2["price_unit"])), "EA"},
		{"line 2 tax_percent (7.50 % -> 7.50)", num(l2["tax_percent"]), "7.50"},
	})

	if !reflect.DeepEqual(dryInv, realInv) {
		dj, _ := json.Marshal(dryInv)
		rj, _ := json.Marshal(realInv)
		t.Errorf("dry-run and real-run payloads differ\n dry:  %s\n real: %s", dj, rj)
	}
}

func strp(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

func TestImportNRS_TaxSubtotalsMatchOnBothRuns(t *testing.T) {
	g := newNRSGateRun(t, nil)
	rows := nrsCanonicalRows("TAX-1")

	g.do(true, rows...)
	g.do(false, rows...)
	for i, run := range []string{"dry run", "real run"} {
		inv := g.onlyInvoice(i)
		subs, _ := inv["tax_subtotals"].([]any)
		if len(subs) != 1 {
			t.Errorf("%s: tax_subtotals = %v, want exactly one (both lines are VAT at 7.50)", run, inv["tax_subtotals"])
			continue
		}
		s := subs[0].(map[string]any)
		checkWants(t, []nrsWant{
			{run + " tax_category", sv(strp(s["tax_category"])), "VAT"},
			{run + " tax_percent", num(s["tax_percent"]), "7.50"},
			{run + " taxable_amount", num(s["taxable_amount"]), "150.00"},
			{run + " tax_amount", num(s["tax_amount"]), "11.25"},
		})
	}
}

// violationsOf lists the stub-rule violations of one invoice as "rule_key@path=actual".
func listViolations(vs []invoice.Violation) []string {
	var out []string
	for _, v := range vs {
		if v.RuleKey == ruleHSList || v.RuleKey == ruleTaxList || v.RuleKey == ruleStateList {
			out = append(out, v.RuleKey+"@"+v.Path+"="+sv(v.Actual))
		}
	}
	slices.Sort(out)
	return out
}

// Rows 2-3 are one invoice. Line 1 has a non-member tax category, line 2 a non-member HS code, the buyer a non-member state.
func nrsViolatingRows(num string) []map[string]string {
	rows := nrsCanonicalRows(num)
	rows[0]["line_tax_category"] = "XX"
	rows[1]["line_hsn_code"] = "9999.99"
	for _, r := range rows {
		r["buyer_state"] = "ZZ"
	}
	return rows
}

var wantListViolations = []string{
	ruleStateList + "@buyer.state=ZZ",
	ruleHSList + "@line_items[2].hsn_code=9999.99",
	ruleTaxList + "@line_items[1].tax_category=XX",
}

func TestImportNRS_ADryRunReportsListViolationsPerRow(t *testing.T) {
	g := newNRSGateRun(t, nrsListRules())
	res := g.do(true, nrsViolatingRows("LST-1")...)
	if res.ReadyInvoices != 1 || len(res.Errors) != 0 {
		t.Fatalf("ready = %d, errors = %+v", res.ReadyInvoices, res.Errors)
	}
	var found *InvoiceViolations
	for i := range res.InvoiceViolations {
		if res.InvoiceViolations[i].InvoiceNumber == "LST-1" {
			found = &res.InvoiceViolations[i]
		}
	}
	if found == nil {
		t.Fatalf("invoice_violations = %+v, want an entry for LST-1", res.InvoiceViolations)
	}
	if !slices.Equal(found.Rows, []int{2, 3}) {
		t.Errorf("rows = %v, want [2 3]", found.Rows)
	}
	if got := listViolations(found.Violations); !slices.Equal(got, wantListViolations) {
		t.Errorf("list violations = %v, want %v", got, wantListViolations)
	}
}

func TestImportNRS_TheRealRunStoresTheSameListViolations(t *testing.T) {
	g := newNRSGateRun(t, nrsListRules())
	res := g.do(false, nrsViolatingRows("LST-2")...)
	if res.ReadyInvoices != 1 || len(res.Errors) != 0 {
		t.Fatalf("ready = %d, errors = %+v", res.ReadyInvoices, res.Errors)
	}
	id := invoiceIDByNumber(t, g.super, g.entityID, "LST-2")
	_, raw, _ := readInvoiceVerdict(t, g.super, id)
	var stored []invoice.Violation
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("decode stored violations %s: %v", raw, err)
	}
	if got := listViolations(stored); !slices.Equal(got, wantListViolations) {
		t.Errorf("stored list violations = %v, want %v", got, wantListViolations)
	}
}

func TestImportNRS_AListMemberPasses(t *testing.T) {
	g := newNRSGateRun(t, nrsListRules())
	res := g.do(true, nrsCanonicalRows("LST-OK")...)
	if res.ReadyInvoices != 1 {
		t.Fatalf("ready = %d, errors = %+v", res.ReadyInvoices, res.Errors)
	}
	// Positive: the codes reached the rule engine, so a pass is a verdict, not an absence.
	inv := g.onlyInvoice(0)
	if sv(strp(nested(inv, "buyer", "state"))) != "LA" || sv(strp(lineOf(t, inv, 0)["hsn_code"])) != "8471.30" || sv(strp(lineOf(t, inv, 0)["tax_category"])) != "VAT" {
		t.Fatalf("payload lacks the list codes: buyer %v line 1 %v", inv["buyer"], lineOf(t, inv, 0))
	}
	for _, iv := range res.InvoiceViolations {
		if got := listViolations(iv.Violations); len(got) != 0 {
			t.Errorf("%s: list violations = %v, want none", iv.InvoiceNumber, got)
		}
	}
}
