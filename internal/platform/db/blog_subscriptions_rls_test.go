// The blog subscription tables are global: no tenant, no RLS. The guarantees here are grants and CHECKs.
// Rows are seeded through the superuser pool; each refused statement runs on its own implicit tx.
package db_test

import (
	"context"
	"crypto/rand"
	"errors"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	blogSubReq           = "blog_subscription_requests"
	blogSubSub           = "blog_subscribers"
	blogSubMigrationGlob = "*_blog_subscriptions.sql"
	pgUniqueViolation    = "23505"
)

var blogSubTables = []string{blogSubReq, blogSubSub}

func blogSubRequireTables(t *testing.T) {
	t.Helper()
	hh := requireHarness(t)
	for _, tbl := range blogSubTables {
		var reg *string
		if err := hh.super.QueryRow(context.Background(), `SELECT to_regclass($1)::text`, "public."+tbl).Scan(&reg); err != nil {
			t.Fatalf("read to_regclass %s: %v", tbl, err)
		}
		if reg == nil {
			t.Fatalf("table public.%s not found: the blog_subscriptions migration is not applied", tbl)
		}
	}
}

// blogSubEmail is unique per call and trimmed lower case; cleanup removes it from both tables.
func blogSubEmail(t *testing.T) string {
	t.Helper()
	hh := requireHarness(t)
	email := "t-" + uuid.NewString() + "@blogsub.test"
	t.Cleanup(func() {
		for _, tbl := range blogSubTables {
			_, _ = hh.super.Exec(context.Background(), `DELETE FROM public.`+tbl+` WHERE email = $1`, email)
		}
	})
	return email
}

func blogSubToken(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random token: %v", err)
	}
	return b
}

func blogSubCats(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "cat-" + strconv.Itoa(i)
	}
	return out
}

// blogSubInsert writes one row with valid columns except the ones passed in.
func blogSubInsert(t *testing.T, c contactsConn, table, email string, cats []string, freq string) error {
	t.Helper()
	ctx := context.Background()
	if table == blogSubReq {
		_, err := c.Exec(ctx,
			`INSERT INTO public.blog_subscription_requests (email, category_ids, frequency, token_hash, expires_at)
			 VALUES ($1, $2, $3, $4, now() + interval '48 hours')`, email, cats, freq, blogSubToken(t, 32))
		return err
	}
	_, err := c.Exec(ctx,
		`INSERT INTO public.blog_subscribers (email, category_ids, frequency, confirmed_at)
		 VALUES ($1, $2, $3, now())`, email, cats, freq)
	return err
}

func blogSubCount(t *testing.T, c contactsConn, table, email string) int {
	t.Helper()
	var n int
	if err := c.QueryRow(context.Background(), `SELECT count(*) FROM public.`+table+` WHERE email = $1`, email).Scan(&n); err != nil {
		t.Fatalf("count %s rows of %s: %v", table, email, err)
	}
	return n
}

func blogSubRefused(t *testing.T, err error, code, constraint string) {
	t.Helper()
	if err == nil {
		t.Fatalf("statement succeeded, want SQLSTATE %s on %s", code, constraint)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("want SQLSTATE %s on %s, got %v", code, constraint, err)
	}
	if pgErr.Code != code || pgErr.ConstraintName != constraint {
		t.Fatalf("want SQLSTATE %s on %s, got %s on %q: %s", code, constraint, pgErr.Code, pgErr.ConstraintName, pgErr.Message)
	}
}

// --- AC-2: grants --------------------------------------------------------------------

