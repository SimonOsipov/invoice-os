package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// goJobRunsJWKSUnderRace reports whether one command line of one `go` job step
// runs the TestJWKS_ tests under -race; needles split across steps do not count.
func goJobRunsJWKSUnderRace(ciYAML string) bool {
	for _, step := range jobSteps(jobBlock(yamlCode(ciYAML), "go")) {
		for _, line := range strings.Split(runText(step), "\n") {
			if strings.Contains(line, "go test") && strings.Contains(line, "-race") &&
				strings.Contains(line, "TestJWKS_") && strings.Contains(line, "./internal/platform/auth/") {
				return true
			}
		}
	}
	return false
}

func TestCIGoJobRunsJWKSTestsUnderRace(t *testing.T) {
	const head = "on: push\njobs:\n  go:\n    runs-on: ubuntu-latest\n    steps:\n      - name: Test\n        run: go test ./...\n"
	const step = "      - name: JWKS under race\n        run: go test -race -count=1 -run 'TestJWKS_' ./internal/platform/auth/\n"
	const blockStep = "      - name: JWKS under race\n        run: |\n          go test -race -count=1 -run 'TestJWKS_' ./internal/platform/auth/\n"
	const noRace = "      - name: JWKS\n        run: go test -count=1 -run 'TestJWKS_' ./internal/platform/auth/\n"
	const split = "      - run: go test -race ./...\n      - run: go test -run 'TestJWKS_' ./internal/platform/auth/\n"
	const commented = "      # - run: go test -race -count=1 -run 'TestJWKS_' ./internal/platform/auth/\n"
	const tail = "  docker-canary:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo canary\n"

	for _, c := range []struct {
		name string
		yaml string
		want bool
	}{
		{"with the step", head + step + tail, true},
		{"with the step as a block scalar", head + blockStep + tail, true},
		{"without the step", head + tail, false},
		{"without -race", head + noRace + tail, false},
		{"needles split across steps", head + split + tail, false},
		{"step commented out", head + commented + tail, false},
		{"step in another job", head + tail + step, false},
	} {
		if got := goJobRunsJWKSUnderRace(c.yaml); got != c.want {
			t.Errorf("fixture %q: runs JWKS under race = %v, want %v", c.name, got, c.want)
		}
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// Control: the scan finds the real go job's steps before judging them.
	steps := jobSteps(jobBlock(yamlCode(string(raw)), "go"))
	if len(steps) == 0 {
		t.Fatal("found no steps in the ci.yml go job; the scan is broken")
	}
	if !goJobRunsJWKSUnderRace(string(raw)) {
		t.Errorf(".github/workflows/ci.yml: no `go` job step runs `go test -race -run 'TestJWKS_' ./internal/platform/auth/`")
	}
}

const idpJobIf = "needs.changes.outputs.idp == 'true'"

// jobIfExpr returns a job's own if: expression without the ${{ }} wrapper.
func jobIfExpr(block []string) string {
	for _, l := range block {
		if v, ok := strings.CutPrefix(l, "    if:"); ok {
			return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(v), "${{"), "}}"))
		}
	}
	return ""
}

