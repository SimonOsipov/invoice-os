package systemhealth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	metricsMatrix   = `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"service_name":"app"},"values":[[1000,"1.5"]]}]}}`
	metricsRevsBody = `{"revisions":[{"name":"projects/p1/locations/r1/services/app/revisions/app-00001","service":"projects/p1/locations/r1/services/app","createTime":"2026-10-23T10:00:00Z"}]}`
)

// comboFake answers the metadata, Monitoring and Run paths on one server.
type comboFake struct {
	mu   sync.Mutex
	hits map[string]int // by path, "query_range" for every Prometheus call
	// gate, when set, holds the token request until it is closed; entered gets one send per held request.
	gate     chan struct{}
	released bool
	entered  chan struct{}
	// override answers a request instead of the default; ok false falls through.
	override func(path, query string) (code int, body string, ok bool)
	forms    []map[string]string // query, start, end, step of each query_range
}

// release opens the gate once; cleanup calls it so a failed test cannot leave a request held.
func (f *comboFake) release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gate != nil && !f.released {
		f.released = true
		close(f.gate)
	}
}

func (f *comboFake) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

func (f *comboFake) googleHits() (prom, run int) {
	return f.count("query_range"), f.count("run")
}

func newComboFake(t *testing.T, clock func() time.Time) (*comboFake, *MetricsSource, *bytes.Buffer) {
	t.Helper()
	f := &comboFake{hits: map[string]int{}, entered: make(chan struct{}, 64)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		query := r.PostForm.Get("query")
		key := r.URL.Path
		switch {
		case strings.HasSuffix(key, "/query_range"):
			key = "query_range"
		case strings.HasPrefix(key, "/v2/"):
			key = "run"
		}
		f.mu.Lock()
		f.hits[key]++
		if key == "query_range" {
			f.forms = append(f.forms, map[string]string{"query": query, "start": r.PostForm.Get("start"), "end": r.PostForm.Get("end"), "step": r.PostForm.Get("step")})
		}
		gate, ov := f.gate, f.override
		if r.URL.Path == tokenPath && gate != nil {
			f.entered <- struct{}{}
		}
		f.mu.Unlock()
		if r.URL.Path == tokenPath && gate != nil {
			<-gate
		}
		if ov != nil {
			if code, body, ok := ov(r.URL.Path, query); ok {
				w.WriteHeader(code)
				io.WriteString(w, body)
				return
			}
		}
		switch {
		case r.URL.Path == tokenPath:
			io.WriteString(w, `{"access_token":"tok","expires_in":3599}`)
		case r.URL.Path == projPath:
			io.WriteString(w, "p1")
		case r.URL.Path == regionPath:
			io.WriteString(w, "projects/1/regions/r1")
		case key == "query_range":
			io.WriteString(w, metricsMatrix)
		case strings.HasSuffix(r.URL.Path, "/revisions"):
			io.WriteString(w, metricsRevsBody)
		default:
			io.WriteString(w, servicesBody)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(f.release)
	g := &Google{Monitoring: srv.URL, Run: srv.URL, Client: srv.Client(), Meta: NewMetadata(srv.URL, srv.Client(), clock)}
	var logs bytes.Buffer
	return f, NewMetricsSource(g, clock, slog.New(slog.NewTextHandler(&logs, nil))), &logs
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)} }

func allUnavailable(t *testing.T, m Metrics) {
	t.Helper()
	for _, p := range m.Panels {
		if p.Available || p.Series == nil || len(p.Series) != 0 {
			t.Fatalf("panel %s = %+v, want unavailable with empty series", p.Key, p)
		}
	}
	if m.Limits.Available || m.Limits.Services == nil || len(m.Limits.Services) != 0 {
		t.Fatalf("limits = %+v", m.Limits)
	}
	if m.Deploys.Available || m.Deploys.Revisions == nil || len(m.Deploys.Revisions) != 0 {
		t.Fatalf("deploys = %+v", m.Deploys)
	}
}

