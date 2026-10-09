package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type staffRow struct {
	ID        int64
	Actor     string
	Version   string
	Event     string
	Payload   string
	CreatedAt time.Time
}

// requireStaffTable fails (not skips) when the migration is missing, so a red run names the cause.
func requireStaffTable(t *testing.T, f *fixture) {
	t.Helper()
	var present bool
	if err := f.mig.QueryRow(context.Background(),
		`SELECT to_regclass('public.staff_audit_log') IS NOT NULL`).Scan(&present); err != nil {
		t.Fatalf("look up public.staff_audit_log: %v", err)
	}
	if !present {
		t.Fatal("public.staff_audit_log does not exist: the staff audit migration is not applied")
	}
}

// staffRows reads as the owner: invoice_app cannot SELECT and the table has no RLS.
func staffRows(t *testing.T, f *fixture, actor uuid.UUID) []staffRow {
	t.Helper()
	rows, err := f.mig.Query(context.Background(),
		`SELECT id, actor::text, rule_set_version_id::text, event, payload::text, created_at
		   FROM public.staff_audit_log WHERE actor = $1 ORDER BY id`, actor.String())
	if err != nil {
		t.Fatalf("read staff_audit_log: %v", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[staffRow])
	if err != nil {
		t.Fatalf("collect staff_audit_log rows: %v", err)
	}
	return out
}

// seedStaffRow inserts through invoice_app with SQL, so the DB-level tests do not depend on RecordStaff.
func seedStaffRow(t *testing.T, f *fixture, actor, version uuid.UUID, event string) {
	t.Helper()
	if _, err := f.app.Exec(context.Background(),
		`INSERT INTO public.staff_audit_log (actor, event, rule_set_version_id) VALUES ($1, $2, $3)`,
		actor.String(), event, version.String()); err != nil {
		t.Fatalf("seed staff_audit_log as invoice_app: %v", err)
	}
	if n := len(staffRows(t, f, actor)); n != 1 {
		t.Fatalf("seeded staff rows for the actor = %d, want 1", n)
	}
}

// withAppTx runs fn in a raw invoice_app transaction (no tenant) and commits when fn succeeds.
func withAppTx(t *testing.T, f *fixture, fn func(tx pgx.Tx) error) error {
	t.Helper()
	ctx := context.Background()
	tx, err := f.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin app tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func recordStaffCommitted(t *testing.T, f *fixture, actor, version uuid.UUID, event string, payload any) {
	t.Helper()
	if err := withAppTx(t, f, func(tx pgx.Tx) error {
		return audit.RecordStaff(context.Background(), tx, actor, version, event, payload)
	}); err != nil {
		t.Fatalf("RecordStaff: %v", err)
	}
}

func assertCheckViolation(t *testing.T, err error, constraint string) {
	t.Helper()
	assertSQLState(t, err, "23514")
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName != constraint {
		t.Errorf("violated constraint = %q, want %q", pgErr.ConstraintName, constraint)
	}
}

// AC-1.
func TestStaffAudit_RecordStaffInsertsRow(t *testing.T) {
	f := requireFixture(t)
	requireStaffTable(t, f)
	actor, version := uuid.New(), uuid.New()

	recordStaffCommitted(t, f, actor, version, "rules.test", map[string]any{"k": "v"})

	rows := staffRows(t, f, actor)
	if len(rows) != 1 {
		t.Fatalf("staff rows for the actor = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Actor != actor.String() || r.Version != version.String() || r.Event != "rules.test" {
		t.Errorf("row = actor %s version %s event %q, want %s %s rules.test", r.Actor, r.Version, r.Event, actor, version)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(r.Payload), &payload); err != nil || !reflect.DeepEqual(payload, map[string]any{"k": "v"}) {
		t.Errorf("payload = %s (%v), want {\"k\": \"v\"}", r.Payload, err)
	}
	if r.ID <= 0 {
		t.Errorf("id = %d, want a positive bigserial default", r.ID)
	}
	if r.CreatedAt.IsZero() {
		t.Error("created_at is zero, want the now() default")
	}
}

// AC-1.
func TestStaffAudit_NilPayloadIsEmptyObject(t *testing.T) {
	f := requireFixture(t)
	requireStaffTable(t, f)
	actor := uuid.New()

	recordStaffCommitted(t, f, actor, uuid.New(), "rules.test", nil)

	rows := staffRows(t, f, actor)
	if len(rows) != 1 {
		t.Fatalf("staff rows for the actor = %d, want 1", len(rows))
	}
	if rows[0].Payload != "{}" {
		t.Errorf("nil payload stored as %s, want {}", rows[0].Payload)
	}
}

// AC-1. The tx is healthy after the marshal error, so it commits: the zero rows are RecordStaff's doing.
func TestStaffAudit_UnmarshallablePayloadWritesNothing(t *testing.T) {
	f := requireFixture(t)
	requireStaffTable(t, f)
	ctx := context.Background()
	actor, version := uuid.New(), uuid.New()

	tx, err := f.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin app tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	recErr := audit.RecordStaff(ctx, tx, actor, version, "rules.test", map[string]any{"c": make(chan int)})
	if recErr == nil || !strings.Contains(recErr.Error(), "audit: marshal payload") {
		t.Errorf("RecordStaff error = %v, want one holding %q", recErr, "audit: marshal payload")
	}
	var unsupported *json.UnsupportedTypeError
	if !errors.As(recErr, &unsupported) {
		t.Errorf("RecordStaff error %v does not wrap the json marshal error", recErr)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit after the marshal error: %v", err)
	}
	if n := len(staffRows(t, f, actor)); n != 0 {
		t.Errorf("staff rows after the marshal error = %d, want 0", n)
	}

	recordStaffCommitted(t, f, actor, version, "rules.test", map[string]any{"ok": true})
	if n := len(staffRows(t, f, actor)); n != 1 {
		t.Errorf("staff rows after a valid payload for the same actor = %d, want 1", n)
	}
}

// AC-2.
func TestStaffAudit_AtomicWithCallerTx(t *testing.T) {
	f := requireFixture(t)
	requireStaffTable(t, f)
	ctx := context.Background()
	rolledBack, committed := uuid.New(), uuid.New()

	err := withAppTx(t, f, func(tx pgx.Tx) error {
		if e := audit.RecordStaff(ctx, tx, rolledBack, uuid.New(), "rules.test", nil); e != nil {
			return e
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("tx err = %v, want errRollback", err)
	}
	if n := len(staffRows(t, f, rolledBack)); n != 0 {
		t.Errorf("staff rows after rollback = %d, want 0", n)
	}

	recordStaffCommitted(t, f, committed, uuid.New(), "rules.test", nil)
	if n := len(staffRows(t, f, committed)); n != 1 {
		t.Errorf("staff rows after commit = %d, want 1", n)
	}
}

// AC-3. The seeded row is matched, so a refusal is the missing grant and not an empty table.
func TestStaffAudit_AppCanOnlyInsert(t *testing.T) {
	f := requireFixture(t)
	requireStaffTable(t, f)
	ctx := context.Background()
	actor := uuid.New()
	seedStaffRow(t, f, actor, uuid.New(), "rules.test")
	before := staffRows(t, f, actor)

	for _, stmt := range []struct{ verb, sql string }{
		{"SELECT", `SELECT id FROM public.staff_audit_log`},
		{"UPDATE", `UPDATE public.staff_audit_log SET event = 'tampered'`},
		{"DELETE", `DELETE FROM public.staff_audit_log`},
		{"TRUNCATE", `TRUNCATE public.staff_audit_log`},
	} {
		t.Run(stmt.verb, func(t *testing.T) {
			_, err := f.app.Exec(ctx, stmt.sql)
			assertSQLState(t, err, "42501")
		})
	}
	if after := staffRows(t, f, actor); !reflect.DeepEqual(before, after) {
		t.Errorf("row after the refused statements = %v, want %v", after, before)
	}
}

// AC-4. The owner holds every privilege, so 23001 can only come from the trigger.
func TestStaffAudit_ImmutableToOwner(t *testing.T) {
	f := requireFixture(t)
	requireStaffTable(t, f)
	ctx := context.Background()
	actor := uuid.New()
	seedStaffRow(t, f, actor, uuid.New(), "rules.test")
	before := staffRows(t, f, actor)

	for _, stmt := range []struct {
		verb, sql string
		args      []any
	}{
		{"UPDATE", `UPDATE public.staff_audit_log SET event = 'tampered' WHERE actor = $1`, []any{actor.String()}},
		{"DELETE", `DELETE FROM public.staff_audit_log WHERE actor = $1`, []any{actor.String()}},
		{"TRUNCATE", `TRUNCATE public.staff_audit_log`, nil},
	} {
		t.Run(stmt.verb, func(t *testing.T) {
			_, err := f.mig.Exec(ctx, stmt.sql, stmt.args...)
			assertSQLState(t, err, "23001")
			if !strings.Contains(err.Error(), "staff_audit_log") {
				t.Errorf("error %q does not name staff_audit_log, want its own trigger function's message", err)
			}
		})
	}
	if after := staffRows(t, f, actor); !reflect.DeepEqual(before, after) {
		t.Errorf("row after the refused owner statements = %v, want %v", after, before)
	}
}

// AC-5. Each case names the constraint it violates, so no case passes through another check.
func TestStaffAudit_ChecksRefuseNilAndBlank(t *testing.T) {
	f := requireFixture(t)
	requireStaffTable(t, f)
	ctx := context.Background()

	for _, tc := range []struct {
		name       string
		nilActor   bool
		nilVersion bool
		event      string
		constraint string
	}{
		{"nil_actor", true, false, "rules.test", "staff_audit_actor_set"},
		{"nil_version", false, true, "rules.test", "staff_audit_version_set"},
		{"empty_event", false, false, "", "staff_audit_event_length"},
		{"event_128_chars", false, false, strings.Repeat("e", 128), "staff_audit_event_length"},
		{"event_128_multibyte_chars", false, false, strings.Repeat("é", 128), "staff_audit_event_length"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor, version := uuid.New(), uuid.New()
			if tc.nilActor {
				actor = uuid.Nil
			}
			if tc.nilVersion {
				version = uuid.Nil
			}
			err := withAppTx(t, f, func(tx pgx.Tx) error {
				return audit.RecordStaff(ctx, tx, actor, version, tc.event, nil)
			})
			assertCheckViolation(t, err, tc.constraint)
			if !tc.nilActor {
				if n := len(staffRows(t, f, actor)); n != 0 {
					t.Errorf("staff rows after the refusal = %d, want 0", n)
				}
			}
		})
	}

	for _, tc := range []struct{ name, event string }{
		{"event_127_chars", strings.Repeat("e", 127)},
		{"event_127_multibyte_chars", strings.Repeat("é", 127)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor := uuid.New()
			recordStaffCommitted(t, f, actor, uuid.New(), tc.event, nil)
			rows := staffRows(t, f, actor)
			if len(rows) != 1 || rows[0].Event != tc.event {
				t.Errorf("staff rows = %v, want the one 127-character event", rows)
			}
		})
	}
}

// AC-6. A customer audit row from the same tx is the control: auditCount sees that one and not the staff event.
func TestStaffAudit_TenantTxWritesNoCustomerAuditRow(t *testing.T) {
	f := requireFixture(t)
	requireStaffTable(t, f)
	ctx := context.Background()
	tenant, actor, version := uuid.NewString(), uuid.New(), uuid.New()
	staffEvent, customerEvent := uuid.NewString(), uuid.NewString()

	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		if e := audit.Record(ctx, tx, "actor", customerEvent, nil); e != nil {
			return e
		}
		return audit.RecordStaff(ctx, tx, actor, version, staffEvent, nil)
	}); err != nil {
		t.Fatalf("customer audit plus RecordStaff in a tenant tx: %v", err)
	}

	if n := auditCount(t, f.app, tenant, customerEvent); n != 1 {
		t.Fatalf("customer audit rows for the control event = %d, want 1", n)
	}
	if n := auditCount(t, f.app, tenant, staffEvent); n != 0 {
		t.Errorf("customer audit rows for the staff event = %d, want 0", n)
	}
	rows := staffRows(t, f, actor)
	if len(rows) != 1 || rows[0].Event != staffEvent {
		t.Errorf("staff rows = %v, want one with event %s", rows, staffEvent)
	}
}

// AC-6. The seeded row exists for the migrator, so the refusal is the grant and not an empty table.
func TestStaffAudit_TenantTxCannotReadStaffRows(t *testing.T) {
	f := requireFixture(t)
	requireStaffTable(t, f)
	ctx := context.Background()
	actor := uuid.New()
	seedStaffRow(t, f, actor, uuid.New(), "rules.test")

	err := db.WithinTenantTx(ctx, f.app, uuid.NewString(), func(tx pgx.Tx) error {
		var n int
		return tx.QueryRow(ctx, `SELECT count(*) FROM public.staff_audit_log`).Scan(&n)
	})
	assertSQLState(t, err, "42501")
}
