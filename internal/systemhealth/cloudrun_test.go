package systemhealth

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type runReq struct{ path, query, auth, token string }

type runFake struct {
	mu   sync.Mutex
	reqs []runReq
}

func newRunFake(t *testing.T, h func(r *http.Request, n int) (int, string)) (*runFake, *Google) {
	t.Helper()
	f := &runFake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, runReq{r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.URL.Query().Get("pageToken")})
		n := len(f.reqs)
		f.mu.Unlock()
		code, body := h(r, n)
		w.WriteHeader(code)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return f, &Google{Monitoring: srv.URL, Run: srv.URL, Client: srv.Client()}
}

const servicesBody = `{"services":[
 {"name":"projects/p1/locations/r1/services/submission","scaling":{"maxInstanceCount":3},
  "template":{"scaling":{"maxInstanceCount":2},"containers":[{"resources":{"limits":{"cpu":"1","memory":"512Mi"}}}]}},
 {"name":"projects/p1/locations/r1/services/app","template":{"containers":[{"resources":{"limits":{"cpu":"2","memory":"1Gi"}}}]}}]}`

func TestLimits_ReadsTheTemplateLimits(t *testing.T) {
	f, g := newRunFake(t, func(*http.Request, int) (int, string) { return 200, servicesBody })
	got, err := g.limits(t.Context(), "tok", "p1", "r1", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []ServiceLimits{{"app", "2", "1Gi", 0}, {"submission", "1", "512Mi", 2}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("limits = %+v, want %+v", got, want)
	}
	if r := f.reqs[0]; r.path != "/v2/projects/p1/locations/r1/services" || r.auth != "Bearer tok" {
		t.Fatalf("request = %+v", r)
	}
}

func TestLimits_TheServiceFilterKeepsOne(t *testing.T) {
	_, g := newRunFake(t, func(*http.Request, int) (int, string) { return 200, servicesBody })
	got, err := g.limits(t.Context(), "tok", "p1", "r1", "app")
	if err != nil || len(got) != 1 || got[0].Name != "app" {
		t.Fatalf("limits = %+v, %v", got, err)
	}
}

func TestLimits_AServiceWithoutContainersKeepsZeroValues(t *testing.T) {
	_, g := newRunFake(t, func(*http.Request, int) (int, string) {
		return 200, `{"services":[{"name":"projects/p1/locations/r1/services/bare","template":{}}]}`
	})
	got, err := g.limits(t.Context(), "tok", "p1", "r1", "")
	if err != nil || !reflect.DeepEqual(got, []ServiceLimits{{Name: "bare"}}) {
		t.Fatalf("limits = %+v, %v", got, err)
	}
}

func TestLimits_IgnoresTheServiceLevelMax(t *testing.T) {
	_, g := newRunFake(t, func(*http.Request, int) (int, string) { return 200, servicesBody })
	got, _ := g.limits(t.Context(), "tok", "p1", "r1", "submission")
	if len(got) != 1 || got[0].MaxInstances != 2 {
		t.Fatalf("limits = %+v, want max 2 (service-level is 3)", got)
	}
}

func rev(svc, name, at string) string {
	return fmt.Sprintf(`{"name":"projects/p1/locations/r1/services/%s/revisions/%s","service":"projects/p1/locations/r1/services/%s","createTime":%q}`, svc, name, svc, at)
}

func TestDeploys_KeepsTheRangeAndSortsAscending(t *testing.T) {
	revs := []string{
		rev("app", "r-1829", "2026-10-23T18:29:00Z"),
		rev("app", "r-0611", "2026-10-23T06:11:00Z"),
		rev("app", "r-1831", "2026-10-23T18:31:00Z"),
		rev("app", "r-1258", "2026-10-23T12:58:00Z"),
		rev("app", "r-0500", "2026-10-23T05:00:00Z"),
	}
	_, g := newRunFake(t, func(*http.Request, int) (int, string) {
		return 200, `{"revisions":[` + strings.Join(revs, ",") + `]}`
	})
	start := time.Date(2026, 10, 23, 6, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 23, 18, 30, 0, 0, time.UTC)
	got, err := g.deploys(t.Context(), "tok", "p1", "r1", "", start, now)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range got {
		names = append(names, d.Revision)
	}
	if want := []string{"r-0611", "r-1258", "r-1829"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("revisions = %v, want %v", names, want)
	}

	// Boundaries are inclusive.
	edge := []string{rev("app", "at-start", "2026-10-23T06:00:00Z"), rev("app", "at-now", "2026-10-23T18:30:00Z")}
	_, g = newRunFake(t, func(*http.Request, int) (int, string) {
		return 200, `{"revisions":[` + strings.Join(edge, ",") + `]}`
	})
	got, _ = g.deploys(t.Context(), "tok", "p1", "r1", "", start, now)
	if len(got) != len(edge) {
		t.Fatalf("edges kept %d of %d", len(got), len(edge))
	}
}

func TestDeploys_UsesTheWildcardOrTheService(t *testing.T) {
	f, g := newRunFake(t, func(*http.Request, int) (int, string) {
		return 200, `{"revisions":[` + rev("submission", "submission-00007-abc", "2026-10-23T10:00:00Z") + `]}`
	})
	start := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	now := start.Add(24 * time.Hour)
	for _, tc := range []struct{ svc, path string }{
		{"", "/v2/projects/p1/locations/r1/services/-/revisions"},
		{"app", "/v2/projects/p1/locations/r1/services/app/revisions"},
	} {
		got, err := g.deploys(t.Context(), "tok", "p1", "r1", tc.svc, start, now)
		if err != nil || len(got) != 1 || got[0].Service != "submission" || got[0].Revision != "submission-00007-abc" {
			t.Fatalf("deploys(%q) = %+v, %v", tc.svc, got, err)
		}
		if last := f.reqs[len(f.reqs)-1]; last.path != tc.path || last.auth != "Bearer tok" {
			t.Fatalf("deploys(%q) request = %+v, want path %s", tc.svc, last, tc.path)
		}
	}
}

func TestDeploys_FollowsPages(t *testing.T) {
	f, g := newRunFake(t, func(r *http.Request, n int) (int, string) {
		if r.URL.Query().Get("pageToken") == "" {
			return 200, `{"revisions":[` + rev("app", "a", "2026-10-23T10:00:00Z") + `,` + rev("app", "b", "2026-10-23T11:00:00Z") + `],"nextPageToken":"p2"}`
		}
		return 200, `{"revisions":[` + rev("app", "c", "2026-10-23T12:00:00Z") + `]}`
	})
	start := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	got, err := g.deploys(t.Context(), "tok", "p1", "r1", "", start, start.Add(24*time.Hour))
	if err != nil || len(got) != 3 {
		t.Fatalf("deploys = %+v, %v", got, err)
	}
	if len(f.reqs) != 2 || f.reqs[0].token != "" || f.reqs[1].token != "p2" {
		t.Fatalf("requests = %+v", f.reqs)
	}
}

func TestDeploys_RefusesMoreThanTwentyPages(t *testing.T) {
	f, g := newRunFake(t, func(*http.Request, int) (int, string) {
		return 200, `{"revisions":[],"nextPageToken":"more"}`
	})
	now := time.Now()
	if _, err := g.deploys(t.Context(), "tok", "p1", "r1", "", now.Add(-time.Hour), now); err == nil {
		t.Fatal("want an error past the page limit")
	}
	if len(f.reqs) != deploysMaxPages {
		t.Fatalf("requests = %d, want %d", len(f.reqs), deploysMaxPages)
	}
}

func TestRunLists_RefuseABadAnswer(t *testing.T) {
	calls := map[string]func(g *Google) error{
		"limits": func(g *Google) error { _, err := g.limits(t.Context(), "tok", "p1", "r1", ""); return err },
		"deploys": func(g *Google) error {
			_, err := g.deploys(t.Context(), "tok", "p1", "r1", "", time.Now().Add(-time.Hour), time.Now())
			return err
		},
	}
	for _, ans := range []struct {
		name, body string
		code       int
	}{
		{"403", `{"error":"denied"}`, 403},
		{"html", `<html>`, 200},
		{"over cap", strings.Repeat("x", deploysBodyCap+1), 200},
	} {
		for name, call := range calls {
			t.Run(name+"/"+ans.name, func(t *testing.T) {
				_, g := newRunFake(t, func(*http.Request, int) (int, string) { return ans.code, ans.body })
				if err := call(g); err == nil {
					t.Fatal("want an error")
				}
			})
		}
	}
}
