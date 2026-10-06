package tenancy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

// apiSender records every Send; fn, when set, decides the answer.
type apiSender struct {
	mu    sync.Mutex
	calls [][]accountmail.Message
	fn    func(ctx context.Context, msgs []accountmail.Message) error
}

func (s *apiSender) Send(ctx context.Context, msgs []accountmail.Message) error {
	s.mu.Lock()
	s.calls = append(s.calls, slices.Clone(msgs))
	s.mu.Unlock()
	if s.fn != nil {
		return s.fn(ctx, msgs)
	}
	return nil
}

func (s *apiSender) sent() [][]accountmail.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func (s *apiSender) messages() []accountmail.Message {
	var out []accountmail.Message
	for _, c := range s.sent() {
		out = append(out, c...)
	}
	return out
}

func failing500() *apiSender {
	return &apiSender{fn: func(context.Context, []accountmail.Message) error { return &accountmail.SendError{Status: 500} }}
}

// failOnce fails the first Send with a vendor 500, then succeeds.
func failOnce() *apiSender {
	var n atomic.Int32
	return &apiSender{fn: func(context.Context, []accountmail.Message) error {
		if n.Add(1) == 1 {
			return &accountmail.SendError{Status: 500}
		}
		return nil
	}}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type apiWorld struct {
	invWorld
	logs *syncBuf
}

func newAPIWorld(t *testing.T, tenantName, adminName string) apiWorld {
	t.Helper()
	return apiWorld{invWorld: newInvWorld(t, tenantName, adminName), logs: &syncBuf{}}
}

// handler wires the three routes over one Inviter that sends through s.
func (a apiWorld) handler(s accountmail.Sender) http.Handler {
	log := slog.New(slog.NewTextHandler(a.logs, nil))
	inv := &Inviter{Store: a.store, Sender: s, Logger: log}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/invitations", InvitationsCreateHandler(inv.Invite, log))
	mux.HandleFunc("GET /v1/invitations", InvitationsListHandler(a.store.ListInvitations, log))
	mux.HandleFunc("POST /v1/invitations/{id}/resend", InvitationResendHandler(inv.Resend, log))
	return mux
}

func apiDo(h http.Handler, ctx context.Context, method, target, body string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, method, target, rd))
	return rec
}

func invitePost(emails []string, role string) string {
	b, _ := json.Marshal(map[string]any{"emails": emails, "role": role})
	return string(b)
}

func resendTarget(id string) string { return "/v1/invitations/" + id + "/resend" }

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

var (
	createItemKeys = []string{"delivery", "email", "expires_at", "id", "role", "status"}
	listItemKeys   = []string{"created_at", "delivery", "email", "expires_at", "id", "invited_by", "role", "status"}
)

