// handlers_correction_typed_db_test.go: what a typed correction teaches on a geometric layout.
// Shares the cx*, cl*, lc* and bl* harnesses, so it adds no skip site. Helpers use a ct* prefix.
package extraction_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	ctField      = "total"
	ctTotal      = "14800000.00"
	ctTotalToken = "₦14,800,000.00"

	// What an expired PDFium pool borrow returns, measured; never context.DeadlineExceeded.
	ctPoolExpiry = "pdfium: get instance: Timeout waiting for idle object"

	ctTINRuleBody   = `{"label":"(?i)\\bTIN\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"tin"}`
	ctSplitValue    = "2150.00"
	ctSplitToken    = "2,150.00"
	ctSplitRuleBody = `{"label":"(?i)\\bTotal\\b","relation":{"kind":"right","max_distance":0.21},"shape":"amount"}`
)

// ctReader is the production page reader over a spy opener serving fixture's bytes, so
// op.count() is the read count.
func ctReader(t *testing.T, fixture string) (*wkOpener, extraction.ReadPageOne) {
	t.Helper()
	op := &wkOpener{body: fxRead(t, fixture)}
	return op, extraction.PageOneReader(op.open, extraction.NewPDFiumReader())
}

// ctLog is a JSON logger over a buffer, so a record's level and attributes decode exactly.
func ctLog() (*bytes.Buffer, *slog.Logger) {
	buf := &bytes.Buffer{}
	return buf, slog.New(slog.NewJSONHandler(buf, nil))
}

// ctWarns decodes every WARN record written to buf.
func ctWarns(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode log record %q: %v", line, err)
		}
		if rec["level"] == slog.LevelWarn.String() {
			out = append(out, rec)
		}
	}
	return out
}

// ctJob seeds a tenant whose job carries fixture's layout.
func ctJob(t *testing.T, ctx context.Context, number, fixture string) (clFixture, string, []extraction.TokenPage) {
	t.Helper()
	f := clSeed(t, ctx, number)
	fp, pages := lcLayout(t, ctx, f.jobID, fixture)
	return f, fp, pages
}

// ctVerdict runs the typed learner over page 1 and the anchors lcLayout stores.
func ctVerdict(t *testing.T, pages []extraction.TokenPage, field, value string) extraction.TypedVerdict {
	t.Helper()
	if len(pages) == 0 || pages[0].Number != 1 {
		t.Fatalf("the fixture read yields no page 1; a verdict over it names no clause")
	}
	_, v := extraction.LearnTypedRule(field, value, pages[0], extraction.AnchorObservations(pages))
	return v
}

// ctPost posts one typed correction and requires a 201.
func ctPost(t *testing.T, f clFixture, jobID, field, value string, pageOne extraction.ReadPageOne, log *slog.Logger, learned ...extraction.RecordAnchorLearned) {
	t.Helper()
	w := cxServeWith(t, f.reqCtx, jobID, field, corBody(value, "typed", ""), pageOne, log,
		cxApplier(false, nil), cxAuditor(nil), learned...)
	if w.Code != http.StatusCreated {
		t.Fatalf("typed %s %q on job %s answered %d (body=%q), want 201", field, value, jobID, w.Code, w.Body.String())
	}
}

