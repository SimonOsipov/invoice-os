package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- handler: body edges ----------------------------------------------------

func TestProvision_Adversarial400(t *testing.T) {
	spy := &provisionSpy{fn: func(in ProvisionInput) (Tenant, string, error) {
		return Tenant{ID: uuid.NewString(), Name: in.WorkspaceName, Kind: "firm"}, uuid.NewString(), nil
	}}
	multi201 := strings.Repeat("é", 201)
	for _, tc := range []struct{ name, body string }{
		{"kind explicit empty", `{"workspace_name":"Acme","display_name":"Ada","kind":""}`},
		{"kind FIRM", `{"workspace_name":"Acme","display_name":"Ada","kind":"FIRM"}`},
		{"kind In_House", `{"workspace_name":"Acme","display_name":"Ada","kind":"In_House"}`},
		{"kind padded", `{"workspace_name":"Acme","display_name":"Ada","kind":" firm"}`},
		{"kind number", `{"workspace_name":"Acme","display_name":"Ada","kind":1}`},
		{"workspace_name unicode whitespace only", `{"workspace_name":" 　 \n","display_name":"Ada"}`},
		{"display_name unicode whitespace only", `{"workspace_name":"Acme","display_name":"  "}`},
		{"workspace_name 201 two-byte runes", `{"workspace_name":"` + multi201 + `","display_name":"Ada"}`},
		{"display_name 201 two-byte runes", `{"workspace_name":"Acme","display_name":"` + multi201 + `"}`},
		{"workspace_name not a string", `{"workspace_name":["Acme"],"display_name":"Ada"}`},
		// The decoder keeps going after a type error, so the valid duplicate would pass validation.
		{"type error then a valid duplicate", `{"workspace_name":1,"workspace_name":"Acme","display_name":"Ada"}`},
		{"empty body", ``},
		{"JSON null", `null`},
		{"over the 4 KiB cap", `{"workspace_name":"Acme","display_name":"Ada","pad":"` + strings.Repeat("x", 5000) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy.calls = nil
			rec, body := doProvision(t, spy.provision, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
			provisionErrorBody(t, body)
			if len(spy.calls) != 0 {
				t.Errorf("provision called %d time(s), want 0", len(spy.calls))
			}
		})
	}
}

func TestProvision_AdversarialAccepted(t *testing.T) {
	multi200 := strings.Repeat("é", 200) // 400 bytes: the cap counts runes, not bytes
	for _, tc := range []struct {
		name, body string
		want       ProvisionInput
	}{
		{"200 two-byte runes", `{"workspace_name":"` + multi200 + `","display_name":"` + multi200 + `"}`,
			ProvisionInput{WorkspaceName: multi200, DisplayName: multi200}},
		{"unicode whitespace trimmed", `{"workspace_name":"　Acme ","display_name":" Ada\n"}`,
			ProvisionInput{WorkspaceName: "Acme", DisplayName: "Ada"}},
		// Unknown fields are ignored, as every tenancy/portfolio decoder does; none reaches the store.
		{"unknown fields ignored", `{"workspace_name":"Acme","display_name":"Ada","kind":"firm","role":"owner","tenant_id":"` + uuid.NewString() + `","email":"x@evil.test"}`,
			ProvisionInput{WorkspaceName: "Acme", DisplayName: "Ada", Kind: "firm"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &provisionSpy{fn: func(in ProvisionInput) (Tenant, string, error) {
				return Tenant{ID: uuid.NewString(), Name: in.WorkspaceName, Kind: "firm"}, uuid.NewString(), nil
			}}
			rec, _ := doProvision(t, spy.provision, tc.body)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
			}
			if len(spy.calls) != 1 || spy.calls[0] != tc.want {
				t.Errorf("provision calls = %+v, want exactly [%+v]", spy.calls, tc.want)
			}
		})
	}
}

func TestProvision_Conflict409Message(t *testing.T) {
	spy := &provisionSpy{fn: func(ProvisionInput) (Tenant, string, error) { return Tenant{}, "", ErrAlreadyProvisioned }}
	_, body := doProvision(t, spy.provision, validProvisionBody)
	if msg := provisionErrorBody(t, body); msg != "this account already has a workspace" {
		t.Errorf("error = %q, want %q", msg, "this account already has a workspace")
	}
}

// --- store + handler against the database ------------------------------------

// postProvision runs POST /v1/workspaces through the real store with ctx as the request context.
func postProvision(store *Store, ctx context.Context, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/v1/workspaces", strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	ProvisionHandler(store.ProvisionWorkspace, nil).ServeHTTP(rec, r)
	return rec
}

