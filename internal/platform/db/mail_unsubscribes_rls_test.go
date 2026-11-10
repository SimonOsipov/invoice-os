// `mail_unsubscribes` holds one random unsubscribe token per address. It is global (no tenant, no RLS),
// so the guarantees are the function that mints tokens, the grants, the trigger and the CHECK.
package db_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	muMigrationGlob = "*_mail_unsubscribes.sql"
	muTokenFn       = "public.mail_unsubscribe_token(text)"
	muCheck         = "mail_unsubscribes_email_check"
	pgRestrict      = "23001"
	pgLockNotAvail  = "55P03"
)

// muRequireTable fails by name when the migration is not applied to the live DB.
func muRequireTable(t *testing.T) {
	t.Helper()
	var table, fn bool
	if err := requireHarness(t).super.QueryRow(context.Background(),
		`SELECT to_regclass('public.mail_unsubscribes') IS NOT NULL, to_regprocedure($1) IS NOT NULL`, muTokenFn).Scan(&table, &fn); err != nil {
		t.Fatalf("read table and function presence: %v", err)
	}
	if !table {
		t.Fatal("public.mail_unsubscribes does not exist: the mail_unsubscribes migration is not applied")
	}
	if !fn {
		t.Fatalf("%s does not exist: the mail_unsubscribes migration is not applied", muTokenFn)
	}
}

func muEmail(t *testing.T) string {
	t.Helper()
	email := "mu-" + uuid.NewString() + "@unsub.test"
	t.Cleanup(func() {
		_, _ = requireHarness(t).super.Exec(context.Background(), `DELETE FROM mail_unsubscribes WHERE email = $1`, email)
	})
	return email
}

func muRowCount(t *testing.T, email string) int {
	t.Helper()
	return mustCount(t, requireHarness(t).super, `SELECT count(*) FROM mail_unsubscribes WHERE email = $1`, email)
}

// muAppToken calls the token function as invoice_app.
func muAppToken(t *testing.T, email string) string {
	t.Helper()
	var token string
	if err := requireHarness(t).app.QueryRow(context.Background(),
		`SELECT public.mail_unsubscribe_token($1)::text`, email).Scan(&token); err != nil {
		t.Fatalf("mail_unsubscribe_token(%q) as invoice_app: %v", email, err)
	}
	return token
}

func muSuperInsert(email string) error {
	_, err := h.super.Exec(context.Background(), `INSERT INTO mail_unsubscribes (email) VALUES ($1)`, email)
	return err
}

func muSeed(t *testing.T) string {
	t.Helper()
	email := muEmail(t)
	if err := muSuperInsert(email); err != nil {
		t.Fatalf("seed a mail_unsubscribes row: %v", err)
	}
	return email
}

func muExecutes(t *testing.T, role string) bool {
	t.Helper()
	var ok bool
	if err := h.super.QueryRow(context.Background(),
		`SELECT has_function_privilege($1, $2::regprocedure, 'EXECUTE')`, role, muTokenFn).Scan(&ok); err != nil {
		t.Fatalf("has_function_privilege(%s): %v", role, err)
	}
	return ok
}

func muPublicExecutes(t *testing.T) bool {
	t.Helper()
	var n int
	if err := h.super.QueryRow(context.Background(), `
		SELECT count(*) FROM pg_proc p, aclexplode(coalesce(p.proacl, acldefault('f', p.proowner))) a
		 WHERE p.oid = $1::regprocedure AND a.grantee = 0 AND a.privilege_type = 'EXECUTE'`, muTokenFn).Scan(&n); err != nil {
		t.Fatalf("read the PUBLIC ACL of %s: %v", muTokenFn, err)
	}
	return n > 0
}

