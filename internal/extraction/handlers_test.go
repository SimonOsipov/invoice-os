// handlers_test.go: the nine-row status/body contract of GET /v1/extraction-jobs, driven with
// httptest and a spy list func. No database — this file must never call stRequire, the
// package's one sanctioned skip site, because scripts/ci/rls-test-gate.sh fails a step on any
// skip.
//
// Helpers use an hnd* prefix; rd st wk dc fx mx px pr ps pt pd pb pe rx de are taken.
package extraction_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- harness --------------------------------------------------------------------------------

const (
	hndSource      = "handlers.go"
	hndRoute       = "/v1/extraction-jobs"
	hndContentType = "application/json"

	// The four wire messages. db.NotActiveMemberMessage is deliberately absent: the 403 body is
	// asserted through the constant, never through a copy.
	hndMsgUnauthorized = "unauthorized"
	hndMsgRequired     = "document_id is required"
	hndMsgMalformed    = "document_id must be a well-formed uuid"
	hndMsgInternal     = "internal server error"

	hndDocumentID = "3f1a2b3c-4d5e-4f60-8a71-9b2c3d4e5f60"
	hndJobID      = "8c9d0e1f-2a3b-4c4d-9e5f-6a7b8c9d0e1f"
)

// hndIdentity is a fixed caller. These cases never reach a database, so it only has to exist.
var hndIdentity = auth.Identity{
	Subject:  "e5b10007-0000-4000-8000-000000000001",
	Role:     "authenticated",
	TenantID: "11111111-1111-1111-1111-111111111111",
}

// hndSpy is the injected reader. calls counts invocations, which is the only way to assert that
// a refusal was raised BEFORE the reader ran rather than merely that it returned the right code.
type hndSpy struct {
	got   string
	calls int
	resp  extraction.JobsResponse
	err   error
}

func (s *hndSpy) list(_ context.Context, documentID string) (extraction.JobsResponse, error) {
	s.got = documentID
	s.calls++
	return s.resp, s.err
}

// hndServe drives the handler once. A nil id means no identity in the context.
func hndServe(t *testing.T, spy *hndSpy, rawQuery string, id *auth.Identity, log *slog.Logger) *httptest.ResponseRecorder {
	t.Helper()
	target := hndRoute
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	w := httptest.NewRecorder()
	extraction.JobsHandler(spy.list, log)(w, r)
	return w
}

// hndGet is the authenticated request every case but the 401 uses.
func hndGet(t *testing.T, spy *hndSpy, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	return hndServe(t, spy, rawQuery, &hndIdentity, nil)
}

// hndValidQuery re-parses the fixture id, so a typo in it cannot silently route a case meant
// for a well-formed uuid through the malformed-uuid arm instead.
func hndValidQuery(t *testing.T) string {
	t.Helper()
	if _, err := uuid.Parse(hndDocumentID); err != nil {
		t.Fatalf("the fixture document id %q is not a well-formed uuid: %v", hndDocumentID, err)
	}
	return "document_id=" + hndDocumentID
}

// hndBody renders the exact bytes writeJSON produces: the marshalled value plus the newline
// json.NewEncoder.Encode appends. An assertion that omits the newline fails for the wrong
// reason and invites someone to weaken it.
func hndBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal want body: %v", err)
	}
	return string(b) + "\n"
}

func hndErrBody(t *testing.T, msg string) string {
	t.Helper()
	return hndBody(t, map[string]string{"error": msg})
}

func hndAssert(t *testing.T, w *httptest.ResponseRecorder, wantStatus int, wantBody string) {
	t.Helper()
	if w.Code != wantStatus {
		t.Errorf("status = %d, want %d (body=%q)", w.Code, wantStatus, w.Body.String())
	}
	if got := w.Body.String(); got != wantBody {
		t.Errorf("body = %q, want %q", got, wantBody)
	}
}

// hndLogger returns a JSON logger and the buffer it writes to.
func hndLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}

