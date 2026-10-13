package importer

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Copy of defaultInvoiceKindInvalid in handlers.go.
const defaultKindMsg = "default_invoice_kind must be B2B, B2G or B2C"

// kindSpy records the default the handler passes to the import func.
type kindSpy struct {
	calls int
	kind  string
	err   error
}

func (k *kindSpy) fn() importFunc {
	return func(_ context.Context, _, _, _ string, _ int, _ map[string]string, _ []string, _ [][]string, _ bool, defaultInvoiceKind string) (BatchResult, error) {
		k.calls++
		k.kind = defaultInvoiceKind
		if k.err != nil {
			return BatchResult{}, k.err
		}
		return BatchResult{ID: "b1", Status: "completed", Errors: []RowError{}, InvoiceViolations: []InvoiceViolations{}}, nil
	}
}

func postKind(t *testing.T, imp importFunc, spy *saveSpy, parts ...importPart) (int, importBatchBody, []byte) {
	t.Helper()
	id := testIdentity()
	mappingJSON := mustMappingJSON(t, map[string]string{"invoice_number": "Invoice No"})
	open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Invoice No"}, [][]string{{"INV-1"}}))
	body, ct := buildImportForm(t, uuid.NewString(), mappingJSON, open.doc.ID, parts...)
	rec, raw, resp := doImportSave(t, imp, open.fn(), spy.fn(), nil, &id, "", ct, body)
	return rec.Code, resp, raw
}

func TestCreateHandler_RejectsADefaultKindOutsideTheThree(t *testing.T) {
	for _, v := range []string{"b2b", "B2X", " B2B"} {
		t.Run(v, func(t *testing.T) {
			k := &kindSpy{}
			code, resp, raw := postKind(t, k.fn(), &saveSpy{}, importPart{field: "default_invoice_kind", content: []byte(v)})
			if code != http.StatusBadRequest || resp.Error != defaultKindMsg {
				t.Fatalf("status = %d, error = %q, want 400 %q (body=%s)", code, resp.Error, defaultKindMsg, raw)
			}
			if k.calls != 0 {
				t.Errorf("import func calls = %d, want 0", k.calls)
			}
		})
	}
}

func TestCreateHandler_PassesAValidDefaultKind(t *testing.T) {
	for _, v := range []string{"B2B", "B2G", "B2C"} {
		k := &kindSpy{}
		code, _, raw := postKind(t, k.fn(), &saveSpy{}, importPart{field: "default_invoice_kind", content: []byte(v)})
		if code != http.StatusCreated || k.kind != v {
			t.Errorf("%s: status = %d, kind = %q, want 201 and %q (body=%s)", v, code, k.kind, v, raw)
		}
	}
}

func TestCreateHandler_NoDefaultPartPassesEmpty(t *testing.T) {
	k := &kindSpy{}
	code, _, raw := postKind(t, k.fn(), &saveSpy{})
	if code != http.StatusCreated || k.calls != 1 || k.kind != "" {
		t.Errorf("status = %d, calls = %d, kind = %q, want 201, 1, \"\" (body=%s)", code, k.calls, k.kind, raw)
	}
}

func TestCreateHandler_ADefaultWithAMappedKindIs400(t *testing.T) {
	k := &kindSpy{err: fmt.Errorf("%w: default_invoice_kind cannot be set when invoice_kind is mapped", ErrValidation)}
	code, resp, raw := postKind(t, k.fn(), &saveSpy{}, importPart{field: "default_invoice_kind", content: []byte("B2B")})
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", code, raw)
	}
	if resp.Error == "" {
		t.Errorf("error body empty, want the service message")
	}
}

func TestCreateHandler_TheSavedMappingHoldsNoDefault(t *testing.T) {
	k := &kindSpy{}
	spy := &saveSpy{}
	code, _, raw := postKind(t, k.fn(), spy,
		importPart{field: "default_invoice_kind", content: []byte("B2G")},
		importPart{field: "remember_mapping", content: []byte("true")})
	if code != http.StatusCreated || len(spy.calls) != 1 {
		t.Fatalf("status = %d, save calls = %d, want 201 and 1 (body=%s)", code, len(spy.calls), raw)
	}
	if got := spy.calls[0].mapping; len(got) != 1 || got["invoice_number"] != "Invoice No" {
		t.Errorf("saved mapping = %v, want only invoice_number", got)
	}
}

func TestCreateHandler_ADefaultWithAMappedKindIs400FromTheRealService(t *testing.T) {
	id := testIdentity()
	mappingJSON := mustMappingJSON(t, map[string]string{"invoice_number": "Invoice No", "invoice_kind": "Kind"})
	open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Invoice No", "Kind"}, [][]string{{"INV-1", "B2C"}}))
	body, ct := buildImportForm(t, uuid.NewString(), mappingJSON, open.doc.ID,
		importPart{field: "default_invoice_kind", content: []byte("B2B")})
	rec, raw, resp := doImportSave(t, (&Service{}).Import, open.fn(), (&saveSpy{}).fn(), nil, &id, "", ct, body)
	want := "default_invoice_kind cannot be set when invoice_kind is mapped"
	if rec.Code != http.StatusBadRequest || !strings.Contains(resp.Error, want) {
		t.Errorf("status = %d, error = %q, want 400 containing %q (body=%s)", rec.Code, resp.Error, want, raw)
	}
}
