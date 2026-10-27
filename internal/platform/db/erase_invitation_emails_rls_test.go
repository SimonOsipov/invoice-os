// public.erase_invitation_emails and its finder public.invitations_to_erase (DEL-13-01):
// ownership, grants, tenant reach, the erasure guard and the GUC restore. Env-gated through
// requireHarness (make test-rls). Every address is unique per test: a call with p_unheld
// reaches every tenant in the database.
package db_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	eraseInvSig  = "public.erase_invitation_emails(uuid,text,boolean)"
	findInvSig   = "public.invitations_to_erase(uuid,text,boolean)"
	eraseInvGlob = "*_erase_invitation_emails.sql"
)

// eiRequireFunctions fails with the missing-migration reason before any case reads a count.
func eiRequireFunctions(t *testing.T) {
	t.Helper()
	requireHarness(t)
	for _, sig := range []string{eraseInvSig, findInvSig} {
		var reg *string
		if err := h.super.QueryRow(context.Background(), `SELECT to_regprocedure($1)::text`, sig).Scan(&reg); err != nil {
			t.Fatalf("read to_regprocedure %s: %v", sig, err)
		}
		if reg == nil {
			t.Fatalf("function %s does not exist: the erase_invitation_emails migration is not applied", sig)
		}
	}
}

// eiAddr is a lower-case address no other test or seed row holds.
func eiAddr() string { return "ei-" + uuid.NewString()[:8] + "@corp.example.test" }

func eiTenant(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	execSetup(t, h.super, `INSERT INTO tenants (id, name, kind) VALUES ($1, 'Erase invitations test', 'firm')`, id)
	t.Cleanup(func() { dropTenantRows(t, h.super, id) })
	return id
}

// eiInvite plants an invitation as the superuser; a pending row gets the token columns its CHECK needs.
func eiInvite(t *testing.T, tenant, email, status string) string {
	t.Helper()
	id := uuid.NewString()
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		t.Fatalf("test setup: random token: %v", err)
	}
	execSetup(t, h.super,
		`INSERT INTO invitations (id, tenant_id, role, invitee_email, status, token_hash, expires_at, invited_by)
		 VALUES ($1, $2, 'admin', $3, $4, $5, now() + interval '7 days', $6)`,
		id, tenant, email, status, tok, uuid.NewString())
	return id
}

