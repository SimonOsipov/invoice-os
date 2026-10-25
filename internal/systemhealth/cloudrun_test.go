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

func svcJSON(name, cpu string) string {
	return fmt.Sprintf(`{"name":"projects/p1/locations/r1/services/%s","template":{"scaling":{"maxInstanceCount":1},"containers":[{"resources":{"limits":{"cpu":%q,"memory":"1Gi"}}}]}}`, name, cpu)
}

func TestLimits_FollowsPagesAndFiltersAcrossThem(t *testing.T) {
	f, g := newRunFake(t, func(r *http.Request, n int) (int, string) {
		if r.URL.Query().Get("pageToken") == "" {
			return 200, `{"services":[` + svcJSON("zeta", "1") + `,` + svcJSON("app", "2") + `],"nextPageToken":"p2"}`
		}
		return 200, `{"services":[` + svcJSON("mid", "4") + `]}`
	})
	got, err := g.limits(t.Context(), "tok", "p1", "r1", "")
	if err != nil || len(got) != 3 || got[0].Name != "app" || got[1].Name != "mid" || got[2].Name != "zeta" {
		t.Fatalf("limits = %+v, %v", got, err)
	}
	if len(f.reqs) != 2 || f.reqs[1].token != "p2" || !strings.Contains(f.reqs[0].query, "pageSize=100") {
		t.Fatalf("requests = %+v", f.reqs)
	}
	got, err = g.limits(t.Context(), "tok", "p1", "r1", "mid")
	if err != nil || len(got) != 1 || got[0].CPU != "4" {
		t.Fatalf("filtered limits = %+v, %v", got, err)
	}
}

// The limits list has no page cap today; a server that never stops paging loops until the caller gives up.
func TestLimits_RefusesAListThatNeverEnds(t *testing.T) {
	f, g := newRunFake(t, func(_ *http.Request, n int) (int, string) {
		if n > 200 {
			return 500, ""
		}
		return 200, `{"services":[` + svcJSON("app", "1") + `],"nextPageToken":"same"}`
	})
	if _, err := g.limits(t.Context(), "tok", "p1", "r1", ""); err == nil {
		t.Fatal("want an error for an endless list")
	}
	if len(f.reqs) > 50 {
		t.Fatalf("limits made %d requests for a list that never ends", len(f.reqs))
	}
}

func TestDeploys_TwentyPagesAreAllowed(t *testing.T) {
	f, g := newRunFake(t, func(_ *http.Request, n int) (int, string) {
		body := `{"revisions":[` + rev("app", fmt.Sprintf("r-%02d", n), "2026-10-23T10:00:00Z")
		if n < deploysMaxPages {
			body += `],"nextPageToken":"t"}`
		} else {
			body += `]}`
		}
		return 200, body
	})
	start := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	got, err := g.deploys(t.Context(), "tok", "p1", "r1", "", start, start.Add(24*time.Hour))
	if err != nil || len(got) != deploysMaxPages || len(f.reqs) != deploysMaxPages {
		t.Fatalf("deploys = %d, %v, requests %d", len(got), err, len(f.reqs))
	}
	if !strings.Contains(f.reqs[0].query, "pageSize=1000") {
		t.Fatalf("query = %s", f.reqs[0].query)
	}
}

func TestDeploys_SortsOldestFirstAndShuffledAcrossPages(t *testing.T) {
	start := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	now := start.Add(24 * time.Hour)
	for name, pages := range map[string][2][]string{
		"newest first": {{rev("app", "c", "2026-10-23T12:00:00Z"), rev("app", "b", "2026-10-23T11:00:00Z")}, {rev("app", "a", "2026-10-23T10:00:00Z")}},
		"oldest first": {{rev("app", "a", "2026-10-23T10:00:00Z"), rev("app", "b", "2026-10-23T11:00:00Z")}, {rev("app", "c", "2026-10-23T12:00:00Z")}},
		"shuffled":     {{rev("app", "b", "2026-10-23T11:00:00Z"), rev("app", "c", "2026-10-23T12:00:00Z")}, {rev("app", "a", "2026-10-23T10:00:00Z")}},
		"equal times":  {{rev("app", "b", "2026-10-23T10:00:00Z")}, {rev("app", "a", "2026-10-23T10:00:00Z"), rev("app", "c", "2026-10-23T10:00:01Z")}},
	} {
		t.Run(name, func(t *testing.T) {
			_, g := newRunFake(t, func(r *http.Request, _ int) (int, string) {
				if r.URL.Query().Get("pageToken") == "" {
					return 200, `{"revisions":[` + strings.Join(pages[0], ",") + `],"nextPageToken":"p2"}`
				}
				return 200, `{"revisions":[` + strings.Join(pages[1], ",") + `]}`
			})
			got, err := g.deploys(t.Context(), "tok", "p1", "r1", "", start, now)
			if err != nil || len(got) != 3 {
				t.Fatalf("deploys = %+v, %v", got, err)
			}
			for i, want := range []string{"a", "b", "c"} {
				if got[i].Revision != want {
					t.Fatalf("order = %+v, want a,b,c", got)
				}
			}
		})
	}
}

