// task-499 (APPR-08-08): the per-row `approval` envelope on GET /v1/invoices --
// ListHandler's second seam, the listItem wrapper it marshals through, and the
// 500-not-a-degraded-200 error contract.
//
// Every assertion below reads RAW bytes rather than a decoded struct: presence,
// absence and explicit null are three different answers here, and a decode collapses
// the first two.
//
// Spec-to-test map (task-499 Test Specs table):
//
//	AC-1  TestListItem_InvoiceKeysUnmovedAndUnrenamed
//	AC-1  TestListHandler_RowCarriesApprovalOrNull
//	AC-1  TestListHandler_ApprovalKeyPresentOnEveryRow
//	AC-3  TestListHandler_EnvelopeStillExactlyTwoKeys
//	AC-4  TestListHandler_RowFactsErrorIs500
//	AC-4  TestListHandler_RowFactsCalledOncePerRequest
//	AC-4  TestListHandler_ApprovalKeyedOnTheStoreReturnedRowID
//	AC-5  TestListHandler_RowFactsNotCalledOnAnEmptyPage
//
// AC-6 (the flag does not gate the row facts) is DB-backed and lives in
// row_facts_store_test.go. The two shipped guards this subtask must not break --
// TestListHandler_EmptyState (handlers_test.go) and TestListHandler_NoActionFlagKeys
// (handlers_test.go) -- stay where they are, unwidened.
package invoice

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- helpers ----------------------------------------------------------------

// doInvoiceListWithFacts is doInvoiceList's sibling for the specs that drive the
// rowFacts seam itself, rather than the shared emptyRowFactsStub. It returns the
// recorder only: every assertion below reads RAW bytes, because listInvoicesResponse
// decodes into []invoiceBody and would silently swallow the very key under test.
func doInvoiceListWithFacts(
	t *testing.T,
	list func(ctx context.Context, f ListFilter) ([]Invoice, int, error),
	rowFacts func(ctx context.Context, ids []string) (map[string]approval.RowFacts, ListGateFacts, error),
	id *auth.Identity,
	query string,
) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/invoices"+query, nil)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	ListHandler(list, rowFacts, nil).ServeHTTP(rec, r)
	return rec
}

// listRowsRaw pulls the response's rows out UNDECODED, so a key's presence,
// absence and explicit null stay distinguishable.
func listRowsRaw(t *testing.T, rec *httptest.ResponseRecorder) []map[string]json.RawMessage {
	t.Helper()
	var env map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", rec.Body.String(), err)
	}
	raw, ok := env["invoices"]
	if !ok {
		t.Fatalf("envelope has no \"invoices\" key: %s", rec.Body.String())
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("decode invoices %q: %v", string(raw), err)
	}
	return rows
}

// jsonKeysInOrder returns an object's keys in WIRE order. json.Unmarshal into a map
// loses that order, and order is half of what AC-1 pins ("approval is last").
func jsonKeysInOrder(t *testing.T, b []byte) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(string(b)))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		t.Fatalf("expected a JSON object, got %v (err %v): %s", tok, err, b)
	}
	var keys []string
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("scan tokens: %v", err)
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{', '[':
				depth++
			case '}', ']':
				if depth == 0 {
					return keys
				}
				depth--
			}
		case string:
			if depth == 0 {
				keys = append(keys, v)
				// Skip this key's value wholesale, so a nested object's own keys
				// never leak into the top-level list.
				var skip json.RawMessage
				if err := dec.Decode(&skip); err != nil {
					t.Fatalf("skip value of %q: %v", v, err)
				}
			}
		}
	}
	return keys
}

