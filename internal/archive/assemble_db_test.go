// assemble_db_test.go: RED specs for AUDIT-05-07 (Mode A) -- the unexported
// assemble(ctx, tx, r, w, assembleOpts) against a real Postgres, driven through
// entity_db_test.go's rollback-wrapped harness (dbSuperPool/beginFixtureTx/actingAs/
// mustCreateTenant/mustCreateEntity/mustCreateInvoice/mustCreateSubmissionJob/
// mustCreateExchange). Three specs need Store.Assemble's own pool transaction instead
// (D-38) and live in assemble_store_db_test.go.
package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// countingWriter counts bytes written and discards them -- AC-2/AC-3 need "zero
// bytes written", not merely "an error returned".
type countingWriter struct{ n int }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	return len(p), nil
}

// mustReadZipEntry returns one entry's decompressed bytes, or fails the test.
func mustReadZipEntry(t *testing.T, zr *zip.Reader, name string) []byte {
	t.Helper()
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open zip entry %s: %v", name, err)
		}
		defer rc.Close()
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatalf("read zip entry %s: %v", name, err)
		}
		return buf.Bytes()
	}
	t.Fatalf("zip has no entry %q", name)
	return nil
}

// --- AC-2: unknown entity writes nothing -------------------------------------------

func TestAssemble_UnknownEntityWritesNothing(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-assemble-unknown-entity")
	actingAs(t, tx, tenant)

	var cw countingWriter
	from := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	err := assemble(context.Background(), tx, Request{EntityID: uuid.NewString(), From: from, To: from.Add(time.Hour)}, &cw,
		assembleOpts{tenantID: tenant, subject: "system", maxInvoices: maxBundleInvoices, now: time.Now()})
	if !errors.Is(err, ErrEntityNotFound) {
		t.Fatalf("assemble(unknown entity) error = %v, want ErrEntityNotFound", err)
	}
	if cw.n != 0 {
		t.Errorf("assemble(unknown entity) wrote %d bytes, want 0", cw.n)
	}
}

// --- AC-3: the cap -------------------------------------------------------------------

func TestAssemble_OverTheCapRefusesBeforeWritingAnything(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-assemble-over-cap")
	entity := mustCreateEntity(t, tx, tenant, "Over Cap Co", "80000003-0001")
	from := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		mustCreateInvoice(t, tx, invoiceFixture{
			tenantID: tenant, entityID: entity, invoiceNumber: fmt.Sprintf("INV-CAP-%d", i),
			createdAt: from.Add(time.Duration(i) * time.Minute),
		})
	}

	actingAs(t, tx, tenant)
	var cw countingWriter
	err := assemble(context.Background(), tx, Request{EntityID: entity, From: from, To: from.Add(time.Hour)}, &cw,
		assembleOpts{tenantID: tenant, subject: "system", maxInvoices: 2, now: time.Now()})

	var tooMany *TooManyInvoicesError
	if !errors.As(err, &tooMany) {
		t.Fatalf("assemble(3 invoices, cap 2) error = %v, want *TooManyInvoicesError", err)
	}
	if tooMany.Count != 3 || tooMany.Limit != 2 {
		t.Errorf("TooManyInvoicesError = %+v, want Count=3 Limit=2", tooMany)
	}
	if cw.n != 0 {
		t.Errorf("assemble(over cap) wrote %d bytes, want 0", cw.n)
	}
}

