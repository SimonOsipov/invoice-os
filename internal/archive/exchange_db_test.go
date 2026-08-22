// exchange_db_test.go: RED specs for AUDIT-05-05 (Mode A) -- selectSubmissions and
// selectExchange against a real Postgres. Reuses entity_db_test.go's rollback-wrapped
// harness (dbSuperPool/beginFixtureTx/actingAs/mustCreateTenant) and
// invoices_db_test.go's mustCreateEntity/mustCreateInvoice, plus this file's own
// submission_jobs/app_exchange fixtures.
package archive

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// --- pinned header copies for CSV parsing (LOCAL and literal, like history_db_test.go's
// wantHistoryHeader -- decoupled from the package vars, which stay empty until Stage 3) ---

var wantSubmissionsHeader = []string{
	"invoice_id", "invoice_number", "submission_job_id", "idempotency_key",
	"state", "attempts", "adapter", "adapter_version", "poll_ref", "last_error",
	"created_at", "updated_at",
}

var wantExchangeHeader = []string{
	"invoice_id", "invoice_number", "submission_job_id", "exchange_id", "operation",
	"outcome", "attempt", "http_status", "latency_ms", "truncated", "encoding_coerced",
	"request_headers", "response_headers", "request_body_file", "response_body_file",
	"adapter", "adapter_version", "occurred_at",
}

// --- fixtures --------------------------------------------------------------------

// submissionJobFixture is mustCreateSubmissionJob's input. Zero-value fields take sane
// defaults: adapter/adapterVersion "mock"/"v1", state "queued", idempotencyKey random.
type submissionJobFixture struct {
	id, tenantID, invoiceID string
	idempotencyKey          string
	adapter, adapterVersion string
	state                   string
	attempts                int
	pollRef, lastError      *string
	createdAt               time.Time
}