func apiItems(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var body struct {
		Invitations []map[string]any `json:"invitations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return body.Invitations
}

func apiItem(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var item map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return item
}

func itemID(t *testing.T, item map[string]any) string {
	t.Helper()
	id, _ := item["id"].(string)
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("item id %v is not a uuid: %v", item["id"], err)
	}
	return id
}

// assertErrorBody checks the status and that the body is exactly {"error": msg}.
func assertErrorBody(t *testing.T, rec *httptest.ResponseRecorder, status int, msg string) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status = %d, want %d: %s", rec.Code, status, rec.Body)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Errorf("body %q is not JSON: %v", rec.Body, err)
		return
	}
	if !slices.Equal(keysOf(m), []string{"error"}) {
		t.Errorf("error body keys = %v, want exactly [error]: %s", keysOf(m), rec.Body)
	}
	if m["error"] != msg {
		t.Errorf("error = %q, want %q", m["error"], msg)
	}
}

var inviteTokenRe = regexp.MustCompile(regexp.QuoteMeta(accountmail.InviteAcceptURL) + `#token=([A-Za-z0-9_-]{43})(?:[^A-Za-z0-9_-]|$)`)

func mailToken(t *testing.T, m accountmail.Message) string {
	t.Helper()
	g := inviteTokenRe.FindStringSubmatch(m.HTML)
	if g == nil {
		t.Fatalf("mail to %s holds no accept URL with a 43-character token", m.To)
	}
	return g[1]
}

func requireHashMatchesRow(t *testing.T, w invWorld, email, token string) {
	t.Helper()
	row := pendingRow(t, w.super, w.tenant, email)
	if !bytes.Equal(row.Hash, sha(token)) {
		t.Errorf("sha256 of the mailed token for %s does not equal the row's token_hash", email)
	}
}

func longAddress(n int) string {
	const domain = "@x.test"
	return strings.Repeat("a", n-len(domain)) + domain
}

func TestInvitationsAPI_AdminInviteSendsOneBatch(t *testing.T) {
	a := newAPIWorld(t, "Obi Partners", "Ada Obi")
	s := &apiSender{}
	emails := []string{"b@x.test", "c@x.test"}

	items := apiItems(t, apiDo(a.handler(s), a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost(emails, "reviewer")))

	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	for i, e := range emails {
		it := items[i]
		if !slices.Equal(keysOf(it), createItemKeys) {
			t.Errorf("item %d keys = %v, want %v", i, keysOf(it), createItemKeys)
		}
		if it["email"] != e || it["role"] != "reviewer" || it["status"] != "pending" || it["delivery"] != "sent" {
			t.Errorf("item %d = %v, want email %s, role reviewer, status pending, delivery sent", i, it, e)
		}
		itemID(t, it)
		exp, err := time.Parse(time.RFC3339, fmt.Sprint(it["expires_at"]))
		if err != nil {
			t.Fatalf("item %d expires_at %v is not RFC 3339: %v", i, it["expires_at"], err)
		}
		near(t, "expires_at", exp, inviteExpiry(), time.Minute)
	}

	calls := s.sent()
	if len(calls) != 1 || len(calls[0]) != 2 {
		t.Fatalf("sender calls = %d, want one call of two messages", len(calls))
	}
	for i, m := range calls[0] {
		if m.To != emails[i] || m.Subject != accountmail.InviteSubject {
			t.Errorf("message %d to/subject = %q/%q, want %q/%q", i, m.To, m.Subject, emails[i], accountmail.InviteSubject)
		}
		for _, want := range []string{"Obi Partners", "Ada Obi", "Reviewer", "7 days"} {
			if !strings.Contains(m.HTML, want) {
				t.Errorf("message %d HTML does not name %q", i, want)
			}
		}
		requireHashMatchesRow(t, a.invWorld, emails[i], mailToken(t, m))
		row := pendingRow(t, a.super, a.tenant, emails[i])
		if row.SendStatus != "sent" {
			t.Errorf("row %s send_status = %q, want sent", emails[i], row.SendStatus)
		}
		if row.ID != items[i]["id"] {
			t.Errorf("row %s id = %s, item id = %v", emails[i], row.ID, items[i]["id"])
		}
	}
}

func TestInvitationsAPI_NoEnumeration(t *testing.T) {
	a := newAPIWorld(t, "Enumeration API Tenant", "Ada Obi")
	other := newInvWorld(t, "Elsewhere API Tenant", "Olu Bee")
	seedIdentityMembership(t, other.super, other.tenant, uuid.NewString(), "admin", "active", strp("Taken Person"), strp("taken@x.test"))
	s := &apiSender{}

	items := apiItems(t, apiDo(a.handler(s), a.adminCtx(), http.MethodPost, "/v1/invitations",
		invitePost([]string{"taken@x.test", "fresh@x.test"}, "preparer")))

	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if !slices.Equal(keysOf(items[0]), keysOf(items[1])) {
		t.Errorf("key sets differ: %v vs %v", keysOf(items[0]), keysOf(items[1]))
	}
	if items[0]["delivery"] != "sent" || items[1]["delivery"] != "sent" {
		t.Errorf("delivery = %v / %v, want sent for both", items[0]["delivery"], items[1]["delivery"])
	}
	if n := countInvitations(t, a.super, other.tenant); n != 0 {
		t.Errorf("the other tenant gained %d invitations rows", n)
	}
}

func TestInvitationsAPI_ValidationRefusals(t *testing.T) {
	a := newAPIWorld(t, "Validation API Tenant", "Ada Obi")
	s := &apiSender{}
	h := a.handler(s)

	base := `{"emails":["ok@x.test"],"role":"preparer","pad":""}`
	oversize := strings.Replace(base, `"pad":""`, `"pad":"`+strings.Repeat("a", 16*1024+1-len(base))+`"`, 1)
	if len(oversize) != 16*1024+1 {
		t.Fatalf("oversize body is %d bytes, want %d", len(oversize), 16*1024+1)
	}
	const (
		badRole  = `role must be "admin", "preparer" or "reviewer"`
		badCount = "emails must hold 1 to 20 addresses"
	)
	cases := []struct{ name, body, want string }{
		{"malformed JSON", `{`, "invalid request body"},
		{"body over 16 KiB", oversize, "invalid request body"},
		{"no role", `{"emails":["ok@x.test"]}`, badRole},
		{"unknown role", `{"emails":["ok@x.test"],"role":"owner"}`, badRole},
		{"no addresses", `{"emails":[],"role":"preparer"}`, badCount},
		{"21 distinct addresses", invitePost(addrs("v", 21), "preparer"), badCount},
		{"malformed addresses", `{"emails":["ok@x.test","nope","a@b"],"role":"preparer"}`, `invalid email address: "nope", "a@b"`},
		{"a refused address is named as sent, not normalised", `{"emails":["ok@x.test"," Nope@X "],"role":"preparer"}`, `invalid email address: " Nope@X "`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertErrorBody(t, apiDo(h, a.adminCtx(), http.MethodPost, "/v1/invitations", c.body), http.StatusBadRequest, c.want)
			if n := countInvitations(t, a.super, a.tenant); n != 0 {
				t.Errorf("invitations rows after a refusal = %d, want 0", n)
			}
			if n := inviteAudit(t, a.super, a.tenant); n != 0 {
				t.Errorf("invitation audit rows after a refusal = %d, want 0", n)
			}
			if n := len(s.sent()); n != 0 {
				t.Errorf("sender calls after a refusal = %d, want 0", n)
			}
		})
	}

	// Control: the same tenant still sends, so the zero counts above are not a dead path.
	apiItems(t, apiDo(h, a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost([]string{"ok@x.test"}, "preparer")))
	if n := len(s.sent()); n != 1 {
		t.Errorf("sender calls after the valid control = %d, want 1", n)
	}
}

func TestInvitationsAPI_AddressRuleRefusals(t *testing.T) {
	a := newAPIWorld(t, "Address Rule API Tenant", "Ada Obi")
	s := &apiSender{}
	h := a.handler(s)

	bad := []struct{ name, addr string }{
		{"empty", ""},
		{"NUL", "a@b.co\u0000"},
		{"tab", "a\tb@x.test"},
		{"newline", "a\nb@x.test"},
		{"U+0085", "a\u0085b@x.test"},
		{"255 bytes", longAddress(255)},
		{"display name", "name<a@b.co>"},
		{"quoted local part", `"x"@b.co`},
		{"trailing angle bracket", "a@b.co>"},
		{"trailing dot", "a@b.co."},
		{"empty domain label", "a@b..co"},
		{"leading dot in the local part", ".a@b.co"},
		{"two at signs", "a@@b.co"},
		{"space inside", "a b@x.test"},
		{"comma", "a@x.test,"},
		{"angle brackets", "<a@x.test>"},
	}
	if got := len(bad[5].addr); got != 255 {
		t.Fatalf("long address is %d bytes, want 255", got)
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			rec := apiDo(h, a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost([]string{"ok@x.test", c.addr}, "preparer"))
			assertErrorBody(t, rec, http.StatusBadRequest, fmt.Sprintf("invalid email address: %q", c.addr))
			if n := countInvitations(t, a.super, a.tenant); n != 0 {
				t.Errorf("invitations rows after a refusal = %d, want 0", n)
			}
			if n := inviteAudit(t, a.super, a.tenant); n != 0 {
				t.Errorf("invitation audit rows after a refusal = %d, want 0", n)
			}
			if n := len(s.sent()); n != 0 {
				t.Errorf("sender calls after a refusal = %d, want 0", n)
			}
		})
	}

	// Control: the valid neighbour alone is accepted.
	apiItems(t, apiDo(h, a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost([]string{"ok@x.test"}, "preparer")))
	if n := countInvitations(t, a.super, a.tenant); n != 1 {
		t.Errorf("invitations rows after the valid control = %d, want 1", n)
	}
}

func TestInvitationsAPI_NormalisesAndDedupes(t *testing.T) {
	a := newAPIWorld(t, "Dedupe API Tenant", "Ada Obi")
	s := &apiSender{}

	items := apiItems(t, apiDo(a.handler(s), a.adminCtx(), http.MethodPost, "/v1/invitations",
		invitePost([]string{" A@X.test ", "a@x.test"}, "preparer")))

	if len(items) != 1 || items[0]["email"] != "a@x.test" {
		t.Fatalf("items = %v, want one item for a@x.test", items)
	}
	msgs := s.messages()
	if len(msgs) != 1 || msgs[0].To != "a@x.test" {
		t.Errorf("messages = %+v, want one mail to a@x.test", msgs)
	}
	if n := countInvitations(t, a.super, a.tenant); n != 1 {
		t.Errorf("invitations rows = %d, want 1", n)
	}
}

