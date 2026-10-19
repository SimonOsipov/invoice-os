// workflow_token_test.go pins two rules for workflows that hold RAILWAY_API_TOKEN: no dependency
// install runs scripts in the same job, and no trusted trigger or inherited secret reaches the token.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

const railwayTokenRef = "secrets.RAILWAY_API_TOKEN"

var (
	// Flags of pnpm, npm and yarn that consume the next field as their value.
	installValueFlags = map[string]bool{
		"--filter": true, "-F": true, "--dir": true, "-C": true, "--prefix": true, "--cwd": true,
		"--workspace": true, "--registry": true, "--reporter": true, "--config": true,
	}
	jsInstallSubs  = []string{"install", "i", "ci", "add", "update", "up", "upgrade", "rebuild"}
	trustedOnRE    = regexp.MustCompile(`\b(pull_request_target|workflow_run|workflow_call)\b`)
	secretsInherit = regexp.MustCompile(`^secrets:\s*inherit\s*$`)
	localUses      = regexp.MustCompile(`^uses:\s*['"]?\./`)
)

// subcommandOf returns the first field that is not a flag, skipping the value of a value flag, and the fields after it.
func subcommandOf(fields []string) (string, []string) {
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		switch {
		case installValueFlags[f]:
			i++
		case strings.HasPrefix(f, "-"):
		default:
			return f, fields[i+1:]
		}
	}
	return "", nil
}

// classifyInstall reports whether cmd installs dependencies and whether it passes --ignore-scripts.
func classifyInstall(cmd string) (install, safe bool) {
	f := strings.Fields(cmd)
	for len(f) > 0 && (shellLeadWords[f[0]] || shellAssignment.MatchString(f[0])) {
		f = f[1:]
	}
	if len(f) == 0 {
		return false, false
	}
	sub, rest := subcommandOf(f[1:])
	switch f[0] {
	case "pnpm", "npm", "bun":
		install = slices.Contains(jsInstallSubs, sub)
	case "yarn":
		install = sub == "" || slices.Contains(jsInstallSubs, sub)
	case "pip", "pip3":
		return sub == "install", false
	case "poetry":
		return sub == "install" || sub == "add" || sub == "update", false
	case "uv":
		if sub == "pip" {
			sub, _ = subcommandOf(rest)
			return sub == "install" || sub == "sync", false
		}
		return sub == "sync" || sub == "add", false
	}
	return install, install && (slices.Contains(f, "--ignore-scripts") || slices.Contains(f, "--ignore-scripts=true"))
}

type tokenInstall struct {
	job, step, cmd string
	safe           bool
}

// tokenInstalls lists every dependency install in a job that holds the Railway token at workflow, job or step level.
func tokenInstalls(yaml string) []tokenInstall {
	lines := strings.Split(yaml, "\n")
	hasToken := func(l string) bool { return strings.Contains(l, railwayTokenRef) }
	workflowToken := slices.ContainsFunc(blockLines(lines, 0, "env"), hasToken)
	var out []tokenInstall
	for _, job := range workflowJobsOf(yaml) {
		if !workflowToken && !slices.ContainsFunc(job.lines, hasToken) {
			continue
		}
		for _, s := range job.steps() {
			name := s.keys["name"]
			if name == "" {
				name = fmt.Sprintf("#%d", s.index)
			}
			for _, cmd := range invocations(s.keys["run"], "") {
				if install, safe := classifyInstall(cmd); install {
					out = append(out, tokenInstall{job.name, name, cmd, safe})
				}
			}
		}
		// workflowStep drops `with:` children, so the action input is read from the job lines.
		for _, l := range job.lines {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "run_install:"); ok && strings.Trim(strings.TrimSpace(v), `'"`) != "false" {
				out = append(out, tokenInstall{job.name, "run_install", "run_install:" + v, false})
			}
		}
	}
	return out
}

// tokenInstallJobs names the jobs that hold the token and install, whether or not the install is safe.
func tokenInstallJobs(yaml string) []string {
	var jobs []string
	for _, in := range tokenInstalls(yaml) {
		if !slices.Contains(jobs, in.job) {
			jobs = append(jobs, in.job)
		}
	}
	return jobs
}

