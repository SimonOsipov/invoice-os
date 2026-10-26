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
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	staffTenantPath  = "6f0f6f0f-0000-4000-8000-0000000000bb"
	reasonSentence   = "reason must be 1 to 500 characters"
	badBodySentence  = "invalid request body"
	tooLargeSentence = "request body too large"
)

type staffHandlerCall struct{ tenant, reason string }

// staffHandlers serves both handlers over stubs; calls records every store call.
type staffHandlers struct {
	start, cancel http.HandlerFunc
	calls         []staffHandlerCall
	logs          *bytes.Buffer
}

func newStaffHandlers(startDue time.Time, startErr, cancelErr error) *staffHandlers {
	h := &staffHandlers{logs: &bytes.Buffer{}}
	log := slog.New(slog.NewJSONHandler(h.logs, nil))
	h.start = StaffScheduleDeletionHandler(func(_ context.Context, tenant, reason string) (time.Time, error) {
		h.calls = append(h.calls, staffHandlerCall{tenant, reason})
		return startDue, startErr
	}, log)
	h.cancel = StaffCancelDeletionHandler(func(_ context.Context, tenant, reason string) error {
		h.calls = append(h.calls, staffHandlerCall{tenant, reason})
		return cancelErr
	}, log)
	return h
}

func staffPost(h http.HandlerFunc, target, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	r.SetPathValue("id", staffTenantPath)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestStaffScheduleDeletionHandler_BodyRules(t *testing.T) {
	// A body of exactly n bytes: a valid object padded with trailing spaces.
	padded := func(n int) string {
		const obj = `{"reason":"x"}`
		return obj + strings.Repeat(" ", n-len(obj))
	}
	reasonOf := func(runes int) string { return `{"reason":"` + strings.Repeat("é", runes) + `"}` }

	rejects := []struct {
		name   string
		body   string
		status int
		msg    string
	}{
		{"no reason", `{}`, 400, reasonSentence},
		{"a null body", `null`, 400, reasonSentence},
		{"an empty reason", `{"reason":""}`, 400, reasonSentence},
		{"a whitespace-only reason", `{"reason":"  \t\n "}`, 400, reasonSentence},
		{"501 runes", reasonOf(501), 400, reasonSentence},
		{"a NUL in the reason", `{"reason":"a\u0000b"}`, 400, reasonSentence},
		{"an unknown field", `{"reason":"x","extra":1}`, 400, badBodySentence},
		{"trailing data", `{"reason":"x"}{}`, 400, badBodySentence},
		{"a second value", `{"reason":"x"} 1`, 400, badBodySentence},
		{"an empty body", ``, 400, badBodySentence},
		{"not JSON", `reason`, 400, badBodySentence},
		{"a reason of the wrong type", `{"reason":5}`, 400, badBodySentence},
		{"9 KiB", `{"reason":"` + strings.Repeat("a", 9<<10) + `"}`, 413, tooLargeSentence},
		{"one byte over 8 KiB", padded(8<<10 + 1), 413, tooLargeSentence},
	}
	for name, h := range map[string]func(*staffHandlers) http.HandlerFunc{
		"start":  func(s *staffHandlers) http.HandlerFunc { return s.start },
		"cancel": func(s *staffHandlers) http.HandlerFunc { return s.cancel },
	} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range rejects {
				t.Run(tc.name, func(t *testing.T) {
					s := newStaffHandlers(time.Now(), nil, nil)
					rec := staffPost(h(s), "/v1/staff/tenants/"+staffTenantPath+"/deletion", tc.body)
					var got map[string]string
					_ = json.Unmarshal(rec.Body.Bytes(), &got)
					if rec.Code != tc.status || got["error"] != tc.msg || len(got) != 1 {
						t.Errorf("got %d %q, want %d {error: %q}", rec.Code, rec.Body.String(), tc.status, tc.msg)
					}
					if len(s.calls) != 0 {
						t.Errorf("the store was called %d time(s) for a refused body, want 0", len(s.calls))
					}
				})
			}

			accepts := []struct{ name, body, wantReason string }{
				{"500 runes", reasonOf(500), strings.Repeat("é", 500)},
				{"a padded reason is trimmed", `{"reason":"  customer asked  "}`, "customer asked"},
				{"exactly 8 KiB", padded(8 << 10), "x"},
			}
			for _, tc := range accepts {
				t.Run(tc.name, func(t *testing.T) {
					s := newStaffHandlers(time.Now(), nil, nil)
					rec := staffPost(h(s), "/v1/staff/tenants/"+staffTenantPath+"/deletion", tc.body)
					if rec.Code != http.StatusOK {
						t.Fatalf("got %d %q, want 200", rec.Code, rec.Body.String())
					}
					if len(s.calls) != 1 || s.calls[0] != (staffHandlerCall{staffTenantPath, tc.wantReason}) {
						t.Errorf("store calls = %+v, want one call with the path id and the trimmed reason", s.calls)
					}
				})
			}
		})
	}
}