func TestDeploys_BadAndMissingCreateTime(t *testing.T) {
	start := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	now := start.Add(24 * time.Hour)
	_, g := newRunFake(t, func(*http.Request, int) (int, string) {
		return 200, `{"revisions":[` + rev("app", "ok", "2026-10-23T10:00:00Z") + `,` + rev("app", "bad", "yesterday") + `]}`
	})
	if _, err := g.deploys(t.Context(), "tok", "p1", "r1", "", start, now); err == nil {
		t.Fatal("want an error for an unparsable createTime")
	}
	_, g = newRunFake(t, func(*http.Request, int) (int, string) {
		return 200, `{"revisions":[` + rev("app", "ok", "2026-10-23T10:00:00Z") + `,{"name":"projects/p1/locations/r1/services/app/revisions/none","service":"projects/p1/locations/r1/services/app"}]}`
	})
	got, err := g.deploys(t.Context(), "tok", "p1", "r1", "", start, now)
	if err != nil || len(got) != 1 || got[0].Revision != "ok" {
		t.Fatalf("a revision without createTime must be dropped: %+v, %v", got, err)
	}
}

func TestRunLists_BodyCapBoundary(t *testing.T) {
	pad := func(base string, size int) string { return base + strings.Repeat(" ", size-len(base)) }
	start := time.Now().Add(-time.Hour)
	now := time.Now()
	for _, tc := range []struct {
		name, base string
		cap        int
		call       func(g *Google) error
	}{
		{"limits", `{"services":[]}`, 1 << 20, func(g *Google) error { _, err := g.limits(t.Context(), "tok", "p1", "r1", ""); return err }},
		{"deploys", `{"revisions":[]}`, 16 << 20, func(g *Google) error { _, err := g.deploys(t.Context(), "tok", "p1", "r1", "", start, now); return err }},
	} {
		for _, over := range []bool{false, true} {
			size := tc.cap
			if over {
				size++
			}
			t.Run(fmt.Sprintf("%s/over=%v", tc.name, over), func(t *testing.T) {
				_, g := newRunFake(t, func(*http.Request, int) (int, string) { return 200, pad(tc.base, size) })
				if err := tc.call(g); (err != nil) != over {
					t.Fatalf("size %d (cap %d): err = %v", size, tc.cap, err)
				}
			})
		}
	}
}

func TestRunLists_Refuse500AndEmptyBody(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	for name, ans := range map[string]struct {
		code int
		body string
	}{"500": {500, `{}`}, "empty 200": {200, ``}} {
		_, g := newRunFake(t, func(*http.Request, int) (int, string) { return ans.code, ans.body })
		if _, err := g.limits(t.Context(), "tok", "p1", "r1", ""); err == nil {
			t.Fatalf("limits %s: want an error", name)
		}
		if _, err := g.deploys(t.Context(), "tok", "p1", "r1", "", start, time.Now()); err == nil {
			t.Fatalf("deploys %s: want an error", name)
		}
	}
}

func TestLimits_FivePagesAllowedAndSixthRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pages int
		fail  bool
	}{{"five pages", 5, false}, {"endless", 1000, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f, g := newRunFake(t, func(_ *http.Request, n int) (int, string) {
				body := `{"services":[` + svcJSON(fmt.Sprintf("s%d", n), "1") + `]`
				if n < tc.pages {
					body += `,"nextPageToken":"t"`
				}
				return 200, body + `}`
			})
			got, err := g.limits(t.Context(), "tok", "p1", "r1", "")
			if (err != nil) != tc.fail {
				t.Fatalf("err = %v, want failure %v", err, tc.fail)
			}
			if tc.fail && len(f.reqs) != 5 {
				t.Fatalf("requests = %d, want 5", len(f.reqs))
			}
			if !tc.fail && len(got) != 5 {
				t.Fatalf("limits = %d services, want 5", len(got))
			}
		})
	}
}

