// lineitems_route_test.go: POST /v1/extractions/{id}/line-items -- registration, adapter and
// the doc row it owes. Same three questions correction_route_test.go answers for the field
// route: is it mounted on app.Mux over the real collaborators, does each domain error cross by
// identity, and does docs/read-path-suspension.md declare it. Nothing here serves a mux or
// opens a database.
//
// Helpers use an li* prefix; fc dr ea wt ds up pgi are taken.
package main

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

const (
	liRoute      = "POST /v1/extractions/{id}/line-items"
	liApplierFn  = "newInvoiceLineItemsApplier"
	liHandlerFn  = "LineItemsHandler"
	liDocumentID = "6a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"

	// docs/read-path-suspension.md reads "62 distinct routes, 68 registrations" before this
	// route lands. Floors, so a later story raises them rather than breaking this.
	liMinDocRoutes        = 63
	liMinDocRegistrations = 69

	// Population floor for the walk below: cmd/submission/main.go registers 7 string-literal
	// patterns today. A floor, so an unrelated route may leave without breaking this; 0 (a
	// broken walk) still reds.
	liMinRegistrationsWalked = 6
)

// Each domain outcome must cross the seam as one of internal/extraction's own sentinels, exactly
// like newInvoiceFieldApplier's own adapter -- built the same way, over the same
// invoiceFieldEdit shape, so it reuses fcEditSpy rather than a second double.
func TestNewInvoiceLineItemsApplier_MapsEachDomainError(t *testing.T) {
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

			id, err := newInvoiceLineItemsApplier(spy.edit)(context.Background(), nil, liDocumentID, []extraction.LineItemInput{})

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

	// The success arm: the invoice id the edit reached is what the audit payload will name.
	spy := &fcEditSpy{returnedID: fcInvoiceID}
	id, err := newInvoiceLineItemsApplier(spy.edit)(context.Background(), nil, liDocumentID, []extraction.LineItemInput{})
	if err != nil {
		t.Fatalf("the adapter returned %v on a successful edit, want nil", err)
	}
	if id != fcInvoiceID {
		t.Errorf("the adapter returned invoice id %q, want the one the edit reached, %q", id, fcInvoiceID)
	}
	if spy.gotDocID != liDocumentID {
		t.Errorf("the edit was handed document %q, want %q", spy.gotDocID, liDocumentID)
	}
}

// A NIL EditInput.LineItems pointer means "leave the lines alone"; a non-nil one replaces the
// whole set ([line-items-optional]), so this route must never send nil. Only the POINTER carries
// that meaning: `&converted` is non-nil however converted was built, so this does not
// distinguish make from append.
func TestNewInvoiceLineItemsApplier_AlwaysPassesANonNilPointer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []extraction.LineItemInput
	}{
		{"nil input slice", nil},
		{"empty input slice", []extraction.LineItemInput{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &fcEditSpy{returnedID: fcInvoiceID}

			if _, err := newInvoiceLineItemsApplier(spy.edit)(context.Background(), nil, liDocumentID, tc.lines); err != nil {
				t.Fatalf("the adapter returned %v, want nil", err)
			}

			got := spy.gotInput
			if got.LineItems == nil {
				t.Fatalf("EditInput.LineItems is nil for %s -- a nil pointer means \"leave the lines alone\", not \"remove every line\"", tc.name)
			}
			if len(*got.LineItems) != 0 {
				t.Errorf("EditInput.LineItems has %d entries, want 0", len(*got.LineItems))
			}
			if n := fcNonNil(got.UpdateInput); n != 0 {
				t.Errorf("EditInput.UpdateInput carries %d non-nil header field(s), want 0 -- a line-items post must never touch a header column", n)
			}
		})
	}
}

