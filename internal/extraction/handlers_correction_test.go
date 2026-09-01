// handlers_correction_test.go: the status/body/ordering contract of
// POST /v1/extractions/{id}/fields/{name}/corrections, driven with httptest and injected seams.
// No database -- this file must never call stRequire, the package's one sanctioned skip site,
// because scripts/ci/rls-test-gate.sh fails a step on any skip. The DB-backed half lives in
// handlers_correction_db_test.go.
//
// The status scheme these cases pin: 400 malformed input, 422 well-formed but semantically
// refused, 409 state conflict.
//
// Helpers use a cor* prefix; hnd dtl up rd st wk dc fx mx px pr ps pt pd pb pe rx de rp eq cs
// rvd are taken.
package extraction_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- harness --------------------------------------------------------------------------------

const (
	corPathID   = "id"
	corPathName = "name"

	// The wire messages this route owns. hndMsgUnauthorized and hndMsgInternal carry the rest;
	// retyping either here would let the wire and the constant drift apart while this file
	// stayed green.
	corMsgMalformedID     = "id must be a well-formed uuid"
	corMsgBlankValue      = "value must not be blank"
	corMsgUnknownMethod   = "method must be one of typed, chosen, pointed, undone"
	corMsgRegionDisagrees = "only a pointed correction carries a region"
	corMsgBadIssueDate    = "issue_date must be a date this system can read"
	corMsgUnknownField    = "this document field is not one we file on an invoice"
	corMsgInvoiceNumber   = "invoice_number identifies the invoice and is not corrected here"
	corMsgSupplierField   = "supplier_tin and supplier_name come from the client record, not from the document"
)

// corSpy is the injected pair. applies/records count invocations, which is the only way to
// prove a refusal was raised BEFORE either seam ran rather than merely that the status came out
// right.
type corSpy struct {
	applies int
	records int

	gotDocumentID string
	gotField      string
	gotValue      string
	gotSubject    string
	gotCorrection extraction.FieldCorrection

	invoiceID string
	applyErr  error
	recordErr error
}

func newCorSpy() *corSpy {
	return &corSpy{invoiceID: "5d2f7a10-6b3c-4e8d-9f01-2a3b4c5d6e7f"}
}

func (s *corSpy) apply(_ context.Context, _ pgx.Tx, documentID, field, value string) (string, error) {
	s.applies++
	s.gotDocumentID, s.gotField, s.gotValue = documentID, field, value
	if s.applyErr != nil {
		return "", s.applyErr
	}
	return s.invoiceID, nil
}

func (s *corSpy) record(_ context.Context, _ pgx.Tx, subject string, c extraction.FieldCorrection) error {
	s.records++
	s.gotSubject, s.gotCorrection = subject, c
	return s.recordErr
}

// corServe drives the handler once. rawID and rawName are what the mux would have bound; body
// is sent verbatim, so a case can send text that is not JSON at all. A nil identity means the
// request carries none.
//
// The pool is nil on purpose: every case in this file must be answered before the handler opens
// a transaction, so a case that reached the pool would panic rather than pass quietly.
func corServe(t *testing.T, spy *corSpy, rawID, rawName, body string, id *auth.Identity, log *slog.Logger) *httptest.ResponseRecorder {
	t.Helper()
	target := "/v1/extractions/" + rawID + "/fields/" + rawName + "/corrections"
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	r.SetPathValue(corPathID, rawID)
	r.SetPathValue(corPathName, rawName)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	w := httptest.NewRecorder()
	extraction.CorrectionHandler(nil, spy.apply, spy.record, log)(w, r)
	return w
}

// corPost is the authenticated request every case but the 401 uses.
func corPost(t *testing.T, spy *corSpy, rawName, body string) *httptest.ResponseRecorder {
	t.Helper()
	return corServe(t, spy, hndJobID, rawName, body, &hndIdentity, nil)
}

// corBody renders one request body. Region is spelled as raw JSON so a case can send a shape
// the struct could not hold.
func corBody(value, method, extra string) string {
	out := `{"value":` + corQuote(value) + `,"method":` + corQuote(method)
	if extra != "" {
		out += "," + extra
	}
	return out + `}`
}

func corQuote(s string) string { return strconv.Quote(s) }