func TestMetricsSource_BodyCarriesEverySection(t *testing.T) {
	clk := newClock()
	f, s, _ := newComboFake(t, clk.now)
	m, err := s.Get(t.Context(), "1h", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(raw, &keys)
	var got []string
	for k := range keys {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"connection_budget", "deploys", "end", "limits", "panels", "range", "service", "start", "step_seconds"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	if len(m.Panels) != len(Panels) {
		t.Fatalf("panels = %d, want %d", len(m.Panels), len(Panels))
	}
	for i, p := range m.Panels {
		if p.Key != Panels[i].Key || p.Unit != Panels[i].Unit || !p.Available || len(p.Series) == 0 {
			t.Fatalf("panel %d = %+v, want %s available with data", i, p, Panels[i].Key)
		}
	}
	fp := panelByKey(t, m, FleetP95Key)
	if fp.Unit != "ms" || !fp.Available || len(fp.Series) != 1 {
		t.Fatalf("fleet panel = %+v, want ms, available, one series", fp)
	}
	if m.StepSeconds != int(Ranges["1h"].Step.Seconds()) || m.ConnectionBudget != ConnectionBudget || m.Range != "1h" {
		t.Fatalf("header = %+v", m)
	}
	from, to := Window(Ranges["1h"], clk.now())
	if !m.Start.Equal(from) || !m.End.Equal(to) {
		t.Fatalf("window = %v..%v, want %v..%v", m.Start, m.End, from, to)
	}
	if !m.Limits.Available || len(m.Limits.Services) == 0 || !m.Deploys.Available {
		t.Fatalf("limits/deploys = %+v / %+v", m.Limits, m.Deploys)
	}
	if prom, _ := f.googleHits(); prom != len(Panels) {
		t.Fatalf("prometheus hits = %d, want %d", prom, len(Panels))
	}
}

func TestMetricsSource_CachesSixtySecondsPerKey(t *testing.T) {
	clk := newClock()
	f, s, _ := newComboFake(t, clk.now)
	n := len(Panels)
	get := func(r, svc string, wantProm int) {
		t.Helper()
		if _, err := s.Get(t.Context(), r, svc); err != nil {
			t.Fatal(err)
		}
		if prom, _ := f.googleHits(); prom != wantProm {
			t.Fatalf("after Get(%q,%q) prometheus hits = %d, want %d", r, svc, prom, wantProm)
		}
	}
	get("1h", "", n)
	clk.advance(MetricsRefreshAfter - time.Second)
	get("1h", "", n)
	clk.advance(-(MetricsRefreshAfter - 2*time.Second))
	get("6h", "", 2*n)
	get("1h", "app", 3*n)
	clk.advance(MetricsStaleTTL - time.Second)
	get("1h", "", 4*n)
}

// The poll arrives MetricsTTL after the previous fetch ended; it must not wait on Google.
func TestMetricsSource_APollOneTTLAfterAFetchDoesNotWaitOnGoogle(t *testing.T) {
	const fetchTime = 1500 * time.Millisecond
	clk := newClock()
	f, s, _ := newComboFake(t, clk.now)
	var first sync.Once
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	f.mu.Lock()
	f.override = func(path, _ string) (int, string, bool) {
		if !strings.HasSuffix(path, "/query_range") {
			return 0, "", false
		}
		select {
		case <-hold:
		default:
			first.Do(func() { clk.advance(fetchTime) })
		}
		return 0, "", false
	}
	f.mu.Unlock()

	body, err := s.Get(t.Context(), "1h", "")
	if err != nil {
		t.Fatal(err)
	}

	// From here every Google query blocks until cleanup.
	f.mu.Lock()
	f.override = func(path, _ string) (int, string, bool) {
		if strings.HasSuffix(path, "/query_range") {
			<-hold
		}
		return 0, "", false
	}
	f.mu.Unlock()
	clk.advance(MetricsTTL)

	type result struct {
		m   Metrics
		err error
	}
	done := make(chan result, 1)
	go func() {
		m, err := s.Get(t.Context(), "1h", "")
		done <- result{m, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if !reflect.DeepEqual(r.m, body) {
			t.Fatal("second Get did not return the first body")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second Get waited on a Google fetch instead of serving the cached body")
	}
}

// holdQueries blocks every query_range request until the returned func runs; each held request sends on entered.
func holdQueries(t *testing.T, f *comboFake) (release func()) {
	t.Helper()
	gate := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.override = func(path, _ string) (int, string, bool) {
		if strings.HasSuffix(path, "/query_range") {
			f.entered <- struct{}{}
			<-gate
		}
		return 0, "", false
	}
	return release
}

// idle waits until the key's background fetch has stored its entry.
func idle(t *testing.T, s *MetricsSource) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.mu.Lock()
		n := len(s.inflight)
		s.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a fetch stayed in flight")
		}
		time.Sleep(time.Millisecond)
	}
}

// getFast fails when Get does not return within a second: it would be waiting on Google.
func getFast(t *testing.T, s *MetricsSource, ctx context.Context) Metrics {
	t.Helper()
	type res struct {
		m   Metrics
		err error
	}
	c := make(chan res, 1)
	go func() { m, err := s.Get(ctx, "1h", ""); c <- res{m, err} }()
	select {
	case r := <-c:
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r.m
	case <-time.After(time.Second):
		t.Fatal("Get waited on a Google fetch")
		return Metrics{}
	}
}

func TestMetricsSource_AgeBoundariesDecideServeRefreshOrWait(t *testing.T) {
	n := len(Panels)
	for _, tc := range []struct {
		name     string
		age      time.Duration
		wantProm int  // after the Get and any background fetch
		blocks   bool // the Get waits on Google
	}{
		{"just under MetricsRefreshAfter is a plain hit", MetricsRefreshAfter - time.Nanosecond, n, false},
		{"exactly MetricsRefreshAfter is served and refreshed", MetricsRefreshAfter, 2 * n, false},
		{"just under MetricsTTL is served and refreshed", MetricsTTL - time.Nanosecond, 2 * n, false},
		{"just under MetricsStaleTTL is served and refreshed", MetricsStaleTTL - time.Nanosecond, 2 * n, false},
		{"exactly MetricsStaleTTL waits", MetricsStaleTTL, 2 * n, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := newClock()
			f, s, _ := newComboFake(t, clk.now)
			first, err := s.Get(t.Context(), "1h", "")
			if err != nil {
				t.Fatal(err)
			}
			clk.advance(tc.age)
			if tc.blocks {
				release := holdQueries(t, f)
				done := make(chan struct{})
				go func() { defer close(done); _, _ = s.Get(t.Context(), "1h", "") }()
				<-f.entered
				select {
				case <-done:
					t.Fatal("Get returned before the Google fetch ended")
				default:
				}
				release()
				<-done
			} else if got := getFast(t, s, t.Context()); !reflect.DeepEqual(got, first) {
				t.Fatal("did not serve the cached body")
			}
			idle(t, s)
			if prom, _ := f.googleHits(); prom != tc.wantProm {
				t.Fatalf("prometheus hits = %d, want %d", prom, tc.wantProm)
			}
		})
	}
}

