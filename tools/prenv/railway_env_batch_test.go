// railway_env_batch_test.go drives the fork variable writers through set_service_vars:
// read once, write only the names that differ in one variableCollectionUpsert, re-read.
package main

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	batchGatewayURL = "https://gateway-pr-900.up.railway.app"
	batchAppURL     = "https://app-pr-900.up.railway.app"
	batchLandingURL = "https://landing-pr-900.up.railway.app"
	batchOpsURL     = "https://ops-console-pr-900.up.railway.app"
	batchSupportURL = "https://support-console-pr-900.up.railway.app"
	// reconcile_url_variables: origins="$app_url,$landing_url,$ops_url,$support_url".
	batchOrigins = batchAppURL + "," + batchLandingURL + "," + batchOpsURL + "," + batchSupportURL

	// What a fork inherits from production.
	batchProdAppURL     = "https://app.ascomply.com"
	batchProdLandingURL = "https://www.ascomply.com"

	batchAllConfirmed = "All 12 environment variables confirmed" // reconcile_url_variables
)

// Secret names print `= <redacted>` (Design, "Variable writes").
var (
	forkAuthSecrets    = map[string][]string{"auth": {"GOTRUE_JWT_KEYS", "GOTRUE_JWT_SECRET"}, "gateway": {"AUTH_ADMIN_PASSWORD"}}
	sentrySecretNames  = []string{"SENTRY_DSN", "VITE_SENTRY_DSN", "SENTRY_AUTH_TOKEN"}
	batchSvcIDExports  = map[string]string{"RAILWAY_SVC_GATEWAY_ID": "gateway", "RAILWAY_SVC_APP_ID": "app", "RAILWAY_SVC_LANDING_ID": "landing", "RAILWAY_SVC_OPS_CONSOLE_ID": "ops-console", "RAILWAY_SVC_SUPPORT_CONSOLE_ID": "support-console", "RAILWAY_SVC_POSTGRES_ID": "Postgres"}
	reconcileURLLabels = []string{"gateway", "app", "landing", "ops-console", "support-console"}
)

func batchExports() string {
	var b strings.Builder
	b.WriteString(forkExports(true, true, true))
	for _, k := range slices.Sorted(maps.Keys(batchSvcIDExports)) {
		b.WriteString("export " + k + "=" + sentrySvcID(batchSvcIDExports[k]) + "\n")
	}
	return b.String()
}

// reconcileIntended is reconcile_url_variables' intended map, by service id.
func reconcileIntended() map[string]map[string]string {
	return map[string]map[string]string{
		sentrySvcID("gateway"):         {"CORS_ALLOWED_ORIGINS": batchOrigins},
		sentrySvcID("app"):             {"VITE_GATEWAY_URL": batchGatewayURL, "VITE_LANDING_URL": batchLandingURL},
		sentrySvcID("landing"):         {"VITE_GATEWAY_URL": batchGatewayURL, "VITE_APP_URL": batchAppURL, "VITE_OPS_URL": batchOpsURL, "VITE_SUPPORT_URL": batchSupportURL, "VITE_REGISTRATION_OPEN": "true"},
		sentrySvcID("ops-console"):     {"VITE_GATEWAY_URL": batchGatewayURL, "VITE_LANDING_URL": batchLandingURL},
		sentrySvcID("support-console"): {"VITE_GATEWAY_URL": batchGatewayURL, "VITE_LANDING_URL": batchLandingURL},
	}
}

// reconcileStale is a fork that inherited production's URLs everywhere.
func reconcileStale() map[string]map[string]string {
	stores := reconcileIntended()
	for _, vars := range stores {
		for n := range vars {
			vars[n] = batchProdLandingURL
		}
	}
	return stores
}

func runReconcileURLs(t *testing.T, s authShim) (stdout, stderr string, code int) {
	t.Helper()
	return s.run(t, batchExports(), "reconcile-urls", forkEnvID, batchGatewayURL, batchAppURL, batchLandingURL, batchOpsURL, batchSupportURL)
}

// fleetShim lists every Sentry service (gateway, submission, invoice among them) in forkEnvID.
func fleetShim(t *testing.T, stores map[string]map[string]string) authShim {
	t.Helper()
	return newAuthShim(t, map[string]string{"envList": sentryEnvList(true, true), "settle": sentrySettle("")}, stores)
}

func sentrySteadyStores() map[string]map[string]string {
	stores := sentryStores()
	for _, vars := range stores {
		for _, n := range sentrySecretNames {
			if _, ok := vars[n]; ok {
				vars[n] = ""
			}
		}
	}
	return stores
}

type collectionWrite struct {
	At           int
	Env, Service string
	Vars         map[string]string
	SkipDeploys  any
	HasReplace   bool
	Input        map[string]any
}

func collectionWritesIn(calls []railwayCall) []collectionWrite {
	var out []collectionWrite
	for i, c := range calls {
		if !strings.Contains(c.Query, "variableCollectionUpsert(") {
			continue
		}
		in, _ := c.Variables["input"].(map[string]any)
		w := collectionWrite{At: i, Input: in, Vars: map[string]string{}, SkipDeploys: in["skipDeploys"]}
		w.Env, _ = in["environmentId"].(string)
		w.Service, _ = in["serviceId"].(string)
		_, w.HasReplace = in["replace"]
		vars, _ := in["variables"].(map[string]any)
		for n, v := range vars {
			w.Vars[n], _ = v.(string)
		}
		out = append(out, w)
	}
	return out
}

func collectionWrites(t *testing.T, s authShim) []collectionWrite {
	t.Helper()
	return collectionWritesIn(s.calls(t))
}

func writesTo(ws []collectionWrite, svc string) []collectionWrite {
	var out []collectionWrite
	for _, w := range ws {
		if w.Service == svc {
			out = append(out, w)
		}
	}
	return out
}

