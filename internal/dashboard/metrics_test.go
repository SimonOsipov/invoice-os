// RED specs for METR-01-02: Bucket.Metrics (per-entity metric aggregation)
// and the generic addMetrics summing loop. Written before store.go's Q1
// gains its metric columns -- see MET-01..MET-17 in the task-426 spec.
package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- fixture helpers local to this file -------------------------------

// activeRuleSetVersionID looks up the one row rule_set_versions_one_active
// guarantees exists.
func activeRuleSetVersionID(t *testing.T, super *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`SELECT id FROM rule_set_versions WHERE is_active`,
	).Scan(&id); err != nil {
		t.Fatalf("look up active rule_set_version_id: %v", err)
	}
	return id
}

// stampRuleSetVersion force-writes rule_set_version_id on an already-seeded
// invoice -- simulates a validated-then-edited-then-demoted row, where the
// row keeps its stamp despite being draft again (Store.Edit never clears it).
func stampRuleSetVersion(t *testing.T, super *pgxpool.Pool, invoiceID, ruleSetVersionID string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET rule_set_version_id = $1 WHERE id = $2`, ruleSetVersionID, invoiceID,
	); err != nil {
		t.Fatalf("stamp rule_set_version_id: %v", err)
	}
}

// seedInvoiceWithVAT seeds a plain draft invoice then force-writes vat.
func seedInvoiceWithVAT(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number, vat string) string {
	t.Helper()
	id := seedInvoice(t, super, tenantID, entityID, number)
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET vat = $1::numeric WHERE id = $2`, vat, id,
	); err != nil {
		t.Fatalf("stamp vat: %v", err)
	}
	return id
}

func rollupFor(t *testing.T, app *pgxpool.Pool, tenantID string) Rollup {
	t.Helper()
	store := NewStore(app)
	cA := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})
	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	return got
}

// --- MET-01..MET-05: single-invoice classification ---------------------

// MET-01: one clean validated invoice is readiness {1,1}.
func TestMetrics_CleanValidatedInvoiceIsReady(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MET-01 tenant")
	entityID := seedEntity(t, super, tenantID, "MET-01 entity")
	seedInvoiceAtStatus(t, super, tenantID, entityID, "MET-01-1", "validated")

	got := rollupFor(t, app, tenantID)
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want 1", len(got.Clients))
	}
	want := Metric{Num: 1, Den: 1}
	if m := got.Clients[0].Metrics["readiness"]; m != want {
		t.Errorf("readiness = %+v, want %+v", m, want)
	}
}

// MET-02: a fresh draft (rule_set_version_id IS NULL) is never_validated,
// not readiness.
func TestMetrics_FreshDraftIsNeverValidated(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MET-02 tenant")
	entityID := seedEntity(t, super, tenantID, "MET-02 entity")
	seedInvoice(t, super, tenantID, entityID, "MET-02-1")

	got := rollupFor(t, app, tenantID)
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want 1", len(got.Clients))
	}
	row := got.Clients[0]
	if m := row.Metrics["readiness"]; m != (Metric{Num: 0, Den: 1}) {
		t.Errorf("readiness = %+v, want {0 1}", m)
	}
	if m := row.Metrics["never_validated"]; m != (Metric{Num: 1, Den: 1}) {
		t.Errorf("never_validated = %+v, want {1 1}", m)
	}
}

// MET-03 (regression): a draft with violations='[]' but a NON-NULL
// rule_set_version_id -- Store.Edit's demote-to-draft leaves the prior
// verdict's rule_set_version_id stamp stale (internal/invoice/store.go:
// 1137-1140), it does not null it out. This must NOT count as
// never_validated -- the naive `violations = '[]'` predicate would wrongly
// mark it never_validated and wrongly deny it readiness.
func TestMetrics_EditedThenDemotedDraftIsNotNeverValidated(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MET-03 tenant")
	entityID := seedEntity(t, super, tenantID, "MET-03 entity")
	rsv := activeRuleSetVersionID(t, super)
	invID := seedInvoiceWithViolations(t, super, tenantID, entityID, "MET-03-1", "draft", `[]`)
	stampRuleSetVersion(t, super, invID, rsv)

	got := rollupFor(t, app, tenantID)
	if len(got.Clients) != 1 {
		t.Fatalf("Clients = %d rows, want 1", len(got.Clients))
	}
	row := got.Clients[0]
	if m := row.Metrics["readiness"]; m != (Metric{Num: 1, Den: 1}) {
		t.Errorf("readiness = %+v, want {1 1}", m)
	}
	if m := row.Metrics["never_validated"]; m != (Metric{Num: 0, Den: 1}) {
		t.Errorf("never_validated = %+v, want {0 1} (edited-then-demoted, not never validated)", m)
	}
}

