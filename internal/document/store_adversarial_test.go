// store_adversarial_test.go: QA Mode B coverage beyond the AC specs — the NULL
// binding and store-time filename coercion neither AC names, Service.Open (which
// DOC-01-05 never exercises, since its handler takes an injected open func), and
// the edges a hostile caller reaches first.
package document_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func ptr(s string) *string { return &s }

// nullableCols reads filename and declared_content_type as *string through the
// superuser pool, so a SQL NULL is distinguishable from the empty string.
func nullableCols(t *testing.T, super *pgxpool.Pool, id string) (filename, contentType *string) {
	t.Helper()
	if err := super.QueryRow(context.Background(),
		`SELECT filename, declared_content_type FROM documents WHERE id = $1`, id,
	).Scan(&filename, &contentType); err != nil {
		t.Fatalf("read back nullable columns for %s: %v", id, err)
	}
	return filename, contentType
}

// auditPayload returns the newest payload text for tenantID+event.
func auditPayload(t *testing.T, pool *pgxpool.Pool, tenantID, event string) string {
	t.Helper()
	ctx := context.Background()
	var payload string
	if err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT payload::text FROM audit_log WHERE event = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, event,
		).Scan(&payload)
	}); err != nil {
		t.Fatalf("read audit_log payload for %s: %v", event, err)
	}
	return payload
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// --- C5: nullif on both nullable columns ------------------------------------

// An empty value must reach the column as SQL NULL: the empty string would make
// an unrecorded name indistinguishable from a file genuinely named nothing.
func TestStoreUpsert_EmptyNullableValuesPersistAsNULL(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := document.NewStore(app)

	tenantID := seedTenant(t, super, "doc nullif")
	c := identity(ctx, tenantID, memberSubject)

	// Positive half first: a real value survives verbatim, so a NULL below is the
	// nullif and not a binding that drops both columns.
	kept := docFixture(tenantID, "nullif-kept", 11)
	kept.Filename, kept.DeclaredContentType = ptr("kept.csv"), ptr("text/csv")
	storedKept, _, err := store.Upsert(c, kept)
	if err != nil {
		t.Fatalf("Upsert with populated columns: %v", err)
	}
	name, ct := nullableCols(t, super, storedKept.ID)
	if name == nil || *name != "kept.csv" {
		t.Errorf("filename = %v, want %q", name, "kept.csv")
	}
	if ct == nil || *ct != "text/csv" {
		t.Errorf("declared_content_type = %v, want %q", ct, "text/csv")
	}

	empty := docFixture(tenantID, "nullif-empty", 11)
	empty.Filename, empty.DeclaredContentType = ptr(""), ptr("")
	storedEmpty, _, err := store.Upsert(c, empty)
	if err != nil {
		t.Fatalf("Upsert with empty-string columns: %v", err)
	}
	name, ct = nullableCols(t, super, storedEmpty.ID)
	if name != nil {
		t.Errorf("filename after an empty-string Upsert = %q, want SQL NULL (nullif($n, '') is missing)", *name)
	}
	if ct != nil {
		t.Errorf("declared_content_type after an empty-string Upsert = %q, want SQL NULL (nullif($n, '') is missing)", *ct)
	}
	if storedEmpty.Filename != nil || storedEmpty.DeclaredContentType != nil {
		t.Errorf("returned Document = (filename %v, content type %v), want both nil",
			storedEmpty.Filename, storedEmpty.DeclaredContentType)
	}
}

func TestStoreUpsert_NilNullableValuesPersistAsNULL(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := document.NewStore(app)

	tenantID := seedTenant(t, super, "doc nil nullable")
	c := identity(ctx, tenantID, memberSubject)

	in := docFixture(tenantID, "nil-nullable", 11)
	in.Filename, in.DeclaredContentType = nil, nil
	stored, _, err := store.Upsert(c, in)
	if err != nil {
		t.Fatalf("Upsert with nil columns: %v", err)
	}
	name, ct := nullableCols(t, super, stored.ID)
	if name != nil || ct != nil {
		t.Errorf("nil columns persisted as (%v, %v), want two SQL NULLs", name, ct)
	}
}

// --- C6: Service.Store coerces the filename itself --------------------------