// writeNames lists a write's names only: values may be secrets.
func writeNames(ws []collectionWrite) []string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Service+":"+strings.Join(slices.Sorted(maps.Keys(w.Vars)), ","))
	}
	return out
}

// isVariableWrite covers both write mutations, so a guard holds before and after the move.
func isVariableWrite(c railwayCall) bool {
	return strings.Contains(c.Query, "variableUpsert(") || strings.Contains(c.Query, "variableCollectionUpsert(")
}

func isVariableRead(c railwayCall) bool { return strings.Contains(c.Query, "variables(projectId") }

func readService(c railwayCall) string {
	if s, ok := c.Variables["s"].(string); ok {
		return s
	}
	s, _ := c.Variables["serviceId"].(string)
	return s
}

// lastWriteOf is the call index of the last write of svc.name in either mutation, or -1.
func lastWriteOf(calls []railwayCall, svc, name string) int {
	at := -1
	for i, c := range calls {
		if !isVariableWrite(c) {
			continue
		}
		in, _ := c.Variables["input"].(map[string]any)
		if in["serviceId"] != svc {
			continue
		}
		vars, _ := in["variables"].(map[string]any)
		if _, ok := vars[name]; ok || in["name"] == name {
			at = i
		}
	}
	return at
}

func readStore(t *testing.T, s authShim, svc string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.dir, "store-"+svc+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("store-%s.json is not a JSON object: %v", svc, err)
	}
	return m
}

// echoLines are the write echoes `  <label>.<NAME> = ...` for label.name.
func echoLines(out, label, name string) []string {
	return regexp.MustCompile(`(?m)^[ \t]+`+regexp.QuoteMeta(label+"."+name)+` = .*$`).FindAllString(out, -1)
}

var heldLine = regexp.MustCompile(`(?m)^  (\S+): (\d+) of (\d+) already hold the intended value — not written\.$`)

// heldLines maps each label to its "k of n" line.
func heldLines(out string) map[string]string {
	got := map[string]string{}
	for _, m := range heldLine.FindAllStringSubmatch(out, -1) {
		got[m[1]] = m[2] + " of " + m[3]
	}
	return got
}

// readsPerService counts variable reads by service id.
func readsPerService(calls []railwayCall) map[string]int {
	got := map[string]int{}
	for _, c := range calls {
		if isVariableRead(c) {
			got[readService(c)]++
		}
	}
	return got
}

func TestSetSentryOff_SteadyStateWritesNothing(t *testing.T) {
	s := fleetShim(t, sentrySteadyStores())
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "set-sentry-off", forkEnvID)
	out := stdout + stderr
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, out)
	}
	if ups := s.upserts(t); len(ups) != 0 {
		t.Errorf("steady state wrote %d variable(s) %v, want none", len(ups), names(ups))
	}
	calls := s.calls(t)
	ops := operations(calls)
	if len(calls) != 16 {
		t.Errorf("%d calls %v, want 16: envList, settle and one read per Sentry service", len(calls), ops)
	}
	if len(ops) < 2 || ops[0] != "envList" || ops[1] != "settle" {
		t.Errorf("calls begin %v, want envList then settle", ops)
	}
	var read, all []string
	for _, c := range calls {
		if isVariableRead(c) {
			read = append(read, readService(c))
		}
	}
	for _, n := range slices.Concat(sentryBackends, sentrySPAs) {
		all = append(all, sentrySvcID(n))
	}
	slices.Sort(read)
	slices.Sort(all)
	if !slices.Equal(read, all) {
		t.Errorf("variable reads = %v, want exactly one per service %v", read, all)
	}
	if e := upsertEcho.FindAllString(out, -1); len(e) != 0 {
		t.Errorf("steady state printed write lines %q", e)
	}
	if !sentryConfirmed(out) {
		t.Errorf("no confirmation line; output = %q", out)
	}
	wantHeld := map[string]string{}
	for _, b := range sentryBackends {
		wantHeld[b] = "1 of 1"
	}
	for _, sp := range sentrySPAs {
		wantHeld[sp] = "2 of 2"
	}
	if got := heldLines(out); !reflect.DeepEqual(got, wantHeld) {
		t.Errorf("held lines = %v, want %v", got, wantHeld)
	}
}

func TestReconcileURLs_SteadyStateMakesFiveReads(t *testing.T) {
	s := newAuthShim(t, nil, reconcileIntended())
	stdout, stderr, code := runReconcileURLs(t, s)
	out := stdout + stderr
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, out)
	}
	if ups := s.upserts(t); len(ups) != 0 {
		t.Errorf("steady state wrote %d variable(s) %v, want none", len(ups), names(ups))
	}
	calls := s.calls(t)
	if len(calls) != 5 {
		t.Errorf("%d calls %v, want 5: one read per service", len(calls), operations(calls))
	}
	var read, want []string
	for _, c := range calls {
		if isVariableRead(c) {
			read = append(read, readService(c))
		}
	}
	for _, l := range reconcileURLLabels {
		want = append(want, sentrySvcID(l))
	}
	slices.Sort(read)
	slices.Sort(want)
	if !slices.Equal(read, want) {
		t.Errorf("variable reads = %v, want exactly one per service %v", read, want)
	}
	if e := upsertEcho.FindAllString(out, -1); len(e) != 0 {
		t.Errorf("steady state printed write lines %q", e)
	}
	if !strings.Contains(out, batchAllConfirmed) {
		t.Errorf("no %q line; output = %q", batchAllConfirmed, out)
	}
	// Each name is checked on its own service's map, so the closing line is not vacuous.
	intended := reconcileIntended()
	wantHeld := map[string]string{}
	for _, l := range reconcileURLLabels {
		vars := intended[sentrySvcID(l)]
		if len(vars) == 0 {
			t.Fatalf("control: no intended names for %s", l)
		}
		n := strconv.Itoa(len(vars))
		wantHeld[l] = n + " of " + n
		for name := range vars {
			if !strings.Contains(out, "  "+l+"."+name+" confirmed.\n") {
				t.Errorf("no `%s.%s confirmed.` line; output = %q", l, name, out)
			}
		}
	}
	if got := heldLines(out); !reflect.DeepEqual(got, wantHeld) {
		t.Errorf("held lines = %v, want %v", got, wantHeld)
	}
}

