// railway_env_sentry_test.go drives railway-env.sh set-sentry-off against a stateful scripted Railway.
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	sentryUsage = "usage: railway-env.sh set-sentry-off <environment-id>"

	// Planted values a fork inherits. None may be printed.
	sentryDSNSentinel   = "https://5eedpublickey@o4500000.ingest.de.sentry.io/4500000000000001"
	sentryTokenSentinel = "sntrys_planted_source_auth_token"
	sentryDBSentinel    = "postgresql://invoice_app:planted-db-password@h:5432/railway"
	sentryReference     = "${{shared.SENTRY_DSN}}"

	sentrySelfTestClosing = "Sentry-off self-test: all fixtures passed, no token read, no network call."
)

var (
	sentryBackends = []string{"gateway", "tenancy", "portfolio", "invoice", "validation", "submission", "dashboard", "notifications", "reconciliation", "docling"}
	sentrySPAs     = []string{"landing", "app", "ops-console", "support-console"}
	sentrySPANames = []string{"VITE_SENTRY_DSN", "SENTRY_AUTH_TOKEN"}

	// SENTRY_DSN not as the tail of VITE_SENTRY_DSN.
	bareSentryDSN = regexp.MustCompile(`(^|[^A-Z_])SENTRY_DSN`)
)

func sentrySvcID(name string) string { return "svc-" + name }

func sentryEdge(id, name string) string {
	return `{"node":{"serviceId":"` + id + `","serviceName":"` + name + `"}}`
}

// sentrySettle lists every Sentry service except skip, plus extra edges.
func sentrySettle(skip string, extra ...string) string {
	var edges []string
	for _, n := range slices.Concat(sentryBackends, sentrySPAs) {
		if n != skip {
			edges = append(edges, sentryEdge(sentrySvcID(n), n))
		}
	}
	edges = append(edges, extra...)
	return `{"data":{"environment":{"serviceInstances":{"edges":[` + strings.Join(edges, ",") + `]}}}}`
}

func sentryEnvList(forkEphemeral, forkListed bool) string {
	edges := []string{`{"node":{"id":"` + persistentEnvironmentID + `","name":"production","isEphemeral":false}}`}
	if forkListed {
		eph := "false"
		if forkEphemeral {
			eph = "true"
		}
		edges = append(edges, `{"node":{"id":"`+forkEnvID+`","name":"pr-900","isEphemeral":`+eph+`}}`)
	}
	edges = append(edges, `{"node":{"id":"env-other","name":"pr-901","isEphemeral":true}}`)
	return `{"data":{"environments":{"edges":[` + strings.Join(edges, ",") + `]}}}`
}

func sentryStores() map[string]map[string]string {
	stores := map[string]map[string]string{}
	for _, b := range sentryBackends {
		stores[sentrySvcID(b)] = map[string]string{"SENTRY_DSN": sentryDSNSentinel, "DATABASE_URL": sentryDBSentinel}
	}
	for _, s := range sentrySPAs {
		stores[sentrySvcID(s)] = map[string]string{
			"VITE_SENTRY_DSN":   sentryDSNSentinel,
			"SENTRY_AUTH_TOKEN": sentryTokenSentinel,
			"DATABASE_URL":      sentryDBSentinel,
		}
	}
	return stores
}

// sentryConfirmed reports whether a non-error line confirms all three names blank in the fork.
func sentryConfirmed(out string) bool {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "::error::") || !strings.Contains(strings.ToLower(l), "confirmed") || !strings.Contains(l, forkEnvID) {
			continue
		}
		if bareSentryDSN.MatchString(l) && strings.Contains(l, "VITE_SENTRY_DSN") && strings.Contains(l, "SENTRY_AUTH_TOKEN") {
			return true
		}
	}
	return false
}

