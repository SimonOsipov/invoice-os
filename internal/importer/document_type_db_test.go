package importer

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// dtSeedPoorScan seeds the one-row document_text_layer set the worker writes for a textless scan.
func dtSeedPoorScan(t *testing.T, super *pgxpool.Pool, tenantID, documentID string) {
	t.Helper()
	job := seedExtractionJob(t, super, tenantID, documentID, "succeeded", time.Now().UTC())
	seedExtractionField(t, super, tenantID, job, "document_text_layer", nil, sxPtr("unreadable"), 0, time.Now().UTC())
}

func dtSetVerdict(t *testing.T, super *pgxpool.Pool, documentID, verdict string) {
	t.Helper()
	ct, err := super.Exec(context.Background(),
		`UPDATE extraction_jobs SET document_type = $2 WHERE document_id = $1`, documentID, verdict)
	if err != nil || ct.RowsAffected() != 1 {
		t.Fatalf("set the verdict on document %s: rows %d, err %v", documentID, ct.RowsAffected(), err)
	}
	var got *string
	if err := super.QueryRow(context.Background(),
		`SELECT document_type FROM extraction_jobs WHERE document_id = $1`, documentID).Scan(&got); err != nil {
		t.Fatalf("read the verdict back: %v", err)
	}
	if got == nil || *got != verdict {
		t.Fatalf("the verdict row holds %v, want %s -- the comparison below would hold over nothing", got, verdict)
	}
}

// dtNormalise blanks what differs between two tenants' runs of one reading.
func dtNormalise(r BatchResult) BatchResult {
	r.ID = ""
	for i := range r.Errors {
		r.Errors[i].InvoiceID = ""
	}
	for i := range r.InvoiceViolations {
		r.InvoiceViolations[i].InvoiceID = ""
	}
	return r
}

func TestServiceImportDocument_ANonNoteVerdictChangesNoOutcome(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	svc := newTestService(app)

	for _, verdict := range []string{"receipt", "proforma", "quotation", "delivery note", "statement", "purchase order"} {
		for _, tc := range []struct {
			name        string
			seed        func(t *testing.T, tenantID, documentID string)
			wantReady   int
			wantMessage string
		}{
			{"clean", func(t *testing.T, tenantID, documentID string) {
				docSeedExtraction(t, super, tenantID, documentID, docCleanValues("DT-1"))
			}, 1, ""},
			{"no number", func(t *testing.T, tenantID, documentID string) {
				docSeedExtraction(t, super, tenantID, documentID, docNoNumberValues())
			}, 0, noInvoiceNumberMessage},
			{"poor scan", func(t *testing.T, tenantID, documentID string) {
				dtSeedPoorScan(t, super, tenantID, documentID)
			}, 0, poorScanMessage},
		} {
			t.Run(verdict+"/"+tc.name, func(t *testing.T) {
				type run struct {
					tenantID, documentID string
					res                  BatchResult
				}
				var runs [2]run
				for i, label := range []string{"verdict", "plain"} {
					tenantID := seedTenant(t, super, "document type "+label+" "+tc.name)
					entityID := seedEntity(t, super, tenantID, "document type "+label)
					documentID := docSeedDocument(t, super, tenantID)
					tc.seed(t, tenantID, documentID)
					if label == "verdict" {
						dtSetVerdict(t, super, documentID, verdict)
					}
					res, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
					if err != nil {
						t.Fatalf("%s: ImportDocument: %v", label, err)
					}
					runs[i] = run{tenantID, documentID, res}
				}
				withV, without := runs[0].res, runs[1].res

				if without.ReadyInvoices != tc.wantReady {
					t.Fatalf("the plain run filed %d invoice(s), want %d: %+v", without.ReadyInvoices, tc.wantReady, without)
				}
				if tc.wantMessage != "" && (len(without.Errors) != 1 || without.Errors[0].Message != tc.wantMessage) {
					t.Fatalf("the plain run's errors = %+v, want one carrying %q", without.Errors, tc.wantMessage)
				}
				if a, b := dtNormalise(withV), dtNormalise(without); !reflect.DeepEqual(a, b) {
					t.Errorf("BatchResult with a verdict differs from without:\n with:    %+v\n without: %+v", a, b)
				}

				if tc.name != "no number" {
					return
				}
				var readings [2]*CarriedReading
				for i, r := range runs {
					reading, err := svc.CarriedReading(sxIdentity(ctx, r.tenantID), r.documentID)
					if err != nil || reading == nil {
						t.Fatalf("CarriedReading for run %d: reading=%v err=%v, want a reading", i, reading, err)
					}
					cp := *reading
					cp.DocumentID, cp.ExtractionJobID = "", ""
					readings[i] = &cp
				}
				if !reflect.DeepEqual(readings[0], readings[1]) {
					t.Errorf("CarriedReading with a verdict differs from without:\n with:    %+v\n without: %+v", *readings[0], *readings[1])
				}
			})
		}
	}
}

