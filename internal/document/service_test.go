// service_test.go: the Service.Store specs — bytes first, then the row.
// DB-backed as invoice_app (same harness as document_test.go) with an in-memory
// ObjectStore, because every ordering claim here is a claim about what the
// documents table holds at the moment the PUT happens.
package document_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/document"
)

// --- fakeObjects ------------------------------------------------------------

// fakeObjects is the in-memory ObjectStore: call order, keys, byte-exact bodies,
// injectable failures.
type fakeObjects struct {
	calls     []string // "put:<key>" / "get:<key>", in order
	putKeys   []string
	putSizes  []int64
	putBodies [][]byte
	putErr    error

	getKeys   []string
	getRanges []string
	getObject document.Object
	getErr    error

	// onPut runs after the body is captured and before putErr is returned — the
	// only place a test can observe the database between the PUT and the INSERT.
	onPut func()
}

// Put reads from the reader's CURRENT offset, exactly like the real client: a
// caller that hashed first and did not rewind records zero bytes here.
func (f *fakeObjects) Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error {
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f.calls = append(f.calls, "put:"+key)
	f.putKeys = append(f.putKeys, key)
	f.putSizes = append(f.putSizes, size)
	f.putBodies = append(f.putBodies, b)
	if f.onPut != nil {
		f.onPut()
	}
	return f.putErr
}

func (f *fakeObjects) Get(ctx context.Context, key, rangeHeader string) (document.Object, error) {
	f.calls = append(f.calls, "get:"+key)
	f.getKeys = append(f.getKeys, key)
	f.getRanges = append(f.getRanges, rangeHeader)
	return f.getObject, f.getErr
}

func (f *fakeObjects) distinctPutKeys() []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range f.putKeys {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

var errPutBoom = errors.New("object storage unreachable")

func tenantDocCount(t *testing.T, super *pgxpool.Pool, tenantID string) int {
	t.Helper()
	return mustCount(t, super, `SELECT count(*) FROM documents WHERE tenant_id = $1`, tenantID)
}

// --- AC-1: bytes first, then the row ---------------------------------------

func TestServiceStore_PutPrecedesRowInsert(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "svc put-precedes-insert")
	c := identity(ctx, tenantID, memberSubject)

	body := []byte("hello world")
	rowsAtPut := -1
	objs := &fakeObjects{}
	objs.onPut = func() { rowsAtPut = tenantDocCount(t, super, tenantID) }
	svc := document.NewService(document.NewStore(app), objs)

	doc, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if doc.ID == "" {
		t.Fatal("Store returned an empty id")
	}
	if len(objs.putKeys) != 1 {
		t.Fatalf("PUTs recorded = %d, want 1", len(objs.putKeys))
	}
	if want := []string{"put:" + doc.StorageKey}; !slices.Equal(objs.calls, want) {
		t.Errorf("object-store calls = %v, want %v", objs.calls, want)
	}
	if rowsAtPut != 0 {
		t.Errorf("documents rows at the moment of the PUT = %d, want 0 (the object is written first)", rowsAtPut)
	}
	if n := tenantDocCount(t, super, tenantID); n != 1 {
		t.Errorf("documents rows after Store = %d, want 1", n)
	}
}

func TestServiceStore_PutFailureWritesNoRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "svc put-failure")
	c := identity(ctx, tenantID, memberSubject)

	body := []byte("hello world")
	objs := &fakeObjects{putErr: errPutBoom}
	svc := document.NewService(document.NewStore(app), objs)

	doc, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body))
	if !errors.Is(err, errPutBoom) {
		t.Fatalf("Store with a failing PUT = %v, want the PUT error (return it, or wrap it with %%w)", err)
	}
	if doc.ID != "" {
		t.Errorf("Store returned id %q after a failed PUT, want the zero value", doc.ID)
	}
	if len(objs.putKeys) != 1 {
		t.Errorf("PUTs recorded = %d, want 1 (the error must come from the PUT, not from before it)", len(objs.putKeys))
	}
	if n := tenantDocCount(t, super, tenantID); n != 0 {
		t.Errorf("documents rows after a failed PUT = %d, want 0 — nothing downstream may believe a document exists", n)
	}
}

func TestServiceStore_ComputesHashAndSizeServerSide(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "svc server-side hash")
	c := identity(ctx, tenantID, memberSubject)

	body := []byte("hello world") // 11 bytes
	const wantHash = "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"

	objs := &fakeObjects{}
	svc := document.NewService(document.NewStore(app), objs)

	// 999 is the caller-declared size; it must not survive.
	doc, _, err := svc.Store(c, "a.csv", "text/csv", 999, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	var gotHash string
	var gotSize int64
	if err := super.QueryRow(ctx,
		`SELECT content_hash, size_bytes FROM documents WHERE id = $1`, doc.ID,
	).Scan(&gotHash, &gotSize); err != nil {
		t.Fatalf("read back the stored row: %v", err)
	}
	if gotHash != wantHash {
		t.Errorf("documents.content_hash = %q, want the sha256 of the bytes %q", gotHash, wantHash)
	}
	if gotSize != 11 {
		t.Errorf("documents.size_bytes = %d, want 11 (the byte count, not the declared 999)", gotSize)
	}
	if doc.ContentHash != wantHash {
		t.Errorf("returned Document.ContentHash = %q, want %q", doc.ContentHash, wantHash)
	}
	if doc.SizeBytes != 11 {
		t.Errorf("returned Document.SizeBytes = %d, want 11", doc.SizeBytes)
	}
	if len(objs.putSizes) != 1 || objs.putSizes[0] != 11 {
		t.Errorf("PUT declared size = %v, want [11] — a declared 999 against 11 bytes is a Content-Length mismatch on the wire", objs.putSizes)
	}
}

