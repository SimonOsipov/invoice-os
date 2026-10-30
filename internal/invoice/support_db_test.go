package invoice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type supportFix struct {
	super, app *pgxpool.Pool
	store      *Store
	tenantID   string
	entityID   string
}

func newSupportFix(t *testing.T, label string) supportFix {
	t.Helper()
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, label+" tenant")
	return supportFix{super, app, NewStore(app), tenantID, seedEntity(t, super, tenantID, label+" entity")}
}

func (f supportFix) run(t *testing.T, fn func(ctx context.Context, tx pgx.Tx) error) error {
	t.Helper()
	return db.WithinTenantTx(context.Background(), f.app, f.tenantID, func(tx pgx.Tx) error {
		return fn(context.Background(), tx)
	})
}

type supportRow struct {
	status      string
	kind        *string
	history     int
	transitions int
}

func (f supportFix) read(t *testing.T, id string) supportRow {
	t.Helper()
	var r supportRow
	if err := f.super.QueryRow(context.Background(),
		`SELECT status, failure_kind,
		        (SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1::uuid),
		        (SELECT count(*) FROM audit_log WHERE event = 'invoice.transitioned' AND payload->>'id' = $1::uuid::text)
		   FROM invoices WHERE id = $1::uuid`, id,
	).Scan(&r.status, &r.kind, &r.history, &r.transitions); err != nil {
		t.Fatalf("read invoice %s: %v", id, err)
	}
	return r
}

// lastHistory returns the newest history row's from, to and actor.
func (f supportFix) lastHistory(t *testing.T, id string) (from, to, actor string) {
	t.Helper()
	if err := f.super.QueryRow(context.Background(),
		`SELECT from_status, to_status, actor FROM invoice_status_history
		  WHERE invoice_id = $1 ORDER BY changed_at DESC LIMIT 1`, id,
	).Scan(&from, &to, &actor); err != nil {
		t.Fatalf("read history: %v", err)
	}
	return
}

func (f supportFix) lastAudit(t *testing.T, id string) (actor, number string) {
	t.Helper()
	if err := f.super.QueryRow(context.Background(),
		`SELECT actor, payload->>'invoice_number' FROM audit_log
		  WHERE event = 'invoice.transitioned' AND payload->>'id' = $1::text
		  ORDER BY created_at DESC LIMIT 1`, id,
	).Scan(&actor, &number); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	return
}

func TestRequeueFailedTx_MovesFailedToQueuedAndClearsTheKind(t *testing.T) {
	f := newSupportFix(t, "SUPP-06-04 requeue")
	id := seedInvoiceAtStatus(t, f.super, f.tenantID, f.entityID, "SUPP-06-04-REQ", StatusFailed)
	if _, err := f.super.Exec(context.Background(),
		`UPDATE invoices SET failure_kind = 'never_acknowledged' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	before := f.read(t, id)

	if err := f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		return f.store.RequeueFailedTx(ctx, tx, id, f.tenantID)
	}); err != nil {
		t.Fatalf("RequeueFailedTx: %v", err)
	}

	got := f.read(t, id)
	if got.status != "queued" || got.kind != nil {
		t.Errorf("status=%q failure_kind=%v, want queued and NULL", got.status, got.kind)
	}
	if got.history != before.history+1 || got.transitions != before.transitions+1 {
		t.Errorf("history %d->%d, audit %d->%d, want one new row each", before.history, got.history, before.transitions, got.transitions)
	}
	if from, to, actor := f.lastHistory(t, id); from != "failed" || to != "queued" || actor != audit.SupportActor {
		t.Errorf("history = %s->%s by %q, want failed->queued by %q", from, to, actor, audit.SupportActor)
	}
	if actor, number := f.lastAudit(t, id); actor != audit.SupportActor || number != "SUPP-06-04-REQ" {
		t.Errorf("audit actor=%q invoice_number=%q, want %q and SUPP-06-04-REQ", actor, number, audit.SupportActor)
	}
}

func TestRequeueFailedTx_RefusesEveryOtherStatus(t *testing.T) {
	f := newSupportFix(t, "SUPP-06-04 requeue refuse")
	for _, st := range allStatuses {
		if st == StatusFailed {
			continue
		}
		t.Run(string(st), func(t *testing.T) {
			id := seedInvoiceAtStatus(t, f.super, f.tenantID, f.entityID, "SUPP-06-04-RR-"+string(st), st)
			before := f.read(t, id)
			err := f.run(t, func(ctx context.Context, tx pgx.Tx) error {
				return f.store.RequeueFailedTx(ctx, tx, id, f.tenantID)
			})
			var se StatusError
			if !errors.As(err, &se) || se.Have != st {
				t.Fatalf("err = %v, want StatusError{Have: %s}", err, st)
			}
			if !errors.Is(err, ErrIllegalTransition) {
				t.Errorf("err does not match ErrIllegalTransition")
			}
			if after := f.read(t, id); after != before {
				t.Errorf("row changed: %+v -> %+v", before, after)
			}
		})
	}
}

func TestRequeueFailedTx_RefusesAResolvedOutsideInvoice(t *testing.T) {
	f := newSupportFix(t, "SUPP-06-04 resolved outside")
	id := seedInvoiceAtStatus(t, f.super, f.tenantID, f.entityID, "SUPP-06-04-KEPT", StatusFailed)
	if _, err := f.super.Exec(context.Background(),
		`UPDATE invoices SET kept_as_is_at = now(), kept_as_is_by = 'someone', kept_as_is_reason = 'filed by hand' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	before := f.read(t, id)
	err := f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		return f.store.RequeueFailedTx(ctx, tx, id, f.tenantID)
	})
	if !errors.Is(err, ErrResolvedOutside) {
		t.Fatalf("err = %v, want ErrResolvedOutside", err)
	}
	if after := f.read(t, id); after != before {
		t.Errorf("row changed: %+v -> %+v", before, after)
	}
}

