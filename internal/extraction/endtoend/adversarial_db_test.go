// adversarial_db_test.go: the negative half of the end-to-end suite -- the gates the happy path
// must be clearing rather than sailing past, the failure branch that must not read as success,
// and the teardown that must leave nothing behind.
package endtoend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/importer"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// eeImportAs runs the import hop under an arbitrary identity and returns the error instead of
// ending the test, so a refusal is observable.
func eeImportAs(ctx context.Context, h *eeHarness, subject, tenantID, entityID, documentID string) (importer.BatchResult, error) {
	svc := importer.NewService(importer.NewStore(h.app), invoice.NewStore(h.app), eeGate{})
	rctx := auth.WithIdentity(ctx, auth.Identity{
		Subject: subject, Role: "authenticated", TenantID: tenantID,
	})
	return svc.ImportDocument(rctx, entityID, documentID)
}

func eeCount(t *testing.T, ctx context.Context, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := eeRequire(t).super.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

// The happy path clears the membership gate rather than bypassing it: a uuid Subject with no
// membership row, and a suspended one, are both refused.
func TestRLS_EndToEndImportRefusesACallerWithoutAnActiveMembership(t *testing.T) {
	h := eeRequire(t)
	ctx := t.Context()
	w := eeSeed(t, ctx, eeLayout)
	eeExtract(t, ctx, w, eeLayout)

	// Control: the seeded subject really is a uuid with an active membership, so the
	// refusals below are the gate answering and not the non-uuid bypass
	// (internal/platform/db/tenant.go's uuid.Parse(id.Subject) arm).
	if _, err := uuid.Parse(w.subject); err != nil {
		t.Fatalf("seeded subject %q is not a uuid, so the membership gate is bypassed, not passed: %v", w.subject, err)
	}
	if got := eeCount(t, ctx,
		`SELECT count(*) FROM memberships WHERE tenant_id = $1 AND user_id = $2 AND status = 'active'`,
		w.tenantID, w.subject); got != 1 {
		t.Fatalf("seeded active memberships for subject %s = %d, want 1", w.subject, got)
	}

	stranger := uuid.NewString()
	if _, err := eeImportAs(ctx, h, stranger, w.tenantID, w.entityID, w.documentID); !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("ImportDocument as a uuid subject with no membership: err = %v, want %v", err, db.ErrNotActiveMember)
	}

	if _, err := h.super.Exec(ctx,
		`UPDATE memberships SET status = 'suspended' WHERE tenant_id = $1 AND user_id = $2`,
		w.tenantID, w.subject); err != nil {
		t.Fatalf("suspend the seeded membership: %v", err)
	}
	if _, err := eeImportAs(ctx, h, w.subject, w.tenantID, w.entityID, w.documentID); !errors.Is(err, db.ErrNotActiveMember) {
		t.Errorf("ImportDocument as a suspended member: err = %v, want %v", err, db.ErrNotActiveMember)
	}

	if got := len(eeInvoices(t, ctx, w.entityID)); got != 0 {
		t.Errorf("entity %s holds %d invoices row(s) after two refused imports, want 0", w.entityID, got)
	}
}

// A non-uuid subject takes the bypass arm and then trips invoice_status_history's
// actor CHECK, so an empty Subject cannot quietly write a row either.
func TestRLS_EndToEndAnEmptySubjectCannotWriteAnInvoice(t *testing.T) {
	h := eeRequire(t)
	ctx := t.Context()
	w := eeSeed(t, ctx, eeLayout)
	eeExtract(t, ctx, w, eeLayout)

	res, err := eeImportAs(ctx, h, "", w.tenantID, w.entityID, w.documentID)
	if err == nil && res.RowsValid > 0 {
		t.Fatalf("ImportDocument with an empty Subject reported %d valid row(s); an empty actor must not reach invoices", res.RowsValid)
	}
	if got := len(eeInvoices(t, ctx, w.entityID)); got != 0 {
		t.Errorf("entity %s holds %d invoices row(s) after an empty-Subject import, want 0", w.entityID, got)
	}
	// Control: the same call with the seeded uuid subject does write the row, so the
	// refusal above is the actor CHECK and not a broken fixture.
	if _, err := eeImportAs(ctx, h, w.subject, w.tenantID, w.entityID, w.documentID); err != nil {
		t.Fatalf("ImportDocument as the seeded member: %v", err)
	}
	if got := len(eeInvoices(t, ctx, w.entityID)); got != 1 {
		t.Errorf("entity %s holds %d invoices row(s) after the seeded member imported, want 1", w.entityID, got)
	}
}

