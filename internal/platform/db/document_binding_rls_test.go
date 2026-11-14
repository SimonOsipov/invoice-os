// Storage suite for the document client binding: a document binds to the client of its first
// citation, a later citation for another client is refused, and one SQL rule (document_readable)
// drives the Go read path and the extraction policies.
//
// Rows are seeded as the superuser; the writes under test run as invoice_app.
package db_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const bindingMigrationGlob = "*_document_client_binding.sql"

var errBdRollback = errors.New("binding test: rollback requested")

type bdFix struct{ tenant, a, b string }

// bdWho is the caller's settings: nil means the setting is not written.
type bdWho struct{ scope, user *string }

func bdStr(s string) *string { return &s }

func bdSet(ids ...string) *string { return bdStr("{" + strings.Join(ids, ",") + "}") }

func bdExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("seed: %v\n%s", err, sql)
	}
}

func bdRequireMigration(t *testing.T) {
	t.Helper()
	var cols, fns int
	var uploaders *string
	if err := h.super.QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM information_schema.columns
		          WHERE table_schema = 'public' AND table_name = 'documents' AND column_name = 'entity_id'),
		        (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		          WHERE n.nspname = 'public' AND p.proname IN ('document_readable', 'bind_document_client', 'documents_binding_guard')),
		        to_regclass('public.document_uploaders')::text`,
	).Scan(&cols, &fns, &uploaders); err != nil {
		t.Fatalf("read the binding objects: %v", err)
	}
	if cols != 1 || fns != 3 || uploaders == nil {
		t.Fatalf("documents.entity_id %d (want 1), binding functions %d (want 3), document_uploaders %v (want present): the document_client_binding migration is not applied", cols, fns, uploaders)
	}
}

// bdSeed makes a throwaway tenant with clients A and B and sweeps its rows, children first.
func bdSeed(t *testing.T) *bdFix {
	t.Helper()
	bdRequireMigration(t)
	f := &bdFix{tenant: caThrowawayTenant(t), a: uuid.NewString(), b: uuid.NewString()}
	for i, e := range []string{f.a, f.b} {
		bdExec(t, `INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`, e, f.tenant, fmt.Sprintf("bd-client-%d", i))
	}
	t.Cleanup(func() {
		for _, tb := range []string{"extraction_page_images", "extraction_jobs", "invoices", "import_batches", "document_uploaders", "documents", "business_entities"} {
			_, _ = h.super.Exec(context.Background(), `DELETE FROM `+tb+` WHERE tenant_id = $1`, f.tenant)
		}
	})
	return f
}

// bdDoc inserts a document; an empty entity is NULL and an empty uploader adds no document_uploaders row.
func bdDoc(t *testing.T, f *bdFix, entity, uploader string) string {
	t.Helper()
	id := uuid.NewString()
	bdExec(t, `INSERT INTO documents (id, tenant_id, storage_key, content_hash, size_bytes, entity_id)
		VALUES ($1, $2, $3, $4, 1024, nullif($5, '')::uuid)`,
		id, f.tenant, "bd-"+uuid.NewString(), docHash(), entity)
	if uploader != "" {
		bdAddUploader(t, f, id, uploader)
	}
	return id
}

func bdAddUploader(t *testing.T, f *bdFix, doc, user string) {
	t.Helper()
	bdExec(t, `INSERT INTO document_uploaders (tenant_id, document_id, user_id) VALUES ($1, $2, $3)`, f.tenant, doc, user)
}

// bdApp opens the tenant transaction as invoice_app and writes the caller's settings the way the seam will.
func bdApp(tenant string, who bdWho, fn func(tx pgx.Tx) error) error {
	ctx := context.Background()
	return db.WithinTenantTx(ctx, h.app, tenant, func(tx pgx.Tx) error {
		if who.scope != nil {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.entity_scope', $1, true)`, *who.scope); err != nil {
				return err
			}
		}
		if who.user != nil {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.current_user_id', $1, true)`, *who.user); err != nil {
				return err
			}
		}
		return fn(tx)
	})
}

func bdInsertInvoice(tx pgx.Tx, tenant, entity, doc string) error {
	_, err := tx.Exec(context.Background(),
		`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, source_document_id)
		 VALUES ($1, $2, $3, $4, nullif($5, '')::uuid)`,
		uuid.NewString(), tenant, entity, "bd-"+uuid.NewString()[:8], doc)
	return err
}

func bdInsertBatch(tx pgx.Tx, tenant, entity, doc string) error {
	_, err := tx.Exec(context.Background(),
		`INSERT INTO import_batches (id, tenant_id, entity_id, document_id) VALUES ($1, $2, $3, nullif($4, '')::uuid)`,
		uuid.NewString(), tenant, entity, doc)
	return err
}

func bdCiteInvoice(f *bdFix, who bdWho, entity, doc string) error {
	return bdApp(f.tenant, who, func(tx pgx.Tx) error { return bdInsertInvoice(tx, f.tenant, entity, doc) })
}

func bdCiteBatch(f *bdFix, who bdWho, entity, doc string) error {
	return bdApp(f.tenant, who, func(tx pgx.Tx) error { return bdInsertBatch(tx, f.tenant, entity, doc) })
}

// bdBind binds an uncited document to the entity through a real citation.
func bdBind(t *testing.T, f *bdFix, entity, doc string) {
	t.Helper()
	if err := bdCiteInvoice(f, bdWho{scope: bdStr("*")}, entity, doc); err != nil {
		t.Fatalf("first citation of %s for %s: %v", doc, entity, err)
	}
}

func bdEntityOf(t *testing.T, doc string) *string {
	t.Helper()
	var e *string
	if err := h.super.QueryRow(context.Background(), `SELECT entity_id::text FROM documents WHERE id = $1`, doc).Scan(&e); err != nil {
		t.Fatalf("read documents.entity_id of %s: %v", doc, err)
	}
	return e
}

func bdWantEntity(t *testing.T, what, doc string, want *string) {
	t.Helper()
	got := bdEntityOf(t, doc)
	if (got == nil) != (want == nil) || (got != nil && *got != *want) {
		t.Errorf("%s: documents.entity_id = %v, want %v", what, bdShow(got), bdShow(want))
	}
}

func bdShow(s *string) string {
	if s == nil {
		return "NULL"
	}
	return *s
}

func bdCitations(t *testing.T, doc string) (invoices, batches int) {
	t.Helper()
	invoices = mustCount(t, h.super, `SELECT count(*) FROM invoices WHERE source_document_id = $1`, doc)
	batches = mustCount(t, h.super, `SELECT count(*) FROM import_batches WHERE document_id = $1`, doc)
	return invoices, batches
}

