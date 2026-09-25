package auth_test

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/tenancy"
)

const (
	// idp-up.sh points idp-mail's confirmation link at this address.
	gatewayAddr = "127.0.0.1:9995"
	linkPrefix  = "http://localhost:9995/auth/verify?"
	siteURL     = "http://localhost:3000"
)

var noRedirect = &http.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// idpMailURL returns idp-mail's base URL; the CI idp job's rls-test-gate.sh turns this skip into a failure.
func idpMailURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("IDP_MAIL_URL")
	if u == "" {
		t.Skip("IDP_MAIL_URL unset; run `make test-idp` or the CI idp job")
	}
	return strings.TrimRight(u, "/")
}

func mailEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("IDP_MAIL_URL is set but %s is not", name)
	}
	return strings.TrimRight(v, "/")
}

// startGateway serves the real register and verify handlers where the mailed link points.
func startGateway(t *testing.T, authBase string) string {
	t.Helper()
	authURL, err := url.Parse(authBase)
	if err != nil {
		t.Fatal(err)
	}
	site, _ := url.Parse(siteURL)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	mux.Handle("POST /auth/register", gateway.RegisterHandler(authURL, noRedirect, log))
	mux.Handle("GET /auth/verify", gateway.VerifyHandler(authURL, site, noRedirect, log))

	l, err := net.Listen("tcp", gatewayAddr)
	if err != nil {
		t.Fatalf("listen on %s (the mailed link's host): %v", gatewayAddr, err)
	}
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL
}

// registrant signs up through the gateway handler; the superuser deletes the GoTrue user and any workspace afterwards.
func registrant(t *testing.T, gw string) idpUser {
	t.Helper()
	conn := superConn(t)
	u := idpUser{email: "idp-mail-" + uuid.NewString() + "@example.test", password: "pw-" + uuid.NewString()}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = conn.Exec(ctx, `DELETE FROM tenants WHERE id IN (
			SELECT m.tenant_id FROM memberships m JOIN auth.users u ON u.id = m.user_id WHERE u.email = $1)`, u.email)
		_, _ = conn.Exec(ctx, `DELETE FROM auth.users WHERE email = $1`, u.email)
	})

	body := strings.NewReader(`{"email":"` + u.email + `","password":"` + u.password + `"}`)
	resp, err := noRedirect.Post(gw+"/auth/register", "application/json", body)
	if err != nil {
		t.Fatalf("POST /auth/register: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /auth/register: status %d, body %s; want 202", resp.StatusCode, b)
	}
	return u
}

// The gateway's refusal copy; TestRegister_FreeMailRefused400NoUpstreamCall pins the same string.
const freeMailRefusal = "a business email address is required; personal email providers are not accepted"

