package tenancy

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const inviteeAddr = "tunde@obi.test"

// acceptWorld is tenant "Obi Partners" with one live invite to inviteeAddr.
type acceptWorld struct {
	invWorld
	invite IssuedInvite
}

func newAcceptWorld(t *testing.T, role string) acceptWorld {
	t.Helper()
	w := newInvWorld(t, "Obi Partners", "Ada Obi")
	return acceptWorld{invWorld: w, invite: mustIssue(t, w.adminCtx(), w.store, []string{inviteeAddr}, role)[0]}
}

func tenantless(subject, email string) context.Context {
	return auth.WithTenantlessCaller(context.Background(), auth.Identity{Subject: subject, Role: "authenticated", Email: email})
}

// tracedStore is a Store over its own app-role pool that records every statement.
func tracedStore(t *testing.T) (*Store, *sqlTrace) {
	t.Helper()
	dbTestPools(t)
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	tr := &sqlTrace{}
	cfg.ConnConfig.Tracer = tr
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return NewStore(pool), tr
}

func (s *sqlTrace) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.stmts)
}

type memberRow struct {
	Tenant, User, Role, Status string
	DisplayName, Email         *string
}

// membersOf reads every membership of a user, in any tenant, as superuser.
func membersOf(t *testing.T, super *pgxpool.Pool, user string) []memberRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT tenant_id::text, user_id::text, role, status, display_name, email FROM memberships WHERE user_id = $1 ORDER BY tenant_id`, user)
	if err != nil {
		t.Fatalf("read memberships: %v", err)
	}
	defer rows.Close()
	var out []memberRow
	for rows.Next() {
		var m memberRow
		if err := rows.Scan(&m.Tenant, &m.User, &m.Role, &m.Status, &m.DisplayName, &m.Email); err != nil {
			t.Fatalf("scan membership: %v", err)
		}
		out = append(out, m)
	}
	return out
}

type acceptedAudit struct {
	Actor   string
	Payload map[string]any
}

func acceptedAudits(t *testing.T, super *pgxpool.Pool, tenant string) []acceptedAudit {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT actor, payload FROM audit_log WHERE tenant_id = $1 AND event = 'invitation.accepted' ORDER BY id`, tenant)
	if err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	defer rows.Close()
	var out []acceptedAudit
	for rows.Next() {
		var a acceptedAudit
		if err := rows.Scan(&a.Actor, &a.Payload); err != nil {
			t.Fatalf("scan audit row: %v", err)
		}
		out = append(out, a)
	}
	return out
}

func requireStatus(t *testing.T, w acceptWorld, want string) {
	t.Helper()
	rows := invRows(t, w.super, w.tenant, "id = $2", w.invite.ID)
	if len(rows) != 1 || rows[0].Status != want {
		t.Errorf("invite rows = %+v, want one with status %q", rows, want)
	}
}

func requireNothingWritten(t *testing.T, w acceptWorld, user string, wantMemberships int) {
	t.Helper()
	if got := len(membersOf(t, w.super, user)); got != wantMemberships {
		t.Errorf("memberships of the caller = %d, want %d unchanged", got, wantMemberships)
	}
	requireStatus(t, w, "pending")
	if n := len(acceptedAudits(t, w.super, w.tenant)); n != 0 {
		t.Errorf("invitation.accepted rows = %d, want 0", n)
	}
}

func TestAccept_PreviewNamesWorkspaceRoleAndAddress(t *testing.T) {
	w := newAcceptWorld(t, "reviewer")
	other := newInvWorld(t, "Elsewhere Ltd", "Bola Eze")
	want := InvitationPreview{Workspace: "Obi Partners", Role: "reviewer", Email: inviteeAddr}

	for name, ctx := range map[string]context.Context{
		"no caller":                context.Background(),
		"tenant-less caller":       tenantless(uuid.NewString(), "someone@else.test"),
		"member of another tenant": other.adminCtx(),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := w.store.PreviewInvitation(ctx, w.invite.Token)
			if err != nil {
				t.Fatalf("PreviewInvitation: %v", err)
			}
			if got != want {
				t.Errorf("preview = %+v, want %+v", got, want)
			}
		})
	}
}