func TestRLS_ATypedCorrectionTeachesThePointedRuleOnAGeometricLayout(t *testing.T) {
	ctx := t.Context()
	p, _, pages := ctJob(t, ctx, "EXTR28-04-PARITY-P", fxLearnedTypedTotal)
	typ, _, _ := ctJob(t, ctx, "EXTR28-04-PARITY-T", fxLearnedTypedTotal)

	w := cxServe(t, p.reqCtx, p.jobID, ctField, clPointedBody(ctTotal, clTokenRegion(t, pages, ctTotalToken), ""),
		cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("the pointed correction answered %d (body=%q), want 201", w.Code, w.Body.String())
	}
	op, pageOne := ctReader(t, fxLearnedTypedTotal)
	learned := &blLearnRecorder{}
	ctPost(t, typ, typ.jobID, ctField, ctTotal, pageOne, nil, learned.record)

	pointed, typed := clRules(t, ctx, p.tenantID), clRules(t, ctx, typ.tenantID)
	if len(pointed) != 1 || len(typed) != 1 {
		t.Fatalf("the pointing tenant holds %d rule(s) and the typing tenant %d, want 1 each", len(pointed), len(typed))
	}
	lcRuleBodyIs(t, ctx, typed[0].id, pointed[0].body)
	lcRuleBodyIs(t, ctx, typed[0].id, lpTotalRuleBody)
	if want := clJobFingerprint(t, ctx, typ.jobID); typed[0].fingerprint != want {
		t.Errorf("the typed rule is keyed to %q, want the job's own %q", typed[0].fingerprint, want)
	}
	evs := learned.events()
	if len(evs) != 1 {
		t.Fatalf("the typed correction emitted %d anchor.learned event(s), want 1", len(evs))
	}
	if evs[0].Relation != extraction.RelRight || evs[0].Shape != extraction.ShapeAmount || evs[0].RuleID != typed[0].id {
		t.Errorf("the emit is %s/%s for rule %q, want %s/%s for %q", evs[0].Relation, evs[0].Shape, evs[0].RuleID,
			extraction.RelRight, extraction.ShapeAmount, typed[0].id)
	}
	if n := op.count(); n != 1 {
		t.Fatalf("the typed correction read the document %d time(s), want 1", n)
	}
	// The job row names the document, and the operator's identity reaches the audited opener.
	read := op.first(t)
	caller, _ := auth.IdentityFromContext(typ.reqCtx)
	if read.doc != typ.documentID || !read.ok || read.id.Subject != caller.Subject || read.id.TenantID != caller.TenantID {
		t.Errorf("the page read opened document %q as %+v (identity present %v), want %q as %+v", read.doc, read.id, read.ok, typ.documentID, caller)
	}
}

// The regression the story was gated on: pointing at this token teaches a TIN rule that misfires;
// typing it must refuse at the self-check, not earlier.
func TestRLS_ATypedTINCorrectionOnTheTwoColumnLayoutRefusesAtTheSelfCheck(t *testing.T) {
	ctx := t.Context()
	f, _, pages := ctJob(t, ctx, "EXTR28-04-TWOCOL", lcTwoCol)
	if v := ctVerdict(t, pages, clField, clTINValue); v != extraction.TypedSelfCheckRefused {
		t.Fatalf("LearnTypedRule(%s, %q) = %d on %s, want TypedSelfCheckRefused", clField, clTINValue, v, lcTwoCol)
	}
	if lr, ok := extraction.LearnRule(clField, clTokenRegion(t, pages, clTINToken), extraction.AnchorObservations(pages)); !ok || string(lr.Body) != ctTINRuleBody {
		t.Fatalf("pointing at %q derives ok=%v %s, want %s -- the refusal below is then not the self-check's", clTINToken, ok, lr.Body, ctTINRuleBody)
	}

	buf, log := ctLog()
	op, pageOne := ctReader(t, lcTwoCol)
	learned := &blLearnRecorder{}
	ctPost(t, f, f.jobID, clField, clTINValue, pageOne, log, learned.record)

	if n := cxCorrectionRows(t, ctx, f.jobID); n != 1 {
		t.Errorf("%d correction row(s), want 1", n)
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("typing the buyer TIN left %d anchor rule(s), want 0", n)
	}
	if n := len(learned.events()); n != 0 {
		t.Errorf("%d anchor.learned event(s), want 0", n)
	}
	if n := op.count(); n != 1 {
		t.Errorf("the typed correction read the document %d time(s), want 1", n)
	}
	if warns := ctWarns(t, buf); len(warns) != 0 {
		t.Errorf("a self-check refusal logged %d WARN record(s), want 0: %v", len(warns), warns)
	}
}

