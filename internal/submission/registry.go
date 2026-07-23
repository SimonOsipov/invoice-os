// registry.go: M5-02-04, the adapter registry, the pure config-lookup Select, and the
// fail-closed production allowlist. See registry_test.go and cmd/submission/main_test.go
// for the specs these bodies satisfy.
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
//
// TODO(M5-03-05): implemented by the executor. The signature already takes cfg (so the RED
// specs compile) but the body still returns an EMPTY registry and DROPS cfg on the floor.
// That is deliberately the exact vacuity hazard task-228's plan names: a body that keeps the
// parameter and ignores it compiles fine, leaves APP_ADAPTER_MOCK_LATENCY inert and kills Core
// AC-5 while every other spec stays green. TestNewDefaultRegistry_PassesConfigToTheMock
// (mock_adapter_test.go) is the one spec that catches it, via a wall-clock oracle.
func NewDefaultRegistry(cfg MockConfig) Registry {
	_ = cfg
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
