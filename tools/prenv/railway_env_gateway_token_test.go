// railway_env_gateway_token_test.go pins set-fork-gateway-token and set-production-gateway-token
// against the stateful scripted Railway of railway_env_auth_test.go.
package main

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	gtBentToken = "PLANTED-BENT-REREAD-VALUE"

	forkGatewayTokenRunCmd = `bash scripts/ci/railway-env.sh set-fork-gateway-token "$ENV_ID"`
)

// Built at runtime so no secret-shaped literal sits in source.
var (
	gtSourceToken = "5eed" + strings.Repeat("00", 29) + "cc"
	gtProdToken   = strings.Repeat("0a", 32) + "0"
	gtOtherToken  = strings.Repeat("0b", 32) + "0"
)

var (
	// Services Railway holds that must never receive GATEWAY_TOKEN.
	gtBystanders    = []string{"reconciliation", "auth", "docling", "app", "landing"}
	alreadySet      = regexp.MustCompile(`(?i)already set`)
	routedBlock     = regexp.MustCompile(`var routedServices = \[\]string\{([^}]*)\}`)
	quotedName      = regexp.MustCompile(`"([a-z]+)"`)
	servicesInArray = regexp.MustCompile(`(?m)^GATEWAY_TOKEN_SERVICES=\(([^)]*)\)`)
)

func gtSvcID(name string) string { return "svc-" + name + "-gt" }

// routedServicesOfGateway reads routedServices from cmd/gateway/main.go.
func routedServicesOfGateway(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(repoRoot(t) + "/cmd/gateway/main.go")
	if err != nil {
		t.Fatal(err)
	}
	m := routedBlock.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("cmd/gateway/main.go has no routedServices slice; the fleet cannot be read")
	}
	var out []string
	for _, q := range quotedName.FindAllStringSubmatch(m[1], -1) {
		out = append(out, q[1])
	}
	if len(out) < 7 {
		t.Fatalf("routedServices parsed as %v, want at least the seven context services", out)
	}
	return out
}

// gatewayTokenTargets is gateway plus every routed service: the eight services that hold GATEWAY_TOKEN.
func gatewayTokenTargets(t *testing.T) []string {
	t.Helper()
	return append([]string{"gateway"}, routedServicesOfGateway(t)...)
}

// gtSettle lists the eight targets and the bystanders, minus the services named in skip.
func gtSettle(t *testing.T, skip ...string) string {
	t.Helper()
	var edges []string
	for _, n := range append(gatewayTokenTargets(t), gtBystanders...) {
		if !slices.Contains(skip, n) {
			edges = append(edges, `{"node":{"serviceId":"`+gtSvcID(n)+`","serviceName":"`+n+`"}}`)
		}
	}
	return `{"data":{"environment":{"serviceInstances":{"edges":[` + strings.Join(edges, ",") + `]}}}}`
}

// gtStores seeds every service; token reports the GATEWAY_TOKEN a target holds, if any.
func gtStores(t *testing.T, envName string, token func(svc string) (string, bool)) map[string]map[string]string {
	t.Helper()
	stores := map[string]map[string]string{}
	for _, n := range append(gatewayTokenTargets(t), gtBystanders...) {
		vars := map[string]string{"RAILWAY_ENVIRONMENT_NAME": envName, "PORT": "8080"}
		if v, ok := token(n); ok && !slices.Contains(gtBystanders, n) {
			vars["GATEWAY_TOKEN"] = v
		}
		stores[gtSvcID(n)] = vars
	}
	return stores
}

func newGTForkShim(t *testing.T, resp map[string]string) authShim {
	t.Helper()
	if resp == nil {
		resp = map[string]string{}
	}
	if resp["envList"] == "" {
		resp["envList"] = authEnvList(true)
	}
	if resp["settle"] == "" {
		resp["settle"] = gtSettle(t)
	}
	return newAuthShim(t, resp, gtStores(t, authForkName, func(string) (string, bool) { return gtSourceToken, true }))
}

func newGTProdShim(t *testing.T, token func(svc string) (string, bool)) authShim {
	t.Helper()
	return newAuthShim(t, map[string]string{"settle": gtSettle(t)}, gtStores(t, "production", token))
}

