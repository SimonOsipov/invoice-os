package platform_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	gwToken     = "gw-boot-test-token"
	gwS2SToken  = "s2s-test"
	gwForgedUID = "11111111-1111-4111-8111-111111111111"
	gwForgedTID = "22222222-2222-4222-8222-222222222222"
	gwSevenSvcs = 7
)

// gwRoutedServices reads routedServices from the gateway main, the one list of context services.
func gwRoutedServices(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "../../cmd/gateway/main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/gateway/main.go: %v", err)
	}
	var names []string
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "routedServices" || len(vs.Values) != 1 {
			return true
		}
		found = true
		if cl, ok := vs.Values[0].(*ast.CompositeLit); ok {
			for _, e := range cl.Elts {
				if bl, ok := e.(*ast.BasicLit); ok && bl.Kind == token.STRING {
					if s, err := strconv.Unquote(bl.Value); err == nil {
						names = append(names, s)
					}
				}
			}
		}
		return false
	})
	if !found {
		t.Fatal("cmd/gateway/main.go declares no routedServices")
	}
	return names
}

func gwFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// gwBootEnv is the boot environment of one main, minus GATEWAY_TOKEN.
func gwBootEnv(svc string, port int) []string {
	env := []string{
		"HOME=" + os.Getenv("HOME"),
		"PORT=" + strconv.Itoa(port),
		"SENTRY_DSN=",
		"DATABASE_URL=" + os.Getenv("DATABASE_URL"),
	}
	docs := []string{
		"DOCUMENT_BUCKET=gw-boot-test",
		"DOCUMENT_ENDPOINT=http://127.0.0.1:1",
		"DOCUMENT_REGION=us-east-1",
		"DOCUMENT_ACCESS_KEY_ID=gw-boot-test",
		"DOCUMENT_SECRET_ACCESS_KEY=gw-boot-test",
	}
	fakes := []string{"AI_FAKE=true", "JEV_FAKE=true"}
	switch svc {
	case "invoice":
		env = append(env, docs...)
		env = append(env, fakes...)
		env = append(env, "VALIDATION_URL=http://127.0.0.1:1", "S2S_TOKEN="+gwS2SToken)
	case "validation":
		env = append(env, "S2S_TOKEN="+gwS2SToken)
	case "submission":
		env = append(env, docs...)
		env = append(env, fakes...)
		env = append(env, "APP_ADAPTER=mock", "EXTRACTOR=mock")
	}
	return env
}

type gwOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *gwOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

func (o *gwOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

type gwProc struct {
	cmd  *exec.Cmd
	out  *gwOutput
	done chan struct{}
	exit int
	port int
}

func gwStart(t *testing.T, bin string, port int, env []string) *gwProc {
	t.Helper()
	p := &gwProc{out: &gwOutput{}, done: make(chan struct{}), port: port}
	p.cmd = exec.Command(bin)
	p.cmd.Env = env
	p.cmd.Stdout, p.cmd.Stderr = p.out, p.out
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	go func() {
		err := p.cmd.Wait()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			p.exit = ee.ExitCode()
		}
		close(p.done)
	}()
	t.Cleanup(func() {
		_ = p.cmd.Process.Kill()
		<-p.done
	})
	return p
}

func (p *gwProc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *gwProc) url(path string) string { return fmt.Sprintf("http://127.0.0.1:%d%s", p.port, path) }

func (p *gwProc) healthy() bool {
	resp, err := http.Get(p.url("/healthz"))
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// gwWaitHealthy returns once /healthz answers 200, and fails when the process exits first.
func gwWaitHealthy(t *testing.T, p *gwProc, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if p.exited() {
			t.Fatalf("main exited %d before /healthz answered:\n%s", p.exit, p.out)
		}
		if p.healthy() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("/healthz did not answer within %s:\n%s", limit, p.out)
}

func (p *gwProc) do(t *testing.T, method, path, body string, headers map[string]string) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, p.url(path), rd)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func gwForged() map[string]string {
	return map[string]string{
		"X-Tenant-ID": gwForgedTID,
		"X-User-ID":   gwForgedUID,
		"X-User-Role": "admin",
	}
}

func gwWith(h map[string]string, k, v string) map[string]string {
	out := map[string]string{k: v}
	for hk, hv := range h {
		out[hk] = hv
	}
	return out
}

const gwUnauthorizedBody = "{\"error\":\"unauthorized\"}\n"

