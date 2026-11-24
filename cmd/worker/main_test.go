package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth/gotrueadmin"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth/iaptest"
)

const unreachableDB = "postgres://invoice_app:app@127.0.0.1:1/invoice_os?sslmode=disable"

const bootWait = 30 * time.Second

// workerBinDir is set before the build so TestMain removes it even when the build fails.
var workerBinDir string

var workerBin = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "worker-")
	if err != nil {
		return "", err
	}
	workerBinDir = dir
	bin := filepath.Join(dir, "worker")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build cmd/worker: %w\n%s", err, out)
	}
	return bin, nil
})

func TestMain(m *testing.M) {
	code := m.Run()
	_ = os.RemoveAll(workerBinDir)
	os.Exit(code)
}

func buildWorker(t *testing.T) string {
	t.Helper()
	bin, err := workerBin()
	if err != nil {
		t.Fatal(err)
	}
	return bin
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// bootVars are the variables every worker boot needs besides the databases; none reaches a network.
var bootVars = map[string]string{
	"GATEWAY_URL": "http://gateway.invalid", "GATEWAY_TOKEN": "gw-token", "APP_ADAPTER": "mock",
	"DOCUMENT_BUCKET": "b", "DOCUMENT_ENDPOINT": "http://127.0.0.1:1", "DOCUMENT_REGION": "r",
	"DOCUMENT_ACCESS_KEY_ID": "k", "DOCUMENT_SECRET_ACCESS_KEY": "s",
}

func bootEnv(vars map[string]string) []string {
	env := []string{"HOME=" + os.Getenv("HOME")}
	for k, v := range bootVars {
		env = append(env, k+"="+v)
	}
	for k, v := range vars {
		env = append(env, k+"="+v)
	}
	return env
}

// run boots the binary with exactly vars as its environment and returns its combined output.
func run(t *testing.T, bin string, vars map[string]string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), bootWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "PORT=" + strconv.Itoa(freePort(t))}
	for k, v := range vars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// exitedOne is true only for exit status 1; a kill or timeout (ExitCode -1) is not.
func exitedOne(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 1
}

func TestWorkerMain_RequiresTheValidationConfig(t *testing.T) {
	bin := buildWorker(t)
	base := map[string]string{"DATABASE_URL": unreachableDB, "DATABASE_READER_URL": unreachableDB, "GATEWAY_URL": "http://gateway.invalid"}

	for _, tc := range []struct{ name, set, missing string }{
		{"VALIDATION_URL unset", "S2S_TOKEN", "VALIDATION_URL"},
		{"S2S_TOKEN unset", "VALIDATION_URL", "S2S_TOKEN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars := map[string]string{tc.set: "http://validation.invalid"}
			for k, v := range base {
				vars[k] = v
			}
			out, err := run(t, bin, vars)
			if !exitedOne(err) {
				t.Fatalf("ended with %v, want exit status 1 through platform.Fatal\n%s", err, out)
			}
			if !strings.Contains(out, tc.missing+" is required") {
				t.Errorf("output does not say %s is required\n%s", tc.missing, out)
			}
		})
	}

	// Control: with both set the boot reaches the next refusal, DOCUMENT_BUCKET.
	vars := map[string]string{"VALIDATION_URL": "http://validation.invalid", "S2S_TOKEN": "tok"}
	for k, v := range base {
		vars[k] = v
	}
	out, err := run(t, bin, vars)
	if !exitedOne(err) {
		t.Fatalf("control ended with %v, want exit status 1\n%s", err, out)
	}
	if !strings.Contains(out, "DOCUMENT_BUCKET is required") {
		t.Fatalf("control did not reach the next refusal, DOCUMENT_BUCKET\n%s", out)
	}
	if strings.Contains(out, "VALIDATION_URL is required") || strings.Contains(out, "S2S_TOKEN is required") {
		t.Errorf("a validation refusal remains with both variables set\n%s", out)
	}
}

