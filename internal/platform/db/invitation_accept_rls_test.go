package db_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	byTokenSig       = "public.invitation_by_token(text)"
	acceptSig        = "public.accept_invitation(uuid, text, uuid, text)"
	acceptCall       = `SELECT invitation_id::text, role FROM public.accept_invitation($1::uuid, $2::text, $3::uuid, $4::text)`
	lookupCall       = `SELECT invitation_id::text, tenant_id::text, workspace, role, email FROM public.invitation_by_token($1::text)`
	invitedAddress   = "tunde@obi.test"
	notValidName     = "invitation_not_valid"
	emailMismatchKey = "invitation_email_mismatch"
)

type lookupRow struct{ id, tenantID, workspace, role, email string }

func newToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("read random bytes: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashOf(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func newNamedTenant(t *testing.T, name string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := h.super.Exec(context.Background(), `INSERT INTO tenants (id, name) VALUES ($1, $2)`, id, name); err != nil {
		t.Fatalf("seed tenant %q: %v", name, err)
	}
	cleanupTenant(t, id)
	return id
}

// seedAcceptInvite writes a pending invite for tenant whose hash is sha256(token), expiring in 7 days.
func seedAcceptInvite(t *testing.T, tenant, role, email, token string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO invitations (id, tenant_id, role, invitee_email, token_hash, expires_at, invited_by)
		 VALUES ($1, $2, $3, $4, $5, now() + interval '7 days', $6)`,
		id, tenant, role, email, hashOf(token), uuid.NewString(),
	); err != nil {
		t.Fatalf("seed invitation: %v", err)
	}
	return id
}

func setInviteState(t *testing.T, id, set string) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(), `UPDATE invitations SET `+set+` WHERE id = $1`, id); err != nil {
		t.Fatalf("set invite state %q: %v", set, err)
	}
}

func inviteStatus(t *testing.T, id string) string {
	t.Helper()
	var s string
	if err := h.super.QueryRow(context.Background(), `SELECT status FROM invitations WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("read invite status: %v", err)
	}
	return s
}

// inAppTx runs fn as invoice_app. An empty guc leaves app.current_tenant unset; fn must only read then.
func inAppTx(ctx context.Context, guc string, fn func(pgx.Tx) error) error {
	if guc != "" {
		return db.WithinTenantTx(ctx, h.app, guc, fn)
	}
	tx, err := h.app.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return fn(tx)
}

func lookupAs(t *testing.T, guc, token string) []lookupRow {
	t.Helper()
	var out []lookupRow
	err := inAppTx(context.Background(), guc, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), lookupCall, token)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r lookupRow
			if err := rows.Scan(&r.id, &r.tenantID, &r.workspace, &r.role, &r.email); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("invitation_by_token as invoice_app (GUC %q): %v", guc, err)
	}
	return out
}

func acceptAs(ctx context.Context, guc, tenant, token, user, email string) (id, role string, err error) {
	err = db.WithinTenantTx(ctx, h.app, guc, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, acceptCall, tenant, token, user, email).Scan(&id, &role)
	})
	return id, role, err
}

func assertAcceptRefusal(t *testing.T, what string, err error, code, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("%s: want %s %q, got %v", what, code, constraint, err)
	}
	if pgErr.Code != code || pgErr.ConstraintName != constraint {
		t.Fatalf("%s: want %s on %q, got %s on %q (%s)", what, code, constraint, pgErr.Code, pgErr.ConstraintName, pgErr.Message)
	}
}

func TestRLS_InvitationByTokenNamesThePendingInvite(t *testing.T) {
	requireHarness(t)
	a := newNamedTenant(t, "Obi Partners")
	b := newNamedTenant(t, "Other Firm")
	token := newToken(t)
	id := seedAcceptInvite(t, a, "reviewer", invitedAddress, token)
	want := lookupRow{id: id, tenantID: a, workspace: "Obi Partners", role: "reviewer", email: invitedAddress}

	for _, c := range []struct{ name, guc string }{
		{"guc unset", ""},
		{"guc nil uuid", uuid.Nil.String()},
		{"guc another tenant", b},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := lookupAs(t, c.guc, token)
			if len(got) != 1 {
				t.Fatalf("rows = %d (%v), want 1", len(got), got)
			}
			if got[0] != want {
				t.Errorf("row = %+v, want %+v", got[0], want)
			}
		})
	}
}