func TestRLS_BlogSubscriptionsAppGrants(t *testing.T) {
	blogSubRequireTables(t)
	hh := requireHarness(t)
	ctx := context.Background()

	reqEmail := blogSubEmail(t)
	if err := blogSubInsert(t, hh.app, blogSubReq, reqEmail, []string{"a", "b"}, "weekly"); err != nil {
		t.Fatalf("app INSERT request: %v", err)
	}
	var freq string
	var cats []string
	if err := hh.app.QueryRow(ctx,
		`SELECT frequency, category_ids FROM public.blog_subscription_requests WHERE email = $1`, reqEmail).Scan(&freq, &cats); err != nil {
		t.Fatalf("app SELECT request: %v", err)
	}
	if freq != "weekly" || !slices.Equal(cats, []string{"a", "b"}) {
		t.Fatalf("app read request (%q, %v), want (weekly, [a b])", freq, cats)
	}
	tag, err := hh.app.Exec(ctx, `UPDATE public.blog_subscription_requests SET frequency = 'monthly' WHERE email = $1`, reqEmail)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("app UPDATE request: rows=%d err=%v, want 1 row", tag.RowsAffected(), err)
	}
	tag, err = hh.app.Exec(ctx, `DELETE FROM public.blog_subscription_requests WHERE email = $1`, reqEmail)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("app DELETE request: rows=%d err=%v, want 1 row", tag.RowsAffected(), err)
	}

	subEmail := blogSubEmail(t)
	if err := blogSubInsert(t, hh.app, blogSubSub, subEmail, []string{"a"}, "every_post"); err != nil {
		t.Fatalf("app INSERT subscriber: %v", err)
	}
	var rowRev int64
	if err := hh.app.QueryRow(ctx,
		`SELECT frequency, version FROM public.blog_subscribers WHERE email = $1`, subEmail).Scan(&freq, &rowRev); err != nil {
		t.Fatalf("app SELECT subscriber: %v", err)
	}
	if freq != "every_post" || rowRev != 1 {
		t.Fatalf("app read subscriber (%q, v%d), want (every_post, v1)", freq, rowRev)
	}
	tag, err = hh.app.Exec(ctx, `UPDATE public.blog_subscribers SET frequency = 'monthly', version = version + 1 WHERE email = $1`, subEmail)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("app UPDATE subscriber: rows=%d err=%v, want 1 row", tag.RowsAffected(), err)
	}
}

func TestRLS_BlogSubscribersAppMayNotDelete(t *testing.T) {
	blogSubRequireTables(t)
	hh := requireHarness(t)
	ctx := context.Background()

	email := blogSubEmail(t)
	if _, err := hh.super.Exec(ctx,
		`INSERT INTO public.blog_subscribers (email, category_ids, frequency, confirmed_at)
		 VALUES ($1, '{a}', 'weekly', now())`, email); err != nil {
		t.Fatalf("seed subscriber: %v", err)
	}
	if n := blogSubCount(t, hh.app, blogSubSub, email); n != 1 {
		t.Fatalf("app sees %d seeded subscribers, want 1", n)
	}

	_, err := hh.app.Exec(ctx, `DELETE FROM public.blog_subscribers WHERE email = $1`, email)
	if pgCode(err) != pgInsufficientPriv {
		t.Fatalf("app DELETE subscriber: got %v, want SQLSTATE %s (no DELETE grant)", err, pgInsufficientPriv)
	}
	if n := blogSubCount(t, hh.super, blogSubSub, email); n != 1 {
		t.Fatalf("subscriber rows after refused DELETE = %d, want 1", n)
	}
}

func TestRLS_BlogSubscriptionsReaderMayNotSelect(t *testing.T) {
	blogSubRequireTables(t)
	hh := requireHarness(t)

	for _, tbl := range blogSubTables {
		t.Run(tbl, func(t *testing.T) {
			var n int
			err := hh.reader.QueryRow(context.Background(), `SELECT count(*) FROM public.`+tbl).Scan(&n)
			if pgCode(err) != pgInsufficientPriv {
				t.Fatalf("reader SELECT %s: got %v, want SQLSTATE %s", tbl, err, pgInsufficientPriv)
			}
		})
	}
}

// --- AC-2: CHECKs and keys -----------------------------------------------------------
// Each case runs on a conn: the live app pool, or a savepoint conn over the replayed migration.

func blogSubEmailCase(t *testing.T, c contactsConn) {
	t.Helper()
	for _, tbl := range blogSubTables {
		id := uuid.NewString()
		for _, bad := range map[string]string{
			"leading space and capitals": " A-" + id + "@b.co",
			"capitals only":              "A-" + id + "@b.co",
		} {
			err := blogSubInsert(t, c, tbl, bad, []string{"a"}, "weekly")
			blogSubRefused(t, err, pgCheckViolation, tbl+"_email_check")
		}
		ok := blogSubEmail(t)
		if err := blogSubInsert(t, c, tbl, ok, []string{"a"}, "weekly"); err != nil {
			t.Fatalf("%s: insert of a trimmed lower-case address: %v", tbl, err)
		}
		if n := blogSubCount(t, c, tbl, ok); n != 1 {
			t.Fatalf("%s: accepted address stored %d rows, want 1", tbl, n)
		}
	}
}