func installTokenFaults(yaml string) []string {
	var faults []string
	for _, in := range tokenInstalls(yaml) {
		if !in.safe {
			faults = append(faults, fmt.Sprintf("job %s step %s: %s installs with scripts while the job holds RAILWAY_API_TOKEN", in.job, in.step, in.cmd))
		}
	}
	return faults
}

// onBlockLines returns the comment-stripped `on:` line remainder and its child lines.
func onBlockLines(lines []string) []string {
	var out []string
	for _, l := range stripHashComments(lines) {
		if rest, ok := strings.CutPrefix(l, "on:"); ok && strings.TrimSpace(rest) != "" {
			out = append(out, rest)
		}
	}
	return append(out, blockLines(lines, 0, "on")...)
}

func trustedTriggerFaults(yaml string) []string {
	lines := strings.Split(yaml, "\n")
	var faults []string
	for _, l := range onBlockLines(lines) {
		if m := trustedOnRE.FindString(l); m != "" {
			faults = append(faults, "on: names "+m)
		}
	}
	for _, l := range stripHashComments(lines) {
		t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "- "))
		switch {
		case secretsInherit.MatchString(t):
			faults = append(faults, "secrets: inherit")
		case localUses.MatchString(t):
			faults = append(faults, "local "+t)
		}
	}
	return faults
}

// goFilterPaths returns the paths listed under the `go:` key of the paths-filter in ci.yml.
func goFilterPaths(ci string) []string {
	var paths []string
	inFilters, inGo := false, false
	for _, l := range strings.Split(ci, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case strings.Contains(t, "filters: |"):
			inFilters = true
		case inFilters && t == "go:":
			inGo = true
		case inGo && strings.HasPrefix(t, "- "):
			paths = append(paths, strings.Trim(strings.TrimPrefix(t, "- "), `'"`))
		case inGo:
			return paths
		}
	}
	return paths
}

func goFilterFaults(ci string, tokenFiles []string) []string {
	paths := goFilterPaths(ci)
	var faults []string
	for _, f := range tokenFiles {
		if !slices.Contains(paths, ".github/workflows/"+f) {
			faults = append(faults, "go filter of ci.yml does not list .github/workflows/"+f)
		}
	}
	return faults
}

