package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/tenancy"
)

// Refusal copy of the gateway and tenancy; their handler tests pin the same strings.
const inviteNotValid = "this invite is no longer valid"

var inviteTokenRe = regexp.MustCompile(`https://www\.ascomply\.com/invite#token=([A-Za-z0-9_-]+)`)

// startInviteGateway serves startGateway's routes plus the invite preview, invitee registration, set-password page and token resend,
// with the store's real lookup, claim and release.
func startInviteGateway(t *testing.T, authBase string, store *tenancy.Store) string {
	t.Helper()
	authURL, err := url.Parse(authBase)
	if err != nil {
		t.Fatal(err)
	}
	site, _ := url.Parse(siteURL)
	mux, _ := gatewayMux(t, authBase, 0, nil)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	limit := gateway.NewSignInThrottle("register", gateway.RegisterPerIP, gateway.RegisterMaxKeys, gateway.RegisterWindow, time.Now)
	notValid := func(err error) error {
		if errors.Is(err, tenancy.ErrInvitationNotValid) {
			return gateway.ErrInvitationNotValid
		}
		return err
	}
	preview := func(ctx context.Context, token string) (gateway.InvitationPreview, error) {
		p, err := store.PreviewInvitation(ctx, token)
		return gateway.InvitationPreview{Workspace: p.Workspace, Role: p.Role, Email: p.Email, Account: p.Account}, notValid(err)
	}
	registrations := gateway.InvitationRegistrations{
		Claim: func(ctx context.Context, token string) (string, bool, error) {
			email, first, err := store.ClaimInvitationRegistration(ctx, token)
			return email, first, notValid(err)
		},
		Release: store.ReleaseInvitationRegistration,
	}
	signInLimit := gateway.NewSignInThrottle("sign-in", gateway.SignInMaxFailures, gateway.SignInMaxKeys, gateway.SignInWindow, time.Now)
	mux.Handle("POST /auth/invitation", gateway.InvitationHandler(preview, log))
	mux.Handle("POST /auth/invitation/register", gateway.InvitationRegisterHandler(authURL, noRedirect, 0, limit, true, log, preview, registrations))
	perAddress := gateway.NewSignInThrottle("resend-address", gateway.ResendPerAddress, gateway.ResendMaxKeys, gateway.ResendWindow, time.Now)
	perIP := gateway.NewSignInThrottle("resend-ip", gateway.ResendPerIP, gateway.ResendMaxKeys, gateway.ResendWindow, time.Now)
	mux.Handle("POST /auth/invitation/resend", gateway.InvitationResendHandler(authURL, noRedirect, perAddress, perIP, true, log, preview))
	mux.Handle("POST /auth/invitation/password", gateway.InvitationPasswordHandler(authURL, site, noRedirect, gateway.NewSessionChecker(authURL, noRedirect, time.Now, log), signInLimit, nil, log, gateway.NewHandoffStore(gateway.HandoffTTL, time.Now)))
	return serveGateway(t, mux)
}

type resendMessage struct {
	To   []string `json:"to"`
	HTML string   `json:"html"`
}

// resendStandIn records each POST /emails/batch body.
type resendStandIn struct {
	mu      sync.Mutex
	batches [][]resendMessage
	URL     string
}

