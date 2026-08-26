// audit_number_search_test.go: AUDIT-11-05, the story's real acceptance. A row written by
// a REAL writer, read back through the REAL reader (audit.NewStore().List, which adds the
// identity -> tenant-GUC seam), searched by the invoice number a human would type.
//
// Only this file and audit_number_search_adversarial_test.go cross that seam: AUDIT-11-09's
// cases all raw-INSERT their audit rows, and no other writer-package case calls the reader.
//
// CF-30: after 09 the reader resolves the typed number through the live invoices table and
// never reads the payload key, so "the row comes back" is satisfiable with subtasks 01-04
// reverted. The assertion that the RETURNED payload carries invoice_number is the one that
// makes those writers load-bearing, and it is the point of this file.
//
// Event-type floor is TWO, one per payload spelling: rule A (payload key "id") here, rule B
// (payload key "invoice_id") in internal/submission/audit_number_search_test.go. AUDIT-11-09
// already proves all 17 invoice-scoped events reach the resolved arm, so driving every one of
// them through a real writer would add nothing.
//
// Run: `DEV_DB_PORT=5442 make test-invoice` (go test -p 1). A bare run with only DEV_DB_PORT
// set skips every case here and still prints ok (CF-6).
package invoice

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// auditSearchLimit is well above any fixture row count here, so HasMore stays false and the
// control assertion sees the whole result set.
const auditSearchLimit = 50

// auditSearchControlNumber belongs to a second invoice in the SAME tenant. Without it a
// reader that ignored the search and returned every row in the tenant would pass.
const auditSearchControlNumber = "ZZ-SEARCH-CONTROL"

// auditSearchIDKey is rule A's payload spelling, the half this file covers.
const auditSearchIDKey = "id"

// auditSearchList runs the production reader the way a request does: audit.NewStore(app).List
// wraps db.WithinRequestTenantTx, which sets app.current_tenant from the identity and runs the
// membership gate. The subject has no memberships row, which proceeds (D-17, NARROW).
func auditSearchList(t *testing.T, app *pgxpool.Pool, tenantID, q string) audit.Response {
	t.Helper()
	ctx := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID,
	})
	got, err := audit.NewStore(app).List(ctx, audit.Filter{Q: q, Limit: auditSearchLimit})
	if err != nil {
		t.Fatalf("audit.Store.List(q=%q): %v", q, err)
	}
	return got
}

func auditSearchPayload(t *testing.T, e audit.Event) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(e.Payload, &m); err != nil {
		t.Fatalf("unmarshal the returned payload for %s: %v (%s)", e.Event, err, e.Payload)
	}
	return m
}

// auditSearchFind returns the returned event named event whose payload id is invoiceID, and
// fails when the response holds none -- every assertion after that would read another row.
//
// Both not-found paths carry auditSearchWriterState: without it a writer that dropped the
// addressing key and a reader that lost the resolved arm red at the same line with the
// same text.
func auditSearchFind(t *testing.T, app *pgxpool.Pool, tenantID string, got audit.Response, q, event, invoiceID string) audit.Event {
	t.Helper()
	if len(got.Events) == 0 {
		t.Fatalf("q=%q returned no events at all (total=%d, log_is_empty=%v); a real writer wrote a row for invoice %s and the real reader must find it%s",
			q, got.Total, got.LogIsEmpty, invoiceID, auditSearchWriterState(t, app, tenantID, event, invoiceID))
	}
	if got.Page.HasMore {
		t.Fatalf("q=%q filled the %d-row page; the control assertion below would only see a prefix", q, auditSearchLimit)
	}
	for _, e := range got.Events {
		if e.Event != event {
			continue
		}
		if p := auditSearchPayload(t, e); p[auditSearchIDKey] == invoiceID {
			return e
		}
	}
	t.Fatalf("q=%q returned %d events (total=%d) but none is a %s for invoice %s:%s%s",
		q, len(got.Events), got.Total, event, invoiceID, auditSearchDump(got),
		auditSearchWriterState(t, app, tenantID, event, invoiceID))
	return audit.Event{}
}

