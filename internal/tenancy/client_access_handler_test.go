//go:build firms0202

// Drop the build tag above when SetMemberClientsHandler lands: these files name API that does not exist yet.

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
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const grammarMsg = `client_access must be "all" or a list of client ids`

type putCall struct {
	user   string
	access ClientAccess
}

type putRig struct {
	calls []putCall
	err   error
	log   bytes.Buffer
}

func (r *putRig) setter(_ context.Context, user string, access ClientAccess) (MemberAccess, error) {
	r.calls = append(r.calls, putCall{user, access})
	if r.err != nil {
		return MemberAccess{}, r.err
	}
	return MemberAccess{Membership: Membership{UserID: user, Role: "preparer", Status: "active"}, ClientAccess: &access}, nil
}

func (r *putRig) put(t *testing.T, id *auth.Identity, pathID, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/memberships/{user_id}/clients",
		SetMemberClientsHandler(r.setter, slog.New(slog.NewTextHandler(&r.log, nil))))
	req := httptest.NewRequest("PUT", "/v1/memberships/"+pathID+"/clients", strings.NewReader(body))
	if id != nil {
		req = req.WithContext(auth.WithIdentity(req.Context(), *id))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var b struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return b.Error
}

func callerID() *auth.Identity {
	return &auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
}

// manyIDs is a body with n distinct valid ids.
func manyIDs(n int) string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = `"` + uuid.NewString() + `"`
	}
	return `{"client_access":[` + strings.Join(ids, ",") + `]}`
}