func bdWantRefused(t *testing.T, what string, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Errorf("%s: err = %v, want 23503 documents_bound_client", what, err)
		return
	}
	if pgErr.Code != "23503" || pgErr.ConstraintName != "documents_bound_client" {
		t.Errorf("%s: SQLSTATE %s constraint %q (%s), want 23503 documents_bound_client", what, pgErr.Code, pgErr.ConstraintName, pgErr.Message)
	}
}

func TestRLS_DocumentBindsToItsFirstInvoiceClient(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")
	bdWantEntity(t, "control, before any citation", doc, nil)

	if err := bdCiteInvoice(f, bdWho{scope: bdSet(f.a)}, f.a, doc); err != nil {
		t.Fatalf("invoice for A citing an uncited document: %v", err)
	}

	bdWantEntity(t, "after A's invoice", doc, &f.a)
}

func TestRLS_DocumentBindsToItsFirstBatchClient(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")
	bdWantEntity(t, "control, before any citation", doc, nil)

	if err := bdCiteBatch(f, bdWho{scope: bdStr("*")}, f.b, doc); err != nil {
		t.Fatalf("batch for B citing an uncited document: %v", err)
	}

	bdWantEntity(t, "after B's batch", doc, &f.b)
}

func TestRLS_BoundDocumentRefusesAnotherClientsInvoice(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")
	bdBind(t, f, f.a, doc)
	if inv, _ := bdCitations(t, doc); inv != 1 {
		t.Fatalf("control: invoices citing the document = %d, want 1 (A's)", inv)
	}

	for name, scope := range map[string]*string{"{B}": bdSet(f.b), "*": bdStr("*"), "unset": nil} {
		err := bdCiteInvoice(f, bdWho{scope: scope}, f.b, doc)
		bdWantRefused(t, "invoice for B under scope "+name, err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM invoices WHERE source_document_id = $1 AND entity_id = $2`, doc, f.b); n != 0 {
		t.Errorf("invoices for B citing the document = %d, want 0", n)
	}
	bdWantEntity(t, "after the refusals", doc, &f.a)
}

func TestRLS_BoundDocumentRefusesAnotherClientsBatch(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")
	bdBind(t, f, f.a, doc)

	err := bdCiteBatch(f, bdWho{scope: bdStr("*")}, f.b, doc)

	bdWantRefused(t, "batch for B under scope *", err)
	if n := mustCount(t, h.super, `SELECT count(*) FROM import_batches WHERE document_id = $1`, doc); n != 0 {
		t.Errorf("batches citing the document = %d, want 0", n)
	}
}

func TestRLS_BoundDocumentRefusesARepointedCitation(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")
	bdBind(t, f, f.a, doc)
	bInvoice, bBatch, aInvoice := uuid.NewString(), uuid.NewString(), ""
	bdExec(t, `INSERT INTO invoices (id, tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3, 'bd-b-uncited')`, bInvoice, f.tenant, f.b)
	bdExec(t, `INSERT INTO import_batches (id, tenant_id, entity_id) VALUES ($1, $2, $3)`, bBatch, f.tenant, f.b)
	if err := h.super.QueryRow(context.Background(),
		`SELECT id::text FROM invoices WHERE source_document_id = $1`, doc).Scan(&aInvoice); err != nil {
		t.Fatalf("read A's citing invoice: %v", err)
	}
	all := bdWho{scope: bdStr("*")}

	err := bdApp(f.tenant, all, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE invoices SET source_document_id = $1 WHERE id = $2`, doc, bInvoice)
		return err
	})
	bdWantRefused(t, "pointing B's uncited invoice at the document", err)
	err = bdApp(f.tenant, all, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE import_batches SET document_id = $1 WHERE id = $2`, doc, bBatch)
		return err
	})
	bdWantRefused(t, "pointing B's uncited batch at the document", err)
	err = bdApp(f.tenant, all, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE invoices SET entity_id = $1 WHERE id = $2`, f.b, aInvoice)
		return err
	})
	bdWantRefused(t, "moving A's citing invoice to B", err)

	if n := mustCount(t, h.super, `SELECT count(*) FROM invoices WHERE id = $1 AND source_document_id IS NULL`, bInvoice); n != 1 {
		t.Errorf("B's invoice still uncited = %d, want 1", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM invoices WHERE id = $1 AND entity_id = $2`, aInvoice, f.a); n != 1 {
		t.Errorf("A's citing invoice still under A = %d, want 1", n)
	}
	bdWantEntity(t, "after the refusals", doc, &f.a)
}

func TestRLS_BoundDocumentAcceptsItsOwnClient(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")
	bdBind(t, f, f.a, doc)
	a := bdWho{scope: bdSet(f.a)}

	if err := bdCiteInvoice(f, a, f.a, doc); err != nil {
		t.Errorf("second invoice for A: %v", err)
	}
	if err := bdCiteBatch(f, a, f.a, doc); err != nil {
		t.Errorf("batch for A: %v", err)
	}

	bdWantEntity(t, "after A's repeat citations", doc, &f.a)
	if inv, batch := bdCitations(t, doc); inv != 2 || batch != 1 {
		t.Errorf("citations = %d invoices, %d batches, want 2 and 1", inv, batch)
	}
}

func TestRLS_CitationWithoutADocumentBindsNothing(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")
	bound := bdDoc(t, f, f.b, "")
	all := bdWho{scope: bdStr("*")}

	if err := bdCiteInvoice(f, all, f.a, ""); err != nil {
		t.Errorf("invoice with no document: %v", err)
	}
	if err := bdCiteBatch(f, all, f.a, ""); err != nil {
		t.Errorf("batch with no document: %v", err)
	}

	bdWantEntity(t, "an uncited document", doc, nil)
	bdWantEntity(t, "a document bound to B", bound, &f.b)
	if n := mustCount(t, h.super, `SELECT count(*) FROM invoices WHERE tenant_id = $1 AND source_document_id IS NULL`, f.tenant); n != 1 {
		t.Errorf("document-less invoices = %d, want 1", n)
	}
}

// bdRace holds tx1's citation for e1 open, runs tx2's citation for e2 as who2 until it waits on the document row,
// then ends tx1. blocked is false when tx2 never waited.
func bdRace(t *testing.T, f *bdFix, doc, e1, e2 string, commit1 bool, who2 bdWho) (err1, err2 error, blocked bool) {
	t.Helper()
	ctx := context.Background()
	inserted, release := make(chan struct{}), make(chan bool, 1)
	pid2 := make(chan int, 1)
	done1, done2 := make(chan error, 1), make(chan error, 1)

	go func() {
		done1 <- bdApp(f.tenant, bdWho{}, func(tx pgx.Tx) error {
			err := bdInsertInvoice(tx, f.tenant, e1, doc)
			close(inserted)
			if err != nil {
				return err
			}
			if !<-release {
				return errBdRollback
			}
			return nil
		})
	}()
	select {
	case <-inserted:
	case <-time.After(15 * time.Second):
		t.Fatalf("tx1 never inserted its citation")
	}
	go func() {
		done2 <- bdApp(f.tenant, who2, func(tx pgx.Tx) error {
			var pid int
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				return err
			}
			pid2 <- pid
			return bdInsertInvoice(tx, f.tenant, e2, doc)
		})
	}()
	var pid int
	select {
	case pid = <-pid2:
	case err2 = <-done2:
		release <- commit1
		return <-done1, err2, false
	case <-time.After(15 * time.Second):
		release <- commit1
		t.Fatalf("tx2 never reported its backend")
	}

	early := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && !blocked && !early; {
		var wait *string
		if err := h.super.QueryRow(ctx, `SELECT wait_event_type FROM pg_stat_activity WHERE pid = $1`, pid).Scan(&wait); err != nil {
			release <- commit1
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		blocked = wait != nil && *wait == "Lock"
		if !blocked {
			select {
			case err2 = <-done2:
				early = true
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	release <- commit1
	err1 = <-done1
	if !early {
		select {
		case err2 = <-done2:
		case <-time.After(15 * time.Second):
			t.Fatalf("tx2 did not finish after tx1 ended")
		}
	}
	return err1, err2, blocked
}

func TestRLS_ConcurrentFirstCitationsBindOneClient(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")

	err1, err2, blocked := bdRace(t, f, doc, f.a, f.b, true, bdWho{})

	if !blocked {
		t.Error("tx2 did not wait on the document row while tx1's first citation was open")
	}
	if err1 != nil {
		t.Errorf("the winner's citation for A: %v", err1)
	}
	bdWantRefused(t, "the loser's citation for B", err2)
	bdWantEntity(t, "after the race", doc, &f.a)
	if inv, batch := bdCitations(t, doc); inv != 1 || batch != 0 {
		t.Errorf("citations = %d invoices, %d batches, want 1 and 0", inv, batch)
	}
}

func TestRLS_ConcurrentFirstCitationBindsWhenTheWinnerRollsBack(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")

	err1, err2, blocked := bdRace(t, f, doc, f.a, f.b, false, bdWho{})

	if !blocked {
		t.Error("tx2 did not wait on the document row while tx1's first citation was open")
	}
	if !errors.Is(err1, errBdRollback) {
		t.Errorf("tx1 ended with %v, want its requested rollback", err1)
	}
	if err2 != nil {
		t.Errorf("the second citation for B after the winner rolled back: %v", err2)
	}
	bdWantEntity(t, "after the rollback", doc, &f.b)
	if inv, _ := bdCitations(t, doc); inv != 1 {
		t.Errorf("invoices citing the document = %d, want 1 (B's)", inv)
	}
}

func TestRLS_ConcurrentFirstCitationsForOneClientBothSucceed(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")

	err1, err2, blocked := bdRace(t, f, doc, f.a, f.a, true, bdWho{})

	if !blocked {
		t.Error("tx2 did not wait on the document row while tx1's first citation was open")
	}
	if err1 != nil || err2 != nil {
		t.Errorf("citations for A: %v / %v, want both to succeed", err1, err2)
	}
	bdWantEntity(t, "after the race", doc, &f.a)
	if inv, _ := bdCitations(t, doc); inv != 2 {
		t.Errorf("invoices citing the document = %d, want 2", inv)
	}
}

// bdRaceNonUploader: the document has one uploader (not the caller); tx1 cites it with full access and holds,
// tx2 is a member limited to A who never uploaded it.
func bdRaceNonUploader(t *testing.T, e2 func(f *bdFix) string, commit1 bool) (f *bdFix, doc string, err1, err2 error, blocked bool) {
	t.Helper()
	f = bdSeed(t)
	doc = bdDoc(t, f, "", uuid.NewString())
	err1, err2, blocked = bdRace(t, f, doc, f.a, e2(f), commit1, bdWho{scope: bdSet(f.a), user: bdStr(uuid.NewString())})
	return f, doc, err1, err2, blocked
}

func TestRLS_ConcurrentCitationByANonUploaderWaitsForTheBinding(t *testing.T) {
	requireHarness(t)

	f, doc, err1, err2, blocked := bdRaceNonUploader(t, func(f *bdFix) string { return f.a }, true)

	if !blocked {
		t.Error("tx2 did not wait on the document row while tx1's first citation was open")
	}
	if err1 != nil {
		t.Errorf("the binder's citation for A: %v", err1)
	}
	if err2 != nil {
		t.Errorf("the non-uploader's citation for A after the binder committed: %v", err2)
	}
	bdWantEntity(t, "after the race", doc, &f.a)
	if inv, batch := bdCitations(t, doc); inv != 2 || batch != 0 {
		t.Errorf("citations = %d invoices, %d batches, want 2 and 0", inv, batch)
	}
}

func TestRLS_ConcurrentCitationByANonUploaderForAnotherClientIsRefusedAfterTheWait(t *testing.T) {
	requireHarness(t)

	f, doc, err1, err2, blocked := bdRaceNonUploader(t, func(f *bdFix) string { return f.b }, true)

	if !blocked {
		t.Error("tx2 did not wait on the document row while tx1's first citation was open")
	}
	if err1 != nil {
		t.Errorf("the binder's citation for A: %v", err1)
	}
	bdWantRefused(t, "the non-uploader's citation for B", err2)
	bdWantEntity(t, "after the race", doc, &f.a)
	if inv, batch := bdCitations(t, doc); inv != 1 || batch != 0 {
		t.Errorf("citations = %d invoices, %d batches, want 1 and 0", inv, batch)
	}
}

func TestRLS_ConcurrentCitationByANonUploaderIsRefusedWhenTheWinnerRollsBack(t *testing.T) {
	requireHarness(t)

	_, doc, err1, err2, blocked := bdRaceNonUploader(t, func(f *bdFix) string { return f.a }, false)

	if !blocked {
		t.Error("tx2 did not wait on the document row while tx1's first citation was open")
	}
	if !errors.Is(err1, errBdRollback) {
		t.Errorf("tx1 ended with %v, want its requested rollback", err1)
	}
	bdWantRefused(t, "the non-uploader's citation after the binder rolled back", err2)
	bdWantEntity(t, "after the rollback", doc, nil)
	if inv, batch := bdCitations(t, doc); inv != 0 || batch != 0 {
		t.Errorf("citations = %d invoices, %d batches, want 0 and 0", inv, batch)
	}
}

// The other order: tx2 is the uploader, so the bind predicate is true for them and they queue on the same row lock.
func TestRLS_ConcurrentCitationByTheUploaderFollowsTheBindingAfterTheWait(t *testing.T) {
	requireHarness(t)

	for name, c := range map[string]struct {
		e2     func(f *bdFix) string
		refuse bool
	}{
		"for the bound client": {func(f *bdFix) string { return f.a }, false},
		"for another client":   {func(f *bdFix) string { return f.b }, true},
	} {
		f := bdSeed(t)
		uploader := uuid.NewString()
		doc := bdDoc(t, f, "", uploader)
		err1, err2, blocked := bdRace(t, f, doc, f.a, c.e2(f), true, bdWho{scope: bdSet(f.a), user: &uploader})
		if !blocked {
			t.Errorf("%s: tx2 did not wait on the document row while tx1's first citation was open", name)
		}
		if err1 != nil {
			t.Errorf("%s: the binder's citation for A: %v", name, err1)
		}
		if c.refuse {
			bdWantRefused(t, name+": the uploader's citation", err2)
		} else if err2 != nil {
			t.Errorf("%s: the uploader's citation after the binder committed: %v", name, err2)
		}
		bdWantEntity(t, name+": after the race", doc, &f.a)
	}
}

func TestRLS_AppCannotRebindADocument(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", uuid.NewString())
	bdBind(t, f, f.a, doc)
	update := func(sql string, args ...any) error {
		return bdApp(f.tenant, bdWho{}, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), sql, args...)
			return err
		})
	}

	for name, c := range map[string]struct {
		sql, code string
		args      []any
	}{
		"entity_id to B":     {`UPDATE documents SET entity_id = $2 WHERE id = $1`, "23001", []any{doc, f.b}},
		"entity_id to NULL":  {`UPDATE documents SET entity_id = NULL WHERE id = $1`, "23001", []any{doc}},
		"filename":           {`UPDATE documents SET filename = 'x' WHERE id = $1`, "42501", []any{doc}},
		"an uploader row":    {`UPDATE document_uploaders SET user_id = $2 WHERE document_id = $1`, "42501", []any{doc, uuid.NewString()}},
		"an uploader delete": {`DELETE FROM document_uploaders WHERE document_id = $1`, "42501", []any{doc}},
	} {
		err := update(c.sql, c.args...)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != c.code {
			t.Errorf("invoice_app UPDATE of %s: err = %v, want SQLSTATE %s", name, err, c.code)
		}
	}
	bdWantEntity(t, "after the refused updates", doc, &f.a)
	if n := mustCount(t, h.super, `SELECT count(*) FROM document_uploaders WHERE document_id = $1`, doc); n != 1 {
		t.Errorf("document_uploaders rows after the refused UPDATE and DELETE = %d, want 1", n)
	}
}

// bdMatrix is one document of each read shape, with one job and one page image each.
type bdMatrix struct {
	f    *bdFix
	user string
	docs map[string]string
}

var bdDocNames = []string{"uncited", "boundA", "boundB", "boundBByUploader", "legacyBoundBCitedByA", "legacyBoundBCitedByABatch"}

func bdSeedMatrix(t *testing.T) *bdMatrix {
	t.Helper()
	f := bdSeed(t)
	m := &bdMatrix{f: f, user: uuid.NewString(), docs: map[string]string{}}
	m.docs["uncited"] = bdDoc(t, f, "", "")
	m.docs["boundA"] = bdDoc(t, f, f.a, "")
	m.docs["boundB"] = bdDoc(t, f, f.b, "")
	m.docs["boundBByUploader"] = bdDoc(t, f, f.b, m.user)
	m.docs["legacyBoundBCitedByA"] = bdDoc(t, f, f.b, "")
	cpLegacyExec(t, `INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, $3, 'bd-legacy-a', $4)`,
		uuid.NewString(), f.tenant, f.a, m.docs["legacyBoundBCitedByA"])
	m.docs["legacyBoundBCitedByABatch"] = bdDoc(t, f, f.b, "")
	cpLegacyExec(t, `INSERT INTO import_batches (id, tenant_id, entity_id, document_id) VALUES ($1, $2, $3, $4)`,
		uuid.NewString(), f.tenant, f.a, m.docs["legacyBoundBCitedByABatch"])
	for _, name := range bdDocNames {
		cpExtractionRows(t, f.tenant, m.docs[name])
	}
	return m
}

type bdCell struct{ goRule, job, page bool }

// bdRead reads one document through the Go rule and through both extraction policies.
func bdRead(t *testing.T, f *bdFix, who bdWho, doc string) bdCell {
	t.Helper()
	var c bdCell
	err := bdApp(f.tenant, who, func(tx pgx.Tx) error {
		ctx := context.Background()
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM documents d WHERE d.id = $1 AND `+document.ReadableByScope+`)`, doc).Scan(&c.goRule); err != nil {
			return err
		}
		var jobs, pages int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM extraction_jobs WHERE document_id = $1`, doc).Scan(&jobs); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM extraction_page_images WHERE document_id = $1`, doc).Scan(&pages); err != nil {
			return err
		}
		c.job, c.page = jobs > 0, pages > 0
		return nil
	})
	if err != nil {
		t.Fatalf("read document %s (scope %s, user %s): %v", doc, bdShowPtr(who.scope), bdShowPtr(who.user), err)
	}
	return c
}

