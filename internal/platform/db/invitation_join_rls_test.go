package db_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	pendingSig      = "public.pending_invites_for_email(text, uuid)"
	acceptByIDSig   = "public.accept_invitation_by_id(uuid, uuid, uuid, text)"
	pendingCall     = `SELECT invitation_id::text, tenant_id::text, workspace, role, inviter, expires_at FROM public.pending_invites_for_email($1::text, $2::uuid)`
	acceptByIDCall  = `SELECT invitation_id::text, role FROM public.accept_invitation_by_id($1::uuid, $2::uuid, $3::uuid, $4::text)`
	mismatchMessage = "row-level security"
)

type pendingRow struct {
	id, tenantID, workspace, role string
	inviter                       *string
	expires                       time.Time
}

// uniqueAddr keeps each test's address its own: the list function reads every tenant.
func uniqueAddr(label string) string {
	return label + "-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12] + "@obi.test"
}

// seedJoinInvite writes a pending invite expiring at the SQL expression expires.
func seedJoinInvite(t *testing.T, tenant, role, email, expires, invitedBy string) string {
	t.Helper()
	id := uuid.NewString()
	if invitedBy == "" {
		invitedBy = uuid.NewString()
	}
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO invitations (id, tenant_id, role, invitee_email, token_hash, expires_at, invited_by)
		 VALUES ($1, $2, $3, $4, $5, `+expires+`, $6)`,
		id, tenant, role, email, hashOf(newToken(t)), invitedBy,
	); err != nil {
		t.Fatalf("seed invitation (expires %s): %v", expires, err)
	}
	return id
}

// seedNamedMember writes an active membership; display and email may be nil.
func seedNamedMember(t *testing.T, tenant, user string, display, email any) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id, role, status, display_name, email)
		 VALUES ($1, $2, 'admin', 'active', $3, $4)`,
		tenant, user, display, email,
	); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
}

// pendingFor lists as a caller who holds no membership.
func pendingFor(t *testing.T, guc string, email any) []pendingRow {
	t.Helper()
	return pendingForUser(t, guc, email, uuid.NewString())
}

func pendingForUser(t *testing.T, guc string, email any, user string) []pendingRow {
	t.Helper()
	var out []pendingRow
	err := inAppTx(context.Background(), guc, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), pendingCall, email, user)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r pendingRow
			if err := rows.Scan(&r.id, &r.tenantID, &r.workspace, &r.role, &r.inviter, &r.expires); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("pending_invites_for_email as invoice_app (GUC %q, email %v): %v", guc, email, err)
	}
	return out
}

func idsOf(rows []pendingRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.id)
	}
	return out
}

func acceptByIDAs(ctx context.Context, guc, tenant, invite, user string, email any) (id, role string, err error) {
	err = inAppTx(ctx, guc, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, acceptByIDCall, tenant, invite, user, email).Scan(&id, &role)
	})
	return id, role, err
}

func TestRLS_PendingInvitesForEmailSpansTenantsInExpiryOrder(t *testing.T) {
	requireHarness(t)
	roles := rolesInDB(t)

	t.Run("three tenants, three expiries", func(t *testing.T) {
		addr := uniqueAddr("tunde")
		t1, t2, t3 := newNamedTenant(t, "Obi Partners"), newNamedTenant(t, "Other Firm"), newNamedTenant(t, "Third Firm")
		in3d := seedJoinInvite(t, t1, roles[0], addr, `now() + interval '3 days'`, "")
		in1d := seedJoinInvite(t, t2, roles[1], addr, `now() + interval '1 day'`, "")
		in2d := seedJoinInvite(t, t3, roles[0], addr, `now() + interval '2 days'`, "")
		want := []pendingRow{
			{id: in1d, tenantID: t2, workspace: "Other Firm", role: roles[1]},
			{id: in2d, tenantID: t3, workspace: "Third Firm", role: roles[0]},
			{id: in3d, tenantID: t1, workspace: "Obi Partners", role: roles[0]},
		}

		for _, c := range []struct{ name, guc string }{{"GUC is tenant 1", t1}, {"GUC unset", ""}} {
			t.Run(c.name, func(t *testing.T) {
				got := pendingFor(t, c.guc, addr)
				if len(got) != len(want) {
					t.Fatalf("rows = %d (%v), want %d", len(got), idsOf(got), len(want))
				}
				for i, w := range want {
					g := got[i]
					if g.id != w.id || g.tenantID != w.tenantID || g.workspace != w.workspace || g.role != w.role {
						t.Errorf("row %d = (%s, %s, %s, %s), want (%s, %s, %s, %s)",
							i, g.id, g.tenantID, g.workspace, g.role, w.id, w.tenantID, w.workspace, w.role)
					}
					var stored time.Time
					if err := h.super.QueryRow(context.Background(), `SELECT expires_at FROM invitations WHERE id = $1`, w.id).Scan(&stored); err != nil {
						t.Fatalf("read stored expiry: %v", err)
					}
					if !g.expires.Equal(stored) {
						t.Errorf("row %d expires_at = %v, want the stored %v", i, g.expires, stored)
					}
					if i > 0 && !got[i-1].expires.Before(g.expires) {
						t.Errorf("row %d expires %v is not after row %d expires %v", i, g.expires, i-1, got[i-1].expires)
					}
				}
			})
		}
	})

	t.Run("equal expiry orders by id", func(t *testing.T) {
		addr := uniqueAddr("tunde")
		const same = `'2031-03-04 05:06:07+00'::timestamptz`
		ids := []string{
			seedJoinInvite(t, newNamedTenant(t, "Obi Partners"), roles[0], addr, same, ""),
			seedJoinInvite(t, newNamedTenant(t, "Other Firm"), roles[0], addr, same, ""),
			seedJoinInvite(t, newNamedTenant(t, "Third Firm"), roles[0], addr, same, ""),
		}
		sort.Strings(ids)

		got := idsOf(pendingFor(t, "", addr))

		if !reflect.DeepEqual(got, ids) {
			t.Errorf("ids = %v, want %v (ascending)", got, ids)
		}
	})
}

