// The chain that keeps the demo purge from failing SILENTLY, mirroring
// reset_gate_test.go's.
//
// The purge is the one non-fatal step in Provision: it fails, Provision logs and
// carries on, the gateway boots, /healthz returns 200 and every fleet gate goes
// green. So a green deploy is not evidence a purge ran. db.DemoPurgeOutcome ->
// platform.DemoPurge -> /healthz `demo_purge` -> dev-env.yml's health-gate is
// what turns that into a red run, and it is four files with no compiler between
// them. None of these tests touches a database.
//
// Names are TestPurge* deliberately — ci.yml's -run alternation is what makes
// them run in CI at all (TestCIRunFiltersReachEveryTestInThePackage).
package db_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// missingFragments returns the wanted fragments src does not carry, in order.
func missingFragments(src string, want []string) []string {
	var out []string
	for _, w := range want {
		if !strings.Contains(src, w) {
			out = append(out, w)
		}
	}
	return out
}

// TestPurgeDecisionIsPublishedByTheGateway pins the publish step against
// main.go's own source: main() opens a listener, so it cannot be called here.
// Asserting on db.DemoPurgeOutcome specifically is the point — a value derived
// any other way is a second copy of what Provision already decided.
func TestPurgeDecisionIsPublishedByTheGateway(t *testing.T) {
	want := []string{"platform.DemoPurge =", "db.DemoPurgeOutcome"}

	b, err := os.ReadFile("../../../cmd/gateway/main.go")
	if err != nil {
		t.Fatalf("read cmd/gateway/main.go: %v", err)
	}
	if missing := missingFragments(string(b), want); len(missing) != 0 {
		t.Errorf("cmd/gateway/main.go carries none of %v — /healthz would carry no demo_purge field, so a swallowed purge failure stays invisible to every gate", missing)
	}

	t.Run("control needle", func(t *testing.T) {
		const publishes = "\tplatform.DemoPurge = string(db.DemoPurgeOutcome)\n"
		if got := missingFragments(publishes, want); len(got) != 0 {
			t.Fatalf("the scanner calls %v missing from a fixture that carries both — it cannot see what it looks for", got)
		}
		const publishesNothing = "\tplatform.DBReset = strconv.FormatBool(provisionCfg.ResetWillRun())\n"
		if got := missingFragments(publishesNothing, want); len(got) != len(want) {
			t.Fatalf("the scanner found only %d of %d fragment(s) missing from a fixture that carries neither — a clean report from it would prove nothing", len(got), len(want))
		}
	})
}

// TestPurgeFieldIsPublishedByNoOtherService: the other eight services never
// provision, so their /healthz bodies must stay byte-identical to before the
// field existed.
func TestPurgeFieldIsPublishedByNoOtherService(t *testing.T) {
	mains, err := filepath.Glob("../../../cmd/*/main.go")
	if err != nil {
		t.Fatalf("glob cmd/*/main.go: %v", err)
	}
	if len(mains) < 2 {
		t.Fatalf("found %d cmd/*/main.go file(s); the fleet has nine, so the scan below would be vacuous", len(mains))
	}

	var assigning []string
	for _, path := range mains {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(b), "platform.DemoPurge =") {
			assigning = append(assigning, filepath.Base(filepath.Dir(path)))
		}
	}

	if len(assigning) != 1 {
		t.Fatalf("%d of %d cmd/*/main.go files assign platform.DemoPurge (%v), want exactly 1", len(assigning), len(mains), assigning)
	}
	if assigning[0] != "gateway" {
		t.Errorf("platform.DemoPurge is assigned by cmd/%s, want cmd/gateway — the gateway is the only binary that provisions", assigning[0])
	}
}

// gateFragments are what the health-gate must carry to read and assert the
// field. Spelled once, reused by the control needle below.
var gateFragments = []string{`.demo_purge // empty`, `[ "$purge" != "true" ]`}

