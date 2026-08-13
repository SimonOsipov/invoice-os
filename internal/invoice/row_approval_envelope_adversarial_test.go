// task-499 (APPR-08-08) QA: the adversarial half of the per-row `approval` envelope.
// row_approval_envelope_test.go pins the acceptance criteria; these pin the failure
// modes those specs leave reachable -- cross-row pointer aliasing, an unlogged 500, a
// seam answer that mentions ids the page does not carry, and the structural guarantee
// the SPA's untouched `run_state` pass-through rests on.
package invoice

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// rowFactsAt decodes one row's `approval` object, failing if the key is absent or null.
func rowFactsAt(t *testing.T, rows []map[string]json.RawMessage, i int) approval.RowFacts {
	t.Helper()
	raw, ok := rows[i]["approval"]
	if !ok {
		t.Fatalf("row %d has no \"approval\" key", i)
	}
	if string(raw) == "null" {
		t.Fatalf("row %d approval is null, want a populated object", i)
	}
	var got approval.RowFacts
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode row %d approval %q: %v", i, string(raw), err)
	}
	return got
}

// TestListHandler_EachRowCarriesItsOwnFacts: three rows, three DISTINCT answers. The
// shipped TestListHandler_RowCarriesApprovalOrNull arms one row and leaves the other
// bare, so a single shared &f across the loop -- every armed row pointing at the LAST
// one's facts -- passes it. Two armed rows is the smallest fixture that separates them,
// and three catches a bug that only mixes neighbours.
//
// The handler is correct today because `f` is declared in the `if` statement's own
// init, which is a fresh variable per iteration; this asserts that rather than
// trusting it, because the fix for a future `var f approval.RowFacts` hoist would be
// invisible on the wire of any one-armed-row page.
func TestListHandler_EachRowCarriesItsOwnFacts(t *testing.T) {
	id := listIdentity()
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}

	// Every field differs per row, so an alias shows up whichever one it copies.
	want := map[string]approval.RowFacts{}
	for i, invID := range ids {
		ord := i
		title := []string{"Finance Lead", "Controller", "CFO"}[i]
		due := []string{"2026-08-18T09:00:00Z", "2026-08-19T09:00:00Z", "2026-08-20T09:00:00Z"}[i]
		when, err := time.Parse(time.RFC3339, due)
		if err != nil {
			t.Fatalf("parse fixture due_at: %v", err)
		}
		want[invID] = approval.RowFacts{
			RunState:          []string{"open", "approved", "rejected"}[i],
			PendingOrd:        &ord,
			PendingRoleTitle:  &title,
			PendingHolderWarn: i == 1,
			DueAt:             &when,
			Overdue:           i == 2,
		}
	}

	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{
			{ID: ids[0], Status: StatusValidated},
			{ID: ids[1], Status: StatusValidated},
			{ID: ids[2], Status: StatusValidated},
		}, 3, nil
	}
	facts := func(ctx context.Context, gotIDs []string) (map[string]approval.RowFacts, error) {
		return want, nil
	}

	rec := doInvoiceListWithFacts(t, list, facts, &id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	rows := listRowsRaw(t, rec)
	if len(rows) != 3 {
		t.Fatalf("len(invoices) = %d, want 3", len(rows))
	}
	for i, invID := range ids {
		got := rowFactsAt(t, rows, i)
		if !reflect.DeepEqual(got, want[invID]) {
			t.Errorf("row %d (%s) approval = %+v, want %+v -- every row must carry its OWN facts, never a neighbour's", i, invID, got, want[invID])
		}
	}
}