func mustCreateSubmissionJob(t *testing.T, tx pgx.Tx, f submissionJobFixture) string {
	t.Helper()
	id := f.id
	if id == "" {
		id = uuid.NewString()
	}
	idem := f.idempotencyKey
	if idem == "" {
		idem = uuid.NewString()
	}
	adapter := f.adapter
	if adapter == "" {
		adapter = "mock"
	}
	adapterVersion := f.adapterVersion
	if adapterVersion == "" {
		adapterVersion = "v1"
	}
	state := f.state
	if state == "" {
		state = "queued"
	}
	createdAt := f.createdAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err := tx.Exec(context.Background(), `
		INSERT INTO submission_jobs
		    (id, tenant_id, invoice_id, idempotency_key, adapter, adapter_version, state, attempts, poll_ref, last_error, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		id, f.tenantID, f.invoiceID, idem, adapter, adapterVersion, state, f.attempts, f.pollRef, f.lastError, createdAt)
	if err != nil {
		t.Fatalf("insert submission_jobs fixture: %v", err)
	}
	return id
}

// exchangeFixture is mustCreateExchange's input. Zero-value fields take sane
// defaults: operation "submit", outcome "sent", attempt 1, headers "{}",
// adapter/adapterVersion "mock"/"v1".
type exchangeFixture struct {
	id, tenantID, submissionJobID, invoiceID string
	operation, outcome                       string
	attempt                                  int
	requestBody, responseBody                *string
	requestHeaders, responseHeaders          string // raw jsonb text; "" -> "{}"
	httpStatus, latencyMs                    *int
	truncated, encodingCoerced               bool
	adapter, adapterVersion                  string
	occurredAt                               time.Time
}

func mustCreateExchange(t *testing.T, tx pgx.Tx, f exchangeFixture) string {
	t.Helper()
	id := f.id
	if id == "" {
		id = uuid.NewString()
	}
	operation := f.operation
	if operation == "" {
		operation = "submit"
	}
	outcome := f.outcome
	if outcome == "" {
		outcome = "sent"
	}
	attempt := f.attempt
	if attempt == 0 {
		attempt = 1
	}
	reqHeaders := f.requestHeaders
	if reqHeaders == "" {
		reqHeaders = "{}"
	}
	respHeaders := f.responseHeaders
	if respHeaders == "" {
		respHeaders = "{}"
	}
	adapter := f.adapter
	if adapter == "" {
		adapter = "mock"
	}
	adapterVersion := f.adapterVersion
	if adapterVersion == "" {
		adapterVersion = "v1"
	}
	occurredAt := f.occurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	_, err := tx.Exec(context.Background(), `
		INSERT INTO app_exchange
		    (id, tenant_id, submission_job_id, invoice_id, operation, outcome, attempt,
		     request_body, request_headers, response_body, response_headers,
		     http_status, latency_ms, truncated, encoding_coerced, adapter, adapter_version, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11::jsonb, $12, $13, $14, $15, $16, $17, $18)`,
		id, f.tenantID, f.submissionJobID, f.invoiceID, operation, outcome, attempt,
		f.requestBody, reqHeaders, f.responseBody, respHeaders,
		f.httpStatus, f.latencyMs, f.truncated, f.encodingCoerced, adapter, adapterVersion, occurredAt)
	if err != nil {
		t.Fatalf("insert app_exchange fixture: %v", err)
	}
	return id
}

// --- header/body-capture test doubles ---------------------------------------------

// fakeBodyWriter captures every WriteBody call by name, for the poll-handle and
// zero-body assertions -- the real streaming zip assembler is a later subtask.
type fakeBodyWriter struct {
	bodies map[string][]byte
}

func newFakeBodyWriter() *fakeBodyWriter {
	return &fakeBodyWriter{bodies: map[string][]byte{}}
}

func (w *fakeBodyWriter) WriteBody(name string, body []byte) error {
	w.bodies[name] = append([]byte(nil), body...)
	return nil
}

func mustMarshalHeaders(t *testing.T, h http.Header) string {
	t.Helper()
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("test setup: marshal headers fixture: %v", err)
	}
	return string(b)
}

func mustUnmarshalHeaders(t *testing.T, cell string) http.Header {
	t.Helper()
	var h http.Header
	if err := json.Unmarshal([]byte(cell), &h); err != nil {
		t.Fatalf("unmarshal headers cell %q: %v", cell, err)
	}
	if h == nil {
		h = http.Header{}
	}
	return h
}

// --- CSV parsing helpers -----------------------------------------------------------

func colIndex(t *testing.T, header []string, column string) int {
	t.Helper()
	for i, h := range header {
		if h == column {
			return i
		}
	}
	t.Fatalf("header %v has no column %q", header, column)
	return -1
}

func parseCSV(t *testing.T, raw []byte) [][]string {
	t.Helper()
	rows, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	return rows
}

func exchangeRowsForInvoice(t *testing.T, raw []byte, invoiceID string) [][]string {
	t.Helper()
	rows := parseCSV(t, raw)
	idx := colIndex(t, wantExchangeHeader, "invoice_id")
	var out [][]string
	for _, row := range rows[1:] {
		if len(row) > idx && row[idx] == invoiceID {
			out = append(out, row)
		}
	}
	return out
}

func exchangeRowByInvoice(t *testing.T, raw []byte, invoiceID string) []string {
	t.Helper()
	rows := exchangeRowsForInvoice(t, raw, invoiceID)
	if len(rows) != 1 {
		t.Fatalf("exchange.csv has %d rows for invoice %s, want exactly 1", len(rows), invoiceID)
	}
	return rows[0]
}

func submissionsRowsForInvoice(t *testing.T, raw []byte, invoiceID string) [][]string {
	t.Helper()
	rows := parseCSV(t, raw)
	idx := colIndex(t, wantSubmissionsHeader, "invoice_id")
	var out [][]string
	for _, row := range rows[1:] {
		if len(row) > idx && row[idx] == invoiceID {
			out = append(out, row)
		}
	}
	return out
}

func submissionsRowByJobID(t *testing.T, raw []byte, jobID string) []string {
	t.Helper()
	rows := parseCSV(t, raw)
	idx := colIndex(t, wantSubmissionsHeader, "submission_job_id")
	for _, row := range rows[1:] {
		if row[idx] == jobID {
			return row
		}
	}
	t.Fatalf("submissions.csv has no row for job %s", jobID)
	return nil
}

// =====================================================================================
// AC-1: http_status/latency_ms/attempt carry through; NULL -> empty cell; RLS scoping.
// =====================================================================================

func TestSelectExchange_CarriesStatusLatencyAndAttempt(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-exchange-status-latency")
	entity := mustCreateEntity(t, tx, tenant, "Status Latency Co", "60000004-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-STATUS-01"})
	jobID := mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tenant, invoiceID: invID})
	httpStatus, latencyMs := 422, 137
	mustCreateExchange(t, tx, exchangeFixture{
		tenantID: tenant, submissionJobID: jobID, invoiceID: invID,
		attempt: 2, httpStatus: &httpStatus, latencyMs: &latencyMs,
	})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	bw := newFakeBodyWriter()
	if err := selectExchange(context.Background(), tx, []string{invID}, w, bw); err != nil {
		t.Fatalf("selectExchange: unexpected error: %v", err)
	}
	w.Flush()

	row := exchangeRowByInvoice(t, buf.Bytes(), invID)
	if got := row[colIndex(t, wantExchangeHeader, "http_status")]; got != "422" {
		t.Errorf("http_status = %q, want %q", got, "422")
	}
	if got := row[colIndex(t, wantExchangeHeader, "latency_ms")]; got != "137" {
		t.Errorf("latency_ms = %q, want %q", got, "137")
	}
	if got := row[colIndex(t, wantExchangeHeader, "attempt")]; got != "2" {
		t.Errorf("attempt = %q, want %q", got, "2")
	}
}

func TestSelectExchange_NullStatusAndLatencyAreEmptyCells(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-exchange-null-status")
	entity := mustCreateEntity(t, tx, tenant, "Null Status Co", "60000005-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-NULLSTATUS-01"})
	jobID := mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tenant, invoiceID: invID})
	// connection_failed: no request reached the wire, so no response -> both nil.
	mustCreateExchange(t, tx, exchangeFixture{tenantID: tenant, submissionJobID: jobID, invoiceID: invID, outcome: "connection_failed"})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	bw := newFakeBodyWriter()
	if err := selectExchange(context.Background(), tx, []string{invID}, w, bw); err != nil {
		t.Fatalf("selectExchange: unexpected error: %v", err)
	}
	w.Flush()

	row := exchangeRowByInvoice(t, buf.Bytes(), invID)
	if got := row[colIndex(t, wantExchangeHeader, "http_status")]; got != "" {
		t.Errorf("http_status = %q, want empty (NULL -> empty cell)", got)
	}
	if got := row[colIndex(t, wantExchangeHeader, "latency_ms")]; got != "" {
		t.Errorf("latency_ms = %q, want empty (NULL -> empty cell)", got)
	}
}

func TestRLS_SelectExchangeCannotReachAnotherTenantsEvidence(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenantA := mustCreateTenant(t, tx, "archive-exchange-rls-a")
	tenantB := mustCreateTenant(t, tx, "archive-exchange-rls-b")
	entityA := mustCreateEntity(t, tx, tenantA, "RLS Evidence Co", "60000013-0001")
	invA := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenantA, entityID: entityA, invoiceNumber: "INV-RLS-EX-01"})
	jobA := mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tenantA, invoiceID: invA})
	mustCreateExchange(t, tx, exchangeFixture{tenantID: tenantA, submissionJobID: jobA, invoiceID: invA})

	// Control needle (superuser, pre-actingAs): the fixture really planted the row.
	var planted int
	if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM app_exchange WHERE invoice_id = $1`, invA).Scan(&planted); err != nil {
		t.Fatalf("control-needle count: %v", err)
	}
	if planted != 1 {
		t.Fatalf("control needle: planted %d app_exchange rows for invA, want 1 -- fixture setup is broken", planted)
	}

	actingAs(t, tx, tenantB)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	bw := newFakeBodyWriter()
	if err := selectExchange(context.Background(), tx, []string{invA}, w, bw); err != nil {
		t.Errorf("selectExchange(another tenant's evidence) error = %v, want nil (RLS: no error, just no rows)", err)
	}
	w.Flush()
	rows := parseCSV(t, buf.Bytes())
	if len(rows) != 1 {
		t.Errorf("exchange.csv has %d rows, want exactly 1 (header only -- RLS must hide tenant A's row)", len(rows))
	}
}