func runForkGT(t *testing.T, s authShim) (out string, code int) {
	t.Helper()
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "set-fork-gateway-token", authForkEnvID)
	return stdout + stderr, code
}

func runProdGT(t *testing.T, s authShim) (out string, code int) {
	t.Helper()
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "set-production-gateway-token", persistentEnvironmentID)
	return stdout + stderr, code
}

// requireWroteTheEight fails unless each target got exactly one GATEWAY_TOKEN write, all with one
// value, and nothing else was written. It returns that value.
func requireWroteTheEight(t *testing.T, s authShim) string {
	t.Helper()
	ups := s.upserts(t)
	var value string
	for _, n := range gatewayTokenTargets(t) {
		v := oneUpsert(t, ups, gtSvcID(n), "GATEWAY_TOKEN")
		if value == "" {
			value = v
		} else if v != value {
			t.Errorf("%s.GATEWAY_TOKEN differs from the value written to the first target; one value goes to all eight", n)
		}
	}
	if len(ups) != 8 {
		t.Errorf("upserts = %v, want exactly the eight GATEWAY_TOKEN writes", names(ups))
	}
	for _, u := range ups {
		if u.Name != "GATEWAY_TOKEN" {
			t.Errorf("%s.%s was written; only GATEWAY_TOKEN may be", u.Service, u.Name)
		}
		for _, b := range gtBystanders {
			if u.Service == gtSvcID(b) {
				t.Errorf("%s received a write; it does not take the gateway token", b)
			}
		}
	}
	if !hex64.MatchString(value) {
		t.Errorf("the written value is not 64 lowercase hex characters (len %d)", len(value))
	}
	return value
}

// requireSkipDeploys fails unless every mutation, and every input of a batched write, carries skipDeploys: true.
func requireSkipDeploys(t *testing.T, s authShim) {
	t.Helper()
	n := 0
	for _, c := range s.calls(t) {
		if !strings.HasPrefix(strings.TrimSpace(c.Query), "mutation") {
			continue
		}
		inputs := collectionWritesIn([]railwayCall{c})
		if len(inputs) == 0 {
			in, _ := c.Variables["input"].(map[string]any)
			inputs = []collectionWrite{{SkipDeploys: in["skipDeploys"]}}
		}
		for _, w := range inputs {
			n++
			if w.SkipDeploys != true {
				t.Errorf("a write omitted skipDeploys: true, so it would redeploy the service: %v", operations([]railwayCall{c}))
			}
		}
	}
	if n == 0 {
		t.Error("no mutation was made, so skipDeploys was never observed")
	}
}

func requireEachReRead(t *testing.T, s authShim) {
	t.Helper()
	for _, n := range gatewayTokenTargets(t) {
		if i := s.lastCallIndex(t, gtSvcID(n), "GATEWAY_TOKEN"); i < 0 || !s.readAfter(t, gtSvcID(n), i) {
			t.Errorf("%s's variables were not re-read after its GATEWAY_TOKEN write", n)
		}
	}
}

