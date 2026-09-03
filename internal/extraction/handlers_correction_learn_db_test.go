// handlers_correction_learn_db_test.go: what a POINTED correction teaches. Shares
// handlers_correction_db_test.go's cx* harness and store_db_test.go's pools, so this file adds
// no second skip site.
//
// Helpers use a cl* prefix.
package extraction_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	// The layout every learning case rides. Its fingerprint and its 7 anchors are read from the
	// real corpus PDF, never retyped.
	clCorpus = "corpus_two_column.pdf"

	// The token a reviewer points at, and the value they post. LearnRule derives same_token on
	// the "TIN" anchor here, and that rule resolves -- the "Buyer" rule below does not.
	clTINToken = "TIN: 99999999-0402"
	clTINValue = "99999999-0402"
	// The extractor's own rank-0 reading, distinct from clTINValue so an undo is observable.
	clReadingTIN = "99999999-0401"

	// Pointing at the buyer NAME derives a below/"Buyer" rule instead.
	clBuyerToken = "Honeywell Group"

	// The label the server derives for each of the two boxes.
	clTINAnchor   = "TIN"
	clBuyerAnchor = "Buyer"

	// A client-supplied anchor_label that must lose to the server's own.
	clHostileLabel = "Nonsense"

	clField = "buyer_tin"
)

// clEmptyCorner is a normalised box in the page's empty lower right. Measured: LearnRule
// refuses it, so the correction commits and teaches nothing.
var clEmptyCorner = extraction.Region{Page: 1, X0: 0.90, Y0: 0.90, X1: 0.98, Y1: 0.98}

// clPages reads the corpus once through the real reader.
func clPages(t *testing.T) []extraction.TokenPage {
	t.Helper()
	return rvCorpusPages(t, clCorpus)
}

// clLayout stamps the corpus layout onto an existing job as the SUPERUSER, in the same bytes
// the worker writes, and returns the fingerprint and the pages.
func clLayout(t *testing.T, ctx context.Context, jobID string) (string, []extraction.TokenPage) {
	t.Helper()
	pages := clPages(t)
	raw, err := extraction.MarshalAnchorObservations(extraction.AnchorObservations(pages))
	if err != nil {
		t.Fatalf("marshal the corpus anchors: %v", err)
	}
	fp := extraction.Fingerprint(pages)
	clLayoutRaw(t, ctx, jobID, fp, raw)
	return fp, pages
}

// clLayoutRaw stamps a fingerprint with caller-chosen anchor bytes: nil for SQL NULL, []byte("[]")
// for an empty array. The state goes to succeeded because a job carrying a layout has been read.
func clLayoutRaw(t *testing.T, ctx context.Context, jobID, fingerprint string, anchors []byte) {
	t.Helper()
	var arg any
	if anchors != nil {
		arg = string(anchors)
	}
	if _, err := stRequire(t).super.Exec(ctx,
		`UPDATE extraction_jobs SET layout_fingerprint = $1, layout_anchors = $2, state = 'succeeded'
		  WHERE id = $3`, fingerprint, arg, jobID); err != nil {
		t.Fatalf("stamp the layout on job %s: %v", jobID, err)
	}
}

