package auth_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// shortURL returns idp-short's base URL; its access tokens live 5 s.
func shortURL(t *testing.T) string {
	t.Helper()
	idpURL(t)
	return strings.TrimRight(requireEnv(t, "IDP_SHORT_URL"), "/")
}

type renewal struct {
	handoff
	refresh http.Handler
}

func newRenewal(t *testing.T, base string) renewal {
	t.Helper()
	authURL, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	return renewal{
		handoff: newHandoff(t, base),
		refresh: gateway.RefreshHandler(authURL, idpHTTP, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
}

// session signs u in through the hand-off and returns the exchanged access and refresh tokens.
func (h renewal) session(t *testing.T, u idpUser) (string, string) {
	t.Helper()
	s := newState(t)
	status, body := h.redeem(t, h.code(t, u, s), s)
	if status != http.StatusOK {
		t.Fatalf("exchange: status %d, error %q; want 200", status, field(t, body, "error"))
	}
	m := fields(t, body)
	if m["access_token"] == "" || m["refresh_token"] == "" || len(m) != 2 {
		t.Fatalf("exchange body keys %v, want exactly a non-empty access_token and refresh_token", keys(m))
	}
	return m["access_token"], m["refresh_token"]
}

func (h renewal) renew(t *testing.T, refreshToken string) (int, map[string]string) {
	t.Helper()
	status, body := serveJSON(t, h.refresh, "/auth/refresh", map[string]string{"refresh_token": refreshToken})
	return status, fields(t, body)
}

// renewOK renews and returns the new access and refresh tokens.
func (h renewal) renewOK(t *testing.T, label, refreshToken string) (string, string) {
	t.Helper()
	status, m := h.renew(t, refreshToken)
	if status != http.StatusOK || m["access_token"] == "" || m["refresh_token"] == "" || len(m) != 2 {
		t.Fatalf("refresh %s: status %d, keys %v, error %q; want 200 with exactly access_token and refresh_token", label, status, keys(m), m["error"])
	}
	return m["access_token"], m["refresh_token"]
}

func claimInt(t *testing.T, tok, name string) int64 {
	t.Helper()
	v, ok := jwtPart(t, tok, 1)[name].(float64)
	if !ok {
		t.Fatalf("token has no numeric %s claim", name)
	}
	return int64(v)
}

func TestIdP_RenewalOutlivesTheAccessTokenTTL(t *testing.T) {
	base := shortURL(t)
	u := signUp(t, superConn(t), base)
	h := newRenewal(t, base)
	v := idpVerifier(t, base)
	a0, r0 := h.session(t, u)

	if _, err := v.Verify(context.Background(), a0); err != nil {
		t.Fatalf("Verify the redeemed token: %v", err)
	}
	exp0 := claimInt(t, a0, "exp")
	if ttl := exp0 - claimInt(t, a0, "iat"); ttl > 10 {
		t.Fatalf("idp-short token lives %d s; want GOTRUE_JWT_EXP=5", ttl)
	}
	time.Sleep(time.Until(time.Unix(exp0+1, 0)))
	if now := time.Now().Unix(); now <= exp0 {
		t.Fatalf("control: now %d is not past exp %d", now, exp0)
	}
	if _, err := v.Verify(context.Background(), a0); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("Verify the expired token: err = %v, want ErrUnauthorized", err)
	}

	a1, _ := h.renewOK(t, "R0 after expiry", r0)
	id, err := v.Verify(context.Background(), a1)
	if err != nil {
		t.Fatalf("Verify the renewed token: %v", err)
	}
	if id.Subject != u.id {
		t.Errorf("renewed Subject = %q, want %q", id.Subject, u.id)
	}
	if exp1 := claimInt(t, a1, "exp"); exp1 <= exp0 {
		t.Errorf("renewed exp %d is not after the first exp %d", exp1, exp0)
	}
}

func TestIdP_ExchangeCarriesARenewableRefreshToken(t *testing.T) {
	base := idpURL(t)
	u := signUp(t, superConn(t), base)
	h := newRenewal(t, base)
	_, r0 := h.session(t, u)

	a1, _ := h.renewOK(t, "R0", r0)
	id, err := idpVerifier(t, base).Verify(context.Background(), a1)
	if err != nil {
		t.Fatalf("Verify the renewed token: %v", err)
	}
	if id.Subject != u.id {
		t.Errorf("renewed Subject = %q, want %q", id.Subject, u.id)
	}
}

// GoTrue answers the active token's parent with the active token; an older token revokes the family.
func TestIdP_RefreshRotationAndReuse(t *testing.T) {
	base := idpURL(t)
	u := signUp(t, superConn(t), base)
	h := newRenewal(t, base)
	_, r0 := h.session(t, u)

	_, r1 := h.renewOK(t, "R0", r0)
	if r1 == r0 {
		t.Fatal("refresh R0 answered R0 again; want a rotated token")
	}
	if _, again := h.renewOK(t, "R0 as R1's parent", r0); again != r1 {
		t.Error("refresh R0 while R1 is active: refresh_token is not R1")
	}
	_, r2 := h.renewOK(t, "R1", r1)
	if r2 == r1 || r2 == r0 {
		t.Fatal("refresh R1 answered an earlier token; want a rotated token")
	}
	for _, step := range []struct{ label, token string }{{"R0 two generations old", r0}, {"R2 after the family was revoked", r2}} {
		if status, m := h.renew(t, step.token); status != http.StatusUnauthorized {
			t.Errorf("refresh %s: status %d, keys %v; want 401", step.label, status, keys(m))
		}
	}
}

func TestIdP_RefreshedTokenKeepsTheTenant(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u := signUp(t, conn, base)
	tenant := seedTenant(t, conn)
	seedActiveMembership(t, conn, tenant, u.id)
	h := newRenewal(t, base)
	_, r0 := h.session(t, u)

	a1, _ := h.renewOK(t, "R0", r0)
	if am, _ := jwtPart(t, a1, 1)["app_metadata"].(map[string]any); am["tenant_id"] != tenant {
		t.Errorf("renewed app_metadata.tenant_id = %v, want %s", am["tenant_id"], tenant)
	}
	id, err := idpVerifier(t, base).Verify(context.Background(), a1)
	if err != nil {
		t.Fatalf("Verify the renewed token: %v", err)
	}
	if id.TenantID != tenant {
		t.Errorf("renewed Identity.TenantID = %q, want %s", id.TenantID, tenant)
	}
}
