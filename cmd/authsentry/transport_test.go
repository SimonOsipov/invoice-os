package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

type ingest struct {
	srv *httptest.Server

	mu       sync.Mutex
	status   func(id string) int
	headers  http.Header
	attempts map[string]int
	got      []string
	last     *http.Request
	lastBody []byte
}

func newIngest(t *testing.T, status func(id string) int) *ingest {
	t.Helper()
	in := &ingest{status: status, attempts: map[string]int{}}
	in.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		lines := strings.SplitN(body.String(), "\n", 3)
		var hdr map[string]any
		_ = json.Unmarshal([]byte(lines[0]), &hdr)
		id, _ := hdr["event_id"].(string)
		in.mu.Lock()
		in.attempts[id]++
		st := in.status(id)
		for k, v := range in.headers {
			w.Header()[k] = v
		}
		if st/100 == 2 {
			in.got = append(in.got, id)
		}
		in.last, in.lastBody = r, body.Bytes()
		in.mu.Unlock()
		w.WriteHeader(st)
	}))
	t.Cleanup(in.srv.Close)
	return in
}

func (in *ingest) dsn() string {
	return "http://public@" + strings.TrimPrefix(in.srv.URL, "http://") + "/1"
}

func (in *ingest) received() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]string(nil), in.got...)
}

func (in *ingest) attemptsFor(id string) int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.attempts[id]
}

type sleeper struct {
	mu    sync.Mutex
	waits []time.Duration
	// gate, when set, makes sleep announce itself and block until released.
	gate     chan struct{}
	sleeping chan struct{}
}

func (s *sleeper) sleep(d time.Duration) {
	s.mu.Lock()
	s.waits = append(s.waits, d)
	g, sl := s.gate, s.sleeping
	s.mu.Unlock()
	if g != nil {
		select {
		case sl <- struct{}{}:
		default:
		}
		<-g
	}
}

func (s *sleeper) recorded() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.waits...)
}

func newTestTransport(t *testing.T, dsn string, client *http.Client, s *sleeper) *bufferedTransport {
	t.Helper()
	if client == nil {
		client = http.DefaultClient
	}
	tr := newBufferedTransport(client, time.Now, s.sleep)
	tr.Configure(sentry.ClientOptions{Dsn: dsn})
	t.Cleanup(tr.Close)
	return tr
}

func ev(i int) *sentry.Event {
	return &sentry.Event{EventID: sentry.EventID(fmt.Sprintf("%032d", i)), Message: "m"}
}