func bdShowPtr(s *string) string {
	if s == nil {
		return "<unset>"
	}
	return fmt.Sprintf("%q", *s)
}

// bdWantReadable is the rule's truth table. scope is "" for every unset or empty or star scope.
func bdWantReadable(name, scopeKind, caller, uploader string) bool {
	switch scopeKind {
	case "full":
		return true
	case "A":
		switch name {
		case "uncited", "boundA", "legacyBoundBCitedByA", "legacyBoundBCitedByABatch":
			return true
		case "boundBByUploader":
			return caller == uploader
		}
	case "none":
		switch name {
		case "uncited":
			return true
		case "boundBByUploader":
			return caller == uploader
		}
	}
	return false
}

type bdScopeCase struct {
	label string
	kind  string
	scope func(f *bdFix) *string
}

var bdScopeCases = []bdScopeCase{
	{"{A}", "A", func(f *bdFix) *string { return bdSet(f.a) }},
	{"{}", "none", func(*bdFix) *string { return bdStr("{}") }},
	{"*", "full", func(*bdFix) *string { return bdStr("*") }},
	{"unset", "full", func(*bdFix) *string { return nil }},
	{"empty string", "full", func(*bdFix) *string { return bdStr("") }},
}

// bdUsers returns the caller settings to cross with the scopes: the uploader, another member, nobody, empty.
func bdUsers(m *bdMatrix) map[string]*string {
	return map[string]*string{"uploader": bdStr(m.user), "other member": bdStr(uuid.NewString()), "unset": nil, "empty": bdStr("")}
}