// eiErasure plants the unfinished person_erasures row the function requires.
func eiErasure(t *testing.T, user, email string) {
	t.Helper()
	execSetup(t, h.super, `INSERT INTO public.person_erasures (user_id, email) VALUES ($1, $2)`, user, email)
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM public.person_erasures WHERE user_id = $1`, user)
	})
}

func eiExists(t *testing.T, id string) bool {
	t.Helper()
	return mustCount(t, h.super, `SELECT count(*) FROM invitations WHERE id = $1`, id) == 1
}

// eiAssertRows fails on any row whose presence differs from want; ids are labelled for the message.
func eiAssertRows(t *testing.T, what string, want map[string]bool, ids map[string]string) {
	t.Helper()
	for label, id := range ids {
		if got := eiExists(t, id); got != want[label] {
			t.Errorf("%s: invitation %q present = %v, want %v", what, label, got, want[label])
		}
	}
}

// eiCall runs the function as invoice_app on a connection with no tenant bound. user and email may be nil.
func eiCall(pool *pgxpool.Pool, user, email any, unheld bool) (int, error) {
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var n int
	if err := tx.QueryRow(ctx, `SELECT public.erase_invitation_emails($1::uuid, $2::text, $3::boolean)`, user, email, unheld).Scan(&n); err != nil {
		return 0, err
	}
	return n, tx.Commit(ctx)
}

func eiErase(t *testing.T, user, email any, unheld bool) int {
	t.Helper()
	n, err := eiCall(h.app, user, email, unheld)
	if err != nil {
		t.Fatalf("erase_invitation_emails as invoice_app: %v", err)
	}
	return n
}

func TestRLS_EraseInvitationEmailsIsOwnedAndGrantedNarrowly(t *testing.T) {
	eiRequireFunctions(t)
	ctx := context.Background()

	for _, c := range []struct {
		sig, owner, volatile, grantee, result string
	}{
		{eraseInvSig, "invoice_migrator", "v", "invoice_app", "integer"},
		{findInvSig, "auth_hook_reader", "s", "invoice_migrator", "TABLE(tenant_id uuid, invitation_id uuid)"},
	} {
		s, ok := readProc(t, c.sig)
		if !ok {
			t.Fatalf("function %s does not exist", c.sig)
		}
		if s.owner != c.owner {
			t.Errorf("%s: owner = %q, want %q", c.sig, s.owner, c.owner)
		}
		if !s.secdef {
			t.Errorf("%s: prosecdef = false, want SECURITY DEFINER", c.sig)
		}
		if s.volatile != c.volatile {
			t.Errorf("%s: provolatile = %q, want %q", c.sig, s.volatile, c.volatile)
		}
		if want := []string{`search_path=""`}; !reflect.DeepEqual(s.config, want) {
			t.Errorf("%s: proconfig = %q, want exactly %q", c.sig, s.config, want)
		}
		if s.publicAny {
			t.Errorf("%s: PUBLIC holds EXECUTE (or proacl is the NULL default), want revoked", c.sig)
		}
		wantACL := []string{c.owner + "=EXECUTE"}
		if c.grantee != c.owner {
			wantACL = append(wantACL, c.grantee+"=EXECUTE")
		}
		sort.Strings(wantACL)
		if !reflect.DeepEqual(s.acl, wantACL) {
			t.Errorf("%s: ACL = %v, want exactly %v", c.sig, s.acl, wantACL)
		}
		var result string
		if err := h.super.QueryRow(ctx, `SELECT pg_get_function_result(to_regprocedure($1))`, c.sig).Scan(&result); err != nil {
			t.Fatalf("read the result type of %s: %v", c.sig, err)
		}
		if result != c.result {
			t.Errorf("%s: result = %q, want %q", c.sig, result, c.result)
		}
		for _, role := range []string{"invoice_app", "invoice_migrator", "invoice_tenant_reader", "invoice_gateway", "supabase_auth_admin"} {
			var got bool
			if err := h.super.QueryRow(ctx, `SELECT has_function_privilege($1, to_regprocedure($2), 'EXECUTE')`, role, c.sig).Scan(&got); err != nil {
				t.Fatalf("has_function_privilege(%s, %s): %v", role, c.sig, err)
			}
			if want := role == c.grantee || role == c.owner; got != want {
				t.Errorf("%s: %s EXECUTE = %v, want %v", c.sig, role, got, want)
			}
		}
	}

	id := uuid.NewString()
	asGateway := func(call string) error {
		tx, err := h.super.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE invoice_gateway`); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, call, id, "x@corp.example.test", true)
		return err
	}
	const callErase = `SELECT public.erase_invitation_emails($1::uuid, $2, $3)`
	const callFind = `SELECT * FROM public.invitations_to_erase($1::uuid, $2, $3)`
	_, errReader := h.reader.Exec(ctx, callErase, id, "x@corp.example.test", true)
	assertPgRefusal(t, "erase as invoice_tenant_reader", errReader, "42501", "permission denied for function erase_invitation_emails")
	assertPgRefusal(t, "erase as invoice_gateway", asGateway(callErase), "42501", "permission denied for function erase_invitation_emails")
	_, errApp := h.app.Exec(ctx, callFind, id, "x@corp.example.test", true)
	assertPgRefusal(t, "finder as invoice_app", errApp, "42501", "permission denied for function invitations_to_erase")
}

func TestRLS_EraseInvitationEmailsDeletesTheAcceptedInvitationOfThePersonsWorkspace(t *testing.T) {
	eiRequireFunctions(t)
	t1, t2 := eiTenant(t), eiTenant(t)
	user, addr := uuid.NewString(), eiAddr()
	seedMemberRow(t, t1, user, "admin", "removed", nil, nil)
	eiErasure(t, user, addr)
	ids := map[string]string{"t1": eiInvite(t, t1, addr, "accepted"), "t2": eiInvite(t, t2, addr, "accepted")}

	if n := eiErase(t, user, addr, false); n != 1 {
		t.Errorf("erase returned %d, want 1", n)
	}
	eiAssertRows(t, "after the erase", map[string]bool{"t1": false, "t2": true}, ids)
}

func TestRLS_EraseInvitationEmailsDeletesForEveryMembershipStatus(t *testing.T) {
	eiRequireFunctions(t)
	for _, status := range []string{"active", "suspended", "invited", "removed"} {
		t.Run(status, func(t *testing.T) {
			tenant, user, addr := eiTenant(t), uuid.NewString(), eiAddr()
			seedMemberRow(t, tenant, user, "admin", status, nil, nil)
			eiErasure(t, user, addr)
			id := eiInvite(t, tenant, addr, "accepted")

			if n := eiErase(t, user, addr, false); n != 1 {
				t.Errorf("erase returned %d, want 1", n)
			}
			if eiExists(t, id) {
				t.Errorf("the accepted invitation survives a %s membership, want it deleted", status)
			}
		})
	}
}

