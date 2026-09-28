package platform

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"
)

// newHubLogger returns a Sentry logger bound to the hub filteredClient set up.
func newHubLogger() sentry.Logger {
	return sentry.NewLogger(sentry.SetHubOnContext(context.Background(), sentry.CurrentHub()))
}

// recordedLogs flushes the log batch and returns every log record it carried.
func recordedLogs(t *testing.T, mt *mockTransport) []sentry.Log {
	t.Helper()
	if !sentry.CurrentHub().Client().Flush(time.Second) {
		t.Fatal("flush timed out")
	}
	var out []sentry.Log
	for _, e := range eventsOfType(mt, "log") {
		out = append(out, e.Logs...)
	}
	if len(out) == 0 {
		t.Fatalf("no log record recorded (events: %d)", len(mt.captured()))
	}
	return out
}

func oneLog(t *testing.T, mt *mockTransport) sentry.Log {
	t.Helper()
	logs := recordedLogs(t, mt)
	if len(logs) != 1 {
		t.Fatalf("recorded %d log records, want 1", len(logs))
	}
	return logs[0]
}

func logAttr(t *testing.T, l sentry.Log, key string) attribute.Value {
	t.Helper()
	v, ok := l.Attributes[key]
	if !ok {
		t.Fatalf("log attribute %q missing (have %v)", key, attrKeys(l))
	}
	return v
}

func attrKeys(l sentry.Log) []string {
	keys := make([]string, 0, len(l.Attributes))
	for k := range l.Attributes {
		keys = append(keys, k)
	}
	return keys
}

func TestSentryLogFilter_NoQueryStringLeaves(t *testing.T) {
	mt := filteredClient(t, false)
	newHubLogger().Info().
		String("url", "https://h/v1/invoices?q="+markerTIN+"#"+markerIRN).
		String("ref", "https://h/v1/invoices#"+markerIRN).
		String("http.query", "q="+markerTIN).
		String("http.fragment", markerIRN).
		StringSlice("urls", []string{"/v1/invoices?q=" + markerTIN, "/v1/invoices#" + markerIRN}).
		Emit("GET /v1/invoices?q=" + markerTIN + " from /v1/invoices#" + markerIRN)

	l := oneLog(t, mt)
	for _, k := range []string{"http.query", "http.fragment"} {
		if _, ok := l.Attributes[k]; ok {
			t.Errorf("log attribute %q arrived, want it deleted", k)
		}
	}
	if want := "GET /v1/invoices from /v1/invoices"; l.Body != want {
		t.Errorf("log body = %q, want %q", l.Body, want)
	}
	for _, k := range []string{"url", "ref"} {
		if got := logAttr(t, l, k).AsString(); got != "https://h/v1/invoices" {
			t.Errorf("log attribute %s = %q, want https://h/v1/invoices", k, got)
		}
	}
	if got, want := logAttr(t, l, "urls").AsStringSlice(), []string{"/v1/invoices", "/v1/invoices"}; !reflect.DeepEqual(got, want) {
		t.Errorf("urls attribute = %q, want %q", got, want)
	}
	assertNoLeak(t, mt.captured())
}