// Field-for-field, order-for-order: a transposed assignment (e.g. UnitPrice into LineTotal)
// passes any test that only checks length or a single populated cell.
func TestNewInvoiceLineItemsApplier_CopiesEveryCellByPosition(t *testing.T) {
	desc1, qty1, price1, total1 := "Widget", "2", "10.00", "20.00"
	desc2 := "Gadget"
	desc3, qty3, price3, total3 := "Gizmo", "1", "5.00", "5.00"

	in := []extraction.LineItemInput{
		{Description: &desc1, Quantity: &qty1, UnitPrice: &price1, LineTotal: &total1},
		{Description: &desc2}, // Quantity/UnitPrice/LineTotal nil on purpose
		{Description: &desc3, Quantity: &qty3, UnitPrice: &price3, LineTotal: &total3},
	}
	want := []invoice.LineItemInput{
		{Description: &desc1, Quantity: &qty1, UnitPrice: &price1, LineTotal: &total1},
		{Description: &desc2},
		{Description: &desc3, Quantity: &qty3, UnitPrice: &price3, LineTotal: &total3},
	}

	spy := &fcEditSpy{returnedID: fcInvoiceID}
	if _, err := newInvoiceLineItemsApplier(spy.edit)(context.Background(), nil, liDocumentID, in); err != nil {
		t.Fatalf("the adapter returned %v, want nil", err)
	}

	got := spy.gotInput.LineItems
	if got == nil {
		t.Fatalf("EditInput.LineItems is nil")
	}
	if len(*got) != len(want) {
		t.Fatalf("%d line(s) captured, want %d", len(*got), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual((*got)[i], want[i]) {
			t.Errorf("line %d = %+v, want %+v -- field-for-field, order-for-order", i, (*got)[i], want[i])
		}
	}
}

// D-04-1: invoice.LineItemInput carries a fifth field, LineTax, that extraction.LineItemInput
// does not. A replace-all save from this route must leave it nil rather than erasing whatever
// line_tax the row already stored by accident -- pinned so that is a choice, not a silent bug.
func TestNewInvoiceLineItemsApplier_LeavesLineTaxNil(t *testing.T) {
	desc, qty, price, total := "Widget", "2", "10.00", "20.00"
	in := []extraction.LineItemInput{
		{Description: &desc, Quantity: &qty, UnitPrice: &price, LineTotal: &total},
	}

	spy := &fcEditSpy{returnedID: fcInvoiceID}
	if _, err := newInvoiceLineItemsApplier(spy.edit)(context.Background(), nil, liDocumentID, in); err != nil {
		t.Fatalf("the adapter returned %v, want nil", err)
	}

	got := spy.gotInput.LineItems
	if got == nil || len(*got) != 1 {
		t.Fatalf("captured %v line(s), want 1", got)
	}
	if (*got)[0].LineTax != nil {
		t.Errorf("LineItemInput.LineTax = %q, want nil -- D-04-1: the grid must not claim a tax value the extractor never read", *(*got)[0].LineTax)
	}
}

// The route must be mounted on app.Mux and dispatched to extraction.LineItemsHandler over the
// REAL store method and the shipped auditor -- mirrors
// TestSubmissionMain_WiresTheCorrectionRouteAndItsCollaborators, plus one extra pin: the
// applier's own argument is invStore.EditBySourceDocumentTx, not a double, because the unit
// tests above already prove what the adapter does with whatever edit func it is given.
func TestSubmissionMain_WiresTheLineItemsRouteAndItsCollaborators(t *testing.T) {
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
		switch strings.Trim(lit.Value, "\"") {
		case "GET /v1/ping":
			foundPing = true
			pingOnAppMux = wtRender(sel.X) == "app.Mux"
		case liRoute:
			found = true
			if got := wtRender(sel.X); got != "app.Mux" {
				t.Errorf("%s is registered on %s, want app.Mux -- only app.Mux is served", liRoute, got)
			}
			handlerCall, ok := call.Args[1].(*ast.CallExpr)
			if !ok {
				t.Errorf("%s' second argument is %T, want a call expression", liRoute, call.Args[1])
				return true
			}
			hsel, ok := handlerCall.Fun.(*ast.SelectorExpr)
			if !ok || hsel.Sel.Name != liHandlerFn {
				t.Errorf("%s' handler call is not ....%s(...), got %s", liRoute, liHandlerFn, wtRender(handlerCall.Fun))
				return true
			}
			if pkg, ok := hsel.X.(*ast.Ident); !ok || pkg.Name != "extraction" {
				t.Errorf("%s' handler is not extraction.%s(...), got %s", liRoute, liHandlerFn, wtRender(handlerCall.Fun))
			}
			if len(handlerCall.Args) != 4 {
				t.Errorf("extraction.%s is called with %d argument(s), want 4 (pool, apply, record, logger)", liHandlerFn, len(handlerCall.Args))
				return true
			}
			applierCall, ok := handlerCall.Args[1].(*ast.CallExpr)
			if !ok || wtCallName(applierCall.Fun) != liApplierFn {
				t.Errorf("%s's apply argument is %s, want a %s(...) call", liHandlerFn, wtRender(handlerCall.Args[1]), liApplierFn)
			} else if len(applierCall.Args) != 1 || wtRender(applierCall.Args[0]) != "invStore.EditBySourceDocumentTx" {
				t.Errorf("%s's argument is %s, want invStore.EditBySourceDocumentTx -- the real store, not a double", liApplierFn, wtRender(applierCall.Args[0]))
			}
			if c, ok := handlerCall.Args[2].(*ast.CallExpr); !ok || wtCallName(c.Fun) != fcAdapterFn {
				t.Errorf("%s's record argument is %s, want a %s(...) call", liHandlerFn, wtRender(handlerCall.Args[2]), fcAdapterFn)
			}
			if got := wtRender(handlerCall.Args[0]); got != fcPoolArg {
				t.Errorf("%s's pool argument is %s, want %s -- the route must run on the service's own invoice_app pool", liHandlerFn, got, fcPoolArg)
			}
			if got := wtRender(handlerCall.Args[3]); got != fcLoggerArg {
				t.Errorf("%s's logger argument is %s, want %s", liHandlerFn, got, fcLoggerArg)
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
		t.Errorf(`no app.Mux.HandleFunc(%q, extraction.%s(...)) registration found in cmd/submission/main.go`, liRoute, liHandlerFn)
	}
}

// TestRLS_ReadPathSuspensionDocEnumeratesEveryRoute reds the moment the route is registered
// without a row; this says the doc owes the row now, and the corrected counts, reusing
// correction_route_test.go's own fcDeclaredRoutes parser rather than a second copy.
func TestReadPathSuspensionDoc_DeclaresTheLineItemsRoute(t *testing.T) {
	lines := drDocSection(t)
	declared := fcDeclaredRoutes(t, lines)

	// Control needle: the collection route this one hangs off is declared today, so a broken
	// parser fails here rather than reporting the line-items route missing.
	if got, ok := declared["GET /v1/extractions"]; !ok || got != "covered" {
		t.Fatalf("the parse read `GET /v1/extractions` as verdict %q (present=%v), want covered -- the row parser is broken", got, ok)
	}

	verdict, ok := declared[liRoute]
	switch {
	case !ok:
		t.Errorf("%s declares no row for `%s`", drDocPath, liRoute)
	case verdict != "covered":
		t.Errorf("%s declares `%s` with verdict %q, want exactly covered", drDocPath, liRoute, verdict)
	}

	var m []string
	for _, line := range lines {
		if got := drCountLine.FindStringSubmatch(line); got != nil {
			if m != nil {
				t.Fatalf("%s carries two route-count sentences; they can disagree", drDocPath)
			}
			m = got
		}
	}
	if m == nil {
		t.Fatalf("%s's endpoint section carries no \"N distinct routes, M registrations\" sentence", drDocPath)
	}
	routes, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("route count %q is not a number: %v", m[1], err)
	}
	registrations, err := strconv.Atoi(m[2])
	if err != nil {
		t.Fatalf("registration count %q is not a number: %v", m[2], err)
	}
	if routes < liMinDocRoutes {
		t.Errorf("%s claims %d distinct routes, want at least %d -- the line-items route raises it by one", drDocPath, routes, liMinDocRoutes)
	}
	if registrations < liMinDocRegistrations {
		t.Errorf("%s claims %d registrations, want at least %d -- the line-items route raises it by one", drDocPath, registrations, liMinDocRegistrations)
	}
}

// TestNewFieldCorrectedAuditor_SpellsTheEventInCmd allows any number of cmd/submission spellings.
// This route reuses the shipped auditor rather than adding a second; that is what pins it.
func TestFieldCorrectedEvent_IsSpelledExactlyOnceInProduction(t *testing.T) {
	root, files := eaProdFiles(t)

	// Control needle: the matcher finds a literal that IS present, or "exactly one" below is a
	// broken walk reporting an empty set.
	if got := eaLiteralSites(t, root, files, drNeedleLiteral); len(got) == 0 {
		t.Fatalf("the literal matcher found no site for %q -- the scan is broken, so the count below means nothing", drNeedleLiteral)
	}

	sites := eaLiteralSites(t, root, files, fcEvent)
	if len(sites) != 1 {
		t.Fatalf("%q is spelled as a production literal at %v, want exactly 1 site -- one seam, one auditor", fcEvent, sites)
	}
	if !strings.HasPrefix(sites[0], "cmd/submission/main.go:") {
		t.Errorf("%q is spelled at %s, want cmd/submission/main.go -- the composition root's job", fcEvent, sites[0])
	}
}

// net/http's mux PANICS on a duplicate pattern, which no test in this package would reach
// because none builds a mux. A second registration is therefore a boot-time crash that only
// the AST can see here.
func TestSubmissionMain_RegistersTheLineItemsRouteExactlyOnce(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/submission/main.go: %v", err)
	}

	var walked, hits int
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
		walked++
		if strings.Trim(lit.Value, "\"") == liRoute {
			hits++
		}
		return true
	})

	// Population floor: the walk must actually reach this file's registrations.
	if walked < liMinRegistrationsWalked {
		t.Fatalf("the walk reached %d HandleFunc registration(s), want at least %d -- a broken walk reports 0 hits and reads as a clean single registration", walked, liMinRegistrationsWalked)
	}
	if hits != 1 {
		t.Errorf("%s is registered %d time(s) on the mux, want exactly 1 -- net/http panics at boot on a duplicate pattern", liRoute, hits)
	}
}

