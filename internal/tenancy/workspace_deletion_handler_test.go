package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// deletionRefusals is the status and sentence of each store outcome, from the API contract.
var deletionRefusals = []struct {
	name   string
	err    error
	status int
	msg    string
}{
	{"no tenant", db.ErrNoTenant, http.StatusUnauthorized, "unauthorized"},
	{"not an active member", db.ErrNotActiveMember, http.StatusForbidden, db.NotActiveMemberMessage},
	{"already locked", db.ErrWorkspaceLocked, http.StatusForbidden, db.WorkspaceLockedMessage},
	{"an unknown failure", errors.New("boom"), http.StatusInternalServerError, "internal server error"},
}

func deletionCall(h http.HandlerFunc, id *auth.Identity, target, body string) (int, map[string]json.RawMessage) {
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	out := map[string]json.RawMessage{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func errorSentence(body map[string]json.RawMessage) string {
	var s string
	_ = json.Unmarshal(body["error"], &s)
	return s
}

var deletionCaller = &auth.Identity{Subject: "6f0f6f0f-0000-4000-8000-000000000001", Role: "authenticated", TenantID: "6f0f6f0f-0000-4000-8000-0000000000aa"}

func TestScheduleDeletionHandler_MapsEveryOutcome(t *testing.T) {
	const target = "/v1/workspace/deletion"
	lagos := time.FixedZone("WAT", 3600)
	due := time.Date(2026, 11, 24, 19, 10, 44, 0, lagos) // 18:10:44Z

	// a body of exactly n bytes whose workspace_name is padding
	bodyOf := func(n int) string {
		const frame = `{"workspace_name":""}`
		return `{"workspace_name":"` + strings.Repeat("a", n-len(frame)) + `"}`
	}

	t.Run("a success answers the due date as UTC", func(t *testing.T) {
		var got string
		h := ScheduleDeletionHandler(func(_ context.Context, typed string) (time.Time, error) { got = typed; return due, nil }, nil)
		status, body := deletionCall(h, deletionCaller, target, `{"workspace_name":" Obi Partners "}`)
		if status != http.StatusOK || string(body["deletion_due_at"]) != `"2026-11-24T18:10:44Z"` {
			t.Errorf("status = %d, body = %v, want 200 and deletion_due_at 2026-11-24T18:10:44Z", status, body)
		}
		if got != " Obi Partners " {
			t.Errorf("typed name passed on = %q, want the bytes unchanged", got)
		}
	})

	t.Run("the body limit is 4 KiB", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			body   string
			called bool
			status int
		}{
			{"4096 bytes", bodyOf(4096), true, http.StatusOK},
			{"4097 bytes", bodyOf(4097), false, http.StatusBadRequest},
			{"malformed JSON", `{"workspace_name":`, false, http.StatusBadRequest},
			{"an empty body", ``, false, http.StatusBadRequest},
		} {
			t.Run(tc.name, func(t *testing.T) {
				called := false
				h := ScheduleDeletionHandler(func(context.Context, string) (time.Time, error) { called = true; return due, nil }, nil)
				status, body := deletionCall(h, deletionCaller, target, tc.body)
				if status != tc.status || called != tc.called {
					t.Fatalf("status = %d, store called = %v, want %d and %v", status, called, tc.status, tc.called)
				}
				if !tc.called && errorSentence(body) != "invalid request body" {
					t.Errorf("error = %q, want %q", errorSentence(body), "invalid request body")
				}
			})
		}
	})

	t.Run("each store outcome has its status and sentence", func(t *testing.T) {
		rows := append([]struct {
			name   string
			err    error
			status int
			msg    string
		}{
			{"a non-admin", ErrDeleteWorkspaceNotPermitted, http.StatusForbidden, "only an admin can delete the workspace"},
			{"a name mismatch", ErrWorkspaceNameMismatch, http.StatusBadRequest, "the workspace name does not match"},
			{"the house workspace", ErrHouseWorkspace, http.StatusConflict, "the ASComply staff workspace cannot be deleted"},
		}, deletionRefusals...)
		for _, tc := range rows {
			t.Run(tc.name, func(t *testing.T) {
				h := ScheduleDeletionHandler(func(context.Context, string) (time.Time, error) { return time.Time{}, tc.err }, nil)
				status, body := deletionCall(h, deletionCaller, target, `{"workspace_name":"x"}`)
				if status != tc.status || errorSentence(body) != tc.msg {
					t.Errorf("status = %d, error = %q, want %d and %q", status, errorSentence(body), tc.status, tc.msg)
				}
			})
		}
	})

	t.Run("no identity is 401 and the store is not called", func(t *testing.T) {
		called := false
		h := ScheduleDeletionHandler(func(context.Context, string) (time.Time, error) { called = true; return due, nil }, nil)
		status, body := deletionCall(h, nil, target, `{"workspace_name":"x"}`)
		if status != http.StatusUnauthorized || errorSentence(body) != "unauthorized" || called {
			t.Errorf("status = %d, error = %q, called = %v, want 401, unauthorized, false", status, errorSentence(body), called)
		}
	})
}

