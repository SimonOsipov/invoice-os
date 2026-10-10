package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var stepEnvLineRE = regexp.MustCompile(`^          ([A-Za-z_][A-Za-z0-9_]*):\s*(.*)$`)

// stepEnv returns a step's own env: entries.
func stepEnv(step []string) map[string]string {
	env := map[string]string{}
	in := false
	for _, l := range step {
		switch {
		case strings.TrimSpace(l) == "env:" && strings.HasPrefix(l, "        env:"):
			in = true
		case in:
			m := stepEnvLineRE.FindStringSubmatch(l)
			if m == nil {
				in = false
				continue
			}
			env[m[1]] = strings.TrimSpace(m[2])
		}
	}
	return env
}

// subcommandArgs returns the arguments after `railway-env.sh <sub>` in step,
// each resolved one level through the step's env:.
func subcommandArgs(step []string, sub string) ([]string, bool) {
	env := stepEnv(step)
	for _, line := range strings.Split(runText(step), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if !strings.HasSuffix(f, "railway-env.sh") || i+1 >= len(fields) || fields[i+1] != sub {
				continue
			}
			var args []string
			for _, a := range fields[i+2:] {
				a = strings.Trim(a, `"'`)
				if m := shellVarRE.FindStringSubmatch(a); m != nil {
					if v, ok := env[m[1]+m[2]]; ok {
						a = v
					}
				}
				args = append(args, a)
			}
			return args, true
		}
	}
	return nil, false
}

// forkPassArgFaults reports each argument, env entry or token of the two fork pass
// steps that does not come from the output it must come from.
func forkPassArgFaults(steps [][]string) []string {
	const envID = "${{ steps.resolve.outputs.environment_id }}"
	const token = "${{ secrets.RAILWAY_API_TOKEN }}"
	urlEnv := []struct{ env, key string }{
		{"GATEWAY_URL", "gateway_url"},
		{"APP_URL", "app_url"},
		{"LANDING_URL", "landing_url"},
		{"OPS_CONSOLE_URL", "ops_console_url"},
		{"SUPPORT_CONSOLE_URL", "support_console_url"},
		{"LIBRARY_URL", "library_url"},
	}
	wantAfter := []string{envID}
	for _, u := range urlEnv {
		wantAfter = append(wantAfter, "${{ steps.urls.outputs."+u.key+" }}")
	}
	want := map[string][]string{
		"fork-vars-before-urls": {envID},
		"fork-vars-after-urls":  wantAfter,
	}
	var faults []string
	for _, sub := range []string{"fork-vars-before-urls", "fork-vars-after-urls"} {
		found := 0
		for _, s := range steps {
			args, ok := subcommandArgs(s, sub)
			if !ok {
				continue
			}
			found++
			if !slices.Equal(args, want[sub]) {
				faults = append(faults, sub+" arguments resolve to "+strings.Join(args, " ")+", want "+strings.Join(want[sub], " "))
			}
			env := stepEnv(s)
			if got := env["RAILWAY_API_TOKEN"]; got != token {
				faults = append(faults, sub+" RAILWAY_API_TOKEN is "+got+", want "+token)
			}
			if got := env["ENV_ID"]; got != envID {
				faults = append(faults, sub+" ENV_ID is "+got+", want "+envID)
			}
			if sub == "fork-vars-after-urls" {
				for _, u := range urlEnv {
					if got, w := env[u.env], "${{ steps.urls.outputs."+u.key+" }}"; got != w {
						faults = append(faults, sub+" env "+u.env+" is "+got+", want "+w)
					}
				}
			}
		}
		if found != 1 {
			faults = append(faults, sub+" is called by "+strconv.Itoa(found)+" step(s), want 1")
		}
	}
	return faults
}

func TestForkPassStepsPassTheResolvedEnvironmentAndTheURLs(t *testing.T) {
	steps := prepareEnvSteps(t)
	if len(steps) < 10 {
		t.Fatalf("control: prepare-env parsed to %d step(s), want at least 10; the scan is broken", len(steps))
	}
	if len(stepsRunning(steps, forkVarsAfterRE)) == 0 {
		t.Fatal("control: no prepare-env step runs fork-vars-after-urls; there are no URL arguments to read")
	}
	for _, f := range forkPassArgFaults(steps) {
		t.Errorf(".github/workflows/dev-env.yml prepare-env: %s", f)
	}
}