// =====================================================================================
// AC-2: truncated/encoding_coerced are independent -- all four combinations round-trip.
// =====================================================================================

func TestSelectExchange_TruncatedAndCoercedAreIndependent(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-exchange-truncated-coerced")
	entity := mustCreateEntity(t, tx, tenant, "Truncated Coerced Co", "60000006-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-TC-01"})
	jobID := mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tenant, invoiceID: invID})

	combos := []struct {
		attempt            int
		truncated, coerced bool
	}{
		{1, false, false},
		{2, true, false},
		{3, false, true},
		{4, true, true},
	}
	for _, c := range combos {
		mustCreateExchange(t, tx, exchangeFixture{
			tenantID: tenant, submissionJobID: jobID, invoiceID: invID,
			attempt: c.attempt, truncated: c.truncated, encodingCoerced: c.coerced,
		})
	}

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	bw := newFakeBodyWriter()
	if err := selectExchange(context.Background(), tx, []string{invID}, w, bw); err != nil {
		t.Fatalf("selectExchange: unexpected error: %v", err)
	}
	w.Flush()

	rows := exchangeRowsForInvoice(t, buf.Bytes(), invID)
	if len(rows) != 4 {
		t.Fatalf("selectExchange wrote %d rows, want 4 (one per truncated/coerced combination)", len(rows))
	}
	attemptIdx := colIndex(t, wantExchangeHeader, "attempt")
	truncIdx := colIndex(t, wantExchangeHeader, "truncated")
	coercedIdx := colIndex(t, wantExchangeHeader, "encoding_coerced")
	byAttempt := map[string][]string{}
	for _, row := range rows {
		byAttempt[row[attemptIdx]] = row
	}
	for _, c := range combos {
		row, ok := byAttempt[strconv.Itoa(c.attempt)]
		if !ok {
			t.Fatalf("no row for attempt %d", c.attempt)
			continue
		}
		if got := row[truncIdx]; got != strconv.FormatBool(c.truncated) {
			t.Errorf("attempt %d truncated = %q, want %q", c.attempt, got, strconv.FormatBool(c.truncated))
		}
		if got := row[coercedIdx]; got != strconv.FormatBool(c.coerced) {
			t.Errorf("attempt %d encoding_coerced = %q, want %q", c.attempt, got, strconv.FormatBool(c.coerced))
		}
	}
}

