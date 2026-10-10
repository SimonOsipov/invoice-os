package validation

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/migrations"
)

// Fixtures live in year 3001+ so they never overlap a real start date. Each test builds them
// inside a tx it always rolls back.

const (
	effectiveDatesMigrationGlob = "*_rule_set_effective_dates.sql"
	ruleImmutabilityMigration   = "20260717120000_rule_immutability_lock.sql"
)

// requireEffectiveFrom fails with a clear message while Migration A is not applied.
func requireEffectiveFrom(t *testing.T, ctx context.Context, q queryRower) {
	t.Helper()
	var ok bool
	if err := q.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_attribute
		  WHERE attrelid = 'rule_set_versions'::regclass AND attname = 'effective_from' AND NOT attisdropped)`,
	).Scan(&ok); err != nil {
		t.Fatalf("probe rule_set_versions.effective_from: %v", err)
	}
	if !ok {
		t.Fatalf("rule_set_versions.effective_from does not exist -- Migration A (rule_set_effective_dates) is not applied")
	}
}

// edFixture inserts a rule_set_versions row inside tx; from == "" leaves it undated.
func edFixture(t *testing.T, ctx context.Context, tx pgx.Tx, version int, sealed bool, from string) string {
	t.Helper()
	requireEffectiveFrom(t, ctx, tx)
	var fromArg *string
	if from != "" {
		fromArg = &from
	}
	var id string
	if err := tx.QueryRow(ctx,
		`INSERT INTO rule_set_versions (version, sealed, effective_from, notes)
		 VALUES ($1, $2, $3::date, $4) RETURNING id`,
		version, sealed, fromArg, fixtureNotes,
	).Scan(&id); err != nil {
		t.Fatalf("insert fixture version=%d sealed=%t from=%q: %v", version, sealed, from, err)
	}
	return id
}

func edBegin(t *testing.T, ctx context.Context, p interface {
	Begin(context.Context) (pgx.Tx, error)
}) pgx.Tx {
	t.Helper()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

func edFrom(t *testing.T, ctx context.Context, q queryRower, id string) *string {
	t.Helper()
	var got *string
	if err := q.QueryRow(ctx, `SELECT effective_from::text FROM rule_set_versions WHERE id = $1`, id).Scan(&got); err != nil {
		t.Fatalf("read effective_from of %s: %v", id, err)
	}
	return got
}

func edVersionFor(t *testing.T, ctx context.Context, q queryRower, date string) *string {
	t.Helper()
	var got *string
	if err := q.QueryRow(ctx, `SELECT rule_set_version_for($1::date)::text`, date).Scan(&got); err != nil {
		t.Fatalf("rule_set_version_for(%s): %v", date, err)
	}
	return got
}

// latestRealDated returns the id of the newest published, dated rule-set version (not a fixture),
// so tests that look past every real start date survive the next publish.
func latestRealDated(t *testing.T, ctx context.Context, q queryRower) string {
	t.Helper()
	var id string
	if err := q.QueryRow(ctx,
		`SELECT id FROM rule_set_versions
		  WHERE notes LIKE 'MBS global rule-set v%' AND effective_from IS NOT NULL
		  ORDER BY effective_from DESC, version DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("read the latest real dated version: %v", err)
	}
	return id
}

func edWant(t *testing.T, got *string, want, what string) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = NULL, want %s", what, want)
		return
	}
	if *got != want {
		t.Errorf("%s = %s, want %s", what, *got, want)
	}
}

func assertConstraintName(t *testing.T, err error, want string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("want a Postgres error naming constraint %s, got %v", want, err)
	}
	if pgErr.ConstraintName != want {
		t.Errorf("constraint name = %q, want %q (%s)", pgErr.ConstraintName, want, pgErr.Message)
	}
}

// ---------------------------------------------------------------------
// AC 1 -- v4 is dated, older versions are not.
// ---------------------------------------------------------------------

