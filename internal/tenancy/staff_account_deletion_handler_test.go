package tenancy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	staffMemberPath      = "6f0f6f0f-0000-4000-8000-0000000000cc"
	staffHeirPath        = "6f0f6f0f-0000-4000-8000-0000000000dd"
	memberDeletionPath   = "/v1/staff/tenants/" + staffTenantPath + "/members/" + staffMemberPath + "/deletion"
	lastMemberSentence   = "this is the last active member; start a workspace deletion instead" // ErrLastActiveMember
	outcomeChangedPhrase = "the workspace changed; review the page and confirm again"           // ErrDeletionOutcomeChanged
)

type accDelCall struct {
	tenant, user string
	want         AccountDeletionOutcome
	heir, reason string
}

// accDelHandlers serves both handlers over stubs; calls records every store call.
type accDelHandlers struct {
	del, preview http.HandlerFunc
	calls        []accDelCall
	previews     [][2]string
	logs         *bytes.Buffer
}

func newAccDelHandlers(prev AccountDeletionPreview, err error) *accDelHandlers {
	h := &accDelHandlers{logs: &bytes.Buffer{}}
	log := slog.New(slog.NewJSONHandler(h.logs, nil))
	h.del = StaffDeleteAccountHandler(func(_ context.Context, tenant, user string, want AccountDeletionOutcome, heir, reason string) error {
		h.calls = append(h.calls, accDelCall{tenant, user, want, heir, reason})
		return err
	}, log)
	h.preview = StaffAccountDeletionPreviewHandler(func(_ context.Context, tenant, user string) (AccountDeletionPreview, error) {
		h.previews = append(h.previews, [2]string{tenant, user})
		return prev, err
	}, log)
	return h
}

func accDelRequest(h http.HandlerFunc, method, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, memberDeletionPath, strings.NewReader(body))
	r.SetPathValue("id", staffTenantPath)
	r.SetPathValue("user_id", staffMemberPath)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func errorBody(rec *httptest.ResponseRecorder) (string, int) {
	var got map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	return got["error"], len(got)
}

