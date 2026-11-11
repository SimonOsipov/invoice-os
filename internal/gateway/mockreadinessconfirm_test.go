package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMockReadinessConfirmToken_AnswersPerOutcome(t *testing.T) {
	token := strings.Repeat("a", 43)
	valid := `{"email":"Owner@Example.io","token":"` + token + `"}`
	for _, c := range []struct {
		name, method, body string
		found              bool
		err                error
		want               int
		calls              int
	}{
		{"replaced", http.MethodPost, valid, true, nil, http.StatusNoContent, 1},
		{"no live held row", http.MethodPost, valid, false, nil, http.StatusNotFound, 1},
		{"store error", http.MethodPost, valid, false, errors.New("db down"), http.StatusBadGateway, 1},
		{"token of 42 characters", http.MethodPost, `{"email":"a@b.io","token":"` + token[:42] + `"}`, true, nil, http.StatusBadRequest, 0},
		{"no email", http.MethodPost, `{"token":"` + token + `"}`, true, nil, http.StatusBadRequest, 0},
		{"blank email", http.MethodPost, `{"email":"  ","token":"` + token + `"}`, true, nil, http.StatusBadRequest, 0},
		{"malformed JSON", http.MethodPost, `{`, true, nil, http.StatusBadRequest, 0},
		{"GET", http.MethodGet, "", true, nil, http.StatusMethodNotAllowed, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			var gotEmail, gotToken string
			calls := 0
			set := func(_ context.Context, email, tok string) (bool, error) {
				calls++
				gotEmail, gotToken = email, tok
				return c.found, c.err
			}
			log, buf := captureLog()
			rec := httptest.NewRecorder()
			MockReadinessConfirmTokenHandler(set, log).ServeHTTP(rec, httptest.NewRequest(c.method, "/auth/mock/readiness-confirm-token", strings.NewReader(c.body)))
			if rec.Code != c.want || calls != c.calls {
				t.Fatalf("status %d with %d setter call(s), want %d with %d: %s", rec.Code, calls, c.want, c.calls, rec.Body.String())
			}
			if c.calls == 1 && (gotEmail != "Owner@Example.io" || gotToken != token) {
				t.Errorf("setter got (%q, %q), want the body's values", gotEmail, gotToken)
			}
			if c.want == http.StatusMethodNotAllowed && rec.Header().Get("Allow") != "POST" {
				t.Errorf("Allow = %q, want POST", rec.Header().Get("Allow"))
			}
			if strings.Contains(buf.String()+rec.Body.String(), token) {
				t.Errorf("the log or the answer carries the token")
			}
		})
	}
}
