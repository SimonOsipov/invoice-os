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
type upStub struct {
	dir, bin string
	noOutput bool // leave GITHUB_OUTPUT unset
}

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
	)
	if !s.noOutput {
		cmd.Env = append(cmd.Env, "GITHUB_OUTPUT="+s.outputPath())
	}
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

// guard, passes at HEAD
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
	const lostPollTail = "reqwest error\n\nCaused by:\n    0: error sending request for url (https://backboard.railway.com/graphql/v2)\n    1: operation timed out\n"
	for _, c := range []struct{ name, out string }{
		{"Deploy failed", upDeployFailed(upIDA)},
		{"Build failed", strings.Replace(upDeployFailed(upIDA), "Deploy failed", "Build failed", 1)},
		{"Deploy failed then a lost poll", upDeployFailed(upIDA) + lostPollTail},
		{"Build failed then a lost poll", strings.Replace(upDeployFailed(upIDA), "Deploy failed", "Build failed", 1) + lostPollTail},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newUpStub(t, upAttempt{c.out, 1})
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
			if w := warningLines(out); len(w) != 0 {
				t.Errorf("::warning:: lines = %q, want none", w)
			}
			if got := s.output(t); got != "" {
				t.Errorf("GITHUB_OUTPUT = %q, want empty: a failed build publishes no deployment id", got)
			}
		})
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
	if strings.Contains(out, "failed twice") {
		t.Errorf("output names 'failed twice' for a first attempt; output:\n%s", out)
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
	"library":         "SPA build",
}

