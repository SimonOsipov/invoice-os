// audit_number_adversarial_test.go: AUDIT-11-04 QA. The edges the Stage 2.5 set does not
// reach -- a hostile number, colliding numbers across tenants, and a renumber landing
// between detection and the audit write.
//
// Every case drives Scan or SweepOnce (CF-19): a hand-built Finding would go green on a
// widened driftPayload while scanQuery stayed unwidened. TestRLS_ prefix is load-bearing --
// `-run TestRLS` (Makefile, ci.yml) filters everything else out and reports it as a pass
// (CF-18).
package reconciliation

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// rcHostileNumber carries every metacharacter the number crosses on its way into an
// immutable jsonb row and back out of a reader's LIKE: JSON quote/backslash/control, SQL
// quote/comment, and LIKE's own % and _.
const rcHostileNumber = "RC-\"A'B\\C%D_E{}\t<&>/;--Z"

// rcRenumber overwrites one invoice's number out of band.
func rcRenumber(t *testing.T, h *harness, invoiceID, number string) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(),
		`UPDATE invoices SET invoice_number = $1 WHERE id = $2`, number, invoiceID); err != nil {
		t.Fatalf("renumber invoice %s: %v", invoiceID, err)
	}
}

// TestRLS_AuditNumber_HostileNumberIsStoredVerbatim: the payload holds the number
// byte-for-byte, as a jsonb string. Catches a payload built by string concatenation, a
// value pre-escaped at write time (which AUDIT-11-05's escapeLike would then double-escape),
// and any truncation at the first quote.
func TestRLS_AuditNumber_HostileNumberIsStoredVerbatim(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "queued"})
	defer rcCompose(h, tenantID, cleanupInvoice)()

	rcRenumber(t, h, invoiceID, rcHostileNumber)
	if got := rcInvoiceNumberFor(t, h, invoiceID); got != rcHostileNumber {
		t.Fatalf("invoices.invoice_number = %q, want %q -- the fixture never stored the hostile "+
			"number, so every assertion below would be vacuous", got, rcHostileNumber)
	}

	// Through Scan, so the value is proven to survive the query, not only driftPayload.
	found, err := rcScan(t, h, tenantID, rcThresholds)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(found) != 1 || found[0].InvoiceID != invoiceID {
		t.Fatalf("Scan findings = %+v, want exactly one queued_never_sent for %s", found, invoiceID)
	}
	if found[0].InvoiceNumber != rcHostileNumber {
		t.Errorf("Finding.InvoiceNumber = %q, want %q -- scanQuery, not driftPayload, is where the "+
			"value comes from", found[0].InvoiceNumber, rcHostileNumber)
	}

	if err := rcReconciler(h).SweepOnce(ctx); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}

	row := rcReadAuditRow(t, h, tenantID, eventDriftDetected)
	rcAssertOneAuditRow(t, row, eventDriftDetected, "hostile number")
	rcAssertNumber(t, row, eventDriftDetected, "hostile number", rcHostileNumber)

	// ->> already unescaped once. Decoding the raw payload in Go is a second, independent
	// path to the same bytes -- a payload that is valid jsonb but wrong Go-side fails here.
	if got, _ := rcDecodePayload(t, row.payload)[auditNumberKey].(string); got != rcHostileNumber {
		t.Errorf("decoded payload[%q] = %q, want %q", auditNumberKey, got, rcHostileNumber)
	}

	var typ string
	if err := h.super.QueryRow(ctx,
		`SELECT jsonb_typeof(payload->'`+auditNumberKey+`') FROM audit_log
		  WHERE tenant_id = $1 AND event = $2`, tenantID, eventDriftDetected).Scan(&typ); err != nil {
		t.Fatalf("jsonb_typeof: %v", err)
	}
	if typ != "string" {
		t.Errorf("jsonb_typeof(payload->'%s') = %q, want \"string\"", auditNumberKey, typ)
	}
}

