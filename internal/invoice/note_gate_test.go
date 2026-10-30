package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// ngFix is one tenant and client with an accepted commercial original (IRN, own total).
type ngFix struct {
	t          *testing.T
	super, app *pgxpool.Pool
	store      *Store
	ctx        context.Context
	tenant     string
	entity     string
	orig       string
}

func newNGFix(t *testing.T, label, origTotal string) ngFix {
	t.Helper()
	super, app := dbTestPools(t)
	return ngFixIn(t, super, app, seedTenant(t, super, label), origTotal)
}

// ngFixIn builds the fixture in an existing tenant. The entity carries the TIN that
// gapiValidInvoiceInput expects, so the notes are clean under the real rule set.
func ngFixIn(t *testing.T, super, app *pgxpool.Pool, tenant, origTotal string) ngFix {
	t.Helper()
	f := ngFix{t: t, super: super, app: app, store: NewStore(app), tenant: tenant}
	f.ctx = auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenant})
	f.entity = seedEntityWithTIN(t, super, tenant, "Acme Ltd", "12345678-0001")
	f.orig = f.original("NG-O", origTotal)
	return f
}

// original seeds an accepted commercial invoice with an IRN and the given total.
func (f ngFix) original(number, total string) string {
	f.t.Helper()
	id := seedInvoice(f.t, f.super, f.tenant, f.entity, number)
	ncAccepted(f.t, f.super, id, "IRN-"+number, total)
	return id
}

// note creates a clean draft note on original through Store.Create, then sets its total.
func (f ngFix) note(number string, typ submission.DocumentType, original, total string) string {
	f.t.Helper()
	in := gapiValidInvoiceInput(f.entity, number)
	in.DocumentType, in.OriginalInvoiceID = typ, &original
	inv, err := f.store.Create(f.ctx, in)
	if err != nil {
		f.t.Fatalf("create %s %s: %v", typ, number, err)
	}
	if _, err := f.super.Exec(context.Background(),
		`UPDATE invoices SET total = $2::numeric WHERE id = $1`, inv.ID, total); err != nil {
		f.t.Fatalf("set total of %s: %v", number, err)
	}
	return inv.ID
}

func (f ngFix) credit(number, total string) string {
	f.t.Helper()
	return f.note(number, submission.CreditNote, f.orig, total)
}

// get returns the stored invoice and the fingerprint a gate run would take of it.
func (f ngFix) get(id string) (Invoice, string) {
	f.t.Helper()
	inv, err := f.store.Get(f.ctx, id)
	if err != nil {
		f.t.Fatalf("get %s: %v", id, err)
	}
	return inv, contentFingerprint(inv, inv.LineItems)
}

// apply runs the deciding write with the verdict vs, as a gate run does.
func (f ngFix) apply(id string, vs []Violation) (Invoice, error) {
	f.t.Helper()
	_, fp := f.get(id)
	return f.store.ApplyValidation(f.ctx, id, vs, seedRuleSetVersionID(f.t, f.super), fp)
}

func (f ngFix) realGate() *Gate {
	return NewGate(f.store, NewValidator(startInProcess04(f.t, f.app).URL, gapiS2SToken, nil))
}

func (f ngFix) status(id string) Status { return readInvoiceStatus(f.t, f.super, id) }

// ngStub is a stand-in for 04 that records requests and can act inside a request.
type ngStub struct {
	*httptest.Server
	mu     sync.Mutex
	reqs   []validateBatchRequest
	during func()
	answer func(ref string) []Violation
}

func newNGStub(t *testing.T, super *pgxpool.Pool) *ngStub {
	t.Helper()
	versionID := seedRuleSetVersionID(t, super)
	s := &ngStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req validateBatchRequest
		_ = json.Unmarshal(body, &req)
		s.mu.Lock()
		s.reqs = append(s.reqs, req)
		during, answer := s.during, s.answer
		s.mu.Unlock()
		if during != nil {
			during()
		}
		results := make([]validateBatchItemResult, len(req.Invoices))
		for i, it := range req.Invoices {
			vs := []Violation{}
			if answer != nil {
				vs = answer(it.Ref)
			}
			results[i] = validateBatchItemResult{Ref: it.Ref, Violations: vs}
		}
		writeValidateResponse(t, w, versionID, results)
	}))
	t.Cleanup(s.Server.Close)
	return s
}

func (s *ngStub) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

func (s *ngStub) item(t *testing.T, ref string) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.reqs {
		for _, it := range r.Invoices {
			if it.Ref == ref {
				return it.Invoice
			}
		}
	}
	t.Fatalf("stub 04 received no item for ref %q", ref)
	return nil
}

func (s *ngStub) gate(f ngFix) *Gate {
	return NewGate(f.store, NewValidator(s.URL, gapiS2SToken, nil))
}

