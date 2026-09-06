// handlers_correction_learn_db_test.go: what a correction teaches -- pointed on a v1: layout,
// typed on a b1: one (EXTR-19-08, the block at "what a TYPED correction teaches"). Shares
// handlers_correction_db_test.go's cx* harness and store_db_test.go's pools, so this file adds
// no second skip site.
//
// Helpers use a cl* prefix.
package extraction_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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

	// The control on the SAME job: a typed correction teaches nothing HERE -- this job carries
	// a v1: key and layout_tokens NULL, so the boxless branch refuses twice over -- and the
	// count above is discriminating rather than "this route writes a rule for every
	// correction". TestRLS_ATypedCorrectionOnABoxlessJobLearnsARule is the b1: case.
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

// --- C-04 / C-05 / C-06 / AC-2: on a v1: layout, only a pointed correction learns ----------

// One layout-bearing job, so a zero count is a refusal to learn rather than a job that could
// never teach anything. The pointed control at the end is what makes the three zeros mean
// something. Undo's full invoice semantics stay owned by
// TestRLS_UndoAppliesTheExtractorsReadingNotThePostedValue.
//
// Since EXTR-19-08 the typed arm's zero is specific to this fixture, not general: clLayout
// stamps a v1: key and leaves layout_tokens NULL, so two of the boxless branch's three
// conjuncts are false. On a b1: job a typed correction DOES teach --
// TestRLS_ATypedCorrectionOnABoxlessJobLearnsARule.
func TestRLS_OnAV1LayoutOnlyAPointedCorrectionLearnsARule(t *testing.T) {
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
func TestRLS_AnUndoDoesNotUnteachAndOnAV1LayoutOnlyAPointedCorrectionSupersedes(t *testing.T) {
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

// --- AC-6 (ordering) : the rule INSERT follows the correction row and its audit row -------

// clOrderingTrigger arms a BEFORE INSERT trigger on extraction_anchor_rules that inspects, from
// INSIDE the same transaction, whether this job's correction row and this tenant's audit row are
// already there. whenPresent=true raises if they are; false raises if they are not. SECURITY
// DEFINER so the probe reads past RLS rather than past the tenant GUC.
func clOrderingTrigger(t *testing.T, ctx context.Context, tenantID, jobID string, whenPresent bool) func() {
	t.Helper()
	h := stRequire(t)
	name := "cl_anchor_rule_order_" + strings.ReplaceAll(uuid.NewString(), "-", "")

	if _, err := h.super.Exec(ctx, fmt.Sprintf(
		`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER AS $$
		 DECLARE siblings boolean;
		 BEGIN
		   siblings := EXISTS (SELECT 1 FROM extraction_field_corrections
		                        WHERE extraction_job_id = '%s'::uuid)
		           AND EXISTS (SELECT 1 FROM audit_log
		                        WHERE tenant_id = '%s'::uuid AND event = '%s');
		   IF siblings = %t THEN
		     RAISE EXCEPTION 'ordering probe: siblings=%%', siblings USING ERRCODE = 'check_violation';
		   END IF;
		   RETURN NEW;
		 END; $$`, name, jobID, tenantID, cxEvent, whenPresent)); err != nil {
		t.Fatalf("create the ordering-probe function: %v", err)
	}
	dropped := false
	drop := func() {
		if dropped {
			return
		}
		dropped = true
		if _, err := h.super.Exec(context.Background(),
			fmt.Sprintf(`DROP FUNCTION IF EXISTS %s() CASCADE`, name)); err != nil {
			t.Errorf("drop the ordering-probe function: %v", err)
		}
	}
	t.Cleanup(drop)

	if _, err := h.super.Exec(ctx, fmt.Sprintf(
		`CREATE TRIGGER %s BEFORE INSERT ON extraction_anchor_rules FOR EACH ROW
		 WHEN (NEW.tenant_id = '%s'::uuid) EXECUTE FUNCTION %s()`,
		name, tenantID, name)); err != nil {
		t.Fatalf("create the ordering-probe trigger: %v", err)
	}
	return drop
}

// AC-6's rollback assertions only mean "all or nothing" if the rule INSERT fires with the other
// three writes already in the transaction. Reorder it to the front and every zero in
// TestRLS_AFailedAnchorRuleWriteRollsBackTheCorrectionTheInvoiceAndTheAudit still holds --
// vacuously, because those rows were never written. This is the oracle for the order.
//
// Both arms run the SAME request against the SAME fixture and must answer differently, so
// neither status is a trigger that simply always fires.
func TestRLS_TheAnchorRuleWriteFollowsTheCorrectionRowAndTheCorrectionAudit(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-06-ORDER")
	_, pages := clLayout(t, ctx, f.jobID)
	region := clTokenRegion(t, pages, clTINToken)
	body := clPointedBody(clTINValue, region, "")

	// Arm 1: raise only if the correction row and the audit row are ALREADY visible.
	var seen cxSeamCall
	dropA := clOrderingTrigger(t, ctx, f.tenantID, f.jobID, true)
	w := cxServe(t, f.reqCtx, f.jobID, clField, body, cxClearingApplier(&seen), cxAuditor(nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("arm 1: status = %d, want %d -- the rule INSERT did not see the correction row and the audit row, so it does not run last and AC-6's rollback zeros are vacuous (body=%q)",
			w.Code, http.StatusInternalServerError, w.Body.String())
	}
	if seen.calls != 1 {
		t.Fatalf("arm 1: the invoice seam ran %d time(s), want 1", seen.calls)
	}
	if n := cxCorrectionRows(t, ctx, f.jobID); n != 0 {
		t.Errorf("arm 1: %d correction row(s) survived, want 0", n)
	}
	dropA()

	// Arm 2: the inverse condition on the same request. It must COMMIT, which is what proves
	// arm 1's 500 came from the siblings being present rather than from any insert at all.
	dropB := clOrderingTrigger(t, ctx, f.tenantID, f.jobID, false)
	w2 := cxServe(t, f.reqCtx, f.jobID, clField, body, cxClearingApplier(&seen), cxAuditor(nil))
	if w2.Code != http.StatusCreated {
		t.Fatalf("arm 2: status = %d, want %d -- the inverse probe fired, so the rule INSERT ran BEFORE the correction row or the audit row (body=%q)",
			w2.Code, http.StatusCreated, w2.Body.String())
	}
	dropB()

	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("arm 2: %d anchor rule(s), want 1", n)
	}
	if n := cxCorrectionRows(t, ctx, f.jobID); n != 1 {
		t.Errorf("arm 2: %d correction row(s), want 1", n)
	}
	if rows := cxCorrectionAudit(t, ctx, f.tenantID); len(rows) != 1 {
		t.Errorf("arm 2: %d %s audit row(s), want 1", len(rows), cxEvent)
	}
}

// --- C-14 / EXTR-19-04 AC-9: a boxless layout teaches nothing ------------------------------

const (
	// A page-1 token of corpus_inline_labels.pdf, and the reading a reviewer posts over it.
	clDateToken = "Invoice Date: 2026-03-04"
	clDateValue = "2026-03-05"
	clDateField = "issue_date"
)

// clLearnRecorder counts anchor.learned emits. The seam, not audit_log: cxServe's default
// recorder writes nothing, so a table count could not tell "refused to learn" from "learned and
// the adapter is not wired here".
type clLearnRecorder struct {
	mu sync.Mutex
	n  int
}

func (r *clLearnRecorder) record(_ context.Context, tx pgx.Tx, _ string, _ extraction.AnchorLearned) error {
	if tx == nil {
		return errors.New("the learning recorder was handed no transaction, so the row cannot share the rule's fate")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
	return nil
}

func (r *clLearnRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// clSettle runs the real ExtractWorker once and returns the extraction job it settled, so the
// correction below reads the layout PRODUCTION writes rather than one this test stamped.
func clSettle(t *testing.T, ctx context.Context, tenantID, documentID string, riverJobID int64, ew *extraction.ExtractWorker) string {
	t.Helper()
	if err := ew.Work(ctx,
		extraction.NewExtractJobForTest(riverJobID, 1, 3, tenantID, documentID, uuid.NewString())); err != nil {
		t.Fatalf("Work over document %s (river job %d): %v", documentID, riverJobID, err)
	}
	jobID := wkExtractionJobID(t, ctx, tenantID, riverJobID)
	stAssertJobState(t, ctx, jobID, "succeeded")
	return jobID
}

// C-14. A DOCX job now carries a layout, so jobLayoutTx answers ok=true where it used to answer
// false -- a path nothing on this route reached before EXTR-19-04. It still teaches nothing:
// every boxless anchor carries the zero box and usableBox refuses it (resolve.go:277-291), so no
// candidate qualifies.
//
// The PDF arm runs FIRST and posts the SAME body against a job settled the SAME way, so the only
// variable between the two is which layout the job carries -- and so the control is proven even
// on a run where the boxless arm below fails.
func TestRLS_APointedCorrectionOnABoxlessJobLearnsNothing(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR19-04-C14")
	wkCleanupInfra(t, f.tenantID)

	// A real PDF token's box, posted unchanged on both arms. usableBox admits it, so a refusal
	// on the DOCX arm is about the ANCHORS and not about the region.
	pdfPages := rvCorpusPages(t, dcCorpusFixture)
	region := clTokenRegion(t, pdfPages, clDateToken)
	body := clPointedBody(clDateValue, region, "")

	// PDF control.
	pdfEW := wpWorker(t, wkOK(), wpCorpusOpener(t),
		wpDoclingReader(t, dcReadNamedGolden(t, dcCorpusGoldenName)), wkNoRules, &wkAuditRecorder{})
	pdfJobID := clSettle(t, ctx, f.tenantID, f.documentID, 915302, pdfEW)

	pdfLearned := &clLearnRecorder{}
	w := cxServe(t, f.reqCtx, pdfJobID, clDateField, body, cxApplier(false, nil), cxAuditor(nil), pdfLearned.record)
	if w.Code != http.StatusCreated {
		t.Fatalf("control: status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}
	rules := clRules(t, ctx, f.tenantID)
	if len(rules) != 1 {
		t.Fatalf("control: this body against a PDF layout left %d anchor rule(s), want 1 -- the boxless zero below proves nothing without it", len(rules))
	}
	if want := clJobFingerprint(t, ctx, pdfJobID); rules[0].fingerprint != want {
		t.Errorf("control: the rule is keyed to %q, want the PDF job's own %q", rules[0].fingerprint, want)
	}
	if n := pdfLearned.count(); n != 1 {
		t.Errorf("control: the PDF arm emitted %d anchor.learned event(s), want 1", n)
	}

	// DOCX arm, same tenant, same body. The counts below are deltas against the control's one
	// rule: clRules is tenant-scoped, so an absolute zero is unavailable here.
	docxEW := wkWorkerPages(t, wkOK(), wkDocxOpener(t), wkForbiddenPages(&wkForbiddenReader{}), &wkAuditRecorder{})
	docxEW.Text = wpDoclingReader(t, dcReadNamedGolden(t, dxGolden))
	docxEW.Rules = wkNoRules
	docxJobID := clSettle(t, ctx, f.tenantID, f.documentID, 915301, docxEW)

	if fp := clJobFingerprint(t, ctx, docxJobID); !strings.HasPrefix(fp, extraction.BoxlessFingerprintVersion+":") {
		t.Fatalf("the settled DOCX job carries layout_fingerprint %q, want the %q namespace -- AC-9 is about a job that HAS a boxless layout, so without one this test asserts nothing new",
			fp, extraction.BoxlessFingerprintVersion)
	}

	docxLearned := &clLearnRecorder{}
	w2 := cxServe(t, f.reqCtx, docxJobID, clDateField, body, cxApplier(false, nil), cxAuditor(nil), docxLearned.record)
	if w2.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w2.Code, http.StatusCreated, w2.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("after a pointed correction on a BOXLESS layout the tenant holds %d anchor rule(s), want the 1 the control wrote -- a rule keyed to an anchor with no geometry can never resolve", n)
	}
	if n := docxLearned.count(); n != 0 {
		t.Errorf("the boxless arm emitted %d anchor.learned event(s), want 0", n)
	}
	if n := cxCorrectionRows(t, ctx, docxJobID); n != 1 {
		t.Errorf("%d correction row(s) on the DOCX job, want 1 -- the correction still commits", n)
	}
}

// --- EXTR-19-08: what a TYPED correction teaches on a boxless document ---------------------
//
// Mode A. Red until the writeCorrection branch, IsBoxlessFingerprint and jobLayoutTokensTx
// land. Under the stubs the refusal arms below pass by construction -- the stub refuses every
// job -- so in every spec here it is the CONTROL that carries the red.

const (
	// The corrected field, and the value A's own page-1 Total token carries.
	//
	// vat, never total: extraction_field_results has no tier column and no rule_id column, so a
	// row can attribute to the learned rule only when the corrected field and the matched label
	// are paired with DIFFERENT tier1Specs entries. Measured over the committed goldens: with
	// the rule A-prime reads vat = 7150.00, without it vat is missing, and all ten other fields
	// are byte-identical. Correcting total instead is vacuous -- tier-1 already resolves it at
	// rank 0 with the same value.
	blField = "vat"
	blValue = "4300.00"

	// A value no page-1 token of A carries under any lexicon label. Measured:
	// LearnBoxlessRule refuses it.
	blUnderivable = "999.99"
)

// blSettleDocx runs the real ExtractWorker over A's committed golden and returns the
// extraction job it settled, so every spec below corrects the layout PRODUCTION wrote -- the
// b1: fingerprint and the layout_tokens together.
func blSettleDocx(t *testing.T, ctx context.Context, tenantID, documentID string, riverJobID int64) string {
	t.Helper()
	ew := wkBoxlessDocx(t, dcReadNamedGolden(t, dxGolden), wkNoRules, &wkAuditRecorder{})
	return clSettle(t, ctx, tenantID, documentID, riverJobID, ew)
}

// blSettlePdf settles the corpus PDF the same way, for the v1: arm.
func blSettlePdf(t *testing.T, ctx context.Context, tenantID, documentID string, riverJobID int64) string {
	t.Helper()
	ew := wpWorker(t, wkOK(), wpCorpusOpener(t),
		wpDoclingReader(t, dcReadNamedGolden(t, dcCorpusGoldenName)), wkNoRules, &wkAuditRecorder{})
	return clSettle(t, ctx, tenantID, documentID, riverJobID, ew)
}

// blTyped is one typed correction body, exactly the shape the SPA posts: no region, no label.
func blTyped(value string) string { return corBody(value, "typed", "") }

// blLearnRecorder captures the anchor.learned emits the seam receives. The seam, not
// audit_log: cxServe's default recorder writes no row, so a table count could not tell a
// refusal to learn from an adapter that is not wired here.
type blLearnRecorder struct {
	mu  sync.Mutex
	got []extraction.AnchorLearned
}

func (r *blLearnRecorder) record(_ context.Context, tx pgx.Tx, _ string, ev extraction.AnchorLearned) error {
	if tx == nil {
		return errors.New("the learning recorder was handed no transaction, so the row cannot share the rule's fate")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, ev)
	return nil
}

func (r *blLearnRecorder) events() []extraction.AnchorLearned {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}

// blBoxlessPremise asserts what makes every zero below attributable: the settled job really
// carries a b1: identity and really stored its page-1 tokens. It returns both.
func blBoxlessPremise(t *testing.T, ctx context.Context, jobID string) (string, []string) {
	t.Helper()
	fp := clJobFingerprint(t, ctx, jobID)
	if !strings.HasPrefix(fp, extraction.BoxlessFingerprintVersion+":") {
		t.Fatalf("the settled DOCX job carries layout_fingerprint %q, want the %q namespace -- every assertion below is about a job that HAS a boxless identity",
			fp, extraction.BoxlessFingerprintVersion)
	}
	toks := wtDecode(t, "the settled DOCX job", wtTokens(t, ctx, jobID))
	if len(toks) == 0 {
		t.Fatalf("the settled DOCX job stored %d page-1 token(s), want more than 0 -- a derivation over an empty token list refuses for a reason no spec here is about", len(toks))
	}
	return fp, toks
}

// blDerived is the body LearnBoxlessRule produces for field/value over toks, and a fatal when
// it refuses -- the premise every "one rule" control rests on.
func blDerived(t *testing.T, field, value string, toks []string) extraction.LearnedRule {
	t.Helper()
	lr, ok := extraction.LearnBoxlessRule(field, value, toks)
	if !ok {
		t.Fatalf("LearnBoxlessRule refused %s = %q over %v; this fixture cannot teach and the control below could never pass", field, value, toks)
	}
	return lr
}

// blSameRuleJSON compares a stored rule body against a derived one through their decoded
// forms: Postgres re-renders jsonb with its own spacing, so the bytes never match.
func blSameRuleJSON(t *testing.T, stored string, derived []byte) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal([]byte(stored), &a); err != nil {
		t.Fatalf("decode the stored rule %s: %v", stored, err)
	}
	if err := json.Unmarshal(derived, &b); err != nil {
		t.Fatalf("decode the derived rule %s: %v", derived, err)
	}
	return reflect.DeepEqual(a, b)
}

// blSetTokens overwrites one job's layout_tokens as the SUPERUSER. nil is SQL NULL.
func blSetTokens(t *testing.T, ctx context.Context, jobID string, raw *string) {
	t.Helper()
	var arg any
	if raw != nil {
		arg = *raw
	}
	if _, err := stRequire(t).super.Exec(ctx,
		`UPDATE extraction_jobs SET layout_tokens = $1 WHERE id = $2`, arg, jobID); err != nil {
		t.Fatalf("set layout_tokens on job %s: %v", jobID, err)
	}
}

// blSetFingerprint overwrites ONE column and nothing else -- AC-8's whole point.
func blSetFingerprint(t *testing.T, ctx context.Context, jobID, fingerprint string) {
	t.Helper()
	if _, err := stRequire(t).super.Exec(ctx,
		`UPDATE extraction_jobs SET layout_fingerprint = $1 WHERE id = $2`, fingerprint, jobID); err != nil {
		t.Fatalf("set layout_fingerprint on job %s: %v", jobID, err)
	}
}

// blJobRow is everything about a job that could change the branch's answer EXCEPT the
// fingerprint, so AC-8 can prove the fingerprint is the only variable between its two arms.
type blJobRow struct {
	anchors, tokens string
	state           string
	documentID      string
}

// Comparable by value: a pointer pair reads as unequal on every re-read.
func blJobExceptFingerprint(t *testing.T, ctx context.Context, jobID string) blJobRow {
	t.Helper()
	var anchors, tokens *string
	var r blJobRow
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT layout_anchors::text, layout_tokens::text, state, document_id::text
		   FROM extraction_jobs WHERE id = $1`, jobID).
		Scan(&anchors, &tokens, &r.state, &r.documentID); err != nil {
		t.Fatalf("read job %s: %v", jobID, err)
	}
	r.anchors, r.tokens = clShowLabel(anchors), clShowLabel(tokens)
	return r
}

// --- AC-1: a typed correction on a boxless job learns one rule -----------------------------

// Core AC-3 by the user's own path. The must-fail mutation stated for this spec -- gate on
// jobLayoutTx's ok alone and drop the IsBoxlessFingerprint conjunct -- leaves it GREEN, because
// this job is boxless in both senses. AC-3 and AC-8 are that mutation's real oracles; this
// spec's job is the positive: one rule, keyed to the job's own value, with one emit beside it.
func TestRLS_ATypedCorrectionOnABoxlessJobLearnsARule(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR19-08-AC1")
	wkCleanupInfra(t, f.tenantID)
	jobID := blSettleDocx(t, ctx, f.tenantID, f.documentID, 915801)
	fp, toks := blBoxlessPremise(t, ctx, jobID)
	want := blDerived(t, blField, blValue, toks)

	learned := &blLearnRecorder{}
	w := cxServe(t, f.reqCtx, jobID, blField, blTyped(blValue),
		cxApplier(false, nil), cxAuditor(nil), learned.record)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}

	rules := clRules(t, ctx, f.tenantID)
	if len(rules) != 1 {
		t.Fatalf("a typed correction on a boxless job left %d anchor rule(s), want exactly 1 -- this is the whole subtask", len(rules))
	}
	r := rules[0]
	if r.fingerprint != fp {
		t.Errorf("the rule is keyed to %q, want the job's own stored %q -- a rule under another key can never be read back for this layout", r.fingerprint, fp)
	}
	if !extraction.IsBoxlessFingerprint(r.fingerprint) {
		t.Errorf("IsBoxlessFingerprint(%q) is false on the key the rule was written under", r.fingerprint)
	}
	if r.field != blField {
		t.Errorf("the rule names field %q, want %q", r.field, blField)
	}
	if r.version != extraction.RuleSchemaVersion {
		t.Errorf("the rule carries schema version %d, want %d -- AnchorRulesFor errors on any other", r.version, extraction.RuleSchemaVersion)
	}
	if !blSameRuleJSON(t, r.body, want.Body) {
		t.Errorf("the stored rule is %s, want the derivation's own %s -- a body the handler invented is not what the next document will read", r.body, want.Body)
	}

	evs := learned.events()
	if len(evs) != 1 {
		t.Fatalf("the typed correction emitted %d anchor.learned event(s), want exactly 1", len(evs))
	}
	if evs[0].LayoutFingerprint != fp {
		t.Errorf("the emit carries layout_fingerprint %q, want the job's own %q", evs[0].LayoutFingerprint, fp)
	}
	if evs[0].RuleID != r.id {
		t.Errorf("the emit names rule %q, want the row that was written, %q", evs[0].RuleID, r.id)
	}
	if evs[0].FieldName != blField {
		t.Errorf("the emit names field %q, want %q", evs[0].FieldName, blField)
	}

	// B-2. The boxless branch writes no provenance line: reader.go turns any non-empty
	// anchor_label into corrected.where for ANY method, so writing one would put "Taken from
	// Total" on every typed DOCX correction. AC-9 carries this claim with its control.
	if label := clAnchorLabel(t, ctx, jobID, blField); label != nil && *label != "" {
		t.Errorf("the stored anchor_label is %s, want absent -- the client sent none and the boxless branch derives none", clShowLabel(label))
	}
}

// --- AC-3: a typed correction on a PDF job learns nothing ----------------------------------

// The PDF arm runs FIRST, so its count is an absolute zero rather than a delta. It varies six
// things at once against the control -- format, content type, tokens, anchors, fingerprint
// namespace and layout_tokens presence -- which is exactly why AC-8 sits beside it and varies
// one.
func TestRLS_ATypedCorrectionOnAPdfJobLearnsNothing(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR19-08-AC3")
	wkCleanupInfra(t, f.tenantID)

	pdfJobID := blSettlePdf(t, ctx, f.tenantID, f.documentID, 915802)
	pdfFP := clJobFingerprint(t, ctx, pdfJobID)
	if !strings.HasPrefix(pdfFP, extraction.FingerprintVersion+":") {
		t.Fatalf("the settled PDF job carries layout_fingerprint %q, want the %q namespace -- without a v1: key this spec asserts nothing about the namespace gate",
			pdfFP, extraction.FingerprintVersion)
	}

	learned := &blLearnRecorder{}
	w := cxServe(t, f.reqCtx, pdfJobID, blField, blTyped(blValue),
		cxApplier(false, nil), cxAuditor(nil), learned.record)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("a typed correction on a PDF job left %d anchor rule(s), want 0 -- the boxless derivation reads token text with no geometry and owns only the b1: namespace", n)
	}
	if n := len(learned.events()); n != 0 {
		t.Errorf("the PDF arm emitted %d anchor.learned event(s), want 0", n)
	}
	if n := cxCorrectionRows(t, ctx, pdfJobID); n != 1 {
		t.Errorf("%d correction row(s) on the PDF job, want 1 -- the human action is still recorded", n)
	}

	// The control: the SAME tenant, the SAME body, the SAME field, on a boxless job.
	docxJobID := blSettleDocx(t, ctx, f.tenantID, f.documentID, 915803)
	blBoxlessPremise(t, ctx, docxJobID)
	w2 := cxServe(t, f.reqCtx, docxJobID, blField, blTyped(blValue), cxApplier(false, nil), cxAuditor(nil))
	if w2.Code != http.StatusCreated {
		t.Fatalf("control: the boxless job answered %d (body=%q), want 201", w2.Code, w2.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Fatalf("control: the identical POST against a boxless job left %d anchor rule(s), want 1 -- the zero above is otherwise only a handler that never learns", n)
	}

	// QA, Mode B. The zero above is over-determined: measured, the PDF job also stores
	// layout_tokens SQL NULL, so dropping the IsBoxlessFingerprint conjunct leaves this spec
	// green. This arm removes that determinant -- the PDF job is handed the DOCX job's own
	// page-1 text, the text the control just taught from -- so only the v1: key is left to
	// refuse, and dropping the conjunct reds here.
	tokens := wtTokens(t, ctx, docxJobID)
	if tokens == nil {
		t.Fatalf("the boxless control job stored layout_tokens NULL, so the arm below would prove nothing")
	}
	if wtTokens(t, ctx, pdfJobID) != nil {
		t.Fatalf("the PDF job already stores layout_tokens, so the zero above was never over-determined and this arm is reading a changed premise")
	}
	blSetTokens(t, ctx, pdfJobID, tokens)
	if got := wtDecode(t, "the re-stamped PDF job", wtTokens(t, ctx, pdfJobID)); !slices.Equal(got, wtDecode(t, "the boxless control job", tokens)) {
		t.Fatalf("the PDF job carries %#v after the stamp, want the DOCX job's own text", got)
	}
	w3 := cxServe(t, f.reqCtx, pdfJobID, blField, blTyped(blValue), cxApplier(false, nil), cxAuditor(nil))
	if w3.Code != http.StatusCreated {
		t.Fatalf("the PDF job carrying derivable tokens answered %d (body=%q), want 201", w3.Code, w3.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("a typed correction on a v1:-keyed job carrying the very tokens that just taught left %d anchor rule(s), want the control's 1 -- the namespace is then not what refuses", n)
	}
}

// --- AC-4: an underivable typed correction records the correction only ---------------------

// The control is the SAME job, the SAME field and the SAME method, differing only in the VALUE:
// no page-1 token of A reads as 999.99 under any lexicon label, and every token that reads as
// 4300.00 does. That single variable is what makes the zero a refusal by the derivation rather
// than a branch that was never entered.
func TestRLS_AnUnderivableTypedCorrectionRecordsTheCorrectionOnly(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR19-08-AC4")
	wkCleanupInfra(t, f.tenantID)
	jobID := blSettleDocx(t, ctx, f.tenantID, f.documentID, 915804)
	_, toks := blBoxlessPremise(t, ctx, jobID)

	if _, ok := extraction.LearnBoxlessRule(blField, blUnderivable, toks); ok {
		t.Fatalf("LearnBoxlessRule DERIVES %s = %q over %v; this spec is about a value it refuses, and the zero below would be about something else", blField, blUnderivable, toks)
	}

	learned := &blLearnRecorder{}
	w := cxServe(t, f.reqCtx, jobID, blField, blTyped(blUnderivable),
		cxApplier(false, nil), cxAuditor(nil), learned.record)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d -- a value no token carries is an honest refusal to learn, not a refused request (body=%q)",
			w.Code, http.StatusCreated, w.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("an underivable typed correction left %d anchor rule(s), want 0", n)
	}
	if n := len(learned.events()); n != 0 {
		t.Errorf("an underivable typed correction emitted %d anchor.learned event(s), want 0", n)
	}
	if n := cxCorrectionRows(t, ctx, jobID); n != 1 {
		t.Errorf("%d correction row(s), want 1 -- the human action is still recorded", n)
	}

	blDerived(t, blField, blValue, toks)
	w2 := cxServe(t, f.reqCtx, jobID, blField, blTyped(blValue), cxApplier(false, nil), cxAuditor(nil))
	if w2.Code != http.StatusCreated {
		t.Fatalf("control: the derivable value answered %d (body=%q), want 201", w2.Code, w2.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: the same job, the same field, the same method and a DERIVABLE value left %d anchor rule(s), want 1 -- the zero above is otherwise free", n)
	}
	if n := cxCorrectionRows(t, ctx, jobID); n != 2 {
		t.Errorf("%d correction row(s) after both POSTs, want 2", n)
	}
}

// --- AC-5: only a typed correction teaches a boxless layout --------------------------------

// D-23. chosen and undone reach writeCorrection and must not enter the branch. The control is
// the same job and the same derivable value posted as typed, so the two zeros are the method
// gate and nothing else.
func TestRLS_OnlyATypedCorrectionTeachesABoxlessLayout(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR19-08-AC5")
	wkCleanupInfra(t, f.tenantID)
	jobID := blSettleDocx(t, ctx, f.tenantID, f.documentID, 915805)
	_, toks := blBoxlessPremise(t, ctx, jobID)
	blDerived(t, blField, blValue, toks)

	learned := &blLearnRecorder{}
	for _, method := range []string{"chosen", "undone"} {
		w := cxServe(t, f.reqCtx, jobID, blField, corBody(blValue, method, ""),
			cxApplier(false, nil), cxAuditor(nil), learned.record)
		if w.Code != http.StatusCreated {
			t.Fatalf("a %q correction answered %d (body=%q), want 201", method, w.Code, w.Body.String())
		}
		if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
			t.Errorf("a %q correction carrying the derivable value left %d anchor rule(s), want 0 -- only a value a human TYPED says what the field reads", method, n)
		}
	}
	if n := len(learned.events()); n != 0 {
		t.Errorf("the chosen and undone arms emitted %d anchor.learned event(s), want 0", n)
	}

	w := cxServe(t, f.reqCtx, jobID, blField, blTyped(blValue), cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("control: the typed correction answered %d (body=%q), want 201", w.Code, w.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: the same job and the same value posted as typed left %d anchor rule(s), want 1 -- the two zeros above prove nothing without it", n)
	}
}

// --- AC-6: a boxless job with no stored tokens learns nothing ------------------------------

// Every job settled by this branch stores its tokens, so both refusal arms set the column
// DIRECTLY, as a pre-migration row and an over-cap row respectively carry it. The control
// RESTORES the job's own bytes and re-posts: one column moves between the arms and the control,
// so the zeros are the reader and not the job.
func TestRLS_ABoxlessJobWithNoStoredTokensLearnsNothing(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR19-08-AC6")
	wkCleanupInfra(t, f.tenantID)
	jobID := blSettleDocx(t, ctx, f.tenantID, f.documentID, 915806)
	fp, toks := blBoxlessPremise(t, ctx, jobID)
	blDerived(t, blField, blValue, toks)
	stored := wtTokens(t, ctx, jobID)

	learned := &blLearnRecorder{}
	for _, tc := range []struct {
		name string
		raw  *string
	}{
		{"layout_tokens SQL NULL", nil},
		{"layout_tokens an empty array", stPtr("[]")},
	} {
		blSetTokens(t, ctx, jobID, tc.raw)
		w := cxServe(t, f.reqCtx, jobID, blField, blTyped(blValue),
			cxApplier(false, nil), cxAuditor(nil), learned.record)
		if w.Code != http.StatusCreated {
			t.Fatalf("%s: status = %d, want %d -- a job with nothing to derive from answers 201, never 500 (body=%q)",
				tc.name, w.Code, http.StatusCreated, w.Body.String())
		}
		if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
			t.Errorf("%s: left %d anchor rule(s), want 0 -- there is no token text to read a value out of", tc.name, n)
		}
		if got := clJobFingerprint(t, ctx, jobID); got != fp {
			t.Fatalf("%s: the job's fingerprint moved to %q from %q; the two arms would then differ in more than the token column", tc.name, got, fp)
		}
	}
	if n := len(learned.events()); n != 0 {
		t.Errorf("the tokenless arms emitted %d anchor.learned event(s), want 0", n)
	}

	blSetTokens(t, ctx, jobID, stored)
	w := cxServe(t, f.reqCtx, jobID, blField, blTyped(blValue), cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("control: the job with its own tokens back answered %d (body=%q), want 201", w.Code, w.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: the same job, the same body, layout_tokens restored, left %d anchor rule(s), want 1 -- the two zeros above prove nothing without it", n)
	}
}

// --- AC-8: a v1:-keyed boxless job learns nothing ------------------------------------------

// B-3. The single-variable oracle for the namespace conjunct, which AC-3 cannot be: the two
// arms differ in the 67 bytes of layout_fingerprint and in nothing else -- same job, same
// tenant, same tokens, same anchors, same body, same method, same field. The needle is asserted
// BEFORE the POST, so the zero is the namespace and not an empty-tokens accident.
func TestRLS_ATypedCorrectionOnAV1KeyedBoxlessJobLearnsNothing(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR19-08-AC8")
	wkCleanupInfra(t, f.tenantID)
	jobID := blSettleDocx(t, ctx, f.tenantID, f.documentID, 915807)
	boxlessFP, toks := blBoxlessPremise(t, ctx, jobID)
	blDerived(t, blField, blValue, toks)

	v1FP := extraction.FingerprintVersion + ":" + strings.Repeat("a", 64)
	if len(boxlessFP) != 67 || len(v1FP) != 67 {
		t.Fatalf("the two keys are %d and %d bytes, want 67 each -- the claim below is that the arms differ in one column of one width",
			len(boxlessFP), len(v1FP))
	}
	if boxlessFP == v1FP {
		t.Fatalf("the two keys are the same string %q; there is no namespace delta left to read", v1FP)
	}

	before := blJobExceptFingerprint(t, ctx, jobID)
	blSetFingerprint(t, ctx, jobID, v1FP)
	if after := blJobExceptFingerprint(t, ctx, jobID); after != before {
		t.Fatalf("re-keying the job also moved %+v to %+v; the arms would differ in more than the fingerprint", before, after)
	}

	learned := &blLearnRecorder{}
	w := cxServe(t, f.reqCtx, jobID, blField, blTyped(blValue),
		cxApplier(false, nil), cxAuditor(nil), learned.record)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("a typed correction on a job re-keyed into the %q namespace left %d anchor rule(s), want 0 -- the derivation owns the boxless namespace and only it",
			extraction.FingerprintVersion, n)
	}
	if n := len(learned.events()); n != 0 {
		t.Errorf("the v1:-keyed arm emitted %d anchor.learned event(s), want 0", n)
	}

	blSetFingerprint(t, ctx, jobID, boxlessFP)
	if after := blJobExceptFingerprint(t, ctx, jobID); after != before {
		t.Fatalf("restoring the key also moved %+v to %+v", before, after)
	}
	w2 := cxServe(t, f.reqCtx, jobID, blField, blTyped(blValue), cxApplier(false, nil), cxAuditor(nil))
	if w2.Code != http.StatusCreated {
		t.Fatalf("control: the job with its own key back answered %d (body=%q), want 201", w2.Code, w2.Body.String())
	}
	rules := clRules(t, ctx, f.tenantID)
	if len(rules) != 1 {
		t.Fatalf("control: the same job with its own %q key left %d anchor rule(s), want 1 -- 67 bytes of one column is then not the variable", boxlessFP, len(rules))
	}
	if rules[0].fingerprint != boxlessFP {
		t.Errorf("control: the rule is keyed to %q, want the job's own %q", rules[0].fingerprint, boxlessFP)
	}
}

// --- AC-9: the boxless branch writes no anchor_label ---------------------------------------

// B-2. The control runs FIRST and on the SAME tenant: a shipped pointed correction over the
// corpus layout stores the server-derived label. Without it, "the boxless row carries no label"
// is equally what a column nothing ever fills looks like.
func TestRLS_ABoxlessLearnedCorrectionWritesNoAnchorLabel(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR19-08-AC9")
	wkCleanupInfra(t, f.tenantID)

	// The control: the shipped pointed path on a v1: layout writes a NON-EMPTY label.
	_, pages := clLayout(t, ctx, f.jobID)
	region := clTokenRegion(t, pages, clTINToken)
	ctrl := cxServe(t, f.reqCtx, f.jobID, clField, clPointedBody(clTINValue, region, ""),
		cxApplier(false, nil), cxAuditor(nil))
	if ctrl.Code != http.StatusCreated {
		t.Fatalf("control: the pointed correction answered %d (body=%q), want 201", ctrl.Code, ctrl.Body.String())
	}
	if label := clAnchorLabel(t, ctx, f.jobID, clField); label == nil || *label != clTINAnchor {
		t.Fatalf("control: the stored anchor_label is %s, want the server-derived %q -- the assertion below would otherwise be reading a column that is always empty",
			clShowLabel(label), clTINAnchor)
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Fatalf("control: the pointed correction left %d anchor rule(s), want 1", n)
	}

	// The boxless arm, in the same tenant. The rule count goes 1 -> 2, so a learned rule and an
	// absent label are asserted together: an empty label on a row that taught nothing is free.
	jobID := blSettleDocx(t, ctx, f.tenantID, f.documentID, 915808)
	fp, toks := blBoxlessPremise(t, ctx, jobID)
	blDerived(t, blField, blValue, toks)

	w := cxServe(t, f.reqCtx, jobID, blField, blTyped(blValue), cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}
	rules := clRules(t, ctx, f.tenantID)
	if len(rules) != 2 {
		t.Fatalf("after the boxless correction the tenant holds %d anchor rule(s), want 2 -- the label assertion below only means something on a correction that DID teach", len(rules))
	}
	if rules[0].fingerprint != fp {
		t.Errorf("the newest rule is keyed to %q, want the boxless job's own %q", rules[0].fingerprint, fp)
	}
	if label := clAnchorLabel(t, ctx, jobID, blField); label != nil && *label != "" {
		t.Errorf("the boxless correction stored anchor_label %s, want absent -- reader.go renders any non-empty label as corrected.where for any method, so this would put a provenance line on every typed DOCX correction",
			clShowLabel(label))
	}
}

// --- EXTR-19-08 (QA, Mode B): the boxless branch's own transaction fate ---------------------

// blFailingLearnRecorder counts and then refuses, so the failure happens INSIDE the handler's
// transaction and after the rule row was written.
type blFailingLearnRecorder struct {
	calls int
	fail  error
}

func (r *blFailingLearnRecorder) record(context.Context, pgx.Tx, string, extraction.AnchorLearned) error {
	r.calls++
	return r.fail
}

// One-transaction-one-fate, for the branch that has never been through it: every shipped
// rollback spec posts a POINTED body, so the boxless arm's five writes shared a fate only by
// construction. Here the emit fails after the rule row is in, and the correction, the invoice
// edit and the field_corrected row must all go with it.
func TestRLS_AFailedBoxlessAnchorLearnedEmitRollsBackTheCorrectionAndTheInvoice(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR19-08-QA-FATE")
	wkCleanupInfra(t, f.tenantID)
	jobID := blSettleDocx(t, ctx, f.tenantID, f.documentID, 915821)
	_, toks := blBoxlessPremise(t, ctx, jobID)
	blDerived(t, blField, blValue, toks)

	var seen cxSeamCall
	rec := &blFailingLearnRecorder{fail: errors.New("forced boxless anchor.learned emit failure")}
	w := cxServe(t, f.reqCtx, jobID, blField, blTyped(blValue), cxClearingApplier(&seen), cxAuditor(nil), rec.record)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d -- a failed emit must take the request down with it (body=%q)",
			w.Code, http.StatusInternalServerError, w.Body.String())
	}
	if rec.calls != 1 {
		t.Fatalf("the learning seam ran %d time(s), want 1 -- the branch never derived, so the zeros below are about a request that never learned", rec.calls)
	}
	if seen.calls != 1 {
		t.Fatalf("the invoice seam ran %d time(s), want 1", seen.calls)
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("%d anchor rule(s) survived a failed emit, want 0 -- a boxless rule no audit row explains", n)
	}
	if n := cxCorrectionRows(t, ctx, jobID); n != 0 {
		t.Errorf("%d correction row(s) survived a failed emit, want 0", n)
	}
	if n := len(cxCorrectionAudit(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("%d %s row(s) survived a failed emit, want 0", n, cxEvent)
	}
	if got := cxBuyerTIN(t, ctx, f.invoiceID); got == nil || *got != clReadingTIN {
		t.Errorf("invoices.buyer_tin = %s after a failed emit, want the unchanged %q", cxShowValue(got), clReadingTIN)
	}

	// The control: the identical request with a recorder that returns nil commits all five.
	ok := &blFailingLearnRecorder{}
	w2 := cxServe(t, f.reqCtx, jobID, blField, blTyped(blValue), cxClearingApplier(&seen), cxAuditor(nil), ok.record)
	if w2.Code != http.StatusCreated {
		t.Fatalf("control: the same request with a working recorder answered %d (body=%q), want 201", w2.Code, w2.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: %d anchor rule(s), want 1 -- the four zeros above prove nothing without this", n)
	}
	if n := cxCorrectionRows(t, ctx, jobID); n != 1 {
		t.Errorf("control: %d correction row(s), want 1", n)
	}
	if got := cxBuyerTIN(t, ctx, f.invoiceID); got == nil || *got != blValue {
		t.Errorf("control: invoices.buyer_tin = %s, want the corrected %q -- the unchanged value above is otherwise a seam that never writes", cxShowValue(got), blValue)
	}
}

// The branch's ONE new 500 surface, reached the only way it can be: the column CHECK admits any
// jsonb array, so a hand-stamped [1,2] is a decode error inside the transaction. Nothing in
// production writes such a row -- layoutTokensStorable marshals a []string -- so this is a guard
// on the guard: it must abort the request rather than answer 201 over an unread column, and it
// must leave nothing behind.
func TestRLS_AnUndecodableLayoutTokensColumnAbortsTheCorrectionAndCommitsNothing(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR19-08-QA-500")
	wkCleanupInfra(t, f.tenantID)
	jobID := blSettleDocx(t, ctx, f.tenantID, f.documentID, 915822)
	_, toks := blBoxlessPremise(t, ctx, jobID)
	blDerived(t, blField, blValue, toks)
	stored := wtTokens(t, ctx, jobID)

	blSetTokens(t, ctx, jobID, stPtr(`[1,2]`))
	var seen cxSeamCall
	w := cxServe(t, f.reqCtx, jobID, blField, blTyped(blValue), cxClearingApplier(&seen), cxAuditor(nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d -- a column the reader cannot decode is an error, never a silent absence (body=%q)",
			w.Code, http.StatusInternalServerError, w.Body.String())
	}
	if n := cxCorrectionRows(t, ctx, jobID); n != 0 {
		t.Errorf("%d correction row(s) survived the aborted request, want 0 -- the read happens before the correction row, so a surviving row means two transactions", n)
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("%d anchor rule(s) survived the aborted request, want 0", n)
	}
	if n := len(cxCorrectionAudit(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("%d %s row(s) survived the aborted request, want 0", n, cxEvent)
	}
	if got := cxBuyerTIN(t, ctx, f.invoiceID); got == nil || *got != clReadingTIN {
		t.Errorf("invoices.buyer_tin = %s after the aborted request, want the unchanged %q", cxShowValue(got), clReadingTIN)
	}

	// The control: the same job with its own bytes back answers 201 and commits all five, so
	// the four zeros above are the undecodable column and not this fixture.
	blSetTokens(t, ctx, jobID, stored)
	w2 := cxServe(t, f.reqCtx, jobID, blField, blTyped(blValue), cxClearingApplier(&seen), cxAuditor(nil))
	if w2.Code != http.StatusCreated {
		t.Fatalf("control: the job with its own tokens back answered %d (body=%q), want 201", w2.Code, w2.Body.String())
	}
	if n := cxCorrectionRows(t, ctx, jobID); n != 1 {
		t.Errorf("control: %d correction row(s), want 1", n)
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: %d anchor rule(s), want 1", n)
	}
	if got := cxBuyerTIN(t, ctx, f.invoiceID); got == nil || *got != blValue {
		t.Errorf("control: invoices.buyer_tin = %s, want the corrected %q", cxShowValue(got), blValue)
	}
}
