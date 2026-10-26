package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
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
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func qaFourKeys(extra ...string) map[string]string {
	m := map[string]string{
		"HUBSPOT_TOKEN": "hs-secret-token", "RESEND_API_KEY": "rs-secret-key",
		"RESEND_SEGMENT_ID": "seg-1", "RESEND_TOPIC_ID": "top-1",
	}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i]] = extra[i+1]
	}
	return m
}

// ---------------------------------------------------------------------------
// The built binary: boot refusals, and the worker running only when delivering.
// ---------------------------------------------------------------------------

const qaUnreachableDB = "postgres://invoice_app:app@127.0.0.1:1/invoice_os?sslmode=disable"

func qaBuildNotifications(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "notifications")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build cmd/notifications: %v\n%s", err, out)
	}
	return bin
}

func qaFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type qaLockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *qaLockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *qaLockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

const qaGatewayToken = "gw-token-for-the-test"

type qaProc struct {
	cmd  *exec.Cmd
	out  *qaLockedBuf
	done chan error
	port int
}

// qaBootEnv is the whole environment of the child: nothing leaks in from the test process.
func qaBootEnv(port int, vars map[string]string) []string {
	env := []string{"HOME=" + os.Getenv("HOME"), "PORT=" + strconv.Itoa(port), "GATEWAY_TOKEN=" + qaGatewayToken}
	for k, v := range vars {
		env = append(env, k+"="+v)
	}
	return env
}

func qaStart(t *testing.T, bin string, vars map[string]string) *qaProc {
	t.Helper()
	p := &qaProc{out: &qaLockedBuf{}, done: make(chan error, 1), port: qaFreePort(t)}
	p.cmd = exec.Command(bin)
	p.cmd.Env = qaBootEnv(p.port, vars)
	p.cmd.Stdout, p.cmd.Stderr = p.out, p.out
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("start notifications: %v", err)
	}
	go func() { p.done <- p.cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(qaBootWait):
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}

const qaBootWait = 30 * time.Second

// qaWorkerBin builds cmd/worker once per test binary: it runs every delivery job.
var qaWorkerBin = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "qa-worker-")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "worker")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, "../worker").CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build cmd/worker: %v\n%s", err, out)
	}
	return bin, nil
})

// qaStartWithWorker boots notifications and the worker on the same vars; both write to one
// buffer, so every output assertion covers the delivering process too.
func qaStartWithWorker(t *testing.T, bin string, vars map[string]string) *qaProc {
	t.Helper()
	wbin, err := qaWorkerBin()
	if err != nil {
		t.Fatal(err)
	}
	p := qaStart(t, bin, vars)
	wvars := qaMerge(map[string]string{
		"DATABASE_READER_URL": os.Getenv("DATABASE_READER_URL"),
		"GATEWAY_URL":         "http://gateway.invalid", "VALIDATION_URL": "http://validation.invalid", "S2S_TOKEN": "tok",
		"APP_ADAPTER": "mock", "DOCUMENT_BUCKET": "b", "DOCUMENT_ENDPOINT": "http://127.0.0.1:1", "DOCUMENT_REGION": "r",
		"DOCUMENT_ACCESS_KEY_ID": "k", "DOCUMENT_SECRET_ACCESS_KEY": "s",
	}, vars)
	w := exec.Command(wbin)
	w.Env = qaBootEnv(qaFreePort(t), wvars)
	w.Stdout, w.Stderr = p.out, p.out
	if err := w.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = w.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = w.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(qaBootWait):
			_ = w.Process.Kill()
			<-done
		}
	})
	return p
}

func (p *qaProc) url(path string) string { return "http://127.0.0.1:" + strconv.Itoa(p.port) + path }

// waitFor polls path until it answers 200; a child that exits first fails the test.
func (p *qaProc) waitFor(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-p.done:
			t.Fatalf("notifications exited before %s answered: %v\n%s", path, err, p.out)
		default:
		}
		if resp, err := http.Get(p.url(path)); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s never answered 200\n%s", path, p.out)
}

func (p *qaProc) healthz(t *testing.T) map[string]string {
	t.Helper()
	resp, err := http.Get(p.url("/healthz"))
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /healthz: %v", err)
	}
	return body
}