// reReads lists the service id of every variable read sent after a write, with its call index.
func reReads(calls []railwayCall) (ids []string, at []int) {
	wrote := false
	for i, c := range calls {
		if isVariableWrite(c) {
			wrote = true
			continue
		}
		if wrote && isVariableRead(c) {
			for _, id := range readServices(c) {
				ids = append(ids, id)
				at = append(at, i)
			}
		}
	}
	return ids, at
}

// singleReRead fails unless one request re-read svc after the write and no read followed it:
// every verdict runs on that one re-read.
func singleReRead(t *testing.T, s authShim, svc string) {
	t.Helper()
	calls := s.calls(t)
	ids, at := reReads(calls)
	if !slices.Contains(ids, sentrySvcID(svc)) {
		t.Errorf("%s was never re-read; re-reads = %v", svc, ids)
		return
	}
	if n := opCount(t, s, "varsRead"); n != 2 {
		t.Errorf("%d varsRead calls, want 2: the read and one re-read", n)
	}
	if last := len(calls) - 1; at[0] != last {
		t.Errorf("a read followed the re-read (call %d of %d)", at[0], last)
	}
}

func refusesAsSet(t *testing.T, out, label string) {
	t.Helper()
	errs := errorLines(out)
	if !strings.Contains(errs, label) || !strings.Contains(errs, "Value not printed") {
		t.Errorf("no ::error:: line names %s and says \"Value not printed\"; output = %q", label, out)
	}
}

// refusesUnreadableRead is the batched read's refusal: it names the service, says it is unreadable, and is not the "set" refusal.
func refusesUnreadableRead(t *testing.T, out, svc string) {
	t.Helper()
	errs := errorLines(out)
	if !strings.Contains(errs, svc) || !strings.Contains(errs, "unreadable") {
		t.Errorf("no ::error:: line names %s and says it is unreadable; output = %q", svc, out)
	}
	if strings.Contains(errs, "Value not printed") {
		t.Errorf("an unreadable map produced the \"set\" refusal; the two must read differently; output = %q", out)
	}
}

func refusesUnreadable(t *testing.T, out string) {
	t.Helper()
	errs := errorLines(out)
	if !strings.Contains(errs, "NOT evidence") {
		t.Errorf("no ::error:: line says the unreadable map is NOT evidence the variables are unset; output = %q", out)
	}
	if strings.Contains(errs, "Value not printed") {
		t.Errorf("an unreadable map produced the \"set\" refusal; the two must read differently; output = %q", out)
	}
}

