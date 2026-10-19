package portfolio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

func TestClientScope_EntityReadsStopAtTheSet(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenant := seedTenant(t, super, "portfolio client scope")

	var entA, entB string
	for name, dst := range map[string]*string{"Client A": &entA, "Client B": &entB} {
		if err := super.QueryRow(ctx,
			`INSERT INTO business_entities (tenant_id, name) VALUES ($1, $2) RETURNING id`, tenant, name).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	member := func(role string, allClients bool, clients ...string) context.Context {
		user := uuid.NewString()
		if _, err := super.Exec(ctx,
			`INSERT INTO memberships (tenant_id, user_id, role, status, all_clients) VALUES ($1, $2, $3, 'active', $4)`,
			tenant, user, role, allClients); err != nil {
			t.Fatalf("seed %s: %v", role, err)
		}
		for _, e := range clients {
			if _, err := super.Exec(ctx,
				`INSERT INTO membership_clients (tenant_id, user_id, entity_id) VALUES ($1, $2, $3)`, tenant, user, e); err != nil {
				t.Fatalf("seed membership_clients: %v", err)
			}
		}
		return auth.WithIdentity(ctx, auth.Identity{Subject: user, Role: "authenticated", TenantID: tenant})
	}
	limited := member("reviewer", false, entA)
	unassigned := member("preparer", false)
	admin := member("admin", false)
	allClients := member("preparer", true)

	store := NewStore(app)
	if _, err := store.GetByID(limited, entB); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByID(client B) as a reviewer limited to A: err = %v, want ErrNotFound", err)
	}
	if got, err := store.GetByID(limited, entA); err != nil || got.ID != entA {
		t.Errorf("GetByID(client A) control: %v, %v", got.ID, err)
	}

	for _, c := range []struct {
		name  string
		ctx   context.Context
		wantN int
		first string
	}{
		{"limited reviewer", limited, 1, entA},
		{"unassigned preparer", unassigned, 0, ""},
		{"admin", admin, 2, ""},
		{"all-clients preparer", allClients, 2, ""},
	} {
		items, total, err := store.List(c.ctx, ListFilter{Limit: 50})
		if err != nil || total != c.wantN || len(items) != c.wantN {
			t.Errorf("%s List = %d items, total %d, err %v, want %d", c.name, len(items), total, err, c.wantN)
			continue
		}
		if c.first != "" && items[0].ID != c.first {
			t.Errorf("%s List[0] = %s, want %s", c.name, items[0].ID, c.first)
		}
	}

	// The route answers the same 404 body for a client outside the set, an unknown id and another tenant's.
	otherTenant := seedTenant(t, super, "portfolio client scope foreign")
	var foreign string
	if err := super.QueryRow(ctx,
		`INSERT INTO business_entities (tenant_id, name) VALUES ($1, 'Foreign') RETURNING id`, otherTenant).Scan(&foreign); err != nil {
		t.Fatalf("seed foreign entity: %v", err)
	}
	get := GetHandler(store.GetByID, nil)
	serve := func(c context.Context, id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/v1/entities/"+id, nil).WithContext(c)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		get(w, r)
		return w
	}
	outside := serve(limited, entB)
	if outside.Code != http.StatusNotFound {
		t.Fatalf("GET client B as a reviewer limited to A: %d %s, want 404", outside.Code, outside.Body.String())
	}
	for who, id := range map[string]string{"unknown": uuid.NewString(), "another tenant's": foreign} {
		if got := serve(limited, id); got.Code != outside.Code || got.Body.String() != outside.Body.String() {
			t.Errorf("GET %s id: %d %s, differs from outside-the-set %d %s", who, got.Code, got.Body.String(), outside.Code, outside.Body.String())
		}
	}
	if w := serve(limited, entA); w.Code != http.StatusOK {
		t.Errorf("GET client A control: %d, want 200", w.Code)
	}
	if w := serve(admin, entB); w.Code != http.StatusOK {
		t.Errorf("GET client B as admin: %d, want 200", w.Code)
	}

	lw := httptest.NewRecorder()
	ListHandler(store.List, nil)(lw, httptest.NewRequest(http.MethodGet, "/v1/entities", nil).WithContext(limited))
	var page struct {
		Entities   []struct{ ID string }
		Pagination struct{ Total int }
	}
	if err := json.Unmarshal(lw.Body.Bytes(), &page); err != nil || lw.Code != http.StatusOK ||
		len(page.Entities) != 1 || page.Entities[0].ID != entA || page.Pagination.Total != 1 {
		t.Errorf("GET /v1/entities as a limited reviewer: %d %s, want client A alone and total 1", lw.Code, lw.Body.String())
	}
}