func TestNotificationsMain_ModeErrorIsFatal(t *testing.T) {
	bin := qaBuildNotifications(t)

	for _, tc := range []struct {
		name string
		vars map[string]string
		want string
	}{
		{"partial keys", map[string]string{"HUBSPOT_TOKEN": "hs-secret-token"}, "partial vendor keys"},
		{"flag plus a key", map[string]string{"CONTACTS_FAKE": "true", "RESEND_API_KEY": "rs-secret-key"}, "unset one"},
		{"unreadable flag", map[string]string{"CONTACTS_FAKE": "banana"}, "not a boolean"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars := map[string]string{"DATABASE_URL": qaUnreachableDB}
			for k, v := range tc.vars {
				vars[k] = v
			}
			ctx, cancel := context.WithTimeout(t.Context(), qaBootWait)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin)
			cmd.Env = qaBootEnv(qaFreePort(t), vars)
			var out qaLockedBuf
			cmd.Stdout, cmd.Stderr = &out, &out
			err := cmd.Run()

			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("notifications ended with %v, want exit status 1 through platform.Fatal\n%s", err, &out)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("output does not hold %q\n%s", tc.want, &out)
			}
			for _, secret := range []string{"hs-secret-token", "rs-secret-key"} {
				if strings.Contains(out.String(), secret) {
					t.Errorf("output carries the secret %q", secret)
				}
			}
		})
	}

	t.Run("control: no keys boots and reports off", func(t *testing.T) {
		p := qaStart(t, bin, map[string]string{"DATABASE_URL": qaUnreachableDB})
		p.waitFor(t, "/healthz")
		if got := p.healthz(t)["contacts"]; got != "off" {
			t.Errorf("/healthz contacts = %q, want off\n%s", got, p.out)
		}
	})
}

// The worker is observed by what it does to a queued job, through the intake route.
func TestNotificationsMain_WorkerRunsOnlyWhenDelivering(t *testing.T) {
	appURL, adminURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || adminURL == "" {
		t.Skip("notifications boot test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
	}
	admin, err := pgxpool.New(t.Context(), adminURL)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	t.Cleanup(admin.Close)
	bin := qaBuildNotifications(t)

	// A dead proxy keeps a real-mode delivery off the network if the transport honours it.
	deadProxy := map[string]string{"HTTPS_PROXY": "http://127.0.0.1:1", "HTTP_PROXY": "http://127.0.0.1:1", "NO_PROXY": ""}
	for _, tc := range []struct {
		mode   string
		vars   map[string]string
		worked bool
	}{
		{"off", nil, false},
		{"fake", map[string]string{"CONTACTS_FAKE": "true"}, true},
		{"real", func() map[string]string {
			m := qaFourKeys()
			for k, v := range deadProxy {
				m[k] = v
			}
			return m
		}(), true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			email := fmt.Sprintf("main-%s-%s@worker.example", tc.mode, uuid.NewString()[:8])
			t.Cleanup(func() {
				ctx := context.Background()
				_, _ = admin.Exec(ctx, `DELETE FROM river_job WHERE kind IN ('contact_deliver', 'demo_deal') AND args->>'email' = $1`, email)
				_, _ = admin.Exec(ctx, `DELETE FROM contacts WHERE email = $1`, email)
			})
			vars := map[string]string{"DATABASE_URL": appURL}
			for k, v := range tc.vars {
				vars[k] = v
			}
			p := qaStartWithWorker(t, bin, vars)
			p.waitFor(t, "/readyz")
			if got := p.healthz(t)["contacts"]; got != tc.mode {
				t.Fatalf("/healthz contacts = %q, want %q", got, tc.mode)
			}

			body := fmt.Sprintf(`{"user_id":%q,"email":%q,"display_name":"Ada Lovelace"}`, uuid.NewString(), email)
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, p.url("/internal/contacts/registrants"), strings.NewReader(body))
			req.Header.Set("X-Gateway-Token", qaGatewayToken)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST registrants: %v", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("POST registrants status %d, want 202 in every mode\n%s", resp.StatusCode, p.out)
			}

			job := func() (state string, attempt int) {
				err := admin.QueryRow(t.Context(), `
					SELECT state::text, attempt FROM river_job
					WHERE kind = 'contact_deliver' AND args->>'destination' = 'hubspot' AND args->>'email' = $1`, email).Scan(&state, &attempt)
				if err != nil {
					t.Fatalf("read the hubspot job: %v", err)
				}
				return state, attempt
			}
			job() // the intake queued it, whatever the mode

			if !tc.worked {
				// A negative has no event to wait for; a few River fetch cycles stand in for one.
				for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
					if state, attempt := job(); state != "available" || attempt != 0 {
						t.Fatalf("job state %q attempt %d in off mode, want it untouched in the queue", state, attempt)
					}
				}
				return
			}
			deadline := time.Now().Add(30 * time.Second)
			for {
				if _, attempt := job(); attempt >= 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("no worker picked the job up in %s mode\n%s", tc.mode, p.out)
				}
				time.Sleep(50 * time.Millisecond)
			}
			if tc.mode != "fake" {
				return
			}
			for {
				var at *time.Time
				var mode *string
				if err := admin.QueryRow(t.Context(), `SELECT hubspot_delivered_at, delivery_mode FROM contacts WHERE email = $1`, email).Scan(&at, &mode); err != nil {
					t.Fatalf("read contact: %v", err)
				}
				if at != nil {
					if mode == nil || *mode != "fake" {
						t.Fatalf("delivery_mode = %v, want fake", mode)
					}
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("the fake delivery was never recorded\n%s", p.out)
				}
				time.Sleep(50 * time.Millisecond)
			}
		})
	}
}