func TestMetricsSource_ConcurrentStaleGetsStartOneBackgroundFetch(t *testing.T) {
	clk := newClock()
	f, s, _ := newComboFake(t, clk.now)
	if _, err := s.Get(t.Context(), "1h", ""); err != nil {
		t.Fatal(err)
	}
	clk.advance(MetricsTTL)
	release := holdQueries(t, f)
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() { defer wg.Done(); getFast(t, s, t.Context()) }()
	}
	wg.Wait()
	<-f.entered
	release()
	idle(t, s)
	if prom, _ := f.googleHits(); prom != 2*len(Panels) {
		t.Fatalf("prometheus hits = %d, want %d: more than one background fetch", prom, 2*len(Panels))
	}
}

func TestMetricsSource_AStaleGetWhileAFetchIsInFlightStartsNone(t *testing.T) {
	clk := newClock()
	f, s, _ := newComboFake(t, clk.now)
	if _, err := s.Get(t.Context(), "1h", ""); err != nil {
		t.Fatal(err)
	}
	clk.advance(MetricsTTL)
	release := holdQueries(t, f)
	getFast(t, s, t.Context())
	<-f.entered
	for range 3 {
		clk.advance(time.Second)
		getFast(t, s, t.Context())
	}
	release()
	idle(t, s)
	if prom, _ := f.googleHits(); prom != 2*len(Panels) {
		t.Fatalf("prometheus hits = %d, want %d", prom, 2*len(Panels))
	}
}

func TestMetricsSource_TheBackgroundFetchReplacesTheEntryEvenWhenTheCallerLeft(t *testing.T) {
	clk := newClock()
	_, s, _ := newComboFake(t, clk.now)
	first, err := s.Get(t.Context(), "1h", "")
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(MetricsTTL)
	ctx, cancel := context.WithCancel(t.Context())
	getFast(t, s, ctx)
	cancel()
	idle(t, s)
	next := getFast(t, s, t.Context())
	if !next.End.After(first.End) {
		t.Fatalf("next poll End = %v, want after %v: the entry was not replaced", next.End, first.End)
	}
	for _, p := range next.Panels {
		if !p.Available {
			t.Fatalf("panel %s unavailable: the cancelled caller aborted the fetch", p.Key)
		}
	}
}

// AC2: an open screen polling every MetricsTTL is never served an entry older than one period plus a fetch.
func TestMetricsSource_SixtySecondPollsNeverWaitAndStayWithinOnePeriodPlusAFetch(t *testing.T) {
	const fetchTime = 1500 * time.Millisecond
	clk := newClock()
	f, s, _ := newComboFake(t, clk.now)
	f.mu.Lock()
	f.override = func(path, _ string) (int, string, bool) {
		if strings.HasSuffix(path, "/query_range") {
			time.Sleep(300 * time.Millisecond)
		}
		return 0, "", false
	}
	f.mu.Unlock()
	if _, err := s.Get(t.Context(), "1h", ""); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 8; i++ {
		clk.advance(MetricsTTL)
		start := time.Now()
		m := getFast(t, s, t.Context())
		if d := time.Since(start); d > 150*time.Millisecond {
			t.Fatalf("poll %d took %v, want a cache answer", i, d)
		}
		if age := clk.now().Sub(m.End); age > MetricsTTL+fetchTime+windowLag {
			t.Fatalf("poll %d served data %v old, want at most one period plus one fetch", i, age)
		}
		clk.advance(fetchTime)
		idle(t, s)
		clk.advance(-fetchTime)
	}
}

// A hit leaves the entry unrefreshed, so a poll that follows by one more period must still not wait.
func TestMetricsSource_AHitThenAPollWithJitterDoesNotWait(t *testing.T) {
	clk := newClock()
	f, s, _ := newComboFake(t, clk.now)
	if _, err := s.Get(t.Context(), "1h", ""); err != nil {
		t.Fatal(err)
	}
	holdQueries(t, f)
	clk.advance(MetricsTTL - 100*time.Millisecond)
	getFast(t, s, t.Context())
	clk.advance(MetricsTTL + 200*time.Millisecond)
	getFast(t, s, t.Context())
}

func TestMetricsSource_CallsNothingUntilAsked(t *testing.T) {
	f, _, _ := newComboFake(t, newClock().now)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.hits) != 0 {
		t.Fatalf("hits before Get = %v", f.hits)
	}
}

