package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

const line500 = `{"level":"info","msg":"request completed","status":500,"error":"boom","path":"/token","request_id":"r1"}` + "\n"

// op is one step of the fake GoTrue.
type op struct {
	Stderr string `json:"stderr,omitempty"`
	Stdout string `json:"stdout,omitempty"`
	Repeat int    `json:"repeat,omitempty"`
	Trap   bool   `json:"trap,omitempty"` // print ready, wait for SIGTERM, print the shutdown line
	Kill   bool   `json:"kill,omitempty"`
	Exit   int    `json:"exit,omitempty"`
}

func TestMain(m *testing.M) {
	if spec := os.Getenv("AUTHSENTRY_HELPER"); spec != "" {
		helperGoTrue(spec)
	}
	os.Exit(m.Run())
}

func helperGoTrue(spec string) {
	var ops []op
	if err := json.Unmarshal([]byte(spec), &ops); err != nil {
		panic(err)
	}
	for _, o := range ops {
		n := max(o.Repeat, 1)
		for range n {
			switch {
			case o.Stderr != "":
				fmt.Fprint(os.Stderr, o.Stderr)
			case o.Stdout != "":
				fmt.Fprint(os.Stdout, o.Stdout)
			}
		}
		if o.Trap {
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, syscall.SIGTERM)
			fmt.Fprintln(os.Stderr, `{"msg":"GoTrue API started on: :9999"}`)
			<-ch
			fmt.Fprintln(os.Stderr, `{"msg":"received graceful shutdown signal"}`)
			os.Exit(0)
		}
		if o.Kill {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {}
		}
		if o.Exit != 0 {
			os.Exit(o.Exit)
		}
	}
	os.Exit(0)
}

type recorder struct {
	mu     sync.Mutex
	events []*sentry.Event
	block  chan struct{}
	panics bool
}

func (r *recorder) Configure(sentry.ClientOptions) {}
func (r *recorder) SendEvent(e *sentry.Event) {
	if r.panics {
		panic("transport panic")
	}
	if r.block != nil {
		<-r.block
	}
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}
func (r *recorder) Flush(time.Duration) bool              { return true }
func (r *recorder) FlushWithContext(context.Context) bool { return true }
func (r *recorder) Close()                                {}

func (r *recorder) got() []*sentry.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*sentry.Event(nil), r.events...)
}

type result struct {
	stdout, stderr string
	code           int
	took           time.Duration
}

// runHelper runs the wrapper over the fake GoTrue. tr nil keeps the real transport.
func runHelper(t *testing.T, ops []op, env map[string]string, tr sentry.Transport) result {
	t.Helper()
	spec, _ := json.Marshal(ops)
	t.Setenv("AUTHSENTRY_HELPER", string(spec))
	for _, k := range []string{"SENTRY_DSN", "SENTRY_TEST_EVENT", "DEPLOY_ENV", "PORT", "K_SERVICE"} {
		t.Setenv(k, "")
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	if tr != nil {
		old := newTransport
		newTransport = func() sentry.Transport { return tr }
		t.Cleanup(func() { newTransport = old })
	}
	var out, errb bytes.Buffer
	start := time.Now()
	code := run([]string{os.Args[0]}, os.Getenv, &out, &errb)
	return result{out.String(), errb.String(), code, time.Since(start)}
}

const fakeDSN = "http://public@127.0.0.1:1/1"

func dsnEnv(extra ...string) map[string]string {
	m := map[string]string{"SENTRY_DSN": fakeDSN, "DEPLOY_ENV": "production"}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i]] = extra[i+1]
	}
	return m
}

func TestRun_PassesGoTrueOutputThroughUnchanged(t *testing.T) {
	big := strings.Repeat("y", 70<<10)
	want := `{"a":1}` + "\n" + `{"b":2}` + "\n" + `{"c":3}` + "\n" + big + "\n" + "partial"
	ops := []op{{Stderr: `{"a":1}` + "\n"}, {Stderr: `{"b":2}` + "\n"}, {Stderr: `{"c":3}` + "\n"},
		{Stderr: big + "\n"}, {Stdout: "hi"}, {Stderr: "partial"}}
	r := runHelper(t, ops, nil, nil)
	if r.stderr != want {
		t.Fatalf("stderr differs: got %d bytes, want %d", len(r.stderr), len(want))
	}
	if r.stdout != "hi" || r.code != 0 {
		t.Fatalf("stdout %q code %d", r.stdout, r.code)
	}
}

func TestRun_ExitsWithGoTrueCode(t *testing.T) {
	for _, tc := range []struct {
		ops  []op
		want int
	}{{[]op{{Exit: 0}}, 0}, {[]op{{Exit: 1}}, 1}, {[]op{{Exit: 3}}, 3}, {[]op{{Kill: true}}, 137}} {
		if r := runHelper(t, tc.ops, nil, nil); r.code != tc.want {
			t.Errorf("ops %+v: code %d, want %d", tc.ops, r.code, tc.want)
		}
	}
}

