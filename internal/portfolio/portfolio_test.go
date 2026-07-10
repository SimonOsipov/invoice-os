package portfolio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- handler tests (httptest + stubbed store closures, no DB) ------------------------

// createRequest mirrors the POST /v1/entities wire body (snake_case JSON
// tags, per the story's Request/response DTO convention).
type createRequest struct {
	Name         string  `json:"name"`
	TIN          string  `json:"tin"`
	Registration *string `json:"registration,omitempty"`
	Sector       *string `json:"sector,omitempty"`
	Address      *string `json:"address,omitempty"`
}

// entityBody mirrors the Entity JSON so handler tests can assert the
// contract, plus an Error field for the {"error":msg} envelope.
type entityBody struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	TIN          *string   `json:"tin"`
	Registration *string   `json:"registration"`
	Sector       *string   `json:"sector"`
	Address      *string   `json:"address"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	Error        string    `json:"error"`
}

func doCreate(t *testing.T, create func(ctx context.Context, in CreateInput) (Entity, error), id *auth.Identity, body createRequest) (*httptest.ResponseRecorder, entityBody) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	r := httptest.NewRequest("POST", "/v1/entities", bytes.NewReader(b))
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	CreateHandler(create, nil).ServeHTTP(rec, r)
	var resp entityBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

func doGet(t *testing.T, get func(ctx context.Context, id string) (Entity, error), id *auth.Identity, entityID string) (*httptest.ResponseRecorder, entityBody) {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/entities/"+entityID, nil)
	r.SetPathValue("id", entityID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	GetHandler(get, nil).ServeHTTP(rec, r)
	var resp entityBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

// TestCreateHandler_201 (task-38 Test Specs, AC1): a valid body with identity
// present must produce 201, with the returned body's id set and
// status=="active".
func TestCreateHandler_201(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	const tin = "1234567897"
	want := Entity{ID: uuid.NewString(), Name: "Acme Ltd", TIN: strPtr(tin), Status: "active", CreatedAt: time.Now()}
	create := func(ctx context.Context, in CreateInput) (Entity, error) {
		if in.Name != "Acme Ltd" || in.TIN != tin {
			t.Fatalf("create called with unexpected input: %+v", in)
		}
		return want, nil
	}
	rec, body := doCreate(t, create, &id, createRequest{Name: "Acme Ltd", TIN: tin})

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if body.ID != want.ID {
		t.Errorf("id = %q, want %q (id set from the created entity)", body.ID, want.ID)
	}
	if body.Status != "active" {
		t.Errorf("status = %q, want %q", body.Status, "active")
	}
}

// TestCreateHandler_InvalidTIN400 (AC2): a body with a bad TIN must produce
// 400 with a non-empty error message.
func TestCreateHandler_InvalidTIN400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	create := func(ctx context.Context, in CreateInput) (Entity, error) {
		return Entity{}, fmt.Errorf("%w: tin checksum is invalid", ErrInvalidTIN)
	}
	rec, body := doCreate(t, create, &id, createRequest{Name: "Acme Ltd", TIN: "1234567890"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestCreateHandler_MissingName400 (AC1): a body without a name must 400
// before create ever runs -- asserted by failing the test if create is
// called.
func TestCreateHandler_MissingName400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	create := func(ctx context.Context, in CreateInput) (Entity, error) {
		t.Fatal("create must not run when name is missing")
		return Entity{}, nil
	}
	rec, body := doCreate(t, create, &id, createRequest{TIN: "1234567897"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestCreateHandler_WhitespaceName400 (CodeRabbit review, PR #33): a
// whitespace-only name (e.g. "   ") must 400 before create ever runs --
// asserted by failing the test if create is called. strings.TrimSpace
// treats "   " as blank, same as "".
func TestCreateHandler_WhitespaceName400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	create := func(ctx context.Context, in CreateInput) (Entity, error) {
		t.Fatal("create must not run when name is whitespace-only")
		return Entity{}, nil
	}
	rec, body := doCreate(t, create, &id, createRequest{Name: "   ", TIN: "1234567897"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body error=%q)", rec.Code, body.Error)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestCreateHandler_TrimsName (CodeRabbit review, PR #33): a name with
// leading/trailing whitespace must reach the store trimmed, so
// "  Acme Ltd  " persists as "Acme Ltd".
func TestCreateHandler_TrimsName(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	const tin = "1234567897"
	want := Entity{ID: uuid.NewString(), Name: "Acme Ltd", TIN: strPtr(tin), Status: "active", CreatedAt: time.Now()}
	var gotName string
	create := func(ctx context.Context, in CreateInput) (Entity, error) {
		gotName = in.Name
		return want, nil
	}
	rec, _ := doCreate(t, create, &id, createRequest{Name: "  Acme Ltd  ", TIN: tin})

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if gotName != "Acme Ltd" {
		t.Errorf("CreateInput.Name = %q, want trimmed %q", gotName, "Acme Ltd")
	}
}

// TestCreateHandler_DuplicateTIN409 (AC4): the store returning ErrDuplicateTIN
// must map to 409 with a non-empty error message.
func TestCreateHandler_DuplicateTIN409(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	create := func(ctx context.Context, in CreateInput) (Entity, error) {
		return Entity{}, ErrDuplicateTIN
	}
	rec, body := doCreate(t, create, &id, createRequest{Name: "Acme Ltd", TIN: "1234567897"})

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestCreateHandler_NoIdentity401 (AC6): no identity in the request context
// must 401 before create ever runs -- asserted by failing the test if create
// is called.
func TestCreateHandler_NoIdentity401(t *testing.T) {
	create := func(ctx context.Context, in CreateInput) (Entity, error) {
		t.Fatal("create must not run without an identity")
		return Entity{}, nil
	}
	rec, body := doCreate(t, create, nil, createRequest{Name: "Acme Ltd", TIN: "1234567897"})

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity in context", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestGetHandler_200 (AC5): a get resolving an entity must produce 200 with
// the entity's id in the body.
func TestGetHandler_200(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	entityID := uuid.NewString()
	want := Entity{ID: entityID, Name: "Acme Ltd", TIN: strPtr("1234567897"), Status: "active", CreatedAt: time.Now()}
	get := func(ctx context.Context, gotID string) (Entity, error) {
		if gotID != entityID {
			t.Fatalf("get called with id = %q, want %q", gotID, entityID)
		}
		return want, nil
	}
	rec, body := doGet(t, get, &id, entityID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body.ID != entityID {
		t.Errorf("id = %q, want %q", body.ID, entityID)
	}
}

// TestGetHandler_404 (AC5): the store returning ErrNotFound must map to 404
// with a non-empty error message -- the shape a cross-tenant id resolves to.
func TestGetHandler_404(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	get := func(ctx context.Context, gotID string) (Entity, error) {
		return Entity{}, ErrNotFound
	}
	rec, body := doGet(t, get, &id, uuid.NewString())

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestGetHandler_NoIdentity401 (AC6): no identity in the request context must
// 401 before get ever runs -- asserted by failing the test if get is called.
func TestGetHandler_NoIdentity401(t *testing.T) {
	get := func(ctx context.Context, gotID string) (Entity, error) {
		t.Fatal("get must not run without an identity")
		return Entity{}, nil
	}
	rec, body := doGet(t, get, nil, uuid.NewString())

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity in context", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// --- List handler tests (task-36, M3-03-03) -------------------------------------------

// doList issues a GET /v1/entities request (optionally with a raw query
// string, e.g. "?status=pending") through ListHandler.
func doList(t *testing.T, list func(ctx context.Context, f ListFilter) ([]Entity, int, error), id *auth.Identity, query string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/entities"+query, nil)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	ListHandler(list, nil).ServeHTTP(rec, r)
	return rec
}

// TestListHandler_EmptyState200 (task-36 Test Specs, AC-5): the store
// returning ([]Entity{}, 0, nil) must produce 200 with the RAW response body
// containing "entities":[] (never "entities":null) and "total":0.
//
// QA mutation probe (Mode B): confirmed this reddens for the right reason --
// forcing ListHandler to emit a nil Entities slice fails the raw-JSON
// "entities":[] assertion below (portfolio_test.go:276), not an
// import/collection error.
func TestListHandler_EmptyState200(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	called := false
	list := func(ctx context.Context, f ListFilter) ([]Entity, int, error) {
		called = true
		return []Entity{}, 0, nil
	}
	rec := doList(t, list, &id, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (list called=%v, body=%s)", rec.Code, called, rec.Body.String())
	}
	if !called {
		t.Error("store.List was not called")
	}
	body := rec.Body.Bytes()
	if !bytes.Contains(body, []byte(`"entities":[]`)) {
		t.Errorf("body = %s, want raw JSON to contain \"entities\":[] (not null)", body)
	}
	if !bytes.Contains(body, []byte(`"total":0`)) {
		t.Errorf("body = %s, want raw JSON to contain \"total\":0", body)
	}
}

// TestListHandler_BadStatus400 (AC-2/4): an invalid ?status= value (anything
// other than "active"/"archived") must 400 with a non-empty error, and must
// never call the store -- validation happens in the handler before List
// runs.
func TestListHandler_BadStatus400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	list := func(ctx context.Context, f ListFilter) ([]Entity, int, error) {
		t.Fatal("store.List must not run when status is invalid")
		return nil, 0, nil
	}
	rec := doList(t, list, &id, "?status=pending")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body["error"] == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestListHandler_LimitDefaultAndClamp (AC-4): the ListFilter the handler
// passes to the store must default an omitted ?limit= to 50, and clamp an
// over-large ?limit=500 down to 200.
func TestListHandler_LimitDefaultAndClamp(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		wantLimit int
	}{
		{"omitted defaults to 50", "", 50},
		{"500 clamps to 200", "?limit=500", 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
			var captured ListFilter
			called := false
			list := func(ctx context.Context, f ListFilter) ([]Entity, int, error) {
				called = true
				captured = f
				return []Entity{}, 0, nil
			}
			rec := doList(t, list, &id, tc.query)
			if !called {
				t.Fatalf("store.List was not called (status=%d, body=%s)", rec.Code, rec.Body.String())
			}
			if captured.Limit != tc.wantLimit {
				t.Errorf("captured ListFilter.Limit = %d, want %d", captured.Limit, tc.wantLimit)
			}
		})
	}
}

// TestListHandler_NoIdentity401 (established pattern, same as Create/Get):
// no identity in the request context must 401 before list ever runs --
// asserted by failing the test if list is called.
func TestListHandler_NoIdentity401(t *testing.T) {
	list := func(ctx context.Context, f ListFilter) ([]Entity, int, error) {
		t.Fatal("store.List must not run without an identity")
		return nil, 0, nil
	}
	rec := doList(t, list, nil, "")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity in context (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body["error"] == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// --- Update/Offboard/Onboard handler tests (task-37, M3-03-04) -----------------------

// doUpdate issues a PATCH /v1/entities/{id} request with a raw JSON body
// (raw, not marshaled from a struct, so callers can pass literal "{}" for the
// empty-body test) through UpdateHandler.
func doUpdate(t *testing.T, update func(ctx context.Context, id string, in UpdateInput) (Entity, error), id *auth.Identity, entityID, rawBody string) (*httptest.ResponseRecorder, entityBody) {
	t.Helper()
	r := httptest.NewRequest("PATCH", "/v1/entities/"+entityID, strings.NewReader(rawBody))
	r.SetPathValue("id", entityID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	UpdateHandler(update, nil).ServeHTTP(rec, r)
	var resp entityBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

// doOffboard issues a POST /v1/entities/{id}/offboard request through
// OffboardHandler.
func doOffboard(t *testing.T, setStatus func(ctx context.Context, id string) (Entity, error), id *auth.Identity, entityID string) (*httptest.ResponseRecorder, entityBody) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/entities/"+entityID+"/offboard", nil)
	r.SetPathValue("id", entityID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	OffboardHandler(setStatus, nil).ServeHTTP(rec, r)
	var resp entityBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

// doOnboard issues a POST /v1/entities/{id}/onboard request through
// OnboardHandler.
func doOnboard(t *testing.T, setStatus func(ctx context.Context, id string) (Entity, error), id *auth.Identity, entityID string) (*httptest.ResponseRecorder, entityBody) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/entities/"+entityID+"/onboard", nil)
	r.SetPathValue("id", entityID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	OnboardHandler(setStatus, nil).ServeHTTP(rec, r)
	var resp entityBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

// TestUpdateHandler_200 (task-37 Test Specs, AC-1): a stubbed store
// returning an updated Entity must produce 200 with the response reflecting
// the updated fields.
func TestUpdateHandler_200(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	entityID := uuid.NewString()
	want := Entity{ID: entityID, Name: "New Name", TIN: strPtr("1234567897"), Status: "active", CreatedAt: time.Now()}
	update := func(ctx context.Context, gotID string, in UpdateInput) (Entity, error) {
		if gotID != entityID {
			t.Fatalf("update called with id = %q, want %q", gotID, entityID)
		}
		if in.Name == nil || *in.Name != "New Name" {
			t.Fatalf("update called with unexpected input: %+v", in)
		}
		return want, nil
	}
	rec, body := doUpdate(t, update, &id, entityID, `{"name":"New Name"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Name != "New Name" {
		t.Errorf("name = %q, want %q", body.Name, "New Name")
	}
}

