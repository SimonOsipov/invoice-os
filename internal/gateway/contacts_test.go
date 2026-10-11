package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
)

const (
	coUserID      = "7f3c2a1e-0b7d-4f51-9a0e-5d1c2b3a4e5f"
	coDisplay     = "Zelda Quill"
	coWorkspace   = "Quillworks Ltd"
	coConsentText = "I agree to receive product news from ASComply."
	coConsentAt   = "2026-09-24T10:00:00Z"

	coRegistration = `"registration":{"workspace_name":"` + coWorkspace + `","display_name":"` + coDisplay + `","kind":"firm"}`
	coConsent      = `"marketing_consent":{"text":"` + coConsentText + `","at":"` + coConsentAt + `"}`

	coMetaFull      = `{` + coRegistration + `,` + coConsent + `}`
	coMetaNoConsent = `{` + coRegistration + `}`
)

// coWant is the contact a coMetaFull user hands off.
var coWant = RegistrantContact{
	UserID: coUserID, Email: regEmail, DisplayName: coDisplay, WorkspaceName: coWorkspace,
	Consent: &MarketingConsent{Text: coConsentText, At: coConsentAt},
}

// coUser is a GoTrue user object; meta is its user_metadata JSON, or "" to omit the key.
func coUser(meta string) string {
	m := ""
	if meta != "" {
		m = `,"user_metadata":` + meta
	}
	return `{"id":"` + coUserID + `","email":"` + regEmail + `","email_confirmed_at":"2026-09-24T10:05:00Z"` + m + `}`
}

// coSession is a GoTrue 200 session body (/verify and /token) carrying user.
func coSession(user string) string {
	return `{"access_token":"` + sessionAT + `","token_type":"bearer","expires_in":3600,"expires_at":1790000000,"refresh_token":"` + sessionRT + `","user":` + user + `}`
}

// recSink records every Registrant call and signals each on calls; onCall, when set, decides the result.
type recSink struct {
	mu     sync.Mutex
	all    []RegistrantContact
	times  []time.Time
	calls  chan RegistrantContact
	onCall func(ctx context.Context, n int, c RegistrantContact) error
}

func newRecSink(onCall func(ctx context.Context, n int, c RegistrantContact) error) *recSink {
	return &recSink{calls: make(chan RegistrantContact, 32), onCall: onCall}
}

func (s *recSink) Registrant(ctx context.Context, c RegistrantContact) error {
	s.mu.Lock()
	s.all = append(s.all, c)
	s.times = append(s.times, time.Now())
	n := len(s.all)
	s.mu.Unlock()
	s.calls <- c
	if s.onCall != nil {
		return s.onCall(ctx, n, c)
	}
	return nil
}

func (s *recSink) DemoRequest(context.Context, DemoRequest) error {
	return errors.New("unexpected demo request")
}

func (s *recSink) got() []RegistrantContact {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RegistrantContact(nil), s.all...)
}

func (s *recSink) callTimes() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.times...)
}

// wait returns the next call's contact, failing when none arrives in 5 s.
func (s *recSink) wait(t *testing.T) RegistrantContact {
	t.Helper()
	select {
	case c := <-s.calls:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("the sink was not called within 5s")
		return RegistrantContact{}
	}
}

// requireOneHandOff waits for the next call, compares it with want, and requires it to be the only call so far.
func requireOneHandOff(t *testing.T, s *recSink, want RegistrantContact) {
	t.Helper()
	if got := s.wait(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("hand-off = %+v (consent %+v), want %+v (consent %+v)", got, got.Consent, want, want.Consent)
	}
	if n := len(s.got()); n != 1 {
		t.Fatalf("sink saw %d calls, want exactly 1", n)
	}
}

const verifyQuery = "token=" + verifyToken + "&type=signup"

// verifyRequest is the click: a form POST whose body is query.
func verifyRequest(ctx context.Context, query string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/auth/verify", strings.NewReader(query)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func doVerifySink(t *testing.T, authURL *url.URL, query string, sink ContactSink) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	VerifyHandler(authURL, siteURL(t), testClient(), slog.New(slog.DiscardHandler), sink, testHandoffStore()).ServeHTTP(rec, verifyRequest(context.Background(), query))
	return rec
}

func requireRedirect(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
		t.Fatalf("answer = %d Location %q, want 303 %q", rec.Code, rec.Header().Get("Location"), want)
	}
}

const (
	verifiedLocation = siteURLValue + "/?verified=1"
	failedLocation   = siteURLValue + "/?verify=failed"
)

// withHandOffDelays sets the waits before the second and third attempt for one test.
func withHandOffDelays(t *testing.T, first, second time.Duration) {
	t.Helper()
	old := handOffDelays
	handOffDelays = []time.Duration{first, second}
	t.Cleanup(func() { handOffDelays = old })
}

func TestVerify_HandsOffTheConfirmedRegistrant(t *testing.T) {
	fake := newFakeGoTrue(t, http.StatusOK, coSession(coUser(coMetaFull)))
	sink := newRecSink(nil)

	rec := doVerifySink(t, fake.URL, verifyQuery, sink)

	requireRedirect(t, rec, verifiedLocation)
	requireOneHandOff(t, sink, coWant)
}

func TestVerify_HandOffWithoutConsent(t *testing.T) {
	for _, c := range []struct{ name, meta string }{
		{"key absent", coMetaNoConsent},
		{"key null", `{` + coRegistration + `,"marketing_consent":null}`},
		{"text empty", `{` + coRegistration + `,"marketing_consent":{"text":"","at":"` + coConsentAt + `"}}`},
		{"text a number", `{` + coRegistration + `,"marketing_consent":{"text":5,"at":"` + coConsentAt + `"}}`},
		{"consent a string", `{` + coRegistration + `,"marketing_consent":"yes"}`},
		{"consent an empty object", `{` + coRegistration + `,"marketing_consent":{}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeGoTrue(t, http.StatusOK, coSession(coUser(c.meta)))
			sink := newRecSink(nil)

			requireRedirect(t, doVerifySink(t, fake.URL, verifyQuery, sink), verifiedLocation)

			want := coWant
			want.Consent = nil
			requireOneHandOff(t, sink, want)
		})
	}
}

func TestVerify_NoHandOffWhenRefused(t *testing.T) {
	good := coSession(coUser(coMetaFull))
	for _, c := range []struct {
		name   string
		query  string
		status int // 0 = unreachable
		body   string
	}{
		{"link expired, 403", verifyQuery, http.StatusForbidden, gtOTPExpired},
		{"gotrue 500", verifyQuery, http.StatusInternalServerError, gtInternal},
		{"gotrue 201", verifyQuery, http.StatusCreated, good},
		{"gotrue 204", verifyQuery, http.StatusNoContent, good},
		{"gotrue unreachable", verifyQuery, 0, ""},
		{"missing token", "type=signup", http.StatusOK, good},
		{"wrong type", "token=" + verifyToken + "&type=recovery", http.StatusOK, good},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sink := newRecSink(nil)

				requireRedirect(t, verifyOffline(t, offlineClient(c.status, c.body), c.query, sink), failedLocation)

				if n := len(sink.got()); n != 0 {
					t.Fatalf("sink saw %d calls after a refused verify, want 0", n)
				}
			})
		})
	}

	t.Run("control: the same GoTrue answered 200 hands off one registrant", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			sink := newRecSink(nil)

			requireRedirect(t, verifyOffline(t, offlineClient(http.StatusOK, good), verifyQuery, sink), verifiedLocation)

			if got := sink.got(); len(got) != 1 || !reflect.DeepEqual(got[0], coWant) {
				t.Fatalf("hand-offs = %+v, want exactly [%+v]", got, coWant)
			}
		})
	})
}

func doSignInSink(t *testing.T, rig *signInRig) *httptest.ResponseRecorder {
	t.Helper()
	return rig.doSignIn(signInBody(regEmail, regPassword, randomState(t)))
}

func TestSignIn_HandsOffAFormRegistrant(t *testing.T) {
	fake := newTokenFake(t, http.StatusOK, coSession(coUser(coMetaFull)))
	sink := newRecSink(nil)
	rig := newSignInRigSink(t, fake.URL, nil, sink)

	requireCode(t, doSignInSink(t, rig))

	requireOneHandOff(t, sink, coWant)
}

func requireOneNow(t *testing.T, sink *recSink, want RegistrantContact) {
	t.Helper()
	if got := sink.got(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("hand-offs = %+v, want exactly [%+v]", got, want)
	}
}

func TestSignIn_NoHandOffWithoutRegistration(t *testing.T) {
	for _, c := range []struct{ name, meta string }{
		{"empty metadata", `{}`},
		{"no user_metadata key", ``},
		{"registration null", `{"registration":null}`},
		{"consent without registration", `{` + coConsent + `}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sink := newRecSink(nil)

				requireCode(t, signInOffline(t, offlineClient(http.StatusOK, coSession(coUser(c.meta))), NewHandoffStore(HandoffTTL, time.Now), sink))

				if n := len(sink.got()); n != 0 {
					t.Fatalf("sink saw %d calls for an account without registration, want 0", n)
				}
			})
		})
	}

	t.Run("control: with registration the same sign-in hands off", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			sink := newRecSink(nil)

			requireCode(t, signInOffline(t, offlineClient(http.StatusOK, coSession(coUser(coMetaFull))), NewHandoffStore(HandoffTTL, time.Now), sink))

			requireOneNow(t, sink, coWant)
		})
	})
}

