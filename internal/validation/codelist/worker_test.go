package codelist

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

func startWorker(t *testing.T, interval time.Duration, fn func(context.Context) error) *Worker {
	t.Helper()
	w := NewWorker(interval, fn)
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { stopWorker(t, w) })
	return w
}

func stopWorker(t *testing.T, w *Worker) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := w.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// scripted runs script[i] on call i+1; calls past the script park until ctx is done.
func scripted(calls *atomic.Int64, script ...func() error) func(context.Context) error {
	return func(ctx context.Context) error {
		n := int(calls.Add(1))
		if n <= len(script) {
			return script[n-1]()
		}
		<-ctx.Done()
		return ctx.Err()
	}
}

func failWith(msg string) func() error { return func() error { return errors.New(msg) } }

func eventText(e *sentry.Event) string {
	text := e.Message
	for _, ex := range e.Exception {
		text += " " + ex.Value
	}
	return text
}

func TestWorker_RunsAtStart(t *testing.T) {
	called := make(chan struct{}, 1)
	startWorker(t, time.Hour, func(context.Context) error {
		select {
		case called <- struct{}{}:
		default:
		}
		return nil
	})
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("fn not called within 1s at an interval of 1h")
	}
}

func TestWorker_RunsEveryInterval(t *testing.T) {
	var calls atomic.Int64
	startWorker(t, 10*time.Millisecond, func(context.Context) error { calls.Add(1); return nil })
	waitFor(t, "3 calls", func() bool { return calls.Load() >= 3 })
}

func TestWorker_SingleFlight(t *testing.T) {
	var in, max atomic.Int64
	gate := make(chan struct{})
	w := startWorker(t, time.Millisecond, func(context.Context) error {
		n := in.Add(1)
		defer in.Add(-1)
		for {
			m := max.Load()
			if n <= m || max.CompareAndSwap(m, n) {
				break
			}
		}
		<-gate
		return nil
	})
	waitFor(t, "first entry", func() bool { return max.Load() >= 1 })
	time.Sleep(50 * time.Millisecond)
	close(gate)
	stopWorker(t, w)
	if got := max.Load(); got != 1 {
		t.Errorf("max concurrent runs = %d, want 1", got)
	}
}

func TestWorker_FailedRunReachesSentry(t *testing.T) {
	_, rec, want := sentrytest.Boot(t, "validation")
	var calls atomic.Int64
	startWorker(t, time.Millisecond, scripted(&calls, failWith("nrs down")))
	waitFor(t, "parked second call", func() bool { return calls.Load() >= 2 })

	e := rec.One(t, want)
	if got := eventText(e); !strings.Contains(got, "nrs down") {
		t.Errorf("event text = %q, want it to carry %q", got, "nrs down")
	}
}

func TestWorker_HeldPullReachesSentry(t *testing.T) {
	s, srv, l, _ := seedN(t, 20)
	srv.set(200, pull(cEntries(1, 17, "x")))
	s.lists = []List{l}
	_, rec, want := sentrytest.Boot(t, "validation")
	startWorker(t, time.Hour, s.SyncAll)
	waitFor(t, "the event", func() bool { return len(rec.Events()) >= 1 })

	e := rec.One(t, want)
	if got := eventText(e); !strings.Contains(got, l.Name) || !strings.Contains(got, "held") {
		t.Errorf("event text = %q, want it to carry %q and %q", got, l.Name, "held")
	}
}

func TestWorker_EachFailedRunIsReported(t *testing.T) {
	_, rec, want := sentrytest.Boot(t, "validation")
	var calls atomic.Int64
	startWorker(t, time.Millisecond, scripted(&calls, failWith("nrs down"), failWith("nrs down")))
	waitFor(t, "parked third call", func() bool { return calls.Load() >= 3 })

	events := rec.Events()
	if len(events) != 2 {
		t.Fatalf("recorded %d events, want 2", len(events))
	}
	for _, e := range events {
		sentrytest.AssertLabels(t, e, want)
	}
}

func TestWorker_PanicIsRecoveredAndReported(t *testing.T) {
	_, rec, want := sentrytest.Boot(t, "validation")
	var calls atomic.Int64
	startWorker(t, time.Millisecond, scripted(&calls,
		func() error { panic("sync boom") },
		func() error { return nil },
	))
	waitFor(t, "call after the panic", func() bool { return calls.Load() >= 3 })

	if e := rec.One(t, want); e.Level != sentry.LevelFatal {
		t.Errorf("event level = %q, want %q", e.Level, sentry.LevelFatal)
	}
}

func TestWorker_SuccessReportsNothing(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "validation")
	var calls atomic.Int64
	startWorker(t, time.Millisecond, func(context.Context) error { calls.Add(1); return nil })
	waitFor(t, "2 calls", func() bool { return calls.Load() >= 2 })

	rec.None(t)
}

func TestWorker_ShutdownCancelReportsNothing(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "validation")
	entered := make(chan struct{})
	w := NewWorker(time.Hour, func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	})
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("fn never started")
	}
	stopWorker(t, w)

	rec.None(t)
}

func TestWorker_StopHalts(t *testing.T) {
	var calls atomic.Int64
	w := startWorker(t, 5*time.Millisecond, func(context.Context) error { calls.Add(1); return nil })
	waitFor(t, "a call", func() bool { return calls.Load() >= 1 })
	stopWorker(t, w)

	after := calls.Load()
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != after {
		t.Errorf("calls grew from %d to %d after Stop", after, got)
	}
}
