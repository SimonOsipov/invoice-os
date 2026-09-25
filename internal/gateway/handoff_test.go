package gateway

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"reflect"
	"regexp"
	"sync"
	"testing"
	"time"
)

// testClock is a goroutine-safe fake clock for the injected now.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func randomState(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func stateHash(s string) [32]byte { return sha256.Sum256([]byte(s)) }

var codeShape = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// storeMapEntries counts entries across the store's top-level map fields,
// so the tests do not pin a field name.
func storeMapEntries(s *HandoffStore) int {
	v := reflect.ValueOf(s).Elem()
	n := 0
	for i := 0; i < v.NumField(); i++ {
		if f := v.Field(i); f.Kind() == reflect.Map {
			n += f.Len()
		}
	}
	return n
}

// holdsBytes walks v and reports whether any string, byte array or byte slice
// equals one of needles; it also counts map entries seen.
func holdsBytes(v reflect.Value, needles [][]byte, seen map[uintptr]bool, entries *int) bool {
	switch v.Kind() {
	case reflect.String:
		for _, n := range needles {
			if v.String() == string(n) {
				return true
			}
		}
	case reflect.Array, reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			b := make([]byte, v.Len())
			for i := range b {
				b[i] = byte(v.Index(i).Uint())
			}
			for _, n := range needles {
				if bytes.Equal(b, n) {
					return true
				}
			}
			return false
		}
		for i := 0; i < v.Len(); i++ {
			if holdsBytes(v.Index(i), needles, seen, entries) {
				return true
			}
		}
	case reflect.Map:
		*entries += v.Len()
		it := v.MapRange()
		for it.Next() {
			if holdsBytes(it.Key(), needles, seen, entries) || holdsBytes(it.Value(), needles, seen, entries) {
				return true
			}
		}
	case reflect.Pointer:
		if v.IsNil() || seen[v.Pointer()] {
			return false
		}
		seen[v.Pointer()] = true
		return holdsBytes(v.Elem(), needles, seen, entries)
	case reflect.Interface:
		if !v.IsNil() {
			return holdsBytes(v.Elem(), needles, seen, entries)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if holdsBytes(v.Field(i), needles, seen, entries) {
				return true
			}
		}
	}
	return false
}

func TestHandoffStore_CodeShape(t *testing.T) {
	s := NewHandoffStore(HandoffTTL, time.Now)
	h := stateHash(randomState(t))
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		c := s.Put("tok", h)
		if !codeShape.MatchString(c) {
			t.Fatalf("Put #%d = %q, want 43 base64url characters", i, c)
		}
		if seen[c] {
			t.Fatalf("Put #%d returned a repeated code %q", i, c)
		}
		seen[c] = true
	}
	if len(seen) != 1000 {
		t.Fatalf("distinct codes = %d, want 1000", len(seen))
	}
}

func TestHandoffStore_TakeIsSingleUse(t *testing.T) {
	s := NewHandoffStore(HandoffTTL, time.Now)
	st := randomState(t)
	c := s.Put("T", stateHash(st))
	if tok, ok := s.Take(c, st); !ok || tok != "T" {
		t.Fatalf("first Take = (%q, %v), want (\"T\", true)", tok, ok)
	}
	if tok, ok := s.Take(c, st); ok || tok != "" {
		t.Fatalf("second Take = (%q, %v), want (\"\", false)", tok, ok)
	}
}

func TestHandoffStore_ExpiresAtTTL(t *testing.T) {
	clk := newTestClock()
	s := NewHandoffStore(HandoffTTL, clk.Now)
	st := randomState(t)

	c := s.Put("T", stateHash(st))
	clk.Advance(HandoffTTL - time.Nanosecond)
	if tok, ok := s.Take(c, st); !ok || tok != "T" {
		t.Fatalf("Take at ttl-1ns = (%q, %v), want (\"T\", true)", tok, ok)
	}

	c2 := s.Put("T2", stateHash(st))
	clk.Advance(HandoffTTL)
	if tok, ok := s.Take(c2, st); ok || tok != "" {
		t.Fatalf("Take at ttl = (%q, %v), want (\"\", false)", tok, ok)
	}
}

func TestHandoffStore_ExpiredTakeStillRemoves(t *testing.T) {
	clk := newTestClock()
	s := NewHandoffStore(HandoffTTL, clk.Now)
	st := randomState(t)

	c := s.Put("T", stateHash(st))
	control := s.Put("C", stateHash(st))
	clk.Advance(HandoffTTL)
	if _, ok := s.Take(c, st); ok {
		t.Fatal("Take of an expired code = true, want false")
	}
	clk.Advance(-HandoffTTL)

	// The untouched control proves the rewind makes a stored code live again.
	if tok, ok := s.Take(control, st); !ok || tok != "C" {
		t.Fatalf("control Take after rewind = (%q, %v), want (\"C\", true)", tok, ok)
	}
	if tok, ok := s.Take(c, st); ok || tok != "" {
		t.Fatalf("Take after rewind = (%q, %v), want (\"\", false): the expired Take did not remove the code", tok, ok)
	}
}

