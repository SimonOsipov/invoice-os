package auth_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// exchange redeems code with state at the gateway and returns the status and JSON body.
func exchange(t *testing.T, gw, code, state string) (int, map[string]any) {
	t.Helper()
	return postJSON(t, gw+"/auth/exchange", map[string]string{"code": code, "state": state})
}

func TestIdP_ConfirmClickSignsTheInviteeInToTheJoinRoutes(t *testing.T) {
	pw := "pw-" + uuid.NewString()
	w := newPreRegisteredInviteWorld(t, "logfix04a-", pw, false)

	code, state := followSignedIn(t, confirmationLink(t, w.email), pw)
	status, session := exchange(t, w.gw, code, state)
	access, _ := session["access_token"].(string)
	if status != http.StatusOK || access == "" {
		t.Fatalf("exchange: status %d, body %v; want 200 with an access token", status, session)
	}
	caller, err := idpVerifier(t, w.base).Verify(context.Background(), access)
	if err != nil || caller.TenantID != "" {
		t.Fatalf("Verify the exchanged token: identity %+v, err %v; want a tenant-less identity", caller, err)
	}
	newJoinEdge(t, w.base).requireJoinRoutes(t, access)
}

func TestIdP_ConfirmClickSessionRefreshes(t *testing.T) {
	pw := "pw-" + uuid.NewString()
	w := newPreRegisteredInviteWorld(t, "logfix04b-", pw, false)

	code, state := followSignedIn(t, confirmationLink(t, w.email), pw)
	status, session := exchange(t, w.gw, code, state)
	refresh, _ := session["refresh_token"].(string)
	if status != http.StatusOK || refresh == "" {
		t.Fatalf("exchange: status %d, body %v; want 200 with a refresh token", status, session)
	}
	status, next := postJSON(t, w.base+"/token?grant_type=refresh_token", map[string]string{"refresh_token": refresh})
	if got, _ := next["access_token"].(string); status != http.StatusOK || got == "" {
		t.Fatalf("refresh grant with the exchanged token: status %d, body %v; want 200", status, next)
	}
}

func TestIdP_ConfirmTwiceSignsInOnceThenFails(t *testing.T) {
	base := idpMailURL(t)
	gw, logs := startGateway(t, base, 0, nil)
	u := registrant(t, gw)
	link := confirmationLink(t, u.email)

	if code, _ := followSignedIn(t, link, u.password); code == "" {
		t.Fatal("first click returned no code")
	}
	if got := clickWithState(t, link, newState(t), u.password); got != siteURL+"/?verify=failed" {
		t.Errorf("second click = %q, want %s/?verify=failed", got, siteURL)
	}
	if !strings.Contains(logs.String(), `"msg":"invitation-password: gotrue refused the link","upstream_status":403`) {
		t.Errorf("gateway log lacks the refused link with upstream_status 403: %s", logs)
	}
}

func TestIdP_ConfirmOnAnotherDeviceSignsInThere(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	s1, s2 := newState(t), newState(t)

	u1 := registrant(t, gw)
	code := handoffCode(t, clickWithState(t, confirmationLink(t, u1.email), s2, u1.password))
	if status, body := exchange(t, gw, code, s1); status != http.StatusBadRequest {
		t.Fatalf("exchange with the other device's state: status %d, body %v; want 400", status, body)
	}

	u2 := registrant(t, gw)
	code = handoffCode(t, clickWithState(t, confirmationLink(t, u2.email), s2, u2.password))
	status, session := exchange(t, gw, code, s2)
	if got, _ := session["access_token"].(string); status != http.StatusOK || got == "" {
		t.Fatalf("exchange with the clicking device's state: status %d, body %v; want 200", status, session)
	}
}

func TestIdP_ConfirmAfterSignInFails(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	u := registrant(t, gw)
	link := confirmationLink(t, u.email)

	if got := follow(t, link, u.password); got != siteURL+"/?verified=1" {
		t.Fatalf("stateless confirm = %q, want %s/?verified=1", got, siteURL)
	}
	accessToken(t, base, u)
	if got := clickWithState(t, link, newState(t), u.password); got != siteURL+"/?verify=failed" {
		t.Errorf("click after sign-in = %q, want %s/?verify=failed", got, siteURL)
	}
}

// handoffCode returns the exchange code in a confirm redirect.
func handoffCode(t *testing.T, location string) string {
	t.Helper()
	code, ok := strings.CutPrefix(location, siteURL+"/?verified=1&handoff=")
	if !ok || code == "" {
		t.Fatalf("confirm redirect = %q, want %s/?verified=1&handoff=<code>", location, siteURL)
	}
	return code
}