func TestInvitationsAPI_TwentyAddressesAreAccepted(t *testing.T) {
	t.Run("20 distinct", func(t *testing.T) {
		a := newAPIWorld(t, "Twenty API Tenant", "Ada Obi")
		s := &apiSender{}
		want := addrs("t", 20)
		items := apiItems(t, apiDo(a.handler(s), a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost(want, "preparer")))
		if len(items) != 20 {
			t.Fatalf("items = %d, want 20", len(items))
		}
		for i, e := range want {
			if items[i]["email"] != e {
				t.Errorf("item %d email = %v, want %s", i, items[i]["email"], e)
			}
		}
		if calls := s.sent(); len(calls) != 1 || len(calls[0]) != 20 {
			t.Errorf("sender calls = %d, want one call of 20 messages", len(calls))
		}
	})

	t.Run("21 raw addresses that dedupe to 20", func(t *testing.T) {
		a := newAPIWorld(t, "Twenty One API Tenant", "Ada Obi")
		s := &apiSender{}
		raw := append(addrs("t", 20), " T1@X.TEST ")
		items := apiItems(t, apiDo(a.handler(s), a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost(raw, "preparer")))
		if len(items) != 20 {
			t.Fatalf("items = %d, want 20", len(items))
		}
		if n := len(s.messages()); n != 20 {
			t.Errorf("messages = %d, want 20", n)
		}
	})

	t.Run("one 254-byte address", func(t *testing.T) {
		a := newAPIWorld(t, "Long Address API Tenant", "Ada Obi")
		s := &apiSender{}
		addr := longAddress(254)
		if len(addr) != 254 {
			t.Fatalf("address is %d bytes, want 254", len(addr))
		}
		items := apiItems(t, apiDo(a.handler(s), a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost([]string{addr}, "preparer")))
		if len(items) != 1 || items[0]["email"] != addr {
			t.Fatalf("items = %v, want one item for the 254-byte address", items)
		}
		pendingRow(t, a.super, a.tenant, addr)
	})
}

func TestInvitationsAPI_StatusMapping(t *testing.T) {
	const createBody = `{"emails":["b@x.test"],"role":"preparer"}`
	const (
		notAdmin = "only an admin can invite people"
		limitMsg = "daily invite limit reached: 20 invite mails per workspace per 24 hours"
	)
	type req struct {
		ctx                  context.Context
		method, target, body string
	}
	create := func(ctx context.Context) req { return req{ctx, http.MethodPost, "/v1/invitations", createBody} }
	list := func(ctx context.Context) req { return req{ctx, http.MethodGet, "/v1/invitations", ""} }
	resend := func(ctx context.Context, id string) req { return req{ctx, http.MethodPost, resendTarget(id), ""} }
	someID := uuid.NewString()

	cases := []struct {
		name   string
		status int
		msg    string
		build  func(t *testing.T, a apiWorld) req
	}{
		{"no identity, create", 401, "unauthorized", func(t *testing.T, a apiWorld) req { return create(context.Background()) }},
		{"no identity, create with a malformed body", 401, "unauthorized", func(t *testing.T, a apiWorld) req {
			return req{context.Background(), http.MethodPost, "/v1/invitations", `{`}
		}},
		{"no identity, list", 401, "unauthorized", func(t *testing.T, a apiWorld) req { return list(context.Background()) }},
		{"no identity, resend", 401, "unauthorized", func(t *testing.T, a apiWorld) req { return resend(context.Background(), someID) }},
		{"preparer, create", 403, notAdmin, func(t *testing.T, a apiWorld) req {
			return create(a.as(a.addMember(t, "preparer", "active", "Pat")))
		}},
		{"preparer, list", 403, notAdmin, func(t *testing.T, a apiWorld) req {
			return list(a.as(a.addMember(t, "preparer", "active", "Pat")))
		}},
		{"preparer, resend", 403, notAdmin, func(t *testing.T, a apiWorld) req {
			return resend(a.as(a.addMember(t, "preparer", "active", "Pat")), someID)
		}},
		{"suspended admin, create", 403, db.NotActiveMemberMessage, func(t *testing.T, a apiWorld) req {
			return create(a.as(a.addMember(t, "admin", "suspended", "Sue")))
		}},
		{"suspended admin, list", 403, db.NotActiveMemberMessage, func(t *testing.T, a apiWorld) req {
			return list(a.as(a.addMember(t, "admin", "suspended", "Sue")))
		}},
		{"suspended admin, resend", 403, db.NotActiveMemberMessage, func(t *testing.T, a apiWorld) req {
			return resend(a.as(a.addMember(t, "admin", "suspended", "Sue")), someID)
		}},
		{"over the daily limit, create", 429, limitMsg, func(t *testing.T, a apiWorld) req {
			seedAuditEvents(t, a.super, a.tenant, "invitation.sent", sentPayload, "1 hour", 20)
			return create(a.adminCtx())
		}},
		{"over the daily limit, resend", 429, limitMsg, func(t *testing.T, a apiWorld) req {
			seedAuditEvents(t, a.super, a.tenant, "invitation.sent", sentPayload, "1 hour", 20)
			inv := seedInvitation(t, a.super, seedInv{tenant: a.tenant, email: "s@x.test", invitedBy: a.admin, sendStatus: "sent"})
			return resend(a.adminCtx(), inv.ID)
		}},
		{"resend of a missing id", 404, "invitation not found", func(t *testing.T, a apiWorld) req {
			return resend(a.adminCtx(), uuid.NewString())
		}},
		{"resend of a non-uuid id", 404, "invitation not found", func(t *testing.T, a apiWorld) req {
			return resend(a.adminCtx(), "not-a-uuid")
		}},
		{"resend of an accepted invite", 409, "this invite is no longer pending", func(t *testing.T, a apiWorld) req {
			inv := seedInvitation(t, a.super, seedInv{tenant: a.tenant, email: "a@x.test", status: "accepted", invitedBy: a.admin})
			return resend(a.adminCtx(), inv.ID)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newAPIWorld(t, "Status API Tenant", "Ada Obi")
			s := &apiSender{}
			r := c.build(t, a)
			assertErrorBody(t, apiDo(a.handler(s), r.ctx, r.method, r.target, r.body), c.status, c.msg)
			if n := len(s.sent()); n != 0 {
				t.Errorf("sender calls after a refusal = %d, want 0", n)
			}
		})
	}

	t.Run("control: the admin's create reaches the sender", func(t *testing.T) {
		a := newAPIWorld(t, "Status API Control Tenant", "Ada Obi")
		s := &apiSender{}
		apiItems(t, apiDo(a.handler(s), a.adminCtx(), http.MethodPost, "/v1/invitations", createBody))
		if n := len(s.sent()); n != 1 {
			t.Errorf("sender calls = %d, want 1", n)
		}
	})
}

