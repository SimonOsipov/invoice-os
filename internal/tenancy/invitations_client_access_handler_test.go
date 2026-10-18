//go:build firms0203

// Needs the new InviteFunc and Inviter.Invite signatures; the executor deletes this tag line and this comment.

package tenancy

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	invitedAccessMsg = "client_access applies to a preparer or reviewer invite"
	unknownClientMsg = "a client in the list is not in this workspace"
	notFirmMsg       = "client access applies to accounting firm workspaces only"
)

type inviteCall struct {
	emails []string
	role   string
	access *ClientAccess
}

type inviteRig struct {
	calls []inviteCall
	err   error
}

func (r *inviteRig) invite(_ context.Context, emails []string, role string, access *ClientAccess) ([]InviteResult, error) {
	r.calls = append(r.calls, inviteCall{emails, role, access})
	return []InviteResult{{ID: uuid.NewString(), Email: emails[0], Role: role, Status: "pending"}}, r.err
}

func (r *inviteRig) post(body string) *httptest.ResponseRecorder {
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()})
	rec := httptest.NewRecorder()
	InvitationsCreateHandler(r.invite, slog.New(slog.DiscardHandler)).
		ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/invitations", strings.NewReader(body)))
	return rec
}

func TestInvitationsCreateHandler_ClientAccessGrammar(t *testing.T) {
	e1, e2 := uuid.NewString(), uuid.NewString()
	const head = `{"emails":["a@x.test"],"role":"preparer",`

	t.Run("an admin invite with any client_access is refused before the store", func(t *testing.T) {
		for _, v := range []string{`"all"`, `[]`, `["` + e1 + `"]`} {
			var r inviteRig
			assertErrorBody(t, r.post(`{"emails":["a@x.test"],"role":"admin","client_access":`+v+`}`), http.StatusBadRequest, invitedAccessMsg)
			if len(r.calls) != 0 {
				t.Errorf("admin + %s reached the store %d times, want 0", v, len(r.calls))
			}
		}
	})
	t.Run("an admin invite without a choice, or with null, reaches the store with nil", func(t *testing.T) {
		for _, body := range []string{`{"emails":["a@x.test"],"role":"admin"}`, `{"emails":["a@x.test"],"role":"admin","client_access":null}`} {
			var r inviteRig
			if rec := r.post(body); rec.Code != http.StatusOK || len(r.calls) != 1 || r.calls[0].access != nil || r.calls[0].role != "admin" {
				t.Errorf("%s: status %d, calls %+v, want 200 and one call with nil access", body, rec.Code, r.calls)
			}
		}
	})
	for name, v := range map[string]string{
		"some":              `"some"`,
		"upper-case ALL":    `"ALL"`,
		"a number":          `1`,
		"true":              `true`,
		"an object":         `{}`,
		"a non-uuid id":     `["x"]`,
		"a number in list":  `[1]`,
		"a null in list":    `[null]`,
		"a duplicate":       `["` + e1 + `","` + e1 + `"]`,
		"a duplicate cased": `["` + e1 + `","` + strings.ToUpper(e1) + `"]`,
	} {
		t.Run("grammar: "+name, func(t *testing.T) {
			var r inviteRig
			assertErrorBody(t, r.post(head+`"client_access":`+v+`}`), http.StatusBadRequest, grammarMsg)
			if len(r.calls) != 0 {
				t.Errorf("reached the store %d times, want 0", len(r.calls))
			}
		})
	}
	t.Run("a broken body keeps the generic message", func(t *testing.T) {
		var r inviteRig
		assertErrorBody(t, r.post(head+`"client_access":`), http.StatusBadRequest, "invalid request body")
	})
	t.Run("a valid list reaches the store canonical", func(t *testing.T) {
		var r inviteRig
		if rec := r.post(head + `"client_access":["` + strings.ToUpper(e1) + `","` + e2 + `"]}`); rec.Code != http.StatusOK || len(r.calls) != 1 {
			t.Fatalf("status %d with %d calls, want 200 and 1", rec.Code, len(r.calls))
		}
		a := r.calls[0].access
		got, want := slices.Clone(a.IDs), []string{e1, e2}
		slices.Sort(got)
		slices.Sort(want)
		if a == nil || a.All || !slices.Equal(got, want) {
			t.Errorf("access = %+v, want ids %v", a, want)
		}
	})
	t.Run("all, an empty list and an absent or null key", func(t *testing.T) {
		var r inviteRig
		for _, v := range []string{`"client_access":"all"`, `"client_access":[]`, `"client_access":null`, `"x":1`} {
			if rec := r.post(head + v + `}`); rec.Code != http.StatusOK {
				t.Fatalf("%s: status %d: %s", v, rec.Code, rec.Body)
			}
		}
		if len(r.calls) != 4 {
			t.Fatalf("store calls = %d, want 4", len(r.calls))
		}
		if a := r.calls[0].access; a == nil || !a.All || len(a.IDs) != 0 {
			t.Errorf("all: access = %+v, want All", a)
		}
		if a := r.calls[1].access; a == nil || a.All || len(a.IDs) != 0 {
			t.Errorf("empty list: access = %+v, want a non-nil empty set", a)
		}
		if r.calls[2].access != nil || r.calls[3].access != nil {
			t.Errorf("null and absent: access = %+v / %+v, want nil", r.calls[2].access, r.calls[3].access)
		}
	})
	t.Run("the store's refusals keep their status", func(t *testing.T) {
		for _, c := range []struct {
			err  error
			code int
			msg  string
		}{
			{ErrUnknownClient, 400, unknownClientMsg},
			{ErrClientAccessNotFirm, 409, notFirmMsg},
		} {
			r := inviteRig{err: c.err}
			assertErrorBody(t, r.post(head+`"client_access":"all"}`), c.code, c.msg)
		}
	})
}

