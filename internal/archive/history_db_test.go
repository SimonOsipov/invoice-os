// history_db_test.go: RED specs for AUDIT-05-04 (Mode A) -- selectHistory and
// actor resolution against a real Postgres. Reuses entity_db_test.go's
// rollback-wrapped harness (dbSuperPool/beginFixtureTx/actingAs/mustCreateTenant)
// and invoices_db_test.go's mustCreateEntity/mustCreateInvoice, plus this file's
// own history/membership fixtures and a locally-defined sqlRecorder (ported from
// internal/invoice/transition_gate_test.go -- unexported there, unreachable here).
package archive

import (
	"bytes"
	"context"
	"encoding/csv"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- fixtures ----------------------------------------------------------------

// historyFixture is mustCreateHistoryRow's input. changedAt zero means the
// column DEFAULT now() -- needed to plant a REAL tie (two rows in one open tx
// share the exact same transaction-start instant).
type historyFixture struct {
	id                  string
	tenantID, invoiceID string
	fromStatus          *string // nil -> NULL (genesis transition)
	toStatus            string
	actor               string
	changedAt           time.Time
}

func mustCreateHistoryRow(t *testing.T, tx pgx.Tx, f historyFixture) string {
	t.Helper()
	id := f.id
	if id == "" {
		id = uuid.NewString()
	}
	ctx := context.Background()
	var err error
	if f.changedAt.IsZero() {
		_, err = tx.Exec(ctx, `
			INSERT INTO invoice_status_history (id, tenant_id, invoice_id, from_status, to_status, actor)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			id, f.tenantID, f.invoiceID, f.fromStatus, f.toStatus, f.actor)
	} else {
		_, err = tx.Exec(ctx, `
			INSERT INTO invoice_status_history (id, tenant_id, invoice_id, from_status, to_status, actor, changed_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			id, f.tenantID, f.invoiceID, f.fromStatus, f.toStatus, f.actor, f.changedAt)
	}
	if err != nil {
		t.Fatalf("insert invoice_status_history fixture: %v", err)
	}
	return id
}

// mustCreateMembership inserts as superuser. role is always 'admin' -- Resolve
// doesn't care about role, only display_name/email.
func mustCreateMembership(t *testing.T, tx pgx.Tx, tenantID, userID string, displayName, email *string) {
	t.Helper()
	if _, err := tx.Exec(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id, role, display_name, email) VALUES ($1, $2, 'admin', $3, $4)`,
		tenantID, userID, displayName, email); err != nil {
		t.Fatalf("insert memberships fixture: %v", err)
	}
}

// --- CSV parsing helpers -------------------------------------------------------

// wantHistoryHeader is the spec's pinned header, kept LOCAL and literal so
// these tests check real column positions rather than the (deliberately empty
// during Mode A) historyCSVHeader var.
var wantHistoryHeader = []string{
	"invoice_id", "invoice_number", "seq", "from_status", "to_status",
	"actor_name", "actor_kind", "changed_at",
}

func historyColIndex(t *testing.T, column string) int {
	t.Helper()
	for i, h := range wantHistoryHeader {
		if h == column {
			return i
		}
	}
	t.Fatalf("wantHistoryHeader has no column %q", column)
	return -1
}

// historyRowsFor returns every parsed data row (header excluded) for one
// invoice, in file order.
func historyRowsFor(t *testing.T, raw []byte, invoiceID string) [][]string {
	t.Helper()
	rows, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	idIdx := historyColIndex(t, "invoice_id")
	var out [][]string
	for _, row := range rows {
		if len(row) > idIdx && row[idIdx] == invoiceID {
			out = append(out, row)
		}
	}
	return out
}

// --- sqlRecorder / traced pool ------------------------------------------------

// sqlRecorder records the SQL of every statement its pool issues. Ported from
// internal/invoice/transition_gate_test.go -- unexported there, unreachable here.
type sqlRecorder struct {
	mu  sync.Mutex
	sql []string
}

func (r *sqlRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	r.sql = append(r.sql, d.SQL)
	r.mu.Unlock()
	return ctx
}

func (r *sqlRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (r *sqlRecorder) reset() {
	r.mu.Lock()
	r.sql = nil
	r.mu.Unlock()
}

func (r *sqlRecorder) mentioning(substr string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, s := range r.sql {
		if strings.Contains(s, substr) {
			out = append(out, s)
		}
	}
	return out
}

// dbSuperPoolTraced is dbSuperPool plus a query tracer, local to this package --
// internal/invoice's sqlRecorder/tracedAppPool aren't importable across
// packages.
func dbSuperPoolTraced(t *testing.T) (*pgxpool.Pool, *sqlRecorder) {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" || os.Getenv("DATABASE_SUPERUSER_URL") == "" {
		t.Skip("archive db-integration test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-archive`)")
	}
	rec := &sqlRecorder{}
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_SUPERUSER_URL"))
	if err != nil {
		t.Fatalf("parse DATABASE_SUPERUSER_URL: %v", err)
	}
	cfg.ConnConfig.Tracer = rec
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect traced superuser pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping traced superuser pool: %v", err)
	}
	return pool, rec
}

