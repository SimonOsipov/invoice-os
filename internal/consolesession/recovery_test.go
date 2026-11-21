package consolesession_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/consolesession"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth/gotrueadmin"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth/iaptest"
)

const (
	route    = "/internal/console-sessions/recovery-token"
	opsAud   = "/projects/111111111111/global/backendServices/1111111111111111111"
	otherAud = "/projects/111111111111/global/backendServices/3333333333333333333"
	staffMsg = "no staff account"
	downMsg  = "console sign-in is unavailable"
)

// linker stands in for GoTrue and records the addresses it was asked about.
type linker struct {
	mu     sync.Mutex
	emails []string
	link   gotrueadmin.RecoveryLink
	err    error
}

func (l *linker) GenerateRecoveryLink(_ context.Context, email string) (gotrueadmin.RecoveryLink, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.emails = append(l.emails, email)
	return l.link, l.err
}

func (l *linker) calls() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.emails...)
}

type rig struct {
	signer *iaptest.Signer
	h      http.Handler
	l      *linker
	log    *bytes.Buffer
	now    time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	signer, jwks := iaptest.NewSigner(t)
	v, err := auth.NewIAPVerifier(auth.IAPConfig{Audiences: []string{opsAud}, JWKSURL: jwks})
	if err != nil {
		t.Fatalf("NewIAPVerifier: %v", err)
	}
	r := &rig{signer: signer, l: &linker{link: gotrueadmin.RecoveryLink{TokenHash: "HASH-ONE", Confirmed: true}}, log: &bytes.Buffer{}, now: time.Now()}
	logger := slog.New(slog.NewTextHandler(r.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r.h = consolesession.RecoveryTokenHandler(v, r.l, func() time.Time { return r.now }, logger)
	return r
}

// assertionFor signs an IAP assertion for email with signer.
func assertionFor(s *iaptest.Signer, aud, email string) string {
	now := time.Now()
	return s.Sign(map[string]any{
		"iss": auth.IAPIssuer, "aud": aud, "sub": "accounts.google.com:118273645546372819", "email": email,
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	})
}

func (r *rig) good(email string) string { return assertionFor(r.signer, opsAud, email) }

func (r *rig) do(method, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, route, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

func (r *rig) post(t *testing.T, fields map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return r.do(http.MethodPost, "application/json", string(b))
}

func (r *rig) postAssertion(t *testing.T, assertion string) *httptest.ResponseRecorder {
	t.Helper()
	return r.post(t, map[string]any{"assertion": assertion})
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("answer %q is not a JSON object: %v", rec.Body.String(), err)
	}
	return m
}

func wantAnswer(t *testing.T, rec *httptest.ResponseRecorder, status int, want map[string]any) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d (%s), want %d", rec.Code, rec.Body.String(), status)
	}
	if got := decode(t, rec); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("answer = %v, want exactly %v", got, want)
	}
}

func TestRecoveryToken_AVerifiedConfirmedAccountGetsAHash(t *testing.T) {
	r := newRig(t)
	rec := r.postAssertion(t, r.good("sam@ascomply.test"))
	wantAnswer(t, rec, http.StatusOK, map[string]any{"token_hash": "HASH-ONE"})
	if got := r.l.calls(); len(got) != 1 || got[0] != "sam@ascomply.test" {
		t.Errorf("linker asked about %v, want exactly the assertion's address", got)
	}
}

func TestRecoveryToken_AnUnverifiedAssertionMakesNoCall(t *testing.T) {
	r := newRig(t)
	stranger, _ := iaptest.NewSigner(t) // same kid, another key
	cases := []struct {
		name   string
		fields map[string]any
	}{
		{"forged signature", map[string]any{"assertion": assertionFor(stranger, opsAud, "sam@ascomply.test")}},
		{"wrong audience", map[string]any{"assertion": assertionFor(r.signer, otherAud, "sam@ascomply.test")}},
		{"bad assertion with an email in the body", map[string]any{"assertion": "not.a.jwt", "email": "victim@ascomply.test"}},
		{"forged signature with an email in the body", map[string]any{"assertion": assertionFor(stranger, opsAud, "sam@ascomply.test"), "email": "victim@ascomply.test"}},
		{"no assertion", map[string]any{"email": "victim@ascomply.test"}},
		{"empty assertion", map[string]any{"assertion": ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantAnswer(t, r.post(t, tc.fields), http.StatusUnauthorized, map[string]any{"error": "no verified Google identity"})
			if got := r.l.calls(); len(got) != 0 {
				t.Errorf("linker called %v, want no call", got)
			}
		})
	}

	// Control: the same rig admits a verified assertion, so the refusals above are the verifier's.
	if rec := r.postAssertion(t, r.good("sam@ascomply.test")); rec.Code != http.StatusOK {
		t.Errorf("control: verified assertion answered %d, want 200", rec.Code)
	}
}

