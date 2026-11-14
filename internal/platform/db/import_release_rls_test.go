// Storage suite for SECAUD-50: a failed or undone import releases its document. A live citation is an
// invoice that is not discarded or a batch that is not failed or undone; the document stays bound only
// while one exists. Rows are seeded as the superuser; the writes under test run as invoice_app.
package db_test

import (
	"context"
	"errors"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const importReleaseMigrationGlob = "*_import_release.sql"

// irRollbackOrder puts the import_release Down before v's when that migration exists, so a Down of an
// earlier migration never leaves its triggers and functions behind.
func irRollbackOrder(t *testing.T, v int64) []int64 {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, importReleaseMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", importReleaseMigrationGlob, err)
	}
	if len(matches) == 0 {
		return []int64{v}
	}
	return []int64{migrationVersion(t, importReleaseMigrationGlob), v}
}

// irLegacyDead seeds one dead citation as a pre-migration row; a refusal is a failure, not a fatal.
func irLegacyDead(t *testing.T, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := h.super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin legacy seed: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatalf("set replica role: %v", err)
	}
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		t.Errorf("the status CHECK must accept the dead citation: %v\n%s", err, sql)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit legacy seed: %v", err)
	}
}

func irPgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func irInsertBatch(tx pgx.Tx, f *bdFix, entity, doc, status string) (string, error) {
	id := uuid.NewString()
	_, err := tx.Exec(context.Background(),
		`INSERT INTO import_batches (id, tenant_id, entity_id, document_id, status) VALUES ($1, $2, $3, nullif($4, '')::uuid, $5)`,
		id, f.tenant, entity, doc, status)
	return id, err
}

// irNewBatch cites doc from a batch in the given status as invoice_app.
func irNewBatch(t *testing.T, f *bdFix, who bdWho, entity, doc, status string) string {
	t.Helper()
	var id string
	err := bdApp(f.tenant, who, func(tx pgx.Tx) (err error) { id, err = irInsertBatch(tx, f, entity, doc, status); return })
	if err != nil {
		t.Fatalf("insert a %s batch for %s on %s: %v", status, entity, doc, err)
	}
	return id
}

func irSetBatchStatus(f *bdFix, who bdWho, batch, status string) error {
	return bdApp(f.tenant, who, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE import_batches SET status = $2 WHERE id = $1`, batch, status)
		return err
	})
}

// irNewInvoice cites doc from an invoice of the batch (empty for none), as the superuser.
func irNewInvoice(t *testing.T, f *bdFix, entity, doc, batch, status string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, source_document_id, import_batch_id, status)
		 VALUES ($1, $2, $3, $4, $5, nullif($6, '')::uuid, 'draft')`,
		id, f.tenant, entity, "ir-"+uuid.NewString()[:8], doc, batch); err != nil {
		t.Fatalf("insert an invoice for %s on %s: %v", entity, doc, err)
	}
	if status != "draft" {
		irSetInvoiceStatus(t, id, status)
	}
	return id
}

func irSetInvoiceStatus(t *testing.T, invoice, status string) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(), `UPDATE invoices SET status = $2 WHERE id = $1`, invoice, status); err != nil {
		t.Fatalf("set invoice %s to %s: the invoices status CHECK must accept it: %v", invoice, status, err)
	}
}

// irFail moves the batch to failed as invoice_app and reports a refusal as a failure.
func irFail(t *testing.T, f *bdFix, who bdWho, batch string) {
	t.Helper()
	if err := irSetBatchStatus(f, who, batch, "failed"); err != nil {
		t.Errorf("batch %s to failed: %v", batch, err)
	}
}

func irThirdClient(t *testing.T, f *bdFix) string {
	t.Helper()
	c := uuid.NewString()
	bdExec(t, `INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, 'ir-client-c')`, c, f.tenant)
	return c
}

var irNullEntity *string