// Steady state for the single-service commands: one read per service, no write, a held line each.
func TestSetServiceVars_SteadyStateWritesNothing(t *testing.T) {
	gw, sub, inv := sentrySvcID("gateway"), sentrySvcID("submission"), sentrySvcID("invoice")
	aiSteady := map[string]string{"AI_FAKE": "true", "JEV_FAKE": "true", "OPENROUTER_API_KEY": ""}
	cases := []struct {
		name     string
		shim     func(t *testing.T) authShim
		run      func(t *testing.T, s authShim) (string, string, int)
		reads    map[string]int
		held     map[string]string
		confirms []string
	}{
		{"set-fork-auth-site", func(t *testing.T) authShim {
			stores := forkAuthStores(freshJWK(t))
			stores[authForkAuthID]["GOTRUE_SITE_URL"] = forkSiteURL
			stores[authForkGatewayID]["AUTH_SITE_URL"] = forkSiteURL
			return newAuthShim(t, forkAuthRailway(), stores)
		}, func(t *testing.T, s authShim) (string, string, int) {
			return s.run(t, forkAuthExports(), "set-fork-auth-site", authForkEnvID, forkSiteURL)
		}, map[string]int{authForkAuthID: 1, authForkGatewayID: 1},
			map[string]string{"auth": "1 of 1", "gateway": "1 of 1"},
			[]string{"auth.GOTRUE_SITE_URL confirmed in environment " + authForkEnvID, "gateway.AUTH_SITE_URL confirmed in environment " + authForkEnvID}},
		{"set-ai-fake", func(t *testing.T) authShim {
			return fleetShim(t, map[string]map[string]string{sub: maps.Clone(aiSteady), inv: maps.Clone(aiSteady)})
		}, func(t *testing.T, s authShim) (string, string, int) {
			return s.run(t, forkExports(true, true, true), "set-ai-fake", forkEnvID)
		}, map[string]int{sub: 1, inv: 1},
			map[string]string{"submission": "3 of 3", "invoice": "3 of 3"},
			[]string{"AI and Jev fake mode confirmed in environment " + forkEnvID}},
		{"set-fork-reconciliation-url", func(t *testing.T) authShim {
			return fleetShim(t, map[string]map[string]string{gw: {"RECONCILIATION_URL": reconciliationURL}})
		}, func(t *testing.T, s authShim) (string, string, int) {
			return s.run(t, forkExports(true, true, true), "set-fork-reconciliation-url", forkEnvID)
		}, map[string]int{gw: 1}, map[string]string{"gateway": "1 of 1"},
			[]string{"gateway.RECONCILIATION_URL confirmed in environment " + forkEnvID}},
		{"set-fork-environment", func(t *testing.T) authShim {
			return fleetShim(t, map[string]map[string]string{gw: {"ENVIRONMENT": "development"}})
		}, func(t *testing.T, s authShim) (string, string, int) {
			return s.run(t, forkExports(true, true, true), "set-fork-environment", forkEnvID)
		}, map[string]int{gw: 1}, map[string]string{"gateway": "1 of 1"},
			[]string{"gateway ENVIRONMENT=development confirmed in environment " + forkEnvID}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := c.shim(t)
			stdout, stderr, code := c.run(t, s)
			out := stdout + stderr
			if code != 0 {
				t.Fatalf("exit %d, want 0; output = %q", code, out)
			}
			calls := s.calls(t)
			if len(calls) == 0 {
				t.Fatal("control: no call reached the shim")
			}
			if m := s.mutations(t); len(m) != 0 {
				t.Errorf("steady state sent mutations %v, want none", m)
			}
			if got := readsPerService(calls); !reflect.DeepEqual(got, c.reads) {
				t.Errorf("variable reads per service = %v, want %v", got, c.reads)
			}
			if e := upsertEcho.FindAllString(out, -1); len(e) != 0 {
				t.Errorf("steady state printed write lines %q", e)
			}
			if got := heldLines(out); !reflect.DeepEqual(got, c.held) {
				t.Errorf("held lines = %v, want %v", got, c.held)
			}
			for _, want := range c.confirms {
				if !strings.Contains(out, want) {
					t.Errorf("no %q line; output = %q", want, out)
				}
			}
		})
	}
}