func TestRLS_AnAmbiguousTypedValueRecordsTheCorrectionAndTeachesNothing(t *testing.T) {
	ctx := t.Context()
	f, _, pages := ctJob(t, ctx, "EXTR28-04-SEVERAL", fxLearnedTypedTotal)
	if v := ctVerdict(t, pages, "currency", "NGN"); v != extraction.TypedSeveralTokens {
		t.Fatalf("LearnTypedRule(currency, NGN) = %d, want TypedSeveralTokens", v)
	}

	op, pageOne := ctReader(t, fxLearnedTypedTotal)
	learned := &blLearnRecorder{}
	ctPost(t, f, f.jobID, "currency", "NGN", pageOne, nil, learned.record)
	if n := cxCorrectionRows(t, ctx, f.jobID); n != 1 {
		t.Errorf("%d correction row(s), want 1", n)
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("a value printed on several tokens left %d anchor rule(s), want 0", n)
	}
	if n := len(learned.events()); n != 0 {
		t.Errorf("%d anchor.learned event(s), want 0", n)
	}
	if n := op.count(); n != 1 {
		t.Errorf("the typed correction read the document %d time(s), want 1", n)
	}

	ctPost(t, f, f.jobID, ctField, ctTotal, pageOne, nil)
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: typed %s on the same job left %d anchor rule(s), want 1", ctField, n)
	}
}

func TestRLS_AnUnprintedTypedValueRecordsTheCorrectionAndTeachesNothing(t *testing.T) {
	ctx := t.Context()
	f, _, pages := ctJob(t, ctx, "EXTR28-04-NOTOKEN", fxLearnedTypedTotal)
	const unprinted = "14800001.00"
	if v := ctVerdict(t, pages, ctField, unprinted); v != extraction.TypedNoToken {
		t.Fatalf("LearnTypedRule(%s, %q) = %d, want TypedNoToken", ctField, unprinted, v)
	}

	op, pageOne := ctReader(t, fxLearnedTypedTotal)
	learned := &blLearnRecorder{}
	ctPost(t, f, f.jobID, ctField, unprinted, pageOne, nil, learned.record)
	if n := cxCorrectionRows(t, ctx, f.jobID); n != 1 {
		t.Errorf("%d correction row(s), want 1", n)
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("a value no token carries left %d anchor rule(s), want 0", n)
	}
	if n := len(learned.events()); n != 0 {
		t.Errorf("%d anchor.learned event(s), want 0", n)
	}
	if n := op.count(); n != 1 {
		t.Errorf("the typed correction read the document %d time(s), want 1", n)
	}

	ctPost(t, f, f.jobID, ctField, ctTotal, pageOne, nil)
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: typed %q on the same job left %d anchor rule(s), want 1", ctTotal, n)
	}
}

