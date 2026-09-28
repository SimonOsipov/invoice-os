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
