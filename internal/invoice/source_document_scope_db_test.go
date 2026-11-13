// source_document_scope_db_test.go: Store.Create cites only a document the caller can read
// (document.ReadableByScope). Every refusal fixture is refusable only by that clause: the
// entity is in the member's set and the document is in the same tenant, so the FK passes.
package invoice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const scUnknownBody = `{"error":"invoice: validation"}`

func scAllClientsMember(t *testing.T, super *pgxpool.Pool, tenant string) context.Context {
	t.Helper()
	user := uuid.NewString()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id, role, status, all_clients) VALUES ($1, $2, 'preparer', 'active', true)`,
		tenant, user); err != nil {
		t.Fatalf("seed all-clients member: %v", err)
	}
	return auth.WithIdentity(context.Background(), auth.Identity{Subject: user, Role: "authenticated", TenantID: tenant})
}

func scCiteByInvoice(t *testing.T, super *pgxpool.Pool, tenant, entity, doc, number string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, $3, $4) RETURNING id`,
		tenant, entity, number, doc).Scan(&id); err != nil {
		t.Fatalf("seed citing invoice: %v", err)
	}
	return id
}

// scCiteByInvoiceLegacy models a pre-migration row: the replica role skips the bind trigger.
func scCiteByInvoiceLegacy(t *testing.T, super *pgxpool.Pool, tenant, entity, doc, number string) {
	t.Helper()
	ctx := context.Background()
	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin legacy citation: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatalf("set replica role: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, $3, $4)`,
		tenant, entity, number, doc); err != nil {
		t.Fatalf("seed legacy citing invoice: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit legacy citation: %v", err)
	}
}

func scCiteByBatch(t *testing.T, super *pgxpool.Pool, tenant, entity, doc string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO import_batches (tenant_id, entity_id, document_id, status, rows_total, rows_valid, rows_invalid, errors)
		 VALUES ($1, $2, $3, 'completed', 1, 1, 0, '[]'::jsonb) RETURNING id`,
		tenant, entity, doc).Scan(&id); err != nil {
		t.Fatalf("seed citing batch: %v", err)
	}
	return id
}

// scSeedExtractionRows seeds one job and one page image; both tables are scoped by the document rule.
func scSeedExtractionRows(t *testing.T, super *pgxpool.Pool, tenant, doc string) {
	t.Helper()
	ctx := context.Background()
	if _, err := super.Exec(ctx,
		`INSERT INTO extraction_jobs (tenant_id, document_id, extractor, extractor_version) VALUES ($1, $2, 'test', '1')`,
		tenant, doc); err != nil {
		t.Fatalf("seed extraction_jobs: %v", err)
	}
	key := "tenants/" + tenant + "/pages/" + seedContentHash() + "/v1/p0001.png"
	if _, err := super.Exec(ctx,
		`INSERT INTO extraction_page_images (tenant_id, document_id, page_number, width_px, height_px, storage_key)
		 VALUES ($1, $2, 1, 1, 1, $3)`, tenant, doc, key); err != nil {
		t.Fatalf("seed extraction_page_images: %v", err)
	}
	// extraction_jobs_tenant_document_fk is ON DELETE RESTRICT; cleanups run last-in first-out.
	t.Cleanup(func() {
		_, _ = super.Exec(ctx, `DELETE FROM extraction_page_images WHERE document_id = $1`, doc)
		_, _ = super.Exec(ctx, `DELETE FROM extraction_jobs WHERE document_id = $1`, doc)
	})
}

func scVisibleExtractionRows(t *testing.T, app *pgxpool.Pool, ctx context.Context, doc string) (jobs, pages int) {
	t.Helper()
	if err := db.WithinRequestTenantTx(ctx, app, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM extraction_jobs WHERE document_id = $1`, doc).Scan(&jobs); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM extraction_page_images WHERE document_id = $1`, doc).Scan(&pages)
	}); err != nil {
		t.Fatalf("count extraction rows: %v", err)
	}
	return jobs, pages
}

func scInHouse(t *testing.T, super *pgxpool.Pool, tenant string) {
	t.Helper()
	if _, err := super.Exec(context.Background(), `UPDATE tenants SET kind = 'in_house' WHERE id = $1`, tenant); err != nil {
		t.Fatalf("set in_house: %v", err)
	}
}

func scPost(f csFixture, who context.Context, entity, number string, doc *string) (int, string) {
	body := fmt.Sprintf(`{"entity_id":%q,"invoice_number":%q`, entity, number)
	if doc != nil {
		body += fmt.Sprintf(`,"source_document_id":%q`, *doc)
	}
	w := csServe(CreateHandler(f.store.Create, nil), who, "POST", "/v1/invoices", "", body+"}")
	return w.Code, strings.TrimSpace(w.Body.String())
}

func scCreate(f csFixture, who context.Context, entity, number string, doc *string) error {
	_, err := f.store.Create(who, CreateInput{EntityID: entity, InvoiceNumber: number, SourceDocumentID: doc,
		LineItems: []LineItemInput{{Description: strPtr("SC line")}}})
	return err
}

func scInvoicesNumbered(t *testing.T, f csFixture, numbers ...string) int {
	t.Helper()
	return mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE tenant_id = $1 AND invoice_number = ANY($2)`, f.tenant, numbers)
}

