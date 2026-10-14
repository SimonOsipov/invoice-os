package systemhealth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
	"github.com/SimonOsipov/invoice-os/internal/validation/codelist"
)

func fakeFleet(status string, down int, err error) FleetReader {
	return func(context.Context) (gateway.FleetHealth, error) {
		f := gateway.FleetHealth{Status: status, Services: []gateway.ServiceHealth{{Status: "up"}}}
		for i := 0; i < down; i++ {
			f.Services = append(f.Services, gateway.ServiceHealth{Status: "down"})
		}
		return f, err
	}
}

func TestSampler_RecordsTheFleetAndTheSlot(t *testing.T) {
	super, app := dbTestPools(t)
	for _, tc := range []struct {
		name   string
		fleet  FleetReader
		status string
		down   *int
		warn   bool
	}{
		{"ok", fakeFleet("ok", 0, nil), "ok", intp(0), false},
		{"degraded", fakeFleet("degraded", 2, nil), "degraded", intp(2), false},
		{"error", fakeFleet("", 0, errors.New("gateway down")), "unknown", nil, true},
	} {
		slot := testSlot(t, super, 2004, 0)
		var logs bytes.Buffer
		s := &Sampler{Store: &Store{Pool: app}, Fleet: tc.fleet, Logger: slog.New(slog.NewTextHandler(&logs, nil)),
			Now: func() time.Time { return slot.Add(2*time.Minute + 13*time.Second) }}
		if err := s.Sample(context.Background()); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		r, ok := readRow(t, super, slot)
		if !ok || r.status != tc.status || (r.down == nil) != (tc.down == nil) || (tc.down != nil && *r.down != *tc.down) {
			t.Errorf("%s: row = %+v (found %v), want %s,%v", tc.name, r, ok, tc.status, tc.down)
		}
		if warned := bytes.Contains(logs.Bytes(), []byte("level=WARN")); warned != tc.warn {
			t.Errorf("%s: warned = %v, want %v (%q)", tc.name, warned, tc.warn, logs.String())
		}
	}
}

func intp(n int) *int { return &n }

func TestSampler_TwoReplicasWriteOneSamplePerSlot(t *testing.T) {
	super, app := dbTestPools(t)
	slot := testSlot(t, super, 2005, 0)
	mk := func(f FleetReader) *Sampler {
		return &Sampler{Store: &Store{Pool: app}, Fleet: f, Logger: slog.Default(), Now: func() time.Time { return slot }}
	}
	a, b := mk(fakeFleet("ok", 0, nil)), mk(fakeFleet("degraded", 1, nil))
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, s := range []*Sampler{a, b} {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = s.Sample(context.Background()) }()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := super.QueryRow(context.Background(), `SELECT count(*) FROM public.health_samples WHERE taken_at = $1`, slot).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows for the slot = %d (%v), want 1", n, err)
	}
	if r, _ := readRow(t, super, slot); r.status != "ok" && r.status != "degraded" {
		t.Errorf("status = %q", r.status)
	}
}

func brokenSampler(t *testing.T) *Sampler {
	return &Sampler{Store: &Store{Pool: unreachablePool(t)}, Fleet: fakeFleet("ok", 0, nil), Logger: slog.Default(), Now: time.Now}
}

func TestSampler_ReturnsAStoreError(t *testing.T) {
	if err := brokenSampler(t).Sample(context.Background()); err == nil {
		t.Fatal("want a store error")
	}
}

func TestSampler_AStoreErrorReachesSentryOnEveryRun(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "systemhealth")
	w := codelist.NewWorker(20*time.Millisecond, brokenSampler(t).Sample)
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(rec.Events()) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("captured %d events, want >= 2", len(rec.Events()))
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := w.Stop(ctx); err != nil {
		t.Errorf("Stop: %v", err)
	}
}