func eventID(i int) string { return string(ev(i).EventID) }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestTransport_SendNeverBlocks(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	t.Cleanup(srv.Close)
	tr := newTestTransport(t, "http://public@"+strings.TrimPrefix(srv.URL, "http://")+"/1", nil, &sleeper{})
	t.Cleanup(func() { close(release) })

	slowest := make(chan time.Duration, 1)
	go func() {
		var worst time.Duration
		for i := 0; i < 150; i++ {
			start := time.Now()
			tr.SendEvent(ev(i))
			if i >= 100 {
				worst = max(worst, time.Since(start))
			}
		}
		slowest <- worst
	}()
	select {
	case d := <-slowest:
		if d > 5*time.Millisecond {
			t.Fatalf("SendEvent took %v", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SendEvent blocked")
	}
}

func TestTransport_FullBufferDropsTheOldest(t *testing.T) {
	var up bool
	in := newIngest(t, func(string) int {
		if up {
			return 200
		}
		return 503
	})
	s := &sleeper{gate: make(chan struct{}), sleeping: make(chan struct{}, 1)}
	tr := newTestTransport(t, in.dsn(), nil, s)

	for i := 1; i <= 105; i++ {
		tr.SendEvent(ev(i))
	}
	<-s.sleeping
	in.mu.Lock()
	up = true
	in.mu.Unlock()
	close(s.gate)

	eventually(t, "100 events", func() bool { return len(in.received()) == 100 })
	got := in.received()
	for i, id := range got {
		if id != eventID(i+6) {
			t.Fatalf("position %d: got %s, want %s", i, id, eventID(i+6))
		}
	}
}

func TestTransport_CapacityBoundary(t *testing.T) {
	in := newIngest(t, func(string) int { return 503 })
	tr := newTestTransport(t, in.dsn(), nil, &sleeper{gate: make(chan struct{}), sleeping: make(chan struct{}, 1)})

	for i := 1; i <= 100; i++ {
		tr.SendEvent(ev(i))
	}
	tr.mu.Lock()
	n, first := len(tr.queue), tr.queue[0].EventID
	tr.mu.Unlock()
	if n != 100 || first != ev(1).EventID {
		t.Fatalf("after 100 sends: len=%d first=%s", n, first)
	}
	tr.SendEvent(ev(101))
	tr.mu.Lock()
	n, first = len(tr.queue), tr.queue[0].EventID
	tr.mu.Unlock()
	if n != 100 || first != ev(2).EventID {
		t.Fatalf("after 101 sends: len=%d first=%s", n, first)
	}
}

func TestTransport_RetriesWithBackoff(t *testing.T) {
	calls := 0
	in := newIngest(t, func(string) int {
		calls++
		if calls < 3 {
			return 503
		}
		return 200
	})
	s := &sleeper{}
	tr := newTestTransport(t, in.dsn(), nil, s)
	tr.SendEvent(ev(1))

	eventually(t, "delivery", func() bool { return len(in.received()) == 1 })
	if n := in.attemptsFor(eventID(1)); n != 3 {
		t.Fatalf("attempts = %d, want 3", n)
	}
	if w := s.recorded(); len(w) != 2 || w[0] != time.Second || w[1] != 2*time.Second {
		t.Fatalf("waits = %v, want [1s 2s]", w)
	}
}

func TestTransport_BackoffCapsAtFiveMinutes(t *testing.T) {
	in := newIngest(t, func(string) int { return 503 })
	s := &sleeper{}
	tr := newTestTransport(t, in.dsn(), nil, s)
	tr.SendEvent(ev(1))

	eventually(t, "12 waits", func() bool { return len(s.recorded()) >= 12 })
	w := s.recorded()[:12]
	for i, d := range w {
		if d > 5*time.Minute {
			t.Fatalf("wait %d = %v exceeds 5m", i, d)
		}
	}
	if w[10] != 5*time.Minute || w[11] != 5*time.Minute {
		t.Fatalf("last waits = %v %v, want 5m", w[10], w[11])
	}
}

func TestTransport_RetriesANetworkError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	s := &sleeper{gate: make(chan struct{}), sleeping: make(chan struct{}, 1)}
	tr := newBufferedTransport(http.DefaultClient, time.Now, s.sleep)
	t.Cleanup(tr.Close)
	tr.Configure(sentry.ClientOptions{Dsn: "http://public@" + addr + "/1"})
	tr.SendEvent(ev(1))
	select {
	case <-s.sleeping:
	case <-time.After(3 * time.Second):
		t.Fatal("no retry wait after a network error")
	}

	in := newIngest(t, func(string) int { return 200 })
	dsn, _ := sentry.NewDsn(in.dsn())
	tr.mu.Lock()
	tr.dsn = dsn
	tr.mu.Unlock()
	close(s.gate)

	eventually(t, "delivery", func() bool { return len(in.received()) == 1 })
	if w := s.recorded(); len(w) < 1 || w[0] != time.Second {
		t.Fatalf("waits = %v, want first 1s", w)
	}
}

func TestTransport_HonoursRetryAfter(t *testing.T) {
	cases := []struct {
		name    string
		headers http.Header
		want    time.Duration
	}{
		{"Retry-After", http.Header{"Retry-After": {"30"}}, 30 * time.Second},
		{"rate limits", http.Header{"X-Sentry-Rate-Limits": {"60:error:organization"}}, 60 * time.Second},
		{"above an hour", http.Header{"Retry-After": {"7200"}}, time.Hour},
		{"HTTP-date falls back to backoff", http.Header{"Retry-After": {"Wed, 21 Oct 2026 07:28:00 GMT"}}, time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			in := newIngest(t, func(string) int {
				calls++
				if calls == 1 {
					return 429
				}
				return 200
			})
			in.headers = c.headers
			s := &sleeper{}
			tr := newTestTransport(t, in.dsn(), nil, s)
			tr.SendEvent(ev(1))

			eventually(t, "delivery", func() bool { return len(in.received()) == 1 })
			if w := s.recorded(); len(w) != 1 || w[0] != c.want {
				t.Fatalf("waits = %v, want [%v]", w, c.want)
			}
		})
	}
}

func TestTransport_DropsARejectedEvent(t *testing.T) {
	for _, code := range []int{400, 413} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			in := newIngest(t, func(id string) int {
				if id == eventID(1) {
					return code
				}
				return 200
			})
			s := &sleeper{}
			tr := newTestTransport(t, in.dsn(), nil, s)
			tr.SendEvent(ev(1))
			tr.SendEvent(ev(2))

			eventually(t, "B delivered", func() bool { return len(in.received()) == 1 })
			if got := in.received()[0]; got != eventID(2) {
				t.Fatalf("delivered %s, want B", got)
			}
			if n := in.attemptsFor(eventID(1)); n != 1 {
				t.Fatalf("A attempted %d times, want 1", n)
			}
			if w := s.recorded(); len(w) != 0 {
				t.Fatalf("unexpected waits %v", w)
			}
		})
	}
}