func newResendStandIn(t *testing.T) *resendStandIn {
	t.Helper()
	r := &resendStandIn{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var batch []resendMessage
		if req.Method != http.MethodPost || req.URL.Path != "/emails/batch" || json.NewDecoder(req.Body).Decode(&batch) != nil {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		r.batches = append(r.batches, batch)
		r.mu.Unlock()
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	t.Cleanup(srv.Close)
	r.URL = srv.URL
	return r
}

type inviteWorld struct {
	gw, base, email, token, tenant string
	store                          *tenancy.Store
	resend                         *resendStandIn
	adminCtx                       context.Context
}

// newInviteWorld is newInviteWorldAt on gmail.com.
func newInviteWorld(t *testing.T, prefix string) inviteWorld {
	t.Helper()
	return newInviteWorldAt(t, prefix, "gmail.com")
}

// newInviteWorldAt seeds a fresh address on domain; see newInviteWorldFor.
func newInviteWorldAt(t *testing.T, prefix, domain string) inviteWorld {
	t.Helper()
	return newInviteWorldFor(t, prefix+uuid.NewString()+"@"+domain, nil)
}

// newInviteWorldFor seeds "IdP Invite Co" with an active admin, starts the gateway, runs before (when set) while email
// is not yet invited, then has the admin invite email as reviewer through the real Inviter and reads the token
// from the mail the Resend stand-in recorded.
func newInviteWorldFor(t *testing.T, email string, before func(w inviteWorld)) inviteWorld {
	t.Helper()
	ctx := context.Background()
	base := idpMailURL(t)
	grantAccountStateRead(t)
	conn := superConn(t)

	pool, err := db.NewPool(ctx, mailEnv(t, "DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := tenancy.NewStore(pool)

	w := inviteWorld{base: base, store: store, resend: newResendStandIn(t), tenant: uuid.NewString(), email: email}
	admin := uuid.NewString()
	exec(t, conn, `INSERT INTO tenants (id, name) VALUES ($1, 'IdP Invite Co')`, w.tenant)
	exec(t, conn, `INSERT INTO memberships (tenant_id, user_id, role, status, display_name, email) VALUES ($1, $2, 'admin', 'active', 'Ada Admin', $3)`,
		w.tenant, admin, admin+"@members.test")
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, w.tenant)
		_, _ = conn.Exec(context.Background(), `DELETE FROM auth.users WHERE email = $1`, w.email)
	})
	w.gw = startInviteGateway(t, base, store)
	if before != nil {
		before(w)
	}

	inviter := &tenancy.Inviter{Store: store, Sender: accountmail.NewResend(w.resend.URL, "k_test", nil), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	adminCtx := auth.WithIdentity(ctx, auth.Identity{Subject: admin, Role: "authenticated", TenantID: w.tenant})
	w.adminCtx = adminCtx
	if res, err := inviter.Invite(adminCtx, []string{w.email}, "reviewer"); err != nil || len(res) != 1 || res[0].Delivery != "sent" {
		t.Fatalf("invite %s: %+v, err %v; want one sent", w.email, res, err)
	}

	w.resend.mu.Lock()
	defer w.resend.mu.Unlock()
	if len(w.resend.batches) != 1 || len(w.resend.batches[0]) != 1 {
		t.Fatalf("Resend stand-in recorded %d batches, want one POST /emails/batch with one message: %+v", len(w.resend.batches), w.resend.batches)
	}
	msg := w.resend.batches[0][0]
	if len(msg.To) != 1 || msg.To[0] != w.email {
		t.Fatalf("invite mail went to %v, want [%s]", msg.To, w.email)
	}
	m := inviteTokenRe.FindStringSubmatch(msg.HTML)
	if m == nil || len(m[1]) != 43 {
		t.Fatalf("invite mail carries no 43-character token in its accept URL: %v", m)
	}
	w.token = m[1]
	return w
}

// newPreRegisteredInviteWorld registers a fresh address through /auth/register with password (and confirms it when confirm
// is set) before the admin invites it: an invite-created account has no password anybody holds.
func newPreRegisteredInviteWorld(t *testing.T, prefix, password string, confirm bool) inviteWorld {
	t.Helper()
	return newInviteWorldFor(t, prefix+uuid.NewString()+"@example.com", func(w inviteWorld) {
		if status, body := postGW(t, w.gw+"/auth/register", map[string]string{"email": w.email, "password": password}); status != http.StatusAccepted {
			t.Fatalf("/auth/register before the invite: status %d, body %s; want 202", status, body)
		}
		if confirm {
			confirmByLink(t, idpUser{email: w.email, password: password})
		}
	})
}

// postGW posts JSON to the gateway and returns the status and body.
func postGW(t *testing.T, url string, body any) (int, string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := noRedirect.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// register posts the invite token alone, as the landing page does.
func (w inviteWorld) register(t *testing.T) {
	t.Helper()
	if status, body := postGW(t, w.gw+"/auth/invitation/register", map[string]string{"token": w.token}); status != http.StatusAccepted {
		t.Fatalf("invitee registration: status %d, body %s; want 202", status, body)
	}
}

// setPasswordFromMail opens the one confirmation mail, submits its set-password form with password and requires 303 ?verified=1.
// It returns the form's action and values for a replay.
func (w inviteWorld) setPasswordFromMail(t *testing.T, password string) (string, url.Values) {
	t.Helper()
	action, values := confirmForm(t, confirmationLink(t, w.email))
	if u, err := url.Parse(action); err != nil || u.Path != "/auth/invitation/password" {
		t.Fatalf("the invitee's confirmation form posts to %s, want /auth/invitation/password", action)
	}
	values.Set("password", password)
	status, location, err := postForm(action, values)
	if err != nil {
		t.Fatalf("POST %s: %v", action, err)
	}
	if status != http.StatusSeeOther || location != siteURL+"/?verified=1" {
		t.Fatalf("submitting the set-password form: status %d, Location %q; want 303 %s/?verified=1", status, location, siteURL)
	}
	return action, values
}

// accept runs the accept handler as the tenant-less caller the middleware would build.
func (w inviteWorld) accept(caller auth.Identity) (int, string) {
	req := httptest.NewRequest(http.MethodPost, "/v1/invitations/accept", strings.NewReader(`{"token":"`+w.token+`"}`))
	rec := httptest.NewRecorder()
	tenancy.AcceptInvitationHandler(w.store.AcceptInvitation, nil).ServeHTTP(rec, req.WithContext(auth.WithTenantlessCaller(req.Context(), caller)))
	return rec.Code, rec.Body.String()
}

func (w inviteWorld) previewStatus(t *testing.T) (int, string) {
	t.Helper()
	return postGW(t, w.gw+"/auth/invitation", map[string]string{"token": w.token})
}

func TestIdP_InviteeRegistersVerifiesSignsInAndJoins(t *testing.T) {
	w := newInviteWorld(t, "resend06-")
	u := idpUser{email: w.email, password: "pw-" + uuid.NewString()}
	ctx := context.Background()

	status, body := w.previewStatus(t)
	var pv struct{ Workspace, Role, Email string }
	if err := json.Unmarshal([]byte(body), &pv); status != http.StatusOK || err != nil ||
		pv.Workspace != "IdP Invite Co" || pv.Role != "reviewer" || pv.Email != w.email {
		t.Fatalf("preview: status %d, body %s; want 200 {IdP Invite Co, reviewer, %s}", status, body, w.email)
	}

	w.register(t)
	requireSignIn(t, w.base, u, u.password, http.StatusBadRequest, "")
	w.setPasswordFromMail(t, u.password)

	status, session := signIn(t, w.base, u)
	first, _ := session["access_token"].(string)
	refresh, _ := session["refresh_token"].(string)
	if status != http.StatusOK || first == "" || refresh == "" {
		t.Fatalf("password grant after the click: status %d, body %v; want 200", status, session)
	}
	if am, _ := jwtPart(t, first, 1)["app_metadata"].(map[string]any); am["tenant_id"] != nil {
		t.Fatalf("first token app_metadata.tenant_id = %v, want absent", am["tenant_id"])
	}
	v := idpVerifier(t, w.base)
	caller, err := v.Verify(ctx, first)
	if err != nil || caller.TenantID != "" {
		t.Fatalf("Verify the first token: identity %+v, err %v; want a tenant-less identity", caller, err)
	}

	if code, body := w.accept(caller); code != http.StatusOK {
		t.Fatalf("accept: status %d, body %s; want 200", code, body)
	}

	status, session = postJSON(t, w.base+"/token?grant_type=refresh_token", map[string]string{"refresh_token": refresh})
	next, _ := session["access_token"].(string)
	if status != http.StatusOK || next == "" {
		t.Fatalf("refresh grant: status %d, body %v; want 200", status, session)
	}
	if am, _ := jwtPart(t, next, 1)["app_metadata"].(map[string]any); am["tenant_id"] != w.tenant {
		t.Fatalf("refreshed token app_metadata.tenant_id = %v, want %s", am["tenant_id"], w.tenant)
	}
	id, err := v.Verify(ctx, next)
	if err != nil {
		t.Fatalf("Verify the refreshed token: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	tenancy.MeHandler(w.store.Me, nil).ServeHTTP(rec, req.WithContext(auth.WithIdentity(ctx, id)))
	var me struct {
		Tenant struct{ ID, Name string }
		User   struct{ Role string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); rec.Code != http.StatusOK || err != nil ||
		me.Tenant.ID != w.tenant || me.Tenant.Name != "IdP Invite Co" || me.User.Role != "reviewer" {
		t.Fatalf("/v1/me: status %d, body %s; want 200 with %s, IdP Invite Co, reviewer", rec.Code, rec.Body, w.tenant)
	}

	conn := superConn(t)
	var inviteStatus string
	var accepted int
	if err := conn.QueryRow(ctx, `SELECT status FROM invitations WHERE tenant_id = $1 AND invitee_email = $2`, w.tenant, w.email).Scan(&inviteStatus); err != nil {
		t.Fatalf("read the invitation: %v", err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'invitation.accepted'`, w.tenant).Scan(&accepted); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if inviteStatus != "accepted" || accepted != 1 {
		t.Errorf("invitation status %q with %d invitation.accepted rows, want accepted with 1", inviteStatus, accepted)
	}
	if status, body := w.previewStatus(t); status != http.StatusNotFound || !strings.Contains(body, inviteNotValid) {
		t.Errorf("preview after accept: status %d, body %s; want 404 %q", status, body, inviteNotValid)
	}
	// A spent token is "no longer valid", not "already a member": the pending filter runs first.
	if code, body := w.accept(id); code != http.StatusNotFound || !strings.Contains(body, inviteNotValid) {
		t.Errorf("second accept: status %d, body %s; want 404 %q", code, body, inviteNotValid)
	}
}