func TestRecoveryToken_IgnoresAnEmailInTheBody(t *testing.T) {
	r := newRig(t)
	rec := r.post(t, map[string]any{"assertion": r.good("a@ascomply.test"), "email": "b@ascomply.test"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if got := r.l.calls(); len(got) != 1 || got[0] != "a@ascomply.test" {
		t.Errorf("linker asked about %v, want only a@ascomply.test", got)
	}
}

func TestRecoveryToken_NoAccountUnconfirmedOrBannedIs403(t *testing.T) {
	r := newRig(t)
	hourAhead, hourAgo := r.now.Add(time.Hour), r.now.Add(-time.Hour)
	refused := []struct {
		name string
		link gotrueadmin.RecoveryLink
		err  error
	}{
		{"no account", gotrueadmin.RecoveryLink{}, gotrueadmin.ErrNoAccount},
		{"no account, wrapped", gotrueadmin.RecoveryLink{}, fmt.Errorf("generate_link: %w", gotrueadmin.ErrNoAccount)},
		{"unconfirmed", gotrueadmin.RecoveryLink{TokenHash: "HASH-UNCONFIRMED", Confirmed: false}, nil},
		{"banned for another hour", gotrueadmin.RecoveryLink{TokenHash: "HASH-BANNED", Confirmed: true, BannedUntil: &hourAhead}, nil},
		{"unconfirmed and banned", gotrueadmin.RecoveryLink{TokenHash: "HASH-BOTH", BannedUntil: &hourAhead}, nil},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			r.l.link, r.l.err = tc.link, tc.err
			rec := r.postAssertion(t, r.good("sam@ascomply.test"))
			wantAnswer(t, rec, http.StatusForbidden, map[string]any{"error": staffMsg})
			if tc.link.TokenHash != "" && strings.Contains(rec.Body.String(), tc.link.TokenHash) {
				t.Errorf("answer %q carries the token hash", rec.Body.String())
			}
		})
	}

	t.Run("a ban that lapsed an hour ago opens", func(t *testing.T) {
		r.l.link, r.l.err = gotrueadmin.RecoveryLink{TokenHash: "HASH-LAPSED", Confirmed: true, BannedUntil: &hourAgo}, nil
		wantAnswer(t, r.postAssertion(t, r.good("sam@ascomply.test")), http.StatusOK, map[string]any{"token_hash": "HASH-LAPSED"})
	})
}

func TestRecoveryToken_UpstreamFailureIs502AndLogsNoIdentity(t *testing.T) {
	r := newRig(t)
	const email = "sam@ascomply.test"
	good, bad := r.good(email), "not.a.jwt"
	var secrets []string
	for _, a := range []string{good, bad} {
		secrets = append(secrets, a)
		for _, seg := range strings.Split(a, ".") {
			if len(seg) > 8 {
				secrets = append(secrets, seg)
			}
		}
	}
	secrets = append(secrets, email, "HASH-FAILING", "HASH-OK", "HASH-BANNED")

	scan := func(t *testing.T) {
		t.Helper()
		for _, s := range secrets {
			if strings.Contains(r.log.String(), s) {
				t.Errorf("log carries %q:\n%s", s, r.log.String())
			}
		}
	}
	failures := []struct {
		name string
		err  error
	}{
		{"GoTrue 500", &gotrueadmin.StatusError{Status: 500}},
		{"GoTrue 401", &gotrueadmin.StatusError{Status: 401}},
		{"transport failure", &gotrueadmin.StatusError{Status: 0}},
		{"other error", errors.New("dial tcp: connection refused")},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			r.log.Reset()
			r.l.link, r.l.err = gotrueadmin.RecoveryLink{TokenHash: "HASH-FAILING", Confirmed: true}, tc.err
			rec := r.postAssertion(t, good)
			wantAnswer(t, rec, http.StatusBadGateway, map[string]any{"error": downMsg})
			if r.log.Len() == 0 {
				t.Error("a 502 left no log line")
			}
			scan(t)
		})
	}

	// The other outcomes through the same logger, then one more 502 so the buffer holds each path.
	hourAhead := r.now.Add(time.Hour)
	r.l.link, r.l.err = gotrueadmin.RecoveryLink{TokenHash: "HASH-OK", Confirmed: true}, nil
	r.postAssertion(t, good)
	r.l.link = gotrueadmin.RecoveryLink{TokenHash: "HASH-BANNED", Confirmed: true, BannedUntil: &hourAhead}
	r.postAssertion(t, good)
	r.l.err = gotrueadmin.ErrNoAccount
	r.postAssertion(t, good)
	r.postAssertion(t, bad)

	r.l.link, r.l.err = gotrueadmin.RecoveryLink{TokenHash: "HASH-FAILING", Confirmed: true}, &gotrueadmin.StatusError{Status: 500}
	r.postAssertion(t, good)
	scan(t)
}

func TestRecoveryToken_BodyLimitsAndMethod(t *testing.T) {
	r := newRig(t)
	good := r.good("sam@ascomply.test")
	valid, _ := json.Marshal(map[string]any{"assertion": good})
	cases := []struct {
		name, method, contentType, body string
		want                            int
	}{
		{"GET", http.MethodGet, "", "", http.StatusMethodNotAllowed},
		{"PUT with a valid body", http.MethodPut, "application/json", string(valid), http.StatusMethodNotAllowed},
		{"form body", http.MethodPost, "application/x-www-form-urlencoded", "assertion=" + good, http.StatusUnsupportedMediaType},
		{"text body with a valid assertion", http.MethodPost, "text/plain", string(valid), http.StatusUnsupportedMediaType},
		{"no content type", http.MethodPost, "", string(valid), http.StatusUnsupportedMediaType},
		{"assertion at the gateway cap is read, then refused as unverifiable", http.MethodPost, "application/json", `{"assertion":"` + strings.Repeat("a", auth.MaxIAPAssertionBytes) + `"}`, http.StatusUnauthorized},
		{"9 KiB body", http.MethodPost, "application/json", `{"assertion":"` + strings.Repeat("a", 9<<10) + `"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rec := r.do(tc.method, tc.contentType, tc.body); rec.Code != tc.want {
				t.Errorf("status = %d (%s), want %d", rec.Code, rec.Body.String(), tc.want)
			}
			if got := r.l.calls(); len(got) != 0 {
				t.Errorf("linker called %v, want no call", got)
			}
		})
	}

	// Control: the valid body is admitted by the same rig.
	if rec := r.do(http.MethodPost, "application/json", string(valid)); rec.Code != http.StatusOK {
		t.Errorf("control: valid body answered %d, want 200", rec.Code)
	}
}