func TestRLS_PendingInvitesForEmailIgnoresUnusableInvites(t *testing.T) {
	requireHarness(t)
	addr := uniqueAddr("tunde")
	live := seedJoinInvite(t, newNamedTenant(t, "Live Firm"), "reviewer", addr, `now() + interval '1 day'`, "")
	expired := seedJoinInvite(t, newNamedTenant(t, "Expired Firm"), "reviewer", addr, `now() - interval '1 second'`, "")
	accepted := seedJoinInvite(t, newNamedTenant(t, "Accepted Firm"), "reviewer", addr, `now() + interval '1 day'`, "")
	setInviteState(t, accepted, `status = 'accepted'`)
	revoked := seedJoinInvite(t, newNamedTenant(t, "Revoked Firm"), "reviewer", addr, `now() + interval '1 day'`, "")
	setInviteState(t, revoked, `status = 'revoked'`)
	other := seedJoinInvite(t, newNamedTenant(t, "Other Address Firm"), "reviewer", uniqueAddr("bola"), `now() + interval '1 day'`, "")

	got := idsOf(pendingFor(t, "", addr))

	if !reflect.DeepEqual(got, []string{live}) {
		t.Errorf("ids = %v, want only the live invite [%s]; expired %s, accepted %s, revoked %s, other address %s must be absent",
			got, live, expired, accepted, revoked, other)
	}
}

func TestRLS_PendingInvitesForEmailExpiryEdge(t *testing.T) {
	requireHarness(t)
	addr := uniqueAddr("tunde")
	id := seedJoinInvite(t, newNamedTenant(t, "Obi Partners"), "reviewer", addr, `now() + interval '2 seconds'`, "")

	if got := idsOf(pendingFor(t, "", addr)); !reflect.DeepEqual(got, []string{id}) {
		t.Fatalf("before expiry: ids = %v, want [%s]", got, id)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		var passed bool
		if err := h.super.QueryRow(context.Background(), `SELECT clock_timestamp() > expires_at FROM invitations WHERE id = $1`, id).Scan(&passed); err != nil {
			t.Fatalf("read the clock: %v", err)
		}
		if passed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the invite never expired within 5 s")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := pendingFor(t, "", addr); len(got) != 0 {
		t.Errorf("after expiry: rows = %v, want none", idsOf(got))
	}
}

func TestRLS_PendingInvitesForEmailMatchesCaseAndSpace(t *testing.T) {
	requireHarness(t)
	local := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	lower := "tunde-" + local + "@obi.test"
	mixedStored := "Tunde-" + local + "@Obi.test"

	for _, c := range []struct {
		name, stored string
		given        any
		want         int
	}{
		{"stored lower, given padded mixed case", lower, "  Tunde-" + local + "@OBI.test ", 1},
		{"stored mixed case, given lower", mixedStored, lower, 1},
		{"stored lower, given exact", lower, lower, 1},
		{"given a prefix of the address", lower, "tunde-" + local, 0},
		{"given NULL", lower, nil, 0},
		{"given empty", lower, "", 0},
		{"given spaces only", lower, "   ", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			id := seedJoinInvite(t, newNamedTenant(t, "Obi Partners"), "reviewer", c.stored, `now() + interval '1 day'`, "")
			if got := idsOf(pendingFor(t, "", c.stored)); !reflect.DeepEqual(got, []string{id}) {
				t.Fatalf("control: exact address ids = %v, want [%s]", got, id)
			}

			got := pendingFor(t, "", c.given)

			if len(got) != c.want {
				t.Errorf("rows for %q = %v, want %d", c.given, idsOf(got), c.want)
			}
		})
	}
}