// qaProxy is an HTTPS proxy that records each CONNECT target and refuses it, so a vendor
// request that honours the proxy is seen and never reaches the vendor.
type qaProxy struct {
	url      string
	mu       sync.Mutex
	targets  []string
	connects chan string
}

func qaStartProxy(t *testing.T) *qaProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	p := &qaProxy{url: "http://" + ln.Addr().String(), connects: make(chan string, 64)}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
				line, _ := bufio.NewReader(c).ReadString('\n')
				f := strings.Fields(line)
				if len(f) < 2 || f[0] != http.MethodConnect {
					return
				}
				p.mu.Lock()
				p.targets = append(p.targets, f[1])
				p.mu.Unlock()
				select {
				case p.connects <- f[1]:
				default:
				}
				_, _ = c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
			}()
		}
	}()
	return p
}

// qaVendorHosts are the contact vendors. The worker's code-list sync also dials NRS through the
// same proxy; that call is not a contact delivery.
var qaVendorHosts = map[string]bool{"api.hubapi.com:443": true, "api.resend.com:443": true}

// vendors returns the vendor CONNECT targets seen so far.
func (p *qaProxy) vendors() []string {
	var out []string
	for _, target := range p.seen() {
		if qaVendorHosts[target] {
			out = append(out, target)
		}
	}
	return out
}

func (p *qaProxy) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.targets...)
}

func (p *qaProxy) vars() map[string]string {
	return map[string]string{"HTTPS_PROXY": p.url, "HTTP_PROXY": p.url, "NO_PROXY": ""}
}

func qaMerge(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func qaAdminPool(t *testing.T) (admin *pgxpool.Pool, appURL string) {
	t.Helper()
	appURL, adminURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || adminURL == "" {
		t.Skip("notifications boot test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
	}
	admin, err := pgxpool.New(t.Context(), adminURL)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	t.Cleanup(admin.Close)
	return admin, appURL
}

func qaCleanupContact(t *testing.T, admin *pgxpool.Pool, email string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = admin.Exec(ctx, `DELETE FROM river_job WHERE kind IN ('contact_deliver', 'demo_deal') AND args->>'email' = $1`, email)
		_, _ = admin.Exec(ctx, `DELETE FROM contacts WHERE email = $1`, email)
	})
}

// qaCall sends one request to the booted service and returns the status and body.
func (p *qaProc) call(t *testing.T, method, path, body string, headers map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, p.url(path), strings.NewReader(body))
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
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

var qaViaGateway = map[string]string{"X-Gateway-Token": qaGatewayToken}

