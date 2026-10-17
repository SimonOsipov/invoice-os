package blog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const routesToken = "blog-routes-gateway-token"

const routesUser = "11111111-1111-1111-1111-111111111111"

type blogRoute struct {
	pattern string
	path    string
	body    string
	success int
}

// blogRoutes mirrors the Route wiring table of BLOG-01.
var blogRoutes = []blogRoute{
	{"GET /v1/staff/blog/categories", "/v1/staff/blog/categories", "", 200},
	{"POST /v1/staff/blog/categories", "/v1/staff/blog/categories", `{"name":"N","icon":"globe","tone":1}`, 201},
	{"PUT /v1/staff/blog/categories/{id}", "/v1/staff/blog/categories/c", `{"name":"N","icon":"globe","tone":1}`, 200},
	{"POST /v1/staff/blog/categories/{id}/move", "/v1/staff/blog/categories/c/move", `{"direction":"up"}`, 200},
	{"DELETE /v1/staff/blog/categories/{id}", "/v1/staff/blog/categories/c", "", 200},
	{"GET /v1/staff/blog/authors", "/v1/staff/blog/authors", "", 200},
	{"GET /v1/staff/blog/posts", "/v1/staff/blog/posts", "", 200},
	{"POST /v1/staff/blog/posts", "/v1/staff/blog/posts", `{"title":"T","status":"draft"}`, 201},
	{"PUT /v1/staff/blog/posts/{id}", "/v1/staff/blog/posts/p", `{"title":"T","status":"draft"}`, 200},
	{"DELETE /v1/staff/blog/posts/{id}", "/v1/staff/blog/posts/p", "", 204},
	{"PUT /v1/staff/blog/posts/{id}/featured", "/v1/staff/blog/posts/p/featured", `{"featured":true}`, 200},
	{"PUT /v1/staff/blog/posts/{id}/cover", "/v1/staff/blog/posts/p/cover", string(jpegBytes(32)), 200},
	{"DELETE /v1/staff/blog/posts/{id}/cover", "/v1/staff/blog/posts/p/cover", "", 204},
}

type routesRig struct {
	h     http.Handler
	calls atomic.Int32
	actor atomic.Value
}

func newRoutesRig(t *testing.T) *routesRig {
	t.Helper()
	t.Setenv("SENTRY_DSN", "")
	app, err := platform.New("notifications")
	if err != nil {
		t.Fatal(err)
	}
	rig := &routesRig{}
	hit := func(ctx context.Context) {
		rig.calls.Add(1)
		id, _ := auth.StaffFromContext(ctx)
		rig.actor.Store(id.Subject)
	}
	l := app.Logger
	m := app.Mux
	m.HandleFunc("GET /v1/staff/blog/categories", ListCategoriesHandler(func(ctx context.Context) ([]Category, error) { hit(ctx); return nil, nil }, l))
	m.HandleFunc("POST /v1/staff/blog/categories", AddCategoryHandler(func(ctx context.Context, _ CategoryInput) (Category, error) { hit(ctx); return Category{}, nil }, l))
	m.HandleFunc("PUT /v1/staff/blog/categories/{id}", UpdateCategoryHandler(func(ctx context.Context, _ string, _ CategoryInput) (Category, error) {
		hit(ctx)
		return Category{}, nil
	}, l))
	m.HandleFunc("POST /v1/staff/blog/categories/{id}/move", MoveCategoryHandler(func(ctx context.Context, _, _ string) (bool, []Category, error) { hit(ctx); return false, nil, nil }, l))
	m.HandleFunc("DELETE /v1/staff/blog/categories/{id}", DeleteCategoryHandler(func(ctx context.Context, _ string, _ *string) (DeleteResult, error) {
		hit(ctx)
		return DeleteResult{}, nil
	}, l))
	m.HandleFunc("GET /v1/staff/blog/authors", AuthorsHandler(func(ctx context.Context) ([]Author, error) { hit(ctx); return nil, nil }, l))
	m.HandleFunc("GET /v1/staff/blog/posts", ListPostsHandler(func(ctx context.Context) ([]Post, error) { hit(ctx); return nil, nil }, l))
	m.HandleFunc("POST /v1/staff/blog/posts", CreatePostHandler(func(ctx context.Context, _ PostInput) (Post, error) { hit(ctx); return Post{}, nil }, l))
	m.HandleFunc("PUT /v1/staff/blog/posts/{id}", UpdatePostHandler(func(ctx context.Context, _ string, _ PostInput) (Post, error) { hit(ctx); return Post{}, nil }, l))
	m.HandleFunc("DELETE /v1/staff/blog/posts/{id}", DeletePostHandler(func(ctx context.Context, _ string) error { hit(ctx); return nil }, l))
	m.HandleFunc("PUT /v1/staff/blog/posts/{id}/featured", SetFeaturedHandler(func(ctx context.Context, _ string, _ bool) (FeaturedResult, error) {
		hit(ctx)
		return FeaturedResult{}, nil
	}, l))
	m.HandleFunc("PUT /v1/staff/blog/posts/{id}/cover", SetCoverHandler(func(ctx context.Context, _, _ string, _ []byte) (Cover, error) { hit(ctx); return Cover{}, nil }, l))
	m.HandleFunc("DELETE /v1/staff/blog/posts/{id}/cover", RemoveCoverHandler(func(ctx context.Context, _ string) error { hit(ctx); return nil }, l))
	app.RequireGateway(routesToken)
	rig.h = app.Handler()
	return rig
}

