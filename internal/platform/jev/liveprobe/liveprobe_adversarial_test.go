package liveprobe

import (
	"math"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

func TestLiveProbeGate_ProbeMustBeExactlyOne(t *testing.T) {
	for name, v := range map[string]string{"leading_space": " 1", "trailing_space": "1 ", "zero_padded": "01", "trailing_newline": "1\n", "yes": "yes"} {
		t.Run(name, func(t *testing.T) {
			lpSetEnv(t, jev.EnvKey, lpSpecKey)
			lpSetEnv(t, lpEnvProbe, v)
			open, reason := lpGate()
			if open {
				t.Fatalf("lpGate() open with %s=%q, want closed", lpEnvProbe, v)
			}
			if !strings.Contains(reason, lpEnvProbe) || strings.Contains(reason, jev.EnvKey) {
				t.Errorf("reason %q, want it to name %s and not %s", reason, lpEnvProbe, jev.EnvKey)
			}
		})
	}
}

// Whitespace counts as a set key, as FromEnv reads it; the live call then fails the verdict.
func TestLiveProbeGate_WhitespaceKeyCountsAsSet(t *testing.T) {
	lpSetEnv(t, jev.EnvKey, "   ")
	lpSetEnv(t, lpEnvProbe, lpUnset)
	open, reason := lpGate()
	if open {
		t.Fatal("lpGate() open without JEV_PROBE, want closed")
	}
	if strings.Contains(reason, jev.EnvKey) {
		t.Errorf("reason %q names %s, which is set", reason, jev.EnvKey)
	}

	lpSetEnv(t, lpEnvProbe, "1")
	if open, reason := lpGate(); !open {
		t.Errorf("lpGate() closed (%q) with a whitespace key and JEV_PROBE=1, want open", reason)
	}
}

func TestLiveProbeVerdict_RejectsAdversarialFields(t *testing.T) {
	zdr := map[string]bool{lpZDRVersion: true}
	good := lpLine{Type: jev.TypeNoul, Purpose: string(jev.PurposeValueCheck), Model: lpZDRVersion,
		Outcome: "ok", Attempts: 1, Cost: 0.0000228, LatencyMS: 495}
	if _, err := lpVerdict(good, zdr); err != nil {
		t.Fatalf("lpVerdict(good line) = %v, want nil", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*lpLine)
		want string
	}{
		{"cost_negative", func(l *lpLine) { l.Cost = -0.0000228 }, "cost"},
		{"cost_nan", func(l *lpLine) { l.Cost = math.NaN() }, "cost"},
		{"attempts_0", func(l *lpLine) { l.Attempts = 0 }, "attempts"},
		{"model_wrong_case", func(l *lpLine) { l.Model = "TypeSafe/jev-1.13-20260917" }, "model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := good
			tc.edit(&line)
			_, err := lpVerdict(line, zdr)
			if err == nil {
				t.Fatalf("lpVerdict(%+v) = nil, want a failure naming %s", line, tc.want)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Errorf("lpVerdict error %q does not name %s", err, tc.want)
			}
		})
	}
}

// A fake line fails every field at once; the verdict must report all, not stop at the first.
func TestLiveProbeVerdict_NamesEveryFailingField(t *testing.T) {
	line := lpLine{Type: jev.TypeChoice, Purpose: string(jev.PurposeDocumentType), Outcome: "fake"}
	_, err := lpVerdict(line, map[string]bool{lpZDRVersion: true})
	if err == nil {
		t.Fatal("lpVerdict(fake line) = nil, want a failure")
	}
	msg := strings.ToLower(err.Error())
	for _, f := range []string{"outcome", "attempts", "cost", "model", "zdr"} {
		if !strings.Contains(msg, f) {
			t.Errorf("lpVerdict error %q does not name %s", err, f)
		}
	}
}

func TestLiveProbeVerdict_NoZDRSetFailsClosed(t *testing.T) {
	good := lpLine{Type: jev.TypeScore, Purpose: string(jev.PurposeMappingCheck), Model: lpZDRVersion,
		Outcome: "ok", Attempts: 1, Cost: 0.0000228, LatencyMS: 303}
	for name, zdr := range map[string]map[string]bool{"nil": nil, "empty": {}, "other_version": {"typesafe/jev-1.12-20260801": true}} {
		t.Run(name, func(t *testing.T) {
			report, err := lpVerdict(good, zdr)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "zdr") {
				t.Errorf("lpVerdict with ZDR set %v = %v, want a failure naming zdr", zdr, err)
			}
			if !strings.Contains(report, "on_zdr_list=false") {
				t.Errorf("report %q, want on_zdr_list=false", report)
			}
		})
	}
}

// The report is what the PR shows, so a failing line prints its true fields too.
func TestLiveProbeVerdict_FailingLineReportCarriesItsFields(t *testing.T) {
	line := lpLine{Type: jev.TypeNoul, Purpose: string(jev.PurposeValueCheck), Model: "typesafe/jev-9.9-20990101",
		Outcome: "skipped_unavailable", Attempts: 2, Cost: 0.0000456, LatencyMS: 2900}
	report, err := lpVerdict(line, map[string]bool{lpZDRVersion: true})
	if err == nil {
		t.Fatal("lpVerdict(failing line) = nil, want a failure")
	}
	want := "probe noul: outcome=skipped_unavailable model=typesafe/jev-9.9-20990101 on_zdr_list=false latency_ms=2900 attempts=2 cost=4.56e-05"
	if report != want {
		t.Errorf("report\n %q\nwant\n %q", report, want)
	}
}