// DOC-01-05's Content-Disposition asserts the name is already coerced at store
// time and DOC-01-06's preview flow never sanitizes, so the coercion has to
// happen here.
func TestServiceStore_SanitizesFilenameAtStoreTime(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "svc sanitize filename")
	c := identity(ctx, tenantID, memberSubject)

	body := []byte("hello world")
	svc := document.NewService(document.NewStore(app), &fakeObjects{})

	// Path segments, a Windows path, a NUL (22021 on insert) and a newline.
	const raw = "../../etc/pa\x00ss\nwd.csv"
	doc, _, err := svc.Store(c, raw, "text/csv", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Store with a hostile filename: %v", err)
	}

	name, _ := nullableCols(t, super, doc.ID)
	if name == nil {
		t.Fatal("filename persisted as NULL, want the sanitized name")
	}
	if *name != "passwd.csv" {
		t.Errorf("documents.filename = %q, want %q — Service.Store must call SanitizeFilename itself", *name, "passwd.csv")
	}
	if doc.Filename == nil || *doc.Filename != "passwd.csv" {
		t.Errorf("returned Document.Filename = %v, want %q", doc.Filename, "passwd.csv")
	}
}

func TestServiceStore_FilenameSanitizingToEmptyPersistsAsNULL(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "svc filename to empty")
	c := identity(ctx, tenantID, memberSubject)

	body := []byte("hello world")
	svc := document.NewService(document.NewStore(app), &fakeObjects{})

	doc, _, err := svc.Store(c, "\x01\x02  \x7f", "", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Store with a filename that sanitizes to empty: %v", err)
	}
	name, ct := nullableCols(t, super, doc.ID)
	if name != nil {
		t.Errorf("documents.filename = %q, want SQL NULL — an unusable name is not a name", *name)
	}
	if ct != nil {
		t.Errorf("documents.declared_content_type = %q, want SQL NULL for an empty declared type", *ct)
	}
}

// --- identity is checked before object storage is touched -------------------

// An unauthenticated caller must not reach the bucket at all: without the guard
// the tenant segment is empty and the bytes land under tenants//<hash>.
func TestServiceStore_NoIdentityTouchesNoObjectStore(t *testing.T) {
	_, app := dbTestPools(t)

	objs := &fakeObjects{}
	svc := document.NewService(document.NewStore(app), objs)

	body := []byte("hello world")
	doc, _, err := svc.Store(context.Background(), "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body))
	if !errors.Is(err, db.ErrNoTenant) {
		t.Fatalf("Store with no identity in context = %v, want db.ErrNoTenant", err)
	}
	if doc.ID != "" {
		t.Errorf("Store returned id %q with no identity, want the zero value", doc.ID)
	}
	if len(objs.calls) != 0 {
		t.Errorf("object-store calls with no identity = %v, want none — the guard must precede the PUT", objs.calls)
	}
}

// --- byte-shape edges -------------------------------------------------------

// size_bytes CHECKs >= 0, so an empty upload is a legal document, not an error.
func TestServiceStore_ZeroByteDocumentIsStored(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "svc zero byte")
	c := identity(ctx, tenantID, memberSubject)

	objs := &fakeObjects{}
	svc := document.NewService(document.NewStore(app), objs)

	doc, _, err := svc.Store(c, "empty.csv", "text/csv", 0, bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("Store of a zero-byte body: %v", err)
	}
	if doc.SizeBytes != 0 {
		t.Errorf("Document.SizeBytes = %d, want 0", doc.SizeBytes)
	}
	if want := sha256Hex(nil); doc.ContentHash != want {
		t.Errorf("Document.ContentHash = %q, want the sha256 of the empty string %q", doc.ContentHash, want)
	}
	if len(objs.putSizes) != 1 || objs.putSizes[0] != 0 {
		t.Errorf("PUT declared sizes = %v, want [0]", objs.putSizes)
	}
	if len(objs.putBodies) != 1 || len(objs.putBodies[0]) != 0 {
		t.Errorf("PUT body length = %d, want 0", len(objs.putBodies[0]))
	}
	if n := tenantDocCount(t, super, tenantID); n != 1 {
		t.Errorf("documents rows = %d, want 1", n)
	}
}