func TestAssemble_AtExactlyTheCapSucceeds(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-assemble-at-cap")
	entity := mustCreateEntity(t, tx, tenant, "At Cap Co", "80000004-0001")
	from := time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		mustCreateInvoice(t, tx, invoiceFixture{
			tenantID: tenant, entityID: entity, invoiceNumber: fmt.Sprintf("INV-ATCAP-%d", i),
			createdAt: from.Add(time.Duration(i) * time.Minute),
		})
	}

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	err := assemble(context.Background(), tx, Request{EntityID: entity, From: from, To: from.Add(time.Hour)}, &buf,
		assembleOpts{tenantID: tenant, subject: "system", maxInvoices: 3, now: time.Now()})
	if err != nil {
		t.Fatalf("assemble(3 invoices, cap 3): unexpected error: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	rows := parseCSV(t, mustReadZipEntry(t, zr, "invoices.csv"))
	if len(rows) != 4 {
		t.Errorf("invoices.csv has %d rows, want 4 (header + 3 invoices)", len(rows))
	}
}

// --- AC-4: empty period ----------------------------------------------------------

func TestAssemble_EmptyPeriodProducesHeaderOnlyCSVs(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-assemble-empty-period")
	entity := mustCreateEntity(t, tx, tenant, "Empty Period Co", "80000005-0001")
	from := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	err := assemble(context.Background(), tx, Request{EntityID: entity, From: from, To: from.Add(time.Hour)}, &buf,
		assembleOpts{tenantID: tenant, subject: "system", maxInvoices: maxBundleInvoices, now: time.Now()})
	if err != nil {
		t.Fatalf("assemble(empty period): unexpected error: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	for _, name := range []string{"invoices.csv", "status_history.csv", "submissions.csv", "exchange.csv"} {
		rows := parseCSV(t, mustReadZipEntry(t, zr, name))
		if len(rows) != 1 {
			t.Errorf("%s has %d rows, want 1 (header only)", name, len(rows))
		}
	}
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "bodies/") {
			t.Errorf("zip contains a bodies/ entry %q, want none for an empty period", f.Name)
		}
	}

	var doc struct {
		Counts struct {
			Invoices, StatusTransitions, Submissions, ExchangeAttempts, BodyFiles int
		} `json:"counts"`
	}
	if err := json.Unmarshal(mustReadZipEntry(t, zr, "manifest.json"), &doc); err != nil {
		t.Fatalf("unmarshal manifest.json: %v", err)
	}
	if doc.Counts.Invoices != 0 || doc.Counts.StatusTransitions != 0 || doc.Counts.Submissions != 0 ||
		doc.Counts.ExchangeAttempts != 0 || doc.Counts.BodyFiles != 0 {
		t.Errorf("manifest counts = %+v, want all zero", doc.Counts)
	}
}

