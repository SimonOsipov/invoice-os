package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

const (
	msgGranted    = "gateway: account state read granted"
	msgNotGranted = "gateway: account state read not granted"
	msgNotStarted = "gateway: account state read not started"
	msgSkipped    = "gateway: account state grant skipped in a hosted environment"

	grantSecret  = "s3cret-xyz"
	grantBadDSN  = "::not a url"
	grantMigDSN  = "postgresql://invoice_migrator:m@h:5432/railway?sslmode=disable"
	grantAuthDSN = "postgresql://supabase_auth_admin:pw@h:5432/railway?sslmode=disable"
)

type logRec struct {
	level, msg string
	attrs      map[string]any
}

type capturedLog struct {
	*slog.Logger
	buf *bytes.Buffer
}

func newCapturedLog() capturedLog {
	buf := &bytes.Buffer{}
	return capturedLog{slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf}
}

func (c capturedLog) records(t *testing.T) []logRec {
	t.Helper()
	var out []logRec
	for _, line := range strings.Split(strings.TrimSpace(c.buf.String()), "\n") {
		if line == "" {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, logRec{level: fmt.Sprint(m["level"]), msg: fmt.Sprint(m["msg"]), attrs: m})
	}
	return out
}

func (c capturedLog) count(t *testing.T, level, msg string) int {
	t.Helper()
	n := 0
	for _, r := range c.records(t) {
		if r.level == level && r.msg == msg {
			n++
		}
	}
	return n
}

func (c capturedLog) countLevel(t *testing.T, level string) int {
	t.Helper()
	n := 0
	for _, r := range c.records(t) {
		if r.level == level {
			n++
		}
	}
	return n
}

type grantStep struct {
	ok  bool
	err error
}

func scriptedGrant(steps []grantStep, calls *int) func(context.Context, string) (bool, error) {
	return func(context.Context, string) (bool, error) {
		i := *calls
		*calls++
		if i >= len(steps) {
			return false, nil
		}
		return steps[i].ok, steps[i].err
	}
}

func TestGrantAccountStateRead_RetriesUntilGoTrueMigrated(t *testing.T) {
	t.Run("false_then_error_then_true", func(t *testing.T) {
		log := newCapturedLog()
		calls := 0
		grantAccountStateRead(context.Background(), grantAuthDSN,
			scriptedGrant([]grantStep{{false, nil}, {false, fmt.Errorf("db: connect: refused")}, {true, nil}, {true, nil}}, &calls),
			time.Millisecond, 10, log.Logger)
		if calls != 3 {
			t.Errorf("grant calls = %d, want 3 (stop at the first true)", calls)
		}
		if n := log.count(t, "INFO", msgGranted); n != 1 {
			t.Errorf("INFO %q count = %d, want 1; log: %s", msgGranted, n, log.buf)
		}
		if n := log.count(t, "WARN", msgNotGranted); n != 0 {
			t.Errorf("WARN %q count = %d, want 0 after a success", msgNotGranted, n)
		}
	})

	t.Run("first_call_true_makes_no_second_call", func(t *testing.T) {
		log := newCapturedLog()
		calls := 0
		grantAccountStateRead(context.Background(), grantAuthDSN, scriptedGrant([]grantStep{{true, nil}}, &calls), time.Millisecond, 10, log.Logger)
		if calls != 1 {
			t.Errorf("grant calls = %d, want 1", calls)
		}
		if n := log.count(t, "INFO", msgGranted); n != 1 {
			t.Errorf("INFO %q count = %d, want 1", msgGranted, n)
		}
	})

	t.Run("the_dsn_reaches_grant_unchanged", func(t *testing.T) {
		var got string
		grantAccountStateRead(context.Background(), grantAuthDSN, func(_ context.Context, dsn string) (bool, error) {
			got = dsn
			return true, nil
		}, time.Millisecond, 1, newCapturedLog().Logger)
		if got != grantAuthDSN {
			t.Errorf("grant got dsn %q, want %q", got, grantAuthDSN)
		}
	})
}

