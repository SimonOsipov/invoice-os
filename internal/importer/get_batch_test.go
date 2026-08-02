// task-283 (INVCR-01-07 subtask 7): RED, DB-backed tests for
// internal/importer's Store.GetBatch -- the batch header GET
// /v1/imports/{id} is built on -- written BEFORE the real implementation
// exists (RED against store.go's not-implemented stub: GetBatch always
// returns the zero Batch{}, nil -- see that file's doc comment). Reuses
// dbTestPools/seedTenant/seedEntity (store_test.go) and
// newTestService/stdHeader/stdMapping/mkRow (service_test.go) -- all
// same-package, none redefined here. doImportGetBatch (handlers_test.go) is
// the shared HTTP-level request builder specs 5/6/4 all use.
//
// Spec-to-test map (Stage 1 Test Specs table, task-283):
//
//	spec 1  TestGetBatch_ReturnsFrozenCountsAndErrors
//	spec 2  TestGetBatch_RuleSetVersionIsMinNotArbitrary
//	spec 3  TestGetBatch_ZeroInvoicesVersionIsNullAndBodyRendersNull
//	spec 5  TestRLS_GetBatchCrossTenantIs404AndIdenticalToUnknown
//	spec 15 TestGetBatch_RowsInvalidAgreesWithErrorRowCount
//
// Specs 4/6 (handler-level, fake store) live in handlers_test.go; specs
// 7-14 live in internal/invoice (violation_summary_test.go / handlers_test.go).
//
// QA Stage 4 addition (task-283, not in Stage 1's table):
// TestGetBatch_DirectMalformedIDReturnsErrValidationNot500 -- the ONE spec
// that exercises GetBatch's own 22P02->ErrValidation mapping directly
// (every handler-level malformed-id test uses a fake store closure).
//
// Run (`make test-rls` does NOT cover this package -- it targets
// ./internal/platform/db/... at port 5432):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5434/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5434/invoice_os?sslmode=disable" \
//	go test -count=1 -p 1 ./internal/importer/...
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// seedInvoiceForBatch inserts one invoices row directly (bypassing
// internal/invoice's Store) as the superuser, linked to batchID by
// import_batch_id and optionally stamped with ruleSetVersionID -- needed by
// GetBatch's rule_set_version derivation specs, which correlate on
// import_batch_id, not just entity_id (store_test.go's own seedInvoice
// leaves import_batch_id NULL).
func seedInvoiceForBatch(t *testing.T, super *pgxpool.Pool, tenantID, entityID, batchID string, ruleSetVersionID *string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, import_batch_id, rule_set_version_id)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		tenantID, entityID, uuid.NewString(), batchID, ruleSetVersionID,
	).Scan(&id); err != nil {
		t.Fatalf("seed invoices (for batch): %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, id)
	})
	return id
}

// ruleSetVersionRow is one rule_set_versions row's (id, version) pair --
// copied from internal/invoice/rule_set_version_adversarial_test.go's own
// helper of the same name (per-package copy convention).
type ruleSetVersionRow struct {
	id      string
	version int
}

