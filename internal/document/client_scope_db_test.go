package document_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/importer"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
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

// csExecLegacy seeds a pre-migration row: the replica role skips the bind trigger.
func csExecLegacy(t *testing.T, super *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin legacy seed: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatalf("set replica role: %v", err)
	}
	if _, err := tx.Exec(ctx, query, args...); err != nil {
		t.Fatalf("seed legacy (%s): %v", query, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit legacy seed: %v", err)
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
	if got, err := f.store.GetLinked(f.limited, f.docZ); err != nil || got.ID != f.docZ {
		t.Errorf("GetLinked(Z, unlinked) = %q, %v, want found", got.ID, err)
	}
	if _, err := f.store.GetLinked(f.limited, f.docY); !errors.Is(err, document.ErrNotFound) {
		t.Errorf("GetLinked(Y, linked only by B's batch) as a reviewer limited to A: err = %v, want ErrNotFound", err)
	}
	if n := auditCount(t, f.app, f.tenant, "document.read"); n != 2 {
		t.Errorf("document.read rows = %d, want 2 (X and Z)", n)
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
	csExecLegacy(t, f.super, `INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, 'CS-BOTH-B', $3)`, f.tenant, f.entB, both)
	onlyB := csDocument(t, f.super, f.tenant)
	csExec(t, f.super, `INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, 'CS-ONLY-B', $3)`, f.tenant, f.entB, onlyB)
	bothMixed := csDocument(t, f.super, f.tenant)
	csExec(t, f.super, `INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, 'CS-MIX-B', $3)`, f.tenant, f.entB, bothMixed)
	csExecLegacy(t, f.super, `INSERT INTO import_batches (tenant_id, entity_id, document_id, status, rows_total, rows_valid, rows_invalid, errors)
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
	for name, id := range map[string]string{"X (A's invoice)": f.docX, "W (A's batch)": f.docW} {
		if _, err := f.store.GetLinked(noClients, id); !errors.Is(err, document.ErrNotFound) {
			t.Errorf("GetLinked(%s) as a reviewer with no clients: err = %v, want ErrNotFound", name, err)
		}
	}
	if got, err := f.store.GetLinked(noClients, f.docZ); err != nil || got.ID != f.docZ {
		t.Errorf("GetLinked(Z, unlinked) as a reviewer with no clients = %q, %v, want found", got.ID, err)
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
	for name, id := range map[string]string{"Y, B's batch": f.docY, "unknown": uuid.NewString()} {
		rec := get(f.limited, id)
		if rec.Code != http.StatusNotFound {
			t.Errorf("limited reviewer, %s: status = %d, want 404", name, rec.Code)
		}
		refused = append(refused, rec.Body.String())
	}
	if len(refused) != 2 || refused[0] != refused[1] || !strings.Contains(refused[0], "not found") {
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

	for name, id := range map[string]string{"X, A's invoice": f.docX, "W, A's batch": f.docW, "Z, unlinked": f.docZ} {
		if rec := get(f.limited, id); rec.Code != http.StatusOK || rec.Body.String() != "hello world" {
			t.Errorf("limited reviewer, %s: %d %q, want 200 with the bytes", name, rec.Code, rec.Body.String())
		}
	}
	for name, ctx := range map[string]context.Context{"admin": f.admin, "All clients": f.allCli} {
		if rec := get(ctx, f.docZ); rec.Code != http.StatusOK {
			t.Errorf("%s reading the unlinked upload: status = %d, want 200", name, rec.Code)
		}
	}
	if n := auditCount(t, f.app, f.tenant, "document.read"); n != 5 {
		t.Errorf("document.read rows = %d, want 5 (three limited reads, two full-access reads)", n)
	}
}

func TestClientScope_AHiddenInvoiceOrBatchLinkRefusesTheDocument(t *testing.T) {
	f := newCSFixture(t)
	byInvoice, byBoth := csDocument(t, f.super, f.tenant), csDocument(t, f.super, f.tenant)
	csExec(t, f.super, `INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, 'CS-HID-I', $3)`, f.tenant, f.entB, byInvoice)
	csExec(t, f.super, `INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, 'CS-HID-J', $3)`, f.tenant, f.entB, byBoth)
	csExec(t, f.super, `INSERT INTO import_batches (tenant_id, entity_id, document_id, status, rows_total, rows_valid, rows_invalid, errors)
		VALUES ($1, $2, $3, 'completed', 1, 1, 0, '[]'::jsonb)`, f.tenant, f.entB, byBoth)

	for name, id := range map[string]string{"B's invoice": byInvoice, "B's invoice and batch": byBoth} {
		if _, err := f.store.GetLinked(f.limited, id); !errors.Is(err, document.ErrNotFound) {
			t.Errorf("GetLinked(linked only by %s) as a reviewer limited to A: err = %v, want ErrNotFound", name, err)
		}
		if got, err := f.store.GetLinked(f.admin, id); err != nil || got.ID != id {
			t.Errorf("GetLinked(linked by %s) as admin = %q, %v, want found", name, got.ID, err)
		}
	}
	if n := auditCount(t, f.app, f.tenant, "document.read"); n != 2 {
		t.Errorf("document.read rows = %d, want 2 (the admin reads only)", n)
	}
}

type linkState struct{ hasInvoice, hasLink bool }

func csLinkState(t *testing.T, ctx context.Context, tx pgx.Tx, doc string) linkState {
	t.Helper()
	var s linkState
	if err := tx.QueryRow(ctx, `SELECT has_invoice, has_link FROM public.document_link_state($1)`, doc).Scan(&s.hasInvoice, &s.hasLink); err != nil {
		t.Fatalf("document_link_state(%s): %v", doc, err)
	}
	return s
}

func csScope(t *testing.T, ctx context.Context, tx pgx.Tx) *string {
	t.Helper()
	var s *string
	if err := tx.QueryRow(ctx, `SELECT current_setting('app.entity_scope', true)`).Scan(&s); err != nil {
		t.Fatalf("read scope: %v", err)
	}
	return s
}

func TestClientScope_DocumentLinkStateAnswersOnlyTheBooleanPairInsideTheTenantAndRestoresTheScope(t *testing.T) {
	f := newCSFixture(t)
	ctx := context.Background()
	byInvoiceB, other := csDocument(t, f.super, f.tenant), csDocument(t, f.super, f.tenant)
	csExec(t, f.super, `INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, 'CS-LS-B', $3)`, f.tenant, f.entB, byInvoiceB)
	foreignTenant := seedTenant(t, f.super, "doc link state foreign")
	foreignDoc := csDocument(t, f.super, foreignTenant)
	csExec(t, f.super, `INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, 'CS-LS-F', $3)`,
		foreignTenant, csEntity(t, f.super, foreignTenant, "Foreign client"), foreignDoc)

	want := map[string]struct {
		doc string
		out linkState
	}{
		"B's batch only":        {f.docY, linkState{false, true}},
		"B's invoice only":      {byInvoiceB, linkState{true, true}},
		"A's invoice":           {f.docX, linkState{true, true}},
		"unlinked":              {f.docZ, linkState{false, false}},
		"another tenant's":      {foreignDoc, linkState{false, false}},
		"an id that is no row":  {uuid.NewString(), linkState{false, false}},
		"unlinked, a second id": {other, linkState{false, false}},
	}
	priors := map[string]*string{"unset": nil, "empty": ptr(""), "braces": ptr("{}"), "star": ptr("*"), "client A": ptr("{" + f.entA + "}")}
	if len(want) == 0 || len(priors) == 0 {
		t.Fatal("no cases")
	}

	for pname, prior := range priors {
		conn, err := f.app.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, f.tenant); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		if prior != nil {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.entity_scope', $1, true)`, *prior); err != nil {
				t.Fatalf("set scope: %v", err)
			}
		}
		for dname, c := range want {
			if got := csLinkState(t, ctx, tx, c.doc); got != c.out {
				t.Errorf("prior scope %s, %s: document_link_state = %+v, want %+v", pname, dname, got, c.out)
			}
			after := csScope(t, ctx, tx)
			switch {
			case prior == nil && (after == nil || *after == ""):
			case prior != nil && after != nil && *after == *prior:
			default:
				t.Errorf("prior scope %s, %s: scope after = %v, want it as before", pname, dname, after)
			}
		}
		if prior != nil && *prior == "{"+f.entA+"}" {
			if n := csTxCount(t, ctx, tx, `SELECT count(*) FROM invoices WHERE source_document_id = $1`, byInvoiceB); n != 0 {
				t.Errorf("after the call the scope is back: B's invoice rows visible = %d, want 0", n)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		var left string
		if err := conn.QueryRow(ctx, `SELECT coalesce(current_setting('app.entity_scope', true), '')`).Scan(&left); err != nil {
			t.Fatalf("read scope after rollback: %v", err)
		}
		if left != "" {
			t.Errorf("prior scope %s: the connection keeps scope %q after the transaction, want none", pname, left)
		}
		conn.Release()
	}
}