func TestInvitationsAPI_FailedSendIsVisible(t *testing.T) {
	_, sentry, _ := sentrytest.Boot(t, "tenancy")
	a := newAPIWorld(t, "Failed Send API Tenant", "Ada Obi")
	s := failing500()

	items := apiItems(t, apiDo(a.handler(s), a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost([]string{"b@x.test"}, "preparer")))

	if len(items) != 1 || items[0]["delivery"] != "failed" || items[0]["status"] != "pending" {
		t.Fatalf("items = %v, want one pending item with delivery failed", items)
	}
	if row := pendingRow(t, a.super, a.tenant, "b@x.test"); row.SendStatus != "failed" {
		t.Errorf("send_status = %q, want failed", row.SendStatus)
	}
	calls := s.sent()
	if len(calls) != 1 || len(calls[0]) != 1 {
		t.Fatalf("sender calls = %d, want one call of one message", len(calls))
	}
	token := mailToken(t, calls[0][0])

	logs := a.logs.String()
	if n := strings.Count(logs, "tenancy: invite mail failed"); n != 1 {
		t.Errorf("log lines %q = %d, want 1:\n%s", "tenancy: invite mail failed", n, logs)
	}
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "tenancy: invite mail failed") && !strings.Contains(line, "level=ERROR") {
			t.Errorf("failure line is not ERROR: %s", line)
		}
	}
	for _, secret := range []string{"b@x.test", token} {
		if strings.Contains(logs, secret) {
			t.Errorf("log holds %q:\n%s", secret, logs)
		}
	}
	if !strings.Contains(logs, "count=1") || !strings.Contains(logs, "status=500") || !strings.Contains(logs, "status 500") {
		t.Errorf("failure line lacks count=1, status=500 or the status text %q:\n%s", "status 500", logs)
	}

	events := sentry.Events()
	if len(events) != 1 {
		t.Fatalf("recorded %d Sentry events, want exactly 1", len(events))
	}
	raw, err := json.Marshal(events[0])
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	for _, secret := range []string{"b@x.test", token, accountmail.InviteAcceptURL} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("Sentry event holds %q: %s", secret, raw)
		}
	}
	var hasStatus bool
	for _, ex := range events[0].Exception {
		hasStatus = hasStatus || strings.Contains(ex.Value, "status 500")
	}
	if !hasStatus {
		t.Errorf("no exception value holds %q: %s", "status 500", raw)
	}
}

func TestInvitationsAPI_UnconfiguredSenderFails(t *testing.T) {
	a := newAPIWorld(t, "Off API Tenant", "Ada Obi")

	items := apiItems(t, apiDo(a.handler(accountmail.Off{}), a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost([]string{"b@x.test"}, "preparer")))

	if len(items) != 1 || items[0]["delivery"] != "failed" {
		t.Fatalf("items = %v, want one item with delivery failed", items)
	}
	if row := pendingRow(t, a.super, a.tenant, "b@x.test"); row.SendStatus != "failed" {
		t.Errorf("send_status = %q, want failed", row.SendStatus)
	}
	if logs := a.logs.String(); !strings.Contains(logs, "status=0") {
		t.Errorf("failure line lacks status=0:\n%s", logs)
	}
}

func TestInvitationsAPI_ResendAfterFailureSends(t *testing.T) {
	a := newAPIWorld(t, "Resend After Failure API Tenant", "Ada Obi")
	items := apiItems(t, apiDo(a.handler(failing500()), a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost([]string{"b@x.test"}, "preparer")))
	if len(items) != 1 || items[0]["delivery"] != "failed" {
		t.Fatalf("first send items = %v, want delivery failed", items)
	}
	id := itemID(t, items[0])
	ok := &apiSender{}

	item := apiItem(t, apiDo(a.handler(ok), a.adminCtx(), http.MethodPost, resendTarget(id), ""))

	if item["delivery"] != "sent" || item["id"] != id {
		t.Errorf("resend item = %v, want id %s and delivery sent", item, id)
	}
	if row := pendingRow(t, a.super, a.tenant, "b@x.test"); row.SendStatus != "sent" {
		t.Errorf("send_status = %q, want sent", row.SendStatus)
	}
	if n := len(ok.messages()); n != 1 {
		t.Errorf("resend mails = %d, want 1", n)
	}
}

func TestInvitationsAPI_FailedBatchThenResendWithinBudget(t *testing.T) {
	a := newAPIWorld(t, "Failed Batch API Tenant", "Ada Obi")
	s := failOnce()
	h := a.handler(s)
	emails := addrs("f", 12)

	items := apiItems(t, apiDo(h, a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost(emails, "preparer")))
	if len(items) != 12 {
		t.Fatalf("items = %d, want 12", len(items))
	}
	for i, it := range items {
		if it["delivery"] != "failed" {
			t.Fatalf("item %d delivery = %v, want failed", i, it["delivery"])
		}
	}

	for i, it := range items {
		got := apiItem(t, apiDo(h, a.adminCtx(), http.MethodPost, resendTarget(itemID(t, it)), ""))
		if got["delivery"] != "sent" {
			t.Errorf("resend %d delivery = %v, want sent", i, got["delivery"])
		}
	}
	for _, e := range emails {
		if row := pendingRow(t, a.super, a.tenant, e); row.SendStatus != "sent" {
			t.Errorf("row %s send_status = %q, want sent", e, row.SendStatus)
		}
	}

	// 12 counted + 1 fits; had the 12 resends counted, 24 + 1 would be a 429.
	next := apiItems(t, apiDo(h, a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost([]string{"new@x.test"}, "preparer")))
	if len(next) != 1 {
		t.Errorf("items after the resends = %d, want 1", len(next))
	}
	if n := len(s.sent()); n != 14 {
		t.Errorf("sender calls = %d, want 14 (1 batch, 12 resends, 1 new)", n)
	}
}