// --- AC-1: order ---------------------------------------------------------------

func TestSelectHistory_EveryTransitionAppearsInChangedAtOrder(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-history-order")
	entity := mustCreateEntity(t, tx, tenant, "History Order Co", "40000001-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-HIST-01"})

	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	draft, validated, queued := "draft", "validated", "queued"
	mustCreateHistoryRow(t, tx, historyFixture{tenantID: tenant, invoiceID: invID, toStatus: "draft", actor: "system", changedAt: base})
	mustCreateHistoryRow(t, tx, historyFixture{tenantID: tenant, invoiceID: invID, fromStatus: &draft, toStatus: "validated", actor: "system", changedAt: base.Add(time.Hour)})
	mustCreateHistoryRow(t, tx, historyFixture{tenantID: tenant, invoiceID: invID, fromStatus: &validated, toStatus: "queued", actor: "system", changedAt: base.Add(2 * time.Hour)})
	mustCreateHistoryRow(t, tx, historyFixture{tenantID: tenant, invoiceID: invID, fromStatus: &queued, toStatus: "submitted", actor: "system", changedAt: base.Add(3 * time.Hour)})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := selectHistory(context.Background(), tx, []string{invID}, w); err != nil {
		t.Fatalf("selectHistory: unexpected error: %v", err)
	}
	w.Flush()

	rows := historyRowsFor(t, buf.Bytes(), invID)
	if len(rows) != 4 {
		t.Fatalf("selectHistory wrote %d rows for the fixture invoice, want 4", len(rows))
	}
	toIdx := historyColIndex(t, "to_status")
	wantOrder := []string{"draft", "validated", "queued", "submitted"}
	for i, want := range wantOrder {
		if rows[i][toIdx] != want {
			t.Errorf("row %d to_status = %q, want %q (changed_at order)", i, rows[i][toIdx], want)
		}
	}
	numIdx := historyColIndex(t, "invoice_number")
	if rows[0][numIdx] != "INV-HIST-01" {
		t.Errorf("row invoice_number = %q, want %q (from invoiceNumbers)", rows[0][numIdx], "INV-HIST-01")
	}
}