func TestRLS_MailUnsubscribesTokenIsGetOrCreateAndNormalised(t *testing.T) {
	muRequireTable(t)
	base := strings.TrimSuffix(muEmail(t), "@unsub.test")
	want := base + "@x.test"

	first := muAppToken(t, " "+strings.ToUpper(base[:1])+base[1:]+"@X.test ")
	second := muAppToken(t, strings.ToUpper(base)+"@x.test")

	if n := muRowCount(t, want); n != 1 {
		t.Errorf("rows for %q after two calls = %d, want 1", want, n)
	}
	var stored, token string
	if err := h.super.QueryRow(context.Background(),
		`SELECT email, token::text FROM mail_unsubscribes WHERE email = $1`, want).Scan(&stored, &token); err != nil {
		t.Fatalf("read the row for %q: %v", want, err)
	}
	if stored != want {
		t.Errorf("stored email = %q, want the trimmed lower-case %q", stored, want)
	}
	if first != second {
		t.Errorf("tokens for the same address = %q and %q, want equal", first, second)
	}
	if first != token {
		t.Errorf("returned token = %q, stored token = %q, want equal", first, token)
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Errorf("token %q is not a UUID: %v", first, err)
	}
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM mail_unsubscribes WHERE email = $1`, want)
	})
}

func TestRLS_MailUnsubscribesTwoAddressesGetTwoTokens(t *testing.T) {
	muRequireTable(t)
	a, b := muAppToken(t, muEmail(t)), muAppToken(t, muEmail(t))
	for _, tok := range []string{a, b} {
		id, err := uuid.Parse(tok)
		if err != nil {
			t.Fatalf("token %q is not a UUID: %v", tok, err)
		}
		if id.Version() != 4 {
			t.Errorf("token %q is UUID version %d, want a random version 4", tok, id.Version())
		}
	}
	if a == b {
		t.Errorf("two addresses got the same token %q", a)
	}
}

// A refused address makes no row, so a caller's lookup error never leaves a token for a bad address.
func TestRLS_MailUnsubscribesTokenFunctionRefusesABadAddress(t *testing.T) {
	muRequireTable(t)
	ctx := context.Background()
	long := strings.Repeat("a", 255-len("@x.test")) + "@x.test"
	for name, arg := range map[string]any{"NULL": nil, "empty": "", "blank": "   ", "two bytes": "ab", "255 bytes": long} {
		var token *string
		err := h.app.QueryRow(ctx, `SELECT public.mail_unsubscribe_token($1)::text`, arg).Scan(&token)
		if err == nil {
			t.Errorf("%s: mail_unsubscribe_token returned %v, want a refusal", name, token)
		}
	}
	if after := mustCount(t, h.super, `SELECT count(*) FROM mail_unsubscribes WHERE email = ANY($1)`, []string{"", "ab", long}); after != 0 {
		t.Errorf("refused addresses left %d row(s), want 0", after)
	}
}

func TestRLS_MailUnsubscribesGrantsAreExact(t *testing.T) {
	muRequireTable(t)
	ctx := context.Background()

	if got, want := aeVerbs(t, "invoice_app", "mail_unsubscribes"), []string{"DELETE", "SELECT", "UPDATE"}; !reflect.DeepEqual(got, want) {
		t.Errorf("invoice_app privileges on mail_unsubscribes = %v, want exactly %v", got, want)
	}
	if got, want := aeNonOwnerACL(t, "mail_unsubscribes"), []string{"invoice_app=DELETE", "invoice_app=SELECT"}; !reflect.DeepEqual(got, want) {
		t.Errorf("table ACL of mail_unsubscribes = %v, want exactly %v (the UPDATE is a column grant)", got, want)
	}
	var tableUpdate bool
	if err := h.super.QueryRow(ctx, `SELECT has_table_privilege('invoice_app', 'public.mail_unsubscribes', 'UPDATE')`).Scan(&tableUpdate); err != nil {
		t.Fatalf("has_table_privilege: %v", err)
	}
	if tableUpdate {
		t.Error("invoice_app holds table-level UPDATE on mail_unsubscribes, want a column grant on unsubscribed_at only")
	}
	for _, c := range []struct {
		column string
		update bool
	}{{"email", false}, {"token", false}, {"unsubscribed_at", true}, {"created_at", false}} {
		for _, verb := range []string{"UPDATE", "INSERT"} {
			var got bool
			if err := h.super.QueryRow(ctx,
				`SELECT has_column_privilege('invoice_app', 'public.mail_unsubscribes', $1, $2)`, c.column, verb).Scan(&got); err != nil {
				t.Fatalf("has_column_privilege(%s, %s): %v", c.column, verb, err)
			}
			if want := verb == "UPDATE" && c.update; got != want {
				t.Errorf("invoice_app %s on mail_unsubscribes.%s = %v, want %v", verb, c.column, got, want)
			}
		}
	}

	for _, role := range []string{"invoice_gateway", "invoice_tenant_reader", "supabase_auth_admin", "auth_hook_reader"} {
		if got := aeVerbs(t, role, "mail_unsubscribes"); len(got) != 0 {
			t.Errorf("%s privileges on mail_unsubscribes = %v, want none", role, got)
		}
	}

	for _, role := range []string{"invoice_app", "invoice_gateway"} {
		if !muExecutes(t, role) {
			t.Errorf("%s lacks EXECUTE on %s", role, muTokenFn)
		}
	}
	for _, role := range []string{"invoice_tenant_reader", "supabase_auth_admin", "auth_hook_reader"} {
		if muExecutes(t, role) {
			t.Errorf("%s holds EXECUTE on %s, want none", role, muTokenFn)
		}
	}
	if muPublicExecutes(t) {
		t.Errorf("PUBLIC holds EXECUTE on %s, want it revoked", muTokenFn)
	}
}

func TestRLS_MailUnsubscribesGatewayCannotReadTheTable(t *testing.T) {
	muRequireTable(t)

	err := aeAsGateway(t, false, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT * FROM mail_unsubscribes`)
		return err
	})
	assertPgRefusal(t, "SELECT as invoice_gateway", err, pgInsufficientPriv, "permission denied for table mail_unsubscribes")

	email := muEmail(t)
	var token string
	if err := aeAsGateway(t, false, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT public.mail_unsubscribe_token($1)::text`, email).Scan(&token)
	}); err != nil {
		t.Fatalf("mail_unsubscribe_token as invoice_gateway: %v", err)
	}
	if _, err := uuid.Parse(token); err != nil {
		t.Errorf("token %q from the gateway call is not a UUID: %v", token, err)
	}
}

// The token UPDATE and the reset run as the superuser, which bypasses grants, so only the trigger can refuse them.
func TestRLS_MailUnsubscribesASetUnsubscribeNeverChanges(t *testing.T) {
	muRequireTable(t)
	ctx := context.Background()
	email := muSeed(t)

	var token string
	var first *time.Time
	read := func() {
		t.Helper()
		if err := h.super.QueryRow(ctx,
			`SELECT token::text, unsubscribed_at FROM mail_unsubscribes WHERE email = $1`, email).Scan(&token, &first); err != nil {
			t.Fatalf("read the row: %v", err)
		}
	}

	tag, err := h.app.Exec(ctx, `UPDATE mail_unsubscribes SET unsubscribed_at = now() WHERE email = $1`, email)
	if err != nil {
		t.Fatalf("set a NULL unsubscribed_at as invoice_app: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("setting unsubscribed_at affected %d rows, want 1", tag.RowsAffected())
	}
	read()
	if first == nil {
		t.Fatal("unsubscribed_at is still NULL after the set")
	}
	wasToken, wasAt := token, *first

	_, err = h.app.Exec(ctx, `UPDATE mail_unsubscribes SET unsubscribed_at = now() + interval '1 day' WHERE email = $1`, email)
	assertPgRefusal(t, "move a set unsubscribed_at as invoice_app", err, pgRestrict, "")
	_, err = h.super.Exec(ctx, `UPDATE mail_unsubscribes SET unsubscribed_at = NULL WHERE email = $1`, email)
	assertPgRefusal(t, "clear a set unsubscribed_at as superuser", err, pgRestrict, "")
	_, err = h.super.Exec(ctx, `UPDATE mail_unsubscribes SET token = gen_random_uuid() WHERE email = $1`, email)
	assertPgRefusal(t, "change token of a set row as superuser", err, pgRestrict, "")

	read()
	if token != wasToken || first == nil || !first.Equal(wasAt) {
		t.Errorf("row after the refused updates = (%s, %v), want it kept at (%s, %v)", token, first, wasToken, wasAt)
	}

	unset := muSeed(t)
	var unsetToken string
	if err := h.super.QueryRow(ctx, `SELECT token::text FROM mail_unsubscribes WHERE email = $1`, unset).Scan(&unsetToken); err != nil {
		t.Fatalf("read the unset row: %v", err)
	}
	_, err = h.super.Exec(ctx, `UPDATE mail_unsubscribes SET token = gen_random_uuid() WHERE email = $1`, unset)
	assertPgRefusal(t, "change token of an unset row as superuser", err, pgRestrict, "")
	_, err = h.app.Exec(ctx, `UPDATE mail_unsubscribes SET token = gen_random_uuid() WHERE email = $1`, unset)
	assertPgRefusal(t, "change token as invoice_app", err, pgInsufficientPriv, "permission denied for table mail_unsubscribes")
	var after string
	if err := h.super.QueryRow(ctx, `SELECT token::text FROM mail_unsubscribes WHERE email = $1`, unset).Scan(&after); err != nil || after != unsetToken {
		t.Errorf("token of the unset row after the refusals = %q (err=%v), want it kept at %q", after, err, unsetToken)
	}
}

func TestRLS_MailUnsubscribesRefusesAnUnnormalisedAddress(t *testing.T) {
	muRequireTable(t)
	good := muEmail(t)
	if err := muSuperInsert(good); err != nil {
		t.Fatalf("a trimmed lower-case address is refused: %v", err)
	}
	max := strings.Repeat("a", 254-len("@x.test")) + "@x.test"
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM mail_unsubscribes WHERE email = $1`, max)
	})
	if err := muSuperInsert(max); err != nil {
		t.Fatalf("a %d-byte address is refused: %v", len(max), err)
	}

	for _, bad := range []string{
		"Sam@x.test",
		" sam@x.test",
		"sam@x.test ",
		strings.Repeat("a", 255-len("@x.test")) + "@x.test",
		strings.Repeat("é", 128) + "@x.test",
	} {
		wantCheckRefusal(t, muSuperInsert(bad), muCheck)
		if n := muRowCount(t, bad); n != 0 {
			t.Errorf("rows for %.20q after the refusal = %d, want 0", bad, n)
		}
	}
}

