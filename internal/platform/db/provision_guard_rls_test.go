package db_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/google/uuid"
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

func requireGuardFunction(t *testing.T) {
	t.Helper()
	if n := mustCount(t, h.super, `SELECT count(*) FROM pg_proc WHERE oid = to_regprocedure($1)`, guardSig); n != 1 {
		t.Fatalf("%s: want 1 pg_proc row, got %d", guardSig, n)
	}
}

func TestRLS_ProvisionGuard_AnyMembershipRefused(t *testing.T) {
	h := requireHarness(t)
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

// Green at head: the control that gives the refusal tests their meaning.
func TestRLS_ProvisionGuard_FreshIdentityStillProvisions(t *testing.T) {
	h := requireHarness(t)
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
}

func TestRLS_ProvisionGuard_FunctionShape(t *testing.T) {
	h := requireHarness(t)
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