func TestCancelQueuedTx_MovesQueuedToFailedWithNoKind(t *testing.T) {
	f := newSupportFix(t, "SUPP-06-04 cancel")
	id := seedInvoiceAtStatus(t, f.super, f.tenantID, f.entityID, "SUPP-06-04-CAN", StatusQueued)
	before := f.read(t, id)

	if err := f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		return f.store.CancelQueuedTx(ctx, tx, id, f.tenantID)
	}); err != nil {
		t.Fatalf("CancelQueuedTx: %v", err)
	}

	got := f.read(t, id)
	if got.status != "failed" || got.kind != nil {
		t.Errorf("status=%q failure_kind=%v, want failed and NULL", got.status, got.kind)
	}
	if got.history != before.history+1 || got.transitions != before.transitions+1 {
		t.Errorf("want one new history row and one new audit row")
	}
	if from, to, actor := f.lastHistory(t, id); from != "queued" || to != "failed" || actor != audit.SupportActor {
		t.Errorf("history = %s->%s by %q, want queued->failed by %q", from, to, actor, audit.SupportActor)
	}
	if actor, _ := f.lastAudit(t, id); actor != audit.SupportActor {
		t.Errorf("audit actor = %q, want %q", actor, audit.SupportActor)
	}
}

func TestCancelQueuedTx_RefusesEveryOtherStatus(t *testing.T) {
	f := newSupportFix(t, "SUPP-06-04 cancel refuse")
	for _, st := range allStatuses {
		if st == StatusQueued {
			continue
		}
		t.Run(string(st), func(t *testing.T) {
			id := seedInvoiceAtStatus(t, f.super, f.tenantID, f.entityID, "SUPP-06-04-CR-"+string(st), st)
			before := f.read(t, id)
			err := f.run(t, func(ctx context.Context, tx pgx.Tx) error {
				return f.store.CancelQueuedTx(ctx, tx, id, f.tenantID)
			})
			var se StatusError
			if !errors.As(err, &se) || se.Have != st {
				t.Fatalf("err = %v, want StatusError{Have: %s}", err, st)
			}
			if after := f.read(t, id); after != before {
				t.Errorf("row changed: %+v -> %+v", before, after)
			}
		})
	}
}

func TestTransition_CustomerRouteStillRefusesFailedToQueued(t *testing.T) {
	f := newSupportFix(t, "SUPP-06-04 customer route")
	id := seedInvoiceAtStatus(t, f.super, f.tenantID, f.entityID, "SUPP-06-04-CUST", StatusFailed)
	_, err := f.store.Transition(tenantCtx(f.tenantID), id, StatusQueued)
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("Transition(failed->queued) err = %v, want ErrIllegalTransition", err)
	}
	if got := f.read(t, id); got.status != "failed" {
		t.Errorf("status = %q, want failed", got.status)
	}
}

