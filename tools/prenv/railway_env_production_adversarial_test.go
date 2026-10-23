// railway_env_production_adversarial_test.go drives set-production-environment past the
// shapes its AC tests leave open, and checks that nothing CI executes can reach it.
package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestSetProductionEnvironmentRefusesANearMissID(t *testing.T) {
	t.Parallel()
	nearMisses := map[string]string{
		"a prefix":            persistentEnvironmentID[:8],
		"upper case":          strings.ToUpper(persistentEnvironmentID),
		"a trailing space":    persistentEnvironmentID + " ",
		"a leading space":     " " + persistentEnvironmentID,
		"a trailing newline":  persistentEnvironmentID + "\n",
		"one character more":  persistentEnvironmentID + "0",
		"one character short": persistentEnvironmentID[:len(persistentEnvironmentID)-1],
	}
	if len(nearMisses) == 0 {
		t.Fatal("no near-miss ids to try")
	}

	// Control: the exact id, with the same exports, passes the refusal and reaches Railway.
	control := newRailwayShim(t, productionRailway(productionVariables(`,"ENVIRONMENT":"production"`)))
	if _, _, code := runSetProductionEnvironment(t, control.prelude, forkExports(true, true, true), persistentEnvironmentID); code != 0 || len(control.calls(t)) == 0 {
		t.Fatalf("control: the exact persistent id exits %d after %d Railway call(s), want exit 0 after at least one", code, len(control.calls(t)))
	}

	for name, id := range nearMisses {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			shim := newRailwayShim(t, productionRailway(productionVariables(`,"ENVIRONMENT":"production"`)))
			stdout, stderr, code := runSetProductionEnvironment(t, shim.prelude, forkExports(true, true, true), id)
			out := stdout + stderr
			if code != 1 {
				t.Errorf("exit code = %d, want 1; output = %q", code, out)
			}
			if !onlyThePersistentEnvironment.MatchString(out) {
				t.Errorf("output does not say it writes only the persistent environment; output = %q", out)
			}
			if calls := shim.calls(t); len(calls) != 0 {
				t.Errorf("a token was set and the refusal still called Railway %v", operations(calls))
			}
			if strings.Contains(out, forkToken) || strings.Contains(out, "confirmed") {
				t.Errorf("output leaks the token or confirms a write; output = %q", out)
			}
			shim.requireLogs(t)
		})
	}
}

func TestSetProductionEnvironmentRefusesTwoGateways(t *testing.T) {
	t.Parallel()
	responses := productionRailway(productionVariables(`,"ENVIRONMENT":"production"`))
	responses["settle"] = forkSettle(
		`{"node":{"serviceId":"svc-gw-a","serviceName":"gateway"}}`,
		`{"node":{"serviceId":"svc-gw-b","serviceName":"gateway"}}`,
	)
	shim := newRailwayShim(t, responses)
	stdout, stderr, code := runSetProductionEnvironment(t, shim.prelude, forkExports(true, true, true), persistentEnvironmentID)
	out := stdout + stderr

	if code != 1 {
		t.Errorf("exit code = %d, want 1 with two services named gateway; output = %q", code, out)
	}
	if !strings.Contains(out, "2 service instances") || !strings.Contains(out, "named 'gateway'") {
		t.Errorf("output does not carry service_id_by_name's duplicate refusal; output = %q", out)
	}
	if ops := operations(shim.calls(t)); !slices.Equal(ops, []string{"settle"}) {
		t.Errorf("Railway calls = %v, want [settle]: nothing may be written against a guessed gateway", ops)
	}
	for _, id := range []string{"svc-gw-a", "svc-gw-b"} {
		if strings.Contains(stdout, id) {
			t.Errorf("stdout carries the service id %s; stdout = %q", id, stdout)
		}
	}
	if strings.Contains(out, "confirmed") {
		t.Errorf("a failed run printed the confirmation line; output = %q", out)
	}
	shim.requireLogs(t)
}

