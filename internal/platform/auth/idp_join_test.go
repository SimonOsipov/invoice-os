package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/tenancy"
)

const (
	joinListPath   = "/api/tenancy/v1/invitations/mine"
	joinAcceptTail = "/accept"
)

// joinEdge is the /api/ edge with a real SessionChecker against real GoTrue and a counting tenancy upstream.
type joinEdge struct {
	clk  *manualClock
	edge http.Handler
	hits *atomic.Int64
}

func newJoinEdge(t *testing.T, base string) joinEdge {
	t.Helper()
	authURL, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	clk := &manualClock{t: revocationT0}
	hits := &atomic.Int64{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	upURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	return joinEdge{clk: clk, hits: hits, edge: gateway.Handler(gateway.Options{
		Verifier:     idpVerifier(t, base),
		Sessions:     gateway.NewSessionChecker(authURL, idpHTTP, clk.now, log),
		Upstreams:    map[string]*url.URL{"tenancy": upURL},
		Logger:       log,
		GatewayToken: "gw-test-token",
	})}
}

func (e joinEdge) do(method, path, token string) int {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.edge.ServeHTTP(rec, req)
	return rec.Code
}

// requireJoinRoutes asserts the confirmed session reaches both join routes through the edge.
func (e joinEdge) requireJoinRoutes(t *testing.T, token string) {
	t.Helper()
	before := e.hits.Load()
	if got := e.do(http.MethodGet, joinListPath, token); got != http.StatusOK {
		t.Errorf("edge GET %s: status %d, want 200", joinListPath, got)
	}
	if got := e.do(http.MethodPost, "/api/tenancy/v1/invitations/"+uuid.NewString()+joinAcceptTail, token); got != http.StatusOK {
		t.Errorf("edge POST invitations/{id}/accept: status %d, want 200", got)
	}
	if got := e.hits.Load() - before; got != 2 {
		t.Errorf("upstream hits for the two join routes = %d, want 2", got)
	}
}

// joinByEmail lists the invitee's pending invites as the verified tenant-less identity and accepts the one by id.
func (w inviteWorld) joinByEmail(t *testing.T, caller auth.Identity) {
	t.Helper()
	ctx := auth.WithTenantlessCaller(context.Background(), caller)
	pending, err := w.store.MyPendingInvitations(ctx)
	if err != nil || len(pending) != 1 || pending[0].Workspace != "IdP Invite Co" || pending[0].Role != "reviewer" {
		t.Fatalf("MyPendingInvitations: %+v, err %v; want one {IdP Invite Co, reviewer}", pending, err)
	}
	tenant, subject, role, err := w.store.AcceptInvitationByID(ctx, pending[0].ID)
	if err != nil || tenant.ID != w.tenant || subject != caller.Subject || role != "reviewer" {
		t.Fatalf("AcceptInvitationByID: tenant %+v, subject %q, role %q, err %v; want %s, the caller, reviewer", tenant, subject, role, err, w.tenant)
	}
}

// signedIn signs u in and returns the access and refresh tokens; the grant must answer 200.
func signedIn(t *testing.T, base string, u idpUser) (string, string) {
	t.Helper()
	status, session := signIn(t, base, u)
	access, _ := session["access_token"].(string)
	refresh, _ := session["refresh_token"].(string)
	if status != http.StatusOK || access == "" || refresh == "" {
		t.Fatalf("password grant: status %d, body %v; want 200", status, session)
	}
	return access, refresh
}

// requireTenantAfterRefresh refreshes and asserts the new token carries the world's tenant and /v1/me answers 200.
func (w inviteWorld) requireTenantAfterRefresh(t *testing.T, refresh string) {
	t.Helper()
	status, session := postJSON(t, w.base+"/token?grant_type=refresh_token", map[string]string{"refresh_token": refresh})
	next, _ := session["access_token"].(string)
	if status != http.StatusOK || next == "" {
		t.Fatalf("refresh grant: status %d, body %v; want 200", status, session)
	}
	if am, _ := jwtPart(t, next, 1)["app_metadata"].(map[string]any); am["tenant_id"] != w.tenant {
		t.Fatalf("refreshed token app_metadata.tenant_id = %v, want %s", am["tenant_id"], w.tenant)
	}
	id, err := idpVerifier(t, w.base).Verify(context.Background(), next)
	if err != nil {
		t.Fatalf("Verify the refreshed token: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	tenancy.MeHandler(w.store.Me, nil).ServeHTTP(rec, req.WithContext(auth.WithIdentity(context.Background(), id)))
	if rec.Code != http.StatusOK {
		t.Fatalf("/v1/me: status %d, body %s; want 200", rec.Code, rec.Body)
	}
}

func TestIdP_ConfirmInAnotherTabThenJoinByEmail(t *testing.T) {
	w := newInviteWorld(t, "logfix03a-")
	u := idpUser{email: w.email, password: "pw-" + uuid.NewString()}
	w.register(t, u.password)
	link := confirmationLink(t, u.email)
	if got := follow(t, link); got != siteURL+"/?verified=1" {
		t.Fatalf("verify redirect = %q, want %s/?verified=1", got, siteURL)
	}

	access, refresh := signedIn(t, w.base, u)
	newJoinEdge(t, w.base).requireJoinRoutes(t, access)
	caller, err := idpVerifier(t, w.base).Verify(context.Background(), access)
	if err != nil || caller.TenantID != "" {
		t.Fatalf("Verify the first token: identity %+v, err %v; want a tenant-less identity", caller, err)
	}
	w.joinByEmail(t, caller)
	w.requireTenantAfterRefresh(t, refresh)
}

func TestIdP_ResetThenSignInJoinsByEmail(t *testing.T) {
	w := newInviteWorld(t, "logfix03b-")
	u := idpUser{email: w.email, password: "pw-" + uuid.NewString()}
	w.register(t, u.password)
	requestResetAccepted(t, w.gw, u.email)
	action, values := confirmForm(t, recoveryLink(t, u.email, 2))
	u.password = "pw-new-" + uuid.NewString()
	if got := submitReset(t, action, values, u.password); got != siteURL+"/?reset=1" {
		t.Fatalf("reset redirect = %q, want %s/?reset=1", got, siteURL)
	}

	access, _ := signedIn(t, w.base, u)
	newJoinEdge(t, w.base).requireJoinRoutes(t, access)
	caller, err := idpVerifier(t, w.base).Verify(context.Background(), access)
	if err != nil || caller.TenantID != "" {
		t.Fatalf("Verify the token: identity %+v, err %v; want a tenant-less identity", caller, err)
	}
	w.joinByEmail(t, caller)
}

func TestIdP_UnconfirmedInviteeGetsNoTokenToJoinWith(t *testing.T) {
	w := newInviteWorld(t, "logfix03c-")
	u := idpUser{email: w.email, password: "pw-" + uuid.NewString()}
	w.register(t, u.password)
	status, body := signIn(t, w.base, u)
	if status != http.StatusBadRequest || body["error_code"] != "email_not_confirmed" {
		t.Fatalf("password grant before any click: status %d, body %v; want 400 email_not_confirmed", status, body)
	}
}

// P8: user_metadata is user-writable, so email_verified there must never open a join route.
func TestIdP_WrittenMetadataOnAnUnconfirmedAddressReachesNoJoinRoute(t *testing.T) {
	base := idpURL(t)
	conn := superConn(t)
	u := signUp(t, conn, base)
	access, refresh := signedIn(t, base, u)
	e := newJoinEdge(t, base)
	acceptPath := "/api/tenancy/v1/invitations/" + uuid.NewString() + joinAcceptTail

	if got := e.do(http.MethodGet, joinListPath, access); got != http.StatusOK || e.hits.Load() != 1 {
		t.Fatalf("control: GET mine status %d with %d upstream hits, want 200 with 1", got, e.hits.Load())
	}

	exec(t, conn, `UPDATE auth.users SET email_confirmed_at = NULL WHERE id = $1`, u.id)
	raw, _ := json.Marshal(map[string]any{"data": map[string]any{"email_verified": true}})
	req, err := http.NewRequest(http.MethodPut, base+"/user", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	resp, err := idpHTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /user: status %d, body %s; want 200", resp.StatusCode, b)
	}

	status, session := postJSON(t, base+"/token?grant_type=refresh_token", map[string]string{"refresh_token": refresh})
	next, _ := session["access_token"].(string)
	if status != http.StatusOK || next == "" {
		t.Fatalf("refresh grant: status %d, body %v; want 200", status, session)
	}
	if um, _ := jwtPart(t, next, 1)["user_metadata"].(map[string]any); um["email_verified"] != true {
		t.Fatalf("refreshed token user_metadata.email_verified = %v, want true (the written value)", um["email_verified"])
	}

	e.clk.set(revocationT0.Add(gateway.SessionCheckTTL + time.Second))
	if got := e.do(http.MethodGet, joinListPath, next); got != http.StatusForbidden {
		t.Errorf("GET mine with written metadata: status %d, want 403", got)
	}
	if got := e.do(http.MethodPost, acceptPath, next); got != http.StatusForbidden {
		t.Errorf("POST accept with written metadata: status %d, want 403", got)
	}
	if got := e.hits.Load(); got != 1 {
		t.Errorf("upstream hits = %d, want 1 (the control only)", got)
	}
}

// A confirmed email opens only the two join routes: other methods and paths stay 403 and reach no upstream.
func TestIdP_ConfirmedSessionReachesNoRouteBesideTheTwoJoinRoutes(t *testing.T) {
	base := idpURL(t)
	u := signUp(t, superConn(t), base)
	access, _ := signedIn(t, base, u)
	e := newJoinEdge(t, base)
	id := uuid.NewString()

	if got := e.do(http.MethodGet, joinListPath, access); got != http.StatusOK || e.hits.Load() != 1 {
		t.Fatalf("control: GET mine status %d with %d upstream hits, want 200 with 1", got, e.hits.Load())
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, joinListPath},
		{http.MethodGet, "/api/tenancy/v1/invitations/" + id + joinAcceptTail},
		{http.MethodPost, "/api/tenancy/v1/invitations/not-a-uuid" + joinAcceptTail},
		{http.MethodGet, "/api/tenancy/v1/me"},
		{http.MethodGet, "/api/tenancy/v1/members"},
	} {
		if got := e.do(c.method, c.path, access); got != http.StatusForbidden {
			t.Errorf("%s %s: status %d, want 403", c.method, c.path, got)
		}
	}
	if got := e.hits.Load(); got != 1 {
		t.Errorf("upstream hits = %d, want 1 (the control only)", got)
	}
}
