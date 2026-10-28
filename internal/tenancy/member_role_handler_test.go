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
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	roleVocabularyMsg = `role must be "admin", "preparer" or "reviewer"`
	roleNotAdminMsg   = "only an admin can change a member's role"
	lastAdminMsg      = "this is the tenant's last active admin — make another member an active admin first"
)

type roleSetter func(ctx context.Context, userID, role string) (MemberAccess, error)

func patchRoleViaMux(t *testing.T, set roleSetter, log *slog.Logger, id *auth.Identity, pathID, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /v1/memberships/{user_id}/role", SetMemberRoleHandler(set, log))
	req := httptest.NewRequest("PATCH", "/v1/memberships/"+pathID+"/role", strings.NewReader(body))
	if id != nil {
		req = req.WithContext(auth.WithIdentity(req.Context(), *id))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type roleCall struct{ user, role string }

type roleRig struct {
	calls []roleCall
	err   error
	log   bytes.Buffer
}

func (r *roleRig) setter(_ context.Context, user, role string) (MemberAccess, error) {
	r.calls = append(r.calls, roleCall{user, role})
	if r.err != nil {
		return MemberAccess{}, r.err
	}
	return MemberAccess{
		Membership:   Membership{UserID: user, Role: role, Status: "active", DisplayName: strp("Pia Preparer"), Email: strp("pia@members.test")},
		ClientAccess: &ClientAccess{All: true},
	}, nil
}

func (r *roleRig) patch(t *testing.T, id *auth.Identity, pathID, body string) *httptest.ResponseRecorder {
	t.Helper()
	return patchRoleViaMux(t, r.setter, slog.New(slog.NewTextHandler(&r.log, nil)), id, pathID, body)
}

func TestSetMemberRoleHandler_BodyAndPath(t *testing.T) {
	target := uuid.NewString()

	t.Run("no identity is 401 and never reaches the store", func(t *testing.T) {
		var r roleRig
		rec := r.patch(t, nil, target, `{"role":"reviewer"}`)
		if rec.Code != http.StatusUnauthorized || errorOf(t, rec) != "unauthorized" || len(r.calls) != 0 {
			t.Errorf("status %d %q with %d calls, want 401 unauthorized and 0", rec.Code, rec.Body.String(), len(r.calls))
		}
	})

	for name, body := range map[string]string{
		"not json":              `{`,
		"an empty body":         ``,
		"a number for the role": `{"role":1}`,
		"a top-level array":     `["reviewer"]`,
		"an array for the role": `{"role":["reviewer"]}`,
		"over 4 KiB":            strings.Repeat(" ", 5*1024) + `{"role":"reviewer"}`,
	} {
		t.Run("400 invalid request body: "+name, func(t *testing.T) {
			var r roleRig
			rec := r.patch(t, callerID(), target, body)
			if rec.Code != http.StatusBadRequest || errorOf(t, rec) != "invalid request body" || len(r.calls) != 0 {
				t.Errorf("status %d %q with %d calls, want 400 invalid request body and 0", rec.Code, rec.Body.String(), len(r.calls))
			}
		})
	}

	for name, body := range map[string]string{
		"owner":              `{"role":"owner"}`,
		"an empty object":    `{}`,
		"null":               `{"role":null}`,
		"an empty string":    `{"role":""}`,
		"upper-case Admin":   `{"role":"Admin"}`,
		"padded reviewer":    `{"role":" reviewer"}`,
		"the wrong key name": `{"access_role":"reviewer"}`,
	} {
		t.Run("400 role sentence: "+name, func(t *testing.T) {
			var r roleRig
			rec := r.patch(t, callerID(), target, body)
			if rec.Code != http.StatusBadRequest || errorOf(t, rec) != roleVocabularyMsg || len(r.calls) != 0 {
				t.Errorf("status %d %q with %d calls, want 400 %q and 0", rec.Code, rec.Body.String(), len(r.calls), roleVocabularyMsg)
			}
		})
	}

	t.Run("the body is checked before the path", func(t *testing.T) {
		var r roleRig
		rec := r.patch(t, callerID(), "nope", `{"role":"owner"}`)
		if rec.Code != http.StatusBadRequest || errorOf(t, rec) != roleVocabularyMsg || len(r.calls) != 0 {
			t.Errorf("status %d %q, want 400 %q for a bad role on a bad path", rec.Code, rec.Body.String(), roleVocabularyMsg)
		}
	})
	t.Run("a non-uuid path is 404", func(t *testing.T) {
		var r roleRig
		rec := r.patch(t, callerID(), "not-a-uuid", `{"role":"reviewer"}`)
		if rec.Code != http.StatusNotFound || errorOf(t, rec) != "membership not found" || len(r.calls) != 0 {
			t.Errorf("status %d %q with %d calls, want 404 membership not found and 0", rec.Code, rec.Body.String(), len(r.calls))
		}
	})
	t.Run("the 4 KiB cap is exact", func(t *testing.T) {
		const doc = `{"role":"reviewer"}`
		for name, tc := range map[string]struct {
			body string
			code int
		}{
			"4096 bytes": {strings.Repeat(" ", 4096-len(doc)) + doc, 200},
			"4097 bytes": {strings.Repeat(" ", 4097-len(doc)) + doc, 400},
		} {
			var r roleRig
			if rec := r.patch(t, callerID(), target, tc.body); rec.Code != tc.code {
				t.Errorf("%s: status %d, want %d", name, rec.Code, tc.code)
			}
		}
	})
	t.Run("body keys beyond role never reach the store", func(t *testing.T) {
		var r roleRig
		other := uuid.NewString()
		rec := r.patch(t, callerID(), target, `{"role":"reviewer","user_id":"`+other+`","status":"suspended","tenant_id":"`+other+`"}`)
		if rec.Code != http.StatusOK || len(r.calls) != 1 {
			t.Fatalf("status %d with %d calls: %s", rec.Code, len(r.calls), rec.Body.String())
		}
		if c := r.calls[0]; c.user != target || c.role != "reviewer" {
			t.Errorf("call = %+v, want the path user %s and reviewer", c, target)
		}
	})
	for name, form := range map[string]string{
		"braced":    "{" + target + "}",
		"urn":       "urn:uuid:" + target,
		"no hyphen": strings.ReplaceAll(target, "-", ""),
	} {
		t.Run("a "+name+" uuid reaches the store canonical", func(t *testing.T) {
			var r roleRig
			rec := r.patch(t, callerID(), form, `{"role":"reviewer"}`)
			if rec.Code != http.StatusOK || len(r.calls) != 1 || r.calls[0].user != target {
				t.Errorf("status %d, calls %+v, want 200 and the canonical %s", rec.Code, r.calls, target)
			}
		})
	}
	t.Run("a body just under the cap reaches the store", func(t *testing.T) {
		var r roleRig
		rec := r.patch(t, callerID(), target, strings.Repeat(" ", 3000)+`{"role":"reviewer"}`)
		if rec.Code != http.StatusOK || len(r.calls) != 1 {
			t.Errorf("status %d with %d calls, want 200 and 1", rec.Code, len(r.calls))
		}
	})
	for _, role := range []string{"admin", "preparer", "reviewer"} {
		t.Run("an upper-case id reaches the store canonical: "+role, func(t *testing.T) {
			var r roleRig
			rec := r.patch(t, callerID(), strings.ToUpper(target), `{"role":"`+role+`"}`)
			if rec.Code != http.StatusOK || len(r.calls) != 1 {
				t.Fatalf("status %d with %d calls: %s", rec.Code, len(r.calls), rec.Body.String())
			}
			if c := r.calls[0]; c.user != target || c.role != role {
				t.Errorf("call = %+v, want the canonical lower-case user %s and role %s", c, target, role)
			}
		})
	}
}

func TestSetMemberRoleHandler_StatusMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		msg  string
	}{
		{"not an admin", ErrRoleChangeNotPermitted, 403, roleNotAdminMsg},
		{"invalid role", ErrInvalidRole, 400, roleVocabularyMsg},
		{"unknown member", ErrMembershipNotFound, 404, "membership not found"},
		{"invited target", ErrInvitedNotTransitionable, 409, "an invited member has no sign-in to suspend or reactivate"},
		{"removed target", ErrRemovedNotTransitionable, 409, "a removed member must be invited again"},
		{"last active admin", ErrLastActiveAdmin, 409, lastAdminMsg},
		{"not an active member", db.ErrNotActiveMember, 403, db.NotActiveMemberMessage},
		{"workspace locked", db.ErrWorkspaceLocked, 403, db.WorkspaceLockedMessage},
		{"no tenant", db.ErrNoTenant, 401, "unauthorized"},
		{"a plain error", errors.New("boom"), 500, "internal server error"},
	}
	for _, tc := range cases {
		for _, wrapped := range []bool{false, true} {
			err, name := tc.err, tc.name
			if wrapped {
				err, name = fmt.Errorf("store: %w", tc.err), tc.name+" (wrapped)"
			}
			t.Run(name, func(t *testing.T) {
				r := roleRig{err: err}
				rec := r.patch(t, callerID(), uuid.NewString(), `{"role":"reviewer"}`)
				if rec.Code != tc.code || errorOf(t, rec) != tc.msg {
					t.Errorf("status %d %q, want %d %q", rec.Code, rec.Body.String(), tc.code, tc.msg)
				}
				if len(r.calls) != 1 {
					t.Errorf("the store was called %d time(s), want 1", len(r.calls))
				}
				if strings.Contains(errorOf(t, rec), "tenancy: ") {
					t.Errorf("error %q leaks the sentinel prefix", errorOf(t, rec))
				}
				if logged := strings.Contains(r.log.String(), "boom"); (tc.code == 500) != logged {
					t.Errorf("logged = %v for status %d, want a log line for a 500 only", logged, tc.code)
				}
			})
		}
	}
}