// clJobFingerprint re-reads the stored fingerprint, so AC-1 compares the rule against the job's
// own column rather than against a retyped constant.
func clJobFingerprint(t *testing.T, ctx context.Context, jobID string) string {
	t.Helper()
	var fp *string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT layout_fingerprint FROM extraction_jobs WHERE id = $1`, jobID).Scan(&fp); err != nil {
		t.Fatalf("read layout_fingerprint for job %s: %v", jobID, err)
	}
	if fp == nil {
		t.Fatalf("job %s carries no layout_fingerprint", jobID)
	}
	return *fp
}

// clRule is one stored anchor rule.
type clRule struct {
	id, fingerprint, field, body string
	version                      int
}

// clRules reads a tenant's rules as the SUPERUSER, newest first. An app-pool read is RLS-scoped
// and would read the same whether or not a row was written.
func clRules(t *testing.T, ctx context.Context, tenantID string) []clRule {
	t.Helper()
	rows, err := stRequire(t).super.Query(ctx,
		`SELECT id::text, layout_fingerprint, field_name, rule::text, rule_schema_version
		   FROM extraction_anchor_rules WHERE tenant_id = $1 ORDER BY seq DESC`, tenantID)
	if err != nil {
		t.Fatalf("read anchor rules for tenant %s: %v", tenantID, err)
	}
	defer rows.Close()

	out := []clRule{}
	for rows.Next() {
		var r clRule
		if err := rows.Scan(&r.id, &r.fingerprint, &r.field, &r.body, &r.version); err != nil {
			t.Fatalf("scan anchor rule: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read anchor rules for tenant %s: %v", tenantID, err)
	}
	return out
}

// clAnchorLabel reads the newest correction's anchor_label for one job and field.
func clAnchorLabel(t *testing.T, ctx context.Context, jobID, field string) *string {
	t.Helper()
	var label *string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT anchor_label FROM extraction_field_corrections
		  WHERE extraction_job_id = $1 AND field_name = $2 ORDER BY seq DESC LIMIT 1`,
		jobID, field).Scan(&label); err != nil {
		t.Fatalf("read anchor_label for job %s field %s: %v", jobID, field, err)
	}
	return label
}

func clShowLabel(p *string) string {
	if p == nil {
		return "<null>"
	}
	return `"` + *p + `"`
}

// clWireRegion renders one box the way the SPA spells it.
func clWireRegion(r extraction.Region) string {
	return fmt.Sprintf(`"region":{"page":%d,"x0":%v,"y0":%v,"x1":%v,"y1":%v}`, r.Page, r.X0, r.Y0, r.X1, r.Y1)
}

// clPointedBody is one pointed correction body. label "" omits the key entirely.
func clPointedBody(value string, r extraction.Region, label string) string {
	extra := clWireRegion(r)
	if label != "" {
		extra += `,"anchor_label":` + corQuote(label)
	}
	return corBody(value, "pointed", extra)
}

// clTokenRegion is the box of the one page-1 token with this text.
func clTokenRegion(t *testing.T, pages []extraction.TokenPage, text string) extraction.Region {
	t.Helper()
	return rvTokenByText(t, pages, text).Region
}

// clFailAnchorRuleWrites arms a BEFORE INSERT trigger on extraction_anchor_rules keyed to this
// test's own tenant, so the SHIPPED insert fails and no concurrent test can see it. The returned
// func drops it early for the control arm; the same drop runs in t.Cleanup even on a panic.
func clFailAnchorRuleWrites(t *testing.T, ctx context.Context, tenantID string) func() {
	t.Helper()
	h := stRequire(t)
	name := "cl_anchor_rule_fail_" + strings.ReplaceAll(uuid.NewString(), "-", "")

	if _, err := h.super.Exec(ctx, fmt.Sprintf(
		`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN RAISE EXCEPTION 'forced anchor-rule failure' USING ERRCODE = 'check_violation'; END; $$`,
		name)); err != nil {
		t.Fatalf("create the forced-failure function: %v", err)
	}
	dropped := false
	drop := func() {
		if dropped {
			return
		}
		dropped = true
		if _, err := h.super.Exec(context.Background(),
			fmt.Sprintf(`DROP FUNCTION IF EXISTS %s() CASCADE`, name)); err != nil {
			t.Errorf("drop the forced-failure function: %v", err)
		}
	}
	t.Cleanup(drop)

	if _, err := h.super.Exec(ctx, fmt.Sprintf(
		`CREATE TRIGGER %s BEFORE INSERT ON extraction_anchor_rules FOR EACH ROW
		 WHEN (NEW.tenant_id = '%s'::uuid) EXECUTE FUNCTION %s()`,
		name, tenantID, name)); err != nil {
		t.Fatalf("create the forced-failure trigger: %v", err)
	}
	return drop
}

// clFixture is a tenant with an active membership, a document, a job, an entity and one draft
// invoice whose buyer_tin starts at clReadingTIN.
type clFixture struct {
	reqCtx     context.Context
	tenantID   string
	documentID string
	jobID      string
	invoiceID  string
}