func TestRLS_AFailedBatchReleasesItsDocument(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")
	batch := irNewBatch(t, f, bdWho{}, f.a, doc, "processing")
	bdWantEntity(t, "while the batch is processing", doc, &f.a)

	irFail(t, f, bdWho{}, batch)
	bdWantEntity(t, "after the only citation failed", doc, irNullEntity)
}

func TestRLS_AReleasedDocumentBindsToTheNextCitationsClient(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")
	batch := irNewBatch(t, f, bdWho{}, f.a, doc, "processing")
	irFail(t, f, bdWho{}, batch)
	bdWantEntity(t, "after the release", doc, irNullEntity)

	if err := bdCiteBatch(f, bdWho{}, f.b, doc); err != nil {
		t.Errorf("B's batch on the released document: %v, want accepted", err)
	}
	bdWantEntity(t, "after B's batch", doc, &f.b)
}

func TestRLS_AFailedBatchKeepsTheBindingWhileAnotherLiveCitationRemains(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)

	other := bdDoc(t, f, "", "")
	irNewBatch(t, f, bdWho{}, f.a, other, "completed")
	second := irNewBatch(t, f, bdWho{}, f.a, other, "processing")
	irFail(t, f, bdWho{}, second)
	bdWantEntity(t, "a completed A batch remains", other, &f.a)

	own := bdDoc(t, f, "", "")
	failing := irNewBatch(t, f, bdWho{}, f.a, own, "processing")
	irNewInvoice(t, f, f.a, own, failing, "draft")
	irFail(t, f, bdWho{}, failing)
	bdWantEntity(t, "an invoice of the failed batch itself remains", own, &f.a)

	alone := bdDoc(t, f, "", "")
	lone := irNewBatch(t, f, bdWho{}, f.a, alone, "processing")
	irFail(t, f, bdWho{}, lone)
	bdWantEntity(t, "control: a failed batch with no other citation", alone, irNullEntity)
}

func TestRLS_AProcessingBatchHoldsTheDocument(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")
	batch := irNewBatch(t, f, bdWho{}, f.a, doc, "processing")

	bdWantRefused(t, "B's batch while A's is processing", bdCiteBatch(f, bdWho{}, f.b, doc))
	bdWantEntity(t, "after the refusal", doc, &f.a)

	irFail(t, f, bdWho{}, batch)
	if err := bdCiteBatch(f, bdWho{}, f.b, doc); err != nil {
		t.Errorf("control: B's batch after A's failed: %v, want accepted", err)
	}
	bdWantEntity(t, "control: after A's batch failed and B's cited", doc, &f.b)
}

func TestRLS_ACompletedBatchHoldsTheDocument(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")
	irNewBatch(t, f, bdWho{}, f.a, doc, "completed")

	bdWantRefused(t, "B's batch against a completed A batch with 0 invoices", bdCiteBatch(f, bdWho{}, f.b, doc))
	irNewInvoice(t, f, f.a, doc, "", "draft")
	bdWantRefused(t, "B's batch against a completed A batch with 1 invoice", bdCiteBatch(f, bdWho{}, f.b, doc))
	bdWantEntity(t, "after the refusals", doc, &f.a)

	failed := bdDoc(t, f, "", "")
	batch := irNewBatch(t, f, bdWho{}, f.a, failed, "processing")
	irFail(t, f, bdWho{}, batch)
	if err := bdCiteBatch(f, bdWho{}, f.b, failed); err != nil {
		t.Errorf("control: B's batch on a document whose A batch failed: %v, want accepted", err)
	}
}

func TestRLS_ARepeatedStatusWriteReleasesNothing(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")
	batch := irNewBatch(t, f, bdWho{}, f.a, doc, "processing")
	irFail(t, f, bdWho{}, batch)
	bdWantEntity(t, "control: the first failed write releases", doc, irNullEntity)

	// A pre-migration binding to B that no live citation holds.
	cpLegacyExec(t, `UPDATE documents SET entity_id = $2 WHERE id = $1`, doc, f.b)
	irFail(t, f, bdWho{}, batch)
	bdWantEntity(t, "after a second failed write", doc, &f.b)
}

