// `readiness_checks` holds one summary row per public check. It is global (no tenant, no RLS),
// so the guarantees are the column set, the grants and the CHECKs. Rows are seeded and removed
// through the superuser pool.
package db_test

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const rcMigrationGlob = "*_readiness_checks.sql"

var rcColumns = []string{"created_at", "email", "failed_rule_ids", "file_type", "id", "row_count"}

// rcRequireTable fails by name when the migration is not applied to the live DB.
func rcRequireTable(t *testing.T) {
	t.Helper()
	var present bool
	if err := requireHarness(t).super.QueryRow(context.Background(),
		`SELECT to_regclass('public.readiness_checks') IS NOT NULL`).Scan(&present); err != nil {
		t.Fatalf("read table presence: %v", err)
	}
	if !present {
		t.Fatal("public.readiness_checks does not exist: the readiness_checks migration is not applied")
	}
}

func rcEmail(t *testing.T) string {
	t.Helper()
	email := "rc-" + uuid.NewString() + "@readiness.test"
	t.Cleanup(func() {
		_, _ = requireHarness(t).super.Exec(context.Background(), `DELETE FROM readiness_checks WHERE email = $1`, email)
	})
	return email
}

func rcSeed(t *testing.T, email string) {
	t.Helper()
	if _, err := requireHarness(t).super.Exec(context.Background(),
		`INSERT INTO readiness_checks (email, file_type, row_count) VALUES ($1, 'csv', 5)`, email); err != nil {
		t.Fatalf("seed readiness_checks row: %v", err)
	}
}

func rcRowCount(t *testing.T, email string) int {
	t.Helper()
	return mustCount(t, requireHarness(t).super, `SELECT count(*) FROM readiness_checks WHERE email = $1`, email)
}

