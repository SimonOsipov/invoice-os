// registry_test.go: M5-02-04 RED specs (Mode A) for the adapter registry, the pure
// config-lookup Select, and the fail-closed production allowlist. Transcribed from the
// story's Test Specs table. package submission_test per this package's existing
// convention (exchange_test.go, canonical_test.go, failure_modes_test.go, ...) -- these
// tests can only reach productionAdapters' emptiness behaviourally, through Select, since
// they cannot see the unexported map.
//
// No t.Skip anywhere in this file: scripts/ci/rls-test-gate.sh runs ./internal/submission/...
// in the queue job and internal/tools/rlsgate/rlsgate.go fails the step on any
// test-level skip. These tests are pure (no DB, no network, no clock) and must never skip.
package submission_test

import (
	"context"
	"errors"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// fakeAdapter is a minimal submission.Adapter double for registry/Select tests. Transform,
// Submit and Poll are never exercised here -- registry.go/Select only ever touch Name().
type fakeAdapter struct {
	name    string
	version string
}

func (f fakeAdapter) Name() string    { return f.name }
func (f fakeAdapter) Version() string { return f.version }
func (f fakeAdapter) Transform(_ context.Context, _ submission.Canonical) (submission.Wire, error) {
	return nil, nil
}
func (f fakeAdapter) Submit(_ context.Context, _ submission.Wire, _ string) (submission.Result, submission.Evidence) {
	return nil, submission.Evidence{}
}
func (f fakeAdapter) Poll(_ context.Context, _ submission.Ref) (submission.Result, submission.Evidence) {
	return nil, submission.Evidence{}
}

// AC-1: NewRegistry keys each adapter by its own Name().
func TestNewRegistry_KeysByName(t *testing.T) {
	a := fakeAdapter{name: "a", version: "v1"}
	b := fakeAdapter{name: "b", version: "v1"}

	reg, err := submission.NewRegistry(a, b)
	if err != nil {
		t.Fatalf("NewRegistry(a, b) returned unexpected error: %v", err)
	}
	if len(reg) != 2 {
		t.Fatalf("NewRegistry(a, b) = %d entries, want 2 (registry: %+v)", len(reg), reg)
	}

	got, ok := reg["a"]
	if !ok {
		t.Fatal(`NewRegistry(a, b): registry has no key "a"`)
	}
	if got.Name() != "a" {
		t.Errorf(`registry["a"].Name() = %q, want "a"`, got.Name())
	}

	got, ok = reg["b"]
	if !ok {
		t.Fatal(`NewRegistry(a, b): registry has no key "b"`)
	}
	if got.Name() != "b" {
		t.Errorf(`registry["b"].Name() = %q, want "b"`, got.Name())
	}
}

// AC-1: NewRegistry rejects an empty adapter name and a duplicate adapter name, returning
// an error and a nil registry in both cases. Also proves the zero-adapter call succeeds
// (distinguishing "no adapters passed" from "an adapter with a bad name").
func TestNewRegistry_RejectsEmptyAndDuplicateNames(t *testing.T) {
	t.Run("empty name", func(t *testing.T) {
		reg, err := submission.NewRegistry(fakeAdapter{name: "", version: "v1"})
		if err == nil {
			t.Fatal("NewRegistry(adapter with empty name) returned nil error, want an error")
		}
		if reg != nil {
			t.Errorf("NewRegistry(adapter with empty name) returned non-nil registry %+v, want nil", reg)
		}
	})

	t.Run("duplicate name", func(t *testing.T) {
		a1 := fakeAdapter{name: "a", version: "v1"}
		a2 := fakeAdapter{name: "a", version: "v2"}
		reg, err := submission.NewRegistry(a1, a2)
		if err == nil {
			t.Fatal("NewRegistry(two adapters both named \"a\") returned nil error, want an error")
		}
		if reg != nil {
			t.Errorf("NewRegistry(duplicate name) returned non-nil registry %+v, want nil", reg)
		}
	})

	t.Run("zero adapters is valid, not an error", func(t *testing.T) {
		reg, err := submission.NewRegistry()
		if err != nil {
			t.Fatalf("NewRegistry() with zero adapters returned unexpected error: %v", err)
		}
		if len(reg) != 0 {
			t.Errorf("NewRegistry() with zero adapters = %d entries, want 0", len(reg))
		}
	})
}

// AC-2: NewDefaultRegistry() is EMPTY in M5-02. This test is M5-03's tripwire: it must be
// updated deliberately when M5-03 registers "mock".
func TestNewDefaultRegistry_IsEmptyInM502(t *testing.T) {
	reg := submission.NewDefaultRegistry()
	if len(reg) != 0 {
		t.Errorf("NewDefaultRegistry() = %d entries, want 0 (M5-02 registers no adapter; M5-03 owns the first entry)", len(reg))
	}
}

// AC-3 / Core AC-6: Select refuses a name in production even when that name IS registered
// in the Registry passed in, because productionAdapters is empty in this subtask.
func TestSelect_ProductionRefusesNonAllowlistedAdapter(t *testing.T) {
	reg, err := submission.NewRegistry(fakeAdapter{name: "mock", version: "v1"})
	if err != nil {
		t.Fatalf("NewRegistry setup failed: %v", err)
	}

	adapter, err := submission.Select(reg, "production", "mock")
	if !errors.Is(err, submission.ErrAdapterNotInProd) {
		t.Fatalf(`Select(reg, "production", "mock") error = %v, want errors.Is(err, ErrAdapterNotInProd)`, err)
	}
	if adapter != nil {
		t.Errorf(`Select(reg, "production", "mock") adapter = %+v, want nil`, adapter)
	}
}

// AC-3: the same registered adapter IS selectable outside production.
func TestSelect_NonProductionAllowsRegisteredAdapter(t *testing.T) {
	want := fakeAdapter{name: "mock", version: "v1"}
	reg, err := submission.NewRegistry(want)
	if err != nil {
		t.Fatalf("NewRegistry setup failed: %v", err)
	}

	adapter, err := submission.Select(reg, "development", "mock")
	if err != nil {
		t.Fatalf(`Select(reg, "development", "mock") returned unexpected error: %v`, err)
	}
	if adapter == nil {
		t.Fatal(`Select(reg, "development", "mock") adapter = nil, want the registered adapter`)
	}
	if adapter.Name() != "mock" {
		t.Errorf(`Select(reg, "development", "mock") adapter.Name() = %q, want "mock"`, adapter.Name())
	}
}

// AC-4: an empty name argument always yields ErrNoAdapterConfigured, in every environment
// -- including production, where it must NOT be shadowed by ErrAdapterNotInProd. This
// pins the precedence: the empty-name check runs before the production-allowlist check.
func TestSelect_EmptyNameIsNoAdapterConfigured(t *testing.T) {
	reg, err := submission.NewRegistry(fakeAdapter{name: "mock", version: "v1"})
	if err != nil {
		t.Fatalf("NewRegistry setup failed: %v", err)
	}

	for _, environment := range []string{"development", "production", "staging", ""} {
		t.Run("environment="+environment, func(t *testing.T) {
			adapter, err := submission.Select(reg, environment, "")
			if !errors.Is(err, submission.ErrNoAdapterConfigured) {
				t.Fatalf("Select(reg, %q, \"\") error = %v, want errors.Is(err, ErrNoAdapterConfigured)", environment, err)
			}
			if err != nil && errors.Is(err, submission.ErrAdapterNotInProd) {
				t.Errorf("Select(reg, %q, \"\") error also satisfies errors.Is(_, ErrAdapterNotInProd) -- empty-name check must be evaluated before the production-allowlist check", environment)
			}
			if adapter != nil {
				t.Errorf("Select(reg, %q, \"\") adapter = %+v, want nil", environment, adapter)
			}
		})
	}
}

// AC-4: a non-empty name that is not a key in the registry yields ErrUnknownAdapter.
func TestSelect_UnknownNameIsUnknownAdapter(t *testing.T) {
	reg, err := submission.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry setup failed: %v", err)
	}

	adapter, err := submission.Select(reg, "development", "sandbox")
	if !errors.Is(err, submission.ErrUnknownAdapter) {
		t.Fatalf(`Select(empty reg, "development", "sandbox") error = %v, want errors.Is(err, ErrUnknownAdapter)`, err)
	}
	if adapter != nil {
		t.Errorf(`Select(empty reg, "development", "sandbox") adapter = %+v, want nil`, adapter)
	}
}