// workflowTexts reads every workflow file on disk, keyed by file name.
func workflowTexts(t *testing.T) (names []string, texts map[string]string) {
	t.Helper()
	dir := filepath.Join(repoRoot(t), ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	texts = map[string]string{}
	for _, e := range entries {
		if ext := filepath.Ext(e.Name()); ext == ".yml" || ext == ".yaml" {
			names = append(names, e.Name())
			texts[e.Name()] = readWorkflow(t, e.Name())
		}
	}
	sort.Strings(names)
	return names, texts
}

var knownWorkflows = []string{"ci.yml", "dev-env-sweeper.yml", "dev-env-teardown.yml", "dev-env.yml", "idp-release-watch.yml", "railway-invariants.yml"}

func requireScanned(t *testing.T, names []string) {
	t.Helper()
	for _, want := range knownWorkflows {
		if !slices.Contains(names, want) {
			t.Fatalf("control: the scan did not cover %s (covered %v)", want, names)
		}
	}
}

// replaceOnce edits text and fails when the target does not occur exactly once, so no mutation is vacuous.
func replaceOnce(t *testing.T, text, old, repl string) string {
	t.Helper()
	if n := strings.Count(text, old); n != 1 {
		t.Fatalf("edit target %q occurs %d times, want 1", old, n)
	}
	return strings.Replace(text, old, repl, 1)
}

// devEnvE2EInstall returns dev-env.yml with the e2e job installing with or without --ignore-scripts.
func devEnvE2EInstall(t *testing.T, flag bool) string {
	t.Helper()
	text := readWorkflow(t, "dev-env.yml")
	start, end := strings.Index(text, "\n  e2e:\n"), strings.Index(text, "\n  topology:\n")
	if strings.Count(text, "\n  e2e:\n") != 1 || end < start || start < 0 {
		t.Fatalf("cannot bound the e2e job (e2e at %d, topology at %d)", start, end)
	}
	const install = "run: pnpm install --frozen-lockfile"
	seg := strings.ReplaceAll(text[start:end], install+" --ignore-scripts", install)
	if n := strings.Count(seg, install); n != 1 {
		t.Fatalf("e2e job holds %d %q lines, want 1", n, install)
	}
	if flag {
		seg = strings.Replace(seg, install, install+" --ignore-scripts", 1)
	}
	return text[:start] + seg + text[end:]
}

// tokenJob is a one-step workflow whose step runs cmd and holds the token in step env.
func tokenJob(cmd string) string {
	return "on: pull_request\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - name: s\n        run: " + cmd +
		"\n        env:\n          RAILWAY_API_TOKEN: ${{ secrets.RAILWAY_API_TOKEN }}\n"
}

func TestNoInstallJobHoldsTheRailwayToken(t *testing.T) {
	t.Parallel()
	names, texts := workflowTexts(t)
	requireScanned(t, names)
	if !slices.Contains(tokenInstallJobs(texts["dev-env.yml"]), "e2e") {
		t.Fatalf("control: tokenInstallJobs(dev-env.yml) = %v, want it to name e2e", tokenInstallJobs(texts["dev-env.yml"]))
	}
	for _, name := range names {
		for _, f := range installTokenFaults(texts[name]) {
			t.Errorf("%s: %s", name, f)
		}
	}
}

func TestInstallTokenFaultsSeeEachShape(t *testing.T) {
	t.Parallel()
	one := func(t *testing.T, faults []string, job string) {
		t.Helper()
		if len(faults) != 1 || !strings.Contains(faults[0], "job "+job+" ") {
			t.Fatalf("faults = %q, want exactly one naming job %s", faults, job)
		}
	}

	t.Run("mutate_e2e_drop_flag", func(t *testing.T) {
		t.Parallel()
		if got := installTokenFaults(devEnvE2EInstall(t, true)); len(got) != 0 {
			t.Fatalf("e2e with the flag has faults: %q", got)
		}
		one(t, installTokenFaults(devEnvE2EInstall(t, false)), "e2e")
	})
	t.Run("mutate_e2e_add_pip_install", func(t *testing.T) {
		t.Parallel()
		base := devEnvE2EInstall(t, true)
		mut := replaceOnce(t, base, "      - name: Verify SPA hostnames route (repair a dark domain)\n",
			"      - name: Python deps\n        run: pip install -r requirements.txt\n      - name: Verify SPA hostnames route (repair a dark domain)\n")
		one(t, installTokenFaults(mut), "e2e")
	})
	t.Run("mutate_topology_gains_token", func(t *testing.T) {
		t.Parallel()
		base := devEnvE2EInstall(t, true)
		mut := replaceOnce(t, base, "          SHARD: ${{ matrix.shard }}\n",
			"          SHARD: ${{ matrix.shard }}\n          RAILWAY_API_TOKEN: ${{ secrets.RAILWAY_API_TOKEN }}\n")
		one(t, installTokenFaults(mut), "topology")
	})
	t.Run("mutate_workflow_env_token_job_installs", func(t *testing.T) {
		t.Parallel()
		base := readWorkflow(t, "railway-invariants.yml")
		if got := installTokenFaults(base); len(got) != 0 {
			t.Fatalf("railway-invariants.yml has faults before the edit: %q", got)
		}
		mut := replaceOnce(t, base, "      - name: Assert PR Environments are OFF\n",
			"      - name: Install\n        run: pnpm install --frozen-lockfile\n      - name: Assert PR Environments are OFF\n")
		one(t, installTokenFaults(mut), "pr-environments-off")
	})

	t.Run("token_in_other_job", func(t *testing.T) {
		t.Parallel()
		const installJob = "  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: pnpm install --frozen-lockfile\n"
		const tokenJobB = "  b:\n    runs-on: ubuntu-latest\n    steps:\n      - run: bash x.sh\n        env:\n          RAILWAY_API_TOKEN: ${{ secrets.RAILWAY_API_TOKEN }}\n"
		if got := installTokenFaults("on: pull_request\njobs:\n" + installJob + tokenJobB); len(got) != 0 {
			t.Fatalf("install and token in different jobs faulted: %q", got)
		}
		same := "on: pull_request\njobs:\n" + installJob + "        env:\n          RAILWAY_API_TOKEN: ${{ secrets.RAILWAY_API_TOKEN }}\n"
		one(t, installTokenFaults(same), "a")
	})
	t.Run("echo_mentions_install", func(t *testing.T) {
		t.Parallel()
		if got := installTokenFaults(tokenJob(`echo "pnpm install"`)); len(got) != 0 {
			t.Fatalf("an echo of an install faulted: %q", got)
		}
		if got := installTokenFaults(tokenJob("bash scripts/ci/railway-env.sh x")); len(got) != 0 {
			t.Fatalf("a token job with no install faulted: %q", got)
		}
		one(t, installTokenFaults(tokenJob(`echo "pnpm install" && pnpm install`)), "a")
	})

	shapes := []struct {
		name  string
		cases map[string]int
	}{
		{"chained_npm_ci", map[string]int{"npm ci && npm test": 1, "npm ci --ignore-scripts && npm test": 0, "npm test": 0}},
		{"filter_flag_before_install", map[string]int{
			"pnpm --filter @invoice-os/e2e install":                                      1,
			"pnpm --filter=@invoice-os/e2e install":                                      1,
			"pnpm --filter @invoice-os/e2e install --ignore-scripts":                     0,
			"pnpm --filter @invoice-os/e2e exec playwright install --with-deps chromium": 0,
		}},
		{"dir_flag_before_install", map[string]int{
			"pnpm -C frontend/app install":                  1,
			"npm --prefix e2e ci":                           1,
			"pnpm -C frontend/app install --ignore-scripts": 0,
			"npm --prefix e2e ci --ignore-scripts":          0,
		}},
		{"recursive_flag_install", map[string]int{"pnpm -r install": 1, "pnpm --recursive install": 1, "pnpm -r install --ignore-scripts": 0}},
		{"bare_yarn", map[string]int{"yarn": 1, "yarn --ignore-scripts": 0, "yarn build": 0}},
		{"pip_install", map[string]int{
			"pip install -r requirements.txt":       1,
			"pip3 install -r requirements.txt":      1,
			"pip install --ignore-scripts -r r.txt": 1,
			"uv sync":                               1,
			"poetry install":                        1,
			"pip --version":                         0,
		}},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			for cmd, want := range s.cases {
				if got := installTokenFaults(tokenJob(cmd)); len(got) != want {
					t.Errorf("%q: faults = %q, want %d", cmd, got, want)
				}
			}
		})
	}

	t.Run("run_install_true", func(t *testing.T) {
		t.Parallel()
		job := func(v string) string {
			return "on: pull_request\njobs:\n  a:\n    runs-on: ubuntu-latest\n    env:\n      RAILWAY_API_TOKEN: ${{ secrets.RAILWAY_API_TOKEN }}\n" +
				"    steps:\n      - uses: pnpm/action-setup@v4\n        with:\n          run_install: " + v + "\n"
		}
		one(t, installTokenFaults(job("true")), "a")
		if got := installTokenFaults(job("false")); len(got) != 0 {
			t.Fatalf("run_install: false faulted: %q", got)
		}
	})
}

