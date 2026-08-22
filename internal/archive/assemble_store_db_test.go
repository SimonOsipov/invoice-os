// assemble_store_db_test.go: RED specs for AUDIT-05-07 (Mode A) -- the three specs
// D-38 says need a real pool and a COMMITTED fixture: Store.Assemble opens its own
// transaction on its own connection, so it cannot see an uncommitted superuser tx the
// way assemble_db_test.go's rollback-wrapped harness plants one. Cleanup is explicit
// ordered DELETEs (app_exchange, invoice_status_history, submission_jobs, invoices,
// business_entities, tenants) -- deleting the tenant row alone is unsafe:
// app_exchange_job_fk is ON DELETE RESTRICT while its tenant FK is ON DELETE CASCADE,
// and Postgres orders neither.
package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
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

// dbAppPoolTraced opens a fresh app-role pool with sql tracing. Store.Assemble owns
// its own transaction (unlike assemble(ctx, tx, ...), driven by the shared rollback-
// wrapped superuser tx elsewhere in this package), so observing its BEGIN/COMMIT/
// ROLLBACK needs a traced pool of its own.
func dbAppPoolTraced(t *testing.T) (*pgxpool.Pool, *sqlRecorder) {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("archive db-integration test skipped: set DATABASE_URL (or run `make test-archive`)")
	}
	rec := &sqlRecorder{}
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	cfg.ConnConfig.Tracer = rec
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect traced app pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping traced app pool: %v", err)
	}
	return pool, rec
}

// mustCommitFixture plants a fixture via plant (which should use the same
// mustCreateXxx helpers as the rollback-wrapped specs) and COMMITS it, registering
// cleanup in FK-safe order.
func mustCommitFixture(t *testing.T, super *pgxpool.Pool, plant func(tx pgx.Tx) string) string {
	t.Helper()
	ctx := context.Background()
	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin committed fixture tx: %v", err)
	}
	tenantID := plant(tx)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit fixture tx: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, stmt := range []string{
			`DELETE FROM app_exchange WHERE tenant_id = $1`,
			`DELETE FROM invoice_status_history WHERE tenant_id = $1`,
			`DELETE FROM submission_jobs WHERE tenant_id = $1`,
			`DELETE FROM invoices WHERE tenant_id = $1`,
			`DELETE FROM business_entities WHERE tenant_id = $1`,
			`DELETE FROM tenants WHERE id = $1`,
		} {
			if _, err := super.Exec(ctx, stmt, tenantID); err != nil {
				t.Errorf("committed-fixture cleanup %q: %v", stmt, err)
			}
		}
	})
	return tenantID
}

// TestAssemble_UsesRepeatableReadReadOnly (D-38, D-33): the emitted BEGIN must name
// the isolation level -- without this, D-33's isolation choice is only a comment. No
// committed fixture needed: an unknown entity 404s before any other statement runs,
// and the BEGIN already happened by then.
func TestAssemble_UsesRepeatableReadReadOnly(t *testing.T) {
	app, rec := dbAppPoolTraced(t)
	s := NewStore(app)
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: "system", TenantID: uuid.NewString()})

	err := s.Assemble(ctx, Request{EntityID: uuid.NewString(), From: time.Now(), To: time.Now()}, io.Discard)
	if !errors.Is(err, ErrEntityNotFound) {
		t.Fatalf("Store.Assemble(unknown entity): error = %v, want ErrEntityNotFound", err)
	}

	begins := rec.mentioning("begin")
	if len(begins) != 1 {
		t.Fatalf("Store.Assemble issued %d begin statement(s), want exactly 1: %q", len(begins), begins)
	}
	if begins[0] != "begin isolation level repeatable read read only" {
		t.Errorf("begin sql = %q, want %q (D-33)", begins[0], "begin isolation level repeatable read read only")
	}
}