// qaWaitDelivered polls until the contact has every named delivery column set, and returns its mode.
func qaWaitDelivered(t *testing.T, admin *pgxpool.Pool, p *qaProc, email string, resend bool) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var hs, rs *time.Time
		var mode *string
		if err := admin.QueryRow(t.Context(), `SELECT hubspot_delivered_at, resend_delivered_at, delivery_mode FROM contacts WHERE email = $1`, email).Scan(&hs, &rs, &mode); err != nil {
			t.Fatalf("read contact: %v", err)
		}
		if hs != nil && (rs != nil || !resend) {
			if mode == nil {
				t.Fatal("delivered with no delivery_mode")
			}
			return *mode
		}
		if time.Now().After(deadline) {
			t.Fatalf("the delivery was never recorded (hubspot %v, resend %v)\n%s", hs, rs, p.out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A fork inherits production's keys and the environment name pr-<N>: the process itself must
// come up fake, deliver without a network call, and never refuse to boot.
func TestNotificationsMain_PreviewEnvironmentBootsFake(t *testing.T) {
	admin, appURL := qaAdminPool(t)
	bin := qaBuildNotifications(t)

	for _, tc := range []struct {
		name string
		vars map[string]string
	}{
		{"four keys", qaFourKeys()},
		{"flag and four keys", qaFourKeys("CONTACTS_FAKE", "true")},
		{"bad flag and a partial key set", map[string]string{"CONTACTS_FAKE": "banana", "HUBSPOT_TOKEN": "hs-secret-token"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := qaStartProxy(t)
			email := fmt.Sprintf("preview-%s@worker.example", uuid.NewString()[:8])
			qaCleanupContact(t, admin, email)
			p := qaStartWithWorker(t, bin, qaMerge(tc.vars, proxy.vars(), map[string]string{"DATABASE_URL": appURL, "DEPLOY_ENV": "pr-7"}))
			p.waitFor(t, "/readyz")
			if got := p.healthz(t)["contacts"]; got != "fake" {
				t.Fatalf("/healthz contacts = %q, want fake in a pr-7 environment", got)
			}

			body := fmt.Sprintf(`{"user_id":%q,"email":%q,"display_name":"Ada Lovelace","marketing_consent":{"text":"I agree","at":"2026-10-04T10:30:00Z"}}`, uuid.NewString(), email)
			if code, out := p.call(t, http.MethodPost, "/internal/contacts/registrants", body, qaViaGateway); code != http.StatusAccepted {
				t.Fatalf("POST registrants status %d (%s), want 202\n%s", code, out, p.out)
			}
			if mode := qaWaitDelivered(t, admin, p, email, true); mode != "fake" {
				t.Errorf("delivery_mode = %q, want fake", mode)
			}
			for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
				var s string
				err := admin.QueryRow(t.Context(), `SELECT state::text FROM river_job WHERE kind = 'demo_deal' AND args->>'email' = $1`, email).Scan(&s)
				if err == nil && s == "completed" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the sign-up demo_deal job did not complete (state %q, err %v)\n%s", s, err, p.out)
				}
			}
			if got := proxy.vendors(); len(got) != 0 {
				t.Errorf("the service dialled %v through the proxy, want no network call in a preview environment", got)
			}
			if strings.Contains(p.out.String(), "hs-secret-token") || strings.Contains(p.out.String(), "rs-secret-key") {
				t.Errorf("output carries a vendor key\n%s", p.out)
			}
		})
	}
}

// Real mode dials the vendors through HTTPS_PROXY: the transport is the default one, not a bare one.
func TestNotificationsMain_RealModeHonoursTheProxy(t *testing.T) {
	admin, appURL := qaAdminPool(t)
	bin := qaBuildNotifications(t)
	proxy := qaStartProxy(t)
	email := fmt.Sprintf("proxy-%s@worker.example", uuid.NewString()[:8])
	qaCleanupContact(t, admin, email)

	p := qaStartWithWorker(t, bin, qaMerge(qaFourKeys(), proxy.vars(), map[string]string{"DATABASE_URL": appURL}))
	p.waitFor(t, "/readyz")
	if got := p.healthz(t)["contacts"]; got != "real" {
		t.Fatalf("/healthz contacts = %q, want real", got)
	}
	body := fmt.Sprintf(`{"user_id":%q,"email":%q,"display_name":"Ada Lovelace"}`, uuid.NewString(), email)
	if code, out := p.call(t, http.MethodPost, "/internal/contacts/registrants", body, qaViaGateway); code != http.StatusAccepted {
		t.Fatalf("POST registrants status %d (%s), want 202\n%s", code, out, p.out)
	}
	for deadline := time.After(30 * time.Second); ; {
		select {
		case target := <-proxy.connects:
			if qaVendorHosts[target] {
				return
			}
		case <-deadline:
			t.Fatalf("no vendor request reached the proxy in real mode (saw %v)\n%s", proxy.seen(), p.out)
		}
	}
}