func TestMetricsSource_ConcurrentCallersShareOneFetch(t *testing.T) {
	f, s, _ := newComboFake(t, newClock().now)
	f.gate = make(chan struct{})
	const callers = 10
	bodies := make([]Metrics, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := s.Get(t.Context(), "1h", "")
			if err != nil {
				t.Error(err)
			}
			bodies[i] = m
		}()
	}
	<-f.entered
	f.release()
	wg.Wait()
	for i := 1; i < callers; i++ {
		if !reflect.DeepEqual(bodies[i], bodies[0]) {
			t.Fatalf("caller %d got a different body", i)
		}
	}
	if prom, _ := f.googleHits(); prom != len(Panels) {
		t.Fatalf("prometheus hits = %d, want %d", prom, len(Panels))
	}
	if n := f.count(tokenPath); n != 1 {
		t.Fatalf("token hits = %d, want 1", n)
	}
}

func TestMetricsSource_AWaiterThatLeavesDoesNotFailTheOthers(t *testing.T) {
	f, s, _ := newComboFake(t, newClock().now)
	f.gate = make(chan struct{})
	type res struct {
		m   Metrics
		err error
	}
	a := make(chan res, 1)
	go func() {
		m, err := s.Get(t.Context(), "1h", "")
		a <- res{m, err}
	}()
	<-f.entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Get(ctx, "1h", ""); err != context.Canceled {
		t.Fatalf("leaving joiner err = %v, want context.Canceled", err)
	}
	f.release()
	r := <-a
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, p := range r.m.Panels {
		if !p.Available {
			t.Fatalf("panel %s unavailable after a joiner left", p.Key)
		}
	}
}

func TestMetricsSource_ALeaderThatLeavesStillFillsTheCache(t *testing.T) {
	f, s, _ := newComboFake(t, newClock().now)
	f.gate = make(chan struct{})
	type res struct {
		m   Metrics
		err error
	}
	ctxA, cancelA := context.WithCancel(t.Context())
	a, b := make(chan res, 1), make(chan res, 1)
	go func() {
		m, err := s.Get(ctxA, "1h", "")
		a <- res{m, err}
	}()
	<-f.entered
	go func() {
		m, err := s.Get(t.Context(), "1h", "")
		b <- res{m, err}
	}()
	cancelA()
	f.release()
	if r := <-a; r.err != context.Canceled {
		t.Fatalf("leader err = %v, want context.Canceled", r.err)
	}
	rb := <-b
	if rb.err != nil {
		t.Fatal(rb.err)
	}
	for _, p := range rb.m.Panels {
		if !p.Available {
			t.Fatalf("panel %s unavailable after the leader left", p.Key)
		}
	}
	prom, run := f.googleHits()
	if _, err := s.Get(t.Context(), "1h", ""); err != nil {
		t.Fatal(err)
	}
	if p2, r2 := f.googleHits(); p2 != prom || r2 != run {
		t.Fatalf("third Get called Google: prom %d->%d run %d->%d", prom, p2, run, r2)
	}
}

func TestMetricsSource_AFailedPanelIsUnavailableAndTheRestServe(t *testing.T) {
	for _, c := range []struct{ key, needle string }{{"sql_connections", "num_backends"}, {"startup_latency_p95_ms", "container_startup_latencies_bucket"}} {
		t.Run(c.key, func(t *testing.T) {
			f, s, logs := newComboFake(t, newClock().now)
			f.override = func(_, query string) (int, string, bool) {
				return 500, "boom secret", strings.Contains(query, c.needle)
			}
			m, err := s.Get(t.Context(), "1h", "")
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range m.Panels {
				if p.Key == c.key {
					if p.Available || p.Series == nil || len(p.Series) != 0 {
						t.Fatalf("%s = %+v", c.key, p)
					}
				} else if !p.Available || len(p.Series) == 0 {
					t.Fatalf("panel %s = %+v, want available with data", p.Key, p)
				}
			}
			if !m.Limits.Available || !m.Deploys.Available {
				t.Fatal("limits/deploys should be available")
			}
			if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), c.key) {
				t.Fatalf("log = %q, want a Warn naming %s", logs.String(), c.key)
			}
			if raw, _ := json.Marshal(m); bytes.Contains(raw, []byte("boom")) || bytes.Contains(raw, []byte("secret")) {
				t.Fatalf("body leaks Google text: %s", raw)
			}
		})
	}
}

func TestMetricsSource_ARunFailureMarksOnlyItsSection(t *testing.T) {
	f, s, _ := newComboFake(t, newClock().now)
	f.override = func(path, _ string) (int, string, bool) {
		return 403, "", strings.HasSuffix(path, "/revisions")
	}
	m, err := s.Get(t.Context(), "1h", "")
	if err != nil {
		t.Fatal(err)
	}
	if m.Deploys.Available || m.Deploys.Revisions == nil || len(m.Deploys.Revisions) != 0 {
		t.Fatalf("deploys = %+v", m.Deploys)
	}
	if !m.Limits.Available {
		t.Fatal("limits should be available")
	}
	for _, p := range m.Panels {
		if !p.Available {
			t.Fatalf("panel %s unavailable", p.Key)
		}
	}
}

