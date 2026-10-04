package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	"github.com/SimonOsipov/invoice-os/internal/notifications"
)

type qaSpied struct {
	Method, Host, Path, Auth string
	Remaining                time.Duration // time left on the request context; 0 without a deadline
}

// qaSpyTransport records every request and answers 200 unless respond says otherwise.
type qaSpyTransport struct {
	mu      sync.Mutex
	reqs    []qaSpied
	respond func(n int, r *http.Request) *http.Response
}

func qaReply(r *http.Request, code int, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: code, Status: strconv.Itoa(code), Header: header, Body: io.NopCloser(strings.NewReader("{}")), Request: r}
}

func (s *qaSpyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var left time.Duration
	if d, ok := r.Context().Deadline(); ok {
		left = time.Until(d)
	}
	s.mu.Lock()
	s.reqs = append(s.reqs, qaSpied{r.Method, r.URL.Host, r.URL.Path, r.Header.Get("Authorization"), left})
	n := len(s.reqs)
	s.mu.Unlock()
	if s.respond != nil {
		return s.respond(n, r), nil
	}
	return qaReply(r, http.StatusOK, nil), nil
}

func (s *qaSpyTransport) seen() []qaSpied {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]qaSpied(nil), s.reqs...)
}

func qaGetenv(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

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

var qaAda = notifications.Contact{Email: "ada@corp.example", FirstName: "Ada", Tags: []string{"registered"}, MarketingEligible: true}

func TestDeliveryClients_PreviewNeverUsesTheTransport(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		vars      map[string]string
	}{
		{"pr-7 with four keys", "pr-7", qaFourKeys()},
		{"a named fork with four keys", "ascomply-pr-12", qaFourKeys()},
		{"pr-7 with the flag and four keys", "pr-7", qaFourKeys("CONTACTS_FAKE", "true")},
		{"pr-7 with a bad flag and a partial key set", "pr-7", map[string]string{"CONTACTS_FAKE": "banana", "HUBSPOT_TOKEN": "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &qaSpyTransport{}
			mode, hs, rs, err := deliveryClients(qaGetenv(tc.vars), tc.env, spy)
			if err != nil {
				t.Fatalf("deliveryClients err = %v, want nil: a preview environment never refuses to boot", err)
			}
			if mode != notifications.ModeFake {
				t.Fatalf("mode = %q, want fake", mode)
			}
			if hs == nil || rs == nil {
				t.Fatalf("clients = %v, %v, want a fake HubSpot and a fake Resend", hs, rs)
			}
			if err := hs.Upsert(t.Context(), qaAda); err != nil {
				t.Errorf("Upsert err = %v, want nil", err)
			}
			for _, optIn := range []bool{true, false} {
				if err := rs.Sync(t.Context(), qaAda, optIn); err != nil {
					t.Errorf("Sync(optIn=%v) err = %v, want nil", optIn, err)
				}
			}
			if got := spy.seen(); len(got) != 0 {
				t.Errorf("the transport saw %d request(s) %+v, want none", len(got), got)
			}
		})
	}
}

func TestDeliveryClients_FakeFlagUsesNoTransport(t *testing.T) {
	spy := &qaSpyTransport{}
	mode, hs, rs, err := deliveryClients(qaGetenv(map[string]string{"CONTACTS_FAKE": "true"}), "production", spy)
	if err != nil || mode != notifications.ModeFake || hs == nil || rs == nil {
		t.Fatalf("deliveryClients = %q, %v, %v, %v, want fake with both clients", mode, hs, rs, err)
	}
	_ = hs.Upsert(t.Context(), qaAda)
	_ = rs.Sync(t.Context(), qaAda, true)
	if got := spy.seen(); len(got) != 0 {
		t.Errorf("the transport saw %+v, want no request in fake mode", got)
	}
}

func TestDeliveryClients_RealUsesTheTransport(t *testing.T) {
	for _, env := range []string{"production", ""} {
		t.Run("env_"+env, func(t *testing.T) {
			spy := &qaSpyTransport{}
			mode, hs, rs, err := deliveryClients(qaGetenv(qaFourKeys()), env, spy)
			if err != nil || mode != notifications.ModeReal {
				t.Fatalf("deliveryClients = %q, err %v, want real", mode, err)
			}
			if hs == nil || rs == nil {
				t.Fatalf("clients = %v, %v, want both", hs, rs)
			}
			if err := hs.Upsert(t.Context(), qaAda); err != nil {
				t.Fatalf("Upsert err = %v", err)
			}
			got := spy.seen()
			if len(got) != 1 || got[0].Method != http.MethodPatch || got[0].Host != "api.hubapi.com" ||
				!strings.HasPrefix(got[0].Path, "/crm/v3/objects/contacts/") || got[0].Auth != "Bearer hs-secret-token" {
				t.Fatalf("Upsert reached the transport as %+v, want one authorised PATCH to api.hubapi.com", got)
			}
			if err := rs.Sync(t.Context(), qaAda, true); err != nil {
				t.Fatalf("Sync err = %v", err)
			}
			got = spy.seen()[1:]
			if len(got) == 0 {
				t.Fatal("Sync never reached the transport")
			}
			for _, r := range got {
				if r.Host != "api.resend.com" || r.Auth != "Bearer rs-secret-key" {
					t.Errorf("Sync request %+v, want api.resend.com with the Resend key", r)
				}
			}
		})
	}
}

