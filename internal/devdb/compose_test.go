package devdb_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const composeLabel = "com.docker.compose.project"

// composeProject is one throwaway compose project of the dev Postgres.
type composeProject struct {
	name, file string
	port       int
}

func (p composeProject) run(timeout time.Duration, args ...string) (string, error) {
	return p.runCtx(context.Background(), timeout, args...)
}

func (p composeProject) runCtx(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	full := append([]string{"compose", "-p", p.name, "-f", p.file}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	cmd.Env = append(cmd.Environ(), fmt.Sprintf("DEV_DB_PORT=%d", p.port))
	cmd.WaitDelay = 5 * time.Second // the compose plugin child can hold the pipe after docker is killed
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// left lists the project's containers, volumes and networks; a failed query counts as "still there".
func (p composeProject) left(ctx context.Context) string {
	var left []string
	for _, q := range [][]string{{"ps", "-aq"}, {"volume", "ls", "-q"}, {"network", "ls", "-q"}} {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		args := append(q, "--filter", "label="+composeLabel+"="+p.name)
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.WaitDelay = 5 * time.Second
		out, err := cmd.Output()
		cancel()
		if err != nil {
			left = append(left, q[0]+" query failed: "+err.Error())
		} else if s := strings.TrimSpace(string(out)); s != "" {
			left = append(left, s)
		}
	}
	return strings.Join(left, "\n")
}

// down retries for 2 minutes, since a Docker VM gap makes single attempts fail; leftovers
// are only logged and the next run's sweepStale removes them.
func (p composeProject) down(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), deadline) // bounds each attempt too
	defer cancel()
	for {
		_, _ = p.runCtx(ctx, 60*time.Second, "down", "-v", "--timeout", "10", "--remove-orphans")
		// the budget may be spent; query on a fresh short context so leftovers are still logged
		qctx, qcancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		left := p.left(qctx)
		qcancel()
		if left == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Logf("compose project %s not removed, left for the next sweep:\n%s", p.name, left)
			return
		}
		time.Sleep(10 * time.Second)
	}
}

// requireDocker skips when docker is missing or gives no answer in 30s (it hangs in a VM gap).
func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	info := exec.CommandContext(ctx, "docker", "info")
	info.WaitDelay = 5 * time.Second
	if err := info.Run(); err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
}

// staleComposeProject reports a devcomposetest<UnixNano> project older than 30 minutes, so a
// parallel test in another worktree is never swept.
func staleComposeProject(name string, now time.Time) bool {
	digits, ok := strings.CutPrefix(name, "devcomposetest")
	if !ok {
		return false
	}
	ns, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return false
	}
	return now.Sub(time.Unix(0, ns)) > 30*time.Minute
}

// sweepStale removes the containers, volumes and networks of stale projects left by killed runs.
func sweepStale(t *testing.T, file string) {
	t.Helper()
	stale := map[string]bool{}
	for _, q := range [][]string{{"ps", "-a"}, {"volume", "ls"}, {"network", "ls"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		args := append(q, "--filter", "label="+composeLabel, "--format", `{{.Label "`+composeLabel+`"}}`)
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.WaitDelay = 5 * time.Second
		out, err := cmd.Output()
		cancel()
		if err != nil {
			t.Logf("sweep: docker %s: %v", strings.Join(q, " "), err)
			continue
		}
		for _, name := range strings.Fields(string(out)) {
			if staleComposeProject(name, time.Now()) {
				stale[name] = true
			}
		}
	}
	for name := range stale {
		p := composeProject{name: name, file: file}
		if out, err := p.run(60*time.Second, "down", "-v", "--timeout", "0", "--remove-orphans"); err != nil {
			t.Logf("sweep %s: %v\n%s", name, err, out)
		}
	}
}

// An orphaned child that exits non-zero must not crash-restart the dev Postgres.
func TestDevComposePostgres_OrphanedChildDoesNotCrashServer(t *testing.T) {
	requireDocker(t)

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	compose := filepath.Join(wd, "..", "..", "docker-compose.yml")
	sweepStale(t, compose)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	p := composeProject{name: fmt.Sprintf("devcomposetest%d", time.Now().UnixNano()), file: compose, port: port}
	run := p.run
	t.Cleanup(func() { p.down(t) })

	if out, err := run(180*time.Second, "up", "-d", "--wait", "--wait-timeout", "120", "postgres"); err != nil {
		t.Fatalf("compose up: %v\n%s", err, out)
	}

	if out, err := run(30*time.Second, "exec", "-T", "postgres", "cat", "/proc/1/cmdline"); err != nil {
		t.Fatalf("read pid 1 cmdline: %v\n%s", err, out)
	} else if !strings.Contains(out, "docker-init") {
		t.Fatalf("PID 1 is not docker-init: %q", out)
	}

	const startQuery = "select pg_postmaster_start_time()"
	startBefore, err := run(30*time.Second, "exec", "-T", "postgres", "psql", "-U", "postgres", "-d", "invoice_os", "-tAc", startQuery)
	if err != nil || strings.TrimSpace(startBefore) == "" {
		t.Fatalf("postmaster start time: %v\n%s", err, startBefore)
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

	startAfter, err := run(30*time.Second, "exec", "-T", "postgres", "psql", "-U", "postgres", "-d", "invoice_os", "-tAc", startQuery)
	if err != nil {
		t.Fatalf("postmaster start time after orphan: %v\n%s", err, startAfter)
	}
	if strings.TrimSpace(startAfter) != strings.TrimSpace(startBefore) {
		t.Fatalf("postmaster restarted: start time %q -> %q", startBefore, startAfter)
	}
}
