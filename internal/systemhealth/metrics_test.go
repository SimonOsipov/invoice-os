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
	clk.advance(59 * time.Second)
	get("1h", "", n)
	clk.advance(-58 * time.Second)
	get("6h", "", 2*n)
	get("1h", "app", 3*n)
	clk.advance(59 * time.Second)
	get("1h", "", 4*n)
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
	f, s, logs := newComboFake(t, newClock().now)
	f.override = func(_, query string) (int, string, bool) {
		return 500, "boom secret", strings.Contains(query, "num_backends")
	}
	m, err := s.Get(t.Context(), "1h", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range m.Panels {
		if p.Key == "sql_connections" {
			if p.Available || p.Series == nil || len(p.Series) != 0 {
				t.Fatalf("sql_connections = %+v", p)
			}
		} else if !p.Available || len(p.Series) == 0 {
			t.Fatalf("panel %s = %+v, want available with data", p.Key, p)
		}
	}
	if !m.Limits.Available || !m.Deploys.Available {
		t.Fatal("limits/deploys should be available")
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "sql_connections") {
		t.Fatalf("log = %q, want a Warn naming sql_connections", logs.String())
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
			clk.advance(MetricsTTL)
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
	clk.advance(MetricsTTL - time.Second)
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
