package auth_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Session limits on idp-session: 20 s from sign-in, 8 s without a refresh.
const (
	sessionTimebox = 20 * time.Second
	sessionIdle    = 8 * time.Second
)

// sessionURL returns idp-session's base URL.
func sessionURL(t *testing.T) string {
	t.Helper()
	idpURL(t)
	return strings.TrimRight(requireEnv(t, "IDP_SESSION_URL"), "/")
}

// goTrueRefresh refreshes straight against GoTrue and returns the status, error_code and msg.
func goTrueRefresh(t *testing.T, base, refreshToken string) (int, string, string) {
	t.Helper()
	status, m := postJSON(t, base+"/token?grant_type=refresh_token", map[string]string{"refresh_token": refreshToken})
	code, _ := m["error_code"].(string)
	msg, _ := m["msg"].(string)
	return status, code, msg
}

// requireRefused expects GoTrue's session_expired and the gateway's 401 for the same token.
func requireRefused(t *testing.T, h renewal, base, label, refreshToken, wantMsg string) {
	t.Helper()
	status, code, msg := goTrueRefresh(t, base, refreshToken)
	if status != http.StatusBadRequest || code != "session_expired" {
		t.Errorf("%s: GoTrue answered %d %q (%q), want 400 session_expired", label, status, code, msg)
	}
	if msg != wantMsg {
		t.Errorf("%s: GoTrue msg = %q, want %q", label, msg, wantMsg)
	}
	gs, m := h.renew(t, refreshToken)
	if gs != http.StatusUnauthorized || m["error"] != "invalid or expired refresh token" {
		t.Errorf("%s: gateway answered %d %q, want 401 invalid or expired refresh token", label, gs, m["error"])
	}
}

// sessionSubjects returns a customer and a staff user; the staff token is checked at sign-in.
func sessionSubjects(t *testing.T, base string) map[string]idpUser {
	t.Helper()
	conn := superConn(t)
	staff := signUp(t, conn, base)
	grantHouseStaff(t, conn, staff)
	return map[string]idpUser{"customer": signUp(t, conn, base), "staff": staff}
}

func TestIdP_SessionPastTheTimeboxIsRefused(t *testing.T) {
	base := sessionURL(t)
	h := newRenewal(t, base)
	for name, u := range sessionSubjects(t, base) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			before0 := time.Now() // before sign-in: never undercounts the session's age
			a, r := h.session(t, u)
			created := time.Now() // after sign-in: never overcounts it (GoTrue counts from created_at)
			if name == "staff" {
				requireStaff(t, "exchanged token", a)
			}
			// Always the latest token, so the parent-token exception never applies.
			for {
				time.Sleep(2 * time.Second)
				before := time.Since(created)
				status, m := h.renew(t, r)
				after := time.Since(before0)
				if status == http.StatusOK {
					if before >= sessionTimebox+2*time.Second {
						t.Fatalf("refresh at %s answered 200; the %s timebox did not end the session", before, sessionTimebox)
					}
					r = m["refresh_token"]
					continue
				}
				if after < sessionTimebox {
					t.Fatalf("refresh at %s answered %d; the session is younger than %s", after, status, sessionTimebox)
				}
				break
			}
			requireRefused(t, h, base, name+" past the timebox", r, "Invalid Refresh Token: Session Expired")
		})
	}
}

func TestIdP_IdleSessionIsRefused(t *testing.T) {
	base := sessionURL(t)
	h := newRenewal(t, base)
	subjects := sessionSubjects(t, base)
	start := time.Now()
	tokens := map[string]string{}
	var lastRefresh time.Time
	for name, u := range subjects {
		a, r := h.session(t, u)
		if name == "staff" {
			requireStaff(t, "exchanged token", a)
		}
		// One immediate refresh proves the session is live before it goes idle.
		_, tokens[name] = h.renewOK(t, name+" control", r)
		lastRefresh = time.Now()
	}
	// The customer's second sign-in (another device) keeps refreshing while the first goes idle.
	_, c := h.session(t, subjects["customer"])
	// Scheduled from the idle sessions' last refresh, so this gap stays well under sessionIdle on a slow runner.
	for _, at := range []time.Duration{3 * time.Second, 6 * time.Second} {
		time.Sleep(time.Until(lastRefresh.Add(at)))
		_, c = h.renewOK(t, "other device", c)
	}

	time.Sleep(time.Until(lastRefresh.Add(sessionIdle + time.Second)))
	if age := time.Since(start); age >= sessionTimebox {
		t.Fatalf("control: the sessions are %s old; the timebox would end them, not the idle limit", age)
	}
	for name, r := range tokens {
		requireRefused(t, h, base, name+" idle", r, "Invalid Refresh Token: Session Expired (Inactivity)")
	}
	h.renewOK(t, "other device after the first session's refusal", c)
}
