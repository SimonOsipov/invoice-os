package liveprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

const (
	lpEnvProbe   = "JEV_PROBE"
	lpSpecKey    = "sk-or-v1-liveprobe-spec-not-a-key"
	lpZDRVersion = "typesafe/jev-1.13-20260917"
	lpUnset      = "<unset>"
)

// Trimmed from the public GET https://openrouter.ai/api/v1/endpoints/zdr body (2026-09-27).
// The TypeSafe entry is second, and the other entry also carries " | ".
const lpZDRFixture = `{"data":[
{"name":"Mistral | z-ai/glm-5.3-20260816","model_id":"z-ai/glm-5.3","model_name":"Z.ai: GLM 5.3","context_length":1048576,"pricing":{"prompt":"0.00000154","completion":"0.00000484","input_cache_read":"0.000000154","discount":0},"provider_name":"Mistral","tag":"mistral/nvfp4","quantization":"nvfp4","status":0},
{"name":"TypeSafe | typesafe/jev-1.13-20260917","model_id":"typesafe/jev-1.13","model_name":"TypeSafe: Jev 1.13","context_length":32000,"pricing":{"prompt":"0.000000042","completion":"0","discount":0},"provider_name":"TypeSafe","tag":"typesafe","quantization":"unknown","status":0}
]}`

// lpSetEnv sets name for the test, or unsets it for value lpUnset; t restores it.
func lpSetEnv(t *testing.T, name, value string) {
	t.Helper()
	t.Setenv(name, "")
	if value == lpUnset {
		os.Unsetenv(name)
		return
	}
	os.Setenv(name, value)
}

// lpCallLines returns the decoded "jev call" lines in a JSON log buffer.
func lpCallLines(t *testing.T, log []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range bytes.Split(bytes.TrimSpace(log), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", raw, err)
		}
		if m["msg"] == "jev call" {
			out = append(out, m)
		}
	}
	return out
}

type lpRefusingTransport struct{ n *atomic.Int32 }

func (d lpRefusingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	d.n.Add(1)
	return nil, errors.New("liveprobe spec: refused " + r.URL.Host)
}

// lpNoDials fails t if fn sends anything through http.DefaultTransport; no test here may call t.Parallel.
func lpNoDials(t *testing.T, fn func()) {
	t.Helper()
	var n atomic.Int32
	orig := http.DefaultTransport
	http.DefaultTransport = lpRefusingTransport{&n}
	defer func() { http.DefaultTransport = orig }()
	fn()
	if got := n.Load(); got != 0 {
		t.Errorf("%d dial(s) during the call, want 0", got)
	}
}

func TestLpNoDials_CountsARefusedDial(t *testing.T) {
	var n atomic.Int32
	orig := http.DefaultTransport
	http.DefaultTransport = lpRefusingTransport{&n}
	defer func() { http.DefaultTransport = orig }()
	if _, err := http.Get("https://openrouter.ai/api/v1/endpoints/zdr"); err == nil {
		t.Fatal("http.Get err = nil, want the refusing transport to refuse it")
	}
	if n.Load() != 1 {
		t.Errorf("counter moved by %d, want 1: lpNoDials reads a counter that never moves", n.Load())
	}
}

// lpRecorder records the purpose of every "jev call" record the client logs.
type lpRecorder struct {
	slog.Handler
	purposes *[]string
}

func (h lpRecorder) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "jev call" {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "purpose" {
				*h.purposes = append(*h.purposes, a.Value.String())
			}
			return true
		})
	}
	return h.Handler.Handle(ctx, r)
}