// A booted binary ticks the custom-rule re-check: nothing else marks a pending revision, and
// a pending one refuses queueing until it is marked.
func TestWorkerMain_TicksTheCustomRuleRecheck(t *testing.T) {
	appURL, adminURL, readerURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_SUPERUSER_URL"), os.Getenv("DATABASE_READER_URL")
	if appURL == "" || adminURL == "" || readerURL == "" {
		t.Skip("worker boot test skipped: set DATABASE_URL, DATABASE_READER_URL and DATABASE_SUPERUSER_URL (or run `make test-db PKGS=./cmd/worker/...`)")
	}
	admin, err := pgxpool.New(t.Context(), adminURL)
	if err != nil {
		t.Fatalf("superuser pool: %v", err)
	}
	t.Cleanup(admin.Close)
	tenant := uuid.NewString()
	if _, err := admin.Exec(t.Context(), `INSERT INTO tenants (id, name, kind) VALUES ($1, 'ENGI-14-06 wiring', 'firm')`, tenant); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant) })
	var revision string
	if err := admin.QueryRow(t.Context(),
		`INSERT INTO custom_rule_revisions (tenant_id, rule_id, revision, type, target, severity, message, enabled, created_by)
		 VALUES ($1, $2, 1, 'required', 'buyer.email', 'error', 'email needed', true, 'qa') RETURNING id`,
		tenant, uuid.NewString()).Scan(&revision); err != nil {
		t.Fatalf("seed revision: %v", err)
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	t.Cleanup(down.Close)
	var out bytes.Buffer
	cmd := exec.Command(buildWorker(t))
	cmd.Env = bootEnv(map[string]string{"PORT": strconv.Itoa(freePort(t)), "DATABASE_URL": appURL, "DATABASE_READER_URL": readerURL, "VALIDATION_URL": down.URL, "S2S_TOKEN": "tok", "RECONCILE_INTERVAL": "200ms"})
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	})

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var marked bool
		if err := admin.QueryRow(t.Context(), `SELECT rechecked_at IS NOT NULL FROM custom_rule_revisions WHERE id = $1`, revision).Scan(&marked); err != nil {
			t.Fatalf("read revision: %v", err)
		}
		if marked {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("the pending revision was not marked within 20s of boot\n%s", out.String())
}

