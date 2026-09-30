// Package sentrytest records the Sentry events a platform service sends; only tests import it.
package sentrytest

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

const (
	fakeDSN        = "https://public@example.com/1"
	fakeRailwaySHA = "d0e09998b867ee781c56969b28f9497a2c2f1595"
)

// Leak markers are URL- and JSON-safe, so encoding cannot disguise a leak.
const (
	MarkerTIN  = "87654321-0009"
	MarkerAmt  = "9999999.99"
	MarkerIRN  = "INV-SECRET-2026"
	MarkerCred = "cred-SECRET-4411"
)

var leakMarkers = []string{MarkerTIN, MarkerAmt, MarkerIRN, MarkerCred}

// Labels are the fields every counted event must carry.
type Labels struct {
	Environment string
	Release     string
	ServerName  string
}

// Recorder is a sentry.Transport that keeps every event it is sent.
type Recorder struct {
	mu     sync.Mutex
	events []*sentry.Event
}

var _ sentry.Transport = (*Recorder)(nil)

func (r *Recorder) Configure(sentry.ClientOptions) {}

func (r *Recorder) SendEvent(e *sentry.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *Recorder) Flush(time.Duration) bool { return true }

func (r *Recorder) FlushWithContext(context.Context) bool { return true }

func (r *Recorder) Close() {}

// Events flushes the hub and returns the error-type events; transactions and check-ins are skipped.
func (r *Recorder) Events() []*sentry.Event {
	sentry.Flush(time.Second)
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*sentry.Event
	for _, e := range r.events {
		if e.Type == "" {
			out = append(out, e)
		}
	}
	return out
}

// LogEvents flushes the hub and returns the log-type events.
func (r *Recorder) LogEvents() []*sentry.Event {
	sentry.Flush(time.Second)
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*sentry.Event
	for _, e := range r.events {
		if e.Type == "log" {
			out = append(out, e)
		}
	}
	return out
}

// Logs flushes the hub and returns every log record the client sent.
func (r *Recorder) Logs() []sentry.Log {
	var out []sentry.Log
	for _, e := range r.LogEvents() {
		out = append(out, e.Logs...)
	}
	return out
}

// One fails unless exactly one event was recorded, asserts its labels and returns it.
func (r *Recorder) One(t testing.TB, want Labels) *sentry.Event {
	t.Helper()
	events := r.Events()
	if len(events) != 1 {
		t.Fatalf("recorded %d events, want 1: %s", len(events), summary(events))
	}
	AssertLabels(t, events[0], want)
	return events[0]
}

// None fails if any event was recorded. Pair it with a counted positive on the same recorder.
func (r *Recorder) None(t testing.TB) {
	t.Helper()
	if events := r.Events(); len(events) != 0 {
		t.Errorf("recorded %d events, want none: %s", len(events), summary(events))
	}
}

// AssertLabels checks the environment, release and server name of one event.
func AssertLabels(t testing.TB, e *sentry.Event, want Labels) {
	t.Helper()
	if e.Environment != want.Environment {
		t.Errorf("event environment = %q, want %q", e.Environment, want.Environment)
	}
	if e.Release != want.Release {
		t.Errorf("event release = %q, want %q", e.Release, want.Release)
	}
	if e.ServerName != want.ServerName {
		t.Errorf("event server_name = %q, want %q", e.ServerName, want.ServerName)
	}
}

// AssertNoLeak fails on any marker in an event's wire JSON or its envelope trace header.
func AssertNoLeak(t testing.TB, events []*sentry.Event) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("no event recorded; nothing to check for leaks")
	}
	for i, e := range events {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal event %d: %v", i, err)
		}
		dsc, err := json.Marshal(e.GetDynamicSamplingContext())
		if err != nil {
			t.Fatalf("marshal DSC of event %d: %v", i, err)
		}
		for _, m := range leakMarkers {
			if strings.Contains(string(raw), m) {
				t.Errorf("event %d (type %q) carries marker %q: %s", i, e.Type, m, raw)
			}
			if strings.Contains(string(dsc), m) {
				t.Errorf("event %d (type %q) trace header carries marker %q: %s", i, e.Type, m, dsc)
			}
		}
	}
}

// Boot builds the service with production's Railway labels and re-points the
// client platform.New installed at a Recorder, keeping its options and hooks.
func Boot(t testing.TB, service string) (*platform.App, *Recorder, Labels) {
	t.Helper()
	t.Setenv("SENTRY_DSN", fakeDSN)
	t.Setenv("RAILWAY_ENVIRONMENT_NAME", "production")
	t.Setenv("ENVIRONMENT", "development")
	t.Setenv("RAILWAY_GIT_COMMIT_SHA", fakeRailwaySHA)

	prevLogger := slog.Default()
	hub := sentry.CurrentHub()
	hub.BindClient(nil)
	t.Cleanup(func() {
		hub.BindClient(nil)
		slog.SetDefault(prevLogger)
	})

	app, err := platform.New(service)
	if err != nil {
		t.Fatalf("platform.New(%q): %v", service, err)
	}
	installed := hub.Client()
	if installed == nil || installed.Options().Dsn == "" {
		t.Fatal("platform.New installed no Sentry client")
	}
	opts := installed.Options()
	installed.Close()

	rec := &Recorder{}
	opts.Transport = rec
	client, err := sentry.NewClient(opts)
	if err != nil {
		t.Fatalf("new client from the installed options: %v", err)
	}
	hub.BindClient(client)

	want := Labels{Environment: "production", Release: "unstamped-" + fakeRailwaySHA, ServerName: service}
	if sha := platform.BuildSHA; sha != "" && sha != "dev" {
		want.Release = sha
	}
	return app, rec, want
}

func summary(events []*sentry.Event) string {
	var s string
	for i, e := range events {
		if i > 0 {
			s += "; "
		}
		s += e.Message
		for _, ex := range e.Exception {
			s += " [" + ex.Type + ": " + ex.Value + "]"
		}
	}
	return s
}
