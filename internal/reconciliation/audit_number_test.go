// audit_number_test.go: AUDIT-11-04 Mode A. The acceptance tests for "both reconciliation
// writers carry invoice_number", authored RED before scanQuery is widened.
//
// Sites are addressed by EVENT NAME, never by line number alone (CF-11). Reuses
// fixture_test.go's harness, seeds and helpers rather than declaring a second set.
//
// NAMING (CF-18). `make test-reconciliation` and .github/workflows/ci.yml both run
// `-run TestRLS ./internal/reconciliation/...`, so a case named otherwise never executes
// anywhere and reports as a pass. Every case here carries the TestRLS_ prefix, including
// the pure ones -- the same choice fixture_test.go's
// TestRLS_ApprovalDriftKindConstantsMatchScanQuery already makes in this package.
//
// NO COMPILE-TIME REFERENCE TO Finding.InvoiceNumber. The field does not exist yet; naming
// it directly would break this package's whole test binary instead of failing one
// assertion. The two cases that need the field reach it through reflect and fail with a
// message, so every other case in the package keeps running.
//
// Run: export the four DSNs (DEV_DB_PORT alone silently skips -- CF-6) and
// `go test -p 1 -count=1 -run TestRLS ./internal/reconciliation/...`.
package reconciliation

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// auditNumberKey is the ONE spelling, settled by AUDIT-11-01, verbatim from
// invoices.invoice_number (D-1, D-2).
const auditNumberKey = "invoice_number"

const (
	eventDriftDetected = "reconciliation.drift_detected"
	eventAutoFixed     = "reconciliation.auto_fixed"
)

// rcFindingNumberField is the Finding field scanQuery's new column lands in. Held as a
// string because the field does not exist yet (see the package note above).
const rcFindingNumberField = "InvoiceNumber"

// rcFindingEqualCallSites is what findingEqual (fixture_test.go) currently decides: twelve
// exact-Finding comparisons across scan_test.go and adversarial_test.go. Quoted in the
// failure message so the cost of leaving the field uncompared is legible.
const rcFindingEqualCallSites = 12

// rcScanArmCount floors rcScanArms: scanQuery is a ten-arm UNION ALL. A short table would
// satisfy every assertion inside the loop vacuously, which is exactly how "widened nine
// arms, missed the tenth" survives a green suite.
const rcScanArmCount = 10

// --- read-back ---------------------------------------------------------------------------

// rcAuditRow is one audit_log row plus both payload-derived columns.
type rcAuditRow struct {
	rows      int
	payload   json.RawMessage
	number    *string // payload->>'invoice_number'; nil means the key is absent
	invoiceID *string
	entityID  *string
}

// rcReadAuditRow returns the newest audit_log row for tenantID+event and how many rows that
// event has. ->> yields NULL for an absent key and "" for a present empty string, so number
// distinguishes the two.
func rcReadAuditRow(t *testing.T, h *harness, tenantID, event string) rcAuditRow {
	t.Helper()
	return rcReadAuditRowWhere(t, h, tenantID, event, "")
}

// rcReadAuditRowForKind narrows to the row one drift kind produced.
func rcReadAuditRowForKind(t *testing.T, h *harness, tenantID, event string, kind DriftKind) rcAuditRow {
	t.Helper()
	return rcReadAuditRowWhere(t, h, tenantID, event, string(kind))
}