func TestAssemble_EmptyPeriodStillCarriesTheManifest(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-assemble-empty-manifest")
	entity := mustCreateEntity(t, tx, tenant, "Empty Manifest Co", "80000006-0001")
	from := time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	err := assemble(context.Background(), tx, Request{EntityID: entity, From: from, To: from.Add(time.Hour)}, &buf,
		assembleOpts{tenantID: tenant, subject: "system", maxInvoices: maxBundleInvoices, now: time.Now()})
	if err != nil {
		t.Fatalf("assemble(empty period): unexpected error: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	var doc struct {
		Entity struct{ ID, Name string } `json:"entity"`
		Period struct{ From, To string } `json:"period"`
	}
	if err := json.Unmarshal(mustReadZipEntry(t, zr, "manifest.json"), &doc); err != nil {
		t.Fatalf("unmarshal manifest.json: %v", err)
	}
	if doc.Entity.ID != entity || doc.Entity.Name != "Empty Manifest Co" {
		t.Errorf("manifest entity = %+v, want ID=%q Name=%q", doc.Entity, entity, "Empty Manifest Co")
	}
	if doc.Period.From == "" || doc.Period.To == "" {
		t.Errorf("manifest period = %+v, want both From and To set", doc.Period)
	}
}

// --- AC-5: invoices without submissions ---------------------------------------------

func TestAssemble_InvoicesWithoutSubmissionsProduceAMixedBundle(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-assemble-mixed-bundle")
	entity := mustCreateEntity(t, tx, tenant, "Mixed Bundle Co", "80000007-0001")
	from := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)
	statuses := []string{"draft", "validated", "draft"}
	for i, status := range statuses {
		mustCreateInvoice(t, tx, invoiceFixture{
			tenantID: tenant, entityID: entity, invoiceNumber: fmt.Sprintf("INV-MIXED-%d", i),
			status: status, createdAt: from.Add(time.Duration(i) * time.Minute),
		})
	}
	// No submission_jobs / app_exchange rows planted at all (AC-5: "no submissions").

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	err := assemble(context.Background(), tx, Request{EntityID: entity, From: from, To: from.Add(time.Hour)}, &buf,
		assembleOpts{tenantID: tenant, subject: "system", maxInvoices: maxBundleInvoices, now: time.Now()})
	if err != nil {
		t.Fatalf("assemble: unexpected error: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	invRows := parseCSV(t, mustReadZipEntry(t, zr, "invoices.csv"))
	if len(invRows) != 4 {
		t.Fatalf("invoices.csv has %d rows, want 4 (header + 3)", len(invRows))
	}
	for _, name := range []string{"submissions.csv", "exchange.csv"} {
		rows := parseCSV(t, mustReadZipEntry(t, zr, name))
		if len(rows) != 1 {
			t.Errorf("%s has %d rows, want 1 (header only)", name, len(rows))
		}
	}

	var doc struct {
		Counts struct {
			Invoices, Submissions, ExchangeAttempts int
		} `json:"counts"`
	}
	if err := json.Unmarshal(mustReadZipEntry(t, zr, "manifest.json"), &doc); err != nil {
		t.Fatalf("unmarshal manifest.json: %v", err)
	}
	if doc.Counts.Invoices != 3 || doc.Counts.Submissions != 0 || doc.Counts.ExchangeAttempts != 0 {
		t.Errorf("manifest counts = %+v, want Invoices=3 Submissions=0 ExchangeAttempts=0", doc.Counts)
	}
}

// --- AC-1 (RLS): tenant B cannot reach tenant A's entity ----------------------------

func TestRLS_AssembleUnderTenantBSeesNothingOfTenantA(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenantA := mustCreateTenant(t, tx, "archive-assemble-rls-a")
	tenantB := mustCreateTenant(t, tx, "archive-assemble-rls-b")
	entityA := mustCreateEntity(t, tx, tenantA, "RLS Assemble Co", "80000008-0001")
	from := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenantA, entityID: entityA, invoiceNumber: "INV-ASSEMBLE-RLS-01", createdAt: from.Add(time.Hour)})

	// Control needle (superuser, pre-actingAs): the fixture really planted the entity.
	var planted int
	if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM business_entities WHERE id = $1`, entityA).Scan(&planted); err != nil {
		t.Fatalf("control-needle count: %v", err)
	}
	if planted != 1 {
		t.Fatalf("control needle: entityA row count = %d, want 1 -- fixture setup is broken", planted)
	}

	actingAs(t, tx, tenantB)
	var cw countingWriter
	err := assemble(context.Background(), tx, Request{EntityID: entityA, From: from, To: from.Add(2 * time.Hour)}, &cw,
		assembleOpts{tenantID: tenantB, subject: "system", maxInvoices: maxBundleInvoices, now: time.Now()})
	if !errors.Is(err, ErrEntityNotFound) {
		t.Errorf("assemble(tenant B acting on tenant A's entity) error = %v, want ErrEntityNotFound", err)
	}
	if cw.n != 0 {
		t.Errorf("assemble(tenant B acting on tenant A's entity) wrote %d bytes, want 0", cw.n)
	}
}

// --- AC-7: a mid-stream writer error leaves no central directory -------------------

// failAfterWriter accepts bytes up to limit, then fails -- simulating a client
// disconnect mid-stream. Retains what it accepted so the test can still feed it to
// zip.NewReader afterward (D-38).
type failAfterWriter struct {
	limit   int
	written int
	buf     bytes.Buffer
}

var errSimulatedWriteFailure = errors.New("test: simulated write failure")

func (w *failAfterWriter) Write(p []byte) (int, error) {
	if w.written >= w.limit {
		return 0, errSimulatedWriteFailure
	}
	n, err := w.buf.Write(p)
	w.written += n
	return n, err
}

func TestAssemble_WriterErrorMidStreamLeavesNoCentralDirectory(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-assemble-writer-error")
	entity := mustCreateEntity(t, tx, tenant, "Writer Error Co", "80000002-0001")
	from := time.Date(2027, 7, 1, 0, 0, 0, 0, time.UTC)
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-WRITERR-01", createdAt: from})
	jobID := mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tenant, invoiceID: invID})
	// Incompressible body (D-36/D-38): compressible filler never reaches 8 KiB under
	// zip's 4096-byte bufio, so the failing writer would never fire.
	body := randomBase64Body(t, 42, 64*1024)
	mustCreateExchange(t, tx, exchangeFixture{tenantID: tenant, submissionJobID: jobID, invoiceID: invID, requestBody: &body})

	actingAs(t, tx, tenant)
	fw := &failAfterWriter{limit: 8 * 1024}
	err := assemble(context.Background(), tx, Request{EntityID: entity, From: from, To: from.Add(time.Hour)}, fw,
		assembleOpts{tenantID: tenant, subject: "system", maxInvoices: maxBundleInvoices, now: time.Now()})
	if err == nil {
		t.Fatal("assemble: want an error from the failing writer, got nil")
	}
	if fw.written == 0 {
		t.Fatal("failAfterWriter never received any bytes -- the assertion below would pass on an implementation that wrote nothing")
	}
	if _, zerr := zip.NewReader(bytes.NewReader(fw.buf.Bytes()), int64(fw.written)); zerr == nil {
		t.Error("zip.NewReader over the abandoned bytes succeeded, want an error (no central directory written)")
	} else if !errors.Is(zerr, zip.ErrFormat) {
		t.Errorf("zip.NewReader error = %v, want errors.Is(_, zip.ErrFormat)", zerr)
	}
}

// --- AC-6: peak memory does not scale with total body size -------------------------

const (
	memRowCount       = 20
	memLargeBodyBytes = 1 << 20 // 1 MiB (encoded size)
	memSmallBodyBytes = 1 << 10 // 1 KiB (encoded size)
	memLargeProbeAt   = 8 << 20 // 8 MiB reached the wire
	memSmallProbeAt   = 8 << 10 // proportional point in the much smaller corpus
)

// randomBase64Body returns base64 of encodedLen/4*3 seeded random bytes -- near-
// incompressible (D-36: repeated CSV-like filler compresses to 0.42%, defeating any
// "bytes reached the wire" assertion) and valid UTF-8 text (app_exchange.request_body
// is `text`, so raw random bytes would be rejected).
func randomBase64Body(t *testing.T, seed int64, encodedLen int) string {
	t.Helper()
	if encodedLen%4 != 0 {
		t.Fatalf("encodedLen %d must be a multiple of 4 for exact base64 sizing", encodedLen)
	}
	raw := make([]byte, encodedLen/4*3)
	rng := rand.New(rand.NewSource(seed))
	if _, err := rng.Read(raw); err != nil {
		t.Fatalf("generate random body bytes: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// liveHeap forces two GC cycles then reads resident heap -- TotalAlloc (the repo's
// one MemStats precedent, internal/approval/policy_test.go:1036-1060) measures churn,
// which DOES scale with total body size and would fail on correct code (D-36).
func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// discardProbe discards written bytes but counts them, firing probe exactly once the
// running total first reaches at -- never a bytes.Buffer, whose retained output would
// dominate the very delta being measured.
type discardProbe struct {
	at    int64
	total int64
	fired bool
	probe func()
}

func (d *discardProbe) Write(p []byte) (int, error) {
	d.total += int64(len(p))
	if !d.fired && d.total >= d.at {
		d.fired = true
		d.probe()
	}
	return len(p), nil
}

// TestAssemble_PeakAllocDoesNotScaleWithBodySize (AC-6, do NOT t.Parallel -- D-36's
// live-heap sampling is corrupted by a concurrent goroutine's own allocations).
func TestAssemble_PeakAllocDoesNotScaleWithBodySize(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-mem-peak")
	entity := mustCreateEntity(t, tx, tenant, "Mem Peak Co", "80000001-0001")

	largeFrom := time.Date(2027, 8, 1, 0, 0, 0, 0, time.UTC)
	smallFrom := time.Date(2027, 9, 1, 0, 0, 0, 0, time.UTC)

	invLarge := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-MEM-LARGE", createdAt: largeFrom})
	jobLarge := mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tenant, invoiceID: invLarge})
	for i := 0; i < memRowCount; i++ {
		body := randomBase64Body(t, int64(1000+i), memLargeBodyBytes)
		mustCreateExchange(t, tx, exchangeFixture{tenantID: tenant, submissionJobID: jobLarge, invoiceID: invLarge, attempt: i + 1, requestBody: &body})
	}

	invSmall := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-MEM-SMALL", createdAt: smallFrom})
	jobSmall := mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tenant, invoiceID: invSmall})
	for i := 0; i < memRowCount; i++ {
		body := randomBase64Body(t, int64(2000+i), memSmallBodyBytes)
		mustCreateExchange(t, tx, exchangeFixture{tenantID: tenant, submissionJobID: jobSmall, invoiceID: invSmall, attempt: i + 1, requestBody: &body})
	}

	actingAs(t, tx, tenant)

	runOnce := func(from time.Time, probeAt int64) (delta int64, fired bool) {
		baseline := int64(liveHeap())
		var probed int64
		sink := &discardProbe{at: probeAt, probe: func() { probed = int64(liveHeap()) - baseline }}
		req := Request{EntityID: entity, From: from, To: from.Add(time.Hour)}
		opts := assembleOpts{tenantID: tenant, subject: "system", maxInvoices: maxBundleInvoices, now: time.Now()}
		if err := assemble(context.Background(), tx, req, sink, opts); err != nil {
			t.Fatalf("assemble: unexpected error: %v", err)
		}
		return probed, sink.fired
	}

	largeDelta, largeFired := runOnce(largeFrom, memLargeProbeAt)
	if !largeFired {
		t.Fatal("large-body run: probe never fired -- fewer than 8 MiB reached the sink, so the delta below proves nothing")
	}
	smallDelta, smallFired := runOnce(smallFrom, memSmallProbeAt)
	if !smallFired {
		t.Fatal("small-body run: probe never fired")
	}

	bound := smallDelta + 6*int64(memLargeBodyBytes)
	corpusBytes := int64(memRowCount * memLargeBodyBytes)
	if bound >= corpusBytes {
		t.Fatalf("non-vacuity guard: bound %d is not below the %d-byte corpus -- a fully-buffering implementation could still pass", bound, corpusBytes)
	}
	if largeDelta >= bound {
		t.Errorf("large-body live-heap delta = %d, want < %d (small delta %d + 6x one body) -- peak scaled with TOTAL body size, not the largest single body", largeDelta, bound, smallDelta)
	}
}