func TestSetProductionEnvironmentStopsOnARailwayError(t *testing.T) {
	t.Parallel()
	const apiError = `{"errors":[{"message":"Not Authorized"}],"data":{"variables":{` + forkEnvSecretSibling + `}}}`
	cases := []struct {
		name     string
		op, body string
		wantOps  []string
		says     string
	}{
		{"the re-read answers a GraphQL error", "svcVars", apiError, []string{"settle", "varUpsert", "svcVars"}, "re-reading gateway variables"},
		{"the re-read is unreadable", "svcVars", `not json ` + forkEnvSecretSibling, []string{"settle", "varUpsert", "svcVars"}, "unreadable"},
		{"the re-read has no variables map", "svcVars", `{"data":{"variables":null}}`, []string{"settle", "varUpsert", "svcVars"}, "unreadable"},
		{"the upsert answers a GraphQL error", "varUpsert", `{"errors":[{"message":"Not Authorized"}]}`, []string{"settle", "varUpsert"}, "setting gateway.ENVIRONMENT"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			responses := productionRailway(productionVariables(`,"ENVIRONMENT":"production"`))
			responses[c.op] = c.body
			shim := newRailwayShim(t, responses)
			stdout, stderr, code := runSetProductionEnvironment(t, shim.prelude, forkExports(true, true, true), persistentEnvironmentID)
			out := stdout + stderr

			if code != 1 {
				t.Errorf("exit code = %d, want 1; output = %q", code, out)
			}
			if !strings.Contains(out, c.says) {
				t.Errorf("output does not carry %q; output = %q", c.says, out)
			}
			if ops := operations(shim.calls(t)); !slices.Equal(ops, c.wantOps) {
				t.Errorf("Railway calls = %v, want %v", ops, c.wantOps)
			}
			if strings.Contains(out, "confirmed") {
				t.Errorf("a failed run printed the confirmation line; output = %q", out)
			}
			for _, leak := range []string{forkEnvSecret, forkToken} {
				if strings.Contains(out, leak) {
					t.Errorf("the output carries %q; output = %q", leak, out)
				}
			}
			shim.requireLogs(t)
		})
	}
}

// productionUsageNote is what both usage lists say about when the subcommand runs.
const productionUsageNote = "by hand, once, never from a workflow"

func TestRailwayEnvUsageListsSayProductionIsWrittenByHand(t *testing.T) {
	t.Parallel()
	stdout, stderr, code := runBashScript(t, "bash '"+railwayEnvScript(t)+"' no-such-subcommand\n")
	generic := stdout + stderr
	if code != 2 || !strings.Contains(generic, "set-fork-environment <environment-id|--self-test>") {
		t.Fatalf("control: the generic usage did not print (exit %d); output = %q", code, generic)
	}
	if want := "set-production-environment <environment-id> (" + productionUsageNote + ")"; !strings.Contains(generic, want) {
		t.Errorf("the dispatcher's usage does not carry %q; output = %q", want, generic)
	}

	raw, err := os.ReadFile(railwayEnvScript(t))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	end := slices.Index(lines, "set -euo pipefail")
	if end < 3 {
		t.Fatalf("railway-env.sh has no `set -euo pipefail` after its header (index %d); the header scan would read nothing", end)
	}
	header := lines[:end]
	listEnd := slices.IndexFunc(header, func(l string) bool { return strings.HasSuffix(l, ">") })
	if listEnd < 1 || !strings.HasPrefix(header[1], "# scripts/ci/railway-env.sh <") {
		t.Fatalf("the header's usage list is not at the top of railway-env.sh (ends at %d)", listEnd)
	}
	list := strings.Join(header[1:listEnd+1], "\n")
	if !strings.Contains(list, "set-fork-environment <environment-id|--self-test>|") {
		t.Fatalf("control: the header usage list does not name set-fork-environment:\n%s", list)
	}
	if !strings.Contains(list, "set-production-environment <environment-id>|") {
		t.Errorf("the header usage list does not name set-production-environment <environment-id>:\n%s", list)
	}
	flow := strings.Join(strings.Fields(strings.ReplaceAll(strings.Join(header, "\n"), "#", " ")), " ")
	if !regexp.MustCompile("`?set-production-environment`? is run " + regexp.QuoteMeta(productionUsageNote)).MatchString(flow) {
		t.Errorf("the header never says set-production-environment is run %s", productionUsageNote)
	}

	// Both gateway-token commands are named at all three usage sites.
	if want := productionGatewayTokenNeedle + " <environment-id> (" + productionUsageNote + ")"; !strings.Contains(generic, want) {
		t.Errorf("the dispatcher's usage does not carry %q; output = %q", want, generic)
	}
	if !strings.Contains(generic, "set-fork-gateway-token <environment-id>") {
		t.Errorf("the dispatcher's usage does not name set-fork-gateway-token <environment-id>; output = %q", generic)
	}
	for _, sub := range []string{"set-fork-gateway-token", productionGatewayTokenNeedle} {
		if !regexp.MustCompile(regexp.QuoteMeta(sub) + ` <environment-id>[|>]`).MatchString(list) {
			t.Errorf("the header usage list does not name %s <environment-id>:\n%s", sub, list)
		}
	}
	if !regexp.MustCompile("`?" + productionGatewayTokenNeedle + "`? is run " + regexp.QuoteMeta(productionUsageNote)).MatchString(flow) {
		t.Errorf("the header never says %s is run %s", productionGatewayTokenNeedle, productionUsageNote)
	}

	if !regexp.MustCompile(`[<|]discover-urls <environment-id>[|>]`).MatchString(generic) {
		t.Errorf("the dispatcher's usage does not name discover-urls <environment-id>; output = %q", generic)
	}
	if !regexp.MustCompile(`discover-urls <environment-id>[|>]`).MatchString(list) {
		t.Errorf("the header usage list does not name discover-urls <environment-id>:\n%s", list)
	}

	if !regexp.MustCompile(`[<|]fork-vars-before-urls <environment-id>[|>]`).MatchString(generic) {
		t.Errorf("the dispatcher's usage does not name fork-vars-before-urls <environment-id>; output = %q", generic)
	}
	if !regexp.MustCompile(`fork-vars-before-urls <environment-id>[|>]`).MatchString(list) {
		t.Errorf("the header usage list does not name fork-vars-before-urls <environment-id>:\n%s", list)
	}

	const afterUsage = `fork-vars-after-urls <environment-id> <gateway-url> <app-url> <landing-url> <ops-console-url> <support-console-url> <library-url> <blog-url>`
	if !regexp.MustCompile(`[<|]` + afterUsage + `[|>]`).MatchString(generic) {
		t.Errorf("the dispatcher's usage does not name %s; output = %q", afterUsage, generic)
	}
	if !strings.Contains(strings.Join(strings.Fields(strings.ReplaceAll(list, "#", " ")), " "), afterUsage+"|") {
		t.Errorf("the header usage list does not name %s:\n%s", afterUsage, list)
	}
}