func TestSignIn_NoHandOffWhenRefused(t *testing.T) {
	good := coSession(coUser(coMetaFull))
	for _, c := range []struct {
		name   string
		status int // 0 = unreachable
		body   string
	}{
		{"invalid credentials", http.StatusBadRequest, gtInvalidCredentials},
		{"banned", http.StatusBadRequest, gtUserBanned},
		{"email not confirmed", http.StatusBadRequest, gtEmailNotConfirmed},
		{"gotrue 429", http.StatusTooManyRequests, gtOverRequestRateLimit},
		{"gotrue 500", http.StatusInternalServerError, gtInternal},
		{"200 without a refresh token", http.StatusOK, `{"access_token":"` + sessionAT + `","user":` + coUser(coMetaFull) + `}`},
		{"200 without an access token", http.StatusOK, `{"refresh_token":"` + sessionRT + `","user":` + coUser(coMetaFull) + `}`},
		{"200 with an empty body", http.StatusOK, ``},
		{"201", http.StatusCreated, good},
		{"unreachable", 0, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sink := newRecSink(nil)

				if rec := signInOffline(t, offlineClient(c.status, c.body), NewHandoffStore(HandoffTTL, time.Now), sink); rec.Code == http.StatusOK {
					t.Fatalf("sign-in answered 200, want a refusal: %s", rec.Body.String())
				}

				if n := len(sink.got()); n != 0 {
					t.Fatalf("sink saw %d calls after a refused sign-in, want 0", n)
				}
			})
		})
	}

	t.Run("control: the same sign-in answered 200 hands off one registrant", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			sink := newRecSink(nil)

			requireCode(t, signInOffline(t, offlineClient(http.StatusOK, good), NewHandoffStore(HandoffTTL, time.Now), sink))

			requireOneNow(t, sink, coWant)
		})
	})
}

// answersWithin serves req on h and fails when no answer arrives within d.
func answersWithin(t *testing.T, h http.Handler, req *http.Request, d time.Duration) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()
	select {
	case <-done:
		return rec
	case <-time.After(d):
		t.Fatalf("no answer within %v while the sink blocks", d)
		return nil
	}
}

// blockingSink enters Registrant, then holds until release is closed or its context ends.
type blockingSink struct {
	entered chan struct{}
	release chan struct{}
	ctxErr  chan error
	once    sync.Once
}

func newBlockingSink(t *testing.T) *blockingSink {
	b := &blockingSink{entered: make(chan struct{}, 8), release: make(chan struct{}), ctxErr: make(chan error, 8)}
	t.Cleanup(b.open)
	return b
}

func (b *blockingSink) open() { b.once.Do(func() { close(b.release) }) }

func (b *blockingSink) Registrant(ctx context.Context, _ RegistrantContact) error {
	b.entered <- struct{}{}
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	b.ctxErr <- ctx.Err()
	return nil
}

func (b *blockingSink) DemoRequest(context.Context, DemoRequest) error { return nil }

// requireHandOffSurvivesRequest ends the request context, frees the sink, and requires the sink's context to be live.
func requireHandOffSurvivesRequest(t *testing.T, b *blockingSink, endRequest context.CancelFunc) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the sink was never called")
	}
	endRequest()
	b.open()
	select {
	case err := <-b.ctxErr:
		if err != nil {
			t.Errorf("the hand-off context ended with the request: %v; want context.WithoutCancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sink never resumed")
	}
}

func TestHandOff_BlockedSinkNeverDelays(t *testing.T) {
	t.Run("verify", func(t *testing.T) {
		fake := newFakeGoTrue(t, http.StatusOK, coSession(coUser(coMetaFull)))
		sink := newBlockingSink(t)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		h := VerifyHandler(fake.URL, siteURL(t), testClient(), slog.New(slog.DiscardHandler), sink, testHandoffStore())

		rec := answersWithin(t, h, verifyRequest(ctx, verifyQuery), time.Second)

		requireRedirect(t, rec, verifiedLocation)
		requireHandOffSurvivesRequest(t, sink, cancel)
	})
	t.Run("sign-in", func(t *testing.T) {
		fake := newTokenFake(t, http.StatusOK, coSession(coUser(coMetaFull)))
		sink := newBlockingSink(t)
		rig := newSignInRigSink(t, fake.URL, nil, sink)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		req := httptest.NewRequest(http.MethodPost, "/auth/sign-in", strings.NewReader(signInBody(regEmail, regPassword, randomState(t)))).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")

		rec := answersWithin(t, rig.signIn, req, time.Second)

		requireCode(t, rec)
		requireHandOffSurvivesRequest(t, sink, cancel)
	})
}

// logRecord is one captured log line, flattened for substring checks.
type logRecord struct {
	level slog.Level
	text  string
	attrs map[string]string
}

type logStore struct {
	mu   sync.Mutex
	recs []logRecord
	warn chan logRecord
}

type logHandler struct {
	st    *logStore
	attrs []slog.Attr
}

// newCaptureLog returns a logger that records every line and signals each WARN on the store's channel.
func newCaptureLog() (*slog.Logger, *logStore) {
	st := &logStore{warn: make(chan logRecord, 32)}
	return slog.New(&logHandler{st: st}), st
}

func (h *logHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *logHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return &logHandler{st: h.st, attrs: append(append([]slog.Attr(nil), h.attrs...), a...)}
}

func (h *logHandler) WithGroup(string) slog.Handler { return h }