func TestGrantAccountStateRead_GivesUpWithOneWarn(t *testing.T) {
	for name, step := range map[string]grantStep{
		"false": {false, nil},
		"error": {false, fmt.Errorf("db: connect: refused")},
	} {
		t.Run(name, func(t *testing.T) {
			log := newCapturedLog()
			calls := 0
			steps := []grantStep{step, step, step, step, step, step}
			grantAccountStateRead(context.Background(), grantAuthDSN, scriptedGrant(steps, &calls), time.Millisecond, 4, log.Logger)
			if calls != 4 {
				t.Errorf("grant calls = %d, want 4", calls)
			}
			if n := log.count(t, "WARN", msgNotGranted); n != 1 {
				t.Fatalf("WARN %q count = %d, want 1; log: %s", msgNotGranted, n, log.buf)
			}
			for _, r := range log.records(t) {
				if r.msg == msgNotGranted && r.attrs["attempts"] != float64(4) {
					t.Errorf("WARN attempts = %v, want 4", r.attrs["attempts"])
				}
			}
			if n := log.count(t, "INFO", msgGranted); n != 0 {
				t.Errorf("INFO %q count = %d, want 0", msgGranted, n)
			}
		})
	}
}

func TestGrantAccountStateRead_StopsWhenTheContextEnds(t *testing.T) {
	log := newCapturedLog()
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	grant := func(context.Context, string) (bool, error) {
		calls++
		cancel()
		return false, nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		grantAccountStateRead(ctx, grantAuthDSN, grant, time.Hour, 60, log.Logger)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("grantAccountStateRead did not return after its context ended")
	}
	if calls != 1 {
		t.Errorf("grant calls = %d, want 1 (the call that cancelled)", calls)
	}
	if n := log.count(t, "WARN", msgNotGranted); n != 0 {
		t.Errorf("WARN %q count = %d, want 0 on cancellation", msgNotGranted, n)
	}

	t.Run("cancel_during_the_last_attempt_logs_nothing", func(t *testing.T) {
		log := newCapturedLog()
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		grantAccountStateRead(ctx, grantAuthDSN, func(context.Context, string) (bool, error) {
			calls++
			cancel()
			return false, nil
		}, time.Millisecond, 1, log.Logger)
		if calls != 1 {
			t.Fatalf("grant calls = %d, want 1", calls)
		}
		if got := log.records(t); len(got) != 0 {
			t.Errorf("log lines after a cancelled last attempt = %v, want none", got)
		}
	})
}

func TestGrantAccountStateRead_LogsCarryNoSecret(t *testing.T) {
	dsn := "postgresql://supabase_auth_admin:" + grantSecret + "@h:5432/railway"
	log := newCapturedLog()
	grant := func(context.Context, string) (bool, error) {
		return false, fmt.Errorf("connect %s: refused", dsn)
	}
	grantAccountStateRead(context.Background(), dsn, grant, time.Millisecond, 3, log.Logger)
	if n := log.count(t, "WARN", msgNotGranted); n != 1 {
		t.Fatalf("control: WARN %q count = %d, want 1 (an empty log proves nothing)", msgNotGranted, n)
	}
	out := log.buf.String()
	if strings.Contains(out, grantSecret) {
		t.Errorf("log carries the password: %s", out)
	}
	if strings.Contains(out, dsn) {
		t.Errorf("log carries the DSN: %s", out)
	}
}

type startProbe struct {
	dsns []string
}

func (p *startProbe) start(dsn string) { p.dsns = append(p.dsns, dsn) }

func TestStartAccountStateGrant_AnEmptyPasswordStartsNothing(t *testing.T) {
	log := newCapturedLog()
	var p startProbe
	if got := startAccountStateGrant("pr-12", grantMigDSN, "", log.Logger, p.start); got {
		t.Error("startAccountStateGrant = true, want false")
	}
	if len(p.dsns) != 0 {
		t.Errorf("start called %d times, want 0", len(p.dsns))
	}
	if n := log.count(t, "WARN", msgNotStarted); n != 1 {
		t.Errorf("WARN %q count = %d, want 1; log: %s", msgNotStarted, n, log.buf)
	}
}

func TestStartAccountStateGrant_ABadDSNStartsNothing(t *testing.T) {
	for name, dsn := range map[string]string{
		"unparseable": grantBadDSN,
		"bad_port":    "postgres://invoice_migrator:" + grantSecret + "@h:notaport/railway",
		"no_host":     "postgres:///railway",
	} {
		t.Run(name, func(t *testing.T) {
			log := newCapturedLog()
			var p startProbe
			if got := startAccountStateGrant("pr-12", dsn, grantSecret, log.Logger, p.start); got {
				t.Error("startAccountStateGrant = true, want false")
			}
			if len(p.dsns) != 0 {
				t.Errorf("start called %d times, want 0", len(p.dsns))
			}
			if n := log.count(t, "WARN", msgNotStarted); n != 1 {
				t.Fatalf("WARN %q count = %d, want 1; log: %s", msgNotStarted, n, log.buf)
			}
			out := log.buf.String()
			if strings.Contains(out, grantSecret) || strings.Contains(out, dsn) {
				t.Errorf("log carries the password or the DSN: %s", out)
			}
		})
	}
}