// TestListHandler_FactsLandOnTheirOwnRowOnly: only the MIDDLE row is armed. A handler
// that paired facts to rows by position rather than by id would arm row 0 instead, and
// every shipped spec whose armed row is first would still pass.
func TestListHandler_FactsLandOnTheirOwnRowOnly(t *testing.T) {
	id := listIdentity()
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{
			{ID: ids[0], Status: StatusDraft},
			{ID: ids[1], Status: StatusValidated},
			{ID: ids[2], Status: StatusAccepted},
		}, 3, nil
	}
	facts := func(ctx context.Context, gotIDs []string) (map[string]approval.RowFacts, error) {
		return map[string]approval.RowFacts{ids[1]: armedRowFacts()}, nil
	}

	rec := doInvoiceListWithFacts(t, list, facts, &id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	rows := listRowsRaw(t, rec)
	if len(rows) != 3 {
		t.Fatalf("len(invoices) = %d, want 3", len(rows))
	}
	if got := rowFactsAt(t, rows, 1); !reflect.DeepEqual(got, armedRowFacts()) {
		t.Errorf("row 1 approval = %+v, want %+v", got, armedRowFacts())
	}
	for _, i := range []int{0, 2} {
		if string(rows[i]["approval"]) != "null" {
			t.Errorf("row %d approval = %s, want null -- the seam did not mention it", i, rows[i]["approval"])
		}
	}
}

// TestListHandler_FactsForAnAbsentIdAreIgnored: the seam answers for an id the page
// does not carry -- reachable whenever the page and the read race a concurrent write.
// The extra entry must be dropped silently: no 500, and none of its values may attach
// themselves to a row that did not ask for them.
func TestListHandler_FactsForAnAbsentIdAreIgnored(t *testing.T) {
	id := listIdentity()
	onPage := uuid.NewString()
	ghost := uuid.NewString()
	ghostTitle := "GHOST-ROLE-MUST-NOT-REACH-THE-WIRE"

	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{{ID: onPage, Status: StatusValidated}}, 1, nil
	}
	facts := func(ctx context.Context, gotIDs []string) (map[string]approval.RowFacts, error) {
		ord := 7
		return map[string]approval.RowFacts{
			ghost: {RunState: "open", PendingOrd: &ord, PendingRoleTitle: &ghostTitle},
		}, nil
	}

	rec := doInvoiceListWithFacts(t, list, facts, &id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- an id the page lost is not an error (body=%s)", rec.Code, rec.Body.String())
	}
	rows := listRowsRaw(t, rec)
	if len(rows) != 1 {
		t.Fatalf("len(invoices) = %d, want 1 -- the seam's map must never add rows", len(rows))
	}
	if string(rows[0]["approval"]) != "null" {
		t.Errorf("row 0 approval = %s, want null -- the only fact answered was for another invoice", rows[0]["approval"])
	}
	for _, leak := range []string{ghost, ghostTitle} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("body leaks %q, which belongs to an invoice that is not on this page: %s", leak, rec.Body.String())
		}
	}
}

// TestListHandler_RowFactsOneCallOnALargePage: the same one-call contract as the
// shipped 50-row spec, at the route's real ceiling (limit clamps to 200,
// handlers.go). A full page is where a per-row loop costs the most.
func TestListHandler_RowFactsOneCallOnALargePage(t *testing.T) {
	id := listIdentity()
	const n = 200
	want := make([]string, 0, n)
	items := make([]Invoice, 0, n)
	for i := 0; i < n; i++ {
		invID := uuid.NewString()
		want = append(want, invID)
		items = append(items, Invoice{ID: invID, Status: StatusValidated})
	}
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return items, n, nil
	}
	calls := 0
	var seen []string
	facts := func(ctx context.Context, gotIDs []string) (map[string]approval.RowFacts, error) {
		calls++
		seen = append([]string(nil), gotIDs...)
		return map[string]approval.RowFacts{}, nil
	}

	rec := doInvoiceListWithFacts(t, list, facts, &id, "?limit=200")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("rowFacts was called %d times for a %d-row page, want exactly 1", calls, n)
	}
	if !reflect.DeepEqual(seen, want) {
		t.Errorf("rowFacts received %d ids, want all %d of the page's, in page order", len(seen), n)
	}
	if got := len(listRowsRaw(t, rec)); got != n {
		t.Errorf("len(invoices) = %d, want %d", got, n)
	}
}