// The failure names the write that would have set the wrong count, not only the count.
func TestAuthIssuersFailureNamesItsLikelyCause(t *testing.T) {
	run := healthGateWaitRun(t)
	for _, c := range []struct {
		isPR          string
		issuers, want string
	}{
		{"true", "1", "fork-vars-before-urls did not write AUTH_ADDITIONAL_ISSUERS"},
		{"false", "2", "AUTH_ADDITIONAL_ISSUERS is set there"},
		{"true", "", "predates the field"},
		{"false", "", "predates the field"},
	} {
		code, out, _ := runWithGateShim(t, run, "healthz", healthzBody(c.isPR == "true", c.issuers), "IS_PR="+c.isPR)
		errs := errorLines(out)
		if code == 0 || len(errs) == 0 {
			t.Errorf("IS_PR=%s auth_issuers=%q: health-gate exits %d with no ::error:: line", c.isPR, c.issuers, code)
			continue
		}
		// The cause text itself mentions '1' and '2', so the value must appear in its quoted slot.
		seen := "auth_issuers='" + c.issuers + "'"
		if c.issuers == "" {
			seen = "auth_issuers='none'"
		}
		if !slices.ContainsFunc(errs, func(l string) bool { return strings.Contains(l, seen) && strings.Contains(l, c.want) }) {
			t.Errorf("IS_PR=%s auth_issuers=%q: no ::error:: line names %q and %q: %q", c.isPR, c.issuers, seen, c.want, errs)
		}
	}
}

// The demo_purge failure points an operator at the step that runs; set-fork-environment no longer does.
func TestPurgeFailureNamesTheForkPassThatSetsEnvironment(t *testing.T) {
	run := healthGateWaitRun(t)
	body := healthzBody(true, "2")
	body["demo_purge"] = "false"
	code, out, _ := runWithGateShim(t, run, "healthz", body, "IS_PR=true")
	errs := errorLines(out)
	if code == 0 || len(errs) == 0 {
		t.Fatalf("a fork reporting demo_purge=false: health-gate exits %d with no ::error:: line", code)
	}
	joined := strings.Join(errs, "\n")
	if !strings.Contains(joined, "demo_purge='false'") || !strings.Contains(joined, "fork-vars-after-urls set ENVIRONMENT=development") {
		t.Errorf("no ::error:: line names demo_purge='false' and the fork-vars-after-urls write: %q", errs)
	}
	for _, removed := range []string{"set-fork-environment", "set-fork-auth", "set-sentry-off", "set-ai-fake", "reconcile-urls"} {
		if strings.Contains(joined, removed) {
			t.Errorf("an ::error:: line sends the operator to %s, a step prepare-env no longer runs: %q", removed, errs)
		}
	}
}

// fleetDownShim serves $SHIM_DIR/fleet.json with the status code in $SHIM_DIR/code.
const fleetDownShim = `#!/bin/sh
out=""; w=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift ;;
    -w) w="$2"; shift ;;
  esac
  shift
done
if [ -n "$out" ]; then cp "$SHIM_DIR/fleet.json" "$out"; else cat "$SHIM_DIR/fleet.json"; fi
if [ -n "$w" ]; then cat "$SHIM_DIR/code"; fi
exit 0
`

// Between merge and the production auth write, auth is down: fleet-gate must fail naming it.
func TestFleetGateNamesAuthWhenItIsDown(t *testing.T) {
	run := fleetGateStaleRun(t)
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Fatalf("jq is not on PATH: %v", err)
	}
	for _, c := range []struct {
		name     string
		code     string
		down     fleetEntry
		wantExit int
		names    string
	}{
		{"control: all up", "200", fleetEntry{"auth", "up", ""}, 0, ""},
		{"auth down", "503", fleetEntry{"auth", "down", ""}, 1, "DOWN: auth"},
		{"tenancy down beside auth up", "503", fleetEntry{"tenancy", "down", "deadbeef"}, 1, "DOWN: tenancy"},
	} {
		dir := t.TempDir()
		body := fleetBody(fleetEntry{"auth", "up", ""}, c.down)
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		for f, b := range map[string][]byte{"curl": []byte(fleetDownShim), "sleep": []byte("#!/bin/sh\n"), "fleet.json": raw, "code": []byte(c.code)} {
			if err := os.WriteFile(filepath.Join(dir, f), b, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		block := strings.ReplaceAll(run, "/tmp/", dir+"/")
		code, out := runGate(t, block, "PATH="+dir+":"+filepath.Dir(jq)+":/usr/bin:/bin", "SHIM_DIR="+dir, "GATEWAY_URL="+probeGatewayURL)
		if code != c.wantExit {
			t.Errorf("%s: fleet-gate exits %d, want %d (output %q)", c.name, code, c.wantExit, out)
			continue
		}
		if c.names != "" && !strings.Contains(out, c.names) {
			t.Errorf("%s: output does not name %q: %q", c.name, c.names, out)
		}
	}
}