func (h *logHandler) Handle(_ context.Context, r slog.Record) error {
	rec := logRecord{level: r.Level, attrs: map[string]string{}}
	var sb strings.Builder
	sb.WriteString(r.Message)
	add := func(a slog.Attr) {
		v := a.Value.Resolve().String()
		rec.attrs[a.Key] = v
		sb.WriteString(" " + a.Key + "=" + v)
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(func(a slog.Attr) bool { add(a); return true })
	rec.text = sb.String()
	h.st.mu.Lock()
	h.st.recs = append(h.st.recs, rec)
	h.st.mu.Unlock()
	if r.Level >= slog.LevelWarn {
		h.st.warn <- rec
	}
	return nil
}

func (s *logStore) all() []logRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]logRecord(nil), s.recs...)
}

func (s *logStore) waitWarn(t *testing.T) logRecord {
	t.Helper()
	select {
	case r := <-s.warn:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no WARN within 5s of a failing sink")
		return logRecord{}
	}
}

func TestHandOff_FailingSinkIsTriedThreeTimes(t *testing.T) {
	transport := func(context.Context, int, RegistrantContact) error { return errors.New("notifications unavailable") }
	refused := func(context.Context, int, RegistrantContact) error {
		return sinkStatusError(http.StatusServiceUnavailable)
	}
	for _, c := range []struct {
		name          string
		entry         string
		first, second time.Duration
		down          func(context.Context, int, RegistrantContact) error
		status        string
	}{
		{"verify, zero delays", "verify", 0, 0, transport, "0"},
		{"sign-in, zero delays", "sign-in", 0, 0, transport, "0"},
		{"verify, the injected delays are waited", "verify", 40 * time.Millisecond, 80 * time.Millisecond, transport, "0"},
		{"verify, notifications answered 503", "verify", 0, 0, refused, "503"},
		{"sign-in, notifications answered 503", "sign-in", 0, 0, refused, "503"},
	} {
		t.Run(c.name, func(t *testing.T) {
			withHandOffDelays(t, c.first, c.second)
			sink := newRecSink(c.down)
			log, logs := newCaptureLog()

			switch c.entry {
			case "verify":
				fake := newFakeGoTrue(t, http.StatusOK, coSession(coUser(coMetaFull)))
				rec := httptest.NewRecorder()
				VerifyHandler(fake.URL, siteURL(t), testClient(), log, sink, testHandoffStore()).ServeHTTP(rec, verifyRequest(context.Background(), verifyQuery))
				requireRedirect(t, rec, verifiedLocation)
			default:
				fake := newTokenFake(t, http.StatusOK, coSession(coUser(coMetaFull)))
				requireCode(t, doSignInSink(t, newSignInRigSink(t, fake.URL, log, sink)))
			}

			warn := logs.waitWarn(t)

			calls := sink.got()
			if len(calls) != 3 {
				t.Fatalf("sink saw %d calls when the WARN was logged, want 3", len(calls))
			}
			for i, got := range calls {
				if !reflect.DeepEqual(got, coWant) {
					t.Errorf("attempt %d carried %+v, want %+v", i+1, got, coWant)
				}
			}
			times := sink.callTimes()
			if gap := times[1].Sub(times[0]); gap < c.first {
				t.Errorf("second attempt after %v, want at least %v", gap, c.first)
			}
			if gap := times[2].Sub(times[1]); gap < c.second {
				t.Errorf("third attempt after %v, want at least %v", gap, c.second)
			}

			if warn.attrs["status"] != c.status {
				t.Errorf("WARN %q has status %q, want %q", warn.text, warn.attrs["status"], c.status)
			}
			if warn.attrs["user_id"] != coUserID {
				t.Errorf("WARN %q has user_id %q, want %q", warn.text, warn.attrs["user_id"], coUserID)
			}
			var warns int
			for _, r := range logs.all() {
				if r.level >= slog.LevelWarn {
					warns++
				}
				for _, secret := range []string{regEmail, coDisplay, coWorkspace} {
					if strings.Contains(r.text, secret) {
						t.Errorf("log line %q carries %q", r.text, secret)
					}
				}
			}
			if warns != 1 {
				t.Errorf("%d WARN lines, want exactly 1 after the last failure", warns)
			}
		})
	}
}

// Only 202 is success; any other 2xx from notifications is a failed hand-off.
func TestHTTPSink_OnlyAcceptedIsSuccess(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNoContent} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			withHandOffDelays(t, 0, 0)
			base, reqs := intakeServer(t, status)
			sink := NewHTTPContactSink(base, testClient(), "tok")

			rec := serveDemo(newDemoHandler(sink), demoJSON(map[string]any{"marketing_consent_text": "I agree."}))
			requireDemoAnswer(t, rec, http.StatusBadGateway, demoUnavail)

			log, logs := newCaptureLog()
			handOffRegistrant(context.Background(), log, "verify", sink, coWant)
			if got := logs.waitWarn(t).attrs["status"]; got != strconv.Itoa(status) {
				t.Errorf("WARN status %q, want %d", got, status)
			}
			if n := len(reqs()); n != 4 {
				t.Errorf("notifications saw %d requests, want 1 demo + 3 hand-off attempts", n)
			}
		})
	}
}

func TestHandOff_WarnNamesTheStatusTheHTTPSinkSaw(t *testing.T) {
	withHandOffDelays(t, 0, 0)
	base, reqs := intakeServer(t, http.StatusServiceUnavailable)
	log, logs := newCaptureLog()

	handOffRegistrant(context.Background(), log, "verify", NewHTTPContactSink(base, testClient(), "tok"), coWant)

	warn := logs.waitWarn(t)
	if got := warn.attrs["status"]; got != "503" {
		t.Errorf("WARN %q has status %q, want 503", warn.text, got)
	}
	if n := len(reqs()); n != 3 {
		t.Errorf("notifications saw %d requests, want 3", n)
	}
}

type sinkRequest struct {
	method, path string
	header       http.Header
	body         []byte
}

func intakeServer(t *testing.T, status int) (*url.URL, func() []sinkRequest) {
	t.Helper()
	var mu sync.Mutex
	var reqs []sinkRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, sinkRequest{r.Method, r.URL.Path, r.Header.Clone(), b})
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u, func() []sinkRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]sinkRequest(nil), reqs...)
	}
}

func requireOneIntakeRequest(t *testing.T, got []sinkRequest, path, token string) map[string]any {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("notifications saw %d requests, want 1", len(got))
	}
	r := got[0]
	if r.method != http.MethodPost || r.path != path {
		t.Fatalf("request = %s %s, want POST %s", r.method, r.path, path)
	}
	if v := r.header.Get("X-Gateway-Token"); v != token {
		t.Errorf("X-Gateway-Token = %q, want %q", v, token)
	}
	if _, has := r.header["X-User-Id"]; has {
		t.Error("request carries X-User-ID; notifications answers 404 to it")
	}
	if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("body %q is not one JSON object: %v", r.body, err)
	}
	return m
}

