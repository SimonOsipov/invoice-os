package invoice

import (
	"context"
	"errors"
	"testing"

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