func csTxCount(t *testing.T, ctx context.Context, tx pgx.Tx, q string, args ...any) int {
	t.Helper()
	var n int
	if err := tx.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		t.Fatalf("count (%s): %v", q, err)
	}
	return n
}

func TestClientScope_DocumentLinkStateIsExecutableByTheAppRoleOnly(t *testing.T) {
	f := newCSFixture(t)
	rows, err := f.super.Query(context.Background(),
		`SELECT a.grantee::regrole::text FROM pg_proc p, aclexplode(p.proacl) a
		  WHERE p.proname = 'document_link_state' AND a.privilege_type = 'EXECUTE' AND a.grantee <> p.proowner`)
	if err != nil {
		t.Fatalf("read grants: %v", err)
	}
	defer rows.Close()
	var grantees []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			t.Fatalf("scan: %v", err)
		}
		grantees = append(grantees, g)
	}
	if len(grantees) != 1 || grantees[0] != "invoice_app" {
		t.Errorf("EXECUTE grantees besides the owner = %v, want [invoice_app] (a PUBLIC grant reads as \"-\")", grantees)
	}
}

// csUpload stores a CSV document through Store.Upsert under who's identity.
func csUpload(t *testing.T, f csFixture, who context.Context, seed string) document.Document {
	t.Helper()
	h := hashHex(seed)
	d, _, err := f.store.Upsert(who, document.Document{
		StorageKey: document.StorageKey(f.tenant, h), ContentHash: h, SizeBytes: 11, Filename: ptr("upload.csv"),
	})
	if err != nil {
		t.Fatalf("Upsert(%s): %v", seed, err)
	}
	return d
}