func TestSentryLogFilter_ErrorTextLosesQuotedValues(t *testing.T) {
	t.Run("attributes", func(t *testing.T) {
		mt := filteredClient(t, false)
		newHubLogger().Error().
			String("err", `issue_date "`+markerIRN+`"`).
			String("cause", "docling: /v1/read returned 500: "+markerTIN).
			StringSlice("rows", []string{`total "` + markerAmt + `"`, "upstream returned 502: " + markerCred}).
			Emit("search failed")

		l := oneLog(t, mt)
		if l.Body != "search failed" {
			t.Errorf("log body = %q, want search failed", l.Body)
		}
		if got, want := logAttr(t, l, "err").AsString(), `issue_date "[redacted]"`; got != want {
			t.Errorf("err attribute = %q, want %q", got, want)
		}
		if got, want := logAttr(t, l, "cause").AsString(), "docling: /v1/read returned 500: [redacted]"; got != want {
			t.Errorf("cause attribute = %q, want %q", got, want)
		}
		rows := logAttr(t, l, "rows")
		if rows.Type() != attribute.STRINGSLICE {
			t.Fatalf("rows attribute type = %s, want stringslice", rows.Type())
		}
		if got, want := rows.AsStringSlice(), []string{`total "[redacted]"`, "upstream returned 502: [redacted]"}; !reflect.DeepEqual(got, want) {
			t.Errorf("rows attribute = %q, want %q", got, want)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("body", func(t *testing.T) {
		mt := filteredClient(t, false)
		newHubLogger().Error().Emit(`import "` + markerIRN + `" failed: docling: /v1/read returned 502: ` + markerTIN)

		l := oneLog(t, mt)
		if want := `import "[redacted]" failed: docling: /v1/read returned 502: [redacted]`; l.Body != want {
			t.Errorf("log body = %q, want %q", l.Body, want)
		}
		assertNoLeak(t, mt.captured())
	})

	t.Run("logger and scope attributes", func(t *testing.T) {
		mt := filteredClient(t, false)
		hub := sentry.CurrentHub().Clone()
		hub.Scope().SetAttributes(attribute.String("route", "/v1/invoices?q="+markerTIN))
		logger := sentry.NewLogger(sentry.SetHubOnContext(context.Background(), hub))
		logger.SetAttributes(attribute.String("err", `issue_date "`+markerIRN+`"`))
		logger.Info().Emit("search failed")

		l := oneLog(t, mt)
		if got := logAttr(t, l, "route").AsString(); got != "/v1/invoices" {
			t.Errorf("route attribute = %q, want /v1/invoices", got)
		}
		if got, want := logAttr(t, l, "err").AsString(), `issue_date "[redacted]"`; got != want {
			t.Errorf("err attribute = %q, want %q", got, want)
		}
		assertNoLeak(t, mt.captured())
	})
}

func TestSentryLogFilter_DropsMessageParameters(t *testing.T) {
	mt := filteredClient(t, false)
	// The SDK writes each argument to a parameter attribute raw, outside any quote.
	newHubLogger().Warn().Emitf("lookup %q failed after %d tries", markerTIN, 3)

	l := oneLog(t, mt)
	for k := range l.Attributes {
		if strings.HasPrefix(k, "sentry.message.parameters.") {
			t.Errorf("log attribute %q arrived, want every sentry.message.parameters.* deleted", k)
		}
	}
	if want := `lookup "[redacted]" failed after 3 tries`; l.Body != want {
		t.Errorf("log body = %q, want %q", l.Body, want)
	}
	if got, want := logAttr(t, l, "sentry.message.template").AsString(), "lookup %q failed after %d tries"; got != want {
		t.Errorf("sentry.message.template = %q, want %q", got, want)
	}
	assertNoLeak(t, mt.captured())
}

func TestSentryLogFilter_KeepsDefaultAttributes(t *testing.T) {
	mt := filteredClient(t, false)
	newHubLogger().Info().Emit("worker started")

	l := oneLog(t, mt)
	if l.Body != "worker started" {
		t.Errorf("log body = %q, want worker started", l.Body)
	}
	if got := logAttr(t, l, "sentry.environment").AsString(); got != "test" {
		t.Errorf("sentry.environment = %q, want test", got)
	}
	if got := logAttr(t, l, "sentry.server.address").AsString(); got != "svc" {
		t.Errorf("sentry.server.address = %q, want svc", got)
	}
	if got := logAttr(t, l, "sentry.sdk.name").AsString(); got != "sentry.go" {
		t.Errorf("sentry.sdk.name = %q, want sentry.go", got)
	}
	if got := logAttr(t, l, "sentry.sdk.version").AsString(); got != sentry.SDKVersion {
		t.Errorf("sentry.sdk.version = %q, want %s", got, sentry.SDKVersion)
	}
	assertNoLeak(t, mt.captured())
}

func TestSentryLogFilter_NoAttributes(t *testing.T) {
	got := scrubLog(&sentry.Log{Body: "worker started"})
	if got == nil {
		t.Fatal("scrubLog dropped a log with no attributes")
	}
	if got.Body != "worker started" {
		t.Errorf("log body = %q, want worker started", got.Body)
	}
}

// Accepted residual: a bare %s argument lands in the body unquoted, so ScrubText cannot see it.
func TestSentryLogFilter_UnquotedArgumentStaysInBody(t *testing.T) {
	mt := filteredClient(t, false)
	newHubLogger().Warn().Emitf("lookup %s failed", markerTIN)

	l := oneLog(t, mt)
	if want := "lookup " + markerTIN + " failed"; l.Body != want {
		t.Errorf("log body = %q, want %q", l.Body, want)
	}
	if _, ok := l.Attributes["sentry.message.parameters.0"]; ok {
		t.Error("sentry.message.parameters.0 arrived, want it deleted")
	}
	if got := logAttr(t, l, "sentry.message.template").AsString(); got != "lookup %s failed" {
		t.Errorf("sentry.message.template = %q, want lookup %%s failed", got)
	}
}

// The deleted set is the key prefix "sentry.message.parameters." with its dot; a lookalike key is an ordinary string.
func TestSentryLogFilter_ParameterLookalikeKeyIsScrubbedNotDropped(t *testing.T) {
	mt := filteredClient(t, false)
	newHubLogger().Info().
		String("sentry.message.parameters_note", `note "`+markerIRN+`"`).
		String("sentry.message.parameters", `note "`+markerIRN+`"`).
		String("sentry.message.parameters.extra", markerTIN).
		Emit("worker started")

	l := oneLog(t, mt)
	for _, k := range []string{"sentry.message.parameters_note", "sentry.message.parameters"} {
		if got, want := logAttr(t, l, k).AsString(), `note "[redacted]"`; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if _, ok := l.Attributes["sentry.message.parameters.extra"]; ok {
		t.Error("sentry.message.parameters.extra arrived, want it deleted")
	}
	assertNoLeak(t, mt.captured())
}

// Numbers pass untouched: numeric attributes carry sizes, costs and latencies, never an invoice amount.
func TestSentryLogFilter_LeavesNonStringAttributes(t *testing.T) {
	mt := filteredClient(t, false)
	newHubLogger().Info().
		Int("latency_ms", 1234).
		Int64Slice("rows", []int64{7, 8}).
		Float64("cost", 0.25).
		Bool("retry", true).
		Emit("worker started")

	l := oneLog(t, mt)
	if v := logAttr(t, l, "latency_ms"); v.Type() != attribute.INT64 || v.AsInt64() != 1234 {
		t.Errorf("latency_ms = %s %v, want int64 1234", v.Type(), v.AsInterface())
	}
	if v := logAttr(t, l, "rows"); v.Type() != attribute.INT64SLICE || !reflect.DeepEqual(v.AsInt64Slice(), []int64{7, 8}) {
		t.Errorf("rows = %s %v, want int64slice [7 8]", v.Type(), v.AsInterface())
	}
	if v := logAttr(t, l, "cost"); v.Type() != attribute.FLOAT64 || v.AsFloat64() != 0.25 {
		t.Errorf("cost = %s %v, want float64 0.25", v.Type(), v.AsInterface())
	}
	if v := logAttr(t, l, "retry"); v.Type() != attribute.BOOL || !v.AsBool() {
		t.Errorf("retry = %s %v, want bool true", v.Type(), v.AsInterface())
	}
}

func TestSentryLogFilter_EmptyStringSlice(t *testing.T) {
	mt := filteredClient(t, false)
	newHubLogger().Info().StringSlice("rows", []string{}).Emit("worker started")

	l := oneLog(t, mt)
	v := logAttr(t, l, "rows")
	if v.Type() != attribute.STRINGSLICE {
		t.Fatalf("rows attribute type = %s, want stringslice", v.Type())
	}
	if got := v.AsStringSlice(); len(got) != 0 {
		t.Errorf("rows attribute = %q, want empty", got)
	}
}