// Put transmits from the reader's current offset, and the hash pass leaves it at
// EOF: without the Seek(0, io.SeekStart) between them the PUT sends nothing.
func TestServiceStore_RewindsBodyBeforePut(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "svc rewind")
	c := identity(ctx, tenantID, memberSubject)

	body := []byte("hello world")
	objs := &fakeObjects{}
	svc := document.NewService(document.NewStore(app), objs)

	if _, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body)); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if len(objs.putBodies) != 1 {
		t.Fatalf("PUTs recorded = %d, want 1", len(objs.putBodies))
	}
	if !bytes.Equal(objs.putBodies[0], body) {
		t.Errorf("the object store received %d bytes (%q), want all %d (%q)",
			len(objs.putBodies[0]), objs.putBodies[0], len(body), body)
	}
}

// The row INSERT is forced to fail on documents' FK to tenants (23503), which
// audit_log has no counterpart of — so this is not the audit-rollback case.
func TestServiceStore_UpsertFailureAfterSuccessfulPutReturnsErrorNoID(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString() // deliberately never seeded into tenants
	c := identity(ctx, tenantID, memberSubject)

	body := []byte("hello world")
	objs := &fakeObjects{}
	svc := document.NewService(document.NewStore(app), objs)

	doc, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body))
	if err == nil {
		t.Fatal("Store under a nonexistent tenant succeeded, want the documents FK violation")
	}
	if doc.ID != "" {
		t.Errorf("Store returned id %q after a failed row write, want the zero value", doc.ID)
	}
	if len(objs.putKeys) != 1 {
		t.Fatalf("PUTs recorded = %d, want 1 (the PUT must have succeeded before the row failed)", len(objs.putKeys))
	}
	if n := tenantDocCount(t, super, tenantID); n != 0 {
		t.Errorf("documents rows after a failed row write = %d, want 0", n)
	}
	// The object is left in place under its content-addressed key: an orphan is
	// the accepted cost of bytes-first.
	if want := document.StorageKey(tenantID, hashHex("hello world")); objs.putKeys[0] != want {
		t.Errorf("PUT key = %q, want %q", objs.putKeys[0], want)
	}
}

func TestServiceStore_RetryAfterUpsertFailureSucceedsWithNoDuplicateObject(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString() // not seeded yet — the first attempt must fail
	c := identity(ctx, tenantID, memberSubject)

	body := []byte("hello world")
	objs := &fakeObjects{}
	svc := document.NewService(document.NewStore(app), objs)

	if _, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body)); err == nil {
		t.Fatal("first Store under a nonexistent tenant succeeded, want the documents FK violation")
	}

	seedTenantWithID(t, super, tenantID, "svc retry after failure")

	doc, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("retried Store: %v", err)
	}
	if doc.ID == "" {
		t.Fatal("retried Store returned an empty id")
	}
	if n := tenantDocCount(t, super, tenantID); n != 1 {
		t.Errorf("documents rows after the retry = %d, want exactly 1", n)
	}
	if len(objs.putKeys) != 2 {
		t.Fatalf("PUTs recorded = %d, want 2 (one per attempt)", len(objs.putKeys))
	}
	if keys := objs.distinctPutKeys(); len(keys) != 1 {
		t.Errorf("distinct PUT keys = %v, want 1 — a content-addressed retry rewrites the same object", keys)
	}
	if objs.putKeys[1] != doc.StorageKey {
		t.Errorf("retried PUT key = %q, want the stored row's storage_key %q", objs.putKeys[1], doc.StorageKey)
	}
}

// --- AC-2: per-tenant dedupe ------------------------------------------------