// csCiteAs files an invoice for entity citing doc, inside who's request transaction.
func csCiteAs(t *testing.T, f csFixture, who context.Context, entity, doc, number string) {
	t.Helper()
	if err := db.WithinRequestTenantTx(who, f.app, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, $3, $4)`,
			f.tenant, entity, number, doc)
		return err
	}); err != nil {
		t.Fatalf("cite %s for %s as a member: %v", doc, entity, err)
	}
}

// csBinding reads documents.entity_id (nil is NULL) and the sorted uploader ids from document_uploaders as the superuser.
func csBinding(t *testing.T, super *pgxpool.Pool, doc string) (entity *string, uploaders []string) {
	t.Helper()
	ctx := context.Background()
	if err := super.QueryRow(ctx, `SELECT entity_id::text FROM documents WHERE id = $1`, doc).Scan(&entity); err != nil {
		t.Fatalf("read binding of %s: %v", doc, err)
	}
	rows, err := super.Query(ctx, `SELECT user_id::text FROM document_uploaders WHERE document_id = $1 ORDER BY 1`, doc)
	if err != nil {
		t.Fatalf("read the uploaders of %s: %v", doc, err)
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			t.Fatalf("scan an uploader: %v", err)
		}
		uploaders = append(uploaders, u)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the uploaders of %s: %v", doc, err)
	}
	return entity, uploaders
}

// csEntityOf reads documents.entity_id as the superuser; nil is NULL.
func csEntityOf(t *testing.T, super *pgxpool.Pool, doc string) *string {
	t.Helper()
	var entity *string
	if err := super.QueryRow(context.Background(), `SELECT entity_id::text FROM documents WHERE id = $1`, doc).Scan(&entity); err != nil {
		t.Fatalf("read documents.entity_id of %s: %v", doc, err)
	}
	return entity
}

// csWantUploaders asserts the document's uploader set is exactly the subjects of who.
func csWantUploaders(t *testing.T, f csFixture, what, doc string, who ...context.Context) {
	t.Helper()
	var want []string
	for _, w := range who {
		want = append(want, subjectOf(t, w))
	}
	slices.Sort(want)
	if _, got := csBinding(t, f.super, doc); !slices.Equal(got, want) {
		t.Errorf("%s: uploaders = %v, want %v", what, got, want)
	}
}

func subjectOf(t *testing.T, ctx context.Context) string {
	t.Helper()
	id, ok := auth.IdentityFromContext(ctx)
	if !ok {
		t.Fatal("context carries no identity")
	}
	return id.Subject
}

// csCitedUpload: a member limited to A uploads a document that the admin then cites for B.
func csCitedUpload(t *testing.T, f csFixture, uploader context.Context) string {
	t.Helper()
	d := csUpload(t, f, uploader, "uploaded-then-cited")
	csCiteAs(t, f, f.admin, f.entB, d.ID, "CS-UP-B")
	if entity := csEntityOf(t, f.super, d.ID); entity == nil || *entity != f.entB {
		t.Fatalf("control: documents.entity_id = %v after the admin cited it for B, want %s", entity, f.entB)
	}
	return d.ID
}

func TestClientScope_EveryMemberWhoUploadsTheBytesIsAnUploader(t *testing.T) {
	f := newCSFixture(t)
	u1, u2 := csMember(t, f.super, f.tenant, "reviewer", false, f.entA), csMember(t, f.super, f.tenant, "reviewer", false, f.entA)

	if n := mustCount(t, f.super, `SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'documents' AND column_name = 'uploaded_by'`); n != 0 {
		t.Errorf("documents.uploaded_by columns = %d, want 0 (document_uploaders replaces it)", n)
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'documents' AND column_name = 'entity_id'`); n != 1 {
		t.Fatalf("control: documents.entity_id columns = %d, want 1", n)
	}

	first := csUpload(t, f, u1, "same-bytes")
	csWantUploaders(t, f, "after the first upload", first.ID, u1)
	second := csUpload(t, f, u2, "same-bytes")
	if second.ID != first.ID {
		t.Fatalf("control: the second upload of the same bytes made a new row %s, want %s", second.ID, first.ID)
	}
	csWantUploaders(t, f, "after the dedupe by a second member", first.ID, u1, u2)
	csUpload(t, f, u1, "same-bytes")
	csWantUploaders(t, f, "after the first member uploads again", first.ID, u1, u2)
	if n := mustCount(t, f.super, `SELECT count(*) FROM document_uploaders WHERE document_id = $1`, first.ID); n != 2 {
		t.Errorf("document_uploaders rows = %d, want 2", n)
	}
	if _, got := csBinding(t, f.super, f.docZ); len(got) != 0 {
		t.Errorf("uploaders of a superuser-inserted document = %v, want none", got)
	}

	// An in-process actor (a non-uuid subject) names no member: its upload, new or deduped, records nobody.
	worker := auth.WithIdentity(context.Background(), auth.Identity{Subject: "worker", Role: "authenticated", TenantID: f.tenant})
	if again := csUpload(t, f, worker, "same-bytes"); again.ID != first.ID {
		t.Fatalf("control: the in-process dedupe made a new row %s, want %s", again.ID, first.ID)
	}
	csWantUploaders(t, f, "after an in-process dedupe", first.ID, u1, u2)
	fresh := csUpload(t, f, worker, "worker-only-bytes")
	if _, got := csBinding(t, f.super, fresh.ID); len(got) != 0 {
		t.Errorf("uploaders of an in-process upload = %v, want none", got)
	}
}