func TestSetForkGatewayToken_Guards(t *testing.T) {
	const usage = "usage: railway-env.sh set-fork-gateway-token <environment-id>"

	t.Run("control: a pr fork is written", func(t *testing.T) {
		s := newGTForkShim(t, nil)
		if out, code := runForkGT(t, s); code != 0 || len(s.mutations(t)) == 0 {
			t.Fatalf("control: exit %d and %d mutation(s), want exit 0 after at least one write; output = %q", code, len(s.mutations(t)), out)
		}
	})

	t.Run("no id", func(t *testing.T) {
		s := newGTForkShim(t, nil)
		stdout, stderr, code := s.run(t, "", "set-fork-gateway-token")
		out := stdout + stderr
		if code != 2 {
			t.Errorf("exit %d, want 2; output = %q", code, out)
		}
		if !strings.Contains(out, usage) {
			t.Errorf("output lacks %q (the generic usage also exits 2, so only this phrase shows the subcommand ran); output = %q", usage, out)
		}
		if strings.Contains(out, "is not set") {
			t.Errorf("the usage guard did not precede require_source_env and require_env; output = %q", out)
		}
		if calls := s.calls(t); len(calls) != 0 {
			t.Errorf("the usage guard called Railway %v", operations(calls))
		}
		s.requireLogs(t)
	})

	t.Run("the persistent id", func(t *testing.T) {
		for _, token := range []bool{true, false} {
			s := newGTForkShim(t, nil)
			stdout, stderr, code := s.run(t, forkExports(token, true, true), "set-fork-gateway-token", persistentEnvironmentID)
			out := stdout + stderr
			if code != 1 {
				t.Errorf("token=%t: exit %d, want 1; output = %q", token, code, out)
			}
			if !authPersisted.MatchString(errorLines(out)) {
				t.Errorf("token=%t: no ::error:: line refuses the persistent environment (%s) by id; output = %q", token, persistentEnvironmentID, out)
			}
			if strings.Contains(out, "RAILWAY_API_TOKEN is not set") {
				t.Errorf("token=%t: require_env ran before the refusal; output = %q", token, out)
			}
			if calls := s.calls(t); len(calls) != 0 {
				t.Errorf("token=%t: the refusal called Railway %v; it must refuse before any network call", token, operations(calls))
			}
			s.requireLogs(t)
		}
	})

	t.Run("a non-ephemeral id", func(t *testing.T) {
		s := newGTForkShim(t, map[string]string{"envList": authEnvList(false)})
		stdout, stderr, code := s.run(t, forkExports(true, true, true), "set-fork-gateway-token", authStaleEnvID)
		out := stdout + stderr
		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, out)
		}
		if e := errorLines(out); !strings.Contains(e, "is NOT ephemeral") || !strings.Contains(e, "GATEWAY_TOKEN") {
			t.Errorf("error lines %q do not say the environment is NOT ephemeral and that GATEWAY_TOKEN was not set", e)
		}
		if m := s.mutations(t); len(m) != 0 {
			t.Errorf("a non-ephemeral environment received mutations %v", m)
		}
		if ops := operations(s.calls(t)); !slices.Contains(ops, "envList") {
			t.Errorf("Railway calls = %v; the ephemeral check never listed environments", ops)
		}
	})
}

func TestSetForkGatewayToken_WritesOneFreshValueToTheEight(t *testing.T) {
	s := newGTForkShim(t, nil)
	out, code := runForkGT(t, s)
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, out)
	}
	value := requireWroteTheEight(t, s)
	if value == gtSourceToken {
		t.Error("the fork's GATEWAY_TOKEN equals the planted source value; it must be generated, never read from the source")
	}
	requireEachReRead(t, s)
	requireSkipDeploys(t, s)

	t.Run("a second run writes a different value", func(t *testing.T) {
		s2 := newGTForkShim(t, nil)
		if out, code := runForkGT(t, s2); code != 0 {
			t.Fatalf("exit %d, want 0; output = %q", code, out)
		}
		if v2 := requireWroteTheEight(t, s2); value != "" && v2 == value {
			t.Error("two runs wrote the same GATEWAY_TOKEN; it is not generated per run")
		}
	})

	t.Run(passSub+" writes one value per run, fresh every run", func(t *testing.T) {
		s := newPassShim(t, nil, nil)
		var tokens [2]string
		for i := range tokens {
			before := len(s.calls(t))
			if out, code := runPass(t, s); code != 0 {
				t.Fatalf("run %d: exit %d, want 0; output = %q", i+1, code, clip(out))
			}
			ws := collectionWritesIn(s.calls(t)[before:])
			for _, n := range gatewayTokenTargets(t) {
				v, ok := passWritten(ws, gtSvcID(n), "GATEWAY_TOKEN")
				if !ok || !hex64.MatchString(v) {
					t.Fatalf("run %d: %s.GATEWAY_TOKEN written as %q (present %t), want 64 lowercase hex characters", i+1, n, v, ok)
				}
				if tokens[i] == "" {
					tokens[i] = v
				} else if v != tokens[i] {
					t.Errorf("run %d: %s.GATEWAY_TOKEN differs from the first target's; one value goes to all eight", i+1, n)
				}
			}
		}
		if tokens[0] == gtSourceToken || tokens[1] == tokens[0] {
			t.Errorf("run 1 token equals the source or run 2's token; each run generates its own")
		}
	})
}

