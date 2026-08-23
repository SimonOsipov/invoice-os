// preview_db_test.go: RED specs for AUDIT-05-09 (Mode A) -- Store.Preview/preview
// against a real Postgres. Most specs reuse entity_db_test.go's rollback-wrapped
// harness plus invoices_db_test.go/history_db_test.go/exchange_db_test.go's fixtures.
// Two specs (D-46) need a committed fixture via assemble_store_db_test.go's
// dbAppPoolTraced/mustCommitFixture -- the D-38 carve-out, unchanged for this subtask.
package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- download-side oracles (D-48): three independent paths, not one ------------------

// mustAssembleBundle runs the real assembler in the same tx as the preview call under
// test, as the download-side oracle. Not itself under test here.
func mustAssembleBundle(t *testing.T, tx pgx.Tx, r Request, o assembleOpts) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := assemble(context.Background(), tx, r, &buf, o); err != nil {
		t.Fatalf("assemble (oracle bundle): %v", err)
	}
	return buf.Bytes()
}

// csvDataRecordCount parses a CSV entry and returns its row count minus the header --
// selectXxx always writes a header even for zero data rows.
func csvDataRecordCount(t *testing.T, zr *zip.Reader, name string) int {
	t.Helper()
	rows, err := csv.NewReader(bytes.NewReader(mustReadZipEntry(t, zr, name))).ReadAll()
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	if len(rows) == 0 {
		t.Fatalf("%s has no rows, not even a header", name)
	}
	return len(rows) - 1
}

// bodyEntryCount counts bodies/-prefixed ZIP entries -- the third download-side oracle.
func bodyEntryCount(zr *zip.Reader) int {
	n := 0
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "bodies/") {
			n++
		}
	}
	return n
}

// manifestOracleCounts re-reads manifest.json's own counts object -- the second
// download-side oracle, independent of the CSV parse above.
func manifestOracleCounts(t *testing.T, zr *zip.Reader) manifestCounts {
	t.Helper()
	var doc manifestDoc
	if err := json.Unmarshal(mustReadZipEntry(t, zr, "manifest.json"), &doc); err != nil {
		t.Fatalf("unmarshal manifest.json: %v", err)
	}
	return doc.Counts
}

// previewQuery builds the query values a Request would arrive as over HTTP.
func previewQuery(r Request) url.Values {
	return url.Values{"entity_id": {r.EntityID}, "from": {r.From.UTC().Format(time.RFC3339)}, "to": {r.To.UTC().Format(time.RFC3339)}}
}

// --- AC-1: counts agree with the download, from three independent oracles ------------

