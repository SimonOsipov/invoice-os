package gateway

import (
	"fmt"
	"testing"
	"time"
)

// D19 "no sweep on every call": while the map stays full and no key can have
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