// csCoUploaded: two members limited to A upload the same bytes; the admin then cites the document for B.
func csCoUploaded(t *testing.T, f csFixture) (u1, u2 context.Context, doc string) {
	t.Helper()
	u1, u2 = csMember(t, f.super, f.tenant, "reviewer", false, f.entA), csMember(t, f.super, f.tenant, "reviewer", false, f.entA)
	d := csUpload(t, f, u1, "co-uploaded")
	if again := csUpload(t, f, u2, "co-uploaded"); again.ID != d.ID {
		t.Fatalf("control: the second upload of the same bytes made a new row %s, want %s", again.ID, d.ID)
	}
	csCiteAs(t, f, f.admin, f.entB, d.ID, "CS-CO-B")
	if entity := csEntityOf(t, f.super, d.ID); entity == nil || *entity != f.entB {
		t.Fatalf("control: documents.entity_id = %v after the admin cited it for B, want %s", entity, f.entB)
	}
	return u1, u2, d.ID
}

func TestClientScope_ACoUploaderReadsTheirDocumentAfterAnotherClientCitesIt(t *testing.T) {
	f := newCSFixture(t)
	_, u2, doc := csCoUploaded(t, f)

	if got, err := f.store.GetLinked(u2, doc); err != nil || got.ID != doc {
		t.Errorf("GetLinked as the co-uploader = %q, %v, want found", got.ID, err)
	}
	objects := &fakeObjects{}
	svc := document.NewService(f.store, objects)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/documents/{id}", document.DownloadHandler(svc.OpenLinked, nil))
	mux.HandleFunc("GET /v1/documents/{id}/sheet", importer.SheetHandler(svc.OpenLinked, nil))
	for route, suffix := range map[string]string{"download": "", "sheet": "/sheet"} {
		objects.getObject = document.Object{Body: io.NopCloser(strings.NewReader("Inv No\nU-1\n")), Size: 12}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/documents/"+doc+suffix, nil).WithContext(u2))
		if rec.Code != http.StatusOK {
			t.Errorf("%s as the co-uploader: %d %s, want 200", route, rec.Code, rec.Body.String())
		}
	}
}

