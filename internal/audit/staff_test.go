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
	Version   *string
	Event     string
	Payload   string
	CreatedAt time.Time
	Tenant    *string
	Subject   *string
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
		`SELECT id, actor::text, rule_set_version_id::text, event, payload::text, created_at, tenant_id::text, subject_id::text
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

func recordStaffCommitted(t *testing.T, f *fixture, ev audit.StaffEvent) {
	t.Helper()
	if err := withAppTx(t, f, func(tx pgx.Tx) error {
		return audit.RecordStaff(context.Background(), tx, ev)
	}); err != nil {
		t.Fatalf("RecordStaff: %v", err)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<NULL>"
	}
	return *s
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

	recordStaffCommitted(t, f, audit.StaffEvent{Actor: actor, Event: "rules.test", RuleSetVersionID: version, Payload: map[string]any{"k": "v"}})

	rows := staffRows(t, f, actor)
	if len(rows) != 1 {
		t.Fatalf("staff rows for the actor = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Actor != actor.String() || r.Version == nil || *r.Version != version.String() || r.Event != "rules.test" {
		t.Errorf("row = actor %s version %s event %q, want %s %s rules.test", r.Actor, deref(r.Version), r.Event, actor, version)
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

	recordStaffCommitted(t, f, audit.StaffEvent{Actor: actor, Event: "rules.test", RuleSetVersionID: uuid.New()})

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
	recErr := audit.RecordStaff(ctx, tx, audit.StaffEvent{Actor: actor, Event: "rules.test", RuleSetVersionID: version, Payload: map[string]any{"c": make(chan int)}})
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

	recordStaffCommitted(t, f, audit.StaffEvent{Actor: actor, Event: "rules.test", RuleSetVersionID: version, Payload: map[string]any{"ok": true}})
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
		if e := audit.RecordStaff(ctx, tx, audit.StaffEvent{Actor: rolledBack, Event: "rules.test", RuleSetVersionID: uuid.New()}); e != nil {
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

	recordStaffCommitted(t, f, audit.StaffEvent{Actor: committed, Event: "rules.test", RuleSetVersionID: uuid.New()})
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
		event      string
		constraint string
	}{
		{"nil_actor", true, "rules.test", "staff_audit_actor_set"},
		{"empty_event", false, "", "staff_audit_event_length"},
		{"event_128_chars", false, strings.Repeat("e", 128), "staff_audit_event_length"},
		{"event_128_multibyte_chars", false, strings.Repeat("é", 128), "staff_audit_event_length"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor := uuid.New()
			if tc.nilActor {
				actor = uuid.Nil
			}
			err := withAppTx(t, f, func(tx pgx.Tx) error {
				return audit.RecordStaff(ctx, tx, audit.StaffEvent{Actor: actor, Event: tc.event, RuleSetVersionID: uuid.New()})
			})
			assertCheckViolation(t, err, tc.constraint)
			if err != nil && !strings.Contains(err.Error(), "audit: record staff event") {
				t.Errorf("error %q does not carry the %q wrap", err, "audit: record staff event")
			}
			if !tc.nilActor {
				if n := len(staffRows(t, f, actor)); n != 0 {
					t.Errorf("staff rows after the refusal = %d, want 0", n)
				}
			}
		})
	}

	for _, tc := range []struct {
		name, event string
		zeroVersion bool
	}{
		{"event_127_chars", strings.Repeat("e", 127), false},
		{"event_127_multibyte_chars", strings.Repeat("é", 127), false},
		{"zero_version", "rules.test", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor, version := uuid.New(), uuid.New()
			if tc.zeroVersion {
				version = uuid.Nil
			}
			recordStaffCommitted(t, f, audit.StaffEvent{Actor: actor, Event: tc.event, RuleSetVersionID: version})
			rows := staffRows(t, f, actor)
			if len(rows) != 1 || rows[0].Event != tc.event {
				t.Errorf("staff rows = %v, want the one accepted event", rows)
			}
		})
	}
}

func TestStaffAudit_ZeroRuleSetVersionIsStoredAsNull(t *testing.T) {
	f := requireFixture(t)
	requireStaffTable(t, f)
	actor := uuid.New()

	recordStaffCommitted(t, f, audit.StaffEvent{Actor: actor, Event: "rules.test"})

	rows := staffRows(t, f, actor)
	if len(rows) != 1 {
		t.Fatalf("staff rows for the actor = %d, want 1", len(rows))
	}
	if r := rows[0]; r.Version != nil || r.Tenant != nil || r.Subject != nil {
		t.Errorf("rule_set_version_id, tenant_id, subject_id = %v, %v, %v, want NULL, NULL, NULL", r.Version, r.Tenant, r.Subject)
	}
}

// auditRow is one audit_log row read inside the tenant's own transaction.
type auditRow struct {
	Actor     string
	Payload   string
	CreatedAt time.Time
}

func auditRowsFor(t *testing.T, f *fixture, tenant, event string) []auditRow {
	t.Helper()
	var out []auditRow
	if err := db.WithinTenantTx(context.Background(), f.app, tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(),
			`SELECT actor, payload::text, created_at FROM audit_log WHERE event = $1 ORDER BY id`, event)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[auditRow])
		return err
	}); err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	return out
}

// supportRowCount counts the tenant's audit_log rows written as the support actor.
func supportRowCount(t *testing.T, f *fixture, tenant string) int {
	t.Helper()
	var n int
	if err := db.WithinTenantTx(context.Background(), f.app, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE actor = $1`, audit.SupportActor).Scan(&n)
	}); err != nil {
		t.Fatalf("count support rows: %v", err)
	}
	return n
}

