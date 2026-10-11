package db_test

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"
)

const (
	pendingForEmailSig        = "public.invitation_pending_for_email(text)"
	registrationMigrationGlob = "*_invitation_registration.sql"
)

// resetRegistrationMigration drops the migration's objects, restores the table's RLS flags and applies
// the embedded Up again, so a test reads the shipped file. It skips the Down: a Down or Up left broken
// by an earlier run cannot block it.
func resetRegistrationMigration(t *testing.T, provider *goose.Provider, version int64) {
	t.Helper()
	if err := resetRegistrationMigrationErr(provider, version); err != nil {
		t.Fatal(err)
	}
}

func resetRegistrationMigrationErr(provider *goose.Provider, version int64) error {
	ctx := context.Background()
	for _, stmt := range []string{
		`DROP FUNCTION IF EXISTS public.invitation_pending_for_email(text)`,
		`ALTER TABLE public.invitations DROP COLUMN IF EXISTS registered_token_hash CASCADE`,
		`ALTER TABLE public.invitations ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.invitations FORCE ROW LEVEL SECURITY`,
	} {
		if _, err := h.super.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("scrub %q: %w", stmt, err)
		}
	}
	if _, err := h.super.Exec(ctx, `DELETE FROM goose_db_version WHERE version_id = $1`, version); err != nil {
		return fmt.Errorf("forget the registration migration: %w", err)
	}
	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		return fmt.Errorf("apply the registration migration: %w", err)
	}
	return nil
}

func reapplyRegistrationMigration(t *testing.T) {
	t.Helper()
	resetRegistrationMigration(t, staffMigrationProvider(t), migrationVersion(t, registrationMigrationGlob))
}

// uniqueAddress keeps the any-tenant lookup from matching rows other tests leave behind.
func uniqueAddress(local string) string {
	return local + "-" + uuid.NewString()[:8] + "@firm.test"
}

func registeredHash(t *testing.T, id string) []byte {
	t.Helper()
	var got []byte
	if err := h.super.QueryRow(context.Background(), `SELECT registered_token_hash FROM invitations WHERE id = $1`, id).Scan(&got); err != nil {
		t.Fatalf("read registered_token_hash: %v", err)
	}
	return got
}

