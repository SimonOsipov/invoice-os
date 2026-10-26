package gateway

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestThrottle(maxKeys int, window time.Duration) (*SignInThrottle, *testClock) {
	clk := newTestClock()
	return NewSignInThrottle("sign-in", SignInMaxFailures, maxKeys, window, clk.Now), clk
}

// reserveN calls Reserve n times and fails on the first refusal.
func reserveN(t *testing.T, th *SignInThrottle, email string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		if !th.Reserve(email) {
			t.Fatalf("Reserve(%q) #%d = false, want true", email, i)
		}
	}
}

func TestSignInThrottle_BlocksAfterMax(t *testing.T) {
	th, _ := newTestThrottle(SignInMaxKeys, SignInWindow)
	reserveN(t, th, "a@x.com", SignInMaxFailures)
	if th.Reserve("a@x.com") {
		t.Fatalf("Reserve #%d = true, want false", SignInMaxFailures+1)
	}
	if !th.Reserve("b@x.com") {
		t.Fatal("Reserve for another address = false, want true")
	}
}

func TestSignInThrottle_WindowBoundary(t *testing.T) {
	th, clk := newTestThrottle(SignInMaxKeys, SignInWindow)
	reserveN(t, th, "a@x.com", SignInMaxFailures)

	clk.Advance(SignInWindow - time.Nanosecond)
	if th.Reserve("a@x.com") {
		t.Fatal("Reserve at t0+window-1ns = true, want false")
	}
	clk.Advance(time.Nanosecond)
	if !th.Reserve("a@x.com") {
		t.Fatal("Reserve at t0+window = false, want true")
	}
}

func TestSignInThrottle_NineStillAllowed(t *testing.T) {
	th, _ := newTestThrottle(SignInMaxKeys, SignInWindow)
	reserveN(t, th, "a@x.com", SignInMaxFailures-1)
	if !th.Reserve("a@x.com") {
		t.Fatalf("Reserve #%d = false, want true", SignInMaxFailures)
	}
}

func TestSignInThrottle_ResetClears(t *testing.T) {
	th, _ := newTestThrottle(SignInMaxKeys, SignInWindow)
	reserveN(t, th, "a@x.com", SignInMaxFailures)
	if th.Reserve("a@x.com") {
		t.Fatal("Reserve past max before Reset = true, want false")
	}
	th.Reset("a@x.com")
	if !th.Reserve("a@x.com") {
		t.Fatal("Reserve after Reset = false, want true")
	}
}

func TestSignInThrottle_RefundReturnsOne(t *testing.T) {
	th, _ := newTestThrottle(SignInMaxKeys, SignInWindow)
	reserveN(t, th, "a@x.com", SignInMaxFailures)
	th.Refund("a@x.com")
	if !th.Reserve("a@x.com") {
		t.Fatal("Reserve after Refund = false, want true")
	}
	if th.Reserve("a@x.com") {
		t.Fatal("second Reserve after one Refund = true, want false")
	}
}

func TestSignInThrottle_KeyIsNormalised(t *testing.T) {
	th, _ := newTestThrottle(SignInMaxKeys, SignInWindow)
	reserveN(t, th, " A@X.com ", SignInMaxFailures)
	if th.Reserve("a@x.com") {
		t.Fatal(`Reserve("a@x.com") after max on " A@X.com " = true, want false`)
	}
	if !th.Reserve("b@x.com") {
		t.Fatal("Reserve for another address = false, want true")
	}
}

func TestSignInThrottle_ConcurrentReserveBounded(t *testing.T) {
	th, _ := newTestThrottle(SignInMaxKeys, SignInWindow)
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		ok    int
		start = make(chan struct{})
	)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if th.Reserve("a@x.com") {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if ok != SignInMaxFailures {
		t.Fatalf("successful Reserves = %d, want exactly %d", ok, SignInMaxFailures)
	}
}

func TestSignInThrottle_FullRefusesNewKeepsHeld(t *testing.T) {
	th, _ := newTestThrottle(3, SignInWindow)
	for _, e := range []string{"a@x.com", "b@x.com", "c@x.com"} {
		reserveN(t, th, e, 1)
	}
	if th.Reserve("d@x.com") {
		t.Fatal("Reserve for a new address on a full map = true, want false")
	}
	if !th.Reserve("a@x.com") {
		t.Fatal("Reserve for a held address on a full map = false, want true")
	}
}

func TestSignInThrottle_FullSweepsExpiredFirst(t *testing.T) {
	// A window under a minute: only the full-map sweep can free room, not the cadence.
	const window = 10 * time.Second
	th, clk := newTestThrottle(3, window)
	for _, e := range []string{"a@x.com", "b@x.com", "c@x.com"} {
		reserveN(t, th, e, 1)
	}
	if th.Reserve("d@x.com") {
		t.Fatal("Reserve for a new address before expiry = true, want false")
	}
	clk.Advance(window)
	if !th.Reserve("d@x.com") {
		t.Fatal("Reserve for a new address after keys expired = false, want true")
	}
}

