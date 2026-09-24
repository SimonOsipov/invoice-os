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

// forkAuthArgFaults reports each argument or token of the two fork auth steps that
// does not come from the output it must come from.
func forkAuthArgFaults(steps [][]string) []string {
	const envID = "${{ steps.resolve.outputs.environment_id }}"
	const token = "${{ secrets.RAILWAY_API_TOKEN }}"
	want := map[string][]string{
		"set-fork-auth":      {envID},
		"set-fork-auth-site": {envID, "${{ steps.urls.outputs.landing_url }}"},
	}
	var faults []string
	for _, sub := range []string{"set-fork-auth", "set-fork-auth-site"} {
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
			if got := stepEnv(s)["RAILWAY_API_TOKEN"]; got != token {
				faults = append(faults, sub+" RAILWAY_API_TOKEN is "+got+", want "+token)
			}
		}
		if found != 1 {
			faults = append(faults, sub+" is called by "+strconv.Itoa(found)+" step(s), want 1")
		}
	}
	return faults
}

func TestForkAuthStepsPassTheResolvedEnvironmentAndLandingURL(t *testing.T) {
	const good = "jobs:\n  prepare-env:\n    steps:\n" +
		"      - name: a\n        env:\n          RAILWAY_API_TOKEN: ${{ secrets.RAILWAY_API_TOKEN }}\n" +
		"          ENV_ID: ${{ steps.resolve.outputs.environment_id }}\n" +
		"        run: bash scripts/ci/railway-env.sh set-fork-auth \"$ENV_ID\"\n" +
		"      - name: b\n        env:\n          RAILWAY_API_TOKEN: ${{ secrets.RAILWAY_API_TOKEN }}\n" +
		"          ENV_ID: ${{ steps.resolve.outputs.environment_id }}\n" +
		"          LANDING_URL: ${{ steps.urls.outputs.landing_url }}\n" +
		"        run: bash scripts/ci/railway-env.sh set-fork-auth-site \"$ENV_ID\" \"$LANDING_URL\"\n"
	parse := func(src string) [][]string { return jobSteps(jobBlock(yamlCode(src), "prepare-env")) }
	if f := forkAuthArgFaults(parse(good)); len(f) != 0 {
		t.Fatalf("the good fixture reports %v; the scan is broken", f)
	}
	for _, c := range []struct{ name, src, want string }{
		{"site gets the environment twice", strings.Replace(good, `"$ENV_ID" "$LANDING_URL"`, `"$ENV_ID" "$ENV_ID"`, 1), "set-fork-auth-site arguments"},
		{"site gets no URL", strings.Replace(good, ` "$LANDING_URL"`, "", 1), "set-fork-auth-site arguments"},
		{"site env reads the app URL", strings.Replace(good, "outputs.landing_url", "outputs.app_url", 1), "set-fork-auth-site arguments"},
		{"auth gets a landing URL", strings.Replace(good, `set-fork-auth "$ENV_ID"`, `set-fork-auth "$ENV_ID" "$LANDING_URL"`, 1), "set-fork-auth arguments"},
		{"auth gets no id", strings.Replace(good, `set-fork-auth "$ENV_ID"`, `set-fork-auth`, 1), "set-fork-auth arguments"},
		{"project token", strings.Replace(good, "secrets.RAILWAY_API_TOKEN", "secrets.RAILWAY_API_DEV_TOKEN", 1), "RAILWAY_API_TOKEN is"},
	} {
		if c.src == good {
			t.Fatalf("fixture %q: the edit did not apply", c.name)
		}
		if f := forkAuthArgFaults(parse(c.src)); !slices.ContainsFunc(f, func(s string) bool { return strings.Contains(s, c.want) }) {
			t.Errorf("fixture %q: faults %v, want one naming %q", c.name, f, c.want)
		}
	}

	for _, f := range forkAuthArgFaults(prepareEnvSteps(t)) {
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
		{"true", "1", "set-fork-auth did not write AUTH_ADDITIONAL_ISSUERS"},
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
