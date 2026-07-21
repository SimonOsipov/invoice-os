// M4-13-01: the end-to-end "spine" integration test -- the one path that
// stitches together every layer M4 shipped in isolation and proves they
// agree on ONE invoice's lifecycle audit trail. It imports all 500 green_500
// invoices through the REAL validate gate (Decode -> real portfolio entity ->
// Service.Import against a real in-process rule-set 04), then walks a
// deterministic sample of 10 (INV-SYN-00001..00010) the rest of the way up
// the state machine (validated -> queued -> submitted -> accepted) via the
// real invoice.Store.Transition, and asserts the resulting
// invoice_status_history chain and audit_log trail are EXACTLY what each
// writer promises -- correlated across the importer, the state machine, the
// validate gate and the audit log, under tenant RLS.
//
// This is a VERIFICATION test, not a RED spec: every layer it exercises
// (Store.Transition, Store.ApplyValidation, audit.Record, the import gate) is
// already shipped and correct, so a correctly-written test PASSES on first
// run (Decision [test-first-red-expectation]). The only genuine failure
// signal is a wrong assertion VALUE here (esp. the row counts), which is a
// bug in THIS test, not in production.
//
// Evidence map -- pillars this file deliberately does NOT re-test, and where
// that coverage lives (story M4-13 Decision [evidence-map-home]):
//
//	transition matrix (legal/redundant/illegal 49-pair sweep)
//	   -> internal/invoice/transition_adversarial_test.go
//	per-edge transition semantics (INV-SM-01..07)
//	   -> internal/invoice/transition_test.go
//	audit immutability / atomicity / isolation
//	   -> internal/audit/audit_test.go
//	RLS on invoices / line_items / invoice_status_history / import_batches
//	   -> internal/platform/db/*_rls_test.go
//	green import counters + 500-invoice perf budget
//	   -> internal/importer/{fixtures_green,perf}_test.go
//
// What THIS file owns: the live end-to-end spine (import -> validate ->
// walk-to-accepted) with the status_history chain + audit-payload {from,to} +
// cross-tenant audit isolation asserted on the REAL path.
//
// Spec-to-test map (M4-13-01 Test Specs, SPINE-01..07), all as subtests of
// TestSpineIntegration so `-run Spine` selects the whole suite:
//
//	SPINE-01 import 500 clean + DB status=='validated' count == 500
//	SPINE-02 walk 10 to accepted; Get.Status == accepted
//	SPINE-03 History is the exact 5-row chain, genesis FromStatus==nil, actor==subject
//	SPINE-04 audit_log has exactly 6 rows per invoice; per-event counts
//	SPINE-05 every audit row's actor == the one known subject
//	SPINE-06 the 4 transitioned edges == the expected edge set
//	SPINE-07 tenant scoping: same query under a second tenant -> 0 rows
//	SPINE-08 (QA-added) a non-sampled invoice stays 'validated' with the exact
//	         2-row history + 3-row audit import-only trail (walk blast-radius)
//
// Run (dev DB on 5433):
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5433/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5433/invoice_os?sslmode=disable" \
//	go test -count=1 -run Spine ./internal/importer/...
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// spineKnownSubject is the ONE fixed identity used for BOTH the import AND the
// state-machine walk (Decision [spine-known-subject]): every genesis /
// transition / validate row Store.Create, transitionTx and ApplyValidation
// write derives its actor from ctx.Subject, so pinning a single subject lets
// SPINE-03/05 assert actor equality on every history and audit row. A plain
// UUID string -- audit_log.actor / invoice_status_history.actor are free-text
// (char_length>0, <=255), never FK'd to a users table, so any non-empty value
// works; a fixed sentinel just makes the equality assertion legible.
const spineKnownSubject = "11111111-1111-1111-1111-111111111111"

// spineAuditRow is one audit_log row read back under tenant RLS (actor +
// event + the raw jsonb payload, the only columns any SPINE assertion needs).
type spineAuditRow struct {
	actor   string
	event   string
	payload json.RawMessage
}

// spineSample is everything collected ONCE per sampled invoice, so SPINE-03..06
// assert over shared state instead of re-walking (M4-13-01 efficiency note).
type spineSample struct {
	id          string
	finalStatus invoice.Status
	hist        []invoice.StatusChange
	audit       []spineAuditRow
}