// AC-5: Select is total. Across the full cross-product of {empty, unknown, registered} x
// {development, production, staging, ""}, every result is exactly one of
// (non-nil Adapter, nil error) or (nil Adapter, non-nil error) -- never nil/nil, never
// non-nil/non-nil. The Select stub deliberately returns nil, nil, so this test is the one
// that must fail loudly against the stub.
func TestSelect_NeverReturnsNilAdapterWithNilError(t *testing.T) {
	reg, err := submission.NewRegistry(fakeAdapter{name: "registered", version: "v1"})
	if err != nil {
		t.Fatalf("NewRegistry setup failed: %v", err)
	}

	names := []string{"", "unknown-name", "registered"}
	environments := []string{"development", "production", "staging", ""}

	for _, name := range names {
		for _, environment := range environments {
			t.Run("name="+name+"/environment="+environment, func(t *testing.T) {
				adapter, err := submission.Select(reg, environment, name)
				switch {
				case adapter == nil && err == nil:
					t.Fatalf("Select(reg, %q, %q) = (nil, nil) -- Select must never return nil adapter with nil error", environment, name)
				case adapter != nil && err != nil:
					t.Fatalf("Select(reg, %q, %q) = (%+v, %v) -- Select must never return a non-nil adapter alongside a non-nil error", environment, name, adapter, err)
				}
			})
		}
	}
}

// AC-3: with the package tests living in submission_test (external), the unexported
// productionAdapters map can't be read directly -- so this proves its emptiness
// behaviourally: several distinct registered names all still refuse in production. This
// is the documented cost of [mock-refuses-production].
func TestSelect_ProductionRefusesEveryNameToday(t *testing.T) {
	names := []string{"mock", "sandbox", "partner-a", "partner-b"}
	var adapters []submission.Adapter
	for _, n := range names {
		adapters = append(adapters, fakeAdapter{name: n, version: "v1"})
	}
	reg, err := submission.NewRegistry(adapters...)
	if err != nil {
		t.Fatalf("NewRegistry setup failed: %v", err)
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			adapter, err := submission.Select(reg, "production", name)
			if !errors.Is(err, submission.ErrAdapterNotInProd) {
				t.Errorf(`Select(reg, "production", %q) error = %v, want errors.Is(err, ErrAdapterNotInProd) -- productionAdapters must be empty in M5-02`, name, err)
			}
			if adapter != nil {
				t.Errorf(`Select(reg, "production", %q) adapter = %+v, want nil`, name, adapter)
			}
		})
	}
}