func TestClientScope_AMemberWhoNeverUploadedIsRefusedACoUploadedBoundDocument(t *testing.T) {
	f := newCSFixture(t)
	_, u2, doc := csCoUploaded(t, f)
	u3 := csMember(t, f.super, f.tenant, "reviewer", false, f.entA)

	// Positive half: a co-uploader reads the very document u3 is refused.
	if got, err := f.store.GetLinked(u2, doc); err != nil || got.ID != doc {
		t.Errorf("control: GetLinked as the co-uploader = %q, %v, want found", got.ID, err)
	}
	if _, err := f.store.GetLinked(u3, doc); !errors.Is(err, document.ErrNotFound) {
		t.Errorf("GetLinked as a member limited to A who never uploaded: err = %v, want ErrNotFound", err)
	}
	if got := csDownloadAs(f, u3, doc); got.Code != http.StatusNotFound {
		t.Errorf("download as a member who never uploaded: %d %q, want 404", got.Code, got.Body.String())
	} else if unknown := csDownloadAs(f, u3, uuid.NewString()); got.Body.String() != unknown.Body.String() || !strings.Contains(got.Body.String(), "not found") {
		t.Errorf("download of the bound document: %q, want the unknown id's body %q", got.Body.String(), unknown.Body.String())
	}
}

// csDownloadAs serves GET /v1/documents/{id} as who with a fake object store.
func csDownloadAs(f csFixture, who context.Context, id string) *httptest.ResponseRecorder {
	objects := &fakeObjects{getObject: document.Object{Body: io.NopCloser(strings.NewReader("hello world")), Size: 11}}
	svc := document.NewService(f.store, objects)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/documents/{id}", document.DownloadHandler(svc.OpenLinked, nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/documents/"+id, nil).WithContext(who))
	return rec
}

// csHeldSentence is the refusal sentence, spelled out so a wrong constant fails here too.
const csHeldSentence = "this file already belongs to a client you cannot see; remove it to continue"

func csStoreAs(svc *document.Service, who context.Context, seed string) (document.Document, bool, error) {
	return svc.Store(who, "up.csv", "text/csv", int64(len(seed)), strings.NewReader(seed))
}

// csHeld: u1 (limited to B) stores the bytes; the admin then cites the document for B.
func csHeld(t *testing.T, f csFixture, svc *document.Service, seed string) (u1 context.Context, doc document.Document) {
	t.Helper()
	u1 = csMember(t, f.super, f.tenant, "reviewer", false, f.entB)
	doc, reused, err := csStoreAs(svc, u1, seed)
	if err != nil || reused {
		t.Fatalf("control: the first upload = reused %v, err %v, want a new document", reused, err)
	}
	csCiteAs(t, f, f.admin, f.entB, doc.ID, "CS-HELD-"+seed)
	return u1, doc
}