func TestRailwayUpCI_LostPollIsToleratedForEveryService(t *testing.T) {
	services := upServices(t)
	if len(services) != 16 {
		t.Fatalf("dev-env.yml deploys %d services through railway-up-ci.sh, want 16: %q", len(services), services)
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
	// Lookalikes of mapped names: the verdict map matches whole names only.
	for _, svc := range []string{"nosuch", "gateway2", "Auth", "app-web", "invoice-x"} {
		t.Run(svc, func(t *testing.T) {
			s := newUpStub(t, upAttempt{upLostPoll(upIDA), 1})
			out, code := s.run(t, svc)

			if code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
			e := errorLines(out)
			for _, needle := range []string{svc, "no later check covers"} {
				if !strings.Contains(e, needle) {
					t.Errorf("error lines %q lack %q", e, needle)
				}
			}
			if w := warningLines(out); len(w) != 0 {
				t.Errorf("::warning:: lines = %q, want none", w)
			}
			if got := s.output(t); got != "" {
				t.Errorf("GITHUB_OUTPUT = %q, want empty: an untolerated attempt publishes no id", got)
			}
		})
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

// guard, passes at HEAD
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
		{"with a transport signature and no Build Logs URL", "Uploading...\nFailed to stream build logs: error sending request for url (https://backboard.railway.com/graphql/v2)\n\nCaused by:\n    operation timed out\n"},
		{"with a Build Logs URL and a transport signature", upStreamFailed(upIDA) + "operation timed out\n"},
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

// upSignatures are the transport signatures, each alone, as the story's Design lists them.
var upSignatures = []struct{ name, line string }{
	{"operation timed out", "    operation timed out"},
	{"error sending request for url", "error sending request for url (https://backboard.railway.com/graphql/v2)"},
	{"Connection initialisation timeout", "got close frame. code: 4408, reason: Connection initialisation timeout"},
	{"status code 500", "Failed to upload code with status code 500 Internal Server Error"},
	{"status code 599", "Failed to upload code with status code 599 Network Connect Timeout"},
}

func TestRailwayUpCI_EachSignatureAloneIsRerunBeforeAcceptance(t *testing.T) {
	for _, sig := range upSignatures {
		t.Run(sig.name, func(t *testing.T) {
			s := newUpStub(t, upAttempt{"Indexing...\nUploading...\n" + sig.line + "\n", 1}, upAttempt{upAccepted(upIDA), 0})
			out, code := s.run(t, "invoice")

			if code != 0 {
				t.Errorf("exit %d, want 0; output:\n%s", code, out)
			}
			if got := s.railwayCalls(t); len(got) != 2 {
				t.Errorf("railway calls = %q, want 2", got)
			}
			w := warningLines(out)
			if len(w) != 1 || !strings.Contains(w[0], "(attempt 1/2): "+strings.TrimSpace(sig.line)+"; re-running once.") {
				t.Errorf("::warning:: lines = %q, want one ending in the trimmed signature line %q", w, strings.TrimSpace(sig.line))
			}
		})
	}
}

func TestRailwayUpCI_EachSignatureAloneIsATolerableLostPoll(t *testing.T) {
	for _, sig := range upSignatures {
		t.Run(sig.name, func(t *testing.T) {
			s := newUpStub(t, upAttempt{"Indexing...\nUploading...\n" + upBuildLogs(upIDA) + "CI mode enabled\n" + sig.line + "\n", 1})
			out, code := s.run(t, "auth")

			if code != 0 {
				t.Errorf("exit %d, want 0; output:\n%s", code, out)
			}
			if got := s.railwayCalls(t); len(got) != 1 {
				t.Errorf("railway calls = %q, want 1", got)
			}
			if w := warningLines(out); len(w) != 1 || !strings.Contains(w[0], strings.TrimSpace(sig.line)) {
				t.Errorf("::warning:: lines = %q, want one naming %q", w, strings.TrimSpace(sig.line))
			}
		})
	}
}

// Near misses of a signature are no transport failure: one call, an ::error::, no warning.
func TestRailwayUpCI_NonTransportFailureIsFatalWithoutARerun(t *testing.T) {
	for _, c := range []struct{ name, line string }{
		{"status code 499", "Failed to upload code with status code 499"},
		{"status code 404", "Failed to upload code with status code 404 Not Found"},
		{"status code 600", "Failed to upload code with status code 600"},
		{"status code 50", "Failed to upload code with status code 50"},
		{"unrelated text", "something else went wrong"},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, prefix := range []string{"", upBuildLogs(upIDA)} {
				s := newUpStub(t, upAttempt{"Indexing...\nUploading...\n" + prefix + c.line + "\n", 1}, upAttempt{upAccepted(upIDA), 0})
				out, code := s.run(t, "auth")
				requireNotRerun(t, s, out, code)
				if w := warningLines(out); len(w) != 0 {
					t.Errorf("::warning:: lines = %q, want none", w)
				}
				if got := s.output(t); got != "" {
					t.Errorf("GITHUB_OUTPUT = %q, want empty", got)
				}
			}
		})
	}
}

func TestRailwayUpCI_SignatureLineIsTheLastMatchTrimmed(t *testing.T) {
	t.Run("upload warning", func(t *testing.T) {
		s := newUpStub(t, upAttempt{upUploadTimeout, 1}, upAttempt{upAccepted(upIDA), 0})
		out, _ := s.run(t, "invoice")
		if w := warningLines(out); len(w) != 1 || !strings.Contains(w[0], "(attempt 1/2): operation timed out; re-running once.") {
			t.Errorf("::warning:: lines = %q, want the last matching line, trimmed", w)
		}
	})
	t.Run("lost poll warning", func(t *testing.T) {
		s := newUpStub(t, upAttempt{upLostPoll(upIDA), 1})
		out, _ := s.run(t, "invoice")
		if w := warningLines(out); len(w) != 1 || !strings.Contains(w[0], "status poll: 1: operation timed out. fleet-gate decides.") {
			t.Errorf("::warning:: lines = %q, want the last matching line, trimmed, before the verdict", w)
		}
	})
	t.Run("both attempts of the second-failure error", func(t *testing.T) {
		s := newUpStub(t, upAttempt{upUploadTimeout, 1}, upAttempt{upUpload503, 1})
		out, _ := s.run(t, "invoice")
		want := "Attempt 1: operation timed out. Attempt 2: Failed to upload code with status code 503 Service Unavailable."
		if e := errorLines(out); !strings.Contains(e, want) {
			t.Errorf("error %q lacks %q", e, want)
		}
	})
}

// The second attempt is classified on its own output.
func TestRailwayUpCI_SecondAttemptIsClassifiedOnItsOwn(t *testing.T) {
	first := upAttempt{upUploadTimeout, 1}
	for _, c := range []struct {
		name     string
		second   upAttempt
		svc      string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{"build failure after acceptance", upAttempt{upDeployFailed(upIDB), 1}, "auth", 1, "", "the build failed"},
		{"stream failure with a Build Logs URL", upAttempt{upStreamFailed(upIDB), 1}, "auth", 0, "deployment_id_auth=" + upIDB + "\n", ""},
		{"lost poll", upAttempt{upLostPoll(upIDB), 1}, "auth", 0, "deployment_id_auth=" + upIDB + "\n", ""},
		{"lost poll of a service outside the map", upAttempt{upLostPoll(upIDB), 1}, "nosuch", 1, "", "no later check covers"},
		{"no transport signature", upAttempt{"Uploading...\nUnauthorized. Please login\n", 1}, "auth", 1, "", "for a non-streaming reason"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newUpStub(t, first, c.second)
			out, code := s.run(t, c.svc)

			if code != c.wantCode {
				t.Errorf("exit %d, want %d; output:\n%s", code, c.wantCode, out)
			}
			if got := s.railwayCalls(t); len(got) != 2 {
				t.Errorf("railway calls = %q, want 2", got)
			}
			if got := s.output(t); got != c.wantOut {
				t.Errorf("GITHUB_OUTPUT = %q, want %q", got, c.wantOut)
			}
			if c.wantErr != "" && !strings.Contains(errorLines(out), c.wantErr) {
				t.Errorf("error lines %q lack %q", errorLines(out), c.wantErr)
			}
			if strings.Contains(out, "failed twice") {
				t.Errorf("output names 'failed twice' although attempt 2 was accepted or failed differently:\n%s", out)
			}
		})
	}
}

