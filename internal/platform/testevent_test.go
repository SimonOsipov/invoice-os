package platform_test

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

const (
	testEventEnv      = "SENTRY_TEST_EVENT"
	testEventValue    = "sentry test event"
	testEventPrintTag = "sentry-test-event"
	platformModule    = "github.com/SimonOsipov/invoice-os/internal/platform"
)

// setupTestEventEnv clears the hub, restores it and slog on cleanup, and sets production's
// labels plus dsn. A nil flag leaves the variable unset.
func setupTestEventEnv(t *testing.T, dsn string, flag *string) {
	t.Helper()
	sentry.CurrentHub().BindClient(nil)
	prev := slog.Default()
	t.Cleanup(func() {
		sentry.CurrentHub().BindClient(nil)
		slog.SetDefault(prev)
	})
	for _, kv := range productionEnv(dsn) {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	t.Setenv(testEventEnv, "")
	if flag == nil {
		os.Unsetenv(testEventEnv)
	} else {
		t.Setenv(testEventEnv, *flag)
	}
}

// bootForTestEvent runs platform.New in-process against dsn.
func bootForTestEvent(t *testing.T, service, dsn string, flag *string) *platform.App {
	t.Helper()
	setupTestEventEnv(t, dsn, flag)
	app, err := platform.New(service)
	if err != nil {
		t.Fatalf("platform.New(%q): %v", service, err)
	}
	sentry.Flush(2 * time.Second)
	return app
}

func strPtr(s string) *string { return &s }

func TestTestEvent_BootSendsOneLabelledEventPerService(t *testing.T) {
	for _, service := range []string{"svc", "gateway"} {
		t.Run(service, func(t *testing.T) {
			in := newIngest(t)
			bootForTestEvent(t, service, in.dsn(), strPtr("true"))
			ev := in.wantEvents(t, 1)[0]
			assertEventLabels(t, ev, service)
			if want := []string{testEventPrintTag, service}; !slices.Equal(ev.Fingerprint, want) {
				t.Errorf("fingerprint = %q, want %q", ev.Fingerprint, want)
			}
		})
	}
}

func TestTestEvent_CarriesAReadableStack(t *testing.T) {
	in := newIngest(t)
	bootForTestEvent(t, "svc", in.dsn(), strPtr("true"))
	ev := in.wantEvents(t, 1)[0]
	if len(ev.Exception) == 0 {
		t.Fatalf("event has no exception: %+v", ev)
	}
	exc := ev.Exception[0]
	if exc.Value != testEventValue {
		t.Errorf("exception value = %q, want %q", exc.Value, testEventValue)
	}
	if exc.Stacktrace == nil || len(exc.Stacktrace.Frames) == 0 {
		t.Fatalf("exception has no stack frames: %+v", exc)
	}
	// Exact match: the platform_test frames of this test must not satisfy it.
	found := false
	for _, f := range exc.Stacktrace.Frames {
		found = found || f.Module == platformModule
	}
	if !found {
		t.Errorf("no frame in module %q: %+v", platformModule, exc.Stacktrace.Frames)
	}
}

func TestTestEvent_OnlyExactTrueFires(t *testing.T) {
	in := newIngest(t)
	for _, v := range []*string{nil, strPtr(""), strPtr("false"), strPtr("1"), strPtr("TRUE"), strPtr(" true")} {
		name := "unset"
		if v != nil {
			name = *v
		}
		bootForTestEvent(t, "svc", in.dsn(), v)
		if got := len(in.requests()); got != 0 {
			t.Fatalf("value %q: ingest received %d requests, want 0", name, got)
		}
	}
	bootForTestEvent(t, "svc", in.dsn(), strPtr("true"))
	in.wantEvents(t, 1)
}

func TestTestEvent_NoDSNSendsNothing(t *testing.T) {
	in := newIngest(t)
	app := bootForTestEvent(t, "svc", "", strPtr("true"))
	if reqs := in.requests(); len(reqs) != 0 {
		t.Errorf("no DSN: ingest received %v, want no requests", reqs)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if body := rec.Body.String(); !strings.Contains(body, `"sentry":"off"`) {
		t.Errorf("/healthz body = %s, want sentry off", body)
	}

	bootForTestEvent(t, "svc", in.dsn(), strPtr("true"))
	in.wantEvents(t, 1)
}

func TestTestEvent_OncePerBootNotPerRequest(t *testing.T) {
	in := newIngest(t)
	app := bootForTestEvent(t, "svc", in.dsn(), strPtr("true"))
	h := app.Handler()
	for _, path := range []string{"/healthz", "/healthz", "/healthz", "/no-such-path"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	sentry.Flush(2 * time.Second)
	evs := in.wantEvents(t, 1)
	if want := []string{testEventPrintTag, "svc"}; !slices.Equal(evs[0].Fingerprint, want) {
		t.Errorf("fingerprint = %q, want %q", evs[0].Fingerprint, want)
	}
}

func TestTestEvent_FingerprintDoesNotStick(t *testing.T) {
	in := newIngest(t)
	bootForTestEvent(t, "svc", in.dsn(), strPtr("true"))
	platform.CaptureError(t.Context(), errors.New("later"))
	sentry.Flush(2 * time.Second)
	evs := in.wantEvents(t, 2)

	var later *wireEvent
	tagged := 0
	for i := range evs {
		if len(evs[i].Exception) > 0 && evs[i].Exception[0].Value == "later" {
			later = &evs[i]
		}
		if len(evs[i].Fingerprint) > 0 && evs[i].Fingerprint[0] == testEventPrintTag {
			tagged++
		}
	}
	if tagged != 1 {
		t.Errorf("%d events carry the test fingerprint, want 1", tagged)
	}
	if later == nil {
		t.Fatalf("no event for the later error: %+v", evs)
	}
	if len(later.Fingerprint) > 0 && later.Fingerprint[0] == testEventPrintTag {
		t.Errorf("later event fingerprint = %q, want it not to start with %q", later.Fingerprint, testEventPrintTag)
	}
}

func TestTestEvent_CarriesNoRequestData(t *testing.T) {
	in := newIngest(t)
	bootForTestEvent(t, "svc", in.dsn(), strPtr("true"))
	ev := in.wantEvents(t, 1)[0]
	if s := strings.TrimSpace(string(ev.Request)); s != "" && s != "null" && s != "{}" {
		t.Errorf("test event carries request data: %s", s)
	}
	for _, k := range []string{"request_id", "tenant_id"} {
		if v, ok := ev.Tags[k]; ok {
			t.Errorf("test event carries tag %s=%q", k, v)
		}
	}
}

func TestTestEvent_BadDSNFailsBootAndSendsNothing(t *testing.T) {
	in := newIngest(t)
	setupTestEventEnv(t, "not-a-dsn", strPtr("true"))
	app, err := platform.New("svc")
	if err == nil {
		t.Fatalf("platform.New with an unparseable DSN returned no error (app %v)", app)
	}
	sentry.Flush(2 * time.Second)
	if reqs := in.requests(); len(reqs) != 0 {
		t.Errorf("failed boot sent %v, want no requests", reqs)
	}
	if s := platform.SentryState(); s != "off" {
		t.Errorf("SentryState after a failed init = %q, want off", s)
	}
}

// A client bound earlier with a blank DSN must not receive the test event.
func TestTestEvent_BlankDSNIgnoresABoundClient(t *testing.T) {
	setupTestEventEnv(t, "", strPtr("true"))
	rec := &sentrytest.Recorder{}
	client, err := sentry.NewClient(sentry.ClientOptions{Transport: rec})
	if err != nil {
		t.Fatal(err)
	}
	sentry.CurrentHub().BindClient(client)
	if _, err := platform.New("svc"); err != nil {
		t.Fatalf("platform.New: %v", err)
	}
	if got := rec.Events(); len(got) != 0 {
		t.Fatalf("blank DSN: bound client received %d events, want 0", len(got))
	}
	// Control: the recorder is wired, so the zero above is not vacuous.
	sentry.CaptureMessage("control")
	if got := rec.Events(); len(got) != 1 {
		t.Fatalf("control: recorder holds %d events, want 1", len(got))
	}
}