// Tenant A cannot import tenant B's document, in either pairing of entity and document.
func TestRLS_EndToEndImportRefusesACrossTenantDocument(t *testing.T) {
	h := eeRequire(t)
	ctx := t.Context()
	a := eeSeed(t, ctx, eeLayout)
	b := eeSeed(t, ctx, eeLayout)
	eeExtract(t, ctx, b, eeLayout)

	for _, c := range []struct {
		name               string
		entityID, document string
	}{
		{"B's entity and document under A's identity", b.entityID, b.documentID},
		{"A's entity, B's document", a.entityID, b.documentID},
	} {
		if _, err := eeImportAs(ctx, h, a.subject, a.tenantID, c.entityID, c.document); !errors.Is(err, importer.ErrNotFound) {
			t.Errorf("%s: err = %v, want %v", c.name, err, importer.ErrNotFound)
		}
	}

	for _, e := range []struct {
		who, id string
	}{{"A", a.entityID}, {"B", b.entityID}} {
		if got := len(eeInvoices(t, ctx, e.id)); got != 0 {
			t.Errorf("entity %s (%s) holds %d invoices row(s) after the cross-tenant attempts, want 0", e.id, e.who, got)
		}
	}

	// Control: B's own identity does import B's document, so the refusals above are RLS
	// and not an unimportable fixture.
	if _, err := eeImportAs(ctx, h, b.subject, b.tenantID, b.entityID, b.documentID); err != nil {
		t.Fatalf("ImportDocument as B: %v", err)
	}
	if got := len(eeInvoices(t, ctx, b.entityID)); got != 1 {
		t.Errorf("entity %s (B) holds %d invoices row(s) after B imported its own document, want 1", b.entityID, got)
	}
}

// The happy path's BatchResult is a clean single-row import, not a quarantined batch. AC-4's
// row count alone cannot tell those apart before the row is written.
func TestRLS_EndToEndTheBatchIsCleanNotQuarantined(t *testing.T) {
	ctx := t.Context()
	w := eeSeed(t, ctx, eeLayout)
	eeExtract(t, ctx, w, eeLayout)

	res := eeImport(t, ctx, w)
	if res.Status != "completed" || res.RowsTotal != 1 || res.RowsValid != 1 ||
		res.RowsInvalid != 0 || res.QuarantinedInvoices != 0 || len(res.Errors) != 0 {
		t.Errorf("BatchResult = {status %q total %d valid %d invalid %d quarantined %d errors %v}, want a clean 1-row import",
			res.Status, res.RowsTotal, res.RowsValid, res.RowsInvalid, res.QuarantinedInvoices, res.Errors)
	}
}

// A worker whose document port fails must leave the job non-succeeded with no rank-0 rows, so
// eeAwaitSucceeded cannot return on it and no invoice can be minted from it.
func TestRLS_EndToEndAFailedExtractionIsNotMistakenForSuccess(t *testing.T) {
	h := eeRequire(t)
	ctx := t.Context()
	w := eeSeed(t, ctx, eeLayout)

	bundle := river.NewWorkers()
	ew := eeWorker(t, nil)
	ew.Open = func(context.Context, string) (extraction.Document, error) {
		return extraction.Document{}, errors.New("the document port is unavailable")
	}
	extraction.AddTo(bundle, ew)
	c, err := queue.New(h.app, queue.Config{
		Queues:  map[string]river.QueueConfig{extraction.QueueName: {MaxWorkers: 1}},
		Workers: bundle,
	})
	if err != nil {
		t.Fatalf("build extraction worker client: %v", err)
	}
	if err := db.WithinTenantTx(ctx, h.app, w.tenantID, func(tx pgx.Tx) error {
		_, e := extraction.EnqueueExtraction(ctx, tx, c, w.tenantID, w.documentID)
		return e
	}); err != nil {
		t.Fatalf("enqueue extraction for document %s: %v", w.documentID, err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start extraction worker pool: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), eeStopBudget)
		defer cancel()
		if err := c.Stop(stopCtx); err != nil {
			hardCtx, hardCancel := context.WithTimeout(context.Background(), eeStopBudget)
			defer hardCancel()
			if err := c.River().StopAndCancel(hardCtx); err != nil {
				t.Errorf("stop extraction worker pool: %v", err)
			}
		}
	})

	var jobID, state string
	deadline := time.Now().Add(eeExtractBudget)
	for {
		err := h.super.QueryRow(ctx,
			`SELECT id, state FROM extraction_jobs WHERE tenant_id = $1 AND document_id = $2`,
			w.tenantID, w.documentID).Scan(&jobID, &state)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("read the extraction_jobs row: %v", err)
		}
		if err == nil && (state == "failed" || state == "dead_lettered" || state == "succeeded") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("extraction is %q after %v, want a failure state", state, eeExtractBudget)
		}
		time.Sleep(eePollEvery)
	}
	if state == "succeeded" {
		t.Fatalf("extraction succeeded with a failing document port -- eeAwaitSucceeded would return on it")
	}

	if got := len(eeFieldResults(t, ctx, jobID)); got != 0 {
		t.Errorf("failed extraction job %s wrote %d extraction_field_results row(s), want 0", jobID, got)
	}
	if _, err := eeImportAs(ctx, h, w.subject, w.tenantID, w.entityID, w.documentID); !errors.Is(err, importer.ErrNotFound) {
		t.Errorf("ImportDocument after a failed extraction: err = %v, want %v", err, importer.ErrNotFound)
	}
	if got := len(eeInvoices(t, ctx, w.entityID)); got != 0 {
		t.Errorf("entity %s holds %d invoices row(s) after a failed extraction, want 0", w.entityID, got)
	}
}