// TestPreview_CountsMatchTheDownloadedBundle (D-48): the fixture's five counts must be
// pairwise distinct and non-zero, or a swapped pair is invisible -- asserted first,
// against the test's OWN expectations, before any fixture is planted.
func TestPreview_CountsMatchTheDownloadedBundle(t *testing.T) {
	want := manifestCounts{Invoices: 2, StatusTransitions: 4, Submissions: 3, ExchangeAttempts: 5, BodyFiles: 6}
	vals := []int{want.Invoices, want.StatusTransitions, want.Submissions, want.ExchangeAttempts, want.BodyFiles}
	seen := map[int]bool{}
	for _, v := range vals {
		if v == 0 {
			t.Fatalf("test setup: fixture counts %v contain a zero -- cannot discriminate a missing count", vals)
		}
		if seen[v] {
			t.Fatalf("test setup: fixture counts %v are not pairwise distinct -- cannot discriminate a swapped pair", vals)
		}
		seen[v] = true
	}

	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-preview-counts")
	entity := mustCreateEntity(t, tx, tenant, "Preview Counts Co", "80000020-0001")
	from := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

	var invIDs []string
	for i := 0; i < 2; i++ {
		invIDs = append(invIDs, mustCreateInvoice(t, tx, invoiceFixture{
			tenantID: tenant, entityID: entity, invoiceNumber: fmt.Sprintf("INV-PC-%d", i),
			createdAt: from.Add(time.Duration(i) * time.Minute),
		}))
	}

	var jobIDs []string
	for i := 0; i < 3; i++ {
		jobIDs = append(jobIDs, mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tenant, invoiceID: invIDs[i%2]}))
	}

	for i := 0; i < 4; i++ {
		mustCreateHistoryRow(t, tx, historyFixture{tenantID: tenant, invoiceID: invIDs[i%2], toStatus: "validated", actor: "system"})
	}

	body := "x"
	for i := 0; i < 5; i++ {
		f := exchangeFixture{tenantID: tenant, submissionJobID: jobIDs[i%3], invoiceID: invIDs[i%2], requestBody: &body}
		if i == 0 {
			f.responseBody = &body // the one row carrying both bodies
		}
		mustCreateExchange(t, tx, f)
	}

	actingAs(t, tx, tenant)
	r := Request{EntityID: entity, From: from, To: from.Add(time.Hour)}

	bundle := mustAssembleBundle(t, tx, r, assembleOpts{tenantID: tenant, subject: "system", maxInvoices: 10, now: time.Now()})
	zr := mustReadZip(t, bundle)

	csvOracle := manifestCounts{
		Invoices:          csvDataRecordCount(t, zr, "invoices.csv"),
		StatusTransitions: csvDataRecordCount(t, zr, "status_history.csv"),
		Submissions:       csvDataRecordCount(t, zr, "submissions.csv"),
		ExchangeAttempts:  csvDataRecordCount(t, zr, "exchange.csv"),
		BodyFiles:         bodyEntryCount(zr),
	}
	manifestOracle := manifestOracleCounts(t, zr)
	if csvOracle != want {
		t.Fatalf("test control: CSV-parsed oracle = %+v, want the pinned fixture %+v -- fixture setup is wrong", csvOracle, want)
	}
	if manifestOracle != want {
		t.Fatalf("test control: manifest.json oracle = %+v, want the pinned fixture %+v", manifestOracle, want)
	}

	got, err := preview(context.Background(), tx, r, previewOpts{maxInvoices: 10})
	if err != nil {
		t.Fatalf("preview: unexpected error: %v", err)
	}
	if got.Counts != want {
		t.Errorf("preview.Counts = %+v, want the pinned fixture %+v", got.Counts, want)
	}
	if got.Counts != csvOracle {
		t.Errorf("preview.Counts = %+v, want it to equal the CSV-parsed oracle %+v", got.Counts, csvOracle)
	}
	if got.Counts != manifestOracle {
		t.Errorf("preview.Counts = %+v, want it to equal the manifest.json oracle %+v", got.Counts, manifestOracle)
	}
}

// TestPreview_UsesTheSamePeriodBoundsAsTheDownload (D-48): a relative-only comparison
// passes on a shared exclusive bound, so counts.invoices==2 is pinned as a literal too --
// the only half that can fail when both sides wrongly exclude an endpoint.
func TestPreview_UsesTheSamePeriodBoundsAsTheDownload(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-preview-bounds")
	entity := mustCreateEntity(t, tx, tenant, "Preview Bounds Co", "80000021-0001")

	from := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2027, 6, 30, 23, 59, 59, 0, time.UTC)
	mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-BOUND-BEFORE", createdAt: from.Add(-time.Second)})
	mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-BOUND-AT-FROM", createdAt: from})
	mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-BOUND-AT-TO", createdAt: to})
	mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-BOUND-AFTER", createdAt: to.Add(time.Second)})

	actingAs(t, tx, tenant)
	r := Request{EntityID: entity, From: from, To: to}

	bundle := mustAssembleBundle(t, tx, r, assembleOpts{tenantID: tenant, subject: "system", maxInvoices: 10, now: time.Now()})
	downloadCount := csvDataRecordCount(t, mustReadZip(t, bundle), "invoices.csv")

	got, err := preview(context.Background(), tx, r, previewOpts{maxInvoices: 10})
	if err != nil {
		t.Fatalf("preview: unexpected error: %v", err)
	}
	if got.Counts.Invoices != 2 {
		t.Errorf("preview.Counts.Invoices = %d, want literal 2 (D-4: inclusive bounds admit exactly-at-from and exactly-at-to)", got.Counts.Invoices)
	}
	if got.Counts.Invoices != downloadCount {
		t.Errorf("preview.Counts.Invoices = %d, download's invoices.csv has %d data rows -- must agree", got.Counts.Invoices, downloadCount)
	}
}

