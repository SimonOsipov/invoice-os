package main

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

// Skips unless DATABASE_URL and DATABASE_SUPERUSER_URL are set; the CI go job sets neither.
func TestNewExtractionEnqueuer_JobContinuesTheUploadRequestTrace(t *testing.T) {
	appURL, superURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("set DATABASE_URL and DATABASE_SUPERUSER_URL")
	}
	ctx := context.Background()
	app, rec, _ := sentrytest.Boot(t, "submission")
	pool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	t.Cleanup(pool.Close)
	super, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	t.Cleanup(super.Close)
	q, err := queue.New(pool, queue.Config{})
	if err != nil {
		t.Fatalf("build insert-only queue client: %v", err)
	}

	tenantID, documentID := uuid.NewString(), uuid.NewString()
	if _, err := super.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'enqueue trace')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		for _, stmt := range []string{
			`DELETE FROM river_job WHERE args->>'tenant_id' = $1`,
			`DELETE FROM idempotency_keys WHERE tenant_id = $1`,
			`DELETE FROM tenants WHERE id = $1`,
		} {
			if _, err := super.Exec(context.Background(), stmt, tenantID); err != nil {
				t.Errorf("%s: %v", stmt, err)
			}
		}
	})

	caller := auth.Identity{Subject: "enqueue-trace-caller", Role: "authenticated", TenantID: tenantID}
	store := func(_ context.Context, filename, contentType string, size int64, _ io.ReadSeeker) (extraction.StoredDocument, error) {
		return extraction.StoredDocument{ID: documentID, Filename: filename, ContentType: contentType, SizeBytes: size}, nil
	}
	h := extraction.UploadHandler(store, newExtractionEnqueuer(pool, q), nil)
	app.Mux.HandleFunc("POST /v1/documents", func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), caller)))
	})

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "scan.pdf")
	_, _ = fw.Write([]byte("%PDF-1.7 fake"))
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/v1/documents", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /v1/documents = %d: %s", w.Code, w.Body)
	}

	var carried string
	if err := super.QueryRow(ctx,
		`SELECT coalesce(metadata->>'sentry_trace', '') FROM river_job WHERE args->>'tenant_id' = $1`,
		tenantID).Scan(&carried); err != nil {
		t.Fatalf("read the enqueued job's metadata: %v", err)
	}
	txs := rec.Transactions()
	if len(txs) != 1 || txs[0].Transaction != "POST /v1/documents" {
		t.Fatalf("recorded %d transactions, want the one upload request", len(txs))
	}
	trace := txs[0].Contexts["trace"]
	traceID, _ := trace["trace_id"].(string)
	spanID, _ := trace["span_id"].(string)
	if traceID == "" || spanID == "" {
		t.Fatalf("request transaction has no trace context: %v", trace)
	}
	if want := traceID + "-" + spanID + "-1"; carried != want {
		t.Errorf("job metadata sentry_trace = %q, want the request transaction's %q", carried, want)
	}
}