func (a apiWorld) postJSON(h http.Handler, body string) *httptest.ResponseRecorder {
	return apiDo(h, a.adminCtx(), http.MethodPost, "/v1/invitations", body)
}

func TestInvitationsAPI_ClientAccessIsStoredOnTheInvites(t *testing.T) {
	a := newAPIWorld(t, "API Set Tenant", "Ada Obi")
	b := newAPIWorld(t, "API Set Other", "Bea Eze")
	e1, e2 := seedEntity(t, a.super, a.tenant), seedEntity(t, a.super, a.tenant)
	x := seedEntity(t, b.super, b.tenant)
	sender := &apiSender{}
	h := a.handler(sender)
	post := func(body string) *httptest.ResponseRecorder { return a.postJSON(h, body) }

	items := apiItems(t, post(`{"emails":["a@x.test","b@x.test"],"role":"preparer","client_access":["`+e2+`","`+e1+`"]}`))
	if len(items) != 2 {
		t.Fatalf("items = %v, want 2", items)
	}
	for _, it := range items {
		a.requireInvite(t, "set "+it["email"].(string), itemID(t, it), false, e1, e2)
	}
	items = apiItems(t, post(`{"emails":["c@x.test"],"role":"reviewer","client_access":"all"}`))
	a.requireInvite(t, "all", itemID(t, items[0]), true)
	items = apiItems(t, post(`{"emails":["d@x.test"],"role":"reviewer","client_access":null}`))
	a.requireInvite(t, "null", itemID(t, items[0]), false)

	// A reissue with no key replaces the first batch's set.
	items = apiItems(t, post(`{"emails":["a@x.test"],"role":"preparer"}`))
	a.requireInvite(t, "reissue with no key", itemID(t, items[0]), false)

	mails := len(sender.messages())
	sent := a.auditCountOf(t, "invitation.sent")
	pending := countInvitations(t, a.super, a.tenant)
	rows := countRows(t, a.invWorld, "invitation_clients")
	for _, c := range map[string]struct {
		body string
		code int
		msg  string
	}{
		"admin role":      {`{"emails":["z@x.test"],"role":"admin","client_access":"all"}`, 400, invitedAccessMsg},
		"foreign client":  {`{"emails":["z@x.test"],"role":"preparer","client_access":["` + x + `"]}`, 400, unknownClientMsg},
		"unknown client":  {`{"emails":["z@x.test"],"role":"preparer","client_access":["` + uuid.NewString() + `"]}`, 400, unknownClientMsg},
		"grammar":         {`{"emails":["z@x.test"],"role":"preparer","client_access":"some"}`, 400, grammarMsg},
		"foreign among 2": {`{"emails":["y@x.test","z@x.test"],"role":"reviewer","client_access":["` + e1 + `","` + x + `"]}`, 400, unknownClientMsg},
	} {
		assertErrorBody(t, post(c.body), c.code, c.msg)
	}
	if got := len(sender.messages()); got != mails {
		t.Errorf("mails sent = %d, want %d unchanged by the refusals", got, mails)
	}
	if got := a.auditCountOf(t, "invitation.sent"); got != sent {
		t.Errorf("invitation.sent rows = %d, want %d unchanged", got, sent)
	}
	if got := countInvitations(t, a.super, a.tenant); got != pending {
		t.Errorf("invites = %d, want %d unchanged", got, pending)
	}
	if got := countRows(t, a.invWorld, "invitation_clients"); got != rows {
		t.Errorf("invitation_clients rows = %d, want %d unchanged", got, rows)
	}
}

func TestInvitationsAPI_ClientAccessInHouseAnswers409(t *testing.T) {
	w := inHouseWorld(t)
	h := w.handlerFor(&apiSender{})
	for _, v := range []string{`"all"`, `[]`} {
		assertErrorBody(t, apiDo(h, w.adminCtx(), http.MethodPost, "/v1/invitations",
			`{"emails":["a@x.test"],"role":"preparer","client_access":`+v+`}`), http.StatusConflict, notFirmMsg)
	}
	if n := countInvitations(t, w.super, w.tenant); n != 0 {
		t.Errorf("invites after the refusals = %d, want 0", n)
	}
	// Control: the same invite without the key goes through.
	if rec := apiDo(h, w.adminCtx(), http.MethodPost, "/v1/invitations", `{"emails":["a@x.test"],"role":"preparer"}`); rec.Code != http.StatusOK {
		t.Errorf("in-house invite without a key: status %d: %s", rec.Code, rec.Body)
	}
}