func TestPreview_NeverSubmittedInvoicesCountZeroSubmissions(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-preview-never-submitted")
	entity := mustCreateEntity(t, tx, tenant, "Never Submitted Co", "80000022-0001")
	from := time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: fmt.Sprintf("INV-NS-%d", i), createdAt: from.Add(time.Duration(i) * time.Minute)})
	}

	actingAs(t, tx, tenant)
	got, err := preview(context.Background(), tx, Request{EntityID: entity, From: from, To: from.Add(time.Hour)}, previewOpts{maxInvoices: 10})
	if err != nil {
		t.Fatalf("preview: unexpected error: %v", err)
	}
	want := manifestCounts{Invoices: 3}
	if got.Counts != want {
		t.Errorf("preview.Counts = %+v, want %+v", got.Counts, want)
	}
}

// --- AC-2: filename matches the download's Content-Disposition (D-50) ----------------

type filenameProbe struct{ served, preview string }

// servedAndPreviewFilenames drives BOTH DownloadHandler and PreviewHandler through the
// same rollback-wrapped tx. The served header is parsed with mime.ParseMediaType per
// the corrected oracle (D-50) -- never compared as a raw string, which is never equal.
func servedAndPreviewFilenames(t *testing.T, tx pgx.Tx, r Request) filenameProbe {
	t.Helper()
	assembleFn := func(ctx context.Context, req Request, w io.Writer, onStart func(string)) error {
		return assemble(ctx, tx, req, w, assembleOpts{tenantID: "irrelevant", subject: "system", maxInvoices: 10, now: time.Now()})
	}
	dh := DownloadHandler(assembleFn, slog.Default())
	dRec := httptest.NewRecorder()
	dh.ServeHTTP(dRec, newTestRequest(t, previewQuery(r), true))
	if dRec.Code != http.StatusOK {
		t.Fatalf("download: status = %d, want 200 (body %s)", dRec.Code, dRec.Body.String())
	}
	_, params, err := mime.ParseMediaType(dRec.Header().Get("Content-Disposition"))
	if err != nil {
		t.Fatalf("mime.ParseMediaType(%q): %v", dRec.Header().Get("Content-Disposition"), err)
	}

	previewFn := func(ctx context.Context, req Request) (Preview, error) {
		return preview(ctx, tx, req, previewOpts{maxInvoices: 10})
	}
	ph := PreviewHandler(previewFn, slog.Default())
	pRec := httptest.NewRecorder()
	ph.ServeHTTP(pRec, newTestRequest(t, previewQuery(r), true))
	if pRec.Code != http.StatusOK {
		t.Fatalf("preview: status = %d, want 200 (body %s)", pRec.Code, pRec.Body.String())
	}
	var doc struct {
		Filename string `json:"filename"`
	}
	if err := json.NewDecoder(pRec.Body).Decode(&doc); err != nil {
		t.Fatalf("decode preview body: %v", err)
	}
	return filenameProbe{served: params["filename"], preview: doc.Filename}
}

