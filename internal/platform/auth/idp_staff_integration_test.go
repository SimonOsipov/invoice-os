package auth_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The D13 runbook statements; .claude/rules/auth.md copies them verbatim.
const (
	grantStaffSQL  = `INSERT INTO public.staff_members (user_id) SELECT id FROM auth.users WHERE email = lower('<address>');`
	removeStaffSQL = `DELETE FROM public.staff_members WHERE user_id = (SELECT id FROM auth.users WHERE email = lower('<address>'));`
)

// runbookFor runs a D13 statement verbatim for an address as the superuser.
func runbookFor(t *testing.T, conn *pgx.Conn, stmt, address string) pgconn.CommandTag {
	t.Helper()
	tag, err := conn.Exec(context.Background(), strings.Replace(stmt, "<address>", address, 1))
	if err != nil {
		t.Fatalf("runbook statement: %v", err)
	}
	return tag
}

// runbook runs a D13 statement for u and asserts one row changed.
func runbook(t *testing.T, conn *pgx.Conn, stmt string, u idpUser) {
	t.Helper()
	if tag := runbookFor(t, conn, stmt, u.email); tag.RowsAffected() != 1 {
		t.Fatalf("runbook statement %q: %s, want one row", stmt[:6], tag)
	}
}

// grantStaff grants through the runbook and removes the row afterwards (it has no FK to auth.users).
func grantStaff(t *testing.T, conn *pgx.Conn, u idpUser) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DELETE FROM public.staff_members WHERE user_id = $1`, u.id)
	})
	runbook(t, conn, grantStaffSQL, u)
}

// staffClaim returns app_metadata.staff and whether the key is present.
func staffClaim(t *testing.T, tok string) (any, bool) {
	t.Helper()
	am, _ := jwtPart(t, tok, 1)["app_metadata"].(map[string]any)
	v, ok := am["staff"]
	return v, ok
}

func requireStaff(t *testing.T, label, tok string) {
	t.Helper()
	if v, _ := staffClaim(t, tok); v != true {
		t.Errorf("%s: app_metadata.staff = %v, want true", label, v)
	}
}

func requireNoStaff(t *testing.T, label, tok string) {
	t.Helper()
	if v, ok := staffClaim(t, tok); ok {
		t.Errorf("%s: app_metadata.staff = %v, want absent", label, v)
	}
}

func requireVerifies(t *testing.T, base, label, tok string) {
	t.Helper()
	if _, err := idpVerifier(t, base).Verify(context.Background(), tok); err != nil {
		t.Errorf("Verify %s: %v", label, err)
	}
}

func TestIdP_RegisteredAccountCarriesNoStaffClaim(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u := workspaceUser(t, base)
	// A claim planted in the provider's own app metadata must not reach the token.
	exec(t, conn, `UPDATE auth.users SET raw_app_meta_data = raw_app_meta_data || '{"staff": true}'::jsonb WHERE id = $1`, u.id)
	a, _ := newRenewal(t, base).session(t, u)

	requireNoStaff(t, "registered account", a)
	requireVerifies(t, base, "registered account token", a)

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read %s: %v", goldenPath, err)
	}
	var fx goldenFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("unmarshal %s: %v", goldenPath, err)
	}
	if len(fx.DecodedClaims) == 0 {
		t.Fatalf("fixture %s has no decoded claims", goldenPath)
	}
	assertSameShape(t, "claims", jwtPart(t, a, 1), fx.DecodedClaims)
}

func TestIdP_StaffGrantReachesTheNextToken(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u, other := workspaceUser(t, base), workspaceUser(t, base)
	h := newRenewal(t, base)
	a0, r0 := h.session(t, u)
	requireNoStaff(t, "before the grant", a0)
	_, otherR0 := h.session(t, other)

	grantStaff(t, conn, u)

	a1, _ := h.renewOK(t, "after the grant", r0)
	requireStaff(t, "refresh after the grant", a1)
	requireVerifies(t, base, "refreshed staff token", a1)

	a2, _ := h.session(t, u)
	requireStaff(t, "sign-in after the grant", a2)
	requireVerifies(t, base, "signed-in staff token", a2)

	o1, _ := h.renewOK(t, "other account after the grant", otherR0)
	requireNoStaff(t, "other account refresh", o1)
	o2, _ := h.session(t, other)
	requireNoStaff(t, "other account sign-in", o2)
}

func TestIdP_StaffGrantStatementMatchesTheAddressInAnyCase(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u := workspaceUser(t, base)
	h := newRenewal(t, base)
	_, r0 := h.session(t, u)

	if tag := runbookFor(t, conn, grantStaffSQL, "nobody-"+u.id+"@example.test"); tag.RowsAffected() != 0 {
		t.Fatalf("grant for an unregistered address: %s, want no rows", tag)
	}
	mixed := strings.ToUpper(u.email)
	if mixed == u.email {
		t.Fatalf("control: %q has no letters to upper-case", u.email)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DELETE FROM public.staff_members WHERE user_id = $1`, u.id)
	})
	if tag := runbookFor(t, conn, grantStaffSQL, mixed); tag.RowsAffected() != 1 {
		t.Fatalf("grant for %q: %s, want one row", mixed, tag)
	}

	a1, _ := h.renewOK(t, "after the mixed-case grant", r0)
	requireStaff(t, "refresh after the mixed-case grant", a1)
}

func TestIdP_StaffRemovalReachesTheNextToken(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u, keep := workspaceUser(t, base), workspaceUser(t, base)
	h := newRenewal(t, base)
	grantStaff(t, conn, u)
	grantStaff(t, conn, keep)
	a0, r0 := h.session(t, u)
	requireStaff(t, "before the removal", a0)
	_, keepR0 := h.session(t, keep)

	runbook(t, conn, removeStaffSQL, u)

	a1, _ := h.renewOK(t, "after the removal", r0)
	requireNoStaff(t, "refresh after the removal", a1)
	requireVerifies(t, base, "refreshed token after the removal", a1)

	a2, _ := h.session(t, u)
	requireNoStaff(t, "sign-in after the removal", a2)
	requireVerifies(t, base, "signed-in token after the removal", a2)

	k1, _ := h.renewOK(t, "other staff after the removal", keepR0)
	requireStaff(t, "other staff refresh", k1)
}

func TestIdP_StaffClaimSurvivesRenewalPastTheTTL(t *testing.T) {
	base := shortURL(t)
	conn := superConn(t)
	u := signUp(t, conn, base)
	h := newRenewal(t, base)
	grantStaff(t, conn, u)
	a0, r0 := h.session(t, u)
	requireStaff(t, "staff session", a0)

	exp0 := claimInt(t, a0, "exp")
	time.Sleep(time.Until(time.Unix(exp0+2, 0)))
	if now := time.Now().Unix(); now <= exp0 {
		t.Fatalf("control: now %d is not past exp %d", now, exp0)
	}

	a1, _ := h.renewOK(t, "R0 after expiry", r0)
	requireVerifies(t, base, "renewed staff token", a1)
	requireStaff(t, "refresh past the TTL", a1)
}