func TestRLS_AFailedPageReadCommitsTheCorrectionAndTeachesNothing(t *testing.T) {
	ctx := t.Context()
	f, _, _ := ctJob(t, ctx, "EXTR28-04-READFAIL", fxLearnedTypedTotal)

	buf, log := ctLog()
	reads := 0
	failing := func(context.Context, string) (extraction.TokenPage, bool, error) {
		reads++
		return extraction.TokenPage{}, false, errors.New("object store down")
	}
	var seen cxSeamCall
	w := cxServeWith(t, f.reqCtx, f.jobID, ctField, corBody(ctTotal, "typed", ""), failing, log,
		cxRecorder(&seen, false), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("a failed page read answered %d (body=%q), want 201 -- the correction did not depend on the read", w.Code, w.Body.String())
	}
	if reads != 1 {
		t.Errorf("the page reader ran %d time(s), want 1", reads)
	}
	if n := cxCorrectionRows(t, ctx, f.jobID); n != 1 {
		t.Errorf("%d correction row(s), want 1", n)
	}
	if seen.calls != 1 || seen.value == nil || *seen.value != ctTotal {
		t.Errorf("the invoice seam ran %d time(s) with value %s, want once with %q", seen.calls, cxShowValue(seen.value), ctTotal)
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("a failed page read left %d anchor rule(s), want 0", n)
	}
	warns := ctWarns(t, buf)
	if len(warns) != 1 {
		t.Fatalf("%d WARN record(s) after a failed page read, want 1: %s", len(warns), buf)
	}
	if warns[0]["job"] != f.jobID || warns[0]["field"] != ctField || warns[0]["err"] != "object store down" {
		t.Errorf("the WARN record carries job=%v field=%v err=%v, want %s, %s and the read's error", warns[0]["job"], warns[0]["field"], warns[0]["err"], f.jobID, ctField)
	}
	for k := range warns[0] {
		if k != "time" && k != "level" && k != "msg" && k != "job" && k != "field" && k != "err" {
			t.Errorf("the WARN record carries %q, want only job, field and err", k)
		}
	}
	if strings.Contains(buf.String(), ctTotal) {
		t.Errorf("the log carries the typed value %q: %s", ctTotal, buf)
	}

	// A document with no page 1 fails the read without an error.
	buf.Reset()
	notFound := func(context.Context, string) (extraction.TokenPage, bool, error) {
		return extraction.TokenPage{}, false, nil
	}
	w = cxServeWith(t, f.reqCtx, f.jobID, ctField, corBody(ctTotal, "typed", ""), notFound, log,
		cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("a read that found no page 1 answered %d (body=%q), want 201", w.Code, w.Body.String())
	}
	if n := len(ctWarns(t, buf)); n != 1 {
		t.Errorf("%d WARN record(s) after a read that found no page 1, want 1: %s", n, buf)
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("a read that found no page 1 left %d anchor rule(s), want 0", n)
	}

	_, pageOne := ctReader(t, fxLearnedTypedTotal)
	ctPost(t, f, f.jobID, ctField, ctTotal, pageOne, nil)
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: the real reader on the same job left %d anchor rule(s), want 1", n)
	}
}

