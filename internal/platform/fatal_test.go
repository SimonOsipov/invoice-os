package platform_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

const (
	fatalHelperEnv  = "PLATFORM_FATAL_HELPER"
	fatalRailwaySHA = "0123456789abcdef0123456789abcdef01234567"
	bootFormat      = "svc: db pool: %v"
	bootMessage     = "svc: db pool: dial tcp: refused"
	pgPassword      = "S3cretPw"
)

// wireEvent is the event item as it crosses the wire; sentry.Event does not decode it.
type wireEvent struct {
	Environment string   `json:"environment"`
	Release     string   `json:"release"`
	ServerName  string   `json:"server_name"`
	Level       string   `json:"level"`
	Message     string   `json:"message"`
	Fingerprint []string `json:"fingerprint"`
	Exception   []struct {
		Type       string `json:"type"`
		Value      string `json:"value"`
		Stacktrace *struct {
			Frames []struct {
				Function string `json:"function"`
			} `json:"frames"`
		} `json:"stacktrace"`
	} `json:"exception"`
}

// ingest stands in for Sentry's endpoint and keeps every event item posted to it.
type ingest struct {
	srv *httptest.Server

	mu       sync.Mutex
	ackDelay time.Duration
	paths    []string
	bodies   []string
	events   []wireEvent
	parseErr error
}

func newIngest(t *testing.T) *ingest {
	t.Helper()
	in := &ingest{}
	in.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		in.mu.Lock()
		delay := in.ackDelay
		in.mu.Unlock()
		// A request whose sender is gone before the ack is not recorded.
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		in.mu.Lock()
		in.paths = append(in.paths, r.Method+" "+r.URL.Path)
		in.bodies = append(in.bodies, string(body))
		evs, err := envelopeEvents(body)
		in.events = append(in.events, evs...)
		if err != nil && in.parseErr == nil {
			in.parseErr = err
		}
		in.mu.Unlock()
		_, _ = io.WriteString(w, "{}")
	}))
	t.Cleanup(in.srv.Close)
	return in
}

// slowAck makes the endpoint record an event only if the sender is still connected
// after d, so a process that exits without flushing loses it.
func (in *ingest) slowAck(d time.Duration) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.ackDelay = d
}

// raw returns every request body received, for a search over the whole envelope.
func (in *ingest) raw() string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return strings.Join(in.bodies, "\n")
}

func (in *ingest) dsn() string {
	return "http://public@" + strings.TrimPrefix(in.srv.URL, "http://") + "/1"
}

func (in *ingest) requests() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return slices.Clone(in.paths)
}

// wantEvents fails unless exactly n event items arrived, and returns them.
func (in *ingest) wantEvents(t *testing.T, n int) []wireEvent {
	t.Helper()
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.parseErr != nil {
		t.Fatalf("ingest could not parse an envelope: %v", in.parseErr)
	}
	if len(in.events) != n {
		t.Fatalf("ingest received %d events, want %d (requests: %v)", len(in.events), n, in.paths)
	}
	return slices.Clone(in.events)
}

// envelopeEvents returns the event items of a newline-delimited envelope.
func envelopeEvents(body []byte) ([]wireEvent, error) {
	r := bufio.NewReader(bytes.NewReader(body))
	if _, err := r.ReadBytes('\n'); err != nil {
		return nil, err
	}
	var out []wireEvent
	for {
		hdrLine, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(hdrLine)) == 0 {
			return out, nil
		}
		var hdr struct {
			Type   string `json:"type"`
			Length int    `json:"length"`
		}
		if uerr := json.Unmarshal(hdrLine, &hdr); uerr != nil {
			return out, uerr
		}
		var payload []byte
		if hdr.Length > 0 {
			payload = make([]byte, hdr.Length)
			if _, rerr := io.ReadFull(r, payload); rerr != nil {
				return out, rerr
			}
			_, _ = r.ReadByte()
		} else if payload, err = r.ReadBytes('\n'); err != nil && err != io.EOF {
			return out, err
		}
		if hdr.Type == "event" {
			var ev wireEvent
			if uerr := json.Unmarshal(payload, &ev); uerr != nil {
				return out, uerr
			}
			out = append(out, ev)
		}
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

// runChild re-runs the test binary as TestFatalHelperProcess with a minimal
// environment, so no real SENTRY_DSN or proxy variable reaches the child.
func runChild(t *testing.T, scenario string, env ...string) (exit int, stdout, stderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFatalHelperProcess$", "-test.count=1")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + os.TempDir(),
		fatalHelperEnv + "=" + scenario,
	}
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+dir)
	}
	cmd.Env = append(cmd.Env, env...)
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
		t.Fatalf("run child %q: %v", scenario, err)
	}
	return exit, out.String(), errOut.String()
}

