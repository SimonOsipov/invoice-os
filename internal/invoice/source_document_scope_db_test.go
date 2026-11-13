// source_document_scope_db_test.go: Store.Create cites only a document the caller can read
// (document.ReadableByScope). Every refusal fixture is refusable only by that clause: the
// entity is in the member's set and the document is in the same tenant, so the FK passes.
package invoice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	_, err := f.store.Create(who, CreateInput{EntityID: entity, InvoiceNumber: number, SourceDocumentID: doc})
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
	if n := mustCount(t, f.super, `SELECT count(*) FROM line_items l JOIN invoices i ON i.id = l.invoice_id WHERE i.tenant_id = $1 AND i.invoice_number = 'SC-2-N'`, f.tenant); n != 0 {
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
		t.Fatalf("citing B's document: %d %s, want 400", code, body)
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
	scCiteByInvoice(t, f.super, f.tenant, f.entB, d, "SC-B-CITE")

	if err := scCreate(f, f.limited, f.entA, "SC-6-N", &d); err != nil {
		t.Fatalf("Create citing a document A's invoice also cites: %v", err)
	}
	assertCitedBy(t, f, "SC-6-N", d)
}

func TestClientScope_FullAccessMembersCiteAnyClientsDocument(t *testing.T) {
	f := newCSFixture(t, "SC full access cites")
	d := seedDocument(t, f.super, f.tenant)
	scCiteByInvoice(t, f.super, f.tenant, f.entB, d, "SC-B-CITE")

	for name, who := range map[string]context.Context{"admin": f.admin, "all-clients member": scAllClientsMember(t, f.super, f.tenant)} {
		number := "SC-7-" + strings.ReplaceAll(name, " ", "-")
		if err := scCreate(f, who, f.entA, number, &d); err != nil {
			t.Errorf("%s citing B's document: %v", name, err)
			continue
		}
		assertCitedBy(t, f, number, d)
	}
	scInHouse(t, f.super, f.tenant)
	if err := scCreate(f, f.limited, f.entA, "SC-7-inhouse", &d); err != nil {
		t.Errorf("limited member of an in-house tenant citing B's document: %v", err)
		return
	}
	assertCitedBy(t, f, "SC-7-inhouse", d)
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
