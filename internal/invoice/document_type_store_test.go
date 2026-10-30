package invoice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

func dtCtx(tenant string) context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenant})
}

// dtRow reads the stored columns as the superuser, so only the row is under test.
func dtRow(t *testing.T, super *pgxpool.Pool, id string) (docType string, original *string) {
	t.Helper()
	if err := super.QueryRow(context.Background(),
		`SELECT document_type, original_invoice_id::text FROM invoices WHERE id = $1`, id,
	).Scan(&docType, &original); err != nil {
		t.Fatalf("read invoice %s: %v", id, err)
	}
	return docType, original
}

// dtNothingWritten requires zero invoices, history rows and invoice.created audit rows in the tenant.
func dtNothingWritten(t *testing.T, super, app *pgxpool.Pool, tenant string) {
	t.Helper()
	if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenant); n != 0 {
		t.Errorf("invoices written = %d, want 0", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE tenant_id = $1`, tenant); n != 0 {
		t.Errorf("history rows written = %d, want 0", n)
	}
	if n := auditCount(t, app, tenant, "invoice.created"); n != 0 {
		t.Errorf("invoice.created audit rows = %d, want 0", n)
	}
}

// dtWantRefusal requires err to be want and none of the other two original sentinels.
func dtWantRefusal(t *testing.T, err, want error) {
	t.Helper()
	for _, s := range []error{ErrOriginalNotFound, ErrOriginalIsANote, ErrOriginalOtherEntity} {
		if errors.Is(err, s) != (s == want) {
			t.Errorf("err = %v; errors.Is(err, %v) = %t, want %t", err, s, errors.Is(err, s), s == want)
		}
	}
}

func dtNote(entity, number string, typ submission.DocumentType, original *string) CreateInput {
	return CreateInput{EntityID: entity, InvoiceNumber: number, DocumentType: typ, OriginalInvoiceID: original}
}

func TestStoreCreate_DefaultsToACommercialInvoice(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "DT default")
	ent := seedEntity(t, super, tenant, "E1")

	inv, err := NewStore(app).Create(dtCtx(tenant), CreateInput{EntityID: ent, InvoiceNumber: "DT-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inv.DocumentType != submission.CommercialInvoice {
		t.Errorf("returned DocumentType = %q, want %q", inv.DocumentType, submission.CommercialInvoice)
	}
	if inv.OriginalInvoiceID != nil {
		t.Errorf("returned OriginalInvoiceID = %q, want nil", *inv.OriginalInvoiceID)
	}
	typ, orig := dtRow(t, super, inv.ID)
	if typ != "commercial_invoice" || orig != nil {
		t.Errorf("row = (%q, %v), want (commercial_invoice, nil)", typ, orig)
	}
}

func TestStoreCreate_StoresANoteAgainstItsOriginal(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "DT note")
	ent := seedEntity(t, super, tenant, "E1")
	original := seedInvoice(t, super, tenant, ent, "DT-ORIG")
	store := NewStore(app)

	for _, typ := range []submission.DocumentType{submission.CreditNote, submission.DebitNote} {
		t.Run(string(typ), func(t *testing.T) {
			inv, err := store.Create(dtCtx(tenant), dtNote(ent, "DT-"+string(typ), typ, &original))
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if inv.DocumentType != typ || inv.OriginalInvoiceID == nil || *inv.OriginalInvoiceID != original {
				t.Errorf("returned = (%q, %v), want (%q, %s)", inv.DocumentType, inv.OriginalInvoiceID, typ, original)
			}
			gotType, gotOrig := dtRow(t, super, inv.ID)
			if gotType != string(typ) || gotOrig == nil || *gotOrig != original {
				t.Errorf("row = (%q, %v), want (%q, %s)", gotType, gotOrig, typ, original)
			}
		})
	}
}

func TestStoreCreate_RefusesAnOriginalInAnotherTenant(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := seedTenant(t, super, "DT tenant A")
	entA := seedEntity(t, super, tenantA, "A1")
	tenantB := seedTenant(t, super, "DT tenant B")
	entB := seedEntity(t, super, tenantB, "B1")
	original := seedInvoice(t, super, tenantB, entB, "DT-B-ORIG")
	store := NewStore(app)

	_, err := store.Create(dtCtx(tenantA), dtNote(entA, "DT-A-NOTE", submission.CreditNote, &original))
	if !errors.Is(err, ErrOriginalNotFound) {
		t.Fatalf("Create in tenant A naming tenant B's invoice: err = %v, want ErrOriginalNotFound", err)
	}
	dtWantRefusal(t, err, ErrOriginalNotFound)
	dtNothingWritten(t, super, app, tenantA)

	// The original is real: tenant B names it without trouble, so tenancy refused A.
	inv, err := store.Create(dtCtx(tenantB), dtNote(entB, "DT-B-NOTE", submission.CreditNote, &original))
	if err != nil {
		t.Fatalf("Create in tenant B naming its own invoice: %v", err)
	}
	if inv.DocumentType != submission.CreditNote {
		t.Errorf("tenant B note DocumentType = %q, want credit_note", inv.DocumentType)
	}
}

func TestStoreCreate_RefusesAnOriginalInAnotherEntity(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "DT other entity")
	e1 := seedEntity(t, super, tenant, "E1")
	e2 := seedEntity(t, super, tenant, "E2")
	original := seedInvoice(t, super, tenant, e1, "DT-E1")
	store := NewStore(app)

	_, err := store.Create(dtCtx(tenant), dtNote(e2, "DT-E2-NOTE", submission.CreditNote, &original))
	if !errors.Is(err, ErrOriginalOtherEntity) {
		t.Fatalf("Create in E2 naming E1's invoice: err = %v, want ErrOriginalOtherEntity", err)
	}
	dtWantRefusal(t, err, ErrOriginalOtherEntity)
	if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE tenant_id = $1 AND entity_id = $2`, tenant, e2); n != 0 {
		t.Errorf("invoices written in E2 = %d, want 0", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE tenant_id = $1`, tenant); n != 0 {
		t.Errorf("history rows = %d, want 0 (the seeded original has none)", n)
	}
	if n := auditCount(t, app, tenant, "invoice.created"); n != 0 {
		t.Errorf("invoice.created audit rows = %d, want 0", n)
	}

	// Same entity as the original: accepted.
	if _, err := store.Create(dtCtx(tenant), dtNote(e1, "DT-E1-NOTE", submission.CreditNote, &original)); err != nil {
		t.Errorf("Create in E1 naming E1's invoice: %v", err)
	}
}

func TestStoreCreate_RefusesANoteAsTheOriginal(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "DT note original")
	ent := seedEntity(t, super, tenant, "E1")
	original := seedInvoice(t, super, tenant, ent, "DT-ORIG")
	store := NewStore(app)

	credit, err := store.Create(dtCtx(tenant), dtNote(ent, "DT-CN", submission.CreditNote, &original))
	if err != nil {
		t.Fatalf("Create credit note on the original: %v", err)
	}
	_, err = store.Create(dtCtx(tenant), dtNote(ent, "DT-DN", submission.DebitNote, &credit.ID))
	if !errors.Is(err, ErrOriginalIsANote) {
		t.Fatalf("Create a debit note naming a credit note: err = %v, want ErrOriginalIsANote", err)
	}
	dtWantRefusal(t, err, ErrOriginalIsANote)
	if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE tenant_id = $1 AND invoice_number = 'DT-DN'`, tenant); n != 0 {
		t.Errorf("debit note rows = %d, want 0", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history h JOIN invoices i ON i.id = h.invoice_id WHERE i.invoice_number = 'DT-DN' AND i.tenant_id = $1`, tenant); n != 0 {
		t.Errorf("debit note history rows = %d, want 0", n)
	}
	if n := auditCount(t, app, tenant, "invoice.created"); n != 1 {
		t.Errorf("invoice.created audit rows = %d, want 1 (the credit note only)", n)
	}
}

func TestStoreCreate_RefusesAnAbsentOriginal(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "DT absent")
	ent := seedEntity(t, super, tenant, "E1")
	absent := uuid.NewString()

	_, err := NewStore(app).Create(dtCtx(tenant), dtNote(ent, "DT-ABS", submission.CreditNote, &absent))
	if !errors.Is(err, ErrOriginalNotFound) {
		t.Fatalf("Create naming a random uuid: err = %v, want ErrOriginalNotFound", err)
	}
	dtWantRefusal(t, err, ErrOriginalNotFound)
	dtNothingWritten(t, super, app, tenant)
}

func TestStoreCreate_RefusesAMismatchedTypeAndOriginalBeforeTheTx(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "DT mismatch")
	ent := seedEntity(t, super, tenant, "E1")
	original := seedInvoice(t, super, tenant, ent, "DT-ORIG")

	// A closed pool cannot open a transaction: only a refusal before the tx returns ErrValidation.
	closed, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	closed.Close()

	cases := []struct {
		name string
		typ  submission.DocumentType
		orig *string
	}{
		{"unknown type with an original", "refund", &original},
		{"credit note without an original", submission.CreditNote, nil},
		{"debit note without an original", submission.DebitNote, nil},
		{"commercial invoice with an original", submission.CommercialInvoice, &original},
		{"empty type with an original", "", &original},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := dtNote(ent, fmt.Sprintf("DT-BAD-%d", i), c.typ, c.orig)
			if _, err := NewStore(app).Create(dtCtx(tenant), in); !errors.Is(err, ErrValidation) {
				t.Errorf("Create: err = %v, want ErrValidation", err)
			}
			if _, err := NewStore(closed).Create(dtCtx(tenant), in); !errors.Is(err, ErrValidation) {
				t.Errorf("Create on a closed pool: err = %v, want ErrValidation before any transaction", err)
			}
		})
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE tenant_id = $1 AND invoice_number LIKE 'DT-BAD-%'`, tenant); n != 0 {
		t.Errorf("rows written = %d, want 0", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenant); n != 1 {
		t.Errorf("tenant invoices = %d, want 1 (the seeded original)", n)
	}
}

func TestStoreGet_ReadsTheTypeAndTheOriginal(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "DT get")
	ent := seedEntity(t, super, tenant, "E1")
	original := seedInvoice(t, super, tenant, ent, "DT-ORIG")
	var noteID string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, document_type, original_invoice_id)
		 VALUES ($1, $2, 'DT-CN', 'credit_note', $3) RETURNING id`, tenant, ent, original).Scan(&noteID); err != nil {
		t.Fatalf("seed credit note: %v", err)
	}
	store := NewStore(app)

	note, err := store.Get(dtCtx(tenant), noteID)
	if err != nil {
		t.Fatalf("Get note: %v", err)
	}
	if note.DocumentType != submission.CreditNote || note.OriginalInvoiceID == nil || *note.OriginalInvoiceID != original {
		t.Errorf("note = (%q, %v), want (credit_note, %s)", note.DocumentType, note.OriginalInvoiceID, original)
	}
	plain, err := store.Get(dtCtx(tenant), original)
	if err != nil {
		t.Fatalf("Get original: %v", err)
	}
	if plain.DocumentType != submission.CommercialInvoice || plain.OriginalInvoiceID != nil {
		t.Errorf("original = (%q, %v), want (commercial_invoice, nil)", plain.DocumentType, plain.OriginalInvoiceID)
	}
}

func TestStoreCreate_RefusesAnOriginalOutsideTheMembersClients(t *testing.T) {
	f := newCSFixture(t, "DT scope")
	before := mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, f.tenant)
	in := dtNote(f.entA, "DT-SCOPE", submission.CreditNote, &f.invB)

	_, err := f.store.Create(f.limited, in)
	if !errors.Is(err, ErrOriginalNotFound) {
		t.Fatalf("limited member naming a client-B invoice: err = %v, want ErrOriginalNotFound", err)
	}
	dtWantRefusal(t, err, ErrOriginalNotFound)
	if n := mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, f.tenant); n != before {
		t.Errorf("tenant invoices = %d, want %d", n, before)
	}

	// Control: the admin sees invB, so the entity rule answers, not the scope.
	_, err = f.store.Create(f.admin, in)
	if !errors.Is(err, ErrOriginalOtherEntity) {
		t.Fatalf("admin naming the same invoice: err = %v, want ErrOriginalOtherEntity", err)
	}
	dtWantRefusal(t, err, ErrOriginalOtherEntity)

	// An original inside the member's client is accepted.
	inv, err := f.store.Create(f.limited, dtNote(f.entA, "DT-SCOPE-OK", submission.CreditNote, &f.invA))
	if err != nil {
		t.Fatalf("limited member naming a client-A invoice: %v", err)
	}
	if inv.DocumentType != submission.CreditNote {
		t.Errorf("DocumentType = %q, want credit_note", inv.DocumentType)
	}
}

func TestStoreCreate_MalformedOriginalIsValidation(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "DT malformed")
	ent := seedEntity(t, super, tenant, "E1")
	bad := "not-a-uuid"

	_, err := NewStore(app).Create(dtCtx(tenant), dtNote(ent, "DT-MAL", submission.CreditNote, &bad))
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Create naming %q: err = %v, want ErrValidation", bad, err)
	}
	dtWantRefusal(t, err, nil)
	dtNothingWritten(t, super, app, tenant)
}

func TestStoreCreate_OriginalRefusalsAnswerTenantFirst(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := seedTenant(t, super, "DT two tenants A")
	a1 := seedEntity(t, super, tenantA, "A1")
	a2 := seedEntity(t, super, tenantA, "A2")
	tenantB := seedTenant(t, super, "DT two tenants B")
	b1 := seedEntity(t, super, tenantB, "B1")
	b2 := seedEntity(t, super, tenantB, "B2")
	store := NewStore(app)

	aOrig := seedInvoice(t, super, tenantA, a1, "DT-A1")
	bOrig := seedInvoice(t, super, tenantB, b1, "DT-B1")
	aNote, err := store.Create(dtCtx(tenantA), dtNote(a1, "DT-A-CN", submission.CreditNote, &aOrig))
	if err != nil {
		t.Fatalf("seed tenant A credit note: %v", err)
	}
	bNote, err := store.Create(dtCtx(tenantB), dtNote(b1, "DT-B-CN", submission.CreditNote, &bOrig))
	if err != nil {
		t.Fatalf("seed tenant B credit note: %v", err)
	}
	aDebit, err := store.Create(dtCtx(tenantA), dtNote(a1, "DT-A-DN", submission.DebitNote, &aOrig))
	if err != nil {
		t.Fatalf("seed tenant A debit note: %v", err)
	}
	before := mustCount(t, super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenantA)
	if before != 3 {
		t.Fatalf("tenant A invoices = %d, want 3", before)
	}

	cases := []struct {
		name     string
		entity   string
		original string
		want     error
	}{
		{"own note as original", a1, aNote.ID, ErrOriginalIsANote},
		{"own debit note as original", a1, aDebit.ID, ErrOriginalIsANote},
		{"own invoice in the other entity", a2, aOrig, ErrOriginalOtherEntity},
		{"other tenant's invoice, same-shaped entity", a1, bOrig, ErrOriginalNotFound},
		{"other tenant's invoice, other entity", a2, bOrig, ErrOriginalNotFound},
		{"other tenant's note", a1, bNote.ID, ErrOriginalNotFound},
		{"other tenant's invoice in a second entity", a1, seedInvoice(t, super, tenantB, b2, "DT-B2"), ErrOriginalNotFound},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := store.Create(dtCtx(tenantA), dtNote(c.entity, fmt.Sprintf("DT-REF-%d", i), submission.DebitNote, &c.original))
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			dtWantRefusal(t, err, c.want)
		})
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenantA); n != before {
		t.Errorf("tenant A invoices = %d, want %d", n, before)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenantB); n != 3 {
		t.Errorf("tenant B invoices = %d, want 3", n)
	}
}

func TestStoreCreate_UpperCaseEntityIDStillMatchesTheOriginalsEntity(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "DT case")
	ent := seedEntity(t, super, tenant, "E1")
	original := seedInvoice(t, super, tenant, ent, "DT-ORIG")

	inv, err := NewStore(app).Create(dtCtx(tenant), dtNote(strings.ToUpper(ent), "DT-UP", submission.CreditNote, &original))
	if err != nil {
		t.Fatalf("Create with an upper-case entity_id naming a same-entity original: %v", err)
	}
	if inv.OriginalInvoiceID == nil || *inv.OriginalInvoiceID != original {
		t.Errorf("OriginalInvoiceID = %v, want %s", inv.OriginalInvoiceID, original)
	}
}

func TestStoreCanonical_CarriesTheOriginalReference(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "DT canonical")
	ent := seedEntity(t, super, tenant, "E1")
	original := seedInvoice(t, super, tenant, ent, "DT-ORIG")
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET irn = 'IRN-ORIG-1', issue_date = '2026-09-10' WHERE id = $1`, original); err != nil {
		t.Fatalf("set original IRN: %v", err)
	}
	var noteID string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, document_type, original_invoice_id)
		 VALUES ($1, $2, 'DT-CN', 'credit_note', $3) RETURNING id`, tenant, ent, original).Scan(&noteID); err != nil {
		t.Fatalf("seed credit note: %v", err)
	}
	store := NewStore(app)
	canonical := func(id string) submission.Canonical {
		var c submission.Canonical
		if err := db.WithinTenantTx(context.Background(), app, tenant, func(tx pgx.Tx) error {
			var err error
			c, err = store.Canonical(context.Background(), tx, id)
			return err
		}); err != nil {
			t.Fatalf("Canonical %s: %v", id, err)
		}
		return c
	}

	ref := canonical(noteID).BillingReference
	if ref == nil || ref.IRN == nil || *ref.IRN != "IRN-ORIG-1" || ref.IssueDate == nil || ref.IssueDate.Format("2006-01-02") != "2026-09-10" {
		t.Errorf("note BillingReference = %+v, want IRN-ORIG-1 / 2026-09-10", ref)
	}
	if got := canonical(original).BillingReference; got != nil {
		t.Errorf("original BillingReference = %+v, want nil", got)
	}
}

func TestStoreGet_NoteWhoseOriginalIsOutsideTheScopeReadsWithoutTheReference(t *testing.T) {
	f := newCSFixture(t, "DT scope read")
	ctx := context.Background()
	if _, err := f.super.Exec(ctx, `UPDATE invoices SET irn = 'IRN-B-1' WHERE id = $1`, f.invB); err != nil {
		t.Fatalf("set IRN: %v", err)
	}
	var noteID string
	if err := f.super.QueryRow(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, document_type, original_invoice_id)
		 VALUES ($1, $2, 'DT-SCOPE-CN', 'credit_note', $3) RETURNING id`, f.tenant, f.entA, f.invB).Scan(&noteID); err != nil {
		t.Fatalf("seed credit note: %v", err)
	}

	got, err := f.store.Get(f.limited, noteID)
	if err != nil {
		t.Fatalf("limited Get: %v", err)
	}
	if got.OriginalInvoiceID == nil || *got.OriginalInvoiceID != f.invB || got.Original != nil {
		t.Errorf("limited: OriginalInvoiceID = %v, Original = %+v; want %s, nil", got.OriginalInvoiceID, got.Original, f.invB)
	}

	got, err = f.store.Get(f.admin, noteID)
	if err != nil {
		t.Fatalf("admin Get: %v", err)
	}
	if got.Original == nil || got.Original.IRN == nil || *got.Original.IRN != "IRN-B-1" {
		t.Errorf("admin: Original = %+v, want IRN-B-1", got.Original)
	}
}