func TestLiveProbeZDR_MalformedBodyIsAnError(t *testing.T) {
	for name, body := range map[string]string{
		"not_json":      "<html>502 Bad Gateway</html>",
		"empty":         "",
		"data_a_string": `{"data":"typesafe/jev-1.13-20260917"}`,
		"truncated":     `{"data":[{"name":"TypeSafe | typesafe/jev-1.13-20260917","model_id":"typesafe/jev-1.13"`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := lpZDRVersions([]byte(body))
			if err == nil {
				t.Errorf("lpZDRVersions(%q) = %v, nil; want an error", body, got)
			}
			if len(got) != 0 {
				t.Errorf("lpZDRVersions(%q) = %v, want no versions beside the error", body, got)
			}
		})
	}
}

func TestLiveProbeZDR_TypeSafeEntryWithoutSeparatorAddsNothing(t *testing.T) {
	body := `{"data":[
{"name":"TypeSafe | typesafe/jev-1.13-20260917","model_id":"typesafe/jev-1.13"},
{"name":"typesafe/jev-2.0-20261001","model_id":"typesafe/jev-2.0"},
{"name":"TypeSafe|typesafe/jev-2.1-20261101","model_id":"typesafe/jev-2.1"},
{"name":"Other | typesafe/jev-3.0-20261201","model_id":"other/jev-3.0"}
]}`
	got, err := lpZDRVersions([]byte(body))
	if err != nil {
		t.Fatalf("lpZDRVersions err = %v, want nil", err)
	}
	if len(got) == 0 {
		t.Fatal("lpZDRVersions = {}, want the well-formed TypeSafe entry's version")
	}
	if len(got) != 1 || !got[lpZDRVersion] {
		t.Errorf("lpZDRVersions = %v, want exactly {%s}", got, lpZDRVersion)
	}
}

func TestLiveProbeZDR_NoTypeSafeEntryIsAnEmptySet(t *testing.T) {
	got, err := lpZDRVersions([]byte(`{"data":[{"name":"Mistral | z-ai/glm-5.3-20260816","model_id":"z-ai/glm-5.3"}]}`))
	if err != nil || len(got) != 0 {
		t.Errorf("lpZDRVersions = %v, %v; want an empty set and nil", got, err)
	}
}

// Each parsed line keeps the question type of the request that logged it.
func TestLiveProbeRun_EachLinePairsItsTypeWithItsPurpose(t *testing.T) {
	t.Setenv(jev.EnvFake, "true")
	t.Setenv(jev.EnvKey, "")
	var lines []lpLine
	lpNoDials(t, func() { lines = lpRun(t, jev.FromEnv) })
	if len(lines) != 3 {
		t.Fatalf("lpRun returned %d line(s), want 3", len(lines))
	}
	want := map[jev.QuestionType]jev.Purpose{
		jev.TypeNoul:   jev.PurposeValueCheck,
		jev.TypeChoice: jev.PurposeDocumentType,
		jev.TypeScore:  jev.PurposeMappingCheck,
	}
	for _, l := range lines {
		if p, ok := want[l.Type]; !ok || l.Purpose != string(p) {
			t.Errorf("line type %q purpose %q, want purpose %q", l.Type, l.Purpose, p)
		}
	}
}

// Open gate with the network refused: the probe fails on the ZDR fetch before any Jev call.
func TestJevLiveProbe_GateOpenFailsWhenTheZDRListIsUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var conns atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			c.Close()
		}
	}()
	proxy := "http://" + ln.Addr().String()
	strip := []string{jev.EnvKey, lpEnvProbe, jev.EnvFake,
		"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy"}
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(strip, name)
	})
	env = append(env, "HTTPS_PROXY="+proxy, "HTTP_PROXY="+proxy, jev.EnvKey+"="+lpSpecKey, lpEnvProbe+"=1")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestJevLiveProbe$", "-test.v", "-test.count=1")
	cmd.Env = env
	raw, err := cmd.CombinedOutput()
	out := string(raw)
	if err == nil {
		t.Fatalf("TestJevLiveProbe with an unreachable ZDR list passed, want a failure:\n%s", out)
	}
	if !strings.Contains(out, "--- FAIL: TestJevLiveProbe ") {
		t.Fatalf("no FAIL line for TestJevLiveProbe:\n%s", out)
	}
	if !strings.Contains(out, "GET "+zdrURL) {
		t.Errorf("the failure does not name the ZDR fetch:\n%s", out)
	}
	// One connection is the ZDR fetch; a Jev call before it would add more.
	if n := conns.Load(); n != 1 {
		t.Errorf("%d connection(s) reached the proxy, want 1: the ZDR fetch alone", n)
	}
	for _, leak := range []string{lpSpecKey, "live probe declined", "probe noul:", "probe choice:", "probe score:"} {
		if strings.Contains(out, leak) {
			t.Errorf("output holds %q, want a fetch failure before any Jev call:\n%s", leak, out)
		}
	}
}