func TestClientScope_CreateCitingAnUnreadableDocumentAnswersAsAnUnknownOne(t *testing.T) {
	f := newCSFixture(t, "SC unknown shape")
	d := seedDocument(t, f.super, f.tenant)
	scCiteByInvoice(t, f.super, f.tenant, f.entB, d, "SC-B-CITE")
	otherTenant := seedTenant(t, f.super, "SC unknown shape foreign")
	foreign := seedDocument(t, f.super, otherTenant)
	unknown := uuid.NewString()

	uCode, uBody := scPost(f, f.limited, f.entA, "SC-1-U", &unknown)
	if uCode != http.StatusBadRequest || uBody != scUnknownBody {
		t.Fatalf("citing an unknown id: %d %s, want 400 %s", uCode, uBody, scUnknownBody)
	}
	for name, c := range map[string]struct{ doc, number string }{
		"another client's document": {d, "SC-1-D"}, "another tenant's document": {foreign, "SC-1-F"},
	} {
		code, body := scPost(f, f.limited, f.entA, c.number, &c.doc)
		if code != uCode || body != uBody {
			t.Errorf("citing %s: %d %s, want %d %s (the unknown id's answer)", name, code, body, uCode, uBody)
		}
	}
	// A full-access caller has no scope to refuse on; another tenant's document and an unknown id still answer alike.
	aCode, aBody := scPost(f, f.admin, f.entA, "SC-1-AU", &unknown)
	fCode, fBody := scPost(f, f.admin, f.entA, "SC-1-AF", &foreign)
	if aCode != http.StatusBadRequest || aBody != scUnknownBody || fCode != aCode || fBody != aBody {
		t.Errorf("admin citing an unknown id / another tenant's document: %d %s / %d %s, want 400 %s for both", aCode, aBody, fCode, fBody, scUnknownBody)
	}
}

