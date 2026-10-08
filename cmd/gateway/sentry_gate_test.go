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

// sentryReadRE matches a jq read of the sentry field; `.sentry.io` is a host, not a read.
var sentryReadRE = regexp.MustCompile(`(?m)\.sentry([^.\w-]|$)`)

// exemptClauseRE is the success line's parenthetical naming what the step skips.
var exemptClauseRE = regexp.MustCompile(`\([^)]*exempt[^)]*\)`)

// pushDirectionRE is how the success line names the push direction.
var pushDirectionRE = regexp.MustCompile(`\bon( or |\|)off\b`)

// sentryCheckedNames are the roll-up entries the step checks: every name except auth.
var sentryCheckedNames = []string{"gateway", "tenancy", "portfolio", "invoice", "validation", "submission", "dashboard", "notifications", "reconciliation", "docling"}

// sentryFleetEntries is a deployed-shape roll-up: each checked entry on deadbeef reporting sentry,
// auth as its custom-path [name status] entry.
func sentryFleetEntries(sentry string) []map[string]any {
	var s []map[string]any
	for _, n := range sentryCheckedNames {
		s = append(s, map[string]any{"name": n, "status": "up", "build": "deadbeef", "sentry": sentry})
	}
	return append(s, map[string]any{"name": "auth", "status": "up"})
}

// sentryFleetAny is sentryFleetEntries as []any, so a row can append a non-object entry.
func sentryFleetAny(sentry string) []any {
	var s []any
	for _, e := range sentryFleetEntries(sentry) {
		s = append(s, e)
	}
	return s
}

func sentryEntry(t *testing.T, s []map[string]any, name string) map[string]any {
	t.Helper()
	i := slices.IndexFunc(s, func(e map[string]any) bool { return e["name"] == name })
	if i < 0 {
		t.Fatalf("fixture has no %q entry", name)
	}
	return s[i]
}

// sentryGateRun returns the run text of the fleet-gate step that reads .sentry, or "".
func sentryGateRun(t *testing.T) string {
	t.Helper()
	steps := jobSteps(jobBlock(devEnvCode(t), "fleet-gate"))
	if len(steps) == 0 {
		t.Fatal("dev-env.yml fleet-gate parsed to no steps; the scan is broken")
	}
	for _, s := range steps {
		if r := runText(s); sentryReadRE.MatchString(r) {
			return r
		}
	}
	return ""
}

type sentryRow struct {
	name     string
	isPR     bool
	body     func(t *testing.T) any
	wantExit int
	named    []string // each must appear in an ::error:: line
	notNamed []string // none may appear in an ::error:: line
}

func withSentry(sentry string, edit func(t *testing.T, s []map[string]any) []map[string]any) func(t *testing.T) any {
	return func(t *testing.T) any {
		s := sentryFleetEntries(sentry)
		if edit != nil {
			s = edit(t, s)
		}
		return map[string]any{"services": s}
	}
}

func setField(name, key string, v any) func(t *testing.T, s []map[string]any) []map[string]any {
	return func(t *testing.T, s []map[string]any) []map[string]any {
		e := sentryEntry(t, s, name)
		if v == nil {
			delete(e, key)
		} else {
			e[key] = v
		}
		return s
	}
}