// changesOutputs returns the job's outputs: entries, one trimmed line each.
func changesOutputs(block []string) []string {
	var out []string
	in := false
	for _, l := range block {
		indent := len(l) - len(strings.TrimLeft(l, " "))
		switch {
		case strings.TrimSpace(l) == "":
		case indent == 4:
			in = strings.TrimSpace(l) == "outputs:"
		case in && indent == 6:
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}

// filterPaths returns the paths-filter entries listed under name: in the changes job.
func filterPaths(block []string, name string) []string {
	var paths []string
	keyIndent := -1
	for _, l := range block {
		trimmed := strings.TrimSpace(l)
		indent := len(l) - len(strings.TrimLeft(l, " "))
		if trimmed == "" {
			continue
		}
		if keyIndent >= 0 {
			if indent <= keyIndent {
				keyIndent = -1
			} else if item, ok := strings.CutPrefix(trimmed, "- "); ok {
				paths = append(paths, strings.Trim(strings.TrimSpace(item), `"'`))
				continue
			}
		}
		if trimmed == name+":" && indent > 6 {
			keyIndent = indent
		}
	}
	return paths
}

// aggregateFailsOnFailureAndCancel is aggregateFailsOn that also requires a cancelled
// job to fail the roll-up; `!= "success"` covers both.
func aggregateFailsOnFailureAndCancel(run, job string) bool {
	lines := strings.Split(run, "\n")
	ref := "${{ needs." + job + ".result }}"
	for i, l := range lines {
		if !strings.HasPrefix(l, "if ") || !strings.Contains(l, ref) {
			continue
		}
		both := strings.Contains(l, `= "failure"`) && strings.Contains(l, `= "cancelled"`)
		if !both && !strings.Contains(l, `!= "success"`) {
			continue
		}
		for _, next := range lines[i:] {
			if strings.Contains(next, "exit 1") {
				return true
			}
			if next == "fi" {
				break
			}
		}
	}
	return false
}

// idpGateStep reports whether one command runs TestIdP on the auth package through the skip gate.
func idpGateStep(block []string) bool {
	for _, line := range strings.Split(runText(block), "\n") {
		if strings.Contains(line, "scripts/ci/rls-test-gate.sh") &&
			(strings.Contains(line, "-run TestIdP") || strings.Contains(line, "-run 'TestIdP")) &&
			strings.Contains(line, "./internal/platform/auth/") {
			return true
		}
	}
	return false
}

func idpJobProblems(ciYAML string) []string {
	lines := yamlCode(ciYAML)
	var problems []string

	changes := jobBlock(lines, "changes")
	if len(changes) == 0 {
		return []string{"no changes job"}
	}
	if !slices.Contains(changesOutputs(changes), "idp: ${{ steps.filter.outputs.idp }}") {
		problems = append(problems, "changes.outputs has no `idp: ${{ steps.filter.outputs.idp }}`; the idp job's if: would read an empty output and never run")
	}
	paths := filterPaths(changes, "idp")
	for _, want := range []string{"sidecar/auth/**", "internal/platform/auth/**"} {
		if !slices.Contains(paths, want) {
			problems = append(problems, fmt.Sprintf("the idp paths filter %v does not list %q", paths, want))
		}
	}

	if idp := jobBlock(lines, "idp"); len(idp) == 0 {
		problems = append(problems, "no idp job")
	} else {
		if got := jobIfExpr(idp); got != idpJobIf {
			problems = append(problems, fmt.Sprintf("the idp job if: reads %q, want %q", got, idpJobIf))
		}
		if !idpGateStep(idp) {
			problems = append(problems, "no idp job step runs `scripts/ci/rls-test-gate.sh … -run TestIdP ./internal/platform/auth/...`; a skipped TestIdP_ test would pass")
		}
	}

	ci := jobBlock(lines, "ci")
	if len(ci) == 0 {
		return append(problems, "no ci job")
	}
	if !slices.Contains(jobNeeds(ci), "idp") {
		problems = append(problems, fmt.Sprintf("the ci job's needs: %v does not list idp", jobNeeds(ci)))
	}
	if !aggregateFailsOnFailureAndCancel(runText(ci), "idp") {
		problems = append(problems, "the ci job's shell block does not fail on a failed or cancelled needs.idp.result")
	}
	return problems
}

func readCIYAML(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestCIAggregateAssertsIdPJob(t *testing.T) {
	const changes = "jobs:\n  changes:\n    runs-on: ubuntu-latest\n    outputs:\n      go: ${{ steps.filter.outputs.go }}\n      idp: ${{ steps.filter.outputs.idp }}\n" +
		"    steps:\n      - uses: dorny/paths-filter@v3\n        id: filter\n        with:\n          filters: |\n" +
		"            go:\n              - 'cmd/**'\n            idp:\n              - 'sidecar/auth/**'\n              - 'internal/platform/auth/**'\n              - 'Makefile'\n"
	const idp = "  idp:\n    needs: changes\n    if: needs.changes.outputs.idp == 'true'\n    runs-on: ubuntu-latest\n    steps:\n" +
		"      - run: scripts/ci/rls-test-gate.sh -count=1 -run TestIdP ./internal/platform/auth/...\n"
	const ci = "  ci:\n    needs: [changes, go, idp]\n    if: always()\n    runs-on: ubuntu-latest\n    steps:\n      - run: |\n" +
		"          echo \"idp: ${{ needs.idp.result }}\"\n" +
		"          if [ \"${{ needs.idp.result }}\" = \"failure\" ] || [ \"${{ needs.idp.result }}\" = \"cancelled\" ]; then\n" +
		"            echo \"::error::IdP suite failed\"; exit 1\n          fi\n"
	good := changes + idp + ci

	if p := idpJobProblems(good); len(p) != 0 {
		t.Fatalf("the good fixture reports %v; the scan is broken", p)
	}
	for _, c := range []struct{ name, yaml, want string }{
		{"no idp output", strings.Replace(good, "      idp: ${{ steps.filter.outputs.idp }}\n", "", 1), "changes.outputs"},
		{"filter misses auth", strings.Replace(good, "              - 'internal/platform/auth/**'\n", "", 1), "internal/platform/auth/**"},
		{"job reads the go output", strings.Replace(good, "outputs.idp == 'true'", "outputs.go == 'true'", 1), "if: reads"},
		{"tests run without the gate", strings.Replace(good, "scripts/ci/rls-test-gate.sh -count=1", "go test -count=1", 1), "rls-test-gate.sh"},
		{"idp missing from needs", strings.Replace(good, "[changes, go, idp]", "[changes, go]", 1), "needs:"},
		{"echo only", strings.Replace(good, "            echo \"::error::IdP suite failed\"; exit 1\n", "            echo \"::error::IdP suite failed\"\n", 1), "shell block"},
		{"failure only", strings.Replace(good, " || [ \"${{ needs.idp.result }}\" = \"cancelled\" ]", "", 1), "shell block"},
		{"no idp job", strings.Replace(good, idp, "", 1), "no idp job"},
	} {
		if c.yaml == good {
			t.Fatalf("fixture %q: the edit did not apply", c.name)
		}
		p := idpJobProblems(c.yaml)
		if !slices.ContainsFunc(p, func(s string) bool { return strings.Contains(s, c.want) }) {
			t.Errorf("fixture %q: problems %v, want one naming %q", c.name, p, c.want)
		}
	}

	raw := readCIYAML(t)
	if len(jobBlock(yamlCode(raw), "ci")) == 0 || len(changesOutputs(jobBlock(yamlCode(raw), "changes"))) == 0 {
		t.Fatal("found no ci job or no changes outputs in ci.yml; the scan is broken")
	}
	for _, p := range idpJobProblems(raw) {
		t.Errorf(".github/workflows/ci.yml: %s", p)
	}
}

var shellVarRE = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?$|^\$\{\{\s*env\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}$`)

// idpUpDSN returns the first idp-up.sh argument in the idp job, and that argument
// resolved one level through the step's or the job's env:.
func idpUpDSN(ciYAML string) (arg, resolved string, found bool) {
	block := jobBlock(yamlCode(ciYAML), "idp")
	for _, step := range jobSteps(block) {
		run := strings.ReplaceAll(runText(step), "\\\n", " ")
		for _, line := range strings.Split(run, "\n") {
			fields := strings.Fields(line)
			for i, f := range fields {
				if !strings.HasSuffix(f, "idp-up.sh") || i+1 >= len(fields) {
					continue
				}
				arg = strings.Trim(fields[i+1], `"'`)
				resolved = arg
				if m := shellVarRE.FindStringSubmatch(arg); m != nil {
					name := m[1] + m[2]
					for _, l := range append(append([]string{}, step...), block...) {
						if v, ok := strings.CutPrefix(strings.TrimSpace(l), name+":"); ok {
							resolved = strings.TrimSpace(v)
							break
						}
					}
				}
				return arg, resolved, true
			}
		}
	}
	return "", "", false
}

