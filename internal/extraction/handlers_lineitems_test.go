// handlers_lineitems_test.go: the status/body/ordering contract of
// POST /v1/extractions/{id}/line-items, driven with httptest and injected seams -- no database.
// This file must never call stRequire, the package's one sanctioned skip site. The DB-backed
// half lives in handlers_lineitems_db_test.go.
//
// Helpers use a lix* prefix; hnd dtl up rd st wk dc fx mx px pr ps pt pd pb pe rx de rp eq cs
// rvd cor cx rda li are taken.
package extraction_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// lixMsgInvalidBody mirrors handlers.go's unexported msgInvalidBody: retyping it here would let
// the wire and the constant drift apart while this file stayed green.
const (
	lixMsgInvalidBody  = "invalid request body"
	lixMsgNoLinesKey   = "lines is required"
	lixMsgTooManyLines = "lines must not exceed 999"
)

// lixSpy is the injected pair, mirroring corSpy for the line-items seam. Counting invocations is
// the only way to prove a refusal was raised BEFORE either seam ran.
type lixSpy struct {
	applies int
	records int
}

func (s *lixSpy) apply(context.Context, pgx.Tx, string, []extraction.LineItemInput) (string, error) {
	s.applies++
	return "5d2f7a10-6b3c-4e8d-9f01-2a3b4c5d6e7f", nil
}

func (s *lixSpy) record(context.Context, pgx.Tx, string, extraction.FieldCorrection) error {
	s.records++
	return nil
}

func lixAssertUntouched(t *testing.T, spy *lixSpy) {
	t.Helper()
	if spy.applies != 0 {
		t.Errorf("the invoice seam ran %d time(s) on a refused request, want 0 -- a refusal that still writes is worse than none", spy.applies)
	}
	if spy.records != 0 {
		t.Errorf("the audit seam ran %d time(s) on a refused request, want 0", spy.records)
	}
}

// lixServe drives the handler once. A nil identity means the request carries none.
func lixServe(t *testing.T, spy *lixSpy, rawID, body string, id *auth.Identity) *httptest.ResponseRecorder {
	t.Helper()
	target := "/v1/extractions/" + rawID + "/line-items"
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	r.SetPathValue(corPathID, rawID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	w := httptest.NewRecorder()
	extraction.LineItemsHandler(corDeadPool, spy.apply, spy.record, nil)(w, r)
	return w
}

// lixPost is the authenticated request every case but the 401 uses.
func lixPost(t *testing.T, spy *lixSpy, body string) *httptest.ResponseRecorder {
	t.Helper()
	return lixServe(t, spy, hndJobID, body, &hndIdentity)
}

// lixLinesBody builds a well-formed body of n lines, each with a distinct description -- so a
// case that reorders or truncates the set has something to disagree about.
func lixLinesBody(n int) string {
	var b strings.Builder
	b.WriteString(`{"lines":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"description":"line ` + strconv.Itoa(i) + `","quantity":"1","unit_price":"1.00","line_total":"1.00"}`)
	}
	b.WriteString(`]}`)
	return b.String()
}

// --- identity, then the path, then the body -------------------------------------------------

// No identity, a malformed job id AND an unparseable body all at once: a handler that read
// either before checking identity would answer 400 and leak that it looked.
func TestLineItemsHandler_UnauthenticatedIs401BeforeParsing(t *testing.T) {
	spy := &lixSpy{}

	w := lixServe(t, spy, "not-a-uuid", "{", nil)

	hndAssert(t, w, http.StatusUnauthorized, hndErrBody(t, hndMsgUnauthorized))
	lixAssertUntouched(t, spy)
}

// The correction and detail routes' message: all three bind the same {id}.
func TestLineItemsHandler_MalformedJobIdIs400(t *testing.T) {
	spy := &lixSpy{}

	w := lixServe(t, spy, "not-a-uuid", lixLinesBody(1), &hndIdentity)

	hndAssert(t, w, http.StatusBadRequest, hndErrBody(t, corMsgMalformedID))
	lixAssertUntouched(t, spy)
}

// --- 400: malformed input ---------------------------------------------------------------

func TestLineItemsHandler_InvalidBodyIs400(t *testing.T) {
	spy := &lixSpy{}

	w := lixPost(t, spy, "this is not json at all")

	hndAssert(t, w, http.StatusBadRequest, hndErrBody(t, lixMsgInvalidBody))
	lixAssertUntouched(t, spy)
}

// A nil slice and an empty array mean different things to a replace-all route, and only the
// second is a legitimate "remove every line" -- so an absent (or explicitly null) lines key is
// refused, while lines: [] is not.
func TestLineItemsHandler_AbsentLinesKeyIs400(t *testing.T) {
	for _, body := range []string{`{}`, `{"lines":null}`} {
		t.Run(body, func(t *testing.T) {
			spy := &lixSpy{}

			w := lixPost(t, spy, body)

			hndAssert(t, w, http.StatusBadRequest, hndErrBody(t, lixMsgNoLinesKey))
			lixAssertUntouched(t, spy)
		})
	}

	// The control: lines: [] is not refused for this reason, so the arms above are refusing
	// absence rather than refusing every request. It reaches corDeadPool and dies there, which
	// is why only the status is checked, not the body.
	spy := &lixSpy{}
	w := lixPost(t, spy, `{"lines":[]}`)
	if w.Code == http.StatusBadRequest && w.Body.String() == hndErrBody(t, lixMsgNoLinesKey) {
		t.Errorf("lines: [] was refused with %q; the arms above then prove nothing about absence", w.Body.String())
	}
}

// The 999 cap is a stated policy guard on an unbounded body, not a derived bound -- the boundary
// itself is proved not to be off by one in handlers_lineitems_db_test.go, where 999 lines can
// actually reach the invoice seam.
func TestLineItemsHandler_RefusesMoreThan999Lines(t *testing.T) {
	spy := &lixSpy{}

	w := lixPost(t, spy, lixLinesBody(1000))

	hndAssert(t, w, http.StatusBadRequest, hndErrBody(t, lixMsgTooManyLines))
	lixAssertUntouched(t, spy)

	// The control: exactly 999 lines is not refused with this message, so the case above is
	// refusing the boundary rather than refusing every large request.
	spy2 := &lixSpy{}
	w2 := lixPost(t, spy2, lixLinesBody(999))
	if w2.Code == http.StatusBadRequest && w2.Body.String() == hndErrBody(t, lixMsgTooManyLines) {
		t.Errorf("999 lines was refused with %q; the case above then proves nothing about the boundary", w2.Body.String())
	}
}