func TestRLS_EraseInvitationEmailsRefusesWithoutARunningErasure(t *testing.T) {
	eiRequireFunctions(t)
	other := uuid.NewString()
	for _, c := range []struct {
		name  string
		plant func(t *testing.T, user, addr string)
	}{
		{"no erasure row", func(*testing.T, string, string) {}},
		{"finished row", func(t *testing.T, user, _ string) {
			execSetup(t, h.super, `INSERT INTO public.person_erasures (user_id, finished_at) VALUES ($1, now())`, user)
			t.Cleanup(func() {
				_, _ = h.super.Exec(context.Background(), `DELETE FROM public.person_erasures WHERE user_id = $1`, user)
			})
		}},
		{"row of another user", func(t *testing.T, _, addr string) { eiErasure(t, other, addr) }},
		{"row with another address", func(t *testing.T, user, _ string) { eiErasure(t, user, eiAddr()) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			t1, t2 := eiTenant(t), eiTenant(t)
			user, addr := uuid.NewString(), eiAddr()
			seedMemberRow(t, t1, user, "admin", "active", nil, nil)
			c.plant(t, user, addr)
			ids := map[string]string{"accepted": eiInvite(t, t1, addr, "accepted"), "pending": eiInvite(t, t2, addr, "pending")}

			if n := eiErase(t, user, addr, true); n != 0 {
				t.Errorf("erase returned %d, want 0", n)
			}
			eiAssertRows(t, "after the refused erase", map[string]bool{"accepted": true, "pending": true}, ids)
		})
		t.Cleanup(func() {
			_, _ = h.super.Exec(context.Background(), `DELETE FROM public.person_erasures WHERE user_id = $1`, other)
		})
	}

	t.Run("a running erasure deletes the same rows", func(t *testing.T) {
		t1, t2 := eiTenant(t), eiTenant(t)
		user, addr := uuid.NewString(), eiAddr()
		seedMemberRow(t, t1, user, "admin", "active", nil, nil)
		eiErasure(t, user, addr)
		ids := map[string]string{"accepted": eiInvite(t, t1, addr, "accepted"), "pending": eiInvite(t, t2, addr, "pending")}

		if n := eiErase(t, user, addr, true); n != 2 {
			t.Errorf("erase returned %d, want 2", n)
		}
		eiAssertRows(t, "after the erase", map[string]bool{"accepted": false, "pending": false}, ids)
	})
}

func TestRLS_EraseInvitationEmailsKeepsAnotherMembersAcceptedInvitation(t *testing.T) {
	eiRequireFunctions(t)
	t1, t2 := eiTenant(t), eiTenant(t)
	user, addr, otherAddr := uuid.NewString(), eiAddr(), eiAddr()
	seedMemberRow(t, t1, user, "admin", "removed", nil, nil)
	seedMemberRow(t, t1, uuid.NewString(), "admin", "active", nil, nil)
	eiErasure(t, user, addr)
	ids := map[string]string{
		"own":          eiInvite(t, t1, addr, "accepted"),
		"otherMember":  eiInvite(t, t1, otherAddr, "accepted"),
		"noMembership": eiInvite(t, t2, addr, "accepted"),
	}

	if n := eiErase(t, user, addr, true); n != 1 {
		t.Errorf("erase returned %d, want 1", n)
	}
	eiAssertRows(t, "after the erase", map[string]bool{"own": false, "otherMember": true, "noMembership": true}, ids)
}

func TestRLS_EraseInvitationEmailsPendingAndRevokedFollowTheUnheldFlag(t *testing.T) {
	eiRequireFunctions(t)
	t1, t2 := eiTenant(t), eiTenant(t)
	user, addr := uuid.NewString(), eiAddr()
	eiErasure(t, user, addr)
	ids := map[string]string{
		"t1 pending": eiInvite(t, t1, addr, "pending"), "t1 revoked": eiInvite(t, t1, addr, "revoked"),
		"t2 pending": eiInvite(t, t2, addr, "pending"), "t2 revoked": eiInvite(t, t2, addr, "revoked"),
	}
	keep := map[string]bool{"t1 pending": true, "t1 revoked": true, "t2 pending": true, "t2 revoked": true}

	if n := eiErase(t, user, addr, false); n != 0 {
		t.Errorf("erase with p_unheld false returned %d, want 0", n)
	}
	eiAssertRows(t, "p_unheld false", keep, ids)

	if n := eiErase(t, user, addr, true); n != 4 {
		t.Errorf("erase with p_unheld true returned %d, want 4", n)
	}
	eiAssertRows(t, "p_unheld true", map[string]bool{}, ids)
}

