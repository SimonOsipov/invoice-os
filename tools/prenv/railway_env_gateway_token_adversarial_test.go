// railway_env_gateway_token_adversarial_test.go: failure, edge and ordering coverage for the
// gateway token writes in railway-env.sh and dev-env.yml.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Built at runtime so no secret-shaped literal sits in source.
var gtGoodValue = strings.Repeat("ab12cd34ef56", 5) + "ab12"

// withOpenssl puts an openssl on PATH, ahead of the real one, that runs body.
func withOpenssl(t *testing.T, s authShim, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.dir, "openssl"), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// gtStored reads the GATEWAY_TOKEN the shim holds for svc ("" and false when absent).
func gtStored(t *testing.T, s authShim, svc string) (string, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.dir, "store-"+gtSvcID(svc)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var vars map[string]string
	if err := json.Unmarshal(raw, &vars); err != nil {
		t.Fatal(err)
	}
	v, ok := vars["GATEWAY_TOKEN"]
	return v, ok
}

func gtAbsent(string) (string, bool) { return "", false }

func TestSetGatewayToken_RefusesAnUnusableGeneratedValue(t *testing.T) {
	const wantsNothingWritten = "a bad generator must stop the command before any write"
	badGenerators := []struct{ name, body string }{
		{"openssl exits 1 with no output", `exit 1`},
		{"openssl is not found", `echo "openssl: not found" >&2; exit 127`},
		{"openssl prints nothing and exits 0", `exit 0`},
		{"openssl prints a short value", `echo abc123`},
		{"openssl prints 65 hex characters", `echo ` + gtGoodValue + `0`},
		{"openssl prints 64 non-hex characters", `echo ` + strings.Repeat("z", 64)},
		{"openssl prints uppercase hex", `echo ` + strings.ToUpper(gtGoodValue)},
		{"openssl prints a value then fails", `echo ` + gtGoodValue + `; exit 1`},
	}
	runners := []struct {
		name string
		make func() authShim
		run  func(*testing.T, authShim) (string, int)
	}{
		{"set-fork-gateway-token", func() authShim { return newGTForkShim(t, nil) }, runForkGT},
		{"set-production-gateway-token", func() authShim { return newGTProdShim(t, gtAbsent) }, runProdGT},
	}
	for _, r := range runners {
		t.Run(r.name+"/control: a well-formed generated value is written", func(t *testing.T) {
			s := r.make()
			withOpenssl(t, s, `echo `+gtGoodValue)
			out, code := r.run(t, s)
			if code != 0 {
				t.Fatalf("exit %d, want 0; output = %q", code, out)
			}
			if v := requireWroteTheEight(t, s); v != gtGoodValue {
				t.Errorf("wrote %q, want the value the openssl on PATH produced: the PATH shim is not in effect", v)
			}
		})
		for _, g := range badGenerators {
			t.Run(r.name+"/"+g.name, func(t *testing.T) {
				s := r.make()
				withOpenssl(t, s, g.body)
				out, code := r.run(t, s)
				if code == 0 {
					t.Errorf("exit 0, want non-zero; output = %q", out)
				}
				if m := s.mutations(t); len(m) != 0 {
					t.Errorf("mutations = %v: %s", m, wantsNothingWritten)
				}
				if ups := s.upserts(t); len(ups) != 0 {
					t.Errorf("upserts = %v, want none", names(ups))
				}
				if len(s.calls(t)) == 0 {
					t.Error("no Railway call was made, so the guards never ran and the case proves nothing")
				}
			})
		}
	}
}

func TestSetGatewayToken_TheValueIsOnNoProcessArgv(t *testing.T) {
	for _, r := range []struct {
		name string
		make func() authShim
		run  func(*testing.T, authShim) (string, int)
	}{
		{"set-fork-gateway-token", func() authShim { return newGTForkShim(t, nil) }, runForkGT},
		{"set-production-gateway-token", func() authShim { return newGTProdShim(t, gtAbsent) }, runProdGT},
	} {
		t.Run(r.name, func(t *testing.T) {
			s := r.make()
			withOpenssl(t, s, `echo `+gtGoodValue)
			jqLog := jqArgvLog(t, s)
			out, code := r.run(t, s)
			if code != 0 {
				t.Fatalf("exit %d, want 0; output = %q", code, out)
			}
			if v := requireWroteTheEight(t, s); v != gtGoodValue {
				t.Fatalf("control: wrote %q, want the planted openssl value", v)
			}
			raw, err := os.ReadFile(jqLog)
			if err != nil || len(raw) == 0 {
				t.Fatalf("control: the jq argv log is empty (%v), so a clean scan proves nothing", err)
			}
			if strings.Contains(string(raw), gtGoodValue) {
				t.Error("jq's argv carries the gateway token; it would show in ps")
			}
			secretScan(t, s, out, map[string]string{"the generated gateway token": gtGoodValue})
		})
	}
}

