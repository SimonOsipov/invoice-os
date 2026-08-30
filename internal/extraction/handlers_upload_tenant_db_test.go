// handlers_upload_tenant_db_test.go: what POST /v1/documents leaves behind belongs to the
// CALLER'S tenant and to nobody else. Package extraction_test, so it shares store_db_test.go's
// TestMain, per-role pools and single skip site (stRequire), and worker_db_test.go's fixtures.
package extraction_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// upVisibleAs counts the documents rows a tenant can SEE for a given id. RLS filters a SELECT
// rather than raising, so a leak shows up as a row count, not an error.
func upVisibleAs(t *testing.T, ctx context.Context, readerTenant, documentID string) int {
	t.Helper()
	var n int
	err := db.WithinTenantTx(ctx, stRequire(t).app, readerTenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM documents WHERE id = $1`, documentID).Scan(&n)
	})
	if err != nil {
		t.Fatalf("read document %s as tenant %s: %v", documentID, readerTenant, err)
	}
	return n
}

// TestRLS_UploadIsInvisibleToAnotherTenant: tenant B uploads, tenant A sees nothing of it --
// not the row, not the job, not the idempotency key.
//
// Every "A sees zero" assertion is paired with the positive control that makes it mean
// something: A can see its OWN document through the same read, and B can see the one it just
// uploaded. Without those, a query that matched nothing at all would pass.
func TestRLS_UploadIsInvisibleToAnotherTenant(t *testing.T) {
	ctx := t.Context()
	tenantA, documentA := wkFixture(t, ctx)
	tenantB, _ := wkFixture(t, ctx)

	sink := &upSink{}
	store := upRealStore(t, tenantB, sink)
	enqueue := upRealEnqueue(t, wkInsertClient(t), tenantB)

	beforeA := upDocumentCount(t, ctx, tenantA)
	if beforeA == 0 {
		t.Fatalf("tenant %s holds no documents before the upload; wkFixture seeds one, so the unchanged-count assertion below would be about an empty table", tenantA)
	}

	code, body := upServeDB(t, upDBIdentity(tenantB), store, enqueue, "b_only.pdf", "application/pdf", []byte("%PDF-1.7 tenant B"))
	if code != http.StatusCreated {
		t.Fatalf("tenant B's upload returned %d, want 201 (body=%s)", code, body)
	}

	var uploaded string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT id::text FROM documents WHERE tenant_id = $1 AND filename = $2`,
		tenantB, "b_only.pdf").Scan(&uploaded); err != nil {
		t.Fatalf("look up tenant B's uploaded document: %v", err)
	}

	// Control, then the claim.
	if got := upVisibleAs(t, ctx, tenantB, uploaded); got != 1 {
		t.Fatalf("tenant B sees %d row(s) for the document it just uploaded, want 1; the invisibility assertion below would pass over a row nobody can see", got)
	}
	if got := upVisibleAs(t, ctx, tenantA, documentA); got != 1 {
		t.Fatalf("tenant A sees %d row(s) for its OWN document, want 1; this read finds nothing for anyone, so the assertion below proves nothing", got)
	}
	if got := upVisibleAs(t, ctx, tenantA, uploaded); got != 0 {
		t.Errorf("tenant A sees %d row(s) for tenant B's uploaded document %s, want 0", got, uploaded)
	}

	if got := upDocumentCount(t, ctx, tenantA); got != beforeA {
		t.Errorf("tenant A holds %d documents after tenant B's upload, want the unchanged %d", got, beforeA)
	}

	// The job and its business key are tenant B's alone.
	row := eqOneJob(t, ctx, tenantB)
	if row.argsDocument != uploaded {
		t.Errorf("tenant B's river_job %d carries document_id %q, want the uploaded %q", row.id, row.argsDocument, uploaded)
	}
	if row.argsTenant != tenantB {
		t.Errorf("tenant B's river_job %d carries tenant_id %q, want %q", row.id, row.argsTenant, tenantB)
	}
	if got := eqJobRows(t, ctx, tenantA); len(got) != 0 {
		t.Errorf("tenant A owns %d river_job row(s) after tenant B's upload, want 0", len(got))
	}
	if n := eqKeyCount(t, ctx, tenantB); n != 1 {
		t.Errorf("tenant B holds %d idempotency_keys row(s), want 1", n)
	}
	if n := eqKeyCount(t, ctx, tenantA); n != 0 {
		t.Errorf("tenant A holds %d idempotency_keys row(s) after tenant B's upload, want 0", n)
	}
}

// TestRLS_UploadCannotWriteADocumentUnderAnotherTenant: the store seam is injected, so the
// only thing standing between a mis-wired adapter and a cross-tenant write is documents'
// tenant_isolation policy. The SQLSTATE is asserted, not merely "an error": a FK violation, a
// NOT NULL violation or a typo in the SQL would all satisfy "err != nil" while proving nothing
// about the policy.
//
// The handler must answer 500 and enqueue nothing: a refused write leaves no document, and a
// job over a document that does not exist burns that id's permanent extract: key.
func TestRLS_UploadCannotWriteADocumentUnderAnotherTenant(t *testing.T) {
	ctx := t.Context()
	tenantA, _ := wkFixture(t, ctx)
	tenantB, _ := wkFixture(t, ctx)

	// A seam scoped to A's transaction that tries to write the row under B.
	var storeErr error
	store := func(ctx context.Context, filename, contentType string, size int64, body io.ReadSeeker) (extraction.StoredDocument, error) {
		if _, err := io.Copy(io.Discard, body); err != nil {
			return extraction.StoredDocument{}, err
		}
		var id string
		storeErr = db.WithinTenantTx(ctx, stRequire(t).app, tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes, filename)
				 VALUES ($1, $2, $3, $4, $5) RETURNING id::text`,
				tenantB, "tenants/"+tenantB+"/cross", strings.Repeat("c", 32)+strings.ReplaceAll(uuid.NewString(), "-", ""), size, filename).Scan(&id)
		})
		if storeErr != nil {
			return extraction.StoredDocument{}, storeErr
		}
		return extraction.StoredDocument{ID: id, Filename: filename, ContentType: contentType, SizeBytes: size}, nil
	}

	enqueued := 0
	enqueue := func(context.Context, string) (bool, error) { enqueued++; return false, nil }

	beforeA, beforeB := upDocumentCount(t, ctx, tenantA), upDocumentCount(t, ctx, tenantB)

	code, body := upServeDB(t, upDBIdentity(tenantA), store, enqueue, "cross.pdf", "application/pdf", []byte("%PDF-1.7 cross"))

	var pgErr *pgconn.PgError
	if !errors.As(storeErr, &pgErr) || pgErr.Code != eqRLSViolation {
		t.Fatalf("inserting a documents row for tenant %s inside tenant %s's transaction returned %v, want SQLSTATE %s from documents' tenant_isolation policy",
			tenantB, tenantA, storeErr, eqRLSViolation)
	}
	if code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 for a store the database refused (body=%s)", code, body)
	}
	if enqueued != 0 {
		t.Errorf("enqueue ran %d time(s) after a refused store, want 0 -- there is no document to extract and the extract: key never comes back", enqueued)
	}
	if got := upDocumentCount(t, ctx, tenantA); got != beforeA {
		t.Errorf("tenant A holds %d documents after the refused write, want the unchanged %d", got, beforeA)
	}
	if got := upDocumentCount(t, ctx, tenantB); got != beforeB {
		t.Errorf("tenant B holds %d documents after the refused write, want the unchanged %d", got, beforeB)
	}
}