// The declared size is overridden in both directions — under-reporting would put
// a short Content-Length against a longer body on the wire.
func TestServiceStore_UnderDeclaredSizeIsOverridden(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "svc under-declared size")
	c := identity(ctx, tenantID, memberSubject)

	objs := &fakeObjects{}
	svc := document.NewService(document.NewStore(app), objs)

	doc, _, err := svc.Store(c, "a.csv", "text/csv", 3, bytes.NewReader([]byte("hello world")))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if doc.SizeBytes != 11 {
		t.Errorf("Document.SizeBytes = %d, want 11 (not the declared 3)", doc.SizeBytes)
	}
	if len(objs.putSizes) != 1 || objs.putSizes[0] != 11 {
		t.Errorf("PUT declared size = %v, want [11] — 3 against 11 bytes is a Content-Length mismatch", objs.putSizes)
	}
	var gotSize int64
	if err := super.QueryRow(ctx, `SELECT size_bytes FROM documents WHERE id = $1`, doc.ID).Scan(&gotSize); err != nil {
		t.Fatalf("read back size_bytes: %v", err)
	}
	if gotSize != 11 {
		t.Errorf("documents.size_bytes = %d, want 11", gotSize)
	}
}

// --- the trail ---------------------------------------------------------------

// Every write event names the row it is about, and the dedupe names the FIRST
// row — a reused event citing a fresh id would be a trail that points nowhere.
func TestServiceStore_AuditPayloadsCiteTheDocumentID(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "svc audit payload")
	c := identity(ctx, tenantID, memberSubject)

	body := []byte("hello world")
	svc := document.NewService(document.NewStore(app), &fakeObjects{})

	first, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("first Store: %v", err)
	}
	if got := auditPayload(t, app, tenantID, "document.created"); !strings.Contains(got, first.ID) {
		t.Errorf("document.created payload = %s, want it to cite %s", got, first.ID)
	}

	if _, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body)); err != nil {
		t.Fatalf("second Store: %v", err)
	}
	if got := auditPayload(t, app, tenantID, "document.reused"); !strings.Contains(got, first.ID) {
		t.Errorf("document.reused payload = %s, want it to cite the FIRST row's id %s", got, first.ID)
	}
	if n := auditCitingCount(t, app, tenantID, "document.created", first.ID); n != 1 {
		t.Errorf("document.created rows citing %s = %d, want 1", first.ID, n)
	}
}

// Storing is not reading: a dedupe resolves an existing row through the write
// path and must not put a read in the trail.
func TestServiceStore_DedupeWritesNoReadAudit(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "svc dedupe no read audit")
	c := identity(ctx, tenantID, memberSubject)

	body := []byte("hello world")
	svc := document.NewService(document.NewStore(app), &fakeObjects{})

	for i := range 2 {
		if _, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body)); err != nil {
			t.Fatalf("Store #%d: %v", i+1, err)
		}
	}
	if n := auditCount(t, app, tenantID, "document.read"); n != 0 {
		t.Errorf("document.read audit rows after two stores = %d, want 0", n)
	}
	if n := auditCount(t, app, tenantID, "document.reused"); n != 1 {
		t.Errorf("document.reused audit rows = %d, want 1", n)
	}
}

// --- failure and concurrency edges ------------------------------------------

