package auth_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The D13 runbook statements.
const (
	grantStaffSQL  = `INSERT INTO public.staff_members (user_id) SELECT id FROM auth.users WHERE email = lower('<address>');`
	removeStaffSQL = `DELETE FROM public.staff_members WHERE user_id = (SELECT id FROM auth.users WHERE email = lower('<address>'));`

	// The D18 rules-role statements.
	grantRulesRoleSQL  = `UPDATE public.staff_members SET rules_role = true WHERE user_id = (SELECT id FROM auth.users WHERE email = lower('<address>'));`
	removeRulesRoleSQL = `UPDATE public.staff_members SET rules_role = false WHERE user_id = (SELECT id FROM auth.users WHERE email = lower('<address>'));`
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

// appMetadataOf returns the token's app_metadata object.
func appMetadataOf(t *testing.T, tok string) map[string]any {
	t.Helper()
	am, _ := jwtPart(t, tok, 1)["app_metadata"].(map[string]any)
	if am == nil {
		t.Fatal("token has no app_metadata object")
	}
	return am
}

// requireIdentity verifies tok against the real provider and asserts (Staff, RulesRole).
func requireIdentity(t *testing.T, base, label, tok string, wantStaff, wantRules bool) auth.Identity {
	t.Helper()
	id, err := idpVerifier(t, base).Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify %s: %v", label, err)
	}
	if staff, rules := flagsOf(t, id); staff != wantStaff || rules != wantRules {
		t.Errorf("%s: (Staff, RulesRole) = (%v, %v), want (%v, %v)", label, staff, rules, wantStaff, wantRules)
	}
	return id
}

func TestIdP_RulesRoleGrantReachesTheNextToken(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u, other := workspaceUser(t, base), workspaceUser(t, base)
	h := newRenewal(t, base)
	// The other account is staff without the rules role: a grant that touched every row would show.
	grantStaff(t, conn, u)
	grantStaff(t, conn, other)
	a0, r0 := h.session(t, u)
	requireIdentity(t, base, "before the rules grant", a0, true, false)
	_, otherR0 := h.session(t, other)

	runbook(t, conn, grantRulesRoleSQL, u)

	a1, _ := h.renewOK(t, "after the rules grant", r0)
	requireIdentity(t, base, "refresh after the rules grant", a1, true, true)
	a2, _ := h.session(t, u)
	requireIdentity(t, base, "sign-in after the rules grant", a2, true, true)

	o1, _ := h.renewOK(t, "other account after the rules grant", otherR0)
	requireIdentity(t, base, "other account refresh", o1, true, false)
	o2, _ := h.session(t, other)
	requireIdentity(t, base, "other account sign-in", o2, true, false)
}

func TestIdP_RulesRoleRemovalReachesTheNextToken(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u, keep := workspaceUser(t, base), workspaceUser(t, base)
	h := newRenewal(t, base)
	for _, x := range []idpUser{u, keep} {
		grantStaff(t, conn, x)
		runbook(t, conn, grantRulesRoleSQL, x)
	}
	a0, r0 := h.session(t, u)
	requireIdentity(t, base, "before the rules removal", a0, true, true)
	_, keepR0 := h.session(t, keep)

	runbook(t, conn, removeRulesRoleSQL, u)

	a1, _ := h.renewOK(t, "after the rules removal", r0)
	requireIdentity(t, base, "refresh after the rules removal", a1, true, false)
	a2, _ := h.session(t, u)
	requireIdentity(t, base, "sign-in after the rules removal", a2, true, false)

	k1, _ := h.renewOK(t, "other account after the rules removal", keepR0)
	requireIdentity(t, base, "other account refresh", k1, true, true)
}

func TestIdP_CustomerAdminCannotPlantStaffOrRulesRole(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u := workspaceUser(t, base) // a workspace admin: the customer, not staff
	h := newRenewal(t, base)

	exec(t, conn, `UPDATE auth.users SET raw_app_meta_data = raw_app_meta_data || '{"staff": true, "rules_role": true}'::jsonb WHERE id = $1`, u.id)
	a1, _ := h.session(t, u)
	am := appMetadataOf(t, a1)
	for _, k := range []string{"staff", "rules_role"} {
		if v, ok := am[k]; ok {
			t.Errorf("planted exact key %q reached the token as %v, want stripped", k, v)
		}
	}
	if am["tenant_id"] == nil {
		t.Error("control: workspace token lost tenant_id")
	}
	id := requireIdentity(t, base, "planted exact keys", a1, false, false)
	if id.TenantID == "" {
		t.Error("control: verified identity has no tenant")
	}

	// The hook strips exact keys only; the verifier must ignore these.
	exec(t, conn, `UPDATE auth.users SET raw_app_meta_data = (raw_app_meta_data - 'staff' - 'rules_role') || '{"Staff": true, "RULES_ROLE": true}'::jsonb WHERE id = $1`, u.id)
	a2, _ := h.session(t, u)
	am = appMetadataOf(t, a2)
	if am["Staff"] != true || am["RULES_ROLE"] != true {
		t.Fatalf("control: case-variant keys did not survive the hook: Staff=%v RULES_ROLE=%v", am["Staff"], am["RULES_ROLE"])
	}
	for _, k := range []string{"staff", "rules_role"} {
		if v, ok := am[k]; ok {
			t.Errorf("exact key %q appeared beside the variants as %v", k, v)
		}
	}
	id = requireIdentity(t, base, "planted case-variant keys", a2, false, false)
	if id.TenantID == "" {
		t.Error("control: verified identity has no tenant")
	}
}

func TestIdP_RulesRoleGrantOnANonStaffAccountChangesNothing(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u := workspaceUser(t, base)

	if tag := runbookFor(t, conn, grantRulesRoleSQL, u.email); tag.String() != "UPDATE 0" {
		t.Fatalf("rules-role grant on a non-staff account: %q, want UPDATE 0", tag)
	}
	var rows int
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM public.staff_members WHERE user_id = $1`, u.id).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("staff_members rows for the account = %d (err %v), want 0", rows, err)
	}

	a, _ := newRenewal(t, base).session(t, u)
	if v, ok := appMetadataOf(t, a)["rules_role"]; ok {
		t.Errorf("non-staff token carries rules_role = %v, want absent", v)
	}
	requireIdentity(t, base, "non-staff account after the grant", a, false, false)

	// Control: the same statement on a staff account answers UPDATE 1.
	s := workspaceUser(t, base)
	grantStaff(t, conn, s)
	if tag := runbookFor(t, conn, grantRulesRoleSQL, s.email); tag.String() != "UPDATE 1" {
		t.Errorf("rules-role grant on a staff account: %q, want UPDATE 1", tag)
	}
}