func TestClientScope_UploadDedupeLeak_AnOutOfScopeHitAnswersNoIDAndNoFilename(t *testing.T) {
	f := newCSFixture(t)
	u2 := csMember(t, f.super, f.tenant, "reviewer", false, f.entA)
	svc := document.NewService(f.store, &fakeObjects{})
	u1, d := csHeld(t, f, svc, "bound-to-b!")

	// The repeat is a second tab or device: the refusal holds no session state.
	for _, attempt := range []string{"first", "repeat"} {
		again, reused, err := csStoreAs(svc, u2, "bound-to-b!")
		if !errors.Is(err, document.ErrHeldElsewhere) {
			t.Errorf("%s dedupe upload of bytes bound to B as a member limited to A: err = %v, want ErrHeldElsewhere", attempt, err)
		}
		if again.ID != "" || again.Filename != nil || reused {
			t.Errorf("%s dedupe upload answered id %q filename %v reused=%v, want the zero Document", attempt, again.ID, again.Filename, reused)
		}
	}
	if n := auditCitingCount(t, f.app, f.tenant, "document.reused", d.ID); n != 0 {
		t.Errorf("document.reused audit rows citing the held document = %d, want 0", n)
	}
	csWantUploaders(t, f, "after the refused dedupe uploads", d.ID, u1)

	if _, err := f.store.GetLinked(u2, d.ID); !errors.Is(err, document.ErrNotFound) {
		t.Errorf("GetLinked as the refused uploader: err = %v, want ErrNotFound", err)
	}
	if got, unknown := csDownloadAs(f, u2, d.ID), csDownloadAs(f, u2, uuid.NewString()); got.Code != http.StatusNotFound || got.Body.String() != unknown.Body.String() {
		t.Errorf("download: %d %q, want the unknown id's 404 %d %q", got.Code, got.Body.String(), unknown.Code, unknown.Body.String())
	}
	// The refusal grants no citation either: the document stays bound to B and A gets no invoice for it.
	err := db.WithinRequestTenantTx(u2, f.app, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, 'CS-OUT-A', $3)`, f.tenant, f.entA, d.ID)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != "documents_bound_client" {
		t.Errorf("citing the bound document for A after the refusal: err = %v, want 23503 documents_bound_client", err)
	}
	if entity := csEntityOf(t, f.super, d.ID); entity == nil || *entity != f.entB {
		t.Errorf("documents.entity_id = %v after the refused citation, want %s", entity, f.entB)
	}
	if got, err := f.store.GetLinked(u1, d.ID); err != nil || got.ID != d.ID {
		t.Errorf("control: GetLinked as the first uploader (limited to B) = %q, %v, want found", got.ID, err)
	}
}

func TestClientScope_UploadDedupeLeak_TheRefusalIsTheReadableClause(t *testing.T) {
	f := newCSFixture(t)
	svc := document.NewService(f.store, &fakeObjects{})
	_, d := csHeld(t, f, svc, "readable-clause")
	limitedB := csMember(t, f.super, f.tenant, "reviewer", false, f.entB)
	limitedA := csMember(t, f.super, f.tenant, "reviewer", false, f.entA)

	// Same bytes, same role, never an uploader: only the client set differs.
	if got, reused, err := csStoreAs(svc, limitedB, "readable-clause"); err != nil || got.ID != d.ID || !reused {
		t.Errorf("control: upload as a member limited to B = id %q reused %v err %v, want %q reused true", got.ID, reused, err, d.ID)
	}
	if _, _, err := csStoreAs(svc, limitedA, "readable-clause"); !errors.Is(err, document.ErrHeldElsewhere) {
		t.Errorf("upload as a member limited to A: err = %v, want ErrHeldElsewhere", err)
	}
}

func TestClientScope_UploadDedupe_AReadableHitAnswersTheDocument(t *testing.T) {
	f := newCSFixture(t)
	svc := document.NewService(f.store, &fakeObjects{})
	coB := csMember(t, f.super, f.tenant, "reviewer", false, f.entB)
	first, reused, err := csStoreAs(svc, coB, "readable-hit")
	if err != nil || reused {
		t.Fatalf("control: the first upload = reused %v, err %v, want a new document", reused, err)
	}
	// coB uploaded before the bind, so it keeps read after A cites the document.
	csCiteAs(t, f, f.admin, f.entA, first.ID, "CS-READ-A")

	want := []context.Context{coB}
	for name, who := range map[string]context.Context{
		"admin": f.admin, "an All clients member": f.allCli, "a member limited to the bound client A": f.limited, "a recorded uploader limited to B": coB,
	} {
		got, reused, err := csStoreAs(svc, who, "readable-hit")
		if err != nil || got.ID != first.ID || !reused {
			t.Errorf("upload as %s = id %q reused %v err %v, want %q reused true", name, got.ID, reused, err, first.ID)
		}
		if who != coB {
			want = append(want, who)
		}
	}
	csWantUploaders(t, f, "after the readable hits", first.ID, want...)

	g := newCSFixture(t)
	gsvc := document.NewService(g.store, &fakeObjects{})
	csExec(t, g.super, `UPDATE tenants SET kind = 'in_house' WHERE id = $1`, g.tenant)
	uploader := csMember(t, g.super, g.tenant, "reviewer", false, g.entA)
	doc, _, err := csStoreAs(gsvc, uploader, "in-house-hit")
	if err != nil {
		t.Fatalf("control: in-house first upload: %v", err)
	}
	csCiteAs(t, g, g.admin, g.entA, doc.ID, "CS-READ-IH")
	inHouse := csMember(t, g.super, g.tenant, "reviewer", false)
	if got, reused, err := csStoreAs(gsvc, inHouse, "in-house-hit"); err != nil || got.ID != doc.ID || !reused {
		t.Errorf("upload as an in-house reviewer with no clients = id %q reused %v err %v, want %q reused true", got.ID, reused, err, doc.ID)
	}
	csWantUploaders(t, g, "after the in-house hit", doc.ID, uploader, inHouse)
}

// csPreviewAs POSTs one file part to the real preview handler over the real Service.Store.
func csPreviewAs(t *testing.T, svc *document.Service, who context.Context, filename, contentType, content string) (int, []byte, map[string]json.RawMessage) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	h.Set("Content-Type", contentType)
	fw, err := w.CreatePart(h)
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	_, _ = fw.Write([]byte(content))
	_ = w.Close()
	r := httptest.NewRequest("POST", "/v1/imports/preview", &buf).WithContext(who)
	r.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	importer.PreviewHandler(svc.Store, nil).ServeHTTP(rec, r)
	decoded := map[string]json.RawMessage{}
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return rec.Code, rec.Body.Bytes(), decoded
}

func TestClientScope_UploadDedupeLeak_ThePreviewRouteAnswers409BeforeTheFormatCheck(t *testing.T) {
	f := newCSFixture(t)
	u2 := csMember(t, f.super, f.tenant, "reviewer", false, f.entA)
	svc := document.NewService(f.store, &fakeObjects{})
	csHeld(t, f, svc, "bound-to-b!")

	for _, c := range []struct{ name, filename, contentType string }{
		{"a recognised format", "held.csv", "text/csv"},
		{"an unrecognised format", "data.txt", "application/octet-stream"},
	} {
		code, raw, decoded := csPreviewAs(t, svc, u2, c.filename, c.contentType, "bound-to-b!")
		if code != http.StatusConflict {
			t.Errorf("%s: held bytes as a member limited to A: status %d (%s), want 409", c.name, code, raw)
		}
		if _, ok := decoded["document_id"]; ok {
			t.Errorf("%s: the answer carries document_id (%s), want none", c.name, raw)
		}
		if got := string(decoded["error"]); got != `"`+csHeldSentence+`"` || len(decoded) != 1 {
			t.Errorf("%s: body %s, want exactly {\"error\":%q}", c.name, raw, csHeldSentence)
		}
	}

	// Control: fresh bytes pass the store and reach the format check, which carries the id.
	code, raw, decoded := csPreviewAs(t, svc, u2, "data.txt", "application/octet-stream", "fresh-bytes")
	if code != http.StatusBadRequest || len(decoded["document_id"]) == 0 {
		t.Errorf("control: fresh bytes as data.txt = %d %s, want 400 with a document_id", code, raw)
	}
}

func TestClientScope_UploadDedupeLeak_TheGateRefusesBeforeTheHeldAnswer(t *testing.T) {
	f := newCSFixture(t)
	svc := document.NewService(f.store, &fakeObjects{})
	csHeld(t, f, svc, "gate-first")
	suspended := csMember(t, f.super, f.tenant, "reviewer", false, f.entA)
	locked := csMember(t, f.super, f.tenant, "reviewer", false, f.entA)
	active := csMember(t, f.super, f.tenant, "reviewer", false, f.entA)

	if _, _, err := csStoreAs(svc, active, "gate-first"); !errors.Is(err, document.ErrHeldElsewhere) {
		t.Errorf("control: an active member limited to A: err = %v, want ErrHeldElsewhere", err)
	}
	csExec(t, f.super, `UPDATE memberships SET status = 'suspended' WHERE tenant_id = $1 AND user_id = $2`, f.tenant, subjectOf(t, suspended))
	if _, _, err := csStoreAs(svc, suspended, "gate-first"); !errors.Is(err, db.ErrNotActiveMember) || errors.Is(err, document.ErrHeldElsewhere) {
		t.Errorf("suspended member on held bytes: err = %v, want db.ErrNotActiveMember", err)
	}
	setTenantLock(t, f.super, f.tenant, true)
	if _, _, err := csStoreAs(svc, locked, "gate-first"); !errors.Is(err, db.ErrWorkspaceLocked) || errors.Is(err, document.ErrHeldElsewhere) {
		t.Errorf("locked workspace on held bytes: err = %v, want db.ErrWorkspaceLocked", err)
	}
}

func TestClientScope_ADedupeUploadDoesNotWaitForAnOpenCitation(t *testing.T) {
	f := newCSFixture(t)
	u1 := csMember(t, f.super, f.tenant, "reviewer", false, f.entA)
	u2 := csMember(t, f.super, f.tenant, "reviewer", false, f.entA)
	d := csUpload(t, f, u1, "open-citation")
	csCiteAs(t, f, f.admin, f.entA, d.ID, "CS-WAIT-A1")

	ctx := context.Background()
	tx, err := f.super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the holding transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A repeat citation of a bound document takes the bind trigger's row lock and writes no new row version.
	if _, err := tx.Exec(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, 'CS-WAIT-A2', $3)`,
		f.tenant, f.entA, d.ID); err != nil {
		t.Fatalf("hold an open repeat citation: %v", err)
	}

	timed, cancel := context.WithTimeout(u2, 5*time.Second)
	defer cancel()
	h := hashHex("open-citation")
	start := time.Now()
	got, _, err := f.store.Upsert(timed, document.Document{StorageKey: document.StorageKey(f.tenant, h), ContentHash: h, SizeBytes: 11})
	if err != nil {
		t.Fatalf("a dedupe upload while a repeat citation is open (waited %s): %v", time.Since(start), err)
	}
	if got.ID != d.ID {
		t.Fatalf("control: the dedupe upload made document %s, want %s", got.ID, d.ID)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("end the holding transaction: %v", err)
	}
	csWantUploaders(t, f, "after the dedupe upload", d.ID, u1, u2)
}