func TestSetMemberRoleHandler_AnswersTheSixKeys(t *testing.T) {
	var r roleRig
	target := uuid.NewString()
	rec := r.patch(t, callerID(), target, `{"role":"reviewer"}`)
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
	if string(row["user_id"]) != `"`+target+`"` || string(row["role"]) != `"reviewer"` || string(row["display_name"]) != `"Pia Preparer"` {
		t.Errorf("body = %s, want user_id %s and role reviewer", rec.Body.String(), target)
	}
}

func TestSetMemberRoleHandler_RefusalsAndLogsCarryNoNameOrEmail(t *testing.T) {
	const adaName, adaEmail = "Ada Obi", "ada.obi@example.test"
	const boName, boEmail = "Bo Eze", "bo.eze@example.test"
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "ACCESS-01-02 no pii")
	ada, prep := uuid.NewString(), uuid.NewString()
	seedIdentityMembership(t, super, tenant, ada, "admin", "active", strp(adaName), strp(adaEmail))
	seedMembership(t, super, tenant, prep, "preparer", "active")
	bo := uuid.NewString()
	seedIdentityMembership(t, super, tenant, bo, "reviewer", "active", strp(boName), strp(boEmail))
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	idOf := func(u string) *auth.Identity {
		return &auth.Identity{Subject: u, Role: "authenticated", TenantID: tenant}
	}

	var refusals []string
	step := func(label string, rec *httptest.ResponseRecorder, want int) {
		t.Helper()
		if rec.Code >= 400 {
			refusals = append(refusals, rec.Body.String())
		}
		if rec.Code != want {
			t.Errorf("%s: status = %d, want %d: %s", label, rec.Code, want, rec.Body.String())
		}
	}
	store := NewStore(app)
	step("preparer changes Ada's role", patchRoleViaMux(t, store.SetMemberRole, logger, idOf(prep), ada, `{"role":"reviewer"}`), 403)
	step("sole admin demotes self", patchRoleViaMux(t, store.SetMemberRole, logger, idOf(ada), ada, `{"role":"preparer"}`), 409)

	closed, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	closed.Close()
	step("role change on a closed pool", patchRoleViaMux(t, NewStore(closed).SetMemberRole, logger, idOf(ada), prep, `{"role":"reviewer"}`), 500)
	drop := failAudit(t, super, tenant, roleChangedEvent)
	step("role change whose audit write fails", patchRoleViaMux(t, store.SetMemberRole, logger, idOf(ada), bo, `{"role":"preparer"}`), 500)
	drop()
	if logs.Len() == 0 {
		t.Fatal("the 500 wrote no log line, so the absence checks below read nothing")
	}
	if len(refusals) != 4 {
		t.Fatalf("scanned %d refusal bodies, want 4 (403, 409, 500, 500)", len(refusals))
	}
	scanned := strings.ToLower(logs.String() + strings.Join(refusals, "\n"))
	for _, needle := range []string{adaName, adaEmail, boName, boEmail} {
		if strings.Contains(scanned, strings.ToLower(needle)) {
			t.Errorf("a refusal body or log line carries the fixture name or address %q", needle[:3]+"...")
		}
	}
}