func TestHTTPContactSink_PostsTheIntakeContract(t *testing.T) {
	const token = "gw-token-value"

	t.Run("registrant with consent", func(t *testing.T) {
		base, reqs := intakeServer(t, http.StatusAccepted)

		err := NewHTTPContactSink(base, testClient(), token).Registrant(context.Background(), coWant)

		if err != nil {
			t.Fatalf("Registrant: %v", err)
		}
		body := requireOneIntakeRequest(t, reqs(), "/internal/contacts/registrants", token)
		want := map[string]any{
			"user_id": coUserID, "email": regEmail, "display_name": coDisplay, "workspace_name": coWorkspace,
			"marketing_consent": map[string]any{"text": coConsentText, "at": coConsentAt},
		}
		if !reflect.DeepEqual(body, want) {
			t.Errorf("body = %v, want %v", body, want)
		}
		if id, _ := body["user_id"].(string); len(id) != 36 || uuid.Validate(id) != nil {
			t.Errorf("user_id = %q, want a canonical 36-character uuid", id)
		}
		consent, _ := body["marketing_consent"].(map[string]any)
		if _, err := time.Parse(time.RFC3339, consent["at"].(string)); err != nil {
			t.Errorf("marketing_consent.at is not RFC 3339: %v", err)
		}
	})

	t.Run("registrant without consent carries no marketing_consent key", func(t *testing.T) {
		base, reqs := intakeServer(t, http.StatusAccepted)

		err := NewHTTPContactSink(base, testClient(), token).Registrant(context.Background(),
			RegistrantContact{UserID: coUserID, Email: regEmail})

		if err != nil {
			t.Fatalf("Registrant: %v", err)
		}
		body := requireOneIntakeRequest(t, reqs(), "/internal/contacts/registrants", token)
		if _, has := body["marketing_consent"]; has {
			t.Errorf("body = %v, want no marketing_consent key", body)
		}
		if body["user_id"] != coUserID || body["email"] != regEmail {
			t.Errorf("body = %v, want user_id and email", body)
		}
	})

	t.Run("demo request", func(t *testing.T) {
		base, reqs := intakeServer(t, http.StatusAccepted)

		err := NewHTTPContactSink(base, testClient(), token).DemoRequest(context.Background(),
			DemoRequest{Email: regEmail, Name: coDisplay, Company: coWorkspace, MarketingConsentText: coConsentText})

		if err != nil {
			t.Fatalf("DemoRequest: %v", err)
		}
		body := requireOneIntakeRequest(t, reqs(), "/internal/contacts/demo-requests", token)
		want := map[string]any{"email": regEmail, "name": coDisplay, "company": coWorkspace, "marketing_consent_text": coConsentText}
		if !reflect.DeepEqual(body, want) {
			t.Errorf("body = %v, want %v", body, want)
		}
	})

	t.Run("a base URL with a trailing slash posts to the same path", func(t *testing.T) {
		base, reqs := intakeServer(t, http.StatusAccepted)
		base.Path = "/"

		err := NewHTTPContactSink(base, testClient(), token).Registrant(context.Background(), coWant)

		if err != nil {
			t.Fatalf("Registrant: %v", err)
		}
		requireOneIntakeRequest(t, reqs(), "/internal/contacts/registrants", token)
	})

	t.Run("a redirect is an error and is never followed", func(t *testing.T) {
		for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
			var calls []string
			client := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
				calls = append(calls, r.URL.String())
				h := http.Header{"Location": {"http://elsewhere.invalid/steal"}}
				return &http.Response{StatusCode: status, Header: h, Body: http.NoBody, Request: r}, nil
			})}
			base, _ := url.Parse("http://notifications.invalid")
			sink := NewHTTPContactSink(base, client, token)

			errR := sink.Registrant(context.Background(), coWant)
			errD := sink.DemoRequest(context.Background(), DemoRequest{Email: regEmail})

			if errR == nil || errD == nil {
				t.Errorf("%d: Registrant err %v, DemoRequest err %v, want an error from both", status, errR, errD)
			}
			if len(calls) != 2 {
				t.Errorf("%d: transport saw %v, want exactly the two original requests", status, calls)
			}
		}
	})

	t.Run("each call ends at the 5 s bound, whatever the caller's context", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			client := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
				<-r.Context().Done()
				return nil, r.Context().Err()
			})}
			base, _ := url.Parse("http://notifications.invalid")
			sink := NewHTTPContactSink(base, client, token)

			for name, call := range map[string]func() error{
				"registrant":   func() error { return sink.Registrant(context.Background(), coWant) },
				"demo request": func() error { return sink.DemoRequest(context.Background(), DemoRequest{Email: regEmail}) },
			} {
				start := time.Now()
				err := call()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("%s: err = %v, want the deadline", name, err)
				}
				if got := time.Since(start); got != 5*time.Second {
					t.Errorf("%s ended after %v, want exactly 5s", name, got)
				}
			}
		})
	})

	for _, status := range []int{http.StatusInternalServerError, http.StatusNotFound, http.StatusBadRequest} {
		t.Run("a "+http.StatusText(status)+" answer is an error", func(t *testing.T) {
			base, reqs := intakeServer(t, status)

			err := NewHTTPContactSink(base, testClient(), token).Registrant(context.Background(), coWant)

			if len(reqs()) != 1 {
				t.Fatalf("notifications saw %d requests, want 1", len(reqs()))
			}
			if err == nil {
				t.Errorf("Registrant answered nil on a %d, want an error so the hand-off retries", status)
			}
		})
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// offlineClient answers every request with status and body without a network; status 0 is a refused connection.
func offlineClient(status int, body string) *http.Client {
	return &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		if status == 0 {
			return nil, errors.New("connection refused")
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
}

var offlineAuth = &url.URL{Scheme: "http", Host: "gotrue.invalid"}

// verifyOffline serves one POST /auth/verify with form query inside a synctest bubble: when it returns the hand-off has run to its end or sleeps.
func verifyOffline(t *testing.T, client *http.Client, query string, sink ContactSink) *httptest.ResponseRecorder {
	t.Helper()
	return verifyOfflineLog(t, client, query, sink, slog.New(slog.DiscardHandler))
}

func verifyOfflineLog(t *testing.T, client *http.Client, query string, sink ContactSink, log *slog.Logger) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	VerifyHandler(offlineAuth, siteURL(t), client, log, sink, testHandoffStore()).ServeHTTP(rec, verifyRequest(context.Background(), query))
	synctest.Wait()
	return rec
}

// signInOffline serves one POST /auth/sign-in inside a synctest bubble, like verifyOffline.
func signInOffline(t *testing.T, client *http.Client, store *HandoffStore, sink ContactSink) *httptest.ResponseRecorder {
	t.Helper()
	return signInOfflineLog(t, client, store, sink, slog.New(slog.DiscardHandler))
}

func signInOfflineLog(t *testing.T, client *http.Client, store *HandoffStore, sink ContactSink, log *slog.Logger) *httptest.ResponseRecorder {
	t.Helper()
	th := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, time.Now)
	rec := serve(SignInHandler(offlineAuth, client, store, th, log, sink), http.MethodPost, "/auth/sign-in", signInBody(regEmail, regPassword, randomState(t)))
	synctest.Wait()
	return rec
}

