package invoice

// ENGI-04-06 Mode A: the re-check of the invoices a rule-set version covers, and the history
// cause Migration C adds. Fixture versions are dated in year 3001 and `today` is injected, so
// the real versions never interfere. Versions are seeded before tenants: LIFO cleanup deletes
// the invoices that stamp a version before the version itself.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

const buyerNameRuleKey = "buyer-name-required"

// seedBuyerNameVersion is seedDatedRuleSetVersion's twin whose one rule is enabled: it blocks
// every invoice that has no buyer name.
func seedBuyerNameVersion(t *testing.T, super *pgxpool.Pool, from string) (id string, version int) {
	t.Helper()
	ctx := context.Background()
	if err := super.QueryRow(ctx,
		`INSERT INTO rule_set_versions (version, sealed, notes)
		 SELECT GREATEST(COALESCE(MAX(version), 0), 950000) + 1, false, 'qa-fixture:internal/invoice/recheck_test.go'
		 FROM rule_set_versions RETURNING id, version`,
	).Scan(&id, &version); err != nil {
		t.Fatalf("seed rule_set_versions: %v", err)
	}
	if _, err := super.Exec(ctx,
		`INSERT INTO rules (rule_set_version_id, key, type, target, params, severity, message, scope, enabled)
		 VALUES ($1, $2, 'required', 'buyer.name', '{}'::jsonb, 'error', 'Buyer name is required.', 'document', true)`,
		id, buyerNameRuleKey,
	); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	if _, err := super.Exec(ctx,
		`UPDATE rule_set_versions SET sealed = true, effective_from = $2::date WHERE id = $1`, id, from,
	); err != nil {
		t.Fatalf("seal and date version: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		tx, err := super.Begin(ctx)
		if err != nil {
			t.Errorf("fixture cleanup: begin: %v", err)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		for _, q := range []string{
			`ALTER TABLE rule_set_versions DISABLE TRIGGER USER`,
			`ALTER TABLE rules DISABLE TRIGGER USER`,
			`DELETE FROM rule_set_versions WHERE id = '` + id + `'`,
			`ALTER TABLE rules ENABLE TRIGGER USER`,
			`ALTER TABLE rule_set_versions ENABLE TRIGGER USER`,
		} {
			if _, err := tx.Exec(ctx, q); err != nil {
				t.Errorf("fixture cleanup: %s: %v", q, err)
				return
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Errorf("fixture cleanup: commit: %v", err)
		}
	})
	return id, version
}

// requireVersionFor fails the test as a fixture error when the selection function does not
// map date to want: a red below must come from the code under test, not from the fixture.
func requireVersionFor(t *testing.T, super *pgxpool.Pool, date, want string) {
	t.Helper()
	var got string
	if err := super.QueryRow(context.Background(), `SELECT rule_set_version_for($1::date)::text`, date).Scan(&got); err != nil {
		t.Fatalf("fixture: rule_set_version_for(%s): %v", date, err)
	}
	if got != want {
		t.Fatalf("fixture: rule_set_version_for(%s) = %s, want %s", date, got, want)
	}
}

// seedRecheckInvoice inserts an invoice as the superuser at the given status. A nil
// issueDate or buyerName leaves the column NULL.
func seedRecheckInvoice(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number string, status Status, issueDate, buyerName *string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, status, issue_date, buyer_name)
		 VALUES ($1, $2, $3, $4, $5::date, $6) RETURNING id`,
		tenantID, entityID, number, string(status), issueDate, buyerName,
	).Scan(&id); err != nil {
		t.Fatalf("seed invoice %s: %v", number, err)
	}
	return id
}

func realRecheckGate(t *testing.T, app *pgxpool.Pool, store *Store) *Gate {
	t.Helper()
	return NewGate(store, NewValidator(startInProcess04(t, app).URL, gapiS2SToken, nil))
}

// newBlockingStub blocks every item with the buyer-name violation stamped with the given
// version on its first failFrom-1 calls, then answers 503. calls counts every request.
func newBlockingStub(t *testing.T, bID string, bVersion, failFrom int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		if n >= failFrom {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req validateBatchRequest
		_ = json.Unmarshal(body, &req)
		results := make([]validateBatchItemResult, len(req.Invoices))
		for i, it := range req.Invoices {
			results[i] = validateBatchItemResult{
				Ref:            it.Ref,
				Violations:     []Violation{{RuleKey: buyerNameRuleKey, Severity: "error", Message: "Buyer name is required.", Path: "buyer.name"}},
				RuleSetVersion: bVersion, RuleSetVersionID: bID,
			}
		}
		writeValidateResponse(t, w, bID, results)
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

func demotionHistoryRows(t *testing.T, super *pgxpool.Pool, invoiceID string) int {
	t.Helper()
	return mustCount(t, super,
		`SELECT count(*) FROM invoice_status_history
		  WHERE invoice_id = $1 AND from_status = 'validated' AND to_status = 'draft' AND actor = 'revalidate-rule-set'`, invoiceID)
}

// demotedAuditPayloads returns the invoice.validated payloads with outcome "demoted" for one invoice.
func demotedAuditPayloads(t *testing.T, app *pgxpool.Pool, tenantID, invoiceID string) []map[string]any {
	t.Helper()
	ctx := context.Background()
	var out []map[string]any
	if err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT payload FROM audit_log
			  WHERE event = 'invoice.validated' AND payload->>'id' = $1 AND payload->>'outcome' = 'demoted'`, invoiceID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("read demoted audit rows for %s: %v", invoiceID, err)
	}
	return out
}