func TestSetSentryOff_InheritedValuesOneWritePerService(t *testing.T) {
	stores := sentryStores()
	// landing's token is already blank, so its write carries VITE_SENTRY_DSN only.
	stores[sentrySvcID("landing")]["SENTRY_AUTH_TOKEN"] = ""
	s := fleetShim(t, stores)
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "set-sentry-off", forkEnvID)
	out := stdout + stderr
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, out)
	}
	ws := collectionWrites(t, s)
	if len(ws) != 14 {
		t.Errorf("%d variableCollectionUpsert call(s) %v, want 14: one per Sentry service", len(ws), writeNames(ws))
	}
	if len(ws) == 0 {
		t.FailNow()
	}
	want := map[string][]string{}
	for _, b := range sentryBackends {
		want[sentrySvcID(b)] = []string{"SENTRY_DSN"}
	}
	for _, sp := range sentrySPAs {
		want[sentrySvcID(sp)] = []string{"SENTRY_AUTH_TOKEN", "VITE_SENTRY_DSN"}
	}
	want[sentrySvcID("landing")] = []string{"VITE_SENTRY_DSN"}

	seen := map[string]int{}
	for _, w := range ws {
		seen[w.Service]++
		if got := slices.Sorted(maps.Keys(w.Vars)); !slices.Equal(got, want[w.Service]) {
			t.Errorf("%s write carries %v, want exactly the differing names %v", w.Service, got, want[w.Service])
		}
		for n, v := range w.Vars {
			if v != "" {
				t.Errorf("%s write sets %s to a non-empty value, want \"\"", w.Service, n)
			}
		}
		if w.SkipDeploys != true {
			t.Errorf("%s write: skipDeploys = %v, want true", w.Service, w.SkipDeploys)
		}
		if w.HasReplace {
			t.Errorf("%s write carries a replace key; replace:true deletes every variable not in the map", w.Service)
		}
		if w.Env != forkEnvID {
			t.Errorf("%s write targets environment %q, want the fork %s", w.Service, w.Env, forkEnvID)
		}
	}
	for svc := range want {
		if seen[svc] != 1 {
			t.Errorf("%s got %d write(s), want 1", svc, seen[svc])
		}
	}
	// A service with nothing already intended prints no "0 of n" line.
	if got, wantHeld := heldLines(out), map[string]string{"landing": "1 of 2"}; !reflect.DeepEqual(got, wantHeld) {
		t.Errorf("held lines = %v, want %v", got, wantHeld)
	}
	if !sentryConfirmed(out) {
		t.Errorf("no confirmation line; output = %q", out)
	}
}

func TestSetServiceVars_OnlyChangedNamesAreWritten(t *testing.T) {
	stores := reconcileIntended()
	stores[sentrySvcID("app")]["VITE_LANDING_URL"] = batchProdLandingURL
	s := newAuthShim(t, nil, stores)
	stdout, stderr, code := runReconcileURLs(t, s)
	out := stdout + stderr
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, out)
	}
	ws := collectionWrites(t, s)
	app := writesTo(ws, sentrySvcID("app"))
	if len(app) != 1 {
		t.Fatalf("app got %d variableCollectionUpsert call(s), want 1; writes = %v", len(app), writeNames(ws))
	}
	if !reflect.DeepEqual(app[0].Vars, map[string]string{"VITE_LANDING_URL": batchLandingURL}) {
		t.Errorf("app's write carries %v, want only VITE_LANDING_URL=%s", app[0].Vars, batchLandingURL)
	}
	if ups := s.upserts(t); len(ups) != 1 {
		t.Errorf("writes = %v, want app.VITE_LANDING_URL only: every other value is already intended", names(ups))
	}
	wantHeld := map[string]string{"gateway": "1 of 1", "app": "1 of 2", "landing": "5 of 5", "ops-console": "2 of 2", "support-console": "2 of 2"}
	if got := heldLines(out); !reflect.DeepEqual(got, wantHeld) {
		t.Errorf("held lines = %v, want %v", got, wantHeld)
	}
	if got := echoLines(out, "app", "VITE_LANDING_URL"); len(got) != 1 || got[0] != "  app.VITE_LANDING_URL = "+batchLandingURL {
		t.Errorf("app.VITE_LANDING_URL write lines = %q, want one with its value", got)
	}
}

// The flag is a build-time switch the fork never inherits: absent or not exactly "true" is rewritten.
func TestReconcileURLs_WritesTheRegistrationFlag(t *testing.T) {
	landing := sentrySvcID("landing")
	for _, stale := range []string{"", "false", "TRUE"} {
		t.Run("stale="+stale, func(t *testing.T) {
			stores := reconcileIntended()
			if stale == "" {
				delete(stores[landing], "VITE_REGISTRATION_OPEN")
			} else {
				stores[landing]["VITE_REGISTRATION_OPEN"] = stale
			}
			s := newAuthShim(t, nil, stores)
			stdout, stderr, code := runReconcileURLs(t, s)
			out := stdout + stderr
			if code != 0 {
				t.Fatalf("exit %d, want 0; output = %q", code, out)
			}
			ups := s.upserts(t)
			if len(ups) != 1 || oneUpsert(t, ups, landing, "VITE_REGISTRATION_OPEN") != "true" {
				t.Errorf("writes = %v, want landing.VITE_REGISTRATION_OPEN=true only", names(ups))
			}
			if got := readStore(t, s, landing)["VITE_REGISTRATION_OPEN"]; got != "true" {
				t.Errorf("landing.VITE_REGISTRATION_OPEN holds %v, want true", got)
			}
			if at := lastWriteOf(s.calls(t), landing, "VITE_REGISTRATION_OPEN"); at < 0 || !s.readAfter(t, landing, at) {
				t.Errorf("landing was not re-read after the flag write (write at call %d)", at)
			}
			if !strings.Contains(out, "  landing.VITE_REGISTRATION_OPEN = true\n") {
				t.Errorf("no write line for landing.VITE_REGISTRATION_OPEN; output = %q", out)
			}
		})
	}
}

// guard, passes at HEAD: the flag write must not widen the refusal of the persistent environment.
func TestReconcileURLs_RefusesThePersistentEnvironment(t *testing.T) {
	s := newAuthShim(t, nil, reconcileIntended())
	stdout, stderr, code := s.run(t, batchExports(), "reconcile-urls", persistentEnvironmentID, batchGatewayURL, batchAppURL, batchLandingURL, batchOpsURL, batchSupportURL)
	out := stdout + stderr
	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, out)
	}
	if !strings.Contains(errorLines(out), "persistent development environment") {
		t.Errorf("no ::error:: line refusing the persistent environment; error lines = %q", errorLines(out))
	}
	if ups := s.upserts(t); len(ups) != 0 {
		t.Errorf("a refused run wrote %v", names(ups))
	}
}