func TestHandOff_UserBodyShapes(t *testing.T) {
	idEmail := RegistrantContact{UserID: coUserID, Email: regEmail}
	noConsent := coWant
	noConsent.Consent = nil
	meta := func(consent string) string { return `{` + coRegistration + `,"marketing_consent":` + consent + `}` }
	str := func(v string) string { return `"` + v + `"` }
	extras := `{"id":"` + coUserID + `","aud":"authenticated","email":"` + regEmail + `","app_metadata":{"provider":"email"},"identities":[{"provider":"email"}],` +
		`"user_metadata":{"email_verified":true,` + coRegistration + `,` + coConsent + `,"avatar":null}}`
	withAt := func(at string) RegistrantContact {
		c := coWant
		c.Consent = &MarketingConsent{Text: coConsentText, At: at}
		return c
	}
	none := (*RegistrantContact)(nil)
	for _, c := range []struct {
		name         string
		user         string
		verify, sign *RegistrantContact
	}{
		{"unknown fields beside the known ones", extras, &coWant, &coWant},
		{"blank email is not handed off", blankEmail(coMetaFull, ``), none, none},
		{"whitespace email is not handed off", blankEmail(coMetaFull, " \\t "), none, none},
		{"no email key is not handed off", `{"id":"` + coUserID + `","user_metadata":` + coMetaFull + `}`, none, none},
		{"no user_metadata: any verified account is handed off, a sign-in is not", coUser(``), &idEmail, none},
		{"empty metadata", coUser(`{}`), &idEmail, none},
		{"user_metadata null", coUser(`null`), &idEmail, none},
		{"user_metadata a string", coUser(str("x")), &idEmail, none},
		{"registration a string still counts as present", coUser(`{"registration":"x"}`), &idEmail, &idEmail},
		{"registration an array still counts as present", coUser(`{"registration":[]}`), &idEmail, &idEmail},
		{"registration an empty object counts as present", coUser(`{"registration":{}}`), &idEmail, &idEmail},
		{"consent text a number", coUser(meta(`{"text":5,"at":"` + coConsentAt + `"}`)), &noConsent, &noConsent},
		{"consent an array", coUser(meta(`[]`)), &noConsent, &noConsent},
		{"consent at not a time is passed on for notifications to refuse", coUser(meta(`{"text":"` + coConsentText + `","at":"yesterday"}`)), contactPtr(withAt("yesterday")), contactPtr(withAt("yesterday"))},
		{"consent at a number", coUser(meta(`{"text":"` + coConsentText + `","at":5}`)), contactPtr(withAt("")), contactPtr(withAt(""))},
	} {
		for _, entry := range []string{"verify", "sign-in"} {
			t.Run(entry+"/"+c.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					sink := newRecSink(nil)
					log, logs := newCaptureLog()
					client := offlineClient(http.StatusOK, coSession(c.user))
					want := c.verify
					if entry == "verify" {
						requireRedirect(t, verifyOfflineLog(t, client, verifyQuery, sink, log), verifiedLocation)
					} else {
						want = c.sign
						requireCode(t, signInOfflineLog(t, client, NewHandoffStore(HandoffTTL, time.Now), sink, log))
					}

					var u struct {
						Email string `json:"email"`
					}
					_ = json.Unmarshal([]byte(c.user), &u)
					warns := logs.all()
					if blank := strings.TrimSpace(u.Email) == ""; blank {
						if len(warns) != 1 || warns[0].level != slog.LevelWarn || !strings.HasPrefix(warns[0].text, entry+": gotrue user has no email; no hand-off") {
							t.Fatalf("logs = %+v, want exactly one %s WARN naming the missing email", warns, entry)
						}
						for _, pii := range []string{regEmail, coUserID, sessionAT, sessionRT} {
							if strings.Contains(warns[0].text, pii) {
								t.Errorf("WARN %q holds %q", warns[0].text, pii)
							}
						}
					} else if len(warns) != 0 {
						t.Fatalf("logs = %+v, want none", warns)
					}

					got := sink.got()
					if want == nil {
						if len(got) != 0 {
							t.Fatalf("handed off %+v, want nothing", got)
						}
						return
					}
					if len(got) != 1 || !reflect.DeepEqual(got[0], *want) {
						t.Fatalf("hand-offs = %+v, want exactly [%+v]", got, *want)
					}
				})
			})
		}
	}
}

// blankEmail is a GoTrue user whose email is email.
func blankEmail(meta, email string) string {
	return `{"id":"` + coUserID + `","email":"` + email + `","user_metadata":` + meta + `}`
}

func contactPtr(c RegistrantContact) *RegistrantContact { return &c }

func TestHandOff_NilSinkIsANoOp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log, logs := newCaptureLog()

		handOffRegistrant(context.Background(), log, "verify", nil, coWant)
		synctest.Wait()

		if got := logs.all(); len(got) != 0 {
			t.Errorf("a nil sink logged %+v, want silence", got)
		}
	})
	t.Run("verify and sign-in answer as without a sink", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			client := offlineClient(http.StatusOK, coSession(coUser(coMetaFull)))
			requireRedirect(t, verifyOffline(t, client, verifyQuery, nil), verifiedLocation)
			requireCode(t, signInOffline(t, client, NewHandoffStore(HandoffTTL, time.Now), nil))
		})
	})
}

func TestHandOff_AttemptsAreSpacedFiveThenThirtySeconds(t *testing.T) {
	down := func(context.Context, int, RegistrantContact) error { return errors.New("notifications unavailable") }
	synctest.Test(t, func(t *testing.T) {
		sink := newRecSink(down)
		log, logs := newCaptureLog()

		handOffRegistrant(context.Background(), log, "verify", sink, coWant)
		synctest.Wait()
		step := func(d time.Duration, wantCalls, wantWarns int) {
			t.Helper()
			time.Sleep(d)
			synctest.Wait()
			if n := len(sink.got()); n != wantCalls {
				t.Fatalf("%d calls, want %d", n, wantCalls)
			}
			if n := len(logs.all()); n != wantWarns {
				t.Fatalf("%d log lines, want %d", n, wantWarns)
			}
		}
		if n := len(sink.got()); n != 1 {
			t.Fatalf("%d calls at once, want 1", n)
		}
		step(5*time.Second-time.Millisecond, 1, 0)
		step(time.Millisecond, 2, 0)
		step(30*time.Second-time.Millisecond, 2, 0)
		step(time.Millisecond, 3, 1)
		step(time.Hour, 3, 1)
		if w := <-logs.warn; w.attrs["user_id"] != coUserID || w.level != slog.LevelWarn {
			t.Errorf("WARN = %+v, want a WARN with user_id %s", w, coUserID)
		}
	})
}

func TestHandOff_StopsAtTheFirstSuccess(t *testing.T) {
	for _, failures := range []int{0, 1, 2} {
		t.Run(strconv.Itoa(failures)+" failures first", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sink := newRecSink(func(_ context.Context, n int, _ RegistrantContact) error {
					if n <= failures {
						return errors.New("notifications unavailable")
					}
					return nil
				})
				log, logs := newCaptureLog()

				handOffRegistrant(context.Background(), log, "verify", sink, coWant)
				time.Sleep(time.Hour)
				synctest.Wait()

				if n := len(sink.got()); n != failures+1 {
					t.Errorf("%d calls, want %d", n, failures+1)
				}
				if n := len(logs.all()); n != 0 {
					t.Errorf("%d log lines after a success, want 0: %+v", n, logs.all())
				}
			})
		})
	}
}

func TestSignIn_NoHandOffWhenTheCodeStoreIsFull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := newRecSink(nil)
		store := NewHandoffStore(HandoffTTL, time.Now)
		fillStore(t, store, HandoffMaxLive)

		rec := signInOffline(t, offlineClient(http.StatusOK, coSession(coUser(coMetaFull))), store, sink)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("sign-in answered %d, want 503", rec.Code)
		}
		if n := len(sink.got()); n != 0 {
			t.Fatalf("handed off %d registrants for a refused sign-in, want 0", n)
		}
	})
}

const (
	demoEmail   = "ada@corp.example"
	demoName    = "Ada Lovelace"
	demoCompany = "Analytical Engines Ltd"
	demoUnavail = `{"error":"demo request is unavailable"}`
	demoNameMsg = "name must be 1 to 200 characters"
	demoCoMsg   = "company must be 1 to 200 characters"
	demoTextMsg = "marketing_consent_text must be 1 to 500 characters"
)

// demoSink records every DemoRequest call; err is the answer.
type demoSink struct {
	mu   sync.Mutex
	reqs []DemoRequest
	err  error
}