// TestUpdateHandler_InvalidTIN400 (AC-1): the store returning ErrInvalidTIN
// (a changed TIN failing re-validation) must map to 400 with a non-empty
// error message.
func TestUpdateHandler_InvalidTIN400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	entityID := uuid.NewString()
	update := func(ctx context.Context, gotID string, in UpdateInput) (Entity, error) {
		return Entity{}, fmt.Errorf("%w: tin checksum is invalid", ErrInvalidTIN)
	}
	// "1234567890" is format-valid (10 digits) but Luhn-fails -- see
	// tin_test.go TestValidateTIN_ChecksumRejected.
	rec, body := doUpdate(t, update, &id, entityID, `{"tin":"1234567890"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestUpdateHandler_DuplicateTIN409 (AC-1): the store returning
// ErrDuplicateTIN must map to 409.
func TestUpdateHandler_DuplicateTIN409(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	entityID := uuid.NewString()
	update := func(ctx context.Context, gotID string, in UpdateInput) (Entity, error) {
		return Entity{}, ErrDuplicateTIN
	}
	rec, body := doUpdate(t, update, &id, entityID, `{"tin":"123456780006"}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestUpdateHandler_EmptyBody400 (AC-7): a PATCH body of "{}" (no fields to
// update) must 400 before the store is ever called -- asserted by failing
// the test if update is called.
func TestUpdateHandler_EmptyBody400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	entityID := uuid.NewString()
	update := func(ctx context.Context, gotID string, in UpdateInput) (Entity, error) {
		t.Fatal("update must not run when the PATCH body has no fields to update")
		return Entity{}, nil
	}
	rec, body := doUpdate(t, update, &id, entityID, `{}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestUpdateHandler_WhitespaceName400 (CodeRabbit review, PR #33): a
// whitespace-only name (e.g. "  ") in the PATCH body must 400 before update
// ever runs -- asserted by failing the test if update is called.
func TestUpdateHandler_WhitespaceName400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	entityID := uuid.NewString()
	update := func(ctx context.Context, gotID string, in UpdateInput) (Entity, error) {
		t.Fatal("update must not run when name is whitespace-only")
		return Entity{}, nil
	}
	rec, body := doUpdate(t, update, &id, entityID, `{"name":"  "}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestUpdateHandler_NotFound404 (AC-6): the store returning ErrNotFound must
// map to 404.
func TestUpdateHandler_NotFound404(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	entityID := uuid.NewString()
	update := func(ctx context.Context, gotID string, in UpdateInput) (Entity, error) {
		return Entity{}, ErrNotFound
	}
	rec, body := doUpdate(t, update, &id, entityID, `{"name":"New Name"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestUpdateHandler_NoIdentity401: no identity in the request context must
// 401 before update ever runs -- asserted by failing the test if update is
// called.
func TestUpdateHandler_NoIdentity401(t *testing.T) {
	entityID := uuid.NewString()
	update := func(ctx context.Context, gotID string, in UpdateInput) (Entity, error) {
		t.Fatal("update must not run without an identity")
		return Entity{}, nil
	}
	rec, body := doUpdate(t, update, nil, entityID, `{"name":"New Name"}`)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity in context (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestOffboardHandler_200: a stubbed setStatus returning an archived Entity
// must produce 200 with status=="archived" in the body.
func TestOffboardHandler_200(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	entityID := uuid.NewString()
	want := Entity{ID: entityID, Name: "Acme Ltd", Status: "archived", CreatedAt: time.Now()}
	setStatus := func(ctx context.Context, gotID string) (Entity, error) {
		if gotID != entityID {
			t.Fatalf("setStatus called with id = %q, want %q", gotID, entityID)
		}
		return want, nil
	}
	rec, body := doOffboard(t, setStatus, &id, entityID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Status != "archived" {
		t.Errorf("status = %q, want %q", body.Status, "archived")
	}
}

// TestOffboardHandler_Redundant409: the stubbed setStatus returning
// ErrRedundantTransition (already archived) must map to 409.
func TestOffboardHandler_Redundant409(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	entityID := uuid.NewString()
	setStatus := func(ctx context.Context, gotID string) (Entity, error) {
		return Entity{}, ErrRedundantTransition
	}
	rec, body := doOffboard(t, setStatus, &id, entityID)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestOffboardHandler_NotFound404: the stubbed setStatus returning
// ErrNotFound must map to 404.
func TestOffboardHandler_NotFound404(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	entityID := uuid.NewString()
	setStatus := func(ctx context.Context, gotID string) (Entity, error) {
		return Entity{}, ErrNotFound
	}
	rec, body := doOffboard(t, setStatus, &id, entityID)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestOnboardHandler_200: a stubbed setStatus returning an active Entity must
// produce 200 with status=="active" in the body.
func TestOnboardHandler_200(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	entityID := uuid.NewString()
	want := Entity{ID: entityID, Name: "Acme Ltd", Status: "active", CreatedAt: time.Now()}
	setStatus := func(ctx context.Context, gotID string) (Entity, error) {
		if gotID != entityID {
			t.Fatalf("setStatus called with id = %q, want %q", gotID, entityID)
		}
		return want, nil
	}
	rec, body := doOnboard(t, setStatus, &id, entityID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Status != "active" {
		t.Errorf("status = %q, want %q", body.Status, "active")
	}
}

// TestOnboardHandler_Redundant409: the stubbed setStatus returning
// ErrRedundantTransition (already active) must map to 409.
func TestOnboardHandler_Redundant409(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	entityID := uuid.NewString()
	setStatus := func(ctx context.Context, gotID string) (Entity, error) {
		return Entity{}, ErrRedundantTransition
	}
	rec, body := doOnboard(t, setStatus, &id, entityID)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestOnboardHandler_NotFound404: the stubbed setStatus returning
// ErrNotFound must map to 404.
func TestOnboardHandler_NotFound404(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	entityID := uuid.NewString()
	setStatus := func(ctx context.Context, gotID string) (Entity, error) {
		return Entity{}, ErrNotFound
	}
	rec, body := doOnboard(t, setStatus, &id, entityID)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

func strPtr(s string) *string { return &s }

// --- DB-backed store tests (real Postgres, dbTestPools) ------------------------------

// dbTestPools returns the superuser (seed) and app-role (Store) pools for the
// portfolio db-integration suite below, or skips the test when the per-role
// DSNs are unset -- copied verbatim from internal/tenancy/tenancy_test.go
// (~line 155), the same env gate `make test-rls` and TestCurrentTenant_RoundTrip
// use (DATABASE_URL for invoice_app, DATABASE_SUPERUSER_URL for seeding as the
// BYPASSRLS superuser).
func dbTestPools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	appURL := os.Getenv("DATABASE_URL")
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("portfolio db-integration test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
	}
	ctx := context.Background()

	s, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	// Registered before the app pool's Cleanup, so per LIFO ordering it closes
	// AFTER app's pool -- and callers that register a row-delete Cleanup of
	// their own (after calling dbTestPools) get it run BEFORE either pool
	// closes.
	t.Cleanup(s.Close)
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping superuser (is the DB up and bootstrapped?): %v", err)
	}

	a, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	t.Cleanup(a.Close)

	return s, a
}

// seedEntity inserts one business_entities row for tenantID as the superuser
// (BYPASSRLS, so seeding needs no tenant context) and registers its own
// cleanup. tin may be nil (the column is nullable).
func seedEntity(t *testing.T, super *pgxpool.Pool, tenantID, name string, tin *string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO business_entities (tenant_id, name, tin) VALUES ($1, $2, $3) RETURNING id`,
		tenantID, name, tin,
	).Scan(&id); err != nil {
		t.Fatalf("seed business_entities: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, id)
	})
	return id
}

// auditCount counts audit_log rows for tenantID+event, scoped via
// db.WithinTenantTx (FORCE RLS) -- mirrors internal/audit/audit_test.go's
// auditCount helper.
func auditCount(t *testing.T, pool *pgxpool.Pool, tenantID, event string) int {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE event = $1`, event).Scan(&n)
	}); err != nil {
		t.Fatalf("count audit_log: %v", err)
	}
	return n
}

// auditActor returns the actor of the most recent audit_log row for
// tenantID+event.
func auditActor(t *testing.T, pool *pgxpool.Pool, tenantID, event string) string {
	t.Helper()
	ctx := context.Background()
	var actor string
	if err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT actor FROM audit_log WHERE event = $1 ORDER BY created_at DESC LIMIT 1`, event,
		).Scan(&actor)
	}); err != nil {
		t.Fatalf("read audit_log actor: %v", err)
	}
	return actor
}

