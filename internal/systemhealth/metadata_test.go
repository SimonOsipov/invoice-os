package systemhealth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type metaFake struct {
	mu     sync.Mutex
	hits   map[string]int
	flavor string
}

func newMetaFake(t *testing.T, h func(path string, hit int) (int, string)) (*metaFake, *httptest.Server) {
	t.Helper()
	f := &metaFake{hits: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits[r.URL.Path]++
		n := f.hits[r.URL.Path]
		f.flavor = r.Header.Get("Metadata-Flavor")
		f.mu.Unlock()
		code, body := h(r.URL.Path, n)
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *metaFake) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

func (f *metaFake) seenFlavor() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flavor
}

const (
	tokenPath  = "/instance/service-accounts/default/token"
	projPath   = "/project/project-id"
	regionPath = "/instance/region"
)

func TestMetadata_TokenReadsTheAccessTokenWithTheFlavorHeader(t *testing.T) {
	f, srv := newMetaFake(t, func(string, int) (int, string) {
		return 200, `{"access_token":"tok-1","expires_in":3599,"token_type":"Bearer"}`
	})
	m := NewMetadata(srv.URL+"/", srv.Client(), time.Now)
	got, err := m.Token(context.Background())
	if err != nil || got != "tok-1" {
		t.Fatalf("Token = %q, %v", got, err)
	}
	if f.count(tokenPath) != 1 || f.seenFlavor() != "Google" {
		t.Fatalf("token hits %d, flavor %q", f.count(tokenPath), f.seenFlavor())
	}
}

func TestMetadata_TokenIsReusedUntilSixtySecondsBeforeExpiry(t *testing.T) {
	f, srv := newMetaFake(t, func(_ string, n int) (int, string) {
		return 200, fmt.Sprintf(`{"access_token":"tok-%d","expires_in":3600}`, n)
	})
	t0 := time.Unix(1_800_000_000, 0)
	now := t0
	m := NewMetadata(srv.URL, srv.Client(), func() time.Time { return now })
	for _, tc := range []struct {
		at   time.Duration
		hits int
		want string
	}{{0, 1, "tok-1"}, {3539 * time.Second, 1, "tok-1"}, {3540 * time.Second, 2, "tok-2"}} {
		now = t0.Add(tc.at)
		got, err := m.Token(context.Background())
		if err != nil || got != tc.want || f.count(tokenPath) != tc.hits {
			t.Fatalf("at +%v: got %q, %v, hits %d; want %q, %d", tc.at, got, err, f.count(tokenPath), tc.want, tc.hits)
		}
	}
}

func TestMetadata_TokenRefusesABadAnswerAndCachesNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		code int
		body string
	}{
		"500":                         {500, "boom"},
		"not json":                    {200, "<html>"},
		"empty token":                 {200, `{"access_token":"","expires_in":3599}`},
		"zero expires":                {200, `{"access_token":"t","expires_in":0}`},
		"negative expires":            {200, `{"access_token":"t","expires_in":-5}`},
		"500 with a valid token body": {500, `{"access_token":"t","expires_in":3599}`},
		"body over the 8 KiB cap":     {200, `{"access_token":"t","expires_in":3599,"pad":"` + strings.Repeat("x", 9<<10) + `"}`},
	} {
		t.Run(name, func(t *testing.T) {
			f, srv := newMetaFake(t, func(string, int) (int, string) { return tc.code, tc.body })
			m := NewMetadata(srv.URL, srv.Client(), time.Now)
			for range 2 {
				if _, err := m.Token(context.Background()); err == nil {
					t.Fatal("want an error")
				}
			}
			if f.count(tokenPath) != 2 {
				t.Fatalf("hits = %d, want 2", f.count(tokenPath))
			}
		})
	}
}

func TestMetadata_LocationReadsProjectAndRegionOnce(t *testing.T) {
	f, srv := newMetaFake(t, func(p string, _ int) (int, string) {
		if p == projPath {
			return 200, "ascomply-prod"
		}
		return 200, "projects/123/regions/africa-south1"
	})
	m := NewMetadata(srv.URL, srv.Client(), time.Now)
	for range 2 {
		p, r, err := m.Location(context.Background())
		if err != nil || p != "ascomply-prod" || r != "africa-south1" {
			t.Fatalf("Location = %q, %q, %v", p, r, err)
		}
	}
	if f.count(projPath) != 1 || f.count(regionPath) != 1 {
		t.Fatalf("project hits %d, region hits %d", f.count(projPath), f.count(regionPath))
	}
}

func TestMetadata_LocationFailureIsNotCached(t *testing.T) {
	const okRegion = "projects/123/regions/africa-south1"
	for name, tc := range map[string]struct {
		badPath string
		code    int
		body    string
	}{
		"project 500":        {projPath, 500, "boom"},
		"region 500":         {regionPath, 500, okRegion},
		"region no regions/": {regionPath, 200, "africa-south1"},
		"region empty seg":   {regionPath, 200, "projects/123/regions/"},
	} {
		t.Run(name, func(t *testing.T) {
			f, srv := newMetaFake(t, func(p string, n int) (int, string) {
				if p == tc.badPath && n == 1 {
					return tc.code, tc.body
				}
				if p == projPath {
					return 200, "ascomply-prod"
				}
				return 200, okRegion
			})
			m := NewMetadata(srv.URL, srv.Client(), time.Now)
			if _, _, err := m.Location(context.Background()); err == nil {
				t.Fatal("want an error")
			}
			p, r, err := m.Location(context.Background())
			if err != nil || p != "ascomply-prod" || r != "africa-south1" {
				t.Fatalf("second Location = %q, %q, %v", p, r, err)
			}
			if f.count(tc.badPath) != 2 {
				t.Fatalf("%s hits = %d, want 2", tc.badPath, f.count(tc.badPath))
			}
		})
	}
}

func TestMetadata_ConcurrentTokenCallersShareOneFetch(t *testing.T) {
	f, srv := newMetaFake(t, func(string, int) (int, string) {
		time.Sleep(100 * time.Millisecond)
		return 200, `{"access_token":"tok","expires_in":3600}`
	})
	m := NewMetadata(srv.URL, srv.Client(), time.Now)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := m.Token(context.Background()); err != nil || got != "tok" {
				t.Errorf("Token = %q, %v", got, err)
			}
		}()
	}
	wg.Wait()
	if f.count(tokenPath) != 1 {
		t.Fatalf("token hits = %d, want 1", f.count(tokenPath))
	}
}

func TestMetadata_CallHasItsOwnFiveSecondDeadline(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	m := NewMetadata(srv.URL, &http.Client{}, time.Now)
	start := time.Now()
	if _, err := m.Token(context.Background()); err == nil {
		t.Fatal("want a timeout error")
	}
	if d := time.Since(start); d < 4*time.Second || d > 7*time.Second {
		t.Fatalf("took %v, want about 5s", d)
	}
}

func TestMetadata_TokenTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	m := NewMetadata(srv.URL, &http.Client{Timeout: 50 * time.Millisecond}, time.Now)
	start := time.Now()
	if _, err := m.Token(context.Background()); err == nil {
		t.Fatal("want a timeout error")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v", d)
	}
	if m.token != "" {
		t.Fatal("a timed-out call cached a token")
	}
}