var (
	// railwayEnvCall is `railway-env.sh` and the word after it.
	railwayEnvCall = regexp.MustCompile(`railway-env\.sh(?:\s+(\S+))?`)
	runKey         = regexp.MustCompile(`(?m)^(\s*-?\s*)run:\s*`)
)

// railwayEnvSubcommandFaults reports each railway-env.sh call in comment-stripped
// workflow text whose subcommand is not a literal, or is set-production-environment or set-production-gateway-token.
func railwayEnvSubcommandFaults(workflow string) (calls int, faults []string) {
	code := runKey.ReplaceAllString(shellCode(strings.Split(workflow, "\n")), "$1")
	for _, inv := range invocations(code, "railway-env.sh") {
		for _, m := range railwayEnvCall.FindAllStringSubmatch(inv, -1) {
			calls++
			sub := strings.TrimRight(m[1], ");")
			switch {
			case !regexp.MustCompile(`^[a-z][a-z-]*$`).MatchString(sub):
				faults = append(faults, "a subcommand that is not a literal: "+inv)
			case sub == productionNeedle:
				faults = append(faults, "runs "+productionNeedle+": "+inv)
			case sub == productionGatewayTokenNeedle:
				faults = append(faults, "runs "+productionGatewayTokenNeedle+": "+inv)
			}
		}
	}
	return calls, faults
}

// workflowEnvCall is one literal railway-env.sh call in a workflow run step.
type workflowEnvCall struct {
	sub  string
	args []string
}

// railwayEnvCalls returns each railway-env.sh call in the run steps of a workflow; an echo is not a call.
func railwayEnvCalls(workflow string) []workflowEnvCall {
	var out []workflowEnvCall
	for _, job := range workflowJobsOf(workflow) {
		for _, s := range job.steps() {
			for _, inv := range invocations(s.keys["run"], "railway-env.sh") {
				f := strings.Fields(inv)
				for i, w := range f {
					if !strings.HasSuffix(w, "railway-env.sh") || i+1 >= len(f) {
						continue
					}
					c := workflowEnvCall{sub: strings.TrimRight(f[i+1], ");")}
					for _, a := range f[i+2:] {
						c.args = append(c.args, strings.TrimRight(a, ");"))
					}
					out = append(out, c)
				}
			}
		}
	}
	return out
}

var singleForkVariableCommands = []string{
	"set-fork-auth", "set-fork-gateway-token", "set-fork-auth-site", "reconcile-urls",
	"set-ai-fake", "set-sentry-off", "set-fork-reconciliation-url", "set-fork-environment",
}

func TestNoWorkflowRunsASingleForkVariableCommandOnAnEnvironment(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(repoRoot(t), ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	read := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		read++
		for _, c := range railwayEnvCalls(string(raw)) {
			if !slices.Contains(singleForkVariableCommands, c.sub) {
				continue
			}
			if slices.Equal(c.args, []string{"--self-test"}) {
				continue
			}
			t.Errorf("%s runs %s %v; only --self-test may run, the fork passes write the fork's variables", e.Name(), c.sub, c.args)
		}
	}
	if read < 3 {
		t.Fatalf("control: read %d workflow file(s), want at least 3; the scan is broken", read)
	}
}

// executableNaming returns each file under root's scripts/ and .github/ trees, and its
// Makefile, whose comment-stripped text names needle. Go files are skipped.
func executableNaming(t *testing.T, root, needle string) (hits []string, read int) {
	t.Helper()
	visit := func(path string) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		read++
		if strings.Contains(shellCode(strings.Split(string(raw), "\n")), needle) {
			rel, _ := filepath.Rel(root, path)
			hits = append(hits, filepath.ToSlash(rel))
		}
	}
	for _, tree := range []string{"scripts", ".github"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".md") {
				return nil
			}
			visit(path)
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "Makefile")); err == nil {
		visit(filepath.Join(root, "Makefile"))
	}
	slices.Sort(hits)
	return hits, read
}
