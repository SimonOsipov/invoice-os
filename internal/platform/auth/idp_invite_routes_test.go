package auth_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// Route 1 (D6): /auth/register for an invited address answers like any other and creates nothing.
func TestIdP_SignUpForAnInvitedAddressCreatesNoAccount(t *testing.T) {
	w := newInviteWorldAt(t, "resend2-01r1-", "example.com")
	conn := superConn(t)
	control := "resend2-01r1-ctl-" + uuid.NewString() + "@example.com"
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `DELETE FROM auth.users WHERE email = $1`, control) })
	password := "pw-" + uuid.NewString()

	invitedStatus, invitedBody := postGW(t, w.gw+"/auth/register", map[string]string{"email": w.email, "password": password})
	controlStatus, controlBody := postGW(t, w.gw+"/auth/register", map[string]string{"email": control, "password": password})

	if invitedStatus != http.StatusAccepted || controlStatus != http.StatusAccepted {
		t.Fatalf("register: invited %d %s, control %d %s; want 202 for both", invitedStatus, invitedBody, controlStatus, controlBody)
	}
	if invitedBody != controlBody {
		t.Errorf("invited body %s differs from the control's %s", invitedBody, controlBody)
	}

	if n := mailCount(t, control); n != 1 {
		t.Fatalf("control mails = %d, want 1: the control proves a sign-up still sends", n)
	}
	if n := mailCount(t, w.email); n != 0 {
		t.Errorf("invited address mails = %d, want 0", n)
	}
	for _, c := range []struct {
		email string
		want  int
	}{{control, 1}, {w.email, 0}} {
		var n int
		if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM auth.users WHERE email = $1`, c.email).Scan(&n); err != nil {
			t.Fatalf("count auth.users: %v", err)
		}
		if n != c.want {
			t.Errorf("auth.users rows for %s = %d, want %d", c.email, n, c.want)
		}
	}

	status, grant := signIn(t, w.base, idpUser{email: w.email, password: password})
	if status != http.StatusBadRequest || grant["error_code"] != "invalid_credentials" {
		t.Errorf("password grant for the invited address: status %d, body %v; want 400 invalid_credentials", status, grant)
	}
}