func rcReadAuditRowWhere(t *testing.T, h *harness, tenantID, event, kind string) rcAuditRow {
	t.Helper()
	ctx := context.Background()

	where := `tenant_id = $1 AND event = $2`
	args := []any{tenantID, event}
	if kind != "" {
		where += ` AND payload->>'drift_kind' = $3`
		args = append(args, kind)
	}

	var r rcAuditRow
	if err := h.super.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE `+where, args...,
	).Scan(&r.rows); err != nil {
		t.Fatalf("count audit_log rows for %q: %v", event, err)
	}
	if r.rows == 0 {
		return r // the caller's floor reports this; there is nothing to scan
	}
	if err := h.super.QueryRow(ctx,
		`SELECT payload, payload->>'`+auditNumberKey+`', invoice_id::text, entity_id::text
		   FROM audit_log WHERE `+where+`
		  ORDER BY created_at DESC, id DESC LIMIT 1`, args...,
	).Scan(&r.payload, &r.number, &r.invoiceID, &r.entityID); err != nil {
		t.Fatalf("read audit_log row for %q: %v", event, err)
	}
	return r
}

// rcAssertOneAuditRow fails when the sweep wrote no row -- every assertion after that would
// read another fixture's row or none at all.
func rcAssertOneAuditRow(t *testing.T, row rcAuditRow, event, what string) {
	t.Helper()
	if row.rows != 1 {
		t.Fatalf("%s (%s): the tenant holds %d rows, want exactly 1 -- the fixture did not drive "+
			"the writer", event, what, row.rows)
	}
}

// rcAssertNumber pins the payload value against the number read back out of invoices --
// never a literal this test wrote, and never merely "the key is there".
func rcAssertNumber(t *testing.T, row rcAuditRow, event, what, want string) {
	t.Helper()
	if row.number == nil {
		t.Errorf("%s (%s): payload->>'%s' is SQL NULL (key absent); payload = %s",
			event, what, auditNumberKey, row.payload)
		return
	}
	if *row.number == "" {
		t.Errorf("%s (%s): payload->>'%s' is the empty string; audit_log rows are immutable, so a "+
			"blank frozen now is permanent; payload = %s", event, what, auditNumberKey, row.payload)
		return
	}
	if *row.number != want {
		t.Errorf("%s (%s): payload->>'%s' = %q, want %q (invoices.invoice_number, read back from "+
			"the table)", event, what, auditNumberKey, *row.number, want)
	}
}

// rcInvoiceNumberFor reads invoices.invoice_number back out of the table, so no assertion
// compares the payload against a literal the test itself wrote.
func rcInvoiceNumberFor(t *testing.T, h *harness, invoiceID string) string {
	t.Helper()
	var n string
	if err := h.super.QueryRow(context.Background(),
		`SELECT invoice_number FROM invoices WHERE id = $1`, invoiceID).Scan(&n); err != nil {
		t.Fatalf("read invoices.invoice_number for %s: %v", invoiceID, err)
	}
	if n == "" {
		t.Fatalf("fixture invoice %s carries a blank invoice_number; every comparison against it "+
			"would be vacuous", invoiceID)
	}
	return n
}

func rcInvoiceEntityFor(t *testing.T, h *harness, invoiceID string) string {
	t.Helper()
	var e string
	if err := h.super.QueryRow(context.Background(),
		`SELECT entity_id::text FROM invoices WHERE id = $1`, invoiceID).Scan(&e); err != nil {
		t.Fatalf("read invoices.entity_id for %s: %v", invoiceID, err)
	}
	return e
}

// rcPayloadKeys returns payload's top-level keys, sorted.
func rcPayloadKeys(t *testing.T, payload json.RawMessage) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatalf("unmarshal audit payload %s: %v", payload, err)
	}
	return rcSortedKeys(m)
}

func rcSortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// rcAssertKeySet compares key sets in BOTH directions: a missing key and a smuggled extra
// key are different defects, and a one-directional check sees only one of them.
func rcAssertKeySet(t *testing.T, got, want []string, what string) {
	t.Helper()
	if len(got) == 0 {
		t.Fatalf("%s: the payload is empty {}, which would satisfy an allowlist vacuously", what)
	}
	inWant := map[string]bool{}
	for _, k := range want {
		inWant[k] = true
	}
	inGot := map[string]bool{}
	for _, k := range got {
		inGot[k] = true
	}
	for _, k := range want {
		if !inGot[k] {
			t.Errorf("%s: payload key %q is MISSING; got %v, want exactly %v", what, k, got, want)
		}
	}
	for _, k := range got {
		if !inWant[k] {
			t.Errorf("%s: payload key %q is not in the summary-only set; got %v, want exactly %v",
				what, k, got, want)
		}
	}
}

func rcDecodePayload(t *testing.T, payload json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatalf("unmarshal audit payload %s: %v", payload, err)
	}
	return m
}

// --- cleanup -----------------------------------------------------------------------------

func rcCleanupAudit(h *harness, tenantID string) {
	_, _ = h.super.Exec(context.Background(), `DELETE FROM audit_log WHERE tenant_id = $1`, tenantID)
}

// rcCleanupReArmKeys removes the idempotency_keys row ReArmPoll writes for a healed
// lost_poll, so a re-run of this suite heals rather than dedupes.
func rcCleanupReArmKeys(h *harness, tenantID, jobID string) {
	_, _ = h.super.Exec(context.Background(),
		`DELETE FROM idempotency_keys WHERE tenant_id = $1 AND key LIKE $2`,
		tenantID, "reconcile-poll:"+jobID+":%")
}

// rcCompose runs the tenant's audit cleanup first, then each cleanup in the order given --
// approval_runs -> invoices is ON DELETE RESTRICT, so the order is load-bearing.
func rcCompose(h *harness, tenantID string, cleanups ...func()) func() {
	return func() {
		rcCleanupAudit(h, tenantID)
		for _, c := range cleanups {
			c()
		}
	}
}

// --- statement tracing -------------------------------------------------------------------

// rcSQLRecorder records the SQL of every statement its pool issues, so a statement COUNT can
// be asserted -- the only way to see a per-finding lookup, whose results are identical.
type rcSQLRecorder struct {
	mu  sync.Mutex
	sql []string
}

var _ pgx.QueryTracer = (*rcSQLRecorder)(nil)

func (r *rcSQLRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	r.sql = append(r.sql, d.SQL)
	r.mu.Unlock()
	return ctx
}

func (r *rcSQLRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (r *rcSQLRecorder) reset() {
	r.mu.Lock()
	r.sql = nil
	r.mu.Unlock()
}

// mentioning filters to the statements containing substr, keeping the count immune to the
// pool's own begin/commit/set_config traffic.
func (r *rcSQLRecorder) mentioning(substr string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, s := range r.sql {
		if strings.Contains(s, substr) {
			out = append(out, s)
		}
	}
	return out
}

// rcTracedAppPool is a second invoice_app pool whose statements are recorded. Callers must
// already have gone through requireHarness, which owns the skip gate.
func rcTracedAppPool(t *testing.T) (*pgxpool.Pool, *rcSQLRecorder) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	rec := &rcSQLRecorder{}
	cfg.ConnConfig.Tracer = rec
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect traced app pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p, rec
}

// --- the field, reached without naming it ------------------------------------------------

// rcWithNumber returns f with its InvoiceNumber field set. reflect, not a struct literal:
// the field does not exist yet, and a compile-time reference would fail the package's whole
// test binary instead of this one assertion.
func rcWithNumber(t *testing.T, f Finding, number string) Finding {
	t.Helper()
	v := reflect.New(reflect.TypeOf(f)).Elem()
	v.Set(reflect.ValueOf(f))

	fld := v.FieldByName(rcFindingNumberField)
	if !fld.IsValid() {
		t.Fatalf("reconciliation.Finding has no %s field, so no scanQuery arm can carry the number "+
			"into driftPayload; want `%s string` on Finding", rcFindingNumberField, rcFindingNumberField)
	}
	if fld.Kind() != reflect.String {
		t.Fatalf("reconciliation.Finding.%s is %s, want string (invoices.invoice_number is text NOT "+
			"NULL)", rcFindingNumberField, fld.Kind())
	}
	fld.SetString(number)
	return v.Interface().(Finding)
}

// --- fixtures ----------------------------------------------------------------------------

// rcSeedDriftPair seeds ONE tenant holding both routings: a lost_poll invoice (healable ->
// reconciliation.auto_fixed) and a submitting_orphan invoice (flagged ->
// reconciliation.drift_detected). Both carry a submission_jobs row, so the two payload key
// sets differ by `action` alone.
func rcSeedDriftPair(t *testing.T, h *harness) (tenantID, healable, flagged string, cleanup func()) {
	t.Helper()
	overdue := time.Now().Add(-1 * time.Hour)

	tenantID, entityID, healable, cleanupHealable := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	healJobID, cleanupHealJob := rcSeedJob(t, h, tenantID, healable,
		rcJobOpts{state: "pending", attempts: 2, nextPollAt: &overdue})

	flagged, cleanupFlagged := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "submitted"})
	_, cleanupFlaggedJob := rcSeedJob(t, h, tenantID, flagged, rcJobOpts{state: "submitting", updatedAt: &overdue})

	return tenantID, healable, flagged, func() {
		rcCleanupReArmKeys(h, tenantID, healJobID)
		rcCleanupPollJobsFor(h, healJobID)
		rcCleanupAudit(h, tenantID)
		cleanupFlaggedJob()
		cleanupFlagged()
		cleanupHealJob()
		cleanupHealable()
	}
}

// --- AC-1: both events carry the number --------------------------------------------------

// TestRLS_AuditNumber_DriftAndAutoFixCarryTheNumber (AC-1): one SweepOnce over one tenant
// holding one healable and one flagged finding. Both audit rows must carry that invoice's
// own number. Driven through SweepOnce, never a hand-built Finding -- widening driftPayload
// alone would turn a Finding-literal case green while scanQuery stays unwidened and
// production writes blanks forever (CF-19).
func TestRLS_AuditNumber_DriftAndAutoFixCarryTheNumber(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, healable, flagged, cleanup := rcSeedDriftPair(t, h)
	defer cleanup()

	wantHealable := rcInvoiceNumberFor(t, h, healable)
	wantFlagged := rcInvoiceNumberFor(t, h, flagged)
	if wantHealable == wantFlagged {
		t.Fatalf("both fixture invoices carry invoice_number %q; a writer stamping the wrong "+
			"invoice's number would be invisible", wantHealable)
	}

	if err := rcReconciler(h).SweepOnce(ctx); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}

	auto := rcReadAuditRow(t, h, tenantID, eventAutoFixed)
	rcAssertOneAuditRow(t, auto, eventAutoFixed, "recordAutoFixAudit")
	rcAssertNumber(t, auto, eventAutoFixed, "recordAutoFixAudit", wantHealable)
	rcAssertKeySet(t, rcPayloadKeys(t, auto.payload),
		[]string{"invoice_id", "submission_job_id", "drift_kind", "action", auditNumberKey},
		eventAutoFixed+" payload")

	autoBody := rcDecodePayload(t, auto.payload)
	if got, _ := autoBody["invoice_id"].(string); got != healable {
		t.Errorf("%s: payload invoice_id = %q, want %q (unchanged -- the id spelling stays, D-3)",
			eventAutoFixed, got, healable)
	}
	if got, _ := autoBody["drift_kind"].(string); got != string(LostPoll) {
		t.Errorf("%s: payload drift_kind = %q, want %q (unchanged)", eventAutoFixed, got, LostPoll)
	}
	if got, _ := autoBody["action"].(string); got != "repoll_reenqueued" {
		t.Errorf("%s: payload action = %q, want %q (unchanged)", eventAutoFixed, got, "repoll_reenqueued")
	}

	drift := rcReadAuditRow(t, h, tenantID, eventDriftDetected)
	rcAssertOneAuditRow(t, drift, eventDriftDetected, "recordDriftAudit")
	rcAssertNumber(t, drift, eventDriftDetected, "recordDriftAudit", wantFlagged)
	rcAssertKeySet(t, rcPayloadKeys(t, drift.payload),
		[]string{"invoice_id", "submission_job_id", "drift_kind", auditNumberKey},
		eventDriftDetected+" payload")

	driftBody := rcDecodePayload(t, drift.payload)
	if got, _ := driftBody["invoice_id"].(string); got != flagged {
		t.Errorf("%s: payload invoice_id = %q, want %q (unchanged)", eventDriftDetected, got, flagged)
	}
	if got, _ := driftBody["drift_kind"].(string); got != string(SubmittingOrphan) {
		t.Errorf("%s: payload drift_kind = %q, want %q (unchanged)", eventDriftDetected, got, SubmittingOrphan)
	}
}

// TestRLS_AuditNumber_ScopedColumnsStillFillForReconciliationEvents (AC-1, CF-4): both
// payload-derived columns still fill once the sibling key is there. audit_log.invoice_id is
// a STORED generated column and audit_log.entity_id is filled by the
// audit_log_entity_on_insert trigger; both read payload->>'invoice_id' by name and neither
// iterates the key set, so an invoice_id-only assertion would not see entity_id regress.
func TestRLS_AuditNumber_ScopedColumnsStillFillForReconciliationEvents(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, healable, flagged, cleanup := rcSeedDriftPair(t, h)
	defer cleanup()

	if err := rcReconciler(h).SweepOnce(ctx); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}

	for _, c := range []struct {
		event   string
		what    string
		invoice string
	}{
		{eventAutoFixed, "recordAutoFixAudit", healable},
		{eventDriftDetected, "recordDriftAudit", flagged},
	} {
		t.Run(c.event, func(t *testing.T) {
			row := rcReadAuditRow(t, h, tenantID, c.event)
			rcAssertOneAuditRow(t, row, c.event, c.what)

			// "with the new key present" is half the claim: without this the case would
			// stay green on a payload this story never touched.
			if row.number == nil {
				t.Fatalf("%s (%s): payload->>'%s' is SQL NULL (key absent), so this case is not yet "+
					"asserting what its name says; payload = %s", c.event, c.what, auditNumberKey, row.payload)
			}

			if row.invoiceID == nil {
				t.Errorf("%s (%s): audit_log.invoice_id is NULL with %q present; payload = %s",
					c.event, c.what, auditNumberKey, row.payload)
			} else if *row.invoiceID != c.invoice {
				t.Errorf("%s (%s): audit_log.invoice_id = %q, want %q", c.event, c.what, *row.invoiceID, c.invoice)
			}

			wantEntity := rcInvoiceEntityFor(t, h, c.invoice)
			if row.entityID == nil {
				t.Errorf("%s (%s): audit_log.entity_id is NULL with %q present, which reads as a "+
					"firm-wide claim; payload = %s", c.event, c.what, auditNumberKey, row.payload)
			} else if *row.entityID != wantEntity {
				t.Errorf("%s (%s): audit_log.entity_id = %q, want %q (the invoice's entity)",
					c.event, c.what, *row.entityID, wantEntity)
			}
		})
	}
}

// --- AC-4: one construction site ---------------------------------------------------------

// TestRLS_AuditNumber_AutoFixAddsOnlyActionOnTopOfDrift (AC-4): both events are built by
// driftPayload and recordAutoFixAudit adds `action` and nothing else. Asserted as a set
// DIFFERENCE between two real rows written by one sweep, so a second construction site that
// spelled the number differently shows up as a key-set divergence.
func TestRLS_AuditNumber_AutoFixAddsOnlyActionOnTopOfDrift(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, _, _, cleanup := rcSeedDriftPair(t, h)
	defer cleanup()

	if err := rcReconciler(h).SweepOnce(ctx); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}

	auto := rcReadAuditRow(t, h, tenantID, eventAutoFixed)
	rcAssertOneAuditRow(t, auto, eventAutoFixed, "recordAutoFixAudit")
	drift := rcReadAuditRow(t, h, tenantID, eventDriftDetected)
	rcAssertOneAuditRow(t, drift, eventDriftDetected, "recordDriftAudit")

	// Both fixtures carry a submission_jobs row, so `action` is the only admissible
	// difference. Without this floor the difference is also {} for two payloads that never
	// gained the number at all.
	if auto.number == nil || drift.number == nil {
		t.Fatalf("payload->>'%s' present on %s = %v / %s = %v; the key-set difference below is not "+
			"yet asserting what this case's name says", auditNumberKey, eventAutoFixed,
			auto.number != nil, eventDriftDetected, drift.number != nil)
	}

	autoKeys := rcPayloadKeys(t, auto.payload)
	driftKeys := rcPayloadKeys(t, drift.payload)

	inDrift := map[string]bool{}
	for _, k := range driftKeys {
		inDrift[k] = true
	}
	var extra []string
	for _, k := range autoKeys {
		if !inDrift[k] {
			extra = append(extra, k)
		}
	}
	if len(extra) != 1 || extra[0] != "action" {
		t.Errorf("%s keys %v minus %s keys %v = %v, want exactly [action] -- driftPayload is the one "+
			"construction site for both events", eventAutoFixed, autoKeys, eventDriftDetected,
			driftKeys, extra)
	}

	inAuto := map[string]bool{}
	for _, k := range autoKeys {
		inAuto[k] = true
	}
	for _, k := range driftKeys {
		if !inAuto[k] {
			t.Errorf("%s carries key %q that %s does not; the auto-fix payload must be the drift "+
				"payload plus `action`, never a second construction", eventDriftDetected, k, eventAutoFixed)
		}
	}
}

// TestRLS_AuditNumber_DriftPayloadWritesTheKeyUnconditionally (AC-4, pure -- no DB):
// driftPayload copies the number off the Finding for every finding, not only the ones
// carrying a submission_jobs row. submission_job_id is deliberately conditional; this key is
// not (subtask 01's convention).
func TestRLS_AuditNumber_DriftPayloadWritesTheKeyUnconditionally(t *testing.T) {
	jobID := uuid.NewString()
	const number = "RC-UNIT-0001"

	withJob := rcWithNumber(t, Finding{
		InvoiceID: uuid.NewString(), SubmissionJobID: &jobID, Kind: LostPoll, Healable: true,
	}, number)
	got := driftPayload(withJob)
	if v, _ := got[auditNumberKey].(string); v != number {
		t.Errorf("driftPayload(finding with a job)[%q] = %v, want %q", auditNumberKey, got[auditNumberKey], number)
	}
	rcAssertKeySet(t, rcSortedKeys(got),
		[]string{"invoice_id", "drift_kind", "submission_job_id", auditNumberKey},
		"driftPayload (finding with a job)")

	noJob := rcWithNumber(t, Finding{InvoiceID: uuid.NewString(), Kind: QueuedNeverSent}, number)
	gotNoJob := driftPayload(noJob)
	if v, _ := gotNoJob[auditNumberKey].(string); v != number {
		t.Errorf("driftPayload(finding with no job)[%q] = %v, want %q -- the two NULL-job arms and "+
			"Q1/C1/C2 still carry a number", auditNumberKey, gotNoJob[auditNumberKey], number)
	}
	rcAssertKeySet(t, rcSortedKeys(gotNoJob),
		[]string{"invoice_id", "drift_kind", auditNumberKey},
		"driftPayload (finding with no job)")

	// Unconditional, not conditional: a blank number is still a key, mirroring the column.
	blank := rcWithNumber(t, Finding{InvoiceID: uuid.NewString(), Kind: QueuedNeverSent}, "")
	if _, ok := driftPayload(blank)[auditNumberKey]; !ok {
		t.Errorf("driftPayload omits %q for a blank number; write it unconditionally so the payload "+
			"mirrors invoices.invoice_number rather than copying submission_job_id's optional shape",
			auditNumberKey)
	}
}

// TestRLS_AuditNumber_FindingEqualComparesTheNumber (CF-19): findingEqual compares Finding
// fields BY NAME, so a new InvoiceNumber field it does not name is invisible at all twelve
// exact-Finding call sites in this package -- every one of them stays green whether or not
// scanQuery supplies the number.
func TestRLS_AuditNumber_FindingEqualComparesTheNumber(t *testing.T) {
	jobID := uuid.NewString()
	base := Finding{InvoiceID: uuid.NewString(), SubmissionJobID: &jobID, Kind: LostPoll, Healable: true}

	a := rcWithNumber(t, base, "RC-AAAAAAA1")
	same := rcWithNumber(t, base, "RC-AAAAAAA1")
	b := rcWithNumber(t, base, "RC-BBBBBBB2")

	if !findingEqual(a, same) {
		t.Fatalf("findingEqual reports two identical findings unequal (%+v vs %+v) -- the positive "+
			"control the inequality assertion below is measured against", a, same)
	}
	if findingEqual(a, b) {
		t.Errorf("findingEqual(%+v, %+v) = true; they differ only in %s, so none of the %d "+
			"exact-Finding call sites in this package can see the number drift", a, b,
			rcFindingNumberField, rcFindingEqualCallSites)
	}
}

// --- AC-3: every arm ---------------------------------------------------------------------

// rcScanArm is one arm of scanQuery: how to provoke it, and which event its finding routes to.
type rcScanArm struct {
	kind  DriftKind
	event string // lost_poll is the ONE healable kind and routes to auto_fixed
	// provoke seeds a fresh tenant holding exactly one invoice that trips this arm alone.
	provoke func(t *testing.T, h *harness) (tenantID, invoiceID string, cleanup func())
}

func rcScanArms() []rcScanArm {
	return []rcScanArm{
		{LostPoll, eventAutoFixed, rcProvokeLostPoll},
		{PendingTooManyHops, eventDriftDetected, rcProvokePendingTooManyHops},
		{PendingTooLong, eventDriftDetected, rcProvokePendingTooLong},
		{SubmittingOrphan, eventDriftDetected, rcProvokeSubmittingOrphan},
		{QueuedNeverSent, eventDriftDetected, rcProvokeQueuedNeverSent},
		{IRNWithoutAccepted, eventDriftDetected, rcProvokeIRNWithoutAccepted},
		{AcceptedWithoutIRN, eventDriftDetected, rcProvokeAcceptedWithoutIRN},
		{VerdictNotRouted, eventDriftDetected, rcProvokeVerdictNotRouted},
		{ApprovalRunOrphaned, eventDriftDetected, rcProvokeApprovalRunOrphaned},
		{ApprovalBlockedUnstaffed, eventDriftDetected, rcProvokeApprovalBlockedUnstaffed},
	}
}

// rcScanArmKindPattern reads the kind literal out of every arm's select list
// (`…, '<kind>', <healable bool>`), so the table above is floored against the SQL itself
// rather than against a second hand-kept list.
var rcScanArmKindPattern = regexp.MustCompile(`'([a-z_]+)',\s+(?:true|false)`)

func rcScanQueryKindLiterals() []string {
	var out []string
	for _, m := range rcScanArmKindPattern.FindAllStringSubmatch(scanQuery, -1) {
		out = append(out, m[1])
	}
	return out
}

// TestRLS_AuditNumber_EveryScanArmYieldsANumber (AC-3): each of the ten arms is provoked in
// its own tenant and swept for real. Widening nine arms and missing one is invisible to
// findingEqual and to every count-only assertion -- only a per-arm read of the immutable row
// catches the arm that yields a blank.
func TestRLS_AuditNumber_EveryScanArmYieldsANumber(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	arms := rcScanArms()
	if len(arms) != rcScanArmCount {
		t.Fatalf("rcScanArms holds %d rows, want %d -- a short table satisfies every assertion in "+
			"the loop below vacuously", len(arms), rcScanArmCount)
	}

	// Bind the table to scanQuery in both directions: an eleventh arm, or a renamed kind,
	// must red here rather than silently escaping the sweep.
	inTable := map[string]bool{}
	for _, a := range arms {
		inTable[string(a.kind)] = true
	}
	literals := rcScanQueryKindLiterals()
	if len(literals) != rcScanArmCount {
		t.Fatalf("scanQuery yields %d kind literals %v, want %d -- the arm table below can no longer "+
			"claim completeness", len(literals), literals, rcScanArmCount)
	}
	inQuery := map[string]bool{}
	for _, k := range literals {
		inQuery[k] = true
		if !inTable[k] {
			t.Errorf("scanQuery has an arm for %q that rcScanArms does not exercise", k)
		}
	}
	for _, a := range arms {
		if !inQuery[string(a.kind)] {
			t.Errorf("rcScanArms names kind %q that scanQuery never selects", a.kind)
		}
	}

	for _, a := range arms {
		t.Run(string(a.kind), func(t *testing.T) {
			tenantID, invoiceID, cleanup := a.provoke(t, h)
			defer cleanup()

			want := rcInvoiceNumberFor(t, h, invoiceID)

			// The fixture really does trip THIS arm and nothing else -- otherwise the audit
			// read below would report another arm's row.
			got, err := rcScan(t, h, tenantID, rcThresholds)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if len(got) == 0 {
				t.Fatalf("Scan returned no findings for the %s fixture; every assertion below would "+
					"be vacuous", a.kind)
			}
			if n := countForInvoice(got, invoiceID); n != 1 {
				t.Fatalf("Scan findings for invoice %q = %d (%+v), want exactly 1 %s", invoiceID, n, got, a.kind)
			}
			if !containsFindingFor(got, invoiceID, a.kind) {
				t.Fatalf("Scan findings = %+v, want one of kind %s for invoice %q", got, a.kind, invoiceID)
			}

			if err := rcReconciler(h).SweepOnce(ctx); err != nil {
				t.Fatalf("SweepOnce: %v", err)
			}

			row := rcReadAuditRowForKind(t, h, tenantID, a.event, a.kind)
			rcAssertOneAuditRow(t, row, a.event, string(a.kind)+" arm")
			rcAssertNumber(t, row, a.event, string(a.kind)+" arm", want)
		})
	}
}

// TestRLS_AuditNumber_NullJobArmsStillCarryTheNumber (AC-3): the two arms selecting
// NULL::uuid for the job id read FROM invoices i like every other arm, so both still supply
// a number. These are the arms a "join through submission_jobs" fix would drop, and the
// submission_job_id absence assertion keeps that NULL from being read as the number's source.
func TestRLS_AuditNumber_NullJobArmsStillCarryTheNumber(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, entityID, orphaned, cleanupOrphaned := rcSeedInvoice(t, h, rcInvoiceOpts{status: "validated"})
	defer cleanupOrphaned()
	blocked, cleanupBlocked := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "validated"})
	defer cleanupBlocked()

	versionID, cleanupPolicy := rcSeedApprovalPolicy(t, h, tenantID)
	defer cleanupPolicy()
	defer rcCleanupAudit(h, tenantID)

	rcSeedRun(t, h, tenantID, orphaned, versionID, "open")
	if _, err := h.super.Exec(ctx, `UPDATE invoices SET status = 'draft' WHERE id = $1`, orphaned); err != nil {
		t.Fatalf("flip invoice to draft: %v", err)
	}

	rcSeedWorkflowRole(t, h, tenantID, "an-null-arm-role", false) // zero holders, on purpose
	roleKey := "an-null-arm-role"
	blockedRun := rcSeedRun(t, h, tenantID, blocked, versionID, "open")
	rcSeedRunStep(t, h, tenantID, blockedRun, 1, "approval", &roleKey, "pending")

	wantOrphaned := rcInvoiceNumberFor(t, h, orphaned)
	wantBlocked := rcInvoiceNumberFor(t, h, blocked)
	if wantOrphaned == wantBlocked {
		t.Fatalf("both fixture invoices carry invoice_number %q; a swapped number would be invisible",
			wantOrphaned)
	}

	if err := rcReconciler(h).SweepOnce(ctx); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}

	if n := mustCount(t, h.super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = $2`, tenantID, eventDriftDetected); n != 2 {
		t.Fatalf("%s rows for tenant %q = %d, want exactly 2 (one per NULL-job arm)", eventDriftDetected, tenantID, n)
	}

	for _, c := range []struct {
		kind    DriftKind
		invoice string
		want    string
	}{
		{ApprovalRunOrphaned, orphaned, wantOrphaned},
		{ApprovalBlockedUnstaffed, blocked, wantBlocked},
	} {
		t.Run(string(c.kind), func(t *testing.T) {
			what := string(c.kind) + " arm (NULL::uuid job id)"
			row := rcReadAuditRowForKind(t, h, tenantID, eventDriftDetected, c.kind)
			rcAssertOneAuditRow(t, row, eventDriftDetected, what)
			rcAssertNumber(t, row, eventDriftDetected, what, c.want)
			rcAssertKeySet(t, rcPayloadKeys(t, row.payload),
				[]string{"invoice_id", "drift_kind", auditNumberKey}, string(c.kind)+" payload")

			body := rcDecodePayload(t, row.payload)
			if _, ok := body["submission_job_id"]; ok {
				t.Errorf("%s: payload = %+v, want submission_job_id ABSENT -- this arm's job id is "+
					"NULL::uuid and the number must come from invoices, not from that column", c.kind, body)
			}
			if got, _ := body["invoice_id"].(string); got != c.invoice {
				t.Errorf("%s: payload invoice_id = %q, want %q", c.kind, got, c.invoice)
			}
		})
	}
}

