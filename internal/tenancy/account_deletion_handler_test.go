package tenancy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	msgOutcomeChanged = "the workspace changed; review the page and confirm again"
	msgNoEraseEmail   = "your account has no email address on record; sign in again and retry"
	meDeletionPath    = "/v1/me/deletion"
)

// deleteStub records every call of the stubbed store.
type deleteStub struct {
	calls   int
	outcome AccountDeletionOutcome
	subject string
	err     error
}

func (s *deleteStub) fn() DeleteMyAccountFunc {
	return func(ctx context.Context, want AccountDeletionOutcome) error {
		s.calls++
		s.outcome = want
		if id, ok := auth.IdentityFromContext(ctx); ok {
			s.subject = id.Subject
		}
		return s.err
	}
}

func TestDeleteMyAccountHandler_MapsEveryOutcome(t *testing.T) {
	// a body of exactly n bytes: a valid outcome plus an ignored padding key
	bodyOf := func(n int) string {
		const frame = `{"outcome":"leave","pad":""}`
		return `{"outcome":"leave","pad":"` + strings.Repeat("a", n-len(frame)) + `"}`
	}

	t.Run("a success echoes the outcome", func(t *testing.T) {
		for _, o := range []AccountDeletionOutcome{OutcomeLeave, OutcomeHandOn} {
			stub := &deleteStub{}
			status, body := deletionCall(DeleteMyAccountHandler(stub.fn(), nil), deletionCaller, meDeletionPath, `{"outcome":"`+string(o)+`"}`)
			if status != http.StatusOK || string(body["outcome"]) != `"`+string(o)+`"` || len(body) != 1 {
				t.Errorf("%s: status = %d, body = %v, want 200 and exactly {outcome:%s}", o, status, body, o)
			}
			if stub.calls != 1 || stub.outcome != o {
				t.Errorf("%s: store calls = %d outcome = %q, want one call with %s", o, stub.calls, stub.outcome, o)
			}
		}
	})

	t.Run("a body the handler cannot use is refused before the store", func(t *testing.T) {
		for _, tc := range []struct{ name, body string }{
			{"bad JSON", `{"outcome":`},
			{"an empty body", ``},
			{"no outcome", `{}`},
			{"an unknown outcome", `{"outcome":"other"}`},
			{"a wrong-case outcome", `{"outcome":"Leave"}`},
			{"a non-string outcome", `{"outcome":1}`},
			{"over 4 KiB", bodyOf(4097)},
		} {
			stub := &deleteStub{}
			status, body := deletionCall(DeleteMyAccountHandler(stub.fn(), nil), deletionCaller, meDeletionPath, tc.body)
			if status != http.StatusBadRequest || errorSentence(body) != "invalid request body" {
				t.Errorf("%s: status = %d %v, want 400 invalid request body", tc.name, status, body)
			}
			if stub.calls != 0 {
				t.Errorf("%s: the store was called %d times, want 0", tc.name, stub.calls)
			}
		}
	})

	t.Run("a body of exactly 4 KiB is read", func(t *testing.T) {
		stub := &deleteStub{}
		status, _ := deletionCall(DeleteMyAccountHandler(stub.fn(), nil), deletionCaller, meDeletionPath, bodyOf(4096))
		if status != http.StatusOK || stub.calls != 1 {
			t.Errorf("status = %d, store calls = %d, want 200 and 1", status, stub.calls)
		}
	})

	t.Run("each store refusal answers its status and sentence", func(t *testing.T) {
		rows := append([]struct {
			name   string
			err    error
			status int
			msg    string
		}{
			{"the outcome changed", ErrDeletionOutcomeChanged, http.StatusConflict, msgOutcomeChanged},
			{"no email on record", ErrNoEraseEmail, http.StatusConflict, msgNoEraseEmail},
		}, deletionRefusals[1:]...) // the no-tenant row has its own subtest: it must not reach the store
		for _, tc := range rows {
			stub := &deleteStub{err: tc.err}
			status, body := deletionCall(DeleteMyAccountHandler(stub.fn(), nil), deletionCaller, meDeletionPath, `{"outcome":"leave"}`)
			if status != tc.status || errorSentence(body) != tc.msg {
				t.Errorf("%s: status = %d %q, want %d %q", tc.name, status, errorSentence(body), tc.status, tc.msg)
			}
			if stub.calls != 1 {
				t.Errorf("%s: store calls = %d, want 1", tc.name, stub.calls)
			}
		}
	})

	t.Run("no identity answers 401 and never reaches the store", func(t *testing.T) {
		stub := &deleteStub{}
		status, body := deletionCall(DeleteMyAccountHandler(stub.fn(), nil), nil, meDeletionPath, `{"outcome":"leave"}`)
		if status != http.StatusUnauthorized || errorSentence(body) != "unauthorized" || stub.calls != 0 {
			t.Errorf("status = %d %q, store calls = %d, want 401 unauthorized and 0", status, errorSentence(body), stub.calls)
		}
	})
}

