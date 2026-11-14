package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

const (
	udForbidden    = "only the uploader, an admin or a full-access member can undo an import"
	udNotCompleted = "only a completed import can be undone"
	udSent         = "an invoice of this import was sent to the tax authority"
)

// udWorld is one tenant with clients A and B and the route under test, over the real handlers.
type udWorld struct {
	csFixture
	admin2     auth.Identity // admin
	uploader   auth.Identity // reviewer limited to A; uploads every document
	allClients auth.Identity // reviewer with "All clients"
	outside    auth.Identity // reviewer limited to B
	mux        *http.ServeMux
	seq        int
	version    string
}

func newUDWorld(t *testing.T, label, kind string) *udWorld {
	t.Helper()
	w := &udWorld{csFixture: newCSFixture(t, label)}
	if kind == "in_house" {
		if _, err := w.super.Exec(context.Background(), `UPDATE tenants SET kind = 'in_house' WHERE id = $1`, w.tenant); err != nil {
			t.Fatalf("make the tenant in-house: %v", err)
		}
	}
	w.admin2, _ = auth.IdentityFromContext(w.admin)
	w.uploader = csMember(t, w.super, w.tenant, "reviewer", w.entA)
	w.allClients = csMember(t, w.super, w.tenant, "reviewer")
	if _, err := w.super.Exec(context.Background(), `UPDATE memberships SET all_clients = true WHERE tenant_id = $1 AND user_id = $2`, w.tenant, w.allClients.Subject); err != nil {
		t.Fatalf("grant All clients: %v", err)
	}
	w.outside = csMember(t, w.super, w.tenant, "reviewer", w.entB)
	w.mux = http.NewServeMux()
	w.mux.HandleFunc("GET /v1/imports/{id}", GetHandler(w.store.GetBatch, nil))
	w.mux.HandleFunc("POST /v1/imports/{id}/undo", UndoHandler(w.store.UndoBatch, nil))
	return w
}

func (w *udWorld) get(id auth.Identity, batch string) *httptest.ResponseRecorder {
	return csGet(w.mux, id, "GET", "/v1/imports/"+batch, "")
}

func (w *udWorld) undo(id auth.Identity, batch string) *httptest.ResponseRecorder {
	return csGet(w.mux, id, "POST", "/v1/imports/"+batch+"/undo", "")
}

type udResult struct {
	code int
	body string
}

func (w *udWorld) undoAsync(id auth.Identity, batch string) <-chan udResult {
	out := make(chan udResult, 1)
	go func() {
		rec := w.undo(id, batch)
		out <- udResult{rec.Code, rec.Body.String()}
	}()
	return out
}

type udInv struct {
	status  string
	number  string
	history []string // statuses after creation, in order
	kept    bool
	run     bool
}

// document stores a unique CSV uploaded by the world's limited uploader.
func (w *udWorld) document(t *testing.T) string {
	t.Helper()
	w.seq++
	body := csvBody(t, []string{"Inv No"}, [][]string{{fmt.Sprintf("UD-%d-%s", w.seq, uuid.NewString()[:8])}})
	return storeDocumentBy(t, w.docSvc, w.tenant, w.uploader.Subject, fmt.Sprintf("ud-%d.csv", w.seq), "text/csv", body).ID
}

// batch creates a batch of client A on a fresh document in the given state, with the invoices filed under it.
func (w *udWorld) batch(t *testing.T, state string, invs ...udInv) (batchID, doc string) {
	t.Helper()
	doc = w.document(t)
	id, err := w.store.CreateBatch(w.admin, w.entA, "ud.csv", doc, 1)
	if err != nil {
		t.Fatalf("create the batch: %v", err)
	}
	for _, in := range invs {
		w.invoice(t, id, doc, in)
	}
	switch state {
	case "processing":
	case "completed", "undone":
		if err := w.store.Finalize(w.admin, id, len(invs), len(invs), 0, nil, "completed"); err != nil {
			t.Fatalf("finalize completed: %v", err)
		}
		if state == "undone" {
			if _, err := w.super.Exec(context.Background(), `UPDATE import_batches SET status = 'undone' WHERE id = $1`, id); err != nil {
				t.Fatalf("mark undone: %v", err)
			}
		}
	case "failed":
		if err := w.store.Finalize(w.admin, id, 0, 0, 0, nil, "failed"); err != nil {
			t.Fatalf("finalize failed: %v", err)
		}
	default:
		t.Fatalf("unknown batch state %q", state)
	}
	return id, doc
}

