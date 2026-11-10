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
	// contactsReadinessMigrationGlob is the migration that adds readiness_checked_at.
	contactsReadinessMigrationGlob = "*_contacts_readiness_checked.sql"
	// contactsReadinessConsentMigrationGlob is the migration that adds readiness_consent_text and readiness_consented_at.
	contactsReadinessConsentMigrationGlob = "*_contacts_readiness_consent.sql"
	// contactsTwoColumnSourceDef is how Postgres prints the source CHECK before readiness_checked_at.
	contactsTwoColumnSourceDef = "CHECK (((registered_at IS NOT NULL) OR (demo_requested_at IS NOT NULL)))"
	pgRestrictViolation        = "23001"
	pgCheckViolation           = "23514"
	pgInsufficientPriv         = "42501"
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

// contactsRequireReadinessColumn fails by name when the migration is not applied to the live DB.
func contactsRequireReadinessColumn(t *testing.T) {
	t.Helper()
	var typ, nullable string
	err := requireHarness(t).super.QueryRow(context.Background(),
		`SELECT data_type, is_nullable FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'contacts' AND column_name = 'readiness_checked_at'`,
	).Scan(&typ, &nullable)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("contacts.readiness_checked_at does not exist: the readiness migration is not applied")
	}
	if err != nil {
		t.Fatalf("read contacts.readiness_checked_at: %v", err)
	}
	if typ != "timestamp with time zone" || nullable != "YES" {
		t.Fatalf("contacts.readiness_checked_at is (%s, nullable=%s), want (timestamp with time zone, YES)", typ, nullable)
	}
}

// contactsSeedAllSources seeds a row that holds all five protected facts.
func contactsSeedAllSources(t *testing.T, email string, registered, demo, consented time.Time) {
	t.Helper()
	contactsRequireReadinessColumn(t)
	contactsSeed(t, email,
		[]string{"registered_at", "demo_requested_at", "marketing_consent_text", "marketing_consented_at", "readiness_checked_at"},
		[]any{registered, demo, "Yes, send me product news.", consented, contactsCheckAt})
}