// A stale or absent value is rewritten to the fork URL and re-read; the consoles' VITE_GATEWAY_URL may be absent.
func TestSetServiceVars_StaleValueIsRewritten(t *testing.T) {
	for _, c := range []struct {
		svc, name, want string
		stale           string // "" deletes the name from the store
	}{
		{"landing", "VITE_APP_URL", batchAppURL, batchProdAppURL},
		{"ops-console", "VITE_GATEWAY_URL", batchGatewayURL, ""},
		{"support-console", "VITE_GATEWAY_URL", batchGatewayURL, ""},
		{"ops-console", "VITE_GATEWAY_URL", batchGatewayURL, batchProdAppURL},
	} {
		t.Run(c.svc+"."+c.name+" stale="+c.stale, func(t *testing.T) {
			id := sentrySvcID(c.svc)
			stores := reconcileIntended()
			if c.stale == "" {
				delete(stores[id], c.name)
			} else {
				stores[id][c.name] = c.stale
			}
			s := newAuthShim(t, nil, stores)
			stdout, stderr, code := runReconcileURLs(t, s)
			out := stdout + stderr
			if code != 0 {
				t.Fatalf("exit %d, want 0; output = %q", code, out)
			}
			if got := readStore(t, s, id)[c.name]; got != c.want {
				t.Errorf("%s.%s holds %v, want the fork URL %s", c.svc, c.name, got, c.want)
			}
			if at := lastWriteOf(s.calls(t), id, c.name); at < 0 || !s.readAfter(t, id, at) {
				t.Errorf("%s was not re-read after the %s write (write at call %d)", c.svc, c.name, at)
			}
			if ups := s.upserts(t); len(ups) != 1 || len(upsertsOf(ups, id, c.name)) != 1 {
				t.Errorf("writes = %v, want %s.%s only", names(ups), c.svc, c.name)
			}
			if !strings.Contains(out, batchAllConfirmed) {
				t.Errorf("no %q line; output = %q", batchAllConfirmed, out)
			}
		})
	}
}

// guard, passes at HEAD (set-ai-fake). set-fork-auth's absent GOTRUE_SMTP_HOST: TestSetForkAuth_BlanksForkSMTP.
func TestSetServiceVars_AbsentIsNotEmpty(t *testing.T) {
	t.Run("set-ai-fake", func(t *testing.T) {
		sub, inv := sentrySvcID("submission"), sentrySvcID("invoice")
		s := fleetShim(t, map[string]map[string]string{
			sub: {"AI_FAKE": "true", "JEV_FAKE": "true", "OPENROUTER_API_KEY": ""},
			inv: {"AI_FAKE": "true", "JEV_FAKE": "true"},
		})
		stdout, stderr, code := s.run(t, forkExports(true, true, true), "set-ai-fake", forkEnvID)
		out := stdout + stderr
		if code != 0 {
			t.Fatalf("exit %d, want 0; output = %q", code, out)
		}
		if got := upsertsOf(s.upserts(t), inv, "OPENROUTER_API_KEY"); len(got) != 1 || got[0].Value != "" {
			t.Errorf("invoice.OPENROUTER_API_KEY writes = %v, want one write of \"\": absent is not empty", got)
		}
		v, ok := readStore(t, s, inv)["OPENROUTER_API_KEY"]
		if !ok || v != "" {
			t.Errorf("invoice's store holds OPENROUTER_API_KEY = %v (present %t), want \"\"", v, ok)
		}
		if !strings.Contains(out, "invoice.OPENROUTER_API_KEY is empty") {
			t.Errorf("the verdict does not say invoice.OPENROUTER_API_KEY is empty; output = %q", out)
		}
	})
	t.Run("set-sentry-off", func(t *testing.T) {
		stores := sentrySteadyStores()
		gw, app := sentrySvcID("gateway"), sentrySvcID("app")
		delete(stores[gw], "SENTRY_DSN")
		delete(stores[app], "SENTRY_AUTH_TOKEN")
		s := fleetShim(t, stores)
		stdout, stderr, code := s.run(t, forkExports(true, true, true), "set-sentry-off", forkEnvID)
		out := stdout + stderr
		if code != 0 {
			t.Fatalf("exit %d, want 0; output = %q", code, out)
		}
		ws := collectionWrites(t, s)
		want := map[string]map[string]string{gw: {"SENTRY_DSN": ""}, app: {"SENTRY_AUTH_TOKEN": ""}}
		got := map[string]map[string]string{}
		for _, w := range ws {
			got[w.Service] = w.Vars
		}
		if len(ws) != 2 || !reflect.DeepEqual(got, want) {
			t.Errorf("writes = %v, want one \"\" write each for gateway.SENTRY_DSN and app.SENTRY_AUTH_TOKEN", writeNames(ws))
		}
		for svc, name := range map[string]string{gw: "SENTRY_DSN", app: "SENTRY_AUTH_TOKEN"} {
			if v, ok := readStore(t, s, svc)[name]; !ok || v != "" {
				t.Errorf("%s's store holds %s = %v (present %t), want \"\"", svc, name, v, ok)
			}
		}
		for _, l := range []string{"gateway.SENTRY_DSN is empty", "app.SENTRY_AUTH_TOKEN is empty"} {
			if !strings.Contains(out, l) {
				t.Errorf("the verdict does not say %s; output = %q", l, out)
			}
		}
	})
}

