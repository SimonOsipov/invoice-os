package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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
