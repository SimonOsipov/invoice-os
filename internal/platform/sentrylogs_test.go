package platform_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

// captureStdout points os.Stdout at a pipe before Boot builds the logger; call the
// returned func once to restore it and read what the process wrote.
func captureStdout(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stdout
	os.Stdout = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	t.Cleanup(func() { os.Stdout = prev })
	return func() string {
		os.Stdout = prev
		_ = w.Close()
		<-done
		_ = r.Close()
		return buf.String()
	}
}

func logsWithBody(rec *sentrytest.Recorder, body string) []sentry.Log {
	var out []sentry.Log
	for _, l := range rec.Logs() {
		if l.Body == body {
			out = append(out, l)
		}
	}
	return out
}

// oneLogWithBody fails unless exactly one recorded log has the body.
func oneLogWithBody(t *testing.T, rec *sentrytest.Recorder, body string) sentry.Log {
	t.Helper()
	got := logsWithBody(rec, body)
	if len(got) != 1 {
		t.Fatalf("recorded %d logs with body %q, want 1 (all bodies: %v)", len(got), body, logBodies(rec))
	}
	return got[0]
}

func logBodies(rec *sentrytest.Recorder) []string {
	var out []string
	for _, l := range rec.Logs() {
		out = append(out, l.Body)
	}
	return out
}

func logAttrString(t *testing.T, l sentry.Log, key string) string {
	t.Helper()
	v, ok := l.Attributes[key]
	if !ok {
		t.Fatalf("log %q has no attribute %q (have %v)", l.Body, key, attrNames(l))
	}
	return v.AsString()
}

func attrNames(l sentry.Log) []string {
	var keys []string
	for k := range l.Attributes {
		keys = append(keys, k)
	}
	return keys
}

func idsContext() (context.Context, string) {
	tenant := uuid.NewString()
	return platform.WithTenantID(platform.WithRequestID(context.Background(), "req-logs-1"), tenant), tenant
}

func TestSentryLogs_RecordCarriesServiceAndRequestContext(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "invoice")
	ctx, tenant := idsContext()

	slog.Default().InfoContext(ctx, "hello")

	l := oneLogWithBody(t, rec, "hello")
	if l.Level != sentry.LogLevelInfo {
		t.Errorf("log level = %q, want info", l.Level)
	}
	if got := logAttrString(t, l, "service"); got != "invoice" {
		t.Errorf("service = %q, want invoice", got)
	}
	if got := logAttrString(t, l, "request_id"); got != "req-logs-1" {
		t.Errorf("request_id = %q, want req-logs-1", got)
	}
	if got := logAttrString(t, l, "tenant_id"); got != tenant {
		t.Errorf("tenant_id = %q, want %q", got, tenant)
	}
}

func TestSentryLogs_ForwardsFromTheConfiguredLevelUp(t *testing.T) {
	t.Run("warn", func(t *testing.T) {
		t.Setenv("LOG_LEVEL", "warn")
		_, rec, _ := sentrytest.Boot(t, "invoice")
		ctx := context.Background()

		slog.Default().InfoContext(ctx, "lvl-info")
		slog.Default().WarnContext(ctx, "lvl-warn")
		slog.Default().ErrorContext(ctx, "lvl-error")

		oneLogWithBody(t, rec, "lvl-warn")
		oneLogWithBody(t, rec, "lvl-error")
		if got := logsWithBody(rec, "lvl-info"); len(got) != 0 {
			t.Errorf("info log below LOG_LEVEL=warn reached Sentry: %d records", len(got))
		}
	})
	t.Run("error", func(t *testing.T) {
		t.Setenv("LOG_LEVEL", "error")
		_, rec, _ := sentrytest.Boot(t, "invoice")
		ctx := context.Background()

		slog.Default().InfoContext(ctx, "lvl-info")
		slog.Default().WarnContext(ctx, "lvl-warn")
		slog.Default().ErrorContext(ctx, "lvl-error")

		oneLogWithBody(t, rec, "lvl-error")
		for _, body := range []string{"lvl-info", "lvl-warn"} {
			if got := logsWithBody(rec, body); len(got) != 0 {
				t.Errorf("%s below LOG_LEVEL=error reached Sentry: %d records", body, len(got))
			}
		}
	})
	t.Run("debug", func(t *testing.T) {
		t.Setenv("LOG_LEVEL", "debug")
		_, rec, _ := sentrytest.Boot(t, "invoice")

		slog.Default().DebugContext(context.Background(), "lvl-debug")

		if l := oneLogWithBody(t, rec, "lvl-debug"); l.Level != sentry.LogLevelDebug {
			t.Errorf("log level = %q, want debug", l.Level)
		}
	})
}