// MET-04: a rejected invoice is failed_in_transmission, not readiness, not
// blocked_by_rules (that arm requires status='draft'), and passes all three
// bars (its violations default to '[]').
func TestMetrics_RejectedInvoiceIsFailedInTransmission(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MET-04 tenant")
	entityID := seedEntity(t, super, tenantID, "MET-04 entity")
	seedInvoiceAtStatus(t, super, tenantID, entityID, "MET-04-1", "rejected")

	got := rollupFor(t, app, tenantID)
	row := got.Clients[0]
	cases := map[string]Metric{
		"readiness":              {Num: 0, Den: 1},
		"failed_in_transmission": {Num: 1, Den: 1},
		"blocked_by_rules":       {Num: 0, Den: 1},
		"bar_field_completeness": {Num: 1, Den: 1},
		"bar_tax_accuracy":       {Num: 1, Den: 1},
		"bar_identifiers_format": {Num: 1, Den: 1},
	}
	for key, want := range cases {
		if got := row.Metrics[key]; got != want {
			t.Errorf("%s = %+v, want %+v", key, got, want)
		}
	}
}

// MET-05: a failed invoice resolved outside (kept_as_is_at set) is NOT
// failed_in_transmission, and is ready.
func TestMetrics_ResolvedOutsideFailedInvoiceIsReady(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MET-05 tenant")
	entityID := seedEntity(t, super, tenantID, "MET-05 entity")
	seedResolvedFailed(t, super, tenantID, entityID, "MET-05-1")

	got := rollupFor(t, app, tenantID)
	row := got.Clients[0]
	if m := row.Metrics["failed_in_transmission"]; m != (Metric{Num: 0, Den: 1}) {
		t.Errorf("failed_in_transmission = %+v, want {0 1}", m)
	}
	if m := row.Metrics["readiness"]; m != (Metric{Num: 1, Den: 1}) {
		t.Errorf("readiness = %+v, want {1 1}", m)
	}
}

// --- MET-06: cross-status invariant -------------------------------------

// MET-06: blocked_by_rules.num + failed_in_transmission.num == needs_attention
// (the pinned column) over every status, crossed with {clean, error
// violation}, plus one resolved-failed row (the only status the kept_as_is
// triple can attach to -- T3-2/invoices_kept_as_is_status).
func TestMetrics_BlockedPlusFailedEqualsNeedsAttention(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MET-06 tenant")
	entityID := seedEntity(t, super, tenantID, "MET-06 entity")

	statuses := []string{"draft", "validated", "queued", "submitted", "accepted", "rejected", "failed"}
	clean := `[]`
	broken := `[{"rule_key":"x","severity":"error","message":"x"}]`
	for i, s := range statuses {
		seedInvoiceWithViolations(t, super, tenantID, entityID, fmt.Sprintf("MET-06-clean-%d", i), s, clean)
		seedInvoiceWithViolations(t, super, tenantID, entityID, fmt.Sprintf("MET-06-broken-%d", i), s, broken)
	}
	seedResolvedFailed(t, super, tenantID, entityID, "MET-06-resolved")

	got := rollupFor(t, app, tenantID)
	row := got.Clients[0]
	sum := row.Metrics["blocked_by_rules"].Num + row.Metrics["failed_in_transmission"].Num
	if sum != int64(row.NeedsAttention) {
		t.Errorf("blocked_by_rules.num(%d) + failed_in_transmission.num(%d) = %d, want needs_attention %d",
			row.Metrics["blocked_by_rules"].Num, row.Metrics["failed_in_transmission"].Num, sum, row.NeedsAttention)
	}
	// Must also hold at the tenant level (Totals is additive).
	tSum := got.Totals.Metrics["blocked_by_rules"].Num + got.Totals.Metrics["failed_in_transmission"].Num
	if tSum != int64(got.Totals.NeedsAttention) {
		t.Errorf("Totals: blocked_by_rules.num + failed_in_transmission.num = %d, want needs_attention %d", tSum, got.Totals.NeedsAttention)
	}
}

