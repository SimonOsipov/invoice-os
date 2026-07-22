// registry_adversarial_test.go: QA Mode B adversarial coverage for M5-02-04, added after
// implementation. package submission_test per this package's existing convention -- these
// tests can only reach productionAdapters' emptiness behaviourally, through Select, since
// they cannot see the unexported map (see registry_test.go's header for the same note).
//
// No t.Skip anywhere in this file: scripts/ci/rls-test-gate.sh runs ./internal/submission/...
// in the queue job and internal/tools/rlsgate/rlsgate.go fails the step on any
// test-level skip. These tests are pure (no DB, no network, no clock) and must never skip.
package submission_test

import (
	"errors"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// TestSelect_ProductionCheckIsCaseAndWhitespaceInsensitive pins the fix for a deployment
// footgun QA found: Select's production gate used to be an exact
// `environment == "production"` string comparison, which a capitalized or
// whitespace-padded ENVIRONMENT value (e.g. "Production", "PRODUCTION", " production",
// "production ") would silently bypass entirely -- Select would treat it as
// non-production and return the adapter with no error, exactly as if ENVIRONMENT were
// "development". ENVIRONMENT is a free-form, unvalidated env var
// (internal/platform/config.go's envString does not normalize it), and this is the repo's
// only boot-refusal gate (Core AC-6), so it must not be defeatable by casing or padding.
// Select now normalizes (trim + lowercase) before comparing, so every variant below must
// be refused with ErrAdapterNotInProd, exactly like the canonical "production" case.
func TestSelect_ProductionCheckIsCaseAndWhitespaceInsensitive(t *testing.T) {
	reg, err := submission.NewRegistry(fakeAdapter{name: "mock", version: "v1"})
	if err != nil {
		t.Fatalf("NewRegistry setup failed: %v", err)
	}

	variants := []string{"Production", "PRODUCTION", " production", "production "}
	for _, environment := range variants {
		t.Run(environment, func(t *testing.T) {
			adapter, err := submission.Select(reg, environment, "mock")
			if !errors.Is(err, submission.ErrAdapterNotInProd) {
				t.Fatalf(`Select(reg, %q, "mock") error = %v, want errors.Is(err, ErrAdapterNotInProd)`, environment, err)
			}
			if adapter != nil {
				t.Errorf(`Select(reg, %q, "mock") adapter = %+v, want nil`, environment, adapter)
			}
		})
	}
}

// TestNewRegistry_WhitespaceOnlyNameIsAccepted documents observed behavior for an adapter
// whose Name() is whitespace-only: NewRegistry's emptiness check is `name == ""`, which
// " " does not satisfy, so it is accepted as a valid (if unusual) registry key. This
// matches the DB CHECK constraint shape on submission_jobs.adapter / app_exchange.adapter
// (CHECK char_length > 0), which " " also satisfies -- so this is plausibly intentional
// rather than an oversight. Pinned here as observed behavior, not asserted as correct or
// incorrect.
func TestNewRegistry_WhitespaceOnlyNameIsAccepted(t *testing.T) {
	reg, err := submission.NewRegistry(fakeAdapter{name: " ", version: "v1"})
	if err != nil {
		t.Fatalf(`OBSERVED: NewRegistry(adapter with Name()==" ") returned error %v -- if whitespace-only names should be rejected, NewRegistry's check needs strings.TrimSpace(name) == "", not name == ""`, err)
	}
	if len(reg) != 1 {
		t.Fatalf(`OBSERVED: NewRegistry(adapter with Name()==" ") = %d entries, want 1`, len(reg))
	}
	got, ok := reg[" "]
	if !ok || got.Name() != " " {
		t.Fatalf(`OBSERVED: NewRegistry(adapter with Name()==" ") registry = %+v, want key " " present`, reg)
	}
}

// TestSelect_NilRegistryReturnsUnknownAdapterNotPanic: Select must be total and safe even
// against a nil Registry (distinct from an empty, non-nil one returned by NewRegistry()).
// Reading a nil map in Go returns the zero value with ok==false -- it does not panic -- but
// this pins that guarantee explicitly against Select's actual signature rather than trusting
// Go map semantics implicitly, since a future refactor (e.g. to a struct-backed Registry)
// could silently drop it.
func TestSelect_NilRegistryReturnsUnknownAdapterNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Select(nil, \"development\", \"anything\") panicked: %v -- Select must be total and safe against a nil Registry", r)
		}
	}()

	var nilReg submission.Registry
	adapter, err := submission.Select(nilReg, "development", "anything")
	if !errors.Is(err, submission.ErrUnknownAdapter) {
		t.Fatalf(`Select(nil, "development", "anything") error = %v, want errors.Is(err, ErrUnknownAdapter)`, err)
	}
	if adapter != nil {
		t.Errorf(`Select(nil, "development", "anything") adapter = %+v, want nil`, adapter)
	}
}

// TestSelect_NilRegistryInProductionStillRefusesFirst: the production-allowlist check runs
// before the registry lookup, so a nil Registry in production still yields
// ErrAdapterNotInProd (not ErrUnknownAdapter) for any non-empty name -- the fail-closed gate
// does not depend on the registry being non-nil.
func TestSelect_NilRegistryInProductionStillRefusesFirst(t *testing.T) {
	var nilReg submission.Registry
	adapter, err := submission.Select(nilReg, "production", "anything")
	if !errors.Is(err, submission.ErrAdapterNotInProd) {
		t.Fatalf(`Select(nil, "production", "anything") error = %v, want errors.Is(err, ErrAdapterNotInProd)`, err)
	}
	if adapter != nil {
		t.Errorf(`Select(nil, "production", "anything") adapter = %+v, want nil`, adapter)
	}
}

// NOTE on two adversarial scenarios from the QA brief NOT covered by a test here:
//
//  1. An adapter whose Name() is non-deterministic (returns a different string on each
//     call), and whether NewRegistry's map key (captured once, at construction) could
//     disagree with a later Name() call. This is covered at the contract level by law L02
//     in M5-02-06 -- not duplicated here.
//
//  2. "A registered adapter whose name is in productionAdapters" (boot-matrix row 6:
//     production + allowlisted + registered -> select it). productionAdapters is an
//     unexported package-level var and ships EMPTY in this subtask
//     ([production-allowlist-is-empty]); package submission_test (external, per this
//     package's established convention) has no way to add an entry to it, and building a
//     Registry directly does not help since the allowlist itself -- not the registry -- is
//     what would need extending. This row is therefore genuinely untestable from outside
//     the package today. It becomes testable only once M6 adds a real entry to
//     productionAdapters, at which point that story's own tests (in-package, or an
//     exported test seam) must cover it. Stated plainly rather than faked.