// invitationPendingFor calls the lookup as invoice_app; an empty guc leaves app.current_tenant unset.
func invitationPendingFor(t *testing.T, guc, email string) bool {
	t.Helper()
	var got bool
	err := inAppTx(context.Background(), guc, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT public.invitation_pending_for_email($1::text)`, email).Scan(&got)
	})
	if err != nil {
		t.Fatalf("invitation_pending_for_email(%q) as invoice_app (GUC %q): %v", email, guc, err)
	}
	return got
}

func TestRLS_RegisteredTokenHashIsNullableAndThirtyTwoBytes(t *testing.T) {
	requireHarness(t)
	reapplyRegistrationMigration(t)
	tenant := newNamedTenant(t, "Registration Firm")
	id := seedAcceptInvite(t, tenant, "reviewer", uniqueAddress("tunde"), newToken(t))

	if got := registeredHash(t, id); got != nil {
		t.Fatalf("fresh invite registered_token_hash = %x, want NULL", got)
	}
	var dataType, nullable string
	var dflt *string
	if err := h.super.QueryRow(context.Background(),
		`SELECT data_type, is_nullable, column_default FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'invitations' AND column_name = 'registered_token_hash'`,
	).Scan(&dataType, &nullable, &dflt); err != nil {
		t.Fatalf("read registered_token_hash column: %v", err)
	}
	if dataType != "bytea" || nullable != "YES" || dflt != nil {
		t.Errorf("registered_token_hash = %s nullable=%s default=%v, want bytea nullable=YES default=<none>", dataType, nullable, dflt)
	}

	want := hashOf(newToken(t))
	if len(want) != 32 {
		t.Fatalf("fixture hash is %d bytes, want 32", len(want))
	}
	if _, err := h.super.Exec(context.Background(), `UPDATE invitations SET registered_token_hash = $2 WHERE id = $1`, id, want); err != nil {
		t.Fatalf("set a 32-byte registered_token_hash: %v", err)
	}
	if got := registeredHash(t, id); !bytes.Equal(got, want) {
		t.Fatalf("registered_token_hash = %x, want %x", got, want)
	}

	_, err := h.super.Exec(context.Background(), `UPDATE invitations SET registered_token_hash = $2 WHERE id = $1`, id, want[:31])
	assertAcceptRefusal(t, "31-byte registered_token_hash", err, "23514", "invitations_registered_token_hash_len")
	if got := registeredHash(t, id); !bytes.Equal(got, want) {
		t.Errorf("registered_token_hash after the refused update = %x, want the 32-byte value %x", got, want)
	}
}

func TestRLS_InvitationPendingForEmailIsADefinerOwnedByTheHookReader(t *testing.T) {
	requireHarness(t)
	reapplyRegistrationMigration(t)

	s, ok := readProc(t, pendingForEmailSig)
	if !ok {
		t.Fatalf("function %s does not exist", pendingForEmailSig)
	}
	if s.owner != "auth_hook_reader" {
		t.Errorf("owner = %q, want auth_hook_reader", s.owner)
	}
	if !s.secdef {
		t.Errorf("prosecdef = false, want SECURITY DEFINER")
	}
	if !reflect.DeepEqual(s.config, []string{`search_path=""`}) {
		t.Errorf("proconfig = %q, want [search_path=\"\"]", s.config)
	}
	if s.volatile != "s" {
		t.Errorf("provolatile = %q, want s (STABLE)", s.volatile)
	}
	assertExecuteOnlyForApp(t, pendingForEmailSig, s, "auth_hook_reader")
}

func TestRLS_InvitationPendingForEmailSeesEveryTenant(t *testing.T) {
	requireHarness(t)
	reapplyRegistrationMigration(t)
	one := newNamedTenant(t, "Firm One")
	two := newNamedTenant(t, "Firm Two")
	a, b := uniqueAddress("a"), uniqueAddress("b")
	seedAcceptInvite(t, one, "reviewer", a, newToken(t))
	seedAcceptInvite(t, two, "reviewer", b, newToken(t))

	for _, c := range []struct{ name, guc string }{
		{"guc unset", ""},
		{"guc nil uuid", uuid.Nil.String()},
		{"guc tenant one", one},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, addr := range []string{a, b} {
				if !invitationPendingFor(t, c.guc, addr) {
					t.Errorf("pending_for_email(%q) = false, want true", addr)
				}
			}
		})
	}
}

func TestRLS_InvitationPendingForEmailIgnoresSpentExpiredAndOtherAddresses(t *testing.T) {
	requireHarness(t)
	reapplyRegistrationMigration(t)
	tenant := newNamedTenant(t, "Spent Firm")
	c, d, e, f := uniqueAddress("c"), uniqueAddress("d"), uniqueAddress("e"), uniqueAddress("f")
	idC := seedAcceptInvite(t, tenant, "reviewer", c, newToken(t))
	idD := seedAcceptInvite(t, tenant, "reviewer", d, newToken(t))
	idE := seedAcceptInvite(t, tenant, "reviewer", e, newToken(t))
	seedAcceptInvite(t, tenant, "reviewer", f, newToken(t))
	setInviteState(t, idC, `status = 'accepted'`)
	setInviteState(t, idD, `status = 'revoked'`)
	setInviteState(t, idE, `expires_at = now() - interval '1 second'`)

	if !invitationPendingFor(t, "", f) {
		t.Fatalf("control: pending invite of %q = false, want true", f)
	}
	for _, tc := range []struct{ name, addr string }{
		{"accepted", c},
		{"revoked", d},
		{"expired", e},
		{"another address", uniqueAddress("g")},
	} {
		if invitationPendingFor(t, "", tc.addr) {
			t.Errorf("%s: pending_for_email(%q) = true, want false", tc.name, tc.addr)
		}
	}
}

func TestRLS_InvitationPendingForEmailFoldsCaseAndSpaces(t *testing.T) {
	requireHarness(t)
	reapplyRegistrationMigration(t)
	tenant := newNamedTenant(t, "Fold Firm")
	local := "pat-" + uuid.NewString()[:8]
	seedAcceptInvite(t, tenant, "reviewer", local+"@firm.test", newToken(t))
	mixed := "Mixed-" + uuid.NewString()[:8]
	seedAcceptInvite(t, tenant, "reviewer", mixed+"@Firm.Test", newToken(t))

	if !invitationPendingFor(t, "", local+"@firm.test") {
		t.Fatalf("control: exact address = false, want true")
	}
	for _, c := range []struct{ name, addr string }{
		{"padded, mixed-case", "  " + local + "@Firm.Test "},
		{"leading spaces only", "   " + local + "@firm.test"},
		{"trailing spaces only", local + "@firm.test   "},
		{"upper-case", strings.ToUpper(local) + "@FIRM.TEST"},
		{"stored mixed case, probed lower", mixed + "@firm.test"},
		{"stored mixed case, probed as stored", mixed + "@Firm.Test"},
		{"stored mixed case, probed upper", "  " + strings.ToUpper(mixed) + "@FIRM.TEST "},
	} {
		if !invitationPendingFor(t, "", c.addr) {
			t.Errorf("%s: pending_for_email(%q) = false, want true", c.name, c.addr)
		}
	}
	if invitationPendingFor(t, "", "  "+local+"x@Firm.Test ") {
		t.Errorf("a different address after folding = true, want false")
	}
}

func TestRLS_RegisteredTokenHashUpdateIsTenantScoped(t *testing.T) {
	requireHarness(t)
	reapplyRegistrationMigration(t)
	one := newNamedTenant(t, "Scope Firm One")
	two := newNamedTenant(t, "Scope Firm Two")
	idOne := seedAcceptInvite(t, one, "reviewer", uniqueAddress("p"), newToken(t))
	idTwo := seedAcceptInvite(t, two, "reviewer", uniqueAddress("q"), newToken(t))
	hash := hashOf(newToken(t))

	var affected int64
	err := inAppTx(context.Background(), one, func(tx pgx.Tx) error {
		tag, err := tx.Exec(context.Background(),
			`UPDATE invitations SET registered_token_hash = $1 WHERE id::text = ANY($2::text[])`, hash, []string{idOne, idTwo})
		affected = tag.RowsAffected()
		return err
	})
	if err != nil {
		t.Fatalf("update registered_token_hash as invoice_app (GUC tenant one): %v", err)
	}
	if affected != 1 {
		t.Errorf("rows affected = %d, want 1 (tenant one only)", affected)
	}
	if got := registeredHash(t, idOne); !bytes.Equal(got, hash) {
		t.Errorf("tenant one registered_token_hash = %x, want %x", got, hash)
	}
	if got := registeredHash(t, idTwo); got != nil {
		t.Errorf("tenant two registered_token_hash = %x, want NULL", got)
	}

	other := hashOf(newToken(t))
	for _, c := range []struct{ name, guc, id string }{
		{"tenant two against tenant one's row", two, idOne},
		{"tenant one against tenant two's row", one, idTwo},
		{"nil uuid GUC against tenant one's row", uuid.Nil.String(), idOne},
		{"unset GUC against tenant one's row", "", idOne},
		{"unset GUC against tenant two's row", "", idTwo},
	} {
		var n int64
		err := inAppTx(context.Background(), c.guc, func(tx pgx.Tx) error {
			tag, err := tx.Exec(context.Background(), `UPDATE invitations SET registered_token_hash = $1 WHERE id = $2::uuid`, other, c.id)
			n = tag.RowsAffected()
			return err
		})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if n != 0 {
			t.Errorf("%s: rows affected = %d, want 0", c.name, n)
		}
	}
	if got := registeredHash(t, idOne); !bytes.Equal(got, hash) {
		t.Errorf("tenant one registered_token_hash after the cross-tenant attempts = %x, want %x", got, hash)
	}
	if got := registeredHash(t, idTwo); got != nil {
		t.Errorf("tenant two registered_token_hash after the cross-tenant attempts = %x, want NULL", got)
	}
}

func TestRLS_RegisteredTokenHashRefusesEveryOtherLength(t *testing.T) {
	requireHarness(t)
	reapplyRegistrationMigration(t)
	tenant := newNamedTenant(t, "Length Firm")
	id := seedAcceptInvite(t, tenant, "reviewer", uniqueAddress("len"), newToken(t))
	want := hashOf(newToken(t))
	set := func(v []byte) error {
		_, err := h.super.Exec(context.Background(), `UPDATE invitations SET registered_token_hash = $2 WHERE id = $1`, id, v)
		return err
	}

	if err := set(want); err != nil {
		t.Fatalf("control: set a 32-byte hash: %v", err)
	}
	for _, n := range []int{0, 1, 31, 33, 64} {
		assertAcceptRefusal(t, fmt.Sprintf("%d-byte registered_token_hash", n), set(bytes.Repeat([]byte{0xab}, n)), "23514", "invitations_registered_token_hash_len")
	}
	if got := registeredHash(t, id); !bytes.Equal(got, want) {
		t.Errorf("registered_token_hash after the refused updates = %x, want %x", got, want)
	}
	if err := set(nil); err != nil {
		t.Fatalf("set registered_token_hash back to NULL: %v", err)
	}
	if got := registeredHash(t, id); got != nil {
		t.Errorf("registered_token_hash after NULL = %x, want NULL", got)
	}
}

func TestRLS_InvitationPendingForEmailRefusesEveryOtherRole(t *testing.T) {
	requireHarness(t)
	reapplyRegistrationMigration(t)
	tenant := newNamedTenant(t, "Role Firm")
	addr := uniqueAddress("role")
	seedAcceptInvite(t, tenant, "reviewer", addr, newToken(t))

	if !invitationPendingFor(t, "", addr) {
		t.Fatalf("control: invoice_app sees the pending invite of %q = false, want true", addr)
	}
	for _, role := range []struct {
		name string
		pool interface {
			QueryRow(context.Context, string, ...any) pgx.Row
		}
	}{
		{"invoice_tenant_reader", h.reader},
		{"supabase_auth_admin", authAdminPool(t)},
	} {
		var got bool
		err := role.pool.QueryRow(context.Background(), `SELECT public.invitation_pending_for_email($1::text)`, addr).Scan(&got)
		if code := sqlstate(err); code != sqlstateInsufficientPrivilege {
			t.Errorf("%s: SQLSTATE %q (%v, got=%v), want %s", role.name, code, err, got, sqlstateInsufficientPrivilege)
		}
	}
}

func TestRLS_InvitationPendingForEmailRevealsOnlyABoolean(t *testing.T) {
	requireHarness(t)
	reapplyRegistrationMigration(t)
	one := newNamedTenant(t, "Bool Firm One")
	two := newNamedTenant(t, "Bool Firm Two")
	addr := uniqueAddress("bool")
	seedAcceptInvite(t, one, "reviewer", addr, newToken(t))
	seedAcceptInvite(t, two, "reviewer", addr, newToken(t))

	var retType string
	var retSet bool
	var nargs int
	if err := h.super.QueryRow(context.Background(),
		`SELECT p.prorettype::regtype::text, p.proretset, p.pronargs FROM pg_proc p WHERE p.oid = to_regprocedure($1)`, pendingForEmailSig,
	).Scan(&retType, &retSet, &nargs); err != nil {
		t.Fatalf("read pg_proc return shape: %v", err)
	}
	if retType != "boolean" || retSet || nargs != 1 {
		t.Errorf("return shape = %s set=%v args=%d, want boolean set=false args=1", retType, retSet, nargs)
	}

	for _, probe := range []string{addr, uniqueAddress("nobody")} {
		var cols, rowCount int
		var oid uint32
		err := inAppTx(context.Background(), "", func(tx pgx.Tx) error {
			rows, err := tx.Query(context.Background(), `SELECT * FROM public.invitation_pending_for_email($1::text)`, probe)
			if err != nil {
				return err
			}
			defer rows.Close()
			fds := rows.FieldDescriptions()
			cols, oid = len(fds), fds[0].DataTypeOID
			for rows.Next() {
				rowCount++
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatalf("select * from the function for %q: %v", probe, err)
		}
		if cols != 1 || oid != 16 || rowCount != 1 {
			t.Errorf("%q: columns=%d type oid=%d rows=%d, want 1 column of boolean (oid 16) and 1 row, though two tenants hold the address", probe, cols, oid, rowCount)
		}
	}
	if !invitationPendingFor(t, "", addr) || invitationPendingFor(t, "", uniqueAddress("nobody")) {
		t.Errorf("the boolean is not true for the shared address and false for a stranger")
	}
}

func TestRLS_InvitationPendingForEmailRefusesNullEmptyAndBlankInput(t *testing.T) {
	requireHarness(t)
	reapplyRegistrationMigration(t)
	tenant := newNamedTenant(t, "Blank Firm")
	seedAcceptInvite(t, tenant, "reviewer", uniqueAddress("blank"), newToken(t))
	seedAcceptInvite(t, tenant, "reviewer", " ", newToken(t))

	if n := mustCount(t, h.super, `SELECT count(*) FROM invitations WHERE status = 'pending' AND expires_at > now()`); n < 2 {
		t.Fatalf("pending invites = %d, want at least 2 so a blank probe cannot pass on an empty table", n)
	}
	var none *string
	empty, oneSpace, spaces, tabs := "", " ", "   ", " \t "
	for _, c := range []struct {
		name string
		in   *string
	}{{"NULL", none}, {"empty", &empty}, {"one space", &oneSpace}, {"spaces", &spaces}, {"tabs", &tabs}} {
		var got *bool
		err := inAppTx(context.Background(), "", func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), `SELECT public.invitation_pending_for_email($1::text)`, c.in).Scan(&got)
		})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got == nil {
			t.Errorf("%s: result is NULL, want false", c.name)
		} else if *got {
			t.Errorf("%s: result = true, want false", c.name)
		}
	}
}

func TestRLS_InvitationPendingForEmailTreatsPatternCharactersAsLiterals(t *testing.T) {
	requireHarness(t)
	reapplyRegistrationMigration(t)
	tenant := newNamedTenant(t, "Pattern Firm")
	local := "pat-" + uuid.NewString()[:8]
	seedAcceptInvite(t, tenant, "reviewer", local+"@firm.test", newToken(t))

	if !invitationPendingFor(t, "", local+"@firm.test") {
		t.Fatalf("control: exact address = false, want true")
	}
	for _, probe := range []string{"%", "_", "%@firm.test", local + "%", local, local + "@firm.tes_", local + "@firm.test.", "x" + local + "@firm.test"} {
		if invitationPendingFor(t, "", probe) {
			t.Errorf("pending_for_email(%q) = true, want false", probe)
		}
	}
}

func TestRLS_RegisteredTokenHashStaysBehindTheTenantPolicy(t *testing.T) {
	requireHarness(t)
	reapplyRegistrationMigration(t)
	ctx := context.Background()
	one := newNamedTenant(t, "Policy Firm One")
	two := newNamedTenant(t, "Policy Firm Two")
	idOne := seedAcceptInvite(t, one, "reviewer", uniqueAddress("r"), newToken(t))
	idTwo := seedAcceptInvite(t, two, "reviewer", uniqueAddress("s"), newToken(t))
	hash := hashOf(newToken(t))
	if _, err := h.super.Exec(ctx, `UPDATE invitations SET registered_token_hash = $1 WHERE id::text = ANY($2::text[])`, hash, []string{idOne, idTwo}); err != nil {
		t.Fatalf("seed registered_token_hash on both rows: %v", err)
	}

	for _, c := range []struct {
		name, guc string
		want      int
	}{{"tenant one", one, 1}, {"tenant two", two, 1}, {"nil uuid", uuid.Nil.String(), 0}, {"unset", "", 0}} {
		var n int
		err := inAppTx(ctx, c.guc, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(registered_token_hash) FROM invitations WHERE id::text = ANY($1::text[])`, []string{idOne, idTwo}).Scan(&n)
		})
		if err != nil {
			t.Fatalf("read as invoice_app (%s): %v", c.name, err)
		}
		if n != c.want {
			t.Errorf("GUC %s reads %d registered_token_hash values of the two rows, want %d", c.name, n, c.want)
		}
	}

	var policies []string
	var rls, forced bool
	rows, err := h.super.Query(ctx, `SELECT polname FROM pg_policy WHERE polrelid = 'public.invitations'::regclass ORDER BY 1`)
	if err != nil {
		t.Fatalf("read invitations policies: %v", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan policy: %v", err)
		}
		policies = append(policies, name)
	}
	rows.Close()
	if err := h.super.QueryRow(ctx, `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = 'public.invitations'::regclass`).Scan(&rls, &forced); err != nil {
		t.Fatalf("read invitations RLS flags: %v", err)
	}
	if want := []string{"invitation_token_lookup", "tenant_isolation"}; !reflect.DeepEqual(policies, want) || !rls || !forced {
		t.Errorf("invitations policies = %v, RLS = %v, forced = %v, want %v with RLS on and forced", policies, rls, forced, want)
	}

	for _, c := range []struct {
		role, priv string
		want       bool
	}{
		{"invoice_app", "SELECT", true}, {"invoice_app", "UPDATE", true},
		{"auth_hook_reader", "SELECT", false}, {"auth_hook_reader", "UPDATE", false}, {"auth_hook_reader", "INSERT", false},
		{"invoice_tenant_reader", "SELECT", false}, {"invoice_tenant_reader", "UPDATE", false},
		{"supabase_auth_admin", "SELECT", false}, {"supabase_auth_admin", "UPDATE", false},
	} {
		var got bool
		if err := h.super.QueryRow(ctx, `SELECT has_column_privilege($1, 'public.invitations', 'registered_token_hash', $2)`, c.role, c.priv).Scan(&got); err != nil {
			t.Fatalf("has_column_privilege(%s, %s): %v", c.role, c.priv, err)
		}
		if got != c.want {
			t.Errorf("%s %s on registered_token_hash = %v, want %v", c.role, c.priv, got, c.want)
		}
	}
}