func TestSetForkGatewayToken_NeverPrintsTheValue(t *testing.T) {
	s := newGTForkShim(t, nil)
	out, code := runForkGT(t, s)
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, out)
	}
	value := requireWroteTheEight(t, s)
	var labels []string
	for _, n := range gatewayTokenTargets(t) {
		labels = append(labels, n+".GATEWAY_TOKEN")
	}
	requireRedacted(t, out, labels...)
	secretScan(t, s, out, map[string]string{"the generated gateway token": value, "the planted source token": gtSourceToken})
}

func TestSetForkGatewayToken_MissingServiceFails(t *testing.T) {
	runners := []struct {
		sub string
		mk  func(t *testing.T, settle string) authShim
	}{
		{"set-fork-gateway-token", func(t *testing.T, settle string) authShim {
			return newGTForkShim(t, map[string]string{"settle": settle})
		}},
		{passSub, func(t *testing.T, settle string) authShim {
			return newPassShim(t, nil, map[string]string{"settle": settle})
		}},
	}
	check := func(t *testing.T, s authShim, sub, service string) {
		t.Helper()
		stdout, stderr, code := s.run(t, forkExports(true, true, true), sub, authForkEnvID)
		out := stdout + stderr
		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, clip(out))
		}
		if !strings.Contains(errorLines(out), service) {
			t.Errorf("error lines %q do not name the service %s", errorLines(out), service)
		}
		if ups := s.upserts(t); len(ups) != 0 {
			t.Errorf("upserts = %v, want none: every service id resolves before the first write", names(ups))
		}
		if m := s.mutations(t); len(m) != 0 {
			t.Errorf("mutations = %v, want none", m)
		}
		if ops := operations(s.calls(t)); !slices.Contains(ops, "settle") {
			t.Errorf("Railway calls = %v; the services were never listed", ops)
		}
	}
	for _, r := range runners {
		for _, missing := range gatewayTokenTargets(t) {
			t.Run(r.sub+"/"+missing, func(t *testing.T) {
				check(t, r.mk(t, gtSettle(t, missing)), r.sub, missing)
			})
		}
		t.Run(r.sub+"/two gateway instances", func(t *testing.T) {
			dup := `{"node":{"serviceId":"svc-gateway-dup","serviceName":"gateway"}}`
			settle := strings.Replace(gtSettle(t), `"edges":[`, `"edges":[`+dup+`,`, 1)
			check(t, r.mk(t, settle), r.sub, "gateway")
		})
	}
}

func TestSetForkGatewayToken_ReReadMismatchFails(t *testing.T) {
	for _, svc := range gatewayTokenTargets(t) {
		for _, c := range []struct{ name, bend string }{
			{"differs", `.GATEWAY_TOKEN = "` + gtBentToken + `"`},
			{"absent", `del(.GATEWAY_TOKEN)`},
			{"empty", `.GATEWAY_TOKEN = ""`},
		} {
			if c.name != "differs" && svc != "invoice" {
				continue
			}
			t.Run(svc+" "+c.name, func(t *testing.T) {
				s := newGTForkShim(t, nil)
				s.bendRead(t, gtSvcID(svc), c.bend)
				out, code := runForkGT(t, s)
				if code != 1 {
					t.Fatalf("exit %d, want 1; output = %q", code, out)
				}
				if !strings.Contains(errorLines(out), svc+".GATEWAY_TOKEN") {
					t.Errorf("error lines %q do not name %s.GATEWAY_TOKEN", errorLines(out), svc)
				}
				written := oneUpsert(t, s.upserts(t), gtSvcID(svc), "GATEWAY_TOKEN")
				secretScan(t, s, out, map[string]string{"the generated gateway token": written, "the bent re-read": gtBentToken, "the planted source token": gtSourceToken})
			})
		}
	}
}

