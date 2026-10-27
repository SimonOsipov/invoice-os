package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func mineMux(list MyPendingInvitationsFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/invitations/mine", InvitationsMineHandler(list, nil))
	return mux
}

func acceptByIDMux(accept AcceptInvitationByIDFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/invitations/{id}/accept", AcceptInvitationByIDHandler(accept, nil))
	return mux
}

func TestInvitationsMine_WireShape(t *testing.T) {
	exp := time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)
	ada := "Ada Obi"
	id1, id2 := uuid.NewString(), uuid.NewString()
	items := []PendingInvite{
		{ID: id1, Workspace: "Eze Ltd", Role: "preparer", Inviter: nil, ExpiresAt: exp},
		{ID: id2, Workspace: "Obi Partners", Role: "reviewer", Inviter: &ada, ExpiresAt: exp.Add(time.Hour)},
	}

	rec := apiDo(mineMux(func(context.Context) (MyInvitations, error) { return MyInvitations{Invitations: items}, nil }), context.Background(), http.MethodGet, "/v1/invitations/mine", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body %q: %v", rec.Body, err)
	}
	if strings.Join(keysOf(raw), ",") != "invitations,removed" || raw["removed"] != false {
		t.Fatalf("body = %s, want exactly {invitations, removed:false}", rec.Body)
	}
	var body struct {
		Invitations []map[string]any `json:"invitations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Invitations) != 2 {
		t.Fatalf("body = %s (%v), want 2 invitations", rec.Body, err)
	}
	for i, it := range body.Invitations {
		if got := keysOf(it); strings.Join(got, ",") != "expires_at,id,inviter,role,workspace" {
			t.Errorf("item %d keys = %v, want exactly id, workspace, role, inviter, expires_at", i, got)
		}
	}
	first, second := body.Invitations[0], body.Invitations[1]
	if first["id"] != id1 || first["workspace"] != "Eze Ltd" || first["role"] != "preparer" {
		t.Errorf("first item = %v, want the Eze Ltd invite first", first)
	}
	if v, present := first["inviter"]; !present || v != nil {
		t.Errorf("first inviter = %v (present %v), want JSON null", v, present)
	}
	if second["inviter"] != "Ada Obi" || second["id"] != id2 {
		t.Errorf("second item = %v, want inviter Ada Obi", second)
	}
	if got, err := time.Parse(time.RFC3339Nano, first["expires_at"].(string)); err != nil || !got.Equal(exp) {
		t.Errorf("expires_at = %v (%v), want RFC 3339 %s", first["expires_at"], err, exp)
	}

	for name, empty := range map[string][]PendingInvite{"empty slice": {}} {
		t.Run(name, func(t *testing.T) {
			rec := apiDo(mineMux(func(context.Context) (MyInvitations, error) { return MyInvitations{Invitations: empty}, nil }), context.Background(), http.MethodGet, "/v1/invitations/mine", "")
			if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"invitations":[],"removed":false}` {
				t.Errorf("answer = %d %s, want 200 {\"invitations\":[],\"removed\":false}", rec.Code, rec.Body)
			}
		})
	}
}

func TestInvitationsMine_MapsStoreErrors(t *testing.T) {
	for _, c := range []struct {
		name   string
		err    error
		status int
		msg    string
	}{
		{"already a member", ErrAlreadyMember, http.StatusConflict, "you already belong to a workspace"},
		{"no caller", db.ErrNoTenant, http.StatusUnauthorized, "unauthorized"},
		{"other", errors.New("pq: connection to 10.0.0.9 refused"), http.StatusInternalServerError, "internal server error"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := apiDo(mineMux(func(context.Context) (MyInvitations, error) { return MyInvitations{}, c.err }), context.Background(), http.MethodGet, "/v1/invitations/mine", "")
			assertErrorBody(t, rec, c.status, c.msg)
			if strings.Contains(rec.Body.String(), "10.0.0.9") {
				t.Errorf("body %s leaks the store error", rec.Body)
			}
		})
	}
}

func TestAcceptByID_MapsRefusals(t *testing.T) {
	id := uuid.NewString()
	for _, c := range []struct {
		name, path string
		err        error
		status     int
		msg        string
	}{
		{"not valid", id, ErrInvitationNotValid, http.StatusNotFound, "this invite is no longer valid"},
		{"already a member", id, ErrAlreadyMember, http.StatusConflict, "you already belong to a workspace"},
		{"no caller", id, db.ErrNoTenant, http.StatusUnauthorized, "unauthorized"},
		{"other", id, errors.New("pq: connection to 10.0.0.9 refused"), http.StatusInternalServerError, "internal server error"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var calls []string
			h := acceptByIDMux(func(_ context.Context, got string, _ *string) (Tenant, string, string, error) {
				calls = append(calls, got)
				return Tenant{}, "", "", c.err
			})
			rec := apiDo(h, context.Background(), http.MethodPost, "/v1/invitations/"+c.path+"/accept", "")
			assertErrorBody(t, rec, c.status, c.msg)
			if strings.Contains(rec.Body.String(), "10.0.0.9") {
				t.Errorf("body %s leaks the store error", rec.Body)
			}
			if len(calls) != 1 || calls[0] != id {
				t.Errorf("store calls = %v, want exactly [%s]", calls, id)
			}
		})
	}
}