// A real tie, not a planted timestamp: both rows omit changed_at, so the column
// DEFAULT now() -- frozen for the whole open transaction -- lands them on the
// exact same instant. Only `id` can then break the tie.
func TestSelectHistory_TiedChangedAtIsBrokenByID(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-history-tie")
	entity := mustCreateEntity(t, tx, tenant, "History Tie Co", "40000002-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-HIST-TIE"})

	const idHigh = "00000000-0000-0000-0000-000000000099"
	const idLow = "00000000-0000-0000-0000-000000000001"
	draft := "draft"
	// idHigh inserted FIRST, on purpose (mirrors TestSelectInvoices_OrdersByCreatedAtThenID):
	// insertion order must not be what decides the tie.
	mustCreateHistoryRow(t, tx, historyFixture{id: idHigh, tenantID: tenant, invoiceID: invID, fromStatus: &draft, toStatus: "queued", actor: "system"})
	mustCreateHistoryRow(t, tx, historyFixture{id: idLow, tenantID: tenant, invoiceID: invID, fromStatus: &draft, toStatus: "validated", actor: "system"})

	// Control needle: confirm the fixture actually planted a real tie before
	// trusting the order assertion below.
	var tHigh, tLow time.Time
	if err := tx.QueryRow(context.Background(), `SELECT changed_at FROM invoice_status_history WHERE id = $1`, idHigh).Scan(&tHigh); err != nil {
		t.Fatalf("control-needle read idHigh changed_at: %v", err)
	}
	if err := tx.QueryRow(context.Background(), `SELECT changed_at FROM invoice_status_history WHERE id = $1`, idLow).Scan(&tLow); err != nil {
		t.Fatalf("control-needle read idLow changed_at: %v", err)
	}
	if !tHigh.Equal(tLow) {
		t.Fatalf("control needle: idHigh changed_at=%v != idLow changed_at=%v -- fixture failed to plant a real tie", tHigh, tLow)
	}

	actingAs(t, tx, tenant)
	toIdx := historyColIndex(t, "to_status")
	for attempt := 1; attempt <= 2; attempt++ {
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		if err := selectHistory(context.Background(), tx, []string{invID}, w); err != nil {
			t.Fatalf("selectHistory (attempt %d): unexpected error: %v", attempt, err)
		}
		w.Flush()
		rows := historyRowsFor(t, buf.Bytes(), invID)
		if len(rows) != 2 {
			t.Fatalf("selectHistory (attempt %d) wrote %d rows, want 2 (the tied pair)", attempt, len(rows))
		}
		if rows[0][toIdx] != "validated" || rows[1][toIdx] != "queued" {
			t.Errorf("selectHistory (attempt %d) tied order = [%q %q], want [validated queued] (id tie-break: %s < %s)",
				attempt, rows[0][toIdx], rows[1][toIdx], idLow, idHigh)
		}
	}
}

// --- AC-2: seq restarts per invoice --------------------------------------------

func TestSelectHistory_SeqRestartsPerInvoice(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-history-seq")
	entity := mustCreateEntity(t, tx, tenant, "History Seq Co", "40000003-0001")
	invA := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-SEQ-A"})
	invB := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-SEQ-B"})

	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	draft, validated := "draft", "validated"
	for i, inv := range []string{invA, invB} {
		off := time.Duration(i) * 10 * time.Minute
		mustCreateHistoryRow(t, tx, historyFixture{tenantID: tenant, invoiceID: inv, toStatus: "draft", actor: "system", changedAt: base.Add(off)})
		mustCreateHistoryRow(t, tx, historyFixture{tenantID: tenant, invoiceID: inv, fromStatus: &draft, toStatus: "validated", actor: "system", changedAt: base.Add(off + time.Hour)})
		mustCreateHistoryRow(t, tx, historyFixture{tenantID: tenant, invoiceID: inv, fromStatus: &validated, toStatus: "queued", actor: "system", changedAt: base.Add(off + 2*time.Hour)})
	}

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := selectHistory(context.Background(), tx, []string{invA, invB}, w); err != nil {
		t.Fatalf("selectHistory: unexpected error: %v", err)
	}
	w.Flush()

	seqIdx := historyColIndex(t, "seq")
	for _, inv := range []string{invA, invB} {
		rows := historyRowsFor(t, buf.Bytes(), inv)
		if len(rows) != 3 {
			t.Fatalf("invoice %s: selectHistory wrote %d rows, want 3", inv, len(rows))
		}
		for i, want := range []string{"1", "2", "3"} {
			if rows[i][seqIdx] != want {
				t.Errorf("invoice %s row %d seq = %q, want %q", inv, i, rows[i][seqIdx], want)
			}
		}
	}
}