func TestRLS_PendingInvitesForEmailNamesTheInviter(t *testing.T) {
	requireHarness(t)
	addr := uniqueAddr("tunde")
	named, unnamed, blank, nobody, elsewhere := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	tNamed, tUnnamed, tBlank, tNobody, tElsewhere, tOther :=
		newNamedTenant(t, "Named Firm"), newNamedTenant(t, "Unnamed Firm"), newNamedTenant(t, "Blank Firm"),
		newNamedTenant(t, "Nobody Firm"), newNamedTenant(t, "Elsewhere Firm"), newNamedTenant(t, "Other Firm")
	seedNamedMember(t, tNamed, named, "Ada Obi", "ada@obi.test")
	seedNamedMember(t, tUnnamed, unnamed, nil, "bola@obi.test")
	seedNamedMember(t, tBlank, blank, "   ", "chidi@obi.test")
	seedNamedMember(t, tOther, elsewhere, "Named Elsewhere", "dayo@obi.test")
	crowded, tCrowded := uuid.NewString(), newNamedTenant(t, "Crowded Firm")
	seedNamedMember(t, tCrowded, uuid.NewString(), "Not The Inviter", "other@obi.test")
	seedNamedMember(t, tCrowded, crowded, "Emeka Obi", "emeka@obi.test")
	tStranger := newNamedTenant(t, "Stranger Firm")
	seedNamedMember(t, tStranger, uuid.NewString(), "Someone Else", "else@obi.test")

	cases := []struct {
		invite string
		want   *string
	}{
		{seedJoinInvite(t, tNamed, "reviewer", addr, `now() + interval '1 day'`, named), ptr("Ada Obi")},
		{seedJoinInvite(t, tUnnamed, "reviewer", addr, `now() + interval '2 days'`, unnamed), ptr("bola@obi.test")},
		{seedJoinInvite(t, tBlank, "reviewer", addr, `now() + interval '3 days'`, blank), ptr("chidi@obi.test")},
		{seedJoinInvite(t, tNobody, "reviewer", addr, `now() + interval '4 days'`, nobody), nil},
		{seedJoinInvite(t, tElsewhere, "reviewer", addr, `now() + interval '5 days'`, elsewhere), nil},
		{seedJoinInvite(t, tCrowded, "reviewer", addr, `now() + interval '6 days'`, crowded), ptr("Emeka Obi")},
		{seedJoinInvite(t, tStranger, "reviewer", addr, `now() + interval '7 days'`, uuid.NewString()), nil},
	}

	got := map[string]*string{}
	rows := pendingFor(t, "", addr)
	if len(rows) != len(cases) {
		t.Fatalf("rows = %d (%v), want %d", len(rows), idsOf(rows), len(cases))
	}
	for _, r := range rows {
		got[r.id] = r.inviter
	}
	for i, c := range cases {
		g, ok := got[c.invite]
		if !ok {
			t.Errorf("case %d: invite %s not listed", i, c.invite)
			continue
		}
		switch {
		case c.want == nil && g != nil:
			t.Errorf("case %d: inviter = %q, want NULL", i, *g)
		case c.want != nil && (g == nil || *g != *c.want):
			t.Errorf("case %d: inviter = %v, want %q", i, g, *c.want)
		}
	}
}

func ptr(s string) *string { return &s }

func TestRLS_AcceptByIdWritesTheMembership(t *testing.T) {
	requireHarness(t)
	for _, role := range rolesInDB(t) {
		t.Run(role, func(t *testing.T) {
			ctx := context.Background()
			a := newNamedTenant(t, "Obi Partners")
			addr := uniqueAddr("tunde")
			inviteID := seedJoinInvite(t, a, role, addr, `now() + interval '1 day'`, "")
			user := uuid.NewString()

			gotID, gotRole, err := acceptByIDAs(ctx, a, a, inviteID, user, "  "+strings.ToUpper(addr)+" ")
			if err != nil {
				t.Fatalf("accept_invitation_by_id under GUC A: want success, got %v", err)
			}
			if gotID != inviteID || gotRole != role {
				t.Errorf("returned (invitation_id, role) = (%s, %s), want (%s, %s)", gotID, gotRole, inviteID, role)
			}

			if n := membershipCount(t, user); n != 1 {
				t.Fatalf("memberships for the user = %d, want 1", n)
			}
			var tenant, mRole, status string
			var display, email *string
			if err := h.super.QueryRow(ctx,
				`SELECT tenant_id::text, role, status, display_name, email FROM memberships WHERE user_id = $1`, user,
			).Scan(&tenant, &mRole, &status, &display, &email); err != nil {
				t.Fatalf("read membership: %v", err)
			}
			if tenant != a || mRole != role || status != "active" {
				t.Errorf("membership (tenant, role, status) = (%s, %s, %s), want (%s, %s, active)", tenant, mRole, status, a, role)
			}
			if display != nil {
				t.Errorf("display_name = %q, want NULL", *display)
			}
			if email == nil || *email != addr {
				t.Errorf("email = %v, want the invited address %q", email, addr)
			}
			if s := inviteStatus(t, inviteID); s != "accepted" {
				t.Errorf("invite status = %q, want accepted", s)
			}
		})
	}
}