func rcColumnNames(t *testing.T) []string {
	t.Helper()
	rows, err := requireHarness(t).super.Query(context.Background(),
		`SELECT column_name FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'readiness_checks' ORDER BY column_name`)
	if err != nil {
		t.Fatalf("query readiness_checks columns: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	return out
}

func rcAppInsert(email, fileType string, rows int) error {
	_, err := h.app.Exec(context.Background(),
		`INSERT INTO readiness_checks (email, file_type, row_count) VALUES ($1, $2, $3)`, email, fileType, rows)
	return err
}

// The exact set also rules out any file, content, payload or address column.
func TestRLS_ReadinessChecksHoldsOnlyTheSummaryColumns(t *testing.T) {
	rcRequireTable(t)
	got := rcColumnNames(t)
	if len(got) == 0 {
		t.Fatal("readiness_checks has no columns")
	}
	if !reflect.DeepEqual(got, rcColumns) {
		t.Errorf("readiness_checks columns = %v, want exactly %v", got, rcColumns)
	}
}

func TestRLS_ReadinessChecksAppInsertsReadsAndDeletes(t *testing.T) {
	rcRequireTable(t)
	ctx := context.Background()
	email := rcEmail(t)

	if err := rcAppInsert(email, "pdf", 3); err != nil {
		t.Fatalf("INSERT as invoice_app: %v", err)
	}
	var fileType string
	var rows int
	var failed []string
	if err := h.app.QueryRow(ctx,
		`SELECT file_type, row_count, failed_rule_ids FROM readiness_checks WHERE email = $1`, email,
	).Scan(&fileType, &rows, &failed); err != nil {
		t.Fatalf("SELECT as invoice_app: %v", err)
	}
	if fileType != "pdf" || rows != 3 || failed == nil || len(failed) != 0 {
		t.Errorf("row read back = (%q, %d, %v), want (pdf, 3, empty non-nil list)", fileType, rows, failed)
	}
	tag, err := h.app.Exec(ctx, `DELETE FROM readiness_checks WHERE email = $1`, email)
	if err != nil {
		t.Fatalf("DELETE as invoice_app: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Errorf("DELETE affected %d rows, want 1", tag.RowsAffected())
	}
	if n := rcRowCount(t, email); n != 0 {
		t.Errorf("rows after the app DELETE = %d, want 0", n)
	}
}

func TestRLS_ReadinessChecksAppCannotUpdate(t *testing.T) {
	rcRequireTable(t)
	email := rcEmail(t)
	rcSeed(t, email)

	_, err := h.app.Exec(context.Background(), `UPDATE readiness_checks SET row_count = 1 WHERE email = $1`, email)
	assertPgRefusal(t, "UPDATE as invoice_app", err, pgInsufficientPriv, "permission denied for table readiness_checks")
	if n := mustCount(t, h.super, `SELECT count(*) FROM readiness_checks WHERE email = $1 AND row_count = 5`, email); n != 1 {
		t.Errorf("rows still at row_count 5 after the refused UPDATE = %d, want 1", n)
	}
}

func TestRLS_ReadinessChecksGatewayRoleHasNoAccess(t *testing.T) {
	rcRequireTable(t)
	err := aeAsGateway(t, false, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT 1 FROM readiness_checks`)
		return err
	})
	assertPgRefusal(t, "SELECT as invoice_gateway", err, pgInsufficientPriv, "permission denied for table readiness_checks")
	if got := aeVerbs(t, "invoice_gateway", "readiness_checks"); len(got) != 0 {
		t.Errorf("invoice_gateway privileges on readiness_checks = %v, want none", got)
	}
	if got, want := aeVerbs(t, "invoice_app", "readiness_checks"), []string{"DELETE", "INSERT", "SELECT"}; !reflect.DeepEqual(got, want) {
		t.Errorf("invoice_app privileges on readiness_checks = %v, want exactly %v", got, want)
	}
}

func TestRLS_ReadinessChecksRefusesAnUnfoldedEmail(t *testing.T) {
	rcRequireTable(t)
	good := rcEmail(t)
	if err := rcAppInsert(good, "csv", 1); err != nil {
		t.Fatalf("a folded address is refused: %v", err)
	}
	for _, bad := range []string{" a@x.io", "A@x.io", "a@x.io "} {
		wantCheckRefusal(t, rcAppInsert(bad, "csv", 1), "readiness_checks_email_check")
		if n := rcRowCount(t, bad); n != 0 {
			t.Errorf("rows for %q after the refusal = %d, want 0", bad, n)
		}
	}
}

func TestRLS_ReadinessChecksRefusesAnUnknownFileType(t *testing.T) {
	rcRequireTable(t)
	for _, ok := range []string{"csv", "xlsx", "pdf", "docx"} {
		if err := rcAppInsert(rcEmail(t), ok, 1); err != nil {
			t.Fatalf("file_type %q is refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"png", "CSV", ""} {
		wantCheckRefusal(t, rcAppInsert(rcEmail(t), bad, 1), "readiness_checks_file_type_check")
	}
}

func TestRLS_ReadinessChecksRefusesANegativeRowCount(t *testing.T) {
	rcRequireTable(t)
	if err := rcAppInsert(rcEmail(t), "csv", 0); err != nil {
		t.Fatalf("row_count 0 is refused: %v", err)
	}
	wantCheckRefusal(t, rcAppInsert(rcEmail(t), "csv", -1), "readiness_checks_row_count_check")
}

func TestRLS_ReadinessChecksConstraintSet(t *testing.T) {
	rcRequireTable(t)
	rows, err := h.super.Query(context.Background(),
		`SELECT conname FROM pg_constraint WHERE conrelid = 'public.readiness_checks'::regclass ORDER BY conname`)
	if err != nil {
		t.Fatalf("query constraints: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	want := []string{
		"readiness_checks_pkey",
		"readiness_checks_email_check", "readiness_checks_file_type_check", "readiness_checks_row_count_check",
		"readiness_checks_created_at_not_null", "readiness_checks_email_not_null", "readiness_checks_failed_rule_ids_not_null",
		"readiness_checks_file_type_not_null", "readiness_checks_id_not_null", "readiness_checks_row_count_not_null",
	}
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("readiness_checks constraints = %v, want exactly %v", got, want)
	}
}

func TestRLS_ReadinessChecksDownThenUp(t *testing.T) {
	ctx := context.Background()
	version := migrationVersion(t, rcMigrationGlob)
	rcRequireTable(t)
	provider := staffMigrationProvider(t)
	restoreMigrationOnCleanup(t, provider, version)

	present := func() bool {
		t.Helper()
		var ok bool
		if err := h.super.QueryRow(ctx, `SELECT to_regclass('public.readiness_checks') IS NOT NULL`).Scan(&ok); err != nil {
			t.Fatalf("read table presence: %v", err)
		}
		return ok
	}
	if !reflect.DeepEqual(rcColumnNames(t), rcColumns) {
		t.Fatalf("columns before the Down = %v, want %v", rcColumnNames(t), rcColumns)
	}

	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("Down of %d: %v", version, err)
	}
	if present() {
		t.Error("readiness_checks survives the Down")
	}

	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("Up of %d: %v", version, err)
	}
	if !present() {
		t.Fatal("readiness_checks is absent after the Up")
	}
	if got := rcColumnNames(t); !reflect.DeepEqual(got, rcColumns) {
		t.Errorf("columns after the Up = %v, want %v", got, rcColumns)
	}
	if got, want := aeVerbs(t, "invoice_app", "readiness_checks"), []string{"DELETE", "INSERT", "SELECT"}; !reflect.DeepEqual(got, want) {
		t.Errorf("invoice_app privileges after the Up = %v, want %v", got, want)
	}
}
