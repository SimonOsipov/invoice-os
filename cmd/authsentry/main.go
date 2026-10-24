// Command authsentry runs GoTrue and forwards its server-side failures to Sentry.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

const (
	maxClassifiedLine = 64 << 10
	lineQueue         = 1024
	exitCodeNotFound  = 127
	crashFlush        = 5 * time.Second
	signalFlush       = 2 * time.Second
	testEventLine     = `{"level":"error","msg":"request completed","status":500,"error":"sentry test event","path":"/sentry-test-event"}`
)

// newTransport is a seam for tests.
var newTransport = func() sentry.Transport {
	return newBufferedTransport(&http.Client{}, time.Now, nil)
}

func main() {
	defer platform.ReportBootPanic()
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// run starts args as GoTrue (default "auth") and returns its exit code.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	cfg, on := initSentry(getenv, stderr)
	cl := newClassifier()

	// Registered before the start so a signal in the gap is not lost.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigs)

	if on && cfg.SentryTestEvent {
		if ev := cl.line([]byte(testEventLine)); ev != nil {
			ev.Fingerprint = []string{"sentry-test-event", platform.AuthService}
			capture(ev)
		}
	}

	cmd, pipe := startGoTrue(args, stdout)
	if err := cmd.Start(); err != nil {
		finish(on, cl.exit(0, err), crashFlush)
		return exitCodeNotFound
	}

	var forwarded atomic.Bool
	stopFwd := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				forwarded.Store(true)
				_ = cmd.Process.Signal(s)
			case <-stopFwd:
				return
			}
		}
	}()

	var lines chan []byte
	classified := make(chan struct{})
	if on {
		lines = make(chan []byte, lineQueue)
		go classify(cl, lines, classified)
	}
	pumpStderr(pipe, stderr, lines)

	err := cmd.Wait()
	close(stopFwd)
	code := exitCode(cmd, err)

	budget := crashFlush
	var ev *sentry.Event
	if forwarded.Load() {
		budget = signalFlush
	}
	if on {
		// A stuck transport must not hold the exit past the budget.
		deadline := time.Now().Add(budget)
		select {
		case <-classified:
		case <-time.After(time.Until(deadline)):
		}
		if !forwarded.Load() {
			ev = cl.exit(code, nil)
		}
		finish(on, ev, time.Until(deadline))
	}
	return code
}

// initSentry turns Sentry on when the config allows. Any failure leaves it off and GoTrue running.
func initSentry(getenv func(string) string, stderr io.Writer) (platform.Config, bool) {
	if getenv("SENTRY_DSN") == "" {
		return platform.Config{}, false
	}
	cfg, err := platform.LoadConfig(platform.AuthService)
	if err == nil {
		err = platform.InitSentry(cfg, newTransport())
	}
	if err != nil {
		b, _ := json.Marshal(map[string]string{"component": "authsentry", "level": "warning", "msg": "sentry off: " + err.Error()})
		fmt.Fprintf(stderr, "%s\n", b)
		return cfg, false
	}
	return cfg, true
}

// exitCode is GoTrue's exit code, or 128 + n when signal n killed it.
func exitCode(cmd *exec.Cmd, waitErr error) int {
	if st, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && st.Signaled() {
		return 128 + int(st.Signal())
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) || waitErr == nil {
		return cmd.ProcessState.ExitCode()
	}
	return 1
}

// pumpStderr copies r to w byte for byte, then offers each line to lines.
// An oversize line is offered as its first 64 KiB; a full queue skips classification, never the copy.
func pumpStderr(r io.Reader, w io.Writer, lines chan<- []byte) {
	defer func() {
		if lines != nil {
			close(lines)
		}
	}()
	br := bufio.NewReaderSize(r, maxClassifiedLine)
	defer func() {
		if recover() != nil {
			_, _ = io.Copy(w, br)
		}
	}()
	var prefix []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			_, _ = w.Write(chunk)
			if lines != nil && prefix == nil {
				prefix = append([]byte(nil), chunk...)
			}
			if err == bufio.ErrBufferFull {
				continue
			}
			if lines != nil {
				select {
				case lines <- prefix:
				default:
				}
			}
			prefix = nil
		}
		if err != nil && err != bufio.ErrBufferFull {
			return
		}
	}
}

// classify drops a line whose classification or capture panics.
func classify(cl *classifier, lines <-chan []byte, done chan<- struct{}) {
	defer close(done)
	for b := range lines {
		func() {
			defer func() { _ = recover() }()
			if ev := cl.line(b); ev != nil {
				sentry.CurrentHub().CaptureEvent(ev)
			}
		}()
	}
}

func capture(ev *sentry.Event) {
	defer func() { _ = recover() }()
	sentry.CurrentHub().CaptureEvent(ev)
}

// finish captures ev, then flushes, both within d.
func finish(on bool, ev *sentry.Event, d time.Duration) {
	if !on || d <= 0 {
		return
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() { _ = recover() }()
		if ev != nil {
			sentry.CurrentHub().CaptureEvent(ev)
		}
		sentry.Flush(d)
	}()
	idle := make(chan struct{})
	go func() { wg.Wait(); close(idle) }()
	select {
	case <-idle:
	case <-time.After(d):
	}
}

// startGoTrue prepares args (default "auth") with stderr piped to the caller.
func startGoTrue(args []string, stdout io.Writer) (*exec.Cmd, io.ReadCloser) {
	if len(args) == 0 {
		args = []string{"auth"}
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = stdout
	pipe, err := cmd.StderrPipe()
	if err != nil {
		platform.Fatal(slog.Default(), "authsentry: stderr pipe: %v", err)
	}
	return cmd, pipe
}
