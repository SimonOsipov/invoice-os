// registry.go: M5-02-04, the adapter registry, the pure config-lookup Select, and the
// fail-closed production allowlist. See registry_test.go and cmd/submission/main_test.go
// for the specs these bodies satisfy.
package submission

import (
	"errors"
	"fmt"
)

// Registry maps an adapter name to its adapter. Built once at boot.
type Registry map[string]Adapter

// NewRegistry keys each adapter by its own Name(), so a key can never disagree with the
// value it stamps into submission_jobs.adapter / app_exchange.adapter. Errors on an empty
// name or a duplicate name. Zero adapters is valid (e.g. the dev boot before M5-03) and
// returns an empty, non-nil Registry.
func NewRegistry(adapters ...Adapter) (Registry, error) {
	reg := make(Registry, len(adapters))
	for _, a := range adapters {
		name := a.Name()
		if name == "" {
			return nil, errors.New("submission: adapter has an empty Name()")
		}
		if _, exists := reg[name]; exists {
			return nil, fmt.Errorf("submission: duplicate adapter name %q", name)
		}
		reg[name] = a
	}
	return reg, nil
}

// NewDefaultRegistry is the single seam through which the binary obtains its adapters.
// EMPTY in M5-02; M5-03 registers "mock"; M6 registers the sandbox. Registering an
// adapter here does NOT make it usable in production -- see productionAdapters.
func NewDefaultRegistry() Registry {
	return Registry{}
}

// productionAdapters is the FAIL-CLOSED allowlist of names permitted when
// ENVIRONMENT=production. Empty through M5-02 and M5-03. Adding a name here is a
// deliberate, reviewable act performed by the story that owns that adapter (M6), never by
// the story that merely registers it (M5-03).
var productionAdapters = map[string]struct{}{}

var (
	ErrNoAdapterConfigured = errors.New("submission: APP_ADAPTER is not set")
	ErrUnknownAdapter      = errors.New("submission: unknown adapter")
	ErrAdapterNotInProd    = errors.New("submission: adapter is not permitted in production")
)

// Select resolves name against reg for environment. Pure and total -- it opens no
// connection and reads no environment variable itself, so it is exhaustively
// unit-testable. Precedence: empty name checked first (works in every environment), then
// the production allowlist (only when environment == "production"), then registry lookup.
func Select(reg Registry, environment, name string) (Adapter, error) {
	if name == "" {
		return nil, ErrNoAdapterConfigured
	}
	if environment == "production" {
		if _, allowed := productionAdapters[name]; !allowed {
			return nil, ErrAdapterNotInProd
		}
	}
	a, ok := reg[name]
	if !ok {
		return nil, ErrUnknownAdapter
	}
	return a, nil
}