func TestRLS_AnUndoneBatchReleasesItsDocument(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)

	doc := bdDoc(t, f, "", "")
	batch := irNewBatch(t, f, bdWho{}, f.a, doc, "completed")
	i1, i2 := irNewInvoice(t, f, f.a, doc, batch, "draft"), irNewInvoice(t, f, f.a, doc, batch, "validated")
	irSetInvoiceStatus(t, i1, "discarded")
	irSetInvoiceStatus(t, i2, "discarded")
	if err := irSetBatchStatus(f, bdWho{}, batch, "undone"); err != nil {
		t.Errorf("batch to undone: %v", err)
	}
	bdWantEntity(t, "after the invoices were discarded and the batch undone", doc, irNullEntity)

	held := bdDoc(t, f, "", "")
	b2 := irNewBatch(t, f, bdWho{}, f.a, held, "completed")
	live := irNewInvoice(t, f, f.a, held, b2, "draft")
	gone := irNewInvoice(t, f, f.a, held, b2, "draft")
	irSetInvoiceStatus(t, gone, "discarded")
	if err := irSetBatchStatus(f, bdWho{}, b2, "undone"); err != nil {
		t.Errorf("batch to undone with a live invoice: %v", err)
	}
	bdWantEntity(t, "while invoice "+live+" is live", held, &f.a)
}

func TestRLS_ALimitedReleaseSeesAnotherClientsLiveCitation(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	c := irThirdClient(t, f)
	limited := bdWho{scope: bdSet(f.a)}
	legacy := func() (doc, batch string) {
		doc, batch = bdDoc(t, f, c, ""), uuid.NewString()
		cpLegacyExec(t, `INSERT INTO import_batches (id, tenant_id, entity_id, document_id, status) VALUES ($1, $2, $3, $4, 'processing')`,
			batch, f.tenant, f.a, doc)
		return doc, batch
	}

	held, heldBatch := legacy()
	cpLegacyExec(t, `INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, source_document_id) VALUES ($1, $2, $3, 'ir-live-c', $4)`,
		uuid.NewString(), f.tenant, c, held)
	irFail(t, f, limited, heldBatch)
	bdWantEntity(t, "an A-limited release beside C's live invoice", held, &c)

	free, freeBatch := legacy()
	irFail(t, f, limited, freeBatch)
	bdWantEntity(t, "control: an A-limited release with no live citation", free, irNullEntity)
}

func TestRLS_AfterAReleaseTheFailedClientLosesReadOnceBoundElsewhere(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	uploader, member := uuid.NewString(), uuid.NewString()
	doc := bdDoc(t, f, "", uploader)
	cpExtractionRows(t, f.tenant, doc)
	batch := irNewBatch(t, f, bdWho{}, f.a, doc, "processing")
	nonUploader := bdWho{scope: bdSet(f.a), user: bdStr(member)}

	irFail(t, f, bdWho{}, batch)
	if got := bdRead(t, f, nonUploader, doc); got != (bdCell{true, true, true}) {
		t.Errorf("A member, document unbound after the release: %+v, want readable everywhere", got)
	}

	if err := bdCiteBatch(f, bdWho{}, f.b, doc); err != nil {
		t.Fatalf("B's batch on the released document: %v, want accepted", err)
	}
	for name, c := range map[string]struct {
		who  bdWho
		want bool
	}{
		"A member, not the uploader": {nonUploader, false},
		"the uploader":               {bdWho{scope: bdSet(f.a), user: bdStr(uploader)}, true},
		"admin (*)":                  {bdWho{scope: bdStr("*"), user: bdStr(member)}, true},
		"unset scope":                {bdWho{user: bdStr(member)}, true},
	} {
		if got := bdRead(t, f, c.who, doc); got != (bdCell{c.want, c.want, c.want}) {
			t.Errorf("%s, document bound to B after A's failed batch: %+v, want %v for the Go rule, the job and the page image", name, got, c.want)
		}
	}
}

