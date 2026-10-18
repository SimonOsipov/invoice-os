package portfolio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const changeDenied = "only an admin can change a client"

var nonAdminRoles = []string{"preparer", "reviewer"}

func entityField(t *testing.T, super *pgxpool.Pool, id, col string) string {
	t.Helper()
	var v string
	if err := super.QueryRow(context.Background(),
		"SELECT "+col+"::text FROM business_entities WHERE id = $1", id,
	).Scan(&v); err != nil {
		t.Fatalf("read %s: %v", col, err)
	}
	return v
}

func TestStoreUpdate_NonAdminRefusedInBothModes(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	const event = "portfolio.entity.updated"
	for _, kind := range createByRoleKinds {
		for _, role := range nonAdminRoles {
			t.Run(kind+"/"+role, func(t *testing.T) {
				tenantID, c := seedCaller(t, super, kind, role, "active")
				id := seedEntity(t, super, tenantID, "Before Co", nil)
				before := auditCount(t, app, tenantID, event)

				_, err := store.Update(c, id, UpdateInput{Name: strPtr("After Co")})
				if !errors.Is(err, ErrNotPermitted) {
					t.Fatalf("Update as %s/%s err = %v, want ErrNotPermitted", kind, role, err)
				}
				if got := entityField(t, super, id, "name"); got != "Before Co" {
					t.Errorf("name = %q, want unchanged %q", got, "Before Co")
				}
				if after := auditCount(t, app, tenantID, event); after != before {
					t.Errorf("audit rows for %s = %d, want unchanged %d", event, after, before)
				}
			})
		}
	}
}

func TestStoreUpdate_NonAdminInputNeverReachesValidation(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenantID, c := seedCaller(t, super, "firm", "preparer", "active")
	id := seedEntity(t, super, tenantID, "Before Co", nil)
	for _, tc := range []struct {
		name string
		id   string
		in   UpdateInput
	}{
		{"empty input", id, UpdateInput{}},
		{"malformed TIN", id, UpdateInput{TIN: strPtr("abc")}},
		{"unknown id", uuid.NewString(), UpdateInput{Name: strPtr("Any Co")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.Update(c, tc.id, tc.in)
			if !errors.Is(err, ErrNotPermitted) {
				t.Fatalf("Update err = %v, want ErrNotPermitted", err)
			}
			for _, other := range []error{ErrValidation, ErrInvalidTIN, ErrNotFound} {
				if errors.Is(err, other) {
					t.Errorf("non-admin reached %v, want the role refusal alone", other)
				}
			}
		})
	}
}

func TestStoreUpdate_AdminEmptyInputStillValidation(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenantID, c := seedCaller(t, super, "firm", "admin", "active")
	id := seedEntity(t, super, tenantID, "Before Co", nil)

	_, err := store.Update(c, id, UpdateInput{})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("admin Update with empty input err = %v, want ErrValidation", err)
	}
}

func TestStoreSetStatus_NonAdminRefused(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	for _, kind := range createByRoleKinds {
		for _, role := range nonAdminRoles {
			for _, tc := range []struct{ target, from, event string }{
				{"archived", "active", "portfolio.entity.offboarded"},
				{"active", "archived", "portfolio.entity.onboarded"},
			} {
				t.Run(kind+"/"+role+"/"+tc.target, func(t *testing.T) {
					tenantID, c := seedCaller(t, super, kind, role, "active")
					id := seedEntityStatus(t, super, tenantID, "Status Co", nil, tc.from)
					before := auditCount(t, app, tenantID, tc.event)

					_, err := store.SetStatus(c, id, tc.target)
					if !errors.Is(err, ErrNotPermitted) {
						t.Fatalf("SetStatus(%s) as %s/%s err = %v, want ErrNotPermitted", tc.target, kind, role, err)
					}
					if got := entityField(t, super, id, "status"); got != tc.from {
						t.Errorf("status = %q, want unchanged %q", got, tc.from)
					}
					if after := auditCount(t, app, tenantID, tc.event); after != before {
						t.Errorf("audit rows for %s = %d, want unchanged %d", tc.event, after, before)
					}
				})
			}
		}
	}
}