func (rig *routesRig) do(rt blogRoute, gateway, tenant, staff, anonymous bool) *httptest.ResponseRecorder {
	method, _, _ := strings.Cut(rt.pattern, " ")
	r := httptest.NewRequest(method, rt.path, strings.NewReader(rt.body))
	if gateway {
		r.Header.Set(platform.HeaderGatewayToken, routesToken)
	}
	if !anonymous {
		r.Header.Set("X-User-ID", routesUser)
		r.Header.Set("X-User-Role", "authenticated")
		if tenant {
			r.Header.Set("X-Tenant-ID", uuid.NewString())
		}
		if staff {
			r.Header.Set("X-User-Staff", "true")
		}
	}
	rec := httptest.NewRecorder()
	rig.h.ServeHTTP(rec, r)
	return rec
}

func TestBlogRoutes_RefuseEveryNonStaffCaller(t *testing.T) {
	cases := map[string][3]bool{
		"customer":               {true, false, false},
		"tenant-less, not staff": {false, false, false},
		"no identity":            {false, false, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rig := newRoutesRig(t)
			for _, rt := range blogRoutes {
				if rec := rig.do(rt, true, c[0], c[1], c[2]); rec.Code != http.StatusForbidden {
					t.Errorf("%s = %d (%s), want 403", rt.pattern, rec.Code, rec.Body.String())
				}
			}
			if n := rig.calls.Load(); n != 0 {
				t.Errorf("%d handlers ran, want 0", n)
			}
		})
	}
}

func TestBlogRoutes_StaffReachesEveryRoute(t *testing.T) {
	rig := newRoutesRig(t)
	for i, rt := range blogRoutes {
		if rec := rig.do(rt, true, false, true, false); rec.Code != rt.success {
			t.Errorf("%s as staff = %d (%s), want %d", rt.pattern, rec.Code, rec.Body.String(), rt.success)
		}
		if n := rig.calls.Load(); int(n) != i+1 {
			t.Errorf("after %s: %d handler calls, want %d", rt.pattern, n, i+1)
		}
	}
	if got, _ := rig.actor.Load().(string); got != routesUser {
		t.Errorf("StaffFromContext subject = %q, want %q", got, routesUser)
	}
}

func TestBlogRoutes_StaffHeaderWithoutGatewayTokenIsRefused(t *testing.T) {
	rig := newRoutesRig(t)
	for _, rt := range blogRoutes {
		rec := rig.do(rt, false, false, true, false)
		if rec.Code != http.StatusUnauthorized || strings.TrimSpace(rec.Body.String()) != `{"error":"unauthorized"}` {
			t.Errorf("%s = %d %q, want 401 unauthorized", rt.pattern, rec.Code, rec.Body.String())
		}
	}
	if n := rig.calls.Load(); n != 0 {
		t.Errorf("%d handlers ran, want 0", n)
	}
}

func TestBlogRoutes_StaffWrongMethodAndOddPathsReachNoHandler(t *testing.T) {
	rig := newRoutesRig(t)
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{"PATCH", "/v1/staff/blog/posts/p", 405},
		{"POST", "/v1/staff/blog/authors", 405},
		{"DELETE", "/v1/staff/blog/categories", 405},
		{"GET", "/v1/staff/blog/posts/p/cover", 405},
		{"PUT", "/v1/staff/blog/posts/a/b", 404},
		{"GET", "/v1/staff/blog/unknown", 404},
	} {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader("{}"))
		r.Header.Set(platform.HeaderGatewayToken, routesToken)
		r.Header.Set("X-User-ID", routesUser)
		r.Header.Set("X-User-Role", "authenticated")
		r.Header.Set("X-User-Staff", "true")
		rec := httptest.NewRecorder()
		rig.h.ServeHTTP(rec, r)
		if rec.Code != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, rec.Code, c.want)
		}
	}
	if n := rig.calls.Load(); n != 0 {
		t.Errorf("%d handlers ran, want 0", n)
	}
}

func TestBlogRoutes_EncodedIDsReachTheStoreDecoded(t *testing.T) {
	var got []string
	m := http.NewServeMux()
	m.HandleFunc("DELETE /v1/staff/blog/posts/{id}", DeletePostHandler(func(_ context.Context, id string) error { got = append(got, id); return nil }, discard))
	for _, p := range []string{"a%2Fb", "%C3%BCber", "x%20y"} {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest("DELETE", "/v1/staff/blog/posts/"+p, nil))
		if rec.Code != http.StatusNoContent {
			t.Errorf("%s = %d, want 204", p, rec.Code)
		}
	}
	want := []string{"a/b", "über", "x y"}
	if len(got) != len(want) {
		t.Fatalf("store saw %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("id %d = %q, want %q", i, got[i], want[i])
		}
	}
}