func TestRailwayUpCI_RerunThenLostPollIsTolerated(t *testing.T) {
	s := newUpStub(t, upAttempt{upUploadTimeout, 1}, upAttempt{upLostPoll(upIDB), 1})
	out, code := s.run(t, "gateway")

	if code != 0 {
		t.Errorf("exit %d, want 0; output:\n%s", code, out)
	}
	if got := s.railwayCalls(t); len(got) != 2 {
		t.Errorf("railway calls = %q, want 2", got)
	}
	w := warningLines(out)
	if len(w) != 2 || !strings.Contains(w[0], "attempt 1/2") || !strings.Contains(w[1], "lost its status poll") || !strings.Contains(w[1], "health-gate") {
		t.Errorf("::warning:: lines = %q, want the re-run warning then the lost-poll warning naming health-gate", w)
	}
}

func TestRailwayUpCI_DeploymentIDParsing(t *testing.T) {
	const base = "https://railway.com/project/" + upProject + "/service/0cf7f5d8-23de-4879-9a2d-fa603ab966b6"
	for _, c := range []struct{ name, out, want string }{
		{"extra parameter after the id", "  Build Logs: " + base + "?id=" + upIDA + "&extra=1\n", upIDA},
		{"extra parameter before the id", "  Build Logs: " + base + "?extra=1&id=" + upIDA + "\n", upIDA},
		{"id with no trailing ampersand", "  Build Logs: " + base + "?id=" + upIDA + "\n", upIDA},
		{"id that is not a UUID", "  Build Logs: " + base + "?id=abc_123\n", "abc_123"},
		{"the last of two URLs", upBuildLogs(upIDA) + upBuildLogs(upIDB), upIDB},
		{"an id on a later line that is not a Build Logs line", upBuildLogs(upIDA) + "see https://example.com/?id=nope\n", upIDA},
		{"serviceId is not id", "  Build Logs: " + base + "?serviceId=" + upIDB + "\n", ""},
		{"an empty id", "  Build Logs: " + base + "?id=&\n", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newUpStub(t, upAttempt{"Uploading...\n" + c.out + "Deploy complete\n", 0})
			if out, code := s.run(t, "auth"); code != 0 {
				t.Errorf("exit %d, want 0; output:\n%s", code, out)
			}
			want := ""
			if c.want != "" {
				want = "deployment_id_auth=" + c.want + "\n"
			}
			if got := s.output(t); got != want {
				t.Errorf("GITHUB_OUTPUT = %q, want %q", got, want)
			}
		})
	}
}

func TestRailwayUpCI_WorksWithoutGithubOutput(t *testing.T) {
	for _, c := range []struct {
		name string
		at   upAttempt
		want string
	}{
		{"success", upAttempt{upAccepted(upIDA), 0}, upAccepted(upIDA)},
		{"lost poll", upAttempt{upLostPoll(upIDA), 1}, upLostPoll(upIDA)},
		{"stream failure", upAttempt{upStreamFailed(upIDA), 1}, upStreamFailed(upIDA)},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newUpStub(t, c.at)
			s.noOutput = true
			out, code := s.run(t, "auth")

			if code != 0 {
				t.Errorf("exit %d, want 0; output:\n%s", code, out)
			}
			if !strings.HasPrefix(out, c.want) {
				t.Errorf("output starts with %q, want the CLI output %q then only ::warning:: lines", out, c.want)
			}
			if rest := strings.TrimPrefix(out, c.want); strings.Contains(rest, "::error::") || strings.Contains(rest, "parameter not set") || strings.Contains(rest, "cannot create") {
				t.Errorf("unexpected output after the CLI output:\n%s", rest)
			}
			if got := s.output(t); got != "" {
				t.Errorf("GITHUB_OUTPUT file = %q, want untouched", got)
			}
		})
	}
}

// The re-run is for pre-acceptance failures only; each blocker alone, each with a transport signature.
func TestRailwayUpCI_EachRerunBlockerAloneBlocksTheRerun(t *testing.T) {
	for _, marker := range []string{"Deploy failed", "Build failed", "Unauthorized", "not found", "Failed to stream build logs"} {
		t.Run(marker, func(t *testing.T) {
			s := newUpStub(t, upAttempt{"Uploading...\nFailed to upload code with status code 502 Bad Gateway\n" + marker + "\n", 1}, upAttempt{upAccepted(upIDA), 0})
			s.run(t, "invoice")

			if got := s.railwayCalls(t); len(got) != 1 {
				t.Errorf("railway calls = %q, want 1", got)
			}
			if got := s.sleeps(t); len(got) != 0 {
				t.Errorf("sleeps = %q, want none", got)
			}
		})
	}
}