func TestRLS_InvitationByTokenIgnoresUnusableInvites(t *testing.T) {
	requireHarness(t)

	for _, c := range []struct {
		name   string
		mutate func(t *testing.T, id string, token *string)
	}{
		{"unknown token", func(t *testing.T, _ string, token *string) { *token = newToken(t) }},
		{"accepted", func(t *testing.T, id string, _ *string) { setInviteState(t, id, `status = 'accepted'`) }},
		{"revoked", func(t *testing.T, id string, _ *string) { setInviteState(t, id, `status = 'revoked'`) }},
		{"expired one second ago", func(t *testing.T, id string, _ *string) {
			setInviteState(t, id, `expires_at = now() - interval '1 second'`)
		}},
		{"old token of a resent invite", func(t *testing.T, id string, _ *string) {
			if _, err := h.super.Exec(context.Background(), `UPDATE invitations SET token_hash = $2 WHERE id = $1`, id, hashOf(newToken(t))); err != nil {
				t.Fatalf("replace the hash: %v", err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tenant := newNamedTenant(t, "Obi Partners")
			token := newToken(t)
			id := seedAcceptInvite(t, tenant, "reviewer", invitedAddress, token)
			if got := lookupAs(t, "", token); len(got) != 1 {
				t.Fatalf("control: live invite rows = %d, want 1", len(got))
			}

			c.mutate(t, id, &token)

			if got := lookupAs(t, "", token); len(got) != 0 {
				t.Errorf("rows = %d (%v), want 0", len(got), got)
			}
		})
	}
}

func TestRLS_InvitationByTokenExpiryEdge(t *testing.T) {
	requireHarness(t)

	for _, c := range []struct {
		name, expires string
		want          int
	}{
		{"one second ahead", `now() + interval '1 second'`, 1},
		{"one second past", `now() - interval '1 second'`, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			tenant := newNamedTenant(t, "Obi Partners")
			token := newToken(t)
			id := seedAcceptInvite(t, tenant, "reviewer", invitedAddress, token)
			setInviteState(t, id, `expires_at = `+c.expires)

			if got := lookupAs(t, "", token); len(got) != c.want {
				t.Errorf("rows = %d (%v), want %d", len(got), got, c.want)
			}
		})
	}
}

type procShape struct {
	owner     string
	secdef    bool
	config    []string
	volatile  string
	acl       []string
	publicAny bool
}

// readProc returns false when the function does not exist.
func readProc(t *testing.T, sig string) (procShape, bool) {
	t.Helper()
	var s procShape
	err := h.super.QueryRow(context.Background(), `
		SELECT pg_get_userbyid(p.proowner), p.prosecdef, coalesce(p.proconfig, '{}'), p.provolatile::text,
		       coalesce((SELECT array_agg(pg_get_userbyid(a.grantee) || '=' || a.privilege_type ORDER BY 1)
		                 FROM aclexplode(p.proacl) a WHERE a.grantee <> 0), '{}'),
		       p.proacl IS NULL OR EXISTS (SELECT 1 FROM aclexplode(p.proacl) a WHERE a.grantee = 0)
		  FROM pg_proc p WHERE p.oid = to_regprocedure($1)`, sig,
	).Scan(&s.owner, &s.secdef, &s.config, &s.volatile, &s.acl, &s.publicAny)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, false
	}
	if err != nil {
		t.Fatalf("read pg_proc for %s: %v", sig, err)
	}
	sort.Strings(s.acl)
	return s, true
}

func assertExecuteOnlyForApp(t *testing.T, sig string, s procShape, owner string) {
	t.Helper()
	if s.publicAny {
		t.Errorf("PUBLIC holds EXECUTE (or proacl is the NULL default), want revoked")
	}
	want := []string{owner + "=EXECUTE", "invoice_app=EXECUTE"}
	sort.Strings(want)
	if !reflect.DeepEqual(s.acl, want) {
		t.Errorf("ACL = %v, want exactly %v", s.acl, want)
	}
	for _, c := range []struct {
		role string
		want bool
	}{{"invoice_app", true}, {"invoice_tenant_reader", false}, {"supabase_auth_admin", false}} {
		var got bool
		if err := h.super.QueryRow(context.Background(), `SELECT has_function_privilege($1, to_regprocedure($2), 'EXECUTE')`, c.role, sig).Scan(&got); err != nil {
			t.Fatalf("has_function_privilege(%s): %v", c.role, err)
		}
		if got != c.want {
			t.Errorf("%s EXECUTE on %s = %v, want %v", c.role, sig, got, c.want)
		}
	}
}

