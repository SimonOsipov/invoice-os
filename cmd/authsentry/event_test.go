package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

const testUUID = "0f8fad5b-d9cb-469f-a165-70867728950e"

func jl(m map[string]any) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

func completed(status int, extra map[string]any) []byte {
	m := map[string]any{
		"level": "error", "msg": "request completed", "component": "api", "method": "POST",
		"path": "/token", "status": status, "request_id": "rid-1", "grant_type": "password",
		"error": "boom",
	}
	for k, v := range extra {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return jl(m)
}

// authBoot binds an auth client onto a recorder, as run does in production.
func authBoot(t *testing.T) *sentrytest.Recorder {
	t.Helper()
	sentry.CurrentHub().BindClient(nil)
	rec := &sentrytest.Recorder{}
	cfg := platform.Config{Service: platform.AuthService, SentryEnvironment: "production", Release: "r", SentryDSN: "https://public@example.com/1"}
	if err := platform.InitSentry(cfg, rec); err != nil {
		t.Fatalf("InitSentry: %v", err)
	}
	t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })
	return rec
}

// wire classifies lines, sends the events through the auth filter and returns what the recorder holds.
func wire(t *testing.T, lines ...[]byte) []*sentry.Event {
	t.Helper()
	rec := authBoot(t)
	c := newClassifier()
	for _, l := range lines {
		if ev := c.line(l); ev != nil {
			sentry.CaptureEvent(ev)
		}
	}
	return rec.Events()
}

func wireJSON(t *testing.T, lines ...[]byte) string {
	t.Helper()
	events := wire(t, lines...)
	if len(events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(events))
	}
	sentrytest.AssertNoLeak(t, events)
	raw, _ := json.Marshal(events[0])
	return string(raw)
}

func TestClassify_A5xxRequestOpensOneEvent(t *testing.T) {
	for _, status := range []int{500, 502, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			ev := newClassifier().line(completed(status, map[string]any{"error_code": "unexpected_failure"}))
			if ev == nil {
				t.Fatal("no event")
			}
			ex := ev.Exception[0]
			if want := fmt.Sprintf("GoTrue %d", status); ex.Type != want || ex.Value != "boom" {
				t.Errorf("exception = %q/%q, want %q/boom", ex.Type, ex.Value, want)
			}
			want := map[string]string{
				"path": "/token", "method": "POST", "status": fmt.Sprint(status),
				"request_id": "rid-1", "error_code": "unexpected_failure", "grant_type": "password",
			}
			for k, v := range want {
				if ev.Tags[k] != v {
					t.Errorf("tag %s = %q, want %q", k, ev.Tags[k], v)
				}
			}
		})
	}
	ev := newClassifier().line(completed(500, map[string]any{"grant_type": nil, "error_code": nil}))
	if _, ok := ev.Tags["grant_type"]; ok {
		t.Error("grant_type tag set without a grant_type")
	}
	if _, ok := ev.Tags["error_code"]; ok {
		t.Error("error_code tag set without an error_code")
	}
}

func TestClassify_ARefusalOpensNone(t *testing.T) {
	c := newClassifier()
	lines := map[string][]byte{}
	for _, s := range []int{400, 401, 403, 404, 422, 429, 200} {
		lines[fmt.Sprint(s)] = completed(s, nil)
	}
	lines["422 user_already_exists"] = []byte(`{"level":"warning","msg":"request completed","path":"/signup","status":422,"error_code":"user_already_exists","method":"POST","request_id":"a"}`)
	lines["403 otp_expired"] = []byte(`{"level":"warning","msg":"request completed","path":"/verify","status":403,"error_code":"otp_expired","method":"GET","request_id":"b"}`)
	lines["200 signup"] = []byte(`{"level":"info","msg":"request completed","path":"/signup","status":200,"method":"POST","request_id":"c"}`)
	lines["error level, no status"] = []byte(`{"level":"error","msg":"apiworker: x"}`)
	for name, l := range lines {
		if ev := c.line(l); ev != nil {
			t.Errorf("%s opened an event", name)
		}
	}
	if c.line(completed(500, nil)) == nil {
		t.Error("control: the 500 line opened no event")
	}
}

func TestClassify_Status499And500Boundary(t *testing.T) {
	c := newClassifier()
	if c.line(completed(499, nil)) != nil {
		t.Error("499 opened an event")
	}
	if c.line(completed(500, nil)) == nil {
		t.Error("500 opened no event")
	}
}