// auditSearchWriterState names which half broke. The resolved arm compares
// audit_log.invoice_id, so a row that names the invoice in its payload but does not carry
// it in that column is a writer defect, and a row that carries it is a reader defect.
func auditSearchWriterState(t *testing.T, app *pgxpool.Pool, tenantID, event, invoiceID string) string {
	t.Helper()
	ctx := context.Background()
	var forEvent, scoped, named int
	if err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*),
			       count(*) FILTER (WHERE invoice_id::text = $2),
			       count(*) FILTER (WHERE payload::text LIKE '%' || $2 || '%')
			  FROM audit_log WHERE event = $1`, event, invoiceID).Scan(&forEvent, &scoped, &named)
	}); err != nil {
		return fmt.Sprintf("\n\tdiagnosis unavailable: %v", err)
	}
	switch {
	case forEvent == 0:
		return fmt.Sprintf("\n\tdiagnosis: audit_log holds no %s row in this tenant at all -- the WRITER did not run", event)
	case scoped > 0:
		return fmt.Sprintf("\n\tdiagnosis: %d %s row(s) carry invoice_id = %s, so the WRITER is fine and the READER did not return them", scoped, event, invoiceID)
	case named > 0:
		return fmt.Sprintf("\n\tdiagnosis: %d %s row(s) name invoice %s in the payload but none carries it in audit_log.invoice_id -- the WRITER dropped the addressing key, so no resolved arm can match", named, event, invoiceID)
	default:
		return fmt.Sprintf("\n\tdiagnosis: %d %s row(s) exist in this tenant but none names invoice %s at all -- the WRITER dropped the addressing key or wrote for another invoice", forEvent, event, invoiceID)
	}
}

func auditSearchDump(got audit.Response) string {
	var b strings.Builder
	for _, e := range got.Events {
		b.WriteString("\n\t" + e.Event + " " + string(e.Payload))
	}
	return b.String()
}

// auditSearchAssertScoped fails when a row belonging to the other invoice in this tenant came
// back: the search must narrow to the resolved invoice, never widen to the tenant.
func auditSearchAssertScoped(t *testing.T, got audit.Response, q, otherID, otherNumber string) {
	t.Helper()
	if strings.Contains(otherNumber, q) {
		t.Fatalf("the needle %q occurs in the other invoice number %q, so that invoice could match legitimately", q, otherNumber)
	}
	for _, e := range got.Events {
		if strings.Contains(string(e.Payload), otherID) {
			t.Errorf("q=%q returned a %s row naming invoice %s, whose number is %q -- the search is not narrowing to the resolved invoice; payload = %s",
				q, e.Event, otherID, otherNumber, e.Payload)
		}
	}
}

// auditSearchAssertNumber is the CF-30 assertion: the RETURNED payload carries the key. It is
// the only assertion in this story that goes red with a writer reverted, and it is what makes
// subtasks 01-04 load-bearing.
func auditSearchAssertNumber(t *testing.T, e audit.Event, site, want string) {
	t.Helper()
	p := auditSearchPayload(t, e)
	v, ok := p[auditNumberKey]
	if !ok {
		t.Fatalf("%s (%s): the returned payload has no %q key, so the row the reader hands back cannot say which invoice it is about; payload = %s, want %q",
			e.Event, site, auditNumberKey, e.Payload, want)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("%s (%s): the returned payload %q is %T (%v), want the string %q", e.Event, site, auditNumberKey, v, v, want)
	}
	if s != want {
		t.Errorf("%s (%s): the returned payload %q = %q, want %q (invoices.invoice_number read back)", e.Event, site, auditNumberKey, s, want)
	}
	// Rule A's spelling, asserted so the two-spelling floor is mechanical rather than prose.
	if p[auditSearchIDKey] == nil {
		t.Errorf("%s (%s): the returned payload has no %q key; this file covers rule A alone and internal/submission covers rule B", e.Event, site, auditSearchIDKey)
	}
	if _, wrong := p["invoice_id"]; wrong {
		t.Errorf("%s (%s): the returned payload carries invoice_id, rule B's spelling; internal/submission owns that half; payload = %s", e.Event, site, e.Payload)
	}
}

// AC-1, AC-2 (Core AC 4): each of the ten real invoice writers leaves a row the real reader
// returns for the invoice number, and the returned row carries that number.
func TestAuditNumber_SearchFindsARealWritersRow(t *testing.T) {
	super, app := dbTestPools(t)

	sites := auditNumberSites()
	if len(sites) != auditNumberSiteCount {
		t.Fatalf("auditNumberSites holds %d rows, want %d -- a short table passes every assertion below vacuously", len(sites), auditNumberSiteCount)
	}

	for _, s := range sites {
		t.Run(s.label, func(t *testing.T) {
			tenantID := seedTenant(t, super, "AN search "+s.label+" tenant")
			entityID := seedEntity(t, super, tenantID, "AN search "+s.label+" entity")

			invID := s.drive(t, super, app, tenantID, entityID)
			want := mustInvoiceNumber(t, super, invID)
			if want == "" {
				t.Fatalf("fixture invoice %s carries a blank invoice_number; a blank needle applies no filter", invID)
			}
			if strings.Contains(invID, want) {
				t.Fatalf("the invoice number %q is a substring of the invoice id %s, so the generic payload arm could supply the match (09's uuid-prefix trap)", want, invID)
			}

			c, _ := auditNumberIdentity(tenantID)
			ctrl, err := NewStore(app).Create(c, CreateInput{EntityID: entityID, InvoiceNumber: auditSearchControlNumber})
			if err != nil {
				t.Fatalf("seed the control invoice: %v", err)
			}
			got := auditSearchList(t, app, tenantID, want)
			if got.Total < 1 {
				t.Fatalf("q=%q reported total %d, want at least 1%s", want, got.Total,
					auditSearchWriterState(t, app, tenantID, s.event, invID))
			}
			e := auditSearchFind(t, app, tenantID, got, want, s.event, invID)
			auditSearchAssertScoped(t, got, want, ctrl.ID, auditSearchControlNumber)
			auditSearchAssertNumber(t, e, s.site, want)
		})
	}

	// AC-2: a human types a fragment, not the whole number. The needle is chosen so the
	// resolved arm is the ONLY arm that can return the row.
	t.Run("fragment_of_the_number", func(t *testing.T) {
		const number = "AN-SEARCH-FRAGMENT-7Q4X2"
		const fragment = "GMENT-7Q4"
		const event = "invoice.created"

		tenantID := seedTenant(t, super, "AN search fragment tenant")
		entityID := seedEntity(t, super, tenantID, "AN search fragment entity")
		c, _ := auditNumberIdentity(tenantID)

		inv, err := NewStore(app).Create(c, CreateInput{EntityID: entityID, InvoiceNumber: number})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		want := mustInvoiceNumber(t, super, inv.ID)
		if !strings.Contains(want, fragment) || want == fragment {
			t.Fatalf("%q is not a proper fragment of the stored number %q", fragment, want)
		}
		if strings.Contains(inv.ID, fragment) {
			t.Fatalf("the fragment %q is a substring of the invoice id %s; the generic payload arm would supply the match", fragment, inv.ID)
		}
		if strings.Contains(strings.ToLower(event), strings.ToLower(fragment)) {
			t.Fatalf("the fragment %q occurs in the event name %q; the event arm would supply the match", fragment, event)
		}

		ctrl, err := NewStore(app).Create(c, CreateInput{EntityID: entityID, InvoiceNumber: auditSearchControlNumber})
		if err != nil {
			t.Fatalf("seed the control invoice: %v", err)
		}

		got := auditSearchList(t, app, tenantID, fragment)
		e := auditSearchFind(t, app, tenantID, got, fragment, event, inv.ID)
		auditSearchAssertScoped(t, got, fragment, ctrl.ID, auditSearchControlNumber)
		auditSearchAssertNumber(t, e, "store.go:269", want)

		// The control is not vacuous: searched by its OWN number it comes back, and this
		// invoice does not. Without this, a Create that wrote no audit row at all would
		// make every control assertion above pass by having nothing to leak.
		ctrlWant := mustInvoiceNumber(t, super, ctrl.ID)
		back := auditSearchList(t, app, tenantID, ctrlWant)
		ce := auditSearchFind(t, app, tenantID, back, ctrlWant, event, ctrl.ID)
		auditSearchAssertScoped(t, back, ctrlWant, inv.ID, want)
		auditSearchAssertNumber(t, ce, "store.go:269", ctrlWant)
	})
}
