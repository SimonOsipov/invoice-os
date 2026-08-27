// The blank-parameter guard for CancelLiveRunTx.
//
// approval.CancelLiveRunTx takes the invoice number as a PARAMETER -- the carrier
// form D-11 rejected for PollArgs. A caller that passes "" compiles silently and
// freezes a blank into an immutable audit row, and a blank is indistinguishable from
// "this invoice has no number". The other three approval writers are RETURNING-fed and
// cannot produce one.
//
// This file lives in internal/invoice because two of the three production callers are
// outside `make test-approvals`. Fixtures reuse apply_validation_arming_test.go's
// ported approval seeders and audit_number_test.go's mustInvoiceNumber.
//
// Run: `DEV_DB_PORT=5442 make test-invoice` (go test -p 1).
package invoice

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// cancelCallerCount floors cancelCallers: approval.CancelLiveRunTx has exactly three
// production callers -- store.go:1402 (Store.Edit's demotion), store.go:1739
// (Store.Transition -> draft) and revalidate.go:77 (DemoteRevalidatedTx). A short table
// passes every assertion inside the loop vacuously.
const cancelCallerCount = 3

// cancelCaller is one production call site of approval.CancelLiveRunTx.
type cancelCaller struct {
	label string
	site  string // file:line at c52b0fa -- re-locate by the CancelLiveRunTx call, not the line
	// drive seeds a fresh tenant holding a validated invoice with one open approval
	// run, walks it back to draft through the real Store method, and returns
	// (tenantID, invoiceID).
	drive func(t *testing.T, super, app *pgxpool.Pool) (string, string)
}

// seedInvoiceWithOpenRun is a validated invoice carrying one open approval run --
// the state every one of the three callers demotes out of.
func seedInvoiceWithOpenRun(t *testing.T, super *pgxpool.Pool, label, number string, status Status) (tenantID, invoiceID string) {
	t.Helper()
	tenantID = seedTenant(t, super, "AN-CANCEL "+label+" tenant")
	entityID := seedEntity(t, super, tenantID, "AN-CANCEL "+label+" entity")
	invoiceID = seedInvoiceAtStatus(t, super, tenantID, entityID, number, status)
	policyID := seedApprovalPolicyFor(t, super, tenantID, "AN-CANCEL "+label+" policy")
	versionID := seedApprovalPolicyVersionFor(t, super, tenantID, policyID)
	seedApprovalRunFor(t, super, tenantID, invoiceID, versionID) // defaults to open
	return tenantID, invoiceID
}

func cancelCallers() []cancelCaller {
	return []cancelCaller{
		{
			label: "Store.Edit demotion", site: "internal/invoice/store.go:1402",
			drive: func(t *testing.T, super, app *pgxpool.Pool) (string, string) {
				tenantID, invoiceID := seedInvoiceWithOpenRun(t, super, "edit", "AN-CANCEL-EDIT-1", StatusValidated)
				c := auth.WithIdentity(context.Background(), auth.Identity{
					Subject: memberSubject, Role: "authenticated", TenantID: tenantID,
				})
				buyer := "AN cancel-caller buyer"
				got, err := NewStore(app).Edit(c, invoiceID, EditInput{UpdateInput: UpdateInput{BuyerName: &buyer}})
				if err != nil {
					t.Fatalf("Edit(validated -> demote to draft): %v", err)
				}
				if got.Status != StatusDraft {
					t.Fatalf("Edit returned status %q, want %q -- no demotion means CancelLiveRunTx never ran", got.Status, StatusDraft)
				}
				return tenantID, invoiceID
			},
		},
		{
			label: "Store.Transition to draft", site: "internal/invoice/store.go:1739",
			drive: func(t *testing.T, super, app *pgxpool.Pool) (string, string) {
				tenantID, invoiceID := seedInvoiceWithOpenRun(t, super, "transition", "AN-CANCEL-TRANSITION-1", StatusValidated)
				c := auth.WithIdentity(context.Background(), auth.Identity{
					Subject: memberSubject, Role: "authenticated", TenantID: tenantID,
				})
				got, err := NewStore(app).Transition(c, invoiceID, StatusDraft)
				if err != nil {
					t.Fatalf("Transition(validated -> draft): %v", err)
				}
				if got.Status != StatusDraft {
					t.Fatalf("Transition returned status %q, want %q", got.Status, StatusDraft)
				}
				return tenantID, invoiceID
			},
		},
		{
			label: "DemoteRevalidatedTx", site: "internal/invoice/revalidate.go:77",
			drive: func(t *testing.T, super, app *pgxpool.Pool) (string, string) {
				tenantID, invoiceID := seedInvoiceWithOpenRun(t, super, "revalidate", "AN-CANCEL-REVALIDATE-1", StatusValidated)
				ctx := context.Background()
				versionID := seedRuleSetVersionID(t, super)
				vs := []Violation{{RuleKey: "vat-standard-rate", Severity: "error", Message: "bad rate"}}
				if err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
					_, err := NewStore(app).DemoteRevalidatedTx(ctx, tx, invoiceID, tenantID, vs, versionID)
					return err
				}); err != nil {
					t.Fatalf("DemoteRevalidatedTx: %v", err)
				}
				return tenantID, invoiceID
			},
		},
	}
}