func TestRLS_APageReadIsBoundedAndItsExpiryCommitsTheCorrection(t *testing.T) {
	ctx := t.Context()
	f, _, _ := ctJob(t, ctx, "EXTR28-04-BOUND", fxLearnedTypedTotal)

	caller, _ := auth.IdentityFromContext(f.reqCtx)
	var (
		deadline time.Time
		bounded  bool
		reader   auth.Identity
		named    bool
	)
	expiring := func(rctx context.Context, _ string) (extraction.TokenPage, bool, error) {
		deadline, bounded = rctx.Deadline()
		reader, named = auth.IdentityFromContext(rctx)
		return extraction.TokenPage{}, false, errors.New(ctPoolExpiry)
	}
	_, log := ctLog()
	before := time.Now()
	w := cxServeWith(t, f.reqCtx, f.jobID, ctField, corBody(ctTotal, "typed", ""), expiring, log,
		cxApplier(false, nil), cxAuditor(nil))
	after := time.Now()

	if !bounded {
		t.Fatalf("the page read ran with no deadline; an exhausted pool would hold the request")
	}
	bound := extraction.PageOneReadTimeoutForTest
	if deadline.Before(before.Add(bound)) || deadline.After(after.Add(bound)) {
		t.Errorf("the page read's deadline is %s after the request began, want %s", deadline.Sub(before), bound)
	}
	// document.read is written for the identity on this ctx.
	if !named || reader.Subject != caller.Subject || reader.TenantID != caller.TenantID {
		t.Errorf("the page read ran as %+v (identity present %v), want the operator %+v", reader, named, caller)
	}
	if w.Code != http.StatusCreated {
		t.Fatalf("an expired page read answered %d (body=%q), want 201", w.Code, w.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("an expired page read left %d anchor rule(s), want 0", n)
	}
	if n := cxCorrectionRows(t, ctx, f.jobID); n != 1 {
		t.Errorf("%d correction row(s), want 1", n)
	}

	// The bound derives from the request ctx, so a caller that goes away cancels the read too.
	reqCtx, cancelReq := context.WithCancel(f.reqCtx)
	defer cancelReq()
	var afterCancel error
	cancelling := func(rctx context.Context, _ string) (extraction.TokenPage, bool, error) {
		cancelReq()
		afterCancel = rctx.Err()
		return extraction.TokenPage{}, false, errors.New(ctPoolExpiry)
	}
	cxServeWith(t, reqCtx, f.jobID, ctField, corBody(ctTotal, "typed", ""), cancelling, log,
		cxApplier(false, nil), cxAuditor(nil))
	if !errors.Is(afterCancel, context.Canceled) {
		t.Errorf("cancelling the request left the page read's ctx error at %v, want context.Canceled", afterCancel)
	}
}

func TestRLS_ChosenAndUndoneNeverReadTheDocumentAndTeachNothing(t *testing.T) {
	ctx := t.Context()
	f, _, _ := ctJob(t, ctx, "EXTR28-04-CHOSEN", fxLearnedTypedTotal)
	cxReading(t, ctx, f.tenantID, f.jobID, ctField, 0, cxStr(ctTotal))

	op, pageOne := ctReader(t, fxLearnedTypedTotal)
	for _, method := range []string{"chosen", "undone"} {
		w := cxServeWith(t, f.reqCtx, f.jobID, ctField, corBody(ctTotal, method, ""), pageOne, nil,
			cxApplier(false, nil), cxAuditor(nil))
		if w.Code != http.StatusCreated {
			t.Fatalf("a %s correction answered %d (body=%q), want 201", method, w.Code, w.Body.String())
		}
	}
	if n := op.count(); n != 0 {
		t.Errorf("chosen and undone read the document %d time(s), want 0", n)
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("chosen and undone left %d anchor rule(s), want 0", n)
	}

	ctPost(t, f, f.jobID, ctField, ctTotal, pageOne, nil)
	if n := op.count(); n != 1 {
		t.Errorf("control: typed read the document %d time(s), want 1", n)
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: typed left %d anchor rule(s), want 1", n)
	}
}

func TestRLS_ATypedCorrectionOnABoxlessJobNeverReadsTheDocumentAndStillLearns(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR28-04-BOXLESS")
	wkCleanupInfra(t, f.tenantID)
	jobID := blSettleDocx(t, ctx, f.tenantID, f.documentID, 928401)
	fp, toks := blBoxlessPremise(t, ctx, jobID)
	want := blDerived(t, blField, blValue, toks)

	op, pageOne := ctReader(t, fxLearnedTypedTotal)
	ctPost(t, f, jobID, blField, blValue, pageOne, nil)
	rules := clRules(t, ctx, f.tenantID)
	if len(rules) != 1 {
		t.Fatalf("a typed correction on a boxless job left %d anchor rule(s), want 1", len(rules))
	}
	if !blSameRuleJSON(t, rules[0].body, want.Body) {
		t.Errorf("the stored rule is %s, want the boxless derivation's %s", rules[0].body, want.Body)
	}
	if n := op.count(); n != 0 {
		t.Errorf("a typed correction on a boxless job read the document %d time(s), want 0", n)
	}

	// The same boxless key over anchors that carry real boxes: only the namespace keeps the read out.
	raw, err := extraction.MarshalAnchorObservations(extraction.AnchorObservations(rvCorpusPages(t, fxLearnedTypedTotal)))
	if err != nil {
		t.Fatalf("marshal the %s anchors: %v", fxLearnedTypedTotal, err)
	}
	clLayoutRaw(t, ctx, jobID, fp, raw)
	ctPost(t, f, jobID, blField, blValue, pageOne, nil)
	if n := op.count(); n != 0 {
		t.Errorf("a boxless key over boxed anchors read the document %d time(s), want 0", n)
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 2 {
		t.Errorf("the boxless job with boxed anchors left %d anchor rule(s), want 2 -- the boxless branch still derives", n)
	}
}

func TestRLS_ATypedCorrectionOnAJobWithNoUsableAnchorReadsNothing(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR28-04-NOANCHOR")
	pages := rvCorpusPages(t, fxLearnedTypedTotal)
	fp := extraction.Fingerprint(pages)
	zero := extraction.AnchorObservations(pages)
	if len(zero) == 0 {
		t.Fatalf("%s carries no anchor; the zero-box arm would stamp an empty list", fxLearnedTypedTotal)
	}
	for i := range zero {
		zero[i].X0, zero[i].Y0, zero[i].X1, zero[i].Y1 = 0, 0, 0, 0
	}
	zeroRaw, err := extraction.MarshalAnchorObservations(zero)
	if err != nil {
		t.Fatalf("marshal zero-box anchors: %v", err)
	}

	op, pageOne := ctReader(t, fxLearnedTypedTotal)
	for _, arm := range []struct {
		name  string
		stamp func()
	}{
		{"no layout", func() {}},
		{"layout_anchors SQL NULL", func() { clLayoutRaw(t, ctx, f.jobID, fp, nil) }},
		{"layout_anchors an empty array", func() { clLayoutRaw(t, ctx, f.jobID, fp, []byte("[]")) }},
		{"every anchor a zero box", func() { clLayoutRaw(t, ctx, f.jobID, fp, zeroRaw) }},
	} {
		arm.stamp()
		ctPost(t, f, f.jobID, ctField, ctTotal, pageOne, nil)
		if n := op.count(); n != 0 {
			t.Errorf("%s: the typed correction read the document %d time(s), want 0", arm.name, n)
		}
		if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
			t.Errorf("%s: %d anchor rule(s), want 0", arm.name, n)
		}
	}

	lcLayout(t, ctx, f.jobID, fxLearnedTypedTotal)
	ctPost(t, f, f.jobID, ctField, ctTotal, pageOne, nil)
	if n := op.count(); n != 1 {
		t.Errorf("control: the job with its real anchors read the document %d time(s), want 1", n)
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: %d anchor rule(s), want 1", n)
	}
}

func TestRLS_ATypedCorrectionOnAnotherTenantsJobReadsNoDocument(t *testing.T) {
	ctx := t.Context()
	a, _, _ := ctJob(t, ctx, "EXTR28-04-XT-A", fxLearnedTypedTotal)
	b := clSeed(t, ctx, "EXTR28-04-XT-B")

	op, pageOne := ctReader(t, fxLearnedTypedTotal)
	w := cxServeWith(t, b.reqCtx, a.jobID, ctField, corBody(ctTotal, "typed", ""), pageOne, nil,
		cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("B typing on A's job answered %d (body=%q), want 404", w.Code, w.Body.String())
	}
	if n := op.count(); n != 0 {
		t.Errorf("B typing on A's job read the document %d time(s), want 0", n)
	}
	for _, tc := range []struct{ who, tenantID string }{{"A", a.tenantID}, {"B", b.tenantID}} {
		if n := len(clRules(t, ctx, tc.tenantID)); n != 0 {
			t.Errorf("tenant %s holds %d anchor rule(s) after a cross-tenant POST, want 0", tc.who, n)
		}
	}

	ctPost(t, a, a.jobID, ctField, ctTotal, pageOne, nil)
	if n := op.count(); n != 1 {
		t.Errorf("control: A typing on its own job read the document %d time(s), want 1", n)
	}
	if n := len(clRules(t, ctx, a.tenantID)); n != 1 {
		t.Errorf("control: A holds %d anchor rule(s), want 1", n)
	}
}

func TestRLS_ATypedGeometricLearningWritesNoAnchorLabel(t *testing.T) {
	ctx := t.Context()
	f, _, pages := ctJob(t, ctx, "EXTR28-04-LABEL", fxLearnedTypedTotal)

	_, pageOne := ctReader(t, fxLearnedTypedTotal)
	ctPost(t, f, f.jobID, ctField, ctTotal, pageOne, nil)
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Fatalf("the typed correction left %d anchor rule(s), want 1 -- an empty label on a correction that taught nothing is free", n)
	}
	if label := clAnchorLabel(t, ctx, f.jobID, ctField); label != nil && *label != "" {
		t.Errorf("the typed correction stored anchor_label %s, want absent -- the operator named no label", clShowLabel(label))
	}

	job2 := cxJobIn(t, ctx, f.tenantID, f.documentID)
	lcLayout(t, ctx, job2, fxLearnedTypedTotal)
	w := cxServe(t, f.reqCtx, job2, ctField, clPointedBody(ctTotal, clTokenRegion(t, pages, ctTotalToken), ""),
		cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("control: the pointed correction answered %d (body=%q), want 201", w.Code, w.Body.String())
	}
	if label := clAnchorLabel(t, ctx, job2, ctField); label == nil || *label != "VAT" {
		t.Errorf("control: the pointed correction stored anchor_label %s, want %q", clShowLabel(label), "VAT")
	}
}