func TestAccept_PreviewRefusesUnusableTokens(t *testing.T) {
	w := newInvWorld(t, "Obi Partners", "Ada Obi")
	store, tr := tracedStore(t)
	issue := func(addr string) IssuedInvite {
		return mustIssue(t, w.adminCtx(), w.store, []string{addr}, "reviewer")[0]
	}
	setState := func(inv IssuedInvite, set string) {
		t.Helper()
		if _, err := w.super.Exec(context.Background(), `UPDATE invitations SET `+set+` WHERE id = $1`, inv.ID); err != nil {
			t.Fatalf("set invite state: %v", err)
		}
	}

	live := issue("live@obi.test")
	before := tr.count()
	if got, err := store.PreviewInvitation(context.Background(), live.Token); err != nil || got.Email != "live@obi.test" {
		t.Errorf("a live invite previews as %+v, err %v; want its address", got, err)
	}
	if tr.count() == before {
		t.Error("the traced pool saw no statement for a live preview, so a zero count proves nothing")
	}

	unknown, _, err := mintToken()
	if err != nil {
		t.Fatal(err)
	}
	accepted, revoked, expired, resent := issue("accepted@obi.test"), issue("revoked@obi.test"), issue("expired@obi.test"), issue("resent@obi.test")
	setState(accepted, `status = 'accepted'`)
	setState(revoked, `status = 'revoked'`)
	setState(expired, `expires_at = now() - interval '1 second'`)
	if _, err := w.store.ResendInvitation(w.adminCtx(), resent.ID); err != nil {
		t.Fatalf("resend: %v", err)
	}

	for _, c := range []struct {
		name, token string
		malformed   bool
	}{
		{"unknown", unknown, false},
		{"accepted", accepted.Token, false},
		{"revoked", revoked.Token, false},
		{"expired", expired.Token, false},
		{"replaced by a resend", resent.Token, false},
		{"42 characters", live.Token[:42], true},
		{"44 characters", live.Token + "A", true},
		{"plus sign", live.Token[:42] + "+", true},
		{"slash", live.Token[:42] + "/", true},
		{"empty", "", true},
		{"long", strings.Repeat("A", 1024), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := tr.count()
			got, err := store.PreviewInvitation(context.Background(), c.token)
			if !errors.Is(err, ErrInvitationNotValid) {
				t.Errorf("err = %v, want ErrInvitationNotValid", err)
			}
			if got != (InvitationPreview{}) {
				t.Errorf("preview = %+v, want the zero value", got)
			}
			if c.malformed {
				if n := tr.count() - before; n != 0 {
					t.Errorf("a malformed token sent %d statements, want none", n)
				}
			}
		})
	}
}

func TestAccept_TenantlessInviteeJoinsWithTheInvitedRole(t *testing.T) {
	w := newAcceptWorld(t, "reviewer")
	user := uuid.NewString()
	inv := invRows(t, w.super, w.tenant, "id = $2", w.invite.ID)[0]

	// An upper-case subject and address: the result and the rows hold the canonical forms.
	tenant, subject, role, err := w.store.AcceptInvitation(tenantless(strings.ToUpper(user), "Tunde@obi.test"), w.invite.Token)
	if err != nil {
		t.Fatalf("AcceptInvitation: %v", err)
	}
	if tenant != (Tenant{ID: w.tenant, Name: "Obi Partners", Kind: "firm"}) {
		t.Errorf("tenant = %+v, want {%s Obi Partners firm}", tenant, w.tenant)
	}
	if subject != user || role != "reviewer" {
		t.Errorf("subject, role = %q, %q; want %q, reviewer", subject, role, user)
	}

	rows := membersOf(t, w.super, user)
	if len(rows) != 1 {
		t.Fatalf("memberships = %+v, want exactly one", rows)
	}
	m := rows[0]
	if m.Tenant != w.tenant || m.User != user || m.Role != "reviewer" || m.Status != "active" {
		t.Errorf("membership = %+v, want (%s, %s, reviewer, active)", m, w.tenant, user)
	}
	if m.DisplayName != nil {
		t.Errorf("display_name = %q, want NULL", *m.DisplayName)
	}
	if m.Email == nil || *m.Email != inviteeAddr {
		t.Errorf("email = %v, want the invited address %q", m.Email, inviteeAddr)
	}
	requireStatus(t, w, "accepted")

	audits := acceptedAudits(t, w.super, w.tenant)
	if len(audits) != 1 {
		t.Fatalf("invitation.accepted rows = %d, want exactly 1", len(audits))
	}
	if audits[0].Actor != user {
		t.Errorf("audit actor = %q, want the caller %q", audits[0].Actor, user)
	}
	if len(audits[0].Payload) != 2 || audits[0].Payload["invitation_id"] != inv.ID || audits[0].Payload["role"] != "reviewer" {
		t.Errorf("audit payload = %v, want exactly {invitation_id: %s, role: reviewer}", audits[0].Payload, inv.ID)
	}
}