func TestSentryGateIsDirectional(t *testing.T) {
	run := sentryGateRun(t)
	if run == "" {
		t.Errorf("dev-env.yml fleet-gate has no step reading .sentry; running an empty step below")
	}

	rows := []sentryRow{
		{name: "pr_all_off", isPR: true, body: withSentry("off", nil), wantExit: 0},
		{name: "pr_invoice_on", isPR: true, body: withSentry("off", setField("invoice", "sentry", "on")), wantExit: 1,
			named: []string{"invoice=on@deadbeef", "fork-vars-after-urls"}, notNamed: []string{"gateway=", "tenancy="}},
		{name: "pr_reconciliation_on", isPR: true, body: withSentry("off", setField("reconciliation", "sentry", "on")), wantExit: 1,
			named: []string{"reconciliation=on@deadbeef", "fork-vars-after-urls"}, notNamed: []string{"gateway=", "invoice="}},
		{name: "pr_tenancy_missing", isPR: true, body: withSentry("off", setField("tenancy", "sentry", nil)), wantExit: 1,
			named: []string{"tenancy=none@deadbeef"}, notNamed: []string{"portfolio="}},
		{name: "push_tenancy_missing", isPR: false, body: withSentry("on", setField("tenancy", "sentry", nil)), wantExit: 1,
			named: []string{"tenancy=none@deadbeef"}, notNamed: []string{"portfolio="}},
		{name: "pr_OFF", isPR: true, body: withSentry("off", setField("validation", "sentry", "OFF")), wantExit: 1,
			named: []string{"validation=OFF"}},
		{name: "pr_off_space", isPR: true, body: withSentry("off", setField("submission", "sentry", "off ")), wantExit: 1,
			named: []string{"submission="}},
		{name: "push_ON", isPR: false, body: withSentry("off", setField("notifications", "sentry", "ON")), wantExit: 1,
			named: []string{"notifications=ON"}},
		{name: "push_all_on", isPR: false, body: withSentry("on", nil), wantExit: 0},
		{name: "push_mixed", isPR: false, body: withSentry("on", func(t *testing.T, s []map[string]any) []map[string]any {
			for i, n := range sentryCheckedNames {
				if i%2 == 1 {
					sentryEntry(t, s, n)["sentry"] = "off"
				}
			}
			return s
		}), wantExit: 0},
		{name: "stale_build", isPR: true, body: withSentry("off", setField("dashboard", "build", "0ld")), wantExit: 1,
			named: []string{"dashboard=off@0ld"}, notNamed: []string{"gateway="}},
		{name: "no_gateway", isPR: true, body: withSentry("off", func(t *testing.T, s []map[string]any) []map[string]any {
			sentryEntry(t, s, "gateway")
			return slices.DeleteFunc(s, func(e map[string]any) bool { return e["name"] == "gateway" })
		}), wantExit: 1, named: []string{"gateway"}},
		{name: "empty", isPR: true, body: func(*testing.T) any { return map[string]any{"services": []any{}} }, wantExit: 1},
		// runWithGateShim marshals this to the JSON string "not json": valid JSON, wrong shape.
		{name: "wrong_shape", isPR: true, body: func(*testing.T) any { return "not json" }, wantExit: 1},
		{name: "authz_is_not_auth", isPR: true, body: withSentry("off", func(t *testing.T, s []map[string]any) []map[string]any {
			return append(s, map[string]any{"name": "authz", "status": "up", "build": "deadbeef"})
		}), wantExit: 1, named: []string{"authz=none@deadbeef"}, notNamed: []string{"auth=none"}},
		{name: "pr_docling_on", isPR: true, body: withSentry("off", setField("docling", "sentry", "on")), wantExit: 1,
			named: []string{"docling=on@deadbeef", "fork-vars-after-urls"}, notNamed: []string{"gateway="}},
		{name: "pr_docling_missing", isPR: true, body: withSentry("off", setField("docling", "sentry", nil)), wantExit: 1,
			named: []string{"docling=none@deadbeef"}, notNamed: []string{"gateway="}},
		{name: "push_docling_missing", isPR: false, body: withSentry("on", setField("docling", "sentry", nil)), wantExit: 1,
			named: []string{"docling=none@deadbeef"}, notNamed: []string{"gateway="}},

		// QA Mode B adversarial rows.
		{name: "pr_docling_empty_sentry", isPR: true, body: withSentry("off", setField("docling", "sentry", "")), wantExit: 1,
			named: []string{"docling=@deadbeef", "fork-vars-after-urls"}, notNamed: []string{"gateway="}},
		{name: "push_docling_empty_sentry", isPR: false, body: withSentry("on", setField("docling", "sentry", "")), wantExit: 1,
			named: []string{"docling=@deadbeef"}, notNamed: []string{"gateway="}},
		{name: "pr_docling_stale_build", isPR: true, body: withSentry("off", setField("docling", "build", "0ld")), wantExit: 1,
			named: []string{"docling=off@0ld"}, notNamed: []string{"gateway="}},
		{name: "push_docling_stale_build", isPR: false, body: withSentry("on", setField("docling", "build", "0ld")), wantExit: 1,
			named: []string{"docling=on@0ld"}, notNamed: []string{"gateway="}},
		{name: "push_docling_sentry_bool", isPR: false, body: withSentry("on", setField("docling", "sentry", true)), wantExit: 1,
			named: []string{"docling=true@deadbeef"}, notNamed: []string{"gateway="}},
		{name: "pr_Docling_is_not_docling", isPR: true, body: withSentry("off", func(t *testing.T, s []map[string]any) []map[string]any {
			return append(s, map[string]any{"name": "Docling", "status": "up", "build": "deadbeef"})
		}), wantExit: 1, named: []string{"Docling=none@deadbeef"}, notNamed: []string{"docling="}},
		{name: "doclingx_is_not_docling", isPR: true, body: withSentry("off", func(t *testing.T, s []map[string]any) []map[string]any {
			return append(s, map[string]any{"name": "doclingx", "status": "up", "build": "deadbeef"})
		}), wantExit: 1, named: []string{"doclingx=none@deadbeef"}, notNamed: []string{"docling="}},
		{name: "pr_off_newline", isPR: true, body: withSentry("off", setField("submission", "sentry", "off\n")), wantExit: 1,
			named: []string{"submission=off"}},
		{name: "push_on_newline", isPR: false, body: withSentry("on", setField("submission", "sentry", "on\n")), wantExit: 1,
			named: []string{"submission=on"}},
		{name: "push_on_space", isPR: false, body: withSentry("on", setField("validation", "sentry", "on ")), wantExit: 1,
			named: []string{"validation=on "}},
		{name: "pr_sentry_true", isPR: true, body: withSentry("off", setField("tenancy", "sentry", true)), wantExit: 1,
			named: []string{"tenancy=true@deadbeef"}, notNamed: []string{"gateway="}},
		{name: "pr_sentry_number", isPR: true, body: withSentry("off", setField("portfolio", "sentry", 0)), wantExit: 1,
			named: []string{"portfolio=0@deadbeef"}, notNamed: []string{"gateway="}},
		{name: "push_sentry_null", isPR: false, body: withSentry("on", setField("invoice", "sentry", json.RawMessage("null"))), wantExit: 1,
			named: []string{"invoice=none@deadbeef"}, notNamed: []string{"gateway="}},
		{name: "push_sentry_false", isPR: false, body: withSentry("off", setField("dashboard", "sentry", false)), wantExit: 1,
			named: []string{"dashboard="}, notNamed: []string{"gateway="}},
		{name: "pr_build_none", isPR: true, body: withSentry("off", setField("invoice", "build", nil)), wantExit: 1,
			named: []string{"invoice=off@none"}, notNamed: []string{"gateway="}},
		{name: "pr_gateway_on", isPR: true, body: withSentry("off", setField("gateway", "sentry", "on")), wantExit: 1,
			named: []string{"gateway=on@deadbeef", "fork-vars-after-urls"}, notNamed: []string{"tenancy="}},
		{name: "push_gateway_stale", isPR: false, body: withSentry("on", setField("gateway", "build", "0ld")), wantExit: 1,
			named: []string{"gateway=on@0ld"}, notNamed: []string{"tenancy="}},
		{name: "pr_Auth_is_not_auth", isPR: true, body: withSentry("off", func(t *testing.T, s []map[string]any) []map[string]any {
			return append(s, map[string]any{"name": "Auth", "status": "up", "build": "deadbeef"})
		}), wantExit: 1, named: []string{"Auth=none@deadbeef"}},
		{name: "pr_xauth_is_not_auth", isPR: true, body: withSentry("off", func(t *testing.T, s []map[string]any) []map[string]any {
			return append(s, map[string]any{"name": "xauth", "status": "up", "build": "deadbeef"})
		}), wantExit: 1, named: []string{"xauth=none@deadbeef"}},
		// Exemption is by name alone: whatever auth reports is not this step's to judge.
		{name: "pr_exempt_values_ignored", isPR: true, body: withSentry("off", func(t *testing.T, s []map[string]any) []map[string]any {
			sentryEntry(t, s, "auth")["sentry"] = "on"
			return s
		}), wantExit: 0},
		{name: "pr_second_gateway_stale", isPR: true, body: withSentry("off", func(t *testing.T, s []map[string]any) []map[string]any {
			return append(s, map[string]any{"name": "gateway", "status": "up", "build": "0ld", "sentry": "off"})
		}), wantExit: 1, named: []string{"gateway=off@0ld"}, notNamed: []string{"gateway=off@deadbeef"}},
		{name: "pr_string_entry", isPR: true, body: func(t *testing.T) any {
			return map[string]any{"services": append(sentryFleetAny("off"), "tenancy")}
		}, wantExit: 1, named: []string{"not an object"}},
		{name: "pr_null_entry", isPR: true, body: func(t *testing.T) any {
			return map[string]any{"services": append(sentryFleetAny("off"), nil)}
		}, wantExit: 1, named: []string{"=none@none"}},
		{name: "services_null", isPR: true, body: func(*testing.T) any { return map[string]any{"services": nil} }, wantExit: 1,
			named: []string{"no services list"}},
		{name: "services_object", isPR: true, body: func(*testing.T) any {
			return map[string]any{"services": map[string]any{"gateway": map[string]any{"build": "deadbeef", "sentry": "off"}}}
		}, wantExit: 1, named: []string{"no services list"}},
		{name: "top_level_array", isPR: true, body: func(*testing.T) any { return sentryFleetAny("off") }, wantExit: 1,
			named: []string{"no services list"}},
		{name: "only_exempt_entries", isPR: true, body: func(*testing.T) any {
			return map[string]any{"services": sentryFleetAny("off")[len(sentryCheckedNames):]}
		}, wantExit: 1, named: []string{"no gateway entry"}},
	}

	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			code, out, urls := runWithGateShim(t, run, "fleet", r.body(t), "EXPECTED_BUILD=deadbeef", "IS_PR="+strconv.FormatBool(r.isPR))
			errs := errorLines(out)
			if r.wantExit == 0 {
				checkSentryPass(t, r, code, out, urls, errs)
				return
			}
			if code != 1 {
				t.Fatalf("exits %d, want 1 (output %q)", code, out)
			}
			if !slices.ContainsFunc(urls, func(u string) bool { return strings.HasSuffix(u, "/healthz/fleet") }) {
				t.Errorf("the curl shim saw %v, no /healthz/fleet fetch", urls)
			}
			if len(errs) == 0 {
				t.Fatalf("a failing step printed no ::error:: line (output %q)", out)
			}
			joined := strings.Join(errs, "\n")
			for _, n := range r.named {
				if !strings.Contains(joined, n) {
					t.Errorf("no ::error:: line contains %q: %q", n, errs)
				}
			}
			for _, n := range r.notNamed {
				if strings.Contains(joined, n) {
					t.Errorf("an ::error:: line names %q, which is not an offender: %q", n, errs)
				}
			}
		})
	}
}