func TestEffectiveDates_V4IsDatedAndOlderVersionsAreNot(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	rows, err := app.Query(ctx,
		`SELECT version, effective_from::text FROM rule_set_versions WHERE version IN (1,2,3,4) ORDER BY version`)
	if err != nil {
		t.Fatalf("read version, effective_from: %v", err)
	}
	defer rows.Close()
	got := map[int]*string{}
	for rows.Next() {
		var v int
		var from *string
		if err := rows.Scan(&v, &from); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[v] = from
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("read %d of the 4 real versions, want 4", len(got))
	}
	edWant(t, got[4], "2026-08-06", "v4.effective_from")
	for v := 1; v <= 3; v++ {
		if got[v] != nil {
			t.Errorf("v%d.effective_from = %s, want NULL", v, *got[v])
		}
	}
}

// ---------------------------------------------------------------------
// AC 3 -- Guard C extended.
// ---------------------------------------------------------------------

func TestEffectiveDates_SealedStartDateIsFixed(t *testing.T) {
	migrator := migratorPool(t)
	ctx := context.Background()
	tx := edBegin(t, ctx, migrator)

	id := edFixture(t, ctx, tx, nextVersion(), true, "3001-01-01")
	for _, set := range []string{`'3001-02-02'`, `NULL`} {
		opErr := attemptWithSavepoint(t, ctx, tx, `UPDATE rule_set_versions SET effective_from = `+set+` WHERE id = $1`, id)
		assertSQLState(t, opErr, "23001")
	}
	edWant(t, edFrom(t, ctx, tx, id), "3001-01-01", "fixture effective_from after the refused changes")
}

func TestEffectiveDates_V4StartDateIsFixed(t *testing.T) {
	migrator := migratorPool(t)
	ctx := context.Background()
	tx := edBegin(t, ctx, migrator)

	requireEffectiveFrom(t, ctx, tx)
	v4 := versionIDByVersion(t, ctx, tx, 4)
	edWant(t, edFrom(t, ctx, tx, v4), "2026-08-06", "v4.effective_from before the attempt")

	for _, set := range []string{`'2026-09-01'`, `NULL`} {
		opErr := attemptWithSavepoint(t, ctx, tx, `UPDATE rule_set_versions SET effective_from = `+set+` WHERE id = $1`, v4)
		assertSQLState(t, opErr, "23001")
	}
	edWant(t, edFrom(t, ctx, tx, v4), "2026-08-06", "v4.effective_from after the refused changes")
}