func TestLiveProbeGate_DeclinesWithoutBothVariables(t *testing.T) {
	for _, tc := range []struct {
		name, key, probe string
		names, omits     []string // variables the reason must name, and set ones it must not
	}{
		{"neither_unset", lpUnset, lpUnset, []string{jev.EnvKey, lpEnvProbe}, nil},
		{"neither_empty", "", "", []string{jev.EnvKey, lpEnvProbe}, nil},
		{"key_only", lpSpecKey, lpUnset, []string{lpEnvProbe}, []string{jev.EnvKey}},
		{"probe_1_only", lpUnset, "1", []string{jev.EnvKey}, []string{lpEnvProbe}},
		{"key_and_probe_0", lpSpecKey, "0", []string{lpEnvProbe}, []string{jev.EnvKey}},
		{"key_and_probe_true", lpSpecKey, "true", []string{lpEnvProbe}, []string{jev.EnvKey}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lpSetEnv(t, jev.EnvKey, tc.key)
			lpSetEnv(t, lpEnvProbe, tc.probe)
			open, reason := lpGate()
			if open {
				t.Fatalf("lpGate() open with %s=%q %s=%q, want closed", jev.EnvKey, tc.key, lpEnvProbe, tc.probe)
			}
			for _, n := range tc.names {
				if !strings.Contains(reason, n) {
					t.Errorf("reason %q does not name %s", reason, n)
				}
			}
			for _, n := range tc.omits {
				if strings.Contains(reason, n) {
					t.Errorf("reason %q names %s, which is set and valid", reason, n)
				}
			}
			if strings.Contains(reason, lpSpecKey) {
				t.Errorf("reason %q carries the key value", reason)
			}
		})
	}

	t.Run("key_and_probe_1_opens", func(t *testing.T) {
		lpSetEnv(t, jev.EnvKey, lpSpecKey)
		lpSetEnv(t, lpEnvProbe, "1")
		if open, reason := lpGate(); !open {
			t.Errorf("lpGate() closed (%q) with both variables set, want open", reason)
		}
	})
}

// Re-runs TestJevLiveProbe in a child binary: only its -v output and its dials show the closed branch.
// JEV_FAKE is invalid there, so any jev.FromEnv call fails the child.
func TestJevLiveProbe_GateClosedPassesAndSendsNothing(t *testing.T) {
	strip := []string{jev.EnvKey, lpEnvProbe, jev.EnvFake,
		"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy"}
	for _, tc := range []struct {
		name  string
		env   []string
		names []string
	}{
		{"neither", nil, []string{jev.EnvKey, lpEnvProbe}},
		{"key_only", []string{jev.EnvKey + "=" + lpSpecKey}, []string{lpEnvProbe}},
		{"key_and_probe_0", []string{jev.EnvKey + "=" + lpSpecKey, lpEnvProbe + "=0"}, []string{lpEnvProbe}},
	} {
		t.Run(tc.name, func(t *testing.T) {
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

			env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
				name, _, _ := strings.Cut(kv, "=")
				return slices.Contains(strip, name)
			})
			env = append(env, "HTTPS_PROXY="+proxy, "HTTP_PROXY="+proxy, jev.EnvFake+"=not-a-bool")
			env = append(env, tc.env...)
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestJevLiveProbe$", "-test.v", "-test.count=1")
			cmd.Env = env
			raw, err := cmd.CombinedOutput()
			out := string(raw)
			if err != nil {
				t.Fatalf("TestJevLiveProbe with the gate closed exited %v, want a pass:\n%s", err, out)
			}
			if !strings.Contains(out, "--- PASS: TestJevLiveProbe ") {
				t.Fatalf("no PASS line for TestJevLiveProbe:\n%s", out)
			}
			if strings.Contains(out, "--- SKIP") {
				t.Errorf("TestJevLiveProbe skipped, want a logged decline and a pass:\n%s", out)
			}
			for _, n := range tc.names {
				if !strings.Contains(out, n) {
					t.Errorf("the decline log does not name %s:\n%s", n, out)
				}
			}
			for _, leak := range []string{lpSpecKey, "probe noul:", "probe choice:", "probe score:"} {
				if strings.Contains(out, leak) {
					t.Errorf("output holds %q with the gate closed:\n%s", leak, out)
				}
			}
			if n := conns.Load(); n != 0 {
				t.Errorf("%d connection(s) reached the proxy with the gate closed, want 0", n)
			}
		})
	}
}