// contactsReadinessUnchanged re-reads readiness_checked_at as the superuser.
func contactsReadinessUnchanged(t *testing.T, email string) {
	t.Helper()
	var got *time.Time
	err := requireHarness(t).super.QueryRow(context.Background(),
		`SELECT readiness_checked_at FROM contacts WHERE email = $1`, email).Scan(&got)
	if err != nil || got == nil || !got.Equal(contactsCheckAt) {
		t.Fatalf("readiness_checked_at = %v (err=%v), want it kept at %v", got, err, contactsCheckAt)
	}
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

// wantCheckRefusal asserts a 23514 raised by the named constraint, so a sibling CHECK cannot pass for it.
func wantCheckRefusal(t *testing.T, err error, constraint string) {
	t.Helper()
	wantPgCode(t, err, pgCheckViolation)
	var pgErr *pgconn.PgError
	errors.As(err, &pgErr)
	if pgErr.ConstraintName != constraint {
		t.Fatalf("refused by constraint %q, want %q", pgErr.ConstraintName, constraint)
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

// The erase routine deletes a contact; the facts-only-grow trigger must not refuse a DELETE.
func TestRLS_ContactsAppMayDelete(t *testing.T) {
	hh := requireHarness(t)
	email := contactsEmail(t)
	contactsSeedConsented(t, email, contactsNow().Add(-3*time.Hour), contactsNow().Add(-2*time.Hour), contactsNow().Add(-time.Hour))
	if n := contactsRowCount(t, email); n != 1 {
		t.Fatalf("seeded row count = %d, want 1", n)
	}

	tag, err := hh.app.Exec(context.Background(), `DELETE FROM contacts WHERE email = $1`, email)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("app DELETE of a row with every fact set: rows=%d err=%v, want 1 row", tag.RowsAffected(), err)
	}
	if n := contactsRowCount(t, email); n != 0 {
		t.Fatalf("row count after DELETE = %d, want 0", n)
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
	contactsSeedAllSources(t, email, reg, demo, consented)

	for _, col := range []string{"registered_at", "demo_requested_at", "readiness_checked_at"} {
		// The other sources stay set, so the source CHECK cannot be what refuses.
		_, err := hh.app.Exec(ctx, `UPDATE contacts SET `+col+` = NULL WHERE email = $1`, email)
		wantPgCode(t, err, pgRestrictViolation)

		_, err = hh.app.Exec(ctx, `UPDATE contacts SET `+col+` = now() WHERE email = $1`, email)
		wantPgCode(t, err, pgRestrictViolation)
		contactsFactsUnchanged(t, email, reg, demo, consented)
		contactsReadinessUnchanged(t, email)
	}
}

func TestRLS_ContactsOwnerCannotRewriteFacts(t *testing.T) {
	hh := requireHarness(t)
	ctx := context.Background()
	email := contactsEmail(t)
	reg, demo, consented := contactsNow().Add(-3*time.Hour), contactsNow().Add(-2*time.Hour), contactsNow().Add(-time.Hour)
	contactsSeedAllSources(t, email, reg, demo, consented)

	// Positive control: the owner may write a non-fact column, so the refusal below is the trigger.
	if _, err := hh.mig.Exec(ctx, `UPDATE contacts SET company = 'Owner Co' WHERE email = $1`, email); err != nil {
		t.Fatalf("owner UPDATE of a non-fact column: %v", err)
	}

	_, err := hh.mig.Exec(ctx,
		`UPDATE contacts SET marketing_consent_text = 'Owner rewrote this.' WHERE email = $1`, email)
	wantPgCode(t, err, pgRestrictViolation)
	contactsFactsUnchanged(t, email, reg, demo, consented)

	_, err = hh.mig.Exec(ctx,
		`UPDATE contacts SET readiness_checked_at = readiness_checked_at + interval '1 day' WHERE email = $1`, email)
	wantPgCode(t, err, pgRestrictViolation)
	contactsFactsUnchanged(t, email, reg, demo, consented)
	contactsReadinessUnchanged(t, email)
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

	// A registered-only row takes its readiness check the first time, and keeps registered_at.
	contactsRequireReadinessColumn(t)
	regOnly := contactsEmail(t)
	registered := contactsNow().Add(-2 * time.Hour)
	contactsSeed(t, regOnly, []string{"registered_at"}, []any{registered})
	tag, err = hh.app.Exec(context.Background(),
		`UPDATE contacts SET readiness_checked_at = now() WHERE email = $1`, regOnly)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("setting readiness_checked_at for the first time: rows=%d err=%v, want 1 row", tag.RowsAffected(), err)
	}
	var checked *time.Time
	if err := hh.super.QueryRow(context.Background(),
		`SELECT readiness_checked_at FROM contacts WHERE email = $1`, regOnly).Scan(&checked); err != nil || checked == nil {
		t.Fatalf("readiness_checked_at not stored (err=%v)", err)
	}
	reg, _, _, _ = contactsFactsOf(t, regOnly)
	if reg == nil || !reg.Equal(registered) {
		t.Fatalf("registered_at = %v, want it kept at %v", reg, registered)
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

	for name, col := range map[string]string{
		"registered": "registered_at", "demo": "demo_requested_at", "readiness": "readiness_checked_at",
	} {
		email := contactsEmail(t)
		if _, err := hh.app.Exec(ctx, `INSERT INTO contacts (email, `+col+`) VALUES ($1, now())`, email); err != nil {
			t.Fatalf("insert with only %s tag: %v", name, err)
		}
	}

	// Consent pair NULL, valid email, no delivery_mode: only the source CHECK can refuse this.
	email := contactsEmail(t)
	_, err := hh.app.Exec(ctx, `INSERT INTO contacts (email, first_name) VALUES ($1, 'Ada')`, email)
	wantCheckRefusal(t, err, "contacts_has_a_source")
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

func contactsMigrationName(t *testing.T, glob string) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, glob)
	if err != nil {
		t.Fatalf("glob %s: %v", glob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d files matching %s (%v), want exactly 1", len(matches), glob, matches)
	}
	return matches[0]
}

func contactsSection(t *testing.T, glob, section string) string {
	t.Helper()
	name := contactsMigrationName(t, glob)
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

func contactsExec(t *testing.T, ctx context.Context, tx pgx.Tx, glob, section string) {
	t.Helper()
	if _, err := tx.Exec(ctx, contactsSection(t, glob, section)); err != nil {
		t.Fatalf("%s body of %s failed: %v", section, glob, err)
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
	contactsExec(t, ctx, tx, contactsMigrationGlob, "Down")
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
	contactsExec(t, ctx, tx, contactsMigrationGlob, "Down")
	if contactsReadCatalog(t, ctx, tx).hasTable {
		t.Fatalf("the Down left public.%s in place -- this case cannot tell 'Up restored it' from 'Down did nothing'", contactsTable)
	}
	contactsExec(t, ctx, tx, contactsMigrationGlob, "Up")
	contactsExec(t, ctx, tx, contactsReadinessMigrationGlob, "Up")
	contactsExec(t, ctx, tx, contactsReadinessConsentMigrationGlob, "Up")
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

// contactsReplayed returns the table as the migration files create it: the original Down and Up,
// then the readiness Up and the readiness consent Up.
// The transaction rolls back before the cleanups run: they DELETE rows and would block on its lock.
func contactsReplayed(t *testing.T) pgx.Tx {
	t.Helper()
	requireHarness(t)
	ctx := context.Background()
	tx := migratorTx(t, ctx)
	contactsExec(t, ctx, tx, contactsMigrationGlob, "Down")
	contactsExec(t, ctx, tx, contactsMigrationGlob, "Up")
	contactsExec(t, ctx, tx, contactsReadinessMigrationGlob, "Up")
	contactsExec(t, ctx, tx, contactsReadinessConsentMigrationGlob, "Up")
	return tx
}

func contactsRollback(tx pgx.Tx) { _ = tx.Rollback(context.Background()) }

var (
	contactsRegAt     = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	contactsDemoAt    = time.Date(2026, 1, 3, 3, 4, 5, 0, time.UTC)
	contactsConsentAt = time.Date(2026, 1, 4, 3, 4, 5, 0, time.UTC)
	contactsCheckAt   = time.Date(2026, 1, 5, 3, 4, 5, 0, time.UTC)
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
		`INSERT INTO contacts (email, registered_at, demo_requested_at, marketing_consent_text, marketing_consented_at, readiness_checked_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`, email, contactsRegAt, contactsDemoAt, contactsConsentText, contactsConsentAt, contactsCheckAt)
	return email
}

func contactsFactsIntact(t *testing.T, c contactsConn, email string) {
	t.Helper()
	var ok bool
	err := c.QueryRow(context.Background(),
		`SELECT coalesce(registered_at = $2 AND demo_requested_at = $3
		            AND marketing_consent_text = $4 AND marketing_consented_at = $5
		            AND readiness_checked_at = $6, false)
		   FROM contacts WHERE email = $1`,
		email, contactsRegAt, contactsDemoAt, contactsConsentText, contactsConsentAt, contactsCheckAt).Scan(&ok)
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
		{"readiness_checked_at", "readiness_checked_at", "readiness_checked_at + interval '1 day'"},
	}
}

type contactsContractCase struct {
	name string
	live bool // also run as invoice_app on the live DB
	run  func(t *testing.T, c contactsConn)
}

func contactsContract() []contactsContractCase {
	var cases []contactsContractCase
	// Each of the five facts alone: clearing text without time would also trip the pair CHECK, so the
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
				        readiness_checked_at = readiness_checked_at, company = 'Same Co' WHERE email = $1`, email)
			if tag.RowsAffected() != 1 {
				t.Fatalf("write-back touched %d rows, want 1", tag.RowsAffected())
			}
			tag = contactsMustExec(t, c,
				`UPDATE contacts SET registered_at = $2, demo_requested_at = $3,
				        marketing_consent_text = $4, marketing_consented_at = $5, readiness_checked_at = $6 WHERE email = $1`,
				email, contactsRegAt, contactsDemoAt, contactsConsentText, contactsConsentAt, contactsCheckAt)
			if tag.RowsAffected() != 1 {
				t.Fatalf("literal write-back touched %d rows, want 1", tag.RowsAffected())
			}
			contactsFactsIntact(t, c, email)
		}},
		// The notifications merge upsert keeps a set fact with COALESCE(contacts.x, EXCLUDED.x).
		contactsContractCase{"MergeUpsertKeepsSetFacts", true, func(t *testing.T, c contactsConn) {
			email := contactsSeedFull(t, c)
			tag := contactsMustExec(t, c,
				`INSERT INTO contacts (email, registered_at, demo_requested_at, marketing_consent_text, marketing_consented_at, readiness_checked_at, first_name)
				 VALUES ($1, now(), now(), 'Later text.', now(), now(), 'Grace')
				 ON CONFLICT (email) DO UPDATE SET
				   registered_at = COALESCE(contacts.registered_at, EXCLUDED.registered_at),
				   demo_requested_at = COALESCE(contacts.demo_requested_at, EXCLUDED.demo_requested_at),
				   marketing_consent_text = COALESCE(contacts.marketing_consent_text, EXCLUDED.marketing_consent_text),
				   marketing_consented_at = COALESCE(contacts.marketing_consented_at, EXCLUDED.marketing_consented_at),
				   readiness_checked_at = COALESCE(contacts.readiness_checked_at, EXCLUDED.readiness_checked_at),
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
			contactsMustExec(t, c, `UPDATE contacts SET readiness_checked_at = $2 WHERE email = $1`, demoOnly, contactsCheckAt)
			contactsFactsIntact(t, c, demoOnly)

			regOnly := contactsEmail(t)
			contactsMustExec(t, c, `INSERT INTO contacts (email, registered_at) VALUES ($1, $2)`, regOnly, contactsRegAt)
			contactsMustExec(t, c, `UPDATE contacts SET demo_requested_at = $2 WHERE email = $1`, regOnly, contactsDemoAt)
			var got time.Time
			if err := c.QueryRow(context.Background(), `SELECT demo_requested_at FROM contacts WHERE email = $1`, regOnly).Scan(&got); err != nil || !got.Equal(contactsDemoAt) {
				t.Fatalf("demo_requested_at = %v (err=%v), want %v", got, err, contactsDemoAt)
			}

			readinessOnly := contactsEmail(t)
			contactsMustExec(t, c, `INSERT INTO contacts (email, readiness_checked_at) VALUES ($1, $2)`, readinessOnly, contactsCheckAt)
			contactsMustExec(t, c, `UPDATE contacts SET registered_at = $2 WHERE email = $1`, readinessOnly, contactsRegAt)
			contactsRefuse(t, c, pgRestrictViolation,
				`UPDATE contacts SET readiness_checked_at = readiness_checked_at + interval '1 day' WHERE email = $1`, readinessOnly)
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
			for _, col := range []string{
				"registered_at", "demo_requested_at", "readiness_checked_at", "registered_at, demo_requested_at, readiness_checked_at",
			} {
				vals := strings.Repeat("now(), ", strings.Count(col, ",")+1)
				contactsMustExec(t, c, `INSERT INTO contacts (email, `+col+`) VALUES ($1, `+strings.TrimSuffix(vals, ", ")+`)`, contactsEmail(t))
			}
			// Consent pair NULL and no delivery_mode: only the source CHECK can refuse this.
			_, err := c.Exec(context.Background(), `INSERT INTO contacts (email, first_name) VALUES ($1, 'Ada')`, contactsEmail(t))
			wantCheckRefusal(t, err, "contacts_has_a_source")
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

// --- readiness_checked_at: a third source, and a fact --------------------------------

type contactsQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// contactsCheckDefs maps each CHECK constraint on public.contacts to its definition.
// The table always has the email and consent CHECKs, so an empty map is a broken query.
func contactsCheckDefs(t *testing.T, q contactsQuerier) map[string]string {
	t.Helper()
	rows, err := q.Query(context.Background(),
		`SELECT conname, pg_get_constraintdef(oid) FROM pg_constraint
		  WHERE conrelid = 'public.contacts'::regclass AND contype = 'c'`)
	if err != nil {
		t.Fatalf("read contacts CHECKs: %v", err)
	}
	defer rows.Close()
	defs := map[string]string{}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			t.Fatalf("scan contacts CHECK: %v", err)
		}
		defs[name] = def
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate contacts CHECKs: %v", err)
	}
	if len(defs) == 0 {
		t.Fatal("contacts has no CHECK constraint: the query is broken")
	}
	return defs
}

func contactsHasReadinessColumn(t *testing.T, tx pgx.Tx) bool {
	t.Helper()
	var n int
	err := tx.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'contacts' AND column_name = 'readiness_checked_at'`).Scan(&n)
	if err != nil {
		t.Fatalf("read contacts columns: %v", err)
	}
	return n == 1
}

func contactsMentionsAllThree(def string) bool {
	return strings.Contains(def, "registered_at") && strings.Contains(def, "demo_requested_at") &&
		strings.Contains(def, "readiness_checked_at")
}

func TestRLS_ContactsReadinessCheckAloneIsASource(t *testing.T) {
	hh := requireHarness(t)
	contactsRequireReadinessColumn(t)
	email := contactsEmail(t)

	if _, err := hh.app.Exec(context.Background(),
		`INSERT INTO contacts (email, readiness_checked_at) VALUES ($1, $2)`, email, contactsCheckAt); err != nil {
		t.Fatalf("app INSERT of a readiness-only row: %v", err)
	}
	var reg, demo, checked *time.Time
	if err := hh.super.QueryRow(context.Background(),
		`SELECT registered_at, demo_requested_at, readiness_checked_at FROM contacts WHERE email = $1`, email,
	).Scan(&reg, &demo, &checked); err != nil {
		t.Fatalf("read the readiness-only row: %v", err)
	}
	if reg != nil || demo != nil {
		t.Fatalf("registered_at=%v demo_requested_at=%v, want both NULL", reg, demo)
	}
	if checked == nil || !checked.Equal(contactsCheckAt) {
		t.Fatalf("readiness_checked_at = %v, want %v", checked, contactsCheckAt)
	}
}

func TestRLS_ContactsSourceCheckIsNamed(t *testing.T) {
	requireHarness(t)
	defs := contactsCheckDefs(t, requireHarness(t).super)

	def, ok := defs["contacts_has_a_source"]
	if !ok {
		t.Fatalf("contacts_has_a_source is missing; contacts CHECKs are %v", defs)
	}
	if !contactsMentionsAllThree(def) {
		t.Fatalf("contacts_has_a_source is %q, want a definition that names registered_at, demo_requested_at and readiness_checked_at", def)
	}
	if old, ok := defs["contacts_check1"]; ok {
		t.Fatalf("contacts_check1 still exists: %s", old)
	}
}

func TestRLS_ContactsReadinessMigrationDownThenUp(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	contactsMigrationName(t, contactsReadinessMigrationGlob)
	contactsMigrationName(t, contactsReadinessConsentMigrationGlob)
	contactsRequireReadinessColumn(t)
	tx := migratorTx(t, ctx)
	defer contactsRollback(tx)
	sp := contactsSavepointConn{tx}

	readinessOnly, registered := contactsEmail(t), contactsEmail(t)
	contactsMustExec(t, sp, `INSERT INTO contacts (email, readiness_checked_at) VALUES ($1, $2)`, readinessOnly, contactsCheckAt)
	contactsMustExec(t, sp, `INSERT INTO contacts (email, registered_at) VALUES ($1, $2)`, registered, contactsRegAt)

	contactsExec(t, ctx, tx, contactsReadinessConsentMigrationGlob, "Down")
	contactsExec(t, ctx, tx, contactsReadinessMigrationGlob, "Down")

	if contactsHasReadinessColumn(t, tx) {
		t.Error("after Down contacts.readiness_checked_at still exists")
	}
	defs := contactsCheckDefs(t, tx)
	if got := defs["contacts_check1"]; got != contactsTwoColumnSourceDef {
		t.Errorf("after Down contacts_check1 = %q, want %q", got, contactsTwoColumnSourceDef)
	}
	if _, ok := defs["contacts_has_a_source"]; ok {
		t.Error("after Down contacts_has_a_source still exists")
	}
	if n := mustCount(t, tx, `SELECT count(*) FROM contacts WHERE email = $1`, readinessOnly); n != 0 {
		t.Errorf("after Down the readiness-only row count = %d, want 0 (the old schema cannot hold it)", n)
	}
	if n := mustCount(t, tx, `SELECT count(*) FROM contacts WHERE email = $1`, registered); n != 1 {
		t.Errorf("after Down the registered row count = %d, want 1", n)
	}
	// Sourceless input dies at the restored contacts_check1.
	_, err := sp.Exec(ctx, `INSERT INTO contacts (email, first_name) VALUES ($1, 'Ada')`, contactsEmail(t))
	wantCheckRefusal(t, err, "contacts_check1")
	// The four-clause trigger: a non-fact write passes, a registered_at rewrite is refused. A function
	// that still read OLD.readiness_checked_at would fail 42703 here instead.
	contactsMustExec(t, sp, `UPDATE contacts SET company = 'Down Co' WHERE email = $1`, registered)
	contactsRefuse(t, sp, pgRestrictViolation,
		`UPDATE contacts SET registered_at = registered_at + interval '1 day' WHERE email = $1`, registered)

	contactsExec(t, ctx, tx, contactsReadinessMigrationGlob, "Up")
	contactsExec(t, ctx, tx, contactsReadinessConsentMigrationGlob, "Up")

	if !contactsHasReadinessColumn(t, tx) {
		t.Fatal("after Up contacts.readiness_checked_at is missing")
	}
	var typ, nullable string
	if err := tx.QueryRow(ctx, `SELECT data_type, is_nullable FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'contacts' AND column_name = 'readiness_checked_at'`).Scan(&typ, &nullable); err != nil {
		t.Fatalf("read the restored column: %v", err)
	}
	if typ != "timestamp with time zone" || nullable != "YES" {
		t.Errorf("after Up readiness_checked_at is (%s, nullable=%s), want (timestamp with time zone, YES)", typ, nullable)
	}
	defs = contactsCheckDefs(t, tx)
	if def, ok := defs["contacts_has_a_source"]; !ok || !contactsMentionsAllThree(def) {
		t.Errorf("after Up contacts_has_a_source = %q (present=%v), want all three source columns", def, ok)
	}
	if _, ok := defs["contacts_check1"]; ok {
		t.Error("after Up contacts_check1 still exists")
	}
	again := contactsEmail(t)
	contactsMustExec(t, sp, `INSERT INTO contacts (email, readiness_checked_at) VALUES ($1, $2)`, again, contactsCheckAt)
	_, err = sp.Exec(ctx, `INSERT INTO contacts (email, first_name) VALUES ($1, 'Ada')`, contactsEmail(t))
	wantCheckRefusal(t, err, "contacts_has_a_source")
	contactsRefuse(t, sp, pgRestrictViolation,
		`UPDATE contacts SET readiness_checked_at = readiness_checked_at + interval '1 day' WHERE email = $1`, again)
	contactsRefuse(t, sp, pgRestrictViolation, `UPDATE contacts SET readiness_checked_at = NULL WHERE email = $1`, again)

	// The new column adds no grant: invoice_app keeps all four verbs.
	for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT has_table_privilege('invoice_app', 'public.contacts', $1)`, priv).Scan(&ok); err != nil || !ok {
			t.Errorf("invoice_app %s on contacts after Down/Up = %v (err=%v), want true", priv, ok, err)
		}
	}
}

func TestRLS_ContactsReadinessUpDropsTheOldCheckByDefinition(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	contactsMigrationName(t, contactsReadinessMigrationGlob)
	contactsMigrationName(t, contactsReadinessConsentMigrationGlob)
	contactsRequireReadinessColumn(t)
	tx := migratorTx(t, ctx)
	defer contactsRollback(tx)

	// The old CHECK under another name: Up must find it by definition.
	contactsExec(t, ctx, tx, contactsReadinessConsentMigrationGlob, "Down")
	contactsExec(t, ctx, tx, contactsReadinessMigrationGlob, "Down")
	if _, ok := contactsCheckDefs(t, tx)["contacts_check1"]; !ok {
		t.Fatal("after Down contacts_check1 is missing, so the rename below proves nothing")
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE public.contacts RENAME CONSTRAINT contacts_check1 TO contacts_legacy_source`); err != nil {
		t.Fatalf("rename contacts_check1: %v", err)
	}
	contactsExec(t, ctx, tx, contactsReadinessMigrationGlob, "Up")
	contactsExec(t, ctx, tx, contactsReadinessConsentMigrationGlob, "Up")

	defs := contactsCheckDefs(t, tx)
	for name, def := range defs {
		if def == contactsTwoColumnSourceDef {
			t.Errorf("after Up CHECK %s still has the two-column definition", name)
		}
	}
	if _, ok := defs["contacts_has_a_source"]; !ok {
		t.Errorf("after Up contacts_has_a_source is missing; CHECKs are %v", defs)
	}

	// No such CHECK at all: Up fails and names the definition it expected.
	inner, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	contactsExec(t, ctx, inner, contactsReadinessConsentMigrationGlob, "Down")
	contactsExec(t, ctx, inner, contactsReadinessMigrationGlob, "Down")
	var dropped string
	for name, def := range contactsCheckDefs(t, inner) {
		if def == contactsTwoColumnSourceDef {
			dropped = name
		}
	}
	if dropped == "" {
		t.Fatal("after Down no CHECK has the two-column definition, so the drop below proves nothing")
	}
	if _, err := inner.Exec(ctx, `ALTER TABLE public.contacts DROP CONSTRAINT `+dropped); err != nil {
		t.Fatalf("drop %s: %v", dropped, err)
	}
	_, err = inner.Exec(ctx, contactsSection(t, contactsReadinessMigrationGlob, "Up"))
	if err == nil {
		t.Fatal("Up succeeded with no two-column source CHECK, want an error")
	}
	if !strings.Contains(err.Error(), contactsTwoColumnSourceDef) {
		t.Fatalf("Up error %q does not name the expected definition %q", err, contactsTwoColumnSourceDef)
	}
	if err := inner.Rollback(ctx); err != nil {
		t.Fatalf("roll back the savepoint: %v", err)
	}

	// Two CHECKs with that definition: Up cannot tell which one is the source rule, so it refuses.
	twin, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	defer func() { _ = twin.Rollback(ctx) }()
	contactsExec(t, ctx, twin, contactsReadinessConsentMigrationGlob, "Down")
	contactsExec(t, ctx, twin, contactsReadinessMigrationGlob, "Down")
	if _, err := twin.Exec(ctx, `ALTER TABLE public.contacts ADD CONSTRAINT contacts_twin_source CHECK (registered_at IS NOT NULL OR demo_requested_at IS NOT NULL)`); err != nil {
		t.Fatalf("add the twin CHECK: %v", err)
	}
	_, err = twin.Exec(ctx, contactsSection(t, contactsReadinessMigrationGlob, "Up"))
	if err == nil {
		t.Fatal("Up succeeded with two CHECKs of the two-column definition, want an error")
	}
	if !strings.Contains(err.Error(), contactsTwoColumnSourceDef) {
		t.Fatalf("Up error %q does not name the expected definition %q", err, contactsTwoColumnSourceDef)
	}
}

// --- readiness consent: a fourth fact pair, apart from marketing_consent_* -----------

// contactsRequireReadinessConsentColumns fails by name when the migration is not applied to the live DB.
func contactsRequireReadinessConsentColumns(t *testing.T) {
	t.Helper()
	for _, col := range []string{"readiness_consent_text", "readiness_consented_at"} {
		if n := mustCount(t, requireHarness(t).super,
			`SELECT count(*) FROM information_schema.columns
			  WHERE table_schema = 'public' AND table_name = 'contacts' AND column_name = $1`, col); n != 1 {
			t.Fatalf("contacts.%s does not exist: the readiness consent migration is not applied", col)
		}
	}
}

func contactsFunctionDef(t *testing.T, tx pgx.Tx) string {
	t.Helper()
	var def string
	if err := tx.QueryRow(context.Background(),
		`SELECT pg_get_functiondef('public.contacts_facts_only_grow()'::regprocedure)`).Scan(&def); err != nil {
		t.Fatalf("read the trigger function: %v", err)
	}
	return def
}

func TestRLS_ContactsReadinessConsentNeedsTextAndTime(t *testing.T) {
	hh := requireHarness(t)
	ctx := context.Background()
	contactsRequireReadinessConsentColumns(t)
	email := contactsEmail(t)
	contactsSeed(t, email, []string{"readiness_checked_at"}, []any{contactsCheckAt})

	_, err := hh.app.Exec(ctx, `UPDATE contacts SET readiness_consent_text = 'Yes.' WHERE email = $1`, email)
	wantCheckRefusal(t, err, "contacts_readiness_consent_pair")
	_, err = hh.app.Exec(ctx, `UPDATE contacts SET readiness_consented_at = now() WHERE email = $1`, email)
	wantCheckRefusal(t, err, "contacts_readiness_consent_pair")

	if _, err := hh.app.Exec(ctx,
		`UPDATE contacts SET readiness_consent_text = 'Yes.', readiness_consented_at = now() WHERE email = $1`, email); err != nil {
		t.Fatalf("app UPDATE setting both readiness consent columns: %v", err)
	}
	var text *string
	var at *time.Time
	if err := hh.super.QueryRow(ctx,
		`SELECT readiness_consent_text, readiness_consented_at FROM contacts WHERE email = $1`, email).Scan(&text, &at); err != nil {
		t.Fatalf("read the readiness consent: %v", err)
	}
	if text == nil || *text != "Yes." || at == nil {
		t.Fatalf("readiness consent = (%v, %v), want ('Yes.', set)", text, at)
	}
}

func TestRLS_ContactsReadinessConsentCannotBeRewritten(t *testing.T) {
	hh := requireHarness(t)
	ctx := context.Background()
	contactsRequireReadinessConsentColumns(t)
	email := contactsEmail(t)
	contactsSeed(t, email,
		[]string{"readiness_checked_at", "readiness_consent_text", "readiness_consented_at"},
		[]any{contactsCheckAt, "Yes.", contactsConsentAt})

	for _, set := range []string{
		`readiness_consent_text = 'Another text.'`,
		`readiness_consent_text = NULL`,
		`readiness_consent_text = NULL, readiness_consented_at = NULL`,
		`readiness_consented_at = now()`,
	} {
		_, err := hh.app.Exec(ctx, `UPDATE contacts SET `+set+` WHERE email = $1`, email)
		wantPgCode(t, err, pgRestrictViolation)
	}

	var text *string
	var at *time.Time
	if err := hh.super.QueryRow(ctx,
		`SELECT readiness_consent_text, readiness_consented_at FROM contacts WHERE email = $1`, email).Scan(&text, &at); err != nil {
		t.Fatalf("read the readiness consent: %v", err)
	}
	if text == nil || *text != "Yes." || at == nil || !at.Equal(contactsConsentAt) {
		t.Fatalf("readiness consent = (%v, %v) after the refusals, want ('Yes.', %v)", text, at, contactsConsentAt)
	}
	// A write that leaves the pair alone still passes.
	if _, err := hh.app.Exec(ctx, `UPDATE contacts SET company = 'Co' WHERE email = $1`, email); err != nil {
		t.Fatalf("app UPDATE of a non-fact column: %v", err)
	}
}

func TestRLS_ContactsReadinessConsentMigrationDownThenUp(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	contactsMigrationName(t, contactsReadinessConsentMigrationGlob)
	contactsRequireReadinessConsentColumns(t)
	tx := migratorTx(t, ctx)
	defer contactsRollback(tx)
	sp := contactsSavepointConn{tx}

	// The readiness-consent trigger body, as that file's own Up creates it.
	ref, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	contactsExec(t, ctx, ref, contactsReadinessConsentMigrationGlob, "Down")
	contactsExec(t, ctx, ref, contactsReadinessMigrationGlob, "Down")
	contactsExec(t, ctx, ref, contactsReadinessMigrationGlob, "Up")
	wantBody := contactsFunctionDef(t, ref)
	if err := ref.Rollback(ctx); err != nil {
		t.Fatalf("roll back the savepoint: %v", err)
	}

	before := contactsReadCatalog(t, ctx, tx)
	row := contactsEmail(t)
	contactsMustExec(t, sp, `INSERT INTO contacts (email, readiness_checked_at, readiness_consent_text, readiness_consented_at)
		VALUES ($1, $2, 'Yes.', $2)`, row, contactsCheckAt)

	contactsExec(t, ctx, tx, contactsReadinessConsentMigrationGlob, "Down")

	down := contactsReadCatalog(t, ctx, tx)
	for _, col := range []string{"readiness_consent_text", "readiness_consented_at"} {
		if slices.Contains(down.columns, col) {
			t.Errorf("after Down contacts.%s still exists", col)
		}
	}
	if _, ok := contactsCheckDefs(t, tx)["contacts_readiness_consent_pair"]; ok {
		t.Error("after Down contacts_readiness_consent_pair still exists")
	}
	if got := contactsFunctionDef(t, tx); got != wantBody {
		t.Errorf("after Down the trigger function is not the FREECHK-02 body:\n%s\nwant:\n%s", got, wantBody)
	}
	if n := mustCount(t, tx, `SELECT count(*) FROM contacts WHERE email = $1`, row); n != 1 {
		t.Errorf("after Down the readiness row count = %d, want 1 (the Down drops columns, not rows)", n)
	}

	contactsExec(t, ctx, tx, contactsReadinessConsentMigrationGlob, "Up")

	after := contactsReadCatalog(t, ctx, tx)
	if !slices.Equal(after.columns, before.columns) {
		t.Errorf("columns after Down/Up = %v, want %v", after.columns, before.columns)
	}
	if _, ok := contactsCheckDefs(t, tx)["contacts_readiness_consent_pair"]; !ok {
		t.Error("after Up contacts_readiness_consent_pair is missing")
	}
	again := contactsEmail(t)
	contactsMustExec(t, sp, `INSERT INTO contacts (email, readiness_checked_at, readiness_consent_text, readiness_consented_at)
		VALUES ($1, $2, 'Yes.', $2)`, again, contactsCheckAt)
	contactsRefuse(t, sp, pgRestrictViolation, `UPDATE contacts SET readiness_consent_text = 'Another.' WHERE email = $1`, again)
	contactsRefuse(t, sp, pgRestrictViolation, `UPDATE contacts SET readiness_consented_at = NULL, readiness_consent_text = NULL WHERE email = $1`, again)
}