// corRegion is a well-formed box, as the wire spells it.
const corRegion = `"region":{"page":1,"x0":0.1,"y0":0.2,"x1":0.3,"y1":0.25}`

// corAssertUntouched is the "nothing was written" half of every refusal.
func corAssertUntouched(t *testing.T, spy *corSpy) {
	t.Helper()
	if spy.applies != 0 {
		t.Errorf("the invoice seam ran %d time(s) on a refused request, want 0 -- a refusal that still writes is worse than none", spy.applies)
	}
	if spy.records != 0 {
		t.Errorf("the audit seam ran %d time(s) on a refused request, want 0", spy.records)
	}
}

// --- identity, then the path, then the body -------------------------------------------------

// The path value and the body are both malformed, so a handler that parsed either first would
// answer 400 and leak that it read them at all.
func TestCorrectionHandler_UnauthenticatedIs401BeforeParsing(t *testing.T) {
	spy := newCorSpy()

	w := corServe(t, spy, "nope", "not_a_field", "{", nil, nil)

	hndAssert(t, w, http.StatusUnauthorized, hndErrBody(t, hndMsgUnauthorized))
	corAssertUntouched(t, spy)
}

// The message names this route's own {id}, the spelling the detail and page routes already use:
// a second spelling would tell a caller the wrong field.
func TestCorrectionHandler_MalformedJobIdIs400(t *testing.T) {
	spy := newCorSpy()

	w := corServe(t, spy, "not-a-uuid", "total", corBody("1500.00", "typed", ""), &hndIdentity, nil)

	hndAssert(t, w, http.StatusBadRequest, hndErrBody(t, corMsgMalformedID))
	corAssertUntouched(t, spy)
}

// --- 400: malformed input -------------------------------------------------------------------

// The DB admits a single space by design (extraction_field_corrections_value_check counts
// characters), so this is the only place a blank correction is closed. A stored space renders
// as an empty cell while the cell claims a human changed it.
func TestCorrectionHandler_BlankValueIsRefused(t *testing.T) {
	for _, value := range []string{"", " ", "   ", "\t"} {
		t.Run(corQuote(value), func(t *testing.T) {
			spy := newCorSpy()

			w := corPost(t, spy, "total", corBody(value, "typed", ""))

			hndAssert(t, w, http.StatusBadRequest, hndErrBody(t, corMsgBlankValue))
			corAssertUntouched(t, spy)
		})
	}
}

// Checked at the boundary, so extraction_field_corrections_method_check never surfaces as a 500.
func TestCorrectionHandler_UnknownMethodIsRefused(t *testing.T) {
	for _, method := range []string{"guessed", "", "TYPED", "pointed "} {
		t.Run(corQuote(method), func(t *testing.T) {
			spy := newCorSpy()

			w := corPost(t, spy, "total", corBody("1500.00", method, ""))

			hndAssert(t, w, http.StatusBadRequest, hndErrBody(t, corMsgUnknownMethod))
			corAssertUntouched(t, spy)
		})
	}
}

// Both directions, mirroring extraction_field_corrections_pointed_has_region: pointed without a
// box, and every other method with one.
func TestCorrectionHandler_MethodAndRegionMustAgreeAtTheBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		extra  string
	}{
		{"pointed with no region", "pointed", ""},
		{"typed with a region", "typed", corRegion},
		{"chosen with a region", "chosen", corRegion},
		{"undone with a region", "undone", corRegion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := newCorSpy()

			w := corPost(t, spy, "total", corBody("1500.00", tc.method, tc.extra))

			hndAssert(t, w, http.StatusBadRequest, hndErrBody(t, corMsgRegionDisagrees))
			corAssertUntouched(t, spy)
		})
	}

	// The control: pointed WITH a region is not refused, so the two arms above are refusing the
	// disagreement rather than refusing every pointed correction.
	spy := newCorSpy()
	w := corPost(t, spy, "total", corBody("1500.00", "pointed", corRegion))
	if w.Code == http.StatusBadRequest {
		t.Errorf("a pointed correction carrying a region was refused with 400 %q; the four arms above then prove nothing about the agreement rule", w.Body.String())
	}
}