func TestMetricsSource_AMetadataFailureMarksEverySection(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		code             int
	}{
		{"token 500", tokenPath, "", 500},
		{"project id 500", projPath, "", 500},
		{"region without regions/", regionPath, "africa-south1", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := newClock()
			f, s, _ := newComboFake(t, clk.now)
			f.override = func(path, _ string) (int, string, bool) { return tc.code, tc.body, path == tc.path }
			m, err := s.Get(t.Context(), "1h", "")
			if err != nil {
				t.Fatal(err)
			}
			allUnavailable(t, m)
			if prom, run := f.googleHits(); prom != 0 || run != 0 {
				t.Fatalf("prometheus/run hits = %d/%d, want 0/0", prom, run)
			}
			clk.advance(MetricsStaleTTL)
			if _, err := s.Get(t.Context(), "1h", ""); err != nil {
				t.Fatal(err)
			}
			if n := f.count(tc.path); n != 2 {
				t.Fatalf("%s hits = %d, want 2", tc.path, n)
			}
		})
	}
}

func TestMetricsSource_AnUnavailableSectionEncodesEmptyArrays(t *testing.T) {
	for name, fail := range map[string]func(path string) bool{
		"every path":      func(string) bool { return true },
		"google not meta": func(p string) bool { return p != tokenPath && p != projPath && p != regionPath },
	} {
		t.Run(name, func(t *testing.T) {
			f, s, _ := newComboFake(t, newClock().now)
			f.override = func(path, _ string) (int, string, bool) { return 500, "", fail(path) }
			m, err := s.Get(t.Context(), "1h", "")
			if err != nil {
				t.Fatal(err)
			}
			allUnavailable(t, m)
			raw, _ := json.Marshal(m)
			if bytes.Contains(raw, []byte("null")) {
				t.Fatalf("body has null: %s", raw)
			}
		})
	}
}

