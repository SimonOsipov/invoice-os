// handlers_correction_learn_audit_db_test.go: the audit row a LEARNED rule owes. One per rule
// written, none otherwise, on the correction's own transaction, carrying no business content.
//
// Shares handlers_correction_db_test.go's cx* harness and the learn file's cl* fixtures, so this
// file adds no second skip site. Helpers use an la* prefix.
package extraction_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// laEvent is the event name cmd/submission spells for production. Spelled here because the
// assertion needs a value to compare against; the rule keeping it out of internal/extraction
// covers non-test source only.
const laEvent = "extraction.anchor.learned"

// The sentinels a leak would carry into audit_log. The value is what the reviewer typed; the
// anchor text is read verbatim off the document.
const (
	laValueSentinel  = "ZQXCORRECTEDVALUE0001"
	laAnchorSentinel = "ZQXANCHORTEXT0002"
)

// laRecorder is the injected learning seam. It writes the same three columns internal/audit's
// Record writes, through the tx the handler hands it -- so a recorder called outside the
// transaction writes nothing and the counts below stay at zero. It also keeps every struct it
// was handed, so a WIDENED AnchorLearned is visible to the leak scan through reflection.
type laRecorder struct {
	calls int
	got   []extraction.AnchorLearned
	fail  error
}

func (r *laRecorder) record(ctx context.Context, tx pgx.Tx, subject string, a extraction.AnchorLearned) error {
	r.calls++
	r.got = append(r.got, a)
	if r.fail != nil {
		return r.fail
	}
	if tx == nil {
		return errors.New("audit: the learning recorder was handed no transaction, so the row cannot share the rule's fate")
	}
	payload, err := json.Marshal(map[string]string{
		"invoice_id":         a.InvoiceID,
		"field":              a.FieldName,
		"layout_fingerprint": a.LayoutFingerprint,
		"anchor_rule_id":     a.RuleID,
		"relation":           string(a.Relation),
		"shape":              string(a.Shape),
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO audit_log (actor, event, payload) VALUES ($1, $2, $3)`,
		subject, laEvent, string(payload))
	return err
}

// laRow is one stored anchor.learned row, with the entity the write-time trigger resolved.
type laRow struct {
	actor    string
	entityID *string
	raw      string
	payload  map[string]any
}

// laRows reads a tenant's anchor.learned rows as the SUPERUSER: an app-pool count is
// RLS-filtered and would read the same whether or not a row was written.
func laRows(t *testing.T, ctx context.Context, tenantID string) []laRow {
	t.Helper()
	rows, err := stRequire(t).super.Query(ctx,
		`SELECT actor, entity_id::text, payload::text FROM audit_log
		  WHERE tenant_id = $1 AND event = $2 ORDER BY id`, tenantID, laEvent)
	if err != nil {
		t.Fatalf("read %s rows for tenant %s: %v", laEvent, tenantID, err)
	}
	defer rows.Close()

	out := []laRow{}
	for rows.Next() {
		var r laRow
		if err := rows.Scan(&r.actor, &r.entityID, &r.raw); err != nil {
			t.Fatalf("scan %s row: %v", laEvent, err)
		}
		if err := json.Unmarshal([]byte(r.raw), &r.payload); err != nil {
			t.Fatalf("decode %s payload %s: %v", laEvent, r.raw, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s rows for tenant %s: %v", laEvent, tenantID, err)
	}
	return out
}

// laInvoiceEntity is the company the invoice belongs to, read as the superuser. AC-2's oracle
// end to end: the row's entity_id must equal it.
func laInvoiceEntity(t *testing.T, ctx context.Context, invoiceID string) string {
	t.Helper()
	var out string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT entity_id::text FROM invoices WHERE id = $1`, invoiceID).Scan(&out); err != nil {
		t.Fatalf("read entity_id for invoice %s: %v", invoiceID, err)
	}
	return out
}

func laString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return "<absent>"
}

// --- A-01 / AC-1: one rule, one row --------------------------------------------------------