func checkSentryPass(t *testing.T, r sentryRow, code int, out string, urls, errs []string) {
	t.Helper()
	if len(urls) == 0 {
		t.Fatalf("the curl shim saw no fetch; the step never read /healthz/fleet (exit %d, output %q)", code, out)
	}
	// Design item 4: one roll-up body, so a passing run fetches once.
	if len(urls) != 1 || !strings.HasSuffix(urls[0], "/healthz/fleet") {
		t.Errorf("the curl shim saw %v, want exactly one /healthz/fleet fetch", urls)
	}
	if code != 0 || len(errs) != 0 {
		t.Fatalf("exits %d with errors %q, want 0 and none (output %q)", code, errs, out)
	}
	lines := strings.Split(out, "\n")
	want := sentryCheckedNames
	// The "(... exempt)" clause may itself name a service; only the checked list counts.
	i := slices.IndexFunc(lines, func(l string) bool {
		l = exemptClauseRE.ReplaceAllString(l, "")
		return slices.IndexFunc(want, func(n string) bool { return !strings.Contains(l, n) }) < 0
	})
	if i < 0 {
		t.Fatalf("no output line names every checked entry %v outside its exempt clause (output %q)", want, out)
	}
	if !strings.Contains(lines[i], "(auth exempt)") {
		t.Errorf("the success line %q does not name auth as exempt", lines[i])
	}
	if push := pushDirectionRE.MatchString(lines[i]); push == r.isPR {
		t.Errorf("IS_PR=%v: the success line %q names the wrong direction (push is %q)", r.isPR, lines[i], pushDirectionRE)
	}
	if r.isPR && !strings.Contains(lines[i], "off") {
		t.Errorf("the PR success line %q does not name off", lines[i])
	}
}