// TestPurgeIsAssertedByTheDevEnvGate pins the far end of the chain: publishing
// the field buys nothing unless a gate reads it.
func TestPurgeIsAssertedByTheDevEnvGate(t *testing.T) {
	yaml := devEnvExecutable(t)
	if !strings.Contains(yaml, "healthz") {
		t.Fatalf("the comment-stripped dev-env.yml mentions healthz nowhere, so it is not the workflow (or devEnvExecutable stripped everything) and the checks below would pass having examined nothing")
	}

	if missing := missingFragments(yaml, gateFragments); len(missing) != 0 {
		t.Errorf("dev-env.yml's health-gate carries none of %v — nothing fails a deploy whose purge errored, which is the exact hole the non-fatal purge leaves open", missing)
	}

	t.Run("control needle", func(t *testing.T) {
		const asserts = `
            purge=$(printf '%s' "$body" | jq -r '.demo_purge // empty' 2>/dev/null || echo '')
          if [ "$purge" != "true" ]; then
            echo "::error::no purge"
            exit 1
          fi
`
		if got := missingFragments(asserts, gateFragments); len(got) != 0 {
			t.Fatalf("the scanner calls %v missing from a fixture that carries both — it cannot see what it looks for", got)
		}
		if got := missingFragments(strings.ReplaceAll(asserts, "purge", "reset"), gateFragments); len(got) != len(gateFragments) {
			t.Fatalf("the scanner found only %d of %d fragment(s) missing from a fixture with the purge assertion renamed away — a clean report from it would prove nothing", len(got), len(gateFragments))
		}
	})
}

// gateSite is a fragment's 0-based line index and leading-space count in a
// workflow; line is -1 when the fragment is absent.
type gateSite struct{ line, indent int }

func findGateSite(yaml, fragment string) gateSite {
	for i, line := range strings.Split(yaml, "\n") {
		if strings.Contains(line, fragment) {
			return gateSite{line: i, indent: len(line) - len(strings.TrimLeft(line, " "))}
		}
	}
	return gateSite{line: -1}
}

// gateLayoutFaults returns every reason the purge assertion in yaml is not
// sitting outside the IS_PR branch. Empty means the layout is right.
func gateLayoutFaults(yaml string) []string {
	purge := findGateSite(yaml, `[ "$purge" != "true" ]`)
	isPR := findGateSite(yaml, `if [ "$IS_PR" = "true" ]`)
	elseReset := findGateSite(yaml, `elif [ "$reset" = "true" ]`)

	var faults []string
	if purge.line < 0 {
		return append(faults, `the workflow carries no [ "$purge" != "true" ] assertion at all`)
	}
	if isPR.line < 0 || elseReset.line < 0 {
		return append(faults, `the workflow carries no if/elif reset branch to place the purge assertion relative to`)
	}
	if purge.indent != isPR.indent {
		faults = append(faults, fmt.Sprintf("the purge assertion is indented %d spaces against the IS_PR branch's %d, so it is nested inside a branch instead of running on every trigger", purge.indent, isPR.indent))
	}
	if purge.line < elseReset.line {
		faults = append(faults, "the purge assertion sits before the elif, so it is inside the pull_request half and never runs on push or workflow_dispatch")
	}
	return faults
}

// TestPurgeGateAssertionCoversBothPaths: the purge is armed by BootstrapEnabled
// alone, which is true on the persistent environment too, so the assertion is
// NOT directional the way db_reset's is. Nesting it inside the IS_PR branch
// would disarm the production half and look identical in review.
func TestPurgeGateAssertionCoversBothPaths(t *testing.T) {
	yaml := devEnvExecutable(t)
	if !strings.Contains(yaml, "healthz") {
		t.Fatalf("the comment-stripped dev-env.yml mentions healthz nowhere; the checks below would pass having examined nothing")
	}

	for _, fault := range gateLayoutFaults(yaml) {
		t.Errorf("dev-env.yml's health-gate: %s", fault)
	}

	t.Run("control needle", func(t *testing.T) {
		const outside = `
          if [ "$IS_PR" = "true" ]; then
            if [ "$reset" != "true" ]; then
              exit 1
            fi
          elif [ "$reset" = "true" ]; then
            exit 1
          fi

          if [ "$purge" != "true" ]; then
            exit 1
          fi
`
		if faults := gateLayoutFaults(outside); len(faults) != 0 {
			t.Fatalf("the scanner reports %v against a fixture whose assertion IS outside the branch — it cannot recognise the shape it demands", faults)
		}

		nested := strings.Replace(outside, `
          if [ "$purge" != "true" ]; then
            exit 1
          fi
`, "", 1)
		nested = strings.Replace(nested, `
            if [ "$reset" != "true" ]; then
              exit 1
            fi`, `
            if [ "$reset" != "true" ]; then
              exit 1
            fi
            if [ "$purge" != "true" ]; then
              exit 1
            fi`, 1)
		if faults := gateLayoutFaults(nested); len(faults) == 0 {
			t.Fatal("the scanner reports the nested fixture clean — it cannot find a planted violation, so a clean report against the real workflow means nothing")
		}
	})
}