func (s *demoSink) Registrant(context.Context, RegistrantContact) error {
	return errors.New("unexpected registrant")
}

func (s *demoSink) DemoRequest(_ context.Context, d DemoRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, d)
	return s.err
}

func (s *demoSink) got() []DemoRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]DemoRequest(nil), s.reqs...)
}

// dropField removes a key from demoJSON's fields.
var dropField = new(int)

// demoJSON is a valid demo-request body with over applied; a dropField value deletes the key.
func demoJSON(over map[string]any) string {
	f := map[string]any{"email": demoEmail, "name": demoName, "company": demoCompany}
	for k, v := range over {
		if v == dropField {
			delete(f, k)
		} else {
			f[k] = v
		}
	}
	b, _ := json.Marshal(f)
	return string(b)
}

func serveDemo(h http.Handler, body string) *httptest.ResponseRecorder {
	return serve(h, http.MethodPost, "/contacts/demo-request", body)
}

func newDemoThrottle() *SignInThrottle {
	return NewSignInThrottle("demo-request", DemoRequestPerIP, DemoRequestMaxKeys, DemoRequestWindow, time.Now)
}

func newDemoHandler(sink ContactSink) http.Handler {
	return DemoRequestHandler(sink, newDemoThrottle(), true, slog.New(slog.DiscardHandler))
}

// requireDemoAnswer fails unless rec is a JSON answer with this status and body and Cache-Control: no-store.
func requireDemoAnswer(t *testing.T, rec *httptest.ResponseRecorder, status int, wantBody string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("%d Cache-Control = %q, want no-store", status, got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("%d Content-Type = %q, want application/json", status, got)
	}
	if wantBody != "" && strings.TrimSpace(rec.Body.String()) != wantBody {
		t.Errorf("%d body = %q, want %s", status, rec.Body.String(), wantBody)
	}
}

func TestDemoRequest_ForwardsAndAccepts(t *testing.T) {
	sink := &demoSink{}
	rec := serveDemo(newDemoHandler(sink), demoJSON(map[string]any{
		"email": "  " + demoEmail + "\t", "name": "\n " + demoName + "  ", "company": "  " + demoCompany + " ",
	}))

	requireDemoAnswer(t, rec, http.StatusAccepted, `{"status":"accepted"}`)
	want := []DemoRequest{{Email: demoEmail, Name: demoName, Company: demoCompany}}
	if got := sink.got(); !reflect.DeepEqual(got, want) {
		t.Errorf("sink got %+v, want exactly %+v", got, want)
	}
}

func TestDemoRequest_ConsentTextIsOptional(t *testing.T) {
	const text = "I agree to receive product news from ASComply — éà, no spam."
	for _, tc := range []struct {
		name string
		over map[string]any
		want string
	}{
		{"absent", nil, ""},
		{"ticked", map[string]any{"marketing_consent_text": text}, text},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &demoSink{}
			requireDemoAnswer(t, serveDemo(newDemoHandler(sink), demoJSON(tc.over)), http.StatusAccepted, `{"status":"accepted"}`)
			want := []DemoRequest{{Email: demoEmail, Name: demoName, Company: demoCompany, MarketingConsentText: tc.want}}
			if got := sink.got(); !reflect.DeepEqual(got, want) {
				t.Errorf("sink got %+v, want %+v", got, want)
			}
		})
	}
}

func TestDemoRequest_InvalidFieldsAre400(t *testing.T) {
	email254 := strings.Repeat("a", 254-len("@corp.example")) + "@corp.example"
	// valid fields plus an ignored key that pushes the body past 4 KiB.
	over4KiB := func(n int) string { return demoJSON(map[string]any{"pad": strings.Repeat("x", n)}) }
	cases := []struct {
		name string
		body string
		msg  string // "" asserts only that an error is reported
	}{
		{"email absent", demoJSON(map[string]any{"email": dropField}), "email is invalid"},
		{"email empty", demoJSON(map[string]any{"email": ""}), "email is invalid"},
		{"email blank", demoJSON(map[string]any{"email": "  \t"}), "email is invalid"},
		{"email without at", demoJSON(map[string]any{"email": "ada.corp.example"}), "email is invalid"},
		{"email with two ats", demoJSON(map[string]any{"email": "a@b@corp.example"}), "email is invalid"},
		{"email with inner space", demoJSON(map[string]any{"email": "ada @corp.example"}), "email is invalid"},
		{"email with inner tab", demoJSON(map[string]any{"email": "ada@corp\t.example"}), "email is invalid"},
		{"email of two bytes", demoJSON(map[string]any{"email": "a@"}), "email is invalid"},
		{"email of 255 bytes", demoJSON(map[string]any{"email": "a" + email254}), "email is invalid"},
		{"email with NUL", demoJSON(map[string]any{"email": "ada\x00@corp.example"}), "email is invalid"},
		{"email wrong type", `{"email":7,"name":"` + demoName + `","company":"` + demoCompany + `"}`, ""},
		{"name absent", demoJSON(map[string]any{"name": dropField}), demoNameMsg},
		{"name empty", demoJSON(map[string]any{"name": ""}), demoNameMsg},
		{"name blank", demoJSON(map[string]any{"name": " \t\n"}), demoNameMsg},
		{"name of 201 runes", demoJSON(map[string]any{"name": strings.Repeat("é", 201)}), demoNameMsg},
		{"name with NUL", demoJSON(map[string]any{"name": "Ada\x00Lovelace"}), "name must not contain a NUL byte"},
		{"company absent", demoJSON(map[string]any{"company": dropField}), demoCoMsg},
		{"company empty", demoJSON(map[string]any{"company": ""}), demoCoMsg},
		{"company blank", demoJSON(map[string]any{"company": "   "}), demoCoMsg},
		{"company of 201 runes", demoJSON(map[string]any{"company": strings.Repeat("c", 201)}), demoCoMsg},
		{"company with NUL", demoJSON(map[string]any{"company": "Acme\x00"}), "company must not contain a NUL byte"},
		{"text empty", demoJSON(map[string]any{"marketing_consent_text": ""}), demoTextMsg},
		{"text blank spaces", demoJSON(map[string]any{"marketing_consent_text": "    "}), demoTextMsg},
		{"text blank mixed whitespace", demoJSON(map[string]any{"marketing_consent_text": " \n\t\r "}), demoTextMsg},
		{"text of 501 runes", demoJSON(map[string]any{"marketing_consent_text": strings.Repeat("é", 501)}), demoTextMsg},
		{"text with NUL", demoJSON(map[string]any{"marketing_consent_text": "I agree\x00"}), demoTextMsg},
		{"text wrong type", demoJSON(map[string]any{"marketing_consent_text": 5}), ""},
		{"body over 4 KiB", over4KiB(4097), ""},
		{"malformed JSON", `{"email":`, ""},
		{"empty body", ``, ""},
		{"JSON array", `[]`, ""},
		{"JSON null", `null`, ""},
	}
	if len(cases) < 30 {
		t.Fatalf("table shrank to %d cases", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &demoSink{}
			rec := serveDemo(newDemoHandler(sink), tc.body)

			requireDemoAnswer(t, rec, http.StatusBadRequest, "")
			var got map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got["error"] == "" {
				t.Fatalf("body = %q, want {\"error\": <message>}", rec.Body.String())
			}
			if tc.msg != "" && got["error"] != tc.msg {
				t.Errorf("error = %q, want %q", got["error"], tc.msg)
			}
			if n := len(sink.got()); n != 0 {
				t.Errorf("sink saw %d calls for an invalid request, want none: %+v", n, sink.got())
			}
		})
	}
}