func insertKey(ctx context.Context, tx pgx.Tx, tenant, key string) error {
	_, err := tx.Exec(ctx, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, $2)`, tenant, key)
	return err
}

func jsonEq(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil || json.Unmarshal([]byte(want), &w) != nil || !reflect.DeepEqual(g, w) {
		t.Errorf("payload = %s, want %s", got, want)
	}
}

// AC-1, AC-2, AC-8.
func TestStaffAudit_TenantChangeWritesBothRowsInOneTx(t *testing.T) {
	f := requireFixture(t)
	requireStaffTable(t, f)
	ctx := context.Background()
	tenant, other := uuid.NewString(), uuid.NewString()
	staff, subject := uuid.New(), uuid.New()
	event, key := uuid.NewString(), uuid.NewString()

	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		if e := insertKey(ctx, tx, tenant, key); e != nil {
			return e
		}
		return audit.RecordStaff(ctx, tx, audit.StaffEvent{
			Actor: staff, Event: event, TenantID: uuid.MustParse(tenant), SubjectID: subject, Payload: map[string]any{"reason": "r"},
		})
	}); err != nil {
		t.Fatalf("tenant change: %v", err)
	}

	srows := staffRows(t, f, staff)
	if len(srows) != 1 {
		t.Fatalf("staff rows = %d, want 1", len(srows))
	}
	s := srows[0]
	if s.Event != event || s.Actor != staff.String() || s.Version != nil || deref(s.Tenant) != tenant || deref(s.Subject) != subject.String() {
		t.Errorf("staff row = event %q actor %s version %s tenant %s subject %s, want %q %s <NULL> %s %s",
			s.Event, s.Actor, deref(s.Version), deref(s.Tenant), deref(s.Subject), event, staff, tenant, subject)
	}
	jsonEq(t, s.Payload, `{"reason":"r"}`)

	arows := auditRowsFor(t, f, tenant, event)
	if len(arows) != 1 {
		t.Fatalf("audit_log rows in the tenant = %d, want 1", len(arows))
	}
	a := arows[0]
	if a.Actor != audit.SupportActor {
		t.Errorf("audit_log actor = %q, want %q", a.Actor, audit.SupportActor)
	}
	jsonEq(t, a.Payload, `{"reason":"r"}`)
	if !a.CreatedAt.Equal(s.CreatedAt) {
		t.Errorf("created_at: audit_log %v, staff_audit_log %v, want equal", a.CreatedAt, s.CreatedAt)
	}
	if n := keyCount(t, f.app, tenant, key); n != 1 {
		t.Errorf("companion key rows = %d, want 1", n)
	}
	if n := auditCount(t, f.app, other, event); n != 0 {
		t.Errorf("another tenant sees %d rows for the event, want 0", n)
	}
	// AC-2: the staff UUID reaches the customer's table nowhere.
	if strings.Contains(a.Actor, staff.String()) || strings.Contains(a.Payload, staff.String()) {
		t.Errorf("audit_log row %+v holds the staff UUID %s", a, staff)
	}

	// The staff UUID is in no column of any audit_log row the tenant can read.
	var leaks int
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE audit_log::text LIKE '%' || $1 || '%'`, staff.String()).Scan(&leaks)
	}); err != nil {
		t.Fatalf("scan audit_log for the staff UUID: %v", err)
	}
	if leaks != 0 {
		t.Errorf("audit_log rows holding the staff UUID = %d, want 0", leaks)
	}

	t.Run("nil_payload_is_an_empty_object_on_both_rows", func(t *testing.T) {
		staff, event := uuid.New(), uuid.NewString()
		if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
			return audit.RecordStaff(ctx, tx, audit.StaffEvent{Actor: staff, Event: event, TenantID: uuid.MustParse(tenant)})
		}); err != nil {
			t.Fatalf("tenant change without a payload: %v", err)
		}
		srows, arows := staffRows(t, f, staff), auditRowsFor(t, f, tenant, event)
		if len(srows) != 1 || len(arows) != 1 {
			t.Fatalf("rows: staff %d audit %d, want 1 and 1", len(srows), len(arows))
		}
		jsonEq(t, srows[0].Payload, `{}`)
		jsonEq(t, arows[0].Payload, `{}`)
	})

	t.Run("unmarshallable_payload_writes_nothing", func(t *testing.T) {
		staff, event := uuid.New(), uuid.NewString()
		err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
			return audit.RecordStaff(ctx, tx, audit.StaffEvent{Actor: staff, Event: event, TenantID: uuid.MustParse(tenant), Payload: map[string]any{"c": make(chan int)}})
		})
		var unsupported *json.UnsupportedTypeError
		if !errors.As(err, &unsupported) {
			t.Errorf("err = %v, want the json marshal error", err)
		}
		if n := len(staffRows(t, f, staff)); n != 0 {
			t.Errorf("staff rows = %d, want 0", n)
		}
		if n := auditCount(t, f.app, tenant, event); n != 0 {
			t.Errorf("audit_log rows = %d, want 0", n)
		}
	})

	t.Run("zero_subject", func(t *testing.T) {
		staff, event := uuid.New(), uuid.NewString()
		if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
			return audit.RecordStaff(ctx, tx, audit.StaffEvent{Actor: staff, Event: event, TenantID: uuid.MustParse(tenant)})
		}); err != nil {
			t.Fatalf("tenant change without a subject: %v", err)
		}
		rows := staffRows(t, f, staff)
		if len(rows) != 1 {
			t.Fatalf("staff rows = %d, want 1", len(rows))
		}
		if rows[0].Subject != nil || deref(rows[0].Tenant) != tenant {
			t.Errorf("subject, tenant = %s, %s, want <NULL>, %s", deref(rows[0].Subject), deref(rows[0].Tenant), tenant)
		}
		if n := auditCount(t, f.app, tenant, event); n != 1 {
			t.Errorf("audit_log rows = %d, want 1", n)
		}
	})
}

