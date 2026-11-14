package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	vhState      = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-abcde" // 43 base64url, stateShape
	vhOtherState = "ZyXwVuTsRqPoNmLkJiHgFeDcBa9876543210-_EDCBA"
	vhCodePrefix = siteURLValue + "/?verified=1&handoff="
)

var vhCodeShape = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func testHandoffStore() *HandoffStore { return NewHandoffStore(HandoffTTL, time.Now) }

func nopLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// vhCode requires the success Location and returns its code.
func vhCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, vhCodePrefix) {
		t.Fatalf("answer = %d Location %q, want 303 %s<code>", rec.Code, loc, vhCodePrefix)
	}
	code := strings.TrimPrefix(loc, vhCodePrefix)
	if !vhCodeShape.MatchString(code) {
		t.Fatalf("code %q is not 43 base64url characters", code)
	}
	return code
}

func vhExchange(store *HandoffStore, code, state string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"code": code, "state": state})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/exchange", strings.NewReader(string(body)))
	ExchangeHandler(store, nil).ServeHTTP(rec, req)
	return rec
}

func vhLogRecords(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(raw) {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// confirmHandler is the registrant's set-password POST against a canned GoTrue; every GoTrue call it makes gets the one answer.
func confirmHandler(t *testing.T, authURL, siteURL *url.URL, client *http.Client, log *slog.Logger, sink ContactSink, store Handoffs, events AccountEvents, perIP *SignInThrottle, enforce bool) http.Handler {
	t.Helper()
	th := NewSignInThrottle("sign-in", SignInMaxFailures, SignInMaxKeys, SignInWindow, newTestClock().Now)
	return InvitationPasswordHandler(authURL, siteURL, client, NewSessionChecker(authURL, client, time.Now, log), th, sink, log, store, events, perIP, enforce)
}