func TestHandoffStore_ConcurrentTakeOneWinner(t *testing.T) {
	s := NewHandoffStore(HandoffTTL, time.Now)
	st := randomState(t)
	c := s.Put("T", stateHash(st))

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		wins  int
		start = make(chan struct{})
	)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if tok, ok := s.Take(c, st); ok && tok == "T" {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins != 1 {
		t.Fatalf("winners = %d, want exactly 1", wins)
	}
}

func TestHandoffStore_KeysAreHashed(t *testing.T) {
	s := NewHandoffStore(HandoffTTL, time.Now)
	c := s.Put("T", stateHash(randomState(t)))
	raw, _ := base64.RawURLEncoding.DecodeString(c)
	needles := [][]byte{[]byte(c)}
	if len(raw) > 0 {
		needles = append(needles, raw)
	}

	entries := 0
	found := holdsBytes(reflect.ValueOf(s), needles, map[uintptr]bool{}, &entries)
	if entries == 0 {
		t.Fatal("store holds no map entries after Put; nothing to inspect")
	}
	if found {
		t.Fatalf("store holds the plaintext code %q", c)
	}
}

func TestHandoffStore_WrongStateRefusedAndSpends(t *testing.T) {
	s := NewHandoffStore(HandoffTTL, time.Now)
	s1, s2 := randomState(t), randomState(t)
	c := s.Put("T", stateHash(s1))
	control := s.Put("C", stateHash(s1))

	if _, ok := s.Take(c, s2); ok {
		t.Fatal("Take with the wrong state = true, want false")
	}
	if tok, ok := s.Take(control, s1); !ok || tok != "C" {
		t.Fatalf("control Take with the right state = (%q, %v), want (\"C\", true)", tok, ok)
	}
	if tok, ok := s.Take(c, s1); ok || tok != "" {
		t.Fatalf("Take with the right state after a wrong one = (%q, %v), want (\"\", false)", tok, ok)
	}
}

func TestHandoffStore_RightStateRedeems(t *testing.T) {
	s := NewHandoffStore(HandoffTTL, time.Now)
	s1 := randomState(t)
	c := s.Put("T", stateHash(s1))
	if tok, ok := s.Take(c, s1); !ok || tok != "T" {
		t.Fatalf("Take = (%q, %v), want (\"T\", true)", tok, ok)
	}
}

func TestHandoffStore_EmptyStateRefused(t *testing.T) {
	s := NewHandoffStore(HandoffTTL, time.Now)
	s1 := randomState(t)
	c := s.Put("T", stateHash(s1))
	control := s.Put("C", stateHash(s1))

	if tok, ok := s.Take(c, ""); ok || tok != "" {
		t.Fatalf("Take with empty state = (%q, %v), want (\"\", false)", tok, ok)
	}
	if tok, ok := s.Take(control, s1); !ok || tok != "C" {
		t.Fatalf("control Take with the right state = (%q, %v), want (\"C\", true)", tok, ok)
	}
}

func TestHandoffStore_UnknownAndEmptyCode(t *testing.T) {
	s := NewHandoffStore(HandoffTTL, time.Now)
	st := randomState(t)
	if tok, ok := s.Take("", st); ok || tok != "" {
		t.Fatalf("Take(\"\") = (%q, %v), want (\"\", false)", tok, ok)
	}
	if tok, ok := s.Take(randomState(t), st); ok || tok != "" {
		t.Fatalf("Take(random) = (%q, %v), want (\"\", false)", tok, ok)
	}

	c := s.Put("T", stateHash(st))
	if _, ok := s.Take(randomState(t), st); ok {
		t.Fatal("Take(random) on a non-empty store = true, want false")
	}
	if tok, ok := s.Take(c, st); !ok || tok != "T" {
		t.Fatalf("Take of the stored code = (%q, %v), want (\"T\", true)", tok, ok)
	}
}

func TestHandoffStore_PutSweepsExpired(t *testing.T) {
	clk := newTestClock()
	s := NewHandoffStore(HandoffTTL, clk.Now)
	h := stateHash(randomState(t))

	s.Put("A", h)
	clk.Advance(HandoffTTL)
	s.Put("B", h)
	if n := storeMapEntries(s); n != 1 {
		t.Fatalf("store entries after the sweeping Put = %d, want 1", n)
	}
}