// dtHeld seeds a tenant, entity and document with a clean reading under verdict.
func dtHeld(t *testing.T, super *pgxpool.Pool, label, verdict string, values map[string]*string) (tenantID, entityID, documentID string) {
	t.Helper()
	tenantID = seedTenant(t, super, label)
	entityID = seedEntity(t, super, tenantID, label)
	documentID = docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, values)
	if verdict != "" {
		dtSetVerdict(t, super, documentID, verdict)
	}
	return
}

func dtBatchCount(t *testing.T, super *pgxpool.Pool, documentID string) int {
	t.Helper()
	return csCount(t, super, `SELECT count(*) FROM import_batches WHERE document_id = $1`, documentID)
}

func dtWantHold(t *testing.T, res BatchResult) {
	t.Helper()
	if res.Status != "completed" || res.ReadyInvoices != 0 || res.RowsInvalid != 1 || res.QuarantinedInvoices != 1 ||
		len(res.Errors) != 1 || res.Errors[0].Field != "original_invoice_id" || res.Errors[0].Message != noteHeldMessage {
		t.Errorf("result = %+v, want a completed hold on original_invoice_id with %q", res, noteHeldMessage)
	}
}

func TestServiceImportDocument_ANoteVerdictHoldsAndCreatesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	svc := newTestService(app)
	for _, verdict := range []string{"credit note", "debit note"} {
		t.Run(verdict, func(t *testing.T) {
			tenantID, entityID, documentID := dtHeld(t, super, "hold "+verdict, verdict, docCleanValues("HOLD-1"))
			res, err := svc.ImportDocument(sxIdentity(context.Background(), tenantID), entityID, documentID)
			if err != nil {
				t.Fatalf("ImportDocument: %v", err)
			}
			dtWantHold(t, res)
			if n := countInvoicesCitingDocument(t, super, documentID); n != 0 {
				t.Errorf("invoices citing the document = %d, want 0", n)
			}
			if n := dtBatchCount(t, super, documentID); n != 1 {
				t.Errorf("batches for the document = %d, want 1", n)
			}
		})
	}
}

func TestServiceImportDocument_ThisIsAnInvoiceOverridesTheVerdict(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID, entityID, documentID := dtHeld(t, super, "override", "credit note", docCleanValues("OVR-1"))
	res, err := newTestService(app).ImportDocumentFor(sxIdentity(context.Background(), tenantID),
		DocumentImport{EntityID: entityID, DocumentID: documentID, DocumentType: "commercial_invoice"})
	if err != nil || res.ReadyInvoices != 1 {
		t.Fatalf("override: res %+v, err %v, want one ready invoice", res, err)
	}
	var typ string
	var original *string
	if err := super.QueryRow(context.Background(),
		`SELECT document_type, original_invoice_id FROM invoices WHERE source_document_id = $1`, documentID).Scan(&typ, &original); err != nil {
		t.Fatalf("read the filed invoice: %v", err)
	}
	if typ != "commercial_invoice" || original != nil {
		t.Errorf("filed as %s with original %v, want commercial_invoice and none", typ, original)
	}
}

func TestServiceImportDocument_OverrideWithNoNumberQuarantinesAsToday(t *testing.T) {
	super, app := dbTestPools(t)
	svc := newTestService(app)
	tenantID, entityID, documentID := dtHeld(t, super, "override no number", "debit note", docNoNumberValues())
	ctx := sxIdentity(context.Background(), tenantID)
	res, err := svc.ImportDocumentAsInvoice(ctx, entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocumentAsInvoice: %v", err)
	}
	if res.ReadyInvoices != 0 || len(res.Errors) != 1 || res.Errors[0].Message != noInvoiceNumberMessage {
		t.Errorf("result = %+v, want one row with %q", res, noInvoiceNumberMessage)
	}
	if reading, err := svc.CarriedReading(ctx, documentID); err != nil || reading == nil {
		t.Errorf("CarriedReading = %v, %v, want a reading", reading, err)
	}
}