func TestSentryLogs_NoEmptyContextAttributes(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "invoice")
	ctx, _ := idsContext()

	slog.Default().InfoContext(ctx, "full")
	slog.Default().Info("bare")
	slog.Default().InfoContext(platform.WithRequestID(context.Background(), "req-only"), "request-only")

	full := oneLogWithBody(t, rec, "full")
	for _, k := range []string{"request_id", "tenant_id"} {
		if _, ok := full.Attributes[k]; !ok {
			t.Errorf("log with ids in its context lacks %q", k)
		}
	}
	bare := oneLogWithBody(t, rec, "bare")
	for _, k := range []string{"request_id", "tenant_id"} {
		if v, ok := bare.Attributes[k]; ok {
			t.Errorf("log without ids carries %s=%q, want no attribute", k, v.AsString())
		}
	}
	partial := oneLogWithBody(t, rec, "request-only")
	if got := logAttrString(t, partial, "request_id"); got != "req-only" {
		t.Errorf("request_id = %q, want req-only", got)
	}
	if v, ok := partial.Attributes["tenant_id"]; ok {
		t.Errorf("log without a tenant id carries tenant_id=%q, want no attribute", v.AsString())
	}
}

func TestSentryLogs_PercentSurvives(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "invoice")
	base := slog.Default()

	cases := []struct {
		name string
		log  *slog.Logger
		msg  string
	}{
		{"mid-message", base, "progress 50% done"},
		{"trailing", base, "done 100%"},
		{"verbs", base, "%s and %d and %v"},
		{"doubled", base, "already %% doubled"},
		{"derived with attrs", base.With("k", "v"), "with-attrs 50% done"},
		{"derived with group", base.WithGroup("g"), "with-group 50% done"},
	}
	for _, tc := range cases {
		tc.log.Info(tc.msg)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if l := oneLogWithBody(t, rec, tc.msg); l.Body != tc.msg {
				t.Errorf("log body = %q, want %q", l.Body, tc.msg)
			}
		})
	}
	if got := logAttrString(t, oneLogWithBody(t, rec, "with-attrs 50% done"), "k"); got != "v" {
		t.Errorf("derived logger attribute k = %q, want v", got)
	}
}

func TestSentryLogs_ErrorRecordOpensNoIssue(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "invoice")
	ctx, _ := idsContext()

	slog.Default().ErrorContext(ctx, "lookup failed", slog.Any("err", errors.New("boom")))
	slog.Default().WarnContext(ctx, "lookup slow")

	if l := oneLogWithBody(t, rec, "lookup failed"); l.Level != sentry.LogLevelError {
		t.Errorf("log level = %q, want error", l.Level)
	}
	oneLogWithBody(t, rec, "lookup slow")
	rec.None(t)
}

func TestSentryLogs_EnvironmentComesFromInitSentry(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "invoice")

	slog.Default().Info("env")

	l := oneLogWithBody(t, rec, "env")
	if got := logAttrString(t, l, "sentry.environment"); got != "production" {
		t.Errorf("sentry.environment = %q, want production", got)
	}
	if v, ok := l.Attributes["environment"]; ok {
		t.Errorf("logger's base environment=%q was forwarded, want it dropped", v.AsString())
	}

	slog.Default().Info("env-group", slog.Group("g", slog.String("environment", "kept")))
	slog.Default().WithGroup("h").Info("env-with-group", "environment", "kept-too")
	if got := logAttrString(t, oneLogWithBody(t, rec, "env-group"), "g.environment"); got != "kept" {
		t.Errorf("grouped g.environment = %q, want kept", got)
	}
	if got := logAttrString(t, oneLogWithBody(t, rec, "env-with-group"), "h.environment"); got != "kept-too" {
		t.Errorf("grouped h.environment = %q, want kept-too", got)
	}
}

