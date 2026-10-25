package auth_test

import (
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth/gotrueadmin"
)

// adminClient signs with the key idp-es256 runs with, which idp-up.sh writes to IDP_ES256_KEYS_FILE.
func adminClient(t *testing.T, base string) *gotrueadmin.Client {
	t.Helper()
	raw, err := os.ReadFile(requireEnv(t, "IDP_ES256_KEYS_FILE"))
	if err != nil {
		t.Fatalf("read IDP_ES256_KEYS_FILE: %v", err)
	}
	c, err := gotrueadmin.New(base, string(raw), idpHTTP)
	if err != nil {
		t.Fatalf("gotrueadmin.New: %v", err)
	}
	return c
}

func deleteAs(t *testing.T, c *gotrueadmin.Client, id string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.DeleteUser(ctx, id)
}

func countAuthRows(t *testing.T, conn *pgx.Conn, sql, id string) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(context.Background(), sql, id).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

const (
	usersSQL      = `SELECT count(*) FROM auth.users WHERE id = $1::uuid`
	identitiesSQL = `SELECT count(*) FROM auth.identities WHERE user_id = $1::uuid`
	sessionsSQL   = `SELECT count(*) FROM auth.sessions WHERE user_id = $1::uuid`
	refreshSQL    = `SELECT count(*) FROM auth.refresh_tokens WHERE user_id = $1::text`
	factorsSQL    = `SELECT count(*) FROM auth.mfa_factors WHERE user_id = $1::uuid`
)

func TestIdP_AdminDeleteRemovesTheAccountAndItsSessions(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	admin := adminClient(t, base)
	r := newRenewal(t, base)
	u := workspaceUser(t, base)
	_, refresh := r.session(t, u)
	exec(t, conn, `INSERT INTO auth.mfa_factors (id, user_id, friendly_name, factor_type, status, created_at, updated_at, secret)
		VALUES (gen_random_uuid(), $1::uuid, 'throwaway', 'totp', 'verified', now(), now(), 'secret')`, u.id)

	// Control: another account whose refresh token must survive.
	bystander := workspaceUser(t, base)
	_, bystanderRefresh := r.session(t, bystander)

	for name, sql := range map[string]string{"users": usersSQL, "identities": identitiesSQL, "sessions": sessionsSQL, "refresh_tokens": refreshSQL, "mfa_factors": factorsSQL} {
		if n := countAuthRows(t, conn, sql, u.id); n == 0 {
			t.Fatalf("control: auth.%s holds no row for the user before the delete", name)
		}
	}

	if err := deleteAs(t, admin, u.id); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	for name, sql := range map[string]string{"users": usersSQL, "identities": identitiesSQL, "sessions": sessionsSQL, "refresh_tokens": refreshSQL, "mfa_factors": factorsSQL} {
		if n := countAuthRows(t, conn, sql, u.id); n != 0 {
			t.Errorf("auth.%s holds %d rows for the deleted user, want 0", name, n)
		}
	}

	status, body := postJSON(t, base+"/token?grant_type=refresh_token", map[string]string{"refresh_token": refresh})
	if status < 400 || status >= 500 {
		t.Errorf("refresh grant with the deleted user's token: status %d, keys %v; want 4xx", status, body["error_code"])
	}
	if status, body := postJSON(t, base+"/token?grant_type=refresh_token", map[string]string{"refresh_token": bystanderRefresh}); status != http.StatusOK {
		t.Errorf("control: the other account's refresh grant: status %d, error_code %v; want 200", status, body["error_code"])
	}
	if n := countAuthRows(t, conn, usersSQL, bystander.id); n != 1 {
		t.Errorf("control: the other account has %d users rows, want 1", n)
	}
}

func TestIdP_AdminDeleteOfAnUnknownUserIsSuccess(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	admin := adminClient(t, base)

	if err := deleteAs(t, admin, uuid.NewString()); err != nil {
		t.Errorf("DeleteUser of an unknown id = %v, want nil", err)
	}

	u := signUp(t, conn, base)
	if err := deleteAs(t, admin, u.id); err != nil {
		t.Fatalf("control: DeleteUser of a real account = %v", err)
	}
	if n := countAuthRows(t, conn, usersSQL, u.id); n != 0 {
		t.Fatalf("control: the account is still there (%d rows)", n)
	}
	if err := deleteAs(t, admin, u.id); err != nil {
		t.Errorf("second DeleteUser of the same id = %v, want nil", err)
	}
}

func TestIdP_DeletedUsersTokenIsRevokedAtTheSessionCheck(t *testing.T) {
	base := idpURL(t)
	admin := adminClient(t, base)
	u := workspaceUser(t, base)
	r := newRevocation(t, base)
	a, _ := r.session(t, u)

	r.expect(t, "live at t0", a, http.StatusOK, 1)
	if err := deleteAs(t, admin, u.id); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	r.clk.set(revocationT0.Add(gateway.SessionCheckTTL - time.Second))
	r.expect(t, "inside the cache window", a, http.StatusOK, 2)
	r.clk.set(revocationT0.Add(gateway.SessionCheckTTL + time.Second))
	r.expect(t, "past SessionCheckTTL", a, http.StatusUnauthorized, 2)

	req, err := http.NewRequest(http.MethodGet, base+"/user", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+a)
	resp, err := idpHTTP.Do(req)
	if err != nil {
		t.Fatalf("GET /user: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(raw), `"error_code":"user_not_found"`) {
		t.Errorf("GET /user for the deleted user: status %d, body %s; want 403 user_not_found", resp.StatusCode, raw)
	}
}

func contains(s, sub string) bool { return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestIdP_AnHS256ServiceRoleTokenIsRefused(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u := signUp(t, conn, base)
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"role": "service_role", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix(),
	}).SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodDelete, base+"/admin/users/"+u.id, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := idpHTTP.Do(req)
	if err != nil {
		t.Fatalf("DELETE /admin/users: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("HS256 service_role token: status %d, want 403", resp.StatusCode)
	}
	if n := countAuthRows(t, conn, usersSQL, u.id); n != 1 {
		t.Fatalf("the refused call still changed auth.users: %d rows, want 1", n)
	}

	// Control: the ES256 key idp-es256 runs with does delete.
	if err := deleteAs(t, adminClient(t, base), u.id); err != nil {
		t.Fatalf("control: DeleteUser with the real key = %v", err)
	}
	if n := countAuthRows(t, conn, usersSQL, u.id); n != 0 {
		t.Errorf("control: the real key left %d rows", n)
	}
}
