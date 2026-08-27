// source_document_qa_test.go: QA adversarial coverage for DOC-02-02
// (task-358) -- the orphaned-document 500 path, wire-level uploader
// rendering, cross-tenant sibling-count leak, a source_document_id set with
// source_rows never recorded (pre-DOC-02-01 imports), ordering stability
// across more siblings, and a NULL documents.filename. Reuses
// seedTenant/seedEntity/seedDocument/insertDocumentAuditRow/doSourceDocument
// from the executor's red-test file (same package).
package invoice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// corruptByDeletingDocument bypasses the invoices_tenant_source_document_fk
// RESTRICT trigger for one transaction only (SET LOCAL reverts on commit,
// never leaks to the pooled connection) to manufacture the state the
// composite FK is supposed to make impossible: a live source_document_id
// whose documents row is gone.
func corruptByDeletingDocument(t *testing.T, super *pgxpool.Pool, documentID string) {
	t.Helper()
	ctx := context.Background()
	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatalf("set session_replication_role: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM documents WHERE id = $1`, documentID); err != nil {
		t.Fatalf("delete documents: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// insertLeakedCrossTenantInvoice bypasses the same trigger to insert an
// invoice for tenantID whose source_document_id names a DIFFERENT tenant's
// document -- a state the FK exists to prevent, used only to prove the
// sibling-count/other-rows reads are RLS-scoped, not merely FK-scoped.
func insertLeakedCrossTenantInvoice(t *testing.T, super *pgxpool.Pool, tenantID, entityID, documentID string, sourceRows []int) string {
	t.Helper()
	ctx := context.Background()
	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatalf("set session_replication_role: %v", err)
	}
	var id string
	if err := tx.QueryRow(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id, source_rows)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		tenantID, entityID, "LEAK-"+uuid.NewString(), documentID, sourceRows,
	).Scan(&id); err != nil {
		t.Fatalf("insert leaked invoice: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return id
}

// QA-1 (Part 2 item 1): statement 2's pgx.ErrNoRows is deliberately unmapped
// -- a document row missing despite a live source_document_id is corruption
// the composite FK is supposed to make impossible, so it must surface as a
// 500, never a 404 (a 404 would mask the corruption as ordinary absence).
// None of the 16 red specs exercised this path.
func TestStoreSourceDocument_OrphanedDocumentReferenceIsInternalError(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-02 QA1 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-02 QA1 entity")
	documentID := seedDocument(t, super, tenantID)

	store := NewStore(app)
	identity := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}
	c := auth.WithIdentity(ctx, identity)
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "DOC-02-02-QA1", SourceDocumentID: &documentID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	corruptByDeletingDocument(t, super, documentID)

	_, err = store.SourceDocument(c, inv.ID)
	if err == nil {
		t.Fatal("SourceDocument err = nil, want a non-nil error for an orphaned source_document_id")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, must NOT be ErrNotFound -- the FK guarantees the document exists; a 404 here would mask corruption as ordinary absence", err)
	}
	if errors.Is(err, ErrValidation) {
		t.Errorf("err = %v, must NOT be ErrValidation", err)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("err = %v, want it to wrap pgx.ErrNoRows (statement 2's unmapped scan miss)", err)
	}

	rec := doSourceDocument(t, store.SourceDocument, &identity, inv.ID)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 for an orphaned document reference (body=%s)", rec.Code, rec.Body.String())
	}
}

// QA-2 (Part 2 item 2, wire-level): T6 (source_document_read_test.go) pins
// the STORE value; none of the 16 red specs check that a populated
// UploadedBy actually reaches the JSON wire. Also checks the DOC-01 retro
// well-formedness bar (uuid regex, not merely non-empty).
func TestSourceDocumentHandler_UploadedByIsCreatorOnWire(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-02 QA2 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-02 QA2 entity")
	documentID := seedDocument(t, super, tenantID)

	creator := uuid.NewString()
	reuser := uuid.NewString()
	insertDocumentAuditRow(t, super, tenantID, "document.created", creator, documentID)
	insertDocumentAuditRow(t, super, tenantID, "document.reused", reuser, documentID)

	store := NewStore(app)
	identity := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}
	c := auth.WithIdentity(ctx, identity)
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "DOC-02-02-QA2", SourceDocumentID: &documentID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := doSourceDocument(t, store.SourceDocument, &identity, inv.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Document struct {
			UploadedBy *string `json:"uploaded_by"`
		} `json:"document"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if body.Document.UploadedBy == nil || *body.Document.UploadedBy == "" {
		t.Fatalf("uploaded_by = %v, want the document.created actor %q", body.Document.UploadedBy, creator)
	}
	if !sourceDocumentUUIDRe.MatchString(*body.Document.UploadedBy) {
		t.Errorf("uploaded_by = %q, not a well-formed uuid", *body.Document.UploadedBy)
	}
	if *body.Document.UploadedBy != creator {
		t.Errorf("uploaded_by = %q, want %q (the document.created actor)", *body.Document.UploadedBy, creator)
	}
	if *body.Document.UploadedBy == reuser {
		t.Errorf("uploaded_by = %q, must not be the document.reused actor", *body.Document.UploadedBy)
	}
}

// QA-3 (Part 2 item 2, edge): a document with ONLY a document.reused row (no
// document.created -- e.g. a data anomaly, since Upsert always writes
// .created first) must still leave UploadedBy nil: the query filters on
// event, it does not fall back to the nearest audit row of any kind.
func TestStoreSourceDocument_OnlyReusedEventLeavesUploaderNil(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-02 QA3 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-02 QA3 entity")
	documentID := seedDocument(t, super, tenantID)
	insertDocumentAuditRow(t, super, tenantID, "document.reused", uuid.NewString(), documentID)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "DOC-02-02-QA3", SourceDocumentID: &documentID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.SourceDocument(c, inv.ID)
	if err != nil {
		t.Fatalf("SourceDocument: %v", err)
	}
	if got.Document == nil {
		t.Fatal("Document = nil, want a populated record")
	}
	if got.Document.UploadedBy != nil {
		t.Errorf("UploadedBy = %q, want nil -- only a document.reused row exists, no document.created", *got.Document.UploadedBy)
	}
}

// QA-4 (Part 2 item 3): sibling counts/rows must be RLS-scoped, not merely
// FK-scoped. insertLeakedCrossTenantInvoice manufactures the exact state the
// FK exists to prevent -- another tenant's invoice pointing at this
// document -- to prove the correlated subquery and the other-rows SELECT
// still exclude it under the reading tenant's own RLS session, rather than
// assuming the FK alone makes the leak impossible.
func TestRLS_SourceDocumentSiblingCountsExcludeCrossTenantLeak(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "DOC-02-02 QA4 tenant A")
	entityA := seedEntity(t, super, tenantA, "DOC-02-02 QA4 entity A")
	documentID := seedDocument(t, super, tenantA)

	tenantB := seedTenant(t, super, "DOC-02-02 QA4 tenant B")
	entityB := seedEntity(t, super, tenantB, "DOC-02-02 QA4 entity B")

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantA})
	inv1, err := store.Create(cA, CreateInput{EntityID: entityA, InvoiceNumber: "DOC-02-02-QA4-1", SourceDocumentID: &documentID, SourceRows: []int{2}})
	if err != nil {
		t.Fatalf("Create inv1: %v", err)
	}
	if _, err := store.Create(cA, CreateInput{EntityID: entityA, InvoiceNumber: "DOC-02-02-QA4-2", SourceDocumentID: &documentID, SourceRows: []int{5}}); err != nil {
		t.Fatalf("Create inv2: %v", err)
	}

	insertLeakedCrossTenantInvoice(t, super, tenantB, entityB, documentID, []int{99})

	got, err := store.SourceDocument(cA, inv1.ID)
	if err != nil {
		t.Fatalf("SourceDocument: %v", err)
	}
	if got.Document == nil {
		t.Fatal("Document = nil, want a populated record")
	}
	if got.Document.InvoicesCreated != 2 {
		t.Errorf("InvoicesCreated = %d, want 2 (tenant A's own 2 invoices, NOT the leaked tenant B row)", got.Document.InvoicesCreated)
	}
	if want := []int{5}; !intSliceEqual(got.Document.OtherInvoiceRows, want) {
		t.Errorf("OtherInvoiceRows = %v, want %v (the leaked tenant B row's [99] must not leak in)", got.Document.OtherInvoiceRows, want)
	}
}

// QA-5 (edge): an invoice imported before DOC-02-01 added source_rows has a
// real source_document_id but a NULL source_rows -- distinct from BOTH T1
// (both set) and T2 (both nil). T7 builds this exact fixture but never
// asserts on the outer SourceRows field; this closes that gap.
func TestStoreSourceDocument_DocumentSetButSourceRowsNeverRecorded(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-02 QA5 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-02 QA5 entity")
	documentID := seedDocument(t, super, tenantID)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "DOC-02-02-QA5", SourceDocumentID: &documentID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.SourceDocument(c, inv.ID)
	if err != nil {
		t.Fatalf("SourceDocument: %v", err)
	}
	if got.Document == nil {
		t.Fatal("Document = nil, want a populated record -- source_document_id is set")
	}
	if got.SourceRows != nil {
		t.Errorf("SourceRows = %v, want nil -- never recorded (pre-DOC-02-01 import)", got.SourceRows)
	}
}

// QA-6 (edge): ordering stability beyond T9's minimal 3-invoice case -- 6
// siblings inserted in scrambled first-row order, asked from a middle one.
func TestStoreSourceDocument_OtherInvoiceRowsStableOrderAcrossManySiblings(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-02 QA6 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-02 QA6 entity")
	documentID := seedDocument(t, super, tenantID)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	var target Invoice
	firsts := [][]int{{50}, {3}, {27}, {9}, {41}, {15}}
	for i, rows := range firsts {
		inv, err := store.Create(c, CreateInput{
			EntityID: entityID, InvoiceNumber: fmt.Sprintf("DOC-02-02-QA6-%d", i),
			SourceDocumentID: &documentID, SourceRows: rows,
		})
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		if rows[0] == 27 {
			target = inv
		}
	}

	got, err := store.SourceDocument(c, target.ID)
	if err != nil {
		t.Fatalf("SourceDocument: %v", err)
	}
	if got.Document == nil {
		t.Fatal("Document = nil, want a populated record")
	}
	want := []int{3, 9, 15, 41, 50}
	if !intSliceEqual(got.Document.OtherInvoiceRows, want) {
		t.Errorf("OtherInvoiceRows = %v, want %v (ascending, self excluded, stable across scrambled insert order)", got.Document.OtherInvoiceRows, want)
	}
}

// QA-7 (edge): documents.filename is nullable -- a document with none must
// render document.filename as an explicit JSON null on the wire, not omitted
// or coerced to "".
func TestSourceDocumentHandler_NullFilenameRendersExplicitlyOnWire(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-02 QA7 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-02 QA7 entity")
	documentID := seedDocument(t, super, tenantID) // filename left NULL

	store := NewStore(app)
	identity := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}
	c := auth.WithIdentity(ctx, identity)
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "DOC-02-02-QA7", SourceDocumentID: &documentID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := doSourceDocument(t, store.SourceDocument, &identity, inv.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	raw := rec.Body.Bytes()
	if !bytes.Contains(raw, []byte(`"filename":null`)) {
		t.Errorf("body = %s, want it to contain \"filename\":null", raw)
	}
}