func TestInvitationsAPI_CancelledClientStillRecordsTheSend(t *testing.T) {
	a := newAPIWorld(t, "Cancelled API Tenant", "Ada Obi")
	ctx, cancel := context.WithCancel(a.adminCtx())
	defer cancel()
	var sendCtxErr error
	s := &apiSender{fn: func(c context.Context, _ []accountmail.Message) error {
		cancel()
		sendCtxErr = c.Err()
		return nil
	}}

	apiItems(t, apiDo(a.handler(s), ctx, http.MethodPost, "/v1/invitations", invitePost([]string{"b@x.test"}, "preparer")))

	if n := len(s.sent()); n != 1 {
		t.Fatalf("sender calls = %d, want 1", n)
	}
	if sendCtxErr != nil {
		t.Errorf("the send ran on a cancelled context: %v", sendCtxErr)
	}
	if row := pendingRow(t, a.super, a.tenant, "b@x.test"); row.SendStatus != "sent" {
		t.Errorf("send_status = %q, want sent (not sending)", row.SendStatus)
	}
}

func TestInvitationsAPI_ListShape(t *testing.T) {
	a := newAPIWorld(t, "List API Tenant", "Ada Obi")
	h := a.handler(&apiSender{})

	empty := apiDo(h, a.adminCtx(), http.MethodGet, "/v1/invitations", "")
	if empty.Code != http.StatusOK || strings.TrimSpace(empty.Body.String()) != `{"invitations":[]}` {
		t.Errorf("empty list = %d %s, want 200 {\"invitations\":[]}", empty.Code, empty.Body)
	}

	apiItems(t, apiDo(h, a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost([]string{"b@x.test", "c@x.test"}, "reviewer")))
	items := apiItems(t, apiDo(h, a.adminCtx(), http.MethodGet, "/v1/invitations", ""))
	if len(items) != 2 {
		t.Fatalf("list items = %d, want 2", len(items))
	}
	var got []string
	for i, it := range items {
		if !slices.Equal(keysOf(it), listItemKeys) {
			t.Errorf("item %d keys = %v, want %v", i, keysOf(it), listItemKeys)
		}
		if it["status"] != "pending" || it["role"] != "reviewer" || it["delivery"] != "sent" || it["invited_by"] != a.admin {
			t.Errorf("item %d = %v, want pending reviewer sent invited by %s", i, it, a.admin)
		}
		got = append(got, fmt.Sprint(it["email"]))
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"b@x.test", "c@x.test"}) {
		t.Errorf("listed emails = %v", got)
	}

	prep := a.as(a.addMember(t, "preparer", "active", "Pat"))
	assertErrorBody(t, apiDo(h, prep, http.MethodGet, "/v1/invitations", ""), http.StatusForbidden, "only an admin can invite people")
}

func TestInvitationsAPI_ListShowsDeliveryAfterFailureAndResend(t *testing.T) {
	a := newAPIWorld(t, "List Delivery API Tenant", "Ada Obi")
	items := apiItems(t, apiDo(a.handler(failing500()), a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost([]string{"b@x.test"}, "preparer")))
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	id := itemID(t, items[0])
	ok := a.handler(&apiSender{})

	listed := apiItems(t, apiDo(ok, a.adminCtx(), http.MethodGet, "/v1/invitations", ""))
	if len(listed) != 1 || listed[0]["delivery"] != "failed" {
		t.Fatalf("list after a failed send = %v, want delivery failed", listed)
	}

	apiItem(t, apiDo(ok, a.adminCtx(), http.MethodPost, resendTarget(id), ""))
	listed = apiItems(t, apiDo(ok, a.adminCtx(), http.MethodGet, "/v1/invitations", ""))
	if len(listed) != 1 || listed[0]["delivery"] != "sent" {
		t.Errorf("list after a resend = %v, want delivery sent", listed)
	}
}

func TestInvitationsAPI_ResendMailsANewToken(t *testing.T) {
	a := newAPIWorld(t, "Resend Token API Tenant", "Ada Obi")
	s := &apiSender{}
	h := a.handler(s)
	items := apiItems(t, apiDo(h, a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost([]string{"b@x.test"}, "preparer")))
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	id := itemID(t, items[0])
	first := s.messages()
	if len(first) != 1 {
		t.Fatalf("mails after the invite = %d, want 1", len(first))
	}
	t1 := mailToken(t, first[0])

	item := apiItem(t, apiDo(h, a.adminCtx(), http.MethodPost, resendTarget(id), ""))

	if !slices.Equal(keysOf(item), createItemKeys) || item["id"] != id || item["delivery"] != "sent" {
		t.Errorf("resend item = %v, want the create item shape for %s with delivery sent", item, id)
	}
	msgs := s.messages()
	if len(msgs) != 2 {
		t.Fatalf("mails after the resend = %d, want 2", len(msgs))
	}
	t2 := mailToken(t, msgs[1])
	if t2 == t1 {
		t.Error("the resend mailed the same token")
	}
	if rows := invRows(t, a.super, a.tenant, "token_hash = $2", sha(t1)); len(rows) != 0 {
		t.Errorf("the old token still matches %d row(s)", len(rows))
	}
	requireHashMatchesRow(t, a.invWorld, "b@x.test", t2)
}

func TestInvitationsAPI_DoubleSubmitReissues(t *testing.T) {
	a := newAPIWorld(t, "Double Submit API Tenant", "Ada Obi")
	s := &apiSender{}
	h := a.handler(s)
	body := `{"emails":["b@x.test"],"role":"preparer"}`

	first := apiItems(t, apiDo(h, a.adminCtx(), http.MethodPost, "/v1/invitations", body))
	second := apiItems(t, apiDo(h, a.adminCtx(), http.MethodPost, "/v1/invitations", body))

	if len(first) != 1 || len(second) != 1 || first[0]["id"] != second[0]["id"] {
		t.Fatalf("ids = %v / %v, want the same id twice", first, second)
	}
	if rows := invRows(t, a.super, a.tenant, "status = 'pending'"); len(rows) != 1 {
		t.Errorf("pending rows = %d, want 1", len(rows))
	}
	msgs := s.messages()
	if len(msgs) != 2 {
		t.Fatalf("mails = %d, want 2", len(msgs))
	}
	t1, t2 := mailToken(t, msgs[0]), mailToken(t, msgs[1])
	if rows := invRows(t, a.super, a.tenant, "token_hash = $2", sha(t1)); len(rows) != 0 {
		t.Errorf("the first mail's token still matches %d row(s)", len(rows))
	}
	requireHashMatchesRow(t, a.invWorld, "b@x.test", t2)
}

type apiRoundTrip func(*http.Request) (*http.Response, error)

