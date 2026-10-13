package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Runbook statements (Design, Runbook statements); subtask 07 copies them byte for byte.
const (
	grantHouseStaffSQL   = `INSERT INTO public.memberships (tenant_id, user_id, role, status, email) SELECT h.tenant_id, u.id, 'reviewer', 'active', u.email FROM public.house_company h CROSS JOIN auth.users u WHERE u.email = lower('<address>') ON CONFLICT (tenant_id, user_id) DO UPDATE SET status = 'active';`
	suspendHouseStaffSQL = `UPDATE public.memberships m SET status = 'suspended' FROM public.house_company h WHERE m.tenant_id = h.tenant_id AND m.user_id = (SELECT id FROM auth.users WHERE email = lower('<address>'));`

	devHouseTenant = "a5c0a5c0-a5c0-a5c0-a5c0-a5c0a5c0a5c0"
)

// runbookFor runs a runbook statement verbatim for an address as the superuser.
func runbookFor(t *testing.T, conn *pgx.Conn, stmt, address string) pgconn.CommandTag {
	t.Helper()
	tag, err := conn.Exec(context.Background(), strings.Replace(stmt, "<address>", address, 1))
	if err != nil {
		t.Fatalf("runbook statement: %v", err)
	}
	return tag
}

// runbook runs a runbook statement for u and asserts one row changed.
func runbook(t *testing.T, conn *pgx.Conn, stmt string, u idpUser) {
	t.Helper()
	if tag := runbookFor(t, conn, stmt, u.email); tag.RowsAffected() != 1 {
		t.Fatalf("runbook statement %q: %s, want one row", stmt[:6], tag)
	}
}

// houseTenant adopts the committed house row, writing the dev one only when the table is empty.
func houseTenant(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	ctx := context.Background()
	read := func() (string, error) {
		var id string
		err := conn.QueryRow(ctx, `SELECT tenant_id::text FROM public.house_company`).Scan(&id)
		return id, err
	}
	id, err := read()
	if errors.Is(err, pgx.ErrNoRows) {
		exec(t, conn, `INSERT INTO tenants (id, name) VALUES ($1, 'ASComply (dev)') ON CONFLICT DO NOTHING`, devHouseTenant)
		exec(t, conn, `INSERT INTO public.house_company (tenant_id) VALUES ($1) ON CONFLICT DO NOTHING`, devHouseTenant)
		id, err = read()
	}
	if err != nil {
		t.Fatalf("read the house row: %v", err)
	}
	return id
}