// --- AC-3/4/5: actor ladder ------------------------------------------------------

func TestSelectHistory_SystemActorRendersSystem(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-history-system-actor")
	entity := mustCreateEntity(t, tx, tenant, "System Actor Co", "40000004-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-SYS-01"})
	mustCreateHistoryRow(t, tx, historyFixture{tenantID: tenant, invoiceID: invID, toStatus: "draft", actor: "system", changedAt: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := selectHistory(context.Background(), tx, []string{invID}, w); err != nil {
		t.Fatalf("selectHistory: unexpected error: %v", err)
	}
	w.Flush()

	rows := historyRowsFor(t, buf.Bytes(), invID)
	if len(rows) != 1 {
		t.Fatalf("selectHistory wrote %d rows, want 1", len(rows))
	}
	nameIdx, kindIdx := historyColIndex(t, "actor_name"), historyColIndex(t, "actor_kind")
	if rows[0][nameIdx] != "System" || rows[0][kindIdx] != "system" {
		t.Errorf("actor_name/actor_kind = %q/%q, want System/system", rows[0][nameIdx], rows[0][kindIdx])
	}
}

func TestSelectHistory_SubjectResolvesToDisplayName(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-history-display-name")
	entity := mustCreateEntity(t, tx, tenant, "Display Name Co", "40000005-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-DISPLAY-01"})
	userID := uuid.NewString()
	displayName := "Adaeze Okafor"
	mustCreateMembership(t, tx, tenant, userID, &displayName, nil)
	mustCreateHistoryRow(t, tx, historyFixture{tenantID: tenant, invoiceID: invID, toStatus: "validated", actor: userID, changedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := selectHistory(context.Background(), tx, []string{invID}, w); err != nil {
		t.Fatalf("selectHistory: unexpected error: %v", err)
	}
	w.Flush()

	rows := historyRowsFor(t, buf.Bytes(), invID)
	if len(rows) != 1 {
		t.Fatalf("selectHistory wrote %d rows, want 1", len(rows))
	}
	nameIdx, kindIdx := historyColIndex(t, "actor_name"), historyColIndex(t, "actor_kind")
	if rows[0][nameIdx] != displayName || rows[0][kindIdx] != "person" {
		t.Errorf("actor_name/actor_kind = %q/%q, want %q/person", rows[0][nameIdx], rows[0][kindIdx], displayName)
	}
}

func TestSelectHistory_NullDisplayNameFallsToEmail(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-history-email-fallback")
	entity := mustCreateEntity(t, tx, tenant, "Email Fallback Co", "40000006-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-EMAIL-01"})
	userID := uuid.NewString()
	email := "no-display-name@example.com"
	mustCreateMembership(t, tx, tenant, userID, nil, &email)
	mustCreateHistoryRow(t, tx, historyFixture{tenantID: tenant, invoiceID: invID, toStatus: "validated", actor: userID, changedAt: time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := selectHistory(context.Background(), tx, []string{invID}, w); err != nil {
		t.Fatalf("selectHistory: unexpected error: %v", err)
	}
	w.Flush()

	rows := historyRowsFor(t, buf.Bytes(), invID)
	if len(rows) != 1 {
		t.Fatalf("selectHistory wrote %d rows, want 1", len(rows))
	}
	nameIdx, kindIdx := historyColIndex(t, "actor_name"), historyColIndex(t, "actor_kind")
	if rows[0][nameIdx] != email || rows[0][kindIdx] != "person" {
		t.Errorf("actor_name/actor_kind = %q/%q, want %q/person (NULL display_name falls back to email)", rows[0][nameIdx], rows[0][kindIdx], email)
	}
}

func TestSelectHistory_UnknownSubjectStaysRaw(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-history-unknown-subject")
	entity := mustCreateEntity(t, tx, tenant, "Unknown Subject Co", "40000007-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-UNKNOWN-01"})
	stranger := uuid.NewString() // no membership row planted
	mustCreateHistoryRow(t, tx, historyFixture{tenantID: tenant, invoiceID: invID, toStatus: "validated", actor: stranger, changedAt: time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := selectHistory(context.Background(), tx, []string{invID}, w); err != nil {
		t.Fatalf("selectHistory: unexpected error: %v", err)
	}
	w.Flush()

	rows := historyRowsFor(t, buf.Bytes(), invID)
	if len(rows) != 1 {
		t.Fatalf("selectHistory wrote %d rows, want 1", len(rows))
	}
	nameIdx, kindIdx := historyColIndex(t, "actor_name"), historyColIndex(t, "actor_kind")
	if rows[0][nameIdx] != stranger || rows[0][kindIdx] != "raw" {
		t.Errorf("actor_name/actor_kind = %q/%q, want %q/raw (uuid nothing can name -- never a fabricated name)", rows[0][nameIdx], rows[0][kindIdx], stranger)
	}
}

func TestSelectHistory_FreeTextActorSurvives(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-history-free-text")
	entity := mustCreateEntity(t, tx, tenant, "Free Text Co", "40000008-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-FREETEXT-01"})
	mustCreateHistoryRow(t, tx, historyFixture{tenantID: tenant, invoiceID: invID, toStatus: "validated", actor: "backfill-source-rows", changedAt: time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := selectHistory(context.Background(), tx, []string{invID}, w); err != nil {
		t.Fatalf("selectHistory: unexpected error: %v (free text must never panic or error)", err)
	}
	w.Flush()

	rows := historyRowsFor(t, buf.Bytes(), invID)
	if len(rows) != 1 {
		t.Fatalf("selectHistory wrote %d rows, want 1", len(rows))
	}
	nameIdx, kindIdx := historyColIndex(t, "actor_name"), historyColIndex(t, "actor_kind")
	if rows[0][nameIdx] != "backfill-source-rows" || rows[0][kindIdx] != "raw" {
		t.Errorf("actor_name/actor_kind = %q/%q, want backfill-source-rows/raw", rows[0][nameIdx], rows[0][kindIdx])
	}
}

// --- AC-6: one resolve per chunk, not per row -------------------------------------

func TestSelectHistory_ResolvesOncePerChunkNotPerRow(t *testing.T) {
	super, rec := dbSuperPoolTraced(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-history-batching")
	entity := mustCreateEntity(t, tx, tenant, "Batching Co", "40000009-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-BATCH-01"})

	userA, userB := uuid.NewString(), uuid.NewString()
	nameA, nameB := "Member A", "Member B"
	mustCreateMembership(t, tx, tenant, userA, &nameA, nil)
	mustCreateMembership(t, tx, tenant, userB, &nameB, nil)
	subjects := []string{userA, userB, "system"}

	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	draft := "draft"
	for i := 0; i < 60; i++ {
		var from *string
		if i > 0 {
			from = &draft
		}
		mustCreateHistoryRow(t, tx, historyFixture{
			tenantID: tenant, invoiceID: invID, fromStatus: from, toStatus: "validated",
			actor: subjects[i%len(subjects)], changedAt: base.Add(time.Duration(i+1) * time.Second),
		})
	}

	actingAs(t, tx, tenant)
	rec.reset()
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := selectHistory(context.Background(), tx, []string{invID}, w); err != nil {
		t.Fatalf("selectHistory: unexpected error: %v", err)
	}
	w.Flush()

	rows := historyRowsFor(t, buf.Bytes(), invID)
	if len(rows) != 60 {
		t.Fatalf("selectHistory wrote %d rows, want 60", len(rows))
	}
	distinct := map[string]bool{}
	for _, s := range subjects {
		distinct[s] = true
	}
	if len(distinct) != 3 {
		t.Fatalf("fixture carries %d distinct actors, want 3 -- a one-statement claim over one actor proves nothing", len(distinct))
	}
	if n := len(rec.mentioning("memberships")); n != 1 {
		t.Errorf("selectHistory over %d rows / %d distinct actors issued %d memberships statement(s), want exactly 1 (AC-6); all: %q",
			len(rows), len(distinct), n, rec.mentioning("memberships"))
	}
	if n := len(rec.mentioning("invoice_status_history")); n != 1 {
		t.Errorf("selectHistory issued %d invoice_status_history statement(s), want exactly 1 -- resolution must not re-read the rows", n)
	}
}

// --- AC-1 (RLS): cross-tenant -----------------------------------------------------

func TestRLS_SelectHistoryCannotReachAnotherTenantsInvoice(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenantA := mustCreateTenant(t, tx, "archive-history-rls-a")
	tenantB := mustCreateTenant(t, tx, "archive-history-rls-b")
	entityA := mustCreateEntity(t, tx, tenantA, "RLS History Co", "40000010-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenantA, entityID: entityA, invoiceNumber: "INV-RLS-HIST-01"})
	draft := "draft"
	mustCreateHistoryRow(t, tx, historyFixture{tenantID: tenantA, invoiceID: invID, toStatus: "draft", actor: "system", changedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)})
	mustCreateHistoryRow(t, tx, historyFixture{tenantID: tenantA, invoiceID: invID, fromStatus: &draft, toStatus: "validated", actor: "system", changedAt: time.Date(2026, 7, 1, 1, 0, 0, 0, time.UTC)})

	// Control needle (superuser, pre-actingAs): the fixture really planted 2 rows.
	var planted int
	if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID).Scan(&planted); err != nil {
		t.Fatalf("control-needle count: %v", err)
	}
	if planted != 2 {
		t.Fatalf("control needle: planted %d history rows, want 2 -- fixture setup is broken", planted)
	}

	actingAs(t, tx, tenantB)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	err := selectHistory(context.Background(), tx, []string{invID}, w)
	if err != nil {
		t.Errorf("selectHistory(another tenant's invoice) error = %v, want nil (RLS: no error, just no rows)", err)
	}
	w.Flush()
	rows := historyRowsFor(t, buf.Bytes(), invID)
	if len(rows) != 0 {
		t.Errorf("selectHistory(another tenant's invoice) rows = %v, want none", rows)
	}
}

// --- D-8: genesis NULL from_status ------------------------------------------------

func TestSelectHistory_GenesisNullFromStatusIsEmptyCell(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-history-genesis")
	entity := mustCreateEntity(t, tx, tenant, "Genesis Co", "40000011-0001")
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-GENESIS-01"})
	mustCreateHistoryRow(t, tx, historyFixture{tenantID: tenant, invoiceID: invID, toStatus: "draft", actor: "system", changedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := selectHistory(context.Background(), tx, []string{invID}, w); err != nil {
		t.Fatalf("selectHistory: unexpected error: %v", err)
	}
	w.Flush()

	if raw := buf.String(); strings.Contains(raw, "null") {
		t.Errorf("csv contains the literal string \"null\" (D-8 forbids it): %q", raw)
	}
	rows := historyRowsFor(t, buf.Bytes(), invID)
	if len(rows) != 1 {
		t.Fatalf("selectHistory wrote %d rows, want 1", len(rows))
	}
	fromIdx := historyColIndex(t, "from_status")
	if rows[0][fromIdx] != "" {
		t.Errorf("genesis row from_status cell = %q, want empty (D-8: NULL -> empty cell, never the string null)", rows[0][fromIdx])
	}
}
