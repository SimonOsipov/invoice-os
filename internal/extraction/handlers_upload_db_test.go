// handlers_upload_db_test.go: the two POST /v1/documents claims a spy cannot settle -- a
// refusal writes no documents row and no object, and a re-upload enqueues once. Package
// extraction_test, so it shares store_db_test.go's TestMain, per-role pools and single skip
// site (stRequire), and worker_db_test.go's fixtures.
//
// The store seam is spelled in SQL here rather than driven through document.Service:
// deps_test.go's fence forbids internal/extraction, tests included, from importing
// internal/document. The dedupe itself belongs to document.Store.Upsert and is pinned in that
// package; what these specs own is the handler's ORDERING around it.
package extraction_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// upSink counts object PUTs. The real store PUTs before it inserts the row
// (TestServiceStore_PutPrecedesRowInsert), so a refusal that reached the store would move this
// counter even if the row write then failed.
type upSink struct {
	mu   sync.Mutex
	puts int
}

func (s *upSink) put() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
}

func (s *upSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts
}

// upDBIdentity is the caller for a tenant seeded by wkFixture.
func upDBIdentity(tenantID string) auth.Identity {
	return auth.Identity{
		Subject:  uuid.NewString(),
		Role:     "authenticated",
		TenantID: tenantID,
	}
}

// upRealStore is the store seam over the real documents table: PUT into the sink, then INSERT
// the row, in that order. Each call mints a fresh content hash, so nothing here dedupes -- the
// reuse spec below scripts its own seam instead.
func upRealStore(t *testing.T, tenantID string, sink *upSink) func(context.Context, string, string, int64, io.ReadSeeker) (extraction.StoredDocument, error) {
	t.Helper()
	pool := stRequire(t).app
	return func(ctx context.Context, filename, contentType string, size int64, body io.ReadSeeker) (extraction.StoredDocument, error) {
		n, err := io.Copy(io.Discard, body)
		if err != nil {
			return extraction.StoredDocument{}, err
		}
		sink.put()

		hash := strings.Repeat("0", 32) + strings.ReplaceAll(uuid.NewString(), "-", "")
		var id string
		err = db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes, filename, declared_content_type)
				 VALUES ($1, $2, $3, $4, nullif($5, ''), nullif($6, ''))
				 RETURNING id::text`,
				tenantID, "tenants/"+tenantID+"/"+hash, hash, n, filename, contentType).Scan(&id)
		})
		if err != nil {
			return extraction.StoredDocument{}, err
		}
		return extraction.StoredDocument{
			ID: id, Filename: filename, ContentType: contentType, SizeBytes: n,
		}, nil
	}
}

// upRealEnqueue is the enqueue seam over the sanctioned EnqueueExtraction, inside a committing
// tenant transaction -- the shape cmd/submission gets from db.WithinRequestTenantTx.
func upRealEnqueue(t *testing.T, c *queue.Client, tenantID string) func(context.Context, string) (bool, error) {
	t.Helper()
	pool := stRequire(t).app
	return func(ctx context.Context, documentID string) (bool, error) {
		var skipped bool
		err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
			var e error
			skipped, e = extraction.EnqueueExtraction(ctx, tx, c, tenantID, documentID)
			return e
		})
		return skipped, err
	}
}

func upDocumentCount(t *testing.T, ctx context.Context, tenantID string) int {
	t.Helper()
	var n int
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT count(*) FROM documents WHERE tenant_id = $1`, tenantID).Scan(&n); err != nil {
		t.Fatalf("count documents for tenant %s: %v", tenantID, err)
	}
	return n
}

// upServeDB drives the handler once against the injected DB-backed seams.
func upServeDB(
	t *testing.T,
	id auth.Identity,
	store func(context.Context, string, string, int64, io.ReadSeeker) (extraction.StoredDocument, error),
	enqueue func(context.Context, string) (bool, error),
	filename, partContentType string,
	content []byte,
) (int, string) {
	t.Helper()
	body, ct := upBody(t, filename, partContentType, content, nil)
	r := httptest.NewRequest(http.MethodPost, upRoute, body)
	r.Header.Set("Content-Type", ct)
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	extraction.UploadHandler(store, enqueue, nil).ServeHTTP(rec, r)
	return rec.Code, rec.Body.String()
}

// --- AC-2 ------------------------------------------------------------------------------------