func (w *udWorld) invoice(t *testing.T, batch, doc string, in udInv) string {
	t.Helper()
	ctx := context.Background()
	status := in.status
	if status == "" {
		status = "draft"
	}
	w.seq++
	number := fmt.Sprintf("%s-%d", in.number, w.seq) // unique per entity
	var id string
	if err := w.super.QueryRow(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, status, import_batch_id, source_document_id)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, w.tenant, w.entA, number, status, batch, doc).Scan(&id); err != nil {
		t.Fatalf("seed invoice %s: %v", in.number, err)
	}
	prev := "draft"
	// the creator is the uploader, so the admin is never the creator in an approval
	rows := [][3]any{{nil, "draft", w.uploader.Subject}}
	for _, to := range in.history {
		rows = append(rows, [3]any{prev, to, w.uploader.Subject})
		prev = to
	}
	for _, r := range rows {
		if _, err := w.super.Exec(ctx,
			`INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor) VALUES ($1, $2, $3, $4, $5)`,
			w.tenant, id, r[0], r[1], r[2]); err != nil {
			t.Fatalf("seed history of %s: %v", in.number, err)
		}
	}
	if in.kept {
		if _, err := w.super.Exec(ctx, `UPDATE invoices SET kept_as_is_at = now(), kept_as_is_by = 'someone', kept_as_is_reason = 'fine' WHERE id = $1`, id); err != nil {
			t.Fatalf("mark %s kept as is: %v", in.number, err)
		}
	}
	if in.run {
		w.openRun(t, id)
	}
	return id
}

// openRun gives the invoice an open run whose one pending step the admin may decide as the named delegate.
func (w *udWorld) openRun(t *testing.T, invoiceID string) string {
	t.Helper()
	ctx := context.Background()
	if w.version == "" {
		var policy string
		if err := w.super.QueryRow(ctx, `INSERT INTO approval_policies (tenant_id, name) VALUES ($1, 'ud policy') RETURNING id`, w.tenant).Scan(&policy); err != nil {
			t.Fatalf("seed policy: %v", err)
		}
		if err := w.super.QueryRow(ctx, `INSERT INTO approval_policy_versions (tenant_id, policy_id, version) VALUES ($1, $2, 1) RETURNING id`, w.tenant, policy).Scan(&w.version); err != nil {
			t.Fatalf("seed policy version: %v", err)
		}
		if _, err := w.super.Exec(ctx,
			`UPDATE approval_policy_versions SET sealed = true, is_active = true, published_at = now(), published_by = 'fixture' WHERE id = $1`, w.version); err != nil {
			t.Fatalf("seal policy version: %v", err)
		}
	}
	var run string
	if err := w.super.QueryRow(ctx,
		`INSERT INTO approval_runs (tenant_id, invoice_id, policy_version_id, content_fingerprint) VALUES ($1, $2, $3, 'fp') RETURNING id`,
		w.tenant, invoiceID, w.version).Scan(&run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if _, err := w.super.Exec(ctx,
		`INSERT INTO approval_run_steps (tenant_id, run_id, ord, kind, allow_delegation, delegate_user_id) VALUES ($1, $2, 0, 'approval', true, $3)`,
		w.tenant, run, w.admin2.Subject); err != nil {
		t.Fatalf("seed run step: %v", err)
	}
	return run
}

// snap is every row an undo could change for the batch, as one comparable string.
func (w *udWorld) snap(t *testing.T, batch, doc string) string {
	t.Helper()
	var s string
	if err := w.super.QueryRow(context.Background(), `
SELECT (SELECT status FROM import_batches WHERE id = $1)
    || ' | ' || coalesce((SELECT string_agg(i.id || ':' || i.status || ':' || coalesce(i.kept_as_is_at::text, '-'), ',' ORDER BY i.id)
                            FROM invoices i WHERE i.import_batch_id = $1), '')
    || ' | ' || (SELECT count(*) FROM invoice_status_history h JOIN invoices i ON i.id = h.invoice_id WHERE i.import_batch_id = $1)
    || ' | ' || coalesce((SELECT string_agg(r.id || ':' || r.state, ',' ORDER BY r.id)
                            FROM approval_runs r JOIN invoices i ON i.id = r.invoice_id WHERE i.import_batch_id = $1), '')
    || ' | ' || coalesce((SELECT entity_id::text FROM documents WHERE id = $2), 'unbound')
    || ' | ' || (SELECT count(*) FROM audit_log WHERE tenant_id = $3)`, batch, doc, w.tenant).Scan(&s); err != nil {
		t.Fatalf("snapshot the batch: %v", err)
	}
	return s
}

func udDecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return m
}