func hndLogLines(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// hndJob is one row on the wire. State comes from rdStates so this file names no state either.
func hndJob() extraction.JobState {
	return extraction.JobState{
		ID:         hndJobID,
		DocumentID: hndDocumentID,
		State:      rdStates[0],
		CreatedAt:  time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
	}
}

// --- row 1: identity is checked first ---------------------------------------------------------

// AC 3. The document_id is malformed too, so a handler that parsed first would answer 400 and
// leak that the parameter was read at all.
func TestExtractionJobsHandler_UnauthenticatedIs401BeforeParsing(t *testing.T) {
	spy := &hndSpy{}
	log, buf := hndLogger()

	w := hndServe(t, spy, "document_id=nope", nil, log)

	hndAssert(t, w, http.StatusUnauthorized, hndErrBody(t, hndMsgUnauthorized))
	if spy.calls != 0 {
		t.Errorf("the reader ran %d time(s) on an unauthenticated request, want 0", spy.calls)
	}
	if lines := hndLogLines(buf.String()); len(lines) != 0 {
		t.Errorf("a 401 refusal must not log as an error: %q", buf.String())
	}
}

// --- rows 2 and 3: the required parameter ------------------------------------------------------

// AC 2, first 400. Absent and empty collapse to ONE message. internal/audit/handlers.go:70-74
// rules EMPTY IS ABSENT for its OPTIONAL filters; document_id is this route's only REQUIRED
// parameter, so both mean the caller named no document (precedent
// internal/importer/handlers.go:231-235). The empty row exists so nobody restores the audit rule
// here, and so the == "" check cannot slip below uuid.Parse, which errors on "" too.
func TestExtractionJobsHandler_MissingDocumentIDIs400(t *testing.T) {
	cases := []struct {
		name     string
		rawQuery string
	}{
		{"absent", ""},
		{"present but empty", "document_id="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &hndSpy{}
			w := hndGet(t, spy, tc.rawQuery)

			hndAssert(t, w, http.StatusBadRequest, hndErrBody(t, hndMsgRequired))
			if spy.calls != 0 {
				t.Errorf("the reader ran %d time(s) on a request naming no document, want 0", spy.calls)
			}
		})
	}
}

// --- row 4: the malformed parameter ------------------------------------------------------------

// AC 2, second 400. The two 400 messages must stay distinct: uuid.Parse("") also errors, so a
// handler that parsed before checking for empty would answer this message on row 3.
func TestExtractionJobsHandler_MalformedDocumentIDIs400(t *testing.T) {
	if hndMsgRequired == hndMsgMalformed {
		t.Fatal("the two 400 messages are the same string, so the ordering claim below is vacuous")
	}

	spy := &hndSpy{}
	w := hndGet(t, spy, "document_id=not-a-uuid")

	hndAssert(t, w, http.StatusBadRequest, hndErrBody(t, hndMsgMalformed))
	if spy.calls != 0 {
		t.Errorf("the reader ran %d time(s) on a malformed document_id, want 0", spy.calls)
	}
}

// --- row 5: no tenant ---------------------------------------------------------------------------

// db.ErrNoTenant is 401, fail-closed. The reader HAS run here, which is what separates this row
// from row 1.
func TestExtractionJobsHandler_NoTenantIs401(t *testing.T) {
	spy := &hndSpy{err: db.ErrNoTenant}
	log, buf := hndLogger()

	w := hndServe(t, spy, hndValidQuery(t), &hndIdentity, log)

	hndAssert(t, w, http.StatusUnauthorized, hndErrBody(t, hndMsgUnauthorized))
	if spy.calls != 1 {
		t.Errorf("the reader ran %d time(s), want 1", spy.calls)
	}
	if lines := hndLogLines(buf.String()); len(lines) != 0 {
		t.Errorf("a 401 refusal must not log as an error: %q", buf.String())
	}
}

// --- row 6: a suspended member -------------------------------------------------------------------

// The 403 body is asserted through db.NotActiveMemberMessage. Retyping it here would let the
// wire message and the constant drift apart while this test stayed green.
func TestExtractionJobsHandler_NotActiveMemberIs403(t *testing.T) {
	spy := &hndSpy{err: db.ErrNotActiveMember}
	log, buf := hndLogger()

	w := hndServe(t, spy, hndValidQuery(t), &hndIdentity, log)

	hndAssert(t, w, http.StatusForbidden, hndErrBody(t, db.NotActiveMemberMessage))
	if spy.calls != 1 {
		t.Errorf("the reader ran %d time(s), want 1", spy.calls)
	}
	if lines := hndLogLines(buf.String()); len(lines) != 0 {
		t.Errorf("a 403 refusal must not log as an error: %q", buf.String())
	}
}

// --- row 7: an unknown error ----------------------------------------------------------------------