func TestLiveProbeRequests_OneOfEachTypeAndValid(t *testing.T) {
	t.Setenv(jev.EnvFake, "true")
	t.Setenv(jev.EnvKey, "")
	reqs := lpRequests()
	if len(reqs) != 3 {
		t.Fatalf("lpRequests() = %d request(s), want 3", len(reqs))
	}
	purposeOf := map[jev.QuestionType]jev.Purpose{
		jev.TypeNoul:   jev.PurposeValueCheck,
		jev.TypeChoice: jev.PurposeDocumentType,
		jev.TypeScore:  jev.PurposeMappingCheck,
	}
	var buf bytes.Buffer
	c, err := jev.FromEnv(slog.New(slog.NewJSONHandler(&buf, nil)))
	if err != nil {
		t.Fatalf("FromEnv in fake mode: %v", err)
	}
	seen := map[jev.QuestionType]int{}
	for i, r := range reqs {
		if r.State == "" || r.State != reqs[0].State {
			t.Errorf("request %d State %q, want the one non-empty synthetic text all three share", i, r.State)
		}
		if len(r.Questions) != 1 {
			t.Errorf("request %d has %d question(s), want 1", i, len(r.Questions))
		}
		for id, q := range r.Questions {
			seen[q.Type]++
			if want, ok := purposeOf[q.Type]; !ok || r.Purpose != want {
				t.Errorf("request %d question %q type %q purpose %q, want the purpose %q", i, id, q.Type, r.Purpose, want)
			}
			if q.Type != jev.TypeNoul && len(q.Options) != 3 {
				t.Errorf("request %d %s question has %d option(s), want 3", i, q.Type, len(q.Options))
			}
		}

		buf.Reset()
		if _, err := c.Ask(t.Context(), r); err != nil {
			t.Errorf("Ask(request %d) = %v, want nil: the client refused it", i, err)
		}
		lines := lpCallLines(t, buf.Bytes())
		if len(lines) != 1 {
			t.Fatalf("Ask(request %d) logged %d jev call line(s), want 1", i, len(lines))
		}
		if got := lines[0]["outcome"]; got != "fake" {
			t.Errorf("Ask(request %d) outcome %v, want fake", i, got)
		}
	}
	for typ := range purposeOf {
		if seen[typ] != 1 {
			t.Errorf("%d %s question(s) across lpRequests(), want exactly 1", seen[typ], typ)
		}
	}
}

func TestLiveProbeZDR_ParsesTheTypeSafeVersion(t *testing.T) {
	got, err := lpZDRVersions([]byte(lpZDRFixture))
	if err != nil {
		t.Fatalf("lpZDRVersions(P24 body) err = %v, want nil", err)
	}
	if len(got) != 1 || !got[lpZDRVersion] {
		t.Errorf("lpZDRVersions(P24 body) = %v, want exactly {%s}", got, lpZDRVersion)
	}
}

var lpReportRE = regexp.MustCompile(`^probe score: outcome=ok model=typesafe/jev-1\.13-20260917 on_zdr_list=true latency_ms=412 attempts=1 cost=(\S+)$`)

