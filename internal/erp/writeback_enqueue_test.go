package erp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

type enqCall struct {
	key  string
	args WriteBackArgs
}

// capQueue records each EnqueueTx instead of inserting.
type capQueue struct {
	mu    sync.Mutex
	calls []enqCall
	err   error
}

func (c *capQueue) EnqueueTx(_ context.Context, _ pgx.Tx, _ string, key string, args river.JobArgs, _ *river.InsertOpts) (bool, error) {
	if c.err != nil {
		return false, c.err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, enqCall{key, args.(WriteBackArgs)})
	return false, nil
}

func realQueue(t *testing.T, app *pgxpool.Pool) *queue.Client {
	t.Helper()
	q, err := queue.New(app, queue.Config{})
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	return q
}

// reach calls the enqueuer in a tenant transaction for the invoice.
func reach(t *testing.T, app *pgxpool.Pool, tenant, invoiceID string, q txEnqueuer) (skipped error) {
	t.Helper()
	ctx := context.Background()
	return db.WithinTenantTx(ctx, app, tenant, func(tx pgx.Tx) error {
		return (&WriteBackEnqueuer{Queue: q}).ResultReached(ctx, tx, tenant, invoiceID)
	})
}

// wbJobs returns the erp.writeback job args and idempotency keys of the tenant.
func wbJobs(t *testing.T, super *pgxpool.Pool, tenant string) (jobs []WriteBackArgs, keys []string) {
	t.Helper()
	ctx := context.Background()
	t.Cleanup(func() { _, _ = super.Exec(ctx, `DELETE FROM river_job WHERE args->>'tenant_id' = $1`, tenant) })
	rs, err := super.Query(ctx, `SELECT args FROM river_job WHERE kind = 'erp.writeback' AND args->>'tenant_id' = $1 ORDER BY id`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	for rs.Next() {
		var raw []byte
		if err := rs.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var a WriteBackArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, a)
	}
	rs.Close()
	ks, err := super.Query(ctx, `SELECT key FROM idempotency_keys WHERE tenant_id = $1 AND key LIKE 'erp.writeback:%' ORDER BY key`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer ks.Close()
	for ks.Next() {
		var k string
		if err := ks.Scan(&k); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	return jobs, keys
}

// setResult moves the invoice to a terminal status with its history row.
func (e *wbEnv) setResult(status, irn, reasons, kind string) {
	e.t.Helper()
	mustExec(e.t, e.super, `UPDATE invoices SET status = $2, irn = nullif($3, ''), rejection_reasons = $4::jsonb, failure_kind = nullif($5, '') WHERE id = $1`,
		e.invoiceID, status, irn, reasons, kind)
	e.history(e.invoiceID, status)
}

func TestEnqueue_LinkedInvoiceGetsOneJob(t *testing.T) {
	for _, state := range []string{"drafted", "linked"} {
		t.Run(state, func(t *testing.T) {
			e := newWB(t, fakeCaps)
			e.doc(e.conn, docOne, state, e.invoiceID, false)
			if err := reach(t, e.app, e.tenant, e.invoiceID, realQueue(t, e.app)); err != nil {
				t.Fatal(err)
			}
			jobs, keys := wbJobs(t, e.super, e.tenant)
			if len(jobs) != 1 || jobs[0].InvoiceID != e.invoiceID {
				t.Fatalf("jobs = %+v, want one for %s", jobs, e.invoiceID)
			}
			if want := "erp.writeback:" + e.invoiceID + ":1"; len(keys) != 1 || keys[0] != want {
				t.Errorf("keys = %v, want [%s]", keys, want)
			}
		})
	}
}

func TestEnqueue_UnlinkedInvoiceGetsNothing(t *testing.T) {
	t.Run("no document", func(t *testing.T) {
		e := newWB(t, fakeCaps)
		if err := reach(t, e.app, e.tenant, e.invoiceID, realQueue(t, e.app)); err != nil {
			t.Fatal(err)
		}
		if jobs, keys := wbJobs(t, e.super, e.tenant); len(jobs) != 0 || len(keys) != 0 {
			t.Errorf("jobs = %v, keys = %v, want none", jobs, keys)
		}
	})
	t.Run("held and failed documents", func(t *testing.T) {
		e := newWB(t, fakeCaps)
		e.doc(e.conn, docOne, "held", "", false)
		e.doc(e.conn, "fake-co-1-inv-2", "failed", "", false)
		if err := reach(t, e.app, e.tenant, e.invoiceID, realQueue(t, e.app)); err != nil {
			t.Fatal(err)
		}
		if jobs, keys := wbJobs(t, e.super, e.tenant); len(jobs) != 0 || len(keys) != 0 {
			t.Errorf("jobs = %v, keys = %v, want none", jobs, keys)
		}
	})
}

func TestEnqueue_EnqueueErrorIsReturned(t *testing.T) {
	e := newWB(t, fakeCaps)
	e.drafted()
	boom := errors.New("enqueue boom")
	if err := reach(t, e.app, e.tenant, e.invoiceID, &capQueue{err: boom}); !errors.Is(err, boom) {
		t.Errorf("ResultReached = %v, want the EnqueueTx error", err)
	}
}

func TestEnqueue_ArgsSnapshotTheResult(t *testing.T) {
	long := strings.Repeat("é", 600)
	cases := []struct {
		name, status, irn, reasons, kind string
		wantIRN, wantReason              string
	}{
		{"accepted", "accepted", wbIRN, "[]", "", wbIRN, ""},
		{"rejected two reasons", "rejected", "", `[{"code":"E1","message":"bad TIN"},{"code":"E2","message":"bad date","path":"x"}]`, "", "", "E1: bad TIN; E2: bad date"},
		{"rejected no reasons", "rejected", "", "[]", "", "", "no reason given"},
		{"rejected long reason", "rejected", "", `[{"code":"E1","message":"` + long + `"}]`, "", "", string([]rune("E1: " + long)[:500])},
		{"failed payload_not_built", "failed", "", "[]", "payload_not_built", "", "the invoice could not be built for NRS"},
		{"failed never_acknowledged", "failed", "", "[]", "never_acknowledged", "", "NRS did not acknowledge the submission"},
		{"failed acknowledged_no_verdict", "failed", "", "[]", "acknowledged_no_verdict", "", "NRS acknowledged the submission but gave no verdict"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newWB(t, fakeCaps)
			e.drafted()
			e.setResult(c.status, c.irn, c.reasons, c.kind)
			q := &capQueue{}
			if err := reach(t, e.app, e.tenant, e.invoiceID, q); err != nil {
				t.Fatal(err)
			}
			if len(q.calls) != 1 {
				t.Fatalf("calls = %d, want 1", len(q.calls))
			}
			want := WriteBackArgs{TenantID: e.tenant, InvoiceID: e.invoiceID, InvoiceNumber: "WB-1", ResultVersion: 2, Outcome: c.status, IRN: c.wantIRN, Reason: c.wantReason}
			if got := q.calls[0].args; got != want {
				t.Errorf("args = %+v, want %+v", got, want)
			}
		})
	}
}

func TestEnqueue_ResultVersionCountsTerminalResults(t *testing.T) {
	e := newWB(t, fakeCaps)
	e.invoiceID = e.invoice("WB-V", "rejected", "")
	e.doc(e.conn, docOne, "drafted", e.invoiceID, false)
	q := realQueue(t, e.app)

	if err := reach(t, e.app, e.tenant, e.invoiceID, q); err != nil {
		t.Fatal(err)
	}
	e.history(e.invoiceID, "draft")
	e.history(e.invoiceID, "validated")
	e.history(e.invoiceID, "queued")
	e.setResult("accepted", wbIRN, "[]", "")
	for range 2 {
		if err := reach(t, e.app, e.tenant, e.invoiceID, q); err != nil {
			t.Fatal(err)
		}
	}
	jobs, keys := wbJobs(t, e.super, e.tenant)
	if len(jobs) != 2 || jobs[0].ResultVersion < 1 || jobs[1].ResultVersion != jobs[0].ResultVersion+1 {
		t.Fatalf("jobs = %+v, want versions 1 then 2", jobs)
	}
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Errorf("keys = %v, want two distinct keys", keys)
	}
}

func TestEnqueue_LinkedDuplicateGetsTheWriteBack(t *testing.T) {
	e := newIngest(t)
	inv := e.plantInvoice(plantInv{number: "FK1-0002"})
	e.mustWork("fake-co-1-inv-2", "1")
	e.wantState("fake-co-1-inv-2", "linked")
	e.accept(inv)
	mustExec(t, e.super, "INSERT INTO invoice_status_history (tenant_id, invoice_id, to_status, actor) VALUES ($1, $2, 'accepted', 'system')", e.tenant, inv)

	if err := reach(t, e.app, e.tenant, inv, realQueue(t, e.app)); err != nil {
		t.Fatal(err)
	}
	jobs, _ := wbJobs(t, e.super, e.tenant)
	if len(jobs) != 1 || jobs[0].InvoiceID != inv || jobs[0].Outcome != "accepted" {
		t.Errorf("jobs = %+v, want one accepted write-back for %s", jobs, inv)
	}
}
