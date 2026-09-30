package platform_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// buildMain compiles one cmd/<svc> binary so a boot failure runs the real main.
func buildMain(t *testing.T, svc string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), svc)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, "../../cmd/"+svc).CombinedOutput(); err != nil {
		t.Fatalf("go build cmd/%s: %v\n%s", svc, err, out)
	}
	return bin
}

// runMain runs a built main with only the given environment.
func runMain(t *testing.T, bin string, env ...string) (exit int, stdout, stderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = append([]string{"HOME=" + os.Getenv("HOME")}, env...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		exit = 0
	case errors.As(err, &exitErr):
		exit = exitErr.ExitCode()
	default:
		t.Fatalf("run %s: %v", bin, err)
	}
	return exit, out.String(), errOut.String()
}

// A boot failure in a real main exits 1, logs at ERROR whichever logger is bound, and
// after platform.New opens one fatal event. Only a built binary reaches the main's own exit call.
func TestCmdMainsBootFailureExitsOneLogsAtErrorAndReports(t *testing.T) {
	t.Run("before platform.New the default logger still shows ERROR", func(t *testing.T) {
		exit, stdout, stderr := runMain(t, buildMain(t, "notifications"), "PORT=not-a-port")
		if exit != 1 {
			t.Errorf("exit code = %d, want 1 (output %q)", exit, stdout+stderr)
		}
		if out := stdout + stderr; !strings.Contains(out, "ERROR") || !strings.Contains(out, "notifications: startup: ") {
			t.Errorf("output %q does not hold notifications' startup failure at ERROR", out)
		}
	})

	t.Run("after platform.New it logs JSON at ERROR and reports one event", func(t *testing.T) {
		in := newIngest(t)
		in.slowAck(300 * time.Millisecond)
		// DATABASE_URL is unset, so dashboard's mustEnv exits through platform.Fatal.
		exit, stdout, _ := runMain(t, buildMain(t, "dashboard"), productionEnv(in.dsn())...)
		if exit != 1 {
			t.Errorf("exit code = %d, want 1", exit)
		}
		const msg = "dashboard: DATABASE_URL is required"
		assertLoggedAtError(t, stdout, msg)

		ev := in.wantEvents(t, 1)[0]
		if ev.Level != "fatal" || ev.Message != msg {
			t.Errorf("event = level %q message %q, want fatal %q", ev.Level, ev.Message, msg)
		}
		if want := []string{"boot-failure", "dashboard: %s is required"}; !slices.Equal(ev.Fingerprint, want) {
			t.Errorf("event fingerprint = %q, want %q", ev.Fingerprint, want)
		}
		if want := "dashboard"; ev.ServerName != want {
			t.Errorf("event server name = %q, want %q", ev.ServerName, want)
		}
	})
}
