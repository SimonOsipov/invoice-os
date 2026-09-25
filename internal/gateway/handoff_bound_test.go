package gateway

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

// fillStore puts n live codes and fails the test on any refusal.
func fillStore(t *testing.T, s *HandoffStore, n int) []string {
	t.Helper()
	h := stateHash(randomState(t))
	codes := make([]string, 0, n)
	for i := 0; i < n; i++ {
		c, ok := s.Put(fmt.Sprintf("T%d", i), h)
		if !ok {
			t.Fatalf("Put #%d on a store under the cap = false, want true", i+1)
		}
		codes = append(codes, c)
	}
	return codes
}

// A Put sweeps only once the earliest live code can have expired.
func TestHandoffStore_PutWithNothingExpiredDoesNotSweep(t *testing.T) {
	clk := newTestClock()
	s := NewHandoffStore(HandoffTTL, clk.Now)
	fillStore(t, s, 2) // warm-up: arms the expiry gate
	base := s.SweepsForTest()

	fillStore(t, s, 500)
	for i := 0; i < 500; i++ {
		clk.Advance(time.Millisecond)
		fillStore(t, s, 1)
	}
	if n := s.SweepsForTest() - base; n != 0 {
		t.Fatalf("sweeps across 1000 Puts with nothing expired = %d, want 0", n)
	}
	if n := storeMapEntries(s); n != 1002 {
		t.Fatalf("store entries = %d, want 1002", n)
	}

	clk.Advance(HandoffTTL) // every code has expired
	fillStore(t, s, 1)
	if n := s.SweepsForTest() - base; n != 1 {
		t.Fatalf("sweeps once the first code expired = %d, want 1", n)
	}
	if n := storeMapEntries(s); n != 1 {
		t.Fatalf("store entries after the sweep = %d, want 1", n)
	}
}

func TestHandoffStore_CapRefusesBeyondMaxLive(t *testing.T) {
	clk := newTestClock()
	s := NewHandoffStore(HandoffTTL, clk.Now)
	st := randomState(t)
	codes := fillStore(t, s, HandoffMaxLive)

	c, ok := s.Put("over", stateHash(st))
	if ok || c != "" {
		t.Fatalf("Put #%d = (%q, %v), want (\"\", false)", HandoffMaxLive+1, c, ok)
	}
	if n := storeMapEntries(s); n != HandoffMaxLive {
		t.Fatalf("store entries after a refused Put = %d, want %d", n, HandoffMaxLive)
	}
	// A redeemed code frees its slot.
	if _, ok := s.Take(codes[0], "wrong"); ok {
		t.Fatal("Take with a wrong state = true, want false")
	}
	c, ok = s.Put("after-take", stateHash(st))
	if !ok || c == "" {
		t.Fatalf("Put after a Take freed a slot = (%q, %v), want (code, true)", c, ok)
	}
	if tok, ok := s.Take(c, st); !ok || tok != "after-take" {
		t.Fatalf("Take of the new code = (%q, %v), want (\"after-take\", true)", tok, ok)
	}
}

func TestHandoffStore_ExpiredEntriesFreeSpace(t *testing.T) {
	clk := newTestClock()
	s := NewHandoffStore(HandoffTTL, clk.Now)
	st := randomState(t)
	fillStore(t, s, HandoffMaxLive)

	clk.Advance(HandoffTTL - time.Nanosecond)
	if c, ok := s.Put("early", stateHash(st)); ok {
		t.Fatalf("Put 1ns before any code expired = (%q, true), want refused", c)
	}
	clk.Advance(time.Nanosecond)
	c, ok := s.Put("late", stateHash(st))
	if !ok || c == "" {
		t.Fatalf("Put once every code expired = (%q, %v), want (code, true)", c, ok)
	}
	if n := storeMapEntries(s); n != 1 {
		t.Fatalf("store entries after the sweep = %d, want 1", n)
	}
}

// A full store answers 503 with no code or token, and refunds the reservation.
func TestSignIn_FullStoreAnswers503(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtSession)
	clk := newTestClock()
	store := NewHandoffStore(HandoffTTL, clk.Now)
	th := NewSignInThrottle(1, SignInMaxKeys, SignInWindow, clk.Now)
	h := SignInHandler(fake.URL, testClient(), store, th, slog.New(slog.DiscardHandler))
	fillStore(t, store, HandoffMaxLive)

	for i := 1; i <= 2; i++ {
		rec := serve(h, http.MethodPost, "/auth/sign-in", signInBody(regEmail, regPassword, randomState(t)))
		requireRefusal(t, rec, http.StatusServiceUnavailable, msgUnavailable)
		for _, secret := range []string{sessionAT, sessionRT} {
			if bytes.Contains(rec.Body.Bytes(), []byte(secret)) {
				t.Fatalf("attempt %d: body carries %q: %s", i, secret, rec.Body.String())
			}
		}
	}
	// With max=1, the second attempt reaches GoTrue only if the first was refunded.
	if n := fake.Hits(); n != 2 {
		t.Fatalf("GoTrue hits = %d, want 2", n)
	}
	if n := storeMapEntries(store); n != HandoffMaxLive {
		t.Fatalf("store entries = %d, want %d", n, HandoffMaxLive)
	}
}

// Control for the 503 test: one free slot still signs in.
func TestSignIn_StoreOneUnderCapSignsIn(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtSession)
	rig := newSignInRig(t, fake.URL, nil)
	fillStore(t, rig.store, HandoffMaxLive-1)
	s := randomState(t)

	code := requireCode(t, rig.doSignIn(signInBody(regEmail, regPassword, s)))
	if tok, ok := rig.store.Take(code, s); !ok || tok != sessionAT {
		t.Fatalf("Take = (%q, %v), want (%q, true)", tok, ok, sessionAT)
	}
}

func TestSignIn_WhitespaceEmailIsRequired(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, gtSession)
	rig := newSignInRig(t, fake.URL, nil)

	for _, email := range []string{"   ", "\t", " \n "} {
		rec := rig.doSignIn(signInBody(email, "x", randomState(t)))
		requireRefusal(t, rec, http.StatusBadRequest, msgFieldsRequired)
	}
	if n := fake.Hits(); n != 0 {
		t.Fatalf("GoTrue hits = %d, want 0", n)
	}
	// Control: a padded real address still reaches GoTrue.
	requireCode(t, rig.doSignIn(signInBody(" "+regEmail+" ", regPassword, randomState(t))))
	if n := fake.Hits(); n != 1 {
		t.Fatalf("GoTrue hits after a padded address = %d, want 1", n)
	}
}