// twoRuleSetVersionRows returns the two distinct rule_set_versions rows
// this dev DB already ships (v1 seeded by the M3-04 migration, v2 by the
// rule_set_v2 migration) -- any two suffice, these specs only need them to
// be genuinely distinct so min() has something to discriminate.
func twoRuleSetVersionRows(t *testing.T, super *pgxpool.Pool) (a, b ruleSetVersionRow) {
	t.Helper()
	rows, err := super.Query(context.Background(), `SELECT id, version FROM rule_set_versions ORDER BY published_at LIMIT 2`)
	if err != nil {
		t.Fatalf("look up two rule_set_versions rows (is the M3-04/rule_set_v2 seed applied?): %v", err)
	}
	defer rows.Close()
	var seeds []ruleSetVersionRow
	for rows.Next() {
		var r ruleSetVersionRow
		if err := rows.Scan(&r.id, &r.version); err != nil {
			t.Fatalf("scan rule_set_versions row: %v", err)
		}
		seeds = append(seeds, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rule_set_versions rows: %v", err)
	}
	if len(seeds) < 2 {
		t.Fatalf("this test needs two distinct rule_set_versions rows, found %d", len(seeds))
	}
	return seeds[0], seeds[1]
}

// sumRowErrorRows totals the sheet rows named across errs -- Rows (plural, a
// shared-group error) contributes len(Rows), Row (scalar, an ungroupable
// row) contributes 1. Mirrors Service.Import's own accounting
// (invalidRows += len(g.rowIdxs) / invalidRows++, service.go:681/691/701/
// 707/719), so a genuine disagreement between this sum and Batch.RowsInvalid
// means the stored errors[] and the stored counters drifted apart.
func sumRowErrorRows(errs []RowError) int {
	n := 0
	for _, e := range errs {
		if len(e.Rows) > 0 {
			n += len(e.Rows)
		} else {
			n++
		}
	}
	return n
}

// TestGetBatch_ReturnsFrozenCountsAndErrors (spec 1): a batch finalized
// 'completed' with counts 9/7/2 and two RowError entries (one scalar Row,
// one plural Rows) round-trips every frozen field verbatim through
// GetBatch. RED against the stub: GetBatch returns the zero Batch{}, so
// every field assertion below fails on value (e.g. Status "" != "completed"),
// not on a compile error.
func TestGetBatch_ReturnsFrozenCountsAndErrors(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "GETBATCH-FROZEN tenant")
	entityID := seedEntity(t, super, tenantID, "GETBATCH-FROZEN entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	batchID, err := store.CreateBatch(c, entityID, "", "")
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	wantErrors := []RowError{
		{Row: 3, Field: "invoice_number", Message: "blank invoice number: row cannot be grouped"},
		{Rows: []int{5, 6}, Field: "total", Message: "rows disagree on total"},
	}
	if err := store.Finalize(c, batchID, 9, 7, 2, wantErrors, "completed"); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	got, err := store.GetBatch(c, batchID)
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	if got.ID != batchID {
		t.Errorf("ID = %q, want %q", got.ID, batchID)
	}
	if got.EntityID != entityID {
		t.Errorf("EntityID = %q, want %q", got.EntityID, entityID)
	}
	if got.Status != "completed" {
		t.Errorf("Status = %q, want %q", got.Status, "completed")
	}
	if got.RowsTotal != 9 || got.RowsValid != 7 || got.RowsInvalid != 2 {
		t.Errorf("counts = (total=%d valid=%d invalid=%d), want (9,7,2)", got.RowsTotal, got.RowsValid, got.RowsInvalid)
	}
	if !reflect.DeepEqual(got.Errors, wantErrors) {
		t.Errorf("Errors = %+v, want %+v", got.Errors, wantErrors)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero, want the batch's real created_at timestamp")
	}
}

// TestGetBatch_RuleSetVersionIsMinNotArbitrary (spec 2, task-283 R4): a
// batch with two invoices stamped against two DIFFERENT rule_set_versions
// must report the MINIMUM of the two, STABLY across repeated calls -- a
// LIMIT 1 (no ORDER BY) implementation would be non-deterministic here and
// could return either version, or flip between calls. RED against the
// stub: RuleSetVersion is always nil, so the first call already fails on
// value.
//
// Seed order is DELIBERATE (QA Stage 4, F1): the HIGHER-version invoice
// row is inserted FIRST, the lower (= correct min) SECOND. A fresh heap
// scan tends to surface rows in insertion order, so a LIMIT 1 (no ORDER
// BY) implementation reaches the higher-version row first and returns the
// WRONG value here -- proven live by reversing the original (lower-first)
// order, which left LIMIT 1 coincidentally agreeing with min() 3/3 (see
// task-283 implementation notes). min() itself is order-independent and
// still returns the true minimum either way.
func TestGetBatch_RuleSetVersionIsMinNotArbitrary(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "GETBATCH-MIN tenant")
	entityID := seedEntity(t, super, tenantID, "GETBATCH-MIN entity")
	rowA, rowB := twoRuleSetVersionRows(t, super)

	lo, hi := rowA, rowB
	if hi.version < lo.version {
		lo, hi = hi, lo
	}
	want := lo.version

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	batchID, err := store.CreateBatch(c, entityID, "", "")
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if err := store.Finalize(c, batchID, 2, 2, 0, nil, "completed"); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	// Higher version FIRST, lower (= min) SECOND -- see doc comment above.
	seedInvoiceForBatch(t, super, tenantID, entityID, batchID, &hi.id)
	seedInvoiceForBatch(t, super, tenantID, entityID, batchID, &lo.id)

	for i := 0; i < 5; i++ {
		got, err := store.GetBatch(c, batchID)
		if err != nil {
			t.Fatalf("GetBatch (call %d): %v", i, err)
		}
		if got.RuleSetVersion == nil || *got.RuleSetVersion != want {
			t.Fatalf("GetBatch (call %d).RuleSetVersion = %v, want %d (min of the two stamped versions %d/%d) -- LIMIT 1 without ORDER BY would be non-deterministic here",
				i, got.RuleSetVersion, want, rowA.version, rowB.version)
		}
	}
}

