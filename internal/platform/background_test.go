package platform

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// stubWorker records whether Start/Stop were called, standing in for a River client.
type stubWorker struct {
	started  atomic.Bool
	stopped  atomic.Bool
	startErr error
}

func (s *stubWorker) Start(context.Context) error {
	s.started.Store(true)
	return s.startErr
}

func (s *stubWorker) Stop(context.Context) error {
	s.stopped.Store(true)
	return nil
}

// A registered background worker must start alongside the server and drain (Stop) when
// the process is signalled to shut down — the lifecycle cmd/submission's River pool rides.
func TestRunStartsAndDrainsBackgroundWorker(t *testing.T) {
	t.Setenv("PORT", "0") // ephemeral port
	app, err := New("svc")
	if err != nil {
		t.Fatal(err)
	}
	w := &stubWorker{}
	app.AddBackgroundWorker(w)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for !w.started.Load() {
		if time.Now().After(deadline) {
			t.Fatal("background worker was not started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel() // simulate SIGINT

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error on graceful shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not shut down within 5s")
	}
	if !w.stopped.Load() {
		t.Error("background worker was not drained (Stop never called)")
	}
}

// A worker that fails to start aborts startup — the process must not come up half-wired.
func TestRunAbortsWhenBackgroundWorkerStartFails(t *testing.T) {
	t.Setenv("PORT", "0")
	app, err := New("svc")
	if err != nil {
		t.Fatal(err)
	}
	app.AddBackgroundWorker(&stubWorker{startErr: errors.New("boom")})

	if err := app.Run(context.Background()); err == nil {
		t.Fatal("Run should return the worker's start error, got nil")
	}
}
