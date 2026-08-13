// store_option_adversarial_test.go: APPR-08-02 Mode B. Pool-free, for the same
// reason store_option_test.go is — see that file's header.
package invoice

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestStoreOptions_ManyOptionsLastWins: 64 alternating options over one store. A
// loop that stops early, dedupes, or applies only opts[0] passes
// TestStoreOptions_ApplyInOrderLastWins's two-option case but dies here.
func TestStoreOptions_ManyOptionsLastWins(t *testing.T) {
	const n = 64
	for _, lastWants := range []bool{true, false} {
		var applied int
		opts := make([]StoreOption, 0, n)
		for i := range n {
			v := i%2 == 0
			if !lastWants {
				v = !v
			}
			opts = append(opts, func(s *Store) {
				applied++
				s.approvalsEnforced = v
			})
		}
		// n is even, so opts[n-1] carries !lastWants; the real last-wins value is
		// asserted through a trailing option instead.
		opts = append(opts, WithApprovalsEnforced(lastWants))

		s := NewStore(nil, opts...)
		if applied != n {
			t.Errorf("NewStore ran %d of %d options, want all of them", applied, n)
		}
		if s.approvalsEnforced != lastWants {
			t.Errorf("after %d options ending in WithApprovalsEnforced(%v), approvalsEnforced = %v", n+1, lastWants, s.approvalsEnforced)
		}
	}
}

// TestWithApprovalsEnforced_RepeatedSameValueIsStable: a double-apply of the same
// value must not toggle. Kills a body that flips the field instead of setting it.
func TestWithApprovalsEnforced_RepeatedSameValueIsStable(t *testing.T) {
	for _, v := range []bool{true, false} {
		one := NewStore(nil, WithApprovalsEnforced(v)).approvalsEnforced
		two := NewStore(nil, WithApprovalsEnforced(v), WithApprovalsEnforced(v)).approvalsEnforced
		three := NewStore(nil, WithApprovalsEnforced(v), WithApprovalsEnforced(v), WithApprovalsEnforced(v)).approvalsEnforced
		if one != v || two != v || three != v {
			t.Errorf("WithApprovalsEnforced(%v) applied 1/2/3 times gives %v/%v/%v, want %v each — the option sets, it does not toggle", v, one, two, three, v)
		}
	}
}

// TestWithApprovalsEnforced_OptionIsReusableAcrossStores: one StoreOption value
// applied to two stores. Kills a constructor that captures per-store state, and
// pins that options carry no cross-store coupling before 03/04/05 add readers.
func TestWithApprovalsEnforced_OptionIsReusableAcrossStores(t *testing.T) {
	on := WithApprovalsEnforced(true)
	a, b := NewStore(nil, on), NewStore(nil, on)
	if !a.approvalsEnforced || !b.approvalsEnforced {
		t.Errorf("one option value applied to two stores gives %v/%v, want true/true", a.approvalsEnforced, b.approvalsEnforced)
	}
	if c := NewStore(nil); c.approvalsEnforced {
		t.Error("a later optionless NewStore is enforced; the option leaked into package state")
	}
}

// TestNewStore_EmptyOptionFormsAllAgree: the three spellings of no options.
func TestNewStore_EmptyOptionFormsAllAgree(t *testing.T) {
	var nilSlice []StoreOption
	empty := []StoreOption{}
	for name, s := range map[string]*Store{
		"NewStore(nil)":              NewStore(nil),
		"NewStore(nil, nilSlice...)": NewStore(nil, nilSlice...),
		"NewStore(nil, empty...)":    NewStore(nil, empty...),
	} {
		if s == nil {
			t.Fatalf("%s returned nil", name)
		}
		if s.approvalsEnforced {
			t.Errorf("%s: approvalsEnforced = true, want false", name)
		}
	}
}

// TestNewStore_KeepsThePoolThroughTheOptionLoop: the option loop must not clobber
// the pool. Every other test in both option files passes nil, so a constructor
// that rebuilt the Store after applying options would drop the pool unnoticed.
// The sentinel is never dialled — NewStore only stores the pointer.
func TestNewStore_KeepsThePoolThroughTheOptionLoop(t *testing.T) {
	pool := &pgxpool.Pool{}
	s := NewStore(pool, WithApprovalsEnforced(true))
	if s.pool != pool {
		t.Errorf("NewStore(pool, WithApprovalsEnforced(true)).pool = %p, want %p", s.pool, pool)
	}
	if !s.approvalsEnforced {
		t.Error("approvalsEnforced = false on the pooled store")
	}
	// The option sees the same store the caller gets back, not a copy.
	var seen *pgxpool.Pool
	NewStore(pool, func(s *Store) { seen = s.pool })
	if seen != pool {
		t.Errorf("the option observed pool %p, want the caller's %p", seen, pool)
	}
}