func TestStoreSetStatus_NonAdminSeesNoRowState(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenantID, c := seedCaller(t, super, "firm", "preparer", "active")
	id := seedEntity(t, super, tenantID, "Active Co", nil)
	for _, tc := range []struct{ name, id string }{
		{"redundant transition", id},
		{"unknown id", uuid.NewString()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.SetStatus(c, tc.id, "active")
			if !errors.Is(err, ErrNotPermitted) {
				t.Fatalf("SetStatus err = %v, want ErrNotPermitted", err)
			}
			if errors.Is(err, ErrRedundantTransition) || errors.Is(err, ErrNotFound) {
				t.Errorf("non-admin saw row state (%v), want the role refusal alone", err)
			}
		})
	}
}

func TestStoreChange_DemotedAdminRefused(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenantID, c := seedCaller(t, super, "firm", "admin", "active")
	id := seedEntity(t, super, tenantID, "Demote Co", nil)
	if _, err := super.Exec(context.Background(),
		`UPDATE memberships SET role = 'preparer' WHERE tenant_id = $1 AND user_id = $2`, tenantID, memberSubject,
	); err != nil {
		t.Fatalf("demote caller: %v", err)
	}

	if _, err := store.Update(c, id, UpdateInput{Name: strPtr("After Co")}); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("Update after demotion err = %v, want ErrNotPermitted", err)
	}
	if _, err := store.SetStatus(c, id, "archived"); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("SetStatus after demotion err = %v, want ErrNotPermitted", err)
	}
}

// A suspended admin is stopped by the seam, not by requireAdmin.
func TestStoreChange_SuspendedAdminRefusedBySeam(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	tenantID, c := seedCaller(t, super, "firm", "admin", "suspended")
	id := seedEntity(t, super, tenantID, "Suspended Co", nil)

	_, upErr := store.Update(c, id, UpdateInput{Name: strPtr("After Co")})
	_, stErr := store.SetStatus(c, id, "archived")
	for name, err := range map[string]error{"Update": upErr, "SetStatus": stErr} {
		if !errors.Is(err, db.ErrNotActiveMember) {
			t.Errorf("%s as suspended admin err = %v, want db.ErrNotActiveMember", name, err)
		}
		if errors.Is(err, ErrNotPermitted) {
			t.Errorf("%s: suspended admin got ErrNotPermitted, want the seam's refusal alone", name)
		}
	}
}

// Roles are per tenant: admin in one workspace grants nothing in another.
func TestStoreChange_AdminElsewhereIsRefusedHere(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	_, _ = seedCaller(t, super, "firm", "admin", "active")
	preparerTenant, preparerCtx := seedCaller(t, super, "in_house", "preparer", "active")
	id := seedEntity(t, super, preparerTenant, "Here Co", nil)

	if _, err := store.Update(preparerCtx, id, UpdateInput{Name: strPtr("After Co")}); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("Update where the caller is preparer err = %v, want ErrNotPermitted", err)
	}
	if _, err := store.SetStatus(preparerCtx, id, "archived"); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("SetStatus where the caller is preparer err = %v, want ErrNotPermitted", err)
	}
	if got := entityField(t, super, id, "name"); got != "Here Co" {
		t.Errorf("name = %q, want unchanged", got)
	}
}

func TestStoreChange_AdminSucceedsInBothModes(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	for _, kind := range createByRoleKinds {
		t.Run(kind, func(t *testing.T) {
			tenantID, c := seedCaller(t, super, kind, "admin", "active")
			id := seedEntity(t, super, tenantID, "Before Co", nil)

			got, err := store.Update(c, id, UpdateInput{Name: strPtr("After Co")})
			if err != nil || got.Name != "After Co" {
				t.Fatalf("Update as %s admin = %q, %v; want After Co", kind, got.Name, err)
			}
			if e, err := store.SetStatus(c, id, "archived"); err != nil || e.Status != "archived" {
				t.Fatalf("SetStatus(archived) as %s admin = %q, %v", kind, e.Status, err)
			}
			if e, err := store.SetStatus(c, id, "active"); err != nil || e.Status != "active" {
				t.Fatalf("SetStatus(active) as %s admin = %q, %v", kind, e.Status, err)
			}
		})
	}
}