// auditEventsForEntity returns the (event, actor) pairs for every audit_log
// row whose JSON payload has "id" == entityID, ordered by the row's bigserial
// id (== commit order) -- used by the full lifecycle round-trip test (QA,
// task-37) to prove the audit trail is complete AND ordered, not just
// individually present per event.
func auditEventsForEntity(t *testing.T, pool *pgxpool.Pool, tenantID, entityID string) (events, actors []string) {
	t.Helper()
	ctx := context.Background()
	if err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT event, actor FROM audit_log WHERE payload->>'id' = $1 ORDER BY id ASC`, entityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event, actor string
			if err := rows.Scan(&event, &actor); err != nil {
				return err
			}
			events = append(events, event)
			actors = append(actors, actor)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("query audit_log for entity %s: %v", entityID, err)
	}
	return events, actors
}

// TestStoreCreate_PersistsAndAudits (task-38 Test Specs, AC1/AC3): a Create
// with valid input, as tenant A's identity, must (a) persist a row visible to
// A via GetByID with status "active", and (b) write exactly one
// "portfolio.entity.created" audit_log row, actor == the caller's subject, in
// the same tenant.
func TestStoreCreate_PersistsAndAudits(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	userID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio create-test tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID})

	const event = "portfolio.entity.created"
	before := auditCount(t, app, tenantID, event)

	const tin = "1234567897"
	entity, err := store.Create(c, CreateInput{Name: "Acme Ltd", TIN: tin})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if entity.ID == "" {
		t.Fatal("Create: entity.ID is empty, want a generated id")
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, entity.ID)
	})
	if entity.Status != "active" {
		t.Errorf("Create: status = %q, want %q", entity.Status, "active")
	}
	if entity.TIN == nil || *entity.TIN != tin {
		t.Errorf("Create: tin = %v, want %q", entity.TIN, tin)
	}

	got, err := store.GetByID(c, entity.ID)
	if err != nil {
		t.Fatalf("GetByID after Create: %v", err)
	}
	if got.ID != entity.ID || got.Status != "active" {
		t.Errorf("GetByID after Create = %+v, want id=%s status=active", got, entity.ID)
	}

	after := auditCount(t, app, tenantID, event)
	if after != before+1 {
		t.Fatalf("audit_log rows for %s = %d, want %d (exactly one new row)", event, after, before+1)
	}
	if actor := auditActor(t, app, tenantID, event); actor != userID {
		t.Errorf("audit actor = %q, want %q", actor, userID)
	}
}

// TestStoreCreate_FailedCreateWritesNoAudit (task-38 Test Specs, AC3/AC4):
// tenant A already has a row with TIN X; Create-ing another row with the same
// canonical TIN must fail with ErrDuplicateTIN AND leave the audit_log count
// unchanged -- proving the insert and the audit write share one transaction
// (the failed insert's audit.Record call, if any, rolled back too).
func TestStoreCreate_FailedCreateWritesNoAudit(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	userID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio dup-tin-test tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	const tin = "1234567897"
	seedEntity(t, super, tenantID, "Existing Co", strPtr(tin))

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID})

	const event = "portfolio.entity.created"
	before := auditCount(t, app, tenantID, event)

	_, err := store.Create(c, CreateInput{Name: "Second Co", TIN: tin})
	if !errors.Is(err, ErrDuplicateTIN) {
		t.Fatalf("Create with duplicate tin err = %v, want ErrDuplicateTIN", err)
	}

	after := auditCount(t, app, tenantID, event)
	if after != before {
		t.Errorf("audit_log rows for %s = %d, want unchanged %d (failed create must write no audit row)", event, after, before)
	}
}

// TestStoreGetByID_OwnTenant (task-38 Test Specs, AC5): an entity seeded in
// tenant A must be returned by GetByID when called as A.
func TestStoreGetByID_OwnTenant(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	userID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio getbyid-test tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	const tin = "1234567897"
	entityID := seedEntity(t, super, tenantID, "Acme Ltd", strPtr(tin))

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID})

	got, err := store.GetByID(c, entityID)
	if err != nil {
		t.Fatalf("GetByID(own tenant): %v", err)
	}
	if got.ID != entityID || got.Name != "Acme Ltd" || got.Status != "active" {
		t.Errorf("GetByID = %+v, want id=%s name=Acme Ltd status=active", got, entityID)
	}
}

// TestStoreGetByID_CrossTenantNotFound (task-38 Test Specs, AC5): an entity
// seeded in tenant B must resolve to ErrNotFound when GetByID is called as
// tenant A -- the service-layer half of cross-tenant read refusal (RLS makes
// B's row invisible to A's tx; the store must translate that absence to
// ErrNotFound, not a bare pgx.ErrNoRows).
func TestStoreGetByID_CrossTenantNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio cross-tenant A', 'firm'), ($2, 'portfolio cross-tenant B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})

	entityIDInB := seedEntity(t, super, tenantB, "B Corp", nil)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	_, err := store.GetByID(c, entityIDInB)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetByID(cross-tenant id) err = %v, want ErrNotFound", err)
	}
}

// --- QA-added adversarial / edge-case tests (task-38 Part 2, QA Verify) --------------

// TestStoreCreate_TINUniquenessIsPerTenantNotGlobal (critical correctness): the
// duplicate-TIN unique index is (tenant_id, tin) -- a PARTIAL index scoped to
// tenant_id, not a global uniqueness constraint. The SAME canonical TIN
// created in TWO DIFFERENT tenants must NOT collide -- both Creates must
// succeed, each producing its own row under its own tenant. (Contrast
// TestStoreCreate_FailedCreateWritesNoAudit, which proves the 409 case: a
// duplicate TIN WITHIN one tenant.)
func TestStoreCreate_TINUniquenessIsPerTenantNotGlobal(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio per-tenant-tin A', 'firm'), ($2, 'portfolio per-tenant-tin B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})

	store := NewStore(app)
	const tin = "1234567897"

	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})
	entityA, err := store.Create(cA, CreateInput{Name: "Tenant A Co", TIN: tin})
	if err != nil {
		t.Fatalf("Create in tenant A: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, entityA.ID)
	})

	cB := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB})
	entityB, err := store.Create(cB, CreateInput{Name: "Tenant B Co", TIN: tin})
	if err != nil {
		t.Fatalf("Create in tenant B with the SAME canonical TIN as tenant A: %v (want success -- the unique index is per-tenant, not global)", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, entityB.ID)
	})

	if entityA.ID == entityB.ID {
		t.Fatalf("entityA.ID == entityB.ID (%s), want two distinct rows", entityA.ID)
	}

	gotA, err := store.GetByID(cA, entityA.ID)
	if err != nil || gotA.TIN == nil || *gotA.TIN != tin {
		t.Fatalf("GetByID(A) = %+v, err=%v, want tin=%q under tenant A", gotA, err, tin)
	}
	gotB, err := store.GetByID(cB, entityB.ID)
	if err != nil || gotB.TIN == nil || *gotB.TIN != tin {
		t.Fatalf("GetByID(B) = %+v, err=%v, want tin=%q under tenant B", gotB, err, tin)
	}
}

// TestStoreCreate_NullableOptionalFieldsRoundTrip (edge case): Registration,
// Sector, and Address are optional (*string, nullable columns). Omitted (nil)
// on Create must persist as SQL NULL and round-trip through GetByID as a nil
// *string -- NOT an empty string. When provided, the values must round-trip
// unchanged.
func TestStoreCreate_NullableOptionalFieldsRoundTrip(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio nullable-fields tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	// Omitted -> nil -> persisted as SQL NULL -> nil *string on read-back.
	omitted, err := store.Create(c, CreateInput{Name: "Nil Fields Co", TIN: "1234567897"})
	if err != nil {
		t.Fatalf("Create (omitted optional fields): %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, omitted.ID)
	})
	if omitted.Registration != nil || omitted.Sector != nil || omitted.Address != nil {
		t.Errorf("Create (omitted): registration=%v sector=%v address=%v, want all nil",
			omitted.Registration, omitted.Sector, omitted.Address)
	}
	got, err := store.GetByID(c, omitted.ID)
	if err != nil {
		t.Fatalf("GetByID (omitted): %v", err)
	}
	if got.Registration != nil || got.Sector != nil || got.Address != nil {
		t.Errorf("GetByID (omitted): registration=%v sector=%v address=%v, want all nil (SQL NULL, not empty string)",
			got.Registration, got.Sector, got.Address)
	}

	// Set -> round-trips the values unchanged.
	reg, sec, addr := "RC-12345", "Manufacturing", "12 Marina Rd, Lagos"
	set, err := store.Create(c, CreateInput{Name: "Set Fields Co", TIN: "123456780006", Registration: &reg, Sector: &sec, Address: &addr})
	if err != nil {
		t.Fatalf("Create (set optional fields): %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, set.ID)
	})
	if set.Registration == nil || *set.Registration != reg || set.Sector == nil || *set.Sector != sec || set.Address == nil || *set.Address != addr {
		t.Errorf("Create (set): registration=%v sector=%v address=%v, want %q/%q/%q",
			set.Registration, set.Sector, set.Address, reg, sec, addr)
	}
	got2, err := store.GetByID(c, set.ID)
	if err != nil {
		t.Fatalf("GetByID (set): %v", err)
	}
	if got2.Registration == nil || *got2.Registration != reg || got2.Sector == nil || *got2.Sector != sec || got2.Address == nil || *got2.Address != addr {
		t.Errorf("GetByID (set): registration=%v sector=%v address=%v, want %q/%q/%q",
			got2.Registration, got2.Sector, got2.Address, reg, sec, addr)
	}
}

// TestStoreCreate_AuditRowIsTenantScoped (AC3, AC5): after a successful Create
// in tenant A, the portfolio.entity.created audit_log row must be visible
// under tenant A's GUC context with actor == the caller's Subject, and must
// NOT be visible under a different tenant B's GUC context -- audit_log's own
// FORCE-RLS tenant isolation applies to this event just as it does to any
// other row.
func TestStoreCreate_AuditRowIsTenantScoped(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio audit-scope A', 'firm'), ($2, 'portfolio audit-scope B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})

	store := NewStore(app)
	userID := uuid.NewString()
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantA})

	const event = "portfolio.entity.created"
	entity, err := store.Create(cA, CreateInput{Name: "Audit Scope Co", TIN: "1234567897"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, entity.ID)
	})

	if n := auditCount(t, app, tenantA, event); n != 1 {
		t.Fatalf("audit_log rows for %s under tenant A = %d, want 1", event, n)
	}
	if actor := auditActor(t, app, tenantA, event); actor != userID {
		t.Errorf("audit actor under tenant A = %q, want %q", actor, userID)
	}
	if n := auditCount(t, app, tenantB, event); n != 0 {
		t.Errorf("audit_log rows for %s under tenant B = %d, want 0 (tenant A's audit row must not be visible to tenant B)", event, n)
	}
}

// TestStoreGetByID_UnknownIDNotFound (AC5): a well-formed uuid that does not
// exist in ANY tenant (not merely a different tenant, per
// TestStoreGetByID_CrossTenantNotFound) must resolve to ErrNotFound.
func TestStoreGetByID_UnknownIDNotFound(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()})

	_, err := store.GetByID(c, uuid.NewString())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetByID(unknown id) err = %v, want ErrNotFound", err)
	}
}

// TestStoreGetByID_MalformedIDNotUUID (AC5, edge case): a non-uuid id string
// does NOT map to ErrNotFound. Postgres rejects it client-side as invalid
// input syntax for type uuid (SQLSTATE 22P02) before any row-matching
// happens, so GetByID surfaces that raw error, which statusForErr's default
// case maps to 500 -- a conscious, documented behavior (not silently assumed)
// for a malformed path param, distinct from the 404 a well-formed-but-absent
// or cross-tenant id produces.
func TestStoreGetByID_MalformedIDNotUUID(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()})

	_, err := store.GetByID(c, "not-a-uuid")
	if err == nil {
		t.Fatal("GetByID(malformed id) err = nil, want a non-nil error")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("GetByID(malformed id) should NOT map to ErrNotFound -- it is a raw Postgres invalid-input error (22P02), which statusForErr maps to 500, not 404")
	}
}

// TestStoreCreate_InvalidTINRejectedAtStoreLayer (AC2, store layer): Store.Create
// itself -- not just the handler's decode/validation path -- must reject a
// Luhn-failing TIN via ValidateTIN before any INSERT runs: no business_entities
// row and no audit_log row.
func TestStoreCreate_InvalidTINRejectedAtStoreLayer(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio store-invalid-tin tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	// No business_entities cleanup registered: none is expected to exist (asserted
	// below), and the FK's ON DELETE CASCADE would remove any surprise row anyway
	// when this tenant is deleted.
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const event = "portfolio.entity.created"
	beforeAudit := auditCount(t, app, tenantID, event)
	var beforeRows int
	if err := super.QueryRow(ctx, `SELECT count(*) FROM business_entities WHERE tenant_id = $1`, tenantID).Scan(&beforeRows); err != nil {
		t.Fatalf("count business_entities before: %v", err)
	}

	// Format-valid (10 digits) but Luhn-failing -- see tin_test.go
	// TestValidateTIN_ChecksumRejected, which proves ValidateTIN itself rejects
	// "1234567890" on checksum grounds alone.
	_, err := store.Create(c, CreateInput{Name: "Bad TIN Co", TIN: "1234567890"})
	if !errors.Is(err, ErrInvalidTIN) {
		t.Fatalf("Create with Luhn-failing TIN err = %v, want ErrInvalidTIN", err)
	}

	var afterRows int
	if err := super.QueryRow(ctx, `SELECT count(*) FROM business_entities WHERE tenant_id = $1`, tenantID).Scan(&afterRows); err != nil {
		t.Fatalf("count business_entities after: %v", err)
	}
	if afterRows != beforeRows {
		t.Errorf("business_entities rows for tenant = %d, want unchanged %d (invalid TIN must write no row)", afterRows, beforeRows)
	}
	if after := auditCount(t, app, tenantID, event); after != beforeAudit {
		t.Errorf("audit_log rows for %s = %d, want unchanged %d (invalid TIN must write no audit row)", event, after, beforeAudit)
	}
}

// --- DB-backed Store.List tests (task-36, M3-03-03) -----------------------------------

// seedEntityStatus inserts one business_entities row for tenantID with an
// EXPLICIT status ("active"|"archived") -- seedEntity always takes the
// column DEFAULT ("active"), so List's status-filter tests need this sibling
// helper to seed archived rows too. Registers its own cleanup, same as
// seedEntity.
func seedEntityStatus(t *testing.T, super *pgxpool.Pool, tenantID, name string, tin *string, status string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO business_entities (tenant_id, name, tin, status) VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, name, tin, status,
	).Scan(&id); err != nil {
		t.Fatalf("seed business_entities (status=%s): %v", status, err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, id)
	})
	return id
}

// TestStoreList_OwnTenantOnly (task-36 Test Specs, AC-1): tenant A has 3
// entities, tenant B has 2 -- List as A must return exactly A's 3, none of
// B's. This is the service-layer half of cross-tenant isolation; table-level
// RLS for business_entities is already covered by
// internal/platform/db/business_entities_rls_test.go (M3-01) and is not
// re-tested here.
//
// QA mutation probe (Mode B): confirmed this reddens for the right reason --
// temporarily seeding tenant B's two rows under tenant A (simulating a
// cross-tenant leak) flips the total assertion to "total = 5, want 3", not
// an import/collection error.
func TestStoreList_OwnTenantOnly(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-own-tenant A', 'firm'), ($2, 'portfolio list-own-tenant B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})

	seedEntity(t, super, tenantA, "A Co One", nil)
	seedEntity(t, super, tenantA, "A Co Two", nil)
	seedEntity(t, super, tenantA, "A Co Three", nil)
	seedEntity(t, super, tenantB, "B Co One", nil)
	seedEntity(t, super, tenantB, "B Co Two", nil)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	items, total, err := store.List(c, ListFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3", len(items))
	}
	for _, e := range items {
		if strings.HasPrefix(e.Name, "B Co") {
			t.Errorf("List(as tenant A) returned tenant B's row: %+v", e)
		}
	}
}

// TestStoreList_StatusFilter (AC-2): tenant A has 2 active + 1 archived --
// List(status="archived") must return exactly the 1 archived row.
func TestStoreList_StatusFilter(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-status-filter tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	seedEntity(t, super, tenantID, "Active One", nil)
	seedEntity(t, super, tenantID, "Active Two", nil)
	archivedID := seedEntityStatus(t, super, tenantID, "Archived One", nil, "archived")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	status := "archived"
	items, total, err := store.List(c, ListFilter{Status: &status, Limit: 50})
	if err != nil {
		t.Fatalf("List(status=archived): %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total=%d len(items)=%d, want 1/1", total, len(items))
	}
	if items[0].ID != archivedID {
		t.Errorf("items[0].ID = %q, want %q (the archived row)", items[0].ID, archivedID)
	}
	if items[0].Status != "archived" {
		t.Errorf("items[0].Status = %q, want %q", items[0].Status, "archived")
	}
}

// TestStoreList_StatusOmittedReturnsBoth (AC-2): the same tenant/data shape
// as TestStoreList_StatusFilter (2 active + 1 archived) -- List(status=nil)
// must return all 3 rows.
func TestStoreList_StatusOmittedReturnsBoth(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-status-omitted tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	seedEntity(t, super, tenantID, "Active One", nil)
	seedEntity(t, super, tenantID, "Active Two", nil)
	seedEntityStatus(t, super, tenantID, "Archived One", nil, "archived")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List(status omitted): %v", err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("total=%d len(items)=%d, want 3/3", total, len(items))
	}
}

// TestStoreList_SearchQ (AC-3): tenant A has "Okafor Ltd" and "Lagos Foods"
// -- List(q="okaf") must match "Okafor Ltd" only, case-insensitive.
//
// QA mutation probe (Mode B): confirmed this reddens for the right reason --
// dropping the name ILIKE arm (leaving only the tin arm) flips this to
// "total=0 len(items)=0, want 1/1", not an import/collection error; the
// sibling TIN-arm mutation (see TestStoreList_SearchQMatchesTIN) leaves this
// test green, proving the two arms are independently load-bearing.
func TestStoreList_SearchQ(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-search-q tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	okaforID := seedEntity(t, super, tenantID, "Okafor Ltd", nil)
	seedEntity(t, super, tenantID, "Lagos Foods", nil)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Q: "okaf", Limit: 50})
	if err != nil {
		t.Fatalf("List(q=okaf): %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total=%d len(items)=%d, want 1/1", total, len(items))
	}
	if items[0].ID != okaforID {
		t.Errorf("items[0].ID = %q, want %q (Okafor Ltd)", items[0].ID, okaforID)
	}
}

// TestStoreList_SearchQMatchesTIN (AC-3): tenant A has an entity with the
// known canonical TIN "1234567897" (valid per ValidateTIN's Luhn check, and
// already used as a known-good TIN elsewhere in this file, e.g.
// TestCreateHandler_201) -- List(q="34567"), a substring of that TIN, must
// match it.
//
// QA mutation probe (Mode B): confirmed this reddens for the right reason --
// dropping the tin ILIKE arm (leaving only the name arm) flips this to
// "total=0 len(items)=0, want 1/1", not an import/collection error.
func TestStoreList_SearchQMatchesTIN(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-search-tin tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	const tin = "1234567897"
	entityID := seedEntity(t, super, tenantID, "Tin Match Co", strPtr(tin))
	seedEntity(t, super, tenantID, "No Tin Co", nil)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Q: "34567", Limit: 50})
	if err != nil {
		t.Fatalf("List(q=34567): %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total=%d len(items)=%d, want 1/1", total, len(items))
	}
	if items[0].ID != entityID {
		t.Errorf("items[0].ID = %q, want %q (the TIN-matching row)", items[0].ID, entityID)
	}
}

// TestStoreList_Pagination (AC-4): tenant A has 5 entities -- List(limit=2,
// offset=2) must return exactly 2 rows, the middle page in stable name ASC,
// id ASC order, with total=5 (the full filtered count, not the page size).
//
// QA mutation probe (Mode B): confirmed this reddens for the right reason --
// returning len(items) as total instead of the COUNT(*) flips this to
// "total = 2, want 5"; separately, dropping LIMIT/OFFSET from the SELECT
// flips it to "len(items) = 5, want 2" -- neither is an import/collection
// error.
func TestStoreList_Pagination(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-pagination tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	names := []string{"Co A", "Co B", "Co C", "Co D", "Co E"}
	for _, name := range names {
		seedEntity(t, super, tenantID, name, nil)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("List(limit=2,offset=2): %v", err)
	}
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	// name ASC lexically: Co A, Co B, Co C, Co D, Co E -- offset=2 skips A,B,
	// so the stable middle page is [Co C, Co D].
	if items[0].Name != "Co C" || items[1].Name != "Co D" {
		t.Errorf("items = [%q, %q], want [\"Co C\", \"Co D\"] (stable name ASC order, middle page)", items[0].Name, items[1].Name)
	}
}

// TestStoreList_EmptyTenant (AC-5): a fresh tenant with 0 rows -- List must
// return a non-nil empty slice, total 0, nil err (never a nil slice, which
// would serialize to JSON null instead of []).
func TestStoreList_EmptyTenant(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-empty-tenant tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List(empty tenant): %v", err)
	}
	if items == nil {
		t.Error("items = nil, want a non-nil empty slice (so JSON marshals [] not null)")
	}
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
}

// --- QA-added adversarial / edge-case tests (task-36 Part 2, QA Verify) ---------------

// TestStoreList_ReverseCrossTenantIsolation (QA-added, mirrors
// TestStoreList_OwnTenantOnly in the OTHER direction): tenant A has 3
// entities, tenant B has 2 -- List AS TENANT B must return exactly B's 2,
// none of A's. TestStoreList_OwnTenantOnly only proves A never sees B's rows;
// this proves the reverse also holds (isolation is not accidentally
// one-directional, e.g. from a filter that happens to exclude B's rows from
// A's result set for an unrelated reason).
func TestStoreList_ReverseCrossTenantIsolation(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-reverse-isolation A', 'firm'), ($2, 'portfolio list-reverse-isolation B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})

	seedEntity(t, super, tenantA, "A Co One", nil)
	seedEntity(t, super, tenantA, "A Co Two", nil)
	seedEntity(t, super, tenantA, "A Co Three", nil)
	seedEntity(t, super, tenantB, "B Co One", nil)
	seedEntity(t, super, tenantB, "B Co Two", nil)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB})

	items, total, err := store.List(c, ListFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List (as tenant B): %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	for _, e := range items {
		if strings.HasPrefix(e.Name, "A Co") {
			t.Errorf("List(as tenant B) returned tenant A's row: %+v", e)
		}
	}
}

// TestStoreList_CombinedStatusAndQFilter (QA-added, AC-2 + AC-3 combined):
// tenant A has an ACTIVE "Okafor Ltd" and an ARCHIVED "Okafor Foods" --
// List(status=active, q="okafor") must match ONLY the active row, proving
// the two filters combine with AND (both must hold), not OR.
func TestStoreList_CombinedStatusAndQFilter(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-combined-filter tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	activeID := seedEntity(t, super, tenantID, "Okafor Ltd", nil)
	seedEntityStatus(t, super, tenantID, "Okafor Foods", nil, "archived")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	status := "active"
	items, total, err := store.List(c, ListFilter{Status: &status, Q: "okafor", Limit: 50})
	if err != nil {
		t.Fatalf("List(status=active,q=okafor): %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total=%d len(items)=%d, want 1/1", total, len(items))
	}
	if items[0].ID != activeID {
		t.Errorf("items[0].ID = %q, want %q (the active Okafor Ltd row, not the archived Okafor Foods)", items[0].ID, activeID)
	}
}

// TestStoreList_SearchQNoMatch (QA-added, AC-3/AC-5 edge): a q that matches
// no row in the tenant must produce the same zero-result contract as an
// empty tenant (TestStoreList_EmptyTenant) -- a non-nil empty slice, total 0,
// nil err -- even though the tenant itself is non-empty.
func TestStoreList_SearchQNoMatch(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-search-nomatch tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	seedEntity(t, super, tenantID, "Lagos Foods", nil)
	seedEntity(t, super, tenantID, "Acme Ltd", nil)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Q: "zzz-no-such-substring", Limit: 50})
	if err != nil {
		t.Fatalf("List(q=no-match): %v", err)
	}
	if items == nil {
		t.Error("items = nil, want a non-nil empty slice")
	}
	if len(items) != 0 {
		t.Errorf("len(items) = %d, want 0", len(items))
	}
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
}

// TestStoreList_SearchQWildcardIsNotEscaped (QA-added -- documents ACTUAL
// behavior, not asserted as a bug): f.Q is always passed as a BOUND
// parameter ('%'||$n||'%'), never string-interpolated into the SQL text, so
// there is NO SQL-injection risk from q. However, Store.List does NOT
// LIKE-escape q's contents before binding it -- so a literal "%" or "_"
// TYPED BY THE USER into q is interpreted by Postgres as an ILIKE wildcard,
// not a literal character. q="%" here becomes the pattern '%'||'%'||'%' =
// '%%%', which ILIKE collapses to "match anything" -- List(q="%") matches
// EVERY row in the tenant, regardless of name/tin content.
//
// QA ruling: acceptable for this MVP/demo search box (M3-03-03, task-36).
// Nigerian business names/TINs containing a literal "%" or "_" are
// vanishingly rare, and the failure mode if one occurs is "search returns
// more rows than the literal string would suggest" -- not a security issue,
// not a cross-tenant leak, not data corruption. NOT bounced as a defect;
// flagged here for the human's awareness in case a future story wants
// stricter literal-search semantics (would require escaping "%", "_", and
// "\" in q before binding).
func TestStoreList_SearchQWildcardIsNotEscaped(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-search-wildcard tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	seedEntity(t, super, tenantID, "Okafor Ltd", nil)
	seedEntity(t, super, tenantID, "Lagos Foods", nil)
	seedEntity(t, super, tenantID, "Acme Traders", nil)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Q: "%", Limit: 50})
	if err != nil {
		t.Fatalf(`List(q="%%"): %v`, err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("total=%d len(items)=%d, want 3/3 (a literal %% in q is treated as an ILIKE wildcard here, matching every row in the tenant -- see comment above)", total, len(items))
	}
}

// TestStoreList_PaginationPastEnd (QA-added, AC-4 edge): tenant A has 3
// entities -- List(limit=10, offset=100), an offset past the end of the
// filtered set, must return an empty slice, but total must still reflect the
// full filtered count (3) -- proving total is computed independently of the
// LIMIT/OFFSET window, not derived from len(items).
func TestStoreList_PaginationPastEnd(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-pagination-past-end tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	seedEntity(t, super, tenantID, "Co A", nil)
	seedEntity(t, super, tenantID, "Co B", nil)
	seedEntity(t, super, tenantID, "Co C", nil)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Limit: 10, Offset: 100})
	if err != nil {
		t.Fatalf("List(offset past end): %v", err)
	}
	if items == nil {
		t.Error("items = nil, want a non-nil empty slice")
	}
	if len(items) != 0 {
		t.Errorf("len(items) = %d, want 0", len(items))
	}
	if total != 3 {
		t.Errorf("total = %d, want 3 (the full filtered count, independent of offset)", total)
	}
}

// TestStoreList_OrderingTieBreak (QA-added, AC-4 edge): two entities in the
// SAME tenant with the IDENTICAL name -- List's ORDER BY name ASC, id ASC
// must break the tie by id, and that order must be STABLE (identical)
// across two separate List calls -- proving id is a real, deterministic
// secondary sort key, not incidental ordering Postgres happens to produce
// once.
func TestStoreList_OrderingTieBreak(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-order-tiebreak tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	const dupName = "Tie Break Co"
	id1 := seedEntity(t, super, tenantID, dupName, nil)
	id2 := seedEntity(t, super, tenantID, dupName, nil)
	wantFirst, wantSecond := id1, id2
	if wantFirst > wantSecond {
		wantFirst, wantSecond = wantSecond, wantFirst
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items1, _, err := store.List(c, ListFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List (1st call): %v", err)
	}
	items2, _, err := store.List(c, ListFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List (2nd call): %v", err)
	}
	if len(items1) != 2 || len(items2) != 2 {
		t.Fatalf("len(items1)=%d len(items2)=%d, want 2/2", len(items1), len(items2))
	}
	if items1[0].ID != wantFirst || items1[1].ID != wantSecond {
		t.Errorf("items1 order = [%q, %q], want [%q, %q] (id ASC tie-break)", items1[0].ID, items1[1].ID, wantFirst, wantSecond)
	}
	if items1[0].ID != items2[0].ID || items1[1].ID != items2[1].ID {
		t.Errorf("order not stable across calls: 1st=[%q,%q] 2nd=[%q,%q]", items1[0].ID, items1[1].ID, items2[0].ID, items2[1].ID)
	}
}

// TestStoreList_LargeLimitReturnsAllRows (QA-added, AC-4 edge, store-level):
// a large limit (200, the handler's max clamp value) with FEWER rows than
// the limit in the tenant must return every row. This is the LIMIT boundary
// at the STORE layer against real Postgres -- distinct from
// TestListHandler_LimitDefaultAndClamp, which only asserts what ListFilter
// the handler CONSTRUCTS from a stubbed store and never proves Store.List
// actually honors a large limit end-to-end.
func TestStoreList_LargeLimitReturnsAllRows(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-large-limit tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	const rowCount = 7
	for i := 0; i < rowCount; i++ {
		seedEntity(t, super, tenantID, fmt.Sprintf("Co %02d", i), nil)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Limit: 200})
	if err != nil {
		t.Fatalf("List(limit=200): %v", err)
	}
	if total != rowCount || len(items) != rowCount {
		t.Fatalf("total=%d len(items)=%d, want %d/%d (limit=200 exceeds the tenant's row count, all rows returned)", total, len(items), rowCount, rowCount)
	}
}

// --- DB-backed Store.Update / Store.SetStatus tests (task-37, M3-03-04) --------------
//
// Two distinct Luhn-valid TINs are used throughout this section:
//   - tinX = "1234567897" (10-digit JTB TIN): from the rightmost digit (7,
//     undoubled), doubling every second digit leftward -- 9->9(18-9),
//     8->8, 7->5(14-9), 6->6, 5->1(10-9), 4->4, 3->6, 2->2, 1->2(2*1) --
//     sums to 50, a multiple of 10 (see tin_test.go's "10-digit JTB TIN"
//     case and TestCreateHandler_201, which already use it as a known-good
//     TIN).
//   - tinY = "123456780006" (12-digit FIRS TIN, canonical form of
//     "12345678-0006" -- see tin_test.go's "FIRS dash-formatted TIN" case):
//     the same Luhn walk over these digits sums to 40, also a multiple of
//     10, and distinct from tinX.

// TestStoreUpdate_PersistsAndAudits (AC-1): updating name+tin on a seeded
// tenant-A entity must persist both fields and write exactly one new
// portfolio.entity.updated audit_log row, actor == the caller's subject.
func TestStoreUpdate_PersistsAndAudits(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	userID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio update-persists tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	const tinX = "1234567897"
	entityID := seedEntity(t, super, tenantID, "Old Name", strPtr(tinX))

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID})

	const event = "portfolio.entity.updated"
	before := auditCount(t, app, tenantID, event)

	newName := "New Name"
	newTIN := "123456780006"
	updated, err := store.Update(c, entityID, UpdateInput{Name: &newName, TIN: &newTIN})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != newName {
		t.Errorf("Update: name = %q, want %q", updated.Name, newName)
	}
	if updated.TIN == nil || *updated.TIN != newTIN {
		t.Errorf("Update: tin = %v, want %q", updated.TIN, newTIN)
	}

	got, err := store.GetByID(c, entityID)
	if err != nil {
		t.Fatalf("GetByID after Update: %v", err)
	}
	if got.Name != newName || got.TIN == nil || *got.TIN != newTIN {
		t.Errorf("GetByID after Update = %+v, want name=%q tin=%q", got, newName, newTIN)
	}

	after := auditCount(t, app, tenantID, event)
	if after != before+1 {
		t.Fatalf("audit_log rows for %s = %d, want %d (exactly one new row)", event, after, before+1)
	}
	if actor := auditActor(t, app, tenantID, event); actor != userID {
		t.Errorf("audit actor = %q, want %q", actor, userID)
	}
}

// TestStoreUpdate_InvalidTINNoWrite (AC-1): updating with a Luhn-failing TIN
// must return ErrInvalidTIN, leave the row unchanged, and write no audit row.
func TestStoreUpdate_InvalidTINNoWrite(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio update-invalid-tin tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	const tinX = "1234567897"
	entityID := seedEntity(t, super, tenantID, "Untouched Co", strPtr(tinX))

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const event = "portfolio.entity.updated"
	before := auditCount(t, app, tenantID, event)

	// Format-valid (10 digits) but Luhn-fails -- see tin_test.go
	// TestValidateTIN_ChecksumRejected and
	// TestStoreCreate_InvalidTINRejectedAtStoreLayer.
	badTIN := "1234567890"
	_, err := store.Update(c, entityID, UpdateInput{TIN: &badTIN})
	if !errors.Is(err, ErrInvalidTIN) {
		t.Fatalf("Update with Luhn-failing tin err = %v, want ErrInvalidTIN", err)
	}

	got, err := store.GetByID(c, entityID)
	if err != nil {
		t.Fatalf("GetByID after failed Update: %v", err)
	}
	if got.Name != "Untouched Co" || got.TIN == nil || *got.TIN != tinX {
		t.Errorf("GetByID after failed Update = %+v, want unchanged name=Untouched Co tin=%q", got, tinX)
	}

	after := auditCount(t, app, tenantID, event)
	if after != before {
		t.Errorf("audit_log rows for %s = %d, want unchanged %d (invalid TIN must write no audit row)", event, after, before)
	}
}

// TestStoreUpdate_DuplicateTINConflict (AC-1): tenant A has entity1 (tinX)
// and entity2 (tinY) -- updating entity1's tin to entity2's must return
// ErrDuplicateTIN, leave entity1 unchanged, and write no audit row.
func TestStoreUpdate_DuplicateTINConflict(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio update-dup-tin tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	const tinX = "1234567897"
	const tinY = "123456780006"
	entity1ID := seedEntity(t, super, tenantID, "Entity One", strPtr(tinX))
	seedEntity(t, super, tenantID, "Entity Two", strPtr(tinY))

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const event = "portfolio.entity.updated"
	before := auditCount(t, app, tenantID, event)

	dupTIN := tinY
	_, err := store.Update(c, entity1ID, UpdateInput{TIN: &dupTIN})
	if !errors.Is(err, ErrDuplicateTIN) {
		t.Fatalf("Update(entity1, tin=entity2's tin) err = %v, want ErrDuplicateTIN", err)
	}

	got, err := store.GetByID(c, entity1ID)
	if err != nil {
		t.Fatalf("GetByID after failed Update: %v", err)
	}
	if got.TIN == nil || *got.TIN != tinX {
		t.Errorf("GetByID(entity1) after failed Update: tin = %v, want unchanged %q", got.TIN, tinX)
	}

	after := auditCount(t, app, tenantID, event)
	if after != before {
		t.Errorf("audit_log rows for %s = %d, want unchanged %d (duplicate-tin update must write no audit row)", event, after, before)
	}
}

// TestStoreUpdate_ArchivedEntityAllowed (AC-2): updating the name of an
// archived entity must succeed, leave status untouched (still "archived"),
// and write one updated audit row -- edit-while-archived per [A6].
func TestStoreUpdate_ArchivedEntityAllowed(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio update-archived tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	entityID := seedEntityStatus(t, super, tenantID, "Archived Co", nil, "archived")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const event = "portfolio.entity.updated"
	before := auditCount(t, app, tenantID, event)

	newName := "Renamed Archived Co"
	updated, err := store.Update(c, entityID, UpdateInput{Name: &newName})
	if err != nil {
		t.Fatalf("Update (archived entity): %v", err)
	}
	if updated.Name != newName {
		t.Errorf("Update: name = %q, want %q", updated.Name, newName)
	}
	if updated.Status != "archived" {
		t.Errorf("Update: status = %q, want still %q (edit-while-archived per [A6])", updated.Status, "archived")
	}

	got, err := store.GetByID(c, entityID)
	if err != nil {
		t.Fatalf("GetByID after Update: %v", err)
	}
	if got.Name != newName || got.Status != "archived" {
		t.Errorf("GetByID after Update = %+v, want name=%q status=archived", got, newName)
	}

	after := auditCount(t, app, tenantID, event)
	if after != before+1 {
		t.Fatalf("audit_log rows for %s = %d, want %d (exactly one new row)", event, after, before+1)
	}
}

// TestStoreUpdate_CrossTenantNotFound (AC-6): updating tenant B's entity id
// as tenant A must return ErrNotFound, leave B's row unchanged, and write no
// audit row under A.
func TestStoreUpdate_CrossTenantNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio update-cross-tenant A', 'firm'), ($2, 'portfolio update-cross-tenant B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})

	entityIDInB := seedEntity(t, super, tenantB, "B Corp", nil)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	const event = "portfolio.entity.updated"
	beforeA := auditCount(t, app, tenantA, event)

	newName := "Hijacked Name"
	_, err := store.Update(cA, entityIDInB, UpdateInput{Name: &newName})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update(B's id) as tenant A err = %v, want ErrNotFound", err)
	}

	cB := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB})
	got, err := store.GetByID(cB, entityIDInB)
	if err != nil {
		t.Fatalf("GetByID(B's entity, as B) after cross-tenant Update attempt: %v", err)
	}
	if got.Name != "B Corp" {
		t.Errorf("B's entity name = %q, want unchanged %q", got.Name, "B Corp")
	}

	afterA := auditCount(t, app, tenantA, event)
	if afterA != beforeA {
		t.Errorf("audit_log rows for %s under tenant A = %d, want unchanged %d (cross-tenant Update must write no audit row under A)", event, afterA, beforeA)
	}
}

// TestStoreUpdate_EmptyInputRejected (AC-7): Update with an all-nil
// UpdateInput must return ErrValidation before any UPDATE runs, leaving the
// row and audit_log untouched.
func TestStoreUpdate_EmptyInputRejected(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio update-empty-input tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	entityID := seedEntity(t, super, tenantID, "Untouched Co", nil)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const event = "portfolio.entity.updated"
	before := auditCount(t, app, tenantID, event)

	_, err := store.Update(c, entityID, UpdateInput{})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Update(all-nil UpdateInput) err = %v, want ErrValidation", err)
	}

	got, err := store.GetByID(c, entityID)
	if err != nil {
		t.Fatalf("GetByID after rejected Update: %v", err)
	}
	if got.Name != "Untouched Co" {
		t.Errorf("name = %q, want unchanged %q", got.Name, "Untouched Co")
	}

	after := auditCount(t, app, tenantID, event)
	if after != before {
		t.Errorf("audit_log rows for %s = %d, want unchanged %d (empty PATCH must write no audit row)", event, after, before)
	}
}

// TestStoreOffboard_ArchivesAndRetains (AC-3): SetStatus(id, "archived") on
// an active entity must archive it, the row must remain retrievable via
// GetByID (retained, not deleted), and exactly one new
// portfolio.entity.offboarded audit row must be written.
func TestStoreOffboard_ArchivesAndRetains(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	userID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio offboard-retains tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	entityID := seedEntity(t, super, tenantID, "Active Co", nil)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID})

	const event = "portfolio.entity.offboarded"
	before := auditCount(t, app, tenantID, event)

	updated, err := store.SetStatus(c, entityID, "archived")
	if err != nil {
		t.Fatalf("SetStatus(archived): %v", err)
	}
	if updated.Status != "archived" {
		t.Errorf("SetStatus(archived): status = %q, want %q", updated.Status, "archived")
	}

	got, err := store.GetByID(c, entityID)
	if err != nil {
		t.Fatalf("GetByID after offboard (row must be RETAINED, not deleted): %v", err)
	}
	if got.Status != "archived" {
		t.Errorf("GetByID after offboard: status = %q, want %q", got.Status, "archived")
	}

	after := auditCount(t, app, tenantID, event)
	if after != before+1 {
		t.Fatalf("audit_log rows for %s = %d, want %d (exactly one new row)", event, after, before+1)
	}
	if actor := auditActor(t, app, tenantID, event); actor != userID {
		t.Errorf("audit actor = %q, want %q", actor, userID)
	}
}

// TestStoreOnboard_Reactivates (AC-4): SetStatus(id, "active") on an
// archived entity must reactivate it and write one new
// portfolio.entity.onboarded audit row.
func TestStoreOnboard_Reactivates(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	userID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio onboard-reactivates tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	entityID := seedEntityStatus(t, super, tenantID, "Archived Co", nil, "archived")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID})

	const event = "portfolio.entity.onboarded"
	before := auditCount(t, app, tenantID, event)

	updated, err := store.SetStatus(c, entityID, "active")
	if err != nil {
		t.Fatalf("SetStatus(active): %v", err)
	}
	if updated.Status != "active" {
		t.Errorf("SetStatus(active): status = %q, want %q", updated.Status, "active")
	}

	after := auditCount(t, app, tenantID, event)
	if after != before+1 {
		t.Fatalf("audit_log rows for %s = %d, want %d (exactly one new row)", event, after, before+1)
	}
	if actor := auditActor(t, app, tenantID, event); actor != userID {
		t.Errorf("audit actor = %q, want %q", actor, userID)
	}
}

// TestStoreSetStatus_RedundantOffboard409 (AC-5): SetStatus(id, "archived")
// on an already-archived entity must return ErrRedundantTransition and write
// no new offboarded audit row.
func TestStoreSetStatus_RedundantOffboard409(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio redundant-offboard tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	entityID := seedEntityStatus(t, super, tenantID, "Already Archived Co", nil, "archived")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const event = "portfolio.entity.offboarded"
	before := auditCount(t, app, tenantID, event)

	_, err := store.SetStatus(c, entityID, "archived")
	if !errors.Is(err, ErrRedundantTransition) {
		t.Fatalf("SetStatus(archived) on already-archived entity err = %v, want ErrRedundantTransition", err)
	}

	after := auditCount(t, app, tenantID, event)
	if after != before {
		t.Errorf("audit_log rows for %s = %d, want unchanged %d (redundant offboard must write no audit row)", event, after, before)
	}
}

// TestStoreSetStatus_RedundantOnboard409 (AC-5): SetStatus(id, "active") on
// an already-active entity must return ErrRedundantTransition and write no
// new onboarded audit row.
func TestStoreSetStatus_RedundantOnboard409(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio redundant-onboard tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	entityID := seedEntity(t, super, tenantID, "Already Active Co", nil)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const event = "portfolio.entity.onboarded"
	before := auditCount(t, app, tenantID, event)

	_, err := store.SetStatus(c, entityID, "active")
	if !errors.Is(err, ErrRedundantTransition) {
		t.Fatalf("SetStatus(active) on already-active entity err = %v, want ErrRedundantTransition", err)
	}

	after := auditCount(t, app, tenantID, event)
	if after != before {
		t.Errorf("audit_log rows for %s = %d, want unchanged %d (redundant onboard must write no audit row)", event, after, before)
	}
}

// TestStoreSetStatus_CrossTenantNotFound (AC-6): SetStatus on tenant B's
// entity id as tenant A must return ErrNotFound, leave B's row unaffected,
// and write no audit row under A.
func TestStoreSetStatus_CrossTenantNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio setstatus-cross-tenant A', 'firm'), ($2, 'portfolio setstatus-cross-tenant B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})

	entityIDInB := seedEntity(t, super, tenantB, "B Corp", nil)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	const event = "portfolio.entity.offboarded"
	beforeA := auditCount(t, app, tenantA, event)

	_, err := store.SetStatus(cA, entityIDInB, "archived")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetStatus(B's id) as tenant A err = %v, want ErrNotFound", err)
	}

	cB := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB})
	got, err := store.GetByID(cB, entityIDInB)
	if err != nil {
		t.Fatalf("GetByID(B's entity, as B) after cross-tenant SetStatus attempt: %v", err)
	}
	if got.Status != "active" {
		t.Errorf("B's entity status = %q, want unchanged %q", got.Status, "active")
	}

	afterA := auditCount(t, app, tenantA, event)
	if afterA != beforeA {
		t.Errorf("audit_log rows for %s under tenant A = %d, want unchanged %d (cross-tenant SetStatus must write no audit row under A)", event, afterA, beforeA)
	}
}

// --- QA (Mode B) adversarial/edge coverage, task-37 ------------------------------------

// TestStoreLifecycle_RoundTripAuditTrail (QA, task-37): the demo's core
// lifecycle -- Create(active) -> Offboard -> Onboard -- must leave the
// audit_log with EXACTLY 3 rows for that entity, in commit order, with the
// exact event keys "portfolio.entity.created" -> "portfolio.entity.offboarded"
// -> "portfolio.entity.onboarded", actor == the caller's Subject on every row.
// This is stronger than the per-event counting the other store tests do: it
// proves the full trail is both complete (no missing/duplicate row) and
// ordered.
func TestStoreLifecycle_RoundTripAuditTrail(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	userID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio lifecycle-roundtrip tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID})

	created, err := store.Create(c, CreateInput{Name: "Lifecycle Co", TIN: "1234567897"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, created.ID)
	})

	if _, err := store.SetStatus(c, created.ID, "archived"); err != nil {
		t.Fatalf("SetStatus(archived): %v", err)
	}
	if _, err := store.SetStatus(c, created.ID, "active"); err != nil {
		t.Fatalf("SetStatus(active): %v", err)
	}

	events, actors := auditEventsForEntity(t, app, tenantID, created.ID)
	wantEvents := []string{"portfolio.entity.created", "portfolio.entity.offboarded", "portfolio.entity.onboarded"}
	if len(events) != len(wantEvents) {
		t.Fatalf("audit events (in order) = %v, want %v", events, wantEvents)
	}
	for i, want := range wantEvents {
		if events[i] != want {
			t.Errorf("audit event[%d] = %q, want %q (full order: %v)", i, events[i], want, events)
		}
	}
	for i, actor := range actors {
		if actor != userID {
			t.Errorf("audit row %d (%s) actor = %q, want %q", i, events[i], actor, userID)
		}
	}
}

// TestStoreUpdate_TINToAnotherTenantsTINSucceeds (QA, task-37): the
// business_entities_tenant_tin_uq unique index is scoped (tenant_id, tin), so
// updating tenant A's entity to hold the SAME tin value already used by
// tenant B's entity must succeed -- uniqueness is per-tenant, not global.
func TestStoreUpdate_TINToAnotherTenantsTINSucceeds(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio update-cross-tenant-tin A', 'firm'), ($2, 'portfolio update-cross-tenant-tin B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})

	const tinX = "1234567897"
	const tinY = "123456780006"
	entityAID := seedEntity(t, super, tenantA, "A Corp", strPtr(tinX))
	seedEntity(t, super, tenantB, "B Corp", strPtr(tinY))

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	newTIN := tinY
	updated, err := store.Update(cA, entityAID, UpdateInput{TIN: &newTIN})
	if err != nil {
		t.Fatalf("Update(A's entity, tin=B's tin) err = %v, want success (TIN uniqueness is per-tenant)", err)
	}
	if updated.TIN == nil || *updated.TIN != tinY {
		t.Errorf("Update: tin = %v, want %q", updated.TIN, tinY)
	}
}

// TestStoreUpdate_PartialUpdateLeavesOtherFieldsIntact (QA, task-37): an
// entity seeded with name+sector+address -- updating name ONLY must leave
// sector/address exactly as they were, both in the returned Entity and on
// re-fetch, proving the dynamic SET clause really only touches the provided
// field(s).
func TestStoreUpdate_PartialUpdateLeavesOtherFieldsIntact(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio update-partial tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	sector, address := "Manufacturing", "12 Marina Rd, Lagos"
	created, err := store.Create(c, CreateInput{Name: "Untouched Fields Co", TIN: "1234567897", Sector: &sector, Address: &address})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, created.ID)
	})

	newName := "Renamed Only"
	updated, err := store.Update(c, created.ID, UpdateInput{Name: &newName})
	if err != nil {
		t.Fatalf("Update(name only): %v", err)
	}
	if updated.Name != newName {
		t.Errorf("Update: name = %q, want %q", updated.Name, newName)
	}
	if updated.Sector == nil || *updated.Sector != sector || updated.Address == nil || *updated.Address != address {
		t.Errorf("Update(name only): sector=%v address=%v, want unchanged %q/%q", updated.Sector, updated.Address, sector, address)
	}

	got, err := store.GetByID(c, created.ID)
	if err != nil {
		t.Fatalf("GetByID after partial Update: %v", err)
	}
	if got.Name != newName {
		t.Errorf("GetByID: name = %q, want %q", got.Name, newName)
	}
	if got.Sector == nil || *got.Sector != sector || got.Address == nil || *got.Address != address {
		t.Errorf("GetByID after partial Update: sector=%v address=%v, want unchanged %q/%q", got.Sector, got.Address, sector, address)
	}
}

// TestStoreUpdate_ArchivedEntityTINChangeSucceeds (QA, task-37): editing an
// archived entity's TIN (not just its name) must still succeed, still leave
// status untouched -- edit-while-archived ([A6]) applies to every mutable
// field, not just name.
func TestStoreUpdate_ArchivedEntityTINChangeSucceeds(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio update-archived-tin tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	const oldTIN = "1234567897"
	const newTIN = "123456780006"
	entityID := seedEntityStatus(t, super, tenantID, "Archived TIN Co", strPtr(oldTIN), "archived")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	newTINVal := newTIN
	updated, err := store.Update(c, entityID, UpdateInput{TIN: &newTINVal})
	if err != nil {
		t.Fatalf("Update (archived entity, tin change): %v", err)
	}
	if updated.Status != "archived" {
		t.Errorf("Update: status = %q, want still %q", updated.Status, "archived")
	}
	if updated.TIN == nil || *updated.TIN != newTIN {
		t.Errorf("Update: tin = %v, want %q", updated.TIN, newTIN)
	}

	got, err := store.GetByID(c, entityID)
	if err != nil {
		t.Fatalf("GetByID after Update: %v", err)
	}
	if got.Status != "archived" || got.TIN == nil || *got.TIN != newTIN {
		t.Errorf("GetByID after Update = %+v, want status=archived tin=%q", got, newTIN)
	}
}

// TestStoreSetStatus_InvalidTargetRejectedByCheckConstraint (QA, task-37,
// defensive): SetStatus's target is normally bound to "active"/"archived" at
// the Offboard/OnboardHandler wiring layer (M3-03-05) -- never taken directly
// from caller-supplied input, so this is not user-reachable today. This test
// is defense-in-depth only: it proves that even a caller bug (a stray target
// string reaching SetStatus) cannot silently corrupt the status column --
// the business_entities status CHECK constraint (status IN ('active',
// 'archived')) rejects the UPDATE, SetStatus returns a non-nil error, and the
// row's status is left unchanged.
func TestStoreSetStatus_InvalidTargetRejectedByCheckConstraint(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio setstatus-invalid-target tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	entityID := seedEntity(t, super, tenantID, "Bogus Target Co", nil)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	if _, err := store.SetStatus(c, entityID, "bogus"); err == nil {
		t.Fatal(`SetStatus(id, "bogus") err = nil, want a non-nil error (status CHECK constraint violation)`)
	}

	got, err := store.GetByID(c, entityID)
	if err != nil {
		t.Fatalf("GetByID after rejected SetStatus: %v", err)
	}
	if got.Status != "active" {
		t.Errorf("GetByID after rejected SetStatus: status = %q, want unchanged %q", got.Status, "active")
	}
}

// TestStoreSetStatus_DoubleOffboardIdempotencySemantics (QA, task-37): a
// live Offboard->Offboard sequence on the SAME entity (not a pre-seeded
// already-archived row) -- the first call must succeed and archive it, the
// second must be the redundant-transition 409 with the audit count unchanged
// after it. Complements TestStoreSetStatus_RedundantOffboard409 (which seeds
// the archived state directly) by exercising the actual state transition
// that produces it.
func TestStoreSetStatus_DoubleOffboardIdempotencySemantics(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio double-offboard tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	entityID := seedEntity(t, super, tenantID, "Double Offboard Co", nil)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const event = "portfolio.entity.offboarded"
	before := auditCount(t, app, tenantID, event)

	first, err := store.SetStatus(c, entityID, "archived")
	if err != nil {
		t.Fatalf("first SetStatus(archived): %v", err)
	}
	if first.Status != "archived" {
		t.Errorf("first SetStatus: status = %q, want %q", first.Status, "archived")
	}

	afterFirst := auditCount(t, app, tenantID, event)
	if afterFirst != before+1 {
		t.Fatalf("audit_log rows for %s after first offboard = %d, want %d", event, afterFirst, before+1)
	}

	_, err = store.SetStatus(c, entityID, "archived")
	if !errors.Is(err, ErrRedundantTransition) {
		t.Fatalf("second SetStatus(archived) err = %v, want ErrRedundantTransition", err)
	}

	afterSecond := auditCount(t, app, tenantID, event)
	if afterSecond != afterFirst {
		t.Errorf("audit_log rows for %s after second (redundant) offboard = %d, want unchanged %d", event, afterSecond, afterFirst)
	}
}

// TestStoreSetStatus_CrossTenantOnboardNotFound (QA, task-37): completes
// cross-tenant coverage for SetStatus -- TestStoreSetStatus_CrossTenantNotFound
// only exercises the offboard (target="archived") direction; this exercises
// the onboard (target="active") direction against a cross-tenant id, which
// must equally return ErrNotFound, leave B's row unaffected, and write no
// audit row under A.
func TestStoreSetStatus_CrossTenantOnboardNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio setstatus-cross-tenant-onboard A', 'firm'), ($2, 'portfolio setstatus-cross-tenant-onboard B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})

	entityIDInB := seedEntityStatus(t, super, tenantB, "B Archived Corp", nil, "archived")

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	const event = "portfolio.entity.onboarded"
	beforeA := auditCount(t, app, tenantA, event)

	_, err := store.SetStatus(cA, entityIDInB, "active")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetStatus(B's id, active) as tenant A err = %v, want ErrNotFound", err)
	}

	cB := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB})
	got, err := store.GetByID(cB, entityIDInB)
	if err != nil {
		t.Fatalf("GetByID(B's entity, as B) after cross-tenant Onboard attempt: %v", err)
	}
	if got.Status != "archived" {
		t.Errorf("B's entity status = %q, want unchanged %q", got.Status, "archived")
	}

	afterA := auditCount(t, app, tenantA, event)
	if afterA != beforeA {
		t.Errorf("audit_log rows for %s under tenant A = %d, want unchanged %d (cross-tenant Onboard must write no audit row under A)", event, afterA, beforeA)
	}
}