func TestStaffDeletionHandlers_MapEveryOutcome(t *testing.T) {
	lagos := time.FixedZone("WAT", 3600)
	due := time.Date(2026, 11, 24, 19, 10, 44, 0, lagos) // 18:10:44Z

	outcomes := []struct {
		name   string
		err    error
		status int
		msg    string
	}{
		{"not staff", db.ErrNotStaff, 403, "forbidden"},
		{"tenant not found", ErrTenantNotFound, 404, "tenant not found"},
		{"membership not found", ErrMembershipNotFound, 404, "membership not found"},
		{"workspace locked", db.ErrWorkspaceLocked, 409, db.WorkspaceLockedMessage},
		{"name mismatch", ErrWorkspaceNameMismatch, 400, "the workspace name does not match"},
		{"house workspace", ErrHouseWorkspace, 409, "the ASComply staff workspace cannot be deleted"},
		{"deletion underway", ErrDeletionUnderway, 409, "this workspace is already being deleted"},
		{"outcome changed", ErrDeletionOutcomeChanged, 409, "the workspace changed; review the page and confirm again"},
		{"an unknown failure", errors.New("boom"), 500, "internal server error"},
	}
	const body = `{"reason":"customer asked by email"}`
	for _, which := range []string{"start", "cancel"} {
		t.Run(which, func(t *testing.T) {
			for _, tc := range outcomes {
				for _, wrapped := range []bool{false, true} {
					err := tc.err
					if wrapped {
						err = fmt.Errorf("x: %w", tc.err)
					}
					t.Run(fmt.Sprintf("%s wrapped=%v", tc.name, wrapped), func(t *testing.T) {
						s := newStaffHandlers(time.Time{}, err, err)
						h := s.start
						if which == "cancel" {
							h = s.cancel
						}
						rec := staffPost(h, "/v1/staff/tenants/"+staffTenantPath+"/deletion", body)
						var got map[string]string
						_ = json.Unmarshal(rec.Body.Bytes(), &got)
						if rec.Code != tc.status || got["error"] != tc.msg || len(got) != 1 {
							t.Errorf("got %d %q, want %d {error: %q}", rec.Code, rec.Body.String(), tc.status, tc.msg)
						}
						errLines := strings.Count(s.logs.String(), `"level":"ERROR"`)
						if tc.status == 500 && errLines != 1 {
							t.Errorf("ERROR log lines = %d, want 1 for a 500", errLines)
						}
						if tc.status != 500 && errLines != 0 {
							t.Errorf("ERROR log lines = %d, want 0 for a %d", errLines, tc.status)
						}
						if strings.Contains(s.logs.String(), "customer asked by email") {
							t.Errorf("the log carries the reason: %s", s.logs.String())
						}
					})
				}
			}
		})
	}

	t.Run("a start answers the due time as UTC", func(t *testing.T) {
		s := newStaffHandlers(due, nil, nil)
		rec := staffPost(s.start, "/v1/staff/tenants/"+staffTenantPath+"/deletion", body)
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"deletion_due_at":"2026-11-24T18:10:44Z"}` {
			t.Errorf("got %d %q, want 200 {deletion_due_at: 2026-11-24T18:10:44Z}", rec.Code, rec.Body.String())
		}
	})
	t.Run("a cancel answers a null due time", func(t *testing.T) {
		s := newStaffHandlers(due, nil, nil)
		rec := staffPost(s.cancel, "/v1/staff/tenants/"+staffTenantPath+"/deletion/cancel", body)
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"deletion_due_at":null}` {
			t.Errorf("got %d %q, want 200 {deletion_due_at: null}", rec.Code, rec.Body.String())
		}
	})
}