// eeOrphanTables are every table a full run writes that the harness must drain. The counts are
// read after a scoped subtest returns, so its t.Cleanup has already run.
var eeOrphanTables = []struct{ table, sql string }{
	{"tenants", `SELECT count(*) FROM tenants WHERE id = $1`},
	{"memberships", `SELECT count(*) FROM memberships WHERE tenant_id = $1`},
	{"business_entities", `SELECT count(*) FROM business_entities WHERE tenant_id = $1`},
	{"documents", `SELECT count(*) FROM documents WHERE tenant_id = $1`},
	{"extraction_jobs", `SELECT count(*) FROM extraction_jobs WHERE tenant_id = $1`},
	{"extraction_field_results", `SELECT count(*) FROM extraction_field_results WHERE tenant_id = $1`},
	{"import_batches", `SELECT count(*) FROM import_batches WHERE tenant_id = $1`},
	{"invoices", `SELECT count(*) FROM invoices WHERE tenant_id = $1`},
	{"audit_log", `SELECT count(*) FROM audit_log WHERE tenant_id = $1`},
	{"idempotency_keys", `SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1`},
	{"river_job", `SELECT count(*) FROM river_job WHERE args->>'tenant_id' = $1`},
}

// A killed run strands an orphan tenant and a stuck river_job, so the drain is asserted rather
// than assumed. river_job carries no tenant_id column and audit_log refuses DELETE for every
// role, which are the two rows the cascade cannot reach.
func TestRLS_EndToEndTeardownDrainsEveryTableItWrote(t *testing.T) {
	h := eeRequire(t)
	ctx := context.Background()

	var w eeWorld
	t.Run("one full run", func(t *testing.T) {
		sub := t.Context()
		w = eeSeed(t, sub, eeLayout)
		eeExtract(t, sub, w, eeLayout)
		eeImport(t, sub, w)

		// Control: every table below really was written, or the zero counts after the
		// subtest would prove nothing.
		for _, tbl := range eeOrphanTables {
			var n int
			if err := h.super.QueryRow(sub, tbl.sql, w.tenantID).Scan(&n); err != nil {
				t.Fatalf("count %s during the run: %v", tbl.table, err)
			}
			if n == 0 {
				t.Errorf("%s held 0 row(s) for tenant %s during the run; a table nothing writes cannot show a teardown leak", tbl.table, w.tenantID)
			}
		}
	})

	if w.tenantID == "" {
		t.Fatal("the scoped run seeded no tenant, so the drain below reads clean vacuously")
	}
	for _, tbl := range eeOrphanTables {
		var n int
		if err := h.super.QueryRow(ctx, tbl.sql, w.tenantID).Scan(&n); err != nil {
			t.Fatalf("count %s after teardown: %v", tbl.table, err)
		}
		if n != 0 {
			t.Errorf("%s still holds %d row(s) for tenant %s after teardown", tbl.table, n, w.tenantID)
		}
	}
}

// AC-1. A production .go file here would compile into cmd/ and put this suite's imports on a
// shipped binary's dependency graph.
func TestEndToEndPackage_HoldsOnlyTestFiles(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	var goFiles []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".go" {
			goFiles = append(goFiles, e.Name())
		}
	}
	if len(goFiles) < eeMinTestFiles {
		t.Fatalf("read %d .go file(s) in internal/extraction/endtoend, want at least %d -- the scan is reading the wrong directory", len(goFiles), eeMinTestFiles)
	}
	// Control needle: the file the whole suite hangs off must be one of them.
	if !containsName(goFiles, eeSkipFile) {
		t.Fatalf("the .go files here are %v; %s is absent, so the scan below is not reading this package", goFiles, eeSkipFile)
	}
	for _, name := range goFiles {
		if !strings.HasSuffix(name, "_test.go") {
			t.Errorf("%s is a production file in a test-only package", name)
		}
	}
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}
