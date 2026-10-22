package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const unreachableDB = "postgres://invoice_app:app@127.0.0.1:1/invoice_os?sslmode=disable"

func buildWorker(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "worker")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build cmd/worker: %v\n%s", err, out)
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
	"GATEWAY_URL": "http://gateway.invalid", "APP_ADAPTER": "mock",
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
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
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
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("ended with %v, want exit status 1 through platform.Fatal\n%s", err, out)
			}
			if !strings.Contains(out, tc.missing+" is required") {
				t.Errorf("output does not say %s is required\n%s", tc.missing, out)
			}
		})
	}

	// Control: with both set the validation refusal is gone (a later required variable stops the boot instead).
	vars := map[string]string{"VALIDATION_URL": "http://validation.invalid", "S2S_TOKEN": "tok"}
	for k, v := range base {
		vars[k] = v
	}
	out, _ := run(t, bin, vars)
	if strings.Contains(out, "VALIDATION_URL is required") || strings.Contains(out, "S2S_TOKEN is required") {
		t.Errorf("a validation refusal remains with both variables set\n%s", out)
	}
}

// A booted binary ticks the custom-rule re-check: nothing else marks a pending revision, and
// a pending one refuses queueing until it is marked.
func TestWorkerMain_TicksTheCustomRuleRecheck(t *testing.T) {
	appURL, adminURL, readerURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_SUPERUSER_URL"), os.Getenv("DATABASE_READER_URL")
	if appURL == "" || adminURL == "" || readerURL == "" {
		t.Skip("worker boot test skipped: set DATABASE_URL, DATABASE_READER_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
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
		t.Skip("worker boot test skipped: set DATABASE_URL, DATABASE_READER_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
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