func TestServiceImportDocument_TheNewestJobDecidesTheHold(t *testing.T) {
	super, app := dbTestPools(t)
	svc := newTestService(app)
	for _, tc := range []struct {
		name         string
		older, newer string
		wantHold     bool
	}{
		{"newer credit note", "receipt", "credit note", true},
		{"newer receipt", "credit note", "receipt", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tenantID := seedTenant(t, super, "newest job "+tc.name)
			entityID := seedEntity(t, super, tenantID, "newest job")
			documentID := docSeedDocument(t, super, tenantID)
			now := time.Now().UTC()
			old := seedExtractionJob(t, super, tenantID, documentID, "succeeded", now.Add(-time.Hour))
			if _, err := super.Exec(context.Background(), `UPDATE extraction_jobs SET document_type = $2 WHERE id = $1`, old, tc.older); err != nil {
				t.Fatalf("set the older verdict: %v", err)
			}
			newer := seedExtractionJob(t, super, tenantID, documentID, "succeeded", now)
			if _, err := super.Exec(context.Background(), `UPDATE extraction_jobs SET document_type = $2 WHERE id = $1`, newer, tc.newer); err != nil {
				t.Fatalf("set the newer verdict: %v", err)
			}
			for name, v := range docCleanValues("NEWEST-1") {
				seedExtractionField(t, super, tenantID, newer, name, v, nil, 0, now)
			}
			res, err := svc.ImportDocument(sxIdentity(context.Background(), tenantID), entityID, documentID)
			if err != nil {
				t.Fatalf("ImportDocument: %v", err)
			}
			if tc.wantHold {
				dtWantHold(t, res)
			} else if res.ReadyInvoices != 1 {
				t.Errorf("result = %+v, want the invoice filed", res)
			}
		})
	}
}

func TestServiceImportDocument_AFiledNoteVerdictDoesNotHoldOrFileAgain(t *testing.T) {
	super, app := dbTestPools(t)
	svc := newTestService(app)
	for _, tc := range []struct {
		name string
		file func(tenantID, entityID, documentID string) string
	}{
		{"filed as a note under a typed number", func(tenantID, entityID, documentID string) string {
			original := seedInvoice(t, super, tenantID, entityID, "ORIG-1")
			var id string
			if err := super.QueryRow(context.Background(),
				`INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id, document_type, original_invoice_id)
				 VALUES ($1, $2, 'CN-TYPED', $3, 'credit_note', $4) RETURNING id`, tenantID, entityID, documentID, original).Scan(&id); err != nil {
				t.Fatalf("seed the filed note: %v", err)
			}
			return id
		}},
		{"filed as an invoice", func(tenantID, entityID, documentID string) string {
			return seedInvoiceForDocument(t, super, tenantID, entityID, documentID, "FILED-INV")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tenantID, entityID, documentID := dtHeld(t, super, "filed "+tc.name, "credit note", docCleanValues("READ-NUM"))
			filedID := tc.file(tenantID, entityID, documentID)
			res, err := svc.ImportDocument(sxIdentity(context.Background(), tenantID), entityID, documentID)
			if err != nil {
				t.Fatalf("ImportDocument: %v", err)
			}
			if res.ReadyInvoices != 0 || len(res.Errors) != 1 {
				t.Fatalf("result = %+v, want no invoice and one row", res)
			}
			got := res.Errors[0]
			if got.RuleKey != ruleKeyDuplicateInvoiceNumber || got.InvoiceID != filedID || got.Message != documentAlreadyFiledReason {
				t.Errorf("row = %+v, want rule_key %q, invoice_id %s, message %q", got, ruleKeyDuplicateInvoiceNumber, filedID, documentAlreadyFiledReason)
			}
			if n := countInvoicesCitingDocument(t, super, documentID); n != 1 {
				t.Errorf("invoices citing the document = %d, want 1", n)
			}
		})
	}
}