// populatedInvoice is a fully-populated Invoice -- every nullable field non-nil, so a
// key that silently changed type or dropped out is visible.
func populatedInvoice(t *testing.T, id string) Invoice {
	t.Helper()
	s := func(v string) *string { return &v }
	when := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	n := 4
	return Invoice{
		ID: id, EntityID: uuid.NewString(), ImportBatchID: s(uuid.NewString()),
		InvoiceNumber: "APPR-08-08-FULL", Status: StatusValidated, IssueDate: &when,
		SupplierTIN: s("00000000001"), SupplierName: s("Acme Ltd"),
		BuyerTIN: s("00000000002"), BuyerName: s("Beta Ltd"),
		Currency: s("NGN"), Subtotal: s("1000.00"), VAT: s("75.00"), Total: s("1075.00"),
		Violations: json.RawMessage(`[]`), RuleSetVersionID: s(uuid.NewString()),
		CreatedAt: when, IRN: s("irn-1"), CSID: s("csid-1"), QRPayload: s("qr-1"),
		RejectionReasons: json.RawMessage(`[]`),
		KeptAsIsAt:       &when, KeptAsIsBy: s("someone"), KeptAsIsReason: s("because"),
		FailureKind: s("payload_not_built"), RuleSetVersion: &n,
	}
}

// armedRowFacts is one invoice's fully-populated standing -- the shape RowFactsTx
// answers for an invoice with an open run and a pending, staffed approval step.
func armedRowFacts() approval.RowFacts {
	ord := 0
	title := "Finance Lead"
	due := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	return approval.RowFacts{
		RunState: "open", PendingOrd: &ord, PendingRoleTitle: &title,
		PendingHolderWarn: true, DueAt: &due, Overdue: true,
	}
}

// listIdentity is the authenticated caller every case below uses.
func listIdentity() auth.Identity {
	return auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
}

// --- AC-1: the row key ------------------------------------------------------

// TestListHandler_RowCarriesApprovalOrNull: the seam's answer reaches the row it is
// keyed on, and a row the map does not mention carries an explicit null -- never the
// zero RowFacts object, which would claim run_state:"" for an invoice that has no run
// AND for one whose run the read failed to see.
func TestListHandler_RowCarriesApprovalOrNull(t *testing.T) {
	id := listIdentity()
	armed, bare := uuid.NewString(), uuid.NewString()
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{{ID: armed, Status: StatusValidated}, {ID: bare, Status: StatusDraft}}, 2, nil
	}
	facts := func(ctx context.Context, ids []string) (map[string]approval.RowFacts, ListGateFacts, error) {
		return map[string]approval.RowFacts{armed: armedRowFacts()}, ListGateFacts{}, nil
	}

	rec := doInvoiceListWithFacts(t, list, facts, &id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	rows := listRowsRaw(t, rec)
	if len(rows) != 2 {
		t.Fatalf("len(invoices) = %d, want 2 (body=%s)", len(rows), rec.Body.String())
	}

	rawArmed, ok := rows[0]["approval"]
	if !ok {
		t.Fatalf("row 0 has no \"approval\" key: %s", rec.Body.String())
	}
	var got approval.RowFacts
	if err := json.Unmarshal(rawArmed, &got); err != nil {
		t.Fatalf("decode row 0 approval %q: %v", string(rawArmed), err)
	}
	if !reflect.DeepEqual(got, armedRowFacts()) {
		t.Errorf("row 0 approval = %+v, want %+v -- the seam's answer must reach the wire unaltered", got, armedRowFacts())
	}

	rawBare, ok := rows[1]["approval"]
	if !ok {
		t.Fatalf("row 1 has no \"approval\" key: %s", rec.Body.String())
	}
	if string(rawBare) != "null" {
		t.Errorf("row 1 approval = %s, want the literal null -- an invoice absent from the seam's map has no run at all", rawBare)
	}
}