// =====================================================================================
// AC-3: every emitted header is a fixed point of submission.ScrubHeaders (never a
// copied allowlist), proved by the round trip, never a per-name spot check.
// =====================================================================================

func TestSelectExchange_EveryEmittedHeaderSurvivesScrubHeaders(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-exchange-scrub-survives")
	entity := mustCreateEntity(t, tx, tenant, "Scrub Survives Co", "60000001-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-SCRUB-01"})
	jobID := mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tenant, invoiceID: invID})

	reqHeaders := mustMarshalHeaders(t, http.Header{
		"Content-Type":  {"application/json"},
		"Authorization": {"Bearer top-secret"},
	})
	respHeaders := mustMarshalHeaders(t, http.Header{
		"Retry-After": {"5"},
		"Set-Cookie":  {"session=abc"},
	})
	mustCreateExchange(t, tx, exchangeFixture{
		tenantID: tenant, submissionJobID: jobID, invoiceID: invID,
		requestHeaders: reqHeaders, responseHeaders: respHeaders,
	})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	bw := newFakeBodyWriter()
	if err := selectExchange(context.Background(), tx, []string{invID}, w, bw); err != nil {
		t.Fatalf("selectExchange: unexpected error: %v", err)
	}
	w.Flush()

	row := exchangeRowByInvoice(t, buf.Bytes(), invID)
	for _, col := range []string{"request_headers", "response_headers"} {
		cell := row[colIndex(t, wantExchangeHeader, col)]
		h := mustUnmarshalHeaders(t, cell)
		if !reflect.DeepEqual(submission.ScrubHeaders(h), h) {
			t.Errorf("%s = %q is not a fixed point of ScrubHeaders -- an unscrubbed header survived the re-scrub on the way out (D-7)", col, cell)
		}
	}
}

// Control needle for the loop above: DeepEqual(ScrubHeaders(h), h) is true VACUOUSLY
// for an empty h too. Confirms an ALLOWED header actually survived into the CSV.
func TestExchangeHeaders_AssertionIsNotVacuous(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-exchange-scrub-not-vacuous")
	entity := mustCreateEntity(t, tx, tenant, "Scrub Not Vacuous Co", "60000002-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-SCRUB-NV-01"})
	jobID := mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tenant, invoiceID: invID})
	mustCreateExchange(t, tx, exchangeFixture{
		tenantID: tenant, submissionJobID: jobID, invoiceID: invID,
		requestHeaders: mustMarshalHeaders(t, http.Header{
			"Content-Type":  {"application/json"},
			"Authorization": {"Bearer x"},
		}),
	})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	bw := newFakeBodyWriter()
	if err := selectExchange(context.Background(), tx, []string{invID}, w, bw); err != nil {
		t.Fatalf("selectExchange: unexpected error: %v", err)
	}
	w.Flush()

	row := exchangeRowByInvoice(t, buf.Bytes(), invID)
	cell := row[colIndex(t, wantExchangeHeader, "request_headers")]
	h := mustUnmarshalHeaders(t, cell)
	if len(h) == 0 {
		t.Fatal("emitted request_headers is empty -- the ScrubHeaders round-trip assertion above would pass vacuously")
	}
	if h.Get("Content-Type") != "application/json" {
		t.Errorf("emitted request_headers = %v, want Content-Type=application/json to have survived (an ALLOWED header must be present)", h)
	}
}

// =====================================================================================
// AC-4: a row planted with a disallowed header (superuser, bypassing RecordExchange)
// emits without it.
// =====================================================================================