// fleetSeqShim is a curl that serves $SHIM_DIR/fleet.<n> on its n-th call, the last
// repeating; a call with no file behind it fails as a refused connection.
const fleetSeqShim = `#!/bin/sh
out=""; url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift ;;
    http*) url="$1" ;;
  esac
  shift
done
echo "$url" >> "$SHIM_DIR/curl.log"
n=$(( $(wc -l < "$SHIM_DIR/curl.log") ))
last=$(cat "$SHIM_DIR/last")
[ "$n" -le "$last" ] || n=$last
[ -f "$SHIM_DIR/fleet.$n" ] || exit 7
cp "$SHIM_DIR/fleet.$n" "$out"
`

// runSentrySeq runs the step with curl answering bodies in call order; a nil body is a refused connection.
func runSentrySeq(t *testing.T, run string, isPR bool, bodies ...[]byte) (code int, out string, urls []string) {
	t.Helper()
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Fatalf("jq is not on PATH: %v", err)
	}
	dir := t.TempDir()
	files := map[string][]byte{"curl": []byte(fleetSeqShim), "sleep": []byte("#!/bin/sh\n"), "last": []byte(strconv.Itoa(len(bodies)))}
	for i, b := range bodies {
		if b != nil {
			files["fleet."+strconv.Itoa(i+1)] = b
		}
	}
	for f, b := range files {
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	block := strings.ReplaceAll(run, "/tmp/", dir+"/")
	code, out = runGate(t, block, "PATH="+dir+":"+filepath.Dir(jq)+":/usr/bin:/bin", "SHIM_DIR="+dir,
		"GATEWAY_URL="+probeGatewayURL, "IS_PR="+strconv.FormatBool(isPR))
	return code, out, readLog(t, filepath.Join(dir, "curl.log"))
}