func (f apiRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInvitationsAPI_CaptureHoldsWhatWouldBeSent(t *testing.T) {
	a := newAPIWorld(t, "Capture API Tenant", "Ada Obi")
	prev := http.DefaultTransport
	http.DefaultTransport = apiRoundTrip(func(r *http.Request) (*http.Response, error) {
		t.Errorf("network call to %s", r.URL)
		return nil, errors.New("no network in this test")
	})
	t.Cleanup(func() { http.DefaultTransport = prev })
	c := &accountmail.Capture{}

	items := apiItems(t, apiDo(a.handler(c), a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost([]string{"b@x.test"}, "preparer")))

	if len(items) != 1 || items[0]["delivery"] != "sent" {
		t.Fatalf("items = %v, want one item with delivery sent", items)
	}
	msgs := c.Messages()
	if len(msgs) != 1 {
		t.Fatalf("captured mails = %d, want 1", len(msgs))
	}
	if msgs[0].To != "b@x.test" || msgs[0].Subject != accountmail.InviteSubject {
		t.Errorf("captured to/subject = %q/%q", msgs[0].To, msgs[0].Subject)
	}
	for _, want := range []string{"Capture API Tenant", "Ada Obi", "Preparer"} {
		if !strings.Contains(msgs[0].HTML, want) {
			t.Errorf("captured HTML does not name %q", want)
		}
	}
	requireHashMatchesRow(t, a.invWorld, "b@x.test", mailToken(t, msgs[0]))
}

func TestInvitationsAPI_UnicodeAddressIsAcceptedAndLowerCased(t *testing.T) {
	a := newAPIWorld(t, "Unicode API Tenant", "Ada Obi")
	s := &apiSender{}

	items := apiItems(t, apiDo(a.handler(s), a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost([]string{"  É@Ünï.TEST "}, "preparer")))

	if len(items) != 1 || items[0]["email"] != "é@ünï.test" {
		t.Fatalf("items = %v, want one item for é@ünï.test", items)
	}
	if msgs := s.messages(); len(msgs) != 1 || msgs[0].To != "é@ünï.test" {
		t.Errorf("messages = %+v, want one mail to é@ünï.test", msgs)
	}
	pendingRow(t, a.super, a.tenant, "é@ünï.test")
}

func TestInvitationsAPI_BodyShapes(t *testing.T) {
	a := newAPIWorld(t, "Body Shape API Tenant", "Ada Obi")
	s := &apiSender{}
	h := a.handler(s)
	const (
		badBody = "invalid request body"
		badRole = `role must be "admin", "preparer" or "reviewer"`
	)

	refused := []struct{ name, body, want string }{
		{"empty body", ``, badBody},
		{"JSON array", `[]`, badBody},
		{"emails is a string", `{"emails":"ok@x.test","role":"preparer"}`, badBody},
		{"emails holds a number", `{"emails":["ok@x.test",5],"role":"preparer"}`, badBody},
		{"role is a number", `{"emails":["ok@x.test"],"role":5}`, badBody},
		{"role is null", `{"emails":["ok@x.test"],"role":null}`, badRole},
		{"JSON null", `null`, badRole},
		{"role in another case", `{"emails":["ok@x.test"],"role":"Admin"}`, badRole},
		{"role with a space", `{"emails":["ok@x.test"],"role":" admin"}`, badRole},
		{"emails is null", `{"emails":null,"role":"preparer"}`, "emails must hold 1 to 20 addresses"},
		{"a null address", `{"emails":["ok@x.test",null],"role":"preparer"}`, `invalid email address: ""`},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			var rd io.Reader
			if c.body != "" {
				rd = strings.NewReader(c.body)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(a.adminCtx(), http.MethodPost, "/v1/invitations", rd))
			assertErrorBody(t, rec, http.StatusBadRequest, c.want)
		})
	}
	if n := countInvitations(t, a.super, a.tenant); n != 0 {
		t.Errorf("invitations rows after the refusals = %d, want 0", n)
	}
	if n := inviteAudit(t, a.super, a.tenant); n != 0 {
		t.Errorf("invitation audit rows after the refusals = %d, want 0", n)
	}
	if n := len(s.sent()); n != 0 {
		t.Errorf("sender calls after the refusals = %d, want 0", n)
	}

	t.Run("a body of exactly 16 KiB is accepted", func(t *testing.T) {
		base := `{"emails":["ok@x.test"],"role":"preparer","pad":""}`
		body := strings.Replace(base, `"pad":""`, `"pad":"`+strings.Repeat("a", 16*1024-len(base))+`"`, 1)
		if len(body) != 16*1024 {
			t.Fatalf("body is %d bytes, want %d", len(body), 16*1024)
		}
		if items := apiItems(t, apiDo(h, a.adminCtx(), http.MethodPost, "/v1/invitations", body)); len(items) != 1 {
			t.Errorf("items = %d, want 1", len(items))
		}
	})

	t.Run("every role is accepted", func(t *testing.T) {
		for _, role := range []string{"admin", "preparer", "reviewer"} {
			items := apiItems(t, apiDo(h, a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost([]string{role + "@x.test"}, role)))
			if len(items) != 1 || items[0]["role"] != role {
				t.Errorf("role %s: items = %v, want one item with that role", role, items)
			}
		}
	})

	t.Run("unknown fields are ignored", func(t *testing.T) {
		items := apiItems(t, apiDo(h, a.adminCtx(), http.MethodPost, "/v1/invitations", `{"emails":["ok@x.test"],"role":"preparer","extra":{"x":1}}`))
		if len(items) != 1 || items[0]["email"] != "ok@x.test" {
			t.Errorf("items = %v, want one item for ok@x.test", items)
		}
	})
}

func TestInvitationsAPI_OutcomeWriteFailureStillReportsTheSend(t *testing.T) {
	for _, c := range []struct {
		name    string
		sendErr error
		want    string
	}{
		{"mail sent", nil, "sent"},
		{"mail failed", &accountmail.SendError{Status: 500}, "failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newAPIWorld(t, "Outcome Write API Tenant", "Ada Obi")
			pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
			if err != nil {
				t.Fatalf("connect app: %v", err)
			}
			t.Cleanup(pool.Close)
			a.store = NewStore(pool)
			emails := []string{"b@x.test", "c@x.test"}
			// Closing the pool after the commit makes every outcome write fail.
			s := &apiSender{fn: func(context.Context, []accountmail.Message) error {
				pool.Close()
				return c.sendErr
			}}

			items := apiItems(t, apiDo(a.handler(s), a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost(emails, "preparer")))

			if len(items) != 2 {
				t.Fatalf("items = %d, want 2", len(items))
			}
			for i, it := range items {
				if it["delivery"] != c.want {
					t.Errorf("item %d delivery = %v, want %s", i, it["delivery"], c.want)
				}
			}
			logs := a.logs.String()
			if n := strings.Count(logs, "tenancy: invite delivery not recorded"); n != 2 {
				t.Errorf("outcome-write log lines = %d, want 2 (one per invite):\n%s", n, logs)
			}
			for _, e := range emails {
				if strings.Contains(logs, e) {
					t.Errorf("log holds %q:\n%s", e, logs)
				}
				if row := pendingRow(t, a.super, a.tenant, e); row.SendStatus != "sending" {
					t.Errorf("row %s send_status = %q, want sending (the write failed)", e, row.SendStatus)
				}
			}
		})
	}
}