// TestGetBatch_ZeroInvoicesVersionIsNullAndBodyRendersNull (spec 3,
// task-283 [Stage-1 F2]): TWO legs in one test. Leg A (stamped) is the
// ONLY leg that discriminates a real implementation from a stub that
// always returns Batch{} (RuleSetVersion nil) -- leg B (zero invoices)
// alone is satisfied by that same nil-returning stub and proves nothing on
// its own. Both legs also marshal through the real batchResponse type
// (handlers.go) to pin the wire rendering: an explicit int, and an
// explicit JSON null (never omitted, never a false 0).
func TestGetBatch_ZeroInvoicesVersionIsNullAndBodyRendersNull(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "GETBATCH-NULLVER tenant")
	entityID := seedEntity(t, super, tenantID, "GETBATCH-NULLVER entity")
	rowA, _ := twoRuleSetVersionRows(t, super)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	// Leg A -- stamped: the genuine leg.
	stampedBatch, err := store.CreateBatch(c, entityID, "", "")
	if err != nil {
		t.Fatalf("CreateBatch (stamped): %v", err)
	}
	if err := store.Finalize(c, stampedBatch, 1, 1, 0, nil, "completed"); err != nil {
		t.Fatalf("Finalize (stamped): %v", err)
	}
	seedInvoiceForBatch(t, super, tenantID, entityID, stampedBatch, &rowA.id)

	stampedGot, err := store.GetBatch(c, stampedBatch)
	if err != nil {
		t.Fatalf("GetBatch (stamped): %v", err)
	}
	if stampedGot.RuleSetVersion == nil || *stampedGot.RuleSetVersion != rowA.version {
		t.Fatalf("GetBatch (stamped).RuleSetVersion = %v, want %d", stampedGot.RuleSetVersion, rowA.version)
	}
	stampedBody, err := json.Marshal(batchResponse{
		ID: stampedGot.ID, EntityID: stampedGot.EntityID, Status: stampedGot.Status,
		RowsTotal: stampedGot.RowsTotal, RowsValid: stampedGot.RowsValid, RowsInvalid: stampedGot.RowsInvalid,
		Errors: stampedGot.Errors, RuleSetVersion: stampedGot.RuleSetVersion, CreatedAt: stampedGot.CreatedAt,
	})
	if err != nil {
		t.Fatalf("marshal stamped batchResponse: %v", err)
	}
	if !bytes.Contains(stampedBody, []byte(fmt.Sprintf(`"rule_set_version":%d`, rowA.version))) {
		t.Errorf("stamped body = %s, want the literal \"rule_set_version\":%d", stampedBody, rowA.version)
	}

	// Leg B -- zero invoices: a real batch with NOTHING linked to it. Kept
	// in the SAME test as leg A per task-283's own vacuity ruling -- in
	// isolation this leg passes against ANY nil-returning stub.
	emptyBatch, err := store.CreateBatch(c, entityID, "", "")
	if err != nil {
		t.Fatalf("CreateBatch (empty): %v", err)
	}
	if err := store.Finalize(c, emptyBatch, 0, 0, 0, nil, "completed"); err != nil {
		t.Fatalf("Finalize (empty): %v", err)
	}

	emptyGot, err := store.GetBatch(c, emptyBatch)
	if err != nil {
		t.Fatalf("GetBatch (empty): %v", err)
	}
	if emptyGot.RuleSetVersion != nil {
		t.Errorf("GetBatch (empty).RuleSetVersion = %d, want nil", *emptyGot.RuleSetVersion)
	}
	emptyBody, err := json.Marshal(batchResponse{
		ID: emptyGot.ID, EntityID: emptyGot.EntityID, Status: emptyGot.Status,
		RowsTotal: emptyGot.RowsTotal, RowsValid: emptyGot.RowsValid, RowsInvalid: emptyGot.RowsInvalid,
		Errors: emptyGot.Errors, RuleSetVersion: emptyGot.RuleSetVersion, CreatedAt: emptyGot.CreatedAt,
	})
	if err != nil {
		t.Fatalf("marshal empty batchResponse: %v", err)
	}
	if !bytes.Contains(emptyBody, []byte(`"rule_set_version":null`)) {
		t.Errorf("empty body = %s, want the literal \"rule_set_version\":null", emptyBody)
	}
	// QA Stage 4 addition: pins the REAL Postgres round-trip for AC #2's
	// "errors is [] never null" -- Finalize(..., nil, ...) normalizes to a
	// jsonb '[]' payload (store.go's Finalize), and GetBatch's
	// json.Unmarshal of that payload must come back a non-nil []RowError.
	// The only other spec covering this shape (handlers_test.go's
	// TestGetBatchHandler_ErrorsIsEmptyArrayNotNull) injects a FAKE store
	// returning Batch{Errors: nil} directly -- it never exercises the real
	// column default or the real Unmarshal path.
	if emptyGot.Errors == nil {
		t.Error("GetBatch (empty).Errors = nil, want a non-nil empty []RowError (real Postgres round-trip)")
	}
	if !bytes.Contains(emptyBody, []byte(`"errors":[]`)) {
		t.Errorf("empty body = %s, want the literal \"errors\":[] (never null, even on a clean/zero-error batch)", emptyBody)
	}
}