func TestSetSentryOffAgainstAScriptedRailway(t *testing.T) {
	noReRead := func(t *testing.T, s authShim, out string) {
		if ids, _ := reReads(s.calls(t)); len(ids) != 0 {
			t.Errorf("a failed write was followed by re-reads %v", ids)
		}
		if sentryConfirmed(out) {
			t.Errorf("a failed write printed the confirmation line; output = %q", out)
		}
		if n := opCount(t, s, "varsWrite"); n != 1 {
			t.Errorf("varsWrite calls = %d, want the one failed write, not retried", n)
		}
	}
	noUpsert := func(says string) func(*testing.T, authShim, string) {
		return func(t *testing.T, s authShim, out string) {
			if !strings.Contains(errorLines(out), says) {
				t.Errorf("no ::error:: line says %q; output = %q", says, out)
			}
			if ups := s.upserts(t); len(ups) != 0 {
				t.Errorf("a refused environment received upserts %v", names(ups))
			}
		}
	}
	noService := func(says string) func(*testing.T, authShim, string) {
		return func(t *testing.T, s authShim, out string) {
			if !strings.Contains(errorLines(out), says) {
				t.Errorf("no ::error:: line says %q; output = %q", says, out)
			}
			if sentryConfirmed(out) {
				t.Errorf("a refused service list printed the confirmation line; output = %q", out)
			}
		}
	}
	// A first read with no map for the gateway refuses before any write.
	unreadableGateway := func(t *testing.T, s authShim, out string) {
		refusesUnreadableRead(t, out, "gateway")
		if n := opCount(t, s, "varsWrite"); n != 0 {
			t.Errorf("an unreadable map was followed by %d varsWrite call(s), want none", n)
		}
	}
	gatewaySet := func(t *testing.T, s authShim, out string) {
		refusesAsSet(t, out, "gateway.SENTRY_DSN")
		singleReRead(t, s, "gateway")
	}

	cases := []struct {
		name    string
		envList string
		settle  string
		bend    map[string]string // service -> jq filter over its read
		files   map[string]string // shim file -> body
		arg     string            // defaults to forkEnvID
		code    int
		needles []string
		check   func(t *testing.T, s authShim, out string)
	}{
		{name: "inherited_values_are_blanked", code: 0, check: checkBlanked},
		{name: "not_ephemeral", envList: sentryEnvList(false, true), code: 1, check: noUpsert("is NOT ephemeral")},
		{name: "foreign_id", envList: sentryEnvList(true, false), code: 1, check: noUpsert("No environment with id " + forkEnvID)},
		{name: "no_docling", settle: sentrySettle("docling"), code: 1, check: noService("is named 'docling'")},
		{name: "two_apps", settle: sentrySettle("", sentryEdge("svc-app-2", "app")), code: 1, check: noService("are named 'app'")},
		{name: "dsn_survives_on_gateway", bend: map[string]string{"gateway": `.SENTRY_DSN = "` + sentryDSNSentinel + `"`}, code: 1, check: gatewaySet},
		{name: "whitespace", bend: map[string]string{"gateway": `.SENTRY_DSN = " \t "`}, code: 1, check: gatewaySet},
		{name: "unrendered_reference", bend: map[string]string{"gateway": `.SENTRY_DSN = "` + sentryReference + `"`}, code: 1, needles: []string{sentryReference}, check: gatewaySet},
		{name: "token_survives_on_landing", bend: map[string]string{"landing": `.SENTRY_AUTH_TOKEN = "` + sentryTokenSentinel + `"`}, code: 1, check: func(t *testing.T, s authShim, out string) {
			refusesAsSet(t, out, "landing.SENTRY_AUTH_TOKEN")
			ids, _ := reReads(s.calls(t))
			// Every verdict runs on the one re-read: the services before landing and the SPAs after it.
			for _, svc := range slices.Concat(sentryBackends, []string{"app", "ops-console", "support-console"}) {
				if !slices.Contains(ids, sentrySvcID(svc)) {
					t.Errorf("%s was never re-read; re-reads = %v", svc, ids)
				}
				name := "SENTRY_DSN"
				if slices.Contains(sentrySPAs, svc) {
					name = "VITE_SENTRY_DSN"
				}
				if !strings.Contains(out, svc+"."+name+" is empty") {
					t.Errorf("%s did not pass its verdict (no %q line); output = %q", svc, svc+"."+name+" is empty", out)
				}
			}
			singleReRead(t, s, "landing")
		}},
		// A backend is checked for SENTRY_DSN only, so the SPA names on it do not refuse.
		{name: "backend_checks_sentry_dsn_only", bend: map[string]string{"gateway": `.VITE_SENTRY_DSN = "` + sentryDSNSentinel + `" | .SENTRY_AUTH_TOKEN = "` + sentryTokenSentinel + `"`}, code: 0, check: func(t *testing.T, s authShim, out string) {
			if !sentryConfirmed(out) {
				t.Errorf("no confirmation line; output = %q", out)
			}
		}},
		{name: "variables_null", bend: map[string]string{"gateway": "null"}, code: 1, check: unreadableGateway},
		{name: "variables_array", bend: map[string]string{"gateway": "[]"}, code: 1, check: unreadableGateway},
		{name: "write_refused", files: map[string]string{"upsert-SENTRY_DSN.json": `{"errors":[{"message":"Not Authorized"}]}`}, code: 1, check: noReRead},
		{name: "write_transport_failure", files: map[string]string{"upsert-SENTRY_DSN.fail": "curl: (22) The requested URL returned error: 400"}, code: 1, check: noReRead},
		{name: "vite_dsn_survives_on_support_console", bend: map[string]string{"support-console": `.VITE_SENTRY_DSN = "` + sentryDSNSentinel + `"`}, code: 1, check: func(t *testing.T, s authShim, out string) {
			refusesAsSet(t, out, "support-console.VITE_SENTRY_DSN")
			if sentryConfirmed(out) {
				t.Errorf("a refused re-read printed the confirmation line; output = %q", out)
			}
		}},
		{name: "json_null_value", bend: map[string]string{"gateway": `.SENTRY_DSN = null`}, code: 1, check: gatewaySet},
		{name: "variables_string", bend: map[string]string{"gateway": `"` + sentryDSNSentinel + `"`}, code: 1, check: unreadableGateway},
		// The shim then answers an empty alias, which the batched read treats as no variable map.
		{name: "read_not_json", bend: map[string]string{"gateway": `error("broken read")`}, code: 1, check: unreadableGateway},
		// Only the re-read after the write is unreadable.
		{name: "reread_unreadable", bend: map[string]string{"gateway": `if .SENTRY_DSN == "" then null else . end`}, code: 1, check: func(t *testing.T, s authShim, out string) {
			refusesUnreadableRead(t, out, "gateway")
			if !strings.Contains(errorLines(out), "written but not confirmed") {
				t.Errorf("no ::error:: line says the write is not confirmed; output = %q", out)
			}
			if n := opCount(t, s, "varsWrite"); n != 1 {
				t.Errorf("varsWrite calls = %d, want the write that landed", n)
			}
			singleReRead(t, s, "gateway")
		}},
		// One request writes every service, so a write refused for the SPA names fails the whole batch.
		{name: "token_write_refused_fails_the_batch", files: map[string]string{"upsert-SENTRY_AUTH_TOKEN.json": `{"errors":[{"message":"Not Authorized"}]}`}, code: 1, check: noReRead},
		// Only the literal compare can refuse here with no call: the ephemeral check would read the list first.
		{name: "persistent_id_with_every_variable_set", arg: persistentEnvironmentID, code: 1, check: func(t *testing.T, s authShim, out string) {
			if !strings.Contains(errorLines(out), persistentEnvironmentID) {
				t.Errorf("no ::error:: line names the persistent environment; output = %q", out)
			}
			if calls := s.calls(t); len(calls) != 0 {
				t.Errorf("the refusal reached Railway: %v", operations(calls))
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := map[string]string{"envList": sentryEnvList(true, true), "settle": sentrySettle("")}
			if c.envList != "" {
				resp["envList"] = c.envList
			}
			if c.settle != "" {
				resp["settle"] = c.settle
			}
			s := newAuthShim(t, resp, sentryStores())
			for svc, filter := range c.bend {
				s.bendRead(t, sentrySvcID(svc), filter)
			}
			for f, body := range c.files {
				writeFile(t, filepath.Join(s.dir, f), body)
			}

			arg := forkEnvID
			if c.arg != "" {
				arg = c.arg
			}
			stdout, stderr, code := s.run(t, forkExports(true, true, true), "set-sentry-off", arg)
			out := stdout + stderr
			if code != c.code {
				t.Fatalf("exit %d, want %d; output = %q", code, c.code, out)
			}
			c.check(t, s, out)

			// No read value and no token on any path.
			for _, n := range slices.Concat([]string{sentryDSNSentinel, sentryTokenSentinel, sentryDBSentinel, forkToken}, c.needles) {
				if strings.Contains(out, n) {
					t.Errorf("output leaks %q; output = %q", n, out)
				}
			}
		})
	}
}

func checkBlanked(t *testing.T, s authShim, out string) {
	t.Helper()
	calls := s.calls(t)
	ups := s.upserts(t)
	if len(ups) == 0 {
		t.Fatal("no upserts at all")
	}
	var want []authUpsert
	for _, b := range sentryBackends {
		want = append(want, authUpsert{sentrySvcID(b), "SENTRY_DSN", ""})
	}
	for _, sp := range sentrySPAs {
		for _, n := range sentrySPANames {
			want = append(want, authUpsert{sentrySvcID(sp), n, ""})
		}
	}
	key := func(u authUpsert) string { return u.Service + "." + u.Name + "=" + u.Value }
	got := make([]string, len(ups))
	for i, u := range ups {
		got[i] = key(u)
	}
	wantKeys := make([]string, len(want))
	for i, u := range want {
		wantKeys[i] = key(u)
	}
	slices.Sort(got)
	slices.Sort(wantKeys)
	if !slices.Equal(got, wantKeys) {
		t.Errorf("upserts = %v\nwant exactly %v", got, wantKeys)
	}

	ws := collectionWritesIn(calls)
	for _, w := range ws {
		if w.SkipDeploys != true || w.Env != forkEnvID {
			t.Errorf("the %s write: skipDeploys=%v environmentId=%v, want true and %s", w.Service, w.SkipDeploys, w.Env, forkEnvID)
		}
	}

	echoes := upsertEcho.FindAllString(out, -1)
	if len(echoes) != len(want) {
		t.Errorf("%d upsert echo line(s), want %d; output = %q", len(echoes), len(want), out)
	}
	for _, e := range echoes {
		if !redactedLine.MatchString(e) {
			t.Errorf("upsert echo %q is not <svc>.<NAME> = <redacted>", e)
		}
	}

	// One write of every service, then one re-read of the same services.
	if want, got := []string{"envList", "settle", "varsRead", "varsWrite", "varsRead"}, operations(calls); !slices.Equal(got, want) {
		t.Errorf("Railway calls = %v, want %v", got, want)
	}
	reread, _ := reReads(calls)
	var all []string
	for _, n := range slices.Concat(sentryBackends, sentrySPAs) {
		all = append(all, sentrySvcID(n))
	}
	slices.Sort(reread)
	slices.Sort(all)
	if len(all) != 14 || !slices.Equal(reread, all) {
		t.Errorf("re-reads = %v, want one per service %v", reread, all)
	}

	if !sentryConfirmed(out) {
		t.Errorf("no confirmation line names the fork and all three variables; output = %q", out)
	}
	// Control for the no-leak check: the maps the command read still carried the DATABASE_URL needle.
	raw, err := os.ReadFile(filepath.Join(s.dir, "store-"+sentrySvcID("gateway")+".json"))
	if err != nil || !strings.Contains(string(raw), sentryDBSentinel) {
		t.Errorf("control: gateway's store no longer holds the DATABASE_URL needle (%v), so its absence from output proves nothing", err)
	}
}

func TestSetSentryOffUsageExitsTwo(t *testing.T) {
	shim := newCurlShim(t)
	stdout, stderr, code := runBashScript(t, shim.prelude+"bash '"+railwayEnvScript(t)+"' set-sentry-off\n")
	out := stdout + stderr
	if code != 2 {
		t.Errorf("exit %d, want 2; output = %q", code, out)
	}
	if !strings.Contains(out, sentryUsage) {
		t.Errorf("output lacks %q; output = %q", sentryUsage, out)
	}
	if strings.Contains(out, "is not set") {
		t.Errorf("the usage guard ran after an env guard; output = %q", out)
	}
	if calls := shim.calls(t); calls != "" {
		t.Errorf("usage called curl:\n%s", calls)
	}
	shim.requireOnPath(t)
}

func TestSetSentryOffRefusesThePersistentEnvironment(t *testing.T) {
	shim := newCurlShim(t)
	stdout, stderr, code := runBashScript(t,
		shim.prelude+"export RAILWAY_DEV_ENVIRONMENT_ID="+persistentEnvironmentID+"\nbash '"+railwayEnvScript(t)+"' set-sentry-off \"$@\"\n",
		persistentEnvironmentID)
	out := stdout + stderr
	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, out)
	}
	if !strings.Contains(errorLines(out), persistentEnvironmentID) {
		t.Errorf("no ::error:: line names the persistent environment %s; output = %q", persistentEnvironmentID, out)
	}
	if strings.Contains(out, "RAILWAY_API_TOKEN is not set") {
		t.Errorf("the refusal ran after require_env; output = %q", out)
	}
	if calls := shim.calls(t); calls != "" {
		t.Errorf("the refusal called curl:\n%s", calls)
	}
	shim.requireOnPath(t)
}