// AC 4, both halves: the internal is absent from the body AND present in exactly one ErrorContext
// line. Asserting only the absence would pass on a handler that logged nothing at all.
func TestExtractionJobsHandler_UnknownErrorIs500AndLogs(t *testing.T) {
	const internalText = "dial tcp 10.0.0.7:5432: connection refused"
	spy := &hndSpy{err: errors.New(internalText)}
	log, buf := hndLogger()

	w := hndServe(t, spy, hndValidQuery(t), &hndIdentity, log)

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
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("the log line is not JSON: %v (%q)", err, lines[0])
	}
	if rec["level"] != "ERROR" {
		t.Errorf("the 500 logged at level %v, want ERROR — ErrorContext is what an operator alerts on", rec["level"])
	}
	if !strings.Contains(lines[0], internalText) {
		t.Errorf("the 500 log line carries no internal error, so the operator has nothing to debug with: %q", lines[0])
	}
}

// --- row 8: zero rows ------------------------------------------------------------------------------

// AC 1. There is no existence oracle on this route: an unknown or foreign document is 200 with an
// empty array, never a 404, which would tell a caller whether a document exists in another tenant.
// The array is literal: a nil slice marshals to null and a caller looping over the result breaks.
func TestExtractionJobsHandler_EmptyResultIsTwoHundredAndAnEmptyArray(t *testing.T) {
	spy := &hndSpy{resp: extraction.JobsResponse{Jobs: []extraction.JobState{}}}

	w := hndGet(t, spy, hndValidQuery(t))

	const wantBody = "{\"jobs\":[]}\n"
	hndAssert(t, w, http.StatusOK, wantBody)
	if strings.Contains(w.Body.String(), "null") {
		t.Errorf("body = %q, want an empty array and never null", w.Body.String())
	}
	if spy.calls != 1 {
		t.Errorf("the reader ran %d time(s), want 1", spy.calls)
	}
}

// --- rows 8 and 9: the document id goes down and the rows come back up -------------------------------

// AC 1. The uuid the reader receives is the one in the querystring, unmodified, and the rows it
// returns reach the wire unchanged (row 9 of the status table).
func TestExtractionJobsHandler_PassesTheDocumentIDThrough(t *testing.T) {
	job := hndJob()
	spy := &hndSpy{resp: extraction.JobsResponse{Jobs: []extraction.JobState{job}}}

	w := hndGet(t, spy, hndValidQuery(t))

	hndAssert(t, w, http.StatusOK, hndBody(t, extraction.JobsResponse{Jobs: []extraction.JobState{job}}))
	if spy.calls != 1 {
		t.Fatalf("the reader ran %d time(s), want 1", spy.calls)
	}
	if spy.got != hndDocumentID {
		t.Errorf("the reader received document_id %q, want %q", spy.got, hndDocumentID)
	}
}

// --- every arm sets the content type -------------------------------------------------------------------

// writeJSON sets the header before WriteHeader, so an error arm that bypassed it would ship a
// JSON body under text/plain and a strict client would refuse to parse it.
//
// Each arm asserts its status as well. Without that, a handler answering every request from one
// code path would pass this table nine times over while proving the header on one arm only.
func TestExtractionJobsHandler_SetsJSONContentType(t *testing.T) {
	valid := hndValidQuery(t)
	job := hndJob()

	cases := []struct {
		name       string
		anon       bool
		query      string
		spy        *hndSpy
		wantStatus int
	}{
		{"no identity", true, "document_id=nope", &hndSpy{}, http.StatusUnauthorized},
		{"document_id absent", false, "", &hndSpy{}, http.StatusBadRequest},
		{"document_id empty", false, "document_id=", &hndSpy{}, http.StatusBadRequest},
		{"document_id malformed", false, "document_id=not-a-uuid", &hndSpy{}, http.StatusBadRequest},
		{"no tenant", false, valid, &hndSpy{err: db.ErrNoTenant}, http.StatusUnauthorized},
		{"not active member", false, valid, &hndSpy{err: db.ErrNotActiveMember}, http.StatusForbidden},
		{"unknown error", false, valid, &hndSpy{err: errors.New("boom")}, http.StatusInternalServerError},
		{"success, zero rows", false, valid, &hndSpy{resp: extraction.JobsResponse{Jobs: []extraction.JobState{}}}, http.StatusOK},
		{"success, one row", false, valid, &hndSpy{resp: extraction.JobsResponse{Jobs: []extraction.JobState{job}}}, http.StatusOK},
	}
	if len(cases) != 9 {
		t.Fatalf("the table holds %d arm(s), want the 9 rows of the status table", len(cases))
	}
	seen := map[int]bool{}
	for _, tc := range cases {
		seen[tc.wantStatus] = true
	}
	if len(seen) != 5 {
		t.Fatalf("the table expects %d distinct status code(s), want 5 (200, 400, 401, 403, 500)", len(seen))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := &hndIdentity
			if tc.anon {
				id = nil
			}
			w := hndServe(t, tc.spy, tc.query, id, nil)
			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body=%q)", w.Code, tc.wantStatus, w.Body.String())
			}
			if ct := w.Header().Get("Content-Type"); ct != hndContentType {
				t.Errorf("Content-Type = %q, want %q (status %d, body %q)", ct, hndContentType, w.Code, w.Body.String())
			}
		})
	}
}