// ngWaitBlocked polls until a backend waits on a lock whose query text contains queryPart, and
// returns that query. It returns "" when finished closes first or after 10s. queryPart "" matches any.
func ngWaitBlocked(t *testing.T, super *pgxpool.Pool, queryPart string, finished <-chan struct{}) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-finished:
			return ""
		default:
		}
		var q string
		err := super.QueryRow(context.Background(),
			`SELECT query FROM pg_stat_activity
			  WHERE datname = current_database() AND pid <> pg_backend_pid()
			    AND wait_event_type = 'Lock' AND query ILIKE '%' || $1 || '%' LIMIT 1`, queryPart).Scan(&q)
		if err == nil {
			return q
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("poll pg_stat_activity: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return ""
}

// ngNoKeyUpdateNowait takes FOR NO KEY UPDATE NOWAIT on id. It conflicts with a FOR UPDATE lock
// but not with the FOR KEY SHARE an FK recheck takes, which any write to a note's row can leave.
func ngNoKeyUpdateNowait(app *pgxpool.Pool, tenant, id string) error {
	return db.WithinTenantTx(context.Background(), app, tenant, func(tx pgx.Tx) error {
		var got string
		return tx.QueryRow(context.Background(), `SELECT id FROM invoices WHERE id = $1 FOR NO KEY UPDATE NOWAIT`, id).Scan(&got)
	})
}

func ngKeys(vs []Violation) []string {
	keys := make([]string, 0, len(vs))
	for _, v := range vs {
		keys = append(keys, v.RuleKey)
	}
	return keys
}

func ngStored(t *testing.T, inv Invoice) []Violation {
	t.Helper()
	var vs []Violation
	if err := json.Unmarshal(inv.Violations, &vs); err != nil {
		t.Fatalf("decode violations %s: %v", inv.Violations, err)
	}
	return vs
}

// ngMessage returns the message of the stored violation with key, or "".
func ngMessage(vs []Violation, key string) string {
	for _, v := range vs {
		if v.RuleKey == key {
			return v.Message
		}
	}
	return ""
}

func ngOverCap() Violation {
	return Violation{RuleKey: ruleCreditNoteOverCap, Severity: "error", Message: "stale over-cap", Path: "total"}
}

func ngDecimal(t *testing.T, v any) string {
	t.Helper()
	switch x := v.(type) {
	case float64:
		return decimal.NewFromFloat(x).StringFixed(2)
	case string:
		return x
	}
	t.Fatalf("not a number: %#v", v)
	return ""
}

// --- AC 1: two credit notes cannot both pass the cap ---------------------------

func TestGateValidate_TwoCreditNotesCannotBothPassTheCap(t *testing.T) {
	f := newNGFix(t, "NG race", "100.00")
	gate := f.realGate()
	for round := 0; round < 10; round++ {
		o := f.original(fmt.Sprintf("NG-RO-%d", round), "100.00")
		ids := []string{
			f.note(fmt.Sprintf("NG-R%d-A", round), submission.CreditNote, o, "60.00"),
			f.note(fmt.Sprintf("NG-R%d-B", round), submission.CreditNote, o, "70.00"),
		}
		start := make(chan struct{})
		errs := make([]error, len(ids))
		var wg sync.WaitGroup
		for i, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, _, errs[i] = gate.Validate(f.ctx, id)
			}()
		}
		close(start)
		wg.Wait()

		validated := 0
		for i, id := range ids {
			if errs[i] != nil {
				t.Fatalf("round %d: Validate(%s): %v", round, id, errs[i])
			}
			switch f.status(id) {
			case StatusValidated:
				validated++
			case StatusDraft:
				if !hasViolation(readViolations(t, f.super, id), ruleCreditNoteOverCap) {
					t.Errorf("round %d: the draft note carries no %s", round, ruleCreditNoteOverCap)
				}
			}
		}
		if validated != 1 {
			t.Errorf("round %d: %d notes validated on one original of 100.00 (60.00 + 70.00), want exactly 1", round, validated)
		}
		credited := decimal.RequireFromString(csCredited(t, f.app, f.tenant, o, ""))
		if credited.GreaterThan(decimal.NewFromInt(100)) {
			t.Errorf("round %d: credited so far = %s, want at most 100.00", round, credited)
		}
	}
}

func TestGateValidate_TwoNotesThatFitBothPass(t *testing.T) {
	f := newNGFix(t, "NG race fit", "100.00")
	gate := f.realGate()
	ids := []string{f.credit("NG-F-A", "60.00"), f.credit("NG-F-B", "40.00")}
	start := make(chan struct{})
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, errs[i] = gate.Validate(f.ctx, id)
		}()
	}
	close(start)
	wg.Wait()
	for i, id := range ids {
		if errs[i] != nil {
			t.Fatalf("Validate(%s): %v", id, errs[i])
		}
		if got := f.status(id); got != StatusValidated {
			t.Errorf("note %s = %s, want validated (60.00 + 40.00 fits 100.00)", id, got)
		}
	}
}