func TestSelectExchange_DropsAPlantedCredentialHeader(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-exchange-drop-credential")
	entity := mustCreateEntity(t, tx, tenant, "Drop Credential Co", "60000003-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-CRED-01"})
	jobID := mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tenant, invoiceID: invID})
	// Planted directly as superuser, bypassing RecordExchange entirely (AC-4).
	mustCreateExchange(t, tx, exchangeFixture{
		tenantID: tenant, submissionJobID: jobID, invoiceID: invID,
		requestHeaders: mustMarshalHeaders(t, http.Header{
			"Content-Type":  {"application/json"},
			"Authorization": {"Bearer super-secret-token"},
		}),
	})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	bw := newFakeBodyWriter()
	if err := selectExchange(context.Background(), tx, []string{invID}, w, bw); err != nil {
		t.Fatalf("selectExchange: unexpected error: %v", err)
	}
	w.Flush()

	row := exchangeRowByInvoice(t, buf.Bytes(), invID)
	cell := row[colIndex(t, wantExchangeHeader, "request_headers")]
	if strings.Contains(strings.ToLower(cell), "authorization") || strings.Contains(cell, "super-secret-token") {
		t.Errorf("request_headers = %q, must not contain the planted Authorization credential", cell)
	}
	// Positive control (paired with the negative check above): the drop is
	// selective, not an empty wipe of the whole header map.
	h := mustUnmarshalHeaders(t, cell)
	if h.Get("Content-Type") == "" {
		t.Errorf("request_headers = %q, want Content-Type to have survived", cell)
	}
}

func TestSelectExchange_DropsAPlantedUnknownHeader(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-exchange-drop-unknown")
	entity := mustCreateEntity(t, tx, tenant, "Drop Unknown Co", "60000014-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-UNKNOWN-01"})
	jobID := mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tenant, invoiceID: invID})
	// A header nobody has thought to blocklist -- proves ALLOWlist, not blocklist.
	mustCreateExchange(t, tx, exchangeFixture{
		tenantID: tenant, submissionJobID: jobID, invoiceID: invID,
		requestHeaders: mustMarshalHeaders(t, http.Header{
			"Content-Type":         {"application/json"},
			"X-Future-Adapter-Tag": {"v99-beta"},
		}),
	})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	bw := newFakeBodyWriter()
	if err := selectExchange(context.Background(), tx, []string{invID}, w, bw); err != nil {
		t.Fatalf("selectExchange: unexpected error: %v", err)
	}
	w.Flush()

	row := exchangeRowByInvoice(t, buf.Bytes(), invID)
	cell := row[colIndex(t, wantExchangeHeader, "request_headers")]
	if strings.Contains(strings.ToLower(cell), "x-future-adapter-tag") || strings.Contains(cell, "v99-beta") {
		t.Errorf("request_headers = %q, must not contain the unlisted X-Future-Adapter-Tag header", cell)
	}
	h := mustUnmarshalHeaders(t, cell)
	if h.Get("Content-Type") == "" {
		t.Errorf("request_headers = %q, want Content-Type to have survived", cell)
	}
}

// =====================================================================================
// AC-6: the poll handle -- one string, two writers (markJobPending's poll_ref and the
// archived 202 body's data.reference), never two.
// =====================================================================================