func TestProvisionHandler_RealStore201(t *testing.T) {
	r := newRegistrant(t)
	rec := postProvision(NewStore(r.app), r.ctx(), `{"workspace_name":" Real Works ","display_name":"Ada","kind":"in_house"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	var me meBody
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if me.Tenant.ID != r.tenantID || me.Tenant.Name != "Real Works" || me.Tenant.Kind != "in_house" {
		t.Errorf("tenant = %+v, want {%s Real Works in_house}", me.Tenant, r.tenantID)
	}
	_, members := provisionedRows(t, r.super, r.tenantID)
	if len(members) != 1 {
		t.Fatalf("memberships = %+v, want exactly one", members)
	}
	if me.User.ID != members[0].UserID || me.User.Role != "admin" {
		t.Errorf("user = %+v, want {%s admin} (the stored membership)", me.User, members[0].UserID)
	}
}

func TestProvisionHandler_ConcurrentDoubleSubmitIsOne201AndOne409(t *testing.T) {
	r := newRegistrant(t)
	store := NewStore(r.app)
	codes := make([]int, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes[i] = postProvision(store, r.ctx(), `{"workspace_name":"Race Works","display_name":"Ada"}`).Code
		}()
	}
	close(start)
	wg.Wait()

	created, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflict++
		}
	}
	if created != 1 || conflict != 1 {
		t.Errorf("statuses = %v, want exactly one 201 and one 409", codes)
	}
	tenants, members := provisionedRows(t, r.super, r.tenantID)
	if len(tenants) != 1 || len(members) != 1 {
		t.Errorf("tenants = %v, memberships = %+v, want one of each", tenants, members)
	}
}

func TestStoreProvisionWorkspace_IdentityWinsOverTenantlessCaller(t *testing.T) {
	r := newRegistrant(t)
	id := r.id
	id.TenantID = uuid.NewString()
	ctx := auth.WithIdentity(r.ctx(), id) // both keys present

	_, _, err := NewStore(r.app).ProvisionWorkspace(ctx, ProvisionInput{WorkspaceName: "Both Keys", DisplayName: "Ada"})
	if !errors.Is(err, ErrAlreadyProvisioned) {
		t.Errorf("err = %v, want ErrAlreadyProvisioned", err)
	}
	if tenants, _ := provisionedRows(t, r.super, r.tenantID); len(tenants) != 0 {
		t.Errorf("tenants at uuidv5(subject) = %v, want none", tenants)
	}
}

// A provisioned workspace is visible to invoice_app only under its own GUC.
func TestStoreProvisionWorkspace_RowsAreTenantIsolated(t *testing.T) {
	a, b := newRegistrant(t), newRegistrant(t)
	store := NewStore(a.app)
	for _, r := range []registrant{a, b} {
		if _, _, err := store.ProvisionWorkspace(r.ctx(), ProvisionInput{WorkspaceName: "Iso " + r.id.Subject[:8], DisplayName: "Ada"}); err != nil {
			t.Fatalf("provision %s: %v", r.id.Subject, err)
		}
	}
	ctx := context.Background()
	visible := func(guc string) (tenants, members []string) {
		t.Helper()
		if err := db.WithinTenantTx(ctx, a.app, guc, func(tx pgx.Tx) error {
			for q, dst := range map[string]*[]string{
				`SELECT id::text FROM tenants WHERE id IN ($1, $2)`:                 &tenants,
				`SELECT user_id::text FROM memberships WHERE tenant_id IN ($1, $2)`: &members,
			} {
				rows, err := tx.Query(ctx, q, a.tenantID, b.tenantID)
				if err != nil {
					return err
				}
				v, err := pgx.CollectRows(rows, pgx.RowTo[string])
				if err != nil {
					return err
				}
				*dst = v
			}
			return nil
		}); err != nil {
			t.Fatalf("read under %s: %v", guc, err)
		}
		return tenants, members
	}
	tenants, members := visible(a.tenantID)
	if len(tenants) != 1 || tenants[0] != a.tenantID || len(members) != 1 || members[0] != a.id.Subject {
		t.Errorf("under A's GUC: tenants %v, members %v, want only A's", tenants, members)
	}
	tenants, members = visible(uuid.NewString())
	if len(tenants) != 0 || len(members) != 0 {
		t.Errorf("under an unrelated GUC: tenants %v, members %v, want none", tenants, members)
	}

	// A's identity pointed at B's workspace has no membership there.
	cross := auth.WithIdentity(ctx, auth.Identity{Subject: a.id.Subject, Role: "authenticated", TenantID: b.tenantID})
	if _, _, err := store.Me(cross); !errors.Is(err, ErrNoMembership) {
		t.Errorf("Me(A in B) err = %v, want ErrNoMembership", err)
	}
	own := auth.WithIdentity(ctx, auth.Identity{Subject: a.id.Subject, Role: "authenticated", TenantID: a.tenantID})
	list, err := store.ListMemberships(own)
	if err != nil {
		t.Fatalf("ListMemberships(A): %v", err)
	}
	if len(list) != 1 || list[0].UserID != a.id.Subject {
		t.Errorf("ListMemberships(A) = %+v, want only A", list)
	}
}