// TestAssemble_ErrorRollsBackTheTransaction (D-38): Assemble only reads, so there is
// no probe row that could survive a commit -- the tracer is the oracle instead.
func TestAssemble_ErrorRollsBackTheTransaction(t *testing.T) {
	app, rec := dbAppPoolTraced(t)
	s := NewStore(app)
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: "system", TenantID: uuid.NewString()})

	err := s.Assemble(ctx, Request{EntityID: uuid.NewString(), From: time.Now(), To: time.Now()}, io.Discard)
	if err == nil {
		t.Fatal("Store.Assemble: want an error, got nil")
	}

	if n := len(rec.mentioning("rollback")); n != 1 {
		t.Errorf("Store.Assemble on error issued %d rollback statement(s), want exactly 1", n)
	}
	if n := len(rec.mentioning("commit")); n != 0 {
		t.Errorf("Store.Assemble on error issued %d commit statement(s), want 0", n)
	}
}

// concurrentCommitSink writes to an internal buffer, but on its FIRST write commits a
// new app_exchange row via a separate connection first. By the time any byte reaches
// this sink, selectEntity/countInvoices/resolveGeneratedBy/selectInvoices have already
// run inside Store.Assemble's own tx, so REPEATABLE READ's snapshot is long since
// fixed -- this fires safely after it, never before.
type concurrentCommitSink struct {
	buf    bytes.Buffer
	once   sync.Once
	commit func()
}

func (s *concurrentCommitSink) Write(p []byte) (int, error) {
	s.once.Do(s.commit)
	return s.buf.Write(p)
}

// TestAssemble_OneSnapshotSurvivesAConcurrentCommit (AC-1) replaces the story's
// TestAssemble_AllFourCSVsSeeOneSnapshot, which was a tautology: the child CSVs are
// queried WHERE invoice_id = ANY(ids) with ids taken FROM invoices.csv itself, so
// referential agreement holds by construction at any isolation level (D-38).
func TestAssemble_OneSnapshotSurvivesAConcurrentCommit(t *testing.T) {
	super := dbSuperPool(t)
	app, _ := dbAppPoolTraced(t)

	var entityID, invID, jobID string
	tenantID := mustCommitFixture(t, super, func(tx pgx.Tx) string {
		tid := mustCreateTenant(t, tx, "archive-store-snapshot")
		entityID = mustCreateEntity(t, tx, tid, "Snapshot Co", "80000012-0001")
		invID = mustCreateInvoice(t, tx, invoiceFixture{tenantID: tid, entityID: entityID, invoiceNumber: "INV-SNAPSHOT-01"})
		jobID = mustCreateSubmissionJob(t, tx, submissionJobFixture{tenantID: tid, invoiceID: invID})
		return tid
	})

	var concurrentExchangeID string
	sink := &concurrentCommitSink{}
	sink.commit = func() {
		ctx := context.Background()
		ctx2, err := super.Begin(ctx)
		if err != nil {
			t.Errorf("concurrent commit: begin: %v", err)
			return
		}
		concurrentExchangeID = mustCreateExchange(t, ctx2, exchangeFixture{tenantID: tenantID, submissionJobID: jobID, invoiceID: invID})
		if err := ctx2.Commit(ctx); err != nil {
			t.Errorf("concurrent commit: commit: %v", err)
		}
	}

	s := NewStore(app)
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: "system", TenantID: tenantID})
	from := time.Now().Add(-time.Hour)
	err := s.Assemble(ctx, Request{EntityID: entityID, From: from, To: from.Add(2 * time.Hour)}, sink)
	if err != nil {
		t.Fatalf("Store.Assemble: unexpected error: %v", err)
	}
	if concurrentExchangeID == "" {
		t.Fatal("concurrent commit never ran -- the snapshot assertion below proves nothing")
	}

	zr, err := zip.NewReader(bytes.NewReader(sink.buf.Bytes()), int64(sink.buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	raw := mustReadZipEntry(t, zr, "exchange.csv")
	if strings.Contains(string(raw), concurrentExchangeID) {
		t.Errorf("exchange.csv contains the concurrently-committed exchange id %s -- the REPEATABLE READ snapshot leaked a post-begin commit", concurrentExchangeID)
	}
}