func TestSelectSubmissions_ArchivedPendingHandleEqualsStoredPollRef(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-poll-handle-equality")
	entity := mustCreateEntity(t, tx, tenant, "Poll Handle Co", "60000007-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-POLL-01"})
	pollRef := "APP-REF-2026-000456"
	jobID := mustCreateSubmissionJob(t, tx, submissionJobFixture{
		tenantID: tenant, invoiceID: invID, state: "pending", attempts: 1, pollRef: &pollRef,
	})
	// The mock adapter's own 202 body shape (mock_script.go:347-360): status/code/
	// message plus data.reference -- the SAME ref markJobPending stores as poll_ref
	// (worker.go's tx2, one Ref, two writers).
	respBody, err := json.Marshal(map[string]any{
		"status":  "PENDING",
		"code":    "PENDING",
		"message": "Invoice queued for clearance.",
		"data": map[string]any{
			"reference":        pollRef,
			"pollAfterSeconds": 5,
		},
	})
	if err != nil {
		t.Fatalf("test setup: marshal mock pending body: %v", err)
	}
	respBodyStr := string(respBody)
	exID := mustCreateExchange(t, tx, exchangeFixture{
		tenantID: tenant, submissionJobID: jobID, invoiceID: invID,
		operation: "poll", responseBody: &respBodyStr,
	})

	actingAs(t, tx, tenant)
	var subBuf, exBuf bytes.Buffer
	subW, exW := csv.NewWriter(&subBuf), csv.NewWriter(&exBuf)
	bw := newFakeBodyWriter()
	if err := selectSubmissions(context.Background(), tx, []string{invID}, subW); err != nil {
		t.Fatalf("selectSubmissions: unexpected error: %v", err)
	}
	if err := selectExchange(context.Background(), tx, []string{invID}, exW, bw); err != nil {
		t.Fatalf("selectExchange: unexpected error: %v", err)
	}
	subW.Flush()
	exW.Flush()

	subRow := submissionsRowByJobID(t, subBuf.Bytes(), jobID)
	gotPollRef := subRow[colIndex(t, wantSubmissionsHeader, "poll_ref")]
	if gotPollRef != pollRef {
		t.Fatalf("submissions.csv poll_ref = %q, want %q", gotPollRef, pollRef)
	}

	body, ok := bw.bodies["bodies/"+exID+".response"]
	if !ok {
		t.Fatalf("bodyWriter never received bodies/%s.response -- want the archived 202 body", exID)
	}
	var decoded struct {
		Data struct {
			Reference string `json:"reference"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("archived response body %q is not valid json: %v", body, err)
	}
	if decoded.Data.Reference != gotPollRef {
		t.Errorf("archived body .data.reference = %q, submissions.csv poll_ref = %q -- want one handle, not two (AC-6)",
			decoded.Data.Reference, gotPollRef)
	}
}

// A poll hop OVERWRITES poll_ref in place (worker.go:416-417) -- the row never grows a
// second handle. Plants the steady state after two hops (REF-1 then REF-2, the latter
// the only one still stored) and confirms exactly one row, carrying only the latest ref.
func TestSelectSubmissions_TwoHopChainKeepsOneHandlePerJob(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-two-hop-one-handle")
	entity := mustCreateEntity(t, tx, tenant, "Two Hop Co", "60000008-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-TWOHOP-01"})
	latest := "APP-REF-HOP-2"
	jobID := mustCreateSubmissionJob(t, tx, submissionJobFixture{
		tenantID: tenant, invoiceID: invID, state: "pending", attempts: 2, pollRef: &latest,
	})
	// Evidence of both hops, neither carrying poll_ref (D-10: only submissions.csv does).
	mustCreateExchange(t, tx, exchangeFixture{tenantID: tenant, submissionJobID: jobID, invoiceID: invID, operation: "poll", attempt: 1})
	mustCreateExchange(t, tx, exchangeFixture{tenantID: tenant, submissionJobID: jobID, invoiceID: invID, operation: "poll", attempt: 2})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := selectSubmissions(context.Background(), tx, []string{invID}, w); err != nil {
		t.Fatalf("selectSubmissions: unexpected error: %v", err)
	}
	w.Flush()

	rows := submissionsRowsForInvoice(t, buf.Bytes(), invID)
	if len(rows) != 1 {
		t.Fatalf("submissions.csv has %d rows for invoice %s, want exactly 1 (one row per job, not per poll hop)", len(rows), invID)
	}
	if got := rows[0][colIndex(t, wantSubmissionsHeader, "poll_ref")]; got != latest {
		t.Errorf("poll_ref = %q, want %q (the overwritten, latest handle)", got, latest)
	}
}

// Resubmission adds a SECOND submission_jobs row for the same invoice (a real re-submit,
// not a poll hop) -- two rows, each with its own handle, never one row holding both.
func TestSelectSubmissions_ResubmissionYieldsTwoRowsNotTwoHandlesOnOne(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-resubmission-two-rows")
	entity := mustCreateEntity(t, tx, tenant, "Resubmission Co", "60000009-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-RESUB-01", status: "failed"})
	refA, refB := "APP-REF-JOB-A", "APP-REF-JOB-B"
	jobA := mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tenant, invoiceID: invID, state: "failed", attempts: 3, pollRef: &refA})
	jobB := mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tenant, invoiceID: invID, state: "pending", attempts: 1, pollRef: &refB})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := selectSubmissions(context.Background(), tx, []string{invID}, w); err != nil {
		t.Fatalf("selectSubmissions: unexpected error: %v", err)
	}
	w.Flush()

	rows := submissionsRowsForInvoice(t, buf.Bytes(), invID)
	if len(rows) != 2 {
		t.Fatalf("submissions.csv has %d rows for invoice %s, want exactly 2 (one per job)", len(rows), invID)
	}
	jobIdx := colIndex(t, wantSubmissionsHeader, "submission_job_id")
	refIdx := colIndex(t, wantSubmissionsHeader, "poll_ref")
	got := map[string]string{}
	for _, row := range rows {
		got[row[jobIdx]] = row[refIdx]
	}
	if got[jobA] != refA {
		t.Errorf("job A (%s) poll_ref = %q, want %q", jobA, got[jobA], refA)
	}
	if got[jobB] != refB {
		t.Errorf("job B (%s) poll_ref = %q, want %q", jobB, got[jobB], refB)
	}
}

// =====================================================================================
// AC-7: outcome accepts all five values, including connection_failed.
// =====================================================================================

func TestSelectExchange_AcceptsConnectionFailedOutcome(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-connection-failed")
	entity := mustCreateEntity(t, tx, tenant, "Connection Failed Co", "60000010-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-CONNFAIL-01"})
	jobID := mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tenant, invoiceID: invID})
	mustCreateExchange(t, tx, exchangeFixture{tenantID: tenant, submissionJobID: jobID, invoiceID: invID, outcome: "connection_failed"})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	bw := newFakeBodyWriter()
	if err := selectExchange(context.Background(), tx, []string{invID}, w, bw); err != nil {
		t.Fatalf("selectExchange: unexpected error: %v", err)
	}
	w.Flush()
	row := exchangeRowByInvoice(t, buf.Bytes(), invID)
	if got := row[colIndex(t, wantExchangeHeader, "outcome")]; got != "connection_failed" {
		t.Errorf("outcome = %q, want %q", got, "connection_failed")
	}
}

// =====================================================================================
// AC-8: index-served in both plan-cache modes, at the corrected fixture scale (Stage 1
// measured 1,000/100 seq-scans; the flip point is ~4,000 invoices). Both cases are
// rollback-wrapped (BEGIN fixture...EXPLAIN...ROLLBACK), never commit-and-leave-behind.
// =====================================================================================

const (
	planInvoiceCount       = 6000
	planExchangeRowsPerJob = 3
	planTargetInvoiceCount = 507
	planExchangeTotalRows  = planInvoiceCount * planExchangeRowsPerJob
	planExchangeIdx        = "app_exchange_tenant_invoice_idx"
	planExchangeJobIdx     = "app_exchange_tenant_job_idx"
)

// buildExchangePlanCorpus plants AUDIT-05-05's corrected fixture (Stage 1 architecture,
// not the story's original 1,000/100 -- that size Seq Scans under the custom plan):
// 6,000 invoices, one submission_jobs row and 3 app_exchange rows each. Bulk INSERT...
// SELECT, not a 24,000-call Go loop.
func buildExchangePlanCorpus(t *testing.T, tx pgx.Tx, tenantID, entityID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := tx.Exec(ctx, `
		INSERT INTO invoices (id, tenant_id, entity_id, invoice_number)
		SELECT gen_random_uuid(), $1, $2, 'INV-PLAN-' || g
		  FROM generate_series(1, $3) g`, tenantID, entityID, planInvoiceCount); err != nil {
		t.Fatalf("bulk-insert plan invoices: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO submission_jobs (id, tenant_id, invoice_id, idempotency_key, adapter, adapter_version)
		SELECT gen_random_uuid(), tenant_id, id, 'idem-' || id, 'mock', 'v1'
		  FROM invoices WHERE tenant_id = $1`, tenantID); err != nil {
		t.Fatalf("bulk-insert plan submission_jobs: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO app_exchange (id, tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, adapter, adapter_version)
		SELECT gen_random_uuid(), sj.tenant_id, sj.id, sj.invoice_id, 'submit', 'sent', g, 'mock', 'v1'
		  FROM submission_jobs sj, generate_series(1, $2) g
		 WHERE sj.tenant_id = $1`, tenantID, planExchangeRowsPerJob); err != nil {
		t.Fatalf("bulk-insert plan app_exchange: %v", err)
	}

	// Control needle: confirm the corpus really landed at the pinned scale before
	// trusting any plan-shape assertion below.
	var invCount, jobCount, exCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenantID).Scan(&invCount); err != nil {
		t.Fatalf("control-needle count invoices: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM submission_jobs WHERE tenant_id = $1`, tenantID).Scan(&jobCount); err != nil {
		t.Fatalf("control-needle count submission_jobs: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM app_exchange WHERE tenant_id = $1`, tenantID).Scan(&exCount); err != nil {
		t.Fatalf("control-needle count app_exchange: %v", err)
	}
	if invCount != planInvoiceCount || jobCount != planInvoiceCount || exCount != planExchangeTotalRows {
		t.Fatalf("control needle: planted invoices=%d jobs=%d exchange=%d, want %d/%d/%d -- fixture setup is broken",
			invCount, jobCount, exCount, planInvoiceCount, planInvoiceCount, planExchangeTotalRows)
	}
}

func planTargetIDs(t *testing.T, tx pgx.Tx, tenantID string, n int) []string {
	t.Helper()
	rows, err := tx.Query(context.Background(),
		`SELECT id::text FROM invoices WHERE tenant_id = $1 ORDER BY id LIMIT $2`, tenantID, n)
	if err != nil {
		t.Fatalf("select plan target ids: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan plan target id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate plan target ids: %v", err)
	}
	if len(ids) != n {
		t.Fatalf("planTargetIDs: got %d ids, want %d", len(ids), n)
	}
	return ids
}

func explainExchangeQuery(t *testing.T, tx pgx.Tx, ids []string) string {
	t.Helper()
	rows, err := tx.Query(context.Background(), "EXPLAIN (COSTS OFF) "+selectExchangeSQL, ids)
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			t.Fatalf("scan explain line: %v", err)
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate explain: %v", err)
	}
	return strings.Join(lines, "\n")
}

func TestSelectExchange_IsIndexServedInBothPlanModes(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-exchange-plan-served")
	entity := mustCreateEntity(t, tx, tenant, "Exchange Plan Co", "70000001-0001")

	buildExchangePlanCorpus(t, tx, tenant, entity)

	// ANALYZE via the SUPERUSER tx, before the role switch below -- invoice_app
	// cannot ANALYZE (Postgres answers with a WARNING and silently does nothing,
	// which already produced one false measurement in this story).
	if _, err := tx.Exec(context.Background(), `ANALYZE app_exchange`); err != nil {
		t.Fatalf("ANALYZE app_exchange: %v", err)
	}

	targets := planTargetIDs(t, tx, tenant, planTargetInvoiceCount)

	actingAs(t, tx, tenant)
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		if _, err := tx.Exec(context.Background(), `SET LOCAL plan_cache_mode = `+mode); err != nil {
			t.Fatalf("[%s] SET LOCAL plan_cache_mode: %v", mode, err)
		}
		plan := explainExchangeQuery(t, tx, targets)
		if strings.Contains(plan, "Seq Scan on app_exchange") {
			t.Errorf("[%s] plan must not Seq Scan app_exchange:\n%s", mode, plan)
		}
		if !strings.Contains(plan, planExchangeIdx) {
			t.Errorf("[%s] plan does not mention %s:\n%s", mode, planExchangeIdx, plan)
		}
	}
}

