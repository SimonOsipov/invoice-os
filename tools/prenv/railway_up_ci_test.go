// railway_up_ci_test.go runs railway-up-ci.sh under a POSIX shell against a scripted railway CLI.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	upEnvironment = "pr-999"
	upProject     = "9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3"
	// Real deployment ids from Build Logs URLs: A run 36520518964 (auth), B run 36009501058 (auth).
	upIDA = "70f187fe-d55f-449d-9a94-608150e49bc0"
	upIDB = "505713d8-9a1a-4055-8597-01fe79c8fd3d"
)

func upBuildLogs(id string) string {
	return "  Build Logs: https://railway.com/project/" + upProject + "/service/0cf7f5d8-23de-4879-9a2d-fa603ab966b6?id=" + id + "&\n"
}

// Output shapes copied from the named job logs; ids and hosts as in those logs.
const (
	// run 36427096752, job 108947739204 (ops-console).
	upUploadTimeout = "Indexing...\nUploading...\n" +
		"error sending request for url (https://backboard.railway.com/project/9ce6caf1-8c9b-4c77-b40d-3d6f1efa48a3/environment/6bc046b0-d75b-4e86-92a7-67dfd6ae9233/up?serviceId=00df2247-ee57-4722-a663-c7d3a29e2cdd)\n" +
		"\nCaused by:\n    operation timed out\n"
	// run 36137746050 attempt 2, job 108093431374 (auth).
	upUpload520 = "Indexing...\nUploading...\nFailed to upload code with status code 520 <unknown status code>\n"
	// The 520 shape with 503; no measured 503 upload.
	upUpload503 = "Indexing...\nUploading...\nFailed to upload code with status code 503 Service Unavailable\n"
	// Story [upload-rerun]: "Unauthorized. Please login" is the measured auth message; no run cited.
	upUnauthorized = "Indexing...\nUploading...\nFailed to upload code with status code 500 Internal Server Error\nUnauthorized. Please login\n"
	// Unmeasured: story [upload-rerun] matches "not found" as a substring.
	upUnknownService = "Service \"nosuch\" not found\nerror sending request for url (https://backboard.railway.com/graphql/v2)\n"
)

// run 36520518964, job 109253950166 (auth).
func upAccepted(id string) string {
	return "Indexing...\nUploading...\n" + upBuildLogs(id) + "CI mode enabled\nscheduling build on Metal builder \"builder-tspzrb\"\nDeploy complete\n"
}

// run 36125486697, job 108041676501 (reconciliation).
func upLostPoll(id string) string {
	return "Indexing...\nUploading...\n" + upBuildLogs(id) + "CI mode enabled\nreqwest error\n\nCaused by:\n" +
		"    0: error sending request for url (https://backboard.railway.com/graphql/v2)\n    1: operation timed out\n"
}

// run 35249176840, job 105299415097 (invoice).
func upLostPollWebsocket(id string) string {
	return "Indexing...\nUploading...\n" + upBuildLogs(id) + "CI mode enabled\ngot close frame. code: 4408, reason: Connection initialisation timeout\n"
}

// run 35869880518 attempt 2, job 107215253523 (app).
func upDeployFailed(id string) string {
	return "Indexing...\nUploading...\n" + upBuildLogs(id) + "CI mode enabled\nscheduling build on Metal builder \"builder-zjatre\"\nDeploy failed\n"
}

// run 35862073294, job 107189070381 (validation).
func upStreamFailed(id string) string {
	return "Indexing...\nUploading...\n" + upBuildLogs(id) + "CI mode enabled\nFailed to stream build logs: Failed to retrieve build log\n"
}

type upAttempt struct {
	out string
	rc  int
}

// upStub is a temp dir holding the scripted railway answers, the call log and GITHUB_OUTPUT.
type upStub struct{ dir, bin string }