// TestRLS_GetBatchCrossTenantIs404AndIdenticalToUnknown (spec 5, task-283
// R5): three legs, ONE test, driven through the REAL HTTP handler (not just
// the store) so the byte-identical assertion is meaningful. The positive,
// same-tenant leg is the ONLY thing that discriminates a real
// implementation from one that always 404s (the RLS vacuity trap --
// needs_attention_test.go:166-174's own reasoning, mirrored here). The
// cross-tenant and unknown-uuid legs must be BYTE-IDENTICAL -- that
// equality, not the 404 status alone, is what proves there is no existence
// oracle.
func TestRLS_GetBatchCrossTenantIs404AndIdenticalToUnknown(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenant1 := seedTenant(t, super, "GETBATCH-RLS tenant 1")
	tenant2 := seedTenant(t, super, "GETBATCH-RLS tenant 2")
	entity1 := seedEntity(t, super, tenant1, "GETBATCH-RLS entity 1")
	entity2 := seedEntity(t, super, tenant2, "GETBATCH-RLS entity 2")

	store := NewStore(app)
	c1 := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenant1})
	c2 := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenant2})

	batch1, err := store.CreateBatch(c1, entity1, "", "")
	if err != nil {
		t.Fatalf("CreateBatch (tenant 1): %v", err)
	}
	if err := store.Finalize(c1, batch1, 4, 3, 1, []RowError{{Row: 2, Message: "bad"}}, "completed"); err != nil {
		t.Fatalf("Finalize (tenant 1): %v", err)
	}

	batch2, err := store.CreateBatch(c2, entity2, "", "")
	if err != nil {
		t.Fatalf("CreateBatch (tenant 2): %v", err)
	}
	if err := store.Finalize(c2, batch2, 1, 1, 0, nil, "completed"); err != nil {
		t.Fatalf("Finalize (tenant 2): %v", err)
	}

	id1 := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenant1}

	// Positive, same-tenant leg -- the discriminating half.
	ownRec, ownResp := doImportGetBatch(t, store.GetBatch, &id1, batch1)
	if ownRec.Code != http.StatusOK {
		t.Errorf("own-tenant GetBatch status = %d, want 200 (body=%s)", ownRec.Code, ownRec.Body.String())
	}
	if ownResp.RowsTotal != 4 || ownResp.RowsValid != 3 || ownResp.RowsInvalid != 1 {
		t.Errorf("own-tenant counts = (total=%d valid=%d invalid=%d), want (4,3,1)", ownResp.RowsTotal, ownResp.RowsValid, ownResp.RowsInvalid)
	}

	// Cross-tenant: tenant 1 asking for tenant 2's batch.
	crossRec, _ := doImportGetBatch(t, store.GetBatch, &id1, batch2)
	if crossRec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant GetBatch status = %d, want 404 (body=%s)", crossRec.Code, crossRec.Body.String())
	}

	// Unknown: tenant 1 asking for an id that exists nowhere.
	unknownRec, _ := doImportGetBatch(t, store.GetBatch, &id1, uuid.NewString())
	if unknownRec.Code != http.StatusNotFound {
		t.Errorf("unknown-id GetBatch status = %d, want 404 (body=%s)", unknownRec.Code, unknownRec.Body.String())
	}

	// The non-oracle property itself.
	if !bytes.Equal(crossRec.Body.Bytes(), unknownRec.Body.Bytes()) {
		t.Errorf("cross-tenant body = %s, unknown-id body = %s -- want byte-identical (no existence oracle)",
			crossRec.Body.String(), unknownRec.Body.String())
	}
}