func TestStaffDeleteAccountHandler_BodyRulesAndOutcomes(t *testing.T) {
	const reason = `"reason":"customer asked by email"`
	heir := `"heir_user_id":"` + staffHeirPath + `"`

	rejects := []struct {
		name   string
		body   string
		status int
		msg    string
	}{
		{"leave with a heir", `{"outcome":"leave",` + heir + `,` + reason + `}`, 400, badBodySentence},
		{"hand_on without a heir", `{"outcome":"hand_on",` + reason + `}`, 400, badBodySentence},
		{"hand_on with a null heir", `{"outcome":"hand_on","heir_user_id":null,` + reason + `}`, 400, badBodySentence},
		{"hand_on with a bad uuid", `{"outcome":"hand_on","heir_user_id":"x",` + reason + `}`, 400, badBodySentence},
		{"delete_workspace", `{"outcome":"delete_workspace",` + reason + `}`, 400, badBodySentence},
		{"no outcome", `{` + reason + `}`, 400, badBodySentence},
		{"an unknown outcome", `{"outcome":"purge",` + reason + `}`, 400, badBodySentence},
		{"a bad outcome and no reason: the outcome rule runs first", `{"outcome":"delete_workspace"}`, 400, badBodySentence},
		{"no reason", `{"outcome":"leave"}`, 400, reasonSentence},
		{"a whitespace-only reason", `{"outcome":"leave","reason":"  \t "}`, 400, reasonSentence},
		{"501 runes", `{"outcome":"leave","reason":"` + strings.Repeat("é", 501) + `"}`, 400, reasonSentence},
		{"a NUL in the reason", `{"outcome":"leave","reason":"a\u0000b"}`, 400, reasonSentence},
		{"an unknown field", `{"outcome":"leave",` + reason + `,"extra":1}`, 400, badBodySentence},
		{"trailing data", `{"outcome":"leave",` + reason + `}{}`, 400, badBodySentence},
		{"an empty body", ``, 400, badBodySentence},
		{"9 KiB", `{"outcome":"leave","reason":"` + strings.Repeat("a", 9<<10) + `"}`, 413, tooLargeSentence},
	}
	for _, tc := range rejects {
		t.Run(tc.name, func(t *testing.T) {
			h := newAccDelHandlers(AccountDeletionPreview{}, nil)
			rec := accDelRequest(h.del, http.MethodPost, tc.body)
			if msg, n := errorBody(rec); rec.Code != tc.status || msg != tc.msg || n != 1 {
				t.Errorf("got %d %q, want %d {error: %q}", rec.Code, rec.Body.String(), tc.status, tc.msg)
			}
			if len(h.calls) != 0 {
				t.Errorf("the store was called %d time(s) for a refused body, want 0", len(h.calls))
			}
		})
	}

	accepts := []struct {
		name, body string
		want       accDelCall
	}{
		{"leave", `{"outcome":"leave",` + reason + `}`,
			accDelCall{staffTenantPath, staffMemberPath, OutcomeLeave, "", "customer asked by email"}},
		{"hand_on with a heir", `{"outcome":"hand_on",` + heir + `,` + reason + `}`,
			accDelCall{staffTenantPath, staffMemberPath, OutcomeHandOn, staffHeirPath, "customer asked by email"}},
		{"the heir id reaches the store in canonical form", `{"outcome":"hand_on","heir_user_id":"` + strings.ToUpper(staffHeirPath) + `",` + reason + `}`,
			accDelCall{staffTenantPath, staffMemberPath, OutcomeHandOn, staffHeirPath, "customer asked by email"}},
		{"the reason is trimmed", `{"outcome":"leave","reason":"  court order  "}`,
			accDelCall{staffTenantPath, staffMemberPath, OutcomeLeave, "", "court order"}},
		{"500 runes", `{"outcome":"leave","reason":"` + strings.Repeat("é", 500) + `"}`,
			accDelCall{staffTenantPath, staffMemberPath, OutcomeLeave, "", strings.Repeat("é", 500)}},
	}
	for _, tc := range accepts {
		t.Run(tc.name, func(t *testing.T) {
			h := newAccDelHandlers(AccountDeletionPreview{}, nil)
			rec := accDelRequest(h.del, http.MethodPost, tc.body)
			wantBody := `{"outcome":"` + string(tc.want.want) + `"}`
			if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != wantBody {
				t.Errorf("got %d %q, want 200 %s", rec.Code, rec.Body.String(), wantBody)
			}
			if len(h.calls) != 1 || h.calls[0] != tc.want {
				t.Errorf("store calls = %+v, want one call %+v", h.calls, tc.want)
			}
		})
	}

	outcomes := []struct {
		name   string
		err    error
		status int
		msg    string
	}{
		{"not staff", db.ErrNotStaff, 403, "forbidden"},
		{"tenant not found", ErrTenantNotFound, 404, "tenant not found"},
		{"membership not found", ErrMembershipNotFound, 404, "membership not found"},
		{"house workspace", ErrHouseWorkspace, 409, "the ASComply staff workspace cannot be deleted"},
		{"workspace locked", db.ErrWorkspaceLocked, 409, db.WorkspaceLockedMessage},
		{"outcome changed", ErrDeletionOutcomeChanged, 409, outcomeChangedPhrase},
		{"last active member", ErrLastActiveMember, 409, lastMemberSentence},
		{"an unknown failure", errors.New("boom"), 500, "internal server error"},
	}
	for _, tc := range outcomes {
		for _, wrapped := range []bool{false, true} {
			err := tc.err
			if wrapped {
				err = fmt.Errorf("x: %w", tc.err)
			}
			t.Run(fmt.Sprintf("%s wrapped=%v", tc.name, wrapped), func(t *testing.T) {
				h := newAccDelHandlers(AccountDeletionPreview{}, err)
				rec := accDelRequest(h.del, http.MethodPost, `{"outcome":"leave","reason":"customer asked by email"}`)
				if msg, n := errorBody(rec); rec.Code != tc.status || msg != tc.msg || n != 1 {
					t.Errorf("got %d %q, want %d {error: %q}", rec.Code, rec.Body.String(), tc.status, tc.msg)
				}
				errLines := strings.Count(h.logs.String(), `"level":"ERROR"`)
				if (tc.status == 500) != (errLines == 1) || (tc.status != 500 && errLines != 0) {
					t.Errorf("ERROR log lines = %d for a %d, want 1 for a 500 and 0 otherwise", errLines, tc.status)
				}
				for _, secret := range []string{"customer asked by email", staffMemberPath} { // the reason and the user id never reach a log
					if strings.Contains(h.logs.String(), secret) {
						t.Errorf("the log carries %q: %s", secret, h.logs.String())
					}
				}
			})
		}
	}
}