func TestPreview_FilenameMatchesTheDownloadDisposition(t *testing.T) {
	t.Run("unquoted", func(t *testing.T) {
		super := dbSuperPool(t)
		tx := beginFixtureTx(t, super)
		tenant := mustCreateTenant(t, tx, "archive-preview-filename-unquoted")
		entity := mustCreateEntity(t, tx, tenant, "Honeywell Group", "80000023-0001")
		actingAs(t, tx, tenant)

		r := Request{EntityID: entity, From: mustParseRFC3339(t, validFrom), To: mustParseRFC3339(t, validTo)}
		wantFilename := bundleFilename("Honeywell Group", r)
		// Control needle (D-44): pins the fixture to the story's own worked example.
		if want := "ASComply_evidence_Honeywell-Group_20260101_20260331.zip"; wantFilename != want {
			t.Fatalf("test setup: bundleFilename computed %q, want %q", wantFilename, want)
		}

		got := servedAndPreviewFilenames(t, tx, r)
		if got.served != wantFilename {
			t.Errorf("served Content-Disposition filename = %q, want %q", got.served, wantFilename)
		}
		if got.preview != wantFilename {
			t.Errorf("preview.Filename = %q, want %q", got.preview, wantFilename)
		}
	})

	t.Run("quoted urn:uuid: fallback", func(t *testing.T) {
		super := dbSuperPool(t)
		tx := beginFixtureTx(t, super)
		tenant := mustCreateTenant(t, tx, "archive-preview-filename-quoted")
		entity := mustCreateEntity(t, tx, tenant, "———", "80000024-0001") // no alnum char: falls back to r.EntityID
		actingAs(t, tx, tenant)

		r := Request{EntityID: "urn:uuid:" + entity, From: mustParseRFC3339(t, validFrom), To: mustParseRFC3339(t, validTo)}
		wantFilename := bundleFilename("———", r)
		if !strings.Contains(wantFilename, ":") {
			t.Fatalf("test setup: bundleFilename(non-alnum name) = %q, want it to contain ':' (the urn:uuid: fallback)", wantFilename)
		}

		got := servedAndPreviewFilenames(t, tx, r)
		if got.served != wantFilename {
			t.Errorf("served Content-Disposition filename = %q, want %q", got.served, wantFilename)
		}
		if got.preview != wantFilename {
			t.Errorf("preview.Filename = %q, want %q", got.preview, wantFilename)
		}
	})
}

// --- AC-3: empty period is a real result, never a 404 in disguise --------------------

func TestPreview_EmptyPeriodIs200WithZeroCounts(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-preview-empty-period")
	entity := mustCreateEntity(t, tx, tenant, "Empty Period Co", "80000025-0001")
	// An invoice exists but far outside the queried period.
	mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-EP-1", createdAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)})

	actingAs(t, tx, tenant)
	from := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	got, err := preview(context.Background(), tx, Request{EntityID: entity, From: from, To: from.Add(time.Hour)}, previewOpts{maxInvoices: 10})
	if err != nil {
		t.Fatalf("preview: unexpected error: %v (an entity that exists must not 404, AC-3)", err)
	}
	if want := (manifestCounts{}); got.Counts != want {
		t.Errorf("preview.Counts = %+v, want all-zero %+v", got.Counts, want)
	}
	if got.OverLimit {
		t.Error("preview.OverLimit = true, want false")
	}
	// Control: entity.name is the real name, proving this resolved rather than 404ing.
	if got.Entity.Name != "Empty Period Co" {
		t.Errorf("preview.Entity.Name = %q, want %q (control: proves the entity really resolved)", got.Entity.Name, "Empty Period Co")
	}
}

// --- AC-4: over_limit is a 200 carrying the real counts (D-51) -----------------------

func TestPreview_OverLimitIs200WithTheFlagSet(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-preview-over-limit")
	entity := mustCreateEntity(t, tx, tenant, "Over Limit Co", "80000026-0001")
	from := time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)

	var invIDs []string
	for i := 0; i < 3; i++ {
		invIDs = append(invIDs, mustCreateInvoice(t, tx, invoiceFixture{
			tenantID: tenant, entityID: entity, invoiceNumber: fmt.Sprintf("INV-OL-%d", i), createdAt: from.Add(time.Duration(i) * time.Minute),
		}))
	}
	jobID := mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tenant, invoiceID: invIDs[0]})
	mustCreateHistoryRow(t, tx, historyFixture{tenantID: tenant, invoiceID: invIDs[0], toStatus: "validated", actor: "system"})
	mustCreateExchange(t, tx, exchangeFixture{tenantID: tenant, submissionJobID: jobID, invoiceID: invIDs[0]})

	actingAs(t, tx, tenant)
	got, err := preview(context.Background(), tx, Request{EntityID: entity, From: from, To: from.Add(time.Hour)}, previewOpts{maxInvoices: 2})
	if err != nil {
		t.Fatalf("preview: unexpected error: %v (over-limit must still be a 200-shaped result, D-51)", err)
	}
	if !got.OverLimit {
		t.Error("preview.OverLimit = false, want true (3 invoices over cap 2)")
	}
	if got.Counts.Invoices != 3 {
		t.Errorf("preview.Counts.Invoices = %d, want 3", got.Counts.Invoices)
	}
	// The child counts must be real, not short-circuited to zero (D-51).
	if got.Counts.Submissions == 0 || got.Counts.StatusTransitions == 0 || got.Counts.ExchangeAttempts == 0 {
		t.Errorf("preview.Counts = %+v, want the real non-zero child counts even over the cap", got.Counts)
	}
}