func TestRLS_MailUnsubscribesDownThenUpRoundTrips(t *testing.T) {
	muRequireTable(t)
	ctx := context.Background()
	version := migrationVersion(t, muMigrationGlob)
	provider := staffMigrationProvider(t)
	restoreMigrationOnCleanup(t, provider, version)

	present := func() (table, fn bool) {
		t.Helper()
		if err := h.super.QueryRow(ctx,
			`SELECT to_regclass('public.mail_unsubscribes') IS NOT NULL, to_regprocedure($1) IS NOT NULL`, muTokenFn).Scan(&table, &fn); err != nil {
			t.Fatalf("read presence: %v", err)
		}
		return table, fn
	}

	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("Down of %d: %v", version, err)
	}
	if table, fn := present(); table || fn {
		t.Errorf("after the Down: table present = %v, function present = %v, want both gone", table, fn)
	}

	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("Up of %d: %v", version, err)
	}
	if table, fn := present(); !table || !fn {
		t.Fatalf("after the Up: table present = %v, function present = %v, want both back", table, fn)
	}
	if got, want := aeVerbs(t, "invoice_app", "mail_unsubscribes"), []string{"DELETE", "SELECT", "UPDATE"}; !reflect.DeepEqual(got, want) {
		t.Errorf("invoice_app privileges after the Up = %v, want %v", got, want)
	}
	if !muExecutes(t, "invoice_app") || muPublicExecutes(t) {
		t.Errorf("function grants after the Up: invoice_app EXECUTE = %v, PUBLIC EXECUTE = %v, want true and false", muExecutes(t, "invoice_app"), muPublicExecutes(t))
	}
	email := muEmail(t)
	if a, b := muAppToken(t, email), muAppToken(t, email); a != b {
		t.Errorf("tokens after the Up = %q and %q, want equal", a, b)
	}
}