func TestAccept_TheJoinedWorkspaceAnswersMe(t *testing.T) {
	w := newAcceptWorld(t, "reviewer")
	user := uuid.NewString()
	if _, _, _, err := w.store.AcceptInvitation(tenantless(user, inviteeAddr), w.invite.Token); err != nil {
		t.Fatalf("first accept: %v", err)
	}

	me := auth.WithIdentity(context.Background(), auth.Identity{Subject: user, Role: "authenticated", TenantID: w.tenant})
	tenant, who, err := w.store.Me(me)
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if tenant.ID != w.tenant || who.Role != "reviewer" {
		t.Errorf("Me = tenant %s role %q, want tenant %s role reviewer", tenant.ID, who.Role, w.tenant)
	}

	// The invite is spent, so the token names no usable invite before any membership guard runs.
	if _, _, _, err := w.store.AcceptInvitation(tenantless(user, inviteeAddr), w.invite.Token); !errors.Is(err, ErrInvitationNotValid) {
		t.Errorf("second accept err = %v, want ErrInvitationNotValid", err)
	}
	if _, err := w.store.PreviewInvitation(context.Background(), w.invite.Token); !errors.Is(err, ErrInvitationNotValid) {
		t.Errorf("preview of a spent token err = %v, want ErrInvitationNotValid", err)
	}
	if n := len(acceptedAudits(t, w.super, w.tenant)); n != 1 {
		t.Errorf("invitation.accepted rows = %d, want 1", n)
	}
}

func TestAccept_AnAccountWithAWorkspaceGainsNothing(t *testing.T) {
	for _, c := range []struct {
		name, status, email string
		bearing             bool
	}{
		{"active member with a tenant-bearing token", "active", inviteeAddr, true},
		{"suspended member as a tenant-less caller", "suspended", inviteeAddr, false},
		{"membership beats an address mismatch", "active", "other@obi.test", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newAcceptWorld(t, "reviewer")
			b := newInvWorld(t, "Other Firm", "Bola Eze")
			user := uuid.NewString()
			seedIdentityMembership(t, w.super, b.tenant, user, "admin", c.status, nil, strp(c.email))
			ctx := tenantless(user, c.email)
			if c.bearing {
				ctx = auth.WithIdentity(context.Background(), auth.Identity{Subject: user, Role: "authenticated", TenantID: b.tenant, Email: c.email})
			}

			_, _, _, err := w.store.AcceptInvitation(ctx, w.invite.Token)
			if !errors.Is(err, ErrAlreadyMember) {
				t.Fatalf("err = %v, want ErrAlreadyMember", err)
			}
			requireNothingWritten(t, w, user, 1)
			if m := membersOf(t, w.super, user)[0]; m.Tenant != b.tenant || m.Role != "admin" || m.Status != c.status {
				t.Errorf("membership changed to %+v", m)
			}

			fresh := uuid.NewString()
			tenant, _, role, err := w.store.AcceptInvitation(tenantless(fresh, inviteeAddr), w.invite.Token)
			if err != nil || tenant.ID != w.tenant || role != "reviewer" {
				t.Errorf("the invited account accepts afterwards: tenant %+v role %q err %v", tenant, role, err)
			}
		})
	}
}

func TestAccept_AnotherAddressIsRefused(t *testing.T) {
	for _, email := range []string{"other@obi.test", ""} {
		t.Run("caller email "+email, func(t *testing.T) {
			w := newAcceptWorld(t, "reviewer")
			user := uuid.NewString()

			_, _, _, err := w.store.AcceptInvitation(tenantless(user, email), w.invite.Token)
			if !errors.Is(err, ErrInvitationEmailMismatch) {
				t.Fatalf("err = %v, want ErrInvitationEmailMismatch", err)
			}
			requireNothingWritten(t, w, user, 0)

			if _, _, _, err := w.store.AcceptInvitation(tenantless(uuid.NewString(), inviteeAddr), w.invite.Token); err != nil {
				t.Errorf("the invited address accepts afterwards: %v", err)
			}
		})
	}
}