// AC-3. The control commit proves the tenant can take both rows, so the zero counts are the rollback.
func TestStaffAudit_TenantChangeRollsBackWhenTheStaffWriteFails(t *testing.T) {
	f := requireFixture(t)
	requireStaffTable(t, f)
	ctx := context.Background()
	tenant, staff, key := uuid.NewString(), uuid.New(), uuid.NewString()

	err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		if e := insertKey(ctx, tx, tenant, key); e != nil {
			return e
		}
		return audit.RecordStaff(ctx, tx, audit.StaffEvent{Actor: staff, Event: "", TenantID: uuid.MustParse(tenant)})
	})
	assertCheckViolation(t, err, "staff_audit_event_length")
	if n := len(staffRows(t, f, staff)); n != 0 {
		t.Errorf("staff rows = %d, want 0", n)
	}
	if n := supportRowCount(t, f, tenant); n != 0 {
		t.Errorf("support audit_log rows = %d, want 0", n)
	}
	if n := keyCount(t, f.app, tenant, key); n != 0 {
		t.Errorf("companion key rows = %d, want 0", n)
	}

	event := uuid.NewString()
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		return audit.RecordStaff(ctx, tx, audit.StaffEvent{Actor: staff, Event: event, TenantID: uuid.MustParse(tenant)})
	}); err != nil {
		t.Fatalf("control commit: %v", err)
	}
	if n, s := supportRowCount(t, f, tenant), len(staffRows(t, f, staff)); n != 1 || s != 1 {
		t.Errorf("control rows: support %d staff %d, want 1 and 1", n, s)
	}
}