func TestRLS_AppReleasesADocumentOnlyWhenNoLiveCitationRemains(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")
	irNewBatch(t, f, bdWho{}, f.a, doc, "failed")
	bdWantEntity(t, "control: a failed batch binds on insert", doc, &f.a)
	release := func() error {
		return bdApp(f.tenant, bdWho{}, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `UPDATE documents SET entity_id = NULL WHERE id = $1`, doc)
			return err
		})
	}

	if err := release(); err != nil {
		t.Errorf("UPDATE entity_id = NULL with only a failed citation: %v, want it to succeed", err)
	}
	bdWantEntity(t, "after the direct release", doc, irNullEntity)

	bdBind(t, f, f.b, doc)
	if code := irPgCode(release()); code != "23001" {
		t.Errorf("UPDATE entity_id = NULL with a live invoice: SQLSTATE %q, want 23001", code)
	}
	bdWantEntity(t, "after the refused release", doc, &f.b)
}

func TestRLS_DocumentLinkStateCountsOnlyLiveCitations(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")
	scope := bdSet(f.b)
	state := func() (invoice, link bool, before, after string) {
		err := bdApp(f.tenant, bdWho{scope: scope}, func(tx pgx.Tx) error {
			ctx := context.Background()
			if err := tx.QueryRow(ctx, `SELECT current_setting('app.entity_scope', true)`).Scan(&before); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT has_invoice, has_link FROM public.document_link_state($1)`, doc).Scan(&invoice, &link); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT current_setting('app.entity_scope', true)`).Scan(&after)
		})
		if err != nil {
			t.Fatalf("read document_link_state: %v", err)
		}
		return invoice, link, before, after
	}

	irNewBatch(t, f, bdWho{}, f.a, doc, "failed")
	if inv, link, _, _ := state(); inv || link {
		t.Errorf("only a failed batch: has_invoice %v, has_link %v, want false, false", inv, link)
	}

	irNewBatch(t, f, bdWho{}, f.a, doc, "undone")
	dead := irNewInvoice(t, f, f.a, doc, "", "draft")
	irSetInvoiceStatus(t, dead, "discarded")
	if inv, link, _, _ := state(); inv || link {
		t.Errorf("failed and undone batches and a discarded invoice: has_invoice %v, has_link %v, want false, false", inv, link)
	}

	irNewInvoice(t, f, f.a, doc, "", "draft")
	inv, link, before, after := state()
	if !inv || !link {
		t.Errorf("with a live invoice: has_invoice %v, has_link %v, want true, true", inv, link)
	}
	if before != after || before == "" {
		t.Errorf("app.entity_scope before %q and after %q the call, want the same non-empty value", before, after)
	}
}

func TestRLS_ACitationForTheSameClientDuringAReleaseKeepsTheBinding(t *testing.T) {
	requireHarness(t)
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")
	batch := irNewBatch(t, f, bdWho{}, f.a, doc, "processing")

	err1, err2, blocked := bdRaceFns(t, f, true,
		bdWho{}, func(tx pgx.Tx) error { return bdInsertInvoice(tx, f.tenant, f.a, doc) },
		bdWho{}, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `UPDATE import_batches SET status = 'failed' WHERE id = $1`, batch)
			return err
		})
	if err1 != nil || err2 != nil {
		t.Errorf("A's invoice %v, A's batch failing %v, want both to commit", err1, err2)
	}
	if !blocked {
		t.Error("the failing batch's release never waited on the document row held by the citation")
	}
	bdWantEntity(t, "after the citation committed first", doc, &f.a)
}