// guard, passes at HEAD
func TestSetServiceVars_EveryWriteTargetsTheForkOnly(t *testing.T) {
	gw := sentrySvcID("gateway")
	cases := []struct {
		name string
		env  string
		shim func(t *testing.T) authShim
		run  func(t *testing.T, s authShim) (string, string, int)
	}{
		{"set-fork-auth", authForkEnvID, func(t *testing.T) authShim { return newForkAuthShim(t, freshJWK(t)) },
			func(t *testing.T, s authShim) (string, string, int) {
				return s.run(t, forkAuthExports(), "set-fork-auth", authForkEnvID)
			}},
		{"set-fork-auth-site", authForkEnvID, newForkSiteShim,
			func(t *testing.T, s authShim) (string, string, int) {
				return s.run(t, forkAuthExports(), "set-fork-auth-site", authForkEnvID, forkSiteURL)
			}},
		{"reconcile-urls", forkEnvID, func(t *testing.T) authShim { return newAuthShim(t, nil, reconcileStale()) }, runReconcileURLs},
		{"set-ai-fake", forkEnvID, func(t *testing.T) authShim {
			planted := map[string]string{"AI_FAKE": "false", "JEV_FAKE": "false", "OPENROUTER_API_KEY": "sk-or-v1-planted"}
			return fleetShim(t, map[string]map[string]string{sentrySvcID("submission"): maps.Clone(planted), sentrySvcID("invoice"): maps.Clone(planted)})
		}, func(t *testing.T, s authShim) (string, string, int) {
			return s.run(t, forkExports(true, true, true), "set-ai-fake", forkEnvID)
		}},
		{"set-sentry-off", forkEnvID, func(t *testing.T) authShim { return fleetShim(t, sentryStores()) },
			func(t *testing.T, s authShim) (string, string, int) {
				return s.run(t, forkExports(true, true, true), "set-sentry-off", forkEnvID)
			}},
		{"set-fork-reconciliation-url", forkEnvID, func(t *testing.T) authShim {
			return fleetShim(t, map[string]map[string]string{gw: {"RECONCILIATION_URL": "http://reconciliation.railway.internal:8081"}})
		}, func(t *testing.T, s authShim) (string, string, int) {
			return s.run(t, forkExports(true, true, true), "set-fork-reconciliation-url", forkEnvID)
		}},
		{"set-fork-environment", forkEnvID, func(t *testing.T) authShim {
			return fleetShim(t, map[string]map[string]string{gw: {"ENVIRONMENT": "production"}})
		}, func(t *testing.T, s authShim) (string, string, int) {
			return s.run(t, forkExports(true, true, true), "set-fork-environment", forkEnvID)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := c.shim(t)
			stdout, stderr, code := c.run(t, s)
			if code != 0 {
				t.Fatalf("exit %d, want 0; output = %q", code, stdout+stderr)
			}
			var targets []string
			for _, call := range s.calls(t) {
				if isVariableWrite(call) {
					in, _ := call.Variables["input"].(map[string]any)
					e, _ := in["environmentId"].(string)
					targets = append(targets, e)
				}
			}
			if len(targets) == 0 {
				t.Fatal("no variable write reached the shim, so the target check proves nothing")
			}
			for _, e := range targets {
				if e == persistentEnvironmentID {
					t.Errorf("a write targets the persistent environment %s", e)
				} else if e != c.env {
					t.Errorf("a write targets environment %q, want the fork %s", e, c.env)
				}
			}
		})
	}
}

// guard, passes at HEAD
func TestSetServiceVars_ReReadMismatchFails(t *testing.T) {
	t.Run("set-fork-environment", func(t *testing.T) {
		gw := sentrySvcID("gateway")
		s := fleetShim(t, map[string]map[string]string{gw: {"ENVIRONMENT": "production"}})
		s.bendRead(t, gw, `.ENVIRONMENT = "production"`)
		stdout, stderr, code := s.run(t, forkExports(true, true, true), "set-fork-environment", forkEnvID)
		out := stdout + stderr
		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, out)
		}
		if !strings.Contains(errorLines(out), "gateway ENVIRONMENT") {
			t.Errorf("no ::error:: line names gateway ENVIRONMENT; error lines = %q", errorLines(out))
		}
		if len(upsertsOf(s.upserts(t), gw, "ENVIRONMENT")) == 0 {
			t.Error("gateway.ENVIRONMENT was never written, so the failure is not a re-read failure")
		}
		if strings.Contains(out, "ENVIRONMENT=development confirmed") {
			t.Errorf("a failed re-read printed the confirmation line; output = %q", out)
		}
	})
	// The first read answers, the re-read after the write is a GraphQL error.
	t.Run("set-fork-environment re-read GraphQL error", func(t *testing.T) {
		gw := sentrySvcID("gateway")
		s := fleetShim(t, map[string]map[string]string{gw: {"ENVIRONMENT": "production", "DATABASE_URL": sentryDBSentinel}})
		setFaults(t, s, "authVars", "ok", "gqlerr")
		stdout, stderr, code := s.run(t, forkExports(true, true, true), "set-fork-environment", forkEnvID)
		out := stdout + stderr
		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, out)
		}
		if !strings.Contains(errorLines(out), "Not Authorized") || !strings.Contains(errorLines(out), "gateway") {
			t.Errorf("no ::error:: line names the gateway's GraphQL error; error lines = %q", errorLines(out))
		}
		if len(upsertsOf(s.upserts(t), gw, "ENVIRONMENT")) != 1 {
			t.Error("gateway.ENVIRONMENT was not written once, so the failure is not a re-read failure")
		}
		if n := readsPerService(s.calls(t))[gw]; n != 2 {
			t.Errorf("%d gateway reads, want 2: the read and the failed re-read", n)
		}
		if strings.Contains(out, "confirmed") || strings.Contains(out, sentryDBSentinel) {
			t.Errorf("a failed re-read printed the confirmation line or a planted value; output = %q", out)
		}
	})
	t.Run("reconcile-urls", func(t *testing.T) {
		landing := sentrySvcID("landing")
		s := newAuthShim(t, nil, reconcileStale())
		s.bendRead(t, landing, `.VITE_APP_URL = "`+batchProdAppURL+`"`)
		stdout, stderr, code := runReconcileURLs(t, s)
		out := stdout + stderr
		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, out)
		}
		if !strings.Contains(errorLines(out), "landing.VITE_APP_URL") {
			t.Errorf("no ::error:: line names landing.VITE_APP_URL; error lines = %q", errorLines(out))
		}
		if len(upsertsOf(s.upserts(t), landing, "VITE_APP_URL")) == 0 {
			t.Error("landing.VITE_APP_URL was never written, so the failure is not a re-read failure")
		}
		if strings.Contains(out, batchAllConfirmed) {
			t.Errorf("a failed re-read printed the confirmation line; output = %q", out)
		}
	})
	// Each console's gateway URL is verified by its own auth_check.
	for _, svc := range []string{"ops-console", "support-console"} {
		t.Run("reconcile-urls "+svc, func(t *testing.T) {
			id := sentrySvcID(svc)
			s := newAuthShim(t, nil, reconcileStale())
			s.bendRead(t, id, `.VITE_GATEWAY_URL = "`+batchProdLandingURL+`"`)
			stdout, stderr, code := runReconcileURLs(t, s)
			out := stdout + stderr
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			if !strings.Contains(errorLines(out), svc+".VITE_GATEWAY_URL") {
				t.Errorf("no ::error:: line names %s.VITE_GATEWAY_URL; error lines = %q", svc, errorLines(out))
			}
			if len(upsertsOf(s.upserts(t), id, "VITE_GATEWAY_URL")) == 0 {
				t.Errorf("%s.VITE_GATEWAY_URL was never written, so the failure is not a re-read failure", svc)
			}
			if strings.Contains(out, batchAllConfirmed) {
				t.Errorf("a failed re-read printed the confirmation line; output = %q", out)
			}
		})
	}
}

