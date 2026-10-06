// railway_env_pass_test.go drives fork-vars-before-urls, and the one-contributor subcommands, as passes:
// one environment list, one service list, one batched read, one batched write, one batched re-read.
package main

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	passSub            = "fork-vars-before-urls"
	passTraceID        = "7992771584715554281"
	passRailwayMessage = "PLANTED-RAILWAY-MESSAGE-TEXT"
	passEchoNeedle     = "PLANTED-ECHOED-INPUT-VALUE"
	passAuthConfirmed  = "Fork auth configuration confirmed in environment " + authForkEnvID
	passTokenConfirmed = "GATEWAY_TOKEN confirmed on 8 services in environment " + authForkEnvID
)

var passSecretNames = []string{"GOTRUE_JWT_KEYS", "GOTRUE_JWT_SECRET", "AUTH_ADMIN_PASSWORD", "GATEWAY_TOKEN"}

// clip keeps a failure message short: the usage line of an unknown subcommand is long.
func clip(s string) string {
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}

// requireWaitNamed fails unless the error lines match every pattern (`429`, the wait, `600`, `second`): the
// wording TestRailwayAPI_RateLimitOverSixHundredFailsAtOnce pins for graphql_post callers.
func requireWaitNamed(t *testing.T, out string, patterns ...string) {
	t.Helper()
	e := errorLines(out)
	for _, re := range patterns {
		if !regexp.MustCompile(re).MatchString(e) {
			t.Errorf("error lines do not match %s: %q", re, e)
		}
	}
	if strings.Contains(out, passEchoNeedle) {
		t.Errorf("the output carries Railway's message; output = %q", clip(out))
	}
}

func lastOp(ops []string) string {
	if len(ops) == 0 {
		return ""
	}
	return ops[len(ops)-1]
}

func passLabel(id string) string { return strings.TrimSuffix(strings.TrimPrefix(id, "svc-"), "-gt") }

// passServices is auth plus the eight GATEWAY_TOKEN services: nine distinct names, gateway once.
func passServices(t *testing.T) []string {
	t.Helper()
	return append([]string{"auth"}, gatewayTokenTargets(t)...)
}

func passServiceIDs(t *testing.T) []string {
	t.Helper()
	var ids []string
	for _, n := range passServices(t) {
		ids = append(ids, gtSvcID(n))
	}
	return ids
}

// passAuthInherited is auth as a fork inherits it from production.
func passAuthInherited(t *testing.T) map[string]string {
	t.Helper()
	return maps.Clone(forkAuthStores(freshJWK(t))[authForkAuthID])
}

// passStores seeds auth with `auth` and the eight targets with the source GATEWAY_TOKEN; gateway also
// inherits production's auth settings.
func passStores(t *testing.T, auth map[string]string) map[string]map[string]string {
	t.Helper()
	stores := gtStores(t, authForkName, func(string) (string, bool) { return gtSourceToken, true })
	stores[gtSvcID("auth")] = auth
	maps.Copy(stores[gtSvcID("gateway")], forkAuthStores("")[authForkGatewayID])
	return stores
}

// newPassShim is a fork with auth and the eight GATEWAY_TOKEN services; resp overrides envList and settle.
func newPassShim(t *testing.T, auth map[string]string, resp map[string]string) authShim {
	t.Helper()
	if auth == nil {
		auth = passAuthInherited(t)
	}
	if resp == nil {
		resp = map[string]string{}
	}
	if resp["envList"] == "" {
		resp["envList"] = authEnvList(true)
	}
	if resp["settle"] == "" {
		resp["settle"] = gtSettle(t)
	}
	return newAuthShim(t, resp, passStores(t, auth))
}

func runPass(t *testing.T, s authShim) (out string, code int) {
	t.Helper()
	stdout, stderr, code := s.run(t, forkAuthExports(), passSub, authForkEnvID)
	return stdout + stderr, code
}

func (s authShim) bendReRead(t *testing.T, svcID, filter string) {
	t.Helper()
	writeFile(t, filepath.Join(s.dir, "reread-"+svcID+".jq"), filter)
}

// plantReadBad makes svcID's alias bad in the first read ("first") or the re-read ("after"):
// mode error, missing, string or null.
func (s authShim) plantReadBad(t *testing.T, phase, svcID, mode string) {
	t.Helper()
	writeFile(t, filepath.Join(s.dir, "readbad-"+phase+"-"+svcID), mode)
}

// failAlias makes svcID's alias fail inside a varsWrite, in mode abort or null.
func (s authShim) failAlias(t *testing.T, svcID, mode string) {
	t.Helper()
	writeFile(t, filepath.Join(s.dir, "aliasfail-"+svcID), "1")
	writeFile(t, filepath.Join(s.dir, "aliasfail.mode"), mode)
}

// plantWriteReply answers every varsWrite with body, as a 200, and applies nothing.
func (s authShim) plantWriteReply(t *testing.T, body string) {
	t.Helper()
	writeFile(t, filepath.Join(s.dir, "upsert-GOTRUE_JWT_SECRET.json"), body)
}

// writeErrors is a 200 varsWrite reply with one error per path field (`"path":[...],` or empty): the
// planted Railway message, a real failure's code, and passTraceID on the first error and traceOf(i) on the rest.
func writeErrors(paths ...string) string {
	var errs []string
	for i, p := range paths {
		errs = append(errs, `{"message":"`+passRailwayMessage+` Variable \"$i0\" got invalid value \"`+passEchoNeedle+`\"",`+p+`"extensions":{"code":"INTERNAL_SERVER_ERROR","traceId":"`+traceOf(i)+`"}}`)
	}
	return `{"data":null,"errors":[` + strings.Join(errs, ",") + `]}`
}

func traceOf(i int) string {
	if i == 0 {
		return passTraceID
	}
	return fmt.Sprintf("TRACE-%d", i)
}

func callsOf(s authShim, t *testing.T, op string) []railwayCall {
	t.Helper()
	var out []railwayCall
	for i, o := range operations(s.calls(t)) {
		if o == op {
			out = append(out, s.calls(t)[i])
		}
	}
	return out
}

// passWrites lists every per-service input of every varsWrite, in call and alias order.
func passWrites(t *testing.T, s authShim) []collectionWrite {
	t.Helper()
	return collectionWritesIn(callsOf(s, t, "varsWrite"))
}