func TestRLS_ACitationForAnotherClientWaitsForTheRelease(t *testing.T) {
	requireHarness(t)
	for name, commit := range map[string]bool{"the release commits": true, "the release rolls back": false} {
		f := bdSeed(t)
		doc := bdDoc(t, f, "", "")
		batch := irNewBatch(t, f, bdWho{}, f.a, doc, "processing")

		err1, err2, blocked := bdRaceFns(t, f, commit,
			bdWho{}, func(tx pgx.Tx) error {
				_, err := tx.Exec(context.Background(), `UPDATE import_batches SET status = 'failed' WHERE id = $1`, batch)
				return err
			},
			bdWho{}, func(tx pgx.Tx) error { _, err := irInsertBatch(tx, f, f.b, doc, "processing"); return err })
		if !blocked {
			t.Errorf("%s: B's batch never waited on the release", name)
		}
		if commit {
			if err1 != nil || err2 != nil {
				t.Errorf("%s: release %v, B's batch %v, want both to commit", name, err1, err2)
			}
			bdWantEntity(t, name+": after the wait", doc, &f.b)
			continue
		}
		if err1 == nil {
			t.Errorf("%s: control: the release did not roll back", name)
		}
		bdWantRefused(t, name+": B's batch after the wait", err2)
		bdWantEntity(t, name+": after the wait", doc, &f.a)
	}
}

func TestRLS_StatusChecksAcceptDiscardedAndUndone(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	f := bdSeed(t)
	doc := bdDoc(t, f, "", "")
	batch := irNewBatch(t, f, bdWho{}, f.a, doc, "completed")
	inv := irNewInvoice(t, f, f.a, doc, batch, "draft")
	history := func(from, to string) error {
		_, err := h.super.Exec(ctx,
			`INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor) VALUES ($1, $2, $3, $4, 'ir-test')`,
			f.tenant, inv, from, to)
		return err
	}

	for name, c := range map[string]struct {
		do       func(value string) error
		accepted string
	}{
		"invoices.status": {func(v string) error {
			_, err := h.super.Exec(ctx, `UPDATE invoices SET status = $2 WHERE id = $1`, inv, v)
			return err
		}, "discarded"},
		"invoice_status_history.to_status":   {func(v string) error { return history("draft", v) }, "discarded"},
		"invoice_status_history.from_status": {func(v string) error { return history(v, "draft") }, "discarded"},
		"import_batches.status": {func(v string) error {
			_, err := h.super.Exec(ctx, `UPDATE import_batches SET status = $2 WHERE id = $1`, batch, v)
			return err
		}, "undone"},
	} {
		if err := c.do(c.accepted); err != nil {
			t.Errorf("%s = %q: %v, want accepted", name, c.accepted, err)
		}
		if code := irPgCode(c.do("bogus")); code != "23514" {
			t.Errorf("%s = 'bogus': SQLSTATE %q, want 23514", name, code)
		}
	}
}

var irTextValue = regexp.MustCompile(`'[a-z_]+'::text`)