func TestInvitationsAPI_RenderFailureFailsOnlyThatInvite(t *testing.T) {
	_, sentry, _ := sentrytest.Boot(t, "tenancy")
	a := newAPIWorld(t, "Render Failure API Tenant", "Ada Obi")
	s := &apiSender{}
	log := slog.New(slog.NewTextHandler(a.logs, nil))
	inv := &Inviter{Store: a.store, Sender: s, Logger: log}
	issued := mustIssue(t, a.adminCtx(), a.store, []string{"b@x.test", "c@x.test"}, "preparer")
	// The roles table allows only the three rendered roles, so the store cannot hand deliver an unrenderable one.
	issued[0].Role = "qa-unknown-role"

	got := inv.deliver(a.adminCtx(), issued)

	if len(got) != 2 || got[0].Delivery != "failed" || got[1].Delivery != "sent" {
		t.Fatalf("results = %+v, want the first failed and the second sent", got)
	}
	msgs := s.messages()
	if calls := s.sent(); len(calls) != 1 || len(msgs) != 1 || msgs[0].To != "c@x.test" {
		t.Errorf("sender calls = %+v, want one call holding only the mail to c@x.test", calls)
	}
	for e, want := range map[string]string{"b@x.test": "failed", "c@x.test": "sent"} {
		if row := pendingRow(t, a.super, a.tenant, e); row.SendStatus != want {
			t.Errorf("row %s send_status = %q, want %s", e, row.SendStatus, want)
		}
	}
	logs := a.logs.String()
	if n := strings.Count(logs, "tenancy: invite mail failed"); n != 1 || !strings.Contains(logs, "count=1") || !strings.Contains(logs, "level=ERROR") {
		t.Errorf("want one ERROR line %q with count=1:\n%s", "tenancy: invite mail failed", logs)
	}
	if strings.Contains(logs, "b@x.test") {
		t.Errorf("log holds the address:\n%s", logs)
	}
	if events := sentry.Events(); len(events) != 1 {
		t.Errorf("recorded %d Sentry events, want exactly 1", len(events))
	}

	// Nothing renders: the sender is not called with an empty batch.
	allBad := mustIssue(t, a.adminCtx(), a.store, []string{"d@x.test", "e@x.test"}, "preparer")
	allBad[0].Role, allBad[1].Role = "qa-unknown-role", "qa-unknown-role"
	got = inv.deliver(a.adminCtx(), allBad)
	if len(got) != 2 || got[0].Delivery != "failed" || got[1].Delivery != "failed" {
		t.Errorf("all-bad results = %+v, want both failed", got)
	}
	if n := len(s.sent()); n != 1 {
		t.Errorf("sender calls after the all-bad batch = %d, want still 1", n)
	}
	if !strings.Contains(a.logs.String(), "count=2") {
		t.Errorf("no failure line with count=2 for the all-bad batch:\n%s", a.logs.String())
	}
}