func TestRLS_ALearnedRuleWritesExactlyOneAnchorLearnedAuditRow(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-07-A01")
	_, pages := clLayout(t, ctx, f.jobID)
	region := clTokenRegion(t, pages, clTINToken)

	rec := &laRecorder{}
	w := cxServe(t, f.reqCtx, f.jobID, clField, clPointedBody(clTINValue, region, ""),
		cxApplier(false, nil), cxAuditor(nil), rec.record)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}

	rules := clRules(t, ctx, f.tenantID)
	if len(rules) != 1 {
		t.Fatalf("the correction left %d anchor rule(s), want exactly 1 -- the row below is owed per RULE", len(rules))
	}
	if rec.calls != 1 {
		t.Errorf("the learning seam ran %d time(s), want 1", rec.calls)
	}

	rows := laRows(t, ctx, f.tenantID)
	if len(rows) != 1 {
		t.Fatalf("%d %s row(s) for one learned rule, want exactly 1 -- audit_log is append-only, so a "+
			"duplicate is permanent and a miss is unrecoverable", len(rows), laEvent)
	}
	// The pair: the correction's own event still lands exactly once, so "one row" is not a
	// handler that swapped one event for the other.
	if n := len(cxCorrectionAudit(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("%d %s row(s), want 1 -- the learning row is an addition, not a replacement", n, cxEvent)
	}

	row := rows[0]
	if got := laString(row.payload, "invoice_id"); got != f.invoiceID {
		t.Errorf("the row carries invoice_id %q, want the invoice the correction reached %q", got, f.invoiceID)
	}
	if got := laString(row.payload, "anchor_rule_id"); got != rules[0].id {
		t.Errorf("the row carries anchor_rule_id %q, want the rule it was written for %q -- a row naming "+
			"no readable rule cannot answer why a later extraction picked a value", got, rules[0].id)
	}
	if got, want := laString(row.payload, "layout_fingerprint"), clJobFingerprint(t, ctx, f.jobID); got != want {
		t.Errorf("the row carries layout_fingerprint %q, want the job's own stored %q", got, want)
	}
	if got := laString(row.payload, "field"); got != clField {
		t.Errorf("the row names field %q, want %q", got, clField)
	}
	if got := laString(row.payload, "relation"); got != string(extraction.RelSameToken) {
		t.Errorf("the row names relation %q, want %q -- the box sits on the anchor token", got, extraction.RelSameToken)
	}
	if got := laString(row.payload, "shape"); got != string(extraction.ShapeTIN) {
		t.Errorf("the row names shape %q, want %q", got, extraction.ShapeTIN)
	}

	// AC-2 end to end: the write-time trigger resolved the row to the invoice's company. A NULL
	// here is audit_log's spelling for "firm-wide", and the column is append-only.
	want := laInvoiceEntity(t, ctx, f.invoiceID)
	if row.entityID == nil {
		t.Errorf("the row's entity_id IS NULL, want %s -- Migration B is what attributes this event", want)
	} else if *row.entityID != want {
		t.Errorf("the row's entity_id = %s, want the invoice's own %s", *row.entityID, want)
	}
}

// --- A-02 / AC-1: the four branches that learn nothing -------------------------------------