func clSeed(t *testing.T, ctx context.Context, number string) clFixture {
	t.Helper()
	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, number, "draft")
	if _, err := stRequire(t).super.Exec(ctx,
		`UPDATE invoices SET buyer_tin = $1 WHERE id = $2`, clReadingTIN, invoiceID); err != nil {
		t.Fatalf("seed the invoice buyer_tin: %v", err)
	}
	return clFixture{reqCtx: reqCtx, tenantID: tenantID, documentID: documentID, jobID: jobID, invoiceID: invoiceID}
}

// --- C-01 / AC-1: one rule, keyed to the job's own fingerprint ---------------------------

func TestRLS_PointedCorrectionWritesOneAnchorRuleKeyedToTheJobsFingerprint(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-06-C01")
	_, pages := clLayout(t, ctx, f.jobID)
	region := clTokenRegion(t, pages, clTINToken)

	w := cxServe(t, f.reqCtx, f.jobID, clField, clPointedBody(clTINValue, region, ""),
		cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}

	rules := clRules(t, ctx, f.tenantID)
	if len(rules) != 1 {
		t.Fatalf("a pointed correction on a layout-bearing job left %d anchor rule(s), want exactly 1", len(rules))
	}
	r := rules[0]
	if want := clJobFingerprint(t, ctx, f.jobID); r.fingerprint != want {
		t.Errorf("the rule is keyed to fingerprint %q, want the job's own stored %q -- a rule under another key can never be read back for this layout",
			r.fingerprint, want)
	}
	if r.field != clField {
		t.Errorf("the rule names field %q, want %q", r.field, clField)
	}
	if r.version != extraction.RuleSchemaVersion {
		t.Errorf("the rule carries schema version %d, want %d -- AnchorRulesFor errors on any other", r.version, extraction.RuleSchemaVersion)
	}

	// The control on the SAME job: a typed correction teaches nothing, so the count above is
	// discriminating and not "this route writes a rule for every correction".
	w2 := cxServe(t, f.reqCtx, f.jobID, clField, corBody(clTINValue, "typed", ""),
		cxApplier(false, nil), cxAuditor(nil))
	if w2.Code != http.StatusCreated {
		t.Fatalf("control: the typed correction answered %d (body=%q), want 201", w2.Code, w2.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("after a typed correction on the same job the tenant holds %d rule(s), want the 1 the pointed correction wrote", n)
	}
}

// --- C-02 / AC-5: the label the server derived reaches the row and the screen -------------

// The reader-side half -- corrected.where carrying the stored label, and null without one -- is
// already pinned by TestExtractionDetail_WhereCarriesTheAnchorLabelAndIsNullWithoutOne. What is
// new here is that the label the SERVER derived is what lands in the column.
func TestRLS_PointedCorrectionStoresTheServerDerivedAnchorLabel(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-06-C02")
	_, pages := clLayout(t, ctx, f.jobID)
	region := clTokenRegion(t, pages, clBuyerToken)

	w := cxServe(t, f.reqCtx, f.jobID, clField, clPointedBody(clTINValue, region, ""),
		cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}

	label := clAnchorLabel(t, ctx, f.jobID, clField)
	if label == nil || *label != clBuyerAnchor {
		t.Fatalf("the stored anchor_label is %s, want the server-derived %q -- the client sent none, so nothing else can fill it",
			clShowLabel(label), clBuyerAnchor)
	}

	got, err := rdReader(t).Detail(f.reqCtx, f.jobID)
	if err != nil {
		t.Fatalf("Detail for job %s: %v", f.jobID, err)
	}
	field := rvcField(t, got, clField)
	if field.Corrected == nil {
		t.Fatalf("the detail carries no corrected block for %s", clField)
	}
	if field.Corrected.Where == nil || *field.Corrected.Where != clBuyerAnchor {
		t.Errorf("the detail renders corrected.where = %s, want %q -- the screen tells the reviewer where the value was taken from",
			clShowLabel(field.Corrected.Where), clBuyerAnchor)
	}
}

// --- C-03 / AC-5: the client's label never beats the server's ------------------------------

func TestRLS_AClientSuppliedAnchorLabelNeverBeatsTheServersOwn(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-06-C03")
	_, pages := clLayout(t, ctx, f.jobID)
	region := clTokenRegion(t, pages, clBuyerToken)

	w := cxServe(t, f.reqCtx, f.jobID, clField, clPointedBody(clTINValue, region, clHostileLabel),
		cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}
	if label := clAnchorLabel(t, ctx, f.jobID, clField); label == nil || *label != clBuyerAnchor {
		t.Errorf("the stored anchor_label is %s, want the server-derived %q -- a caller must not be able to write the provenance line",
			clShowLabel(label), clBuyerAnchor)
	}

	// The paired fallback: on a job with NO layout the server derives nothing, and the client's
	// value still stands. Without this, "the server wins" reads as "the client's value is always
	// dropped".
	f2 := clSeed(t, ctx, "EXTR14-06-C03-NOLAYOUT")
	w2 := cxServe(t, f2.reqCtx, f2.jobID, clField, clPointedBody(clTINValue, region, clHostileLabel),
		cxApplier(false, nil), cxAuditor(nil))
	if w2.Code != http.StatusCreated {
		t.Fatalf("control: the layout-less job answered %d (body=%q), want 201", w2.Code, w2.Body.String())
	}
	if label := clAnchorLabel(t, ctx, f2.jobID, clField); label == nil || *label != clHostileLabel {
		t.Errorf("on a job with no layout the stored anchor_label is %s, want the client's own %q -- D-5 replaces it only when the server derived one",
			clShowLabel(label), clHostileLabel)
	}
}

// --- C-04 / C-05 / C-06 / AC-2: only a pointed correction learns --------------------------

// One layout-bearing job, so a zero count is a refusal to learn rather than a job that could
// never teach anything. The pointed control at the end is what makes the three zeros mean
// something. Undo's full invoice semantics stay owned by
// TestRLS_UndoAppliesTheExtractorsReadingNotThePostedValue.
func TestRLS_OnlyAPointedCorrectionLearnsARule(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-06-C0456")
	_, pages := clLayout(t, ctx, f.jobID)
	cxReading(t, ctx, f.tenantID, f.jobID, clField, 0, cxStr(clReadingTIN))

	var seen cxSeamCall
	for _, method := range []string{"typed", "chosen", "undone"} {
		w := cxServe(t, f.reqCtx, f.jobID, clField, corBody(clTINValue, method, ""),
			cxClearingApplier(&seen), cxAuditor(nil))
		if w.Code != http.StatusCreated {
			t.Fatalf("a %q correction answered %d (body=%q), want 201", method, w.Code, w.Body.String())
		}
		if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
			t.Errorf("a %q correction left %d anchor rule(s), want 0 -- only a box a human drew says where a value lives", method, n)
		}
	}

	// The undo still resets the register to the extractor's own reading: a handler that stopped
	// short of the invoice write would satisfy the zero counts above.
	if got := cxBuyerTIN(t, ctx, f.invoiceID); got == nil || *got != clReadingTIN {
		t.Errorf("invoices.buyer_tin = %s after the undo, want the extractor's own %q", cxShowValue(got), clReadingTIN)
	}

	region := clTokenRegion(t, pages, clTINToken)
	w := cxServe(t, f.reqCtx, f.jobID, clField, clPointedBody(clTINValue, region, ""),
		cxClearingApplier(&seen), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("control: the pointed correction answered %d (body=%q), want 201", w.Code, w.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: the pointed correction on the same job left %d anchor rule(s), want 1 -- the three zeros above prove nothing without it", n)
	}
}

// --- C-07 / AC-3: a box that anchors to nothing commits and teaches nothing ---------------

func TestRLS_APointedCorrectionThatAnchorsToNothingCommitsWithoutARule(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-06-C07")
	_, pages := clLayout(t, ctx, f.jobID)

	w := cxServe(t, f.reqCtx, f.jobID, clField, clPointedBody(clTINValue, clEmptyCorner, clHostileLabel),
		cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d -- a box that relates to no anchor is an honest refusal to learn, not a refused request (body=%q)",
			w.Code, http.StatusCreated, w.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("a box in the page's empty corner left %d anchor rule(s), want 0", n)
	}
	if n := cxCorrectionRows(t, ctx, f.jobID); n != 1 {
		t.Errorf("%d correction row(s) after a box that taught nothing, want 1 -- the human action is still recorded", n)
	}
	if label := clAnchorLabel(t, ctx, f.jobID, clField); label == nil || *label != clHostileLabel {
		t.Errorf("the stored anchor_label is %s, want the client's own %q -- nothing was derived, so nothing overrides it",
			clShowLabel(label), clHostileLabel)
	}

	region := clTokenRegion(t, pages, clTINToken)
	w2 := cxServe(t, f.reqCtx, f.jobID, clField, clPointedBody(clTINValue, region, ""),
		cxApplier(false, nil), cxAuditor(nil))
	if w2.Code != http.StatusCreated {
		t.Fatalf("control: the anchored box answered %d (body=%q), want 201", w2.Code, w2.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: a box ON an anchor left %d rule(s), want 1 -- the zero above is then only a handler that never learns", n)
	}
}

// --- C-08 / AC-4: no recorded layout ------------------------------------------------------

func TestRLS_APointedCorrectionOnAJobWithNoLayoutCommitsWithoutARule(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-06-C08")
	pages := clPages(t)
	region := clTokenRegion(t, pages, clTINToken)

	w := cxServe(t, f.reqCtx, f.jobID, clField, clPointedBody(clTINValue, region, ""),
		cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("a job with no recorded layout left %d anchor rule(s), want 0 -- a rule under no fingerprint can never be read back", n)
	}
	if n := cxCorrectionRows(t, ctx, f.jobID); n != 1 {
		t.Errorf("%d correction row(s), want 1 -- the correction still commits", n)
	}

	clLayout(t, ctx, f.jobID)
	w2 := cxServe(t, f.reqCtx, f.jobID, clField, clPointedBody(clTINValue, region, ""),
		cxApplier(false, nil), cxAuditor(nil))
	if w2.Code != http.StatusCreated {
		t.Fatalf("control: the same job with a layout answered %d (body=%q), want 201", w2.Code, w2.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: the same job, same box, layout recorded, left %d rule(s), want 1", n)
	}
}

// --- C-08b / AC-4: a fingerprint with no anchors ------------------------------------------

// jobLayoutTx answers ok=true with an EMPTY slice for a job whose fingerprint is set and whose
// anchors are absent -- a different branch from the layout-less job above, and one nothing on
// this route reaches today. The two columns are independently nullable, so both spellings run.
func TestRLS_APointedCorrectionOnALayoutWithNoAnchorsCommitsWithoutARule(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-06-C08B")
	pages := clPages(t)
	region := clTokenRegion(t, pages, clTINToken)
	fingerprint := extraction.Fingerprint(pages)

	for _, tc := range []struct {
		name    string
		anchors []byte
	}{
		{"layout_anchors SQL NULL", nil},
		{"layout_anchors an empty array", []byte("[]")},
	} {
		clLayoutRaw(t, ctx, f.jobID, fingerprint, tc.anchors)

		w := cxServe(t, f.reqCtx, f.jobID, clField, clPointedBody(clTINValue, region, clHostileLabel),
			cxApplier(false, nil), cxAuditor(nil))
		if w.Code != http.StatusCreated {
			t.Fatalf("%s: status = %d, want %d (body=%q)", tc.name, w.Code, http.StatusCreated, w.Body.String())
		}
		if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
			t.Errorf("%s: left %d anchor rule(s), want 0 -- there is no anchor to name", tc.name, n)
		}
		// The label is what proves the derive branch was ENTERED and refused rather than skipped.
		if label := clAnchorLabel(t, ctx, f.jobID, clField); label == nil || *label != clHostileLabel {
			t.Errorf("%s: the stored anchor_label is %s, want the client's own %q", tc.name, clShowLabel(label), clHostileLabel)
		}
	}

	clLayout(t, ctx, f.jobID)
	w := cxServe(t, f.reqCtx, f.jobID, clField, clPointedBody(clTINValue, region, clHostileLabel),
		cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("control: the same job with real anchors answered %d (body=%q), want 201", w.Code, w.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: the same job, same box, anchors recorded, left %d rule(s), want 1 -- the two zeros above prove nothing without it", n)
	}
	if label := clAnchorLabel(t, ctx, f.jobID, clField); label == nil || *label != clTINAnchor {
		t.Errorf("control: the stored anchor_label is %s, want the server-derived %q", clShowLabel(label), clTINAnchor)
	}
}

// --- C-11 (rule half) / AC-7: a locked field teaches nothing ------------------------------

// The status and the message are already pinned by TestCorrectionHandler_InvoiceNumberIsRefusedWithAReason,
// TestCorrectionHandler_SupplierFieldsAreRefusedWithAReason and
// TestCorrectionHandler_LockedFieldRefusalPrecedesTheBodyDecode, which have no database. This is
// the half they cannot measure: a layout-bearing job and a valid pointed body, so nothing but
// the field lock stands between the request and a rule.
func TestRLS_ALockedFieldRefusalWritesNoAnchorRule(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-06-C11")
	_, pages := clLayout(t, ctx, f.jobID)
	region := clTokenRegion(t, pages, clTINToken)
	body := clPointedBody(clTINValue, region, "")

	for _, tc := range []struct {
		field string
		msg   string
	}{
		{"invoice_number", corMsgInvoiceNumber},
		{"supplier_tin", corMsgSupplierField},
		{"supplier_name", corMsgSupplierField},
	} {
		w := cxServe(t, f.reqCtx, f.jobID, tc.field, body, cxApplier(false, nil), cxAuditor(nil))
		hndAssert(t, w, http.StatusUnprocessableEntity, hndErrBody(t, tc.msg))
		if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
			t.Errorf("a refused %s correction left %d anchor rule(s), want 0", tc.field, n)
		}
	}

	w := cxServe(t, f.reqCtx, f.jobID, clField, body, cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("control: the unlocked field answered %d (body=%q), want 201", w.Code, w.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: an unlocked field on the same job and box left %d rule(s), want 1", n)
	}
}

// --- C-12 / AC-7: another tenant's job teaches nothing -------------------------------------

// TestRLS_CorrectionCrossTenantIsIndistinguishableFromAbsent owns the 404-equals-absent claim
// and stays untouched. This adds the rule half, with BOTH jobs carrying a layout so the zero
// counts are about the tenant boundary and not about a job that could never teach.
func TestRLS_APointedCorrectionOnAnotherTenantsJobWritesNoRule(t *testing.T) {
	ctx := t.Context()
	a := clSeed(t, ctx, "EXTR14-06-C12-A")
	b := clSeed(t, ctx, "EXTR14-06-C12-B")
	_, pages := clLayout(t, ctx, a.jobID)
	clLayout(t, ctx, b.jobID)
	region := clTokenRegion(t, pages, clTINToken)
	body := clPointedBody(clTINValue, region, "")

	cross := cxServe(t, a.reqCtx, b.jobID, clField, body, cxApplier(false, nil), cxAuditor(nil))
	if cross.Code != http.StatusNotFound {
		t.Fatalf("A posting to B's job answered %d (body=%q), want 404", cross.Code, cross.Body.String())
	}
	for _, tc := range []struct {
		who      string
		tenantID string
	}{{"A", a.tenantID}, {"B", b.tenantID}} {
		if n := len(clRules(t, ctx, tc.tenantID)); n != 0 {
			t.Errorf("tenant %s holds %d anchor rule(s) after a cross-tenant POST, want 0 -- a rule learned on another firm's layout is that firm's document leaking",
				tc.who, n)
		}
	}

	own := cxServe(t, a.reqCtx, a.jobID, clField, body, cxApplier(false, nil), cxAuditor(nil))
	if own.Code != http.StatusCreated {
		t.Fatalf("control: A posting to its OWN job answered %d (body=%q), want 201", own.Code, own.Body.String())
	}
	if n := len(clRules(t, ctx, a.tenantID)); n != 1 {
		t.Errorf("control: A's own job left %d rule(s), want 1 -- the zeros above are then only a handler that never learns", n)
	}
	if n := len(clRules(t, ctx, b.tenantID)); n != 0 {
		t.Errorf("tenant B holds %d anchor rule(s) after A corrected A's own job, want 0", n)
	}
}

// --- C-10 / AC-6: a failed rule write rolls back everything -------------------------------

// The failure is forced by a BEFORE INSERT trigger keyed to this test's own tenant, so it fails
// the SHIPPED insert, needs no production seam, and no concurrent test can see it. The invoice
// seam WRITES before the failure, so the unchanged column below is a rollback and not a seam
// that never touched a row.
func TestRLS_AFailedAnchorRuleWriteRollsBackTheCorrectionTheInvoiceAndTheAudit(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-06-C10")
	_, pages := clLayout(t, ctx, f.jobID)
	region := clTokenRegion(t, pages, clTINToken)
	body := clPointedBody(clTINValue, region, "")

	drop := clFailAnchorRuleWrites(t, ctx, f.tenantID)

	var seen cxSeamCall
	w := cxServe(t, f.reqCtx, f.jobID, clField, body, cxClearingApplier(&seen), cxAuditor(nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d for a failed anchor-rule write (body=%q)", w.Code, http.StatusInternalServerError, w.Body.String())
	}
	if seen.calls != 1 {
		t.Fatalf("the invoice seam ran %d time(s), want 1 -- the unchanged column below would then prove nothing", seen.calls)
	}
	if n := cxCorrectionRows(t, ctx, f.jobID); n != 0 {
		t.Errorf("%d correction row(s) survived a failed rule write, want 0", n)
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("%d anchor rule(s) survived their own failed write, want 0", n)
	}
	if got := cxBuyerTIN(t, ctx, f.invoiceID); got == nil || *got != clReadingTIN {
		t.Errorf("invoices.buyer_tin = %s after a failed rule write, want the unchanged %q -- the register kept a value no correction row explains",
			cxShowValue(got), clReadingTIN)
	}
	if rows := cxCorrectionAudit(t, ctx, f.tenantID); len(rows) != 0 {
		t.Errorf("%d %s audit row(s) survived a failed rule write, want 0", len(rows), cxEvent)
	}

	drop()
	w2 := cxServe(t, f.reqCtx, f.jobID, clField, body, cxClearingApplier(&seen), cxAuditor(nil))
	if w2.Code != http.StatusCreated {
		t.Fatalf("control: with the trigger dropped the same request answered %d (body=%q), want 201", w2.Code, w2.Body.String())
	}
	if n := cxCorrectionRows(t, ctx, f.jobID); n != 1 {
		t.Errorf("control: %d correction row(s), want 1 -- the four zeros above prove nothing without this", n)
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: %d anchor rule(s), want 1", n)
	}
	if got := cxBuyerTIN(t, ctx, f.invoiceID); got == nil || *got != clTINValue {
		t.Errorf("control: invoices.buyer_tin = %s, want the posted %q", cxShowValue(got), clTINValue)
	}
	if rows := cxCorrectionAudit(t, ctx, f.tenantID); len(rows) != 1 {
		t.Errorf("control: %d %s audit row(s), want 1", len(rows), cxEvent)
	}
}

// --- C-13 / AC-8: an undo does not un-teach ------------------------------------------------

// D-17. Three POSTs on one field, each asserted before the next runs. Arm 1 proves a rule was
// learned, so arm 2's "R1 is still there" is not vacuous; arm 3 proves a POINTED correction DOES
// prepend a superseding rule, so arm 2's non-supersession is a decision and not an inability of
// the write path. R1 is the same_token/TIN rule because the below/Buyer rule resolves to zero
// candidates on its own page, which would make arm 2's Resolve oracle vacuous.
func TestRLS_AnUndoDoesNotUnteachAndOnlyAPointedCorrectionSupersedes(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-06-C13")
	fingerprint, pages := clLayout(t, ctx, f.jobID)
	r1Region := clTokenRegion(t, pages, clTINToken)
	r2Region := clTokenRegion(t, pages, clBuyerToken)
	store := stStore(t)

	// resolved returns the buyer_tin candidates the tenant's stored rules produce on this page.
	resolved := func(t *testing.T) []extraction.Candidate {
		t.Helper()
		learned, err := store.AnchorRulesFor(ctx, f.tenantID, fingerprint)
		if err != nil {
			t.Fatalf("AnchorRulesFor: %v", err)
		}
		var out []extraction.Candidate
		for _, c := range extraction.Resolve(pages, extraction.RuleSet{Learned: learned}) {
			if c.Field == clField {
				out = append(out, c)
			}
		}
		return out
	}

	// Arm 1: the pointed correction writes R1.
	w := cxServe(t, f.reqCtx, f.jobID, clField, clPointedBody(clTINValue, r1Region, ""),
		cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("arm 1: status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}
	rules := clRules(t, ctx, f.tenantID)
	if len(rules) != 1 {
		t.Fatalf("arm 1: %d anchor rule(s), want exactly 1 -- every arm below reads against this one", len(rules))
	}
	r1 := rules[0]
	if r1.fingerprint != clJobFingerprint(t, ctx, f.jobID) {
		t.Fatalf("arm 1: R1 is keyed to %q, want the job's own stored fingerprint", r1.fingerprint)
	}
	cands := resolved(t)
	if len(cands) != 2 {
		t.Fatalf("arm 1: R1 resolves to %d %s candidate(s), want 2 -- a rule that fires nowhere cannot show an undo left it live", len(cands), clField)
	}
	for _, c := range cands {
		if c.RuleID != r1.id || c.Tier != extraction.TierLearned {
			t.Fatalf("arm 1: a candidate came from rule %q at tier %v, want %q at TierLearned", c.RuleID, c.Tier, r1.id)
		}
	}

	// Arm 2: the undo writes no rule and withdraws none.
	w = cxServe(t, f.reqCtx, f.jobID, clField, corBody(clTINValue, "undone", ""),
		cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("arm 2: status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}
	after := clRules(t, ctx, f.tenantID)
	if len(after) != 1 || after[0].id != r1.id {
		t.Fatalf("arm 2: after an undo the tenant holds %d rule(s) (first id %s), want the same 1 row %s -- an undo revises one field's value, it does not un-teach where the field lives",
			len(after), clFirstID(after), r1.id)
	}
	if after[0].body != r1.body {
		t.Errorf("arm 2: R1's body changed to %s, want the unchanged %s", after[0].body, r1.body)
	}
	cands = resolved(t)
	if len(cands) != 2 {
		t.Errorf("arm 2: R1 resolves to %d candidate(s) after the undo, want the same 2 -- the rule must still FIRE, not merely still exist", len(cands))
	}
	for _, c := range cands {
		if c.RuleID != r1.id {
			t.Errorf("arm 2: a candidate came from rule %q, want R1 %q", c.RuleID, r1.id)
		}
	}
	if n := clCorrectionsByMethod(t, ctx, f.jobID, "undone"); n != 1 {
		t.Errorf("arm 2: %d undone correction row(s), want 1 -- the human action is still recorded", n)
	}

	// Arm 3: a second POINTED correction is what supersedes.
	w = cxServe(t, f.reqCtx, f.jobID, clField, clPointedBody(clTINValue, r2Region, ""),
		cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("arm 3: status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}
	final := clRules(t, ctx, f.tenantID)
	if len(final) != 2 {
		t.Fatalf("arm 3: %d anchor rule(s) after a second pointed correction, want 2 -- the table is append-only", len(final))
	}
	if final[0].id == r1.id || final[1].id != r1.id {
		t.Errorf("arm 3: the rules read back as [%s %s], want the new one ahead of R1 %s -- newest first is what makes a later correction supersede an earlier one",
			final[0].id, final[1].id, r1.id)
	}
	if final[0].body == final[1].body {
		t.Errorf("arm 3: both rules carry the same body %s, so \"two rows\" cannot tell a superseding rule from a duplicate insert", final[0].body)
	}
	// C-09: both rows are keyed to the one fingerprint, so both are readable for this layout.
	for _, r := range final {
		if r.fingerprint != fingerprint {
			t.Errorf("arm 3: a rule is keyed to fingerprint %q, want the job's own %q", r.fingerprint, fingerprint)
		}
	}
}

func clFirstID(rules []clRule) string {
	if len(rules) == 0 {
		return "<none>"
	}
	return rules[0].id
}

// clCorrectionsByMethod counts one job's correction rows written with one method.
func clCorrectionsByMethod(t *testing.T, ctx context.Context, jobID, method string) int {
	t.Helper()
	var n int
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT count(*) FROM extraction_field_corrections WHERE extraction_job_id = $1 AND method = $2`,
		jobID, method).Scan(&n); err != nil {
		t.Fatalf("count %s corrections for job %s: %v", method, jobID, err)
	}
	return n
}
