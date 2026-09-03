// correction_route_test.go: where the correction route is REGISTERED, where its audit event is
// SPELLED, and what its invoice adapter does with each domain outcome. Nothing here serves a mux
// or opens a database; the claims are read off main.go's source and off the adapters directly.
//
// Helpers use an fc* prefix; dr ea wt ds up are taken.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
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
	fcPoolArg     = "pool"
	fcLoggerArg   = "app.Logger"

	// The method every row here posts. Named so a later undo-specific row reads as the
	// deliberate other half rather than as a typo.
	fcMethodTyped = extraction.MethodTyped

	// docs/read-path-suspension.md reads "61 distinct routes, 67 registrations" before this
	// route lands. Floors, so a later story raises them rather than breaking this.
	fcMinDocRoutes        = 62
	fcMinDocRegistrations = 68
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
			if len(handlerCall.Args) != 5 {
				t.Errorf("extraction.CorrectionHandler is called with %d argument(s), want 5 (pool, apply, record, recordLearned, logger)", len(handlerCall.Args))
				return true
			}
			if c, ok := handlerCall.Args[1].(*ast.CallExpr); !ok || wtCallName(c.Fun) != fcApplierFn {
				t.Errorf("CorrectionHandler's apply argument is %s, want a %s(...) call: only that closure runs the shared edit path on the handler's own transaction",
					wtRender(handlerCall.Args[1]), fcApplierFn)
			}
			if c, ok := handlerCall.Args[2].(*ast.CallExpr); !ok || wtCallName(c.Fun) != fcAdapterFn {
				t.Errorf("CorrectionHandler's record argument is %s, want a %s(...) call", wtRender(handlerCall.Args[2]), fcAdapterFn)
			}
			// The pool and the logger, for the same reason: nothing in Go dereferences either
			// until a request arrives, so a nil in argument 0 compiles, passes every adapter
			// test and panics inside BeginTx on the first correction.
			if got := wtRender(handlerCall.Args[0]); got != fcPoolArg {
				t.Errorf("CorrectionHandler's pool argument is %s, want %s -- the route must run on the service's own invoice_app pool", got, fcPoolArg)
			}
			// The learning recorder is its own adapter, not newFieldCorrectedAuditor widened:
			// cmd/submission hands that one to LineItemsHandler too, and the line-items route
			// learns nothing.
			if c, ok := handlerCall.Args[3].(*ast.CallExpr); !ok || wtCallName(c.Fun) != alAdapterFn {
				t.Errorf("CorrectionHandler's recordLearned argument is %s, want a %s(...) call", wtRender(handlerCall.Args[3]), alAdapterFn)
			}
			if got := wtRender(handlerCall.Args[4]); got != fcLoggerArg {
				t.Errorf("CorrectionHandler's logger argument is %s, want %s", got, fcLoggerArg)
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
	gotTx      pgx.Tx
	gotInput   invoice.EditInput
	err        error
	returnedID string
}