// --- MET-07..MET-10: bar category isolation -----------------------------

// MET-07: a vat-standard-rate error (tax-accuracy category) lowers only
// bar_tax_accuracy, leaving field and identifier bars untouched. Status is
// 'validated' (not 'draft') so is_never_validated cannot mask the bar
// result -- never_validated's arm is status='draft' only.
func TestMetrics_TaxErrorLowersOnlyTaxBar(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MET-07 tenant")
	entityID := seedEntity(t, super, tenantID, "MET-07 entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "MET-07-1", "validated",
		`[{"rule_key":"vat-standard-rate","severity":"error","message":"x"}]`)

	got := rollupFor(t, app, tenantID)
	row := got.Clients[0]
	if m := row.Metrics["bar_tax_accuracy"]; m != (Metric{Num: 0, Den: 1}) {
		t.Errorf("bar_tax_accuracy = %+v, want {0 1}", m)
	}
	if m := row.Metrics["bar_field_completeness"]; m != (Metric{Num: 1, Den: 1}) {
		t.Errorf("bar_field_completeness = %+v, want {1 1}", m)
	}
	if m := row.Metrics["bar_identifiers_format"]; m != (Metric{Num: 1, Den: 1}) {
		t.Errorf("bar_identifiers_format = %+v, want {1 1}", m)
	}
}

// MET-08: two errors in the SAME category (tax) count once, not twice --
// den stays 1, num drops to 0 exactly (never negative).
func TestMetrics_TwoErrorsSameCategoryCountOnce(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MET-08 tenant")
	entityID := seedEntity(t, super, tenantID, "MET-08 entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "MET-08-1", "validated",
		`[{"rule_key":"vat-standard-rate","severity":"error","message":"a"},`+
			`{"rule_key":"vat-non-negative","severity":"error","message":"b"}]`)

	got := rollupFor(t, app, tenantID)
	if m := got.Clients[0].Metrics["bar_tax_accuracy"]; m != (Metric{Num: 0, Den: 1}) {
		t.Errorf("bar_tax_accuracy = %+v, want {0 1}", m)
	}
}

// MET-09: one invoice failing a field rule AND an identifier rule lowers
// BOTH bars, and leaves tax alone.
func TestMetrics_FieldAndIdentifierErrorsLowerBothBars(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MET-09 tenant")
	entityID := seedEntity(t, super, tenantID, "MET-09 entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "MET-09-1", "validated",
		`[{"rule_key":"supplier-tin-required","severity":"error","message":"a"},`+
			`{"rule_key":"supplier-tin-format","severity":"error","message":"b"}]`)

	got := rollupFor(t, app, tenantID)
	row := got.Clients[0]
	if m := row.Metrics["bar_field_completeness"]; m != (Metric{Num: 0, Den: 1}) {
		t.Errorf("bar_field_completeness = %+v, want {0 1}", m)
	}
	if m := row.Metrics["bar_identifiers_format"]; m != (Metric{Num: 0, Den: 1}) {
		t.Errorf("bar_identifiers_format = %+v, want {0 1}", m)
	}
	if m := row.Metrics["bar_tax_accuracy"]; m != (Metric{Num: 1, Den: 1}) {
		t.Errorf("bar_tax_accuracy = %+v, want {1 1}", m)
	}
}