func TestAcceptByID_WireShapeMatchesTheTokenAccept(t *testing.T) {
	id, subject := uuid.NewString(), uuid.NewString()
	tenant := Tenant{ID: uuid.NewString(), Name: "Obi Partners", Kind: "firm"}

	// A sent name changes nothing in the answer; a malformed body is a 400 (TestAcceptInvitationByIDHandler_MalformedBody).
	rec := apiDo(acceptByIDMux(func(context.Context, string, *string) (Tenant, string, string, error) {
		return tenant, subject, "reviewer", nil
	}),
		context.Background(), http.MethodPost, "/v1/invitations/"+id+"/accept", `{"display_name":"Ada Obi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %q: %v", rec.Body, err)
	}
	gt, _ := got["tenant"].(map[string]any)
	gu, _ := got["user"].(map[string]any)
	if strings.Join(keysOf(got), ",") != "tenant,user" ||
		strings.Join(keysOf(gt), ",") != "id,kind,name" || strings.Join(keysOf(gu), ",") != "id,role" {
		t.Errorf("body = %s, want exactly {tenant:{id,name,kind}, user:{id,role}}", rec.Body)
	}
	if gt["id"] != tenant.ID || gt["name"] != "Obi Partners" || gt["kind"] != "firm" || gu["id"] != subject || gu["role"] != "reviewer" {
		t.Errorf("body = %s, want the stub's tenant, subject and role", rec.Body)
	}

	// Same bytes as the token accept for the same result.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/invitations/accept", AcceptInvitationHandler(func(context.Context, string, *string) (Tenant, string, string, error) {
		return tenant, subject, "reviewer", nil
	}, nil))
	tok := apiDo(mux, context.Background(), http.MethodPost, "/v1/invitations/accept", `{"token":"x"}`)
	if tok.Code != http.StatusOK || tok.Body.String() != rec.Body.String() {
		t.Errorf("token accept = %d %s, by id = %s; want the same body", tok.Code, tok.Body, rec.Body)
	}
}

func TestAcceptInvitationByIDHandler_EmptyBodyIsNoName(t *testing.T) {
	route := acceptRoutes()[1]
	for _, c := range []struct{ name, body, want string }{
		{"no body", ``, "<nil>"},
		{"empty object", `{}`, "<nil>"},
		// Control: the same route hands a sent name over, so the nil above is not a body left unread.
		{"a name", `{"display_name":" Ada Obi "}`, "v:Ada Obi"},
	} {
		t.Run(c.name, func(t *testing.T) {
			spy := &nameSpy{}
			rec := apiDo(route.serve(spy, nil), context.Background(), http.MethodPost, route.path, c.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
			}
			if got := gotName(t, spy); got != c.want {
				t.Errorf("accept got name %q, want %q", got, c.want)
			}
		})
	}
}

func TestAcceptInvitationByIDHandler_MalformedBody(t *testing.T) {
	route := acceptRoutes()[1]
	for _, body := range []string{`{`, `{"display_name":`, `[]`, `"Ada Obi"`, `{not json`} {
		t.Run(body, func(t *testing.T) {
			spy := &nameSpy{}
			rec := apiDo(route.serve(spy, nil), context.Background(), http.MethodPost, route.path, body)
			assertErrorBody(t, rec, http.StatusBadRequest, "invalid request body")
			if len(spy.names) != 0 {
				t.Errorf("accept called %d time(s), want 0", len(spy.names))
			}
		})
	}
}

func TestInvitationsMineHandler_Shape(t *testing.T) {
	exp := time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)
	item := PendingInvite{ID: uuid.NewString(), Workspace: "Eze Ltd", Role: "preparer", ExpiresAt: exp}
	mux := func(m MyInvitations) http.Handler {
		h := http.NewServeMux()
		h.HandleFunc("GET /v1/invitations/mine", InvitationsMineHandler(func(context.Context) (MyInvitations, error) { return m, nil }, nil))
		return h
	}
	for _, c := range []struct {
		name string
		in   MyInvitations
		want string
	}{
		{"removed with no invites", MyInvitations{Invitations: []PendingInvite{}, Removed: true}, `{"invitations":[],"removed":true}`},
		{"not removed with no invites", MyInvitations{Invitations: []PendingInvite{}}, `{"invitations":[],"removed":false}`},
		{"removed with an invite", MyInvitations{Invitations: []PendingInvite{item}, Removed: true},
			`{"invitations":[{"id":"` + item.ID + `","workspace":"Eze Ltd","role":"preparer","inviter":null,"expires_at":"2026-10-14T09:30:00Z"}],"removed":true}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := apiDo(mux(c.in), context.Background(), http.MethodGet, "/v1/invitations/mine", "")
			if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != c.want {
				t.Errorf("answer = %d %s, want 200 %s", rec.Code, rec.Body, c.want)
			}
		})
	}

	rec := apiDo(func() http.Handler {
		h := http.NewServeMux()
		h.HandleFunc("GET /v1/invitations/mine", InvitationsMineHandler(func(context.Context) (MyInvitations, error) {
			return MyInvitations{}, ErrAlreadyMember
		}, nil))
		return h
	}(), context.Background(), http.MethodGet, "/v1/invitations/mine", "")
	assertErrorBody(t, rec, http.StatusConflict, "you already belong to a workspace")
}