// irCheckValues counts the values of a status CHECK.
func irCheckValues(t *testing.T, table, name string) int {
	t.Helper()
	var def string
	if err := h.super.QueryRow(context.Background(),
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = $1::regclass AND conname = $2`, "public."+table, name).Scan(&def); err != nil {
		t.Fatalf("read constraint %s on %s: %v", name, table, err)
	}
	return len(irTextValue.FindAllString(def, -1))
}

func irFunctionDef(t *testing.T, name string) (def string, present bool) {
	t.Helper()
	err := h.super.QueryRow(context.Background(),
		`SELECT pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		  WHERE n.nspname = 'public' AND p.proname = $1`, name).Scan(&def)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read function %s: %v", name, err)
	}
	return def, true
}

func irTriggerCount(t *testing.T) int {
	t.Helper()
	return mustCount(t, h.super, `SELECT count(*) FROM pg_trigger WHERE tgname = 'import_batches_release_document' AND NOT tgisinternal`)
}

func TestRLS_ImportReleaseMigrationBackfillsAndRoundTrips(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	reapply := rollBackMigrations(t, staffMigrationProvider(t), migrationVersion(t, importReleaseMigrationGlob))

	tenant, a, c := caThrowawayTenant(t), uuid.NewString(), uuid.NewString()
	bdExec(t, `INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $3, 'ir-a'), ($2, $3, 'ir-c')`, a, c, tenant)
	t.Cleanup(func() {
		for _, tb := range []string{"invoices", "import_batches", "documents", "business_entities"} {
			_, _ = h.super.Exec(ctx, `DELETE FROM `+tb+` WHERE tenant_id = $1`, tenant)
		}
	})
	doc := func() string {
		id := uuid.NewString()
		bdExec(t, `INSERT INTO documents (id, tenant_id, storage_key, content_hash, size_bytes, entity_id) VALUES ($1, $2, $3, $4, 1024, $5)`,
			id, tenant, "ir-"+uuid.NewString(), docHash(), a)
		return id
	}
	batch := func(d, status string, at time.Time) {
		bdExec(t, `INSERT INTO import_batches (id, tenant_id, entity_id, document_id, status, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
			uuid.NewString(), tenant, a, d, status, at)
	}
	failedOnly, failedThenC, live, uncited := doc(), doc(), doc(), doc()
	t0 := time.Now().Add(-3 * time.Hour)
	batch(failedOnly, "failed", t0)
	batch(failedThenC, "failed", t0)
	cpLegacyExec(t, `INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, source_document_id, created_at) VALUES ($1, $2, $3, 'ir-c-live', $4, $5)`,
		uuid.NewString(), tenant, c, failedThenC, t0.Add(time.Hour))
	batch(live, "completed", t0)

	reapply()

	for name, want := range map[string]struct {
		doc    string
		entity *string
	}{
		"bound by a failed batch only":                   {failedOnly, irNullEntity},
		"bound by a failed A batch and a live C invoice": {failedThenC, &c},
		"bound by a live batch":                          {live, &a},
		"bound with no citation":                         {uncited, irNullEntity},
	} {
		bdWantEntity(t, name, want.doc, want.entity)
	}
}

func TestRLS_ImportReleaseMigrationDownRestoresTheBindingRules(t *testing.T) {
	requireHarness(t)
	reapply := rollBackMigrations(t, staffMigrationProvider(t), migrationVersion(t, importReleaseMigrationGlob))

	if _, present := irFunctionDef(t, "release_document_binding"); present {
		t.Error("after the Down: release_document_binding still exists")
	}
	if n := irTriggerCount(t); n != 0 {
		t.Errorf("after the Down: %d import_batches_release_document triggers, want 0", n)
	}
	if def, _ := irFunctionDef(t, "document_link_state"); strings.Contains(def, "discarded") {
		t.Errorf("after the Down: document_link_state still reads discarded:\n%s", def)
	}
	if def, _ := irFunctionDef(t, "documents_binding_guard"); strings.Contains(def, "document_link_state") {
		t.Errorf("after the Down: documents_binding_guard still reads document_link_state:\n%s", def)
	}
	checks := []struct {
		table, name   string
		down, upAgain int
	}{
		{"invoices", "invoices_status_check", 7, 8},
		{"invoice_status_history", "invoice_status_history_from_status_check", 7, 8},
		{"invoice_status_history", "invoice_status_history_to_status_check", 7, 8},
		{"import_batches", "import_batches_status_check", 4, 5},
	}
	for _, c := range checks {
		if n := irCheckValues(t, c.table, c.name); n != c.down {
			t.Errorf("after the Down: %s holds %d values, want %d", c.name, n, c.down)
		}
	}

	reapply()

	if _, present := irFunctionDef(t, "release_document_binding"); !present {
		t.Error("after the Up: release_document_binding is absent")
	}
	if n := irTriggerCount(t); n != 1 {
		t.Errorf("after the Up: %d import_batches_release_document triggers, want 1", n)
	}
	if def, _ := irFunctionDef(t, "document_link_state"); !strings.Contains(def, "discarded") {
		t.Errorf("after the Up: document_link_state does not read discarded:\n%s", def)
	}
	for _, c := range checks {
		if n := irCheckValues(t, c.table, c.name); n != c.upAgain {
			t.Errorf("after the Up: %s holds %d values, want %d", c.name, n, c.upAgain)
		}
	}
}
