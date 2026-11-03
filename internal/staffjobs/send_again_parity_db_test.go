package staffjobs

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// parityOutcome is what a requeue leaves behind, minus the ids and the actors that may differ.
type parityOutcome struct {
	JobState, InvoiceStatus, FailureKind string
	Attempts                             int
	KeyUnchanged                         bool
	RiverState, RiverKind                string
	RiverArgsMatch                       bool
	HistoryRows                          int
	HistoryFrom, HistoryTo               string
}

// outcome reads the post-state of r after a requeue and the actors of its transition rows.
func (r *redriveRig) outcome(t *testing.T) (parityOutcome, string, string) {
	t.Helper()
	ctx := context.Background()
	var o parityOutcome
	var key string
	var riverID int64
	if err := r.super.QueryRow(ctx,
		`SELECT state, attempts, idempotency_key, COALESCE(river_job_id, 0) FROM submission_jobs WHERE id = $1`, r.job,
	).Scan(&o.JobState, &o.Attempts, &key, &riverID); err != nil {
		t.Fatal(err)
	}
	o.KeyUnchanged = key == r.args.IdempotencyKey
	if err := r.super.QueryRow(ctx, `SELECT status, COALESCE(failure_kind, '') FROM invoices WHERE id = $1`, r.invoice).Scan(&o.InvoiceStatus, &o.FailureKind); err != nil {
		t.Fatal(err)
	}
	if row, ok := r.riverRow(t, riverID); ok {
		o.RiverState, o.RiverKind, o.RiverArgsMatch = row.State, row.Kind, row.Args == r.args
	}
	var historyActor string
	if err := r.super.QueryRow(ctx, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, r.invoice).Scan(&o.HistoryRows); err != nil {
		t.Fatal(err)
	}
	if err := r.super.QueryRow(ctx,
		`SELECT from_status, to_status, actor FROM invoice_status_history WHERE invoice_id = $1 ORDER BY changed_at DESC LIMIT 1`, r.invoice,
	).Scan(&o.HistoryFrom, &o.HistoryTo, &historyActor); err != nil {
		t.Fatalf("read the transition row: %v", err)
	}
	var auditActor string
	if err := r.super.QueryRow(ctx,
		`SELECT actor FROM audit_log WHERE tenant_id = $1 AND event = 'invoice.transitioned' ORDER BY created_at DESC LIMIT 1`, r.tenant,
	).Scan(&auditActor); err != nil {
		t.Fatalf("read the transition audit row: %v", err)
	}
	if historyActor != auditActor {
		t.Errorf("history actor %q differs from audit actor %q", historyActor, auditActor)
	}
	return o, historyActor, auditActor
}

// The job steps of send again duplicate Redrive's; this keeps the two in line.
func TestRLS_SendAgainLeavesTheSameStateAsRedrive(t *testing.T) {
	viaRedrive := newRedriveRig(t, "dead_lettered", "discarded")
	viaSendAgain := newRedriveRig(t, "dead_lettered", "discarded")

	if _, err := viaRedrive.store.Redrive(viaRedrive.ctx, viaRedrive.job, "APP back up"); err != nil {
		t.Fatalf("Redrive: %v", err)
	}
	customer := uuid.NewString()
	seedMembership(t, viaSendAgain.super, viaSendAgain.tenant, customer)
	q, err := queue.New(viaSendAgain.app, queue.Config{})
	if err != nil {
		t.Fatalf("queue client: %v", err)
	}
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: customer, Role: "authenticated", TenantID: viaSendAgain.tenant})
	got, err := invoice.NewSubmitter(invoice.NewStore(viaSendAgain.app), q).SendAgain(ctx, viaSendAgain.invoice)
	if err != nil {
		t.Fatalf("SendAgain: %v", err)
	}
	if got.Status != invoice.StatusQueued {
		t.Errorf("SendAgain returned status %q, want queued", got.Status)
	}

	want, redriveHistoryActor, _ := viaRedrive.outcome(t)
	have, sendAgainHistoryActor, _ := viaSendAgain.outcome(t)
	if want.JobState != "queued" || want.InvoiceStatus != "queued" || want.FailureKind != "" || !want.KeyUnchanged || !want.RiverArgsMatch ||
		want.RiverState != "available" || want.HistoryRows != 1 || want.HistoryFrom != "failed" || want.HistoryTo != "queued" {
		t.Fatalf("the Redrive baseline is not the expected requeue: %+v", want)
	}
	if have != want {
		t.Errorf("send again and Re-drive differ:\nRe-drive   %+v\nsend again %+v", want, have)
	}
	if redriveHistoryActor != audit.SupportActor {
		t.Errorf("Re-drive actor = %q, want %q", redriveHistoryActor, audit.SupportActor)
	}
	if sendAgainHistoryActor != customer {
		t.Errorf("send again actor = %q, want the customer %q", sendAgainHistoryActor, customer)
	}
}
