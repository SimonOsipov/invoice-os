package auth_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestIdP_ConfirmThenSignInAdmitsTheInviteeToTheJoinRoutes(t *testing.T) {
	pw := "pw-" + uuid.NewString()
	w := newPreRegisteredInviteWorld(t, "logfix04a-", pw, false)

	if got := clickWithState(t, confirmationLink(t, w.email), newState(t), pw); got != siteURL+"/?verified=1" {
		t.Fatalf("confirm click = %q, want %s/?verified=1", got, siteURL)
	}
	access := accessToken(t, w.base, idpUser{email: w.email, password: pw})
	caller, err := idpVerifier(t, w.base).Verify(context.Background(), access)
	if err != nil || caller.TenantID != "" {
		t.Fatalf("Verify the signed-in token: identity %+v, err %v; want a tenant-less identity", caller, err)
	}
	newJoinEdge(t, w.base).requireJoinRoutes(t, access)
}

func TestIdP_ConfirmTwiceConfirmsOnceThenFails(t *testing.T) {
	base := idpMailURL(t)
	gw, logs := startGateway(t, base, 0, nil)
	u := registrant(t, gw)
	link := confirmationLink(t, u.email)

	if got := clickWithState(t, link, newState(t), u.password); got != siteURL+"/?verified=1" {
		t.Fatalf("first click = %q, want %s/?verified=1", got, siteURL)
	}
	if got := clickWithState(t, link, newState(t), u.password); got != siteURL+"/?verify=failed" {
		t.Errorf("second click = %q, want %s/?verify=failed", got, siteURL)
	}
	if n := sessionRows(t, u.email); n != 0 {
		t.Errorf("%d session rows after the confirms, want 0: a confirm mints no session", n)
	}
	if !strings.Contains(logs.String(), `"msg":"invitation-password: gotrue refused the link","upstream_status":403`) {
		t.Errorf("gateway log lacks the refused link with upstream_status 403: %s", logs)
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