// If either type grows a field, the adapter leaves the new invoice cell nil and
// CopiesEveryCellByPosition still passes -- both sides get the zero. This forces D-04-1 to be
// retaken rather than defaulted.
func TestLineItemInputTypes_CarryTheFieldsTheAdapterWasWrittenFor(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  reflect.Type
		want []string
	}{
		{"extraction.LineItemInput", reflect.TypeOf(extraction.LineItemInput{}), []string{"Description", "Quantity", "UnitPrice", "LineTotal"}},
		{"invoice.LineItemInput", reflect.TypeOf(invoice.LineItemInput{}), []string{"Description", "Quantity", "UnitPrice", "LineTotal", "LineTax"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.want) == 0 {
				t.Fatal("the expected field list is empty; the comparison below would assert nothing")
			}
			var got []string
			for i := 0; i < tc.typ.NumField(); i++ {
				got = append(got, tc.typ.Field(i).Name)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s has fields %v, want %v -- a new cell needs a deliberate mapping decision (D-04-1), not a silent nil", tc.name, got, tc.want)
			}
		})
	}
}

// The conversion must happen before the edit runs, so a failing edit still saw the replace-all
// shape -- and the handler's own transaction must reach the store untouched, or the row lands
// outside the request's tx.
func TestNewInvoiceLineItemsApplier_HandsTheTxThroughAndConvertsBeforeTheEditFails(t *testing.T) {
	desc := "Widget"
	in := []extraction.LineItemInput{{Description: &desc}}
	tx := &eaTx{}

	spy := &fcEditSpy{err: invoice.ErrNotFixable, returnedID: fcInvoiceID}
	if _, err := newInvoiceLineItemsApplier(spy.edit)(context.Background(), tx, liDocumentID, in); !errors.Is(err, extraction.ErrInvoiceNotEditable) {
		t.Fatalf("the adapter returned %v, want %v", err, extraction.ErrInvoiceNotEditable)
	}

	if spy.gotTx != tx {
		t.Errorf("the edit was handed tx %v, want the caller's own %v -- a different handle writes outside the request's transaction", spy.gotTx, tx)
	}
	if spy.gotInput.LineItems == nil || len(*spy.gotInput.LineItems) != 1 {
		t.Fatalf("the failing edit saw LineItems %v, want a non-nil pointer to 1 line -- the conversion must precede the call", spy.gotInput.LineItems)
	}
	if got := (*spy.gotInput.LineItems)[0].Description; got == nil || *got != desc {
		t.Errorf("the failing edit saw description %v, want %q", got, desc)
	}
}

// The caller's slice is the decoded request body; the adapter must not write back into it.
func TestNewInvoiceLineItemsApplier_LeavesTheCallersSliceUntouched(t *testing.T) {
	desc, qty := "Widget", "2"
	in := []extraction.LineItemInput{{Description: &desc, Quantity: &qty}}
	want := []extraction.LineItemInput{{Description: &desc, Quantity: &qty}}

	spy := &fcEditSpy{returnedID: fcInvoiceID}
	if _, err := newInvoiceLineItemsApplier(spy.edit)(context.Background(), nil, liDocumentID, in); err != nil {
		t.Fatalf("the adapter returned %v, want nil", err)
	}

	if !reflect.DeepEqual(in, want) {
		t.Errorf("the caller's slice is now %+v, want %+v -- the adapter must convert, never write back", in, want)
	}
}
