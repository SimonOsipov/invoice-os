// handlers_detail_test.go: the status/body contract of GET /v1/extractions/{id}, driven with
// httptest and a spy Detail func. No database -- this file must never call stRequire, the
// package's one sanctioned skip site, because scripts/ci/rls-test-gate.sh fails a step on any
// skip. Shares handlers_test.go's hnd* harness: the two routes answer with the same envelope,
// and a second copy of it could drift.
//
// Helpers use a dtl* prefix; hnd up rd st wk dc fx mx px pr ps pt pd pb pe rx de rp are taken.
package extraction_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- harness --------------------------------------------------------------------------------

const (
	// The wildcard name in "GET /v1/extractions/{id}". The mux binds it in production.
	dtlPathKey = "id"

	// The two wire messages this route owns. hndMsgUnauthorized, hndMsgInternal and
	// db.NotActiveMemberMessage carry the rest; retyping any of them here would let the wire
	// and the constant drift apart while this file stayed green.
	dtlMsgMalformed = "id must be a well-formed uuid"
	dtlMsgNotFound  = "not found"
)

// dtlSpy is the injected reader seam. calls counts invocations, which is the only way to assert
// that a refusal was raised BEFORE the reader ran rather than merely that it returned the right
// code.
type dtlSpy struct {
	got   string
	calls int
	resp  extraction.ExtractionDetail
	err   error
}

func (s *dtlSpy) detail(_ context.Context, jobID string) (extraction.ExtractionDetail, error) {
	s.got = jobID
	s.calls++
	return s.resp, s.err
}

// dtlDetail is the 200 body. Pages and Fields are literal empty slices: a nil slice marshals to
// JSON null and every consumer loops over them. State comes from rdStates so this file names no
// stage either.
func dtlDetail() extraction.ExtractionDetail {
	return extraction.ExtractionDetail{
		ID:         hndJobID,
		DocumentID: hndDocumentID,
		State:      rdStates[0],
		Document: extraction.ExtractionDocument{
			SizeBytes: 151552,
			StoredAt:  "2026-08-30T10:42:07Z",
		},
		Pages:  []extraction.ExtractionPage{},
		Fields: []extraction.ExtractionFieldState{},
	}
}

// dtlServe drives the handler once. rawID is what the mux would have bound to {id}; a nil
// identity means the request carries none.
func dtlServe(t *testing.T, spy *dtlSpy, rawID string, id *auth.Identity, log *slog.Logger) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/extractions/"+url.PathEscape(rawID), nil)
	r.SetPathValue(dtlPathKey, rawID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	w := httptest.NewRecorder()
	extraction.DetailHandler(spy.detail, log)(w, r)
	return w
}

// dtlGet is the authenticated request every case but the 401 uses.
func dtlGet(t *testing.T, spy *dtlSpy, rawID string) *httptest.ResponseRecorder {
	t.Helper()
	return dtlServe(t, spy, rawID, &hndIdentity, nil)
}

// --- AC 4: identity is checked first ------------------------------------------------------

// The path value is malformed too, so a handler that parsed first would answer 400 and leak
// that the wildcard was read at all.
func TestExtractionDetailHandler_UnauthenticatedIs401BeforeParsing(t *testing.T) {
	spy := &dtlSpy{}
	log, buf := hndLogger()

	w := dtlServe(t, spy, "nope", nil, log)

	hndAssert(t, w, http.StatusUnauthorized, hndErrBody(t, hndMsgUnauthorized))
	if spy.calls != 0 {
		t.Errorf("the reader ran %d time(s) on an unauthenticated request, want 0", spy.calls)
	}
	if lines := hndLogLines(buf.String()); len(lines) != 0 {
		t.Errorf("a 401 refusal must not log as an error: %q", buf.String())
	}
}

// --- AC 5: the malformed path value ---------------------------------------------------------

// The body is exact. The message differs from the collection route's on purpose: the two routes
// name different parameters, and one message for both would tell a caller the wrong field.
func TestExtractionDetailHandler_MalformedIdIs400(t *testing.T) {
	if dtlMsgMalformed == hndMsgMalformed {
		t.Fatal("the detail and collection 400 messages are the same string; one of them names the wrong parameter")
	}

	cases := []struct {
		name  string
		rawID string
	}{
		{"plain text", "not-a-uuid"},
		{"empty", ""},
		{"too short", "3f1a2b3c"},
	}
	if len(cases) == 0 {
		t.Fatal("the case table is empty, so this test examined nothing")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &dtlSpy{}
			log, buf := hndLogger()

			w := dtlServe(t, spy, tc.rawID, &hndIdentity, log)

			hndAssert(t, w, http.StatusBadRequest, hndErrBody(t, dtlMsgMalformed))
			if spy.calls != 0 {
				t.Errorf("the reader ran %d time(s) on a malformed id, want 0", spy.calls)
			}
			if lines := hndLogLines(buf.String()); len(lines) != 0 {
				t.Errorf("a 400 refusal must not log as an error: %q", buf.String())
			}
		})
	}
}