func TestRLS_AcceptByIdMatchesTheAddressInEitherCase(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	local := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	stored := "Tunde-" + local + "@Obi.test"
	a := newNamedTenant(t, "Obi Partners")
	inviteID := seedJoinInvite(t, a, "reviewer", stored, `now() + interval '1 day'`, "")
	user := uuid.NewString()

	if _, _, err := acceptByIDAs(ctx, a, a, inviteID, user, "  tunde-"+local+"@obi.test "); err != nil {
		t.Fatalf("accept a mixed-case invite with the lower-case address: want success, got %v", err)
	}

	if s := inviteStatus(t, inviteID); s != "accepted" {
		t.Errorf("invite status = %q, want accepted", s)
	}
	if n := membershipCount(t, user); n != 1 {
		t.Errorf("memberships for the user = %d, want 1", n)
	}
}

func TestRLS_AcceptByIdLeavesTheOtherInvitesPending(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	t1, t2, t3 := newNamedTenant(t, "Obi Partners"), newNamedTenant(t, "Other Firm"), newNamedTenant(t, "Third Firm")
	addr := uniqueAddr("tunde")
	first := seedJoinInvite(t, t1, "reviewer", addr, `now() + interval '1 day'`, "")
	second := seedJoinInvite(t, t2, "reviewer", addr, `now() + interval '2 days'`, "")
	third := seedJoinInvite(t, t3, "reviewer", addr, `now() + interval '3 days'`, "")
	sibling := seedJoinInvite(t, t1, "reviewer", uniqueAddr("bola"), `now() + interval '1 day'`, "")
	user := uuid.NewString()

	if _, _, err := acceptByIDAs(ctx, t1, t1, first, user, addr); err != nil {
		t.Fatalf("accept the first invite: want success, got %v", err)
	}

	if s := inviteStatus(t, first); s != "accepted" {
		t.Errorf("accepted invite status = %q, want accepted", s)
	}
	for name, id := range map[string]string{"second": second, "third": third, "same tenant, another address": sibling} {
		if s := inviteStatus(t, id); s != "pending" {
			t.Errorf("%s invite status = %q, want pending", name, s)
		}
	}
	if n := membershipCount(t, user); n != 1 {
		t.Errorf("memberships for the user = %d, want 1", n)
	}
}

func TestRLS_AcceptByIdRefusals(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	type fixture struct {
		a, b, user       string
		addr             string
		inviteA, inviteB string
	}
	for _, c := range []struct {
		name       string
		setup      func(t *testing.T, f *fixture)
		invite     func(f *fixture) string
		email      func(f *fixture) any
		wantStatus string
	}{
		{name: "unknown id", invite: func(*fixture) string { return uuid.NewString() }},
		{name: "another tenant's id under GUC and p_tenant_id = A", invite: func(f *fixture) string { return f.inviteB }},
		{name: "another address", email: func(*fixture) any { return uniqueAddr("bola") }},
		{name: "expired", setup: func(t *testing.T, f *fixture) {
			setInviteState(t, f.inviteA, `expires_at = now() - interval '1 second'`)
		}},
		{name: "accepted", setup: func(t *testing.T, f *fixture) { setInviteState(t, f.inviteA, `status = 'accepted'`) }, wantStatus: "accepted"},
		{name: "revoked", setup: func(t *testing.T, f *fixture) { setInviteState(t, f.inviteA, `status = 'revoked'`) }, wantStatus: "revoked"},
		{name: "empty email", email: func(*fixture) any { return "" }},
		{name: "spaces-only email", email: func(*fixture) any { return "   " }},
		{name: "the address with a suffix", email: func(f *fixture) any { return f.addr + "x" }},
		{name: "a prefix of the address", email: func(f *fixture) any { return strings.TrimSuffix(f.addr, "@obi.test") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fixture{user: uuid.NewString(), addr: uniqueAddr("tunde")}
			f.a = newNamedTenant(t, "Obi Partners")
			f.b = newNamedTenant(t, "Other Firm")
			f.inviteA = seedJoinInvite(t, f.a, "reviewer", f.addr, `now() + interval '1 day'`, "")
			f.inviteB = seedJoinInvite(t, f.b, "reviewer", f.addr, `now() + interval '1 day'`, "")
			if c.setup != nil {
				c.setup(t, f)
			}
			invite := f.inviteA
			if c.invite != nil {
				invite = c.invite(f)
			}
			var email any = f.addr
			if c.email != nil {
				email = c.email(f)
			}
			wantStatus := c.wantStatus
			if wantStatus == "" {
				wantStatus = "pending"
			}

			_, _, err := acceptByIDAs(ctx, f.a, f.a, invite, f.user, email)

			assertAcceptRefusal(t, c.name, err, "P0002", notValidName)
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && (pgErr.Message != "invitation is not valid" || pgErr.Detail != "" || pgErr.Hint != "") {
				t.Errorf("message/detail/hint = %q/%q/%q, want the generic message and nothing else (no tenant or id)", pgErr.Message, pgErr.Detail, pgErr.Hint)
			}
			if n := membershipCount(t, f.user); n != 0 {
				t.Errorf("memberships for the user = %d, want 0", n)
			}
			if s := inviteStatus(t, f.inviteA); s != wantStatus {
				t.Errorf("tenant A's invite status = %q, want %q", s, wantStatus)
			}
			if s := inviteStatus(t, f.inviteB); s != "pending" {
				t.Errorf("tenant B's invite status = %q, want pending", s)
			}
		})
	}
}