// Every contact route is served by the binary behind the gateway token, in any mode.
func TestNotificationsMain_ServesTheContactRoutes(t *testing.T) {
	admin, appURL := qaAdminPool(t)
	bin := qaBuildNotifications(t)
	p := qaStart(t, bin, map[string]string{"DATABASE_URL": appURL})
	p.waitFor(t, "/readyz")
	email := fmt.Sprintf("routes-%s@worker.example", uuid.NewString()[:8])
	qaCleanupContact(t, admin, email)

	demo := fmt.Sprintf(`{"email":%q,"name":"Grace Hopper","company":"Navy"}`, strings.ToUpper(email))
	registrant := fmt.Sprintf(`{"user_id":%q,"email":%q,"display_name":"Grace Hopper"}`, uuid.NewString(), email)
	for path, body := range map[string]string{"/internal/contacts/demo-requests": demo, "/internal/contacts/registrants": registrant} {
		if code, out := p.call(t, http.MethodPost, path, body, nil); code != http.StatusUnauthorized {
			t.Errorf("POST %s without the gateway token: status %d (%s), want 401", path, code, out)
		}
		if code, out := p.call(t, http.MethodPost, path, body, qaMerge(qaViaGateway, map[string]string{"X-User-ID": uuid.NewString()})); code != http.StatusNotFound {
			t.Errorf("POST %s with X-User-ID: status %d (%s), want 404", path, code, out)
		}
	}
	var n int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM contacts WHERE email = $1`, email).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows after the refused calls = %d (%v), want 0", n, err)
	}

	for path, body := range map[string]string{"/internal/contacts/demo-requests": demo, "/internal/contacts/registrants": registrant} {
		if code, out := p.call(t, http.MethodPost, path, body, qaViaGateway); code != http.StatusAccepted {
			t.Errorf("POST %s: status %d (%s), want 202\n%s", path, code, out, p.out)
		}
	}

	if code, _ := p.call(t, http.MethodGet, "/v1/contacts/me", "", map[string]string{"X-User-Email": email}); code != http.StatusUnauthorized {
		t.Errorf("GET me without the gateway token: status %d, want 401", code)
	}
	if code, out := p.call(t, http.MethodGet, "/v1/contacts/me", "", qaViaGateway); code != http.StatusNotFound {
		t.Errorf("GET me without X-User-Email: status %d (%s), want 404", code, out)
	}
	code, out := p.call(t, http.MethodGet, "/v1/contacts/me", "", qaMerge(qaViaGateway, map[string]string{"X-User-Email": email}))
	if code != http.StatusOK {
		t.Fatalf("GET me: status %d (%s), want 200", code, out)
	}
	var me struct {
		Email string   `json:"email"`
		Tags  []string `json:"tags"`
		Mode  *string  `json:"mode"`
	}
	if err := json.Unmarshal([]byte(out), &me); err != nil {
		t.Fatalf("GET me body %q: %v", out, err)
	}
	if me.Email != email || len(me.Tags) != 2 || me.Mode != nil {
		t.Errorf("GET me = %+v, want %s with registered + demo request tags and a null mode in off mode", me, email)
	}
}

// /readyz is the database's verdict: an unreachable database is a 503 that names it, while /healthz stays up.
func TestNotificationsMain_ReadinessReportsTheDatabase(t *testing.T) {
	bin := qaBuildNotifications(t)
	p := qaStart(t, bin, map[string]string{"DATABASE_URL": qaUnreachableDB})
	p.waitFor(t, "/healthz")
	code, out := p.call(t, http.MethodGet, "/readyz", "", nil)
	if code != http.StatusServiceUnavailable || !strings.Contains(out, "database") {
		t.Errorf("GET /readyz = %d %s, want 503 naming database", code, out)
	}
}

// A service that cannot reach a database it needs must not boot: the fork's deploy gate sees a crash, not a half service.
func TestNotificationsMain_RefusesToBootWithoutADatabase(t *testing.T) {
	bin := qaBuildNotifications(t)
	ctx, cancel := context.WithTimeout(t.Context(), qaBootWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = qaBootEnv(qaFreePort(t), nil)
	var out qaLockedBuf
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("notifications ended with %v, want exit status 1\n%s", err, &out)
	}
	if !strings.Contains(out.String(), "DATABASE_URL") {
		t.Errorf("output does not name DATABASE_URL\n%s", &out)
	}
}

// A demo request or a first sign-up queues a deal job: fake mode completes it with no network call, off mode leaves it queued.
func TestNotificationsMain_FakeModeCompletesADemoDealJob(t *testing.T) {
	admin, appURL := qaAdminPool(t)
	bin := qaBuildNotifications(t)

	for _, tc := range []struct {
		mode string
		vars map[string]string
	}{
		{"fake", map[string]string{"CONTACTS_FAKE": "true"}},
		{"off", nil},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			demoEmail := fmt.Sprintf("deal-%s-%s@worker.example", tc.mode, uuid.NewString()[:8])
			signupEmail := fmt.Sprintf("dealsu-%s-%s@worker.example", tc.mode, uuid.NewString()[:8])
			qaCleanupContact(t, admin, demoEmail)
			qaCleanupContact(t, admin, signupEmail)
			p := qaStartWithWorker(t, bin, qaMerge(tc.vars, map[string]string{"DATABASE_URL": appURL}))
			p.waitFor(t, "/readyz")

			body := fmt.Sprintf(`{"email":%q,"name":"Grace Hopper","company":"Navy"}`, demoEmail)
			if code, out := p.call(t, http.MethodPost, "/internal/contacts/demo-requests", body, qaViaGateway); code != http.StatusAccepted {
				t.Fatalf("POST demo-requests status %d (%s), want 202\n%s", code, out, p.out)
			}
			body = fmt.Sprintf(`{"user_id":%q,"email":%q,"display_name":"Ada Lovelace"}`, uuid.NewString(), signupEmail)
			if code, out := p.call(t, http.MethodPost, "/internal/contacts/registrants", body, qaViaGateway); code != http.StatusAccepted {
				t.Fatalf("POST registrants status %d (%s), want 202\n%s", code, out, p.out)
			}
			state := func(email string) string {
				var s string
				if err := admin.QueryRow(t.Context(), `SELECT state::text FROM river_job WHERE kind = 'demo_deal' AND args->>'email' = $1`, email).Scan(&s); err != nil {
					t.Fatalf("read the demo_deal job: %v", err)
				}
				return s
			}
			for _, leg := range []struct{ name, email string }{{"demo", demoEmail}, {"signup", signupEmail}} {
				if tc.mode == "off" {
					for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
						if s := state(leg.email); s != "available" {
							t.Fatalf("%s demo_deal state %q in off mode, want available", leg.name, s)
						}
					}
					continue
				}
				for deadline := time.Now().Add(30 * time.Second); state(leg.email) != "completed"; time.Sleep(50 * time.Millisecond) {
					if time.Now().After(deadline) {
						t.Fatalf("the %s demo_deal job never completed in fake mode\n%s", leg.name, p.out)
					}
				}
			}
		})
	}
}

// The staff blog routes decide who may write posts. AST over the registration calls, so a comment
// or gofmt cannot satisfy it; a mis-bound method (SetCover on the featured route) fails by pattern.
func TestNotificationsMain_RegistersTheBlogRoutes(t *testing.T) {
	want := map[string][2]string{
		"GET /v1/staff/blog/categories":            {"ListCategoriesHandler", "ListCategories"},
		"POST /v1/staff/blog/categories":           {"AddCategoryHandler", "AddCategory"},
		"PUT /v1/staff/blog/categories/{id}":       {"UpdateCategoryHandler", "UpdateCategory"},
		"POST /v1/staff/blog/categories/{id}/move": {"MoveCategoryHandler", "MoveCategory"},
		"DELETE /v1/staff/blog/categories/{id}":    {"DeleteCategoryHandler", "DeleteCategory"},
		"GET /v1/staff/blog/authors":               {"AuthorsHandler", "Authors"},
		"GET /v1/staff/blog/posts":                 {"ListPostsHandler", "ListPosts"},
		"POST /v1/staff/blog/posts":                {"CreatePostHandler", "CreatePost"},
		"PUT /v1/staff/blog/posts/{id}":            {"UpdatePostHandler", "UpdatePost"},
		"DELETE /v1/staff/blog/posts/{id}":         {"DeletePostHandler", "DeletePost"},
		"PUT /v1/staff/blog/posts/{id}/featured":   {"SetFeaturedHandler", "SetFeatured"},
		"PUT /v1/staff/blog/posts/{id}/cover":      {"SetCoverHandler", "SetCover"},
		"DELETE /v1/staff/blog/posts/{id}/cover":   {"RemoveCoverHandler", "RemoveCover"},
		"GET /v1/staff/blog/posts/{id}/cover":      {"GetCoverHandler", "GetCover"},
		"GET /internal/blog/categories":            {"PublicCategoriesHandler", "PublicCategories"},
		"GET /internal/blog/posts":                 {"PublicPostsHandler", "PublicPosts"},
		"GET /internal/blog/posts/{id}":            {"PublicPostHandler", "PublicPost"},
		"GET /internal/blog/covers/{id}/{version}": {"PublicCoverHandler", "PublicCover"},
	}
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/notifications/main.go: %v", err)
	}
	found := map[string]int{}
	var scanned int
	var control bool
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") || len(call.Args) < 2 {
			return true
		}
		// A blog handler outside the table sits outside the /v1/staff class: no staff check guards it.
		mentionsBlog := false
		ast.Inspect(call.Args[1], func(m ast.Node) bool {
			if id, ok := m.(*ast.Ident); ok && (id.Name == "blog" || id.Name == "blogStore") {
				mentionsBlog = true
			}
			return true
		})
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			if mentionsBlog {
				t.Errorf("a blog handler is registered under a non-literal pattern at %d", call.Pos())
			}
			return true
		}
		scanned++
		pattern := strings.Trim(lit.Value, "\"`")
		if pattern == "GET /v1/contacts/me" {
			control = true
		}
		exp, ok := want[pattern]
		if !ok {
			if strings.Contains(pattern, "/v1/staff/blog") || mentionsBlog {
				t.Errorf("%q is registered; it is not a route of the blog Route wiring table", pattern)
			}
			return true
		}
		found[pattern]++
		hc, ok := call.Args[1].(*ast.CallExpr)
		if !ok {
			t.Errorf("%s's second argument is %T, want a call expression", pattern, call.Args[1])
			return true
		}
		hsel, ok := hc.Fun.(*ast.SelectorExpr)
		if !ok {
			t.Errorf("%s is not served by blog.%s(...)", pattern, exp[0])
			return true
		}
		if pkg, ok := hsel.X.(*ast.Ident); !ok || pkg.Name != "blog" || hsel.Sel.Name != exp[0] {
			t.Errorf("%s is not served by blog.%s(...)", pattern, exp[0])
		}
		if len(hc.Args) == 0 {
			t.Errorf("%s: blog.%s has no store method argument", pattern, exp[0])
			return true
		}
		msel, ok := hc.Args[0].(*ast.SelectorExpr)
		if !ok {
			t.Errorf("%s: first handler argument is %T, want blogStore.%s", pattern, hc.Args[0], exp[1])
			return true
		}
		if recv, ok := msel.X.(*ast.Ident); !ok || recv.Name != "blogStore" || msel.Sel.Name != exp[1] {
			t.Errorf("%s is not bound to blogStore.%s", pattern, exp[1])
		}
		return true
	})
	if scanned == 0 || !control {
		t.Fatalf("control needle: scanned %d HandleFunc calls, GET /v1/contacts/me found=%v -- the AST walk is broken", scanned, control)
	}
	for pattern := range want {
		switch found[pattern] {
		case 1:
		case 0:
			t.Errorf("no app.Mux.HandleFunc(%q, ...) registration found in cmd/notifications/main.go", pattern)
		default:
			t.Errorf("%q is registered %d times", pattern, found[pattern])
		}
	}
}