func TestEffectiveDates_FirstDateOnSealedUndatedVersionAllowed(t *testing.T) {
	migrator := migratorPool(t)
	ctx := context.Background()
	tx := edBegin(t, ctx, migrator)

	id := edFixture(t, ctx, tx, nextVersion(), true, "")
	if got := edFrom(t, ctx, tx, id); got != nil {
		t.Fatalf("fixture effective_from = %s, want NULL before the first date", *got)
	}
	tag, err := tx.Exec(ctx, `UPDATE rule_set_versions SET effective_from = '3001-01-01' WHERE id = $1`, id)
	if err != nil {
		t.Fatalf("first date on a sealed undated version: %v -- want success (ENGI-12 publish path)", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("RowsAffected = %d, want 1", tag.RowsAffected())
	}
	edWant(t, edFrom(t, ctx, tx, id), "3001-01-01", "fixture effective_from")

	// The first date is itself final.
	assertSQLState(t, attemptWithSavepoint(t, ctx, tx,
		`UPDATE rule_set_versions SET effective_from = '3001-02-02' WHERE id = $1`, id), "23001")
	edWant(t, edFrom(t, ctx, tx, id), "3001-01-01", "fixture effective_from after the refused re-date")
}

func TestEffectiveDates_SealedUpdateThatKeepsTheDateIsAllowed(t *testing.T) {
	migrator := migratorPool(t)
	ctx := context.Background()
	tx := edBegin(t, ctx, migrator)

	id := edFixture(t, ctx, tx, nextVersion(), true, "3001-01-01")
	for _, stmt := range []string{
		`UPDATE rule_set_versions SET effective_from = effective_from WHERE id = $1`,
		`UPDATE rule_set_versions SET notes = 'ed-keep-date' WHERE id = $1`,
	} {
		tag, err := tx.Exec(ctx, stmt, id)
		if err != nil {
			t.Fatalf("%s: %v -- want success, the date does not change", stmt, err)
		}
		if tag.RowsAffected() != 1 {
			t.Fatalf("%s: RowsAffected = %d, want 1", stmt, tag.RowsAffected())
		}
	}
	edWant(t, edFrom(t, ctx, tx, id), "3001-01-01", "fixture effective_from")
}

func TestEffectiveDates_GuardStillRefusesUnsealAndDelete(t *testing.T) {
	migrator := migratorPool(t)
	ctx := context.Background()
	tx := edBegin(t, ctx, migrator)

	id := edFixture(t, ctx, tx, nextVersion(), true, "3001-01-01")

	// The second statement passes the dated-is-sealed CHECK, so only Guard C can refuse it.
	for _, stmt := range []string{
		`UPDATE rule_set_versions SET sealed = false WHERE id = $1`,
		`UPDATE rule_set_versions SET sealed = false, effective_from = NULL WHERE id = $1`,
		`DELETE FROM rule_set_versions WHERE id = $1`,
	} {
		assertSQLState(t, attemptWithSavepoint(t, ctx, tx, stmt, id), "23001")
	}
	opErr := attemptWithSavepoint(t, ctx, tx,
		`INSERT INTO rules (rule_set_version_id, key, type, severity, message)
		 VALUES ($1, 'ed-guard-probe', 'required', 'error', 'probe')`, id)
	assertSQLState(t, opErr, "23001")

	var sealed bool
	if err := tx.QueryRow(ctx, `SELECT sealed FROM rule_set_versions WHERE id = $1`, id).Scan(&sealed); err != nil {
		t.Fatalf("fixture must survive the refused statements: %v", err)
	}
	if !sealed {
		t.Error("fixture sealed = false after the refused statements, want true")
	}
	edWant(t, edFrom(t, ctx, tx, id), "3001-01-01", "fixture effective_from after the refused statements")
}

// ---------------------------------------------------------------------
// AC 4 -- rule_set_version_for implements D4.
// ---------------------------------------------------------------------

func TestVersionFor_LatestStartOnOrBeforeTheDate(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tx := edBegin(t, ctx, super)

	a := edFixture(t, ctx, tx, nextVersion(), true, "3001-01-01")
	b := edFixture(t, ctx, tx, nextVersion(), true, "3001-06-01")
	for _, c := range []struct{ date, want, name string }{
		{"3001-05-31", a, "A"}, {"3001-06-01", b, "B"}, {"3001-12-31", b, "B"},
	} {
		edWant(t, edVersionFor(t, ctx, tx, c.date), c.want, "rule_set_version_for("+c.date+") = "+c.name)
	}
}

func TestVersionFor_SameStartDateGoesToTheHigherVersion(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tx := edBegin(t, ctx, super)

	lo, hi := nextVersion(), nextVersion()
	// The higher version is inserted first so row order cannot decide the tie.
	hiID := edFixture(t, ctx, tx, hi, true, "3002-01-01")
	loID := edFixture(t, ctx, tx, lo, true, "3002-01-01")
	got := edVersionFor(t, ctx, tx, "3002-01-01")
	edWant(t, got, hiID, "rule_set_version_for(3002-01-01) = higher version")
	if got != nil && *got == loID {
		t.Errorf("tie went to the lower version %d", lo)
	}

	// Pre-history tie: both start before v4, and a date before both gets the higher version.
	preLo, preHi := nextVersion(), nextVersion()
	preHiID := edFixture(t, ctx, tx, preHi, true, "1000-01-01")
	edFixture(t, ctx, tx, preLo, true, "1000-01-01")
	edWant(t, edVersionFor(t, ctx, tx, "0999-01-01"), preHiID, "rule_set_version_for(0999-01-01) = higher of the earliest tie")
}

func TestVersionFor_DateBeforeEveryStartGetsTheEarliest(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	requireEffectiveFrom(t, ctx, app)
	v4 := versionIDByVersion(t, ctx, app, 4)
	for _, date := range []string{"1999-01-01", "2026-08-05", "2026-08-06"} {
		edWant(t, edVersionFor(t, ctx, app, date), v4, "rule_set_version_for("+date+")")
	}

	// With a later dated version present, "earliest" still means v4, not the latest.
	tx := edBegin(t, ctx, super)
	later := edFixture(t, ctx, tx, nextVersion(), true, "3001-01-01")
	for _, date := range []string{"1999-01-01", "2026-08-05"} {
		got := edVersionFor(t, ctx, tx, date)
		edWant(t, got, v4, "rule_set_version_for("+date+") with a later version present")
		if got != nil && *got == later {
			t.Errorf("rule_set_version_for(%s) chose the later fixture", date)
		}
	}
}

func TestVersionFor_UndatedVersionsAreNeverChosen(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tx := edBegin(t, ctx, super)

	undated := edFixture(t, ctx, tx, nextVersion(), true, "")
	v4 := versionIDByVersion(t, ctx, tx, 4)
	latest := latestRealDated(t, ctx, tx)
	for date, want := range map[string]string{"1999-01-01": v4, "2026-08-06": v4, "3001-01-01": latest} {
		got := edVersionFor(t, ctx, tx, date)
		edWant(t, got, want, "rule_set_version_for("+date+")")
		if got != nil && *got == undated {
			t.Errorf("rule_set_version_for(%s) chose the undated fixture", date)
		}
	}
}

func TestVersionFor_NullDateIsNull(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	var got *string
	if err := app.QueryRow(ctx, `SELECT rule_set_version_for(NULL::date)::text`).Scan(&got); err != nil {
		t.Fatalf("rule_set_version_for(NULL): %v", err)
	}
	if got != nil {
		t.Errorf("rule_set_version_for(NULL) = %s, want NULL (STRICT)", *got)
	}
}

func TestVersionFor_AppRoleCanCall(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	requireEffectiveFrom(t, ctx, app)
	v4 := versionIDByVersion(t, ctx, app, 4)
	// v4's own start date, not today: a later published version must not break this test.
	edWant(t, edVersionFor(t, ctx, app, "2026-08-06"), v4, "invoice_app rule_set_version_for(2026-08-06)")
}

// ---------------------------------------------------------------------
// AC 5 -- re-check markers.
// ---------------------------------------------------------------------

func TestRechecks_AppCanReadAndInsertOnly(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tx := edBegin(t, ctx, super)

	id := edFixture(t, ctx, tx, nextVersion(), true, "3001-01-01")

	// The table is global and holds no tenant data; only the app role and the owner may touch it.
	rows, err := tx.Query(ctx,
		`SELECT CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE a.grantee::regrole::text END, a.privilege_type
		   FROM pg_class c, aclexplode(c.relacl) a
		  WHERE c.oid = 'rule_set_version_rechecks'::regclass AND a.grantee <> c.relowner
		  ORDER BY 1, 2`)
	if err != nil {
		t.Fatalf("read rule_set_version_rechecks ACL: %v", err)
	}
	var grants []string
	for rows.Next() {
		var grantee, priv string
		if err := rows.Scan(&grantee, &priv); err != nil {
			t.Fatalf("scan ACL: %v", err)
		}
		grants = append(grants, grantee+":"+priv)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("ACL rows: %v", err)
	}
	if want := "invoice_app:INSERT,invoice_app:SELECT"; strings.Join(grants, ",") != want {
		t.Errorf("non-owner grants on rule_set_version_rechecks = %v, want %s", grants, want)
	}

	if _, err := tx.Exec(ctx, `SET LOCAL ROLE invoice_app`); err != nil {
		t.Fatalf("SET LOCAL ROLE invoice_app: %v", err)
	}

	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM rule_set_version_rechecks`).Scan(&n); err != nil {
		t.Fatalf("invoice_app SELECT on rule_set_version_rechecks: %v", err)
	}
	if n < 1 {
		t.Errorf("rule_set_version_rechecks has %d rows, want >= 1 (v4's marker)", n)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO rule_set_version_rechecks (rule_set_version_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatalf("invoice_app INSERT of a marker: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("INSERT RowsAffected = %d, want 1", tag.RowsAffected())
	}
	assertAppRefused(t, attemptWithSavepoint(t, ctx, tx,
		`UPDATE rule_set_version_rechecks SET rechecked_at = now() WHERE rule_set_version_id = $1`, id), "UPDATE marker")
	assertAppRefused(t, attemptWithSavepoint(t, ctx, tx,
		`DELETE FROM rule_set_version_rechecks WHERE rule_set_version_id = $1`, id), "DELETE marker")
}

func TestRechecks_V4IsMarked(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	var v4Marked, olderMarked int
	if err := app.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE v.version = 4), count(*) FILTER (WHERE v.version < 4)
		   FROM rule_set_version_rechecks r JOIN rule_set_versions v ON v.id = r.rule_set_version_id`,
	).Scan(&v4Marked, &olderMarked); err != nil {
		t.Fatalf("read markers: %v", err)
	}
	if v4Marked != 1 {
		t.Errorf("markers for v4 = %d, want 1 (the deploy re-checks nothing)", v4Marked)
	}
	if olderMarked != 0 {
		t.Errorf("markers for v1-v3 = %d, want 0 (undated versions are never due)", olderMarked)
	}
}

