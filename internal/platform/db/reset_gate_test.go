// reset_gate_test.go — the chain that keeps the PR-environment reset from
// stopping SILENTLY.
//
// Reset (reset.go) is what makes a PR fork start from the curated demo state
// instead of from every row the persistent environment has accumulated. Both of
// its inputs are hand-set Railway variables, and both fail CLOSED: lose
// GATEWAY_DB_RESET when a service is recreated and Provision simply does not
// call Reset. Nothing errors, /healthz greens, the fleet gate passes, and the
// E2E suites go back to running against inherited residue — the pre-2026-07-28
// world, arrived at with no failure anywhere to mark the transition.
//
// So the fact is published (ProvisionConfig.ResetWillRun -> platform.DBReset ->
// /healthz `db_reset`) and asserted by dev-env.yml's health-gate. That is three
// files with no compiler between them, which is exactly the seam the tests below
// pin. None of them touches a database.
//
// Every test here is named TestReset* deliberately. This package's own
// TestCIRunFiltersReachEveryTestInThePackage requires each test to be reachable
// from a ci.yml `-run` filter, and `TestReset` is already in ci.yml:262's
// alternation — so the names are what make these run in CI at all. Renaming one
// off that prefix orphans it, loudly, in that test.
package db_test

import (
	"os"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// TestResetWillRun pins the predicate both Provision and the gateway branch on.
// The two rows that matter most are the two real deployments: a PR fork, where
// ENVIRONMENT is the literal "development" it inherited and only
// RAILWAY_ENVIRONMENT_NAME distinguishes the fork, and the persistent
// environment, where the same ENVIRONMENT value must NOT produce a reset.
func TestResetWillRun(t *testing.T) {
	cases := []struct {
		name        string
		environment string // ENVIRONMENT — forked verbatim, hence "development" in both live rows
		bootstrap   string // GATEWAY_DB_BOOTSTRAP
		railwayName string // RAILWAY_ENVIRONMENT_NAME — the only input that differs live
		reset       string // GATEWAY_DB_RESET
		want        bool
	}{
		{"a real PR fork", "development", "true", "pr-155", "true", true},
		{"a real PR fork, repo-prefixed env name", "development", "true", "invoice-os-pr-155", "true", true},
		{"the persistent environment", "development", "true", "production", "true", false},
		{"the persistent environment, pre-rename name", "development", "true", "development", "true", false},
		{"GATEWAY_DB_RESET lost when the service was recreated", "development", "true", "pr-155", "", false},
		{"GATEWAY_DB_RESET set to something other than true", "development", "true", "pr-155", "1", false},
		{"bootstrap off, so there would be no seed to converge to", "development", "", "pr-155", "true", false},
		{"ENVIRONMENT unset, the fail-open shape the allowlist exists to close", "", "true", "pr-155", "true", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := db.ProvisionConfig{
				Environment:            c.environment,
				BootstrapFlag:          c.bootstrap,
				RailwayEnvironmentName: c.railwayName,
				ResetFlag:              c.reset,
			}
			if got := cfg.ResetWillRun(); got != c.want {
				t.Errorf("ResetWillRun() = %v, want %v (ENVIRONMENT=%q GATEWAY_DB_BOOTSTRAP=%q RAILWAY_ENVIRONMENT_NAME=%q GATEWAY_DB_RESET=%q)",
					got, c.want, c.environment, c.bootstrap, c.railwayName, c.reset)
			}
		})
	}
}

// TestResetDecisionIsPublishedByTheGateway: main() cannot be called from a
// test (it opens a listener), so the publish step is pinned the same way
// TestGatewayMainPassesRawEnvironmentToProvisioningGuard pins the guard read —
// against main.go's own source.
//
// Asserting on ResetWillRun specifically is the point. Recomputing the two
// guards inline at the call site would look identical and read identically, and
// would be a second copy of a condition that has to stay in step with
// Provision's nesting forever.
func TestResetDecisionIsPublishedByTheGateway(t *testing.T) {
	b, err := os.ReadFile("../../../cmd/gateway/main.go")
	if err != nil {
		t.Fatalf("read cmd/gateway/main.go: %v", err)
	}
	src := string(b)

	if !strings.Contains(src, "platform.DBReset =") {
		t.Error("cmd/gateway/main.go never assigns platform.DBReset, so /healthz carries no db_reset field and dev-env.yml's health-gate has nothing to assert — the PR-environment reset can stop happening with nothing red anywhere")
	}
	if !strings.Contains(src, "ResetWillRun()") {
		t.Error("cmd/gateway/main.go does not publish db.ProvisionConfig.ResetWillRun() — a value derived any other way is a second copy of Provision's own branch condition and will drift from it")
	}
}

// devEnvExecutable returns dev-env.yml with its comment lines removed.
//
// Every string this test looks for is also spelled out in the prose next to the
// check, at length. Scanning the raw file would therefore pass on the comments
// alone with the check itself deleted — an instrument that reports all-clear
// while examining nothing but its own explanation.
func devEnvExecutable(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../../.github/workflows/dev-env.yml")
	if err != nil {
		t.Fatalf("read .github/workflows/dev-env.yml: %v", err)
	}
	var kept []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// TestResetIsAssertedByTheDevEnvGate pins the far end of the chain: publishing
// the field buys nothing unless a gate reads it.
func TestResetIsAssertedByTheDevEnvGate(t *testing.T) {
	yaml := devEnvExecutable(t)

	// Control needle: a step this workflow has always had. Without it, a
	// stripping bug or a moved file leaves every check below passing on an empty
	// string — the failure mode this whole file exists to make impossible.
	if !strings.Contains(yaml, "healthz") {
		t.Fatalf("the comment-stripped dev-env.yml contains no reference to healthz at all, so it is not the workflow (or devEnvExecutable stripped everything) and the checks below would pass having examined nothing")
	}

	for _, want := range []struct{ fragment, why string }{
		{`.db_reset // empty`, "the health-gate never reads db_reset out of the /healthz body"},
		{`[ "$reset" != "true" ]`, "nothing fails a PULL REQUEST run whose fork was not reset, so E2E would verify against inherited residue"},
		{`elif [ "$reset" = "true" ]`, "nothing fails a PUSH/DISPATCH run that reports a reset against the PERSISTENT environment, whose data has no fork underneath it"},
	} {
		if !strings.Contains(yaml, want.fragment) {
			t.Errorf("dev-env.yml no longer contains %q: %s", want.fragment, want.why)
		}
	}
}