// TestGetBatch_ProcessingStatusUnfinalizedBatchReturns200 (QA Stage 4,
// task-283 trap 2): CreateBatch alone (no Finalize) leaves a row with
// status='processing' and all-zero counts -- reachable in production by a
// crash between the two calls, and now user-visible for the first time via
// this route (no terminal transition, no reaper -- flagged, out of scope
// per the plan). This pins that such a row is NOT an error case: GET
// returns 200 with status="processing", zeroed counts, "errors":[], and
// rule_set_version:null (no invoices are linked yet) -- never a 404, never
// a 500, and never silently coerced to some other status string.
func TestGetBatch_ProcessingStatusUnfinalizedBatchReturns200(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "GETBATCH-PROCESSING tenant")
	entityID := seedEntity(t, super, tenantID, "GETBATCH-PROCESSING entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	batchID, err := store.CreateBatch(c, entityID, "", "")
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	// Deliberately NOT calling Finalize -- simulates a crash between the
	// two calls.

	got, err := store.GetBatch(c, batchID)
	if err != nil {
		t.Fatalf("GetBatch (unfinalized): %v, want 200/no error", err)
	}
	if got.Status != "processing" {
		t.Errorf("Status = %q, want %q (CreateBatch's own INSERT value)", got.Status, "processing")
	}
	if got.RowsTotal != 0 || got.RowsValid != 0 || got.RowsInvalid != 0 {
		t.Errorf("counts = (total=%d valid=%d invalid=%d), want (0,0,0)", got.RowsTotal, got.RowsValid, got.RowsInvalid)
	}
	if got.Errors == nil {
		t.Error("Errors = nil, want a non-nil empty []RowError")
	}
	if got.RuleSetVersion != nil {
		t.Errorf("RuleSetVersion = %v, want nil (no invoices linked to an unfinalized batch)", *got.RuleSetVersion)
	}

	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenantID}
	rec, _ := doImportGetBatch(t, store.GetBatch, &id, batchID)
	if rec.Code != http.StatusOK {
		t.Errorf("HTTP status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"status":"processing"`)) {
		t.Errorf("body = %s, want the literal \"status\":\"processing\"", rec.Body.Bytes())
	}
}