func dueIDs(vs []DueVersion) []string {
	ids := make([]string, len(vs))
	for i, v := range vs {
		ids[i] = v.ID
	}
	return ids
}

// --- AC 1: DueRechecks --------------------------------------------------------

func TestDueRechecks_InForceAndUnmarkedOnly(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	aID, aVer := seedDatedRuleSetVersion(t, super, "3001-01-01")
	bID, bVer := seedDatedRuleSetVersion(t, super, "3001-06-01")
	cID, _ := seedDatedRuleSetVersion(t, super, "3001-03-01")
	if _, err := super.Exec(ctx, `INSERT INTO rule_set_version_rechecks (rule_set_version_id) VALUES ($1)`, cID); err != nil {
		t.Fatalf("mark fixture C: %v", err)
	}

	got, err := DueRechecks(ctx, app, day(3001, 6, 1))
	if err != nil {
		t.Fatalf("DueRechecks(3001-06-01): %v", err)
	}
	if want := []string{aID, bID}; !slices.Equal(dueIDs(got), want) {
		t.Fatalf("DueRechecks(3001-06-01) ids = %v, want [A B] = %v (oldest first; marked C and the marked real versions excluded)", dueIDs(got), want)
	}
	if got[0].Version != aVer || got[1].Version != bVer {
		t.Errorf("versions = %d, %d, want %d, %d", got[0].Version, got[1].Version, aVer, bVer)
	}
	if got[0].EffectiveFrom.Format("2006-01-02") != "3001-01-01" || got[1].EffectiveFrom.Format("2006-01-02") != "3001-06-01" {
		t.Errorf("EffectiveFrom = %s, %s, want 3001-01-01, 3001-06-01", got[0].EffectiveFrom, got[1].EffectiveFrom)
	}

	// B starts on 3001-06-01: the day before, it is scheduled, not due.
	got, err = DueRechecks(ctx, app, day(3001, 5, 31))
	if err != nil {
		t.Fatalf("DueRechecks(3001-05-31): %v", err)
	}
	if want := []string{aID}; !slices.Equal(dueIDs(got), want) {
		t.Errorf("DueRechecks(3001-05-31) ids = %v, want only A = %v (scheduled B is not due)", dueIDs(got), want)
	}

	got, err = DueRechecks(ctx, app, day(3000, 12, 31))
	if err != nil {
		t.Fatalf("DueRechecks(3000-12-31): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("DueRechecks(3000-12-31) = %v, want none (every fixture is scheduled)", dueIDs(got))
	}
}

func TestDueRechecks_NothingDueAtHead(t *testing.T) {
	super, app := dbTestPools(t)
	today := time.Now().UTC().Truncate(24 * time.Hour)

	// Positive control: a dated, marked version exists, so an empty answer is the marker at work.
	if n := mustCount(t, super,
		`SELECT count(*) FROM rule_set_versions v JOIN rule_set_version_rechecks r ON r.rule_set_version_id = v.id
		  WHERE v.effective_from <= $1::date`, today); n == 0 {
		t.Fatal("fixture: no dated, marked version in force today (v4's marker row is missing)")
	}
	got, err := DueRechecks(context.Background(), app, today)
	if err != nil {
		t.Fatalf("DueRechecks(today): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("DueRechecks(today) = %v, want none -- every real version in force is marked", dueIDs(got))
	}
}

// --- AC 2: which invoices are covered ------------------------------------------

func TestRecheckCovered_DemotesOnlyCoveredInvoicesThatNowFail(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	bID, bVer := seedBuyerNameVersion(t, super, "3001-06-01")
	tenantID := seedTenant(t, super, "RC-COVER tenant")
	entityID := seedEntity(t, super, tenantID, "RC-COVER entity")
	requireVersionFor(t, super, "3001-07-01", bID)

	failing := seedRecheckInvoice(t, super, tenantID, entityID, "RC-FAIL", StatusValidated, strPtr("3001-07-01"), nil)
	passing := seedRecheckInvoice(t, super, tenantID, entityID, "RC-PASS", StatusValidated, strPtr("3001-07-01"), strPtr("Buyer Ltd"))
	uncovered := seedRecheckInvoice(t, super, tenantID, entityID, "RC-OLD", StatusValidated, strPtr("2026-09-01"), nil)
	passingBefore := snapshotInvoiceGateState(t, super, passing)
	uncoveredBefore := snapshotInvoiceGateState(t, super, uncovered)

	res, err := RecheckCovered(ctx, app, store, realRecheckGate(t, app, store), tenantID,
		DueVersion{ID: bID, Version: bVer, EffectiveFrom: day(3001, 6, 1)}, day(3001, 6, 1))
	if err != nil {
		t.Fatalf("RecheckCovered: %v", err)
	}
	if res.Examined != 2 || res.Demoted != 1 || res.Clean != 1 {
		t.Errorf("result = %+v, want Examined 2, Demoted 1, Clean 1 (the 2026-dated invoice is not covered)", res)
	}
	if got := readInvoiceStatus(t, super, failing); got != StatusDraft {
		t.Errorf("covered failing invoice status = %q, want draft", got)
	}
	if got := stampedVersionID(t, super, failing); got != bID {
		t.Errorf("covered failing invoice stamp = %s, want B %s", got, bID)
	}
	assertGateSnapshotUnchanged(t, passingBefore, snapshotInvoiceGateState(t, super, passing), "covered passing invoice")
	assertGateSnapshotUnchanged(t, uncoveredBefore, snapshotInvoiceGateState(t, super, uncovered), "uncovered invoice")
}

func TestRecheckCovered_SkipsInvoicesAlreadyStampedWithTheVersion(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	bID, bVer := seedBuyerNameVersion(t, super, "3001-06-01")
	tenantID := seedTenant(t, super, "RC-STAMPED tenant")
	entityID := seedEntity(t, super, tenantID, "RC-STAMPED entity")
	requireVersionFor(t, super, "3001-07-01", bID)
	inv := seedRecheckInvoice(t, super, tenantID, entityID, "RC-STAMPED", StatusValidated, strPtr("3001-07-01"), nil)
	if _, err := super.Exec(ctx, `UPDATE invoices SET rule_set_version_id = $1 WHERE id = $2`, bID, inv); err != nil {
		t.Fatalf("stamp fixture: %v", err)
	}
	gate := realRecheckGate(t, app, store)
	v := DueVersion{ID: bID, Version: bVer, EffectiveFrom: day(3001, 6, 1)}

	res, err := RecheckCovered(ctx, app, store, gate, tenantID, v, day(3001, 6, 1))
	if err != nil {
		t.Fatalf("RecheckCovered: %v", err)
	}
	if res.Examined != 0 {
		t.Errorf("Examined = %d, want 0 for an invoice already stamped with the version", res.Examined)
	}
	if got := readInvoiceStatus(t, super, inv); got != StatusValidated {
		t.Errorf("status = %q, want validated", got)
	}

	// Positive control: the same invoice without the stamp is examined and demoted.
	if _, err := super.Exec(ctx, `UPDATE invoices SET rule_set_version_id = NULL WHERE id = $1`, inv); err != nil {
		t.Fatalf("clear stamp: %v", err)
	}
	res, err = RecheckCovered(ctx, app, store, gate, tenantID, v, day(3001, 6, 1))
	if err != nil {
		t.Fatalf("RecheckCovered (unstamped): %v", err)
	}
	if res.Examined != 1 {
		t.Errorf("unstamped Examined = %d, want 1 (a NULL stamp is distinct from B)", res.Examined)
	}
}

func TestRecheckCovered_UndatedInvoiceBelongsToTodaysVersion(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	aID, aVer := seedDatedRuleSetVersion(t, super, "3001-01-01")
	bID, bVer := seedBuyerNameVersion(t, super, "3001-06-01")
	tenantID := seedTenant(t, super, "RC-UNDATED tenant")
	entityID := seedEntity(t, super, tenantID, "RC-UNDATED entity")
	requireVersionFor(t, super, "3001-06-01", bID)
	requireVersionFor(t, super, "3001-05-31", aID)
	inv := seedRecheckInvoice(t, super, tenantID, entityID, "RC-UNDATED", StatusValidated, nil, nil)
	setBuyerTIN(t, super, inv, "87654321-0002")
	// A clean stub keeps the invoice validated, so both runs see the same candidate.
	srv := newTINValidatorServer(t, bID)
	gate := NewGate(store, NewValidator(srv.URL, revalidateS2SToken, nil))
	today := day(3001, 6, 1)

	res, err := RecheckCovered(ctx, app, store, gate, tenantID, DueVersion{ID: aID, Version: aVer, EffectiveFrom: day(3001, 1, 1)}, today)
	if err != nil {
		t.Fatalf("RecheckCovered(A): %v", err)
	}
	if res.Examined != 0 {
		t.Errorf("run for A: Examined = %d, want 0 -- today's version is B, not A", res.Examined)
	}
	res, err = RecheckCovered(ctx, app, store, gate, tenantID, DueVersion{ID: bID, Version: bVer, EffectiveFrom: day(3001, 6, 1)}, today)
	if err != nil {
		t.Fatalf("RecheckCovered(B): %v", err)
	}
	if res.Examined != 1 {
		t.Errorf("run for B: Examined = %d, want 1 -- an undated invoice belongs to the version in force today", res.Examined)
	}
}

func TestRecheckCovered_BoundaryDays(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	bID, bVer := seedBuyerNameVersion(t, super, "3001-06-01")
	tenantID := seedTenant(t, super, "RC-BOUNDARY tenant")
	entityID := seedEntity(t, super, tenantID, "RC-BOUNDARY entity")
	requireVersionFor(t, super, "3001-06-01", bID)
	dayBefore := seedRecheckInvoice(t, super, tenantID, entityID, "RC-0531", StatusValidated, strPtr("3001-05-31"), nil)
	startDay := seedRecheckInvoice(t, super, tenantID, entityID, "RC-0601", StatusValidated, strPtr("3001-06-01"), nil)

	res, err := RecheckCovered(ctx, app, store, realRecheckGate(t, app, store), tenantID,
		DueVersion{ID: bID, Version: bVer, EffectiveFrom: day(3001, 6, 1)}, day(3001, 6, 1))
	if err != nil {
		t.Fatalf("RecheckCovered: %v", err)
	}
	if res.Examined != 1 {
		t.Errorf("Examined = %d, want 1 (only the invoice dated on the start day)", res.Examined)
	}
	if got := readInvoiceStatus(t, super, startDay); got != StatusDraft {
		t.Errorf("invoice dated 3001-06-01 status = %q, want draft (examined and failing)", got)
	}
	if got := readInvoiceStatus(t, super, dayBefore); got != StatusValidated {
		t.Errorf("invoice dated 3001-05-31 status = %q, want validated (belongs to the version before B)", got)
	}
}

// --- AC 3: the demotion tells the user -----------------------------------------

func TestRecheckCovered_DemotionWritesHistoryAndAudit(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	bID, bVer := seedBuyerNameVersion(t, super, "3001-06-01")
	tenantID := seedTenant(t, super, "RC-AUDIT tenant")
	entityID := seedEntity(t, super, tenantID, "RC-AUDIT entity")
	requireVersionFor(t, super, "3001-07-01", bID)
	inv := seedRecheckInvoice(t, super, tenantID, entityID, "RC-AUDIT-1", StatusValidated, strPtr("3001-07-01"), nil)

	res, err := RecheckCovered(ctx, app, store, realRecheckGate(t, app, store), tenantID,
		DueVersion{ID: bID, Version: bVer, EffectiveFrom: day(3001, 6, 1)}, day(3001, 6, 1))
	if err != nil {
		t.Fatalf("RecheckCovered: %v", err)
	}
	if res.Demoted != 1 {
		t.Fatalf("Demoted = %d, want 1", res.Demoted)
	}

	if got := readInvoiceStatus(t, super, inv); got != StatusDraft {
		t.Errorf("status = %q, want draft", got)
	}
	vs := readViolations(t, super, inv)
	if len(vs) == 0 || vs[0].RuleKey != buyerNameRuleKey {
		t.Errorf("stored violations = %+v, want the %s violation", vs, buyerNameRuleKey)
	}
	if n := demotionHistoryRows(t, super, inv); n != 1 {
		t.Errorf("validated->draft history rows by revalidate-rule-set = %d, want 1", n)
	}
	audits := demotedAuditPayloads(t, app, tenantID, inv)
	if len(audits) != 1 {
		t.Fatalf("invoice.validated demoted audit rows = %d, want 1", len(audits))
	}
	if audits[0]["rule_set_version_id"] != bID || audits[0]["invoice_number"] != "RC-AUDIT-1" || audits[0]["outcome"] != "demoted" {
		t.Errorf("audit payload = %v, want rule_set_version_id %s, invoice_number RC-AUDIT-1, outcome demoted", audits[0], bID)
	}
}

// --- AC 4: per tenant ----------------------------------------------------------

func TestRecheckCovered_NeverTouchesAnotherTenantsInvoices(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	bID, bVer := seedBuyerNameVersion(t, super, "3001-06-01")
	t1 := seedTenant(t, super, "RC-T1 tenant")
	t2 := seedTenant(t, super, "RC-T2 tenant")
	e1 := seedEntity(t, super, t1, "RC-T1 entity")
	e2 := seedEntity(t, super, t2, "RC-T2 entity")
	requireVersionFor(t, super, "3001-07-01", bID)
	inv1 := seedRecheckInvoice(t, super, t1, e1, "RC-T1-1", StatusValidated, strPtr("3001-07-01"), nil)
	inv2 := seedRecheckInvoice(t, super, t2, e2, "RC-T2-1", StatusValidated, strPtr("3001-07-01"), nil)
	before := snapshotInvoiceGateState(t, super, inv2)

	res, err := RecheckCovered(ctx, app, store, realRecheckGate(t, app, store), t1,
		DueVersion{ID: bID, Version: bVer, EffectiveFrom: day(3001, 6, 1)}, day(3001, 6, 1))
	if err != nil {
		t.Fatalf("RecheckCovered(T1): %v", err)
	}
	if res.Examined != 1 || res.Demoted != 1 {
		t.Errorf("result = %+v, want Examined 1, Demoted 1 (only T1's invoice)", res)
	}
	if got := readInvoiceStatus(t, super, inv1); got != StatusDraft {
		t.Errorf("T1 invoice status = %q, want draft", got)
	}
	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, inv2), "T2 invoice")
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv2); n != 0 {
		t.Errorf("T2 history rows = %d, want 0", n)
	}
}