// --- AC-2: the number rides the query Scan already runs -----------------------------------

// TestRLS_AuditNumber_ScanIssuesNoExtraStatement (AC-2): three flagged invoices under one
// tenant. Scan must still issue exactly ONE statement against invoices -- a per-finding
// lookup would run once per drift row on every five-minute sweep. The number assertions are
// what make the count meaningful: a count of 1 also holds for today's unwidened query.
func TestRLS_AuditNumber_ScanIssuesNoExtraStatement(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, entityID, first, cleanupFirst := rcSeedInvoice(t, h, rcInvoiceOpts{status: "accepted"})
	defer cleanupFirst()
	second, cleanupSecond := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "accepted"})
	defer cleanupSecond()
	third, cleanupThird := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "accepted"})
	defer cleanupThird()
	defer rcCleanupAudit(h, tenantID)

	traced, rec := rcTracedAppPool(t)

	rec.reset()
	var got []Finding
	if err := db.WithinTenantTx(ctx, traced, tenantID, func(tx pgx.Tx) error {
		var scanErr error
		got, scanErr = Scan(ctx, tx, rcThresholds)
		return scanErr
	}); err != nil {
		t.Fatalf("Scan on the traced pool: %v", err)
	}
	stmts := rec.mentioning("invoices")

	if len(got) != 3 {
		t.Fatalf("Scan findings = %d (%+v), want 3 (one accepted_without_irn per seeded invoice) -- "+
			"a smaller result makes the statement count below meaningless", len(got), got)
	}
	if len(stmts) != 1 {
		t.Errorf("Scan issued %d statement(s) against invoices for %d findings, want exactly 1 (the "+
			"widened scanQuery, same round trip): %v", len(stmts), len(got), stmts)
	}

	// The count above is only worth asserting once the number actually arrives.
	if err := rcReconciler(h).SweepOnce(ctx); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = $2`, tenantID, eventDriftDetected); n != 3 {
		t.Fatalf("%s rows for tenant %q = %d, want 3", eventDriftDetected, tenantID, n)
	}
	for _, invoiceID := range []string{first, second, third} {
		want := rcInvoiceNumberFor(t, h, invoiceID)
		var number *string
		if err := h.super.QueryRow(ctx,
			`SELECT payload->>'`+auditNumberKey+`' FROM audit_log
			  WHERE tenant_id = $1 AND event = $2 AND payload->>'invoice_id' = $3`,
			tenantID, eventDriftDetected, invoiceID).Scan(&number); err != nil {
			t.Fatalf("read the %s row for invoice %s: %v", eventDriftDetected, invoiceID, err)
		}
		if number == nil {
			t.Errorf("%s for invoice %s: payload->>'%s' is SQL NULL (key absent)",
				eventDriftDetected, invoiceID, auditNumberKey)
			continue
		}
		if *number != want {
			t.Errorf("%s for invoice %s: payload->>'%s' = %q, want %q",
				eventDriftDetected, invoiceID, auditNumberKey, *number, want)
		}
	}
}

// --- per-arm fixtures --------------------------------------------------------------------
//
// Each seeds a fresh tenant holding exactly ONE invoice that trips exactly ONE arm, so the
// per-arm audit read is unambiguous.

func rcProvokeLostPoll(t *testing.T, h *harness) (string, string, func()) {
	t.Helper()
	overdue := time.Now().Add(-1 * time.Hour)
	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	jobID, cleanupJob := rcSeedJob(t, h, tenantID, invoiceID,
		rcJobOpts{state: "pending", attempts: 2, nextPollAt: &overdue})
	return tenantID, invoiceID, func() {
		rcCleanupReArmKeys(h, tenantID, jobID)
		rcCleanupPollJobsFor(h, jobID)
		rcCleanupAudit(h, tenantID)
		cleanupJob()
		cleanupInvoice()
	}
}

func rcProvokePendingTooManyHops(t *testing.T, h *harness) (string, string, func()) {
	t.Helper()
	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	_, cleanupJob := rcSeedJob(t, h, tenantID, invoiceID, rcJobOpts{state: "pending", attempts: 99})
	return tenantID, invoiceID, rcCompose(h, tenantID, cleanupJob, cleanupInvoice)
}

func rcProvokePendingTooLong(t *testing.T, h *harness) (string, string, func()) {
	t.Helper()
	old := time.Now().Add(-72 * time.Hour)
	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	_, cleanupJob := rcSeedJob(t, h, tenantID, invoiceID, rcJobOpts{state: "pending", createdAt: &old})
	return tenantID, invoiceID, rcCompose(h, tenantID, cleanupJob, cleanupInvoice)
}

func rcProvokeSubmittingOrphan(t *testing.T, h *harness) (string, string, func()) {
	t.Helper()
	stale := time.Now().Add(-1 * time.Hour)
	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	_, cleanupJob := rcSeedJob(t, h, tenantID, invoiceID, rcJobOpts{state: "submitting", updatedAt: &stale})
	return tenantID, invoiceID, rcCompose(h, tenantID, cleanupJob, cleanupInvoice)
}

func rcProvokeQueuedNeverSent(t *testing.T, h *harness) (string, string, func()) {
	t.Helper()
	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "queued"})
	return tenantID, invoiceID, rcCompose(h, tenantID, cleanupInvoice)
}

func rcProvokeIRNWithoutAccepted(t *testing.T, h *harness) (string, string, func()) {
	t.Helper()
	irn := "NG-AN-" + uuid.NewString()[:8]
	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted", irn: &irn})
	return tenantID, invoiceID, rcCompose(h, tenantID, cleanupInvoice)
}

func rcProvokeAcceptedWithoutIRN(t *testing.T, h *harness) (string, string, func()) {
	t.Helper()
	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "accepted"})
	return tenantID, invoiceID, rcCompose(h, tenantID, cleanupInvoice)
}

func rcProvokeVerdictNotRouted(t *testing.T, h *harness) (string, string, func()) {
	t.Helper()
	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	_, cleanupJob := rcSeedJob(t, h, tenantID, invoiceID, rcJobOpts{state: "accepted"})
	return tenantID, invoiceID, rcCompose(h, tenantID, cleanupJob, cleanupInvoice)
}

func rcProvokeApprovalRunOrphaned(t *testing.T, h *harness) (string, string, func()) {
	t.Helper()
	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "validated"})
	versionID, cleanupPolicy := rcSeedApprovalPolicy(t, h, tenantID)
	rcSeedRun(t, h, tenantID, invoiceID, versionID, "open")
	if _, err := h.super.Exec(context.Background(),
		`UPDATE invoices SET status = 'draft' WHERE id = $1`, invoiceID); err != nil {
		t.Fatalf("flip invoice to draft: %v", err)
	}
	return tenantID, invoiceID, rcCompose(h, tenantID, cleanupPolicy, cleanupInvoice)
}

func rcProvokeApprovalBlockedUnstaffed(t *testing.T, h *harness) (string, string, func()) {
	t.Helper()
	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "validated"})
	versionID, cleanupPolicy := rcSeedApprovalPolicy(t, h, tenantID)
	rcSeedWorkflowRole(t, h, tenantID, "an-arm-empty-role", false) // zero holders, on purpose
	roleKey := "an-arm-empty-role"
	runID := rcSeedRun(t, h, tenantID, invoiceID, versionID, "open")
	rcSeedRunStep(t, h, tenantID, runID, 1, "approval", &roleKey, "pending")
	return tenantID, invoiceID, rcCompose(h, tenantID, cleanupPolicy, cleanupInvoice)
}