// newUpStub scripts call N from up-N.out/up-N.rc; an unscripted call exits 97.
func newUpStub(t *testing.T, attempts ...upAttempt) upStub {
	t.Helper()
	dir := t.TempDir()
	s := upStub{dir: dir, bin: filepath.Join(dir, "bin")}
	if err := os.Mkdir(s.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, a := range attempts {
		writeFile(t, filepath.Join(dir, "up-"+strconv.Itoa(i+1)+".out"), a.out)
		writeFile(t, filepath.Join(dir, "up-"+strconv.Itoa(i+1)+".rc"), strconv.Itoa(a.rc))
	}
	writeFile(t, s.outputPath(), "")
	railway := `#!/bin/sh
d='` + dir + `'
n=$(( $(cat "$d/n" 2>/dev/null || echo 0) + 1 ))
echo "$n" > "$d/n"
{ printf 'railway'; printf ' [%s]' "$@"; printf '\n'; } >> "$d/calls.log"
if [ -f "$d/up-$n.out" ]; then cat "$d/up-$n.out"; elif [ -f "$d/up.out" ]; then cat "$d/up.out"; else echo "stub: unscripted call $n"; exit 97; fi
if [ -f "$d/up-$n.rc" ]; then exit "$(cat "$d/up-$n.rc")"; elif [ -f "$d/up.rc" ]; then exit "$(cat "$d/up.rc")"; fi
exit 0
`
	sleep := `#!/bin/sh
printf 'sleep %s\n' "$*" >> '` + dir + `/calls.log'
`
	for name, body := range map[string]string{"railway": railway, "sleep": sleep} {
		if err := os.WriteFile(filepath.Join(s.bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func (s upStub) outputPath() string { return filepath.Join(s.dir, "github_output") }

// upShell prefers dash, the closest local stand-in for the Alpine ash in ghcr.io/railwayapp/cli.
func upShell() string {
	if p, err := exec.LookPath("dash"); err == nil {
		return p
	}
	return "sh"
}

// run executes the script for svc and returns combined output and exit code.
func (s upStub) run(t *testing.T, svc string) (string, int) {
	t.Helper()
	root := repoRoot(t)
	cmd := exec.Command(upShell(), filepath.Join(root, "scripts", "ci", "railway-up-ci.sh"), svc)
	cmd.Dir = root
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "RAILWAY_") || strings.HasPrefix(kv, "GITHUB_") || strings.HasPrefix(kv, "PATH=") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env,
		"PATH="+s.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RAILWAY_ENVIRONMENT="+upEnvironment,
		"RAILWAY_PROJECT_ID="+upProject,
		"GITHUB_OUTPUT="+s.outputPath(),
	)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	code := 0
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("harness: running railway-up-ci.sh: %v", err)
		}
		code = exitErr.ExitCode()
	}
	if len(s.railwayCalls(t)) == 0 {
		t.Fatalf("harness: the railway stub never ran; exit %d, output:\n%s", code, buf.String())
	}
	return buf.String(), code
}

// calls returns the call log: one `railway [arg]...` or `sleep <args>` line per call, in order.
func (s upStub) calls(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.dir, "calls.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

func (s upStub) railwayCalls(t *testing.T) []string {
	var out []string
	for _, c := range s.calls(t) {
		if strings.HasPrefix(c, "railway ") {
			out = append(out, c)
		}
	}
	return out
}

func (s upStub) sleeps(t *testing.T) []string {
	var out []string
	for _, c := range s.calls(t) {
		if strings.HasPrefix(c, "sleep ") {
			out = append(out, c)
		}
	}
	return out
}

func (s upStub) output(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(s.outputPath())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func upArgv(svc string) string {
	return "railway [up] [--ci] [--service] [" + svc + "] [--environment] [" + upEnvironment + "] [--project] [" + upProject + "]"
}

func errorCount(out string) int { return strings.Count(errorLines(out), "::error::") }

func TestRailwayUpCI_UploadTimeoutIsRerunOnce(t *testing.T) {
	s := newUpStub(t, upAttempt{upUploadTimeout, 1}, upAttempt{upAccepted(upIDA), 0})
	out, code := s.run(t, "gateway")

	if code != 0 {
		t.Errorf("exit %d, want 0; output:\n%s", code, out)
	}
	want := []string{upArgv("gateway"), "sleep 10", upArgv("gateway")}
	if got := s.calls(t); !slices.Equal(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}
}

func TestRailwayUpCI_Upload5xxIsRerunOnce(t *testing.T) {
	s := newUpStub(t, upAttempt{upUpload520, 1}, upAttempt{upAccepted(upIDA), 0})
	out, code := s.run(t, "invoice")

	if code != 0 {
		t.Errorf("exit %d, want 0; output:\n%s", code, out)
	}
	if got := s.railwayCalls(t); len(got) != 2 {
		t.Errorf("railway calls = %q, want 2", got)
	}
	if got := s.sleeps(t); !slices.Equal(got, []string{"sleep 10"}) {
		t.Errorf("sleeps = %q, want [sleep 10]", got)
	}
}

func TestRailwayUpCI_UploadRerunPrintsOneWarning(t *testing.T) {
	s := newUpStub(t, upAttempt{upUploadTimeout, 1}, upAttempt{upAccepted(upIDA), 0})
	out, _ := s.run(t, "invoice")

	w := warningLines(out)
	if len(w) != 1 {
		t.Fatalf("::warning:: lines = %q, want exactly 1; output:\n%s", w, out)
	}
	for _, needle := range []string{"invoice", "attempt 1/2", "operation timed out"} {
		if !strings.Contains(w[0], needle) {
			t.Errorf("warning %q lacks %q", w[0], needle)
		}
	}
}

func TestRailwayUpCI_SecondUploadFailureNamesBothAttempts(t *testing.T) {
	s := newUpStub(t, upAttempt{upUploadTimeout, 1}, upAttempt{upUpload503, 1})
	out, code := s.run(t, "invoice")

	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if got := s.railwayCalls(t); len(got) != 2 {
		t.Errorf("railway calls = %q, want 2", got)
	}
	if n := errorCount(out); n != 1 {
		t.Fatalf("::error:: lines = %d, want 1; output:\n%s", n, out)
	}
	e := errorLines(out)
	for _, needle := range []string{"failed twice before Railway accepted the deploy", "operation timed out", "status code 503"} {
		if !strings.Contains(e, needle) {
			t.Errorf("error %q lacks %q", e, needle)
		}
	}
}

// guard, passes at HEAD: HEAD never re-runs, so this pins "no re-run after a Build Logs URL" for the new code.
func TestRailwayUpCI_NoRerunAfterBuildLogs(t *testing.T) {
	s := newUpStub(t, upAttempt{upLostPoll(upIDA), 1})
	out, code := s.run(t, "nosuch")

	if code != 1 {
		t.Errorf("exit %d, want 1; output:\n%s", code, out)
	}
	if got := s.railwayCalls(t); len(got) != 1 {
		t.Errorf("railway calls = %q, want 1", got)
	}
	if got := s.sleeps(t); len(got) != 0 {
		t.Errorf("sleeps = %q, want none", got)
	}
}

// guard, passes at HEAD
func TestRailwayUpCI_DeployFailedAfterBuildLogsFails(t *testing.T) {
	s := newUpStub(t, upAttempt{upDeployFailed(upIDA), 1})
	out, code := s.run(t, "app")

	if code != 1 {
		t.Errorf("exit %d, want 1; output:\n%s", code, out)
	}
	if got := s.railwayCalls(t); len(got) != 1 {
		t.Errorf("railway calls = %q, want 1", got)
	}
	if n := errorCount(out); n != 1 {
		t.Errorf("::error:: lines = %d, want 1; output:\n%s", n, out)
	}
}

// requireNotRerun asserts a fatal first attempt: exit 1, one railway call, no sleep, an ::error::.
func requireNotRerun(t *testing.T, s upStub, out string, code int) {
	t.Helper()
	if code != 1 {
		t.Errorf("exit %d, want 1; output:\n%s", code, out)
	}
	if got := s.railwayCalls(t); len(got) != 1 {
		t.Errorf("railway calls = %q, want 1", got)
	}
	if got := s.sleeps(t); len(got) != 0 {
		t.Errorf("sleeps = %q, want none", got)
	}
	if errorCount(out) == 0 {
		t.Errorf("no ::error:: line; output:\n%s", out)
	}
}

// guard, passes at HEAD
func TestRailwayUpCI_UnauthorizedIsNotRerun(t *testing.T) {
	s := newUpStub(t, upAttempt{upUnauthorized, 1}, upAttempt{upAccepted(upIDA), 0})
	out, code := s.run(t, "gateway")
	requireNotRerun(t, s, out, code)
}

// guard, passes at HEAD
func TestRailwayUpCI_UnknownServiceIsNotRerun(t *testing.T) {
	s := newUpStub(t, upAttempt{upUnknownService, 1}, upAttempt{upAccepted(upIDA), 0})
	out, code := s.run(t, "nosuch")
	requireNotRerun(t, s, out, code)
}

// guard, passes at HEAD: AC 4's build-failure markers block the re-run with no Build Logs URL too.
func TestRailwayUpCI_BuildFailureMarkerIsNotRerun(t *testing.T) {
	for _, marker := range []string{"Deploy failed", "Build failed"} {
		t.Run(marker, func(t *testing.T) {
			s := newUpStub(t, upAttempt{"Indexing...\nUploading...\nFailed to upload code with status code 502 Bad Gateway\n" + marker + "\n", 1}, upAttempt{upAccepted(upIDA), 0})
			out, code := s.run(t, "invoice")
			requireNotRerun(t, s, out, code)
		})
	}
}

var matrixServices = regexp.MustCompile(`^\s*service:\s*\[(.*)\]\s*$`)

// upServices lists every service dev-env.yml passes to railway-up-ci.sh: literal args plus the job's matrix.service list.
func upServices(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, j := range workflowJobsOf(readWorkflow(t, "dev-env.yml")) {
		var matrix []string
		for _, l := range j.lines {
			if m := matrixServices.FindStringSubmatch(l); m != nil {
				for _, s := range strings.Split(m[1], ",") {
					matrix = append(matrix, strings.TrimSpace(s))
				}
			}
		}
		for _, st := range j.steps() {
			for _, cmd := range invocations(st.keys["run"], "railway-up-ci.sh") {
				if strings.Contains(cmd, "matrix.service") {
					if len(matrix) == 0 {
						t.Fatalf("job %s runs railway-up-ci.sh on matrix.service but has no service matrix", j.name)
					}
					out = append(out, matrix...)
					continue
				}
				f := strings.Fields(cmd)
				out = append(out, f[len(f)-1])
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// upVerdicts is the Design's verdict map ([up-tolerance]): the later check each warning must name.
var upVerdicts = map[string]string{
	"gateway":         "health-gate",
	"tenancy":         "fleet-gate",
	"portfolio":       "fleet-gate",
	"invoice":         "fleet-gate",
	"validation":      "fleet-gate",
	"submission":      "fleet-gate",
	"dashboard":       "fleet-gate",
	"notifications":   "fleet-gate",
	"reconciliation":  "fleet-gate",
	"docling":         "fleet-gate",
	"auth":            "auth deployment",
	"landing":         "SPA build",
	"app":             "SPA build",
	"ops-console":     "SPA build",
	"support-console": "SPA build",
}

func TestRailwayUpCI_LostPollIsToleratedForEveryService(t *testing.T) {
	services := upServices(t)
	if len(services) != 15 {
		t.Fatalf("dev-env.yml deploys %d services through railway-up-ci.sh, want 15: %q", len(services), services)
	}
	for _, svc := range services {
		t.Run(svc, func(t *testing.T) {
			verdict, ok := upVerdicts[svc]
			if !ok {
				t.Fatalf("dev-env.yml deploys %q through railway-up-ci.sh; the Design verdict map lacks it", svc)
			}
			s := newUpStub(t, upAttempt{upLostPoll(upIDA), 1})
			out, code := s.run(t, svc)

			if code != 0 {
				t.Errorf("exit %d, want 0; output:\n%s", code, out)
			}
			if got := s.railwayCalls(t); len(got) != 1 {
				t.Errorf("railway calls = %q, want 1", got)
			}
			w := warningLines(out)
			if len(w) != 1 {
				t.Fatalf("::warning:: lines = %q, want 1", w)
			}
			for _, needle := range []string{svc, verdict} {
				if !strings.Contains(w[0], needle) {
					t.Errorf("warning %q lacks %q", w[0], needle)
				}
			}
		})
	}
}

func TestRailwayUpCI_LostPollWebsocketTimeoutIsTolerated(t *testing.T) {
	s := newUpStub(t, upAttempt{upLostPollWebsocket(upIDA), 1})
	out, code := s.run(t, "auth")

	if code != 0 {
		t.Errorf("exit %d, want 0; output:\n%s", code, out)
	}
	if got := s.railwayCalls(t); len(got) != 1 {
		t.Errorf("railway calls = %q, want 1", got)
	}
}

func TestRailwayUpCI_LostPollFailsForAServiceWithoutAVerdict(t *testing.T) {
	s := newUpStub(t, upAttempt{upLostPoll(upIDA), 1})
	out, code := s.run(t, "nosuch")

	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	e := errorLines(out)
	for _, needle := range []string{"nosuch", "no later check covers"} {
		if !strings.Contains(e, needle) {
			t.Errorf("error lines %q lack %q", e, needle)
		}
	}
}

func TestRailwayUpCI_WritesTheDeploymentID(t *testing.T) {
	line := "deployment_id_auth=" + upIDA + "\n"
	for _, c := range []struct {
		name, prior string
		attempt     upAttempt
	}{
		{"success", "", upAttempt{upAccepted(upIDA), 0}},
		{"stream failure with a Build Logs URL", "", upAttempt{upStreamFailed(upIDA), 1}},
		{"appends to earlier outputs", "earlier=1\n", upAttempt{upAccepted(upIDA), 0}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newUpStub(t, c.attempt)
			writeFile(t, s.outputPath(), c.prior)
			if out, code := s.run(t, "auth"); code != 0 {
				t.Errorf("exit %d, want 0; output:\n%s", code, out)
			}
			if got, want := s.output(t), c.prior+line; got != want {
				t.Errorf("GITHUB_OUTPUT = %q, want %q", got, want)
			}
		})
	}
}

func TestRailwayUpCI_DeploymentIDNameIsOutputSafe(t *testing.T) {
	s := newUpStub(t, upAttempt{upAccepted(upIDA), 0})
	s.run(t, "ops-console")

	if got, want := s.output(t), "deployment_id_ops_console="+upIDA+"\n"; got != want {
		t.Errorf("GITHUB_OUTPUT = %q, want %q", got, want)
	}
}

func TestRailwayUpCI_DeploymentIDComesFromTheAcceptedAttempt(t *testing.T) {
	s := newUpStub(t, upAttempt{upUploadTimeout, 1}, upAttempt{upAccepted(upIDB), 0})
	s.run(t, "auth")

	if got, want := s.output(t), "deployment_id_auth="+upIDB+"\n"; got != want {
		t.Errorf("GITHUB_OUTPUT = %q, want %q", got, want)
	}
}

func TestRailwayUpCI_LostPollStillWritesTheDeploymentID(t *testing.T) {
	s := newUpStub(t, upAttempt{upLostPoll(upIDA), 1})
	out, code := s.run(t, "auth")

	if code != 0 {
		t.Errorf("exit %d, want 0; output:\n%s", code, out)
	}
	if got, want := s.output(t), "deployment_id_auth="+upIDA+"\n"; got != want {
		t.Errorf("GITHUB_OUTPUT = %q, want %q", got, want)
	}
}

// guard, passes at HEAD: HEAD writes no output at all; pins "no Build Logs URL, no id" for the new code.
func TestRailwayUpCI_NoBuildLogsWritesNoID(t *testing.T) {
	for _, c := range []struct{ name, out, line string }{
		{"fatal", upUnauthorized, "Unauthorized. Please login"},
		{"tolerated stream failure", "Failed to stream build logs: Failed to retrieve build log\n", "Failed to stream build logs"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newUpStub(t, upAttempt{c.out, 1})
			out, _ := s.run(t, "auth")

			if !strings.Contains(out, c.line) {
				t.Errorf("output lacks the scripted %q line:\n%s", c.line, out)
			}
			if got := s.output(t); got != "" {
				t.Errorf("GITHUB_OUTPUT = %q, want empty", got)
			}
		})
	}
}

// guard, passes at HEAD
func TestRailwayUpCI_StreamFailureKeepsItsTolerance(t *testing.T) {
	for _, c := range []struct{ name, out string }{
		{"with a Build Logs URL", upStreamFailed(upIDA)},
		{"without a Build Logs URL", "Failed to stream build logs: Failed to retrieve build log\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newUpStub(t, upAttempt{c.out, 1})
			out, code := s.run(t, "validation")

			if code != 0 {
				t.Errorf("exit %d, want 0; output:\n%s", code, out)
			}
			if got := s.railwayCalls(t); len(got) != 1 {
				t.Errorf("railway calls = %q, want 1", got)
			}
			want := "::warning::railway up --service validation exited 1 on a build-log STREAM failure; the deploy was submitted. health-gate/fleet-gate verify actual health."
			if w := warningLines(out); !slices.Equal(w, []string{want}) {
				t.Errorf("::warning:: lines = %q, want [%q]", w, want)
			}
		})
	}
}

// guard, passes at HEAD
func TestRailwayUpCI_PassesServiceEnvironmentAndProject(t *testing.T) {
	s := newUpStub(t, upAttempt{upAccepted(upIDA), 0})
	if out, code := s.run(t, "invoice"); code != 0 {
		t.Errorf("exit %d, want 0; output:\n%s", code, out)
	}
	if got := s.calls(t); !slices.Equal(got, []string{upArgv("invoice")}) {
		t.Errorf("calls = %q, want [%q]", got, upArgv("invoice"))
	}
}