func (s *fcEditSpy) edit(_ context.Context, tx pgx.Tx, documentID string, in invoice.EditInput) (invoice.Invoice, error) {
	s.calls++
	s.gotDocID, s.gotTx, s.gotInput = documentID, tx, in
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

			id, err := newInvoiceFieldApplier(spy.edit)(context.Background(), nil, fcDocumentID, "total", fcStr(fcCorrectedTo), fcMethodTyped)

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

	// The success arm: the invoice the edit reached is what the audit payload will name, and the
	// handler's OWN transaction is what the edit runs on. internal/extraction's DB suite injects
	// an applier of its own, so nothing there can see this adapter drop the transaction -- and a
	// nil one panics inside EditBySourceDocumentTx on the first correction.
	var tx pgx.Tx = &eaTx{}
	spy := &fcEditSpy{returnedID: fcInvoiceID}
	id, err := newInvoiceFieldApplier(spy.edit)(context.Background(), tx, fcDocumentID, "total", fcStr(fcCorrectedTo), fcMethodTyped)
	if err != nil {
		t.Fatalf("the adapter returned %v on a successful edit, want nil", err)
	}
	if id != fcInvoiceID {
		t.Errorf("the adapter returned invoice id %q, want the one the edit reached, %q", id, fcInvoiceID)
	}
	if spy.gotDocID != fcDocumentID {
		t.Errorf("the edit was handed document %q, want %q", spy.gotDocID, fcDocumentID)
	}
	if spy.gotTx != tx {
		t.Errorf("the edit was handed transaction %v, want the caller's own %v -- the correction row, the invoice write and the audit row share one transaction or none of the three does", spy.gotTx, tx)
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

			if _, err := newInvoiceFieldApplier(spy.edit)(context.Background(), nil, fcDocumentID, tc.field, fcStr(tc.value), fcMethodTyped); err != nil {
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

// A nil value is an undo of a field the extractor never read: every writable column takes its
// own clear sentinel, so the invoice ends up holding nothing rather than the empty string --
// which for the three numeric columns is 22P02, not a clear. issue_date is the arm that would
// otherwise time.Parse a value that is not there.
func TestInvoiceEditFor_ANilValueClearsEveryWritableColumn(t *testing.T) {
	for _, tc := range []struct {
		field string
		read  func(invoice.UpdateInput) any
		want  any
	}{
		{"issue_date", func(in invoice.UpdateInput) any { return in.IssueDate }, invoice.ClearDate},
		{"buyer_tin", func(in invoice.UpdateInput) any { return in.BuyerTIN }, invoice.ClearText},
		{"buyer_name", func(in invoice.UpdateInput) any { return in.BuyerName }, invoice.ClearText},
		{"currency", func(in invoice.UpdateInput) any { return in.Currency }, invoice.ClearText},
		{"subtotal", func(in invoice.UpdateInput) any { return in.Subtotal }, invoice.ClearText},
		{"vat", func(in invoice.UpdateInput) any { return in.VAT }, invoice.ClearText},
		{"total", func(in invoice.UpdateInput) any { return in.Total }, invoice.ClearText},
	} {
		t.Run(tc.field, func(t *testing.T) {
			in, err := invoiceEditFor(tc.field, nil)
			if err != nil {
				t.Fatalf("clearing %s: %v", tc.field, err)
			}
			// Pointer IDENTITY: a copy of the sentinel is refused by updateContentTx, so a
			// value comparison here would pass on an input the write layer rejects.
			if got := tc.read(in.UpdateInput); got != tc.want {
				t.Errorf("%s reached the edit as %s, want the clear sentinel", tc.field, fcShow(got))
			}
			if n := fcNonNil(in.UpdateInput); n != 1 {
				t.Errorf("clearing %s built an EditInput with %d non-nil header field(s), want exactly 1", tc.field, n)
			}
			if in.LineItems != nil {
				t.Errorf("clearing %s sent a line-items array", tc.field)
			}
		})
	}

	// The control: a nil on a field outside the writable seven is still refused, so the clear
	// arm did not widen the vocabulary.
	if _, err := invoiceEditFor("invoice_number", nil); !errors.Is(err, extraction.ErrValueRefused) {
		t.Errorf("clearing invoice_number: err = %v, want ErrValueRefused", err)
	}
}

// The clear is matched by POINTER identity, and a request body can only produce a string. So a
// posted value spelling the sentinel's own contents reaches the write layer as ORDINARY DATA;
// updateContentTx then names it a copy and refuses it (TestUpdateContentTx_RefusesACopiedClearSentinel),
// which newInvoiceFieldApplier maps to ErrValueRefused and the boundary renders as a 400.
func TestInvoiceEditFor_AJSONBodyCannotForgeTheClearSentinel(t *testing.T) {
	var body struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(`{"value":"\u0000invoice.clear"}`), &body); err != nil {
		t.Fatalf("decoding the forged body: %v", err)
	}
	// The floor: the decoded string really is the sentinel's own contents, so a pointer
	// comparison below is the only thing that can tell it from the sentinel.
	if body.Value != *invoice.ClearText {
		t.Fatalf("the forged body decoded to %q, not the sentinel's contents -- every claim below is vacuous", body.Value)
	}

	for _, field := range []string{"buyer_tin", "buyer_name", "currency", "subtotal", "vat", "total"} {
		t.Run(field, func(t *testing.T) {
			in, err := invoiceEditFor(field, &body.Value)
			if err != nil {
				t.Fatalf("%s: %v", field, err)
			}
			got := fcHeaderPtr(t, in.UpdateInput, field)
			if got == invoice.ClearText {
				t.Fatalf("%s reached the write layer AS the clear sentinel; a posted value would clear the column", field)
			}
			if got == nil || *got != body.Value {
				t.Errorf("%s reached the write layer as %s, want the posted text", field, fcShow(got))
			}
		})
	}
}

// fcHeaderPtr reads one writable text member off an UpdateInput by its column name.
func fcHeaderPtr(t *testing.T, in invoice.UpdateInput, field string) *string {
	t.Helper()
	switch field {
	case "buyer_tin":
		return in.BuyerTIN
	case "buyer_name":
		return in.BuyerName
	case "currency":
		return in.Currency
	case "subtotal":
		return in.Subtotal
	case "vat":
		return in.VAT
	case "total":
		return in.Total
	}
	t.Fatalf("%s is not a writable text column", field)
	return nil
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

// fcDeclaredRoutes parses docs/read-path-suspension.md's endpoint table to route -> verdict.
func fcDeclaredRoutes(t *testing.T, lines []string) map[string]string {
	t.Helper()
	declared := map[string]string{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(trimmed, "|"), "|")
		if len(cells) < 3 {
			continue
		}
		m := drRowCell.FindStringSubmatch(strings.TrimSpace(cells[0]))
		if m == nil {
			continue
		}
		declared[m[1]] = strings.TrimSpace(cells[2])
	}
	if len(declared) < drMinDocRows {
		t.Fatalf("%s's endpoint table parsed to %d row(s), want at least %d -- a parse that lost the table finds no missing row either",
			drDocPath, len(declared), drMinDocRows)
	}
	return declared
}

// TestRLS_ReadPathSuspensionDocEnumeratesEveryRoute errors for a registered route with no row,
// but its floors leave the prose count free: the sentence could keep claiming 61/67 while the
// table carried 62. Floors move with the table, so an honest doc is what ships.
func TestReadPathSuspensionDoc_DeclaresTheCorrectionRoute(t *testing.T) {
	lines := drDocSection(t)
	declared := fcDeclaredRoutes(t, lines)

	// Control needle: the collection route this one hangs off is declared today, so a broken
	// parser fails here rather than reporting the correction route missing.
	if got, ok := declared["GET /v1/extractions"]; !ok || got != "covered" {
		t.Fatalf("the parse read `GET /v1/extractions` as verdict %q (present=%v), want covered -- the row parser is broken", got, ok)
	}

	verdict, ok := declared[fcRoute]
	switch {
	case !ok:
		t.Errorf("%s declares no row for `%s`", drDocPath, fcRoute)
	case verdict != "covered":
		t.Errorf("%s declares `%s` with verdict %q, want exactly covered", drDocPath, fcRoute, verdict)
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
	if routes < fcMinDocRoutes {
		t.Errorf("%s claims %d distinct routes, want at least %d -- the correction route raises it by one", drDocPath, routes, fcMinDocRoutes)
	}
	if registrations < fcMinDocRegistrations {
		t.Errorf("%s claims %d registrations, want at least %d -- the correction route raises it by one", drDocPath, registrations, fcMinDocRegistrations)
	}
}