// Each arm ends with the SAME request made learnable on the SAME fixture. Without that control
// every zero below also holds on a handler that never emits at all.
func TestRLS_ACorrectionThatLearnsNothingWritesNoAnchorLearnedRow(t *testing.T) {
	for _, tc := range []struct {
		name string
		// arrange prepares the branch and returns the body the no-rule request sends.
		arrange func(t *testing.T, ctx context.Context, f clFixture) string
	}{
		{
			// B1: any method but pointed.
			name: "a typed correction on a layout-bearing job",
			arrange: func(t *testing.T, ctx context.Context, f clFixture) string {
				clLayout(t, ctx, f.jobID)
				return corBody(clTINValue, "typed", "")
			},
		},
		{
			// B2: pointed, but the job recorded no layout the box could anchor to.
			name: "a pointed correction on a job with no layout",
			arrange: func(t *testing.T, ctx context.Context, f clFixture) string {
				return clPointedBody(clTINValue, clTokenRegion(t, clPages(t), clTINToken), "")
			},
		},
		{
			// B3: pointed, fingerprint set, layout_anchors empty.
			name: "a pointed correction on a layout with no anchors",
			arrange: func(t *testing.T, ctx context.Context, f clFixture) string {
				pages := clPages(t)
				clLayoutRaw(t, ctx, f.jobID, extraction.Fingerprint(pages), []byte("[]"))
				return clPointedBody(clTINValue, clTokenRegion(t, pages, clTINToken), "")
			},
		},
		{
			// B4: pointed, layout present, the box relates to no anchor.
			name: "a pointed correction whose box anchors to nothing",
			arrange: func(t *testing.T, ctx context.Context, f clFixture) string {
				clLayout(t, ctx, f.jobID)
				return clPointedBody(clTINValue, clEmptyCorner, "")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			f := clSeed(t, ctx, "EXTR14-07-A02-"+strconv.Itoa(len(tc.name)))
			body := tc.arrange(t, ctx, f)

			rec := &laRecorder{}
			w := cxServe(t, f.reqCtx, f.jobID, clField, body, cxApplier(false, nil), cxAuditor(nil), rec.record)
			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want %d -- refusing to learn is not refusing the request (body=%q)",
					w.Code, http.StatusCreated, w.Body.String())
			}
			if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
				t.Fatalf("%d anchor rule(s) written, want 0 -- this arm is not the branch it claims to be", n)
			}
			if rec.calls != 0 {
				t.Errorf("the learning seam ran %d time(s) with no rule written, want 0", rec.calls)
			}
			if n := len(laRows(t, ctx, f.tenantID)); n != 0 {
				t.Errorf("%d %s row(s) with no rule written, want 0 -- the event claims a rule a reader "+
					"can never find", n, laEvent)
			}
			// The human action is still recorded, so the zeros above are not a refused request.
			if n := cxCorrectionRows(t, ctx, f.jobID); n != 1 {
				t.Errorf("%d correction row(s), want 1", n)
			}
			if n := len(cxCorrectionAudit(t, ctx, f.tenantID)); n != 1 {
				t.Errorf("%d %s row(s), want 1", n, cxEvent)
			}

			// The control: make the SAME fixture learnable and the row must appear.
			_, pages := clLayout(t, ctx, f.jobID)
			w2 := cxServe(t, f.reqCtx, f.jobID, clField,
				clPointedBody(clTINValue, clTokenRegion(t, pages, clTINToken), ""),
				cxApplier(false, nil), cxAuditor(nil), rec.record)
			if w2.Code != http.StatusCreated {
				t.Fatalf("control: status = %d, want %d (body=%q)", w2.Code, http.StatusCreated, w2.Body.String())
			}
			if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
				t.Fatalf("control: %d anchor rule(s), want 1 -- the arm above is then only a handler that never learns", n)
			}
			if n := len(laRows(t, ctx, f.tenantID)); n != 1 {
				t.Errorf("control: %d %s row(s) for one learned rule, want 1 -- the zero above proves nothing without this",
					n, laEvent)
			}
		})
	}
}

// --- A-01c / AC-1: a failed rule write leaves no row ---------------------------------------