func TestRLS_ATypedCorrectionOnARetiredGenerationJobWritesARuleNoExtractionSelects(t *testing.T) {
	ctx := t.Context()
	f, fp, pages := ctJob(t, ctx, "EXTR28-04-RETIRED", fxLearnedTypedTotal)
	// v1, not the prior generation: TestFingerprint_NoSourceStillNamesTheOldGeneration forbids its literal.
	current := extraction.FingerprintVersion + ":"
	if !strings.HasPrefix(fp, current) || extraction.FingerprintVersion == "v1" {
		t.Fatalf("the fixture's key %q is not in the current %q namespace above v1", fp, extraction.FingerprintVersion)
	}
	retired := "v1:" + strings.TrimPrefix(fp, current)
	raw, err := extraction.MarshalAnchorObservations(extraction.AnchorObservations(pages))
	if err != nil {
		t.Fatalf("marshal the anchors: %v", err)
	}
	clLayoutRaw(t, ctx, f.jobID, retired, raw)

	op, pageOne := ctReader(t, fxLearnedTypedTotal)
	learned := &blLearnRecorder{}
	ctPost(t, f, f.jobID, ctField, ctTotal, pageOne, nil, learned.record)

	rules := clRules(t, ctx, f.tenantID)
	if len(rules) != 1 {
		t.Fatalf("a typed correction on a retired-generation job left %d anchor rule(s), want 1 -- a pointed one writes its rule under any stored key", len(rules))
	}
	if rules[0].fingerprint != retired {
		t.Errorf("the rule is keyed to %q, want the job's stored %q", rules[0].fingerprint, retired)
	}
	if evs := learned.events(); len(evs) != 1 || evs[0].LayoutFingerprint != retired {
		t.Errorf("anchor.learned events %+v, want one carrying %q", evs, retired)
	}
	if n := op.count(); n != 1 {
		t.Errorf("the typed correction read the document %d time(s), want 1", n)
	}
	for _, tc := range []struct {
		key  string
		want int
	}{{fp, 0}, {retired, 1}} {
		got, err := stStore(t).AnchorRulesFor(ctx, f.tenantID, tc.key)
		if err != nil {
			t.Fatalf("AnchorRulesFor(%s): %v", tc.key, err)
		}
		if len(got) != tc.want {
			t.Errorf("AnchorRulesFor(%s) returned %d rule(s), want %d", tc.key, len(got), tc.want)
		}
	}
}

