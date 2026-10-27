package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"
)

func TestContextHandlerInjectsIDs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(&contextHandler{Handler: slog.NewJSONHandler(&buf, nil)})
	ctx := WithTenantID(WithRequestID(context.Background(), "req-1"), "tnt-2")
	logger.InfoContext(ctx, "hello")

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("log line is not valid JSON: %v", err)
	}
	if m["request_id"] != "req-1" {
		t.Errorf("request_id = %v, want req-1", m["request_id"])
	}
	if m["tenant_id"] != "tnt-2" {
		t.Errorf("tenant_id = %v, want tnt-2", m["tenant_id"])
	}
	if m["msg"] != "hello" {
		t.Errorf("msg = %v, want hello", m["msg"])
	}
}

func TestContextHandlerOmitsUnsetIDs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(&contextHandler{Handler: slog.NewJSONHandler(&buf, nil)})
	logger.InfoContext(context.Background(), "hello")

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("log line is not valid JSON: %v", err)
	}
	if _, ok := m["request_id"]; ok {
		t.Error("request_id should be absent when unset")
	}
	if _, ok := m["tenant_id"]; ok {
		t.Error("tenant_id should be absent when unset")
	}
}

func TestContextHandlerPreservesWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(&contextHandler{Handler: slog.NewJSONHandler(&buf, nil)}).With(slog.String("service", "svc"))
	logger.InfoContext(WithRequestID(context.Background(), "r1"), "m")

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if m["service"] != "svc" {
		t.Errorf("service = %v, want svc (With attr lost through wrapper)", m["service"])
	}
	if m["request_id"] != "r1" {
		t.Errorf("request_id = %v, want r1 (context enrichment lost through wrapper)", m["request_id"])
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
		"":      slog.LevelInfo,
		"bogus": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSentryLogBridge_DropsUserDotAttributes(t *testing.T) {
	mt := filteredClient(t, false)
	const u = "3f2b8c1e-0000-4000-8000-000000000001"
	slog.New(sentryLogHandler(slog.LevelDebug)).WarnContext(context.Background(), "probe",
		slog.String("user.id", u), slog.String("user_id", u))
	sentry.Flush(time.Second)

	var attrs map[string]attribute.Value
	for _, e := range mt.captured() {
		if len(e.Logs) > 0 {
			attrs = e.Logs[0].Attributes
		}
	}
	if attrs == nil {
		t.Fatal("no log reached the transport")
	}
	if _, ok := attrs["user.id"]; ok {
		t.Error("user.id reached Sentry Logs")
	}
	if got := attrs["user_id"].AsString(); got != u {
		t.Errorf("user_id = %q, want %q (the control for the rename)", got, u)
	}
}
