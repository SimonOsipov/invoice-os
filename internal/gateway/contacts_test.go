package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
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

func verifyRequest(ctx context.Context, query string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/auth/verify?"+query, nil).WithContext(ctx)
}

func doVerifySink(t *testing.T, authURL *url.URL, query string, sink ContactSink) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	VerifyHandler(authURL, siteURL(t), testClient(), slog.New(slog.DiscardHandler), sink).ServeHTTP(rec, verifyRequest(context.Background(), query))
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
	sink := newRecSink(nil)
	good := coSession(coUser(coMetaFull))
	for _, c := range []struct {
		name   string
		url    func(t *testing.T) *url.URL
		query  string
		status int
		body   string
	}{
		{"link expired, 403", nil, verifyQuery, http.StatusForbidden, gtOTPExpired},
		{"gotrue 500", nil, verifyQuery, http.StatusInternalServerError, gtInternal},
		{"gotrue unreachable", closedURL, verifyQuery, 0, ""},
		{"missing token", nil, "type=signup", http.StatusOK, good},
		{"wrong type", nil, "token=" + verifyToken + "&type=recovery", http.StatusOK, good},
	} {
		t.Run(c.name, func(t *testing.T) {
			authURL := closedURL(t)
			if c.url == nil {
				authURL = newFakeGoTrue(t, c.status, c.body).URL
			}

			requireRedirect(t, doVerifySink(t, authURL, c.query, sink), failedLocation)

			if n := len(sink.got()); n != 0 {
				t.Fatalf("sink saw %d calls after a refused verify, want 0", n)
			}
		})
	}

	// Positive control on the same sink: a 200 hands off, and it is the only call ever made.
	fake := newFakeGoTrue(t, http.StatusOK, good)
	requireRedirect(t, doVerifySink(t, fake.URL, verifyQuery, sink), verifiedLocation)
	requireOneHandOff(t, sink, coWant)
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

func TestSignIn_NoHandOffWithoutRegistration(t *testing.T) {
	sink := newRecSink(nil)
	for _, c := range []struct{ name, meta string }{
		{"empty metadata", `{}`},
		{"no user_metadata key", ``},
		{"registration null", `{"registration":null}`},
		{"consent without registration", `{` + coConsent + `}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := newTokenFake(t, http.StatusOK, coSession(coUser(c.meta)))
			rig := newSignInRigSink(t, fake.URL, nil, sink)

			requireCode(t, doSignInSink(t, rig))

			if n := len(sink.got()); n != 0 {
				t.Fatalf("sink saw %d calls for an account without registration, want 0", n)
			}
		})
	}

	// Positive control on the same sink.
	fake := newTokenFake(t, http.StatusOK, coSession(coUser(coMetaFull)))
	requireCode(t, doSignInSink(t, newSignInRigSink(t, fake.URL, nil, sink)))
	requireOneHandOff(t, sink, coWant)
}

func TestSignIn_NoHandOffWhenRefused(t *testing.T) {
	sink := newRecSink(nil)
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
		{"unreachable", 0, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			authURL := closedURL(t)
			if c.status != 0 {
				authURL = newTokenFake(t, c.status, c.body).URL
			}
			rig := newSignInRigSink(t, authURL, nil, sink)

			if rec := doSignInSink(t, rig); rec.Code == http.StatusOK {
				t.Fatalf("sign-in answered 200, want a refusal: %s", rec.Body.String())
			}

			if n := len(sink.got()); n != 0 {
				t.Fatalf("sink saw %d calls after a refused sign-in, want 0", n)
			}
		})
	}

	// Positive control on the same sink.
	fake := newTokenFake(t, http.StatusOK, coSession(coUser(coMetaFull)))
	requireCode(t, doSignInSink(t, newSignInRigSink(t, fake.URL, nil, sink)))
	requireOneHandOff(t, sink, coWant)
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
		h := VerifyHandler(fake.URL, siteURL(t), testClient(), slog.New(slog.DiscardHandler), sink)

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
	down := func(context.Context, int, RegistrantContact) error { return errors.New("notifications unavailable") }
	for _, c := range []struct {
		name          string
		entry         string
		first, second time.Duration
	}{
		{"verify, zero delays", "verify", 0, 0},
		{"sign-in, zero delays", "sign-in", 0, 0},
		{"verify, the injected delays are waited", "verify", 40 * time.Millisecond, 80 * time.Millisecond},
	} {
		t.Run(c.name, func(t *testing.T) {
			withHandOffDelays(t, c.first, c.second)
			sink := newRecSink(down)
			log, logs := newCaptureLog()

			switch c.entry {
			case "verify":
				fake := newFakeGoTrue(t, http.StatusOK, coSession(coUser(coMetaFull)))
				rec := httptest.NewRecorder()
				VerifyHandler(fake.URL, siteURL(t), testClient(), log, sink).ServeHTTP(rec, verifyRequest(context.Background(), verifyQuery))
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

func TestRegister_NeverHandsOff(t *testing.T) {
	// An autoconfirming GoTrue answers signup with a confirmed user and a session.
	fake := newFakeGoTrue(t, http.StatusOK, coSession(coUser(coMetaFull)))
	sink := newRecSink(nil)

	requirePending202(t, doRegister(t, fake.URL, nil, registerBodyWithAnswers(regEmail,
		map[string]any{"workspace_name": coWorkspace, "display_name": coDisplay, "marketing_consent_text": coConsentText})))

	if n := len(sink.got()); n != 0 {
		t.Fatalf("sink saw %d calls after register, want 0", n)
	}

	// Positive control: the same GoTrue and sink, the verify route hands off, so an empty sink above means something.
	requireRedirect(t, doVerifySink(t, fake.URL, verifyQuery, sink), verifiedLocation)
	requireOneHandOff(t, sink, coWant)
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