func TestRLS_MailUnsubscribesAppCanLockARow(t *testing.T) {
	muRequireTable(t)
	ctx := context.Background()
	email := muEmail(t)
	token := muAppToken(t, email)
	const lock = `SELECT email FROM mail_unsubscribes WHERE token = $1 FOR UPDATE`

	holder, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	var got string
	if err := holder.QueryRow(ctx, lock, token).Scan(&got); err != nil {
		t.Fatalf("SELECT … FOR UPDATE as invoice_app: %v", err)
	}
	if got != email {
		t.Errorf("locked row email = %q, want %q", got, email)
	}

	waiter, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = waiter.Rollback(ctx) }()
	_, err = waiter.Exec(ctx, `SELECT email FROM mail_unsubscribes WHERE token = $1 FOR UPDATE NOWAIT`, token)
	assertPgRefusal(t, "FOR UPDATE NOWAIT while the row is locked", err, pgLockNotAvail, "could not obtain lock")
	_ = waiter.Rollback(ctx)

	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("release the lock: %v", err)
	}
	free, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = free.Rollback(ctx) }()
	if _, err := free.Exec(ctx, `SELECT email FROM mail_unsubscribes WHERE token = $1 FOR UPDATE NOWAIT`, token); err != nil {
		t.Errorf("FOR UPDATE NOWAIT after the lock was released: %v", err)
	}
}

