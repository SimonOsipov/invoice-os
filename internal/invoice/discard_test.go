package invoice

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func discardBatch(t *testing.T, super *pgxpool.Pool, tenant, entity string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO import_batches (tenant_id, entity_id, status) VALUES ($1, $2, 'completed') RETURNING id`,
		tenant, entity).Scan(&id); err != nil {
		t.Fatalf("seed batch: %v", err)
	}
	return id
}

func discardInvoice(t *testing.T, super *pgxpool.Pool, tenant, entity, batch, number string, status Status) string {
	t.Helper()
	id := seedInvoiceAtStatus(t, super, tenant, entity, number, status)
	if _, err := super.Exec(context.Background(), `UPDATE invoices SET import_batch_id = $2 WHERE id = $1`, id, batch); err != nil {
		t.Fatalf("link invoice to batch: %v", err)
	}
	return id
}

// discardState is "status/kept/history" per invoice id, in id order.
func discardState(t *testing.T, super *pgxpool.Pool, ids ...string) string {
	t.Helper()
	var out []string
	for _, id := range ids {
		var s string
		if err := super.QueryRow(context.Background(),
			`SELECT i.status || '/' || coalesce(i.kept_as_is_at::text, '-') || '/' ||
			        (SELECT count(*) FROM invoice_status_history h WHERE h.invoice_id = i.id AND h.to_status = 'discarded')
			   FROM invoices i WHERE i.id = $1`, id).Scan(&s); err != nil {
			t.Fatalf("read invoice %s: %v", id, err)
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

func TestDiscardBatchTx_TouchesOnlyTheBatchsInvoices(t *testing.T) {
	r := newSARig(t, "SECAUD-50-03 discard")
	one := discardBatch(t, r.super, r.tenant, r.entity)
	two := discardBatch(t, r.super, r.tenant, r.entity)
	a1 := discardInvoice(t, r.super, r.tenant, r.entity, one, "DS-1-DRAFT", StatusDraft)
	a2 := discardInvoice(t, r.super, r.tenant, r.entity, one, "DS-1-VALID", StatusValidated)
	b1 := discardInvoice(t, r.super, r.tenant, r.entity, two, "DS-2-DRAFT", StatusDraft)
	b2 := discardInvoice(t, r.super, r.tenant, r.entity, two, "DS-2-VALID", StatusValidated)
	untouchedBefore := discardState(t, r.super, b1, b2)

	var n int
	err := db.WithinRequestTenantTx(r.ctx, r.app, func(tx pgx.Tx) error {
		var derr error
		n, derr = DiscardBatchTx(r.ctx, tx, one, actorFromContext(r.ctx))
		return derr
	})
	if err != nil {
		t.Fatalf("DiscardBatchTx: %v", err)
	}
	if n != 2 {
		t.Errorf("discarded = %d, want 2", n)
	}
	if got, want := discardState(t, r.super, a1, a2), "discarded/-/1 discarded/-/1"; got != want {
		t.Errorf("the discarded batch's invoices = %q, want %q", got, want)
	}
	if got := discardState(t, r.super, b1, b2); got != untouchedBefore {
		t.Errorf("the other batch's invoices = %q, want %q (unchanged)", got, untouchedBefore)
	}
	var from, actor string
	if err := r.super.QueryRow(context.Background(),
		`SELECT from_status, actor FROM invoice_status_history WHERE invoice_id = $1 AND to_status = 'discarded'`, a2).Scan(&from, &actor); err != nil {
		t.Fatalf("read the discard history row: %v", err)
	}
	if from != "validated" || actor != memberSubject {
		t.Errorf("history row = %s by %s, want validated by %s", from, actor, memberSubject)
	}
}
