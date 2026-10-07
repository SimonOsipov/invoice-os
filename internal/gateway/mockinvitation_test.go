package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// 43 base64url characters, '-' and '_' included: the token shape tenancy mints.
const mockInviteToken = "abcdefghijklmnopqrstuvwxyz-_ABCDEFGHIJKLMNO"

type inviteTokenSet struct {
	tenant, invitation uuid.UUID
	token              string
}

type inviteTokenRecorder struct {
	got   []inviteTokenSet
	found bool
	err   error
}

func (r *inviteTokenRecorder) set(_ context.Context, tenantID, invitationID uuid.UUID, token string) (bool, error) {
	r.got = append(r.got, inviteTokenSet{tenantID, invitationID, token})
	return r.found, r.err
}

func mockInviteBody(tenant, invitation, token string) string {
	return `{"tenant_id":"` + tenant + `","invitation_id":"` + invitation + `","token":"` + token + `"}`
}

func postMockInvite(h http.Handler, method, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, "/auth/mock/invitation-token", strings.NewReader(body)))
	return rec
}

func TestMockInvitationToken_Contract(t *testing.T) {
	tenant, invitation := uuid.New(), uuid.New()
	hex32 := func(id uuid.UUID) string { return strings.ReplaceAll(id.String(), "-", "") }
	valid := mockInviteBody(tenant.String(), invitation.String(), mockInviteToken)
	if len(mockInviteToken) != 43 {
		t.Fatalf("fixture token is %d characters, want 43", len(mockInviteToken))
	}

	t.Run("replaced: 204 with the parsed values", func(t *testing.T) {
		log, _ := captureLog()
		rec := &inviteTokenRecorder{found: true}
		resp := postMockInvite(MockInvitationTokenHandler(rec.set, log), http.MethodPost, valid)
		if resp.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204: %s", resp.Code, resp.Body.String())
		}
		if resp.Body.Len() != 0 {
			t.Errorf("204 body = %q, want empty", resp.Body.String())
		}
		if got := resp.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
		if want := (inviteTokenSet{tenant, invitation, mockInviteToken}); len(rec.got) != 1 || rec.got[0] != want {
			t.Errorf("setter calls = %+v, want exactly [%+v]", rec.got, want)
		}
	})

	t.Run("uppercase hyphenated ids are accepted", func(t *testing.T) {
		log, _ := captureLog()
		rec := &inviteTokenRecorder{found: true}
		body := mockInviteBody(strings.ToUpper(tenant.String()), strings.ToUpper(invitation.String()), mockInviteToken)
		if resp := postMockInvite(MockInvitationTokenHandler(rec.set, log), http.MethodPost, body); resp.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204: %s", resp.Code, resp.Body.String())
		}
		if want := (inviteTokenSet{tenant, invitation, mockInviteToken}); len(rec.got) != 1 || rec.got[0] != want {
			t.Errorf("setter calls = %+v, want exactly [%+v]", rec.got, want)
		}
	})

	t.Run("no pending row: 404 invitation not found", func(t *testing.T) {
		log, _ := captureLog()
		rec := &inviteTokenRecorder{found: false}
		resp := postMockInvite(MockInvitationTokenHandler(rec.set, log), http.MethodPost, valid)
		if resp.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %s", resp.Code, resp.Body.String())
		}
		requireStringMap(t, resp, map[string]string{"error": "invitation not found"})
		if len(rec.got) != 1 {
			t.Errorf("setter calls = %d, want 1", len(rec.got))
		}
	})

	t.Run("setter error: 502 with no token in the answer or the log", func(t *testing.T) {
		log, buf := captureLog()
		rec := &inviteTokenRecorder{err: errors.New("db: set invitation token: connect")}
		resp := postMockInvite(MockInvitationTokenHandler(rec.set, log), http.MethodPost, valid)
		if resp.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502: %s", resp.Code, resp.Body.String())
		}
		if got := errorBody(t, resp); got == "" || strings.Contains(got, mockInviteToken) {
			t.Errorf("error = %q, want a message without the token", got)
		}
		if buf.Len() == 0 {
			t.Error("no log line on a setter error: the token check below proves nothing")
		}
		if strings.Contains(buf.String(), mockInviteToken) {
			t.Errorf("a log line carries the token: %s", buf.String())
		}
	})

	bad := []struct{ name, body string }{
		{"malformed JSON", `{`},
		{"empty body", ``},
		{"tenant_id 32-hex", mockInviteBody(hex32(tenant), invitation.String(), mockInviteToken)},
		{"invitation_id 32-hex", mockInviteBody(tenant.String(), hex32(invitation), mockInviteToken)},
		{"invitation_id braced", mockInviteBody(tenant.String(), "{"+invitation.String()+"}", mockInviteToken)},
		{"tenant_id missing", `{"invitation_id":"` + invitation.String() + `","token":"` + mockInviteToken + `"}`},
		{"token missing", `{"tenant_id":"` + tenant.String() + `","invitation_id":"` + invitation.String() + `"}`},
		{"token of 42 characters", mockInviteBody(tenant.String(), invitation.String(), mockInviteToken[:42])},
		{"token of 44 characters", mockInviteBody(tenant.String(), invitation.String(), mockInviteToken+"A")},
		{"token with a standard-base64 plus", mockInviteBody(tenant.String(), invitation.String(), mockInviteToken[:42]+"+")},
		{"token with padding", mockInviteBody(tenant.String(), invitation.String(), mockInviteToken[:42]+"=")},
		{"token with a trailing newline", mockInviteBody(tenant.String(), invitation.String(), mockInviteToken[:42]+`\n`)},
	}
	for _, c := range bad {
		t.Run("400 "+c.name, func(t *testing.T) {
			log, _ := captureLog()
			rec := &inviteTokenRecorder{found: true}
			resp := postMockInvite(MockInvitationTokenHandler(rec.set, log), http.MethodPost, c.body)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.Code, resp.Body.String())
			}
			requireStringMap(t, resp, map[string]string{"error": msgInvalidBody})
			if len(rec.got) != 0 {
				t.Errorf("setter ran for a refused body: %+v", rec.got)
			}
		})
	}

	t.Run("GET is 405 Allow POST", func(t *testing.T) {
		log, _ := captureLog()
		rec := &inviteTokenRecorder{found: true}
		resp := postMockInvite(MockInvitationTokenHandler(rec.set, log), http.MethodGet, "")
		if resp.Code != http.StatusMethodNotAllowed || resp.Header().Get("Allow") != "POST" {
			t.Errorf("GET = %d Allow %q, want 405 POST", resp.Code, resp.Header().Get("Allow"))
		}
		if len(rec.got) != 0 {
			t.Errorf("setter ran on a GET: %+v", rec.got)
		}
	})
}
