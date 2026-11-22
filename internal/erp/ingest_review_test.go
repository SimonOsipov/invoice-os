package erp

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// A claim older than claimLife is still the same job's when its revision and number match: River's
// backoff outlasts the claim, so the sixth attempt of a gate outage meets a stale claim.
func TestIngest_StaleOwnClaimOfTheSameRevisionIsAdopted(t *testing.T) {
	e := newIngest(t)
	inv := e.plantInvoice(plantInv{number: numOne, conn: &e.conn})
	e.plantDocument(plantDoc{docID: docOne, state: "pending", claimed: sp(numOne), updatedAgo: 20 * time.Minute})
	e.mustWork(docOne, "1")

	d := e.wantState(docOne, "drafted")
	if d.InvoiceID == nil || *d.InvoiceID != inv || d.Claimed != nil {
		t.Errorf("row invoice_id/claim = %v/%v, want %s/NULL", d.InvoiceID, d.Claimed, inv)
	}
	if got := e.val.called(); len(got) != 1 || got[0] != inv {
		t.Errorf("validator saw %v, want the adopted draft [%s]", got, inv)
	}
	if e.ruleSetOf(inv) == nil {
		t.Error("the adopted draft was never validated")
	}
	e.wantAudit("erp.document.pulled", pulledKeys)
	e.wantNoAudit("erp.document.skipped_duplicate")
	if n := e.invoices(); n != 1 {
		t.Errorf("invoices = %d, want 1", n)
	}
}

// An older revision's job never replaces what a newer revision's job holds or left.
func TestIngest_OlderRevisionJobYieldsToANewerRow(t *testing.T) {
	t.Run("not found snoozes on another revision's live claim", func(t *testing.T) {
		e := newIngest(t)
		e.plantDocument(plantDoc{docID: docOne, rev: "2", state: "pending", claimed: sp(numOne)})
		e.onFetch = func(context.Context, DocRef) (Document, error) {
			return Document{}, fmt.Errorf("vendor: %w", ErrNotFound)
		}
		before := e.docJSON(docOne)
		if _, snoozed := snoozeOf(e.work(docOne, "1")); !snoozed {
			t.Fatal("want a River snooze while revision 2 holds the claim")
		}
		if after := e.docJSON(docOne); after != before {
			t.Errorf("the row changed:\nbefore %s\nafter  %s", before, after)
		}
	})
	t.Run("not found leaves a newer revision's invoice row", func(t *testing.T) {
		e := newIngest(t)
		inv := e.plantInvoice(plantInv{number: numOne, conn: &e.conn})
		e.plantDocument(plantDoc{docID: docOne, rev: "2", state: "drafted", invoiceID: &inv})
		e.onFetch = func(context.Context, DocRef) (Document, error) {
			return Document{}, fmt.Errorf("vendor: %w", ErrNotFound)
		}
		before := e.docJSON(docOne)
		if err := e.work(docOne, "1"); err != nil {
			t.Fatal(err)
		}
		if after := e.docJSON(docOne); after != before {
			t.Errorf("the row changed:\nbefore %s\nafter  %s", before, after)
		}
	})
	t.Run("an exhausted job leaves a deleted row", func(t *testing.T) {
		e := newIngest(t)
		e.plantDocument(plantDoc{docID: docOne, rev: "2", state: "deleted", deleted: tp(time.Now())})
		e.onFetch = func(context.Context, DocRef) (Document, error) { return Document{}, errVendor }
		before := e.docJSON(docOne)
		if err := e.workAs(context.Background(), e.worker, e.args(docOne, "1", false), 8); !errors.Is(err, errVendor) {
			t.Fatalf("Work err = %v, want the vendor error", err)
		}
		if after := e.docJSON(docOne); after != before {
			t.Errorf("the row changed:\nbefore %s\nafter  %s", before, after)
		}
	})
}

func TestIngest_UnvalidatedDraftIsRetriedNotSettled(t *testing.T) {
	e := newIngest(t)
	e.val.skipFirst = true

	if err := e.work(docOne, "1"); err == nil {
		t.Fatal("Work = nil, want an error so the retry validates the draft")
	}
	id, status, _ := e.invoiceByNumber(e.entity, numOne)
	if status != "draft" || e.ruleSetOf(id) != nil {
		t.Fatalf("invoice status/rule set = %q/%v, want an unvalidated draft", status, e.ruleSetOf(id))
	}
	e.wantState(docOne, "pending")
	e.wantNoAudit("erp.document.pulled")

	e.mustWork(docOne, "1")
	if e.ruleSetOf(id) == nil {
		t.Error("the retry left the draft unvalidated")
	}
	e.wantState(docOne, "drafted")
	e.wantAudit("erp.document.pulled", pulledKeys)
	if n := e.invoices(); n != 1 {
		t.Errorf("invoices = %d, want 1", n)
	}
}

func TestIngest_RateLimitSnoozesWithoutSpendingAnAttempt(t *testing.T) {
	limited := &RateLimitError{RetryAfter: 90 * time.Second}
	t.Run("fetch", func(t *testing.T) {
		e := newIngest(t)
		e.onFetch = func(context.Context, DocRef) (Document, error) { return Document{}, fmt.Errorf("vendor: %w", limited) }
		err := e.workAs(context.Background(), e.worker, e.args(docOne, "1", false), 8)
		if d, snoozed := snoozeOf(err); !snoozed || d != 90*time.Second {
			t.Fatalf("Work err = %v, want a 90s snooze", err)
		}
		e.noDoc(docOne)
	})
	t.Run("party", func(t *testing.T) {
		e := newIngest(t)
		reg, err := NewRegistry(scriptConnector{
			Connector: testFake(t),
			fetchParty: func(context.Context, Credentials, string, string) (Party, error) {
				return Party{}, fmt.Errorf("vendor: %w", limited)
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		e.worker.Registry = reg
		err = e.workAs(context.Background(), e.worker, e.args(docOne, "1", false), 8)
		if d, snoozed := snoozeOf(err); !snoozed || d != 90*time.Second {
			t.Fatalf("Work err = %v, want a 90s snooze", err)
		}
		e.noDoc(docOne)
	})
}

// A new revision whose ingest is exhausted must not stay spent: the epoch bump lists it again.
func TestIngest_ExhaustedNewRevisionOfATakenDocumentReopensTheListing(t *testing.T) {
	e := newIngest(t)
	inv := acceptedDraft(e)
	mustExec(t, e.super, "UPDATE erp_connections SET cursor = '\\x0a0b' WHERE id = $1", e.conn)
	e.onFetch = func(context.Context, DocRef) (Document, error) { return Document{}, errVendor }

	if err := e.workAs(context.Background(), e.worker, e.args(docOne, "2", false), 8); !errors.Is(err, errVendor) {
		t.Fatalf("Work err = %v, want the vendor error", err)
	}
	var cursor []byte
	var epoch int64
	mustScan(t, e.super, "SELECT cursor, ingest_epoch FROM erp_connections WHERE id = $1", []any{e.conn}, &cursor, &epoch)
	if cursor != nil || epoch != 1 {
		t.Errorf("cursor/ingest_epoch = %v/%d, want NULL/1", cursor, epoch)
	}
	if d := e.wantState(docOne, "drafted"); d.InvoiceID == nil || *d.InvoiceID != inv {
		t.Errorf("row invoice_id = %v, want %s kept", d.InvoiceID, inv)
	}
}