func TestClientScope_TheUploaderReadsTheirDocumentAfterAnotherClientCitesIt(t *testing.T) {
	f := newCSFixture(t)
	uploader := csMember(t, f.super, f.tenant, "reviewer", false, f.entA)
	doc := csCitedUpload(t, f, uploader)

	if got, err := f.store.GetLinked(uploader, doc); err != nil || got.ID != doc {
		t.Errorf("GetLinked as the uploader = %q, %v, want found", got.ID, err)
	}

	objects := &fakeObjects{}
	svc := document.NewService(f.store, objects)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/documents/{id}", document.DownloadHandler(svc.OpenLinked, nil))
	mux.HandleFunc("GET /v1/documents/{id}/sheet", importer.SheetHandler(svc.OpenLinked, nil))
	for route, suffix := range map[string]string{"download": "", "sheet": "/sheet"} {
		objects.getObject = document.Object{Body: io.NopCloser(strings.NewReader("Inv No\nU-1\n")), Size: 12}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/documents/"+doc+suffix, nil).WithContext(uploader))
		if rec.Code != http.StatusOK {
			t.Errorf("%s as the uploader: %d %s, want 200", route, rec.Code, rec.Body.String())
		}
	}
}

func TestClientScope_ANonUploaderOfTheSameClientIsRefusedABoundDocument(t *testing.T) {
	f := newCSFixture(t)
	uploader := csMember(t, f.super, f.tenant, "reviewer", false, f.entA)
	other := csMember(t, f.super, f.tenant, "reviewer", false, f.entA)
	doc := csCitedUpload(t, f, uploader)

	// Positive half: the uploader reads the very document the other member is refused.
	if got, err := f.store.GetLinked(uploader, doc); err != nil || got.ID != doc {
		t.Fatalf("control: GetLinked as the uploader = %q, %v, want found", got.ID, err)
	}
	if _, err := f.store.GetLinked(other, doc); !errors.Is(err, document.ErrNotFound) {
		t.Errorf("GetLinked as another member limited to A: err = %v, want ErrNotFound", err)
	}

	objects := &fakeObjects{}
	svc := document.NewService(f.store, objects)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/documents/{id}", document.DownloadHandler(svc.OpenLinked, nil))
	get := func(id string) *httptest.ResponseRecorder {
		objects.getObject = document.Object{Body: io.NopCloser(strings.NewReader("hello world")), Size: 11}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/documents/"+id, nil).WithContext(other))
		return rec
	}
	bound, unknown := get(doc), get(uuid.NewString())
	if bound.Code != http.StatusNotFound || bound.Body.String() != unknown.Body.String() || !strings.Contains(bound.Body.String(), "not found") {
		t.Errorf("download of the bound document: %d %q, want the unknown id's 404 %d %q", bound.Code, bound.Body.String(), unknown.Code, unknown.Body.String())
	}
}

func TestClientScope_TheBoundClientAndFullAccessReadABoundDocument(t *testing.T) {
	f := newCSFixture(t)
	uploader := csMember(t, f.super, f.tenant, "reviewer", false, f.entA)
	doc := csCitedUpload(t, f, uploader)

	readers := map[string]context.Context{
		"the uploader":          uploader,
		"a member limited to B": csMember(t, f.super, f.tenant, "reviewer", false, f.entB),
		"admin":                 f.admin,
		"an All clients member": f.allCli,
	}
	for name, who := range readers {
		if got, err := f.store.GetLinked(who, doc); err != nil || got.ID != doc {
			t.Errorf("GetLinked as %s = %q, %v, want found", name, got.ID, err)
		}
	}
}
