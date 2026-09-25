package auth_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
)

// handoff is the sign-in and exchange handlers sharing one store, as the gateway mounts them.
type handoff struct {
	signIn, exchange http.Handler
}

func newHandoff(t *testing.T, base string) handoff {
	t.Helper()
	authURL, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	store := gateway.NewHandoffStore(gateway.HandoffTTL, time.Now)
	throttle := gateway.NewSignInThrottle(gateway.SignInMaxFailures, gateway.SignInMaxKeys, gateway.SignInWindow, time.Now)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return handoff{
		signIn:   gateway.SignInHandler(authURL, idpHTTP, store, throttle, log),
		exchange: gateway.ExchangeHandler(store),
	}
}

// newState mints a 43-character base64url state, as the app does (D25).
func newState(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func serveJSON(t *testing.T, h http.Handler, path string, body any) (int, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw))))
	return rec.Code, rec.Body.String()
}

func field(t *testing.T, body, name string) string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("body is not a JSON object of strings: %s", body)
	}
	return m[name]
}

func (h handoff) code(t *testing.T, u idpUser, state string) string {
	t.Helper()
	status, body := serveJSON(t, h.signIn, "/auth/sign-in", map[string]string{"email": u.email, "password": u.password, "state": state})
	if status != http.StatusOK {
		t.Fatalf("sign-in: status %d, body %s; want 200", status, body)
	}
	code := field(t, body, "code")
	if len(code) != 43 {
		t.Fatalf("sign-in code %q is %d characters, want 43", code, len(code))
	}
	return code
}

func (h handoff) redeem(t *testing.T, code, state string) (int, string) {
	t.Helper()
	return serveJSON(t, h.exchange, "/auth/exchange", map[string]string{"code": code, "state": state})
}

func TestIdP_SignInCodeRedeemsForAVerifiableToken(t *testing.T) {
	base := idpURL(t)
	u := signUp(t, superConn(t), base)
	h := newHandoff(t, base)
	s := newState(t)

	status, body := h.redeem(t, h.code(t, u, s), s)
	if status != http.StatusOK {
		t.Fatalf("exchange: status %d, body %s; want 200", status, body)
	}
	tok := field(t, body, "access_token")
	if tok == "" {
		t.Fatalf("exchange body has no access_token: %s", body)
	}
	id, err := idpVerifier(t, base).Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify the redeemed token: %v", err)
	}
	if id.Subject != u.id {
		t.Errorf("Subject = %q, want the GoTrue user id %q", id.Subject, u.id)
	}
}

func TestIdP_SignInCodeRedeemsOnce(t *testing.T) {
	base := idpURL(t)
	u := signUp(t, superConn(t), base)
	h := newHandoff(t, base)
	s := newState(t)
	code := h.code(t, u, s)

	want := []int{http.StatusOK, http.StatusBadRequest}
	got := make([]int, 0, len(want))
	for range want {
		status, _ := h.redeem(t, code, s)
		got = append(got, status)
	}
	if len(got) == 0 {
		t.Fatal("no exchange was attempted")
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("exchange %d: status %d, want %d", i+1, got[i], want[i])
		}
	}
}

func TestIdP_SignInCodeRefusesAWrongState(t *testing.T) {
	base := idpURL(t)
	u := signUp(t, superConn(t), base)
	h := newHandoff(t, base)
	s1, s2 := newState(t), newState(t)
	code := h.code(t, u, s1)

	steps := []struct {
		label, state string
	}{{"wrong state S2", s2}, {"right state S1 after the refusal", s1}}
	if len(steps) == 0 {
		t.Fatal("no exchange steps")
	}
	for _, st := range steps {
		status, body := h.redeem(t, code, st.state)
		if status != http.StatusBadRequest {
			t.Errorf("exchange with %s: status %d, want 400", st.label, status)
		}
		if strings.Contains(body, "eyJ") {
			t.Errorf("exchange with %s: body carries a JWT", st.label)
		}
	}
}

func TestIdP_SignInWrongPassword401(t *testing.T) {
	base := idpURL(t)
	u := signUp(t, superConn(t), base)
	h := newHandoff(t, base)

	status, body := serveJSON(t, h.signIn, "/auth/sign-in",
		map[string]string{"email": u.email, "password": u.password + "-wrong", "state": newState(t)})
	if status != http.StatusUnauthorized {
		t.Errorf("sign-in with a wrong password: status %d, want 401", status)
	}
	if body == "" {
		t.Fatal("sign-in with a wrong password: empty body")
	}
	if strings.Contains(body, "eyJ") {
		t.Errorf("sign-in with a wrong password: body carries a JWT")
	}
}