func TestRLS_AcceptByIdRefusalOrder(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	t.Run("member, invite for another address: not valid, not 23505", func(t *testing.T) {
		a, c := newNamedTenant(t, "Obi Partners"), newNamedTenant(t, "Third Firm")
		inviteID := seedJoinInvite(t, a, "reviewer", uniqueAddr("bola"), `now() + interval '1 day'`, "")
		user := uuid.NewString()
		seedHookMembership(t, c, user, "active")

		_, _, err := acceptByIDAs(ctx, a, a, inviteID, user, uniqueAddr("tunde"))

		assertAcceptRefusal(t, "a member probing another address's invite", err, "P0002", notValidName)
	})

	t.Run("member, unknown id: not valid", func(t *testing.T) {
		a, c := newNamedTenant(t, "Obi Partners"), newNamedTenant(t, "Third Firm")
		user := uuid.NewString()
		seedHookMembership(t, c, user, "suspended")

		_, _, err := acceptByIDAs(ctx, a, a, uuid.NewString(), user, uniqueAddr("tunde"))

		assertAcceptRefusal(t, "a member probing an unknown id", err, "P0002", notValidName)
	})

	t.Run("member, own live invite: one workspace per identity", func(t *testing.T) {
		for _, status := range []string{"active", "suspended", "invited"} {
			a, c := newNamedTenant(t, "Obi Partners"), newNamedTenant(t, "Third Firm")
			addr := uniqueAddr("tunde")
			inviteID := seedJoinInvite(t, a, "reviewer", addr, `now() + interval '1 day'`, "")
			user := uuid.NewString()
			seedHookMembership(t, c, user, status)

			_, _, err := acceptByIDAs(ctx, a, a, inviteID, user, addr)

			assertAcceptRefusal(t, "a "+status+" member accepting a live invite", err, "23505", guardConstraint)
			if s := inviteStatus(t, inviteID); s != "pending" {
				t.Errorf("%s member: invite status = %q, want pending", status, s)
			}
			if n := membershipCount(t, user); n != 1 {
				t.Errorf("%s member: memberships = %d, want 1 unchanged", status, n)
			}
		}
	})
}

func TestRLS_AcceptByIdRefusesAMismatchedGUC(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	for _, c := range []struct {
		name string
		guc  func(a, b string) string
		live bool
	}{
		{"GUC unset, live invite", func(string, string) string { return "" }, true},
		{"GUC unset, unknown id", func(string, string) string { return "" }, false},
		{"GUC is tenant B, live invite of A", func(_, b string) string { return b }, true},
		{"GUC is tenant B, unknown id", func(_, b string) string { return b }, false},
		{"GUC is the nil uuid, live invite", func(string, string) string { return uuid.Nil.String() }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, b := newNamedTenant(t, "Obi Partners"), newNamedTenant(t, "Other Firm")
			addr := uniqueAddr("tunde")
			inviteID := seedJoinInvite(t, a, "reviewer", addr, `now() + interval '1 day'`, "")
			target := uuid.NewString()
			if c.live {
				target = inviteID
			}
			user := uuid.NewString()

			_, _, err := acceptByIDAs(ctx, c.guc(a, b), a, target, user, addr)

			assertPgRefusal(t, c.name, err, "42501", mismatchMessage)
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				for _, secret := range []string{a, b, inviteID, user, addr} {
					if strings.Contains(pgErr.Message+pgErr.Detail+pgErr.Hint, secret) {
						t.Errorf("refusal text %q leaks %s", pgErr.Message, secret)
					}
				}
			}
			if n := membershipCount(t, user); n != 0 {
				t.Errorf("memberships for the user = %d, want 0", n)
			}
			if s := inviteStatus(t, inviteID); s != "pending" {
				t.Errorf("invite status = %q, want pending", s)
			}
		})
	}
}

