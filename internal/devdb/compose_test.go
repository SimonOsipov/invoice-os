package devdb_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An orphaned child that exits non-zero must not crash-restart the dev Postgres.
func TestDevComposePostgres_OrphanedChildDoesNotCrashServer(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker unavailable: %v", err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	compose := filepath.Join(wd, "..", "..", "docker-compose.yml")

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	project := fmt.Sprintf("devcomposetest%d", time.Now().UnixNano())
	run := func(timeout time.Duration, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		full := append([]string{"compose", "-p", project, "-f", compose}, args...)
		cmd := exec.CommandContext(ctx, "docker", full...)
		cmd.Env = append(cmd.Environ(), fmt.Sprintf("DEV_DB_PORT=%d", port))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	t.Cleanup(func() { _, _ = run(90*time.Second, "down", "-v", "--timeout", "10") })

	if out, err := run(180*time.Second, "up", "-d", "--wait", "--wait-timeout", "120", "postgres"); err != nil {
		t.Fatalf("compose up: %v\n%s", err, out)
	}

	if out, err := run(30*time.Second, "exec", "-T", "postgres", "sh", "-c",
		"(pg_isready -h /nonexistent >/dev/null &)"); err != nil {
		t.Fatalf("spawn orphan: %v\n%s", err, out)
	}

	// Wait for the orphan to exit, then give the server a moment to log.
	gone := false
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		_, err := run(15*time.Second, "exec", "-T", "postgres", "sh", "-c",
			"grep -l pg_isready /proc/[0-9]*/comm")
		if err != nil { // grep exits 1 when no process matches
			gone = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !gone {
		t.Fatal("orphaned pg_isready still running after 20s")
	}

	const crashLine = "terminating any other active server processes"
	var matched []string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		logs, err := run(30*time.Second, "logs", "--no-color", "--no-log-prefix", "postgres")
		if err != nil {
			t.Fatalf("compose logs: %v\n%s", err, logs)
		}
		matched = matched[:0]
		for _, line := range strings.Split(logs, "\n") {
			if strings.Contains(line, crashLine) || strings.Contains(line, "untracked child process") {
				matched = append(matched, line)
			}
		}
		if len(matched) > 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(matched) > 0 {
		t.Fatalf("postgres crash-restarted after an orphaned child exited; log lines:\n%s", strings.Join(matched, "\n"))
	}
}