// Cancelling between the PUT and the row write must leave nothing behind: the tx
// never begins, so there is no row, no audit row and no half-written state.
func TestServiceStore_CancellationMidStoreCommitsNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tenantID := seedTenant(t, super, "svc cancel mid-store")
	c := identity(ctx, tenantID, memberSubject)

	objs := &fakeObjects{}
	objs.onPut = cancel // cancel after the bytes are in, before the row
	svc := document.NewService(document.NewStore(app), objs)

	body := []byte("hello world")
	doc, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Store cancelled after the PUT = %v, want a context.Canceled", err)
	}
	if doc.ID != "" {
		t.Errorf("Store returned id %q after cancellation, want the zero value", doc.ID)
	}
	if n := tenantDocCount(t, super, tenantID); n != 0 {
		t.Errorf("documents rows after cancellation = %d, want 0", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM audit_log WHERE tenant_id = $1`, tenantID); n != 0 {
		t.Errorf("audit_log rows after cancellation = %d, want 0", n)
	}
}

// Two concurrent stores of identical bytes by one tenant. ON CONFLICT DO NOTHING
// waits on the uncommitted conflicting insert, so the loser's fallback SELECT —
// a fresh READ COMMITTED snapshot — resolves the winner's row rather than
// erroring.
func TestServiceStore_ConcurrentIdenticalStoresYieldOneRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "svc concurrent identical")
	c := identity(ctx, tenantID, memberSubject)

	body := []byte("hello world")

	// Both goroutines park in onPut until the other arrives, so the two INSERTs
	// genuinely overlap instead of serialising by luck.
	var barrier sync.WaitGroup
	barrier.Add(2)
	release := func() {
		barrier.Done()
		barrier.Wait()
	}

	type result struct {
		doc document.Document
		err error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			objs := &fakeObjects{onPut: release} // per-goroutine: the fake is not concurrency safe
			svc := document.NewService(document.NewStore(app), objs)
			doc, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body))
			results[i] = result{doc, err}
		}()
	}
	wg.Wait()

	for i, r := range results {
		if r.err != nil {
			t.Fatalf("concurrent Store #%d failed: %v — identical concurrent stores must dedupe, not error", i+1, r.err)
		}
		if r.doc.ID == "" {
			t.Fatalf("concurrent Store #%d returned an empty id", i+1)
		}
	}
	if results[0].doc.ID != results[1].doc.ID {
		t.Errorf("concurrent stores returned ids %q and %q, want the same row", results[0].doc.ID, results[1].doc.ID)
	}
	if n := tenantDocCount(t, super, tenantID); n != 1 {
		t.Errorf("documents rows after two concurrent identical stores = %d, want exactly 1", n)
	}
	if n := auditCount(t, app, tenantID, "document.created"); n != 1 {
		t.Errorf("document.created audit rows = %d, want 1", n)
	}
	if n := auditCount(t, app, tenantID, "document.reused"); n != 1 {
		t.Errorf("document.reused audit rows = %d, want 1 (the loser reuses, it does not fail)", n)
	}
}

// Bytes-first's whole point: a committed row always has an object behind it.
// Swept across a create, a dedupe and a PUT failure in one tenant.
func TestServiceStore_EveryCommittedRowHasAStoredObject(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "svc no dangling pointer")
	c := identity(ctx, tenantID, memberSubject)

	objs := &fakeObjects{}
	svc := document.NewService(document.NewStore(app), objs)

	stored := []byte("hello world")
	if _, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(stored)), bytes.NewReader(stored)); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(stored)), bytes.NewReader(stored)); err != nil {
		t.Fatalf("dedupe: %v", err)
	}
	objs.putErr = errPutBoom
	refused := []byte("never stored")
	if _, _, err := svc.Store(c, "b.csv", "text/csv", int64(len(refused)), bytes.NewReader(refused)); !errors.Is(err, errPutBoom) {
		t.Fatalf("failing PUT = %v, want errPutBoom", err)
	}

	put := map[string]bool{}
	for i, k := range objs.putKeys {
		// The failed attempt's key was recorded but its bytes were refused.
		if i < 2 {
			put[k] = true
		}
	}
	rows, err := super.Query(ctx, `SELECT id, storage_key FROM documents WHERE tenant_id = $1`, tenantID)
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	defer rows.Close()
	var n int
	for rows.Next() {
		var id, key string
		if err := rows.Scan(&id, &key); err != nil {
			t.Fatalf("scan: %v", err)
		}
		n++
		if !put[key] {
			t.Errorf("document %s points at storage_key %q, which was never PUT successfully — a dangling pointer", id, key)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate documents: %v", err)
	}
	if n != 1 {
		t.Errorf("documents rows = %d, want 1 (create, then a dedupe, then a refused PUT)", n)
	}
}

// --- Service.Open ------------------------------------------------------------
//
// DOC-01-05's DownloadHandler takes an injected open func, so nothing downstream
// exercises Service.Open itself.

func TestServiceOpen_FetchesTheStoredKeyAndForwardsRange(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "svc open happy")
	c := identity(ctx, tenantID, memberSubject)

	body := []byte("hello world")
	objs := &fakeObjects{getObject: document.Object{
		Body: io.NopCloser(strings.NewReader("hello worl")), Size: 10,
		ContentRange: "bytes 0-9/11", Partial: true,
	}}
	svc := document.NewService(document.NewStore(app), objs)

	stored, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	doc, obj, err := svc.Open(c, stored.ID, "bytes=0-9")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if doc.ID != stored.ID || doc.StorageKey != stored.StorageKey {
		t.Errorf("Open returned document (%q, %q), want (%q, %q)", doc.ID, doc.StorageKey, stored.ID, stored.StorageKey)
	}
	if want := []string{stored.StorageKey}; !equalStrings(objs.getKeys, want) {
		t.Errorf("object-store GET keys = %v, want %v — the key comes off the row, never from the caller", objs.getKeys, want)
	}
	if want := []string{"bytes=0-9"}; !equalStrings(objs.getRanges, want) {
		t.Errorf("forwarded ranges = %v, want %v (verbatim, unparsed)", objs.getRanges, want)
	}
	if !obj.Partial || obj.ContentRange != "bytes 0-9/11" || obj.Size != 10 {
		t.Errorf("Open returned object (partial %v, range %q, size %d), want the store's (true, %q, 10)",
			obj.Partial, obj.ContentRange, obj.Size, "bytes 0-9/11")
	}
	if n := auditCount(t, app, tenantID, "document.read"); n != 1 {
		t.Errorf("document.read audit rows after one Open = %d, want 1", n)
	}
}

// A row the caller cannot see must be refused before any byte is fetched —
// otherwise the object store becomes the cross-tenant oracle the row lookup is
// there to prevent.
func TestServiceOpen_CrossTenantRefusedBeforeAnyObjectFetch(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "svc open cross A")
	tenantB := seedTenant(t, super, "svc open cross B")
	cA := identity(ctx, tenantA, memberSubject)
	cB := identity(ctx, tenantB, memberSubject)

	body := []byte("hello world")
	objs := &fakeObjects{getObject: document.Object{Body: io.NopCloser(strings.NewReader("leak")), Size: 4}}
	svc := document.NewService(document.NewStore(app), objs)

	stored, _, err := svc.Store(cA, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Store as A: %v", err)
	}

	// Positive half: A can open it, so the refusal below is a refusal.
	if _, _, err := svc.Open(cA, stored.ID, ""); err != nil {
		t.Fatalf("Open as the owning tenant: %v", err)
	}
	before := len(objs.getKeys)

	doc, obj, err := svc.Open(cB, stored.ID, "")
	if !errors.Is(err, document.ErrNotFound) {
		t.Fatalf("Open of A's document as B = %v, want ErrNotFound", err)
	}
	if doc.ID != "" || obj.Body != nil {
		t.Errorf("refused Open returned (id %q, body %v), want two zero values", doc.ID, obj.Body)
	}
	if len(objs.getKeys) != before {
		t.Errorf("object-store GETs after a refused Open = %d, want %d — no bytes may be fetched for an invisible row",
			len(objs.getKeys), before)
	}
	if n := auditCount(t, app, tenantB, "document.read"); n != 0 {
		t.Errorf("document.read rows under the refused tenant = %d, want 0", n)
	}
}

func TestServiceOpen_MalformedIDIsValidationErrorAndFetchesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "svc open malformed")
	c := identity(ctx, tenantID, memberSubject)

	objs := &fakeObjects{}
	svc := document.NewService(document.NewStore(app), objs)

	_, _, err := svc.Open(c, "not-a-uuid", "")
	if !errors.Is(err, document.ErrValidation) {
		t.Fatalf("Open(%q) = %v, want ErrValidation", "not-a-uuid", err)
	}
	if len(objs.calls) != 0 {
		t.Errorf("object-store calls for a malformed id = %v, want none", objs.calls)
	}
}

// The row resolves and its read is audited; only the byte fetch fails. The
// caller gets the error and no half-populated Document to act on.
func TestServiceOpen_ObjectFailureReturnsNoDocument(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "svc open object failure")
	c := identity(ctx, tenantID, memberSubject)

	body := []byte("hello world")
	objs := &fakeObjects{getErr: document.ErrRangeNotSatisfiable}
	svc := document.NewService(document.NewStore(app), objs)

	stored, _, err := svc.Store(c, "a.csv", "text/csv", int64(len(body)), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	doc, obj, err := svc.Open(c, stored.ID, "bytes=99-200")
	if !errors.Is(err, document.ErrRangeNotSatisfiable) {
		t.Fatalf("Open with an unsatisfiable range = %v, want ErrRangeNotSatisfiable to survive unwrapped", err)
	}
	if doc.ID != "" || obj.Body != nil {
		t.Errorf("failed Open returned (id %q, body %v), want two zero values", doc.ID, obj.Body)
	}
	// The read was resolved and committed before the fetch was attempted.
	if n := auditCount(t, app, tenantID, "document.read"); n != 1 {
		t.Errorf("document.read audit rows = %d, want 1 — the row lookup committed before the fetch failed", n)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
