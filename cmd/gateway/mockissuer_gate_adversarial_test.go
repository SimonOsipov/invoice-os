package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// runShimmed runs block with the probe shims first on PATH and exactly env, so IS_PR may be unset.
func runShimmed(t *testing.T, block string, codes map[string][]string, env ...string) probeRun {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{"curl": probeShim, "sleep": "#!/bin/sh\necho \"$*\" >> \"$SHIM_DIR/sleep.log\"\n"}
	for route, c := range codes {
		files[route+".codes"] = strings.Join(c, " ") + "\n"
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	base := []string{"PATH=" + dir + ":/usr/bin:/bin", "SHIM_DIR=" + dir, "GATEWAY_URL=" + probeGatewayURL}
	code, out := runGate(t, block, append(base, env...)...)
	r := probeRun{code: code, out: out, calls: map[string][][]string{}, sleeps: readLog(t, filepath.Join(dir, "sleep.log"))}
	for _, l := range readLog(t, filepath.Join(dir, "curl.log")) {
		f := strings.Fields(l)
		r.calls[f[0]] = append(r.calls[f[0]], f[1:])
	}
	return r
}

// codesThen is n copies of code followed by last.
func codesThen(n int, code, last string) []string {
	return append(slices.Repeat([]string{code}, n), last)
}

func errorOutput(out string) string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "::error::") {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

func TestMintRouteProbeReportsTheLastCodeOfTheWindow(t *testing.T) {
	rows := []struct {
		name        string
		jwks, login []string
		wantExit    int
		calls       map[string]int
		says        []string
		saysNot     []string
	}{
		{"jwks turns 503 on the last attempt", codesThen(35, "200", "503"), repeat("404"), 1,
			map[string]int{"jwks": 36}, []string{"/.well-known/jwks.json", "answered 503"}, []string{"answered 200", "/auth/login"}},
		{"login turns unreachable on the last attempt", repeat("404"), codesThen(35, "403", "000"), 1,
			map[string]int{"jwks": 1, "login": 36}, []string{"/auth/login", "answered 000"}, []string{"answered 403", "/.well-known/jwks.json"}},
		{"jwks answers 404 on the 36th attempt", codesThen(35, "200", "404"), repeat("404"), 0,
			map[string]int{"jwks": 36, "login": 1}, nil, nil},
		{"jwks would answer 404 only on a 37th attempt", codesThen(36, "200", "404"), repeat("404"), 1,
			map[string]int{"jwks": 36}, []string{"/.well-known/jwks.json", "answered 200"}, nil},
		{"login flaps 200 then 404 then 200", repeat("404"), []string{"200", "404", "200"}, 0,
			map[string]int{"jwks": 1, "login": 2}, nil, nil},
	}
	t.Run("control needle", func(t *testing.T) {
		for _, row := range rows {
			r := runShimmed(t, referenceProbe, map[string][]string{"jwks": row.jwks, "login": row.login}, "IS_PR=false")
			if r.code != row.wantExit {
				t.Fatalf("the reference probe exits %d on %q, want %d; the rows are wrong (output %q)", r.code, row.name, row.wantExit, r.out)
			}
		}
	})

	block := probeScript(runText(devEnvHealthGate(t)))
	if block == "" {
		t.Fatal(`dev-env.yml's health-gate carries no if [ "$IS_PR" != "true" ] probe block`)
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			r := runShimmed(t, block, map[string][]string{"jwks": row.jwks, "login": row.login}, "IS_PR=false")
			if r.code != row.wantExit {
				t.Errorf("exit %d, want %d (output %q)", r.code, row.wantExit, r.out)
			}
			for route, n := range row.calls {
				if got := len(r.calls[route]); got != n {
					t.Errorf("%s probed %d time(s), want %d", route, got, n)
				}
			}
			errs := errorOutput(r.out)
			if row.wantExit == 0 && errs != "" {
				t.Errorf("a passing probe printed %q", errs)
			}
			for _, s := range row.says {
				if !strings.Contains(errs, s) {
					t.Errorf("the ::error:: output does not carry %q: %q", s, errs)
				}
			}
			for _, s := range row.saysNot {
				if strings.Contains(errs, s) {
					t.Errorf("the ::error:: output carries %q, which is not the last code of the failing route: %q", s, errs)
				}
			}
		})
	}
}

// gateTail returns, as runnable shell, the top-level blocks after the build wait: the
// db_reset branch, the purge/mock expectation, both assertions and the probe block.
func gateTail(run string) string {
	return topLevelIfs(run, func(b []gateStmt) bool {
		return strings.HasPrefix(b[0].text, `if [ "$IS_PR"`) ||
			slices.ContainsFunc(b, func(s gateStmt) bool {
				w := stmtWord(s.text)
				return (w == "if" || w == "elif") && (strings.Contains(s.text, `"$purge"`) || strings.Contains(s.text, `"$mock"`))
			})
	})
}