func TestStoreCanonical_EachNoteGetsItsOwnOriginalsReference(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "DT two originals")
	ent := seedEntity(t, super, tenant, "E1")
	ctx := context.Background()
	o1 := seedInvoice(t, super, tenant, ent, "DT-O1")
	o2 := seedInvoice(t, super, tenant, ent, "DT-O2")
	o3 := seedInvoice(t, super, tenant, ent, "DT-O3")
	for _, q := range []struct{ id, sql string }{
		{o1, `UPDATE invoices SET irn = 'IRN-ONE', issue_date = '2026-01-01' WHERE id = $1`},
		{o2, `UPDATE invoices SET irn = 'IRN-TWO', issue_date = '2026-02-02' WHERE id = $1`},
		{o3, `UPDATE invoices SET irn = 'IRN-THREE' WHERE id = $1`},
	} {
		if _, err := super.Exec(ctx, q.sql, q.id); err != nil {
			t.Fatalf("set original: %v", err)
		}
	}
	note := func(n, orig string) string {
		var id string
		if err := super.QueryRow(ctx,
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number, document_type, original_invoice_id)
			 VALUES ($1, $2, $3, 'debit_note', $4) RETURNING id`, tenant, ent, n, orig).Scan(&id); err != nil {
			t.Fatalf("seed note: %v", err)
		}
		return id
	}
	store := NewStore(app)
	want := map[string][2]string{note("DT-N1", o1): {"IRN-ONE", "2026-01-01"}, note("DT-N2", o2): {"IRN-TWO", "2026-02-02"}, note("DT-N3", o3): {"IRN-THREE", ""}}
	if len(want) != 3 {
		t.Fatalf("seeded %d notes, want 3", len(want))
	}
	for id, w := range want {
		var c submission.Canonical
		if err := db.WithinTenantTx(ctx, app, tenant, func(tx pgx.Tx) error {
			var err error
			c, err = store.Canonical(ctx, tx, id)
			return err
		}); err != nil {
			t.Fatalf("Canonical: %v", err)
		}
		ref := c.BillingReference
		if ref == nil || ref.IRN == nil || *ref.IRN != w[0] {
			t.Errorf("note %s: reference = %+v, want IRN %s", id, ref, w[0])
			continue
		}
		got := ""
		if ref.IssueDate != nil {
			got = ref.IssueDate.Format("2006-01-02")
		}
		if got != w[1] {
			t.Errorf("note %s: IssueDate = %q, want %q", id, got, w[1])
		}
	}
}

func TestStoreCanonical_NoteOnAnOriginalWithoutAnIRNCarriesANilIRN(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "DT draft original")
	ent := seedEntity(t, super, tenant, "E1")
	original := seedInvoice(t, super, tenant, ent, "DT-DRAFT-O")
	var noteID string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, document_type, original_invoice_id)
		 VALUES ($1, $2, 'DT-DRAFT-N', 'credit_note', $3) RETURNING id`, tenant, ent, original).Scan(&noteID); err != nil {
		t.Fatalf("seed note: %v", err)
	}
	var c submission.Canonical
	if err := db.WithinTenantTx(context.Background(), app, tenant, func(tx pgx.Tx) error {
		var err error
		c, err = NewStore(app).Canonical(context.Background(), tx, noteID)
		return err
	}); err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	if c.BillingReference == nil || c.BillingReference.IRN != nil {
		t.Errorf("BillingReference = %+v, want non-nil with nil IRN", c.BillingReference)
	}
}