func TestSupportTransitions_RefuseAnUnknownMalformedOrOtherTenantInvoice(t *testing.T) {
	f := newSupportFix(t, "SUPP-06-04 lookup")
	other := newSupportFix(t, "SUPP-06-04 lookup other")
	foreign := seedInvoiceAtStatus(t, other.super, other.tenantID, other.entityID, "SUPP-06-04-FOREIGN", StatusFailed)
	foreignQueued := seedInvoiceAtStatus(t, other.super, other.tenantID, other.entityID, "SUPP-06-04-FOREIGN-Q", StatusQueued)
	calls := map[string]func(ctx context.Context, tx pgx.Tx, id string) error{
		"requeue": func(ctx context.Context, tx pgx.Tx, id string) error {
			return f.store.RequeueFailedTx(ctx, tx, id, f.tenantID)
		},
		"cancel": func(ctx context.Context, tx pgx.Tx, id string) error {
			return f.store.CancelQueuedTx(ctx, tx, id, f.tenantID)
		},
	}
	cases := []struct {
		name string
		id   func(call string) string
		want error
	}{
		{"unknown", func(string) string { return "00000000-0000-0000-0000-000000000000" }, ErrNotFound},
		{"malformed", func(string) string { return "not-a-uuid" }, ErrValidation},
		{"other tenant", func(call string) string {
			if call == "requeue" {
				return foreign
			}
			return foreignQueued
		}, ErrNotFound},
	}
	for call, fn := range calls {
		for _, c := range cases {
			t.Run(call+"/"+c.name, func(t *testing.T) {
				id := c.id(call)
				err := f.run(t, func(ctx context.Context, tx pgx.Tx) error { return fn(ctx, tx, id) })
				if !errors.Is(err, c.want) {
					t.Fatalf("err = %v, want %v", err, c.want)
				}
			})
		}
	}
	if got := other.read(t, foreign); got.status != "failed" {
		t.Errorf("other tenant's invoice moved to %q", got.status)
	}
	if got := other.read(t, foreignQueued); got.status != "queued" {
		t.Errorf("other tenant's queued invoice moved to %q", got.status)
	}
}

// rqOriginal seeds an accepted commercial original with the given total.
func rqOriginal(t *testing.T, f supportFix, number, total string) string {
	t.Helper()
	id := seedInvoice(t, f.super, f.tenantID, f.entityID, number)
	ncAccepted(t, f.super, id, "IRN-"+number, total)
	return id
}

func (f supportFix) requeue(t *testing.T, id string) error {
	t.Helper()
	return f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		return f.store.RequeueFailedTx(ctx, tx, id, f.tenantID)
	})
}

func TestRequeueFailed_CreditNoteOverTheCapIsRefused(t *testing.T) {
	f := newSupportFix(t, "03-04 over cap")
	o := rqOriginal(t, f, "RQ-O", "100.00")
	a := csNote(t, f.super, f.tenantID, f.entityID, "RQ-A", "credit_note", o, "failed", csAmount("60.00"))
	csNote(t, f.super, f.tenantID, f.entityID, "RQ-B", "credit_note", o, "accepted", csAmount("60.00"))
	before := f.read(t, a)

	err := f.requeue(t, a)
	if !errors.Is(err, ErrNoteChecksFailed) {
		t.Fatalf("RequeueFailedTx = %v, want ErrNoteChecksFailed", err)
	}
	after := f.read(t, a)
	if after.status != "failed" {
		t.Errorf("status = %q, want failed", after.status)
	}
	if after != before {
		t.Errorf("a refused re-drive wrote: %+v -> %+v (history and audit rows must not move)", before, after)
	}
}