// Control for the test above: omitting ANALYZE on the SAME corpus/scale demotes
// invoice_id off the index entirely (Stage 1 measured: a Filter on the WRONG index,
// app_exchange_tenant_job_idx). Proves the "must be index served" claim is not
// vacuously true regardless of statistics.
func TestSelectExchange_IndexAssertionIsNotVacuous(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-exchange-plan-vacuous")
	entity := mustCreateEntity(t, tx, tenant, "Exchange Plan Vacuous Co", "70000002-0001")

	buildExchangePlanCorpus(t, tx, tenant, entity)
	// Deliberately NO ANALYZE.
	targets := planTargetIDs(t, tx, tenant, planTargetInvoiceCount)

	actingAs(t, tx, tenant)
	if _, err := tx.Exec(context.Background(), `SET LOCAL plan_cache_mode = force_custom_plan`); err != nil {
		t.Fatalf("SET LOCAL plan_cache_mode: %v", err)
	}
	plan := explainExchangeQuery(t, tx, targets)
	if plan == "" {
		t.Fatal("EXPLAIN returned an empty plan -- the assertion below would pass vacuously")
	}
	if strings.Contains(plan, "Index Cond: (invoice_id") {
		t.Errorf("plan used invoice_id as an Index Cond WITHOUT ANALYZE -- the control has stopped proving "+
			"ANALYZE is load-bearing:\n%s", plan)
	}
}

