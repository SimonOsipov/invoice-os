// `readiness_check_requests` holds one row per confirm link until the owner presses it. It is
// global (no tenant, no RLS), so the guarantees are the column set, the grants and the CHECKs.
// Rows are seeded and removed through the superuser pool.
package db_test

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const rcrMigrationGlob = "*_readiness_check_requests.sql"

var rcrColumns = []string{"email", "token_hash", "file_type", "row_count", "failed_rule_ids", "mail", "expires_at", "created_at"}

// rcrRequireTable fails by name when the migration is not applied to the live DB.
func rcrRequireTable(t *testing.T) {
	t.Helper()
	var present bool
	if err := requireHarness(t).super.QueryRow(context.Background(),
		`SELECT to_regclass('public.readiness_check_requests') IS NOT NULL`).Scan(&present); err != nil {
		t.Fatalf("read table presence: %v", err)
	}
	if !present {
		t.Fatal("public.readiness_check_requests does not exist: the readiness_check_requests migration is not applied")
	}
}

func rcrEmail(t *testing.T) string {
	t.Helper()
	email := "rcr-" + uuid.NewString() + "@readiness.test"
	t.Cleanup(func() {
		_, _ = requireHarness(t).super.Exec(context.Background(), `DELETE FROM readiness_check_requests WHERE email = $1`, email)
	})
	return email
}

// rcrHash returns an n-byte token hash, unique per call.
func rcrHash(n int) []byte {
	b := make([]byte, 0, n)
	for len(b) < n {
		b = append(b, uuid.NewString()...)
	}
	return b[:n]
}

const rcrInsertSQL = `INSERT INTO readiness_check_requests (email, token_hash, file_type, row_count, mail, expires_at)
	VALUES ($1, $2, $3, $4, $5::jsonb, now() + interval '24 hours')`

func rcrInsert(email string, hash []byte, fileType string, rows int, mail string) error {
	_, err := h.app.Exec(context.Background(), rcrInsertSQL, email, hash, fileType, rows, mail)
	return err
}

func rcrRowCount(t *testing.T, email string) int {
	t.Helper()
	return mustCount(t, requireHarness(t).super, `SELECT count(*) FROM readiness_check_requests WHERE email = $1`, email)
}