func passWritten(ws []collectionWrite, svcID, name string) (string, bool) {
	for _, w := range writesTo(ws, svcID) {
		if v, ok := w.Vars[name]; ok {
			return v, true
		}
	}
	return "", false
}

// confirmedLines are the lines that confirm a value; "not confirmed" is a failure line.
func confirmedLines(out string) []string {
	var got []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "confirmed") && !strings.Contains(l, "not confirmed") {
			got = append(got, l)
		}
	}
	return got
}

func wordIn(line, word string) bool {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`).MatchString(line)
}

// requireNamedIn fails unless one line of text carries every word.
func requireNamedIn(t *testing.T, text string, words ...string) string {
	t.Helper()
	for _, l := range strings.Split(text, "\n") {
		all := true
		for _, w := range words {
			if !strings.Contains(l, w) {
				all = false
			}
		}
		if all {
			return l
		}
	}
	t.Errorf("no single line carries all of %q; lines = %q", words, text)
	return ""
}

func TestForkVarsBeforeURLs_FreshForkMakesFiveCalls(t *testing.T) {
	s := newPassShim(t, nil, nil)
	out, code := runPass(t, s)
	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, clip(out))
	}
	want := []string{"envList", "settle", "varsRead", "varsWrite", "varsRead"}
	if got := operations(s.calls(t)); !slices.Equal(got, want) {
		t.Errorf("Railway calls = %v, want %v", got, want)
	}
	for _, l := range []string{passAuthConfirmed, passTokenConfirmed} {
		if !strings.Contains(out, l) {
			t.Errorf("no %q line; output = %q", l, clip(out))
		}
	}
}

func TestForkPass_OneGuardReadPerPassWhateverTheContributorCount(t *testing.T) {
	for _, c := range []struct {
		sub  string
		shim func(t *testing.T) authShim
	}{
		{"set-fork-auth", func(t *testing.T) authShim { return newForkAuthShim(t, freshJWK(t)) }},
		{"set-fork-gateway-token", func(t *testing.T) authShim { return newGTForkShim(t, nil) }},
		{passSub, func(t *testing.T) authShim { return newPassShim(t, nil, nil) }},
	} {
		t.Run(c.sub, func(t *testing.T) {
			s := c.shim(t)
			stdout, stderr, code := s.run(t, forkAuthExports(), c.sub, authForkEnvID)
			if code != 0 {
				t.Fatalf("exit %d, want 0; output = %q", code, clip(stdout+stderr))
			}
			for _, op := range []string{"envList", "settle"} {
				if n := opCount(t, s, op); n != 1 {
					t.Errorf("%s calls = %d, want exactly 1 whatever the contributor count; Railway calls = %v", op, n, operations(s.calls(t)))
				}
			}
		})
	}
}

func TestForkVarsBeforeURLs_ReadCoversTheNineServicesOnce(t *testing.T) {
	s := newPassShim(t, nil, nil)
	if out, code := runPass(t, s); code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, clip(out))
	}
	reads := callsOf(s, t, "varsRead")
	if len(reads) != 2 {
		t.Fatalf("%d varsRead calls, want 2: the read and the re-read", len(reads))
	}
	want := passServiceIDs(t)
	if len(want) != 9 {
		t.Fatalf("control: the expected service set has %d ids, want 9", len(want))
	}
	for i, c := range reads {
		got := readServices(c)
		if len(got) != 9 || !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
			t.Errorf("varsRead %d asks for %v, want each of %v exactly once", i+1, got, want)
		}
		if !regexp.MustCompile(`unrendered:\s*true`).MatchString(c.Query) {
			t.Errorf("varsRead %d omits unrendered: true, so DATABASE_URL would read back rendered: %q", i+1, c.Query)
		}
	}
}

func TestForkVarsBeforeURLs_WritesOnlyTheNamesThatDiffer(t *testing.T) {
	auth := passAuthInherited(t)
	delete(auth, "API_EXTERNAL_URL")
	auth["PORT"] = "8080"
	auth["DATABASE_URL"] = authDSNReference
	s := newPassShim(t, auth, nil)
	out, code := runPass(t, s)
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, clip(out))
	}
	ws := writesTo(passWrites(t, s), gtSvcID("auth"))
	if len(ws) != 1 {
		t.Fatalf("auth has %d write input(s), want 1", len(ws))
	}
	for _, held := range []string{"PORT", "DATABASE_URL"} {
		if _, ok := ws[0].Vars[held]; ok {
			t.Errorf("auth's write carries %s, which auth already holds", held)
		}
	}
	for _, differs := range []string{"GOTRUE_JWT_ISSUER", "GOTRUE_JWT_KEYS", "GOTRUE_JWT_SECRET"} {
		if _, ok := ws[0].Vars[differs]; !ok {
			t.Errorf("auth's write lacks %s, which differs; write names = %v", differs, slices.Sorted(maps.Keys(ws[0].Vars)))
		}
	}
	if got := heldLines(out)["auth"]; got != "2 of 10" {
		t.Errorf("auth's held line = %q, want \"2 of 10\"; held lines = %v", got, heldLines(out))
	}
}

func TestForkVarsBeforeURLs_AbsentNameDiffersFromEmpty(t *testing.T) {
	for _, c := range []struct {
		name   string
		hold   bool
		wantIn bool
	}{
		{"absent is written as an empty string", false, true},
		{"control: a name that already holds the empty string is not written", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			auth := passAuthInherited(t)
			delete(auth, "GOTRUE_SMTP_HOST")
			if c.hold {
				auth["GOTRUE_SMTP_HOST"] = ""
			}
			s := newPassShim(t, auth, nil)
			out, code := runPass(t, s)
			if code != 0 {
				t.Fatalf("exit %d, want 0; output = %q", code, clip(out))
			}
			ws := writesTo(passWrites(t, s), gtSvcID("auth"))
			if len(ws) != 1 {
				t.Fatalf("auth has %d write input(s), want 1", len(ws))
			}
			v, ok := ws[0].Vars["GOTRUE_SMTP_HOST"]
			if ok != c.wantIn || (ok && v != "") {
				t.Errorf("auth's write carries GOTRUE_SMTP_HOST = %q (present %t), want present %t and \"\"", v, ok, c.wantIn)
			}
		})
	}
}

// The first read passes (auth holds PORT) and PORT is not written, so only a verdict fed the
// re-read can see the bend.
func TestForkVarsBeforeURLs_VerdictsReadTheReRead(t *testing.T) {
	auth := passAuthInherited(t)
	auth["PORT"] = "8080"
	t.Run("control: the same run without the bend confirms", func(t *testing.T) {
		s := newPassShim(t, maps.Clone(auth), nil)
		out, code := runPass(t, s)
		if code != 0 || !strings.Contains(out, passAuthConfirmed) {
			t.Errorf("exit %d and output %q, want exit 0 with %q", code, clip(out), passAuthConfirmed)
		}
	})
	t.Run("a re-read that drops PORT fails", func(t *testing.T) {
		s := newPassShim(t, maps.Clone(auth), nil)
		s.bendReRead(t, gtSvcID("auth"), `del(.PORT)`)
		out, code := runPass(t, s)
		if _, ok := passWritten(passWrites(t, s), gtSvcID("auth"), "PORT"); ok {
			t.Fatal("control: PORT was written, so the first read did not pass and the failure is not a re-read failure")
		}
		if len(passWrites(t, s)) == 0 {
			t.Fatal("control: nothing was written, so no re-read ran")
		}
		if code != 1 || !strings.Contains(errorLines(out), "auth.PORT") {
			t.Errorf("exit %d and error lines %q, want exit 1 naming auth.PORT", code, errorLines(out))
		}
		if strings.Contains(out, passAuthConfirmed) {
			t.Errorf("a failed verdict printed the fork_auth confirmation; output = %q", clip(out))
		}
	})
}

// Every bend below acts on a read sent after the write, on a name the pass writes (or, for the
// gateway pair, one that differs), so only a verdict fed the re-read can see it.
func TestForkVarsBeforeURLs_ReReadDropsTheGatewayToken(t *testing.T) {
	const bent = "PLANTED-BENT-REREAD-VALUE"
	other := freshJWK(t)
	t.Run("control: no bend confirms both contributors", func(t *testing.T) {
		out, code := runPass(t, newPassShim(t, nil, nil))
		if code != 0 || !strings.Contains(out, passAuthConfirmed) || !strings.Contains(out, passTokenConfirmed) {
			t.Errorf("exit %d and output %q, want exit 0 with both confirmation lines", code, clip(out))
		}
	})
	for _, c := range []struct {
		name, svc, filter, names string
		needle, confirm          string // a value the output must not carry; the line it must not print
	}{
		{"tenancy drops its token", "tenancy", `del(.GATEWAY_TOKEN)`, "tenancy.GATEWAY_TOKEN", "", passTokenConfirmed},
		{"tenancy reads another token", "tenancy", `.GATEWAY_TOKEN = "` + bent + `"`, "tenancy.GATEWAY_TOKEN", bent, passTokenConfirmed},
		{"notifications reads an empty token", "notifications", `.GATEWAY_TOKEN = ""`, "notifications.GATEWAY_TOKEN", "", passTokenConfirmed},
		{"auth reads another JWT secret", "auth", `.GOTRUE_JWT_SECRET = "` + bent + `"`, "auth.GOTRUE_JWT_SECRET", bent, passAuthConfirmed},
		{"auth reads another valid key", "auth", `.GOTRUE_JWT_KEYS = ` + jqString(t, other), "auth.GOTRUE_JWT_KEYS", jwkPrivateScalar(t, other), passAuthConfirmed},
		{"gateway reads another admin password", "gateway", `.AUTH_ADMIN_PASSWORD = "` + bent + `"`, "gateway.AUTH_ADMIN_PASSWORD", bent, passAuthConfirmed},
		{"gateway reads another AUTH_URL", "gateway", `.AUTH_URL = "http://` + bent + `"`, "gateway.AUTH_URL", "", passAuthConfirmed},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newPassShim(t, nil, nil)
			s.bendReRead(t, gtSvcID(c.svc), c.filter)
			out, code := runPass(t, s)
			if len(passWrites(t, s)) == 0 {
				t.Fatal("control: nothing was written, so no re-read ran")
			}
			if code != 1 || !strings.Contains(errorLines(out), c.names) {
				t.Errorf("exit %d and error lines %q, want exit 1 naming %s", code, errorLines(out), c.names)
			}
			if c.needle != "" && strings.Contains(out, c.needle) {
				t.Errorf("the output carries %q, a value the re-read returned", c.needle)
			}
			for _, name := range passSecretNames {
				for _, w := range passWrites(t, s) {
					if v, ok := w.Vars[name]; ok && v != "" && strings.Contains(out, v) {
						t.Errorf("the output carries the written %s.%s", passLabel(w.Service), name)
					}
				}
			}
			if strings.Contains(out, c.confirm) {
				t.Errorf("a failed verdict printed %q; output = %q", c.confirm, clip(out))
			}
		})
	}
}

func TestForkVarsBeforeURLs_EveryContributorReportsItsFailure(t *testing.T) {
	s := newPassShim(t, nil, nil)
	s.bendReRead(t, gtSvcID("auth"), `.GOTRUE_JWT_SECRET = "PLANTED-BENT-REREAD-VALUE"`)
	s.bendReRead(t, gtSvcID("tenancy"), `del(.GATEWAY_TOKEN)`)
	out, code := runPass(t, s)
	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, clip(out))
	}
	for _, name := range []string{"auth.GOTRUE_JWT_SECRET", "tenancy.GATEWAY_TOKEN"} {
		if !strings.Contains(errorLines(out), name) {
			t.Errorf("error lines %q do not name %s: a failed contributor must not hide the next one's verdicts", errorLines(out), name)
		}
	}
	for _, l := range []string{passAuthConfirmed, passTokenConfirmed} {
		if strings.Contains(out, l) {
			t.Errorf("a failed contributor printed %q", l)
		}
	}
}

func TestForkPass_FailedReadsFailBeforeAnyWrite(t *testing.T) {
	envOnly := func(t *testing.T, s authShim) {
		t.Helper()
		if ops := operations(s.calls(t)); len(ops) == 0 || ops[0] != "envList" {
			t.Errorf("Railway calls = %v, want a run that reaches envList", ops)
		}
	}
	for _, c := range []struct {
		name  string
		plant func(t *testing.T, s authShim)
		names string // the service or environment the error must name
		says  string
	}{
		{"a GraphQL error on one alias names its service", func(t *testing.T, s authShim) { s.plantReadBad(t, "first", gtSvcID("tenancy"), "error") }, "tenancy", ""},
		{"an alias missing from data is unreadable", func(t *testing.T, s authShim) { s.plantReadBad(t, "first", gtSvcID("invoice"), "missing") }, "invoice", "unreadable"},
		{"an alias that is a string is unreadable", func(t *testing.T, s authShim) { s.plantReadBad(t, "first", gtSvcID("dashboard"), "string") }, "dashboard", "unreadable"},
		{"HTTP 400 on the read names the environment and not Railway's message", func(t *testing.T, s authShim) {
			setFaults(t, s, "varsRead", "400")
			writeFile(t, filepath.Join(s.dir, "faultbody-varsRead"), `{"errors":[{"message":"`+passEchoNeedle+`","extensions":{"code":"BAD_USER_INPUT"}}]}`)
		}, authForkEnvID, "named no service"},
		{"a 429 over 600 s on the first read names the wait", func(t *testing.T, s authShim) {
			setFaults(t, s, "varsRead", "429")
			writeFile(t, filepath.Join(s.dir, "hdr.txt"), "HTTP/2 429\r\nretry-after: 601\r\n\r\n")
			writeFile(t, filepath.Join(s.dir, "faultbody-varsRead"), `{"errors":[{"message":"`+passEchoNeedle+`"}]}`)
		}, "429", "601|600"},
		{"a second 429 on the first read names the wait", func(t *testing.T, s authShim) {
			setFaults(t, s, "varsRead", "429", "429")
			writeFile(t, filepath.Join(s.dir, "hdr.txt"), "HTTP/2 429\r\nretry-after: 30\r\n\r\n")
			writeFile(t, filepath.Join(s.dir, "faultbody-varsRead"), `{"errors":[{"message":"`+passEchoNeedle+`"}]}`)
		}, "429", "second|30"},
		{"a 429 with no wait given on the first read names it", func(t *testing.T, s authShim) {
			setFaults(t, s, "varsRead", "429")
			plantRateLimitHeaders(t, s)
			writeFile(t, filepath.Join(s.dir, "faultbody-varsRead"), `{"errors":[{"message":"`+passEchoNeedle+`"}]}`)
		}, "429", "gave no wait"},
		{"a settle with two auth instances", func(t *testing.T, s authShim) {
			dup := `{"node":{"serviceId":"svc-auth-dup","serviceName":"auth"}}`
			writeFile(t, filepath.Join(s.dir, "settle.json"), strings.Replace(gtSettle(t), `"edges":[`, `"edges":[`+dup+`,`, 1))
		}, "'auth'", "Refusing to guess"},
		{"a settle with no auth instance", func(t *testing.T, s authShim) {
			writeFile(t, filepath.Join(s.dir, "settle.json"), gtSettle(t, "auth"))
		}, "'auth'", "None of the"},
		{"a failed settle names the environment", func(t *testing.T, s authShim) { setFaults(t, s, "settle", "gqlerr") }, authForkEnvID, ""},
		{"a failed envList names the environment", func(t *testing.T, s authShim) { setFaults(t, s, "envList", "gqlerr") }, "environment", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newPassShim(t, nil, nil)
			c.plant(t, s)
			out, code := runPass(t, s)
			envOnly(t, s)
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, clip(out))
			}
			if n := opCount(t, s, "varsWrite"); n != 0 {
				t.Errorf("%d varsWrite call(s), want 0: an unreadable read must stop the pass before any write", n)
			}
			if !strings.Contains(errorLines(out), c.names) {
				t.Errorf("error lines %q do not name %q", errorLines(out), c.names)
			}
			for _, w := range strings.Split(c.says, "|") {
				if w != "" && !strings.Contains(errorLines(out), w) {
					t.Errorf("error lines %q do not say %q", errorLines(out), w)
				}
			}
			if got := confirmedLines(out); len(got) != 0 {
				t.Errorf("a failed read printed confirmation lines %q", got)
			}
			if strings.Contains(out, passEchoNeedle) {
				t.Errorf("the output carries Railway's message; output = %q", clip(out))
			}
		})
	}
	t.Run("a 429 over the job's 600 s total on the first read names the total", func(t *testing.T) {
		s := newPassShim(t, nil, nil)
		setFaults(t, s, "varsRead", "429")
		plantRateLimitHeaders(t, s, "retry-after: 30")
		writeFile(t, filepath.Join(s.dir, "faultbody-varsRead"), `{"errors":[{"message":"`+passEchoNeedle+`"}]}`)
		tmp := t.TempDir()
		writeFile(t, filepath.Join(tmp, "railway-api-429-waited"), "590\n")
		stdout, stderr, code := s.run(t, forkAuthExports()+runnerTempExport(tmp), passSub, authForkEnvID)
		out := stdout + stderr
		if code != 1 || opCount(t, s, "varsWrite") != 0 {
			t.Errorf("exit %d with %d varsWrite call(s), want exit 1 and none; output = %q", code, opCount(t, s, "varsWrite"), clip(out))
		}
		requireWaitNamed(t, out, `429`, `\b590\b`, `\b600\b`)
	})
	t.Run("control: no fault writes once", func(t *testing.T) {
		s := newPassShim(t, nil, nil)
		if out, code := runPass(t, s); code != 0 || opCount(t, s, "varsWrite") != 1 {
			t.Errorf("exit %d with %d varsWrite call(s), want exit 0 and 1; output = %q", code, opCount(t, s, "varsWrite"), clip(out))
		}
	})
}

func TestForkPass_FailedReReadConfirmsNothing(t *testing.T) {
	for _, c := range []struct {
		name   string
		plant  func(t *testing.T, s authShim)
		names  string
		sayNot string
	}{
		{"the re-read answers a GraphQL error on one alias", func(t *testing.T, s authShim) { s.plantReadBad(t, "after", gtSvcID("submission"), "error") }, "submission", "written but not confirmed"},
		{"the re-read lacks one alias", func(t *testing.T, s authShim) { s.plantReadBad(t, "after", gtSvcID("portfolio"), "missing") }, "portfolio", "written but not confirmed"},
		{"the re-read answers HTTP 400", func(t *testing.T, s authShim) {
			setFaults(t, s, "varsRead", "ok", "400")
			writeFile(t, filepath.Join(s.dir, "faultbody-varsRead"), `{"errors":[{"message":"`+passEchoNeedle+`","extensions":{"code":"BAD_USER_INPUT"}}]}`)
		}, authForkEnvID, "written but not confirmed"},
		{"the re-read answers a GraphQL error with no alias", func(t *testing.T, s authShim) { setFaults(t, s, "varsRead", "ok", "gqlerr") }, authForkEnvID, "written but not confirmed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newPassShim(t, nil, nil)
			c.plant(t, s)
			out, code := runPass(t, s)
			if n := opCount(t, s, "varsWrite"); n != 1 {
				t.Errorf("%d varsWrite call(s), want 1: the write landed and only the re-read failed", n)
			}
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, clip(out))
			}
			errs := errorLines(out)
			if !strings.Contains(errs, c.sayNot) || !strings.Contains(errs, c.names) {
				t.Errorf("error lines %q do not say %q and name %q", errs, c.sayNot, c.names)
			}
			if got := confirmedLines(out); len(got) != 0 {
				t.Errorf("an unconfirmed write printed confirmation lines %q", got)
			}
			if strings.Contains(out, passEchoNeedle) {
				t.Errorf("the output carries Railway's message; output = %q", clip(out))
			}
		})
	}
}

// passHeldStores is a fork that already holds every config pair of auth and gateway. Auth holds key
// as GOTRUE_JWT_KEYS; with allHeld, every generated secret reads as gtGoodValue on every service.
func passHeldStores(t *testing.T, key string, allHeld bool) map[string]map[string]string {
	t.Helper()
	auth := map[string]string{
		"RAILWAY_ENVIRONMENT_NAME":  authForkName,
		"GOTRUE_JWT_ISSUER":         authForkIssuer,
		"DATABASE_URL":              authDSNReference,
		"API_EXTERNAL_URL":          authInternalURL,
		"PORT":                      "8080",
		"GOTRUE_DISABLE_SIGNUP":     "false",
		"GOTRUE_SMTP_HOST":          "",
		"GOTRUE_SMTP_PASS":          "",
		"GOTRUE_MAILER_AUTOCONFIRM": "true",
		"GOTRUE_JWT_KEYS":           key,
		"GOTRUE_JWT_SECRET":         authSourceJWTSecret,
	}
	stores := passStores(t, auth)
	gw := stores[gtSvcID("gateway")]
	gw["AUTH_ISSUER"], gw["AUTH_JWKS_URL"], gw["AUTH_URL"] = authMockIssuer, authLoopbackJWKS, authInternalURL
	gw["AUTH_ADDITIONAL_ISSUERS"] = `[{"issuer":"` + authForkIssuer + `","jwks_url":"` + authJWKSURL + `"}]`
	stores[gtSvcID("tenancy")]["GATEWAY_TOKEN"] = gtGoodValue
	if allHeld {
		auth["GOTRUE_JWT_SECRET"] = gtGoodValue
		gw["AUTH_ADMIN_PASSWORD"] = gtGoodValue
		for _, n := range gatewayTokenTargets(t) {
			stores[gtSvcID(n)]["GATEWAY_TOKEN"] = gtGoodValue
		}
	}
	return stores
}

// withFixedKey makes `prenv jwk-es256` print key and leaves every other prenv command real.
func withFixedKey(t *testing.T, s authShim, key string) {
	t.Helper()
	fake := "#!/bin/sh\nif [ \"$1\" = jwk-es256 ]; then printf '%s' '" + key + "'; exit 0; fi\nexec '" + binPath + "' \"$@\"\n"
	goShim := "#!/bin/sh\nout=''\nwhile [ $# -gt 0 ]; do [ \"$1\" = -o ] && out=\"$2\"; shift; done\n" +
		"cat > \"$out\" <<'FAKE'\n" + fake + "FAKE\nchmod +x \"$out\"\n"
	writeFile(t, filepath.Join(s.dir, "go"), goShim)
	if err := os.Chmod(filepath.Join(s.dir, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// Under /bin/bash (3.2 on macOS) an empty array expansion is unbound under set -u.
func TestForkPass_AServiceWithNothingToWriteHasNoAlias(t *testing.T) {
	const bash = "/bin/bash"
	if _, err := os.Stat(bash); err != nil {
		t.Skip("no /bin/bash")
	}
	if v, err := exec.Command(bash, "-c", "echo $BASH_VERSINFO").Output(); err == nil && strings.TrimSpace(string(v)) != "3" {
		t.Logf("/bin/bash is version %s, not 3.2: the empty-array case is only enforced on macOS", strings.TrimSpace(string(v)))
	}
	runUnder := func(t *testing.T, s authShim) (string, int) {
		t.Helper()
		stdout, stderr, code := runBashScript(t, s.prelude+forkAuthExports()+bash+" '"+railwayEnvScript(t)+"' "+passSub+" "+authForkEnvID+"\n")
		out := stdout + stderr
		if strings.Contains(out, "unbound variable") {
			t.Errorf("the pass hit an unbound variable under %s; output = %q", bash, clip(out))
		}
		return out, code
	}

	// Every generated secret reads as gtGoodValue, and tenancy already holds it.
	s := newAuthShim(t, map[string]string{"envList": authEnvList(true), "settle": gtSettle(t)}, passHeldStores(t, freshJWK(t), false))
	withOpenssl(t, s, `echo `+gtGoodValue)
	out, code := runUnder(t, s)
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, clip(out))
	}
	ws := passWrites(t, s)
	if len(ws) == 0 {
		t.Fatal("control: nothing was written")
	}
	if got := writesTo(ws, gtSvcID("tenancy")); len(got) != 0 {
		t.Errorf("tenancy already holds the run's token and has %d write input(s), want none: a service with nothing to write has no alias", len(got))
	}
	want := map[string][]string{
		"auth":          {"GOTRUE_JWT_KEYS", "GOTRUE_JWT_SECRET"},
		"gateway":       {"AUTH_ADMIN_PASSWORD", "GATEWAY_TOKEN"},
		"portfolio":     {"GATEWAY_TOKEN"},
		"invoice":       {"GATEWAY_TOKEN"},
		"validation":    {"GATEWAY_TOKEN"},
		"submission":    {"GATEWAY_TOKEN"},
		"dashboard":     {"GATEWAY_TOKEN"},
		"notifications": {"GATEWAY_TOKEN"},
	}
	got := map[string][]string{}
	for _, w := range ws {
		got[passLabel(w.Service)] = slices.Sorted(maps.Keys(w.Vars))
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("varsWrite aliases = %v, want only the services with a differing name: %v", got, want)
	}

	t.Run("nothing differs: no write and no re-read, and the verdicts read the first read", func(t *testing.T) {
		key := freshJWK(t)
		s := newAuthShim(t, map[string]string{"envList": authEnvList(true), "settle": gtSettle(t)}, passHeldStores(t, key, true))
		withOpenssl(t, s, `echo `+gtGoodValue)
		withFixedKey(t, s, key)
		out, code := runUnder(t, s)
		if want := []string{"envList", "settle", "varsRead"}; !slices.Equal(operations(s.calls(t)), want) {
			t.Errorf("Railway calls = %v, want %v: with nothing to write there is no write and no re-read", operations(s.calls(t)), want)
		}
		if code != 0 {
			t.Errorf("exit %d, want 0; output = %q", code, clip(out))
		}
		for _, l := range []string{passAuthConfirmed, passTokenConfirmed} {
			if !strings.Contains(out, l) {
				t.Errorf("no %q line; output = %q", l, clip(out))
			}
		}
	})
}

// requireAliasFailureReport checks AC-5 for one failed alias, whichever shape Railway answered.
func requireAliasFailureReport(t *testing.T, s authShim, out string, code int, failing string) {
	t.Helper()
	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, clip(out))
	}
	if n := opCount(t, s, "varsWrite"); n != 1 {
		t.Fatalf("%d varsWrite call(s), want 1", n)
	}
	if ops := operations(s.calls(t)); lastOp(ops) != "varsWrite" {
		t.Errorf("Railway calls = %v, want varsWrite last: a failed write is never re-read", ops)
	}
	ws := passWrites(t, s)
	bad := writesTo(ws, gtSvcID(failing))
	if len(bad) != 1 {
		t.Fatalf("%s has %d write input(s), want 1", failing, len(bad))
	}
	errs := errorLines(out)
	words := append([]string{"INTERNAL_SERVER_ERROR", passTraceID}, slices.Sorted(maps.Keys(bad[0].Vars))...)
	if l := requireNamedIn(t, errs, words...); l != "" && !wordIn(l, failing) {
		t.Errorf("the failure line %q does not name the service %s", l, failing)
	}
	var others []string
	for _, w := range ws {
		if w.Service != gtSvcID(failing) {
			others = append(others, passLabel(w.Service))
		}
	}
	if len(others) == 0 {
		t.Fatal("control: the write has no other service, so no 'not confirmed' list is observable")
	}
	for _, l := range strings.Split(errs, "\n") {
		if !strings.Contains(l, "not confirmed") {
			continue
		}
		if wordIn(l, failing) {
			t.Errorf("the 'not confirmed' line %q lists %s, which is the failed service", l, failing)
		}
		for _, o := range others {
			if !wordIn(l, o) {
				t.Errorf("the 'not confirmed' line %q omits %s", l, o)
			}
		}
	}
	if !strings.Contains(errs, "not confirmed") {
		t.Errorf("error lines %q do not list the other services as not confirmed", errs)
	}
	for _, w := range ws {
		for _, name := range passSecretNames {
			if v, ok := w.Vars[name]; ok && v != "" && strings.Contains(out, v) {
				t.Errorf("the output carries the %s.%s value", passLabel(w.Service), name)
			}
		}
	}
	if strings.Contains(out, passRailwayMessage) {
		t.Errorf("the output carries Railway's message text; output = %q", clip(out))
	}
	if e := upsertEcho.FindAllString(out, -1); len(e) != 0 {
		t.Errorf("a failed write printed write lines %q", e)
	}
	if got := confirmedLines(out); len(got) != 0 {
		t.Errorf("a failed write printed confirmation lines %q", got)
	}
}

func TestForkPass_OneAliasFailingNamesItsServiceAndNames(t *testing.T) {
	t.Run("abort: data is null and the later aliases are not applied", func(t *testing.T) {
		s := newPassShim(t, nil, nil)
		s.failAlias(t, gtSvcID("gateway"), "abort")
		out, code := runPass(t, s)
		requireAliasFailureReport(t, s, out, code, "gateway")
	})
	t.Run("the message echoes an input value", func(t *testing.T) {
		s := newPassShim(t, nil, nil)
		s.failAlias(t, gtSvcID("gateway"), "abort")
		writeFile(t, filepath.Join(s.dir, "aliasfail.message"), `Variable "$i1" got invalid value "`+passEchoNeedle+`"`)
		out, code := runPass(t, s)
		requireAliasFailureReport(t, s, out, code, "gateway")
		for _, needle := range []string{passEchoNeedle, "invalid value"} {
			if strings.Contains(out, needle) {
				t.Errorf("the output carries %q from Railway's message; output = %q", needle, clip(out))
			}
		}
	})
	// The write is auth, gateway, tenancy, portfolio, invoice, validation, submission, dashboard, notifications.
	for _, mode := range []string{"abort", "null"} {
		for _, c := range []struct{ svc, at string }{{"auth", "first"}, {"invoice", "middle"}, {"notifications", "last"}} {
			t.Run(mode+"/"+c.at+" alias fails", func(t *testing.T) {
				s := newPassShim(t, nil, nil)
				s.failAlias(t, gtSvcID(c.svc), mode)
				out, code := runPass(t, s)
				ws := passWrites(t, s)
				pos := map[string]int{"first": 0, "middle": len(ws) / 2, "last": len(ws) - 1}[c.at]
				if len(ws) != 9 || ws[pos].Service != gtSvcID(c.svc) {
					t.Fatalf("control: %s is not the %s alias of the write; services = %v", c.svc, c.at, ws)
				}
				requireAliasFailureReport(t, s, out, code, c.svc)
				if n := strings.Count(errorLines(out), "The batched variable write"); n != 1 {
					t.Errorf("%d failure line(s) name a service, want 1; error lines = %q", n, errorLines(out))
				}
			})
		}
	}
	t.Run("an alias past a service with nothing to write names its own service", func(t *testing.T) {
		stores := passStores(t, passAuthInherited(t))
		stores[gtSvcID("tenancy")]["GATEWAY_TOKEN"] = gtGoodValue
		s := newAuthShim(t, map[string]string{"envList": authEnvList(true), "settle": gtSettle(t)}, stores)
		withOpenssl(t, s, `echo `+gtGoodValue)
		s.failAlias(t, gtSvcID("portfolio"), "abort")
		out, code := runPass(t, s)
		if ws := passWrites(t, s); len(ws) != 8 || len(writesTo(ws, gtSvcID("tenancy"))) != 0 || ws[2].Service != gtSvcID("portfolio") {
			t.Fatalf("control: want 8 aliases, no tenancy, portfolio as alias 2; services = %v", ws)
		}
		requireAliasFailureReport(t, s, out, code, "portfolio")
	})
	t.Run("a valid alias beside an out-of-range one names only the valid one", func(t *testing.T) {
		s := newPassShim(t, nil, nil)
		s.plantWriteReply(t, writeErrors(`"path":["s2"],`, `"path":["s9"],`))
		out, code := runPass(t, s)
		requireAliasFailureReport(t, s, out, code, "tenancy")
		if n := strings.Count(errorLines(out), "The batched variable write"); n != 1 {
			t.Errorf("%d failure line(s) name a service, want 1 (s9 names none); error lines = %q", n, errorLines(out))
		}
	})
	t.Run("two aliases fail: both are named, neither is listed as not confirmed", func(t *testing.T) {
		s := newPassShim(t, nil, nil)
		s.plantWriteReply(t, writeErrors(`"path":["s1"],`, `"path":["s4"],`))
		out, code := runPass(t, s)
		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, clip(out))
		}
		ws := passWrites(t, s)
		if len(ws) != 9 {
			t.Fatalf("control: the write carried %d service input(s), want 9", len(ws))
		}
		for n, i := range []int{1, 4} {
			words := append([]string{"The batched variable write", "failed for " + passLabel(ws[i].Service) + ":", traceOf(n)}, slices.Sorted(maps.Keys(ws[i].Vars))...)
			if l := requireNamedIn(t, errorLines(out), words...); strings.Contains(l, traceOf(1-n)) {
				t.Errorf("the line for %s carries the other error's trace id %s: %q", passLabel(ws[i].Service), traceOf(1-n), l)
			}
		}
		notConfirmed := requireNamedIn(t, errorLines(out), "not confirmed")
		for i, w := range ws {
			if listed := wordIn(notConfirmed, passLabel(w.Service)); listed == (i == 1 || i == 4) {
				t.Errorf("%s listed as not confirmed = %t, want %t; line = %q", passLabel(w.Service), listed, !listed, notConfirmed)
			}
		}
	})
	t.Run("a 200 with no errors and false for one alias is not confirmed by the re-read", func(t *testing.T) {
		s := newPassShim(t, nil, nil)
		s.plantWriteReply(t, `{"data":{"s0":true,"s1":true,"s2":false,"s3":true,"s4":true,"s5":true,"s6":true,"s7":true,"s8":true}}`)
		out, code := runPass(t, s)
		if n := opCount(t, s, "varsWrite"); n != 1 {
			t.Fatalf("%d varsWrite call(s), want 1", n)
		}
		if code != 1 {
			t.Errorf("exit %d, want 1: nothing was stored, so no verdict can pass; output = %q", code, clip(out))
		}
		if !strings.Contains(errorLines(out), "tenancy.GATEWAY_TOKEN") {
			t.Errorf("error lines %q do not name tenancy.GATEWAY_TOKEN", errorLines(out))
		}
		for _, l := range []string{passAuthConfirmed, passTokenConfirmed} {
			if strings.Contains(out, l) {
				t.Errorf("an unstored write printed %q", l)
			}
		}
	})
}

func TestForkPass_OneAliasFailingWithTheOthersApplied(t *testing.T) {
	s := newPassShim(t, nil, nil)
	s.failAlias(t, gtSvcID("gateway"), "null")
	out, code := runPass(t, s)
	requireAliasFailureReport(t, s, out, code, "gateway")
}

func TestForkPass_WriteErrorWithNoPathNamesEveryService(t *testing.T) {
	for _, c := range []struct {
		name  string
		plant func(t *testing.T, s authShim)
	}{
		{"HTTP 400 with no path", func(t *testing.T, s authShim) {
			setFaults(t, s, "varsWrite", "400")
			writeFile(t, filepath.Join(s.dir, "faultbody-varsWrite"),
				`{"errors":[{"message":"Variable \"$i0\" got invalid value \"`+passEchoNeedle+`\"","extensions":{"code":"BAD_USER_INPUT"}}]}`)
		}},
		{"a 200 error with no path key", func(t *testing.T, s authShim) { s.plantWriteReply(t, writeErrors(``)) }},
		{"an alias past the end of the write", func(t *testing.T, s authShim) { s.plantWriteReply(t, writeErrors(`"path":["s9"],`)) }},
		{"a non-canonical alias", func(t *testing.T, s authShim) { s.plantWriteReply(t, writeErrors(`"path":["s01"],`)) }},
		{"a path that is not an alias", func(t *testing.T, s authShim) {
			s.plantWriteReply(t, writeErrors(`"path":["variableCollectionUpsert"],`))
		}},
		{"a numeric path", func(t *testing.T, s authShim) { s.plantWriteReply(t, writeErrors(`"path":[1],`)) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newPassShim(t, nil, nil)
			c.plant(t, s)
			out, code := runPass(t, s)
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, clip(out))
			}
			ws := passWrites(t, s)
			if len(ws) != 9 {
				t.Fatalf("the failed write carried %d service input(s), want all 9", len(ws))
			}
			// One failure line names every service; a "not confirmed" list would also name them.
			line := requireNamedIn(t, errorLines(out), "The batched variable write", "failed for")
			for _, w := range ws {
				if !wordIn(line, passLabel(w.Service)) {
					t.Errorf("the failure line %q does not name %s, which was in the write", line, passLabel(w.Service))
				}
			}
			for _, needle := range []string{passEchoNeedle, "invalid value", passRailwayMessage} {
				if strings.Contains(out, needle) {
					t.Errorf("the output carries %q from Railway's message; output = %q", needle, clip(out))
				}
			}
			if ops := operations(s.calls(t)); lastOp(ops) != "varsWrite" {
				t.Errorf("Railway calls = %v, want varsWrite last", ops)
			}
			if got := confirmedLines(out); len(got) != 0 {
				t.Errorf("a failed write printed confirmation lines %q", got)
			}
		})
	}
}

func TestForkPass_WriteExhaustedBudgetExitsWithoutReRead(t *testing.T) {
	s := newPassShim(t, nil, nil)
	setFaults(t, s, "varsWrite", "timeout", "timeout", "timeout")
	out, code := runPass(t, s)
	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, clip(out))
	}
	if n := opCount(t, s, "varsWrite"); n != 3 {
		t.Errorf("varsWrite calls = %d, want 3", n)
	}
	if ops := operations(s.calls(t)); lastOp(ops) != "varsWrite" || opCount(t, s, "varsRead") != 1 {
		t.Errorf("Railway calls = %v, want one varsRead and no read after the failed write", ops)
	}
	if got := s.sleeps(t); !slices.Equal(got, []string{"5", "10"}) {
		t.Errorf("sleeps = %v, want [5 10]", got)
	}
	errs := errorLines(out)
	if !strings.Contains(errs, "after 3 attempts") {
		t.Errorf("error lines %q do not say \"after 3 attempts\"", errs)
	}
	line := requireNamedIn(t, errs, "The batched variable write", "failed for")
	for _, w := range passWrites(t, s) {
		if !wordIn(line, passLabel(w.Service)) {
			t.Errorf("the failure line %q does not name %s, which was in the write", line, passLabel(w.Service))
		}
	}
	if got := confirmedLines(out); len(got) != 0 {
		t.Errorf("a failed write printed confirmation lines %q", got)
	}

	t.Run("a 429 waits Retry-After, resends the write once and confirms", func(t *testing.T) {
		s := newPassShim(t, nil, nil)
		setFaults(t, s, "varsWrite", "429")
		writeFile(t, filepath.Join(s.dir, "hdr.txt"), "HTTP/2 429\r\nretry-after: 30\r\n\r\n")
		out, code := runPass(t, s)
		if code != 0 {
			t.Fatalf("exit %d, want 0; output = %q", code, clip(out))
		}
		if got := s.sleeps(t); !slices.Equal(got, []string{"30"}) {
			t.Errorf("sleeps = %v, want [30]", got)
		}
		if n := opCount(t, s, "varsWrite"); n != 2 {
			t.Errorf("varsWrite calls = %d, want 2", n)
		}
	})
	t.Run("a 429 that asks for more than 600 s fails the step and names the services", func(t *testing.T) {
		s := newPassShim(t, nil, nil)
		setFaults(t, s, "varsWrite", "429")
		writeFile(t, filepath.Join(s.dir, "hdr.txt"), "HTTP/2 429\r\nretry-after: 601\r\n\r\n")
		writeFile(t, filepath.Join(s.dir, "faultbody-varsWrite"), `{"errors":[{"message":"`+passEchoNeedle+`"}]}`)
		out, code := runPass(t, s)
		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, clip(out))
		}
		if got := s.sleeps(t); len(got) != 0 {
			t.Errorf("sleeps = %v, want none: the wait is over the limit", got)
		}
		if n, ops := opCount(t, s, "varsWrite"), operations(s.calls(t)); n != 1 || lastOp(ops) != "varsWrite" {
			t.Errorf("Railway calls = %v, want one varsWrite and no re-read", ops)
		}
		line := requireNamedIn(t, errorLines(out), "The batched variable write", "failed for")
		for _, w := range passWrites(t, s) {
			if !wordIn(line, passLabel(w.Service)) {
				t.Errorf("the failure line %q does not name %s, which was in the write", line, passLabel(w.Service))
			}
		}
		requireWaitNamed(t, out, `429`, `\b601\b`, `\b600\b`)
	})
	t.Run("a second 429 fails the step and names the wait", func(t *testing.T) {
		s := newPassShim(t, nil, nil)
		setFaults(t, s, "varsWrite", "429", "429")
		writeFile(t, filepath.Join(s.dir, "hdr.txt"), "HTTP/2 429\r\nretry-after: 30\r\n\r\n")
		writeFile(t, filepath.Join(s.dir, "faultbody-varsWrite"), `{"errors":[{"message":"`+passEchoNeedle+`"}]}`)
		out, code := runPass(t, s)
		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, clip(out))
		}
		if got := s.sleeps(t); !slices.Equal(got, []string{"30"}) {
			t.Errorf("sleeps = %v, want [30]", got)
		}
		if n, ops := opCount(t, s, "varsWrite"), operations(s.calls(t)); n != 2 || lastOp(ops) != "varsWrite" {
			t.Errorf("Railway calls = %v, want two varsWrite and no re-read", ops)
		}
		requireWaitNamed(t, out, `429`, `(?i)second`, `\b30\b`)
	})
	for _, fault := range []string{"503", "timeout"} {
		t.Run(fault+" then success resends the same write once and confirms", func(t *testing.T) {
			s := newPassShim(t, nil, nil)
			setFaults(t, s, "varsWrite", fault)
			out, code := runPass(t, s)
			if code != 0 {
				t.Fatalf("exit %d, want 0: a write that got no response is retried; output = %q", code, clip(out))
			}
			writes := callsOf(s, t, "varsWrite")
			if len(writes) != 2 || fmt.Sprint(writes[0].Variables) != fmt.Sprint(writes[1].Variables) {
				t.Errorf("varsWrite calls = %d, want 2 carrying the same variables", len(writes))
			}
			if want := []string{"envList", "settle", "varsRead", "varsWrite", "varsWrite", "varsRead"}; !slices.Equal(operations(s.calls(t)), want) {
				t.Errorf("Railway calls = %v, want %v", operations(s.calls(t)), want)
			}
			if got := s.sleeps(t); !slices.Equal(got, []string{"5"}) {
				t.Errorf("sleeps = %v, want [5]", got)
			}
			if !strings.Contains(out, "succeeded on attempt 2/3") {
				t.Errorf("no 'succeeded on attempt 2/3' warning; output = %q", clip(out))
			}
			for _, l := range []string{passAuthConfirmed, passTokenConfirmed} {
				if !strings.Contains(out, l) {
					t.Errorf("no %q line; output = %q", l, clip(out))
				}
			}
		})
	}
}
