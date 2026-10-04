package notifications

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type qaIntakeStore struct {
	mu          sync.Mutex
	registrants []RegistrantIntake
	demos       []DemoIntake
	meEmails    []string
	me          Contact
	meErr       error
}

func (f *qaIntakeStore) Registrant(_ context.Context, in RegistrantIntake) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registrants = append(f.registrants, in)
	return nil
}

func (f *qaIntakeStore) DemoRequest(_ context.Context, in DemoIntake) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.demos = append(f.demos, in)
	return nil
}

func (f *qaIntakeStore) Me(_ context.Context, email string) (Contact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.meEmails = append(f.meEmails, email)
	return f.me, f.meErr
}

func (f *qaIntakeStore) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.registrants) + len(f.demos) + len(f.meEmails)
}

func qaServe(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const (
	qaRegistrantsPath = "/internal/contacts/registrants"
	qaDemosPath       = "/internal/contacts/demo-requests"
)

func qaRegistrantBody(userID, email string, consent string) string {
	b := `{"user_id":"` + userID + `","email":"` + email + `","display_name":"Ada Lovelace","workspace_name":"Analytical Engines"`
	if consent != "" {
		b += `,"marketing_consent":` + consent
	}
	return b + `}`
}

func qaDemoBody(email, consentText string) string {
	b := `{"email":"` + email + `","name":"Grace Hopper","company":"Navy"`
	if consentText != "" {
		b += `,"marketing_consent_text":"` + consentText + `"`
	}
	return b + `}`
}

type qaIntakeRoute struct {
	h    http.Handler
	body string
}

// qaIntakeRoutes pairs each intake route with a handler over store and a valid body.
func qaIntakeRoutes(store IntakeStore, log *qaLogSink) map[string]qaIntakeRoute {
	return map[string]qaIntakeRoute{
		qaRegistrantsPath: {RegistrantsHandler(store, log.logger()), qaRegistrantBody(uuid.NewString(), "ada@corp.example", "")},
		qaDemosPath:       {DemoRequestsHandler(store, log.logger()), qaDemoBody("grace@navy.example", "")},
	}
}

func TestIntake_RegistrantAccepted(t *testing.T) {
	store := &qaIntakeStore{}
	h := RegistrantsHandler(store, (&qaLogSink{}).logger())
	uid := uuid.NewString()

	consent := `{"text":"` + consentText + `","at":"2026-10-04T10:30:00Z"}`
	if rec := qaServe(h, "POST", qaRegistrantsPath, qaRegistrantBody(uid, "Ada@Corp.example", consent), nil); rec.Code != http.StatusAccepted {
		t.Fatalf("ticked registrant: status %d, want 202 (body %q)", rec.Code, rec.Body)
	}
	if rec := qaServe(h, "POST", qaRegistrantsPath, qaRegistrantBody(uid, "bob@corp.example", ""), nil); rec.Code != http.StatusAccepted {
		t.Fatalf("unticked registrant: status %d, want 202 (body %q)", rec.Code, rec.Body)
	}

	if len(store.registrants) != 2 {
		t.Fatalf("Store.Registrant called %d times, want 2", len(store.registrants))
	}
	got := store.registrants[0]
	if got.UserID != uid || got.Email != "Ada@Corp.example" || got.DisplayName != "Ada Lovelace" || got.WorkspaceName != "Analytical Engines" {
		t.Errorf("ticked intake = %+v, want the body's fields, the email unchanged (Postgres normalises it)", got)
	}
	if got.ConsentText != consentText || !got.ConsentAt.Equal(time.Date(2026, 10, 4, 10, 30, 0, 0, time.UTC)) {
		t.Errorf("consent = %q at %v, want the sentence and the stated time", got.ConsentText, got.ConsentAt)
	}
	if plain := store.registrants[1]; plain.ConsentText != "" || !plain.ConsentAt.IsZero() {
		t.Errorf("unticked intake carries consent %q at %v", plain.ConsentText, plain.ConsentAt)
	}
}

func TestIntake_DemoAccepted(t *testing.T) {
	store := &qaIntakeStore{}
	h := DemoRequestsHandler(store, (&qaLogSink{}).logger())

	if rec := qaServe(h, "POST", qaDemosPath, qaDemoBody("grace@navy.example", consentText), nil); rec.Code != http.StatusAccepted {
		t.Fatalf("ticked demo request: status %d, want 202 (body %q)", rec.Code, rec.Body)
	}
	if rec := qaServe(h, "POST", qaDemosPath, qaDemoBody("hedy@navy.example", ""), nil); rec.Code != http.StatusAccepted {
		t.Fatalf("unticked demo request: status %d, want 202 (body %q)", rec.Code, rec.Body)
	}

	if len(store.demos) != 2 {
		t.Fatalf("Store.DemoRequest called %d times, want 2", len(store.demos))
	}
	want := DemoIntake{Email: "grace@navy.example", Name: "Grace Hopper", Company: "Navy", ConsentText: consentText}
	if store.demos[0] != want {
		t.Errorf("ticked intake = %+v, want %+v", store.demos[0], want)
	}
	if store.demos[1].ConsentText != "" || store.demos[1].Email != "hedy@navy.example" {
		t.Errorf("unticked intake = %+v, want no consent", store.demos[1])
	}
}

func TestIntake_MalformedIs400(t *testing.T) {
	bad := map[string]string{
		"not json":      `{"email":`,
		"empty body":    ``,
		"empty object":  `{}`,
		"blank email":   `{"email":"","name":"Grace Hopper","company":"Navy"}`,
		"wrong type":    `[1,2]`,
		"email a float": `{"email":1.5}`,
	}
	store := &qaIntakeStore{}
	for path, r := range qaIntakeRoutes(store, &qaLogSink{}) {
		for name, body := range bad {
			t.Run(strings.TrimPrefix(path, "/internal/contacts/")+"/"+name, func(t *testing.T) {
				if rec := qaServe(r.h, "POST", path, body, nil); rec.Code != http.StatusBadRequest {
					t.Errorf("status %d, want 400 (body %q)", rec.Code, rec.Body)
				}
			})
		}
	}
	if n := store.calls(); n != 0 {
		t.Fatalf("the store was called %d times for malformed bodies, want 0", n)
	}

	// The valid body for each route is accepted, so the 400s above are the bodies' fault.
	for path, r := range qaIntakeRoutes(store, &qaLogSink{}) {
		if rec := qaServe(r.h, "POST", path, r.body, nil); rec.Code != http.StatusAccepted {
			t.Errorf("%s: valid body status %d, want 202", path, rec.Code)
		}
	}
	if n := store.calls(); n != 2 {
		t.Errorf("the store was called %d times for the two valid bodies, want 2", n)
	}
}

// A proxied request always carries X-User-ID; the gateway's own call never does.
func TestIntake_ProxiedRequestIs404(t *testing.T) {
	store := &qaIntakeStore{}
	for path, r := range qaIntakeRoutes(store, &qaLogSink{}) {
		for name, id := range map[string]string{"opaque id": "u1", "uuid": uuid.NewString()} {
			t.Run(strings.TrimPrefix(path, "/internal/contacts/")+"/"+name, func(t *testing.T) {
				if rec := qaServe(r.h, "POST", path, r.body, map[string]string{"X-User-ID": id}); rec.Code != http.StatusNotFound {
					t.Errorf("status %d, want 404 (body %q)", rec.Code, rec.Body)
				}
			})
		}
	}
	if n := store.calls(); n != 0 {
		t.Fatalf("the store was called %d times for proxied requests, want 0", n)
	}

	for path, r := range qaIntakeRoutes(store, &qaLogSink{}) {
		if rec := qaServe(r.h, "POST", path, r.body, nil); rec.Code != http.StatusAccepted {
			t.Errorf("%s: the same body without X-User-ID: status %d, want 202", path, rec.Code)
		}
	}
	if n := store.calls(); n != 2 {
		t.Errorf("the store was called %d times for the two direct requests, want 2", n)
	}
}

func TestIntake_UnparseableConsentTimeDropsConsent(t *testing.T) {
	store := &qaIntakeStore{}
	sink := &qaLogSink{}
	h := RegistrantsHandler(store, sink.logger())
	uid, email := uuid.NewString(), "zelda.quux@corp.example"

	bad := `{"text":"` + consentText + `","at":"soon"}`
	if rec := qaServe(h, "POST", qaRegistrantsPath, qaRegistrantBody(uid, email, bad), nil); rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: the registrant is still handed on, without consent", rec.Code)
	}
	if len(store.registrants) != 1 {
		t.Fatalf("Store.Registrant called %d times, want 1", len(store.registrants))
	}
	if got := store.registrants[0]; got.ConsentText != "" || !got.ConsentAt.IsZero() || got.Email != email {
		t.Errorf("intake = %+v, want the registrant with no consent", got)
	}
	var warned bool
	for _, l := range sink.at(slog.LevelWarn) {
		warned = warned || strings.Contains(l.Text, uid)
	}
	if !warned {
		t.Errorf("no WARN line names user %s; lines: %+v", uid, sink.snapshot())
	}
	qaRequireNoLeak(t, nil, []*qaLogSink{sink}, email, "Zelda")

	// A parseable time keeps the consent.
	good := `{"text":"` + consentText + `","at":"2026-10-04T10:30:00Z"}`
	qaServe(h, "POST", qaRegistrantsPath, qaRegistrantBody(uuid.NewString(), "bob@corp.example", good), nil)
	if len(store.registrants) != 2 || store.registrants[1].ConsentText != consentText {
		t.Errorf("a parseable consent time lost the consent: %+v", store.registrants)
	}
}