func TestRLS_InvitationByTokenIsOwnedAndGrantedNarrowly(t *testing.T) {
	requireHarness(t)

	if s, ok := readProc(t, byTokenSig); !ok {
		t.Errorf("function %s does not exist", byTokenSig)
	} else {
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
		assertExecuteOnlyForApp(t, byTokenSig, s, "auth_hook_reader")
	}

	for _, want := range []struct{ table, policy string }{
		{"invitations", "invitation_token_lookup"},
		{"tenants", "invitation_workspace_lookup"},
	} {
		rows, err := h.super.Query(context.Background(),
			`SELECT policyname, cmd, roles::text[], qual FROM pg_policies
			  WHERE schemaname = 'public' AND tablename = $1 AND 'auth_hook_reader' = ANY (roles)`, want.table)
		if err != nil {
			t.Fatalf("read pg_policies for %s: %v", want.table, err)
		}
		type pol struct {
			name, cmd, qual string
			roles           []string
		}
		var got []pol
		for rows.Next() {
			var p pol
			if err := rows.Scan(&p.name, &p.cmd, &p.roles, &p.qual); err != nil {
				rows.Close()
				t.Fatalf("scan pg_policies: %v", err)
			}
			got = append(got, p)
		}
		rows.Close()
		wantPol := pol{want.policy, "SELECT", "true", []string{"auth_hook_reader"}}
		if len(got) != 1 || !reflect.DeepEqual(got[0], wantPol) {
			t.Errorf("auth_hook_reader policies on %s = %+v, want exactly [%+v]", want.table, got, wantPol)
		}
	}
}

