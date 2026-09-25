package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func requireStore(t *testing.T, s *HandoffStore, step string, entries, sweeps int) {
	t.Helper()
	if n := storeMapEntries(s); n != entries {
		t.Fatalf("%s: entries = %d, want %d", step, n, entries)
	}
	if n := s.SweepsForTest(); n != sweeps {
		t.Fatalf("%s: sweeps = %d, want %d", step, n, sweeps)
	}
}

func TestHandoffMaxLive_Pinned(t *testing.T) {
	if HandoffMaxLive != 10_000 {
		t.Fatalf("HandoffMaxLive = %d, want 10000", HandoffMaxLive)
	}
}

// After a sweep that leaves survivors, the gate re-arms at the earliest survivor.
func TestHandoffStore_SweepRearmsAtEarliestSurvivor(t *testing.T) {
	clk := newTestClock()
	s := NewHandoffStore(HandoffTTL, clk.Now)
	h := stateHash(randomState(t))
	put := func() {
		t.Helper()
		if _, ok := s.Put("T", h); !ok {
			t.Fatal("Put refused under the cap")
		}
	}

	put() // t0: first Put sweeps the empty store
	requireStore(t, s, "t0", 1, 1)
	clk.Advance(20 * time.Second)
	put() // t20
	requireStore(t, s, "t20", 2, 1)
	clk.Advance(20 * time.Second)
	put() // t40
	requireStore(t, s, "t40", 3, 1)
	clk.Advance(20 * time.Second)
	put() // t60: the t0 code expired
	requireStore(t, s, "t60", 3, 2)
	clk.Advance(time.Millisecond)
	put() // nothing expired since the sweep
	requireStore(t, s, "t60+1ms", 4, 2)
	clk.Advance(20*time.Second - time.Millisecond)
	put() // t80: the t20 code expired
	requireStore(t, s, "t80", 4, 3)
}

// A Take never delays the sweep of the codes that remain.
func TestHandoffStore_TakeOfEarliestThenPutStillSweeps(t *testing.T) {
	clk := newTestClock()
	s := NewHandoffStore(HandoffTTL, clk.Now)
	st := randomState(t)
	a, _ := s.Put("A", stateHash(st))
	clk.Advance(30 * time.Second)
	if _, ok := s.Put("B", stateHash(st)); !ok {
		t.Fatal("Put B refused")
	}
	if tok, ok := s.Take(a, st); !ok || tok != "A" {
		t.Fatalf("Take(a) = (%q, %v), want (\"A\", true)", tok, ok)
	}
	clk.Advance(HandoffTTL) // t90: B expired at t90
	if _, ok := s.Put("C", stateHash(st)); !ok {
		t.Fatal("Put C refused")
	}
	if n := storeMapEntries(s); n != 1 {
		t.Fatalf("entries = %d, want 1 (B swept)", n)
	}
}

// A clock step back gives a Put an expiry earlier than every stored one; the gate follows it.
func TestHandoffStore_ClockStepBackLowersTheGate(t *testing.T) {
	clk := newTestClock()
	s := NewHandoffStore(HandoffTTL, clk.Now)
	h := stateHash(randomState(t))
	s.Put("A", h) // t0, expires t60
	clk.Advance(-30 * time.Second)
	s.Put("B", h) // t-30, expires t30
	clk.Advance(60 * time.Second)
	s.Put("C", h) // t30: B expired
	if n := storeMapEntries(s); n != 2 {
		t.Fatalf("entries = %d, want 2 (B swept)", n)
	}
}

// GoTrue receives the trimmed address.
func TestSignIn_PaddedEmailReachesGoTrueTrimmed(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtSession)
	rig := newSignInRig(t, fake.URL, nil)
	requireCode(t, rig.doSignIn(signInBody(" \t"+regEmail+" \n", regPassword, randomState(t))))
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("GoTrue calls = %d, want 1", len(calls))
	}
	var body map[string]string
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("GoTrue body %q: %v", calls[0].Body, err)
	}
	if body["email"] != regEmail {
		t.Fatalf("GoTrue email = %q, want %q", body["email"], regEmail)
	}
}

// The full-store WARN names the condition and carries no address, secret, code or state.
func TestSignIn_FullStoreWarnCarriesNoSecrets(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtSession)
	log, buf := captureLog()
	rig := newSignInRig(t, fake.URL, log)
	fillStore(t, rig.store, HandoffMaxLive)
	st := randomState(t)

	requireRefusal(t, rig.doSignIn(signInBody(regEmail, regPassword, st)), http.StatusServiceUnavailable, msgUnavailable)
	out := buf.String()
	if !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, "hand-off store full") {
		t.Fatalf("log lacks the full-store WARN: %s", out)
	}
	local := regEmail[:strings.IndexByte(regEmail, '@')]
	for _, secret := range []string{regEmail, local, regPassword, sessionAT, sessionRT, st} {
		if strings.Contains(out, secret) {
			t.Fatalf("log carries %q: %s", secret, out)
		}
	}
}
