// The `contacts` table keeps each fact once. Contacts carry no tenant, so
// the guarantees here are grants, CHECKs and the facts-only-grow trigger, not RLS.
// Rows are seeded through the superuser pool; each rejected statement runs on its own
// implicit transaction so a failure cannot poison a later one.
package db_test

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	contactsTable         = "contacts"
	contactsFunction      = "contacts_facts_only_grow"
	contactsMigrationGlob = "*_contacts.sql"
	pgRestrictViolation   = "23001"
	pgCheckViolation      = "23514"
	pgInsufficientPriv    = "42501"
)

// contactsEmail is unique per call and already trimmed lower case; the row is removed
// on cleanup, which is registered before any assertion can fatal.
func contactsEmail(t *testing.T) string {
	t.Helper()
	email := "t-" + uuid.NewString() + "@contacts.test"
	contactsCleanup(t, email)
	return email
}

func contactsCleanup(t *testing.T, emails ...string) {
	t.Helper()
	hh := requireHarness(t)
	t.Cleanup(func() {
		_, _ = hh.super.Exec(context.Background(), `DELETE FROM contacts WHERE email = ANY($1)`, emails)
	})
}

// contactsSeed inserts a row as the superuser. cols/vals are column names and values.
func contactsSeed(t *testing.T, email string, cols []string, vals []any) {
	t.Helper()
	hh := requireHarness(t)
	all := append([]string{"email"}, cols...)
	args := append([]any{email}, vals...)
	ph := make([]string, len(all))
	for i := range all {
		ph[i] = "$" + string(rune('1'+i))
	}
	sql := "INSERT INTO contacts (" + strings.Join(all, ", ") + ") VALUES (" + strings.Join(ph, ", ") + ")"
	if _, err := hh.super.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("seed contacts row: %v", err)
	}
}

// contactsSeedConsented seeds a row that holds every fact the trigger protects.
func contactsSeedConsented(t *testing.T, email string, registered, demo, consented time.Time) {
	t.Helper()
	contactsSeed(t, email,
		[]string{"registered_at", "demo_requested_at", "marketing_consent_text", "marketing_consented_at"},
		[]any{registered, demo, "Yes, send me product news.", consented})
}

func wantPgCode(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("want SQLSTATE %s, got %v", code, err)
	}
	if pgErr.Code != code {
		t.Fatalf("want SQLSTATE %s, got %s: %s", code, pgErr.Code, pgErr.Message)
	}
}

// contactsRowCount counts rows for email as the superuser, so it also proves the row
// survived a refused statement.
func contactsRowCount(t *testing.T, email string) int {
	t.Helper()
	return mustCount(t, requireHarness(t).super, `SELECT count(*) FROM contacts WHERE email = $1`, email)
}

func contactsFactsOf(t *testing.T, email string) (reg, demo *time.Time, text *string, at *time.Time) {
	t.Helper()
	err := requireHarness(t).super.QueryRow(context.Background(),
		`SELECT registered_at, demo_requested_at, marketing_consent_text, marketing_consented_at
		   FROM contacts WHERE email = $1`, email).Scan(&reg, &demo, &text, &at)
	if err != nil {
		t.Fatalf("read facts of %s: %v", email, err)
	}
	return
}

// --- AC-1: grants --------------------------------------------------------------------

