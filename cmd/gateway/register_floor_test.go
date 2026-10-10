package main

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/token"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRegistrationNotConfigured_DoesNotWait(t *testing.T) {
	authURL, calls := fakeAuth(t)
	log := slog.New(slog.DiscardHandler)
	const body = `{"email":"new@corp.example","password":"Corr3ct-Horse"}`

	// Control: with a site URL the same 150 ms minimum does hold the answer.
	site, _ := url.Parse("https://site.example")
	start := time.Now()
	if rec := serveRegistration(registrationHandlers(authURL, site, 150*time.Millisecond, log, nil, noPendingInvite).Register, http.MethodPost, "/auth/register", body); rec.Code != http.StatusAccepted {
		t.Fatalf("control Register = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("control answered after %v, want no earlier than 150ms", elapsed)
	}
	before := len(calls())

	reg := registrationHandlers(authURL, nil, 3*time.Second, log, nil, noPendingInvite)
	start = time.Now()
	rec := serveRegistration(reg.Register, http.MethodPost, "/auth/register", body)
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("Register = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 1 || got["error"] != "registration is not configured" {
		t.Errorf("body = %s, want {\"error\":\"registration is not configured\"}", rec.Body.String())
	}
	if elapsed >= time.Second {
		t.Errorf("answered after %v, want no wait while AUTH_SITE_URL is unset", elapsed)
	}
	if n := len(calls()); n != before {
		t.Errorf("GoTrue saw %d new calls, want none", n-before)
	}
}

func TestMustParseRegisterMinResponse_Values(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"":       2 * time.Second,
		"3s":     3 * time.Second,
		"1500ms": 1500 * time.Millisecond,
	} {
		if got := mustParseRegisterMinResponse(raw, slog.New(slog.DiscardHandler)); got != want {
			t.Errorf("mustParseRegisterMinResponse(%q) = %v, want %v", raw, got, want)
		}
	}
}

const minResponseChildEnv = "GATEWAY_TEST_MINRESPONSE_CHILD"

func runMinResponseChild(t *testing.T, raw string) (int, string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMustParseRegisterMinResponse_BadValueStopsBoot$", "-test.count=1")
	cmd.Env = append(os.Environ(), minResponseChildEnv+"=1", "AUTH_REGISTER_MIN_RESPONSE="+raw)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &exit):
		return exit.ExitCode(), string(out)
	}
	t.Fatalf("run child: %v", err)
	return -1, ""
}

// echoes reports whether any log attribute other than time carries raw as a standalone token.
func echoes(t *testing.T, out, raw string) bool {
	t.Helper()
	tok := regexp.MustCompile(`(?:^|[^0-9A-Za-z_-])` + regexp.QuoteMeta(raw) + `(?:[^0-9A-Za-z_]|$)`)
	for line := range strings.Lines(out) {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		delete(rec, "time")
		b, _ := json.Marshal(rec)
		if tok.Match(b) {
			return true
		}
	}
	return false
}

func TestMustParseRegisterMinResponse_BadValueStopsBoot(t *testing.T) {
	if os.Getenv(minResponseChildEnv) == "1" {
		mustParseRegisterMinResponse(os.Getenv("AUTH_REGISTER_MIN_RESPONSE"), slog.New(slog.NewJSONHandler(os.Stderr, nil)))
		os.Exit(0)
	}

	// Positive pair: unset and valid values boot, so exit 1 below is the parse failing.
	for _, raw := range []string{"", "3s", "1500ms"} {
		if code, out := runMinResponseChild(t, raw); code != 0 {
			t.Errorf("AUTH_REGISTER_MIN_RESPONSE=%q: exit %d, want 0 (log %q)", raw, code, out)
		}
	}

	for name, raw := range map[string]string{
		"bare number":   "2",
		"not a number":  "abc",
		"zero":          "0s",
		"negative":      "-1s",
		"canary string": "canary-q7zx",
		"padded":        " 3s",
		"split unit":    "3 s",
		"overflow":      "99999999999h",
		"unit only":     "ms",
	} {
		t.Run(name, func(t *testing.T) {
			code, out := runMinResponseChild(t, raw)
			if code != 1 {
				t.Errorf("exit %d, want 1 (log %q)", code, out)
			}
			if n := strings.Count(out, `"level":"ERROR"`); n != 1 {
				t.Errorf("%d ERROR lines, want 1: %q", n, out)
			}
			if !strings.Contains(out, "AUTH_REGISTER_MIN_RESPONSE") {
				t.Errorf("log %q does not name AUTH_REGISTER_MIN_RESPONSE", out)
			}
			if echoes(t, out, raw) {
				t.Errorf("log %q carries the raw value %q", out, raw)
			}
		})
	}
}

// main boots a full gateway, so the wiring is read from its source: comments are not in the AST,
// and only func main's own statements are walked.
func TestMainIgnoresAuthRegisterMinResponse_FailsWhenUnwired(t *testing.T) {
	_, body := parseMain(t)

	minVar := ""
	for _, st := range body.List {
		as, ok := st.(*ast.AssignStmt)
		if !ok || as.Tok != token.DEFINE || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			continue
		}
		call, ok := isCallTo(as.Rhs[0], "", "mustParseRegisterMinResponse")
		if !ok || len(call.Args) != 2 {
			continue
		}
		if env, ok := isCallTo(call.Args[0], "os", "Getenv"); ok && len(env.Args) == 1 && isStringLit(env.Args[0], "AUTH_REGISTER_MIN_RESPONSE") {
			if id, ok := as.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
				minVar = id.Name
			}
		}
	}
	if minVar == "" {
		t.Fatal(`main has no top-level x := mustParseRegisterMinResponse(os.Getenv("AUTH_REGISTER_MIN_RESPONSE"), ...)`)
	}

	calls, wired := 0, false
	ast.Inspect(body, func(n ast.Node) bool {
		if e, ok := n.(ast.Expr); ok {
			if call, ok := isCallTo(e, "", "registrationHandlers"); ok {
				calls++
				wired = wired || slices.ContainsFunc(call.Args, func(a ast.Expr) bool {
					id, ok := a.(*ast.Ident)
					return ok && id.Name == minVar
				})
			}
		}
		return true
	})
	if calls != 1 {
		t.Fatalf("main calls registrationHandlers %d times, want 1", calls)
	}
	if !wired {
		t.Errorf("registrationHandlers is not passed %s, the parsed AUTH_REGISTER_MIN_RESPONSE", minVar)
	}
}