func TestSetForkGatewayToken_PartialWriteFailure(t *testing.T) {
	s := newGTForkShim(t, nil)
	// Three writes pass; the fourth gets a GraphQL error.
	writeFile(t, filepath.Join(s.dir, "faults-varCollectionUpsert"), "ok ok ok gqlerr")
	out, code := runForkGT(t, s)
	if code == 0 {
		t.Fatalf("exit 0 after a failed write, want non-zero; output = %q", out)
	}
	targets := gatewayTokenTargets(t)
	var fresh string
	for i, svc := range targets {
		v, ok := gtStored(t, s, svc)
		switch {
		case i < 3:
			if !ok || !hex64.MatchString(v) || v == gtSourceToken {
				t.Errorf("%s holds %q before the failure point, want the fresh value", svc, v)
			}
			if i == 0 {
				fresh = v
			} else if v != fresh {
				t.Errorf("%s holds a different value from %s", svc, targets[0])
			}
		default:
			if v != gtSourceToken {
				t.Errorf("%s was written after the failed write; the run must stop at the first failure", svc)
			}
		}
	}
	if m := s.mutations(t); len(m) != 4 {
		t.Errorf("mutations = %v, want exactly 4: three that landed and the one that failed", m)
	}
	if fresh == "" {
		t.Fatal("control: no fresh value found on the first target")
	}
	secretScan(t, s, out, map[string]string{"the generated gateway token": fresh, "the planted source token": gtSourceToken})
}

func TestSetProductionGatewayToken_PartialWriteFailureIsNotSilent(t *testing.T) {
	s := newGTProdShim(t, gtAbsent)
	writeFile(t, filepath.Join(s.dir, "faults-varCollectionUpsert"), "ok ok ok gqlerr")
	out, code := runProdGT(t, s)
	if code == 0 {
		t.Fatalf("exit 0 after a failed write, want non-zero; output = %q", out)
	}
	held := 0
	for _, svc := range gatewayTokenTargets(t) {
		if _, ok := gtStored(t, s, svc); ok {
			held++
		}
	}
	if held != 3 {
		t.Fatalf("%d services hold the token after the failure, want 3: the run must stop at the first failure", held)
	}
	// Spec D10: a partly-set group is refused, so the by-hand run cannot resume.
	before := len(s.mutations(t))
	out, code = runProdGT(t, s)
	if code != 1 || !strings.Contains(errorLines(out), "partly set") {
		t.Errorf("rerun exit %d and error lines %q, want exit 1 saying partly set", code, errorLines(out))
	}
	if after := len(s.mutations(t)); after != before {
		t.Errorf("the rerun made %d more mutation(s), want none", after-before)
	}
}

func TestSetProductionGatewayToken_UnreadableMapRefusesBeforeAnyWrite(t *testing.T) {
	for _, bad := range gatewayTokenTargets(t) {
		for _, c := range []struct {
			name  string
			token func(string) (string, bool)
		}{
			{"the rest absent", gtAbsent},
			{"the rest hold one value", func(string) (string, bool) { return gtProdToken, true }},
		} {
			t.Run(bad+" unreadable, "+c.name, func(t *testing.T) {
				s := newGTProdShim(t, c.token)
				s.bendRead(t, gtSvcID(bad), `"not an object"`)
				out, code := runProdGT(t, s)
				if code != 1 {
					t.Errorf("exit %d, want 1; output = %q", code, out)
				}
				if !regexp.MustCompile(`(?i)unreadable|not an object`).MatchString(errorLines(out)) {
					t.Errorf("error lines %q do not say the map is unreadable", errorLines(out))
				}
				if alreadySet.MatchString(out) {
					t.Errorf("output says already set while a target is unreadable; output = %q", out)
				}
				if m := s.mutations(t); len(m) != 0 {
					t.Errorf("mutations = %v, want none: an unreadable target is never evidence that it is unset", m)
				}
				if len(s.calls(t)) == 0 {
					t.Error("no Railway call was made")
				}
			})
		}
	}
}

func TestSetProductionGatewayToken_MixedSetsAreRefusedWhateverTheMajority(t *testing.T) {
	split := func(vOn ...string) func(string) (string, bool) {
		return func(svc string) (string, bool) {
			if slices.Contains(vOn, svc) {
				return gtProdToken, true
			}
			return gtOtherToken, true
		}
	}
	targets := gatewayTokenTargets(t)
	distinct := func(svc string) (string, bool) {
		return strings.Repeat(string(rune('a'+slices.Index(targets, svc))), 64), true
	}
	for _, c := range []struct {
		name  string
		token func(string) (string, bool)
	}{
		{"4 hold v and 4 hold w", split(targets[:4]...)},
		{"4 hold w and 4 hold v", split(targets[4:]...)},
		{"4 hold v and 4 are absent", func(svc string) (string, bool) { return gtProdToken, slices.Index(targets, svc) < 4 }},
		{"4 hold v and 4 are empty", func(svc string) (string, bool) {
			if slices.Index(targets, svc) < 4 {
				return gtProdToken, true
			}
			return "", true
		}},
		{"eight distinct values", distinct},
		{"only the last holds a value", func(svc string) (string, bool) { return gtProdToken, svc == targets[7] }},
		{"one differs only by a trailing newline", func(string) (string, bool) { return gtProdToken, true }},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newGTProdShim(t, c.token)
			if strings.Contains(c.name, "newline") {
				s.bendRead(t, gtSvcID("invoice"), `.GATEWAY_TOKEN = .GATEWAY_TOKEN + "\n"`)
			}
			out, code := runProdGT(t, s)
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			if m := s.mutations(t); len(m) != 0 {
				t.Errorf("mutations = %v, want none", m)
			}
			e := errorLines(out)
			named := 0
			for _, n := range targets {
				if strings.Contains(e, n) {
					named++
				}
			}
			if named == 0 || named == len(targets) {
				t.Errorf("error lines %q name %d of %d services, want some but not all of them", e, named, len(targets))
			}
			if strings.Contains(c.name, "newline") && !strings.Contains(e, "invoice") {
				t.Errorf("error lines %q do not name invoice, whose value differs by a trailing newline", e)
			}
			for _, v := range []string{gtProdToken, gtOtherToken} {
				if strings.Contains(out, v) {
					t.Errorf("the output carries a held value")
				}
			}
		})
	}
}