func TestRequeueFailed_ANoteWhoseOriginalHasNoIRNIsRefused(t *testing.T) {
	f := newSupportFix(t, "03-04 no irn")
	o := rqOriginal(t, f, "RQ-O", "100.00")
	if _, err := f.super.Exec(context.Background(), `UPDATE invoices SET irn = NULL WHERE id = $1`, o); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"credit_note", "debit_note"} {
		n := csNote(t, f.super, f.tenantID, f.entityID, "RQ-"+typ, typ, o, "failed", csAmount("10.00"))
		if err := f.requeue(t, n); !errors.Is(err, ErrNoteChecksFailed) {
			t.Errorf("%s: RequeueFailedTx = %v, want ErrNoteChecksFailed", typ, err)
		}
		if got := f.read(t, n); got.status != "failed" {
			t.Errorf("%s: status = %q, want failed", typ, got.status)
		}
	}
}

func TestRequeueFailed_ExactlyAtTheCapRequeues(t *testing.T) {
	f := newSupportFix(t, "03-04 at cap")
	o := rqOriginal(t, f, "RQ-O", "100.00")
	a := csNote(t, f.super, f.tenantID, f.entityID, "RQ-A", "credit_note", o, "failed", csAmount("100.00"))

	if err := f.requeue(t, a); err != nil {
		t.Fatalf("RequeueFailedTx at the cap = %v, want nil", err)
	}
	if got := f.read(t, a); got.status != "queued" {
		t.Errorf("status = %q, want queued", got.status)
	}
}

func TestRequeueFailed_CreditNoteThatFitsRequeues(t *testing.T) {
	f := newSupportFix(t, "03-04 fits")
	o := rqOriginal(t, f, "RQ-O", "100.00")
	a := csNote(t, f.super, f.tenantID, f.entityID, "RQ-A", "credit_note", o, "failed", csAmount("40.00"))
	csNote(t, f.super, f.tenantID, f.entityID, "RQ-B", "credit_note", o, "accepted", csAmount("60.00"))

	if err := f.requeue(t, a); err != nil {
		t.Fatalf("RequeueFailedTx = %v, want nil", err)
	}
	if got := f.read(t, a); got.status != "queued" {
		t.Errorf("status = %q, want queued", got.status)
	}
}

func TestRequeueFailed_CommercialAndDebitUnchanged(t *testing.T) {
	f := newSupportFix(t, "03-04 unchanged")
	ci := seedInvoiceAtStatus(t, f.super, f.tenantID, f.entityID, "RQ-CI", StatusFailed)
	if err := f.requeue(t, ci); err != nil {
		t.Fatalf("commercial invoice: %v", err)
	}
	if got := f.read(t, ci); got.status != "queued" {
		t.Errorf("commercial status = %q, want queued", got.status)
	}

	o := rqOriginal(t, f, "RQ-O", "100.00")
	d := csNote(t, f.super, f.tenantID, f.entityID, "RQ-D", "debit_note", o, "failed", csAmount("500.00"))
	// A debit note takes no lock on the original (Decision 44): a held row lock must not stall it.
	hold, err := f.super.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(context.Background())
	if _, err := hold.Exec(context.Background(), `SELECT 1 FROM invoices WHERE id = $1 FOR UPDATE`, o); err != nil {
		t.Fatal(err)
	}
	err = f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return f.store.RequeueFailedTx(ctx, tx, d, f.tenantID)
	})
	if err != nil {
		t.Fatalf("debit note above the original's total: %v, want nil with no wait", err)
	}
	if got := f.read(t, d); got.status != "queued" {
		t.Errorf("debit status = %q, want queued", got.status)
	}
}

func TestRequeueFailed_ResolvedOutsideRefusesFirst(t *testing.T) {
	f := newSupportFix(t, "03-04 resolved first")
	o := rqOriginal(t, f, "RQ-O", "100.00")
	a := csNote(t, f.super, f.tenantID, f.entityID, "RQ-A", "credit_note", o, "failed", csAmount("60.00"))
	csNote(t, f.super, f.tenantID, f.entityID, "RQ-B", "credit_note", o, "accepted", csAmount("60.00"))
	if _, err := f.super.Exec(context.Background(),
		`UPDATE invoices SET kept_as_is_at = now(), kept_as_is_by = 'someone', kept_as_is_reason = 'filed by hand' WHERE id = $1`, a); err != nil {
		t.Fatal(err)
	}

	err := f.requeue(t, a)
	if !errors.Is(err, ErrResolvedOutside) || errors.Is(err, ErrNoteChecksFailed) {
		t.Fatalf("RequeueFailedTx = %v, want ErrResolvedOutside and not ErrNoteChecksFailed", err)
	}
}