func TestLiveProbeVerdict_RejectsEachBadField(t *testing.T) {
	zdr := map[string]bool{lpZDRVersion: true}
	good := lpLine{Type: jev.TypeScore, Purpose: string(jev.PurposeMappingCheck), Model: lpZDRVersion,
		Outcome: "ok", Attempts: 1, Cost: 0.0000126, LatencyMS: 412}
	fields := []string{"outcome", "attempts", "cost", "model", "zdr"}

	for _, tc := range []struct {
		name  string
		edit  func(*lpLine)
		want  string   // the field the message must name
		alsos []string // other fields a line may also fail on
	}{
		{"outcome_skipped_refused", func(l *lpLine) { l.Outcome = "skipped_refused" }, "outcome", nil},
		{"attempts_2", func(l *lpLine) { l.Attempts = 2 }, "attempts", nil},
		{"cost_0", func(l *lpLine) { l.Cost = 0 }, "cost", nil},
		{"model_empty", func(l *lpLine) { l.Model = "" }, "model", []string{"zdr"}},
		{"model_not_typesafe_jev", func(l *lpLine) { l.Model = "google/gemini" }, "model", []string{"zdr"}},
		{"model_off_the_zdr_list", func(l *lpLine) { l.Model = "typesafe/jev-9.9-20990101" }, "zdr", []string{"model"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := good
			tc.edit(&line)
			report, err := lpVerdict(line, zdr)
			if err == nil {
				t.Fatalf("lpVerdict(%+v) = nil, want a failure naming %s", line, tc.want)
			}
			msg := strings.ToLower(err.Error())
			if !strings.Contains(msg, tc.want) {
				t.Errorf("lpVerdict error %q does not name %s", err, tc.want)
			}
			for _, f := range fields {
				if f != tc.want && !slices.Contains(tc.alsos, f) && strings.Contains(msg, f) {
					t.Errorf("lpVerdict error %q names %s, which the line passes", err, f)
				}
			}
			if tc.want == "zdr" && !strings.Contains(report, "on_zdr_list=false") {
				t.Errorf("report %q, want on_zdr_list=false", report)
			}
		})
	}

	t.Run("good_line_passes", func(t *testing.T) {
		report, err := lpVerdict(good, zdr)
		if err != nil {
			t.Fatalf("lpVerdict(good line) = %v, want nil", err)
		}
		m := lpReportRE.FindStringSubmatch(report)
		if m == nil {
			t.Fatalf("report %q does not match %s", report, lpReportRE)
		}
		if cost, err := strconv.ParseFloat(m[1], 64); err != nil || cost != good.Cost {
			t.Errorf("report cost %q, want %v", m[1], good.Cost)
		}
	})
}

// The build blanks the key and turns fake on, so a verdict that passes here cannot tell ok from fake.
func TestLiveProbeRun_FakeBuildIsRejectedByTheVerdict(t *testing.T) {
	t.Setenv(lpEnvProbe, "1")
	t.Setenv(jev.EnvKey, lpSpecKey)
	t.Setenv(jev.EnvFake, "")
	if open, reason := lpGate(); !open {
		t.Fatalf("lpGate() closed (%q), want open for this control", reason)
	}

	builds := 0
	var asked []string
	build := func(l *slog.Logger) (*jev.Client, error) {
		builds++
		if l == nil {
			t.Fatal("lpRun passed a nil logger; it has no line to parse")
		}
		t.Setenv(jev.EnvFake, "true")
		t.Setenv(jev.EnvKey, "")
		return jev.FromEnv(slog.New(lpRecorder{l.Handler(), &asked}))
	}
	var lines []lpLine
	lpNoDials(t, func() { lines = lpRun(t, build) })

	if builds == 0 {
		t.Fatal("lpRun never called build")
	}
	if len(asked) != 3 {
		t.Fatalf("the client logged %d jev call(s), want 3: one Ask per request", len(asked))
	}
	if len(lines) != 3 {
		t.Fatalf("lpRun returned %d line(s), want 3", len(lines))
	}
	zdr := map[string]bool{lpZDRVersion: true}
	var types []string
	var purposes []string
	for _, l := range lines {
		types = append(types, string(l.Type))
		purposes = append(purposes, l.Purpose)
		if l.Outcome != "fake" {
			t.Errorf("%s line outcome %q, want fake", l.Type, l.Outcome)
		}
		if _, err := lpVerdict(l, zdr); err == nil || !strings.Contains(strings.ToLower(err.Error()), "outcome") {
			t.Errorf("lpVerdict(fake %s line) = %v, want a failure naming outcome", l.Type, err)
		}
	}
	slices.Sort(types)
	if want := []string{"choice", "noul", "score"}; !slices.Equal(types, want) {
		t.Errorf("line types %v, want %v", types, want)
	}
	slices.Sort(purposes)
	slices.Sort(asked)
	if !slices.Equal(purposes, asked) {
		t.Errorf("parsed purposes %v, want the logged %v", purposes, asked)
	}
}
