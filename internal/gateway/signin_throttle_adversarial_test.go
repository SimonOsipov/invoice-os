package gateway

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// The window runs from the first counted attempt, not the latest one.
func TestSignInThrottle_WindowStartsAtFirstAttempt(t *testing.T) {
	th, clk := newTestThrottle(SignInMaxKeys, SignInWindow)
	reserveN(t, th, "a@x.com", 1)
	clk.Advance(10 * time.Minute)
	reserveN(t, th, "a@x.com", SignInMaxFailures-1)
	if th.Reserve("a@x.com") {
		t.Fatal("Reserve past max inside the window = true, want false")
	}
	clk.Advance(5 * time.Minute) // t0 + window
	if !th.Reserve("a@x.com") {
		t.Fatal("Reserve at first-attempt + window = false, want true")
	}
}

// A Reserve after expiry opens a new window at now.
func TestSignInThrottle_NewWindowStartsAtNow(t *testing.T) {
	th, clk := newTestThrottle(SignInMaxKeys, SignInWindow)
	reserveN(t, th, "a@x.com", SignInMaxFailures)
	clk.Advance(SignInWindow + 7*time.Minute) // t1
	reserveN(t, th, "a@x.com", SignInMaxFailures)
	if th.Reserve("a@x.com") {
		t.Fatal("Reserve past max in the second window = true, want false")
	}
	clk.Advance(SignInWindow - time.Nanosecond)
	if th.Reserve("a@x.com") {
		t.Fatal("Reserve at t1+window-1ns = true, want false")
	}
	clk.Advance(time.Nanosecond)
	if !th.Reserve("a@x.com") {
		t.Fatal("Reserve at t1+window = false, want true")
	}
}

func TestSignInThrottle_RefundFloorsAtZero(t *testing.T) {
	th, _ := newTestThrottle(SignInMaxKeys, SignInWindow)
	reserveN(t, th, "a@x.com", 1)
	for i := 0; i < 5; i++ {
		th.Refund("a@x.com")
	}
	reserveN(t, th, "a@x.com", SignInMaxFailures)
	if th.Reserve("a@x.com") {
		t.Fatal("extra Refunds banked credit past max, want false")
	}
}

// Refund of an unknown address must not create a key that bypasses the cap.
func TestSignInThrottle_RefundUnknownCreatesNoKey(t *testing.T) {
	th, _ := newTestThrottle(1, SignInWindow)
	reserveN(t, th, "a@x.com", 1)
	th.Refund("b@x.com")
	if th.Reserve("b@x.com") {
		t.Fatal("Refund of an unknown address let it past a full map")
	}
	if !th.Reserve("a@x.com") {
		t.Fatal("held address refused after an unrelated Refund")
	}
}

func TestSignInThrottle_ResetUnknownIsHarmless(t *testing.T) {
	th, _ := newTestThrottle(SignInMaxKeys, SignInWindow)
	reserveN(t, th, "a@x.com", SignInMaxFailures)
	th.Reset("b@x.com")
	th.Reset(" B@X.COM ")
	if th.Reserve("a@x.com") {
		t.Fatal("Reset of another address cleared a@x.com")
	}
	if !th.Reserve("b@x.com") {
		t.Fatal("Reserve for the reset-but-unknown address = false, want true")
	}
}

func TestSignInThrottle_ResetNormalises(t *testing.T) {
	th, _ := newTestThrottle(SignInMaxKeys, SignInWindow)
	reserveN(t, th, "a@x.com", SignInMaxFailures)
	th.Reset("  A@X.COM\t")
	if !th.Reserve("a@x.com") {
		t.Fatal("Reset with different case/whitespace did not clear the address")
	}
}

func TestSignInThrottle_ResetFreesRoomOnFullMap(t *testing.T) {
	th, _ := newTestThrottle(2, SignInWindow)
	reserveN(t, th, "a@x.com", 1)
	reserveN(t, th, "b@x.com", 1)
	if th.Reserve("c@x.com") {
		t.Fatal("new address on a full map = true, want false")
	}
	th.Reset("a@x.com")
	if !th.Reserve("c@x.com") {
		t.Fatal("new address after Reset freed a slot = false, want true")
	}
}

func TestSignInThrottle_ConcurrentNewKeysBoundedByCap(t *testing.T) {
	const maxKeys = 10
	th, _ := newTestThrottle(maxKeys, SignInWindow)
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		ok    int
		start = make(chan struct{})
	)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if th.Reserve(fmt.Sprintf("u%d@x.com", i)) {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if ok != maxKeys {
		t.Fatalf("new addresses admitted = %d, want exactly %d", ok, maxKeys)
	}
}

func TestSignInThrottle_SweepCadenceIsOneMinute(t *testing.T) {
	th, clk := newTestThrottle(SignInMaxKeys, SignInWindow)
	th.Reserve("a@x.com")
	base := th.SweepsForTest()
	if base != 1 {
		t.Fatalf("sweeps after the first Reserve = %d, want 1", base)
	}
	clk.Advance(time.Minute - time.Nanosecond)
	th.Reserve("b@x.com")
	if n := th.SweepsForTest(); n != base {
		t.Fatalf("sweeps at +1m-1ns = %d, want %d", n, base)
	}
	clk.Advance(time.Nanosecond)
	th.Reserve("c@x.com")
	if n := th.SweepsForTest(); n != base+1 {
		t.Fatalf("sweeps at +1m = %d, want %d", n, base+1)
	}
}

func TestSignInThrottle_FullWarnsOncePerMinute(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	th, clk := newTestThrottle(1, SignInWindow)
	reserveN(t, th, "a@x.com", 1)
	for i := 0; i < 20; i++ {
		if th.Reserve(fmt.Sprintf("n%d@x.com", i)) {
			t.Fatal("new address on a full map = true, want false")
		}
	}
	if n := strings.Count(buf.String(), "level=WARN"); n != 1 {
		t.Fatalf("WARN lines within one minute = %d, want 1\n%s", n, buf.String())
	}
	if strings.Contains(buf.String(), "@x.com") {
		t.Fatalf("WARN logs an email address: %s", buf.String())
	}
	clk.Advance(time.Minute)
	th.Reserve("late@x.com")
	if n := strings.Count(buf.String(), "level=WARN"); n != 2 {
		t.Fatalf("WARN lines after one more minute = %d, want 2", n)
	}
}