func TestServiceImportDocument_ThisIsAnInvoiceRefusesAFiledDocument(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID, entityID, documentID := dtHeld(t, super, "refuse filed", "credit note", docCleanValues("RF-1"))
	seedInvoiceForDocument(t, super, tenantID, entityID, documentID, "RF-FILED")
	_, err := newTestService(app).ImportDocumentAsInvoice(sxIdentity(context.Background(), tenantID), entityID, documentID)
	if !errors.Is(err, ErrDocumentAlreadyFiled) {
		t.Fatalf("err = %v, want ErrDocumentAlreadyFiled", err)
	}
	if n := dtBatchCount(t, super, documentID); n != 0 {
		t.Errorf("batches for the document = %d, want 0", n)
	}
	if n := countInvoicesCitingDocument(t, super, documentID); n != 1 {
		t.Errorf("invoices citing the document = %d, want 1", n)
	}
}

func TestGetBatch_AHoldRowEndsWhenTheDocumentIsFiled(t *testing.T) {
	super, app := dbTestPools(t)
	svc := newTestService(app)
	tenantID, entityID, documentID := dtHeld(t, super, "hold ends", "credit note", docCleanValues("HE-1"))
	ctx := sxIdentity(context.Background(), tenantID)
	res, err := svc.ImportDocument(ctx, entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	b, err := svc.batch.GetBatch(ctx, res.ID)
	if err != nil || len(b.Errors) != 1 || b.RowsInvalid != 1 {
		t.Fatalf("hold batch: %+v, %v, want one row and rows_invalid 1", b, err)
	}
	seedInvoiceForDocument(t, super, tenantID, entityID, documentID, "HE-FILED")
	b, err = svc.batch.GetBatch(ctx, res.ID)
	if err != nil || len(b.Errors) != 0 || b.RowsInvalid != 0 {
		t.Errorf("filed: %+v, %v, want no row and rows_invalid 0", b, err)
	}
	if n := csCount(t, super, `SELECT jsonb_array_length(errors) FROM import_batches WHERE id = $1`, res.ID); n != 1 {
		t.Errorf("stored errors = %d, want the hold row kept", n)
	}
}

func TestGetBatch_OtherRowsOfAFiledDocumentStay(t *testing.T) {
	super, app := dbTestPools(t)
	svc := newTestService(app)
	tenantID, entityID, documentID := dtHeld(t, super, "other rows", "", docNoNumberValues())
	ctx := sxIdentity(context.Background(), tenantID)
	res, err := svc.ImportDocument(ctx, entityID, documentID)
	if err != nil || len(res.Errors) != 1 {
		t.Fatalf("ImportDocument: %+v, %v, want a quarantined row", res, err)
	}
	seedInvoiceForDocument(t, super, tenantID, entityID, documentID, "OR-FILED")
	b, err := svc.batch.GetBatch(ctx, res.ID)
	if err != nil || len(b.Errors) != 1 || b.Errors[0].Field != "invoice_number" || b.RowsInvalid != 1 {
		t.Errorf("batch = %+v, %v, want the no-number row and rows_invalid 1", b, err)
	}
}

func TestSupplyInvoiceNumber_AHeldNoteVerdictFilesAnInvoice(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID, entityID, documentID := dtHeld(t, super, "supply held", "credit note", docNoNumberValues())
	ctx := sxIdentity(context.Background(), tenantID)
	svc := newTestServiceWithGate(app, &fakeGate{})
	res, err := svc.ImportDocument(ctx, entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	dtWantHold(t, res)
	if _, err := svc.SupplyInvoiceNumber(ctx, entityID, documentID, "SH-1"); err != nil {
		t.Fatalf("SupplyInvoiceNumber: %v", err)
	}
	var typ string
	var original *string
	if err := super.QueryRow(context.Background(),
		`SELECT document_type, original_invoice_id FROM invoices WHERE source_document_id = $1`, documentID).Scan(&typ, &original); err != nil {
		t.Fatalf("read the filed invoice: %v", err)
	}
	if typ != "commercial_invoice" || original != nil {
		t.Errorf("filed as %s with original %v, want commercial_invoice and none", typ, original)
	}
	if b, err := svc.batch.GetBatch(ctx, res.ID); err != nil || len(b.Errors) != 0 || b.RowsInvalid != 0 {
		t.Errorf("hold batch after filing: %+v, %v, want no hold row", b, err)
	}
}