func svcBody(names ...string) string {
	var parts []string
	for _, n := range names {
		parts = append(parts, fmt.Sprintf(`{"name":"projects/p1/locations/r1/services/%s","template":{"scaling":{"maxInstanceCount":%d},"containers":[{"resources":{"limits":{"cpu":"1","memory":"%dMi"}}}]}}`, n, len(n), len(n)))
	}
	return `{"services":[` + strings.Join(parts, ",") + `]}`
}

func previewLimits(t *testing.T, suffix, svc string, names ...string) []ServiceLimits {
	t.Helper()
	_, g := newRunFake(t, func(*http.Request, int) (int, string) { return 200, svcBody(names...) })
	g.Suffix = suffix
	got, err := g.limits(t.Context(), "tok", "p1", "r1", svc)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func limitNames(l []ServiceLimits) []string {
	var out []string
	for _, x := range l {
		out = append(out, x.Name)
	}
	return out
}

func TestLimits_APreviewKeepsOnlyItsPRServices(t *testing.T) {
	got := previewLimits(t, "-pr-545", "", "gateway-pr-545", "gateway-pr-546", "app-pr-545", "gateway")
	if want := []string{"app", "gateway"}; !reflect.DeepEqual(limitNames(got), want) {
		t.Fatalf("names = %v, want %v", limitNames(got), want)
	}
}

func TestLimits_APreviewSuffixIsNotAPrefixOfALongerPR(t *testing.T) {
	got := previewLimits(t, "-pr-54", "", "gateway-pr-545", "gateway-pr-54")
	if len(got) != 1 || got[0].Name != "gateway" || got[0].Memory != "13Mi" {
		t.Fatalf("limits = %+v, want gateway from gateway-pr-54", got)
	}
}

func TestLimits_APreviewPickKeepsItsPRService(t *testing.T) {
	got := previewLimits(t, "-pr-545", "gateway", "gateway-pr-545", "gateway-pr-546", "app-pr-545")
	want := []ServiceLimits{{"gateway", "1", "14Mi", 14}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("limits = %+v, want %+v", got, want)
	}
}

func TestDeploys_APreviewAsksForItsPRService(t *testing.T) {
	f, g := newRunFake(t, func(*http.Request, int) (int, string) {
		return 200, `{"revisions":[` + rev("gateway-pr-545", "gateway-pr-545-00003-pqf", "2026-10-23T10:00:00Z") + `]}`
	})
	g.Suffix = "-pr-545"
	start := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	got, err := g.deploys(t.Context(), "tok", "p1", "r1", "gateway", start, start.Add(24*time.Hour))
	if err != nil || len(got) != 1 || got[0].Service != "gateway" {
		t.Fatalf("deploys = %+v, %v", got, err)
	}
	if p := f.reqs[0].path; p != "/v2/projects/p1/locations/r1/services/gateway-pr-545/revisions" {
		t.Fatalf("path = %s", p)
	}
}

func previewDeploys(t *testing.T, suffix string, revs ...string) (string, []Deploy) {
	t.Helper()
	f, g := newRunFake(t, func(*http.Request, int) (int, string) {
		return 200, `{"revisions":[` + strings.Join(revs, ",") + `]}`
	})
	g.Suffix = suffix
	start := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	got, err := g.deploys(t.Context(), "tok", "p1", "r1", "", start, start.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return f.reqs[0].path, got
}

func TestDeploys_APreviewSuffixIsNotAPrefixOfALongerPR(t *testing.T) {
	_, got := previewDeploys(t, "-pr-54",
		rev("gateway-pr-545", "gateway-pr-545-00001-aaa", "2026-10-23T10:00:00Z"),
		rev("gateway-pr-54", "gateway-pr-54-00001-bbb", "2026-10-23T11:00:00Z"))
	if len(got) != 1 || got[0].Service != "gateway" || got[0].Revision != "gateway-pr-54-00001-bbb" {
		t.Fatalf("deploys = %+v", got)
	}
}

func TestDeploys_APreviewWildcardKeepsOnlyItsPR(t *testing.T) {
	path, got := previewDeploys(t, "-pr-545",
		rev("gateway-pr-545", "gateway-pr-545-00003-pqf", "2026-10-23T10:00:00Z"),
		rev("gateway-pr-546", "gateway-pr-546-00001-xyz", "2026-10-23T11:00:00Z"),
		rev("gateway", "gateway-00001-abc", "2026-10-23T12:00:00Z"))
	if path != "/v2/projects/p1/locations/r1/services/-/revisions" {
		t.Fatalf("path = %s", path)
	}
	if len(got) != 1 || got[0].Service != "gateway" || got[0].Revision != "gateway-pr-545-00003-pqf" {
		t.Fatalf("deploys = %+v", got)
	}
}
