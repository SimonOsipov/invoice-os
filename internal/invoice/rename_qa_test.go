package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Typed out, not read from the constants: a rewording must fail in CI, not only at the deploy gate.
const (
	qa27TakenSentence = "This invoice number is already in the register for this company. Enter a different number."
	qa27FixedSentence = "The invoice number can only be corrected while the invoice is a draft that has never been submitted."
)

func qa27Ctx(tenantID, subject string) context.Context {
	return auth.WithIdentity(context.Background(), auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})
}

func qa27Number(t *testing.T, super *pgxpool.Pool, id string) string {
	t.Helper()
	var n string
	if err := super.QueryRow(context.Background(), `SELECT invoice_number FROM invoices WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("read invoice_number for %s: %v", id, err)
	}
	return n
}

// The predicate admits validated history: validate-then-edit is the ordinary fix loop.
func TestQA27_ADraftDemotedFromValidatedStillRenames(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenantID := seedTenant(t, super, "QA27 demoted tenant")
	entityID := seedEntity(t, super, tenantID, "QA27 demoted entity")
	c := qa27Ctx(tenantID, memberSubject)

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "QA27-DEM-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("-> validated: %v", err)
	}
	demoted, err := store.Edit(c, inv.ID, EditInput{UpdateInput: UpdateInput{BuyerName: strPtr("Demoted Buyer")}})
	if err != nil {
		t.Fatalf("Edit (demote): %v", err)
	}
	if demoted.Status != StatusDraft {
		t.Fatalf("status after edit = %q, want draft", demoted.Status)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND to_status = 'validated'`, inv.ID); n != 1 {
		t.Fatalf("validated history rows = %d, want 1 -- without it this test discriminates nothing", n)
	}

	got, err := store.Get(c, inv.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.EverSubmitted {
		t.Errorf("EverSubmitted = true for a draft that only ever reached validated, want false")
	}
	newNumber := "QA27-DEM-2"
	renamed, err := store.Edit(c, inv.ID, EditInput{InvoiceNumber: &newNumber})
	if err != nil {
		t.Fatalf("Edit (rename a draft demoted from validated): want success, got %v", err)
	}
	if renamed.InvoiceNumber != newNumber || qa27Number(t, super, inv.ID) != newNumber {
		t.Errorf("number = returned %q / stored %q, want %q", renamed.InvoiceNumber, qa27Number(t, super, inv.ID), newNumber)
	}
}

func TestQA27_EveryHistoryPastValidatedFixesTheNumber(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenantID := seedTenant(t, super, "QA27 history tenant")
	entityID := seedEntity(t, super, tenantID, "QA27 history entity")
	c := qa27Ctx(tenantID, memberSubject)

	cases := []struct {
		from  *Status
		to    Status
		fixed bool
	}{
		{nil, StatusDraft, false},
		{statusPtr(StatusDraft), StatusValidated, false},
		{statusPtr(StatusValidated), StatusQueued, true},
		{statusPtr(StatusQueued), StatusSubmitted, true},
		{statusPtr(StatusSubmitted), StatusAccepted, true},
		{statusPtr(StatusQueued), StatusRejected, true},
		{statusPtr(StatusQueued), StatusFailed, true},
	}
	var fixed, open int
	for i, tc := range cases {
		t.Run(string(tc.to), func(t *testing.T) {
			number := fmt.Sprintf("QA27-HIST-%d", i)
			id := seedInvoice(t, super, tenantID, entityID, number)
			seedHistoryRow(t, super, tenantID, id, tc.from, tc.to, memberSubject, time.Now().UTC())

			got, err := store.Get(c, id)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Status != StatusDraft || got.EverSubmitted != tc.fixed {
				t.Errorf("status/EverSubmitted = %q/%v, want draft/%v", got.Status, got.EverSubmitted, tc.fixed)
			}

			newNumber := number + "-R"
			_, err = store.Edit(c, id, EditInput{InvoiceNumber: &newNumber})
			if tc.fixed {
				fixed++
				if !errors.Is(err, ErrNumberFixed) {
					t.Fatalf("Edit (rename) err = %v, want ErrNumberFixed", err)
				}
				if n := qa27Number(t, super, id); n != number {
					t.Errorf("stored number = %q, want unchanged %q", n, number)
				}
				return
			}
			open++
			if err != nil {
				t.Fatalf("Edit (rename) err = %v, want success", err)
			}
			if n := qa27Number(t, super, id); n != newNumber {
				t.Errorf("stored number = %q, want %q", n, newNumber)
			}
		})
	}
	if fixed != 5 || open != 2 {
		t.Errorf("rows exercised fixed/open = %d/%d, want 5/2", fixed, open)
	}
}

func TestQA27_AQueuedInvoiceRefusesARenameAsNotFixable(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenantID := seedTenant(t, super, "QA27 queued tenant")
	entityID := seedEntity(t, super, tenantID, "QA27 queued entity")
	id := seedInvoiceAtStatus(t, super, tenantID, entityID, "QA27-Q-1", StatusQueued)

	newNumber := "QA27-Q-2"
	if _, err := store.Edit(qa27Ctx(tenantID, memberSubject), id, EditInput{InvoiceNumber: &newNumber}); !errors.Is(err, ErrNotFixable) {
		t.Fatalf("Edit (rename a queued invoice) err = %v, want ErrNotFixable", err)
	}
	if n := qa27Number(t, super, id); n != "QA27-Q-1" {
		t.Errorf("stored number = %q, want unchanged", n)
	}
}

func TestQA27_RenameIsTenantScoped(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenantA := seedTenant(t, super, "QA27 scope tenant A")
	tenantB := seedTenant(t, super, "QA27 scope tenant B")
	entityA := seedEntity(t, super, tenantA, "QA27 scope entity A")
	entityB := seedEntity(t, super, tenantB, "QA27 scope entity B")
	cA := qa27Ctx(tenantA, memberSubject)
	cB := qa27Ctx(tenantB, memberSubject)

	target, err := store.Create(cA, CreateInput{EntityID: entityA, InvoiceNumber: "QA27-SCOPE-A1"})
	if err != nil {
		t.Fatalf("Create A1: %v", err)
	}
	if _, err := store.Create(cA, CreateInput{EntityID: entityA, InvoiceNumber: "QA27-SCOPE-A2"}); err != nil {
		t.Fatalf("Create A2: %v", err)
	}
	const shared = "QA27-SCOPE-SHARED"
	if _, err := store.Create(cB, CreateInput{EntityID: entityB, InvoiceNumber: shared}); err != nil {
		t.Fatalf("Create B shared: %v", err)
	}

	stolen := "QA27-SCOPE-STOLEN"
	if _, err := store.Edit(cB, target.ID, EditInput{InvoiceNumber: &stolen}); !errors.Is(err, ErrNotFound) {
		t.Errorf("tenant B renaming A's invoice err = %v, want ErrNotFound", err)
	}
	if _, err := store.Get(cB, target.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("tenant B reading A's invoice err = %v, want ErrNotFound", err)
	}
	if n := qa27Number(t, super, target.ID); n != "QA27-SCOPE-A1" {
		t.Errorf("A1 number after tenant B's attempt = %q, want unchanged", n)
	}
	for _, tenant := range []string{tenantA, tenantB} {
		if n := auditCount(t, app, tenant, "invoice.updated"); n != 0 {
			t.Errorf("tenant %s invoice.updated rows = %d, want 0", tenant, n)
		}
	}

	// The unique index is per (tenant, entity): B's number is free in A.
	sharedNumber := shared
	if _, err := store.Edit(cA, target.ID, EditInput{InvoiceNumber: &sharedNumber}); err != nil {
		t.Fatalf("tenant A renaming to a number only tenant B holds: want success, got %v", err)
	}
	for tenant, want := range map[string]int{tenantA: 1, tenantB: 1} {
		if n := invoiceCountByNumber(t, super, tenant, shared); n != want {
			t.Errorf("tenant %s invoices numbered %q = %d, want %d", tenant, shared, n, want)
		}
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenantA); n != 2 {
		t.Errorf("tenant A invoices = %d, want 2 (B holds 1)", n)
	}
}

func TestQA27_ANumberIsTakenPerEntityNotPerTenant(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenantID := seedTenant(t, super, "QA27 entity tenant")
	e1 := seedEntity(t, super, tenantID, "QA27 entity one")
	e2 := seedEntity(t, super, tenantID, "QA27 entity two")
	c := qa27Ctx(tenantID, memberSubject)

	r, err := store.Create(c, CreateInput{EntityID: e1, InvoiceNumber: "QA27-ENT-R"})
	if err != nil {
		t.Fatalf("Create R: %v", err)
	}
	for _, seed := range []struct{ entity, number string }{{e1, "QA27-ENT-TAKEN-E1"}, {e2, "QA27-ENT-TAKEN-E2"}} {
		if _, err := store.Create(c, CreateInput{EntityID: seed.entity, InvoiceNumber: seed.number}); err != nil {
			t.Fatalf("Create %s: %v", seed.number, err)
		}
	}

	otherEntity := "QA27-ENT-TAKEN-E2"
	if _, err := store.Edit(c, r.ID, EditInput{InvoiceNumber: &otherEntity}); err != nil {
		t.Fatalf("rename to a number held by another entity: want success, got %v", err)
	}
	sameEntity := "QA27-ENT-TAKEN-E1"
	if _, err := store.Edit(c, r.ID, EditInput{InvoiceNumber: &sameEntity}); !errors.Is(err, ErrNumberTaken) {
		t.Fatalf("rename to a number held in the same entity err = %v, want ErrNumberTaken", err)
	}
	if n := qa27Number(t, super, r.ID); n != otherEntity {
		t.Errorf("stored number = %q, want %q", n, otherEntity)
	}
}

func TestQA27_ASuppliedNumberThatIsTakenWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenantID := seedTenant(t, super, "QA27 supplied-taken tenant")
	entityID := seedEntity(t, super, tenantID, "QA27 supplied-taken entity")
	documentID := seedDocument(t, super, tenantID)
	c := qa27Ctx(tenantID, memberSubject)

	const taken = "QA27-SUP-TAKEN"
	if _, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: taken}); err != nil {
		t.Fatalf("Create the holder: %v", err)
	}
	beforeCreated := auditCount(t, app, tenantID, "invoice.created")

	_, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: taken, SourceDocumentID: &documentID, NumberSupplied: true})
	if !errors.Is(err, ErrDuplicateNumber) {
		t.Fatalf("supplied create with a taken number err = %v, want ErrDuplicateNumber", err)
	}
	if n := invoiceCountByNumber(t, super, tenantID, taken); n != 1 {
		t.Errorf("invoices numbered %q = %d, want 1", taken, n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE source_document_id = $1`, documentID); n != 0 {
		t.Errorf("invoices citing the document = %d, want 0", n)
	}
	if n := auditCount(t, app, tenantID, "invoice.created"); n != beforeCreated {
		t.Errorf("invoice.created rows = %d, want unchanged %d", n, beforeCreated)
	}
}

// A document import files with a SourceDocumentID and no supplied number; its payload must not grow.
func TestQA27_ADocumentCreateWithoutASuppliedNumberKeepsTheOrdinaryPayload(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenantID := seedTenant(t, super, "QA27 document-create tenant")
	entityID := seedEntity(t, super, tenantID, "QA27 document-create entity")
	documentID := seedDocument(t, super, tenantID)
	c := qa27Ctx(tenantID, memberSubject)

	if _, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "QA27-DOC-ORD", SourceDocumentID: &documentID}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	payload := auditPayloadMap(t, app, tenantID, "invoice.created")
	keys := make([]string, 0, len(payload))
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"id", "invoice_number"}) {
		t.Errorf("payload keys = %v, want exactly [id invoice_number] (payload=%v)", keys, payload)
	}
	if payload["invoice_number"] != "QA27-DOC-ORD" {
		t.Errorf("payload invoice_number = %v, want QA27-DOC-ORD", payload["invoice_number"])
	}
}

func TestQA27_ARenameWithHeaderAndLinesAuditsEveryField(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenantID := seedTenant(t, super, "QA27 combined tenant")
	entityID := seedEntity(t, super, tenantID, "QA27 combined entity")
	c := qa27Ctx(tenantID, memberSubject)

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "QA27-ALL-1", BuyerName: strPtr("Old Buyer")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")

	desc, price := "QA27 line", "10.00"
	newNumber := "QA27-ALL-2"
	got, err := store.Edit(c, inv.ID, EditInput{
		UpdateInput:   UpdateInput{BuyerName: strPtr("New Buyer")},
		LineItems:     &[]LineItemInput{{Description: &desc, UnitPrice: &price}},
		InvoiceNumber: &newNumber,
	})
	if err != nil {
		t.Fatalf("Edit (rename + header + lines): %v", err)
	}
	if got.InvoiceNumber != newNumber {
		t.Errorf("returned number = %q, want %q", got.InvoiceNumber, newNumber)
	}
	if got.BuyerName == nil || *got.BuyerName != "New Buyer" || len(got.LineItems) != 1 {
		t.Errorf("returned buyer/lines = %v/%d, want New Buyer/1", got.BuyerName, len(got.LineItems))
	}
	if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated+1 {
		t.Errorf("invoice.updated rows = %d, want %d", n, beforeUpdated+1)
	}
	if fields := auditFields(t, app, tenantID, "invoice.updated"); !reflect.DeepEqual(fields, []string{"invoice_number", "buyer_name", "line_items"}) {
		t.Errorf("audit fields = %v, want [invoice_number buyer_name line_items]", fields)
	}
	payload := auditPayloadMap(t, app, tenantID, "invoice.updated")
	if payload["invoice_number"] != newNumber || payload["previous_invoice_number"] != "QA27-ALL-1" {
		t.Errorf("payload numbers = %v -> %v, want QA27-ALL-1 -> %s", payload["previous_invoice_number"], payload["invoice_number"], newNumber)
	}
	var stored, buyer string
	if err := super.QueryRow(context.Background(), `SELECT invoice_number, buyer_name FROM invoices WHERE id = $1`, inv.ID).Scan(&stored, &buyer); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != newNumber || buyer != "New Buyer" {
		t.Errorf("stored number/buyer = %q/%q, want %q/New Buyer", stored, buyer, newNumber)
	}
}

func TestQA27_ASuspendedMemberCannotRename(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenantID := seedTenant(t, super, "QA27 suspended tenant")
	entityID := seedEntity(t, super, tenantID, "QA27 suspended entity")
	c := qa27Ctx(tenantID, memberSubject)

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "QA27-SUSP-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	subject := uuid.NewString()
	seedMembershipWithStatus(t, super, tenantID, subject, "preparer", "suspended")

	newNumber := "QA27-SUSP-2"
	if _, err := store.Edit(qa27Ctx(tenantID, subject), inv.ID, EditInput{InvoiceNumber: &newNumber}); !errors.Is(err, db.ErrNotActiveMember) {
		t.Fatalf("suspended rename err = %v, want db.ErrNotActiveMember", err)
	}
	if n := qa27Number(t, super, inv.ID); n != "QA27-SUSP-1" {
		t.Errorf("stored number = %q, want unchanged", n)
	}
	if n := auditCount(t, app, tenantID, "invoice.updated"); n != 0 {
		t.Errorf("invoice.updated rows = %d, want 0", n)
	}
	// control: the active member renames the same draft.
	if _, err := store.Edit(c, inv.ID, EditInput{InvoiceNumber: &newNumber}); err != nil {
		t.Fatalf("control: active member rename: %v", err)
	}
}

func TestQA27_ByDocumentRenameFollowsTheSameGate(t *testing.T) {
	f := ebsSeed(t, "QA27-EBS-1")

	renamed := "QA27-EBS-2"
	got, err := f.edit(t, f.documentID, EditInput{InvoiceNumber: &renamed})
	if err != nil {
		t.Fatalf("by-document rename of a draft: %v", err)
	}
	if got.InvoiceNumber != renamed || qa27Number(t, f.super, f.invoiceID) != renamed {
		t.Errorf("number = returned %q / stored %q, want %q", got.InvoiceNumber, qa27Number(t, f.super, f.invoiceID), renamed)
	}

	if _, err := f.store.Transition(f.ctx, f.invoiceID, StatusValidated); err != nil {
		t.Fatalf("-> validated: %v", err)
	}
	refused := "QA27-EBS-3"
	if _, err := f.edit(t, f.documentID, EditInput{InvoiceNumber: &refused}); !errors.Is(err, ErrNumberFixed) {
		t.Fatalf("by-document rename of a validated invoice err = %v, want ErrNumberFixed", err)
	}
	if n := qa27Number(t, f.super, f.invoiceID); n != renamed {
		t.Errorf("stored number = %q, want unchanged %q", n, renamed)
	}
}

func TestQA27_EditHandlerTrimsOnlyTheEdges(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()

	cases := []struct {
		body string
		want *string
	}{
		{`{"invoice_number":"\t n-27 B \n"}`, strPtr("n-27 B")},
		{`{"invoice_number":null,"buyer_name":"X"}`, nil},
		{`{"buyer_name":"X"}`, nil},
	}
	for _, tc := range cases {
		called := false
		var gotIn EditInput
		edit := func(ctx context.Context, _ string, in EditInput) (Invoice, error) {
			called, gotIn = true, in
			return Invoice{ID: invoiceID, Status: StatusDraft}, nil
		}
		rec, _ := doInvoiceEdit(t, edit, &id, invoiceID, tc.body)
		if rec.Code != http.StatusOK || !called {
			t.Errorf("body=%s: status = %d, called = %v, want 200/true", tc.body, rec.Code, called)
			continue
		}
		if !reflect.DeepEqual(gotIn.InvoiceNumber, tc.want) {
			t.Errorf("body=%s: EditInput.InvoiceNumber = %v, want %v", tc.body, gotIn.InvoiceNumber, tc.want)
		}
	}
}

func TestQA27_EditHandlerRefusalsCarryTheDecidedSentences(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()

	for _, tc := range []struct {
		err  error
		want string
	}{{ErrNumberTaken, qa27TakenSentence}, {ErrNumberFixed, qa27FixedSentence}} {
		edit := func(context.Context, string, EditInput) (Invoice, error) { return Invoice{}, tc.err }
		rec, resp := doInvoiceEdit(t, edit, &id, invoiceID, `{"invoice_number":"N2"}`)
		if rec.Code != http.StatusConflict || resp.Error != tc.want {
			t.Errorf("%v: status/error = %d/%q, want 409/%q", tc.err, rec.Code, resp.Error, tc.want)
		}
	}
}

func TestQA27_GetGateOnSubmittedHistory(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	quoted, _ := json.Marshal(qa27FixedSentence)

	for _, tc := range []struct {
		status     Status
		wantReason string
	}{
		{StatusRejected, string(quoted)}, // editable, so the reason is shown
		{StatusAccepted, "null"},         // not editable, so no reason
	} {
		invoiceID := uuid.NewString()
		get := func(context.Context, string) (Invoice, error) {
			return Invoice{ID: invoiceID, Status: tc.status, EverSubmitted: true}, nil
		}
		rec, _ := doInvoiceGet(t, get, &id, invoiceID)
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatalf("%s: decode: %v", tc.status, err)
		}
		if string(raw["can_correct_invoice_number"]) != "false" || string(raw["invoice_number_blocked_reason"]) != tc.wantReason {
			t.Errorf("%s: can_correct/reason = %s/%s, want false/%s", tc.status, raw["can_correct_invoice_number"], raw["invoice_number_blocked_reason"], tc.wantReason)
		}
	}
}