// --- structural guards: green against the stub -----------------------------------------------------------

// hndDerivedKeys are the field names a progress bar invites and the schema cannot support. A
// percentage or a confidence has no column behind it, so any handler that shipped one would be
// inventing the number.
var hndDerivedKeys = []string{"confidence", "percent", "progress"}

// AC 5. GREEN against the stub — a drift pin, not a red test. It fires the moment a handler pins
// a state name or invents a derived field.
//
// Three floors, because any empty set would make the absence meaningless: both needle lists must
// be non-empty, and handlers.go must hold at least one string literal to examine. A scan that
// stopped matching reports zero hits, which reads exactly like a clean file.
func TestExtractionHandlers_NamesNoStateLiteral(t *testing.T) {
	if len(rdStates) == 0 {
		t.Fatal("the state fixture list is empty, so the scan below examined nothing")
	}
	if len(hndDerivedKeys) == 0 {
		t.Fatal("the derived-key needle list is empty, so the scan below examined nothing")
	}

	f, fset := mxParse(t, hndSource)

	var lits int
	ast.Inspect(f, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		lits++
		unq, err := strconv.Unquote(bl.Value)
		if err != nil {
			t.Fatalf("%s: cannot unquote %s: %v", fset.Position(bl.Pos()), bl.Value, err)
		}
		lower := strings.ToLower(unq)
		for _, state := range rdStates {
			if unq == state {
				t.Errorf("%s: %s names the state %q; the column value passes through untouched",
					fset.Position(bl.Pos()), hndSource, state)
			}
			// The SQL-quoted form catches a state pinned inside a larger statement, which a
			// whole-literal comparison alone would miss.
			if strings.Contains(unq, "'"+state+"'") {
				t.Errorf("%s: %s pins the state %q inside a SQL literal", fset.Position(bl.Pos()), hndSource, state)
			}
		}
		for _, key := range hndDerivedKeys {
			if strings.Contains(lower, key) {
				t.Errorf("%s: %s names %q; nothing in extraction_jobs backs a derived number",
					fset.Position(bl.Pos()), hndSource, key)
			}
		}
		return true
	})
	if lits == 0 {
		t.Fatalf("%s holds no string literals, so this scan examined nothing", hndSource)
	}
}

// AC 6, a verify-not-edit. GREEN from the start — it reads internal/gateway/cors.go and is
// independent of the stub. A NEW http method would need a corsAllowMethods edit that no other Go
// or e2e test can see; GET needs none, and this case is what makes that claim checkable.
// Mirrors internal/audit/handlers_test.go:452-483, both of its guards included.
func TestExtractionHandlers_CorsAllowMethodsAlreadyNamesGET(t *testing.T) {
	methods := hndCorsAllowMethods(t)
	if strings.TrimSpace(methods) == "" {
		t.Fatalf("corsAllowMethods read as empty; the extraction is broken and the check below " +
			"would pass vacuously")
	}
	if !strings.Contains(methods, "GET") {
		t.Errorf("corsAllowMethods = %q, want it to already contain GET", methods)
	}
}

// hndCorsAllowMethodsRE extracts the constant quoted value from the gateway source.
// internal/gateway keeps it unexported, and importing that package here would breach the import
// fence deps_test.go guards, so the file is read instead.
var hndCorsAllowMethodsRE = regexp.MustCompile(`corsAllowMethods\s*=\s*"([^"]*)"`)

func hndCorsAllowMethods(t *testing.T) string {
	t.Helper()
	const path = "../gateway/cors.go"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m := hndCorsAllowMethodsRE.FindSubmatch(raw)
	if m == nil {
		t.Fatalf("no corsAllowMethods constant in %s; the extraction lost its anchor", path)
	}
	return string(m[1])
}