// spineEdge is a (from,to) pair decoded from an "invoice.transitioned" payload.
type spineEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func TestSpineIntegration(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "SPINE tenant A")
	entityID, _ := createEntityViaRealPortfolioStore(t, super, app, tenantID, "SPINE entity", tinFixFIRSTIN)

	// ONE identity for BOTH import and walk ([spine-known-subject]).
	idCtx := auth.WithIdentity(ctx, auth.Identity{
		Subject:  spineKnownSubject,
		Role:     "authenticated",
		TenantID: tenantID,
	})

	// --- import all 500 green_500 through the REAL gate (fixtures_green_test.go
	// wiring, but with our fixed subject instead of importGreenFixture's random
	// one) ------------------------------------------------------------------
	data, err := os.ReadFile("../../testdata/invoices/green_500.csv")
	if err != nil {
		t.Fatalf("read green_500.csv: %v", err)
	}
	header, rows, _, err := Decode(bytes.NewReader(data), "csv")
	if err != nil {
		t.Fatalf("Decode green_500.csv: %v", err)
	}

	srv := startInProcess04ForImporter(t, app)
	validator := invoice.NewValidator(srv.URL, impvS2SToken, nil)
	svc := newTestServiceWithGate(app, invoice.NewGate(invoice.NewStore(app), validator))

	res, err := svc.Import(idCtx, entityID, stdMapping, header, rows, false)
	if err != nil {
		t.Fatalf("Import green_500.csv: %v", err)
	}

	// Snapshot the "all validated" count NOW, BEFORE the walk mutates 10 of
	// them to accepted -- SPINE-01 asserts against this snapshot.
	validatedCount := countInvoicesByStatus(t, app, tenantID, entityID, "validated")

	// --- fetch the deterministic sample of 10 (first by invoice_number ASC)
	// under tenant A (RLS makes them visible only under this tenant) ---------
	sampleIDs := selectSampleInvoiceIDs(t, app, tenantID, entityID, 10)
	if len(sampleIDs) != 10 {
		t.Fatalf("sampled %d invoice ids, want 10 (INV-SYN-00001..00010)", len(sampleIDs))
	}

	// --- walk each sampled invoice validated -> queued -> submitted ->
	// accepted, then collect its history + audit trail ONCE -----------------
	invStore := invoice.NewStore(app)
	samples := make([]spineSample, 0, len(sampleIDs))
	for _, id := range sampleIDs {
		for _, target := range []invoice.Status{invoice.StatusQueued, invoice.StatusSubmitted, invoice.StatusAccepted} {
			if _, err := invStore.Transition(idCtx, id, target); err != nil {
				t.Fatalf("Transition(%s -> %s): %v", id, target, err)
			}
		}
		got, err := invStore.Get(idCtx, id)
		if err != nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		hist, err := invStore.History(idCtx, id)
		if err != nil {
			t.Fatalf("History(%s): %v", id, err)
		}
		samples = append(samples, spineSample{
			id:          id,
			finalStatus: got.Status,
			hist:        hist,
			audit:       readAuditForInvoice(t, app, tenantID, id),
		})
	}

	// SPINE-01: the import is clean and every one of the 500 landed 'validated'.
	t.Run("SPINE-01_import500CleanAndAllValidated", func(t *testing.T) {
		assertCleanImport(t, "green_500", res, 500)
		if validatedCount != 500 {
			t.Errorf("invoices with status='validated' after import = %d, want 500", validatedCount)
		}
	})

	// SPINE-02: each sampled invoice walked to accepted.
	t.Run("SPINE-02_walkToAccepted", func(t *testing.T) {
		for _, s := range samples {
			if s.finalStatus != invoice.StatusAccepted {
				t.Errorf("invoice %s final status = %q, want %q", s.id, s.finalStatus, invoice.StatusAccepted)
			}
		}
	})

	// SPINE-03: the 5-row history chain, exact order + values; genesis
	// FromStatus==nil, every other FromStatus == the prior row's ToStatus;
	// every actor == the one known subject.
	t.Run("SPINE-03_historyChain5Rows", func(t *testing.T) {
		wantChain := []struct {
			from *invoice.Status // nil only on the genesis row
			to   invoice.Status
		}{
			{nil, invoice.StatusDraft},
			{statusPtr(invoice.StatusDraft), invoice.StatusValidated},
			{statusPtr(invoice.StatusValidated), invoice.StatusQueued},
			{statusPtr(invoice.StatusQueued), invoice.StatusSubmitted},
			{statusPtr(invoice.StatusSubmitted), invoice.StatusAccepted},
		}
		for _, s := range samples {
			if len(s.hist) != 5 {
				t.Errorf("invoice %s: History len = %d, want 5", s.id, len(s.hist))
				continue
			}
			// Row 1 is the genesis row (Store.Create, from_status=NULL).
			if s.hist[0].FromStatus != nil {
				t.Errorf("invoice %s: hist[0].FromStatus = %v, want nil (genesis row)", s.id, *s.hist[0].FromStatus)
			}
			for i, want := range wantChain {
				row := s.hist[i]
				if row.ToStatus != want.to {
					t.Errorf("invoice %s: hist[%d].ToStatus = %q, want %q", s.id, i, row.ToStatus, want.to)
				}
				if i == 0 {
					continue // genesis From already checked; nil by design
				}
				if row.FromStatus == nil {
					t.Errorf("invoice %s: hist[%d].FromStatus = nil, want %q", s.id, i, *want.from)
					continue
				}
				if *row.FromStatus != *want.from {
					t.Errorf("invoice %s: hist[%d].FromStatus = %q, want %q", s.id, i, *row.FromStatus, *want.from)
				}
				// FromStatus must equal the PRIOR row's ToStatus (chain contiguity).
				if *row.FromStatus != s.hist[i-1].ToStatus {
					t.Errorf("invoice %s: hist[%d].FromStatus = %q, want prior ToStatus %q",
						s.id, i, *row.FromStatus, s.hist[i-1].ToStatus)
				}
			}
			for i, row := range s.hist {
				if row.Actor != spineKnownSubject {
					t.Errorf("invoice %s: hist[%d].Actor = %q, want %q", s.id, i, row.Actor, spineKnownSubject)
				}
			}
		}
	})

	// SPINE-04: exactly 6 audit rows per invoice; per-event counts.
	t.Run("SPINE-04_auditLog6RowsPerEvent", func(t *testing.T) {
		for _, s := range samples {
			if len(s.audit) != 6 {
				t.Errorf("invoice %s: audit rows = %d, want 6", s.id, len(s.audit))
			}
			counts := map[string]int{}
			for _, a := range s.audit {
				counts[a.event]++
			}
			want := map[string]int{
				"invoice.created":      1,
				"invoice.transitioned": 4,
				"invoice.validated":    1,
			}
			for event, n := range want {
				if counts[event] != n {
					t.Errorf("invoice %s: audit event %q count = %d, want %d (all counts: %v)",
						s.id, event, counts[event], n, counts)
				}
			}
			if len(counts) != len(want) {
				t.Errorf("invoice %s: audit events seen = %v, want exactly %v", s.id, counts, want)
			}
		}
	})

	// SPINE-05: every audit row's actor == the one known subject.
	t.Run("SPINE-05_auditActorIsKnownSubject", func(t *testing.T) {
		for _, s := range samples {
			for i, a := range s.audit {
				if a.actor != spineKnownSubject {
					t.Errorf("invoice %s: audit[%d] (%s) actor = %q, want %q", s.id, i, a.event, a.actor, spineKnownSubject)
				}
			}
		}
	})

	// SPINE-06: the 4 transitioned payloads decode to the expected edge set.
	t.Run("SPINE-06_transitionedEdgeSet", func(t *testing.T) {
		wantEdges := map[spineEdge]bool{
			{"draft", "validated"}:    true,
			{"validated", "queued"}:   true,
			{"queued", "submitted"}:   true,
			{"submitted", "accepted"}: true,
		}
		for _, s := range samples {
			gotEdges := map[spineEdge]bool{}
			for _, a := range s.audit {
				if a.event != "invoice.transitioned" {
					continue
				}
				var e spineEdge
				if err := json.Unmarshal(a.payload, &e); err != nil {
					t.Fatalf("invoice %s: unmarshal transitioned payload %s: %v", s.id, a.payload, err)
				}
				gotEdges[e] = true
			}
			if len(gotEdges) != len(wantEdges) {
				t.Errorf("invoice %s: transitioned edges = %v, want %v", s.id, gotEdges, wantEdges)
			}
			for e := range wantEdges {
				if !gotEdges[e] {
					t.Errorf("invoice %s: missing transitioned edge %v (got %v)", s.id, e, gotEdges)
				}
			}
		}
	})

	// SPINE-07: tenant scoping. The SAME payload->>'id' query under a second,
	// throwaway tenant's RLS context returns 0 rows; tenant A's own read
	// returned 6 (non-vacuous, so the 0 is real isolation, not an empty DB).
	t.Run("SPINE-07_auditTenantScoped", func(t *testing.T) {
		tenantB := seedTenant(t, super, "SPINE tenant B (throwaway)")
		probeID := samples[0].id

		if got := countAuditForInvoiceUnderTenant(t, app, tenantB, probeID); got != 0 {
			t.Errorf("tenant B saw %d audit rows for tenant A's invoice %s, want 0 (RLS breach)", got, probeID)
		}
		if got := countAuditForInvoiceUnderTenant(t, app, tenantID, probeID); got != 6 {
			t.Errorf("tenant A saw %d audit rows for its own invoice %s, want 6 (non-vacuous guard)", got, probeID)
		}
	})

	// SPINE-08 (adversarial, added by QA M4-13-01): bound the walk's blast radius
	// AND pin the import-only lifecycle shape. A deterministic NON-sampled invoice
	// (highest invoice_number, outside the first-10-ASC sample) must still sit at
	// 'validated' -- the walk advanced ONLY the 10 it targeted -- carrying EXACTLY
	// the import->validate trail: a 2-row history (NULL->draft, draft->validated)
	// and a 3-row audit_log (created + transitioned + validated). No SPINE-01..07
	// spec observes a non-walked invoice's POST-walk state (SPINE-01 counts
	// validated BEFORE the walk), nor the 2/3-row shape of the dominant 490/500
	// import-only path (SPINE-03/04 read only walked invoices, whose chains are
	// 5/6 rows). Actor and edge VALUES are deliberately NOT re-checked here: the
	// import-time draft->validated promotion is the SAME transitionTx and SAME
	// subject that SPINE-03/05/06 already pin via the walked samples -- re-asserting
	// them would be padding.
	t.Run("SPINE-08_nonSampledInvoiceUntouchedAtValidated", func(t *testing.T) {
		id := selectLastInvoiceID(t, app, tenantID, entityID)
		for _, s := range sampleIDs {
			if s == id {
				t.Fatalf("non-sampled id %s is in the walked sample -- picked the wrong row", id)
			}
		}

		got, err := invStore.Get(idCtx, id)
		if err != nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		if got.Status != invoice.StatusValidated {
			t.Errorf("non-sampled invoice %s status = %q, want %q (walk leaked past its sample)", id, got.Status, invoice.StatusValidated)
		}

		hist, err := invStore.History(idCtx, id)
		if err != nil {
			t.Fatalf("History(%s): %v", id, err)
		}
		if len(hist) != 2 {
			t.Fatalf("non-sampled invoice %s: History len = %d, want 2 (genesis + import promotion only)", id, len(hist))
		}
		if hist[0].FromStatus != nil || hist[0].ToStatus != invoice.StatusDraft {
			t.Errorf("non-sampled invoice %s: hist[0] = (%v -> %q), want (nil -> draft)", id, hist[0].FromStatus, hist[0].ToStatus)
		}
		if hist[1].FromStatus == nil || *hist[1].FromStatus != invoice.StatusDraft || hist[1].ToStatus != invoice.StatusValidated {
			t.Errorf("non-sampled invoice %s: hist[1] = (%v -> %q), want (draft -> validated)", id, hist[1].FromStatus, hist[1].ToStatus)
		}

		auditRows := readAuditForInvoice(t, app, tenantID, id)
		if len(auditRows) != 3 {
			t.Errorf("non-sampled invoice %s: audit rows = %d, want 3 (created+transitioned+validated)", id, len(auditRows))
		}
		counts := map[string]int{}
		for _, a := range auditRows {
			counts[a.event]++
		}
		want := map[string]int{"invoice.created": 1, "invoice.transitioned": 1, "invoice.validated": 1}
		for event, n := range want {
			if counts[event] != n {
				t.Errorf("non-sampled invoice %s: audit event %q count = %d, want %d (all: %v)", id, event, counts[event], n, counts)
			}
		}
		if len(counts) != len(want) {
			t.Errorf("non-sampled invoice %s: audit events = %v, want exactly %v", id, counts, want)
		}
	})
}