func TestSetSentryOffSelfTestPassesWithNoTokenAndNoNetwork(t *testing.T) {
	shim := newCurlShim(t)
	stdout, stderr, code := runBashScript(t, shim.prelude+"bash '"+railwayEnvScript(t)+"' set-sentry-off --self-test\n")
	out := stdout + stderr
	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, out)
	}
	if !strings.Contains(stdout, sentrySelfTestClosing) {
		t.Errorf("stdout lacks the closing line %q; output = %q", sentrySelfTestClosing, out)
	}
	if calls := shim.calls(t); calls != "" {
		t.Errorf("the self-test called curl:\n%s", calls)
	}
	shim.requireOnPath(t)
}

// The failure no runtime test sees: a service added to the fleet but not here keeps production's DSN in every fork.
func TestSentryOffListsMatchTheDeployedFleet(t *testing.T) {
	root := repoRoot(t)
	script, err := os.ReadFile(filepath.Join(root, "scripts", "ci", "railway-env.sh"))
	if err != nil {
		t.Fatal(err)
	}
	devEnv, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "dev-env.yml"))
	if err != nil {
		t.Fatal(err)
	}
	lines := stripHashComments(strings.Split(string(script), "\n"))

	contextMatrix := sentryMatrixList(t, string(devEnv), "deploy-context")
	wantBackends := []string{"gateway"}
	for _, n := range contextMatrix {
		if n != "auth" {
			wantBackends = append(wantBackends, n)
		}
	}
	wantSPAs := sentryMatrixList(t, string(devEnv), "deploy-spas")

	for _, c := range []struct {
		array string
		want  []string
	}{{"SENTRY_BACKENDS", wantBackends}, {"SENTRY_SPAS", wantSPAs}} {
		got, ok := bashArray(lines, c.array)
		if !ok {
			t.Errorf("railway-env.sh defines no %s=(...) array", c.array)
			continue
		}
		slices.Sort(got)
		want := slices.Clone(c.want)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v (from dev-env.yml)", c.array, got, want)
		}
	}
}