// ---------------------------------------------------------------------
// AC 6 -- the Down restores the previous schema and Guard C's M4-17 body.
// ---------------------------------------------------------------------

func TestEffectiveDates_DownRestoresThePreviousSchema(t *testing.T) {
	migrator := migratorPool(t)
	ctx := context.Background()

	matches, err := fs.Glob(migrations.FS, effectiveDatesMigrationGlob)
	if err != nil || len(matches) != 1 {
		t.Fatalf("want exactly one migrations/%s, got %v (err %v)", effectiveDatesMigrationGlob, matches, err)
	}
	raw, err := fs.ReadFile(migrations.FS, matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	downStmts := gooseDownStatements(t, string(raw))
	if len(downStmts) == 0 {
		t.Fatalf("%s has no Down statements", matches[0])
	}
	m417 := guardBodyOf(t, ruleImmutabilityMigration)

	tx := edBegin(t, ctx, migrator)
	requireEffectiveFrom(t, ctx, tx)
	if got := guardSrc(t, ctx, tx); got == m417 {
		t.Fatal("Guard C body equals the M4-17 body before the Down: Migration A did not extend it")
	}

	for _, stmt := range downStmts {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("Down statement failed: %v\n%s", err, stmt)
		}
	}

	for _, c := range []struct{ what, sql string }{
		{"column rule_set_versions.effective_from", `SELECT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = 'rule_set_versions'::regclass AND attname = 'effective_from' AND NOT attisdropped)`},
		{"constraint rule_set_versions_dated_is_sealed", `SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'rule_set_versions_dated_is_sealed')`},
		{"function rule_set_version_for", `SELECT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'rule_set_version_for')`},
		{"table rule_set_version_rechecks", `SELECT to_regclass('rule_set_version_rechecks') IS NOT NULL`},
	} {
		var exists bool
		if err := tx.QueryRow(ctx, c.sql).Scan(&exists); err != nil {
			t.Fatalf("probe %s: %v", c.what, err)
		}
		if exists {
			t.Errorf("%s still exists after the Down", c.what)
		}
	}

	if got := guardSrc(t, ctx, tx); got != m417 {
		t.Errorf("Guard C body after the Down differs from the M4-17 Up body\n got: %q\nwant: %q", got, m417)
	}
	var def string
	if err := tx.QueryRow(ctx, `SELECT pg_get_functiondef('rule_set_versions_seal_guard'::regproc)`).Scan(&def); err != nil {
		t.Fatalf("pg_get_functiondef: %v", err)
	}
	if strings.Contains(def, "effective_from") {
		t.Error("Guard C definition still mentions effective_from after the Down")
	}
}