func TestSetMemberClientsHandler_BodyAndPath(t *testing.T) {
	target := uuid.NewString()
	e1 := uuid.NewString()

	t.Run("no identity is 401 and never reaches the store", func(t *testing.T) {
		var r putRig
		rec := r.put(t, nil, target, `{"client_access":"all"}`)
		if rec.Code != http.StatusUnauthorized || len(r.calls) != 0 {
			t.Errorf("status %d, %d store calls, want 401 and 0", rec.Code, len(r.calls))
		}
	})

	over := manyIDs(1800)
	if len(over) <= 64*1024 {
		t.Fatalf("fixture is %d bytes, want over 64 KiB", len(over))
	}
	bad := map[string]string{
		"an empty body":                     ``,
		"not json":                          `nope`,
		"an empty object":                   `{}`,
		"null":                              `{"client_access":null}`,
		"some":                              `{"client_access":"some"}`,
		"upper-case ALL":                    `{"client_access":"ALL"}`,
		"a number":                          `{"client_access":1}`,
		"true":                              `{"client_access":true}`,
		"an object":                         `{"client_access":{}}`,
		"a non-uuid id":                     `{"client_access":["x"]}`,
		"a number in the list":              `{"client_access":[1]}`,
		"a null in the list":                `{"client_access":[null]}`,
		"a duplicate":                       `{"client_access":["` + e1 + `","` + e1 + `"]}`,
		"a duplicate, any case":             `{"client_access":["` + strings.ToUpper(e1) + `","` + e1 + `"]}`,
		"over 64 KiB":                       over,
		"a top-level array":                 `["` + e1 + `"]`,
		"the right key spelled differently": `{"clientAccess":"all"}`,
	}
	for name, body := range bad {
		t.Run("400: "+name, func(t *testing.T) {
			var r putRig
			rec := r.put(t, callerID(), target, body)
			if rec.Code != http.StatusBadRequest || errorOf(t, rec) != grammarMsg {
				t.Errorf("status %d %q, want 400 %q", rec.Code, rec.Body.String(), grammarMsg)
			}
			if len(r.calls) != 0 {
				t.Errorf("the store was called %d time(s) for a refused body", len(r.calls))
			}
		})
	}

	t.Run("the body is checked before the path", func(t *testing.T) {
		var r putRig
		rec := r.put(t, callerID(), "nope", `{}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status %d, want 400 for a bad body on a bad path", rec.Code)
		}
	})
	t.Run("a non-uuid path is 404", func(t *testing.T) {
		var r putRig
		rec := r.put(t, callerID(), "nope", `{"client_access":"all"}`)
		if rec.Code != http.StatusNotFound || errorOf(t, rec) != "membership not found" || len(r.calls) != 0 {
			t.Errorf("status %d %q with %d store calls, want 404 membership not found and 0", rec.Code, rec.Body.String(), len(r.calls))
		}
	})
	t.Run("a body just under the cap reaches the store", func(t *testing.T) {
		var r putRig
		body := manyIDs(1000)
		if len(body) >= 64*1024 {
			t.Fatalf("fixture is %d bytes, want under 64 KiB", len(body))
		}
		rec := r.put(t, callerID(), target, body)
		if rec.Code != http.StatusOK || len(r.calls) != 1 || len(r.calls[0].access.IDs) != 1000 {
			t.Errorf("status %d with %d calls, want 200 and one call with 1000 ids", rec.Code, len(r.calls))
		}
	})
	t.Run("all reaches the store as All", func(t *testing.T) {
		var r putRig
		rec := r.put(t, callerID(), strings.ToUpper(target), `{"client_access":"all"}`)
		if rec.Code != http.StatusOK || len(r.calls) != 1 {
			t.Fatalf("status %d with %d calls: %s", rec.Code, len(r.calls), rec.Body.String())
		}
		if c := r.calls[0]; !c.access.All || len(c.access.IDs) != 0 || c.user != target {
			t.Errorf("call = %+v, want All, no ids and the canonical lower-case user %s", c, target)
		}
	})
	t.Run("an upper-case id reaches the store canonical", func(t *testing.T) {
		var r putRig
		other := uuid.NewString()
		rec := r.put(t, callerID(), target, `{"client_access":["`+strings.ToUpper(e1)+`","`+other+`"]}`)
		if rec.Code != http.StatusOK || len(r.calls) != 1 {
			t.Fatalf("status %d with %d calls: %s", rec.Code, len(r.calls), rec.Body.String())
		}
		got := slices.Clone(r.calls[0].access.IDs)
		want := []string{e1, other}
		slices.Sort(got)
		slices.Sort(want)
		if r.calls[0].access.All || !slices.Equal(got, want) {
			t.Errorf("access = %+v, want ids %v", r.calls[0].access, want)
		}
	})
	t.Run("an empty list reaches the store as no clients", func(t *testing.T) {
		var r putRig
		rec := r.put(t, callerID(), target, `{"client_access":[]}`)
		if rec.Code != http.StatusOK || len(r.calls) != 1 || r.calls[0].access.All || len(r.calls[0].access.IDs) != 0 {
			t.Errorf("status %d, calls %+v, want 200 and one call with neither All nor ids", rec.Code, r.calls)
		}
		var row map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &row); err != nil || string(row["client_access"]) != "[]" {
			t.Errorf("body %s, want client_access []", rec.Body.String())
		}
	})
}

func TestSetMemberClientsHandler_StatusMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		msg  string
	}{
		{"not an admin", ErrClientAccessNotPermitted, 403, "only an admin can choose a member's clients"},
		{"an admin target", ErrClientAccessAdmin, 409, "an admin sees every client"},
		{"in-house", ErrClientAccessNotFirm, 409, "client access applies to accounting firm workspaces only"},
		{"unknown client", ErrUnknownClient, 400, "a client in the list is not in this workspace"},
		{"invalid access", ErrInvalidClientAccess, 400, grammarMsg},
		{"unknown member", ErrMembershipNotFound, 404, "membership not found"},
		{"no tenant", db.ErrNoTenant, 401, "unauthorized"},
		{"not an active member", db.ErrNotActiveMember, 403, db.NotActiveMemberMessage},
		{"a plain error", errors.New("boom"), 500, "internal server error"},
	}
	for _, tc := range cases {
		for _, wrapped := range []bool{false, true} {
			err := tc.err
			name := tc.name
			if wrapped {
				err, name = fmt.Errorf("store: %w", tc.err), tc.name+" (wrapped)"
			}
			t.Run(name, func(t *testing.T) {
				r := putRig{err: err}
				rec := r.put(t, callerID(), uuid.NewString(), `{"client_access":"all"}`)
				if rec.Code != tc.code || errorOf(t, rec) != tc.msg {
					t.Errorf("status %d %q, want %d %q", rec.Code, rec.Body.String(), tc.code, tc.msg)
				}
				if strings.Contains(errorOf(t, rec), "tenancy: ") {
					t.Errorf("error %q leaks the sentinel prefix", errorOf(t, rec))
				}
				logged := strings.Contains(r.log.String(), "boom")
				if (tc.code == 500) != logged {
					t.Errorf("logged = %v for status %d, want a log line for a 500 only", logged, tc.code)
				}
			})
		}
	}
}

func TestSetMemberClientsHandler_AnswersTheSixKeys(t *testing.T) {
	var r putRig
	target := uuid.NewString()
	rec := r.put(t, callerID(), target, `{"client_access":"all"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var row map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &row); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, sixKeys) {
		t.Errorf("keys = %v, want %v", keys, sixKeys)
	}
	if string(row["client_access"]) != `"all"` || string(row["user_id"]) != `"`+target+`"` {
		t.Errorf("body = %s, want client_access \"all\" for %s", rec.Body.String(), target)
	}
}

func TestMemberships_ProjectsSixKeys(t *testing.T) {
	id := auth.Identity{Subject: "caller", Role: "authenticated", TenantID: uuid.NewString()}
	rowOf := func(u string, a *ClientAccess) MemberAccess {
		return MemberAccess{Membership: Membership{UserID: u, Role: "preparer", Status: "active"}, ClientAccess: a}
	}
	load := func(context.Context) ([]MemberAccess, error) {
		return []MemberAccess{
			rowOf("u1", &ClientAccess{All: true}),
			rowOf("u2", &ClientAccess{IDs: []string{}}),
			rowOf("u3", &ClientAccess{IDs: []string{"b", "a"}}),
			rowOf("u4", nil),
			rowOf("u5", &ClientAccess{}),
		}, nil
	}
	rec, body := doMemberships(t, load, &id)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(body.Memberships, &items); err != nil {
		t.Fatalf("decode memberships %q: %v", body.Memberships, err)
	}
	want := []string{`"all"`, `[]`, `["a","b"]`, `null`, `[]`}
	if len(items) != len(want) {
		t.Fatalf("%d items, want %d", len(items), len(want))
	}
	for i, it := range items {
		keys := make([]string, 0, len(it))
		for k := range it {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, sixKeys) {
			t.Errorf("item %d keys = %v, want %v", i, keys, sixKeys)
		}
		if string(it["client_access"]) != want[i] {
			t.Errorf("item %d client_access = %s, want %s", i, it["client_access"], want[i])
		}
	}
}

func TestMembership_PatchAnswersTheSixKeys(t *testing.T) {
	set := func(_ context.Context, user, status string) (MemberAccess, error) {
		return MemberAccess{Membership: Membership{UserID: user, Role: "preparer", Status: status}, ClientAccess: &ClientAccess{IDs: []string{"e1"}}}, nil
	}
	target := uuid.NewString()
	rec := patchViaMux(t, set, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}, target, `{"status":"suspended"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var row map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &row); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, sixKeys) || string(row["client_access"]) != `["e1"]` {
		t.Errorf("body = %s, want the six keys with client_access [\"e1\"]", rec.Body.String())
	}
}