// The limits are inclusive: the same fields one step further are the 400s of the table above.
func TestDemoRequest_BoundaryValuesAreAccepted(t *testing.T) {
	email254 := strings.Repeat("a", 254-len("@corp.example")) + "@corp.example"
	name200, company200, text500 := strings.Repeat("é", 200), strings.Repeat("ç", 200), strings.Repeat("ß", 500)
	fields := map[string]any{"email": email254, "name": name200, "company": company200, "marketing_consent_text": text500}

	sink := &demoSink{}
	requireDemoAnswer(t, serveDemo(newDemoHandler(sink), demoJSON(fields)), http.StatusAccepted, `{"status":"accepted"}`)
	want := []DemoRequest{{Email: email254, Name: name200, Company: company200, MarketingConsentText: text500}}
	if got := sink.got(); !reflect.DeepEqual(got, want) {
		t.Errorf("sink got %+v, want %+v", got, want)
	}

	// The lower limits are inclusive too, and the consent sentence is forwarded as sent, padding included.
	const padded = "  I agree.\n"
	sink = &demoSink{}
	requireDemoAnswer(t, serveDemo(newDemoHandler(sink), demoJSON(map[string]any{"email": "a@b", "name": "A", "company": "é", "marketing_consent_text": padded})), http.StatusAccepted, `{"status":"accepted"}`)
	want = []DemoRequest{{Email: "a@b", Name: "A", Company: "é", MarketingConsentText: padded}}
	if got := sink.got(); !reflect.DeepEqual(got, want) {
		t.Errorf("sink got %+v, want %+v", got, want)
	}

	// A body of exactly 4096 bytes is accepted.
	empty := len(demoJSON(map[string]any{"pad": ""}))
	body := demoJSON(map[string]any{"pad": strings.Repeat("x", 4096-empty)})
	if len(body) != 4096 {
		t.Fatalf("test body is %d bytes, want 4096", len(body))
	}
	sink = &demoSink{}
	requireDemoAnswer(t, serveDemo(newDemoHandler(sink), body), http.StatusAccepted, `{"status":"accepted"}`)
	if n := len(sink.got()); n != 1 {
		t.Errorf("sink saw %d calls for a 4096-byte body, want 1", n)
	}
}

func TestDemoRequest_UpstreamFailureIs502(t *testing.T) {
	const secret = "secret-upstream-detail"
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"transport error naming the email", errors.New(secret + " for " + demoEmail)},
		{"notifications 500", sinkStatusError(http.StatusInternalServerError)},
		{"notifications 400", sinkStatusError(http.StatusBadRequest)},
		{"notifications 404", sinkStatusError(http.StatusNotFound)},
		{"deadline exceeded", context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &demoSink{err: tc.err}
			rec := serveDemo(newDemoHandler(sink), demoJSON(map[string]any{"marketing_consent_text": "I agree."}))

			requireDemoAnswer(t, rec, http.StatusBadGateway, demoUnavail)
			for _, leak := range []string{demoEmail, secret, "500", "notifications"} {
				if strings.Contains(rec.Body.String(), leak) {
					t.Errorf("502 body %q leaks %q", rec.Body.String(), leak)
				}
			}
			if n := len(sink.got()); n != 1 {
				t.Errorf("sink saw %d attempts, want exactly 1 (the browser retries)", n)
			}
		})
	}

	// The 502 never puts the visitor's data in a log line.
	t.Run("log carries no personal data", func(t *testing.T) {
		log, store := newCaptureLog()
		sink := &demoSink{err: errors.New("connection refused")}
		rec := serveDemo(DemoRequestHandler(sink, newDemoThrottle(), true, log), demoJSON(map[string]any{"marketing_consent_text": "I agree to everything."}))
		requireDemoAnswer(t, rec, http.StatusBadGateway, demoUnavail)
		for _, l := range store.all() {
			for _, pii := range []string{demoEmail, demoName, demoCompany, "I agree to everything."} {
				if strings.Contains(l.text, pii) {
					t.Errorf("log line %q carries %q", l.text, pii)
				}
			}
		}
	})
}

// A notifications that never answers is cut at 5 s by the real HTTP sink, once, on the fake clock.
func TestDemoRequest_UpstreamTimeoutIs502(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		client := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			<-r.Context().Done()
			return nil, r.Context().Err()
		})}
		sink := NewHTTPContactSink(&url.URL{Scheme: "http", Host: "notifications.invalid"}, client, "tok")

		start := time.Now()
		rec := serveDemo(newDemoHandler(sink), demoJSON(nil))
		elapsed := time.Since(start)

		requireDemoAnswer(t, rec, http.StatusBadGateway, demoUnavail)
		if elapsed != 5*time.Second {
			t.Errorf("answered after %v, want the 5 s timeout", elapsed)
		}
		if calls != 1 {
			t.Errorf("notifications was called %d times, want 1", calls)
		}
	})
}

func TestDemoRequest_OnlyPostIsServed(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			sink := &demoSink{}
			rec := serve(newDemoHandler(sink), method, "/contacts/demo-request", demoJSON(nil))

			requireDemoAnswer(t, rec, http.StatusMethodNotAllowed, "")
			if got := rec.Header().Get("Allow"); got != http.MethodPost {
				t.Errorf("Allow = %q, want POST", got)
			}
			if method != http.MethodHead && strings.TrimSpace(rec.Body.String()) != `{"error":"method not allowed"}` {
				t.Errorf("body = %q, want the method-not-allowed envelope", rec.Body.String())
			}
			if n := len(sink.got()); n != 0 {
				t.Errorf("sink saw %d calls for %s, want none", n, method)
			}
		})
	}

	sink := &demoSink{}
	requireDemoAnswer(t, serveDemo(newDemoHandler(sink), demoJSON(nil)), http.StatusAccepted, `{"status":"accepted"}`)
	if n := len(sink.got()); n != 1 {
		t.Errorf("sink saw %d calls for the POST, want 1", n)
	}
}

const demoTooMany = `{"error":"too many requests"}`

