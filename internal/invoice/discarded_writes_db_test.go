package invoice

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// discardedWriteState is every row a write on a discarded invoice could touch.
type discardedWriteState struct {
	sa      saSnap
	keptAt  *string
	audit   int
	history int
}

func (r *saRig) discardedState(t *testing.T, id string) discardedWriteState {
	t.Helper()
	s := discardedWriteState{sa: r.snap(t, id)}
	if err := r.super.QueryRow(context.Background(),
		`SELECT kept_as_is_at::text, (SELECT count(*) FROM audit_log WHERE tenant_id = $2) FROM invoices WHERE id = $1`,
		id, r.tenant).Scan(&s.keptAt, &s.audit); err != nil {
		t.Fatalf("snapshot discarded invoice: %v", err)
	}
	s.history = s.sa.History
	return s
}

func TestDiscarded_EveryWriteIsRefusedAndWritesNothing(t *testing.T) {
	r := newSARig(t, "SECAUD-50-02 writes")
	store := NewStore(r.app)
	// A blocking violation makes KeepAsIs refuse for the status alone.
	id := seedDiscarded(t, r.super, r.tenant, r.entity, "SECAUD-WRITES", "", discardedViolation)
	r.job(t, id, saJob{state: "dead_lettered", attempts: 8, riverState: "discarded"})

	rows := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"KeepAsIs", func(t *testing.T) {
			if _, err := store.KeepAsIs(r.ctx, id, "reason"); !errors.Is(err, ErrNotKeepable) {
				t.Errorf("KeepAsIs err = %v, want ErrNotKeepable", err)
			}
		}},
		{"transition to queued", func(t *testing.T) {
			w := csServe(TransitionHandler(store.Transition, store.CallerRole, nil), r.ctx, "POST", "/", id, `{"target":"queued"}`)
			if w.Code != http.StatusConflict {
				t.Errorf("status = %d, want 409 (body=%s)", w.Code, w.Body.String())
			}
		}},
		{"BatchSubmit", func(t *testing.T) {
			out, err := r.sub.BatchSubmit(r.ctx, BatchSubmitInput{InvoiceIDs: []string{id}, IdempotencyKey: "secaud-50-02-batch"})
			if err != nil {
				t.Fatalf("BatchSubmit: %v", err)
			}
			if len(out.Results) != 1 {
				t.Fatalf("results = %+v, want one row", out.Results)
			}
			if got := out.Results[0]; got.Enqueued || got.Status != string(StatusDiscarded) || got.Reason != batchSubmitReasonNotValidated {
				t.Errorf("result = %+v, want not enqueued, status discarded, reason %q", got, batchSubmitReasonNotValidated)
			}
		}},
		{"SendAgain", func(t *testing.T) {
			_, err := r.sub.SendAgain(r.ctx, id)
			var se StatusError
			if !errors.As(err, &se) || se.Have != StatusDiscarded {
				t.Errorf("SendAgain err = %v, want StatusError{Have: discarded}", err)
			}
		}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			before := r.discardedState(t, id)
			if before.sa.Status != string(StatusDiscarded) || before.history == 0 {
				t.Fatalf("seed: status %q, history %d, want discarded with a history row", before.sa.Status, before.history)
			}
			row.run(t)
			if after := r.discardedState(t, id); after != before {
				t.Errorf("a refused write changed state:\nbefore %+v\nafter  %+v", before, after)
			}
		})
	}
}
