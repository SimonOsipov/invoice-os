package main

import (
	"context"
	"encoding/json"
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

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/notifications"
)

const (
	cwUserID  = "7f3c2a1e-0b7d-4f51-9a0e-5d1c2b3a4e5f"
	cwEmail   = "new@corp.example"
	cwText    = "I agree to receive product news from ASComply."
	cwAt      = "2026-09-24T10:00:00Z"
	cwSession = `{"access_token":"at","refresh_token":"rt","user":{"id":"` + cwUserID + `","email":"` + cwEmail + `","user_metadata":{` +
		`"registration":{"workspace_name":"Quillworks Ltd","display_name":"Zelda Quill"},` +
		`"marketing_consent":{"text":"` + cwText + `","at":"` + cwAt + `"}}}}`
)

var cwWant = gateway.RegistrantContact{
	UserID: cwUserID, Email: cwEmail, DisplayName: "Zelda Quill", WorkspaceName: "Quillworks Ltd",
	Consent: &gateway.MarketingConsent{Text: cwText, At: cwAt},
}

// chanSink delivers each registrant on a channel.
type chanSink struct {
	got chan gateway.RegistrantContact
}

func newChanSink() *chanSink { return &chanSink{got: make(chan gateway.RegistrantContact, 8)} }

func (s *chanSink) Registrant(_ context.Context, c gateway.RegistrantContact) error {
	s.got <- c
	return nil
}

func (s *chanSink) DemoRequest(context.Context, gateway.DemoRequest) error { return nil }

func (s *chanSink) wait(t *testing.T) gateway.RegistrantContact {
	t.Helper()
	select {
	case c := <-s.got:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("the sink was not called within 5s")
		return gateway.RegistrantContact{}
	}
}