func TestRLS_ContactsAppMayInsertSelectUpdate(t *testing.T) {
	hh := requireHarness(t)
	ctx := context.Background()
	email := contactsEmail(t)

	if _, err := hh.app.Exec(ctx,
		`INSERT INTO contacts (email, first_name, last_name, company, registered_at)
		 VALUES ($1, 'Ada', 'Lovelace', 'Corp', now())`, email); err != nil {
		t.Fatalf("app INSERT: %v", err)
	}

	var first, last, company string
	var version int64
	if err := hh.app.QueryRow(ctx,
		`SELECT first_name, last_name, company, version FROM contacts WHERE email = $1`, email,
	).Scan(&first, &last, &company, &version); err != nil {
		t.Fatalf("app SELECT: %v", err)
	}
	if first != "Ada" || last != "Lovelace" || company != "Corp" || version != 1 {
		t.Fatalf("app read (%q, %q, %q, v%d), want (Ada, Lovelace, Corp, v1)", first, last, company, version)
	}

	tag, err := hh.app.Exec(ctx,
		`UPDATE contacts SET first_name = 'Augusta', last_name = 'King', company = 'Analytical' WHERE email = $1`, email)
	if err != nil {
		t.Fatalf("app UPDATE: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("app UPDATE touched %d rows, want 1", tag.RowsAffected())
	}
	if err := hh.super.QueryRow(ctx,
		`SELECT first_name, last_name, company FROM contacts WHERE email = $1`, email,
	).Scan(&first, &last, &company); err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if first != "Augusta" || last != "King" || company != "Analytical" {
		t.Fatalf("after UPDATE row is (%q, %q, %q), want (Augusta, King, Analytical)", first, last, company)
	}
}

func TestRLS_ContactsAppMayNotDelete(t *testing.T) {
	hh := requireHarness(t)
	email := contactsEmail(t)
	contactsSeed(t, email, []string{"registered_at"}, []any{contactsNow()})
	if n := contactsRowCount(t, email); n != 1 {
		t.Fatalf("seeded row count = %d, want 1", n)
	}

	_, err := hh.app.Exec(context.Background(), `DELETE FROM contacts WHERE email = $1`, email)
	wantPgCode(t, err, pgInsufficientPriv)
	if n := contactsRowCount(t, email); n != 1 {
		t.Fatalf("row count after refused DELETE = %d, want 1", n)
	}
}

func TestRLS_ContactsReaderMayNotSelect(t *testing.T) {
	hh := requireHarness(t)
	email := contactsEmail(t)
	contactsSeed(t, email, []string{"registered_at"}, []any{contactsNow()})
	if n := contactsRowCount(t, email); n != 1 {
		t.Fatalf("seeded row count = %d, want 1", n)
	}

	var n int
	err := hh.reader.QueryRow(context.Background(), `SELECT count(*) FROM contacts`).Scan(&n)
	wantPgCode(t, err, pgInsufficientPriv)
}

// --- AC-2: a set fact never changes --------------------------------------------------

// contactsFactsUnchanged re-reads the protected facts as the superuser and compares.
func contactsFactsUnchanged(t *testing.T, email string, reg, demo, consented time.Time) {
	t.Helper()
	r, d, text, at := contactsFactsOf(t, email)
	if r == nil || !r.Equal(reg) || d == nil || !d.Equal(demo) ||
		text == nil || *text != "Yes, send me product news." || at == nil || !at.Equal(consented) {
		t.Fatalf("facts changed despite the refusal: registered=%v demo=%v text=%v at=%v", r, d, text, at)
	}
}

func TestRLS_ContactsConsentCannotBeCleared(t *testing.T) {
	hh := requireHarness(t)
	email := contactsEmail(t)
	reg, demo, consented := contactsNow().Add(-3*time.Hour), contactsNow().Add(-2*time.Hour), contactsNow().Add(-time.Hour)
	contactsSeedConsented(t, email, reg, demo, consented)

	_, err := hh.app.Exec(context.Background(),
		`UPDATE contacts SET marketing_consent_text = NULL, marketing_consented_at = NULL WHERE email = $1`, email)
	wantPgCode(t, err, pgRestrictViolation)
	contactsFactsUnchanged(t, email, reg, demo, consented)
}

func TestRLS_ContactsConsentCannotBeRewritten(t *testing.T) {
	hh := requireHarness(t)
	ctx := context.Background()
	email := contactsEmail(t)
	reg, demo, consented := contactsNow().Add(-3*time.Hour), contactsNow().Add(-2*time.Hour), contactsNow().Add(-time.Hour)
	contactsSeedConsented(t, email, reg, demo, consented)

	_, err := hh.app.Exec(ctx,
		`UPDATE contacts SET marketing_consent_text = 'A different text.' WHERE email = $1`, email)
	wantPgCode(t, err, pgRestrictViolation)
	contactsFactsUnchanged(t, email, reg, demo, consented)

	_, err = hh.app.Exec(ctx,
		`UPDATE contacts SET marketing_consented_at = now() WHERE email = $1`, email)
	wantPgCode(t, err, pgRestrictViolation)
	contactsFactsUnchanged(t, email, reg, demo, consented)
}

func TestRLS_ContactsTagFactsCannotBeCleared(t *testing.T) {
	hh := requireHarness(t)
	ctx := context.Background()
	email := contactsEmail(t)
	reg, demo, consented := contactsNow().Add(-3*time.Hour), contactsNow().Add(-2*time.Hour), contactsNow().Add(-time.Hour)
	contactsSeedConsented(t, email, reg, demo, consented)

	for _, col := range []string{"registered_at", "demo_requested_at"} {
		// The other tag time stays set, so the either-tag CHECK cannot be what refuses.
		_, err := hh.app.Exec(ctx, `UPDATE contacts SET `+col+` = NULL WHERE email = $1`, email)
		wantPgCode(t, err, pgRestrictViolation)

		_, err = hh.app.Exec(ctx, `UPDATE contacts SET `+col+` = now() WHERE email = $1`, email)
		wantPgCode(t, err, pgRestrictViolation)
		contactsFactsUnchanged(t, email, reg, demo, consented)
	}
}

func TestRLS_ContactsOwnerCannotRewriteFacts(t *testing.T) {
	hh := requireHarness(t)
	ctx := context.Background()
	email := contactsEmail(t)
	reg, demo, consented := contactsNow().Add(-3*time.Hour), contactsNow().Add(-2*time.Hour), contactsNow().Add(-time.Hour)
	contactsSeedConsented(t, email, reg, demo, consented)

	// Positive control: the owner may write a non-fact column, so the refusal below is the trigger.
	if _, err := hh.mig.Exec(ctx, `UPDATE contacts SET company = 'Owner Co' WHERE email = $1`, email); err != nil {
		t.Fatalf("owner UPDATE of a non-fact column: %v", err)
	}

	_, err := hh.mig.Exec(ctx,
		`UPDATE contacts SET marketing_consent_text = 'Owner rewrote this.' WHERE email = $1`, email)
	wantPgCode(t, err, pgRestrictViolation)
	contactsFactsUnchanged(t, email, reg, demo, consented)
}

// --- AC-3: a NULL fact is set the first time ----------------------------------------

func TestRLS_ContactsFactSetOnceSucceeds(t *testing.T) {
	hh := requireHarness(t)
	email := contactsEmail(t)
	demo := contactsNow().Add(-time.Hour)
	contactsSeed(t, email, []string{"demo_requested_at"}, []any{demo})

	tag, err := hh.app.Exec(context.Background(),
		`UPDATE contacts
		    SET registered_at = now(), marketing_consent_text = 'Yes, send me product news.',
		        marketing_consented_at = now()
		  WHERE email = $1`, email)
	if err != nil {
		t.Fatalf("setting NULL facts for the first time: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("UPDATE touched %d rows, want 1", tag.RowsAffected())
	}
	reg, gotDemo, text, at := contactsFactsOf(t, email)
	if reg == nil || text == nil || *text != "Yes, send me product news." || at == nil {
		t.Fatalf("facts not stored: registered=%v text=%v at=%v", reg, text, at)
	}
	if gotDemo == nil || !gotDemo.Equal(demo) {
		t.Fatalf("demo_requested_at = %v, want it kept at %v", gotDemo, demo)
	}
}

// --- AC-4: CHECK constraints --------------------------------------------------------

func TestRLS_ContactsConsentNeedsTextAndTime(t *testing.T) {
	hh := requireHarness(t)
	ctx := context.Background()

	okEmail := contactsEmail(t)
	if _, err := hh.app.Exec(ctx,
		`INSERT INTO contacts (email, registered_at, marketing_consent_text, marketing_consented_at)
		 VALUES ($1, now(), 'Yes.', now())`, okEmail); err != nil {
		t.Fatalf("insert with text and time: %v", err)
	}

	textOnly := contactsEmail(t)
	_, err := hh.app.Exec(ctx,
		`INSERT INTO contacts (email, registered_at, marketing_consent_text) VALUES ($1, now(), 'Yes.')`, textOnly)
	wantPgCode(t, err, pgCheckViolation)

	timeOnly := contactsEmail(t)
	_, err = hh.app.Exec(ctx,
		`INSERT INTO contacts (email, registered_at, marketing_consented_at) VALUES ($1, now(), now())`, timeOnly)
	wantPgCode(t, err, pgCheckViolation)

	if n := contactsRowCount(t, textOnly) + contactsRowCount(t, timeOnly); n != 0 {
		t.Fatalf("refused inserts left %d rows", n)
	}
}

func TestRLS_ContactsNeedsATag(t *testing.T) {
	hh := requireHarness(t)
	ctx := context.Background()

	for name, col := range map[string]string{"registered": "registered_at", "demo": "demo_requested_at"} {
		email := contactsEmail(t)
		if _, err := hh.app.Exec(ctx, `INSERT INTO contacts (email, `+col+`) VALUES ($1, now())`, email); err != nil {
			t.Fatalf("insert with only %s tag: %v", name, err)
		}
	}

	email := contactsEmail(t)
	_, err := hh.app.Exec(ctx, `INSERT INTO contacts (email, first_name) VALUES ($1, 'Ada')`, email)
	wantPgCode(t, err, pgCheckViolation)
	if n := contactsRowCount(t, email); n != 0 {
		t.Fatalf("refused insert left %d rows", n)
	}
}

func TestRLS_ContactsEmailIsTrimmedLowerCase(t *testing.T) {
	hh := requireHarness(t)
	ctx := context.Background()
	const insert = `INSERT INTO contacts (email, registered_at) VALUES ($1, now())`

	ok := "ada-" + uuid.NewString() + "@corp.example"
	contactsCleanup(t, ok)
	if _, err := hh.app.Exec(ctx, insert, ok); err != nil {
		t.Fatalf("insert of a trimmed lower-case address: %v", err)
	}

	// 254 bytes is the ceiling and is accepted: 124 two-byte runes + "@a.ioo" = 254 bytes.
	at254 := strings.Repeat("é", 124) + "@a.ioo"
	contactsCleanup(t, at254)
	if got := len(at254); got != 254 {
		t.Fatalf("fixture is %d bytes, want 254", got)
	}
	if _, err := hh.app.Exec(ctx, insert, at254); err != nil {
		t.Fatalf("insert of a 254-byte address: %v", err)
	}

	// 3 bytes is the floor and is accepted; 2 and 0 are refused.
	const at3 = "abc"
	contactsCleanup(t, at3)
	if _, err := hh.app.Exec(ctx, insert, at3); err != nil {
		t.Fatalf("insert of a 3-byte address: %v", err)
	}

	over := strings.Repeat("é", 125) + "@a.io"
	if got := len(over); got != 255 {
		t.Fatalf("fixture is %d bytes, want 255", got)
	}
	bad := map[string]string{
		"leading space and capitals": " Ada@Corp.example",
		"capitals only":              "Ada@corp.example",
		"trailing space":             "ada@corp.example ",
		"255 bytes of multi-byte":    over,
		"2 bytes":                    "ab",
		"empty":                      "",
	}
	for name, email := range bad {
		contactsCleanup(t, email)
		_, err := hh.app.Exec(ctx, insert, email)
		if err == nil {
			t.Errorf("%s: insert succeeded, want SQLSTATE %s", name, pgCheckViolation)
			continue
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != pgCheckViolation {
			t.Errorf("%s: got %v, want SQLSTATE %s", name, err, pgCheckViolation)
		}
	}
	if n := contactsRowCount(t, ok) + contactsRowCount(t, at254) + contactsRowCount(t, at3); n != 3 {
		t.Fatalf("accepted addresses stored %d rows, want 3", n)
	}
}

// --- AC-5: Down and Up --------------------------------------------------------------

type contactsCatalog struct {
	tables    []string // public base tables
	functions []string // public functions
	hasTable  bool
	hasFunc   bool
	grants    []string // invoice_app privileges on contacts, sorted
	triggers  []string // "<name>:<function>:<before>:<update>" for non-internal triggers on contacts
	columns   []string // sorted
}

func contactsReadCatalog(t *testing.T, ctx context.Context, tx pgx.Tx) contactsCatalog {
	t.Helper()
	strs := func(what, sql string) []string {
		rows, err := tx.Query(ctx, sql)
		if err != nil {
			t.Fatalf("query %s: %v", what, err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatalf("scan %s: %v", what, err)
			}
			out = append(out, s)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate %s: %v", what, err)
		}
		return out
	}
	var c contactsCatalog
	// Never empty (the schema has other tables and functions), so an empty read is a broken query.
	c.tables = strs("public tables", `SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE' ORDER BY table_name`)
	c.functions = strs("public functions", `SELECT p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = 'public' ORDER BY p.proname`)
	if len(c.tables) == 0 || len(c.functions) == 0 {
		t.Fatalf("catalog read came back empty (tables=%d functions=%d) -- the query is broken", len(c.tables), len(c.functions))
	}
	c.hasTable = slices.Contains(c.tables, contactsTable)
	c.hasFunc = slices.Contains(c.functions, contactsFunction)
	if c.hasTable {
		c.grants = strs("invoice_app grants", `SELECT DISTINCT privilege_type FROM information_schema.role_table_grants
			WHERE table_schema = 'public' AND table_name = 'contacts' AND grantee = 'invoice_app' ORDER BY privilege_type`)
		c.triggers = strs("contacts triggers", `SELECT t.tgname || ':' || p.proname || ':' || ((t.tgtype & 2) <> 0)::text
			|| ':' || ((t.tgtype & 16) <> 0)::text
			FROM pg_trigger t JOIN pg_proc p ON p.oid = t.tgfoid
			WHERE t.tgrelid = 'public.contacts'::regclass AND NOT t.tgisinternal ORDER BY t.tgname`)
		c.columns = strs("contacts columns", `SELECT column_name FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'contacts' ORDER BY column_name`)
	}
	return c
}

func contactsAssertUpApplied(t *testing.T, c contactsCatalog) {
	t.Helper()
	if !c.hasTable {
		t.Fatalf("public.%s does not exist before the Down runs -- the migration is not applied, so this case would pass vacuously", contactsTable)
	}
	if !c.hasFunc {
		t.Fatalf("public.%s() does not exist before the Down runs -- the migration is not applied", contactsFunction)
	}
}

func contactsMigrationName(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, contactsMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", contactsMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d files matching %s (%v), want exactly 1", len(matches), contactsMigrationGlob, matches)
	}
	return matches[0]
}

func contactsSection(t *testing.T, section string) string {
	t.Helper()
	name := contactsMigrationName(t)
	b, err := fs.ReadFile(migrations.FS, name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	raw := string(b)
	up, ok := auditEntityUpOf(raw)
	if !ok {
		t.Fatalf("%s: want both %q and %q, in that order", name, gooseUp, gooseDown)
	}
	if section == "Up" {
		return up
	}
	return raw[strings.Index(raw, gooseDown)+len(gooseDown):]
}

func contactsExec(t *testing.T, ctx context.Context, tx pgx.Tx, section string) {
	t.Helper()
	if _, err := tx.Exec(ctx, contactsSection(t, section)); err != nil {
		t.Fatalf("%s body failed: %v", section, err)
	}
}

func contactsWithout(all []string, drop string) []string {
	return slices.DeleteFunc(slices.Clone(all), func(s string) bool { return s == drop })
}

func TestRLS_ContactsMigrationDownDropsTableAndFunction(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	tx := migratorTx(t, ctx)

	before := contactsReadCatalog(t, ctx, tx)
	contactsAssertUpApplied(t, before)
	contactsExec(t, ctx, tx, "Down")
	after := contactsReadCatalog(t, ctx, tx)

	if after.hasTable {
		t.Errorf("after Down public.%s still exists", contactsTable)
	}
	if after.hasFunc {
		t.Errorf("after Down public.%s() still exists", contactsFunction)
	}
	// Exactly the table and the function: every other public table and function survives.
	if want := contactsWithout(before.tables, contactsTable); !slices.Equal(after.tables, want) {
		t.Errorf("public tables after Down = %v, want %v", after.tables, want)
	}
	if want := contactsWithout(before.functions, contactsFunction); !slices.Equal(after.functions, want) {
		t.Errorf("public functions after Down = %v, want %v", after.functions, want)
	}
}

func TestRLS_ContactsMigrationUpRestoresWhatDownDropped(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	tx := migratorTx(t, ctx)

	before := contactsReadCatalog(t, ctx, tx)
	contactsAssertUpApplied(t, before)
	contactsExec(t, ctx, tx, "Down")
	if contactsReadCatalog(t, ctx, tx).hasTable {
		t.Fatalf("the Down left public.%s in place -- this case cannot tell 'Up restored it' from 'Down did nothing'", contactsTable)
	}
	contactsExec(t, ctx, tx, "Up")
	after := contactsReadCatalog(t, ctx, tx)

	contactsAssertUpApplied(t, after)
	if !slices.Equal(after.grants, []string{"INSERT", "SELECT", "UPDATE"}) {
		t.Errorf("invoice_app grants on contacts after Up = %v, want [INSERT SELECT UPDATE]", after.grants)
	}
	if !slices.Equal(after.columns, before.columns) {
		t.Errorf("columns after Down/Up = %v, want %v", after.columns, before.columns)
	}
	if !slices.Equal(after.tables, before.tables) || !slices.Equal(after.functions, before.functions) {
		t.Errorf("public tables or functions differ after Down/Up")
	}
	// One BEFORE UPDATE trigger on the function; the trigger's own name is not pinned here.
	if len(after.triggers) != 1 || !strings.HasSuffix(after.triggers[0], ":"+contactsFunction+":true:true") {
		t.Fatalf("triggers on contacts after Up = %v, want one BEFORE UPDATE trigger calling %s", after.triggers, contactsFunction)
	}
	if !slices.Equal(after.triggers, before.triggers) {
		t.Errorf("triggers after Down/Up = %v, want %v", after.triggers, before.triggers)
	}

	// The restored trigger still refuses a rewrite. Last statement: a failure aborts the tx.
	email := "t-" + uuid.NewString() + "@contacts.test"
	if _, err := tx.Exec(ctx,
		`INSERT INTO contacts (email, registered_at) VALUES ($1, now())`, email); err != nil {
		t.Fatalf("insert after Up: %v", err)
	}
	_, err := tx.Exec(ctx, `UPDATE contacts SET registered_at = registered_at + interval '1 day' WHERE email = $1`, email)
	wantPgCode(t, err, pgRestrictViolation)
}

// --- Adversarial contract: live DB as invoice_app, and the migration file replayed ----
//
// The cases above see a DB that was migrated before the run, so a regression in the
// migration SQL cannot fail them. contactsContract runs against both: the live DB as
// invoice_app, and the file's own Down then Up inside a rolled-back owner transaction.

// contactsConn is satisfied by *pgxpool.Pool and by contactsSavepointConn.
type contactsConn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// contactsSavepointConn runs each Exec in a savepoint so a refusal leaves the transaction usable.
type contactsSavepointConn struct{ tx pgx.Tx }

func (c contactsSavepointConn) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	sp, err := c.tx.Begin(ctx)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	tag, err := sp.Exec(ctx, sql, args...)
	if err != nil {
		_ = sp.Rollback(ctx)
		return tag, err
	}
	return tag, sp.Commit(ctx)
}

func (c contactsSavepointConn) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return c.tx.QueryRow(ctx, sql, args...)
}