// --- AC 2: the lock and the read in the verdict transaction -------------------

func TestApplyValidation_ReadsCreditedAfterTheLockWait(t *testing.T) {
	f := newNGFix(t, "NG after wait", "100.00")
	n1, n2 := f.credit("NG-W-1", "60.00"), f.credit("NG-W-2", "60.00")
	_, fp2 := f.get(n2)
	rsv := seedRuleSetVersionID(t, f.super)

	finished := make(chan struct{})
	var got Invoice
	var applyErr error
	waitingOn := ""
	err := db.WithinTenantTx(context.Background(), f.app, f.tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(f.ctx, `SELECT 1 FROM invoices WHERE id = $1 FOR UPDATE`, f.orig); err != nil {
			return err
		}
		if _, err := transitionTx(f.ctx, tx, n1, StatusDraft, StatusValidated, actorFromContext(f.ctx)); err != nil {
			return err
		}
		go func() {
			defer close(finished)
			got, applyErr = f.store.ApplyValidation(f.ctx, n2, nil, rsv, fp2)
		}()
		waitingOn = ngWaitBlocked(t, f.super, "", finished)
		return nil
	})
	if err != nil {
		t.Fatalf("test transaction: %v", err)
	}
	<-finished

	// A promotion also waits on the FK recheck of its own row, so the wait must be the locking read.
	if !strings.Contains(waitingOn, "FOR UPDATE") {
		t.Errorf("ApplyValidation waited at %q, want a wait on the original's FOR UPDATE read", waitingOn)
	}
	if applyErr != nil {
		t.Fatalf("ApplyValidation: %v", applyErr)
	}
	if got.Status != StatusDraft {
		t.Errorf("N2 = %s, want draft: N1 took 60.00 of 100.00 before the lock wait ended", got.Status)
	}
	if msg := ngMessage(ngStored(t, got), ruleCreditNoteOverCap); !strings.Contains(msg, "40.00") {
		t.Errorf("over-cap message = %q, want it to state the 40.00 still creditable", msg)
	}
}

