package db_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"
)

const (
	guardSig        = "public.identity_has_membership(uuid)"
	guardConstraint = "one_workspace_per_identity"
	guardGlob       = "*_provision_workspace_one_per_identity.sql"
)

// assertGuardRefusal asserts the 23505 the guard raises, naming its constraint.
func assertGuardRefusal(t *testing.T, what string, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("%s: want 23505 on %s, got %v", what, guardConstraint, err)
	}
	if pgErr.Code != "23505" || pgErr.ConstraintName != guardConstraint {
		t.Fatalf("%s: want 23505 on %s, got %s on %q (%s)", what, guardConstraint, pgErr.Code, pgErr.ConstraintName, pgErr.Message)
	}
}

func membershipCount(t *testing.T, userID string) int {
	t.Helper()
	return mustCount(t, h.super, `SELECT count(*) FROM memberships WHERE user_id = $1`, userID)
}

// useShippedGuard applies the shipped guard Up over whatever an earlier run left, so the test reads
// the file and not the DB migrated earlier. It skips the Down, which a broken Down could block.
func useShippedGuard(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	provider, version := guardMigrationProvider(t)
	if _, err := h.super.Exec(ctx, `DROP FUNCTION IF EXISTS `+guardSig); err != nil {
		t.Fatalf("drop %s: %v", guardSig, err)
	}
	if _, err := h.super.Exec(ctx, `DELETE FROM goose_db_version WHERE version_id = $1`, version); err != nil {
		t.Fatalf("forget the guard migration: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("apply the guard migration: %v", err)
	}
}

func requireGuardFunction(t *testing.T) {
	t.Helper()
	if n := mustCount(t, h.super, `SELECT count(*) FROM pg_proc WHERE oid = to_regprocedure($1)`, guardSig); n != 1 {
		t.Fatalf("%s: want 1 pg_proc row, got %d", guardSig, n)
	}
}

func TestRLS_ProvisionGuard_AnyMembershipRefused(t *testing.T) {
	h := requireHarness(t)
	useShippedGuard(t)
	ctx := context.Background()

	for _, status := range []string{"active", "suspended", "invited"} {
		t.Run(status, func(t *testing.T) {
			userID := uuid.NewString()
			seedHookMembership(t, h.tenantA, userID, status)
			if n := membershipCount(t, userID); n != 1 {
				t.Fatalf("seeded memberships for the user = %d, want 1", n)
			}
			id := uuid.NewString()
			cleanupTenant(t, id)
			a := newProvisionArgs(id)
			a.userID = userID

			err := provisionAs(ctx, h.app, id, a)

			assertGuardRefusal(t, "provision for a user whose membership is "+status, err)
			assertNothingWritten(t, id)
			if n := membershipCount(t, userID); n != 1 {
				t.Errorf("memberships for the user after the refusal = %d, want 1", n)
			}
		})
	}
}

func TestRLS_ProvisionGuard_TwoActiveMembershipsRefused(t *testing.T) {
	h := requireHarness(t)
	useShippedGuard(t)
	ctx := context.Background()
	userID := uuid.NewString()
	seedHookMembership(t, h.tenantA, userID, "active")
	seedHookMembership(t, h.tenantB, userID, "active")
	if n := membershipCount(t, userID); n != 2 {
		t.Fatalf("seeded memberships for the user = %d, want 2", n)
	}
	id := uuid.NewString()
	cleanupTenant(t, id)
	a := newProvisionArgs(id)
	a.userID = userID

	err := provisionAs(ctx, h.app, id, a)

	assertGuardRefusal(t, "provision for a user with two active memberships", err)
	assertNothingWritten(t, id)
	if n := membershipCount(t, userID); n != 2 {
		t.Errorf("memberships for the user after the refusal = %d, want 2", n)
	}
}

// The control that gives the refusal tests their meaning; the hook read is the next token.
func TestRLS_ProvisionGuard_FreshIdentityStillProvisions(t *testing.T) {
	h := requireHarness(t)
	useShippedGuard(t)
	ctx := context.Background()
	id := uuid.NewString()
	cleanupTenant(t, id)
	a := newProvisionArgs(id)

	if err := provisionAs(ctx, h.app, id, a); err != nil {
		t.Fatalf("provision for a user with no membership: want success, got %v", err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM tenants WHERE id = $1`, id); n != 1 {
		t.Errorf("tenants rows = %d, want 1", n)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM memberships WHERE tenant_id = $1 AND user_id = $2 AND role = 'admin' AND status = 'active'`,
		id, a.userID); n != 1 {
		t.Errorf("active admin rows for the user = %d, want 1", n)
	}
	got, _ := hookRun(t, authAdminPool(t), a.userID, nil)
	if tid := appMetadataOf(t, got)["tenant_id"]; tid != id {
		t.Errorf("hook app_metadata.tenant_id = %v, want the new tenant %s", tid, id)
	}
}

func TestRLS_ProvisionGuard_FunctionShape(t *testing.T) {
	h := requireHarness(t)
	useShippedGuard(t)
	ctx := context.Background()
	requireGuardFunction(t)

	var (
		secdef    bool
		owner     string
		config    []string
		argTypes  string
		returns   string
		grantees  []string
		publicAny bool
	)
	if err := h.super.QueryRow(ctx, `
		SELECT p.prosecdef, pg_get_userbyid(p.proowner), coalesce(p.proconfig, '{}'), (SELECT array_agg(t::regtype::text) FROM unnest(p.proargtypes::oid[]) t)::text, p.prorettype::regtype::text,
		       coalesce((SELECT array_agg(pg_get_userbyid(a.grantee) || '=' || a.privilege_type ORDER BY 1)
		                 FROM aclexplode(p.proacl) a WHERE a.grantee <> 0), '{}'),
		       p.proacl IS NULL OR EXISTS (SELECT 1 FROM aclexplode(p.proacl) a WHERE a.grantee = 0)
		FROM pg_proc p WHERE p.oid = to_regprocedure($1)`, guardSig,
	).Scan(&secdef, &owner, &config, &argTypes, &returns, &grantees, &publicAny); err != nil {
		t.Fatalf("read pg_proc: %v", err)
	}

	if !secdef {
		t.Errorf("prosecdef = false, want true (SECURITY DEFINER)")
	}
	if owner != "auth_hook_reader" {
		t.Errorf("owner = %q, want auth_hook_reader", owner)
	}
	if !reflect.DeepEqual(config, []string{`search_path=""`}) {
		t.Errorf("proconfig = %q, want [search_path=\"\"]", config)
	}
	if argTypes != "{uuid}" {
		t.Errorf("argument types = %s, want {uuid}", argTypes)
	}
	if returns != "boolean" {
		t.Errorf("return type = %q, want boolean", returns)
	}
	if publicAny {
		t.Errorf("PUBLIC holds EXECUTE (or proacl is the NULL default), want revoked")
	}
	sort.Strings(grantees)
	if want := []string{"auth_hook_reader=EXECUTE", "invoice_migrator=EXECUTE"}; !reflect.DeepEqual(grantees, want) {
		t.Errorf("ACL = %v, want exactly %v", grantees, want)
	}
}

func TestRLS_ProvisionGuard_AppCannotAskAboutAnIdentity(t *testing.T) {
	h := requireHarness(t)
	useShippedGuard(t)
	ctx := context.Background()
	requireGuardFunction(t)
	member := uuid.NewString()
	seedHookMembership(t, h.tenantA, member, "active")

	// Positive control: the one granted role gets a real answer.
	for _, c := range []struct {
		who  string
		user string
		want bool
	}{{"member", member, true}, {"stranger", uuid.NewString(), false}} {
		var got bool
		if err := h.mig.QueryRow(ctx, `SELECT public.identity_has_membership($1)`, c.user).Scan(&got); err != nil {
			t.Fatalf("invoice_migrator asks about a %s: %v", c.who, err)
		}
		if got != c.want {
			t.Errorf("identity_has_membership(%s) = %v, want %v", c.who, got, c.want)
		}
	}

	var got bool
	err := h.app.QueryRow(ctx, `SELECT public.identity_has_membership($1)`, member).Scan(&got)
	assertPgRefusal(t, "invoice_app executes identity_has_membership", err, "42501", "permission denied for function identity_has_membership")
}

func guardMigrationProvider(t *testing.T) (*goose.Provider, int64) {
	t.Helper()
	return staffMigrationProvider(t), migrationVersion(t, guardGlob)
}

func TestRLS_ProvisionGuard_DownRestoresTheUnguardedFunction(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireGuardFunction(t)
	provider, version := guardMigrationProvider(t)

	t.Cleanup(func() {
		if _, err := provider.ApplyVersion(context.Background(), version, true); err != nil && !errors.Is(err, goose.ErrAlreadyApplied) {
			t.Errorf("re-apply the guard migration: %v", err)
		}
	})
	member := uuid.NewString()
	seedHookMembership(t, h.tenantA, member, "suspended")

	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("roll back the guard migration: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM pg_proc WHERE oid = to_regprocedure($1)`, guardSig); n != 0 {
		t.Errorf("%s after Down: %d pg_proc rows, want 0", guardSig, n)
	}
	assertProvisionShape(t, h.super)
	downID := uuid.NewString()
	cleanupTenant(t, downID)
	a := newProvisionArgs(downID)
	a.userID = member
	if err := provisionAs(ctx, h.app, downID, a); err != nil {
		t.Errorf("provision for a member after Down: want the AUTH-03 success, got %v", err)
	}

	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("re-apply the guard migration: %v", err)
	}
	requireGuardFunction(t)
	assertProvisionShape(t, h.super)
	upID := uuid.NewString()
	cleanupTenant(t, upID)
	b := newProvisionArgs(upID)
	b.userID = member
	assertGuardRefusal(t, "provision for a member after Up", provisionAs(ctx, h.app, upID, b))
	assertNothingWritten(t, upID)
}

func TestRLS_ProvisionGuard_MismatchedGUCAnswersAlikeForMemberAndStranger(t *testing.T) {
	h := requireHarness(t)
	useShippedGuard(t)
	ctx := context.Background()
	member := uuid.NewString()
	seedHookMembership(t, h.tenantA, member, "suspended")
	if n := membershipCount(t, member); n != 1 {
		t.Fatalf("seeded memberships for the member = %d, want 1", n)
	}

	for _, who := range []struct{ name, user string }{{"member", member}, {"stranger", uuid.NewString()}} {
		x, y := uuid.NewString(), uuid.NewString()
		cleanupTenant(t, x, y)
		a := newProvisionArgs(x)
		a.userID = who.user

		err := provisionAs(ctx, h.app, y, a)
		assertPgRefusal(t, who.name+" under a mismatched GUC", err, "42501", "row-level security")

		tx, err := h.app.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		err = a.exec(ctx, tx)
		_ = tx.Rollback(ctx)
		assertPgRefusal(t, who.name+" with the GUC unset", err, "42501", "row-level security")

		assertNothingWritten(t, x, y)
	}
}

func TestRLS_ProvisionGuard_MatchingGUCResidualIsTheDocumentedOne(t *testing.T) {
	h := requireHarness(t)
	useShippedGuard(t)
	ctx := context.Background()
	member := uuid.NewString()
	seedHookMembership(t, h.tenantA, member, "suspended")

	memberID, strangerID := uuid.NewString(), uuid.NewString()
	cleanupTenant(t, memberID, strangerID)
	m := newProvisionArgs(memberID)
	m.userID = member
	s := newProvisionArgs(strangerID)

	assertGuardRefusal(t, "member under a matching GUC", provisionAs(ctx, h.app, memberID, m))
	assertNothingWritten(t, memberID)
	if err := provisionAs(ctx, h.app, strangerID, s); err != nil {
		t.Fatalf("stranger under a matching GUC: want a tenant written, got %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM tenants WHERE id = $1`, strangerID); n != 1 {
		t.Errorf("tenants rows for the stranger = %d, want 1", n)
	}
}

func provisionSrc(t *testing.T, q querier) string {
	t.Helper()
	var src string
	if err := q.QueryRow(context.Background(), `SELECT prosrc FROM pg_proc WHERE oid = to_regprocedure($1)`, provisionSig).Scan(&src); err != nil {
		t.Fatalf("read provision_workspace prosrc: %v", err)
	}
	return src
}

func TestRLS_ProvisionGuard_DownBodyIsTheAuth03Body(t *testing.T) {
	h := requireHarness(t)
	useShippedGuard(t)
	provider, version := guardMigrationProvider(t)
	t.Cleanup(func() {
		if _, err := provider.ApplyVersion(context.Background(), version, true); err != nil && !errors.Is(err, goose.ErrAlreadyApplied) {
			t.Errorf("re-apply the guard migration: %v", err)
		}
	})
	guarded := provisionSrc(t, h.super)

	if _, err := provider.ApplyVersion(context.Background(), version, false); err != nil {
		t.Fatalf("roll back the guard migration: %v", err)
	}
	afterDown := provisionSrc(t, h.super)
	auth03 := provisionSrc(t, shippedProvisionUpTx(t))

	if afterDown == guarded {
		t.Errorf("provision_workspace body is unchanged by the Down, want the unguarded body")
	}
	if afterDown != auth03 {
		t.Errorf("provision_workspace body after the Down differs from the AUTH-03 Up body:\n--- Down\n%s\n--- AUTH-03\n%s", afterDown, auth03)
	}
}

func TestRLS_ProvisionGuard_DeletingTheOnlyTenantFreesTheIdentity(t *testing.T) {
	h := requireHarness(t)
	useShippedGuard(t)
	ctx := context.Background()
	tenant := uuid.NewString()
	if _, err := h.super.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'Doomed')`, tenant); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	cleanupTenant(t, tenant)
	user := uuid.NewString()
	seedHookMembership(t, tenant, user, "active")

	refusedID := uuid.NewString()
	cleanupTenant(t, refusedID)
	r := newProvisionArgs(refusedID)
	r.userID = user
	assertGuardRefusal(t, "provision while the tenant exists", provisionAs(ctx, h.app, refusedID, r))

	if _, err := h.super.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant); err != nil {
		t.Fatalf("delete tenant: %v", err)
	}
	if n := membershipCount(t, user); n != 0 {
		t.Fatalf("memberships after the tenant delete = %d, want 0 (cascade)", n)
	}
	freedID := uuid.NewString()
	cleanupTenant(t, freedID)
	f := newProvisionArgs(freedID)
	f.userID = user
	if err := provisionAs(ctx, h.app, freedID, f); err != nil {
		t.Errorf("provision after the only tenant was deleted: want success, got %v", err)
	}
}

func TestRLS_ProvisionGuard_ConcurrentSameWorkspaceHasOneWinner(t *testing.T) {
	h := requireHarness(t)
	useShippedGuard(t)
	ctx := context.Background()
	id := uuid.NewString()
	cleanupTenant(t, id)
	a := newProvisionArgs(id)

	errs := make([]error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = provisionAs(ctx, h.app, id, a)
		}()
	}
	close(start)
	wg.Wait()

	wins := 0
	for _, err := range errs {
		var pgErr *pgconn.PgError
		switch {
		case err == nil:
			wins++
		case errors.As(err, &pgErr) && pgErr.Code == "23505" && (pgErr.ConstraintName == "tenants_pkey" || pgErr.ConstraintName == guardConstraint):
		default:
			t.Errorf("loser: want 23505 on tenants_pkey or %s, got %v", guardConstraint, err)
		}
	}
	if wins != 1 {
		t.Errorf("successes = %d (errs %v), want exactly 1", wins, errs)
	}
	if n := membershipCount(t, a.userID); n != 1 {
		t.Errorf("memberships for the user = %d, want 1", n)
	}
}

// beginProvision opens an invoice_app tx with its GUC set and calls provision_workspace in it.
// It returns errors, not fatals, so a goroutine can call it.
func beginProvision(ctx context.Context, a provisionArgs) (pgx.Tx, error) {
	tx, err := h.app.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, a.tenantID); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, a.exec(ctx, tx)
}

func TestRLS_ProvisionGuard_ConcurrentDifferentTenantIdsHaveOneWinner(t *testing.T) {
	h := requireHarness(t)
	useShippedGuard(t)
	ctx := context.Background()
	user := uuid.NewString()
	x, y := uuid.NewString(), uuid.NewString()
	cleanupTenant(t, x, y)
	a, b := newProvisionArgs(x), newProvisionArgs(y)
	a.userID, b.userID = user, user

	first, err := beginProvision(ctx, a)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	defer func() { _ = first.Rollback(ctx) }()

	done := make(chan error, 1)
	go func() {
		second, err := beginProvision(ctx, b)
		switch {
		case second == nil:
		case err == nil:
			err = second.Commit(ctx)
		default:
			_ = second.Rollback(ctx)
		}
		done <- err
	}()

	// The second call must wait on the first's uncommitted work, not pass the guard beside it.
	deadline := time.Now().Add(10 * time.Second)
	for waiting := 0; waiting == 0; {
		select {
		case err := <-done:
			t.Fatalf("second call finished while the first was open (err %v), want it blocked", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("second call never blocked on an advisory lock")
		}
		waiting = mustCount(t, h.super, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`)
	}

	if err := first.Commit(ctx); err != nil {
		t.Fatalf("commit the first call: %v", err)
	}
	assertGuardRefusal(t, "second call for the same identity", <-done)
	if n := membershipCount(t, user); n != 1 {
		t.Errorf("memberships for the user = %d, want 1", n)
	}
	assertNothingWritten(t, y)
}