func guardSrc(t *testing.T, ctx context.Context, q queryRower) string {
	t.Helper()
	var src string
	if err := q.QueryRow(ctx, `SELECT prosrc FROM pg_proc WHERE proname = 'rule_set_versions_seal_guard'`).Scan(&src); err != nil {
		t.Fatalf("read Guard C prosrc: %v", err)
	}
	return src
}

// guardBodyOf returns the text between the $$ pair of the seal-guard function in a migration.
func guardBodyOf(t *testing.T, file string) string {
	t.Helper()
	raw, err := fs.ReadFile(migrations.FS, file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	s := string(raw)
	start := strings.Index(s, "CREATE OR REPLACE FUNCTION rule_set_versions_seal_guard()")
	if start < 0 {
		t.Fatalf("%s: seal-guard function not found", file)
	}
	s = s[start:]
	open := strings.Index(s, "$$")
	closeIdx := strings.Index(s[open+2:], "$$")
	if open < 0 || closeIdx < 0 {
		t.Fatalf("%s: seal-guard $$ body not found", file)
	}
	return s[open+2 : open+2+closeIdx]
}

// gooseDownStatements splits the Down section into statements; a StatementBegin/End block is one.
func gooseDownStatements(t *testing.T, sql string) []string {
	t.Helper()
	idx := strings.Index(sql, "-- +goose Down")
	if idx < 0 {
		t.Fatal("no -- +goose Down section")
	}
	var stmts []string
	var cur []string
	inBlock := false
	flush := func() {
		if s := strings.TrimSpace(strings.Join(cur, "\n")); s != "" {
			stmts = append(stmts, s)
		}
		cur = nil
	}
	for _, line := range strings.Split(sql[idx:], "\n")[1:] {
		trim := strings.TrimSpace(line)
		switch {
		case trim == "-- +goose StatementBegin":
			inBlock = true
		case trim == "-- +goose StatementEnd":
			inBlock = false
			flush()
		case !inBlock && (trim == "" || strings.HasPrefix(trim, "--")):
		case !inBlock && strings.HasSuffix(trim, ";"):
			cur = append(cur, line)
			flush()
		default:
			cur = append(cur, line)
		}
	}
	flush()
	return stmts
}

// ---------------------------------------------------------------------
// ENGI-04-05 -- Migration B drops the single-active model.
// ---------------------------------------------------------------------

const dropActiveFlagMigrationGlob = "*_drop_rule_set_active_flag.sql"

func TestEffectiveDates_SingleActiveModelIsGone(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	// Positive probes first: the negative ones below would pass vacuously on a wrong table name.
	requireEffectiveFrom(t, ctx, super)
	var datedCheck, approvalColumn, approvalCheck bool
	if err := super.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'rule_set_versions_dated_is_sealed'),
		        EXISTS (SELECT 1 FROM information_schema.columns
		                 WHERE table_name = 'approval_policy_versions' AND column_name = 'is_active'),
		        EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'approval_policy_versions_active_is_sealed')`,
	).Scan(&datedCheck, &approvalColumn, &approvalCheck); err != nil {
		t.Fatalf("probe the successor and sibling model: %v", err)
	}
	if !datedCheck {
		t.Error("rule_set_versions_dated_is_sealed is missing: the successor invariant must stay")
	}
	if !approvalColumn || !approvalCheck {
		t.Errorf("approval_policy_versions.is_active present=%t, its CHECK present=%t: Migration B must leave that table alone",
			approvalColumn, approvalCheck)
	}

	for _, c := range []struct{ what, sql string }{
		{"column rule_set_versions.is_active", `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'rule_set_versions' AND column_name = 'is_active')`},
		{"index rule_set_versions_one_active", `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'rule_set_versions_one_active')`},
		{"constraint rule_set_versions_active_is_sealed", `SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'rule_set_versions_active_is_sealed')`},
	} {
		var exists bool
		if err := super.QueryRow(ctx, c.sql).Scan(&exists); err != nil {
			t.Fatalf("probe %s: %v", c.what, err)
		}
		if exists {
			t.Errorf("%s still exists, want it dropped (Core AC 1)", c.what)
		}
	}
}

func TestEffectiveDates_DropDownRestoresTheActiveFlag(t *testing.T) {
	migrator := migratorPool(t)
	ctx := context.Background()

	matches, err := fs.Glob(migrations.FS, dropActiveFlagMigrationGlob)
	if err != nil || len(matches) != 1 {
		t.Fatalf("want exactly one migrations/%s, got %v (err %v)", dropActiveFlagMigrationGlob, matches, err)
	}
	raw, err := fs.ReadFile(migrations.FS, matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	downStmts := gooseDownStatements(t, string(raw))
	if len(downStmts) == 0 {
		t.Fatalf("%s has no Down statements", matches[0])
	}

	const inForce = `rule_set_version_for((now() AT TIME ZONE 'UTC')::date)::text`
	for _, tc := range []struct {
		name         string
		fixtureToday bool
	}{
		{"the version in force is the active one", false},
		{"a version dated today becomes the active one", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := edBegin(t, ctx, migrator)
			requireEffectiveFrom(t, ctx, tx)
			var present bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'rule_set_versions' AND column_name = 'is_active')`).Scan(&present); err != nil {
				t.Fatalf("probe is_active: %v", err)
			}
			if present {
				t.Fatal("rule_set_versions.is_active exists before the Down: Migration B is not applied")
			}

			v4 := versionIDByVersion(t, ctx, tx, 4)
			want := v4
			if tc.fixtureToday {
				if err := tx.QueryRow(ctx,
					`INSERT INTO rule_set_versions (version, sealed, effective_from, notes)
					 VALUES ($1, true, (now() AT TIME ZONE 'UTC')::date, $2) RETURNING id`,
					nextVersion(), fixtureNotes).Scan(&want); err != nil {
					t.Fatalf("insert the fixture dated today: %v", err)
				}
			}
			var forceNow *string
			if err := tx.QueryRow(ctx, `SELECT `+inForce).Scan(&forceNow); err != nil {
				t.Fatalf("read the version in force: %v", err)
			}
			edWant(t, forceNow, want, "version in force before the Down")

			for _, stmt := range downStmts {
				if _, err := tx.Exec(ctx, stmt); err != nil {
					t.Fatalf("Down statement failed: %v\n%s", err, stmt)
				}
			}

			var colType, nullable, dflt string
			if err := tx.QueryRow(ctx,
				`SELECT data_type, is_nullable, coalesce(column_default, '') FROM information_schema.columns
				  WHERE table_name = 'rule_set_versions' AND column_name = 'is_active'`,
			).Scan(&colType, &nullable, &dflt); err != nil {
				t.Fatalf("is_active column missing after the Down: %v", err)
			}
			if colType != "boolean" || nullable != "NO" || dflt != "false" {
				t.Errorf("is_active = %s nullable=%s default=%q, want boolean NOT NULL DEFAULT false", colType, nullable, dflt)
			}

			var idxDef, checkDef string
			if err := tx.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE indexname = 'rule_set_versions_one_active'`).Scan(&idxDef); err != nil {
				t.Fatalf("rule_set_versions_one_active missing after the Down: %v", err)
			}
			if !strings.Contains(idxDef, "UNIQUE") || !strings.Contains(idxDef, "WHERE is_active") {
				t.Errorf("rule_set_versions_one_active = %q, want a unique index WHERE is_active", idxDef)
			}
			if err := tx.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'rule_set_versions_active_is_sealed'`).Scan(&checkDef); err != nil {
				t.Fatalf("rule_set_versions_active_is_sealed missing after the Down: %v", err)
			}
			if !strings.Contains(checkDef, "is_active") || !strings.Contains(checkDef, "sealed") {
				t.Errorf("rule_set_versions_active_is_sealed = %q, want a CHECK on is_active and sealed", checkDef)
			}

			var active []string
			rows, err := tx.Query(ctx, `SELECT id::text FROM rule_set_versions WHERE is_active`)
			if err != nil {
				t.Fatalf("read active rows: %v", err)
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					t.Fatalf("scan active row: %v", err)
				}
				active = append(active, id)
			}
			rows.Close()
			if len(active) != 1 || active[0] != want {
				t.Errorf("active rows after the Down = %v, want exactly [%s] (the version in force today)", active, want)
			}
			if tc.fixtureToday && len(active) == 1 && active[0] == v4 {
				t.Error("v4 is active although a version dated today is in force")
			}

			// The restored objects enforce, not just exist.
			unsealed := edFixture(t, ctx, tx, nextVersion(), false, "")
			assertSQLState(t, attemptWithSavepoint(t, ctx, tx,
				`UPDATE rule_set_versions SET is_active = true WHERE id = $1`, unsealed), "23514")
			sealedOther := edFixture(t, ctx, tx, nextVersion(), true, "")
			assertSQLState(t, attemptWithSavepoint(t, ctx, tx,
				`UPDATE rule_set_versions SET is_active = true WHERE id = $1`, sealedOther), "23505")
		})
	}
}

