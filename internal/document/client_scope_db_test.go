package document_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

type csFixture struct {
	super, app             *pgxpool.Pool
	store                  *document.Store
	tenant                 string
	entA, entB             string
	admin, limited, allCli context.Context
	// docX is linked by A's invoice, docY by B's batch, docZ by nothing, docW by A's batch only.
	docX, docY, docZ, docW string
}

func csExec(t *testing.T, super *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	if _, err := super.Exec(context.Background(), query, args...); err != nil {
		t.Fatalf("seed (%s): %v", query, err)
	}
}

func csMember(t *testing.T, super *pgxpool.Pool, tenant, role string, all bool, clients ...string) context.Context {
	t.Helper()
	user := uuid.NewString()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id, role, status, all_clients) VALUES ($1, $2, $3, 'active', $4)`,
		tenant, user, role, all); err != nil {
		t.Fatalf("seed %s: %v", role, err)
	}
	for _, e := range clients {
		csExec(t, super, `INSERT INTO membership_clients (tenant_id, user_id, entity_id) VALUES ($1, $2, $3)`, tenant, user, e)
	}
	return auth.WithIdentity(context.Background(), auth.Identity{Subject: user, Role: "authenticated", TenantID: tenant})
}

func csDocument(t *testing.T, super *pgxpool.Pool, tenant string) string {
	t.Helper()
	h := hashHex(uuid.NewString())
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes) VALUES ($1, $2, $3, 11) RETURNING id`,
		tenant, document.StorageKey(tenant, h), h).Scan(&id); err != nil {
		t.Fatalf("seed documents: %v", err)
	}
	return id
}

