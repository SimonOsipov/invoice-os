package gateway

import (
	"fmt"
	"testing"
	"time"
)

// No sweep on every call: while the map stays full and no key can have
// expired, refused new addresses must not each pay an O(maxKeys) sweep.
func TestSignInThrottle_FullMapDoesNotSweepPerCall(t *testing.T) {
	const maxKeys = 100
	th, clk := newTestThrottle(maxKeys, SignInWindow)
	for i := 0; i < maxKeys; i++ {
		reserveN(t, th, fmt.Sprintf("held%d@x.com", i), 1)
	}
	base := th.SweepsForTest()

	refused := 0
	for i := 0; i < 1000; i++ {
		if !th.Reserve(fmt.Sprintf("new%d@x.com", i)) {
			refused++
		}
		clk.Advance(59 * time.Millisecond) // 59 s in total: no cadence sweep is due
	}
	if refused != 1000 {
		t.Fatalf("refused new addresses on a full map = %d, want 1000", refused)
	}
	if !th.Reserve("held0@x.com") {
		t.Fatal("held address refused on a full map, want true")
	}
	if n := th.SweepsForTest() - base; n > 1 {
		t.Fatalf("sweeps across 1000 refused new addresses = %d, want <= 1", n)
	}

	clk.Advance(SignInWindow)
	if !th.Reserve("after@x.com") {
		t.Fatal("new address once held keys expired = false, want true")
	}
}

// The full-map gate opens at the earliest held expiry, not a later one.
// All steps stay inside one minute, so the cadence sweep never runs.
func TestSignInThrottle_FullMapFreesEarliestExpiryOnTime(t *testing.T) {
	const window = 30 * time.Second
	th, clk := newTestThrottle(3, window)
	reserveN(t, th, "a@x.com", 1) // t0
	clk.Advance(10 * time.Second)
	reserveN(t, th, "b@x.com", 1) // t0+10s
	clk.Advance(10 * time.Second)
	reserveN(t, th, "c@x.com", 1) // t0+20s
	if th.Reserve("d@x.com") {
		t.Fatal("d at t0+20s on a full map = true, want false")
	}
	clk.Advance(10*time.Second - time.Nanosecond)
	if th.Reserve("d@x.com") {
		t.Fatal("d at t0+30s-1ns = true, want false")
	}
	clk.Advance(time.Nanosecond)
	if !th.Reserve("d@x.com") {
		t.Fatal("d at t0+30s, when a expired = false, want true")
	}
	if th.Reserve("e@x.com") {
		t.Fatal("e at t0+30s on a refilled map = true, want false")
	}
	// The gate re-arms at b's expiry; refusals before it do not sweep.
	base := th.SweepsForTest()
	for i := 0; i < 9; i++ {
		clk.Advance(time.Second)
		if th.Reserve(fmt.Sprintf("x%d@x.com", i)) {
			t.Fatalf("x%d before b expired = true, want false", i)
		}
	}
	if n := th.SweepsForTest() - base; n != 0 {
		t.Fatalf("sweeps before the re-armed gate = %d, want 0", n)
	}
	clk.Advance(time.Second)
	if !th.Reserve("e@x.com") {
		t.Fatal("e at t0+40s, when b expired = false, want true")
	}
}