// The control keeps the isolation reads from passing on a missing lookup.
func TestRLS_InvitationLookupPoliciesDoNotWidenTheApp(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	a := newNamedTenant(t, "Obi Partners")
	b := newNamedTenant(t, "Other Firm")
	token := newToken(t)
	seedAcceptInvite(t, a, "reviewer", invitedAddress, token)
	if got := lookupAs(t, b, token); len(got) != 1 {
		t.Fatalf("control: lookup under tenant B rows = %d, want 1", len(got))
	}

	for _, c := range []struct {
		scope string
		guc   string
		want  int
	}{{"tenant B", b, 0}, {"tenant A (control)", a, 1}} {
		err := db.WithinTenantTx(ctx, h.app, c.guc, func(tx pgx.Tx) error {
			if n := mustCount(t, tx, `SELECT count(*) FROM invitations WHERE token_hash = $1`, hashOf(token)); n != c.want {
				t.Errorf("%s: invitations by hash = %d, want %d", c.scope, n, c.want)
			}
			if n := mustCount(t, tx, `SELECT count(*) FROM tenants WHERE id = $1`, a); n != c.want {
				t.Errorf("%s: tenant A rows = %d, want %d", c.scope, n, c.want)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", c.scope, err)
		}
	}
}

func TestRLS_AcceptInvitationWritesTheMembership(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	a := newNamedTenant(t, "Obi Partners")
	token := newToken(t)
	inviteID := seedAcceptInvite(t, a, "reviewer", invitedAddress, token)
	user := uuid.NewString()

	gotID, gotRole, err := acceptAs(ctx, a, a, token, user, " Tunde@Obi.test ")
	if err != nil {
		t.Fatalf("accept_invitation under GUC A: want success, got %v", err)
	}
	if gotID != inviteID || gotRole != "reviewer" {
		t.Errorf("returned (invitation_id, role) = (%s, %s), want (%s, reviewer)", gotID, gotRole, inviteID)
	}

	if n := membershipCount(t, user); n != 1 {
		t.Fatalf("memberships for the user = %d, want 1", n)
	}
	var tenant, role, status string
	var display *string
	var email *string
	if err := h.super.QueryRow(ctx,
		`SELECT tenant_id::text, role, status, display_name, email FROM memberships WHERE user_id = $1`, user,
	).Scan(&tenant, &role, &status, &display, &email); err != nil {
		t.Fatalf("read membership: %v", err)
	}
	if tenant != a || role != "reviewer" || status != "active" {
		t.Errorf("membership (tenant, role, status) = (%s, %s, %s), want (%s, reviewer, active)", tenant, role, status, a)
	}
	if display != nil {
		t.Errorf("display_name = %q, want NULL", *display)
	}
	if email == nil || *email != invitedAddress {
		t.Errorf("email = %v, want the invited address %q", email, invitedAddress)
	}
	if s := inviteStatus(t, inviteID); s != "accepted" {
		t.Errorf("invite status = %q, want accepted", s)
	}
}

func TestRLS_AcceptInvitationRefusals(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	type fixture struct {
		a, b, c, user string
		token, tokenB string
		inviteID      string
		inviteBID     string
	}
	for _, c := range []struct {
		name       string
		setup      func(t *testing.T, f *fixture)
		guc        func(f *fixture) string
		token      func(f *fixture) string
		email      string
		code       string
		constraint string
		msg        string
		wantStatus string
	}{
		{name: "guc is another tenant", guc: func(f *fixture) string { return f.b },
			code: "42501", msg: "row-level security", email: invitedAddress},
		{name: "unknown token", token: func(*fixture) string { return "no-such-token" },
			code: "P0002", constraint: notValidName, email: invitedAddress},
		{name: "accepted invite", setup: func(t *testing.T, f *fixture) { setInviteState(t, f.inviteID, `status = 'accepted'`) },
			code: "P0002", constraint: notValidName, email: invitedAddress, wantStatus: "accepted"},
		{name: "revoked invite", setup: func(t *testing.T, f *fixture) { setInviteState(t, f.inviteID, `status = 'revoked'`) },
			code: "P0002", constraint: notValidName, email: invitedAddress, wantStatus: "revoked"},
		{name: "expired invite", setup: func(t *testing.T, f *fixture) {
			setInviteState(t, f.inviteID, `expires_at = now() - interval '1 second'`)
		}, code: "P0002", constraint: notValidName, email: invitedAddress},
		{name: "token of tenant B's invite under GUC A", token: func(f *fixture) string { return f.tokenB },
			code: "P0002", constraint: notValidName, email: invitedAddress},
		{name: "user is an active admin elsewhere", setup: func(t *testing.T, f *fixture) { seedHookMembership(t, f.c, f.user, "active") },
			code: "23505", constraint: guardConstraint, email: invitedAddress},
		{name: "user is suspended elsewhere", setup: func(t *testing.T, f *fixture) { seedHookMembership(t, f.c, f.user, "suspended") },
			code: "23505", constraint: guardConstraint, email: invitedAddress},
		{name: "user is invited elsewhere", setup: func(t *testing.T, f *fixture) { seedHookMembership(t, f.c, f.user, "invited") },
			code: "23505", constraint: guardConstraint, email: invitedAddress},
		{name: "email differs from the invited address", code: "P0001", constraint: emailMismatchKey, email: "other@obi.test"},
		{name: "empty email", code: "P0001", constraint: emailMismatchKey, email: ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fixture{user: uuid.NewString(), token: newToken(t), tokenB: newToken(t)}
			f.a = newNamedTenant(t, "Obi Partners")
			f.b = newNamedTenant(t, "Other Firm")
			f.c = newNamedTenant(t, "Third Firm")
			f.inviteID = seedAcceptInvite(t, f.a, "reviewer", invitedAddress, f.token)
			f.inviteBID = seedAcceptInvite(t, f.b, "reviewer", invitedAddress, f.tokenB)
			if c.setup != nil {
				c.setup(t, f)
			}
			guc, token := f.a, f.token
			if c.guc != nil {
				guc = c.guc(f)
			}
			if c.token != nil {
				token = c.token(f)
			}
			wantStatus := c.wantStatus
			if wantStatus == "" {
				wantStatus = "pending"
			}
			before := membershipCount(t, f.user)

			_, _, err := acceptAs(ctx, guc, f.a, token, f.user, c.email)

			if c.msg != "" {
				assertPgRefusal(t, c.name, err, c.code, c.msg)
			} else {
				assertAcceptRefusal(t, c.name, err, c.code, c.constraint)
			}
			if n := mustCount(t, h.super, `SELECT count(*) FROM memberships WHERE tenant_id = $1 AND user_id = $2`, f.a, f.user); n != 0 {
				t.Errorf("memberships for the user in A = %d, want 0", n)
			}
			if n := membershipCount(t, f.user); n != before {
				t.Errorf("memberships for the user = %d, want %d unchanged", n, before)
			}
			if s := inviteStatus(t, f.inviteID); s != wantStatus {
				t.Errorf("invite status = %q, want %q", s, wantStatus)
			}
			if s := inviteStatus(t, f.inviteBID); s != "pending" {
				t.Errorf("tenant B's invite status = %q, want pending", s)
			}
		})
	}
}

func TestRLS_AcceptInvitationRefusalOrder(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	t.Run("membership elsewhere beats an email mismatch", func(t *testing.T) {
		a, c := newNamedTenant(t, "Obi Partners"), newNamedTenant(t, "Third Firm")
		token := newToken(t)
		inviteID := seedAcceptInvite(t, a, "reviewer", invitedAddress, token)
		user := uuid.NewString()
		seedHookMembership(t, c, user, "suspended")

		_, _, err := acceptAs(ctx, a, a, token, user, "other@obi.test")

		assertAcceptRefusal(t, "suspended elsewhere and a wrong email", err, "23505", guardConstraint)
		if s := inviteStatus(t, inviteID); s != "pending" {
			t.Errorf("invite status = %q, want pending", s)
		}
	})

	t.Run("invalid token beats a membership elsewhere", func(t *testing.T) {
		a, c := newNamedTenant(t, "Obi Partners"), newNamedTenant(t, "Third Firm")
		user := uuid.NewString()
		seedHookMembership(t, c, user, "active")

		_, _, err := acceptAs(ctx, a, a, "no-such-token", user, invitedAddress)

		assertAcceptRefusal(t, "unknown token and a membership elsewhere", err, "P0002", notValidName)
	})
}

func TestRLS_AcceptInvitationSharesTheProvisionLock(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	a := newNamedTenant(t, "Obi Partners")
	token := newToken(t)
	inviteID := seedAcceptInvite(t, a, "reviewer", invitedAddress, token)
	user := uuid.NewString()
	fresh := uuid.NewString()
	cleanupTenant(t, fresh)
	p := newProvisionArgs(fresh)
	p.userID = user

	lock, err := h.super.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire a superuser connection: %v", err)
	}
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1::text, 0))`, user); err != nil {
		lock.Release()
		t.Fatalf("take the identity lock: %v", err)
	}
	var wg sync.WaitGroup
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if _, err := lock.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended($1::text, 0))`, user); err != nil {
			t.Errorf("release the identity lock: %v", err)
		}
		lock.Release()
		wg.Wait()
	}
	defer release()

	acceptErr, provisionErr := make(chan error, 1), make(chan error, 1)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, err := acceptAs(ctx, a, a, token, user, invitedAddress)
		acceptErr <- err
	}()
	go func() {
		defer wg.Done()
		provisionErr <- provisionAs(ctx, h.app, fresh, p)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		waiting := mustCount(t, h.super,
			`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted
			  AND ((classid::bigint << 32) | objid::bigint) = hashtextextended($1::text, 0)`, user)
		if waiting == 2 {
			break
		}
		select {
		case err := <-acceptErr:
			t.Fatalf("accept_invitation returned while the identity lock was held (err %v), want it waiting", err)
		case err := <-provisionErr:
			t.Fatalf("provision_workspace returned while the identity lock was held (err %v), want it waiting", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("advisory requests waiting on the identity key = %d, want 2", waiting)
		}
		time.Sleep(20 * time.Millisecond)
	}

	release()
	errs := []error{<-acceptErr, <-provisionErr}
	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
			continue
		}
		assertGuardRefusal(t, "the loser", err)
	}
	if wins != 1 {
		t.Errorf("successes = %d (errs %v), want exactly 1", wins, errs)
	}
	if n := membershipCount(t, user); n != 1 {
		t.Errorf("memberships for the user = %d, want 1", n)
	}
	if errs[0] != nil {
		if s := inviteStatus(t, inviteID); s != "pending" {
			t.Errorf("invite status after a lost accept = %q, want pending", s)
		}
	}
}

func TestRLS_AcceptInvitationIsOwnedAndGrantedNarrowly(t *testing.T) {
	requireHarness(t)

	s, ok := readProc(t, acceptSig)
	if !ok {
		t.Fatalf("function %s does not exist", acceptSig)
	}
	if s.owner != "invoice_migrator" {
		t.Errorf("owner = %q, want invoice_migrator", s.owner)
	}
	if !s.secdef {
		t.Errorf("prosecdef = false, want SECURITY DEFINER")
	}
	if !reflect.DeepEqual(s.config, []string{`search_path=""`}) {
		t.Errorf("proconfig = %q, want [search_path=\"\"]", s.config)
	}
	assertExecuteOnlyForApp(t, acceptSig, s, "invoice_migrator")
}