// postRegister posts email through the gateway handler and returns the status and body.
func postRegister(t *testing.T, gw, email string) (int, string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"email": email, "password": "pw-" + uuid.NewString()})
	resp, err := noRedirect.Post(gw+"/auth/register", "application/json", strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("POST /auth/register: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// None may register. The gateway refuses the trailing-dot row; real GoTrue decides the rest.
func TestIdP_FreeMailVariantsAreNotAccepted(t *testing.T) {
	gw := startGateway(t, idpMailURL(t))
	conn := superConn(t)
	ctx := context.Background()

	for _, c := range []struct{ name, domain string }{
		{"fullwidth", "ｇｍａｉｌ.com"},
		{"ideographic_full_stop", "gmail。com"},
		{"trailing_dots", "gmail.com.."},
		{"leading_space_in_domain", " gmail.com"},
		{"trailing_zero_width_space", "gmail.com​"},
	} {
		t.Run(c.name, func(t *testing.T) {
			local := "idp-fm-" + uuid.NewString()
			posted, ascii := local+"@"+c.domain, local+"@gmail.com"
			t.Cleanup(func() {
				_, _ = conn.Exec(ctx, `DELETE FROM auth.users WHERE lower(email) IN (lower($1), $2)`, posted, ascii)
			})

			status, body := postRegister(t, gw, posted)
			t.Logf("%q -> %d %s", posted, status, body)

			// 400, not merely non-202: a 502 from an unreachable GoTrue is not a refusal.
			if status != http.StatusBadRequest {
				t.Errorf("status = %d for %q, want 400", status, posted)
			}
			var n int
			if err := conn.QueryRow(ctx, `SELECT count(*) FROM auth.users WHERE lower(email) IN (lower($1), $2)`,
				posted, ascii).Scan(&n); err != nil {
				t.Fatalf("count auth.users: %v", err)
			}
			if n != 0 {
				t.Errorf("auth.users holds %d rows for %q or %q, want 0", n, posted, ascii)
			}
		})
	}

	t.Run("control_upper_case", func(t *testing.T) {
		posted := "idp-fm-" + uuid.NewString() + "@GMAIL.COM"
		t.Cleanup(func() {
			_, _ = conn.Exec(ctx, `DELETE FROM auth.users WHERE lower(email) = lower($1)`, posted)
		})

		status, body := postRegister(t, gw, posted)

		if status != http.StatusBadRequest {
			t.Fatalf("status = %d for %q, want 400: %s", status, posted, body)
		}
		var e struct{ Error string }
		if err := json.Unmarshal([]byte(body), &e); err != nil || e.Error != freeMailRefusal {
			t.Errorf("body = %s, want error %q", body, freeMailRefusal)
		}
	})
}

type mailpitSearch struct {
	Messages []struct {
		ID string `json:"ID"`
		To []struct {
			Address string `json:"Address"`
		} `json:"To"`
	} `json:"messages"`
}

var hrefRe = regexp.MustCompile(`href="([^"]+)"`)

// confirmationLink waits for the address's mail and returns the one link in it. It fails unless exactly one mail arrived.
func confirmationLink(t *testing.T, email string) string {
	t.Helper()
	mailpit := mailEnv(t, "MAILPIT_URL")
	// Mailpit's search matches loosely, so the exact recipient is checked here.
	var ids []string
	for deadline := time.Now().Add(10 * time.Second); len(ids) == 0 && time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		var found mailpitSearch
		getJSON(t, mailpit+"/api/v1/search?query="+url.QueryEscape(`to:"`+email+`"`), &found)
		for _, m := range found.Messages {
			for _, to := range m.To {
				if to.Address == email {
					ids = append(ids, m.ID)
				}
			}
		}
	}
	if len(ids) != 1 {
		t.Fatalf("mailpit holds %d mails for %s, want exactly 1", len(ids), email)
	}

	var msg struct {
		HTML string `json:"HTML"`
	}
	getJSON(t, mailpit+"/api/v1/message/"+ids[0], &msg)
	links := hrefRe.FindAllStringSubmatch(msg.HTML, -1)
	if len(links) != 1 {
		t.Fatalf("mail carries %d links, want 1: %s", len(links), msg.HTML)
	}
	return html.UnescapeString(links[0][1])
}

func getJSON(t *testing.T, u string, out any) {
	t.Helper()
	resp, err := idpHTTP.Get(u)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", u, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
}

// follow opens the mailed link as a mail client would and returns the redirect target.
func follow(t *testing.T, link string) string {
	t.Helper()
	resp, err := noRedirect.Get(link)
	if err != nil {
		t.Fatalf("GET %s: %v", link, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET the mailed link: status %d, want 303", resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

func TestIdP_RegisterLeavesTheAccountUnverified(t *testing.T) {
	base := idpMailURL(t)
	u := registrant(t, startGateway(t, base))

	var confirmed bool
	if err := superConn(t).QueryRow(context.Background(),
		`SELECT email_confirmed_at IS NOT NULL FROM auth.users WHERE email = $1`, u.email).Scan(&confirmed); err != nil {
		t.Fatalf("read the registered user: %v", err)
	}
	if confirmed {
		t.Error("the registered user is already confirmed; idp-mail autoconfirms")
	}
	status, body := signIn(t, base, u)
	if status != http.StatusBadRequest || body["error_code"] != "email_not_confirmed" {
		t.Errorf("password grant before verification: status %d, body %v; want 400 email_not_confirmed", status, body)
	}
}

func TestIdP_ConfirmationLinkTargetsTheGateway(t *testing.T) {
	base := idpMailURL(t)
	u := registrant(t, startGateway(t, base))

	link := confirmationLink(t, u.email)
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse the mailed link %q: %v", link, err)
	}
	if !strings.HasPrefix(link, linkPrefix) {
		t.Errorf("mailed link = %q, want the configured absolute %s…", link, linkPrefix)
	}
	if q := parsed.Query(); q.Get("type") != "signup" || q.Get("token") == "" {
		t.Errorf("mailed link query = %v, want type=signup and a token", q)
	}
}

func TestIdP_EmailedLinkVerifiesThenSignInSucceeds(t *testing.T) {
	base := idpMailURL(t)
	u := registrant(t, startGateway(t, base))

	if got := follow(t, confirmationLink(t, u.email)); got != siteURL+"/?verified=1" {
		t.Fatalf("verify redirect = %q, want %s/?verified=1", got, siteURL)
	}
	accessToken(t, base, u)
}

func TestIdP_VerificationLinkIsSingleUse(t *testing.T) {
	base := idpMailURL(t)
	u := registrant(t, startGateway(t, base))

	link := confirmationLink(t, u.email)
	if got := follow(t, link); got != siteURL+"/?verified=1" {
		t.Fatalf("first follow = %q, want %s/?verified=1", got, siteURL)
	}
	if got := follow(t, link); got != siteURL+"/?verify=failed" {
		t.Errorf("second follow = %q, want %s/?verify=failed", got, siteURL)
	}
}

func TestIdP_ProvisionedWorkspaceReachesTheNextToken(t *testing.T) {
	base := idpMailURL(t)
	u := registrant(t, startGateway(t, base))
	if got := follow(t, confirmationLink(t, u.email)); got != siteURL+"/?verified=1" {
		t.Fatalf("verify redirect = %q, want %s/?verified=1", got, siteURL)
	}

	ctx := context.Background()
	v := idpVerifier(t, base)
	status, session := signIn(t, base, u)
	first, _ := session["access_token"].(string)
	refresh, _ := session["refresh_token"].(string)
	if status != http.StatusOK || first == "" || refresh == "" {
		t.Fatalf("sign in after verification: status %d, body %v", status, session)
	}
	if am, _ := jwtPart(t, first, 1)["app_metadata"].(map[string]any); am["tenant_id"] != nil {
		t.Fatalf("first token app_metadata.tenant_id = %v, want absent", am["tenant_id"])
	}
	caller, err := v.Verify(ctx, first)
	if err != nil || caller.TenantID != "" {
		t.Fatalf("Verify the first token: identity %+v, err %v; want a tenant-less identity", caller, err)
	}

	pool, err := db.NewPool(ctx, mailEnv(t, "DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := tenancy.NewStore(pool)

	// The caller goes on the context the way identityMiddleware places it.
	req := httptest.NewRequest(http.MethodPost, "/v1/workspaces",
		strings.NewReader(`{"workspace_name":"IdP Works","display_name":"Ada","kind":"in_house"}`))
	rec := httptest.NewRecorder()
	tenancy.ProvisionHandler(store.ProvisionWorkspace, nil).ServeHTTP(rec, req.WithContext(auth.WithTenantlessCaller(ctx, caller)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("provision: status %d, body %s; want 201", rec.Code, rec.Body)
	}
	var created struct {
		Tenant struct{ ID string } `json:"tenant"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.Tenant.ID == "" {
		t.Fatalf("provision body %s: tenant id missing (%v)", rec.Body, err)
	}

	status, session = postJSON(t, base+"/token?grant_type=refresh_token", map[string]string{"refresh_token": refresh})
	next, _ := session["access_token"].(string)
	if status != http.StatusOK || next == "" {
		t.Fatalf("refresh grant: status %d, body %v", status, session)
	}
	if am, _ := jwtPart(t, next, 1)["app_metadata"].(map[string]any); am["tenant_id"] != created.Tenant.ID {
		t.Errorf("refreshed token app_metadata.tenant_id = %v, want %s", am["tenant_id"], created.Tenant.ID)
	}
	id, err := v.Verify(ctx, next)
	if err != nil {
		t.Fatalf("Verify the refreshed token: %v", err)
	}
	if id.TenantID != created.Tenant.ID {
		t.Fatalf("Identity.TenantID = %q, want %s", id.TenantID, created.Tenant.ID)
	}

	tenant, role, err := store.Me(auth.WithIdentity(ctx, id))
	if err != nil {
		t.Fatalf("Store.Me with the refreshed identity: %v", err)
	}
	if tenant.ID != created.Tenant.ID || tenant.Name != "IdP Works" || tenant.Kind != "in_house" || role != "admin" {
		t.Errorf("Store.Me = %+v role %q, want the new workspace IdP Works (in_house), role admin", tenant, role)
	}
}
