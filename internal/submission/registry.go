// registry.go: M5-02-04 (the adapter registry, the pure config-lookup Select and the
// fail-closed production allowlist) and M5-03-05 (NewDefaultRegistry registering the mock).
// See registry_test.go, internal/submission/mock_adapter_test.go and
// cmd/submission/main_test.go for the specs these bodies satisfy.
package submission

import (
	"errors"
	"fmt"
	"strings"
)

// Registry maps an adapter name to its adapter. Built once at boot.
type Registry map[string]Adapter

// NewRegistry keys each adapter by its own Name(), so a key can never disagree with the
// value it stamps into submission_jobs.adapter / app_exchange.adapter. Errors on an empty
// name or a duplicate name. Zero adapters is valid and returns an empty, non-nil Registry.
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
// It registers "mock" (M5-03); M6 registers the sandbox. Registering an adapter here does
// NOT make it usable in production -- see productionAdapters.
//
// A one-key map literal rather than NewRegistry(...): NewRegistry returns an error this
// signature cannot carry, and swallowing an impossible error with `_` or panicking is worse.
// The key is mockAdapterName, the SAME constant (*MockAdapter).Name() returns, so the key and
// the value can never disagree about what lands in submission_jobs.adapter. The literal does
// bypass NewRegistry's key-by-Name() invariant, which is why
// TestNewDefaultRegistry_RegistersExactlyMock asserts it by iteration.
//
// cfg MUST reach NewMockAdapter. A body that accepts the parameter and ignores it compiles
// fine, leaves APP_ADAPTER_MOCK_LATENCY inert and kills Core AC-5 while every other spec stays
// green; TestNewDefaultRegistry_PassesConfigToTheMock (mock_adapter_test.go) is the one spec
// that catches that, via a wall-clock oracle.
func NewDefaultRegistry(cfg MockConfig) Registry {
	return Registry{mockAdapterName: NewMockAdapter(cfg)}
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
// the production allowlist (only when environment normalizes to "production"), then
// registry lookup.
func Select(reg Registry, environment, name string) (Adapter, error) {
	if name == "" {
		return nil, ErrNoAdapterConfigured
	}
	if IsProduction(environment) {
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

// IsProduction normalizes environment (trim + lowercase) before comparing to
// "production". ENVIRONMENT is a free-form, unvalidated env var
// (internal/platform/config.go's envString does no normalization or validation), and
// Select's production check is this repo's only boot-refusal gate (Core AC-6: refuse to
// start rather than run an unauthorized adapter in production). A fail-closed gate that an
// exact string match defeats via casing or padding -- "Production", "PRODUCTION",
// " production" -- is not fail-closed at all, so the comparison here must not be
// case/whitespace sensitive. Exported so cmd/submission/main.go's own production check
// (whether to log.Fatalf when no adapter is selectable) uses this exact normalization
// instead of duplicating it with a second, unnormalized comparison.
//
// internal/gateway.MockIssuerEnabled gates the dev/CI mock issuer with the same
// `environment != "production"` exact match and is deliberately NOT changed here -- it
// guards two dev-only routes, not a boot, and is a different story's code. Reconciling the
// two notions of "production" belongs to M8-07, which MockIssuerEnabled's own comment
// already defers to.
func IsProduction(environment string) bool {
	return strings.ToLower(strings.TrimSpace(environment)) == "production"
}