// TestListHandler_ApprovalKeyPresentOnEveryRow: the OMITTED-key half of AC-1. A page
// where the seam mentions nobody must still carry `approval` on every row: an absent
// key is indistinguishable from an older server that never heard of it, the
// fail-open shape [gates-on-the-wire] exists to remove (getResponse's own
// no-omitempty rule, handlers.go).
func TestListHandler_ApprovalKeyPresentOnEveryRow(t *testing.T) {
	id := listIdentity()
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{
			{ID: uuid.NewString(), Status: StatusDraft},
			{ID: uuid.NewString(), Status: StatusValidated},
			{ID: uuid.NewString(), Status: StatusAccepted},
		}, 3, nil
	}
	facts := func(ctx context.Context, ids []string) (map[string]approval.RowFacts, ListGateFacts, error) {
		return map[string]approval.RowFacts{}, ListGateFacts{}, nil
	}

	rec := doInvoiceListWithFacts(t, list, facts, &id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	rows := listRowsRaw(t, rec)
	if len(rows) != 3 {
		t.Fatalf("len(invoices) = %d, want 3", len(rows))
	}
	for i, row := range rows {
		raw, ok := row["approval"]
		if !ok {
			t.Errorf("row %d has no \"approval\" key -- the field carries no omitempty, so it is present on every row: %s", i, rec.Body.String())
			continue
		}
		if string(raw) != "null" {
			t.Errorf("row %d approval = %s, want the literal null", i, raw)
		}
	}
	// The raw-byte half: a `"approval":null` that decoded fine could still have been
	// emitted as `"approval": null` by a hand-rolled writer. Cheap, and it is the
	// exact assertion TestListHandler_EmptyState makes about "invoices":[].
	if n := strings.Count(rec.Body.String(), `"approval":null`); n != 3 {
		t.Errorf("body carries %d literal \"approval\":null, want 3: %s", n, rec.Body.String())
	}
}

// TestListItem_InvoiceKeysUnmovedAndUnrenamed: the wrapper is ADDITIVE. Marshalling
// listItem must emit every Invoice key with the same name, the same value and the
// same POSITION it has when Invoice is marshalled alone, with the three siblings
// appended after it IN ORDER -- the declaration-order-is-wire-order rule getResponse's
// comment states.
//
// A09-6 (APPR-12-09) widened the tail from one sibling to three: `approval`, then
// `can_approve`, then `approve_blocked_reason`. The leading-keys check below is
// deliberately byte-unchanged -- that half is what proves the widening is purely
// additive rather than a reshuffle.
func TestListItem_InvoiceKeysUnmovedAndUnrenamed(t *testing.T) {
	inv := populatedInvoice(t, uuid.NewString())

	bare, err := json.Marshal(inv)
	if err != nil {
		t.Fatalf("marshal Invoice: %v", err)
	}
	wrapped, err := json.Marshal(listItem{Invoice: inv})
	if err != nil {
		t.Fatalf("marshal listItem: %v", err)
	}

	bareKeys := jsonKeysInOrder(t, bare)
	wrappedKeys := jsonKeysInOrder(t, wrapped)

	if len(wrappedKeys) != len(bareKeys)+3 {
		t.Fatalf("listItem has %d keys %v, want Invoice's %d %v plus exactly three (\"approval\", \"can_approve\", \"approve_blocked_reason\")",
			len(wrappedKeys), wrappedKeys, len(bareKeys), bareKeys)
	}
	if !reflect.DeepEqual(wrappedKeys[:len(bareKeys)], bareKeys) {
		t.Errorf("listItem's leading keys = %v, want Invoice's own order %v -- embedding must not move or rename a key",
			wrappedKeys[:len(bareKeys)], bareKeys)
	}
	wantTail := []string{"approval", "can_approve", "approve_blocked_reason"}
	if tail := wrappedKeys[len(bareKeys):]; !reflect.DeepEqual(tail, wantTail) {
		t.Errorf("listItem's trailing keys = %v, want %v -- the siblings are appended in declaration order, never interleaved", tail, wantTail)
	}

	// Values, not just names: a key that kept its name but changed type would pass
	// the order check above.
	var bareObj, wrappedObj map[string]json.RawMessage
	if err := json.Unmarshal(bare, &bareObj); err != nil {
		t.Fatalf("decode Invoice: %v", err)
	}
	if err := json.Unmarshal(wrapped, &wrappedObj); err != nil {
		t.Fatalf("decode listItem: %v", err)
	}
	for k, want := range bareObj {
		got, ok := wrappedObj[k]
		if !ok {
			t.Errorf("listItem dropped Invoice's %q key", k)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("listItem[%q] = %s, want Invoice's own %s", k, got, want)
		}
	}

	// No omitempty (AC-1): a nil Approval renders explicit null, never an absent key.
	if !strings.Contains(string(wrapped), `"approval":null`) {
		t.Errorf("listItem with a nil Approval = %s, want the literal \"approval\":null -- the field must carry no omitempty", wrapped)
	}
}