func udError(rec *httptest.ResponseRecorder) string {
	var m struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return m.Error
}

func udCount(t *testing.T, w *udWorld, query string, args ...any) int {
	t.Helper()
	return csCount(t, w.super, query, args...)
}

func (w *udWorld) undoneAudits(t *testing.T) int {
	return udCount(t, w, `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'import.undone'`, w.tenant)
}

// wantUndo requires status code and message, and that nothing changed when the answer is a refusal.
func (w *udWorld) wantUndo(t *testing.T, id auth.Identity, batch, doc string, code int, msg string) {
	t.Helper()
	before := w.snap(t, batch, doc)
	rec := w.undo(id, batch)
	if rec.Code != code || (msg != "" && udError(rec) != msg) {
		t.Errorf("undo = %d %s, want %d %q", rec.Code, rec.Body.String(), code, msg)
	}
	if code != http.StatusOK {
		if after := w.snap(t, batch, doc); after != before {
			t.Errorf("a refused undo changed state:\nbefore %s\nafter  %s", before, after)
		}
	}
}

func TestUndo_DiscardsEveryInvoiceAndReleasesTheDocument(t *testing.T) {
	w := newUDWorld(t, "UD discards", "firm")
	batch, doc := w.batch(t, "completed",
		udInv{number: "UD-DRAFT"},
		udInv{number: "UD-VALID", status: "validated", history: []string{"validated"}, run: true})
	csRequireBoundTo(t, w.csFixture, doc, w.entA)
	var draft, valid string
	if err := w.super.QueryRow(context.Background(), `SELECT id FROM invoices WHERE import_batch_id = $1 AND invoice_number LIKE 'UD-DRAFT-%'`, batch).Scan(&draft); err != nil {
		t.Fatal(err)
	}
	if err := w.super.QueryRow(context.Background(), `SELECT id FROM invoices WHERE import_batch_id = $1 AND invoice_number LIKE 'UD-VALID-%'`, batch).Scan(&valid); err != nil {
		t.Fatal(err)
	}

	rec := w.undo(w.admin2, batch)
	if rec.Code != http.StatusOK {
		t.Fatalf("undo = %d %s, want 200", rec.Code, rec.Body.String())
	}
	body := udDecode(t, rec)
	if body["id"] != batch || body["status"] != "undone" || body["discarded_invoices"] != float64(2) {
		t.Errorf("body = %v, want id %s, status undone, discarded_invoices 2", body, batch)
	}
	for inv, from := range map[string]string{draft: "draft", valid: "validated"} {
		var status string
		if err := w.super.QueryRow(context.Background(), `SELECT status FROM invoices WHERE id = $1`, inv).Scan(&status); err != nil || status != "discarded" {
			t.Errorf("invoice %s status = %q (err %v), want discarded", inv, status, err)
		}
		if n := udCount(t, w, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = $2 AND to_status = 'discarded' AND actor = $3`, inv, from, w.admin2.Subject); n != 1 {
			t.Errorf("history rows %s -> discarded by the admin for %s = %d, want 1", from, inv, n)
		}
	}
	if n := udCount(t, w, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1 AND state = 'cancelled'`, valid); n != 1 {
		t.Errorf("cancelled runs of the validated invoice = %d, want 1", n)
	}
	if n := udCount(t, w, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1 AND state IN ('open','approved')`, valid); n != 0 {
		t.Errorf("live runs of the validated invoice = %d, want 0", n)
	}
	var status string
	if err := w.super.QueryRow(context.Background(), `SELECT status FROM import_batches WHERE id = $1`, batch).Scan(&status); err != nil || status != "undone" {
		t.Errorf("batch status = %q (err %v), want undone", status, err)
	}
	csRequireBoundTo(t, w.csFixture, doc, "")

	imp := csImportFor(t, w.csFixture, w.entB)(w.admin2, doc)
	if imp.Code != http.StatusCreated {
		t.Errorf("import for B of the released document: %d %s, want 201", imp.Code, imp.Body.String())
	}
}

func TestUndo_OnlyTheUploaderAnAdminOrAFullAccessMemberUndoes(t *testing.T) {
	firm := newUDWorld(t, "UD who firm", "firm")
	house := newUDWorld(t, "UD who in-house", "in_house")
	cases := []struct {
		name  string
		world *udWorld
		who   auth.Identity
		code  int
	}{
		{"admin", firm, firm.admin2, 200},
		{"all clients reviewer", firm, firm.allClients, 200},
		{"in-house member", house, house.outside, 200}, // client scope is ignored in an in-house tenant
		{"limited uploader", firm, firm.uploader, 200},
		{"limited non-uploader with the client in scope", firm, firm.limitedID, 403},
		{"limited member outside the client", firm, firm.outside, 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			batch, doc := tc.world.batch(t, "completed", udInv{number: "WHO-1"}, udInv{number: "WHO-2", status: "validated", history: []string{"validated"}})
			switch tc.code {
			case 200:
				tc.world.wantUndo(t, tc.who, batch, doc, 200, "")
			case 403:
				tc.world.wantUndo(t, tc.who, batch, doc, 403, udForbidden)
			case 404:
				csSameAsUnknown(t, "undo of a batch outside the client set", tc.world.undo(tc.who, batch), tc.world.undo(tc.who, uuid.NewString()))
				if n := tc.world.undoneAudits(t); n != 0 {
					t.Errorf("import.undone rows after a refusal = %d, want 0", n)
				}
			}
		})
	}
}

func TestUndo_DiscardsAKeptAsIsDraft(t *testing.T) {
	w := newUDWorld(t, "UD kept", "firm")
	batch, doc := w.batch(t, "completed", udInv{number: "KEPT-1", kept: true})
	w.wantUndo(t, w.admin2, batch, doc, 200, "")
	var status string
	var marks int
	if err := w.super.QueryRow(context.Background(),
		`SELECT status, (kept_as_is_at IS NOT NULL)::int + (kept_as_is_by IS NOT NULL)::int + (kept_as_is_reason IS NOT NULL)::int
		   FROM invoices WHERE import_batch_id = $1`, batch).Scan(&status, &marks); err != nil {
		t.Fatal(err)
	}
	if status != "discarded" || marks != 0 {
		t.Errorf("kept draft = status %q with %d kept marks, want discarded with 0", status, marks)
	}
}

func TestUndo_ACompletedImportWithNoInvoiceReleasesTheDocument(t *testing.T) {
	w := newUDWorld(t, "UD empty", "firm")
	batch, doc := w.batch(t, "completed")
	csRequireBoundTo(t, w.csFixture, doc, w.entA)
	rec := w.undo(w.admin2, batch)
	if rec.Code != http.StatusOK {
		t.Fatalf("undo = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if body := udDecode(t, rec); body["discarded_invoices"] != float64(0) || body["status"] != "undone" {
		t.Errorf("body = %v, want status undone and discarded_invoices 0", body)
	}
	csRequireBoundTo(t, w.csFixture, doc, "")
}

func TestUndo_AnySentInvoiceRefusesTheUndoAndChangesNothing(t *testing.T) {
	w := newUDWorld(t, "UD sent", "firm")
	rows := []struct {
		name string
		sent udInv
	}{
		{"queued", udInv{number: "S-Q", status: "queued", history: []string{"validated", "queued"}}},
		{"submitted", udInv{number: "S-S", status: "submitted", history: []string{"validated", "queued", "submitted"}}},
		{"accepted", udInv{number: "S-A", status: "accepted", history: []string{"validated", "queued", "submitted", "accepted"}}},
		{"rejected", udInv{number: "S-R", status: "rejected", history: []string{"validated", "queued", "submitted", "rejected"}}},
		{"failed", udInv{number: "S-F", status: "failed", history: []string{"validated", "queued", "failed"}}},
		{"draft after queued, rejected", udInv{number: "S-D", status: "draft", history: []string{"validated", "queued", "rejected", "draft"}}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			batch, doc := w.batch(t, "completed",
				udInv{number: "S-" + row.name + "-V", status: "validated", history: []string{"validated"}, run: true},
				row.sent)
			w.wantUndo(t, w.admin2, batch, doc, 409, udSent)
			if n := w.undoneAudits(t); n != 0 {
				t.Errorf("import.undone rows = %d, want 0", n)
			}
		})
	}
}

func TestUndo_OnlyACompletedImportCanBeUndone(t *testing.T) {
	w := newUDWorld(t, "UD states", "firm")
	for _, state := range []string{"processing", "failed", "undone"} {
		t.Run(state, func(t *testing.T) {
			batch, doc := w.batch(t, state)
			w.wantUndo(t, w.admin2, batch, doc, 409, udNotCompleted)
		})
	}
	t.Run("second undo", func(t *testing.T) {
		batch, doc := w.batch(t, "completed", udInv{number: "TWICE-1"})
		w.wantUndo(t, w.admin2, batch, doc, 200, "")
		w.wantUndo(t, w.admin2, batch, doc, 409, udNotCompleted)
		if n := w.undoneAudits(t); n != 1 {
			t.Errorf("import.undone rows after two undos = %d, want 1", n)
		}
	})
}

func TestUndo_NotActiveMemberAndLockedWorkspaceAreRefused(t *testing.T) {
	w := newUDWorld(t, "UD gates", "firm")
	suspended := csMember(t, w.super, w.tenant, "admin")
	if _, err := w.super.Exec(context.Background(), `UPDATE memberships SET status = 'suspended' WHERE tenant_id = $1 AND user_id = $2`, w.tenant, suspended.Subject); err != nil {
		t.Fatal(err)
	}
	batch, doc := w.batch(t, "completed", udInv{number: "GATE-1", status: "validated", history: []string{"validated"}, run: true})
	w.wantUndo(t, suspended, batch, doc, 403, db.NotActiveMemberMessage)

	if _, err := w.super.Exec(context.Background(),
		`UPDATE tenants SET deletion_scheduled_at = now(), deletion_scheduled_by = $2, deletion_due_at = now() + interval '30 days' WHERE id = $1`,
		w.tenant, w.admin2.Subject); err != nil {
		t.Fatalf("schedule the workspace for deletion: %v", err)
	}
	w.wantUndo(t, w.admin2, batch, doc, 403, db.WorkspaceLockedMessage)
	if n := w.undoneAudits(t); n != 0 {
		t.Errorf("import.undone rows = %d, want 0", n)
	}
}

func TestUndo_WritesOneImportUndoneRecord(t *testing.T) {
	w := newUDWorld(t, "UD audit", "firm")
	batch, doc := w.batch(t, "completed", udInv{number: "AUD-1"}, udInv{number: "AUD-2", status: "validated", history: []string{"validated"}})
	w.wantUndo(t, w.admin2, batch, doc, 200, "")

	var actor, payload string
	var entity *string
	if err := w.super.QueryRow(context.Background(),
		`SELECT actor, payload::text, entity_id::text FROM audit_log WHERE tenant_id = $1 AND event = 'import.undone'`, w.tenant).Scan(&actor, &payload, &entity); err != nil {
		t.Fatalf("read the import.undone row (exactly one expected): %v", err)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatal(err)
	}
	if actor != w.admin2.Subject || p["batch_id"] != batch || p["invoices"] != float64(2) || len(p) != 2 {
		t.Errorf("import.undone = actor %s payload %s, want the admin and exactly {batch_id: %s, invoices: 2}", actor, payload, batch)
	}
	if entity != nil {
		t.Errorf("import.undone entity_id = %s, want NULL", *entity)
	}
	if n := udCount(t, w, `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'import.undone'`, w.tenant); n != 1 {
		t.Errorf("import.undone rows = %d, want 1", n)
	}
	if n := udCount(t, w, `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'invoice.transitioned'`, w.tenant); n != 0 {
		t.Errorf("invoice.transitioned rows from the discard = %d, want 0", n)
	}
}

// S1: every caller against every batch state.
func TestUndo_CallerByBatchStateMatrix(t *testing.T) {
	firm := newUDWorld(t, "UD matrix firm", "firm")
	house := newUDWorld(t, "UD matrix in-house", "in_house")
	other := newUDWorld(t, "UD matrix other tenant", "firm")
	callers := []struct {
		name  string
		world *udWorld // where the batch lives
		who   auth.Identity
		kind  string // full, uploader, nonuploader, hidden
	}{
		{"admin", firm, firm.admin2, "full"},
		{"all clients", firm, firm.allClients, "full"},
		{"in-house member", house, house.outside, "full"},
		{"limited uploader", firm, firm.uploader, "uploader"},
		{"limited non-uploader", firm, firm.limitedID, "nonuploader"},
		{"limited outside the client", firm, firm.outside, "hidden"},
		{"other tenant admin", firm, other.admin2, "hidden"},
	}
	states := []struct {
		name string
		make func(w *udWorld) (batch, doc string)
		sent bool
	}{
		{"processing", func(w *udWorld) (string, string) { return w.batch(t, "processing") }, false},
		{"completed unsent", func(w *udWorld) (string, string) {
			return w.batch(t, "completed", udInv{number: "M-D"}, udInv{number: "M-V", status: "validated", history: []string{"validated"}, run: true})
		}, false},
		{"completed sent", func(w *udWorld) (string, string) {
			return w.batch(t, "completed", udInv{number: "M-D"}, udInv{number: "M-Q", status: "queued", history: []string{"validated", "queued"}})
		}, true},
		{"failed", func(w *udWorld) (string, string) { return w.batch(t, "failed") }, false},
		{"undone", func(w *udWorld) (string, string) { return w.batch(t, "undone", udInv{number: "M-U"}) }, false},
	}
	for _, c := range callers {
		for _, s := range states {
			t.Run(c.name+"/"+s.name, func(t *testing.T) {
				batch, doc := s.make(c.world)
				wantCode, wantMsg := 200, ""
				switch {
				case c.kind == "hidden":
					wantCode = 404
				case c.kind == "nonuploader":
					wantCode, wantMsg = 403, udForbidden
				case s.name == "processing" || s.name == "failed" || s.name == "undone":
					wantCode, wantMsg = 409, udNotCompleted
				case s.sent:
					wantCode, wantMsg = 409, udSent
				}

				got := c.world.get(c.who, batch)
				if c.kind == "hidden" {
					csSameAsUnknown(t, "read of a hidden batch", got, c.world.get(c.who, uuid.NewString()))
					before := c.world.snap(t, batch, doc)
					csSameAsUnknown(t, "undo of a hidden batch", c.world.undo(c.who, batch), c.world.undo(c.who, uuid.NewString()))
					if after := c.world.snap(t, batch, doc); after != before {
						t.Errorf("a hidden caller's undo changed state:\nbefore %s\nafter  %s", before, after)
					}
					return
				}
				if got.Code != http.StatusOK {
					t.Fatalf("GET batch = %d %s, want 200", got.Code, got.Body.String())
				}
				if undoable := udDecode(t, got)["undoable"]; undoable != (wantCode == 200) {
					t.Errorf("undoable = %v, want %v", undoable, wantCode == 200)
				}
				c.world.wantUndo(t, c.who, batch, doc, wantCode, wantMsg)
			})
		}
	}
}

func TestUndo_TheUndoneClientsLimitedMemberLosesTheDocumentOnceBoundElsewhere(t *testing.T) {
	w := newUDWorld(t, "UD read", "firm")
	batch, doc := w.batch(t, "completed", udInv{number: "READ-A"})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/documents/{id}", document.DownloadHandler(w.docSvc.OpenLinked, nil))
	open := func(id auth.Identity, d string) *httptest.ResponseRecorder {
		return csGet(mux, id, "GET", "/v1/documents/"+d, "")
	}
	if rec := open(w.limitedID, doc); rec.Code != http.StatusOK {
		t.Fatalf("control: the A member reads the document of A's live import: %d, want 200", rec.Code)
	}

	w.wantUndo(t, w.admin2, batch, doc, 200, "")
	if rec := csImportFor(t, w.csFixture, w.entB)(w.admin2, doc); rec.Code != http.StatusCreated {
		t.Fatalf("import for B of the released document: %d %s, want 201", rec.Code, rec.Body.String())
	}
	csRequireBoundTo(t, w.csFixture, doc, w.entB)
	csSameAsUnknown(t, "the A member's download of the document now bound to B", open(w.limitedID, doc), open(w.limitedID, uuid.NewString()))
	if rec := open(w.uploader, doc); rec.Code != http.StatusOK {
		t.Errorf("the uploader's download = %d, want 200", rec.Code)
	}
}

// udAwaitLock returns once a backend waits on a lock in a statement containing needle. finished reports an early answer.
func udAwaitLock(t *testing.T, super *pgxpool.Pool, needle string, finished func() (string, bool)) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if what, done := finished(); done {
			t.Fatalf("answered without waiting on a lock in a statement containing %q: %s", needle, what)
		}
		var n int
		if err := super.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND pid <> pg_backend_pid() AND position($1 in query) > 0`, needle).Scan(&n); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if n > 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no backend waited on a lock in a statement containing %q", needle)
}

func udUndoFinished(res <-chan udResult, got *udResult) func() (string, bool) {
	return func() (string, bool) {
		select {
		case r := <-res:
			*got = r
			return fmt.Sprintf("undo = %d %s", r.code, r.body), true
		default:
			return "", false
		}
	}
}

func udReceive(t *testing.T, res <-chan udResult) udResult {
	t.Helper()
	select {
	case r := <-res:
		return r
	case <-time.After(15 * time.Second):
		t.Fatal("the undo never answered")
		return udResult{}
	}
}

func TestUndo_ASubmitThatCommitsFirstRefusesTheUndo(t *testing.T) {
	w := newUDWorld(t, "UD submit first", "firm")
	batch, doc := w.batch(t, "completed", udInv{number: "SF-D"}, udInv{number: "SF-V", status: "validated", history: []string{"validated"}})
	var valid string
	if err := w.super.QueryRow(context.Background(), `SELECT id FROM invoices WHERE import_batch_id = $1 AND invoice_number LIKE 'SF-V-%'`, batch).Scan(&valid); err != nil {
		t.Fatal(err)
	}
	before := w.snap(t, batch, doc)

	ctx := context.Background()
	tx1, err := w.super.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx1.Rollback(ctx) }()
	if _, err := tx1.Exec(ctx, `UPDATE invoices SET status = 'queued' WHERE id = $1`, valid); err != nil {
		t.Fatalf("queue the invoice in tx1: %v", err)
	}
	if _, err := tx1.Exec(ctx, `INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor) VALUES ($1, $2, 'validated', 'queued', $3)`, w.tenant, valid, w.admin2.Subject); err != nil {
		t.Fatal(err)
	}

	res := w.undoAsync(w.admin2, batch)
	var early udResult
	udAwaitLock(t, w.super, "FROM invoices", udUndoFinished(res, &early))
	if err := tx1.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := udReceive(t, res)
	if got.code != http.StatusConflict || !strings.Contains(got.body, udSent) {
		t.Errorf("undo = %d %s, want 409 %q", got.code, got.body, udSent)
	}
	after := w.snap(t, batch, doc)
	if !strings.Contains(after, valid+":queued") || strings.Contains(after, "discarded") || !strings.HasPrefix(after, "completed |") {
		t.Errorf("state after the refused undo:\nbefore %s\nafter  %s\nwant the submitted invoice queued, nothing discarded, batch completed", before, after)
	}
}