func TestRLS_InvitationRegistrationDownDropsTheFunctionAndTheColumn(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	provider, version := staffMigrationProvider(t), migrationVersion(t, registrationMigrationGlob)
	resetRegistrationMigration(t, provider, version)
	t.Cleanup(func() {
		if err := resetRegistrationMigrationErr(provider, version); err != nil {
			t.Errorf("restore the registration migration: %v", err)
		}
	})

	columns := func() []string {
		t.Helper()
		rows, err := h.super.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'invitations' ORDER BY column_name`)
		if err != nil {
			t.Fatalf("read invitations columns: %v", err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				t.Fatalf("scan column: %v", err)
			}
			out = append(out, c)
		}
		sort.Strings(out)
		return out
	}
	fn := func() bool {
		t.Helper()
		var ok bool
		if err := h.super.QueryRow(ctx, `SELECT to_regprocedure($1) IS NOT NULL`, pendingForEmailSig).Scan(&ok); err != nil {
			t.Fatalf("look up %s: %v", pendingForEmailSig, err)
		}
		return ok
	}

	upColumns := columns()
	var without []string
	for _, c := range upColumns {
		if c != "registered_token_hash" {
			without = append(without, c)
		}
	}
	if len(without) != len(upColumns)-1 || len(without) < 2 || !fn() {
		t.Fatalf("before Down: columns=%v function present=%v, want the new column and the function", upColumns, fn())
	}

	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("roll back the registration migration: %v", err)
	}
	if fn() {
		t.Errorf("after Down: %s still exists", pendingForEmailSig)
	}
	if got := columns(); !reflect.DeepEqual(got, without) {
		t.Errorf("after Down: invitations columns = %v, want exactly %v", got, without)
	}

	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("re-apply the registration migration: %v", err)
	}
	if got := columns(); !reflect.DeepEqual(got, upColumns) || !fn() {
		t.Errorf("after re-applying Up: columns = %v function present = %v, want %v and the function", got, fn(), upColumns)
	}
}
