// workflow_token_test.go pins two rules for workflows that hold RAILWAY_API_TOKEN: no dependency
// install runs scripts in the same job, and no trusted trigger or inherited secret reaches the token.
package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

var (
	// Flags of the package managers that consume the next field as their value.
	installValueFlags = map[string]bool{
		"--filter": true, "-F": true, "--dir": true, "-C": true, "--prefix": true, "--cwd": true,
		"--workspace": true, "--registry": true, "--reporter": true, "--config": true,
		"--store-dir": true, "--cache": true, "--cache-dir": true, "--loglevel": true, "--userconfig": true,
	}
	jsInstallSubs = []string{"install", "i", "ci", "add", "update", "up", "upgrade", "rebuild", "install-test", "it", "ic", "cit", "install-ci-test"}
	// Words that run the next command: its tool is the next field.
	wrapperWords = map[string]bool{"corepack": true, "env": true, "command": true, "nohup": true}
	pythonRE     = regexp.MustCompile(`^python[0-9.]*$`)
	pipRE        = regexp.MustCompile(`^pip[0-9.]*$`)
	// The secrets context is case-insensitive and takes the bracket form.
	tokenRefRE     = regexp.MustCompile(`(?i)\bsecrets(\.|\[\s*['"])RAILWAY_API_TOKEN\b`)
	trustedOnRE    = regexp.MustCompile(`(?i)\b(pull_request_target|workflow_run|workflow_call)\b`)
	secretsInherit = regexp.MustCompile(`^secrets:\s*['"]?inherit['"]?\s*$`)
	localUses      = regexp.MustCompile(`^uses:\s*['"]?\./`)
)

// subcommandOf returns the first field that is not a flag, skipping the value of a value flag, and the fields after it.
func subcommandOf(fields []string, extraValueFlags ...string) (string, []string) {
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		switch {
		case installValueFlags[f] || slices.Contains(extraValueFlags, f):
			i++
		case strings.HasPrefix(f, "-"):
		default:
			return f, fields[i+1:]
		}
	}
	return "", nil
}

// stripWrappers drops shell lead words, assignments, sudo, corepack and `python -m` so f[0] is the tool.
func stripWrappers(f []string) []string {
	for len(f) > 0 {
		f[0] = strings.TrimLeft(f[0], "(")
		switch {
		case f[0] == "":
			f = f[1:]
		case f[0] == "sudo":
			f = f[1:]
			for len(f) > 0 && strings.HasPrefix(f[0], "-") {
				n := 1
				if f[0] == "-u" || f[0] == "-g" {
					n = 2
				}
				f = f[min(n, len(f)):]
			}
		case f[0] == "timeout":
			f = skipWrapperArgs(f[1:], map[string]bool{"-k": true, "-s": true, "--kill-after": true, "--signal": true}, 1)
		case f[0] == "nice":
			f = skipWrapperArgs(f[1:], map[string]bool{"-n": true, "--adjustment": true}, 0)
		case f[0] == "xargs":
			f = skipWrapperArgs(f[1:], map[string]bool{"-n": true, "-I": true, "-P": true, "-L": true, "-d": true, "-E": true, "-s": true, "-a": true}, 0)
		case shellLeadWords[f[0]] || shellAssignment.MatchString(f[0]) || wrapperWords[f[0]]:
			f = f[1:]
		case pythonRE.MatchString(f[0]) && len(f) > 2 && f[1] == "-m":
			f = f[2:]
		default:
			return f
		}
	}
	return f
}

// skipWrapperArgs drops the leading flags (and the values of valueFlags) of a wrapper, then `positional` bare fields.
func skipWrapperArgs(f []string, valueFlags map[string]bool, positional int) []string {
	for len(f) > 0 && strings.HasPrefix(f[0], "-") {
		n := 1
		if valueFlags[f[0]] {
			n = 2
		}
		f = f[min(n, len(f)):]
	}
	return f[min(positional, len(f)):]
}

// unquoteOnce strips one matching pair of surrounding quotes.
func unquoteOnce(s string) string {
	s = strings.TrimSpace(s)
	if n := len(s); n >= 2 && (s[0] == '"' || s[0] == '\'') && s[n-1] == s[0] {
		return s[1 : n-1]
	}
	return s
}

// classifyInstall reports whether cmd installs dependencies and whether it passes --ignore-scripts.
func classifyInstall(cmd string) (install, safe bool) {
	f := stripWrappers(strings.Fields(strings.TrimRight(cmd, ") ")))
	if len(f) == 0 {
		return false, false
	}
	name := path.Base(f[0])
	if name == "bash" || name == "sh" {
		for i, a := range f[1:] {
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.HasSuffix(a, "c") {
				return classifyScript(strings.Join(f[i+2:], " "))
			}
		}
		return false, false
	}
	if name == "npx" || name == "bunx" || name == "uvx" {
		return true, false
	}
	var extra []string
	if name == "npm" {
		extra = []string{"-w"}
	}
	sub, rest := subcommandOf(f[1:], extra...)
	switch {
	case name == "npm" && sub == "exec", name == "pnpm" && sub == "dlx", name == "yarn" && sub == "dlx":
		return true, false
	case name == "pnpm" || name == "npm" || name == "bun":
		install = slices.Contains(jsInstallSubs, sub)
	case name == "yarn":
		help := slices.ContainsFunc(f, func(a string) bool { return slices.Contains([]string{"--version", "-v", "--help", "-h"}, a) })
		install = (sub == "" && !help) || slices.Contains(jsInstallSubs, sub)
	case pipRE.MatchString(name):
		return sub == "install", false
	case name == "poetry":
		return sub == "install" || sub == "add" || sub == "update" || sub == "sync", false
	case name == "uv":
		if sub == "pip" || sub == "tool" {
			sub2, _ := subcommandOf(rest)
			return sub2 == "install" || sub2 == "sync", false
		}
		return sub == "sync" || sub == "add" || sub == "run", false
	}
	return install, install && (slices.Contains(f, "--ignore-scripts") || slices.Contains(f, "--ignore-scripts=true"))
}

// classifyScript classifies a quoted script: it installs when any of its commands does, safe only when all installs are.
func classifyScript(script string) (install, safe bool) {
	safe = true
	for _, c := range invocations(unquoteOnce(script), "") {
		if i, s := classifyInstall(c); i {
			install, safe = true, safe && s
		}
	}
	return install, install && safe
}

type tokenInstall struct {
	job, step, cmd string
	safe           bool
}

// tokenInstalls lists every dependency install in a job that holds the Railway token at workflow, job or step level.
func tokenInstalls(yaml string) []tokenInstall {
	lines := strings.Split(yaml, "\n")
	hasToken := tokenRefRE.MatchString
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
			for _, cmd := range invocations(unquoteOnce(s.keys["run"]), "") {
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

var onKeyRE = regexp.MustCompile(`^(on|'on'|"on"):`)

// onBlockLines returns the comment-stripped inline value of `on:` and its direct child lines.
func onBlockLines(lines []string) []string {
	var out []string
	in, childIndent := false, -1
	for _, l := range stripHashComments(lines) {
		if strings.TrimSpace(l) == "" {
			continue
		}
		cur := len(l) - len(strings.TrimLeft(l, " "))
		if cur == 0 {
			in, childIndent = false, -1
			if loc := onKeyRE.FindStringIndex(l); loc != nil {
				in = true
				if rest := strings.TrimSpace(l[loc[1]:]); rest != "" {
					out = append(out, rest)
				}
			}
			continue
		}
		if in && (childIndent < 0 || cur == childIndent) {
			childIndent = cur
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
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