func TestUndo_AnUndoThatCommitsFirstRefusesTheSubmit(t *testing.T) {
	w := newUDWorld(t, "UD undo first", "firm")
	batch, doc := w.batch(t, "completed", udInv{number: "UF-V", status: "validated", history: []string{"validated"}})
	var valid string
	if err := w.super.QueryRow(context.Background(), `SELECT id FROM invoices WHERE import_batch_id = $1`, batch).Scan(&valid); err != nil {
		t.Fatal(err)
	}
	w.wantUndo(t, w.admin2, batch, doc, 200, "")

	q, err := queue.New(w.app, queue.Config{})
	if err != nil {
		t.Fatalf("build the insert-only queue client: %v", err)
	}
	out, err := invoice.NewSubmitter(invoice.NewStore(w.app), q).BatchSubmit(w.admin, invoice.BatchSubmitInput{InvoiceIDs: []string{valid}, IdempotencyKey: "ud-undo-first"})
	if err != nil {
		t.Fatalf("BatchSubmit: %v", err)
	}
	if len(out.Results) != 1 || out.Results[0].Enqueued || out.Results[0].Status != "discarded" {
		t.Errorf("submit results = %+v, want one result, not enqueued, status discarded", out.Results)
	}
	var status string
	if err := w.super.QueryRow(context.Background(), `SELECT status FROM invoices WHERE id = $1`, valid).Scan(&status); err != nil || status != "discarded" {
		t.Errorf("invoice status = %q (err %v), want discarded", status, err)
	}
	if n := udCount(t, w, `SELECT count(*) FROM submission_jobs WHERE invoice_id = $1`, valid); n != 0 {
		t.Errorf("submission_jobs rows = %d, want 0", n)
	}
	if n := udCount(t, w, `SELECT count(*) FROM river_job WHERE args->>'invoice_id' = $1`, valid); n != 0 {
		t.Errorf("river_job rows = %d, want 0", n)
	}
}