func blogSubFrequencyCase(t *testing.T, c contactsConn) {
	t.Helper()
	for _, tbl := range blogSubTables {
		for _, bad := range []string{"each", "daily", ""} {
			err := blogSubInsert(t, c, tbl, blogSubEmail(t), []string{"a"}, bad)
			blogSubRefused(t, err, pgCheckViolation, tbl+"_frequency_check")
		}
		for _, good := range []string{"weekly", "every_post", "monthly"} {
			email := blogSubEmail(t)
			if err := blogSubInsert(t, c, tbl, email, []string{"a"}, good); err != nil {
				t.Fatalf("%s: insert of frequency %q: %v", tbl, good, err)
			}
			if n := blogSubCount(t, c, tbl, email); n != 1 {
				t.Fatalf("%s: frequency %q stored %d rows, want 1", tbl, good, n)
			}
		}
	}
}

func blogSubCategoriesCase(t *testing.T, c contactsConn) {
	t.Helper()
	for _, tbl := range blogSubTables {
		for _, bad := range [][]string{{}, blogSubCats(51)} {
			err := blogSubInsert(t, c, tbl, blogSubEmail(t), bad, "weekly")
			blogSubRefused(t, err, pgCheckViolation, tbl+"_category_ids_check")
		}
		for name, good := range map[string][]string{"1 id": blogSubCats(1), "50 ids": blogSubCats(50)} {
			email := blogSubEmail(t)
			if err := blogSubInsert(t, c, tbl, email, good, "weekly"); err != nil {
				t.Fatalf("%s: insert of %s: %v", tbl, name, err)
			}
			if n := blogSubCount(t, c, tbl, email); n != 1 {
				t.Fatalf("%s: %s stored %d rows, want 1", tbl, name, n)
			}
		}
	}
}

func blogSubTokenCase(t *testing.T, c contactsConn) {
	t.Helper()
	ctx := context.Background()
	const insert = `INSERT INTO public.blog_subscription_requests (email, category_ids, frequency, token_hash, expires_at)
		VALUES ($1, '{a}', 'weekly', $2, now() + interval '48 hours')`

	for _, n := range []int{31, 33} {
		email := blogSubEmail(t)
		_, err := c.Exec(ctx, insert, email, blogSubToken(t, n))
		blogSubRefused(t, err, pgCheckViolation, "blog_subscription_requests_token_hash_check")
	}

	hash := blogSubToken(t, 32)
	first := blogSubEmail(t)
	if _, err := c.Exec(ctx, insert, first, hash); err != nil {
		t.Fatalf("insert of a 32-byte token hash: %v", err)
	}
	if n := blogSubCount(t, c, blogSubReq, first); n != 1 {
		t.Fatalf("32-byte token stored %d rows, want 1", n)
	}
	second := blogSubEmail(t)
	_, err := c.Exec(ctx, insert, second, hash)
	blogSubRefused(t, err, pgUniqueViolation, "blog_subscription_requests_token_hash_key")
	if n := blogSubCount(t, c, blogSubReq, second); n != 0 {
		t.Fatalf("refused duplicate token left %d rows", n)
	}
}

func blogSubOnePerEmailCase(t *testing.T, c contactsConn) {
	t.Helper()
	email := blogSubEmail(t)
	if err := blogSubInsert(t, c, blogSubReq, email, []string{"a"}, "weekly"); err != nil {
		t.Fatalf("first request for %s: %v", email, err)
	}
	err := blogSubInsert(t, c, blogSubReq, email, []string{"b"}, "monthly")
	blogSubRefused(t, err, pgUniqueViolation, "blog_subscription_requests_pkey")
	if n := blogSubCount(t, c, blogSubReq, email); n != 1 {
		t.Fatalf("requests for one email = %d, want 1", n)
	}
}

func TestRLS_BlogSubscriptionsEmailIsTrimmedLowerCase(t *testing.T) {
	blogSubRequireTables(t)
	blogSubEmailCase(t, requireHarness(t).app)
}

func TestRLS_BlogSubscriptionsFrequencyIsOneOfThree(t *testing.T) {
	blogSubRequireTables(t)
	blogSubFrequencyCase(t, requireHarness(t).app)
}

func TestRLS_BlogSubscriptionsCategoriesOneToFifty(t *testing.T) {
	blogSubRequireTables(t)
	blogSubCategoriesCase(t, requireHarness(t).app)
}

func TestRLS_BlogSubscriptionRequestTokenHashIs32BytesAndUnique(t *testing.T) {
	blogSubRequireTables(t)
	blogSubTokenCase(t, requireHarness(t).app)
}

func TestRLS_BlogSubscriptionRequestIsOnePerEmail(t *testing.T) {
	blogSubRequireTables(t)
	blogSubOnePerEmailCase(t, requireHarness(t).app)
}

// --- AC-2: Down and Up ---------------------------------------------------------------