func TestMetricsSource_APartialResultIsCachedAndLeaksNoGoogleText(t *testing.T) {
	clk := newClock()
	f, s, _ := newComboFake(t, clk.now)
	f.override = func(_, query string) (int, string, bool) {
		return 500, "boom secret", strings.Contains(query, "num_backends")
	}
	m, err := s.Get(t.Context(), "1h", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	if len(raw) == 0 || bytes.Contains(raw, []byte("boom")) || bytes.Contains(raw, []byte("secret")) || bytes.Contains(raw, []byte("500")) {
		t.Fatalf("body leaks Google text: %s", raw)
	}
	prom, run := f.googleHits()
	clk.advance(MetricsRefreshAfter - time.Second)
	again, err := s.Get(t.Context(), "1h", "")
	if err != nil {
		t.Fatal(err)
	}
	if p2, r2 := f.googleHits(); p2 != prom || r2 != run {
		t.Fatalf("partial result refetched inside 60 s: prom %d->%d run %d->%d", prom, p2, run, r2)
	}
	if !reflect.DeepEqual(again, m) {
		t.Fatal("cached partial body differs")
	}
}

func TestMetricsSource_ALimitsFailureMarksOnlyItsSection(t *testing.T) {
	f, s, _ := newComboFake(t, newClock().now)
	f.override = func(path, _ string) (int, string, bool) {
		return 403, "", strings.HasSuffix(path, "/services")
	}
	m, err := s.Get(t.Context(), "1h", "")
	if err != nil {
		t.Fatal(err)
	}
	if m.Limits.Available || m.Limits.Services == nil || len(m.Limits.Services) != 0 {
		t.Fatalf("limits = %+v", m.Limits)
	}
	if !m.Deploys.Available || m.Deploys.Revisions == nil {
		t.Fatalf("deploys = %+v", m.Deploys)
	}
	if len(m.Panels) == 0 {
		t.Fatal("no panels")
	}
	for _, p := range m.Panels {
		if !p.Available {
			t.Fatalf("panel %s unavailable", p.Key)
		}
	}
}

func TestMetricsSource_DifferentKeysFetchSeparately(t *testing.T) {
	f, s, _ := newComboFake(t, newClock().now)
	var wg sync.WaitGroup
	for _, k := range [][2]string{{"1h", ""}, {"6h", ""}, {"1h", "app"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Get(t.Context(), k[0], k[1]); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if prom, _ := f.googleHits(); prom != 3*len(Panels) {
		t.Fatalf("prometheus hits = %d, want %d", prom, 3*len(Panels))
	}
}

// panicHandler blocks the first log call until proceed closes, then panics.
type panicHandler struct {
	slog.Handler
	proceed chan struct{}
}

func (panicHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h panicHandler) Handle(context.Context, slog.Record) error {
	<-h.proceed
	panic("log boom")
}

// An hour-old entry with a fetch in flight is not served: the second caller joins the fetch.
func TestMetricsSource_AVeryOldEntryWithAFetchInFlightJoinsIt(t *testing.T) {
	clk := newClock()
	f, s, _ := newComboFake(t, clk.now)
	first, err := s.Get(t.Context(), "1h", "")
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Hour)
	release := holdQueries(t, f)
	type res struct {
		m   Metrics
		err error
	}
	get := func() chan res {
		c := make(chan res, 1)
		go func() { m, err := s.Get(t.Context(), "1h", ""); c <- res{m, err} }()
		return c
	}
	leader := get()
	<-f.entered
	joiner := get()
	select {
	case <-joiner:
		t.Fatal("Get served the hour-old entry instead of joining the fetch")
	case <-time.After(200 * time.Millisecond):
	}
	release()
	r := <-joiner
	<-leader
	if r.err != nil || !r.m.End.After(first.End) {
		t.Fatalf("joiner = %v, %v: want the fetch's fresh body", r.m.End, r.err)
	}
}

func TestMetricsSource_APanickingFetchReleasesWaitersAndCachesNothing(t *testing.T) {
	clk := newClock()
	f, s, _ := newComboFake(t, clk.now)
	proceed := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-proceed:
		default:
			close(proceed)
		}
	})
	s.log = slog.New(panicHandler{proceed: proceed})
	// Get reads the clock under s.mu, so the second read means the joiner has registered on the call.
	var reads atomic.Int32
	s.now = func() time.Time { reads.Add(1); return clk.now() }
	f.override = func(path, _ string) (int, string, bool) { return 500, "", path == tokenPath }
	leader := make(chan any, 1)
	go func() {
		defer func() { leader <- recover() }()
		_, _ = s.Get(t.Context(), "1h", "")
	}()
	for reads.Load() < 1 {
		runtime.Gosched()
	}
	joined := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				joined <- errors.New("joiner panicked: it led its own fetch instead of joining")
			}
		}()
		_, err := s.Get(t.Context(), "1h", "")
		joined <- err
	}()
	for reads.Load() < 2 {
		runtime.Gosched()
	}
	s.mu.Lock()
	s.mu.Unlock()
	close(proceed)
	if r := <-leader; r == nil {
		t.Fatal("leader did not panic")
	}
	select {
	case err := <-joined:
		if !errors.Is(err, errFetchAborted) {
			t.Fatalf("joiner err = %v, want errFetchAborted", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waiter hung after the leader panicked")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.inflight) != 0 || len(s.entries) != 0 {
		t.Fatalf("inflight=%d entries=%d, want 0/0", len(s.inflight), len(s.entries))
	}
}

func TestMetricsSource_AnEmptyAnswerFromRunEncodesEmptyArrays(t *testing.T) {
	f, s, _ := newComboFake(t, newClock().now)
	f.override = func(path, _ string) (int, string, bool) {
		return 200, `{}`, strings.HasPrefix(path, "/v2/")
	}
	m, err := s.Get(t.Context(), "1h", "")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Limits.Available || !m.Deploys.Available {
		t.Fatalf("limits/deploys = %+v / %+v, want available", m.Limits, m.Deploys)
	}
	raw, _ := json.Marshal(m)
	if !bytes.Contains(raw, []byte(`"services":[]`)) || !bytes.Contains(raw, []byte(`"revisions":[]`)) || bytes.Contains(raw, []byte("null")) {
		t.Fatalf("body = %s", raw)
	}
}

func matrixOf(names ...string) string {
	var rs []string
	for _, n := range names {
		m := `{}`
		if n != "" {
			m = `{"service_name":"` + n + `"}`
		}
		rs = append(rs, `{"metric":`+m+`,"values":[[1000,"1.5"]]}`)
	}
	return `{"status":"success","data":{"resultType":"matrix","result":[` + strings.Join(rs, ",") + `]}}`
}

// previewFake answers Cloud Run queries with runNames and SQL queries with an unnamed series, and records every request.
func previewFake(t *testing.T, suffix string, runNames ...string) (*MetricsSource, func() []string) {
	t.Helper()
	f, s, _ := newComboFake(t, newClock().now)
	s.g.Suffix = suffix
	var mu sync.Mutex
	var seen []string
	f.override = func(path, query string) (int, string, bool) {
		mu.Lock()
		seen = append(seen, path+" "+query)
		mu.Unlock()
		if strings.HasSuffix(path, "/query_range") {
			if strings.Contains(query, "cloud_run_revision") {
				return 200, matrixOf(runNames...), true
			}
			return 200, matrixOf(""), true
		}
		return 0, "", false
	}
	return s, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) }
}

func panelNames(m Metrics, cloudRun bool) map[string][]string {
	out := map[string][]string{}
	for i, p := range Panels {
		if p.CloudRun != cloudRun || p.Key == FleetP95Key {
			continue
		}
		for _, s := range m.Panels[i].Series {
			out[p.Key] = append(out[p.Key], s.Name)
		}
	}
	return out
}

func TestMetricsSource_APreviewTrimsItsSuffixFromSeriesNames(t *testing.T) {
	s, seen := previewFake(t, "-pr-545", "gateway-pr-545", "app-pr-545")
	m, err := s.Get(t.Context(), "1h", "")
	if err != nil {
		t.Fatal(err)
	}
	cloud := panelNames(m, true)
	if len(cloud["startup_latency_p95_ms"]) == 0 {
		t.Error("startup panel has no series")
	}
	for k, names := range cloud {
		if sort.Strings(names); !reflect.DeepEqual(names, []string{"app", "gateway"}) {
			t.Errorf("%s names = %v", k, names)
		}
	}
	for k, names := range panelNames(m, false) {
		if !reflect.DeepEqual(names, []string{"database"}) {
			t.Errorf("%s names = %v", k, names)
		}
	}
	for _, r := range seen() {
		if strings.Contains(r, "cloud_run_revision") && !strings.Contains(r, `service_name=~".+-pr-545"`) {
			t.Errorf("Cloud Run query without the PR matcher: %s", r)
		}
	}
}

