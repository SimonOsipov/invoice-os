package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	xhtml "golang.org/x/net/html"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/tenancy"
)

// idp-up.sh points idp-mail's confirmation link at this port, shifted by 10 per IDP_SLOT.
var verifyPort = func() int {
	slot, _ := strconv.Atoi(os.Getenv("IDP_SLOT"))
	return 9995 + 10*slot
}()

var (
	gatewayAddr = fmt.Sprintf("127.0.0.1:%d", verifyPort)
	linkPrefix  = fmt.Sprintf("http://localhost:%d/auth/verify?", verifyPort)
)

const siteURL = "http://localhost:3000"

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

// startGateway serves the real register, resend, reset-request, confirm and reset pages, and verify handlers where the mailed links point, and returns its JSON log.
func startGateway(t *testing.T, authBase string, minResponse time.Duration, sink gateway.ContactSink) (string, *bytes.Buffer) {
	t.Helper()
	mux, logs := gatewayMux(t, authBase, minResponse, sink)
	return serveGateway(t, mux), logs
}

// gatewayMux builds startGateway's routes and the JSON log they write.
func gatewayMux(t *testing.T, authBase string, minResponse time.Duration, sink gateway.ContactSink) (*http.ServeMux, *bytes.Buffer) {
	t.Helper()
	authURL, err := url.Parse(authBase)
	if err != nil {
		t.Fatal(err)
	}
	site, _ := url.Parse(siteURL)
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(&syncWriter{w: logs}, nil))
	mux := http.NewServeMux()
	registerLimit := gateway.NewSignInThrottle("register", gateway.RegisterPerIP, gateway.RegisterMaxKeys, gateway.RegisterWindow, time.Now)
	pool, err := db.NewPool(context.Background(), mailEnv(t, "DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	mux.Handle("POST /auth/register", gateway.RegisterHandler(authURL, noRedirect, minResponse, registerLimit, true, log, tenancy.NewStore(pool).InvitationPendingForEmail))
	perAddress := gateway.NewSignInThrottle("resend-address", gateway.ResendPerAddress, gateway.ResendMaxKeys, gateway.ResendWindow, time.Now)
	perIP := gateway.NewSignInThrottle("resend-ip", gateway.ResendPerIP, gateway.ResendMaxKeys, gateway.ResendWindow, time.Now)
	mux.Handle("POST /auth/resend-verification", gateway.ResendVerificationHandler(authURL, noRedirect, minResponse, perAddress, perIP, true, log))
	mux.Handle("POST /auth/request-password-reset", gateway.RequestPasswordResetHandler(authURL, noRedirect, minResponse, perAddress, perIP, true, log))
	verifyPage, err := gateway.VerifyPageHandler(site)
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("GET /auth/verify", verifyPage)
	store := gateway.NewHandoffStore(gateway.HandoffTTL, time.Now)
	mux.Handle("POST /auth/verify", gateway.VerifyHandler(authURL, site, noRedirect, log, sink, store))
	mux.Handle("POST /auth/exchange", gateway.ExchangeHandler(store))
	inviteeSignIn := gateway.NewSignInThrottle("sign-in", gateway.SignInMaxFailures, gateway.SignInMaxKeys, gateway.SignInWindow, time.Now)
	mux.Handle("POST /auth/invitation/password", gateway.InvitationPasswordHandler(authURL, site, noRedirect, gateway.NewSessionChecker(authURL, noRedirect, time.Now, log), inviteeSignIn, sink, log, store))
	confirmationMail, err := gateway.MailTemplate("confirmation")
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("GET /emails/confirmation.html", confirmationMail)
	mux.Handle("GET /auth/reset-password", gateway.ResetPasswordPageHandler(site))
	resetSignIn := gateway.NewSignInThrottle("sign-in", gateway.SignInMaxFailures, gateway.SignInMaxKeys, gateway.SignInWindow, time.Now)
	mux.Handle("POST /auth/reset-password", gateway.ResetPasswordHandler(authURL, site, noRedirect, gateway.NewSessionChecker(authURL, noRedirect, time.Now, log), resetSignIn, log))
	recoveryMail, err := gateway.MailTemplate("recovery")
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("GET /emails/recovery.html", recoveryMail)
	mux.Handle("GET /emails/mark.png", gateway.MailLogo())
	return mux, logs
}

// serveGateway serves mux on the address the mailed links point at.
func serveGateway(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
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

// syncWriter serialises the handler goroutines' log writes into one buffer.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// registrant signs up through the gateway handler; the superuser deletes the GoTrue user and any workspace afterwards.
// answers, when given, ride in the register body as the landing form sends them.
func registrant(t *testing.T, gw string, answers ...map[string]string) idpUser {
	t.Helper()
	conn := superConn(t)
	u := idpUser{email: "idp-mail-" + uuid.NewString() + "@example.test", password: "pw-" + uuid.NewString()}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = conn.Exec(ctx, `DELETE FROM tenants WHERE id IN (
			SELECT m.tenant_id FROM memberships m JOIN auth.users u ON u.id = m.user_id WHERE u.email = $1)`, u.email)
		_, _ = conn.Exec(ctx, `DELETE FROM auth.users WHERE email = $1`, u.email)
	})

	fields := map[string]string{"email": u.email, "password": u.password}
	for _, a := range answers {
		maps.Copy(fields, a)
	}
	payload, _ := json.Marshal(fields)
	resp, err := noRedirect.Post(gw+"/auth/register", "application/json", bytes.NewReader(payload))
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
	gw, _ := startGateway(t, idpMailURL(t), 0, nil)
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

var anchorRe = regexp.MustCompile(`(?s)<a\b[^>]*?href="([^"]*)"[^>]*>(.*?)</a>`)

type mailpitMessage struct {
	Subject string `json:"Subject"`
	HTML    string `json:"HTML"`
}

// mailFor waits for the address's mail and returns it. It fails unless exactly one mail arrived.
func mailFor(t *testing.T, email string) mailpitMessage { return mailsFor(t, email, 1)[0] }

// mailsFor polls up to 10 s for want mails to exactly this address and fails on more or on a timeout.
func mailsFor(t *testing.T, email string, want int) []mailpitMessage {
	t.Helper()
	mailpit := mailEnv(t, "MAILPIT_URL")
	// Mailpit's search matches loosely, so the exact recipient is checked here.
	var ids []string
	for deadline := time.Now().Add(10 * time.Second); len(ids) < want && time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		ids = ids[:0]
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
	if len(ids) != want {
		t.Fatalf("mailpit holds %d mails for %s, want exactly %d", len(ids), email, want)
	}
	msgs := make([]mailpitMessage, len(ids))
	for i, id := range ids {
		getJSON(t, mailpit+"/api/v1/message/"+id, &msgs[i])
	}
	return msgs
}

// actionLink returns the action link: the one URL an anchor shows as its own text (the fallback),
// which a second anchor (the button) must also carry. Other anchors are ignored.
func actionLink(body string) (string, error) {
	var urls []string
	hrefs := map[string]int{}
	for _, m := range anchorRe.FindAllStringSubmatch(body, -1) {
		href := html.UnescapeString(m[1])
		hrefs[href]++
		if html.UnescapeString(strings.TrimSpace(m[2])) == href && !slices.Contains(urls, href) {
			urls = append(urls, href)
		}
	}
	if len(urls) != 1 {
		return "", fmt.Errorf("mail has %d fallback anchors, want exactly 1: %s", len(urls), body)
	}
	if hrefs[urls[0]] < 2 {
		return "", fmt.Errorf("no button anchor shares the fallback href %s: %s", urls[0], body)
	}
	return urls[0], nil
}

// confirmationLink waits for the address's mail and returns its action link. It fails unless exactly one mail arrived.
func confirmationLink(t *testing.T, email string) string { return confirmationLinks(t, email, 1)[0] }

// confirmationLinks returns the action link of each of the address's want mails.
func confirmationLinks(t *testing.T, email string, want int) []string {
	t.Helper()
	var links []string
	for _, msg := range mailsFor(t, email, want) {
		link, err := actionLink(msg.HTML)
		if err != nil {
			t.Fatal(err)
		}
		links = append(links, link)
	}
	return links
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

// open fetches the mailed link once, as a mail scanner would (GET or HEAD), requires 200 and returns the body.
func open(t *testing.T, link, method string) string {
	t.Helper()
	req, err := http.NewRequest(method, link, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, link, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s the mailed link: status %d, want 200: %s", method, resp.StatusCode, b)
	}
	return string(b)
}

// withState appends state to the mailed link, as the app does when it forwards the click.
func withState(link, state string) string { return link + "&state=" + state }

// openStateless opens the link as a mail scanner does (no state) and requires the 303 bounce to the landing page.
func openStateless(t *testing.T, link, method string) {
	t.Helper()
	req, err := http.NewRequest(method, link, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, link, err)
	}
	defer resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, siteURL+"/?confirm=1#token=") {
		t.Fatalf("%s the mailed link without a state: status %d, Location %q; want 303 to %s/?confirm=1#token=", method, resp.StatusCode, loc, siteURL)
	}
}

