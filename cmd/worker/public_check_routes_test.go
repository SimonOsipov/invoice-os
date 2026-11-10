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
	"github.com/SimonOsipov/invoice-os/internal/notifications"
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

func TestWorkerMain_PublicCheckRoutesSitBehindTheGatewayToken(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	body := wtMainBody(t, f)
	reg, regCalls := callIndexes(body, "registerPublicCheckRoutes")
	gate, gateCalls := callIndexes(body, "app.RequireGateway")
	run, _ := callIndexes(body, "app.Run")
	if len(reg) != 1 || len(gate) != 1 || len(run) != 1 {
		t.Fatalf("main() has %d registerPublicCheckRoutes, %d app.RequireGateway and %d app.Run statements, want 1 each", len(reg), len(gate), len(run))
	}
	if !(reg[0] < gate[0] && gate[0] < run[0]) {
		t.Errorf("statement order: routes %d, RequireGateway %d, Run %d; want routes, then the guard, then Run", reg[0], gate[0], run[0])
	}
	if args := regCalls[0].Args; len(args) != 3 || wtExprString(args[2]) != "nl.Sender" {
		t.Errorf("registerPublicCheckRoutes must take the newsletter wiring's sender (nl.Sender) as its last argument, so the capture gate reads the sender that sends")
	}
	args := gateCalls[0].Args
	if len(args) != 1 {
		t.Fatalf("app.RequireGateway has %d arguments, want only the token: the worker has no open peer route", len(args))
	}
	if c, ok := args[0].(*ast.CallExpr); !ok || wtCallName(c.Fun) != "mustEnv" || len(c.Args) != 1 || wtExprString(c.Args[0]) != `"GATEWAY_TOKEN"` {
		t.Errorf(`app.RequireGateway token is %s, want mustEnv("GATEWAY_TOKEN")`, wtExprString(args[0]))
	}
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
	base := bootWorker(t, "pr-1") // a preview captures mail, so all three routes exist
	for _, route := range []string{runRoute, mappingRoute, capturedRoute} {
		for name, tok := range map[string]string{"no token": "", "wrong token": "not-the-token"} {
			if got := post(t, base+route, tok, strings.NewReader(`{"to":"a@x.io"}`)); got != http.StatusUnauthorized {
				t.Errorf("POST %s with %s = %d, want 401", route, name, got)
			}
		}
		// The control: past the guard the route exists, so the 401 above came from the guard and not from a missing route.
		if got := post(t, base+route, "gw-token", strings.NewReader(`{"to":"a@x.io"}`)); got == http.StatusUnauthorized || got == http.StatusNotFound {
			t.Errorf("POST %s with the gateway token = %d, want the route to answer", route, got)
		}
	}
	if resp, err := http.Get(base + "/health"); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("GET /health with no token: %v %v, want 200", resp, err)
	}
}

func TestWorker_CapturedMailRouteIsAbsentWhenTheWorkerDoesNotCapture(t *testing.T) {
	base := bootWorker(t, "")
	if got := post(t, base+capturedRoute, "gw-token", strings.NewReader(`{"to":"a@x.io"}`)); got != http.StatusNotFound {
		t.Errorf("POST %s on a worker with no sending key = %d, want 404", capturedRoute, got)
	}
	if got := post(t, base+runRoute, "gw-token", strings.NewReader(`{}`)); got == http.StatusNotFound || got == http.StatusUnauthorized {
		t.Errorf("POST %s with the gateway token = %d, want the route to answer", runRoute, got)
	}
}

func TestWorkerMain_CapturedMailRouteFollowsThePosture(t *testing.T) {
	nightlyKey := map[string]string{"RESEND_SENDING_KEY": "sk", "RESEND_RECEIVING_DOMAIN": "x.resend.app"}
	for _, tc := range []struct {
		name, env string
		vars      map[string]string
		capture   bool
	}{
		{"preview", "pr-7", nil, true},
		{"nightly with no sending key", "nightly", nil, true},
		{"production with a sending key", "production", map[string]string{"RESEND_SENDING_KEY": "sk"}, false},
		{"production with no sending key", "production", nil, false},
		{"nightly with a key and a receiving domain", "nightly", nightlyKey, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(k string) string { return tc.vars[k] }
			nl, err := notifications.NewsletterClients(getenv, tc.env, notifications.ModeFake, http.DefaultTransport)
			if err != nil {
				t.Fatalf("NewsletterClients: %v", err)
			}
			_, isCapture := nl.Sender.(*accountmail.Capture)
			if isCapture != tc.capture {
				t.Fatalf("setup: sender is %T, capture = %v, want capture = %v", nl.Sender, isCapture, tc.capture)
			}
			svc := readinesscheck.NewService(&publiccheck.Checker{}, nil, nl.Sender, slog.New(slog.NewTextHandler(io.Discard, nil)))
			mux := http.NewServeMux()
			registerPublicCheckRoutes(mux, svc, nl.Sender)

			if c, ok := nl.Sender.(*accountmail.Capture); ok {
				_ = c.Send(t.Context(), []accountmail.Message{{To: "a@x.io", Subject: "subject-one", HTML: "html-one"}})
			}
			get := func(route, body string) *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, route, strings.NewReader(body))
				mux.ServeHTTP(rec, req)
				return rec
			}
			for _, route := range []string{runRoute, mappingRoute} {
				if rec := get(route, ""); rec.Code == http.StatusNotFound {
					t.Errorf("POST %s = 404 on %s, want the route registered in every posture", route, tc.name)
				}
			}
			rec := get(capturedRoute, `{"to":"a@x.io"}`)
			if !tc.capture {
				if rec.Code != http.StatusNotFound {
					t.Errorf("POST %s = %d on %s, want 404", capturedRoute, rec.Code, tc.name)
				}
				return
			}
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "subject-one") {
				t.Errorf("POST %s = %d %q on %s, want 200 with the captured mail", capturedRoute, rec.Code, rec.Body.String(), tc.name)
			}
		})
	}
}