func TestCancelDeletionHandler_MapsEveryOutcome(t *testing.T) {
	const target = "/v1/workspace/deletion/cancel"

	t.Run("a success answers a null due date", func(t *testing.T) {
		h := CancelDeletionHandler(func(context.Context) error { return nil }, nil)
		status, body := deletionCall(h, deletionCaller, target, "")
		raw, present := body["deletion_due_at"]
		if status != http.StatusOK || !present || string(raw) != "null" {
			t.Errorf("status = %d, body = %v, want 200 with deletion_due_at null", status, body)
		}
	})

	rows := append([]struct {
		name   string
		err    error
		status int
		msg    string
	}{
		{"a non-admin", ErrCancelDeletionNotPermitted, http.StatusForbidden, "only an admin can cancel the deletion"},
	}, deletionRefusals...)
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			h := CancelDeletionHandler(func(context.Context) error { return tc.err }, nil)
			status, body := deletionCall(h, deletionCaller, target, "")
			if status != tc.status || errorSentence(body) != tc.msg {
				t.Errorf("status = %d, error = %q, want %d and %q", status, errorSentence(body), tc.status, tc.msg)
			}
		})
	}

	t.Run("no identity is 401 and the store is not called", func(t *testing.T) {
		called := false
		h := CancelDeletionHandler(func(context.Context) error { called = true; return nil }, nil)
		status, body := deletionCall(h, nil, target, "")
		if status != http.StatusUnauthorized || errorSentence(body) != "unauthorized" || called {
			t.Errorf("status = %d, error = %q, called = %v, want 401, unauthorized, false", status, errorSentence(body), called)
		}
	})
}

func TestCancelDeletionByLinkHandler_MapsEveryOutcome(t *testing.T) {
	const target = "/v1/workspace/deletion/cancel-by-link"
	const token = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCde"

	// a body of exactly n bytes whose token is padding
	bodyOf := func(n int) string {
		const frame = `{"token":""}`
		return `{"token":"` + strings.Repeat("a", n-len(frame)) + `"}`
	}

	t.Run("a success answers a null due date and passes the token on", func(t *testing.T) {
		var got string
		h := CancelDeletionByLinkHandler(func(_ context.Context, tok string) error { got = tok; return nil }, nil)
		status, body := deletionCall(h, deletionCaller, target, `{"token":"`+token+`"}`)
		raw, present := body["deletion_due_at"]
		if status != http.StatusOK || !present || string(raw) != "null" {
			t.Errorf("status = %d, body = %v, want 200 with deletion_due_at null", status, body)
		}
		if got != token {
			t.Errorf("token passed on = %q, want %q", got, token)
		}
	})

	t.Run("the body limit is 1 KiB", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			body   string
			called bool
			status int
		}{
			{"1024 bytes", bodyOf(1024), true, http.StatusOK},
			{"2048 bytes", bodyOf(2048), false, http.StatusBadRequest},
			{"malformed JSON", `{"token":`, false, http.StatusBadRequest},
			{"an empty body", ``, false, http.StatusBadRequest},
		} {
			t.Run(tc.name, func(t *testing.T) {
				called := false
				h := CancelDeletionByLinkHandler(func(context.Context, string) error { called = true; return nil }, nil)
				status, body := deletionCall(h, deletionCaller, target, tc.body)
				if status != tc.status || called != tc.called {
					t.Fatalf("status = %d, store called = %v, want %d and %v", status, called, tc.status, tc.called)
				}
				if !tc.called && errorSentence(body) != "invalid request body" {
					t.Errorf("error = %q, want %q", errorSentence(body), "invalid request body")
				}
			})
		}
	})

	t.Run("each store outcome has its status and sentence", func(t *testing.T) {
		rows := append([]struct {
			name   string
			err    error
			status int
			msg    string
		}{
			{"a bad link", ErrDeletionLinkNotValid, http.StatusForbidden, "this cancel link is no longer valid"},
			{"a non-admin", ErrCancelDeletionNotPermitted, http.StatusForbidden, "only an admin can cancel the deletion"},
		}, deletionRefusals...)
		for _, tc := range rows {
			t.Run(tc.name, func(t *testing.T) {
				h := CancelDeletionByLinkHandler(func(context.Context, string) error { return tc.err }, nil)
				status, body := deletionCall(h, deletionCaller, target, `{"token":"`+token+`"}`)
				if status != tc.status || errorSentence(body) != tc.msg {
					t.Errorf("status = %d, error = %q, want %d and %q", status, errorSentence(body), tc.status, tc.msg)
				}
			})
		}
	})

	t.Run("no identity is 401 and the store is not called", func(t *testing.T) {
		called := false
		h := CancelDeletionByLinkHandler(func(context.Context, string) error { called = true; return nil }, nil)
		status, body := deletionCall(h, nil, target, `{"token":"`+token+`"}`)
		if status != http.StatusUnauthorized || errorSentence(body) != "unauthorized" || called {
			t.Errorf("status = %d, error = %q, called = %v, want 401, unauthorized, false", status, errorSentence(body), called)
		}
	})
}

func TestMeHandler_SerialisesTheDueDateAsRFC3339UTC(t *testing.T) {
	lagos := time.FixedZone("WAT", 3600)
	due := time.Date(2026, 11, 24, 19, 10, 44, 0, lagos)
	tenantKeys := func(due *time.Time) map[string]json.RawMessage {
		load := func(context.Context) (Tenant, MeUser, error) {
			return Tenant{ID: "t", Name: "Obi", Kind: "firm", DeletionDueAt: due}, MeUser{Role: "admin"}, nil
		}
		r := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		r = r.WithContext(auth.WithIdentity(r.Context(), *deletionCaller))
		rec := httptest.NewRecorder()
		MeHandler(load, nil).ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var body map[string]map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
		return body["tenant"]
	}

	if got := string(tenantKeys(&due)["deletion_due_at"]); got != `"2026-11-24T18:10:44Z"` {
		t.Errorf("tenant.deletion_due_at = %s, want the UTC string \"2026-11-24T18:10:44Z\"", got)
	}
	raw, present := tenantKeys(nil)["deletion_due_at"]
	if !present || string(raw) != "null" {
		t.Errorf("unscheduled tenant.deletion_due_at = %q (present = %v), want the key with JSON null", raw, present)
	}
}