func TestRLS_AFailedAnchorRuleWriteLeavesNoAnchorLearnedRow(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-07-A01C")
	_, pages := clLayout(t, ctx, f.jobID)
	body := clPointedBody(clTINValue, clTokenRegion(t, pages, clTINToken), "")

	drop := clFailAnchorRuleWrites(t, ctx, f.tenantID)

	var seen cxSeamCall
	rec := &laRecorder{}
	w := cxServe(t, f.reqCtx, f.jobID, clField, body, cxClearingApplier(&seen), cxAuditor(nil), rec.record)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d for a failed anchor-rule write (body=%q)",
			w.Code, http.StatusInternalServerError, w.Body.String())
	}
	if seen.calls != 1 {
		t.Fatalf("the invoice seam ran %d time(s), want 1 -- the zeros below would then prove nothing", seen.calls)
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("%d anchor rule(s) survived their own failed write, want 0", n)
	}
	if n := len(laRows(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("%d %s row(s) survived a failed rule write, want 0 -- the row names a rule id no row carries", n, laEvent)
	}

	drop()
	w2 := cxServe(t, f.reqCtx, f.jobID, clField, body, cxClearingApplier(&seen), cxAuditor(nil), rec.record)
	if w2.Code != http.StatusCreated {
		t.Fatalf("control: with the trigger dropped the same request answered %d (body=%q), want 201", w2.Code, w2.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Fatalf("control: %d anchor rule(s), want 1", n)
	}
	if n := len(laRows(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: %d %s row(s), want 1 -- the zero above proves nothing without this", n, laEvent)
	}
}

// --- A-01d / AC-1: the emit is INSIDE the transaction --------------------------------------

// The only spec that proves the emit shares the rule's fate rather than following it. A recorder
// that failed after the commit would leave the rule, the correction and the invoice edit behind.
func TestRLS_AFailedAnchorLearnedEmitRollsBackTheRuleTheCorrectionAndTheInvoice(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-07-A01D")
	_, pages := clLayout(t, ctx, f.jobID)
	body := clPointedBody(clTINValue, clTokenRegion(t, pages, clTINToken), "")

	var seen cxSeamCall
	rec := &laRecorder{fail: errors.New("forced anchor.learned emit failure")}
	w := cxServe(t, f.reqCtx, f.jobID, clField, body, cxClearingApplier(&seen), cxAuditor(nil), rec.record)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d -- a failed audit emit must take the request down with it (body=%q)",
			w.Code, http.StatusInternalServerError, w.Body.String())
	}
	if rec.calls != 1 {
		t.Fatalf("the learning seam ran %d time(s), want 1 -- the rollback below would then be a request that never reached the emit", rec.calls)
	}
	if seen.calls != 1 {
		t.Fatalf("the invoice seam ran %d time(s), want 1", seen.calls)
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("%d anchor rule(s) survived a failed emit, want 0 -- a rule with no audit row is a rule "+
			"nothing explains, and both tables are append-only", n)
	}
	if n := cxCorrectionRows(t, ctx, f.jobID); n != 0 {
		t.Errorf("%d correction row(s) survived a failed emit, want 0", n)
	}
	if n := len(cxCorrectionAudit(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("%d %s row(s) survived a failed emit, want 0", n, cxEvent)
	}
	if n := len(laRows(t, ctx, f.tenantID)); n != 0 {
		t.Errorf("%d %s row(s) survived a failed emit, want 0", n, laEvent)
	}
	if got := cxBuyerTIN(t, ctx, f.invoiceID); got == nil || *got != clReadingTIN {
		t.Errorf("invoices.buyer_tin = %s after a failed emit, want the unchanged %q", cxShowValue(got), clReadingTIN)
	}

	ok := &laRecorder{}
	w2 := cxServe(t, f.reqCtx, f.jobID, clField, body, cxClearingApplier(&seen), cxAuditor(nil), ok.record)
	if w2.Code != http.StatusCreated {
		t.Fatalf("control: the same request with a working recorder answered %d (body=%q), want 201", w2.Code, w2.Body.String())
	}
	if n := len(clRules(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: %d anchor rule(s), want 1 -- the four zeros above prove nothing without this", n)
	}
	if n := len(laRows(t, ctx, f.tenantID)); n != 1 {
		t.Errorf("control: %d %s row(s), want 1", n, laEvent)
	}
}

// --- A-03b / AC-3: the leak guard, end to end ----------------------------------------------

// The value the reviewer typed and the anchor text read off the document are both distinctive
// sentinels here, and both reach a stored column -- the correction row's value and its
// anchor_label. Neither may reach audit_log, which is append-only. The fingerprint control is
// what makes "contains neither" a report from a query that read a real payload.
func TestRLS_TheAnchorLearnedRowCarriesNoValueNoAnchorTextAndNoBox(t *testing.T) {
	ctx := t.Context()
	f := clSeed(t, ctx, "EXTR14-07-A03B")
	fingerprint, region := laSentinelLayout(t, ctx, f.jobID)

	rec := &laRecorder{}
	w := cxServe(t, f.reqCtx, f.jobID, clField, clPointedBody(laValueSentinel, region, ""),
		cxApplier(false, nil), cxAuditor(nil), rec.record)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}

	// Control needles: each sentinel really did reach a stored column, so its absence from the
	// audit payload is a decision rather than a fixture that never carried it.
	if got := cxCorrectionValue(t, ctx, f.jobID); got != laValueSentinel {
		t.Fatalf("control needle: the correction row's value is %q, want the sentinel %q -- the scan below reads nothing", got, laValueSentinel)
	}
	if label := clAnchorLabel(t, ctx, f.jobID, clField); label == nil || *label != laAnchorSentinel {
		t.Fatalf("control needle: the stored anchor_label is %s, want the sentinel %q -- the derive branch never ran",
			clShowLabel(label), laAnchorSentinel)
	}

	rows := laRows(t, ctx, f.tenantID)
	if len(rows) != 1 {
		t.Fatalf("%d %s row(s), want 1 -- there is no payload to scan", len(rows), laEvent)
	}
	raw := rows[0].raw
	if !strings.Contains(raw, fingerprint) {
		t.Fatalf("control needle: the stored payload %s does not carry the layout fingerprint %s -- a scan "+
			"that finds nothing forbidden is reading the wrong row", raw, fingerprint)
	}

	edges := laBoxEdges(region)
	if len(edges) == 0 {
		t.Fatalf("control needle: the pointed box %v renders no coordinate long enough to scan for, so the "+
			"no-box assertions below are vacuous", region)
	}

	// The struct dump, so a WIDENED AnchorLearned is visible even before an adapter spells the
	// new key: the recorder keeps what the handler handed it.
	handed, err := json.Marshal(rec.got)
	if err != nil {
		t.Fatalf("re-encode the handed AnchorLearned: %v", err)
	}
	for _, where := range []struct{ label, body string }{
		{"the stored payload", raw},
		{"the AnchorLearned the handler built", string(handed)},
	} {
		if strings.Contains(where.body, laValueSentinel) {
			t.Errorf("%s carries the corrected value: %s -- audit_log is append-only and the value is business content",
				where.label, where.body)
		}
		if strings.Contains(where.body, laAnchorSentinel) {
			t.Errorf("%s carries the anchor text: %s -- the anchor text is read verbatim off the document",
				where.label, where.body)
		}
		for _, edge := range edges {
			if strings.Contains(where.body, edge) {
				t.Errorf("%s carries the box edge %s: %s -- the region is where on the page a human pointed",
					where.label, edge, where.body)
			}
		}
	}
}

// laSentinelLayout stamps the corpus layout with the winning anchor's text replaced by a
// sentinel, and returns the fingerprint and the box to point at. Same-token: the box IS the
// anchor's own, which is what makes the derived label the sentinel.
func laSentinelLayout(t *testing.T, ctx context.Context, jobID string) (string, extraction.Region) {
	t.Helper()
	pages := clPages(t)
	obs := extraction.AnchorObservations(pages)

	region := clTokenRegion(t, pages, clTINToken)
	hits := 0
	for i := range obs {
		if obs[i].Text == clTINAnchor && obs[i].Page == region.Page &&
			obs[i].X0 == region.X0 && obs[i].Y0 == region.Y0 {
			obs[i].Text = laAnchorSentinel
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("rewrote %d anchor observation(s) named %q on the pointed token's box, want exactly 1 -- "+
			"the fixture no longer carries the sentinel", hits, clTINAnchor)
	}

	raw, err := extraction.MarshalAnchorObservations(obs)
	if err != nil {
		t.Fatalf("marshal the sentinel anchors: %v", err)
	}
	fp := extraction.Fingerprint(pages)
	clLayoutRaw(t, ctx, jobID, fp, raw)
	return fp, region
}

// laBoxEdges renders the coordinates a leaked box would print as. Short renderings are dropped:
// "0.5" could occur inside a uuid-free payload by chance, and a false positive here is worse
// than the one edge it would have caught.
func laBoxEdges(r extraction.Region) []string {
	out := []string{}
	for _, v := range []float64{r.X0, r.Y0, r.X1, r.Y1} {
		s := strconv.FormatFloat(v, 'f', -1, 64)
		if len(s) >= 6 {
			out = append(out, s)
		}
	}
	return out
}