func TestStatusForChangeErr(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		msg    string
	}{
		{"not permitted bare", ErrNotPermitted, http.StatusForbidden, changeDenied},
		{"not permitted wrapped", fmt.Errorf("x: %w", ErrNotPermitted), http.StatusForbidden, changeDenied},
		{"not found", ErrNotFound, http.StatusNotFound, "not found"},
		{"redundant transition", ErrRedundantTransition, http.StatusConflict, "redundant transition"},
		{"not active member", db.ErrNotActiveMember, http.StatusForbidden, db.NotActiveMemberMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, msg := statusForChangeErr(tc.err)
			if status != tc.status || msg != tc.msg {
				t.Errorf("statusForChangeErr = %d %q, want %d %q", status, msg, tc.status, tc.msg)
			}
		})
	}
}

func serveChange(h http.HandlerFunc, method, path, id string, body []byte, ident auth.Identity) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.SetPathValue("id", id)
	r = r.WithContext(auth.WithIdentity(r.Context(), ident))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestChangeHandlers_NotPermitted403(t *testing.T) {
	denied := func(context.Context, string) (Entity, error) { return Entity{}, ErrNotPermitted }
	update := func(context.Context, string, UpdateInput) (Entity, error) { return Entity{}, ErrNotPermitted }
	ident := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		path string
		body string
	}{
		{"update", UpdateHandler(update, nil), "/v1/entities/x", `{"name":"New Co"}`},
		{"offboard", OffboardHandler(denied, nil), "/v1/entities/x/offboard", ""},
		{"onboard", OnboardHandler(denied, nil), "/v1/entities/x/onboard", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveChange(tc.h, "POST", tc.path, "x", []byte(tc.body), ident)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode %q: %v", rec.Body.String(), err)
			}
			if len(body) != 1 || body["error"] != changeDenied {
				t.Errorf("body = %v, want exactly {error: %q}", body, changeDenied)
			}
		})
	}
}

func TestChangeHandlers_RealStoreByRole(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)
	offboard := func(ctx context.Context, id string) (Entity, error) { return store.SetStatus(ctx, id, "archived") }
	onboard := func(ctx context.Context, id string) (Entity, error) { return store.SetStatus(ctx, id, "active") }
	for _, role := range []string{"admin", "preparer", "reviewer"} {
		want := http.StatusForbidden
		if role == "admin" {
			want = http.StatusOK
		}
		for _, tc := range []struct {
			name, from, path, body string
			h                      http.HandlerFunc
		}{
			{"update", "active", "/v1/entities/", `{"name":"New Co"}`, UpdateHandler(store.Update, nil)},
			{"offboard", "active", "/v1/entities/", "", OffboardHandler(offboard, nil)},
			{"onboard", "archived", "/v1/entities/", "", OnboardHandler(onboard, nil)},
		} {
			t.Run(role+"/"+tc.name, func(t *testing.T) {
				tenantID, c := seedCaller(t, super, "in_house", role, "active")
				ident, _ := auth.IdentityFromContext(c)
				id := seedEntityStatus(t, super, tenantID, "Real Co", nil, tc.from)

				rec := serveChange(tc.h, "POST", tc.path+id, id, []byte(tc.body), ident)
				if rec.Code != want {
					t.Fatalf("%s as %s = %d, want %d (body=%s)", tc.name, role, rec.Code, want, rec.Body.String())
				}
				if want == http.StatusForbidden && !bytes.Contains(rec.Body.Bytes(), []byte(changeDenied)) {
					t.Errorf("body = %s, want %q", rec.Body.String(), changeDenied)
				}
			})
		}
	}
}