// TestRLS_UploadRefusalLeavesNoDocumentRowAndNoObject: the accepted upload is a POSITIVE
// CONTROL, and it is what makes the refusal assertion mean anything -- "both counters
// unchanged" also holds for a handler that never reaches the store on ANY input.
func TestRLS_UploadRefusalLeavesNoDocumentRowAndNoObject(t *testing.T) {
	ctx := t.Context()
	tenantID, _ := wkFixture(t, ctx)
	id := upDBIdentity(tenantID)
	sink := &upSink{}
	store := upRealStore(t, tenantID, sink)
	enqueue := upRealEnqueue(t, wkInsertClient(t), tenantID)

	beforeRows, beforePuts := upDocumentCount(t, ctx, tenantID), sink.count()

	code, body := upServeDB(t, id, store, enqueue, "native_invoice.pdf", "application/pdf", []byte("%PDF-1.7 fake"))
	if code != http.StatusCreated {
		t.Fatalf("control: an accepted pdf returned %d, want 201 (body=%s)", code, body)
	}
	controlRows, controlPuts := upDocumentCount(t, ctx, tenantID), sink.count()
	if controlRows != beforeRows+1 {
		t.Fatalf("control: documents went %d -> %d on an accepted pdf, want +1; the refusal assertions below would pass vacuously", beforeRows, controlRows)
	}
	if controlPuts != beforePuts+1 {
		t.Fatalf("control: object PUTs went %d -> %d on an accepted pdf, want +1; the refusal assertions below would pass vacuously", beforePuts, controlPuts)
	}

	code, body = upServeDB(t, id, store, enqueue, "archive.zip", "application/zip", []byte("PK\x03\x04"))
	if code != http.StatusBadRequest {
		t.Fatalf("a .zip returned %d, want 400 (body=%s)", code, body)
	}
	if got := upDocumentCount(t, ctx, tenantID); got != controlRows {
		t.Errorf("documents = %d after a refused upload, want the control's %d -- a refusal stored bytes", got, controlRows)
	}
	if got := sink.count(); got != controlPuts {
		t.Errorf("object PUTs = %d after a refused upload, want the control's %d -- a refusal PUT an object", got, controlPuts)
	}
}

// --- AC-5 ------------------------------------------------------------------------------------

// TestRLS_UploadTwiceReusesTheDocumentAndEnqueuesOnce: the claim only a database can settle.
// The store seam is SCRIPTED (same id, reused false then true) because the dedupe is
// document.Store.Upsert's and is pinned there; what runs for real here is EnqueueExtraction,
// whose permanent per-document key must leave exactly one river_job behind.
func TestRLS_UploadTwiceReusesTheDocumentAndEnqueuesOnce(t *testing.T) {
	ctx := t.Context()
	tenantID, documentID := wkFixture(t, ctx)
	id := upDBIdentity(tenantID)

	var storeCalls int
	store := func(_ context.Context, filename, contentType string, size int64, body io.ReadSeeker) (extraction.StoredDocument, error) {
		if _, err := io.Copy(io.Discard, body); err != nil {
			return extraction.StoredDocument{}, err
		}
		storeCalls++
		return extraction.StoredDocument{
			ID: documentID, Filename: filename, ContentType: contentType, SizeBytes: size,
			Reused: storeCalls > 1,
		}, nil
	}
	enqueue := upRealEnqueue(t, wkInsertClient(t), tenantID)

	content := []byte("%PDF-1.7 the same bytes twice")
	for i, wantReused := range []bool{false, true} {
		code, body := upServeDB(t, id, store, enqueue, "scan.pdf", "application/pdf", content)
		if code != http.StatusCreated {
			t.Fatalf("upload %d returned %d, want 201 (body=%s)", i+1, code, body)
		}
		if !strings.Contains(body, `"document_id":"`+documentID+`"`) {
			t.Errorf("upload %d body = %s, want it to name document_id %q", i+1, body, documentID)
		}
		if wantReused && !strings.Contains(body, `"reused":true`) {
			t.Errorf("upload %d body = %s, want reused true", i+1, body)
		}
		if !wantReused && !strings.Contains(body, `"reused":false`) {
			t.Errorf("upload %d body = %s, want reused false", i+1, body)
		}
	}
	if storeCalls != 2 {
		t.Fatalf("the store seam ran %d time(s), want 2; the job-count assertion below would not be about a re-upload", storeCalls)
	}

	row := eqOneJob(t, ctx, tenantID)
	if row.argsDocument != documentID {
		t.Errorf("river_job %d carries document_id %q, want %q", row.id, row.argsDocument, documentID)
	}
	if n := eqKeyCount(t, ctx, tenantID); n != 1 {
		t.Errorf("tenant holds %d idempotency_keys row(s) after two uploads of one document, want 1", n)
	}
	if !eqKeyExists(t, ctx, tenantID, eqKey(documentID)) {
		t.Errorf("no idempotency_keys row for %q; the two uploads did not share the per-document key", eqKey(documentID))
	}
}