// TestEffectiveDates_DownChainKeepsTheActiveFlag: Migration B's Down then Migration A's Down
// leaves is_active and its index and CHECK, with the version in force still the active one
// (older Downs read the flag). Replaces the D9 check Migration A's own Down test lost.
func TestEffectiveDates_DownChainKeepsTheActiveFlag(t *testing.T) {
	migrator := migratorPool(t)
	ctx := context.Background()

	tx := edBegin(t, ctx, migrator)
	requireEffectiveFrom(t, ctx, tx)
	var inForce string
	if err := tx.QueryRow(ctx, `SELECT rule_set_version_for((now() AT TIME ZONE 'UTC')::date)::text`).Scan(&inForce); err != nil {
		t.Fatalf("read the version in force: %v", err)
	}

	for _, glob := range []string{dropActiveFlagMigrationGlob, effectiveDatesMigrationGlob} {
		matches, err := fs.Glob(migrations.FS, glob)
		if err != nil || len(matches) != 1 {
			t.Fatalf("want exactly one migrations/%s, got %v (err %v)", glob, matches, err)
		}
		raw, err := fs.ReadFile(migrations.FS, matches[0])
		if err != nil {
			t.Fatalf("read %s: %v", matches[0], err)
		}
		stmts := gooseDownStatements(t, string(raw))
		if len(stmts) == 0 {
			t.Fatalf("%s has no Down statements", matches[0])
		}
		for _, stmt := range stmts {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				t.Fatalf("Down statement of %s failed: %v\n%s", matches[0], err, stmt)
			}
		}
	}

	var column, index, check bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = 'rule_set_versions'::regclass AND attname = 'is_active' AND NOT attisdropped),
		        EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'rule_set_versions_one_active'),
		        EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'rule_set_versions_active_is_sealed')`,
	).Scan(&column, &index, &check); err != nil {
		t.Fatalf("probe the restored flag: %v", err)
	}
	if !column || !index || !check {
		t.Fatalf("after both Downs is_active column=%t, one_active index=%t, active_is_sealed CHECK=%t, want all present", column, index, check)
	}
	var active []string
	rows, err := tx.Query(ctx, `SELECT id::text FROM rule_set_versions WHERE is_active`)
	if err != nil {
		t.Fatalf("read active rows: %v", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan active row: %v", err)
		}
		active = append(active, id)
	}
	rows.Close()
	if len(active) != 1 || active[0] != inForce {
		t.Errorf("active rows after both Downs = %v, want exactly [%s] (the version that was in force)", active, inForce)
	}
}