// A booted binary writes the day's rollup snapshot. The run enumerates the whole dev DB, so rows for other tenants are expected.
func TestWorkerMain_SnapshotsAtBoot(t *testing.T) {
	appURL, adminURL, readerURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_SUPERUSER_URL"), os.Getenv("DATABASE_READER_URL")
	if appURL == "" || adminURL == "" || readerURL == "" {
		t.Skip("worker boot test skipped: set DATABASE_URL, DATABASE_READER_URL and DATABASE_SUPERUSER_URL (or run `make test-db PKGS=./cmd/worker/...`)")
	}
	admin, err := pgxpool.New(t.Context(), adminURL)
	if err != nil {
		t.Fatalf("superuser pool: %v", err)
	}
	t.Cleanup(admin.Close)
	tenant := uuid.NewString()
	if _, err := admin.Exec(t.Context(), `INSERT INTO tenants (id, name, kind) VALUES ($1, 'OVER-03-02 snapshot wiring', 'firm')`, tenant); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant) })
	var entity string
	if err := admin.QueryRow(t.Context(),
		`INSERT INTO business_entities (tenant_id, name) VALUES ($1, 'Snapshot wiring client') RETURNING id`, tenant).Scan(&entity); err != nil {
		t.Fatalf("seed entity: %v", err)
	}
	if _, err := admin.Exec(t.Context(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, 'OVER-03-02-1')`, tenant, entity); err != nil {
		t.Fatalf("seed invoice: %v", err)
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	t.Cleanup(down.Close)
	var out bytes.Buffer
	cmd := exec.Command(buildWorker(t))
	cmd.Env = bootEnv(map[string]string{"PORT": strconv.Itoa(freePort(t)), "DATABASE_URL": appURL, "DATABASE_READER_URL": readerURL, "VALIDATION_URL": down.URL, "S2S_TOKEN": "tok"})
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	})

	// WAT is UTC+1 all year; computed here so the test does not lean on dashboard.WATDay.
	wat := time.FixedZone("WAT", 3600)
	before := time.Now().In(wat).Format(time.DateOnly)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var days []string
		rows, err := admin.Query(t.Context(), `SELECT to_char(day, 'YYYY-MM-DD') FROM rollup_snapshots WHERE tenant_id = $1 AND entity_id = $2`, tenant, entity)
		if err != nil {
			t.Fatalf("read rollup_snapshots: %v", err)
		}
		for rows.Next() {
			var d string
			if err := rows.Scan(&d); err != nil {
				t.Fatalf("scan day: %v", err)
			}
			days = append(days, d)
		}
		rows.Close()
		after := time.Now().In(wat).Format(time.DateOnly)
		for _, d := range days {
			if d == before || d == after {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("no rollup_snapshots row for the entity and today's WAT day within 20s of boot\n%s", out.String())
}

// A hosted boot with ERP_FAKE set refuses, names the variable and never its value.
func TestWorkerMain_HostedBootWithERPFakeIsFatal(t *testing.T) {
	appURL, readerURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_READER_URL")
	if appURL == "" || readerURL == "" {
		t.Skip("worker boot test skipped: set DATABASE_URL and DATABASE_READER_URL")
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	t.Cleanup(down.Close)
	out, err := run(t, buildWorker(t), map[string]string{
		"DATABASE_URL": appURL, "DATABASE_READER_URL": readerURL, "VALIDATION_URL": down.URL, "S2S_TOKEN": "tok",
		"GATEWAY_URL": "http://gateway.invalid", "GATEWAY_TOKEN": "gw-token", "APP_ADAPTER": "mock",
		"DOCUMENT_BUCKET": "b", "DOCUMENT_ENDPOINT": "http://127.0.0.1:1", "DOCUMENT_REGION": "r",
		"DOCUMENT_ACCESS_KEY_ID": "k", "DOCUMENT_SECRET_ACCESS_KEY": "s",
		"DEPLOY_ENV": "production", "ERP_FAKE": "true",
	})
	if !exitedOne(err) {
		t.Fatalf("ended with %v, want exit status 1 through platform.Fatal\n%s", err, out)
	}
	if !strings.Contains(out, "ERP_FAKE") {
		t.Errorf("output does not name ERP_FAKE\n%s", out)
	}
	if strings.Contains(out, "ERP_FAKE=true") || strings.Contains(out, "ERP_FAKE is true") {
		t.Errorf("output quotes the value of ERP_FAKE\n%s", out)
	}
}

// A hosted boot with ERP_SANDBOX set refuses, names the variable and never its value.
func TestWorkerMain_HostedBootWithERPSandboxIsFatal(t *testing.T) {
	appURL, readerURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_READER_URL")
	if appURL == "" || readerURL == "" {
		t.Skip("worker boot test skipped: set DATABASE_URL and DATABASE_READER_URL")
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	t.Cleanup(down.Close)
	out, err := run(t, buildWorker(t), map[string]string{
		"DATABASE_URL": appURL, "DATABASE_READER_URL": readerURL, "VALIDATION_URL": down.URL, "S2S_TOKEN": "tok",
		"GATEWAY_URL": "http://gateway.invalid", "GATEWAY_TOKEN": "gw-token", "APP_ADAPTER": "mock",
		"DOCUMENT_BUCKET": "b", "DOCUMENT_ENDPOINT": "http://127.0.0.1:1", "DOCUMENT_REGION": "r",
		"DOCUMENT_ACCESS_KEY_ID": "k", "DOCUMENT_SECRET_ACCESS_KEY": "s",
		"DEPLOY_ENV": "production", "ERP_SANDBOX": "true",
	})
	if !exitedOne(err) {
		t.Fatalf("ended with %v, want exit status 1 through platform.Fatal\n%s", err, out)
	}
	if !strings.Contains(out, "ERP_SANDBOX") {
		t.Errorf("output does not name ERP_SANDBOX\n%s", out)
	}
	if strings.Contains(out, "ERP_SANDBOX=true") || strings.Contains(out, "ERP_SANDBOX is true") {
		t.Errorf("output quotes the value of ERP_SANDBOX\n%s", out)
	}
}

// A nightly boot without the Intuit variables refuses and names the first one.
func TestWorkerMain_NightlyWithoutIntuitIsFatal(t *testing.T) {
	appURL, readerURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_READER_URL")
	if appURL == "" || readerURL == "" {
		t.Skip("worker boot test skipped: set DATABASE_URL and DATABASE_READER_URL")
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	t.Cleanup(down.Close)
	out, err := run(t, buildWorker(t), map[string]string{
		"DATABASE_URL": appURL, "DATABASE_READER_URL": readerURL, "VALIDATION_URL": down.URL, "S2S_TOKEN": "tok",
		"GATEWAY_URL": "http://gateway.invalid", "GATEWAY_TOKEN": "gw-token", "APP_ADAPTER": "mock",
		"DOCUMENT_BUCKET": "b", "DOCUMENT_ENDPOINT": "http://127.0.0.1:1", "DOCUMENT_REGION": "r",
		"DOCUMENT_ACCESS_KEY_ID": "k", "DOCUMENT_SECRET_ACCESS_KEY": "s",
		"DEPLOY_ENV": "nightly", "ERP_REDIRECT_URL": "https://gw.example/erp/callback",
	})
	if !exitedOne(err) {
		t.Fatalf("ended with %v, want exit status 1 through platform.Fatal\n%s", err, out)
	}
	if !strings.Contains(out, "INTUIT_CLIENT_ID") {
		t.Errorf("output does not name INTUIT_CLIENT_ID\n%s", out)
	}
}

const (
	iapRoute = "/internal/console-sessions/recovery-token"
	iapOps   = "/projects/111111111111/global/backendServices/1111111111111111111"
	iapSup   = "/projects/111111111111/global/backendServices/2222222222222222222"
)

// iapLinker stands in for GoTrue and counts the addresses it was asked about.
type iapLinker struct{ emails []string }

func (l *iapLinker) GenerateRecoveryLink(_ context.Context, email string) (gotrueadmin.RecoveryLink, error) {
	l.emails = append(l.emails, email)
	return gotrueadmin.RecoveryLink{TokenHash: "HASH-WORKER", Confirmed: true}, nil
}

func envFrom(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestWorkerMain_IAPAudiencesBootRules(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "")
	t.Setenv("K_SERVICE", "")
	cases := []struct {
		name    string
		env     map[string]string
		wantErr bool
		wantOn  bool
	}{
		{"hosted on Cloud Run, unset", map[string]string{"DEPLOY_ENV": "production", "K_SERVICE": "worker"}, true, false},
		{"hosted on Cloud Run, blank", map[string]string{"DEPLOY_ENV": "production", "K_SERVICE": "worker", "IAP_AUDIENCES": "  "}, true, false},
		{"hosted on Cloud Run, bad shape", map[string]string{"DEPLOY_ENV": "production", "K_SERVICE": "worker", "IAP_AUDIENCES": "ops.ascomply.com"}, true, false},
		{"hosted on Cloud Run, one bad of two", map[string]string{"DEPLOY_ENV": "production", "K_SERVICE": "worker", "IAP_AUDIENCES": iapOps + ",/projects/x/global/backendServices/2"}, true, false},
		{"hosted on Cloud Run, repeated", map[string]string{"DEPLOY_ENV": "production", "K_SERVICE": "worker", "IAP_AUDIENCES": iapOps + "," + iapOps}, true, false},
		{"hosted on Cloud Run, set", map[string]string{"DEPLOY_ENV": "production", "K_SERVICE": "worker", "IAP_AUDIENCES": iapOps + "," + iapSup}, false, true},
		{"hosted off Cloud Run, unset", map[string]string{"DEPLOY_ENV": "production"}, false, false},
		{"preview, bad shape", map[string]string{"DEPLOY_ENV": "pr-12", "IAP_AUDIENCES": "ops.ascomply.com"}, true, false},
		{"preview, unset", map[string]string{"DEPLOY_ENV": "pr-12"}, false, false},
		{"nightly, unset", map[string]string{"DEPLOY_ENV": "nightly"}, false, false},
		{"local, unset", map[string]string{}, false, false},
		{"preview, set", map[string]string{"DEPLOY_ENV": "pr-12", "IAP_AUDIENCES": iapOps}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, jwks := iaptest.NewSigner(t)
			app, err := platform.New("worker")
			if err != nil {
				t.Fatal(err)
			}
			err = mountConsoleSession(app, envFrom(tc.env), &iapLinker{}, auth.IAPConfig{JWKSURL: jwks})
			if (err != nil) != tc.wantErr {
				t.Fatalf("mountConsoleSession error = %v, want error: %v", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "IAP_AUDIENCES") {
				t.Errorf("error %q does not name IAP_AUDIENCES", err)
			}
			_, pattern := app.Mux.Handler(httptest.NewRequest(http.MethodPost, iapRoute, nil))
			if on := pattern == "POST "+iapRoute; on != tc.wantOn {
				t.Errorf("route mounted = %v (pattern %q), want %v", on, pattern, tc.wantOn)
			}
		})
	}
}

func TestWorkerMain_IAPAudiencesWithoutAGoTrueAdminClientRefusesToBoot(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "")
	t.Setenv("K_SERVICE", "")
	_, jwks := iaptest.NewSigner(t)
	app, err := platform.New("worker")
	if err != nil {
		t.Fatal(err)
	}
	err = mountConsoleSession(app, envFrom(map[string]string{"IAP_AUDIENCES": iapOps}), nil, auth.IAPConfig{JWKSURL: jwks})
	if err == nil || !strings.Contains(err.Error(), "IAP_AUDIENCES") {
		t.Fatalf("mountConsoleSession with no admin client = %v, want an error naming IAP_AUDIENCES", err)
	}
	if _, pattern := app.Mux.Handler(httptest.NewRequest(http.MethodPost, iapRoute, nil)); pattern == "POST "+iapRoute {
		t.Error("route mounted without a GoTrue admin client")
	}
}

func TestWorkerMain_RecoveryRouteRefusesACallWithoutTheGatewayToken(t *testing.T) {
	t.Setenv("DEPLOY_ENV", "")
	t.Setenv("K_SERVICE", "")
	const gatewayToken = "gateway-token-for-this-test"
	signer, jwks := iaptest.NewSigner(t)
	now := time.Now()
	assertion := signer.Sign(map[string]any{
		"iss": auth.IAPIssuer, "aud": iapOps, "sub": "accounts.google.com:118273645546372819", "email": "sam@ascomply.test",
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	})
	// The guard call below restates main's; TestWorkerMain_PublicCheckRoutesSitBehindTheGatewayToken pins main's.
	boot := func(t *testing.T, env map[string]string) (http.Handler, *iapLinker) {
		t.Helper()
		linker := &iapLinker{}
		app, err := platform.New("worker")
		if err != nil {
			t.Fatal(err)
		}
		if err := mountConsoleSession(app, envFrom(env), linker, auth.IAPConfig{JWKSURL: jwks}); err != nil {
			t.Fatalf("mountConsoleSession: %v", err)
		}
		app.RequireGateway(gatewayToken)
		return app.Handler(), linker
	}
	call := func(h http.Handler, token string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"assertion": assertion})
		req := httptest.NewRequest(http.MethodPost, iapRoute, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set(platform.HeaderGatewayToken, token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	t.Run("audiences set", func(t *testing.T) {
		h, linker := boot(t, map[string]string{"IAP_AUDIENCES": iapOps})
		for name, token := range map[string]string{"no token": "", "wrong token": "not-the-token"} {
			rec := call(h, token)
			if rec.Code != http.StatusUnauthorized || rec.Header().Get(platform.HeaderGatewayGuard) != platform.GatewayGuardRefused {
				t.Errorf("%s: status %d, guard header %q (%s); want the platform guard's 401", name, rec.Code, rec.Header().Get(platform.HeaderGatewayGuard), rec.Body.String())
			}
		}
		if len(linker.emails) != 0 {
			t.Fatalf("linker called %v without the gateway token, want no call", linker.emails)
		}
		rec := call(h, gatewayToken)
		if rec.Code != http.StatusOK && rec.Code != http.StatusForbidden {
			t.Errorf("with the gateway token: status %d (%s), want 200 or 403 from the handler", rec.Code, rec.Body.String())
		}
		if len(linker.emails) != 1 || linker.emails[0] != "sam@ascomply.test" {
			t.Errorf("with the gateway token the linker saw %v, want the assertion's address once", linker.emails)
		}
	})

	t.Run("audiences unset", func(t *testing.T) {
		h, linker := boot(t, map[string]string{})
		if rec := call(h, gatewayToken); rec.Code != http.StatusNotFound || len(linker.emails) != 0 {
			t.Errorf("status %d, linker calls %v; want 404 and no call", rec.Code, linker.emails)
		}
	})
}