type purgeCall struct{ tenant, name, reason string }

// purgeHandler serves StaffPurgeHandler over a stub; calls records every store call.
func purgeHandler(at time.Time, err error) (http.HandlerFunc, *[]purgeCall, *bytes.Buffer) {
	var calls []purgeCall
	logs := &bytes.Buffer{}
	h := StaffPurgeHandler(func(_ context.Context, tenant, name, reason string) (time.Time, error) {
		calls = append(calls, purgeCall{tenant, name, reason})
		return at, err
	}, slog.New(slog.NewJSONHandler(logs, nil)))
	return h, &calls, logs
}

const purgePath = "/v1/staff/tenants/" + staffTenantPath + "/deletion/purge"

func TestStaffPurgeHandler_MapsEveryOutcome(t *testing.T) {
	lagos := time.FixedZone("WAT", 3600)
	at := time.Date(2026, 11, 24, 19, 10, 44, 0, lagos) // 18:10:44Z
	const body = `{"workspace_name":"Obi Partners","reason":"customer asked by email"}`

	outcomes := []struct {
		name   string
		err    error
		status int
		msg    string
	}{
		{"not staff", db.ErrNotStaff, 403, "forbidden"},
		{"tenant not found", ErrTenantNotFound, 404, "tenant not found"},
		{"name mismatch", ErrWorkspaceNameMismatch, 400, "the workspace name does not match"},
		{"house workspace", ErrHouseWorkspace, 409, "the ASComply staff workspace cannot be deleted"},
		{"deletion underway", ErrDeletionUnderway, 409, "this workspace is already being deleted"},
		{"an unknown failure", errors.New("boom"), 500, "internal server error"},
	}
	for _, tc := range outcomes {
		for _, wrapped := range []bool{false, true} {
			err := tc.err
			if wrapped {
				err = fmt.Errorf("x: %w", tc.err)
			}
			t.Run(fmt.Sprintf("%s wrapped=%v", tc.name, wrapped), func(t *testing.T) {
				h, calls, logs := purgeHandler(time.Time{}, err)
				rec := staffPost(h, purgePath, body)
				var got map[string]string
				_ = json.Unmarshal(rec.Body.Bytes(), &got)
				if rec.Code != tc.status || got["error"] != tc.msg || len(got) != 1 {
					t.Errorf("got %d %q, want %d {error: %q}", rec.Code, rec.Body.String(), tc.status, tc.msg)
				}
				if len(*calls) != 1 {
					t.Errorf("store calls = %d, want 1", len(*calls))
				}
				errLines := strings.Count(logs.String(), `"level":"ERROR"`)
				if (tc.status == 500) != (errLines == 1) || (tc.status != 500 && errLines != 0) {
					t.Errorf("ERROR log lines = %d for a %d, want 1 for a 500 and 0 otherwise", errLines, tc.status)
				}
				for _, secret := range []string{"customer asked by email"} { // the reason never reaches a log
					if strings.Contains(logs.String(), secret) {
						t.Errorf("the log carries %q: %s", secret, logs.String())
					}
				}
			})
		}
	}

	t.Run("a purge answers 202 and the marker time as UTC", func(t *testing.T) {
		h, calls, _ := purgeHandler(at, nil)
		rec := staffPost(h, purgePath, body)
		if rec.Code != http.StatusAccepted || strings.TrimSpace(rec.Body.String()) != `{"deletion_purge_started_at":"2026-11-24T18:10:44Z"}` {
			t.Errorf("got %d %q, want 202 {deletion_purge_started_at: 2026-11-24T18:10:44Z}", rec.Code, rec.Body.String())
		}
		if len(*calls) != 1 || (*calls)[0] != (purgeCall{staffTenantPath, "Obi Partners", "customer asked by email"}) {
			t.Errorf("store calls = %+v, want one with the path id, the typed name and the reason", *calls)
		}
	})
}