// AC-4.
func TestStaffAudit_TenantChangeRollsBackWhenTheCustomerWriteFails(t *testing.T) {
	f := requireFixture(t)
	requireStaffTable(t, f)
	ctx := context.Background()
	tenant := uuid.NewString()
	boom := errors.New("boom")

	run := func(staff uuid.UUID, event, key string) error {
		return db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
			if e := insertKey(ctx, tx, tenant, key); e != nil {
				return e
			}
			return audit.RecordStaff(ctx, tx, audit.StaffEvent{Actor: staff, Event: event, TenantID: uuid.MustParse(tenant)})
		})
	}

	t.Run("forced_failure", func(t *testing.T) {
		audit.SetRecordTenantForTest(t, func(context.Context, pgx.Tx, string, string, any) error { return boom })
		staff, event, key := uuid.New(), uuid.NewString(), uuid.NewString()
		if err := run(staff, event, key); !errors.Is(err, boom) {
			t.Errorf("err = %v, want boom", err)
		}
		if n := len(staffRows(t, f, staff)); n != 0 {
			t.Errorf("staff rows = %d, want 0", n)
		}
		if n := auditCount(t, f.app, tenant, event); n != 0 {
			t.Errorf("audit_log rows = %d, want 0", n)
		}
		if n := keyCount(t, f.app, tenant, key); n != 0 {
			t.Errorf("companion key rows = %d, want 0", n)
		}
	})

	t.Run("control_without_the_swap", func(t *testing.T) {
		staff, event, key := uuid.New(), uuid.NewString(), uuid.NewString()
		if err := run(staff, event, key); err != nil {
			t.Fatalf("control run: %v", err)
		}
		if n := len(staffRows(t, f, staff)); n != 1 {
			t.Errorf("staff rows = %d, want 1", n)
		}
		if n := auditCount(t, f.app, tenant, event); n != 1 {
			t.Errorf("audit_log rows = %d, want 1", n)
		}
		if n := keyCount(t, f.app, tenant, key); n != 1 {
			t.Errorf("companion key rows = %d, want 1", n)
		}
	})
}

// AC-5. Tenant B inside a tenant-A tx is the cross-tenant refusal: neither tenant gets a row.
func TestStaffAudit_TenantMustMatchTheTransaction(t *testing.T) {
	f := requireFixture(t)
	requireStaffTable(t, f)
	ctx := context.Background()
	a, b := uuid.NewString(), uuid.NewString()

	for _, tc := range []struct {
		name     string
		inTenant string // "" runs in a tx with no tenant
		tenantID string // "" is the zero TenantID
	}{
		{"tenant_set_in_a_tx_with_no_tenant", "", a},
		{"tenant_b_in_a_tx_for_tenant_a", a, b},
		{"zero_tenant_in_a_tx_for_tenant_a", a, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			staff, event, key := uuid.New(), uuid.NewString(), uuid.NewString()
			ev := audit.StaffEvent{Actor: staff, Event: event}
			if tc.tenantID != "" {
				ev.TenantID = uuid.MustParse(tc.tenantID)
			}
			fn := func(tx pgx.Tx) error {
				if tc.inTenant != "" {
					if e := insertKey(ctx, tx, tc.inTenant, key); e != nil {
						return e
					}
				}
				return audit.RecordStaff(ctx, tx, ev)
			}
			var err error
			if tc.inTenant == "" {
				err = withAppTx(t, f, fn)
			} else {
				err = db.WithinTenantTx(ctx, f.app, tc.inTenant, fn)
			}
			if !errors.Is(err, audit.ErrStaffTenantMismatch) {
				t.Errorf("err = %v, want ErrStaffTenantMismatch", err)
			}
			if n := len(staffRows(t, f, staff)); n != 0 {
				t.Errorf("staff rows = %d, want 0", n)
			}
			for _, tenant := range []string{a, b} {
				if n := auditCount(t, f.app, tenant, event); n != 0 {
					t.Errorf("audit_log rows for tenant %s = %d, want 0", tenant, n)
				}
			}
			if tc.inTenant != "" {
				if n := keyCount(t, f.app, tc.inTenant, key); n != 0 {
					t.Errorf("companion key rows = %d, want 0", n)
				}
			}
		})
	}
}