func TestRLS_AcceptByIdRefusesNullArguments(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	for _, c := range []struct {
		name string
		args func(a, invite, user, addr string) []any
		code string
	}{
		{"null tenant", func(_, i, u, e string) []any { return []any{nil, i, u, e} }, "42501"},
		{"null invitation id", func(a, _, u, e string) []any { return []any{a, nil, u, e} }, "P0002"},
		{"null email", func(a, i, u, _ string) []any { return []any{a, i, u, nil} }, "P0002"},
		{"null user id", func(a, i, _, e string) []any { return []any{a, i, nil, e} }, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newNamedTenant(t, "Obi Partners")
			addr := uniqueAddr("tunde")
			inviteID := seedJoinInvite(t, a, "reviewer", addr, `now() + interval '1 day'`, "")
			args := c.args(a, inviteID, uuid.NewString(), addr)

			err := db.WithinTenantTx(ctx, h.app, a, func(tx pgx.Tx) error {
				var id, role string
				return tx.QueryRow(ctx, acceptByIDCall, args...).Scan(&id, &role)
			})

			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) {
				t.Fatalf("%s: want a pg refusal, got %v", c.name, err)
			}
			if pgErr.Code == "42883" {
				t.Fatalf("%s: function missing (42883): %v", c.name, err)
			}
			if c.code != "" && pgErr.Code != c.code {
				t.Errorf("%s: SQLSTATE = %s, want %s (%s)", c.name, pgErr.Code, c.code, pgErr.Message)
			}
			if n := mustCount(t, h.super, `SELECT count(*) FROM memberships WHERE tenant_id = $1`, a); n != 0 {
				t.Errorf("memberships in A = %d, want 0", n)
			}
			if s := inviteStatus(t, inviteID); s != "pending" {
				t.Errorf("invite status = %q, want pending", s)
			}
		})
	}
}