func bdCallerOf(m *bdMatrix, label string) string {
	if label == "uploader" {
		return m.user
	}
	return "someone else"
}

func TestRLS_DocumentRuleAgreesAcrossGoAndPolicies(t *testing.T) {
	requireHarness(t)
	m := bdSeedMatrix(t)

	readable, hidden := 0, 0
	for _, sc := range bdScopeCases {
		for ulabel, user := range bdUsers(m) {
			for _, name := range bdDocNames {
				c := bdRead(t, m.f, bdWho{scope: sc.scope(m.f), user: user}, m.docs[name])
				if c.goRule != c.job || c.goRule != c.page {
					t.Errorf("%s, scope %s, caller %s: Go rule %v, extraction job %v, page image %v, want one answer",
						name, sc.label, ulabel, c.goRule, c.job, c.page)
				}
				if c.goRule {
					readable++
				} else {
					hidden++
				}
			}
		}
	}
	if readable == 0 || hidden == 0 {
		t.Errorf("the matrix reads %d cells and hides %d, want both non-zero", readable, hidden)
	}

	// Two uploaders of a document bound to B: both read it under scope {A}, a third caller does not.
	u2, u3 := uuid.NewString(), uuid.NewString()
	shared := bdDoc(t, m.f, m.f.b, m.user)
	bdAddUploader(t, m.f, shared, u2)
	cpExtractionRows(t, m.f.tenant, shared)
	for who, c := range map[string]struct {
		user string
		want bool
	}{"U": {m.user, true}, "U2": {u2, true}, "U3": {u3, false}} {
		got := bdRead(t, m.f, bdWho{scope: bdSet(m.f.a), user: bdStr(c.user)}, shared)
		if got != (bdCell{c.want, c.want, c.want}) {
			t.Errorf("document bound to B with uploaders U and U2, caller %s under {A}: Go rule %v, job %v, page image %v, want %v for all", who, got.goRule, got.job, got.page, c.want)
		}
	}
}

