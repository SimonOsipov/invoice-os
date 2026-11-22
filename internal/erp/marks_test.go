package erp

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func TestInvoiceMarks_ReturnsTheLinkedDocumentsMarks(t *testing.T) {
	e := newIngest(t)
	c2 := e.addConnection(e.entity, "fake-co-2")
	inv := e.plantInvoice(plantInv{number: numOne, status: "accepted"})
	other := e.plantInvoice(plantInv{number: "FK1-0002"})
	lone := e.plantInvoice(plantInv{number: "FK1-0003"})
	changed := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)

	e.plantDocument(plantDoc{conn: e.conn, docID: "d1", state: "linked", invoiceID: &inv, changed: &changed})
	e.plantDocument(plantDoc{conn: c2, docID: "d2", state: "drafted", invoiceID: &inv, differs: true})
	e.plantDocument(plantDoc{conn: e.conn, docID: "d3", state: "linked", invoiceID: &other, differs: true})

	ctx := context.Background()
	var got, none []DocumentMark
	err := db.WithinTenantTx(ctx, e.app, e.tenant, func(tx pgx.Tx) error {
		var err error
		if got, err = InvoiceMarks(ctx, tx, inv); err != nil {
			return err
		}
		none, err = InvoiceMarks(ctx, tx, lone)
		return err
	})
	if err != nil {
		t.Fatalf("InvoiceMarks: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("marks = %+v, want the two documents linked to the invoice (d3 belongs to another)", got)
	}
	first, second := got[0], got[1]
	if first.ConnectionID != e.conn || first.SourceDocID != "d1" || first.System != fakeSystem || first.State != "linked" ||
		first.DiffersFromERP || first.ChangedAfterClearanceAt == nil || !first.ChangedAfterClearanceAt.Equal(changed) || first.DeletedInERPAt != nil {
		t.Errorf("first mark = %+v, want d1 on %s: linked, changed at %s, not differing", first, e.conn, changed)
	}
	if second.ConnectionID != c2 || second.SourceDocID != "d2" || second.State != "drafted" ||
		!second.DiffersFromERP || second.ChangedAfterClearanceAt != nil || second.DeletedInERPAt != nil {
		t.Errorf("second mark = %+v, want d2 on %s: drafted, differing, not changed", second, c2)
	}

	if none == nil {
		t.Error("an invoice with no ERP document got a nil slice, want empty")
	}
	if len(none) != 0 {
		t.Errorf("marks of an unlinked invoice = %+v, want none", none)
	}
}

func TestInvoiceMarks_ClientScopeHidesAnotherClientsDocuments(t *testing.T) {
	e := newIngest(t)
	y := e.addClient("client Y")
	inv := e.plantInvoice(plantInv{number: numOne, status: "accepted"})
	e.plantDocument(plantDoc{docID: "d1", state: "linked", invoiceID: &inv, changed: tp(time.Now())})

	marksFor := func(ctx context.Context) []DocumentMark {
		t.Helper()
		var marks []DocumentMark
		err := db.WithinRequestTenantTx(ctx, e.app, func(tx pgx.Tx) error {
			var err error
			marks, err = InvoiceMarks(ctx, tx, inv)
			return err
		})
		if err != nil {
			t.Fatalf("InvoiceMarks in the request seam: %v", err)
		}
		return marks
	}

	// The control: a member who sees the client reads the mark, so the empty answer below is the scope.
	all := seedMember(t, e.super, e.tenant, "preparer", "active", true)
	if got := marksFor(all); len(got) != 1 {
		t.Fatalf("an all-clients member reads %d mark(s), want 1", len(got))
	}
	scopedToX := seedMember(t, e.super, e.tenant, "preparer", "active", false, e.entity)
	if got := marksFor(scopedToX); len(got) != 1 {
		t.Fatalf("a member scoped to the invoice's client reads %d mark(s), want 1", len(got))
	}
	scopedToY := seedMember(t, e.super, e.tenant, "preparer", "active", false, y)
	if got := marksFor(scopedToY); len(got) != 0 {
		t.Errorf("a member scoped to another client reads %+v, want none", got)
	}
}