// TestPreview_AtTheCapExactlyIsNotOverLimit (D-48, added): the story's cap-2/3-invoices
// spec passes under both > and >=. Cap 3 / 3 invoices must be false; cap 3 / 4 must be
// true, as the positive control.
func TestPreview_AtTheCapExactlyIsNotOverLimit(t *testing.T) {
	t.Run("cap equals count", func(t *testing.T) {
		super := dbSuperPool(t)
		tx := beginFixtureTx(t, super)
		tenant := mustCreateTenant(t, tx, "archive-preview-at-cap")
		entity := mustCreateEntity(t, tx, tenant, "At Cap Co", "80000027-0001")
		from := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)
		for i := 0; i < 3; i++ {
			mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: fmt.Sprintf("INV-ATCAP-%d", i), createdAt: from.Add(time.Duration(i) * time.Minute)})
		}
		actingAs(t, tx, tenant)
		got, err := preview(context.Background(), tx, Request{EntityID: entity, From: from, To: from.Add(time.Hour)}, previewOpts{maxInvoices: 3})
		if err != nil {
			t.Fatalf("preview: unexpected error: %v", err)
		}
		if got.OverLimit {
			t.Error("preview.OverLimit = true for cap 3 / 3 invoices, want false (guards > vs >=)")
		}
	})

	t.Run("count exceeds cap by one", func(t *testing.T) {
		super := dbSuperPool(t)
		tx := beginFixtureTx(t, super)
		tenant := mustCreateTenant(t, tx, "archive-preview-over-cap-by-one")
		entity := mustCreateEntity(t, tx, tenant, "Over Cap By One Co", "80000028-0001")
		from := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)
		for i := 0; i < 4; i++ {
			mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: fmt.Sprintf("INV-OC1-%d", i), createdAt: from.Add(time.Duration(i) * time.Minute)})
		}
		actingAs(t, tx, tenant)
		got, err := preview(context.Background(), tx, Request{EntityID: entity, From: from, To: from.Add(time.Hour)}, previewOpts{maxInvoices: 3})
		if err != nil {
			t.Fatalf("preview: unexpected error: %v", err)
		}
		if !got.OverLimit {
			t.Error("preview.OverLimit = false for cap 3 / 4 invoices, want true (positive control)")
		}
	})
}

// TestPreview_EmptyBodyStringStillCountsAsABodyFile (D-47/D-48): app_exchange has six
// CHECK constraints, none on the body columns, both text NULL -- an empty string is
// representable and is the one place SQL's IS NOT NULL could drift from Go's != nil.
func TestPreview_EmptyBodyStringStillCountsAsABodyFile(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-preview-empty-body")
	entity := mustCreateEntity(t, tx, tenant, "Empty Body Co", "80000029-0001")
	from := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	inv := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-EB-1", createdAt: from})
	job := mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tenant, invoiceID: inv})
	empty := ""
	mustCreateExchange(t, tx, exchangeFixture{tenantID: tenant, submissionJobID: job, invoiceID: inv, requestBody: &empty})

	actingAs(t, tx, tenant)
	r := Request{EntityID: entity, From: from, To: from.Add(time.Hour)}

	bundle := mustAssembleBundle(t, tx, r, assembleOpts{tenantID: tenant, subject: "system", maxInvoices: 10, now: time.Now()})
	if n := bodyEntryCount(mustReadZip(t, bundle)); n != 1 {
		t.Fatalf("test control: download produced %d bodies/ entries, want 1 (request_body='' must still be a file)", n)
	}

	got, err := preview(context.Background(), tx, r, previewOpts{maxInvoices: 10})
	if err != nil {
		t.Fatalf("preview: unexpected error: %v", err)
	}
	if got.Counts.BodyFiles != 1 {
		t.Errorf("preview.Counts.BodyFiles = %d, want 1 (SQL IS NOT NULL must agree with Go's != nil on an empty string)", got.Counts.BodyFiles)
	}
}

