package main

import (
	"bytes"
	"context"
	"errors"
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
)

const (
	unreachableDB = "postgres://invoice_app:app@127.0.0.1:1/invoice_os?sslmode=disable"
	gatewayToken  = "gw-token-for-the-test"
	bootWait      = 30 * time.Second
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func buildPortfolio(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "portfolio")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build cmd/portfolio: %v\n%s", err, out)
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

// bootEnv is the whole environment of the child: nothing leaks in from the test process.
func bootEnv(port int, vars map[string]string) []string {
	env := []string{"HOME=" + os.Getenv("HOME"), "PORT=" + strconv.Itoa(port), "GATEWAY_TOKEN=" + gatewayToken, "DATABASE_URL=" + unreachableDB}
	for k, v := range vars {
		env = append(env, k+"="+v)
	}
	return env
}

func TestPortfolioMain_HostedWithERPFakeRefusesToBoot(t *testing.T) {
	bin := buildPortfolio(t)
	ctx, cancel := context.WithTimeout(t.Context(), bootWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = bootEnv(freePort(t), map[string]string{"DEPLOY_ENV": "production", "ERP_FAKE": "true"})
	var out lockedBuf
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("portfolio ended with %v, want exit status 1 through platform.Fatal\n%s", err, &out)
	}
	if !strings.Contains(out.String(), "ERP_FAKE") {
		t.Errorf("output does not name ERP_FAKE\n%s", &out)
	}
	if strings.Contains(out.String(), "=true") {
		t.Errorf("output carries the flag value\n%s", &out)
	}
}

func TestPortfolioMain_PreviewWithoutRedirectRefusesToBoot(t *testing.T) {
	bin := buildPortfolio(t)
	ctx, cancel := context.WithTimeout(t.Context(), bootWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = bootEnv(freePort(t), map[string]string{"DEPLOY_ENV": "pr-1"})
	var out lockedBuf
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("portfolio ended with %v, want exit status 1 through platform.Fatal\n%s", err, &out)
	}
	if !strings.Contains(out.String(), "ERP_REDIRECT_URL") {
		t.Errorf("output does not name ERP_REDIRECT_URL\n%s", &out)
	}
}

func TestPortfolioMain_HostedWithoutERPFakeBoots(t *testing.T) {
	bin := buildPortfolio(t)
	port := freePort(t)
	cmd := exec.Command(bin)
	cmd.Env = bootEnv(port, map[string]string{"DEPLOY_ENV": "production"})
	var out lockedBuf
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start portfolio: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(bootWait):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/healthz"
	for deadline := time.Now().Add(bootWait); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		select {
		case err := <-done:
			done <- err
			t.Fatalf("portfolio exited before /healthz answered: %v\n%s", err, &out)
		default:
		}
		if resp, err := http.Get(url); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
	}
	t.Fatalf("/healthz never answered 200\n%s", &out)
}