func TestDeleteMyAccountHandler_RefusesDeleteWorkspace(t *testing.T) {
	stub := &deleteStub{}
	status, body := deletionCall(DeleteMyAccountHandler(stub.fn(), nil), deletionCaller, meDeletionPath,
		`{"outcome":"delete_workspace","workspace_name":"X"}`)
	if status != http.StatusBadRequest || errorSentence(body) != "invalid request body" {
		t.Errorf("status = %d %v, want 400 invalid request body", status, body)
	}
	if stub.calls != 0 {
		t.Errorf("the store was called %d times, want 0 (the schedule goes through the DEL-04 route)", stub.calls)
	}
	// positive pair: the handler is wired and reads a leave from the same caller
	ok := &deleteStub{}
	if status, _ := deletionCall(DeleteMyAccountHandler(ok.fn(), nil), deletionCaller, meDeletionPath, `{"outcome":"leave"}`); status != http.StatusOK || ok.calls != 1 {
		t.Errorf("control: leave status = %d, calls = %d, want 200 and 1", status, ok.calls)
	}
}

func TestDeleteMyAccountHandler_IgnoresAUserIDInTheBody(t *testing.T) {
	stub := &deleteStub{}
	other := "6f0f6f0f-0000-4000-8000-0000000000ff"
	status, _ := deletionCall(DeleteMyAccountHandler(stub.fn(), nil), deletionCaller, meDeletionPath,
		`{"outcome":"leave","user_id":"`+other+`"}`)
	if status != http.StatusOK || stub.calls != 1 || stub.outcome != OutcomeLeave {
		t.Fatalf("status = %d, calls = %d, outcome = %q, want 200, one call, leave", status, stub.calls, stub.outcome)
	}
	if stub.subject != deletionCaller.Subject {
		t.Errorf("store ran as %q, want the verified caller %q (never the body's user_id)", stub.subject, deletionCaller.Subject)
	}
}

func TestDeleteMyAccountHandler_NeedsNoTypedPhrase(t *testing.T) {
	stub := &deleteStub{}
	status, body := deletionCall(DeleteMyAccountHandler(stub.fn(), nil), deletionCaller, meDeletionPath, `{"outcome":"leave"}`)
	if status != http.StatusOK || stub.calls != 1 || stub.outcome != OutcomeLeave {
		t.Errorf("status = %d %v, calls = %d outcome = %q, want 200 and one leave", status, body, stub.calls, stub.outcome)
	}
}

func previewCall(h http.HandlerFunc, id *auth.Identity) (int, string) {
	r := httptest.NewRequest(http.MethodGet, meDeletionPath, nil)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

func TestAccountDeletionPreviewHandler_Shape(t *testing.T) {
	name := "Chidi Eze"
	serve := func(p AccountDeletionPreview, err error) http.HandlerFunc {
		return AccountDeletionPreviewHandler(func(context.Context) (AccountDeletionPreview, error) { return p, err }, nil)
	}

	t.Run("hand_on names the heir", func(t *testing.T) {
		status, body := previewCall(serve(AccountDeletionPreview{Outcome: OutcomeHandOn, HeirName: &name}, nil), deletionCaller)
		if status != http.StatusOK || body != `{"outcome":"hand_on","heir_name":"Chidi Eze"}` {
			t.Errorf("status = %d, body = %s, want 200 and the heir named", status, body)
		}
	})

	t.Run("no heir keeps the key as null", func(t *testing.T) {
		for _, o := range []AccountDeletionOutcome{OutcomeLeave, OutcomeDeleteWorkspace} {
			status, body := previewCall(serve(AccountDeletionPreview{Outcome: o}, nil), deletionCaller)
			var got map[string]json.RawMessage
			_ = json.Unmarshal([]byte(body), &got)
			if status != http.StatusOK || string(got["outcome"]) != `"`+string(o)+`"` || string(got["heir_name"]) != "null" || len(got) != 2 {
				t.Errorf("%s: status = %d, body = %s, want 200 with outcome and heir_name:null", o, status, body)
			}
		}
	})

	t.Run("no identity answers 401", func(t *testing.T) {
		called := false
		h := AccountDeletionPreviewHandler(func(context.Context) (AccountDeletionPreview, error) {
			called = true
			return AccountDeletionPreview{}, nil
		}, nil)
		status, body := previewCall(h, nil)
		if status != http.StatusUnauthorized || !strings.Contains(body, `"unauthorized"`) || called {
			t.Errorf("status = %d, body = %s, called = %v, want 401 unauthorized and no store call", status, body, called)
		}
	})

	t.Run("a refused caller answers the seam sentence", func(t *testing.T) {
		status, body := previewCall(serve(AccountDeletionPreview{}, db.ErrNotActiveMember), deletionCaller)
		if status != http.StatusForbidden || !strings.Contains(body, db.NotActiveMemberMessage) {
			t.Errorf("status = %d, body = %s, want 403 %q", status, body, db.NotActiveMemberMessage)
		}
	})
}