func TestRLS_EraseInvitationEmailsMatchesAddressCaseAndSpaces(t *testing.T) {
	eiRequireFunctions(t)
	t1, t2 := eiTenant(t), eiTenant(t)
	user, addr := uuid.NewString(), eiAddr()
	seedMemberRow(t, t1, user, "admin", "active", nil, nil)
	eiErasure(t, user, addr)
	stored := " " + strings.ToUpper(addr) + " "
	ids := map[string]string{"accepted": eiInvite(t, t1, stored, "accepted"), "pending": eiInvite(t, t2, stored, "pending")}

	if n := eiErase(t, user, "  "+addr+" ", true); n != 2 {
		t.Errorf("erase returned %d, want 2", n)
	}
	eiAssertRows(t, "after the erase", map[string]bool{}, ids)
}

func TestRLS_EraseInvitationEmailsMatchesTheCalledAddressInAnyCase(t *testing.T) {
	eiRequireFunctions(t)
	t1, t2 := eiTenant(t), eiTenant(t)
	user, addr := uuid.NewString(), eiAddr()
	seedMemberRow(t, t1, user, "admin", "active", nil, nil)
	eiErasure(t, user, addr)
	ids := map[string]string{"accepted": eiInvite(t, t1, addr, "accepted"), "pending": eiInvite(t, t2, addr, "pending")}

	if n := eiErase(t, user, " "+strings.ToUpper(addr)+" ", true); n != 2 {
		t.Errorf("erase returned %d, want 2", n)
	}
	eiAssertRows(t, "after the erase", map[string]bool{}, ids)
}