// bashArray returns the words of a top-level NAME=(...) assignment, which may span lines.
func bashArray(lines []string, name string) ([]string, bool) {
	for i, l := range lines {
		if !strings.HasPrefix(l, name+"=(") {
			continue
		}
		body := strings.TrimPrefix(l, name+"=(")
		for !strings.Contains(body, ")") && i+1 < len(lines) {
			i++
			body += " " + lines[i]
		}
		body, _, _ = strings.Cut(body, ")")
		var out []string
		for _, w := range strings.Fields(body) {
			out = append(out, strings.Trim(w, `"'`))
		}
		return out, true
	}
	return nil, false
}

var (
	sentryMatrixRe  = regexp.MustCompile(`^\s+service:\s*\[([^\]]*)\]\s*$`)
	sentryJobHeadRe = regexp.MustCompile(`^  [a-zA-Z]`)
)

// sentryMatrixList returns the `service: [...]` matrix of the named job (as fleetgate's matrixList does).
func sentryMatrixList(t *testing.T, content, job string) []string {
	t.Helper()
	lines := strings.Split(content, "\n")
	start := slices.IndexFunc(lines, func(l string) bool { return strings.TrimRight(l, " \t\r") == "  "+job+":" })
	if start < 0 {
		t.Fatalf("dev-env.yml has no `%s:` job", job)
	}
	for _, l := range lines[start+1:] {
		if sentryJobHeadRe.MatchString(l) {
			break
		}
		if m := sentryMatrixRe.FindStringSubmatch(l); m != nil {
			var out []string
			for _, f := range strings.Split(m[1], ",") {
				if f = strings.TrimSpace(f); f != "" {
					out = append(out, f)
				}
			}
			if len(out) == 0 {
				t.Fatalf("dev-env.yml job `%s` has an empty service matrix", job)
			}
			return out
		}
	}
	t.Fatalf("dev-env.yml job `%s` has no `service: [...]` matrix", job)
	return nil
}