// TestListItem_ApproveFlagsCarryNoOmitempty (A09-5, APPR-12-09): the ZERO listItem is the
// emptiest row the handler can build, and BOTH new keys must still be on it -- as
// "can_approve":false and "approve_blocked_reason":null.
//
// `omitempty` on a false bool and on a nil *string BOTH drop the key entirely, and an
// absent key is indistinguishable from an older server that never heard of it: the SPA
// would read undefined and fail OPEN on a permission-shaped flag. This is getResponse's
// own rule (handlers.go), applied to the list wrapper.
func TestListItem_ApproveFlagsCarryNoOmitempty(t *testing.T) {
	raw, err := json.Marshal(listItem{})
	if err != nil {
		t.Fatalf("marshal a zero listItem: %v", err)
	}
	for _, want := range []string{`"can_approve":false`, `"approve_blocked_reason":null`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("a zero listItem marshals to %s, want the literal %s -- neither field may carry omitempty", raw, want)
		}
	}
	// Presence, not just the literal: a key emitted with a leading space would pass the
	// substring check above only by accident, and this says which key is missing.
	keys := map[string]bool{}
	for _, k := range jsonKeysInOrder(t, raw) {
		keys[k] = true
	}
	for _, k := range []string{"can_approve", "approve_blocked_reason"} {
		if !keys[k] {
			t.Errorf("a zero listItem has no %q key: %s", k, raw)
		}
	}
}

