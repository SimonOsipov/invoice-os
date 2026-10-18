package blog

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

var publicPaths = []string{
	"/internal/blog/categories",
	"/internal/blog/posts",
	"/internal/blog/posts/p1",
	"/internal/blog/covers/p1/1700000000000000",
}

// newPublicRig wires the four public routes behind the platform gateway guard, as main.go does.
func newPublicRig(t *testing.T) (http.Handler, *publicFake) {
	t.Helper()
	t.Setenv("SENTRY_DSN", "")
	app, err := platform.New("notifications")
	if err != nil {
		t.Fatal(err)
	}
	f := &publicFake{ctype: "image/png", bytes: []byte("x")}
	m := app.Mux
	m.HandleFunc("GET /internal/blog/categories", PublicCategoriesHandler(f.categories, app.Logger))
	m.HandleFunc("GET /internal/blog/posts", PublicPostsHandler(f.listPosts, app.Logger))
	m.HandleFunc("GET /internal/blog/posts/{id}", PublicPostHandler(f.getPost, app.Logger))
	m.HandleFunc("GET /internal/blog/covers/{id}/{version}", PublicCoverHandler(f.cover, app.Logger))
	app.RequireGateway(routesToken)
	return app.Handler(), f
}

func getPublic(h http.Handler, path string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	if mutate != nil {
		mutate(r)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func withToken(r *http.Request) { r.Header.Set(platform.HeaderGatewayToken, routesToken) }

func TestPublicRoutes_RefuseACallWithoutTheGatewayToken(t *testing.T) {
	h, f := newPublicRig(t)
	for _, p := range publicPaths {
		// Control: with the token the route exists, so a 401 below is the guard and not a missing route.
		if rec := getPublic(h, p, withToken); rec.Code != http.StatusOK {
			t.Errorf("control %s with token = %d, want 200", p, rec.Code)
		}
	}
	served := f.calls
	if served != len(publicPaths) {
		t.Fatalf("control: %d store calls, want %d", served, len(publicPaths))
	}
	for _, p := range publicPaths {
		if rec := getPublic(h, p, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without token = %d (%s), want 401", p, rec.Code, rec.Body.String())
		}
		wrong := func(r *http.Request) { r.Header.Set(platform.HeaderGatewayToken, routesToken+"x") }
		if rec := getPublic(h, p, wrong); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with a wrong token = %d (%s), want 401", p, rec.Code, rec.Body.String())
		}
	}
	if f.calls != served {
		t.Errorf("%d store calls after the refused calls, want 0", f.calls-served)
	}
}

func TestPublicRoutes_AnswerAGatewayCallWithNoIdentity(t *testing.T) {
	h, f := newPublicRig(t)
	for i, p := range publicPaths {
		if rec := getPublic(h, p, withToken); rec.Code != http.StatusOK {
			t.Errorf("%s = %d (%s), want 200", p, rec.Code, rec.Body.String())
		}
		if f.calls != i+1 {
			t.Errorf("after %s: %d store calls, want %d", p, f.calls, i+1)
		}
	}
}

func TestPublicRoutes_TenantHeaderWithoutUserIsServed(t *testing.T) {
	h, f := newPublicRig(t)
	for _, p := range publicPaths {
		rec := getPublic(h, p, func(r *http.Request) {
			withToken(r)
			r.Header.Set("X-Tenant-ID", uuid.NewString())
		})
		if rec.Code != http.StatusOK {
			t.Errorf("%s with only X-Tenant-ID = %d (%s), want 200", p, rec.Code, rec.Body.String())
		}
	}
	if f.calls != len(publicPaths) {
		t.Errorf("%d store calls, want %d", f.calls, len(publicPaths))
	}
}

func TestPublicRoutes_ProxiedIdentityIsRefused(t *testing.T) {
	h, f := newPublicRig(t)
	for _, p := range publicPaths {
		if rec := getPublic(h, p, withToken); rec.Code != http.StatusOK {
			t.Fatalf("control %s with token and no identity = %d, want 200", p, rec.Code)
		}
	}
	served := f.calls
	for _, p := range publicPaths {
		rec := getPublic(h, p, func(r *http.Request) {
			withToken(r)
			r.Header.Set("X-User-ID", routesUser)
			r.Header.Set("X-User-Role", "authenticated")
			r.Header.Set("X-Tenant-ID", uuid.NewString())
		})
		if rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
			t.Errorf("%s with identity = %d %q, want 404 %q", p, rec.Code, rec.Body.String(), notFoundBody)
		}
	}
	if f.calls != served {
		t.Errorf("%d store calls for proxied requests, want 0", f.calls-served)
	}
}
