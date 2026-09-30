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
}

func TestSentryLogs_PercentSurvives(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "invoice")

	slog.Default().Info("progress 50% done")

	if l := oneLogWithBody(t, rec, "progress 50% done"); l.Body != "progress 50% done" {
		t.Errorf("log body = %q", l.Body)
	}
}

func TestSentryLogs_ErrorRecordOpensNoIssue(t *testing.T) {
	_, rec, _ := sentrytest.Boot(t, "invoice")
	ctx, _ := idsContext()

	slog.Default().ErrorContext(ctx, "lookup failed", slog.Any("err", errors.New("boom")))

	if l := oneLogWithBody(t, rec, "lookup failed"); l.Level != sentry.LogLevelError {
		t.Errorf("log level = %q, want error", l.Level)
	}
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

	slog.Default().Log(ctx, slog.Level(2), "custom")
	slog.Default().Info("after")

	oneLogWithBody(t, rec, "after")
	if got := logsWithBody(rec, "custom"); len(got) != 0 {
		t.Errorf("custom-level record reached Sentry: %d records", len(got))
	}
	if out := read(); !strings.Contains(out, `"msg":"custom"`) {
		t.Errorf("stdout has no custom-level line:\n%s", out)
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
	sentrytest.AssertNoLeak(t, rec.LogEvents())
}