// TestListItem_ApprovalObjectHasExactlySixKeys closes the leak hole APPR-12-09 opens.
//
// approval.RowFacts gains an INTERNAL PendingRoleKey field, tagged json:"-" so the list's
// gate can resolve the pending step's workflow-role key without publishing it. Nothing in
// this repo asserted the approval object's key SET before this spec: both existing checks
// (row_approval_envelope_adversarial_test.go's six-key loop and gate_adversarial_test.go's)
// are PRESENCE-only, so a PendingRoleKey that shipped without its tag would put
// `pending_role_key` on the public wire with no Go test failing.
//
// GREEN before and after. It is the tag's only oracle.
func TestListItem_ApprovalObjectHasExactlySixKeys(t *testing.T) {
	facts := armedRowFacts()
	wrapped, err := json.Marshal(listItem{Invoice: populatedInvoice(t, uuid.NewString()), Approval: &facts})
	if err != nil {
		t.Fatalf("marshal listItem: %v", err)
	}
	var row map[string]json.RawMessage
	if err := json.Unmarshal(wrapped, &row); err != nil {
		t.Fatalf("decode listItem: %v", err)
	}
	rawApproval, ok := row["approval"]
	if !ok || string(rawApproval) == "null" {
		t.Fatalf("listItem's approval = %s, want a populated object -- the key set below cannot be read off a null", rawApproval)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(rawApproval, &obj); err != nil {
		t.Fatalf("decode approval %q: %v", string(rawApproval), err)
	}

	got := make([]string, 0, len(obj))
	for k := range obj {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"due_at", "overdue", "pending_holder_warn", "pending_ord", "pending_role_title", "run_state"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the approval object's keys = %v,\nwant exactly %v\n-- a field added to approval.RowFacts without json:\"-\" leaks onto the public wire; PendingRoleKey (APPR-12-09) is internal to the gate", got, want)
	}
}

// --- AC-3: the envelope is untouched ----------------------------------------

// TestListHandler_EnvelopeStillExactlyTwoKeys: `approval` is a per-ROW key, so the
// top-level envelope stays {invoices,pagination}. The shipped
// TestListHandler_EnvelopeExactKeysAndEffectiveClampedValues asserts this over an
// EMPTY page; this one asserts it over a page whose rows carry the new key, which is
// the case that could actually have leaked one upward.
func TestListHandler_EnvelopeStillExactlyTwoKeys(t *testing.T) {
	id := listIdentity()
	invID := uuid.NewString()
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{{ID: invID, Status: StatusValidated}}, 1, nil
	}
	facts := func(ctx context.Context, ids []string) (map[string]approval.RowFacts, ListGateFacts, error) {
		return map[string]approval.RowFacts{invID: armedRowFacts()}, ListGateFacts{}, nil
	}

	rec := doInvoiceListWithFacts(t, list, facts, &id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if len(env) != 2 {
		t.Fatalf("envelope has %d top-level keys, want exactly 2 (invoices, pagination): %s", len(env), rec.Body.String())
	}
	for _, k := range []string{"invoices", "pagination"} {
		if _, ok := env[k]; !ok {
			t.Errorf("envelope missing %q", k)
		}
	}
	// Positive control: the row key really is on the wire, so the len==2 above is not
	// passing because nothing was added at all.
	rows := listRowsRaw(t, rec)
	if len(rows) != 1 {
		t.Fatalf("len(invoices) = %d, want 1", len(rows))
	}
	if _, ok := rows[0]["approval"]; !ok {
		t.Errorf("row 0 has no \"approval\" key -- this spec must not pass by the envelope staying two keys because nothing shipped: %s", rec.Body.String())
	}
}

// --- AC-4: the seam's error is a 500, never a degraded 200 ------------------

// TestListHandler_RowFactsErrorIs500: a read fault must NOT serve 200 with
// approval:null on every row. `null` is a positive claim ("this invoice has no
// approval run"), not an abstention, so serving it on a failed read is a silent lie
// on a compliance surface. This is deliberately the OPPOSITE of GetHandler's
// approvalFacts arm (handlers.go), whose zero value means "deny" and is therefore a
// safe answer to log and continue on.
func TestListHandler_RowFactsErrorIs500(t *testing.T) {
	id := listIdentity()
	invID := uuid.NewString()
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{{ID: invID, Status: StatusValidated}}, 1, nil
	}
	facts := func(ctx context.Context, ids []string) (map[string]approval.RowFacts, ListGateFacts, error) {
		return nil, ListGateFacts{}, context.DeadlineExceeded
	}

	rec := doInvoiceListWithFacts(t, list, facts, &id, "")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when the row-facts read fails (body=%s)", rec.Code, rec.Body.String())
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope %q: %v", rec.Body.String(), err)
	}
	if _, ok := env["error"]; !ok {
		t.Errorf("body = %s, want the shared {\"error\":\"...\"} envelope", rec.Body.String())
	}
	if _, ok := env["invoices"]; ok {
		t.Errorf("body = %s, must NOT carry a rows payload -- a degraded 200 with null approvals is the failure mode this spec exists for", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "context deadline exceeded") {
		t.Errorf("body = %s, must not leak the underlying error text -- it is logged, not written", rec.Body.String())
	}
}