func TestRun_ForwardsSIGTERM(t *testing.T) {
	rec := &recorder{}
	go func() {
		time.Sleep(800 * time.Millisecond)
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()
	r := runHelper(t, []op{{Trap: true}}, dsnEnv(), rec)
	if !strings.Contains(r.stderr, "received graceful shutdown signal") || r.code != 0 {
		t.Fatalf("stderr %q code %d", r.stderr, r.code)
	}
	if n := len(rec.got()); n != 0 {
		t.Fatalf("%d events, want 0", n)
	}
}

func TestRun_A5xxLineReachesSentryLabelled(t *testing.T) {
	rec := &recorder{}
	r := runHelper(t, []op{{Stderr: line500}}, dsnEnv(), rec)
	cfg, _ := platform.LoadConfig("auth")
	evs := rec.got()
	if r.code != 0 || len(evs) != 1 {
		t.Fatalf("code %d, %d events", r.code, len(evs))
	}
	if evs[0].ServerName != "auth" || evs[0].Environment != "production" || evs[0].Release != cfg.Release || cfg.Release == "" {
		t.Fatalf("labels %q %q %q", evs[0].ServerName, evs[0].Environment, evs[0].Release)
	}
}

func TestRun_BootFailureIsOneFatalEvent(t *testing.T) {
	rec := &recorder{}
	r := runHelper(t, []op{{Stderr: `{"level":"fatal","msg":"Failed to load configuration: bad"}` + "\n"}, {Exit: 1}}, dsnEnv(), rec)
	evs := rec.got()
	if r.code != 1 || len(evs) != 1 || evs[0].Level != sentry.LevelFatal || evs[0].Tags["auth.phase"] != "boot" {
		t.Fatalf("code %d events %+v", r.code, evs)
	}
}

func TestRun_MissingGoTrue(t *testing.T) {
	rec := &recorder{}
	t.Setenv("SENTRY_DSN", fakeDSN)
	t.Setenv("DEPLOY_ENV", "production")
	old := newTransport
	newTransport = func() sentry.Transport { return rec }
	t.Cleanup(func() { newTransport = old })
	var out, errb bytes.Buffer
	code := run([]string{"/nonexistent"}, os.Getenv, &out, &errb)
	evs := rec.got()
	if code != 127 || len(evs) != 1 || evs[0].Exception[0].Value != "GoTrue did not start" {
		t.Fatalf("code %d events %+v", code, evs)
	}
}

func TestRun_ABlockedSentryNeverSlowsGoTrue(t *testing.T) {
	rec := &recorder{block: make(chan struct{})}
	t.Cleanup(func() { close(rec.block) })
	r := runHelper(t, []op{{Stderr: line500, Repeat: 10000}}, dsnEnv(), rec)
	if r.code != 0 || strings.Count(r.stderr, "\n") != 10000 {
		t.Fatalf("code %d, %d lines", r.code, strings.Count(r.stderr, "\n"))
	}
}

func TestRun_SentryOutageChangesNothing(t *testing.T) {
	want := strings.Repeat(line500, 5)
	for name, dsn := range map[string]string{
		"closed port": fakeDSN,
		"429":         newIngest(t, func(string) int { return 429 }).dsn(),
		"400":         newIngest(t, func(string) int { return 400 }).dsn(),
	} {
		t.Run(name, func(t *testing.T) {
			r := runHelper(t, []op{{Stderr: line500, Repeat: 5}, {Exit: 3}}, dsnEnv("SENTRY_DSN", dsn), nil)
			if r.stderr != want || r.code != 3 {
				t.Fatalf("stderr %d bytes code %d", len(r.stderr), r.code)
			}
			if r.took > 6*time.Second+time.Second {
				t.Fatalf("took %s", r.took)
			}
		})
	}
}

func TestRun_NoDSNSendsNothing(t *testing.T) {
	in := newIngest(t, func(string) int { return 200 })
	r := runHelper(t, []op{{Stderr: line500}}, map[string]string{"SENTRY_DSN": ""}, nil)
	time.Sleep(200 * time.Millisecond)
	if r.stderr != line500 || len(in.received()) != 0 || in.requests() != 0 {
		t.Fatalf("stderr %q", r.stderr)
	}
}

func TestRun_ABadConfigStillRunsGoTrue(t *testing.T) {
	r := runHelper(t, []op{{Stderr: "ok\n"}}, dsnEnv("PORT", "abc"), &recorder{})
	first, rest, _ := strings.Cut(r.stderr, "\n")
	var w map[string]string
	if err := json.Unmarshal([]byte(first), &w); err != nil {
		t.Fatalf("first line %q: %v", first, err)
	}
	if w["component"] != "authsentry" || w["level"] != "warning" || !strings.HasPrefix(w["msg"], "sentry off") || rest != "ok\n" || r.code != 0 {
		t.Fatalf("stderr %q code %d", r.stderr, r.code)
	}
}

func TestRun_TestEventReadsTheConfigFlag(t *testing.T) {
	for val, want := range map[string]int{"true": 1, "1": 0} {
		rec := &recorder{}
		runHelper(t, []op{{Exit: 0}}, dsnEnv("SENTRY_TEST_EVENT", val), rec)
		evs := rec.got()
		if len(evs) != want {
			t.Fatalf("SENTRY_TEST_EVENT=%s: %d events, want %d", val, len(evs), want)
		}
		if want == 1 {
			e := evs[0]
			if strings.Join(e.Fingerprint, "|") != "sentry-test-event|auth" || e.ServerName != "auth" || e.Environment != "production" {
				t.Fatalf("event %+v", e)
			}
		}
	}
}

func TestRun_APanickingSentryNeverStopsGoTrue(t *testing.T) {
	r := runHelper(t, []op{{Stderr: line500, Repeat: 100}, {Exit: 3}}, dsnEnv(), &recorder{panics: true})
	if r.stderr != strings.Repeat(line500, 100) || r.code != 3 {
		t.Fatalf("stderr %d bytes code %d", len(r.stderr), r.code)
	}
}

func (in *ingest) requests() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return len(in.attempts)
}
