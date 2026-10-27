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

type linkTokenSet struct {
	tenant, user uuid.UUID
	token        string
}

type linkTokenRecorder struct {
	got   []linkTokenSet
	found bool
	err   error
}

func (r *linkTokenRecorder) set(_ context.Context, tenantID, userID uuid.UUID, token string) (bool, error) {
	r.got = append(r.got, linkTokenSet{tenantID, userID, token})
	return r.found, r.err
}

func mockLinkBody(tenant, user, token string) string {
	return `{"tenant_id":"` + tenant + `","user_id":"` + user + `","token":"` + token + `"}`
}

func postMockLink(h http.Handler, method, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, "/auth/mock/deletion-link-token", strings.NewReader(body)))
	return rec
}

func TestMockDeletionLinkToken_Contract(t *testing.T) {
	tenant, user := uuid.New(), uuid.New()
	hex32 := func(id uuid.UUID) string { return strings.ReplaceAll(id.String(), "-", "") }
	valid := mockLinkBody(tenant.String(), user.String(), mockInviteToken)

	t.Run("replaced: 204 with the parsed values", func(t *testing.T) {
		log, _ := captureLog()
		rec := &linkTokenRecorder{found: true}
		resp := postMockLink(MockDeletionLinkTokenHandler(rec.set, log), http.MethodPost, valid)
		if resp.Code != http.StatusNoContent || resp.Body.Len() != 0 {
			t.Fatalf("status = %d body %q, want 204 empty", resp.Code, resp.Body.String())
		}
		if want := (linkTokenSet{tenant, user, mockInviteToken}); len(rec.got) != 1 || rec.got[0] != want {
			t.Errorf("setter calls = %+v, want exactly [%+v]", rec.got, want)
		}
	})

	t.Run("no live link: 404 deletion link not found", func(t *testing.T) {
		log, _ := captureLog()
		rec := &linkTokenRecorder{found: false}
		resp := postMockLink(MockDeletionLinkTokenHandler(rec.set, log), http.MethodPost, valid)
		if resp.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %s", resp.Code, resp.Body.String())
		}
		requireStringMap(t, resp, map[string]string{"error": "deletion link not found"})
	})

	t.Run("setter error: 502 with no token in the answer or the log", func(t *testing.T) {
		log, buf := captureLog()
		rec := &linkTokenRecorder{err: errors.New("db: set deletion link token: connect")}
		resp := postMockLink(MockDeletionLinkTokenHandler(rec.set, log), http.MethodPost, valid)
		if resp.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502: %s", resp.Code, resp.Body.String())
		}
		requireStringMap(t, resp, map[string]string{"error": "deletion link token unavailable"})
		if buf.Len() == 0 || strings.Contains(buf.String(), mockInviteToken) {
			t.Errorf("log = %q, want a line without the token", buf.String())
		}
	})

	bad := []struct{ name, body string }{
		{"malformed JSON", `{`},
		{"tenant_id 32-hex", mockLinkBody(hex32(tenant), user.String(), mockInviteToken)},
		{"user_id 32-hex", mockLinkBody(tenant.String(), hex32(user), mockInviteToken)},
		{"user_id missing", `{"tenant_id":"` + tenant.String() + `","token":"` + mockInviteToken + `"}`},
		{"token of 42 characters", mockLinkBody(tenant.String(), user.String(), mockInviteToken[:42])},
		{"token of 44 characters", mockLinkBody(tenant.String(), user.String(), mockInviteToken+"A")},
	}
	for _, c := range bad {
		t.Run("400 "+c.name, func(t *testing.T) {
			log, _ := captureLog()
			rec := &linkTokenRecorder{found: true}
			resp := postMockLink(MockDeletionLinkTokenHandler(rec.set, log), http.MethodPost, c.body)
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
		rec := &linkTokenRecorder{found: true}
		resp := postMockLink(MockDeletionLinkTokenHandler(rec.set, log), http.MethodGet, "")
		if resp.Code != http.StatusMethodNotAllowed || resp.Header().Get("Allow") != "POST" || len(rec.got) != 0 {
			t.Errorf("GET = %d Allow %q setter calls %d, want 405 POST 0", resp.Code, resp.Header().Get("Allow"), len(rec.got))
		}
	})
}