// purgeAssertionBlock returns dev-env.yml's `if [ "$purge" != "true" ]` block as
// runnable shell, dedented. Empty when the workflow carries none.
func purgeAssertionBlock(yaml string) string {
	lines := strings.Split(yaml, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), `if [ "$purge" != "true" ]`) {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	var block []string
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)
		block = append(block, trimmed)
		if trimmed == "fi" {
			return strings.Join(block, "\n")
		}
	}
	return ""
}

// runGateBlock runs block with $purge set, and returns its exit status.
func runGateBlock(t *testing.T, block, purge string) int {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sh", "-c", block)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "purge=" + purge, "EXPECTED_BUILD=deadbeef"}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("run gate block with purge=%q: %v (output %q)", purge, err, out)
	}
	return exitErr.ExitCode()
}

// gateOutcomes are the four values /healthz can hand the gate. Only "true" may
// pass: "error" is the swallowed purge failure this whole chain exists for,
// "false" is a lost GATEWAY_DB_BOOTSTRAP, and "" is a gateway too old to carry
// the field or a deleted assignment in main.go.
var gateOutcomes = []struct {
	purge    string
	wantExit int
	cause    string
}{
	{"true", 0, "a purge that ran"},
	{"error", 1, "the purge failed and Provision swallowed it to keep the fleet up"},
	{"false", 1, "GATEWAY_DB_BOOTSTRAP is not true, or ENVIRONMENT is off the allowlist"},
	{"", 1, "the gateway predates the field, or main.go's assignment was deleted"},
}

// TestPurgeGateFailsOnEveryOutcomeThatIsNotTrue executes the committed
// assertion. A gate that cannot go red is not a gate, and reading the YAML back
// cannot tell the two apart.
func TestPurgeGateFailsOnEveryOutcomeThatIsNotTrue(t *testing.T) {
	// First, so the runner is proved able to observe both outcomes even on the
	// runs where the real block is still missing.
	t.Run("control needle", func(t *testing.T) {
		const equivalent = "if [ \"$purge\" != \"true\" ]; then\necho fail\nexit 1\nfi"
		for _, c := range gateOutcomes {
			if got := runGateBlock(t, equivalent, c.purge); got != c.wantExit {
				t.Fatalf("the runner reports exit %d for purge=%q against a hand-written equivalent block, want %d — it cannot observe the outcome it asserts", got, c.purge, c.wantExit)
			}
		}
		const inverted = "if [ \"$purge\" = \"true\" ]; then\nexit 1\nfi"
		for _, c := range gateOutcomes {
			if got := runGateBlock(t, inverted, c.purge); got == c.wantExit {
				t.Fatalf("the runner reports exit %d for purge=%q against an INVERTED block too — it is not reading the block it was given", got, c.purge)
			}
		}
	})

	block := purgeAssertionBlock(devEnvExecutable(t))
	if block == "" {
		t.Fatalf(`dev-env.yml's health-gate carries no complete "if [ \"$purge\" != \"true\" ] ... fi" block, so no outcome can fail a run`)
	}

	for _, c := range gateOutcomes {
		if got := runGateBlock(t, block, c.purge); got != c.wantExit {
			t.Errorf("demo_purge=%q exits %d, want %d — %s", c.purge, got, c.wantExit, c.cause)
		}
	}
}