func TestRLS_LimitedScopeReadsByUploaderClientOrCitation(t *testing.T) {
	requireHarness(t)
	m := bdSeedMatrix(t)

	for _, sc := range bdScopeCases {
		for ulabel, user := range bdUsers(m) {
			for _, name := range bdDocNames {
				want := bdWantReadable(name, sc.kind, bdCallerOf(m, ulabel), m.user)
				c := bdRead(t, m.f, bdWho{scope: sc.scope(m.f), user: user}, m.docs[name])
				if c != (bdCell{want, want, want}) {
					t.Errorf("%s, scope %s, caller %s: Go rule %v, job %v, page image %v, want %v for all",
						name, sc.label, ulabel, c.goRule, c.job, c.page, want)
				}
			}
		}
	}
}

func TestRLS_DocumentUploadersIsTenantIsolated(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	f, g := bdSeed(t), bdSeed(t)
	userT, userU := uuid.NewString(), uuid.NewString()
	docT, docU := bdDoc(t, f, "", userT), bdDoc(t, g, "", userU)

	var rls, forced bool
	if err := h.super.QueryRow(ctx, `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = 'public.document_uploaders'::regclass`).Scan(&rls, &forced); err != nil {
		t.Fatalf("read the RLS flags: %v", err)
	}
	if !rls || !forced {
		t.Errorf("document_uploaders relrowsecurity %v, relforcerowsecurity %v, want both true", rls, forced)
	}

	for role, pool := range map[string]*pgxpool.Pool{"invoice_app": h.app, "invoice_migrator": h.mig} {
		err := db.WithinTenantTx(ctx, pool, f.tenant, func(tx pgx.Tx) error {
			if n := mustCount(t, tx, `SELECT count(*) FROM document_uploaders WHERE document_id = $1`, docT); n != 1 {
				t.Errorf("%s under tenant T sees %d of T's rows, want 1", role, n)
			}
			if n := mustCount(t, tx, `SELECT count(*) FROM document_uploaders`); n != 1 {
				t.Errorf("%s under tenant T sees %d rows in all, want only T's 1", role, n)
			}
			if n := mustCount(t, tx, `SELECT count(*) FROM document_uploaders WHERE tenant_id = $1`, g.tenant); n != 0 {
				t.Errorf("%s under tenant T sees %d of U's rows, want 0", role, n)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("%s under tenant T: %v", role, err)
		}
	}

	err := db.WithinTenantTx(ctx, h.app, f.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO document_uploaders (tenant_id, document_id, user_id) VALUES ($1, $2, $3)`, g.tenant, docU, uuid.NewString())
		return err
	})
	assertRLSViolation(t, err)
	if err := db.WithinTenantTx(ctx, h.app, f.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO document_uploaders (tenant_id, document_id, user_id) VALUES ($1, $2, $3)`, f.tenant, docT, uuid.NewString())
		return err
	}); err != nil {
		t.Errorf("control: invoice_app under T inserting a T row: %v", err)
	}
	// A T row that names tenant U's document passes the policy and must still fail the composite foreign key.
	err = db.WithinTenantTx(ctx, h.app, f.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO document_uploaders (tenant_id, document_id, user_id) VALUES ($1, $2, $3)`, f.tenant, docU, uuid.NewString())
		return err
	})
	var fkErr *pgconn.PgError
	if !errors.As(err, &fkErr) || fkErr.Code != "23503" {
		t.Errorf("invoice_app under T inserting a T row for U's document: err = %v, want SQLSTATE 23503", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM document_uploaders WHERE document_id = $1`, docU); n != 1 {
		t.Errorf("uploader rows of U's document = %d, want only U's own 1", n)
	}

	if n := mustCount(t, h.app, `SELECT count(*) FROM document_uploaders`); n != 0 {
		t.Errorf("invoice_app with no tenant set sees %d rows, want 0", n)
	}
	for name, c := range map[string]struct {
		sql  string
		args []any
	}{
		"UPDATE": {`UPDATE document_uploaders SET user_id = $2 WHERE document_id = $1`, []any{docT, uuid.NewString()}},
		"DELETE": {`DELETE FROM document_uploaders WHERE document_id = $1`, []any{docT}},
	} {
		err := db.WithinTenantTx(ctx, h.app, f.tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, c.sql, c.args...)
			return err
		})
		if !isRLSViolation(err) {
			t.Errorf("invoice_app %s on document_uploaders: err = %v, want SQLSTATE 42501", name, err)
		}
	}
}