// Two rows holding one token would make a link unsubscribe the wrong address.
func TestRLS_MailUnsubscribesTokenIsUnique(t *testing.T) {
	muRequireTable(t)
	ctx := context.Background()
	a, b := muEmail(t), muEmail(t)
	token := muAppToken(t, a)
	if muRowCount(t, a) != 1 {
		t.Fatalf("the token call for %q left no row", a)
	}
	_, err := h.super.Exec(ctx, `INSERT INTO mail_unsubscribes (email, token) VALUES ($1, $2)`, b, token)
	assertPgRefusal(t, "INSERT of a second row with the same token", err, pgUniqueViolation, "mail_unsubscribes_token_key")
	if n := muRowCount(t, b); n != 0 {
		t.Errorf("the refused insert left %d row(s)", n)
	}
}

// A definer function with a writable search_path lets a caller shadow now(), lower() or the table.
func TestRLS_MailUnsubscribesTokenFunctionIsAPinnedDefiner(t *testing.T) {
	muRequireTable(t)
	var definer bool
	var config []string
	if err := h.super.QueryRow(context.Background(),
		`SELECT prosecdef, coalesce(proconfig, '{}') FROM pg_proc WHERE oid = $1::regprocedure`, muTokenFn).Scan(&definer, &config); err != nil {
		t.Fatalf("read pg_proc: %v", err)
	}
	if !definer {
		t.Errorf("%s is not SECURITY DEFINER: the gateway has no table privilege and could not call it", muTokenFn)
	}
	if !reflect.DeepEqual(config, []string{"search_path=\"\""}) {
		t.Errorf("%s proconfig = %v, want exactly [search_path=\"\"]", muTokenFn, config)
	}
}

// A read of a known address must write no tuple and wait on no row lock: it runs for every mail and
// every backfilled account, and a press holds the row FOR UPDATE.
func TestRLS_MailUnsubscribesTokenReadOfAKnownAddressWritesAndWaitsOnNothing(t *testing.T) {
	muRequireTable(t)
	ctx := context.Background()
	email := muEmail(t)
	token := muAppToken(t, email)
	xmin := func() string {
		var x string
		if err := h.super.QueryRow(ctx, `SELECT xmin::text FROM mail_unsubscribes WHERE email = $1`, email).Scan(&x); err != nil {
			t.Fatalf("read xmin: %v", err)
		}
		return x
	}
	before := xmin()

	holder, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `SELECT email FROM mail_unsubscribes WHERE token = $1 FOR UPDATE`, token); err != nil {
		t.Fatalf("lock the row: %v", err)
	}

	reader, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = reader.Rollback(ctx) }()
	if _, err := reader.Exec(ctx, `SET LOCAL lock_timeout = '1s'`); err != nil {
		t.Fatalf("set lock_timeout: %v", err)
	}
	var got string
	if err := reader.QueryRow(ctx, `SELECT public.mail_unsubscribe_token($1)::text`, email).Scan(&got); err != nil {
		t.Fatalf("token read while the row is locked: %v", err)
	}
	if got != token {
		t.Errorf("token = %s, want the existing %s", got, token)
	}
	if after := xmin(); after != before {
		t.Errorf("xmin = %s after the read, want %s: the read wrote a tuple", after, before)
	}
}