// cancelledNumberFor returns payload->>'invoice_number' for the invoice's
// invoice.approval_cancelled row, plus how many such rows it has and the raw payload.
// ->> yields NULL for an absent key and "" for a present empty string, so the two are
// distinguishable -- and CF-10's defect is exactly the second.
func cancelledNumberFor(t *testing.T, super *pgxpool.Pool, invoiceID string) (rows int, number *string, payload string) {
	t.Helper()
	ctx := context.Background()
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE event = 'invoice.approval_cancelled' AND payload->>'id' = $1`,
		invoiceID).Scan(&rows); err != nil {
		t.Fatalf("count invoice.approval_cancelled rows for %s: %v", invoiceID, err)
	}
	if rows == 0 {
		return rows, nil, ""
	}
	if err := super.QueryRow(ctx,
		`SELECT payload->>'`+auditNumberKey+`', payload::text
		   FROM audit_log WHERE event = 'invoice.approval_cancelled' AND payload->>'id' = $1
		  ORDER BY created_at DESC, id DESC LIMIT 1`,
		invoiceID).Scan(&number, &payload); err != nil {
		t.Fatalf("read the invoice.approval_cancelled row for %s: %v", invoiceID, err)
	}
	return rows, number, payload
}

// TestAuditNumber_EveryCancelCallerPassesTheNumber (AC-3, CF-10): all three production
// callers of approval.CancelLiveRunTx hand it the invoice's REAL number, never "".
// The number is read back out of invoices.invoice_number, so nothing here compares the
// payload against a literal the test itself wrote.
//
// This is the one spec that catches a partial fix: the number arrives by parameter, so
// widening the callee and updating only the caller in front of you leaves the other two
// writing a blank into an append-only table, where it can never be corrected.
func TestAuditNumber_EveryCancelCallerPassesTheNumber(t *testing.T) {
	super, app := dbTestPools(t)

	callers := cancelCallers()
	if len(callers) != cancelCallerCount {
		t.Fatalf("cancelCallers holds %d rows, want %d -- a short table passes every assertion below vacuously", len(callers), cancelCallerCount)
	}

	for _, c := range callers {
		t.Run(c.label, func(t *testing.T) {
			_, invoiceID := c.drive(t, super, app)

			want := mustInvoiceNumber(t, super, invoiceID)
			if want == "" {
				t.Fatalf("fixture invoice %s carries a blank invoice_number; the comparison below could not tell a blank the caller passed from the column's own", invoiceID)
			}

			rows, got, payload := cancelledNumberFor(t, super, invoiceID)
			if rows != 1 {
				t.Fatalf("%s (%s): invoice.approval_cancelled rows for the invoice = %d, want exactly 1 -- the fixture did not reach CancelLiveRunTx", c.label, c.site, rows)
			}
			if got == nil {
				t.Fatalf("%s (%s): payload->>'%s' is SQL NULL (key absent); payload = %s, want %q", c.label, c.site, auditNumberKey, payload, want)
			}
			if *got == "" {
				t.Fatalf("%s (%s): payload->>'%s' is the empty string -- this caller passed \"\" and froze a blank into an append-only audit row (CF-10); payload = %s, want %q", c.label, c.site, auditNumberKey, payload, want)
			}
			if *got != want {
				t.Errorf("%s (%s): payload->>'%s' = %q, want %q (invoices.invoice_number)", c.label, c.site, auditNumberKey, *got, want)
			}
		})
	}
}