func TestClassify_APanicIsOneEventPerRequest(t *testing.T) {
	c := newClassifier()
	panicked := jl(map[string]any{"level": "error", "msg": "request panicked", "request_id": "A", "path": "/token", "method": "POST", "panic": "nil map", "stack": "goroutine 1"})
	ev := c.line(panicked)
	if ev == nil || ev.Exception[0].Type != "GoTrue panic" || ev.Exception[0].Value != "nil map" {
		t.Fatalf("panic event = %+v", ev)
	}
	if c.line(completed(500, map[string]any{"request_id": "A"})) != nil {
		t.Error("the 500 line of the panicked request opened a second event")
	}
	if c.line(completed(500, map[string]any{"request_id": "B"})) == nil {
		t.Error("a 500 line of another request opened no event")
	}
}

func TestClassify_APanicIsOneEventEvenWhenSuppressed(t *testing.T) {
	c := newClassifier()
	panicked := func(rid string) []byte {
		return jl(map[string]any{"level": "error", "msg": "request panicked", "request_id": rid, "path": "/token", "panic": "x"})
	}
	if c.line(panicked("A")) == nil {
		t.Fatal("first panic opened no event")
	}
	if c.line(panicked("B")) != nil {
		t.Fatal("second panic with the same fingerprint was not suppressed")
	}
	if c.line(completed(500, map[string]any{"request_id": "B"})) != nil {
		t.Error("the 500 line of a suppressed panic opened an event")
	}
}

func TestClassify_RequestMemoIsBounded(t *testing.T) {
	c := newClassifier()
	c.line(jl(map[string]any{"msg": "request panicked", "request_id": "first", "path": "/a", "panic": "x"}))
	for i := 0; i < memoSize; i++ {
		c.line(jl(map[string]any{"msg": "request panicked", "request_id": fmt.Sprintf("r%d", i), "path": fmt.Sprintf("/p%d", i), "panic": "x"}))
	}
	if len(c.memo) != memoSize {
		t.Errorf("memo holds %d ids, want %d", len(c.memo), memoSize)
	}
	if c.line(completed(500, map[string]any{"request_id": "first", "path": "/other"})) == nil {
		t.Error("the evicted request id still suppresses its 500 line")
	}
}

func TestClassify_BootFailureCarriesOnlyTheLabel(t *testing.T) {
	c := newClassifier()
	c.line(jl(map[string]any{"level": "fatal", "msg": "Failed to load configuration: " + sentrytest.MarkerCred}))
	ev := c.exit(1, nil)
	if ev == nil {
		t.Fatal("no event")
	}
	if got, want := ev.Exception[0].Value, "GoTrue exited with code 1: Failed to load configuration"; got != want {
		t.Errorf("value = %q, want %q", got, want)
	}
	if ev.Level != sentry.LevelFatal || ev.Tags["auth.phase"] != "boot" {
		t.Errorf("level %q phase %q, want fatal/boot", ev.Level, ev.Tags["auth.phase"])
	}
	raw, _ := json.Marshal(ev)
	if strings.Contains(string(raw), sentrytest.MarkerCred) {
		t.Errorf("event carries the marker: %s", raw)
	}
}

func TestClassify_ACrashAfterReadyIsRunPhase(t *testing.T) {
	c := newClassifier()
	c.line(jl(map[string]any{"level": "info", "msg": "GoTrue API started on: :8080"}))
	c.line([]byte("panic: boom " + sentrytest.MarkerCred))
	ev := c.exit(2, nil)
	if ev.Tags["auth.phase"] != "run" {
		t.Errorf("phase = %q, want run", ev.Tags["auth.phase"])
	}
	if v := ev.Exception[0].Value; !strings.HasSuffix(v, ": panic") || strings.Contains(v, sentrytest.MarkerCred) {
		t.Errorf("value = %q, want it to end with \": panic\" and hold no marker", v)
	}
}