// raceUnderIdentityLock holds the user's advisory key, starts every call, waits until each one
// is blocked on it, then releases. It returns each call's id and error.
func raceUnderIdentityLock(t *testing.T, user string, calls ...func(ctx context.Context) (string, error)) ([]string, []error) {
	t.Helper()
	ctx := context.Background()
	lock, err := h.super.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire a superuser connection: %v", err)
	}
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1::text, 0))`, user); err != nil {
		lock.Release()
		t.Fatalf("take the identity lock: %v", err)
	}
	released := false
	var wg sync.WaitGroup
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

	ids, errs := make([]string, len(calls)), make([]error, len(calls))
	done := make(chan int, len(calls))
	for i, call := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids[i], errs[i] = call(ctx)
			done <- i
		}()
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		waiting := mustCount(t, h.super,
			`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted
			  AND ((classid::bigint << 32) | objid::bigint) = hashtextextended($1::text, 0)`, user)
		if waiting == len(calls) {
			break
		}
		select {
		case i := <-done:
			release()
			t.Fatalf("call %d returned while the identity lock was held, want it waiting: %v", i, errs[i])
		default:
		}
		if time.Now().After(deadline) {
			release()
			t.Fatalf("calls waiting on the identity key = %d, want %d", waiting, len(calls))
		}
		time.Sleep(20 * time.Millisecond)
	}
	release()
	return ids, errs
}

func TestRLS_AcceptByIdJoinsOnceUnderRace(t *testing.T) {
	requireHarness(t)
	t1, t2 := newNamedTenant(t, "Obi Partners"), newNamedTenant(t, "Other Firm")
	addr := uniqueAddr("tunde")
	i1 := seedJoinInvite(t, t1, "reviewer", addr, `now() + interval '1 day'`, "")
	i2 := seedJoinInvite(t, t2, "reviewer", addr, `now() + interval '2 days'`, "")
	user := uuid.NewString()
	accept := func(tenant, invite string) func(context.Context) (string, error) {
		return func(ctx context.Context) (string, error) {
			id, _, err := acceptByIDAs(ctx, tenant, tenant, invite, user, addr)
			return id, err
		}
	}

	_, errs := raceUnderIdentityLock(t, user, accept(t1, i1), accept(t2, i2))

	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
			continue
		}
		assertAcceptRefusal(t, "the losing accept", err, "23505", guardConstraint)
	}
	if wins != 1 {
		t.Errorf("successes = %d (errs %v), want exactly 1", wins, errs)
	}
	if n := membershipCount(t, user); n != 1 {
		t.Errorf("memberships for the user = %d, want 1", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM invitations WHERE id IN ($1, $2) AND status = 'accepted'`, i1, i2); n != 1 {
		t.Errorf("accepted invites = %d, want 1", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM invitations WHERE id IN ($1, $2) AND status = 'pending'`, i1, i2); n != 1 {
		t.Errorf("pending invites = %d, want 1", n)
	}
}

func TestRLS_AcceptByIdSameInviteUnderRace(t *testing.T) {
	requireHarness(t)
	a := newNamedTenant(t, "Obi Partners")
	addr := uniqueAddr("tunde")
	inviteID := seedJoinInvite(t, a, "reviewer", addr, `now() + interval '1 day'`, "")
	user := uuid.NewString()
	accept := func(ctx context.Context) (string, error) {
		id, _, err := acceptByIDAs(ctx, a, a, inviteID, user, addr)
		return id, err
	}

	ids, errs := raceUnderIdentityLock(t, user, accept, accept)

	wins := 0
	for i, err := range errs {
		if err == nil {
			wins++
			if ids[i] != inviteID {
				t.Errorf("winner returned invitation_id %s, want %s", ids[i], inviteID)
			}
			continue
		}
		assertAcceptRefusal(t, "the second accept of one invite", err, "P0002", notValidName)
	}
	if wins != 1 {
		t.Errorf("successes = %d (errs %v), want exactly 1", wins, errs)
	}
	if n := membershipCount(t, user); n != 1 {
		t.Errorf("memberships for the user = %d, want 1", n)
	}
	if s := inviteStatus(t, inviteID); s != "accepted" {
		t.Errorf("invite status = %q, want accepted", s)
	}
}

func TestRLS_PendingInvitesForEmailIsEmptyForAMember(t *testing.T) {
	requireHarness(t)
	roles := rolesInDB(t)
	addr := uniqueAddr("member")
	t1, t2 := newNamedTenant(t, "Obi Partners"), newNamedTenant(t, "Other Firm")
	invite := seedJoinInvite(t, t1, roles[0], addr, `now() + interval '1 day'`, "")

	if got := pendingForUser(t, "", addr, uuid.NewString()); len(got) != 1 || got[0].id != invite {
		t.Fatalf("member-less control = %+v, want the invite", got)
	}
	for _, status := range []string{"active", "suspended"} {
		user := uuid.NewString()
		if _, err := h.super.Exec(context.Background(),
			`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'admin', $3)`, t2, user, status); err != nil {
			t.Fatalf("seed %s membership: %v", status, err)
		}
		if got := pendingForUser(t, "", addr, user); len(got) != 0 {
			t.Errorf("a %s member's list = %+v, want empty", status, got)
		}
	}
}

func TestRLS_JoinFunctionsAreOwnedAndGrantedNarrowly(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	// invoice_tenant_reader holds no EXECUTE; the call is refused before it runs.
	for _, c := range []struct {
		name, sql string
		args      []any
	}{
		{"pending_invites_for_email", `SELECT count(*) FROM public.pending_invites_for_email($1::text, $2::uuid)`, []any{"a@obi.test", uuid.NewString()}},
		{"accept_invitation_by_id", `SELECT count(*) FROM public.accept_invitation_by_id($1::uuid, $2::uuid, $3::uuid, $4::text)`,
			[]any{uuid.NewString(), uuid.NewString(), uuid.NewString(), "a@obi.test"}},
	} {
		var n int
		assertPgRefusal(t, "invoice_tenant_reader calling "+c.name, h.reader.QueryRow(ctx, c.sql, c.args...).Scan(&n), "42501", "permission denied")
	}

	list, ok := readProc(t, pendingSig)
	if !ok {
		t.Fatalf("function %s does not exist", pendingSig)
	}
	if list.owner != "auth_hook_reader" {
		t.Errorf("%s owner = %q, want auth_hook_reader", pendingSig, list.owner)
	}
	if !list.secdef {
		t.Errorf("%s prosecdef = false, want SECURITY DEFINER", pendingSig)
	}
	if !reflect.DeepEqual(list.config, []string{`search_path=""`}) {
		t.Errorf("%s proconfig = %q, want [search_path=\"\"]", pendingSig, list.config)
	}
	if list.volatile != "s" {
		t.Errorf("%s provolatile = %q, want s (STABLE)", pendingSig, list.volatile)
	}
	assertExecuteOnlyForApp(t, pendingSig, list, "auth_hook_reader")

	acc, ok := readProc(t, acceptByIDSig)
	if !ok {
		t.Fatalf("function %s does not exist", acceptByIDSig)
	}
	if acc.owner != "invoice_migrator" {
		t.Errorf("%s owner = %q, want invoice_migrator", acceptByIDSig, acc.owner)
	}
	if !acc.secdef {
		t.Errorf("%s prosecdef = false, want SECURITY DEFINER", acceptByIDSig)
	}
	if !reflect.DeepEqual(acc.config, []string{`search_path=""`}) {
		t.Errorf("%s proconfig = %q, want [search_path=\"\"]", acceptByIDSig, acc.config)
	}
	assertExecuteOnlyForApp(t, acceptByIDSig, acc, "invoice_migrator")
}

// The control proves the list sees tenant 1 while the direct reads do not.
func TestRLS_JoinLookupDoesNotWidenTheApp(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	t1, t2 := newNamedTenant(t, "Obi Partners"), newNamedTenant(t, "Other Firm")
	addr := uniqueAddr("tunde")
	inviter := uuid.NewString()
	seedNamedMember(t, t1, inviter, "Ada Obi", "ada@obi.test")
	seedNamedMember(t, t2, uuid.NewString(), "Bo Two", "bo@other.test")
	seedJoinInvite(t, t1, "reviewer", addr, `now() + interval '1 day'`, inviter)
	seedJoinInvite(t, t2, "reviewer", uniqueAddr("bola"), `now() + interval '1 day'`, "")

	if got := pendingFor(t, t2, addr); len(got) != 1 || got[0].tenantID != t1 {
		t.Fatalf("control: list under GUC T2 = %+v, want the one tenant 1 invite", got)
	}

	for _, c := range []struct {
		scope, guc string
		want       int
	}{{"tenant 2 reads tenant 1", t2, 0}, {"tenant 1 reads itself (control)", t1, 1}} {
		err := db.WithinTenantTx(ctx, h.app, c.guc, func(tx pgx.Tx) error {
			for _, q := range []struct{ table, sql string }{
				{"invitations", `SELECT count(*) FROM invitations WHERE tenant_id = $1`},
				{"memberships", `SELECT count(*) FROM memberships WHERE tenant_id = $1`},
				{"tenants", `SELECT count(*) FROM tenants WHERE id = $1`},
			} {
				if n := mustCount(t, tx, q.sql, t1); n != c.want {
					t.Errorf("%s: %s rows of tenant 1 = %d, want %d", c.scope, q.table, n, c.want)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", c.scope, err)
		}
	}
}

// Drives the shipped Down and Up through goose; a Down that leaves a function or a grant fails here.
func TestRLS_JoinMigrationDownRemovesFunctionsAndGrantsThenReapplies(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	join := migrationVersion(t, "*_invitation_join_by_email.sql")
	sqlDB, err := sql.Open("pgx", os.Getenv("DATABASE_MIGRATION_URL"))
	if err != nil {
		t.Fatalf("open migrator connection: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatalf("build migration provider: %v", err)
	}
	footprint := func() (fns, grants int) {
		t.Helper()
		if err := h.super.QueryRow(ctx,
			`SELECT (SELECT count(*) FROM pg_proc WHERE proname IN ('pending_invites_for_email', 'accept_invitation_by_id')),
			        (SELECT count(*) FROM information_schema.column_privileges
			          WHERE grantee = 'auth_hook_reader'
			            AND ((table_name = 'memberships' AND column_name IN ('display_name', 'email'))
			              OR (table_name = 'invitations' AND column_name = 'invited_by')))`,
		).Scan(&fns, &grants); err != nil {
			t.Fatalf("read join footprint: %v", err)
		}
		return
	}
	if fns, grants := footprint(); fns != 2 || grants != 3 {
		t.Fatalf("before Down: functions=%d grants=%d, want 2 3", fns, grants)
	}
	applied := true
	t.Cleanup(func() {
		if !applied {
			if _, err := provider.ApplyVersion(context.Background(), join, true); err != nil {
				t.Errorf("restore the join migration: %v", err)
			}
		}
	})

	if _, err := provider.ApplyVersion(ctx, join, false); err != nil {
		t.Fatalf("roll back the join migration: %v", err)
	}
	applied = false
	if fns, grants := footprint(); fns != 0 || grants != 0 {
		t.Errorf("after Down: functions=%d grants=%d, want 0 0", fns, grants)
	}

	if _, err := provider.ApplyVersion(ctx, join, true); err != nil {
		t.Fatalf("re-apply the join migration: %v", err)
	}
	applied = true
	if fns, grants := footprint(); fns != 2 || grants != 3 {
		t.Errorf("after Up again: functions=%d grants=%d, want 2 3", fns, grants)
	}
}
