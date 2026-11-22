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
	"slices"
	"strconv"
	"strings"
	"sync"
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
	confirmRoute  = "/internal/public-checks/confirm"
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

// storeMergesContacts reports whether e is a readinesscheck.Store literal whose Contacts field is rw.Store.
func storeMergesContacts(e ast.Expr) bool {
	if u, ok := e.(*ast.UnaryExpr); ok {
		e = u.X
	}
	lit, ok := e.(*ast.CompositeLit)
	if !ok || wtExprString(lit.Type) != "readinesscheck.Store" {
		return false
	}
	for _, el := range lit.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok && wtExprString(kv.Key) == "Contacts" && wtExprString(kv.Value) == "rw.Store" {
			return true
		}
	}
	return false
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
	if args := regCalls[0].Args; len(args) == 2 {
		if c, ok := args[1].(*ast.CallExpr); ok && len(c.Args) == 4 && !storeMergesContacts(c.Args[1]) {
			problems = append(problems, "the service's store must be &readinesscheck.Store{..., Contacts: rw.Store}: the press merges the contact in its transaction")
		}
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
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if problems := publicCheckWiringProblems(t, f); len(problems) != 0 {
		t.Errorf("main.go wiring: %v", problems)
	}
}

// metadataStub stands in for the Cloud Run metadata server: it serves the project id and the region and
// records every path asked, so a boot that skipped the read is visible.
type metadataStub struct {
	host  string
	mu    sync.Mutex
	paths []string
}

func newMetadataStub(t *testing.T) *metadataStub {
	t.Helper()
	m := &metadataStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.paths = append(m.paths, r.URL.Path)
		m.mu.Unlock()
		w.Header().Set("Metadata-Flavor", "Google")
		switch r.URL.Path {
		case "/computeMetadata/v1/project/project-id":
			_, _ = io.WriteString(w, "erp-boot-project")
		case "/computeMetadata/v1/instance/region":
			_, _ = io.WriteString(w, "projects/1/regions/africa-south1")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	m.host = strings.TrimPrefix(srv.URL, "http://")
	return m
}

func (m *metadataStub) asked(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Contains(m.paths, "/computeMetadata/v1/"+path)
}

// bootWorker starts the binary against the dev DB and returns its base URL once /healthz answers.
func bootWorker(t *testing.T, deployEnv string) string {
	t.Helper()
	base, _ := bootWorkerWithMetadata(t, deployEnv)
	return base
}

// bootWorkerWithMetadata is bootWorker that also returns the metadata stub a preview or nightly boot reads.
// The ERP variables are those preview.sh sets; the stub keeps the boot off the network and off Secret Manager.
func bootWorkerWithMetadata(t *testing.T, deployEnv string) (string, *metadataStub) {
	t.Helper()
	appURL, readerURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_READER_URL")
	if appURL == "" || readerURL == "" {
		t.Skip("worker boot test skipped: set DATABASE_URL and DATABASE_READER_URL (or run `make test-db PKGS=./cmd/worker/...`)")
	}
	port := freePort(t)
	vars := map[string]string{"PORT": strconv.Itoa(port), "DATABASE_URL": appURL, "DATABASE_READER_URL": readerURL,
		"VALIDATION_URL": "http://validation.invalid", "S2S_TOKEN": "tok", "DEPLOY_ENV": deployEnv}
	md := newMetadataStub(t)
	if deployEnv == "nightly" || strings.HasPrefix(deployEnv, "pr-") {
		vars["ERP_REDIRECT_URL"] = "https://gateway.invalid/erp/callback"
		vars["ERP_SECRET_SCOPE"] = "slot-1"
		vars["GCE_METADATA_HOST"] = md.host
	}
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
				return base, md
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("worker did not answer /health within %s\n%s", bootWait, out.String())
	return "", nil
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
	for _, route := range []string{runRoute, mappingRoute, confirmRoute} {
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

// A preview worker captures mail; the route must still be absent there.
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
	for _, route := range []string{runRoute, mappingRoute, confirmRoute} {
		if got := post(route, ""); got == http.StatusNotFound {
			t.Errorf("POST %s = 404, want the route registered", route)
		}
	}
}

// A preview worker builds its ERP registry and secret store at boot: it reads the project id and the region from the
// metadata server, once, and makes no Secret Manager call.
func TestWorker_PreviewBootReadsTheProjectAndRegionFromMetadata(t *testing.T) {
	_, md := bootWorkerWithMetadata(t, "pr-1")
	for _, path := range []string{"project/project-id", "instance/region"} {
		if !md.asked(path) {
			t.Errorf("the preview worker booted without reading %s from the metadata server (asked %v)", path, md.paths)
		}
	}
}

// The gateway forwards the callback here; the ID token is checked by Cloud Run, so only the gateway token is observable.
func TestWorkerRoutes_ERPCallbackNeedsTheGatewayToken(t *testing.T) {
	base := bootWorker(t, "pr-1")
	const route = "/internal/erp/callback"
	body := `{"state":"not-a-state","code":"","error":""}`
	for name, tok := range map[string]string{"no token": "", "wrong token": "not-the-token"} {
		if got := post(t, base+route, tok, strings.NewReader(body)); got != http.StatusUnauthorized {
			t.Errorf("POST %s with %s = %d, want 401", route, name, got)
		}
	}
	// The control: past the guard the route answers its outcome, so the 401 above came from the guard and not from a missing route.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+route, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Gateway-Token", "gw-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(answer)) != `{"outcome":"invalid"}` {
		t.Errorf("POST %s with the gateway token = %d %s, want 200 {\"outcome\":\"invalid\"}", route, resp.StatusCode, answer)
	}
}