// TestListHandler_RowFactsNotCalledOnAnEmptyPage (AC-5): zero rows means zero ids,
// and a round trip to ask about none of them is pure waste. The empty page must
// still serialize "invoices":[] -- that guarantee moves from Store.List to this
// handler's own make() the moment it maps into []listItem
// (TestListHandler_EmptyState, handlers_test.go, is the standing guard).
func TestListHandler_RowFactsNotCalledOnAnEmptyPage(t *testing.T) {
	id := listIdentity()
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{}, 0, nil
	}
	called := false
	facts := func(ctx context.Context, ids []string) (map[string]approval.RowFacts, ListGateFacts, error) {
		called = true
		return map[string]approval.RowFacts{}, ListGateFacts{}, nil
	}

	rec := doInvoiceListWithFacts(t, list, facts, &id, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if called {
		t.Error("rowFacts was called on an empty page -- there are no ids to ask about")
	}
	if !strings.Contains(rec.Body.String(), `"invoices":[]`) {
		t.Errorf("body = %s, want raw JSON to contain \"invoices\":[] (never null) -- a `var rows []listItem` marshals null", rec.Body.String())
	}
}

// TestListHandler_RowFactsCalledOncePerRequest: ONE call carrying the WHOLE page,
// never one per row. A per-row loop would answer identically on the wire and cost 50
// transactions, so the call count is the only oracle that separates them.
func TestListHandler_RowFactsCalledOncePerRequest(t *testing.T) {
	id := listIdentity()
	want := make([]string, 0, 50)
	rows := make([]Invoice, 0, 50)
	for i := 0; i < 50; i++ {
		invID := uuid.NewString()
		want = append(want, invID)
		rows = append(rows, Invoice{ID: invID, Status: StatusValidated})
	}
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return rows, 50, nil
	}
	calls := 0
	var seen []string
	facts := func(ctx context.Context, ids []string) (map[string]approval.RowFacts, ListGateFacts, error) {
		calls++
		seen = append([]string(nil), ids...)
		return map[string]approval.RowFacts{}, ListGateFacts{}, nil
	}

	rec := doInvoiceListWithFacts(t, list, facts, &id, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("rowFacts was called %d times for a 50-row page, want exactly 1", calls)
	}
	if !reflect.DeepEqual(seen, want) {
		t.Errorf("rowFacts received %d ids, want all 50 of the page's, in page order (got %v)", len(seen), seen)
	}
}

// TestListHandler_ApprovalKeyedOnTheStoreReturnedRowID: the ids handed to the seam
// are the STORE's own inv.ID, never a caller-supplied spelling. APPR-08-01 recorded
// DEFECT QA-1 -- a non-canonical uuid spelling is absent from RowFactsTx's map, so it
// would silently read "no run". That defect is unreachable HERE only because the ids
// come from the rows the store returned; this asserts that rather than assuming it,
// mirroring TestGetHandler_ApprovalSeamKeyedOnTheFetchedRowId.
func TestListHandler_ApprovalKeyedOnTheStoreReturnedRowID(t *testing.T) {
	id := listIdentity()
	canonical := strings.ToLower(uuid.NewString())
	upper := strings.ToUpper(canonical)
	if upper == canonical {
		t.Fatalf("fixture id %q has no hex letters -- the case this test exists for is not exercised", canonical)
	}
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{{ID: canonical, Status: StatusValidated}}, 1, nil
	}
	var seen []string
	facts := func(ctx context.Context, ids []string) (map[string]approval.RowFacts, ListGateFacts, error) {
		seen = append([]string(nil), ids...)
		return map[string]approval.RowFacts{canonical: armedRowFacts()}, ListGateFacts{}, nil
	}

	// ?q= carries the SAME invoice's id in Postgres's non-canonical spelling: the only
	// caller-supplied string on this route that could plausibly be mistaken for a row id.
	rec := doInvoiceListWithFacts(t, list, facts, &id, "?q="+upper)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(seen, []string{canonical}) {
		t.Fatalf("rowFacts received %v, want exactly [%s] -- the store-returned row id, never the query string", seen, canonical)
	}
	rows := listRowsRaw(t, rec)
	if len(rows) != 1 {
		t.Fatalf("len(invoices) = %d, want 1", len(rows))
	}
	raw, ok := rows[0]["approval"]
	if !ok || string(raw) == "null" {
		t.Errorf("row 0 approval = %s, want the seam's answer -- keying on anything but inv.ID silently reads \"no run\"", raw)
	}
}