func productionEnv(dsn string) []string {
	return []string{
		"SENTRY_DSN=" + dsn,
		"RAILWAY_ENVIRONMENT_NAME=production",
		"ENVIRONMENT=development",
		"RAILWAY_GIT_COMMIT_SHA=" + fatalRailwaySHA,
	}
}

func wantLabels() sentrytest.Labels {
	release := "unstamped-" + fatalRailwaySHA
	if sha := platform.BuildSHA; sha != "" && sha != "dev" {
		release = sha
	}
	return sentrytest.Labels{Environment: "production", Release: release, ServerName: "svc"}
}

func assertEventLabels(t *testing.T, ev wireEvent) {
	t.Helper()
	sentrytest.AssertLabels(t, &sentry.Event{Environment: ev.Environment, Release: ev.Release, ServerName: ev.ServerName}, wantLabels())
}

// assertLoggedAtError requires at least one JSON record whose msg contains want,
// and every such record at ERROR.
func assertLoggedAtError(t *testing.T, stdout, want string) {
	t.Helper()
	found := 0
	for _, line := range strings.Split(stdout, "\n") {
		var rec struct {
			Level string `json:"level"`
			Msg   string `json:"msg"`
		}
		if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &rec) != nil || !strings.Contains(rec.Msg, want) {
			continue
		}
		found++
		if rec.Level != "ERROR" {
			t.Errorf("record %q logged at %s, want ERROR", rec.Msg, rec.Level)
		}
	}
	if found == 0 {
		t.Errorf("stdout has no record containing %q: %s", want, stdout)
	}
}

type blockingWorker struct{}

func (blockingWorker) Start(context.Context) error { return nil }