func TestNoWorkflowReachesSecretsThroughATrustedTrigger(t *testing.T) {
	t.Parallel()
	names, texts := workflowTexts(t)
	requireScanned(t, names)
	if on := strings.Join(onBlockLines(strings.Split(texts["dev-env.yml"], "\n")), "\n"); !regexp.MustCompile(`\bpull_request\b`).MatchString(on) {
		t.Fatalf("control: the on: block of dev-env.yml names no pull_request: %q", on)
	}
	for _, name := range names {
		for _, f := range trustedTriggerFaults(texts[name]) {
			t.Errorf("%s: %s", name, f)
		}
	}
}

func TestTrustedTriggerFaultsSeeEachShape(t *testing.T) {
	t.Parallel()
	const jobs = "jobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n"
	one := func(t *testing.T, yaml string) {
		t.Helper()
		if got := trustedTriggerFaults(yaml); len(got) != 1 {
			t.Fatalf("faults = %q, want exactly one for:\n%s", got, yaml)
		}
	}

	t.Run("mutate_pull_request_target", func(t *testing.T) {
		t.Parallel()
		base := readWorkflow(t, "dev-env.yml")
		if got := trustedTriggerFaults(base); len(got) != 0 {
			t.Fatalf("dev-env.yml has faults before the edit: %q", got)
		}
		one(t, replaceOnce(t, base, "on:\n  pull_request:\n", "on:\n  pull_request_target:\n  pull_request:\n"))
	})
	t.Run("inline_workflow_run", func(t *testing.T) {
		t.Parallel()
		one(t, "on: [workflow_run]\n"+jobs)
	})
	t.Run("workflow_call_secrets_inherit_local_action", func(t *testing.T) {
		t.Parallel()
		one(t, "on:\n  workflow_call:\n"+jobs)
		one(t, "on: pull_request\njobs:\n  a:\n    uses: o/r/.github/workflows/x.yml@main\n    secrets: inherit\n")
		one(t, "on: pull_request\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: ./.github/actions/x\n")
		if got := trustedTriggerFaults("on: pull_request\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n"); len(got) != 0 {
			t.Fatalf("a marketplace action faulted: %q", got)
		}
	})
	t.Run("comment_only", func(t *testing.T) {
		t.Parallel()
		if got := trustedTriggerFaults("# pull_request_target is not used\non:\n  pull_request:\n" + jobs); len(got) != 0 {
			t.Fatalf("a comment faulted: %q", got)
		}
		one(t, "on:\n  pull_request:\n  pull_request_target:\n"+jobs)
	})
}