// AC-6. TestStaffAudit_TenantChangeRollsBackWhenTheCustomerWriteFails proves the seam is the one RecordStaff calls.
func TestStaffAudit_GlobalChangeWritesNoTenantRow(t *testing.T) {
	f := requireFixture(t)
	requireStaffTable(t, f)
	var calls int
	audit.SetRecordTenantForTest(t, func(context.Context, pgx.Tx, string, string, any) error {
		calls++
		return nil
	})
	staff, event := uuid.New(), uuid.NewString()

	recordStaffCommitted(t, f, audit.StaffEvent{Actor: staff, Event: event})

	rows := staffRows(t, f, staff)
	if len(rows) != 1 {
		t.Fatalf("staff rows = %d, want 1", len(rows))
	}
	if rows[0].Tenant != nil {
		t.Errorf("tenant_id = %s, want NULL", deref(rows[0].Tenant))
	}
	if calls != 0 {
		t.Errorf("recordTenant calls = %d, want 0", calls)
	}

	// A pooled connection reads app.current_tenant as '' after a tenant tx, not NULL.
	t.Run("empty_tenant_setting_is_no_tenant", func(t *testing.T) {
		staff, event := uuid.New(), uuid.NewString()
		if err := withAppTx(t, f, func(tx pgx.Tx) error {
			if _, e := tx.Exec(context.Background(), `SELECT set_config('app.current_tenant', '', true)`); e != nil {
				return e
			}
			return audit.RecordStaff(context.Background(), tx, audit.StaffEvent{Actor: staff, Event: event})
		}); err != nil {
			t.Fatalf("global change with an empty tenant setting: %v", err)
		}
		if rows := staffRows(t, f, staff); len(rows) != 1 || rows[0].Tenant != nil {
			t.Errorf("staff rows = %+v, want one with tenant_id NULL", rows)
		}
	})
}

// AC-7.
func TestStaffAudit_SupportRowReadsAsASComplySupport(t *testing.T) {
	f := requireFixture(t)
	requireStaffTable(t, f)
	ctx := context.Background()
	tenant, event := uuid.NewString(), uuid.NewString()

	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		return audit.RecordStaff(ctx, tx, audit.StaffEvent{Actor: uuid.New(), Event: event, TenantID: uuid.MustParse(tenant), Payload: map[string]any{"reason": "r"}})
	}); err != nil {
		t.Fatalf("tenant change: %v", err)
	}

	find := func(filter audit.Filter) (audit.Event, bool) {
		var resp audit.Response
		if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
			var e error
			resp, e = audit.Query(ctx, tx, filter)
			return e
		}); err != nil {
			t.Fatalf("Query: %v", err)
		}
		for _, e := range resp.Events {
			if e.Event == event {
				return e, true
			}
		}
		return audit.Event{}, false
	}

	e, ok := find(audit.Filter{Limit: 50, Company: audit.AllCompanies()})
	if !ok {
		t.Fatal("the support row is not on the tenant's page")
	}
	// The literal, not audit.SupportActor: a renamed constant must fail here.
	const want = "ASComply support"
	if e.Actor != want || e.ActorName != want || e.ActorKind != "raw" || e.CompanyScope != audit.ScopeUnattributed {
		t.Errorf("row = actor %q name %q kind %q scope %q, want %q %q raw unattributed",
			e.Actor, e.ActorName, e.ActorKind, e.CompanyScope, want, want)
	}

	for _, tc := range []struct {
		name   string
		filter audit.Filter
		want   bool
	}{
		{"people", audit.Filter{Limit: 50, Company: audit.AllCompanies(), ActorKind: "people"}, true},
		{"system", audit.Filter{Limit: 50, Company: audit.AllCompanies(), ActorKind: "system"}, false},
		{"workspace_only", audit.Filter{Limit: 50, Company: audit.WorkspaceOnly()}, true},
		{"named_company", audit.Filter{Limit: 50, Company: audit.NamedCompany(uuid.NewString())}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := find(tc.filter); got != tc.want {
				t.Errorf("support row present = %v, want %v", got, tc.want)
			}
		})
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