func TestSentryLogs_FatalLevelIsNotForwardedAndDoesNotExit(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "invoice")
	ctx := context.Background()

	slog.Default().Log(ctx, slog.Level(12), "fatal-level")
	slog.Default().Info("after")

	oneLogWithBody(t, rec, "after")
	if got := logsWithBody(rec, "fatal-level"); len(got) != 0 {
		t.Errorf("level-12 record reached Sentry: %d records", len(got))
	}
}

func TestSentryLogs_CustomLevelStaysOnStdout(t *testing.T) {
	read := captureStdout(t)
	_, rec, _ := sentrytest.Boot(t, "invoice")
	ctx := context.Background()

	levels := []slog.Level{1, 2, 3, 5, 6, 7, 9, 10, 11, 13, 16}
	for _, lvl := range levels {
		slog.Default().Log(ctx, lvl, fmt.Sprintf("custom-%d", lvl))
	}
	slog.Default().Info("after")

	oneLogWithBody(t, rec, "after")
	out := read()
	for _, lvl := range levels {
		body := fmt.Sprintf("custom-%d", lvl)
		if got := logsWithBody(rec, body); len(got) != 0 {
			t.Errorf("level-%d record reached Sentry: %d records", lvl, len(got))
		}
		if !strings.Contains(out, `"msg":"`+body+`"`) {
			t.Errorf("stdout has no line for the level-%d record:\n%s", lvl, out)
		}
	}
}

func TestSentryLogs_StdoutKeepsEveryRecord(t *testing.T) {
	read := captureStdout(t)
	_, rec, _ := sentrytest.Boot(t, "invoice")
	ctx, _ := idsContext()

	slog.Default().InfoContext(ctx, "hello")

	out := read()
	var line map[string]any
	for _, raw := range strings.Split(out, "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(raw), &m) == nil && m["msg"] == "hello" {
			line = m
		}
	}
	if line == nil {
		t.Fatalf("stdout has no JSON line with msg=hello:\n%s", out)
	}
	want := map[string]string{"request_id": "req-logs-1", "service": "invoice", "environment": "development"}
	for k, v := range want {
		if fmt.Sprint(line[k]) != v {
			t.Errorf("stdout %s = %v, want %q", k, line[k], v)
		}
	}
	oneLogWithBody(t, rec, "hello")
}

func TestSentryLogs_CustomerTextIsScrubbed(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "invoice")
	ctx, _ := idsContext()

	slog.Default().ErrorContext(ctx, "lookup",
		slog.Any("err", fmt.Errorf("tin %q", sentrytest.MarkerTIN)),
		slog.String("url", "/v1/x?q="+sentrytest.MarkerTIN))

	l := oneLogWithBody(t, rec, "lookup")
	if l.Body != "lookup" {
		t.Errorf("log body = %q, want lookup", l.Body)
	}

	slog.Default().ErrorContext(ctx, fmt.Sprintf("import %q failed: GET /v1/x?q=%s", sentrytest.MarkerIRN, sentrytest.MarkerTIN),
		slog.Group("req", slog.String("url", "/v1/y?q="+sentrytest.MarkerAmt)))
	if got := logsWithBody(rec, `import "[redacted]" failed: GET /v1/x`); len(got) != 1 {
		t.Errorf("recorded %d logs with the scrubbed body, want 1 (bodies: %v)", len(got), logBodies(rec))
	}
	sentrytest.AssertNoLeak(t, rec.LogEvents())
}

func TestSentryLogs_NoDSNBuildsNoBridge(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "invoice")
	slog.Default().Info("with-dsn")

	t.Setenv("SENTRY_DSN", "")
	if _, err := platform.New("invoice"); err != nil {
		t.Fatalf("platform.New without a DSN: %v", err)
	}
	slog.Default().Info("no-dsn")

	oneLogWithBody(t, rec, "with-dsn")
	if got := logsWithBody(rec, "no-dsn"); len(got) != 0 {
		t.Errorf("a logger built without a DSN sent %d logs to the bound Sentry client", len(got))
	}
}