func udApprovals(w *udWorld) *approval.Store {
	return approval.NewStore(w.app, nil, nil)
}

func TestUndo_AnApprovalDecisionThatCommitsFirstIsCancelledByTheUndo(t *testing.T) {
	w := newUDWorld(t, "UD decide first", "firm")
	ctx := context.Background()
	// Control: the run fixture decides through the real store, so the held decision below is a real one.
	cBatch, _ := w.batch(t, "completed", udInv{number: "DF-C", status: "validated", history: []string{"validated"}, run: true})
	var control string
	if err := w.super.QueryRow(ctx, `SELECT id FROM invoices WHERE import_batch_id = $1`, cBatch).Scan(&control); err != nil {
		t.Fatal(err)
	}
	if _, err := udApprovals(w).Decide(w.admin, control, "approved", nil); err != nil {
		t.Fatalf("control: the admin cannot decide the seeded run: %v", err)
	}

	batch, _ := w.batch(t, "completed", udInv{number: "DF-V", status: "validated", history: []string{"validated"}, run: true})
	var valid, run string
	if err := w.super.QueryRow(ctx, `SELECT i.id, r.id FROM invoices i JOIN approval_runs r ON r.invoice_id = i.id WHERE i.import_batch_id = $1`, batch).Scan(&valid, &run); err != nil {
		t.Fatal(err)
	}
	// Holding the run row makes Decide wait after it took the invoice lock.
	hold, err := w.super.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback(ctx) }()
	if _, err := hold.Exec(ctx, `SELECT 1 FROM approval_runs WHERE id = $1 FOR UPDATE`, run); err != nil {
		t.Fatal(err)
	}
	decided := make(chan error, 1)
	go func() {
		_, err := udApprovals(w).Decide(w.admin, valid, "approved", nil)
		decided <- err
	}()
	udAwaitLock(t, w.super, "FROM approval_runs", func() (string, bool) {
		select {
		case err := <-decided:
			return fmt.Sprintf("Decide = %v", err), true
		default:
			return "", false
		}
	})
	res := w.undoAsync(w.admin2, batch)
	var early udResult
	udAwaitLock(t, w.super, "FROM invoices", udUndoFinished(res, &early))
	if err := hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if err := <-decided; err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got := udReceive(t, res); got.code != http.StatusOK {
		t.Fatalf("undo = %d %s, want 200", got.code, got.body)
	}
	if n := udCount(t, w, `SELECT count(*) FROM approval_decisions WHERE run_id = $1`, run); n != 1 {
		t.Errorf("decisions of the run = %d, want 1 (the decision committed first)", n)
	}
	if n := udCount(t, w, `SELECT count(*) FROM approval_runs WHERE id = $1 AND state = 'cancelled'`, run); n != 1 {
		t.Errorf("the run is not cancelled")
	}
	if n := udCount(t, w, `SELECT count(*) FROM invoices WHERE id = $1 AND status = 'discarded'`, valid); n != 1 {
		t.Errorf("the invoice is not discarded")
	}
}

