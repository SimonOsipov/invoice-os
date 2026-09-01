// correction_route_test.go: where the correction route is REGISTERED, where its audit event is
// SPELLED, and what its invoice adapter does with each domain outcome. Nothing here serves a mux
// or opens a database; the claims are read off main.go's source and off the adapters directly.
//
// Helpers use an fc* prefix; dr ea wt ds up are taken.
package main

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

const (
	fcRoute       = "POST /v1/extractions/{id}/fields/{name}/corrections"
	fcEvent       = "extraction.field_corrected"
	fcAdapterFn   = "newFieldCorrectedAuditor"
	fcApplierFn   = "newInvoiceFieldApplier"
	fcDocumentID  = "3f1a2b3c-4d5e-4f60-8a71-9b2c3d4e5f60"
	fcInvoiceID   = "5d2f7a10-6b3c-4e8d-9f01-2a3b4c5d6e7f"
	fcCorrectedTo = "1500.00"
	fcActor       = "e5b10007-0000-4000-8000-000000000001"
)

// The route must be mounted on app.Mux -- a route on a locally built mux is registered and
// unreachable -- and dispatched to extraction.CorrectionHandler over BOTH adapters. Nothing in
// Go reads the mux pattern, so go build, go vet and the whole internal/extraction suite stay
// green on a wrong registration.
func TestSubmissionMain_WiresTheCorrectionRouteAndItsCollaborators(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/submission/main.go: %v", err)
	}

	var foundPing, pingOnAppMux, found bool
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" || len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		switch strings.Trim(lit.Value, `"`) {
		case "GET /v1/ping":
			foundPing = true
			pingOnAppMux = wtRender(sel.X) == "app.Mux"
		case fcRoute:
			found = true
			if got := wtRender(sel.X); got != "app.Mux" {
				t.Errorf("%s is registered on %s, want app.Mux -- only app.Mux is served", fcRoute, got)
			}
			handlerCall, ok := call.Args[1].(*ast.CallExpr)
			if !ok {
				t.Errorf("%s' second argument is %T, want a call expression", fcRoute, call.Args[1])
				return true
			}
			hsel, ok := handlerCall.Fun.(*ast.SelectorExpr)
			if !ok || hsel.Sel.Name != "CorrectionHandler" {
				t.Errorf("%s' handler call is not ....CorrectionHandler(...), got %s", fcRoute, wtRender(handlerCall.Fun))
				return true
			}
			if pkg, ok := hsel.X.(*ast.Ident); !ok || pkg.Name != "extraction" {
				t.Errorf("%s' handler is not extraction.CorrectionHandler(...), got %s", fcRoute, wtRender(handlerCall.Fun))
			}
			// Both adapters are unit-tested over injected seams below, so only this scan can say
			// the handler is built over the REAL pool, store and audit module. A nil collaborator
			// compiles and fails on the first correction.
			if len(handlerCall.Args) != 4 {
				t.Errorf("extraction.CorrectionHandler is called with %d argument(s), want 4 (pool, apply, record, logger)", len(handlerCall.Args))
				return true
			}
			if c, ok := handlerCall.Args[1].(*ast.CallExpr); !ok || wtCallName(c.Fun) != fcApplierFn {
				t.Errorf("CorrectionHandler's apply argument is %s, want a %s(...) call: only that closure runs the shared edit path on the handler's own transaction",
					wtRender(handlerCall.Args[1]), fcApplierFn)
			}
			if c, ok := handlerCall.Args[2].(*ast.CallExpr); !ok || wtCallName(c.Fun) != fcAdapterFn {
				t.Errorf("CorrectionHandler's record argument is %s, want a %s(...) call", wtRender(handlerCall.Args[2]), fcAdapterFn)
			}
		}
		return true
	})

	if !foundPing {
		t.Fatal("control needle: no GET /v1/ping registration found -- the AST walk itself is broken, so the assertion below is vacuous")
	}
	if !pingOnAppMux {
		t.Fatal("control needle: the GET /v1/ping receiver does not render as app.Mux -- the receiver check above cannot fail")
	}
	if !found {
		t.Errorf(`no app.Mux.HandleFunc(%q, extraction.CorrectionHandler(...)) registration found in cmd/submission/main.go`, fcRoute)
	}
}

// The event literal belongs in cmd/, never in internal/extraction: a const identifier inside
// that package reads as a non-literal to internal/platform/db's repo-wide audit.Record scan and
// lands the call site in no bucket.
func TestNewFieldCorrectedAuditor_SpellsTheEventInCmd(t *testing.T) {
	root, files := eaProdFiles(t)
	extractionFiles := drExtractionFiles(t, files)

	// Control needle: the literal matcher must find a literal that IS in internal/extraction, or
	// the zero-hit clearance below is a broken walk reporting an empty set.
	if got := eaLiteralSites(t, root, extractionFiles, drNeedleLiteral); len(got) != 1 {
		t.Fatalf("the literal matcher found %q at %v under %s, want exactly 1 site -- the matcher is broken", drNeedleLiteral, got, drExtractionD)
	}

	sites := eaLiteralSites(t, root, files, fcEvent)

	var inCmd []string
	for _, s := range sites {
		if strings.HasPrefix(s, "cmd/submission/") {
			inCmd = append(inCmd, s)
		}
	}
	if len(inCmd) == 0 {
		t.Errorf("%q is spelled as a production literal at %v, none of them under cmd/submission -- %s is the composition root's job", fcEvent, sites, fcAdapterFn)
	}
	if got := eaLiteralSites(t, root, extractionFiles, fcEvent); len(got) != 0 {
		t.Errorf("%q is spelled as a production literal under %s at %v -- a literal there drops the call site out of the repo-wide audit.Record partition",
			fcEvent, drExtractionD, got)
	}
}

// The payload the resolver dispatches on. Driving the REAL adapter is the only oracle for the
// key spelling: internal/extraction's DB suite injects a recorder of its own, so a production
// adapter writing "id" instead passes every test in it while every row resolves entity_id NULL.
func TestNewFieldCorrectedAuditor_WritesTheInvoiceIdPayload(t *testing.T) {
	tx := &eaTx{}
	c := extraction.FieldCorrection{
		InvoiceID: fcInvoiceID, FieldName: "total", Method: extraction.MethodPointed,
	}

	if err := newFieldCorrectedAuditor()(context.Background(), tx, fcActor, c); err != nil {
		t.Fatalf("the recorder returned %v, want nil", err)
	}
	row := eaDecodeOne(t, tx)

	if row.actor != fcActor {
		t.Errorf("the row names actor %q, want the caller's own subject %q", row.actor, fcActor)
	}
	if row.event != fcEvent {
		t.Errorf("the row carries event %q, want %q", row.event, fcEvent)
	}
	want := map[string]any{"invoice_id": fcInvoiceID, "field": "total", "method": "pointed"}
	if !reflect.DeepEqual(row.payload, want) {
		t.Errorf("the row carries payload %v, want exactly %v -- audit_log_set_entity dispatches this event on payload->>'invoice_id', and any other spelling resolves entity_id NULL, which claims the correction was firm-wide",
			row.payload, want)
	}
	if _, ok := row.payload["value"]; ok {
		t.Errorf("the row carries the corrected value %v; audit_log is append-only and business content does not belong in it", row.payload["value"])
	}
}

// fcEditSpy records the EditInput the adapter builds and reports a chosen outcome.
type fcEditSpy struct {
	calls      int
	gotDocID   string
	gotInput   invoice.EditInput
	err        error
	returnedID string
}

func (s *fcEditSpy) edit(_ context.Context, _ pgx.Tx, documentID string, in invoice.EditInput) (invoice.Invoice, error) {
	s.calls++
	s.gotDocID, s.gotInput = documentID, in
	if s.err != nil {
		return invoice.Invoice{}, s.err
	}
	return invoice.Invoice{ID: s.returnedID}, nil
}

// Each domain outcome crosses the seam as one of internal/extraction's own sentinels, so
// statusForErr maps it by identity. Anything unrecognised must pass through raw and stay a 500.
func TestNewInvoiceFieldApplier_MapsEachDomainError(t *testing.T) {
	raw := errors.New("dial tcp 10.0.0.7:5432: connection refused")

	for _, tc := range []struct {
		name string
		in   error
		want error
	}{
		{"no invoice filed from the document", invoice.ErrNotFound, extraction.ErrNoInvoiceForDocument},
		{"invoice past the fixable states", invoice.ErrNotFixable, extraction.ErrInvoiceNotEditable},
		{"the invoice refused the value", invoice.ErrValidation, extraction.ErrValueRefused},
		{"anything else, raw", raw, raw},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &fcEditSpy{err: tc.in, returnedID: fcInvoiceID}

			id, err := newInvoiceFieldApplier(spy.edit)(context.Background(), nil, fcDocumentID, "total", fcCorrectedTo)

			if !errors.Is(err, tc.want) {
				t.Errorf("the adapter returned %v for %v, want %v", err, tc.in, tc.want)
			}
			if id != "" {
				t.Errorf("the adapter returned invoice id %q alongside an error, want the empty string", id)
			}
			if spy.calls != 1 {
				t.Errorf("the edit ran %d time(s), want 1", spy.calls)
			}
		})
	}

	// The success arm: the invoice the edit reached is what the audit payload will name.
	spy := &fcEditSpy{returnedID: fcInvoiceID}
	id, err := newInvoiceFieldApplier(spy.edit)(context.Background(), nil, fcDocumentID, "total", fcCorrectedTo)
	if err != nil {
		t.Fatalf("the adapter returned %v on a successful edit, want nil", err)
	}
	if id != fcInvoiceID {
		t.Errorf("the adapter returned invoice id %q, want the one the edit reached, %q", id, fcInvoiceID)
	}
	if spy.gotDocID != fcDocumentID {
		t.Errorf("the edit was handed document %q, want %q", spy.gotDocID, fcDocumentID)
	}
}

// The field-to-column projection lives here, not in the handler: internal/extraction cannot name
// invoice.UpdateInput, so the handler validates the field NAME and this adapter is the only place
// that can put the value on the right member. A projection that wrote nothing would leave every
// status assertion in internal/extraction green.
func TestNewInvoiceFieldApplier_MapsEachWritableFieldOntoItsColumn(t *testing.T) {
	issued := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		field string
		value string
		read  func(invoice.UpdateInput) any
		want  any
	}{
		{"issue_date", "2026-03-01", func(in invoice.UpdateInput) any { return in.IssueDate }, &issued},
		{"buyer_tin", "12345678-0001", func(in invoice.UpdateInput) any { return in.BuyerTIN }, fcStr("12345678-0001")},
		{"buyer_name", "Honeywell Group", func(in invoice.UpdateInput) any { return in.BuyerName }, fcStr("Honeywell Group")},
		{"currency", "NGN", func(in invoice.UpdateInput) any { return in.Currency }, fcStr("NGN")},
		{"subtotal", "1000.00", func(in invoice.UpdateInput) any { return in.Subtotal }, fcStr("1000.00")},
		{"vat", "75.00", func(in invoice.UpdateInput) any { return in.VAT }, fcStr("75.00")},
		{"total", "1075.00", func(in invoice.UpdateInput) any { return in.Total }, fcStr("1075.00")},
	} {
		t.Run(tc.field, func(t *testing.T) {
			spy := &fcEditSpy{returnedID: fcInvoiceID}

			if _, err := newInvoiceFieldApplier(spy.edit)(context.Background(), nil, fcDocumentID, tc.field, tc.value); err != nil {
				t.Fatalf("applying %s=%q: %v", tc.field, tc.value, err)
			}
			if spy.calls != 1 {
				t.Fatalf("the edit ran %d time(s) for %s, want 1", spy.calls, tc.field)
			}
			if got := fcShow(tc.read(spy.gotInput.UpdateInput)); got != fcShow(tc.want) {
				t.Errorf("%s=%q reached the edit as %s, want %s", tc.field, tc.value, got, fcShow(tc.want))
			}
			if n := fcNonNil(spy.gotInput.UpdateInput); n != 1 {
				t.Errorf("%s=%q built an EditInput with %d non-nil header field(s), want exactly 1 -- a correction settles one field", tc.field, tc.value, n)
			}
			if spy.gotInput.LineItems != nil {
				t.Errorf("%s=%q sent a line-items array; a header correction must leave the stored lines alone", tc.field, tc.value)
			}
		})
	}
}

func fcStr(s string) *string { return &s }

// fcShow renders a possibly-nil pointer so a mismatch names values rather than addresses.
func fcShow(v any) string {
	switch p := v.(type) {
	case *string:
		if p == nil {
			return "<nil>"
		}
		return *p
	case *time.Time:
		if p == nil {
			return "<nil>"
		}
		return p.Format(time.RFC3339)
	}
	return "<unreadable>"
}

func fcNonNil(in invoice.UpdateInput) int {
	n := 0
	for _, set := range []bool{
		in.IssueDate != nil, in.SupplierTIN != nil, in.SupplierName != nil,
		in.BuyerTIN != nil, in.BuyerName != nil, in.Currency != nil,
		in.Subtotal != nil, in.VAT != nil, in.Total != nil,
	} {
		if set {
			n++
		}
	}
	return n
}
