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
	"github.com/jackc/pgx/v5"
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