func TestSetProductionGatewayToken_ASecondRunAfterAWriteIsANoOp(t *testing.T) {
	s := newGTProdShim(t, gtAbsent)
	if out, code := runProdGT(t, s); code != 0 {
		t.Fatalf("first run: exit %d, want 0; output = %q", code, out)
	}
	value := requireWroteTheEight(t, s)
	before := len(s.mutations(t))
	out, code := runProdGT(t, s)
	if code != 0 || !alreadySet.MatchString(out) {
		t.Errorf("second run: exit %d and output %q, want exit 0 saying already set", code, out)
	}
	if after := len(s.mutations(t)); after != before {
		t.Errorf("the second run made %d more mutation(s), want none", after-before)
	}
	for _, svc := range gatewayTokenTargets(t) {
		if v, _ := gtStored(t, s, svc); v != value {
			t.Errorf("%s no longer holds the first run's value", svc)
		}
	}
}

func TestAuditSealed_GatewayTokenMustStayUnsealed(t *testing.T) {
	var plain []string
	for _, svc := range []string{sealedGatewayID, sealedAuthID, sealedAdminID} {
		plain = append(plain, plainOn("GATEWAY_TOKEN", svc))
	}
	s := newSealedShim(t, sourceInstances(), plain...)
	stdout, out, code := runAudit(t, s)
	requireAllowed(t, stdout, out, code)
	if !strings.Contains(stdout, "clean") {
		t.Errorf("stdout %q has no clean line", stdout)
	}

	t.Run("a sealed GATEWAY_TOKEN fails the audit, on auth too", func(t *testing.T) {
		s := newSealedShim(t, sourceInstances(), sealedOn("GATEWAY_TOKEN", sealedGatewayID), sealedOn("GATEWAY_TOKEN", sealedAuthID))
		_, out, code := runAudit(t, s)
		requireRefused(t, out, code, "GATEWAY_TOKEN@"+sealedGatewayID, "GATEWAY_TOKEN@"+sealedAuthID)
	})
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

func TestEveryGatewayTokenServiceIsRedeployedAfterTheForkWrite(t *testing.T) {
	jobs := workflowJobsOf(readWorkflow(t, "dev-env.yml"))
	deployed := map[string]string{}
	deployJobs := 0
	for _, j := range jobs {
		var runs string
		for _, s := range j.steps() {
			runs += s.keys["run"] + "\n"
		}
		ups := invocations(runs, "railway-up-ci.sh")
		if len(ups) == 0 {
			continue
		}
		deployJobs++
		if !slices.Contains(jobList(j, "needs"), "prepare-env") {
			t.Errorf("job %s deploys a service but does not need prepare-env, so it can run before the token is written; needs = %v", j.name, jobList(j, "needs"))
		}
		for _, inv := range ups {
			svc := strings.TrimSpace(inv[strings.Index(inv, "railway-up-ci.sh")+len("railway-up-ci.sh"):])
			if svc == "${{ matrix.service }}" {
				for _, m := range jobList(j, "service") {
					deployed[m] = j.name
				}
				continue
			}
			deployed[svc] = j.name
		}
	}
	if deployJobs == 0 {
		t.Fatal("control: no job runs railway-up-ci.sh; the scan is broken")
	}
	for _, svc := range gatewayTokenTargets(t) {
		if deployed[svc] == "" {
			t.Errorf("%s holds GATEWAY_TOKEN but no job deploys it after prepare-env; a stale deployment would keep the old token", svc)
		}
	}
}

func TestDeployGatewayRequiresPrepareEnvSuccess(t *testing.T) {
	for _, j := range workflowJobsOf(readWorkflow(t, "dev-env.yml")) {
		if j.name != "deploy-gateway" {
			continue
		}
		text := strings.Join(j.lines, "\n")
		// `!cancelled()` runs the job after a failed need; only this clause stops a deploy after a failed token write.
		if !strings.Contains(text, "needs.prepare-env.result == 'success'") {
			t.Error("deploy-gateway does not require needs.prepare-env.result == 'success', so a failed fork token write would not stop the deploy")
		}
		return
	}
	t.Fatal("no deploy-gateway job found")
}