// --- AC-5: an entity the caller cannot see is a 404, identically to the download -----

func TestPreview_UnknownEntityIs404(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-preview-unknown-entity")
	actingAs(t, tx, tenant)

	r := Request{EntityID: uuid.NewString(), From: mustParseRFC3339(t, validFrom), To: mustParseRFC3339(t, validTo)}
	if _, err := preview(context.Background(), tx, r, previewOpts{maxInvoices: 10}); !errors.Is(err, ErrEntityNotFound) {
		t.Fatalf("preview(unknown entity) error = %v, want ErrEntityNotFound", err)
	}

	previewFn := func(ctx context.Context, req Request) (Preview, error) {
		return preview(ctx, tx, req, previewOpts{maxInvoices: 10})
	}
	ph := PreviewHandler(previewFn, slog.Default())
	rec := httptest.NewRecorder()
	ph.ServeHTTP(rec, newTestRequest(t, previewQuery(r), true))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body) != 1 {
		t.Errorf("body has %d top-level keys, want exactly 1 (\"error\") -- no partial Preview shape must leak: %s", len(body), rec.Body.String())
	}
	var errMsg string
	_ = json.Unmarshal(body["error"], &errMsg)
	if errMsg != "not found" {
		t.Errorf("error = %q, want %q", errMsg, "not found")
	}
}