// --- AC 5: only validated; outages --------------------------------------------

func TestRecheckCovered_IgnoresNonValidatedStatuses(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	bID, bVer := seedBuyerNameVersion(t, super, "3001-06-01")
	tenantID := seedTenant(t, super, "RC-STATUS tenant")
	entityID := seedEntity(t, super, tenantID, "RC-STATUS entity")
	requireVersionFor(t, super, "3001-07-01", bID)
	others := map[Status]string{}
	for _, st := range []Status{StatusDraft, StatusQueued, StatusSubmitted} {
		others[st] = seedRecheckInvoice(t, super, tenantID, entityID, "RC-"+string(st), st, strPtr("3001-07-01"), nil)
	}
	gate := realRecheckGate(t, app, store)
	v := DueVersion{ID: bID, Version: bVer, EffectiveFrom: day(3001, 6, 1)}

	res, err := RecheckCovered(ctx, app, store, gate, tenantID, v, day(3001, 6, 1))
	if err != nil {
		t.Fatalf("RecheckCovered: %v", err)
	}
	if res.Examined != 0 {
		t.Errorf("Examined = %d, want 0 for draft, queued and submitted invoices", res.Examined)
	}
	for st, id := range others {
		if got := readInvoiceStatus(t, super, id); got != st {
			t.Errorf("%s invoice status = %q after the re-check, want unchanged", st, got)
		}
	}

	// Positive control: a validated sibling with the same date and content is examined.
	seedRecheckInvoice(t, super, tenantID, entityID, "RC-validated", StatusValidated, strPtr("3001-07-01"), nil)
	res, err = RecheckCovered(ctx, app, store, gate, tenantID, v, day(3001, 6, 1))
	if err != nil {
		t.Fatalf("RecheckCovered (with validated sibling): %v", err)
	}
	if res.Examined != 1 {
		t.Errorf("Examined = %d with one validated sibling, want 1", res.Examined)
	}
}

