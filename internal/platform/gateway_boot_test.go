package platform_test

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	case "reconciliation":
		env = append(env, "VALIDATION_URL=http://127.0.0.1:1", "S2S_TOKEN="+gwS2SToken)
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

const gwRefusalLog = "request refused: no gateway token"

type gwRefusalKey struct{ method, path string }

// gwRefusals counts the guard's refusal lines by exact method and path.
func gwRefusals(p *gwProc) map[gwRefusalKey]int {
	got := map[gwRefusalKey]int{}
	for _, line := range strings.Split(p.out.String(), "\n") {
		var rec struct{ Msg, Method, Path string }
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Msg == gwRefusalLog {
			got[gwRefusalKey{rec.Method, rec.Path}]++
		}
	}
	return got
}

// gwAwaitRefusal polls up to 5s for the child's pipe to deliver n refusal lines for k.
func gwAwaitRefusal(p *gwProc, k gwRefusalKey, n int) bool {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if gwRefusals(p)[k] >= n {
			return true
		}
	}
	return false
}

// gwSettledRefusals returns the counts once ready holds and two reads 250ms apart agree, or after 5s.
func gwSettledRefusals(p *gwProc, ready func(map[gwRefusalKey]int) bool) map[gwRefusalKey]int {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		a := gwRefusals(p)
		if !ready(a) {
			continue
		}
		time.Sleep(250 * time.Millisecond)
		if b := gwRefusals(p); maps.Equal(a, b) {
			return b
		}
	}
	return gwRefusals(p)
}

const gwUnauthorizedBody = "{\"error\":\"unauthorized\"}\n"

// gwTenantRoutes holds real tenant-owned routes per service; notifications registers none.
var gwTenantRoutes = map[string][][2]string{
	"tenancy": {
		{"GET", "/v1/me"}, {"GET", "/v1/memberships"},
		{"PATCH", "/v1/memberships/" + gwForgedUID}, {"POST", "/v1/workspaces"},
	},
	"portfolio": {
		{"GET", "/v1/entities"}, {"POST", "/v1/entities"}, {"POST", "/v1/entities/x/offboard"},
	},
	"invoice": {
		{"GET", "/v1/invoices"}, {"POST", "/v1/invoices"}, {"GET", "/v1/audit-log"},
		{"POST", "/v1/invoices/submissions"}, {"POST", "/v1/imports/preview"},
	},
	"validation":    {{"PATCH", "/v1/rules/x"}},
	"submission":    {{"GET", "/v1/extractions"}, {"POST", "/v1/documents"}},
	"dashboard":     {{"GET", "/v1/rollup"}},
	"notifications": nil,
}