func TestGatewayTokenServicesMatchTheRoutedFleet(t *testing.T) {
	raw, err := os.ReadFile(railwayEnvScript(t))
	if err != nil {
		t.Fatal(err)
	}
	m := servicesInArray.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("railway-env.sh defines no GATEWAY_TOKEN_SERVICES=(...) array")
	}
	got := strings.Fields(m[1])
	want := gatewayTokenTargets(t)
	if len(got) == 0 {
		t.Fatal("GATEWAY_TOKEN_SERVICES is empty")
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("GATEWAY_TOKEN_SERVICES = %v, want gateway plus routedServices = %v (sorted, duplicates count)", got, want)
	}
}

func TestDevEnvYmlRunsSetForkGatewayTokenAfterForkAuth(t *testing.T) {
	devEnv := readWorkflow(t, "dev-env.yml")
	for _, f := range prOnlyPrepareEnvStepFaults(devEnv, "set-fork-gateway-token", forkGatewayTokenRunCmd) {
		t.Errorf(".github/workflows/dev-env.yml: %s", f)
	}

	const forkAuthCmd = `bash scripts/ci/railway-env.sh set-fork-auth "$ENV_ID"`
	auth, token := -1, -1
	for _, job := range workflowJobsOf(devEnv) {
		if job.name != "prepare-env" {
			continue
		}
		for _, s := range job.steps() {
			if slices.Contains(invocations(s.keys["run"], "set-fork-auth"), forkAuthCmd) {
				auth = s.index
			}
			if len(invocations(s.keys["run"], "set-fork-gateway-token")) > 0 {
				token = s.index
			}
		}
	}
	if auth < 0 {
		t.Fatal("control: no set-fork-auth step found in prepare-env; the scan is broken")
	}
	switch {
	case token < 0:
		t.Errorf("no set-fork-gateway-token step in prepare-env, want the step directly after the set-fork-auth step (step %d)", auth)
	case token != auth+1:
		t.Errorf("the set-fork-gateway-token step is prepare-env step %d, want %d: directly after the set-fork-auth step", token, auth+1)
	}
}

func TestSetProductionGatewayToken_RefusesAForkID(t *testing.T) {
	const usage = "usage: railway-env.sh set-production-gateway-token <environment-id>"

	t.Run("control: the persistent id is written", func(t *testing.T) {
		s := newGTProdShim(t, func(string) (string, bool) { return "", false })
		if out, code := runProdGT(t, s); code != 0 || len(s.mutations(t)) == 0 {
			t.Fatalf("control: exit %d and %d mutation(s), want exit 0 after at least one write; output = %q", code, len(s.mutations(t)), out)
		}
	})

	t.Run("a fork id", func(t *testing.T) {
		for _, token := range []bool{true, false} {
			s := newGTProdShim(t, func(string) (string, bool) { return "", false })
			stdout, stderr, code := s.run(t, forkExports(token, true, true), "set-production-gateway-token", authForkEnvID)
			out := stdout + stderr
			if code != 1 {
				t.Errorf("token=%t: exit %d, want 1; output = %q", token, code, out)
			}
			if e := errorLines(out); !strings.Contains(e, authForkEnvID) || !onlyThePersistentEnvironment.MatchString(e) {
				t.Errorf("token=%t: error lines %q do not name the refused id and say only the persistent environment is written", token, e)
			}
			if strings.Contains(out, "RAILWAY_API_TOKEN is not set") {
				t.Errorf("token=%t: require_env ran before the refusal; output = %q", token, out)
			}
			if calls := s.calls(t); len(calls) != 0 {
				t.Errorf("token=%t: the refusal called Railway %v", token, operations(calls))
			}
			s.requireLogs(t)
		}
	})

	t.Run("no id", func(t *testing.T) {
		s := newGTProdShim(t, func(string) (string, bool) { return "", false })
		stdout, stderr, code := s.run(t, "", "set-production-gateway-token")
		out := stdout + stderr
		if code != 2 || !strings.Contains(out, usage) {
			t.Errorf("exit %d and output %q, want exit 2 carrying %q (the generic usage also exits 2)", code, out, usage)
		}
		if calls := s.calls(t); len(calls) != 0 {
			t.Errorf("the usage guard called Railway %v", operations(calls))
		}
	})
}