func TestSetServiceVars_UnreadableMapWritesNothing(t *testing.T) {
	gw := sentrySvcID("gateway")
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, s authShim)
	}{
		// Both variable reads fault, so the case holds whichever one reads first.
		{"graphql errors", func(t *testing.T, s authShim) {
			setFaults(t, s, "authVars", "gqlerr")
			setFaults(t, s, "svcVars", "gqlerr")
		}},
		{"not an object", func(t *testing.T, s authShim) { s.bendRead(t, gw, "null") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := fleetShim(t, sentryStores())
			c.setup(t, s)
			stdout, stderr, code := s.run(t, forkExports(true, true, true), "set-sentry-off", forkEnvID)
			out := stdout + stderr
			if ups := s.upserts(t); len(ups) != 0 {
				t.Errorf("an unreadable map was followed by %d write(s) %v, want none", len(ups), names(ups))
			}
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			if !strings.Contains(errorLines(out), "gateway") {
				t.Errorf("no ::error:: line names the gateway; error lines = %q", errorLines(out))
			}
			if sentryConfirmed(out) {
				t.Errorf("an unreadable map printed the confirmation line; output = %q", out)
			}
			for _, n := range []string{sentryDSNSentinel, sentryTokenSentinel, sentryDBSentinel} {
				if strings.Contains(out, n) {
					t.Error("the output carries a planted value")
				}
			}
		})
	}
}

func TestSetForkAuth_FreshSecretsAlwaysWritten(t *testing.T) {
	s, _ := runForkAuthOK(t, freshJWK(t))
	first := map[string]string{}
	for label, ns := range forkAuthSecrets {
		svc := map[string]string{"auth": authForkAuthID, "gateway": authForkGatewayID}[label]
		for _, n := range ns {
			got := upsertsOf(s.upserts(t), svc, n)
			if len(got) == 0 {
				t.Fatalf("control: run 1 never wrote %s.%s", label, n)
			}
			first[n] = got[len(got)-1].Value
		}
	}
	n := len(s.calls(t))

	stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth", authForkEnvID)
	if code != 0 {
		t.Fatalf("second run exit %d, want 0; output = %q", code, stdout+stderr)
	}
	second := s.calls(t)[n:]
	ws := collectionWritesIn(second)
	for label, svc := range map[string]string{"auth": authForkAuthID, "gateway": authForkGatewayID} {
		got := writesTo(ws, svc)
		if len(got) != 1 {
			t.Errorf("%s got %d variableCollectionUpsert call(s) on the second run, want 1; writes = %v", label, len(got), writeNames(ws))
			continue
		}
		want := slices.Sorted(slices.Values(forkAuthSecrets[label]))
		if names := slices.Sorted(maps.Keys(got[0].Vars)); !slices.Equal(names, want) {
			t.Errorf("%s's write carries %v, want exactly the fresh secrets %v", label, names, want)
		}
		for _, name := range forkAuthSecrets[label] {
			if v := got[0].Vars[name]; v == "" || v == first[name] {
				t.Errorf("%s.%s on the second run is empty or equal to run 1's value, want a fresh one", label, name)
			}
		}
	}
	// Run 2 writes only through the one collection write per service checked above.
	for _, c := range second {
		if strings.Contains(c.Query, "variableUpsert(") {
			in, _ := c.Variables["input"].(map[string]any)
			t.Errorf("the second run wrote %v.%v with variableUpsert; want only the collection writes", in["serviceId"], in["name"])
		}
	}
}