// issue_date is the one field the handler must parse: UpdateInput.IssueDate is a *time.Time, so
// an unreadable date has to be refused here rather than reaching the invoice as text.
func TestCorrectionHandler_UnparseableIssueDateIsRefused(t *testing.T) {
	for _, value := range []string{"tuesday", "31/31/2026", "2026-13-01", "26-01-02"} {
		t.Run(value, func(t *testing.T) {
			spy := newCorSpy()

			w := corPost(t, spy, "issue_date", corBody(value, "typed", ""))

			hndAssert(t, w, http.StatusBadRequest, hndErrBody(t, corMsgBadIssueDate))
			corAssertUntouched(t, spy)
		})
	}

	// The control: an ISO date reaches the seam unchanged, so the arms above refuse an
	// unreadable date rather than refusing issue_date altogether.
	spy := newCorSpy()
	corPost(t, spy, "issue_date", corBody("2026-03-01", "typed", ""))
	if spy.applies != 1 {
		t.Fatalf("an ISO issue_date reached the invoice seam %d time(s), want 1 -- the arms above refuse the field, not the value", spy.applies)
	}
	if spy.gotField != "issue_date" || spy.gotValue != "2026-03-01" {
		t.Errorf("the seam was handed (%q, %q), want (%q, %q) -- the correction row and the invoice must carry one spelling",
			spy.gotField, spy.gotValue, "issue_date", "2026-03-01")
	}
}

// --- 422: well-formed, semantically refused -------------------------------------------------

func TestCorrectionHandler_UnknownFieldNameIsRefused(t *testing.T) {
	for _, name := range []string{"not_a_field", "total_amount", "TOTAL", "line_items"} {
		t.Run(name, func(t *testing.T) {
			spy := newCorSpy()

			w := corPost(t, spy, name, corBody("1500.00", "typed", ""))

			hndAssert(t, w, http.StatusUnprocessableEntity, hndErrBody(t, corMsgUnknownField))
			corAssertUntouched(t, spy)
		})
	}

	// The control: a name that IS in the vocabulary is not refused for this reason, so the arms
	// above are reading the vocabulary rather than refusing everything.
	spy := newCorSpy()
	w := corPost(t, spy, "total", corBody("1500.00", "typed", ""))
	if w.Code == http.StatusUnprocessableEntity {
		t.Errorf("the field name %q was refused with 422 %q; it is in HeaderFields, so the arms above prove nothing", "total", w.Body.String())
	}
}

// The identity fence: invoice_number is what the invoice is filed under, and the shipped edit
// path refuses to move it.
func TestCorrectionHandler_InvoiceNumberIsRefusedWithAReason(t *testing.T) {
	spy := newCorSpy()

	w := corPost(t, spy, "invoice_number", corBody("INV-77", "typed", ""))

	hndAssert(t, w, http.StatusUnprocessableEntity, hndErrBody(t, corMsgInvoiceNumber))
	corAssertUntouched(t, spy)
}

// The client-record fence: updateContentTx re-derives supplier_tin and supplier_name from the
// entity on every write and never reads the input, so accepting one would store the client
// record's value under a cell that claims the human typed it.
func TestCorrectionHandler_SupplierFieldsAreRefusedWithAReason(t *testing.T) {
	for _, name := range []string{"supplier_tin", "supplier_name"} {
		t.Run(name, func(t *testing.T) {
			spy := newCorSpy()

			w := corPost(t, spy, name, corBody("12345678-0001", "typed", ""))

			hndAssert(t, w, http.StatusUnprocessableEntity, hndErrBody(t, corMsgSupplierField))
			corAssertUntouched(t, spy)
		})
	}
}

// Ordering: the reason a caller reads must be the real one. A locked field with an unparseable
// body answers 422, not the 400 a body-first handler would give.
func TestCorrectionHandler_LockedFieldRefusalPrecedesTheBodyDecode(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{"invoice_number", corMsgInvoiceNumber},
		{"supplier_tin", corMsgSupplierField},
		{"supplier_name", corMsgSupplierField},
		{"not_a_field", corMsgUnknownField},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := newCorSpy()

			w := corPost(t, spy, tc.name, "this is not json at all")

			hndAssert(t, w, http.StatusUnprocessableEntity, hndErrBody(t, tc.want))
			corAssertUntouched(t, spy)
		})
	}
}