// sentry_verdict on shapes set-sentry-off cannot produce: graphql_post exits first on `.errors`, and the shim always wraps a map.
func TestSentryVerdictTruthTableIncludingShapesNoFixtureCovers(t *testing.T) {
	script := "set -euo pipefail\n" + shellFunctionSource(t, "auth_kind", "sentry_verdict") +
		"rc=0\nsentry_verdict \"$1\" landing \"${@:2}\" || rc=$?\nexit \"$rc\"\n"
	const (
		leak  = "sntrys_truth_table_marker"
		dsn   = "https://truthkey@o1.ingest.de.sentry.io/9"
		spa   = "VITE_SENTRY_DSN SENTRY_AUTH_TOKEN"
		pass  = "pass"
		set   = "set"
		unred = "unreadable"
	)
	vars := func(fields string) string {
		return `{"data":{"variables":{"DATABASE_URL":"` + leak + `"` + fields + `}}}`
	}

	cases := []struct {
		name, json, names, want string
		named                   []string // <svc>.<NAME> each refusal or pass line must carry
	}{
		{"absent", vars(``), "SENTRY_DSN", pass, []string{"landing.SENTRY_DSN is absent"}},
		{"empty", vars(`,"SENTRY_DSN":""`), "SENTRY_DSN", pass, []string{"landing.SENTRY_DSN is empty"}},
		{"both spa names empty beside an empty errors array", `{"errors":[],"data":{"variables":{"VITE_SENTRY_DSN":"","SENTRY_AUTH_TOKEN":"","DATABASE_URL":"` + leak + `"}}}`, spa, pass,
			[]string{"landing.VITE_SENTRY_DSN is empty", "landing.SENTRY_AUTH_TOKEN is empty"}},
		// The SDK reads the exact name, so another case is not a live DSN.
		{"lower-case name only", vars(`,"sentry_dsn":"` + dsn + `"`), "SENTRY_DSN", pass, []string{"landing.SENTRY_DSN is absent"}},
		{"dsn", vars(`,"SENTRY_DSN":"` + dsn + `"`), "SENTRY_DSN", set, []string{"landing.SENTRY_DSN"}},
		{"single space", vars(`,"SENTRY_DSN":" "`), "SENTRY_DSN", set, []string{"landing.SENTRY_DSN"}},
		{"newline only", vars(`,"SENTRY_DSN":"\n"`), "SENTRY_DSN", set, []string{"landing.SENTRY_DSN"}},
		{"json null value", vars(`,"SENTRY_DSN":null`), "SENTRY_DSN", set, []string{"landing.SENTRY_DSN"}},
		{"false value", vars(`,"SENTRY_DSN":false`), "SENTRY_DSN", set, []string{"landing.SENTRY_DSN"}},
		{"zero value", vars(`,"SENTRY_DSN":0`), "SENTRY_DSN", set, []string{"landing.SENTRY_DSN"}},
		{"object value", vars(`,"SENTRY_DSN":{"v":"` + dsn + `"}`), "SENTRY_DSN", set, []string{"landing.SENTRY_DSN"}},
		{"unrendered reference", vars(`,"SENTRY_DSN":"${{shared.SENTRY_DSN}}"`), "SENTRY_DSN", set, []string{"landing.SENTRY_DSN"}},
		// Every name is checked before the verdict returns.
		{"both spa names set", vars(`,"VITE_SENTRY_DSN":"` + dsn + `","SENTRY_AUTH_TOKEN":"` + leak + `"`), spa, set,
			[]string{"landing.VITE_SENTRY_DSN", "landing.SENTRY_AUTH_TOKEN"}},
		{"only the second spa name set", vars(`,"VITE_SENTRY_DSN":"","SENTRY_AUTH_TOKEN":"` + leak + `"`), spa, set, []string{"landing.SENTRY_AUTH_TOKEN"}},
		{"only the first spa name set", vars(`,"VITE_SENTRY_DSN":"` + dsn + `","SENTRY_AUTH_TOKEN":""`), spa, set, []string{"landing.VITE_SENTRY_DSN"}},
		{"errors beside a clean map", `{"errors":[{"message":"Not Authorized"}],"data":{"variables":{"DATABASE_URL":"` + leak + `"}}}`, "SENTRY_DSN", unred, nil},
		{"errors carrying a value", `{"errors":[{"message":"` + dsn + `"}],"data":{"variables":{"SENTRY_DSN":""}}}`, "SENTRY_DSN", unred, nil},
		{"top-level null", `null`, "SENTRY_DSN", unred, nil},
		{"top-level array", `[` + vars(`,"SENTRY_DSN":""`) + `]`, "SENTRY_DSN", unred, nil},
		{"top-level string", `"` + dsn + `"`, "SENTRY_DSN", unred, nil},
		{"top-level number", `42`, "SENTRY_DSN", unred, nil},
		{"empty object", `{}`, "SENTRY_DSN", unred, nil},
		{"data null", `{"data":null}`, "SENTRY_DSN", unred, nil},
		{"variables null", `{"data":{"variables":null}}`, "SENTRY_DSN", unred, nil},
		{"variables a string", `{"data":{"variables":"` + dsn + `"}}`, "SENTRY_DSN", unred, nil},
		{"variables an array", `{"data":{"variables":["` + dsn + `"]}}`, "SENTRY_DSN", unred, nil},
		{"not json", leak + `{`, "SENTRY_DSN", unred, nil},
		{"truncated json", vars(`,"SENTRY_DSN":""`)[:30], "SENTRY_DSN", unred, nil},
		{"empty input", ``, "SENTRY_DSN", unred, nil},
		{"two json documents", vars(`,"SENTRY_DSN":""`) + " " + vars(`,"SENTRY_DSN":"`+dsn+`"`), "SENTRY_DSN", unred, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, code := runBashScript(t, script, append([]string{c.json}, strings.Fields(c.names)...)...)
			out := stdout + stderr
			errs := errorLines(out)
			switch c.want {
			case pass:
				if code != 0 || errs != "" {
					t.Errorf("exit %d with errors %q, want a pass; output = %q", code, errs, out)
				}
				for _, n := range c.named {
					if !strings.Contains(out, n) {
						t.Errorf("output lacks %q; output = %q", n, out)
					}
				}
			case set:
				if code == 0 {
					t.Errorf("exit 0, want a refusal; output = %q", out)
				}
				for _, n := range c.named {
					refusesAsSet(t, out, n)
				}
				if strings.Contains(errs, "NOT evidence") {
					t.Errorf("a readable set value produced the unreadable refusal; output = %q", out)
				}
			case unred:
				if code == 0 {
					t.Errorf("exit 0, want a refusal; output = %q", out)
				}
				refusesUnreadable(t, out)
			}
			for _, n := range []string{leak, dsn} {
				if strings.Contains(out, n) {
					t.Errorf("the verdict printed %q; output = %q", n, out)
				}
			}
		})
	}
}
