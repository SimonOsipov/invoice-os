package blogweb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeGateway struct {
	*httptest.Server
	mu       sync.Mutex
	calls    map[string]int
	origins  []string
	cats     []Category
	posts    []Post
	catCC    string
	postCC   string
	status   int // 0 = 200
	body     func(path string) []byte
	hang     time.Duration
	lastPath []string
}

func newGateway(t *testing.T) *fakeGateway {
	t.Helper()
	g := &fakeGateway{calls: map[string]int{}, catCC: "public, max-age=60", postCC: "public, max-age=60"}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.calls[r.URL.Path]++
		g.origins = append(g.origins, r.Header.Get("Origin"))
		g.lastPath = append(g.lastPath, r.Method+" "+r.URL.Path)
		status, cc, hang, body := g.status, g.catCC, g.hang, g.body
		var payload any = categoriesEnvelope{g.cats}
		if strings.HasSuffix(r.URL.Path, "/posts") {
			cc, payload = g.postCC, postsEnvelope{g.posts}
		}
		g.mu.Unlock()
		if hang > 0 {
			time.Sleep(hang)
		}
		if status == -1 { // drop the connection
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		if cc != "" {
			w.Header().Set("Cache-Control", cc)
		}
		if status != 0 {
			if status >= 300 && status < 400 {
				w.Header().Set("Location", "/elsewhere")
			}
			w.WriteHeader(status)
			return
		}
		if body != nil {
			_, _ = w.Write(body(r.URL.Path))
			return
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *fakeGateway) set(f func()) { g.mu.Lock(); f(); g.mu.Unlock() }
func (g *fakeGateway) count(path string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls[path]
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *clock               { return &clock{time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)} }

func newSource(t *testing.T, g *fakeGateway, c *clock) (*Source, *bytes.Buffer) {
	t.Helper()
	u, _ := url.Parse(g.URL + "/")
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	return NewSource(u, &http.Client{Timeout: 300 * time.Millisecond}, c.now, log), &buf
}

func TestSource_ReusesTheSnapshotWithinMaxAge(t *testing.T) {
	g, c := newGateway(t), newClock()
	s, _ := newSource(t, g, c)
	for _, d := range []time.Duration{0, 59 * time.Second} {
		c.t = newClock().t.Add(d)
		if _, err := s.Get(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if g.count("/blog/categories") != 1 || g.count("/blog/posts") != 1 {
		t.Errorf("calls %v", g.calls)
	}
}

func TestSource_RefetchesAfterMaxAge(t *testing.T) {
	g, c := newGateway(t), newClock()
	s, _ := newSource(t, g, c)
	if _, err := s.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	g.set(func() { g.posts = []Post{{ID: "added"}} })
	c.add(60 * time.Second)
	snap, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if g.count("/blog/categories") != 2 || g.count("/blog/posts") != 2 {
		t.Errorf("calls %v", g.calls)
	}
	if len(snap.Posts) != 1 || snap.Posts[0].ID != "added" {
		t.Errorf("posts %+v", snap.Posts)
	}
}

func TestSource_UsesTheShorterMaxAgeOfTheTwoRoutes(t *testing.T) {
	g, c := newGateway(t), newClock()
	g.postCC = "public, max-age=10"
	s, _ := newSource(t, g, c)
	_, _ = s.Get(context.Background())
	c.add(10 * time.Second)
	_, _ = s.Get(context.Background())
	if g.count("/blog/posts") != 2 {
		t.Errorf("calls %v", g.calls)
	}
}

func TestSource_NoMaxAgeMeansNoReuse(t *testing.T) {
	for _, cc := range []string{"no-cache", ""} {
		g, c := newGateway(t), newClock()
		g.postCC = cc
		s, _ := newSource(t, g, c)
		_, _ = s.Get(context.Background())
		_, _ = s.Get(context.Background())
		if g.count("/blog/posts") != 2 {
			t.Errorf("cc %q: calls %v", cc, g.calls)
		}
	}
}

func TestSource_FailureIsUnavailableAndDropsTheSnapshot(t *testing.T) {
	g, c := newGateway(t), newClock()
	g.posts = []Post{{ID: "t0"}}
	s, _ := newSource(t, g, c)
	if _, err := s.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	g.set(func() { g.status = 500 })
	c.add(61 * time.Second)
	if _, err := s.Get(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("t0+61s: %v", err)
	}
	g.set(func() { g.status, g.posts = 0, []Post{{ID: "t1"}} })
	c.add(time.Second)
	before := g.count("/blog/categories")
	if _, err := s.Get(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("back-off: %v", err)
	}
	if g.count("/blog/categories") != before {
		t.Error("fetched during back-off")
	}
	c.add(4 * time.Second)
	snap, err := s.Get(context.Background())
	if err != nil || len(snap.Posts) != 1 || snap.Posts[0].ID != "t1" {
		t.Fatalf("t0+66s: %+v %v", snap, err)
	}
}

func TestSource_EachFailureKindIsUnavailable(t *testing.T) {
	big := func(string) []byte { return bytes.Repeat([]byte(" "), maxUpstreamBytes+1) }
	cases := map[string]func(g *fakeGateway){
		"404":      func(g *fakeGateway) { g.status = 404 },
		"302":      func(g *fakeGateway) { g.status = 302 },
		"429":      func(g *fakeGateway) { g.status = 429 },
		"closed":   func(g *fakeGateway) { g.status = -1 },
		"timeout":  func(g *fakeGateway) { g.hang = 700 * time.Millisecond },
		"over cap": func(g *fakeGateway) { g.body = big },
		"bad JSON": func(g *fakeGateway) { g.body = func(string) []byte { return []byte("{") } },
	}
	for name, mod := range cases {
		g, c := newGateway(t), newClock()
		mod(g)
		s, buf := newSource(t, g, c)
		if _, err := s.Get(context.Background()); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		if len(lines) != 1 {
			t.Errorf("%s: %d log lines: %s", name, len(lines), buf.String())
			continue
		}
		var rec map[string]any
		_ = json.Unmarshal([]byte(lines[0]), &rec)
		want := map[string]float64{"404": 404, "302": 302, "429": 429, "over cap": 200, "bad JSON": 200}[name]
		if rec["msg"] != "blogweb: upstream failed" || rec["level"] != "WARN" || rec["status"] != want {
			t.Errorf("%s: log %v", name, rec)
		}
	}
}

func TestSource_RequestsTheTwoPublicRoutesWithoutOrigin(t *testing.T) {
	g, c := newGateway(t), newClock()
	u, _ := url.Parse(g.URL + "/api/")
	s := NewSource(u, &http.Client{}, c.now, slog.Default())
	if _, err := s.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.lastPath) != 2 || g.lastPath[0] != "GET /api/blog/categories" || g.lastPath[1] != "GET /api/blog/posts" {
		t.Errorf("requests %v", g.lastPath)
	}
	for _, o := range g.origins {
		if o != "" {
			t.Errorf("Origin %q sent", o)
		}
	}
}

func TestSource_AValidBodyOverTheCapIsRefusedAndOneAtTheCapIsRead(t *testing.T) {
	pad := func(n int) func(string) []byte {
		return func(string) []byte {
			head, tail := `{"categories":[],"posts":[],"pad":"`, `"}`
			return []byte(head + strings.Repeat("a", n-len(head)-len(tail)) + tail)
		}
	}
	g, c := newGateway(t), newClock()
	g.body = pad(maxUpstreamBytes)
	s, _ := newSource(t, g, c)
	if _, err := s.Get(context.Background()); err != nil {
		t.Errorf("body of exactly maxUpstreamBytes: %v", err)
	}
	g, c = newGateway(t), newClock()
	g.body = pad(maxUpstreamBytes + 1)
	s, _ = newSource(t, g, c)
	if _, err := s.Get(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("valid JSON one byte over the cap: %v", err)
	}
}

func TestSource_AHungUpstreamIsCutAtTheUpstreamTimeout(t *testing.T) {
	t.Parallel()
	g, c := newGateway(t), newClock()
	g.hang = upstreamTimeout + 1500*time.Millisecond
	u, _ := url.Parse(g.URL)
	s := NewSource(u, &http.Client{}, c.now, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	start := time.Now()
	if _, err := s.Get(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err %v", err)
	}
	if d := time.Since(start); d > upstreamTimeout+1200*time.Millisecond {
		t.Errorf("waited %v, want about %v", d, upstreamTimeout)
	}
}