func TestClassify_AChildThatDidNotStart(t *testing.T) {
	ev := newClassifier().exit(127, exec.ErrNotFound)
	if ev == nil || ev.Exception[0].Value != "GoTrue did not start" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestClassify_ACleanExitOpensNone(t *testing.T) {
	if newClassifier().exit(0, nil) != nil {
		t.Error("exit code 0 opened an event")
	}
}

func TestClassify_AJSONLabelBeatsTheFirstRawLine(t *testing.T) {
	c := newClassifier()
	c.line([]byte("warming up: x"))
	c.line(jl(map[string]any{"level": "fatal", "msg": "bad config: x"}))
	if v := c.exit(1, nil).Exception[0].Value; v != "GoTrue exited with code 1: bad config" {
		t.Errorf("value = %q", v)
	}
}

func TestAuthEvent_CarriesNoClientIP(t *testing.T) {
	raw := wireJSON(t, completed(500, map[string]any{
		"remote_addr":      sentrytest.MarkerCred,
		"auth_audit_event": map[string]any{"ip_address": sentrytest.MarkerCred},
	}))
	if strings.Contains(raw, "ip_address") || strings.Contains(raw, "remote_addr") {
		t.Errorf("event carries an IP key: %s", raw)
	}
}

func TestAuthEvent_CarriesNoToken(t *testing.T) {
	jwt := "eyJ" + sentrytest.MarkerCred + ".eyJx.sig"
	raw := wireJSON(t, completed(500, map[string]any{
		"error": "bad " + jwt, "access_token": sentrytest.MarkerCred, "refresh_token": sentrytest.MarkerCred,
	}))
	if !strings.Contains(raw, "[redacted]") {
		t.Errorf("JWT not redacted: %s", raw)
	}
}

func TestAuthEvent_CarriesNoPassword(t *testing.T) {
	wireJSON(t, completed(500, map[string]any{"password": sentrytest.MarkerCred}))
}

func TestAuthEvent_CarriesNoCookie(t *testing.T) {
	events := wire(t, completed(500, map[string]any{"cookie": sentrytest.MarkerCred}))
	sentrytest.AssertNoLeak(t, events)
	if events[0].Request != nil {
		t.Errorf("event has a request: %+v", events[0].Request)
	}
}

func TestAuthEvent_CarriesNoQueryString(t *testing.T) {
	events := wire(t, completed(500, map[string]any{
		"path": "/verify?token=" + sentrytest.MarkerCred, "referer": "https://x/?q=" + sentrytest.MarkerTIN,
	}))
	sentrytest.AssertNoLeak(t, events)
	if got := events[0].Tags["path"]; got != "/verify" {
		t.Errorf("path tag = %q, want /verify", got)
	}
}

func TestAuthEvent_CarriesNoRequestBody(t *testing.T) {
	events := wire(t, completed(500, map[string]any{"body": `{"email":"` + sentrytest.MarkerAmt + `"}`}))
	sentrytest.AssertNoLeak(t, events)
	if events[0].Request != nil {
		t.Errorf("event has a request: %+v", events[0].Request)
	}
}

func TestAuthEvent_CarriesNoUserIdOrName(t *testing.T) {
	events := wire(t, completed(500, map[string]any{
		"user_id": sentrytest.MarkerCred,
		"auth_event": map[string]any{
			"actor_id": sentrytest.MarkerTIN, "actor_name": sentrytest.MarkerIRN,
		},
		"path": "/admin/users/" + testUUID,
	}))
	sentrytest.AssertNoLeak(t, events)
	raw, _ := json.Marshal(events[0])
	if strings.Contains(string(raw), testUUID) {
		t.Errorf("event carries the UUID: %s", raw)
	}
	if got := events[0].Tags["path"]; got != "/admin/users/{id}" {
		t.Errorf("path tag = %q, want /admin/users/{id}", got)
	}
}

func TestAuthEvent_ErrorTextCarriesNoIdOrDetail(t *testing.T) {
	raw := wireJSON(t, completed(500, map[string]any{
		"error": `ERROR: duplicate key value violates unique constraint "users_email_key" (SQLSTATE 23505): user ` + testUUID + ` org aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee phone 2348000000000`,
	}))
	for _, bad := range []string{testUUID, "aaaaaaaa-bbbb-cccc", "2348000000000", "users_email_key"} {
		if strings.Contains(raw, bad) {
			t.Errorf("event carries %q: %s", bad, raw)
		}
	}
	for _, want := range []string{"SQLSTATE 23505", "[redacted]", "{id}"} {
		if !strings.Contains(raw, want) {
			t.Errorf("event lacks %q: %s", want, raw)
		}
	}
}

func TestAuthEvent_ATagValueFromTheClientIsDropped(t *testing.T) {
	ev := newClassifier().line(completed(500, map[string]any{"grant_type": "pass word " + sentrytest.MarkerCred, "request_id": "a b"}))
	raw, _ := json.Marshal(ev)
	if strings.Contains(string(raw), sentrytest.MarkerCred) {
		t.Errorf("event carries a free-text tag: %s", raw)
	}
	if _, ok := ev.Tags["request_id"]; ok {
		t.Error("a request id with a space became a tag")
	}
}

func emailEvent(m map[string]any) []byte { return completed(500, m) }

func TestAuthEvent_CarriesTheAccountEmail(t *testing.T) {
	events := wire(t, emailEvent(map[string]any{"auth_event": map[string]any{"actor_username": "ok@example.com"}}))
	if got := events[0].User.Email; got != "ok@example.com" {
		t.Errorf("User.Email = %q", got)
	}
	if u := events[0].User; u.ID != "" || u.IPAddress != "" || u.Username != "" || u.Name != "" {
		t.Errorf("user carries more than the email: %+v", u)
	}
}

func TestAuthEvent_TakesTheEmailFromAnEarlierLine(t *testing.T) {
	audit := jl(map[string]any{
		"level": "info", "msg": "audit_event",
		"auth_audit_event": map[string]any{"actor_username": "ok@example.com", "request_id": "A"},
	})
	events := wire(t, audit, completed(500, map[string]any{"request_id": "A"}), completed(500, map[string]any{"request_id": "B", "path": "/other"}))
	if len(events) != 2 {
		t.Fatalf("recorded %d events, want 2", len(events))
	}
	if events[0].User.Email != "ok@example.com" {
		t.Errorf("request A email = %q", events[0].User.Email)
	}
	if !events[1].User.IsEmpty() {
		t.Errorf("request B user = %+v, want empty", events[1].User)
	}
}

func TestAuthEvent_APhoneIsNoEmail(t *testing.T) {
	events := wire(t, emailEvent(map[string]any{"auth_event": map[string]any{"actor_username": "+2348000000000"}}))
	if !events[0].User.IsEmpty() {
		t.Errorf("user = %+v, want empty", events[0].User)
	}
}

func TestAuthEvent_SignupMailFailureCarriesTheEmail(t *testing.T) {
	events := wire(t, emailEvent(map[string]any{
		"path": "/signup", "error": "Error sending confirmation email",
		"auth_event": map[string]any{"actor_username": "ok@example.com"},
	}))
	if events[0].User.Email != "ok@example.com" {
		t.Errorf("User.Email = %q", events[0].User.Email)
	}
}

func TestAuthEvent_AnEarlyFailureCarriesNoEmail(t *testing.T) {
	events := wire(t, emailEvent(map[string]any{"path": "/signup", "error": "Database error finding user"}))
	if !events[0].User.IsEmpty() {
		t.Errorf("user = %+v, want empty", events[0].User)
	}
}

func TestClassify_MalformedLinesAreIgnored(t *testing.T) {
	c := newClassifier()
	for _, l := range []string{"panic: x", `{"level":`, `[]`, `"s"`, ``, `null`, `{"msg":5,"status":"x"}`} {
		if ev := c.line([]byte(l)); ev != nil {
			t.Errorf("line %q opened an event", l)
		}
	}
}

func fp(t *testing.T, line []byte) string {
	t.Helper()
	ev := newClassifier().line(line)
	if ev == nil {
		t.Fatal("no event")
	}
	return strings.Join(ev.Fingerprint, "|")
}

func TestFingerprint_SameFailureJoinsOneIssue(t *testing.T) {
	a := fp(t, completed(500, map[string]any{"request_id": "r1", "error": "user " + testUUID + " id 123456789 failed"}))
	b := fp(t, completed(500, map[string]any{"request_id": "r2", "error": "user 11111111-2222-3333-4444-555555555555 id 987654321 failed"}))
	if a != b {
		t.Errorf("fingerprints differ:\n%s\n%s", a, b)
	}
}

func TestFingerprint_DifferentPathsSplit(t *testing.T) {
	if fp(t, completed(500, nil)) == fp(t, completed(500, map[string]any{"path": "/signup"})) {
		t.Error("two paths share a fingerprint")
	}
}

func TestFingerprint_DifferentCausesSplit(t *testing.T) {
	a := fp(t, completed(500, map[string]any{"path": "/signup", "error": "Error sending confirmation email"}))
	b := fp(t, completed(500, map[string]any{"path": "/signup", "error": "Database error finding user"}))
	if a == b {
		t.Error("two causes share a fingerprint")
	}
}

func TestClassify_OneFailureSendsOncePerHour(t *testing.T) {
	c := newClassifier()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	sent := 0
	for i := 0; i < 50; i++ {
		now = now.Add(10 * time.Second)
		if c.line(completed(500, nil)) != nil {
			sent++
		}
	}
	if sent != 1 {
		t.Fatalf("50 identical lines within 10 min sent %d events, want 1", sent)
	}
	start := now.Add(-50 * 10 * time.Second)
	now = start.Add(59 * time.Minute)
	if c.line(completed(500, nil)) != nil {
		t.Error("a line at +59 min sent an event")
	}
	now = start.Add(61 * time.Minute)
	if c.line(completed(500, nil)) == nil {
		t.Error("a line at +61 min sent nothing")
	}
}

func TestClassify_NoDailyCap(t *testing.T) {
	c := newClassifier()
	now := time.Date(2026, 10, 24, 0, 5, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	for i := 0; i < 20; i++ {
		now = now.Add(time.Minute)
		if c.line(completed(500, map[string]any{"error": fmt.Sprintf("cause %c", 'a'+i)})) == nil {
			t.Fatalf("distinct failure %d sent nothing", i)
		}
	}
	if c.line(completed(500, map[string]any{"error": "cause a"})) != nil {
		t.Error("the same fingerprint within the hour sent an event")
	}
	now = now.Add(time.Hour)
	if c.line(completed(500, map[string]any{"error": "cause a"})) == nil {
		t.Error("the same fingerprint after an hour sent nothing")
	}
}

func TestClassify_AStartFailureHasItsOwnFingerprint(t *testing.T) {
	boot := newClassifier()
	boot.line(jl(map[string]any{"level": "fatal", "msg": "Failed to load configuration: x"}))
	a := strings.Join(boot.exit(1, nil).Fingerprint, "|")
	b := strings.Join(newClassifier().exit(127, errors.New("not found")).Fingerprint, "|")
	if a == b {
		t.Error("a start failure shares the fingerprint of a boot failure")
	}
}

func TestAuthEvent_AnInvalidEmailNeverReachesTheUser(t *testing.T) {
	bad := map[string]string{
		"space":  "a b@example.com",
		"quote":  `a"b@example.com`,
		"two at": "a@b@example.com",
		"long":   strings.Repeat("a", 250) + "@example.com",
		"no at":  "ok.example.com",
	}
	for name, addr := range bad {
		t.Run(name, func(t *testing.T) {
			events := wire(t, emailEvent(map[string]any{"auth_event": map[string]any{"actor_username": addr}}))
			if len(events) != 1 {
				t.Fatalf("recorded %d events, want 1", len(events))
			}
			if !events[0].User.IsEmpty() {
				t.Errorf("user = %+v, want empty", events[0].User)
			}
		})
	}
}

func TestAuthEvent_AnExitEventCarriesNoUser(t *testing.T) {
	rec := authBoot(t)
	c := newClassifier()
	c.line(jl(map[string]any{"level": "fatal", "msg": "bad config: x", "auth_event": map[string]any{"actor_username": "ok@example.com"}}))
	sentry.CaptureEvent(c.exit(1, nil))
	events := rec.Events()
	if len(events) != 1 || !events[0].User.IsEmpty() {
		t.Fatalf("events = %d, user = %+v", len(events), events)
	}
}

func TestClassify_AnIncompleteRequestLineOpensNone(t *testing.T) {
	c := newClassifier()
	for _, l := range []string{`{"msg":"request completed","status":"x"}`, `{"msg":"request completed"}`, `{"msg":"request completed","status":null,"error":5}`} {
		if ev := c.line([]byte(l)); ev != nil {
			t.Errorf("line %q opened an event", l)
		}
	}
}

func TestClassify_PruningKeepsTheHourlyLimit(t *testing.T) {
	c := newClassifier()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	for i := 0; i < memoSize*4+10; i++ {
		if c.line(completed(500, map[string]any{"error": fmt.Sprintf("cause %c%c%c", 'a'+i%26, 'a'+i/26%26, 'a'+i/676)})) == nil {
			t.Fatalf("distinct failure %d sent nothing", i)
		}
	}
	if c.line(completed(500, map[string]any{"error": "cause aaa"})) != nil {
		t.Error("a repeat within the hour sent an event after the table was pruned")
	}
}

func TestAuthEvent_DroppedFieldsNeverReachTheWire(t *testing.T) {
	extra := map[string]any{}
	for _, k := range []string{"remote_addr", "referer", "body", "cookie", "password", "user_id", "user_agent", "stack", "traits", "access_token", "refresh_token", "ip_address"} {
		extra[k] = sentrytest.MarkerCred
	}
	extra["auth_event"] = map[string]any{"actor_id": sentrytest.MarkerTIN, "actor_name": sentrytest.MarkerIRN, "traits": sentrytest.MarkerAmt}
	extra["auth_audit_event"] = map[string]any{"ip_address": sentrytest.MarkerCred}
	raw := wireJSON(t, completed(500, extra))
	for _, m := range []string{sentrytest.MarkerCred, sentrytest.MarkerTIN, sentrytest.MarkerIRN, sentrytest.MarkerAmt} {
		if strings.Contains(raw, m) {
			t.Errorf("event carries %q: %s", m, raw)
		}
	}
}