func TestUndo_AnUndoThatCommitsFirstRefusesTheApprovalDecision(t *testing.T) {
	w := newUDWorld(t, "UD undo then decide", "firm")
	batch, doc := w.batch(t, "completed", udInv{number: "UD-V", status: "validated", history: []string{"validated"}, run: true})
	var valid, run string
	if err := w.super.QueryRow(context.Background(), `SELECT i.id, r.id FROM invoices i JOIN approval_runs r ON r.invoice_id = i.id WHERE i.import_batch_id = $1`, batch).Scan(&valid, &run); err != nil {
		t.Fatal(err)
	}
	w.wantUndo(t, w.admin2, batch, doc, 200, "")

	_, err := udApprovals(w).Decide(w.admin, valid, "approved", nil)
	if !errors.Is(err, approval.ErrNotAwaitingApproval) {
		t.Errorf("Decide after the undo = %v, want %v", err, approval.ErrNotAwaitingApproval)
	}
	if n := udCount(t, w, `SELECT count(*) FROM approval_runs WHERE id = $1 AND state = 'cancelled'`, run); n != 1 {
		t.Errorf("the run is not cancelled")
	}
	if n := udCount(t, w, `SELECT count(*) FROM approval_run_steps WHERE run_id = $1 AND state <> 'pending'`, run); n != 0 {
		t.Errorf("decided steps of the cancelled run = %d, want 0", n)
	}
	if n := udCount(t, w, `SELECT count(*) FROM approval_decisions WHERE run_id = $1`, run); n != 0 {
		t.Errorf("decisions of the cancelled run = %d, want 0", n)
	}
}

func TestUndo_ConcurrentUndosDiscardOnce(t *testing.T) {
	w := newUDWorld(t, "UD concurrent", "firm")
	batch, _ := w.batch(t, "completed", udInv{number: "CC-1"}, udInv{number: "CC-2", status: "validated", history: []string{"validated"}, run: true})
	start := make(chan struct{})
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes[i] = w.undo(w.admin2, batch).Code
		}()
	}
	close(start)
	wg.Wait()
	slices.Sort(codes)
	if !slices.Equal(codes, []int{200, 409}) {
		t.Errorf("answers = %v, want one 200 and one 409", codes)
	}
	if n := udCount(t, w, `SELECT count(*) FROM invoice_status_history h JOIN invoices i ON i.id = h.invoice_id WHERE i.import_batch_id = $1 AND h.to_status = 'discarded'`, batch); n != 2 {
		t.Errorf("-> discarded history rows = %d, want 2 (one per invoice)", n)
	}
	if n := w.undoneAudits(t); n != 1 {
		t.Errorf("import.undone rows = %d, want 1", n)
	}
}