func TestTransport_FlushReturnsByTheDeadline(t *testing.T) {
	up := false
	in := newIngest(t, func(string) int {
		if up {
			return 200
		}
		return 503
	})
	s := &sleeper{gate: make(chan struct{}), sleeping: make(chan struct{}, 1)}
	tr := newTestTransport(t, in.dsn(), nil, s)
	tr.SendEvent(ev(1))

	start := time.Now()
	if tr.Flush(200 * time.Millisecond) {
		t.Fatal("Flush returned true while the ingest is down")
	}
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Fatalf("Flush took %v", d)
	}

	in.mu.Lock()
	up = true
	in.mu.Unlock()
	close(s.gate)
	if !tr.Flush(5 * time.Second) {
		t.Fatal("Flush returned false once the ingest is up")
	}
}

func TestTransport_PostsAnEnvelope(t *testing.T) {
	in := newIngest(t, func(string) int { return 200 })
	tr := newTestTransport(t, in.dsn(), nil, &sleeper{})
	tr.SendEvent(&sentry.Event{EventID: ev(1).EventID, Message: "boom", Level: sentry.LevelError})

	eventually(t, "delivery", func() bool { return len(in.received()) == 1 })
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.last.URL.Path != "/api/1/envelope/" {
		t.Fatalf("path = %s", in.last.URL.Path)
	}
	if a := in.last.Header.Get("X-Sentry-Auth"); !strings.Contains(a, "sentry_key=public") {
		t.Fatalf("X-Sentry-Auth = %q", a)
	}
	lines := bytes.Split(bytes.TrimSuffix(in.lastBody, []byte("\n")), []byte("\n"))
	if len(lines) != 3 {
		t.Fatalf("envelope has %d lines", len(lines))
	}
	var hdr map[string]any
	if err := json.Unmarshal(lines[0], &hdr); err != nil || hdr["event_id"] != eventID(1) || hdr["dsn"] == "" {
		t.Fatalf("header = %s (%v)", lines[0], err)
	}
	var item struct {
		Type   string `json:"type"`
		Length int    `json:"length"`
	}
	if err := json.Unmarshal(lines[1], &item); err != nil || item.Type != "event" || item.Length != len(lines[2]) {
		t.Fatalf("item header = %s, body length %d", lines[1], len(lines[2]))
	}
	var body map[string]any
	if err := json.Unmarshal(lines[2], &body); err != nil || body["message"] != "boom" || body["event_id"] != eventID(1) {
		t.Fatalf("body = %s (%v)", lines[2], err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransport_APanickingPostDropsOneEvent(t *testing.T) {
	var mu sync.Mutex
	var delivered []string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		id := strings.SplitN(body.String(), "\n", 2)[0]
		if strings.Contains(id, eventID(1)) {
			panic("boom")
		}
		mu.Lock()
		delivered = append(delivered, id)
		mu.Unlock()
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
	})}
	tr := newTestTransport(t, "http://public@127.0.0.1:1/1", client, &sleeper{})
	tr.SendEvent(ev(1))
	tr.SendEvent(ev(2))

	eventually(t, "B delivered", func() bool { mu.Lock(); defer mu.Unlock(); return len(delivered) == 1 })
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(delivered[0], eventID(2)) {
		t.Fatalf("delivered %v, want only B", delivered)
	}
}

func TestTransport_DroppingTheInFlightHeadKeepsTheRest(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	in := newIngest(t, func(id string) int {
		if id == eventID(1) {
			started <- struct{}{}
			<-release
		}
		return 200
	})
	tr := newTestTransport(t, in.dsn(), nil, &sleeper{})
	tr.SendEvent(ev(1))
	<-started
	for i := 2; i <= 101; i++ {
		tr.SendEvent(ev(i))
	}
	close(release)

	eventually(t, "events 2..101", func() bool { return len(in.received()) >= 100 })
	got := in.received()
	if got[len(got)-1] != eventID(101) {
		t.Fatalf("last delivered %s, want event 101", got[len(got)-1])
	}
	for i := 2; i <= 101; i++ {
		if in.attemptsFor(eventID(i)) != 1 {
			t.Fatalf("event %d attempted %d times, want 1", i, in.attemptsFor(eventID(i)))
		}
	}
}