func TestInvitationsAPI_ResponsesNeverCarryTheTokenOrItsHash(t *testing.T) {
	a := newAPIWorld(t, "No Token API Tenant", "Ada Obi")
	s := &apiSender{}
	h := a.handler(s)

	created := apiDo(h, a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost([]string{"b@x.test"}, "preparer"))
	items := apiItems(t, created)
	id := itemID(t, items[0])
	resent := apiDo(h, a.adminCtx(), http.MethodPost, resendTarget(id), "")
	listed := apiDo(h, a.adminCtx(), http.MethodGet, "/v1/invitations", "")

	msgs := s.messages()
	if len(msgs) != 2 {
		t.Fatalf("mails = %d, want 2", len(msgs))
	}
	if n := len(apiItems(t, listed)); n != 1 {
		t.Fatalf("listed items = %d, want 1", n)
	}
	var secrets []string
	for _, m := range msgs {
		tok := mailToken(t, m)
		secrets = append(secrets, tok, fmt.Sprintf("%x", sha(tok)), fmt.Sprintf("%X", sha(tok)), `\\x`+fmt.Sprintf("%x", sha(tok)))
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{"create": created, "resend": resent, "list": listed} {
		body := rec.Body.String()
		if body == "" {
			t.Errorf("%s body is empty", name)
		}
		for _, secret := range secrets {
			if strings.Contains(body, secret) {
				t.Errorf("%s body holds %q: %s", name, secret, body)
			}
		}
		for _, key := range []string{"token", "token_hash", "send_status"} {
			if strings.Contains(body, `"`+key+`"`) {
				t.Errorf("%s body holds the key %q: %s", name, key, body)
			}
		}
	}
}

func TestInvitationsAPI_RateLimitedRequestWritesAndSendsNothing(t *testing.T) {
	a := newAPIWorld(t, "Rate Limit API Tenant", "Ada Obi")
	seedAuditEvents(t, a.super, a.tenant, "invitation.sent", sentPayload, "1 hour", 20)
	existing := seedInvitation(t, a.super, seedInv{tenant: a.tenant, email: "s@x.test", invitedBy: a.admin, sendStatus: "sent"})
	s := &apiSender{}
	h := a.handler(s)
	const limitMsg = "daily invite limit reached: 20 invite mails per workspace per 24 hours"

	assertErrorBody(t, apiDo(h, a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost([]string{"b@x.test"}, "preparer")), http.StatusTooManyRequests, limitMsg)
	assertErrorBody(t, apiDo(h, a.adminCtx(), http.MethodPost, resendTarget(existing.ID), ""), http.StatusTooManyRequests, limitMsg)

	if n := len(s.sent()); n != 0 {
		t.Errorf("sender calls = %d, want 0", n)
	}
	if n := countInvitations(t, a.super, a.tenant); n != 1 {
		t.Errorf("invitations rows = %d, want only the seeded one", n)
	}
	if n := inviteAudit(t, a.super, a.tenant); n != 20 {
		t.Errorf("invitation audit rows = %d, want the 20 seeded (no new one)", n)
	}
	row := pendingRow(t, a.super, a.tenant, "s@x.test")
	if !bytes.Equal(row.Hash, existing.Hash) || row.SendStatus != "sent" {
		t.Errorf("the refused resend changed the row: hash equal %v, send_status %q", bytes.Equal(row.Hash, existing.Hash), row.SendStatus)
	}
}

func TestInvitationsAPI_AnotherTenantsInviteIsNotFoundAndNotListed(t *testing.T) {
	a := newAPIWorld(t, "Cross Tenant API A", "Ada Obi")
	b := newInvWorld(t, "Cross Tenant API B", "Bo Okafor")
	theirs := seedInvitation(t, b.super, seedInv{tenant: b.tenant, email: "theirs@x.test", invitedBy: b.admin, sendStatus: "sent"})
	s := &apiSender{}
	h := a.handler(s)

	assertErrorBody(t, apiDo(h, a.adminCtx(), http.MethodPost, resendTarget(theirs.ID), ""), http.StatusNotFound, "invitation not found")

	if n := len(s.sent()); n != 0 {
		t.Errorf("sender calls = %d, want 0", n)
	}
	row := pendingRow(t, b.super, b.tenant, "theirs@x.test")
	if !bytes.Equal(row.Hash, theirs.Hash) || row.SendStatus != "sent" {
		t.Errorf("the other tenant's row changed: hash equal %v, send_status %q", bytes.Equal(row.Hash, theirs.Hash), row.SendStatus)
	}
	if n := inviteAudit(t, b.super, b.tenant); n != 0 {
		t.Errorf("the other tenant gained %d audit rows", n)
	}
	rec := apiDo(h, a.adminCtx(), http.MethodGet, "/v1/invitations", "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"invitations":[]}` {
		t.Errorf("list = %d %s, want 200 with no items: the other tenant's invite must not show", rec.Code, rec.Body)
	}
	// Control: the other tenant's own admin resends the same id.
	apiItem(t, apiDo(b.handlerFor(s), b.adminCtx(), http.MethodPost, resendTarget(theirs.ID), ""))
	if n := len(s.sent()); n != 1 {
		t.Errorf("sender calls after the owner's resend = %d, want 1", n)
	}
}

func (w invWorld) handlerFor(s accountmail.Sender) http.Handler {
	return apiWorld{invWorld: w, logs: &syncBuf{}}.handler(s)
}

func TestInvitationsAPI_StoreFailureIsAnOpaque500(t *testing.T) {
	var logs syncBuf
	log := slog.New(slog.NewTextHandler(&logs, nil))
	boom := errors.New("pq: connection to 10.0.0.9 refused for b@x.test")
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/invitations", InvitationsCreateHandler(func(context.Context, []string, string) ([]InviteResult, error) { return nil, boom }, log))
	mux.HandleFunc("GET /v1/invitations", InvitationsListHandler(func(context.Context) ([]Invitation, error) { return nil, boom }, log))
	mux.HandleFunc("POST /v1/invitations/{id}/resend", InvitationResendHandler(func(context.Context, string) (InviteResult, error) { return InviteResult{}, boom }, log))

	for _, c := range []struct{ name, method, target, body string }{
		{"create", http.MethodPost, "/v1/invitations", invitePost([]string{"b@x.test"}, "preparer")},
		{"list", http.MethodGet, "/v1/invitations", ""},
		{"resend", http.MethodPost, resendTarget(uuid.NewString()), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertErrorBody(t, apiDo(mux, ctx, c.method, c.target, c.body), http.StatusInternalServerError, "internal server error")
		})
	}
	if n := strings.Count(logs.String(), "level=ERROR"); n != 3 {
		t.Errorf("ERROR log lines = %d, want 3 (one per failed request):\n%s", n, logs.String())
	}
}

func TestInvitationsAPI_NoSecretReachesLogsOrSentryAcrossFailureAndResend(t *testing.T) {
	_, sentry, _ := sentrytest.Boot(t, "tenancy")
	a := newAPIWorld(t, "No Secret API Tenant", "Ada Obi")
	const key = "k_secret_sending_key"
	// The real sender over a transport that fails like a dropped connection: its *url.Error would carry the URL.
	down := apiRoundTrip(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial tcp 10.1.2.3:443 for %s with %s: refused", r.URL, r.Header.Get("Authorization"))
	})
	real := accountmail.NewResend(accountmail.ResendBaseURL, key, down)
	rec := &apiSender{}
	tee := &apiSender{fn: func(ctx context.Context, msgs []accountmail.Message) error {
		_ = rec.Send(ctx, msgs)
		return real.Send(ctx, msgs)
	}}
	h := a.handler(tee)

	items := apiItems(t, apiDo(h, a.adminCtx(), http.MethodPost, "/v1/invitations", invitePost([]string{"b@x.test", "c@x.test"}, "preparer")))
	if len(items) != 2 || items[0]["delivery"] != "failed" || items[1]["delivery"] != "failed" {
		t.Fatalf("items = %v, want two failed items", items)
	}
	for _, it := range items {
		apiItem(t, apiDo(h, a.adminCtx(), http.MethodPost, resendTarget(itemID(t, it)), ""))
	}

	msgs := rec.messages()
	if len(msgs) != 4 {
		t.Fatalf("recorded mails = %d, want 4 (batch of 2, then 2 resends)", len(msgs))
	}
	secrets := []string{key, "b@x.test", "c@x.test", accountmail.InviteAcceptURL, accountmail.ResendBaseURL, "10.1.2.3"}
	for _, m := range msgs {
		secrets = append(secrets, mailToken(t, m))
	}
	logs := a.logs.String()
	if n := strings.Count(logs, "tenancy: invite mail failed"); n != 3 || strings.Count(logs, "count=2") != 1 || strings.Count(logs, "count=1") != 2 {
		t.Errorf("want 3 failure lines, counts 2 (the batch) then 1 and 1 (the resends):\n%s", logs)
	}
	events := sentry.Events()
	if len(events) != 3 {
		t.Fatalf("recorded %d Sentry events, want 3", len(events))
	}
	for _, secret := range secrets {
		if strings.Contains(logs, secret) {
			t.Errorf("log holds %q:\n%s", secret, logs)
		}
		for i, ev := range events {
			raw, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("marshal event %d: %v", i, err)
			}
			if strings.Contains(string(raw), secret) {
				t.Errorf("Sentry event %d holds %q: %s", i, secret, raw)
			}
		}
	}
}