func TestRecheckCovered_UpstreamOutageWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	bID, bVer := seedBuyerNameVersion(t, super, "3001-06-01")
	tenantID := seedTenant(t, super, "RC-OUTAGE tenant")
	entityID := seedEntity(t, super, tenantID, "RC-OUTAGE entity")
	requireVersionFor(t, super, "3001-07-01", bID)
	inv := seedRecheckInvoice(t, super, tenantID, entityID, "RC-OUTAGE-1", StatusValidated, strPtr("3001-07-01"), nil)
	before := snapshotInvoiceGateState(t, super, inv)
	srv, calls := newBlockingStub(t, bID, bVer, 1)

	_, err := RecheckCovered(ctx, app, store, NewGate(store, NewValidator(srv.URL, revalidateS2SToken, nil)), tenantID,
		DueVersion{ID: bID, Version: bVer, EffectiveFrom: day(3001, 6, 1)}, day(3001, 6, 1))
	if !errors.Is(err, ErrNoActiveRuleSet) {
		t.Errorf("err = %v, want it to wrap ErrNoActiveRuleSet", err)
	}
	if calls.Load() == 0 {
		t.Error("the stub received no call: the covered invoice was never sent to the validator")
	}
	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, inv), "outage")
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv); n != 0 {
		t.Errorf("history rows = %d, want 0", n)
	}
}

