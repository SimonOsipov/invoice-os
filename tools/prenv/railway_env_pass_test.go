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

func TestForkVarsBeforeURLs_ReReadDropsTheGatewayToken(t *testing.T) {
	s := newPassShim(t, nil, nil)
	s.bendReRead(t, gtSvcID("tenancy"), `del(.GATEWAY_TOKEN)`)
	out, code := runPass(t, s)
	token, ok := passWritten(passWrites(t, s), gtSvcID("tenancy"), "GATEWAY_TOKEN")
	if !ok || !hex64.MatchString(token) {
		t.Fatalf("control: tenancy's GATEWAY_TOKEN write = %q (present %t), want 64 hex characters", token, ok)
	}
	if code != 1 || !strings.Contains(errorLines(out), "tenancy.GATEWAY_TOKEN") {
		t.Errorf("exit %d and error lines %q, want exit 1 naming tenancy.GATEWAY_TOKEN", code, errorLines(out))
	}
	if strings.Contains(out, token) {
		t.Error("the output carries the generated GATEWAY_TOKEN")
	}
	if strings.Contains(out, passTokenConfirmed) {
		t.Errorf("a missing re-read token still printed the GATEWAY_TOKEN confirmation; output = %q", clip(out))
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
			if c.says != "" && !strings.Contains(errorLines(out), c.says) {
				t.Errorf("error lines %q do not say %q", errorLines(out), c.says)
			}
			if got := confirmedLines(out); len(got) != 0 {
				t.Errorf("a failed read printed confirmation lines %q", got)
			}
		})
	}
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
		})
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
		"GOTRUE_JWT_KEYS":           freshJWK(t),
		"GOTRUE_JWT_SECRET":         authSourceJWTSecret,
	}
	stores := passStores(t, auth)
	gw := stores[gtSvcID("gateway")]
	gw["AUTH_ISSUER"], gw["AUTH_JWKS_URL"], gw["AUTH_URL"] = authMockIssuer, authLoopbackJWKS, authInternalURL
	gw["AUTH_ADDITIONAL_ISSUERS"] = `[{"issuer":"` + authForkIssuer + `","jwks_url":"` + authJWKSURL + `"}]`
	// Every generated secret reads as gtGoodValue, and tenancy already holds it.
	stores[gtSvcID("tenancy")]["GATEWAY_TOKEN"] = gtGoodValue
	s := newAuthShim(t, map[string]string{"envList": authEnvList(true), "settle": gtSettle(t)}, stores)
	withOpenssl(t, s, `echo `+gtGoodValue)

	stdout, stderr, code := runBashScript(t, s.prelude+forkAuthExports()+bash+" '"+railwayEnvScript(t)+"' "+passSub+" "+authForkEnvID+"\n")
	out := stdout + stderr
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, clip(out))
	}
	if strings.Contains(out, "unbound variable") {
		t.Errorf("the pass hit an unbound variable under %s; output = %q", bash, clip(out))
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
}

func TestForkPass_OneAliasFailingWithTheOthersApplied(t *testing.T) {
	s := newPassShim(t, nil, nil)
	s.failAlias(t, gtSvcID("gateway"), "null")
	out, code := runPass(t, s)
	requireAliasFailureReport(t, s, out, code, "gateway")
}

func TestForkPass_WriteErrorWithNoPathNamesEveryService(t *testing.T) {
	s := newPassShim(t, nil, nil)
	setFaults(t, s, "varsWrite", "400")
	writeFile(t, filepath.Join(s.dir, "faultbody-varsWrite"),
		`{"errors":[{"message":"Variable \"$i0\" got invalid value \"`+passEchoNeedle+`\"","extensions":{"code":"BAD_USER_INPUT"}}]}`)
	out, code := runPass(t, s)
	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, clip(out))
	}
	ws := passWrites(t, s)
	if len(ws) != 9 {
		t.Fatalf("the failed write carried %d service input(s), want all 9", len(ws))
	}
	for _, w := range ws {
		if !wordIn(errorLines(out), passLabel(w.Service)) {
			t.Errorf("error lines %q do not name %s, which was in the write", errorLines(out), passLabel(w.Service))
		}
	}
	for _, needle := range []string{passEchoNeedle, "invalid value"} {
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
	for _, w := range passWrites(t, s) {
		if !wordIn(errs, passLabel(w.Service)) {
			t.Errorf("error lines %q do not name %s, which was in the write", errs, passLabel(w.Service))
		}
	}
	if got := confirmedLines(out); len(got) != 0 {
		t.Errorf("a failed write printed confirmation lines %q", got)
	}
}