// statusPtr returns a pointer to s -- for building the expected history chain's
// nullable FromStatus fields.
func statusPtr(s invoice.Status) *invoice.Status { return &s }

// countInvoicesByStatus counts entityID's invoices in a given status, read
// under tenantID's RLS context via the app pool.
func countInvoicesByStatus(t *testing.T, app *pgxpool.Pool, tenantID, entityID, status string) int {
	t.Helper()
	var n int
	if err := db.WithinTenantTx(context.Background(), app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM invoices WHERE entity_id = $1 AND status = $2`, entityID, status,
		).Scan(&n)
	}); err != nil {
		t.Fatalf("count invoices status=%s: %v", status, err)
	}
	return n
}

// selectSampleInvoiceIDs returns the first `limit` invoice ids for entityID
// ordered by invoice_number ASC (INV-SYN-00001.. is deterministic), read under
// tenantID's RLS context.
func selectSampleInvoiceIDs(t *testing.T, app *pgxpool.Pool, tenantID, entityID string, limit int) []string {
	t.Helper()
	var ids []string
	if err := db.WithinTenantTx(context.Background(), app, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(),
			`SELECT id FROM invoices WHERE entity_id = $1 ORDER BY invoice_number ASC LIMIT $2`, entityID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("select sample invoice ids: %v", err)
	}
	return ids
}

// selectLastInvoiceID returns the id of entityID's HIGHEST invoice_number
// (INV-SYN-00500 for green_500) under tenantID's RLS -- a deterministic pick
// GUARANTEED outside the first-10-ASC sample, used by SPINE-08 to prove the walk
// advanced ONLY the invoices it targeted.
func selectLastInvoiceID(t *testing.T, app *pgxpool.Pool, tenantID, entityID string) string {
	t.Helper()
	var id string
	if err := db.WithinTenantTx(context.Background(), app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT id FROM invoices WHERE entity_id = $1 ORDER BY invoice_number DESC LIMIT 1`, entityID,
		).Scan(&id)
	}); err != nil {
		t.Fatalf("select last invoice id: %v", err)
	}
	return id
}

