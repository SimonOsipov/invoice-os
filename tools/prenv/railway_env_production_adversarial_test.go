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
			responses := productionRailway(productionVariables(`,"ENVIRONMENT":"production"`))
			responses[c.op] = c.body
			if c.op == "svcVars" {
				responses["vars"] = c.body
			}
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
}

var (
	// railwayEnvCall is `railway-env.sh` and the word after it.
	railwayEnvCall = regexp.MustCompile(`railway-env\.sh(?:\s+(\S+))?`)
	runKey         = regexp.MustCompile(`(?m)^(\s*-?\s*)run:\s*`)
)

// railwayEnvSubcommandFaults reports each railway-env.sh call in comment-stripped
// workflow text whose subcommand is not a literal, or is set-production-environment.
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
			}
		}
	}
	return calls, faults
}

func TestNoWorkflowPicksTheRailwayEnvSubcommandAtRunTime(t *testing.T) {
	t.Run("planted fixture", func(t *testing.T) {
		step := func(run string) string {
			return "jobs:\n  deploy:\n    steps:\n      - name: x\n        run: " + run + "\n"
		}
		for _, bad := range []string{
			`bash scripts/ci/railway-env.sh "$SUBCOMMAND" "$ENV_ID"`,
			`bash scripts/ci/railway-env.sh set-production-environment "$ENV_ID"`,
			`bash scripts/ci/railway-env.sh "set-production-$WHICH" "$ENV_ID"`,
			`bash scripts/ci/railway-env.sh ${{ inputs.subcommand }} "$ENV_ID"`,
			`bash scripts/ci/railway-env.sh`,
		} {
			if _, faults := railwayEnvSubcommandFaults(step(bad)); len(faults) == 0 {
				t.Errorf("fixture %q: no fault reported", bad)
			}
		}
		good := step(`if ! sel=$(echo "$r" | bash scripts/ci/railway-env.sh select-domain); then exit 1; fi`)
		if calls, faults := railwayEnvSubcommandFaults(good); calls != 1 || len(faults) != 0 {
			t.Errorf("a literal subcommand inside $(…) reports %d call(s) and %v, want 1 and none", calls, faults)
		}
		if calls, _ := railwayEnvSubcommandFaults(step("echo run railway-env.sh set-production-environment")); calls != 0 {
			t.Errorf("an echo counts as %d call(s), want 0", calls)
		}
	})

	dir := filepath.Join(repoRoot(t), ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		calls, faults := railwayEnvSubcommandFaults(string(raw))
		total += calls
		for _, f := range faults {
			t.Errorf("%s: %s", e.Name(), f)
		}
	}
	// Floor: today's workflows call railway-env.sh at least 15 times, each with a literal subcommand.
	if total < 15 {
		t.Errorf("found %d railway-env.sh call(s) across .github/workflows, want at least 15; the scan is not reading the calls", total)
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

func TestSetProductionAuthIsByHandOnly(t *testing.T) {
	const needle = "set-production-auth"
	stdout, stderr, code := runBashScript(t, "bash '"+railwayEnvScript(t)+"' no-such-subcommand\n")
	generic := stdout + stderr
	if code != 2 || !strings.Contains(generic, "set-production-environment <environment-id> ("+productionUsageNote+")") {
		t.Fatalf("control: the generic usage did not print its production note (exit %d); output = %q", code, generic)
	}
	if !regexp.MustCompile(regexp.QuoteMeta(needle) + `\b[^()]*\(` + regexp.QuoteMeta(productionUsageNote) + `\)`).MatchString(generic) {
		t.Errorf("the dispatcher's usage does not list %s followed by (%s); output = %q", needle, productionUsageNote, generic)
	}

	raw, err := os.ReadFile(railwayEnvScript(t))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	header := strings.Join(lines[:max(slices.Index(lines, "set -euo pipefail"), 0)], "\n")
	flow := strings.Join(strings.Fields(strings.ReplaceAll(header, "#", " ")), " ")
	if !regexp.MustCompile("`?" + needle + "`? is run " + regexp.QuoteMeta(productionUsageNote)).MatchString(flow) {
		t.Errorf("the header never says %s is run %s", needle, productionUsageNote)
	}

	root := repoRoot(t)
	dir := filepath.Join(root, ".github", "workflows")
	if control, read := workflowsNaming(t, dir, "set-fork-environment"); read < 3 || !slices.Contains(control, "dev-env.yml") {
		t.Fatalf("control: read %d workflow file(s) and found set-fork-environment in %v; the scan is broken", read, control)
	}
	if hits, _ := workflowsNaming(t, dir, needle); len(hits) != 0 {
		t.Errorf("%v run or name %s; production's auth configuration is written by hand", hits, needle)
	}
	hits, _ := executableNaming(t, root, needle)
	if !slices.Contains(hits, "scripts/ci/railway-env.sh") {
		t.Errorf("scripts/ci/railway-env.sh does not name %s in code; the command is not defined", needle)
	}
	for _, h := range hits {
		if h != "scripts/ci/railway-env.sh" {
			t.Errorf("%s names %s outside a comment; nothing CI executes may run it", h, needle)
		}
	}
}

func TestNothingCIRunsNamesSetProductionEnvironment(t *testing.T) {
	const definer = "scripts/ci/railway-env.sh"

	t.Run("planted fixture", func(t *testing.T) {
		root := t.TempDir()
		files := map[string]string{
			"scripts/ci/deploy-prod.sh":       "#!/bin/sh\nbash scripts/ci/railway-env.sh " + productionNeedle + " \"$ENV_ID\"\n",
			"scripts/ci/commented.sh":         "#!/bin/sh\n# bash scripts/ci/railway-env.sh " + productionNeedle + " \"$ENV_ID\"\n",
			".github/actions/prod/action.yml": "runs:\n  using: composite\n  steps:\n    - run: bash scripts/ci/deploy-prod.sh\n      shell: bash\n",
			"Makefile":                        "prod:\n\tbash scripts/ci/railway-env.sh " + productionNeedle + " $(ENV_ID)\n",
		}
		for name, body := range files {
			p := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		hits, read := executableNaming(t, root, productionNeedle)
		if want := []string{"Makefile", "scripts/ci/deploy-prod.sh"}; read != len(files) || !slices.Equal(hits, want) {
			t.Fatalf("planted: read %d file(s) and reported %v, want %d and %v", read, hits, len(files), want)
		}
	})

	root := repoRoot(t)
	// Control: the walk reads workflow code, and reads the one script that defines the subcommand.
	if control, _ := executableNaming(t, root, "set-fork-environment"); !slices.Contains(control, ".github/workflows/dev-env.yml") || !slices.Contains(control, definer) {
		t.Fatalf("control: set-fork-environment found in %v; the walk is not reading the workflows and scripts", control)
	}
	hits, read := executableNaming(t, root, productionNeedle)
	if read < 10 {
		t.Fatalf("read %d file(s) under scripts/, .github/ and Makefile; the walk is broken", read)
	}
	if !slices.Contains(hits, definer) {
		t.Fatalf("control: %s does not name %s in code; the walk cannot see the definition", definer, productionNeedle)
	}
	for _, h := range hits {
		if h != definer {
			t.Errorf("%s names %s outside a comment; only %s may, and nothing CI executes may run it", h, productionNeedle, definer)
		}
	}
}