// TestRLS_AuditNumber_CollidingNumbersStayWithTheirOwnTenant: invoices_tenant_entity_number_uq
// is per (tenant, entity), so two tenants may hold the SAME number. One sweep covers both.
// The number cannot discriminate the rows, so invoice_id/tenant_id must -- an arm that
// cross-joined tenants (the failure the two approval arms' explicit tenant_id predicates
// exist to prevent) shows up here and nowhere else.
func TestRLS_AuditNumber_CollidingNumbersStayWithTheirOwnTenant(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	const shared = "RC-COLLIDE-0001"

	tenantA, _, invoiceA, cleanupA := rcSeedInvoice(t, h, rcInvoiceOpts{status: "queued"})
	defer rcCompose(h, tenantA, cleanupA)()
	tenantB, _, invoiceB, cleanupB := rcSeedInvoice(t, h, rcInvoiceOpts{status: "queued"})
	defer rcCompose(h, tenantB, cleanupB)()

	rcRenumber(t, h, invoiceA, shared)
	rcRenumber(t, h, invoiceB, shared)
	if invoiceA == invoiceB || tenantA == tenantB {
		t.Fatalf("fixture invoices/tenants are not distinct (%s/%s, %s/%s)", invoiceA, invoiceB, tenantA, tenantB)
	}
	if rcInvoiceNumberFor(t, h, invoiceA) != rcInvoiceNumberFor(t, h, invoiceB) {
		t.Fatalf("the two fixture numbers are not equal, so this case is not testing a collision")
	}

	if err := rcReconciler(h).SweepOnce(ctx); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}

	for _, c := range []struct {
		name    string
		tenant  string
		invoice string
		other   string
	}{
		{"tenantA", tenantA, invoiceA, invoiceB},
		{"tenantB", tenantB, invoiceB, invoiceA},
	} {
		t.Run(c.name, func(t *testing.T) {
			row := rcReadAuditRow(t, h, c.tenant, eventDriftDetected)
			rcAssertOneAuditRow(t, row, eventDriftDetected, c.name)
			rcAssertNumber(t, row, eventDriftDetected, c.name, shared)

			if row.invoiceID == nil || *row.invoiceID != c.invoice {
				t.Errorf("%s: audit_log.invoice_id = %v, want %q -- the shared number cannot tell "+
					"the two rows apart, so this is the only discriminator", c.name, row.invoiceID, c.invoice)
			}
			wantEntity := rcInvoiceEntityFor(t, h, c.invoice)
			if row.entityID == nil || *row.entityID != wantEntity {
				t.Errorf("%s: audit_log.entity_id = %v, want %q", c.name, row.entityID, wantEntity)
			}
			if n := mustCount(t, h.super,
				`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND payload->>'invoice_id' = $2`,
				c.tenant, c.other); n != 0 {
				t.Errorf("%s holds %d audit row(s) naming the OTHER tenant's invoice %s", c.name, n, c.other)
			}
		})
	}
}

// TestRLS_AuditNumber_PayloadHoldsTheNumberScanRead: a renumber commits between detection
// and the audit write. The payload must hold the number Scan read alongside the invoice_id
// it wrote -- one row read, not two. A per-finding lookup (the shape AC-2's statement count
// forbids) would re-read under READ COMMITTED and freeze the post-rename value against a
// drift that was detected on the old one.
func TestRLS_AuditNumber_PayloadHoldsTheNumberScanRead(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, _, invoiceID, cleanupInvoice := rcSeedInvoice(t, h, rcInvoiceOpts{status: "queued"})
	defer rcCompose(h, tenantID, cleanupInvoice)()

	atScan := rcInvoiceNumberFor(t, h, invoiceID)
	renamed := "RC-RENAMED-" + invoiceID[:8]
	if atScan == renamed {
		t.Fatalf("the fixture number already equals the rename target %q", renamed)
	}

	if err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		found, err := Scan(ctx, tx, rcThresholds)
		if err != nil {
			return err
		}
		if len(found) != 1 || found[0].InvoiceID != invoiceID {
			return fmt.Errorf("Scan findings = %+v, want exactly one for %s", found, invoiceID)
		}
		if found[0].InvoiceNumber != atScan {
			return fmt.Errorf("Finding.InvoiceNumber = %q, want %q", found[0].InvoiceNumber, atScan)
		}
		rcRenumber(t, h, invoiceID, renamed) // committed on another connection, mid-sweep
		return recordDriftAudit(ctx, tx, found[0])
	}); err != nil {
		t.Fatalf("interleaved sweep: %v", err)
	}

	if got := rcInvoiceNumberFor(t, h, invoiceID); got != renamed {
		t.Fatalf("invoices.invoice_number = %q, want %q -- the interleaved rename never landed, so "+
			"this case is not testing the race", got, renamed)
	}

	row := rcReadAuditRow(t, h, tenantID, eventDriftDetected)
	rcAssertOneAuditRow(t, row, eventDriftDetected, "renumbered mid-sweep")
	rcAssertNumber(t, row, eventDriftDetected, "renumbered mid-sweep", atScan)

	body := rcDecodePayload(t, row.payload)
	if got, _ := body["invoice_id"].(string); got != invoiceID {
		t.Errorf("payload invoice_id = %q, want %q -- the id and the number must come from one read",
			got, invoiceID)
	}
}