func blogSubSection(t *testing.T, section string) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, blogSubMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", blogSubMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d files matching %s (%v), want exactly 1", len(matches), blogSubMigrationGlob, matches)
	}
	b, err := fs.ReadFile(migrations.FS, matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	raw := string(b)
	up, ok := auditEntityUpOf(raw)
	if !ok {
		t.Fatalf("%s: want both %q and %q, in that order", matches[0], gooseUp, gooseDown)
	}
	if section == "Up" {
		return up
	}
	return raw[strings.Index(raw, gooseDown)+len(gooseDown):]
}

func blogSubExec(t *testing.T, ctx context.Context, tx pgx.Tx, section string) {
	t.Helper()
	if _, err := tx.Exec(ctx, blogSubSection(t, section)); err != nil {
		t.Fatalf("%s body failed: %v", section, err)
	}
}

func blogSubPublicTables(t *testing.T, ctx context.Context, tx pgx.Tx) []string {
	t.Helper()
	rows, err := tx.Query(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE' ORDER BY table_name`)
	if err != nil {
		t.Fatalf("query public tables: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan public tables: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate public tables: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("public table read came back empty: the query is broken")
	}
	return out
}

func blogSubGrantsCase(t *testing.T, c contactsConn) {
	t.Helper()
	for _, g := range []struct {
		role, table, priv string
		want              bool
	}{
		{"invoice_app", blogSubReq, "SELECT", true}, {"invoice_app", blogSubReq, "INSERT", true},
		{"invoice_app", blogSubReq, "UPDATE", true}, {"invoice_app", blogSubReq, "DELETE", true},
		{"invoice_app", blogSubReq, "TRUNCATE", false},
		{"invoice_app", blogSubSub, "SELECT", true}, {"invoice_app", blogSubSub, "INSERT", true},
		{"invoice_app", blogSubSub, "UPDATE", true}, {"invoice_app", blogSubSub, "DELETE", false},
		{"invoice_app", blogSubSub, "TRUNCATE", false},
		{"invoice_tenant_reader", blogSubReq, "SELECT", false}, {"invoice_tenant_reader", blogSubSub, "SELECT", false},
		{"invoice_tenant_reader", blogSubReq, "INSERT", false}, {"invoice_tenant_reader", blogSubSub, "INSERT", false},
	} {
		var got bool
		err := c.QueryRow(context.Background(),
			`SELECT has_table_privilege($1, $2, $3)`, g.role, "public."+g.table, g.priv).Scan(&got)
		if err != nil || got != g.want {
			t.Errorf("%s %s on %s = %v (err=%v), want %v", g.role, g.priv, g.table, got, err, g.want)
		}
	}
}

func TestRLS_BlogSubscriptionsMigrationDownDropsBothTables(t *testing.T) {
	blogSubRequireTables(t)
	ctx := context.Background()
	tx := migratorTx(t, ctx)

	before := blogSubPublicTables(t, ctx, tx)
	blogSubExec(t, ctx, tx, "Down")
	after := blogSubPublicTables(t, ctx, tx)

	for _, tbl := range blogSubTables {
		if slices.Contains(after, tbl) {
			t.Errorf("after Down public.%s still exists", tbl)
		}
	}
	want := slices.DeleteFunc(slices.Clone(before), func(s string) bool { return slices.Contains(blogSubTables, s) })
	if !slices.Equal(after, want) {
		t.Errorf("public tables after Down = %v, want %v", after, want)
	}
}

func TestRLS_BlogSubscriptionsMigrationUpRestoresWhatDownDropped(t *testing.T) {
	blogSubRequireTables(t)
	ctx := context.Background()
	tx := migratorTx(t, ctx)
	// Roll back before the row cleanups run: their DELETE would block on the tx's table locks.
	defer func() { _ = tx.Rollback(ctx) }()

	before := blogSubPublicTables(t, ctx, tx)
	blogSubExec(t, ctx, tx, "Down")
	if mid := blogSubPublicTables(t, ctx, tx); slices.Contains(mid, blogSubReq) || slices.Contains(mid, blogSubSub) {
		t.Fatal("the Down left a table in place: this case cannot tell 'Up restored it' from 'Down did nothing'")
	}
	blogSubExec(t, ctx, tx, "Up")

	if after := blogSubPublicTables(t, ctx, tx); !slices.Equal(after, before) {
		t.Fatalf("public tables after Down/Up = %v, want %v", after, before)
	}
	blogSubGrantsCase(t, tx)

	sp := contactsSavepointConn{tx}
	blogSubEmailCase(t, sp)
	blogSubFrequencyCase(t, sp)
	blogSubCategoriesCase(t, sp)
	blogSubTokenCase(t, sp)
	blogSubOnePerEmailCase(t, sp)
}