func TestClientScope_CreateCitingAnotherClientsDocumentWritesNothing(t *testing.T) {
	f := newCSFixture(t, "SC writes nothing")
	d, d2 := seedDocument(t, f.super, f.tenant), seedDocument(t, f.super, f.tenant)
	scCiteByInvoice(t, f.super, f.tenant, f.entB, d, "SC-B-CITE")

	// Control: the same member and entity cite an uncited document, so the counters can move.
	if err := scCreate(f, f.limited, f.entA, "SC-2-CTRL", &d2); err != nil {
		t.Fatalf("control Create citing an uncited document: %v", err)
	}
	if n := scInvoicesNumbered(t, f, "SC-2-CTRL"); n != 1 {
		t.Fatalf("control invoices = %d, want 1", n)
	}
	lineItemsOf := `SELECT count(*) FROM line_items l JOIN invoices i ON i.id = l.invoice_id WHERE i.tenant_id = $1 AND i.invoice_number = $2`
	if n := mustCount(t, f.super, lineItemsOf, f.tenant, "SC-2-CTRL"); n != 1 {
		t.Fatalf("control line items = %d, want 1 -- the counter below would report 0 for anything", n)
	}
	before := auditCount(t, f.app, f.tenant, "invoice.created")

	for i := 1; i <= 2; i++ {
		err := scCreate(f, f.limited, f.entA, "SC-2-N", &d)
		if !errors.Is(err, ErrValidation) || errors.Is(err, ErrDuplicateNumber) {
			t.Errorf("Create #%d citing B's document: err = %v, want ErrValidation (not ErrDuplicateNumber)", i, err)
		}
	}
	if n := scInvoicesNumbered(t, f, "SC-2-N"); n != 0 {
		t.Errorf("invoices written = %d, want 0", n)
	}
	if n := mustCount(t, f.super, lineItemsOf, f.tenant, "SC-2-N"); n != 0 {
		t.Errorf("line items written = %d, want 0", n)
	}
	if n := auditCount(t, f.app, f.tenant, "invoice.created"); n != before {
		t.Errorf("invoice.created rows = %d, want %d", n, before)
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE source_document_id = $1`, d); n != 1 {
		t.Errorf("invoices citing the document = %d, want 1 (B's)", n)
	}
}

func TestClientScope_CreateCitingADocumentLinkedOnlyByAnotherClientsBatchIsRefused(t *testing.T) {
	f := newCSFixture(t, "SC batch cite refused")
	d := seedDocument(t, f.super, f.tenant)
	scCiteByBatch(t, f.super, f.tenant, f.entB, d)

	err := scCreate(f, f.limited, f.entA, "SC-3-N", &d)
	if !errors.Is(err, ErrValidation) {
		t.Errorf("Create citing a document cited only by B's batch: err = %v, want ErrValidation", err)
	}
	if n := scInvoicesNumbered(t, f, "SC-3-N"); n != 0 {
		t.Errorf("invoices written = %d, want 0", n)
	}
}

func TestClientScope_RefusedCitationLeavesTheDocumentHidden(t *testing.T) {
	f := newCSFixture(t, "SC stays hidden")
	d := seedDocument(t, f.super, f.tenant)
	scCiteByInvoice(t, f.super, f.tenant, f.entB, d, "SC-B-CITE")
	scSeedExtractionRows(t, f.super, f.tenant, d)
	docs := document.NewStore(f.app)

	hidden := func(stage string) {
		t.Helper()
		if _, err := docs.GetLinked(f.limited, d); !errors.Is(err, document.ErrNotFound) {
			t.Errorf("%s: limited GetLinked err = %v, want document.ErrNotFound", stage, err)
		}
		if jobs, pages := scVisibleExtractionRows(t, f.app, f.limited, d); jobs != 0 || pages != 0 {
			t.Errorf("%s: limited member sees %d jobs / %d page images, want 0 / 0", stage, jobs, pages)
		}
	}
	hidden("before")
	if jobs, pages := scVisibleExtractionRows(t, f.app, f.admin, d); jobs != 1 || pages != 1 {
		t.Fatalf("admin sees %d jobs / %d page images, want 1 / 1", jobs, pages)
	}
	if _, err := docs.GetLinked(f.admin, d); err != nil {
		t.Fatalf("admin GetLinked: %v", err)
	}

	if code, body := scPost(f, f.limited, f.entA, "SC-4-N", &d); code != http.StatusBadRequest {
		t.Errorf("citing B's document: %d %s, want 400", code, body)
	}
	hidden("after")
}

func TestClientScope_LimitedMemberCitesAnUncitedDocument(t *testing.T) {
	f := newCSFixture(t, "SC uncited ok")
	d := seedDocument(t, f.super, f.tenant)

	err := scCreate(f, f.limited, f.entA, "SC-5-N", &d)
	if err != nil {
		t.Fatalf("Create citing an uncited document: %v", err)
	}
	assertCitedBy(t, f, "SC-5-N", d)
}

func TestClientScope_LimitedMemberCitesADocumentItsClientAlsoCites(t *testing.T) {
	f := newCSFixture(t, "SC own client cites")
	d := seedDocument(t, f.super, f.tenant)
	scCiteByInvoice(t, f.super, f.tenant, f.entA, d, "SC-A-CITE")
	scCiteByInvoiceLegacy(t, f.super, f.tenant, f.entB, d, "SC-B-CITE")

	if err := scCreate(f, f.limited, f.entA, "SC-6-N", &d); err != nil {
		t.Fatalf("Create citing a document A's invoice also cites: %v", err)
	}
	assertCitedBy(t, f, "SC-6-N", d)
}

func TestClientScope_EveryCallerCitesABoundDocumentOnlyForItsClient(t *testing.T) {
	f := newCSFixture(t, "SC bound cites")
	d := seedDocument(t, f.super, f.tenant)
	scCiteByInvoice(t, f.super, f.tenant, f.entB, d, "SC-B-CITE")
	unknown := uuid.NewString()

	scInHouse(t, f.super, f.tenant)
	callers := map[string]context.Context{
		"admin": f.admin, "all-clients member": scAllClientsMember(t, f.super, f.tenant), "in-house limited member": f.limited,
	}
	lineItems := `SELECT count(*) FROM line_items l JOIN invoices i ON i.id = l.invoice_id WHERE i.tenant_id = $1`
	invoices := `SELECT count(*) FROM invoices WHERE tenant_id = $1`
	wantInvoices, wantLines := mustCount(t, f.super, invoices, f.tenant), mustCount(t, f.super, lineItems, f.tenant)
	if wantInvoices == 0 {
		t.Fatalf("control: the tenant holds no invoice, the counters below would report 0 for anything")
	}
	before := auditCount(t, f.app, f.tenant, "invoice.created")

	for name, who := range callers {
		slug := strings.ReplaceAll(name, " ", "-")
		uCode, uBody := scPost(f, who, f.entA, "SC-7-U-"+slug, &unknown)
		if uCode != http.StatusBadRequest || uBody != scUnknownBody {
			t.Fatalf("%s citing an unknown id: %d %s, want 400 %s", name, uCode, uBody, scUnknownBody)
		}
		if code, body := scPost(f, who, f.entA, "SC-7-D-"+slug, &d); code != uCode || body != uBody {
			t.Errorf("%s citing B's document for A: %d %s, want %d %s (the unknown id's answer)", name, code, body, uCode, uBody)
		}
	}
	if n := mustCount(t, f.super, invoices, f.tenant); n != wantInvoices {
		t.Errorf("invoices = %d, want %d", n, wantInvoices)
	}
	if n := mustCount(t, f.super, lineItems, f.tenant); n != wantLines {
		t.Errorf("line items = %d, want %d", n, wantLines)
	}
	if n := auditCount(t, f.app, f.tenant, "invoice.created"); n != before {
		t.Errorf("invoice.created rows = %d, want %d", n, before)
	}
}

func TestClientScope_BoundDocumentWithATakenNumberAnswersAsAnUnknownOne(t *testing.T) {
	f := newCSFixture(t, "SC bound taken number")
	d := seedDocument(t, f.super, f.tenant)
	scCiteByInvoice(t, f.super, f.tenant, f.entB, d, "SC-B-CITE")

	// CS-A-1 is taken for A by the fixture.
	if code, body := scPost(f, f.admin, f.entA, "CS-A-1", &d); code != http.StatusBadRequest || body != scUnknownBody {
		t.Errorf("taken number citing B's document: %d %s, want 400 %s (not a 409)", code, body, scUnknownBody)
	}
}

func assertCitedBy(t *testing.T, f csFixture, number, doc string) {
	t.Helper()
	var ids []string
	rows, err := f.super.Query(context.Background(), `SELECT id FROM invoices WHERE tenant_id = $1 AND invoice_number = $2`, f.tenant, number)
	if err != nil {
		t.Fatalf("read invoice %s: %v", number, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 1 {
		t.Fatalf("invoices numbered %s = %d, want 1", number, len(ids))
	}
	if got := sourceDocumentOf(t, f.super, ids[0]); got == nil || *got != doc {
		t.Errorf("invoice %s source_document_id = %v, want %s", number, got, doc)
	}
}

func TestClientScope_CreateForAnOutsideClientStaysNotFoundWhateverItCites(t *testing.T) {
	f := newCSFixture(t, "SC outside order")
	d := seedDocument(t, f.super, f.tenant)
	scCiteByInvoice(t, f.super, f.tenant, f.entB, d, "SC-B-CITE")
	random := uuid.NewString()

	want := `{"error":"not found"}`
	for name, c := range map[string]struct {
		who           context.Context
		entity, doc   string
		invoiceNumber string
	}{
		"limited, entity B, unreadable document": {f.limited, f.entB, d, "SC-8-1"},
		"limited, entity B, unknown document":    {f.limited, f.entB, random, "SC-8-2"},
		"no clients, entity A, unreadable":       {f.unassigned, f.entA, d, "SC-8-3"},
	} {
		if code, body := scPost(f, c.who, c.entity, c.invoiceNumber, &c.doc); code != http.StatusNotFound || body != want {
			t.Errorf("%s: %d %s, want 404 %s", name, code, body, want)
		}
	}
}

func TestClientScope_StoreCreateWithAMalformedSourceDocumentIsValidation(t *testing.T) {
	f := newCSFixture(t, "SC malformed doc")
	bad := "x"
	if err := scCreate(f, f.limited, f.entA, "SC-9-N", &bad); !errors.Is(err, ErrValidation) {
		t.Errorf("Create with source document \"x\": err = %v, want ErrValidation", err)
	}
	if n := scInvoicesNumbered(t, f, "SC-9-N"); n != 0 {
		t.Errorf("invoices written = %d, want 0", n)
	}
}

func TestClientScope_LimitedMemberCreatesWithoutADocument(t *testing.T) {
	f := newCSFixture(t, "SC no document")
	inv, err := f.store.Create(f.limited, CreateInput{EntityID: f.entA, InvoiceNumber: "SC-10-N"})
	if err != nil {
		t.Fatalf("Create without a document: %v", err)
	}
	if got := sourceDocumentOf(t, f.super, inv.ID); got != nil {
		t.Errorf("source_document_id = %s, want NULL", *got)
	}
}

func TestClientScope_CreateCitingTheSameIdAnswersAlikeBeforeAndAfterItIsDeleted(t *testing.T) {
	f := newCSFixture(t, "SC same id deleted")
	d := seedDocument(t, f.super, f.tenant)
	b := scCiteByInvoice(t, f.super, f.tenant, f.entB, d, "SC-B-CITE")

	code1, body1 := scPost(f, f.limited, f.entA, "SC-11-1", &d)
	if code1 != http.StatusBadRequest {
		t.Fatalf("citing B's document while it exists: %d %s, want 400", code1, body1)
	}
	if _, err := f.super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, b); err != nil {
		t.Fatalf("delete B's citing invoice: %v", err)
	}
	if _, err := f.super.Exec(context.Background(), `DELETE FROM documents WHERE id = $1`, d); err != nil {
		t.Fatalf("delete document: %v", err)
	}
	code2, body2 := scPost(f, f.limited, f.entA, "SC-11-2", &d)
	if code2 != code1 || body2 != body1 {
		t.Errorf("after deletion: %d %s, want %d %s (same as while it existed)", code2, body2, code1, body1)
	}
	if n := scInvoicesNumbered(t, f, "SC-11-1", "SC-11-2"); n != 0 {
		t.Errorf("invoices written = %d, want 0", n)
	}
}

func TestClientScope_LimitedMemberCitesADocumentItsClientsBatchCites(t *testing.T) {
	f := newCSFixture(t, "SC own batch cites")
	d := seedDocument(t, f.super, f.tenant)
	scCiteByBatch(t, f.super, f.tenant, f.entA, d)

	if err := scCreate(f, f.limited, f.entA, "SC-12-N", &d); err != nil {
		t.Fatalf("Create citing a document A's batch cites: %v", err)
	}
	assertCitedBy(t, f, "SC-12-N", d)
}

func TestClientScope_UnreadableDocumentWithATakenNumberAnswersAsAnUnknownOne(t *testing.T) {
	f := newCSFixture(t, "SC taken number order")
	d := seedDocument(t, f.super, f.tenant)
	scCiteByInvoice(t, f.super, f.tenant, f.entB, d, "SC-B-CITE")
	unknown := uuid.NewString()

	uCode, uBody := scPost(f, f.limited, f.entA, "CS-A-1", &unknown)
	if uCode != http.StatusBadRequest || uBody != scUnknownBody {
		t.Errorf("taken number citing an unknown id: %d %s, want 400 %s", uCode, uBody, scUnknownBody)
	}
	if code, body := scPost(f, f.limited, f.entA, "CS-A-1", &d); code != http.StatusBadRequest || body != scUnknownBody {
		t.Errorf("taken number citing B's document: %d %s, want 400 %s (not a 409)", code, body, scUnknownBody)
	}
}

// scUpload stores a document through document.Store.Upsert under who's identity, so the seam and the uploader insert are exercised.
func scUpload(t *testing.T, f csFixture, who context.Context, seed string) string {
	t.Helper()
	return scUploadBytes(t, f, who, strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", ""))
}

// scUploadBytes uploads the content hash h; the same h from a second member is a dedupe hit.
func scUploadBytes(t *testing.T, f csFixture, who context.Context, h string) string {
	t.Helper()
	d, _, err := document.NewStore(f.app).Upsert(who, document.Document{
		StorageKey: document.StorageKey(f.tenant, h), ContentHash: h, SizeBytes: 11,
	})
	if err != nil {
		t.Fatalf("Upsert(%s): %v", h, err)
	}
	return d.ID
}

// scBinding reads documents.entity_id ("" is NULL) and the sorted uploader ids from document_uploaders as the superuser.
func scBinding(t *testing.T, f csFixture, doc string) (entity string, uploaders []string) {
	t.Helper()
	ctx := context.Background()
	var e *string
	if err := f.super.QueryRow(ctx, `SELECT entity_id::text FROM documents WHERE id = $1`, doc).Scan(&e); err != nil {
		t.Fatalf("read binding of %s: %v", doc, err)
	}
	if e != nil {
		entity = *e
	}
	rows, err := f.super.Query(ctx, `SELECT user_id::text FROM document_uploaders WHERE document_id = $1 ORDER BY 1`, doc)
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

func scSubject(t *testing.T, ctx context.Context) string {
	t.Helper()
	id, ok := auth.IdentityFromContext(ctx)
	if !ok {
		t.Fatal("context carries no identity")
	}
	return id.Subject
}

func TestClientScope_ALimitedMemberCannotCiteAnotherMembersUncitedUpload(t *testing.T) {
	f := newCSFixture(t, "SC uncited cite harm")
	u1 := csMember(t, f.super, f.tenant, "reviewer", f.entB)
	u2 := csMember(t, f.super, f.tenant, "reviewer", f.entA)
	d := scUpload(t, f, u1, "b-upload")
	unknown := uuid.NewString()

	if _, got := scBinding(t, f, d); !slices.Equal(got, []string{scSubject(t, u1)}) {
		t.Errorf("uploaders = %v after the upload, want [%s]", got, scSubject(t, u1))
	}
	before := mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, f.tenant)
	if before == 0 {
		t.Fatal("control: the tenant holds no invoice, the counter below would report 0 for anything")
	}

	uCode, uBody := scPost(f, u2, f.entA, "SC-UH-U", &unknown)
	if uCode != http.StatusBadRequest || uBody != scUnknownBody {
		t.Fatalf("citing an unknown id: %d %s, want 400 %s", uCode, uBody, scUnknownBody)
	}
	if code, body := scPost(f, u2, f.entA, "SC-UH-D", &d); code != uCode || body != uBody {
		t.Errorf("citing another member's uncited upload: %d %s, want %d %s (the unknown id's answer)", code, body, uCode, uBody)
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, f.tenant); n != before {
		t.Errorf("invoices = %d, want %d", n, before)
	}
	if entity, _ := scBinding(t, f, d); entity != "" {
		t.Errorf("documents.entity_id = %s after the refused citation, want NULL", entity)
	}
}

func TestClientScope_UploaderLegacyAndFullAccessCitationsOfAnUncitedDocumentBind(t *testing.T) {
	f := newCSFixture(t, "SC uncited cite ok")
	u := f.limited
	u2 := csMember(t, f.super, f.tenant, "reviewer", f.entA)
	u1 := csMember(t, f.super, f.tenant, "reviewer", f.entB)
	allClients := scAllClientsMember(t, f.super, f.tenant)

	doc1 := scUpload(t, f, u, "own-upload")
	legacy := seedDocument(t, f.super, f.tenant)
	doc3 := scUpload(t, f, u1, "b-upload")

	for _, c := range []struct {
		name, number, doc string
		who               context.Context
		uploaders         []string
	}{
		{"the uploader citing their own upload", "SC-OK-1", doc1, u, []string{scSubject(t, u)}},
		{"a limited member citing a legacy document", "SC-OK-2", legacy, u2, nil},
		{"an All clients member citing another member's upload", "SC-OK-3", doc3, allClients, []string{scSubject(t, u1)}},
	} {
		if code, body := scPost(f, c.who, f.entA, c.number, &c.doc); code != http.StatusCreated {
			t.Errorf("%s: %d %s, want 201", c.name, code, body)
		}
		entity, uploaders := scBinding(t, f, c.doc)
		if entity != f.entA {
			t.Errorf("%s: documents.entity_id = %q, want %s", c.name, entity, f.entA)
		}
		if !slices.Equal(uploaders, c.uploaders) {
			t.Errorf("%s: uploaders = %v, want %v", c.name, uploaders, c.uploaders)
		}
	}
}

func TestClientScope_ACoUploaderCitesTheirDedupedUpload(t *testing.T) {
	f := newCSFixture(t, "SC co-uploader cites")
	u1 := csMember(t, f.super, f.tenant, "reviewer", f.entA)
	u2 := csMember(t, f.super, f.tenant, "reviewer", f.entA)
	h := strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")

	d := scUploadBytes(t, f, u1, h)
	if again := scUploadBytes(t, f, u2, h); again != d {
		t.Fatalf("control: the same bytes from a second member made document %s, want %s", again, d)
	}

	if code, body := scPost(f, u2, f.entA, "SC-CO-1", &d); code != http.StatusCreated {
		t.Errorf("the co-uploader citing the deduped upload for A: %d %s, want 201", code, body)
	}
	entity, uploaders := scBinding(t, f, d)
	if entity != f.entA {
		t.Errorf("documents.entity_id = %q after the co-uploader's citation, want %s", entity, f.entA)
	}
	want := []string{scSubject(t, u1), scSubject(t, u2)}
	slices.Sort(want)
	if !slices.Equal(uploaders, want) {
		t.Errorf("uploaders = %v, want %v", uploaders, want)
	}
}

func TestClientScope_AMemberWhoNeverUploadedCannotCiteACoUploadedDocument(t *testing.T) {
	f := newCSFixture(t, "SC co-uploaded refused")
	u1 := csMember(t, f.super, f.tenant, "reviewer", f.entA)
	u2 := csMember(t, f.super, f.tenant, "reviewer", f.entA)
	u3 := csMember(t, f.super, f.tenant, "reviewer", f.entA)
	h := strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
	d := scUploadBytes(t, f, u1, h)
	scUploadBytes(t, f, u2, h)
	unknown := uuid.NewString()

	want := []string{scSubject(t, u1), scSubject(t, u2)}
	slices.Sort(want)
	if _, got := scBinding(t, f, d); !slices.Equal(got, want) {
		t.Fatalf("control: uploaders = %v, want %v (the refusal below must come from the uploader clause)", got, want)
	}
	before := mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, f.tenant)
	if before == 0 {
		t.Fatal("control: the tenant holds no invoice, the counter below would report 0 for anything")
	}

	uCode, uBody := scPost(f, u3, f.entA, "SC-CO-U", &unknown)
	if uCode != http.StatusBadRequest || uBody != scUnknownBody {
		t.Fatalf("citing an unknown id: %d %s, want 400 %s", uCode, uBody, scUnknownBody)
	}
	if code, body := scPost(f, u3, f.entA, "SC-CO-D", &d); code != uCode || body != uBody {
		t.Errorf("a member who never uploaded citing the co-uploaded document: %d %s, want %d %s (the unknown id's answer)", code, body, uCode, uBody)
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, f.tenant); n != before {
		t.Errorf("invoices = %d, want %d", n, before)
	}
	if entity, _ := scBinding(t, f, d); entity != "" {
		t.Errorf("documents.entity_id = %s after the refused citation, want NULL", entity)
	}
}

// The sentinel is pinned by identity, not by name: ErrSourceDocumentBound does not exist yet and
// naming it would stop the whole package compiling. The green step adds the errors.Is line.
func TestStoreCreate_BoundElsewhereIsItsOwnErrorAndKeepsTheValidationText(t *testing.T) {
	f := newCSFixture(t, "SC bound sentinel")
	d := seedDocument(t, f.super, f.tenant)
	scCiteByBatch(t, f.super, f.tenant, f.entB, d)
	if entity, _ := scBinding(t, f, d); entity != f.entB {
		t.Fatalf("control: documents.entity_id = %q, want %s", entity, f.entB)
	}
	badBatch := uuid.NewString()

	bound := scCreate(f, f.admin, f.entA, "SC-SENT-1", &d)
	boundAgain := scCreate(f, f.admin, f.entA, "SC-SENT-2", &d)
	_, otherErr := f.store.Create(f.admin, CreateInput{EntityID: f.entA, InvoiceNumber: "SC-SENT-3", ImportBatchID: &badBatch,
		LineItems: []LineItemInput{{Description: strPtr("SC line")}}})

	for name, err := range map[string]error{"bound elsewhere": bound, "another 23503": otherErr} {
		if !errors.Is(err, ErrValidation) || err.Error() != "invoice: validation" {
			t.Errorf("%s: err = %v, want ErrValidation with the text %q", name, err, "invoice: validation")
		}
	}
	if !errors.Is(bound, ErrSourceDocumentBound) || errors.Is(otherErr, ErrSourceDocumentBound) {
		t.Errorf("bound elsewhere: errors.Is(ErrSourceDocumentBound) = false, or another 23503 matches it (%v, %v)", bound, otherErr)
	}
	if bound == ErrValidation || !errors.Is(boundAgain, bound) {
		t.Errorf("bound elsewhere: err is the bare ErrValidation or not one stable sentinel (%v, %v), want its own sentinel wrapping ErrValidation", bound, boundAgain)
	}
	if otherErr != ErrValidation || errors.Is(otherErr, bound) {
		t.Errorf("another 23503: err = %v, want the bare ErrValidation, not the bound-elsewhere sentinel", otherErr)
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE source_document_id = $1`, d); n != 0 {
		t.Errorf("invoices citing the document = %d, want 0", n)
	}
}