// partialFailure leaves 201 covered failing invoices after a re-check whose second chunk hit
// an outage: the first revalidateChunkSize are draft, the last stays validated.
type partialFailure struct {
	super, app *pgxpool.Pool
	store      *Store
	tenantID   string
	version    DueVersion
	ids        []string // sorted: chunk order
}

func setUpPartialFailure(t *testing.T) partialFailure {
	t.Helper()
	if revalidateChunkSize != 200 {
		t.Fatalf("revalidateChunkSize = %d, want 200 -- update this test's boundary too", revalidateChunkSize)
	}
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	bID, bVer := seedBuyerNameVersion(t, super, "3001-06-01")
	tenantID := seedTenant(t, super, "RC-PARTIAL tenant")
	entityID := seedEntity(t, super, tenantID, "RC-PARTIAL entity")
	requireVersionFor(t, super, "3001-07-01", bID)

	rows, err := super.Query(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, status, issue_date)
		 SELECT $1, $2, 'RC-P-' || g, 'validated', '3001-07-01' FROM generate_series(1, $3::int) g RETURNING id`,
		tenantID, entityID, revalidateChunkSize+1)
	if err != nil {
		t.Fatalf("seed %d invoices: %v", revalidateChunkSize+1, err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan id: %v", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != revalidateChunkSize+1 {
		t.Fatalf("seeded %d invoices, want %d", len(ids), revalidateChunkSize+1)
	}
	slices.Sort(ids)

	srv, calls := newBlockingStub(t, bID, bVer, 2)
	v := DueVersion{ID: bID, Version: bVer, EffectiveFrom: day(3001, 6, 1)}
	_, err = RecheckCovered(ctx, app, store, NewGate(store, NewValidator(srv.URL, revalidateS2SToken, nil)), tenantID, v, day(3001, 6, 1))
	if !errors.Is(err, ErrNoActiveRuleSet) {
		t.Fatalf("first run err = %v, want it to wrap ErrNoActiveRuleSet from the second chunk", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("stub calls = %d, want 2 (chunk 1 answered, chunk 2 refused)", calls.Load())
	}
	return partialFailure{super: super, app: app, store: store, tenantID: tenantID, version: v, ids: ids}
}

func TestRecheckCovered_OutageOnSecondChunkKeepsTheFirst(t *testing.T) {
	p := setUpPartialFailure(t)

	drafts := 0
	for _, id := range p.ids[:revalidateChunkSize] {
		if readInvoiceStatus(t, p.super, id) == StatusDraft {
			drafts++
		}
	}
	if drafts != revalidateChunkSize {
		t.Errorf("first chunk drafts = %d, want %d (the earlier chunk's demotions stand)", drafts, revalidateChunkSize)
	}
	last := p.ids[revalidateChunkSize]
	if got := readInvoiceStatus(t, p.super, last); got != StatusValidated {
		t.Errorf("invoice in the failed chunk status = %q, want validated", got)
	}
	if n := demotionHistoryRows(t, p.super, last); n != 0 {
		t.Errorf("failed-chunk invoice history rows = %d, want 0", n)
	}
	if n := mustCount(t, p.super, `SELECT count(*) FROM invoice_status_history WHERE tenant_id = $1`, p.tenantID); n != revalidateChunkSize {
		t.Errorf("tenant history rows = %d, want %d (one per demotion of the first chunk)", n, revalidateChunkSize)
	}
}

func TestRecheckCovered_RerunAfterPartialFailureDemotesEachInvoiceOnce(t *testing.T) {
	p := setUpPartialFailure(t)

	res, err := RecheckCovered(context.Background(), p.app, p.store, realRecheckGate(t, p.app, p.store), p.tenantID, p.version, day(3001, 6, 1))
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if res.Examined != 1 || res.Demoted != 1 {
		t.Errorf("re-run result = %+v, want Examined 1, Demoted 1 (only the invoice the outage left)", res)
	}

	if got := readInvoiceStatus(t, p.super, p.ids[revalidateChunkSize]); got != StatusDraft {
		t.Errorf("last invoice status = %q after the re-run, want draft", got)
	}
	once := mustCount(t, p.super,
		`SELECT count(*) FROM (SELECT invoice_id FROM invoice_status_history
		   WHERE tenant_id = $1 AND from_status = 'validated' AND to_status = 'draft' AND actor = 'revalidate-rule-set'
		   GROUP BY invoice_id HAVING count(*) = 1) s`, p.tenantID)
	if once != len(p.ids) {
		t.Errorf("invoices with exactly one validated->draft history row = %d, want %d", once, len(p.ids))
	}
	if n := mustCount(t, p.super, `SELECT count(*) FROM invoice_status_history WHERE tenant_id = $1`, p.tenantID); n != len(p.ids) {
		t.Errorf("tenant history rows = %d, want %d (no second row for any invoice)", n, len(p.ids))
	}
	demoted := 0
	for _, id := range p.ids {
		if len(demotedAuditPayloads(t, p.app, p.tenantID, id)) == 1 {
			demoted++
		}
	}
	if demoted != len(p.ids) {
		t.Errorf("invoices with exactly one invoice.validated demoted audit row = %d, want %d", demoted, len(p.ids))
	}
}

// --- AC 6: the marker ----------------------------------------------------------

func TestMarkRechecked_IsIdempotent(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	bID, _ := seedDatedRuleSetVersion(t, super, "3001-06-01")
	count := func() int {
		return mustCount(t, super, `SELECT count(*) FROM rule_set_version_rechecks WHERE rule_set_version_id = $1`, bID)
	}
	if n := count(); n != 0 {
		t.Fatalf("fixture: %d marker rows before MarkRechecked, want 0", n)
	}

	if err := MarkRechecked(ctx, app, bID); err != nil {
		t.Fatalf("MarkRechecked (first): %v", err)
	}
	if err := MarkRechecked(ctx, app, bID); err != nil {
		t.Fatalf("MarkRechecked (second): %v", err)
	}
	if n := count(); n != 1 {
		t.Errorf("marker rows = %d, want 1", n)
	}
}

// --- AC 7: the history cause ---------------------------------------------------

func TestRecheckCovered_HistoryRowRecordsTheCause(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	bID, bVer := seedBuyerNameVersion(t, super, "3001-06-01")
	tenantID := seedTenant(t, super, "RC-CAUSE tenant")
	entityID := seedEntity(t, super, tenantID, "RC-CAUSE entity")
	requireVersionFor(t, super, "3001-07-01", bID)
	inv := seedRecheckInvoice(t, super, tenantID, entityID, "RC-CAUSE-1", StatusValidated, strPtr("3001-07-01"), nil)

	if _, err := RecheckCovered(ctx, app, store, realRecheckGate(t, app, store), tenantID,
		DueVersion{ID: bID, Version: bVer, EffectiveFrom: day(3001, 6, 1)}, day(3001, 6, 1)); err != nil {
		t.Fatalf("RecheckCovered: %v", err)
	}

	var cause *string
	if err := super.QueryRow(ctx,
		`SELECT cause_rule_set_version_id::text FROM invoice_status_history
		  WHERE invoice_id = $1 AND from_status = 'validated' AND to_status = 'draft'`, inv).Scan(&cause); err != nil {
		t.Fatalf("read the demotion row's cause: %v", err)
	}
	if cause == nil || *cause != bID {
		t.Errorf("cause_rule_set_version_id = %v, want B %s", cause, bID)
	}
}

func TestHistoryHandler_ReturnsTheCauseOfARecheckDemotion(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	bID, bVer := seedBuyerNameVersion(t, super, "3001-06-01")
	tenantID := seedTenant(t, super, "RC-WIRE tenant")
	entityID := seedEntity(t, super, tenantID, "RC-WIRE entity")
	requireVersionFor(t, super, "3001-07-01", bID)
	ident := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}
	c := auth.WithIdentity(ctx, ident)

	date := day(3001, 7, 1)
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "RC-WIRE-1", IssueDate: &date})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("Transition to validated (setup, via the real state machine): %v", err)
	}
	if _, err := RecheckCovered(ctx, app, store, realRecheckGate(t, app, store), tenantID,
		DueVersion{ID: bID, Version: bVer, EffectiveFrom: day(3001, 6, 1)}, day(3001, 6, 1)); err != nil {
		t.Fatalf("RecheckCovered: %v", err)
	}

	rec := doInvoiceHistory(t, store.History, &ident, inv.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET history = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode history %q: %v", rec.Body.String(), err)
	}
	if len(rows) != 3 {
		t.Fatalf("history rows = %d, want 3 (genesis, promotion, demotion): %s", len(rows), rec.Body.String())
	}
	for i := range 2 {
		raw, ok := rows[i]["cause"]
		if !ok || string(raw) != "null" {
			t.Errorf("row %d cause = %s (present %v), want the key present with null", i, raw, ok)
		}
	}
	raw, ok := rows[2]["cause"]
	if !ok {
		t.Fatalf("demotion row has no cause key: %s", rec.Body.String())
	}
	var cause map[string]any
	if err := json.Unmarshal(raw, &cause); err != nil {
		t.Fatalf("demotion row cause %s is not an object: %v", raw, err)
	}
	want := map[string]any{"rule_set_version": float64(bVer), "rule_set_version_id": bID, "effective_from": "3001-06-01"}
	if fmt.Sprint(cause) != fmt.Sprint(want) {
		t.Errorf("demotion row cause = %v, want %v", cause, want)
	}
}

func TestStatusHistoryCause_ColumnShapeAndGrant(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	var dataType, nullable string
	if err := super.QueryRow(ctx,
		`SELECT data_type, is_nullable FROM information_schema.columns
		  WHERE table_name = 'invoice_status_history' AND column_name = 'cause_rule_set_version_id'`,
	).Scan(&dataType, &nullable); err != nil {
		t.Fatalf("invoice_status_history.cause_rule_set_version_id does not exist -- Migration C is not applied: %v", err)
	}
	if dataType != "uuid" || nullable != "YES" {
		t.Errorf("column is %s nullable=%s, want a nullable uuid", dataType, nullable)
	}
	if n := mustCount(t, super,
		`SELECT count(*) FROM pg_constraint k JOIN pg_attribute a ON a.attrelid = k.conrelid AND a.attnum = ANY (k.conkey)
		  WHERE k.contype = 'f' AND k.conrelid = 'invoice_status_history'::regclass
		    AND k.confrelid = 'rule_set_versions'::regclass AND a.attname = 'cause_rule_set_version_id'`); n != 1 {
		t.Errorf("foreign keys from the cause column to rule_set_versions = %d, want 1", n)
	}

	tenantID := seedTenant(t, super, "RC-GRANT tenant")
	entityID := seedEntity(t, super, tenantID, "RC-GRANT entity")
	inv := seedInvoice(t, super, tenantID, entityID, "RC-GRANT-1")
	versionID := seedRuleSetVersionID(t, super)
	// Positive control: invoice_app can write the cause on INSERT (what DemoteRevalidatedTx does).
	if err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor, cause_rule_set_version_id)
			 VALUES ($1, $2, 'validated', 'draft', 'revalidate-rule-set', $3)`, tenantID, inv, versionID)
		return err
	}); err != nil {
		t.Fatalf("invoice_app INSERT with a cause: %v", err)
	}
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE invoice_status_history SET cause_rule_set_version_id = NULL WHERE invoice_id = $1`, inv)
		return err
	})
	if got := pgCode(err); got != "42501" {
		t.Errorf("invoice_app UPDATE of a history row: SQLSTATE %q (err %v), want 42501 -- the table stays append-only", got, err)
	}
}

func TestStatusHistoryCause_DownDropsTheColumn(t *testing.T) {
	migURL := os.Getenv("DATABASE_MIGRATION_URL")
	if migURL == "" {
		t.Skip("set DATABASE_MIGRATION_URL to run the owner-role Down test")
	}
	ctx := context.Background()

	matches, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*_status_history_cause.sql"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("want exactly one migrations/*_status_history_cause.sql (Migration C), got %v (err %v)", matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	_, down, found := strings.Cut(string(raw), "-- +goose Down")
	if !found {
		t.Fatalf("%s has no -- +goose Down section", matches[0])
	}
	var kept []string
	for _, line := range strings.Split(down, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			kept = append(kept, line)
		}
	}
	var stmts []string
	for _, s := range strings.Split(strings.Join(kept, "\n"), ";") {
		if s = strings.TrimSpace(s); s != "" {
			stmts = append(stmts, s)
		}
	}
	if len(stmts) == 0 {
		t.Fatalf("%s has an empty Down", matches[0])
	}

	mig, err := pgxpool.New(ctx, migURL)
	if err != nil {
		t.Fatalf("connect migrator: %v", err)
	}
	t.Cleanup(mig.Close)
	tx, err := mig.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	hasColumn := func() bool {
		var ok bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = 'invoice_status_history'::regclass
			   AND attname = 'cause_rule_set_version_id' AND NOT attisdropped)`).Scan(&ok); err != nil {
			t.Fatalf("probe column: %v", err)
		}
		return ok
	}
	if !hasColumn() {
		t.Fatal("the cause column does not exist before the Down: Migration C is not applied")
	}
	for _, s := range stmts {
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("Down statement failed: %v\n%s", err, s)
		}
	}
	if hasColumn() {
		t.Error("the cause column still exists after the Down")
	}
}