func TestMetricsSource_APreviewTrimsOnlyItsOwnSuffix(t *testing.T) {
	s, _ := previewFake(t, "-pr-54", "gateway-pr-54", "gateway-pr-545", "gateway-pr-546")
	m, err := s.Get(t.Context(), "1h", "")
	if err != nil {
		t.Fatal(err)
	}
	for k, names := range panelNames(m, true) {
		if want := []string{"gateway", "gateway-pr-545", "gateway-pr-546"}; !reflect.DeepEqual(names, want) {
			t.Errorf("%s names = %v, want %v", k, names, want)
		}
	}
}

func TestMetricsSource_APreviewPickTargetsItsPRService(t *testing.T) {
	s, seen := previewFake(t, "-pr-545", "gateway-pr-545")
	m, err := s.Get(t.Context(), "6h", "gateway")
	if err != nil {
		t.Fatal(err)
	}
	if m.Service != "gateway" {
		t.Fatalf("service = %q", m.Service)
	}
	var sawRevs bool
	for _, r := range seen() {
		switch {
		case strings.Contains(r, "cloud_run_revision"):
			if !strings.Contains(r, `service_name="gateway-pr-545"`) {
				t.Errorf("query: %s", r)
			}
		case strings.HasSuffix(strings.Fields(r)[0], "/revisions"):
			sawRevs = true
			if p := strings.Fields(r)[0]; p != "/v2/projects/p1/locations/r1/services/gateway-pr-545/revisions" {
				t.Errorf("revisions path = %s", p)
			}
		}
	}
	if !sawRevs {
		t.Fatal("no revisions request")
	}
}

func TestMetricsSource_ProductionNamesAreUnchanged(t *testing.T) {
	s, seen := previewFake(t, "", "gateway")
	m, err := s.Get(t.Context(), "1h", "")
	if err != nil {
		t.Fatal(err)
	}
	for k, names := range panelNames(m, true) {
		if !reflect.DeepEqual(names, []string{"gateway"}) {
			t.Errorf("%s names = %v", k, names)
		}
	}
	for _, r := range seen() {
		if strings.Contains(r, "=~") {
			t.Errorf("production query holds =~: %s", r)
		}
	}
}

func TestMetricsSource_APreviewReadsOnlyItsMetadataProject(t *testing.T) {
	s, seen := previewFake(t, "-pr-545", "gateway-pr-545")
	if _, err := s.Get(t.Context(), "1h", ""); err != nil {
		t.Fatal(err)
	}
	var n int
	for _, r := range seen() {
		p := strings.Fields(r)[0]
		switch {
		case strings.HasSuffix(p, "/query_range"):
			n++
			if !strings.HasPrefix(p, "/v1/projects/p1/") {
				t.Errorf("query_range path = %s", p)
			}
		case strings.HasPrefix(p, "/v2/"):
			n++
			if !strings.HasPrefix(p, "/v2/projects/p1/") {
				t.Errorf("run path = %s", p)
			}
		}
	}
	if n < len(Panels)+2 {
		t.Fatalf("saw %d Google requests, want at least %d", n, len(Panels)+2)
	}
}

func TestMetricsSource_APreviewFleetListsOnlyItsOwnLimitsAndDeploys(t *testing.T) {
	f, s, _ := newComboFake(t, newClock().now)
	s.g.Suffix = "-pr-54"
	f.override = func(path, _ string) (int, string, bool) {
		switch {
		case strings.HasSuffix(path, "/revisions"):
			return 200, `{"revisions":[` + rev("gateway-pr-545", "gateway-pr-545-00001-aaa", "2026-10-23T11:00:00Z") + `,` +
				rev("gateway-pr-54", "gateway-pr-54-00001-bbb", "2026-10-23T11:10:00Z") + `]}`, true
		case strings.HasPrefix(path, "/v2/"):
			return 200, svcBody("gateway-pr-545", "gateway-pr-54", "app"), true
		}
		return 0, "", false
	}
	m, err := s.Get(t.Context(), "1h", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := limitNames(m.Limits.Services); !reflect.DeepEqual(got, []string{"gateway"}) {
		t.Errorf("limits = %v", got)
	}
	if len(m.Deploys.Revisions) != 1 || m.Deploys.Revisions[0].Service != "gateway" || m.Deploys.Revisions[0].Revision != "gateway-pr-54-00001-bbb" {
		t.Errorf("deploys = %+v", m.Deploys.Revisions)
	}
}

func panelByKey(t *testing.T, m Metrics, key string) PanelData {
	t.Helper()
	for _, p := range m.Panels {
		if p.Key == key {
			return p
		}
	}
	t.Fatalf("no panel %s", key)
	return PanelData{}
}

func (f *comboFake) formOf(t *testing.T, match func(q string) bool) map[string]string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.forms {
		if match(m["query"]) {
			return m
		}
	}
	t.Fatal("no matching query_range form")
	return nil
}