func TestRLS_ANullUploaderGrantsNoRead(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, f.b, "")
	cpExtractionRows(t, f.tenant, doc)
	if c := bdRead(t, f, bdWho{scope: bdStr("*")}, doc); c != (bdCell{true, true, true}) {
		t.Fatalf("control: scope * reads the document as %+v, want readable through all three", c)
	}

	for name, user := range map[string]*string{"unset": nil, "empty": bdStr(""), "a uuid": bdStr(uuid.NewString())} {
		c := bdRead(t, f, bdWho{scope: bdSet(f.a), user: user}, doc)
		if c != (bdCell{}) {
			t.Errorf("scope {A}, current user %s, document bound to B with no uploader: %+v, want hidden through all three", name, c)
		}
	}
}

func TestRLS_FullAccessReadsEveryBoundDocument(t *testing.T) {
	requireHarness(t)
	m := bdSeedMatrix(t)

	for label, scope := range map[string]*string{"*": bdStr("*"), "unset": nil} {
		for _, name := range bdDocNames {
			if c := bdRead(t, m.f, bdWho{scope: scope}, m.docs[name]); c != (bdCell{true, true, true}) {
				t.Errorf("%s under scope %s: Go rule %v, job %v, page image %v, want readable through all three", name, label, c.goRule, c.job, c.page)
			}
		}
	}
}

func TestRLS_DocumentReadableIsExecutableByTheAppRoleOnly(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	var reg *string
	if err := h.super.QueryRow(ctx, `SELECT to_regprocedure('public.document_readable(uuid)')::text`).Scan(&reg); err != nil {
		t.Fatalf("read to_regprocedure: %v", err)
	}
	if reg == nil {
		t.Fatal("public.document_readable(uuid) does not exist: the document_client_binding migration is not applied")
	}

	var app, reader, public bool
	if err := h.super.QueryRow(ctx,
		`SELECT has_function_privilege('invoice_app', 'public.document_readable(uuid)', 'EXECUTE'),
		        has_function_privilege('invoice_tenant_reader', 'public.document_readable(uuid)', 'EXECUTE'),
		        EXISTS (SELECT 1 FROM pg_proc p, aclexplode(coalesce(p.proacl, acldefault('f', p.proowner))) a
		                 WHERE p.oid = 'public.document_readable(uuid)'::regprocedure AND a.grantee = 0 AND a.privilege_type = 'EXECUTE')`,
	).Scan(&app, &reader, &public); err != nil {
		t.Fatalf("read function privileges: %v", err)
	}
	if !app || reader || public {
		t.Errorf("EXECUTE on document_readable: invoice_app %v, invoice_tenant_reader %v, PUBLIC %v, want true, false, false", app, reader, public)
	}
}