func TestGoFilterCoversEveryTokenWorkflow(t *testing.T) {
	t.Parallel()
	names, texts := workflowTexts(t)
	requireScanned(t, names)
	var tokenFiles []string
	for _, n := range names {
		if slices.ContainsFunc(stripHashComments(strings.Split(texts[n], "\n")), func(l string) bool { return strings.Contains(l, railwayTokenRef) }) {
			tokenFiles = append(tokenFiles, n)
		}
	}
	for _, want := range []string{"dev-env.yml", "railway-invariants.yml", "dev-env-teardown.yml", "dev-env-sweeper.yml"} {
		if !slices.Contains(tokenFiles, want) {
			t.Fatalf("control: %s not found among the token workflows %v", want, tokenFiles)
		}
	}
	if !slices.Contains(goFilterPaths(texts["ci.yml"]), ".github/workflows/ci.yml") {
		t.Fatalf("control: the go filter of ci.yml was not parsed: %q", goFilterPaths(texts["ci.yml"]))
	}
	for _, f := range goFilterFaults(texts["ci.yml"], tokenFiles) {
		t.Error(f)
	}

	t.Run("mutate_filter_drops_a_workflow", func(t *testing.T) {
		t.Parallel()
		const (
			anchor  = "              - '.github/workflows/idp-release-watch.yml'\n"
			sweeper = "              - '.github/workflows/dev-env-sweeper.yml'\n"
			tear    = "              - '.github/workflows/dev-env-teardown.yml'\n"
		)
		ci := texts["ci.yml"]
		for _, line := range []string{tear, sweeper} {
			if !strings.Contains(ci, line) {
				ci = replaceOnce(t, ci, anchor, anchor+line)
			}
		}
		if got := goFilterFaults(ci, tokenFiles); len(got) != 0 {
			t.Fatalf("a filter listing every token workflow has faults: %q", got)
		}
		got := goFilterFaults(replaceOnce(t, ci, sweeper, ""), tokenFiles)
		if len(got) != 1 || !strings.Contains(got[0], "dev-env-sweeper.yml") {
			t.Fatalf("faults = %q, want exactly one naming dev-env-sweeper.yml", got)
		}
	})
}