// postDemoFrom posts a valid demo body from remoteAddr, with an optional X-Real-IP.
func postDemoFrom(h http.Handler, remoteAddr, realIP string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/contacts/demo-request", strings.NewReader(demoJSON(nil)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	if realIP != "" {
		req.Header.Set("X-Real-IP", realIP)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDemoRequest_SixthFromOneIPIsRefused(t *testing.T) {
	sink := &demoSink{}
	h := newDemoHandler(sink)
	for i := 1; i <= 5; i++ {
		if rec := postDemoFrom(h, "203.0.113.7:4000", ""); rec.Code != http.StatusAccepted {
			t.Fatalf("request %d = %d, want 202", i, rec.Code)
		}
	}
	requireDemoAnswer(t, postDemoFrom(h, "203.0.113.7:4000", ""), http.StatusTooManyRequests, demoTooMany)
	if n := len(sink.got()); n != 5 {
		t.Errorf("sink saw %d, want 5", n)
	}
}

func TestDemoRequest_OtherIPStillGoesThrough(t *testing.T) {
	sink := &demoSink{}
	h := newDemoHandler(sink)
	for range DemoRequestPerIP + 1 {
		postDemoFrom(h, "203.0.113.7:4000", "")
	}
	if rec := postDemoFrom(h, "198.51.100.9:4000", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("other IP = %d, want 202", rec.Code)
	}
	if n := len(sink.got()); n != DemoRequestPerIP+1 {
		t.Errorf("sink saw %d, want %d", n, DemoRequestPerIP+1)
	}
}

func TestDemoRequest_KeyIsXRealIPThenRemoteAddr(t *testing.T) {
	h := newDemoHandler(&demoSink{})
	for range DemoRequestPerIP {
		postDemoFrom(h, "192.0.2.1:1", "203.0.113.7")
	}
	if rec := postDemoFrom(h, "192.0.2.1:1", "198.51.100.9"); rec.Code != http.StatusAccepted {
		t.Errorf("other X-Real-IP = %d, want 202", rec.Code)
	}
	if rec := postDemoFrom(h, "192.0.2.2:1", "203.0.113.7"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("same X-Real-IP, other RemoteAddr = %d, want 429", rec.Code)
	}
	h = newDemoHandler(&demoSink{})
	for i := range DemoRequestPerIP {
		addr := "[2001:db8:1:2::1]:1"
		if i%2 == 1 {
			addr = "[2001:db8:1:2:ffff::9]:1"
		}
		postDemoFrom(h, addr, "")
	}
	if rec := postDemoFrom(h, "[2001:db8:1:2::77]:1", ""); rec.Code != http.StatusTooManyRequests {
		t.Errorf("sixth in one /64 = %d, want 429", rec.Code)
	}
}

func TestDemoRequest_InvalidBodySpendsNoBudget(t *testing.T) {
	h := newDemoHandler(&demoSink{})
	for range 8 {
		req := httptest.NewRequest(http.MethodPost, "/contacts/demo-request", strings.NewReader(demoJSON(map[string]any{"email": "nope"})))
		req.RemoteAddr = "203.0.113.7:4000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("bad email = %d, want 400", rec.Code)
		}
	}
	for i := range DemoRequestPerIP {
		if rec := postDemoFrom(h, "203.0.113.7:4000", ""); rec.Code != http.StatusAccepted {
			t.Fatalf("valid %d = %d, want 202", i+1, rec.Code)
		}
	}
	if rec := postDemoFrom(h, "203.0.113.7:4000", ""); rec.Code != http.StatusTooManyRequests {
		t.Errorf("sixth valid = %d, want 429", rec.Code)
	}
}

func TestDemoRequest_NonPostSpendsNoBudget(t *testing.T) {
	h := newDemoHandler(&demoSink{})
	for range 8 {
		req := httptest.NewRequest(http.MethodGet, "/contacts/demo-request", nil)
		req.RemoteAddr = "203.0.113.7:4000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET = %d, want 405", rec.Code)
		}
	}
	for i := range DemoRequestPerIP {
		if rec := postDemoFrom(h, "203.0.113.7:4000", ""); rec.Code != http.StatusAccepted {
			t.Fatalf("valid %d = %d, want 202", i+1, rec.Code)
		}
	}
}

func TestDemoRequest_SinkFailureSpendsBudget(t *testing.T) {
	sink := &demoSink{err: errors.New("connection refused")}
	h := newDemoHandler(sink)
	for i := range DemoRequestPerIP {
		if rec := postDemoFrom(h, "203.0.113.7:4000", ""); rec.Code != http.StatusBadGateway {
			t.Fatalf("failing request %d = %d, want 502", i+1, rec.Code)
		}
	}
	if rec := postDemoFrom(h, "203.0.113.7:4000", ""); rec.Code != http.StatusTooManyRequests {
		t.Errorf("sixth = %d, want 429", rec.Code)
	}
	if n := len(sink.got()); n != DemoRequestPerIP {
		t.Errorf("sink saw %d calls, want %d", n, DemoRequestPerIP)
	}
}

func TestDemoRequest_Sink4xxRefundsBudget(t *testing.T) {
	sink := &demoSink{err: sinkStatusError(http.StatusBadRequest)}
	h := newDemoHandler(sink)
	for i := range DemoRequestPerIP + 3 {
		if rec := postDemoFrom(h, "203.0.113.7:4000", ""); rec.Code != http.StatusBadGateway {
			t.Fatalf("request %d = %d, want 502 (budget refunded)", i+1, rec.Code)
		}
	}
}

func TestDemoRequest_Sink5xxSpendsBudget(t *testing.T) {
	sink := &demoSink{err: sinkStatusError(http.StatusServiceUnavailable)}
	h := newDemoHandler(sink)
	for i := range DemoRequestPerIP {
		if rec := postDemoFrom(h, "203.0.113.7:4000", ""); rec.Code != http.StatusBadGateway {
			t.Fatalf("request %d = %d, want 502", i+1, rec.Code)
		}
	}
	if rec := postDemoFrom(h, "203.0.113.7:4000", ""); rec.Code != http.StatusTooManyRequests {
		t.Errorf("sixth = %d, want 429", rec.Code)
	}
}

func TestDemoRequest_UnenforcedLogsAndLetsThrough(t *testing.T) {
	sink := &demoSink{}
	log, store := newCaptureLog()
	h := DemoRequestHandler(sink, newDemoThrottle(), false, log)
	for i := range DemoRequestPerIP + 1 {
		if rec := postDemoFrom(h, "203.0.113.7:4000", ""); rec.Code != http.StatusAccepted {
			t.Fatalf("request %d = %d, want 202", i+1, rec.Code)
		}
	}
	if n := len(sink.got()); n != DemoRequestPerIP+1 {
		t.Errorf("sink saw %d, want %d", n, DemoRequestPerIP+1)
	}
	var hits []logRecord
	for _, l := range store.all() {
		if strings.HasPrefix(l.text, "demo-request: limit reached") {
			hits = append(hits, l)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("limit log lines = %d, want 1", len(hits))
	}
	for k, v := range map[string]string{"limit": "ip", "key_source": "remote_addr", "enforced": "false"} {
		if hits[0].attrs[k] != v {
			t.Errorf("log %s = %q, want %q", k, hits[0].attrs[k], v)
		}
	}
}

func TestDemoRequest_LimitLogCarriesNoIPOrEmail(t *testing.T) {
	var buf bytes.Buffer
	h := DemoRequestHandler(&demoSink{}, newDemoThrottle(), true, slog.New(slog.NewJSONHandler(&buf, nil)))
	for range DemoRequestPerIP + 1 {
		postDemoFrom(h, "203.0.113.7:4000", "")
	}
	if !strings.Contains(buf.String(), "demo-request: limit reached") {
		t.Fatalf("no limit log line in %q", buf.String())
	}
	for _, s := range []string{"203.0.113.7", demoEmail} {
		if strings.Contains(buf.String(), s) {
			t.Errorf("log carries %q: %s", s, buf.String())
		}
	}
}

func TestDemoRequest_FullMapRefusesNewKeys(t *testing.T) {
	sink := &demoSink{}
	h := DemoRequestHandler(sink, NewSignInThrottle("demo-request", 5, 2, time.Hour, time.Now), true, slog.New(slog.DiscardHandler))
	for _, ip := range []string{"192.0.2.1:1", "192.0.2.2:1"} {
		if rec := postDemoFrom(h, ip, ""); rec.Code != http.StatusAccepted {
			t.Fatalf("%s = %d, want 202", ip, rec.Code)
		}
	}
	if rec := postDemoFrom(h, "192.0.2.3:1", ""); rec.Code != http.StatusTooManyRequests {
		t.Errorf("new IP with the map full = %d, want 429", rec.Code)
	}
	if n := len(sink.got()); n != 2 {
		t.Errorf("sink saw %d, want 2", n)
	}
	if rec := postDemoFrom(h, "192.0.2.1:1", ""); rec.Code != http.StatusAccepted {
		t.Errorf("counted IP = %d, want 202", rec.Code)
	}
}