// MET-10: a severity:"warning" violation lowers no bar.
func TestMetrics_WarningSeverityLowersNoBar(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MET-10 tenant")
	entityID := seedEntity(t, super, tenantID, "MET-10 entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "MET-10-1", "validated",
		`[{"rule_key":"vat-standard-rate","severity":"warning","message":"x"}]`)

	got := rollupFor(t, app, tenantID)
	row := got.Clients[0]
	for _, key := range []string{"bar_field_completeness", "bar_tax_accuracy", "bar_identifiers_format"} {
		if m := row.Metrics[key]; m != (Metric{Num: 1, Den: 1}) {
			t.Errorf("%s = %+v, want {1 1} (warning must not lower a bar)", key, m)
		}
	}
}

// --- MET-11: additivity, including differently-keyed maps --------------

// MET-11a (pure, no DB): addMetrics is the ONE generic loop -- a key
// present in only one of several src maps must still land in dst, summed
// independently from every other key, num and den kept separate.
func TestMetrics_AddMetricsUnionsKeysAcrossDifferentlyShapedMaps(t *testing.T) {
	dst := map[string]Metric{}
	addMetrics(dst, map[string]Metric{"readiness": {Num: 1, Den: 2}})
	addMetrics(dst, map[string]Metric{"never_validated": {Num: 3, Den: 4}})
	addMetrics(dst, map[string]Metric{"readiness": {Num: 5, Den: 6}, "vat_tracked": {Num: 100, Den: 1}})

	want := map[string]Metric{
		"readiness":       {Num: 6, Den: 8},
		"never_validated": {Num: 3, Den: 4},
		"vat_tracked":     {Num: 100, Den: 1},
	}
	if !reflect.DeepEqual(dst, want) {
		t.Errorf("dst = %+v, want %+v", dst, want)
	}
}

// MET-11b (DB-backed): Totals.Metrics is the element-wise sum of three
// differently-shaped clients (one all-ready, one all-never-validated, one
// mixed), num and den checked separately for every key.
func TestMetrics_TotalsSumsThreeDifferentlyShapedClients(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MET-11 tenant")
	ready := seedEntity(t, super, tenantID, "MET-11 ready")
	unvalidated := seedEntity(t, super, tenantID, "MET-11 unvalidated")
	mixed := seedEntity(t, super, tenantID, "MET-11 mixed")

	seedInvoiceAtStatus(t, super, tenantID, ready, "MET-11-r1", "validated")
	seedInvoiceAtStatus(t, super, tenantID, ready, "MET-11-r2", "accepted")

	seedInvoice(t, super, tenantID, unvalidated, "MET-11-u1")

	seedInvoiceAtStatus(t, super, tenantID, mixed, "MET-11-m1", "rejected")
	seedResolvedFailed(t, super, tenantID, mixed, "MET-11-m2")

	got := rollupFor(t, app, tenantID)
	if len(got.Clients) != 3 {
		t.Fatalf("Clients = %d rows, want 3", len(got.Clients))
	}

	want := map[string]Metric{}
	for _, c := range got.Clients {
		addMetrics(want, c.Metrics)
	}
	if !reflect.DeepEqual(got.Totals.Metrics, want) {
		t.Errorf("Totals.Metrics = %+v, want (sum of Clients) %+v", got.Totals.Metrics, want)
	}
	// Pin an exact, independently-derived expectation too -- summing
	// Clients back into Totals alone would pass even if every client row
	// were miscounted identically.
	wantExact := map[string]Metric{
		"readiness":              {Num: 3, Den: 5}, // r1, r2, m2(resolved) ready; u1 never-validated; m1 failed-in-transmission
		"bar_field_completeness": {Num: 4, Den: 5}, // every row but u1 (never-validated) passes clean bars
		"bar_tax_accuracy":       {Num: 4, Den: 5},
		"bar_identifiers_format": {Num: 4, Den: 5},
		"blocked_by_rules":       {Num: 0, Den: 5},
		"failed_in_transmission": {Num: 1, Den: 5}, // m1 only; m2 is resolved
		"never_validated":        {Num: 1, Den: 5}, // u1 only
		"vat_tracked":            {Num: 0, Den: 5}, // no vat set on any seeded row
	}
	if !reflect.DeepEqual(got.Totals.Metrics, wantExact) {
		t.Errorf("Totals.Metrics = %+v, want %+v", got.Totals.Metrics, wantExact)
	}
}

// --- MET-12: empty tenant -------------------------------------------------