func TestStaffAccountDeletionPreviewHandler_ShapeAndOutcomes(t *testing.T) {
	name, id := "Chidi Eze", staffHeirPath
	t.Run("hand_on answers the heir", func(t *testing.T) {
		h := newAccDelHandlers(AccountDeletionPreview{Outcome: OutcomeHandOn, HeirName: &name, HeirUserID: &id}, nil)
		rec := accDelRequest(h.preview, http.MethodGet, "")
		want := `{"outcome":"hand_on","heir_name":"Chidi Eze","heir_user_id":"` + staffHeirPath + `"}`
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
			t.Errorf("got %d %q, want 200 %s", rec.Code, rec.Body.String(), want)
		}
		if len(h.previews) != 1 || h.previews[0] != [2]string{staffTenantPath, staffMemberPath} {
			t.Errorf("store calls = %v, want one with the path tenant and user", h.previews)
		}
	})
	t.Run("leave answers null heir fields", func(t *testing.T) {
		h := newAccDelHandlers(AccountDeletionPreview{Outcome: OutcomeLeave}, nil)
		rec := accDelRequest(h.preview, http.MethodGet, "")
		want := `{"outcome":"leave","heir_name":null,"heir_user_id":null}`
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
			t.Errorf("got %d %q, want 200 %s", rec.Code, rec.Body.String(), want)
		}
	})
	for _, tc := range []struct {
		name   string
		err    error
		status int
		msg    string
	}{
		{"not staff", db.ErrNotStaff, 403, "forbidden"},
		{"tenant not found", ErrTenantNotFound, 404, "tenant not found"},
		{"membership not found", ErrMembershipNotFound, 404, "membership not found"},
		{"house workspace", ErrHouseWorkspace, 409, "the ASComply staff workspace cannot be deleted"},
		{"an unknown failure", errors.New("boom"), 500, "internal server error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newAccDelHandlers(AccountDeletionPreview{}, fmt.Errorf("x: %w", tc.err))
			rec := accDelRequest(h.preview, http.MethodGet, "")
			if msg, n := errorBody(rec); rec.Code != tc.status || msg != tc.msg || n != 1 {
				t.Errorf("got %d %q, want %d {error: %q}", rec.Code, rec.Body.String(), tc.status, tc.msg)
			}
			errLines := strings.Count(h.logs.String(), `"level":"ERROR"`)
			if (tc.status == 500) != (errLines == 1) || (tc.status != 500 && errLines != 0) {
				t.Errorf("ERROR log lines = %d for a %d, want 1 for a 500 and 0 otherwise", errLines, tc.status)
			}
			if strings.Contains(h.logs.String(), staffMemberPath) {
				t.Errorf("the log carries the user id: %s", h.logs.String())
			}
		})
	}
}