func idpHarnessDSNProblem(ciYAML string) string {
	arg, resolved, found := idpUpDSN(ciYAML)
	switch {
	case !found:
		return "no idp job step calls idp-up.sh with an argument"
	case strings.Contains(arg+resolved, "SUPERUSER") || strings.Contains(resolved, "//postgres:"):
		return fmt.Sprintf("idp-up.sh's DSN argument %q (resolves to %q) is the superuser; it bypasses RLS, grants and search_path", arg, resolved)
	case !strings.Contains(arg+resolved, "AUTH_ADMIN") && !strings.Contains(resolved, "supabase_auth_admin"):
		return fmt.Sprintf("idp-up.sh's DSN argument %q (resolves to %q) is not the supabase_auth_admin DSN", arg, resolved)
	}
	return ""
}

func TestIdPHarnessNeverUsesTheSuperuserDSN(t *testing.T) {
	const head = "jobs:\n  idp:\n    runs-on: ubuntu-latest\n    env:\n" +
		"      DATABASE_SUPERUSER_URL: postgres://postgres:postgres@localhost:5432/invoice_os\n" +
		"      DATABASE_AUTH_ADMIN_URL: postgres://supabase_auth_admin:auth_admin@localhost:5432/invoice_os\n    steps:\n"
	const good = head + "      - name: Start the IdP\n        run: scripts/ci/idp-up.sh \"$DATABASE_AUTH_ADMIN_URL\" 5432\n"

	if p := idpHarnessDSNProblem(good); p != "" {
		t.Fatalf("the good fixture reports %q; the scan is broken", p)
	}
	for _, c := range []struct{ name, yaml string }{
		{"superuser DSN", strings.Replace(good, "$DATABASE_AUTH_ADMIN_URL", "$DATABASE_SUPERUSER_URL", 1)},
		{"superuser through a step alias", head + "      - env:\n          DSN: ${{ env.DATABASE_SUPERUSER_URL }}\n        run: scripts/ci/idp-up.sh \"$DSN\" 5432\n"},
		{"no idp-up step", head + "      - run: echo hi\n"},
	} {
		if idpHarnessDSNProblem(c.yaml) == "" {
			t.Errorf("fixture %q: no problem reported", c.name)
		}
	}

	if p := idpHarnessDSNProblem(readCIYAML(t)); p != "" {
		t.Errorf(".github/workflows/ci.yml: %s", p)
	}
}
