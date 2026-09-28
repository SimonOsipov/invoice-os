package main

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// sentryReadRE matches a jq read of the sentry field; `.sentry.io` is a host, not a read.
var sentryReadRE = regexp.MustCompile(`(?m)\.sentry([^.\w-]|$)`)

// pushDirectionRE is how the success line names the push direction.
var pushDirectionRE = regexp.MustCompile(`\bon( or |\|)off\b`)

// sentryGoNames are the roll-up entries the step checks: every name except docling and auth.
var sentryGoNames = []string{"gateway", "tenancy", "portfolio", "invoice", "validation", "submission", "dashboard", "notifications", "reconciliation"}

// sentryFleetEntries is a deployed-shape roll-up: each Go entry on deadbeef reporting sentry,
// docling on deadbeef without the field, auth as its custom-path [name status] entry.
func sentryFleetEntries(sentry string) []map[string]any {
	var s []map[string]any
	for _, n := range sentryGoNames {
		s = append(s, map[string]any{"name": n, "status": "up", "build": "deadbeef", "sentry": sentry})
	}
	return append(s,
		map[string]any{"name": "docling", "status": "up", "build": "deadbeef"},
		map[string]any{"name": "auth", "status": "up"})
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
			named: []string{"invoice=on@deadbeef", "set-sentry-off"}, notNamed: []string{"gateway=", "tenancy="}},
		{name: "pr_reconciliation_on", isPR: true, body: withSentry("off", setField("reconciliation", "sentry", "on")), wantExit: 1,
			named: []string{"reconciliation=on@deadbeef", "set-sentry-off"}, notNamed: []string{"gateway=", "invoice="}},
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
			for i, n := range sentryGoNames {
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
		{name: "doclingx_is_not_docling", isPR: true, body: withSentry("off", func(t *testing.T, s []map[string]any) []map[string]any {
			return append(s, map[string]any{"name": "doclingx", "status": "up", "build": "deadbeef"})
		}), wantExit: 1, named: []string{"doclingx=none@deadbeef"}, notNamed: []string{"docling=none"}},
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
	i := slices.IndexFunc(lines, func(l string) bool {
		return slices.IndexFunc(sentryGoNames, func(n string) bool { return !strings.Contains(l, n) }) < 0
	})
	if i < 0 {
		t.Fatalf("no output line names every checked entry %v (output %q)", sentryGoNames, out)
	}
	if push := pushDirectionRE.MatchString(lines[i]); push == r.isPR {
		t.Errorf("IS_PR=%v: the success line %q names the wrong direction (push is %q)", r.isPR, lines[i], pushDirectionRE)
	}
	if r.isPR && !strings.Contains(lines[i], "off") {
		t.Errorf("the PR success line %q does not name off", lines[i])
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

// X17: IS_PR hard-coded or the step gated to push lets a leaked DSN pass on a PR; no runtime row sees it.
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