func TestRLS_EraseInvitationEmailsCallerBoundToAnotherTenantReachesOnlyTheErasedAddress(t *testing.T) {
	eiRequireFunctions(t)
	ctx := context.Background()
	t1, t2, t3 := eiTenant(t), eiTenant(t), eiTenant(t)
	user, addr, other := uuid.NewString(), eiAddr(), eiAddr()
	seedMemberRow(t, t1, user, "admin", "active", nil, nil)
	eiErasure(t, user, addr)
	ids := map[string]string{
		"t1 accepted": eiInvite(t, t1, addr, "accepted"), "t2 pending": eiInvite(t, t2, addr, "pending"),
		"t3 pending other": eiInvite(t, t3, other, "pending"), "t3 accepted other": eiInvite(t, t3, other, "accepted"),
		"t3 accepted address": eiInvite(t, t3, addr, "accepted"),
	}

	var n int
	err := db.WithinTenantTx(ctx, h.app, t3, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT public.erase_invitation_emails($1::uuid, $2, true)`, user, addr).Scan(&n)
	})
	if err != nil {
		t.Fatalf("call bound to the unrelated tenant: %v", err)
	}
	if n != 2 {
		t.Errorf("erase returned %d, want 2", n)
	}
	eiAssertRows(t, "after the erase", map[string]bool{
		"t1 accepted": false, "t2 pending": false, "t3 pending other": true, "t3 accepted other": true, "t3 accepted address": true,
	}, ids)
	if again := eiErase(t, user, addr, true); again != 0 {
		t.Errorf("second erase returned %d, want 0", again)
	}
}

func TestRLS_EraseInvitationEmailsRefusesNothingOnNullOrBlankInput(t *testing.T) {
	eiRequireFunctions(t)
	t1, t2 := eiTenant(t), eiTenant(t)
	user, addr := uuid.NewString(), eiAddr()
	seedMemberRow(t, t1, user, "admin", "active", nil, nil)
	eiErasure(t, user, addr)
	ids := map[string]string{"accepted": eiInvite(t, t1, addr, "accepted"), "pending": eiInvite(t, t2, addr, "pending")}
	keep := map[string]bool{"accepted": true, "pending": true}

	for _, c := range []struct {
		name  string
		user  any
		email any
	}{
		{"NULL user", nil, addr},
		{"NULL email", user, nil},
		{"empty email", user, ""},
		{"blank email", user, "   "},
	} {
		if n := eiErase(t, c.user, c.email, true); n != 0 {
			t.Errorf("%s: erase returned %d, want 0", c.name, n)
		}
		eiAssertRows(t, c.name, keep, ids)
	}

	if n := eiErase(t, user, addr, true); n != 2 {
		t.Errorf("erase with valid input returned %d, want 2", n)
	}
	eiAssertRows(t, "valid input", map[string]bool{}, ids)
}

func TestRLS_EraseInvitationEmailsRestoresTheTenantBinding(t *testing.T) {
	eiRequireFunctions(t)
	ctx := context.Background()
	t1, t2 := eiTenant(t), eiTenant(t)
	user, addr := uuid.NewString(), eiAddr()
	seedMemberRow(t, t1, user, "admin", "active", nil, nil)
	eiErasure(t, user, addr)
	// The pending row in t2 forces the function to bind a second tenant before it restores.
	ids := map[string]string{"accepted": eiInvite(t, t1, addr, "accepted"), "pending": eiInvite(t, t2, addr, "pending")}

	var bound, count string
	err := db.WithinTenantTx(ctx, h.app, t1, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT public.erase_invitation_emails($1::uuid, $2, true)`, user, addr).Scan(&n); err != nil {
			return err
		}
		if n != 2 {
			return fmt.Errorf("erase returned %d, want 2", n)
		}
		return tx.QueryRow(ctx, `SELECT current_setting('app.current_tenant', true), (SELECT count(*) FROM public.invitations)::text`).Scan(&bound, &count)
	})
	if err != nil {
		t.Fatalf("bound transaction: %v", err)
	}
	if bound != t1 {
		t.Errorf("app.current_tenant after the call = %q, want the bound tenant %q", bound, t1)
	}
	if count != "0" {
		t.Errorf("invitations visible to the restored tenant = %s, want 0", count)
	}
	eiAssertRows(t, "bound call", map[string]bool{}, ids)

	// Unset stays unset: a fresh connection state reads '' or NULL, never a tenant id.
	user2, addr2 := uuid.NewString(), eiAddr()
	eiErasure(t, user2, addr2)
	pending := eiInvite(t, t2, addr2, "pending")
	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var before, after *string
	if err := tx.QueryRow(ctx, `SELECT nullif(current_setting('app.current_tenant', true), '')`).Scan(&before); err != nil {
		t.Fatalf("read the binding before: %v", err)
	}
	if before != nil {
		t.Fatalf("test setup: the pool connection is bound to %q before the call", *before)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT public.erase_invitation_emails($1::uuid, $2, true)`, user2, addr2).Scan(&n); err != nil {
		t.Fatalf("unbound call: %v", err)
	}
	if n != 1 {
		t.Errorf("unbound call returned %d, want 1", n)
	}
	if err := tx.QueryRow(ctx, `SELECT nullif(current_setting('app.current_tenant', true), '')`).Scan(&after); err != nil {
		t.Fatalf("read the binding after: %v", err)
	}
	if after != nil {
		t.Errorf("app.current_tenant after the unbound call = %q, want unset", *after)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if eiExists(t, pending) {
		t.Errorf("the pending invitation survives the unbound call, want it deleted")
	}
}

// A pooled connection that once held a binding reads an empty string, not NULL; only a fresh connection reads NULL.
func TestRLS_EraseInvitationEmailsLeavesAFreshConnectionUnbound(t *testing.T) {
	eiRequireFunctions(t)
	ctx := context.Background()
	t1, t2 := eiTenant(t), eiTenant(t)
	user, addr := uuid.NewString(), eiAddr()
	seedMemberRow(t, t1, user, "admin", "active", nil, nil)
	eiErasure(t, user, addr)
	ids := map[string]string{"accepted": eiInvite(t, t1, addr, "accepted"), "pending": eiInvite(t, t2, addr, "pending")}

	conn, err := pgx.ConnectConfig(ctx, h.app.Config().ConnConfig)
	if err != nil {
		t.Fatalf("open a fresh invoice_app connection: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var before, after *string
	if err := tx.QueryRow(ctx, `SELECT current_setting('app.current_tenant', true)`).Scan(&before); err != nil {
		t.Fatalf("read the binding before: %v", err)
	}
	if before != nil {
		t.Fatalf("test setup: a fresh connection reads %q, want NULL", *before)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT public.erase_invitation_emails($1::uuid, $2, true)`, user, addr).Scan(&n); err != nil {
		t.Fatalf("call: %v", err)
	}
	if n != 2 {
		t.Errorf("erase returned %d, want 2", n)
	}
	if err := tx.QueryRow(ctx, `SELECT nullif(current_setting('app.current_tenant', true), '')`).Scan(&after); err != nil {
		t.Fatalf("read the binding after: %v", err)
	}
	if after != nil {
		t.Errorf("app.current_tenant after the call = %q, want unset", *after)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	eiAssertRows(t, "after the erase", map[string]bool{}, ids)
}