func TestSetMemberRole_WireThroughTheRealStore(t *testing.T) {
	w := newInvWorld(t, "role wire "+uuid.NewString()[:8], "Ada Admin")
	prep := w.addMember(t, "preparer", "active", "Pia Preparer")
	idOf := func(u string) *auth.Identity {
		return &auth.Identity{Subject: u, Role: "authenticated", TenantID: w.tenant}
	}
	do := func(caller, target, role string) *httptest.ResponseRecorder {
		return patchRoleViaMux(t, w.store.SetMemberRole, nil, idOf(caller), target, `{"role":"`+role+`"}`)
	}

	rec := do(prep, w.admin, "reviewer")
	if rec.Code != http.StatusForbidden || errorOf(t, rec) != roleNotAdminMsg {
		t.Errorf("preparer: status %d %q, want 403 %q", rec.Code, rec.Body.String(), roleNotAdminMsg)
	}
	rec = do(w.admin, w.admin, "preparer")
	if rec.Code != http.StatusConflict || errorOf(t, rec) != lastAdminMsg {
		t.Errorf("sole admin demotes self: status %d %q, want 409 %q", rec.Code, rec.Body.String(), lastAdminMsg)
	}
	if got := w.storedRole(t, w.admin); got != "admin" {
		t.Errorf("the refused demotion left role %q, want admin", got)
	}

	rec = do(w.admin, prep, "reviewer")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin sets reviewer: status %d: %s", rec.Code, rec.Body.String())
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
	if string(row["role"]) != `"reviewer"` || string(row["user_id"]) != `"`+prep+`"` {
		t.Errorf("body = %s, want role reviewer for %s", rec.Body.String(), prep)
	}
	if got := w.storedRole(t, prep); got != "reviewer" {
		t.Errorf("stored role = %q, want reviewer", got)
	}

	other := newInvWorld(t, "role wire other "+uuid.NewString()[:8], "Zed Okoro")
	cross := other.addMember(t, "reviewer", "active", "Tunde Bello")
	invited := w.addMember(t, "preparer", "invited", "")
	removed := w.addMember(t, "preparer", "removed", "Rex Removed")
	crossBefore := imageOf(t, other.super, other.tenant, cross)
	for name, tc := range map[string]struct {
		caller, target string
		code           int
		msg            string
	}{
		"a member of another tenant":  {w.admin, cross, 404, "membership not found"},
		"an unknown member":           {w.admin, uuid.NewString(), 404, "membership not found"},
		"an invited member":           {w.admin, invited, 409, "an invited member has no sign-in to suspend or reactivate"},
		"a removed member":            {w.admin, removed, 409, "a removed member must be invited again"},
		"a caller with no membership": {uuid.NewString(), prep, 403, db.NotActiveMemberMessage},
	} {
		rec := do(tc.caller, tc.target, "admin")
		if rec.Code != tc.code || errorOf(t, rec) != tc.msg {
			t.Errorf("%s: status %d %q, want %d %q", name, rec.Code, rec.Body.String(), tc.code, tc.msg)
		}
	}
	if imageOf(t, other.super, other.tenant, cross) != crossBefore {
		t.Error("a cross-tenant request over the handler wrote to the other tenant's row")
	}
	if got := w.storedRole(t, invited) + w.storedRole(t, removed); got != "preparerpreparer" {
		t.Errorf("refused invited/removed requests left roles %q, want both preparer", got)
	}
	before := w.auditCountOf(t, roleChangedEvent)
	if rec := do(w.admin, prep, "reviewer"); rec.Code != http.StatusOK {
		t.Errorf("same role: status %d, want 200", rec.Code)
	}
	if after := w.auditCountOf(t, roleChangedEvent); after != before {
		t.Errorf("a same-role request wrote %d audit rows, want 0", after-before)
	}
}
