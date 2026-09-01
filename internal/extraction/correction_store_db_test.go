// correction_store_db_test.go: DB-backed specs for the append-only correction record. Shares
// store_db_test.go's harness (stRequire/stTenant), so this file adds no second skip site.
package extraction_test

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// The actor every correction here is written by: a raw GoTrue subject, the convention
// audit_log.actor already follows.
const csActor = "8f1c0d64-4c2e-4a1b-9d33-6f5f0f2c7a01"

// csAppend and csLatest open the tenant transaction the deleted CorrectionStore wrappers used
// to open. Each spec composes the tx-taking half itself, the way the production callers do.
func csAppend(t *testing.T, ctx context.Context, tenantID, jobID string, c extraction.Correction) extraction.Correction {
	t.Helper()
	var out extraction.Correction
	if err := db.WithinTenantTx(ctx, stRequire(t).app, tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = extraction.AppendCorrectionForTest(ctx, tx, tenantID, jobID, c)
		return err
	}); err != nil {
		t.Fatalf("append %s=%s: %v", c.FieldName, c.Value, err)
	}
	return out
}

func csLatest(t *testing.T, ctx context.Context, tenantID, jobID string) []extraction.Correction {
	t.Helper()
	out := []extraction.Correction{}
	if err := db.WithinTenantTx(ctx, stRequire(t).app, tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = extraction.LatestCorrectionsPerFieldForTest(ctx, tx, tenantID, jobID)
		return err
	}); err != nil {
		t.Fatalf("read the latest correction per field for job %s: %v", jobID, err)
	}
	return out
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
// through raw SQL because csAppend opens its own transaction, so two calls could never tie.
// Twenty independent fields: an order that came out right by luck comes out right about half
// the time, and one repetition cannot tell that from a guarantee.
func TestExtractionCorrectionStore_TiedCreatedAtStillOrdersTotally(t *testing.T) {
	ctx := t.Context()
	h := stRequire(t)
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

		got := csLatest(t, ctx, tenantID, jobID)
		c, ok := csByField(got)[field]
		if !ok {
			t.Fatalf("the latest-per-field read returned no row for %s (%d row(s) for the job)", field, len(got))
		}
		if c.Value != second {
			t.Errorf("the latest row for %s carries Value %q, want %q — the later row of a created_at tie",
				field, c.Value, second)
		}
	}
}

// AC-7: one row per field, each the newest. The third correction supersedes the first on the
// same field, so a read that returned every row would come back with three.
func TestExtractionCorrectionStore_LatestPerFieldReturnsOneRowPerField(t *testing.T) {
	ctx := t.Context()
	tenantID, jobID := csJob(t, ctx)

	pointedBox := extraction.Region{Page: 1, X0: 0.1, Y0: 0.2, X1: 0.3, Y1: 0.4}
	for _, c := range []extraction.Correction{
		{FieldName: "total_amount", Value: "100.00", Method: extraction.MethodTyped, Actor: csActor},
		{FieldName: "invoice_number", Value: "INV-1", Method: extraction.MethodChosen, Actor: csActor},
		{FieldName: "total_amount", Value: "212.50", Method: extraction.MethodPointed, Actor: csActor,
			Region: &pointedBox, AnchorLabel: "Total"},
	} {
		appended := csAppend(t, ctx, tenantID, jobID, c)
		if appended.ID == "" || appended.Seq == 0 || appended.CreatedAt.IsZero() {
			t.Errorf("appending %s returned {ID %q, Seq %d, CreatedAt %v}; the database assigns all three",
				c.FieldName, appended.ID, appended.Seq, appended.CreatedAt)
		}
	}

	got := csLatest(t, ctx, tenantID, jobID)
	if len(got) != 2 {
		t.Fatalf("the latest-per-field read returned %d row(s) (%+v), want 2 — one per field", len(got), got)
	}
	if got[0].FieldName != "invoice_number" || got[1].FieldName != "total_amount" {
		t.Fatalf("the latest-per-field order = [%s %s], want [invoice_number total_amount]",
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

// The source the latest-per-field query is read out of, and the index it must lean on.
const (
	csStoreSource = "correction_store.go"
	csLatestIndex = "extraction_field_corrections_tenant_job_field_seq_idx"
)

// AC-6's query half. The tied-created_at case above cannot reach it: a tie is settled by
// whatever row order the plan happens to feed a stable sort, so ORDER BY created_at DESC
// reads as correct under an index scan and incorrect under a seq scan. Here the two orderings
// DISAGREE — the row that supersedes carries the EARLIER timestamp — so only seq answers.
func TestExtractionCorrectionStore_HigherSeqWinsAnEarlierCreatedAt(t *testing.T) {
	ctx := t.Context()
	h := stRequire(t)
	tenantID, jobID := csJob(t, ctx)

	if err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		for _, r := range []struct {
			value     string
			createdAt time.Time
		}{
			{"superseded", time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)},
			{"current", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)},
		} {
			if _, err := tx.Exec(ctx,
				`INSERT INTO extraction_field_corrections
				     (tenant_id, extraction_job_id, field_name, value, method, actor, created_at)
				 VALUES ($1, $2, 'total_amount', $3, 'typed', $4, $5)`,
				tenantID, jobID, r.value, csActor, r.createdAt); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("write the disagreeing pair: %v", err)
	}

	// Without a genuine disagreement the assertion below is satisfied by either ordering.
	var bySeq, byCreatedAt string
	if err := h.super.QueryRow(ctx,
		`SELECT (SELECT value FROM extraction_field_corrections
		          WHERE extraction_job_id = $1 ORDER BY seq DESC LIMIT 1),
		        (SELECT value FROM extraction_field_corrections
		          WHERE extraction_job_id = $1 ORDER BY created_at DESC LIMIT 1)`,
		jobID).Scan(&bySeq, &byCreatedAt); err != nil {
		t.Fatalf("read back the disagreeing pair: %v", err)
	}
	if bySeq != "current" || byCreatedAt != "superseded" {
		t.Fatalf("seq DESC picks %q and created_at DESC picks %q, want %q and %q — the pair does "+
			"not disagree, so this test proves nothing", bySeq, byCreatedAt, "current", "superseded")
	}

	got := csLatest(t, ctx, tenantID, jobID)
	if len(got) != 1 {
		t.Fatalf("the latest-per-field read returned %d row(s) (%+v), want 1", len(got), got)
	}
	if got[0].Value != "current" {
		t.Errorf("the latest-per-field read picked %q, want %q — the highest seq, never the latest created_at",
			got[0].Value, "current")
	}
}

// csLatestPerFieldSQL reads the query latestCorrectionsPerFieldTx actually issues out of the
// source, so the EXPLAIN below cannot drift from a copy of it.
func csLatestPerFieldSQL(t *testing.T) string {
	t.Helper()
	f, _ := mxParse(t, csStoreSource)

	var sql string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "latestCorrectionsPerFieldTx" {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if ok && bl.Kind == token.STRING && strings.Contains(bl.Value, "DISTINCT ON") {
				sql = strings.Trim(bl.Value, "`")
			}
			return true
		})
	}
	if sql == "" {
		t.Fatalf("%s: latestCorrectionsPerFieldTx issues no DISTINCT ON query, so this test has "+
			"lost its subject", csStoreSource)
	}
	return sql
}