// A PR fork has no blog keys and no sending key: both routes must work in capture and fake
// mode, the boot purge must run, and nothing may dial a vendor.
func TestNotificationsMain_PreviewServesTheBlogSubscriptionRoutes(t *testing.T) {
	admin, appURL := qaAdminPool(t)
	bin := qaBuildNotifications(t)
	ctx := t.Context()
	suffix := uuid.NewString()[:8]
	cat := "pv-" + suffix
	expired, live, fresh := "pv-exp-"+suffix+"@worker.example", "pv-live-"+suffix+"@worker.example", "pv-new-"+suffix+"@worker.example"
	member := "pv-member-" + suffix + "@worker.example"
	token := "preview-token-" + suffix
	hash := func(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = admin.Exec(bg, `DELETE FROM blog_subscription_requests WHERE email = ANY($1)`, []string{expired, live, fresh})
		_, _ = admin.Exec(bg, `DELETE FROM river_job WHERE kind = 'blog_subscriber_sync' AND args->>'email' = ANY($1)`, []string{live, member})
		_, _ = admin.Exec(bg, `DELETE FROM blog_subscribers WHERE email = ANY($1)`, []string{live, member})
		_, _ = admin.Exec(bg, `DELETE FROM blog_categories WHERE id = $1`, cat)
	})
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO blog_categories (id, name, headline, icon, tone, position) VALUES ($1, 'Preview', 'h', 'package', 0, $2)`, []any{cat, 2_000_000_000 + int(time.Now().UnixNano()%1_000_000)}},
		{`INSERT INTO blog_subscription_requests (email, category_ids, frequency, token_hash, expires_at) VALUES ($1, ARRAY[$2]::text[], 'weekly', $3, now() - interval '1 second')`, []any{expired, cat, hash("old-" + suffix)}},
		{`INSERT INTO blog_subscription_requests (email, category_ids, frequency, token_hash, expires_at) VALUES ($1, ARRAY[$2]::text[], 'weekly', $3, now() + interval '1 hour')`, []any{live, cat, hash(token)}},
		{`INSERT INTO blog_subscribers (email, category_ids, frequency, confirmed_at) VALUES ($1, ARRAY[$2]::text[], 'weekly', now())`, []any{member, cat}},
	} {
		if _, err := admin.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	proxy := qaStartProxy(t)
	p := qaStartWithWorker(t, bin, qaMerge(proxy.vars(), map[string]string{"DATABASE_URL": appURL, "DEPLOY_ENV": "pr-7"}))
	p.waitFor(t, "/readyz")

	count := func(email string) (n int) {
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM blog_subscription_requests WHERE email = $1`, email).Scan(&n); err != nil {
			t.Fatalf("count request: %v", err)
		}
		return n
	}
	for deadline := time.Now().Add(15 * time.Second); count(expired) != 0; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the boot purge never deleted the expired request\n%s", p.out)
		}
	}
	if count(live) != 1 {
		t.Error("the purge deleted a live request")
	}
	var members int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM blog_subscribers WHERE email = $1`, member).Scan(&members); err != nil || members != 1 {
		t.Errorf("the purge touched blog_subscribers: count=%d err=%v", members, err)
	}

	body := fmt.Sprintf(`{"email":%q,"category_ids":[%q],"frequency":"monthly"}`, fresh, cat)
	if code, out := p.call(t, http.MethodPost, "/internal/blog/subscriptions", body, qaViaGateway); code != http.StatusAccepted {
		t.Fatalf("subscribe in a fork = %d (%s), want 202\n%s", code, out, p.out)
	}
	if count(fresh) != 1 {
		t.Error("subscribe stored no request in a fork")
	}
	if code, out := p.call(t, http.MethodPost, "/internal/blog/subscriptions/confirm", fmt.Sprintf(`{"token":%q}`, token), qaViaGateway); code != http.StatusOK ||
		!strings.Contains(out, `"unsubscribed_everywhere":false`) {
		t.Fatalf("confirm in a fork = %d (%s), want 200 false\n%s", code, out, p.out)
	}
	var mode *string
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		var at *time.Time
		if err := admin.QueryRow(ctx, `SELECT resend_delivered_at, delivery_mode FROM blog_subscribers WHERE email = $1`, live).Scan(&at, &mode); err != nil {
			t.Fatalf("read subscriber: %v", err)
		}
		if at != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the blog sync job never completed in a fork\n%s", p.out)
		}
	}
	if mode == nil || *mode != "fake" {
		t.Errorf("delivery_mode = %v, want fake", mode)
	}
	if got := proxy.vendors(); len(got) != 0 {
		t.Errorf("the fork dialled %v, want no vendor call", got)
	}
	if out := p.out.String(); strings.Contains(out, live) || strings.Contains(out, fresh) || strings.Contains(out, token) {
		t.Errorf("service output carries an email or token\n%s", out)
	}
}

func TestNotificationsMain_PurgesExpiredRequestsWhileNewsletterIsOff(t *testing.T) {
	admin, appURL := qaAdminPool(t)
	bin := qaBuildNotifications(t)
	ctx := t.Context()
	suffix := uuid.NewString()[:8]
	cat := "off-" + suffix
	expired := "off-exp-" + suffix + "@worker.example"
	h := sha256.Sum256([]byte("off-" + suffix))
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = admin.Exec(bg, `DELETE FROM blog_subscription_requests WHERE email = $1`, expired)
		_, _ = admin.Exec(bg, `DELETE FROM blog_categories WHERE id = $1`, cat)
	})
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO blog_categories (id, name, headline, icon, tone, position) VALUES ($1, 'Off', 'h', 'package', 0, $2)`, []any{cat, 2_000_000_000 + int(time.Now().UnixNano()%1_000_000)}},
		{`INSERT INTO blog_subscription_requests (email, category_ids, frequency, token_hash, expires_at) VALUES ($1, ARRAY[$2]::text[], 'weekly', $3, now() - interval '1 second')`, []any{expired, cat, h[:]}},
	} {
		if _, err := admin.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	p := qaStartWithWorker(t, bin, map[string]string{"DATABASE_URL": appURL})
	p.waitFor(t, "/readyz")
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM blog_subscription_requests WHERE email = $1`, expired).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("newsletter off: the expired request was never purged\n%s", p.out)
		}
	}
}