func TestDeliveryClients_OffReturnsNoClients(t *testing.T) {
	for _, env := range []string{"production", ""} {
		spy := &qaSpyTransport{}
		mode, hs, rs, err := deliveryClients(qaGetenv(nil), env, spy)
		if err != nil || mode != notifications.ModeOff {
			t.Fatalf("env %q: deliveryClients = %q, err %v, want off", env, mode, err)
		}
		if hs != nil || rs != nil {
			t.Errorf("env %q: clients = %v, %v, want nil interfaces", env, hs, rs)
		}
	}
}

func TestDeliveryClients_ModeErrorIsReturned(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"partial keys":        {"HUBSPOT_TOKEN": "hs-secret-token"},
		"flag plus a key":     {"CONTACTS_FAKE": "true", "RESEND_API_KEY": "rs-secret-key"},
		"unreadable flag":     {"CONTACTS_FAKE": "banana"},
		"flag plus four keys": qaFourKeys("CONTACTS_FAKE", "true"),
	} {
		t.Run(name, func(t *testing.T) {
			spy := &qaSpyTransport{}
			mode, hs, rs, err := deliveryClients(qaGetenv(vars), "production", spy)
			if err == nil {
				t.Fatalf("deliveryClients = %q, %v, %v, want an error", mode, hs, rs)
			}
			if hs != nil || rs != nil {
				t.Errorf("clients = %v, %v alongside an error, want nil", hs, rs)
			}
			for _, secret := range []string{"hs-secret-token", "rs-secret-key"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error %q carries a secret value", err)
				}
			}
		})
	}
}

// The vendor constructors set noRedirect only on the client they build; a client injected
// here keeps Go's default, which follows a 301 on a PATCH as a GET to the Location.
func TestDeliveryClients_RealDoesNotFollowRedirects(t *testing.T) {
	redirect := func(n int, r *http.Request) *http.Response {
		if n == 1 {
			return qaReply(r, http.StatusMovedPermanently, http.Header{"Location": {"https://evil.example/steal"}})
		}
		return qaReply(r, http.StatusOK, nil)
	}
	for name, call := range map[string]func(hs notifications.HubSpotClient, rs notifications.ResendClient) error{
		"hubspot PATCH": func(hs notifications.HubSpotClient, _ notifications.ResendClient) error {
			return hs.Upsert(t.Context(), qaAda)
		},
		"resend GET": func(_ notifications.HubSpotClient, rs notifications.ResendClient) error {
			return rs.Sync(t.Context(), qaAda, false)
		},
	} {
		t.Run(name, func(t *testing.T) {
			spy := &qaSpyTransport{respond: redirect}
			mode, hs, rs, err := deliveryClients(qaGetenv(qaFourKeys()), "production", spy)
			if err != nil || mode != notifications.ModeReal || hs == nil || rs == nil {
				t.Fatalf("deliveryClients = %q, %v, %v, %v, want real with both clients", mode, hs, rs, err)
			}
			cerr := call(hs, rs)
			var de *notifications.DeliveryError
			if !errors.As(cerr, &de) || de.Status != http.StatusMovedPermanently {
				t.Errorf("err = %v, want a *DeliveryError with status 301: a redirect is not a delivery", cerr)
			}
			for _, r := range spy.seen() {
				if r.Host == "evil.example" {
					t.Errorf("the client followed the redirect to %+v", r)
				}
			}
			if n := len(spy.seen()); n != 1 {
				t.Errorf("the transport saw %d requests, want 1", n)
			}
		})
	}
}

func TestDeliveryClients_RealTimesOutAfterTenSeconds(t *testing.T) {
	spy := &qaSpyTransport{}
	_, hs, rs, err := deliveryClients(qaGetenv(qaFourKeys()), "production", spy)
	if err != nil || hs == nil || rs == nil {
		t.Fatalf("deliveryClients err = %v, clients %v %v", err, hs, rs)
	}
	_ = hs.Upsert(t.Context(), qaAda)
	_ = rs.Sync(t.Context(), qaAda, false)
	got := spy.seen()
	if len(got) < 2 {
		t.Fatalf("the transport saw %d requests, want at least one per vendor", len(got))
	}
	for _, r := range got {
		if r.Remaining <= 8*time.Second || r.Remaining > 10*time.Second {
			t.Errorf("%s %s%s carried %v of deadline, want a client timeout of 10s", r.Method, r.Host, r.Path, r.Remaining)
		}
	}
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
	go func() { p.done <- p.cmd.Wait() }()
	t.Cleanup(func() {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			_ = p.cmd.Process.Kill()
			<-p.done
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
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
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
				_, _ = admin.Exec(ctx, `DELETE FROM river_job WHERE kind = 'contact_deliver' AND args->>'email' = $1`, email)
				_, _ = admin.Exec(ctx, `DELETE FROM contacts WHERE email = $1`, email)
			})
			vars := map[string]string{"DATABASE_URL": appURL}
			for k, v := range tc.vars {
				vars[k] = v
			}
			p := qaStart(t, bin, vars)
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