// gwStandInDB listens where a Postgres would; every accepted connection is database contact.
func gwStandInDB(t *testing.T) (dsn string, accepted *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("stand-in database: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted = new(atomic.Int32)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	return fmt.Sprintf("postgres://u:p@%s/db?sslmode=disable", ln.Addr()), accepted
}

// gwDatabaseURL returns dsn with its database name replaced.
func gwDatabaseURL(t *testing.T, dsn, name string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

// gwThrowawayDB copies the job's database (roles, schema, grants) into a fresh one and drops it
// at cleanup: invoice's boot seeds demo approval runs, which the shared database must not keep.
func gwThrowawayDB(t *testing.T, superDSN string) string {
	t.Helper()
	ctx := t.Context()
	u, err := url.Parse(superDSN)
	if err != nil {
		t.Fatalf("parse DATABASE_SUPERUSER_URL: %v", err)
	}
	src := strings.TrimPrefix(u.Path, "/")
	name := fmt.Sprintf("gwboot_%d", time.Now().UnixNano())
	conn, err := pgx.Connect(ctx, gwDatabaseURL(t, superDSN, "postgres"))
	if err != nil {
		t.Fatalf("connect as superuser: %v", err)
	}
	t.Cleanup(func() {
		defer conn.Close(context.Background())
		if _, err := conn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop %s: %v", name, err)
		}
	})
	// the template needs an idle source database; wait out a session that is closing
	create := fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, pgx.Identifier{src}.Sanitize())
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(250 * time.Millisecond) {
		_, err = conn.Exec(ctx, create)
		var pe *pgconn.PgError
		if err == nil || !errors.As(err, &pe) || pe.Code != "55006" || time.Now().After(deadline) {
			break
		}
	}
	if err != nil {
		t.Fatalf("create throwaway database: %v", err)
	}
	return name
}

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
	superDSN := os.Getenv("DATABASE_SUPERUSER_URL")
	if superDSN == "" {
		t.Skip("DATABASE_SUPERUSER_URL not set")
	}
	scratch := gwThrowawayDB(t, superDSN)
	t.Setenv("DATABASE_URL", gwDatabaseURL(t, os.Getenv("DATABASE_URL"), scratch))
	if r := os.Getenv("DATABASE_READER_URL"); r != "" {
		t.Setenv("DATABASE_READER_URL", gwDatabaseURL(t, r, scratch))
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

			t.Run("checks the rules role on the whole staff class", func(t *testing.T) {
				staff := func(h map[string]string, rules bool) map[string]string {
					out := gwWith(gwWith(h, "X-Gateway-Token", gwToken), "X-User-Staff", "true")
					if rules {
						out["X-User-Rules-Role"] = "true"
					}
					return out
				}
				for _, target := range []string{"/v1/staff/x", "/v1/staff", "/v1/./staff/x"} {
					if code, body := p.do(t, "GET", target, "", staff(gwForged(), false)); code != http.StatusForbidden || body != "{\"error\":\"forbidden\"}\n" {
						t.Errorf("GET %s, staff without the rules role = %d %q, want 403 forbidden", target, code, body)
					}
				}
				if code, body := p.do(t, "GET", "/v1/staff/x", "", map[string]string{"X-Gateway-Token": gwToken}); code != http.StatusForbidden || body != "{\"error\":\"forbidden\"}\n" {
					t.Errorf("GET /v1/staff/x, token and no caller = %d %q, want 403 forbidden", code, body)
				}
				// Control: with the rules role the check admits the request, so the 403s above are the check's.
				if code, body := p.do(t, "GET", "/v1/staff/x", "", staff(gwForged(), true)); code == http.StatusForbidden || code == http.StatusUnauthorized {
					t.Errorf("GET /v1/staff/x, staff with the rules role = %d %q, want the check to admit it", code, body)
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

			t.Run("refuses every real tenant route without the token", func(t *testing.T) {
				routes, ok := gwTenantRoutes[svc]
				if !ok {
					t.Fatalf("gwTenantRoutes has no entry for %q; list its tenant routes, or nil when it has none", svc)
				}
				for _, r := range routes {
					body := ""
					if r[0] != "GET" {
						body = "{}"
					}
					code, got := p.do(t, r[0], r[1], body, gwForged())
					if code != http.StatusUnauthorized || got != gwUnauthorizedBody {
						t.Errorf("%s %s, forged identity, no token = %d %q, want 401 %q", r[0], r[1], code, got, gwUnauthorizedBody)
					}
					code, got = p.do(t, r[0], r[1], body, gwWith(gwForged(), "X-Gateway-Token", gwToken))
					if code == http.StatusUnauthorized && got == gwUnauthorizedBody || got == "404 page not found\n" {
						t.Errorf("%s %s with the token = %d %q, want the route reached", r[0], r[1], code, got)
					}
				}
			})

			t.Run("a refused boot contacts no database", func(t *testing.T) {
				if svc != "notifications" {
					dsn, accepted := gwStandInDB(t)
					port := gwFreePort(t)
					cp := gwStart(t, bin, port, append(gwBootEnv(svc, port), "DATABASE_URL="+dsn, "GATEWAY_TOKEN="+gwToken))
					for deadline := time.Now().Add(60 * time.Second); accepted.Load() == 0 && !cp.exited(); {
						if time.Now().After(deadline) {
							t.Fatalf("%s never contacted the stand-in database with the token, so the run below proves nothing:\n%s", svc, cp.out)
						}
						_, _ = http.Get(cp.url("/readyz"))
						time.Sleep(100 * time.Millisecond)
					}
					if accepted.Load() == 0 {
						t.Fatalf("%s exited before it contacted the stand-in database:\n%s", svc, cp.out)
					}
				}
				dsn, accepted := gwStandInDB(t)
				port := gwFreePort(t)
				bp := gwStart(t, bin, port, append(gwBootEnv(svc, port), "DATABASE_URL="+dsn))
				select {
				case <-bp.done:
				case <-time.After(60 * time.Second):
					t.Fatalf("%s neither exited nor failed within 60s without GATEWAY_TOKEN:\n%s", svc, bp.out)
				}
				if n := accepted.Load(); n != 0 || bp.exit != 1 {
					t.Errorf("%s without GATEWAY_TOKEN: exit %d, %d database connection(s); want exit 1 and none:\n%s", svc, bp.exit, n, bp.out)
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

			t.Run("open pattern admits no other route, method or path form", func(t *testing.T) {
				const batch = `{"invoices":[]}`
				s2s := map[string]string{"X-S2S-Token": gwS2SToken}
				if code, _ := p.do(t, "POST", "/v1/validate/batch", batch, s2s); code == http.StatusUnauthorized || code == http.StatusNotFound {
					t.Fatalf("POST /v1/validate/batch, S2S token only = %d, want the route reached", code)
				}
				cases := []gwRefusalKey{
					{"GET", "/v1/validate/batch"},
					{"PUT", "/v1/validate/batch"},
					{"POST", "/v1/validate/batch/"},
					{"POST", "/V1/validate/batch"},
					{"POST", "/v1/validate/batch/extra"},
					{"PATCH", "/v1/rules/x"},
					{"GET", "/v1/ping"},
				}
				// The child logs in request order, so once the barrier line lands every earlier line has too.
				barrier := gwRefusalKey{"GET", "/v1/log-barrier"}
				p.do(t, barrier.method, barrier.path, "", gwForged())
				if !gwAwaitRefusal(p, barrier, 1) {
					t.Fatalf("no refusal line for %s %s within 5s:\n%s", barrier.method, barrier.path, p.out)
				}
				before := gwRefusals(p)
				for _, c := range cases {
					code, body := p.do(t, c.method, c.path, batch, gwWith(gwForged(), "X-S2S-Token", gwS2SToken))
					if code != http.StatusUnauthorized || body != gwUnauthorizedBody {
						t.Errorf("%s %s, S2S token and forged identity, no gateway token = %d %q, want 401 %q", c.method, c.path, code, body, gwUnauthorizedBody)
					}
					// a handler can answer the same 401; only the guard logs the refusal
					if !gwAwaitRefusal(p, c, before[c]+1) {
						t.Errorf("%s %s: guard logged no refusal line within 5s; the route reached its handler", c.method, c.path)
					}
				}
				after := gwSettledRefusals(p, func(m map[gwRefusalKey]int) bool {
					for _, c := range cases {
						if m[c] < before[c]+1 {
							return false
						}
					}
					return true
				})
				for _, c := range cases {
					if got := after[c] - before[c]; got != 1 {
						t.Errorf("%s %s: guard logged %d refusal line(s), want 1", c.method, c.path, got)
					}
				}
				total := func(m map[gwRefusalKey]int) (n int) {
					for _, v := range m {
						n += v
					}
					return n
				}
				if got := total(after) - total(before); got != len(cases) {
					t.Errorf("guard logged %d refusal line(s) for %d requests, want one each", got, len(cases))
				}
			})
		})
	}

	t.Run("reconciliation is not guarded and still serves health", func(t *testing.T) {
		if slices.Contains(services, "reconciliation") {
			t.Fatal("routedServices names reconciliation; D4 says the gateway gives it no proxy route")
		}
		port := gwFreePort(t)
		reader := cmp.Or(os.Getenv("DATABASE_READER_URL"), os.Getenv("DATABASE_URL"))
		p := gwStart(t, buildMain(t, "reconciliation"), port, append(gwBootEnv("reconciliation", port), "DATABASE_READER_URL="+reader))
		gwWaitHealthy(t, p, 90*time.Second)
		if code, body := p.do(t, "GET", "/v1/ping", "", gwForged()); code != http.StatusNotFound {
			t.Errorf("GET /v1/ping, forged identity, no token = %d %q, want the mux's 404: no guard (D4)", code, body)
		}
	})
}