// "urn:uuid:<id>" is the one spelling uuid.Parse accepts and Postgres refuses (SQLSTATE 22P02),
// so forwarding the raw path value turns a caller-supplied string into a 500. The spy has no
// Postgres parser behind it, so the exact bytes the reader receives ARE the oracle: assert the
// canonical form, not the parsed value.
func TestExtractionDetailHandler_UrnPrefixedUuidReachesTheReaderCanonicalised(t *testing.T) {
	want := uuid.MustParse(hndJobID).String()
	if want != hndJobID {
		t.Fatalf("the fixture id %q is not its own canonical form %q; this case asserts the wrong string", hndJobID, want)
	}
	value := "urn:uuid:" + hndJobID
	if value == want {
		t.Fatal("the urn value equals the canonical form, so this case proves nothing")
	}

	spy := &dtlSpy{resp: dtlDetail()}
	w := dtlGet(t, spy, value)

	// The 200 body is asserted here rather than in a case of its own: this is the one path that
	// reaches the reader and returns its value.
	hndAssert(t, w, http.StatusOK, hndBody(t, dtlDetail()))
	if spy.calls != 1 {
		t.Fatalf("the reader ran %d time(s), want 1", spy.calls)
	}
	if spy.got != want {
		t.Errorf("the reader received id %q, want the canonical %q; Postgres refuses anything else", spy.got, want)
	}
}

// --- AC 6: absent or another tenant's -------------------------------------------------------

// statusForErr's default arm answers 500 for every error that is not one of its sentinels, so
// without the new branch an absent job is an operator-alerting 500 on a route the screen opens
// on every visit. The wrapped case is not decoration: EXTR-11-04 reuses ErrNotFound for a
// missing page and may wrap it, and an err == comparison would silently stop matching.
func TestExtractionDetailHandler_NotFoundIs404(t *testing.T) {
	const wrapText = "extraction: read job: "
	cases := []struct {
		name string
		err  error
	}{
		{"bare sentinel", extraction.ErrNotFound},
		{"wrapped sentinel", fmt.Errorf("%s%w", wrapText, extraction.ErrNotFound)},
	}
	if len(cases) == 0 {
		t.Fatal("the case table is empty, so this test examined nothing")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &dtlSpy{err: tc.err}
			log, buf := hndLogger()

			w := dtlServe(t, spy, hndJobID, &hndIdentity, log)

			hndAssert(t, w, http.StatusNotFound, hndErrBody(t, dtlMsgNotFound))
			if spy.calls != 1 {
				t.Errorf("the reader ran %d time(s), want 1", spy.calls)
			}
			if strings.Contains(w.Body.String(), wrapText) {
				t.Errorf("the 404 body carries the reader's own text: %q", w.Body.String())
			}
			if lines := hndLogLines(buf.String()); len(lines) != 0 {
				t.Errorf("a 404 refusal must not log as an error: %q", buf.String())
			}
		})
	}
}

// --- AC 7: a suspended member ---------------------------------------------------------------

// The 403 body is asserted through db.NotActiveMemberMessage. Retyping it here would let the
// wire message and the constant drift apart while this test stayed green.
func TestExtractionDetailHandler_NotActiveMemberIs403(t *testing.T) {
	spy := &dtlSpy{err: db.ErrNotActiveMember}
	log, buf := hndLogger()

	w := dtlServe(t, spy, hndJobID, &hndIdentity, log)

	hndAssert(t, w, http.StatusForbidden, hndErrBody(t, db.NotActiveMemberMessage))
	if spy.calls != 1 {
		t.Errorf("the reader ran %d time(s), want 1", spy.calls)
	}
	if lines := hndLogLines(buf.String()); len(lines) != 0 {
		t.Errorf("a 403 refusal must not log as an error: %q", buf.String())
	}
}

// --- AC 9: an unknown error -----------------------------------------------------------------

// Both halves: the internal is absent from the body AND present in exactly one ErrorContext
// line. Asserting only the absence would pass on a handler that logged nothing at all.
func TestExtractionDetailHandler_InternalErrorLeaksNothing(t *testing.T) {
	const internalText = "dial tcp 10.0.0.7:5432: connection refused"
	spy := &dtlSpy{err: errors.New(internalText)}
	log, buf := hndLogger()

	w := dtlServe(t, spy, hndJobID, &hndIdentity, log)

	hndAssert(t, w, http.StatusInternalServerError, hndErrBody(t, hndMsgInternal))
	if strings.Contains(w.Body.String(), internalText) {
		t.Errorf("the 500 body leaks the internal error: %q", w.Body.String())
	}
	if spy.calls != 1 {
		t.Errorf("the reader ran %d time(s), want 1", spy.calls)
	}

	lines := hndLogLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("the 500 emitted %d log line(s), want exactly 1: %q", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], internalText) {
		t.Errorf("the 500 log line carries no internal error, so the operator has nothing to debug with: %q", lines[0])
	}
	if !strings.Contains(lines[0], `"level":"ERROR"`) {
		t.Errorf("the 500 did not log at level ERROR, which is what an operator alerts on: %q", lines[0])
	}
}