// failAcceptAudit makes the tenant's invitation.accepted insert raise; the returned func drops the trigger.
func failAcceptAudit(t *testing.T, super *pgxpool.Pool, tenant string) (drop func()) {
	t.Helper()
	ctx := context.Background()
	name := "accept_audit_fail_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := super.Exec(ctx,
		`CREATE FUNCTION `+name+`() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN RAISE EXCEPTION 'forced audit failure' USING ERRCODE = 'check_violation'; END; $$`); err != nil {
		t.Fatalf("create the forced-failure function: %v", err)
	}
	t.Cleanup(func() { _, _ = super.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+name+`() CASCADE`) })
	if _, err := super.Exec(ctx,
		`CREATE TRIGGER `+name+` BEFORE INSERT ON audit_log FOR EACH ROW
		 WHEN (NEW.tenant_id = `+quoteLiteral(tenant)+`::uuid AND NEW.event = 'invitation.accepted') EXECUTE FUNCTION `+name+`()`); err != nil {
		t.Fatalf("create the forced-failure trigger: %v", err)
	}
	return func() {
		if _, err := super.Exec(context.Background(), `DROP TRIGGER `+name+` ON audit_log`); err != nil {
			t.Fatalf("drop the forced-failure trigger: %v", err)
		}
	}
}

func TestAccept_AFailedAuditRollsTheAcceptBack(t *testing.T) {
	w := newAcceptWorld(t, "reviewer")
	user := uuid.NewString()
	drop := failAcceptAudit(t, w.super, w.tenant)

	_, _, _, err := w.store.AcceptInvitation(tenantless(user, inviteeAddr), w.invite.Token)
	if err == nil {
		t.Fatal("AcceptInvitation succeeded although its audit write fails")
	}
	for _, s := range []error{ErrInvitationNotValid, ErrAlreadyMember, ErrInvitationEmailMismatch} {
		if errors.Is(err, s) {
			t.Errorf("err = %v, want the audit failure, not the refusal %v", err, s)
		}
	}
	requireNothingWritten(t, w, user, 0)

	drop()
	if _, _, _, err := w.store.AcceptInvitation(tenantless(user, inviteeAddr), w.invite.Token); err != nil {
		t.Errorf("accept after the trigger is gone: %v", err)
	}
	if got := len(membersOf(t, w.super, user)); got != 1 {
		t.Errorf("memberships after the retry = %d, want 1", got)
	}
}