func TestIntake_MeReadsTheTokenEmail(t *testing.T) {
	delivered := time.Date(2026, 10, 4, 11, 0, 0, 123000000, time.UTC)
	mode := "fake"
	store := &qaIntakeStore{me: Contact{
		Email: "ada@corp.example", Tags: []string{"registered", "demo request"}, MarketingEligible: true, ResendApplies: true,
		HubSpotDeliveredAt: &delivered, Mode: &mode,
	}}
	h := MeHandler(store, (&qaLogSink{}).logger())
	headers := map[string]string{"X-User-Email": "Ada@Corp.example", "X-User-ID": uuid.NewString(), "X-Tenant-ID": uuid.NewString()}

	rec := qaServe(h, "GET", "/v1/contacts/me", "", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 (body %q)", rec.Code, rec.Body)
	}
	if !slices.Equal(store.meEmails, []string{"Ada@Corp.example"}) {
		t.Fatalf("Store.Me called with %q, want the X-User-Email value unchanged", store.meEmails)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body, err)
	}
	if keys := slices.Sorted(maps.Keys(body)); !slices.Equal(keys, []string{"email", "hubspot", "marketing_eligible", "mode", "resend", "tags"}) {
		t.Errorf("body keys = %v, want exactly email, hubspot, marketing_eligible, mode, resend, tags", keys)
	}
	if body["email"] != "ada@corp.example" || body["marketing_eligible"] != true || body["mode"] != "fake" {
		t.Errorf("body = %v, want the contact's email, marketing_eligible true, mode fake", body)
	}
	if tags, _ := body["tags"].([]any); len(tags) != 2 || tags[0] != "registered" || tags[1] != "demo request" {
		t.Errorf("tags = %v, want [registered, demo request]", body["tags"])
	}
	hubspot, _ := body["hubspot"].(map[string]any)
	at, _ := hubspot["delivered_at"].(string)
	if parsed, err := time.Parse(time.RFC3339Nano, at); err != nil || !parsed.Equal(delivered) {
		t.Errorf("hubspot = %v, want delivered_at %v in RFC 3339", hubspot, delivered)
	}
	resend, _ := body["resend"].(map[string]any)
	if v, has := resend["delivered_at"]; !has || v != nil || resend["applies"] != true {
		t.Errorf("resend = %v, want applies true and delivered_at null", resend)
	}

	// Nothing delivered yet: null times and a null mode, not absent keys.
	store.me = Contact{Email: "ada@corp.example", Tags: []string{"demo request"}}
	rec = qaServe(h, "GET", "/v1/contacts/me", "", headers)
	var fresh map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &fresh); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %q (%v), want 200 JSON", rec.Code, rec.Body, err)
	}
	if v, has := fresh["mode"]; !has || v != nil {
		t.Errorf("mode = %v (present %v), want null", v, has)
	}
	if hs, _ := fresh["hubspot"].(map[string]any); hs == nil || hs["delivered_at"] != nil {
		t.Errorf("hubspot = %v, want delivered_at null", fresh["hubspot"])
	}

	store.meErr = ErrNotFound
	if rec := qaServe(h, "GET", "/v1/contacts/me", "", headers); rec.Code != http.StatusNotFound {
		t.Errorf("unknown contact: status %d, want 404", rec.Code)
	}
}