func TestSignInThrottle_SweepIsAmortised(t *testing.T) {
	th, clk := newTestThrottle(SignInMaxKeys, SignInWindow)
	granted := 0
	for i := 0; i < 1000; i++ {
		if th.Reserve(fmt.Sprintf("u%d@x.com", i)) {
			granted++
		}
		clk.Advance(time.Millisecond)
	}
	if granted != 1000 {
		t.Fatalf("granted Reserves = %d, want 1000", granted)
	}
	if n := th.SweepsForTest(); n > 1 {
		t.Fatalf("sweeps within one minute = %d, want <= 1", n)
	}
	clk.Advance(time.Minute)
	th.Reserve("late@x.com")
	if n := th.SweepsForTest(); n != 2 {
		t.Fatalf("sweeps after one more minute = %d, want 2", n)
	}
}

func TestSignInThrottle_SharedFailsClosedWhenTheDatabaseIsDown(t *testing.T) {
	const addr = "victim-7f3a9c@corp.example"
	th, _ := newTestThrottle(SignInMaxKeys, SignInWindow)
	log, logs := newCaptureLog()
	th.Shared(deadPool(t), log)

	admitted := th.Reserve(addr)
	th.Refund(addr)
	th.Reset(addr)

	if admitted {
		t.Error("Reserve with the database down = true, want false (fail closed, D6)")
	}
	var errs []string
	for _, r := range logs.all() {
		if r.level == slog.LevelError {
			errs = append(errs, r.text)
		}
		if strings.Contains(r.text, addr) || strings.Contains(r.text, "victim") {
			t.Errorf("log line carries the address: %q", r.text)
		}
	}
	want := []string{"sign-in throttle: sweep", "sign-in throttle: reserve", "sign-in throttle: refund", "sign-in throttle: reset"}
	if len(errs) != len(want) {
		t.Fatalf("ERROR records = %d %q, want %d (sweep, reserve, refund, reset)", len(errs), errs, len(want))
	}
	for i, w := range want {
		if !strings.HasPrefix(errs[i], w) {
			t.Errorf("ERROR #%d = %q, want it to start %q", i+1, errs[i], w)
		}
	}
}

func TestSignInThrottle_AdmitReturnsTheDatabaseError(t *testing.T) {
	shared, _ := newTestThrottle(SignInMaxKeys, SignInWindow)
	shared.Shared(deadPool(t), slog.New(slog.DiscardHandler))
	if ok, err := shared.Admit("a@x.com"); ok || err == nil {
		t.Errorf("Admit with the database down = %v, %v; want false and an error", ok, err)
	}

	local, _ := newTestThrottle(SignInMaxKeys, SignInWindow)
	if ok, err := local.Admit("a@x.com"); !ok || err != nil {
		t.Errorf("in-process Admit = %v, %v; want true and nil", ok, err)
	}
}

// Holds today: a nil pool is the in-process count, with no database touched.
func TestSignInThrottle_NilPoolKeepsTheInProcessCount(t *testing.T) {
	th, _ := newTestThrottle(SignInMaxKeys, SignInWindow)
	th.Shared(nil, slog.New(slog.DiscardHandler))

	reserveN(t, th, "a@x.com", SignInMaxFailures)

	if th.Reserve("a@x.com") {
		t.Errorf("Reserve #%d = true, want false", SignInMaxFailures+1)
	}
}

func TestSignInThrottle_SharedSweepsOncePerIntervalAndOnlyInAdmit(t *testing.T) {
	th, clk := newTestThrottle(SignInMaxKeys, SignInWindow)
	log, logs := newCaptureLog()
	th.Shared(deadPool(t), log)
	sweeps := func() int {
		n := 0
		for _, e := range errorRecords(logs) {
			if strings.HasPrefix(e, "sign-in throttle: sweep") {
				n++
			}
		}
		return n
	}

	th.Refund("a@x.com")
	th.Reset("a@x.com")
	if n := sweeps(); n != 0 {
		t.Fatalf("sweeps after Refund and Reset = %d, want 0: only Admit sweeps", n)
	}
	th.Reserve("a@x.com")
	th.Reserve("a@x.com")
	if n := sweeps(); n != 1 {
		t.Fatalf("sweeps after two Reserves in one instant = %d, want 1", n)
	}
	clk.Advance(signInSweepEvery - time.Nanosecond)
	th.Reserve("a@x.com")
	if n := sweeps(); n != 1 {
		t.Fatalf("sweeps just under %v later = %d, want 1", signInSweepEvery, n)
	}
	clk.Advance(time.Nanosecond)
	th.Reserve("a@x.com")
	if n := sweeps(); n != 2 {
		t.Fatalf("sweeps %v after the first = %d, want 2", signInSweepEvery, n)
	}
}

// A stalled database must not block other callers on t.mu.
func TestSignInThrottle_SharedAdmitDoesNotHoldTheMutexAcrossSQL(t *testing.T) {
	th, _ := newTestThrottle(SignInMaxKeys, SignInWindow)
	th.Shared(stalledPool(t), slog.New(slog.DiscardHandler))
	go th.Admit("a@x.com")

	deadline := time.Now().Add(3 * time.Second)
	for {
		if th.mu.TryLock() {
			inSQL := !th.lastSweep.IsZero()
			th.mu.Unlock()
			if inSQL {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("t.mu stayed locked while Admit waited on the database, want it released before any SQL")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
