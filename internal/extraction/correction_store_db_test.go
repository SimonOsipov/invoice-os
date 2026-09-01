// correction_store_db_test.go: DB-backed specs for the append-only correction record. Shares
// store_db_test.go's harness (stRequire/stTenant), so this file adds no second skip site.
package extraction_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// The actor every correction here is written by: a raw GoTrue subject, the convention
// audit_log.actor already follows.
const csActor = "8f1c0d64-4c2e-4a1b-9d33-6f5f0f2c7a01"

func csStore(t *testing.T) *extraction.CorrectionStore {
	t.Helper()
	return &extraction.CorrectionStore{Pool: stRequire(t).app}
}

// csJob seeds a tenant, a document and one extraction job as the superuser. stTenant's own
// cleanup deletes the tenant, and the cascade reaches the job.
func csJob(t *testing.T, ctx context.Context) (tenantID, jobID string) {
	t.Helper()
	tenantID, documentID := stTenant(t, ctx)
	jobID = uuid.NewString()
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
		 VALUES ($1, $2, $3, $4, $5)`,
		jobID, tenantID, documentID, stExtractor, stExtractorVersion); err != nil {
		t.Fatalf("seed extraction job: %v", err)
	}
	return tenantID, jobID
}

func csByField(cs []extraction.Correction) map[string]extraction.Correction {
	out := map[string]extraction.Correction{}
	for _, c := range cs {
		out[c.FieldName] = c
	}
	return out
}

// AC-6: created_at defaults to now(), which is transaction-constant, so two corrections
// written in one transaction tie on it exactly and only seq still separates them. Written
// through raw SQL because Append opens its own transaction, so two calls could never tie.
// Twenty independent fields: an order that came out right by luck comes out right about half
// the time, and one repetition cannot tell that from a guarantee.
func TestExtractionCorrectionStore_TiedCreatedAtStillOrdersTotally(t *testing.T) {
	ctx := t.Context()
	h := stRequire(t)
	s := csStore(t)
	tenantID, jobID := csJob(t, ctx)

	for i := range 20 {
		field := fmt.Sprintf("line_total_%02d", i)
		first := fmt.Sprintf("first-%02d", i)
		second := fmt.Sprintf("second-%02d", i)

		if err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
			for _, v := range []string{first, second} {
				if _, err := tx.Exec(ctx,
					`INSERT INTO extraction_field_corrections
					     (tenant_id, extraction_job_id, field_name, value, method, actor)
					 VALUES ($1, $2, $3, $4, 'typed', $5)`,
					tenantID, jobID, field, v, csActor); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("write the tied pair for %s: %v", field, err)
		}

		var createdAts, seqs int
		if err := h.super.QueryRow(ctx,
			`SELECT count(DISTINCT created_at), count(DISTINCT seq)
			   FROM extraction_field_corrections
			  WHERE extraction_job_id = $1 AND field_name = $2`,
			jobID, field).Scan(&createdAts, &seqs); err != nil {
			t.Fatalf("read back the pair for %s: %v", field, err)
		}
		// Without the tie the ordering assertion below proves nothing.
		if createdAts != 1 {
			t.Fatalf("%s: the pair holds %d distinct created_at value(s), want 1 — the rows did not tie, "+
				"so this repetition tests nothing", field, createdAts)
		}
		if seqs != 2 {
			t.Errorf("%s: the pair holds %d distinct seq value(s), want 2 — seq must advance per row "+
				"insert, not per transaction", field, seqs)
		}

		got, err := s.LatestPerField(ctx, tenantID, jobID)
		if err != nil {
			t.Fatalf("LatestPerField after writing %s: %v", field, err)
		}
		c, ok := csByField(got)[field]
		if !ok {
			t.Fatalf("LatestPerField returned no row for %s (%d row(s) for the job)", field, len(got))
		}
		if c.Value != second {
			t.Errorf("LatestPerField(%s).Value = %q, want %q — the later row of a created_at tie",
				field, c.Value, second)
		}
	}
}

// AC-7: one row per field, each the newest. The third correction supersedes the first on the
// same field, so a read that returned every row would come back with three.
func TestExtractionCorrectionStore_LatestPerFieldReturnsOneRowPerField(t *testing.T) {
	ctx := t.Context()
	s := csStore(t)
	tenantID, jobID := csJob(t, ctx)

	pointedBox := extraction.Region{Page: 1, X0: 0.1, Y0: 0.2, X1: 0.3, Y1: 0.4}
	for _, c := range []extraction.Correction{
		{FieldName: "total_amount", Value: "100.00", Method: extraction.MethodTyped, Actor: csActor},
		{FieldName: "invoice_number", Value: "INV-1", Method: extraction.MethodChosen, Actor: csActor},
		{FieldName: "total_amount", Value: "212.50", Method: extraction.MethodPointed, Actor: csActor,
			Region: &pointedBox, AnchorLabel: "Total"},
	} {
		appended, err := s.Append(ctx, tenantID, jobID, c)
		if err != nil {
			t.Fatalf("Append %s=%s: %v", c.FieldName, c.Value, err)
		}
		if appended.ID == "" || appended.Seq == 0 || appended.CreatedAt.IsZero() {
			t.Errorf("Append %s returned {ID %q, Seq %d, CreatedAt %v}; the database assigns all three",
				c.FieldName, appended.ID, appended.Seq, appended.CreatedAt)
		}
	}

	got, err := s.LatestPerField(ctx, tenantID, jobID)
	if err != nil {
		t.Fatalf("LatestPerField: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("LatestPerField returned %d row(s) (%+v), want 2 — one per field", len(got), got)
	}
	if got[0].FieldName != "invoice_number" || got[1].FieldName != "total_amount" {
		t.Fatalf("LatestPerField field order = [%s %s], want [invoice_number total_amount]",
			got[0].FieldName, got[1].FieldName)
	}

	latest := got[1]
	if latest.Value != "212.50" {
		t.Errorf("total_amount value = %q, want %q — the third correction supersedes the first",
			latest.Value, "212.50")
	}
	if latest.Method != extraction.MethodPointed {
		t.Errorf("total_amount method = %q, want %q", latest.Method, extraction.MethodPointed)
	}
	if latest.AnchorLabel != "Total" {
		t.Errorf("total_amount anchor_label = %q, want %q", latest.AnchorLabel, "Total")
	}
	if latest.Region == nil {
		t.Fatal("total_amount carries no Region; a pointed correction always does")
	}
	if *latest.Region != pointedBox {
		t.Errorf("total_amount region = %+v, want %+v", *latest.Region, pointedBox)
	}
	// The negative half: a chosen correction reads back with no box at all.
	if got[0].Region != nil {
		t.Errorf("invoice_number carries a Region %+v; only a pointed correction does", *got[0].Region)
	}
	if got[0].Value != "INV-1" {
		t.Errorf("invoice_number value = %q, want %q", got[0].Value, "INV-1")
	}
}