func TestSentryGateRetriesTheRollUp(t *testing.T) {
	run := sentryGateRun(t)
	if run == "" {
		t.Fatal("dev-env.yml fleet-gate has no step reading .sentry")
	}
	fleet := func(edit func(t *testing.T, s []map[string]any) []map[string]any) []byte {
		b, err := json.Marshal(withSentry("off", edit)(t))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	good, invoiceOn := fleet(nil), fleet(setField("invoice", "sentry", "on"))

	rows := []struct {
		name      string
		bodies    [][]byte
		wantExit  int
		wantFetch int
		named     string
	}{
		{"unreachable", [][]byte{nil}, 1, 5, "/healthz/fleet was unreachable"},
		{"unparseable", [][]byte{[]byte("not json{")}, 1, 5, "no services list"},
		{"wrong_shape_then_good", [][]byte{[]byte(`"not json"`), good}, 0, 2, ""},
		{"down_then_good", [][]byte{nil, nil, nil, nil, good}, 0, 5, ""},
		{"down_then_offender", [][]byte{nil, invoiceOn}, 1, 2, "invoice=on@deadbeef"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			code, out, urls := runSentrySeq(t, run, true, r.bodies...)
			errs := errorLines(out)
			if code != r.wantExit {
				t.Fatalf("exits %d, want %d (output %q)", code, r.wantExit, out)
			}
			if len(urls) != r.wantFetch || !strings.HasSuffix(urls[0], "/healthz/fleet") {
				t.Errorf("the curl shim saw %v, want %d /healthz/fleet fetches", urls, r.wantFetch)
			}
			if r.wantExit == 0 {
				if len(errs) != 0 || !strings.Contains(out, "reconciliation") || !strings.Contains(out, "docling") {
					t.Errorf("a passing run printed errors %q or no success line naming reconciliation and docling (output %q)", errs, out)
				}
				return
			}
			if !strings.Contains(strings.Join(errs, "\n"), r.named) {
				t.Errorf("no ::error:: line contains %q: %q", r.named, errs)
			}
		})
	}
}