// AC-7's index half. The read's ORDER BY must be answerable by csLatestIndex's column order
// alone; a Sort node means it no longer is. enable_seqscan=off is the only way to ask on a
// table this small — on cost the planner would never choose the index. The Index Scan is
// asserted first, or "no Sort" would also pass on a plan that never reached the index.
func TestExtractionCorrectionStore_LatestPerFieldOrdersFromTheIndexWithoutASort(t *testing.T) {
	ctx := t.Context()
	h := stRequire(t)
	tenantID, jobID := csJob(t, ctx)

	for _, c := range []extraction.Correction{
		{FieldName: "total_amount", Value: "100.00", Method: extraction.MethodTyped, Actor: csActor},
		{FieldName: "invoice_number", Value: "INV-1", Method: extraction.MethodChosen, Actor: csActor},
		{FieldName: "total_amount", Value: "212.50", Method: extraction.MethodTyped, Actor: csActor},
	} {
		csAppend(t, ctx, tenantID, jobID, c)
	}

	var plan strings.Builder
	if err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+csLatestPerFieldSQL(t), tenantID, jobID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			plan.WriteString(line + "\n")
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("explain the latest-per-field read: %v", err)
	}

	if !strings.Contains(plan.String(), "Index Scan using "+csLatestIndex) {
		t.Fatalf("the plan does not scan %s, so the assertion below examines nothing:\n%s",
			csLatestIndex, plan.String())
	}
	if strings.Contains(plan.String(), "Sort") {
		t.Errorf("the plan sorts, so %s no longer answers ORDER BY field_name, seq DESC on its "+
			"own:\n%s", csLatestIndex, plan.String())
	}
}

// seq is a bigserial, not GENERATED ALWAYS, so a caller-supplied seq is accepted by the
// database — total order rests on the writer never naming the column. The SELECT and
// RETURNING clauses do name it, so only the INSERT's column list is the subject here.
func TestExtractionCorrectionStore_TheInsertNeverNamesSeq(t *testing.T) {
	f, fset := mxParse(t, csStoreSource)

	var checked int
	ast.Inspect(f, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING || !strings.Contains(bl.Value, "INSERT INTO extraction_field_corrections") {
			return true
		}
		checked++
		cols, _, _ := strings.Cut(bl.Value, "VALUES")
		if strings.Contains(cols, "seq") {
			t.Errorf("%s: the INSERT names seq in its column list; a hand-supplied seq breaks the "+
				"total order latestCorrectionsPerFieldTx reads", fset.Position(bl.Pos()))
		}
		return true
	})
	if checked == 0 {
		t.Fatalf("%s issues no INSERT INTO extraction_field_corrections, so this scan examined nothing",
			csStoreSource)
	}
}