// guard, passes at HEAD
func TestSetServiceVars_SecretsNeverOnArgvOrInOutput(t *testing.T) {
	scan := func(t *testing.T, s authShim, jqLog, out string, needles map[string]string) {
		t.Helper()
		curlArgv, jqArgv := s.argv(t), readLog(t, jqLog)
		if out == "" || curlArgv == "" || jqArgv == "" {
			t.Fatalf("control: output (%d bytes), curl argv log (%d) or jq argv log (%d) is empty, so a clean scan proves nothing", len(out), len(curlArgv), len(jqArgv))
		}
		for label, n := range needles {
			if n == "" {
				t.Errorf("needle %s is empty", label)
				continue
			}
			for where, text := range map[string]string{"the output": out, "curl's argv": curlArgv, "jq's argv": jqArgv} {
				if strings.Contains(text, n) {
					t.Errorf("%s carries %s", where, label)
				}
			}
		}
	}
	redacted := func(t *testing.T, out, label, name string) {
		t.Helper()
		lines := echoLines(out, label, name)
		if len(lines) == 0 {
			t.Errorf("no write line for %s.%s", label, name)
		}
		for _, l := range lines {
			if !redactedLine.MatchString(l) {
				t.Errorf("the write line for %s.%s is not `= <redacted>`", label, name)
			}
		}
	}

	t.Run("set-fork-auth", func(t *testing.T) {
		src := freshJWK(t)
		s := newForkAuthShim(t, src)
		jqLog := jqArgvLog(t, s)
		stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth", authForkEnvID)
		out := stdout + stderr
		if code != 0 {
			t.Fatalf("exit %d, want 0; output = %q", code, out)
		}
		ups := s.upserts(t)
		last := func(svc, name string) string {
			got := upsertsOf(ups, svc, name)
			if len(got) == 0 {
				t.Fatalf("control: %s was never written", name)
			}
			return got[len(got)-1].Value
		}
		key := last(authForkAuthID, "GOTRUE_JWT_KEYS")
		scan(t, s, jqLog, out, map[string]string{
			"the source key's private scalar":    jwkPrivateScalar(t, src),
			"the source JWT secret":              authSourceJWTSecret,
			"the source admin password":          authSourcePassword,
			"the source Resend key":              authSourceResendKey,
			"the gateway's migration DSN secret": forkEnvSecret,
			"the generated key":                  key,
			"the generated key's private scalar": jwkPrivateScalar(t, key),
			"the generated JWT secret":           last(authForkAuthID, "GOTRUE_JWT_SECRET"),
			"the generated admin password":       last(authForkGatewayID, "AUTH_ADMIN_PASSWORD"),
		})
		for label, ns := range forkAuthSecrets {
			for _, n := range ns {
				redacted(t, out, label, n)
			}
		}
	})

	t.Run("set-sentry-off", func(t *testing.T) {
		s := fleetShim(t, sentryStores())
		jqLog := jqArgvLog(t, s)
		stdout, stderr, code := s.run(t, forkExports(true, true, true), "set-sentry-off", forkEnvID)
		out := stdout + stderr
		if code != 0 {
			t.Fatalf("exit %d, want 0; output = %q", code, out)
		}
		scan(t, s, jqLog, out, map[string]string{
			"the inherited DSN":        sentryDSNSentinel,
			"the inherited auth token": sentryTokenSentinel,
			"the sibling DATABASE_URL": sentryDBSentinel,
		})
		if strings.Contains(out, forkToken) {
			t.Error("the output carries the API token")
		}
		for _, b := range sentryBackends {
			redacted(t, out, b, "SENTRY_DSN")
		}
		for _, sp := range sentrySPAs {
			for _, n := range sentrySPANames {
				redacted(t, out, sp, n)
			}
		}
	})
}

func TestSetServiceVars_CollectionWriteTimeoutRetries(t *testing.T) {
	stores := reconcileIntended()
	stores[sentrySvcID("app")]["VITE_LANDING_URL"] = batchProdLandingURL
	s := newAuthShim(t, nil, stores)
	setFaults(t, s, "varCollectionUpsert", "timeout")
	stdout, stderr, code := runReconcileURLs(t, s)
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, stdout+stderr)
	}
	ws := collectionWrites(t, s)
	if len(ws) != 2 {
		t.Fatalf("%d variableCollectionUpsert call(s) %v, want 2: the timed-out write and its retry", len(ws), writeNames(ws))
	}
	if ws[0].Service != sentrySvcID("app") || !reflect.DeepEqual(ws[0].Input, ws[1].Input) {
		t.Errorf("the retry is not app's write sent again with identical input: %v", writeNames(ws))
	}
	if w := warningLines(stderr); len(w) != 1 {
		t.Errorf("%d ::warning:: line(s) on stderr, want 1: %q", len(w), w)
	}
	if got := readStore(t, s, sentrySvcID("app"))["VITE_LANDING_URL"]; got != batchLandingURL {
		t.Errorf("app.VITE_LANDING_URL holds %v after the retry, want %s", got, batchLandingURL)
	}
	if !strings.Contains(stdout+stderr, batchAllConfirmed) {
		t.Errorf("no %q line; output = %q", batchAllConfirmed, stdout+stderr)
	}
}

// NAME=VALUE splits at the first "=" only.
func TestSetServiceVars_ValueKeepsEveryEqualsSign(t *testing.T) {
	const url = "https://landing-pr-7.up.railway.app/?a=b=c"
	s := newForkSiteShim(t)
	stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth-site", authForkEnvID, url)
	out := stdout + stderr
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, out)
	}
	for svc, name := range map[string]string{authForkAuthID: "GOTRUE_SITE_URL", authForkGatewayID: "AUTH_SITE_URL"} {
		if got := oneUpsert(t, s.upserts(t), svc, name); got != url {
			t.Errorf("%s.%s written as %q, want %q", svc, name, got, url)
		}
	}
}