func (blockingWorker) Stop(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

//go:noinline
func bootStep() {
	defer platform.ReportBootPanic()
	panic(errors.New("boot: nil map"))
}

type bootFault struct{ Code int }

//go:noinline
func bootStepTyped() {
	defer platform.ReportBootPanic()
	panic(bootFault{Code: 42})
}

//go:noinline
func bootStepQuiet() {
	defer platform.ReportBootPanic()
}

// runToShutdownTimeout runs the app until a blocking worker overruns SHUTDOWN_TIMEOUT.
func runToShutdownTimeout(app *platform.App) error {
	app.AddBackgroundWorker(blockingWorker{})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	return app.Run(ctx)
}

// TestFatalHelperProcess is the child that runChild starts; it does nothing in a normal run.
func TestFatalHelperProcess(t *testing.T) {
	scenario := os.Getenv(fatalHelperEnv)
	if scenario == "" {
		return
	}
	switch scenario {
	case "boot-failure":
		app, err := platform.New("svc")
		if err != nil {
			t.Fatal(err)
		}
		platform.Fatal(app.Logger, bootFormat, errors.New("dial tcp: refused"))
	case "before-new":
		platform.Fatal(slog.Default(), "svc: startup: %v", errors.New("bad config"))
	case "shutdown-timeout":
		app, err := platform.New("svc")
		if err != nil {
			t.Fatal(err)
		}
		platform.Fatal(app.Logger, "svc: %v", runToShutdownTimeout(app))
	case "shutdown-wrapped":
		app, err := platform.New("svc")
		if err != nil {
			t.Fatal(err)
		}
		platform.Fatal(app.Logger, "svc: %s: %v", "run", fmt.Errorf("outer: %w", runToShutdownTimeout(app)))
	case "shutdown-lookalike":
		app, err := platform.New("svc")
		if err != nil {
			t.Fatal(err)
		}
		platform.Fatal(app.Logger, "svc: %v", errors.New("platform: graceful shutdown: not the sentinel"))
	case "nil-logger":
		if _, err := platform.New("svc"); err != nil {
			t.Fatal(err)
		}
		platform.Fatal(nil, bootFormat, errors.New("dial tcp: refused"))
	case "secrets":
		app, err := platform.New("svc")
		if err != nil {
			t.Fatal(err)
		}
		_, perr := pgxpool.ParseConfig("postgres://app:" + pgPassword + "@host:notaport/db")
		platform.Fatal(app.Logger, "svc: DATABASE_URL=%q: %v", "postgres://app:"+pgPassword+"@host/db", perr)
	case "boot-panic":
		if _, err := platform.New("svc"); err != nil {
			t.Fatal(err)
		}
		bootStep()
	case "boot-panic-typed":
		if _, err := platform.New("svc"); err != nil {
			t.Fatal(err)
		}
		bootStepTyped()
	case "boot-no-panic":
		if _, err := platform.New("svc"); err != nil {
			t.Fatal(err)
		}
		bootStepQuiet()
		os.Exit(0)
	default:
		t.Fatalf("unknown scenario %q", scenario)
	}
	// Reaching here means Fatal returned or the panic was swallowed.
	os.Exit(3)
}

// The slow ack makes a Fatal that exits without flushing lose the event.
func TestFatal_ReportsBeforeExit(t *testing.T) {
	in := newIngest(t)
	in.slowAck(400 * time.Millisecond)
	exit, stdout, _ := runChild(t, "boot-failure", productionEnv(in.dsn())...)
	if exit != 1 {
		t.Errorf("exit code = %d, want 1", exit)
	}
	assertLoggedAtError(t, stdout, bootMessage)

	ev := in.wantEvents(t, 1)[0]
	assertEventLabels(t, ev)
	if ev.Level != "fatal" {
		t.Errorf("event level = %q, want fatal", ev.Level)
	}
	if ev.Message != bootMessage {
		t.Errorf("event message = %q, want %q", ev.Message, bootMessage)
	}
	if want := []string{"boot-failure", bootFormat}; !slices.Equal(ev.Fingerprint, want) {
		t.Errorf("event fingerprint = %q, want %q", ev.Fingerprint, want)
	}
}

// The no-DSN half is a guard: a skipped exit fails it. The control makes it fail against a Fatal that never captures.
func TestFatal_NoDSNExitsWithoutSending(t *testing.T) {
	in := newIngest(t)
	exit, stdout, _ := runChild(t, "boot-failure", productionEnv("")...)
	if exit != 1 {
		t.Errorf("no DSN: exit code = %d, want 1", exit)
	}
	assertLoggedAtError(t, stdout, bootMessage)
	if reqs := in.requests(); len(reqs) != 0 {
		t.Errorf("no DSN: ingest received %v, want no requests", reqs)
	}

	if exit, _, _ := runChild(t, "boot-failure", productionEnv(in.dsn())...); exit != 1 {
		t.Errorf("control: exit code = %d, want 1", exit)
	}
	in.wantEvents(t, 1)
}

// Guard row; logging below ERROR fails it.
func TestFatal_LogLevelErrorStillLogs(t *testing.T) {
	exit, stdout, _ := runChild(t, "boot-failure", append(productionEnv(""), "LOG_LEVEL=error")...)
	if exit != 1 {
		t.Errorf("exit code = %d, want 1", exit)
	}
	assertLoggedAtError(t, stdout, bootMessage)
}

// Guard row; returning early when no client is bound fails it.
func TestFatal_BeforeNewStillExits(t *testing.T) {
	exit, stdout, stderr := runChild(t, "before-new")
	if exit != 1 {
		t.Errorf("exit code = %d, want 1", exit)
	}
	if out := stdout + stderr; !strings.Contains(out, "svc: startup: bad config") || !strings.Contains(out, "ERROR") {
		t.Errorf("output %q does not hold the message at ERROR", out)
	}
}

func TestFatal_GracefulShutdownErrorOpensNothing(t *testing.T) {
	in := newIngest(t)
	env := append(productionEnv(in.dsn()), "SHUTDOWN_TIMEOUT=50ms", "PORT="+freePort(t))
	exit, stdout, _ := runChild(t, "shutdown-timeout", env...)
	if exit != 1 {
		t.Errorf("exit code = %d, want 1", exit)
	}
	assertLoggedAtError(t, stdout, "platform: graceful shutdown")
	in.wantEvents(t, 0)

	// Control: the same ingest server counts a plain boot error.
	runChild(t, "boot-failure", productionEnv(in.dsn())...)
	in.wantEvents(t, 1)
}

// The slow ack makes a ReportBootPanic that re-panics without flushing lose the event.
func TestReportBootPanic_ReportsThenRepanics(t *testing.T) {
	in := newIngest(t)
	in.slowAck(400 * time.Millisecond)
	exit, _, stderr := runChild(t, "boot-panic", productionEnv(in.dsn())...)
	if exit != 2 {
		t.Errorf("exit code = %d, want 2", exit)
	}
	if !strings.Contains(stderr, "panic: boot: nil map") {
		t.Errorf("stderr %q lacks Go's panic output", stderr)
	}

	ev := in.wantEvents(t, 1)[0]
	assertEventLabels(t, ev)
	if ev.Level != "fatal" {
		t.Errorf("event level = %q, want fatal", ev.Level)
	}
	if len(ev.Exception) == 0 || ev.Exception[0].Value != "boot: nil map" {
		t.Fatalf("event exception = %+v, want value %q", ev.Exception, "boot: nil map")
	}
	named := false
	if st := ev.Exception[0].Stacktrace; st != nil {
		for _, f := range st.Frames {
			named = named || strings.Contains(f.Function, "bootStep")
		}
	}
	if !named {
		t.Errorf("no stack frame names bootStep: %+v", ev.Exception[0].Stacktrace)
	}

	// Sentry off: still re-panics, and adds no event to the first child's.
	exit, _, stderr = runChild(t, "boot-panic", productionEnv("")...)
	if exit != 2 || !strings.Contains(stderr, "panic: boot: nil map") {
		t.Errorf("no DSN: exit %d, stderr %q, want exit 2 with the panic output", exit, stderr)
	}
	in.wantEvents(t, 1)
}

// Second half of the panic test: the re-panic carries the original value, not its text.
func TestReportBootPanic_RepanicsTheSameValue(t *testing.T) {
	in := newIngest(t)
	exit, _, stderr := runChild(t, "boot-panic-typed", productionEnv(in.dsn())...)
	if exit != 2 {
		t.Errorf("exit code = %d, want 2", exit)
	}
	// Go marks a re-panic of the recovered value itself; a different value prints a second "panic:" line.
	if first, _, _ := strings.Cut(stderr, "\n"); !strings.HasPrefix(first, "panic: (platform_test.bootFault)") || !strings.HasSuffix(first, "[recovered, repanicked]") {
		t.Errorf("first stderr line %q, want the original bootFault marked [recovered, repanicked]", first)
	}
	if ev := in.wantEvents(t, 1)[0]; ev.Level != "fatal" {
		t.Errorf("event level = %q, want fatal", ev.Level)
	}
}

func TestReportBootPanic_NoPanicDoesNothing(t *testing.T) {
	in := newIngest(t)
	exit, _, stderr := runChild(t, "boot-no-panic", productionEnv(in.dsn())...)
	if exit != 0 {
		t.Errorf("exit code = %d, want 0", exit)
	}
	if strings.Contains(stderr, "panic") {
		t.Errorf("stderr %q shows a panic", stderr)
	}
	in.wantEvents(t, 0)

	// Control: the same ingest server counts a real boot panic.
	runChild(t, "boot-panic", productionEnv(in.dsn())...)
	in.wantEvents(t, 1)
}

func TestFatal_NilLoggerFallsBackToDefault(t *testing.T) {
	in := newIngest(t)
	exit, stdout, _ := runChild(t, "nil-logger", productionEnv(in.dsn())...)
	if exit != 1 {
		t.Errorf("exit code = %d, want 1", exit)
	}
	assertLoggedAtError(t, stdout, bootMessage)
	if ev := in.wantEvents(t, 1)[0]; ev.Message != bootMessage {
		t.Errorf("event message = %q, want %q", ev.Message, bootMessage)
	}
}

// The skip follows errors.Is on the sentinel, never the error text.
func TestFatal_ShutdownSkipFollowsTheSentinelNotTheText(t *testing.T) {
	in := newIngest(t)
	env := append(productionEnv(in.dsn()), "SHUTDOWN_TIMEOUT=50ms", "PORT="+freePort(t))

	exit, stdout, _ := runChild(t, "shutdown-lookalike", env...)
	if exit != 1 {
		t.Errorf("lookalike: exit code = %d, want 1", exit)
	}
	assertLoggedAtError(t, stdout, "not the sentinel")
	ev := in.wantEvents(t, 1)[0]
	if !strings.Contains(ev.Message, "not the sentinel") {
		t.Errorf("lookalike event message = %q, want the lookalike text", ev.Message)
	}

	exit, stdout, _ = runChild(t, "shutdown-wrapped", env...)
	if exit != 1 {
		t.Errorf("wrapped: exit code = %d, want 1", exit)
	}
	assertLoggedAtError(t, stdout, "outer: platform: graceful shutdown")
	in.wantEvents(t, 1)
}

// Boot errors can carry connection strings; scrubEvent must cover this path.
func TestFatal_EventHoldsNoConnectionPassword(t *testing.T) {
	in := newIngest(t)
	exit, stdout, _ := runChild(t, "secrets", productionEnv(in.dsn())...)
	if exit != 1 {
		t.Errorf("exit code = %d, want 1", exit)
	}
	if !strings.Contains(stdout, "DATABASE_URL=") {
		t.Errorf("stdout lacks the message: %s", stdout)
	}
	ev := in.wantEvents(t, 1)[0]
	if !strings.Contains(ev.Message, "DATABASE_URL=") {
		t.Errorf("event message = %q, want the boot message", ev.Message)
	}
	if want := []string{"boot-failure", "svc: DATABASE_URL=%q: %v"}; !slices.Equal(ev.Fingerprint, want) {
		t.Errorf("event fingerprint = %q, want %q", ev.Fingerprint, want)
	}
	if raw := in.raw(); strings.Contains(raw, pgPassword) {
		t.Errorf("the envelope holds the connection password: %s", raw)
	}
}