func rcrColumnNames(t *testing.T) []string {
	t.Helper()
	rows, err := requireHarness(t).super.Query(context.Background(),
		`SELECT column_name FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'readiness_check_requests' ORDER BY ordinal_position`)
	if err != nil {
		t.Fatalf("query readiness_check_requests columns: %v", err)
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

// The exact ordered set also rules out any file, IP or consent column.
func TestRLS_ReadinessCheckRequestsHoldsOnlyTheseColumns(t *testing.T) {
	rcrRequireTable(t)
	got := rcrColumnNames(t)
	if len(got) == 0 {
		t.Fatal("readiness_check_requests has no columns")
	}
	if !reflect.DeepEqual(got, rcrColumns) {
		t.Errorf("readiness_check_requests columns = %v, want exactly %v", got, rcrColumns)
	}
}

func TestRLS_ReadinessCheckRequestsAppInsertsReadsUpdatesAndDeletes(t *testing.T) {
	rcrRequireTable(t)
	ctx := context.Background()
	email := rcrEmail(t)
	hash, newHash := rcrHash(32), rcrHash(32)

	if err := rcrInsert(email, hash, "pdf", 3, `{"k":"v"}`); err != nil {
		t.Fatalf("INSERT as invoice_app: %v", err)
	}
	var fileType string
	var rows int
	var failed []string
	var mailKind string
	if err := h.app.QueryRow(ctx,
		`SELECT file_type, row_count, failed_rule_ids, jsonb_typeof(mail) FROM readiness_check_requests WHERE email = $1`, email,
	).Scan(&fileType, &rows, &failed, &mailKind); err != nil {
		t.Fatalf("SELECT as invoice_app: %v", err)
	}
	if fileType != "pdf" || rows != 3 || failed == nil || len(failed) != 0 || mailKind != "object" {
		t.Errorf("row read back = (%q, %d, %v, %q), want (pdf, 3, empty non-nil list, object)", fileType, rows, failed, mailKind)
	}

	tag, err := h.app.Exec(ctx, `UPDATE readiness_check_requests SET token_hash = $1 WHERE token_hash = $2`, newHash, hash)
	if err != nil {
		t.Fatalf("UPDATE as invoice_app: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Errorf("UPDATE affected %d rows, want 1", tag.RowsAffected())
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM readiness_check_requests WHERE token_hash = $1`, newHash); n != 1 {
		t.Errorf("rows holding the new token hash after the UPDATE = %d, want 1", n)
	}

	tag, err = h.app.Exec(ctx, `DELETE FROM readiness_check_requests WHERE email = $1`, email)
	if err != nil {
		t.Fatalf("DELETE as invoice_app: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Errorf("DELETE affected %d rows, want 1", tag.RowsAffected())
	}
	if n := rcrRowCount(t, email); n != 0 {
		t.Errorf("rows after the app DELETE = %d, want 0", n)
	}
}

func TestRLS_ReadinessCheckRequestsGatewayRoleHasNoAccess(t *testing.T) {
	rcrRequireTable(t)
	err := aeAsGateway(t, false, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT 1 FROM readiness_check_requests`)
		return err
	})
	assertPgRefusal(t, "SELECT as invoice_gateway", err, pgInsufficientPriv, "permission denied for table readiness_check_requests")

	email := rcrEmail(t)
	err = aeAsGateway(t, false, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, rcrInsertSQL, email, rcrHash(32), "csv", 1, `{}`)
		return err
	})
	assertPgRefusal(t, "INSERT as invoice_gateway", err, pgInsufficientPriv, "permission denied for table readiness_check_requests")
	if n := rcrRowCount(t, email); n != 0 {
		t.Errorf("rows after the refused gateway INSERT = %d, want 0", n)
	}

	if got := aeVerbs(t, "invoice_gateway", "readiness_check_requests"); len(got) != 0 {
		t.Errorf("invoice_gateway privileges on readiness_check_requests = %v, want none", got)
	}
	if got, want := aeVerbs(t, "invoice_app", "readiness_check_requests"), []string{"DELETE", "INSERT", "SELECT", "UPDATE"}; !reflect.DeepEqual(got, want) {
		t.Errorf("invoice_app privileges on readiness_check_requests = %v, want exactly %v", got, want)
	}

	// The cross-tenant reader and the sign-in service have no business with held results either.
	err = func() error {
		_, err := h.reader.Exec(context.Background(), `SELECT 1 FROM readiness_check_requests`)
		return err
	}()
	assertPgRefusal(t, "SELECT as invoice_tenant_reader", err, pgInsufficientPriv, "permission denied for table readiness_check_requests")
	for _, role := range []string{"invoice_tenant_reader", "supabase_auth_admin"} {
		if got := aeVerbs(t, role, "readiness_check_requests"); len(got) != 0 {
			t.Errorf("%s privileges on readiness_check_requests = %v, want none", role, got)
		}
	}
	var publicGrants int
	if err := h.super.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.role_table_grants WHERE table_name = 'readiness_check_requests' AND grantee = 'PUBLIC'`).Scan(&publicGrants); err != nil {
		t.Fatalf("read PUBLIC grants: %v", err)
	}
	if publicGrants != 0 {
		t.Errorf("PUBLIC grants on readiness_check_requests = %d, want 0", publicGrants)
	}
}

func TestRLS_ReadinessCheckRequestsRefusesAnUnfoldedEmail(t *testing.T) {
	rcrRequireTable(t)
	good := rcrEmail(t)
	if err := rcrInsert(good, rcrHash(32), "csv", 1, `{}`); err != nil {
		t.Fatalf("a folded address is refused: %v", err)
	}
	for _, bad := range []string{" A@x.io", "A@x.io", "a@x.io "} {
		wantCheckRefusal(t, rcrInsert(bad, rcrHash(32), "csv", 1, `{}`), "readiness_check_requests_email_check")
		if n := rcrRowCount(t, bad); n != 0 {
			t.Errorf("rows for %q after the refusal = %d, want 0", bad, n)
		}
	}
}

func TestRLS_ReadinessCheckRequestsRefusesAShortTokenHash(t *testing.T) {
	rcrRequireTable(t)
	if err := rcrInsert(rcrEmail(t), rcrHash(32), "csv", 1, `{}`); err != nil {
		t.Fatalf("a 32-byte token hash is refused: %v", err)
	}
	for _, n := range []int{31, 33} {
		email := rcrEmail(t)
		wantCheckRefusal(t, rcrInsert(email, rcrHash(n), "csv", 1, `{}`), "readiness_check_requests_token_hash_check")
		if got := rcrRowCount(t, email); got != 0 {
			t.Errorf("rows after the refused %d-byte hash = %d, want 0", n, got)
		}
	}
}

// One row per link, not per address: a second check for the same address adds a link.
func TestRLS_ReadinessCheckRequestsAllowsTwoRowsPerAddress(t *testing.T) {
	rcrRequireTable(t)
	email := rcrEmail(t)
	if err := rcrInsert(email, rcrHash(32), "csv", 1, `{}`); err != nil {
		t.Fatalf("first row: %v", err)
	}
	if err := rcrInsert(email, rcrHash(32), "xlsx", 2, `{}`); err != nil {
		t.Fatalf("second row for the same address: %v", err)
	}
	if n := rcrRowCount(t, email); n != 2 {
		t.Errorf("rows for one address = %d, want 2", n)
	}
}

func TestRLS_ReadinessCheckRequestsRefusesAReusedTokenHash(t *testing.T) {
	rcrRequireTable(t)
	hash := rcrHash(32)
	first, second := rcrEmail(t), rcrEmail(t)
	if err := rcrInsert(first, hash, "csv", 1, `{}`); err != nil {
		t.Fatalf("first row: %v", err)
	}
	assertPgRefusal(t, "INSERT of a reused token hash", rcrInsert(second, hash, "csv", 1, `{}`),
		pgUniqueViolation, `"readiness_check_requests_pkey"`)
	if n := rcrRowCount(t, second); n != 0 {
		t.Errorf("rows for the second address after the refusal = %d, want 0", n)
	}
	if n := rcrRowCount(t, first); n != 1 {
		t.Errorf("rows for the first address = %d, want 1", n)
	}
}

func TestRLS_ReadinessCheckRequestsRefusesUnknownFileTypeNegativeRowsAndNonObjectMail(t *testing.T) {
	rcrRequireTable(t)
	for _, ok := range []string{"csv", "xlsx", "pdf", "docx"} {
		if err := rcrInsert(rcrEmail(t), rcrHash(32), ok, 0, `{}`); err != nil {
			t.Fatalf("file_type %q with row_count 0 is refused: %v", ok, err)
		}
	}
	wantCheckRefusal(t, rcrInsert(rcrEmail(t), rcrHash(32), "txt", 1, `{}`), "readiness_check_requests_file_type_check")
	wantCheckRefusal(t, rcrInsert(rcrEmail(t), rcrHash(32), "csv", -1, `{}`), "readiness_check_requests_row_count_check")
	for _, bad := range []string{`[]`, `"text"`, `null`} {
		email := rcrEmail(t)
		wantCheckRefusal(t, rcrInsert(email, rcrHash(32), "csv", 1, bad), "readiness_check_requests_mail_check")
		if n := rcrRowCount(t, email); n != 0 {
			t.Errorf("rows after the refused mail %s = %d, want 0", bad, n)
		}
	}
}

func TestRLS_ReadinessCheckRequestsConstraintSet(t *testing.T) {
	rcrRequireTable(t)
	rows, err := h.super.Query(context.Background(),
		`SELECT conname FROM pg_constraint WHERE conrelid = 'public.readiness_check_requests'::regclass ORDER BY conname`)
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
		"readiness_check_requests_pkey",
		"readiness_check_requests_email_check", "readiness_check_requests_token_hash_check",
		"readiness_check_requests_file_type_check", "readiness_check_requests_row_count_check",
		"readiness_check_requests_mail_check",
		"readiness_check_requests_email_not_null", "readiness_check_requests_token_hash_not_null",
		"readiness_check_requests_file_type_not_null", "readiness_check_requests_row_count_not_null",
		"readiness_check_requests_failed_rule_ids_not_null", "readiness_check_requests_mail_not_null",
		"readiness_check_requests_expires_at_not_null", "readiness_check_requests_created_at_not_null",
	}
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("readiness_check_requests constraints = %v, want exactly %v", got, want)
	}
}

func TestRLS_ReadinessCheckRequestsDownThenUp(t *testing.T) {
	ctx := context.Background()
	rcrRequireTable(t)
	version := migrationVersion(t, rcrMigrationGlob)
	provider := staffMigrationProvider(t)
	restoreMigrationOnCleanup(t, provider, version)

	present := func() bool {
		t.Helper()
		var ok bool
		if err := h.super.QueryRow(ctx, `SELECT to_regclass('public.readiness_check_requests') IS NOT NULL`).Scan(&ok); err != nil {
			t.Fatalf("read table presence: %v", err)
		}
		return ok
	}
	wantApp := []string{"DELETE", "INSERT", "SELECT", "UPDATE"}
	if !reflect.DeepEqual(rcrColumnNames(t), rcrColumns) {
		t.Fatalf("columns before the Down = %v, want %v", rcrColumnNames(t), rcrColumns)
	}
	if got := aeVerbs(t, "invoice_app", "readiness_check_requests"); !reflect.DeepEqual(got, wantApp) {
		t.Fatalf("invoice_app privileges before the Down = %v, want %v", got, wantApp)
	}

	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("Down of %d: %v", version, err)
	}
	if present() {
		t.Error("readiness_check_requests survives the Down")
	}

	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("Up of %d: %v", version, err)
	}
	if !present() {
		t.Fatal("readiness_check_requests is absent after the Up")
	}
	if got := rcrColumnNames(t); !reflect.DeepEqual(got, rcrColumns) {
		t.Errorf("columns after the Up = %v, want %v", got, rcrColumns)
	}
	if got := aeVerbs(t, "invoice_app", "readiness_check_requests"); !reflect.DeepEqual(got, wantApp) {
		t.Errorf("invoice_app privileges after the Up = %v, want %v", got, wantApp)
	}
	if got := aeVerbs(t, "invoice_gateway", "readiness_check_requests"); len(got) != 0 {
		t.Errorf("invoice_gateway privileges after the Up = %v, want none", got)
	}
}