// grantHouseStaff runs the runbook grant for u and deletes the membership afterwards.
func grantHouseStaff(t *testing.T, conn *pgx.Conn, u idpUser) {
	t.Helper()
	house := houseTenant(t, conn)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DELETE FROM public.memberships WHERE tenant_id = $1 AND user_id = $2`, house, u.id)
	})
	runbook(t, conn, grantHouseStaffSQL, u)
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

func requireTenant(t *testing.T, label, tok string, want bool) {
	t.Helper()
	if v, has := appMetadataOf(t, tok)["tenant_id"]; has != want {
		t.Errorf("%s: app_metadata.tenant_id = %v, want present=%v", label, v, want)
	}
}

// verifiedStaff verifies tok against the real provider and asserts Identity.Staff.
func verifiedStaff(t *testing.T, base, label, tok string, wantStaff bool) auth.Identity {
	t.Helper()
	id, err := idpVerifier(t, base).Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify %s: %v", label, err)
	}
	if id.Staff != wantStaff {
		t.Errorf("%s: Identity.Staff = %v, want %v", label, id.Staff, wantStaff)
	}
	return id
}

func TestIdP_RegisteredAccountCarriesNoStaffClaim(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	houseTenant(t, conn) // an absent claim must not be an empty-table artefact
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

func TestIdP_HouseGrantReachesTheNextToken(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u, other := workspaceUser(t, base), workspaceUser(t, base)
	h := newRenewal(t, base)
	a0, r0 := h.session(t, u)
	requireNoStaff(t, "before the grant", a0)
	requireTenant(t, "before the grant", a0, true)
	_, otherR0 := h.session(t, other)

	grantHouseStaff(t, conn, u)

	// Two active memberships: staff, and no tenant (Core AC 3).
	a1, _ := h.renewOK(t, "after the grant", r0)
	requireStaff(t, "refresh after the grant", a1)
	requireTenant(t, "refresh after the grant", a1, false)
	requireVerifies(t, base, "refreshed staff token", a1)

	a2, _ := h.session(t, u)
	requireStaff(t, "sign-in after the grant", a2)
	requireTenant(t, "sign-in after the grant", a2, false)
	requireVerifies(t, base, "signed-in staff token", a2)

	o1, _ := h.renewOK(t, "other account after the grant", otherR0)
	requireNoStaff(t, "other account refresh", o1)
	o2, _ := h.session(t, other)
	requireNoStaff(t, "other account sign-in", o2)
}

func TestIdP_HouseGrantStatementMatchesTheAddressInAnyCase(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	house := houseTenant(t, conn)
	u := workspaceUser(t, base)

	mixed := strings.ToUpper(u.email)
	if mixed == u.email {
		t.Fatalf("control: %q has no letters to upper-case", u.email)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DELETE FROM public.memberships WHERE tenant_id = $1 AND user_id = $2`, house, u.id)
	})
	if tag := runbookFor(t, conn, grantHouseStaffSQL, mixed); tag.String() != "INSERT 0 1" {
		t.Fatalf("grant for %q: %q, want INSERT 0 1", mixed, tag)
	}
	if tag := runbookFor(t, conn, grantHouseStaffSQL, "nobody-"+u.id+"@example.test"); tag.String() != "INSERT 0 0" {
		t.Fatalf("grant for an unregistered address: %q, want INSERT 0 0", tag)
	}

	a, _ := newRenewal(t, base).session(t, u)
	requireStaff(t, "sign-in after the mixed-case grant", a)
}

func TestIdP_HouseSuspensionReachesTheNextToken(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u, keep := workspaceUser(t, base), workspaceUser(t, base)
	h := newRenewal(t, base)
	grantHouseStaff(t, conn, u)
	grantHouseStaff(t, conn, keep)
	a0, r0 := h.session(t, u)
	requireStaff(t, "before the suspension", a0)
	_, keepR0 := h.session(t, keep)

	runbook(t, conn, suspendHouseStaffSQL, u)

	// One active membership is left, so the workspace tenant comes back with the staff claim gone.
	a1, _ := h.renewOK(t, "after the suspension", r0)
	requireNoStaff(t, "refresh after the suspension", a1)
	requireTenant(t, "refresh after the suspension", a1, true)
	requireVerifies(t, base, "refreshed token after the suspension", a1)

	a2, _ := h.session(t, u)
	requireNoStaff(t, "sign-in after the suspension", a2)
	requireVerifies(t, base, "signed-in token after the suspension", a2)

	k1, _ := h.renewOK(t, "other staff after the suspension", keepR0)
	requireStaff(t, "other staff refresh", k1)
}

func TestIdP_StaffClaimSurvivesRenewalPastTheTTL(t *testing.T) {
	base := shortURL(t)
	conn := superConn(t)
	u := signUp(t, conn, base)
	h := newRenewal(t, base)
	grantHouseStaff(t, conn, u)
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

func TestIdP_CustomerAdminCannotPlantStaff(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	houseTenant(t, conn)        // an absent claim must not be an empty-table artefact
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
	id := verifiedStaff(t, base, "planted exact keys", a1, false)
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
	id = verifiedStaff(t, base, "planted case-variant keys", a2, false)
	if id.TenantID == "" {
		t.Error("control: verified identity has no tenant")
	}
}