func TestServiceStore_IdenticalBytesSameTenantDedupes(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "svc same-tenant dedupe")
	c := identity(ctx, tenantID, memberSubject)

	body := []byte("hello world")
	objs := &fakeObjects{}
	svc := document.NewService(document.NewStore(app), objs)

	first, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("first Store: %v", err)
	}
	second, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("second Store: %v", err)
	}

	if second.ID != first.ID {
		t.Errorf("second Store id = %q, want the first's %q", second.ID, first.ID)
	}
	if n := tenantDocCount(t, super, tenantID); n != 1 {
		t.Errorf("documents rows after two identical stores = %d, want 1", n)
	}
	if keys := objs.distinctPutKeys(); len(keys) != 1 {
		t.Errorf("distinct PUT keys = %v, want 1", keys)
	}
	// The audit trail, independently of Store's reuse flag (service_reuse_value_test.go).
	if n := auditCount(t, app, tenantID, "document.created"); n != 1 {
		t.Errorf("document.created audit rows = %d, want 1", n)
	}
	if n := auditCount(t, app, tenantID, "document.reused"); n != 1 {
		t.Errorf("document.reused audit rows = %d, want 1 (the second store is a dedupe, not a create)", n)
	}
}

// The ON CONFLICT DO NOTHING RETURNING yields zero rows on conflict; without the
// fallback SELECT the second call hands back a zero-value Document.
func TestServiceStore_DedupeReturnsExistingRowNotZeroValue(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "svc dedupe returns row")
	c := identity(ctx, tenantID, memberSubject)

	body := []byte("hello world")
	svc := document.NewService(document.NewStore(app), &fakeObjects{})

	first, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("first Store: %v", err)
	}
	if first.CreatedAt.IsZero() || first.StorageKey == "" {
		t.Fatalf("first Store returned an underpopulated Document (created_at %v, storage_key %q)", first.CreatedAt, first.StorageKey)
	}

	second, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("second Store: %v", err)
	}
	if second.CreatedAt.IsZero() {
		t.Error("second Store returned a zero CreatedAt — the fallback SELECT after DO NOTHING RETURNING is missing")
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("second Store CreatedAt = %v, want the first row's %v", second.CreatedAt, first.CreatedAt)
	}
	if second.StorageKey != first.StorageKey {
		t.Errorf("second Store StorageKey = %q, want the first row's %q", second.StorageKey, first.StorageKey)
	}
	if second.ContentHash != first.ContentHash || second.SizeBytes != first.SizeBytes {
		t.Errorf("second Store = (hash %q, size %d), want the first row's (%q, %d)",
			second.ContentHash, second.SizeBytes, first.ContentHash, first.SizeBytes)
	}
}

// --- AC-3: the same bytes under two tenants are two documents ---------------

func TestServiceStore_IdenticalBytesDifferentTenantsDoNotDedupe(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "svc cross-tenant no-dedupe A")
	tenantB := seedTenant(t, super, "svc cross-tenant no-dedupe B")
	cA := identity(ctx, tenantA, memberSubject)
	cB := identity(ctx, tenantB, memberSubject)

	body := []byte("hello world")
	objs := &fakeObjects{}
	svc := document.NewService(document.NewStore(app), objs)

	docA, _, err := svc.Store(cA, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Store as A: %v", err)
	}
	docB, _, err := svc.Store(cB, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Store as B: %v", err)
	}

	if docA.ID == docB.ID {
		t.Errorf("both tenants got document id %q, want two distinct documents", docA.ID)
	}
	if n := tenantDocCount(t, super, tenantA); n != 1 {
		t.Errorf("tenant A documents rows = %d, want 1", n)
	}
	if n := tenantDocCount(t, super, tenantB); n != 1 {
		t.Errorf("tenant B documents rows = %d, want 1", n)
	}
	if docA.StorageKey == docB.StorageKey {
		t.Errorf("both tenants share storage key %q, want two distinct keys", docA.StorageKey)
	}
	if keys := objs.distinctPutKeys(); len(keys) != 2 {
		t.Errorf("distinct PUT keys = %v, want 2", keys)
	}
	if docA.StorageKey != document.StorageKey(tenantA, docA.ContentHash) {
		t.Errorf("tenant A storage key = %q, want %q", docA.StorageKey, document.StorageKey(tenantA, docA.ContentHash))
	}
	if docB.StorageKey != document.StorageKey(tenantB, docB.ContentHash) {
		t.Errorf("tenant B storage key = %q, want %q", docB.StorageKey, document.StorageKey(tenantB, docB.ContentHash))
	}
}

// --- structural pins --------------------------------------------------------

// Upsert returns the Document, not just an id: the dedupe path has to carry the
// FIRST row's created_at. Service.Store keeps its declared-size parameter, which
// is what ComputesHashAndSizeServerSide overrides.
var (
	_ document.ObjectStore                                                                                            = (*fakeObjects)(nil)
	_ func(*pgxpool.Pool) *document.Store                                                                             = document.NewStore
	_ func(*document.Store, document.ObjectStore) *document.Service                                                   = document.NewService
	_ func(*document.Store, context.Context, document.Document) (document.Document, bool, error)                      = (*document.Store).Upsert
	_ func(*document.Store, context.Context, string) (document.Document, error)                                       = (*document.Store).Get
	_ func(*document.Service, context.Context, string, string, int64, io.ReadSeeker) (document.Document, bool, error) = (*document.Service).Store
	_ func(*document.Service, context.Context, string, string) (document.Document, document.Object, error)            = (*document.Service).Open
)