// contactsReplayed returns the table as the migration file creates it: Down, then Up.
// The transaction rolls back before the cleanups run: they DELETE rows and would block on its lock.
func contactsReplayed(t *testing.T) pgx.Tx {
	t.Helper()
	requireHarness(t)
	ctx := context.Background()
	tx := migratorTx(t, ctx)
	contactsExec(t, ctx, tx, "Down")
	contactsExec(t, ctx, tx, "Up")
	return tx
}

func contactsRollback(tx pgx.Tx) { _ = tx.Rollback(context.Background()) }

var (
	contactsRegAt     = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	contactsDemoAt    = time.Date(2026, 1, 3, 3, 4, 5, 0, time.UTC)
	contactsConsentAt = time.Date(2026, 1, 4, 3, 4, 5, 0, time.UTC)
)

const contactsConsentText = "Yes, send me product news."

func contactsMustExec(t *testing.T, c contactsConn, sql string, args ...any) pgconn.CommandTag {
	t.Helper()
	tag, err := c.Exec(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return tag
}

func contactsRefuse(t *testing.T, c contactsConn, code, sql string, args ...any) {
	t.Helper()
	_, err := c.Exec(context.Background(), sql, args...)
	wantPgCode(t, err, code)
}

func contactsSeedFull(t *testing.T, c contactsConn) string {
	t.Helper()
	email := contactsEmail(t)
	contactsMustExec(t, c,
		`INSERT INTO contacts (email, registered_at, demo_requested_at, marketing_consent_text, marketing_consented_at)
		 VALUES ($1, $2, $3, $4, $5)`, email, contactsRegAt, contactsDemoAt, contactsConsentText, contactsConsentAt)
	return email
}

func contactsFactsIntact(t *testing.T, c contactsConn, email string) {
	t.Helper()
	var ok bool
	err := c.QueryRow(context.Background(),
		`SELECT coalesce(registered_at = $2 AND demo_requested_at = $3
		            AND marketing_consent_text = $4 AND marketing_consented_at = $5, false)
		   FROM contacts WHERE email = $1`,
		email, contactsRegAt, contactsDemoAt, contactsConsentText, contactsConsentAt).Scan(&ok)
	if err != nil || !ok {
		t.Fatalf("protected facts changed or row missing (err=%v)", err)
	}
}

func contactsFactCases() []struct{ name, col, rewrite string } {
	return []struct{ name, col, rewrite string }{
		{"registered_at", "registered_at", "registered_at + interval '1 day'"},
		{"demo_requested_at", "demo_requested_at", "demo_requested_at + interval '1 day'"},
		{"marketing_consent_text", "marketing_consent_text", "'A different text.'"},
		{"marketing_consented_at", "marketing_consented_at", "marketing_consented_at + interval '1 day'"},
	}
}

type contactsContractCase struct {
	name string
	live bool // also run as invoice_app on the live DB
	run  func(t *testing.T, c contactsConn)
}

func contactsContract() []contactsContractCase {
	var cases []contactsContractCase
	// Each fact alone: clearing text without time would also trip the pair CHECK, so the
	// SQLSTATE proves the trigger ran first and named the fact.
	for _, f := range contactsFactCases() {
		cases = append(cases, contactsContractCase{"FactsCannotChange_" + f.name, true, func(t *testing.T, c contactsConn) {
			email := contactsSeedFull(t, c)
			contactsRefuse(t, c, pgRestrictViolation, `UPDATE contacts SET `+f.col+` = NULL WHERE email = $1`, email)
			contactsFactsIntact(t, c, email)
			contactsRefuse(t, c, pgRestrictViolation, `UPDATE contacts SET `+f.col+` = `+f.rewrite+` WHERE email = $1`, email)
			contactsFactsIntact(t, c, email)
		}})
	}
	cases = append(cases,
		contactsContractCase{"WriteBackSucceeds", true, func(t *testing.T, c contactsConn) {
			email := contactsSeedFull(t, c)
			tag := contactsMustExec(t, c,
				`UPDATE contacts SET registered_at = registered_at, demo_requested_at = demo_requested_at,
				        marketing_consent_text = marketing_consent_text, marketing_consented_at = marketing_consented_at,
				        company = 'Same Co' WHERE email = $1`, email)
			if tag.RowsAffected() != 1 {
				t.Fatalf("write-back touched %d rows, want 1", tag.RowsAffected())
			}
			tag = contactsMustExec(t, c,
				`UPDATE contacts SET registered_at = $2, demo_requested_at = $3,
				        marketing_consent_text = $4, marketing_consented_at = $5 WHERE email = $1`,
				email, contactsRegAt, contactsDemoAt, contactsConsentText, contactsConsentAt)
			if tag.RowsAffected() != 1 {
				t.Fatalf("literal write-back touched %d rows, want 1", tag.RowsAffected())
			}
			contactsFactsIntact(t, c, email)
		}},
		// The notifications merge upsert keeps a set fact with COALESCE(contacts.x, EXCLUDED.x).
		contactsContractCase{"MergeUpsertKeepsSetFacts", true, func(t *testing.T, c contactsConn) {
			email := contactsSeedFull(t, c)
			tag := contactsMustExec(t, c,
				`INSERT INTO contacts (email, registered_at, demo_requested_at, marketing_consent_text, marketing_consented_at, first_name)
				 VALUES ($1, now(), now(), 'Later text.', now(), 'Grace')
				 ON CONFLICT (email) DO UPDATE SET
				   registered_at = COALESCE(contacts.registered_at, EXCLUDED.registered_at),
				   demo_requested_at = COALESCE(contacts.demo_requested_at, EXCLUDED.demo_requested_at),
				   marketing_consent_text = COALESCE(contacts.marketing_consent_text, EXCLUDED.marketing_consent_text),
				   marketing_consented_at = COALESCE(contacts.marketing_consented_at, EXCLUDED.marketing_consented_at),
				   first_name = COALESCE(NULLIF(EXCLUDED.first_name, ''), contacts.first_name)`, email)
			if tag.RowsAffected() != 1 {
				t.Fatalf("merge upsert touched %d rows, want 1", tag.RowsAffected())
			}
			contactsFactsIntact(t, c, email)
			var first string
			if err := c.QueryRow(context.Background(), `SELECT first_name FROM contacts WHERE email = $1`, email).Scan(&first); err != nil || first != "Grace" {
				t.Fatalf("first_name = %q (err=%v), want Grace", first, err)
			}
		}},
		contactsContractCase{"FirstSetSucceeds", true, func(t *testing.T, c contactsConn) {
			demoOnly := contactsEmail(t)
			contactsMustExec(t, c, `INSERT INTO contacts (email, demo_requested_at) VALUES ($1, $2)`, demoOnly, contactsDemoAt)
			contactsMustExec(t, c, `UPDATE contacts SET registered_at = $2 WHERE email = $1`, demoOnly, contactsRegAt)
			contactsMustExec(t, c,
				`UPDATE contacts SET marketing_consent_text = $2, marketing_consented_at = $3 WHERE email = $1`,
				demoOnly, contactsConsentText, contactsConsentAt)
			contactsFactsIntact(t, c, demoOnly)

			regOnly := contactsEmail(t)
			contactsMustExec(t, c, `INSERT INTO contacts (email, registered_at) VALUES ($1, $2)`, regOnly, contactsRegAt)
			contactsMustExec(t, c, `UPDATE contacts SET demo_requested_at = $2 WHERE email = $1`, regOnly, contactsDemoAt)
			var got time.Time
			if err := c.QueryRow(context.Background(), `SELECT demo_requested_at FROM contacts WHERE email = $1`, regOnly).Scan(&got); err != nil || !got.Equal(contactsDemoAt) {
				t.Fatalf("demo_requested_at = %v (err=%v), want %v", got, err, contactsDemoAt)
			}
		}},
		contactsContractCase{"NonFactColumnsStayWritable", true, func(t *testing.T, c contactsConn) {
			email := contactsSeedFull(t, c)
			tag := contactsMustExec(t, c,
				`UPDATE contacts SET first_name = 'A', last_name = 'B', company = 'C', user_id = $2, version = version + 1,
				        hubspot_delivered_at = now(), resend_delivered_at = now(), resend_opt_in_sent_at = now(),
				        delivery_mode = 'real', updated_at = now() WHERE email = $1`, email, uuid.New())
			if tag.RowsAffected() != 1 {
				t.Fatalf("UPDATE touched %d rows, want 1 (a trigger returning NULL would skip the row)", tag.RowsAffected())
			}
			var version int64
			var mode string
			var delivered bool
			if err := c.QueryRow(context.Background(),
				`SELECT version, delivery_mode, hubspot_delivered_at IS NOT NULL FROM contacts WHERE email = $1`, email,
			).Scan(&version, &mode, &delivered); err != nil {
				t.Fatalf("read back: %v", err)
			}
			if version != 2 || mode != "real" || !delivered {
				t.Fatalf("read back (v%d, %q, delivered=%v), want (v2, real, true)", version, mode, delivered)
			}
			contactsFactsIntact(t, c, email)
		}},
		contactsContractCase{"VersionDefaultsToOne", true, func(t *testing.T, c contactsConn) {
			email := contactsEmail(t)
			contactsMustExec(t, c, `INSERT INTO contacts (email, registered_at) VALUES ($1, now())`, email)
			var version int64
			var stamped bool
			if err := c.QueryRow(context.Background(),
				`SELECT version, created_at IS NOT NULL AND updated_at IS NOT NULL FROM contacts WHERE email = $1`, email,
			).Scan(&version, &stamped); err != nil || version != 1 || !stamped {
				t.Fatalf("defaults (v%d, stamped=%v, err=%v), want (v1, true)", version, stamped, err)
			}
		}},
		contactsContractCase{"DeliveryModeIsRealOrFake", true, func(t *testing.T, c contactsConn) {
			for _, ok := range []any{"real", "fake", nil} {
				contactsMustExec(t, c, `INSERT INTO contacts (email, registered_at, delivery_mode) VALUES ($1, now(), $2)`, contactsEmail(t), ok)
			}
			for _, bad := range []string{"off", "", "REAL", "real "} {
				contactsRefuse(t, c, pgCheckViolation,
					`INSERT INTO contacts (email, registered_at, delivery_mode) VALUES ($1, now(), $2)`, contactsEmail(t), bad)
			}
		}},
		// user_id carries no CHECK: its uuid type is the whole constraint.
		contactsContractCase{"UserIDMustBeAUUID", true, func(t *testing.T, c contactsConn) {
			contactsMustExec(t, c, `INSERT INTO contacts (email, registered_at, user_id) VALUES ($1, now(), $2)`, contactsEmail(t), uuid.New())
			contactsRefuse(t, c, "22P02",
				`INSERT INTO contacts (email, registered_at, user_id) VALUES ($1, now(), 'not-a-uuid')`, contactsEmail(t))
		}},
		contactsContractCase{"ConsentNeedsTextAndTime", false, func(t *testing.T, c contactsConn) {
			contactsMustExec(t, c, `INSERT INTO contacts (email, registered_at, marketing_consent_text, marketing_consented_at)
				VALUES ($1, now(), 'Yes.', now())`, contactsEmail(t))
			contactsMustExec(t, c, `INSERT INTO contacts (email, registered_at) VALUES ($1, now())`, contactsEmail(t))
			contactsRefuse(t, c, pgCheckViolation,
				`INSERT INTO contacts (email, registered_at, marketing_consent_text) VALUES ($1, now(), 'Yes.')`, contactsEmail(t))
			contactsRefuse(t, c, pgCheckViolation,
				`INSERT INTO contacts (email, registered_at, marketing_consented_at) VALUES ($1, now(), now())`, contactsEmail(t))
		}},
		contactsContractCase{"NeedsATag", false, func(t *testing.T, c contactsConn) {
			for _, col := range []string{"registered_at", "demo_requested_at", "registered_at, demo_requested_at"} {
				vals := strings.Repeat("now(), ", strings.Count(col, ",")+1)
				contactsMustExec(t, c, `INSERT INTO contacts (email, `+col+`) VALUES ($1, `+strings.TrimSuffix(vals, ", ")+`)`, contactsEmail(t))
			}
			contactsRefuse(t, c, pgCheckViolation, `INSERT INTO contacts (email, first_name) VALUES ($1, 'Ada')`, contactsEmail(t))
		}},
		contactsContractCase{"EmailShape", false, func(t *testing.T, c contactsConn) {
			const insert = `INSERT INTO contacts (email, registered_at) VALUES ($1, now())`
			good := []string{
				"abc", // 3 bytes: the floor
				strings.Repeat("é", 124) + "@a.ioo",
				"t-" + uuid.NewString() + "@contacts.test",
			}
			bad := []string{
				"", "ab", // under the floor
				strings.Repeat("é", 125) + "@a.io", // 255 bytes, 130 runes
				" ada@corp.example", "ada@corp.example ", "Ada@corp.example",
			}
			for _, e := range good {
				contactsCleanup(t, e)
				contactsMustExec(t, c, insert, e)
			}
			for _, e := range bad {
				contactsCleanup(t, e)
				contactsRefuse(t, c, pgCheckViolation, insert, e)
			}
		}},
	)
	return cases
}

func TestRLS_ContactsContractAsApp(t *testing.T) {
	hh := requireHarness(t)
	ran := 0
	for _, tc := range contactsContract() {
		if !tc.live {
			continue
		}
		ran++
		t.Run(tc.name, func(t *testing.T) { tc.run(t, hh.app) })
	}
	if ran == 0 {
		t.Fatal("no live cases ran")
	}
}

func TestRLS_ContactsReplayedMigrationHoldsContract(t *testing.T) {
	requireHarness(t)
	cases := contactsContract()
	if len(cases) == 0 {
		t.Fatal("no contract cases")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx := contactsReplayed(t)
			defer contactsRollback(tx)
			tc.run(t, contactsSavepointConn{tx})
		})
	}

	for _, g := range []struct {
		role, priv string
		want       bool
	}{
		{"invoice_app", "SELECT", true}, {"invoice_app", "INSERT", true}, {"invoice_app", "UPDATE", true},
		{"invoice_app", "DELETE", false}, {"invoice_app", "TRUNCATE", false},
		{"invoice_tenant_reader", "SELECT", false}, {"invoice_tenant_reader", "INSERT", false},
		{"invoice_tenant_reader", "UPDATE", false}, {"invoice_tenant_reader", "DELETE", false},
	} {
		t.Run("Grant_"+g.role+"_"+g.priv, func(t *testing.T) {
			tx := contactsReplayed(t)
			defer contactsRollback(tx)
			var got bool
			err := tx.QueryRow(context.Background(),
				`SELECT has_table_privilege($1, 'public.contacts', $2)`, g.role, g.priv).Scan(&got)
			if err != nil || got != g.want {
				t.Fatalf("%s %s on contacts = %v (err=%v), want %v", g.role, g.priv, got, err, g.want)
			}
		})
	}
}

// timestamptz holds microseconds; a finer Go time never round-trips equal.
func contactsNow() time.Time { return time.Now().Truncate(time.Microsecond) }