func TestStaffPurgeHandler_BodyRules(t *testing.T) {
	reasonOf := func(runes int) string {
		return `{"workspace_name":"Obi Partners","reason":"` + strings.Repeat("é", runes) + `"}`
	}
	rejects := []struct {
		name   string
		body   string
		status int
		msg    string
	}{
		{"no reason", `{"workspace_name":"Obi Partners"}`, 400, reasonSentence},
		{"a whitespace-only reason", `{"workspace_name":"Obi Partners","reason":" \t "}`, 400, reasonSentence},
		{"501 runes", reasonOf(501), 400, reasonSentence},
		{"a NUL in the reason", `{"workspace_name":"Obi Partners","reason":"a\u0000b"}`, 400, reasonSentence},
		{"an unknown field", `{"workspace_name":"Obi Partners","reason":"x","extra":1}`, 400, badBodySentence},
		{"trailing data", `{"workspace_name":"Obi Partners","reason":"x"}{}`, 400, badBodySentence},
		{"an empty body", ``, 400, badBodySentence},
		{"a name of the wrong type", `{"workspace_name":5,"reason":"x"}`, 400, badBodySentence},
		{"9 KiB", `{"workspace_name":"Obi Partners","reason":"` + strings.Repeat("a", 9<<10) + `"}`, 413, tooLargeSentence},
	}
	for _, tc := range rejects {
		t.Run(tc.name, func(t *testing.T) {
			h, calls, _ := purgeHandler(time.Now(), nil)
			rec := staffPost(h, purgePath, tc.body)
			var got map[string]string
			_ = json.Unmarshal(rec.Body.Bytes(), &got)
			if rec.Code != tc.status || got["error"] != tc.msg || len(got) != 1 {
				t.Errorf("got %d %q, want %d {error: %q}", rec.Code, rec.Body.String(), tc.status, tc.msg)
			}
			if len(*calls) != 0 {
				t.Errorf("the store was called %d time(s) for a refused body, want 0", len(*calls))
			}
		})
	}

	accepts := []struct {
		name, body string
		want       purgeCall
	}{
		{"500 runes", reasonOf(500), purgeCall{staffTenantPath, "Obi Partners", strings.Repeat("é", 500)}},
		{"the reason is trimmed", `{"workspace_name":"Obi Partners","reason":"  court order  "}`, purgeCall{staffTenantPath, "Obi Partners", "court order"}},
		// The name is byte-compared by the store: the handler must not trim or fold it.
		{"the name reaches the store untouched", `{"workspace_name":" obi partners ","reason":"x"}`, purgeCall{staffTenantPath, " obi partners ", "x"}},
		{"no name reaches the store as empty", `{"reason":"x"}`, purgeCall{staffTenantPath, "", "x"}},
	}
	for _, tc := range accepts {
		t.Run(tc.name, func(t *testing.T) {
			h, calls, _ := purgeHandler(time.Now(), nil)
			rec := staffPost(h, purgePath, tc.body)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("got %d %q, want 202", rec.Code, rec.Body.String())
			}
			if len(*calls) != 1 || (*calls)[0] != tc.want {
				t.Errorf("store calls = %+v, want one %+v", *calls, tc.want)
			}
		})
	}
}