// TestGetBatch_RowsInvalidAgreesWithErrorRowCount (spec 15): drives the
// REAL Service.Import (not a stub) on a fixture that quarantines via BOTH
// error shapes -- a 2-row disagreeing group (RowError.Rows) and a 1-row
// blank-invoice-number row (RowError.Row) -- then asserts GetBatch's
// persisted RowsInvalid agrees with (a) Service.Import's own trusted count
// and (b) the sum of rows named across GetBatch's own persisted errors[].
// If these ever disagree, the review screen's right channel ("N rows not
// imported") would contradict its own error list.
func TestGetBatch_RowsInvalidAgreesWithErrorRowCount(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "GETBATCH-ROWCOUNT tenant")
	entityID := seedEntity(t, super, tenantID, "GETBATCH-ROWCOUNT entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-OK", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),        // sheet 2 -- ready
		mkRow("INV-BAD", "2026-02-01", "T2", "B2", "NGN", "100.00", "0.00", "100.00", "BadItem1", "1", "100.00"), // sheet 3 -- disagreeing group
		mkRow("INV-BAD", "2026-02-01", "T2", "B2", "NGN", "100.00", "0.00", "200.00", "BadItem2", "1", "200.00"), // sheet 4 -- total differs
		mkRow("", "2026-01-10", "T3", "B3", "NGN", "5.00", "0.00", "5.00", "Blank", "1", "5.00"),                 // sheet 5 -- blank invoice number
	}

	res, err := svc.Import(c, entityID, "", "", stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	const wantInvalid = 3 // 2 rows in the disagreeing group + 1 ungroupable blank row
	if res.RowsInvalid != wantInvalid {
		t.Fatalf("Import.RowsInvalid = %d, want %d -- fixture assumption broken", res.RowsInvalid, wantInvalid)
	}

	store := NewStore(app)
	got, err := store.GetBatch(c, res.ID)
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	if got.RowsInvalid != res.RowsInvalid {
		t.Errorf("GetBatch.RowsInvalid = %d, want %d (Service.Import's own, trusted count)", got.RowsInvalid, res.RowsInvalid)
	}
	if gotSum := sumRowErrorRows(got.Errors); gotSum != got.RowsInvalid {
		t.Errorf("sum of RowError row counts = %d, want %d (GetBatch.RowsInvalid) -- if these disagree, the review screen's right channel contradicts its own error list", gotSum, got.RowsInvalid)
	}
	if gotSum := sumRowErrorRows(got.Errors); gotSum != wantInvalid {
		t.Errorf("sum of RowError row counts = %d, want %d", gotSum, wantInvalid)
	}
}

// --- BULK-01-01 (task-305): filename served by GET -----------------------