// bdDocumentShape reports documents.entity_id, documents.uploaded_by and the table document_uploaders.
func bdDocumentShape(t *testing.T) (entityID, uploadedBy, uploaders bool) {
	t.Helper()
	if err := h.super.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'documents' AND column_name = 'entity_id'),
		        EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'documents' AND column_name = 'uploaded_by'),
		        to_regclass('public.document_uploaders') IS NOT NULL`,
	).Scan(&entityID, &uploadedBy, &uploaders); err != nil {
		t.Fatalf("read the document shape: %v", err)
	}
	return entityID, uploadedBy, uploaders
}

func bdBindingFunctions(t *testing.T) int {
	t.Helper()
	return mustCount(t, h.super,
		`SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		  WHERE n.nspname = 'public' AND p.proname IN ('document_readable', 'bind_document_client', 'documents_binding_guard')`)
}

// bdPolicyText returns pg_get_expr of the tenant_isolation policy of each extraction table.
func bdPolicyText(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range []string{"extraction_jobs", "extraction_page_images"} {
		var expr string
		if err := h.super.QueryRow(context.Background(),
			`SELECT pg_get_expr(polqual, polrelid) FROM pg_policy WHERE polrelid = $1::regclass AND polname = 'tenant_isolation'`, "public."+table,
		).Scan(&expr); err != nil {
			t.Fatalf("read the tenant_isolation policy of %s: %v", table, err)
		}
		out[table] = expr
	}
	return out
}

func TestRLS_DocumentBindingMigrationDownRestoresChildScope(t *testing.T) {
	requireHarness(t)
	v := migrationVersion(t, bindingMigrationGlob)
	reapply := rollBackMigrations(t, staffMigrationProvider(t), v)

	if e, c, u := bdDocumentShape(t); e || c || u {
		t.Errorf("after the Down: documents.entity_id %v, documents.uploaded_by %v, document_uploaders %v, want all absent", e, c, u)
	}
	if n := bdBindingFunctions(t); n != 0 {
		t.Errorf("after the Down: %d of document_readable, bind_document_client, documents_binding_guard remain, want 0", n)
	}
	policies := bdPolicyText(t)
	for table, expr := range policies {
		if !strings.Contains(expr, "document_link_state") || strings.Contains(expr, "document_readable") {
			t.Errorf("after the Down: %s policy = %s, want the child_scope text (document_link_state, no document_readable)", table, expr)
		}
	}
	if len(policies) != 2 {
		t.Fatalf("read %d extraction policies, want 2", len(policies))
	}

	reapply()

	if e, c, u := bdDocumentShape(t); !e || c || !u {
		t.Errorf("after the Up: documents.entity_id %v, documents.uploaded_by %v, document_uploaders %v, want true, false, true", e, c, u)
	}
	if n := bdBindingFunctions(t); n != 3 {
		t.Errorf("after the Up: %d of the three functions exist, want 3", n)
	}
	for table, expr := range bdPolicyText(t) {
		if !strings.Contains(expr, "document_readable") {
			t.Errorf("after the Up: %s policy = %s, want it to call document_readable", table, expr)
		}
	}
}

func TestRLS_DocumentBindingMigrationBackfillsAndRoundTrips(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	v := migrationVersion(t, bindingMigrationGlob)
	reapply := rollBackMigrations(t, staffMigrationProvider(t), v)

	tenant, a, b := caThrowawayTenant(t), uuid.NewString(), uuid.NewString()
	bdExec(t, `INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $3, 'bf-a'), ($2, $3, 'bf-b')`, a, b, tenant)
	t.Cleanup(func() {
		for _, tb := range []string{"invoices", "import_batches", "documents", "business_entities"} {
			_, _ = h.super.Exec(ctx, `DELETE FROM `+tb+` WHERE tenant_id = $1`, tenant)
		}
	})
	doc := func() string {
		id := uuid.NewString()
		bdExec(t, `INSERT INTO documents (id, tenant_id, storage_key, content_hash, size_bytes) VALUES ($1, $2, $3, $4, 1024)`,
			id, tenant, "bf-"+uuid.NewString(), docHash())
		return id
	}
	both, aOnly, uncited := doc(), doc(), doc()
	t0 := time.Now().Add(-3 * time.Hour)
	t1 := t0.Add(time.Hour)
	// A's invoice goes in first with the later time: the earliest citation is B's batch, not the first insert.
	bdExec(t, `INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, source_document_id, created_at) VALUES ($1, $2, $3, 'bf-1', $4, $5)`,
		uuid.NewString(), tenant, a, both, t1)
	bdExec(t, `INSERT INTO import_batches (id, tenant_id, entity_id, document_id, created_at) VALUES ($1, $2, $3, $4, $5)`,
		uuid.NewString(), tenant, b, both, t0)
	bdExec(t, `INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, $3, 'bf-2', $4)`,
		uuid.NewString(), tenant, a, aOnly)

	u1, u2 := uuid.NewString(), uuid.NewString()
	audit := func(event, docID, actor string, at time.Time) {
		bdExec(t, `INSERT INTO audit_log (tenant_id, actor, event, payload, created_at) VALUES ($1, $2, $3, jsonb_build_object('id', $4::text), $5)`,
			tenant, actor, event, docID, at)
	}
	audit("document.created", both, "system", t0.Add(-time.Hour))
	audit("document.created", both, u1, t1.Add(time.Hour)) // after both citations: only the created arm counts it
	audit("document.reused", both, u2, t1)                 // after B's batch at t0: not an uploader
	audit("document.created", aOnly, "system", t0)

	snapshot := func() string {
		var s string
		if err := h.super.QueryRow(ctx,
			`SELECT coalesce((SELECT string_agg(row_to_json(i)::text, E'\n' ORDER BY i.id) FROM invoices i WHERE i.tenant_id = $1), '')
			     || coalesce((SELECT string_agg(row_to_json(b)::text, E'\n' ORDER BY b.id) FROM import_batches b WHERE b.tenant_id = $1), '')`,
			tenant).Scan(&s); err != nil {
			t.Fatalf("snapshot the citations: %v", err)
		}
		return s
	}
	before := snapshot()
	if before == "" {
		t.Fatal("control: no citation rows to snapshot")
	}

	reapply()

	for name, c := range map[string]struct {
		doc       string
		entity    *string
		uploaders []string
	}{
		"cited by B's batch then A's invoice": {both, &b, []string{u1}},
		"cited by A's invoice only":           {aOnly, &a, nil},
		"uncited":                             {uncited, nil, nil},
	} {
		var entity *string
		if err := h.super.QueryRow(ctx, `SELECT entity_id::text FROM documents WHERE id = $1`, c.doc).Scan(&entity); err != nil {
			t.Fatalf("read the backfilled %s: %v", name, err)
		}
		if bdShow(entity) != bdShow(c.entity) {
			t.Errorf("%s: entity_id = %s, want %s", name, bdShow(entity), bdShow(c.entity))
		}
		if got := bdUploaders(t, c.doc); !slices.Equal(got, c.uploaders) {
			t.Errorf("%s: uploaders = %v, want %v", name, got, c.uploaders)
		}
	}
	if after := snapshot(); after != before {
		t.Errorf("the citation rows changed across the migration:\nbefore %s\nafter  %s", before, after)
	}
}

// bdUploaders returns the document's uploader ids from document_uploaders, sorted.
func bdUploaders(t *testing.T, doc string) []string {
	t.Helper()
	rows, err := h.super.Query(context.Background(), `SELECT user_id::text FROM document_uploaders WHERE document_id = $1 ORDER BY 1`, doc)
	if err != nil {
		t.Fatalf("read the uploaders of %s: %v", doc, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			t.Fatalf("scan an uploader: %v", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the uploaders of %s: %v", doc, err)
	}
	return out
}

// The backfill form of the co-uploader gate: a reuser counts only before the document's first citation,
// whether that citation is a batch or an invoice.
func TestRLS_BackfillRecordsAReuserOnlyBeforeTheFirstCitation(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	v := migrationVersion(t, bindingMigrationGlob)
	reapply := rollBackMigrations(t, staffMigrationProvider(t), v)

	tenant, b := caThrowawayTenant(t), uuid.NewString()
	bdExec(t, `INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, 'br-b')`, b, tenant)
	t.Cleanup(func() {
		for _, tb := range []string{"invoices", "import_batches", "documents", "business_entities"} {
			_, _ = h.super.Exec(ctx, `DELETE FROM `+tb+` WHERE tenant_id = $1`, tenant)
		}
	})
	newDoc := func() string {
		id := uuid.NewString()
		bdExec(t, `INSERT INTO documents (id, tenant_id, storage_key, content_hash, size_bytes) VALUES ($1, $2, $3, $4, 1024)`,
			id, tenant, "br-"+uuid.NewString(), docHash())
		return id
	}
	byBatch, byInvoice := newDoc(), newDoc()
	t1 := time.Now().Add(-5 * time.Hour)
	t2, t3, t4 := t1.Add(time.Hour), t1.Add(2*time.Hour), t1.Add(3*time.Hour)
	bdExec(t, `INSERT INTO import_batches (id, tenant_id, entity_id, document_id, created_at) VALUES ($1, $2, $3, $4, $5)`,
		uuid.NewString(), tenant, b, byBatch, t3)
	bdExec(t, `INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, source_document_id, created_at) VALUES ($1, $2, $3, 'br-1', $4, $5)`,
		uuid.NewString(), tenant, b, byInvoice, t3)
	type upload struct{ created, before, after string }
	ups := map[string]*upload{
		byBatch:   {uuid.NewString(), uuid.NewString(), uuid.NewString()},
		byInvoice: {uuid.NewString(), uuid.NewString(), uuid.NewString()},
	}
	for doc, u := range ups {
		for _, r := range []struct {
			event, actor string
			at           time.Time
		}{
			{"document.created", u.created, t1}, {"document.reused", u.before, t2}, {"document.reused", "worker", t2}, {"document.reused", u.after, t4},
		} {
			bdExec(t, `INSERT INTO audit_log (tenant_id, actor, event, payload, created_at) VALUES ($1, $2, $3, jsonb_build_object('id', $4::text), $5)`,
				tenant, r.actor, r.event, doc, r.at)
		}
	}

	reapply()

	for name, doc := range map[string]string{"cited by a batch": byBatch, "cited by an invoice": byInvoice} {
		u := ups[doc]
		want := []string{u.created, u.before}
		slices.Sort(want)
		if got := bdUploaders(t, doc); !slices.Equal(got, want) {
			t.Errorf("%s: uploaders = %v, want %v (the creator and the reuser before the citation; not %s after it, not the non-uuid actor)", name, got, want, u.after)
		}
	}
}

// Equal created_at: a batch precedes an invoice, then the lower id wins; an upper-case uuid actor is an uploader.
func TestRLS_DocumentBindingBackfillBreaksTiesBatchFirst(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	v := migrationVersion(t, bindingMigrationGlob)
	reapply := rollBackMigrations(t, staffMigrationProvider(t), v)

	tenant, a, b := caThrowawayTenant(t), uuid.NewString(), uuid.NewString()
	bdExec(t, `INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $3, 'bt-a'), ($2, $3, 'bt-b')`, a, b, tenant)
	t.Cleanup(func() {
		for _, tb := range []string{"invoices", "import_batches", "documents", "business_entities"} {
			_, _ = h.super.Exec(ctx, `DELETE FROM `+tb+` WHERE tenant_id = $1`, tenant)
		}
	})
	doc := func() string {
		id := uuid.NewString()
		bdExec(t, `INSERT INTO documents (id, tenant_id, storage_key, content_hash, size_bytes) VALUES ($1, $2, $3, $4, 1024)`,
			id, tenant, "bt-"+uuid.NewString(), docHash())
		return id
	}
	at := time.Now().Add(-2 * time.Hour)
	batch := func(id, entity, docID string) {
		bdExec(t, `INSERT INTO import_batches (id, tenant_id, entity_id, document_id, created_at) VALUES ($1, $2, $3, $4, $5)`, id, tenant, entity, docID, at)
	}

	batchBeatsInvoice, lowerIDWins := doc(), doc()
	// The invoice for A goes in first, the batch for B second, at the same instant.
	bdExec(t, `INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, source_document_id, created_at) VALUES ($1, $2, $3, 'bt-1', $4, $5)`,
		uuid.NewString(), tenant, a, batchBeatsInvoice, at)
	batch(uuid.NewString(), b, batchBeatsInvoice)
	// B's batch has the higher id and goes in first; A's has the lower id.
	batch("ffffffff-0000-4000-8000-000000000001", b, lowerIDWins)
	batch("00000000-0000-4000-8000-000000000001", a, lowerIDWins)

	upper := strings.ToUpper(uuid.NewString())
	bdExec(t, `INSERT INTO audit_log (tenant_id, actor, event, payload, created_at) VALUES ($1, $2, 'document.created', jsonb_build_object('id', $3::text), $4)`,
		tenant, upper, batchBeatsInvoice, at)

	reapply()

	var entity *string
	if err := h.super.QueryRow(ctx, `SELECT entity_id::text FROM documents WHERE id = $1`, batchBeatsInvoice).Scan(&entity); err != nil {
		t.Fatalf("read the backfilled document: %v", err)
	}
	if bdShow(entity) != b {
		t.Errorf("batch and invoice at one instant: entity_id = %s, want the batch's client %s", bdShow(entity), b)
	}
	if got := bdUploaders(t, batchBeatsInvoice); !slices.Equal(got, []string{strings.ToLower(upper)}) {
		t.Errorf("upper-case uuid actor: uploaders = %v, want one lower-case row %s", got, strings.ToLower(upper))
	}
	bdWantEntity(t, "two batches at one instant, the lower id", lowerIDWins, &a)
}

func TestRLS_ALimitedCallerCannotBindAnotherMembersUpload(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	u1, u2 := uuid.NewString(), uuid.NewString()
	doc := bdDoc(t, f, "", u1)
	uncitedInvoice := uuid.NewString()
	bdExec(t, `INSERT INTO invoices (id, tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3, 'bd-a-uncited')`, uncitedInvoice, f.tenant, f.a)
	limited := func(user *string) bdWho { return bdWho{scope: bdSet(f.a), user: user} }

	bdWantRefused(t, "invoice for A by another member", bdCiteInvoice(f, limited(bdStr(u2)), f.a, doc))
	bdWantRefused(t, "batch for A by another member", bdCiteBatch(f, limited(bdStr(u2)), f.a, doc))
	err := bdApp(f.tenant, limited(bdStr(u2)), func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE invoices SET source_document_id = $1 WHERE id = $2`, doc, uncitedInvoice)
		return err
	})
	bdWantRefused(t, "pointing A's uncited invoice at the document", err)
	bdWantRefused(t, "invoice for A with no current user", bdCiteInvoice(f, limited(nil), f.a, doc))
	bdWantRefused(t, "invoice for A with an empty current user", bdCiteInvoice(f, limited(bdStr("")), f.a, doc))

	bdWantEntity(t, "after the refusals", doc, nil)
	if inv, batch := bdCitations(t, doc); inv != 0 || batch != 0 {
		t.Errorf("citations = %d invoices, %d batches, want 0 and 0", inv, batch)
	}
}

