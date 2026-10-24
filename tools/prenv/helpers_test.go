package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// workflowJob is one jobs.<name> block of comment-stripped workflow lines.
type workflowJob struct {
	name  string
	lines []string
}

// runBashScript runs a bash snippet without RUNNER_TEMP, so a CI run never appends to the runner's call log.
func runBashScript(t *testing.T, script string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()

	cmd := exec.Command("bash", append([]string{"-c", script, "bash"}, args...)...)
	cmd.Dir = repoRoot(t)

	var filtered []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "RUNNER_TEMP=") {
			continue
		}
		filtered = append(filtered, kv)
	}
	cmd.Env = filtered

	stdout, stderr, err := runToFiles(t, cmd, false)
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running a bash snippet: %v", err)
		}
		exitCode = exitErr.ExitCode()
	}
	return stdout, stderr, exitCode
}

// repoRoot resolves the repository this test is running in. Uses git rather
// than a relative path so the test is correct under a worktree checkout --
// which is where this story is being developed.
func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func (j workflowJob) steps() []workflowStep {
	var raw [][]string
	inSteps := false
	for _, line := range j.lines {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		trimmed := strings.TrimSpace(line)
		if !inSteps {
			inSteps = indent == 4 && trimmed == "steps:"
			continue
		}
		if trimmed == "" {
			continue
		}
		if indent <= 4 {
			break
		}
		if indent == 6 && strings.HasPrefix(trimmed, "- ") {
			raw = append(raw, nil)
		}
		if len(raw) > 0 {
			raw[len(raw)-1] = append(raw[len(raw)-1], line)
		}
	}
	steps := make([]workflowStep, len(raw))
	for i, lines := range raw {
		s := workflowStep{index: i, keys: map[string]string{}, env: map[string]string{}}
		lines[0] = strings.Replace(lines[0], "- ", "  ", 1)
		block := ""
		var run []string
		for _, line := range lines {
			indent := len(line) - len(strings.TrimLeft(line, " "))
			trimmed := strings.TrimSpace(line)
			if indent > 8 {
				switch block {
				case "env":
					if k, v, ok := strings.Cut(trimmed, ":"); ok {
						s.env[strings.TrimSpace(k)] = strings.TrimSpace(v)
					}
				case "run":
					run = append(run, trimmed)
				}
				continue
			}
			k, v, _ := strings.Cut(trimmed, ":")
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			switch {
			case k == "env" && v == "":
				block = "env"
			case k == "run" && (strings.HasPrefix(v, "|") || strings.HasPrefix(v, ">")):
				block = "run"
			default:
				block = ""
				s.keys[k] = v
			}
		}
		if len(run) > 0 {
			s.keys["run"] = strings.Join(run, "\n")
		}
		steps[i] = s
	}
	return steps
}

// workflowStep holds one step's scalar keys (a block run: joined by newlines) and its env map.
type workflowStep struct {
	index int
	keys  map[string]string
	env   map[string]string
}

func workflowJobsOf(yaml string) []workflowJob {
	var jobs []workflowJob
	inJobs := false
	for _, line := range stripHashComments(strings.Split(yaml, "\n")) {
		if strings.HasPrefix(line, "jobs:") {
			inJobs = true
			continue
		}
		if !inJobs {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(line, " ") {
			break
		}
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") && strings.HasSuffix(trimmed, ":") {
			jobs = append(jobs, workflowJob{name: strings.TrimSuffix(trimmed, ":")})
			continue
		}
		if len(jobs) > 0 {
			jobs[len(jobs)-1].lines = append(jobs[len(jobs)-1].lines, line)
		}
	}
	return jobs
}

// jobList returns the inline `key: [a, b]` list of a job, or nil.
func jobList(j workflowJob, key string) []string {
	for _, l := range j.lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, key+": [") && strings.HasSuffix(t, "]") {
			inner := strings.TrimSuffix(strings.TrimPrefix(t, key+": ["), "]")
			var out []string
			for _, f := range strings.Split(inner, ",") {
				out = append(out, strings.TrimSpace(f))
			}
			return out
		}
	}
	return nil
}

// stripHashComments drops shell and YAML `#` comments outside quotes.
func stripHashComments(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = line
		var inS, inD bool
		for j := 0; j < len(line); j++ {
			switch c := line[j]; {
			case c == '\\' && inD:
				j++
			case c == '\'' && !inD:
				inS = !inS
			case c == '"' && !inS:
				inD = !inD
			case c == '#' && !inS && !inD && (j == 0 || line[j-1] == ' ' || line[j-1] == '\t'):
				out[i] = strings.TrimRight(line[:j], " \t")
				j = len(line)
			}
		}
	}
	return out
}

func readWorkflow(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// writeSleepStub puts a sleep on PATH that returns at once and logs its argument to sleep.log.
func writeSleepStub(t *testing.T, dir string) {
	t.Helper()
	stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + filepath.Join(dir, "sleep.log") + "'\n"
	writeShim(t, filepath.Join(dir, "sleep"), stub)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// sleepsIn lists every sleep argument the scripts passed, in order, from dir/sleep.log.
func sleepsIn(t *testing.T, dir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "sleep.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(raw))
}

func nonBlankLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func errorLines(out string) string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "::error::") {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}
