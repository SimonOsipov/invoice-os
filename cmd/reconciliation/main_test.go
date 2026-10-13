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

func buildReconciliation(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "reconciliation")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build cmd/reconciliation: %v\n%s", err, out)
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

func TestReconciliationMain_RequiresTheValidationConfig(t *testing.T) {
	bin := buildReconciliation(t)
	base := map[string]string{"DATABASE_URL": unreachableDB, "DATABASE_READER_URL": unreachableDB}

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

	// Control: with both set the validation refusal is gone (the unreachable DB stops the boot instead).
	vars := map[string]string{"VALIDATION_URL": "http://validation.invalid", "S2S_TOKEN": "tok"}
	for k, v := range base {
		vars[k] = v
	}
	out, _ := run(t, bin, vars)
	if strings.Contains(out, "is required") {
		t.Errorf("a refusal remains with both variables set\n%s", out)
	}
}

// A booted binary ticks the custom-rule re-check: nothing else marks a pending revision, and
// a pending one refuses queueing until it is marked.
func TestReconciliationMain_TicksTheCustomRuleRecheck(t *testing.T) {
	appURL, adminURL, readerURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_SUPERUSER_URL"), os.Getenv("DATABASE_READER_URL")
	if appURL == "" || adminURL == "" || readerURL == "" {
		t.Skip("reconciliation boot test skipped: set DATABASE_URL, DATABASE_READER_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
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
	cmd := exec.Command(buildReconciliation(t))
	cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "PORT=" + strconv.Itoa(freePort(t)),
		"DATABASE_URL=" + appURL, "DATABASE_READER_URL=" + readerURL,
		"VALIDATION_URL=" + down.URL, "S2S_TOKEN=tok", "RECONCILE_INTERVAL=200ms"}
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start reconciliation: %v", err)
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