// =====================================================================================
// AC-9: a non-empty id list with zero matching child rows writes a header-only CSV and
// issues no further work.
// =====================================================================================

func TestSelectSubmissions_NeverSubmittedInvoicesYieldHeaderOnly(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-submissions-header-only")
	entity := mustCreateEntity(t, tx, tenant, "Header Only Co", "60000011-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-NOJOB-01"})
	// No submission_jobs row planted at all (AC-9: 8/13 Honeywell-tenant demo invoices
	// have none -- this test scopes its own isolated tenant instead of relying on that
	// shared, tenant-specific figure).

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := selectSubmissions(context.Background(), tx, []string{invID}, w); err != nil {
		t.Fatalf("selectSubmissions: unexpected error: %v", err)
	}
	w.Flush()
	rows := parseCSV(t, buf.Bytes())
	if len(rows) != 1 {
		t.Fatalf("submissions.csv has %d rows, want exactly 1 (header only)", len(rows))
	}
	if !reflect.DeepEqual(rows[0], wantSubmissionsHeader) {
		t.Errorf("header row = %v, want %v", rows[0], wantSubmissionsHeader)
	}
}

func TestSelectExchange_NeverSubmittedInvoicesYieldHeaderOnly(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-exchange-header-only")
	entity := mustCreateEntity(t, tx, tenant, "Exchange Header Only Co", "60000012-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-NOEXCHANGE-01"})
	// No app_exchange row planted at all.

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	bw := newFakeBodyWriter()
	if err := selectExchange(context.Background(), tx, []string{invID}, w, bw); err != nil {
		t.Fatalf("selectExchange: unexpected error: %v", err)
	}
	w.Flush()
	rows := parseCSV(t, buf.Bytes())
	if len(rows) != 1 {
		t.Fatalf("exchange.csv has %d rows, want exactly 1 (header only)", len(rows))
	}
	if !reflect.DeepEqual(rows[0], wantExchangeHeader) {
		t.Errorf("header row = %v, want %v", rows[0], wantExchangeHeader)
	}
	if len(bw.bodies) != 0 {
		t.Errorf("bodyWriter received %d bodies, want 0 (AC-9: no further work)", len(bw.bodies))
	}
}