// Each real context-service main refuses a request the gateway did not sign. A built
// binary is the only layer that sees a main that never calls RequireGateway.
func TestRLS_EveryContextServiceRefusesAForgedRequest(t *testing.T) {
	services := gwRoutedServices(t)
	if len(services) == 0 {
		t.Fatal("routedServices is empty: the subtests below would assert nothing")
	}
	if len(services) != gwSevenSvcs {
		t.Fatalf("routedServices holds %d services %v, want %d", len(services), services, gwSevenSvcs)
	}
	for _, svc := range services {
		if _, err := os.Stat(filepath.Join("..", "..", "cmd", svc, "main.go")); err != nil {
			t.Errorf("routedServices names %q but cmd/%s/main.go does not exist: %v", svc, svc, err)
		}
	}
	if t.Failed() {
		return
	}
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set")
	}

	for _, svc := range services {
		t.Run(svc, func(t *testing.T) {
			bin := buildMain(t, svc)
			port := gwFreePort(t)
			p := gwStart(t, bin, port, append(gwBootEnv(svc, port), "GATEWAY_TOKEN="+gwToken))
			gwWaitHealthy(t, p, 90*time.Second)

			t.Run("refuses forged identity headers without the token", func(t *testing.T) {
				code, body := p.do(t, "GET", "/v1/ping", "", gwForged())
				if code != http.StatusUnauthorized || body != gwUnauthorizedBody {
					t.Errorf("GET /v1/ping, forged identity, no token = %d %q, want 401 %q", code, body, gwUnauthorizedBody)
				}
			})

			t.Run("refuses a wrong gateway token", func(t *testing.T) {
				code, _ := p.do(t, "GET", "/v1/ping", "", gwWith(gwForged(), "X-Gateway-Token", gwToken+"x"))
				if code != http.StatusUnauthorized {
					t.Errorf("GET /v1/ping, wrong token = %d, want 401", code)
				}
			})

			t.Run("refuses the peer credential in place of the gateway token", func(t *testing.T) {
				code, _ := p.do(t, "GET", "/v1/ping", "", gwWith(gwForged(), "X-S2S-Token", gwS2SToken))
				if code != http.StatusUnauthorized {
					t.Errorf("GET /v1/ping with X-S2S-Token only = %d, want 401", code)
				}
			})

			t.Run("refuses an unmatched path without the token", func(t *testing.T) {
				code, body := p.do(t, "GET", "/v1/no-such-route", "", nil)
				if code != http.StatusUnauthorized || body != gwUnauthorizedBody {
					t.Errorf("GET /v1/no-such-route, no token = %d %q, want 401 %q", code, body, gwUnauthorizedBody)
				}
			})

			t.Run("admits a request carrying the token", func(t *testing.T) {
				code, body := p.do(t, "GET", "/v1/ping", "", gwWith(gwForged(), "X-Gateway-Token", gwToken))
				var got map[string]string
				if err := json.Unmarshal([]byte(body), &got); err != nil || code != http.StatusOK ||
					got["service"] != svc || got["status"] != "ok" {
					t.Errorf("GET /v1/ping with token = %d %q, want 200 {service:%q,status:ok}", code, body, svc)
				}
			})

			t.Run("keeps health routes open without the token", func(t *testing.T) {
				if code, _ := p.do(t, "GET", "/healthz", "", gwForged()); code != http.StatusOK {
					t.Errorf("GET /healthz, no token = %d, want 200", code)
				}
				if code, _ := p.do(t, "GET", "/readyz", "", gwForged()); code == http.StatusUnauthorized {
					t.Errorf("GET /readyz, no token = 401, want it open")
				}
			})

			t.Run("refuses to boot without GATEWAY_TOKEN", func(t *testing.T) {
				for _, c := range []struct {
					name string
					env  []string
				}{
					{"unset", nil},
					{"empty", []string{"GATEWAY_TOKEN="}},
				} {
					t.Run(c.name, func(t *testing.T) {
						port := gwFreePort(t)
						bp := gwStart(t, bin, port, append(gwBootEnv(svc, port), c.env...))
						deadline := time.Now().Add(60 * time.Second)
						for !bp.exited() && time.Now().Before(deadline) {
							if bp.healthy() {
								t.Fatalf("%s booted and answered /healthz with GATEWAY_TOKEN %s", svc, c.name)
							}
							time.Sleep(100 * time.Millisecond)
						}
						if !bp.exited() {
							t.Fatalf("%s neither exited nor served within 60s with GATEWAY_TOKEN %s:\n%s", svc, c.name, bp.out)
						}
						want := svc + ": GATEWAY_TOKEN is required"
						if bp.exit != 1 || !strings.Contains(bp.out.String(), want) {
							t.Errorf("exit = %d, output %q: want exit 1 naming %q", bp.exit, bp.out, want)
						}
					})
				}
			})

			if svc != "validation" {
				return
			}
			t.Run("peer route stays open to the S2S token alone", func(t *testing.T) {
				const batch = `{"invoices":[]}`
				code, _ := p.do(t, "POST", "/v1/validate/batch", batch, map[string]string{"X-S2S-Token": gwS2SToken})
				if code == http.StatusUnauthorized || code == http.StatusNotFound {
					t.Errorf("POST /v1/validate/batch, S2S token only = %d, want the route reached", code)
				}
				code, body := p.do(t, "POST", "/v1/validate/batch", batch, nil)
				if code != http.StatusUnauthorized || body != gwUnauthorizedBody {
					t.Errorf("POST /v1/validate/batch, neither token = %d %q, want 401 %q", code, body, gwUnauthorizedBody)
				}
				code, _ = p.do(t, "POST", "/v1/validate/batch", batch, map[string]string{"X-Gateway-Token": gwToken})
				if code != http.StatusUnauthorized {
					t.Errorf("POST /v1/validate/batch, gateway token only = %d, want 401 from the S2S check", code)
				}
			})
		})
	}
}