func TestRLS_ALimitedCallerBindsTheirOwnUpload(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	u := uuid.NewString()
	doc := bdDoc(t, f, "", u)

	if err := bdCiteInvoice(f, bdWho{scope: bdSet(f.a), user: bdStr(u)}, f.a, doc); err != nil {
		t.Fatalf("the uploader's invoice for A: %v", err)
	}

	bdWantEntity(t, "after the uploader's citation", doc, &f.a)
}

func TestRLS_ALimitedCallerBindsALegacyUpload(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")

	if err := bdCiteBatch(f, bdWho{scope: bdSet(f.a), user: bdStr(uuid.NewString())}, f.a, doc); err != nil {
		t.Fatalf("batch for A of a document with no uploader: %v", err)
	}

	bdWantEntity(t, "after the citation", doc, &f.a)
}

func TestRLS_FullAccessBindsAnotherMembersUpload(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	u1, u2 := uuid.NewString(), uuid.NewString()
	star, unset := bdDoc(t, f, "", u1), bdDoc(t, f, "", u1)

	if err := bdCiteInvoice(f, bdWho{scope: bdStr("*"), user: bdStr(u2)}, f.a, star); err != nil {
		t.Errorf("scope * citing another member's upload: %v", err)
	}
	if err := bdCiteInvoice(f, bdWho{user: bdStr(u2)}, f.a, unset); err != nil {
		t.Errorf("unset scope citing another member's upload: %v", err)
	}

	bdWantEntity(t, "scope *", star, &f.a)
	bdWantEntity(t, "unset scope", unset, &f.a)
}