// identityLockRequests counts advisory-lock requests on the identity key accept and provision share.
func identityLockRequests(t *testing.T, super *pgxpool.Pool, user string, granted bool) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM pg_locks
		  WHERE locktype = 'advisory' AND granted = $2
		    AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		    AND ((classid::bigint << 32) | objid::bigint) = hashtextextended($1::text, 0)`,
		user, granted).Scan(&n); err != nil {
		t.Fatalf("read pg_locks: %v", err)
	}
	return n
}

func TestAccept_ConcurrentAcceptsOfOneTokenJoinOnce(t *testing.T) {
	w := newAcceptWorld(t, "reviewer")
	user := uuid.NewString()
	ctx := context.Background()

	conn, err := w.super.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1::text, 0))`, user); err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended($1::text, 0))`, user); err != nil {
			t.Errorf("unlock: %v", err)
		}
		conn.Release()
	}
	t.Cleanup(release)
	if n := identityLockRequests(t, w.super, user, true); n != 1 {
		t.Fatalf("pg_locks filter sees %d granted locks on the key, want the test's own 1", n)
	}

	type result struct {
		tenant Tenant
		err    error
	}
	ch := make(chan result, 2)
	for range 2 {
		go func() {
			tenant, _, _, err := w.store.AcceptInvitation(tenantless(user, inviteeAddr), w.invite.Token)
			ch <- result{tenant, err}
		}()
	}

	deadline := time.Now().Add(5 * time.Second)
	for identityLockRequests(t, w.super, user, false) < 2 {
		select {
		case r := <-ch:
			t.Fatalf("an accept returned while the identity lock was held (tenant %+v, err %v)", r.tenant, r.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("pg_locks shows %d ungranted requests on the identity key after 5 s, want 2", identityLockRequests(t, w.super, user, false))
		}
		time.Sleep(25 * time.Millisecond)
	}

	release()
	ok, refused := 0, 0
	for range 2 {
		select {
		case r := <-ch:
			switch {
			case r.err == nil && r.tenant.ID == w.tenant:
				ok++
			// The loser re-reads the invite under the lock: it is spent, so it is not valid.
			case errors.Is(r.err, ErrInvitationNotValid):
				refused++
			default:
				t.Errorf("unexpected result: tenant %+v, err %v", r.tenant, r.err)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("an accept did not return after the lock was released")
		}
	}
	if ok != 1 || refused != 1 {
		t.Errorf("successes/refusals = %d/%d, want 1/1", ok, refused)
	}
	if got := len(membersOf(t, w.super, user)); got != 1 {
		t.Errorf("memberships = %d, want 1", got)
	}
	if n := len(acceptedAudits(t, w.super, w.tenant)); n != 1 {
		t.Errorf("invitation.accepted rows = %d, want 1", n)
	}
}

func TestAccept_NoCallerIs401BeforeAnyQuery(t *testing.T) {
	w := newAcceptWorld(t, "reviewer")
	store, tr := tracedStore(t)
	before := tr.count()

	_, _, _, err := store.AcceptInvitation(context.Background(), w.invite.Token)
	if !errors.Is(err, db.ErrNoTenant) {
		t.Errorf("err = %v, want db.ErrNoTenant", err)
	}
	// The store reads the caller; the handler cannot see a tenant-less one, so it maps this error.
	rec := apiDo(AcceptInvitationHandler(store.AcceptInvitation, nil), context.Background(), http.MethodPost, "/v1/invitations/accept", `{"token":"`+w.invite.Token+`"}`)
	assertErrorBody(t, rec, http.StatusUnauthorized, "unauthorized")
	if n := tr.count() - before; n != 0 {
		t.Errorf("a call with no caller sent %d statements, want none", n)
	}
	requireStatus(t, w, "pending")
}

// D7 (pm #156): the 403 reads the same whether or not the invited address has an account.
func TestAccept_AMismatchedAddressIsRefusedAlikeWithOrWithoutAnAccount(t *testing.T) {
	fresh := newAcceptWorld(t, "reviewer")

	known := newInvWorld(t, "Known Partners", "Kemi Ade")
	const knownAddr = "ada@known.test"
	elsewhere := newInvWorld(t, "Elsewhere Ltd", "Bola Eze")
	seedIdentityMembership(t, known.super, elsewhere.tenant, uuid.NewString(), "preparer", "active", strp("Ada Known"), strp(knownAddr))
	knownInvite := mustIssue(t, known.adminCtx(), known.store, []string{knownAddr}, "reviewer")[0]

	type answer struct {
		status int
		body   string
		ctype  string
	}
	ask := func(store *Store, token, email string) answer {
		rec := apiDo(AcceptInvitationHandler(store.AcceptInvitation, nil), tenantless(uuid.NewString(), email), http.MethodPost, "/v1/invitations/accept", `{"token":"`+token+`"}`)
		return answer{rec.Code, rec.Body.String(), rec.Header().Get("Content-Type")}
	}

	var first answer
	for i, c := range []struct {
		name, email string
		store       *Store
		token       string
	}{
		{"no account, other address", "other@obi.test", fresh.store, fresh.invite.Token},
		{"has an account, other address", "other@obi.test", known.store, knownInvite.Token},
		{"no account, no email", "", fresh.store, fresh.invite.Token},
		{"has an account, no email", "", known.store, knownInvite.Token},
	} {
		got := ask(c.store, c.token, c.email)
		if i == 0 {
			first = got
			if got.status != http.StatusForbidden || !strings.Contains(got.body, "this invite was sent to a different email address") {
				t.Fatalf("%s: status %d body %s, want 403 with the different-address message", c.name, got.status, got.body)
			}
		}
		if got != first {
			t.Errorf("%s: answer %+v differs from the first %+v", c.name, got, first)
		}
		if strings.Contains(got.body, knownAddr) || strings.Contains(got.body, inviteeAddr) {
			t.Errorf("%s: body %s names an address", c.name, got.body)
		}
	}
}