func TestHealthGateFailsClosedWhenIS_PRIsNotTrue(t *testing.T) {
	tail := gateTail(runText(devEnvHealthGate(t)))
	for _, part := range []string{`"$reset" != "true"`, `elif [ "$reset" = "true" ]`, "want_purge=false", `"$purge" != "$want_purge"`, `"$mock" != "$want_mock"`, "curl "} {
		if !strings.Contains(tail, part) {
			t.Fatalf("the extracted gate tail carries no %q; the runs below would test a partial gate:\n%s", part, tail)
		}
	}
	allNotFound := map[string][]string{"jwks": repeat("404"), "login": repeat("404")}
	stillServed := map[string][]string{"jwks": repeat("200"), "login": repeat("403")}
	fork := []string{"reset=true", "purge=true", "mock=on"}
	production := []string{"reset=false", "purge=false", "mock=absent"}

	// Controls: each target passes with its own healthz values.
	if r := runShimmed(t, tail, allNotFound, append([]string{"IS_PR=true"}, fork...)...); r.code != 0 || len(r.calls) != 0 {
		t.Fatalf("control: a healthy PR fork exits %d with curl calls %v, want 0 and none (output %q)", r.code, r.calls, r.out)
	}
	if r := runShimmed(t, tail, allNotFound, append([]string{"IS_PR=false"}, production...)...); r.code != 0 || len(r.calls["jwks"]) != 1 || len(r.calls["login"]) != 1 {
		t.Fatalf("control: healthy production exits %d with curl calls %v, want 0 and one per route (output %q)", r.code, r.calls, r.out)
	}

	// IS_PR is anything but "true" on a PR run: the persistent branch is taken, and a
	// real fork's values must fail it before any probe runs.
	notTrue := map[string][]string{"unset": nil, "empty": {"IS_PR="}, "True": {"IS_PR=True"}, "1": {"IS_PR=1"}}
	for name, isPR := range notTrue {
		for _, values := range [][]string{fork, {"reset=false", "purge=true", "mock=on"}, {"reset=false", "purge=false", "mock=on"}} {
			r := runShimmed(t, tail, stillServed, append(slices.Clone(isPR), values...)...)
			if r.code == 0 {
				t.Errorf("IS_PR %s with a PR fork's %v exits 0; a fork read as production must go red (output %q)", name, values, r.out)
			}
			if len(r.calls) != 0 {
				t.Errorf("IS_PR %s with %v probed %v before failing on the fork's healthz values", name, values, r.calls)
			}
		}
		// The persistent branch is the default, which is right for push and dispatch.
		if r := runShimmed(t, tail, allNotFound, append(slices.Clone(isPR), production...)...); r.code != 0 {
			t.Errorf("IS_PR %s with production's values exits %d, want 0 (output %q)", name, r.code, r.out)
		}
		if r := runShimmed(t, tail, stillServed, append(slices.Clone(isPR), production...)...); r.code != 1 || len(r.calls["jwks"]) != 36 {
			t.Errorf("IS_PR %s with production's values and live mint routes exits %d after %d jwks probe(s), want 1 after 36", name, r.code, len(r.calls["jwks"]))
		}
	}
}

func TestMockIssuerGateFailsOnAValueOutsideItsDomain(t *testing.T) {
	offDomain := []string{"ON", "On", "on ", " on", "true", "1", "yes", "ABSENT", "Absent", "absent ", " absent", "none", "null", "OFF", "Off", "off "}
	targets := []struct{ isPR, passes string }{{"true", "on"}, {"false", "absent"}}
	if len(offDomain) == 0 {
		t.Fatal("no values to try")
	}
	block := mockGateScript(runText(devEnvHealthGate(t)))
	if block == "" {
		t.Fatal(`dev-env.yml's health-gate carries no if-block on "$mock" to run`)
	}
	for _, tg := range targets {
		if got, out := runGate(t, block, "IS_PR="+tg.isPR, "mock="+tg.passes); got != 0 {
			t.Errorf("IS_PR=%s mock_issuer=%q exits %d, want 0; the gate refuses the one value this target expects (output %q)", tg.isPR, tg.passes, got, out)
		}
		for _, v := range offDomain {
			if got, _ := runGate(t, block, "IS_PR="+tg.isPR, "mock="+v); got != 1 {
				t.Errorf("IS_PR=%s mock_issuer=%q exits %d, want 1; only exactly %q passes on this target", tg.isPR, v, got, tg.passes)
			}
		}
	}
}