func csEntity(t *testing.T, super *pgxpool.Pool, tenant, name string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO business_entities (tenant_id, name) VALUES ($1, $2) RETURNING id`, tenant, name).Scan(&id); err != nil {
		t.Fatalf("seed business_entities: %v", err)
	}
	return id
}

func newCSFixture(t *testing.T) csFixture {
	t.Helper()
	super, app := dbTestPools(t)
	f := csFixture{super: super, app: app, store: document.NewStore(app)}
	f.tenant = seedTenant(t, super, "doc client scope")
	f.entA = csEntity(t, super, f.tenant, "Client A")
	f.entB = csEntity(t, super, f.tenant, "Client B")
	f.admin = csMember(t, super, f.tenant, "admin", false)
	f.limited = csMember(t, super, f.tenant, "reviewer", false, f.entA)
	f.allCli = csMember(t, super, f.tenant, "preparer", true)

	f.docX, f.docY, f.docZ, f.docW = csDocument(t, super, f.tenant), csDocument(t, super, f.tenant), csDocument(t, super, f.tenant), csDocument(t, super, f.tenant)
	csExec(t, super, `INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, 'CS-X', $3)`, f.tenant, f.entA, f.docX)
	csExec(t, super, `INSERT INTO import_batches (tenant_id, entity_id, document_id, status, rows_total, rows_valid, rows_invalid, errors)
		VALUES ($1, $2, $3, 'completed', 1, 1, 0, '[]'::jsonb)`, f.tenant, f.entB, f.docY)
	csExec(t, super, `INSERT INTO import_batches (tenant_id, entity_id, document_id, status, rows_total, rows_valid, rows_invalid, errors)
		VALUES ($1, $2, $3, 'completed', 1, 0, 1, '[]'::jsonb)`, f.tenant, f.entA, f.docW)
	return f
}

func TestClientScope_LinkedDocumentFollowsTheSet(t *testing.T) {
	f := newCSFixture(t)

	if got, err := f.store.GetLinked(f.limited, f.docX); err != nil || got.ID != f.docX {
		t.Errorf("GetLinked(X, linked by A's invoice) = %q, %v, want found", got.ID, err)
	}
	for name, id := range map[string]string{"Y, linked only by B's batch": f.docY, "Z, unlinked": f.docZ} {
		if _, err := f.store.GetLinked(f.limited, id); !errors.Is(err, document.ErrNotFound) {
			t.Errorf("GetLinked(%s) as a reviewer limited to A: err = %v, want ErrNotFound", name, err)
		}
	}
	if n := auditCount(t, f.app, f.tenant, "document.read"); n != 1 {
		t.Errorf("document.read rows = %d, want 1 (only X)", n)
	}
	if n := auditCitingCount(t, f.app, f.tenant, "document.read", f.docX); n != 1 {
		t.Errorf("document.read rows citing X = %d, want 1", n)
	}
	if _, err := f.store.GetLinked(f.limited, "nope"); !errors.Is(err, document.ErrValidation) {
		t.Errorf("GetLinked(malformed id): err = %v, want ErrValidation", err)
	}
}

func TestClientScope_LinkedThroughABatchOnly(t *testing.T) {
	f := newCSFixture(t)
	if got, err := f.store.GetLinked(f.limited, f.docW); err != nil || got.ID != f.docW {
		t.Errorf("GetLinked(W, linked only by A's quarantined batch) = %q, %v, want found", got.ID, err)
	}
}

func TestClientScope_FullAccessReadsAnUnlinkedUpload(t *testing.T) {
	f := newCSFixture(t)
	for name, ctx := range map[string]context.Context{"admin": f.admin, "All clients preparer": f.allCli} {
		for label, id := range map[string]string{"unlinked": f.docZ, "linked to B": f.docY} {
			if got, err := f.store.GetLinked(ctx, id); err != nil || got.ID != id {
				t.Errorf("GetLinked(%s) as %s = %q, %v, want found", label, name, got.ID, err)
			}
		}
	}
}

func TestClientScope_LinkIsAnyVisibleInvoiceOrBatch(t *testing.T) {
	f := newCSFixture(t)
	both := csDocument(t, f.super, f.tenant)
	csExec(t, f.super, `INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, 'CS-BOTH-A', $3)`, f.tenant, f.entA, both)
	csExec(t, f.super, `INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, 'CS-BOTH-B', $3)`, f.tenant, f.entB, both)
	onlyB := csDocument(t, f.super, f.tenant)
	csExec(t, f.super, `INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, 'CS-ONLY-B', $3)`, f.tenant, f.entB, onlyB)
	bothMixed := csDocument(t, f.super, f.tenant)
	csExec(t, f.super, `INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, 'CS-MIX-B', $3)`, f.tenant, f.entB, bothMixed)
	csExec(t, f.super, `INSERT INTO import_batches (tenant_id, entity_id, document_id, status, rows_total, rows_valid, rows_invalid, errors)
		VALUES ($1, $2, $3, 'completed', 1, 1, 0, '[]'::jsonb)`, f.tenant, f.entA, bothMixed)
	noClients := csMember(t, f.super, f.tenant, "reviewer", false)

	for name, id := range map[string]string{"linked to A's and B's invoice": both, "B's invoice plus A's batch": bothMixed} {
		if got, err := f.store.GetLinked(f.limited, id); err != nil || got.ID != id {
			t.Errorf("GetLinked(%s) as a reviewer limited to A = %q, %v, want found", name, got.ID, err)
		}
	}
	if _, err := f.store.GetLinked(f.limited, onlyB); !errors.Is(err, document.ErrNotFound) {
		t.Errorf("GetLinked(linked only by B's invoice): err = %v, want ErrNotFound", err)
	}
	for name, id := range map[string]string{"X (A's invoice)": f.docX, "W (A's batch)": f.docW, "Z (unlinked)": f.docZ} {
		if _, err := f.store.GetLinked(noClients, id); !errors.Is(err, document.ErrNotFound) {
			t.Errorf("GetLinked(%s) as a reviewer with no clients: err = %v, want ErrNotFound", name, err)
		}
	}
	if _, err := f.store.GetLinked(f.limited, uuid.NewString()); !errors.Is(err, document.ErrNotFound) {
		t.Errorf("GetLinked(unknown id): err = %v, want ErrNotFound", err)
	}
}

func TestClientScope_InHouseMemberReadsAnUnlinkedUpload(t *testing.T) {
	f := newCSFixture(t)
	csExec(t, f.super, `UPDATE tenants SET kind = 'in_house' WHERE id = $1`, f.tenant)
	inHouse := csMember(t, f.super, f.tenant, "reviewer", false)
	if got, err := f.store.GetLinked(inHouse, f.docZ); err != nil || got.ID != f.docZ {
		t.Errorf("GetLinked(unlinked) as an in-house reviewer = %q, %v, want found", got.ID, err)
	}
}

func TestClientScope_DownloadRouteAnswersOneNotFoundAndAuditsOnlyServedReads(t *testing.T) {
	f := newCSFixture(t)
	objects := &fakeObjects{}
	svc := document.NewService(f.store, objects)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/documents/{id}", document.DownloadHandler(svc.OpenLinked, nil))
	get := func(ctx context.Context, id string) *httptest.ResponseRecorder {
		objects.getObject = document.Object{Body: io.NopCloser(strings.NewReader("hello world")), Size: 11}
		r := httptest.NewRequest("GET", "/v1/documents/"+id, nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec
	}

	var refused []string
	for name, id := range map[string]string{"Y, B's batch": f.docY, "Z, unlinked": f.docZ, "unknown": uuid.NewString()} {
		rec := get(f.limited, id)
		if rec.Code != http.StatusNotFound {
			t.Errorf("limited reviewer, %s: status = %d, want 404", name, rec.Code)
		}
		refused = append(refused, rec.Body.String())
	}
	if len(refused) != 3 || refused[0] != refused[1] || refused[1] != refused[2] || !strings.Contains(refused[0], "not found") {
		t.Errorf("refusal bodies = %q, want one identical not-found body", refused)
	}
	if n := len(objects.getKeys); n != 0 {
		t.Errorf("object-store reads for refused ids = %d, want 0", n)
	}
	if n := auditCount(t, f.app, f.tenant, "document.read"); n != 0 {
		t.Errorf("document.read rows after refusals = %d, want 0", n)
	}
	if rec := get(f.limited, "nope"); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed id: status = %d, want 400", rec.Code)
	}

	for name, id := range map[string]string{"X, A's invoice": f.docX, "W, A's batch": f.docW} {
		if rec := get(f.limited, id); rec.Code != http.StatusOK || rec.Body.String() != "hello world" {
			t.Errorf("limited reviewer, %s: %d %q, want 200 with the bytes", name, rec.Code, rec.Body.String())
		}
	}
	for name, ctx := range map[string]context.Context{"admin": f.admin, "All clients": f.allCli} {
		if rec := get(ctx, f.docZ); rec.Code != http.StatusOK {
			t.Errorf("%s reading the unlinked upload: status = %d, want 200", name, rec.Code)
		}
	}
	if n := auditCount(t, f.app, f.tenant, "document.read"); n != 4 {
		t.Errorf("document.read rows = %d, want 4 (two limited reads, two full-access reads)", n)
	}
}