// confirmForm opens the link's page with a fresh state and returns its one form's action, resolved against the link, and its input values.
func confirmForm(t *testing.T, link string) (string, url.Values) {
	t.Helper()
	return confirmFormWithState(t, link, newState(t))
}

func confirmFormWithState(t *testing.T, link, state string) (string, url.Values) {
	t.Helper()
	doc, err := xhtml.Parse(strings.NewReader(open(t, withState(link, state), http.MethodGet)))
	if err != nil {
		t.Fatalf("parse the confirm page: %v", err)
	}
	attr := func(n *xhtml.Node, key string) string {
		for _, a := range n.Attr {
			if a.Key == key {
				return a.Val
			}
		}
		return ""
	}
	var forms []*xhtml.Node
	var find func(*xhtml.Node)
	find = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode && n.Data == "form" {
			forms = append(forms, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(doc)
	if len(forms) != 1 {
		t.Fatalf("the confirm page holds %d forms, want exactly 1", len(forms))
	}
	if m := attr(forms[0], "method"); !strings.EqualFold(m, "post") {
		t.Fatalf("confirm form method = %q, want post", m)
	}
	values := url.Values{}
	var collect func(*xhtml.Node)
	collect = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode && n.Data == "input" && attr(n, "name") != "" {
			values.Add(attr(n, "name"), attr(n, "value"))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			collect(c)
		}
	}
	collect(forms[0])
	base, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	action, err := url.Parse(attr(forms[0], "action"))
	if err != nil {
		t.Fatal(err)
	}
	return base.ResolveReference(action).String(), values
}

// postForm posts values as the confirm button does and returns the status and Location.
func postForm(action string, values url.Values) (int, string, error) {
	resp, err := noRedirect.PostForm(action, values)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Location"), nil
}

// follow opens the mailed link's page and clicks its button without the state, and returns the redirect target.
// The unchanged ?verified=1 comparisons keep testing the stateless path.
func follow(t *testing.T, link string) string {
	t.Helper()
	action, values := confirmForm(t, link)
	values.Del("state")
	status, location, err := postForm(action, values)
	if err != nil {
		t.Fatalf("POST %s: %v", action, err)
	}
	if status != http.StatusSeeOther {
		t.Fatalf("clicking the confirm button: status %d, want 303", status)
	}
	return location
}

// clickWithState clicks the link's button carrying state and returns the redirect target.
func clickWithState(t *testing.T, link, state string) string {
	t.Helper()
	action, values := confirmFormWithState(t, link, state)
	status, location, err := postForm(action, values)
	if err != nil {
		t.Fatalf("POST %s: %v", action, err)
	}
	if status != http.StatusSeeOther {
		t.Fatalf("clicking the confirm button: status %d, want 303", status)
	}
	return location
}

// followSignedIn clicks the link with a fresh state, requires the hand-off redirect, and returns its code and the state.
func followSignedIn(t *testing.T, link string) (code, state string) {
	t.Helper()
	state = newState(t)
	loc := clickWithState(t, link, state)
	u, err := url.Parse(loc)
	if err != nil || !strings.HasPrefix(loc, siteURL+"/?verified=1&handoff=") {
		t.Fatalf("confirm redirect = %q (%v), want %s/?verified=1&handoff=<code>", loc, err, siteURL)
	}
	return u.Query().Get("handoff"), state
}

func TestIdP_RegisterLeavesTheAccountUnverified(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	u := registrant(t, gw)

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
	gw, _ := startGateway(t, base, 0, nil)
	u := registrant(t, gw)

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

// recordingSink keeps every registrant the gateway hands off and signals each on calls.
type recordingSink struct {
	mu    sync.Mutex
	all   []gateway.RegistrantContact
	calls chan gateway.RegistrantContact
}

func newRecordingSink() *recordingSink {
	return &recordingSink{calls: make(chan gateway.RegistrantContact, 8)}
}

func (s *recordingSink) Registrant(_ context.Context, c gateway.RegistrantContact) error {
	s.mu.Lock()
	s.all = append(s.all, c)
	s.mu.Unlock()
	s.calls <- c
	return nil
}

func (s *recordingSink) DemoRequest(context.Context, gateway.DemoRequest) error { return nil }

func (s *recordingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.all)
}

// next waits for a hand-off, which the gateway sends in the background.
func (s *recordingSink) next(t *testing.T) gateway.RegistrantContact {
	t.Helper()
	select {
	case c := <-s.calls:
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("no hand-off within 10s of following the mailed link")
		return gateway.RegistrantContact{}
	}
}

func TestIdP_EmailedLinkVerifiesThenSignInSucceeds(t *testing.T) {
	base := idpMailURL(t)
	sink := newRecordingSink()
	gw, _ := startGateway(t, base, 0, sink)
	const consentText = "I agree to receive product news from ASComply."
	u := registrant(t, gw, map[string]string{
		"workspace_name": "IdP Hand-off", "display_name": "Ada", "marketing_consent_text": consentText,
	})

	link := confirmationLink(t, u.email)
	if n := sink.count(); n != 0 {
		t.Fatalf("%d registrants handed off before the link was followed, want 0", n)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodGet} {
		openStateless(t, link, method)
	}
	if emailConfirmed(t, u.email) {
		t.Fatal("opening the link confirmed the account")
	}
	if status, body := signIn(t, base, u); status != http.StatusBadRequest || body["error_code"] != "email_not_confirmed" {
		t.Fatalf("password grant after opening the link: status %d, body %v; want 400 email_not_confirmed", status, body)
	}
	if n := sink.count(); n != 0 {
		t.Fatalf("opening the link handed off %d registrants, want 0", n)
	}
	if got := follow(t, link); got != siteURL+"/?verified=1" {
		t.Fatalf("verify redirect = %q, want %s/?verified=1", got, siteURL)
	}

	got := sink.next(t)
	var id string
	if err := superConn(t).QueryRow(context.Background(), `SELECT id::text FROM auth.users WHERE email = $1`, u.email).Scan(&id); err != nil {
		t.Fatalf("read the registered user: %v", err)
	}
	if got.UserID != id || got.Email != u.email || got.DisplayName != "Ada" || got.WorkspaceName != "IdP Hand-off" {
		t.Errorf("hand-off = %+v, want user %s, email %s, Ada, IdP Hand-off", got, id, u.email)
	}
	if got.Consent == nil || got.Consent.Text != consentText {
		t.Fatalf("hand-off consent = %+v, want the text given at register", got.Consent)
	}
	if at, err := time.Parse(time.RFC3339, got.Consent.At); err != nil || time.Since(at) > 2*time.Minute || time.Until(at) > time.Minute {
		t.Errorf("hand-off consent time = %q (%v), want an RFC 3339 time from the register call", got.Consent.At, err)
	}
	if n := sink.count(); n != 1 {
		t.Errorf("%d registrants handed off, want exactly 1", n)
	}
	accessToken(t, base, u)
}

// Two clicks race for one token: GoTrue spends it once, and the gateway hands off once per verified answer.
// With a state, each verified click also yields one code that exchanges for a session.
func TestIdP_TwoConcurrentClicksConfirmOnce(t *testing.T) {
	for _, stateful := range []bool{false, true} {
		t.Run(fmt.Sprintf("stateful=%t", stateful), func(t *testing.T) {
			base := idpMailURL(t)
			sink := newRecordingSink()
			gw, _ := startGateway(t, base, 0, sink)
			u := registrant(t, gw, map[string]string{"workspace_name": "IdP Race", "display_name": "Ada"})
			action, values := confirmForm(t, confirmationLink(t, u.email))
			state := values.Get("state")
			if !stateful {
				values.Del("state")
			}

			type answer struct {
				status   int
				location string
				err      error
			}
			answers := make(chan answer, 2)
			start := make(chan struct{})
			for range 2 {
				go func() {
					<-start
					status, location, err := postForm(action, values)
					answers <- answer{status, location, err}
				}()
			}
			close(start)

			verified := 0
			var codes []string
			for range 2 {
				a := <-answers
				if a.err != nil || a.status != http.StatusSeeOther {
					t.Fatalf("click: status %d, err %v; want 303", a.status, a.err)
				}
				switch {
				case a.location == siteURL+"/?verify=failed":
				case !stateful && a.location == siteURL+"/?verified=1":
					verified++
				case stateful && strings.HasPrefix(a.location, siteURL+"/?verified=1&handoff="):
					verified++
					codes = append(codes, strings.TrimPrefix(a.location, siteURL+"/?verified=1&handoff="))
				default:
					t.Errorf("click redirected to %q, want a ?verified=1 or ?verify=failed answer under %s", a.location, siteURL)
				}
			}
			if verified < 1 {
				t.Fatal("no click landed on ?verified=1")
			}
			if stateful {
				if len(codes) != verified {
					t.Fatalf("%d codes for %d verified clicks, want one each", len(codes), verified)
				}
				for _, code := range codes {
					if status, session := exchange(t, gw, code, state); status != http.StatusOK || session["access_token"] == "" {
						t.Errorf("exchange of a delivered code: status %d, body %v; want 200", status, session)
					}
				}
			}
			if !emailConfirmed(t, u.email) {
				t.Error("the account is not confirmed after the clicks")
			}
			for range verified {
				sink.next(t)
			}
			// A hand-off the gateway sent late would arrive after the wait above.
			time.Sleep(500 * time.Millisecond)
			if n := sink.count(); n != verified {
				t.Errorf("%d hand-offs, want %d (one per ?verified=1 answer)", n, verified)
			}
		})
	}
}

func TestIdP_VerificationLinkIsSingleUse(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	u := registrant(t, gw)

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
	gw, _ := startGateway(t, base, 0, nil)
	answers := map[string]string{"workspace_name": "IdP Works", "display_name": "Ada", "kind": "in_house"}
	u := registrant(t, gw, answers)
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
	// The answers survive register, the mailed link and sign-in, and are the only source of the provision body.
	um, _ := jwtPart(t, first, 1)["user_metadata"].(map[string]any)
	stored, _ := um["registration"].(map[string]any)
	if len(stored) != len(answers) {
		t.Fatalf("first token user_metadata.registration = %v, want exactly %v", um["registration"], answers)
	}
	for k, want := range answers {
		if stored[k] != want {
			t.Errorf("user_metadata.registration[%q] = %v, want %q", k, stored[k], want)
		}
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
	provisionBody, _ := json.Marshal(stored)
	req := httptest.NewRequest(http.MethodPost, "/v1/workspaces", bytes.NewReader(provisionBody))
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

	tenant, me, err := store.Me(auth.WithIdentity(ctx, id))
	role := me.Role
	if err != nil {
		t.Fatalf("Store.Me with the refreshed identity: %v", err)
	}
	if tenant.ID != created.Tenant.ID || tenant.Name != "IdP Works" || tenant.Kind != "in_house" || role != "admin" {
		t.Errorf("Store.Me = %+v role %q, want the new workspace IdP Works (in_house), role admin", tenant, role)
	}
}

// A repeat signup changes neither the password nor the answers.
func TestIdP_RepeatRegistrationKeepsTheFirstAnswers(t *testing.T) {
	base := idpMailURL(t)
	gw, _ := startGateway(t, base, 0, nil)
	first := map[string]string{"workspace_name": "First Co", "display_name": "First", "kind": "firm"}
	u := registrant(t, gw, first)

	repeat, _ := json.Marshal(map[string]string{"email": u.email, "password": "other-" + uuid.NewString(),
		"workspace_name": "Second Co", "display_name": "Second", "kind": "in_house"})
	resp, err := noRedirect.Post(gw+"/auth/register", "application/json", bytes.NewReader(repeat))
	if err != nil {
		t.Fatalf("POST /auth/register (repeat): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("repeat register status = %d, want 202", resp.StatusCode)
	}

	if got := follow(t, confirmationLink(t, u.email)); got != siteURL+"/?verified=1" {
		t.Fatalf("verify redirect = %q, want %s/?verified=1", got, siteURL)
	}
	status, session := signIn(t, base, u)
	tok, _ := session["access_token"].(string)
	if status != http.StatusOK || tok == "" {
		t.Fatalf("sign in with the first password: status %d, body %v", status, session)
	}
	um, _ := jwtPart(t, tok, 1)["user_metadata"].(map[string]any)
	stored, _ := um["registration"].(map[string]any)
	if len(stored) != len(first) {
		t.Fatalf("user_metadata.registration = %v, want exactly the first registrant's %v", um["registration"], first)
	}
	for k, want := range first {
		if stored[k] != want {
			t.Errorf("user_metadata.registration[%q] = %v, want %q", k, stored[k], want)
		}
	}
}
