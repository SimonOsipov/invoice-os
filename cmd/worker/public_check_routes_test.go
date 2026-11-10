package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/publiccheck"
	"github.com/SimonOsipov/invoice-os/internal/readinesscheck"
)

const (
	runRoute      = "/internal/public-checks/run"
	mappingRoute  = "/internal/public-checks/mapping"
	capturedRoute = "/internal/public-checks/captured-mail"
)

// callIndexes returns the index of each top-level statement of body that holds a call named want.
func callIndexes(body []ast.Stmt, want string) (idx []int, calls []*ast.CallExpr) {
	for i, stmt := range body {
		ast.Inspect(stmt, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && wtCallName(c.Fun) == want {
				idx, calls = append(idx, i), append(calls, c)
			}
			return true
		})
	}
	return idx, calls
}

// publicCheckWiringProblems reports how main() in f departs from the public-check wiring: routes before the
// gateway guard before Run, two arguments, and the guarded sender (nl.Sender) as the service's third.
func publicCheckWiringProblems(t *testing.T, f *ast.File) []string {
	t.Helper()
	body := wtMainBody(t, f)
	reg, regCalls := callIndexes(body, "registerPublicCheckRoutes")
	gate, gateCalls := callIndexes(body, "app.RequireGateway")
	run, _ := callIndexes(body, "app.Run")
	if len(reg) != 1 || len(gate) != 1 || len(run) != 1 {
		return []string{"main() must hold exactly one registerPublicCheckRoutes, app.RequireGateway and app.Run statement"}
	}
	var problems []string
	if !(reg[0] < gate[0] && gate[0] < run[0]) {
		problems = append(problems, "statement order must be routes, then the guard, then Run")
	}
	if args := regCalls[0].Args; len(args) != 2 {
		problems = append(problems, "registerPublicCheckRoutes must take the mux and the service only")
	} else if c, ok := args[1].(*ast.CallExpr); !ok || wtCallName(c.Fun) != "readinesscheck.NewService" || len(c.Args) != 4 || wtExprString(c.Args[2]) != "nl.Sender" {
		problems = append(problems, "the service must be readinesscheck.NewService(..., nl.Sender, ...): the guarded sender")
	}
	if args := gateCalls[0].Args; len(args) != 1 {
		problems = append(problems, "app.RequireGateway must take only the token")
	} else if c, ok := args[0].(*ast.CallExpr); !ok || wtCallName(c.Fun) != "mustEnv" || len(c.Args) != 1 || wtExprString(c.Args[0]) != `"GATEWAY_TOKEN"` {
		problems = append(problems, `app.RequireGateway token must be mustEnv("GATEWAY_TOKEN")`)
	}
	return problems
}

func TestWorkerMain_PublicCheckRoutesSitBehindTheGatewayToken(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	parse := func(src []byte) *ast.File {
		f, err := parser.ParseFile(token.NewFileSet(), "main.go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	if problems := publicCheckWiringProblems(t, parse(src)); len(problems) != 0 {
		t.Errorf("main.go wiring: %v", problems)
	}
	t.Run("a raw sender in place of nl.Sender is reported", func(t *testing.T) {
		mutated := strings.Replace(string(src), "&readinesscheck.Store{Pool: pool}, nl.Sender,", "&readinesscheck.Store{Pool: pool}, nil,", 1)
		if mutated == string(src) {
			t.Fatal("main.go no longer holds the NewService call this mutation targets")
		}
		if len(publicCheckWiringProblems(t, parse([]byte(mutated)))) == 0 {
			t.Error("a NewService call without nl.Sender went unreported")
		}
	})
}

// bootWorker starts the binary against the dev DB and returns its base URL once /healthz answers.
func bootWorker(t *testing.T, deployEnv string) string {
	t.Helper()
	appURL, readerURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_READER_URL")
	if appURL == "" || readerURL == "" {
		t.Skip("worker boot test skipped: set DATABASE_URL and DATABASE_READER_URL (or run `make test-db PKGS=./cmd/worker/...`)")
	}
	port := freePort(t)
	vars := map[string]string{"PORT": strconv.Itoa(port), "DATABASE_URL": appURL, "DATABASE_READER_URL": readerURL,
		"VALIDATION_URL": "http://validation.invalid", "S2S_TOKEN": "tok", "DEPLOY_ENV": deployEnv}
	var out bytes.Buffer
	cmd := exec.Command(buildWorker(t))
	cmd.Env = bootEnv(vars)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	})
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(bootWait)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(base + "/health"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return base
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("worker did not answer /health within %s\n%s", bootWait, out.String())
	return ""
}

func post(t *testing.T, url, token string, body io.Reader) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("X-Gateway-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// The ID token is checked by Cloud Run before the request reaches the worker, so only the gateway token is observable here.
func TestWorker_PublicCheckRouteRefusesAMissingGatewayToken(t *testing.T) {
	base := bootWorker(t, "pr-1")
	for _, route := range []string{runRoute, mappingRoute} {
		for name, tok := range map[string]string{"no token": "", "wrong token": "not-the-token"} {
			if got := post(t, base+route, tok, strings.NewReader(`{}`)); got != http.StatusUnauthorized {
				t.Errorf("POST %s with %s = %d, want 401", route, name, got)
			}
		}
		// The control: past the guard the route exists, so the 401 above came from the guard and not from a missing route.
		if got := post(t, base+route, "gw-token", strings.NewReader(`{}`)); got == http.StatusUnauthorized || got == http.StatusNotFound {
			t.Errorf("POST %s with the gateway token = %d, want the route to answer", route, got)
		}
	}
	if resp, err := http.Get(base + "/health"); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("GET /health with no token: %v %v, want 200", resp, err)
	}
}

// A preview worker's sender captures, the one posture that once served the route.
func TestWorker_CapturedMailRouteIsGoneOnAPreview(t *testing.T) {
	base := bootWorker(t, "pr-1")
	if got := post(t, base+capturedRoute, "gw-token", strings.NewReader(`{"to":"a@x.io"}`)); got != http.StatusNotFound {
		t.Errorf("POST %s = %d, want 404", capturedRoute, got)
	}
	if got := post(t, base+runRoute, "gw-token", strings.NewReader(`{}`)); got == http.StatusNotFound || got == http.StatusUnauthorized {
		t.Errorf("POST %s with the gateway token = %d, want the route to answer", runRoute, got)
	}
}

func TestWorkerMain_CapturedMailRouteIsGone(t *testing.T) {
	svc := readinesscheck.NewService(&publiccheck.Checker{}, nil, &accountmail.Capture{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	registerPublicCheckRoutes(mux, svc)
	post := func(route, body string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, route, strings.NewReader(body)))
		return rec.Code
	}
	if got := post(capturedRoute, `{"to":"a@x.io"}`); got != http.StatusNotFound {
		t.Errorf("POST %s = %d, want 404", capturedRoute, got)
	}
	for _, route := range []string{runRoute, mappingRoute} {
		if got := post(route, ""); got == http.StatusNotFound {
			t.Errorf("POST %s = 404, want the route registered", route)
		}
	}
}