func TestRLS_PreviewOfAnotherTenantsEntityIs404(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenantA := mustCreateTenant(t, tx, "archive-preview-tenant-a")
	tenantB := mustCreateTenant(t, tx, "archive-preview-tenant-b")
	entityA := mustCreateEntity(t, tx, tenantA, "Tenant A Preview Co", "80000030-0001")

	// Control needle (superuser, pre-actingAs): the fixture really planted the row.
	var planted int
	if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM business_entities WHERE id = $1`, entityA).Scan(&planted); err != nil {
		t.Fatalf("control-needle count: %v", err)
	}
	if planted != 1 {
		t.Fatalf("control needle: entityA row count = %d, want 1 -- fixture setup is broken", planted)
	}

	actingAs(t, tx, tenantB)
	r := Request{EntityID: entityA, From: mustParseRFC3339(t, validFrom), To: mustParseRFC3339(t, validTo)}
	if _, err := preview(context.Background(), tx, r, previewOpts{maxInvoices: 10}); !errors.Is(err, ErrEntityNotFound) {
		t.Fatalf("preview(another tenant's entity) error = %v, want ErrEntityNotFound", err)
	}

	previewFn := func(ctx context.Context, req Request) (Preview, error) {
		return preview(ctx, tx, req, previewOpts{maxInvoices: 10})
	}
	ph := PreviewHandler(previewFn, slog.Default())
	rec := httptest.NewRecorder()
	ph.ServeHTTP(rec, newTestRequest(t, previewQuery(r), true))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, entityA) {
		t.Errorf("body %q names the entity id, want it absent", body)
	}
	if strings.Contains(body, "Tenant A Preview Co") {
		t.Errorf("body %q names the entity name, want it absent", body)
	}
}

// --- D-46: isolation -- Store.Preview owns its own tx, needs the real pool -----------

// TestPreview_UsesRepeatableReadReadOnly mirrors TestAssemble_UsesRepeatableReadReadOnly
// exactly: without this, D-46's isolation choice is only a comment.
func TestPreview_UsesRepeatableReadReadOnly(t *testing.T) {
	app, rec := dbAppPoolTraced(t)
	s := NewStore(app)
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: "system", TenantID: uuid.NewString()})

	_, err := s.Preview(ctx, Request{EntityID: uuid.NewString(), From: time.Now(), To: time.Now()})
	if !errors.Is(err, ErrEntityNotFound) {
		t.Fatalf("Store.Preview(unknown entity): error = %v, want ErrEntityNotFound", err)
	}

	begins := rec.mentioning("begin")
	if len(begins) != 1 {
		t.Fatalf("Store.Preview issued %d begin statement(s), want exactly 1: %q", len(begins), begins)
	}
	if begins[0] != "begin isolation level repeatable read read only" {
		t.Errorf("begin sql = %q, want %q", begins[0], "begin isolation level repeatable read read only")
	}
}

// hookTracer embeds sqlRecorder rather than adding a field to the shared one in
// history_db_test.go, and fires hook once, the moment a query mentioning
// "FROM app_exchange" starts -- always after every earlier statement in the tx has
// already fixed the REPEATABLE READ snapshot.
type hookTracer struct {
	sqlRecorder
	once sync.Once
	hook func()
}

func (h *hookTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.Contains(d.SQL, "FROM app_exchange") {
		h.once.Do(h.hook)
	}
	return h.sqlRecorder.TraceQueryStart(ctx, conn, d)
}

// dbAppPoolWithTracer is dbAppPoolTraced generalized to an arbitrary tracer --
// hookTracer is not a *sqlRecorder, so it can't reuse that helper directly.
func dbAppPoolWithTracer(t *testing.T, tracer pgx.QueryTracer) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("archive db-integration test skipped: set DATABASE_URL (or run `make test-archive`)")
	}
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect traced app pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping traced app pool: %v", err)
	}
	return pool
}

// TestPreview_OneSnapshotSurvivesAConcurrentCommit is the discriminating D-46 spec: a
// second pool commits a new app_exchange row for an invoice already in the id list,
// timed to land after the snapshot is fixed but before the exchange count query runs.
func TestPreview_OneSnapshotSurvivesAConcurrentCommit(t *testing.T) {
	super := dbSuperPool(t)

	var entityID, invID, jobID string
	tenantID := mustCommitFixture(t, super, func(tx pgx.Tx) string {
		tid := mustCreateTenant(t, tx, "archive-preview-snapshot")
		entityID = mustCreateEntity(t, tx, tid, "Preview Snapshot Co", "80000031-0001")
		invID = mustCreateInvoice(t, tx, invoiceFixture{tenantID: tid, entityID: entityID, invoiceNumber: "INV-PREVIEW-SNAPSHOT"})
		jobID = mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tid, invoiceID: invID})
		body := "seed"
		mustCreateExchange(t, tx, exchangeFixture{tenantID: tid, submissionJobID: jobID, invoiceID: invID, requestBody: &body, responseBody: &body})
		return tid
	})

	var hookFired bool
	tracer := &hookTracer{}
	tracer.hook = func() {
		hookFired = true
		ctx2, err := super.Begin(context.Background())
		if err != nil {
			t.Errorf("concurrent commit: begin: %v", err)
			return
		}
		extraBody := "concurrent"
		mustCreateExchange(t, ctx2, exchangeFixture{tenantID: tenantID, submissionJobID: jobID, invoiceID: invID, requestBody: &extraBody})
		if err := ctx2.Commit(context.Background()); err != nil {
			t.Errorf("concurrent commit: commit: %v", err)
		}
	}
	app := dbAppPoolWithTracer(t, tracer)

	s := NewStore(app)
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: "system", TenantID: tenantID})
	from := time.Now().Add(-time.Hour)
	got, err := s.Preview(ctx, Request{EntityID: entityID, From: from, To: from.Add(2 * time.Hour)})
	if err != nil {
		t.Fatalf("Store.Preview: unexpected error: %v", err)
	}
	if !hookFired {
		t.Fatal("the concurrent-commit hook never fired -- the snapshot assertions below prove nothing")
	}

	var superCount int
	if err := super.QueryRow(context.Background(), `SELECT count(*) FROM app_exchange WHERE invoice_id = $1`, invID).Scan(&superCount); err != nil {
		t.Fatalf("post-hoc superuser count: %v", err)
	}
	if superCount != 2 {
		t.Fatalf("control: superuser sees %d app_exchange rows for the invoice, want 2 -- the concurrent commit did not really land", superCount)
	}

	if got.Counts.ExchangeAttempts != 1 {
		t.Errorf("preview.Counts.ExchangeAttempts = %d, want 1 (the fixture's own number -- REPEATABLE READ must not see the concurrent commit)", got.Counts.ExchangeAttempts)
	}
	if got.Counts.BodyFiles != 2 {
		t.Errorf("preview.Counts.BodyFiles = %d, want 2 (the fixture's own number)", got.Counts.BodyFiles)
	}
}
