// TestReset* names keep these reachable from ci.yml's -run alternation
// (TestCIRunFiltersReachEveryTestInThePackage).
package db_test

import (
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// TestResetWillRun pins the predicate both Provision and the gateway branch on.
// The two rows that matter most are the two real deployments: a PR fork, where
// ENVIRONMENT is the literal "development" it inherited and only
// DEPLOY_ENV distinguishes the fork, and the persistent
// environment, where the same ENVIRONMENT value must NOT produce a reset.
func TestResetWillRun(t *testing.T) {
	cases := []struct {
		name        string
		environment string // ENVIRONMENT — forked verbatim, hence "development" in both live rows
		bootstrap   string // GATEWAY_DB_BOOTSTRAP
		railwayName string // DEPLOY_ENV — the only input that differs live
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
				Environment:   c.environment,
				BootstrapFlag: c.bootstrap,
				DeployEnv:     c.railwayName,
				ResetFlag:     c.reset,
			}
			if got := cfg.ResetWillRun(); got != c.want {
				t.Errorf("ResetWillRun() = %v, want %v (ENVIRONMENT=%q GATEWAY_DB_BOOTSTRAP=%q DEPLOY_ENV=%q GATEWAY_DB_RESET=%q)",
					got, c.want, c.environment, c.bootstrap, c.railwayName, c.reset)
			}
		})
	}
}