// The intended divergence: on this layout pointing teaches the misfiring rule and typing refuses.
func TestRLS_OnTheSplitLayoutPointingTeachesTheTotalAndTypingItRefuses(t *testing.T) {
	ctx := t.Context()
	f, _, pages := ctJob(t, ctx, "EXTR28-04-SPLIT", lcSplit)
	jobP := cxJobIn(t, ctx, f.tenantID, f.documentID)
	lcLayout(t, ctx, jobP, lcSplit)

	if v := ctVerdict(t, pages, ctField, ctSplitValue); v != extraction.TypedSelfCheckRefused {
		t.Fatalf("LearnTypedRule(%s, %q) = %d on %s, want TypedSelfCheckRefused", ctField, ctSplitValue, v, lcSplit)
	}
	region := clTokenRegion(t, pages, ctSplitToken)
	if lr, ok := extraction.LearnRule(ctField, region, extraction.AnchorObservations(pages)); !ok || string(lr.Body) != ctSplitRuleBody {
		t.Fatalf("pointing at %q derives ok=%v %s, want %s", ctSplitToken, ok, lr.Body, ctSplitRuleBody)
	}

	buf, log := ctLog()
	op, pageOne := ctReader(t, lcSplit)
	learned := &blLearnRecorder{}
	ctPost(t, f, f.jobID, ctField, ctSplitValue, pageOne, log, learned.record)
	if n := cxCorrectionRows(t, ctx, f.jobID); n != 1 {
		t.Errorf("%d correction row(s) on the typed job, want 1", n)
	}
	if n := op.count(); n != 1 {
		t.Errorf("the typed correction read the document %d time(s), want 1", n)
	}
	if warns := ctWarns(t, buf); len(warns) != 0 {
		t.Errorf("a self-check refusal logged %d WARN record(s), want 0: %v", len(warns), warns)
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("typing the split-layout total left %d anchor rule(s), want 0", n)
	}
	if n := len(learned.events()); n != 0 {
		t.Errorf("%d anchor.learned event(s), want 0", n)
	}

	w := cxServe(t, f.reqCtx, jobP, ctField, clPointedBody(ctSplitValue, region, ""), cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("the pointed correction answered %d (body=%q), want 201", w.Code, w.Body.String())
	}
	rules := clRules(t, ctx, f.tenantID)
	if len(rules) != 1 {
		t.Fatalf("pointing at the same token left %d anchor rule(s), want 1", len(rules))
	}
	lcRuleBodyIs(t, ctx, rules[0].id, ctSplitRuleBody)
}