func goTrueAnswering(t *testing.T, body string) *url.URL {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestRegistrationHandlers_VerifyHandsOffToTheSink(t *testing.T) {
	site, _ := url.Parse("https://site.example")
	sink := newChanSink()
	verify := handoffVerify(goTrueAnswering(t, cwSession), site, sink)

	rec := serveForm(verify, "/auth/verify", "token=tok&type=signup")

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "https://site.example/?verified=1" {
		t.Fatalf("verify answered %d Location %q, want 303 to /?verified=1", rec.Code, rec.Header().Get("Location"))
	}
	if got := sink.wait(t); !reflect.DeepEqual(got, cwWant) {
		t.Errorf("hand-off = %+v, want %+v", got, cwWant)
	}
}

func TestHandoffHandlers_SignInHandsOffToTheSink(t *testing.T) {
	sink := newChanSink()
	log := slog.New(slog.DiscardHandler)
	h := handoffHandlers(goTrueAnswering(t, cwSession), nil, gateway.NewSessionChecker(nil, nil, time.Now, log), log, sink)

	rec := httptest.NewRecorder()
	h.SignIn.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/sign-in",
		strings.NewReader(`{"email":"`+cwEmail+`","password":"pw","state":"`+handoffState+`"}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("sign-in answered %d: %s", rec.Code, rec.Body.String())
	}
	if got := sink.wait(t); !reflect.DeepEqual(got, cwWant) {
		t.Errorf("hand-off = %+v, want %+v", got, cwWant)
	}
}

// intakeStore records what notifications' real intake handlers decode.
type intakeStore struct {
	mu   sync.Mutex
	reg  []notifications.RegistrantIntake
	demo []notifications.DemoIntake
}

func (s *intakeStore) Registrant(_ context.Context, in notifications.RegistrantIntake) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reg = append(s.reg, in)
	return nil
}

func (s *intakeStore) DemoRequest(_ context.Context, in notifications.DemoIntake) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.demo = append(s.demo, in)
	return nil
}

func (s *intakeStore) Me(context.Context, string) (notifications.Contact, error) {
	return notifications.Contact{}, notifications.ErrNotFound
}

// The gateway's hand-off body must be exactly what notifications' own intake handlers read.
func TestContactSink_BodyIsWhatNotificationsIntakeReads(t *testing.T) {
	const token = "gw-token-value"
	store := &intakeStore{}
	log := slog.New(slog.DiscardHandler)
	mux := http.NewServeMux()
	mux.Handle("POST /internal/contacts/registrants", notifications.RegistrantsHandler(store, log))
	mux.Handle("POST /internal/contacts/demo-requests", notifications.DemoRequestsHandler(store, log))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Gateway-Token") != token {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	sink := gateway.NewHTTPContactSink(base, &http.Client{Timeout: 5 * time.Second}, token)
	ctx := context.Background()

	if err := sink.Registrant(ctx, cwWant); err != nil {
		t.Fatalf("Registrant with consent: %v", err)
	}
	if err := sink.Registrant(ctx, gateway.RegistrantContact{UserID: cwUserID, Email: cwEmail}); err != nil {
		t.Fatalf("Registrant without consent: %v", err)
	}
	if err := sink.DemoRequest(ctx, gateway.DemoRequest{Email: cwEmail, Name: "Zelda Quill", Company: "Quillworks Ltd", MarketingConsentText: cwText}); err != nil {
		t.Fatalf("DemoRequest: %v", err)
	}

	at, _ := time.Parse(time.RFC3339, cwAt)
	wantReg := []notifications.RegistrantIntake{
		{UserID: cwUserID, Email: cwEmail, DisplayName: "Zelda Quill", WorkspaceName: "Quillworks Ltd", ConsentText: cwText, ConsentAt: at},
		{UserID: cwUserID, Email: cwEmail},
	}
	if len(store.reg) != len(wantReg) {
		t.Fatalf("intake stored %d registrants, want %d", len(store.reg), len(wantReg))
	}
	for i, want := range wantReg {
		got := store.reg[i]
		if got.UserID != want.UserID || got.Email != want.Email || got.DisplayName != want.DisplayName ||
			got.WorkspaceName != want.WorkspaceName || got.ConsentText != want.ConsentText || !got.ConsentAt.Equal(want.ConsentAt) {
			t.Errorf("registrant %d = %+v, want %+v", i, got, want)
		}
	}
	wantDemo := notifications.DemoIntake{Email: cwEmail, Name: "Zelda Quill", Company: "Quillworks Ltd", ConsentText: cwText}
	if len(store.demo) != 1 || store.demo[0] != wantDemo {
		t.Errorf("intake stored demos %+v, want [%+v]", store.demo, wantDemo)
	}
}

// What the demo route forwards is exactly what notifications' own intake reads: the four contract fields,
// none of the extras a caller adds, and none of the caller's headers.
func TestDemoRequest_ForwardedBodyPassesNotificationsIntake(t *testing.T) {
	const token = "gw-token-value"
	type seen struct {
		path   string
		header http.Header
		body   []byte
	}
	var (
		mu   sync.Mutex
		reqs []seen
	)
	store := &intakeStore{}
	log := slog.New(slog.DiscardHandler)
	mux := http.NewServeMux()
	mux.Handle("POST /internal/contacts/demo-requests", notifications.DemoRequestsHandler(store, log))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(b)))
		mu.Lock()
		reqs = append(reqs, seen{r.URL.Path, r.Header.Clone(), b})
		mu.Unlock()
		if r.Header.Get("X-Gateway-Token") != token {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	sink := gateway.NewHTTPContactSink(base, &http.Client{Timeout: 5 * time.Second}, token)
	site, _ := url.Parse("https://site.example")
	reg := registrationHandlers(goTrueAnswering(t, cwSession), site, 0, log, sink)

	const extras = `"user_id":"` + cwUserID + `","marketing_consent":{"text":"forged","at":"2026-01-01T00:00:00Z"},"demo_requested_at":"2026-01-01T00:00:00Z","tags":["registered"],"version":9`
	for _, tc := range []struct {
		name, body string
		want       notifications.DemoIntake
	}{
		{"unticked", `{"email":"  ` + cwEmail + ` ","name":" Zelda Quill ","company":" Quillworks Ltd ",` + extras + `}`,
			notifications.DemoIntake{Email: cwEmail, Name: "Zelda Quill", Company: "Quillworks Ltd"}},
		{"ticked", `{"email":"` + cwEmail + `","name":"Zelda Quill","company":"Quillworks Ltd","marketing_consent_text":"` + cwText + `",` + extras + `}`,
			notifications.DemoIntake{Email: cwEmail, Name: "Zelda Quill", Company: "Quillworks Ltd", ConsentText: cwText}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			reqs = nil
			mu.Unlock()
			before := len(store.demo)

			req := httptest.NewRequest(http.MethodPost, "/contacts/demo-request", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			// A caller cannot make notifications see a proxied request, or borrow the gateway's token.
			req.Header.Set("X-User-ID", cwUserID)
			req.Header.Set("X-Gateway-Token", "attacker")
			rec := httptest.NewRecorder()
			reg.DemoRequest.ServeHTTP(rec, req)

			if rec.Code != http.StatusAccepted || strings.TrimSpace(rec.Body.String()) != `{"status":"accepted"}` {
				t.Fatalf("demo request answered %d %s, want 202 accepted", rec.Code, rec.Body.String())
			}
			mu.Lock()
			defer mu.Unlock()
			if len(reqs) != 1 {
				t.Fatalf("notifications saw %d requests, want 1", len(reqs))
			}
			got := reqs[0]
			if got.path != "/internal/contacts/demo-requests" {
				t.Errorf("path = %q, want /internal/contacts/demo-requests", got.path)
			}
			if got.header.Get("X-Gateway-Token") != token || got.header.Get("X-User-ID") != "" {
				t.Errorf("forwarded headers X-Gateway-Token=%q X-User-ID=%q, want the gateway token and no user id", got.header.Get("X-Gateway-Token"), got.header.Get("X-User-ID"))
			}
			var keys map[string]json.RawMessage
			if err := json.Unmarshal(got.body, &keys); err != nil {
				t.Fatalf("forwarded body %q: %v", got.body, err)
			}
			for k, v := range keys {
				switch k {
				case "email", "name", "company":
				case "marketing_consent_text":
					if string(v) != `""` && tc.want.ConsentText == "" {
						t.Errorf("unticked request forwards marketing_consent_text = %s", v)
					}
				default:
					t.Errorf("forwarded body carries %q: %s", k, got.body)
				}
			}
			if len(store.demo) != before+1 || store.demo[before] != tc.want {
				t.Errorf("intake stored %+v, want one more: %+v", store.demo, tc.want)
			}
		})
	}
}

// The demo route needs neither GoTrue nor the site URL, so an unconfigured registration does not close it.
func TestRegistrationHandlers_DemoRequestWorksWithoutAuthConfig(t *testing.T) {
	sink := &demoRecSink{}
	reg := registrationHandlers(nil, nil, 0, slog.New(slog.DiscardHandler), sink)

	ctl := httptest.NewRecorder()
	reg.Register.ServeHTTP(ctl, httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(`{}`)))
	if ctl.Code != http.StatusServiceUnavailable {
		t.Fatalf("control: unconfigured register = %d, want 503", ctl.Code)
	}
	if reg.DemoRequest == nil {
		t.Fatal("an unconfigured registration has no DemoRequest handler")
	}
	req := httptest.NewRequest(http.MethodPost, "/contacts/demo-request", strings.NewReader(`{"email":"ada@corp.example","name":"Ada","company":"Acme"}`))
	rec := httptest.NewRecorder()
	reg.DemoRequest.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("demo request without auth config = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if got := sink.calls(); len(got) != 1 {
		t.Errorf("sink saw %+v, want one call", got)
	}
}