// TestGetBatch_ServesFilenameViaStoreAndWire (BULK-01-2): a batch created
// with a filename must have it readable back both at the store (raw column)
// level and at the HTTP wire level. Batch.Filename/batchResponse's "filename"
// json key do NOT exist yet (AC #6 is the executor's job -- see
// handlers_test.go's batchResponseBody comment), so this reads the column
// directly via the superuser pool for the store-level pin, and relies on
// batchResponseBody's own Filename field (added for BULK-01-2/3) for the
// wire-level pin. RED against the stubs: the store-level read comes back nil
// (CreateBatch never writes the column) and the wire-level body never
// contains a "filename" key at all (batchResponse has no such field).
func TestGetBatch_ServesFilenameViaStoreAndWire(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BULK-01-2 tenant")
	entityID := seedEntity(t, super, tenantID, "BULK-01-2 entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const wantFilename = "branch-lagos.csv"
	batchID, err := store.CreateBatch(c, entityID, wantFilename, "")
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if err := store.Finalize(c, batchID, 0, 0, 0, nil, "completed"); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Store-level pin.
	var gotFilename *string
	if err := super.QueryRow(ctx, `SELECT filename FROM import_batches WHERE id = $1`, batchID).Scan(&gotFilename); err != nil {
		t.Fatalf("read back filename: %v", err)
	}
	if gotFilename == nil || *gotFilename != wantFilename {
		t.Errorf("persisted filename = %v, want %q", gotFilename, wantFilename)
	}

	// Wire-level pin: GetHandler must emit the exact stored name.
	reqID := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenantID}
	rec, resp := doImportGetBatch(t, store.GetBatch, &reqID, batchID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Filename == nil || *resp.Filename != wantFilename {
		t.Errorf("resp.Filename = %v, want %q", resp.Filename, wantFilename)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"filename":"`+wantFilename+`"`)) {
		t.Errorf("body = %s, want the literal \"filename\":%q", rec.Body.Bytes(), wantFilename)
	}
}

// TestGetBatch_PreExistingRowFilenameIsNullBothSides (BULK-01-3): a batch row
// inserted with no filename (simulating one that predates this migration, or
// whose upload simply carried no recoverable name) must read back nil at the
// store level and render an EXPLICIT "filename":null on the wire -- present,
// never omitted (AC #6's "no omitempty"). RED against the stubs: the wire
// body never contains a "filename" key AT ALL today (absent, not merely
// null), so the literal-substring assertion fails.
func TestGetBatch_PreExistingRowFilenameIsNullBothSides(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BULK-01-3 tenant")
	entityID := seedEntity(t, super, tenantID, "BULK-01-3 entity")

	// Inserted directly (bypassing Store.CreateBatch) with no filename column
	// value at all -- exactly what a pre-migration row looks like.
	var batchID string
	if err := super.QueryRow(ctx,
		`INSERT INTO import_batches (tenant_id, entity_id, status, rows_total, rows_valid, rows_invalid, errors)
		 VALUES ($1, $2, 'completed', 0, 0, 0, '[]'::jsonb) RETURNING id`,
		tenantID, entityID,
	).Scan(&batchID); err != nil {
		t.Fatalf("seed pre-existing import_batches row: %v", err)
	}

	store := NewStore(app)
	reqID := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: tenantID}
	rec, resp := doImportGetBatch(t, store.GetBatch, &reqID, batchID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Filename != nil {
		t.Errorf("resp.Filename = %q, want nil", *resp.Filename)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"filename":null`)) {
		t.Errorf("body = %s, want the literal \"filename\":null (key present, not omitted)", rec.Body.Bytes())
	}
}

// TestGetBatch_DirectMalformedIDReturnsErrValidationNot500 (QA Stage 4,
// task-283 AC #1 / R5): calls Store.GetBatch DIRECTLY (bypassing
// GetHandler's own uuid.Parse guard entirely) with a malformed,
// non-well-formed-uuid id. This is the ONE spec that actually exercises
// GetBatch's own 22P02->ErrValidation mapping -- every other malformed-id
// test in this package (handlers_test.go's
// TestGetBatchHandler_MalformedID400StoreNeverCalled) uses a fake `get`
// closure precisely so the real store is NEVER called, and no other spec
// here passes a non-uuid string to store.GetBatch. Without this mapping
// (AC #1's "EntitySupplier alone would 500 on a malformed id"), a
// non-HTTP caller of Store.GetBatch would get a raw pgx 22P02 error
// instead of the package's own ErrValidation sentinel -- and any handler
// built on top would map that to a 500 via statusForErr's default case,
// not the intended 400.
func TestGetBatch_DirectMalformedIDReturnsErrValidationNot500(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "GETBATCH-MALFORMED tenant")
	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	_, err := store.GetBatch(c, "not-a-uuid")
	if err == nil {
		t.Fatal("GetBatch(\"not-a-uuid\") = nil error, want ErrValidation")
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("GetBatch(\"not-a-uuid\") error = %v, want errors.Is(err, ErrValidation) -- a malformed id reaching the store directly must not surface as a raw/unmapped error (which a caller would 500 on)", err)
	}
}