// MET-12: an empty tenant marshals "metrics":{}, never null, and Totals
// carries no key at den:0 (the map itself is empty, not populated with
// zeroed entries).
func TestMetrics_EmptyTenantMarshalsEmptyObject(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MET-12 tenant")

	got := rollupFor(t, app, tenantID)
	if got.Totals.Metrics == nil {
		t.Error("Totals.Metrics is nil, want a non-nil empty map")
	}
	if len(got.Totals.Metrics) != 0 {
		t.Errorf("Totals.Metrics = %+v, want empty", got.Totals.Metrics)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !bytes.Contains(body, []byte(`"metrics":{}`)) {
		t.Errorf("marshalled body = %s, want it to contain \"metrics\":{}", body)
	}
	if bytes.Contains(body, []byte(`"metrics":null`)) {
		t.Errorf("marshalled body = %s, want \"metrics\":{} not null", body)
	}
}

// --- MET-13: vat_tracked kobo conversion ----------------------------------

// MET-13: vat_tracked sums round(vat*100) in kobo; a NULL vat adds 0 to num
// but still counts toward den.
func TestMetrics_VATTrackedSumsKoboIgnoringNullNumerator(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MET-13 tenant")
	entityID := seedEntity(t, super, tenantID, "MET-13 entity")
	seedInvoiceWithVAT(t, super, tenantID, entityID, "MET-13-1", "123.45")
	seedInvoice(t, super, tenantID, entityID, "MET-13-2") // vat left NULL

	got := rollupFor(t, app, tenantID)
	want := Metric{Num: 12345, Den: 2}
	if m := got.Clients[0].Metrics["vat_tracked"]; m != want {
		t.Errorf("vat_tracked = %+v, want %+v", m, want)
	}
}

// --- MET-14: non-array violations shapes -----------------------------------

// MET-14: a non-array violations value (object, scalar) must neither error
// the query nor register a bar failure. Status is 'validated' so
// never_validated cannot mask the bar result.
func TestMetrics_NonArrayViolationsNeitherErrorsNorFlagsABar(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MET-14 tenant")
	entityID := seedEntity(t, super, tenantID, "MET-14 entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "MET-14-obj", "validated", `{"oops":true}`)
	seedInvoiceWithViolations(t, super, tenantID, entityID, "MET-14-scalar", "validated", `"oops"`)

	got := rollupFor(t, app, tenantID) // must not error
	row := got.Clients[0]
	for _, key := range []string{"bar_field_completeness", "bar_tax_accuracy", "bar_identifiers_format"} {
		if m := row.Metrics[key]; m != (Metric{Num: 2, Den: 2}) {
			t.Errorf("%s = %+v, want {2 2} (non-array violations must not flag a bar failure)", key, m)
		}
	}
}

// --- MET-15: pathological overlap never goes negative ----------------------

// MET-15: a row that is BOTH never_validated (draft, rule_set_version_id
// NULL) AND carries an error violation (blocked_by_rules) must still leave
// readiness_num and every bar_*_num non-negative -- these are FILTER
// counts, never subtractions ([non-negative-by-filter]).
func TestMetrics_PathologicalOverlapNeverGoesNegative(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MET-15 tenant")
	entityID := seedEntity(t, super, tenantID, "MET-15 entity")
	seedInvoiceWithViolations(t, super, tenantID, entityID, "MET-15-1", "draft",
		`[{"rule_key":"vat-standard-rate","severity":"error","message":"x"}]`)

	got := rollupFor(t, app, tenantID)
	row := got.Clients[0]
	for _, key := range []string{
		"readiness", "bar_field_completeness", "bar_tax_accuracy", "bar_identifiers_format",
		"blocked_by_rules", "failed_in_transmission", "never_validated",
	} {
		if m := row.Metrics[key]; m.Num < 0 || m.Den < 0 {
			t.Errorf("%s = %+v, want both fields >= 0", key, m)
		}
	}
	// The overlap itself must actually be exercised, or this test proves
	// nothing.
	if m := row.Metrics["blocked_by_rules"]; m.Num != 1 {
		t.Errorf("blocked_by_rules.num = %d, want 1 (the pathological row must register as blocked)", m.Num)
	}
	if m := row.Metrics["never_validated"]; m.Num != 1 {
		t.Errorf("never_validated.num = %d, want 1 (the pathological row must also register as never-validated)", m.Num)
	}
}

// --- MET-16: den == total invoice count everywhere --------------------------

// MET-16: every emitted metric's den equals the client's own total invoice
// count -- checked per client (different counts on purpose) and on Totals.
func TestMetrics_DenAlwaysEqualsInvoiceCount(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MET-16 tenant")
	small := seedEntity(t, super, tenantID, "MET-16 small")
	big := seedEntity(t, super, tenantID, "MET-16 big")

	seedInvoiceAtStatus(t, super, tenantID, small, "MET-16-s1", "draft")
	seedInvoiceAtStatus(t, super, tenantID, small, "MET-16-s2", "validated")

	for i, s := range []string{"draft", "validated", "queued", "submitted", "accepted"} {
		seedInvoiceAtStatus(t, super, tenantID, big, fmt.Sprintf("MET-16-b%d", i), s)
	}

	got := rollupFor(t, app, tenantID)
	if len(got.Clients) != 2 {
		t.Fatalf("Clients = %d rows, want 2", len(got.Clients))
	}
	for _, c := range got.Clients {
		total := c.Counts.Draft + c.Counts.Validated + c.Counts.Queued + c.Counts.Submitted +
			c.Counts.Accepted + c.Counts.Rejected + c.Counts.Failed
		if len(c.Metrics) != 8 {
			t.Fatalf("entity %s: Metrics has %d keys, want 8", c.EntityName, len(c.Metrics))
		}
		for key, m := range c.Metrics {
			if m.Den != int64(total) {
				t.Errorf("entity %s metric %s: den = %d, want %d (its own invoice count)", c.EntityName, key, m.Den, total)
			}
		}
	}
	for key, m := range got.Totals.Metrics {
		if m.Den != 7 {
			t.Errorf("Totals metric %s: den = %d, want 7 (tenant-wide invoice count)", key, m.Den)
		}
	}
}

// --- MET-17: readiness derived from the pinned column -----------------------

// MET-17: on a fixture with no pathological (overlapping-flag) rows,
// readiness.num == total - needs_attention - never_validated, where
// needs_attention is read from the PINNED column
// (TestStoreRollup_NeedsAttentionSQLRejectedArmIsBare) -- the only
// structural tie between the new CTE flags and that literal column.
func TestMetrics_ReadinessEqualsTotalMinusNeedsAttentionMinusNeverValidated(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "MET-17 tenant")
	entityID := seedEntity(t, super, tenantID, "MET-17 entity")
	rsv := activeRuleSetVersionID(t, super)

	seedInvoice(t, super, tenantID, entityID, "MET-17-neverval") // never_validated
	seedInvoiceAtStatus(t, super, tenantID, entityID, "MET-17-rejected", "rejected")
	seedInvoiceAtStatus(t, super, tenantID, entityID, "MET-17-failed", "failed") // unresolved
	seedResolvedFailed(t, super, tenantID, entityID, "MET-17-resolved")
	seedInvoiceAtStatus(t, super, tenantID, entityID, "MET-17-clean", "validated")
	blockedID := seedInvoiceWithViolations(t, super, tenantID, entityID, "MET-17-blocked", "draft",
		`[{"rule_key":"x","severity":"error","message":"x"}]`)
	stampRuleSetVersion(t, super, blockedID, rsv) // avoid also being never_validated -- no pathological rows

	got := rollupFor(t, app, tenantID)
	row := got.Clients[0]
	total := row.Counts.Draft + row.Counts.Validated + row.Counts.Queued + row.Counts.Submitted +
		row.Counts.Accepted + row.Counts.Rejected + row.Counts.Failed
	want := int64(total) - int64(row.NeedsAttention) - row.Metrics["never_validated"].Num
	if got := row.Metrics["readiness"].Num; got != want {
		t.Errorf("readiness.num = %d, want %d (total %d - needs_attention %d - never_validated %d)",
			got, want, total, row.NeedsAttention, row.Metrics["never_validated"].Num)
	}
}