func TestSetProductionGatewayToken_WritesWhenAllAbsent(t *testing.T) {
	absent := func(svc string) (string, bool) {
		if svc == "tenancy" || svc == "dashboard" {
			return "", true
		}
		return "", false
	}
	s := newGTProdShim(t, absent)
	out, code := runProdGT(t, s)
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, out)
	}
	value := requireWroteTheEight(t, s)
	requireEachReRead(t, s)
	requireSkipDeploys(t, s)
	var labels []string
	for _, n := range gatewayTokenTargets(t) {
		labels = append(labels, n+".GATEWAY_TOKEN")
	}
	requireRedacted(t, out, labels...)
	secretScan(t, s, out, map[string]string{"the generated gateway token": value})

	t.Run("a re-read that differs fails", func(t *testing.T) {
		s := newGTProdShim(t, absent)
		s.bendRead(t, gtSvcID("invoice"), `del(.GATEWAY_TOKEN)`)
		out, code := runProdGT(t, s)
		if code != 1 || !strings.Contains(errorLines(out), "invoice.GATEWAY_TOKEN") {
			t.Errorf("exit %d and error lines %q, want exit 1 naming invoice.GATEWAY_TOKEN", code, errorLines(out))
		}
	})
}

func TestSetProductionGatewayToken_NoOpWhenAllEqual(t *testing.T) {
	s := newGTProdShim(t, func(string) (string, bool) { return gtProdToken, true })
	out, code := runProdGT(t, s)
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, out)
	}
	if !alreadySet.MatchString(out) {
		t.Errorf("output does not say the token is already set; output = %q", out)
	}
	if ups := s.upserts(t); len(ups) != 0 {
		t.Errorf("upserts = %v, want none: eight equal values are left alone", names(ups))
	}
	if m := s.mutations(t); len(m) != 0 {
		t.Errorf("mutations = %v, want none", m)
	}
	reads := 0
	for _, c := range s.calls(t) {
		if strings.Contains(c.Query, "variables(projectId") {
			reads++
		}
	}
	if reads < 8 {
		t.Errorf("%d variable read(s), want at least 8: the no-op must read every target before it decides", reads)
	}
	secretScan(t, s, out, map[string]string{"the production gateway token": gtProdToken})
}

func TestSetProductionGatewayToken_RefusesAPartialOrMixedSet(t *testing.T) {
	cases := []struct {
		name      string
		token     func(svc string) (string, bool)
		offenders []string
	}{
		{"seven hold v, notifications absent", func(svc string) (string, bool) { return gtProdToken, svc != "notifications" }, []string{"notifications"}},
		{"six hold v, invoice and dashboard hold w", func(svc string) (string, bool) {
			if svc == "invoice" || svc == "dashboard" {
				return gtOtherToken, true
			}
			return gtProdToken, true
		}, []string{"invoice", "dashboard"}},
		{"seven hold v, validation is empty", func(svc string) (string, bool) {
			if svc == "validation" {
				return "", true
			}
			return gtProdToken, true
		}, []string{"validation"}},
		{"only gateway holds v", func(svc string) (string, bool) { return gtProdToken, svc == "gateway" },
			[]string{"tenancy", "portfolio", "invoice", "validation", "submission", "dashboard", "notifications"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newGTProdShim(t, c.token)
			out, code := runProdGT(t, s)
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			e := errorLines(out)
			for _, o := range c.offenders {
				if !strings.Contains(e, o) {
					t.Errorf("error lines %q do not name the offender %s", e, o)
				}
			}
			for _, n := range []string{"tenancy", "portfolio", "invoice", "validation", "submission", "dashboard", "notifications"} {
				if !slices.Contains(c.offenders, n) && strings.Contains(e, n) {
					t.Errorf("error lines %q name %s, which holds the value the others are compared with", e, n)
				}
			}
			if m := s.mutations(t); len(m) != 0 {
				t.Errorf("mutations = %v, want none: the refusal precedes every write", m)
			}
			if len(s.calls(t)) == 0 {
				t.Error("no Railway call was made, so the targets were never read")
			}
			for label, v := range map[string]string{"v": gtProdToken, "w": gtOtherToken} {
				if strings.Contains(out, v) {
					t.Errorf("the output carries the held value %s", label)
				}
			}
		})
	}
}