var jobNameRE = regexp.MustCompile(`^  ([A-Za-z0-9_-]+):\s*$`)

// devEnvJobNames returns the job ids under jobs: in comment-stripped lines.
func devEnvJobNames(lines []string) []string {
	var names []string
	in := false
	for _, l := range lines {
		if strings.HasPrefix(l, "jobs:") {
			in = true
			continue
		}
		if in && strings.TrimSpace(l) != "" && !strings.HasPrefix(l, " ") {
			break
		}
		if m := jobNameRE.FindStringSubmatch(l); in && m != nil {
			names = append(names, m[1])
		}
	}
	return names
}

// IS_PR hard-coded or the step gated to push lets a leaked DSN pass on a PR; no runtime row sees it.
func TestSentryGateIsOneFleetGateStepWithIsPR(t *testing.T) {
	lines := devEnvCode(t)
	jobs := devEnvJobNames(lines)
	if !slices.Contains(jobs, "fleet-gate") || !slices.Contains(jobs, "prepare-env") {
		t.Fatalf("dev-env.yml parsed to jobs %v, without fleet-gate or prepare-env; the scan is broken", jobs)
	}
	type hit struct {
		job  string
		step []string
	}
	var hits []hit
	scanned := 0
	for _, j := range jobs {
		for _, s := range jobSteps(jobBlock(lines, j)) {
			scanned++
			if sentryReadRE.MatchString(runText(s)) {
				hits = append(hits, hit{j, s})
			}
		}
	}
	if scanned == 0 {
		t.Fatal("dev-env.yml parsed to no steps; the scan is broken")
	}
	if len(hits) != 1 {
		var where []string
		for _, h := range hits {
			where = append(where, h.job)
		}
		t.Fatalf("dev-env.yml has %d step(s) reading .sentry (in %v) across %d steps, want exactly 1", len(hits), where, scanned)
	}
	h := hits[0]
	if h.job != "fleet-gate" {
		t.Errorf("the .sentry step is in job %q, want fleet-gate", h.job)
	}
	if v, ok := stepKey(h.step, "if"); ok || stepIf(h.step) != "" {
		t.Errorf("the .sentry step carries if: %q; it must run on every event", v)
	}
	env := stepEnv(h.step)
	for k, want := range map[string]string{
		"IS_PR":          "${{ github.event_name == 'pull_request' }}",
		"EXPECTED_BUILD": "${{ github.sha }}",
	} {
		if got, ok := env[k]; !ok || got != want {
			t.Errorf("the .sentry step's env %s = %q (set %v), want %q", k, got, ok, want)
		}
	}
}