func TestStartAccountStateGrant_StartsOnceWithTheOwnersDSN(t *testing.T) {
	log := newCapturedLog()
	var p startProbe
	if got := startAccountStateGrant("pr-12", grantMigDSN, "pw", log.Logger, p.start); !got {
		t.Error("startAccountStateGrant = false, want true")
	}
	if len(p.dsns) != 1 || p.dsns[0] != grantAuthDSN {
		t.Errorf("start calls = %q, want exactly [%q]", p.dsns, grantAuthDSN)
	}
	if n := log.countLevel(t, "WARN"); n != 0 {
		t.Errorf("WARN count = %d, want 0; log: %s", n, log.buf)
	}
	if strings.Contains(log.buf.String(), "pw@") || strings.Contains(log.buf.String(), grantAuthDSN) {
		t.Errorf("log carries the password or the DSN: %s", log.buf)
	}
}

// Names are RAILWAY_ENVIRONMENT_NAME values; the posture each maps to comes from
// platform.Posture (PostureHosted for all of them, PostureLocal for "", PosturePreview for a PR name).
func TestStartAccountStateGrant_AHostedEnvironmentStartsNothing(t *testing.T) {
	for _, name := range []string{"production", "Production", "development", "staging", "pr-12 ", "pr-", "PR-12", " pr-12", "pr-12\n", "pr-12-b", "invoice-os-PR-12"} {
		if got := platform.Posture(name); got != platform.PostureHosted {
			t.Fatalf("control: platform.Posture(%q) = %q, want %q; the case does not test the hosted gate", name, got, platform.PostureHosted)
		}
		for label, c := range map[string]struct{ dsn, pw string }{
			"valid_input":         {grantMigDSN, "pw"},
			"empty_password":      {grantMigDSN, ""},
			"unparseable_dsn":     {grantBadDSN, "pw"},
			"secret_never_logged": {grantMigDSN, grantSecret},
		} {
			t.Run(fmt.Sprintf("%q/%s", name, label), func(t *testing.T) {
				log := newCapturedLog()
				var p startProbe
				if got := startAccountStateGrant(name, c.dsn, c.pw, log.Logger, p.start); got {
					t.Error("startAccountStateGrant = true, want false")
				}
				if len(p.dsns) != 0 {
					t.Errorf("start called %d times, want 0", len(p.dsns))
				}
				if n := log.count(t, "INFO", msgSkipped); n != 1 {
					t.Errorf("INFO %q count = %d, want 1; log: %s", msgSkipped, n, log.buf)
				}
				if n := log.countLevel(t, "WARN"); n != 0 {
					t.Errorf("WARN count = %d, want 0 (a hosted skip must not read the password); log: %s", n, log.buf)
				}
				if strings.Contains(log.buf.String(), grantSecret) {
					t.Errorf("log carries the password: %s", log.buf)
				}
			})
		}
	}
}

func TestStartAccountStateGrant_LocalAndPRForksStart(t *testing.T) {
	want := map[string]platform.PostureKind{
		"":                 platform.PostureLocal,
		"pr-12":            platform.PosturePreview,
		"invoice-os-pr-12": platform.PosturePreview,
	}
	for name, posture := range want {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			if got := platform.Posture(name); got != posture {
				t.Fatalf("control: platform.Posture(%q) = %q, want %q", name, got, posture)
			}
			log := newCapturedLog()
			var p startProbe
			if got := startAccountStateGrant(name, grantMigDSN, "pw", log.Logger, p.start); !got {
				t.Error("startAccountStateGrant = false, want true")
			}
			if len(p.dsns) != 1 {
				t.Errorf("start called %d times, want 1", len(p.dsns))
			}
			if n := log.count(t, "INFO", msgSkipped); n != 0 {
				t.Errorf("INFO %q count = %d, want 0", msgSkipped, n)
			}
		})
	}
}