func isFleetQuery(q string) bool { return strings.Contains(q, "sum by (le)") }

func TestMetricsSource_FleetP95UsesTheWindowAndStepOfTheLatencyPanel(t *testing.T) {
	clk := newClock()
	f, s, _ := newComboFake(t, clk.now)
	if _, err := s.Get(t.Context(), "6h", ""); err != nil {
		t.Fatal(err)
	}
	fleet := f.formOf(t, isFleetQuery)
	lat := f.formOf(t, func(q string) bool {
		return strings.Contains(q, "0.95") && strings.Contains(q, "request_latencies_bucket") && strings.Contains(q, "sum by (service_name, le)") && !strings.Contains(q, "100 *")
	})
	for _, k := range []string{"start", "end", "step"} {
		if fleet[k] != lat[k] || fleet[k] == "" {
			t.Errorf("%s: fleet %q, latency %q", k, fleet[k], lat[k])
		}
	}
	if want := strconv.FormatInt(clk.now().Add(-windowLag).Unix(), 10); fleet["end"] != want {
		t.Errorf("fleet end = %q, want now-4m %q", fleet["end"], want)
	}
}

func TestMetricsSource_AnUnlabelledFleetAnswerIsNamedFleet(t *testing.T) {
	f, s, _ := newComboFake(t, newClock().now)
	f.override = func(_, query string) (int, string, bool) { return 200, matrixOf(""), isFleetQuery(query) }
	m, err := s.Get(t.Context(), "1h", "")
	if err != nil {
		t.Fatal(err)
	}
	fp := panelByKey(t, m, FleetP95Key)
	if len(fp.Series) != 1 || fp.Series[0].Name != "fleet" {
		t.Fatalf("fleet series = %+v, want one named fleet", fp.Series)
	}
}

func TestMetricsSource_APreviewFleetSeriesKeepsItsName(t *testing.T) {
	s, seen := previewFake(t, "-pr-545", "gateway-pr-545")
	m, err := s.Get(t.Context(), "1h", "")
	if err != nil {
		t.Fatal(err)
	}
	fp := panelByKey(t, m, FleetP95Key)
	if len(fp.Series) == 0 {
		t.Fatal("no fleet series")
	}
	for _, sr := range fp.Series {
		if sr.Name != FleetSeriesName {
			t.Errorf("fleet series name = %q", sr.Name)
		}
	}
	var sawFleet bool
	for _, r := range seen() {
		if isFleetQuery(r) && strings.Contains(r, "cloud_run_revision") {
			sawFleet = true
			if !strings.Contains(r, `service_name=~".+-pr-545"`) {
				t.Errorf("fleet query without the PR matcher: %s", r)
			}
		}
	}
	if !sawFleet {
		t.Fatal("no fleet query recorded")
	}
}

func TestMetricsSource_AFailedFleetPanelIsUnavailableAndTheRestServe(t *testing.T) {
	f, s, logs := newComboFake(t, newClock().now)
	f.override = func(_, query string) (int, string, bool) { return 500, "boom secret", isFleetQuery(query) }
	m, err := s.Get(t.Context(), "1h", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range m.Panels {
		if p.Key == FleetP95Key {
			if p.Available || p.Series == nil || len(p.Series) != 0 {
				t.Fatalf("fleet panel = %+v", p)
			}
		} else if !p.Available || len(p.Series) == 0 {
			t.Fatalf("panel %s = %+v, want available with data", p.Key, p)
		}
	}
	raw, _ := json.Marshal(m)
	if !bytes.Contains(raw, []byte(`"series":[]`)) || bytes.Contains(raw, []byte("null")) || bytes.Contains(raw, []byte("boom secret")) {
		t.Fatalf("body = %s", raw)
	}
	if !strings.Contains(logs.String(), FleetP95Key) {
		t.Fatalf("log = %q, want it to name %s", logs.String(), FleetP95Key)
	}
}

func TestMetricsSource_AnEmptyAnswerIsAvailableWithNoSeries(t *testing.T) {
	for key, match := range map[string]func(string) bool{
		FleetP95Key:              isFleetQuery,
		"startup_latency_p95_ms": func(q string) bool { return strings.Contains(q, "container_startup_latencies_bucket") },
	} {
		t.Run(key, func(t *testing.T) {
			f, s, _ := newComboFake(t, newClock().now)
			f.override = func(_, query string) (int, string, bool) { return 200, emptyMatrix, match(query) }
			m, err := s.Get(t.Context(), "1h", "")
			if err != nil {
				t.Fatal(err)
			}
			fp := panelByKey(t, m, key)
			if !fp.Available || fp.Series == nil || len(fp.Series) != 0 {
				t.Fatalf("%s panel = %+v", key, fp)
			}
			if raw, _ := json.Marshal(m); bytes.Contains(raw, []byte("null")) {
				t.Fatalf("body holds null: %s", raw)
			}
		})
	}
}