func TestApplyValidation_HoldsTheOriginalLockUntilCommit(t *testing.T) {
	f := newNGFix(t, "NG hold", "100.00")
	n := f.credit("NG-H-1", "10.00")
	_, fp := f.get(n)
	rsv := seedRuleSetVersionID(t, f.super)

	ctx := context.Background()
	stx, err := f.super.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stx.Rollback(ctx) }()
	// Every audit write now waits: the verdict transaction stops after the original's lock.
	if _, err := stx.Exec(ctx, `LOCK TABLE audit_log IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("lock audit_log: %v", err)
	}

	finished := make(chan struct{})
	var got Invoice
	var applyErr error
	go func() {
		defer close(finished)
		got, applyErr = f.store.ApplyValidation(f.ctx, n, nil, rsv, fp)
	}()
	blocked := ngWaitBlocked(t, f.super, "audit_log", finished) != ""
	var during error
	if blocked {
		during = ngNoKeyUpdateNowait(f.app, f.tenant, f.orig)
	}
	if err := stx.Commit(ctx); err != nil {
		t.Fatalf("release audit_log: %v", err)
	}
	<-finished

	if !blocked {
		t.Fatal("ApplyValidation never waited on the audit_log lock")
	}
	var pgErr *pgconn.PgError
	if !errors.As(during, &pgErr) || pgErr.Code != "55P03" {
		t.Errorf("FOR NO KEY UPDATE NOWAIT on the original while the verdict is pending = %v, want 55P03", during)
	}
	if applyErr != nil || got.Status != StatusValidated {
		t.Errorf("ApplyValidation = (%s, %v), want validated and nil error", got.Status, applyErr)
	}
	if err := ngNoKeyUpdateNowait(f.app, f.tenant, f.orig); err != nil {
		t.Errorf("NOWAIT after commit: %v, want success", err)
	}
}

func TestApplyValidation_CapReadIsTheOneInTheVerdictTx(t *testing.T) {
	f := newNGFix(t, "NG verdict tx", "100.00")
	n1, n2 := f.credit("NG-V-1", "60.00"), f.credit("NG-V-2", "60.00")
	stub := newNGStub(t, f.super)
	stub.during = func() {
		if _, err := f.apply(n1, nil); err != nil {
			t.Errorf("stub 04 could not validate N1: %v", err)
		}
	}

	got, _, err := stub.gate(f).Validate(f.ctx, n2)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if f.status(n1) != StatusValidated {
		t.Fatal("setup: N1 did not validate during N2's evaluation")
	}
	if got.Status != StatusDraft || !hasViolation(ngStored(t, got), ruleCreditNoteOverCap) {
		t.Errorf("N2 = %s %v, want draft with %s: Evaluate saw room, the verdict transaction did not",
			got.Status, ngKeys(ngStored(t, got)), ruleCreditNoteOverCap)
	}
}

// --- AC 3: the locked verdict replaces Evaluate's note violations ---------------

func TestApplyValidation_DropsAStaleOverCapViolation(t *testing.T) {
	f := newNGFix(t, "NG stale", "100.00")
	n1, n2 := f.credit("NG-S-1", "60.00"), f.credit("NG-S-2", "60.00")

	// Evaluate saw N1 counted and reported over-cap; N1 is a draft again when the verdict is written.
	got, err := f.apply(n2, []Violation{ngOverCap()})
	if err != nil {
		t.Fatalf("ApplyValidation: %v", err)
	}
	if hasViolation(ngStored(t, got), ruleCreditNoteOverCap) || got.Status != StatusValidated {
		t.Errorf("N2 = %s %v, want validated with no over-cap key (N1 %s counts nothing)",
			got.Status, ngKeys(ngStored(t, got)), f.status(n1))
	}
}

// The stub demotes N1 while Evaluate's unlocked read already counted it. The stub also echoes
// the stale key, so a gate that stores Evaluate's list as it is fails here today.
func TestGateValidate_AStaleOverCapFromEvaluateDoesNotSurvive(t *testing.T) {
	f := newNGFix(t, "NG stale gate", "100.00")
	n1, n2 := f.credit("NG-SG-1", "60.00"), f.credit("NG-SG-2", "60.00")
	if _, err := f.apply(n1, nil); err != nil {
		t.Fatalf("setup: validate N1: %v", err)
	}
	stub := newNGStub(t, f.super)
	stub.answer = func(string) []Violation { return []Violation{ngOverCap()} }
	stub.during = func() {
		if _, err := f.store.Transition(f.ctx, n1, StatusDraft); err != nil {
			t.Errorf("stub 04 could not demote N1: %v", err)
		}
	}

	got, _, err := stub.gate(f).Validate(f.ctx, n2)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.Status != StatusValidated || hasViolation(ngStored(t, got), ruleCreditNoteOverCap) {
		t.Errorf("N2 = %s %v, want validated with no over-cap key", got.Status, ngKeys(ngStored(t, got)))
	}
}

func TestApplyValidation_KeepsEveryOtherViolation(t *testing.T) {
	f := newNGFix(t, "NG keep", "100.00")
	n := f.credit("NG-K-1", "60.00")
	warn := Violation{RuleKey: "some-warning", Severity: "warning", Message: "advisory", Path: "currency"}

	got, err := f.apply(n, []Violation{warn, ngOverCap()})
	if err != nil {
		t.Fatalf("ApplyValidation: %v", err)
	}
	if keys := ngKeys(ngStored(t, got)); !slices.Equal(keys, []string{"some-warning"}) {
		t.Errorf("stored keys = %v, want exactly [some-warning]", keys)
	}
	if got.Status != StatusValidated {
		t.Errorf("status = %s, want validated (a warning does not block)", got.Status)
	}
}

// --- AC 4: a blocked note stays a draft ------------------------------------------

func ngAuditOutcome(t *testing.T, super *pgxpool.Pool, id string) string {
	t.Helper()
	var outcome string
	if err := super.QueryRow(context.Background(),
		`SELECT payload->>'outcome' FROM audit_log
		  WHERE event = 'invoice.validated' AND payload->>'id' = $1 ORDER BY created_at DESC LIMIT 1`, id).Scan(&outcome); err != nil {
		t.Fatalf("read the invoice.validated audit row of %s: %v", id, err)
	}
	return outcome
}

func TestGateValidate_BlockedNoteStaysDraftWithItsViolation(t *testing.T) {
	super, app := dbTestPools(t)
	tenant, _, _ := seedOneStepActivePolicyTenant(t, super, "NG blocked")
	f := ngFixIn(t, super, app, tenant, "100.00")
	gate := f.realGate()

	draftOrig := f.original("NG-B-DO", "100.00")
	mustExec(t, super, `UPDATE invoices SET status = 'draft' WHERE id = $1`, draftOrig)
	noIRN := f.original("NG-B-NI", "100.00")
	mustExec(t, super, `UPDATE invoices SET irn = NULL WHERE id = $1`, noIRN)
	aNote := csNote(t, super, tenant, f.entity, "NG-B-AN", "credit_note", f.orig, "accepted", csAmount("5.00"))
	mustExec(t, super, `UPDATE invoices SET irn = 'IRN-NG-B-AN' WHERE id = $1`, aNote)

	cases := []struct {
		name, key string
		original  string
		total     string
	}{
		{"original is a draft", ruleNoteOriginalNotAccepted, draftOrig, "10.00"},
		{"original has no IRN", ruleNoteOriginalNoIRN, noIRN, "10.00"},
		{"original is a note", ruleNoteOriginalNotCommercial, aNote, "10.00"},
		{"over the cap", ruleCreditNoteOverCap, f.orig, "150.00"},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := f.credit(fmt.Sprintf("NG-B-%d", i), c.total)
			// Create refuses a note as an original, so the pointer moves as the superuser.
			mustExec(t, super, `UPDATE invoices SET original_invoice_id = $2 WHERE id = $1`, n, c.original)

			got, _, err := gate.Validate(f.ctx, n)
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if got.Status != StatusDraft {
				t.Errorf("status = %s, want draft", got.Status)
			}
			vs := ngStored(t, got)
			if !hasViolation(vs, c.key) {
				t.Errorf("stored keys = %v, want %s", ngKeys(vs), c.key)
			}
			if o := ngAuditOutcome(t, super, n); o != "blocked" {
				t.Errorf("invoice.validated outcome = %q, want blocked", o)
			}
			if runs := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1`, n); runs != 0 {
				t.Errorf("approval_runs rows = %d, want 0", runs)
			}
		})
	}

	// The policy is live: a clean note in the same tenant arms exactly one run.
	good := f.credit("NG-B-GOOD", "10.00")
	if got, _, err := gate.Validate(f.ctx, good); err != nil || got.Status != StatusValidated {
		t.Fatalf("clean note = (%s, %v), want validated", got.Status, err)
	}
	if runs := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1`, good); runs != 1 {
		t.Errorf("approval_runs rows of the clean note = %d, want 1", runs)
	}
}

func TestBatchSubmit_DoesNotQueueABlockedNote(t *testing.T) {
	f := newNGFix(t, "NG submit", "100.00")
	n := f.credit("NG-Q-1", "150.00")
	if got, _, err := f.realGate().Validate(f.ctx, n); err != nil || got.Status != StatusDraft {
		t.Fatalf("setup: over-cap note = (%s, %v), want a blocked draft", got.Status, err)
	}

	submitter := NewSubmitter(f.store, newInsertOnlyQueueClient(t, f.app))
	resp, err := submitter.BatchSubmit(f.ctx, BatchSubmitInput{InvoiceIDs: []string{n}, IdempotencyKey: "NG-Q-" + f.tenant})
	if err != nil {
		t.Fatalf("BatchSubmit: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Enqueued || resp.Results[0].Reason != batchSubmitReasonNotValidated {
		t.Errorf("results = %+v, want one skipped %q", resp.Results, batchSubmitReasonNotValidated)
	}
	if got := f.status(n); got != StatusDraft {
		t.Errorf("status = %s, want draft", got)
	}
	if jobs := countBatchSubmitJobs(t, f.app, n); jobs != 0 {
		t.Errorf("river_job rows = %d, want 0", jobs)
	}
}

// --- AC 5: valid notes promote -----------------------------------------------------

func TestGateValidate_ValidNotePromotes(t *testing.T) {
	f := newNGFix(t, "NG valid", "100.00")
	gate := f.realGate()
	credit := f.credit("NG-P-C", "60.00")
	debit := f.note("NG-P-D", submission.DebitNote, f.orig, "500.00")
	for name, id := range map[string]string{"credit note within the cap": credit, "debit note above the total": debit} {
		got, _, err := gate.Validate(f.ctx, id)
		if err != nil {
			t.Fatalf("%s: Validate: %v", name, err)
		}
		if got.Status != StatusValidated {
			t.Errorf("%s = %s %v, want validated", name, got.Status, ngKeys(ngStored(t, got)))
		}
	}
}

func TestGateValidate_RejectedAndFailedEarlierNotesLeaveRoom(t *testing.T) {
	f := newNGFix(t, "NG room", "100.00")
	for i, st := range []string{"rejected", "failed", "draft"} {
		csNote(t, f.super, f.tenant, f.entity, fmt.Sprintf("NG-RM-%d", i), "credit_note", f.orig, st, csAmount("60.00"))
	}
	n := f.credit("NG-RM-N", "100.00")

	got, _, err := f.realGate().Validate(f.ctx, n)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("note of 100.00 = %s %v, want validated: rejected, failed and draft notes do not count", got.Status, ngKeys(ngStored(t, got)))
	}
}

// --- AC 6: Evaluate sends the facts and writes nothing -----------------------------

func TestGateEvaluate_SendsTheFactsAndReturnsTheViolations(t *testing.T) {
	f := newNGFix(t, "NG facts", "100.00")
	csNote(t, f.super, f.tenant, f.entity, "NG-E-A", "credit_note", f.orig, "accepted", csAmount("30.00"))
	n := f.credit("NG-E-N", "90.00")
	inv, _ := f.get(n)
	before := snapshotInvoiceGateState(t, f.super, n)
	auditBefore := mustCount(t, f.super, `SELECT count(*) FROM audit_log WHERE tenant_id = $1`, f.tenant)
	stub := newNGStub(t, f.super)

	res, err := stub.gate(f).Evaluate(f.ctx, []EvalItem{{Ref: n, Invoice: inv}})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	p := stub.item(t, n)
	if p["document_type"] != "credit_note" {
		t.Errorf("document_type = %#v, want credit_note", p["document_type"])
	}
	if br, _ := p["billing_reference"].(map[string]any); br["irn"] != "IRN-NG-O" || br["issue_date"] != "2026-03-04" {
		t.Errorf("billing_reference = %#v", p["billing_reference"])
	}
	if p["original_total"] == nil || ngDecimal(t, p["original_total"]) != "100.00" {
		t.Errorf("original_total = %#v, want 100", p["original_total"])
	}
	if p["credited_before"] == nil || ngDecimal(t, p["credited_before"]) != "30.00" {
		t.Errorf("credited_before = %#v, want 30", p["credited_before"])
	}
	if !hasViolation(res.ByRef[n], ruleCreditNoteOverCap) {
		t.Errorf("ByRef keys = %v, want %s (30.00 + 90.00 > 100.00)", ngKeys(res.ByRef[n]), ruleCreditNoteOverCap)
	}
	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, f.super, n), "evaluated note")
	if rows := mustCount(t, f.super, `SELECT count(*) FROM audit_log WHERE tenant_id = $1`, f.tenant); rows != auditBefore {
		t.Errorf("audit rows = %d, want %d: Evaluate writes nothing", rows, auditBefore)
	}
}

func TestGateEvaluate_AFactsReadFailureSendsNothing(t *testing.T) {
	f := newNGFix(t, "NG read fails", "100.00")
	closed, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	stub := newNGStub(t, f.super)
	gate := &Gate{store: NewStore(closed), validator: NewValidator(stub.URL, gapiS2SToken, nil), customRules: noCustomRules}
	orig := f.orig
	note := Invoice{ID: "n", InvoiceNumber: "NG-X", DocumentType: submission.CreditNote, OriginalInvoiceID: &orig, Total: strPtr("10.00")}

	if _, err := gate.Evaluate(f.ctx, []EvalItem{{Ref: "n", Invoice: note}}); err == nil {
		t.Error("Evaluate returned no error although the facts read failed")
	}
	if n := stub.calls(); n != 0 {
		t.Errorf("04 received %d requests, want 0: an outage is not a verdict", n)
	}
}

// --- AC 7: re-validation --------------------------------------------------------------

func TestRevalidate_ValidatedNoteAtTheCapStaysValidated(t *testing.T) {
	f := newNGFix(t, "NG reval cap", "100.00")
	n := f.credit("NG-RV-1", "100.00")
	gate := f.realGate()
	if got, _, err := gate.Validate(f.ctx, n); err != nil || got.Status != StatusValidated {
		t.Fatalf("setup: note at the cap = (%s, %v), want validated", got.Status, err)
	}

	res, err := RevalidateActive(context.Background(), f.app, f.store, gate, f.tenant, false)
	if err != nil {
		t.Fatalf("RevalidateActive: %v", err)
	}
	if res.Examined != 1 || res.Clean != 1 || res.Demoted != 0 {
		t.Errorf("result = %+v, want 1 examined, 1 clean, 0 demoted: the note excludes itself from its sum", res)
	}
	if got := f.status(n); got != StatusValidated {
		t.Errorf("status = %s, want validated", got)
	}
}

func TestRevalidate_OverCapValidatedNoteDemotes(t *testing.T) {
	f := newNGFix(t, "NG reval over", "100.00")
	csNote(t, f.super, f.tenant, f.entity, "NG-RO-A", "credit_note", f.orig, "accepted", csAmount("60.00"))
	n := f.credit("NG-RO-N", "60.00")
	mustExec(t, f.super, `UPDATE invoices SET status = 'validated' WHERE id = $1`, n)

	res, err := RevalidateActive(context.Background(), f.app, f.store, f.realGate(), f.tenant, false)
	if err != nil {
		t.Fatalf("RevalidateActive: %v", err)
	}
	if res.Demoted != 1 {
		t.Errorf("Demoted = %d, want 1 (60.00 + 60.00 > 100.00)", res.Demoted)
	}
	if got := f.status(n); got != StatusDraft {
		t.Errorf("status = %s, want draft", got)
	}
	if !hasViolation(readViolations(t, f.super, n), ruleCreditNoteOverCap) {
		t.Errorf("stored keys = %v, want %s", ngKeys(readViolations(t, f.super, n)), ruleCreditNoteOverCap)
	}
}

// --- AC 8: ValidateBatch reports the locked verdict ------------------------------------

func TestValidateBatch_CountsTheLockedVerdict(t *testing.T) {
	f := newNGFix(t, "NG batch", "100.00")
	n1, n2 := f.credit("NG-VB-1", "60.00"), f.credit("NG-VB-2", "60.00")
	stub := newNGStub(t, f.super)
	stub.during = func() {
		if _, err := f.apply(n1, nil); err != nil {
			t.Errorf("stub 04 could not validate N1: %v", err)
		}
	}
	inv2, _ := f.get(n2)

	out, err := stub.gate(f).ValidateBatch(f.ctx, []Invoice{inv2})
	if err != nil {
		t.Fatalf("ValidateBatch: %v", err)
	}
	if f.status(n1) != StatusValidated {
		t.Fatal("setup: N1 did not validate during the batch")
	}
	if out.Clean != 0 || out.WithViolations != 1 {
		t.Errorf("Clean/WithViolations = %d/%d, want 0/1", out.Clean, out.WithViolations)
	}
	if !hasViolation(out.ByID[n2], ruleCreditNoteOverCap) {
		t.Errorf("ByID keys = %v, want %s from the locked verdict", ngKeys(out.ByID[n2]), ruleCreditNoteOverCap)
	}
	if got := f.status(n2); got != StatusDraft {
		t.Errorf("N2 = %s, want draft", got)
	}
}

// The stub echoes the stale key as well, so a batch that reports Evaluate's list fails today.
func TestValidateBatch_ByIDDropsAStaleOverCap(t *testing.T) {
	f := newNGFix(t, "NG batch stale", "100.00")
	n1, n2 := f.credit("NG-VS-1", "60.00"), f.credit("NG-VS-2", "60.00")
	if _, err := f.apply(n1, nil); err != nil {
		t.Fatalf("setup: validate N1: %v", err)
	}
	stub := newNGStub(t, f.super)
	stub.answer = func(string) []Violation { return []Violation{ngOverCap()} }
	stub.during = func() {
		if _, err := f.store.Transition(f.ctx, n1, StatusDraft); err != nil {
			t.Errorf("stub 04 could not demote N1: %v", err)
		}
	}
	inv2, _ := f.get(n2)

	out, err := stub.gate(f).ValidateBatch(f.ctx, []Invoice{inv2})
	if err != nil {
		t.Fatalf("ValidateBatch: %v", err)
	}
	if out.Clean != 1 || out.WithViolations != 0 {
		t.Errorf("Clean/WithViolations = %d/%d, want 1/0", out.Clean, out.WithViolations)
	}
	if hasViolation(out.ByID[n2], ruleCreditNoteOverCap) {
		t.Errorf("ByID keys = %v, want no %s", ngKeys(out.ByID[n2]), ruleCreditNoteOverCap)
	}
	if got := f.status(n2); got != StatusValidated {
		t.Errorf("N2 = %s, want validated", got)
	}
}

// --- AC 9: a commercial invoice is unchanged ----------------------------------------------

func TestApplyValidation_CommercialVerdictIsUnchanged(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenant := seedTenant(t, super, "NG commercial")
	entity := seedEntity(t, super, tenant, "E1")
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenant})
	rsv := seedRuleSetVersionID(t, super)

	cases := []struct {
		name       string
		vs         []Violation
		wantStatus Status
	}{
		{"one warning promotes", []Violation{{RuleKey: "w-1", Severity: "warning", Message: "m", Path: "currency"}}, StatusValidated},
		{"one error blocks", []Violation{{RuleKey: "e-1", Severity: "error", Message: "m", Path: "currency"}}, StatusDraft},
	}
	for i, tc := range cases {
		inv, err := store.Create(c, CreateInput{EntityID: entity, InvoiceNumber: fmt.Sprintf("NG-C-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		got, err := store.ApplyValidation(c, inv.ID, tc.vs, rsv, contentFingerprint(inv, inv.LineItems))
		if err != nil {
			t.Fatalf("%s: ApplyValidation: %v", tc.name, err)
		}
		if got.Status != tc.wantStatus {
			t.Errorf("%s: status = %s, want %s", tc.name, got.Status, tc.wantStatus)
		}
		if stored := ngKeys(ngStored(t, got)); !slices.Equal(stored, ngKeys(tc.vs)) {
			t.Errorf("%s: stored keys = %v, want exactly %v", tc.name, stored, ngKeys(tc.vs))
		}
	}
}

func TestGateEvaluate_CommercialBatchNeverTouchesTheStore(t *testing.T) {
	want := map[string][]Violation{
		"a": {{RuleKey: "r-a", Severity: "warning", Message: "m"}},
		"b": {},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req validateBatchRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		results := make([]validateBatchItemResult, len(req.Invoices))
		for i, it := range req.Invoices {
			results[i] = validateBatchItemResult{Ref: it.Ref, Violations: want[it.Ref]}
		}
		writeValidateResponse(t, w, "00000000-0000-0000-0000-000000000000", results)
	}))
	t.Cleanup(srv.Close)
	gate := &Gate{store: nil, validator: NewValidator(srv.URL, gapiS2SToken, nil), customRules: noCustomRules}

	res, err := gate.Evaluate(context.Background(), []EvalItem{
		{Ref: "a", Invoice: Invoice{InvoiceNumber: "a", DocumentType: submission.CommercialInvoice}},
		{Ref: "b", Invoice: Invoice{InvoiceNumber: "b"}},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(res.ByRef) != 2 || !slices.Equal(ngKeys(res.ByRef["a"]), []string{"r-a"}) || len(res.ByRef["b"]) != 0 {
		t.Errorf("ByRef = %+v, want the stub's answer", res.ByRef)
	}
}

// --- AC 10: an original hidden from the member --------------------------------------------

func TestApplyValidation_OriginalOutsideTheMembersClientsIsMissing(t *testing.T) {
	f := newCSFixture(t, "NG hidden")
	n := csNote(t, f.super, f.tenant, f.entA, "NG-HID", "credit_note", f.invB, "draft", csAmount("10.00"))
	stub := newNGStub(t, f.super)
	gate := NewGate(f.store, NewValidator(stub.URL, gapiS2SToken, nil))

	// Another session holds client B's invoice. A hidden row is skipped by a locking read, so
	// Validate must not wait. NO KEY UPDATE: a plain FOR UPDATE also blocks any promotion's FK recheck.
	ctx := context.Background()
	holder, err := f.super.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `SELECT 1 FROM invoices WHERE id = $1 FOR NO KEY UPDATE`, f.invB); err != nil {
		t.Fatal(err)
	}
	limited, cancel := context.WithTimeout(f.limited, 5*time.Second)
	defer cancel()

	got, _, err := gate.Validate(limited, n)
	if err != nil {
		t.Fatalf("Validate as the limited member: %v", err)
	}
	if got.Status != StatusDraft || !hasViolation(ngStored(t, got), ruleNoteOriginalMissing) {
		t.Errorf("limited member: %s %v, want draft with %s", got.Status, ngKeys(ngStored(t, got)), ruleNoteOriginalMissing)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// The admin sees client B, so the same note is judged on the original's facts instead.
	adm, _, err := gate.Validate(f.admin, n)
	if err != nil {
		t.Fatalf("Validate as admin: %v", err)
	}
	if hasViolation(ngStored(t, adm), ruleNoteOriginalMissing) || !hasViolation(ngStored(t, adm), ruleNoteOriginalNotAccepted) {
		t.Errorf("admin: %v, want %s and no %s", ngKeys(ngStored(t, adm)), ruleNoteOriginalNotAccepted, ruleNoteOriginalMissing)
	}
}

// --- AC 12: an in-memory note with no original ----------------------------------------------

func TestGateEvaluate_NoteWithNoOriginalIDIsMissing(t *testing.T) {
	f := newNGFix(t, "NG dry run", "100.00")
	stub := newNGStub(t, f.super)
	note := Invoice{InvoiceNumber: "NG-DRY", DocumentType: submission.CreditNote, Total: strPtr("10.00")}

	res, err := stub.gate(f).Evaluate(f.ctx, []EvalItem{{Ref: "NG-DRY", Invoice: note}})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !hasViolation(res.ByRef["NG-DRY"], ruleNoteOriginalMissing) {
		t.Errorf("ByRef keys = %v, want %s", ngKeys(res.ByRef["NG-DRY"]), ruleNoteOriginalMissing)
	}
	if n := stub.calls(); n != 1 {
		t.Errorf("04 received %d requests, want 1", n)
	}
}

// --- AC 13: a debit note takes no lock on the original ---------------------------------------

func TestApplyValidation_DebitNoteTakesNoLockOnTheOriginal(t *testing.T) {
	f := newNGFix(t, "NG debit", "100.00")
	d := f.note("NG-DB-1", submission.DebitNote, f.orig, "500.00")
	_, fp := f.get(d)
	rsv := seedRuleSetVersionID(t, f.super)
	bounded, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()

	var got Invoice
	err := db.WithinTenantTx(context.Background(), f.app, f.tenant, func(tx pgx.Tx) error {
		// NO KEY UPDATE, not FOR UPDATE: the promotion's own FK recheck waits on a FOR UPDATE lock
		// whatever the code does, so only a FOR UPDATE request by the code can be told apart.
		if _, err := tx.Exec(f.ctx, `SELECT 1 FROM invoices WHERE id = $1 FOR NO KEY UPDATE`, f.orig); err != nil {
			return err
		}
		var err error
		got, err = f.store.ApplyValidation(bounded, d, nil, rsv, fp)
		return err
	})
	if err != nil {
		t.Fatalf("ApplyValidation of a debit note while the original is locked: %v, want nil", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("debit note = %s, want validated", got.Status)
	}
}