// readAuditForInvoice reads every audit_log row whose payload->>'id' == id,
// under tenantID's RLS context (audit_log has NO invoice_id column, so the
// invoice is correlated through the jsonb payload).
func readAuditForInvoice(t *testing.T, app *pgxpool.Pool, tenantID, id string) []spineAuditRow {
	t.Helper()
	var out []spineAuditRow
	if err := db.WithinTenantTx(context.Background(), app, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(),
			`SELECT actor, event, payload FROM audit_log WHERE payload->>'id' = $1`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a spineAuditRow
			if err := rows.Scan(&a.actor, &a.event, &a.payload); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("read audit_log for invoice %s: %v", id, err)
	}
	return out
}

// countAuditForInvoiceUnderTenant counts audit_log rows for one invoice id as
// seen under an ARBITRARY tenant's RLS context -- the SPINE-07 isolation probe.
func countAuditForInvoiceUnderTenant(t *testing.T, app *pgxpool.Pool, tenantID, id string) int {
	t.Helper()
	var n int
	if err := db.WithinTenantTx(context.Background(), app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM audit_log WHERE payload->>'id' = $1`, id,
		).Scan(&n)
	}); err != nil {
		t.Fatalf("count audit_log for invoice %s under tenant %s: %v", id, tenantID, err)
	}
	return n
}