func TestStoreCanonical_ANoteIsNotReadableFromAnotherTenant(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "DT note owner")
	other := seedTenant(t, super, "DT note stranger")
	ent := seedEntity(t, super, tenant, "E1")
	original := seedInvoice(t, super, tenant, ent, "DT-X-O")
	var noteID string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, document_type, original_invoice_id)
		 VALUES ($1, $2, 'DT-X-N', 'credit_note', $3) RETURNING id`, tenant, ent, original).Scan(&noteID); err != nil {
		t.Fatalf("seed note: %v", err)
	}
	err := db.WithinTenantTx(context.Background(), app, other, func(tx pgx.Tx) error {
		_, err := NewStore(app).Canonical(context.Background(), tx, noteID)
		return err
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Canonical of another tenant's note: err = %v, want ErrNotFound", err)
	}
}

func TestStoreList_ReturnsDocumentTypeAndOriginal(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "DT list")
	ent := seedEntity(t, super, tenant, "E1")
	original := seedInvoice(t, super, tenant, ent, "DT-LIST-ORIG")
	store := NewStore(app)

	note, err := store.Create(dtCtx(tenant), dtNote(ent, "DT-LIST-CN", submission.CreditNote, &original))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	items, total, err := store.List(dtCtx(tenant), ListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("List = %d rows, total %d, want 2 and 2", len(items), total)
	}
	for _, it := range items {
		switch it.ID {
		case original:
			if it.DocumentType != submission.CommercialInvoice || it.OriginalInvoiceID != nil {
				t.Errorf("invoice row = (%q, %v), want (commercial_invoice, nil)", it.DocumentType, it.OriginalInvoiceID)
			}
		case note.ID:
			if it.DocumentType != submission.CreditNote || it.OriginalInvoiceID == nil || *it.OriginalInvoiceID != original {
				t.Errorf("note row = (%q, %v), want (credit_note, %s)", it.DocumentType, it.OriginalInvoiceID, original)
			}
		default:
			t.Errorf("unexpected row %s", it.ID)
		}
	}
}