func TestRLS_EraseInvitationEmailsCascadesInvitationClients(t *testing.T) {
	eiRequireFunctions(t)
	tenant, user, addr := eiTenant(t), uuid.NewString(), eiAddr()
	seedMemberRow(t, tenant, user, "admin", "active", nil, nil)
	eiErasure(t, user, addr)
	entity := uuid.NewString()
	execSetup(t, h.super, `INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, 'Erase invitations client')`, entity, tenant)
	inv := eiInvite(t, tenant, addr, "accepted")
	execSetup(t, h.super, `INSERT INTO invitation_clients (tenant_id, invitation_id, entity_id) VALUES ($1, $2, $3)`, tenant, inv, entity)
	if n := mustCount(t, h.super, `SELECT count(*) FROM invitation_clients WHERE invitation_id = $1`, inv); n != 1 {
		t.Fatalf("test setup: %d invitation_clients rows, want 1", n)
	}

	if n := eiErase(t, user, addr, false); n != 1 {
		t.Errorf("erase returned %d, want 1", n)
	}
	if eiExists(t, inv) {
		t.Errorf("the invitation survives, want it deleted")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM invitation_clients WHERE invitation_id = $1`, inv); n != 0 {
		t.Errorf("invitation_clients rows after the erase = %d, want 0", n)
	}
}

func TestRLS_EraseInvitationEmailsLeavesADirectDeleteRefused(t *testing.T) {
	eiRequireFunctions(t)
	ctx := context.Background()
	tenant, user, addr := eiTenant(t), uuid.NewString(), eiAddr()
	seedMemberRow(t, tenant, user, "admin", "active", nil, nil)
	eiErasure(t, user, addr)
	inv := eiInvite(t, tenant, addr, "accepted")

	err := db.WithinTenantTx(ctx, h.app, tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM invitations WHERE id = $1`, inv)
		return err
	})
	assertPgRefusal(t, "direct DELETE as invoice_app", err, "42501", "permission denied for table invitations")
	if !eiExists(t, inv) {
		t.Fatalf("the invitation is gone after the refused direct DELETE")
	}

	if n := eiErase(t, user, addr, false); n != 1 {
		t.Errorf("erase returned %d, want 1", n)
	}
	if eiExists(t, inv) {
		t.Errorf("the function did not delete the invitation the direct DELETE could not")
	}
}

// Drives the shipped Down and Up through goose. The migration is the newest, so no other migration rolls back with it.
func TestRLS_EraseInvitationEmailsDownThenUp(t *testing.T) {
	eiRequireFunctions(t)
	ctx := context.Background()
	version := migrationVersion(t, eraseInvGlob)
	provider := staffMigrationProvider(t)
	restoreMigrationOnCleanup(t, provider, version)

	present := func() (erase, find bool) {
		t.Helper()
		if err := h.super.QueryRow(ctx,
			`SELECT to_regprocedure($1) IS NOT NULL, to_regprocedure($2) IS NOT NULL`, eraseInvSig, findInvSig,
		).Scan(&erase, &find); err != nil {
			t.Fatalf("read function presence: %v", err)
		}
		return erase, find
	}
	if erase, find := present(); !erase || !find {
		t.Fatalf("before Down: erase = %v, finder = %v, want both present", erase, find)
	}

	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("Down of %d: %v", version, err)
	}
	if erase, find := present(); erase || find {
		t.Errorf("after Down: erase = %v, finder = %v, want both absent", erase, find)
	}

	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("Up of %d: %v", version, err)
	}
	if erase, find := present(); !erase || !find {
		t.Errorf("after Up: erase = %v, finder = %v, want both present", erase, find)
	}
	if s, ok := readProc(t, findInvSig); !ok || s.owner != "auth_hook_reader" {
		t.Errorf("after Up: finder owner = %q (present %v), want auth_hook_reader", s.owner, ok)
	}
	if s, ok := readProc(t, eraseInvSig); !ok || s.owner != "invoice_migrator" {
		t.Errorf("after Up: erase owner = %q (present %v), want invoice_migrator", s.owner, ok)
	}
}