// TestListHandler_RowFactsErrorIsLogged: the LOGGED half of AC-4. The shipped
// TestListHandler_RowFactsErrorIs500 asserts the status and the body, both of which
// survive deleting the log line -- a 500 whose cause is nowhere in the operator's log
// is the failure this closes. Follows the injected-buffer idiom already used for
// GetHandler's QR render failure (handlers_test.go) and UBLHandler (ubl_adversarial_test.go).
func TestListHandler_RowFactsErrorIsLogged(t *testing.T) {
	id := listIdentity()
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{{ID: uuid.NewString(), Status: StatusValidated}}, 1, nil
	}
	facts := func(ctx context.Context, ids []string) (map[string]approval.RowFacts, error) {
		return nil, context.DeadlineExceeded
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	r := httptest.NewRequest("GET", "/v1/invoices", nil)
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	ListHandler(list, facts, logger).ServeHTTP(rec, r)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body=%s)", rec.Code, rec.Body.String())
	}
	if buf.Len() == 0 {
		t.Fatal("the row-facts read failed and nothing was written to the injected logger -- a 500 with no logged cause is undiagnosable")
	}
	if !strings.Contains(buf.String(), "invoice: list row facts") {
		t.Errorf("log = %s, want the \"invoice: list row facts\" message -- distinct from \"invoice: list\", so the two 500s stay tellable apart", buf.String())
	}
	if !strings.Contains(buf.String(), "context deadline exceeded") {
		t.Errorf("log = %s, want the underlying error attached -- the message alone does not say what failed", buf.String())
	}
}

// TestListHandler_NonNullApprovalAlwaysCarriesRunState: the premise the SPA's
// normaliseInvoiceRow relies on. It passes `run_state` through UNTOUCHED -- no
// `?? ”`, because a fallback here would be the SPA-authored copy [gates-on-the-wire]
// forbids -- which is only sound because a non-null `approval` structurally cannot omit
// the key: RowFacts.RunState is a plain string with no omitempty
// (TestRowFacts_JSONTagsCarryNoOmitempty, package approval, pins the tag; this pins it
// through THIS handler's wrapper).
//
// The zero RowFacts below is the emptiest answer the seam can give; `run_state` must
// still be emitted, as "". That empty string is NOT a backend state -- approval_runs.state
// is NOT NULL and RowFactsTx only maps ids that have a run -- it is the marshaller's
// zero, used here because it is the exact value an omitempty would swallow.
func TestListHandler_NonNullApprovalAlwaysCarriesRunState(t *testing.T) {
	id := listIdentity()
	invID := uuid.NewString()
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{{ID: invID, Status: StatusValidated}}, 1, nil
	}
	facts := func(ctx context.Context, ids []string) (map[string]approval.RowFacts, error) {
		return map[string]approval.RowFacts{invID: {}}, nil
	}

	rec := doInvoiceListWithFacts(t, list, facts, &id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	rows := listRowsRaw(t, rec)
	raw, ok := rows[0]["approval"]
	if !ok || string(raw) == "null" {
		t.Fatalf("row 0 approval = %s, want an object -- an id present in the seam's map is armed", raw)
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("decode approval %q: %v", string(raw), err)
	}
	// All six keys, every time: the SPA reads each of them off a non-null approval.
	for _, k := range []string{"run_state", "pending_ord", "pending_role_title", "pending_holder_warn", "due_at", "overdue"} {
		if _, ok := obj[k]; !ok {
			t.Errorf("approval has no %q key: %s -- the SPA reads it off every non-null approval", k, raw)
		}
	}
	if got := string(obj["run_state"]); got != `""` {
		t.Errorf("run_state = %s, want the explicit \"\" -- an omitempty would drop it and the SPA would read undefined through a `string`", got)
	}
}