// A corrupt stored column is a data error, not a failed read: 500, nothing commits, no WARN.
func TestRLS_AnUndecodableLayoutAnchorsColumnAbortsATypedCorrection(t *testing.T) {
	ctx := t.Context()
	f, fp, _ := ctJob(t, ctx, "EXTR28-04-UNDECODABLE", fxLearnedTypedTotal)
	clLayoutRaw(t, ctx, f.jobID, fp, []byte(`[1,2]`))

	buf, log := ctLog()
	_, pageOne := ctReader(t, fxLearnedTypedTotal)
	w := cxServeWith(t, f.reqCtx, f.jobID, ctField, corBody(ctTotal, "typed", ""), pageOne, log,
		cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("an undecodable layout_anchors column answered %d (body=%q), want 500", w.Code, w.Body.String())
	}
	if n := cxCorrectionRows(t, ctx, f.jobID); n != 0 {
		t.Errorf("%d correction row(s) survived the aborted request, want 0", n)
	}
	if n := len(cxCorrectionAudit(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("%d %s row(s) survived the aborted request, want 0", n, cxEvent)
	}
	if warns := ctWarns(t, buf); len(warns) != 0 {
		t.Errorf("an undecodable column logged %d WARN record(s), want 0: %v", len(warns), warns)
	}

	// Control: the same job with its anchors back commits.
	lcLayout(t, ctx, f.jobID, fxLearnedTypedTotal)
	ctPost(t, f, f.jobID, ctField, ctTotal, pageOne, nil)
	if n := cxCorrectionRows(t, ctx, f.jobID); n != 1 {
		t.Errorf("control: %d correction row(s), want 1", n)
	}
	if n := len(cxCorrectionAudit(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: %d %s row(s), want 1", n, cxEvent)
	}
}
